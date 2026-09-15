package estatemigration

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/url"
	"path"
	"reflect"
	"sort"
	"strings"
	"unicode/utf8"

	"loom.local/loom/internal/filesystemmeta"
	"loom.local/loom/internal/lane"
)

const (
	ManifestSchemaVersion        = "loom.digital_estate_migration_manifest.v1"
	MetadataPolicyStrictPortable = "strict_portable_v1"

	maxSerializedManifestBytes = 256 * 1024 * 1024
	minimumSpaceSafetyBytes    = uint64(64 * 1024 * 1024)
	spaceSafetyDivisor         = uint64(10)
)

var (
	ErrInvalidManifest      = errors.New("invalid digital estate migration manifest")
	ErrOverlappingSources   = errors.New("digital estate migration sources overlap")
	ErrUnsupportedEntry     = errors.New("digital estate migration entry is unsupported")
	ErrInsufficientSpace    = errors.New("insufficient space for digital estate migration")
	ErrDestinationExists    = errors.New("digital estate migration destination already exists")
	ErrChangedReplay        = errors.New("digital estate migration replay changed")
	ErrVerificationFailed   = errors.New("digital estate migration verification failed")
	ErrPublicationUncertain = errors.New("digital estate migration publication state is uncertain")
)

type RootSelection struct {
	RootID            string `json:"root_id"`
	DestinationPrefix string `json:"destination_prefix,omitempty"`
}

type DestinationSpec struct {
	Domain Domain `json:"domain"`
	Name   string `json:"name"`
	Mode   uint32 `json:"mode"`
}

// PlanInput carries only reviewed, redacted inventory state. FileDigests must
// contain one SHA-256 digest for every selected regular file and no entries for
// ignored or unsupported files. AvailableBytes is a reviewed space observation,
// not a live filesystem query.
type PlanInput struct {
	Inventory          Inventory          `json:"inventory"`
	Selections         []RootSelection    `json:"selections"`
	Destination        DestinationSpec    `json:"destination"`
	FileDigests        map[string]string  `json:"file_digests"`
	RequestedTransport lane.TransportMode `json:"requested_transport"`
	MetadataPolicy     string             `json:"metadata_policy"`
	AvailableBytes     uint64             `json:"available_bytes"`
}

type MigrationManifest struct {
	SchemaVersion   string                    `json:"schema_version"`
	ManifestID      string                    `json:"manifest_id"`
	ManifestDigest  string                    `json:"manifest_digest"`
	InventoryDigest string                    `json:"inventory_digest"`
	SourceDigest    string                    `json:"source_digest"`
	Sources         []ManifestSource          `json:"sources"`
	Destination     ManifestDestination       `json:"destination"`
	Entries         []ManifestEntry           `json:"entries"`
	Totals          ManifestTotals            `json:"totals"`
	MetadataPolicy  ManifestMetadataPolicy    `json:"metadata_policy"`
	Transport       lane.BundleRecommendation `json:"transport"`
	Space           SpacePreflight            `json:"space_preflight"`
	Bounds          PublicationBounds         `json:"bounds"`
	AcceptanceGates []string                  `json:"acceptance_gates"`
	Rollback        []RollbackInstruction     `json:"rollback"`
}

type ManifestSource struct {
	RootID               string          `json:"root_id"`
	Locator              string          `json:"locator"`
	Domain               Domain          `json:"domain"`
	DestinationPrefix    string          `json:"destination_prefix,omitempty"`
	PolicyFingerprint    string          `json:"policy_fingerprint"`
	RootMetadata         MetadataPosture `json:"root_metadata"`
	InventorySummary     RootSummary     `json:"inventory_summary"`
	SelectedEntryCount   int             `json:"selected_entry_count"`
	StructuralEntryCount int             `json:"structural_entry_count"`
	SelectedLogicalBytes uint64          `json:"selected_logical_bytes"`
}

type ManifestDestination struct {
	Domain         Domain `json:"domain"`
	Name           string `json:"name"`
	Locator        string `json:"locator"`
	IdentityDigest string `json:"identity_digest"`
	Mode           uint32 `json:"mode"`
}

type ManifestEntry struct {
	SourceLocator        string          `json:"source_locator"`
	TargetPath           string          `json:"target_path"`
	Kind                 string          `json:"kind"`
	LogicalBytes         uint64          `json:"logical_bytes"`
	Mode                 uint32          `json:"mode"`
	ContentDigest        string          `json:"content_digest,omitempty"`
	SourceObjectIdentity string          `json:"source_object_identity,omitempty"`
	SourceLinkCount      uint64          `json:"source_link_count,omitempty"`
	HardLinkGroup        string          `json:"hard_link_group,omitempty"`
	SourceSymlinkTarget  string          `json:"source_symlink_target,omitempty"`
	TargetSymlinkPath    string          `json:"target_symlink_path,omitempty"`
	StructuralOnly       bool            `json:"structural_only"`
	Metadata             MetadataPosture `json:"metadata"`
	Ignore               IgnoreDecision  `json:"ignore"`
}

type ManifestTotals struct {
	EntryCount       int    `json:"entry_count"`
	RegularFileCount int    `json:"regular_file_count"`
	DirectoryCount   int    `json:"directory_count"`
	SymlinkCount     int    `json:"symlink_count"`
	StructuralCount  int    `json:"structural_count"`
	LogicalBytes     uint64 `json:"logical_bytes"`
}

type ManifestMetadataPolicy struct {
	Name                    string `json:"name"`
	RegularContent          string `json:"regular_content"`
	PermissionBits          string `json:"permission_bits"`
	SafeRelativeSymlinks    string `json:"safe_relative_symlinks"`
	HardLinks               string `json:"hard_links"`
	ExtendedMetadata        string `json:"extended_metadata"`
	SparseFiles             string `json:"sparse_files"`
	PackagesAndSpecialFiles string `json:"packages_and_special_files"`
}

type SpacePreflight struct {
	PayloadBytes            uint64 `json:"payload_bytes"`
	TransportTemporaryBytes uint64 `json:"transport_temporary_bytes"`
	SafetyMarginBytes       uint64 `json:"safety_margin_bytes"`
	RequiredBytes           uint64 `json:"required_bytes"`
	PlannedAvailableBytes   uint64 `json:"planned_available_bytes"`
	Sufficient              bool   `json:"sufficient"`
}

type PublicationBounds struct {
	MaxEntries      int    `json:"max_entries"`
	MaxDepth        int    `json:"max_depth"`
	MaxPayloadBytes uint64 `json:"max_payload_bytes"`
	MaxFileBytes    uint64 `json:"max_file_bytes"`
}

type RollbackInstruction struct {
	Phase              string   `json:"phase"`
	Action             string   `json:"action"`
	Target             string   `json:"target"`
	RequiredConditions []string `json:"required_conditions"`
	SourceMutation     bool     `json:"source_mutation"`
}

func strictPortableMetadataPolicy() ManifestMetadataPolicy {
	return ManifestMetadataPolicy{
		Name:                    MetadataPolicyStrictPortable,
		RegularContent:          "sha256_exact",
		PermissionBits:          "exact",
		SafeRelativeSymlinks:    "preserve_resolved_target",
		HardLinks:               "preserve_complete_groups",
		ExtendedMetadata:        "permit_platform_provenance_reject_others",
		SparseFiles:             "reject",
		PackagesAndSpecialFiles: "reject",
	}
}

