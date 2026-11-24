package main

// ---------- picks from rejoin saves ----------
//
// The lobby file has no heroes in it. Once the game starts, the client writes rejoin saves
// (Documents\Heroes of the Storm\Accounts\<id>\<toon>\Saves\Rejoin\*.StormSave) so a player who drops can
// reconnect; they carry each player's hero. The server reads them, so the agent only has to send the newest one.
//
// Rejoin folders are set per Battle.net account in their own list (Config.RejoinFolders), apart from the replay
// folders: live picks only look there. With none set, the main account's folder is set automatically, once
// (autoLiveFolder); the settings page suggests the others. Without one, the live page shows each player's most
// played hero instead of their pick.

import (
	"bytes"
	"fmt"
	"mime/multipart"
	"net/textproto"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// how long after a lobby was seen a save can still belong to its game
const saveWindow = 3 * time.Hour

// savesState follows the saves for the game that loaded last.
type savesState struct {
	lobbyAt   time.Time            // when the last lobby was reported
	sent      map[string]time.Time // save path → mtime already sent
	done      bool                 // nothing more to send for this game
	announced bool                 // "picks read" has been said for this game
	skipping  bool                 // "someone else is sending" has been said for this game
	talents   int                  // talents the server had after the last save
	lastCheck time.Time
}

var toonSegRe = regexp.MustCompile(`(?i)^\d+-Hero-\d+-\d+$`)

// toonDirOf finds the Battle.net account folder (…\2-Hero-1-269807) a replay sits under.
func toonDirOf(p string) (toon, dir string) {
	d := filepath.Dir(p)
	for {
		if b := filepath.Base(d); toonSegRe.MatchString(b) {
			return b, d
		}
		parent := filepath.Dir(d)
		if parent == d {
			return "", ""
		}
		d = parent
	}
}

// RejoinDirFor is where the game writes an account's rejoin saves.
func RejoinDirFor(toonDir string) string { return filepath.Join(toonDir, "Saves", "Rejoin") }

func sameFolder(a, b string) bool { return strings.EqualFold(filepath.Clean(a), filepath.Clean(b)) }

// SaveFiles lists the rejoin saves in the given rejoin folders. A folder may also be an account folder, whose
// Saves\Rejoin is then looked in.
func SaveFiles(dirs []string) map[string]time.Time {
	out := map[string]time.Time{}
	for _, dir := range dirs {
		for _, pat := range []string{filepath.Join(dir, "*"), filepath.Join(RejoinDirFor(dir), "*")} {
			m, _ := filepath.Glob(pat)
			for _, p := range m {
				if !strings.EqualFold(filepath.Ext(p), ".stormsave") {
					continue
				}
				if st, err := os.Stat(p); err == nil && !st.IsDir() {
					out[p] = st.ModTime()
				}
			}
		}
	}
	return out
}

// LiveAccount is one Battle.net account's rejoin folder, for the settings page.
type LiveAccount struct {
	Toon      string     `json:"toon"`
	Suggested string     `json:"suggested"` // the game's own rejoin folder for this account
	Folder    string     `json:"folder"`    // the one set, if any
	Set       bool       `json:"set"`
	Off       bool       `json:"off"`    // set, but switched off
	Exists    bool       `json:"exists"` // the folder is there (the game makes it the first time it saves)
	Saves     int        `json:"saves"`
	LastSave  *time.Time `json:"lastSave"`
}

// rejoinFolderFor: the rejoin folder set for an account, picked for it or the game's own.
func rejoinFolderFor(folders []Folder, toon, suggested string) (Folder, bool) {
	for _, f := range folders {
		if f.Toon != "" && strings.EqualFold(f.Toon, toon) {
			return f, true
		}
	}
	for _, f := range folders {
		if suggested != "" && sameFolder(f.Path, suggested) {
			return f, true
		}
	}
	return Folder{}, false
}

// LiveAccounts lists every account found in the replay folders with its rejoin folder, and the rejoin folders
// set that don't belong to any of them.
func LiveAccounts(cfg Config, scan ScanStats) (accounts []LiveAccount, others []Folder) {
	used := map[int]bool{}
	for t := range scan.Toons {
		v := LiveAccount{Toon: t}
		if d := scan.ToonDirs[t]; d != "" {
			v.Suggested = RejoinDirFor(d)
		}
		if f, ok := rejoinFolderFor(cfg.RejoinFolders, t, v.Suggested); ok {
			v.Folder, v.Set, v.Off = f.Path, true, f.Off
			for i, g := range cfg.RejoinFolders {
				if g.Path == f.Path {
					used[i] = true
				}
			}
		}
		look := v.Folder
		if look == "" {
			look = v.Suggested
		}
		if look != "" {
			if st, err := os.Stat(look); err == nil && st.IsDir() {
				v.Exists = true
				var last time.Time
				for _, mt := range SaveFiles([]string{look}) {
					v.Saves++
					if mt.After(last) {
						last = mt
					}
				}
				if !last.IsZero() {
					v.LastSave = &last
				}
			}
		}
		accounts = append(accounts, v)
	}
	sort.Slice(accounts, func(i, j int) bool {
		a, b := scan.Toons[accounts[i].Toon], scan.Toons[accounts[j].Toon]
		return a > b || (a == b && accounts[i].Toon < accounts[j].Toon)
	})
	for i, f := range cfg.RejoinFolders {
		if !used[i] {
			others = append(others, f)
		}
	}
	return accounts, others
}

// LiveReport is what the heartbeat tells the dashboard about live picks: how many of the accounts found in the
// replay folders have a rejoin folder switched on, and when the newest save was written. Admin uses it to spot
// uploaders whose games will never get picks.
type LiveReport struct {
	Accounts int        `json:"accounts"`
	Folders  int        `json:"folders"`
	LastSave *time.Time `json:"lastSave,omitempty"`
}

func liveReport(accounts []LiveAccount) LiveReport {
	r := LiveReport{Accounts: len(accounts)}
	for _, a := range accounts {
		if a.Set && !a.Off {
			r.Folders++
		}
		if a.LastSave != nil && (r.LastSave == nil || a.LastSave.After(*r.LastSave)) {
			t := a.LastSave.UTC()
			r.LastSave = &t
		}
	}
	return r
}

// liveMissing: accounts with no rejoin folder (or one switched off), so their games show no picks.
func liveMissing(accounts []LiveAccount) []string {
	var out []string
	for _, a := range accounts {
		if !a.Set || a.Off {
			out = append(out, a.Toon)
		}
	}
	return out
}

// mainLiveFolder is the rejoin folder to set automatically: the game's own for the account with the most replays
// (accounts come in that order from LiveAccounts). None when live games are off, when any rejoin folder is already
// set (even one switched off), or once this has been done or a folder was removed by hand.
func mainLiveFolder(cfg Config, accounts []LiveAccount) (Folder, bool) {
	if cfg.NoLive || cfg.RejoinAuto || len(cfg.RejoinFolders) > 0 {
		return Folder{}, false
	}
	for _, a := range accounts {
		if a.Suggested != "" {
			return Folder{Path: a.Suggested, Toon: a.Toon}, true
		}
	}
	return Folder{}, false
}

// autoLiveFolder sets the main account's rejoin folder, for a new install and for anyone who never set one. It runs
// after each scan: a new install has no accounts until the first scan has found its replays.
func (a *Agent) autoLiveFolder() {
	cfg := a.Config()
	accounts, _ := LiveAccounts(cfg, a.Scan())
	f, ok := mainLiveFolder(cfg, accounts)
	if !ok {
		return
	}
	a.Update(func(c *Config) {
		if len(c.RejoinFolders) == 0 {
			c.RejoinFolders = []Folder{f}
		}
		c.RejoinAuto = true
	})
	a.event("Live picks: on for your main account, using %s", f.Path)
}

func (a *Agent) rejoinDirs() []string {
	var out []string
	for _, f := range a.Config().RejoinFolders {
		if !f.Off {
			out = append(out, f.Path)
		}
	}
	return out
}

// promptLiveFolders asks once, with a notification, for the rejoin folders of accounts that have none.
func (a *Agent) promptLiveFolders() {
	cfg := a.Config()
	if cfg.NoLive || cfg.RejoinPrompted {
		return
	}
	accounts, _ := LiveAccounts(cfg, a.Scan())
	missing := liveMissing(accounts)
	if len(missing) == 0 {
		return
	}
	a.Update(func(c *Config) { c.RejoinPrompted = true })
	a.event("Live picks need a rejoin folder for %d account%s: set them in Settings, under Live game folders", len(missing), plural(len(missing)))
	if a.Notify != nil {
		title, text := "Set up live hero picks",
			"Choose the rejoin folder for each of your accounts in the uploader's settings. Until then the live page shows each player's most played hero, not what they picked."
		if len(missing) < len(accounts) {
			title, text = "Live hero picks for your other accounts",
				"Live picks are on for your main account. To get them on your others too, set their rejoin folders in the uploader's settings."
		}
		a.Notify(title, text, a.SettingsURL)
	}
}

// lobbySeenNow starts following saves for a new game.
func (a *Agent) lobbySeenNow() {
	a.mu.Lock()
	a.saves = savesState{lobbyAt: time.Now(), sent: map[string]time.Time{}}
	a.mu.Unlock()
	if len(a.rejoinDirs()) == 0 {
		a.event("No rejoin folder set, so this game's picks can't be read: the live page shows most played heroes (Settings → Live game folders)")
	}
}

// saveResult is what the server says about a rejoin save.
type saveResult struct {
	Applied int    `json:"applied"`
	Players int    `json:"players"`
	Talents int    `json:"talents"`
	More    bool   `json:"more"`
	Reason  string `json:"reason"`
}

// savesDone reports whether later saves of this game are worth sending. A server that reads talents says more
// (each save adds the talents picked since); an older one only wanted the heroes.
func savesDone(r saveResult) bool {
	return !r.More && r.Players > 0 && r.Applied >= r.Players
}

// checkSaves sends the newest rejoin save written since the game loaded (the game rewrites it about every 90s),
// for as long as the server wants them.
func (a *Agent) checkSaves() {
	a.mu.Lock()
	s := &a.saves
	if s.lobbyAt.IsZero() || s.done || time.Since(s.lobbyAt) > saveWindow || time.Since(s.lastCheck) < 5*time.Second {
		a.mu.Unlock()
		return
	}
	s.lastCheck = time.Now()
	lobbyAt := s.lobbyAt
	a.mu.Unlock()

	dirs := a.rejoinDirs()
	if len(dirs) == 0 {
		return
	}
	var newest string
	var newestAt time.Time
	for p, mt := range SaveFiles(dirs) {
		if mt.Before(lobbyAt) || time.Since(mt) < 2*time.Second { // older game, or still being written
			continue
		}
		a.mu.Lock()
		prev, seen := a.saves.sent[p]
		a.mu.Unlock()
		if seen && !mt.After(prev) {
			continue
		}
		if mt.After(newestAt) {
			newest, newestAt = p, mt
		}
	}
	if newest == "" {
		return
	}
	a.mu.Lock()
	a.saves.sent[newest] = newestAt
	a.mu.Unlock()

	// one save updates everyone in the game, so one uploader per team sends them. Asked once per save, so an uploader
	// that isn't sending asks again at the next one, and takes over if the sender has dropped out
	if !a.saveTurn() {
		a.mu.Lock()
		first := !a.saves.skipping
		a.saves.skipping = true
		a.mu.Unlock()
		if first {
			a.event("Another player's uploader is sending this game's picks, so this one won't send them")
		}
		return
	}
	a.mu.Lock()
	a.saves.skipping = false
	a.mu.Unlock()

	r, err := a.sendSave(newest)
	if err != nil {
		a.log.Printf("rejoin save: %v", err)
		return
	}
	a.mu.Lock()
	announce := r.Applied > 0 && !a.saves.announced
	talentsChanged := r.Talents != a.saves.talents
	if r.Applied > 0 {
		a.saves.announced = true
		a.saves.talents = r.Talents
	}
	if savesDone(r) {
		a.saves.done = true
	}
	a.mu.Unlock()
	if announce {
		a.event("Picks read from the game in progress (%d of %d players)", r.Applied, r.Players)
	} else if r.Applied > 0 && talentsChanged {
		a.log.Printf("rejoin save: %d talents picked so far", r.Talents)
	}
}

// saveTurn asks whether to send this save. Anything but a clear "no" sends it: an older server doesn't know the
// question, and a spare save does less harm than missing picks.
func (a *Agent) saveTurn() bool {
	var out struct {
		Skip bool `json:"skip"`
	}
	if err := a.postJSON("live/save-turn", map[string]any{}, &out); err != nil {
		return true
	}
	return !out.Skip
}

func (a *Agent) sendSave(p string) (saveResult, error) {
	var out saveResult
	if st, err := os.Stat(p); err == nil && st.Size() > maxSaveBytes {
		return out, errTooBig
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return out, err
	}
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	h := make(textproto.MIMEHeader)
	h.Set("Content-Disposition", fmt.Sprintf(`form-data; name="save"; filename="%s"`, strings.ReplaceAll(filepath.Base(p), `"`, "")))
	h.Set("Content-Type", "application/octet-stream")
	part, _ := w.CreatePart(h)
	_, _ = part.Write(b)
	_ = w.Close()
	if err := a.call("POST", "live/save", &buf, w.FormDataContentType(), &out); err != nil {
		return out, err
	}
	if out.Reason != "" {
		a.log.Printf("rejoin save %s: %s", filepath.Base(p), out.Reason)
	}
	return out, nil
}
