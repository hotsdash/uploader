package agent

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// asExampleDotCom gives a TLS test server a public name (its certificate is for example.com too): the address to call
// it by, and a client that reaches it there (and any plain http server as usual).
func asExampleDotCom(srv *httptest.Server) (string, *http.Client) {
	addr := srv.Listener.Addr().String()
	_, port, _ := net.SplitHostPort(addr)
	tr := srv.Client().Transport.(*http.Transport).Clone()
	tr.DialContext = func(ctx context.Context, network, a string) (net.Conn, error) {
		if strings.HasPrefix(a, "example.com:") {
			a = addr
		}
		return (&net.Dialer{}).DialContext(ctx, network, a)
	}
	tr.TLSClientConfig = &tls.Config{RootCAs: tr.TLSClientConfig.RootCAs}
	return "https://example.com:" + port, &http.Client{Transport: tr}
}

// A move to another site isn't followed by itself: it waits for the person, and the token stays put until then.
func TestMoveToAnotherSiteWaitsForThePerson(t *testing.T) {
	var sawToken bool
	newSite := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			sawToken = true
		}
		_ = json.NewEncoder(w).Encode(latestInfo{Version: "2099.01.01.0000"})
	}))
	defer newSite.Close()
	// another machine name than the old one's
	moved, client := asExampleDotCom(newSite)
	old := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(latestInfo{Version: "2099.01.01.0000", Server: moved})
	}))
	defer old.Close()
	dir := t.TempDir()
	a := NewAgent(Config{Server: old.URL, Token: "secret", Folders: []Folder{{Path: dir}}}, dir, log.New(io.Discard, "", 0))
	a.client = client
	var notes int
	a.Notify = func(string, string, string) { notes++ }

	if _, err := a.latest(); err != nil {
		t.Fatal(err)
	}
	if got := a.Config().Server; got != old.URL {
		t.Fatalf("switched to %s by itself", got)
	}
	if st := a.UpdateState(); st.MoveTo != moved {
		t.Fatalf("moveTo = %q, want %q", st.MoveTo, moved)
	}
	_, _ = a.latest()
	if notes != 1 {
		t.Fatalf("told %d times, want once", notes)
	}
	if sawToken {
		t.Fatal("the token went to the new site before the person said so")
	}

	// not now: stays, and that address isn't suggested again for a while
	a.DismissMove()
	_, _ = a.latest()
	if a.UpdateState().MoveTo != "" || a.Config().MoveDismissed != moved {
		t.Fatalf("dismissed move came back: %+v", a.UpdateState())
	}

	// a couple of weeks on it's suggested again (the dashboard may really have moved)
	a.Update(func(c *Config) { c.MoveDismissedAt = time.Now().Add(-moveSnooze - time.Hour).Unix() })
	a.upd.probed = nil // and its last try was long ago
	_, _ = a.latest()
	if err := a.AcceptMove(); err != nil {
		t.Fatal(err)
	}
	if got := a.Config().Server; got != moved {
		t.Fatalf("server = %s, want %s", got, moved)
	}
}

func TestSecureUpgradeAndSecureServer(t *testing.T) {
	for next, want := range map[string]bool{
		"https://HOME.example.org/hots":              true,
		"https://home.example.org/hots/":             true,
		"https://home.example.org:443/hots":          true,
		"https://home.example.org:8443/hots":         false, // another port: maybe another app
		"https://home.example.org/other":             false, // another path on the same proxy
		"https://home.example.org.evil.example/hots": false,
		"http://home.example.org/hots":               false,
	} {
		if got := secureUpgrade("http://home.example.org/hots", next); got != want {
			t.Errorf("secureUpgrade(%q) = %v, want %v", next, got, want)
		}
	}
	if !secureUpgrade("http://127.0.0.1:5000", "https://127.0.0.1:5000") {
		t.Error("same port, http to https")
	}
	for s, want := range map[string]bool{
		"https://hots.example.com":   true,
		"http://127.0.0.1:3000":      true,
		"http://localhost:3000/hots": true,
		"http://[::1]:3000":          true,
		"http://192.168.1.20:3000":   false,
		"http://home.example.org":    false,
		"ftp://hots.example.com":     false,
	} {
		if got := secureServer(s); got != want {
			t.Errorf("secureServer(%q) = %v, want %v", s, got, want)
		}
	}
}

