package nodeagent

import (
	"encoding/json"
	"os"
	"strings"
	"sync"
	"time"
)

type statusCacheEntry struct {
	files []os.FileInfo
	value []byte
	at    time.Time
}

var localStatusCache = struct {
	sync.Mutex
	entries map[string]statusCacheEntry
}{entries: make(map[string]statusCacheEntry)}

// Only small, detached status results are cached, never mutable queue snapshots.
// Atomic replacement by another process invalidates the entry immediately.
func cachedLocalStatus[T any](key string, paths []string, load func() (T, error)) (T, error) {
	key += "\x00" + strings.Join(paths, "\x00")
	before, err := statusFileInfos(paths)
	if err != nil {
		return load()
	}
	localStatusCache.Lock()
	entry, found := localStatusCache.entries[key]
	localStatusCache.Unlock()
	if found && time.Since(entry.at) < time.Hour && sameStatusFiles(entry.files, before) {
		var value T
		if json.Unmarshal(entry.value, &value) == nil {
			return value, nil
		}
	}
	value, err := load()
	if err != nil {
		return value, err
	}
	after, statErr := statusFileInfos(paths)
	if statErr != nil || !sameStatusFiles(before, after) {
		return value, nil
	}
	encoded, err := json.Marshal(value)
	if err != nil || len(encoded) > 64*1024 {
		return value, nil
	}
	localStatusCache.Lock()
	if len(localStatusCache.entries) >= 128 {
		// This acceleration is disposable; bounded eviction needs no durable state.
		clear(localStatusCache.entries)
	}
	localStatusCache.entries[key] = statusCacheEntry{files: after, value: encoded, at: time.Now()}
	localStatusCache.Unlock()
	return value, nil
}

func statusFileInfos(paths []string) ([]os.FileInfo, error) {
	infos := make([]os.FileInfo, len(paths))
	for i, path := range paths {
		info, err := os.Stat(path)
		if err != nil {
			return nil, err
		}
		infos[i] = info
	}
	return infos, nil
}

func sameStatusFiles(a, b []os.FileInfo) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !os.SameFile(a[i], b[i]) || a[i].Size() != b[i].Size() || a[i].Mode() != b[i].Mode() || !a[i].ModTime().Equal(b[i].ModTime()) {
			return false
		}
	}
	return true
}
