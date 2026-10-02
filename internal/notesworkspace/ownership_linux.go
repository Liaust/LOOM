//go:build linux

package notesworkspace

import (
	"encoding/binary"
	"fmt"
	"maps"
	"sort"
)

const accessACL = "system.posix_acl_access"

type accessEntry struct {
	tag, permission uint16
	id              uint32
}

// Atomic saves can replace a file in an admitted writable directory without
// permission to chown it. Retain the old owner's access using a named ACL entry.
// Clamp masked entries before widening the mask, so no other user/group gains
// permissions. The old inode's metadata remains untouched in recovery custody.
func writerOwnedMetadata(original replacementMetadata, writer uint32) (replacementMetadata, error) {
	const undefined = ^uint32(0)
	entries := []accessEntry{{1, uint16(original.Mode >> 6 & 7), undefined}, {4, uint16(original.Mode >> 3 & 7), undefined}, {32, uint16(original.Mode & 7), undefined}}
	mask := uint16(original.Mode >> 3 & 7)
	if raw, ok := original.Xattrs[accessACL]; ok {
		if len(raw) < 28 || (len(raw)-4)%8 != 0 || binary.LittleEndian.Uint32(raw) != 2 {
			return replacementMetadata{}, fmt.Errorf("unsupported Linux access ACL encoding")
		}
		entries = nil
		seen := map[[2]uint32]bool{}
		for offset := 4; offset < len(raw); offset += 8 {
			e := accessEntry{binary.LittleEndian.Uint16(raw[offset:]), binary.LittleEndian.Uint16(raw[offset+2:]), binary.LittleEndian.Uint32(raw[offset+4:])}
			key := [2]uint32{uint32(e.tag), e.id}
			if e.permission > 7 || seen[key] {
				return replacementMetadata{}, fmt.Errorf("invalid Linux access ACL entry")
			}
			seen[key] = true
			switch e.tag {
			case 1, 4, 16, 32:
				if e.id != undefined {
					return replacementMetadata{}, fmt.Errorf("invalid Linux access ACL qualifier")
				}
			case 2, 8:
				if e.id == undefined {
					return replacementMetadata{}, fmt.Errorf("missing Linux access ACL qualifier")
				}
			default:
				return replacementMetadata{}, fmt.Errorf("unsupported Linux access ACL tag")
			}
			if e.tag == 16 {
				mask = e.permission
			}
			entries = append(entries, e)
		}
		for _, tag := range []uint32{1, 4, 32} {
			if !seen[[2]uint32{tag, undefined}] {
				return replacementMetadata{}, fmt.Errorf("incomplete Linux access ACL")
			}
		}
		for _, e := range entries {
			if (e.tag == 2 || e.tag == 8) && !seen[[2]uint32{16, undefined}] {
				return replacementMetadata{}, fmt.Errorf("missing Linux access ACL mask")
			}
		}
	}
	owner := uint16(original.Mode >> 6 & 7)
	next := make([]accessEntry, 0, len(entries)+2)
	for _, e := range entries {
		if e.tag == 16 || e.tag == 2 && e.id == original.UID {
			continue
		}
		if e.tag == 2 || e.tag == 4 || e.tag == 8 {
			e.permission &= mask
		}
		next = append(next, e)
	}
	next = append(next, accessEntry{2, owner, original.UID}, accessEntry{16, mask | owner, undefined})
	sort.Slice(next, func(i, j int) bool {
		if next[i].tag == next[j].tag {
			return next[i].id < next[j].id
		}
		return next[i].tag < next[j].tag
	})
	raw := make([]byte, 4+8*len(next))
	binary.LittleEndian.PutUint32(raw, 2)
	for i, e := range next {
		offset := 4 + 8*i
		binary.LittleEndian.PutUint16(raw[offset:], e.tag)
		binary.LittleEndian.PutUint16(raw[offset+2:], e.permission)
		binary.LittleEndian.PutUint32(raw[offset+4:], e.id)
	}
	result := original
	result.UID = writer
	result.Mode = original.Mode&^0070 | uint32(mask|owner)<<3
	result.Xattrs = maps.Clone(original.Xattrs)
	if result.Xattrs == nil {
		result.Xattrs = map[string][]byte{}
	}
	result.Xattrs[accessACL] = raw
	return result, nil
}
