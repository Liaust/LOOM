package nodeagent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"loom.local/loom/internal/ids"
	"loom.local/loom/internal/nodeagent/watchedroots"
)

func TestSyncRepairPayloadEqualityIsLossless(t *testing.T) {
	if syncRepairPayloadEqual([]byte(`{"local_sequence":9007199254740992}`), []byte(`{"local_sequence":9007199254740993}`)) {
		t.Fatal("repair aliased distinct sequence evidence")
	}
	if syncRepairPayloadEqual([]byte(`{"local_sequence":12} {}`), []byte(`{"local_sequence":12}`)) {
		t.Fatal("repair accepted trailing payload")
	}
	if !syncRepairPayloadEqual([]byte(`{ "b":2, "a":1 }`), []byte(`{"a":1,"b":2}`)) {
		t.Fatal("object formatting affected evidence comparison")
	}
}

func syncRepairFixture(t *testing.T) (Store, Config, State, []LocalSyncDeletionRequest, []LocalSyncOutboxItem) {
	t.Helper()
	s, config, state, _ := localSyncObjectTestStore(t)
	if err := s.EnsureDataDirs(); err != nil {
		t.Fatal(err)
	}
	canonical, err := filepath.EvalSymlinks(s.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	s.DataDir = canonical
	objects := []LocalSyncObject{}
	for _, root := range []string{"first", "second"} {
		object := LocalSyncObject{LocalObjectID: ids.NewObjectID(), MainObjectID: ids.NewObjectID(), HashURI: "sha256:" + strings.Repeat("a", 64), SourcePath: "watched-root://" + root + "/note.md", Metadata: objectJSON(map[string]any{"node_key": config.NodeKey, "watched_root": root, "relative_path": "note.md"})}
		objects = append(objects, object)
		_, _, err := s.QueueWatchedRootDeletion(config, state, watchedroots.OutputAction{RootKey: root, RelativePath: "note.md", LocalObjectID: object.LocalObjectID, MainObjectID: object.MainObjectID, ContentHashURI: object.HashURI, Reason: "path was deleted from watched root"})
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := s.SaveSyncObjects(objects); err != nil {
		t.Fatal(err)
	}
	d, err := s.LoadSyncDeletions()
	if err != nil {
		t.Fatal(err)
	}
	o, err := s.LoadSyncOutbox()
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SaveSyncDeletions(d[1:]); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveSyncOutbox(o[:1]); err != nil {
		t.Fatal(err)
	}
	return s, config, state, d, o
}

func TestSyncRepairPairsPreserveIdentityAndBeforeImages(t *testing.T) {
	s, c, state, originalD, originalO := syncRepairFixture(t)
	before, _ := os.ReadFile(s.syncOutboxPath())
	plan, err := s.RepairSyncDeletionPairs(context.Background(), c, state, false, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.MissingRecords) != 1 || len(plan.MissingOutbox) != 1 || plan.Applied {
		t.Fatalf("plan: %+v", plan)
	}
	after, _ := os.ReadFile(s.syncOutboxPath())
	if string(after) != string(before) {
		t.Fatal("dry run mutated queue")
	}
	if _, err := s.RepairSyncDeletionPairs(context.Background(), c, state, true, "wrong"); err == nil {
		t.Fatal("accepted wrong digest")
	}
	result, err := s.RepairSyncDeletionPairs(context.Background(), c, state, true, plan.Digest)
	if err != nil || !result.Applied {
		t.Fatalf("apply: %+v %v", result, err)
	}
	d, _ := s.LoadSyncDeletions()
	o, _ := s.LoadSyncOutbox()
	if len(d) != 2 || len(o) != 2 {
		t.Fatal("lost or duplicated evidence")
	}
	for _, want := range originalD {
		found := false
		for _, got := range d {
			if got.LocalDeletionID == want.LocalDeletionID {
				found = true
				if string(localDeletionPayload(got)) != string(localDeletionPayload(want)) || got.LocalObjectID != want.LocalObjectID || !got.CreatedAt.Equal(want.CreatedAt) || got.SyncStatus != localSyncStatusPending || deletionRequestIdempotencyKey(state.NodeID, got) != deletionRequestIdempotencyKey(state.NodeID, want) {
					t.Fatal("request identity changed")
				}
			}
		}
		if !found {
			t.Fatal("missing original request")
		}
	}
	if o[0].LocalOutboxID != originalO[0].LocalOutboxID || string(o[0].PayloadJSON) != string(originalO[0].PayloadJSON) {
		t.Fatal("original outbox changed")
	}
	saved, err := os.ReadFile(filepath.Join(s.syncRoot(), "repair-evidence", plan.Digest, localSyncOutboxFile))
	if err != nil || string(saved) != string(before) {
		t.Fatal("before image not retained")
	}
	replay, err := s.RepairSyncDeletionPairs(context.Background(), c, state, false, "")
	if err != nil || len(replay.MissingRecords)+len(replay.MissingOutbox) != 0 {
		t.Fatalf("replay: %+v %v", replay, err)
	}
}

func TestSyncRepairRefusesAmbiguousOrChangedEvidence(t *testing.T) {
	for _, name := range []string{"payload", "object", "identity", "cursor", "symlink", "hardlink", "review"} {
		t.Run(name, func(t *testing.T) {
			s, c, state, _, _ := syncRepairFixture(t)
			plan, err := s.RepairSyncDeletionPairs(context.Background(), c, state, false, "")
			if err != nil {
				t.Fatal(err)
			}
			switch name {
			case "payload":
				o, _ := s.LoadSyncOutbox()
				o[0].PayloadHash = strings.Repeat("b", 64)
				if err := s.SaveSyncOutbox(o); err != nil {
					t.Fatal(err)
				}
			case "object":
				if err := s.SaveSyncObjects(nil); err != nil {
					t.Fatal(err)
				}
			case "identity":
				c.NodeKey = "other"
			case "cursor":
				if err := s.SaveSyncCursors(nil); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				p := s.syncObjectsPath()
				if err := os.Rename(p, p+".original"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(p+".original", p); err != nil {
					t.Fatal(err)
				}
			case "hardlink":
				if err := os.Link(s.syncObjectsPath(), s.syncObjectsPath()+".link"); err != nil {
					t.Fatal(err)
				}
			case "review":
				plan.Digest = "stale"
			}
			if _, err := s.RepairSyncDeletionPairs(context.Background(), c, state, true, plan.Digest); err == nil {
				t.Fatal("unsafe repair accepted")
			}
		})
	}
}
