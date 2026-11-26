//go:build !windows

package main

// Linux and Macs. Run from a download, the program installs itself (installPath), sets itself to start when you log
// in, starts in the background and opens its settings page, like the Windows one does. Run again (the installed copy,
// from a terminal or a launcher), it opens the settings page of the copy that's running. The service itself runs it
// with --service. Flags such as --server or --once run it in the terminal instead, which is also how it's tested.

import (
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/hotsdash/uploader/internal/links"
)

func main() {
	server := flag.String("server", "", "hots-dash address, e.g. https://hotsdash.com (runs in this terminal)")
	token := flag.String("token", "", "upload token, with -server (other programs on this computer can see it: use "+tokenEnv+" instead)")
	replays := flag.String("replays", "", "replay folders, comma-separated (default: found automatically)")
	ui := flag.Bool("ui", false, "serve the settings page and print its address")
	lobby := flag.String("lobby", "", "where the game writes the lobby file")
	data := flag.String("data", "", "where to keep settings and state (default: your config folder)/hots-dash")
	once := flag.Bool("once", false, "upload what's there, then exit")
	foreground := flag.Bool("foreground", false, "run in this terminal instead of in the background")
	service := flag.Bool("service", false, "run as the background service (what systemd or launchd start)")
	updated := flag.Bool("updated", false, "")
	uninstall := flag.Bool("uninstall", false, "stop it, stop starting it at log-in, and remove the installed program (settings stay)")
	showVersion := flag.Bool("version", false, "print the version")
	flag.Parse()

	if *showVersion {
		fmt.Println("hots-dash uploader", version)
		return
	}
	*token = uploadToken(*token, os.Stderr)
	terminal := *foreground
	flag.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "server", "token", "replays", "ui", "lobby", "data", "once":
			terminal = true
		}
	})
	dir := *data
	if dir == "" {
		dir = appDir()
	}
	_ = os.MkdirAll(dir, 0o700) // a -data folder that already exists is left as it is

	switch {
	case *uninstall:
		uninstallIt(dir)
	case *service:
		runService(dir, *updated)
	case terminal:
		runTerminal(dir, *server, *token, *replays, *lobby, *ui, *once)
	default:
		installOrOpen(dir)
	}
}

// tokenEnv: the upload token can come from the environment, which other users of the computer can't read.
const tokenEnv = "HOTSDASH_TOKEN"

// uploadToken is the token for -server: --token if given (with a warning, as command lines show in the process list),
// else tokenEnv. It's removed from the environment so the programs started from here (the browser) don't get it.
func uploadToken(fromFlag string, warn io.Writer) string {
	fromEnv := os.Getenv(tokenEnv)
	_ = os.Unsetenv(tokenEnv)
	if fromFlag == "" {
		return fromEnv
	}
	fmt.Fprintf(warn, "Warning: other programs on this computer can see a token given with --token. Set %s instead.\n", tokenEnv)
	return fromFlag
}

// runTerminal: the uploader in this terminal, until Ctrl+C.
func runTerminal(dir, server, token, replays, lobby string, ui, once bool) {
	cfg := resolveConfig(dir)
	if server != "" {
		cfg = Config{Server: server, Token: token}
	}
	if replays != "" {
		cfg.Folders = nil
		for _, p := range strings.Split(replays, ",") {
			if p = strings.TrimSpace(p); p != "" {
				cfg.Folders = append(cfg.Folders, Folder{Path: p})
			}
		}
	}
	if lobby != "" {
		cfg.Lobby = lobby
	}
	logger := newLogger(dir, os.Stdout)
	a := NewAgent(cfg, dir, logger)
	a.OnConfigChange(func(c Config) { saveConfig(dir, c) })
	a.Notify = func(title, msg, _ string) { logger.Printf("NOTIFY %s\n%s", title, msg) }
	if once {
		if err := a.Sync(); err != nil {
			logger.Fatal(err)
		}
		logger.Println(a.Status().Text)
		return
	}
	if ui {
		u, err := StartUI(a, takeHandoff(dir))
		if err != nil {
			logger.Fatal(err)
		}
		a.SettingsURL = u.URL()
		logger.Printf("Settings: %s", u.URL())
	}
	a.Run(stopOnSignal())
}

// stopOnSignal closes the channel on Ctrl+C or when the service manager stops it (SIGTERM), so it exits cleanly.
func stopOnSignal() chan struct{} {
	stop := make(chan struct{})
	go func() {
		c := make(chan os.Signal, 1)
		signal.Notify(c, os.Interrupt, syscall.SIGTERM)
		<-c
		close(stop)
	}()
	return stop
}

