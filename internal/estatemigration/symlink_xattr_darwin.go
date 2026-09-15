//go:build darwin

package estatemigration

import "golang.org/x/sys/unix"

func requireOnlyPlatformProvenanceSymlinkAt(parentFD int, name string, expected publicationFileIdentity) error {
	fd, err := unix.Openat(parentFD, name, unix.O_RDONLY|unix.O_SYMLINK|unix.O_CLOEXEC, 0)
	if err != nil {
		return ErrVerificationFailed
	}
	defer unix.Close(fd)
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil || !sameObjectIdentity(expected, identityFromStat(stat)) {
		return ErrPublicationUncertain
	}
	if err := requireOnlyPlatformProvenance(fd); err != nil {
		return err
	}
	return verifyNamedIdentity(parentFD, name, expected)
}
