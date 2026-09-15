package estatemigration

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync/atomic"
	"testing"

	"golang.org/x/sys/unix"
	"loom.local/loom/internal/filepolicy"
	"loom.local/loom/internal/filesystemmeta"
	"loom.local/loom/internal/lane"
)

const disposableAvailableBytes = uint64(2 * 1024 * 1024 * 1024)

type reviewedFixture struct {
	source    string
	inventory Inventory
	input     PlanInput
	manifest  MigrationManifest
}

func TestPlanMigrationIsDeterministicRedactedAndAccountsForIgnoredTree(t *testing.T) {
	t.Parallel()
	fixture := newReviewedFixture(t, false)

	reversedDigests := map[string]string{}
	keys := make([]string, 0, len(fixture.input.FileDigests))
	for key := range fixture.input.FileDigests {
		keys = append(keys, key)
	}
	sort.Sort(sort.Reverse(sort.StringSlice(keys)))
	for _, key := range keys {
		reversedDigests[key] = fixture.input.FileDigests[key]
	}
	repeatedInput := fixture.input
	repeatedInput.FileDigests = reversedDigests
	repeated, err := PlanMigration(context.Background(), repeatedInput)
	if err != nil {
		t.Fatalf("repeat plan: %v", err)
	}
	firstBytes, err := MarshalManifest(fixture.manifest)
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	repeatedBytes, err := MarshalManifest(repeated)
	if err != nil {
		t.Fatalf("marshal repeated manifest: %v", err)
	}
	if !bytes.Equal(firstBytes, repeatedBytes) || !reflect.DeepEqual(fixture.manifest, repeated) {
		t.Fatalf("manifest changed with map insertion order")
	}
	if fixture.manifest.ManifestDigest == fixture.inventory.Digest || fixture.manifest.SourceDigest == fixture.inventory.Digest {
		t.Fatalf("manifest confused inventory, source, and plan identities")
	}
	if fixture.manifest.Transport.SelectedMode != lane.TransportModeFileTree || fixture.manifest.Transport.SchemaVersion != lane.BundlePlanSchemaVersion {
		t.Fatalf("Lane transport selector was not preserved: %#v", fixture.manifest.Transport)
	}
	bundleInput := fixture.input
	bundleInput.RequestedTransport = lane.TransportModeBundleSeed
	bundle, err := PlanMigration(context.Background(), bundleInput)
	if err != nil {
		t.Fatalf("plan forced bundle transport: %v", err)
	}
	if bundle.Transport.SelectedMode != lane.TransportModeBundleSeed || bundle.Space.TransportTemporaryBytes != uint64(bundle.Transport.EstimatedTemporaryBytes) || bundle.Space.RequiredBytes != bundle.Space.PayloadBytes+bundle.Space.TransportTemporaryBytes {
		t.Fatalf("bundle space contract=%#v transport=%#v", bundle.Space, bundle.Transport)
	}
	if len(fixture.manifest.Sources) != 1 {
		t.Fatalf("sources=%#v", fixture.manifest.Sources)
	}
	source := fixture.manifest.Sources[0]
	if source.InventorySummary.IgnoredCount == 0 || source.InventorySummary.EntryCount <= source.SelectedEntryCount || source.InventorySummary.Accounting.LogicalBytes <= source.SelectedLogicalBytes {
		t.Fatalf("complete ignored-tree accounting was not retained: %#v", source)
	}
	for _, entry := range fixture.manifest.Entries {
		if !entry.Ignore.Included && !entry.StructuralOnly {
			t.Fatalf("ignored payload entered manifest: %#v", entry)
		}
		if entry.Kind == filesystemmeta.ObjectKindRegularFile && !validDigest(entry.ContentDigest) {
			t.Fatalf("regular file lacks exact digest: %#v", entry)
		}
	}
	for _, forbidden := range []string{fixture.source, filepath.Dir(fixture.source), "ignored secret payload"} {
		if bytes.Contains(firstBytes, []byte(forbidden)) {
			t.Fatalf("manifest leaked source data %q", forbidden)
		}
	}
	parsed, err := UnmarshalManifest(firstBytes)
	if err != nil {
		t.Fatalf("unmarshal byte-stable manifest: %v", err)
	}
	parsedBytes, err := MarshalManifest(parsed)
	if err != nil || !bytes.Equal(firstBytes, parsedBytes) {
		t.Fatalf("manifest round trip changed bytes: err=%v", err)
	}
	if _, err := UnmarshalManifest(append(firstBytes, []byte(`{"extra":true}`)...)); !errors.Is(err, ErrInvalidManifest) {
		t.Fatalf("trailing JSON error=%v", err)
	}
	invalid := fixture.manifest
	invalid.Destination.Name = "invalid-" + string([]byte{0xff})
	if _, err := MarshalManifest(invalid); !errors.Is(err, ErrInvalidManifest) {
		t.Fatalf("invalid UTF-8 manifest error=%v", err)
	}
}

