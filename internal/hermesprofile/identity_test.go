package hermesprofile

import (
	"testing"
)

func TestRecoveryIdentitySelection(t *testing.T) {
	fixture := "/tmp/.loom-acceptance/neutral-workspace"
	for _, tc := range []struct {
		name                   string
		id                     RecoveryIdentity
		path, location, origin string
		valid                  bool
	}{
		{"old-default", "", "", WorkspaceRoot, WorkspaceRoot, true},
		{"old-explicit", MorathustraIdentity, "", WorkspaceRoot, WorkspaceRoot, true},
		{"new-explicit", MinaIdentity, "", MinaWorkspaceRoot, MinaWorkspaceRoot, true},
		{"old-fixture-origin", "", fixture, fixture, fixture, true},
		{"new-fixture-origin", MinaIdentity, fixture, fixture, MinaWorkspaceRoot, true},
		{"explicit-old-fixture", MorathustraIdentity, fixture, fixture, WorkspaceRoot, true},
		{"no-inference", "", MinaWorkspaceRoot, "", "", false},
		{"conflicting-old", MinaIdentity, WorkspaceRoot, "", "", false},
		{"conflicting-new", MorathustraIdentity, MinaWorkspaceRoot, "", "", false},
		{"typo", "mnia", "", "", "", false},
		{"case", "MINA", "", "", "", false},
		{"path-is-not-identity", RecoveryIdentity(MinaWorkspaceRoot), "", "", "", false},
		{"noncanonical", MinaIdentity, MinaWorkspaceRoot + "/", "", "", false},
		{"arbitrary", MinaIdentity, "/tmp/mina", "", "", false},
		{"relative", MinaIdentity, ".loom-acceptance/mina", "", "", false},
		{"fixture-dotdot", MinaIdentity, fixture + "/../escape", "", "", false},
		{"production-fixture-old", MinaIdentity, WorkspaceRoot + "/.loom-acceptance/a", "", "", false},
		{"production-fixture-new", MinaIdentity, MinaWorkspaceRoot + "/.loom-acceptance/a", "", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			location, origin, err := ResolveWorkspace(tc.id, tc.path)
			if (err == nil) != tc.valid || tc.valid && (location != tc.location || origin != tc.origin) {
				t.Fatalf("%q %q %v", location, origin, err)
			}
		})
	}
	for _, path := range []string{WorkspaceRoot, MinaWorkspaceRoot, "/tmp/.loom-acceptance", fixture + "/"} {
		if FixtureWorkspace(path) {
			t.Fatalf("nonfixture admitted: %s", path)
		}
	}

}

func TestRecoveryIdentityPolicyBindings(t *testing.T) {
	for _, p := range []Policy{
		{Identity: "MINA"}, {Identity: MinaIdentity, Workspace: WorkspaceRoot},
		{RetainedMorathustra: []string{"old"}},
		{Identity: MorathustraIdentity, RetainedMorathustra: []string{"old"}},
		{Identity: MinaIdentity, RetainedMorathustra: []string{"old", "old"}},
		{Identity: MinaIdentity, RetainedMorathustra: []string{"../old"}},
		{Identity: MinaIdentity, RetainedMorathustra: []string{""}},
	} {
		if _, _, err := p.Resolve(); err == nil {
			t.Fatalf("invalid disabled policy accepted: %+v", p)
		}
	}
}
