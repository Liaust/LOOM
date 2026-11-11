package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const retainedCompletedRuns = 512
const retainedFailedRuns = 128

type RunHistoryReceipt struct {
	WorkerKey  string `json:"worker_key"`
	Scanned    int    `json:"scanned"`
	Candidates int    `json:"candidates"`
	Deleted    int    `json:"deleted"`
	Moved      int    `json:"moved"`
	Kept       int    `json:"kept"`
	Held       int    `json:"held"`
	Bytes      int64  `json:"candidate_logical_bytes"`
	Applied    bool   `json:"applied"`
}

func historyBucket(run WorkerRun) string {
	if run.FinishedAt == nil || run.StartedAt.IsZero() || run.FinishedAt.Before(run.StartedAt) {
		return ""
	}
	switch run.Status {
	case RunStatusSucceeded, RunStatusSkipped:
		return "completed"
	case RunStatusFailed:
		return "failed"
	default:
		return ""
	}
}

func safeHistoryID(id string) bool {
	return id != "" && id != "." && id != ".." && filepath.Base(id) == id && !strings.ContainsAny(id, `/\`)
}

// Native IDs are chronological. Unknown/custom names are held, not silently
// expired based on a lexicographic order that may mean something else.
func nativeHistoryID(id string) bool {
	value, ok := strings.CutPrefix(id, "local_worker_run_")
	if !ok || len(value) != 21 || value[14] != '_' {
		return false
	}
	if _, err := time.Parse("20060102150405", value[:14]); err != nil {
		return false
	}
	for _, digit := range value[15:] {
		if digit < '0' || digit > '9' {
			return false
		}
	}
	return true
}

func (s Store) historyPins(workerKey string) (map[string]bool, error) {
	pins := map[string]bool{}
	checkpoint, err := s.LoadCheckpoint(workerKey)
	if err == nil {
		pins[checkpoint.LastRunID] = true
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	health, err := s.LoadHealth(workerKey)
	if err == nil {
		pins[health.LastRunID] = true
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	return pins, nil
}

func (s Store) appendRunHistory(run WorkerRun) error {
	if !safeHistoryID(run.LocalRunID) || !safeHistoryID(run.WorkerKey) || safeFileSegment(run.WorkerKey) != run.WorkerKey {
		return errors.New("invalid local worker run ID")
	}
	release, err := s.acquireProjectArchiveLock(context.Background(), "history:"+run.WorkerKey)
	if err != nil {
		return err
	}
	defer release()
	base := filepath.Join(s.runsDir(), safeFileSegment(run.WorkerKey))
	bucket := historyBucket(run)
	if !nativeHistoryID(run.LocalRunID) {
		bucket = ""
	}
	dir := filepath.Join(base, bucket)
	if err := writeJSONFile(filepath.Join(dir, run.LocalRunID+".json"), run, 0o600); err != nil {
		return err
	}
	if bucket == "" {
		return nil
	}
	pins, err := s.historyPins(run.WorkerKey)
	if err != nil {
		return err
	}
	// Registry publishes checkpoint/health after AppendRun, so keep both their
	// previous references and this new run until that publication completes.
	pins[run.LocalRunID] = true
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	limit := retainedCompletedRuns
	if bucket == "failed" {
		limit = retainedFailedRuns
	}
	kept := 0
	for i := len(entries) - 1; i >= 0; i-- {
		entry := entries[i]
		id := strings.TrimSuffix(entry.Name(), ".json")
		if !entry.Type().IsRegular() || !strings.HasSuffix(entry.Name(), ".json") || !nativeHistoryID(id) || pins[id] {
			continue
		}
		kept++
		if kept <= limit {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		old, info, err := readHistoryRun(path, run.WorkerKey)
		if err != nil || historyBucket(old) != bucket {
			continue
		}
		if _, err := removeHistoryRun(path, info); err != nil {
			return err
		}
	}
	return nil
}

func readHistoryRun(path, workerKey string) (WorkerRun, fs.FileInfo, error) {
	var run WorkerRun
	info, err := os.Lstat(path)
	if err != nil {
		return run, nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > 16<<20 {
		return run, nil, errors.New("unknown history file")
	}
	file, err := os.Open(path)
	if err != nil {
		return run, nil, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return run, nil, errors.New("history changed while opening")
	}
	decoder := json.NewDecoder(io.LimitReader(file, 16<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&run); err != nil {
		return run, nil, err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return run, nil, errors.New("unexpected trailing history data")
	}
	if run.WorkerKey != workerKey || !safeHistoryID(run.LocalRunID) || filepath.Base(path) != run.LocalRunID+".json" {
		return run, nil, errors.New("history identity mismatch")
	}
	return run, info, nil
}

func removeHistoryRun(path string, expected fs.FileInfo) (bool, error) {
	current, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !current.Mode().IsRegular() || !os.SameFile(expected, current) || current.Size() != expected.Size() || !current.ModTime().Equal(expected.ModTime()) {
		return false, nil
	}
	return true, os.Remove(path)
}

// PruneRunHistory is the explicit one-time adoption of old flat history. Normal
// writes prune only their small bucket; no recurring whole-directory collector.
func (s Store) PruneRunHistory(ctx context.Context, workerKey string, apply bool) (RunHistoryReceipt, error) {
	receipt := RunHistoryReceipt{WorkerKey: workerKey, Applied: apply}
	if !safeHistoryID(workerKey) || safeFileSegment(workerKey) != workerKey {
		return receipt, errors.New("invalid history worker key")
	}
	if err := s.Ensure(); err != nil {
		return receipt, err
	}
	release, err := s.AcquireWorkerExecutionLock(ctx, workerKey)
	if err != nil {
		return receipt, err
	}
	defer release()
	historyRelease, err := s.acquireProjectArchiveLock(ctx, "history:"+workerKey)
	if err != nil {
		return receipt, err
	}
	defer historyRelease()
	pins, err := s.historyPins(workerKey)
	if err != nil {
		return receipt, err
	}
	base := filepath.Join(s.runsDir(), safeFileSegment(workerKey))
	type candidate struct {
		path, id, bucket string
		started          time.Time
		info             fs.FileInfo
	}
	var candidates []candidate
	duplicates := map[string]int{}
	for _, dir := range []string{base, filepath.Join(base, "completed"), filepath.Join(base, "failed")} {
		if info, err := os.Lstat(dir); errors.Is(err, fs.ErrNotExist) {
			continue
		} else if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return receipt, fmt.Errorf("unsafe history directory %s", dir)
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			return receipt, err
		}
		for _, entry := range entries {
			if err := ctx.Err(); err != nil {
				return receipt, err
			}
			if dir == base && entry.IsDir() && (entry.Name() == "completed" || entry.Name() == "failed") {
				continue
			}
			receipt.Scanned++
			path := filepath.Join(dir, entry.Name())
			run, info, err := readHistoryRun(path, workerKey)
			bucket := historyBucket(run)
			if err != nil || bucket == "" || !nativeHistoryID(run.LocalRunID) {
				receipt.Held++
				continue
			}
			duplicates[run.LocalRunID]++
			candidates = append(candidates, candidate{path, run.LocalRunID, bucket, run.StartedAt, info})
		}
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].started.Equal(candidates[j].started) {
			return candidates[i].id > candidates[j].id
		}
		return candidates[i].started.After(candidates[j].started)
	})
	counts := map[string]int{}
	for _, item := range candidates {
		if err := ctx.Err(); err != nil {
			return receipt, err
		}
		if duplicates[item.id] > 1 {
			receipt.Held++
			continue
		}
		keep := pins[item.id]
		if !keep {
			counts[item.bucket]++
			limit := retainedCompletedRuns
			if item.bucket == "failed" {
				limit = retainedFailedRuns
			}
			keep = counts[item.bucket] <= limit
		}
		if keep {
			receipt.Kept++
			destination := filepath.Join(base, item.bucket, item.id+".json")
			if apply && destination != item.path {
				if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
					return receipt, err
				}
				if _, err := os.Lstat(destination); !errors.Is(err, fs.ErrNotExist) {
					return receipt, fmt.Errorf("history destination already exists: %s", destination)
				}
				if err := os.Rename(item.path, destination); err != nil {
					return receipt, err
				}
				receipt.Moved++
			}
			continue
		}
		receipt.Candidates++
		receipt.Bytes += item.info.Size()
		if apply {
			removed, err := removeHistoryRun(item.path, item.info)
			if err != nil {
				return receipt, err
			}
			if removed {
				receipt.Deleted++
			} else {
				receipt.Held++
			}
		}
	}
	return receipt, nil
}

func (s Store) RunHistoryWorkers() ([]string, error) {
	entries, err := os.ReadDir(s.runsDir())
	if errors.Is(err, fs.ErrNotExist) {
		return []string{}, nil
	}
	if err != nil {
		return nil, err
	}
	var keys []string
	for _, entry := range entries {
		if entry.IsDir() && strings.HasPrefix(entry.Name(), WorkerKeyPrefix) {
			keys = append(keys, entry.Name())
		}
	}
	return keys, nil
}