func requiredMigrationGates() []string {
	return []string{
		"absent_destination",
		"atomic_no_replace_publication",
		"backup_fetch_restore_before_canonical",
		"canonical_ownership_permissions",
		"entry_fidelity",
		"loom_registration_after_verification",
		"orca_registration_after_loom_acceptance",
		"source_retained",
		"source_stability",
		"space_recheck",
		"staged_content_digest",
	}
}

func rollbackInstructions(destination ManifestDestination) []RollbackInstruction {
	return []RollbackInstruction{
		{
			Phase:  "before_publication",
			Action: "remove_unpublished_staging_only",
			Target: "migration://staging/<attempt_id>",
			RequiredConditions: []string{
				"staging identity is bound to this manifest",
				"canonical destination is absent",
			},
			SourceMutation: false,
		},
		{
			Phase:  "after_publication_before_canonical_acceptance",
			Action: "remove_new_target_only",
			Target: destination.Locator,
			RequiredConditions: []string{
				"target still matches this manifest",
				"LOOM has not accepted canonical custody",
				"registration and backup receipts do not claim completion",
			},
			SourceMutation: false,
		},
	}
}

// PlanMigration converts explicitly selected roots from a reviewed inventory
// into one deterministic, path-redacted manifest. It performs no filesystem,
// transfer, registration, or publication operation.
func PlanMigration(ctx context.Context, input PlanInput) (MigrationManifest, error) {
	if err := ctx.Err(); err != nil {
		return MigrationManifest{}, err
	}
	if err := validateInventoryForPlan(input.Inventory); err != nil {
		return MigrationManifest{}, err
	}
	if len(input.Inventory.Overlaps) != 0 {
		return MigrationManifest{}, ErrOverlappingSources
	}
	policyName := strings.TrimSpace(input.MetadataPolicy)
	if input.MetadataPolicy != "" && input.MetadataPolicy != policyName {
		return MigrationManifest{}, fmt.Errorf("%w: metadata policy is not canonical", ErrInvalidManifest)
	}
	if policyName == "" {
		policyName = MetadataPolicyStrictPortable
	}
	if policyName != MetadataPolicyStrictPortable {
		return MigrationManifest{}, fmt.Errorf("%w: unsupported metadata policy", ErrInvalidManifest)
	}
	destination, err := normalizeDestination(input.Destination)
	if err != nil {
		return MigrationManifest{}, err
	}
	selections, err := normalizeSelections(input.Selections, input.Inventory.Bounds.MaxRoots)
	if err != nil {
		return MigrationManifest{}, err
	}
	if len(selections) > 1 {
		for _, selection := range selections {
			if selection.DestinationPrefix == "" {
				return MigrationManifest{}, fmt.Errorf("%w: multiple roots require distinct destination prefixes", ErrInvalidManifest)
			}
		}
	}

	roots := make(map[string]RootInventory, len(input.Inventory.Roots))
	for _, root := range input.Inventory.Roots {
		roots[root.ID] = root
	}
	for _, selection := range selections {
		root, ok := roots[selection.RootID]
		if !ok {
			return MigrationManifest{}, fmt.Errorf("%w: selected root %q is absent", ErrInvalidManifest, selection.RootID)
		}
		if root.Domain != destination.Domain {
			return MigrationManifest{}, fmt.Errorf("%w: source and destination domains differ", ErrInvalidManifest)
		}
	}

	usedDigests := map[string]struct{}{}
	manifest := MigrationManifest{
		SchemaVersion:   ManifestSchemaVersion,
		InventoryDigest: input.Inventory.Digest,
		Sources:         []ManifestSource{},
		Destination:     destination,
		Entries:         []ManifestEntry{},
		MetadataPolicy:  strictPortableMetadataPolicy(),
		AcceptanceGates: requiredMigrationGates(),
		Bounds: PublicationBounds{
			MaxEntries: input.Inventory.Bounds.MaxEntries,
			MaxDepth:   input.Inventory.Bounds.MaxDepth,
		},
	}
	entryByTarget := map[string]ManifestEntry{}
	selectedSourceLocators := map[string]struct{}{}

	for _, selection := range selections {
		if err := ctx.Err(); err != nil {
			return MigrationManifest{}, err
		}
		root := roots[selection.RootID]
		byLocator := make(map[string]Entry, len(root.Entries))
		for _, entry := range root.Entries {
			byLocator[entry.Locator] = entry
		}
		rootEntry, ok := byLocator[root.Locator]
		if !ok || rootEntry.Kind != filesystemmeta.ObjectKindDirectory || !rootEntry.Ignore.Included {
			return MigrationManifest{}, fmt.Errorf("%w: selected root entry is not a plain directory", ErrUnsupportedEntry)
		}

		source := ManifestSource{
			RootID:            root.ID,
			Locator:           root.Locator,
			Domain:            root.Domain,
			DestinationPrefix: selection.DestinationPrefix,
			PolicyFingerprint: root.PolicyFingerprint,
			RootMetadata:      rootEntry.Metadata,
			InventorySummary:  root.Summary,
		}
		if selection.DestinationPrefix != "" {
			candidate := manifestEntryFromInventory(rootEntry, selection.DestinationPrefix, true)
			candidate.SourceObjectIdentity = ""
			candidate.Metadata = portableStructuralMetadata(rootEntry.Metadata)
			if err := addManifestEntry(entryByTarget, candidate); err != nil {
				return MigrationManifest{}, err
			}
			source.StructuralEntryCount++
		}

		included := map[string]struct{}{}
		for _, entry := range root.Entries {
			if entry.Locator == root.Locator || !entry.Ignore.Included {
				continue
			}
			if err := validateStrictMetadata(entry); err != nil {
				return MigrationManifest{}, fmt.Errorf("%w: %s", err, entry.Locator)
			}
			included[entry.Locator] = struct{}{}
			selectedSourceLocators[entry.Locator] = struct{}{}
			relative, err := rootRelativeLocator(root.ID, entry.Locator)
			if err != nil {
				return MigrationManifest{}, err
			}
			target := joinEncodedPath(selection.DestinationPrefix, relative)
			candidate := manifestEntryFromInventory(entry, target, false)
			if entry.Kind == filesystemmeta.ObjectKindRegularFile {
				digest, present := input.FileDigests[entry.Locator]
				if !present || !validCanonicalDigest(digest) {
					return MigrationManifest{}, fmt.Errorf("%w: regular file digest is missing or invalid for %s", ErrInvalidManifest, entry.Locator)
				}
				candidate.ContentDigest = digest
				usedDigests[entry.Locator] = struct{}{}
			}
			if err := addManifestEntry(entryByTarget, candidate); err != nil {
				return MigrationManifest{}, err
			}
			source.SelectedEntryCount++
			if checked, addErr := addUint64(source.SelectedLogicalBytes, candidate.LogicalBytes); addErr != nil {
				return MigrationManifest{}, addErr
			} else {
				source.SelectedLogicalBytes = checked
			}
		}

		for locator := range included {
			relative, err := rootRelativeLocator(root.ID, locator)
			if err != nil {
				return MigrationManifest{}, err
			}
			for parent := encodedParent(relative); parent != ""; parent = encodedParent(parent) {
				parentLocator := root.Locator + "/" + parent
				if _, alreadyIncluded := included[parentLocator]; alreadyIncluded {
					continue
				}
				parentEntry, present := byLocator[parentLocator]
				if !present || parentEntry.Kind != filesystemmeta.ObjectKindDirectory {
					return MigrationManifest{}, fmt.Errorf("%w: selected entry lacks an inventoried directory parent", ErrInvalidManifest)
				}
				if err := validateStrictMetadata(parentEntry); err != nil {
					return MigrationManifest{}, err
				}
				target := joinEncodedPath(selection.DestinationPrefix, parent)
				candidate := manifestEntryFromInventory(parentEntry, target, true)
				if _, exists := entryByTarget[target]; !exists {
					if err := addManifestEntry(entryByTarget, candidate); err != nil {
						return MigrationManifest{}, err
					}
					source.StructuralEntryCount++
				}
			}
		}
		manifest.Sources = append(manifest.Sources, source)
	}

	for locator, digest := range input.FileDigests {
		if !utf8.ValidString(locator) || !validCanonicalDigest(digest) {
			return MigrationManifest{}, fmt.Errorf("%w: file digest evidence is malformed", ErrInvalidManifest)
		}
		if _, used := usedDigests[locator]; !used {
			return MigrationManifest{}, fmt.Errorf("%w: file digest evidence includes an unselected locator", ErrInvalidManifest)
		}
	}

	for _, entry := range entryByTarget {
		manifest.Entries = append(manifest.Entries, entry)
	}
	sort.Slice(manifest.Entries, func(left, right int) bool {
		if manifest.Entries[left].TargetPath != manifest.Entries[right].TargetPath {
			return manifest.Entries[left].TargetPath < manifest.Entries[right].TargetPath
		}
		return manifest.Entries[left].SourceLocator < manifest.Entries[right].SourceLocator
	})
	if err := validateTargetHierarchy(manifest.Entries, manifest.Bounds.MaxDepth); err != nil {
		return MigrationManifest{}, err
	}
	if len(manifest.Entries) > manifest.Bounds.MaxEntries {
		return MigrationManifest{}, budgetError("migration manifest entries", manifest.Bounds.MaxEntries)
	}
	if err := bindSymlinkTargets(&manifest); err != nil {
		return MigrationManifest{}, err
	}
	if err := bindHardLinkGroups(&manifest, input.Inventory, selectedSourceLocators); err != nil {
		return MigrationManifest{}, err
	}
	manifest.Totals, manifest.Bounds.MaxFileBytes, err = manifestTotals(manifest.Entries)
	if err != nil {
		return MigrationManifest{}, err
	}
	manifest.Bounds.MaxPayloadBytes = manifest.Totals.LogicalBytes
	manifest.SourceDigest, err = manifestSourceDigest(manifest.Entries)
	if err != nil {
		return MigrationManifest{}, err
	}
	if manifest.Totals.LogicalBytes > math.MaxInt64 {
		return MigrationManifest{}, ErrMetadataOverflow
	}
	manifest.Transport, err = lane.SelectBundleTransport(lane.TransferPlan{
		FileCount:  manifest.Totals.RegularFileCount,
		TotalBytes: int64(manifest.Totals.LogicalBytes),
	}, input.RequestedTransport)
	if err != nil {
		return MigrationManifest{}, fmt.Errorf("%w: transport selection failed", ErrInvalidManifest)
	}
	manifest.Space, err = buildSpacePreflight(manifest.Totals.LogicalBytes, manifest.Transport, input.AvailableBytes)
	if err != nil {
		return MigrationManifest{}, err
	}
	manifest.Rollback = rollbackInstructions(manifest.Destination)
	manifest.ManifestID, err = manifestSemanticID(manifest)
	if err != nil {
		return MigrationManifest{}, err
	}
	manifest.ManifestDigest, err = manifestDigest(manifest)
	if err != nil {
		return MigrationManifest{}, err
	}
	if err := ValidateManifest(manifest); err != nil {
		return MigrationManifest{}, err
	}
	return manifest, nil
}

