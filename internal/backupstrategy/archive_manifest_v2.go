package backupstrategy

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"loom.local/loom/internal/provenance"

	"loom.local/loom/internal/hermesprofile"
)

type DirectArchiveRequestV2 struct {
	HermesRecovery      []hermesprofile.Evidence        `json:"hermes_recovery,omitempty"`
	Schema              string                          `json:"schema"`
	VerificationProfile string                          `json:"verification_profile"`
	NodeID              string                          `json:"node_id"`
	ArchiveRef          string                          `json:"archive_ref"`
	ArchiveClass        string                          `json:"archive_class"`
	CreatedAt           time.Time                       `json:"created_at"`
	Roots               []DirectArchiveRoot             `json:"roots"`
	Exclusions          []DirectArchiveExclusion        `json:"exclusions"`
	OperationalPackage  DirectArchiveOperationalPackage `json:"operational_package"`
	ProvenancePackage   DirectArchiveProvenancePackage  `json:"provenance_package"`
}

type DirectArchiveManifestPrepareInputV2 struct {
	Request     DirectArchiveRequestV2
	Backend     string
	Repository  string
	ArchiveName string
}

type DirectArchiveManifestV2 struct {
	HermesRecovery      []hermesprofile.Evidence                  `json:"hermes_recovery,omitempty"`
	Schema              string                                    `json:"schema"`
	VerificationProfile string                                    `json:"verification_profile"`
	Backend             string                                    `json:"backend"`
	Repository          string                                    `json:"repository"`
	ArchiveName         string                                    `json:"archive_name"`
	NodeID              string                                    `json:"node_id"`
	ArchiveRef          string                                    `json:"archive_ref"`
	ArchiveClass        string                                    `json:"archive_class"`
	CreatedAt           time.Time                                 `json:"created_at"`
	ExclusionPolicy     string                                    `json:"exclusion_policy"`
	Exclusions          []DirectArchiveExclusion                  `json:"exclusions"`
	Roots               []DirectArchiveRootManifestV2             `json:"roots"`
	OperationalPackage  DirectArchiveOperationalPackageManifestV2 `json:"operational_package"`
	ProvenancePackage   DirectArchiveProvenancePackageManifestV2  `json:"provenance_package"`
}

type DirectArchiveRootManifestV2 struct {
	Name        string `json:"name"`
	SourcePath  string `json:"source_path"`
	ArchivePath string `json:"archive_path"`
}

type DirectArchiveOperationalPackageManifestV2 struct {
	Schema         string               `json:"schema"`
	SourcePath     string               `json:"source_path"`
	ArchivePath    string               `json:"archive_path"`
	PackageID      string               `json:"package_id"`
	ManifestSHA256 string               `json:"manifest_sha256"`
	SchemaHead     int64                `json:"schema_head"`
	CreatedAt      time.Time            `json:"created_at"`
	ArtifactCount  int                  `json:"artifact_count"`
	EntryCount     int64                `json:"entry_count"`
	TotalBytes     int64                `json:"total_bytes"`
	EntriesSHA256  string               `json:"entries_sha256"`
	Entries        []DirectArchiveEntry `json:"entries"`
}

type DirectArchiveProvenancePackageManifestV2 struct {
	Schema         string               `json:"schema"`
	SourcePath     string               `json:"source_path"`
	ArchivePath    string               `json:"archive_path"`
	PackageID      string               `json:"package_id"`
	ManifestSHA256 string               `json:"manifest_sha256"`
	SchemaHead     int                  `json:"schema_head"`
	GraphDigest    string               `json:"graph_digest"`
	CompletedAt    time.Time            `json:"completed_at"`
	DumpSizeBytes  int64                `json:"dump_size_bytes"`
	EntryCount     int64                `json:"entry_count"`
	TotalBytes     int64                `json:"total_bytes"`
	EntriesSHA256  string               `json:"entries_sha256"`
	Entries        []DirectArchiveEntry `json:"entries"`
}

type directArchivePreparedPackagesV2 struct {
	Operational DirectArchiveOperationalPackageManifestV2
	Provenance  DirectArchiveProvenancePackageManifestV2
}

