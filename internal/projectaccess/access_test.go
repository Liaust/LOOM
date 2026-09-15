package projectaccess

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"loom.local/loom/internal/projectcontracts"
)

func TestAnalyzeChecksResolvedCanonicalContractPath(t *testing.T) {
	root := t.TempDir()
	contractPath := filepath.Join(root, filepath.FromSlash(projectcontracts.CanonicalRootContractPath))
	if err := os.MkdirAll(filepath.Dir(contractPath), 0o755); err != nil {
		t.Fatalf("create metadata directory: %v", err)
	}
	if err := os.WriteFile(contractPath, []byte(strings.TrimSpace(`
kind: loom.project
schema_version: project.contract.v0.3
project:
  slug: access-layout
  name: Access Layout
  owner_node: main
`)+"\n"), 0o600); err != nil {
		t.Fatalf("write contract: %v", err)
	}
	analysis := projectcontracts.Analyze(root)
	checks := Analyze(analysis)
	for _, check := range checks {
		if check.Key == "runtime_access.project_contract" {
			if check.Path != contractPath {
				t.Fatalf("contract check path = %q, want %q", check.Path, contractPath)
			}
			return
		}
	}
	t.Fatalf("missing project contract access check: %#v", checks)
}
