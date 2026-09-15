//go:build !darwin && !linux

package filesystemmeta

import "os"

func applyPlatformStat(_ string, _ os.FileInfo, _ *Observation) error {
	return nil
}

func applyPlatformXattrs(_ string, _ DetectOptions, _ *Observation) {
}
