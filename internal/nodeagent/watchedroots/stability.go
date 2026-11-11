package watchedroots

import (
	"path/filepath"
	"strings"
	"time"
)

const smallTextStability = 250 * time.Millisecond
const smallTextMaxBytes = 256 * 1024

// Coalesce editor saves without giving small text the large-upload quiet time.
// Hashing still verifies the exact file before, during and after its read.
func effectiveHashStability(config ScanConfig, relativePath string, size int64, kind string) time.Duration {
	delay, _ := time.ParseDuration(config.StabilityWindow)
	if kind != PathKindFile || size < 0 || size > smallTextMaxBytes {
		return delay
	}
	switch strings.ToLower(filepath.Ext(relativePath)) {
	case ".md", ".markdown", ".txt":
		return min(delay, smallTextStability)
	default:
		return delay
	}
}
