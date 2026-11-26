package main

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Whole disks, the home folder and Documents can't be added, as replay folders or rejoin folders.
func TestWholeDiskFoldersAreRefused(t *testing.T) {
	home := t.TempDir()
	for _, k := range []string{"HOME", "USERPROFILE"} {
		t.Setenv(k, home)
	}
	for _, k := range []string{"OneDrive", "OneDriveConsumer", "OneDriveCommercial"} {
		t.Setenv(k, "")
	}
	docs := filepath.Join(home, "Documents")
	oneDocs := filepath.Join(home, "OneDrive - Work", "Documents")
	accounts := filepath.Join(docs, "Heroes of the Storm", "Accounts")
	backup := filepath.Join(home, "Backups")
	for _, d := range []string{accounts, oneDocs, backup} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	root := filepath.VolumeName(home) + string(filepath.Separator)
	sep := string(filepath.Separator)
	refused := []string{root, home, home + sep, docs, docs + sep, oneDocs, accounts + sep + ".." + sep + ".."}
	if link := filepath.Join(backup, "link"); os.Symlink(home, link) == nil { // Windows may not allow making one
		refused = append(refused, link)
	}

	a := NewAgent(Config{Server: "http://127.0.0.1:1", Folders: []Folder{{Path: backup}}}, t.TempDir(), log.New(io.Discard, "", 0))
	u := &ui{a: a}
	add := func(f func(*http.Request) (any, error), path string) error {
		b, _ := json.Marshal(map[string]string{"action": "add", "path": path})
		_, err := f(httptest.NewRequest(http.MethodPost, "/", strings.NewReader(string(b))))
		return err
	}
	for _, p := range refused {
		for name, f := range map[string]func(*http.Request) (any, error){"folders": u.folders, "rejoin": u.rejoin} {
			if err := add(f, p); err == nil || !strings.Contains(err.Error(), "Accounts") {
				t.Errorf("%s add %q: %v", name, p, err)
			}
		}
	}
	if c := a.Config(); len(c.Folders) != 1 || len(c.RejoinFolders) != 0 {
		t.Fatalf("added anyway: %+v, %+v", c.Folders, c.RejoinFolders)
	}
	if err := add(u.rejoin, ""); err == nil {
		t.Error("an empty rejoin path is the current folder, not a choice")
	}

	inDocs := filepath.Join(docs, "Old replays")
	if err := os.Mkdir(inDocs, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{accounts, inDocs} {
		if err := add(u.folders, p); err != nil {
			t.Errorf("folders add %q: %v", p, err)
		}
	}
	if err := add(u.rejoin, filepath.Join(accounts, "1", "2-Hero-1-2", "..", "Rejoin")); err != nil {
		t.Errorf("rejoin add: %v", err)
	}
	if c := a.Config(); len(c.Folders) != 3 || len(c.RejoinFolders) != 1 {
		t.Fatalf("not added: %+v, %+v", c.Folders, c.RejoinFolders)
	}
}
