package knowledge

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"loom.local/loom/internal/box"
	"loom.local/loom/internal/ids"
	"loom.local/loom/internal/migrations"
	"loom.local/loom/internal/nodes"
	"loom.local/loom/internal/requestctx"
	"loom.local/loom/internal/storagecatalog"
	"loom.local/loom/internal/watchedroots"
)

func TestFolderPolicyBoxToPipeline(t *testing.T) {
	root := t.TempDir()
	resolved := box.Resolved{RootPath: root, Profile: box.ProfileMain, OwnerNode: "main", RuntimeStateRoot: t.TempDir()}
	if _, err := box.Init(box.InitInput{Resolved: resolved}); err != nil {
		t.Fatal(err)
	}
	legacyPlan, err := box.BuildWatchPlan(box.WatchStatusInput{Resolved: resolved})
	if err != nil {
		t.Fatal(err)
	}
	contractPath := box.ContractPath(root)
	original, _ := os.ReadFile(contractPath)
	updated := strings.Replace(string(original), box.SchemaVersion, box.FolderPolicySchemaVersion, 1) + `files:
  rules:
    - path: Notes
      enrichment: [embeddings]
    - path: Notes/books
      enrichment: []
    - path: Notes/books/reviewed
      enrichment: [embeddings, ocr]
`
	if err := os.WriteFile(contractPath, []byte(updated), 0600); err != nil {
		t.Fatal(err)
	}
	plan, err := box.BuildWatchPlan(box.WatchStatusInput{Resolved: resolved})
	if err != nil {
		t.Fatalf("plan: %v diagnostics=%+v", err, plan.Diagnostics)
	}
	var metadata json.RawMessage
	for _, item := range plan.WatchedRoots {
		if item.Key == box.AreaNotes {
			for _, old := range legacyPlan.WatchedRoots {
				if old.Key != box.AreaNotes {
					continue
				}
				source := SourceRoot{NotesSourceRootID: "root", RootKind: RootKindBoxNotes, NodeKey: "main", SourcePath: filepath.Join(root, item.RootRelativePath), RootRelativePath: item.RootRelativePath, BackendRootKey: item.BackendRootKey,
					Metadata: mustJSON(t, map[string]any{"registration_metadata": old.Metadata}), AuthorizationMetadata: json.RawMessage(`{"authority":"source_root"}`)}
				anchor := workspaceAuthority{Root: workspaceRoot(source), ConfigHash: old.ConfigHash, Config: old.ConfigJSON, Metadata: source.Metadata, Authorization: source.AuthorizationMetadata}
				source.WorkspaceAuthority = mustJSON(t, anchor)
				generation := workspaceSourceGeneration(source, old.ConfigHash, old.ConfigJSON, "same-ignore-policy")
				source.Metadata = mustJSON(t, map[string]any{"registration_metadata": item.Metadata})
				if workspaceSourceGeneration(source, item.ConfigHash, item.ConfigJSON, "same-ignore-policy") != generation {
					t.Fatal("first files-only adoption changed the actual legacy Box Notes edit generation")
				}
			}
			metadata = mustJSON(t, map[string]any{"source_root": map[string]any{
				"root_relative_path": item.RootRelativePath, "knowledge_source": item.Metadata["knowledge_source"],
			}})
		}
	}
	if len(metadata) == 0 {
		t.Fatal("missing Notes metadata")
	}
	for _, tc := range []struct {
		path, class string
		embed, ocr  bool
	}{
		{"ordinary.md", storagecatalog.FileClassMarkdown, true, false},
		{"books/new/deep/paper.pdf", storagecatalog.FileClassPDF, false, false},
		{"books/reviewed/paper.pdf", storagecatalog.FileClassPDF, true, true},
		{"books/image.png", storagecatalog.FileClassImage, false, false},
	} {
		object := KnowledgeObject{RelativePath: tc.path, FileClass: tc.class, Metadata: metadata}
		compiled, err := CompilePipelinePlan(object, PipelinePolicy{EmbeddingsEnabled: true, PDFOCREnabled: true, ImageOCREnabled: true, ImageDescriptionsEnabled: true})
		if err != nil || compiled.EffectivePolicy == nil {
			t.Fatalf("%s: %+v %v", tc.path, compiled, err)
		}
		effective := compiled.EffectivePolicy
		if effective.EmbeddingsEnabled != tc.embed || effective.PDFOCREnabled != tc.ocr || effective.ImageOCREnabled != tc.ocr || effective.ImageDescriptionsEnabled {
			t.Fatalf("%s: wrong automatic policy %+v", tc.path, effective)
		}
		compiled, err = CompilePipelinePlan(object, PipelinePolicy{})
		if err != nil || compiled.EffectivePolicy.EmbeddingsEnabled || compiled.EffectivePolicy.PDFOCREnabled {
			t.Fatalf("folder rule enabled host service: %s %v", tc.path, err)
		}
	}
	// Ordinary source bytes and folder positions remain outside the policy layer.
	if _, err := os.Stat(filepath.Join(root, "Notes")); err != nil {
		t.Fatal(err)
	}
}