func normalizeSelections(input []RootSelection, limit int) ([]RootSelection, error) {
	if len(input) == 0 {
		return nil, fmt.Errorf("%w: at least one root selection is required", ErrInvalidManifest)
	}
	if len(input) > limit {
		return nil, budgetError("migration source roots", limit)
	}
	result := append([]RootSelection{}, input...)
	seenRoots := map[string]struct{}{}
	seenPrefixes := map[string]struct{}{}
	for index := range result {
		selection := &result[index]
		if selection.RootID != strings.TrimSpace(selection.RootID) {
			return nil, fmt.Errorf("%w: selected root id is not canonical", ErrInvalidManifest)
		}
		if !rootIDPattern(selection.RootID) {
			return nil, fmt.Errorf("%w: selected root id is invalid", ErrInvalidManifest)
		}
		if _, exists := seenRoots[selection.RootID]; exists {
			return nil, fmt.Errorf("%w: selected root is duplicated", ErrInvalidManifest)
		}
		seenRoots[selection.RootID] = struct{}{}
		encoded, err := encodeInputRelativePath(selection.DestinationPrefix, true)
		if err != nil {
			return nil, err
		}
		if strings.Contains(encoded, "/") {
			return nil, fmt.Errorf("%w: destination prefix must be one direct child", ErrInvalidManifest)
		}
		selection.DestinationPrefix = encoded
		if encoded != "" {
			if _, exists := seenPrefixes[encoded]; exists {
				return nil, fmt.Errorf("%w: destination prefix is duplicated", ErrInvalidManifest)
			}
			seenPrefixes[encoded] = struct{}{}
		}
	}
	sort.Slice(result, func(left, right int) bool { return result[left].RootID < result[right].RootID })
	for left := range result {
		for right := left + 1; right < len(result); right++ {
			leftPrefix := result[left].DestinationPrefix
			rightPrefix := result[right].DestinationPrefix
			if leftPrefix == "" || rightPrefix == "" || strings.HasPrefix(leftPrefix, rightPrefix+"/") || strings.HasPrefix(rightPrefix, leftPrefix+"/") {
				return nil, fmt.Errorf("%w: selected roots have overlapping destination prefixes", ErrInvalidManifest)
			}
		}
	}
	return result, nil
}

func normalizeDestination(input DestinationSpec) (ManifestDestination, error) {
	if !input.Domain.valid() {
		return ManifestDestination{}, fmt.Errorf("%w: destination domain is invalid", ErrInvalidManifest)
	}
	if input.Name != strings.TrimSpace(input.Name) {
		return ManifestDestination{}, fmt.Errorf("%w: destination name is not canonical", ErrInvalidManifest)
	}
	if !validPathComponent(input.Name) || len([]byte(input.Name)) > 255 || input.Name == ".loom-migrations" {
		return ManifestDestination{}, fmt.Errorf("%w: destination name is invalid", ErrInvalidManifest)
	}
	if input.Mode == 0 {
		input.Mode = 0o750
	}
	if input.Mode&^uint32(0o777) != 0 || input.Mode&0o700 != 0o700 {
		return ManifestDestination{}, fmt.Errorf("%w: destination mode is invalid", ErrInvalidManifest)
	}
	locator := "target://" + string(input.Domain) + "/" + url.PathEscape(input.Name)
	identity := digestText("migration-destination-v1\x00" + string(input.Domain) + "\x00" + input.Name)
	return ManifestDestination{Domain: input.Domain, Name: input.Name, Locator: locator, IdentityDigest: identity, Mode: input.Mode}, nil
}

