package main

import (
	"encoding/base64"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type gameSummary struct {
	Pending bool     `json:"pending"`
	Failed  bool     `json:"failed"`
	None    bool     `json:"none"`
	MatchID int64    `json:"matchId"`
	Title   string   `json:"title"`
	Line    string   `json:"line"`
	Notable []string `json:"notable"`
	Session *string  `json:"session"`
	// the lobby review, when the dashboard offers it (rate the other players in the game)
	Review *struct {
		Players int    `json:"players"`
		Path    string `json:"path"`
	} `json:"review"`
}

// reviewLink: where the notification for a game opens. The lobby review when the dashboard offers one (only ever a page
// of its own game pages), otherwise the game.
func reviewLink(server string, s gameSummary) (string, string) {
	game := fmt.Sprintf("%s/matches/%d", server, s.MatchID)
	if s.Review == nil || s.Review.Players <= 0 || !strings.HasPrefix(s.Review.Path, "/matches/") || strings.ContainsAny(s.Review.Path, "\\@ \t\r\n") {
		return game, ""
	}
	return server + s.Review.Path, fmt.Sprintf("Click to review your lobby: %d players, about 30 seconds.", s.Review.Players)
}

// followGame waits for the server to read a fresh replay, then shows how the game went.
func (a *Agent) followGame(replayID int64) {
	for i := 0; i < 45; i++ {
		time.Sleep(4 * time.Second)
		var s gameSummary
		if err := a.call("GET", fmt.Sprintf("replays/%d/summary", replayID), nil, "", &s); err != nil {
			return // e.g. an old server, or not signed in: no summary
		}
		if s.Pending {
			continue
		}
		if s.Failed || s.None || s.Title == "" {
			return
		}
		lines := []string{s.Line}
		lines = append(lines, s.Notable...)
		if s.Session != nil && *s.Session != "" {
			lines = append(lines, *s.Session)
		}
		a.event("%s: %s", s.Title, strings.Join(lines, "; "))
		a.setStatus(func(st *Status) { st.LastGame = s.Title })
		if a.Notify != nil && !a.Config().NoGameNotes {
			open, ask := reviewLink(a.Config().Server, s)
			if ask != "" {
				lines = append(lines, ask)
			}
			a.Notify(s.Title, strings.Join(lines, "\n"), open)
		}
		return
	}
}

// ---------- live lobby ----------

var tagRe = regexp.MustCompile(`([\p{L}\p{N}_]{2,24})#(\d{3,10})`)
var digitsRe = regexp.MustCompile(`^\d+$`)

// BattletagsFrom pulls battletags out of the lobby file, in slot order (first half is one team).
func BattletagsFrom(b []byte) []string {
	var out []string
	seen := map[string]bool{}
	for _, m := range tagRe.FindAllSubmatch(b, -1) {
		if digitsRe.Match(m[1]) {
			continue
		}
		t := string(m[0])
		if !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	return out
}

var toonDirRe = regexp.MustCompile(`^(\d+)-Hero-\d+-\d+$`)

func (a *Agent) region() *int {
	regions := map[int]bool{}
	for t := range a.Scan().Toons {
		if m := toonDirRe.FindStringSubmatch(t); m != nil {
			n, _ := strconv.Atoi(m[1])
			regions[n] = true
		}
	}
	if len(regions) != 1 {
		return nil
	}
	for r := range regions {
		return &r
	}
	return nil
}

func (a *Agent) lobbyFiles() map[string]time.Time {
	out := map[string]time.Time{}
	root := a.Config().Lobby
	if root == "" { // no game to watch (lobbyDir)
		return out
	}
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() && strings.Count(strings.TrimPrefix(p, root), string(os.PathSeparator)) > 3 {
			return filepath.SkipDir
		}
		if !d.IsDir() && strings.EqualFold(d.Name(), "replay.server.battlelobby") {
			if info, err := d.Info(); err == nil {
				out[p] = info.ModTime()
			}
		}
		return nil
	})
	return out
}

type lobbyPlayer struct {
	Name       string `json:"name"`
	Nickname   string `json:"nickname"`
	Me         bool   `json:"me"`
	Team       int    `json:"team"`
	Total      int    `json:"total"`
	Flag       string `json:"flag"`
	WithGames  int    `json:"withGames"`
	AgainstGms int    `json:"againstGames"`
}

// lobbyRepeat: the game can write the lobby file more than once while a game loads. The same players after this long
// are a new game: a tournament series, or a custom rematch. No game ends sooner than this.
const lobbyRepeatWindow = 10 * time.Minute

func lobbyRepeat(key, last string, lastAt, now time.Time) bool {
	return key == last && now.Sub(lastAt) < lobbyRepeatWindow
}

