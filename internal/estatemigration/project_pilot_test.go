package estatemigration

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"loom.local/loom/internal/filepolicy"
	"loom.local/loom/internal/lane"
)

const digitalEstatePilotRootEnv = "LOOM_DIGITAL_ESTATE_PILOT_ROOT"

type digitalEstatePilotEvidence struct {
	SchemaVersion string                           `json:"schema_version"`
	Pilots        []digitalEstatePilotEvidenceItem `json:"pilots"`
}

type digitalEstatePilotEvidenceItem struct {
	Name                  string   `json:"name"`
	SourceInventoryDigest string   `json:"source_inventory_digest"`
	SourceDigest          string   `json:"source_digest"`
	ManifestID            string   `json:"manifest_id"`
	ManifestDigest        string   `json:"manifest_digest"`
	DestinationName       string   `json:"destination_name"`
	DestinationIdentity   string   `json:"destination_identity_digest"`
	PublicationStatus     string   `json:"publication_status"`
	RepositoryCount       int      `json:"repository_count"`
	DirtyRepositories     int      `json:"dirty_repositories"`
	UntrackedEntries      int      `json:"untracked_entries"`
	TagRefs               int      `json:"tag_refs"`
	BranchRefs            int      `json:"branch_refs"`
	Submodules            int      `json:"submodules"`
	Symlinks              int      `json:"symlinks"`
	IgnoredEntries        int      `json:"ignored_entries"`
	CompletedGates        []string `json:"completed_gates"`
	PendingGates          []string `json:"pending_gates"`
}

