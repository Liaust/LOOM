package sync_test

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5/stdlib"
	"testing"
	"time"

	"loom.local/loom/internal/ids"
	"loom.local/loom/internal/nodeagent"
	"loom.local/loom/internal/nodeagent/watchedroots"
	"loom.local/loom/internal/nodes"
	"loom.local/loom/internal/objects"
	"loom.local/loom/internal/objectstore"
	"loom.local/loom/internal/search"
	loomsync "loom.local/loom/internal/sync"
	mainroots "loom.local/loom/internal/watchedroots"
	"loom.local/loom/internal/workers"
)

func TestFileObservationPostgres(t *testing.T) {
	db, req := metadataDatabase(t)
	ctx := t.Context()
	s := loomsync.NewService(db)
	node, err := nodes.NewService(db).GetNode(ctx, "main")
	if err != nil {
		t.Fatal(err)
	}
	cred, err := nodes.NewService(db).IssueNodeCredential(ctx, req, nodes.IssueNodeCredentialInput{NodeRef: node.NodeID, Reason: "file identity fixture"})
	if err != nil {
		t.Fatal(err)
	}
	localID := ids.NewObjectID()
	now := time.Now().UTC().Truncate(time.Microsecond)
	p := loomsync.FileObservation{SchemaVersion: loomsync.FileObservationSchema, LocalObjectRef: localID,
		RootKey: "files", RelativePath: "note.md", Generation: "generation", Presence: "present", ObservedAt: now, SourceMtime: &now, SizeBytes: 3}
	push := func(sequence int64, p loomsync.FileObservation, ref string) (loomsync.PushBatchResult, error) {
		t.Helper()
		raw, _ := json.Marshal(p)
		return s.PushBatch(ctx, req, loomsync.PushBatchInput{NodeRef: node.NodeID, CredentialToken: cred.CredentialToken,
			Items: []loomsync.PushBatchItem{{ItemKind: loomsync.ItemKindObjectMetadata, StreamName: loomsync.StreamObjectMetadata,
				LocalSequence: sequence, LocalRef: ref, PayloadJSON: raw}}})
	}
	firstRef := ids.NewLocalOutboxID()
	if _, err := push(1, p, firstRef); err == nil {
		t.Fatal("unreported root was admitted")
	}
	config, _ := json.Marshal(watchedroots.RootConfig{RootKey: p.RootKey, SyncPolicy: watchedroots.SyncPolicy{ScopeRef: req.ScopeID}})
	if _, err := mainroots.NewService(db).Report(ctx, mainroots.ReportInput{NodeRef: node.NodeID, CredentialToken: cred.CredentialToken,
		RootKey: p.RootKey, ConfigHash: p.Generation, ConfigJSON: config, Status: "healthy", Metadata: json.RawMessage(`{"root_reachable":true}`)}); err != nil {
		t.Fatal(err)
	}
	listener, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	if _, err := listener.ExecContext(ctx, "LISTEN loom_worker_wake"); err != nil {
		t.Fatal(err)
	}
	first, err := push(1, p, firstRef)
	if err != nil || len(first.Items) != 1 || first.Items[0].Status != loomsync.ItemStatusAccepted {
		t.Fatalf("first admission: %+v %v", first, err)
	}
	if err := listener.Raw(func(driver any) error {
		wait, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
		defer cancel()
		for {
			notice, err := driver.(*stdlib.Conn).Conn().WaitForNotification(wait)
			if errors.Is(err, context.DeadlineExceeded) {
				return nil
			}
			if err != nil {
				return err
			}
			if notice.Payload == workers.KindKnowledgeIndexer {
				return fmt.Errorf("unready presence started knowledge repair before blob commit")
			}
		}
	}); err != nil {
		t.Fatal(err)
	}
	canonical := first.Items[0].GlobalRef
	var versions int
	if err := db.QueryRow(`SELECT count(*) FROM objects.object_versions WHERE object_id=$1`, canonical).Scan(&versions); err != nil || versions != 0 {
		t.Fatalf("metadata manufactured a version: %d %v", versions, err)
	}
	replay, err := push(1, p, firstRef)
	if err != nil || replay.Items[0].Status != loomsync.ItemStatusDuplicate || replay.Items[0].GlobalRef != canonical {
		t.Fatalf("replay: %+v %v", replay, err)
	}
	// The actual owner must accept the real receiver receipt, not a fake digest.
	owner := nodeagent.Store{DataDir: t.TempDir()}
	if err := owner.EnsureSyncDataDirs(); err != nil {
		t.Fatal(err)
	}
	ws := watchedroots.NewStore(owner.DataDir)
	if err := ws.SavePathState(watchedroots.PathState{RootKey: p.RootKey, RelativePath: p.RelativePath, LocalObjectID: localID}); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(p)
	var normalized any
	_ = json.Unmarshal(raw, &normalized)
	canonicalRaw, _ := json.Marshal(normalized)
	item := nodeagent.LocalSyncOutboxItem{LocalRef: firstRef, LocalOutboxID: firstRef, ItemKind: loomsync.ItemKindObjectMetadata,
		StreamName: loomsync.StreamObjectMetadata, LocalSequence: 1, PayloadJSON: raw, PayloadHash: fmt.Sprintf("%x", sha256.Sum256(canonicalRaw)), Status: "pending"}
	if err := owner.SaveSyncOutbox([]nodeagent.LocalSyncOutboxItem{item}); err != nil {
		t.Fatal(err)
	}
	if err := owner.ApplySyncPushResult(first); err != nil {
		t.Fatal(err)
	}
	ack, err := ws.LoadPathState(p.RootKey, p.RelativePath)
	if err != nil || ack.MainObjectID != canonical || ack.LastFileObservationSequence != 1 {
		t.Fatalf("real receipt: %+v %v", ack, err)
	}
	store := objectstore.New(t.TempDir())
	objService := objects.NewService(db, store)
	input := loomsync.SyncedObjectInput{NodeRef: node.NodeID, CredentialToken: cred.CredentialToken, LocalObjectRef: localID,
		LocalVersionRef: ids.NewObjectVersionID(), LocalSequence: 1, LogicalName: "note.md", SourcePath: "watched-root://files/note.md",
		SourceMtime: &now, ScopeRef: req.ScopeID, SizeBytes: 3, MimeType: "text/markdown",
		HashURI: "sha256:ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad", ContentBase64: base64.StdEncoding.EncodeToString([]byte("abc")), IndexPolicy: loomsync.IndexPolicyNone}
	upload, err := s.IngestSyncedObject(ctx, req, objService, search.NewService(db, store), input)
	if err != nil || upload.ObjectID != canonical {
		t.Fatalf("blob created a second file: %+v %v", upload, err)
	}
	p.Presence = "missing"
	missing, err := push(3, p, ids.NewLocalOutboxID())
	if err != nil || missing.Items[0].Status != loomsync.ItemStatusAccepted || missing.Items[0].GlobalRef != canonical {
		t.Fatalf("withdrawal: %+v %v", missing, err)
	}
	p.Presence = "present"
	stale, err := push(2, p, ids.NewLocalOutboxID())
	if err != nil || stale.Items[0].Status != loomsync.ItemStatusConflicted {
		t.Fatalf("stale presence: %+v %v", stale, err)
	}
	var presence, status string
	if err := db.QueryRow(`SELECT f.source_presence,o.status FROM files.file_metadata f JOIN objects.objects o USING(object_id) WHERE f.object_id=$1`, canonical).Scan(&presence, &status); err != nil || presence != "missing" || status != "active" {
		t.Fatalf("withdrawal deleted protected data: %s %s %v", presence, status, err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM objects.object_versions WHERE object_id=$1`, canonical).Scan(&versions); err != nil || versions != 1 {
		t.Fatalf("retained version: %d %v", versions, err)
	}
	var searchable bool
	if err := db.QueryRow(`SELECT `+objects.CurrentFileSourceSQL("f")+` FROM files.file_metadata f WHERE object_id=$1`, canonical).Scan(&searchable); err != nil || searchable {
		t.Fatalf("generic serving retained a withdrawn source: %t %v", searchable, err)
	}
	detail, err := objService.GetObject(ctx, canonical)
	if err != nil || detail.File == nil || detail.File.SourcePresence != "missing" || detail.LatestVersion == nil {
		t.Fatalf("technical inspection lost retention/presence: %+v %v", detail, err)
	}
	p.Generation = "obsolete"
	obsolete, err := push(4, p, ids.NewLocalOutboxID())
	if err != nil || obsolete.Items[0].Status != loomsync.ItemStatusConflicted {
		t.Fatalf("obsolete generation: %+v %v", obsolete, err)
	}
}
