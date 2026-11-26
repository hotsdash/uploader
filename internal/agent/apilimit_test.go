package agent

import (
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAPIResponsesAreLimited(t *testing.T) {
	var size int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"text":"`+strings.Repeat("a", size)+`"}`)
	}))
	defer srv.Close()
	a := NewAgent(Config{Server: srv.URL, Folders: []Folder{{Path: t.TempDir()}}}, t.TempDir(), log.New(io.Discard, "", 0))
	var out struct{ Text string }
	size = 1 << 20
	if err := a.call("GET", "x", nil, "", &out); err != nil || len(out.Text) != size {
		t.Fatalf("an ordinary answer: %v, %d bytes", err, len(out.Text))
	}
	out.Text, size = "", maxResponseBytes
	if err := a.call("GET", "x", nil, "", &out); err == nil || out.Text != "" {
		t.Fatalf("an answer over the limit: err %v, %d bytes", err, len(out.Text))
	}
}
