package main

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/hotsdash/uploader/updatesig"
)

// Updating itself: every few hours the uploader asks the dashboard for the newest version. Versions are the time
// of the release (2026.10.27.1730), so a bigger one is
// newer, and a release that didn't touch the uploader hands out the same version (installed copies stay put). With automatic updates on (the default) it downloads the new
// program, checks it, and hands over to it between uploads (see applyUpdate). Off, it says an update is ready and
// waits for "Update now".

// updateKey: the public keys (hex, comma-separated) an update may be signed with, set at build time
// (-X main.updateKey=…, see build.sh and the release workflow): the release key, then a backup kept apart from it, so
// a lost or leaked key can be replaced. With them, an update is only run when its signature checks out, so a dashboard
// (or anyone between it and this computer) can't hand out a program hots-dash didn't sign. A copy with a version but
// no key never updates itself; only a build from source ("dev") goes by the size and hash alone.
var updateKey = ""

// applyUpdate starts the downloaded program so it can replace this one (set by the installed copy on Windows, Linux
// and Macs; nil when run from a terminal with flags: then it just tells).
var applyUpdate func(path string) error

// platformQuery: which build to ask the dashboard for. Copies from before Linux and Mac builds ask without it, and get
// the Windows one, which is what they are.
func platformQuery() string { return "?os=" + runtime.GOOS + "&arch=" + runtime.GOARCH }

// programName: the program's file name on this system.
func programName() string {
	if runtime.GOOS == "windows" {
		return "hots-dash-uploader.exe"
	}
	return "hots-dash-uploader"
}

// userAgent: how the uploader names itself to the dashboard (its version, and which system it's on).
func userAgent() string {
	return "hots-dash-uploader/" + version + " (" + runtime.GOOS + "; " + runtime.GOARCH + ")"
}

type updater struct {
	mu        sync.Mutex
	latest    string
	checked   time.Time
	busy      bool
	err       string
	announced string // the version we've already told about
	moveTo    string // another site the dashboard says it has moved to, waiting for the person to confirm
}

// UpdateState is what the settings page shows.
type UpdateState struct {
	Version   string `json:"version"`
	Latest    string `json:"latest,omitempty"`
	Available bool   `json:"available"`
	CanApply  bool   `json:"canApply"`
	Auto      bool   `json:"auto"`
	Busy      bool   `json:"busy"`
	Error     string `json:"error,omitempty"`
	Checked   string `json:"checked,omitempty"`
	// MoveTo: the dashboard says it has moved to another site; the settings page asks before switching
	MoveTo string `json:"moveTo,omitempty"`
	// Insecure: the dashboard's address is plain http (not this computer), so it can't update itself
	Insecure bool `json:"insecure,omitempty"`
}

type latestInfo struct {
	Server  string `json:"server"` // the dashboard's own address, when it has one
	Version string `json:"version"`
	SHA256  string `json:"sha256"`
	Size    int64  `json:"size"`
	// URL: where this platform's bare program can be fetched from directly (a GitHub release file). Used only when it
	// starts with releaseURLPrefix; otherwise the dashboard's own /api/uploader/update is asked, as before.
	URL string `json:"url,omitempty"`
	// Signature: base64 Ed25519 over updatesig.Message(Version, sha256 of the program)
	Signature string `json:"signature,omitempty"`
	// SignatureV2: the same over updatesig.MessageV2, which names the platform too. Missing from older releases and
	// dashboards.
	SignatureV2 string `json:"signatureV2,omitempty"`
}

// releaseURLPrefix: the only addresses a dashboard may point the update download at (a variable only so tests can
// aim it at a local server).
var releaseURLPrefix = "https://github.com/hotsdash/uploader/releases/download/"

// updateURL: where to fetch the update from: the dashboard's url when it's a release file of this project, else
// the dashboard's own download endpoint.
func updateURL(v *latestInfo, server string) string {
	if v != nil && strings.HasPrefix(v.URL, releaseURLPrefix) && len(v.URL) > len(releaseURLPrefix) {
		if u, err := url.Parse(v.URL); err == nil && u.User == nil && u.Fragment == "" {
			return v.URL
		}
	}
	return server + "/api/uploader/update" + platformQuery()
}

var errInsecureUpdate = errors.New("this dashboard's address is plain http, so the uploader won't update itself from it: download the new version from the dashboard")

var errNoUpdateKey = errors.New("this copy has no update key: download the new version from the dashboard")

// unkeyed: a release or other versioned build with no key to check an update's signature against.
func unkeyed() bool { return version != "dev" && updateKey == "" }