func manifestEntryFromInventory(entry Entry, target string, structural bool) ManifestEntry {
	mode := entry.Metadata.Mode
	logicalBytes := uint64(0)
	if entry.Kind == filesystemmeta.ObjectKindRegularFile {
		logicalBytes = entry.LogicalBytes
	}
	if entry.Kind == filesystemmeta.ObjectKindSymlink {
		mode = 0
	}
	linkCount := uint64(0)
	objectIdentity := ""
	if entry.Kind == filesystemmeta.ObjectKindRegularFile && entry.LinkCount != nil {
		linkCount = *entry.LinkCount
		objectIdentity = entry.ObjectIdentity
	}
	return ManifestEntry{
		SourceLocator:        entry.Locator,
		TargetPath:           target,
		Kind:                 entry.Kind,
		LogicalBytes:         logicalBytes,
		Mode:                 mode,
		SourceObjectIdentity: objectIdentity,
		SourceLinkCount:      linkCount,
		SourceSymlinkTarget:  entry.SymlinkTarget,
		StructuralOnly:       structural,
		Metadata:             entry.Metadata,
		Ignore:               entry.Ignore,
	}
}

func portableStructuralMetadata(input MetadataPosture) MetadataPosture {
	return MetadataPosture{
		Known:      true,
		Mode:       input.Mode,
		Executable: input.Executable,
		XattrNames: []string{},
		Risks:      []string{},
	}
}

func addManifestEntry(entries map[string]ManifestEntry, candidate ManifestEntry) error {
	if candidate.TargetPath == "" {
		return fmt.Errorf("%w: manifest entry cannot replace the target root", ErrInvalidManifest)
	}
	if existing, present := entries[candidate.TargetPath]; present {
		if reflect.DeepEqual(existing, candidate) {
			return nil
		}
		return fmt.Errorf("%w: source entries collide at target path %s", ErrInvalidManifest, candidate.TargetPath)
	}
	entries[candidate.TargetPath] = candidate
	return nil
}

func validateTargetHierarchy(entries []ManifestEntry, maxDepth int) error {
	byTarget := make(map[string]ManifestEntry, len(entries))
	for _, entry := range entries {
		if entry.TargetPath == "" || strings.Count(entry.TargetPath, "/")+1 > maxDepth {
			return fmt.Errorf("%w: target path exceeds the publication depth bound", ErrInvalidManifest)
		}
		byTarget[entry.TargetPath] = entry
	}
	for _, entry := range entries {
		parent := encodedParent(entry.TargetPath)
		if parent == "" {
			continue
		}
		parentEntry, present := byTarget[parent]
		if !present || parentEntry.Kind != filesystemmeta.ObjectKindDirectory {
			return fmt.Errorf("%w: target path lacks an explicit directory parent", ErrInvalidManifest)
		}
	}
	return nil
}

func validateStrictMetadata(entry Entry) error {
	if !entry.Metadata.Known || entry.Metadata.PermissionDenied {
		return fmt.Errorf("%w: metadata is unknown or unreadable", ErrUnsupportedEntry)
	}
	if len(entry.Metadata.Risks) != 0 {
		return fmt.Errorf("%w: metadata fidelity risks are present", ErrUnsupportedEntry)
	}
	if entry.Metadata.HasACL || entry.Metadata.HasResourceFork || entry.Metadata.HasFinderTags || entry.Metadata.HasQuarantine {
		return fmt.Errorf("%w: extended metadata is present", ErrUnsupportedEntry)
	}
	if entry.Metadata.HasXattrs {
		if len(entry.Metadata.XattrNames) == 0 {
			return fmt.Errorf("%w: extended metadata names are unavailable", ErrUnsupportedEntry)
		}
		for _, name := range entry.Metadata.XattrNames {
			if name != "com.apple.provenance" {
				return fmt.Errorf("%w: unsupported extended metadata is present", ErrUnsupportedEntry)
			}
		}
	} else if len(entry.Metadata.XattrNames) != 0 {
		return fmt.Errorf("%w: extended metadata posture is inconsistent", ErrUnsupportedEntry)
	}
	if entry.Metadata.Sparse {
		return fmt.Errorf("%w: sparse files are not supported by the strict portable policy", ErrUnsupportedEntry)
	}
	if entry.Metadata.Package {
		return fmt.Errorf("%w: package directories are not supported by the strict portable policy", ErrUnsupportedEntry)
	}
	switch entry.Kind {
	case filesystemmeta.ObjectKindRegularFile:
		if !validCanonicalDigest(entry.ObjectIdentity) || entry.AllocatedBytes == nil || entry.LinkCount == nil {
			return fmt.Errorf("%w: regular-file identity is incomplete", ErrUnsupportedEntry)
		}
	case filesystemmeta.ObjectKindDirectory:
	case filesystemmeta.ObjectKindSymlink:
		if entry.SymlinkTarget == "" {
			return fmt.Errorf("%w: symlink target is unavailable", ErrUnsupportedEntry)
		}
	default:
		return fmt.Errorf("%w: object kind %q is unsupported", ErrUnsupportedEntry, entry.Kind)
	}
	return nil
}

func bindSymlinkTargets(manifest *MigrationManifest) error {
	targetsBySource := make(map[string]string, len(manifest.Entries))
	for _, entry := range manifest.Entries {
		targetsBySource[entry.SourceLocator] = entry.TargetPath
	}
	for index := range manifest.Entries {
		entry := &manifest.Entries[index]
		if entry.Kind != filesystemmeta.ObjectKindSymlink {
			if entry.SourceSymlinkTarget != "" {
				return fmt.Errorf("%w: non-symlink carries a symlink target", ErrInvalidManifest)
			}
			continue
		}
		if _, _, err := splitRootLocator(entry.SourceSymlinkTarget); err != nil {
			return fmt.Errorf("%w: symlink target is external or malformed", ErrUnsupportedEntry)
		}
		targetPath, published := targetsBySource[entry.SourceSymlinkTarget]
		if !published {
			return fmt.Errorf("%w: symlink target is not in the published selection", ErrUnsupportedEntry)
		}
		entry.TargetSymlinkPath = targetPath
		if entry.TargetSymlinkPath == "" {
			return fmt.Errorf("%w: symlink to the publication root is unsupported", ErrUnsupportedEntry)
		}
	}
	return nil
}

func bindHardLinkGroups(manifest *MigrationManifest, inventory Inventory, selected map[string]struct{}) error {
	allDuplicates := map[string][]string{}
	for _, duplicate := range inventory.DuplicateObjects {
		allDuplicates[duplicate.ObjectIdentity] = duplicate.Locators
	}
	selectedCounts := map[string]int{}
	for _, entry := range manifest.Entries {
		if entry.Kind == filesystemmeta.ObjectKindRegularFile {
			selectedCounts[entry.SourceObjectIdentity]++
		}
	}
	for index := range manifest.Entries {
		entry := &manifest.Entries[index]
		if entry.Kind != filesystemmeta.ObjectKindRegularFile {
			continue
		}
		group := allDuplicates[entry.SourceObjectIdentity]
		if len(group) > 1 {
			for _, locator := range group {
				if _, present := selected[locator]; !present {
					return fmt.Errorf("%w: hard-link group is only partially selected", ErrUnsupportedEntry)
				}
			}
			entry.HardLinkGroup = entry.SourceObjectIdentity
		}
		if entry.SourceLinkCount != uint64(selectedCounts[entry.SourceObjectIdentity]) {
			return fmt.Errorf("%w: hard-link group extends beyond the selected inventory", ErrUnsupportedEntry)
		}
		if selectedCounts[entry.SourceObjectIdentity] > 1 && entry.HardLinkGroup == "" {
			return fmt.Errorf("%w: duplicate file identity lacks complete hard-link evidence", ErrUnsupportedEntry)
		}
	}
	return nil
}

