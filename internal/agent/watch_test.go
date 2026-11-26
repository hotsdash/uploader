package agent

import (
	"io"
	"log"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestReadyReplays(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	write := func(name string, size int, age time.Duration) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, make([]byte, size), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, now.Add(-age), now.Add(-age)); err != nil {
			t.Fatal(err)
		}
		return p
	}
	done := write("done.StormReplay", 100, time.Minute)
	fresh := write("fresh.StormReplay", 100, 10*time.Second)
	writing := write("writing.StormReplay", 50, 500*time.Millisecond)
	write("notes.txt", 10, time.Minute)
	write("empty.StormReplay", 0, time.Minute)
	sent := func(p string) bool { return p == done }

	// first look: nothing to compare sizes with yet, so nothing is ready
	ready, sizes := readyReplays([]string{dir}, sent, nil, now)
	if len(ready) != 0 {
		t.Fatalf("first look: got %v", ready)
	}
	want := map[string]int64{fresh: 100, writing: 50}
	if !reflect.DeepEqual(sizes, want) {
		t.Fatalf("sizes: got %v want %v", sizes, want)
	}

	// next look: the settled one is ready; the one written half a second ago isn't
	ready, sizes = readyReplays([]string{dir}, sent, sizes, now)
	if !reflect.DeepEqual(ready, []string{fresh}) {
		t.Fatalf("second look: got %v", ready)
	}

	// still growing: not ready even once it's old enough
	write("writing.StormReplay", 80, 3*time.Second)
	ready, _ = readyReplays([]string{dir}, sent, sizes, now)
	if !reflect.DeepEqual(ready, []string{fresh}) {
		t.Fatalf("growing file: got %v", ready)
	}

	// a missing folder is skipped
	if r, s := readyReplays([]string{filepath.Join(dir, "nope")}, sent, nil, now); len(r) != 0 || len(s) != 0 {
		t.Fatalf("missing folder: %v %v", r, s)
	}
}

func TestReplayDirsOf(t *testing.T) {
	a := filepath.Join("x", "Multiplayer")
	b := filepath.Join("y", "Multiplayer")
	got := replayDirsOf([]string{filepath.Join(b, "2.StormReplay"), filepath.Join(a, "1.StormReplay"), filepath.Join(b, "3.StormReplay")})
	if !reflect.DeepEqual(got, []string{a, b}) {
		t.Fatalf("got %v", got)
	}
}

func TestWatchReplaysKicksOnce(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "new.StormReplay")
	if err := os.WriteFile(p, make([]byte, 100), 0o644); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Minute)
	_ = os.Chtimes(p, old, old)
	a := &Agent{files: map[string]*fileState{}, kick: make(chan struct{}, 1), log: log.New(io.Discard, "", 0), replayDirs: []string{dir}}
	kicked := func() bool {
		select {
		case <-a.kick:
			return true
		default:
			return false
		}
	}
	if again := a.watchReplays(); !again || kicked() { // first look: sizes only, so look again soon
		t.Fatalf("first look: again=%v, want a second look and no kick", again)
	}
	if again := a.watchReplays(); again || !kicked() {
		t.Fatalf("second look: again=%v, want a kick and nothing more to wait for", again)
	}
	if again := a.watchReplays(); again || kicked() { // its upload failed: the 30 s scan retries, not every look
		t.Fatal("kicked twice for the same replay, or kept looking")
	}
	a.files[p] = &fileState{Sent: true}
	a.watchReplays()
	if kicked() || len(a.watch.kicked) != 0 {
		t.Fatal("a sent replay should be forgotten")
	}

	// a scan already running: wait for it rather than kick (it may have skipped the replay as too new)
	q := filepath.Join(dir, "next.StormReplay")
	_ = os.WriteFile(q, make([]byte, 100), 0o644)
	_ = os.Chtimes(q, old, old)
	a.syncing.Store(true)
	a.watchReplays()
	if again := a.watchReplays(); !again || kicked() {
		t.Fatal("while a scan runs: should wait and look again, not kick")
	}
	a.syncing.Store(false)
	a.watchReplays()
	if !kicked() {
		t.Fatal("didn't kick once the scan finished")
	}
}

func TestGameLately(t *testing.T) {
	a := &Agent{}
	now := time.Now()
	if a.gameLately(now) {
		t.Fatal("no game yet")
	}
	a.lastLobbyAt = now.Add(-30 * time.Minute)
	if !a.gameLately(now) {
		t.Fatal("a game loaded half an hour ago")
	}
	a.lastLobbyAt = now.Add(-2 * time.Hour)
	if a.gameLately(now) {
		t.Fatal("two hours ago is too long")
	}
}

func TestFolderWatchNil(t *testing.T) {
	var w *folderWatch
	if w.ok() {
		t.Fatal("no watch isn't ok")
	}
	w.close() // mustn't panic
}
