package storagearchive

import (
	"fmt"

	"golang.org/x/sys/unix"
)

// Empty infrastructure directories are created by their service owner. This
// avoids tmpfiles crossing the operator-owned /srv/loom parent, and never
// changes an existing retained directory's permissions or contents.
func PrepareRetainedRoots(storage string) error {
	chain, err := openHeldDirectoryChain(storage, ".", nil)
	if err != nil {
		return err
	}
	defer chain.Close()
	for _, name := range []string{"Projects", "Library", "Records", ".loom"} {
		// Setgid is inherited from Storage. Explicit set-ID mode requests are
		// rejected by the real service's RestrictSUIDSGID syscall filter.
		if err = unix.Mkdirat(chain.ParentFD(), name, 0750); err != nil && err != unix.EEXIST {
			return fmt.Errorf("prepare retained %s: %w", name, err)
		}
		fd, err := unix.Openat(chain.ParentFD(), name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if err != nil {
			return fmt.Errorf("retained %s must be a real directory: %w", name, err)
		}
		_ = unix.Close(fd)
	}
	meta, err := openHeldDirectoryChain(storage, ".loom", nil)
	if err != nil {
		return err
	}
	defer meta.Close()
	if err = unix.Mkdirat(meta.ParentFD(), "workspace-archives", 0750); err != nil && err != unix.EEXIST {
		return err
	}
	archives, err := openHeldDirectoryChain(storage, ".loom/workspace-archives", nil)
	if err != nil {
		return err
	}
	archives.Close()
	if err = unix.Fsync(meta.ParentFD()); err != nil {
		return err
	}
	if err := chain.verifyNamed(); err != nil {
		return err
	}
	return unix.Fsync(chain.ParentFD())
}
