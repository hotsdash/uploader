package main

import (
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// The settings page changes the settings while the uploader is using them. Meant for go test -race, which fails on
// any read of them that isn't locked.
func TestSettingsCanChangeWhileInUse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/live/lobby":
			_, _ = io.WriteString(w, `{"players":[{"name":"Sam","total":2,"withGames":2}]}`)
		case "/api/replays/1/summary":
			_, _ = io.WriteString(w, `{"matchId":1,"title":"Win"}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	lobby := t.TempDir()
	a := NewAgent(Config{Server: srv.URL, Lobby: lobby, Folders: []Folder{{Path: t.TempDir()}}}, t.TempDir(), log.New(io.Discard, "", 0))
	a.Notify = func(string, string, string) {}

	var wg sync.WaitGroup
	run := func(f func()) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			f()
		}()
	}
	run(func() {
		for i := 0; i < 200; i++ {
			a.Update(func(c *Config) { c.Server, c.Lobby = srv.URL, lobby })
		}
	})
	run(func() {
		for i := 0; i < 20; i++ {
			a.showReviewed([]reviewedNotice{{Count: 1, Path: "/matches/1"}})
		}
	})
	run(func() {
		for i := 0; i < 5; i++ {
			a.reportLobby([]string{"Sam#1234"}, []byte("lobby"))
		}
	})
	run(func() {
		for i := 0; i < 50; i++ {
			a.lobbyFiles()
		}
	})
	run(func() { a.followGame(1) }) // waits a few seconds before it asks
	run(func() {
		stop := make(chan struct{})
		close(stop)
		a.Run(stop)
	})
	for i := 0; i < 100; i++ {
		// still changing when followGame gets its answer
		time.Sleep(50 * time.Millisecond)
		a.Update(func(c *Config) { c.Server, c.Lobby = srv.URL, lobby })
	}
	wg.Wait()
}
