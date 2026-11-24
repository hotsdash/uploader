package main

import (
	"encoding/base64"
	"unicode/utf16"
)

// shortcutScript: the PowerShell that makes the Start menu shortcut (lnk) to the installed program (target), with
// its icon (icon). The paths come from the environment and can hold anything a folder name can, so they're only ever
// in the script as base64, like the notification text (see notify.go).
func shortcutScript(lnk, target, icon string) string {
	dec := func(s string) string {
		return `[System.Text.Encoding]::UTF8.GetString([System.Convert]::FromBase64String('` + base64.StdEncoding.EncodeToString([]byte(s)) + `'))`
	}
	return `$s = (New-Object -ComObject WScript.Shell).CreateShortcut(` + dec(lnk) + `)
$s.TargetPath = ` + dec(target) + `
$s.IconLocation = ` + dec(icon) + `
$s.Description = 'Upload your Heroes of the Storm replays to hots-dash'
$s.Save()`
}

// encodedCommand: a script as PowerShell's -EncodedCommand wants it (UTF-16LE, base64).
func encodedCommand(ps string) string {
	enc := utf16.Encode([]rune(ps))
	b := make([]byte, len(enc)*2)
	for i, v := range enc {
		b[i*2], b[i*2+1] = byte(v), byte(v>>8)
	}
	return base64.StdEncoding.EncodeToString(b)
}