// PrepareDirectArchiveManifestV2 authenticates the bounded recovery packages
// and records only configured user-data root boundaries. It deliberately does
// not walk, stat, or hash entries below those roots.
func PrepareDirectArchiveManifestV2(ctx context.Context, input DirectArchiveManifestPrepareInputV2) (PreparedDirectArchiveManifest, error) {
	if err := ctx.Err(); err != nil {
		return PreparedDirectArchiveManifest{}, err
	}
	request, err := normalizeDirectArchiveRequestV2(input.Request)
	if err != nil {
		return PreparedDirectArchiveManifest{}, err
	}
	if input.Backend != DirectArchiveBackendBorg {
		return PreparedDirectArchiveManifest{}, fmt.Errorf("%w: unsupported backend %q", ErrDirectArchiveRequestInvalid, input.Backend)
	}
	if strings.TrimSpace(input.Repository) == "" || input.Repository != strings.TrimSpace(input.Repository) {
		return PreparedDirectArchiveManifest{}, fmt.Errorf("%w: exact repository identity is required", ErrDirectArchiveRequestInvalid)
	}
	if err := validateArchiveIdentity(input.ArchiveName); err != nil {
		return PreparedDirectArchiveManifest{}, err
	}
	classToken := strings.ReplaceAll(request.ArchiveClass, "_", "-")
	if input.ArchiveName != "__loom-direct-"+classToken+"-"+request.NodeID+"-"+request.ArchiveRef {
		return PreparedDirectArchiveManifest{}, fmt.Errorf("%w: archive name is not bound to class, node, and reference", ErrDirectArchiveRequestInvalid)
	}

	packageStarted := time.Now()
	packages, err := prepareDirectArchivePackagesV2(ctx, request)
	if err != nil {
		return PreparedDirectArchiveManifest{}, err
	}
	packageDurationMS := time.Since(packageStarted).Milliseconds()

	envelopeStarted := time.Now()
	roots := make([]DirectArchiveRootManifestV2, 0, len(request.Roots))
	for _, root := range request.Roots {
		roots = append(roots, DirectArchiveRootManifestV2{Name: root.Name, SourcePath: root.Path, ArchivePath: directArchivePath(root.Path)})
	}
	manifest := DirectArchiveManifestV2{
		HermesRecovery:      append([]hermesprofile.Evidence(nil), request.HermesRecovery...),
		Schema:              DirectArchiveManifestSchemaV2,
		VerificationProfile: DirectArchiveVerificationProfileRoutineIncrementalV1,
		Backend:             DirectArchiveBackendBorg,
		Repository:          input.Repository,
		ArchiveName:         input.ArchiveName,
		NodeID:              request.NodeID,
		ArchiveRef:          request.ArchiveRef,
		ArchiveClass:        request.ArchiveClass,
		CreatedAt:           request.CreatedAt,
		ExclusionPolicy:     DirectArchiveExclusionPolicy,
		Exclusions:          append([]DirectArchiveExclusion(nil), request.Exclusions...),
		Roots:               roots,
		OperationalPackage:  packages.Operational,
		ProvenancePackage:   packages.Provenance,
	}
	if err := validateDirectArchiveManifestV2(manifest); err != nil {
		return PreparedDirectArchiveManifest{}, err
	}
	raw, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return PreparedDirectArchiveManifest{}, err
	}
	raw = append(raw, '\n')
	digest := sha256.Sum256(raw)
	manifestSHA := hex.EncodeToString(digest[:])
	return PreparedDirectArchiveManifest{
		ManifestV2: &manifest, ManifestBytes: raw, ManifestSHA256: manifestSHA,
		HashFileBytes:                 []byte(manifestSHA + "  " + DirectArchiveManifestFile + "\n"),
		PackagePreparationDurationMS:  packageDurationMS,
		EnvelopePreparationDurationMS: time.Since(envelopeStarted).Milliseconds(),
	}, nil
}

