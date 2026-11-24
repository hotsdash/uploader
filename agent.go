// hots-dash uploader: sends your Heroes of the Storm replays to a hots-dash server and checks who's in
// a game as it loads. On Windows it lives in the tray (tray_windows.go); on Linux and Macs it runs in the background
// as a login service (main_unix.go), and from a terminal with flags, which is also how it's tested.
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

var version = "dev"

// Config is what the dashboard bakes into the download (see embedded.go), saved next to the state.
type Config struct {
	Server string `json:"server"`
	Token  string `json:"token"`
	// Enrol: the one-time code a fresh download carries instead of the token; swapped for it on first start (enrol)
	Enrol   string   `json:"enrol,omitempty"`
	Name    string   `json:"name,omitempty"`    // who it uploads as, for the tray
	Replays string   `json:"replays,omitempty"` // older single-folder setting, moved into Folders
	Folders []Folder `json:"folders,omitempty"` // replay folders: the game's, and any backups
	Lobby   string   `json:"lobby,omitempty"`   // override where the game writes the lobby file
	NoLive  bool     `json:"noLive,omitempty"`
	// RejoinFolders: where the game writes rejoin saves while a game is on, one per Battle.net account.
	// Kept apart from the replay folders: live picks only read these (saves.go).
	RejoinFolders []Folder `json:"rejoinFolders,omitempty"`
	// RejoinPrompted: the one-off "set up live picks" notification has been shown
	RejoinPrompted bool `json:"rejoinPrompted,omitempty"`
	// RejoinAuto: the main account's rejoin folder was set automatically, or a folder was removed by hand: don't set it again
	RejoinAuto bool `json:"rejoinAuto,omitempty"`
	// NoGameNotes: no notification after each game (result, personal bests, a nudge after a losing run)
	NoGameNotes bool `json:"noGameNotes,omitempty"`
	// NoAutoUpdate: say when a new version is ready instead of updating by itself
	NoAutoUpdate bool `json:"noAutoUpdate,omitempty"`
	// MoveDismissed: another site the dashboard said it moved to, which the person chose not to switch to
	MoveDismissed   string `json:"moveDismissed,omitempty"`
	MoveDismissedAt int64  `json:"moveDismissedAt,omitempty"` // when (unix seconds): suggested again after moveSnooze
	// Embedded: which download these settings came from, so the same copy starting again doesn't undo later changes
	Embedded string `json:"embedded,omitempty"`
	// AskSignIn: a copy with no settings from a download (the same file for everyone) opens the browser to sign in
	// once, on its first run. Cleared when it has.
	AskSignIn bool `json:"askSignIn,omitempty"`
}

// Folder is a place replays are kept: the game's Accounts folder, or a backup.
type Folder struct {
	Path string `json:"path"`
	Off  bool   `json:"off,omitempty"` // kept in the list but not uploaded from
	// Toon: for a rejoin folder, the Battle.net account it belongs to (2-Hero-1-269807)
	Toon string `json:"toon,omitempty"`
}

type fileState struct {
	Key  string `json:"key"` // size:mtime, so unchanged files aren't hashed again
	Hash string `json:"hash"`
	Sent bool   `json:"sent"`
}

// Event is a line in the recent activity list.
type Event struct {
	At   time.Time `json:"at"`
	Text string    `json:"text"`
}

// ScanStats: what the last pass found, per folder and per Battle.net account (toon folder).
type ScanStats struct {
	Folders map[string]*FolderStats `json:"folders"`
	Toons   map[string]int          `json:"toons"` // replays per toon, e.g. 2-Hero-1-269807
	// ToonDirs: each toon's folder (…\Accounts\<id>\2-Hero-1-269807), where its rejoin saves are expected
	ToonDirs map[string]string `json:"toonDirs"`
	Unique   int               `json:"unique"`
	Sent     int               `json:"sent"`
}

type FolderStats struct {
	Exists bool `json:"exists"`
	Files  int  `json:"files"`
	Sent   int  `json:"sent"`
}

// Status is what the tray shows.
type Status struct {
	Text      string
	Problem   bool
	Replays   int
	LastSync  time.Time
	LastLobby string
	LastGame  string
	Client    string // the game client, open or not (client.go)
}

