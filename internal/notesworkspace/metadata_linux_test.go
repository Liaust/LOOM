//go:build linux

package notesworkspace

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"golang.org/x/sys/unix"
)

func TestLinuxAccessACLPreserved(t *testing.T) {
	s, _, r := fixture(t)
	name := filepath.Join(r.root, "note.md")
	write(t, name, "A")
	// Linux POSIX ACL xattr v2: owner, named reader, group, mask, other.
	raw := new(bytes.Buffer)
	for _, value := range []any{uint32(2), uint16(1), uint16(6), uint32(0xffffffff), uint16(2), uint16(4), uint32(12345), uint16(4), uint16(4), uint32(0xffffffff), uint16(16), uint16(4), uint32(0xffffffff), uint16(32), uint16(0), uint32(0xffffffff)} {
		if err := binary.Write(raw, binary.LittleEndian, value); err != nil {
			t.Fatal(err)
		}
	}
	if err := unix.Setxattr(name, "system.posix_acl_access", raw.Bytes(), 0); err != nil {
		t.Fatal(err)
	}
	receive(t, s, "acl", readBase(t, s, "note.md"), "B")
	apply(t, s, "acl", Accepted)
	actual := make([]byte, 128)
	n, err := unix.Getxattr(name, "system.posix_acl_access", actual)
	if err != nil || !bytes.Equal(actual[:n], raw.Bytes()) {
		t.Fatalf("ACL lost: %x %v", actual[:n], err)
	}
	contents(t, name, "B")
}

func TestNewNoteInheritsSourceDefaultACL(t *testing.T) {
	s, _, r := fixture(t)
	raw := new(bytes.Buffer)
	for _, value := range []any{uint32(2), uint16(1), uint16(7), uint32(0xffffffff), uint16(2), uint16(6), uint32(12345), uint16(4), uint16(4), uint32(0xffffffff), uint16(16), uint16(6), uint32(0xffffffff), uint16(32), uint16(0), uint32(0xffffffff)} {
		if err := binary.Write(raw, binary.LittleEndian, value); err != nil {
			t.Fatal(err)
		}
	}
	if err := unix.Setxattr(r.root, "system.posix_acl_default", raw.Bytes(), 0); err != nil {
		t.Fatal(err)
	}
	receive(t, s, "new-acl", readBase(t, s, "new.md"), "new note")
	op := apply(t, s, "new-acl", Accepted)
	info, err := os.Stat(filepath.Join(r.root, "new.md"))
	if err != nil || info.Mode().Perm() != 0660 {
		t.Fatalf("new access: %v %v", info, err)
	}
	actual := make([]byte, 128)
	n, err := unix.Getxattr(filepath.Join(r.root, "new.md"), accessACL, actual)
	if err != nil {
		t.Fatal(err)
	}
	// Normal 0666 creation removes execute from the owner/default ACL.
	binary.LittleEndian.PutUint16(raw.Bytes()[6:], 6)
	if !bytes.Equal(actual[:n], raw.Bytes()) {
		t.Fatalf("inherited ACL lost: %x", actual[:n])
	}
	journal, err := os.Stat(filepath.Join(r.root, op.Journal))
	if err != nil || journal.Mode().Perm() != 0700 {
		t.Fatalf("journal no longer private: %v %v", journal, err)
	}
	intent, err := os.Stat(filepath.Join(r.root, op.Journal, "intent"))
	if err != nil || intent.Mode().Perm() != 0600 {
		t.Fatalf("intent no longer private: %v %v", intent, err)
	}
}

func TestWriterOwnedReplacementKeepsAccess(t *testing.T) {
	for _, extended := range []bool{false, true} {
		t.Run(fmtBool(extended), func(t *testing.T) {
			original := replacementMetadata{UID: 12345, GID: uint32(os.Getegid()), Mode: 0640, Xattrs: map[string][]byte{"user.notes_workspace_test": []byte("retained")}}
			if extended {
				// Masked write bits must not spring to life when the owner's rw
				// entry requires a wider mask. Include an existing owner entry.
				raw := new(bytes.Buffer)
				for _, value := range []any{uint32(2), uint16(1), uint16(6), uint32(0xffffffff), uint16(2), uint16(0), uint32(12345), uint16(2), uint16(6), uint32(12346), uint16(4), uint16(6), uint32(0xffffffff), uint16(8), uint16(7), uint32(12347), uint16(16), uint16(4), uint32(0xffffffff), uint16(32), uint16(0), uint32(0xffffffff)} {
					if err := binary.Write(raw, binary.LittleEndian, value); err != nil {
						t.Fatal(err)
					}
				}
				original.Xattrs[accessACL] = raw.Bytes()
			}
			before := append([]byte(nil), original.Xattrs[accessACL]...)
			got, err := writerOwnedMetadata(original, uint32(os.Geteuid()))
			if err != nil {
				t.Fatal(err)
			}
			if got.UID != uint32(os.Geteuid()) || got.GID != original.GID || got.Mode != 0660 || !bytes.Equal(original.Xattrs[accessACL], before) {
				t.Fatalf("unexpected metadata or mutated original: %+v %v", got, err)
			}
			acl := got.Xattrs[accessACL]
			ownerEntries := 0
			for offset := 4; offset < len(acl); offset += 8 {
				tag, perm, id := binary.LittleEndian.Uint16(acl[offset:]), binary.LittleEndian.Uint16(acl[offset+2:]), binary.LittleEndian.Uint32(acl[offset+4:])
				if tag == 2 && id == original.UID {
					ownerEntries++
					if perm != 6 {
						t.Fatal("original owner lost access")
					}
				} else if tag == 2 || tag == 4 || tag == 8 {
					if perm != 4 {
						t.Fatalf("masked permissions expanded: %d %d", tag, perm)
					}
				}
			}
			if ownerEntries != 1 {
				t.Fatal("owner ACL not unique")
			}
			// Ask the actual kernel to accept and canonicalize the result.
			f, err := os.CreateTemp(t.TempDir(), "acl-")
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			if err := unix.Fchmod(int(f.Fd()), got.Mode); err != nil {
				t.Fatal(err)
			}
			for key, value := range got.Xattrs {
				if err := unix.Fsetxattr(int(f.Fd()), key, value, 0); err != nil {
					t.Fatal(err)
				}
			}
			actual, err := metadataAt(int(f.Fd()))
			if err != nil || !reflect.DeepEqual(got, actual) {
				t.Fatalf("kernel metadata mismatch: %+v %v", actual, err)
			}
		})
	}
}

func fmtBool(value bool) string {
	if value {
		return "extended"
	}
	return "basic"
}

func TestWriterOwnedReplacementRejectsMalformedACL(t *testing.T) {
	for _, raw := range [][]byte{{}, {2, 0, 0, 0}, make([]byte, 28)} {
		_, err := writerOwnedMetadata(replacementMetadata{UID: 12345, Mode: 0644, Xattrs: map[string][]byte{accessACL: raw}}, 1000)
		if err == nil {
			t.Fatal("accepted malformed access ACL")
		}
	}
}
