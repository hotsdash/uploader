package main

import (
	"os"
	"sync"
	"testing"
)

// No test may launch a browser or any program, or touch the real user's settings, autostart entries or shortcuts.
// TestMain runs before every test in the package (whatever its build tags): it swaps the openers for a recorder
// and points the home and config folders at a throwaway folder.

var (
	openedMu sync.Mutex
	opened   []string
)

func TestMain(m *testing.M) {
	openURL = func(u string) bool {
		if !webLink(u) {
			return false
		}
		openedMu.Lock()
		opened = append(opened, u)
		openedMu.Unlock()
		return true
	}
	openFile = func(string) {}
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

// openedURLs is what the code under test asked the browser to open.
func openedURLs() []string {
	openedMu.Lock()
	defer openedMu.Unlock()
	return append([]string(nil), opened...)
}

func TestTheOpenersAreRecordersInTests(t *testing.T) {
	before := len(openedURLs())
	if !openURL("https://hots.example.com/link?code=X") || len(openedURLs()) != before+1 {
		t.Fatal("openURL isn't the recorder: a test could open a real browser")
	}
	if openURL("file:///etc/passwd") {
		t.Fatal("non-web address opened")
	}
	if h := os.Getenv("HOME"); h == "" || h != os.Getenv("APPDATA") {
		t.Fatal("tests aren't using a throwaway home")
	}
}