type Agent struct {
	cfg    Config
	dir    string // where config, state and log live
	log    *log.Logger
	client *http.Client

	mu     sync.Mutex
	files  map[string]*fileState
	dirty  bool
	status Status
	recent []Event
	scan   ScanStats
	// the game client, from the process list (client.go)
	gameClient ClientState
	onCfg      func(Config) // persists settings changes

	syncing     atomic.Bool
	pausedTil   atomic.Int64 // unix ms: the server asked us to hold off uploading until then (503 + Retry-After)
	kick        chan struct{}
	lobbySeen   map[string]time.Time
	lastLobby   string
	lastLobbyAt time.Time
	tooBig      map[string]bool     // files skipped for size, already logged
	saves       savesState          // rejoin saves of the game that loaded last (saves.go)
	replayDirs  []string            // folders that held replays at the last scan (watch.go looks at them between scans)
	watch       watchState          // replays seen between scans (watch.go)
	seen        map[string]seenInfo // when each unsent replay was first seen, and how (timing.go)
	clock       clockOffset         // the dashboard's clock minus ours (timing.go)
	scanned     atomic.Bool         // a scan has finished since the uploader started

	signin signIn // signing in from the settings page (signin.go)
	// AutoSignIn: the real program starts the browser sign-in itself on first run (Config.AskSignIn); off in tests
	AutoSignIn bool
	authGen    atomic.Int64 // bumped when the account changes, so cached "is it yours" answers are asked again

	upd updater // updating itself (update.go)

	// SettingsURL: the settings page, for notifications that ask for something to be set up
	SettingsURL string

	// hooks for the tray
	OnUpdate func(latest string) // a new version is ready (automatic updates off)
	OnStatus func(Status)
	Notify   func(title, message, openURL string)
}

// ResetToons makes the settings page ask the dashboard again whose each account folder is.
func (a *Agent) ResetToons() { a.authGen.Add(1) }

func NewAgent(cfg Config, dir string, logger *log.Logger) *Agent {
	cfg.Server = strings.TrimRight(cfg.Server, "/")
	if len(cfg.Folders) == 0 {
		if cfg.Replays != "" {
			cfg.Folders = []Folder{{Path: cfg.Replays}}
		} else {
			for _, d := range FindReplayFolders() {
				cfg.Folders = append(cfg.Folders, Folder{Path: d})
			}
		}
		cfg.Replays = ""
	}
	if cfg.Lobby == "" {
		// in Wine the game's %TEMP% is inside its prefix, not this computer's own
		if cfg.Lobby = wineLobbyDir(cfg.Folders); cfg.Lobby == "" {
			cfg.Lobby = filepath.Join(os.TempDir(), "Heroes of the Storm")
		}
	}
	tr := newFallbackTransport() // copes with networks that intercept the dashboard (netfix.go)
	a := &Agent{cfg: cfg, dir: dir, log: logger, client: &http.Client{Timeout: 5 * time.Minute, Transport: tr},
		files: map[string]*fileState{}, kick: make(chan struct{}, 1), lobbySeen: map[string]time.Time{}}
	tr.OnFallback = func(host string, ips []string, ech bool) {
		how := ""
		if ech {
			how = " with the name hidden (encrypted hello)"
		}
		a.event("Your network answered for %s instead of the dashboard: going around it via %s%s", host, strings.Join(ips, ", "), how)
	}
	a.loadState()
	return a
}

func (a *Agent) Config() Config {
	a.mu.Lock()
	defer a.mu.Unlock()
	c := a.cfg
	c.Folders = append([]Folder(nil), a.cfg.Folders...)
	return c
}

// Update changes the settings (from the settings page) and saves them.
func (a *Agent) Update(f func(c *Config)) Config {
	a.mu.Lock()
	f(&a.cfg)
	c := a.cfg
	c.Folders = append([]Folder(nil), a.cfg.Folders...)
	a.mu.Unlock()
	if a.onCfg != nil {
		a.onCfg(c)
	}
	return c
}

// OnConfigChange is called with the new settings whenever they change.
func (a *Agent) OnConfigChange(f func(Config)) { a.onCfg = f }

func (a *Agent) event(format string, args ...any) {
	text := fmt.Sprintf(format, args...)
	a.log.Print(text)
	a.mu.Lock()
	a.recent = append([]Event{{At: time.Now(), Text: text}}, a.recent...)
	if len(a.recent) > 40 {
		a.recent = a.recent[:40]
	}
	a.mu.Unlock()
}

func (a *Agent) Recent() []Event {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]Event(nil), a.recent...)
}

func (a *Agent) Scan() ScanStats {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.scan
}