// VerifyDirectArchiveV2PackageStability re-verifies only the bounded recovery
// packages against the already-authenticated v2 envelope. It deliberately does
// not rebuild the envelope or walk any user-data root.
func VerifyDirectArchiveV2PackageStability(ctx context.Context, input DirectArchiveManifestPrepareInputV2, expected PreparedDirectArchiveManifest) error {
	request := input.Request
	if expected.ManifestV2 == nil || expected.ManifestV2.Schema != DirectArchiveManifestSchemaV2 ||
		request.Schema != DirectArchiveRequestSchemaV2 || request.VerificationProfile != DirectArchiveVerificationProfileRoutineIncrementalV1 ||
		input.Backend != expected.ManifestV2.Backend || input.Repository != expected.ManifestV2.Repository ||
		input.ArchiveName != expected.ManifestV2.ArchiveName ||
		request.OperationalPackage.Path != expected.ManifestV2.OperationalPackage.SourcePath ||
		request.OperationalPackage.PackageID != expected.ManifestV2.OperationalPackage.PackageID ||
		request.OperationalPackage.ManifestSHA256 != expected.ManifestV2.OperationalPackage.ManifestSHA256 ||
		request.ProvenancePackage.Path != expected.ManifestV2.ProvenancePackage.SourcePath ||
		request.ProvenancePackage.PackageID != expected.ManifestV2.ProvenancePackage.PackageID ||
		request.ProvenancePackage.ManifestSHA256 != expected.ManifestV2.ProvenancePackage.ManifestSHA256 ||
		request.ProvenancePackage.Verify == nil {
		return fmt.Errorf("%w: v2 package stability identity mismatch", ErrDirectArchiveRequestInvalid)
	}
	current, err := prepareDirectArchivePackagesV2(ctx, request)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrDirectArchiveSourceChanged, err)
	}
	currentRaw, err := json.Marshal(current)
	if err != nil {
		return err
	}
	expectedRaw, err := json.Marshal(directArchivePreparedPackagesV2{
		Operational: expected.ManifestV2.OperationalPackage,
		Provenance:  expected.ManifestV2.ProvenancePackage,
	})
	if err != nil {
		return err
	}
	if !bytes.Equal(currentRaw, expectedRaw) {
		return ErrDirectArchiveSourceChanged
	}
	return nil
}

