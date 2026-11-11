package watchedroots

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"loom.local/loom/internal/filesystemconnector"
)

func TestRuntimePolicyReusesContinuationAndInvalidatesCurrentRules(t *testing.T) {
	directory := t.TempDir()
	root := testScanRoot(t, directory, RootConfig{RootKey: "notes", SafeRootKey: "slice09", IgnorePolicy: IgnorePolicy{Profile: "managed", DiscoverUserRules: true}})
	store := NewStore(t.TempDir())
	now := time.Now()
	_, err := Reconcile(t.Context(), store, ScanRequest{Root: root, Mode: ScanModeFull})
	if err != nil {
		t.Fatal(err)
	}
	fsConfig := filesystemconnector.Config{SafeRoots: []filesystemconnector.SafeRoot{filesystemconnector.DefaultSafeRoot("slice09", directory)}}
	first, err := ValidateRuntimeRoot(store, root.Config, fsConfig, now)
	if err != nil {
		t.Fatal(err)
	}
	second, err := ValidateRuntimeRoot(store, root.Config, fsConfig, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if first.PolicyResolver != second.PolicyResolver {
		t.Fatal("unchanged continuation replaced its policy snapshot")
	}
	if err := os.WriteFile(filepath.Join(directory, ".loomignore"), []byte("private/**\n"), 0600); err != nil {
		t.Fatal(err)
	}
	third, err := ValidateRuntimeRoot(store, root.Config, fsConfig, now.Add(2*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if third.PolicyResolver.Fingerprint() == first.PolicyResolver.Fingerprint() {
		t.Fatal("new policy was reused")
	}
	hints, err := store.ListDirtyHints(root.Config.RootKey)
	if err != nil || !hasRescanHint(hints) {
		t.Fatal("changed policy did not invalidate classifications", err)
	}
	if decision, err := third.PolicyResolver.Resolve("private/secret.md", false); err != nil || decision.Decision.Included {
		t.Fatal(decision, err)
	}
}

func TestOutputPageRefusesNewNestedPolicyBeforePlanning(t *testing.T) {
	directory := t.TempDir()
	if err := os.Mkdir(filepath.Join(directory, "nested"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "nested", "note.md"), []byte("protected"), 0600); err != nil {
		t.Fatal(err)
	}
	root := testScanRoot(t, directory, RootConfig{RootKey: "notes", SafeRootKey: "slice09", IgnorePolicy: IgnorePolicy{Profile: "managed", DiscoverUserRules: true}})
	store := NewStore(t.TempDir())
	if _, err := Reconcile(t.Context(), store, ScanRequest{Root: root, Mode: ScanModeFull}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "nested", ".loomignore"), []byte("*.md\n"), 0600); err != nil {
		t.Fatal(err)
	}
	plan, err := PlanOutputsPage(store, root, "test", "", 4096, 64)
	if !errors.Is(err, ErrPolicyInputsChanged) || len(plan.Actions) != 0 {
		t.Fatalf("stale policy produced output: %+v %v", plan, err)
	}
}

func TestRuntimePolicyReplacementRequiresFreshSourceClassification(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "source")
	if err := os.Mkdir(directory, 0700); err != nil {
		t.Fatal(err)
	}
	root := testScanRoot(t, directory, RootConfig{RootKey: "notes", SafeRootKey: "slice09", IgnorePolicy: IgnorePolicy{Profile: "managed", DiscoverUserRules: true}})
	store := NewStore(t.TempDir())
	if _, err := Reconcile(t.Context(), store, ScanRequest{Root: root, Mode: ScanModeFull}); err != nil {
		t.Fatal(err)
	}
	fsConfig := filesystemconnector.Config{SafeRoots: []filesystemconnector.SafeRoot{filesystemconnector.DefaultSafeRoot("slice09", directory)}}
	if _, err := ValidateRuntimeRoot(store, root.Config, fsConfig, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(directory, directory+"-old"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(directory, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := ValidateRuntimeRoot(store, root.Config, fsConfig, time.Now()); err != nil {
		t.Fatal(err)
	}
	hints, err := store.ListDirtyHints(root.Config.RootKey)
	if err != nil || !hasRescanHint(hints) {
		t.Fatal("replacement source reused old classifications", err)
	}
}
