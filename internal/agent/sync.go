package agent

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"mime/multipart"
	"net/textproto"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/hotsdash/uploader/internal/netfix"
)

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
		f.Sent, f.Fails, f.RetryAt = true, 0, 0
		a.dirty = true
	}
	a.mu.Unlock()
}

// retryAfter: how long to wait after the first, second… time a replay isn't accepted. One that's turned down once
// more after the last wait isn't sent again.
var retryAfter = []time.Duration{time.Minute, 10 * time.Minute, time.Hour, 6 * time.Hour}

var maxFails = len(retryAfter) + 1

// markFailed notes that a replay (every copy of it) wasn't accepted. refused is the dashboard saying no; without it
// (no answer for this file) it's retried for as long as it takes. True when this was the last try.
func (a *Agent) markFailed(paths []string, refused bool) (gaveUp bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	n := 0
	for _, p := range paths {
		if f := a.files[p]; f != nil {
			n = max(n, f.Fails)
		}
	}
	n++
	if !refused {
		n = min(n, maxFails-1)
	}
	until := time.Now().Add(retryAfter[min(n, len(retryAfter))-1]).UnixMilli()
	for _, p := range paths {
		if f := a.files[p]; f != nil {
			f.Fails, f.RetryAt = n, until
			a.dirty = true
		}
	}
	return n >= maxFails
}

// heldBack: a replay that wasn't accepted and isn't due another try (or has had its last).
func (a *Agent) heldBack(paths []string, now time.Time) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, p := range paths {
		if f := a.files[p]; f != nil && (f.Fails >= maxFails || now.UnixMilli() < f.RetryAt) {
			return true
		}
	}
	return false
}

type uploadResult struct {
	FileName string `json:"fileName"`
	Status   string `json:"status"`
	Error    string `json:"error"`
	ReplayID int64  `json:"replayId"`
}

// refusedStatuses: the upload results that mean the dashboard didn't take a file. Any other status counts as
// accepted, unless the result carries an error. Only "queued" and "known" are known to be sent by the server; the
// full set is unconfirmed, so check this list against it.
var refusedStatuses = map[string]bool{"error": true, "failed": true, "rejected": true, "invalid": true}

func (r uploadResult) accepted() bool {
	return r.Error == "" && !refusedStatuses[strings.ToLower(r.Status)]
}

// uploadName: the file name a replay or rejoin save is sent under (and a replay's result comes back with). It goes
// in a header, so control characters, quotes and backslashes are dropped.
func uploadName(p string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || r == '"' || r == '\\' {
			return -1
		}
		return r
	}, filepath.Base(p))
}

// matchResults pairs the dashboard's results with the files that were sent: by file name, and where names are
// missing or shared by position, but only when there's a result for every file. A file with no result of its own
// gets nil.
func matchResults(sent []string, results []uploadResult) map[string]*uploadResult {
	files, answers := map[string]int{}, map[string]int{}
	for _, p := range sent {
		files[uploadName(p)]++
	}
	byName := map[string]int{} // file name → its result
	for i, r := range results {
		answers[r.FileName]++
		byName[r.FileName] = i
	}
	out := map[string]*uploadResult{}
	used := make([]bool, len(results))
	for _, p := range sent {
		out[p] = nil
		n := uploadName(p)
		if i, ok := byName[n]; ok && files[n] == 1 && answers[n] == 1 {
			out[p], used[i] = &results[i], true
		}
	}
	if len(results) != len(sent) {
		return out
	}
	for i, p := range sent {
		// not one that names another of the files
		if out[p] == nil && !used[i] && (results[i].FileName == uploadName(p) || files[results[i].FileName] == 0) {
			out[p] = &results[i]
		}
	}
	return out
}

