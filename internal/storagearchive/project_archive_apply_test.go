package storagearchive

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"loom.local/loom/internal/projects"
)

func TestProjectArchiveApplyMovesPayloadAndCommitsLifecycle(t *testing.T) {
	environment, plan := newProjectArchiveApplyEnvironment(t)
	result, err := environment.service.ApplyProjectPhysicalArchive(context.Background(), plan, plan.PlanDigest)
	if err != nil {
		t.Fatal(err)
	}
	if result.Phase != projects.ProjectArchivePhaseComplete || result.Recoverable || !result.MutationBlocked || result.State.ArchiveManifestDigest == "" {
		t.Fatalf("archive result = %#v", result)
	}
	if _, err := os.Lstat(environment.canonicalRoot); !os.IsNotExist(err) {
		t.Fatalf("active project still exists: %v", err)
	}
	archived := plan.Workspace.Destination.Path.AbsolutePath
	if payload, err := os.ReadFile(filepath.Join(archived, "README.md")); err != nil || string(payload) != "disposable project fixture\n" {
		t.Fatalf("archived payload = %q err=%v", payload, err)
	}
	if result.Project.Project.Project.Status != "archived" || result.Project.Registration == nil || result.Project.Registration.RegistrationStatus != projects.ProjectRegistrationStatusArchived || result.Project.Registration.ActivationStatus != projects.ProjectActivationStatusInactive {
		t.Fatalf("project lifecycle was not archived: %#v", result.Project)
	}
	if environment.projects.eventCount != 1 {
		t.Fatalf("project lifecycle events = %d, want 1", environment.projects.eventCount)
	}
	repository, err := environment.repositories.ReadProjectRepositoryState(context.Background(), plan.Custody.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	if repository.Source == nil || repository.Source.SourceRevision != plan.Custody.RepositorySourceRevision {
		t.Fatalf("repository source revision changed: %#v", repository.Source)
	}
	for _, member := range repository.Members {
		if member.MembershipLifecycle != projects.RepositoryLifecycleArchived || member.RepositoryLifecycle != projects.RepositoryLifecycleArchived {
			t.Fatalf("repository member remains active: %#v", member)
		}
	}
	if !sortStringsEqual(environment.projects.lockKeys, append([]string(nil), environment.projects.lockKeys...)) {
		t.Fatalf("archive locks were not sorted: %#v", environment.projects.lockKeys)
	}
	if err := filepath.WalkDir(filepath.Dir(environment.roots.BoxRoot), func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".tar") {
			t.Fatalf("canonical archive created tar payload %s", path)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestProjectArchiveApplyRequiresExplicitArchiveDeactivationBeforeMutation(t *testing.T) {
	environment, plan := newProjectArchiveApplyEnvironment(t)
	environment.service.Activation = &fakeProjectRuntimeActivationService{}

	if _, err := environment.service.ApplyProjectPhysicalArchive(context.Background(), plan, plan.PlanDigest); err == nil || !strings.Contains(err.Error(), "does not support physical archive deactivation") {
		t.Fatalf("archive-only activation error = %v", err)
	}
	if environment.projects.transitions != 0 || environment.projects.eventCount != 0 {
		t.Fatalf("archive lifecycle mutated before archive-only activation check: transitions=%d events=%d", environment.projects.transitions, environment.projects.eventCount)
	}
	if _, err := os.Lstat(environment.canonicalRoot); err != nil {
		t.Fatalf("archive-only activation check moved the workspace: %v", err)
	}
}

func TestProjectArchiveCrashRecoveryBoundaries(t *testing.T) {
	tests := []struct {
		name         string
		boundary     ProjectPhysicalArchiveFailureBoundary
		sourceAbsent bool
		phase        projects.ProjectPhysicalArchivePhase
	}{
		{name: "after deactivation", boundary: ProjectArchiveBoundaryAfterDeactivation, phase: projects.ProjectArchivePhaseRuntimeDeactivated},
		{name: "after move", boundary: ProjectArchiveBoundaryAfterWorkspaceMove, sourceAbsent: true, phase: projects.ProjectArchivePhaseRuntimeDeactivated},
		{name: "before project state commit", boundary: ProjectArchiveBoundaryBeforeProjectStateCommit, sourceAbsent: true, phase: projects.ProjectArchivePhaseProjectStatePending},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			environment, plan := newProjectArchiveApplyEnvironment(t)
			failed := false
			environment.service.ArchiveFailureHook = func(boundary ProjectPhysicalArchiveFailureBoundary) error {
				if boundary == test.boundary && !failed {
					failed = true
					return errors.New("injected project archive crash")
				}
				return nil
			}
			partial, err := environment.service.ApplyProjectPhysicalArchive(context.Background(), plan, plan.PlanDigest)
			if err == nil || !strings.Contains(err.Error(), "injected project archive crash") {
				t.Fatalf("apply error = %v", err)
			}
			if partial.Phase != test.phase || !partial.Recoverable || !partial.MutationBlocked {
				t.Fatalf("partial result = %#v", partial)
			}
			if err := projects.EnsureProjectMutable(partial.Project.Project.Project, "project_activation", "scripts"); !projects.IsProjectArchiveInProgress(err) {
				t.Fatalf("partial archive did not block mutation: %v", err)
			}
			_, sourceErr := os.Lstat(environment.canonicalRoot)
			if test.sourceAbsent != os.IsNotExist(sourceErr) {
				t.Fatalf("source absence = %t err=%v, want %t", os.IsNotExist(sourceErr), sourceErr, test.sourceAbsent)
			}
			environment.service.ArchiveFailureHook = nil
			recovered, err := environment.service.RecoverProjectPhysicalArchive(context.Background(), plan, plan.PlanDigest)
			if err != nil {
				t.Fatal(err)
			}
			if recovered.Phase != projects.ProjectArchivePhaseComplete || recovered.Recoverable || recovered.Project.Project.Project.Status != "archived" {
				t.Fatalf("recovered result = %#v", recovered)
			}
			if environment.projects.eventCount != 1 {
				t.Fatalf("project lifecycle events = %d, want 1", environment.projects.eventCount)
			}
		})
	}
}

func TestProjectArchiveReplaySuppressesDuplicateLifecycle(t *testing.T) {
	environment, plan := newProjectArchiveApplyEnvironment(t)
	first, err := environment.service.ApplyProjectPhysicalArchive(context.Background(), plan, plan.PlanDigest)
	if err != nil {
		t.Fatal(err)
	}
	transitionsBeforeReplay := environment.projects.transitions
	second, err := environment.service.ApplyProjectPhysicalArchive(context.Background(), plan, plan.PlanDigest)
	if err != nil {
		t.Fatal(err)
	}
	if first.State.ArchiveManifestDigest != second.State.ArchiveManifestDigest || !second.Replay {
		t.Fatalf("replay changed evidence: first=%#v second=%#v", first.State, second.State)
	}
	journal := environment.workspace.planner.Journal.(*moveFakeJournal)
	if environment.projects.eventCount != 1 || journal.eventCommits != 1 || journal.catalogCommits != 0 {
		t.Fatalf("duplicate replay side effects: project_events=%d workspace_events=%d catalog=%d", environment.projects.eventCount, journal.eventCommits, journal.catalogCommits)
	}
	if environment.projects.transitions != transitionsBeforeReplay+1 {
		t.Fatalf("terminal replay did not revalidate the lifecycle transaction: transitions=%d before=%d", environment.projects.transitions, transitionsBeforeReplay)
	}
}

func TestProjectArchiveConflictRejectsConcurrentPlan(t *testing.T) {
	environment, first := newProjectArchiveApplyEnvironment(t)
	second, err := environment.service.PlanProjectPhysicalArchive(context.Background(), projectArchivePlanRequest(), first.Custody.ProjectID, ProjectPhysicalArchivePlanInput{
		OperationID: "workspace_archive_operation_01ARZ3NDEKTSV4RRFFQ69G5FAZ", Reason: "archive completed project", PlannedAt: first.Workspace.PlannedAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	failed := false
	environment.service.ArchiveFailureHook = func(boundary ProjectPhysicalArchiveFailureBoundary) error {
		if boundary == ProjectArchiveBoundaryAfterDeactivation && !failed {
			failed = true
			return errors.New("stop first operation")
		}
		return nil
	}
	if _, err := environment.service.ApplyProjectPhysicalArchive(context.Background(), first, first.PlanDigest); err == nil {
		t.Fatal("first operation did not stop")
	}
	environment.service.ArchiveFailureHook = nil
	if _, err := environment.service.ApplyProjectPhysicalArchive(context.Background(), second, second.PlanDigest); err == nil || !strings.Contains(err.Error(), "conflicts") {
		t.Fatalf("conflicting plan error = %v", err)
	}
	if _, err := os.Lstat(environment.canonicalRoot); err != nil {
		t.Fatalf("conflicting operation moved source: %v", err)
	}
}

func TestProjectArchiveConcurrentReplaySerializes(t *testing.T) {
	environment, plan := newProjectArchiveApplyEnvironment(t)
	var wg sync.WaitGroup
	errorsFound := make(chan error, 2)
	results := make(chan ProjectPhysicalArchiveResult, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := environment.service.ApplyProjectPhysicalArchive(context.Background(), plan, plan.PlanDigest)
			if err != nil {
				errorsFound <- err
				return
			}
			results <- result
		}()
	}
	wg.Wait()
	close(errorsFound)
	close(results)
	for err := range errorsFound {
		t.Fatal(err)
	}
	count := 0
	replays := 0
	for result := range results {
		count++
		if result.Replay {
			replays++
		}
	}
	if count != 2 || replays != 1 || environment.projects.eventCount != 1 {
		t.Fatalf("concurrent results=%d replays=%d events=%d", count, replays, environment.projects.eventCount)
	}
}

func TestProjectArchiveRequiresExactOwnerNodeQuiescenceBeforeMove(t *testing.T) {
	environment, plan := newProjectArchiveApplyEnvironment(t)
	request, err := projectRuntimeQuiescenceRequest(plan)
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]bool{}
	for _, target := range request.Targets {
		kinds[target.Kind] = true
		switch target.Kind {
		case "watched_root_supervisor":
			if target.OwnerNode != "main" || target.LocalRootKey != "project" || target.BackendRootRef == "" || target.WorkerKey != "worker" || target.ConfigHash != "sha256:"+strings.Repeat("a", 64) {
				t.Fatalf("watched-root quiescence identity = %#v", target)
			}
		case "service_process":
			if target.OwnerNode != "main" || target.ProviderKey != "service-one" || target.ProviderAddress != "main@service-one" || target.ProviderID != "prov_01ARZ3NDEKTSV4RRFFQ69G5FAV" ||
				target.RuntimeProfileDigest != "sha256:"+strings.Repeat("c", 64) || target.AllowlistKey != "service-one" || target.Manager != "systemd" || target.Unit != "loom-service-one.service" {
				t.Fatalf("service quiescence identity = %#v", target)
			}
		}
	}
	if !kinds["watched_root_supervisor"] || !kinds["service_process"] {
		t.Fatalf("quiescence targets do not distinguish owner runtime processes: %#v", request.Targets)
	}
	environment.service.RuntimeQuiescence = nil
	result, err := environment.service.ApplyProjectPhysicalArchive(context.Background(), plan, plan.PlanDigest)
	if err == nil || !strings.Contains(err.Error(), "registry disablement is insufficient") {
		t.Fatalf("quiescence error = %v", err)
	}
	if result.Phase != projects.ProjectArchivePhaseDeactivationPending || !result.Recoverable || !result.MutationBlocked {
		t.Fatalf("fail-closed result = %#v", result)
	}
	if _, err := os.Lstat(environment.canonicalRoot); err != nil {
		t.Fatalf("project moved without owner-node quiescence evidence: %v", err)
	}
	if _, err := environment.workspace.InspectArchive(context.Background(), plan.Workspace.OperationID); err == nil {
		t.Fatal("generic workspace operation started without owner-node quiescence evidence")
	}
}

func TestProjectArchiveEmptyQuiescenceTargetSetProducesNoNodeCall(t *testing.T) {
	environment := newProjectArchivePlanEnvironment(t, projectArchiveAdapterFixture{
		Key: "canonical-empty-runtime", OwnerNode: "main", RootKind: "canonical", RepositoryCount: 1,
	})
	plan, err := environment.service.PlanProjectPhysicalArchive(context.Background(), projectArchivePlanRequest(), "canonical-empty-runtime", ProjectPhysicalArchivePlanInput{
		OperationID: projectArchiveAdapterOperationID, Reason: "archive completed project",
		PlannedAt: time.Date(2026, 9, 4, 10, 30, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	request, err := projectRuntimeQuiescenceRequest(plan)
	if err != nil {
		t.Fatal(err)
	}
	if request.Targets == nil || len(request.Targets) != 0 {
		t.Fatalf("empty target set is not canonical: %#v", request.Targets)
	}
	if _, err := environment.service.ApplyProjectPhysicalArchive(context.Background(), plan, plan.PlanDigest); err != nil {
		t.Fatal(err)
	}
	if environment.quiescence.calls != 0 {
		t.Fatalf("empty target set made %d node calls", environment.quiescence.calls)
	}
}

func TestProjectArchiveRejectsNonTerminalQuiescenceReceiptBeforeMove(t *testing.T) {
	environment, plan := newProjectArchiveApplyEnvironment(t)
	environment.quiescence.mutate = func(receipt *ProjectRuntimeQuiescenceReceipt) {
		receipt.Evidence[0].State = "running"
	}
	result, err := environment.service.ApplyProjectPhysicalArchive(context.Background(), plan, plan.PlanDigest)
	if err == nil || !strings.Contains(err.Error(), "lacks exact terminal quiescence evidence") {
		t.Fatalf("quiescence receipt error = %v", err)
	}
	if result.Phase != projects.ProjectArchivePhaseDeactivationPending {
		t.Fatalf("state advanced past missing quiescence: %#v", result.State)
	}
	if _, err := os.Lstat(environment.canonicalRoot); err != nil {
		t.Fatalf("project moved with non-terminal quiescence receipt: %v", err)
	}
}

func TestProjectArchiveRecoveryRejectsChangedQuiescenceReceipt(t *testing.T) {
	environment, plan := newProjectArchiveApplyEnvironment(t)
	failOnceAtProjectArchiveBoundary(t, &environment.service, plan, ProjectArchiveBoundaryAfterDeactivation)
	environment.quiescence.mutate = func(receipt *ProjectRuntimeQuiescenceReceipt) {
		receipt.Evidence[0].ReceiptID = "sha256:" + strings.Repeat("f", 64)
	}
	result, err := environment.service.RecoverProjectPhysicalArchive(context.Background(), plan, plan.PlanDigest)
	if err == nil || !strings.Contains(err.Error(), "runtime quiescence evidence changed") {
		t.Fatalf("changed quiescence receipt error = %v", err)
	}
	if result.Phase != projects.ProjectArchivePhaseRuntimeDeactivated || !result.Recoverable {
		t.Fatalf("changed receipt recovery result = %#v", result)
	}
	if _, err := os.Lstat(environment.canonicalRoot); err != nil {
		t.Fatalf("changed receipt recovery moved project: %v", err)
	}
}

func TestProjectArchiveDeactivationValidationAllowsIdempotentReplay(t *testing.T) {
	reviewed := ProjectArchiveDeactivationPlan{
		Input: projects.DeactivateProjectInput{Facet: "services"},
		Actions: []projects.ProjectDeactivationAction{{
			Key: "service-one", Kind: "service", Ref: "main@service-one", Status: "would_disable",
		}},
	}
	actual := projects.ProjectDeactivationResult{
		Facet: "services", Actions: []projects.ProjectDeactivationAction{{
			Key: "service-one", Kind: "service", Ref: "main@service-one", Status: "already_disabled",
		}},
	}
	if err := validateAppliedProjectDeactivation(reviewed, actual); err != nil {
		t.Fatalf("idempotent deactivation replay rejected: %v", err)
	}
	actual.Actions[0].Ref = "other@service-one"
	if err := validateAppliedProjectDeactivation(reviewed, actual); err == nil {
		t.Fatal("substituted idempotent action was accepted")
	}
	actual.Actions[0].Ref = reviewed.Actions[0].Ref
	reviewed.Actions[0].Metadata = json.RawMessage(`{"schema":"reviewed"}`)
	actual.Actions[0].Metadata = json.RawMessage(`{"schema":"substituted"}`)
	if err := validateAppliedProjectDeactivation(reviewed, actual); err == nil {
		t.Fatal("substituted service control metadata was accepted")
	}
}

func TestProjectArchiveRecoveryRejectsImmutableStateTamper(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*projects.ProjectPhysicalArchiveState)
	}{
		{name: "actor", mutate: func(state *projects.ProjectPhysicalArchiveState) { state.ActorID = "actor_substituted" }},
		{name: "active path", mutate: func(state *projects.ProjectPhysicalArchiveState) { state.ActivePath += "-substituted" }},
		{name: "reason", mutate: func(state *projects.ProjectPhysicalArchiveState) { state.Reason = "different reason" }},
		{name: "started at", mutate: func(state *projects.ProjectPhysicalArchiveState) { state.StartedAt = state.StartedAt.Add(time.Second) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			environment, plan := newProjectArchiveApplyEnvironment(t)
			failOnceAtProjectArchiveBoundary(t, &environment.service, plan, ProjectArchiveBoundaryAfterDeactivation)
			environment.projects.mu.Lock()
			state, ok := projects.ParseProjectPhysicalArchiveState(environment.projects.detail.Project.Project.ArchiveState)
			if !ok {
				environment.projects.mu.Unlock()
				t.Fatal("missing recovery state")
			}
			test.mutate(&state)
			environment.projects.detail.Project.Project.ArchiveState, _ = json.Marshal(state)
			environment.projects.mu.Unlock()
			if _, err := environment.service.RecoverProjectPhysicalArchive(context.Background(), plan, plan.PlanDigest); err == nil || !strings.Contains(err.Error(), "immutable reviewed plan fields") {
				t.Fatalf("tampered recovery error = %v", err)
			}
			if _, err := os.Lstat(environment.canonicalRoot); err != nil {
				t.Fatalf("tampered recovery moved project: %v", err)
			}
		})
	}
}

func TestProjectArchiveRecoveryRejectsUnrelatedMetadataDrift(t *testing.T) {
	environment, plan := newProjectArchiveApplyEnvironment(t)
	failOnceAtProjectArchiveBoundary(t, &environment.service, plan, ProjectArchiveBoundaryAfterDeactivation)
	environment.projects.mu.Lock()
	environment.projects.detail.Project.Project.Metadata = json.RawMessage(`{"unreviewed":"drift"}`)
	environment.projects.mu.Unlock()
	if _, err := environment.service.RecoverProjectPhysicalArchive(context.Background(), plan, plan.PlanDigest); err == nil || !strings.Contains(err.Error(), "outside allowed lifecycle fields") {
		t.Fatalf("metadata drift error = %v", err)
	}
	if _, err := os.Lstat(environment.canonicalRoot); err != nil {
		t.Fatalf("metadata drift recovery moved project: %v", err)
	}
}

func TestProjectArchiveRecoveryRejectsUnknownStateFields(t *testing.T) {
	environment, plan := newProjectArchiveApplyEnvironment(t)
	failOnceAtProjectArchiveBoundary(t, &environment.service, plan, ProjectArchiveBoundaryAfterDeactivation)
	environment.projects.mu.Lock()
	raw := strings.TrimSuffix(string(environment.projects.detail.Project.Project.ArchiveState), "}") + `,"unexpected_state":true}`
	environment.projects.detail.Project.Project.ArchiveState = json.RawMessage(raw)
	environment.projects.mu.Unlock()
	if _, err := environment.service.RecoverProjectPhysicalArchive(context.Background(), plan, plan.PlanDigest); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("unknown archive-state field error = %v", err)
	}
	if _, err := os.Lstat(environment.canonicalRoot); err != nil {
		t.Fatalf("unknown state recovery moved project: %v", err)
	}
}

func TestProjectArchiveRecoveryRejectsPhaseAheadOfWorkspaceEvidence(t *testing.T) {
	environment, plan := newProjectArchiveApplyEnvironment(t)
	failOnceAtProjectArchiveBoundary(t, &environment.service, plan, ProjectArchiveBoundaryAfterDeactivation)
	environment.projects.mu.Lock()
	state, ok := projects.ParseProjectPhysicalArchiveState(environment.projects.detail.Project.Project.ArchiveState)
	if !ok {
		environment.projects.mu.Unlock()
		t.Fatal("missing recovery state")
	}
	workspaceAt := *state.RuntimeDeactivatedAt
	state.Phase = projects.ProjectArchivePhaseProjectStatePending
	state.WorkspaceCompletedAt = &workspaceAt
	state.ArchiveManifestDigest = "sha256:" + strings.Repeat("e", 64)
	environment.projects.detail.Project.Project.ArchiveState, _ = json.Marshal(state)
	environment.projects.mu.Unlock()
	if _, err := environment.service.RecoverProjectPhysicalArchive(context.Background(), plan, plan.PlanDigest); err == nil || !strings.Contains(err.Error(), "without generic operation evidence") {
		t.Fatalf("phase chronology error = %v", err)
	}
	if _, err := os.Lstat(environment.canonicalRoot); err != nil {
		t.Fatalf("phase chronology recovery moved project: %v", err)
	}
}

func TestProjectArchiveTerminalReplayRejectsInconsistentLifecycle(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*projectArchivePlanEnvironment)
	}{
		{name: "registration", mutate: func(environment *projectArchivePlanEnvironment) {
			environment.projects.detail.Registration.ActivationStatus = projects.ProjectActivationStatusBaseActive
		}},
		{name: "repository member", mutate: func(environment *projectArchivePlanEnvironment) {
			environment.repositories.state.Members[0].MembershipLifecycle = projects.RepositoryLifecycleActive
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			environment, plan := newProjectArchiveApplyEnvironment(t)
			if _, err := environment.service.ApplyProjectPhysicalArchive(context.Background(), plan, plan.PlanDigest); err != nil {
				t.Fatal(err)
			}
			environment.projects.mu.Lock()
			environment.repositories.mu.Lock()
			test.mutate(&environment)
			environment.repositories.mu.Unlock()
			environment.projects.mu.Unlock()
			if _, err := environment.service.ApplyProjectPhysicalArchive(context.Background(), plan, plan.PlanDigest); err == nil || !strings.Contains(err.Error(), "lifecycle is inconsistent") {
				t.Fatalf("inconsistent terminal replay error = %v", err)
			}
			if environment.projects.eventCount != 1 {
				t.Fatalf("terminal inconsistency emitted duplicate events: %d", environment.projects.eventCount)
			}
		})
	}
}