func prepareDirectArchivePackagesV2(ctx context.Context, request DirectArchiveRequestV2) (directArchivePreparedPackagesV2, error) {
	operational, err := VerifyOperationalPackage(ctx, OperationalPackageVerificationInput{
		PackageDir:             request.OperationalPackage.Path,
		ExpectedManifestSHA256: request.OperationalPackage.ManifestSHA256,
		ExpectedPackageID:      request.OperationalPackage.PackageID,
		ExpectedSchemaHead:     request.OperationalPackage.ExpectedSchemaHead,
		ForbiddenValues:        request.OperationalPackage.ForbiddenValues,
	})
	if err != nil {
		return directArchivePreparedPackagesV2{}, err
	}
	if operational.Status != "succeeded" || operational.Manifest == nil {
		return directArchivePreparedPackagesV2{}, fmt.Errorf("%w: operational package verification failed: %s", ErrDirectArchiveRequestInvalid, summarizeOperationalFindings(operational.Findings))
	}
	provenanceVerification, err := request.ProvenancePackage.Verify(ctx, request.ProvenancePackage.Path, request.ProvenancePackage.ManifestSHA256)
	if err != nil {
		return directArchivePreparedPackagesV2{}, fmt.Errorf("%w: provenance package verification failed: %v", ErrDirectArchiveRequestInvalid, err)
	}
	if provenanceVerification.ManifestSHA256 != request.ProvenancePackage.ManifestSHA256 || !validDirectArchiveProvenanceSchemaHead(provenanceVerification.SchemaHead) || provenanceVerification.CompletedAt.IsZero() || provenanceVerification.CompletedAt.Location() != time.UTC || provenanceVerification.DumpSizeBytes <= 0 || !validDirectArchiveGraphDigest(provenanceVerification.GraphDigest) {
		return directArchivePreparedPackagesV2{}, fmt.Errorf("%w: provenance package verification evidence is incomplete", ErrDirectArchiveRequestInvalid)
	}

	hardlinks := make(map[directArchiveHardlinkKey]string)
	operationalEntries, operationalBytes, err := inspectDirectArchiveTree(ctx, operational.PackageDir, directArchivePath(operational.PackageDir), nil, hardlinks)
	if err != nil {
		return directArchivePreparedPackagesV2{}, fmt.Errorf("inspect verified operational package: %w", err)
	}
	if err := verifyDirectArchiveOperationalEntries(operational, operationalEntries); err != nil {
		return directArchivePreparedPackagesV2{}, err
	}
	operationalDigest, err := directArchiveEntriesDigest(operationalEntries)
	if err != nil {
		return directArchivePreparedPackagesV2{}, err
	}
	if operationalBytes != operational.TotalBytes {
		return directArchivePreparedPackagesV2{}, fmt.Errorf("%w: verified operational package byte total changed", ErrDirectArchiveSourceChanged)
	}
	provenanceEntries, provenanceBytes, err := inspectDirectArchiveTree(ctx, request.ProvenancePackage.Path, directArchivePath(request.ProvenancePackage.Path), nil, hardlinks)
	if err != nil {
		return directArchivePreparedPackagesV2{}, fmt.Errorf("inspect verified provenance package: %w", err)
	}
	if err := verifyDirectArchiveProvenanceEntries(provenanceVerification, provenanceEntries); err != nil {
		return directArchivePreparedPackagesV2{}, err
	}
	provenanceDigest, err := directArchiveEntriesDigest(provenanceEntries)
	if err != nil {
		return directArchivePreparedPackagesV2{}, err
	}
	return directArchivePreparedPackagesV2{
		Operational: DirectArchiveOperationalPackageManifestV2{
			Schema: OperationalPackageSchema, SourcePath: operational.PackageDir,
			ArchivePath: directArchivePath(operational.PackageDir), PackageID: operational.PackageID,
			ManifestSHA256: operational.ManifestSHA256, SchemaHead: operational.SchemaHead,
			CreatedAt: operational.CreatedAt, ArtifactCount: operational.ArtifactCount,
			EntryCount: int64(len(operationalEntries)), TotalBytes: operationalBytes,
			EntriesSHA256: operationalDigest, Entries: operationalEntries,
		},
		Provenance: DirectArchiveProvenancePackageManifestV2{
			Schema: provenance.RecoveryManifestSchema, SourcePath: request.ProvenancePackage.Path,
			ArchivePath: directArchivePath(request.ProvenancePackage.Path), PackageID: request.ProvenancePackage.PackageID,
			ManifestSHA256: provenanceVerification.ManifestSHA256, SchemaHead: provenanceVerification.SchemaHead,
			GraphDigest: provenanceVerification.GraphDigest, CompletedAt: provenanceVerification.CompletedAt,
			DumpSizeBytes: provenanceVerification.DumpSizeBytes, EntryCount: int64(len(provenanceEntries)),
			TotalBytes: provenanceBytes, EntriesSHA256: provenanceDigest, Entries: provenanceEntries,
		},
	}, nil
}

func normalizeDirectArchiveRequestV2(request DirectArchiveRequestV2) (DirectArchiveRequestV2, error) {
	if request.Schema != DirectArchiveRequestSchemaV2 {
		return request, fmt.Errorf("%w: unsupported request schema %q", ErrDirectArchiveRequestInvalid, request.Schema)
	}
	if request.VerificationProfile != DirectArchiveVerificationProfileRoutineIncrementalV1 {
		return request, fmt.Errorf("%w: unsupported verification profile %q", ErrDirectArchiveRequestInvalid, request.VerificationProfile)
	}
	archiveClass, err := NormalizeDirectArchiveClass(request.ArchiveClass)
	if err != nil || request.ArchiveClass == "" || archiveClass != request.ArchiveClass {
		return request, fmt.Errorf("%w: exact archive class is required", ErrDirectArchiveRequestInvalid)
	}
	legacy, err := normalizeDirectArchiveRequestWithBoxRoots(DirectArchiveRequest{
		Schema: DirectArchiveRequestSchema, NodeID: request.NodeID, ArchiveRef: request.ArchiveRef,
		ArchiveClass: request.ArchiveClass, CreatedAt: request.CreatedAt,
		Roots:              append([]DirectArchiveRoot(nil), request.Roots...),
		Exclusions:         append([]DirectArchiveExclusion(nil), request.Exclusions...),
		OperationalPackage: request.OperationalPackage, ProvenancePackage: request.ProvenancePackage,
	}, true)
	if err != nil {
		return request, err
	}
	request.NodeID = legacy.NodeID
	request.ArchiveRef = legacy.ArchiveRef
	request.ArchiveClass = legacy.ArchiveClass
	request.CreatedAt = legacy.CreatedAt
	request.Roots = legacy.Roots
	request.Exclusions = legacy.Exclusions
	request.OperationalPackage = legacy.OperationalPackage
	request.ProvenancePackage = legacy.ProvenancePackage
	if filepath.Base(request.OperationalPackage.Path) != request.OperationalPackage.PackageID {
		return request, fmt.Errorf("%w: operational package path does not match package id", ErrDirectArchiveRequestInvalid)
	}
	if err := rejectDirectArchiveExclusionOverlaps(request.Exclusions); err != nil {
		return request, err
	}
	if err := rejectDirectArchiveResolvedPathAliases(request); err != nil {
		return request, err
	}
	return request, nil
}

