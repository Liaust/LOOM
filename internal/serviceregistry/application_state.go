package serviceregistry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

// State belongs to the root effect owner. All updates and descriptor publication
// take the SAME per-instance flock; a late old operation cannot commit over a
// newer published revision. Pool admission has separate ordered pool locks.
type ApplicationStateStore struct {
	Root                string
	OwnerUID            uint32
	TrustedParentOwners map[string]uint32
}

func (s ApplicationStateStore) directory() error {
	if !applicationAbsolutePath(s.Root) {
		return applicationError("state.path")
	}
	if s.OwnerUID == 0 && applicationCheckParents(s.Root, 0) != nil {
		return applicationError("state.parent_custody")
	}
	if err := os.MkdirAll(s.Root, 0700); err != nil {
		return applicationError("state.unavailable")
	}
	return applicationCustody(s.Root, s.OwnerUID, true)
}
func applicationCustody(path string, uid uint32, directory bool) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	// os.FileInfo carries syscall.Stat_t, so use the portable unix stat directly.
	var stat unix.Stat_t
	if unix.Lstat(path, &stat) != nil || info.Mode()&os.ModeSymlink != 0 || stat.Uid != uid || info.Mode().Perm()&0022 != 0 || info.IsDir() != directory {
		return applicationError("custody.invalid")
	}
	return nil
}
func (s ApplicationStateStore) lock(ctx context.Context, key string) (func(), error) {
	if err := s.directory(); err != nil {
		return nil, err
	}
	return applicationFileLock(ctx, filepath.Join(s.Root, "lock-"+applicationSHA(key)[7:]), s.OwnerUID)
}
func applicationFileLock(ctx context.Context, path string, uid uint32) (func(), error) {
	fd, err := unix.Open(path, unix.O_CREAT|unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, applicationError("lock.open")
	}
	var st unix.Stat_t
	if unix.Fstat(fd, &st) != nil || st.Uid != uid || st.Mode&0022 != 0 || st.Mode&unix.S_IFMT != unix.S_IFREG || st.Nlink != 1 {
		unix.Close(fd)
		return nil, applicationError("lock.custody")
	}
	for {
		err = unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			break
		}
		if !errors.Is(err, unix.EWOULDBLOCK) {
			unix.Close(fd)
			return nil, applicationError("lock.failed")
		}
		select {
		case <-ctx.Done():
			unix.Close(fd)
			return nil, ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
	return func() { _ = unix.Flock(fd, unix.LOCK_UN); _ = unix.Close(fd) }, nil
}
func (s ApplicationStateStore) read(key string, out any) error {
	path := filepath.Join(s.Root, key+".json")
	if err := applicationCustody(path, s.OwnerUID, false); err != nil {
		return err
	}
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, applicationMaxBytes+1))
	if err != nil {
		return applicationError("state.read")
	}
	return applicationDecodeJSON(b, out)
}
func (s ApplicationStateStore) write(key string, value any) error {
	b, err := json.Marshal(value)
	if err != nil || len(b) > applicationMaxBytes {
		return applicationError("state.size")
	}
	return applicationAtomic(filepath.Join(s.Root, key+".json"), b, 0600)
}
func applicationAtomic(path string, b []byte, mode os.FileMode) error {
	return applicationAtomicGroup(path, b, mode, -1)
}
func applicationAtomicGroup(path string, b []byte, mode os.FileMode, gid int) error {
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, ".application-")
	if err != nil {
		return applicationError("state.create")
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if gid >= 0 {
		if err = f.Chown(0, gid); err != nil {
			_ = f.Close()
			return applicationError("state.owner")
		}
	}
	if err = f.Chmod(mode); err == nil {
		_, err = f.Write(b)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(tmp, path)
	}
	if err == nil {
		err = applicationSyncDir(dir)
	}
	if err != nil {
		return applicationError("state.publish")
	}
	return nil
}
func applicationSyncDir(path string) error {
	f, e := os.Open(path)
	if e != nil {
		return e
	}
	defer f.Close()
	return f.Sync()
}

