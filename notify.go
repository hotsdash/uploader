package main

import (
	"encoding/base64"
	"strings"
)

// Windows notifications are shown through PowerShell (no extra install). Their text comes partly from the
// dashboard (game summaries, player names), so it must never become part of the script: the toast XML goes in as
// base64 and is decoded inside it. Escaping quotes isn't enough, because PowerShell also ends a single-quoted
// string at curly quotes (U+2018 to U+201B).

var xmlEscaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&apos;")

// xmlEscape escapes text for the toast XML and drops control characters XML can't hold.
func xmlEscape(s string) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 && r != '\t' || r == 0xFFFE || r == 0xFFFF {
			return -1
		}
		return r
	}, s)
	return xmlEscaper.Replace(s)
}

// toastXML: the notification, one line of text per line of message. Clicking it opens open, if that's a web address.
func toastXML(title, message, open string) string {
	// a notification shows a few lines; keeping it short also keeps the script well under Windows' command-line limit
	title, message = clip(title, 200), clip(message, 1500)
	var lines strings.Builder
	for _, l := range strings.Split(message, "\n") {
		if l != "" {
			lines.WriteString("<text>" + xmlEscape(l) + "</text>")
		}
	}
	launch := ""
	if webLink(open) {
		launch = ` activationType="protocol" launch="` + xmlEscape(open) + `"`
	}
	return `<toast` + launch + `><visual><binding template="ToastGeneric"><text>` + xmlEscape(title) + `</text>` + lines.String() + `</binding></visual></toast>`
}

// toastScript: the PowerShell that shows the notification. The XML is only ever in it as base64.
func toastScript(xml string) string {
	b64 := base64.StdEncoding.EncodeToString([]byte(xml))
	// PowerShell's own app id: always registered, so the notification shows on Windows 10 and 11
	return `[Windows.UI.Notifications.ToastNotificationManager, Windows.UI.Notifications, ContentType = WindowsRuntime] | Out-Null
[Windows.Data.Xml.Dom.XmlDocument, Windows.Data.Xml.Dom.XmlDocument, ContentType = WindowsRuntime] | Out-Null
$x = New-Object Windows.Data.Xml.Dom.XmlDocument
$x.LoadXml([System.Text.Encoding]::UTF8.GetString([System.Convert]::FromBase64String('` + b64 + `')))
$t = New-Object Windows.UI.Notifications.ToastNotification $x
[Windows.UI.Notifications.ToastNotificationManager]::CreateToastNotifier('{1AC14E77-02E7-4E5D-B744-2EB1AE5198B7}\WindowsPowerShell\v1.0\powershell.exe').Show($t)`
}

// clip shortens s to at most n runes.
func clip(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}
