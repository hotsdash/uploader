package netfix

import (
	"context"
	"errors"
	"testing"
)

// Only the hosts Allow names are gone around.
func TestWorkaroundOnlyForAllowedHosts(t *testing.T) {
	f, _, _ := interceptSetup(t, nil) // every normal connection is answered by a filter
	var lookups []string
	f.resolve = func(_ context.Context, host string) (route, error) {
		lookups = append(lookups, host) // a lookup means it tried to go around
		return route{}, errors.New("no lookups in tests")
	}
	f.Allow = func(host string) bool { return host == "hotsdash.com" }
	try := func(host string) error {
		_, err := f.RoundTrip(mustReq(t, "GET", "https://"+host+"/api/x", ""))
		if err == nil {
			t.Fatalf("%s: the filter should have been in the way", host)
		}
		return err
	}
	for _, host := range []string{"example.com", "dash.hotsdash.com", "github.com"} {
		if err := try(host); errors.Is(err, ErrBlocked) || !intercepted(err) {
			t.Errorf("%s: %v", host, err)
		}
	}
	if len(lookups) != 0 {
		t.Fatalf("went around for %v", lookups)
	}
	if err := try("hotsdash.com"); !errors.Is(err, ErrBlocked) || len(lookups) != 1 || lookups[0] != "hotsdash.com" {
		t.Fatalf("the allowed host: %v, lookups %v", err, lookups)
	}

	f.Allow = nil
	if err := try("hotsdash.com"); errors.Is(err, ErrBlocked) || !intercepted(err) || len(lookups) != 1 {
		t.Fatalf("no hosts allowed: %v, lookups %v", err, lookups)
	}
}

// Switching it off also stops using a route already found.
func TestWorkaroundSwitchedOffDropsTheFoundRoute(t *testing.T) {
	f, u, hits := interceptSetup(t, func(context.Context, string) (route, error) {
		return route{ips: []string{"127.0.0.1"}}, nil
	})
	on := true
	f.Allow = func(string) bool { return on }
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
