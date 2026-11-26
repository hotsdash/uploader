// sign: the uploader's update signing key, used by the release workflow (.github/workflows/release.yml).
//
//	go run ./tools/sign keygen                       prints a new private key (base64; keep it secret)
//	go run ./tools/sign pub  -key FILE               prints its public key (hex, built into the uploader)
//	go run ./tools/sign sign -key FILE -version V -in EXE -out SIG
//
// The signature covers the version and the program's sha256 (see the updatesig package), so a dashboard can only hand out
// programs that were signed as that version.
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
	"strings"

	"github.com/hotsdash/uploader/updatesig"
)

func main() {
	if len(os.Args) < 2 {
		fail(errors.New("usage: sign keygen | pub -key FILE | sign -key FILE -version V -in EXE -out SIG"))
	}
	fs := flag.NewFlagSet(os.Args[1], flag.ExitOnError)
	keyFile := fs.String("key", "", "private key file (base64 seed)")
	ver := fs.String("version", "", "the version being signed")
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
		if *ver == "" || *in == "" || *out == "" {
			fail(errors.New("sign needs -version, -in and -out"))
		}
		priv := load(*keyFile)
		b, err := os.ReadFile(*in)
		fail(err)
		sum := sha256.Sum256(b)
		sig := ed25519.Sign(priv, updatesig.Message(*ver, hex.EncodeToString(sum[:])))
		fail(os.WriteFile(*out, []byte(base64.StdEncoding.EncodeToString(sig)), 0o644))
	default:
		fail(fmt.Errorf("unknown command %q", os.Args[1]))
	}
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