func TestPlanMigrationFailsClosedOnIncompleteUnsafeOrUnboundedInputs(t *testing.T) {
	t.Parallel()
	fixture := newReviewedFixture(t, false)

	missing := fixture.input
	missing.FileDigests = cloneStringMap(fixture.input.FileDigests)
	for locator := range missing.FileDigests {
		delete(missing.FileDigests, locator)
		break
	}
	if _, err := PlanMigration(context.Background(), missing); !errors.Is(err, ErrInvalidManifest) {
		t.Fatalf("missing digest error=%v", err)
	}

	extra := fixture.input
	extra.FileDigests = cloneStringMap(fixture.input.FileDigests)
	extra.FileDigests["root://documents/ignored/cache.txt"] = digestBytes([]byte("ignored secret payload"))
	if _, err := PlanMigration(context.Background(), extra); !errors.Is(err, ErrInvalidManifest) {
		t.Fatalf("ignored digest error=%v", err)
	}

	lowSpace := fixture.input
	lowSpace.AvailableBytes = 1
	if _, err := PlanMigration(context.Background(), lowSpace); !errors.Is(err, ErrInsufficientSpace) {
		t.Fatalf("space error=%v", err)
	}

	tampered := fixture.input
	tampered.Inventory.Digest = digestBytes([]byte("changed inventory"))
	if _, err := PlanMigration(context.Background(), tampered); !errors.Is(err, ErrInvalidManifest) {
		t.Fatalf("tampered inventory error=%v", err)
	}

	overlapParent := t.TempDir()
	overlapChild := filepath.Join(overlapParent, "child")
	mustMkdirAll(t, overlapChild)
	mustWriteFile(t, filepath.Join(overlapChild, "payload.txt"), "payload")
	overlapInventory, err := (Service{}).Inventory(context.Background(), Config{Roots: []RootSpec{
		{ID: "parent", Path: overlapParent, Domain: DomainDocuments},
		{ID: "child", Path: overlapChild, Domain: DomainDocuments},
	}})
	if err != nil {
		t.Fatalf("inventory overlap fixture: %v", err)
	}
	if _, err := PlanMigration(context.Background(), PlanInput{
		Inventory:      overlapInventory,
		Selections:     []RootSelection{{RootID: "parent", DestinationPrefix: "parent"}, {RootID: "child", DestinationPrefix: "child"}},
		Destination:    DestinationSpec{Domain: DomainDocuments, Name: "overlap", Mode: 0o750},
		AvailableBytes: disposableAvailableBytes,
	}); !errors.Is(err, ErrOverlappingSources) {
		t.Fatalf("overlap error=%v", err)
	}

	unsafeRoot := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.txt")
	mustWriteFile(t, outside, "outside")
	if err := os.Symlink(outside, filepath.Join(unsafeRoot, "escape")); err != nil {
		t.Fatalf("create external symlink: %v", err)
	}
	unsafeInventory, err := (Service{}).Inventory(context.Background(), Config{Roots: []RootSpec{{ID: "unsafe", Path: unsafeRoot, Domain: DomainDocuments}}})
	if err != nil {
		t.Fatalf("inventory unsafe fixture: %v", err)
	}
	if _, err := PlanMigration(context.Background(), PlanInput{
		Inventory: unsafeInventory, Selections: []RootSelection{{RootID: "unsafe"}},
		Destination: DestinationSpec{Domain: DomainDocuments, Name: "unsafe-target", Mode: 0o750}, AvailableBytes: disposableAvailableBytes,
	}); !errors.Is(err, ErrUnsupportedEntry) {
		t.Fatalf("external symlink error=%v", err)
	}

	ignoredTargetRoot := t.TempDir()
	mustWriteFile(t, filepath.Join(ignoredTargetRoot, ".loomignore"), "ignored.txt\n")
	mustWriteFile(t, filepath.Join(ignoredTargetRoot, "ignored.txt"), "ignored target")
	if err := os.Symlink("ignored.txt", filepath.Join(ignoredTargetRoot, "included-link")); err != nil {
		t.Fatalf("create ignored-target symlink: %v", err)
	}
	ignoredTargetInventory, err := (Service{}).Inventory(context.Background(), Config{Roots: []RootSpec{{
		ID: "ignored_target", Path: ignoredTargetRoot, Domain: DomainDocuments,
		Ignore: IgnoreConfig{Profile: filepolicy.ProfileManaged, DiscoverUserRules: true},
	}}})
	if err != nil {
		t.Fatalf("inventory ignored-target fixture: %v", err)
	}
	ignoredTargetDigests := fixtureFileDigests(t, ignoredTargetRoot, findRoot(t, ignoredTargetInventory, "ignored_target"))
	if _, err := PlanMigration(context.Background(), PlanInput{
		Inventory: ignoredTargetInventory, Selections: []RootSelection{{RootID: "ignored_target"}},
		Destination: DestinationSpec{Domain: DomainDocuments, Name: "ignored-target", Mode: 0o750},
		FileDigests: ignoredTargetDigests, AvailableBytes: disposableAvailableBytes,
	}); !errors.Is(err, ErrUnsupportedEntry) {
		t.Fatalf("ignored symlink target error=%v", err)
	}

	specialRoot := t.TempDir()
	if err := unix.Mkfifo(filepath.Join(specialRoot, "pipe"), 0o600); err != nil {
		t.Fatalf("create fifo: %v", err)
	}
	specialInventory, err := (Service{}).Inventory(context.Background(), Config{Roots: []RootSpec{{ID: "special", Path: specialRoot, Domain: DomainDocuments}}})
	if err != nil {
		t.Fatalf("inventory special fixture: %v", err)
	}
	if _, err := PlanMigration(context.Background(), PlanInput{
		Inventory: specialInventory, Selections: []RootSelection{{RootID: "special"}},
		Destination: DestinationSpec{Domain: DomainDocuments, Name: "special-target", Mode: 0o750}, AvailableBytes: disposableAvailableBytes,
	}); !errors.Is(err, ErrUnsupportedEntry) {
		t.Fatalf("special entry error=%v", err)
	}
}

