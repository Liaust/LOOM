package backupstrategy

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"loom.local/loom/internal/boxregistry"
)

func recoveryCatalogFixture() boxregistry.Catalog {
	return boxregistry.Catalog{SchemaVersion: boxregistry.SchemaVersion, DefaultBox: "personal", Boxes: []boxregistry.Box{
		{ID: "box_personal", Key: "personal", Node: "main", RootPath: "/srv/loom/box", RootRef: "box", StateRoot: "/var/lib/loom/box-state", Primary: true, Available: true},
		{ID: "box_company", Key: "company", Node: "main", RootPath: "/srv/loom/boxes/company", RootRef: "box:box_company", StateRoot: "/var/lib/loom/boxes/box_company", ArchiveRoot: "/srv/loom/storage/Boxes/company", Available: true},
	}}
}

func TestBoxRecoveryContextRoundTripAndLegacyArtifact(t *testing.T) {
	catalog := recoveryCatalogFixture()
	raw, err := WithBoxRecoveryContext([]byte(`{"state_dir":"/var/lib/loom/update","history":[]}`), catalog)
	if err != nil {
		t.Fatal(err)
	}
	got, err := ReadBoxRecoveryContext(raw)
	if err != nil || !reflect.DeepEqual(got, &catalog) || !strings.Contains(string(raw), `"history":[]`) {
		t.Fatalf("context did not preserve catalog contents and update state: got=%+v err=%v", got, err)
	}
	if err := validateStateArtifact(OperationalArtifactUpdate, raw, 1); err != nil {
		t.Fatal(err)
	}
	got, err = ReadBoxRecoveryContext([]byte(`{"history":[]}`))
	if err != nil || got != nil {
		t.Fatalf("historical artifact rejected: %v %v", got, err)
	}
	// Missing roots/descriptors are recoverable configuration, not invented IDs.
	catalog.Boxes[1].ID, catalog.Boxes[1].RootRef, catalog.Boxes[1].Available = "", "", false
	if _, err := WithBoxRecoveryContext([]byte(`{"history":[]}`), catalog); err != nil {
		t.Fatal(err)
	}
}

func TestBoxRecoveryContextRejectsInvalidIdentityAndUnknownContent(t *testing.T) {
	for _, mutate := range []func(*boxregistry.Catalog){
		func(c *boxregistry.Catalog) { c.Boxes[1].RootRef = "box" },
		func(c *boxregistry.Catalog) { c.Boxes[1].ID = c.Boxes[0].ID },
		func(c *boxregistry.Catalog) { c.Boxes[1].Key = "personal" },
		func(c *boxregistry.Catalog) { c.Boxes[0].Primary = false },
	} {
		catalog := recoveryCatalogFixture()
		mutate(&catalog)
		if _, err := WithBoxRecoveryContext([]byte(`{"history":[]}`), catalog); err == nil {
			t.Fatal("invalid context accepted")
		}
	}
	raw, _ := json.Marshal(recoveryCatalogFixture())
	raw = []byte(strings.Replace(string(raw), `"default_box"`, `"credential_value":"must-not-be-accepted","default_box"`, 1))
	if _, err := ReadBoxRecoveryContext(append(append([]byte(`{"box_context":`), raw...), '}')); err == nil {
		t.Fatal("unknown configuration content accepted")
	}
}

func TestBoxRecoveryContextRetainsFixedOperationalLayout(t *testing.T) {
	fixture := newOperationalFixture(t)
	raw, err := WithBoxRecoveryContext([]byte(`{"status":"idle"}`), recoveryCatalogFixture())
	if err != nil {
		t.Fatal(err)
	}
	mustWriteOperational(t, fixture.updateState, raw)
	// Existing fixture writes tiny synthetic artifacts; no pg_dump or real backup.
	result := createOperationalFixture(t, fixture, "operational-box-context", time.Now().UTC(), nil)
	if result.Verification.ArtifactCount != 7 || result.Verification.Status != "succeeded" {
		t.Fatalf("v1 layout or verification changed: %+v", result.Verification)
	}
	stored, err := os.ReadFile(filepath.Join(result.PackageDir, "update-state.json"))
	if err != nil {
		t.Fatal(err)
	}
	want := recoveryCatalogFixture()
	if catalog, err := ReadBoxRecoveryContext(stored); err != nil || !reflect.DeepEqual(catalog, &want) {
		t.Fatalf("recoverable contents lost: %+v %v", catalog, err)
	}
	verification, err := VerifyOperationalPackage(context.Background(), OperationalPackageVerificationInput{PackageDir: result.PackageDir, ExpectedManifestSHA256: result.ManifestSHA256})
	if err != nil || verification.Status != "succeeded" {
		t.Fatalf("authenticated package reader rejected context: %+v %v", verification, err)
	}
}

func TestAdditionalBoxArchiveRootsStableAndNoPersonalDuplication(t *testing.T) {
	catalog := recoveryCatalogFixture()
	roots, err := AdditionalBoxArchiveRoots(catalog)
	if err != nil || len(roots) != 3 {
		t.Fatalf("roots=%+v err=%v", roots, err)
	}
	if roots[0].Name != "box_company_source" || roots[1].Name != "box_company_state" || roots[2].Name != "box_company_archive" {
		t.Fatalf("unstable names: %+v", roots)
	}
	catalog.Boxes[1].Key, catalog.Boxes[1].Label = "renamed", "New label"
	renamed, err := AdditionalBoxArchiveRoots(catalog)
	if err != nil || !reflect.DeepEqual(roots, renamed) {
		t.Fatal("rename changed capture identity")
	}
	catalog.Boxes[1].Available = false
	if roots, err := AdditionalBoxArchiveRoots(catalog); err != nil || len(roots) != 0 {
		t.Fatalf("unavailable Box selected: %+v %v", roots, err)
	}
	for _, name := range []string{"box_company_source", "box_company_state", "box_company_archive"} {
		if !DirectArchiveRootAllowsEmpty(name) {
			t.Fatalf("empty registered namespace rejected: %s", name)
		}
	}
	for _, name := range []string{"box_notes", "box_projects", "box_company_unknown", "box_../invalid_source"} {
		if DirectArchiveRootAllowsEmpty(name) {
			t.Fatalf("legacy or malformed policy widened: %s", name)
		}
	}
}
