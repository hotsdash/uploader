//go:build windows

package agent

import "golang.org/x/sys/windows"

// maxWaitHandles: WaitForMultipleObjects takes at most 64 handles; one is the stop event.
const maxWaitHandles = 64

// watchFolders asks Windows to say when anything under these folders (subfolders too) is created, renamed, resized or
// written, and nudges changed when it does. Nothing runs between changes: the goroutine sleeps in
// WaitForMultipleObjects. Notifications say only "something changed here", so the caller lists the replay folders to
// see what (watch.go). A folder Windows can't watch (gone, or a drive that doesn't support it) is skipped.
func watchFolders(dirs []string, changed chan<- struct{}) *folderWatch {
	const filter = windows.FILE_NOTIFY_CHANGE_FILE_NAME | windows.FILE_NOTIFY_CHANGE_SIZE | windows.FILE_NOTIFY_CHANGE_LAST_WRITE
	var handles []windows.Handle
	for _, d := range dirs {
		if len(handles) == maxWaitHandles-1 {
			break
		}
		h, err := windows.FindFirstChangeNotification(d, true, filter)
		if err != nil {
			continue
		}
		handles = append(handles, h)
	}
	closeAll := func() {
		for _, h := range handles {
			_ = windows.FindCloseChangeNotification(h)
		}
	}
	if len(handles) == 0 {
		return nil
	}
	quit, err := windows.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		closeAll()
		return nil
	}
	wait := append(append([]windows.Handle{}, handles...), quit)
	w := &folderWatch{done: make(chan struct{})}
	w.alive.Store(true)
	go func() {
		defer close(w.done)
		defer func() { closeAll(); _ = windows.CloseHandle(quit) }()
		defer w.alive.Store(false)
		for {
			ev, err := windows.WaitForMultipleObjects(wait, false, windows.INFINITE)
			if err != nil || ev == windows.WAIT_FAILED {
				return
			}
			i := int(ev - windows.WAIT_OBJECT_0)
			if i < 0 || i >= len(handles) { // the stop event (or something unexpected)
				return
			}
			select {
			case changed <- struct{}{}:
			default: // a look is already due
			}
			if windows.FindNextChangeNotification(handles[i]) != nil {
				return
			}
		}
	}()
	w.stop = func() { _ = windows.SetEvent(quit); <-w.done }
	return w
}
