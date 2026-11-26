package agent

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

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

// maxResponseBytes: far more than any of the dashboard's answers; a bigger one is cut off and fails to decode.
const maxResponseBytes = 8 << 20

// apiRedirect: where an API call, which carries the token, may be redirected (at most 5 times): the same address,
// or from http to https on it. Go's own rule keeps the token for the same machine name even over plain http.
func apiRedirect(req *http.Request, via []*http.Request) error {
	if len(via) > 5 {
		return errors.New("too many redirects")
	}
	from, to := via[len(via)-1].URL, req.URL
	port := func(u *url.URL) string {
		switch {
		case u.Port() != "":
			return u.Port()
		case u.Scheme == "https":
			return "443"
		}
		return "80"
	}
	if strings.EqualFold(from.Hostname(), to.Hostname()) {
		if to.Scheme == from.Scheme && port(to) == port(from) {
			return nil
		}
		if from.Scheme == "http" && to.Scheme == "https" && (port(to) == port(from) || port(from) == "80" && port(to) == "443") {
			return nil
		}
	}
	return fmt.Errorf("the dashboard redirected to %s://%s, which isn't allowed", to.Scheme, to.Host)
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
	return json.NewDecoder(io.LimitReader(res.Body, maxResponseBytes)).Decode(out)
}

func (a *Agent) postJSON(path string, in, out any) error {
	b, _ := json.Marshal(in)
	return a.call("POST", path, bytes.NewReader(b), "application/json", out)
}
