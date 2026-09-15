package nodes

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"loom.local/loom/internal/nodeprofiles"
)

func TestAssignDefaultProfilesUsesNodeProfileResolver(t *testing.T) {
	t.Parallel()

	fake := &fakeProfileAssignmentDB{rowsAffected: 1}
	err := assignDefaultProfiles(
		context.Background(),
		fake,
		"node_primary",
		"workspace",
		"primary_workspace",
		"workspace_full",
		"actor_admin",
	)
	if err != nil {
		t.Fatalf("assignDefaultProfiles returned error: %v", err)
	}
	if fake.execCount != 1 {
		t.Fatalf("exec count = %d, want 1", fake.execCount)
	}
	if !strings.Contains(fake.query, "CROSS JOIN nodes.runtime_profiles") {
		t.Fatalf("query did not independently select runtime profile: %s", fake.query)
	}
	if !strings.Contains(fake.query, `"source":"node_profile_resolver"`) {
		t.Fatalf("query did not write resolver metadata: %s", fake.query)
	}
	wantArgs := []any{
		"node_primary",
		nodeprofiles.AuthorityPrimaryWorkspaceDefault,
		nodeprofiles.RuntimeWorkspaceFull,
		"actor_admin",
	}
	if len(fake.args) != len(wantArgs) {
		t.Fatalf("args = %#v, want %#v", fake.args, wantArgs)
	}
	for i := range wantArgs {
		if fake.args[i] != wantArgs[i] {
			t.Fatalf("args[%d] = %#v, want %#v; args=%#v", i, fake.args[i], wantArgs[i], fake.args)
		}
	}
}

func TestAssignDefaultProfilesRejectsInvalidRuntimeCompatibility(t *testing.T) {
	t.Parallel()

	fake := &fakeProfileAssignmentDB{rowsAffected: 1}
	err := assignDefaultProfiles(
		context.Background(),
		fake,
		"node_bad",
		"main",
		"main",
		"hardware_agent",
		"actor_admin",
	)
	if err == nil {
		t.Fatal("assignDefaultProfiles returned nil error")
	}
	if fake.execCount != 0 {
		t.Fatalf("exec count = %d, want 0", fake.execCount)
	}
}

func TestAssignDefaultProfilesFailsWhenSeedRowsAreMissing(t *testing.T) {
	t.Parallel()

	fake := &fakeProfileAssignmentDB{rowsAffected: 0}
	err := assignDefaultProfiles(
		context.Background(),
		fake,
		"node_missing_seed",
		"workspace",
		"primary_workspace",
		"workspace_full",
		"actor_admin",
	)
	if err == nil {
		t.Fatal("assignDefaultProfiles returned nil error")
	}
	if !strings.Contains(err.Error(), "node profile assignment could not resolve") {
		t.Fatalf("error = %q, want missing seed diagnostic", err)
	}
}

func TestNormalizeEnrollmentRequestInputDefaultsToCanonicalProfiles(t *testing.T) {
	t.Parallel()

	input := normalizeEnrollmentRequestInput(CreateEnrollmentRequestInput{
		RequestedNodeKey:     " node-workspace ",
		RequestedDisplayName: " Workspace ",
		RequestedNodeKind:    " workspace ",
	})

	if input.RequestedNodeKey != "node-workspace" {
		t.Fatalf("RequestedNodeKey = %q", input.RequestedNodeKey)
	}
	if input.RequestedDisplayName != "Workspace" {
		t.Fatalf("RequestedDisplayName = %q", input.RequestedDisplayName)
	}
	if input.RequestedNodeRole != "workspace" {
		t.Fatalf("RequestedNodeRole = %q, want workspace", input.RequestedNodeRole)
	}
	if input.RequestedRuntimeClass != nodeprofiles.RuntimeWorkspaceFull {
		t.Fatalf("RequestedRuntimeClass = %q, want %q", input.RequestedRuntimeClass, nodeprofiles.RuntimeWorkspaceFull)
	}
}

type fakeProfileAssignmentDB struct {
	query        string
	args         []any
	execCount    int
	rowsAffected int64
}

func (f *fakeProfileAssignmentDB) QueryContext(context.Context, string, ...any) (*sql.Rows, error) {
	panic("QueryContext should not be called")
}

func (f *fakeProfileAssignmentDB) QueryRowContext(context.Context, string, ...any) *sql.Row {
	panic("QueryRowContext should not be called")
}

func (f *fakeProfileAssignmentDB) ExecContext(_ context.Context, query string, args ...any) (sql.Result, error) {
	f.execCount++
	f.query = query
	f.args = args
	return fakeProfileAssignmentResult{rowsAffected: f.rowsAffected}, nil
}

type fakeProfileAssignmentResult struct {
	rowsAffected int64
}

func (r fakeProfileAssignmentResult) LastInsertId() (int64, error) {
	return 0, nil
}

func (r fakeProfileAssignmentResult) RowsAffected() (int64, error) {
	return r.rowsAffected, nil
}
