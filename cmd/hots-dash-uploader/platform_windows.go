//go:build windows

package main

import "github.com/hotsdash/uploader/internal/agent"

func autostartState() (supported, on bool) { return true, autostartEnabled() }
func setAutostartState(on bool) error      { return setAutostart(on) }

// the settings page's "start at log-in" switch
func init() { agent.AutostartState, agent.SetAutostartState = autostartState, setAutostartState }
