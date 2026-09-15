package filepolicy

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoomignoreGitStyleMatchingAndMetadata(t *testing.T) {
	root := t.TempDir()
	writePolicyTestFile(t, filepath.Join(root, ".loomignore"), strings.Join([]string{
		"# comment",
		"/anchored.txt",
		"scratch/",
		"*.tmp",
		"reports/**/draft?.md",
		`\#literal`,
		`\!important`,
		"!scratch/keep.txt",
		"",
	}, "\n"))
	resolver := newPolicyTestResolver(t, root, ProfileManaged)
	tests := []struct {
		path     string
		isDir    bool
		included bool
		pattern  string
	}{
		{path: "anchored.txt", included: false, pattern: "/anchored.txt"},
		{path: "nested/anchored.txt", included: true},
		{path: "scratch/file.bin", included: false, pattern: "scratch/"},
		{path: "scratch/keep.txt", included: true, pattern: "!scratch/keep.txt"},
		{path: "nested/cache.tmp", included: false, pattern: "*.tmp"},
		{path: "reports/2026/draft1.md", included: false, pattern: "reports/**/draft?.md"},
		{path: "#literal", included: false, pattern: `\#literal`},
		{path: "!important", included: false, pattern: `\!important`},
		{path: ".loomignore", included: true},
	}
	for _, test := range tests {
		t.Run(test.path, func(t *testing.T) {
			result, err := resolver.Resolve(test.path, test.isDir)
			if err != nil {
				t.Fatal(err)
			}
			if result.Decision.Included != test.included || result.Decision.Pattern != test.pattern {
				t.Fatalf("Resolve() = %#v", result)
			}
			if test.pattern != "" && (result.Decision.SourceLine == 0 || result.Decision.SourceFile == "" || len(resolver.PolicyFiles()[0].ContentHash) != 71) {
				t.Fatalf("missing source metadata: %#v files=%#v", result.Decision, resolver.PolicyFiles())
			}
		})
	}
}

func TestNestedPolicyPrecedenceAndManagedNegation(t *testing.T) {
	root := t.TempDir()
	writePolicyTestFile(t, filepath.Join(root, ".loomignore"), "*.log\nnode_modules/\n")
	writePolicyTestFile(t, filepath.Join(root, "project", ".loomignore"), "!keep.log\n!node_modules/kept.js\n")
	resolver := newPolicyTestResolver(t, root, ProfileManaged)
	for _, test := range []struct {
		path     string
		included bool
	}{
		{path: "other/keep.log", included: false},
		{path: "project/keep.log", included: true},
		{path: "project/drop.log", included: false},
		{path: "project/node_modules/drop.js", included: false},
		{path: "project/node_modules/kept.js", included: true},
	} {
		result, err := resolver.Resolve(test.path, false)
		if err != nil {
			t.Fatal(err)
		}
		if result.Decision.Included != test.included {
			t.Fatalf("%s included=%t, want %t: %#v", test.path, result.Decision.Included, test.included, result)
		}
	}
}

func TestPolicyBoundaryAppliesAncestorThenWatchedRootRules(t *testing.T) {
	policyRoot := t.TempDir()
	root := filepath.Join(policyRoot, "Documents")
	writePolicyTestFile(t, filepath.Join(policyRoot, ".loomignore"), "*.cache\n")
	writePolicyTestFile(t, filepath.Join(root, ".loomignore"), "!keep.cache\n")
	resolver, err := NewResolver(root, ProfileManaged, ResolverOptions{DiscoverUserRules: true, PolicyRoot: policyRoot})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		path     string
		included bool
	}{
		{path: "drop.cache", included: false},
		{path: "keep.cache", included: true},
	} {
		result, err := resolver.Resolve(test.path, false)
		if err != nil || result.Decision.Included != test.included {
			t.Fatalf("%s resolution=%#v err=%v", test.path, result, err)
		}
	}
}

func TestMandatoryPolicyCannotBeNegatedAndExactSkipsUserRules(t *testing.T) {
	root := t.TempDir()
	writePolicyTestFile(t, filepath.Join(root, ".loomignore"), "*\n!.loom/state/secret.db\n")
	managed := newPolicyTestResolver(t, root, ProfileManaged)
	result, err := managed.Resolve(".loom/state/secret.db", false)
	if err != nil {
		t.Fatal(err)
	}
	if result.Decision.Included || result.Decision.RuleCategory != RuleCategoryMandatorySafety {
		t.Fatalf("mandatory state was re-included: %#v", result)
	}
	exact, err := NewResolver(root, ProfileExact, ResolverOptions{DiscoverUserRules: true})
	if err != nil {
		t.Fatal(err)
	}
	result, err = exact.Resolve("ordinary.txt", false)
	if err != nil || !result.Decision.Included || len(exact.PolicyFiles()) != 0 {
		t.Fatalf("exact policy applied user rules: result=%#v err=%v", result, err)
	}
}

