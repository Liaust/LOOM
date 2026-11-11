package nodeagent

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"

	bolt "go.etcd.io/bbolt"
)

// These are disposable lookup keys in the existing v2 index buckets, not a
// replacement for any retained row, order, journal, or compatibility export.
// The transaction watermark also detects writes by an older record-aware binary.
func backupSelectionWatermark(tx *bolt.Tx) []byte {
	return []byte(fmt.Sprintf("v1:%d", tx.ID()))
}

func (s Store) ensureBackupSelectionIndexes() error {
	db := s.queueDB
	current := false
	if err := db.View(func(tx *bolt.Tx) error {
		current = bytes.Equal(tx.Bucket([]byte("meta")).Get([]byte("backup-selection-index")), backupSelectionWatermark(tx))
		return nil
	}); err != nil || current {
		return err
	}
	return db.Update(func(tx *bolt.Tx) error {
		for _, c := range []string{"backup/items", "backup/outbox"} {
			index := recordBucket(tx, c, "index")
			cur := index.Cursor()
			prefix := []byte("backup-")
			for k, _ := cur.Seek(prefix); k != nil && bytes.HasPrefix(k, prefix); k, _ = cur.Next() {
				if err := cur.Delete(); err != nil {
					return err
				}
			}
			if err := recordBucket(tx, c, "rows").ForEach(func(id, raw []byte) error {
				if s.syncBatch != nil {
					s.syncBatch.stats.RecordReads++
					s.syncBatch.stats.RecordIndexBuildRows++
				}
				return updateBackupSelectionIndexes(tx, c, string(id), raw, true)
			}); err != nil {
				return err
			}
		}
		if err := carryQueueReport(tx); err != nil {
			return err
		}
		return tx.Bucket([]byte("meta")).Put([]byte("backup-selection-index"), backupSelectionWatermark(tx))
	})
}

func backupSelectionKeys(c, id string, raw, position []byte) ([]string, error) {
	var item struct {
		Batch    string `json:"local_batch_id"`
		Root     string `json:"root_key"`
		Path     string `json:"relative_path"`
		Kind     string `json:"item_kind"`
		Hash     string `json:"content_hash_uri"`
		Previous string `json:"previous_hash_uri"`
		Status   string `json:"status"`
		Sequence int64  `json:"local_sequence"`
	}
	if c != "backup/items" && c != "backup/outbox" {
		return nil, nil
	}
	if err := json.Unmarshal(raw, &item); err != nil {
		return nil, err
	}
	if c == "backup/items" {
		keys := []string{"backup-batch\x00" + item.Batch + "\x00" + id}
		if item.Status != localBackupStatusFailed {
			keys = append(keys, backupActionIndexPrefix(item.Root, item.Path, item.Kind, item.Hash, item.Previous)+fmt.Sprintf("%x\x00%s", position, id))
		}
		return keys, nil
	}
	keys := []string{fmt.Sprintf("backup-sequence\x00%020d\x00%s", item.Sequence, id)}
	if item.Status == "" || item.Status == localBackupStatusPending {
		// Positions preserve compatibility-array order even for imported queues
		// whose sequence numbers are not sorted. New rows append at commit.
		keys = append(keys, fmt.Sprintf("backup-ready\x00%s\x00%x\x00%s", item.Root, position, id), fmt.Sprintf("backup-ready-all\x00%x\x00%s", position, id))
	}
	return keys, nil
}

func backupActionIndexPrefix(root, path, kind, hash, previous string) string {
	return strings.Join([]string{"backup-action", root, path, kind, hash, previous, ""}, "\x00")
}

func updateBackupSelectionIndexes(tx *bolt.Tx, c, id string, raw []byte, add bool) error {
	keys, err := backupSelectionKeys(c, id, raw, recordBucket(tx, c, "positions").Get([]byte(id)))
	if err != nil {
		return err
	}
	index := recordBucket(tx, c, "index")
	for _, key := range keys {
		if add {
			err = index.Put([]byte(key), []byte(id))
		} else {
			err = index.Delete([]byte(key))
		}
		if err != nil {
			return err
		}
	}
	return carryBackupSelectionWatermark(tx)
}