// newer: is latest a newer build than have? A build from source ("dev") never updates itself.
func newer(have, latest string) bool {
	return have != "dev" && have != "" && latest != "" && latest > have
}

func (a *Agent) UpdateState() UpdateState {
	a.upd.mu.Lock()
	defer a.upd.mu.Unlock()
	secure := secureServer(a.Config().Server)
	s := UpdateState{Version: version, Latest: a.upd.latest, Available: newer(version, a.upd.latest), CanApply: applyUpdate != nil && secure && !unkeyed(),
		Auto: !a.Config().NoAutoUpdate, Busy: a.upd.busy, Error: a.upd.err, MoveTo: a.upd.moveTo, Insecure: !secure}
	if !a.upd.checked.IsZero() {
		s.Checked = a.upd.checked.Format(time.RFC3339)
	}
	return s
}

func (a *Agent) latest() (*latestInfo, error) {
	var v latestInfo
	if err := a.call("GET", "uploader/version"+platformQuery(), nil, "", &v); err != nil {
		return nil, err
	}
	a.upd.mu.Lock()
	a.upd.latest, a.upd.checked = v.Version, time.Now()
	a.upd.mu.Unlock()
	a.followMove(v.Server)
	return &v, nil
}

// followMove: the dashboard has moved to its own address (e.g. from a home server's name to a proper domain).
// Only the step from http to https on the same address switches by itself (once it answers there). Anything else,
// even another port or path on the same machine (a reverse proxy may host other apps beside it), only asks: whoever answers there would get the sign-in, the replays and future updates, so the
// person confirms it in Settings (AcceptMove). The old address keeps working until then.
func (a *Agent) followMove(server string) {
	server = strings.TrimRight(strings.TrimSpace(server), "/")
	cfg := a.Config()
	cur := cfg.Server
	if server == "" || strings.EqualFold(server, cur) || !strings.HasPrefix(server, "https://") || !webLink(server) {
		return
	}
	res, err := a.client.Get(server + "/api/uploader/version")
	if err != nil {
		return
	}
	res.Body.Close()
	if res.StatusCode != 200 {
		return
	}
	if secureUpgrade(cur, server) {
		a.moveTo(server)
		return
	}
	if strings.EqualFold(server, cfg.MoveDismissed) && time.Since(time.Unix(cfg.MoveDismissedAt, 0)) < moveSnooze {
		return // they said not now to this one, lately
	}
	a.upd.mu.Lock()
	told := a.upd.moveTo == server
	a.upd.moveTo = server
	a.upd.mu.Unlock()
	if told {
		return
	}
	a.event("The dashboard says it has moved to %s: confirm it in Settings to upload there", server)
	a.log.Printf("server says it moved: %s -> %s (waiting for confirmation)", cur, server)
	if a.Notify != nil {
		a.Notify("hots-dash: has your dashboard moved?", "It says it's now at "+server+". Open Settings to switch, if you expected this.", a.SettingsURL)
	}
}

func (a *Agent) moveTo(server string) {
	cur := a.Config().Server
	a.Update(func(c *Config) { c.Server, c.MoveDismissed, c.MoveDismissedAt = server, "", 0 })
	a.upd.mu.Lock()
	a.upd.moveTo = ""
	a.upd.mu.Unlock()
	a.ResetToons()
	a.event("The dashboard has moved to %s: uploading there from now on", server)
	a.log.Printf("server moved: %s -> %s", cur, server)
}

// AcceptMove switches to the site the dashboard said it moved to (from the settings page).
func (a *Agent) AcceptMove() error {
	a.upd.mu.Lock()
	to := a.upd.moveTo
	a.upd.mu.Unlock()
	if to == "" {
		return errors.New("no move waiting")
	}
	a.moveTo(to)
	a.SyncSoon()
	return nil
}

// moveSnooze: after "Not now", the same address is suggested again after this long (it may really have moved).
const moveSnooze = 14 * 24 * time.Hour

// DismissMove: not now. That address isn't suggested again for a while (a different one would be at once).
func (a *Agent) DismissMove() {
	a.upd.mu.Lock()
	to := a.upd.moveTo
	a.upd.moveTo = ""
	a.upd.mu.Unlock()
	if to != "" {
		a.Update(func(c *Config) { c.MoveDismissed, c.MoveDismissedAt = to, time.Now().Unix() })
		a.event("Staying on %s", a.Config().Server)
	}
}

