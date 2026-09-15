package filepolicy

import "testing"

func TestGeneratedPolicyRecognizesRuntimeAndDependencyPaths(t *testing.T) {
	for _, path := range []string{
		"repo/node_modules/pkg/index.js",
		"repo/.git/config",
		"repo/.venv/bin/python",
		"repo/dist/bundle.js",
		"repo/build/output.o",
		"repo/target/debug/app",
		"repo/.cache/tool/cache.db",
		"repo/.data/runtime.db",
		"repo/.secrets/token",
		"repo/__pycache__/module.pyc",
		"Documents/.DS_Store",
		"Documents/._report.md",
		"Downloads/file.partial",
		"Downloads/file.crdownload",
	} {
		if !IsGeneratedPath(path) {
			t.Fatalf("%q should be generated", path)
		}
	}
	for _, path := range []string{
		"repo/src/index.ts",
		"Documents/report.md",
		"Notes/research/building-a-model.md",
	} {
		if IsGeneratedPath(path) {
			t.Fatalf("%q should not be generated", path)
		}
	}
}

func TestGeneratedPolicyExposesDefaultExcludes(t *testing.T) {
	patterns := DefaultExcludePatterns()
	for _, want := range []string{"**/node_modules/**", "**/.git/**", "**/.venv/**", "**/dist/**", "**/.secrets/**", "**/.DS_Store", "**/*.partial"} {
		if !containsGeneratedPolicyPattern(patterns, want) {
			t.Fatalf("DefaultExcludePatterns missing %q: %#v", want, patterns)
		}
	}
}

func containsGeneratedPolicyPattern(patterns []string, want string) bool {
	for _, pattern := range patterns {
		if pattern == want {
			return true
		}
	}
	return false
}
