package agentpack

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

func validPackPath(t *testing.T) string {
	t.Helper()
	path, err := filepath.Abs(filepath.Join("testdata", "valid-pack"))
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func loadValidPack(t *testing.T) *Pack {
	t.Helper()
	pack, err := LoadFromPath(validPackPath(t))
	if err != nil {
		t.Fatal(err)
	}
	return pack
}

func TestLoadPackAndResolvePrecedence(t *testing.T) {
	pack := loadValidPack(t)
	if pack.Manifest.Name != "fixture-pack" || len(pack.Catalogue.Skills) != 1 {
		t.Fatalf("unexpected pack: %#v", pack)
	}
	root, err := ResolveRoot(ResolveOptions{ExplicitPath: validPackPath(t), Environment: "/missing"})
	if err != nil || root.Source != RootExplicit {
		t.Fatalf("root=%#v err=%v", root, err)
	}
	root, err = ResolveRoot(ResolveOptions{Environment: validPackPath(t), ExecutablePath: "/missing/bin/loom"})
	if err != nil || root.Source != RootEnvironment {
		t.Fatalf("root=%#v err=%v", root, err)
	}
}

func TestLoadRejectsCatalogueTraversal(t *testing.T) {
	root := copyPackFixture(t)
	replaceFile(t, filepath.Join(root, "manifest.yaml"), "catalogue: catalogue.yaml", "catalogue: ../outside.yaml")
	if _, err := LoadFromPath(root); err == nil {
		t.Fatal("expected catalogue traversal to fail")
	}
}

func TestLoadRejectsRetiredInstallationProfileSchema(t *testing.T) {
	root := copyPackFixture(t)
	replaceFile(t, filepath.Join(root, "manifest.yaml"), "recommended_skill_sets:", "installation_profiles:")
	if _, err := LoadFromPath(root); err == nil {
		t.Fatal("expected retired installation profile schema to fail")
	}
}

func copyPackFixture(t *testing.T) string {
	t.Helper()
	destination := filepath.Join(t.TempDir(), "pack")
	err := filepath.WalkDir(validPackPath(t), func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(validPackPath(t), path)
		if err != nil {
			return err
		}
		target := filepath.Join(destination, relative)
		if entry.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		payload, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, payload, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
	return destination
}

func replaceFile(t *testing.T, path, old, replacement string) {
	t.Helper()
	payload, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	updated := []byte(string(payload))
	updated = []byte(replaceOnce(string(updated), old, replacement))
	if err := os.WriteFile(path, updated, 0o644); err != nil {
		t.Fatal(err)
	}
}

func replaceOnce(value, old, replacement string) string {
	for index := 0; index+len(old) <= len(value); index++ {
		if value[index:index+len(old)] == old {
			return value[:index] + replacement + value[index+len(old):]
		}
	}
	return value
}
