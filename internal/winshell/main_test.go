package winshell

import (
	"os"
	"testing"
)

// No test may touch the real user's settings or shortcuts: the home and config folders point at a throwaway folder.
func TestMain(m *testing.M) {
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