func manifestTotals(entries []ManifestEntry) (ManifestTotals, uint64, error) {
	result := ManifestTotals{EntryCount: len(entries)}
	var maxFile uint64
	for _, entry := range entries {
		switch entry.Kind {
		case filesystemmeta.ObjectKindRegularFile:
			result.RegularFileCount++
			if entry.LogicalBytes > maxFile {
				maxFile = entry.LogicalBytes
			}
			value, err := addUint64(result.LogicalBytes, entry.LogicalBytes)
			if err != nil {
				return ManifestTotals{}, 0, err
			}
			result.LogicalBytes = value
		case filesystemmeta.ObjectKindDirectory:
			result.DirectoryCount++
		case filesystemmeta.ObjectKindSymlink:
			result.SymlinkCount++
		default:
			return ManifestTotals{}, 0, ErrUnsupportedEntry
		}
		if entry.StructuralOnly {
			result.StructuralCount++
		}
	}
	return result, maxFile, nil
}

func buildSpacePreflight(payload uint64, transport lane.BundleRecommendation, available uint64) (SpacePreflight, error) {
	safety := payload / spaceSafetyDivisor
	if safety < minimumSpaceSafetyBytes {
		safety = minimumSpaceSafetyBytes
	}
	temporary := safety
	if transport.SelectedMode == lane.TransportModeBundleSeed {
		if transport.EstimatedTemporaryBytes < 0 {
			return SpacePreflight{}, ErrMetadataOverflow
		}
		temporary = uint64(transport.EstimatedTemporaryBytes)
	}
	required, err := addUint64(payload, temporary)
	if err != nil {
		return SpacePreflight{}, err
	}
	result := SpacePreflight{
		PayloadBytes:            payload,
		TransportTemporaryBytes: temporary,
		SafetyMarginBytes:       safety,
		RequiredBytes:           required,
		PlannedAvailableBytes:   available,
		Sufficient:              available >= required,
	}
	if !result.Sufficient {
		return result, ErrInsufficientSpace
	}
	return result, nil
}

func addUint64(left, right uint64) (uint64, error) {
	if math.MaxUint64-left < right {
		return 0, ErrMetadataOverflow
	}
	return left + right, nil
}

func manifestSourceDigest(entries []ManifestEntry) (string, error) {
	payload, err := json.Marshal(struct {
		Schema  string          `json:"schema"`
		Entries []ManifestEntry `json:"entries"`
	}{Schema: "loom.digital_estate_source_digest.v1", Entries: entries})
	if err != nil {
		return "", err
	}
	return digestBytes(payload), nil
}

func manifestSemanticID(manifest MigrationManifest) (string, error) {
	manifest.ManifestID = ""
	manifest.ManifestDigest = ""
	payload, err := json.Marshal(manifest)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(append([]byte("migration-manifest-id-v1\x00"), payload...))
	return "migration-" + hex.EncodeToString(sum[:16]), nil
}

func manifestDigest(manifest MigrationManifest) (string, error) {
	manifest.ManifestDigest = ""
	payload, err := json.Marshal(manifest)
	if err != nil {
		return "", err
	}
	return digestBytes(payload), nil
}

func digestBytes(payload []byte) string {
	sum := sha256.Sum256(payload)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func digestText(value string) string { return digestBytes([]byte(value)) }

func validCanonicalDigest(value string) bool {
	return validDigest(value) && value == strings.ToLower(value)
}

// MarshalManifest emits one byte-stable representation after revalidating all
// identity-bearing fields. Invalid UTF-8 is rejected before encoding/json can
// normalize it to a replacement rune.
func MarshalManifest(manifest MigrationManifest) ([]byte, error) {
	if err := ValidateManifest(manifest); err != nil {
		return nil, err
	}
	payload, err := json.Marshal(manifest)
	if err != nil {
		return nil, fmt.Errorf("marshal migration manifest: %w", err)
	}
	if len(payload) > maxSerializedManifestBytes {
		return nil, budgetError("serialized migration manifest bytes", maxSerializedManifestBytes)
	}
	return append(payload, '\n'), nil
}

func UnmarshalManifest(payload []byte) (MigrationManifest, error) {
	if len(payload) > maxSerializedManifestBytes {
		return MigrationManifest{}, budgetError("serialized migration manifest bytes", maxSerializedManifestBytes)
	}
	if !utf8.Valid(payload) {
		return MigrationManifest{}, fmt.Errorf("%w: serialized manifest is not valid UTF-8", ErrInvalidManifest)
	}
	var manifest MigrationManifest
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return MigrationManifest{}, fmt.Errorf("%w: decode failed", ErrInvalidManifest)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return MigrationManifest{}, fmt.Errorf("%w: trailing JSON content", ErrInvalidManifest)
	}
	if err := ValidateManifest(manifest); err != nil {
		return MigrationManifest{}, err
	}
	return manifest, nil
}

