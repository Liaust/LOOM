//go:build linux

package notesworkspace

import (
	"encoding/binary"
	"fmt"
	"maps"
	"sort"
)

const accessACL = "system.posix_acl_access"
const aclUndefined = ^uint32(0)

type accessEntry struct {
	tag, permission uint16
	id              uint32
}

// Atomic saves may replace an admitted writable file without permission to
// chown/chgrp. Preserve effective access using named old-owner/group entries.
// The replacement owner gets only the writer's already-authorised permissions;
// masked bits cannot spring to life when the new ACL mask is widened. The old
// inode and its original ownership/metadata remain untouched in recovery custody.
func writerOwnedMetadata(original replacementMetadata, writer, group uint32, groups []int) (replacementMetadata, error) {
	if group == aclUndefined {
		return replacementMetadata{}, fmt.Errorf("unsupported replacement group")
	}
	entries, mask, err := replacementACL(original)
	if err != nil {
		return replacementMetadata{}, err
	}
	writerPerm, err := writerACLPermissions(entries, mask, original, writer, groups)
	if err != nil {
		return replacementMetadata{}, err
	}
	if writerPerm&2 == 0 {
		return replacementMetadata{}, fmt.Errorf("replacement writer lacks source write access")
	}
	oldOwner, oldGroup, other := uint16(original.Mode>>6&7), uint16(0), uint16(original.Mode&7)
	for _, e := range entries {
		if e.tag == 4 {
			oldGroup = e.permission & mask
		}
	}
	newGroup := oldGroup
	if group != original.GID {
		// A new owning group matches even users who previously fell through
		// to other. Use its old named entry if present. Otherwise other is
		// equivalent only when it cannot broaden any matching old group.
		newGroup = other
		named := false
		for _, e := range entries {
			if e.tag == 8 && e.id == group {
				newGroup, named = e.permission&mask, true
			}
		}
		if !named {
			for _, e := range entries {
				if (e.tag == 4 || e.tag == 8) && e.permission&mask&other != other {
					return replacementMetadata{}, fmt.Errorf("replacement group cannot preserve other access without broadening a source group")
				}
			}
		}
	}
	nextMask := mask
	if writer != original.UID {
		nextMask |= oldOwner
	}
	next := make([]accessEntry, 0, len(entries)+3)
	oldGroupEntry := false
	for _, e := range entries {
		if e.tag == 16 || writer != original.UID && e.tag == 2 && e.id == original.UID {
			continue
		}
		switch e.tag {
		case 1:
			e.permission = writerPerm
		case 2, 8:
			e.permission &= mask
			if group != original.GID && e.tag == 8 && e.id == original.GID {
				// Both entries used to match the same group. Combining split
				// rw permissions could newly allow a read-write open, so hold
				// unless one of the existing grants already covers the other.
				if e.permission|oldGroup != e.permission && e.permission|oldGroup != oldGroup {
					return replacementMetadata{}, fmt.Errorf("replacement cannot merge split source group access")
				}
				e.permission |= oldGroup
				oldGroupEntry = true
			}
		case 4:
			e.permission = newGroup
		}
		next = append(next, e)
	}
	if writer != original.UID {
		next = append(next, accessEntry{2, oldOwner, original.UID})
	}
	if group != original.GID && !oldGroupEntry {
		next = append(next, accessEntry{8, oldGroup, original.GID})
	}
	next = append(next, accessEntry{16, nextMask, aclUndefined})
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
	result.UID, result.GID = writer, group
	result.Mode = uint32(writerPerm)<<6 | uint32(nextMask)<<3 | uint32(other)
	result.Xattrs = maps.Clone(original.Xattrs)
	if result.Xattrs == nil {
		result.Xattrs = map[string][]byte{}
	}
	result.Xattrs[accessACL] = raw
	return result, nil
}

