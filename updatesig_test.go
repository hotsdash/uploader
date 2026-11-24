package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
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