type ApplicationEffect struct {
	Kind  string `json:"kind"`
	State string `json:"state"`
}
type ApplicationJournal struct {
	Owner       ApplicationOwner           `json:"owner"`
	Token       string                     `json:"token"`
	InputDigest string                     `json:"input_digest"`
	Revision    string                     `json:"revision"`
	Effects     []ApplicationEffect        `json:"effects"`
	Receipt     *ApplicationRuntimeReceipt `json:"receipt,omitempty"`
}
type ApplicationInstallation struct {
	PreviousEndpoint   *ApplicationEndpointRequest        `json:"previous_endpoint,omitempty"`
	AppliedAt          *time.Time                         `json:"applied_at,omitempty"`
	Committed          bool                               `json:"committed"`
	Request            *ApplicationRuntimeRequest         `json:"request,omitempty"`
	Applied            bool                               `json:"applied"`
	Revision           string                             `json:"revision"`
	Owner              ApplicationOwner                   `json:"owner"`
	Generation         string                             `json:"generation"`
	Previous           string                             `json:"previous"`
	Descriptor         ApplicationArtifactDescriptor      `json:"descriptor"`
	PreviousDescriptor *ApplicationArtifactDescriptor     `json:"previous_descriptor,omitempty"`
	Data               map[string]ApplicationDataIdentity `json:"data"`
	UID                uint32                             `json:"uid"`
	Retired            bool                               `json:"retired"`
	Fenced             bool                               `json:"fenced"`
}

// Claims bind an unpublished sibling inode to one exact destination and purpose.
// An orphan left before claim publication is never adopted; it stays private.
// Publication is no-replace, and all ownership/mode changes use the claimed FD.
type applicationDirectoryClaim struct {
	InitialGID  uint32 `json:"initial_gid"`
	Path        string `json:"path"`
	Purpose     string `json:"purpose"`
	Stage       string `json:"stage"`
	Filesystem  string `json:"filesystem"`
	Inode       uint64 `json:"inode"`
	ParentInode uint64 `json:"parent_inode"`
	UID         uint32 `json:"uid"`
	GID         uint32 `json:"gid"`
	Mode        uint32 `json:"mode"`
}

func applicationFilesystem(path string) (string, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x:%v", st.Type, st.Fsid), nil
}

