package nodeagent

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	bolt "go.etcd.io/bbolt"
	"loom.local/loom/internal/nodeagent/watchedroots"
)

type watchedRootBackupQueue struct {
	artifacts []LocalWatchedRootBackupArtifact
	batches   []LocalWatchedRootBackupBatch
	items     []LocalWatchedRootBackupItem
	outbox    []LocalWatchedRootBackupOutboxItem
}

// Full compatibility reads remain available for legacy nodes and explicit
// export/audit. Record-mode producers use only the path or batch they own.
func (s Store) loadWatchedRootBackupQueue() (q watchedRootBackupQueue, err error) {
	if q.artifacts, err = s.LoadWatchedRootBackupArtifacts(); err != nil {
		return q, err
	}
	if q.batches, err = s.LoadWatchedRootBackupBatches(); err != nil {
		return q, err
	}
	if q.items, err = s.LoadWatchedRootBackupItems(); err != nil {
		return q, err
	}
	q.outbox, err = s.LoadWatchedRootBackupOutbox()
	return q, err
}

func (s Store) loadWatchedRootBackupAction(action watchedroots.OutputAction) (q watchedRootBackupQueue, err error) {
	if s.queueDB == nil {
		return s.loadWatchedRootBackupQueue()
	}
	prefix := backupActionIndexPrefix(action.RootKey, action.RelativePath, action.BackupItemKind, action.ContentHashURI, action.PreviousHashURI)
	q.items, err = readBackupRecordSelection[LocalWatchedRootBackupItem](s, "backup/items", prefix, nil, 0)
	if err != nil {
		return q, err
	}
	var batches []string
	seen := map[string]bool{}
	for _, item := range q.items {
		if seen[item.LocalBatchID] {
			continue
		}
		seen[item.LocalBatchID] = true
		batches = append(batches, item.LocalBatchID)
		outbox, err := readBackupRecordSelection[LocalWatchedRootBackupOutboxItem](s, "backup/outbox", "ref\x00"+item.LocalBatchID+"\x00", nil, 0)
		if err != nil {
			return q, err
		}
		q.outbox = append(q.outbox, outbox...)
	}
	q.batches, err = readBackupRecordSelection[LocalWatchedRootBackupBatch](s, "backup/batches", "", batches, 0)
	return q, err
}

func (s Store) loadWatchedRootBackupBatch(outbox LocalWatchedRootBackupOutboxItem) (q watchedRootBackupQueue, err error) {
	if s.queueDB == nil {
		return s.loadWatchedRootBackupQueue()
	}
	q.outbox, err = readBackupRecordSelection[LocalWatchedRootBackupOutboxItem](s, "backup/outbox", "", []string{outbox.LocalOutboxID}, 0)
	if err != nil {
		return q, err
	}
	q.batches, err = readBackupRecordSelection[LocalWatchedRootBackupBatch](s, "backup/batches", "", []string{outbox.LocalRef}, 0)
	if err != nil {
		return q, err
	}
	q.items, err = readBackupRecordSelection[LocalWatchedRootBackupItem](s, "backup/items", "backup-batch\x00"+outbox.LocalRef+"\x00", nil, 0)
	if err != nil {
		return q, err
	}
	var artifacts []string
	for _, item := range q.items {
		artifacts = append(artifacts, item.LocalArtifactID)
	}
	q.artifacts, err = readBackupRecordSelection[LocalWatchedRootBackupArtifact](s, "backup/artifacts", "", artifacts, 0)
	return q, err
}

func (s Store) pendingWatchedRootBackupOutbox(root string, limit int) ([]LocalWatchedRootBackupOutboxItem, error) {
	if s.queueDB != nil {
		prefix := "backup-ready-all\x00"
		if root != "" {
			prefix = "backup-ready\x00" + root + "\x00"
		}
		return readBackupRecordSelection[LocalWatchedRootBackupOutboxItem](s, "backup/outbox", prefix, nil, limit)
	}
	outbox, err := s.LoadWatchedRootBackupOutbox()
	if err != nil {
		return nil, err
	}
	var pending []LocalWatchedRootBackupOutboxItem
	for _, item := range pendingWatchedRootBackupOutboxItems(outbox) {
		if root == "" || root == item.RootKey {
			pending = append(pending, item)
			if limit > 0 && len(pending) == limit {
				break
			}
		}
	}
	return pending, nil
}

