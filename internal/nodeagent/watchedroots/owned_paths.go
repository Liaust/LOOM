package watchedroots

import (
	"encoding/hex"
	"strings"
)

// These are Notes' retained filesystem transaction journals, not user files.
// They remain in source/recovery custody, outside ordinary sync and indexing.
func IsNotesRecoveryPath(p string) bool {
	for _, part := range strings.Split(strings.ReplaceAll(p, "\\", "/"), "/") {
		suffix, ok := strings.CutPrefix(part, ".loom-notes-")
		if ok && len(suffix) == 64 {
			if _, err := hex.DecodeString(suffix); err == nil {
				return true
			}
		}
	}
	return false
}
