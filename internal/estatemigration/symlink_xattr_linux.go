//go:build linux

package estatemigration

import (
	"bytes"
	"strconv"

	"golang.org/x/sys/unix"
)

func requireOnlyPlatformProvenanceSymlinkAt(parentFD int, name string, expected publicationFileIdentity) error {
	if !validRawPathComponent(name) {
		return ErrInvalidManifest
	}
	// Linux has no fd-relative llistxattr syscall. /proc/self/fd binds the
	// intermediate directory to the already-open descriptor while Llistxattr
	// refuses to follow the final symlink component.
	boundPath := "/proc/self/fd/" + strconv.Itoa(parentFD) + "/" + name
	count, err := unix.Llistxattr(boundPath, nil)
	if err != nil {
		return ErrVerificationFailed
	}
	if count != 0 {
		buffer := make([]byte, count)
		count, err = unix.Llistxattr(boundPath, buffer)
		if err != nil {
			return ErrVerificationFailed
		}
		if err := validatePlatformProvenanceXattrList(bytes.TrimRight(buffer[:count], "\x00")); err != nil {
			return err
		}
	}
	return verifyNamedIdentity(parentFD, name, expected)
}
