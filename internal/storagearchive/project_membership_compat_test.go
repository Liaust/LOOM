package storagearchive

import (
	"bytes"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"loom.local/loom/internal/projects"
)

const legacyMembershipRevision = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func bindLegacyPersonalProject(env *projectArchivePlanEnvironment, planner *projectRestorePlanTestPlanner) {
	env.roots.BoxID, env.roots.Primary = "box_personal", true
	env.service.WorkspaceRoots = env.roots
	env.workspace.planner.Roots = env.roots
	if planner != nil {
		planner.Roots = env.roots
	}
}

func addLegacyProjectMembership(t *testing.T, env *projectArchivePlanEnvironment, home string) {
	t.Helper()
	metadata, err := projectMembershipMetadata(env.projects.detail.Project.Project.Metadata)
	if err != nil {
		t.Fatal(err)
	}
	if metadata == nil {
		metadata = map[string]any{}
	}
	metadata["home_box_id"], metadata["box_membership_revision"] = home, legacyMembershipRevision
	raw, err := json.Marshal(metadata)
	if err != nil {
		t.Fatal(err)
	}
	// These are the exact two additions and projects.updated_at write performed
	// by ReconcileBoxMembership. Both readers expose that same projects row.
	at := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	env.projects.detail.Project.Project.Metadata = raw
	env.projects.detail.Project.Project.UpdatedAt = at
	env.repositories.state.Project.UpdatedAt = at
}

func TestLegacyProjectMembershipArchiveThenFreshRestorePreservesSignedHistory(t *testing.T) {
	env, archive, planner := newProjectRestorePlanEnvironment(t)
	archiveBefore, _ := json.Marshal(archive)
	journal := env.workspace.planner.Journal.(*moveFakeJournal)
	journalBefore, _ := json.Marshal(journal.record)
	payloadBefore := snapshotWorkspacePayload(t, archive.Workspace.Destination.Path.AbsolutePath)
	bindLegacyPersonalProject(&env, planner)
	addLegacyProjectMembership(t, &env, "box_personal")
	currentBefore, _ := json.Marshal(env.projects.detail)
	plan, err := env.service.PlanProjectPhysicalRestore(t.Context(), projectRestorePlanRequest(), archive, projectRestorePlanInput())
	if err != nil {
		t.Fatal("fresh restore after membership reconciliation failed", err)
	}
	currentAfter, _ := json.Marshal(env.projects.detail)
	journalAfter, _ := json.Marshal(journal.record)
	if !bytes.Equal(currentAfter, currentBefore) || !bytes.Equal(journalAfter, journalBefore) {
		t.Fatal("planning rewrote current rows or signed archive history")
	}
	if !strings.Contains(string(plan.Registration.Project.Project.Metadata), "box_personal") {
		t.Fatal("fresh restore lost current attribution")
	}
	env.service.Now = planner.Now
	result, err := env.service.ApplyProjectPhysicalRestore(t.Context(), plan.Request, plan, plan.PlanDigest)
	if err != nil {
		t.Fatal("restore apply after membership reconciliation failed", err)
	}
	if result.State == nil || result.State.Phase != projects.ProjectRestorePhaseComplete {
		t.Fatalf("restore did not complete: %+v", result)
	}
	if got := snapshotWorkspacePayload(t, archive.Workspace.Source.Path.AbsolutePath); !reflect.DeepEqual(got, payloadBefore) {
		t.Fatal("restored payload changed")
	}
	if !strings.Contains(string(env.projects.detail.Project.Project.Metadata), "box_personal") {
		t.Fatal("restore discarded reconciled membership")
	}
	archiveAfter, _ := json.Marshal(archive)
	if !bytes.Equal(archiveBefore, archiveAfter) || archive.Workspace.BoxID != "" || archive.PlanDigest != plan.ArchivePlan.PlanDigest {
		t.Fatal("identity-free signed archive plan was rewritten")
	}
}

func TestLegacyProjectMembershipPendingArchiveReviewStillApplies(t *testing.T) {
	env, plan := newProjectArchiveApplyEnvironment(t)
	signedBefore, _ := json.Marshal(plan)
	bindLegacyPersonalProject(&env, nil)
	addLegacyProjectMembership(t, &env, "box_personal")
	result, err := env.service.ApplyProjectPhysicalArchive(t.Context(), plan, plan.PlanDigest)
	if err != nil {
		t.Fatal("pending old archive review refused additive membership", err)
	}
	if result.Phase != projects.ProjectArchivePhaseComplete {
		t.Fatal("archive incomplete")
	}
	after, _ := json.Marshal(plan)
	if !bytes.Equal(signedBefore, after) {
		t.Fatal("pending signed archive review changed")
	}
	if !strings.Contains(string(env.projects.detail.Project.Project.Metadata), "box_personal") {
		t.Fatal("archive dropped membership")
	}
}

