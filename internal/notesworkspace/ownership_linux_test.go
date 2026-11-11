//go:build linux

package notesworkspace

import (
	"encoding/binary"
	"reflect"
	"strings"
	"testing"
)

func aclBytes(entries ...accessEntry) []byte {
	raw := make([]byte, 4+8*len(entries))
	binary.LittleEndian.PutUint32(raw, 2)
	for i, e := range entries {
		offset := 4 + 8*i
		binary.LittleEndian.PutUint16(raw[offset:], e.tag)
		binary.LittleEndian.PutUint16(raw[offset+2:], e.permission)
		binary.LittleEndian.PutUint32(raw[offset+4:], e.id)
	}
	return raw
}

func aclFixture(mode uint32, entries ...accessEntry) replacementMetadata {
	m := replacementMetadata{UID: 1000, GID: 100, Mode: mode, Xattrs: map[string][]byte{"user.retained": []byte("original")}}
	if len(entries) > 0 {
		m.Xattrs[accessACL] = aclBytes(entries...)
	}
	return m
}

// Model the kernel's requested-permissions check, not a union of matching
// groups: two separate r and w grants do not permit an O_RDWR open.
func aclAllows(t *testing.T, m replacementMetadata, uid uint32, groups []int, want uint16) bool {
	t.Helper()
	entries, mask, err := replacementACL(m)
	if err != nil {
		t.Fatal(err)
	}
	if uid == m.UID {
		return uint16(m.Mode>>6&7)&want == want
	}
	for _, e := range entries {
		if e.tag == 2 && e.id == uid {
			return e.permission&mask&want == want
		}
	}
	matched := false
	for _, e := range entries {
		if e.tag != 4 && e.tag != 8 {
			continue
		}
		id := e.id
		if e.tag == 4 {
			id = m.GID
		}
		for _, g := range groups {
			if g >= 0 && uint32(g) == id {
				matched = true
				if e.permission&mask&want == want {
					return true
				}
			}
		}
	}
	return !matched && uint16(m.Mode&7)&want == want
}

func TestWriterOwnedTranslationPreservesEffectiveAccess(t *testing.T) {
	const undef = aclUndefined
	for _, tc := range []struct {
		name     string
		original replacementMetadata
		writer   uint32
		group    uint32
		groups   []int
	}{
		{"legacy_owner_and_group", aclFixture(0674,
			accessEntry{1, 6, undef}, accessEntry{2, 6, 990}, accessEntry{4, 7, undef}, accessEntry{16, 7, undef}, accessEntry{32, 4, undef}), 990, 986, []int{986}},
		{"mode_only_both_identities", aclFixture(0660), 990, 986, []int{100, 986}},
		{"retain_real_group", aclFixture(0760), 990, 100, []int{100, 986}},
		{"group_only_same_owner", aclFixture(0640), 1000, 986, []int{986}},
		{"writer_must_not_inherit_owner_execute", aclFixture(0760,
			accessEntry{1, 7, undef}, accessEntry{2, 6, 990}, accessEntry{2, 0, 1000}, accessEntry{2, 7, 2000}, accessEntry{4, 7, undef}, accessEntry{8, 7, 200}, accessEntry{16, 6, undef}, accessEntry{32, 0, undef}), 990, 986, []int{986}},
		{"existing_new_group_restricted", aclFixture(0674,
			accessEntry{1, 6, undef}, accessEntry{2, 6, 990}, accessEntry{4, 7, undef}, accessEntry{8, 0, 986}, accessEntry{16, 7, undef}, accessEntry{32, 4, undef}), 990, 986, []int{986}},
		{"existing_old_group_entry", aclFixture(0670,
			accessEntry{1, 6, undef}, accessEntry{2, 6, 990}, accessEntry{4, 6, undef}, accessEntry{8, 4, 100}, accessEntry{16, 7, undef}, accessEntry{32, 0, undef}), 990, 986, []int{986}},
		{"writer_via_other", aclFixture(0666), 990, 986, []int{986}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := aclFixture(tc.original.Mode)
			for key, value := range tc.original.Xattrs {
				before.Xattrs[key] = append([]byte(nil), value...)
			}
			got, err := writerOwnedMetadata(tc.original, tc.writer, tc.group, tc.groups)
			if err != nil {
				t.Fatal(err)
			}
			if got.UID != tc.writer || got.GID != tc.group || !reflect.DeepEqual(before, tc.original) || !reflect.DeepEqual(got.Xattrs["user.retained"], before.Xattrs["user.retained"]) {
				t.Fatalf("ownership/xattr preservation failed: %+v", got)
			}
			again, err := writerOwnedMetadata(got, tc.writer, tc.group, tc.groups)
			if err != nil || !reflect.DeepEqual(got, again) {
				t.Fatalf("translation replay changed access: %+v %v", again, err)
			}
			for _, uid := range []uint32{990, 1000, 2000, 3000} {
				for bits := 0; bits < 16; bits++ {
					groups := []int{}
					for i, gid := range []int{100, 200, 986, 300} {
						if bits&(1<<i) != 0 {
							groups = append(groups, gid)
						}
					}
					if uid == tc.writer {
						groups = tc.groups
					}
					for want := uint16(1); want <= 7; want++ {
						if aclAllows(t, before, uid, groups, want) != aclAllows(t, got, uid, groups, want) {
							t.Fatalf("changed access uid=%d groups=%v requested=%o before=%+v after=%+v", uid, groups, want, before, got)
						}
					}
				}
			}
		})
	}
}

