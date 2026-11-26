package main

import (
	"encoding/binary"
	"encoding/hex"
	"strings"
)

// The Start menu shortcut is made through COM (shortcut_windows.go). It also carries the app's ID
// (System.AppUserModel.ID), which is how Windows knows whose notifications are whose. What that needs and can be
// checked on any system is here.

// appUserModelID: the uploader's name to Windows. Changing it loses the person's notification settings for it.
const appUserModelID = "HotsDash.Uploader"

// guid is Windows' GUID, laid out as in memory.
type guid struct {
	Data1 uint32
	Data2 uint16
	Data3 uint16
	Data4 [8]byte
}

// mustGUID reads a GUID written as in the Windows headers, 8-4-4-4-12.
func mustGUID(s string) guid {
	b, err := hex.DecodeString(strings.ReplaceAll(s, "-", ""))
	if err != nil || len(s) != 36 || len(b) != 16 {
		panic("bad GUID " + s)
	}
	g := guid{Data1: binary.BigEndian.Uint32(b), Data2: binary.BigEndian.Uint16(b[4:]), Data3: binary.BigEndian.Uint16(b[6:])}
	copy(g.Data4[:], b[8:])
	return g
}

// propertyKey is Windows' PROPERTYKEY.
type propertyKey struct {
	fmtid guid
	pid   uint32
}

// pkeyAppUserModelID is PKEY_AppUserModel_ID (propkey.h).
var pkeyAppUserModelID = propertyKey{mustGUID("9f4c2855-9f79-4b39-a8d0-e1d42de1d5f3"), 5}

// propVariant is a PROPVARIANT holding a string: the type, three reserved words, then a union two pointers wide.
type propVariant struct {
	vt  uint16
	_   [3]uint16
	str *uint16
	_   uintptr
}

const vtLPWSTR = 31
