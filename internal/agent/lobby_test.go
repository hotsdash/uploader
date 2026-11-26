package agent

import (
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"
)

// Without Wine there's no game on Linux to write a lobby file, and the shared /tmp is anyone's to write to.
func TestLinuxWithoutWineHasNoLobbyFolder(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux only")
	}
	var asked atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { asked.Add(1) }))
	defer srv.Close()
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	old := filepath.Join(tmp, "Heroes of the Storm")
	// a new install, and one whose saved settings hold the old default
	for _, saved := range []string{"", old} {
		a := NewAgent(Config{Server: srv.URL, Lobby: saved, Folders: []Folder{{Path: t.TempDir()}}}, t.TempDir(), log.New(io.Discard, "", 0))
		if got := a.Config().Lobby; got != "" {
			t.Fatalf("saved %q: lobby %q", saved, got)
		}
		a.checkLobby(true)
		p := filepath.Join(old, "TempWriteReplayP1", "replay.server.battlelobby")
		writeReplay(t, p, "Someone#1234")
		if got := a.lobbyFiles(); len(got) != 0 {
			t.Fatalf("lobby files: %v", got)
		}
		a.checkLobby(false)
		if asked.Load() != 0 || len(a.Recent()) != 0 {
			t.Fatalf("saved %q: a lobby was reported", saved)
		}
	}
	// somewhere chosen by hand is kept
	if a := NewAgent(Config{Server: srv.URL, Lobby: "/mnt/lobby", Folders: []Folder{{Path: t.TempDir()}}}, t.TempDir(), log.New(io.Discard, "", 0)); a.Config().Lobby != "/mnt/lobby" {
		t.Fatalf("lobby %q", a.Config().Lobby)
	}
}

// An empty lobby folder means none, not the current folder.
func TestEmptyLobbyFolderHasNoFiles(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := os.WriteFile("replay.server.battlelobby", []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	a := &Agent{}
	if got := a.lobbyFiles(); len(got) != 0 {
		t.Fatalf("lobby files: %v", got)
	}
}

func TestLobbyDirOnWindowsAndMacsIsAsBefore(t *testing.T) {
	own := filepath.Join(os.TempDir(), "Heroes of the Storm")
	for _, goos := range []string{"windows", "darwin"} {
		for _, set := range []string{"", own} {
			if got := lobbyDir(goos, set, nil); got != own {
				t.Errorf("%s, saved %q: %q", goos, set, got)
			}
		}
	}
}
