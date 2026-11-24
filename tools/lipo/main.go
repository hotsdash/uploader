// lipo joins Mac programs built for different processors into one "universal" program that runs on both Apple
// silicon and Intel Macs, like Apple's lipo -create (which isn't there when building on Linux).
//
//	go run ./tools/lipo -out hots-dash-uploader-macos hots-dash-uploader-darwin-arm64 hots-dash-uploader-darwin-amd64
package main

import (
	"encoding/binary"
	"errors"
	"flag"
	"fmt"
	"os"
)

const (
	fatMagic  = 0xcafebabe
	machMagic = 0xfeedfacf // 64-bit Mach-O, as Go writes it (little-endian on both)
	align     = 14         // each program starts on a 16 KB boundary, as Apple's lipo does for arm64
)

type thin struct {
	cpu, sub uint32
	body     []byte
}

func read(p string) (thin, error) {
	b, err := os.ReadFile(p)
	if err != nil {
		return thin{}, err
	}
	if len(b) < 12 || binary.LittleEndian.Uint32(b) != machMagic {
		return thin{}, fmt.Errorf("%s isn't a 64-bit Mac program", p)
	}
	return thin{cpu: binary.LittleEndian.Uint32(b[4:]), sub: binary.LittleEndian.Uint32(b[8:]) &^ 0xff000000, body: b}, nil
}

// fat lays the programs out: a big-endian header and one entry per program, then each program on an aligned offset.
func fat(ts []thin) ([]byte, error) {
	if len(ts) == 0 {
		return nil, errors.New("no programs")
	}
	seen := map[uint32]bool{}
	head := make([]byte, 8+20*len(ts))
	binary.BigEndian.PutUint32(head, fatMagic)
	binary.BigEndian.PutUint32(head[4:], uint32(len(ts)))
	size := len(head)
	offs := make([]int, len(ts))
	for i, t := range ts {
		if seen[t.cpu] {
			return nil, fmt.Errorf("two programs for the same processor (%#x)", t.cpu)
		}
		seen[t.cpu] = true
		offs[i] = (size + 1<<align - 1) &^ (1<<align - 1)
		size = offs[i] + len(t.body)
		e := head[8+20*i:]
		binary.BigEndian.PutUint32(e, t.cpu)
		binary.BigEndian.PutUint32(e[4:], t.sub)
		binary.BigEndian.PutUint32(e[8:], uint32(offs[i]))
		binary.BigEndian.PutUint32(e[12:], uint32(len(t.body)))
		binary.BigEndian.PutUint32(e[16:], align)
	}
	out := make([]byte, size)
	copy(out, head)
	for i, t := range ts {
		copy(out[offs[i]:], t.body)
	}
	return out, nil
}

func main() {
	out := flag.String("out", "", "the universal program to write")
	flag.Parse()
	if *out == "" || flag.NArg() < 2 {
		fmt.Fprintln(os.Stderr, "usage: lipo -out <file> <mac program> <mac program>...")
		os.Exit(2)
	}
	var ts []thin
	for _, p := range flag.Args() {
		t, err := read(p)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		ts = append(ts, t)
	}
	b, err := fat(ts)
	if err == nil {
		err = os.WriteFile(*out, b, 0o755)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
