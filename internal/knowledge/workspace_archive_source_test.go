package knowledge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"loom.local/loom/internal/filepolicy"
	rootpolicy "loom.local/loom/internal/nodeagent/watchedroots"
	"loom.local/loom/internal/storagearchive"
)

func TestWorkspaceArchiveRetainedNotesV05Authority(t *testing.T) {
	query := archiveWorkspaceDeclarationSQL()
	stop := notesProjectArchiveStopSQL("r", "candidate", "$2")
	retained := declarationLegacyNotesAuthoritySQL("r", "true", stop)
	if !strings.Contains(query, " OR "+retained+")") {
		t.Fatal("archive reader must reuse authenticated retained Notes authority alongside material authority")
	}
	for _, fence := range []string{
		"(r.status='active' OR (" + stop + "))",
		"r.metadata->'registration_metadata'=successor_metadata.value OR (" + stop + ")",
		"(legacy_row.metadata=successor_metadata.value OR (" + stop + "))",
	} {
		if !strings.Contains(retained, fence) {
			t.Fatalf("archive stop cannot replace only the retained writer's stopped metadata: %s", fence)
		}
	}
	for _, fence := range []string{
		"WHERE r.notes_source_root_id=$1 AND " + stop,
		"accepted.project_root || CASE WHEN r.root_relative_path='.' THEN '' ELSE '/' || r.root_relative_path END=r.source_path",
		"expected.value->>'backend_root_key'=r.backend_root_key",
		"report.watched_root_id=$3",
		"report.config_json=writer.config_json AND report.config_hash=writer.config_hash",
		"FOR SHARE OF writer,accepted,report,node",
	} {
		if !strings.Contains(query, fence) {
			t.Fatalf("retained Notes authority bypassed common archive/policy fence: %s", fence)
		}
	}
	// Reuse retains the receipt/source checks; neither a v0.5 label nor a raw
	// notes_contract source kind is sufficient authority on its own.
	for _, fence := range []string{"projects.declaration_owner_receipts adoption", "projects.declaration_action_receipts action", "legacy_source.contract_schema_version IN ('project.contract.v0.5','project.contract.v0.6')", "legacy_source.contract_json->>'schema_version'=legacy_source.contract_schema_version", "legacy_row.source_kinds_json ? 'notes_contract'"} {
		if !strings.Contains(retained, fence) {
			t.Fatalf("retained Notes authority lost authentication: %s", fence)
		}
	}
}