func rejectDirectArchiveExclusionOverlaps(exclusions []DirectArchiveExclusion) error {
	for i := range exclusions {
		for j := i + 1; j < len(exclusions) && exclusions[j].Root == exclusions[i].Root; j++ {
			left := exclusions[i].RelativePath
			right := exclusions[j].RelativePath
			if strings.HasPrefix(right, left+"/") || strings.HasPrefix(left, right+"/") {
				return fmt.Errorf("%w: exclusion paths overlap", ErrDirectArchiveRequestInvalid)
			}
		}
	}
	return nil
}

func rejectDirectArchiveResolvedPathAliases(request DirectArchiveRequestV2) error {
	type namedPath struct {
		name string
		path string
	}
	paths := make([]namedPath, 0, len(request.Roots)+2)
	for _, root := range request.Roots {
		paths = append(paths, namedPath{name: "root " + root.Name, path: root.Path})
	}
	paths = append(paths,
		namedPath{name: "operational package", path: request.OperationalPackage.Path},
		namedPath{name: "provenance package", path: request.ProvenancePackage.Path},
	)
	for index := range paths {
		resolved, err := filepath.EvalSymlinks(paths[index].path)
		if err != nil {
			return fmt.Errorf("%w: resolve %s path: %v", ErrDirectArchiveRequestInvalid, paths[index].name, err)
		}
		paths[index].path = filepath.Clean(resolved)
	}
	for i := range paths {
		for j := i + 1; j < len(paths); j++ {
			if directArchivePathsOverlap(paths[i].path, paths[j].path) {
				return fmt.Errorf("%w: resolved archive paths overlap or alias", ErrDirectArchiveRequestInvalid)
			}
		}
	}
	return nil
}

