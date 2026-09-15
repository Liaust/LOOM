package lane

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestBundleManifestRejectsSchemaIdentityAndInventoryMismatches(t *testing.T) {
	root, lanePath, plan, createdAt := bundleFixture(t)
	artifact, err := CreateBundle(CreateBundleInput{
		LanePath: lanePath, PolicyRoot: root, ArtifactRoot: filepath.Join(root, DefaultStateRelPath, "bundles"),
		BatchID: "lane_manifest_validation", SourceNodeKey: "macbook", Plan: plan, CreatedAt: createdAt,
		AvailableBytes: maxBundleTestSpace,
	})
	if err != nil {
		t.Fatal(err)
	}
	payload, err := os.ReadFile(artifact.ManifestPath)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		mutate func(*BundleManifest)
	}{
		{name: "schema", mutate: func(value *BundleManifest) { value.SchemaVersion = "future" }},
		{name: "batch", mutate: func(value *BundleManifest) { value.BatchID = "../bad" }},
		{name: "mode", mutate: func(value *BundleManifest) { value.TransferMode = TransportModeFileTree }},
		{name: "fingerprint", mutate: func(value *BundleManifest) { value.PolicyFingerprint = "changed" }},
		{name: "inventory", mutate: func(value *BundleManifest) { value.InventoryHash = "sha256:changed" }},
		{name: "after fingerprint", mutate: func(value *BundleManifest) { value.SourceFingerprintAfter = "sha256:changed" }},
		{name: "summary", mutate: func(value *BundleManifest) { value.TotalSourceFiles++ }},
		{name: "archive count", mutate: func(value *BundleManifest) { value.ArchiveParts = nil }},
		{name: "archive path", mutate: func(value *BundleManifest) { value.ArchiveParts[0].Name = "../bundle.tar" }},
		{name: "entry kind", mutate: func(value *BundleManifest) { value.Plan.Entries[0].Kind = "symlink" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var manifest BundleManifest
			if err := json.Unmarshal(payload, &manifest); err != nil {
				t.Fatal(err)
			}
			test.mutate(&manifest)
			if err := validateBundleManifest(manifest); err == nil {
				t.Fatalf("mutated manifest passed validation: %#v", manifest)
			}
		})
	}
}

func maxBundleTestSpace(string) (int64, error) {
	return int64(^uint64(0) >> 1), nil
}
