package main

// Noticing a finished game's replay as soon as the game writes it. The full scan (Sync) walks every replay in every
// folder and checks each one, so it only runs every 30 s. Between scans, the system tells us when something in the replay
// folders changes (watch_windows.go, watch_linux.go with inotify, watch_darwin.go with kqueue), and only then do we list the folders that held replays at the last scan (no files
// read) and start a scan once a replay that hasn't been sent has finished being written. While a new replay is still
// being written we look again every second until it settles; otherwise nothing runs. A game then reaches the dashboard
// a few seconds after it ends, not up to half a minute later (and overlays on stream follow it that much sooner).
//
// Where folder notifications aren't available (a folder the system can't watch), we look every couple of seconds
// instead, but only for a while after a game loaded.

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"time"
)

// replaySettle: a replay written more recently than this may still be being written, so it waits for the next look.
const replaySettle = 2 * time.Second

// lookAgain: how soon to look again while a new replay is still being written (or a scan is running).
const lookAgain = time.Second

// pollAfterLobby: without folder notifications, how long after a game loads to keep looking every couple of seconds.
// Longer than nearly every game, queue and draft included; the 30 s scan catches the rest.
const pollAfterLobby = 75 * time.Minute

// folderWatch: the system watching the replay folders for us. alive goes false if it stops.
type folderWatch struct {
	alive atomic.Bool
	done  chan struct{}
	stop  func()
}

func (w *folderWatch) ok() bool { return w != nil && w.alive.Load() }

func (w *folderWatch) close() {
	if w != nil && w.stop != nil {
		w.stop()
	}
}

// watchState is only used from Run's loop.
type watchState struct {
	sizes  map[string]int64 // replays not sent yet, and their size at the last look
	kicked map[string]int64 // replays a scan was started for, and their size then (once each, so a failing upload
	// doesn't start a scan every tick: the 30 s scan keeps retrying those)
}

// readyReplays lists dirs (not their subfolders) and returns the replays in them that haven't been sent and are ready
// to: written at least replaySettle ago and the same size as at the last look (prev). sizes is every unsent replay
// seen, for the next look. Sent replays aren't stat'ed at all.
func readyReplays(dirs []string, sent func(string) bool, prev map[string]int64, now time.Time) (ready []string, sizes map[string]int64) {
	sizes = map[string]int64{}
	for _, d := range dirs {
		ents, err := os.ReadDir(d)
		if err != nil {
			continue
		}
		for _, e := range ents {
			if e.IsDir() || !strings.HasSuffix(strings.ToLower(e.Name()), ".stormreplay") {
				continue
			}
			p := filepath.Join(d, e.Name())
			if sent(p) {
				continue
			}
			info, err := e.Info()
			if err != nil || info.Size() == 0 || info.Size() > maxReplayBytes {
				continue
			}
			sizes[p] = info.Size()
			if was, ok := prev[p]; ok && was == info.Size() && now.Sub(info.ModTime()) >= replaySettle {
				ready = append(ready, p)
			}
		}
	}
	sort.Strings(ready)
	return ready, sizes
}

// replayDirsOf: the folders these replays are in, for the quick look between scans.
func replayDirsOf(paths []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, p := range paths {
		d := filepath.Dir(p)
		if !seen[d] {
			seen[d] = true
			out = append(out, d)
		}
	}
	sort.Strings(out)
	return out
}

// sentOrSkipped: a replay this uploader has sent (or the dashboard already had), or one it skips for its size.
func (a *Agent) sentOrSkipped(p string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	f := a.files[p]
	return (f != nil && f.Sent) || a.tooBig[p]
}

// watchReplays starts a scan when a new replay is ready. Called from Run when a folder changed (or on the fallback
// poll). It says whether to look again soon: a replay is still being written, or a scan was already running.
func (a *Agent) watchReplays() (again bool) {
	a.mu.Lock()
	dirs := a.replayDirs
	a.mu.Unlock()
	if len(dirs) == 0 { // no scan has finished yet: the first one will find everything
		return false
	}
	w := &a.watch
	now := time.Now()
	ready, sizes := readyReplays(dirs, a.sentOrSkipped, w.sizes, now)
	for p := range sizes {
		if _, had := w.sizes[p]; !had {
			a.noteSeen(p, foundWatch, now) // the first look that found it
		}
	}
	w.sizes = sizes
	if w.kicked == nil {
		w.kicked = map[string]int64{}
	}
	for p := range w.kicked {
		if _, ok := sizes[p]; !ok { // sent since, or gone
			delete(w.kicked, p)
		}
	}
	isReady := map[string]bool{}
	for _, p := range ready {
		isReady[p] = true
	}
	for p, n := range sizes {
		if was, ok := w.kicked[p]; !isReady[p] && (!ok || was != n) {
			again = true // still being written, or not looked at twice yet
		}
	}
	// a scan that's running now may have started before this replay was ready: look again once it's done
	if a.syncing.Load() {
		return again || len(ready) > 0
	}
	kick := 0
	for _, p := range ready {
		if was, ok := w.kicked[p]; ok && was == sizes[p] {
			continue
		}
		w.kicked[p] = sizes[p]
		kick++
	}
	if kick > 0 {
		a.log.Printf("%d new replay%s written: uploading now", kick, plural(kick))
		a.SyncSoon()
	}
	return again
}

// gameLately: a game loaded recently, so without folder notifications it's worth looking every couple of seconds.
// Only called from Run's loop (checkLobby sets lastLobbyAt there too).
func (a *Agent) gameLately(now time.Time) bool {
	return !a.lastLobbyAt.IsZero() && now.Sub(a.lastLobbyAt) < pollAfterLobby
}
