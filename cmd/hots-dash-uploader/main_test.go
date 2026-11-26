package main

import (
	"os"
	"testing"

	"github.com/hotsdash/uploader/internal/links"
)

// No test may launch a browser or any program, or touch the real user's settings, autostart entries or shortcuts:
// the openers do nothing here, and the home and config folders point at a throwaway folder.
func TestMain(m *testing.M) {
	links.OpenURL = func(u string) bool { return links.WebLink(u) }
	links.OpenFile = func(string) {}
	home, err := os.MkdirTemp("", "hots-dash-test-home-")
	if err != nil {
		panic(err)
	}
	for _, k := range []string{"HOME", "USERPROFILE", "APPDATA", "LOCALAPPDATA", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_BIN_HOME", "XDG_STATE_HOME"} {
		_ = os.Setenv(k, home)
	}
	code := m.Run()
	_ = os.RemoveAll(home)
	os.Exit(code)
}