func TestPublisherStagesVerifiesPublishesAndReplaysIdenticalManifest(t *testing.T) {
	t.Parallel()
	fixture := newReviewedFixture(t, true)
	domainRoot := t.TempDir()
	target := filepath.Join(domainRoot, fixture.manifest.Destination.Name)
	assertAbsent(t, target)
	beforeSource := filesystemSnapshot(t, fixture.source)
	var calls atomic.Int32
	publisher := Publisher{
		Stager: StageFunc(func(ctx context.Context, request StageRequest) error {
			calls.Add(1)
			return stageReviewedFixture(ctx, request, map[string]string{"documents": fixture.source}, true)
		}),
		SpaceProbe: fixedSpaceProbe(disposableAvailableBytes),
	}
	result, err := publisher.Publish(context.Background(), fixture.manifest, PublicationInput{
		DomainRoot: domainRoot, AttemptID: "first-attempt", ReviewedManifestDigest: fixture.manifest.ManifestDigest, Confirmed: true,
	})
	if err != nil {
		t.Fatalf("publish disposable target: %v: %v", err, errors.Unwrap(err))
	}
	if result.Status != publicationStatePublished || result.Idempotent || result.Recovered || result.Acceptance == nil {
		t.Fatalf("unexpected publication result: %#v", result)
	}
	if result.Acceptance.CanonicalCustodyAccepted || result.Acceptance.ORCARegistrationAuthorized || result.Acceptance.SourceDeletionAuthorized {
		t.Fatalf("staged publication overclaimed acceptance: %#v", result.Acceptance)
	}
	for _, pending := range []string{"backup_fetch_restore_before_canonical", "canonical_ownership_permissions", "loom_registration_after_verification", "orca_registration_after_loom_acceptance", "source_retained", "source_stability"} {
		if !containsString(result.Acceptance.PendingGates, pending) {
			t.Fatalf("pending acceptance gate %q missing: %#v", pending, result.Acceptance.PendingGates)
		}
	}
	if got := filesystemSnapshot(t, fixture.source); !reflect.DeepEqual(got, beforeSource) {
		t.Fatalf("source changed during staged publication\nbefore=%v\nafter=%v", beforeSource, got)
	}
	if calls.Load() != 1 {
		t.Fatalf("stage calls=%d", calls.Load())
	}
	if _, err := os.Stat(filepath.Join(target, "ignored", "cache.txt")); !os.IsNotExist(err) {
		t.Fatalf("ignored payload was published: %v", err)
	}
	if data, err := os.ReadFile(filepath.Join(target, "kept.txt")); err != nil || string(data) != "kept payload" {
		t.Fatalf("published payload=%q err=%v", data, err)
	}
	link, err := os.Readlink(filepath.Join(target, "kept-link"))
	if err != nil || link != "kept.txt" {
		t.Fatalf("published link=%q err=%v", link, err)
	}
	encodedResult, err := jsonMarshal(result)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	for _, forbidden := range []string{domainRoot, fixture.source} {
		if bytes.Contains(encodedResult, []byte(forbidden)) {
			t.Fatalf("publication result leaked %q", forbidden)
		}
	}

	replay := Publisher{
		Stager: StageFunc(func(context.Context, StageRequest) error {
			return errors.New("stager must not run for an identical replay")
		}),
		SpaceProbe: fixedSpaceProbe(0),
	}
	replayed, err := replay.Publish(context.Background(), fixture.manifest, PublicationInput{
		DomainRoot: domainRoot, AttemptID: "second-attempt", ReviewedManifestDigest: fixture.manifest.ManifestDigest, Confirmed: true,
	})
	if err != nil || !replayed.Idempotent || replayed.Status != publicationStatePublished {
		t.Fatalf("identical replay result=%#v err=%v", replayed, err)
	}

	changedInput := fixture.input
	changedInput.RequestedTransport = lane.TransportModeFileTree
	changed, err := PlanMigration(context.Background(), changedInput)
	if err != nil {
		t.Fatalf("plan changed replay: %v", err)
	}
	if changed.ManifestDigest == fixture.manifest.ManifestDigest {
		t.Fatal("changed replay did not change manifest identity")
	}
	if _, err := replay.Publish(context.Background(), changed, PublicationInput{
		DomainRoot: domainRoot, AttemptID: "changed-attempt", ReviewedManifestDigest: changed.ManifestDigest, Confirmed: true,
	}); !errors.Is(err, ErrChangedReplay) {
		t.Fatalf("changed replay error=%v", err)
	}
}