func TestWriterOwnedTranslationHoldsUnrepresentableAccess(t *testing.T) {
	const undef = aclUndefined
	for _, tc := range []struct {
		name, reason string
		original     replacementMetadata
		groups       []int
	}{
		{"writer_read_only", "lacks source write", aclFixture(0640), []int{100, 986}},
		{"named_writer_overrides_group", "lacks source write", aclFixture(0670,
			accessEntry{1, 6, undef}, accessEntry{2, 4, 990}, accessEntry{4, 7, undef}, accessEntry{16, 7, undef}, accessEntry{32, 0, undef}), []int{100, 986}},
		{"group_match_blocks_other", "lacks source write", aclFixture(0606), []int{100, 986}},
		{"writer_split_group_access", "split writer", aclFixture(0670,
			accessEntry{1, 6, undef}, accessEntry{4, 4, undef}, accessEntry{8, 2, 200}, accessEntry{16, 7, undef}, accessEntry{32, 0, undef}), []int{100, 200, 986}},
		{"old_group_split_entries", "split source group", aclFixture(0670,
			accessEntry{1, 6, undef}, accessEntry{2, 6, 990}, accessEntry{4, 4, undef}, accessEntry{8, 2, 100}, accessEntry{16, 7, undef}, accessEntry{32, 0, undef}), []int{986}},
		{"other_would_broaden_group", "without broadening", aclFixture(0674,
			accessEntry{1, 6, undef}, accessEntry{2, 6, 990}, accessEntry{4, 0, undef}, accessEntry{16, 7, undef}, accessEntry{32, 4, undef}), []int{986}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := writerOwnedMetadata(tc.original, 990, 986, tc.groups)
			if err == nil || !strings.Contains(err.Error(), tc.reason) {
				t.Fatalf("expected %q hold, got %v", tc.reason, err)
			}
		})
	}
}

func TestWriterOwnedTranslationRejectsMalformedACL(t *testing.T) {
	const undef = aclUndefined
	valid := []accessEntry{{1, 6, undef}, {2, 6, 990}, {4, 6, undef}, {16, 6, undef}, {32, 0, undef}}
	for _, tc := range []struct {
		name string
		edit func([]accessEntry) []accessEntry
	}{
		{"duplicate", func(e []accessEntry) []accessEntry { return append(e[:2], append([]accessEntry{e[1]}, e[2:]...)...) }},
		{"invalid_permission", func(e []accessEntry) []accessEntry { e[1].permission = 8; return e }},
		{"invalid_tag", func(e []accessEntry) []accessEntry { e[1].tag = 3; return e }},
		{"missing_qualifier", func(e []accessEntry) []accessEntry { e[1].id = undef; return e }},
		{"unexpected_qualifier", func(e []accessEntry) []accessEntry { e[0].id = 990; return e }},
		{"missing_owner", func(e []accessEntry) []accessEntry { return e[1:] }},
		{"missing_group", func(e []accessEntry) []accessEntry { return append(e[:2], e[3:]...) }},
		{"missing_other", func(e []accessEntry) []accessEntry { return e[:len(e)-1] }},
		{"missing_mask", func(e []accessEntry) []accessEntry { return append(e[:3], e[4:]...) }},
		{"mode_disagrees_owner", func(e []accessEntry) []accessEntry { e[0].permission = 7; return e }},
		{"mode_disagrees_mask", func(e []accessEntry) []accessEntry { e[3].permission = 7; return e }},
		{"mode_disagrees_other", func(e []accessEntry) []accessEntry { e[4].permission = 4; return e }},
		{"unordered", func(e []accessEntry) []accessEntry { e[1], e[2] = e[2], e[1]; return e }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := aclFixture(0660)
			m.Xattrs[accessACL] = aclBytes(tc.edit(append([]accessEntry(nil), valid...))...)
			if _, err := writerOwnedMetadata(m, 990, 986, []int{986}); err == nil {
				t.Fatal("accepted malformed ACL")
			}
		})
	}
}
