//go:build linux

package hermesprofile

import (
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func inotifyFixtureEvent(wd int32, mask uint32, name string) []byte {
	if name != "" {
		name += "\x00"
	}
	b := make([]byte, 16+len(name))
	binary.NativeEndian.PutUint32(b, uint32(wd))
	binary.NativeEndian.PutUint32(b[4:], mask)
	binary.NativeEndian.PutUint32(b[12:], uint32(len(name)))
	copy(b[16:], name)
	return b
}
func TestSourceMonitorLinuxEventLoss(t *testing.T) {
	for _, raw := range [][]byte{inotifyFixtureEvent(-1, unix.IN_Q_OVERFLOW, ""), inotifyFixtureEvent(1, unix.IN_IGNORED, ""), inotifyFixtureEvent(1, unix.IN_UNMOUNT, ""), inotifyFixtureEvent(1, unix.IN_MOVE_SELF, ""), inotifyFixtureEvent(1, unix.IN_DELETE_SELF, ""), inotifyFixtureEvent(1, unix.IN_ATTRIB, ""), inotifyFixtureEvent(2, unix.IN_MODIFY, ""), inotifyFixtureEvent(1, 0, ""), {1, 2, 3}, append(inotifyFixtureEvent(1, unix.IN_MODIFY, ""), 0)} {
		m := &inotifyMonitor{watches: map[int32]sourceWatch{1: {kind: watchDatabase}}, changed: map[*os.File]uint64{}}
		if m.events(raw, &sourceTree{}) == nil {
			t.Fatalf("lost/invalid event accepted: %x", raw)
		}
	}
	m := &inotifyMonitor{fd: -1}
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

func TestSourceLinuxTransientExternalLogLinkNonEffect(t *testing.T) {
	s, root := sourceFixture(t)
	link := filepath.Join(t.TempDir(), "outside-watched-profile")
	if err := os.Link(filepath.Join(root, "logs/agent.log"), link); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	// Linux's IN_ATTRIB is also emitted by Hermes's same-mode chmod. No
	// substituted pathname or external inode bytes can enter the source here.
	if err := s.revalidate(context.Background()); err != nil {
		t.Fatal(err)
	}
}