// secureUpgrade: next is cur with http changed to https and nothing else (same machine name and path, and the
// same port or the standard ones).
func secureUpgrade(cur, next string) bool {
	a, err1 := url.Parse(cur)
	b, err2 := url.Parse(next)
	if err1 != nil || err2 != nil || a.Scheme != "http" || b.Scheme != "https" || a.Hostname() == "" ||
		!strings.EqualFold(a.Hostname(), b.Hostname()) || strings.TrimRight(a.Path, "/") != strings.TrimRight(b.Path, "/") {
		return false
	}
	pa, pb := a.Port(), b.Port()
	return pa == pb || (pa == "" || pa == "80") && (pb == "" || pb == "443")
}

// secureServer: updates only come over https, or from this computer (developing, tests).
func secureServer(server string) bool {
	p, err := url.Parse(server)
	if err != nil {
		return false
	}
	if p.Scheme == "https" {
		return true
	}
	if p.Scheme != "http" {
		return false
	}
	h := p.Hostname()
	if strings.EqualFold(h, "localhost") {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

// download fetches the new program into the app folder and checks it's exactly what the dashboard described.
func (a *Agent) download(v *latestInfo) (string, error) {
	if !secureServer(a.Config().Server) {
		return "", errInsecureUpdate
	}
	if unkeyed() {
		return "", errNoUpdateKey
	}
	d := filepath.Join(a.dir, "update")
	_ = os.RemoveAll(d)
	if err := os.MkdirAll(d, 0o755); err != nil {
		return "", err
	}
	req, err := http.NewRequest("GET", updateURL(v, a.Config().Server), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", userAgent())
	// the dashboard may answer with a redirect to the release file on GitHub: followed, but only to https on the
	// dashboard's own host or GitHub's. The size, hash and signature are checked below whoever served the bytes.
	c := *a.client
	c.CheckRedirect = updateRedirect(req.URL.Host)
	res, err := c.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return "", fmt.Errorf("download: %d", res.StatusCode)
	}
	p := filepath.Join(d, programName())
	f, err := os.OpenFile(p, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), io.LimitReader(res.Body, 200<<20))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(p)
		return "", err
	}
	if v.Size > 0 && n != v.Size {
		_ = os.Remove(p)
		return "", fmt.Errorf("download was cut short (%d of %d bytes)", n, v.Size)
	}
	sum := hex.EncodeToString(h.Sum(nil))
	if v.SHA256 == "" || !strings.EqualFold(sum, v.SHA256) {
		_ = os.Remove(p)
		return "", errors.New("the download didn't match what the dashboard said it would be")
	}
	if updateKey != "" {
		if err := verifySignature(v, sum); err != nil {
			_ = os.Remove(p)
			a.log.Printf("update refused: %v", err)
			return "", err
		}
	}
	// a dashboard from before the Linux and Mac builds answers every system with the Windows program (signed, so it
	// checks out above): never put that, or anything else that won't run here, in place of this one
	if err := runsHere(p); err != nil {
		_ = os.Remove(p)
		a.log.Printf("update refused: %v", err)
		return "", err
	}
	return p, nil
}

// verifySignature checks the program's hash was signed for this version by a trusted key: for this platform when
// there's a v2 signature (and then that one has to be good), else by the v1 signature.
func verifySignature(v *latestInfo, sum string) error {
	if v.SignatureV2 != "" {
		return updatesig.VerifyAnyV2(updateKey, v.Version, sum, updatesig.Platform(runtime.GOOS, runtime.GOARCH), v.SignatureV2)
	}
	// LATER: require SignatureV2 once installed copies have moved on (and every dashboard passes it on).
	return updatesig.VerifyAny(updateKey, v.Version, sum, v.Signature)
}

// updateRedirect: where the update download may be redirected to (at most 5 times): another https address on the
// dashboard's own host, github.com, or GitHub's release file hosts (objects.githubusercontent.com,
// release-assets.githubusercontent.com). Never plain http, except to the same host (a dashboard on this computer).
func updateRedirect(origin string) func(*http.Request, []*http.Request) error {
	return func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return errors.New("too many redirects")
		}
		h := strings.ToLower(req.URL.Hostname())
		same := strings.EqualFold(req.URL.Host, origin)
		github := h == "github.com" || strings.HasSuffix(h, ".githubusercontent.com")
		if (req.URL.Scheme == "https" && (same || github)) || (same && req.URL.Scheme == via[0].URL.Scheme) {
			return nil
		}
		return fmt.Errorf("the update download was redirected to %s, which isn't allowed", req.URL.Host)
	}
}

