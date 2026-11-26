package agent

import (
	"strings"
	"time"
)

// SyncSoon asks the run loop for a sync (from the tray's "Sync now").
func (a *Agent) SyncSoon() {
	select {
	case a.kick <- struct{}{}:
	default:
	}
}

// ---------- main loop ----------

// Run syncs now, then keeps checking for new replays (a full scan every 30s, plus a quick look whenever the system says a
// replay folder changed, watch.go) and games loading (every 2s) until stop closes.
func (a *Agent) Run(stop <-chan struct{}) {
	a.event("Started: uploading to %s from %d folder%s", a.Config().Server, len(a.enabledFolders()), plural(len(a.enabledFolders())))
	a.setStatus(func(s *Status) { s.Text = "Starting…" })
	go func() { _ = a.Sync() }()
	go a.updateLoop(stop)
	a.checkClient(time.Now())
	go a.heartbeat()
	a.checkLobby(true)
	replays := time.NewTicker(30 * time.Second)
	lobby := time.NewTicker(2 * time.Second)
	beat := time.NewTicker(60 * time.Second)
	game := time.NewTicker(clientCheckEvery)
	defer game.Stop()
	defer replays.Stop()
	defer lobby.Stop()
	defer beat.Stop()

	// the system says when a replay folder changes; the folders are watched again if Settings changes them
	changed := make(chan struct{}, 1)
	var folders *folderWatch
	watching := "\x00" // no folders watched yet
	rewatch := func() {
		dirs := a.enabledFolders()
		key := strings.Join(dirs, "\n")
		if key == watching && (folders.ok() || len(dirs) == 0) {
			return
		}
		folders.close()
		folders = watchFolders(dirs, changed)
		watching = key
	}
	rewatch()
	defer func() { folders.close() }()
	look := time.NewTimer(time.Hour)
	look.Stop()
	lookNow := func() {
		if a.watchReplays() {
			look.Reset(lookAgain)
		}
	}

	for {
		select {
		case <-stop:
			a.saveState()
			return
		case <-a.kick:
			go func() { _ = a.Sync() }()
		case <-replays.C:
			go func() { _ = a.Sync() }()
			rewatch()
		case <-beat.C:
			go a.heartbeat()
		case <-game.C:
			a.checkClient(time.Now())
		case <-changed:
			lookNow()
		case <-look.C:
			lookNow()
		case <-lobby.C:
			if !folders.ok() && a.gameLately(time.Now()) {
				lookNow()
			}
			if !a.Config().NoLive {
				a.checkLobby(false)
				go a.checkSaves()
			}
		}
	}
}