// installOrOpen: run from a download, install and start; run as the installed copy, open the settings page.
func installOrOpen(dir string) {
	cfg := resolveConfig(dir)
	exe, _ := os.Executable()
	target := installPath()
	if !samePath(exe, target) {
		_, statErr := os.Stat(target)
		fresh := os.IsNotExist(statErr)
		wasAuto := !fresh && autostartEnabled()
		if stopRunning(dir) {
			fmt.Println("Stopped the copy that was running.")
		}
		if err := copyFile(exe, target); err != nil {
			fmt.Fprintf(os.Stderr, "Couldn't install the uploader to %s: %v\n", target, err)
			os.Exit(1)
		}
		atLogin := fresh || wasAuto
		if err := writeService(target, atLogin); err != nil {
			fmt.Fprintf(os.Stderr, "Couldn't set up the background service: %v\n", err)
		}
		if atLogin {
			if err := setAutostart(true); err != nil {
				fmt.Fprintf(os.Stderr, "Couldn't set it to start when you log in: %v\n", err)
			}
		}
		fmt.Printf("Installed the hots-dash uploader (version %s) to %s\n", version, target)
		who := ""
		if cfg.Name != "" {
			who = " as " + cfg.Name
		}
		if !start(dir, target) {
			return
		}
		fmt.Printf("It's running in the background, uploading your replays%s, and starts when you log in.\n", who)
		fmt.Printf("To open its settings again, run:\n  %s\n", target)
		fmt.Printf("To remove it:\n  %s --uninstall\n", target)
		return
	}
	// the installed copy, run by hand
	if running(dir) {
		if u := settingsURL(dir, 5*time.Second); u != "" {
			fmt.Println("The uploader is running. Settings:", u)
			links.OpenURL(u)
			return
		}
		fmt.Println("The uploader is running, but its settings page isn't answering. Its log is", filepath.Join(dir, "uploader.log"))
		return
	}
	start(dir, target)
}

// start starts the installed copy in the background and opens its settings page once it's up.
func start(dir, target string) bool {
	_ = os.Remove(filepath.Join(dir, "settings-url.txt"))
	if err := startService(target); err != nil {
		fmt.Fprintf(os.Stderr, "Couldn't start it in the background (%v).\nRun it in this terminal instead:\n  %s --foreground\n", err, target)
		return false
	}
	if u := settingsURL(dir, 15*time.Second); u != "" {
		fmt.Println("Settings (opening in your browser):", u)
		links.OpenURL(u)
	} else {
		fmt.Println("Started, but its settings page didn't come up. Its log is", filepath.Join(dir, "uploader.log"))
	}
	return true
}

// settingsURL waits up to wait for the running copy's settings page address.
func settingsURL(dir string, wait time.Duration) string {
	for end := time.Now().Add(wait); ; time.Sleep(200 * time.Millisecond) {
		if b, err := os.ReadFile(filepath.Join(dir, "settings-url.txt")); err == nil {
			if u := strings.TrimSpace(string(b)); links.WebLink(u) {
				return u
			}
		}
		if time.Now().After(end) {
			return ""
		}
	}
}

// runService: the background copy (started by systemd, launchd or the autostart entry).
func runService(dir string, updated bool) {
	logger := newLogger(dir, nil)
	cfg := resolveConfig(dir)
	if !takeLock(dir) {
		logger.Printf("already running")
		return
	}
	a := NewAgent(cfg, dir, logger)
	a.AutoSignIn = true
	a.OnConfigChange(func(c Config) { saveConfig(dir, c) })
	saveConfig(dir, a.Config()) // records the folders found on first run
	a.Notify = notify
	var prev *uiHandoff
	if updated {
		prev = takeHandoff(dir) // the settings page's address from the copy this one replaced
	}
	settings := ""
	page, err := StartUI(a, prev)
	if err != nil {
		logger.Printf("settings page: %v", err)
	} else {
		settings = page.URL()
		a.SettingsURL = settings
		_ = os.WriteFile(filepath.Join(dir, "settings-url.txt"), []byte(settings), 0o600)
		defer os.Remove(filepath.Join(dir, "settings-url.txt"))
	}
	// updating itself: put the new program where this one is, note where the settings page is (so the new copy serves
	// it there and an open page carries on), then become the new program. Same process, so the service manager
	// doesn't notice.
	applyUpdate = func(p string) error {
		self, err := os.Executable()
		if err != nil {
			return err
		}
		if s, err := filepath.EvalSymlinks(self); err == nil {
			self = s
		}
		if err := copyFile(p, self); err != nil {
			return err
		}
		if page != nil {
			if err := writeHandoff(dir, page.Handoff()); err != nil {
				logger.Printf("update: settings page handoff: %v", err)
			}
		}
		logger.Printf("update: starting the new version")
		return syscall.Exec(self, []string{self, "--service", "--updated"}, os.Environ())
	}
	if updated {
		logger.Printf("updated to %s", version)
		notify("hots-dash uploader updated", "Now on version "+version+". It carries on uploading as before.", "")
		switch {
		case page == nil || prev == nil:
		case page.Reused(prev):
			logger.Printf("settings page: same address as before the update")
		case prev.PageOpen:
			logger.Printf("settings page: new address after the update, opening it")
			links.OpenURL(settings)
		}
	}
	a.Run(stopOnSignal())
	logger.Printf("stopped")
}

// uninstallIt: stop it, don't start it at log-in any more, remove the installed program. Settings and log stay.
func uninstallIt(dir string) {
	if stopRunning(dir) {
		fmt.Println("Stopped the uploader.")
	}
	removeService()
	target := installPath()
	if err := os.Remove(target); err == nil {
		fmt.Println("Removed", target)
	} else if !os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "Couldn't remove %s: %v\n", target, err)
	}
	fmt.Printf("It won't start when you log in any more. Its settings and log are still in %s: delete that folder to remove them too.\n", dir)
}
