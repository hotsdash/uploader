package updatesig

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"testing"
)

func TestVerify(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	ph := hex.EncodeToString(pub)
	sig := base64.StdEncoding.EncodeToString(ed25519.Sign(priv, Message("2026.10.30.0100", "ABCDEF")))
	if err := Verify(ph, "2026.10.30.0100", "abcdef", sig); err != nil {
		t.Fatalf("good signature (hash case doesn't matter): %v", err)
	}
	if Verify(ph, "2026.10.30.0101", "abcdef", sig) == nil {
		t.Fatal("another version must fail")
	}
	if Verify(ph, "2026.10.30.0100", "abcdee", sig) == nil {
		t.Fatal("another hash must fail")
	}
	if Verify("zz", "2026.10.30.0100", "abcdef", sig) == nil || Verify(ph, "2026.10.30.0100", "abcdef", "") == nil {
		t.Fatal("broken key or missing signature must fail")
	}
}
