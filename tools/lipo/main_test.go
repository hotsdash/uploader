package main

import (
	"encoding/binary"
	"testing"
)

func machO(cpu, sub uint32, n int) thin {
	b := make([]byte, n)
	binary.LittleEndian.PutUint32(b, machMagic)
	binary.LittleEndian.PutUint32(b[4:], cpu)
	binary.LittleEndian.PutUint32(b[8:], sub)
	return thin{cpu: cpu, sub: sub, body: b}
}

func TestFatLayout(t *testing.T) {
	arm := machO(0x0100000c, 0, 100)
	x86 := machO(0x01000007, 3, 50)
	b, err := fat([]thin{arm, x86})
	if err != nil {
		t.Fatal(err)
	}
	if binary.BigEndian.Uint32(b) != fatMagic || binary.BigEndian.Uint32(b[4:]) != 2 {
		t.Fatal("header")
	}
	for i, want := range []thin{arm, x86} {
		e := b[8+20*i:]
		cpu, off, size := binary.BigEndian.Uint32(e), binary.BigEndian.Uint32(e[8:]), binary.BigEndian.Uint32(e[12:])
		if cpu != want.cpu || size != uint32(len(want.body)) || off%(1<<align) != 0 || binary.BigEndian.Uint32(e[16:]) != align {
			t.Fatalf("entry %d: cpu %#x off %d size %d", i, cpu, off, size)
		}
		if string(b[off:off+size]) != string(want.body) {
			t.Fatalf("entry %d: body moved", i)
		}
	}
	if _, err := fat([]thin{arm, arm}); err == nil {
		t.Fatal("the same processor twice")
	}
}
