package knowledge

import (
	"encoding/json"
	"testing"
	"time"

	"loom.local/loom/internal/box"
	"loom.local/loom/internal/ids"
	"loom.local/loom/internal/projectcontracts"
)

func TestExpandedSourceRootPolicyMembership(t *testing.T) {
	service := NewService(nil)
	for _, kind := range []string{RootKindBoxTopics, RootKindBoxLibrary, RootKindProjectMaterial} {
		t.Run(kind, func(t *testing.T) {
			category, path := "topics", "Topics"
			if kind == RootKindBoxLibrary {
				category, path = "library", "Library"
			}
			if kind == RootKindProjectMaterial {
				category, path = "projects", "docs"
			}
			for _, activation := range []string{"reported", "registered", "pending_agent_apply", "applied", "disabled", "stale", "blocked"} {
				decl := map[string]any{"root_kind": kind, "category": category, "root_relative_path": path, "enabled": true, "declaration": "docs"}
				metadata := mustJSON(t, map[string]any{"knowledge_source": decl})
				input := SourceRootReconcileInput{}
				if kind == RootKindProjectMaterial {
					input.ProjectRegistrations = []ProjectWatchedRootRegistration{{ProjectWatchedRootRegistrationID: ids.NewProjectWatchedRootRegistrationID(), ProjectID: ids.NewProjectID(), ProjectRoot: "/fixture/project", NodeID: ids.NewNodeID(), OwnerNodeKey: "main", LocalRootKey: "docs", BackendRootKey: "project__docs", RootRelativePath: path, SourceKinds: mustJSON(t, []string{"project_material"}), ActivationStatus: activation, Metadata: metadata}}
				} else {
					input.BoxRegistrations = []BoxWatchRootRegistration{{BoxWatchRootRegistrationID: ids.NewBoxWatchRootRegistrationID(), BoxID: ids.NewBoxID(), BoxRootPath: "/fixture/box", NodeID: ids.NewNodeID(), OwnerNodeKey: "main", AreaKey: category, LocalRootKey: category, BackendRootKey: "loom_box__" + category, RootRelativePath: path, SourceKinds: mustJSON(t, []string{kind}), ActivationStatus: activation, Metadata: metadata}}
				}
				candidates, skipped := service.BuildSourceRootCandidates(input)
				if len(candidates) != 1 || len(skipped) != 0 || candidates[0].Root.RootKind != kind {
					t.Fatalf("classification: %#v %v", candidates, skipped)
				}
				want := SourceRootStatusBlocked
				if activation == "reported" {
					want = SourceRootStatusActive
				}
				if activation == "disabled" || activation == "stale" {
					want = activation
				}
				if candidates[0].Root.Status != want {
					t.Fatalf("%s: got %s want %s", activation, candidates[0].Root.Status, want)
				}
			}
			for _, field := range []string{"enabled", "root_kind", "category", "root_relative_path"} {
				decl := map[string]any{"root_kind": kind, "category": category, "root_relative_path": path, "enabled": true, "declaration": "docs"}
				delete(decl, field)
				if expandedSourceStatus("reported", mustJSON(t, map[string]any{"knowledge_source": decl}), kind, path) != SourceRootStatusBlocked {
					t.Fatalf("accepted missing %s", field)
				}
			}
			for _, path := range []string{".", "../escape", "docs/../../escape", "/absolute", "docs\\escape"} {
				decl := map[string]any{"root_kind": kind, "category": category, "root_relative_path": path, "enabled": true, "declaration": "docs"}
				if expandedSourceStatus("reported", mustJSON(t, map[string]any{"knowledge_source": decl}), kind, path) != SourceRootStatusBlocked {
					t.Fatalf("accepted unsafe path %s", path)
				}
			}
		})
	}
}