func validateDirectArchiveManifestV2(manifest DirectArchiveManifestV2) error {
	hermesRoots := make([]DirectArchiveRoot, 0, len(manifest.Roots))
	for _, root := range manifest.Roots {
		hermesRoots = append(hermesRoots, DirectArchiveRoot{root.Name, root.SourcePath})
	}
	if err := validateHermesEvidence(manifest.HermesRecovery, hermesRoots, manifest.Exclusions); err != nil {
		return err
	}

	if manifest.Schema != DirectArchiveManifestSchemaV2 || manifest.VerificationProfile != DirectArchiveVerificationProfileRoutineIncrementalV1 || manifest.Backend != DirectArchiveBackendBorg {
		return fmt.Errorf("%w: unsupported v2 archive manifest schema, profile, or backend", ErrDirectArchiveRequestInvalid)
	}
	if strings.TrimSpace(manifest.Repository) == "" || manifest.Repository != strings.TrimSpace(manifest.Repository) {
		return fmt.Errorf("%w: invalid repository identity", ErrDirectArchiveRequestInvalid)
	}
	if err := validateArchiveIdentity(manifest.ArchiveName); err != nil {
		return err
	}
	if err := validateArchiveIdentity(manifest.NodeID); err != nil {
		return err
	}
	if err := validateArchiveIdentity(manifest.ArchiveRef); err != nil {
		return err
	}
	archiveClass, err := NormalizeDirectArchiveClass(manifest.ArchiveClass)
	if err != nil || manifest.ArchiveClass == "" || archiveClass != manifest.ArchiveClass {
		return fmt.Errorf("%w: direct archive class is invalid", ErrDirectArchiveRequestInvalid)
	}
	classToken := strings.ReplaceAll(manifest.ArchiveClass, "_", "-")
	if manifest.ArchiveName != "__loom-direct-"+classToken+"-"+manifest.NodeID+"-"+manifest.ArchiveRef {
		return fmt.Errorf("%w: archive name is not bound to class, node, and reference", ErrDirectArchiveRequestInvalid)
	}
	if manifest.CreatedAt.IsZero() || manifest.CreatedAt.Location() != time.UTC || manifest.ExclusionPolicy != DirectArchiveExclusionPolicy || len(manifest.Roots) == 0 {
		return fmt.Errorf("%w: incomplete v2 archive manifest identity", ErrDirectArchiveRequestInvalid)
	}

	rootNames := make(map[string]struct{}, len(manifest.Roots))
	rootPaths := make([]string, 0, len(manifest.Roots))
	for index, root := range manifest.Roots {
		if err := validateArchiveIdentity(root.Name); err != nil {
			return err
		}
		sourcePath, err := exactAbsolutePath(root.SourcePath)
		if err != nil || sourcePath == string(filepath.Separator) || directArchivePath(sourcePath) != root.ArchivePath || directArchivePortablePathsOverlap(root.ArchivePath, DirectArchiveEvidenceDir) {
			return fmt.Errorf("%w: root %q has invalid path evidence", ErrDirectArchiveRequestInvalid, root.Name)
		}
		if index > 0 && manifest.Roots[index-1].Name >= root.Name {
			return fmt.Errorf("%w: manifest roots are not uniquely sorted", ErrDirectArchiveRequestInvalid)
		}
		if _, exists := rootNames[root.Name]; exists {
			return fmt.Errorf("%w: duplicate root in manifest", ErrDirectArchiveRequestInvalid)
		}
		rootNames[root.Name] = struct{}{}
		rootPaths = append(rootPaths, sourcePath)
	}
	for i := range rootPaths {
		for j := i + 1; j < len(rootPaths); j++ {
			if directArchivePathsOverlap(rootPaths[i], rootPaths[j]) {
				return fmt.Errorf("%w: manifest roots overlap", ErrDirectArchiveRequestInvalid)
			}
		}
	}
	for index, exclusion := range manifest.Exclusions {
		if _, ok := rootNames[exclusion.Root]; !ok {
			return fmt.Errorf("%w: exclusion names unknown manifest root", ErrDirectArchiveRequestInvalid)
		}
		if _, err := directArchiveRelativePath(exclusion.RelativePath); err != nil {
			return fmt.Errorf("%w: invalid manifest exclusion", ErrDirectArchiveRequestInvalid)
		}
		if index > 0 {
			previous := manifest.Exclusions[index-1]
			if previous.Root > exclusion.Root || (previous.Root == exclusion.Root && previous.RelativePath >= exclusion.RelativePath) {
				return fmt.Errorf("%w: manifest exclusions are not uniquely sorted", ErrDirectArchiveRequestInvalid)
			}
		}
	}
	if err := rejectDirectArchiveExclusionOverlaps(manifest.Exclusions); err != nil {
		return err
	}

	archiveEntries := make(map[string]DirectArchiveEntry)
	opPath, err := validateDirectArchiveOperationalPackageManifestV2(manifest.OperationalPackage, rootPaths, archiveEntries)
	if err != nil {
		return err
	}
	provPath, err := validateDirectArchiveProvenancePackageManifestV2(manifest.ProvenancePackage, rootPaths, archiveEntries)
	if err != nil {
		return err
	}
	if directArchivePathsOverlap(opPath, provPath) {
		return fmt.Errorf("%w: operational and provenance packages overlap", ErrDirectArchiveRequestInvalid)
	}
	return nil
}

