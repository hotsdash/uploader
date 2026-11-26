package main

import (
	"crypto/rand"
	"crypto/subtle"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// The settings page: a small web page served only to this computer (127.0.0.1, random port), opened from the
// tray. Every request needs the secret in the address the tray opens, so other web pages can't change settings.
//
// Updating restarts the program, which would leave an open settings page talking to nothing. So the old copy
// leaves a handoff (port, secret, whether the page was open) and the new copy serves the page at the same address:
// the open page reconnects and reloads itself. If that port can't be had again, the new copy opens the page at
// its new address instead (only when it was open before).

//go:embed ui.html
var uiHTML string

var errNoPicker = errors.New("no folder picker on this system")

// expandHome: a pasted ~/… path (Linux, Mac) is in the home folder.
func expandHome(p string) string {
	if runtime.GOOS != "windows" && (p == "~" || strings.HasPrefix(p, "~/")) {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, strings.TrimPrefix(p, "~"))
		}
	}
	return p
}

// systemText: the settings page's wording that depends on the system (the page is written for Windows).
func systemText() map[string]string {
	if runtime.GOOS == "windows" {
		return map[string]string{
			"os": "windows", "startLabel": "Start with Windows", "notification": "a Windows notification",
			"where":       "The uploader sits by the clock and keeps sending new replays as you play.",
			"reopen":      "open it again from the Start menu or tray.",
			"examplePath": `D:\Backups\HotS Replays`,
			"forbidden":   "Open the settings from the hots-dash icon in the tray.",
		}
	}
	return map[string]string{
		"os": runtime.GOOS, "startLabel": "Start when you log in", "notification": "a notification",
		"where":       "The uploader runs in the background and keeps sending new replays as you play. To open this page again, run hots-dash-uploader.",
		"reopen":      "run hots-dash-uploader to start it again.",
		"examplePath": "~/Backups/HotS Replays",
		"forbidden":   "Open the settings by running hots-dash-uploader.",
	}
}

type ui struct {
	a    *Agent
	key  string
	port int
	seen atomic.Int64 // when the page last asked for anything (unix ms)

	mu       sync.Mutex
	toons    map[string]toonInfo // battletags for account folders, from the dashboard
	toonsAt  time.Time
	toonsKey string
}

type toonInfo struct {
	Toon      string `json:"toon"`
	Name      string `json:"name"`
	Battletag string `json:"battletag"`
	Games     int    `json:"games"`
	Yours     bool   `json:"yours"`
	Taken     bool   `json:"taken"` // linked to someone else on the dashboard
}

// uiHandoff is what an updating copy passes to the next one about its settings page.
type uiHandoff struct {
	Port     int       `json:"port"`
	Key      string    `json:"key"`
	PageOpen bool      `json:"pageOpen"`
	At       time.Time `json:"at"`
}

const handoffFile = "ui-handoff.json"

// pageOpenWithin: the page polls every 2s, so a request in the last 15s means it's open in a browser.
const pageOpenWithin = 15 * time.Second

// writeHandoff records the settings page's address for the copy that's about to take over.
func writeHandoff(dir string, h uiHandoff) error {
	b, err := json.Marshal(h)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, handoffFile), b, 0o600)
}

// takeHandoff reads (and removes) what the previous copy left, if it left it in the last couple of minutes.
func takeHandoff(dir string) *uiHandoff {
	p := filepath.Join(dir, handoffFile)
	b, err := os.ReadFile(p)
	if err != nil {
		return nil
	}
	_ = os.Remove(p)
	var h uiHandoff
	if json.Unmarshal(b, &h) != nil || h.Port <= 0 || len(h.Key) != 32 || time.Since(h.At) > 2*time.Minute {
		return nil
	}
	return &h
}

// listenAgain tries the previous copy's port for a few seconds (it may still be letting go of it).
func listenAgain(port int, tries int, gap time.Duration) net.Listener {
	for i := 0; i < tries; i++ {
		if ln, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(port)); err == nil {
			return ln
		}
		time.Sleep(gap)
	}
	return nil
}

