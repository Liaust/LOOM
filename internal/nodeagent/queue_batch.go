package nodeagent

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"

	"loom.local/loom/internal/nodeagent/watchedroots"
)

// Only used under lockLocalSync. A redo record makes replacement of several
// existing snapshots recoverable without changing their public/on-disk format.
type queueBatch struct {
	values         map[string]any
	original       map[string]json.RawMessage
	dirty          map[string]bool
	paths          map[string]watchedroots.PathState
	stats          QueuePersistenceStats
	recordOriginal map[string]map[string]json.RawMessage
	recordChanges  map[string]map[string]json.RawMessage
}

// Counts installed snapshot work separately from redo bytes. These counters
// describe successful pages in this turn, not retries during startup recovery.
type QueuePersistenceStats struct {
	SnapshotReads        int   `json:"snapshot_reads"`
	RecordReads          int   `json:"record_reads,omitempty"`
	RecordIndexReads     int   `json:"record_index_reads,omitempty"`
	RecordIndexBuildRows int   `json:"record_index_build_rows,omitempty"`
	RecordWrites         int   `json:"record_writes,omitempty"`
	RecordBytes          int64 `json:"record_bytes,omitempty"`
	RecordPageAllocBytes int64 `json:"record_page_alloc_bytes,omitempty"`
	RecordWriteCalls     int64 `json:"record_write_calls,omitempty"`
	SnapshotWrites       int   `json:"snapshot_writes"`
	SnapshotBytes        int64 `json:"snapshot_bytes"`
	JournalBytes         int64 `json:"journal_bytes"`
	CommitPages          int   `json:"commit_pages"`
	CommitMillis         int64 `json:"commit_millis"`
	LockHeldMillis       int64 `json:"lock_held_millis"`
	// Transport phase wall time, including payload preparation and, for backup
	// transfers, durable progress checkpoints. This is not pure network time.
	DeliveryMillis int64 `json:"delivery_millis"`
}

const localQueueCommitPageSize = 64

// A nested mutation joins its caller's page. Only the page owner commits it.
func (s Store) joinSyncBatch() (Store, func() error, error) {
	if s.syncBatch != nil {
		if !s.syncLocked {
			return s, nil, fmt.Errorf("sync batch requires one held queue lock")
		}
		return s, func() error { return nil }, nil
	}
	s, err := s.beginSyncBatch()
	return s, s.commitSyncBatch, err
}

func (s Store) loadQueuePathState(root, relative string) (watchedroots.PathState, error) {
	if s.syncBatch != nil {
		if state, ok := s.syncBatch.paths[watchedroots.PathKey(root, relative)]; ok {
			return state, nil
		}
	}
	return watchedroots.NewStore(s.DataDir).LoadPathState(root, relative)
}

func (s Store) saveQueuePathState(state watchedroots.PathState) error {
	if s.syncBatch != nil {
		s.syncBatch.paths[watchedroots.PathKey(state.RootKey, state.RelativePath)] = state
		return nil
	}
	return watchedroots.NewStore(s.DataDir).SavePathState(state)
}

type queueBatchJournal struct {
	Version int                        `json:"version"`
	Files   map[string]json.RawMessage `json:"files"`
	Paths   []watchedroots.PathState   `json:"paths,omitempty"`
	// Version 2 adds backup snapshots. Version 1 remains readable and is still
	// emitted for sync-only pages. Old readers must settle v2 redo before rollback.
	BackupFiles map[string]json.RawMessage            `json:"backup_files,omitempty"`
	Records     map[string]map[string]json.RawMessage `json:"records,omitempty"`
}

func (s Store) beginSyncBatch() (Store, error) {
	if !s.syncLocked || s.syncBatch != nil {
		return s, fmt.Errorf("sync batch requires one held queue lock")
	}
	if err := s.EnsureSyncDataDirs(); err != nil {
		return s, err
	}
	s.syncBatch = &queueBatch{values: map[string]any{}, original: map[string]json.RawMessage{}, dirty: map[string]bool{}, paths: map[string]watchedroots.PathState{}}
	s.syncBatch.recordOriginal = map[string]map[string]json.RawMessage{}
	s.syncBatch.recordChanges = map[string]map[string]json.RawMessage{}
	return s, nil
}

