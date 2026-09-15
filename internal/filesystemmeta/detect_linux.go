//go:build linux

package filesystemmeta

import (
	"errors"
	"os"
	"sort"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

func applyPlatformStat(path string, info os.FileInfo, observation *Observation) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat == nil {
		return nil
	}
	uid := int(stat.Uid)
	gid := int(stat.Gid)
	deviceID := int64(stat.Dev)
	inode := int64(stat.Ino)
	linkCount := int64(stat.Nlink)
	allocated := stat.Blocks * 512
	observation.UID = &uid
	observation.GID = &gid
	observation.DeviceID = &deviceID
	observation.Inode = &inode
	observation.LinkCount = &linkCount
	observation.AllocatedBytes = &allocated
	if observation.Kind == ObjectKindRegularFile && linkCount > 1 {
		observation.IsHardLink = true
	}
	if observation.Kind == ObjectKindRegularFile && observation.LogicalSizeBytes > 0 && allocated < observation.LogicalSizeBytes {
		observation.IsSparse = true
	}
	var statx unix.Statx_t
	if err := unix.Statx(unix.AT_FDCWD, path, unix.AT_SYMLINK_NOFOLLOW, unix.STATX_BTIME, &statx); err == nil && statx.Mask&unix.STATX_BTIME != 0 {
		createdAt := time.Unix(statx.Btime.Sec, int64(statx.Btime.Nsec)).UTC()
		observation.SourceCreatedAt = &createdAt
		observation.SourceCreatedBasis = SourceTimeBasisFilesystemBirthtime
	}
	return nil
}

func applyPlatformXattrs(path string, opts DetectOptions, observation *Observation) {
	names, err := listXattrNames(path)
	if err != nil || len(names) == 0 {
		return
	}
	observation.HasXattrs = true
	if opts.IncludeXattrNames {
		observation.XattrNames = names
	}
	for _, name := range names {
		if strings.Contains(strings.ToLower(name), "acl") {
			observation.HasACL = true
			observation.Risks = appendRisk(observation.Risks, FidelityRiskMetadataOnly)
		}
	}
}

func listXattrNames(path string) ([]string, error) {
	size, err := unix.Llistxattr(path, nil)
	if err != nil {
		if isNoXattrError(err) {
			return nil, nil
		}
		return nil, err
	}
	if size <= 0 {
		return nil, nil
	}
	buf := make([]byte, size)
	n, err := unix.Llistxattr(path, buf)
	if err != nil {
		if isNoXattrError(err) {
			return nil, nil
		}
		return nil, err
	}
	names := splitNullSeparated(buf[:n])
	sort.Strings(names)
	return names, nil
}

func isNoXattrError(err error) bool {
	return errors.Is(err, unix.ENODATA) || errors.Is(err, unix.ENOTSUP) || errors.Is(err, unix.EPERM)
}
