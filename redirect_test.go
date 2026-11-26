package main

import (
	"context"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// redirectSite is a dashboard called example.com on the standard ports: https and plain http, each answering with
// its own handler, and an agent signed in to it. seen is every request that arrived, with the token it carried.
func redirectSite(t *testing.T, secure, plain http.HandlerFunc) (a *Agent, seen func() []string) {
	t.Helper()
	var mu sync.Mutex
	var got []string
	record := func(scheme string, h http.HandlerFunc) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			got = append(got, scheme+"://"+r.Host+r.URL.Path+" "+r.Header.Get("Authorization"))
			mu.Unlock()
			h(w, r)
		})
	}
	s := httptest.NewTLSServer(record("https", secure))
	t.Cleanup(s.Close)
	p := httptest.NewServer(record("http", plain))
	t.Cleanup(p.Close)
	a = NewAgent(Config{Server: "https://example.com", Token: "secret", Folders: []Folder{{Path: t.TempDir()}}}, t.TempDir(), log.New(io.Discard, "", 0))
	tr := s.Client().Transport.(*http.Transport).Clone() // trusts the test certificate, which is for example.com
	tr.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		to := s.Listener.Addr().String()
		if strings.HasSuffix(addr, ":80") {
			to = p.Listener.Addr().String()
		}
		return (&net.Dialer{}).DialContext(ctx, network, to)
	}
	a.client.Transport = tr // the agent's own client, so its redirect rule is what's tested
	return a, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), got...)
	}
}

func okJSON(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, `{"ok":true}`) }

func redirectTo(u string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, u, http.StatusTemporaryRedirect) }
}

func TestAPIRedirectToPlainHTTPNeverCarriesTheToken(t *testing.T) {
	a, seen := redirectSite(t, redirectTo("http://example.com/api/x"), okJSON)
	err := a.call("GET", "x", nil, "", nil)
	for _, s := range seen() {
		if strings.HasPrefix(s, "http://") {
			t.Errorf("followed to plain http: %q", s)
		}
	}
	if err == nil {
		t.Fatal("the redirect should be an error")
	}
}

func TestAPIRedirectToAnotherHostIsRefused(t *testing.T) {
	for _, to := range []string{"https://other.example.com/api/x", "https://sub.example.com/api/x", "https://example.com:8443/api/x"} {
		a, seen := redirectSite(t, redirectTo(to), okJSON)
		if err := a.call("GET", "x", nil, "", nil); err == nil || len(seen()) != 1 {
			t.Errorf("%s: err %v, requests %q", to, err, seen())
		}
	}
}

func TestAPIRedirectOnTheSameHostIsFollowed(t *testing.T) {
	a, seen := redirectSite(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/x" {
			redirectTo("https://example.com:443/api/y")(w, r)
			return
		}
		okJSON(w, r)
	}, okJSON)
	var out struct{ OK bool }
	if err := a.call("GET", "x", nil, "", &out); err != nil || !out.OK {
		t.Fatalf("err %v, ok %v", err, out.OK)
	}
	if got := seen(); len(got) != 2 || got[1] != "https://example.com:443/api/y Bearer secret" {
		t.Fatalf("requests %q", got)
	}
}

func TestAPIRedirectRule(t *testing.T) {
	req := func(u string) *http.Request { return mustReq(t, "GET", u, "") }
	for _, c := range []struct {
		from, to string
		ok       bool
	}{
		{"http://example.com/a", "https://example.com/b", true},
		{"http://example.com:80/a", "https://EXAMPLE.com:443/b", true},
		{"http://127.0.0.1:5000/a", "https://127.0.0.1:5000/b", true},
		{"https://example.com/a", "https://example.com:443/b", true},
		{"https://example.com/a", "http://example.com/b", false},
		{"https://example.com:8443/a", "http://example.com:8443/b", false},
		{"http://example.com/a", "https://example.com:8443/b", false},
		{"https://example.com/a", "https://example.org/b", false},
	} {
		if err := apiRedirect(req(c.to), []*http.Request{req(c.from)}); (err == nil) != c.ok {
			t.Errorf("%s -> %s: %v", c.from, c.to, err)
		}
	}
	// once upgraded, not back down
	if apiRedirect(req("http://example.com/c"), []*http.Request{req("http://example.com/a"), req("https://example.com/b")}) == nil {
		t.Error("downgrade after an upgrade allowed")
	}
	var via []*http.Request
	for range 5 {
		via = append(via, req("https://example.com/a"))
	}
	if err := apiRedirect(req("https://example.com/a"), via); err != nil {
		t.Errorf("fifth redirect: %v", err)
	}
	if apiRedirect(req("https://example.com/a"), append(via, req("https://example.com/a"))) == nil {
		t.Error("sixth redirect allowed")
	}
}