func (s ApplicationStateStore) ensureClaimedDirectory(ctx context.Context, path, purpose string, uid, gid uint32, mode os.FileMode, infrastructure bool, hook func(string) error) error {
	return s.ensureClaimedDirectoryEpoch(ctx, path, purpose, uid, gid, mode, infrastructure, "", hook)
}
func (s ApplicationStateStore) ensureClaimedDirectoryEpoch(ctx context.Context, path, purpose string, uid, gid uint32, mode os.FileMode, infrastructure bool, epoch string, hook func(string) error) error {
	if !applicationAbsolutePath(path) || purpose == "" || mode.Perm()&0022 != 0 || applicationCheckTrustedParents(path, s.OwnerUID, s.TrustedParentOwners) != nil {
		return applicationError("directory.parent_custody")
	}
	release, err := s.lock(ctx, "directory:"+path)
	if err != nil {
		return err
	}
	defer release()
	parent := filepath.Dir(path)
	var parentStat unix.Stat_t
	if unix.Lstat(parent, &parentStat) != nil {
		return applicationError("directory.parent_custody")
	}
	filesystem, err := applicationFilesystem(parent)
	if err != nil {
		return err
	}
	key := "directory-" + applicationSHA(path)[7:]
	if epoch != "" {
		key = "directory-" + applicationSHA([]string{path, epoch})[7:]
	}
	var claim applicationDirectoryClaim
	err = s.read(key, &claim)
	if errors.Is(err, os.ErrNotExist) {
		var existing unix.Stat_t
		if e := unix.Lstat(path, &existing); e == nil {
			// Only module-owned infrastructure roots, never app/data/generation paths.
			if infrastructure && existing.Uid == uid && existing.Gid == gid && existing.Mode&unix.S_IFMT == unix.S_IFDIR && uint32(existing.Mode)&07777 == uint32(mode.Perm()) {
				return nil
			}
			return applicationError("directory.unclaimed")
		} else if !errors.Is(e, unix.ENOENT) {
			return applicationError("directory.custody")
		}
		stage, e := os.MkdirTemp(parent, ".loom-directory-")
		if e != nil {
			return applicationError("directory.prepare")
		}
		if hook != nil {
			if e = hook("directory:unclaimed_mkdir"); e != nil {
				return e
			}
		}
		var st unix.Stat_t
		if unix.Lstat(stage, &st) != nil {
			return applicationError("directory.prepare")
		}
		claim = applicationDirectoryClaim{InitialGID: st.Gid, Path: path, Purpose: purpose, Stage: stage, Filesystem: filesystem, Inode: st.Ino, ParentInode: parentStat.Ino, UID: uid, GID: gid, Mode: uint32(mode.Perm())}
		if e = applicationSyncDir(parent); e != nil {
			return e
		}
		if e = s.write(key, claim); e != nil {
			return e
		}
		// The hook is after the exact mkdir inode claim is durable. A process killed
		// in the smaller pre-claim window leaks only an unreferenced private staging
		// inode; retry creates a new one and cannot mistake it for the destination.
		if hook != nil {
			if e = hook("directory:mkdir"); e != nil {
				return e
			}
		}
	} else if err != nil {
		return err
	}
	if claim.Path != path || claim.Purpose != purpose || claim.UID != uid || claim.GID != gid || claim.Mode != uint32(mode.Perm()) || claim.Filesystem != filesystem || claim.ParentInode != parentStat.Ino || filepath.Dir(claim.Stage) != parent || !strings.HasPrefix(filepath.Base(claim.Stage), ".loom-directory-") {
		return applicationError("directory.claim_conflict")
	}
	published := false
	chosen := claim.Stage
	var target unix.Stat_t
	if err = unix.Lstat(path, &target); err == nil {
		published = true
		chosen = path
	} else if !errors.Is(err, unix.ENOENT) {
		return applicationError("directory.custody")
	}
	fd, err := unix.Open(chosen, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return applicationError("directory.claim_missing")
	}
	f := os.NewFile(uintptr(fd), chosen)
	defer f.Close()
	verify := func() error {
		var st, leaf, ps unix.Stat_t
		fs, err := applicationFilesystem(parent)
		if err != nil || applicationCheckTrustedParents(path, s.OwnerUID, s.TrustedParentOwners) != nil || unix.Lstat(parent, &ps) != nil || ps.Ino != claim.ParentInode || fs != claim.Filesystem || unix.Fstat(fd, &st) != nil || unix.Lstat(chosen, &leaf) != nil || st.Ino != claim.Inode || leaf.Ino != claim.Inode || st.Dev != leaf.Dev || leaf.Mode&unix.S_IFMT != unix.S_IFDIR || (st.Uid != s.OwnerUID && st.Uid != uid) || (st.Gid != claim.InitialGID && st.Gid != gid) || st.Mode&0022 != 0 {
			return applicationError("directory.claim_replaced")
		}
		return nil
	}
	if err = verify(); err != nil {
		return err
	}
	if !published {
		if err = f.Chown(int(uid), int(gid)); err != nil {
			return applicationError("directory.owner")
		}
		for _, stage := range []string{"directory:chown", "directory:before_chmod"} {
			if hook != nil {
				if err = hook(stage); err != nil {
					return err
				}
			}
		}
		if err = verify(); err != nil {
			return err
		}
		if err = f.Chmod(mode); err != nil {
			return err
		}
		if hook != nil {
			if err = hook("directory:chmod"); err != nil {
				return err
			}
			if err = hook("directory:before_fsync"); err != nil {
				return err
			}
		}
		if err = verify(); err != nil {
			return err
		}
		if err = f.Sync(); err != nil {
			return err
		}
		if hook != nil {
			if err = hook("directory:fsync"); err != nil {
				return err
			}
		}
		if err = verify(); err != nil {
			return err
		}
		if err = applicationRenameDirectoryNoReplace(claim.Stage, path); err != nil {
			return applicationError("directory.publication_conflict")
		}
		chosen = path
		if hook != nil {
			if err = hook("directory:published"); err != nil {
				return err
			}
		}
	}
	if err = verify(); err != nil {
		return err
	}
	var actual unix.Stat_t
	if unix.Fstat(fd, &actual) != nil || actual.Uid != uid || actual.Gid != gid || uint32(actual.Mode)&07777 != uint32(mode.Perm()) {
		return applicationError("directory.final_custody")
	}
	if err = applicationSyncDir(parent); err != nil {
		return err
	}
	if hook != nil {
		return hook("directory:parent_fsync")
	}
	return nil
}