func TestFolderPolicyPreservesLegacyRestrictionsAndSnapshotFences(t *testing.T) {
	object := KnowledgeObject{RelativePath: "books/paper.pdf", FileClass: storagecatalog.FileClassPDF,
		Metadata: json.RawMessage(`{"source_root":{"root_relative_path":"Notes","knowledge_source":{"policy":{
		 "processing":{"paths":["books/**"],"embeddings":false,"ocr":"off"},
		 "files":{"rules":[{"path":"Notes/books","enrichment":["ocr","vision","embeddings"]}]}
		}}}}`)}
	compiled, err := CompilePipelinePlan(object, PipelinePolicy{EmbeddingsEnabled: true, PDFOCREnabled: true})
	if err != nil || compiled.EffectivePolicy.EmbeddingsEnabled || compiled.EffectivePolicy.PDFOCREnabled {
		t.Fatalf("folder rule widened existing source restrictions: %+v %v", compiled, err)
	}
	selected := object
	selected.Metadata = json.RawMessage(`{"source_root":{"root_relative_path":"Notes","knowledge_source":{"policy":{"files":{"rules":[{"path":"Topics","enrichment":[]}]}}}}}`)
	current := selected
	current.Metadata = json.RawMessage(`{"source_root":{"root_relative_path":"Notes","knowledge_source":{"policy":{"files":{"rules":[{"path":"Notes","enrichment":[]}]}}}}}`)
	if pipelineAdmissionMatches(selected, current) {
		t.Fatal("a files-only policy change bypassed a running pipeline's source fence")
	}
	if !pipelineAdmissionMatches(selected, selected) {
		t.Fatal("unchanged source stopped matching")
	}
	current.Metadata = json.RawMessage(`{"source_root":{"root_relative_path":"Notes","knowledge_source":{"policy":{"files":{"rules":[{"path":"Projects","enrichment":[]}]}}}}}`)
	if pipelineAdmissionMatches(selected, current) {
		t.Fatal("snapshot comparison dropped files-only policies with no matching processing override")
	}
	current.Metadata = json.RawMessage(`{"source_root":{"knowledge_source":{"policy":{"files":{"rules":[{"path":"**","enrichment":[]}]}}}}}`)
	if _, err := knowledgeSourcePolicy(current); err == nil {
		t.Fatal("missing source context silently ignored an inherited restriction")
	}
}

func TestFolderPolicySelectiveEnrichmentStillExplicit(t *testing.T) {
	object := enrichmentTestObject(t, storagecatalog.FileClassPDF)
	object.Metadata = json.RawMessage(`{"source_root":{"root_relative_path":"Notes","knowledge_source":{"policy":{"files":{"rules":[{"path":"Notes","enrichment":[]}]}}}}}`)
	host := PipelinePolicy{PDFOCREnabled: true, EmbeddingsEnabled: true}
	intent := enrichmentTestIntent(t, object, host, EnrichmentStages{OCR: true})
	plan, err := compileEnrichmentPlan(object, host, intent)
	if err != nil || !compiledPlanContainsSelectedStage(plan, FilePipelineStagePDFOCR) || plan.Enrichment.AutomaticPolicy.PDFOCREnabled {
		t.Fatalf("explicit one-time OCR was confused with automatic defaults: %+v %v", plan, err)
	}
	if compiledPlanContainsSelectedStage(plan, FilePipelineStageEmbedding) {
		t.Fatal("one-time OCR silently enabled embeddings")
	}
	host.PDFOCREnabled = false
	intent = enrichmentTestIntent(t, object, host, EnrichmentStages{OCR: true})
	if _, err := compileEnrichmentPlan(object, host, intent); err == nil {
		t.Fatal("one-time request enabled host-disabled OCR")
	}
}

