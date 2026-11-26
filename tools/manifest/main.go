// manifest: writes latest.json, which the dashboard reads to know what the newest uploader is and how to check it.
//
//	go run ./tools/manifest -dir dist -version 2026.11.25.1430 -out dist/latest.json
//
// For each system: the bare program the updater fetches and verifies (name, size, sha256, and the Ed25519 signatures:
// "signature" from <program>.sig, over updatesig.Message(version, sha256), and "signatureV2" from <program>.sig2, over
// updatesig.MessageV2(version, sha256, system)), and the file a first install downloads.
// A program without both signatures is an error (-unsigned allows it, for trying a build locally).
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type program struct {
	Name        string `json:"name"`
	Size        int64  `json:"size"`
	SHA256      string `json:"sha256"`
	Signature   string `json:"signature"`
	SignatureV2 string `json:"signatureV2"`
}

type file struct {
	Program  program `json:"program"`
	Download string  `json:"download"`
}

// systems, in the order they're written: the program, and the first-install download.
var systems = []struct{ key, program, download string }{
	{"windows", "hots-dash-uploader.exe", "hots-dash-uploader.exe"},
	{"linux-amd64", "hots-dash-uploader-linux-amd64", "hots-dash-uploader-linux-amd64.tar.gz"},
	{"linux-arm64", "hots-dash-uploader-linux-arm64", "hots-dash-uploader-linux-arm64.tar.gz"},
	{"macos", "hots-dash-uploader-macos", "hots-dash-uploader-macos.tar.gz"},
}

func build(dir, version string, unsigned bool) ([]byte, error) {
	if version == "" {
		return nil, errors.New("-version is needed")
	}
	var b strings.Builder
	fmt.Fprintf(&b, `{"version":%q,"files":{`, version)
	for i, s := range systems {
		data, err := os.ReadFile(filepath.Join(dir, s.program))
		if err != nil {
			return nil, err
		}
		if _, err := os.Stat(filepath.Join(dir, s.download)); err != nil {
			return nil, err
		}
		read := func(ext string) string {
			raw, err := os.ReadFile(filepath.Join(dir, s.program+ext))
			if err != nil {
				return ""
			}
			return strings.TrimSpace(string(raw))
		}
		sig, sig2 := read(".sig"), read(".sig2")
		if (sig == "" || sig2 == "") && !unsigned {
			return nil, fmt.Errorf("%s isn't signed", s.program)
		}
		sum := sha256.Sum256(data)
		f, _ := json.Marshal(file{program{s.program, int64(len(data)), hex.EncodeToString(sum[:]), sig, sig2}, s.download})
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, "%q:%s", s.key, f)
	}
	b.WriteString("}}\n")
	return []byte(b.String()), nil
}

func main() {
	dir := flag.String("dir", "dist", "where the programs, signatures and downloads are")
	version := flag.String("version", "", "the version")
	out := flag.String("out", "", "where to write latest.json (default: print it)")
	unsigned := flag.Bool("unsigned", false, "allow programs without a signature")
	flag.Parse()
	b, err := build(*dir, *version, *unsigned)
	if err == nil && *out != "" {
		err = os.WriteFile(*out, b, 0o644)
	} else if err == nil {
		_, err = os.Stdout.Write(b)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "manifest:", err)
		os.Exit(1)
	}
}
