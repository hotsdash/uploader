//go:build linux

package main

import (
	"os"
	"path/filepath"
	"syscall"
	"unsafe"
)

// watchFolders asks Linux (inotify) to say when anything under these folders is created, moved, written or closed
// after writing, and nudges changed when it does. inotify watches one folder at a time, so every folder under them is
// watched, and a folder created later (a new account, the first replay of a new kind) is added when it appears.
// Notifications only say "something changed", so the caller lists the replay folders to see what (watch.go).
func watchFolders(dirs []string, changed chan<- struct{}) *folderWatch {
	fd, err := syscall.InotifyInit1(syscall.IN_CLOEXEC | syscall.IN_NONBLOCK)
	if err != nil {
		return nil
	}
	const mask = syscall.IN_CREATE | syscall.IN_MOVED_TO | syscall.IN_MODIFY | syscall.IN_CLOSE_WRITE |
		syscall.IN_DELETE_SELF | syscall.IN_MOVE_SELF | syscall.IN_ONLYDIR
	paths := map[int32]string{}
	add := func(d string) {
		for _, p := range subdirs(d, maxWatchedDirs-len(paths)) {
			if wd, err := syscall.InotifyAddWatch(fd, p, mask); err == nil {
				paths[int32(wd)] = p
			}
		}
	}
	for _, d := range dirs {
		add(d)
	}
	if len(paths) == 0 {
		syscall.Close(fd)
		return nil
	}
	// non-blocking, so reads wait in Go's poller and closing the file ends a read that's waiting
	f := os.NewFile(uintptr(fd), "inotify")
	w := &folderWatch{done: make(chan struct{})}
	w.alive.Store(true)
	go func() {
		defer close(w.done)
		defer w.alive.Store(false)
		buf := make([]byte, 64*1024)
		for {
			got, err := f.Read(buf)
			if err != nil {
				return // closed (stopping), or inotify failed: the 30 s scan watches again
			}
			for off := 0; off+syscall.SizeofInotifyEvent <= got; {
				ev := (*syscall.InotifyEvent)(unsafe.Pointer(&buf[off]))
				name := ""
				if ev.Len > 0 {
					b := buf[off+syscall.SizeofInotifyEvent : off+syscall.SizeofInotifyEvent+int(ev.Len)]
					for i, c := range b {
						if c == 0 {
							b = b[:i]
							break
						}
					}
					name = string(b)
				}
				switch {
				case ev.Mask&syscall.IN_ISDIR != 0 && ev.Mask&(syscall.IN_CREATE|syscall.IN_MOVED_TO) != 0:
					if parent, ok := paths[ev.Wd]; ok && name != "" && len(paths) < maxWatchedDirs {
						add(filepath.Join(parent, name))
					}
				case ev.Mask&syscall.IN_IGNORED != 0:
					delete(paths, ev.Wd)
				}
				off += syscall.SizeofInotifyEvent + int(ev.Len)
			}
			if len(paths) == 0 {
				return // every watched folder is gone: the 30 s scan watches again
			}
			nudge(changed)
		}
	}()
	w.stop = func() { _ = f.Close(); <-w.done }
	return w
}