func (a *Agent) enabledFolders() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	var out []string
	for _, f := range a.cfg.Folders {
		if !f.Off {
			out = append(out, f.Path)
		}
	}
	return out
}

// ---------- folders ----------

// FindReplayFolders lists the places Heroes keeps replays on this computer (Documents, OneDrive, Mac, Wine on Linux).
func FindReplayFolders() []string {
	home, _ := os.UserHomeDir()
	var candidates []string
	for _, env := range []string{"OneDrive", "OneDriveConsumer", "OneDriveCommercial"} {
		if od := os.Getenv(env); od != "" {
			candidates = append(candidates, filepath.Join(od, "Documents", "Heroes of the Storm", "Accounts"))
		}
	}
	candidates = append(candidates,
		filepath.Join(home, "Documents", "Heroes of the Storm", "Accounts"),
		filepath.Join(home, "OneDrive", "Documents", "Heroes of the Storm", "Accounts"),
		filepath.Join(home, "Library", "Application Support", "Blizzard", "Heroes of the Storm", "Accounts"),
	)
	if m, _ := filepath.Glob(filepath.Join(home, "OneDrive*", "Documents", "Heroes of the Storm", "Accounts")); len(m) > 0 {
		candidates = append(candidates, m...)
	}
	if runtime.GOOS == "linux" {
		candidates = append(candidates, wineReplayFolders(home)...)
	}
	var out []string
	seen := map[string]bool{}
	for _, c := range candidates {
		key := strings.ToLower(filepath.Clean(c))
		if seen[key] {
			continue
		}
		seen[key] = true
		if st, err := os.Stat(c); err == nil && st.IsDir() {
			out = append(out, c)
		}
	}
	return out
}

// ---------- state ----------

func (a *Agent) statePath() string { return filepath.Join(a.dir, "state.json") }

func (a *Agent) loadState() {
	b, err := os.ReadFile(a.statePath())
	if err != nil {
		return
	}
	var s struct {
		Files map[string]*fileState `json:"files"`
	}
	if json.Unmarshal(b, &s) == nil && s.Files != nil {
		a.files = s.Files
	}
}

func (a *Agent) saveState() {
	a.mu.Lock()
	if !a.dirty {
		a.mu.Unlock()
		return
	}
	b, err := json.Marshal(map[string]any{"files": a.files})
	a.dirty = false
	a.mu.Unlock()
	if err != nil {
		return
	}
	tmp := a.statePath() + ".tmp"
	if os.WriteFile(tmp, b, 0o600) == nil {
		_ = os.Rename(tmp, a.statePath())
	}
}

func (a *Agent) setStatus(f func(s *Status)) {
	a.mu.Lock()
	f(&a.status)
	s := a.status
	a.mu.Unlock()
	if a.OnStatus != nil {
		a.OnStatus(s)
	}
}

func (a *Agent) Status() Status {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.status
}

// ---------- server ----------

// ErrToken means the server refused the token (rotated, or the person was removed).
var ErrToken = errors.New("the server didn't accept this uploader's sign-in")

// errNotFound: the dashboard doesn't have that (an older one, or nothing there).
var errNotFound = errors.New("not found")

func (a *Agent) call(method, path string, body io.Reader, contentType string, out any) error {
	return a.do(method, path, body, contentType, out, true)
}

// callAnon posts JSON without the token (signing in).
func (a *Agent) callAnon(path string, in, out any) error {
	b, _ := json.Marshal(in)
	return a.do("POST", path, bytes.NewReader(b), "application/json", out, false)
}

func (a *Agent) do(method, path string, body io.Reader, contentType string, out any, withToken bool) error {
	cfg := a.Config()
	req, err := http.NewRequest(method, cfg.Server+"/api/"+path, body)
	if err != nil {
		return err
	}
	if withToken && cfg.Token != "" {
		req.Header.Set("Authorization", "Bearer "+cfg.Token)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	req.Header.Set("User-Agent", userAgent())
	res, err := a.client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode == 401 || res.StatusCode == 403 {
		return ErrToken
	}
	// the dashboard has uploads paused (503) or we're sending too fast (429): wait as long as it asks before trying again
	if res.StatusCode == 503 || res.StatusCode == 429 {
		if secs, err := strconv.Atoi(strings.TrimSpace(res.Header.Get("Retry-After"))); err == nil && secs > 0 {
			a.pausedTil.Store(time.Now().Add(time.Duration(min(secs, 3600)) * time.Second).UnixMilli())
		}
	}
	if res.StatusCode == 404 {
		return fmt.Errorf("%s: %w", path, errNotFound)
	}
	if res.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(res.Body, 300))
		return fmt.Errorf("%s: %d %s", path, res.StatusCode, strings.TrimSpace(string(b)))
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(res.Body).Decode(out)
}

