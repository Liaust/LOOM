package nodeagent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"

	"loom.local/loom/internal/nodeagent/watchedroots"
)

// Only used under lockLocalSync. A redo record makes replacement of several
// existing snapshots recoverable without changing their public/on-disk format.
type queueBatch struct {
	values map[string]any
	dirty  map[string]bool
	paths  map[string]watchedroots.PathState
}

type queueBatchJournal struct {
	Version int                        `json:"version"`
	Files   map[string]json.RawMessage `json:"files"`
	Paths   []watchedroots.PathState   `json:"paths,omitempty"`
}

func (s Store) beginSyncBatch() (Store, error) {
	if !s.syncLocked || s.syncBatch != nil {
		return s, fmt.Errorf("sync batch requires one held queue lock")
	}
	if err := s.EnsureSyncDataDirs(); err != nil {
		return s, err
	}
	s.syncBatch = &queueBatch{values: map[string]any{}, dirty: map[string]bool{}, paths: map[string]watchedroots.PathState{}}
	return s, nil
}

func (s Store) readSyncJSON(path string, target any) error {
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
	if err := readJSONFile(path, target); err != nil {
		return err
	}
	s.syncBatch.values[path] = reflect.ValueOf(target).Elem().Interface()
	return nil
}

func (s Store) writeSyncJSON(path string, value any) error {
	if s.syncBatch == nil {
		return writeJSONFile(path, value, 0o600)
	}
	s.syncBatch.values[path] = value
	s.syncBatch.dirty[path] = true
	return nil
}

func (s Store) syncBatchJournalPath() string {
	return filepath.Join(s.syncRoot(), "pending-batch.json")
}

func (s Store) commitSyncBatch() error {
	if s.syncBatch == nil || len(s.syncBatch.dirty)+len(s.syncBatch.paths) == 0 {
		return nil
	}
	journal := queueBatchJournal{Version: 1, Files: map[string]json.RawMessage{}}
	for path := range s.syncBatch.dirty {
		data, err := json.Marshal(s.syncBatch.values[path])
		if err != nil {
			return err
		}
		journal.Files[filepath.Base(path)] = data
	}
	var keys []string
	for key := range s.syncBatch.paths {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		journal.Paths = append(journal.Paths, s.syncBatch.paths[key])
	}
	if err := writeDurableQueueJSON(s.syncBatchJournalPath(), journal); err != nil {
		return err
	}
	return s.recoverSyncBatch()
}

func (s Store) recoverSyncBatch() error {
	var journal queueBatchJournal
	if err := readJSONFile(s.syncBatchJournalPath(), &journal); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	allowed := map[string]bool{}
	for _, path := range []string{s.syncEventsPath(), s.syncObjectsPath(), s.syncDeletionsPath(), s.syncOutboxPath(), s.syncCursorsPath(), s.syncConflictsPath()} {
		allowed[filepath.Base(path)] = true
	}
	if journal.Version != 1 || len(journal.Files)+len(journal.Paths) == 0 {
		return fmt.Errorf("invalid sync batch journal")
	}
	var names []string
	for name, data := range journal.Files {
		if !allowed[name] || !json.Valid(data) {
			return fmt.Errorf("invalid sync batch snapshot")
		}
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if err := writeDurableQueueJSON(filepath.Join(s.syncRoot(), name), journal.Files[name]); err != nil {
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

func writeDurableQueueJSON(path string, value any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".queue-batch-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if err := json.NewEncoder(f).Encode(value); err != nil {
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
