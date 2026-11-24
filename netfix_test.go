package main

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// a network that answers the dashboard's HTTPS port with a plain-HTTP block page,
// and the real dashboard (a TLS server) reachable at an address only DNS-over-HTTPS knows
func interceptSetup(t *testing.T, resolve func(context.Context, string) (route, error)) (*fallbackTransport, string, *atomic.Int32) {
	t.Helper()
	filter := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "blocked by your provider")
	}))
	t.Cleanup(filter.Close)
	var hits atomic.Int32
	real := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		b, _ := io.ReadAll(r.Body)
		io.WriteString(w, "dashboard:"+string(b))
	}))
	t.Cleanup(real.Close)
	_, port, _ := net.SplitHostPort(strings.TrimPrefix(real.URL, "https://"))

	cfg := real.Client().Transport.(*http.Transport).TLSClientConfig.Clone()
	base := &http.Transport{
		TLSClientConfig: cfg,
		// the local DNS sends every name to the filter
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, strings.TrimPrefix(filter.URL, "http://"))
		},
	}
	f := newFallbackTransportWith(base, cfg, resolve)
	// the test certificate is for example.com
	return f, "https://example.com:" + port, &hits
}

func TestInterceptedDetected(t *testing.T) {
	f, u, _ := interceptSetup(t, func(context.Context, string) (route, error) { return route{}, errors.New("no") })
	_, err := f.base.RoundTrip(mustReq(t, "GET", u+"/api/x", ""))
	if !intercepted(err) {
		t.Fatalf("want an intercepted error, got %v", err)
	}
}

func TestFallbackGoesAround(t *testing.T) {
	var lookups atomic.Int32
	f, u, hits := interceptSetup(t, func(_ context.Context, host string) (route, error) {
		lookups.Add(1)
		if host != "example.com" {
			t.Errorf("looked up %q", host)
		}
		return route{ips: []string{"127.0.0.1"}}, nil
	})
	var logged string
	f.OnFallback = func(host string, ips []string, ech bool) { logged = host }
	c := &http.Client{Transport: f}

	// a POST with a body is sent again after the lookup
	res, err := c.Post(u+"/api/agent/link", "application/json", strings.NewReader(`{"a":1}`))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if string(b) != `dashboard:{"a":1}` {
		t.Fatalf("got %q", b)
	}
	if logged != "example.com" {
		t.Fatalf("fallback not reported: %q", logged)
	}
	// later requests use the address found, without asking again
	for range 3 {
		res, err := c.Get(u + "/api/x")
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
	}
	if lookups.Load() != 1 || hits.Load() != 4 {
		t.Fatalf("lookups %d, hits %d", lookups.Load(), hits.Load())
	}
	// after pinFor, the normal route is tried again (and is still intercepted)
	f.now = func() time.Time { return time.Now().Add(pinFor + time.Minute) }
	if res, err := c.Get(u + "/api/x"); err != nil {
		t.Fatal(err)
	} else {
		res.Body.Close()
	}
	if lookups.Load() != 2 {
		t.Fatalf("want a second lookup, got %d", lookups.Load())
	}
}

func TestFallbackFailsInPlainWords(t *testing.T) {
	f, u, _ := interceptSetup(t, func(context.Context, string) (route, error) {
		return route{}, errors.New("doh unreachable")
	})
	_, err := (&http.Client{Transport: f}).Get(u + "/api/x")
	if !errors.Is(err, errBlocked) {
		t.Fatalf("want errBlocked, got %v", err)
	}
	if !strings.Contains(err.Error(), "Your network isn't letting the uploader reach example.com") ||
		!strings.Contains(err.Error(), "going around it: doh unreachable") {
		t.Fatalf("message: %v", err)
	}
	var ue *url.Error
	if !errors.As(err, &ue) {
		t.Fatalf("still a url.Error: %T", err)
	}
}

func TestNormalNetworkUntouched(t *testing.T) {
	real := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "ok") }))
	defer real.Close()
	f := newFallbackTransportWith(real.Client().Transport, nil, func(context.Context, string) (route, error) {
		t.Fatal("no lookup expected")
		return route{}, nil
	})
	res, err := (&http.Client{Transport: f}).Get(real.URL)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if string(b) != "ok" {
		t.Fatalf("got %q", b)
	}
}

func TestParseDoH(t *testing.T) {
	body := `{"Status":0,"Answer":[{"type":5,"data":"x.example."},{"type":1,"data":"104.21.1.2"},` +
		`{"type":1,"data":"192.168.0.10"},{"type":1,"data":"172.67.3.4"}]}`
	ips, err := parseDoH(strings.NewReader(body), "hotsdash.com")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(ips, ",") != "104.21.1.2,172.67.3.4" {
		t.Fatalf("got %v", ips)
	}
	if _, err := parseDoH(strings.NewReader(`{"Status":3}`), "hotsdash.com"); err == nil {
		t.Fatal("NXDOMAIN should be an error")
	}
}

func TestProblemTextBlocked(t *testing.T) {
	dir := t.TempDir()
	a := NewAgent(Config{Server: "https://hotsdash.com", Folders: []Folder{{Path: dir}}}, dir, log.New(io.Discard, "", 0))
	a.problem(&blockedError{host: "hotsdash.com", err: tls.RecordHeaderError{Msg: "first record does not look like a TLS handshake"}})
	if !strings.Contains(a.Status().Text, "network is blocking") {
		t.Fatalf("status %q", a.Status().Text)
	}
}

func mustReq(t *testing.T, method, u, body string) *http.Request {
	t.Helper()
	r, err := http.NewRequest(method, u, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	return r
}