func TestLegacyProjectMembershipPendingRestoreReviewStillApplies(t *testing.T) {
	env, plan, planner := newProjectRestoreApplyEnvironment(t)
	signedBefore, _ := json.Marshal(plan)
	bindLegacyPersonalProject(&env, planner)
	addLegacyProjectMembership(t, &env, "box_personal")
	result, err := env.service.ApplyProjectPhysicalRestore(t.Context(), plan.Request, plan, plan.PlanDigest)
	if err != nil {
		t.Fatal("pending old restore review refused additive membership", err)
	}
	if result.State == nil || result.State.Phase != projects.ProjectRestorePhaseComplete {
		t.Fatal("restore incomplete")
	}
	after, _ := json.Marshal(plan)
	if !bytes.Equal(signedBefore, after) {
		t.Fatal("pending signed restore review changed")
	}
}

func TestLegacyProjectMembershipKeepsExistingMetadataExact(t *testing.T) {
	env, plan := newProjectArchiveApplyEnvironment(t)
	original := json.RawMessage(`{"keep":{"policy":"original"},"counter":9007199254740993}`)
	plan.Registration.Project.Project.Metadata = original
	if err := SealProjectPhysicalArchivePlan(&plan); err != nil {
		t.Fatal(err)
	}
	env.projects.detail.Project.Project.Metadata = append(json.RawMessage(nil), original...)
	bindLegacyPersonalProject(&env, nil)
	addLegacyProjectMembership(t, &env, "box_personal")
	if err := validateCurrentProjectArchiveSnapshots(plan, env.roots, env.projects.detail, env.repositories.state, nil); err != nil {
		t.Fatal("unchanged prior metadata was not preserved", err)
	}
	metadata, _ := projectMembershipMetadata(env.projects.detail.Project.Project.Metadata)
	metadata["counter"] = json.Number("9007199254740992")
	env.projects.detail.Project.Project.Metadata, _ = json.Marshal(metadata)
	if err := validateCurrentProjectArchiveSnapshots(plan, env.roots, env.projects.detail, env.repositories.state, nil); err == nil {
		t.Fatal("integer metadata drift was hidden by membership normalization")
	}
}

