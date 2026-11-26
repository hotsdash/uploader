// Package links decides which addresses may be opened, and opens them.
package links

import (
	"net/url"
	"path"
	"strings"
)

// Opening things: Windows' "open" (ShellExecute) runs whatever it's given, so a program path, a network share
// (\\host\share\x.exe) or another app's link (ms-msdt:, search-ms:) would start a program. Addresses can come from
// the dashboard (the sign-in page), so only real web addresses are ever opened; the log file is opened by
// OpenFile with a path the uploader builds itself.

// WebLink: u is a plain http(s) address with a host and no user name or password in it. Only printable ASCII
// without quotes or shell-ish characters: Windows hands the address to the browser on a command line ("%1"), so a
// quote in it could end that argument and add browser options of the dashboard's choosing.
func WebLink(u string) bool {
	if u == "" {
		return false
	}
	for _, r := range u {
		if r <= ' ' || r >= 0x7f || strings.ContainsRune("\"'<>^`|{}\\", r) {
			return false
		}
	}
	p, err := url.Parse(u)
	if err != nil || p.Host == "" || p.User != nil || p.Opaque != "" {
		return false
	}
	return p.Scheme == "https" || p.Scheme == "http"
}

// OnSite: u is a web address on the dashboard's own site (same scheme, host and port, under its path: a reverse proxy
// may host other apps beside it).
func OnSite(u, server string) bool {
	if !WebLink(u) || !WebLink(server) {
		return false
	}
	a, _ := url.Parse(u)
	b, _ := url.Parse(server)
	// a ".." would take the browser or the proxy out from under the path being checked. Path is already decoded, so
	// this sees %2e%2e too, and a ".." hidden behind an encoded slash.
	for _, seg := range strings.FieldsFunc(a.Path, func(r rune) bool { return r == '/' || r == '\\' }) {
		if seg == ".." {
			return false
		}
	}
	base := strings.TrimRight(path.Clean("/"+b.Path), "/") // "" for a dashboard at the root
	p := path.Clean("/" + a.Path)
	return a.Scheme == b.Scheme && strings.EqualFold(a.Host, b.Host) && (p == base || strings.HasPrefix(p, base+"/"))
}

// OpenURL opens a web address in the browser (a variable so tests can swap in a recorder: each package's TestMain
// does, before any test runs, so no test can open a browser).
var OpenURL = openURLReal

// OpenFile opens a file the uploader made itself (the log), never a path from elsewhere. A variable for the same reason.
var OpenFile = openFileReal

// openURLReal opens a web address in the browser. Anything else is refused.
func openURLReal(u string) bool {
	if !WebLink(u) {
		return false
	}
	shellOpen(u)
	return true
}

func openFileReal(path string) { shellOpen(path) }
