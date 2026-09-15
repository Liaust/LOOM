package runtime

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

// This immutable per-call envelope is separate from the retry queue's mutable
// status. It survives every generic queue transition and dispatch ACK loss.
type ApplicationResultRecord struct {
	DispatchDigest string          `json:"dispatch_digest"`
	Envelope       json.RawMessage `json:"envelope"`
	Outbox         OutboxItem      `json:"outbox"`
}

func ApplicationResultKey(callID string) string {
	h := sha256.Sum256([]byte(callID))
	return "application-" + hex.EncodeToString(h[:])
}
func (s Store) applicationResultLocation(key string) (projectArchiveFileLocation, error) {
	if len(key) != 76 || key[:12] != "application-" {
		return projectArchiveFileLocation{}, fmt.Errorf("invalid application result key")
	}
	if _, e := hex.DecodeString(key[12:]); e != nil {
		return projectArchiveFileLocation{}, e
	}
	rootFD, root, rootStat, e := openDirectoryPathNoFollow(s.DataDir, true)
	if e != nil {
		return projectArchiveFileLocation{}, e
	}
	defer unix.Close(rootFD)
	if e = unix.Mkdirat(rootFD, "application-results", 0700); e != nil && !errors.Is(e, unix.EEXIST) {
		return projectArchiveFileLocation{}, e
	}
	if e = unix.Fsync(rootFD); e != nil {
		return projectArchiveFileLocation{}, e
	}
	fd, e := unix.Openat(rootFD, "application-results", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if e != nil {
		return projectArchiveFileLocation{}, e
	}
	var st unix.Stat_t
	if e = unix.Fstat(fd, &st); e != nil || st.Uid != uint32(os.Geteuid()) || st.Mode&0777 != 0700 {
		unix.Close(fd)
		return projectArchiveFileLocation{}, fmt.Errorf("application result directory custody")
	}
	directory := &secureDirectory{fd: fd, root: root, components: []string{"application-results"}, rootStat: rootStat, leafStat: st}
	if e = directory.verify(); e != nil {
		directory.close()
		return projectArchiveFileLocation{}, e
	}
	return projectArchiveFileLocation{directory: directory, name: key + ".json", path: filepath.Join(root, "application-results", key+".json")}, nil
}
func (s Store) ReadApplicationResult(key string) (ApplicationResultRecord, error) {
	var record ApplicationResultRecord
	location, e := s.applicationResultLocation(key)
	if e != nil {
		return record, e
	}
	defer location.directory.close()
	b, _, e := s.readSecureBoundedFile(location, 512*1024)
	if e != nil {
		return record, e
	}
	e = decodeCanonicalSecureJSON(b, &record)
	return record, e
}
func (s Store) PublishApplicationResult(key string, record ApplicationResultRecord) error {
	if record.DispatchDigest == "" || !json.Valid(record.Envelope) || record.Outbox.LocalOutboxID != key || record.Outbox.Kind != OutboxKindCapabilityResult || !bytes.Equal(record.Envelope, record.Outbox.PayloadJSON) {
		return fmt.Errorf("invalid application result envelope")
	}
	location, e := s.applicationResultLocation(key)
	if e != nil {
		return e
	}
	defer location.directory.close()
	b, e := json.Marshal(record)
	if e != nil {
		return e
	}
	e = s.writeSecureDurableFile(location, b, 512*1024)
	if errors.Is(e, fs.ErrExist) {
		old, readErr := s.ReadApplicationResult(key)
		if readErr != nil {
			return readErr
		}
		oldRaw, _ := json.Marshal(old)
		if !bytes.Equal(oldRaw, b) {
			return fmt.Errorf("application result envelope conflict")
		}
		return nil
	}
	return e
}
func (s Store) QueueApplicationResult(record ApplicationResultRecord) (OutboxItem, error) {
	for _, status := range []string{OutboxStatusDone, OutboxStatusInflight, OutboxStatusPending, OutboxStatusFailed, OutboxStatusManualAction} {
		p := s.outboxPath(status, record.Outbox.LocalOutboxID)
		if _, e := os.Lstat(p); e == nil {
			var item OutboxItem
			if e = readJSONFile(p, &item); e != nil {
				return item, e
			}
			if !applicationJSONEqual(item.PayloadJSON, record.Envelope) {
				return item, fmt.Errorf("application queued envelope conflict")
			}
			return item, nil
		} else if !errors.Is(e, fs.ErrNotExist) {
			return OutboxItem{}, e
		}
	}
	item, e := s.QueueOutbox(record.Outbox)
	if e != nil {
		return item, e
	}
	path := s.outboxPath(item.Status, item.LocalOutboxID)
	f, e := os.OpenFile(path, os.O_RDONLY, 0)
	if e != nil {
		return item, e
	}
	e = f.Sync()
	_ = f.Close()
	if e != nil {
		return item, e
	}
	dir, e := os.Open(filepath.Dir(path))
	if e != nil {
		return item, e
	}
	defer dir.Close()
	return item, dir.Sync()
}

// Restore missing queue entries after a crash during a generic queue status
// transition. It retries only frozen bytes; it never executes an application.
func (s Store) RestoreApplicationResults(limit int) error {
	dir := filepath.Join(s.DataDir, "application-results")
	entries, e := os.ReadDir(dir)
	if errors.Is(e, fs.ErrNotExist) {
		return nil
	}
	if e != nil {
		return e
	}
	if limit <= 0 {
		limit = 128
	}
	// This cursor is only a scan hint, never result authority. Losing it safely
	// restarts a scan. Retained result history must not halt unrelated outboxes
	// when it grows beyond one bounded batch.
	var cursor struct {
		After string `json:"after"`
	}
	cursorPath := filepath.Join(dir, ".cursor.json")
	if e = readJSONFile(cursorPath, &cursor); e != nil && !errors.Is(e, fs.ErrNotExist) {
		return e
	}
	names := []string{}
	for _, entry := range entries {
		name := entry.Name()
		if name == ".cursor.json" || strings.HasPrefix(name, ".publish-") || strings.HasPrefix(name, ".tmp-.cursor.json-") {
			continue
		}
		if len(name) != 81 || filepath.Ext(name) != ".json" {
			return fmt.Errorf("unexpected application result entry")
		}
		names = append(names, name)
	}
	start := 0
	for start < len(names) && names[start] <= cursor.After {
		start++
	}
	if start == len(names) {
		start = 0
	}
	for i := start; i < len(names) && i < start+limit; i++ {
		record, e := s.ReadApplicationResult(names[i][:76])
		if e != nil {
			return e
		}
		if _, e = s.QueueApplicationResult(record); e != nil {
			return e
		}
		cursor.After = names[i]
	}
	if len(names) > 0 {
		return writeJSONFile(cursorPath, cursor, 0600)
	}
	return nil
}

func NewApplicationResultOutbox(callID, key, correlationID, messageID string, envelope json.RawMessage, createdAt time.Time) OutboxItem {
	return OutboxItem{LocalOutboxID: ApplicationResultKey(callID), Kind: OutboxKindCapabilityResult, Status: OutboxStatusPending, IdempotencyKey: key, CorrelationID: correlationID, CreatedAt: createdAt, PayloadJSON: envelope, SourceWorkerKey: WorkerKeyPoll, SourceMessageID: messageID}
}

func applicationJSONEqual(a, b []byte) bool {
	var aa, bb bytes.Buffer
	return json.Compact(&aa, a) == nil && json.Compact(&bb, b) == nil && bytes.Equal(aa.Bytes(), bb.Bytes())
}