// TestDigitalEstateProjectPilot is the fixture bridge for the Slice 2 smoke.
// It deliberately skips during ordinary package runs. The smoke supplies two
// already-reviewed disposable source projects, and this test exercises the
// production inventory, manifest, and publication abstractions without adding
// a second migration engine or making either source writable.
func TestDigitalEstateProjectPilot(t *testing.T) {
	pilotRoot := strings.TrimSpace(os.Getenv(digitalEstatePilotRootEnv))
	if pilotRoot == "" {
		t.Skipf("%s is unset; disposable project pilot is driven by the smoke test", digitalEstatePilotRootEnv)
	}
	pilotRoot, err := filepath.Abs(pilotRoot)
	if err != nil {
		t.Fatal(err)
	}
	domainRoot := filepath.Join(pilotRoot, "runtime", "box", "Projects")
	if err := os.MkdirAll(domainRoot, 0o750); err != nil {
		t.Fatal(err)
	}

	pilots := []struct {
		name             string
		rootID           string
		source           string
		destination      string
		minimumRepos     int
		requireSubmodule bool
	}{
		{name: "single-repository", rootID: "single_project", source: filepath.Join(pilotRoot, "sources", "single-project"), destination: "single-project", minimumRepos: 1},
		{name: "multi-repository", rootID: "multi_project", source: filepath.Join(pilotRoot, "sources", "multi-project"), destination: "multi-project", minimumRepos: 3, requireSubmodule: true},
	}

	evidence := digitalEstatePilotEvidence{SchemaVersion: "loom.digital_estate_project_pilot.v1", Pilots: make([]digitalEstatePilotEvidenceItem, 0, len(pilots))}
	for _, pilot := range pilots {
		t.Run(pilot.name, func(t *testing.T) {
			item := runDigitalEstatePilot(t, domainRoot, pilot.rootID, pilot.source, pilot.destination, pilot.minimumRepos, pilot.requireSubmodule)
			evidence.Pilots = append(evidence.Pilots, item)
		})
	}
	sort.Slice(evidence.Pilots, func(left, right int) bool { return evidence.Pilots[left].Name < evidence.Pilots[right].Name })
	payload, err := json.MarshalIndent(evidence, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	payload = append(payload, '\n')
	if err := os.WriteFile(filepath.Join(pilotRoot, "publication-evidence.json"), payload, 0o600); err != nil {
		t.Fatal(err)
	}
}

func runDigitalEstatePilot(t *testing.T, domainRoot, rootID, source, destination string, minimumRepos int, requireSubmodule bool) digitalEstatePilotEvidenceItem {
	t.Helper()
	beforeInventory := filesystemSnapshot(t, source)
	inventory, err := (Service{}).Inventory(context.Background(), Config{Roots: []RootSpec{{
		ID: rootID, Path: source, Domain: DomainProjects,
		Ignore: IgnoreConfig{Profile: filepolicy.ProfileManaged, DiscoverUserRules: true},
	}}})
	if err != nil {
		t.Fatalf("inventory disposable project: %v: %v", err, errors.Unwrap(err))
	}
	if got := filesystemSnapshot(t, source); !reflect.DeepEqual(got, beforeInventory) {
		t.Fatal("inventory changed the disposable source project")
	}
	if len(inventory.Roots) != 1 || len(inventory.Repositories) < minimumRepos {
		t.Fatalf("repository inventory = %d, want at least %d", len(inventory.Repositories), minimumRepos)
	}

	root := findRoot(t, inventory, rootID)
	digests := fixtureFileDigests(t, source, root)
	manifest, err := PlanMigration(context.Background(), PlanInput{
		Inventory:          inventory,
		Selections:         []RootSelection{{RootID: rootID}},
		Destination:        DestinationSpec{Domain: DomainProjects, Name: destination, Mode: 0o750},
		FileDigests:        digests,
		RequestedTransport: lane.TransportModeAuto,
		MetadataPolicy:     MetadataPolicyStrictPortable,
		AvailableBytes:     disposableAvailableBytes,
	})
	if err != nil {
		t.Fatalf("plan disposable project migration: %v: %v", err, errors.Unwrap(err))
	}
	if _, err := os.Lstat(filepath.Join(domainRoot, destination)); !os.IsNotExist(err) {
		t.Fatalf("destination must be absent before publication: %v", err)
	}

	beforePublication := filesystemSnapshot(t, source)
	publisher := Publisher{
		Stager: StageFunc(func(ctx context.Context, request StageRequest) error {
			return stageReviewedFixture(ctx, request, map[string]string{rootID: source}, true)
		}),
		SpaceProbe: fixedSpaceProbe(disposableAvailableBytes),
	}
	result, err := publisher.Publish(context.Background(), manifest, PublicationInput{
		DomainRoot: domainRoot, AttemptID: rootID + "-attempt", ReviewedManifestDigest: manifest.ManifestDigest, Confirmed: true,
	})
	if err != nil {
		t.Fatalf("publish disposable project: %v: %v", err, errors.Unwrap(err))
	}
	if result.Status != publicationStatePublished || result.Acceptance == nil {
		t.Fatalf("publication result = %#v", result)
	}
	if result.Acceptance.CanonicalCustodyAccepted || result.Acceptance.ORCARegistrationAuthorized || result.Acceptance.SourceDeletionAuthorized {
		t.Fatalf("publisher overclaimed downstream acceptance: %#v", result.Acceptance)
	}
	if got := filesystemSnapshot(t, source); !reflect.DeepEqual(got, beforePublication) {
		t.Fatal("publication changed the disposable source project")
	}
	repeated, err := (Service{}).Inventory(context.Background(), Config{Roots: []RootSpec{{
		ID: rootID, Path: source, Domain: DomainProjects,
		Ignore: IgnoreConfig{Profile: filepolicy.ProfileManaged, DiscoverUserRules: true},
	}}})
	if err != nil {
		t.Fatalf("repeat source inventory: %v", err)
	}
	if repeated.Digest != inventory.Digest {
		t.Fatalf("source inventory drifted: %s != %s", repeated.Digest, inventory.Digest)
	}

	item := digitalEstatePilotEvidenceItem{
		Name: pilotEvidenceName(destination), SourceInventoryDigest: inventory.Digest, SourceDigest: manifest.SourceDigest,
		ManifestID: manifest.ManifestID, ManifestDigest: manifest.ManifestDigest,
		DestinationName: destination, DestinationIdentity: manifest.Destination.IdentityDigest,
		PublicationStatus: result.Status, RepositoryCount: len(inventory.Repositories),
		CompletedGates: append([]string(nil), result.Acceptance.CompletedGates...),
		PendingGates:   append([]string(nil), result.Acceptance.PendingGates...),
	}
	for _, entry := range root.Entries {
		if !entry.Ignore.Included {
			item.IgnoredEntries++
		}
		if entry.Kind == "symlink" {
			item.Symlinks++
		}
	}
	for _, repository := range inventory.Repositories {
		if repository.Dirty {
			item.DirtyRepositories++
		}
		item.UntrackedEntries += len(repository.Untracked)
		item.Submodules += len(repository.Submodules)
		for _, ref := range repository.Refs {
			switch {
			case strings.HasPrefix(ref.Name, "refs/tags/"):
				item.TagRefs++
			case strings.HasPrefix(ref.Name, "refs/heads/"):
				item.BranchRefs++
			}
		}
	}
	if item.DirtyRepositories == 0 || item.UntrackedEntries == 0 || item.TagRefs == 0 || item.BranchRefs < 2 || item.Symlinks == 0 || item.IgnoredEntries == 0 {
		t.Fatalf("pilot coverage is incomplete: %#v", item)
	}
	if requireSubmodule && item.Submodules == 0 {
		t.Fatalf("multi-repository pilot omitted submodule evidence: %#v", item)
	}
	for _, gate := range []string{"entry_fidelity", "staged_content_digest"} {
		if !containsString(item.CompletedGates, gate) {
			t.Fatalf("publisher did not complete gate %q: %#v", gate, item.CompletedGates)
		}
	}
	for _, gate := range []string{"backup_fetch_restore_before_canonical", "loom_registration_after_verification", "orca_registration_after_loom_acceptance", "source_retained", "source_stability"} {
		if !containsString(item.PendingGates, gate) {
			t.Fatalf("publisher did not retain gate %q for the orchestration smoke: %#v", gate, item.PendingGates)
		}
	}
	return item
}

func pilotEvidenceName(destination string) string {
	if destination == "single-project" {
		return "single-repository"
	}
	return "multi-repository"
}
