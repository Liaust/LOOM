package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"time"
)

const fileSummaryPageSize = 512

var errFileSummaryRebuilding = errors.New("file queue summary rebuilding")

type directoryQueueSummary struct {
	Stamp       string `json:"stamp"`
	Offset      int    `json:"offset,omitempty"`
	Complete    bool   `json:"complete"`
	Count       int    `json:"count"`
	Oldest, Due *time.Time
	Bytes       int64 `json:"bytes"`
}

type fileQueueSnapshot struct {
	Revision     uint64        `json:"revision"`
	Outbox       OutboxSummary `json:"outbox"`
	Inbox        InboxSummary  `json:"inbox"`
	LegacyOutbox int           `json:"legacy_outbox"`
}

type fileQueueCache struct {
	Version     int                              `json:"version"`
	Revision    uint64                           `json:"revision"`
	Dirty       bool                             `json:"dirty"`
	Directories map[string]directoryQueueSummary `json:"directories"`
	Snapshot    fileQueueSnapshot                `json:"snapshot"`
	RepairAt    time.Time                        `json:"repair_at"`
}

func (s Store) fileQueueCachePath() string {
	return filepath.Join(s.DataDir, "summaries", "file-queues.json")
}
func (s Store) loadFileQueueCache() fileQueueCache {
	var cache fileQueueCache
	if readJSONFile(s.fileQueueCachePath(), &cache) != nil || cache.Version != 1 || cache.Directories == nil {
		cache = fileQueueCache{Version: 1, Directories: map[string]directoryQueueSummary{}}
	}
	return cache
}

func syncRuntimePath(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}

func writeDurableRuntimeJSON(path string, value any) error {
	if err := writeJSONFile(path, value, 0600); err != nil {
		return err
	}
	if err := syncRuntimePath(path); err != nil {
		return err
	}
	return syncRuntimePath(filepath.Dir(path))
}

func directoryStamp(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("invalid file queue directory")
	}
	stat := info.Sys().(*syscall.Stat_t)
	return fmt.Sprintf("%d:%d:%d", stat.Ino, info.ModTime().UnixNano(), info.Size()), nil
}

// All supported queue writers share this short lock. Persist invalidation before
// touching files; a crash leaves a rebuildable dirty view, never authoritative
// counts. Reconciliation reads names only for history, not historical payloads.
func (s Store) mutateFileQueue(destination, previous string, value any) error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	unlock, err := s.AcquireWorkerExecutionLock(ctx, "node-agent.file-queue-summary")
	if err != nil {
		return err
	}
	defer unlock()
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	old, readErr := os.ReadFile(destination)
	if readErr != nil && !errors.Is(readErr, fs.ErrNotExist) {
		return readErr
	}
	if (previous == "" || previous == destination) && bytes.Equal(old, raw) {
		return nil
	}
	cache := s.loadFileQueueCache()
	type directoryChange struct {
		part    directoryQueueSummary
		current bool
		delta   int
	}
	changes := map[string]directoryChange{}
	for _, path := range []string{destination, previous} {
		if path == "" {
			continue
		}
		dir := filepath.Dir(path)
		key, err := filepath.Rel(s.DataDir, dir)
		if err != nil {
			return err
		}
		if _, exists := changes[key]; exists {
			continue
		}
		stamp, err := directoryStamp(dir)
		if err != nil {
			return err
		}
		part, exists := cache.Directories[key]
		changes[key] = directoryChange{part: part, current: exists && part.Complete && part.Stamp == stamp}
		delete(cache.Directories, key)
	}
	key, _ := filepath.Rel(s.DataDir, filepath.Dir(destination))
	change := changes[key]
	if errors.Is(readErr, fs.ErrNotExist) {
		change.delta++
	}
	changes[key] = change
	if previous != "" && previous != destination {
		if _, err := os.Lstat(previous); err == nil {
			key, _ := filepath.Rel(s.DataDir, filepath.Dir(previous))
			change := changes[key]
			change.delta--
			changes[key] = change
		} else if !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	cache.Dirty = true
	cache.Revision++
	if err := writeDurableRuntimeJSON(s.fileQueueCachePath(), cache); err != nil {
		return err
	}
	if err := writeDurableRuntimeJSON(destination, value); err != nil {
		return err
	}
	if previous != "" && previous != destination {
		if err := os.Remove(previous); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		if err := syncRuntimePath(filepath.Dir(previous)); err != nil {
			return err
		}
	}
	for key, change := range changes {
		// Only pending outbox entries contribute oldest/due/bytes; recompute that
		// active subset. Known history mutations need no directory enumeration.
		if !change.current || key == filepath.Join("outbox", OutboxStatusPending) {
			continue
		}
		part := change.part
		part.Count += change.delta
		part.Stamp, err = directoryStamp(filepath.Join(s.DataDir, key))
		if err != nil {
			return err
		}
		cache.Directories[key] = part
	}
	// Missing/dirty directories still require reconciliation before Snapshot is
	// usable. A crash before this commit retains the earlier durable invalidation.
	return writeDurableRuntimeJSON(s.fileQueueCachePath(), cache)
}

// Resume a stable directory's metadata walk by ordinal, discarding names in
// bounded buffers instead of materializing and sorting all history. A changed
// stamp restarts the disposable cursor; no cursor is execution authority.
func readQueueSummaryPage(dir string, offset int) ([]os.DirEntry, bool, error) {
	directory, err := os.Open(dir)
	if err != nil {
		return nil, false, err
	}
	defer directory.Close()
	for offset > 0 {
		page, err := directory.ReadDir(min(offset, fileSummaryPageSize))
		offset -= len(page)
		if err != nil {
			return nil, false, err
		}
	}
	page, err := directory.ReadDir(fileSummaryPageSize)
	if err != nil && err != io.EOF {
		return nil, false, err
	}
	return page, len(page) == fileSummaryPageSize && err == nil, nil
}

