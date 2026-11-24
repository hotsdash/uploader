//go:build linux || darwin

package main

import (
	"io/fs"
	"path/filepath"
)

// maxWatchedDirs: folders watched at most (each Battle.net account has a few; a big backup folder could have many).
// Past it the 30 s scan still finds everything, just not within seconds.
const maxWatchedDirs = 4096

// subdirs lists dir and every folder under it (not following links, like the replay walk), up to max.
func subdirs(dir string, max int) []string {
	var out []string
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if d != nil && d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.IsDir() {
			return nil
		}
		if len(out) >= max {
			return filepath.SkipAll
		}
		out = append(out, p)
		return nil
	})
	return out
}

func nudge(changed chan<- struct{}) {
	select {
	case changed <- struct{}{}:
	default: // a look is already due
	}
}
