//go:build !windows

package agent

import (
	"os"
	"path/filepath"
	"testing"
)

func TestProcHasGame(t *testing.T) {
	root := t.TempDir()
	mk := func(pid, comm, cmdline string) {
		d := filepath.Join(root, pid)
		_ = os.MkdirAll(d, 0o755)
		_ = os.WriteFile(filepath.Join(d, "comm"), []byte(comm+"\n"), 0o644)
		_ = os.WriteFile(filepath.Join(d, "cmdline"), []byte(cmdline), 0o644)
	}
	mk("1", "systemd", "/sbin/init\x00")
	_ = os.MkdirAll(filepath.Join(root, "self"), 0o755)
	if open, ok := procHasGame(root); open || !ok {
		t.Fatalf("no game: %v %v", open, ok)
	}
	mk("4242", "wine64-preloader", "Z:\\games\\hots\\Versions\\Base91756\\HeroesOfTheStorm_x64.exe\x00-uid\x00heroes\x00")
	if open, ok := procHasGame(root); !open || !ok {
		t.Fatalf("Wine game: %v %v", open, ok)
	}
	if _, ok := procHasGame(filepath.Join(root, "missing")); ok {
		t.Fatal("no /proc: unknown")
	}
}