// StartUI serves the settings page. With a handoff from the copy it's replacing, it serves it at the same
// address when it can (Reused() says whether it did).
func StartUI(a *Agent, prev *uiHandoff) (*ui, error) {
	var ln net.Listener
	key := ""
	if prev != nil {
		if ln = listenAgain(prev.Port, 20, 250*time.Millisecond); ln != nil {
			key = prev.Key
		}
	}
	if ln == nil {
		var err error
		if ln, err = net.Listen("tcp", "127.0.0.1:0"); err != nil {
			return nil, err
		}
		b := make([]byte, 16)
		_, _ = rand.Read(b)
		key = hex.EncodeToString(b)
	}
	u := &ui{a: a, key: key, port: ln.Addr().(*net.TCPAddr).Port, toons: map[string]toonInfo{}}
	mux := http.NewServeMux()
	mux.HandleFunc("/", u.page)
	mux.HandleFunc("/api/state", u.api(u.state))
	mux.HandleFunc("/api/folders", u.api(u.folders))
	mux.HandleFunc("/api/sync", u.api(func(r *http.Request) (any, error) { a.SyncSoon(); return map[string]bool{"ok": true}, nil }))
	mux.HandleFunc("/api/live", u.api(u.live))
	mux.HandleFunc("/api/rejoin", u.api(u.rejoin))
	mux.HandleFunc("/api/games", u.api(u.games))
	mux.HandleFunc("/api/autostart", u.api(u.autostart))
	mux.HandleFunc("/api/open", u.api(u.open))
	mux.HandleFunc("/api/signin", u.api(u.signIn))
	mux.HandleFunc("/api/update", u.api(u.update))
	mux.HandleFunc("/api/move", u.api(u.move))
	go func() { _ = http.Serve(ln, u.guard(mux)) }()
	return u, nil
}

// Reused: serving at the previous copy's address, so a page that was open carries on by itself.
func (u *ui) Reused(prev *uiHandoff) bool {
	return prev != nil && prev.Port == u.port && prev.Key == u.key
}

// Handoff: this page's address, and whether it's open in a browser right now.
func (u *ui) Handoff() uiHandoff {
	seen := u.seen.Load()
	return uiHandoff{Port: u.port, Key: u.key, PageOpen: seen > 0 && time.Since(time.UnixMilli(seen)) < pageOpenWithin, At: time.Now()}
}

func (u *ui) URL() string { return fmt.Sprintf("http://127.0.0.1:%d/?k=%s", u.port, u.key) }

// guard: only this computer, by its loopback name (stops DNS-rebinding pages).
func (u *ui) guard(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := r.Host
		if host != "127.0.0.1:"+strconv.Itoa(u.port) && host != "localhost:"+strconv.Itoa(u.port) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Frame-Options", "DENY")
		h.ServeHTTP(w, r)
	})
}

// keyOK: the secret, compared in constant time so how long a wrong one takes says nothing about it.
func (u *ui) keyOK(k string) bool {
	return subtle.ConstantTimeCompare([]byte(k), []byte(u.key)) == 1
}

func (u *ui) page(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" || !u.keyOK(r.URL.Query().Get("k")) {
		http.Error(w, systemText()["forbidden"], http.StatusForbidden)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; img-src 'self' data:")
	_, _ = w.Write([]byte(strings.Replace(uiHTML, "__KEY__", u.key, 1)))
}

func (u *ui) api(f func(*http.Request) (any, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !u.keyOK(r.Header.Get("X-Key")) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		u.seen.Store(time.Now().UnixMilli())
		if r.Method != http.MethodGet && r.Method != http.MethodPost {
			http.Error(w, "method", http.StatusMethodNotAllowed)
			return
		}
		out, err := f(r)
		w.Header().Set("Content-Type", "application/json")
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}
		_ = json.NewEncoder(w).Encode(out)
	}
}

func body(r *http.Request, v any) error {
	if r.Method != http.MethodPost {
		return errors.New("POST only")
	}
	return json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<16)).Decode(v)
}

// ---------- state ----------

type folderView struct {
	Path   string `json:"path"`
	On     bool   `json:"on"`
	Exists bool   `json:"exists"`
	Files  int    `json:"files"`
	Sent   int    `json:"sent"`
}

type accountView struct {
	toonInfo
	Region  string `json:"region"`
	Replays int    `json:"replays"`
}

var regions = map[string]string{"1": "Americas", "2": "Europe", "3": "Asia", "5": "China", "98": "PTR"}

