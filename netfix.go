package main

// Some networks send hotsdash.com somewhere that isn't the dashboard: ISP web filters, router or
// Pi-hole rules, some VPNs and antivirus web protection answer the HTTPS port with a plain-HTTP block
// page, and Go fails with "tls: first record does not look like a TLS handshake".
//
// Those filters work one of two ways. Some lie in DNS; for those, fallbackTransport looks the name up
// over DNS-over-HTTPS (Cloudflare, then Google), reached by IP so the local DNS isn't asked, and
// connects to the address it gets. Others read the name the uploader sends at the start of the TLS
// handshake (SNI) and step in whatever the address; for those, the fallback connection uses Encrypted
// Client Hello, which the dashboard's Cloudflare front supports: the name goes inside the encrypted
// part and the network only sees "cloudflare-ech.com". The ECH key comes from the same DNS-over-HTTPS
// lookup (the HTTPS record), or a built-in one that Cloudflare replaces with its current key on first
// contact if it is out of date.
//
// This is only done for hotsdash.com, and can be switched off in Settings (Agent.goAround).
//
// The dashboard's real certificate is still checked for the real name, so this only ever reaches the
// genuine dashboard. If none of it works, the error says in plain words that the network is in the way,
// with what was tried, instead of the raw Go error.

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// how long an address found over DNS-over-HTTPS is used before the normal route is tried again
const pinFor = time.Hour

// echSeed: the dashboard's ECH key as published in its HTTPS DNS record, for when no DNS-over-HTTPS
// server can be reached. Cloudflare rotates these keys, but answers an old one with its current key
// (the "retry config"), which the uploader then uses, so a stale seed still gets through.
var echSeed = map[string]string{
	"hotsdash.com": "AEX+DQBBAgAgACDt7x3BsJGUM8EGcp3IxkIFKyy/Mgk6MLzoEE2gWqtmcAAEAAEAAQASY2xvdWRmbGFyZS1lY2guY29tAAA=",
}

// errBlocked is wrapped by the error returned when the network keeps answering for the dashboard.
var errBlocked = errors.New("network blocking the dashboard")

type blockedError struct {
	host string
	err  error // what the normal route got
	via  error // why going around it didn't work
}

func (e *blockedError) Error() string {
	detail := e.err.Error()
	if e.via != nil {
		detail += "; going around it: " + e.via.Error()
	}
	return fmt.Sprintf("Your network isn't letting the uploader reach %s: something else answered instead "+
		"(often an internet provider's web filter, a VPN or antivirus web protection). "+
		"Allow %s there, or switch your DNS to 1.1.1.1. (%s)", e.host, e.host, detail)
}
func (e *blockedError) Unwrap() []error { return []error{errBlocked, e.err} }

// intercepted: something answered the HTTPS port without speaking TLS.
func intercepted(err error) bool {
	var rh tls.RecordHeaderError
	return errors.As(err, &rh)
}

// route: where to connect for a host, and its ECH key if it has one.
type route struct {
	ips []string
	ech []byte // ECHConfigList; nil for a plain handshake
}

type pin struct {
	route
	until time.Time
}

type fallbackTransport struct {
	base    http.RoundTripper
	direct  *http.Transport // dials the pinned addresses
	resolve func(ctx context.Context, host string) (route, error)
	now     func() time.Time
	// allow: which hosts may be gone around; every other request goes the normal way only. nil allows none.
	allow func(host string) bool

	mu   sync.Mutex
	pins map[string]pin // host -> route found when the normal one was intercepted

	OnFallback func(host string, ips []string, ech bool) // for the uploader's log
}

func newFallbackTransport() *fallbackTransport {
	base := http.DefaultTransport.(*http.Transport).Clone()
	return newFallbackTransportWith(base, nil, lookupRoute)
}

// tlsCfg is for tests (a test certificate); nil uses the system's.
func newFallbackTransportWith(base http.RoundTripper, tlsCfg *tls.Config,
	resolve func(context.Context, string) (route, error)) *fallbackTransport {
	f := &fallbackTransport{base: base, resolve: resolve, now: time.Now, pins: map[string]pin{}}
	d := &net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}
	f.direct = &http.Transport{
		Proxy:               nil, // a broken proxy setting can be the very thing in the way
		ForceAttemptHTTP2:   true,
		TLSHandshakeTimeout: 10 * time.Second,
		IdleConnTimeout:     90 * time.Second,
		// the handshake is done here so each host gets its own ECH key
		DialTLSContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, err
			}
			p, ok := f.pinned(host)
			if !ok || len(p.ips) == 0 {
				return nil, fmt.Errorf("no address for %s", host)
			}
			var last error
			for _, ip := range p.ips {
				c, err := f.handshake(ctx, d, network, net.JoinHostPort(ip, port), host, p.ech, tlsCfg)
				if err == nil {
					return c, nil
				}
				last = err
			}
			return nil, last
		},
	}
	return f
}

