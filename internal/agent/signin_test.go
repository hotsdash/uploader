package agent

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestNoSettingsMeansTheDefaultServerAndAFirstRunSignIn(t *testing.T) {
	c := ResolveConfig(t.TempDir())
	if c.Server != defaultServer || !c.AskSignIn || c.Token != "" {
		t.Fatalf("%+v", c)
	}
	dir := t.TempDir()
	SaveConfig(dir, Config{Server: "https://self.example", Token: "hdu_x"})
	if c := ResolveConfig(dir); c.Server != "https://self.example" || c.AskSignIn {
		t.Fatalf("a saved config is kept as it is: %+v", c)
	}
}

func TestFirstRunStartsSignInOnceWithTheCodeInTheAddress(t *testing.T) {
	asked := make(chan string, 4)
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/agent/link":
			asked <- r.URL.Path
			_ = json.NewEncoder(w).Encode(linkStart{DeviceCode: "dev", UserCode: "ABCD-EFGH", VerifyURL: srv.URL + "/link?code=ABCD-EFGH", Interval: 60, ExpiresIn: 600})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	dir := t.TempDir()
	a := NewAgent(Config{Server: srv.URL, AskSignIn: true, Folders: []Folder{{Path: dir}}}, dir, log.New(io.Discard, "", 0))
	defer a.CancelSignIn()
	a.autoSignIn() // not for tests: AutoSignIn is off
	select {
	case <-asked:
		t.Fatal("signed in without being asked to")
	case <-time.After(100 * time.Millisecond):
	}
	a.AutoSignIn = true
	a.autoSignIn()
	select {
	case <-asked:
	case <-time.After(3 * time.Second):
		t.Fatal("the first run didn't start signing in")
	}
	if a.Config().AskSignIn {
		t.Fatal("asked again next time")
	}
	for i := 0; i < 50 && !a.SignInState().Pending; i++ {
		time.Sleep(20 * time.Millisecond)
	}
	if u := a.SignInState().URL; !strings.HasSuffix(u, "/link?code=ABCD-EFGH") {
		t.Fatalf("approval page %q", u)
	}
	for i := 0; i < 100 && len(openedURLs()) == 0; i++ { // opened just after it's marked pending
		time.Sleep(20 * time.Millisecond)
	}
	got := openedURLs()
	if len(got) == 0 || got[len(got)-1] != srv.URL+"/link?code=ABCD-EFGH" {
		t.Fatalf("the browser was asked to open %v, want the verify URL with the code", got)
	}
	a.autoSignIn()
	select {
	case <-asked:
		t.Fatal("started twice")
	case <-time.After(100 * time.Millisecond):
	}
}

func TestSignInRefusesAnApprovalPageOnAnotherSite(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(linkStart{DeviceCode: "d", UserCode: "ABCD-EFGH", VerifyURL: `\\evil.example\share\x.exe`, Interval: 2, ExpiresIn: 60})
	}))
	defer srv.Close()
	dir := t.TempDir()
	a := NewAgent(Config{Server: srv.URL, Folders: []Folder{{Path: dir}}}, dir, log.New(io.Discard, "", 0))
	_, err := a.StartSignIn()
	if err == nil || !strings.Contains(err.Error(), "another site") {
		t.Fatalf("want a refusal, got %v", err)
	}
	if a.SignInState().Pending {
		t.Fatal("nothing should be waiting for approval")
	}
}
