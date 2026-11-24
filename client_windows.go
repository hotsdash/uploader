//go:build windows

package main

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

// gameRunning reads the process list (a snapshot, no access to the game's own process needed) for the game's .exe.
// ok is false when the list couldn't be read.
func gameRunning() (open, ok bool) {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return false, false
	}
	defer windows.CloseHandle(snap)
	var e windows.ProcessEntry32
	e.Size = uint32(unsafe.Sizeof(e))
	if err := windows.Process32First(snap, &e); err != nil {
		return false, false
	}
	for {
		if isGameProcess(windows.UTF16ToString(e.ExeFile[:])) {
			return true, true
		}
		if err := windows.Process32Next(snap, &e); err != nil {
			return false, true // ERROR_NO_MORE_FILES: the end of the list
		}
	}
}
