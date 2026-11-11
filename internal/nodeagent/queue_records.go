package nodeagent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"

	bolt "go.etcd.io/bbolt"
	"loom.local/loom/internal/nodeagent/watchedroots"
	loomsync "loom.local/loom/internal/sync"
)

const queueRecordsFormat = "loom.node-queue.records.v2"

var recordCollections = []string{"sync/events", "sync/objects", "sync/deletions", "sync/outbox", "sync/cursors", "sync/conflicts", "backup/artifacts", "backup/batches", "backup/items", "backup/outbox"}

type queueRecordReceipt struct {
	Format      string                         `json:"format"`
	Collections map[string]queueRecordBaseline `json:"collections"`
}
type queueRecordBaseline struct {
	Count           int    `json:"count"`
	SHA256          string `json:"sha256"`
	CanonicalSHA256 string `json:"canonical_sha256"`
}

func (s Store) queueRecordsPath() string     { return filepath.Join(s.syncRoot(), "records.db") }
func (s Store) queueRecordsBaseline() string { return filepath.Join(s.syncRoot(), "legacy-records-v1") }
func recordMarker() []byte {
	return []byte(`{"format":"` + queueRecordsFormat + `","error":"use the record-aware node-agent; legacy export requires an explicit stopped-owner rollback"}` + "\n")
}
func (s Store) recordCollectionPath(c string) string {
	parts := strings.Split(c, "/")
	if parts[0] == "sync" {
		return filepath.Join(s.syncRoot(), parts[1]+".json")
	}
	return filepath.Join(s.watchedRootBackupRoot(), parts[1]+".json")
}
func (s Store) collectionForPath(path string) string {
	for _, c := range recordCollections {
		if filepath.Clean(path) == s.recordCollectionPath(c) {
			return c
		}
	}
	return ""
}
func (s Store) recordsExist() bool { _, err := os.Stat(s.queueRecordsPath()); return err == nil }

// Open only under the existing cross-process lock; no long-lived database owner
// can block another CLI or let a read transaction outlive that lock.
func (s Store) openQueueRecords() (Store, error) {
	info, err := os.Lstat(s.queueRecordsPath())
	if os.IsNotExist(err) {
		return s, nil
	}
	if err != nil {
		return s, err
	}
	if !validQueueRecordFile(info) {
		return s, fmt.Errorf("invalid queue record store file")
	}
	db, err := bolt.Open(s.queueRecordsPath(), 0600, &bolt.Options{Timeout: time.Second})
	if err != nil {
		return s, err
	}
	if err = db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte("meta"))
		if b == nil || (string(b.Get([]byte("format"))) != queueRecordsFormat && string(b.Get([]byte("format"))) != "loom.node-queue.records.v1") {
			return fmt.Errorf("unsupported queue record store")
		}
		return nil
	}); err != nil {
		db.Close()
		return s, err
	}
	s.queueDB = db
	if err := upgradeRecordSummaries(db); err != nil {
		db.Close()
		s.queueDB = nil
		return s, err
	}
	var fenced bool
	err = db.View(func(tx *bolt.Tx) error {
		fenced = string(tx.Bucket([]byte("meta")).Get([]byte("fenced"))) == "yes"
		return nil
	})
	if err == nil && !fenced {
		err = s.fenceLegacyQueues()
	}
	if err != nil {
		db.Close()
		s.queueDB = nil
		return s, err
	}
	if err := ensureQueueReport(db); err != nil {
		db.Close()
		s.queueDB = nil
		return s, err
	}
	return s, nil
}

