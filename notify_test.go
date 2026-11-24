package main

import (
	"encoding/base64"
	"regexp"
	"strings"
	"testing"
)

// Text from the dashboard must never reach the PowerShell script as text: curly quotes end a single-quoted
// PowerShell string just like ' does.
func TestToastTextNeverReachesTheScript(t *testing.T) {
	payloads := []string{
		"Nice game'); Start-Process calc; ('",
		"Nice game’); Start-Process calc; (‘",
		"Nice game‚); Start-Process calc; (‛",
		"$(Start-Process calc)",
		"`$env:USERNAME",
	}
	b64 := regexp.MustCompile(`FromBase64String\('([A-Za-z0-9+/=]*)'\)`)
	for _, p := range payloads {
		xml := toastXML(p, p+"\nsecond line "+p, "https://hots.example.com/matches/1")
		ps := toastScript(xml)
		if strings.Contains(ps, "Start-Process") || strings.Contains(ps, "USERNAME") {
			t.Fatalf("text reached the script for %q", p)
		}
		for _, q := range []string{"‘", "’", "‚", "‛"} {
			if strings.Contains(ps, q) {
				t.Fatalf("a curly quote reached the script for %q", p)
			}
		}
		m := b64.FindStringSubmatch(ps)
		if m == nil {
			t.Fatal("no base64 XML in the script")
		}
		got, err := base64.StdEncoding.DecodeString(m[1])
		if err != nil || string(got) != xml {
			t.Fatalf("the XML doesn't round-trip: %v", err)
		}
	}
}

func TestToastXML(t *testing.T) {
	x := toastXML(`A & B <win>`, "one\n\ntwo\x07", "https://hots.example.com/live")
	want := `<toast activationType="protocol" launch="https://hots.example.com/live"><visual><binding template="ToastGeneric"><text>A &amp; B &lt;win&gt;</text><text>one</text><text>two</text></binding></visual></toast>`
	if x != want {
		t.Fatalf("got  %s\nwant %s", x, want)
	}
	if strings.Contains(toastXML("t", "m", `\\evil.example\share\x.exe`), "launch=") {
		t.Fatal("only web addresses open from a notification")
	}
}

func TestLongToastsAreClipped(t *testing.T) {
	ps := toastScript(toastXML(strings.Repeat("t", 5000), strings.Repeat("m", 50000), ""))
	if len(ps) > 12000 { // -EncodedCommand doubles it (UTF-16) then base64s it: stay well under 32K
		t.Fatalf("script is %d bytes", len(ps))
	}
}
