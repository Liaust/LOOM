package cloudstorage

import (
	"encoding/binary"
	"fmt"
	"unsafe"

	"golang.org/x/sys/unix"
)

func cleanupLeafFlags(mode uint32) int {
	if mode&unix.S_IFMT == unix.S_IFLNK {
		return unix.O_RDONLY | unix.O_SYMLINK | unix.O_CLOEXEC
	}
	return unix.O_RDONLY | unix.O_NOFOLLOW | unix.O_CLOEXEC | unix.O_NONBLOCK
}

func cleanupMountID(fd int) (string, error) {
	var st unix.Statfs_t
	if err := unix.Fstatfs(fd, &st); err != nil {
		return "", err
	}
	return fmt.Sprint(st.Fsid.Val), nil
}

func cleanupDirectoryACL(fd int, allowStickyMode bool) error {
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return err
	}
	if uint32(st.Mode)&unix.S_IFMT != unix.S_IFLNK && uint32(st.Mode)&0022 != 0 && !allowStickyMode {
		return fmt.Errorf("unsafe non-owner mode authority")
	}
	return cleanupDarwinACL(fd, false)
}
func cleanupPayloadACL(fd int) error { return cleanupDarwinACL(fd, true) }
func cleanupDarwinACL(fd int, contentWrites bool) error {
	return cleanupDarwinACLPolicy(fd, contentWrites, false)
}
func cleanupDarwinACLPolicy(fd int, contentWrites, private bool) error {
	// fgetattrlist's descriptor-bound extended-security attribute is a
	// kauth_filesec (sys/attr.h, sys/kauth.h). Refuse unknown/truncated formats
	// and all permit ACEs carrying mutation authority; deny-only ACLs such as
	// macOS's default home-directory delete denial do not grant a writer.
	attrs := unix.Attrlist{Bitmapcount: 5, Commonattr: unix.ATTR_CMN_EXTENDED_SECURITY}
	var buf [4096]byte // 44-byte filesec + at most 128 24-byte ACEs + header
	_, _, errno := unix.Syscall6(unix.SYS_FGETATTRLIST, uintptr(fd), uintptr(unsafe.Pointer(&attrs)), uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)), 0, 0)
	if errno != 0 {
		return fmt.Errorf("directory ACL inspection unavailable")
	}
	u32 := binary.NativeEndian.Uint32
	n := int(u32(buf[:4]))
	if n < 12 || n > len(buf) {
		return fmt.Errorf("invalid ACL attribute length")
	}
	offset, size := int(int32(u32(buf[4:8])))+4, int(u32(buf[8:12]))
	if size == 0 {
		return nil
	}
	if offset < 12 || size < 44 || offset > n-size {
		return fmt.Errorf("invalid ACL reference")
	}
	acl := buf[offset : offset+size]
	if u32(acl[:4]) != 0x012cc16d {
		return fmt.Errorf("invalid ACL magic")
	}
	count := u32(acl[36:40])
	if count == 0xffffffff {
		return nil
	}
	if count > 128 || size != 44+int(count)*24 {
		return fmt.Errorf("invalid ACL count")
	}
	mutation := uint32((1 << 2) | (1 << 4) | (1 << 5) | (1 << 6) | (1 << 8) | (1 << 10) | (1 << 12) | (1 << 13) | (1 << 21) | (1 << 23))
	if contentWrites {
		mutation &^= (1 << 2) | (1 << 5)
	}
	const knownRights = uint32(0x01f03ffe) // vnode rights 1..13, synchronize and generic rights 20..24
	for i := 0; i < int(count); i++ {
		ace := acl[44+i*24 : 44+(i+1)*24]
		kind, rights := u32(ace[16:20])&15, u32(ace[20:24])
		if (kind != 1 && kind != 2) || rights&^knownRights != 0 || (kind == 1 && (rights&mutation != 0 || private && rights != 0)) {
			return fmt.Errorf("unexpected directory ACL authority")
		}
	}
	return nil
}

func cleanupRenameNoReplace(from int, name string, to int, dest string) error {
	return unix.RenameatxNp(from, name, to, dest, unix.RENAME_EXCL)
}
func cleanupPrivateDirectoryACL(fd int) error { return cleanupDarwinACLPolicy(fd, false, true) }
func cleanupReadPayloadDirACL(fd int) (cleanupACLState, error) {
	return cleanupACLState{}, cleanupDarwinACL(fd, false)
}
func cleanupSealedACL(original cleanupACLState, mode uint32) cleanupACLState { return original }