func TestNoUpdatesOverPlainHTTP(t *testing.T) {
	dir := t.TempDir()
	a := NewAgent(Config{Server: "http://192.168.1.20:3000", Folders: []Folder{{Path: dir}}}, dir, log.New(io.Discard, "", 0))
	if _, err := a.download(&latestInfo{Version: "2099.01.01.0000"}); err != errInsecureUpdate {
		t.Fatalf("want errInsecureUpdate, got %v", err)
	}
	if st := a.UpdateState(); !st.Insecure || st.CanApply {
		t.Fatalf("state %+v", st)
	}
}

// The dashboard can't have the uploader knock on addresses of the computer or the network it's on.
func TestMoveTargetsMustBePublicNames(t *testing.T) {
	for host, want := range map[string]bool{
		"hotsdash.com":          true,
		"Hots.Example.COM.":     true,
		"xn--bcher-kva.example": true,
		"1password.example":     true,
		"localhost.example.com": true,
		"local.example.com":     true,
		"127.0.0.1":             false,
		"192.168.1.20":          false,
		"8.8.8.8":               false, // any IP address, public ones too
		"8.8.8.8.":              false,
		"::1":                   false,
		"::ffff:10.0.0.1":       false,
		"fe80::1%eth0":          false,
		"2606:4700:4700::1111":  false,
		"127.1":                 false,
		"127.0.0.0x1":           false,
		"2130706433":            false,
		"localhost":             false,
		"LOCALHOST.":            false,
		"app.localhost":         false,
		"nas.local":             false,
		"NAS.Local.":            false,
		"router":                false,
		"":                      false,
	} {
		if got := publicHost(host); got != want {
			t.Errorf("publicHost(%q) = %v, want %v", host, got, want)
		}
	}
}

// moveRecorder answers every request with "moved to *to", and records what else was asked for.
type moveRecorder struct {
	to     string
	probes []string
}

func (m *moveRecorder) RoundTrip(r *http.Request) (*http.Response, error) {
	body := `{}`
	if r.URL.Host == "dash.example.org" {
		body = `{"version":"2099.01.01.0000","server":` + jsonString(m.to) + `}`
	} else {
		m.probes = append(m.probes, r.URL.String())
	}
	return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
}

func TestFollowMoveProbesPublicNamesOnceADay(t *testing.T) {
	m := &moveRecorder{}
	dir := t.TempDir()
	a := NewAgent(Config{Server: "https://dash.example.org", Folders: []Folder{{Path: dir}}}, dir, log.New(io.Discard, "", 0))
	a.client = &http.Client{Transport: m}
	ask := func(to string) {
		t.Helper()
		m.to = to
		if _, err := a.latest(); err != nil {
			t.Fatal(err)
		}
	}
	for _, to := range []string{"https://127.0.0.1:8443", "https://10.0.0.1", "https://[::1]", "https://localhost", "https://printer.local/x"} {
		ask(to)
		if len(m.probes) != 0 || a.UpdateState().MoveTo != "" {
			t.Fatalf("%s: probed %v, moveTo %q", to, m.probes, a.UpdateState().MoveTo)
		}
	}

	ask("https://new.example.net")
	ask("https://new.example.net")
	ask("https://NEW.example.net/")
	if len(m.probes) != 1 || a.UpdateState().MoveTo != "https://new.example.net" {
		t.Fatalf("a public name, three times in a day: probed %v, moveTo %q", m.probes, a.UpdateState().MoveTo)
	}
	ask("https://other.example.net") // each address has its own day
	if len(m.probes) != 2 {
		t.Fatalf("another address: %v", m.probes)
	}
	for k := range a.upd.probed {
		a.upd.probed[k] = time.Now().Add(-moveProbeEvery - time.Minute)
	}
	ask("https://new.example.net")
	if len(m.probes) != 3 {
		t.Fatalf("a day later: %v", m.probes)
	}
}

func TestMoveProbeIsNotRedirectedElsewhere(t *testing.T) {
	var elsewhere int
	other := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { elsewhere++ }))
	defer other.Close()
	newSite := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+"/inside", http.StatusFound)
	}))
	defer newSite.Close()
	moved, client := asExampleDotCom(newSite)
	dir := t.TempDir()
	a := NewAgent(Config{Server: "https://dash.example.org", Folders: []Folder{{Path: dir}}}, dir, log.New(io.Discard, "", 0))
	a.client = client
	a.followMove(moved)
	if elsewhere != 0 || a.UpdateState().MoveTo != "" {
		t.Fatalf("followed %d times, moveTo %q", elsewhere, a.UpdateState().MoveTo)
	}
}