func validRecordCollection(c string) bool {
	for _, v := range recordCollections {
		if c == v {
			return true
		}
	}
	return false
}
func recordBucket(tx *bolt.Tx, c, kind string) *bolt.Bucket { return tx.Bucket([]byte(c + "/" + kind)) }
func initRecordDB(tx *bolt.Tx) error {
	for _, name := range []string{"meta", "sequence", "counts", "backup-summary", "backup-issues"} {
		if _, err := tx.CreateBucketIfNotExists([]byte(name)); err != nil {
			return err
		}
	}
	for _, c := range recordCollections {
		for _, kind := range []string{"rows", "order", "positions", "index"} {
			if _, err := tx.CreateBucketIfNotExists([]byte(c + "/" + kind)); err != nil {
				return err
			}
		}
	}
	return tx.Bucket([]byte("meta")).Put([]byte("format"), []byte(queueRecordsFormat))
}
func recordKey(c string, raw []byte) (string, error) {
	var target any
	switch c {
	case "sync/events":
		target = &LocalSyncEvent{}
	case "sync/objects":
		target = &LocalSyncObject{}
	case "sync/deletions":
		target = &LocalSyncDeletionRequest{}
	case "sync/outbox":
		target = &LocalSyncOutboxItem{}
	case "sync/cursors":
		target = &LocalSyncCursor{}
	case "sync/conflicts":
		target = &LocalSyncConflict{}
	case "backup/artifacts":
		target = &LocalWatchedRootBackupArtifact{}
	case "backup/batches":
		target = &LocalWatchedRootBackupBatch{}
	case "backup/items":
		target = &LocalWatchedRootBackupItem{}
	case "backup/outbox":
		target = &LocalWatchedRootBackupOutboxItem{}
	default:
		return "", fmt.Errorf("unknown queue collection")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return "", err
	}
	var x map[string]json.RawMessage
	if err := json.Unmarshal(raw, &x); err != nil {
		return "", err
	}
	field := map[string]string{"sync/events": "local_event_id", "sync/objects": "local_object_id", "sync/deletions": "local_deletion_id", "sync/outbox": "local_outbox_id", "sync/cursors": "stream_name", "sync/conflicts": "local_conflict_id", "backup/artifacts": "local_artifact_id", "backup/batches": "local_batch_id", "backup/items": "local_item_id", "backup/outbox": "local_outbox_id"}[c]
	var id string
	if field == "" || json.Unmarshal(x[field], &id) != nil || id == "" || strings.ContainsRune(id, 0) {
		return "", fmt.Errorf("invalid record identity in %s", c)
	}
	if c == "sync/objects" {
		var version string
		if json.Unmarshal(x["local_version_id"], &version) != nil || version == "" || strings.ContainsRune(version, 0) {
			return "", fmt.Errorf("invalid object version")
		}
		id += "\x00" + version
	}
	return id, nil
}

type queueRecordDetails struct {
	Root, Path, Ref, Status, Stream string
	Sequence                        int64
}

