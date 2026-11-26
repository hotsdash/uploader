package agent

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func mkfile(t *testing.T, p string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestSaveFilesReadsOnlyRejoinFolders(t *testing.T) {
	toon := filepath.Join(t.TempDir(), "123", "2-Hero-1-456")
	rejoin := RejoinDirFor(toon)
	mkfile(t, filepath.Join(rejoin, "save.StormSave"))
	mkfile(t, filepath.Join(rejoin, "notes.txt"))
	if got := SaveFiles([]string{rejoin}); len(got) != 1 {
		t.Fatalf("rejoin folder: want 1 save, got %v", got)
	}
	// an account folder works too: its Saves\Rejoin is looked in
	if got := SaveFiles([]string{toon}); len(got) != 1 {
		t.Fatalf("account folder: want 1 save, got %v", got)
	}
	// the Accounts folder (a replay folder) is not searched
	if got := SaveFiles([]string{filepath.Dir(filepath.Dir(toon))}); len(got) != 0 {
		t.Fatalf("Accounts folder: want none, got %v", got)
	}
}

func TestToonDirOf(t *testing.T) {
	root := t.TempDir()
	p := filepath.Join(root, "Accounts", "123", "2-Hero-1-456", "Replays", "Multiplayer", "game.StormReplay")
	toon, dir := toonDirOf(p)
	if toon != "2-Hero-1-456" || dir != filepath.Join(root, "Accounts", "123", "2-Hero-1-456") {
		t.Fatalf("got %q %q", toon, dir)
	}
	if toon, _ := toonDirOf(filepath.Join(root, "backup", "game.StormReplay")); toon != "" {
		t.Fatalf("backup folder has no account, got %q", toon)
	}
}

func TestLiveAccounts(t *testing.T) {
	root := t.TempDir()
	eu := filepath.Join(root, "1", "2-Hero-1-111")
	us := filepath.Join(root, "1", "1-Hero-1-222")
	kr := filepath.Join(root, "1", "3-Hero-1-333")
	mkfile(t, filepath.Join(RejoinDirFor(eu), "a.StormSave"))
	scan := ScanStats{
		Toons:    map[string]int{"2-Hero-1-111": 50, "1-Hero-1-222": 5, "3-Hero-1-333": 1},
		ToonDirs: map[string]string{"2-Hero-1-111": eu, "1-Hero-1-222": us, "3-Hero-1-333": kr},
	}
	custom := filepath.Join(root, "elsewhere")
	cfg := Config{RejoinFolders: []Folder{
		{Path: RejoinDirFor(eu)},                      // the game's folder, matched by path
		{Path: custom, Toon: "1-Hero-1-222"},          // picked for an account
		{Path: filepath.Join(root, "old"), Off: true}, // belongs to no account found
	}}
	accounts, others := LiveAccounts(cfg, scan)
	if len(accounts) != 3 || accounts[0].Toon != "2-Hero-1-111" {
		t.Fatalf("accounts in replay order: %+v", accounts)
	}
	if a := accounts[0]; !a.Set || !a.Exists || a.Saves != 1 || a.LastSave == nil {
		t.Fatalf("EU account: %+v", a)
	}
	if a := accounts[1]; !a.Set || a.Folder != custom {
		t.Fatalf("US account picked folder: %+v", a)
	}
	if a := accounts[2]; a.Set || a.Suggested != RejoinDirFor(kr) || a.Exists {
		t.Fatalf("KR account not set, suggested: %+v", a)
	}
	if len(others) != 1 || !others[0].Off {
		t.Fatalf("others: %+v", others)
	}
	if m := liveMissing(accounts); len(m) != 1 || m[0] != "3-Hero-1-333" {
		t.Fatalf("missing: %v", m)
	}
}

func TestMainLiveFolder(t *testing.T) {
	accounts := []LiveAccount{
		{Toon: "4-Hero-1-444"}, // most replays, but no folder known for it
		{Toon: "2-Hero-1-111", Suggested: "eu-rejoin"},
		{Toon: "1-Hero-1-222", Suggested: "us-rejoin"},
	}
	f, ok := mainLiveFolder(Config{}, accounts)
	if !ok || f.Path != "eu-rejoin" || f.Toon != "2-Hero-1-111" {
		t.Fatalf("the busiest account with a folder: %+v, %v", f, ok)
	}
	for name, cfg := range map[string]Config{
		"live games off":        {NoLive: true},
		"already done":          {RejoinAuto: true},
		"one set":               {RejoinFolders: []Folder{{Path: "us-rejoin", Toon: "1-Hero-1-222"}}},
		"one set, switched off": {RejoinFolders: []Folder{{Path: "eu-rejoin", Off: true}}},
	} {
		if f, ok := mainLiveFolder(cfg, accounts); ok {
			t.Errorf("%s: shouldn't set one, got %+v", name, f)
		}
	}
	if f, ok := mainLiveFolder(Config{}, nil); ok {
		t.Errorf("no accounts found yet: got %+v", f)
	}
}

func TestLiveAccountsTiesInToonOrder(t *testing.T) {
	scan := ScanStats{Toons: map[string]int{"2-Hero-1-111": 3, "1-Hero-1-222": 3, "3-Hero-1-333": 9}}
	accounts, _ := LiveAccounts(Config{}, scan)
	if len(accounts) != 3 || accounts[0].Toon != "3-Hero-1-333" || accounts[1].Toon != "1-Hero-1-222" || accounts[2].Toon != "2-Hero-1-111" {
		t.Fatalf("order: %+v", accounts)
	}
}

func TestAutoLiveFolderSetsOnceAndStaysRemoved(t *testing.T) {
	root := t.TempDir()
	eu := filepath.Join(root, "1", "2-Hero-1-111")
	a := &Agent{log: log.New(io.Discard, "", 0), scan: ScanStats{
		Toons:    map[string]int{"2-Hero-1-111": 50},
		ToonDirs: map[string]string{"2-Hero-1-111": eu},
	}}
	a.autoLiveFolder()
	c := a.Config()
	if len(c.RejoinFolders) != 1 || c.RejoinFolders[0].Path != RejoinDirFor(eu) || !c.RejoinAuto {
		t.Fatalf("after the first scan: %+v", c)
	}
	if dirs := a.rejoinDirs(); len(dirs) != 1 || dirs[0] != RejoinDirFor(eu) {
		t.Fatalf("saves should be read from it: %v", dirs)
	}
	// removed by hand (ui.go keeps RejoinAuto on): the next scan leaves it removed
	a.Update(func(c *Config) { c.RejoinFolders = nil })
	a.autoLiveFolder()
	if c := a.Config(); len(c.RejoinFolders) != 0 {
		t.Fatalf("came back after being removed: %+v", c.RejoinFolders)
	}
}

func TestLiveReport(t *testing.T) {
	early := time.Date(2026, 10, 30, 20, 0, 0, 0, time.UTC)
	late := early.Add(time.Hour)
	r := liveReport([]LiveAccount{
		{Toon: "a", Set: true, LastSave: &early},
		{Toon: "b", Set: true, Off: true, LastSave: &late},
		{Toon: "c"},
	})
	if r.Accounts != 3 || r.Folders != 1 {
		t.Fatalf("got %d of %d, want 1 of 3", r.Folders, r.Accounts)
	}
	if r.LastSave == nil || !r.LastSave.Equal(late) {
		t.Fatalf("last save %v, want %v", r.LastSave, late)
	}
	if none := liveReport(nil); none.Accounts != 0 || none.LastSave != nil {
		t.Fatalf("no accounts: %+v", none)
	}
}

func TestSavesDone(t *testing.T) {
	cases := []struct {
		r    saveResult
		want bool
	}{
		{saveResult{Applied: 10, Players: 10, More: true}, false}, // talents keep coming
		{saveResult{Applied: 10, Players: 10}, true},              // an older server: heroes were all it wanted
		{saveResult{Applied: 7, Players: 10}, false},
		{saveResult{Reason: "No game loading"}, false},
	}
	for _, c := range cases {
		if got := savesDone(c.r); got != c.want {
			t.Errorf("savesDone(%+v) = %v, want %v", c.r, got, c.want)
		}
	}
}

func TestLobbyRepeat(t *testing.T) {
	at := time.Date(2026, 11, 4, 18, 0, 0, 0, time.UTC)
	cases := []struct {
		name      string
		key, last string
		after     time.Duration
		want      bool
	}{
		{"written again while loading", "a|b", "a|b", 3 * time.Second, true},
		{"the same players, a game later", "a|b", "a|b", lobbyRepeatWindow, false},
		{"other players", "a|c", "a|b", time.Second, false},
	}
	for _, c := range cases {
		if got := lobbyRepeat(c.key, c.last, at, at.Add(c.after)); got != c.want {
			t.Errorf("%s: lobbyRepeat = %v, want %v", c.name, got, c.want)
		}
	}
}

// a game in progress with one save waiting to go, against a server that counts what it's sent.
func saveTurnAgent(t *testing.T, turn http.HandlerFunc) (*Agent, *int32, *int32) {
	t.Helper()
	var asked, sent int32
	mux := http.NewServeMux()
	mux.HandleFunc("/api/live/save-turn", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&asked, 1)
		turn(w, r)
	})
	mux.HandleFunc("/api/live/save", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&sent, 1)
		fmt.Fprint(w, `{"applied":10,"players":10,"talents":0,"more":true}`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	dir := t.TempDir()
	save := filepath.Join(dir, "0xabc.StormSave")
	mkfile(t, save)
	written := time.Now().Add(-5 * time.Second)
	if err := os.Chtimes(save, written, written); err != nil {
		t.Fatal(err)
	}
	a := &Agent{log: log.New(io.Discard, "", 0), client: srv.Client(), saves: savesState{lobbyAt: time.Now().Add(-time.Minute), sent: map[string]time.Time{}}}
	a.cfg.Server = srv.URL
	a.cfg.RejoinFolders = []Folder{{Path: dir}}
	return a, &asked, &sent
}

func TestSaveNotSentWhenItIsAnotherUploadersTurn(t *testing.T) {
	a, asked, sent := saveTurnAgent(t, func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, `{"skip":true}`) })
	a.checkSaves()
	if *asked != 1 || *sent != 0 {
		t.Fatalf("asked %d, sent %d: want asked once and nothing sent", *asked, *sent)
	}
	// nothing new has been written: no reason to ask again until the next save
	a.saves.lastCheck = time.Time{}
	a.checkSaves()
	if *asked != 1 {
		t.Fatalf("asked %d times for one save", *asked)
	}
}

func TestSaveSentOnItsTurn(t *testing.T) {
	a, _, sent := saveTurnAgent(t, func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, `{"skip":false}`) })
	a.checkSaves()
	if *sent != 1 {
		t.Fatalf("sent %d, want 1", *sent)
	}
}

func TestSaveSentWhenTheServerCannotSay(t *testing.T) {
	for name, turn := range map[string]http.HandlerFunc{
		"older server": func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) },
		"server error": func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(500) },
	} {
		a, _, sent := saveTurnAgent(t, turn)
		a.checkSaves()
		if *sent != 1 {
			t.Errorf("%s: sent %d, want 1", name, *sent)
		}
	}
}
