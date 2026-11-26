package main

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// uploadDash is a dashboard that knows none of the replays, and answers each upload with what reply says about the
// file names it was sent.
type uploadDash struct {
	mu      sync.Mutex
	batches [][]string // the file names of each upload
	reply   func(names []string) []map[string]any
	onKnown func() // runs between the scan and the upload
}

func (d *uploadDash) uploads() [][]string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([][]string(nil), d.batches...)
}

func (d *uploadDash) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/api/replays/known":
		if d.onKnown != nil {
			d.onKnown()
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"known": []string{}})
	case "/api/replays":
		if err := r.ParseMultipartForm(32 << 20); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		var names []string
		for _, f := range r.MultipartForm.File["files"] {
			names = append(names, f.Filename)
		}
		d.mu.Lock()
		d.batches = append(d.batches, names)
		d.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"results": d.reply(names)})
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

// answer gives every file the status named for it ("queued" if not named).
func answer(statuses map[string]string) func([]string) []map[string]any {
	return func(names []string) []map[string]any {
		var out []map[string]any
		for _, n := range names {
			s := statuses[n]
			if s == "" {
				s = "queued"
			}
			out = append(out, map[string]any{"fileName": n, "status": s})
		}
		return out
	}
}

// writeReplay writes a finished replay (old enough to upload); the content makes its hash.
func writeReplay(t *testing.T, p, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(p, old, old); err != nil {
		t.Fatal(err)
	}
}

func syncAgent(t *testing.T, d *uploadDash, folders ...string) *Agent {
	t.Helper()
	srv := httptest.NewServer(d)
	t.Cleanup(srv.Close)
	cfg := Config{Server: srv.URL, NoLive: true}
	for _, f := range folders {
		cfg.Folders = append(cfg.Folders, Folder{Path: f})
	}
	return NewAgent(cfg, t.TempDir(), log.New(io.Discard, "", 0))
}

func mustSync(t *testing.T, a *Agent) {
	t.Helper()
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
}

func isSent(a *Agent, p string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	f := a.files[p]
	return f != nil && f.Sent
}

func TestTurnedDownReplayIsNotMarkedSent(t *testing.T) {
	dir := t.TempDir()
	game := filepath.Join(dir, "game.StormReplay")
	writeReplay(t, game, "one")
	a := syncAgent(t, &uploadDash{reply: answer(map[string]string{"game.StormReplay": "error"})}, dir)
	mustSync(t, a)
	if isSent(a, game) {
		t.Fatal("the dashboard turned it down, but it's marked sent")
	}
}

func TestOnlyAcceptedReplaysAreMarkedSent(t *testing.T) {
	dir := t.TempDir()
	good, bad, gone := filepath.Join(dir, "a-good.StormReplay"), filepath.Join(dir, "b-bad.StormReplay"), filepath.Join(dir, "c-gone.StormReplay")
	writeReplay(t, good, "good")
	writeReplay(t, bad, "bad")
	writeReplay(t, gone, "gone")
	d := &uploadDash{reply: answer(map[string]string{"b-bad.StormReplay": "error"})}
	d.onKnown = func() { _ = os.Remove(gone) } // can't be read by the time it's uploaded
	a := syncAgent(t, d, dir)
	mustSync(t, a)
	if got := d.uploads(); len(got) != 1 || len(got[0]) != 2 {
		t.Fatalf("uploads: %v", got)
	}
	if !isSent(a, good) {
		t.Error("the accepted replay isn't marked sent")
	}
	if isSent(a, bad) {
		t.Error("the turned down replay is marked sent")
	}
	if isSent(a, gone) {
		t.Error("the replay that couldn't be read is marked sent")
	}
}

// due makes every waiting retry due now.
func due(a *Agent) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, f := range a.files {
		f.RetryAt = 0
	}
}

func fails(a *Agent, p string) (int, time.Duration) {
	a.mu.Lock()
	defer a.mu.Unlock()
	f := a.files[p]
	return f.Fails, time.Until(time.UnixMilli(f.RetryAt))
}

func turnedDownEvents(a *Agent) (out []string) {
	for _, e := range a.Recent() {
		if strings.Contains(e.Text, "turned down") {
			out = append(out, e.Text)
		}
	}
	return out
}