func carryBackupSelectionWatermark(tx *bolt.Tx) error {
	// Only carry forward an already complete index. If an older binary wrote
	// since the last indexed transaction, leave the watermark stale for rebuild.
	meta := tx.Bucket([]byte("meta"))
	mark := meta.Get([]byte("backup-selection-index"))
	if bytes.Equal(mark, backupSelectionWatermark(tx)) {
		return nil
	}
	if bytes.Equal(mark, []byte(fmt.Sprintf("v1:%d", tx.ID()-1))) {
		return meta.Put([]byte("backup-selection-index"), backupSelectionWatermark(tx))
	}
	return nil
}

// Selection reads merge the caller's existing redo page. They do not populate
// the full-array cache or turn omitted records into deletion tombstones.
func readBackupRecordSelection[T any](s Store, c, prefix string, exact []string, limit int) ([]T, error) {
	if err := s.ensureBackupSelectionIndexes(); err != nil {
		return nil, err
	}
	var result []T
	err := s.queueDB.View(func(tx *bolt.Tx) error {
		changes := map[string]json.RawMessage{}
		if s.syncBatch != nil {
			changes = s.syncBatch.recordChanges[c]
		}
		ids := map[string]bool{}
		if prefix != "" {
			p := []byte(prefix)
			cur := recordBucket(tx, c, "index").Cursor()
			for k, id := cur.Seek(p); k != nil && bytes.HasPrefix(k, p); k, id = cur.Next() {
				if s.syncBatch != nil {
					s.syncBatch.stats.RecordIndexReads++
				}
				if _, staged := changes[string(id)]; staged {
					continue
				}
				ids[string(id)] = true
				if limit > 0 && len(ids) >= limit {
					break
				}
			}
			for id, raw := range changes {
				if raw == nil {
					continue
				}
				keys, err := backupSelectionKeys(c, id, raw, recordBucket(tx, c, "positions").Get([]byte(id)))
				if err != nil {
					return err
				}
				for _, key := range append(keys, recordIndexKeys(recordDetails(nil, c, raw), id)...) {
					if strings.HasPrefix(key, prefix) {
						ids[id] = true
						break
					}
				}
			}
		} else {
			for _, id := range exact {
				if id != "" {
					ids[id] = true
				}
			}
		}
		rows := map[string]json.RawMessage{}
		var order []string
		for id := range ids {
			raw, staged := changes[id]
			if !staged {
				raw = recordBucket(tx, c, "rows").Get([]byte(id))
				if s.syncBatch != nil && raw != nil {
					s.syncBatch.stats.RecordReads++
				}
			}
			if raw != nil {
				rows[id] = raw
				order = append(order, id)
			}
		}
		sort.Slice(order, func(i, j int) bool {
			a, b := recordBucket(tx, c, "positions").Get([]byte(order[i])), recordBucket(tx, c, "positions").Get([]byte(order[j]))
			if len(a) != 0 || len(b) != 0 {
				if len(a) == 0 || len(b) == 0 {
					return len(a) != 0
				}
				return bytes.Compare(a, b) < 0
			}
			aSeq, bSeq := recordDetails(nil, c, rows[order[i]]).Sequence, recordDetails(nil, c, rows[order[j]]).Sequence
			if aSeq != bSeq {
				return aSeq < bSeq
			}
			return order[i] < order[j]
		})
		if limit > 0 && len(order) > limit {
			order = order[:limit]
		}
		for _, id := range order {
			var item T
			if err := json.Unmarshal(rows[id], &item); err != nil {
				return err
			}
			result = append(result, item)
		}
		return nil
	})
	return result, err
}

