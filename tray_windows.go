//go:build windows

package main

import (
	_ "embed"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"fyne.io/systray"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

//go:embed icon.ico
var iconICO []byte

const (
	appName        = "hots-dash uploader"
	mutexName      = `Local\hots-dash-uploader`
	quitEventName  = `Local\hots-dash-uploader-quit`
	runKeyPath     = `Software\Microsoft\Windows\CurrentVersion\Run`
	runValue       = "hots-dash uploader"
	createNoWindow = 0x08000000
)

func u16(s string) *uint16 { p, _ := windows.UTF16PtrFromString(s); return p }

func msgBox(text string, icon uint32) {
	_, _ = windows.MessageBox(0, u16(text), u16(appName), icon|windows.MB_OK|windows.MB_SETFOREGROUND)
}

// installPath is where it lives once installed: %LOCALAPPDATA%\Programs\hots-dash\hots-dash-uploader.exe
func installPath() string {
	base := os.Getenv("LOCALAPPDATA")
	if base == "" {
		base, _ = os.UserCacheDir()
	}
	return filepath.Join(base, "Programs", "hots-dash", "hots-dash-uploader.exe")
}

func samePath(a, b string) bool {
	ea, _ := filepath.EvalSymlinks(a)
	eb, _ := filepath.EvalSymlinks(b)
	if ea == "" {
		ea = a
	}
	if eb == "" {
		eb = b
	}
	return strings.EqualFold(filepath.Clean(ea), filepath.Clean(eb))
}

func copyFile(src, dst string) error {
	_ = os.MkdirAll(filepath.Dir(dst), 0o755)
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".new"
	out, err := os.Create(tmp)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, in)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, dst) // replaces dst: removing it first could leave no program at all
	}
	if err != nil {
		_ = os.Remove(tmp)
	}
	return err
}

// stopRunning asks a running copy to quit (so a fresh download can replace it) and waits for it. False means it's
// still running.
func stopRunning() bool {
	ev, err := windows.OpenEvent(windows.EVENT_MODIFY_STATE, false, u16(quitEventName))
	if err != nil {
		return true // nothing running
	}
	_ = windows.SetEvent(ev)
	_ = windows.CloseHandle(ev)
	for i := 0; i < 50; i++ {
		h, err := windows.CreateMutex(nil, false, u16(mutexName))
		if h != 0 {
			_ = windows.CloseHandle(h)
		}
		if err != windows.ERROR_ALREADY_EXISTS {
			time.Sleep(300 * time.Millisecond) // let the old process finish exiting
			return true
		}
		time.Sleep(200 * time.Millisecond)
	}
	return false
}

func autostartEnabled() bool {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKeyPath, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer k.Close()
	_, _, err = k.GetStringValue(runValue)
	return err == nil
}

func setAutostart(on bool) error {
	k, _, err := registry.CreateKey(registry.CURRENT_USER, runKeyPath, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	if on {
		return k.SetStringValue(runValue, `"`+installPath()+`"`)
	}
	err = k.DeleteValue(runValue)
	if err == registry.ErrNotExist {
		return nil
	}
	return err
}

func shellOpen(u string) {
	_ = windows.ShellExecute(0, u16("open"), u16(u), nil, nil, windows.SW_SHOWNORMAL)
}

// toast shows a Windows notification (see notify.go: the text only ever reaches PowerShell as base64).
// Clicking it opens open, when that's a web address.
func toast(title, message, open string) {
	cmd := powershell(toastScript(toastXML(title, message, open)))
	_ = cmd.Start()
	go func() { _ = cmd.Wait() }()
}

func powershell(script string) *exec.Cmd {
	cmd := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-WindowStyle", "Hidden", "-EncodedCommand", encodedCommand(script))
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
	return cmd
}

// shortcutPath: the Start menu entry, so it can be opened again after Quit (%APPDATA%\Microsoft\Windows\Start Menu\Programs).
func shortcutPath() string {
	base := os.Getenv("APPDATA")
	if base == "" {
		base, _ = os.UserConfigDir()
	}
	return filepath.Join(base, "Microsoft", "Windows", "Start Menu", "Programs", appName+".lnk")
}

// ensureShortcut adds the Start menu entry when it's missing (a fresh install, or one from before it had one).
// The program has no icon of its own, so the tray icon goes next to it for the shortcut to use.
func ensureShortcut(target string, logger interface{ Printf(string, ...any) }) {
	lnk := shortcutPath()
	if _, err := os.Stat(lnk); err == nil {
		return
	}
	icon := filepath.Join(filepath.Dir(target), "hots-dash.ico")
	if err := os.WriteFile(icon, iconICO, 0o644); err != nil {
		logger.Printf("start menu shortcut: icon: %v", err)
	}
	_ = os.MkdirAll(filepath.Dir(lnk), 0o755)
	cmd := powershell(shortcutScript(lnk, target, icon))
	if err := cmd.Start(); err != nil {
		logger.Printf("start menu shortcut: %v", err)
		return
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			logger.Printf("start menu shortcut: %v", err)
		}
	case <-time.After(15 * time.Second):
		logger.Printf("start menu shortcut: PowerShell took too long")
	}
}