func TestBuildSourceRootCandidatesFiltersNotesRoots(t *testing.T) {
	fixed := time.Date(2026, 7, 3, 14, 0, 0, 0, time.UTC)
	service := NewService(nil, WithClock(func() time.Time { return fixed }))

	nodeID := ids.NewNodeID()
	projectID := ids.NewProjectID()
	boxRegistrationID := ids.NewBoxWatchRootRegistrationID()
	projectRegistrationID := ids.NewProjectWatchedRootRegistrationID()

	candidates, skipped := service.BuildSourceRootCandidates(SourceRootReconcileInput{
		BoxRegistrations: []BoxWatchRootRegistration{
			{
				BoxWatchRootRegistrationID: boxRegistrationID,
				BoxID:                      ids.NewBoxID(),
				BoxRootPath:                "/Users/example/loom-box",
				NodeID:                     nodeID,
				OwnerNodeKey:               "main",
				AreaKey:                    "notes",
				LocalRootKey:               "notes",
				BackendRootKey:             "loom_box__notes",
				SourceKinds:                mustJSON(t, []string{"box_policy", "box_notes"}),
				RootRelativePath:           "Notes",
				DisplayName:                "Box Notes",
				ActivationStatus:           "reported",
				Metadata:                   mustJSON(t, map[string]any{"policy": "notes"}),
			},
			{
				BoxWatchRootRegistrationID: ids.NewBoxWatchRootRegistrationID(),
				BoxID:                      ids.NewBoxID(),
				BoxRootPath:                "/Users/example/loom-box",
				NodeID:                     nodeID,
				OwnerNodeKey:               "main",
				AreaKey:                    "launchpad",
				LocalRootKey:               "launchpad",
				BackendRootKey:             "loom_box__launchpad",
				SourceKinds:                mustJSON(t, []string{"box_launchpad"}),
				RootRelativePath:           "Launchpad",
				DisplayName:                "Launchpad",
				ActivationStatus:           "reported",
				Metadata:                   mustJSON(t, map[string]any{}),
			},
		},
		ProjectRegistrations: []ProjectWatchedRootRegistration{
			{
				ProjectWatchedRootRegistrationID: projectRegistrationID,
				ProjectContractRegistrationID:    ids.NewProjectContractRegistrationID(),
				ProjectID:                        projectID,
				ProjectRoot:                      "/Users/example/loom-box/Projects/osint-tools",
				NodeID:                           nodeID,
				OwnerNodeKey:                     "main",
				LocalRootKey:                     "notes",
				BackendRootKey:                   "project_osint_tools__notes",
				SourceKinds:                      mustJSON(t, []string{"notes_contract", "sync_policy"}),
				RootRelativePath:                 "notes",
				DisplayName:                      "Project Notes",
				ActivationStatus:                 "pending_agent_apply",
				Metadata:                         mustJSON(t, map[string]any{"facet": "notes"}),
			},
			{
				ProjectWatchedRootRegistrationID: ids.NewProjectWatchedRootRegistrationID(),
				ProjectContractRegistrationID:    ids.NewProjectContractRegistrationID(),
				ProjectID:                        projectID,
				ProjectRoot:                      "/Users/example/loom-box/Projects/osint-tools",
				NodeID:                           nodeID,
				OwnerNodeKey:                     "main",
				LocalRootKey:                     "docs",
				BackendRootKey:                   "project_osint_tools__docs",
				SourceKinds:                      mustJSON(t, []string{"sync_policy"}),
				RootRelativePath:                 "docs",
				DisplayName:                      "Project Docs",
				ActivationStatus:                 "reported",
				Metadata:                         mustJSON(t, map[string]any{"facet": "docs"}),
			},
		},
	})

	if len(candidates) != 2 {
		t.Fatalf("candidates len = %d, want 2: %#v", len(candidates), candidates)
	}
	if len(skipped) != 2 {
		t.Fatalf("skipped len = %d, want 2: %#v", len(skipped), skipped)
	}

	boxRoot := candidates[0].Root
	if boxRoot.RootKind != RootKindBoxNotes || boxRoot.SourcePath != "/Users/example/loom-box/Notes" {
		t.Fatalf("box root = %#v, want box notes source path", boxRoot)
	}
	if boxRoot.BoxWatchRootRegistrationID == nil || *boxRoot.BoxWatchRootRegistrationID != boxRegistrationID {
		t.Fatalf("box registration ref = %#v, want %q", boxRoot.BoxWatchRootRegistrationID, boxRegistrationID)
	}
	if !boxRoot.CreatedAt.Equal(fixed) || !boxRoot.UpdatedAt.Equal(fixed) {
		t.Fatalf("box root timestamps not defaulted from fixed clock")
	}

	projectRoot := candidates[1].Root
	if projectRoot.RootKind != RootKindProjectNotes || projectRoot.SourcePath != "/Users/example/loom-box/Projects/osint-tools/notes" {
		t.Fatalf("project root = %#v, want project notes source path", projectRoot)
	}
	if projectRoot.ProjectID == nil || *projectRoot.ProjectID != projectID {
		t.Fatalf("project id = %#v, want %q", projectRoot.ProjectID, projectID)
	}
	if projectRoot.ProjectWatchedRootRegistrationID == nil || *projectRoot.ProjectWatchedRootRegistrationID != projectRegistrationID {
		t.Fatalf("project registration ref = %#v, want %q", projectRoot.ProjectWatchedRootRegistrationID, projectRegistrationID)
	}
}

