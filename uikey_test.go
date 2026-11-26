package main

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
)

// The settings page needs the key (in the address for the page, in X-Key for the rest) and a loopback Host.
func TestSettingsPageNeedsTheKeyAndTheHost(t *testing.T) {
	const key = "0123456789abcdef0123456789abcdef"
	u := &ui{key: key, port: 5000}
	mux := http.NewServeMux()
	mux.HandleFunc("/", u.page)
	mux.HandleFunc("/api/x", u.api(func(*http.Request) (any, error) { return true, nil }))
	h := u.guard(mux)
	for _, host := range []string{"127.0.0.1:" + strconv.Itoa(u.port), "localhost:5000", "evil.example:5000", "127.0.0.1:5001"} {
		for _, k := range []string{key, "", key[:31], key + "0", "1" + key[1:]} {
			want := k == key && (host == "127.0.0.1:5000" || host == "localhost:5000")
			page := httptest.NewRequest("GET", "/?k="+k, nil)
			api := httptest.NewRequest("GET", "/api/x", nil)
			api.Header.Set("X-Key", k)
			for _, r := range []*http.Request{page, api} {
				r.Host = host
				w := httptest.NewRecorder()
				h.ServeHTTP(w, r)
				if (w.Code == http.StatusOK) != want {
					t.Errorf("%s, host %s, key %q: %d", r.URL.Path, host, k, w.Code)
				}
			}
		}
	}
}
