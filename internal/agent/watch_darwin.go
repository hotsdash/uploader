//go:build darwin

package agent

import (
	"syscall"
)

// oEvtOnly: open a folder only to be told about it (O_EVTONLY), so watching doesn't stop a drive being ejected.
const oEvtOnly = 0x8000

// watchFolders asks macOS (kqueue) to say when anything changes in these folders and every folder under them, and
// nudges changed when it does. kqueue watches one folder at a time and says only that something in it changed (a file
// added, renamed or removed), so after each change the folders are listed again to watch new ones. A replay still being
// written is followed by watch.go looking again every second until it settles, so file writes needn't be watched.
func watchFolders(dirs []string, changed chan<- struct{}) *folderWatch {
	kq, err := syscall.Kqueue()
	if err != nil {
		return nil
	}
	syscall.CloseOnExec(kq)
	watched := map[string]int{} // folder -> its fd
	const flags = syscall.NOTE_WRITE | syscall.NOTE_EXTEND | syscall.NOTE_DELETE | syscall.NOTE_RENAME
	add := func() {
		for _, d := range dirs {
			for _, p := range subdirs(d, maxWatchedDirs-len(watched)) {
				if _, ok := watched[p]; ok {
					continue
				}
				fd, err := syscall.Open(p, oEvtOnly|syscall.O_CLOEXEC, 0)
				if err != nil {
					continue
				}
				ev := syscall.Kevent_t{Ident: uint64(fd), Filter: syscall.EVFILT_VNODE, Flags: syscall.EV_ADD | syscall.EV_CLEAR, Fflags: flags}
				if _, err := syscall.Kevent(kq, []syscall.Kevent_t{ev}, nil, nil); err != nil {
					syscall.Close(fd)
					continue
				}
				watched[p] = fd
			}
		}
	}
	add()
	if len(watched) == 0 {
		syscall.Close(kq)
		return nil
	}
	// a user event on the same queue wakes the loop when stopping
	const wakeID = 1
	wake := syscall.Kevent_t{Ident: wakeID, Filter: syscall.EVFILT_USER, Flags: syscall.EV_ADD | syscall.EV_CLEAR}
	if _, err := syscall.Kevent(kq, []syscall.Kevent_t{wake}, nil, nil); err != nil {
		for _, fd := range watched {
			syscall.Close(fd)
		}
		syscall.Close(kq)
		return nil
	}
	w := &folderWatch{done: make(chan struct{})}
	w.alive.Store(true)
	go func() {
		defer close(w.done)
		defer func() {
			for _, fd := range watched {
				syscall.Close(fd)
			}
			syscall.Close(kq)
		}()
		defer w.alive.Store(false)
		events := make([]syscall.Kevent_t, 64)
		for {
			n, err := syscall.Kevent(kq, nil, events, nil)
			if err == syscall.EINTR {
				continue
			}
			if err != nil {
				return
			}
			gone := false
			for _, ev := range events[:n] {
				if ev.Filter == syscall.EVFILT_USER {
					return
				}
				if ev.Fflags&(syscall.NOTE_DELETE|syscall.NOTE_RENAME) != 0 {
					gone = true
				}
			}
			if gone { // a watched folder went: forget the ones that aren't there any more
				for p, fd := range watched {
					var st syscall.Stat_t
					if syscall.Stat(p, &st) != nil {
						syscall.Close(fd)
						delete(watched, p)
					}
				}
			}
			add() // new folders (a new account, its first replay of a kind)
			if len(watched) == 0 {
				return // the 30 s scan watches again
			}
			nudge(changed)
		}
	}()
	w.stop = func() {
		trigger := syscall.Kevent_t{Ident: wakeID, Filter: syscall.EVFILT_USER, Fflags: syscall.NOTE_TRIGGER}
		_, _ = syscall.Kevent(kq, []syscall.Kevent_t{trigger}, nil, nil)
		<-w.done
	}
	return w
}
