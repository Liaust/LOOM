package storagecatalog

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"loom.local/loom/internal/ids"
)

func TestCommitArchiveRollsBackEveryEarlierMutationOnLateFailure(t *testing.T) {
	state := &archiveCommitDriverState{failAt: 4}
	db := sql.OpenDB(archiveCommitConnector{state: state})
	defer db.Close()
	_, err := NewService(db).CommitArchive(context.Background(), validArchiveCommitInput())
	if err == nil || !strings.Contains(err.Error(), "injected archive commit failure") {
		t.Fatalf("CommitArchive error = %v", err)
	}
	if state.committed || !state.rolledBack || state.execCount != 4 {
		t.Fatalf("transaction state after late failure = %#v", state)
	}
}

func TestCommitArchiveCommitsCompleteGenerationOnce(t *testing.T) {
	state := &archiveCommitDriverState{}
	db := sql.OpenDB(archiveCommitConnector{state: state})
	defer db.Close()
	result, err := NewService(db).CommitArchive(context.Background(), validArchiveCommitInput())
	if err != nil {
		t.Fatalf("CommitArchive returned error: %v", err)
	}
	if !state.committed || state.rolledBack || state.execCount != 5 {
		t.Fatalf("transaction state after success = %#v", state)
	}
	if result.Manifest.Status != "complete" || len(result.Entries) != 1 || len(result.Refs) != 1 {
		t.Fatalf("commit result = %#v", result)
	}
}

func validArchiveCommitInput() ArchiveCommitInput {
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	manifestID := ids.NewStorageArchiveManifestID()
	entryID := ids.NewStorageEntryID()
	refID := ids.NewStoragePhysicalRefID()
	sourceID := ids.NewStorageEntryID()
	size := int64(7)
	return ArchiveCommitInput{
		Manifest: CreateArchiveManifestInput{
			ArchiveManifestID: manifestID, ArchiveKey: "test-key", ArchiveKind: "document_archive",
			OwnerNodeKey: "main", SourceRef: sourceID, Status: "complete",
			ManifestJSON: json.RawMessage(`{"schema_version":"storage.archive_manifest.v0.7"}`), FinalizedAt: &now,
		},
		Entries: []ArchiveCommitEntryInput{{
			Entry: RegisterEntryInput{
				StorageEntryID: entryID, StorageClass: StorageClassArchiveEntry, SourceArea: SourceAreaMainArchive,
				OriginNodeKey: "main", ArchiveManifestID: manifestID, LogicalPath: "Documents/test.txt",
				CurrentViewPath: "main/Archive/Documents/test.txt", ChecksumAlgorithm: "sha256",
				ChecksumHex: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", SizeBytes: &size,
				FileClass: FileClassText, ProcessingState: ProcessingStateMetadataOnly,
				AvailabilityState: AvailabilityStateAvailable, RetentionState: RetentionStateRetained,
				Metadata: json.RawMessage(`{"source":"test"}`),
			},
			Ref: RegisterPhysicalRefInput{
				StoragePhysicalRefID: refID, StorageEntryID: entryID, RefKind: PhysicalRefKindArchiveFile,
				URI: "/srv/loom/storage/archive/test-key/objects/sha256/aa/aaaaaaaa", NodeKey: "main",
				ContentAddress: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
				Status:         PhysicalRefStatusAvailable, Metadata: json.RawMessage(`{"source":"test"}`),
			},
			Item: ArchiveItemInput{ArchiveManifestID: manifestID, StorageEntryID: entryID, ArchivePath: "Documents/test.txt", Metadata: json.RawMessage(`{"source":"test"}`)},
		}},
		SourceDispositions: []ArchiveSourceDispositionInput{{
			StorageEntryID: sourceID, ExpectedAvailability: AvailabilityStateAvailable, AvailabilityState: AvailabilityStateArchived,
		}},
	}
}

type archiveCommitDriverState struct {
	execCount  int
	failAt     int
	committed  bool
	rolledBack bool
}

type archiveCommitConnector struct{ state *archiveCommitDriverState }

func (c archiveCommitConnector) Connect(context.Context) (driver.Conn, error) {
	return &archiveCommitConn{state: c.state}, nil
}
func (c archiveCommitConnector) Driver() driver.Driver { return archiveCommitDriver{} }

type archiveCommitDriver struct{}

func (archiveCommitDriver) Open(string) (driver.Conn, error) { return nil, fmt.Errorf("use connector") }

type archiveCommitConn struct {
	state *archiveCommitDriverState
	inTx  bool
}

func (c *archiveCommitConn) Prepare(string) (driver.Stmt, error) {
	return nil, fmt.Errorf("unsupported")
}
func (c *archiveCommitConn) Close() error { return nil }
func (c *archiveCommitConn) Begin() (driver.Tx, error) {
	c.inTx = true
	return archiveCommitTx{conn: c}, nil
}
func (c *archiveCommitConn) ExecContext(context.Context, string, []driver.NamedValue) (driver.Result, error) {
	if !c.inTx {
		return nil, fmt.Errorf("exec outside transaction")
	}
	c.state.execCount++
	if c.state.failAt == c.state.execCount {
		return nil, errors.New("injected archive commit failure")
	}
	return driver.RowsAffected(1), nil
}

type archiveCommitTx struct{ conn *archiveCommitConn }

func (tx archiveCommitTx) Commit() error {
	tx.conn.inTx = false
	tx.conn.state.committed = true
	return nil
}
func (tx archiveCommitTx) Rollback() error {
	tx.conn.inTx = false
	tx.conn.state.rolledBack = true
	return nil
}
