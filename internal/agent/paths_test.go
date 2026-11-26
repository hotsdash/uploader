package agent

import (
	"io"
	"log"
	"os"
	"path/filepath"
	"testing"
)

func TestSharedPath(t *testing.T) {
	cases := map[string]string{
		`C:\Users\Sam Smith\OneDrive\Documents\Heroes of the Storm\Accounts\112392579\2-Hero-1-269807\Replays\Multiplayer\2026-10-29 Braxis.StormReplay`: `2-Hero-1-269807\Replays\Multiplayer\2026-10-29 Braxis.StormReplay`,
		`D:\Backups\HotS\2-Hero-1-269807\Replays\Multiplayer\x.StormReplay`:                                                                              `2-Hero-1-269807\Replays\Multiplayer\x.StormReplay`,
		"/home/sam/Library/Application Support/Blizzard/Heroes of the Storm/Accounts/1/1-Hero-1-42/Replays/Multiplayer/x.StormReplay":                    "1-Hero-1-42/Replays/Multiplayer/x.StormReplay",
		`D:\Backups\Sam\loose\x.StormReplay`:    `x.StormReplay`,
		`D:\x12-Hero-1-2\Replays\y.StormReplay`: `y.StormReplay`,
	}
	for in, want := range cases {
		if got := sharedPath(in); got != want {
			t.Errorf("sharedPath(%q) = %q, want %q", in, got, want)
		}
	}
	// what the dashboard reads from it (storage.ts toonFromPath) is still there
	if m := toonRe.FindStringSubmatch(sharedPath(`C:\Users\Sam\Documents\Heroes of the Storm\Accounts\1\2-Hero-1-269807\Replays\Multiplayer\x.StormReplay`)); m == nil || m[1] != "2-Hero-1-269807" {
		t.Fatalf("toon lost: %v", m)
	}
}

func TestOversizedFilesAreSkipped(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "huge.StormReplay")
	f, _ := os.Create(p)
	_ = f.Truncate(maxReplayBytes + 1) // sparse: nothing that big is really written
	f.Close()
	a := NewAgent(Config{Server: "http://127.0.0.1:1", Folders: []Folder{{Path: dir}}}, dir, log.New(io.Discard, "", 0))
	if _, _, err := a.hashFile(p); err != errTooBig {
		t.Fatalf("want errTooBig, got %v", err)
	}
}