// uploadBatch sends these replays. The map has an entry for each one that was sent (not those that couldn't be
// read), holding the dashboard's result for it, or nil if it gave none.
func (a *Agent) uploadBatch(paths []string) (map[string]*uploadResult, error) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	_ = w.WriteField("source", "agent")
	var sent []string
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			a.log.Printf("can't read %s: %v", filepath.Base(p), err)
			continue
		}
		sent = append(sent, p)
		st, _ := os.Stat(p)
		h := make(textproto.MIMEHeader)
		h.Set("Content-Disposition", fmt.Sprintf(`form-data; name="files"; filename="%s"`, uploadName(p)))
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
	if len(sent) == 0 {
		return nil, nil
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
	return matchResults(sent, out.Results), nil
}

// pruneState forgets files that have been deleted: in a folder that was scanned, not seen, and not there any more.
// Nothing in a folder that was missing is touched (an unplugged backup drive comes back as it was).
func (a *Agent) pruneState(scanned, missing, found []string) {
	seen := map[string]bool{}
	for _, p := range found {
		seen[p] = true
	}
	inAny := func(p string, dirs []string) bool {
		for _, d := range dirs {
			if rel, err := filepath.Rel(d, p); err == nil && filepath.IsLocal(rel) {
				return true
			}
		}
		return false
	}
	var unseen []string
	a.mu.Lock()
	for p := range a.files {
		if !seen[p] && inAny(p, scanned) && !inAny(p, missing) {
			unseen = append(unseen, p)
		}
	}
	a.mu.Unlock()
	for _, p := range unseen {
		if _, err := os.Stat(p); !errors.Is(err, fs.ErrNotExist) {
			continue // still there, or can't tell
		}
		a.forgetSeen(p)
		a.mu.Lock()
		delete(a.files, p)
		delete(a.tooBig, p)
		a.dirty = true
		a.mu.Unlock()
	}
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
	var found []string            // every replay listed, readable or not
	var scanned, missing []string // folders that were there this time, and those that weren't
	for _, dir := range folders {
		fs := &FolderStats{}
		stats.Folders[dir] = fs
		if st, err := os.Stat(dir); err != nil || !st.IsDir() {
			missing = append(missing, dir)
			continue
		}
		fs.Exists = true
		scanned = append(scanned, dir)
		for _, p := range walkReplays(dir) {
			found = append(found, p)
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
	a.pruneState(scanned, missing, found)
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
		} else if !a.heldBack(byHash[h], scanAt) {
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

	accepted, added := 0, 0
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
		for j, h := range batch {
			p := paths[j]
			r, sent := res[p]
			switch {
			case !sent: // couldn't be read: the next scan looks again
			case r == nil:
				a.log.Printf("no result from the dashboard for %s: trying again later", filepath.Base(p))
				a.markFailed(byHash[h], false)
			case !r.accepted():
				why := r.Error
				if why == "" {
					why = r.Status
				}
				a.log.Printf("%s wasn't accepted: %s", filepath.Base(p), why)
				if a.markFailed(byHash[h], true) {
					a.event("%s was turned down by the dashboard: %s", filepath.Base(p), why)
				}
			default:
				for _, same := range byHash[h] {
					a.markSent(same)
				}
				accepted++
				if r.Status == "queued" {
					added++
					// a game that just finished: tell them how it went once the server has read it
					if r.ReplayID > 0 {
						if st, err := os.Stat(p); err == nil && time.Since(st.ModTime()) < 20*time.Minute {
							go a.followGame(r.ReplayID)
						}
					}
				}
			}
		}
		if i%100 == 0 {
			a.saveState()
		}
	}
	if accepted > 0 {
		a.event("Uploaded %d replay%s (%d new to the dashboard)", accepted, plural(accepted), added)
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

func (a *Agent) problem(err error) {
	text := "Can't reach the dashboard"
	if errors.Is(err, ErrToken) {
		text = "Sign-in not accepted: sign in again in Settings"
	} else if errors.Is(err, netfix.ErrBlocked) {
		text = "Your network is blocking the dashboard: see Recent activity"
	}
	if prev := a.Status(); !prev.Problem || prev.Text != text {
		a.event("Problem: %v", err)
	}
	a.setStatus(func(s *Status) { s.Text = text; s.Problem = true })
}
