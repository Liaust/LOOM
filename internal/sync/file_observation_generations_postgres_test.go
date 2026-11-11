package sync_test

import (
	"encoding/json"
	"testing"
	"time"

	"loom.local/loom/internal/ids"
	"loom.local/loom/internal/nodeagent/watchedroots"
	"loom.local/loom/internal/nodes"
	loomsync "loom.local/loom/internal/sync"
	mainroots "loom.local/loom/internal/watchedroots"
)

func TestFileObservationOperationalGenerationPostgres(t *testing.T) {
	db, req := metadataDatabase(t)
	ctx := t.Context()
	node, err := nodes.NewService(db).GetNode(ctx, "main")
	if err != nil {
		t.Fatal(err)
	}
	cred, err := nodes.NewService(db).IssueNodeCredential(ctx, req, nodes.IssueNodeCredentialInput{NodeRef: node.NodeID, Reason: "generation compatibility fixture"})
	if err != nil {
		t.Fatal(err)
	}
	config := watchedroots.NormalizeRootConfig(watchedroots.RootConfig{RootKey: "generation-fixture", Include: []string{"**/*"}, SyncPolicy: watchedroots.SyncPolicy{ScopeRef: req.ScopeID}})
	report := func() string {
		t.Helper()
		raw, _ := json.Marshal(config)
		hash := watchedroots.ConfigHash(config)
		_, err := mainroots.NewService(db).Report(ctx, mainroots.ReportInput{NodeRef: node.NodeID, CredentialToken: cred.CredentialToken, RootKey: config.RootKey, ConfigHash: hash, ConfigJSON: raw, Status: "healthy", Metadata: json.RawMessage(`{"root_reachable":true}`)})
		if err != nil {
			t.Fatal(err)
		}
		return hash
	}
	old := report()
	config.Scan.FullRescanInterval = "12h"
	config.Watch.DebounceWindow = "2s"
	if report() == old {
		t.Fatal("fixture did not change exact configuration")
	}
	now := time.Now().UTC()
	observation := loomsync.FileObservation{SchemaVersion: loomsync.FileObservationSchema, LocalObjectRef: ids.NewObjectID(), RootKey: config.RootKey, RelativePath: "note.md", Generation: old, Presence: "present", ObservedAt: now, SourceMtime: &now, SizeBytes: 3}
	push := func(sequence int64) loomsync.PushBatchResult {
		t.Helper()
		raw, _ := json.Marshal(observation)
		result, err := loomsync.NewService(db).PushBatch(ctx, req, loomsync.PushBatchInput{NodeRef: node.NodeID, CredentialToken: cred.CredentialToken, Items: []loomsync.PushBatchItem{{ItemKind: loomsync.ItemKindObjectMetadata, StreamName: loomsync.StreamObjectMetadata, LocalSequence: sequence, LocalRef: ids.NewLocalOutboxID(), PayloadJSON: raw}}})
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	if result := push(1); len(result.Items) != 1 || result.Items[0].Status != loomsync.ItemStatusAccepted {
		t.Fatal("timing update rejected retained pending observation", result)
	}
	config.Include = []string{"selected/**"}
	report()
	if result := push(2); len(result.Items) != 1 || result.Items[0].Status != loomsync.ItemStatusConflicted {
		t.Fatal("selection change retained obsolete authority", result)
	}
}
