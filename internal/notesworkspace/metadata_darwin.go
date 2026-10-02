//go:build darwin

package notesworkspace

import (
	"encoding/binary"
	"fmt"
	"strings"
	"unsafe"

	"golang.org/x/sys/unix"
)

// Darwin ACLs are not ordinary listxattr entries. Query the descriptor's extended
// security attribute; until copying these ACLs is supported, refuse any ACL.
// Layout follows sys/attr.h attrlist/attrreference and sys/kauth.h kauth_filesec.
func checkPlatformMetadata(fd int, stat unix.Stat_t) error {
	if stat.Flags != 0 {
		return fmt.Errorf("unsupported Darwin file flags")
	}
	attrs := struct {
		Count, Reserved                 uint16
		Common, Volume, Dir, File, Fork uint32
	}{Count: 5, Common: unix.ATTR_CMN_EXTENDED_SECURITY}
	buffer := make([]byte, 4096)
	_, _, errno := unix.Syscall6(unix.SYS_FGETATTRLIST, uintptr(fd), uintptr(unsafe.Pointer(&attrs)), uintptr(unsafe.Pointer(&buffer[0])), uintptr(len(buffer)), 0, 0)
	if errno != 0 {
		return fmt.Errorf("inspect Darwin ACL: %w", errno)
	}
	length := binary.LittleEndian.Uint32(buffer[:4])
	if length < 12 || length > uint32(len(buffer)) {
		return fmt.Errorf("unsupported Darwin security attribute size")
	}
	size := binary.LittleEndian.Uint32(buffer[8:12])
	if size != 0 {
		return fmt.Errorf("Darwin extended ACL/security metadata requires explicit recovery")
	}
	return nil
}
func supportedXattr(name string) bool {
	// Security/ResourceFork and compressed/opaque system data are not editable text
	// metadata. Finder tags, quarantine and other ordinary attributes round-trip.
	return !strings.HasPrefix(name, "com.apple.system.") && name != "com.apple.ResourceFork" && name != "com.apple.decmpfs"
}

func writerOwnedMetadata(_ replacementMetadata, _ uint32) (replacementMetadata, error) {
	return replacementMetadata{}, fmt.Errorf("cross-owner replacement requires Linux access ACL support")
}
