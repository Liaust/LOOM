package maintenance

import (
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"loom.local/loom/internal/lane"
)

const ImportsCustodyLocationEvidenceSchema = "loom.imports.custody_locations.v1"

// ImportsCustodyLocationEvidence describes only the current retained layout.
// It is not a backup/cloud receipt or evidence that a partial archive is usable.
type ImportsCustodyLocationEvidence struct {
	Schema               string                        `json:"schema"`
	ImportsInventoryHash string                        `json:"imports_inventory_hash"`
	Batches              []ImportsCustodyLocationBatch `json:"batches"`
}

type ImportsCustodyLocationBatch struct {
	SourceNodeKey         string                         `json:"source_node_key"`
	BatchID               string                         `json:"batch_id"`
	AcceptedDate          string                         `json:"accepted_date"`
	Marker                ImportsBackupEvidenceEntry     `json:"marker"`
	ManifestInventoryHash string                         `json:"manifest_inventory_hash"`
	Members               []ImportsCustodyLocationMember `json:"members"`
}

type ImportsCustodyLocationMember struct {
	Original                  ImportsBackupEvidenceEntry       `json:"original"`
	StorageRelativePath       string                           `json:"storage_relative_path"`
	QualifiedCurrentDirectory *ImportsCustodyDirectoryMetadata `json:"qualified_current_directory,omitempty"`
}

type ImportsCustodyDirectoryMetadata struct {
	Mode       uint32    `json:"mode"`
	ModifiedAt time.Time `json:"modified_at"`
}

