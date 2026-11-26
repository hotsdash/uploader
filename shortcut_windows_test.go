//go:build windows

package main

import (
	"os"
	"path/filepath"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Makes a real shortcut (in a throwaway folder) and reads it back through COM: the program it opens and the app ID.
func TestSaveShortcut(t *testing.T) {
	dir := t.TempDir()
	target, lnk := filepath.Join(dir, "O’Brien's $(x) uploader.exe"), filepath.Join(dir, "x.lnk")
	if err := os.WriteFile(target, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ { // the second writes over the first
		if err := saveShortcut(lnk, target, target); err != nil {
			t.Fatal(err)
		}
	}
	file, err := windows.UTF16PtrFromString(lnk)
	if err != nil {
		t.Fatal(err)
	}
	err = inApartment(func() error {
		link, err := createInstance(clsidShellLink, iidShellLinkW)
		if err != nil {
			return err
		}
		defer link.release()
		persist, err := link.query(iidPersistFile)
		if err != nil {
			return err
		}
		defer persist.release()
		const persistFileLoad, shellLinkGetPath, propertyStoreGetValue, rawPath = 5, 3, 5, 0x4
		if err := persist.call(persistFileLoad, uintptr(unsafe.Pointer(file)), 0); err != nil {
			return err
		}
		buf := make([]uint16, windows.MAX_PATH)
		if err := link.call(shellLinkGetPath, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)), 0, rawPath); err != nil {
			return err
		}
		got, err := os.Stat(windows.UTF16ToString(buf))
		if err != nil {
			return err
		}
		if want, err := os.Stat(target); err != nil || !os.SameFile(got, want) {
			t.Errorf("opens %s", windows.UTF16ToString(buf))
		}
		props, err := link.query(iidPropertyStore)
		if err != nil {
			return err
		}
		defer props.release()
		key, value := pkeyAppUserModelID, propVariant{}
		if err := props.call(propertyStoreGetValue, uintptr(unsafe.Pointer(&key)), uintptr(unsafe.Pointer(&value))); err != nil {
			return err
		}
		defer ole32.NewProc("PropVariantClear").Call(uintptr(unsafe.Pointer(&value)))
		if value.vt != vtLPWSTR || windows.UTF16PtrToString(value.str) != appUserModelID {
			t.Errorf("app ID: type %d", value.vt)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