func TestFolderSearchPolicyPlansAutomaticWork(t *testing.T) {
	host := PipelinePolicy{PDFOCREnabled: true, ImageOCREnabled: true, ImageDescriptionsEnabled: true, EmbeddingsEnabled: true}
	for _, search := range []string{"none", "metadata", "text"} {
		for _, class := range []string{storagecatalog.FileClassMarkdown, storagecatalog.FileClassText, storagecatalog.FileClassPDF, storagecatalog.FileClassImage, storagecatalog.FileClassOfficeDocument} {
			t.Run(search+"/"+class, func(t *testing.T) {
				object := KnowledgeObject{RelativePath: "nested/file.docx", FileClass: class, Metadata: mustJSON(t, map[string]any{
					"source_root": map[string]any{"root_relative_path": "Research", "knowledge_source": map[string]any{"policy": map[string]any{
						"files": map[string]any{"rules": []any{map[string]any{"path": "Research/nested", "search": search}}},
					}}},
				})}
				plan, err := CompilePipelinePlan(object, host)
				if err != nil {
					t.Fatal(err)
				}
				for _, stage := range plan.Stages {
					if search == "text" {
						if !stage.Selected {
							t.Fatalf("text disabled %s", stage.StageKey)
						}
						continue
					}
					want := stage.StageKey == FilePipelineStageMetadata || stage.StageKey == FilePipelineStageFinalize ||
						(search == "metadata" && stage.StageKey == FilePipelineStageLexicalIndex)
					if stage.Selected != want || (!want && stage.SkipReason != "disabled_by_source_search") {
						t.Fatalf("unexpected stage: %+v", stage)
					}
				}
				if search != "text" && *plan.EffectivePolicy != (PipelinePolicy{}) {
					t.Fatalf("metadata search retained automatic enrichment: %+v", plan.EffectivePolicy)
				}
				snapshot, err := plan.Snapshot()
				if err != nil || pipelinePlanNeedsSource(snapshot, object) != (search == "text") {
					t.Fatalf("source snapshot selection: %v", err)
				}
				if search != "text" && shouldIndexKnowledgeObjectMetadata(object) != (search == "metadata") {
					t.Fatal("metadata search writer disagrees with inherited search")
				}
			})
		}
	}
}

func TestFolderSearchInheritanceAndRootRelativePaths(t *testing.T) {
	for _, base := range []string{"Research", "."} {
		object := KnowledgeObject{RelativePath: "nested/ordinary.md", FileClass: storagecatalog.FileClassMarkdown,
			Metadata: mustJSON(t, map[string]any{"source_root": map[string]any{"root_relative_path": base, "knowledge_source": map[string]any{"policy": map[string]any{
				"files": map[string]any{"rules": []any{
					map[string]any{"path": "**", "search": "metadata", "enrichment": []string{}},
					map[string]any{"path": filepath.ToSlash(filepath.Join(base, "nested")), "search": "text"},
				}},
			}}}})}
		plan, err := CompilePipelinePlan(object, DefaultPipelinePolicy())
		if err != nil || !compiledPlanContainsSelectedStage(plan, FilePipelineStageNativeText) || compiledPlanContainsSelectedStage(plan, FilePipelineStageEmbedding) {
			t.Fatalf("%s: independent search/enrichment inheritance: %+v %v", base, plan, err)
		}
		legacy := object
		legacy.Metadata = nil
		plan, err = CompilePipelinePlan(legacy, DefaultPipelinePolicy())
		if err != nil || !compiledPlanContainsSelectedStage(plan, FilePipelineStageEmbedding) {
			t.Fatalf("legacy text pipeline changed: %+v %v", plan, err)
		}
		if pipelineAdmissionMatches(object, legacy) {
			t.Fatal("search policy change bypassed snapshot fence")
		}
	}
}

