package agent

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hotsdash/uploader/internal/netfix"
)

// The workaround is for hotsdash.com only, and only while it's switched on: the agent's transport asks it.
func TestWorkaroundOnlyForTheDefaultDashboard(t *testing.T) {
	a := NewAgent(Config{Server: "https://hotsdash.com", Folders: []Folder{{Path: t.TempDir()}}}, t.TempDir(), log.New(io.Discard, "", 0))
	f := a.client.Transport.(*netfix.Transport)
	for _, host := range []string{"example.com", "dash.hotsdash.com", "hotsdash.com.example.com", "github.com"} {
		if f.Allow(host) {
			t.Errorf("%s: allowed", host)
		}
	}
	if !f.Allow("hotsdash.com") {
		t.Fatal("the default dashboard isn't allowed")
	}
	a.Update(func(c *Config) { c.NoNetworkWorkaround = true })
	if f.Allow("hotsdash.com") {
		t.Fatal("allowed while switched off")
	}
}

func TestWorkaroundSetting(t *testing.T) {
	// settings saved before there was a choice keep it on
	var old Config
	if err := json.Unmarshal([]byte(`{"server":"https://hotsdash.com","token":"t"}`), &old); err != nil || old.NoNetworkWorkaround {
		t.Fatalf("err %v, %+v", err, old)
	}
	if b, _ := json.Marshal(old); strings.Contains(string(b), "noNetworkWorkaround") {
		t.Fatalf("saved when on: %s", b)
	}

	a := NewAgent(old, t.TempDir(), log.New(io.Discard, "", 0))
	u := &ui{a: a}
	for _, on := range []bool{false, true} {
		body, _ := json.Marshal(map[string]bool{"on": on})
		if _, err := u.network(httptest.NewRequest(http.MethodPost, "/api/network", strings.NewReader(string(body)))); err != nil {
			t.Fatal(err)
		}
		if a.Config().NoNetworkWorkaround == on || a.goAround("hotsdash.com") != on || a.goAround("HotsDash.com") != on {
			t.Fatalf("on=%v: %+v", on, a.Config())
		}
	}
}

func TestProblemTextBlocked(t *testing.T) {
	dir := t.TempDir()
	a := NewAgent(Config{Server: "https://hotsdash.com", Folders: []Folder{{Path: dir}}}, dir, log.New(io.Discard, "", 0))
	a.problem(fmt.Errorf("%w: %w", netfix.ErrBlocked, tls.RecordHeaderError{Msg: "first record does not look like a TLS handshake"}))
	if !strings.Contains(a.Status().Text, "network is blocking") {
		t.Fatalf("status %q", a.Status().Text)
	}
}
