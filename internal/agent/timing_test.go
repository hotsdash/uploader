package agent

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"
)

func TestClockOffset(t *testing.T) {
	var c clockOffset
	if c.field() != "" {
		t.Fatal("no offset before one is measured")
	}
	t0 := time.UnixMilli(1_000_000)
	c.measure(1_000_000+50+2500, t0, t0.Add(100*time.Millisecond)) // the dashboard read it at our +50 ms, 2.5 s ahead
	if c.field() != "2500" {
		t.Fatalf("got %q", c.field())
	}
	c.measure(0, t0, t0.Add(time.Second)) // an older dashboard sends no time: keep the last
	c.measure(9_999_999, t0, t0.Add(time.Minute))
	if c.field() != "2500" {
		t.Fatalf("an untrustworthy reply changed it: %q", c.field())
	}
}

func TestNoteSeenKeepsTheFirst(t *testing.T) {
	a := &Agent{}
	first := time.Date(2026, 11, 8, 14, 30, 2, 500e6, time.UTC)
	a.noteSeen("x", foundWatch, first)
	a.noteSeen("x", foundScan, first.Add(20*time.Second))
	at, how := a.seenFields("x")
	if at != "2026-11-08T14:30:02.500Z" || how != foundWatch {
		t.Fatalf("got %s %s", at, how)
	}
	a.forgetSeen("x")
	if at, how := a.seenFields("x"); at != "" || how != "" {
		t.Fatal("forgotten")
	}
}

// A scan uploads with when and how each replay was seen, when it was sent, and the clock offset from the check before.
func TestUploadCarriesTimings(t *testing.T) {
	var mu sync.Mutex
	got := map[string][]string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/replays/known":
			_ = json.NewEncoder(w).Encode(map[string]any{"known": []string{}, "now": time.Now().UnixMilli() + 3000})
		case "/api/replays":
			if err := r.ParseMultipartForm(32 << 20); err != nil {
				t.Errorf("multipart: %v", err)
			}
			mu.Lock()
			for k, v := range r.MultipartForm.Value {
				got[k] = v
			}
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{"results": []map[string]any{{"status": "known"}}})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	dir := t.TempDir()
	game := filepath.Join(dir, "Replays", "Multiplayer", "game.StormReplay")
	if err := os.MkdirAll(filepath.Dir(game), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(game, []byte("MPQ\x1a fake replay"), 0o644); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Minute)
	_ = os.Chtimes(game, old, old)
	a := NewAgent(Config{Server: srv.URL, Folders: []Folder{{Path: dir}}}, t.TempDir(), log.New(io.Discard, "", 0))
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(got["found"]) != 1 || got["found"][0] != foundStart {
		t.Fatalf("the first scan after starting: found=%v", got["found"])
	}
	if _, err := time.Parse(timeLayout, got["seen"][0]); err != nil {
		t.Fatalf("seen: %v", got["seen"])
	}
	if _, err := time.Parse(timeLayout, got["sentAt"][0]); err != nil {
		t.Fatalf("sentAt: %v", got["sentAt"])
	}
	if _, err := time.Parse(timeLayout, got["mtimes"][0]); err != nil {
		t.Fatalf("mtimes to the millisecond: %v", got["mtimes"])
	}
	off, _ := strconv.Atoi(got["clockOffsetMs"][0])
	if off < 2500 || off > 3500 {
		t.Fatalf("clock offset: %v", got["clockOffsetMs"])
	}
	if at, _ := a.seenFields(game); at != "" {
		t.Fatal("sent: no longer remembered")
	}
	if !a.scanned.Load() {
		t.Fatal("a scan has finished")
	}
}