func (u *ui) state(r *http.Request) (any, error) {
	cfg := u.a.Config()
	scan := u.a.Scan()
	st := u.a.Status()
	var folders []folderView
	inList := map[string]bool{}
	for _, f := range cfg.Folders {
		v := folderView{Path: f.Path, On: !f.Off}
		if fs := scan.Folders[f.Path]; fs != nil {
			v.Exists, v.Files, v.Sent = fs.Exists, fs.Files, fs.Sent
		} else if s, err := os.Stat(f.Path); err == nil && s.IsDir() {
			v.Exists = true
		}
		folders = append(folders, v)
		inList[strings.ToLower(filepath.Clean(f.Path))] = true
	}
	var suggest []string
	for _, d := range FindReplayFolders() {
		if !inList[strings.ToLower(filepath.Clean(d))] {
			suggest = append(suggest, d)
		}
	}
	names := u.toonNames(scan.Toons)
	var accounts []accountView
	for t, n := range scan.Toons {
		v := accountView{toonInfo: names[t], Replays: n, Region: regions[strings.SplitN(t, "-", 2)[0]]}
		v.Toon = t
		accounts = append(accounts, v)
	}
	sort.Slice(accounts, func(i, j int) bool { return accounts[i].Replays > accounts[j].Replays })
	// live game folders: each account's rejoin folder, apart from the replay folders
	liveAccts, liveOthers := LiveAccounts(cfg, scan)
	type liveAccountRow struct {
		LiveAccount
		Battletag string `json:"battletag"`
		Name      string `json:"name"`
		Region    string `json:"region"`
	}
	var liveRows []liveAccountRow
	for _, la := range liveAccts {
		n := names[la.Toon]
		liveRows = append(liveRows, liveAccountRow{LiveAccount: la, Battletag: n.Battletag, Name: n.Name, Region: regions[strings.SplitN(la.Toon, "-", 2)[0]]})
	}
	var otherRows []folderView
	for _, f := range liveOthers {
		v := folderView{Path: f.Path, On: !f.Off}
		if st, err := os.Stat(f.Path); err == nil && st.IsDir() {
			v.Exists = true
			v.Files = len(SaveFiles([]string{f.Path}))
		}
		otherRows = append(otherRows, v)
	}
	auto, autoOn := autostartState()
	var lastSync *time.Time
	if !st.LastSync.IsZero() {
		lastSync = &st.LastSync
	}
	return map[string]any{
		"version": version, "server": cfg.Server, "name": cfg.Name,
		"signedIn": cfg.Token != "", "signin": u.a.SignInState(),
		"status":  map[string]any{"text": st.Text, "problem": st.Problem, "lastSync": lastSync, "lastLobby": st.LastLobby, "lastGame": st.LastGame, "client": st.Client},
		"folders": folders, "suggest": suggest, "picker": hasFolderPicker(),
		"replays": scan.Unique, "sent": scan.Sent,
		"accounts":  accounts,
		"live":      map[string]any{"on": !cfg.NoLive, "folder": cfg.Lobby},
		"rejoin":    map[string]any{"accounts": liveRows, "others": otherRows, "missing": len(liveMissing(liveAccts)), "set": len(u.a.rejoinDirs())},
		"games":     map[string]any{"on": !cfg.NoGameNotes},
		"autostart": map[string]bool{"supported": auto, "on": autoOn},
		"system":    systemText(),
		"recent":    u.a.Recent(),
		"update":    u.a.UpdateState(),
	}, nil
}

// toonNames asks the dashboard who each account folder is (battletag, games, whether it's linked to you).
// Cached; asked again when new folders appear or every 10 minutes.
func (u *ui) toonNames(toons map[string]int) map[string]toonInfo {
	var list []string
	for t := range toons {
		list = append(list, t)
	}
	sort.Strings(list)
	key := fmt.Sprintf("%d|%s", u.a.authGen.Load(), strings.Join(list, ","))
	u.mu.Lock()
	fresh := key == u.toonsKey && time.Since(u.toonsAt) < 10*time.Minute
	out := u.toons
	u.mu.Unlock()
	if fresh || len(list) == 0 {
		return out
	}
	var r struct {
		Toons []toonInfo `json:"toons"`
	}
	if err := u.a.postJSON("replays/toons", map[string]any{"toons": list}, &r); err != nil {
		return out
	}
	m := map[string]toonInfo{}
	for _, t := range r.Toons {
		m[t.Toon] = t
	}
	u.mu.Lock()
	u.toons, u.toonsKey, u.toonsAt = m, key, time.Now()
	u.mu.Unlock()
	return m
}