func TestReconcileSourceRootsDryRun(t *testing.T) {
	service := NewService(nil)
	nodeID := ids.NewNodeID()

	result, err := service.ReconcileSourceRoots(t.Context(), SourceRootReconcileInput{
		DryRun: true,
		BoxRegistrations: []BoxWatchRootRegistration{{
			BoxWatchRootRegistrationID: ids.NewBoxWatchRootRegistrationID(),
			BoxID:                      ids.NewBoxID(),
			BoxRootPath:                "/loom-box",
			NodeID:                     nodeID,
			OwnerNodeKey:               "main",
			AreaKey:                    "notes",
			LocalRootKey:               "notes",
			BackendRootKey:             "loom_box__notes",
			SourceKinds:                mustJSON(t, []string{"box_notes"}),
			RootRelativePath:           "Notes",
			ActivationStatus:           "reported",
			Metadata:                   mustJSON(t, map[string]any{}),
		}},
	})
	if err != nil {
		t.Fatalf("ReconcileSourceRoots dry run returned error: %v", err)
	}
	if !result.DryRun || result.Applied != 0 {
		t.Fatalf("dry run result = %#v, want dry_run true and applied 0", result)
	}
	if len(result.Candidates) != 1 || len(result.SourceRoots) != 1 {
		t.Fatalf("dry run candidates/source roots = %d/%d, want 1/1", len(result.Candidates), len(result.SourceRoots))
	}
}

func TestReconcileSourceRootsWithoutStoreFailsWhenApplying(t *testing.T) {
	service := NewService(nil)

	_, err := service.ReconcileSourceRoots(t.Context(), SourceRootReconcileInput{
		BoxRegistrations: []BoxWatchRootRegistration{{
			BoxWatchRootRegistrationID: ids.NewBoxWatchRootRegistrationID(),
			BoxID:                      ids.NewBoxID(),
			BoxRootPath:                "/loom-box",
			NodeID:                     ids.NewNodeID(),
			OwnerNodeKey:               "main",
			AreaKey:                    "notes",
			LocalRootKey:               "notes",
			BackendRootKey:             "loom_box__notes",
			SourceKinds:                mustJSON(t, []string{"box_notes"}),
			RootRelativePath:           "Notes",
			ActivationStatus:           "reported",
			Metadata:                   mustJSON(t, map[string]any{}),
		}},
	})
	if err == nil {
		t.Fatal("ReconcileSourceRoots apply returned nil error without configured store")
	}
}

func TestBoxWatchPlanSourceRootRegistrationsBuildsNotesCandidate(t *testing.T) {
	service := NewService(nil)
	plan := box.WatchPlan{
		RootPath:  "/Users/example/loom-box",
		OwnerNode: "macbook",
		BoxID:     ids.NewBoxID(),
		WatchedRoots: []projectcontracts.ProjectWatchedRootItem{
			{
				Key:              "notes",
				BackendRootKey:   "loom_box__notes",
				SourceKinds:      []string{"box_policy", "box_notes"},
				OwnerNode:        "macbook",
				RootRelativePath: "Notes",
				DisplayName:      "Box Notes",
				ActivationStatus: box.BoxWatchStatusPendingAgentApply,
				Metadata:         map[string]any{"policy": ".loom/policies/notes.yaml"},
			},
		},
	}

	registrations := BoxWatchPlanSourceRootRegistrations(plan, "")
	candidates, skipped := service.BuildSourceRootCandidates(SourceRootReconcileInput{BoxRegistrations: registrations})

	if len(skipped) != 0 {
		t.Fatalf("skipped = %#v, want none", skipped)
	}
	if len(candidates) != 1 {
		t.Fatalf("candidates len = %d, want 1: %#v", len(candidates), candidates)
	}
	root := candidates[0].Root
	if root.RootKind != RootKindBoxNotes || root.NodeKey != "macbook" || root.SourcePath != "/Users/example/loom-box/Notes" {
		t.Fatalf("root = %#v, want macbook Box Notes source", root)
	}
}

