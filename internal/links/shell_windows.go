//go:build windows

package links

import "golang.org/x/sys/windows"

func u16(s string) *uint16 { p, _ := windows.UTF16PtrFromString(s); return p }

func shellOpen(u string) {
	_ = windows.ShellExecute(0, u16("open"), u16(u), nil, nil, windows.SW_SHOWNORMAL)
}
