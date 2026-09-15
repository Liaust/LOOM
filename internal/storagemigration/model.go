package storagemigration

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

const (
	ManifestSchemaVersion    = "loom.canonical-filesystem-migration.v1"
	ManifestSetSchemaVersion = "loom.canonical-filesystem-migration-set.v1"
)

type RootMapping struct {
	Name            string `json:"name"`
	OldRoot         string `json:"old_root"`
	NewRoot         string `json:"new_root"`
	OldFilesystemID string `json:"old_filesystem_id,omitempty"`
	NewFilesystemID string `json:"new_filesystem_id,omitempty"`
}

type FileEvidence struct {
	Path         string    `json:"path"`
	ObjectType   string    `json:"object_type"`
	SizeBytes    int64     `json:"size_bytes"`
	Mode         uint32    `json:"mode"`
	DeviceID     uint64    `json:"device_id"`
	Inode        uint64    `json:"inode"`
	ModifiedAt   time.Time `json:"modified_at"`
	ChecksumType string    `json:"checksum_type,omitempty"`
	ChecksumHex  string    `json:"checksum_hex,omitempty"`
}

type RollbackAction struct {
	PhysicalRefID    string `json:"physical_ref_id,omitempty"`
	StorageEntryID   string `json:"storage_entry_id"`
	ExpectedURI      string `json:"expected_uri,omitempty"`
	RestoreURI       string `json:"restore_uri,omitempty"`
	ExpectedOriginal string `json:"expected_original_source_path,omitempty"`
	RestoreOriginal  string `json:"restore_original_source_path,omitempty"`
	ExpectedView     string `json:"expected_current_view_path,omitempty"`
	RestoreView      string `json:"restore_current_view_path,omitempty"`
}

type EntryPathEvidence struct {
	Field    string       `json:"field"`
	RootName string       `json:"root_name"`
	OldPath  string       `json:"old_path"`
	NewPath  string       `json:"new_path"`
	Expected FileEvidence `json:"expected"`
}

type PathAction struct {
	ActionID       string              `json:"action_id"`
	RootName       string              `json:"root_name"`
	StorageEntryID string              `json:"storage_entry_id"`
	PhysicalRefID  string              `json:"physical_ref_id,omitempty"`
	RefKind        string              `json:"ref_kind,omitempty"`
	RefClass       string              `json:"ref_class,omitempty"`
	OldURI         string              `json:"old_uri,omitempty"`
	NewURI         string              `json:"new_uri,omitempty"`
	OldOriginal    string              `json:"old_original_source_path,omitempty"`
	NewOriginal    string              `json:"new_original_source_path,omitempty"`
	OldView        string              `json:"old_current_view_path,omitempty"`
	NewView        string              `json:"new_current_view_path,omitempty"`
	Expected       FileEvidence        `json:"expected"`
	EntryEvidence  []EntryPathEvidence `json:"entry_evidence,omitempty"`
	Rollback       RollbackAction      `json:"rollback"`
}

type Conflict struct {
	Code    string `json:"code"`
	Path    string `json:"path,omitempty"`
	EntryID string `json:"storage_entry_id,omitempty"`
	RefID   string `json:"physical_ref_id,omitempty"`
	Detail  string `json:"detail"`
}

// RootInventoryEvidence summarizes the no-follow physical/catalog
// reconciliation performed before migration actions are generated. Exact
// blocking paths remain in Conflicts so an operator can review and reconcile
// them without auto-registering or discarding custody.
type RootInventoryEvidence struct {
	RootName              string `json:"root_name"`
	OldRoot               string `json:"old_root"`
	DirectoryCount        int    `json:"directory_count"`
	RegularFileCount      int    `json:"regular_file_count"`
	CatalogRefCount       int    `json:"catalog_ref_count"`
	CatalogPathCount      int    `json:"catalog_path_count"`
	MatchedFileCount      int    `json:"matched_file_count"`
	InternalMarkerCount   int    `json:"internal_marker_count"`
	UntrackedFileCount    int    `json:"untracked_file_count"`
	MissingCatalogCount   int    `json:"missing_catalog_count"`
	DuplicateCatalogCount int    `json:"duplicate_catalog_count"`
	SymlinkCount          int    `json:"symlink_count"`
	SpecialFileCount      int    `json:"special_file_count"`
	InventoryErrorCount   int    `json:"inventory_error_count"`
	StructureDigest       string `json:"structure_digest"`
}

type Review struct {
	ReviewedBy string     `json:"reviewed_by,omitempty"`
	ReviewedAt *time.Time `json:"reviewed_at,omitempty"`
}

