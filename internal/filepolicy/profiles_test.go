package filepolicy

import "testing"

func TestBuiltInProfiles(t *testing.T) {
	tests := []struct {
		name     string
		profile  Profile
		path     string
		included bool
		category RuleCategory
	}{
		{name: "managed dependency", profile: ProfileManaged, path: "repo/node_modules/pkg/index.js", category: RuleCategoryReconstructible},
		{name: "source-only environment", profile: ProfileSourceOnly, path: "repo/.venv/bin/python", category: RuleCategoryReconstructible},
		{name: "faithful dependency", profile: ProfileFaithful, path: "repo/node_modules/pkg/index.js", included: true, category: RuleCategoryNone},
		{name: "exact dependency", profile: ProfileExact, path: "repo/.pytest_cache/data", included: true, category: RuleCategoryNone},
		{name: "mandatory project state", profile: ProfileFaithful, path: "project/.loom/state/index.db", category: RuleCategoryMandatorySafety},
		{name: "mandatory project tmp", profile: ProfileExact, path: "project/.loom/tmp/export.tar", category: RuleCategoryMandatorySafety},
		{name: "mandatory transfer partial", profile: ProfileExact, path: "project/.loom-partial/chunk", category: RuleCategoryMandatorySafety},
		{name: "portable project control", profile: ProfileManaged, path: "project/.loom/contracts/backup.yaml", included: true, category: RuleCategoryNone},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := EvaluateBuiltIn(test.profile, test.path)
			if got.Included != test.included || got.RuleCategory != test.category || got.Profile != test.profile || got.PolicyVersion != BuiltInPolicyVersion {
				t.Fatalf("EvaluateBuiltIn() = %#v", got)
			}
		})
	}
}

func TestManagedPolicyRetainsUserAndRepositoryState(t *testing.T) {
	for _, path := range []string{
		"repo/.git/config",
		"repo/.env",
		"repo/.secrets/token",
		"repo/.data/app.db",
		"repo/dist/bundle.js",
		"repo/build/output.o",
		"repo/target/debug/app",
	} {
		if got := EvaluateBuiltIn(ProfileManaged, path); !got.Included {
			t.Fatalf("managed policy unexpectedly excluded %q: %#v", path, got)
		}
	}
}

func TestParseProfile(t *testing.T) {
	for _, value := range []string{"managed", "faithful", "source_only", "exact"} {
		if _, err := ParseProfile(value); err != nil {
			t.Fatalf("ParseProfile(%q): %v", value, err)
		}
	}
	if _, err := ParseProfile("unknown"); err == nil {
		t.Fatal("expected unsupported profile error")
	}
}