func recordDetails(tx *bolt.Tx, c string, raw []byte) queueRecordDetails {
	var x struct {
		Root     string `json:"root_key"`
		Path     string `json:"relative_path"`
		Ref      string `json:"local_ref"`
		Object   string `json:"local_object_id"`
		Status   string `json:"status"`
		Stream   string `json:"stream_name"`
		Sequence int64  `json:"local_sequence"`
		Metadata struct {
			Root string `json:"watched_root"`
			Path string `json:"relative_path"`
		} `json:"metadata"`
		Payload struct {
			Root    string `json:"root_key"`
			Path    string `json:"relative_path"`
			Object  string `json:"local_object_ref"`
			Version string `json:"local_version_ref"`
			Source  string `json:"source_path"`
		} `json:"payload_json"`
	}
	_ = json.Unmarshal(raw, &x)
	d := queueRecordDetails{Root: x.Root, Path: x.Path, Ref: x.Ref, Status: x.Status, Stream: x.Stream, Sequence: x.Sequence}
	if c == "sync/objects" {
		d.Root, d.Path, d.Ref = x.Metadata.Root, x.Metadata.Path, x.Object
		d.Stream = "object_blobs"
	}
	if c == "sync/deletions" {
		d.Stream = loomsync.StreamDeletionRequests
	}
	if c == "sync/outbox" {
		if x.Payload.Object != "" {
			d.Ref = x.Payload.Object
		}
		if x.Payload.Root != "" {
			d.Root, d.Path = x.Payload.Root, x.Payload.Path
		}
		if d.Root == "" && strings.HasPrefix(x.Payload.Source, "watched-root://") {
			d.Root, d.Path, _ = strings.Cut(strings.TrimPrefix(x.Payload.Source, "watched-root://"), "/")
		}
		if d.Root == "" && tx != nil {
			var object []byte
			if x.Payload.Version != "" {
				object = recordBucket(tx, "sync/objects", "rows").Get([]byte(d.Ref + "\x00" + x.Payload.Version))
			}
			if object == nil {
				prefix := []byte("ref\x00" + d.Ref + "\x00")
				cur := recordBucket(tx, "sync/objects", "index").Cursor()
				for k, v := cur.Seek(prefix); k != nil && bytes.HasPrefix(k, prefix); k, v = cur.Next() {
					object = recordBucket(tx, "sync/objects", "rows").Get(v)
				}
			}
			if object != nil {
				o := recordDetails(nil, "sync/objects", object)
				d.Root, d.Path = o.Root, o.Path
			}
		}
	}
	return d
}
func recordIndexKeys(d queueRecordDetails, id string) []string {
	keys := []string{"path\x00" + d.Root + "\x00" + d.Path + "\x00" + id, "ref\x00" + d.Ref + "\x00" + id}
	if d.Status != "accepted" && d.Status != "duplicate" && d.Status != "failed" && d.Status != "conflicted" && d.Status != localSyncStatusRetainedInternal && d.Status != localSyncStatusRetainedSuperseded {
		keys = append(keys, fmt.Sprintf("pending\x00%s\x00%s\x00%020d\x00%s", d.Root, d.Stream, d.Sequence, id), fmt.Sprintf("pending-all\x00%s\x00%020d\x00%s", d.Stream, d.Sequence, id))
	}
	return keys
}
func syncCountDelta(c *LocalSyncCounts, status string, delta int) {
	switch status {
	case "accepted":
		c.Accepted += delta
	case "duplicate":
		c.Duplicates += delta
	case "failed":
		c.Failed += delta
	case "conflicted":
		c.Conflicted += delta
	case localSyncStatusRetainedInternal:
		c.RetainedInternal += delta
	case localSyncStatusRetainedSuperseded:
		c.RetainedSuperseded += delta
	default:
		c.Pending += delta
	}
}
func updateRecordCounts(tx *bolt.Tx, c string, d queueRecordDetails, delta int) error {
	if c != "sync/outbox" && c != "backup/outbox" {
		return nil
	}
	b := tx.Bucket([]byte("counts"))
	key := []byte(c + "\x00" + d.Root)
	var count LocalSyncCounts
	if raw := b.Get(key); raw != nil {
		if err := json.Unmarshal(raw, &count); err != nil {
			return err
		}
	}
	syncCountDelta(&count, d.Status, delta)
	raw, err := json.Marshal(count)
	if err != nil {
		return err
	}
	return b.Put(key, raw)
}
func putQueueRecord(tx *bolt.Tx, c, id string, raw json.RawMessage) (bool, error) {
	b := recordBucket(tx, c, "rows")
	old := b.Get([]byte(id))
	if bytes.Equal(old, raw) {
		return false, nil
	}
	if err := updateQueueReport(tx, c, id, old, raw); err != nil {
		return false, err
	}
	index := recordBucket(tx, c, "index")
	if old != nil {
		if err := updateBackupSelectionIndexes(tx, c, id, old, false); err != nil {
			return false, err
		}
		if err := updateBackupSummary(tx, c, id, old, -1); err != nil {
			return false, err
		}
		d := recordDetails(tx, c, old)
		for _, k := range recordIndexKeys(d, id) {
			if err := index.Delete([]byte(k)); err != nil {
				return false, err
			}
		}
		if err := updateRecordCounts(tx, c, d, -1); err != nil {
			return false, err
		}
	}
	if raw == nil {
		if err := b.Delete([]byte(id)); err != nil {
			return false, err
		}
		pos := recordBucket(tx, c, "positions")
		if p := pos.Get([]byte(id)); p != nil {
			if err := recordBucket(tx, c, "order").Delete(p); err != nil {
				return false, err
			}
			if err := pos.Delete([]byte(id)); err != nil {
				return false, err
			}
		}
		return true, nil
	}
	if old == nil {
		order := recordBucket(tx, c, "order")
		n, err := order.NextSequence()
		if err != nil {
			return false, err
		}
		var pos [8]byte
		binary.BigEndian.PutUint64(pos[:], n)
		if err := order.Put(pos[:], []byte(id)); err != nil {
			return false, err
		}
		if err := recordBucket(tx, c, "positions").Put([]byte(id), pos[:]); err != nil {
			return false, err
		}
	}
	if err := b.Put([]byte(id), raw); err != nil {
		return false, err
	}
	if err := updateBackupSelectionIndexes(tx, c, id, raw, true); err != nil {
		return false, err
	}
	if err := updateBackupSummary(tx, c, id, raw, 1); err != nil {
		return false, err
	}
	d := recordDetails(tx, c, raw)
	for _, k := range recordIndexKeys(d, id) {
		if err := index.Put([]byte(k), []byte(id)); err != nil {
			return false, err
		}
	}
	if err := updateRecordCounts(tx, c, d, 1); err != nil {
		return false, err
	}
	if d.Stream != "" {
		seq := tx.Bucket([]byte("sequence"))
		v := seq.Get([]byte(d.Stream))
		if len(v) == 0 || binary.BigEndian.Uint64(v) < uint64(d.Sequence) {
			var n [8]byte
			binary.BigEndian.PutUint64(n[:], uint64(d.Sequence))
			if err := seq.Put([]byte(d.Stream), n[:]); err != nil {
				return false, err
			}
		}
	}
	return true, nil
}