func TestContractExcludeWinsAfterUserNegation(t *testing.T) {
	root := t.TempDir()
	writePolicyTestFile(t, filepath.Join(root, ".loomignore"), "*.tmp\n!keep.tmp\n")
	resolver, err := NewResolver(root, ProfileManaged, ResolverOptions{
		DiscoverUserRules: true,
		ContractExcludes:  []string{"keep.tmp"},
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := resolver.Resolve("keep.tmp", false)
	if err != nil || result.Decision.Included || result.Decision.RuleCategory != RuleCategoryContract {
		t.Fatalf("contract precedence result=%#v err=%v", result, err)
	}
}

func TestPolicyEditInvalidatesResolverCache(t *testing.T) {
	root := t.TempDir()
	policyPath := filepath.Join(root, ".loomignore")
	writePolicyTestFile(t, policyPath, "first.txt\n")
	first := newPolicyTestResolver(t, root, ProfileFaithful)
	writePolicyTestFile(t, policyPath, "second.txt\n")
	second := newPolicyTestResolver(t, root, ProfileFaithful)
	if first.Fingerprint() == second.Fingerprint() {
		t.Fatal("policy edit did not invalidate resolver fingerprint")
	}
	result, err := second.Resolve("second.txt", false)
	if err != nil || result.Decision.Included {
		t.Fatalf("edited policy was stale: result=%#v err=%v", result, err)
	}
}

func TestMalformedAndSymlinkPolicyErrorsAreVisible(t *testing.T) {
	root := t.TempDir()
	writePolicyTestFile(t, filepath.Join(root, ".loomignore"), "[bad\n")
	if _, err := NewResolver(root, ProfileManaged, ResolverOptions{DiscoverUserRules: true}); err == nil || !strings.Contains(err.Error(), "malformed") {
		t.Fatalf("malformed policy error = %v", err)
	}
	os.Remove(filepath.Join(root, ".loomignore"))
	outside := filepath.Join(t.TempDir(), ".loomignore")
	writePolicyTestFile(t, outside, "secret\n")
	if err := os.Symlink(outside, filepath.Join(root, ".loomignore")); err != nil {
		t.Fatal(err)
	}
	if _, err := NewResolver(root, ProfileManaged, ResolverOptions{DiscoverUserRules: true}); err == nil || !strings.Contains(err.Error(), "must not be a symlink") {
		t.Fatalf("symlink policy error = %v", err)
	}
}

func TestResolvePathRejectsTraversalAndEscapingSymlink(t *testing.T) {
	root := t.TempDir()
	resolver := newPolicyTestResolver(t, root, ProfileManaged)
	if _, err := resolver.Resolve("../escape", false); err == nil {
		t.Fatal("expected path traversal error")
	}
	outside := filepath.Join(t.TempDir(), "outside.txt")
	writePolicyTestFile(t, outside, "outside")
	symlink := filepath.Join(root, "escape")
	if err := os.Symlink(outside, symlink); err != nil {
		t.Fatal(err)
	}
	if _, err := resolver.ResolvePath(symlink); err == nil || !strings.Contains(err.Error(), "escapes") {
		t.Fatalf("escaping symlink error = %v", err)
	}
}

func TestLargeRuleSetResolvesDeterministically(t *testing.T) {
	root := t.TempDir()
	var policy strings.Builder
	for index := 0; index < 5000; index++ {
		fmt.Fprintf(&policy, "cache-%04d/**\n", index)
	}
	policy.WriteString("!cache-4999/keep.txt\n")
	writePolicyTestFile(t, filepath.Join(root, ".loomignore"), policy.String())
	resolver := newPolicyTestResolver(t, root, ProfileManaged)
	result, err := resolver.Resolve("cache-4999/keep.txt", false)
	if err != nil || !result.Decision.Included {
		t.Fatalf("large rule set resolution = %#v err=%v", result, err)
	}
}

func newPolicyTestResolver(t *testing.T, root string, profile Profile) *Resolver {
	t.Helper()
	resolver, err := NewResolver(root, profile, ResolverOptions{DiscoverUserRules: true})
	if err != nil {
		t.Fatal(err)
	}
	return resolver
}

func writePolicyTestFile(t *testing.T, pathValue, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(pathValue), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pathValue, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
