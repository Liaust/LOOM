//go:build darwin

package hermesprofile

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

type vnodeMonitor struct {
	fd      int
	watches map[uint64]sourceWatch
	changed map[*os.File]uint64
}

func newSourceMonitor() (sourceMonitor, error) {
	fd, err := unix.Kqueue()
	if err != nil {
		return nil, err
	}
	unix.CloseOnExec(fd)
	return &vnodeMonitor{fd: fd, watches: map[uint64]sourceWatch{}, changed: map[*os.File]uint64{}}, nil
}
func (m *vnodeMonitor) add(f *os.File, initial Identity, w sourceWatch) error {
	if err := monitorBinding(f, initial); err != nil {
		return err
	}
	ev := unix.Kevent_t{Ident: uint64(f.Fd()), Filter: unix.EVFILT_VNODE, Flags: unix.EV_ADD | unix.EV_CLEAR,
		Fflags: unix.NOTE_DELETE | unix.NOTE_WRITE | unix.NOTE_EXTEND | unix.NOTE_ATTRIB | unix.NOTE_LINK | unix.NOTE_RENAME | unix.NOTE_REVOKE}
	if _, err := unix.Kevent(m.fd, []unix.Kevent_t{ev}, nil, nil); err != nil {
		return err
	}
	w.file, w.initial = f, initial
	m.watches[ev.Ident] = w
	return monitorBinding(f, initial)
}
func (m *vnodeMonitor) event(ev unix.Kevent_t) error {
	w, ok := m.watches[ev.Ident]
	if !ok || ev.Filter != unix.EVFILT_VNODE || ev.Flags&(unix.EV_ERROR|unix.EV_EOF) != 0 {
		return fmt.Errorf("profile custody event lost or invalid")
	}
	allowed := uint32(0)
	if w.kind == watchDirectory || w.kind == watchDatabase || w.kind == watchLog {
		allowed = unix.NOTE_WRITE | unix.NOTE_EXTEND
	}
	if w.kind == watchSidecar {
		allowed = unix.NOTE_WRITE | unix.NOTE_EXTEND | unix.NOTE_DELETE | unix.NOTE_ATTRIB
		if ev.Fflags&unix.NOTE_DELETE != 0 {
			allowed |= unix.NOTE_LINK
		}
	}
	// Darwin coalesces content changes with NOTE_ATTRIB. Metadata remains exact
	// at the endpoint and in ZIP mode matching. Rename/delete/link stay fatal
	// for every bound source inode, even when coalesced with permitted writes.
	if (w.kind == watchDatabase || w.kind == watchLog || w.kind == watchSidecar) && ev.Fflags&(unix.NOTE_WRITE|unix.NOTE_EXTEND) != 0 {
		allowed |= unix.NOTE_ATTRIB
	}
	if w.kind == watchLog {
		allowed |= unix.NOTE_ATTRIB
	} // Native managed log constructor reapplies 0660.
	if ev.Fflags == 0 || ev.Fflags & ^allowed != 0 {
		return fmt.Errorf("profile inode custody changed (kind=%d flags=%x)", w.kind, ev.Fflags)
	}
	if w.kind == watchDirectory {
		m.changed[w.dir]++
	}
	return nil
}
func (m *vnodeMonitor) check(_ *sourceTree) (map[*os.File]uint64, error) {
	events := make([]unix.Kevent_t, 128)
	// EV_CLEAR retains the union of vnode flags, so coalescing cannot discard a
	// bound inode's rename/delete/link/attribute event. Drain with a hard bound.
	for count := 0; count <= maxEntries; count += len(events) {
		n, err := unix.Kevent(m.fd, nil, events, &unix.Timespec{})
		if err == unix.EINTR {
			continue
		} // No events consumed; bounded retry.
		if err != nil {
			return nil, err
		}
		for _, ev := range events[:n] {
			if err = m.event(ev); err != nil {
				return nil, err
			}
			if err = validateMutableWatch(m.watches[ev.Ident]); err != nil {
				return nil, err
			}
		}
		if n == 0 {
			return sourceChanges(m.changed), nil
		}
	}
	return nil, fmt.Errorf("profile custody event limit exceeded")
}
func (m *vnodeMonitor) close() { unix.Close(m.fd) }