func ValidateManifest(manifest MigrationManifest) error {
	if manifest.SchemaVersion != ManifestSchemaVersion || !validManifestText(manifest) {
		return ErrInvalidManifest
	}
	if !manifest.Destination.Domain.valid() || !validPathComponent(manifest.Destination.Name) || manifest.Destination.Mode&^uint32(0o777) != 0 || manifest.Destination.Mode&0o700 != 0o700 {
		return ErrInvalidManifest
	}
	expectedDestination, err := normalizeDestination(DestinationSpec{Domain: manifest.Destination.Domain, Name: manifest.Destination.Name, Mode: manifest.Destination.Mode})
	if err != nil || expectedDestination != manifest.Destination {
		return ErrInvalidManifest
	}
	if !validCanonicalDigest(manifest.InventoryDigest) || !validCanonicalDigest(manifest.SourceDigest) || !validCanonicalDigest(manifest.ManifestDigest) {
		return ErrInvalidManifest
	}
	if !reflect.DeepEqual(manifest.MetadataPolicy, strictPortableMetadataPolicy()) || !reflect.DeepEqual(manifest.AcceptanceGates, requiredMigrationGates()) || !reflect.DeepEqual(manifest.Rollback, rollbackInstructions(manifest.Destination)) {
		return ErrInvalidManifest
	}
	if len(manifest.Sources) == 0 || len(manifest.Sources) > absoluteMaxRoots || manifest.Bounds.MaxEntries <= 0 || manifest.Bounds.MaxEntries > absoluteMaxEntries || manifest.Bounds.MaxDepth <= 0 || manifest.Bounds.MaxDepth > absoluteMaxDepth {
		return ErrInvalidManifest
	}
	seenRoots := map[string]struct{}{}
	sourcesByID := map[string]ManifestSource{}
	lastRoot := ""
	for index, source := range manifest.Sources {
		if !rootIDPattern(source.RootID) || source.Locator != rootLocator(source.RootID, ".") || source.Domain != manifest.Destination.Domain || !validCanonicalDigest(source.PolicyFingerprint) || !validRootSummary(source.InventorySummary) {
			return ErrInvalidManifest
		}
		if !validMetadataPostureText(source.RootMetadata) {
			return ErrInvalidManifest
		}
		if index > 0 && source.RootID <= lastRoot {
			return ErrInvalidManifest
		}
		lastRoot = source.RootID
		if _, exists := seenRoots[source.RootID]; exists {
			return ErrInvalidManifest
		}
		seenRoots[source.RootID] = struct{}{}
		sourcesByID[source.RootID] = source
		if _, err := decodeEncodedPath(source.DestinationPrefix, true); err != nil {
			return ErrInvalidManifest
		}
		if strings.Contains(source.DestinationPrefix, "/") {
			return ErrInvalidManifest
		}
	}
	if len(manifest.Sources) > 1 {
		seenPrefixes := map[string]struct{}{}
		for _, source := range manifest.Sources {
			if source.DestinationPrefix == "" {
				return ErrInvalidManifest
			}
			if _, exists := seenPrefixes[source.DestinationPrefix]; exists {
				return ErrInvalidManifest
			}
			seenPrefixes[source.DestinationPrefix] = struct{}{}
		}
	}
	seenTargets := map[string]struct{}{}
	seenSources := map[string]struct{}{}
	selectedCounts := map[string]int{}
	structuralCounts := map[string]int{}
	selectedBytes := map[string]uint64{}
	linkCounts := map[string]int{}
	lastTarget := ""
	for index, entry := range manifest.Entries {
		if entry.TargetPath == "" || !filesystemmeta.ValidObjectKind(entry.Kind) || entry.LogicalBytes > manifest.Bounds.MaxFileBytes && entry.Kind == filesystemmeta.ObjectKindRegularFile {
			return ErrInvalidManifest
		}
		if _, err := decodeEncodedPath(entry.TargetPath, false); err != nil {
			return ErrInvalidManifest
		}
		if index > 0 && entry.TargetPath <= lastTarget {
			return ErrInvalidManifest
		}
		lastTarget = entry.TargetPath
		if _, exists := seenTargets[entry.TargetPath]; exists {
			return ErrInvalidManifest
		}
		seenTargets[entry.TargetPath] = struct{}{}
		if _, exists := seenSources[entry.SourceLocator]; exists {
			return ErrInvalidManifest
		}
		seenSources[entry.SourceLocator] = struct{}{}
		rootID, sourceRelative, err := splitRootLocator(entry.SourceLocator)
		if err != nil {
			return ErrInvalidManifest
		}
		source, selected := sourcesByID[rootID]
		if !selected || entry.TargetPath != joinEncodedPath(source.DestinationPrefix, sourceRelative) {
			return ErrInvalidManifest
		}
		if entry.StructuralOnly {
			structuralCounts[rootID]++
		} else {
			selectedCounts[rootID]++
			var addErr error
			selectedBytes[rootID], addErr = addUint64(selectedBytes[rootID], entry.LogicalBytes)
			if addErr != nil {
				return ErrInvalidManifest
			}
		}
		switch entry.Kind {
		case filesystemmeta.ObjectKindRegularFile:
			if !validCanonicalDigest(entry.ContentDigest) || !validCanonicalDigest(entry.SourceObjectIdentity) || entry.SourceLinkCount == 0 || entry.SourceSymlinkTarget != "" || entry.TargetSymlinkPath != "" || entry.Mode != entry.Metadata.Mode || entry.Mode&^uint32(0o777) != 0 || entry.Metadata.Executable != (entry.Mode&0o111 != 0) {
				return ErrInvalidManifest
			}
			if entry.HardLinkGroup != "" && entry.HardLinkGroup != entry.SourceObjectIdentity {
				return ErrInvalidManifest
			}
			linkCounts[entry.SourceObjectIdentity]++
		case filesystemmeta.ObjectKindDirectory:
			if entry.LogicalBytes != 0 || entry.ContentDigest != "" || entry.SourceObjectIdentity != "" || entry.SourceLinkCount != 0 || entry.HardLinkGroup != "" || entry.SourceSymlinkTarget != "" || entry.TargetSymlinkPath != "" || entry.Mode != entry.Metadata.Mode || entry.Mode&^uint32(0o777) != 0 || entry.Metadata.Executable != (entry.Mode&0o111 != 0) {
				return ErrInvalidManifest
			}
		case filesystemmeta.ObjectKindSymlink:
			if entry.ContentDigest != "" || entry.SourceObjectIdentity != "" || entry.SourceLinkCount != 0 || entry.HardLinkGroup != "" || entry.SourceSymlinkTarget == "" || entry.TargetSymlinkPath == "" || entry.Mode != 0 {
				return ErrInvalidManifest
			}
			targetRootID, targetRelative, err := splitRootLocator(entry.SourceSymlinkTarget)
			if err != nil {
				return ErrInvalidManifest
			}
			targetSource, present := sourcesByID[targetRootID]
			if !present || entry.TargetSymlinkPath != joinEncodedPath(targetSource.DestinationPrefix, targetRelative) {
				return ErrInvalidManifest
			}
			if _, err := decodeEncodedPath(entry.TargetSymlinkPath, true); err != nil {
				return ErrInvalidManifest
			}
		default:
			return ErrInvalidManifest
		}
		if err := validateStrictMetadata(Entry{Kind: entry.Kind, ObjectIdentity: entry.SourceObjectIdentity, LogicalBytes: entry.LogicalBytes, AllocatedBytes: uint64Pointer(0), LinkCount: uint64Pointer(1), SymlinkTarget: entry.SourceSymlinkTarget, Metadata: entry.Metadata}); err != nil {
			return ErrInvalidManifest
		}
	}
	if err := validateTargetHierarchy(manifest.Entries, manifest.Bounds.MaxDepth); err != nil {
		return ErrInvalidManifest
	}
	for _, source := range manifest.Sources {
		if selectedCounts[source.RootID] != source.SelectedEntryCount || structuralCounts[source.RootID] != source.StructuralEntryCount || selectedBytes[source.RootID] != source.SelectedLogicalBytes || source.InventorySummary.IncludedCount != source.SelectedEntryCount+1 {
			return ErrInvalidManifest
		}
	}
	for _, entry := range manifest.Entries {
		if entry.Kind != filesystemmeta.ObjectKindRegularFile {
			if entry.Kind == filesystemmeta.ObjectKindSymlink {
				if _, published := seenSources[entry.SourceSymlinkTarget]; !published {
					return ErrInvalidManifest
				}
			}
			continue
		}
		count := linkCounts[entry.SourceObjectIdentity]
		if count <= 0 || entry.SourceLinkCount != uint64(count) || count == 1 && entry.HardLinkGroup != "" || count > 1 && entry.HardLinkGroup != entry.SourceObjectIdentity {
			return ErrInvalidManifest
		}
	}
	if len(manifest.Entries) > manifest.Bounds.MaxEntries {
		return ErrInvalidManifest
	}
	totals, maxFile, err := manifestTotals(manifest.Entries)
	if err != nil || totals != manifest.Totals || maxFile != manifest.Bounds.MaxFileBytes || manifest.Bounds.MaxPayloadBytes != totals.LogicalBytes {
		return ErrInvalidManifest
	}
	sourceDigest, err := manifestSourceDigest(manifest.Entries)
	if err != nil || sourceDigest != manifest.SourceDigest {
		return ErrInvalidManifest
	}
	if manifest.Transport.SchemaVersion != lane.BundlePlanSchemaVersion || manifest.Transport.SelectedMode != lane.TransportModeFileTree && manifest.Transport.SelectedMode != lane.TransportModeBundleSeed {
		return ErrInvalidManifest
	}
	expectedTransport, err := lane.SelectBundleTransport(lane.TransferPlan{FileCount: totals.RegularFileCount, TotalBytes: int64(totals.LogicalBytes)}, manifest.Transport.RequestedMode)
	if err != nil || !reflect.DeepEqual(expectedTransport, manifest.Transport) {
		return ErrInvalidManifest
	}
	expectedSpace, err := buildSpacePreflight(totals.LogicalBytes, manifest.Transport, manifest.Space.PlannedAvailableBytes)
	if err != nil || expectedSpace != manifest.Space {
		return ErrInvalidManifest
	}
	expectedID, err := manifestSemanticID(manifest)
	if err != nil || expectedID != manifest.ManifestID {
		return ErrInvalidManifest
	}
	expectedDigest, err := manifestDigest(manifest)
	if err != nil || expectedDigest != manifest.ManifestDigest {
		return ErrInvalidManifest
	}
	return nil
}

