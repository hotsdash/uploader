package main

import "strings"

// Windows notifications are toasts, shown through WinRT (toast_windows.go). Their text comes partly from the
// dashboard (game summaries, player names), so it only ever goes into the toast's XML escaped, as text.

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
	// a notification shows a few lines, and Windows refuses a very long one
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

// clip shortens s to at most n runes.
func clip(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}
