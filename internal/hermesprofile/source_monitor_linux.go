//go:build linux

package hermesprofile

import (
	"encoding/binary"
	"fmt"
	"os"
	"strings"

	"golang.org/x/sys/unix"
)

type inotifyMonitor struct {
	fd      int
	watches map[int32]sourceWatch
	changed map[*os.File]uint64
}

func newSourceMonitor() (sourceMonitor, error) {
	fd, err := unix.InotifyInit1(unix.IN_CLOEXEC | unix.IN_NONBLOCK)
	if err != nil {
		return nil, err
	}
	return &inotifyMonitor{fd: fd, watches: map[int32]sourceWatch{}, changed: map[*os.File]uint64{}}, nil
}
func (m *inotifyMonitor) add(f *os.File, initial Identity, w sourceWatch) error {
	if w.kind == watchSidecar {
		return monitorBinding(f, initial)
	}
	if err := monitorBinding(f, initial); err != nil {
		return err
	}
	flags := uint32(unix.IN_ATTRIB | unix.IN_MODIFY | unix.IN_CREATE | unix.IN_DELETE | unix.IN_MOVED_FROM | unix.IN_MOVED_TO | unix.IN_MOVE_SELF | unix.IN_DELETE_SELF | unix.IN_UNMOUNT)
	if directory(initial) {
		flags |= unix.IN_ONLYDIR
	}
	// This proc link names the already-held inode, not the mutable source path.
	wd, err := unix.InotifyAddWatch(m.fd, fmt.Sprintf("/proc/self/fd/%d", f.Fd()), flags)
	if err != nil {
		return err
	}
	if _, ok := m.watches[int32(wd)]; ok {
		return fmt.Errorf("duplicate profile custody watch")
	}
	w.file, w.initial = f, initial
	m.watches[int32(wd)] = w
	return monitorBinding(f, initial)
}
func (m *inotifyMonitor) events(raw []byte, s *sourceTree) error {
	for len(raw) > 0 {
		if len(raw) < unix.SizeofInotifyEvent {
			return fmt.Errorf("truncated profile custody event")
		}
		wd := int32(binary.NativeEndian.Uint32(raw[:4]))
		mask := binary.NativeEndian.Uint32(raw[4:8])
		length := int(binary.NativeEndian.Uint32(raw[12:16]))
		if length > len(raw)-unix.SizeofInotifyEvent {
			return fmt.Errorf("invalid profile custody event length")
		}
		name := strings.TrimRight(string(raw[16:16+length]), "\x00")
		raw = raw[16+length:]
		w, ok := m.watches[wd]
		if !ok || mask&(unix.IN_Q_OVERFLOW|unix.IN_IGNORED|unix.IN_UNMOUNT|unix.IN_MOVE_SELF|unix.IN_DELETE_SELF|unix.IN_MOVED_FROM|unix.IN_MOVED_TO) != 0 {
			return fmt.Errorf("profile custody event lost or inode moved")
		}
		if name != "" {
			if w.kind != watchDirectory || strings.ContainsRune(name, '\x00') {
				return fmt.Errorf("invalid profile namespace event")
			}
			if f, ok := s.filesFor(w.dir, name); ok && ((f.db || f.log) && mask == unix.IN_MODIFY || f.log && mask == unix.IN_ATTRIB) {
				continue
			}
			if _, ok := s.allowedSidecar(w.dir, name); !ok || mask & ^uint32(unix.IN_CREATE|unix.IN_DELETE|unix.IN_MODIFY|unix.IN_ATTRIB) != 0 {
				return fmt.Errorf("unexpected profile namespace event")
			}
			// Name events for ephemeral sidecars have no persistent inode watch.
			// Recheck any current object no-follow even in the final event drain.
			base, _ := s.allowedSidecar(w.dir, name)
			info, err := statAt(w.dir, name)
			if err != nil && err != unix.ENOENT {
				return err
			}
			if err == nil && !safeSidecar(info, base) {
				return fmt.Errorf("unsafe current SQLite sidecar")
			}

			if mask&(unix.IN_CREATE|unix.IN_DELETE) != 0 {
				m.changed[w.dir]++
			}
		} else {
			allowed := uint32(unix.IN_MODIFY)
			// inotify cannot distinguish a same-mode managed log chmod from a
			// transient external hardlink. Endpoints require nlink=1; watched namespace
			// links and every source replacement/move/delete still fail closed.
			if w.kind == watchLog {
				allowed |= unix.IN_ATTRIB
			}
			if mask == 0 || mask & ^allowed != 0 || w.kind != watchDatabase && w.kind != watchLog {
				return fmt.Errorf("profile inode custody changed")
			}
		}
	}
	return nil
}
func (m *inotifyMonitor) check(s *sourceTree) (map[*os.File]uint64, error) {
	buf := make([]byte, 64*1024)
	for count := 0; count < maxEntries; count++ {
		n, err := unix.Read(m.fd, buf)
		if err == unix.EAGAIN {
			return sourceChanges(m.changed), nil
		}
		if err != nil {
			return nil, err
		}
		if n == 0 {
			return nil, fmt.Errorf("profile custody event stream closed")
		}
		if err = m.events(buf[:n], s); err != nil {
			return nil, err
		}
		for _, w := range m.watches {
			if err = validateMutableWatch(w); err != nil {
				return nil, err
			}
		}
	}
	return nil, fmt.Errorf("profile custody event limit exceeded")
}
func (m *inotifyMonitor) close() { unix.Close(m.fd) }
