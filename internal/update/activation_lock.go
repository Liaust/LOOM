package update

import (
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// Keep the lock inode outside the manifest directory so a refused update does
// not create update state. Never unlink it: process exit releases the lease.
// Main provisions this file for the deployment group; its parent stays unwritable.
func acquireActivationLease(stateDir string) (*os.File, error) {
	if !filepath.IsAbs(stateDir) || filepath.Clean(stateDir) != stateDir || stateDir == "/" {
		return nil, fmt.Errorf("exact update state directory required")
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(stateDir))
	if err != nil {
		return nil, err
	}
	directory, err := unix.Open(parent, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	defer unix.Close(directory)
	name := filepath.Base(stateDir) + ".activation.lock"
	fd, err := unix.Openat(directory, name, unix.O_RDONLY|unix.O_CREAT|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), filepath.Join(parent, name))
	var held, named unix.Stat_t
	if err = unix.Fstat(fd, &held); err != nil || held.Mode&unix.S_IFMT != unix.S_IFREG || held.Nlink != 1 {
		file.Close()
		return nil, fmt.Errorf("invalid update activation lease")
	}
	if err = unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		file.Close()
		return nil, fmt.Errorf("another Main update or rollback is active; wait for that owner: %w", err)
	}
	if err = unix.Fstatat(directory, name, &named, unix.AT_SYMLINK_NOFOLLOW); err != nil || held.Dev != named.Dev || held.Ino != named.Ino {
		file.Close()
		return nil, fmt.Errorf("update activation lease changed")
	}
	return file, nil
}