func (f *fallbackTransport) handshake(ctx context.Context, d *net.Dialer, network, addr, host string,
	ech []byte, base *tls.Config) (net.Conn, error) {
	for attempt := 0; ; attempt++ {
		raw, err := d.DialContext(ctx, network, addr)
		if err != nil {
			return nil, err
		}
		cfg := &tls.Config{}
		if base != nil {
			cfg = base.Clone()
		}
		cfg.ServerName = host
		cfg.NextProtos = []string{"h2", "http/1.1"}
		if len(ech) > 0 {
			cfg.EncryptedClientHelloConfigList = ech
			cfg.MinVersion = tls.VersionTLS13 // required for ECH
		}
		c := tls.Client(raw, cfg)
		err = c.HandshakeContext(ctx)
		if err == nil {
			return c, nil
		}
		raw.Close()
		// an out-of-date ECH key: the server sent its current one, so use that (once)
		var rej *tls.ECHRejectionError
		if errors.As(err, &rej) && len(rej.RetryConfigList) > 0 && attempt == 0 {
			ech = rej.RetryConfigList
			f.mu.Lock()
			if p, ok := f.pins[host]; ok {
				p.ech = ech
				f.pins[host] = p
			}
			f.mu.Unlock()
			continue
		}
		return nil, err
	}
}

func (f *fallbackTransport) pinned(host string) (pin, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p, ok := f.pins[host]
	if !ok || f.now().After(p.until) {
		delete(f.pins, host)
		return pin{}, false
	}
	return p, true
}

func (f *fallbackTransport) unpin(host string) {
	f.mu.Lock()
	delete(f.pins, host)
	f.mu.Unlock()
}

func (f *fallbackTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	host := req.URL.Hostname()
	if req.URL.Scheme != "https" || net.ParseIP(host) != nil || f.allow == nil || !f.allow(host) {
		return f.base.RoundTrip(req)
	}
	if _, ok := f.pinned(host); ok {
		res, err := f.direct.RoundTrip(req)
		if err == nil {
			return res, nil
		}
		// the found route stopped working: forget it and go the normal way
		f.unpin(host)
		if r2, ok := rewind(req); ok {
			req = r2
		} else {
			return nil, err
		}
	}
	res, err := f.base.RoundTrip(req)
	if err == nil || !intercepted(err) {
		return res, err
	}
	ctx, cancel := context.WithTimeout(req.Context(), 15*time.Second)
	rt, rerr := f.resolve(ctx, host)
	cancel()
	if rerr != nil || len(rt.ips) == 0 {
		if rerr == nil {
			rerr = errors.New("no address found")
		}
		return nil, &blockedError{host: host, err: err, via: rerr}
	}
	f.mu.Lock()
	f.pins[host] = pin{route: rt, until: f.now().Add(pinFor)}
	f.mu.Unlock()
	if f.OnFallback != nil {
		f.OnFallback(host, rt.ips, len(rt.ech) > 0)
	}
	r2, ok := rewind(req)
	if !ok {
		return nil, err // can't send this one again; the next request uses the route found
	}
	res, derr := f.direct.RoundTrip(r2)
	if derr != nil {
		f.unpin(host)
		return nil, &blockedError{host: host, err: err, via: derr}
	}
	return res, nil
}

// rewind: a copy of the request with its body from the start, if it can be sent again.
func rewind(req *http.Request) (*http.Request, bool) {
	r2 := req.Clone(req.Context())
	if req.Body == nil || req.Body == http.NoBody {
		return r2, true
	}
	if req.GetBody == nil {
		return nil, false
	}
	b, err := req.GetBody()
	if err != nil {
		return nil, false
	}
	r2.Body = b
	return r2, true
}

// ---------- finding the real route ----------