func TestPublisherRejectsDestinationRaceAndNoFollowViolations(t *testing.T) {
	t.Parallel()
	fixture := newReviewedFixture(t, true)

	t.Run("destination race preserves intruder", func(t *testing.T) {
		domainRoot := t.TempDir()
		target := filepath.Join(domainRoot, fixture.manifest.Destination.Name)
		assertAbsent(t, target)
		publisher := Publisher{
			Stager: StageFunc(func(ctx context.Context, request StageRequest) error {
				return stageReviewedFixture(ctx, request, map[string]string{"documents": fixture.source}, true)
			}),
			SpaceProbe: fixedSpaceProbe(disposableAvailableBytes),
			hooks: &publicationTestHooks{afterPreparedRecord: func() error {
				if err := os.Mkdir(target, 0o700); err != nil {
					return err
				}
				return os.WriteFile(filepath.Join(target, "intruder.txt"), []byte("do not overwrite"), 0o600)
			}},
		}
		if _, err := publisher.Publish(context.Background(), fixture.manifest, PublicationInput{
			DomainRoot: domainRoot, AttemptID: "race-attempt", ReviewedManifestDigest: fixture.manifest.ManifestDigest, Confirmed: true,
		}); !errors.Is(err, ErrDestinationExists) {
			t.Fatalf("destination race error=%v", err)
		}
		payload, err := os.ReadFile(filepath.Join(target, "intruder.txt"))
		if err != nil || string(payload) != "do not overwrite" {
			t.Fatalf("racing destination was overwritten: %q err=%v", payload, err)
		}
	})

	t.Run("escaping symlink is never followed", func(t *testing.T) {
		domainRoot := t.TempDir()
		target := filepath.Join(domainRoot, fixture.manifest.Destination.Name)
		assertAbsent(t, target)
		outside := filepath.Join(t.TempDir(), "outside.txt")
		mustWriteFile(t, outside, "protected")
		publisher := Publisher{
			Stager: StageFunc(func(ctx context.Context, request StageRequest) error {
				if err := stageReviewedFixture(ctx, request, map[string]string{"documents": fixture.source}, true); err != nil {
					return err
				}
				if err := os.Remove(filepath.Join(request.StagingPath, "kept-link")); err != nil {
					return err
				}
				return os.Symlink("../../outside.txt", filepath.Join(request.StagingPath, "kept-link"))
			}),
			SpaceProbe: fixedSpaceProbe(disposableAvailableBytes),
		}
		if _, err := publisher.Publish(context.Background(), fixture.manifest, PublicationInput{
			DomainRoot: domainRoot, AttemptID: "escape-attempt", ReviewedManifestDigest: fixture.manifest.ManifestDigest, Confirmed: true,
		}); !errors.Is(err, ErrVerificationFailed) {
			t.Fatalf("escaping symlink error=%v", err)
		}
		if payload, err := os.ReadFile(outside); err != nil || string(payload) != "protected" {
			t.Fatalf("outside file changed: %q err=%v", payload, err)
		}
		assertAbsent(t, target)
	})

	t.Run("unexpected symlink metadata is rejected without following", func(t *testing.T) {
		probeRoot := t.TempDir()
		probeLink := filepath.Join(probeRoot, "probe-link")
		if err := os.Symlink("missing", probeLink); err != nil {
			t.Fatalf("create xattr probe symlink: %v", err)
		}
		if err := unix.Lsetxattr(probeLink, "com.example.loom-test", []byte("unexpected"), 0); err != nil {
			t.Skipf("symlink xattrs unavailable on this disposable filesystem: %v", err)
		}

		domainRoot := t.TempDir()
		target := filepath.Join(domainRoot, fixture.manifest.Destination.Name)
		publisher := Publisher{
			Stager: StageFunc(func(ctx context.Context, request StageRequest) error {
				if err := stageReviewedFixture(ctx, request, map[string]string{"documents": fixture.source}, true); err != nil {
					return err
				}
				return unix.Lsetxattr(filepath.Join(request.StagingPath, "kept-link"), "com.example.loom-test", []byte("unexpected"), 0)
			}),
			SpaceProbe: fixedSpaceProbe(disposableAvailableBytes),
		}
		if _, err := publisher.Publish(context.Background(), fixture.manifest, PublicationInput{
			DomainRoot: domainRoot, AttemptID: "symlink-xattr-attempt", ReviewedManifestDigest: fixture.manifest.ManifestDigest, Confirmed: true,
		}); !errors.Is(err, ErrVerificationFailed) {
			t.Fatalf("symlink xattr error=%v", err)
		}
		assertAbsent(t, target)
	})

	t.Run("stage mutation after prepared record is detected", func(t *testing.T) {
		domainRoot := t.TempDir()
		target := filepath.Join(domainRoot, fixture.manifest.Destination.Name)
		assertAbsent(t, target)
		var stagedPath string
		publisher := Publisher{
			Stager: StageFunc(func(ctx context.Context, request StageRequest) error {
				stagedPath = request.StagingPath
				return stageReviewedFixture(ctx, request, map[string]string{"documents": fixture.source}, true)
			}),
			SpaceProbe: fixedSpaceProbe(disposableAvailableBytes),
			hooks: &publicationTestHooks{afterPreparedRecord: func() error {
				return os.WriteFile(filepath.Join(stagedPath, "kept.txt"), []byte("evil payload"), 0o640)
			}},
		}
		if _, err := publisher.Publish(context.Background(), fixture.manifest, PublicationInput{
			DomainRoot: domainRoot, AttemptID: "mutation-attempt", ReviewedManifestDigest: fixture.manifest.ManifestDigest, Confirmed: true,
		}); !errors.Is(err, ErrVerificationFailed) {
			t.Fatalf("stage mutation error=%v", err)
		}
		assertAbsent(t, target)
	})
}

