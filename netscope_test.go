package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The workaround is for hotsdash.com only, and only while it's switched on.
func TestWorkaroundOnlyForTheDefaultDashboard(t *testing.T) {
	a := NewAgent(Config{Server: "https://hotsdash.com", Folders: []Folder{{Path: t.TempDir()}}}, t.TempDir(), log.New(io.Discard, "", 0))
	f := a.client.Transport.(*fallbackTransport)
	// every normal connection is answered by a filter; a lookup means the uploader tried to go around it
	filtered, _, _ := interceptSetup(t, nil)
	f.base = filtered.base
	var lookups []string
	f.resolve = func(_ context.Context, host string) (route, error) {
		lookups = append(lookups, host)
		return route{}, errors.New("no lookups in tests")
	}
	try := func(host string) error {
		_, err := f.RoundTrip(mustReq(t, "GET", "https://"+host+"/api/x", ""))
		if err == nil {
			t.Fatalf("%s: the filter should have been in the way", host)
		}
		return err
	}
	for _, host := range []string{"example.com", "dash.hotsdash.com", "hotsdash.com.example.com", "github.com"} {
		if err := try(host); errors.Is(err, errBlocked) || !intercepted(err) {
			t.Errorf("%s: %v", host, err)
		}
	}
	if len(lookups) != 0 {
		t.Fatalf("went around for %v", lookups)
	}
	if err := try("hotsdash.com"); !errors.Is(err, errBlocked) || len(lookups) != 1 || lookups[0] != "hotsdash.com" {
		t.Fatalf("the default dashboard: %v, lookups %v", err, lookups)
	}

	a.Update(func(c *Config) { c.NoNetworkWorkaround = true })
	if err := try("hotsdash.com"); errors.Is(err, errBlocked) || !intercepted(err) || len(lookups) != 1 {
		t.Fatalf("switched off: %v, lookups %v", err, lookups)
	}
}

// Switching it off also stops using a route already found.
func TestWorkaroundSwitchedOffDropsTheFoundRoute(t *testing.T) {
	f, u, hits := interceptSetup(t, func(context.Context, string) (route, error) {
		return route{ips: []string{"127.0.0.1"}}, nil
	})
	on := true
	f.allow = func(string) bool { return on }
	res, err := f.RoundTrip(mustReq(t, "GET", u+"/api/x", ""))
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	on = false
	if _, err := f.RoundTrip(mustReq(t, "GET", u+"/api/x", "")); !intercepted(err) || hits.Load() != 1 {
		t.Fatalf("err %v, hits %d", err, hits.Load())
	}
}

func TestWorkaroundSetting(t *testing.T) {
	// settings saved before there was a choice keep it on
	var old Config
	if err := json.Unmarshal([]byte(`{"server":"https://hotsdash.com","token":"t"}`), &old); err != nil || old.NoNetworkWorkaround {
		t.Fatalf("err %v, %+v", err, old)
	}
	if b, _ := json.Marshal(old); strings.Contains(string(b), "noNetworkWorkaround") {
		t.Fatalf("saved when on: %s", b)
	}

	a := NewAgent(old, t.TempDir(), log.New(io.Discard, "", 0))
	u := &ui{a: a}
	for _, on := range []bool{false, true} {
		body, _ := json.Marshal(map[string]bool{"on": on})
		if _, err := u.network(httptest.NewRequest(http.MethodPost, "/api/network", strings.NewReader(string(body)))); err != nil {
			t.Fatal(err)
		}
		if a.Config().NoNetworkWorkaround == on || a.goAround("hotsdash.com") != on || a.goAround("HotsDash.com") != on {
			t.Fatalf("on=%v: %+v", on, a.Config())
		}
	}
}
