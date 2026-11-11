package notesworkspacesync

import "testing"

func placementFixture() (RuntimeConfig, EnrollmentInventory, []enrollmentCandidate) {
	cfg := runtimeFixture()
	cfg.LayoutPublication = true
	previous := EnrollmentInventory{Collections: []EnrollmentCollection{
		{RuntimeSelection: RuntimeSelection{Scope: Scope{Workspace: "pilot", Collection: "orientation", SourceCollection: "source-notes", Generation: "enrollment", Root: "Projects/Legacy/Notes"}}, Status: "active", SourceGeneration: "notes-generation"},
		{RuntimeSelection: RuntimeSelection{Scope: Scope{Workspace: "pilot", Collection: "research", SourceCollection: "source-research", Generation: "enrollment", Root: "Projects/Renamed/repos/research"}}, Status: "active", SourceGeneration: "research-generation"},
	}}
	candidates := []enrollmentCandidate{
		{ID: "source-notes", Root: "Projects/Renamed/Notes", ProjectID: "project"},
		{ID: "source-research", Root: "Projects/Renamed/repos/research", ProjectID: "project", ExplicitRoot: "Projects/Legacy/Research"},
	}
	return cfg, previous, candidates
}

func TestExplicitPlacementPreservesCollectionAndPreviousGeneration(t *testing.T) {
	cfg, previous, candidates := placementFixture()
	selections, err := mergeEnrollmentSelections(cfg, previous, candidates)
	if err != nil {
		t.Fatal(err)
	}
	var research RuntimeSelection
	for _, s := range selections {
		if s.SourceCollection == "source-research" {
			research = s
		}
		if s.SourceCollection == "source-notes" && s.Root != "Projects/Legacy/Notes" {
			t.Fatal("unmodified orientation moved")
		}
	}
	if research.Collection != "research" || research.Generation != "enrollment" || research.Root != "Projects/Legacy/Research" || research.LayoutFrom != "Projects/Renamed/repos/research" || research.LayoutGeneration != "research-generation" {
		t.Fatalf("placement changed identity or lost predecessor: %+v", research)
	}
	previous.Collections[1].RuntimeSelection = research
	again, err := mergeEnrollmentSelections(cfg, previous, candidates)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range again {
		if s.SourceCollection == research.SourceCollection && s != research {
			t.Fatalf("placement replay changed selection: %+v -> %+v", research, s)
		}
	}
}

func TestNewCollectionReusesRetainedProjectGroup(t *testing.T) {
	cfg, previous, candidates := placementFixture()
	previous.Collections = previous.Collections[:1]
	candidates[1].ExplicitRoot = ""
	selections, err := mergeEnrollmentSelections(cfg, previous, candidates)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range selections {
		if s.SourceCollection == "source-research" && s.Root != "Projects/Legacy/repos/research" {
			t.Fatalf("new collection split a renamed project: %+v", s)
		}
	}
}

func TestPlacementRejectsNamespaceCollisionAndUnreviewedAmbiguity(t *testing.T) {
	cfg, previous, candidates := placementFixture()
	candidates[1].ExplicitRoot = "Projects/Unrelated/Research"
	if _, err := mergeEnrollmentSelections(cfg, previous, candidates); err == nil {
		t.Fatal("unrelated project namespace accepted")
	}
	candidates[1].ExplicitRoot = "Projects/Legacy/Notes/Nested"
	if _, err := mergeEnrollmentSelections(cfg, previous, candidates); err == nil {
		t.Fatal("overlapping collection accepted")
	}
	candidates[1].ExplicitRoot = ""
	candidates = append(candidates, enrollmentCandidate{ID: "third", ProjectID: "project", Root: "Projects/Renamed/New"})
	if _, err := mergeEnrollmentSelections(cfg, previous, candidates); err == nil {
		t.Fatal("multiple existing groups guessed for new collection")
	}
	cfg.LayoutPublication = false
	_, previous, candidates = placementFixture()
	if _, err := mergeEnrollmentSelections(cfg, previous, candidates); err == nil {
		t.Fatal("existing root correction silently ignored without rollout support")
	}
}

func TestExplicitPassivePlacementDoesNotOscillate(t *testing.T) {
	cfg, previous, candidates := placementFixture()
	candidates[1].WorkState = "passive"
	previous.Collections[1].Root = "Projects/_Passive/Legacy/Research"
	previous.Collections[1].WorkingRoot = "Projects/Legacy/Research"
	selections, err := mergeEnrollmentSelections(cfg, previous, candidates)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range selections {
		if s.SourceCollection == "source-research" && (s.Root != "Projects/_Passive/Legacy/Research" || s.LayoutFrom != "") {
			t.Fatalf("stable passive placement was republished: %+v", s)
		}
	}
}