func TestPublisherRejectsPublicationNamespaceReplacementAndRecoversTruthfully(t *testing.T) {
	t.Parallel()
	fixture := newReviewedFixture(t, false)

	t.Run("intermediate destination ancestor replaced after prepared record", func(t *testing.T) {
		workspace := t.TempDir()
		ancestor := filepath.Join(workspace, "canonical-parent")
		domainRoot := filepath.Join(ancestor, "domain")
		mustMkdirAll(t, domainRoot)
		detachedAncestor := filepath.Join(workspace, "detached-canonical-parent")
		replacementAside := filepath.Join(workspace, "replacement-canonical-parent")
		replacementMarker := filepath.Join(domainRoot, "replacement-marker.txt")
		target := filepath.Join(domainRoot, fixture.manifest.Destination.Name)
		detachedTarget := filepath.Join(detachedAncestor, "domain", fixture.manifest.Destination.Name)

		publisher := Publisher{
			Stager: StageFunc(func(ctx context.Context, request StageRequest) error {
				return stageReviewedFixture(ctx, request, map[string]string{"documents": fixture.source}, true)
			}),
			SpaceProbe: fixedSpaceProbe(disposableAvailableBytes),
			hooks: &publicationTestHooks{afterPreparedRecord: func() error {
				if err := os.Rename(ancestor, detachedAncestor); err != nil {
					return err
				}
				if err := os.MkdirAll(domainRoot, 0o755); err != nil {
					return err
				}
				return os.WriteFile(replacementMarker, []byte("replacement remains untouched"), 0o600)
			}},
		}
		result, err := publisher.Publish(context.Background(), fixture.manifest, PublicationInput{
			DomainRoot: domainRoot, AttemptID: "ancestor-replacement", ReviewedManifestDigest: fixture.manifest.ManifestDigest, Confirmed: true,
		})
		if !errors.Is(err, ErrPublicationUncertain) || result.Status == publicationStatePublished {
			t.Fatalf("ancestor replacement result=%#v err=%v", result, err)
		}
		assertAbsent(t, target)
		assertAbsent(t, detachedTarget)
		if payload, readErr := os.ReadFile(replacementMarker); readErr != nil || string(payload) != "replacement remains untouched" {
			t.Fatalf("replacement marker changed: %q err=%v", payload, readErr)
		}

		if err := os.Rename(ancestor, replacementAside); err != nil {
			t.Fatalf("retain replacement ancestor: %v", err)
		}
		if err := os.Rename(detachedAncestor, ancestor); err != nil {
			t.Fatalf("restore bound ancestor: %v", err)
		}
		recovered, err := (Publisher{}).Publish(context.Background(), fixture.manifest, PublicationInput{
			DomainRoot: domainRoot, AttemptID: "ancestor-recovery", ReviewedManifestDigest: fixture.manifest.ManifestDigest, Confirmed: true,
		})
		if err != nil || !recovered.Idempotent || !recovered.Recovered || recovered.Status != publicationStatePublished || recovered.Acceptance == nil || recovered.Acceptance.State != publicationStatePublished {
			t.Fatalf("ancestor recovery result=%#v err=%v", recovered, err)
		}
		if payload, readErr := os.ReadFile(filepath.Join(replacementAside, "domain", "replacement-marker.txt")); readErr != nil || string(payload) != "replacement remains untouched" {
			t.Fatalf("retained replacement changed during replay: %q err=%v", payload, readErr)
		}
	})

	t.Run("publication control replaced after prepared record", func(t *testing.T) {
		domainRoot := t.TempDir()
		controlPath := filepath.Join(domainRoot, publicationControlDirectory)
		detachedControl := filepath.Join(domainRoot, ".loom-migrations-detached")
		replacementAside := filepath.Join(domainRoot, ".loom-migrations-replacement")
		replacementMarker := filepath.Join(controlPath, "replacement-marker.txt")
		target := filepath.Join(domainRoot, fixture.manifest.Destination.Name)

		publisher := Publisher{
			Stager: StageFunc(func(ctx context.Context, request StageRequest) error {
				return stageReviewedFixture(ctx, request, map[string]string{"documents": fixture.source}, true)
			}),
			SpaceProbe: fixedSpaceProbe(disposableAvailableBytes),
			hooks: &publicationTestHooks{afterPreparedRecord: func() error {
				if err := os.Rename(controlPath, detachedControl); err != nil {
					return err
				}
				if err := os.Mkdir(controlPath, 0o700); err != nil {
					return err
				}
				return os.WriteFile(replacementMarker, []byte("replacement remains untouched"), 0o600)
			}},
		}
		result, err := publisher.Publish(context.Background(), fixture.manifest, PublicationInput{
			DomainRoot: domainRoot, AttemptID: "control-replacement", ReviewedManifestDigest: fixture.manifest.ManifestDigest, Confirmed: true,
		})
		if !errors.Is(err, ErrPublicationUncertain) || result.Status == publicationStatePublished {
			t.Fatalf("control replacement result=%#v err=%v", result, err)
		}
		assertAbsent(t, target)
		assertAbsent(t, filepath.Join(detachedControl, fixture.manifest.Destination.Name))
		if payload, readErr := os.ReadFile(replacementMarker); readErr != nil || string(payload) != "replacement remains untouched" {
			t.Fatalf("replacement marker changed: %q err=%v", payload, readErr)
		}

		if err := os.Rename(controlPath, replacementAside); err != nil {
			t.Fatalf("retain replacement control: %v", err)
		}
		if err := os.Rename(detachedControl, controlPath); err != nil {
			t.Fatalf("restore bound control: %v", err)
		}
		recovered, err := (Publisher{}).Publish(context.Background(), fixture.manifest, PublicationInput{
			DomainRoot: domainRoot, AttemptID: "control-recovery", ReviewedManifestDigest: fixture.manifest.ManifestDigest, Confirmed: true,
		})
		if err != nil || !recovered.Idempotent || !recovered.Recovered || recovered.Status != publicationStatePublished || recovered.Acceptance == nil || recovered.Acceptance.State != publicationStatePublished {
			t.Fatalf("control recovery result=%#v err=%v", recovered, err)
		}
		if payload, readErr := os.ReadFile(filepath.Join(replacementAside, "replacement-marker.txt")); readErr != nil || string(payload) != "replacement remains untouched" {
			t.Fatalf("retained replacement changed during replay: %q err=%v", payload, readErr)
		}
	})
}

