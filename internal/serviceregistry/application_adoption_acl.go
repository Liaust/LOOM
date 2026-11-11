package serviceregistry

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// ACL snapshots contain numeric POSIX entries only, never getfacl headers,
// filenames or restore directives. A mask expansion must not unmask old rights.
type applicationAdoptionACL map[string]uint8

func applicationAdoptionParseACL(raw string, directory bool) (applicationAdoptionACL, error) {
	if len(raw) > 16384 {
		return nil, applicationError("adoption.acl_limit")
	}
	a := applicationAdoptionACL{}
	for _, line := range strings.Split(strings.TrimSpace(raw), "\n") {
		if line == "" {
			continue
		}
		parts := strings.Split(line, ":")
		def := len(parts) == 4 && parts[0] == "default"
		if def {
			parts = parts[1:]
		}
		if len(parts) != 3 || (def && !directory) {
			return nil, applicationError("adoption.acl_invalid")
		}
		tag, id := parts[0], parts[1]
		if tag != "user" && tag != "group" && tag != "mask" && tag != "other" {
			return nil, applicationError("adoption.acl_invalid")
		}
		if id != "" {
			n, err := strconv.ParseUint(id, 10, 32)
			if err != nil || strconv.FormatUint(n, 10) != id || (tag != "user" && tag != "group") {
				return nil, applicationError("adoption.acl_invalid")
			}
		}
		p, ok := applicationAdoptionParsePerm(parts[2])
		if !ok {
			return nil, applicationError("adoption.acl_invalid")
		}
		key := tag + ":" + id
		if def {
			key = "default:" + key
		}
		if _, exists := a[key]; exists || len(a) >= 128 {
			return nil, applicationError("adoption.acl_invalid")
		}
		a[key] = p
	}
	for _, prefix := range []string{"", "default:"} {
		present := prefix == ""
		named := false
		for k := range a {
			if strings.HasPrefix(k, "default:") != (prefix != "") {
				continue
			}
			present = true
			k = strings.TrimPrefix(k, prefix)
			if (strings.HasPrefix(k, "user:") && k != "user:") || (strings.HasPrefix(k, "group:") && k != "group:") {
				named = true
			}
		}
		if !present {
			continue
		}
		for _, tag := range []string{"user:", "group:", "other:"} {
			if _, ok := a[prefix+tag]; !ok {
				return nil, applicationError("adoption.acl_invalid")
			}
		}
		if _, ok := a[prefix+"mask:"]; named && !ok {
			return nil, applicationError("adoption.acl_invalid")
		}
	}
	return a, nil
}

func applicationAdoptionParsePerm(s string) (uint8, bool) {
	if len(s) != 3 {
		return 0, false
	}
	var p uint8
	for i, c := range []byte("rwx") {
		if s[i] == c {
			p |= 1 << (2 - i)
		} else if s[i] != '-' {
			return 0, false
		}
	}
	return p, true
}

func applicationAdoptionPerm(p uint8) string {
	b := []byte("---")
	for i, c := range []byte("rwx") {
		if p&(1<<(2-i)) != 0 {
			b[i] = c
		}
	}
	return string(b)
}

func (a applicationAdoptionACL) text() string {
	keys := make([]string, 0, len(a))
	for k := range a {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	lines := make([]string, 0, len(keys))
	for _, k := range keys {
		lines = append(lines, k+":"+applicationAdoptionPerm(a[k]))
	}
	return strings.Join(lines, "\n") + "\n"
}

func (a applicationAdoptionACL) mode() uint32 {
	group := a["group:"]
	if m, ok := a["mask:"]; ok {
		group = m
	}
	return uint32(a["user:"])<<6 | uint32(group)<<3 | uint32(a["other:"])
}

func (a applicationAdoptionACL) access(uid, owner uint32, gids []uint32, group uint32) uint8 {
	if uid == owner {
		return a["user:"]
	}
	mask := uint8(7)
	if m, ok := a["mask:"]; ok {
		mask = m
	}
	if p, ok := a[fmt.Sprintf("user:%d", uid)]; ok {
		return p & mask
	}
	var p uint8
	matched := false
	for _, gid := range gids {
		if gid == group {
			p |= a["group:"]
			matched = true
		}
		if g, ok := a[fmt.Sprintf("group:%d", gid)]; ok {
			p |= g
			matched = true
		}
	}
	if matched {
		return p & mask
	}
	return a["other:"]
}

func (a applicationAdoptionACL) grant(prefix string, uid uint32, perm uint8) error {
	key := fmt.Sprintf("%suser:%d", prefix, uid)
	if p, exists := a[key]; exists && p != perm {
		return applicationError("adoption.acl_principal_conflict")
	}
	mask, exists := a[prefix+"mask:"]
	if !exists {
		mask = a[prefix+"group:"]
	}
	next := mask | perm
	for k, p := range a {
		if strings.HasPrefix(k, "default:") != (prefix != "") || k == key {
			continue
		}
		tag := strings.TrimPrefix(k, prefix)
		masked := strings.HasPrefix(tag, "group:") || (strings.HasPrefix(tag, "user:") && tag != "user:")
		if masked && p&next != p&mask {
			return applicationError("adoption.acl_mask_conflict")
		}
	}
	a[key], a[prefix+"mask:"] = perm, next
	return nil
}

// Freeze the mask's current effect into access entries before expanding it for
// a managed principal. Dormant foreign permissions stay dormant, not unmasked.
func (a applicationAdoptionACL) preserveEffectiveAccess() {
	mask, exists := a["mask:"]
	if !exists {
		return
	}
	for key, perm := range a {
		if strings.HasPrefix(key, "group:") || strings.HasPrefix(key, "user:") && key != "user:" {
			a[key] = perm & mask
		}
	}
}

func applicationAdoptionDesiredACL(before string, role string, id ApplicationStatePathIdentity, q ApplicationStateAdoptionRequest) (string, error) {
	a, err := applicationAdoptionParseACL(before, id.Directory)
	if err != nil {
		return "", err
	}
	if role == "parent" {
		current := a.access(q.ApplicationUID, id.UID, q.ApplicationGroups, id.GID)
		if current&1 != 0 {
			return a.text(), nil
		}
		a.preserveEffectiveAccess()
		delete(a, fmt.Sprintf("user:%d", q.ApplicationUID))
		if err = a.grant("", q.ApplicationUID, current|1); err != nil {
			return "", err
		}
		return a.text(), nil
	}
	perm := uint8(6)
	if id.Directory {
		perm = 7
	}
	if a["user:"]&perm != perm {
		return "", applicationError("adoption.project_owner_access")
	}
	if q.NormalizePrivateDefaults {
		a.preserveEffectiveAccess()
		delete(a, fmt.Sprintf("user:%d", q.ApplicationUID))
	}
	if err = a.grant("", q.ApplicationUID, perm); err != nil {
		return "", err
	}
	if id.Directory {
		if q.NormalizePrivateDefaults {
			for key := range a {
				if strings.HasPrefix(key, "default:") {
					delete(a, key)
				}
			}
		}
		if _, exists := a["default:user:"]; !exists {
			// Private defaults: newly app-owned files retain the project owner's
			// access without granting the owning group or other users new rights.
			a["default:user:"], a["default:group:"], a["default:other:"] = 7, 0, 0
		}
		if a["default:user:"] != 7 {
			return "", applicationError("adoption.default_owner_access")
		}
		if err = a.grant("default:", q.ProjectUID, 7); err != nil {
			return "", err
		}
		if err = a.grant("default:", q.ApplicationUID, 7); err != nil {
			return "", err
		}
	}
	return a.text(), nil
}