func main() {
	dir := appDir()
	logger := newLogger(dir, nil)
	cfg := resolveConfig(dir)
	if cfg == nil {
		msgBox("This copy isn't set up for a dashboard.\n\nOpen your hots-dash dashboard, go to Accounts, and use \"Download the uploader\": that copy is set up for you.", windows.MB_ICONINFORMATION)
		return
	}

	// Run from a download: install (or update) into the user's programs folder, start that copy, done.
	exe, _ := os.Executable()
	target := installPath()
	if !samePath(exe, target) {
		_, statErr := os.Stat(target)
		fresh := os.IsNotExist(statErr)
		if !stopRunning() {
			logger.Printf("install failed: the running copy didn't quit")
			msgBox("The uploader is still running.\n\nClose the uploader from the tray and run this again.", windows.MB_ICONWARNING)
			return
		}
		if err := copyFile(exe, target); err != nil {
			logger.Printf("install failed: %v", err)
			msgBox("Couldn't install the uploader:\n"+err.Error(), windows.MB_ICONERROR)
			return
		}
		if fresh || autostartEnabled() {
			_ = setAutostart(true)
		}
		// an update hands over quietly; a fresh download shows what it found
		mode := "--installed"
		if len(os.Args) > 1 && os.Args[1] == "--update" {
			mode = "--updated"
			logger.Printf("updated to %s", version)
		}
		cmd := exec.Command(target, mode)
		if err := cmd.Start(); err != nil {
			msgBox("Installed, but couldn't start it:\n"+err.Error(), windows.MB_ICONERROR)
		}
		ensureShortcut(target, logger) // after starting it, so the settings page isn't held up
		return
	}

	// One copy at a time.
	mutex, err := windows.CreateMutex(nil, false, u16(mutexName))
	if err == windows.ERROR_ALREADY_EXISTS {
		// already running: show its settings page instead
		if b, err := os.ReadFile(filepath.Join(dir, "settings-url.txt")); err == nil && len(b) > 0 {
			openURL(strings.TrimSpace(string(b)))
			return
		}
		msgBox("The uploader is already running: look for the hots-dash icon by the clock.", windows.MB_ICONINFORMATION)
		return
	}
	defer windows.CloseHandle(mutex)
	quit, _ := windows.CreateEvent(nil, 1, 0, u16(quitEventName))
	defer windows.CloseHandle(quit)

	a := NewAgent(*cfg, dir, logger)
	a.AutoSignIn = true
	a.OnConfigChange(func(c Config) {
		saveConfig(dir, c)
		if onAccount != nil {
			onAccount(c) // signed in or out from the settings page
		}
	})
	saveConfig(dir, a.Config()) // records the folders found on first run
	a.Notify = toast
	updated := len(os.Args) > 1 && os.Args[1] == "--updated"
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
	// updating itself: note where the settings page is (so the next copy can serve it there and an open page
	// carries on), then start the downloaded copy, which stops this one, replaces it and starts again
	applyUpdate = func(p string) error {
		if page != nil {
			if err := writeHandoff(dir, page.Handoff()); err != nil {
				logger.Printf("update: settings page handoff: %v", err)
			}
		}
		return exec.Command(p, "--update").Start()
	}
	if updated {
		toast("hots-dash uploader updated", "Now on version "+version+". It carries on uploading as before.", "")
		switch {
		case page == nil || prev == nil:
		case page.Reused(prev):
			logger.Printf("settings page: same address as before the update")
		case prev.PageOpen:
			logger.Printf("settings page: new address after the update, opening it")
			openURL(settings) // the old address is gone, and the page was open: show it at the new one
		}
	}
	if len(os.Args) > 1 && os.Args[1] == "--installed" {
		who := ""
		if cfg.Name != "" {
			who = " as " + cfg.Name
		}
		toast("hots-dash uploader is running", "It's uploading your replays"+who+" and will keep sending new ones. It's in the tray by the clock.", cfg.Server)
		if settings != "" {
			openURL(settings) // show what it found, so folders can be added straight away
		}
	}

	go func() {
		_, _ = windows.WaitForSingleObject(quit, windows.INFINITE)
		systray.Quit()
	}()
	stop := make(chan struct{})
	systray.Run(func() { onReady(a, stop, settings) }, func() {
		select {
		case <-stop:
		default:
			close(stop)
		}
		time.Sleep(300 * time.Millisecond) // let state save
	})
}