func (a *Agent) postJSON(path string, in, out any) error {
	b, _ := json.Marshal(in)
	return a.call("POST", path, bytes.NewReader(b), "application/json", out)
}

// ---------- replays ----------

func walkReplays(dir string) []string {
	var out []string
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // unreadable folder: skip it
		}
		if !d.IsDir() && strings.HasSuffix(strings.ToLower(d.Name()), ".stormreplay") {
			out = append(out, p)
		}
		return nil
	})
	sort.Strings(out)
	return out
}

// maxReplayBytes: replays are a few MB; anything bigger than the dashboard accepts (20 MB, ingest.controller.ts)
// isn't read into memory, and doesn't fail the rest of its batch.
const maxReplayBytes = 20 << 20

// maxSaveBytes: the dashboard's limit for a rejoin save (30 MB, live.controller.ts).
const maxSaveBytes = 30 << 20

var errTooBig = errors.New("too big to be a replay")

func (a *Agent) hashFile(p string) (*fileState, os.FileInfo, error) {
	st, err := os.Stat(p)
	if err != nil {
		return nil, nil, err
	}
	if st.Size() > maxReplayBytes {
		a.mu.Lock()
		told := a.tooBig[p]
		if a.tooBig == nil {
			a.tooBig = map[string]bool{}
		}
		a.tooBig[p] = true
		a.mu.Unlock()
		if !told {
			a.log.Printf("skipping %s: %d MB is too big to be a replay", filepath.Base(p), st.Size()>>20)
		}
		return nil, nil, errTooBig
	}
	key := fmt.Sprintf("%d:%d", st.Size(), st.ModTime().UnixMilli())
	a.mu.Lock()
	cached := a.files[p]
	a.mu.Unlock()
	if cached != nil && cached.Key == key {
		return cached, st, nil
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, nil, err
	}
	sum := sha256.Sum256(b)
	fsx := &fileState{Key: key, Hash: hex.EncodeToString(sum[:])}
	a.mu.Lock()
	a.files[p] = fsx
	a.dirty = true
	a.mu.Unlock()
	return fsx, st, nil
}

func (a *Agent) markSent(p string) {
	a.forgetSeen(p)
	a.mu.Lock()
	if f := a.files[p]; f != nil && !f.Sent {
		f.Sent = true
		a.dirty = true
	}
	a.mu.Unlock()
}

type uploadResult struct {
	FileName string `json:"fileName"`
	Status   string `json:"status"`
	Error    string `json:"error"`
	ReplayID int64  `json:"replayId"`
}

func (a *Agent) uploadBatch(paths []string) ([]uploadResult, error) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	_ = w.WriteField("source", "agent")
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		st, _ := os.Stat(p)
		h := make(textproto.MIMEHeader)
		h.Set("Content-Disposition", fmt.Sprintf(`form-data; name="files"; filename="%s"`, strings.ReplaceAll(filepath.Base(p), `"`, "")))
		h.Set("Content-Type", "application/octet-stream")
		part, _ := w.CreatePart(h)
		_, _ = part.Write(b)
		_ = w.WriteField("paths", sharedPath(p))
		if st != nil {
			_ = w.WriteField("mtimes", st.ModTime().UTC().Format(timeLayout))
		}
		seenAt, how := a.seenFields(p)
		_ = w.WriteField("seen", seenAt)
		_ = w.WriteField("found", how)
	}
	// when sending began, and how far our clock is from the dashboard's (Admin › Game delays)
	_ = w.WriteField("sentAt", time.Now().UTC().Format(timeLayout))
	if off := a.clock.field(); off != "" {
		_ = w.WriteField("clockOffsetMs", off)
	}
	_ = w.Close()
	var out struct {
		Results []uploadResult `json:"results"`
	}
	if err := a.call("POST", "replays", &buf, w.FormDataContentType(), &out); err != nil {
		return nil, err
	}
	for _, p := range paths {
		a.markSent(p)
	}
	return out.Results, nil
}