func (s Store) readRecordRows(c string) (map[string]json.RawMessage, []string, error) {
	rows := map[string]json.RawMessage{}
	var order []string
	err := s.queueDB.View(func(tx *bolt.Tx) error {
		if s.recordView != nil && strings.HasPrefix(c, "sync/") && c != "sync/cursors" && c != "sync/conflicts" {
			for id := range s.recordView[c] {
				if raw := recordBucket(tx, c, "rows").Get([]byte(id)); raw != nil {
					rows[id] = bytes.Clone(raw)
					order = append(order, id)
				}
			}
			sort.Slice(order, func(i, j int) bool {
				return bytes.Compare(recordBucket(tx, c, "positions").Get([]byte(order[i])), recordBucket(tx, c, "positions").Get([]byte(order[j]))) < 0
			})
			return nil
		}
		return recordBucket(tx, c, "order").ForEach(func(_, id []byte) error {
			raw := recordBucket(tx, c, "rows").Get(id)
			if raw == nil {
				return fmt.Errorf("missing queue row")
			}
			rows[string(id)] = bytes.Clone(raw)
			order = append(order, string(id))
			return nil
		})
	})
	return rows, order, err
}
func (s Store) readRecordJSON(path string, target any) error {
	c := s.collectionForPath(path)
	if c == "" {
		return fmt.Errorf("unknown queue collection")
	}
	if s.syncBatch != nil {
		if value, ok := s.syncBatch.values[path]; ok {
			reflect.ValueOf(target).Elem().Set(reflect.ValueOf(value))
			return nil
		}
	}
	rows, order, err := s.readRecordRows(c)
	if err != nil {
		return err
	}
	original := rows
	if s.syncBatch != nil && strings.HasPrefix(c, "backup/") {
		rows, order = s.overlayBackupRecordChanges(c, rows, order)
	}
	slice := reflect.MakeSlice(reflect.TypeOf(target).Elem(), 0, len(order))
	for _, id := range order {
		element := reflect.New(slice.Type().Elem())
		if err := json.Unmarshal(rows[id], element.Interface()); err != nil {
			return err
		}
		slice = reflect.Append(slice, element.Elem())
	}
	reflect.ValueOf(target).Elem().Set(slice)
	if s.syncBatch != nil {
		s.syncBatch.values[path] = slice.Interface()
		s.syncBatch.recordOriginal[c] = original
		s.syncBatch.stats.RecordReads += len(original)
	}
	return nil
}
func (s Store) writeRecordJSON(path string, value any) error {
	if s.syncBatch == nil {
		var err error
		s, err = s.beginSyncBatch()
		if err != nil {
			return err
		}
		if err = s.writeRecordJSON(path, value); err != nil {
			return err
		}
		return s.commitSyncBatch()
	}
	c := s.collectionForPath(path)
	if c == "" {
		return fmt.Errorf("unknown queue collection")
	}
	if _, ok := s.syncBatch.recordOriginal[c]; !ok {
		target := reflect.New(reflect.TypeOf(value)).Interface()
		if err := s.readRecordJSON(path, target); err != nil {
			return err
		}
	}
	original := s.syncBatch.recordOriginal[c]
	changes := map[string]json.RawMessage{}
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
			return fmt.Errorf("duplicate queue identity in %s", c)
		}
		seen[id] = true
		if !bytes.Equal(raw, original[id]) {
			changes[id] = raw
		}
	}
	for id := range original {
		if !seen[id] {
			changes[id] = nil
		}
	}
	s.syncBatch.recordChanges[c] = changes
	s.syncBatch.values[path] = value
	return nil
}