func TestPublisherRecoversPreparedRecordAfterPromotionInterruption(t *testing.T) {
	t.Parallel()
	fixture := newReviewedFixture(t, false)
	domainRoot := t.TempDir()
	target := filepath.Join(domainRoot, fixture.manifest.Destination.Name)
	assertAbsent(t, target)
	interruption := errors.New("simulated crash after rename")
	publisher := Publisher{
		Stager: StageFunc(func(ctx context.Context, request StageRequest) error {
			return stageReviewedFixture(ctx, request, map[string]string{"documents": fixture.source}, true)
		}),
		SpaceProbe: fixedSpaceProbe(disposableAvailableBytes),
		hooks:      &publicationTestHooks{afterPromote: func() error { return interruption }},
	}
	first, err := publisher.Publish(context.Background(), fixture.manifest, PublicationInput{
		DomainRoot: domainRoot, AttemptID: "crash-attempt", ReviewedManifestDigest: fixture.manifest.ManifestDigest, Confirmed: true,
	})
	if !errors.Is(err, ErrPublicationUncertain) || first.Status != "publication_uncertain" {
		t.Fatalf("interrupted result=%#v err=%v", first, err)
	}
	if _, err := os.Stat(target); err != nil {
		t.Fatalf("atomic rename did not occur before interruption: %v", err)
	}
	recovered, err := (Publisher{}).Publish(context.Background(), fixture.manifest, PublicationInput{
		DomainRoot: domainRoot, AttemptID: "recovery-attempt", ReviewedManifestDigest: fixture.manifest.ManifestDigest, Confirmed: true,
	})
	if err != nil || !recovered.Idempotent || !recovered.Recovered || recovered.Status != publicationStatePublished {
		t.Fatalf("recovery result=%#v err=%v", recovered, err)
	}
}

func TestPublisherSerializesConcurrentIdenticalPublication(t *testing.T) {
	t.Parallel()
	fixture := newReviewedFixture(t, false)
	domainRoot := t.TempDir()
	target := filepath.Join(domainRoot, fixture.manifest.Destination.Name)
	assertAbsent(t, target)
	var stageCalls atomic.Int32
	publisher := Publisher{
		Stager: StageFunc(func(ctx context.Context, request StageRequest) error {
			stageCalls.Add(1)
			return stageReviewedFixture(ctx, request, map[string]string{"documents": fixture.source}, true)
		}),
		SpaceProbe: fixedSpaceProbe(disposableAvailableBytes),
	}
	type outcome struct {
		result PublicationResult
		err    error
	}
	start := make(chan struct{})
	outcomes := make(chan outcome, 2)
	for _, attempt := range []string{"concurrent-a", "concurrent-b"} {
		attempt := attempt
		go func() {
			<-start
			result, err := publisher.Publish(context.Background(), fixture.manifest, PublicationInput{
				DomainRoot: domainRoot, AttemptID: attempt, ReviewedManifestDigest: fixture.manifest.ManifestDigest, Confirmed: true,
			})
			outcomes <- outcome{result: result, err: err}
		}()
	}
	close(start)
	first := <-outcomes
	second := <-outcomes
	for _, observed := range []outcome{first, second} {
		if observed.err != nil || observed.result.Status != publicationStatePublished {
			t.Fatalf("concurrent result=%#v err=%v", observed.result, observed.err)
		}
	}
	if first.result.Idempotent == second.result.Idempotent || stageCalls.Load() != 1 {
		t.Fatalf("concurrent replay truth first=%#v second=%#v stage_calls=%d", first.result, second.result, stageCalls.Load())
	}
}

