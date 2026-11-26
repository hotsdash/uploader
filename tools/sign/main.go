// sign: the uploader's update signing key, used by the release workflow (.github/workflows/release.yml).
//
//	go run ./tools/sign keygen                       prints a new private key (base64; keep it secret)
//	go run ./tools/sign pub  -key FILE               prints its public key (hex, built into the uploader)
//	go run ./tools/sign sign -key FILE -version V -platform P -in EXE -out SIG
//
// P is the system the program is for, as latest.json names it (windows, linux-amd64, linux-arm64, macos). Two
// signatures are written: SIG (v1) covers the version and the program's sha256, for copies already installed; the
// .sig2 beside it covers the platform as well (see the updatesig package). So a dashboard can only hand out programs
// that were signed as that version, and for that system.
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/hotsdash/uploader/updatesig"
)

func main() {
	if len(os.Args) < 2 {
		fail(errors.New("usage: sign keygen | pub -key FILE | sign -key FILE -version V -platform P -in EXE -out SIG"))
	}
	fs := flag.NewFlagSet(os.Args[1], flag.ExitOnError)
	keyFile := fs.String("key", "", "private key file (base64 seed)")
	ver := fs.String("version", "", "the version being signed")
	platform := fs.String("platform", "", "the system the program is for: "+strings.Join(updatesig.Platforms, ", "))
	in := fs.String("in", "", "the program")
	out := fs.String("out", "", "where to write the signature (base64)")
	_ = fs.Parse(os.Args[2:])
	switch os.Args[1] {
	case "keygen":
		_, priv, err := ed25519.GenerateKey(rand.Reader)
		fail(err)
		fmt.Println(base64.StdEncoding.EncodeToString(priv.Seed()))
	case "pub":
		priv := load(*keyFile)
		fmt.Println(hex.EncodeToString(priv.Public().(ed25519.PublicKey)))
	case "sign":
		if *ver == "" || *platform == "" || *in == "" || *out == "" {
			fail(errors.New("sign needs -version, -platform, -in and -out"))
		}
		fail(signProgram(load(*keyFile), *ver, *platform, *in, *out))
	default:
		fail(fmt.Errorf("unknown command %q", os.Args[1]))
	}
}

// signProgram writes the v1 signature of the program at in to out, and the v2 one beside it (.sig2).
func signProgram(priv ed25519.PrivateKey, version, platform, in, out string) error {
	// a mistyped platform would make a signature every copy refuses
	if !slices.Contains(updatesig.Platforms, platform) {
		return fmt.Errorf("unknown platform %q (one of %s)", platform, strings.Join(updatesig.Platforms, ", "))
	}
	b, err := os.ReadFile(in)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(b)
	sumHex := hex.EncodeToString(sum[:])
	for path, msg := range map[string][]byte{
		out: updatesig.Message(version, sumHex),
		strings.TrimSuffix(out, ".sig") + ".sig2": updatesig.MessageV2(version, sumHex, platform),
	} {
		sig := base64.StdEncoding.EncodeToString(ed25519.Sign(priv, msg))
		if err := os.WriteFile(path, []byte(sig), 0o644); err != nil {
			return err
		}
	}
	return nil
}

func load(path string) ed25519.PrivateKey {
	if path == "" {
		fail(errors.New("-key is needed"))
	}
	b, err := os.ReadFile(path)
	fail(err)
	seed, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(b)))
	fail(err)
	if len(seed) != ed25519.SeedSize {
		fail(errors.New("the key should be a base64 32-byte seed (from keygen)"))
	}
	return ed25519.NewKeyFromSeed(seed)
}

func fail(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "sign:", err)
		os.Exit(1)
	}
}
