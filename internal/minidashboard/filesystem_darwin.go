//go:build darwin

package minidashboard

import (
	"fmt"
	"syscall"
)

func platformFilesystemCapacity(path string) (DiskCapacity, error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return DiskCapacity{}, err
	}
	b := uint64(stat.Bsize)
	return DiskCapacity{Filesystem: fmt.Sprintf("%x", stat.Fsid), Path: path, TotalBytes: stat.Blocks * b, UsedBytes: (stat.Blocks - stat.Bfree) * b, FreeBytes: stat.Bfree * b, AvailableBytes: stat.Bavail * b}, nil
}

func platformFilesystemUsage(path string) (float64, error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return 0, err
	}
	if stat.Blocks == 0 {
		return 0, nil
	}
	return float64(stat.Blocks-stat.Bavail) * 100 / float64(stat.Blocks), nil
}