func TestPublisherEnforcesSpaceCancellationHardLinksAndRedaction(t *testing.T) {
	t.Parallel()

	t.Run("space and cancellation stop before staging", func(t *testing.T) {
		fixture := newReviewedFixture(t, false)
		domainRoot := t.TempDir()
		target := filepath.Join(domainRoot, fixture.manifest.Destination.Name)
		assertAbsent(t, target)
		var calls atomic.Int32
		publisher := Publisher{
			Stager:     StageFunc(func(context.Context, StageRequest) error { calls.Add(1); return nil }),
			SpaceProbe: fixedSpaceProbe(1),
		}
		if _, err := publisher.Publish(context.Background(), fixture.manifest, PublicationInput{
			DomainRoot: domainRoot, AttemptID: "unconfirmed-attempt", ReviewedManifestDigest: fixture.manifest.ManifestDigest,
		}); !errors.Is(err, ErrInvalidManifest) {
			t.Fatalf("unconfirmed publication error=%v", err)
		}
		assertAbsent(t, target)
		if _, err := publisher.Publish(context.Background(), fixture.manifest, PublicationInput{
			DomainRoot: domainRoot, AttemptID: "space-attempt", ReviewedManifestDigest: fixture.manifest.ManifestDigest, Confirmed: true,
		}); !errors.Is(err, ErrInsufficientSpace) {
			t.Fatalf("live space error=%v", err)
		}
		if calls.Load() != 0 {
			t.Fatalf("stager ran after failed space gate")
		}
		assertAbsent(t, target)

		cancelled, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := publisher.Publish(cancelled, fixture.manifest, PublicationInput{
			DomainRoot: domainRoot, AttemptID: "cancelled-attempt", ReviewedManifestDigest: fixture.manifest.ManifestDigest, Confirmed: true,
		}); !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation error=%v", err)
		}
	})

	t.Run("adapter errors cannot leak paths", func(t *testing.T) {
		fixture := newReviewedFixture(t, false)
		domainRoot := t.TempDir()
		assertAbsent(t, filepath.Join(domainRoot, fixture.manifest.Destination.Name))
		publisher := Publisher{
			Stager: StageFunc(func(context.Context, StageRequest) error {
				return fmt.Errorf("source %s destination %s", fixture.source, domainRoot)
			}),
			SpaceProbe: fixedSpaceProbe(disposableAvailableBytes),
		}
		_, err := publisher.Publish(context.Background(), fixture.manifest, PublicationInput{
			DomainRoot: domainRoot, AttemptID: "redaction-attempt", ReviewedManifestDigest: fixture.manifest.ManifestDigest, Confirmed: true,
		})
		if err == nil || strings.Contains(err.Error(), fixture.source) || strings.Contains(err.Error(), domainRoot) {
			t.Fatalf("unredacted adapter error=%v", err)
		}
	})

	t.Run("hard-link relation is exact", func(t *testing.T) {
		source := t.TempDir()
		mustWriteFile(t, filepath.Join(source, "original.txt"), "linked payload")
		if err := os.Link(filepath.Join(source, "original.txt"), filepath.Join(source, "alias.txt")); err != nil {
			t.Fatalf("create source hard link: %v", err)
		}
		fixture := planFixtureFromRoot(t, source, "documents", "hardlinks", false)

		flattenedRoot := t.TempDir()
		assertAbsent(t, filepath.Join(flattenedRoot, fixture.manifest.Destination.Name))
		flattened := Publisher{
			Stager: StageFunc(func(ctx context.Context, request StageRequest) error {
				return stageReviewedFixture(ctx, request, map[string]string{"documents": source}, false)
			}),
			SpaceProbe: fixedSpaceProbe(disposableAvailableBytes),
		}
		if _, err := flattened.Publish(context.Background(), fixture.manifest, PublicationInput{
			DomainRoot: flattenedRoot, AttemptID: "flattened-attempt", ReviewedManifestDigest: fixture.manifest.ManifestDigest, Confirmed: true,
		}); !errors.Is(err, ErrVerificationFailed) {
			t.Fatalf("flattened hard-link error=%v", err)
		}

		preservedRoot := t.TempDir()
		target := filepath.Join(preservedRoot, fixture.manifest.Destination.Name)
		assertAbsent(t, target)
		preserved := Publisher{
			Stager: StageFunc(func(ctx context.Context, request StageRequest) error {
				return stageReviewedFixture(ctx, request, map[string]string{"documents": source}, true)
			}),
			SpaceProbe: fixedSpaceProbe(disposableAvailableBytes),
		}
		if _, err := preserved.Publish(context.Background(), fixture.manifest, PublicationInput{
			DomainRoot: preservedRoot, AttemptID: "preserved-attempt", ReviewedManifestDigest: fixture.manifest.ManifestDigest, Confirmed: true,
		}); err != nil {
			t.Fatalf("publish preserved hard links: %v", err)
		}
		original, err := os.Stat(filepath.Join(target, "original.txt"))
		if err != nil {
			t.Fatal(err)
		}
		alias, err := os.Stat(filepath.Join(target, "alias.txt"))
		if err != nil || !os.SameFile(original, alias) {
			t.Fatalf("published hard links differ: err=%v", err)
		}
	})
}

func newReviewedFixture(t *testing.T, withSymlink bool) reviewedFixture {
	t.Helper()
	source := t.TempDir()
	mustMkdirAll(t, filepath.Join(source, "notes"))
	mustMkdirAll(t, filepath.Join(source, "ignored"))
	mustWriteFile(t, filepath.Join(source, ".loomignore"), "ignored/\n")
	mustWriteFile(t, filepath.Join(source, "kept.txt"), "kept payload")
	mustWriteFile(t, filepath.Join(source, "notes", "note.md"), "# Note\n")
	mustWriteFile(t, filepath.Join(source, "ignored", "cache.txt"), "ignored secret payload")
	if withSymlink {
		if err := os.Symlink("kept.txt", filepath.Join(source, "kept-link")); err != nil {
			t.Fatalf("create safe symlink: %v", err)
		}
	}
	return planFixtureFromRoot(t, source, "documents", "reviewed-documents", true)
}