func validateDirectArchiveOperationalPackageManifestV2(pkg DirectArchiveOperationalPackageManifestV2, rootPaths []string, prior map[string]DirectArchiveEntry) (string, error) {
	sourcePath, err := exactAbsolutePath(pkg.SourcePath)
	if err != nil || sourcePath == string(filepath.Separator) || directArchivePath(sourcePath) != pkg.ArchivePath || directArchivePortablePathsOverlap(pkg.ArchivePath, DirectArchiveEvidenceDir) || filepath.Base(sourcePath) != pkg.PackageID || pkg.Schema != OperationalPackageSchema || normalizeSHA256(pkg.ManifestSHA256) == "" || pkg.SchemaHead < 0 || pkg.CreatedAt.IsZero() || pkg.CreatedAt.Location() != time.UTC || pkg.ArtifactCount <= 0 || pkg.EntryCount != int64(len(pkg.Entries)) || pkg.EntryCount != int64(pkg.ArtifactCount+3) || pkg.TotalBytes <= 0 {
		return "", fmt.Errorf("%w: operational package evidence is incomplete", ErrDirectArchiveRequestInvalid)
	}
	if err := validatePackageID(pkg.PackageID); err != nil {
		return "", err
	}
	for _, rootPath := range rootPaths {
		if directArchivePathsOverlap(rootPath, sourcePath) {
			return "", fmt.Errorf("%w: operational package overlaps a canonical root", ErrDirectArchiveRequestInvalid)
		}
	}
	if err := validateDirectArchivePackageEntriesV2(pkg.ArchivePath, pkg.Entries, pkg.EntryCount, pkg.TotalBytes, pkg.EntriesSHA256, prior); err != nil {
		return "", err
	}
	root := pkg.Entries[0]
	if root.Path != "." || root.Type != "directory" || root.Mode != 0o700 {
		return "", fmt.Errorf("%w: operational package directory evidence mismatch", ErrDirectArchiveRequestInvalid)
	}
	manifestEntry := directArchiveEntryByPath(pkg.Entries, OperationalManifestFile)
	if manifestEntry == nil || manifestEntry.Type != "file" || manifestEntry.Mode != 0o600 || manifestEntry.SHA256 != pkg.ManifestSHA256 {
		return "", fmt.Errorf("%w: operational package manifest bytes are not bound", ErrDirectArchiveRequestInvalid)
	}
	hashPayload := []byte(pkg.ManifestSHA256 + "  " + OperationalManifestFile + "\n")
	hashDigest := sha256.Sum256(hashPayload)
	hashEntry := directArchiveEntryByPath(pkg.Entries, OperationalManifestHashFile)
	if hashEntry == nil || hashEntry.Type != "file" || hashEntry.Mode != 0o600 || hashEntry.SizeBytes != int64(len(hashPayload)) || hashEntry.SHA256 != hex.EncodeToString(hashDigest[:]) {
		return "", fmt.Errorf("%w: operational package manifest authentication bytes are not bound", ErrDirectArchiveRequestInvalid)
	}
	for _, entry := range pkg.Entries[1:] {
		if entry.Type != "file" || entry.Mode != 0o600 {
			return "", fmt.Errorf("%w: operational package file mode or type is invalid", ErrDirectArchiveRequestInvalid)
		}
	}
	return sourcePath, nil
}

func validateDirectArchiveProvenancePackageManifestV2(pkg DirectArchiveProvenancePackageManifestV2, rootPaths []string, prior map[string]DirectArchiveEntry) (string, error) {
	sourcePath, err := exactAbsolutePath(pkg.SourcePath)
	if err != nil || sourcePath == string(filepath.Separator) || directArchivePath(sourcePath) != pkg.ArchivePath || directArchivePortablePathsOverlap(pkg.ArchivePath, DirectArchiveEvidenceDir) || filepath.Base(sourcePath) != pkg.PackageID || pkg.Schema != provenance.RecoveryManifestSchema || normalizeSHA256(pkg.ManifestSHA256) == "" || !validDirectArchiveProvenanceSchemaHead(pkg.SchemaHead) || !validDirectArchiveGraphDigest(pkg.GraphDigest) || pkg.CompletedAt.IsZero() || pkg.CompletedAt.Location() != time.UTC || pkg.DumpSizeBytes <= 0 || pkg.EntryCount != int64(len(pkg.Entries)) || pkg.EntryCount != 3 || pkg.TotalBytes <= 0 {
		return "", fmt.Errorf("%w: provenance package evidence is incomplete", ErrDirectArchiveRequestInvalid)
	}
	if err := validatePackageID(pkg.PackageID); err != nil {
		return "", err
	}
	for _, rootPath := range rootPaths {
		if directArchivePathsOverlap(rootPath, sourcePath) {
			return "", fmt.Errorf("%w: provenance package overlaps a canonical root", ErrDirectArchiveRequestInvalid)
		}
	}
	if err := validateDirectArchivePackageEntriesV2(pkg.ArchivePath, pkg.Entries, pkg.EntryCount, pkg.TotalBytes, pkg.EntriesSHA256, prior); err != nil {
		return "", err
	}
	root := pkg.Entries[0]
	if root.Path != "." || root.Type != "directory" || root.Mode != 0o700 {
		return "", fmt.Errorf("%w: provenance package directory evidence mismatch", ErrDirectArchiveRequestInvalid)
	}
	manifestEntry := directArchiveEntryByPath(pkg.Entries, provenance.RecoveryManifestFile)
	dumpEntry := directArchiveEntryByPath(pkg.Entries, provenance.RecoveryDumpFile)
	if manifestEntry == nil || manifestEntry.Type != "file" || manifestEntry.Mode != 0o600 || manifestEntry.SHA256 != pkg.ManifestSHA256 || dumpEntry == nil || dumpEntry.Type != "file" || dumpEntry.Mode != 0o600 || dumpEntry.SizeBytes != pkg.DumpSizeBytes {
		return "", fmt.Errorf("%w: provenance package entry evidence mismatch", ErrDirectArchiveRequestInvalid)
	}
	return sourcePath, nil
}

