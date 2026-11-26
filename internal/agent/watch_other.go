//go:build !windows && !linux && !darwin

package agent

// watchFolders: no folder notifications on this system. The caller falls back to looking every couple of seconds,
// only around a game (watch.go).
func watchFolders([]string, chan<- struct{}) *folderWatch { return nil }