func replacementACL(original replacementMetadata) ([]accessEntry, uint16, error) {
	if original.UID == aclUndefined || original.GID == aclUndefined || original.Mode&^0777 != 0 {
		return nil, 0, fmt.Errorf("unsupported replacement ownership or mode")
	}
	mask := uint16(original.Mode >> 3 & 7)
	entries := []accessEntry{{1, uint16(original.Mode >> 6 & 7), aclUndefined}, {4, mask, aclUndefined}, {32, uint16(original.Mode & 7), aclUndefined}}
	raw, ok := original.Xattrs[accessACL]
	if !ok {
		return entries, mask, nil
	}
	if len(raw) < 28 || len(raw) > maxMetadataBytes || (len(raw)-4)%8 != 0 || binary.LittleEndian.Uint32(raw) != 2 {
		return nil, 0, fmt.Errorf("unsupported Linux access ACL encoding")
	}
	entries = nil
	seen := map[[2]uint32]bool{}
	for offset := 4; offset < len(raw); offset += 8 {
		e := accessEntry{binary.LittleEndian.Uint16(raw[offset:]), binary.LittleEndian.Uint16(raw[offset+2:]), binary.LittleEndian.Uint32(raw[offset+4:])}
		key := [2]uint32{uint32(e.tag), e.id}
		if e.permission > 7 || seen[key] {
			return nil, 0, fmt.Errorf("invalid Linux access ACL entry")
		}
		seen[key] = true
		switch e.tag {
		case 1, 4, 16, 32:
			if e.id != aclUndefined {
				return nil, 0, fmt.Errorf("invalid Linux access ACL qualifier")
			}
		case 2, 8:
			if e.id == aclUndefined {
				return nil, 0, fmt.Errorf("missing Linux access ACL qualifier")
			}
		default:
			return nil, 0, fmt.Errorf("unsupported Linux access ACL tag")
		}
		if len(entries) > 0 {
			previous := entries[len(entries)-1]
			if e.tag < previous.tag || e.tag == previous.tag && e.id <= previous.id {
				return nil, 0, fmt.Errorf("unordered Linux access ACL")
			}
		}
		if e.tag == 16 {
			mask = e.permission
		}
		entries = append(entries, e)
	}
	for _, tag := range []uint32{1, 4, 32} {
		if !seen[[2]uint32{tag, aclUndefined}] {
			return nil, 0, fmt.Errorf("incomplete Linux access ACL")
		}
	}
	for _, e := range entries {
		if (e.tag == 2 || e.tag == 8) && !seen[[2]uint32{16, aclUndefined}] {
			return nil, 0, fmt.Errorf("missing Linux access ACL mask")
		}
		if e.tag == 4 && !seen[[2]uint32{16, aclUndefined}] {
			mask = e.permission
		}
		if e.tag == 1 && uint32(e.permission) != original.Mode>>6&7 || e.tag == 32 && uint32(e.permission) != original.Mode&7 {
			return nil, 0, fmt.Errorf("Linux access ACL disagrees with source mode")
		}
	}
	if uint32(mask) != original.Mode>>3&7 {
		return nil, 0, fmt.Errorf("Linux access ACL mask disagrees with source mode")
	}
	return entries, mask, nil
}

func writerACLPermissions(entries []accessEntry, mask uint16, original replacementMetadata, writer uint32, groups []int) (uint16, error) {
	if writer == aclUndefined {
		return 0, fmt.Errorf("unsupported replacement writer")
	}
	if writer == original.UID {
		return uint16(original.Mode >> 6 & 7), nil
	}
	for _, e := range entries {
		if e.tag == 2 && e.id == writer {
			return e.permission & mask, nil
		}
	}
	memberships := make(map[uint32]bool, len(groups))
	for _, group := range groups {
		if group >= 0 && uint64(group) < uint64(aclUndefined) {
			memberships[uint32(group)] = true
		}
	}
	matched := []uint16{}
	var combined uint16
	for _, e := range entries {
		if e.tag != 4 && e.tag != 8 {
			continue
		}
		id := e.id
		if e.tag == 4 {
			id = original.GID
		}
		if memberships[id] {
			permission := e.permission & mask
			matched = append(matched, permission)
			combined |= permission
		}
	}
	if len(matched) == 0 {
		return uint16(original.Mode & 7), nil
	}
	for _, permission := range matched {
		if permission == combined {
			return combined, nil
		}
	}
	return 0, fmt.Errorf("replacement owner cannot preserve split writer group access")
}