// ---------- actions ----------

func (u *ui) folders(r *http.Request) (any, error) {
	var in struct {
		Action string `json:"action"` // add | pick | toggle | remove
		Path   string `json:"path"`
		On     bool   `json:"on"`
	}
	if err := body(r, &in); err != nil {
		return nil, err
	}
	path := expandHome(strings.TrimSpace(strings.Trim(in.Path, `"`)))
	switch in.Action {
	case "pick":
		p, err := pickFolder()
		if err != nil {
			return nil, err
		}
		if p == "" {
			return map[string]bool{"cancelled": true}, nil
		}
		path = p
		fallthrough
	case "add":
		if path == "" {
			return nil, errors.New("choose a folder")
		}
		path = filepath.Clean(path)
		if s, err := os.Stat(path); err != nil || !s.IsDir() {
			return nil, fmt.Errorf("can't open %s", path)
		}
		u.a.Update(func(c *Config) {
			for _, f := range c.Folders {
				if strings.EqualFold(filepath.Clean(f.Path), path) {
					return
				}
			}
			c.Folders = append(c.Folders, Folder{Path: path})
		})
		u.a.event("Added folder %s", path)
	case "toggle":
		u.a.Update(func(c *Config) {
			for i := range c.Folders {
				if c.Folders[i].Path == path {
					c.Folders[i].Off = !in.On
				}
			}
		})
	case "remove":
		u.a.Update(func(c *Config) {
			out := c.Folders[:0]
			for _, f := range c.Folders {
				if f.Path != path {
					out = append(out, f)
				}
			}
			c.Folders = out
		})
		u.a.event("Removed folder %s", path)
	default:
		return nil, errors.New("unknown action")
	}
	u.a.SyncSoon()
	return map[string]any{"ok": true, "path": path}, nil
}

// rejoin: the live game folders. use (an account's own rejoin folder), useAll (every account's), pick or add
// (a folder of your choosing, for one account or none), toggle, remove.
func (u *ui) rejoin(r *http.Request) (any, error) {
	var in struct {
		Action string `json:"action"`
		Toon   string `json:"toon"`
		Path   string `json:"path"`
		On     bool   `json:"on"`
	}
	if err := body(r, &in); err != nil {
		return nil, err
	}
	path := expandHome(strings.TrimSpace(strings.Trim(in.Path, `"`)))
	accounts, _ := LiveAccounts(u.a.Config(), u.a.Scan())
	suggestedFor := func(toon string) string {
		for _, a := range accounts {
			if a.Toon == toon {
				return a.Suggested
			}
		}
		return ""
	}
	// set (or replace) the rejoin folder for an account, or add one that belongs to none
	set := func(toon, path string) {
		u.a.Update(func(c *Config) {
			out := c.RejoinFolders[:0]
			for _, f := range c.RejoinFolders {
				if sameFolder(f.Path, path) || (toon != "" && strings.EqualFold(f.Toon, toon)) {
					continue
				}
				out = append(out, f)
			}
			c.RejoinFolders = append(out, Folder{Path: path, Toon: toon})
		})
	}
	// the game makes Saves\Rejoin the first time it saves, so a folder that isn't there yet is fine if its parent is
	usable := func(p string) error {
		if p == "" {
			return errors.New("choose a folder")
		}
		for _, d := range []string{p, filepath.Dir(p), filepath.Dir(filepath.Dir(p))} {
			if s, err := os.Stat(d); err == nil && s.IsDir() {
				return nil
			}
		}
		return fmt.Errorf("can't open %s", p)
	}
	switch in.Action {
	case "use":
		p := suggestedFor(in.Toon)
		if p == "" {
			return nil, errors.New("no rejoin folder known for that account: choose one")
		}
		set(in.Toon, p)
		u.a.event("Live picks: using %s", p)
	case "useAll":
		n := 0
		for _, a := range accounts {
			if !a.Set && a.Suggested != "" {
				set(a.Toon, a.Suggested)
				n++
			}
		}
		u.a.event("Live picks: set the rejoin folder for %d account%s", n, plural(n))
	case "pick":
		p, err := pickFolder()
		if err != nil {
			return nil, err
		}
		if p == "" {
			return map[string]bool{"cancelled": true}, nil
		}
		path = p
		fallthrough
	case "add":
		path = filepath.Clean(path)
		if err := usable(path); err != nil {
			return nil, err
		}
		set(in.Toon, path)
		u.a.event("Live picks: using %s", path)
	case "toggle":
		u.a.Update(func(c *Config) {
			for i := range c.RejoinFolders {
				if c.RejoinFolders[i].Path == path {
					c.RejoinFolders[i].Off = !in.On
				}
			}
		})
	case "remove":
		u.a.Update(func(c *Config) {
			out := c.RejoinFolders[:0]
			for _, f := range c.RejoinFolders {
				if f.Path != path {
					out = append(out, f)
				}
			}
			c.RejoinFolders = out
			c.RejoinAuto = true // their choice: the main account's folder doesn't come back by itself
		})
		u.a.event("Live picks: stopped using %s", path)
	default:
		return nil, errors.New("unknown action")
	}
	return map[string]bool{"ok": true}, nil
}