// InspectImportsCustodyLocations verifies CURRENT committed Lane members. The
// original and marker paths are Imports-relative; current paths are Storage-
// relative. Qualified directory metadata records only changes authorized by
// LaneCustodyDirectoryMutationVerifier, without altering original evidence.
// Callers must hold the custody-reader lease. Never substitute this live reader
// for ValidateImportsBackup or use its resolver to validate a frozen snapshot.
func InspectImportsCustodyLocations(ctx context.Context, importsRoot, storageRoot string, resolver lane.LaneCustodyMemberResolver) (ImportsCustodyLocationEvidence, error) {
	if err := ctx.Err(); err != nil {
		return ImportsCustodyLocationEvidence{}, err
	}
	for _, root := range []string{storageRoot, importsRoot} {
		directory, err := openImportsLocationsDirectory(root)
		if err != nil {
			return ImportsCustodyLocationEvidence{}, err
		}
		_ = directory.Close()
	}
	if _, err := importsLocationRelativePath(storageRoot, importsRoot); err != nil {
		return ImportsCustodyLocationEvidence{}, fmt.Errorf("Imports must be inside Storage: %w", err)
	}
	physical, err := buildImportsBackupEvidence(ctx, importsRoot, ImportsBackupPolicyCanonical)
	if err != nil {
		return ImportsCustodyLocationEvidence{}, err
	}
	markers := make(map[string]ImportsBackupEvidenceEntry)
	physicalByPath := make(map[string]ImportsBackupEvidenceEntry, len(physical.Entries))
	structural := make(map[string]bool)
	identities := make(map[[2]string]bool)
	destinations := make(map[string]string)
	for _, entry := range physical.Entries {
		physicalByPath[entry.RelativePath] = entry
		if path.Base(entry.RelativePath) != laneCustodyManifestName {
			continue
		}
		parts := strings.Split(entry.RelativePath, "/")
		if len(parts) != 4 || entry.Kind != "regular_file" {
			return ImportsCustodyLocationEvidence{}, fmt.Errorf("invalid Lane commit marker layout %q", entry.RelativePath)
		}
		if _, err := time.Parse("2006-01-02", parts[1]); err != nil {
			return ImportsCustodyLocationEvidence{}, fmt.Errorf("invalid Lane commit marker date %q", entry.RelativePath)
		}
		identity := [2]string{parts[0], parts[2]}
		if identities[identity] {
			return ImportsCustodyLocationEvidence{}, fmt.Errorf("duplicate original Lane batch identity %q/%q", identity[0], identity[1])
		}
		identities[identity] = true
		prefix := strings.Join(parts[:3], "/")
		markers[prefix] = entry
		for depth := 1; depth <= 3; depth++ {
			ancestor := strings.Join(parts[:depth], "/")
			structural[ancestor] = true
			destinations[filepath.Join(importsRoot, filepath.FromSlash(ancestor))] = "Imports structure " + ancestor
		}
		destinations[filepath.Join(importsRoot, filepath.FromSlash(entry.RelativePath))] = "Lane commit marker " + entry.RelativePath
	}
	for _, entry := range physical.Entries {
		parts := strings.Split(entry.RelativePath, "/")
		if len(parts) <= 3 {
			if entry.Kind != "directory" || !structural[entry.RelativePath] {
				return ImportsCustodyLocationEvidence{}, fmt.Errorf("Imports entry %q is outside a committed batch", entry.RelativePath)
			}
		} else if _, ok := markers[strings.Join(parts[:3], "/")]; !ok {
			return ImportsCustodyLocationEvidence{}, fmt.Errorf("Imports entry %q belongs to an uncommitted batch", entry.RelativePath)
		}
	}

	evidence := ImportsCustodyLocationEvidence{Schema: ImportsCustodyLocationEvidenceSchema, ImportsInventoryHash: physical.InventoryHash, Batches: []ImportsCustodyLocationBatch{}}
	accounted := make(map[string]bool, len(physical.Entries))
	for relative := range structural {
		accounted[relative] = true
	}
	// Physical entries are sorted, so marker discovery also fixes batch order.
	for _, marker := range physical.Entries {
		if path.Base(marker.RelativePath) != laneCustodyManifestName {
			continue
		}
		parts := strings.Split(marker.RelativePath, "/")
		prefix := strings.Join(parts[:3], "/")
		batchRoot := filepath.Join(importsRoot, filepath.FromSlash(prefix))
		bounded := &importsLocationsResolver{
			resolver: resolver, source: parts[0], batch: parts[2], prefix: prefix,
			batchRoot: batchRoot, storageRoot: storageRoot, destinations: destinations,
			qualified: make(map[string][2]time.Time),
		}
		manifest, paths, err := lane.VerifyCommittedCustody(ctx, batchRoot, parts[0], parts[2], parts[1], bounded)
		if err != nil {
			return ImportsCustodyLocationEvidence{}, fmt.Errorf("verify current Lane batch %q: %w", prefix, err)
		}
		batch := ImportsCustodyLocationBatch{
			SourceNodeKey: manifest.SourceNodeKey, BatchID: manifest.BatchID, AcceptedDate: manifest.AcceptedDate,
			Marker: marker, ManifestInventoryHash: manifest.InventoryHash, Members: []ImportsCustodyLocationMember{},
		}
		for _, entry := range manifest.Entries {
			actual := paths[entry.RelativePath]
			relative, err := importsLocationRelativePath(storageRoot, actual)
			if err != nil {
				return ImportsCustodyLocationEvidence{}, err
			}
			originalRelative, err := normalizeImportsEvidencePath(prefix + "/" + entry.RelativePath)
			if err != nil {
				return ImportsCustodyLocationEvidence{}, err
			}
			member := ImportsCustodyLocationMember{
				Original: ImportsBackupEvidenceEntry{
					RelativePath: originalRelative, Kind: entry.Kind, Mode: entry.Mode,
					ModifiedAt: entry.ModifiedAt, SizeBytes: entry.SizeBytes, SHA256: entry.SHA256, LinkTarget: entry.LinkTarget,
				},
				StorageRelativePath: relative,
			}
			if qualification, ok := bounded.qualified[entry.RelativePath]; ok {
				if entry.Kind != "directory" || !qualification[0].Equal(entry.ModifiedAt) {
					return ImportsCustodyLocationEvidence{}, fmt.Errorf("qualified Lane directory identity changed")
				}
				member.QualifiedCurrentDirectory = &ImportsCustodyDirectoryMetadata{Mode: entry.Mode, ModifiedAt: qualification[1]}
			}
			batch.Members = append(batch.Members, member)
			if physicalRelative, err := importsLocationRelativePath(importsRoot, actual); err == nil {
				got, ok := physicalByPath[physicalRelative]
				want := member.Original
				if member.QualifiedCurrentDirectory != nil {
					want.ModifiedAt = member.QualifiedCurrentDirectory.ModifiedAt
				}
				if !ok || got.Kind != want.Kind || got.Mode != want.Mode || !got.ModifiedAt.Equal(want.ModifiedAt) || got.SizeBytes != want.SizeBytes || got.SHA256 != want.SHA256 || got.LinkTarget != want.LinkTarget {
					return ImportsCustodyLocationEvidence{}, fmt.Errorf("Imports member %q changed during location inspection", physicalRelative)
				}
				accounted[physicalRelative] = true
			}
		}
		// Bind the marker bytes seen by the physical inventory to the manifest
		// just verified by Lane, rather than mixing two generations of evidence.
		markerPath := filepath.Join(importsRoot, filepath.FromSlash(marker.RelativePath))
		digest, size, err := hashImportsNoFollowRegular(ctx, markerPath)
		if err != nil {
			return ImportsCustodyLocationEvidence{}, err
		}
		info, err := os.Lstat(markerPath)
		if err != nil || !info.Mode().IsRegular() || digest != marker.SHA256 || size != marker.SizeBytes || uint32(info.Mode().Perm()) != marker.Mode || !info.ModTime().Equal(marker.ModifiedAt) {
			return ImportsCustodyLocationEvidence{}, fmt.Errorf("Lane commit marker %q changed during location inspection", marker.RelativePath)
		}
		accounted[marker.RelativePath] = true
		evidence.Batches = append(evidence.Batches, batch)
	}
	for _, entry := range physical.Entries {
		if !accounted[entry.RelativePath] {
			return ImportsCustodyLocationEvidence{}, fmt.Errorf("uncommitted remaining Imports entry %q", entry.RelativePath)
		}
	}
	return evidence, nil
}