func TestWorkspaceArchiveRetainedNotesV05AuthorityPostgres(t *testing.T) {
	testDeclarationLegacyNotesContinuityObserved(t, "healthy", func(f *declarationEnrollmentDBFixture, root SourceRoot) {
		tx, err := f.db.BeginTx(t.Context(), nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		// The archive owner authenticates the stop independently. Exercise just
		// the reused authority with its two possible stop outcomes, retaining the
		// real adopted declaration, predecessor, ACK and successor report.
		for _, mutation := range []string{
			`UPDATE projects.project_watched_root_registrations SET activation_status='disabled',metadata='{"source":"project.deactivate"}'::jsonb WHERE project_watched_root_registration_id=$1`,
			`UPDATE knowledge.notes_source_roots SET status='disabled',metadata=jsonb_set(metadata,'{registration_metadata}','{"source":"project.deactivate"}'::jsonb) WHERE project_watched_root_registration_id=$1`,
		} {
			if _, err := tx.ExecContext(t.Context(), mutation, root.ProjectWatchedRootRegistrationID); err != nil {
				t.Fatal(err)
			}
		}
		check := func(stop string, want bool) {
			t.Helper()
			var allowed bool
			err := tx.QueryRowContext(t.Context(), `SELECT `+declarationLegacyNotesAuthoritySQL("r", "true", stop)+` FROM knowledge.notes_source_roots r WHERE r.notes_source_root_id=$1`, root.NotesSourceRootID).Scan(&allowed)
			if err != nil || allowed != want {
				t.Fatalf("retained Notes archive stop=%s: allowed=%v want=%v err=%v", stop, allowed, want, err)
			}
		}
		check("true", true)
		check("false", false)
		if _, err := tx.ExecContext(t.Context(), `DELETE FROM projects.declaration_owner_receipts WHERE project_id=$1 AND owner='projects'`, root.ProjectID); err != nil {
			t.Fatal(err)
		}
		check("true", false)
	})
}

func workspaceArchiveSelectionFixture(t *testing.T) (SourceRoot, rootpolicy.RootConfig, storagearchive.WorkspaceLifecycleEvidence) {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	project := "project_archive_notes"
	root := SourceRoot{NotesSourceRootID: "notes_root_archive", ProjectID: &project, NodeKey: "main", RootKind: RootKindProjectMaterial,
		RootRelativePath: "docs", SourcePath: filepath.Join(base, "active", "docs"), BackendRootKey: "project_docs", Status: SourceRootStatusDisabled,
		Metadata: json.RawMessage(`{"registration_metadata":{"knowledge_source":{"enabled":true,"include":["**"],"exclude":["excluded/**"]}}}`)}
	e := storagearchive.WorkspaceLifecycleEvidence{ManifestDigest: "sha256:manifest"}
	e.Plan.Kind, e.Plan.ObjectID, e.Plan.OperationKind = storagearchive.WorkspaceKindProject, project, storagearchive.WorkspaceOperationArchive
	e.Plan.OperationID, e.Event.EventID = "workspace_archive_one", "workspace_event_one"
	e.Plan.Source.Path.AbsolutePath = filepath.Join(base, "active")
	e.Plan.Destination.Path.AbsolutePath = filepath.Join(base, "archive", "workspace")
	e.Manifest.LifecycleState = storagearchive.WorkspaceLifecycleArchived
	config := rootpolicy.RootConfig{RootKey: root.BackendRootKey, RootRelativePath: "docs", Include: []string{"**"}, Exclude: []string{"config-denied/**"}}
	for _, dir := range []string{filepath.Join(e.Plan.Destination.Path.AbsolutePath, "docs"), filepath.Join(e.Plan.Destination.Path.AbsolutePath, "docs", "nested")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	return root, config, e
}

func archiveTestEntry(relative, body string) storagearchive.NoFollowInventoryEntry {
	return storagearchive.NoFollowInventoryEntry{RelativePath: relative, Kind: storagearchive.InventoryEntryFile,
		ContentDigest: hashArtifactValue(body), SizeBytes: int64(len(body))}
}

func TestWorkspaceArchiveSelectionNeverIndexedAndExclusions(t *testing.T) {
	root, config, e := workspaceArchiveSelectionFixture(t)
	for _, relative := range []string{"docs/z-never-indexed.md", "docs/a-attachment.pdf", "docs/excluded/secret.md", "docs/config-denied/secret.md", "docs/.hidden.md",
		"docs/private_no_index/secret.md", "docs/credentials/password.md", "docs/node_modules/readme.md", "docs/loom.notes.yaml", "other/not-declared.md", "docs/executable.sh"} {
		e.ArchivePlan.Inventory.Entries = append(e.ArchivePlan.Inventory.Entries, archiveTestEntry(relative, relative))
	}
	e.ArchivePlan.Inventory.Entries = append(e.ArchivePlan.Inventory.Entries, storagearchive.NoFollowInventoryEntry{RelativePath: "docs/link.md", Kind: storagearchive.InventoryEntrySymlink})
	out, err := archiveWorkspaceSelection(t.Context(), root, mustJSON(t, config), e)
	if err != nil {
		t.Fatal(err)
	}
	want := []WorkspaceArchiveFile{
		{RelativePath: "a-attachment.pdf", SourceHash: hashArtifactValue("docs/a-attachment.pdf"), SizeBytes: int64(len("docs/a-attachment.pdf"))},
		{RelativePath: "z-never-indexed.md", SourceHash: hashArtifactValue("docs/z-never-indexed.md"), SizeBytes: int64(len("docs/z-never-indexed.md"))},
	}
	if !reflect.DeepEqual(out.Files, want) || !out.ReadOnly || out.Root.SourcePath != root.SourcePath || out.Root.NotesSourceRootID != root.NotesSourceRootID || out.Generation == "" ||
		out.CanonicalRoot != filepath.Join(e.Plan.Destination.Path.AbsolutePath, "docs") {
		t.Fatalf("selection: %+v", out)
	}
	// Even a readable on-disk file must never join the authenticated inventory.
	if err := os.WriteFile(filepath.Join(out.CanonicalRoot, "injected.md"), []byte("not archived"), 0o600); err != nil {
		t.Fatal(err)
	}
	after, err := archiveWorkspaceSelection(t.Context(), root, mustJSON(t, config), e)
	if err != nil || !reflect.DeepEqual(out, after) {
		t.Fatalf("unmanifested file affected selection: %+v %v", after, err)
	}
	old := out.Generation
	e.Plan.OperationID = "workspace_archive_two"
	after, err = archiveWorkspaceSelection(t.Context(), root, mustJSON(t, config), e)
	if err != nil || old == after.Generation {
		t.Fatalf("new archive instance retained generation: %v", err)
	}
	e.Manifest.LifecycleState = storagearchive.WorkspaceLifecycleActive
	if _, err := archiveWorkspaceSelection(t.Context(), root, mustJSON(t, config), e); !errors.Is(err, ErrNotesCustodyPaused) {
		t.Fatalf("restored manifest admitted: %v", err)
	}
}

func TestWorkspaceArchiveSelectionRetainedPolicy(t *testing.T) {
	root, config, e := workspaceArchiveSelectionFixture(t)
	config.IgnorePolicy = rootpolicy.IgnorePolicy{Profile: string(filepolicy.ProfileManaged), DiscoverUserRules: true, PolicyRootRelativePath: "."}
	policyPath := filepath.Join(e.Plan.Destination.Path.AbsolutePath, ".loomignore")
	body := "docs/denied.md\n"
	if err := os.WriteFile(policyPath, []byte(body), 0o400); err != nil {
		t.Fatal(err)
	}
	e.ArchivePlan.Inventory.Entries = []storagearchive.NoFollowInventoryEntry{archiveTestEntry(".loomignore", body), archiveTestEntry("docs/denied.md", "private"), archiveTestEntry("docs/keep.md", "keep")}
	out, err := archiveWorkspaceSelection(t.Context(), root, mustJSON(t, config), e)
	if err != nil || len(out.Files) != 1 || out.Files[0].RelativePath != "keep.md" {
		t.Fatalf("retained ancestor policy: %+v %v", out, err)
	}
	if err := os.Remove(policyPath); err != nil {
		t.Fatal(err)
	}
	if _, err := archiveWorkspaceSelection(t.Context(), root, mustJSON(t, config), e); !errors.Is(err, ErrNotesCustodyPaused) {
		t.Fatalf("missing retained policy admitted: %v", err)
	}
	if err := os.WriteFile(policyPath, []byte("# tampered policy\n"), 0o400); err != nil {
		t.Fatal(err)
	}
	if _, err := archiveWorkspaceSelection(t.Context(), root, mustJSON(t, config), e); !errors.Is(err, ErrNotesCustodyPaused) {
		t.Fatalf("changed retained policy admitted: %v", err)
	}
	config.RootRelativePath = "Projects/example/docs"
	if _, err := archiveWorkspaceSelection(t.Context(), root, mustJSON(t, config), e); !errors.Is(err, ErrNotesCustodyPaused) {
		t.Fatalf("external policy boundary guessed: %v", err)
	}
}

func TestWorkspaceArchiveSelectionDeclarationAndIncludes(t *testing.T) {
	root, config, e := workspaceArchiveSelectionFixture(t)
	e.ArchivePlan.Inventory.Entries = []storagearchive.NoFollowInventoryEntry{archiveTestEntry("docs/one.md", "one"), archiveTestEntry("docs/two.pdf", "two")}
	config.Include = []string{"**/*.md"}
	out, err := archiveWorkspaceSelection(t.Context(), root, mustJSON(t, config), e)
	if err != nil || len(out.Files) != 1 || out.Files[0].RelativePath != "one.md" {
		t.Fatalf("config include not enforced: %+v %v", out, err)
	}
	root.Metadata = json.RawMessage(`{"registration_metadata":{"knowledge_source":{"enabled":false,"include":["**"]}}}`)
	if _, err := archiveWorkspaceSelection(t.Context(), root, mustJSON(t, config), e); !errors.Is(err, ErrNotesCustodyPaused) {
		t.Fatalf("withdrawn declaration admitted: %v", err)
	}
	root.Metadata = json.RawMessage(`{}`)
	if _, err := archiveWorkspaceSelection(t.Context(), root, mustJSON(t, config), e); !errors.Is(err, ErrNotesCustodyPaused) {
		t.Fatalf("undeclared material admitted: %v", err)
	}
	// Verified legacy Notes have no material declaration to manufacture.
	root.RootKind = RootKindProjectNotes
	if _, err := archiveWorkspaceSelection(t.Context(), root, mustJSON(t, config), e); err != nil {
		t.Fatalf("legacy Notes rejected: %v", err)
	}
	config.RootKey = "another-root"
	if _, err := archiveWorkspaceSelection(t.Context(), root, mustJSON(t, config), e); !errors.Is(err, ErrNotesCustodyPaused) {
		t.Fatalf("unbound config admitted: %v", err)
	}
}

func TestWorkspaceArchiveFileOriginalBytesAndFences(t *testing.T) {
	for _, name := range []string{"note.md", "reference.pdf"} {
		t.Run(name, func(t *testing.T) {
			base, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			file := filepath.Join(base, name)
			body := "original\r\nbytes\x00including binary\xff"
			if err := os.WriteFile(file, []byte(body), 0o400); err != nil {
				t.Fatal(err)
			}
			expected := WorkspaceArchiveFile{RelativePath: name, SourceHash: hashArtifactValue(body), SizeBytes: int64(len(body))}
			if err := withArchiveWorkspaceFile(t.Context(), file, expected, func(reader io.Reader) error {
				got, err := io.ReadAll(reader)
				if err != nil || string(got) != body {
					t.Fatalf("original bytes: %q %v", got, err)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			stat, _ := os.Stat(file)
			if stat.Mode().Perm() != 0o400 {
				t.Fatal("archive mode changed")
			}
			expected.SourceHash = hashArtifactValue("wrong")
			called := false
			if err := withArchiveWorkspaceFile(t.Context(), file, expected, func(io.Reader) error { called = true; return nil }); !errors.Is(err, ErrFileSourceChanged) || called {
				t.Fatalf("bad digest reached callback: %v %v", called, err)
			}
		})
	}
}

func TestWorkspaceArchiveFileRejectsLinksAndSubstitution(t *testing.T) {
	for _, attack := range []string{"leaf-symlink", "parent-symlink", "hardlink", "leaf-replacement", "parent-replacement", "content-change", "canceled"} {
		t.Run(attack, func(t *testing.T) {
			base, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			parent := filepath.Join(base, "docs")
			if err := os.Mkdir(parent, 0o700); err != nil {
				t.Fatal(err)
			}
			file := filepath.Join(parent, "note.md")
			body := "original bytes"
			if err := os.WriteFile(file, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			expected := WorkspaceArchiveFile{SourceHash: hashArtifactValue(body), SizeBytes: int64(len(body))}
			switch attack {
			case "leaf-symlink":
				if err := os.Rename(file, filepath.Join(base, "outside.md")); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Join(base, "outside.md"), file); err != nil {
					t.Fatal(err)
				}
			case "parent-symlink":
				if err := os.Rename(parent, parent+"-old"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(parent+"-old", parent); err != nil {
					t.Fatal(err)
				}
			case "hardlink":
				if err := os.Link(file, filepath.Join(base, "alias.md")); err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if attack == "canceled" {
				cancel()
			}
			called := false
			err = withArchiveWorkspaceFile(ctx, file, expected, func(reader io.Reader) error {
				called = true
				if _, err := io.ReadAll(reader); err != nil {
					return err
				}
				switch attack {
				case "leaf-replacement":
					if err := os.Rename(file, file+"-old"); err != nil {
						return err
					}
					return os.WriteFile(file, []byte(body), 0o600)
				case "parent-replacement":
					if err := os.Rename(parent, parent+"-old"); err != nil {
						return err
					}
					return os.Mkdir(parent, 0o700)
				case "content-change":
					return os.WriteFile(file, []byte("changed bytes!"), 0o600)
				}
				return nil
			})
			if err == nil {
				t.Fatal("unsafe archived read accepted")
			}
			pre := attack == "leaf-symlink" || attack == "parent-symlink" || attack == "hardlink" || attack == "canceled"
			if called == pre {
				t.Fatalf("callback admission for %s: %v", attack, called)
			}
		})
	}
}

func TestWorkspaceArchiveReaderRequiresTrustedConfiguration(t *testing.T) {
	reader := WorkspaceArchiveSourceReader{}
	called := false
	if err := reader.WithRoot(t.Context(), "root", func(WorkspaceArchiveSource) error { called = true; return nil }); err == nil || called {
		t.Fatal("unconfigured reader admitted a root")
	}
	for _, relative := range []string{"../outside.md", "a/../b.md", "/absolute.md", "a\\b.md", "."} {
		if err := reader.WithFile(t.Context(), "root", relative, "generation", 1, func(WorkspaceArchiveSource, WorkspaceArchiveFile, io.Reader) error { called = true; return nil }); !errors.Is(err, ErrInvalid) || called {
			t.Fatalf("invalid read %q: %v", relative, err)
		}
	}
	service := NewService(nil)
	owner := &storagearchive.WorkspaceMoveService{ManifestKeyID: "existing-owner-key"}
	configured := service.ArchiveSourceReader(owner, "main")
	if configured == nil || configured.Workspace.ManifestKeyID != owner.ManifestKeyID || configured.Knowledge != service || configured.NodeKey != "main" || service.ArchiveSourceReader(nil, "main") != nil {
		t.Fatal("existing archive owner not reused")
	}
}

func TestWorkspaceArchiveReaderProjectCustodyPostgres(t *testing.T) {
	for _, scenario := range []struct{ material, declaration bool }{{false, false}, {true, false}, {true, true}} {
		t.Run(fmt.Sprint(scenario), func(t *testing.T) {
			prepare := func(f *notesArchivePostgresFixture, root SourceRoot) {
				for relative, body := range map[string]string{"never-indexed.md": "# Never indexed\r\nOriginal bytes.\r\n", "attachment.pdf": "%PDF-1.7\x00original attachment"} {
					if err := os.WriteFile(filepath.Join(root.SourcePath, relative), []byte(body), 0o600); err != nil {
						t.Fatal(err)
					}
				}
				seedNotesArchiveSibling(t, f.s, root, "private.md", "never indexed private content")
				if _, err := f.s.store.db.Exec(`UPDATE files.file_metadata SET index_policy='private_no_index' WHERE source_path=$1`, "watched-root://"+root.BackendRootKey+"/private.md"); err != nil {
					t.Fatal(err)
				}
			}
			testNotesArchiveProjectCompletionPrepared(t, scenario.material, scenario.declaration, false, prepare, func(f *notesArchivePostgresFixture, object KnowledgeObject, lifecycle SourceLifecycle) {
				reader := f.s.ArchiveSourceReader(&f.p.Workspace, "main")
				f.s.store.db.SetMaxOpenConns(1)
				var selected WorkspaceArchiveSource
				err := reader.WithRoot(t.Context(), object.NotesSourceRootID, func(source WorkspaceArchiveSource) error { selected = source; return nil })
				if lifecycle == SourceLifecycleActive {
					if !errors.Is(err, ErrNotesCustodyPaused) {
						t.Fatalf("restore retained archive admission: %v", err)
					}
					return
				}
				if err != nil || !selected.ReadOnly || len(selected.Files) != 3 || selected.Files[0].RelativePath != "attachment.pdf" || selected.Files[1].RelativePath != "decision.md" || selected.Files[2].RelativePath != "never-indexed.md" {
					t.Fatalf("authenticated selection: %+v %v", selected, err)
				}
				if err := reader.WithFile(t.Context(), object.NotesSourceRootID, "decision.md", selected.Generation, 1<<20, func(source WorkspaceArchiveSource, file WorkspaceArchiveFile, r io.Reader) error {
					body, err := io.ReadAll(r)
					if err != nil || hashArtifactValue(string(body)) != object.SourceHash || file.SourceHash != object.SourceHash {
						t.Fatalf("original read: %v", err)
					}
					return nil
				}); err != nil {
					t.Fatal(err)
				}
				for relative, body := range map[string]string{"never-indexed.md": "# Never indexed\r\nOriginal bytes.\r\n", "attachment.pdf": "%PDF-1.7\x00original attachment"} {
					if err := reader.WithFile(t.Context(), object.NotesSourceRootID, relative, selected.Generation, 1<<20, func(_ WorkspaceArchiveSource, file WorkspaceArchiveFile, reader io.Reader) error {
						got, err := io.ReadAll(reader)
						if err != nil || string(got) != body || file.SourceHash != hashArtifactValue(body) {
							t.Fatalf("never-indexed original %s: %q %v", relative, got, err)
						}
						return nil
					}); err != nil {
						t.Fatal(err)
					}
				}
				if err := reader.WithFile(t.Context(), object.NotesSourceRootID, "private.md", selected.Generation, 1<<20, func(WorkspaceArchiveSource, WorkspaceArchiveFile, io.Reader) error {
					t.Fatal("private file admitted")
					return nil
				}); !errors.Is(err, ErrNotesPathExcluded) {
					t.Fatalf("private exclusion: %v", err)
				}
				for _, input := range []struct {
					path, generation string
					limit            int64
				}{{"decision.md", "stale-generation", 1 << 20}, {"decision.md", selected.Generation, 1}, {"not-manifested.md", selected.Generation, 1 << 20}} {
					called := false
					if err := reader.WithFile(t.Context(), object.NotesSourceRootID, input.path, input.generation, input.limit, func(WorkspaceArchiveSource, WorkspaceArchiveFile, io.Reader) error { called = true; return nil }); err == nil || called {
						t.Fatalf("invalid archive file admitted: %+v %v", input, err)
					}
				}
				bad := *reader
				bad.Workspace.ManifestKey = func(context.Context, string) ([]byte, error) { return []byte("wrong-key-wrong-key-wrong-key-123"), nil }
				if err := bad.WithRoot(t.Context(), object.NotesSourceRootID, func(WorkspaceArchiveSource) error { t.Fatal("unauthenticated archive admitted"); return nil }); err == nil {
					t.Fatal("wrong manifest key accepted")
				}
				if err := f.s.WithWritableNotesSource(t.Context(), object.NotesSourceRootID, "main", "decision.md", func(SourceRoot, string) error { t.Fatal("archive became writable"); return nil }); err == nil {
					t.Fatal("archive write admitted")
				}
			})
		})
	}
}
