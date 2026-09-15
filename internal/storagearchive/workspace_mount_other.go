//go:build !linux

package storagearchive

import (
	"fmt"

	"golang.org/x/sys/unix"
)

func sameWorkspaceMounts(fds ...int) error {
	var device uint64
	for i, fd := range fds {
		var st unix.Stat_t
		if err := unix.Fstat(fd, &st); err != nil {
			return fmt.Errorf("inspect workspace filesystem identity: %w", err)
		}
		if i > 0 && uint64(st.Dev) != device {
			return fmt.Errorf("workspace move crosses a filesystem boundary")
		}
		device = uint64(st.Dev)
	}
	return nil
}
