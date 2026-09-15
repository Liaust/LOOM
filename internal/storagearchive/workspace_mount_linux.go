package storagearchive

import (
	"fmt"

	"golang.org/x/sys/unix"
)

// Device identity alone does not distinguish bind mounts of one filesystem.
func sameWorkspaceMounts(fds ...int) error {
	var mountID uint64
	for i, fd := range fds {
		var st unix.Statx_t
		if err := unix.Statx(fd, "", unix.AT_EMPTY_PATH|unix.AT_SYMLINK_NOFOLLOW, unix.STATX_MNT_ID, &st); err != nil {
			return fmt.Errorf("inspect workspace mount identity: %w", err)
		}
		if st.Mask&unix.STATX_MNT_ID == 0 || st.Mnt_id == 0 {
			return fmt.Errorf("workspace mount identity is unavailable")
		}
		if i > 0 && st.Mnt_id != mountID {
			return fmt.Errorf("workspace move crosses a mount boundary")
		}
		mountID = st.Mnt_id
	}
	return nil
}
