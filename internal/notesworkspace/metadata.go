package notesworkspace

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"

	"golang.org/x/sys/unix"
)

// Only the replacement's access metadata is copied. Edit timestamps are new.
// Bound metadata memory; an unreadable/unsupported attribute holds publication.
const maxMetadataBytes = 1 << 20

type replacementMetadata struct {
	UID, GID uint32
	Mode     uint32
	Xattrs   map[string][]byte
}

func metadataAt(fd int) (replacementMetadata, error) {
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return replacementMetadata{}, err
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Nlink != 1 {
		return replacementMetadata{}, ErrUnsafePath
	}
	if err := checkPlatformMetadata(fd, stat); err != nil {
		return replacementMetadata{}, err
	}
	m := replacementMetadata{UID: stat.Uid, GID: stat.Gid, Mode: uint32(stat.Mode) & 07777, Xattrs: map[string][]byte{}}
	// Privileged executable bits are not transferable to newly supplied content.
	if m.Mode&07000 != 0 {
		return m, fmt.Errorf("unsupported special permission bits")
	}
	n, err := unix.Flistxattr(fd, nil)
	if err != nil {
		return m, err
	}
	if n > maxMetadataBytes {
		return m, fmt.Errorf("extended attribute names exceed metadata limit")
	}
	names := make([]byte, n)
	if n > 0 {
		n, err = unix.Flistxattr(fd, names)
		if err != nil {
			return m, err
		}
	}
	total := n
	for _, name := range bytes.Split(names[:n], []byte{0}) {
		if len(name) == 0 {
			continue
		}
		key := string(name)
		if !supportedXattr(key) {
			return m, fmt.Errorf("unsupported extended attribute %q", key)
		}
		size, err := unix.Fgetxattr(fd, key, nil)
		if err != nil {
			return m, err
		}
		total += size
		if total > maxMetadataBytes {
			return m, fmt.Errorf("extended attributes exceed metadata limit")
		}
		value := make([]byte, size)
		got, err := unix.Fgetxattr(fd, key, value)
		if err != nil {
			return m, fmt.Errorf("read extended attribute %q: %w", key, err)
		}
		if got != size {
			return m, fmt.Errorf("extended attribute changed during read: %q", key)
		}
		m.Xattrs[key] = value
	}
	return m, nil
}

func openMetadataFile(parent *os.File, name string) (*os.File, error) {
	fd, err := unix.Openat(int(parent.Fd()), name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), name), nil
}

func preserveReplacementMetadata(journal *os.File) error {
	original, err := openMetadataFile(journal, "displaced")
	if err != nil {
		return err
	}
	defer original.Close()
	proposed, err := openMetadataFile(journal, "proposed")
	if err != nil {
		return err
	}
	defer proposed.Close()
	src, dst := int(original.Fd()), int(proposed.Fd())
	wanted, err := metadataAt(src)
	if err != nil {
		return fmt.Errorf("replacement metadata unavailable: %w", err)
	}
	originalMetadata := wanted
	existing, err := metadataAt(dst)
	if err != nil {
		return fmt.Errorf("staged metadata unavailable: %w", err)
	}
	if existing.UID != wanted.UID || existing.GID != wanted.GID {
		if err := unix.Fchown(dst, int(wanted.UID), int(wanted.GID)); err != nil {
			if !errors.Is(err, unix.EPERM) || existing.UID != uint32(os.Geteuid()) || existing.UID == wanted.UID {
				return fmt.Errorf("preserve ownership: %w", err)
			}
			wanted, err = writerOwnedMetadata(wanted, existing.UID)
			if err != nil {
				return err
			}
			if err := unix.Fchown(dst, -1, int(wanted.GID)); err != nil {
				return fmt.Errorf("preserve group: %w", err)
			}
		}
	}
	if err := unix.Fchmod(dst, wanted.Mode); err != nil {
		return err
	}
	// Remove inherited/stale attributes so retry cannot silently broaden access.
	for name := range existing.Xattrs {
		if _, ok := wanted.Xattrs[name]; !ok {
			if err := unix.Fremovexattr(dst, name); err != nil {
				return err
			}
		}
	}
	// Linux access ACLs are xattrs. Apply after chmod (which updates the ACL mask).
	for name, value := range wanted.Xattrs {
		if err := unix.Fsetxattr(dst, name, value, 0); err != nil {
			return fmt.Errorf("preserve extended attribute %q: %w", name, err)
		}
	}
	actual, err := metadataAt(dst)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(wanted, actual) {
		return fmt.Errorf("replacement metadata verification failed")
	}
	after, err := metadataAt(src)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(originalMetadata, after) {
		return fmt.Errorf("source metadata changed during replacement")
	}
	return proposed.Sync()
}

func ordinaryUserXattr(name string) bool { return strings.HasPrefix(name, "user.") }
