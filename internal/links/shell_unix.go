//go:build !windows

package links

import (
	"os/exec"
	"runtime"
)

func shellOpen(u string) {
	cmd := "xdg-open"
	if runtime.GOOS == "darwin" {
		cmd = "open"
	}
	c := exec.Command(cmd, u)
	if c.Start() == nil {
		go func() { _ = c.Wait() }()
	}
}
