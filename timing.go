package main

// Telling the dashboard how long each new replay took on this computer (Admin › Game delays): when the uploader first
// saw it and how (the folder watch, the 30 s scan, or when the uploader started), when it started sending it, and
// how far this computer's clock is from the dashboard's, so those times can be put on the dashboard's clock.

import (
	"strconv"
	"sync/atomic"
	"time"
)

// How the uploader came across a replay.
const (
	foundWatch = "watch" // Windows said the folder changed
	foundScan  = "scan"  // the 30 s scan
	foundStart = "start" // the first scan after the uploader started: it wasn't running when the game ended
)

// timeLayout: to the millisecond, which the dashboard reads; RFC3339 alone stops at the second.
const timeLayout = "2006-01-02T15:04:05.000Z07:00"

type seenInfo struct {
	at  time.Time
	how string
}

// clockOffset: the dashboard's clock minus ours, in ms, measured on each "do you have these?" check before uploading.
type clockOffset struct {
	ms   atomic.Int64
	have atomic.Bool
}

// measure takes the dashboard's time from a reply, asked at t0 and answered at t1: it was read around halfway.
func (c *clockOffset) measure(serverMs int64, t0, t1 time.Time) {
	if serverMs <= 0 || t1.Before(t0) || t1.Sub(t0) > 30*time.Second { // no time sent (an older dashboard), or too slow to trust
		return
	}
	mid := t0.UnixMilli() + t1.Sub(t0).Milliseconds()/2
	c.ms.Store(serverMs - mid)
	c.have.Store(true)
}

// field: the offset for the upload, or "" before one has been measured.
func (c *clockOffset) field() string {
	if !c.have.Load() {
		return ""
	}
	return strconv.FormatInt(c.ms.Load(), 10)
}

// noteSeen remembers when a replay was first seen, and how. Later sightings keep the first.
func (a *Agent) noteSeen(p, how string, at time.Time) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.seen == nil {
		a.seen = map[string]seenInfo{}
	}
	if _, ok := a.seen[p]; !ok {
		a.seen[p] = seenInfo{at: at, how: how}
	}
}

// seenFields: when and how this replay was first seen, for its upload ("" when not known).
func (a *Agent) seenFields(p string) (at, how string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	s, ok := a.seen[p]
	if !ok {
		return "", ""
	}
	return s.at.UTC().Format(timeLayout), s.how
}

// forgetSeen: sent, or gone.
func (a *Agent) forgetSeen(p string) {
	a.mu.Lock()
	delete(a.seen, p)
	a.mu.Unlock()
}