func TestTurnedDownReplayIsRetriedWithBackoffThenLeft(t *testing.T) {
	dir := t.TempDir()
	game := filepath.Join(dir, "game.StormReplay")
	writeReplay(t, game, "one")
	d := &uploadDash{reply: func(n []string) []map[string]any {
		return []map[string]any{{"fileName": n[0], "status": "error", "error": "not a replay"}}
	}}
	a := syncAgent(t, d, dir)
	for i, wait := range []time.Duration{time.Minute, 10 * time.Minute, time.Hour, 6 * time.Hour, 6 * time.Hour} {
		mustSync(t, a)
		mustSync(t, a) // not due yet: nothing goes
		if got := len(d.uploads()); got != i+1 {
			t.Fatalf("try %d: %d uploads", i+1, got)
		}
		n, in := fails(a, game)
		if n != i+1 || in > wait || in < wait-time.Minute/2 {
			t.Fatalf("try %d: fails %d, retry in %v, want %v", i+1, n, in, wait)
		}
		if want := (i + 1) / 5; len(turnedDownEvents(a)) != want {
			t.Fatalf("try %d: events %v", i+1, turnedDownEvents(a))
		}
		due(a)
	}
	if got := turnedDownEvents(a)[0]; got != "game.StormReplay was turned down by the dashboard: not a replay" {
		t.Fatalf("event: %q", got)
	}
	mustSync(t, a)
	if len(d.uploads()) != 5 || isSent(a, game) {
		t.Fatalf("after giving up: %d uploads, sent %v", len(d.uploads()), isSent(a, game))
	}

	// the count survives a restart
	b := NewAgent(a.Config(), a.dir, log.New(io.Discard, "", 0))
	mustSync(t, b)
	if len(d.uploads()) != 5 {
		t.Fatalf("sent again after a restart: %d uploads", len(d.uploads()))
	}

	// a changed file starts again, and is marked sent once accepted
	writeReplay(t, game, "a different one")
	d.reply = answer(nil)
	mustSync(t, b)
	if len(d.uploads()) != 6 || !isSent(b, game) {
		t.Fatalf("changed file: %d uploads, sent %v", len(d.uploads()), isSent(b, game))
	}
}

func TestResultWithoutAnAnswerIsRetriedNotGivenUp(t *testing.T) {
	dir := t.TempDir()
	game := filepath.Join(dir, "game.StormReplay")
	writeReplay(t, game, "one")
	d := &uploadDash{reply: func([]string) []map[string]any { return nil }}
	a := syncAgent(t, d, dir)
	for i := 0; i < 8; i++ {
		mustSync(t, a)
		due(a)
	}
	if len(d.uploads()) != 8 || isSent(a, game) || len(turnedDownEvents(a)) != 0 {
		t.Fatalf("%d uploads, sent %v, events %v", len(d.uploads()), isSent(a, game), turnedDownEvents(a))
	}
}

func TestKnownStatusCountsAsSent(t *testing.T) {
	dir := t.TempDir()
	game := filepath.Join(dir, "game.StormReplay")
	writeReplay(t, game, "one")
	a := syncAgent(t, &uploadDash{reply: answer(map[string]string{"game.StormReplay": "known"})}, dir)
	mustSync(t, a)
	if !isSent(a, game) {
		t.Fatal("known: the dashboard has it")
	}
}

func TestUploadResultAccepted(t *testing.T) {
	for r, want := range map[uploadResult]bool{
		{Status: "queued"}: true, {Status: "known"}: true, {Status: "something-new"}: true,
		{Status: "error"}: false, {Status: "Failed"}: false, {Status: "rejected"}: false, {Status: "invalid"}: false,
		{Status: "queued", Error: "disk full"}: false,
	} {
		if got := r.accepted(); got != want {
			t.Errorf("%+v: accepted %v", r, got)
		}
	}
}

func TestMatchResults(t *testing.T) {
	a, b, c := filepath.Join("x", "a.StormReplay"), filepath.Join("x", "b.StormReplay"), filepath.Join("y", "a.StormReplay")
	res := func(name string, id int64) uploadResult { return uploadResult{FileName: name, ReplayID: id} }
	for _, tc := range []struct {
		name    string
		sent    []string
		results []uploadResult
		want    []int64 // the replay id matched to each file sent, 0 for none
	}{
		{"out of order", []string{a, b}, []uploadResult{res("b.StormReplay", 2), res("a.StormReplay", 1)}, []int64{1, 2}},
		{"one missing from the reply", []string{a, b}, []uploadResult{res("b.StormReplay", 2)}, []int64{0, 2}},
		{"no names", []string{a, b}, []uploadResult{res("", 1), res("", 2)}, []int64{1, 2}},
		{"no names, too few", []string{a, b}, []uploadResult{res("", 1)}, []int64{0, 0}},
		{"shared name", []string{a, c}, []uploadResult{res("a.StormReplay", 1), res("a.StormReplay", 2)}, []int64{1, 2}},
		{"shared name, too few", []string{a, c}, []uploadResult{res("a.StormReplay", 1)}, []int64{0, 0}},
		{"a name the server rewrote", []string{a, b}, []uploadResult{res("b.StormReplay", 2), res("?.StormReplay", 1)}, []int64{0, 2}},
		{"rewritten, in place", []string{a, b}, []uploadResult{res("?.StormReplay", 1), res("b.StormReplay", 2)}, []int64{1, 2}},
	} {
		got := matchResults(tc.sent, tc.results)
		for i, p := range tc.sent {
			var id int64
			if got[p] != nil {
				id = got[p].ReplayID
			}
			if id != tc.want[i] {
				t.Errorf("%s: file %d got result %d, want %d", tc.name, i, id, tc.want[i])
			}
		}
	}
}