type importsLocationsResolver struct {
	resolver               lane.LaneCustodyMemberResolver
	source, batch, prefix  string
	batchRoot, storageRoot string
	destinations           map[string]string
	qualified              map[string][2]time.Time
}

func (r *importsLocationsResolver) PrepareLaneCustodyResolution(ctx context.Context, source, batch string) (context.Context, error) {
	if source != r.source || batch != r.batch {
		return nil, fmt.Errorf("unexpected original Lane batch identity")
	}
	if preparer, ok := r.resolver.(lane.LaneCustodyResolutionPreparer); ok {
		return preparer.PrepareLaneCustodyResolution(ctx, source, batch)
	}
	return ctx, nil
}

func (r *importsLocationsResolver) ResolveLaneCustodyMember(ctx context.Context, source, batch, relative string) (string, error) {
	if source != r.source || batch != r.batch {
		return "", fmt.Errorf("unexpected original Lane batch identity")
	}
	actual := ""
	if r.resolver != nil {
		var err error
		actual, err = r.resolver.ResolveLaneCustodyMember(ctx, source, batch, relative)
		if err != nil {
			return "", err
		}
	}
	if actual == "" {
		actual = filepath.Join(r.batchRoot, filepath.FromSlash(relative))
	}
	if _, err := importsLocationRelativePath(r.storageRoot, actual); err != nil {
		return "", err
	}
	if owner, exists := r.destinations[actual]; exists {
		return "", fmt.Errorf("Lane current location collision %q with %q", actual, owner)
	}
	r.destinations[actual] = r.prefix + "/" + relative
	return actual, nil
}

func (r *importsLocationsResolver) VerifyLaneCustodyDirectoryMutation(ctx context.Context, source, batch, relative string, original, observed time.Time) error {
	verifier, ok := r.resolver.(lane.LaneCustodyDirectoryMutationVerifier)
	if !ok {
		return fmt.Errorf("Lane directory mutation requires an exact evidence verifier")
	}
	if err := verifier.VerifyLaneCustodyDirectoryMutation(ctx, source, batch, relative, original, observed); err != nil {
		return err
	}
	r.qualified[relative] = [2]time.Time{original, observed}
	return nil
}

func importsLocationRelativePath(root, actual string) (string, error) {
	if !filepath.IsAbs(actual) || filepath.Clean(actual) != actual || strings.ContainsRune(actual, '\x00') {
		return "", fmt.Errorf("current Lane location must be a clean absolute path: %q", actual)
	}
	relative, err := filepath.Rel(root, actual)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("current Lane location %q is outside Storage root %q", actual, root)
	}
	return normalizeImportsEvidencePath(filepath.ToSlash(relative))
}

func openImportsLocationsDirectory(root string) (*os.File, error) {
	if !filepath.IsAbs(root) || filepath.Clean(root) != root || root == string(filepath.Separator) || strings.ContainsRune(root, '\x00') {
		return nil, fmt.Errorf("current custody root must be a clean bounded absolute directory: %q", root)
	}
	fd, err := unix.Open(string(filepath.Separator), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	for _, component := range strings.Split(strings.TrimPrefix(root, string(filepath.Separator)), string(filepath.Separator)) {
		next, err := unix.Openat(fd, component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		_ = unix.Close(fd)
		if err != nil {
			return nil, fmt.Errorf("open current custody root without following links %q: %w", root, err)
		}
		fd = next
	}
	return os.NewFile(uintptr(fd), root), nil
}