func TestBuildSourceRootCandidatesDeduplicatesBoxSeedAndStoredRoot(t *testing.T) {
	service := NewService(nil)
	registrationID := ids.NewBoxWatchRootRegistrationID()
	seed := BoxWatchRootRegistration{
		BoxID:            ids.NewBoxID(),
		BoxRootPath:      "/Users/example/loom-box",
		OwnerNodeKey:     "macbook",
		AreaKey:          "notes",
		LocalRootKey:     "notes",
		BackendRootKey:   "loom_box__notes",
		SourceKinds:      mustJSON(t, []string{"box_notes"}),
		RootRelativePath: "Notes",
		ActivationStatus: box.BoxWatchStatusPendingAgentApply,
		Metadata:         mustJSON(t, map[string]any{}),
	}
	stored := seed
	stored.BoxWatchRootRegistrationID = registrationID
	stored.NodeID = ids.NewNodeID()

	candidates, skipped := service.BuildSourceRootCandidates(SourceRootReconcileInput{BoxRegistrations: []BoxWatchRootRegistration{stored, seed}})

	if len(skipped) != 0 {
		t.Fatalf("skipped = %#v, want none", skipped)
	}
	if len(candidates) != 1 {
		t.Fatalf("candidates len = %d, want 1: %#v", len(candidates), candidates)
	}
	if candidates[0].Root.BoxWatchRootRegistrationID == nil || *candidates[0].Root.BoxWatchRootRegistrationID != registrationID {
		t.Fatalf("candidate registration id = %#v, want stored registration", candidates[0].Root.BoxWatchRootRegistrationID)
	}
}

func TestProjectNotesCandidateRequiresProjectID(t *testing.T) {
	service := NewService(nil)

	candidates, skipped := service.BuildSourceRootCandidates(SourceRootReconcileInput{
		ProjectRegistrations: []ProjectWatchedRootRegistration{{
			ProjectWatchedRootRegistrationID: ids.NewProjectWatchedRootRegistrationID(),
			ProjectContractRegistrationID:    ids.NewProjectContractRegistrationID(),
			NodeID:                           ids.NewNodeID(),
			OwnerNodeKey:                     "main",
			LocalRootKey:                     "notes",
			BackendRootKey:                   "project_missing__notes",
			SourceKinds:                      mustJSON(t, []string{"notes_contract"}),
			RootRelativePath:                 "notes",
			ActivationStatus:                 "reported",
			Metadata:                         mustJSON(t, map[string]any{}),
		}},
	})
	if len(candidates) != 0 {
		t.Fatalf("candidates len = %d, want 0", len(candidates))
	}
	if len(skipped) != 1 || skipped[0].Reason == "not_project_notes" {
		t.Fatalf("skipped = %#v, want invalid project notes skip", skipped)
	}
}

func TestSourceRootStatusFromActivation(t *testing.T) {
	tests := map[string]string{
		"reported":            SourceRootStatusActive,
		"pending_agent_apply": SourceRootStatusActive,
		"disabled":            SourceRootStatusDisabled,
		"stale":               SourceRootStatusStale,
		"blocked":             SourceRootStatusBlocked,
	}
	for input, want := range tests {
		if got := sourceRootStatusFromActivation(input); got != want {
			t.Fatalf("sourceRootStatusFromActivation(%q) = %q, want %q", input, got, want)
		}
	}
}

func mustJSON(t *testing.T, value any) json.RawMessage {
	t.Helper()
	payload, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal test JSON: %v", err)
	}
	return json.RawMessage(payload)
}

func TestDeclarationLegacyNotesCandidateIdentity(t *testing.T) {
	s := NewService(nil)
	input := ProjectWatchedRootRegistration{ProjectWatchedRootRegistrationID: ids.NewProjectWatchedRootRegistrationID(), ProjectContractRegistrationID: ids.NewProjectContractRegistrationID(), ProjectID: ids.NewProjectID(), ProjectRoot: "/fixture/project", NodeID: ids.NewNodeID(), OwnerNodeKey: "main", LocalRootKey: "notes", BackendRootKey: "project_fixture__notes", RootRelativePath: "notes", SourceKinds: mustJSON(t, []string{"notes_contract", "backup_policy"}), ActivationStatus: "reported", Metadata: mustJSON(t, map[string]any{"facet": "notes", "project_slug": "fixture"})}
	before, ok, reason := s.projectSourceRootCandidate(input)
	if !ok {
		t.Fatal(reason)
	}
	for _, activation := range []string{"pending_agent_apply", "applied", "reported"} {
		input.ActivationStatus = activation
		metadata := jsonObject(input.Metadata)
		metadata["declaration_adapter"] = map[string]any{"schema_version": "project.watch.binding.v1"}
		metadata["declaration_sources"] = []any{}
		input.Metadata = mustJSON(t, metadata)
		after, ok, reason := s.projectSourceRootCandidate(input)
		if !ok {
			t.Fatal(reason)
		}
		if after.Root.RootKind != RootKindProjectNotes || after.Root.SourcePath != before.Root.SourcePath || after.Root.BackendRootKey != before.Root.BackendRootKey || *after.Root.ProjectWatchedRootRegistrationID != *before.Root.ProjectWatchedRootRegistrationID {
			t.Fatal("candidate recast preserved Notes")
		}
	}
	// Candidate classification grants no adoption authority. The store-backed
	// reconciliation test proves that only the journal/receipt SQL admits it.
}
