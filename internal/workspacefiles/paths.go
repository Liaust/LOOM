package workspacefiles

import (
	"errors"
	"io/fs"
	"os"
	"strings"

	"golang.org/x/sys/unix"
)

// A substituted FIFO must not block the daemon before its descriptor is checked.
func openNonblocking(root *os.Root, relative string) (*os.File, error) {
	current, err := root.OpenFile(".", os.O_RDONLY|unix.O_NONBLOCK|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	if relative == "." {
		return current, nil
	}
	parts := strings.Split(relative, "/")
	for index, part := range parts {
		flags := unix.O_RDONLY | unix.O_NONBLOCK | unix.O_NOFOLLOW | unix.O_CLOEXEC
		if index < len(parts)-1 {
			flags |= unix.O_DIRECTORY
		}
		fd, openErr := unix.Openat(int(current.Fd()), part, flags, 0)
		_ = current.Close()
		if openErr != nil {
			if errors.Is(openErr, unix.ELOOP) || errors.Is(openErr, unix.ENOTDIR) {
				return nil, fs.ErrPermission
			}
			return nil, openErr
		}
		current = os.NewFile(uintptr(fd), part)
	}
	return current, nil
}
