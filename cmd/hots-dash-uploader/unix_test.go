//go:build !windows

package main

import (
	"os"
	"strings"
	"testing"
)

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