type Manifest struct {
	SchemaVersion     string                  `json:"schema_version"`
	ManifestHash      string                  `json:"manifest_hash"`
	GeneratedAt       time.Time               `json:"generated_at"`
	NodeID            string                  `json:"node_id"`
	LayoutFingerprint string                  `json:"layout_fingerprint"`
	Roots             []RootMapping           `json:"roots"`
	RootInventory     []RootInventoryEvidence `json:"root_inventory"`
	Actions           []PathAction            `json:"actions"`
	Conflicts         []Conflict              `json:"conflicts,omitempty"`
	Review            Review                  `json:"review"`
}

// ManifestBatch describes one independently verified bounded child of a
// larger migration artifact. All children in the reviewed set are applied in
// one catalog transaction.
type ManifestBatch struct {
	File          string `json:"file"`
	ManifestHash  string `json:"manifest_hash"`
	EncodedBytes  int64  `json:"encoded_bytes"`
	ActionCount   int    `json:"action_count"`
	ConflictCount int    `json:"conflict_count"`
}

// ManifestSet is the bounded, reviewable index for a production-shaped
// migration. Review is attached to the set so an operator does not have to
// edit thousands of child manifests; each child remains integrity checked.
type ManifestSet struct {
	SchemaVersion     string                  `json:"schema_version"`
	ManifestSetHash   string                  `json:"manifest_set_hash"`
	GeneratedAt       time.Time               `json:"generated_at"`
	NodeID            string                  `json:"node_id"`
	LayoutFingerprint string                  `json:"layout_fingerprint"`
	Roots             []RootMapping           `json:"roots"`
	RootInventory     []RootInventoryEvidence `json:"root_inventory"`
	Batches           []ManifestBatch         `json:"batches"`
	ActionCount       int                     `json:"action_count"`
	ConflictCount     int                     `json:"conflict_count"`
	Review            Review                  `json:"review"`
}