// lookupRoute: addresses and ECH key over DNS-over-HTTPS; if no DoH server answers (they can be
// blocked too), the system's DNS for the addresses and the built-in ECH key. A filter that only
// reads the name in the handshake leaves DNS alone, so that still gets through with ECH.
func lookupRoute(ctx context.Context, host string) (route, error) {
	rt, err := dohResolve(ctx, host)
	if err == nil {
		if rt.ech == nil {
			rt.ech = seedECH(host)
		}
		return rt, nil
	}
	ips, serr := systemResolve(ctx, host)
	if serr != nil || len(ips) == 0 {
		return route{}, fmt.Errorf("DNS-over-HTTPS: %v", err)
	}
	ech := seedECH(host)
	if ech == nil {
		// without ECH, connecting to the same address the normal way already did won't help
		return route{}, fmt.Errorf("DNS-over-HTTPS: %v", err)
	}
	return route{ips: ips, ech: ech}, nil
}

func seedECH(host string) []byte {
	if s, ok := echSeed[host]; ok {
		if b, err := base64.StdEncoding.DecodeString(s); err == nil {
			return b
		}
	}
	return nil
}

func systemResolve(ctx context.Context, host string) ([]string, error) {
	addrs, err := net.DefaultResolver.LookupIP(ctx, "ip4", host)
	if err != nil {
		return nil, err
	}
	var ips []string
	for _, ip := range addrs {
		if publicV4(ip) {
			ips = append(ips, ip.String())
		}
	}
	return ips, nil
}

func publicV4(ip net.IP) bool {
	return ip != nil && ip.To4() != nil && !ip.IsPrivate() && !ip.IsLoopback() && !ip.IsUnspecified()
}

// reached by IP (their certificates cover these addresses), so the local DNS isn't involved
var dohServers = []string{
	"https://1.1.1.1/dns-query",
	"https://8.8.8.8/resolve",
}

var dohClient = &http.Client{
	Timeout:   8 * time.Second,
	Transport: &http.Transport{Proxy: nil, TLSHandshakeTimeout: 8 * time.Second},
}

func dohResolve(ctx context.Context, host string) (route, error) {
	var errs []string
	for _, s := range dohServers {
		ips, err := dohQuery(ctx, s, host, "A")
		if err == nil && len(ips) == 0 {
			err = errors.New("no addresses")
		}
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", s, err))
			continue
		}
		rt := route{ips: ips}
		// the ECH key is a bonus: without it, a filter that only lies in DNS is still got around
		if recs, err := dohQuery(ctx, s, host, "HTTPS"); err == nil {
			rt.ech = echFromHTTPS(recs)
		}
		return rt, nil
	}
	return route{}, errors.New(strings.Join(errs, "; "))
}

// dohQuery returns the A addresses (public only), or the HTTPS records' text.
func dohQuery(ctx context.Context, server, host, typ string) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", server+"?type="+typ+"&name="+url.QueryEscape(host), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/dns-json")
	res, err := dohClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return nil, fmt.Errorf("%s", res.Status)
	}
	if typ == "HTTPS" {
		return parseDoHType(res.Body, host, 65)
	}
	return parseDoH(res.Body, host)
}

func parseDoH(r io.Reader, host string) ([]string, error) {
	recs, err := parseDoHType(r, host, 1) // A
	if err != nil {
		return nil, err
	}
	var ips []string
	for _, d := range recs {
		// skip anything private (a filter answering over DoH too)
		if ip := net.ParseIP(d); publicV4(ip) {
			ips = append(ips, ip.String())
		}
	}
	return ips, nil
}

func parseDoHType(r io.Reader, host string, typ int) ([]string, error) {
	var v struct {
		Status int
		Answer []struct {
			Type int    `json:"type"`
			Data string `json:"data"`
		}
	}
	if err := json.NewDecoder(r).Decode(&v); err != nil {
		return nil, err
	}
	if v.Status != 0 {
		return nil, fmt.Errorf("lookup %s: status %d", host, v.Status)
	}
	var out []string
	for _, a := range v.Answer {
		if a.Type == typ {
			out = append(out, strings.TrimSpace(a.Data))
		}
	}
	return out, nil
}

// echFromHTTPS: the ech= value of an HTTPS record in presentation form, e.g.
// `1 . alpn=h3,h2 ipv4hint=104.21.49.166 ech=AEX+DQ...AA== ipv6hint=...`
func echFromHTTPS(recs []string) []byte {
	for _, r := range recs {
		for _, f := range strings.Fields(r) {
			if v, ok := strings.CutPrefix(f, "ech="); ok {
				if b, err := base64.StdEncoding.DecodeString(strings.Trim(v, `"`)); err == nil && len(b) > 0 {
					return b
				}
			}
		}
	}
	return nil
}
