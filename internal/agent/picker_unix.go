//go:build !windows

package agent

import (
	"errors"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

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