func TestFolderSearchSelectiveEnrichment(t *testing.T) {
	host := PipelinePolicy{PDFOCREnabled: true, ImageOCREnabled: true, ImageDescriptionsEnabled: true, EmbeddingsEnabled: true}
	for _, search := range []string{"none", "metadata"} {
		for _, tc := range []struct {
			class    string
			stages   EnrichmentStages
			selected string
		}{
			{storagecatalog.FileClassPDF, EnrichmentStages{OCR: true}, FilePipelineStagePDFOCR},
			{storagecatalog.FileClassImage, EnrichmentStages{Vision: true}, FilePipelineStageImageDescription},
			{storagecatalog.FileClassMarkdown, EnrichmentStages{Embeddings: true}, FilePipelineStageEmbedding},
		} {
			object := enrichmentTestObject(t, tc.class)
			object.Metadata = mustJSON(t, map[string]any{"source_root": map[string]any{"root_relative_path": ".", "knowledge_source": map[string]any{"policy": map[string]any{
				"files": map[string]any{"rules": []any{map[string]any{"path": "**", "search": search, "enrichment": []string{}}}},
			}}}})
			intent := enrichmentTestIntent(t, object, host, tc.stages)
			plan, err := compileEnrichmentPlan(object, host, intent)
			if err != nil || !compiledPlanContainsSelectedStage(plan, tc.selected) || !compiledPlanContainsSelectedStage(plan, FilePipelineStageChunk) {
				t.Fatalf("%s/%s: requested stage lost: %+v %v", search, tc.class, plan, err)
			}
			if plan.Enrichment.AutomaticPolicy != (PipelinePolicy{}) {
				t.Fatalf("explicit stage widened defaults: %+v", plan.Enrichment)
			}
			if !tc.stages.Embeddings && compiledPlanContainsSelectedStage(plan, FilePipelineStageEmbedding) {
				t.Fatal("explicit extraction enabled unrequested embeddings")
			}
			for _, stage := range plan.Stages {
				if stage.Selected && slices.Contains([]string{FilePipelineStagePDFOCR, FilePipelineStageImageOCR}, stage.StageKey) && !tc.stages.OCR {
					t.Fatal("unrequested OCR selected")
				}
			}
			intent = enrichmentTestIntent(t, object, PipelinePolicy{}, tc.stages)
			if _, err := compileEnrichmentPlan(object, PipelinePolicy{}, intent); err == nil {
				t.Fatal("explicit request bypassed host policy")
			}
		}
	}
}

func TestFolderSearchWithdrawsPublishedBodyPostgres(t *testing.T) {
	db, url := boxSourcesDatabase(t)
	if _, err := migrations.Up(t.Context(), url, filepath.Join("..", "..", "migrations")); err != nil {
		t.Fatal(err)
	}
	for _, search := range []string{"none", "metadata"} {
		for _, unified := range []bool{false, true} {
			t.Run(search+"/unified="+strconv.FormatBool(unified), func(t *testing.T) {
				s, object := pipelineFixture(t, db, storagecatalog.FileClassMarkdown, "text/markdown", "cobalt antecedent marker", time.Now().UTC())
				processed, err := s.ProcessTextObject(t.Context(), TextPipelineInput{Object: object, Content: "cobalt antecedent marker"})
				if err != nil {
					t.Fatal(err)
				}
				if unified {
					if _, err := s.EnsurePipelineRun(t.Context(), object, PipelinePolicy{}, false, 100); err != nil {
						t.Fatal(err)
					}
					if _, err := db.Exec(`UPDATE knowledge.knowledge_objects SET lexical_version_id=$2,lexical_published_at=now() WHERE knowledge_object_id=$1`, object.KnowledgeObjectID, processed.Version.KnowledgeObjectVersionID); err != nil {
						t.Fatal(err)
					}
				}
				find := func(query string) int {
					t.Helper()
					result, err := s.SearchNotes(t.Context(), NotesSearchInput{Query: query, NotesSourceRootID: object.NotesSourceRootID, Mode: NotesSearchModeLexical})
					if err != nil {
						t.Fatal(err)
					}
					return len(result.Results)
				}
				if find("cobalt antecedent") != 1 {
					t.Fatal("missing initial body publication")
				}
				object.Metadata = mustJSON(t, map[string]any{"source_root": map[string]any{"root_relative_path": ".", "knowledge_source": map[string]any{"policy": map[string]any{
					"files": map[string]any{"rules": []any{map[string]any{"path": "**", "search": search}}},
				}}}})
				plan, err := CompilePipelinePlan(object, DefaultPipelinePolicy())
				if err != nil {
					t.Fatal(err)
				}
				tx, err := db.BeginTx(t.Context(), nil)
				if err != nil {
					t.Fatal(err)
				}
				defer tx.Rollback()
				// Start with a metadata projection too: none must withdraw both lanes.
				if _, err := upsertKnowledgeMetadataSearchDocumentTx(t.Context(), tx, object); err != nil {
					t.Fatal(err)
				}
				if err := withdrawIncompatiblePublicationsTx(t.Context(), tx, object.KnowledgeObjectID, plan); err != nil {
					t.Fatal(err)
				}
				if err := tx.Commit(); err != nil {
					t.Fatal(err)
				}
				if find("cobalt antecedent") != 0 {
					t.Fatal("metadata-only replacement retained body snippets")
				}
				wantMetadata := 0
				if search == "metadata" {
					wantMetadata = 1
				}
				if got := find("fixture"); got != wantMetadata {
					t.Fatalf("metadata results=%d, want %d", got, wantMetadata)
				}
				var history int
				if err := db.QueryRow(`SELECT count(*) FROM knowledge.knowledge_chunks WHERE knowledge_object_version_id=$1`, processed.Version.KnowledgeObjectVersionID).Scan(&history); err != nil || history != len(processed.Chunks) {
					t.Fatalf("retained chunks lost: %d %v", history, err)
				}
				if _, err := s.store.getKnowledgeObjectVersion(t.Context(), processed.Version.KnowledgeObjectVersionID); err != nil {
					t.Fatal("retained source version lost", err)
				}
			})
		}
	}
}

