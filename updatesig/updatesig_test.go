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

func TestVerifyAny(t *testing.T) {
	pubA, privA, _ := ed25519.GenerateKey(rand.Reader)
	pubB, privB, _ := ed25519.GenerateKey(rand.Reader)
	_, privC, _ := ed25519.GenerateKey(rand.Reader)
	a, b := hex.EncodeToString(pubA), hex.EncodeToString(pubB)
	sign := func(k ed25519.PrivateKey) string {
		return base64.StdEncoding.EncodeToString(ed25519.Sign(k, Message("2026.10.30.0100", "abcd")))
	}
	verify := func(list, sig string) error { return VerifyAny(list, "2026.10.30.0100", "abcd", sig) }
	for name, k := range map[string]ed25519.PrivateKey{"first key": privA, "second key": privB} {
		if err := verify(a+","+b, sign(k)); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	if err := verify(a, sign(privA)); err != nil {
		t.Errorf("a list of one: %v", err)
	}
	if verify(a+","+b, sign(privC)) == nil {
		t.Error("neither key signed it")
	}
	if verify(a+","+b, "") == nil {
		t.Error("unsigned")
	}
	// a broken entry anywhere fails, even when a good key signed
	for _, list := range []string{"", a + ",", "," + a, a + ",,", a + ",zz", "zz," + a, a + "," + b[:62], a + " " + b} {
		if verify(list, sign(privA)) == nil {
			t.Errorf("malformed list %q accepted", list)
		}
	}
}
