//go:build !windows

package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// gameRunning: on Linux (the game under Wine or Proton) each /proc/<pid>'s name and first argument; on macOS the
// process list from ps. ok is false when neither can be read.
func gameRunning() (open, ok bool) {
	if runtime.GOOS == "linux" {
		return procHasGame("/proc")
	}
	out, err := exec.Command("ps", "-axo", "comm=").Output()
	if err != nil {
		return false, false
	}
	for _, line := range strings.Split(string(out), "\n") {
		if isGameProcess(line) {
			return true, true
		}
	}
	return false, true
}

// procHasGame looks through a /proc-like folder (a parameter, for tests).
func procHasGame(root string) (open, ok bool) {
	dirs, err := os.ReadDir(root)
	if err != nil {
		return false, false
	}
	for _, d := range dirs {
		if !d.IsDir() || strings.TrimLeft(d.Name(), "0123456789") != "" {
			continue // not a process
		}
		if comm, err := os.ReadFile(filepath.Join(root, d.Name(), "comm")); err == nil && isGameProcess(string(comm)) {
			return true, true
		}
		// Wine often names the process after the loader (wine64-preloader); its first argument is the .exe
		if cmd, err := os.ReadFile(filepath.Join(root, d.Name(), "cmdline")); err == nil {
			if arg0, _, _ := bytes.Cut(cmd, []byte{0}); isGameProcess(string(arg0)) {
				return true, true
			}
		}
	}
	return false, true
}
