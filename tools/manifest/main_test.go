package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/hotsdash/uploader/updatesig"
)

func fixture(t *testing.T, sign bool) string {
	d := t.TempDir()
	for _, s := range systems {
		_ = os.WriteFile(filepath.Join(d, s.program), []byte("program "+s.key), 0o755)
		_ = os.WriteFile(filepath.Join(d, s.download), []byte("download "+s.key), 0o644)
		if sign {
			_ = os.WriteFile(filepath.Join(d, s.program+".sig"), []byte("c2ln"+s.key+"\n"), 0o644)
			_ = os.WriteFile(filepath.Join(d, s.program+".sig2"), []byte("djI="+s.key+"\n"), 0o644)
		}
	}
	return d
}

func TestManifestShapeMatchesWhatTheDashboardReads(t *testing.T) {
	b, err := build(fixture(t, true), "2026.11.25.1430", false)
	if err != nil {
		t.Fatal(err)
	}
	var m struct {
		Version string
		Files   map[string]struct {
			Program struct {
				Name        string
				Size        int64
				SHA256      string
				Signature   string
				SignatureV2 string `json:"signatureV2"`
			}
			Download string
		}
	}
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("%v\n%s", err, b)
	}
	w := m.Files["windows"]
	if m.Version != "2026.11.25.1430" || len(m.Files) != 4 || w.Program.Name != "hots-dash-uploader.exe" || w.Download != "hots-dash-uploader.exe" ||
		w.Program.Size != int64(len("download windows")) /* the .exe is both the program and the download */ || len(w.Program.SHA256) != 64 || w.Program.Signature != "c2lnwindows" || w.Program.SignatureV2 != "djI=windows" {
		t.Fatalf("%+v", m)
	}
	if l := m.Files["macos"]; l.Program.Name != "hots-dash-uploader-macos" || l.Download != "hots-dash-uploader-macos.tar.gz" {
		t.Fatalf("%+v", l)
	}
	b2, _ := build(fixture(t, true), "2026.11.25.1430", false)
	if string(b) != string(b2) {
		t.Fatal("not repeatable")
	}
}

// v2 signatures name the system, so the signing tool and the manifest have to call each one the same.
func TestManifestSystemsAreTheSignedPlatforms(t *testing.T) {
	var keys []string
	for _, s := range systems {
		keys = append(keys, s.key)
	}
	if !slices.Equal(keys, updatesig.Platforms) {
		t.Fatalf("%v != %v", keys, updatesig.Platforms)
	}
}

func TestManifestNeedsBothSignatures(t *testing.T) {
	for _, ext := range []string{".sig", ".sig2"} {
		d := fixture(t, true)
		if err := os.Remove(filepath.Join(d, systems[1].program+ext)); err != nil {
			t.Fatal(err)
		}
		if _, err := build(d, "2026.11.25.1430", false); err == nil {
			t.Errorf("released without a %s", ext)
		}
	}
}

func TestManifestRefusesUnsignedPrograms(t *testing.T) {
	if _, err := build(fixture(t, false), "2026.11.25.1430", false); err == nil {
		t.Fatal("unsigned release accepted")
	}
	if _, err := build(fixture(t, false), "2026.11.25.1430", true); err != nil {
		t.Fatal(err)
	}
}
