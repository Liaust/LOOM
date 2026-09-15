package knowledge

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"loom.local/loom/internal/ids"
	"loom.local/loom/internal/nodes"
	"loom.local/loom/internal/projectcontracts"
	"loom.local/loom/internal/projectregistration"
	"loom.local/loom/internal/projects"
	"loom.local/loom/internal/projectwatch"
	"loom.local/loom/internal/requestctx"
	"loom.local/loom/internal/storagecatalog"
)

// These deliberately mutate observed database evidence. Supported sync replay
// refuses an in-place path change; this does not model an ordinary rename.
func TestNotesPassageCurrentPathPostgres(t *testing.T) {
	s, roots := boxSyncedFixture(t)
	metadata := jsonObject(roots[0].Metadata)
	metadata["registration_metadata"].(map[string]any)["knowledge_source"].(map[string]any)["exclude"] = []string{"excluded/**"}
	roots[0].Metadata = mustJSON(t, metadata)
	var err error
	roots[0], err = s.store.UpsertSourceRoot(t.Context(), roots[0])
	if err != nil {
		t.Fatal(err)
	}
	objects := reconcileBoxSyncedFixture(t, s, roots)
	advanceBoxSyncedFixture(t, s)
	var object KnowledgeObject
	for _, candidate := range objects {
		if candidate.NotesSourceRootID == roots[0].NotesSourceRootID {
			object = candidate
		}
	}
	input := notesFollowupExpected(t, s, object.KnowledgeObjectID, SourceLifecycleFilterActive)
	want, err := s.GetNotesPassage(t.Context(), input)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := s.store.ListSyncedObjectEntriesForNotesRoots(t.Context(), roots[0].NotesSourceRootID)
	if err != nil || len(entries) != 1 {
		t.Fatalf("source fixture: %v", err)
	}
	e := entries[0]
	prefix := "watched-root://" + roots[0].BackendRootKey + "/"
	for _, tc := range []struct {
		name, path string
		allowed    bool
	}{
		{"unchanged", prefix + "source.md", true},
		{"eligible_renamed_md", prefix + "renamed.md", true},
		{"eligible_nested_md", prefix + "nested/renamed.md", true},
		{"outside_include", prefix + "source.md.moved", false},
		{"explicit_exclude", prefix + "excluded/source.md", false},
		{"private_directory", prefix + "private_no_index/source.md", false},
		{"credentials_directory", prefix + "credentials/source.md", false},
		{"control_contract", prefix + "loom.notes.yaml", false},
		{"cross_root", "watched-root://other/source.md", false},
		{"escaping_path", prefix + "../source.md", false},
		{"absolute_relative", prefix + "/source.md", false},
		{"control_character", prefix + "source\n.md", false},
		{"missing_path", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := s.store.db.Exec(`UPDATE objects.object_versions SET source_path=$1 WHERE object_version_id=$2`, tc.path, e.ObjectVersionID); err != nil {
				t.Fatal(err)
			}
			defer func() {
				if _, err := s.store.db.Exec(`UPDATE objects.object_versions SET source_path=$1 WHERE object_version_id=$2`, e.SourcePath, e.ObjectVersionID); err != nil {
					t.Error(err)
				}
			}()
			notesFollowupReadOnly(t, s, func() {
				got, err := s.GetNotesPassage(t.Context(), input)
				if !tc.allowed {
					if !errors.Is(err, sql.ErrNoRows) {
						t.Fatalf("SQL-observed ineligible path returned passage: %v", err)
					}
					return
				}
				if err != nil || got.Text != want.Text || got.NotesPassageInput != input || got.CurrentSourceContext != want.CurrentSourceContext || got.Custody != want.Custody {
					t.Fatalf("eligible path changed retained identity/text/context: %+v %v", got, err)
				}
			})
		})
	}
	t.Run("missing_current_version", func(t *testing.T) {
		if _, err := s.store.db.Exec(`UPDATE files.file_metadata SET latest_version_id=NULL WHERE object_id=$1`, e.ObjectID); err != nil {
			t.Fatal(err)
		}
		defer func() {
			if _, err := s.store.db.Exec(`UPDATE files.file_metadata SET latest_version_id=$1 WHERE object_id=$2`, e.ObjectVersionID, e.ObjectID); err != nil {
				t.Error(err)
			}
		}()
		if _, err := s.GetNotesPassage(t.Context(), input); !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("missing current evidence returned passage: %v", err)
		}
	})
	t.Run("catalog_custody_is_unchanged", func(t *testing.T) {
		entry, err := storagecatalog.NewService(s.store.db).RegisterEntry(t.Context(), storagecatalog.RegisterEntryInput{StorageClass: storagecatalog.StorageClassObjectBlob,
			SourceArea: storagecatalog.SourceAreaNotes, OriginNodeKey: object.SourceNodeKey, WatchedRootKey: roots[0].BackendRootKey,
			LogicalPath: object.RelativePath, OriginalSourcePath: object.SourcePath, FileClass: object.FileClass, MimeType: object.MimeType,
			ChecksumAlgorithm: "sha256", ChecksumHex: strings.TrimPrefix(object.SourceHash, "sha256:"), AvailabilityState: storagecatalog.AvailabilityStateAvailable})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.store.db.Exec(`UPDATE knowledge.knowledge_objects SET storage_entry_id=$1,metadata=metadata||$2::jsonb WHERE knowledge_object_id=$3`, entry.StorageEntryID,
			mustJSON(t, map[string]any{"storage_entry": map[string]any{"checksum_hex": strings.TrimPrefix(object.SourceHash, "sha256:")}, "storage_metadata": map[string]any{}}), object.KnowledgeObjectID); err != nil {
			t.Fatal(err)
		}
		if _, err := s.store.db.Exec(`UPDATE objects.object_versions SET source_path='watched-root://other/no-longer-the-catalog-source' WHERE object_version_id=$1`, e.ObjectVersionID); err != nil {
			t.Fatal(err)
		}
		got, err := s.GetNotesPassage(t.Context(), input)
		if err != nil || got.Text != want.Text || got.NotesPassageInput != input {
			t.Fatalf("catalog custody depended on unused sync evidence: %+v %v", got, err)
		}
	})
	t.Run("legacy_project_logical_name", testNotesPassageLegacyProjectLogicalName)
}