func (s Store) readSyncJSON(path string, target any) error {
	if s.queueDB != nil {
		return s.readRecordJSON(path, target)
	}
	if s.recordsExist() {
		return s.withRecordAccess(func(locked Store) error { return locked.readRecordJSON(path, target) })
	}
	if s.syncBatch == nil {
		return readJSONFile(path, target)
	}
	if value, ok := s.syncBatch.values[path]; ok {
		if reflect.TypeOf(value) != reflect.TypeOf(target).Elem() {
			return fmt.Errorf("queue snapshot type mismatch")
		}
		reflect.ValueOf(target).Elem().Set(reflect.ValueOf(value))
		return nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	s.syncBatch.stats.SnapshotReads++
	s.syncBatch.values[path] = reflect.ValueOf(target).Elem().Interface()
	var baseline bytes.Buffer
	if err := json.Compact(&baseline, raw); err != nil {
		return err
	}
	s.syncBatch.original[path] = baseline.Bytes()
	return nil
}

func (s Store) writeSyncJSON(path string, value any) error {
	if s.queueDB != nil {
		return s.writeRecordJSON(path, value)
	}
	if s.recordsExist() {
		return s.withRecordAccess(func(locked Store) error { return locked.writeRecordJSON(path, value) })
	}
	if s.syncBatch == nil {
		return writeJSONFile(path, value, 0o600)
	}
	if _, ok := s.syncBatch.original[path]; !ok {
		// Capture an immutable baseline before callers mutate a cached slice.
		target := reflect.New(reflect.TypeOf(value)).Interface()
		if err := s.readSyncJSON(path, target); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	s.syncBatch.values[path] = value
	s.syncBatch.dirty[path] = true
	return nil
}

func (s Store) syncBatchJournalPath() string {
	return filepath.Join(s.syncRoot(), "pending-batch.json")
}

func (s Store) commitSyncBatch() error {
	if s.queueDB != nil && s.syncBatch != nil {
		return s.commitRecordBatch()
	}
	if s.syncBatch == nil || len(s.syncBatch.dirty)+len(s.syncBatch.paths) == 0 {
		return nil
	}
	started := time.Now()
	journal := queueBatchJournal{Version: 1, Files: map[string]json.RawMessage{}}
	for path := range s.syncBatch.dirty {
		data, err := json.Marshal(s.syncBatch.values[path])
		if err != nil {
			return err
		}
		if bytes.Equal(data, s.syncBatch.original[path]) {
			continue
		}
		if filepath.Dir(path) == s.watchedRootBackupRoot() {
			journal.Version = 2
			if journal.BackupFiles == nil {
				journal.BackupFiles = map[string]json.RawMessage{}
			}
			journal.BackupFiles[filepath.Base(path)] = data
		} else {
			journal.Files[filepath.Base(path)] = data
		}
	}
	var keys []string
	for key := range s.syncBatch.paths {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		journal.Paths = append(journal.Paths, s.syncBatch.paths[key])
	}
	if len(journal.Files)+len(journal.BackupFiles)+len(journal.Paths) == 0 {
		return nil
	}
	data, err := json.Marshal(journal)
	if err != nil {
		return err
	}
	if err := writeDurableQueueJSON(s.syncBatchJournalPath(), json.RawMessage(data)); err != nil {
		return err
	}
	if err := s.installSyncBatch(journal); err != nil {
		return err
	}
	s.syncBatch.stats.CommitPages++
	s.syncBatch.stats.CommitMillis += time.Since(started).Milliseconds()
	for _, files := range []map[string]json.RawMessage{journal.Files, journal.BackupFiles} {
		for _, data := range files {
			s.syncBatch.stats.SnapshotWrites++
			s.syncBatch.stats.SnapshotBytes += int64(len(data) + 1)
		}
	}
	s.syncBatch.stats.JournalBytes += int64(len(data) + 1)
	// The queue lock still excludes other sync writers. Keep decoded sync state
	// across delivery pages, but advance its immutable baseline after each commit.
	for name, data := range journal.Files {
		s.syncBatch.original[filepath.Join(s.syncRoot(), name)] = data
	}
	// Backup writers have their own lock, so their cache cannot span pages.
	for path := range s.syncBatch.values {
		if filepath.Dir(path) == s.watchedRootBackupRoot() {
			delete(s.syncBatch.values, path)
			delete(s.syncBatch.original, path)
		}
	}
	s.syncBatch.dirty = map[string]bool{}
	s.syncBatch.paths = map[string]watchedroots.PathState{}
	return nil
}

func (s Store) recoverSyncBatch() error {
	var journal queueBatchJournal
	if err := readJSONFile(s.syncBatchJournalPath(), &journal); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	return s.installSyncBatch(journal)
}

func (s Store) installSyncBatch(journal queueBatchJournal) error {
	if journal.Version == 3 {
		return s.applyRecordJournal(journal)
	}
	if len(journal.Records) > 0 {
		return fmt.Errorf("record changes in legacy journal")
	}
	if s.queueDB != nil {
		return fmt.Errorf("legacy redo after record migration requires explicit recovery")
	}
	allowed := map[string]bool{}
	for _, path := range []string{s.syncEventsPath(), s.syncObjectsPath(), s.syncDeletionsPath(), s.syncOutboxPath(), s.syncCursorsPath(), s.syncConflictsPath()} {
		allowed[filepath.Base(path)] = true
	}
	if journal.Version != 1 && journal.Version != 2 ||
		journal.Version == 1 && len(journal.BackupFiles) != 0 ||
		len(journal.Files)+len(journal.BackupFiles)+len(journal.Paths) == 0 {
		return fmt.Errorf("invalid sync batch journal")
	}
	var names []string
	for name, data := range journal.Files {
		if !allowed[name] || !json.Valid(data) {
			return fmt.Errorf("invalid sync batch snapshot")
		}
		names = append(names, name)
	}
	var backupNames []string
	backupCounts := map[string]LocalWatchedRootBackupQueueStatusCounts{}
	for name, data := range journal.BackupFiles {
		counts, err := backupSnapshotCounts(name, data)
		if err != nil {
			return err
		}
		backupNames = append(backupNames, name)
		backupCounts[name] = counts
	}
	sort.Strings(names)
	sort.Strings(backupNames)
	if len(backupNames) > 0 && !s.backupLocked {
		return s.withWatchedRootBackupQueueLock(func(locked Store) error {
			return locked.installSyncBatch(journal)
		})
	}
	for _, name := range names {
		if err := writeDurableQueueJSON(filepath.Join(s.syncRoot(), name), journal.Files[name]); err != nil {
			return err
		}
	}
	for _, name := range backupNames {
		if err := writeDurableQueueJSON(filepath.Join(s.watchedRootBackupRoot(), name), journal.BackupFiles[name]); err != nil {
			return err
		}
		if err := s.appendWatchedRootBackupQueueEvent(strings.TrimSuffix(name, ".json"), backupCounts[name]); err != nil {
			return err
		}
	}
	ws := watchedroots.NewStore(s.DataDir)
	for _, state := range journal.Paths {
		if err := ws.SavePathStateDurable(state); err != nil {
			return err
		}
	}
	if err := os.Remove(s.syncBatchJournalPath()); err != nil {
		return err
	}
	return syncQueueDirectory(s.syncRoot())
}

func backupSnapshotCounts(name string, data json.RawMessage) (LocalWatchedRootBackupQueueStatusCounts, error) {
	var counts LocalWatchedRootBackupQueueStatusCounts
	var err error
	switch name {
	case localBackupArtifactsFile:
		var value []LocalWatchedRootBackupArtifact
		err = json.Unmarshal(data, &value)
		counts = countWatchedRootBackupArtifacts(value)
	case localBackupBatchesFile:
		var value []LocalWatchedRootBackupBatch
		err = json.Unmarshal(data, &value)
		counts = countWatchedRootBackupBatches(value)
	case localBackupItemsFile:
		var value []LocalWatchedRootBackupItem
		err = json.Unmarshal(data, &value)
		counts = countWatchedRootBackupItems(value)
	case localBackupOutboxFile:
		var value []LocalWatchedRootBackupOutboxItem
		err = json.Unmarshal(data, &value)
		counts = countWatchedRootBackupOutbox(value)
	default:
		return counts, fmt.Errorf("invalid backup batch snapshot")
	}
	return counts, err
}

func writeDurableQueueJSON(path string, value any) error {
	var raw []byte
	if r, ok := value.(json.RawMessage); ok {
		raw = r
	} else {
		var err error
		raw, err = json.Marshal(value)
		if err != nil {
			return err
		}
	}
	return writeDurableQueueBytes(path, append(bytes.Clone(raw), '\n'))
}

func writeDurableQueueBytes(path string, raw []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".queue-batch-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if _, err := f.Write(raw); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(f.Name(), path); err != nil {
		return err
	}
	return syncQueueDirectory(filepath.Dir(path))
}

func syncQueueDirectory(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}