// runsHere checks a downloaded program is one for this system (from its first bytes): a Windows .exe, a Linux ELF
// program for this processor, or a Mac program (one for both processors, or this one's).
func runsHere(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	b := make([]byte, 20)
	if _, err := io.ReadFull(f, b); err != nil {
		return errors.New("the update isn't a program")
	}
	return programFits(b, runtime.GOOS, runtime.GOARCH)
}

// programFits: do these first bytes start a program for goos/goarch? Pure, for tests.
func programFits(b []byte, goos, goarch string) error {
	wrong := fmt.Errorf("the update isn't a program for %s/%s: the dashboard may be older than this copy", goos, goarch)
	if len(b) < 20 {
		return wrong
	}
	switch goos {
	case "windows":
		if b[0] == 'M' && b[1] == 'Z' {
			return nil
		}
	case "linux":
		// ELF, 64-bit, little-endian; e_machine at 18: 62 x86-64, 183 arm64
		machine := map[string]uint16{"amd64": 62, "arm64": 183}[goarch]
		if string(b[:4]) == "\x7fELF" && (machine == 0 || binary.LittleEndian.Uint16(b[18:]) == machine) {
			return nil
		}
	case "darwin":
		if binary.BigEndian.Uint32(b) == 0xcafebabe { // universal
			return nil
		}
		cpu := map[string]uint32{"amd64": 0x01000007, "arm64": 0x0100000c}[goarch]
		if binary.LittleEndian.Uint32(b) == 0xfeedfacf && (cpu == 0 || binary.LittleEndian.Uint32(b[4:]) == cpu) {
			return nil
		}
	default:
		return nil // no check for other systems
	}
	return wrong
}

// UpdateNow downloads the newest version and hands over to it (from the settings page or the tray).
func (a *Agent) UpdateNow() error {
	if applyUpdate == nil {
		return errors.New("this copy can't update itself: download the new version from the dashboard")
	}
	a.upd.mu.Lock()
	if a.upd.busy {
		a.upd.mu.Unlock()
		return nil
	}
	a.upd.busy, a.upd.err = true, ""
	a.upd.mu.Unlock()
	err := a.updateNow()
	a.upd.mu.Lock()
	a.upd.busy = false
	if err != nil {
		a.upd.err = err.Error()
	}
	a.upd.mu.Unlock()
	return err
}

func (a *Agent) updateNow() error {
	v, err := a.latest()
	if err != nil {
		return err
	}
	if !newer(version, v.Version) {
		return nil
	}
	// not in the middle of sending replays
	for i := 0; i < 600 && a.syncing.Load(); i++ {
		time.Sleep(time.Second)
	}
	a.log.Printf("updating from %s to %s", version, v.Version)
	p, err := a.download(v)
	if err != nil {
		a.log.Printf("update: %v", err)
		return err
	}
	a.saveState()
	if err := applyUpdate(p); err != nil {
		a.log.Printf("update: couldn't start the new version: %v", err)
		return err
	}
	return nil // the new version stops this one
}

// checkUpdate asks for the newest version; updates (automatic) or says one is ready (once per version).
func (a *Agent) checkUpdate() {
	v, err := a.latest()
	if err != nil || !newer(version, v.Version) {
		return
	}
	if !a.Config().NoAutoUpdate && applyUpdate != nil && secureServer(a.Config().Server) && !unkeyed() {
		_ = a.UpdateNow()
		return
	}
	a.upd.mu.Lock()
	told := a.upd.announced == v.Version
	a.upd.announced = v.Version
	a.upd.mu.Unlock()
	if !told {
		a.event("A new version (%s) is ready", v.Version)
		if a.OnUpdate != nil {
			a.OnUpdate(v.Version)
		}
		if a.Notify != nil {
			how := "Open Settings (or the tray menu) and choose Update now."
			if unkeyed() {
				how = "Download it from the dashboard."
			}
			a.Notify("hots-dash uploader: update ready", "Version "+v.Version+" is ready. "+how, "")
		}
	}
}

// updateLoop: a first look a minute after starting, then every 6 hours.
func (a *Agent) updateLoop(stop <-chan struct{}) {
	wait := time.Minute
	for {
		select {
		case <-stop:
			return
		case <-time.After(wait):
		}
		if wait == time.Minute {
			_ = os.RemoveAll(filepath.Join(a.dir, "update")) // left over from the last update (that copy has exited by now)
		}
		a.checkUpdate()
		wait = 6 * time.Hour
	}
}
