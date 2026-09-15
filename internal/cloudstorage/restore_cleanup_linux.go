package cloudstorage

import (
	"encoding/binary"
	"errors"
	"fmt"
	"golang.org/x/sys/unix"
)

func cleanupLeafFlags(mode uint32) int {
	if mode&unix.S_IFMT == unix.S_IFLNK {
		return unix.O_PATH | unix.O_NOFOLLOW | unix.O_CLOEXEC
	}
	return unix.O_RDONLY | unix.O_NOFOLLOW | unix.O_CLOEXEC | unix.O_NONBLOCK
}
func cleanupMountID(fd int) (string, error) {
	var st unix.Statx_t
	if err := unix.Statx(fd, "", unix.AT_EMPTY_PATH|unix.AT_SYMLINK_NOFOLLOW, unix.STATX_MNT_ID, &st); err != nil || st.Mask&unix.STATX_MNT_ID == 0 || st.Mnt_id == 0 {
		return "", fmt.Errorf("mount identity unavailable")
	}
	return fmt.Sprint(st.Mnt_id), nil
}

// POSIX regular-file write permission does not confer chmod/chown or parent
// entry replacement. The common caller still binds type, owner, nlink and stat.
func cleanupPayloadACL(int) error { return nil }
func cleanupDirectoryACL(fd int, allowStickyMode bool) error {
	var before, after unix.Stat_t
	if err := unix.Fstat(fd, &before); err != nil {
		return err
	}
	access, present, err := cleanupLinuxReadACL(fd, "system.posix_acl_access")
	if err != nil {
		return err
	}
	if present {
		authority, err := cleanupParsePOSIXACL(access, before.Uid, false)
		if err != nil || authority.mode != uint32(before.Mode)&0777 || authority.nonOwnerWrite {
			return fmt.Errorf("unsafe access ACL authority")
		}
	} else if uint32(before.Mode)&0022 != 0 && !allowStickyMode {
		return fmt.Errorf("unsafe non-owner mode authority")
	}
	if uint32(before.Mode)&unix.S_IFMT == unix.S_IFDIR {
		defaults, present, err := cleanupLinuxReadACL(fd, "system.posix_acl_default")
		if err != nil {
			return err
		}
		if present {
			authority, err := cleanupParsePOSIXACL(defaults, before.Uid, true)
			if err != nil || authority.nonOwnerWrite {
				return fmt.Errorf("unsafe default ACL authority")
			}
		}
	}
	if err := unix.Fstat(fd, &after); err != nil {
		return err
	}
	if before.Dev != after.Dev || before.Ino != after.Ino || before.Mode != after.Mode || before.Uid != after.Uid || before.Gid != after.Gid || before.Ctim != after.Ctim || before.Mtim != after.Mtim || before.Nlink != after.Nlink || before.Size != after.Size {
		return fmt.Errorf("ACL metadata changed while inspecting")
	}
	return nil
}
func cleanupLinuxReadACL(fd int, name string) ([]byte, bool, error) {
	n, err := unix.Fgetxattr(fd, name, nil)
	if errors.Is(err, unix.ENODATA) {
		return nil, false, nil
	}
	if err != nil || n < 4 || n > 65536 {
		return nil, false, fmt.Errorf("ACL inspection unavailable or oversized")
	}
	raw := make([]byte, n)
	got, err := unix.Fgetxattr(fd, name, raw)
	if err != nil || got != n {
		return nil, false, fmt.Errorf("ACL changed or truncated while reading")
	}
	return raw, true, nil
}

func cleanupRenameNoReplace(from int, name string, to int, dest string) error {
	return unix.Renameat2(from, name, to, dest, unix.RENAME_NOREPLACE)
}
func cleanupPrivateDirectoryACL(fd int) error { return cleanupDirectoryACL(fd, false) }
func cleanupReadPayloadDirACL(fd int) (cleanupACLState, error) {
	var acl cleanupACLState
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return acl, err
	}
	access, present, err := cleanupLinuxReadACL(fd, "system.posix_acl_access")
	if err != nil {
		return acl, err
	}
	if present {
		info, e := cleanupParsePOSIXACL(access, st.Uid, false)
		if e != nil || info.mode != uint32(st.Mode)&0777 {
			return acl, fmt.Errorf("invalid payload access ACL")
		}
		acl.Access = access
	}
	defaults, present, err := cleanupLinuxReadACL(fd, "system.posix_acl_default")
	if err != nil {
		return acl, err
	}
	if present {
		if _, e := cleanupParsePOSIXACL(defaults, st.Uid, true); e != nil {
			return acl, e
		}
		acl.Default = defaults
	}
	return acl, nil
}
func cleanupSealedACL(original cleanupACLState, mode uint32) cleanupACLState {
	result := cleanupACLState{Access: append([]byte(nil), original.Access...), Default: append([]byte(nil), original.Default...)}
	hasMask := false
	for n := 4; n < len(result.Access); n += 8 {
		if binary.LittleEndian.Uint16(result.Access[n:]) == 16 {
			hasMask = true
		}
	}
	for n := 4; n < len(result.Access); n += 8 {
		tag := binary.LittleEndian.Uint16(result.Access[n:])
		var perm uint16
		switch {
		case tag == 1:
			perm = uint16(mode>>6) & 7
		case tag == 16 || tag == 4 && !hasMask:
			perm = uint16(mode>>3) & 7
		case tag == 32:
			perm = uint16(mode) & 7
		default:
			continue
		}
		binary.LittleEndian.PutUint16(result.Access[n+2:], perm)
	}
	return result
}