func (a *Agent) checkLobby(initial bool) {
	files := a.lobbyFiles()
	for p, mt := range files {
		prev, seen := a.lobbySeen[p]
		a.lobbySeen[p] = mt
		if initial || (seen && !mt.After(prev)) {
			continue
		}
		if time.Since(mt) < 800*time.Millisecond {
			delete(a.lobbySeen, p) // still being written; look again next tick
			continue
		}
		if info, err := os.Stat(p); err != nil || info.Size() > 4<<20 { // a lobby file is a few KB
			continue
		}
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		tags := BattletagsFrom(b)
		// one battletag is a game against AI on your own (a custom game to test with): still a game
		if len(tags) < 1 {
			continue
		}
		key := strings.Join(tags, "|")
		if lobbyRepeat(key, a.lastLobby, a.lastLobbyAt, time.Now()) {
			continue
		}
		a.lastLobby, a.lastLobbyAt = key, time.Now()
		a.reportLobby(tags, b)
	}
}

// reportLobby sends the battletags and the whole lobby file: the server reads parties, levels and the map from it.
func (a *Agent) reportLobby(tags []string, file []byte) {
	a.event("Game loading with %d players", len(tags))
	a.lobbySeenNow()
	var r struct {
		Players  []lobbyPlayer `json:"players"`
		MyTeam   *int          `json:"myTeam"`
		Insights struct {
			Alerts []string `json:"alerts"`
		} `json:"insights"`
	}
	if err := a.postJSON("live/lobby", map[string]any{"battletags": tags, "region": a.region(), "source": "agent", "lobby": base64.StdEncoding.EncodeToString(file)}, &r); err != nil {
		a.log.Printf("lobby lookup failed: %v", err)
		return
	}
	var known []string
	for _, p := range r.Players {
		if p.Me || p.Total == 0 {
			continue
		}
		side := "enemy"
		if r.MyTeam != nil && p.Team == *r.MyTeam {
			side = "ally"
		}
		name := p.Nickname
		if name == "" {
			name = p.Name
		}
		known = append(known, fmt.Sprintf("%s (%s): %d with, %d against", name, side, p.WithGames, p.AgainstGms))
	}
	summary := "Nobody you've played with before"
	if len(known) > 0 {
		summary = fmt.Sprintf("%d familiar player", len(known))
		if len(known) > 1 {
			summary += "s"
		}
	}
	alerts := r.Insights.Alerts
	a.setStatus(func(s *Status) { s.LastLobby = summary })
	a.event("Lobby check: %s", summary)
	for _, al := range alerts {
		a.event("Heads up: %s", al)
	}
	if (len(known) > 0 || len(alerts) > 0) && a.Notify != nil {
		// a notification shows a few lines: warnings first (enemy stack, nemesis, losing run), then familiar faces
		lines := append([]string{}, alerts...)
		if len(lines) > 2 {
			lines = lines[:2]
		}
		for _, k := range known {
			if len(lines) >= 4 {
				break
			}
			lines = append(lines, k)
		}
		title := summary + " in this game"
		if len(alerts) > 0 {
			title = "Heads up: " + summary
		}
		a.Notify(title, strings.Join(lines, "\n"), a.Config().Server+"/live")
	}
}

// heartbeat tells the dashboard this uploader is running (and whether its live game check is on), so
// "Your live game" can say it's connected while there's no game. With live games on it also says how many accounts
// have a live game folder, for Admin. Signed-out copies have nobody to report to.
func (a *Agent) heartbeat() {
	if a.Config().Token == "" {
		return
	}
	cfg := a.Config()
	body := map[string]any{"live": !cfg.NoLive}
	if !cfg.NoLive {
		accounts, _ := LiveAccounts(cfg, a.Scan())
		body["liveFolders"] = liveReport(accounts)
	}
	if c := clientReport(a.clientState()); c != nil {
		body["client"] = c
	}
	var r struct {
		Reviewed []reviewedNotice `json:"reviewed"`
	}
	if err := a.postJSON("live/heartbeat", body, &r); err != nil {
		a.log.Printf("heartbeat: %v", err)
		return
	}
	a.showReviewed(r.Reviewed)
}

// reviewedNotice: someone from one of your games reviewed you. Only that it happened: no name, no stars or tags.
type reviewedNotice struct {
	GameID string `json:"gameId"`
	Map    string `json:"map"`
	Hero   string `json:"hero"`
	Count  int    `json:"count"`
	Path   string `json:"path"`
}

// reviewedNote: the notification for a review of you. The text is the dashboard's, so it only ever opens one of its own
// game pages (as reviewLink does), otherwise the matches list.
func reviewedNote(server string, n reviewedNotice) (title, message, open string) {
	title = "A player reviewed you"
	if n.Count > 1 {
		title = fmt.Sprintf("%d players reviewed you", n.Count)
	}
	message = "Someone from your last game rated you. Reviews are anonymous."
	if n.Map != "" {
		message = fmt.Sprintf("From your game on %s. Reviews are anonymous.", n.Map)
	}
	open = server + "/matches"
	if strings.HasPrefix(n.Path, "/matches/") && !strings.ContainsAny(n.Path, "\\@?# \t\r\n") {
		open = server + n.Path
	}
	return title, message, open
}

// showReviewed tells you about reviews of you, as game notifications are (not when those are turned off).
func (a *Agent) showReviewed(notices []reviewedNotice) {
	for _, n := range notices {
		title, message, open := reviewedNote(a.Config().Server, n)
		a.event("%s", title)
		if a.Notify != nil && !a.Config().NoGameNotes {
			a.Notify(title, message, open)
		}
	}
}