func testNotesPassageLegacyProjectLogicalName(t *testing.T) {
	s, boxRoots := boxSyncedFixture(t)
	rootPath := t.TempDir()
	for _, relative := range []string{".loom/contracts", "notes"} {
		if err := os.MkdirAll(filepath.Join(rootPath, relative), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	projectID, actorID := ids.NewProjectID(), ids.NewActorID()
	if _, err := s.store.db.Exec(`INSERT INTO identity.actors(actor_id,actor_key,actor_kind,display_name,status) VALUES ($1,$1,'human','Legacy Notes fixture','active')`, actorID); err != nil {
		t.Fatal(err)
	}
	contract := fmt.Sprintf("kind: loom.project\nschema_version: project.contract.v0.4\nproject:\n  id: %s\n  slug: legacy-notes\n  name: Legacy Notes fixture\n  owner_node: sync-owner\n  status: active\nfacets:\n  notes: true\n", projectID)
	for relative, body := range map[string]string{
		".loom/project.yaml":         contract,
		".loom/contracts/notes.yaml": "kind: loom.notes\nschema_version: notes.contract.v0.3\nnotes:\n  status: draft\n  sync: true\n  index: true\n  backup: false\n",
	} {
		if err := os.WriteFile(filepath.Join(rootPath, relative), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	registration, err := projectregistration.BuildInput(projectcontracts.Analyze(rootPath), "notes-passage-legacy-fixture")
	if err != nil {
		t.Fatal(err)
	}
	request := requestctx.Context{ActorID: actorID, ActorKey: actorID, OriginNodeID: *boxRoots[0].NodeID, OriginNodeKey: boxRoots[0].NodeKey, CorrelationID: "corr_notes_passage_legacy", FreshnessMode: "live_required", Source: "disposable-acceptance"}
	project := projects.NewService(s.store.db)
	if _, err := project.RegisterProjectContract(t.Context(), request, registration); err != nil {
		t.Fatal(err)
	}
	watch := projectwatch.NewService(projectwatch.Deps{Projects: project, Nodes: nodes.NewService(s.store.db)})
	if _, err := watch.ApplyDesiredState(t.Context(), request, projectID, projects.ApplyProjectWatchPolicyInput{UseRegisteredSnapshot: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReconcileSourceRoots(t.Context(), SourceRootReconcileInput{DiscoverFromStore: true}); err != nil {
		t.Fatal(err)
	}
	roots, err := s.store.ListSourceRoots(t.Context(), SourceRootFilter{ProjectID: projectID})
	if err != nil || len(roots) != 1 || roots[0].RootKind != RootKindProjectNotes || roots[0].BackendRootKey == "" {
		t.Fatalf("real legacy project root: %+v %v", roots, err)
	}
	root := roots[0]
	const body = "# Legacy project decision\nKeep the authoritative logical name.\n"
	seedNotesArchiveSibling(t, s, root, "decision.md", body)
	// Configure the supported pre-admission metadata alternative: project scope,
	// matching watched_root metadata, and the current file's logical_name. No
	// Knowledge row or publication is seeded, and no filename is guessed from
	// the non-watched source path.
	entries, err := s.store.ListSyncedObjectEntriesForNotesRoots(t.Context(), root.NotesSourceRootID)
	if err != nil || len(entries) != 1 {
		t.Fatalf("project source: %+v %v", entries, err)
	}
	e := entries[0]
	for _, statement := range []struct {
		query string
		args  []any
	}{
		{`UPDATE objects.objects SET metadata=$2::jsonb WHERE object_id=$1`, []any{e.ObjectID, mustJSON(t, map[string]any{"metadata": map[string]any{"watched_root": root.BackendRootKey}})}},
		{`UPDATE objects.object_versions SET source_path='/remote/opaque-source-17' WHERE object_version_id=$1`, []any{e.ObjectVersionID}},
		{`UPDATE files.file_metadata SET source_path='/remote/opaque-source-17' WHERE object_id=$1`, []any{e.ObjectID}},
	} {
		if _, err := s.store.db.Exec(statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
	objects := reconcileBoxSyncedFixture(t, s, roots)
	if len(objects) != 1 || objects[0].RelativePath != "decision.md" || advanceBoxSyncedFixture(t, s) == 0 {
		t.Fatalf("real legacy project admission: %+v", objects)
	}
	input := notesFollowupExpected(t, s, objects[0].KnowledgeObjectID, SourceLifecycleFilterActive)
	notesFollowupReadOnly(t, s, func() {
		got, err := s.GetNotesPassage(t.Context(), input)
		if err != nil || got.Text != strings.TrimSpace(body) || got.NotesPassageInput != input || got.CurrentSourceContext.SourceCategory != "projects" {
			t.Fatalf("supported legacy project passage lost: %+v %v", got, err)
		}
	})
	for _, tc := range []struct{ name, logicalName string }{
		{"missing_logical_name", ""},
		{"escaping_logical_name", "../decision.md"},
		{"private_logical_name", "private_no_index/decision.md"},
		{"control_logical_name", "decision\n.md"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := s.store.db.Exec(`UPDATE files.file_metadata SET logical_name=$2 WHERE object_id=$1`, e.ObjectID, tc.logicalName); err != nil {
				t.Fatal(err)
			}
			defer func() {
				if _, err := s.store.db.Exec(`UPDATE files.file_metadata SET logical_name='decision.md' WHERE object_id=$1`, e.ObjectID); err != nil {
					t.Error(err)
				}
			}()
			notesFollowupReadOnly(t, s, func() {
				if _, err := s.GetNotesPassage(t.Context(), input); !errors.Is(err, sql.ErrNoRows) {
					t.Fatalf("ineligible authoritative logical name returned passage: %v", err)
				}
			})
		})
	}
	t.Run("logical_name_cannot_rescue_cross_root", func(t *testing.T) {
		if _, err := s.store.db.Exec(`UPDATE objects.object_versions SET source_path='watched-root://other/decision.md' WHERE object_version_id=$1`, e.ObjectVersionID); err != nil {
			t.Fatal(err)
		}
		notesFollowupReadOnly(t, s, func() {
			if _, err := s.GetNotesPassage(t.Context(), input); !errors.Is(err, sql.ErrNoRows) {
				t.Fatalf("logical name rescued another watched root: %v", err)
			}
		})
	})
}
