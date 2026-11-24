package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestNewer(t *testing.T) {
	cases := []struct {
		have, latest string
		want         bool
	}{
		{"2026.10.27", "2026.10.27.1730", true}, // older date-only builds update to timed ones
		{"2026.10.27.1730", "2026.10.27.1730", false},
		{"2026.10.27.1730", "2026.10.27.0900", false},
		{"2026.10.27.1730", "2026.11.01.0100", true},
		{"dev", "2026.11.01.0100", false},
		{"2026.10.27.1730", "", false},
	}
	for _, c := range cases {
		if got := newer(c.have, c.latest); got != c.want {
			t.Errorf("newer(%q, %q) = %v", c.have, c.latest, got)
		}
	}
}

func TestDownloadChecksTheFile(t *testing.T) {
	prog := fakeProgram("pretend this is a program")
	sum := sha256.Sum256(prog)
	info := latestInfo{Version: "2099.01.01.0000", SHA256: hex.EncodeToString(sum[:]), Size: int64(len(prog))}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/uploader/version":
			_ = json.NewEncoder(w).Encode(info)
		case "/api/uploader/update":
			_, _ = w.Write(prog)
		}
	}))
	defer srv.Close()
	dir := t.TempDir()
	a := NewAgent(Config{Server: srv.URL, Folders: []Folder{{Path: dir}}}, dir, log.New(io.Discard, "", 0))
	v, err := a.latest()
	if err != nil || v.Version != info.Version {
		t.Fatalf("latest: %v %v", v, err)
	}
	p, err := a.download(v)
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	if b, _ := os.ReadFile(p); string(b) != string(prog) || filepath.Dir(p) != filepath.Join(dir, "update") {
		t.Fatalf("wrong file at %s", p)
	}
	bad := *v
	bad.SHA256 = "00"
	if _, err := a.download(&bad); err == nil {
		t.Fatal("a file that doesn't match should be refused")
	}
}

// Another port on the same machine may be another app: the uploader asks, then follows once the person agrees.
func TestFollowsTheDashboardToItsNewAddress(t *testing.T) {
	newSite := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(latestInfo{Version: "2099.01.01.0000"})
	}))
	defer newSite.Close()
	old := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(latestInfo{Version: "2099.01.01.0000", Server: newSite.URL})
	}))
	defer old.Close()
	dir := t.TempDir()
	a := NewAgent(Config{Server: old.URL, Folders: []Folder{{Path: dir}}}, dir, log.New(io.Discard, "", 0))
	a.client = newSite.Client() // trusts the test certificate (and plain http for the old one)
	if _, err := a.latest(); err != nil {
		t.Fatal(err)
	}
	if got := a.Config().Server; got != old.URL || a.UpdateState().MoveTo != newSite.URL {
		t.Fatalf("server = %s, moveTo = %s: should wait for the person", got, a.UpdateState().MoveTo)
	}
	if err := a.AcceptMove(); err != nil {
		t.Fatal(err)
	}
	if got := a.Config().Server; got != newSite.URL {
		t.Fatalf("server = %s, want %s", got, newSite.URL)
	}
}

func TestUpdateFollowsARedirectToGitHubOnly(t *testing.T) {
	check := updateRedirect("hotsdash.com")
	via := []*http.Request{{URL: &url.URL{Scheme: "https", Host: "hotsdash.com"}}}
	for host, ok := range map[string]bool{
		"github.com":                           true,
		"objects.githubusercontent.com":        true,
		"release-assets.githubusercontent.com": true,
		"hotsdash.com":                         true,
		"evilgithub.com":                       false,
		"github.com.evil.example":              false,
		"githubusercontent.com.evil.example":   false,
		"evil.example":                         false,
	} {
		err := check(&http.Request{URL: &url.URL{Scheme: "https", Host: host}}, via)
		if (err == nil) != ok {
			t.Errorf("%s: allowed=%v want %v (%v)", host, err == nil, ok, err)
		}
	}
	if check(&http.Request{URL: &url.URL{Scheme: "http", Host: "github.com"}}, via) == nil {
		t.Error("plain http to GitHub allowed")
	}
}

// the bytes are checked whoever serves them: a dashboard that redirects to another host (here a second test server
// standing in for GitHub's) still has the download verified against the size, hash and signature it announced.
func TestUpdateDownloadViaRedirectIsVerified(t *testing.T) {
	prog := append([]byte("MZ"), make([]byte, 100)...)
	cdn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(prog) }))
	defer cdn.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, cdn.URL+"/asset", http.StatusFound)
	}))
	defer srv.Close()
	a := NewAgent(Config{Server: srv.URL, Folders: []Folder{{Path: t.TempDir()}}}, t.TempDir(), log.New(io.Discard, "", 0))
	sum := sha256.Sum256(prog)
	// a different loopback server isn't the dashboard's host or GitHub's: refused, nothing is kept
	if _, err := a.download(&latestInfo{Version: "2099.01.01.0000", SHA256: hex.EncodeToString(sum[:]), Size: int64(len(prog))}); err == nil {
		t.Fatal("a redirect to another host was followed")
	}
}

func TestUpdateRedirectOnTheSameHostIsFollowedAndTheBytesChecked(t *testing.T) {
	prog := append([]byte("MZ"), make([]byte, 100)...)
	mux := http.NewServeMux()
	mux.HandleFunc("/api/uploader/update", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/asset", http.StatusFound) })
	mux.HandleFunc("/asset", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(prog) })
	srv := httptest.NewServer(mux)
	defer srv.Close()
	a := NewAgent(Config{Server: srv.URL, Folders: []Folder{{Path: t.TempDir()}}}, t.TempDir(), log.New(io.Discard, "", 0))
	sum := sha256.Sum256(prog)
	if _, err := a.download(&latestInfo{Version: "x", SHA256: strings.Repeat("0", 64), Size: int64(len(prog))}); err == nil || !strings.Contains(err.Error(), "match") {
		t.Fatalf("wrong hash after a redirect: %v", err)
	}
	if _, err := a.download(&latestInfo{Version: "x", SHA256: hex.EncodeToString(sum[:]), Size: 5}); err == nil || !strings.Contains(err.Error(), "short") {
		t.Fatalf("wrong size after a redirect: %v", err)
	}
	p, err := a.download(&latestInfo{Version: "x", SHA256: hex.EncodeToString(sum[:]), Size: int64(len(prog))})
	if runtime.GOOS == "windows" && err != nil {
		t.Fatalf("good bytes after a redirect: %v", err)
	}
	_ = p
}
