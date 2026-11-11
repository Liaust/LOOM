//go:build darwin || linux

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"loom.local/loom/internal/notesworkspace"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

const directoryFlags = unix.O_RDONLY | unix.O_DIRECTORY | unix.O_NOFOLLOW | unix.O_CLOEXEC

type inode struct{ device, number uint64 }

func (i inode) matchesDirectory(s *unix.Stat_t) bool {
	return s != nil && s.Mode&unix.S_IFMT == unix.S_IFDIR && uint64(s.Dev) == i.device && uint64(s.Ino) == i.number
}

type reply struct {
	OK       bool   `json:"ok"`
	Replayed bool   `json:"replayed,omitempty"`
	Error    string `json:"error,omitempty"`
}

func main() { os.Exit(run(os.Args[1:], os.Stdout)) }

func run(args []string, out io.Writer) int {
	if len(args) == 2 && args[0] == "import" {
		return runImport(args[1], os.Stdin, out)
	}
	if len(args) == 2 && args[0] == "local" {
		var request notesworkspace.LocalClientRequest
		decoder := json.NewDecoder(io.LimitReader(os.Stdin, 12<<20))
		decoder.DisallowUnknownFields()
		err := decoder.Decode(&request)
		if err == nil {
			var extra any
			if decoder.Decode(&extra) != io.EOF {
				err = fmt.Errorf("invalid local request")
			}
		}
		if err == nil {
			err = notesworkspace.ApplyLocalClient(args[1], request)
		}
		result := reply{OK: err == nil}
		if err != nil {
			result.Error = err.Error()
		}
		json.NewEncoder(out).Encode(result)
		if err != nil {
			return 1
		}
		return 0
	}
	if len(args) == 2 && args[0] == "hold" {
		if err := hold(args[1], out); err != nil {
			json.NewEncoder(out).Encode(reply{Error: err.Error()})
			return 1
		}
		return 0
	}
	var result reply
	var err error
	if len(args) != 5 || args[0] != "move" {
		err = fmt.Errorf("usage: loom-workspace-fs move ROOT FROM TO EXPECTED_DEV:INO")
	} else {
		var expected inode
		expected, err = parseInode(args[4])
		if err == nil {
			result.Replayed, err = moveDirectory(args[1], args[2], args[3], expected)
		}
	}
	result.OK = err == nil
	if err != nil {
		result.Error = err.Error()
	}
	if json.NewEncoder(out).Encode(result) != nil || err != nil {
		return 1
	}
	return 0
}

// Python's stopped-client reader takes the same flock. The app keeps stdin
// open until all native writers close, so an idle GUI is still a live owner.
func hold(root string, out io.Writer) error {
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil || resolved != root || !filepath.IsAbs(root) {
		return fmt.Errorf("invalid native owner root")
	}
	fd, err := unix.Open(filepath.Join(root, "native-recovery.lock"), unix.O_RDWR|unix.O_CREAT|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return err
	}
	if st.Mode&unix.S_IFMT != unix.S_IFREG || st.Nlink != 1 || int(st.Uid) != os.Geteuid() {
		return fmt.Errorf("unsafe native owner lock")
	}
	if err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		return fmt.Errorf("native state already owned: %w", err)
	}
	if err := json.NewEncoder(out).Encode(reply{OK: true}); err != nil {
		return err
	}
	_, err = io.Copy(io.Discard, os.Stdin)
	return err
}

func parseInode(value string) (inode, error) {
	parts := strings.Split(value, ":")
	if len(parts) != 2 {
		return inode{}, fmt.Errorf("expected decimal DEV:INO")
	}
	var numbers [2]uint64
	for i, part := range parts {
		if part == "" || strings.IndexFunc(part, func(c rune) bool { return c < '0' || c > '9' }) >= 0 {
			return inode{}, fmt.Errorf("expected decimal DEV:INO")
		}
		value, err := strconv.ParseUint(part, 10, 64)
		if err != nil {
			return inode{}, fmt.Errorf("expected decimal DEV:INO: %w", err)
		}
		numbers[i] = value
	}
	return inode{numbers[0], numbers[1]}, nil
}

func validateRelative(value string) error {
	if value == "" || filepath.IsAbs(value) || filepath.Clean(value) != value || strings.ContainsAny(value, "\\\x00") {
		return fmt.Errorf("paths must be clean visible relative paths")
	}
	for _, part := range strings.Split(value, "/") {
		if strings.HasPrefix(part, ".") {
			return fmt.Errorf("hidden or traversing paths are not allowed")
		}
	}
	return nil
}

func within(value, parent string) bool {
	return value == parent || strings.HasPrefix(value, parent+"/")
}

type directoryChain struct {
	fds   []int
	names []string
}