func failOnceAtProjectArchiveBoundary(t *testing.T, service *ProjectRuntimeService, plan ProjectPhysicalArchivePlan, boundary ProjectPhysicalArchiveFailureBoundary) {
	t.Helper()
	failed := false
	service.ArchiveFailureHook = func(observed ProjectPhysicalArchiveFailureBoundary) error {
		if observed == boundary && !failed {
			failed = true
			return errors.New("injected project archive crash")
		}
		return nil
	}
	if _, err := service.ApplyProjectPhysicalArchive(context.Background(), plan, plan.PlanDigest); err == nil {
		t.Fatal("project archive did not stop at injected boundary")
	}
	service.ArchiveFailureHook = nil
}

func newProjectArchiveApplyEnvironment(t *testing.T) (projectArchivePlanEnvironment, ProjectPhysicalArchivePlan) {
	t.Helper()
	environment := newProjectArchivePlanEnvironment(t, projectArchiveAdapterFixture{
		Key: "canonical-many", OwnerNode: "main", RootKind: "canonical", RepositoryCount: 3,
		RuntimeFacets: []string{"scripts", "workflows", "connectors", "schedules", "direct_events", "watched_roots", "modules", "services"},
	})
	plan, err := environment.service.PlanProjectPhysicalArchive(context.Background(), projectArchivePlanRequest(), "canonical-many", ProjectPhysicalArchivePlanInput{
		OperationID: projectArchiveAdapterOperationID,
		Reason:      "archive completed project",
		PlannedAt:   time.Date(2026, 9, 4, 10, 30, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	return environment, plan
}

func sortStringsEqual(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	sorted := append([]string(nil), right...)
	sort.Strings(sorted)
	for index := range left {
		if left[index] != sorted[index] {
			return false
		}
	}
	return true
}

func TestDeclarationArchiveApplyRereadsOwnerBeforeMutation(t *testing.T) {
	for _, name := range []string{"missing_source", "source_revision", "registration_hash"} {
		t.Run(name, func(t *testing.T) {
			environment, plan := declarationArchivePlanEnvironment(t)
			switch name {
			case "missing_source":
				environment.repositories.state.Source = nil
			case "source_revision":
				environment.repositories.state.Source.SourceRevision++
			case "registration_hash":
				environment.projects.detail.Registration.ContractHash = "sha256:" + strings.Repeat("0", 64)
			}
			calls := len(environment.activation.inputs)
			if _, err := environment.service.ApplyProjectPhysicalArchive(t.Context(), plan, plan.PlanDigest); err == nil {
				t.Fatal("stale declaration owner accepted")
			}
			if len(environment.activation.inputs) != calls || environment.quiescence.calls != 0 {
				t.Fatal("fresh-owner drift reached runtime deactivation")
			}
			if _, err := os.Stat(environment.canonicalRoot); err != nil {
				t.Fatal("fresh-owner drift moved source")
			}
		})
	}
}
