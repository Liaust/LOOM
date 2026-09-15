package projectwatch

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"loom.local/loom/internal/nodes"
	"loom.local/loom/internal/projectcontracts"
	"loom.local/loom/internal/projects"
	"loom.local/loom/internal/requestctx"
)

// Every store mutator refuses, including ones that only append observations.
type dryRunReadOnlyStore struct {
	detail    projects.ProjectRegistrationDetail
	mutations []string
}

func (s *dryRunReadOnlyStore) refuse(name string) error {
	s.mutations = append(s.mutations, name)
	return fmt.Errorf("dry-run attempted mutation: %s", name)
}
func (s *dryRunReadOnlyStore) GetProjectRegistrationStatus(context.Context, string) (projects.ProjectRegistrationDetail, error) {
	return s.detail, nil
}
func (s *dryRunReadOnlyStore) ActivateProjectBase(context.Context, requestctx.Context, string) (projects.ProjectRegistrationDetail, error) {
	return projects.ProjectRegistrationDetail{}, s.refuse("ActivateProjectBase")
}
func (s *dryRunReadOnlyStore) ListProjectWatchedRootRegistrations(context.Context, string) ([]projects.ProjectWatchedRootRegistration, error) {
	return nil, nil
}
func (s *dryRunReadOnlyStore) UpsertProjectWatchedRootRegistration(context.Context, requestctx.Context, projects.UpsertProjectWatchedRootRegistrationInput) (projects.ProjectWatchedRootRegistration, error) {
	return projects.ProjectWatchedRootRegistration{}, s.refuse("UpsertProjectWatchedRootRegistration")
}
func (s *dryRunReadOnlyStore) MarkStaleProjectWatchedRoots(context.Context, requestctx.Context, string, string, []string) error {
	return s.refuse("MarkStaleProjectWatchedRoots")
}
func (s *dryRunReadOnlyStore) CorrelateProjectWatchedRootReports(context.Context, string) error {
	return s.refuse("CorrelateProjectWatchedRootReports")
}
func (s *dryRunReadOnlyStore) MarkProjectFacetActivated(context.Context, requestctx.Context, string, string, string, json.RawMessage) error {
	return s.refuse("MarkProjectFacetActivated")
}
func (s *dryRunReadOnlyStore) GetNode(context.Context, string) (nodes.Node, error) {
	return nodes.Node{}, fmt.Errorf("unexpected node lookup during local planning")
}