func (c *directoryChain) fd() int { return c.fds[len(c.fds)-1] }
func (c *directoryChain) close() {
	for i := len(c.fds) - 1; i >= 0; i-- {
		unix.Close(c.fds[i])
	}
}

func openParent(root, relative string) (*directoryChain, string, error) {
	fd, err := unix.Open("/", directoryFlags, 0)
	if err != nil {
		return nil, "", err
	}
	chain := &directoryChain{fds: []int{fd}}
	parent := filepath.Dir(filepath.Join(root, relative))
	for _, name := range strings.Split(strings.TrimPrefix(parent, "/"), "/") {
		if name == "" {
			continue
		}
		child, err := unix.Openat(chain.fd(), name, directoryFlags, 0)
		if err != nil {
			chain.close()
			return nil, "", fmt.Errorf("open parent: %w", err)
		}
		chain.names = append(chain.names, name)
		chain.fds = append(chain.fds, child)
	}
	return chain, filepath.Base(relative), nil
}

func (c *directoryChain) check() error {
	for i, name := range c.names {
		var actual, pinned unix.Stat_t
		if err := unix.Fstatat(c.fds[i], name, &actual, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			return fmt.Errorf("parent chain changed: %w", err)
		}
		if err := unix.Fstat(c.fds[i+1], &pinned); err != nil {
			return err
		}
		if !(inode{uint64(pinned.Dev), uint64(pinned.Ino)}).matchesDirectory(&actual) {
			return fmt.Errorf("parent chain changed")
		}
	}
	return nil
}

func statAt(fd int, name string) (*unix.Stat_t, error) {
	var stat unix.Stat_t
	err := unix.Fstatat(fd, name, &stat, unix.AT_SYMLINK_NOFOLLOW)
	if errors.Is(err, unix.ENOENT) {
		return nil, nil
	}
	return &stat, err
}

func moveDirectory(root, from, to string, expected inode) (bool, error) {
	if !filepath.IsAbs(root) || filepath.Clean(root) != root || strings.ContainsRune(root, '\x00') {
		return false, fmt.Errorf("root must be absolute and clean")
	}
	if err := errors.Join(validateRelative(from), validateRelative(to)); err != nil {
		return false, err
	}
	if within(from, to) || within(to, from) {
		return false, fmt.Errorf("source and destination must not overlap")
	}
	sourceParent, sourceName, err := openParent(root, from)
	if err != nil {
		return false, err
	}
	defer sourceParent.close()
	targetParent, targetName, err := openParent(root, to)
	if err != nil {
		return false, err
	}
	defer targetParent.close()
	if err := errors.Join(sourceParent.check(), targetParent.check()); err != nil {
		return false, err
	}
	for _, chain := range []*directoryChain{sourceParent, targetParent} {
		var parent unix.Stat_t
		if err := unix.Fstat(chain.fd(), &parent); err != nil {
			return false, err
		}
		if uint64(parent.Dev) != expected.device {
			return false, fmt.Errorf("source and destination must be on the expected filesystem")
		}
		// Also reject physical ancestry through case aliases or mount aliases.
		for _, fd := range chain.fds {
			if err := unix.Fstat(fd, &parent); err != nil {
				return false, err
			}
			if expected.matchesDirectory(&parent) {
				return false, fmt.Errorf("parent overlaps moving directory")
			}
		}
	}
	source, err := statAt(sourceParent.fd(), sourceName)
	if err != nil {
		return false, err
	}
	target, err := statAt(targetParent.fd(), targetName)
	if err != nil {
		return false, err
	}
	replayed := source == nil
	if replayed {
		if !expected.matchesDirectory(target) {
			return false, fmt.Errorf("source absent without matching destination directory")
		}
	} else {
		if !expected.matchesDirectory(source) {
			return false, fmt.Errorf("source directory identity mismatch")
		}
		if target != nil {
			return false, fmt.Errorf("destination already exists")
		}
		if err := errors.Join(sourceParent.check(), targetParent.check()); err != nil {
			return false, err
		}
		if err := renameNoReplace(sourceParent.fd(), sourceName, targetParent.fd(), targetName); err != nil {
			return false, fmt.Errorf("rename directory: %w", err)
		}
	}
	// Flush even on an uncertain post-rename check; replay must verify the inode.
	syncErr := errors.Join(unix.Fsync(sourceParent.fd()), unix.Fsync(targetParent.fd()))
	if err := errors.Join(syncErr, sourceParent.check(), targetParent.check()); err != nil {
		return false, err
	}
	source, err = statAt(sourceParent.fd(), sourceName)
	if err != nil {
		return false, err
	}
	target, err = statAt(targetParent.fd(), targetName)
	if err != nil {
		return false, err
	}
	if source != nil || !expected.matchesDirectory(target) {
		return false, fmt.Errorf("move outcome is unverified; retry the exact request")
	}
	return replayed, nil
}
