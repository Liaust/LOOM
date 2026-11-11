package watchedroots

import (
	"crypto/rand"
	"path"
	"strings"
	"time"

	"loom.local/loom/internal/filesystemconnector"
)

// DirtyHintSubtree invalidates the named path and all descendants. It is not
// deletion evidence: reconciliation must inspect the current admitted prefix.
const DirtyHintSubtree = "subtree"

// MergeDirtyHints coalesces exact paths and subtree ancestry without mutating
// existing. An absorbed arrival renews the covering hint's generation, so an
// older scanner snapshot cannot acknowledge work that arrived during its walk.
func MergeDirtyHints(existing map[string]DirtyHint, additions []DirtyHint) map[string]DirtyHint {
	merged := make(map[string]DirtyHint, len(existing)+len(additions))
	for _, hint := range existing {
		merged[dirtyHintKey(hint.RelativePath)] = hint
	}
	now := time.Now().UTC()
	for _, hint := range additions {
		hint.Generation = rand.Text()
		hint.RelativePath = normalizeDirtyPath(hint.RelativePath)
		if strings.TrimSpace(hint.HintKind) == "" {
			hint.HintKind = DirtyHintModified
		}
		if hint.HintKind == DirtyHintSubtree {
			relative, _, err := filesystemconnector.NormalizeRelativePath(hint.RelativePath)
			if err != nil || relative == "." || relative == "__rescan__" {
				hint.RelativePath, hint.HintKind = "__rescan__", DirtyHintRescanRequired
				hint.LastErrorCode = "invalid_subtree_hint"
			} else {
				hint.RelativePath = relative
			}
		}
		if strings.TrimSpace(hint.Source) == "" {
			hint.Source = DirtyHintSourceManual
		}
		if hint.FirstSeenAt.IsZero() {
			hint.FirstSeenAt = now
		}
		if hint.LastSeenAt.IsZero() {
			hint.LastSeenAt = now
		}
		if hint.Count <= 0 {
			hint.Count = 1
		}
		key := dirtyHintKey(hint.RelativePath)
		covered := false
		if hint.HintKind != DirtyHintRescanRequired && key != "__rescan__" {
			for ancestor := key; ancestor != "." && ancestor != "/"; ancestor = path.Dir(ancestor) {
				previous, ok := merged[ancestor]
				if !ok || previous.HintKind != DirtyHintSubtree {
					continue
				}
				previous.Generation = hint.Generation
				if ancestor == key {
					previous.Source, previous.LastErrorCode, previous.Observation = hint.Source, hint.LastErrorCode, hint.Observation
				}
				accumulateDirtyHint(&previous, hint)
				merged[ancestor] = previous
				covered = true
				break
			}
		}
		if covered {
			continue
		}
		if hint.HintKind == DirtyHintSubtree {
			for previousKey, previous := range merged {
				if previous.HintKind != DirtyHintRescanRequired && previousKey != "__rescan__" && dirtySubtreeContains(key, previous.RelativePath) {
					accumulateDirtyHint(&hint, previous)
					delete(merged, previousKey)
				}
			}
		} else if previous, ok := merged[key]; ok {
			accumulateDirtyHint(&hint, previous)
		}
		merged[key] = hint
	}
	return merged
}

func dirtySubtreeContains(prefix, relative string) bool {
	return relative == prefix || strings.HasPrefix(relative, prefix+"/")
}

func accumulateDirtyHint(hint *DirtyHint, previous DirtyHint) {
	hint.Count += previous.Count
	if hint.FirstSeenAt.IsZero() || !previous.FirstSeenAt.IsZero() && previous.FirstSeenAt.Before(hint.FirstSeenAt) {
		hint.FirstSeenAt = previous.FirstSeenAt
	}
	if previous.LastSeenAt.After(hint.LastSeenAt) {
		hint.LastSeenAt = previous.LastSeenAt
	}
}
