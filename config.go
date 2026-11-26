package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// The dashboard appends the person's settings to the .exe it hands out:
//
//	[program][settings JSON][4-byte little-endian length]["HDCFG001"]
//
// Windows ignores anything after the program, so the download just works, set up for whoever downloaded it.
//
// The Linux and Mac downloads are a .tar.gz instead (a browser drops the "can run" bit of a bare program, and adding
// bytes to a Mac program breaks its signature), so the same settings JSON comes as setupFile, next to the program.
//
// The downloads no longer carry settings (every copy is the same file, so it can be recognised and trusted); this still
// reads them so copies downloaded before keep working. Without any, the program points at defaultServer and signs in
// from the browser on first run (signin.go autoSignIn).
const trailerMagic = "HDCFG001"

// defaultServer: where a copy with no settings uploads. Self-hosters start it with -server.
const defaultServer = "https://hotsdash.com"

// setupFile: the settings that come with a Linux or Mac download, beside the program.
const setupFile = "hots-dash-setup.json"

func embeddedConfig() (*Config, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	if b, err := trailerSettings(exe); err == nil {
		return parseSettings(b)
	}
	b, err := os.ReadFile(filepath.Join(filepath.Dir(exe), setupFile))
	if err != nil || len(b) > 64*1024 {
		return nil, errors.New("no settings")
	}
	return parseSettings(b)
}

// trailerSettings reads the settings JSON appended to the program at exe (the Windows download).
func trailerSettings(exe string) ([]byte, error) {
	f, err := os.Open(exe)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || st.Size() < 12 {
		return nil, errors.New("no settings")
	}
	tail := make([]byte, 12)
	if _, err := f.ReadAt(tail, st.Size()-12); err != nil {
		return nil, err
	}
	if !bytes.Equal(tail[4:], []byte(trailerMagic)) {
		return nil, errors.New("no settings")
	}
	n := int64(binary.LittleEndian.Uint32(tail[:4]))
	if n <= 0 || n > 64*1024 || n > st.Size()-12 {
		return nil, errors.New("bad settings")
	}
	b := make([]byte, n)
	if _, err := f.ReadAt(b, st.Size()-12-n); err != nil {
		return nil, err
	}
	return b, nil
}

// parseSettings: the download's settings, marked with a hash of them (so the same download starting again is known).
func parseSettings(b []byte) (*Config, error) {
	var c Config
	if err := json.Unmarshal(b, &c); err != nil || c.Server == "" {
		return nil, errors.New("bad settings")
	}
	sum := sha256.Sum256(b)
	c.Embedded = hex.EncodeToString(sum[:8])
	return &c, nil
}

// appDir is where config, state and the log live: %APPDATA%\hots-dash on Windows, ~/.config/hots-dash on Linux,
// ~/Library/Application Support/hots-dash on a Mac. On Linux and Macs only you can read it.
func appDir() string {
	base, err := os.UserConfigDir()
	if err != nil {
		base, _ = os.UserHomeDir()
	}
	d := filepath.Join(base, "hots-dash")
	_ = os.MkdirAll(d, 0o700)
	makePrivate(d, 0o700) // one made by an earlier version
	return d
}

// makePrivate takes other users' access away from p, as far as it can. Not on Windows, where %APPDATA% is already
// the user's own.
func makePrivate(p string, mode os.FileMode) {
	if runtime.GOOS != "windows" {
		_ = os.Chmod(p, mode)
	}
}

func loadSaved(dir string) *Config {
	b, err := os.ReadFile(filepath.Join(dir, "config.json"))
	if err != nil {
		return nil
	}
	var c Config
	if json.Unmarshal(b, &c) != nil || c.Server == "" {
		return nil
	}
	return &c
}

func saveConfig(dir string, c Config) {
	b, _ := json.MarshalIndent(c, "", "  ")
	_ = os.WriteFile(filepath.Join(dir, "config.json"), b, 0o600)
}

// resolveConfig: settings in a fresh download win (new settings), but keep any folder overrides the person set by
// hand in config.json. The same download starting again (every Windows start) keeps what was changed since, such
// as signing in or out from the settings page. With neither, it's defaultServer and a first-run sign-in.
func resolveConfig(dir string) Config {
	saved := loadSaved(dir)
	emb, _ := embeddedConfig()
	if emb == nil {
		if saved == nil {
			return Config{Server: defaultServer, AskSignIn: true}
		}
		return *saved
	}
	if saved != nil && saved.Embedded == emb.Embedded {
		return *saved
	}
	if saved != nil {
		// a fresh download brings the server address and key; your folders and choices stay
		if len(emb.Folders) == 0 {
			emb.Folders = saved.Folders
		}
		if emb.Replays == "" {
			emb.Replays = saved.Replays
		}
		if emb.Lobby == "" {
			emb.Lobby = saved.Lobby
		}
		if len(emb.RejoinFolders) == 0 {
			emb.RejoinFolders = saved.RejoinFolders
		}
		emb.RejoinPrompted = emb.RejoinPrompted || saved.RejoinPrompted
		emb.RejoinAuto = emb.RejoinAuto || saved.RejoinAuto
		emb.NoLive = emb.NoLive || saved.NoLive
		emb.NoGameNotes = emb.NoGameNotes || saved.NoGameNotes
		emb.NoAutoUpdate = emb.NoAutoUpdate || saved.NoAutoUpdate
		if emb.MoveDismissed == "" {
			emb.MoveDismissed, emb.MoveDismissedAt = saved.MoveDismissed, saved.MoveDismissedAt
		}
		// a copy downloaded without signing in doesn't sign you out of one you signed in from the settings page
		if emb.Token == "" && saved.Token != "" && strings.TrimRight(emb.Server, "/") == strings.TrimRight(saved.Server, "/") {
			emb.Token, emb.Name = saved.Token, saved.Name
		}
	}
	saveConfig(dir, *emb)
	return *emb
}

// newLogger writes to uploader.log in dir (starting afresh once it passes 1 MB), and to extra if given.
func newLogger(dir string, extra io.Writer) *log.Logger {
	p := filepath.Join(dir, "uploader.log")
	if st, err := os.Stat(p); err == nil && st.Size() > 1<<20 {
		_ = os.Rename(p, p+".old")
	}
	var w io.Writer = io.Discard
	if f, err := os.OpenFile(p, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600); err == nil {
		w = f
	}
	makePrivate(p, 0o600)
	makePrivate(p+".old", 0o600)
	if extra != nil {
		w = io.MultiWriter(w, extra)
	}
	return log.New(w, "", log.LstdFlags)
}
