//go:build windows

package main

import (
	"runtime"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

func autostartState() (supported, on bool) { return true, autostartEnabled() }
func setAutostartState(on bool) error      { return setAutostart(on) }
func hasFolderPicker() bool                { return true }

var (
	shell32                  = windows.NewLazySystemDLL("shell32.dll")
	ole32                    = windows.NewLazySystemDLL("ole32.dll")
	user32                   = windows.NewLazySystemDLL("user32.dll")
	procSHBrowseForFolderW   = shell32.NewProc("SHBrowseForFolderW")
	procSHGetPathFromIDListW = shell32.NewProc("SHGetPathFromIDListW")
	procCoInitializeEx       = ole32.NewProc("CoInitializeEx")
	procCoUninitialize       = ole32.NewProc("CoUninitialize")
	procCoTaskMemFree        = ole32.NewProc("CoTaskMemFree")
	procGetForegroundWindow  = user32.NewProc("GetForegroundWindow")
)

// BROWSEINFOW
type browseInfo struct {
	Owner       uintptr
	Root        uintptr
	DisplayName *uint16
	Title       *uint16
	Flags       uint32
	Callback    uintptr
	LParam      uintptr
	Image       int32
}

const (
	bifReturnOnlyFSDirs = 0x0001
	bifEditBox          = 0x0010
	bifNewDialogStyle   = 0x0040
)

// pickFolder shows Windows' folder picker. Returns "" if cancelled.
func pickFolder() (string, error) {
	type res struct {
		p   string
		err error
	}
	ch := make(chan res, 1)
	go func() {
		// the picker needs a COM apartment on one OS thread
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		procCoInitializeEx.Call(0, 0x2) // COINIT_APARTMENTTHREADED
		defer procCoUninitialize.Call()
		owner, _, _ := procGetForegroundWindow.Call()
		name := make([]uint16, windows.MAX_PATH)
		title, _ := windows.UTF16PtrFromString("Choose a folder with Heroes of the Storm replays (e.g. Documents\\Heroes of the Storm\\Accounts, or a backup)")
		bi := browseInfo{Owner: owner, DisplayName: &name[0], Title: title, Flags: bifReturnOnlyFSDirs | bifNewDialogStyle | bifEditBox}
		pidl, _, _ := procSHBrowseForFolderW.Call(uintptr(unsafe.Pointer(&bi)))
		if pidl == 0 {
			ch <- res{"", nil}
			return
		}
		defer procCoTaskMemFree.Call(pidl)
		buf := make([]uint16, 32768)
		ok, _, _ := procSHGetPathFromIDListW.Call(pidl, uintptr(unsafe.Pointer(&buf[0])))
		if ok == 0 {
			ch <- res{"", syscall.EINVAL}
			return
		}
		ch <- res{windows.UTF16ToString(buf), nil}
	}()
	r := <-ch
	return r.p, r.err
}