// onAccount updates the tray menu when the uploader signs in or out.
var onAccount func(Config)

func onReady(a *Agent, stop chan struct{}, settings string) {
	cfg := a.Config()
	systray.SetIcon(iconICO)
	systray.SetTitle(appName)
	systray.SetTooltip(appName)

	status := systray.AddMenuItem("Starting…", "")
	status.Disable()
	who := systray.AddMenuItem("", "")
	who.Disable()
	showWho := func(c Config) {
		switch {
		case c.Name != "":
			who.SetTitle("Uploading as " + c.Name)
		case c.Token == "":
			who.SetTitle("Not signed in (Settings → Sign in)")
		default:
			who.Hide()
			return
		}
		who.Show()
	}
	showWho(cfg)
	onAccount = showWho
	lobby := systray.AddMenuItem("", "")
	lobby.Disable()
	lobby.Hide()
	systray.AddSeparator()
	settingsItem := systray.AddMenuItem("Settings and folders…", "")
	open := systray.AddMenuItem("Open dashboard", "")
	syncNow := systray.AddMenuItem("Upload new replays now", "")
	auto := systray.AddMenuItemCheckbox("Start with Windows", "", autostartEnabled())
	logs := systray.AddMenuItem("Show log", "")
	upd := systray.AddMenuItem("", "")
	upd.Hide()
	a.OnUpdate = func(v string) {
		upd.SetTitle("Update to version " + v)
		upd.Show()
	}
	systray.AddSeparator()
	quit := systray.AddMenuItem("Quit", "")
	systray.SetOnTapped(func() {
		if settings != "" {
			openURL(settings)
		} else {
			openURL(cfg.Server)
		}
	})

	a.OnStatus = func(s Status) {
		status.SetTitle(s.Text)
		tip := appName + "\n" + s.Text
		if len(tip) > 120 {
			tip = tip[:120]
		}
		systray.SetTooltip(tip)
		if s.LastLobby != "" {
			lobby.SetTitle("Last game: " + s.LastLobby)
			lobby.Show()
		}
	}

	go a.Run(stop)
	go func() {
		for {
			select {
			case <-settingsItem.ClickedCh:
				if settings != "" {
					openURL(settings)
				}
			case <-open.ClickedCh:
				openURL(cfg.Server)
			case <-syncNow.ClickedCh:
				a.SyncSoon()
			case <-auto.ClickedCh:
				on := !auto.Checked()
				if err := setAutostart(on); err == nil {
					if on {
						auto.Check()
					} else {
						auto.Uncheck()
					}
				}
			case <-upd.ClickedCh:
				upd.SetTitle("Updating…")
				go func() {
					if err := a.UpdateNow(); err != nil {
						upd.SetTitle("Update failed: try again")
					}
				}()
			case <-logs.ClickedCh:
				openFile(filepath.Join(a.dir, "uploader.log"))
			case <-quit.ClickedCh:
				systray.Quit()
				return
			case <-stop:
				return
			}
		}
	}()
}
