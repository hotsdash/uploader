package main

import (
	"reflect"
	"testing"

	"github.com/hotsdash/uploader/internal/agent"
)

// Without these the settings page would quietly lose its "start at log-in" switch. Only compared, not called: that
// would ask the real system.
func TestSettingsPageCanSetAutostart(t *testing.T) {
	same := func(a, b any) bool { return reflect.ValueOf(a).Pointer() == reflect.ValueOf(b).Pointer() }
	if !same(agent.AutostartState, autostartState) || !same(agent.SetAutostartState, setAutostartState) {
		t.Fatal("the settings page isn't given autostart")
	}
}