func uint64Pointer(value uint64) *uint64 { return &value }

func validRootSummary(summary RootSummary) bool {
	counts := []int{
		summary.EntryCount,
		summary.IncludedCount,
		summary.IgnoredCount,
		summary.RegularFileCount,
		summary.DirectoryCount,
		summary.SymlinkCount,
		summary.HardLinkCount,
		summary.SpecialFileCount,
		summary.MetadataRiskCount,
		summary.MetadataUnknownCount,
		summary.RepositoryCount,
		summary.Accounting.UnknownAllocationCount,
	}
	for _, count := range counts {
		if count < 0 || count > summary.EntryCount {
			return false
		}
	}
	if summary.IncludedCount+summary.IgnoredCount != summary.EntryCount || summary.RegularFileCount+summary.DirectoryCount+summary.SymlinkCount+summary.SpecialFileCount != summary.EntryCount {
		return false
	}
	accounting := summary.Accounting
	return accounting.IncludedLogicalBytes <= accounting.LogicalBytes &&
		accounting.IncludedAllocatedBytes <= accounting.AllocatedBytes &&
		accounting.UniqueAllocatedBytes <= accounting.AllocatedBytes &&
		accounting.DuplicateAllocatedBytes <= accounting.AllocatedBytes &&
		accounting.IncludedUniqueAllocatedBytes <= accounting.IncludedAllocatedBytes &&
		accounting.IncludedDuplicateAllocatedBytes <= accounting.IncludedAllocatedBytes
}

func validateInventoryForPlan(inventory Inventory) error {
	if inventory.SchemaVersion != InventorySchemaVersion || !validCanonicalDigest(inventory.Digest) || !validInventoryText(inventory) {
		return fmt.Errorf("%w: inventory envelope is invalid", ErrInvalidManifest)
	}
	normalized, err := normalizeBounds(inventory.Bounds)
	if err != nil || normalized != inventory.Bounds || len(inventory.Roots) == 0 || len(inventory.Roots) > normalized.MaxRoots {
		return fmt.Errorf("%w: inventory bounds are invalid", ErrInvalidManifest)
	}
	repositoryCounts := map[string]int{}
	lastRepository := ""
	for index, repository := range inventory.Repositories {
		if index > 0 && repository.Locator <= lastRepository {
			return ErrInvalidManifest
		}
		lastRepository = repository.Locator
		rootID, _, splitErr := splitRootLocator(repository.Locator)
		if splitErr != nil {
			return ErrInvalidManifest
		}
		repositoryCounts[rootID]++
	}
	allEntries := []Entry{}
	lastRoot := ""
	seenRoots := map[string]struct{}{}
	for rootIndex, root := range inventory.Roots {
		if !rootIDPattern(root.ID) || !root.Domain.valid() || root.Locator != rootLocator(root.ID, ".") || !validCanonicalDigest(root.PolicyFingerprint) || rootIndex > 0 && root.ID <= lastRoot {
			return ErrInvalidManifest
		}
		lastRoot = root.ID
		seenRoots[root.ID] = struct{}{}
		lastLocator := ""
		seenLocators := map[string]struct{}{}
		for entryIndex, entry := range root.Entries {
			if entry.Domain != root.Domain || entryIndex > 0 && entry.Locator <= lastLocator {
				return ErrInvalidManifest
			}
			lastLocator = entry.Locator
			if _, exists := seenLocators[entry.Locator]; exists {
				return ErrInvalidManifest
			}
			seenLocators[entry.Locator] = struct{}{}
			relative, splitErr := rootRelativeLocator(root.ID, entry.Locator)
			if splitErr != nil || !filesystemmeta.ValidObjectKind(entry.Kind) {
				return ErrInvalidManifest
			}
			if entry.ObjectIdentity != "" && !validCanonicalDigest(entry.ObjectIdentity) {
				return ErrInvalidManifest
			}
			if entry.Kind == filesystemmeta.ObjectKindSymlink {
				if entry.SymlinkTarget == "" || !strings.HasPrefix(entry.SymlinkTarget, "root://") && !strings.HasPrefix(entry.SymlinkTarget, "external://sha256:") {
					return ErrInvalidManifest
				}
			} else if entry.SymlinkTarget != "" {
				return ErrInvalidManifest
			}
			if isEncodedGitPath(relative) && !entry.Ignore.Included {
				return fmt.Errorf("%w: Git metadata was excluded", ErrInvalidManifest)
			}
		}
		expectedSummary, sumErr := summarizeRoot(root.Entries)
		if sumErr != nil {
			return sumErr
		}
		expectedSummary.RepositoryCount = repositoryCounts[root.ID]
		if expectedSummary != root.Summary {
			return fmt.Errorf("%w: root summary does not match complete entries", ErrInvalidManifest)
		}
		allEntries = append(allEntries, root.Entries...)
	}
	for rootID := range repositoryCounts {
		if _, exists := seenRoots[rootID]; !exists {
			return ErrInvalidManifest
		}
	}
	totals, err := calculateAccounting(allEntries)
	if err != nil || totals != inventory.Totals {
		return fmt.Errorf("%w: inventory totals do not match complete entries", ErrInvalidManifest)
	}
	duplicates, err := duplicateObjects(allEntries)
	if err != nil || !reflect.DeepEqual(duplicates, inventory.DuplicateObjects) {
		return fmt.Errorf("%w: inventory duplicate accounting does not match", ErrInvalidManifest)
	}
	for index, overlap := range inventory.Overlaps {
		if _, ok := seenRoots[overlap.AncestorRootID]; !ok {
			return ErrInvalidManifest
		}
		if _, ok := seenRoots[overlap.DescendantRootID]; !ok || overlap.AncestorRootID == overlap.DescendantRootID {
			return ErrInvalidManifest
		}
		if index > 0 {
			previous := inventory.Overlaps[index-1]
			if overlap.AncestorRootID < previous.AncestorRootID || overlap.AncestorRootID == previous.AncestorRootID && overlap.DescendantRootID <= previous.DescendantRootID {
				return ErrInvalidManifest
			}
		}
	}
	digest, err := inventoryDigest(inventory)
	if err != nil || digest != inventory.Digest {
		return fmt.Errorf("%w: inventory digest mismatch", ErrInvalidManifest)
	}
	return nil
}

func validInventoryText(inventory Inventory) bool {
	for _, root := range inventory.Roots {
		if !utf8.ValidString(root.ID) || !utf8.ValidString(root.Locator) || !utf8.ValidString(string(root.Domain)) || !utf8.ValidString(root.PolicyFingerprint) {
			return false
		}
		for _, entry := range root.Entries {
			if !utf8.ValidString(entry.Locator) || !utf8.ValidString(entry.Kind) || !utf8.ValidString(entry.ObjectIdentity) || !utf8.ValidString(entry.SymlinkTarget) || !validMetadataPostureText(entry.Metadata) || !validIgnoreDecisionText(entry.Ignore) {
				return false
			}
		}
	}
	for _, repository := range inventory.Repositories {
		if !utf8.ValidString(repository.Locator) || !utf8.ValidString(repository.IdentityDigest) || !utf8.ValidString(repository.Head.Commit) || !utf8.ValidString(repository.Head.Branch) {
			return false
		}
		for _, value := range repository.RemoteDigests {
			if !utf8.ValidString(value) {
				return false
			}
		}
		for _, value := range repository.Untracked {
			if !utf8.ValidString(value) {
				return false
			}
		}
		for _, ref := range repository.Refs {
			if !utf8.ValidString(ref.Name) || !utf8.ValidString(ref.Object) {
				return false
			}
		}
		for _, module := range repository.Submodules {
			if !utf8.ValidString(module.Path) || !utf8.ValidString(module.Commit) || !utf8.ValidString(module.State) {
				return false
			}
		}
		for _, worktree := range repository.Worktrees {
			if !utf8.ValidString(worktree.Locator) || !utf8.ValidString(worktree.Head) || !utf8.ValidString(worktree.Branch) {
				return false
			}
		}
	}
	return true
}

