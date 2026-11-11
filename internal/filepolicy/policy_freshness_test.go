package filepolicy

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPolicyFreshnessDetectsNewNestedRuleWithoutInventoryDiscovery(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "nested"), 0700); err != nil {
		t.Fatal(err)
	}
	resolver, err := NewResolver(root, ProfileManaged, ResolverOptions{DiscoverUserRules: true})
	if err != nil {
		t.Fatal(err)
	}
	current, err := resolver.CurrentForPaths([]string{"nested/new.md"})
	if err != nil || !current {
		t.Fatalf("new ordinary path is not a policy change: %v %v", current, err)
	}
	policy := filepath.Join(root, "nested/.loomignore")
	if err := os.WriteFile(policy, []byte("*.md\n"), 0600); err != nil {
		t.Fatal(err)
	}
	current, err = resolver.CurrentForPaths([]string{"nested/new.md"})
	if err != nil || current {
		t.Fatalf("new nested policy missed: %v %v", current, err)
	}
	resolver, err = NewResolver(root, ProfileManaged, ResolverOptions{DiscoverUserRules: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(policy); err != nil {
		t.Fatal(err)
	}
	current, err = resolver.CurrentForPaths(nil)
	if err != nil || current {
		t.Fatalf("deleted known policy missed: %v %v", current, err)
	}
	if err := os.Symlink(filepath.Join(root, "missing"), policy); err != nil {
		t.Fatal(err)
	}
	if _, err := resolver.CurrentForPaths([]string{"nested/new.md"}); err == nil {
		t.Fatal("policy symlink accepted")
	}
}
