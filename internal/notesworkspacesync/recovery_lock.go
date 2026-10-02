package notesworkspacesync

import (
	"errors"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// The recovery publisher takes this same lock before cold capture. An ordinary
// sync tick skips the quiet interval instead of failing or advancing its cursor.
func lockReplica(replicaDir string) (*os.File, bool, error) {
	path := filepath.Join(filepath.Dir(replicaDir), "recovery.lock")
	fd, err := unix.Open(path, unix.O_CREAT|unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, false, err
	}
	f := os.NewFile(uintptr(fd), path)
	var st unix.Stat_t
	if err = unix.Fstat(fd, &st); err != nil || st.Mode&unix.S_IFMT != unix.S_IFREG || st.Nlink != 1 {
		f.Close()
		return nil, false, errors.New("invalid Notes recovery lock")
	}
	if err = unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, unix.EWOULDBLOCK) {
			return nil, false, nil
		}
		return nil, false, err
	}
	return f, true, nil
}
