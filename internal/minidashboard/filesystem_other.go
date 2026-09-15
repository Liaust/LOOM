//go:build !linux && !darwin

package minidashboard

import "fmt"

func platformFilesystemUsage(string) (float64, error) {
	return 0, fmt.Errorf("filesystem telemetry is unsupported on this platform")
}
