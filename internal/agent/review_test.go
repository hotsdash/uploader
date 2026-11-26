package agent

import (
	"encoding/json"
	"io"
	"log"
	"strings"
	"testing"
)

func TestReviewLink(t *testing.T) {
	server := "https://hots.example.com"
	read := func(body string) gameSummary {
		var s gameSummary
		if err := json.Unmarshal([]byte(body), &s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	// an older dashboard: no review, the game opens as before
	if open, ask := reviewLink(server, read(`{"matchId":7,"title":"Win"}`)); open != server+"/matches/7" || ask != "" {
		t.Fatalf("no review: %q %q", open, ask)
	}
	open, ask := reviewLink(server, read(`{"matchId":7,"review":{"players":9,"path":"/matches/7?review=1"}}`))
	if open != server+"/matches/7?review=1" || ask != "Click to review your lobby: 9 players, about 30 seconds." {
		t.Fatalf("review: %q %q", open, ask)
	}
	// only ever one of the dashboard's own game pages
	for _, path := range []string{"//evil.example/x", "https://evil.example", "/accounts", "/matches/7@evil.example", `/matches/7\..\x`} {
		if open, ask := reviewLink(server, read(`{"matchId":7,"review":{"players":9,"path":`+jsonString(path)+`}}`)); open != server+"/matches/7" || ask != "" {
			t.Fatalf("%s: %q %q", path, open, ask)
		}
	}
	if _, ask := reviewLink(server, read(`{"matchId":7,"review":{"players":0,"path":"/matches/7?review=1"}}`)); ask != "" {
		t.Fatal("nobody to review")
	}
}

func jsonString(s string) string { b, _ := json.Marshal(s); return string(b) }

func TestReviewedNote(t *testing.T) {
	server := "https://hots.example.com"
	title, msg, open := reviewedNote(server, reviewedNotice{Count: 1, Map: "Sky Temple", Path: "/matches/7"})
	if title != "A player reviewed you" || open != server+"/matches/7" || !strings.Contains(msg, "Sky Temple") {
		t.Fatalf("one: %q %q %q", title, msg, open)
	}
	if title, _, _ := reviewedNote(server, reviewedNotice{Count: 3, Path: "/matches/7"}); title != "3 players reviewed you" {
		t.Fatalf("three: %q", title)
	}
	// only ever one of the dashboard's own game pages: anything else falls back to the matches list
	for _, path := range []string{"//evil.example/x", "https://evil.example", "/accounts", "/matches/7@evil.example", `/matches/7\..\x`, "/matches/7?x=1", ""} {
		if _, _, open := reviewedNote(server, reviewedNotice{Count: 1, Path: path}); open != server+"/matches" {
			t.Fatalf("%q: %q", path, open)
		}
	}
}

func TestShowReviewedRespectsGameNotes(t *testing.T) {
	var got []string
	a := NewAgent(Config{Server: "https://hots.example.com"}, t.TempDir(), log.New(io.Discard, "", 0))
	a.Notify = func(title, message, open string) { got = append(got, title) }
	a.showReviewed([]reviewedNotice{{Count: 1, Path: "/matches/7"}})
	if len(got) != 1 {
		t.Fatalf("shown: %v", got)
	}
	a.cfg.NoGameNotes = true
	a.showReviewed([]reviewedNotice{{Count: 1, Path: "/matches/7"}})
	if len(got) != 1 {
		t.Fatalf("game notifications are off: %v", got)
	}
}
