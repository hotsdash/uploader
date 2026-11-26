//go:build windows

package main

import (
	"os"
	"path/filepath"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	clsidShellLink   = mustGUID("00021401-0000-0000-c000-000000000046")
	iidShellLinkW    = mustGUID("000214f9-0000-0000-c000-000000000046")
	iidPersistFile   = mustGUID("0000010b-0000-0000-c000-000000000046")
	iidPropertyStore = mustGUID("886d8eeb-8cf2-4446-8d02-cdba1dbdcf99")
)

// positions in the method tables
const (
	shellLinkSetDescription  = 7
	shellLinkSetIconLocation = 17
	shellLinkSetPath         = 20
	persistFileSave          = 6
	propertyStoreSetValue    = 6
	propertyStoreCommit      = 7
)

// shortcutPath: the Start menu entry, so it can be opened again after Quit (%APPDATA%\Microsoft\Windows\Start Menu\Programs).
func shortcutPath() string {
	base := os.Getenv("APPDATA")
	if base == "" {
		base, _ = os.UserConfigDir()
	}
	return filepath.Join(base, "Microsoft", "Windows", "Start Menu", "Programs", appName+".lnk")
}

// ensureShortcut writes the Start menu entry at each install and update. An existing one is written over: those
// made by earlier versions don't carry the app's ID.
// The program has no icon of its own, so the tray icon goes next to it for the shortcut to use.
func ensureShortcut(target string, logger interface{ Printf(string, ...any) }) {
	icon := filepath.Join(filepath.Dir(target), "hots-dash.ico")
	if err := os.WriteFile(icon, iconICO, 0o644); err != nil {
		logger.Printf("start menu shortcut: icon: %v", err)
	}
	lnk := shortcutPath()
	_ = os.MkdirAll(filepath.Dir(lnk), 0o755)
	if err := saveShortcut(lnk, target, icon); err != nil {
		logger.Printf("start menu shortcut: %v", err)
	}
}

// saveShortcut makes the shortcut file lnk to the program target (IShellLinkW, IPropertyStore, IPersistFile).
func saveShortcut(lnk, target, icon string) error {
	var ptrs [5]*uint16
	for i, s := range []string{lnk, target, icon, "Upload your Heroes of the Storm replays to hots-dash", appUserModelID} {
		p, err := windows.UTF16PtrFromString(s)
		if err != nil {
			return err
		}
		ptrs[i] = p
	}
	file, program, iconFile, description, id := ptrs[0], ptrs[1], ptrs[2], ptrs[3], ptrs[4]
	return inApartment(func() error {
		link, err := createInstance(clsidShellLink, iidShellLinkW)
		if err != nil {
			return err
		}
		defer link.release()
		if err := link.call(shellLinkSetPath, uintptr(unsafe.Pointer(program))); err != nil {
			return err
		}
		if err := link.call(shellLinkSetDescription, uintptr(unsafe.Pointer(description))); err != nil {
			return err
		}
		if err := link.call(shellLinkSetIconLocation, uintptr(unsafe.Pointer(iconFile)), 0); err != nil {
			return err
		}

		props, err := link.query(iidPropertyStore)
		if err != nil {
			return err
		}
		defer props.release()
		key, value := pkeyAppUserModelID, propVariant{vt: vtLPWSTR, str: id} // SetValue copies the string
		if err := props.call(propertyStoreSetValue, uintptr(unsafe.Pointer(&key)), uintptr(unsafe.Pointer(&value))); err != nil {
			return err
		}
		if err := props.call(propertyStoreCommit); err != nil {
			return err
		}

		persist, err := link.query(iidPersistFile)
		if err != nil {
			return err
		}
		defer persist.release()
		return persist.call(persistFileSave, uintptr(unsafe.Pointer(file)), 1) // remember the name: TRUE
	})
}
