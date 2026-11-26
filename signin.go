package main

import (
	"errors"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/hotsdash/uploader/internal/links"
)

// Signing in, like a TV app: the uploader asks the dashboard for a code, opens the browser on the dashboard's
// approval page, and asks every few seconds until someone signed in there approves it. Then it gets that
// person's upload token and keeps it in config.json. Not signed in, it still uploads (anonymously).

type signIn struct {
	mu       sync.Mutex
	pending  bool
	userCode string
	url      string
	err      string
	cancel   chan struct{}
}

// SignInState is what the settings page shows.
type SignInState struct {
	Pending  bool   `json:"pending"`
	UserCode string `json:"userCode,omitempty"`
	URL      string `json:"url,omitempty"`
	Error    string `json:"error,omitempty"`
}

func (a *Agent) SignInState() SignInState {
	s := &a.signin
	s.mu.Lock()
	defer s.mu.Unlock()
	return SignInState{Pending: s.pending, UserCode: s.userCode, URL: s.url, Error: s.err}
}

type linkStart struct {
	DeviceCode string `json:"deviceCode"`
	UserCode   string `json:"userCode"`
	VerifyURL  string `json:"verifyUrl"`
	Interval   int    `json:"interval"`
	ExpiresIn  int    `json:"expiresIn"`
}

type linkPoll struct {
	Status string `json:"status"` // pending | approved | denied | expired
	Token  string `json:"token"`
	Name   string `json:"name"`
}

// StartSignIn asks the dashboard for a code and opens the approval page in the browser.
func (a *Agent) StartSignIn() (SignInState, error) {
	a.CancelSignIn()
	var toons []map[string]any
	scan := a.Scan()
	for t, n := range scan.Toons {
		toons = append(toons, map[string]any{"toon": t, "replays": n})
	}
	sort.Slice(toons, func(i, j int) bool { return toons[i]["replays"].(int) > toons[j]["replays"].(int) })
	host, _ := os.Hostname()
	var st linkStart
	// asked without a token: signing in is how you get one (and an old, refused token mustn't get in the way)
	if err := a.callAnon("agent/link", map[string]any{"computer": host, "toons": toons}, &st); err != nil {
		return a.SignInState(), err
	}
	if st.DeviceCode == "" || st.VerifyURL == "" {
		return a.SignInState(), errors.New("this dashboard doesn't support signing in from the uploader yet")
	}
	// the approval page has to be on the dashboard itself: anything else isn't opened (see links.go)
	if !links.OnSite(st.VerifyURL, a.Config().Server) {
		a.log.Printf("sign-in: refused approval page %q (not on %s)", st.VerifyURL, a.Config().Server)
		return a.SignInState(), errors.New("the dashboard sent a sign-in page on another site, so it wasn't opened")
	}
	s := &a.signin
	s.mu.Lock()
	s.pending, s.userCode, s.url, s.err = true, st.UserCode, st.VerifyURL, ""
	s.cancel = make(chan struct{})
	cancel := s.cancel
	s.mu.Unlock()
	a.event("Signing in: approve code %s in the browser", st.UserCode)
	links.OpenURL(st.VerifyURL)
	go a.pollSignIn(st, cancel)
	return a.SignInState(), nil
}

func (a *Agent) pollSignIn(st linkStart, cancel chan struct{}) {
	interval := time.Duration(max(st.Interval, 2)) * time.Second
	deadline := time.Now().Add(time.Duration(max(st.ExpiresIn, 60)) * time.Second)
	finish := func(errText string) {
		s := &a.signin
		s.mu.Lock()
		if s.cancel == cancel {
			s.pending, s.userCode, s.url, s.err, s.cancel = false, "", "", errText, nil
		}
		s.mu.Unlock()
	}
	for time.Now().Before(deadline) {
		select {
		case <-cancel:
			return
		case <-time.After(interval):
		}
		var r linkPoll
		if err := a.callAnon("agent/link/poll", map[string]any{"deviceCode": st.DeviceCode}, &r); err != nil {
			continue // the dashboard may be restarting: keep asking until the code runs out
		}
		switch r.Status {
		case "pending":
			continue
		case "approved":
			a.Update(func(c *Config) { c.Token, c.Name = r.Token, r.Name })
			finish("")
			who := r.Name
			if who == "" {
				who = "you"
			}
			a.event("Signed in as %s: replays now count as theirs", who)
			if a.Notify != nil {
				a.Notify("hots-dash uploader signed in", "Uploading as "+who+".", a.Config().Server+"/accounts")
			}
			a.ResetToons()
			a.SyncSoon()
			return
		case "denied":
			finish("Sign-in was cancelled in the browser.")
			return
		default:
			finish("The code ran out: press Sign in again.")
			return
		}
	}
	finish("The code ran out: press Sign in again.")
}

// CancelSignIn stops waiting for an approval.
func (a *Agent) CancelSignIn() {
	s := &a.signin
	s.mu.Lock()
	if s.cancel != nil {
		close(s.cancel)
		s.cancel = nil
	}
	s.pending, s.userCode, s.url, s.err = false, "", "", ""
	s.mu.Unlock()
}

// SignOut forgets the token: the uploader carries on uploading, anonymously.
func (a *Agent) SignOut() {
	a.CancelSignIn()
	a.Update(func(c *Config) { c.Token, c.Name = "", "" })
	a.event("Signed out: replays are uploaded without an account")
	a.ResetToons()
	a.setStatus(func(s *Status) {
		if s.Problem {
			s.Problem, s.Text = false, "Signed out"
		}
	})
	a.SyncSoon()
}

// enrol swaps the one-time code a fresh download carries for the person's upload token. The download itself never
// holds the token, so the copy left in Downloads (or one shared) can't upload as them. Tried before each sync until
// it works or the dashboard says the code is used up or ran out.
func (a *Agent) enrol() {
	code := a.Config().Enrol
	if code == "" {
		return
	}
	var r linkPoll
	err := a.callAnon("agent/link/enrol", map[string]any{"code": code}, &r)
	if err != nil && !errors.Is(err, errNotFound) {
		a.log.Printf("sign-in from the download: %v (trying again later)", err) // the dashboard may be restarting
		return
	}
	if err == nil && r.Status == "approved" && r.Token != "" {
		a.Update(func(c *Config) { c.Token, c.Name, c.Enrol = r.Token, r.Name, "" })
		who := r.Name
		if who == "" {
			who = "you"
		}
		a.event("Signed in as %s: replays now count as theirs", who)
		a.ResetToons()
		return
	}
	a.Update(func(c *Config) { c.Enrol = "" })
	if a.Config().Token == "" {
		a.event("This download's sign-in was already used or ran out: sign in from Settings to have your replays count as yours")
	}
}

// autoSignIn: the download is the same file for everyone, so a first run that isn't signed in starts the sign-in
// itself: the browser opens on the dashboard's approval page (with the code in the address) and the person clicks
// Approve where they're already logged in. Once only: after that it's the Sign in button in Settings.
func (a *Agent) autoSignIn() {
	c := a.Config()
	if !a.AutoSignIn || !c.AskSignIn || c.Token != "" || c.Enrol != "" {
		return
	}
	a.Update(func(c *Config) { c.AskSignIn = false })
	go func() {
		if _, err := a.StartSignIn(); err != nil {
			a.log.Printf("sign-in on first run: %v (press Sign in in Settings)", err)
		}
	}()
}
