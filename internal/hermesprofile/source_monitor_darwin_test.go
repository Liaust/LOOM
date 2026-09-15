//go:build darwin

package hermesprofile

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func TestSourceMonitorDarwinEvents(t *testing.T) {
	for _, kind := range []watchKind{watchOrdinary, watchDirectory, watchStatic, watchDatabase, watchLog} {
		for _, flags := range []uint32{unix.NOTE_RENAME, unix.NOTE_DELETE, unix.NOTE_REVOKE, unix.NOTE_LINK, unix.NOTE_ATTRIB, unix.NOTE_WRITE | unix.NOTE_EXTEND | unix.NOTE_ATTRIB, unix.NOTE_WRITE | unix.NOTE_RENAME, 0, 1 << 31} {
			m := &vnodeMonitor{watches: map[uint64]sourceWatch{1: {kind: kind}}, changed: map[*os.File]uint64{}}
			err := m.event(unix.Kevent_t{Ident: 1, Filter: unix.EVFILT_VNODE, Fflags: flags})
			allowed := ((kind == watchDatabase || kind == watchLog) && flags == unix.NOTE_WRITE|unix.NOTE_EXTEND|unix.NOTE_ATTRIB) || kind == watchLog && flags == unix.NOTE_ATTRIB
			if (err == nil) != allowed {
				t.Fatalf("kind=%d flags=%x err=%v", kind, flags, err)
			}
		}
	}
	for _, ev := range []unix.Kevent_t{{Ident: 2, Filter: unix.EVFILT_VNODE, Fflags: unix.NOTE_WRITE}, {Ident: 1, Filter: unix.EVFILT_READ, Fflags: unix.NOTE_WRITE}, {Ident: 1, Filter: unix.EVFILT_VNODE, Flags: unix.EV_ERROR}, {Ident: 1, Filter: unix.EVFILT_VNODE, Flags: unix.EV_EOF}} {
		m := &vnodeMonitor{watches: map[uint64]sourceWatch{1: {kind: watchDatabase}}, changed: map[*os.File]uint64{}}
		if m.event(ev) == nil {
			t.Fatal("lost/invalid event accepted")
		}
	}
	m := &vnodeMonitor{fd: -1}
	if _, err := m.check(nil); err == nil {
		t.Fatal("read error accepted")
	}
	f, err := os.Create(filepath.Join(t.TempDir(), "fixture"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	initial, err := statFD(f)
	if err != nil {
		t.Fatal(err)
	}
	if m.add(f, initial, sourceWatch{}) == nil {
		t.Fatal("registration error accepted")
	}
}
func TestSourceDarwinTransientUnboundNonEffect(t *testing.T) {
	s, root := sourceFixture(t)
	// Kqueue does not name directory writes. An unbound entry that is absent at
	// both endpoints and never enters a ZIP is outside the promised evidence.
	tmp := filepath.Join(root, "transient-not-packaged")
	mustWriteSource(t, tmp, "not packaged")
	if err := os.Remove(tmp); err != nil {
		t.Fatal(err)
	}
	sidecarChurn(t, root)
	if err := s.revalidate(context.Background()); err != nil {
		t.Fatal(err)
	}
}
