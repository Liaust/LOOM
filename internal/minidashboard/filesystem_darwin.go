//go:build darwin

package minidashboard

import "syscall"

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
