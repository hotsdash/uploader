package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"github.com/hotsdash/uploader/internal/updatesig"
)

func TestSignProgramWritesBothSignatures(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader) // thrown away with the test
	ph := hex.EncodeToString(pub)
	d := t.TempDir()
	prog := filepath.Join(d, "hots-dash-uploader-linux-amd64")
	if err := os.WriteFile(prog, []byte("program"), 0o755); err != nil {
		t.Fatal(err)
	}
	const ver = "2026.11.25.1430"
	if err := signProgram(priv, ver, "linux-amd64", prog, prog+".sig"); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte("program"))
	sumHex := hex.EncodeToString(sum[:])
	v1, err1 := os.ReadFile(prog + ".sig")
	v2, err2 := os.ReadFile(prog + ".sig2")
	if err1 != nil || err2 != nil {
		t.Fatal(err1, err2)
	}
	if err := updatesig.VerifyAny(ph, ver, sumHex, string(v1)); err != nil {
		t.Errorf("v1: %v", err)
	}
	if err := updatesig.VerifyAnyV2(ph, ver, sumHex, "linux-amd64", string(v2)); err != nil {
		t.Errorf("v2: %v", err)
	}
	if updatesig.VerifyAnyV2(ph, ver, sumHex, "windows", string(v2)) == nil {
		t.Error("the linux-amd64 signature was accepted for windows")
	}
	if err := signProgram(priv, ver, "linux_amd64", prog, filepath.Join(d, "x.sig")); err == nil {
		t.Error("a mistyped platform was signed")
	}
}