func TestFolderPolicyReapplyAndRefreshPostgres(t *testing.T) {
	db, url := boxSourcesDatabase(t)
	if _, err := migrations.Up(t.Context(), url, filepath.Join("..", "..", "migrations")); err != nil {
		t.Fatal(err)
	}
	// Exercise the existing registration/report owners in this disposable DB,
	// never the Main registry or user folders.
	actorID, nodeID := ids.NewActorID(), ids.NewNodeID()
	if _, err := db.Exec(`INSERT INTO identity.actors (actor_id,actor_key,display_name,actor_kind,status) VALUES ($1,'folder-policy','Folder policy test','human','active')`, actorID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO nodes.nodes (node_id,node_key,display_name,node_kind,node_role,runtime_class,status) VALUES ($1,'main','Folder policy test','server','main','native','active')`, nodeID); err != nil {
		t.Fatal(err)
	}
	req := requestctx.Context{ActorID: actorID, OriginNodeID: nodeID, OriginNodeKey: "main"}
	credential, err := nodes.NewService(db).IssueNodeCredential(t.Context(), req, nodes.IssueNodeCredentialInput{NodeRef: nodeID, Reason: "disposable folder policy check"})
	if err != nil {
		t.Fatal(err)
	}
	resolved := box.Resolved{RootPath: t.TempDir(), Profile: box.ProfileMain, OwnerNode: "main", RuntimeStateRoot: t.TempDir()}
	if _, err := box.Init(box.InitInput{Resolved: resolved}); err != nil {
		t.Fatal(err)
	}
	contractPath := box.ContractPath(resolved.RootPath)
	original, _ := os.ReadFile(contractPath)
	updated := strings.Replace(string(original), box.SchemaVersion, box.FolderPolicySchemaVersion, 1) + "files:\n  rules:\n    - path: Notes/books\n      enrichment: []\n"
	if err := os.WriteFile(contractPath, []byte(updated), 0600); err != nil {
		t.Fatal(err)
	}
	owner := box.NewService(db)
	first, err := owner.ApplyWatchPolicy(t.Context(), req, box.WatchApplyInput{Resolved: resolved})
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range first.Plan.WatchedRoots {
		if _, err := watchedroots.NewService(db).Report(t.Context(), watchedroots.ReportInput{NodeRef: nodeID, CredentialToken: credential.CredentialToken, RootKey: item.BackendRootKey, ConfigHash: item.ConfigHash, ConfigJSON: item.ConfigJSON, Status: watchedroots.StatusHealthy}); err != nil {
			t.Fatal(err)
		}
	}
	for range 2 {
		repeated, err := owner.ApplyWatchPolicy(t.Context(), req, box.WatchApplyInput{Resolved: resolved})
		if err != nil {
			t.Fatal(err)
		}
		for _, registration := range repeated.Registrations {
			if registration.DesiredRevision != 1 || registration.ActivationStatus != box.BoxWatchStatusReported {
				t.Fatalf("equivalent reapply reset a reported root: %+v", registration)
			}
		}
	}
	checkFolderRefreshContext(t, db)
}

func checkFolderRefreshContext(t *testing.T, db *sql.DB) {
	t.Helper()
	s, object := pipelineFixture(t, db, storagecatalog.FileClassMarkdown, "text/markdown", "small source", time.Now())
	metadata := `{"source_root":{"root_relative_path":"Notes","knowledge_source":{"policy":{"files":{"rules":[{"path":"Notes","enrichment":[]}]}}}}}`
	if _, err := db.Exec(`UPDATE knowledge.knowledge_objects SET metadata=$2 WHERE knowledge_object_id=$1`, object.KnowledgeObjectID, metadata); err != nil {
		t.Fatal(err)
	}
	refresh, err := s.pipelineRefreshStatus(t.Context(), PipelineRun{KnowledgeObjectID: object.KnowledgeObjectID, SourceRevision: object.SourceRevision, SourceHash: object.SourceHash})
	if err != nil || refresh.RequestedPolicy == nil || refresh.RequestedPolicy.Processing == nil || refresh.RequestedPolicy.Processing.Embeddings == nil || *refresh.RequestedPolicy.Processing.Embeddings {
		t.Fatalf("refresh omitted real path/family context: %+v %v", refresh, err)
	}
}