func validManifestText(manifest MigrationManifest) bool {
	values := []string{
		manifest.SchemaVersion,
		manifest.ManifestID,
		manifest.ManifestDigest,
		manifest.InventoryDigest,
		manifest.SourceDigest,
		string(manifest.Destination.Domain),
		manifest.Destination.Name,
		manifest.Destination.Locator,
		manifest.Destination.IdentityDigest,
		manifest.MetadataPolicy.Name,
		manifest.MetadataPolicy.RegularContent,
		manifest.MetadataPolicy.PermissionBits,
		manifest.MetadataPolicy.SafeRelativeSymlinks,
		manifest.MetadataPolicy.HardLinks,
		manifest.MetadataPolicy.ExtendedMetadata,
		manifest.MetadataPolicy.SparseFiles,
		manifest.MetadataPolicy.PackagesAndSpecialFiles,
		manifest.Transport.SchemaVersion,
		string(manifest.Transport.RequestedMode),
		string(manifest.Transport.RecommendedMode),
		string(manifest.Transport.SelectedMode),
		manifest.Transport.ReasonCode,
		manifest.Transport.Reason,
		manifest.Transport.ForceWarning,
	}
	for _, value := range values {
		if !utf8.ValidString(value) {
			return false
		}
	}
	for _, source := range manifest.Sources {
		if !utf8.ValidString(source.RootID) || !utf8.ValidString(source.Locator) || !utf8.ValidString(string(source.Domain)) || !utf8.ValidString(source.DestinationPrefix) || !utf8.ValidString(source.PolicyFingerprint) || !validMetadataPostureText(source.RootMetadata) {
			return false
		}
	}
	for _, entry := range manifest.Entries {
		if !utf8.ValidString(entry.SourceLocator) || !utf8.ValidString(entry.TargetPath) || !utf8.ValidString(entry.Kind) || !utf8.ValidString(entry.ContentDigest) || !utf8.ValidString(entry.SourceObjectIdentity) || !utf8.ValidString(entry.HardLinkGroup) || !utf8.ValidString(entry.SourceSymlinkTarget) || !utf8.ValidString(entry.TargetSymlinkPath) || !validMetadataPostureText(entry.Metadata) || !validIgnoreDecisionText(entry.Ignore) {
			return false
		}
	}
	for _, value := range manifest.AcceptanceGates {
		if !utf8.ValidString(value) {
			return false
		}
	}
	for _, instruction := range manifest.Rollback {
		if !utf8.ValidString(instruction.Phase) || !utf8.ValidString(instruction.Action) || !utf8.ValidString(instruction.Target) {
			return false
		}
		for _, condition := range instruction.RequiredConditions {
			if !utf8.ValidString(condition) {
				return false
			}
		}
	}
	return true
}

func validMetadataPostureText(metadata MetadataPosture) bool {
	if !utf8.ValidString(metadata.PackageKind) {
		return false
	}
	for index, value := range metadata.XattrNames {
		if !utf8.ValidString(value) {
			return false
		}
		if index > 0 && value <= metadata.XattrNames[index-1] {
			return false
		}
	}
	for index, value := range metadata.Risks {
		if !utf8.ValidString(value) {
			return false
		}
		if index > 0 && value <= metadata.Risks[index-1] {
			return false
		}
	}
	return true
}

func validIgnoreDecisionText(decision IgnoreDecision) bool {
	return utf8.ValidString(string(decision.Profile)) && utf8.ValidString(string(decision.RuleCategory)) && utf8.ValidString(decision.Pattern) && utf8.ValidString(decision.PolicyVersion) && utf8.ValidString(decision.Source)
}

func validPathComponent(value string) bool {
	return utf8.ValidString(value) && value != "" && value != "." && value != ".." && !strings.ContainsRune(value, 0) && !strings.Contains(value, "/")
}

func encodeInputRelativePath(value string, allowEmpty bool) (string, error) {
	if !utf8.ValidString(value) || strings.ContainsRune(value, 0) || strings.HasPrefix(value, "/") || strings.HasSuffix(value, "/") {
		return "", fmt.Errorf("%w: destination prefix is invalid", ErrInvalidManifest)
	}
	if value == "" {
		if allowEmpty {
			return "", nil
		}
		return "", ErrInvalidManifest
	}
	parts := strings.Split(value, "/")
	encoded := make([]string, len(parts))
	for index, part := range parts {
		if !validPathComponent(part) {
			return "", fmt.Errorf("%w: destination prefix is invalid", ErrInvalidManifest)
		}
		encoded[index] = url.PathEscape(part)
	}
	return strings.Join(encoded, "/"), nil
}

func decodeEncodedPath(value string, allowEmpty bool) (string, error) {
	if value == "" {
		if allowEmpty {
			return "", nil
		}
		return "", ErrInvalidManifest
	}
	parts := strings.Split(value, "/")
	decoded := make([]string, len(parts))
	for index, part := range parts {
		if part == "" {
			return "", ErrInvalidManifest
		}
		plain, err := url.PathUnescape(part)
		if err != nil || plain == "" || plain == "." || plain == ".." || strings.ContainsRune(plain, 0) || strings.Contains(plain, "/") || url.PathEscape(plain) != part {
			return "", ErrInvalidManifest
		}
		decoded[index] = plain
	}
	return strings.Join(decoded, "/"), nil
}

func splitRootLocator(locator string) (string, string, error) {
	if !strings.HasPrefix(locator, "root://") {
		return "", "", ErrInvalidManifest
	}
	remainder := strings.TrimPrefix(locator, "root://")
	rootID := remainder
	relative := ""
	if slash := strings.IndexByte(remainder, '/'); slash >= 0 {
		rootID = remainder[:slash]
		relative = remainder[slash+1:]
	}
	if !rootIDPattern(rootID) {
		return "", "", ErrInvalidManifest
	}
	if _, err := decodeEncodedPath(relative, true); err != nil {
		return "", "", err
	}
	return rootID, relative, nil
}

func rootRelativeLocator(rootID, locator string) (string, error) {
	observedRoot, relative, err := splitRootLocator(locator)
	if err != nil || observedRoot != rootID {
		return "", ErrInvalidManifest
	}
	return relative, nil
}

func joinEncodedPath(prefix, relative string) string {
	if prefix == "" {
		return relative
	}
	if relative == "" {
		return prefix
	}
	return prefix + "/" + relative
}

func encodedParent(value string) string {
	if value == "" || !strings.Contains(value, "/") {
		return ""
	}
	return path.Dir(value)
}

func isEncodedGitPath(value string) bool {
	if value == "" {
		return false
	}
	for _, encoded := range strings.Split(value, "/") {
		plain, err := url.PathUnescape(encoded)
		if err == nil && plain == ".git" {
			return true
		}
	}
	return false
}