func LayoutFingerprint(nodeID string, roots map[string]string) string {
	keys := make([]string, 0, len(roots))
	for key := range roots {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	hash := sha256.New()
	fmt.Fprintf(hash, "node=%s\n", strings.TrimSpace(nodeID))
	for _, key := range keys {
		fmt.Fprintf(hash, "%s=%s\n", key, roots[key])
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func RefreshManifestHash(manifest *Manifest) error {
	if manifest == nil {
		return fmt.Errorf("manifest is required")
	}
	clone := *manifest
	clone.ManifestHash = ""
	clone.Review = Review{}
	digest, err := hashJSON(clone)
	if err != nil {
		return err
	}
	manifest.ManifestHash = digest
	return nil
}

func ValidateManifestHash(manifest Manifest) error {
	want := manifest.ManifestHash
	if strings.TrimSpace(want) == "" {
		return fmt.Errorf("manifest hash is required")
	}
	if err := RefreshManifestHash(&manifest); err != nil {
		return err
	}
	if manifest.ManifestHash != want {
		return fmt.Errorf("manifest integrity check failed")
	}
	return nil
}

func RefreshManifestSetHash(set *ManifestSet) error {
	if set == nil {
		return fmt.Errorf("manifest set is required")
	}
	clone := *set
	clone.ManifestSetHash = ""
	clone.Review = Review{}
	digest, err := hashJSON(clone)
	if err != nil {
		return err
	}
	set.ManifestSetHash = digest
	return nil
}

// hashJSON streams the canonical Go JSON encoding into the digest. Migration
// plans can contain hundreds of thousands of actions, so integrity hashing
// must not allocate a second manifest-sized byte slice.
func hashJSON(value any) (string, error) {
	hash := sha256.New()
	if err := json.NewEncoder(hash).Encode(value); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func ValidateManifestSetHash(set ManifestSet) error {
	want := set.ManifestSetHash
	if strings.TrimSpace(want) == "" {
		return fmt.Errorf("manifest set hash is required")
	}
	if err := RefreshManifestSetHash(&set); err != nil {
		return err
	}
	if set.ManifestSetHash != want {
		return fmt.Errorf("manifest set integrity check failed")
	}
	return nil
}

// SplitManifest partitions a migration into deterministic, independently
// verifiable children that are applied as one set. Both record count and
// encoded size are bounded so a dry-run artifact cannot create a child that
// its reader refuses to load.
func SplitManifest(source Manifest, maxRecords, maxEncodedBytes int) (ManifestSet, []Manifest, error) {
	if maxRecords <= 0 || maxEncodedBytes <= 0 {
		return ManifestSet{}, nil, fmt.Errorf("positive manifest batch limits are required")
	}
	if err := ValidateManifestHash(source); err != nil {
		return ManifestSet{}, nil, err
	}
	newBatch := func() Manifest {
		return Manifest{
			SchemaVersion:     ManifestSchemaVersion,
			GeneratedAt:       source.GeneratedAt,
			NodeID:            source.NodeID,
			LayoutFingerprint: source.LayoutFingerprint,
			Roots:             append([]RootMapping(nil), source.Roots...),
			RootInventory:     append([]RootInventoryEvidence(nil), source.RootInventory...),
		}
	}
	type record struct {
		action   *PathAction
		conflict *Conflict
	}
	type unit []record
	actionsByEntry := map[string]unit{}
	for index := range source.Actions {
		action := &source.Actions[index]
		actionsByEntry[action.StorageEntryID] = append(actionsByEntry[action.StorageEntryID], record{action: action})
	}
	entryIDs := make([]string, 0, len(actionsByEntry))
	for entryID := range actionsByEntry {
		entryIDs = append(entryIDs, entryID)
	}
	sort.Strings(entryIDs)
	units := make([]unit, 0, len(entryIDs)+len(source.Conflicts))
	for _, entryID := range entryIDs {
		group := actionsByEntry[entryID]
		if len(group) > maxRecords {
			return ManifestSet{}, nil, fmt.Errorf("storage entry %s requires %d records, exceeding the %d-record transactional batch limit", entryID, len(group), maxRecords)
		}
		units = append(units, group)
	}
	for index := range source.Conflicts {
		units = append(units, unit{{conflict: &source.Conflicts[index]}})
	}
	buildBatch := func(units []unit) (Manifest, int, int, error) {
		batch := newBatch()
		records := 0
		for _, group := range units {
			for _, item := range group {
				if item.action != nil {
					batch.Actions = append(batch.Actions, *item.action)
				} else {
					batch.Conflicts = append(batch.Conflicts, *item.conflict)
				}
				records++
			}
		}
		if err := RefreshManifestHash(&batch); err != nil {
			return Manifest{}, 0, 0, err
		}
		payload, err := json.MarshalIndent(batch, "", "  ")
		if err != nil {
			return Manifest{}, 0, 0, err
		}
		return batch, records, len(payload) + 1, nil
	}
	var batches []Manifest
	var emit func([]unit) error
	emit = func(units []unit) error {
		recordCount := 0
		for _, group := range units {
			recordCount += len(group)
		}
		if recordCount > maxRecords && len(units) > 1 {
			middle := len(units) / 2
			if err := emit(units[:middle]); err != nil {
				return err
			}
			return emit(units[middle:])
		}
		batch, records, size, err := buildBatch(units)
		if err != nil {
			return err
		}
		if records <= maxRecords && size <= maxEncodedBytes {
			batches = append(batches, batch)
			return nil
		}
		if len(units) <= 1 {
			return fmt.Errorf("one storage-entry manifest unit requires %d records and %d bytes, exceeding batch limits", records, size)
		}
		middle := len(units) / 2
		if err := emit(units[:middle]); err != nil {
			return err
		}
		return emit(units[middle:])
	}
	if len(units) == 0 {
		if err := emit(nil); err != nil {
			return ManifestSet{}, nil, err
		}
	} else {
		if err := emit(units); err != nil {
			return ManifestSet{}, nil, err
		}
	}
	set := ManifestSet{
		SchemaVersion:     ManifestSetSchemaVersion,
		GeneratedAt:       source.GeneratedAt,
		NodeID:            source.NodeID,
		LayoutFingerprint: source.LayoutFingerprint,
		Roots:             append([]RootMapping(nil), source.Roots...),
		RootInventory:     append([]RootInventoryEvidence(nil), source.RootInventory...),
		ActionCount:       len(source.Actions),
		ConflictCount:     len(source.Conflicts),
	}
	for index := range batches {
		batch := &batches[index]
		payload, err := json.MarshalIndent(batch, "", "  ")
		if err != nil {
			return ManifestSet{}, nil, err
		}
		set.Batches = append(set.Batches, ManifestBatch{
			File:          fmt.Sprintf("batch-%06d.json", index+1),
			ManifestHash:  batch.ManifestHash,
			EncodedBytes:  int64(len(payload) + 1),
			ActionCount:   len(batch.Actions),
			ConflictCount: len(batch.Conflicts),
		})
	}
	if err := RefreshManifestSetHash(&set); err != nil {
		return ManifestSet{}, nil, err
	}
	return set, batches, nil
}