// sharedPath: the part of a replay's path the dashboard gets, from the Battle.net account folder on
// (2-Hero-1-269807\Replays\Multiplayer\x.StormReplay), which is what links the account to you. Never the rest:
// that holds the Windows user name and folder layout. A replay outside an account folder is sent by name only.
func sharedPath(p string) string {
	if m := sharedPathRe.FindStringSubmatchIndex(p); m != nil {
		return p[m[2]:]
	}
	return p[strings.LastIndexAny(p, `\/`)+1:] // the name (either separator, whatever this computer uses)
}

var sharedPathRe = regexp.MustCompile(`(?i)(?:^|[\\/])(\d+-Hero-\d+-\d+[\\/]+Replays[\\/].*)$`)

var toonRe = regexp.MustCompile(`(?i)(\d+-Hero-\d+-\d+)[\\/]+Replays`)

// Sync uploads every replay the server doesn't have yet, from every folder that's switched on. Safe to call
// often: unchanged files aren't re-read, and a replay that's in several folders (a backup) goes up once.
func (a *Agent) Sync() error {
	if til := time.UnixMilli(a.pausedTil.Load()); time.Now().Before(til) {
		a.setStatus(func(s *Status) {
			s.Text = fmt.Sprintf("The dashboard asked to wait a little: trying again at %s", til.Format("15:04"))
			s.Problem = false
		})
		return nil
	}
	if !a.syncing.CompareAndSwap(false, true) {
		return nil
	}
	defer a.syncing.Store(false)
	defer a.saveState()
	a.enrol()
	defer a.autoSignIn()

	stats := ScanStats{Folders: map[string]*FolderStats{}, Toons: map[string]int{}, ToonDirs: map[string]string{}}
	byHash := map[string][]string{} // hash → every path that has it
	folderOf := map[string]string{} // path → the folder it was found in
	modOf := map[string]time.Time{} // hash → when its file was written
	var order []string              // hashes waiting, in the order found
	recent := time.Now().Add(-replaySettle)
	how := foundScan
	if !a.scanned.Load() {
		how = foundStart
	}
	scanAt := time.Now()
	folders := a.enabledFolders()
	var allPaths []string
	for _, dir := range folders {
		fs := &FolderStats{}
		stats.Folders[dir] = fs
		if st, err := os.Stat(dir); err != nil || !st.IsDir() {
			continue
		}
		fs.Exists = true
		for _, p := range walkReplays(dir) {
			f, st, err := a.hashFile(p)
			if err != nil {
				continue
			}
			allPaths = append(allPaths, p)
			fs.Files++
			folderOf[p] = dir
			first := len(byHash[f.Hash]) == 0
			byHash[f.Hash] = append(byHash[f.Hash], p)
			if first {
				stats.Unique++
				if m := toonRe.FindStringSubmatch(p); m != nil {
					stats.Toons[m[1]]++
					if _, ok := stats.ToonDirs[m[1]]; !ok {
						if _, d := toonDirOf(p); d != "" {
							stats.ToonDirs[m[1]] = d
						}
					}
				}
				if !f.Sent {
					a.noteSeen(p, how, scanAt)
				}
				if !f.Sent && !st.ModTime().After(recent) { // the game may still be writing a brand-new one
					order = append(order, f.Hash)
					modOf[f.Hash] = st.ModTime()
				}
			}
		}
	}
	// a hash already sent from any folder counts as sent everywhere
	pending := order[:0]
	for _, h := range order {
		sent := false
		for _, p := range byHash[h] {
			a.mu.Lock()
			if fs := a.files[p]; fs != nil && fs.Sent {
				sent = true
			}
			a.mu.Unlock()
		}
		if sent {
			for _, p := range byHash[h] {
				a.markSent(p)
			}
		} else {
			pending = append(pending, h)
		}
	}
	a.mu.Lock()
	a.scan = stats
	a.replayDirs = replayDirsOf(allPaths)
	a.mu.Unlock()
	a.scanned.Store(true)
	a.setStatus(func(s *Status) { s.Replays = stats.Unique })
	a.autoLiveFolder()
	a.promptLiveFolders()
	if len(folders) == 0 {
		a.setStatus(func(s *Status) { s.Text = "No replay folders: add one in Settings"; s.Problem = true })
		return nil
	}

	var unknown []string // one path per hash to upload
	for i := 0; i < len(pending); i += 500 {
		chunk := pending[i:min(i+500, len(pending))]
		paths := make([]string, len(chunk))
		for j, h := range chunk {
			paths[j] = sharedPath(byHash[h][0])
		}
		var r struct {
			Known []string `json:"known"`
			Now   int64    `json:"now"`
		}
		t0 := time.Now()
		if err := a.postJSON("replays/known", map[string]any{"hashes": chunk, "paths": paths}, &r); err != nil {
			a.problem(err)
			return err
		}
		a.clock.measure(r.Now, t0, time.Now())
		known := map[string]bool{}
		for _, h := range r.Known {
			known[h] = true
		}
		for _, h := range chunk {
			if known[h] {
				for _, p := range byHash[h] {
					a.markSent(p)
				}
			} else {
				unknown = append(unknown, h)
			}
		}
	}

	// newest games first: the server shares its reader out one turn per person, and your latest games are the ones you want to see
	sort.SliceStable(unknown, func(i, j int) bool { return modOf[unknown[i]].After(modOf[unknown[j]]) })

	added := 0
	for i := 0; i < len(unknown); i += 10 {
		a.setStatus(func(s *Status) { s.Text = fmt.Sprintf("Uploading %d of %d…", i+1, len(unknown)); s.Problem = false })
		batch := unknown[i:min(i+10, len(unknown))]
		paths := make([]string, len(batch))
		for j, h := range batch {
			paths[j] = byHash[h][0]
		}
		res, err := a.uploadBatch(paths)
		if err != nil {
			a.problem(err)
			return err
		}
		for _, h := range batch {
			for _, p := range byHash[h] {
				a.markSent(p)
			}
		}
		for j, r := range res {
			if r.Status == "queued" {
				added++
				// a game that just finished: tell them how it went once the server has read it
				if j < len(paths) && r.ReplayID > 0 {
					if st, err := os.Stat(paths[j]); err == nil && time.Since(st.ModTime()) < 20*time.Minute {
						go a.followGame(r.ReplayID)
					}
				}
			}
		}
		if i%100 == 0 {
			a.saveState()
		}
	}
	if len(unknown) > 0 {
		a.event("Uploaded %d replay%s (%d new to the dashboard)", len(unknown), plural(len(unknown)), added)
	}
	// recount what's sent now, per folder
	a.mu.Lock()
	for _, ps := range byHash {
		if fs := a.files[ps[0]]; fs != nil && fs.Sent {
			a.scan.Sent++
			for _, p := range ps {
				if st := a.scan.Folders[folderOf[p]]; st != nil {
					st.Sent++
				}
			}
		}
	}
	a.mu.Unlock()
	a.setStatus(func(s *Status) {
		s.Problem = false
		s.LastSync = time.Now()
		s.Text = fmt.Sprintf("Up to date · %d replays", stats.Unique)
	})
	return nil
}

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
			open, ask := reviewLink(a.cfg.Server, s)
			if ask != "" {
				lines = append(lines, ask)
			}
			a.Notify(s.Title, strings.Join(lines, "\n"), open)
		}
		return
	}
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

