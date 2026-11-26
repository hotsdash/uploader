package agent

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"testing"
)

func enrolAgent(t *testing.T, server string) *Agent {
	dir := t.TempDir()
	return NewAgent(Config{Server: server, Enrol: "hde_code", Folders: []Folder{{Path: dir}}}, dir, log.New(io.Discard, "", 0))
}

func TestEnrolSwapsTheCodeForTheToken(t *testing.T) {
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		var in struct{ Code string }
		_ = json.NewDecoder(r.Body).Decode(&in)
		if r.URL.Path != "/api/agent/link/enrol" || in.Code != "hde_code" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(linkPoll{Status: "approved", Token: "hdu_tok", Name: "Alex"})
	}))
	defer srv.Close()
	a := enrolAgent(t, srv.URL)
	a.enrol()
	c := a.Config()
	if c.Token != "hdu_tok" || c.Name != "Alex" || c.Enrol != "" {
		t.Fatalf("config %+v", c)
	}
	if auth != "" {
		t.Fatal("the swap is asked without a token")
	}
}

func TestEnrolUsedUpOrRunOut(t *testing.T) {
	for name, h := range map[string]http.HandlerFunc{
		"expired": func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewEncoder(w).Encode(linkPoll{Status: "expired"})
		},
		"old server": func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) },
	} {
		srv := httptest.NewServer(h)
		a := enrolAgent(t, srv.URL)
		a.enrol()
		if c := a.Config(); c.Enrol != "" || c.Token != "" {
			t.Errorf("%s: config %+v", name, c)
		}
		srv.Close()
	}
}

func TestEnrolKeepsTheCodeWhileTheDashboardIsDown(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "restarting", 502) }))
	defer srv.Close()
	a := enrolAgent(t, srv.URL)
	a.enrol()
	if a.Config().Enrol != "hde_code" {
		t.Fatal("the code should be kept to try again")
	}
}
