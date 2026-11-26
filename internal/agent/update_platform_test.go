package agent

import (
	"encoding/binary"
	"runtime"
	"testing"
)

// An update must be a program for this system: a dashboard older than the Linux and Mac builds hands everyone the
// Windows .exe, and a Linux or Mac copy must not install that over itself.
func TestUpdateMustRunHere(t *testing.T) {
	exe := append([]byte("MZ"), make([]byte, 30)...)
	elf := func(machine uint16) []byte {
		b := make([]byte, 32)
		copy(b, "\x7fELF")
		binary.LittleEndian.PutUint16(b[18:], machine)
		return b
	}
	mach := func(cpu uint32) []byte {
		b := make([]byte, 32)
		binary.LittleEndian.PutUint32(b, 0xfeedfacf)
		binary.LittleEndian.PutUint32(b[4:], cpu)
		return b
	}
	fat := make([]byte, 32)
	binary.BigEndian.PutUint32(fat, 0xcafebabe)
	for _, c := range []struct {
		name       string
		b          []byte
		goos, arch string
		ok         bool
	}{
		{"exe on windows", exe, "windows", "amd64", true},
		{"elf on windows", elf(62), "windows", "amd64", false},
		{"exe on linux", exe, "linux", "amd64", false},
		{"x86-64 elf on linux", elf(62), "linux", "amd64", true},
		{"arm elf on x86-64 linux", elf(183), "linux", "amd64", false},
		{"arm elf on arm linux", elf(183), "linux", "arm64", true},
		{"exe on a mac", exe, "darwin", "arm64", false},
		{"universal on a mac", fat, "darwin", "arm64", true},
		{"arm64 mach-o on apple silicon", mach(0x0100000c), "darwin", "arm64", true},
		{"x86-64 mach-o on apple silicon", mach(0x01000007), "darwin", "arm64", false},
		{"too short", []byte("MZ"), "windows", "amd64", false},
	} {
		if err := programFits(c.b, c.goos, c.arch); (err == nil) != c.ok {
			t.Errorf("%s: got %v", c.name, err)
		}
	}
}

// fakeProgram: rest, behind the first bytes of a program for the system the tests run on (what runsHere looks at).
func fakeProgram(rest string) []byte {
	b := make([]byte, 20)
	switch runtime.GOOS {
	case "windows":
		copy(b, "MZ")
	case "darwin":
		binary.BigEndian.PutUint32(b, 0xcafebabe)
	default:
		copy(b, "\x7fELF")
		binary.LittleEndian.PutUint16(b[18:], map[string]uint16{"amd64": 62, "arm64": 183}[runtime.GOARCH])
	}
	return append(b, rest...)
}