func (a *Agent) problem(err error) {
	text := "Can't reach the dashboard"
	if errors.Is(err, ErrToken) {
		text = "Sign-in not accepted: sign in again in Settings"
	} else if errors.Is(err, errBlocked) {
		text = "Your network is blocking the dashboard: see Recent activity"
	}
	if prev := a.Status(); !prev.Problem || prev.Text != text {
		a.event("Problem: %v", err)
	}
	a.setStatus(func(s *Status) { s.Text = text; s.Problem = true })
}

// SyncSoon asks the run loop for a sync (from the tray's "Sync now").
func (a *Agent) SyncSoon() {
	select {
	case a.kick <- struct{}{}:
	default:
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
	root := a.cfg.Lobby
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
		a.Notify(title, strings.Join(lines, "\n"), a.cfg.Server+"/live")
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
		title, message, open := reviewedNote(a.cfg.Server, n)
		a.event("%s", title)
		if a.Notify != nil && !a.Config().NoGameNotes {
			a.Notify(title, message, open)
		}
	}
}

// ---------- main loop ----------

// Run syncs now, then keeps checking for new replays (a full scan every 30s, plus a quick look whenever the system says a
// replay folder changed, watch.go) and games loading (every 2s) until stop closes.
func (a *Agent) Run(stop <-chan struct{}) {
	a.event("Started: uploading to %s from %d folder%s", a.cfg.Server, len(a.enabledFolders()), plural(len(a.enabledFolders())))
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
