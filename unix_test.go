//go:build !windows

package main

import (
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func mkdirs(t *testing.T, ps ...string) {
	t.Helper()
	for _, p := range ps {
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

func TestWineReplayFoldersAreFound(t *testing.T) {
	home := t.TempDir()
	t.Setenv("WINEPREFIX", "")
	lutris := filepath.Join(home, "Games", "battlenet", "drive_c", "users", "sam", "Documents", "Heroes of the Storm", "Accounts")
	proton := filepath.Join(home, ".local", "share", "Steam", "steamapps", "compatdata", "123", "pfx", "drive_c", "users", "steamuser", "Documents", "Heroes of the Storm", "Accounts")
	bottles := filepath.Join(home, ".var", "app", "com.usebottles.bottles", "data", "bottles", "bottles", "Battle.net", "drive_c", "users", "sam", "My Documents", "Heroes of the Storm", "Accounts")
	mkdirs(t, lutris, proton, bottles, filepath.Join(home, "Games", "other", "drive_c", "users", "sam", "Documents"))
	got := strings.Join(wineReplayFolders(home), "\n")
	for _, want := range []string{lutris, proton, bottles} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %s in\n%s", want, got)
		}
	}
	if strings.Contains(got, "other") {
		t.Errorf("a prefix without Heroes was listed:\n%s", got)
	}

	custom := t.TempDir()
	mine := filepath.Join(custom, "drive_c", "users", "me", "Documents", "Heroes of the Storm", "Accounts")
	mkdirs(t, mine)
	t.Setenv("WINEPREFIX", custom)
	if got := wineReplayFolders(home); len(got) == 0 || got[0] != mine {
		t.Errorf("WINEPREFIX first: %v", got)
	}
}

func TestWineLobbyDirIsInsideThePrefix(t *testing.T) {
	home := t.TempDir()
	user := filepath.Join(home, "Games", "bnet", "drive_c", "users", "sam")
	accounts := filepath.Join(user, "Documents", "Heroes of the Storm", "Accounts")
	mkdirs(t, accounts, filepath.Join(user, "AppData", "Local", "Temp"))
	folders := []Folder{{Path: "/mnt/backup/replays"}, {Path: accounts}}
	if got, want := wineLobbyDir(folders), filepath.Join(user, "AppData", "Local", "Temp", "Heroes of the Storm"); got != want {
		t.Errorf("got %s, want %s", got, want)
	}
	if got := wineLobbyDir([]Folder{{Path: "/home/x/Library/Application Support/Blizzard/Heroes of the Storm/Accounts"}}); got != "" {
		t.Errorf("not Wine: %q", got)
	}
	a := NewAgent(Config{Server: "http://127.0.0.1:1", Folders: folders}, t.TempDir(), log.New(io.Discard, "", 0))
	if !strings.HasPrefix(a.Config().Lobby, user) {
		t.Errorf("agent lobby %s", a.Config().Lobby)
	}
}

func TestSetupFileNextToTheProgram(t *testing.T) {
	b := []byte(`{"server":"https://hotsdash.com","enrol":"hde_x","name":"Alex"}`)
	c, err := parseSettings(b)
	if err != nil || c.Server != "https://hotsdash.com" || c.Enrol != "hde_x" || c.Embedded == "" {
		t.Fatalf("%+v %v", c, err)
	}
	if _, err := parseSettings([]byte(`{"enrol":"x"}`)); err == nil {
		t.Fatal("settings without a server are refused")
	}
	exe := filepath.Join(t.TempDir(), "prog")
	_ = os.WriteFile(exe, []byte("\x7fELF not a trailer"), 0o755)
	if _, err := trailerSettings(exe); err == nil {
		t.Fatal("no trailer")
	}
}

func TestServiceFilesQuotePaths(t *testing.T) {
	exe := `/home/a "b"/50%/$HOME/hots-dash-uploader`
	u := unitFile(exe)
	if !strings.Contains(u, `ExecStart="/home/a \"b\"/50%%/$$HOME/hots-dash-uploader" --service`) {
		t.Errorf("unit:\n%s", u)
	}
	d := desktopFile(exe)
	if !strings.Contains(d, `Exec="/home/a \"b\"/50%%/\$HOME/hots-dash-uploader" --service`) {
		t.Errorf("desktop:\n%s", d)
	}
	p := plistFile(`/Users/a&b/<x>/hots-dash-uploader`, "/tmp/l", true)
	if !strings.Contains(p, `<string>/Users/a&amp;b/&lt;x&gt;/hots-dash-uploader</string>`) || !strings.Contains(p, "<key>RunAtLoad</key><true/>") {
		t.Errorf("plist:\n%s", p)
	}
	if strings.Contains(plistFile("/x", "/l", false), "<true/>\n\t<key>KeepAlive") {
		t.Error("RunAtLoad off")
	}
}

func TestOneCopyAtATime(t *testing.T) {
	dir := t.TempDir()
	if running(dir) {
		t.Fatal("nothing is running yet")
	}
	if !takeLock(dir) {
		t.Fatal("first copy gets the lock")
	}
	defer func() { lockFile.Close(); lockFile = nil }()
	if !running(dir) {
		t.Fatal("held lock means running")
	}
	if takeLock(dir) {
		t.Fatal("a second copy doesn't")
	}
}

func TestNotificationTextIsPlain(t *testing.T) {
	if got := plainText("a\x1b[31mb\x07\nc"); got != "a[31mb\nc" {
		t.Errorf("%q", got)
	}
}

func TestExpandHome(t *testing.T) {
	home, _ := os.UserHomeDir()
	if got := expandHome("~/Backups/HotS"); got != filepath.Join(home, "Backups", "HotS") {
		t.Errorf("%s", got)
	}
	if got := expandHome("/abs/~x"); got != "/abs/~x" {
		t.Errorf("%s", got)
	}
}

// The folder watch says when a replay lands, in a folder made after watching started too (a new account).
func TestFolderWatchSeesNewReplays(t *testing.T) {
	root := t.TempDir()
	changed := make(chan struct{}, 1)
	w := watchFolders([]string{root}, changed)
	if w == nil {
		t.Skip("no folder notifications here")
	}
	defer w.close()
	wait := func(what string) {
		t.Helper()
		select {
		case <-changed:
		case <-time.After(3 * time.Second):
			t.Fatalf("no nudge for %s", what)
		}
	}
	sub := filepath.Join(root, "1", "2-Hero-1-42", "Replays", "Multiplayer")
	mkdirs(t, sub)
	wait("new folders")
	time.Sleep(300 * time.Millisecond) // let the new folders be watched
	for len(changed) > 0 {
		<-changed
	}
	if err := os.WriteFile(filepath.Join(sub, "x.StormReplay"), []byte("replay"), 0o644); err != nil {
		t.Fatal(err)
	}
	wait("a replay in a new folder")
	if !w.ok() {
		t.Fatal("watch died")
	}
	w.close()
	if w.ok() {
		t.Fatal("still alive after close")
	}
}

func TestTokenFromTheEnvironment(t *testing.T) {
	var warn strings.Builder
	t.Setenv(tokenEnv, "hdu_env")
	if got := uploadToken("", &warn); got != "hdu_env" || warn.Len() != 0 {
		t.Errorf("from the environment: %q, warned %q", got, warn.String())
	}
	if v, set := os.LookupEnv(tokenEnv); set {
		t.Errorf("programs started later would inherit the token: %q", v)
	}
	t.Setenv(tokenEnv, "hdu_env")
	if got := uploadToken("hdu_flag", &warn); got != "hdu_flag" {
		t.Errorf("--token wins: %q", got)
	}
	if !strings.Contains(warn.String(), "other programs") || !strings.Contains(warn.String(), tokenEnv) {
		t.Errorf("no warning: %q", warn.String())
	}
	if strings.Contains(warn.String(), "hdu_") {
		t.Errorf("the warning repeats the token: %q", warn.String())
	}
	if _, set := os.LookupEnv(tokenEnv); set {
		t.Error("left in the environment")
	}
}

func wantMode(t *testing.T, p string, want os.FileMode) {
	t.Helper()
	st, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if got := st.Mode().Perm(); got != want {
		t.Errorf("%s is %o, want %o", p, got, want)
	}
}

func TestTheSettingsFolderIsPrivate(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	d := appDir()
	wantMode(t, d, 0o700)
	if err := os.Chmod(d, 0o755); err != nil { // made by an earlier version
		t.Fatal(err)
	}
	appDir()
	wantMode(t, d, 0o700)
}

func TestTheLogIsPrivate(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "uploader.log")
	newLogger(dir, nil).Print("x")
	wantMode(t, p, 0o600)
	if err := os.Chmod(p, 0o644); err != nil { // made by an earlier version
		t.Fatal(err)
	}
	newLogger(dir, nil)
	wantMode(t, p, 0o600)

	// a full log is set aside, and the old one is made private too
	if err := os.WriteFile(p, make([]byte, 1<<20+1), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, 0o644); err != nil {
		t.Fatal(err)
	}
	newLogger(dir, nil)
	wantMode(t, p, 0o600)
	wantMode(t, p+".old", 0o600)
}
