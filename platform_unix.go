//go:build !windows

package main

// Linux and Macs: no tray. The uploader installs itself as a login service (a systemd user service on Linux, or an
// XDG autostart entry where there's no systemd; a LaunchAgent on a Mac), and its settings page is the way in: running
// the program again opens it.

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	serviceName = "hots-dash-uploader"    // systemd unit (.service) and XDG autostart entry (.desktop)
	launchLabel = "com.hotsdash.uploader" // the Mac LaunchAgent
)

// osName: the system's everyday name, for messages.
func osName() string {
	if runtime.GOOS == "darwin" {
		return "macOS"
	}
	return "Linux"
}

// installPath is where it lives once installed: ~/.local/bin on Linux, the app folder on a Mac.
func installPath() string {
	home, _ := os.UserHomeDir()
	if runtime.GOOS == "darwin" {
		return filepath.Join(appDir(), "hots-dash-uploader")
	}
	if b := os.Getenv("XDG_BIN_HOME"); filepath.IsAbs(b) {
		return filepath.Join(b, "hots-dash-uploader")
	}
	return filepath.Join(home, ".local", "bin", "hots-dash-uploader")
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
	return filepath.Clean(ea) == filepath.Clean(eb)
}

// copyFile copies src over dst (as a program) through a temporary file, so a running dst is never half-written.
func copyFile(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".new"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, dst)
}

// ---------- one copy at a time ----------

// lockFile is held (flock) for as long as this copy runs; kept here so it's never closed (and unlocked) early.
var lockFile *os.File