func (s Store) applyRecordJournal(j queueBatchJournal) error {
	if j.Version != 3 || len(j.Files)+len(j.BackupFiles) != 0 || s.queueDB == nil {
		return fmt.Errorf("invalid record journal")
	}
	for c, rows := range j.Records {
		if !validRecordCollection(c) {
			return fmt.Errorf("unknown queue record collection")
		}
		for id, raw := range rows {
			// JSON null is the on-disk tombstone; a decoded RawMessage is not nil.
			if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
				rows[id] = nil
				raw = nil
			}
			if id == "" {
				return fmt.Errorf("empty queue key")
			}
			if raw != nil {
				key, err := recordKey(c, raw)
				if err != nil || key != id {
					return fmt.Errorf("invalid queue record identity")
				}
			}
		}
	}
	if err := s.queueDB.Update(func(tx *bolt.Tx) error {
		// Object identities must exist before deriving their outbox indexes.
		for _, c := range recordCollections {
			var ids []string
			for id := range j.Records[c] {
				ids = append(ids, id)
			}
			sort.Slice(ids, func(left, right int) bool {
				a, b := recordDetails(nil, c, j.Records[c][ids[left]]), recordDetails(nil, c, j.Records[c][ids[right]])
				if a.Sequence != b.Sequence {
					return a.Sequence < b.Sequence
				}
				return ids[left] < ids[right]
			})
			for _, id := range ids {
				raw := j.Records[c][id]
				if _, err := putQueueRecord(tx, c, id, raw); err != nil {
					return err
				}
			}
		}
		// Path-only redo and already-applied rows still commit a bbolt
		// transaction. Preserve a current derived index across those no-ops.
		if err := carryBackupSelectionWatermark(tx); err != nil {
			return err
		}
		return carryQueueReport(tx)
	}); err != nil {
		return err
	}
	ws := watchedroots.NewStore(s.DataDir)
	for _, p := range j.Paths {
		if err := ws.SavePathStateDurable(p); err != nil {
			return err
		}
	}
	if err := os.Remove(s.syncBatchJournalPath()); err != nil {
		return err
	}
	return syncQueueDirectory(s.syncRoot())
}
func (s Store) commitRecordBatch() error {
	j := queueBatchJournal{Version: 3, Records: map[string]map[string]json.RawMessage{}}
	for c, rows := range s.syncBatch.recordChanges {
		if len(rows) > 0 {
			j.Records[c] = rows
		}
	}
	var keys []string
	for k := range s.syncBatch.paths {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		j.Paths = append(j.Paths, s.syncBatch.paths[k])
	}
	if len(j.Records)+len(j.Paths) == 0 {
		return nil
	}
	started := time.Now()
	raw, err := json.Marshal(j)
	if err != nil {
		return err
	}
	if err := writeDurableQueueJSON(s.syncBatchJournalPath(), json.RawMessage(raw)); err != nil {
		return err
	}
	before := s.queueDB.Stats()
	if err := s.applyRecordJournal(j); err != nil {
		return err
	}
	after := s.queueDB.Stats()
	delta := after.Sub(&before)
	stats := &s.syncBatch.stats
	stats.RecordPageAllocBytes += delta.TxStats.GetPageAlloc()
	stats.RecordWriteCalls += delta.TxStats.GetWrite()
	stats.CommitPages++
	stats.CommitMillis += time.Since(started).Milliseconds()
	stats.JournalBytes += int64(len(raw) + 1)
	for c, rows := range j.Records {
		if s.syncBatch.recordOriginal[c] == nil {
			s.syncBatch.recordOriginal[c] = map[string]json.RawMessage{}
		}
		for id, raw := range rows {
			stats.RecordWrites++
			stats.RecordBytes += int64(len(raw))
			if raw == nil {
				delete(s.syncBatch.recordOriginal[c], id)
			} else {
				s.syncBatch.recordOriginal[c][id] = bytes.Clone(raw)
			}
		}
	}
	s.syncBatch.recordChanges = map[string]map[string]json.RawMessage{}
	s.syncBatch.paths = map[string]watchedroots.PathState{}
	return nil
}

