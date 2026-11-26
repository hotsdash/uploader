// Package updatesig is what an uploader update's signature covers, shared by the signing tool and the uploader.
package updatesig

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"
)

// Message: the bytes signed for one update. It names the version, so a signed program can't be handed out as
// a different (newer-looking) version.
func Message(version, sha256hex string) []byte {
	return []byte("hots-dash-uploader update\nversion " + version + "\nsha256 " + strings.ToLower(sha256hex) + "\n")
}

// Verify checks a base64 signature over Message(version, sha256hex) with a hex public key.
func Verify(pubHex, version, sha256hex, sigB64 string) error {
	pub, err := publicKey(pubHex)
	if err != nil {
		return err
	}
	return check([]ed25519.PublicKey{pub}, Message(version, sha256hex), sigB64)
}

// VerifyAny is Verify with a comma-separated list of hex public keys: any one of them will do. Every entry has to be a
// key, so a typo is an error rather than quietly trusting fewer keys than meant.
func VerifyAny(pubHexList, version, sha256hex, sigB64 string) error {
	var pubs []ed25519.PublicKey
	for _, h := range strings.Split(pubHexList, ",") {
		pub, err := publicKey(h)
		if err != nil {
			return err
		}
		pubs = append(pubs, pub)
	}
	return check(pubs, Message(version, sha256hex), sigB64)
}

func publicKey(pubHex string) (ed25519.PublicKey, error) {
	pub, err := hex.DecodeString(strings.TrimSpace(pubHex))
	if err != nil || len(pub) != ed25519.PublicKeySize {
		return nil, errors.New("the update key built into this uploader is broken")
	}
	return pub, nil
}

func check(pubs []ed25519.PublicKey, msg []byte, sigB64 string) error {
	if sigB64 == "" {
		return errors.New("the update isn't signed")
	}
	sig, err := base64.StdEncoding.DecodeString(strings.TrimSpace(sigB64))
	if err != nil || len(sig) != ed25519.SignatureSize {
		return errors.New("the update's signature is malformed")
	}
	for _, pub := range pubs {
		if ed25519.Verify(pub, msg, sig) {
			return nil
		}
	}
	return errors.New("the update's signature doesn't match: it wasn't signed by hots-dash")
}
