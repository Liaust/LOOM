//go:build linux

package hermesprofile

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

func openDirectoryPathAt(parent *os.File, name string) (*os.File, error) {
	fd, err := unix.Openat(int(parent.Fd()), name, unix.O_PATH|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_DIRECTORY, 0)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), name)
	info, err := statFD(f)
	if err != nil || !directory(info) {
		f.Close()
		return nil, fmt.Errorf("unsafe recovery ancestor")
	}
	return f, nil
}
