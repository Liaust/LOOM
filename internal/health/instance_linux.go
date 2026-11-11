//go:build linux

package health

import "os"

func selfIdentity() *InstanceIdentity {
	stat, err := os.ReadFile("/proc/self/stat")
	if err != nil {
		return nil
	}
	return parseInstance(stat, os.Getpid())
}