func (u *ui) live(r *http.Request) (any, error) {
	var in struct{ On bool }
	if err := body(r, &in); err != nil {
		return nil, err
	}
	u.a.Update(func(c *Config) { c.NoLive = !in.On })
	return map[string]bool{"ok": true}, nil
}

func (u *ui) games(r *http.Request) (any, error) {
	var in struct{ On bool }
	if err := body(r, &in); err != nil {
		return nil, err
	}
	u.a.Update(func(c *Config) { c.NoGameNotes = !in.On })
	return map[string]bool{"ok": true}, nil
}

// update: check now, update now, or turn automatic updates on or off.
func (u *ui) update(r *http.Request) (any, error) {
	var in struct {
		Action string `json:"action"` // check | now | auto
		On     bool   `json:"on"`
	}
	if err := body(r, &in); err != nil {
		return nil, err
	}
	switch in.Action {
	case "check":
		if _, err := u.a.latest(); err != nil {
			return nil, err
		}
	case "now":
		go func() { _ = u.a.UpdateNow() }()
	case "auto":
		u.a.Update(func(c *Config) { c.NoAutoUpdate = !in.On })
		if in.On {
			go u.a.checkUpdate()
		}
	}
	return u.a.UpdateState(), nil
}

// move: switch to the site the dashboard says it moved to, or not.
func (u *ui) move(r *http.Request) (any, error) {
	var in struct{ Action string }
	if err := body(r, &in); err != nil {
		return nil, err
	}
	switch in.Action {
	case "accept":
		if err := u.a.AcceptMove(); err != nil {
			return nil, err
		}
	case "dismiss":
		u.a.DismissMove()
	default:
		return nil, errors.New("unknown action")
	}
	return u.a.UpdateState(), nil
}

func (u *ui) autostart(r *http.Request) (any, error) {
	var in struct{ On bool }
	if err := body(r, &in); err != nil {
		return nil, err
	}
	if err := setAutostartState(in.On); err != nil {
		return nil, err
	}
	return map[string]bool{"ok": true}, nil
}

// signIn: start (opens the browser on the dashboard's approval page), cancel, or sign out.
func (u *ui) signIn(r *http.Request) (any, error) {
	var in struct{ Action string }
	if err := body(r, &in); err != nil {
		return nil, err
	}
	switch in.Action {
	case "start":
		return u.a.StartSignIn()
	case "cancel":
		u.a.CancelSignIn()
	case "signout":
		u.a.SignOut()
	case "reopen":
		if st := u.a.SignInState(); st.URL != "" {
			openURL(st.URL)
		}
	default:
		return nil, errors.New("unknown action")
	}
	return u.a.SignInState(), nil
}

func (u *ui) open(r *http.Request) (any, error) {
	var in struct{ What string }
	if err := body(r, &in); err != nil {
		return nil, err
	}
	switch in.What {
	case "dashboard":
		openURL(u.a.Config().Server)
	case "accounts":
		openURL(u.a.Config().Server + "/accounts")
	case "log":
		openFile(filepath.Join(u.a.dir, "uploader.log"))
	default:
		return nil, errors.New("unknown")
	}
	return map[string]bool{"ok": true}, nil
}