func (s Store) nextBackupRecordSequence() (int64, error) {
	if err := s.ensureBackupSelectionIndexes(); err != nil {
		return 0, err
	}
	var maximum int64
	err := s.queueDB.View(func(tx *bolt.Tx) error {
		prefix := []byte("backup-sequence\x00")
		cur := recordBucket(tx, "backup/outbox", "index").Cursor()
		k, id := cur.Seek(append(bytes.Clone(prefix), 0xff))
		if k == nil {
			k, id = cur.Last()
		} else {
			k, id = cur.Prev()
		}
		for ; k != nil && bytes.HasPrefix(k, prefix); k, id = cur.Prev() {
			if s.syncBatch != nil {
				s.syncBatch.stats.RecordIndexReads++
				if _, changed := s.syncBatch.recordChanges["backup/outbox"][string(id)]; changed {
					continue
				}
			}
			value, _, _ := bytes.Cut(k[len(prefix):], []byte{0})
			n, err := strconv.ParseInt(string(value), 10, 64)
			if err != nil {
				return err
			}
			if n > maximum {
				maximum = n
			}
			break
		}
		return nil
	})
	if s.syncBatch != nil {
		for _, raw := range s.syncBatch.recordChanges["backup/outbox"] {
			if raw != nil {
				if n := recordDetails(nil, "backup/outbox", raw).Sequence; n > maximum {
					maximum = n
				}
			}
		}
	}
	return maximum + 1, err
}

func (s Store) saveBackupQueueSelection(path string, value any) error {
	if s.queueDB == nil {
		switch rows := value.(type) {
		case []LocalWatchedRootBackupArtifact:
			return s.SaveWatchedRootBackupArtifacts(rows)
		case []LocalWatchedRootBackupBatch:
			return s.SaveWatchedRootBackupBatches(rows)
		case []LocalWatchedRootBackupItem:
			return s.SaveWatchedRootBackupItems(rows)
		case []LocalWatchedRootBackupOutboxItem:
			return s.SaveWatchedRootBackupOutbox(rows)
		}
		return fmt.Errorf("unknown backup selection")
	}
	store, commit, err := s.joinSyncBatch()
	if err != nil {
		return err
	}
	s = store
	if err := s.ensureBackupSelectionIndexes(); err != nil {
		return err
	}
	c := s.collectionForPath(path)
	if !strings.HasPrefix(c, "backup/") {
		return fmt.Errorf("unknown backup selection")
	}
	err = s.queueDB.View(func(tx *bolt.Tx) error {
		changes := map[string]json.RawMessage{}
		for id, raw := range s.syncBatch.recordChanges[c] {
			changes[id] = raw
		}
		seen := map[string]bool{}
		items := reflect.ValueOf(value)
		for i := 0; i < items.Len(); i++ {
			raw, err := json.Marshal(items.Index(i).Interface())
			if err != nil {
				return err
			}
			id, err := recordKey(c, raw)
			if err != nil {
				return err
			}
			if seen[id] {
				return fmt.Errorf("duplicate backup selection identity")
			}
			seen[id] = true
			old := recordBucket(tx, c, "rows").Get([]byte(id))
			if s.syncBatch != nil && old != nil {
				s.syncBatch.stats.RecordReads++
			}
			if bytes.Equal(old, raw) {
				delete(changes, id)
			} else {
				changes[id] = raw
			}
		}
		s.syncBatch.recordChanges[c] = changes
		delete(s.syncBatch.values, path)
		return nil
	})
	if err != nil {
		return err
	}
	return commit()
}

func (s Store) overlayBackupRecordChanges(c string, original map[string]json.RawMessage, order []string) (map[string]json.RawMessage, []string) {
	rows := make(map[string]json.RawMessage, len(original))
	for id, raw := range original {
		rows[id] = raw
	}
	var added []string
	for id, raw := range s.syncBatch.recordChanges[c] {
		if raw == nil {
			delete(rows, id)
		} else {
			if _, exists := original[id]; !exists {
				added = append(added, id)
			}
			rows[id] = raw
		}
	}
	selected := make([]string, 0, len(rows))
	for _, id := range order {
		if rows[id] != nil {
			selected = append(selected, id)
		}
	}
	sort.Slice(added, func(i, j int) bool {
		a, b := recordDetails(nil, c, rows[added[i]]).Sequence, recordDetails(nil, c, rows[added[j]]).Sequence
		if a != b {
			return a < b
		}
		return added[i] < added[j]
	})
	return rows, append(selected, added...)
}
