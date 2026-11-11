package applicationbuild

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDeclaredPackageDataIsCapturedWithoutPrivateState(t *testing.T) {
	service, input, _ := fixture(t)
	file := filepath.Join(input.SourceRoot, "app", "default_config.json")
	if err := os.WriteFile(file, []byte(`{"mode":"read_only"}`), 0600); err != nil {
		t.Fatal(err)
	}
	input.PackageFiles = []string{"app/default_config.json"}
	first, err := service.Snapshot(t.Context(), input)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, file := range first.Files {
		found = found || file.Path == "app/default_config.json"
	}
	if !found {
		t.Fatal("declared package data omitted")
	}
	for _, unsafe := range []string{".data/token.json", "app/credentials.json", "../secret.json"} {
		next := input
		next.PackageFiles = []string{unsafe}
		if _, err := service.Snapshot(t.Context(), next); err == nil {
			t.Fatal("private/escaping input accepted", unsafe)
		}
	}
	if err := os.WriteFile(file, []byte(`{"mode":"changed"}`), 0600); err != nil {
		t.Fatal(err)
	}
	second, err := service.Snapshot(t.Context(), input)
	if err != nil || first.SourceDigest == second.SourceDigest {
		t.Fatal("package data not fenced", err)
	}
}
