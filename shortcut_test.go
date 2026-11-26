package main

import (
	"testing"
	"unsafe"
)

func TestGUIDLayout(t *testing.T) {
	want := guid{0x9f4c2855, 0x9f79, 0x4b39, [8]byte{0xa8, 0xd0, 0xe1, 0xd4, 0x2d, 0xe1, 0xd5, 0xf3}}
	if pkeyAppUserModelID.fmtid != want || pkeyAppUserModelID.pid != 5 {
		t.Fatalf("PKEY_AppUserModel_ID: %+v", pkeyAppUserModelID)
	}
	if unsafe.Sizeof(guid{}) != 16 || unsafe.Sizeof(propertyKey{}) != 20 {
		t.Fatalf("sizes: GUID %d, PROPERTYKEY %d", unsafe.Sizeof(guid{}), unsafe.Sizeof(propertyKey{}))
	}
	// PROPVARIANT: 8 bytes of header and a two-pointer union (24 bytes on 64-bit Windows)
	var v propVariant
	if unsafe.Sizeof(v) != 8+2*unsafe.Sizeof(uintptr(0)) || unsafe.Offsetof(v.str) != 8 {
		t.Fatalf("PROPVARIANT: size %d, string at %d", unsafe.Sizeof(v), unsafe.Offsetof(v.str))
	}
}

// Windows allows an app ID of up to 128 characters, without spaces.
func TestAppUserModelID(t *testing.T) {
	if n := len(appUserModelID); n == 0 || n > 128 {
		t.Fatalf("length %d", n)
	}
	for _, r := range appUserModelID {
		if r <= ' ' || r > '~' || r == '\\' {
			t.Fatalf("%q in the app ID", r)
		}
	}
}
