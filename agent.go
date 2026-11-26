// hots-dash uploader: sends your Heroes of the Storm replays to a hots-dash server and checks who's in
// a game as it loads. On Windows it lives in the tray (tray_windows.go); on Linux and Macs it runs in the background
// as a login service (main_unix.go), and from a terminal with flags, which is also how it's tested.
package main

import (
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hotsdash/uploader/internal/netfix"
)

var version = "dev"

// Config is what the dashboard bakes into the download (see config.go), saved next to the state.
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
	// NoNetworkWorkaround: don't go around a network that answers for hotsdash.com (internal/netfix). Off by default,
	// so settings saved before it carry on as they did
	NoNetworkWorkaround bool `json:"noNetworkWorkaround,omitempty"`
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
	// for a replay the dashboard didn't accept: how many times, and when to try again (unix ms). A changed file
	// gets a new fileState, so it starts again.
	Fails   int   `json:"fails,omitempty"`
	RetryAt int64 `json:"retryAt,omitempty"`
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
	cfg.Lobby = lobbyDir(runtime.GOOS, cfg.Lobby, cfg.Folders)
	tr := netfix.New() // copes with networks that intercept the dashboard (internal/netfix)
	a := &Agent{cfg: cfg, dir: dir, log: logger, client: &http.Client{Timeout: 5 * time.Minute, Transport: tr},
		files: map[string]*fileState{}, kick: make(chan struct{}, 1), lobbySeen: map[string]time.Time{}}
	tr.Allow = a.goAround
	tr.OnFallback = func(host string, ips []string, ech bool) {
		how := ""
		if ech {
			how = " with the name hidden (encrypted hello)"
		}
		a.event("Your network answered for %s instead of the dashboard: going around it via %s%s", host, strings.Join(ips, ", "), how)
	}
	a.client.CheckRedirect = apiRedirect
	a.loadState()
	return a
}

// goAround: whether the network workaround may be used for a host: only the public dashboard's, and only while
// it isn't switched off in Settings.
func (a *Agent) goAround(host string) bool {
	u, err := url.Parse(defaultServer)
	return err == nil && strings.EqualFold(host, u.Hostname()) && !a.Config().NoNetworkWorkaround
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

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
