package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/hotsdash/uploader/updatesig"
)

func TestSignedUpdates(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	prog := fakeProgram("pretend this is a program")
	sum := sha256.Sum256(prog)
	sumHex := hex.EncodeToString(sum[:])
	sign := func(k ed25519.PrivateKey, ver string) string {
		return base64.StdEncoding.EncodeToString(ed25519.Sign(k, updatesig.Message(ver, sumHex)))
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(prog) }))
	defer srv.Close()
	dir := t.TempDir()
	a := NewAgent(Config{Server: srv.URL, Folders: []Folder{{Path: dir}}}, dir, log.New(io.Discard, "", 0))

	old := updateKey
	updateKey = hex.EncodeToString(pub)
	defer func() { updateKey = old }()

	good := latestInfo{Version: "2099.01.01.0000", SHA256: sumHex, Size: int64(len(prog)), Signature: sign(priv, "2099.01.01.0000")}
	if _, err := a.download(&good); err != nil {
		t.Fatalf("a signed update should be accepted: %v", err)
	}

	_, other, _ := ed25519.GenerateKey(rand.Reader)
	cases := map[string]latestInfo{
		"unsigned":                  {Version: good.Version, SHA256: sumHex, Size: good.Size},
		"signed by another key":     {Version: good.Version, SHA256: sumHex, Size: good.Size, Signature: sign(other, good.Version)},
		"signed as another version": {Version: "2099.12.31.2359", SHA256: sumHex, Size: good.Size, Signature: sign(priv, good.Version)},
		"garbage signature":         {Version: good.Version, SHA256: sumHex, Size: good.Size, Signature: "bm9wZQ=="},
		"no hash":                   {Version: good.Version, Size: good.Size, Signature: good.Signature},
	}
	for name, v := range cases {
		v := v
		p, err := a.download(&v)
		if err == nil {
			t.Errorf("%s: accepted", name)
		}
		if p != "" {
			if _, err := os.Stat(p); err == nil {
				t.Errorf("%s: refused download left on disk", name)
			}
		}
	}
}

// Either of the keys built in will do, so one can be retired with a release signed by the other.
func TestUpdatesSignedByEitherTrustedKey(t *testing.T) {
	pubA, privA, _ := ed25519.GenerateKey(rand.Reader)
	pubB, privB, _ := ed25519.GenerateKey(rand.Reader)
	prog := fakeProgram("pretend this is a program")
	sum := sha256.Sum256(prog)
	sumHex := hex.EncodeToString(sum[:])
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(prog) }))
	defer srv.Close()
	dir := t.TempDir()
	a := NewAgent(Config{Server: srv.URL, Folders: []Folder{{Path: dir}}}, dir, log.New(io.Discard, "", 0))

	old := updateKey
	updateKey = hex.EncodeToString(pubA) + "," + hex.EncodeToString(pubB)
	defer func() { updateKey = old }()

	for name, k := range map[string]ed25519.PrivateKey{"release key": privA, "backup key": privB} {
		v := latestInfo{Version: "2099.01.01.0000", SHA256: sumHex, Size: int64(len(prog)),
			Signature: base64.StdEncoding.EncodeToString(ed25519.Sign(k, updatesig.Message("2099.01.01.0000", sumHex)))}
		if _, err := a.download(&v); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// A copy with a version but no key has nothing to check a signature against, so it doesn't update at all. A build
// from source ("dev") is as it was.
func TestVersionedBuildsNeedAnUpdateKey(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	prog := fakeProgram("pretend this is a program")
	sum := sha256.Sum256(prog)
	sumHex := hex.EncodeToString(sum[:])
	info := latestInfo{Version: "2099.01.01.0000", SHA256: sumHex, Size: int64(len(prog)),
		Signature: base64.StdEncoding.EncodeToString(ed25519.Sign(priv, updatesig.Message("2099.01.01.0000", sumHex)))}
	var downloads int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/uploader/version" {
			_ = json.NewEncoder(w).Encode(info)
			return
		}
		downloads++
		_, _ = w.Write(prog)
	}))
	defer srv.Close()
	dir := t.TempDir()
	a := NewAgent(Config{Server: srv.URL, Folders: []Folder{{Path: dir}}}, dir, log.New(io.Discard, "", 0))
	var note string
	a.Notify = func(_, message, _ string) { note = message }

	oldKey, oldVersion, oldApply := updateKey, version, applyUpdate
	defer func() { updateKey, version, applyUpdate = oldKey, oldVersion, oldApply }()
	var applied int
	applyUpdate = func(string) error { applied++; return nil }

	updateKey, version = "", "2026.11.24.1717"
	if p, err := a.download(&info); err == nil || p != "" || !strings.Contains(err.Error(), "no update key") {
		t.Fatalf("versioned, no key: %q %v", p, err)
	}
	a.checkUpdate() // automatic updates are on, but it mustn't try
	if downloads != 0 || applied != 0 {
		t.Fatalf("versioned, no key: %d downloads, %d applied", downloads, applied)
	}
	if st := a.UpdateState(); st.CanApply || !st.Available || st.Error != "" {
		t.Fatalf("versioned, no key: %+v", st)
	}
	if !strings.Contains(note, "Download it from the dashboard") {
		t.Fatalf("told: %q", note)
	}

	version = "dev"
	if _, err := a.download(&info); err != nil {
		t.Fatalf("dev, no key: %v", err)
	}
	if !a.UpdateState().CanApply {
		t.Fatal("dev: CanApply as before")
	}

	updateKey, version = hex.EncodeToString(pub), "2026.11.24.1717"
	if !a.UpdateState().CanApply {
		t.Fatal("versioned with a key: can apply")
	}
	downloads = 0
	a.checkUpdate()
	if downloads != 1 || applied != 1 {
		t.Fatalf("versioned with a key: %d downloads, %d applied", downloads, applied)
	}
}
