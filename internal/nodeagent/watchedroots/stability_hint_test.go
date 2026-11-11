package watchedroots

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestNativeObservationUsesOneStabilityWindow(t *testing.T) {
	for _, changed := range []bool{false, true} {
		t.Run(map[bool]string{false: "stable", true: "changed"}[changed], func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "note.md")
			if err := os.WriteFile(path, []byte("before"), 0600); err != nil {
				t.Fatal(err)
			}
			root := testScanRoot(t, dir, RootConfig{RootKey: "notes", SafeRootKey: "slice09", Include: []string{"**/*.md"}, Scan: ScanConfig{StabilityWindow: "2s"}})
			store := NewStore(t.TempDir())
			obs := ObservePath(root, "note.md")
			if changed {
				if err := os.WriteFile(path, []byte("new fingerprint"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if err := store.AddDirtyHint("notes", DirtyHint{RelativePath: "note.md", Source: "native", LastSeenAt: time.Now().Add(-3 * time.Second), Observation: &obs}); err != nil {
				t.Fatal(err)
			}
			result, err := Reconcile(t.Context(), store, ScanRequest{Root: root, Mode: ScanModeAuto})
			if err != nil {
				t.Fatal(err)
			}
			if !changed && result.Counts.HashComputed != 1 {
				t.Fatalf("stable native fingerprint delayed again: %+v", result.Counts)
			}
			if changed && result.Counts.HashDeferred != 1 {
				t.Fatalf("changed fingerprint trusted: %+v", result.Counts)
			}
		})
	}
}
