//go:build darwin

package lane

import (
	"os"

	"golang.org/x/sys/unix"
)

func cloneRegularFile(src, dst string, mode os.FileMode) (bool, error) {
	if err := unix.Clonefile(src, dst, unix.CLONE_NOFOLLOW); err != nil {
		_ = os.Remove(dst)
		return false, nil
	}
	if err := os.Chmod(dst, mode); err != nil {
		return true, err
	}
	return true, nil
}