func TestProjectWatchDryRunInactiveNeverMutates(t *testing.T) {
	scaffold, err := projectcontracts.ScaffoldProject(projectcontracts.ScaffoldOptions{Name: "Dry Run", Slug: "dry-run", OwnerNode: "main", Preset: projectcontracts.PresetMinimal, Directory: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	store := &dryRunReadOnlyStore{detail: projects.ProjectRegistrationDetail{Registration: &projects.ProjectContractRegistration{ProjectRoot: scaffold.ProjectRoot, ActivationStatus: projects.ProjectActivationStatusInactive}}}
	before, err := json.Marshal(store.detail)
	if err != nil {
		t.Fatal(err)
	}
	result, err := NewService(Deps{Projects: store, Nodes: store}).ApplyDesiredState(context.Background(), requestctx.Context{}, "dry-run", projects.ApplyProjectWatchPolicyInput{ProjectRoot: scaffold.ProjectRoot, DryRun: true})
	if err != nil {
		t.Fatalf("read-only plan failed: %v; mutations=%v", err, store.mutations)
	}
	if len(store.mutations) != 0 {
		t.Fatalf("dry-run wrote: %v", store.mutations)
	}
	if !result.DryRun || len(result.Commands) == 0 {
		t.Fatalf("missing usable dry-run plan: %#v", result)
	}
	after, err := json.Marshal(result.Detail)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatalf("dry-run changed reported registration: before=%s after=%s", before, after)
	}
}

func dryRunFixture(t *testing.T, status string) (*dryRunReadOnlyStore, projectcontracts.Analysis) {
	t.Helper()
	scaffold, err := projectcontracts.ScaffoldProject(projectcontracts.ScaffoldOptions{Name: "Dry Run", Slug: "dry-run", OwnerNode: "main", Preset: projectcontracts.PresetMinimal, Directory: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	analysis := projectcontracts.Analyze(scaffold.ProjectRoot)
	if analysis.Loaded == nil || !analysis.Report.OK || len(analysis.Plan.WatchedRoots) == 0 {
		t.Fatalf("invalid fixture: %#v", analysis.Report)
	}
	plan, err := json.Marshal(analysis.Plan)
	if err != nil {
		t.Fatal(err)
	}
	return &dryRunReadOnlyStore{detail: projects.ProjectRegistrationDetail{Registration: &projects.ProjectContractRegistration{
		ProjectRoot: scaffold.ProjectRoot, ActivationStatus: status,
		ContractHash: hashBytesURI(analysis.Loaded.Raw), RegistrationPlan: plan,
	}}}, analysis
}

func assertDryRunDetailUnchanged(t *testing.T, store *dryRunReadOnlyStore, before []byte, result projects.ApplyProjectWatchPolicyResult) {
	t.Helper()
	if len(store.mutations) != 0 {
		t.Fatalf("dry-run wrote: %v", store.mutations)
	}
	for name, detail := range map[string]projects.ProjectRegistrationDetail{"store": store.detail, "response": result.Detail} {
		after, err := json.Marshal(detail)
		if err != nil {
			t.Fatal(err)
		}
		if string(before) != string(after) {
			t.Fatalf("dry-run changed %s detail: before=%s after=%s", name, before, after)
		}
	}
	if !result.DryRun || len(result.Commands) == 0 || len(result.WatchedRoots) != 0 {
		t.Fatalf("expected usable plan without applied registrations: %#v", result)
	}
}

func TestProjectWatchDryRunReplayNeverMutates(t *testing.T) {
	for _, status := range []string{projects.ProjectActivationStatusInactive, projects.ProjectActivationStatusBaseActive, projects.ProjectActivationStatusBlocked} {
		for _, source := range []string{"local", "registered", "snapshot"} {
			t.Run(status+"/"+source, func(t *testing.T) {
				store, analysis := dryRunFixture(t, status)
				input := projects.ApplyProjectWatchPolicyInput{DryRun: true}
				switch source {
				case "local":
					input.ProjectRoot = analysis.Plan.ProjectRoot
				case "snapshot":
					store.detail.Registration.ProjectRoot = filepath.Join(t.TempDir(), "unavailable")
					input.UseRegisteredSnapshot = true
				}
				before, err := json.Marshal(store.detail)
				if err != nil {
					t.Fatal(err)
				}
				service := NewService(Deps{Projects: store, Nodes: store})
				for attempt := 0; attempt < 2; attempt++ {
					result, err := service.ApplyDesiredState(context.Background(), requestctx.Context{}, "dry-run", input)
					if err != nil {
						t.Fatalf("attempt %d: %v", attempt, err)
					}
					assertDryRunDetailUnchanged(t, store, before, result)
				}
			})
		}
	}
}

func TestProjectWatchDryRunRejectedInputsNeverMutate(t *testing.T) {
	for _, scenario := range []string{"malformed", "invalid", "stale", "unresolved_root", "unregistered"} {
		t.Run(scenario, func(t *testing.T) {
			store, analysis := dryRunFixture(t, projects.ProjectActivationStatusInactive)
			wantError := ""
			switch scenario {
			case "malformed":
				if err := os.WriteFile(analysis.Loaded.ContractPath, []byte("project: ["), 0o600); err != nil {
					t.Fatal(err)
				}
				wantError = "could not be loaded"
			case "invalid":
				// Loadable YAML must still reach the validation gate.
				invalid := []byte("kind: loom.project\nschema_version: project.contract.v0.4\nproject: {}\n")
				if err := os.WriteFile(analysis.Loaded.ContractPath, invalid, 0o600); err != nil {
					t.Fatal(err)
				}
				store.detail.Registration.ContractHash = hashBytesURI(invalid)
				wantError = "validation errors"
			case "stale":
				store.detail.Registration.ContractHash = "sha256:" + strings.Repeat("0", 64)
				wantError = "hash is stale"
			case "unresolved_root":
				store.detail.Registration.ProjectRoot = filepath.Join(t.TempDir(), "unavailable")
				wantError = "could not be loaded"
			case "unregistered":
				store.detail.Registration = nil
				wantError = "no registered project contract"
			}
			before, err := json.Marshal(store.detail)
			if err != nil {
				t.Fatal(err)
			}
			for attempt := 0; attempt < 2; attempt++ {
				result, err := NewService(Deps{Projects: store, Nodes: store}).ApplyDesiredState(context.Background(), requestctx.Context{}, "dry-run", projects.ApplyProjectWatchPolicyInput{DryRun: true})
				if err == nil || !strings.Contains(err.Error(), wantError) || result.DryRun {
					t.Fatalf("expected %q without success, result=%#v err=%v", wantError, result, err)
				}
				after, err := json.Marshal(store.detail)
				if err != nil {
					t.Fatal(err)
				}
				if len(store.mutations) != 0 || string(after) != string(before) {
					t.Fatalf("rejected dry-run changed state: mutations=%v before=%s after=%s", store.mutations, before, after)
				}
			}
		})
	}
}

type dryRunUnresolvedProjectStore struct{ *dryRunReadOnlyStore }

func (s dryRunUnresolvedProjectStore) GetProjectRegistrationStatus(context.Context, string) (projects.ProjectRegistrationDetail, error) {
	return projects.ProjectRegistrationDetail{}, sql.ErrNoRows
}

func TestProjectWatchDryRunUnresolvedProjectNeverMutates(t *testing.T) {
	store := dryRunUnresolvedProjectStore{&dryRunReadOnlyStore{}}
	_, err := NewService(Deps{Projects: store, Nodes: store}).ApplyDesiredState(context.Background(), requestctx.Context{}, "missing", projects.ApplyProjectWatchPolicyInput{DryRun: true})
	if !errors.Is(err, sql.ErrNoRows) || len(store.mutations) != 0 {
		t.Fatalf("unresolved project: err=%v mutations=%v", err, store.mutations)
	}
}

func TestProjectWatchApplyStillAttemptsActivation(t *testing.T) {
	store, _ := dryRunFixture(t, projects.ProjectActivationStatusInactive)
	result, err := NewService(Deps{Projects: store, Nodes: store}).ApplyDesiredState(context.Background(), requestctx.Context{}, "dry-run", projects.ApplyProjectWatchPolicyInput{})
	if err == nil || !strings.Contains(err.Error(), "ActivateProjectBase") || result.DryRun {
		t.Fatalf("actual apply must propagate activation failure: result=%#v err=%v", result, err)
	}
	if len(store.mutations) != 1 || store.mutations[0] != "ActivateProjectBase" {
		t.Fatalf("unexpected actual apply effects: %v", store.mutations)
	}
}
