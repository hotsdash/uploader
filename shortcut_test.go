package main

import (
	"encoding/base64"
	"regexp"
	"strings"
	"testing"
	"unicode/utf16"
)

// A folder name can hold anything, so the paths must never reach the shortcut script as text.
func TestShortcutPathsNeverReachTheScript(t *testing.T) {
	odd := `C:\Users\O’Brien'); Start-Process calc; ('$(whoami)` + "`" + `\AppData`
	ps := shortcutScript(odd+`\Start Menu\x.lnk`, odd+`\hots-dash-uploader.exe`, odd+`\hots-dash.ico`)
	for _, bad := range []string{"Start-Process", "whoami", "O’Brien", "`"} {
		if strings.Contains(ps, bad) {
			t.Fatalf("%q reached the script:\n%s", bad, ps)
		}
	}
	b64 := regexp.MustCompile(`FromBase64String\('([A-Za-z0-9+/=]*)'\)`)
	m := b64.FindAllStringSubmatch(ps, -1)
	if len(m) != 3 {
		t.Fatalf("want the shortcut, program and icon paths, got %d", len(m))
	}
	got, _ := base64.StdEncoding.DecodeString(m[1][1])
	if string(got) != odd+`\hots-dash-uploader.exe` {
		t.Fatalf("program path came back as %q", got)
	}
}

func TestEncodedCommandIsUTF16LE(t *testing.T) {
	b, err := base64.StdEncoding.DecodeString(encodedCommand("a’"))
	if err != nil || len(b) != 4 {
		t.Fatalf("got %v %v", b, err)
	}
	u := []uint16{uint16(b[0]) | uint16(b[1])<<8, uint16(b[2]) | uint16(b[3])<<8}
	if string(utf16.Decode(u)) != "a’" {
		t.Fatalf("got %v", u)
	}
}
