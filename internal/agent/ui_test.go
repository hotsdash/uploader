package agent

import (
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestHandoffRoundTrip(t *testing.T) {
	dir := t.TempDir()
	h := UIHandoff{Port: 51234, Key: "0123456789abcdef0123456789abcdef", PageOpen: true, At: time.Now()}
	if err := WriteHandoff(dir, h); err != nil {
		t.Fatal(err)
	}
	got := TakeHandoff(dir)
	if got == nil || got.Port != h.Port || got.Key != h.Key || !got.PageOpen {
		t.Fatalf("got %+v", got)
	}
	if _, err := os.Stat(filepath.Join(dir, handoffFile)); !os.IsNotExist(err) {
		t.Fatal("handoff should be removed once taken")
	}
	if TakeHandoff(dir) != nil {
		t.Fatal("handoff should only be taken once")
	}
}

func TestHandoffIgnoresStaleOrBroken(t *testing.T) {
	dir := t.TempDir()
	_ = WriteHandoff(dir, UIHandoff{Port: 51234, Key: "0123456789abcdef0123456789abcdef", At: time.Now().Add(-time.Hour)})
	if TakeHandoff(dir) != nil {
		t.Fatal("an hour-old handoff is from some other run")
	}
	_ = WriteHandoff(dir, UIHandoff{Port: 51234, Key: "short", At: time.Now()})
	if TakeHandoff(dir) != nil {
		t.Fatal("a bad key is ignored")
	}
}

func TestStartUIReusesThePreviousAddress(t *testing.T) {
	first, err := StartUI(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	// pretend the old copy has exited: free its port by starting from a closed listener on a known free port
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	prev := &UIHandoff{Port: port, Key: first.key, At: time.Now()}
	u, err := StartUI(nil, prev)
	if err != nil {
		t.Fatal(err)
	}
	if !u.Reused(prev) || u.port != port || u.key != first.key {
		t.Fatalf("wanted port %d and the same key, got port %d", port, u.port)
	}
}

func TestStartUIFallsBackWhenThePortIsTaken(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	defer ln.Close()
	prev := &UIHandoff{Port: ln.Addr().(*net.TCPAddr).Port, Key: "0123456789abcdef0123456789abcdef", At: time.Now()}
	u, err := StartUI(nil, prev)
	if err != nil {
		t.Fatal(err)
	}
	if u.Reused(prev) || u.key == prev.Key {
		t.Fatal("a new address should get a new key")
	}
}

func TestHandoffKnowsWhetherThePageIsOpen(t *testing.T) {
	u := &ui{key: "k"}
	if u.Handoff().PageOpen {
		t.Fatal("never asked: not open")
	}
	h := u.api(func(*http.Request) (any, error) { return map[string]bool{"ok": true}, nil })
	req := httptest.NewRequest("GET", "/api/state", nil)
	req.Header.Set("X-Key", "k")
	h(httptest.NewRecorder(), req)
	if !u.Handoff().PageOpen {
		t.Fatal("just asked: open")
	}
	u.seen.Store(time.Now().Add(-time.Minute).UnixMilli())
	if u.Handoff().PageOpen {
		t.Fatal("a minute ago: closed")
	}
}
