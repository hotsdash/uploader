package main

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
	// the test certificate is for example.com too: call the new site that, so it's another machine name
	addr := newSite.Listener.Addr().String()
	_, port, _ := net.SplitHostPort(addr)
	moved := "https://example.com:" + port
	old := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(latestInfo{Version: "2099.01.01.0000", Server: moved})
	}))
	defer old.Close()
	dir := t.TempDir()
	a := NewAgent(Config{Server: old.URL, Token: "secret", Folders: []Folder{{Path: dir}}}, dir, log.New(io.Discard, "", 0))
	tr := newSite.Client().Transport.(*http.Transport).Clone()
	tr.DialContext = func(ctx context.Context, network, a string) (net.Conn, error) {
		if strings.HasPrefix(a, "example.com:") {
			a = addr
		}
		return (&net.Dialer{}).DialContext(ctx, network, a)
	}
	tr.TLSClientConfig = &tls.Config{RootCAs: tr.TLSClientConfig.RootCAs}
	a.client = &http.Client{Transport: tr}
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
