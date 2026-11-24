package main

import (
	"strings"
	"time"
)

// The game client: is Heroes of the Storm running on this PC? The uploader looks every few seconds (client_windows.go,
// client_other.go) and tells the dashboard with the heartbeat, straight away when it opens or closes. Nothing more is
// known from outside the game: queueing, the menus and the draft write nothing to disk, so "open" covers all of them.
// The loading screen (the lobby file) and the game itself (rejoin saves) are seen as before.

// clientCheckEvery is how often the process list is read: cheap, and quick enough for a stream overlay to follow.
const clientCheckEvery = 5 * time.Second

// ClientState is what the heartbeat says about the game client. Known is false when the process list couldn't be read
// (then nothing is sent and the site keeps saying it doesn't know).
type ClientState struct {
	Known bool
	Open  bool
	Since time.Time // when it was first seen open or closed by this copy of the uploader
}

// gameProcessNames are the game's own executables (the Battle.net launcher isn't the game). Windows and Wine/Proton
// show the .exe; Linux truncates a process name to 15 characters (HeroesOfTheSto); macOS runs Heroes.app.
var gameProcessNames = []string{"heroesofthestorm_x64.exe", "heroesofthestorm.exe", "heroesofthestorm_x64", "heroesofthestorm", "heroesofthesto"}

// isGameProcess: does this process name (or path) belong to the game? Pure.
func isGameProcess(name string) bool {
	n := strings.ToLower(strings.TrimSpace(name))
	if n == "" {
		return false
	}
	// a full path (a program's first argument, Wine's Windows paths too): look at the program's own name
	n = strings.ReplaceAll(n, "\\", "/")
	if strings.HasSuffix(n, "/heroes.app/contents/macos/heroes") {
		return true
	}
	base := n[strings.LastIndex(n, "/")+1:]
	for _, g := range gameProcessNames {
		if base == g {
			return true
		}
	}
	return false
}

// nextClient folds one look at the process list into the state: Since moves only when open/closed changes. Pure.
func nextClient(prev ClientState, open, ok bool, now time.Time) (ClientState, bool) {
	if !ok {
		return prev, false // keep what we knew; one failed look isn't news
	}
	if prev.Known && prev.Open == open {
		return prev, false
	}
	return ClientState{Known: true, Open: open, Since: now}, true
}

// clientReport is the heartbeat's client field: nil when unknown.
func clientReport(c ClientState) map[string]any {
	if !c.Known {
		return nil
	}
	return map[string]any{"open": c.Open, "since": c.Since.UTC().Format(time.RFC3339)}
}

// checkClient looks at the process list once; on a change it says so in the log and checks in at once.
func (a *Agent) checkClient(now time.Time) {
	open, ok := gameRunning()
	a.mu.Lock()
	next, changed := nextClient(a.gameClient, open, ok, now)
	first := !a.gameClient.Known
	a.gameClient = next
	a.mu.Unlock()
	if !changed {
		return
	}
	switch {
	case next.Open:
		a.event("Heroes of the Storm is open")
	case !first:
		a.event("Heroes of the Storm closed")
	}
	a.setStatus(func(s *Status) { s.Client = clientText(next) })
	// the first look happens as the uploader starts, just before its first check-in
	if !first {
		go a.heartbeat()
	}
}

// clientText is the line on the uploader's own page. Pure.
func clientText(c ClientState) string {
	if !c.Known {
		return ""
	}
	if c.Open {
		return "Heroes of the Storm: open"
	}
	return "Heroes of the Storm: not running"
}

func (a *Agent) clientState() ClientState {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.gameClient
}
