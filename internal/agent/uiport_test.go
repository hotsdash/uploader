package agent

import (
	"testing"
	"time"
)

// A copy that was serving on a port browsers refuse doesn't hand it on: the new one moves, with a new key.
func TestStartUIDoesNotReuseAPortBrowsersRefuse(t *testing.T) {
	prev := &UIHandoff{Port: 6669, Key: "0123456789abcdef0123456789abcdef", PageOpen: true, At: time.Now()}
	u, err := StartUI(nil, prev)
	if err != nil {
		t.Fatal(err)
	}
	if u.Reused(prev) || u.port <= 10080 || u.key == prev.Key {
		t.Fatalf("port %d, same key %v", u.port, u.key == prev.Key)
	}
}

func TestBrowserSafePort(t *testing.T) {
	for port, want := range map[int]bool{6669: false, 10080: false, 10081: true, 49152: true} {
		if got := browserSafePort(port); got != want {
			t.Errorf("browserSafePort(%d) = %v", port, got)
		}
	}
}

func TestStartUIAlwaysUsesAPortBrowsersOpen(t *testing.T) {
	for range 10 {
		u, err := StartUI(nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		if !browserSafePort(u.port) {
			t.Fatalf("port %d", u.port)
		}
	}
}