func (s Store) fileQueueSummary() (result fileQueueSnapshot, resultErr error) {
	defer func() {
		availability, reason := "available", ""
		if resultErr != nil {
			availability, reason = "unavailable", resultErr.Error()
			if result.Revision != 0 {
				availability = "stale"
			}
		}
		result.Outbox.Availability, result.Outbox.UnavailableReason = availability, reason
		result.Inbox.Availability, result.Inbox.UnavailableReason = availability, reason
	}()
	if err := s.Ensure(); err != nil {
		return fileQueueSnapshot{}, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	unlock, err := s.AcquireWorkerExecutionLock(ctx, "node-agent.file-queue-summary")
	if err != nil {
		return s.loadFileQueueCache().Snapshot, fmt.Errorf("file queue summary unavailable: %w", err)
	}
	defer unlock()
	cache := s.loadFileQueueCache()
	before, _ := json.Marshal(cache)
	if cache.RepairAt.IsZero() || !time.Now().Before(cache.RepairAt) {
		cache.Directories = map[string]directoryQueueSummary{}
		cache.Dirty = true
		cache.RepairAt = time.Now().Add(6 * time.Hour)
	}
	snapshot := fileQueueSnapshot{Revision: cache.Revision,
		Outbox: OutboxSummary{Counts: map[string]int{}}, Inbox: InboxSummary{Counts: map[string]int{}}}
	complete := true
	for _, key := range []string{"inbox", "outbox", "inbox/pending", "inbox/done", "inbox/failed", "outbox/pending", "outbox/inflight", "outbox/done", "outbox/failed", "outbox/manual_action"} {
		dir := filepath.Join(s.DataDir, filepath.FromSlash(key))
		stamp, err := directoryStamp(dir)
		if err != nil {
			return cache.Snapshot, err
		}
		part, exists := cache.Directories[key]
		if !exists || part.Stamp != stamp {
			part = directoryQueueSummary{Stamp: stamp}
			cache.Revision++
		}
		if !part.Complete {
			// The cursor is disposable. Directory identity is rechecked before
			// resuming, including after a restart or an untracked older writer.
			entries, more, err := readQueueSummaryPage(dir, part.Offset)
			if err != nil {
				return cache.Snapshot, err
			}
			part.Complete = !more
			part.Offset += len(entries)
			for _, entry := range entries {
				if !entry.Type().IsRegular() || !strings.HasSuffix(entry.Name(), ".json") {
					continue
				}
				part.Count++
				if key == "outbox/pending" {
					var item OutboxItem
					if err := readJSONFile(filepath.Join(dir, entry.Name()), &item); err != nil {
						return cache.Snapshot, err
					}
					info, err := entry.Info()
					if err != nil {
						return cache.Snapshot, err
					}
					part.Bytes += info.Size()
					if part.Oldest == nil || item.CreatedAt.Before(*part.Oldest) {
						created := item.CreatedAt
						part.Oldest = &created
					}
					if item.NextAttemptAt != nil && (part.Due == nil || item.NextAttemptAt.Before(*part.Due)) {
						due := *item.NextAttemptAt
						part.Due = &due
					}
				}
			}
			if after, err := directoryStamp(dir); err != nil || after != stamp {
				return cache.Snapshot, errors.New("file queue changed during summary rebuild")
			}
			cache.Directories[key] = part
		}
		complete = complete && part.Complete
		parts := strings.Split(key, "/")
		if len(parts) == 1 {
			if key == "inbox" {
				snapshot.Inbox.LegacyRecords = part.Count
			} else {
				snapshot.LegacyOutbox = part.Count
			}
		} else if parts[0] == "inbox" {
			snapshot.Inbox.Counts[parts[1]] = part.Count
		} else {
			snapshot.Outbox.Counts[parts[1]] = part.Count
			if key == "outbox/pending" {
				snapshot.Outbox.OldestPendingAt, snapshot.Outbox.NextDueAt, snapshot.Outbox.TotalPendingBytes = part.Oldest, part.Due, part.Bytes
			}
		}
	}
	if complete {
		for key, part := range cache.Directories {
			stamp, err := directoryStamp(filepath.Join(s.DataDir, key))
			if err != nil || stamp != part.Stamp {
				return cache.Snapshot, errors.New("file queue changed during summary rebuild")
			}
		}
		snapshot.Revision = cache.Revision
		cache.Snapshot, cache.Dirty = snapshot, false
	}
	after, _ := json.Marshal(cache)
	if !bytes.Equal(before, after) {
		if err := writeDurableRuntimeJSON(s.fileQueueCachePath(), cache); err != nil {
			return cache.Snapshot, err
		}
	}
	if !complete {
		return cache.Snapshot, errFileSummaryRebuilding
	}
	return snapshot, nil
}

func sameSummaryMaterial(a, b LocalSummary) bool {
	a.GeneratedAt, b.GeneratedAt = time.Time{}, time.Time{}
	a.CheckedAt, b.CheckedAt = time.Time{}, time.Time{}
	a.QueueObservedAt, b.QueueObservedAt = time.Time{}, time.Time{}
	a.RetryAt, b.RetryAt = nil, nil
	return reflect.DeepEqual(a, b)
}
