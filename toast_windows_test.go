//go:build windows

package main

import (
	"strings"
	"testing"
)

// Builds real notifications through WinRT, without showing them: Windows parses the XML and takes every call up to
// Show.
func TestWindowsTakesTheToast(t *testing.T) {
	for _, x := range []string{
		toastXML("Title", "one\ntwo", "https://hots.example.com/matches/1?a=1&b=2"),
		toastXML(`<a href="x">'&’`, "]]>\n<!--\n\U0001F3AE é\x00\x1b", ""),
		toastXML(strings.Repeat("t", 5000), strings.Repeat("m\n", 5000), ""),
	} {
		called := false
		err := withToast(x, func(notifier, notification *comObject) error {
			called = notifier != nil && notification != nil
			return nil
		})
		if err != nil || !called {
			t.Errorf("%.80s…: %v", x, err)
		}
	}
}