func planFixtureFromRoot(t *testing.T, source, rootID, destination string, discoverIgnore bool) reviewedFixture {
	t.Helper()
	inventory, err := (Service{}).Inventory(context.Background(), Config{Roots: []RootSpec{{
		ID: rootID, Path: source, Domain: DomainDocuments,
		Ignore: IgnoreConfig{Profile: filepolicy.ProfileManaged, DiscoverUserRules: discoverIgnore},
	}}})
	if err != nil {
		t.Fatalf("inventory fixture: %v: %v", err, errors.Unwrap(err))
	}
	fileDigests := fixtureFileDigests(t, source, findRoot(t, inventory, rootID))
	input := PlanInput{
		Inventory:          inventory,
		Selections:         []RootSelection{{RootID: rootID}},
		Destination:        DestinationSpec{Domain: DomainDocuments, Name: destination, Mode: 0o750},
		FileDigests:        fileDigests,
		RequestedTransport: lane.TransportModeAuto,
		MetadataPolicy:     MetadataPolicyStrictPortable,
		AvailableBytes:     disposableAvailableBytes,
	}
	manifest, err := PlanMigration(context.Background(), input)
	if err != nil {
		t.Fatalf("plan fixture: %v: %v", err, errors.Unwrap(err))
	}
	return reviewedFixture{source: source, inventory: inventory, input: input, manifest: manifest}
}

func fixtureFileDigests(t *testing.T, source string, root RootInventory) map[string]string {
	t.Helper()
	result := map[string]string{}
	for _, entry := range root.Entries {
		if entry.Kind != filesystemmeta.ObjectKindRegularFile || !entry.Ignore.Included {
			continue
		}
		encoded, err := rootRelativeLocator(root.ID, entry.Locator)
		if err != nil {
			t.Fatal(err)
		}
		relative, err := decodeEncodedPath(encoded, false)
		if err != nil {
			t.Fatal(err)
		}
		payload, err := os.ReadFile(filepath.Join(source, filepath.FromSlash(relative)))
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(payload)
		result[entry.Locator] = "sha256:" + hex.EncodeToString(sum[:])
	}
	return result
}

func stageReviewedFixture(ctx context.Context, request StageRequest, sources map[string]string, preserveHardLinks bool) error {
	linked := map[string]string{}
	for _, entry := range request.Manifest.Entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		targetRelative, err := decodeEncodedPath(entry.TargetPath, false)
		if err != nil {
			return err
		}
		target := filepath.Join(request.StagingPath, filepath.FromSlash(targetRelative))
		rootID, sourceEncoded, err := splitRootLocator(entry.SourceLocator)
		if err != nil {
			return err
		}
		sourceRoot, present := sources[rootID]
		if !present {
			return fmt.Errorf("missing source fixture")
		}
		sourceRelative, err := decodeEncodedPath(sourceEncoded, true)
		if err != nil {
			return err
		}
		sourcePath := sourceRoot
		if sourceRelative != "" {
			sourcePath = filepath.Join(sourceRoot, filepath.FromSlash(sourceRelative))
		}
		switch entry.Kind {
		case filesystemmeta.ObjectKindDirectory:
			if err := os.MkdirAll(target, os.FileMode(entry.Mode)); err != nil {
				return err
			}
			if err := os.Chmod(target, os.FileMode(entry.Mode)); err != nil {
				return err
			}
		case filesystemmeta.ObjectKindRegularFile:
			if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
				return err
			}
			if preserveHardLinks {
				if existing := linked[entry.SourceObjectIdentity]; existing != "" {
					if err := os.Link(existing, target); err != nil {
						return err
					}
					continue
				}
			}
			payload, err := os.ReadFile(sourcePath)
			if err != nil {
				return err
			}
			if err := os.WriteFile(target, payload, os.FileMode(entry.Mode)); err != nil {
				return err
			}
			if err := os.Chmod(target, os.FileMode(entry.Mode)); err != nil {
				return err
			}
			linked[entry.SourceObjectIdentity] = target
		case filesystemmeta.ObjectKindSymlink:
			if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
				return err
			}
			targetOfLink, err := decodeEncodedPath(entry.TargetSymlinkPath, true)
			if err != nil {
				return err
			}
			relativeTarget, err := filepath.Rel(filepath.Dir(targetRelative), targetOfLink)
			if err != nil {
				return err
			}
			if err := os.Symlink(filepath.ToSlash(relativeTarget), target); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unsupported fixture entry %q", entry.Kind)
		}
	}
	return nil
}

func fixedSpaceProbe(value uint64) SpaceProbe {
	return SpaceProbeFunc(func(context.Context, string) (uint64, error) { return value, nil })
}

func cloneStringMap(input map[string]string) map[string]string {
	result := make(map[string]string, len(input))
	for key, value := range input {
		result[key] = value
	}
	return result
}

func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func assertAbsent(t *testing.T, value string) {
	t.Helper()
	if _, err := os.Lstat(value); !os.IsNotExist(err) {
		t.Fatalf("path must be absent before publication: %s err=%v", value, err)
	}
}

func jsonMarshal(value any) ([]byte, error) {
	return json.Marshal(value)
}