func validDirectArchiveProvenanceSchemaHead(schemaHead int) bool {
	_, err := provenance.RecoveryRelationsForSchemaHead(schemaHead)
	return err == nil
}

func validateDirectArchivePackageEntriesV2(archivePath string, entries []DirectArchiveEntry, entryCount, totalBytes int64, entriesSHA string, prior map[string]DirectArchiveEntry) error {
	if len(entries) == 0 || entryCount != int64(len(entries)) {
		return fmt.Errorf("%w: package entry count mismatch", ErrDirectArchiveRequestInvalid)
	}
	digest, err := directArchiveEntriesDigest(entries)
	if err != nil || digest != entriesSHA {
		return fmt.Errorf("%w: package entries digest mismatch", ErrDirectArchiveRequestInvalid)
	}
	var total int64
	for index, entry := range entries {
		if err := validateDirectArchiveEntry(entry); err != nil {
			return err
		}
		if index > 0 && entries[index-1].Path >= entry.Path {
			return fmt.Errorf("%w: package entries are not uniquely sorted", ErrDirectArchiveRequestInvalid)
		}
		archiveEntryPath := directArchiveEntryPath(archivePath, entry.Path)
		if err := validateDirectArchiveHardlink(entry, prior); err != nil {
			return err
		}
		if _, exists := prior[archiveEntryPath]; exists {
			return fmt.Errorf("%w: duplicate archive entry path", ErrDirectArchiveRequestInvalid)
		}
		prior[archiveEntryPath] = entry
		if entry.Type == "file" {
			total += entry.SizeBytes
		}
	}
	if total != totalBytes {
		return fmt.Errorf("%w: package byte total mismatch", ErrDirectArchiveRequestInvalid)
	}
	return nil
}

func directArchiveEntryByPath(entries []DirectArchiveEntry, name string) *DirectArchiveEntry {
	for index := range entries {
		if entries[index].Path == name {
			return &entries[index]
		}
	}
	return nil
}

func directArchiveManifestSchema(raw []byte) (string, error) {
	var envelope struct {
		Schema string `json:"schema"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if err := decoder.Decode(&envelope); err != nil {
		return "", fmt.Errorf("%w: archive manifest JSON is invalid", ErrDirectArchiveRequestInvalid)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return "", fmt.Errorf("%w: archive manifest has trailing content", ErrDirectArchiveRequestInvalid)
	}
	if envelope.Schema == "" {
		return "", fmt.Errorf("%w: archive manifest schema is required", ErrDirectArchiveRequestInvalid)
	}
	return envelope.Schema, nil
}

func decodeExactDirectArchiveManifest(raw []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("%w: archive manifest JSON is invalid", ErrDirectArchiveRequestInvalid)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return fmt.Errorf("%w: archive manifest has trailing content", ErrDirectArchiveRequestInvalid)
	}
	return nil
}
