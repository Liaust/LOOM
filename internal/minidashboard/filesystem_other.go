//go:build !linux && !darwin

package minidashboard

import "fmt"

func platformFilesystemCapacity(string) (DiskCapacity, error) {
	return DiskCapacity{}, fmt.Errorf("filesystem telemetry is unsupported on this platform")
}

func platformFilesystemUsage(string) (float64, error) {
	return 0, fmt.Errorf("filesystem telemetry is unsupported on this platform")
}