func canonicalQueueRows(raw []byte) ([]json.RawMessage, error) {
	var rows []json.RawMessage
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if err := decoder.Decode(&rows); err != nil {
		return nil, err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, fmt.Errorf("trailing queue data")
	}
	return rows, nil
}
func queueDigest(rows []json.RawMessage) string {
	h := sha256.New()
	for _, r := range rows {
		var compact bytes.Buffer
		_ = json.Compact(&compact, r)
		h.Write(compact.Bytes())
		h.Write([]byte{'\n'})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// Migration is explicit. Its immutable baseline precedes publication, and every
// old writer is fenced before the supported stopped-owner cutover completes.
func (s Store) MigrateQueueRecords(ctx context.Context) (queueRecordReceipt, error) {
	s, unlock, err := s.lockLocalSync(ctx)
	if err != nil {
		return queueRecordReceipt{}, err
	}
	defer unlock()
	var receipt queueRecordReceipt
	if s.queueDB != nil {
		err := s.queueDB.View(func(tx *bolt.Tx) error {
			return json.Unmarshal(tx.Bucket([]byte("meta")).Get([]byte("migration")), &receipt)
		})
		return receipt, err
	}
	if err := s.EnsureSyncDataDirs(); err != nil {
		return receipt, err
	}
	if err := s.EnsureWatchedRootBackupDataDirs(); err != nil {
		return receipt, err
	}
	err = s.withWatchedRootBackupQueueLock(func(s Store) error {
		if err := os.MkdirAll(s.queueRecordsBaseline(), 0700); err != nil {
			return err
		}
		temp := s.queueRecordsPath() + ".migrating"
		if info, err := os.Lstat(temp); err == nil {
			if !validQueueRecordFile(info) {
				return fmt.Errorf("invalid unfinished queue import")
			}
		} else if !os.IsNotExist(err) {
			return err
		}
		db, err := bolt.Open(temp, 0600, nil)
		if err != nil {
			return err
		}
		defer db.Close()
		receipt = queueRecordReceipt{Format: queueRecordsFormat, Collections: map[string]queueRecordBaseline{}}
		err = db.Update(func(tx *bolt.Tx) error {
			// The unpublished import is disposable, but its source baseline is not.
			// Rebuild in one transaction; a changed baseline refuses below and rolls
			// this reset back too. A crash never activates a partial import.
			var buckets [][]byte
			if err := tx.ForEach(func(name []byte, _ *bolt.Bucket) error { buckets = append(buckets, bytes.Clone(name)); return nil }); err != nil {
				return err
			}
			for _, name := range buckets {
				if err := tx.DeleteBucket(name); err != nil {
					return err
				}
			}
			if err := initRecordDB(tx); err != nil {
				return err
			}
			for _, c := range recordCollections {
				path := s.recordCollectionPath(c)
				raw, err := os.ReadFile(path)
				if err != nil {
					return err
				}
				rows, err := canonicalQueueRows(raw)
				if err != nil {
					return fmt.Errorf("import %s: %w", c, err)
				}
				sum := sha256.Sum256(raw)
				receipt.Collections[c] = queueRecordBaseline{len(rows), hex.EncodeToString(sum[:]), queueDigest(rows)}
				baseline := filepath.Join(s.queueRecordsBaseline(), strings.ReplaceAll(c, "/", ".")+".json")
				if prior, err := os.ReadFile(baseline); err == nil {
					if !bytes.Equal(prior, raw) {
						return fmt.Errorf("queue baseline changed: %s", c)
					}
				} else if os.IsNotExist(err) {
					if err := writeDurableQueueBytes(baseline, raw); err != nil {
						return err
					}
				} else {
					return err
				}
				for _, row := range rows {
					id, err := recordKey(c, row)
					if err != nil {
						return err
					}
					if recordBucket(tx, c, "rows").Get([]byte(id)) != nil {
						return fmt.Errorf("duplicate import identity in %s", c)
					}
					var compact bytes.Buffer
					if err := json.Compact(&compact, row); err != nil {
						return err
					}
					if _, err := putQueueRecord(tx, c, id, compact.Bytes()); err != nil {
						return err
					}
				}
			}
			raw, _ := json.Marshal(receipt)
			return tx.Bucket([]byte("meta")).Put([]byte("migration"), raw)
		})
		if err != nil {
			return err
		}
		if err := db.View(func(tx *bolt.Tx) error { return verifyRecordImport(tx, receipt) }); err != nil {
			return err
		}
		if err := db.Close(); err != nil {
			return err
		}
		if err := os.Rename(temp, s.queueRecordsPath()); err != nil {
			return err
		}
		if err := syncQueueDirectory(s.syncRoot()); err != nil {
			return err
		}
		s, err = s.openQueueRecords()
		if err != nil {
			return err
		}
		defer s.queueDB.Close()
		return s.fenceLegacyQueues()
	})
	return receipt, err
}
func verifyRecordImport(tx *bolt.Tx, receipt queueRecordReceipt) error {
	for c, expected := range receipt.Collections {
		var rows []json.RawMessage
		if err := recordBucket(tx, c, "order").ForEach(func(_, id []byte) error { rows = append(rows, recordBucket(tx, c, "rows").Get(id)); return nil }); err != nil {
			return err
		}
		if len(rows) != expected.Count || queueDigest(rows) != expected.CanonicalSHA256 {
			return fmt.Errorf("queue import differs: %s", c)
		}
	}
	return nil
}
func (s Store) fenceLegacyQueues() error {
	var receipt queueRecordReceipt
	if err := s.queueDB.View(func(tx *bolt.Tx) error {
		return json.Unmarshal(tx.Bucket([]byte("meta")).Get([]byte("migration")), &receipt)
	}); err != nil {
		return err
	}
	for _, c := range recordCollections {
		expected, ok := receipt.Collections[c]
		if !ok {
			return fmt.Errorf("missing queue migration inventory")
		}
		baseline, err := os.ReadFile(filepath.Join(s.queueRecordsBaseline(), strings.ReplaceAll(c, "/", ".")+".json"))
		if err != nil {
			return err
		}
		hash := sha256.Sum256(baseline)
		if hex.EncodeToString(hash[:]) != expected.SHA256 {
			return fmt.Errorf("queue baseline identity mismatch: %s", c)
		}
		path := s.recordCollectionPath(c)
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if bytes.Equal(raw, recordMarker()) {
			continue
		}
		sum := sha256.Sum256(raw)
		if hex.EncodeToString(sum[:]) != expected.SHA256 {
			return fmt.Errorf("legacy queue changed after import: %s", c)
		}
		if err := writeDurableQueueJSON(path, json.RawMessage(bytes.TrimSpace(recordMarker()))); err != nil {
			return err
		}
	}
	return s.queueDB.Update(func(tx *bolt.Tx) error { return tx.Bucket([]byte("meta")).Put([]byte("fenced"), []byte("yes")) })
}

func (s Store) withRecordAccess(fn func(Store) error) error {
	locked, unlock, err := s.lockLocalSync(context.Background())
	if err != nil {
		return err
	}
	defer unlock()
	return fn(locked)
}

// Main's managed read-only ACL sets the group mask to r-- during activation.
// Creating private files remains 0600; neither mode permits group/other writes.
func validQueueRecordFile(info os.FileInfo) bool {
	return info.Mode().IsRegular() && (info.Mode().Perm() == 0600 || info.Mode().Perm() == 0640)
}

func (s Store) nextRecordSequence(stream string) (int64, error) {
	var maximum int64
	err := s.queueDB.View(func(tx *bolt.Tx) error {
		if v := tx.Bucket([]byte("sequence")).Get([]byte(stream)); len(v) > 0 {
			maximum = int64(binary.BigEndian.Uint64(v))
		}
		return nil
	})
	if s.syncBatch != nil {
		for c, rows := range s.syncBatch.recordChanges {
			for _, raw := range rows {
				d := recordDetails(nil, c, raw)
				if d.Stream == stream && d.Sequence > maximum {
					maximum = d.Sequence
				}
			}
		}
	}
	return maximum + 1, err
}
func (s Store) selectRecordTurn(root string, actions []watchedroots.OutputAction) (Store, error) {
	if s.queueDB == nil {
		return s, nil
	}
	view := map[string]map[string]bool{}
	for _, c := range recordCollections {
		view[c] = map[string]bool{}
	}
	err := s.queueDB.View(func(tx *bolt.Tx) error {
		selectPrefix := func(c, prefix string, limit int) {
			cur := recordBucket(tx, c, "index").Cursor()
			p := []byte(prefix)
			n := 0
			for k, id := cur.Seek(p); k != nil && bytes.HasPrefix(k, p); k, id = cur.Next() {
				view[c][string(id)] = true
				n++
				if limit > 0 && n >= limit {
					break
				}
			}
		}
		// A normal page depends on at most the selected pending deliveries. Exact
		// path history is retained for replay/metadata matching, never other paths.
		selectPrefix("sync/outbox", "pending\x00"+root+"\x00", localQueueCommitPageSize)
		for _, action := range actions {
			prefix := "path\x00" + root + "\x00" + action.RelativePath + "\x00"
			selectPrefix("sync/objects", prefix, 0)
			selectPrefix("sync/outbox", prefix, 0)
			selectPrefix("sync/deletions", prefix, 0)
		}
		for id := range view["sync/outbox"] {
			raw := recordBucket(tx, "sync/outbox", "rows").Get([]byte(id))
			var item LocalSyncOutboxItem
			if err := json.Unmarshal(raw, &item); err != nil {
				return err
			}
			switch item.ItemKind {
			case loomsync.ItemKindObjectBlob, loomsync.ItemKindObjectMetadata:
				d := recordDetails(tx, "sync/outbox", raw)
				selectPrefix("sync/objects", "ref\x00"+d.Ref+"\x00", 0)
			case loomsync.ItemKindEvent:
				view["sync/events"][item.LocalRef] = true
			case loomsync.ItemKindDeletionRequest:
				view["sync/deletions"][item.LocalRef] = true
			}
		}
		return nil
	})
	s.recordView = view
	return s, err
}
func (s Store) recordSyncCounts() (map[string]LocalSyncCounts, error) {
	counts := map[string]LocalSyncCounts{}
	err := s.queueDB.View(func(tx *bolt.Tx) error {
		prefix := []byte("sync/outbox\x00")
		cur := tx.Bucket([]byte("counts")).Cursor()
		for k, raw := cur.Seek(prefix); k != nil && bytes.HasPrefix(k, prefix); k, raw = cur.Next() {
			var count LocalSyncCounts
			if err := json.Unmarshal(raw, &count); err != nil {
				return err
			}
			counts[string(k[len(prefix):])] = count
		}
		if s.syncBatch != nil {
			for id, raw := range s.syncBatch.recordChanges["sync/outbox"] {
				old := recordBucket(tx, "sync/outbox", "rows").Get([]byte(id))
				for i, r := range [][]byte{old, raw} {
					if r == nil {
						continue
					}
					d := recordDetails(tx, "sync/outbox", r)
					c := counts[d.Root]
					delta := 1
					if i == 0 {
						delta = -1
					}
					syncCountDelta(&c, d.Status, delta)
					counts[d.Root] = c
				}
			}
		}
		return nil
	})
	return counts, err
}
func (s Store) recordBackupDeliveryCounts() (map[string]backupDeliveryCounts, error) {
	counts := map[string]backupDeliveryCounts{}
	err := s.queueDB.View(func(tx *bolt.Tx) error {
		prefix := []byte("pending-all\x00")
		cur := recordBucket(tx, "backup/outbox", "index").Cursor()
		for k, id := cur.Seek(prefix); k != nil && bytes.HasPrefix(k, prefix); k, id = cur.Next() {
			var item LocalWatchedRootBackupOutboxItem
			if err := json.Unmarshal(recordBucket(tx, "backup/outbox", "rows").Get(id), &item); err != nil {
				return err
			}
			c := counts[item.RootKey]
			c.Pending++
			if watchedRootBackupHasError(item.LastErrorCode, item.LastErrorMessage) {
				c.Retryable++
			}
			counts[item.RootKey] = c
		}
		return nil
	})
	return counts, err
}
func (s Store) queueStatusPaths(paths []string) []string {
	if s.queueDB != nil || s.recordsExist() {
		return []string{s.queueRecordsPath()}
	}
	return paths
}