// Derived counters/issues share the row transaction. They never replace the
// retained records. A format change prevents an older writer leaving them stale.
func upgradeRecordSummaries(db *bolt.DB) error {
	var format string
	if err := db.View(func(tx *bolt.Tx) error { format = string(tx.Bucket([]byte("meta")).Get([]byte("format"))); return nil }); err != nil {
		return err
	}
	if format == queueRecordsFormat {
		return nil
	}
	if format != "loom.node-queue.records.v1" {
		return fmt.Errorf("unsupported queue record store")
	}
	return db.Update(func(tx *bolt.Tx) error {
		if _, err := tx.CreateBucketIfNotExists([]byte("backup-summary")); err != nil {
			return err
		}
		if _, err := tx.CreateBucketIfNotExists([]byte("backup-issues")); err != nil {
			return err
		}
		for _, c := range recordCollections {
			if !strings.HasPrefix(c, "backup/") {
				continue
			}
			if err := recordBucket(tx, c, "rows").ForEach(func(id, raw []byte) error { return updateBackupSummary(tx, c, string(id), raw, 1) }); err != nil {
				return err
			}
		}
		return tx.Bucket([]byte("meta")).Put([]byte("format"), []byte(queueRecordsFormat))
	})
}
func backupContribution(c string, raw []byte) (string, LocalWatchedRootBackupCounts, []LocalWatchedRootBackupError) {
	var root string
	var counts LocalWatchedRootBackupCounts
	var issues []LocalWatchedRootBackupError
	switch c {
	case "backup/artifacts":
		var item LocalWatchedRootBackupArtifact
		_ = json.Unmarshal(raw, &item)
		root = item.RootKey
		counts.Artifacts = 1
		issues = watchedRootBackupErrors("", []LocalWatchedRootBackupArtifact{item}, nil, nil, nil)
	case "backup/batches":
		var item LocalWatchedRootBackupBatch
		_ = json.Unmarshal(raw, &item)
		root = item.RootKey
		counts.Batches = 1
		issues = watchedRootBackupErrors("", nil, []LocalWatchedRootBackupBatch{item}, nil, nil)
	case "backup/items":
		var item LocalWatchedRootBackupItem
		_ = json.Unmarshal(raw, &item)
		root = item.RootKey
		counts.Items = 1
		issues = watchedRootBackupErrors("", nil, nil, []LocalWatchedRootBackupItem{item}, nil)
	case "backup/outbox":
		var item LocalWatchedRootBackupOutboxItem
		_ = json.Unmarshal(raw, &item)
		root = item.RootKey
		issues = watchedRootBackupErrors("", nil, nil, nil, []LocalWatchedRootBackupOutboxItem{item})
		switch item.Status {
		case "accepted":
			counts.Accepted = 1
		case "duplicate":
			counts.Duplicates = 1
		case "conflicted":
			counts.Conflicted = 1
		case "failed":
			counts.Failed = 1
			counts.ManualAction = 1
		default:
			counts.Pending = 1
			if watchedRootBackupHasError(item.LastErrorCode, item.LastErrorMessage) {
				counts.Retryable = 1
			}
		}
	}
	return root, counts, issues
}
func addBackupCounts(c *LocalWatchedRootBackupCounts, d LocalWatchedRootBackupCounts, n int) {
	c.Artifacts += d.Artifacts * n
	c.Batches += d.Batches * n
	c.Items += d.Items * n
	c.Pending += d.Pending * n
	c.Retryable += d.Retryable * n
	c.Accepted += d.Accepted * n
	c.Duplicates += d.Duplicates * n
	c.Conflicted += d.Conflicted * n
	c.Failed += d.Failed * n
	c.ManualAction += d.ManualAction * n
}
func updateBackupSummary(tx *bolt.Tx, c, id string, raw []byte, delta int) error {
	if !strings.HasPrefix(c, "backup/") {
		return nil
	}
	root, contribution, issues := backupContribution(c, raw)
	bucket := tx.Bucket([]byte("backup-summary"))
	var counts LocalWatchedRootBackupCounts
	if value := bucket.Get([]byte(root)); value != nil {
		if err := json.Unmarshal(value, &counts); err != nil {
			return err
		}
	}
	addBackupCounts(&counts, contribution, delta)
	value, err := json.Marshal(counts)
	if err != nil {
		return err
	}
	if err := bucket.Put([]byte(root), value); err != nil {
		return err
	}
	key := []byte(root + "\x00" + c + "\x00" + id)
	if delta < 0 || len(issues) == 0 {
		return tx.Bucket([]byte("backup-issues")).Delete(key)
	}
	value, err = json.Marshal(issues)
	if err != nil {
		return err
	}
	return tx.Bucket([]byte("backup-issues")).Put(key, value)
}
func (s Store) recordBackupStatus(config Config, state State, root string) (LocalWatchedRootBackupStatus, error) {
	status := LocalWatchedRootBackupStatus{NodeID: state.NodeID, NodeKey: config.NodeKey, RootKey: root, Paths: s.watchedRootBackupPaths(), Limits: s.watchedRootBackupLimits(root)}
	issues := map[string][]LocalWatchedRootBackupError{}
	err := s.queueDB.View(func(tx *bolt.Tx) error {
		if root != "" {
			if raw := tx.Bucket([]byte("backup-summary")).Get([]byte(root)); raw != nil {
				if err := json.Unmarshal(raw, &status.Counts); err != nil {
					return err
				}
			}
		} else {
			if err := tx.Bucket([]byte("backup-summary")).ForEach(func(_, raw []byte) error {
				var c LocalWatchedRootBackupCounts
				if err := json.Unmarshal(raw, &c); err != nil {
					return err
				}
				addBackupCounts(&status.Counts, c, 1)
				return nil
			}); err != nil {
				return err
			}
		}
		cur := tx.Bucket([]byte("backup-issues")).Cursor()
		prefix := []byte{}
		if root != "" {
			prefix = []byte(root + "\x00")
		}
		for k, v := cur.Seek(prefix); k != nil && bytes.HasPrefix(k, prefix); k, v = cur.Next() {
			var list []LocalWatchedRootBackupError
			if err := json.Unmarshal(v, &list); err != nil {
				return err
			}
			issues[string(k)] = list
		}
		if s.syncBatch != nil {
			for c, rows := range s.syncBatch.recordChanges {
				if !strings.HasPrefix(c, "backup/") {
					continue
				}
				for id, raw := range rows {
					old := recordBucket(tx, c, "rows").Get([]byte(id))
					for i, value := range [][]byte{old, raw} {
						if value == nil {
							continue
						}
						r, count, list := backupContribution(c, value)
						if root != "" && r != root {
							continue
						}
						n := 1
						if i == 0 {
							n = -1
						}
						addBackupCounts(&status.Counts, count, n)
						key := r + "\x00" + c + "\x00" + id
						if i == 0 || len(list) == 0 {
							delete(issues, key)
						} else {
							issues[key] = list
						}
					}
				}
			}
		}
		// Only pending outbox identities need batch-byte lookups.
		pending := map[string]LocalWatchedRootBackupOutboxItem{}
		p := []byte("pending-all\x00")
		if root != "" {
			p = []byte("pending\x00" + root + "\x00")
		}
		cursor := recordBucket(tx, "backup/outbox", "index").Cursor()
		for k, id := cursor.Seek(p); k != nil && bytes.HasPrefix(k, p); k, id = cursor.Next() {
			var item LocalWatchedRootBackupOutboxItem
			if s.syncBatch != nil {
				s.syncBatch.stats.RecordIndexReads++
				s.syncBatch.stats.RecordReads++
			}
			if err := json.Unmarshal(recordBucket(tx, "backup/outbox", "rows").Get(id), &item); err != nil {
				return err
			}
			pending[string(id)] = item
		}
		if s.syncBatch != nil {
			for id, raw := range s.syncBatch.recordChanges["backup/outbox"] {
				delete(pending, id)
				if raw != nil {
					var item LocalWatchedRootBackupOutboxItem
					_ = json.Unmarshal(raw, &item)
					if (root == "" || item.RootKey == root) && item.Status != "accepted" && item.Status != "duplicate" && item.Status != "failed" && item.Status != "conflicted" {
						pending[id] = item
					}
				}
			}
		}
		for _, item := range pending {
			raw := recordBucket(tx, "backup/batches", "rows").Get([]byte(item.LocalRef))
			if s.syncBatch != nil && raw != nil {
				s.syncBatch.stats.RecordReads++
			}
			if s.syncBatch != nil {
				if staged, ok := s.syncBatch.recordChanges["backup/batches"][item.LocalRef]; ok {
					raw = staged
				}
			}
			if raw != nil {
				var batch LocalWatchedRootBackupBatch
				if err := json.Unmarshal(raw, &batch); err != nil {
					return err
				}
				status.Counts.PendingBytes += batch.TotalBytes
			}
		}
		return nil
	})
	for _, list := range issues {
		status.Failed = append(status.Failed, list...)
	}
	sort.Slice(status.Failed, func(i, j int) bool {
		a, _ := json.Marshal(status.Failed[i])
		b, _ := json.Marshal(status.Failed[j])
		return bytes.Compare(a, b) < 0
	})
	if root != "" {
		if summary, err := watchedroots.NewStore(s.DataDir).LoadLatestSummary(root); err == nil {
			status.Counts.Protected, status.Counts.Ignored = summary.Included, summary.Excluded
			status.PolicyVersion, status.PolicyFingerprint = summary.PolicyVersion, summary.PolicyFingerprint
		}
	}
	return status, err
}
