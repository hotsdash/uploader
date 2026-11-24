package main

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWebLink(t *testing.T) {
	ok := []string{
		"https://hots.example.com",
		"https://hots.example.com/link?code=ABCD-EFGH",
		"http://127.0.0.1:51234/?k=0123",
		"http://home.example.org/hots/accounts",
	}
	bad := []string{
		"",
		`C:\Windows\System32\cmd.exe`,
		`\\evil.example\share\x.exe`,
		"file:///C:/Windows/System32/calc.exe",
		"ms-msdt:/id PCWDiagnostic",
		"search-ms:query=x&crumb=location:\\\\evil\\share",
		"javascript:alert(1)",
		"https://",
		"https:evil.example",
		"https://user:pass@hots.example.com",
		"https://hots.example.com/ x",
		"https://hots.example.com/\\..\\x",
		"ftp://hots.example.com",
		"cmd.exe /c calc",
		`https://hots.example.com/link?code="x" --gpu-launcher=calc`,
		"https://hots.example.com/?q=\"x",
		"https://hots.example.com/'x",
		"https://hots.example.com/a|b",
		"https://hots.example.com/a^b",
		"https://hots.example.com/\u00e9",
	}
	for _, u := range ok {
		if !webLink(u) {
			t.Errorf("webLink(%q) = false, want true", u)
		}
	}
	for _, u := range bad {
		if webLink(u) {
			t.Errorf("webLink(%q) = true, want false", u)
		}
	}
}

func TestOnSite(t *testing.T) {
	server := "https://hots.example.com/hots"
	cases := map[string]bool{
		"https://hots.example.com/hots/link?code=AB": true,
		"https://HOTS.example.com/hots/link":         true,
		"https://hots.example.com/link":              false, // another app on the same proxy
		"https://hots.example.com/hotsx/link":        false,
		"http://hots.example.com/hots/link":          false, // https dashboard, http link
		"https://hots.example.com:8443/link":         false,
		"https://evil.example/hots/link":             false,
		"https://hots.example.com.evil.example/link": false,
		`\\hots.example.com\share`:                   false,
	}
	for u, want := range cases {
		if got := onSite(u, server); got != want {
			t.Errorf("onSite(%q) = %v, want %v", u, got, want)
		}
	}
}

func TestSignInRefusesAnApprovalPageOnAnotherSite(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(linkStart{DeviceCode: "d", UserCode: "ABCD-EFGH", VerifyURL: `\\evil.example\share\x.exe`, Interval: 2, ExpiresIn: 60})
	}))
	defer srv.Close()
	dir := t.TempDir()
	a := NewAgent(Config{Server: srv.URL, Folders: []Folder{{Path: dir}}}, dir, log.New(io.Discard, "", 0))
	_, err := a.StartSignIn()
	if err == nil || !strings.Contains(err.Error(), "another site") {
		t.Fatalf("want a refusal, got %v", err)
	}
	if a.SignInState().Pending {
		t.Fatal("nothing should be waiting for approval")
	}
}