func hasState(a *Agent, p string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.files[p] != nil
}

func TestDeletedFilesArePrunedButNotThoseInAMissingFolder(t *testing.T) {
	game, backup := t.TempDir(), filepath.Join(t.TempDir(), "drive")
	kept, deleted := filepath.Join(game, "kept.StormReplay"), filepath.Join(game, "sub", "deleted.StormReplay")
	away := filepath.Join(backup, "away.StormReplay")
	writeReplay(t, kept, "kept")
	writeReplay(t, deleted, "deleted")
	writeReplay(t, away, "away")
	a := syncAgent(t, &uploadDash{reply: answer(nil)}, game, backup)
	mustSync(t, a)

	if err := os.RemoveAll(filepath.Dir(deleted)); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(backup); err != nil { // unplugged
		t.Fatal(err)
	}
	mustSync(t, a)
	if hasState(a, deleted) {
		t.Error("the deleted file is still remembered")
	}
	if !hasState(a, kept) || !isSent(a, away) {
		t.Error("forgot a file that's there, or one in a folder that's missing")
	}
	if b := NewAgent(a.Config(), a.dir, log.New(io.Discard, "", 0)); hasState(b, deleted) || !hasState(b, away) {
		t.Error("the pruned state wasn't saved")
	}
}

func TestPruneKeepsWhatItCannotJudge(t *testing.T) {
	root := t.TempDir()
	inner := filepath.Join(root, "backup") // a configured folder inside another one, missing
	gone := filepath.Join(inner, "x.StormReplay")
	elsewhere := filepath.Join(t.TempDir(), "y.StormReplay") // in no folder that was scanned
	a := syncAgent(t, &uploadDash{reply: answer(nil)}, root, inner)
	a.files[gone] = &fileState{Sent: true}
	a.files[elsewhere] = &fileState{Sent: true}
	mustSync(t, a)
	if !hasState(a, gone) || !hasState(a, elsewhere) {
		t.Fatalf("pruned: in a missing folder %v, outside the folders %v", !hasState(a, gone), !hasState(a, elsewhere))
	}
}

func TestUploadNameDropsWhatCouldBreakTheHeader(t *testing.T) {
	if got := uploadName(filepath.Join("d", "a\r\n\t\x00\x7f\"b é.StormReplay")); got != "ab é.StormReplay" {
		t.Fatalf("got %q", got)
	}
}

// A file name with line breaks in it can't add headers to its part, for a replay or a rejoin save.
func TestFileNameCannotAddHeaderLines(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows doesn't allow such names")
	}
	var mu sync.Mutex
	var headers []textproto.MIMEHeader
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(32 << 20); err != nil {
			t.Errorf("multipart: %v", err)
		} else {
			mu.Lock()
			for _, files := range r.MultipartForm.File {
				for _, f := range files {
					headers = append(headers, f.Header)
				}
			}
			mu.Unlock()
		}
		_, _ = io.WriteString(w, `{}`)
	}))
	defer srv.Close()
	a := NewAgent(Config{Server: srv.URL, Folders: []Folder{{Path: t.TempDir()}}}, t.TempDir(), log.New(io.Discard, "", 0))
	name := "a\r\nX-Injected: yes\r\nb"
	replay, save := filepath.Join(t.TempDir(), name+".StormReplay"), filepath.Join(t.TempDir(), name+".StormSave")
	writeReplay(t, replay, "one")
	writeReplay(t, save, "two")
	if _, err := a.uploadBatch([]string{replay}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.sendSave(save); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(headers) != 2 {
		t.Fatalf("files received: %d", len(headers))
	}
	for _, h := range headers {
		if h.Get("X-Injected") != "" || len(h) != 2 {
			t.Errorf("headers: %v", h)
		}
	}
}