func TestLegacyProjectMembershipComparisonRejectsOtherDrift(t *testing.T) {
	tests := []struct {
		name   string
		change func(*ProjectPhysicalArchivePlan, *TrustedWorkspaceRoots, *projects.ProjectRegistrationDetail, *projects.ProjectRepositoryReadModel)
	}{
		{"foreign home", func(_ *ProjectPhysicalArchivePlan, _ *TrustedWorkspaceRoots, d *projects.ProjectRegistrationDetail, _ *projects.ProjectRepositoryReadModel) {
			d.Project.Project.Metadata = json.RawMessage(`{"home_box_id":"box_company","box_membership_revision":"` + legacyMembershipRevision + `"}`)
		}},
		{"missing revision", func(_ *ProjectPhysicalArchivePlan, _ *TrustedWorkspaceRoots, d *projects.ProjectRegistrationDetail, _ *projects.ProjectRepositoryReadModel) {
			d.Project.Project.Metadata = json.RawMessage(`{"home_box_id":"box_personal"}`)
		}},
		{"invalid revision", func(_ *ProjectPhysicalArchivePlan, _ *TrustedWorkspaceRoots, d *projects.ProjectRegistrationDetail, _ *projects.ProjectRepositoryReadModel) {
			d.Project.Project.Metadata = json.RawMessage(`{"home_box_id":"box_personal","box_membership_revision":"not-evidence"}`)
		}},
		{"non-string revision", func(_ *ProjectPhysicalArchivePlan, _ *TrustedWorkspaceRoots, d *projects.ProjectRegistrationDetail, _ *projects.ProjectRepositoryReadModel) {
			d.Project.Project.Metadata = json.RawMessage(`{"home_box_id":"box_personal","box_membership_revision":3}`)
		}},
		{"additional metadata", func(_ *ProjectPhysicalArchivePlan, _ *TrustedWorkspaceRoots, d *projects.ProjectRegistrationDetail, _ *projects.ProjectRepositoryReadModel) {
			d.Project.Project.Metadata = json.RawMessage(`{"home_box_id":"box_personal","box_membership_revision":"` + legacyMembershipRevision + `","new_policy":true}`)
		}},
		{"existing metadata changed", func(p *ProjectPhysicalArchivePlan, _ *TrustedWorkspaceRoots, d *projects.ProjectRegistrationDetail, _ *projects.ProjectRepositoryReadModel) {
			p.Registration.Project.Project.Metadata = json.RawMessage(`{"policy":"original"}`)
			_ = SealProjectPhysicalArchivePlan(p)
			d.Project.Project.Metadata = json.RawMessage(`{"home_box_id":"box_personal","box_membership_revision":"` + legacyMembershipRevision + `","policy":"changed"}`)
		}},
		{"already attributed snapshot", func(p *ProjectPhysicalArchivePlan, _ *TrustedWorkspaceRoots, _ *projects.ProjectRegistrationDetail, _ *projects.ProjectRepositoryReadModel) {
			p.Registration.Project.Project.Metadata = json.RawMessage(`{"home_box_id":"box_personal"}`)
			_ = SealProjectPhysicalArchivePlan(p)
		}},
		{"explicit null prior key", func(p *ProjectPhysicalArchivePlan, _ *TrustedWorkspaceRoots, _ *projects.ProjectRegistrationDetail, _ *projects.ProjectRepositoryReadModel) {
			p.Registration.Project.Project.Metadata = json.RawMessage(`{"home_box_id":null}`)
			_ = SealProjectPhysicalArchivePlan(p)
		}},
		{"identity-free Company", func(_ *ProjectPhysicalArchivePlan, roots *TrustedWorkspaceRoots, _ *projects.ProjectRegistrationDetail, _ *projects.ProjectRepositoryReadModel) {
			roots.Primary = false
		}},
		{"missing trusted identity", func(_ *ProjectPhysicalArchivePlan, roots *TrustedWorkspaceRoots, _ *projects.ProjectRegistrationDetail, _ *projects.ProjectRepositoryReadModel) {
			roots.BoxID = ""
		}},
		{"foreign trusted source root", func(_ *ProjectPhysicalArchivePlan, roots *TrustedWorkspaceRoots, _ *projects.ProjectRegistrationDetail, _ *projects.ProjectRepositoryReadModel) {
			roots.BoxRoot += "-other"
		}},
		{"registration root drift", func(_ *ProjectPhysicalArchivePlan, _ *TrustedWorkspaceRoots, d *projects.ProjectRegistrationDetail, _ *projects.ProjectRepositoryReadModel) {
			d.Registration.ProjectRoot += "-other"
		}},
		{"repository root drift", func(_ *ProjectPhysicalArchivePlan, _ *TrustedWorkspaceRoots, _ *projects.ProjectRegistrationDetail, r *projects.ProjectRepositoryReadModel) {
			r.Source.ProjectRoot += "-other"
		}},
		{"lifecycle drift", func(_ *ProjectPhysicalArchivePlan, _ *TrustedWorkspaceRoots, d *projects.ProjectRegistrationDetail, _ *projects.ProjectRepositoryReadModel) {
			d.Project.Project.Status = "archived"
		}},
		{"repository lifecycle drift", func(_ *ProjectPhysicalArchivePlan, _ *TrustedWorkspaceRoots, _ *projects.ProjectRegistrationDetail, r *projects.ProjectRepositoryReadModel) {
			r.Members[0].MembershipLifecycle = projects.RepositoryLifecycleArchived
		}},
		{"unrelated project drift", func(_ *ProjectPhysicalArchivePlan, _ *TrustedWorkspaceRoots, d *projects.ProjectRegistrationDetail, _ *projects.ProjectRepositoryReadModel) {
			d.Project.Project.Name = "unreviewed"
		}},
		{"mixed project timestamps", func(_ *ProjectPhysicalArchivePlan, _ *TrustedWorkspaceRoots, _ *projects.ProjectRegistrationDetail, r *projects.ProjectRepositoryReadModel) {
			r.Project.UpdatedAt = r.Project.UpdatedAt.Add(time.Second)
		}},
		{"timestamp without additions", func(p *ProjectPhysicalArchivePlan, _ *TrustedWorkspaceRoots, d *projects.ProjectRegistrationDetail, _ *projects.ProjectRepositoryReadModel) {
			d.Project.Project.Metadata = p.Registration.Project.Project.Metadata
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			env, plan := newProjectArchiveApplyEnvironment(t)
			bindLegacyPersonalProject(&env, nil)
			addLegacyProjectMembership(t, &env, "box_personal")
			detail := cloneProjectArchiveTestValue(t, env.projects.detail)
			repository := cloneProjectArchiveTestValue(t, env.repositories.state)
			repository.Source.ProjectRoot = env.repositories.state.Source.ProjectRoot
			test.change(&plan, &env.roots, &detail, &repository)
			before, _ := json.Marshal(detail)
			signed, _ := json.Marshal(plan)
			if err := validateCurrentProjectArchiveSnapshots(plan, env.roots, detail, repository, nil); err == nil {
				t.Fatal("unqualified drift was normalized")
			}
			after, _ := json.Marshal(detail)
			afterPlan, _ := json.Marshal(plan)
			if !bytes.Equal(before, after) || !bytes.Equal(signed, afterPlan) {
				t.Fatal("rejected comparison mutated source or signed history")
			}
			if _, err := os.Stat(env.canonicalRoot); err != nil {
				t.Fatal("comparison touched payload", err)
			}
		})
	}
}