// takeLock: false when another copy already holds it. The lock goes when this process ends or execs (an update).
func takeLock(dir string) bool {
	f, err := os.OpenFile(filepath.Join(dir, "uploader.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return true // can't tell: run anyway
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return false
	}
	lockFile = f
	_ = os.WriteFile(filepath.Join(dir, "uploader.pid"), []byte(strconv.Itoa(os.Getpid())), 0o600)
	return true
}

// running: is a copy running (holding the lock)?
func running(dir string) bool {
	f, err := os.OpenFile(filepath.Join(dir, "uploader.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return false
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return true
	}
	_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	return false
}

// stopRunning asks a running copy to quit (so a fresh download can replace it) and waits for it. A copy stopped this
// way exits cleanly, so neither systemd nor launchd starts it again.
func stopRunning(dir string) bool {
	if !running(dir) {
		return false
	}
	if b, err := os.ReadFile(filepath.Join(dir, "uploader.pid")); err == nil {
		if pid, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil && pid > 1 && pid != os.Getpid() {
			_ = syscall.Kill(pid, syscall.SIGTERM)
		}
	}
	for i := 0; i < 50 && running(dir); i++ {
		time.Sleep(200 * time.Millisecond)
	}
	return true
}

// ---------- the login service ----------

var (
	systemdOnce sync.Once
	systemdOK   bool
)

// hasSystemd: can we use a systemd user service here (most Linux desktops, NixOS included)?
func hasSystemd() bool {
	systemdOnce.Do(func() {
		if runtime.GOOS != "linux" {
			return
		}
		if _, err := exec.LookPath("systemctl"); err != nil {
			return
		}
		systemdOK = exec.Command("systemctl", "--user", "show-environment").Run() == nil
	})
	return systemdOK
}

func configHome() string {
	if c := os.Getenv("XDG_CONFIG_HOME"); filepath.IsAbs(c) {
		return c
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config")
}

func unitPath() string { return filepath.Join(configHome(), "systemd", "user", serviceName+".service") }
func desktopPath() string {
	return filepath.Join(configHome(), "autostart", serviceName+".desktop")
}
func plistPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "LaunchAgents", launchLabel+".plist")
}

// systemdQuote quotes a path for ExecStart (systemd's own rules: double quotes, backslash escapes, %% for % and $$ for $).
func systemdQuote(s string) string {
	s = strings.NewReplacer(`\`, `\\`, `"`, `\"`, "%", "%%", "$", "$$").Replace(s)
	return `"` + s + `"`
}

func unitFile(exe string) string {
	return `[Unit]
Description=hots-dash uploader: sends your Heroes of the Storm replays to hots-dash
After=network-online.target

[Service]
ExecStart=` + systemdQuote(exe) + ` --service
Restart=on-failure
RestartSec=10

[Install]
WantedBy=default.target
`
}

var xmlText = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")

func plistFile(exe, logFile string, atLogin bool) string {
	run := "<false/>"
	if atLogin {
		run = "<true/>"
	}
	return `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key><string>` + launchLabel + `</string>
	<key>ProgramArguments</key>
	<array><string>` + xmlText.Replace(exe) + `</string><string>--service</string></array>
	<key>RunAtLoad</key>` + run + `
	<key>KeepAlive</key><dict><key>SuccessfulExit</key><false/></dict>
	<key>ProcessType</key><string>Background</string>
	<key>StandardErrorPath</key><string>` + xmlText.Replace(logFile) + `</string>
</dict>
</plist>
`
}

// desktopQuote quotes a path for an XDG .desktop Exec line.
func desktopQuote(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\\\`, `"`, `\"`, "`", "\\`", "$", `\$`, "%", "%%").Replace(s) + `"`
}

func desktopFile(exe string) string {
	return `[Desktop Entry]
Type=Application
Name=hots-dash uploader
Comment=Sends your Heroes of the Storm replays to hots-dash
Exec=` + desktopQuote(exe) + ` --service
Terminal=false
NoDisplay=true
X-GNOME-Autostart-enabled=true
`
}

func launchDomain() string { return "gui/" + strconv.Itoa(os.Getuid()) }

// writeService writes the service definition for exe (always, so it can be started; whether it starts at login is
// autostart). On a Mac the LaunchAgent carries that choice itself.
func writeService(exe string, atLogin bool) error {
	switch {
	case runtime.GOOS == "darwin":
		p := plistPath()
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		return os.WriteFile(p, []byte(plistFile(exe, filepath.Join(appDir(), "service.log"), atLogin)), 0o644)
	case hasSystemd():
		p := unitPath()
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(p, []byte(unitFile(exe)), 0o644); err != nil {
			return err
		}
		return exec.Command("systemctl", "--user", "daemon-reload").Run()
	}
	return nil // started by hand below; autostart is the .desktop entry
}

func autostartEnabled() bool {
	switch {
	case runtime.GOOS == "darwin":
		b, err := os.ReadFile(plistPath())
		return err == nil && strings.Contains(string(b), "<key>RunAtLoad</key><true/>")
	case hasSystemd():
		return exec.Command("systemctl", "--user", "is-enabled", "--quiet", serviceName+".service").Run() == nil
	}
	_, err := os.Stat(desktopPath())
	return err == nil
}

// setAutostart: start (or not) when you log in. Doesn't start or stop the copy that's running.
func setAutostart(on bool) error {
	exe := installPath()
	switch {
	case runtime.GOOS == "darwin":
		return writeService(exe, on) // read at the next login
	case hasSystemd():
		if _, err := os.Stat(unitPath()); err != nil {
			if err := writeService(exe, on); err != nil {
				return err
			}
		}
		verb := "disable"
		if on {
			verb = "enable"
		}
		if out, err := exec.Command("systemctl", "--user", verb, serviceName+".service").CombinedOutput(); err != nil {
			return fmt.Errorf("systemctl %s: %v %s", verb, err, strings.TrimSpace(string(out)))
		}
		return nil
	}
	if !on {
		if err := os.Remove(desktopPath()); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(desktopPath()), 0o755); err != nil {
		return err
	}
	return os.WriteFile(desktopPath(), []byte(desktopFile(exe)), 0o644)
}

// startService starts the installed copy in the background, through the service manager when there is one.
func startService(exe string) error {
	switch {
	case runtime.GOOS == "darwin":
		_ = exec.Command("launchctl", "bootout", launchDomain()+"/"+launchLabel).Run() // an older definition
		var err error
		var out []byte
		for i := 0; i < 10; i++ { // bootout finishes in the background: bootstrap fails until it has
			if out, err = exec.Command("launchctl", "bootstrap", launchDomain(), plistPath()).CombinedOutput(); err == nil {
				break
			}
			time.Sleep(300 * time.Millisecond)
		}
		if err != nil {
			return fmt.Errorf("launchctl bootstrap: %v %s", err, strings.TrimSpace(string(out)))
		}
		return exec.Command("launchctl", "kickstart", launchDomain()+"/"+launchLabel).Run()
	case hasSystemd():
		if out, err := exec.Command("systemctl", "--user", "restart", serviceName+".service").CombinedOutput(); err != nil {
			return fmt.Errorf("systemctl start: %v %s", err, strings.TrimSpace(string(out)))
		}
		return nil
	}
	cmd := exec.Command(exe, "--service")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

// removeService takes away the service definition and autostart entry (uninstall).
func removeService() {
	switch {
	case runtime.GOOS == "darwin":
		_ = exec.Command("launchctl", "bootout", launchDomain()+"/"+launchLabel).Run()
		_ = os.Remove(plistPath())
	case hasSystemd():
		_ = exec.Command("systemctl", "--user", "disable", "--now", serviceName+".service").Run()
		_ = os.Remove(unitPath())
		_ = exec.Command("systemctl", "--user", "daemon-reload").Run()
	}
	_ = os.Remove(desktopPath())
}

func autostartState() (supported, on bool) { return true, autostartEnabled() }
func setAutostartState(on bool) error      { return setAutostart(on) }

// ---------- notifications, folder picker ----------

// notify shows a desktop notification. Its text comes partly from the dashboard, so it's only ever an argument
// (notify-send, after --) or an environment variable (macOS), never part of a command or script. Clicking it does
// nothing here (the Windows one opens open).
func notify(title, message, _ string) {
	title, message = clip(plainText(title), 200), clip(plainText(message), 1000)
	var c *exec.Cmd
	if runtime.GOOS == "darwin" {
		c = exec.Command("osascript", "-l", "JavaScript", "-e",
			`ObjC.import("stdlib"); var a = Application.currentApplication(); a.includeStandardAdditions = true; a.displayNotification($.getenv("HD_MESSAGE"), {withTitle: $.getenv("HD_TITLE")})`)
		c.Env = append(os.Environ(), "HD_TITLE="+title, "HD_MESSAGE="+message)
	} else {
		if _, err := exec.LookPath("notify-send"); err != nil {
			return
		}
		c = exec.Command("notify-send", "--app-name=hots-dash uploader", "--", title, message)
	}
	if c.Start() == nil {
		go func() { _ = c.Wait() }()
	}
}

// plainText drops control characters (keeping new lines) from notification text.
func plainText(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 && r != '\n' || r == 0x7f {
			return -1
		}
		return r
	}, s)
}

// pickerCommand: a folder picker on this system (macOS' own; zenity or kdialog on Linux), or nil.
func pickerCommand() []string {
	const prompt = "Choose a folder with Heroes of the Storm replays"
	if runtime.GOOS == "darwin" {
		return []string{"osascript", "-e", `POSIX path of (choose folder with prompt "` + prompt + `")`}
	}
	if os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == "" {
		return nil
	}
	if p, err := exec.LookPath("zenity"); err == nil {
		return []string{p, "--file-selection", "--directory", "--title=" + prompt}
	}
	if p, err := exec.LookPath("kdialog"); err == nil {
		return []string{p, "--getexistingdirectory", os.Getenv("HOME"), "--title", prompt}
	}
	return nil
}

func hasFolderPicker() bool { return pickerCommand() != nil }

// pickFolder shows the folder picker. Returns "" if cancelled.
func pickFolder() (string, error) {
	c := pickerCommand()
	if c == nil {
		return "", errNoPicker
	}
	out, err := exec.Command(c[0], c[1:]...).Output()
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return "", nil // cancelled
	}
	if err != nil {
		return "", err
	}
	p := strings.TrimSpace(string(out))
	if len(p) > 1 {
		p = strings.TrimRight(p, "/")
	}
	return p, nil
}
