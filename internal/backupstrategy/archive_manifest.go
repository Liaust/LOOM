package backupstrategy

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
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/sys/unix"

	"loom.local/loom/internal/provenance"

	"loom.local/loom/internal/hermesprofile"
)

const (
	DirectArchiveRequestSchema    = "loom.direct_archive.request.v1"
	DirectArchiveManifestSchema   = "loom.direct_archive.manifest.v1"
	DirectArchiveRequestSchemaV2  = "loom.direct_archive.request.v2"
	DirectArchiveManifestSchemaV2 = "loom.direct_archive.manifest.v2"

	DirectArchiveVerificationProfileRoutineIncrementalV1 = "routine_incremental_v1"

	DirectArchiveBackendBorg      = "borg"
	DirectArchiveExclusionPolicy  = "path_prefix_v1"
	DirectArchiveEvidenceDir      = ".loom-direct-archive"
	DirectArchiveManifestFile     = "manifest.json"
	DirectArchiveManifestHashFile = "manifest.sha256"

	DirectArchiveClassUserData   = "user_data"
	DirectArchiveClassAcceptance = "acceptance"
	DirectArchiveClassMilestone  = "milestone"
)

var (
	ErrDirectArchiveRequestInvalid = errors.New("invalid direct archive request")
	ErrDirectArchiveSourceChanged  = errors.New("direct archive source changed")
)

type DirectArchiveRequest struct {
	HermesRecovery     []hermesprofile.Evidence        `json:"hermes_recovery,omitempty"`
	Schema             string                          `json:"schema"`
	NodeID             string                          `json:"node_id"`
	ArchiveRef         string                          `json:"archive_ref"`
	ArchiveClass       string                          `json:"archive_class,omitempty"`
	CreatedAt          time.Time                       `json:"created_at"`
	Roots              []DirectArchiveRoot             `json:"roots"`
	Exclusions         []DirectArchiveExclusion        `json:"exclusions"`
	OperationalPackage DirectArchiveOperationalPackage `json:"operational_package"`
	ProvenancePackage  DirectArchiveProvenancePackage  `json:"provenance_package"`
}

type DirectArchiveRoot struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

type DirectArchiveExclusion struct {
	Root         string `json:"root"`
	RelativePath string `json:"relative_path"`
}

type DirectArchiveOperationalPackage struct {
	Path               string   `json:"path"`
	ManifestSHA256     string   `json:"manifest_sha256"`
	PackageID          string   `json:"package_id"`
	ExpectedSchemaHead *int64   `json:"expected_schema_head,omitempty"`
	ForbiddenValues    []string `json:"-"`
}

type DirectArchiveProvenancePackage struct {
	Path           string                                 `json:"path"`
	ManifestSHA256 string                                 `json:"manifest_sha256"`
	PackageID      string                                 `json:"package_id"`
	Verify         DirectArchiveProvenancePackageVerifier `json:"-"`
}

type DirectArchiveProvenancePackageVerifier func(context.Context, string, string) (DirectArchiveProvenanceVerification, error)

type DirectArchiveProvenanceVerification struct {
	ManifestSHA256 string
	SchemaHead     int
	GraphDigest    string
	CompletedAt    time.Time
	DumpSizeBytes  int64
}

type DirectArchiveManifestPrepareInput struct {
	Request     DirectArchiveRequest
	Backend     string
	Repository  string
	ArchiveName string
}

type PreparedDirectArchiveManifest struct {
	Manifest                      DirectArchiveManifest
	ManifestV2                    *DirectArchiveManifestV2
	ManifestBytes                 []byte
	ManifestSHA256                string
	HashFileBytes                 []byte
	PackagePreparationDurationMS  int64
	EnvelopePreparationDurationMS int64
	V2UserSymlinkTargets          map[string]string `json:"-"`
}

type DirectArchiveManifest struct {
	HermesRecovery       []hermesprofile.Evidence                `json:"hermes_recovery,omitempty"`
	Schema               string                                  `json:"schema"`
	Backend              string                                  `json:"backend"`
	Repository           string                                  `json:"repository"`
	ArchiveName          string                                  `json:"archive_name"`
	NodeID               string                                  `json:"node_id"`
	ArchiveRef           string                                  `json:"archive_ref"`
	ArchiveClass         string                                  `json:"archive_class,omitempty"`
	CreatedAt            time.Time                               `json:"created_at"`
	ExclusionPolicy      string                                  `json:"exclusion_policy"`
	Exclusions           []DirectArchiveExclusion                `json:"exclusions"`
	Roots                []DirectArchiveRootManifest             `json:"roots"`
	OperationalPackage   DirectArchiveOperationalPackageManifest `json:"operational_package"`
	ProvenancePackage    DirectArchiveProvenancePackageManifest  `json:"provenance_package"`
	SourceSnapshotSHA256 string                                  `json:"source_snapshot_sha256"`
}

type DirectArchiveRootManifest struct {
	Name          string               `json:"name"`
	SourcePath    string               `json:"source_path"`
	ArchivePath   string               `json:"archive_path"`
	EntryCount    int64                `json:"entry_count"`
	TotalBytes    int64                `json:"total_bytes"`
	EntriesSHA256 string               `json:"entries_sha256"`
	Entries       []DirectArchiveEntry `json:"entries"`
}

type DirectArchiveEntry struct {
	Path             string `json:"path"`
	Type             string `json:"type"`
	SizeBytes        int64  `json:"size_bytes,omitempty"`
	SHA256           string `json:"sha256,omitempty"`
	Mode             uint32 `json:"mode"`
	ModifiedUnixNano int64  `json:"modified_unix_nano"`
	LinkTarget       string `json:"link_target,omitempty"`
	HardlinkTarget   string `json:"hardlink_target,omitempty"`
}

type DirectArchiveOperationalPackageManifest struct {
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

type DirectArchiveProvenancePackageManifest struct {
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

type DirectArchiveExtractionVerification struct {
	Status         string            `json:"status"`
	Root           string            `json:"root"`
	ManifestSHA256 string            `json:"manifest_sha256"`
	Checks         map[string]string `json:"checks"`
	Errors         []string          `json:"errors,omitempty"`
}

func PrepareDirectArchiveManifest(ctx context.Context, input DirectArchiveManifestPrepareInput) (PreparedDirectArchiveManifest, error) {
	if err := ctx.Err(); err != nil {
		return PreparedDirectArchiveManifest{}, err
	}
	request, err := normalizeDirectArchiveRequest(input.Request)
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

	verification, err := VerifyOperationalPackage(ctx, OperationalPackageVerificationInput{
		PackageDir:             request.OperationalPackage.Path,
		ExpectedManifestSHA256: request.OperationalPackage.ManifestSHA256,
		ExpectedPackageID:      request.OperationalPackage.PackageID,
		ExpectedSchemaHead:     request.OperationalPackage.ExpectedSchemaHead,
		ForbiddenValues:        request.OperationalPackage.ForbiddenValues,
	})
	if err != nil {
		return PreparedDirectArchiveManifest{}, err
	}
	if verification.Status != "succeeded" || verification.Manifest == nil {
		return PreparedDirectArchiveManifest{}, fmt.Errorf("%w: operational package verification failed: %s", ErrDirectArchiveRequestInvalid, summarizeOperationalFindings(verification.Findings))
	}
	provenanceVerification, err := request.ProvenancePackage.Verify(ctx, request.ProvenancePackage.Path, request.ProvenancePackage.ManifestSHA256)
	if err != nil {
		return PreparedDirectArchiveManifest{}, fmt.Errorf("%w: provenance package verification failed: %v", ErrDirectArchiveRequestInvalid, err)
	}
	if provenanceVerification.ManifestSHA256 != request.ProvenancePackage.ManifestSHA256 || provenanceVerification.SchemaHead != provenance.SchemaHead || provenanceVerification.CompletedAt.IsZero() || provenanceVerification.CompletedAt.Location() != time.UTC || provenanceVerification.DumpSizeBytes <= 0 || !validDirectArchiveGraphDigest(provenanceVerification.GraphDigest) {
		return PreparedDirectArchiveManifest{}, fmt.Errorf("%w: provenance package verification evidence is incomplete", ErrDirectArchiveRequestInvalid)
	}

	exclusionsByRoot := directArchiveExclusionsByRoot(request.Exclusions)
	hardlinks := make(map[directArchiveHardlinkKey]string)
	roots := make([]DirectArchiveRootManifest, 0, len(request.Roots))
	for _, root := range request.Roots {
		if err := ctx.Err(); err != nil {
			return PreparedDirectArchiveManifest{}, err
		}
		manifest, inspectErr := inspectDirectArchiveRoot(ctx, root, exclusionsByRoot[root.Name], hardlinks)
		if inspectErr != nil {
			return PreparedDirectArchiveManifest{}, inspectErr
		}
		roots = append(roots, manifest)
	}
	sort.Slice(roots, func(i, j int) bool { return roots[i].Name < roots[j].Name })
	operationalEntries, operationalBytes, err := inspectDirectArchiveTree(ctx, verification.PackageDir, directArchivePath(verification.PackageDir), nil, hardlinks)
	if err != nil {
		return PreparedDirectArchiveManifest{}, fmt.Errorf("inspect verified operational package: %w", err)
	}
	if err := verifyDirectArchiveOperationalEntries(verification, operationalEntries); err != nil {
		return PreparedDirectArchiveManifest{}, err
	}
	operationalEntriesDigest, err := directArchiveEntriesDigest(operationalEntries)
	if err != nil {
		return PreparedDirectArchiveManifest{}, err
	}
	if operationalBytes != verification.TotalBytes {
		return PreparedDirectArchiveManifest{}, fmt.Errorf("%w: verified operational package byte total changed", ErrDirectArchiveSourceChanged)
	}
	provenanceEntries, provenanceBytes, err := inspectDirectArchiveTree(ctx, request.ProvenancePackage.Path, directArchivePath(request.ProvenancePackage.Path), nil, hardlinks)
	if err != nil {
		return PreparedDirectArchiveManifest{}, fmt.Errorf("inspect verified provenance package: %w", err)
	}
	if err := verifyDirectArchiveProvenanceEntries(provenanceVerification, provenanceEntries); err != nil {
		return PreparedDirectArchiveManifest{}, err
	}
	provenanceEntriesDigest, err := directArchiveEntriesDigest(provenanceEntries)
	if err != nil {
		return PreparedDirectArchiveManifest{}, err
	}

	manifest := DirectArchiveManifest{
		HermesRecovery:  append([]hermesprofile.Evidence(nil), request.HermesRecovery...),
		Schema:          DirectArchiveManifestSchema,
		Backend:         DirectArchiveBackendBorg,
		Repository:      input.Repository,
		ArchiveName:     input.ArchiveName,
		NodeID:          request.NodeID,
		ArchiveRef:      request.ArchiveRef,
		ArchiveClass:    request.ArchiveClass,
		CreatedAt:       request.CreatedAt,
		ExclusionPolicy: DirectArchiveExclusionPolicy,
		Exclusions:      append([]DirectArchiveExclusion(nil), request.Exclusions...),
		Roots:           roots,
		OperationalPackage: DirectArchiveOperationalPackageManifest{
			SourcePath:     verification.PackageDir,
			ArchivePath:    directArchivePath(verification.PackageDir),
			PackageID:      verification.PackageID,
			ManifestSHA256: verification.ManifestSHA256,
			SchemaHead:     verification.SchemaHead,
			CreatedAt:      verification.CreatedAt,
			ArtifactCount:  verification.ArtifactCount,
			EntryCount:     int64(len(operationalEntries)),
			TotalBytes:     operationalBytes,
			EntriesSHA256:  operationalEntriesDigest,
			Entries:        operationalEntries,
		},
		ProvenancePackage: DirectArchiveProvenancePackageManifest{
			SourcePath: request.ProvenancePackage.Path, ArchivePath: directArchivePath(request.ProvenancePackage.Path),
			PackageID: request.ProvenancePackage.PackageID, ManifestSHA256: provenanceVerification.ManifestSHA256,
			SchemaHead: provenanceVerification.SchemaHead, GraphDigest: provenanceVerification.GraphDigest,
			CompletedAt: provenanceVerification.CompletedAt, DumpSizeBytes: provenanceVerification.DumpSizeBytes,
			EntryCount: int64(len(provenanceEntries)), TotalBytes: provenanceBytes, EntriesSHA256: provenanceEntriesDigest, Entries: provenanceEntries,
		},
	}
	manifest.SourceSnapshotSHA256, err = directArchiveSourceSnapshotDigest(manifest)
	if err != nil {
		return PreparedDirectArchiveManifest{}, err
	}
	if err := validateDirectArchiveManifest(manifest); err != nil {
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
		Manifest:       manifest,
		ManifestBytes:  raw,
		ManifestSHA256: manifestSHA,
		HashFileBytes:  []byte(manifestSHA + "  " + DirectArchiveManifestFile + "\n"),
	}, nil
}

func verifyDirectArchiveOperationalEntries(verification OperationalPackageVerification, entries []DirectArchiveEntry) error {
	if verification.Manifest == nil {
		return fmt.Errorf("%w: verified operational package manifest is missing", ErrDirectArchiveRequestInvalid)
	}
	byPath := make(map[string]DirectArchiveEntry, len(entries))
	for _, entry := range entries {
		byPath[entry.Path] = entry
	}
	root, ok := byPath["."]
	if !ok || root.Type != "directory" || root.Mode != 0o700 {
		return fmt.Errorf("%w: operational package directory evidence mismatch", ErrDirectArchiveRequestInvalid)
	}
	manifestEntry, ok := byPath[OperationalManifestFile]
	if !ok || manifestEntry.Type != "file" || manifestEntry.Mode != 0o600 || manifestEntry.SHA256 != verification.ManifestSHA256 {
		return fmt.Errorf("%w: operational package manifest entry mismatch", ErrDirectArchiveRequestInvalid)
	}
	hashPayload := []byte(verification.ManifestSHA256 + "  " + OperationalManifestFile + "\n")
	hashDigest := sha256.Sum256(hashPayload)
	hashEntry, ok := byPath[OperationalManifestHashFile]
	if !ok || hashEntry.Type != "file" || hashEntry.Mode != 0o600 || hashEntry.SizeBytes != int64(len(hashPayload)) || hashEntry.SHA256 != hex.EncodeToString(hashDigest[:]) {
		return fmt.Errorf("%w: operational package manifest authentication entry mismatch", ErrDirectArchiveRequestInvalid)
	}
	for _, artifact := range verification.Manifest.Artifacts {
		entry, ok := byPath[artifact.Path]
		if !ok || entry.Type != "file" || entry.SizeBytes != artifact.SizeBytes || entry.SHA256 != normalizeSHA256(artifact.SHA256) || entry.Mode != artifact.Mode {
			return fmt.Errorf("%w: operational package artifact %q does not match verified manifest", ErrDirectArchiveRequestInvalid, artifact.Path)
		}
	}
	if len(entries) != len(verification.Manifest.Artifacts)+3 {
		return fmt.Errorf("%w: operational package entry set does not match verified package", ErrDirectArchiveRequestInvalid)
	}
	return nil
}

func verifyDirectArchiveProvenanceEntries(verification DirectArchiveProvenanceVerification, entries []DirectArchiveEntry) error {
	byPath := make(map[string]DirectArchiveEntry, len(entries))
	for _, entry := range entries {
		byPath[entry.Path] = entry
	}
	root, ok := byPath["."]
	if !ok || root.Type != "directory" || root.Mode != 0o700 {
		return fmt.Errorf("%w: provenance package directory evidence mismatch", ErrDirectArchiveRequestInvalid)
	}
	manifest, ok := byPath[provenance.RecoveryManifestFile]
	if !ok || manifest.Type != "file" || manifest.Mode != 0o600 || manifest.SHA256 != verification.ManifestSHA256 {
		return fmt.Errorf("%w: provenance package manifest entry mismatch", ErrDirectArchiveRequestInvalid)
	}
	dump, ok := byPath[provenance.RecoveryDumpFile]
	if !ok || dump.Type != "file" || dump.Mode != 0o600 || dump.SizeBytes != verification.DumpSizeBytes {
		return fmt.Errorf("%w: provenance package dump entry mismatch", ErrDirectArchiveRequestInvalid)
	}
	if len(entries) != 3 {
		return fmt.Errorf("%w: provenance package entry set does not match verified package", ErrDirectArchiveRequestInvalid)
	}
	return nil
}

func VerifyDirectArchiveSourceStability(ctx context.Context, input DirectArchiveManifestPrepareInput, expected PreparedDirectArchiveManifest) error {
	current, err := PrepareDirectArchiveManifest(ctx, input)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrDirectArchiveSourceChanged, err)
	}
	if current.ManifestSHA256 != expected.ManifestSHA256 || !bytes.Equal(current.ManifestBytes, expected.ManifestBytes) {
		return ErrDirectArchiveSourceChanged
	}
	return nil
}

func ParseAuthenticatedDirectArchiveManifest(manifestBytes, hashFileBytes []byte) (PreparedDirectArchiveManifest, error) {
	digest := sha256.Sum256(manifestBytes)
	manifestSHA := hex.EncodeToString(digest[:])
	wantHashFile := manifestSHA + "  " + DirectArchiveManifestFile + "\n"
	if string(hashFileBytes) != wantHashFile {
		return PreparedDirectArchiveManifest{}, fmt.Errorf("%w: archive manifest authentication hash mismatch", ErrDirectArchiveRequestInvalid)
	}
	schema, err := directArchiveManifestSchema(manifestBytes)
	if err != nil {
		return PreparedDirectArchiveManifest{}, err
	}
	switch schema {
	case DirectArchiveManifestSchema:
		var manifest DirectArchiveManifest
		if err := decodeExactDirectArchiveManifest(manifestBytes, &manifest); err != nil {
			return PreparedDirectArchiveManifest{}, err
		}
		if err := validateDirectArchiveManifest(manifest); err != nil {
			return PreparedDirectArchiveManifest{}, err
		}
		return PreparedDirectArchiveManifest{
			Manifest:       manifest,
			ManifestBytes:  append([]byte(nil), manifestBytes...),
			ManifestSHA256: manifestSHA,
			HashFileBytes:  append([]byte(nil), hashFileBytes...),
		}, nil
	case DirectArchiveManifestSchemaV2:
		var manifest DirectArchiveManifestV2
		if err := decodeExactDirectArchiveManifest(manifestBytes, &manifest); err != nil {
			return PreparedDirectArchiveManifest{}, err
		}
		if err := validateDirectArchiveManifestV2(manifest); err != nil {
			return PreparedDirectArchiveManifest{}, err
		}
		return PreparedDirectArchiveManifest{
			ManifestV2:     &manifest,
			ManifestBytes:  append([]byte(nil), manifestBytes...),
			ManifestSHA256: manifestSHA,
			HashFileBytes:  append([]byte(nil), hashFileBytes...),
		}, nil
	default:
		return PreparedDirectArchiveManifest{}, fmt.Errorf("%w: unsupported archive manifest schema %q", ErrDirectArchiveRequestInvalid, schema)
	}
}

// VerifyExtractedDirectArchive authenticates an isolated Borg extraction
// against the exact direct-archive manifest. It never follows symlinks and it
// rejects undeclared files, special nodes, metadata drift, and hard-link
// topology drift. Ancestor directories synthesized while extracting absolute
// archive paths are allowed only when they lead to a declared entry.
func VerifyExtractedDirectArchive(ctx context.Context, root string, prepared PreparedDirectArchiveManifest) (DirectArchiveExtractionVerification, error) {
	verification := DirectArchiveExtractionVerification{
		Status: "failed", ManifestSHA256: prepared.ManifestSHA256,
		Checks: map[string]string{}, Errors: []string{},
	}
	root, err := exactAbsolutePath(root)
	if err != nil {
		return verification, err
	}
	verification.Root = root
	if err := requireRealDirectory(root); err != nil {
		return verification, err
	}
	if prepared.ManifestV2 != nil {
		return verifyExtractedDirectArchiveV2(ctx, root, prepared, verification)
	}
	if err := validateDirectArchiveManifest(prepared.Manifest); err != nil {
		return verification, err
	}
	digest := sha256.Sum256(prepared.ManifestBytes)
	if prepared.ManifestSHA256 != hex.EncodeToString(digest[:]) || string(prepared.HashFileBytes) != prepared.ManifestSHA256+"  "+DirectArchiveManifestFile+"\n" {
		return verification, fmt.Errorf("%w: extracted archive manifest identity is invalid", ErrDirectArchiveRequestInvalid)
	}
	expected, ancestors, err := extractedDirectArchiveExpected(prepared)
	if err != nil {
		return verification, err
	}
	actualInfo := make(map[string]os.FileInfo, len(expected))
	seen := make(map[string]struct{}, len(expected))
	err = filepath.WalkDir(root, func(current string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		relative, err := filepath.Rel(root, current)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		if relative == "." {
			return nil
		}
		want, declared := expected[relative]
		if !declared {
			if _, allowed := ancestors[relative]; allowed && entry.IsDir() {
				info, statErr := os.Lstat(current)
				if statErr != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
					return fmt.Errorf("synthetic archive ancestor %q is unsafe", relative)
				}
				return nil
			}
			return fmt.Errorf("extracted archive contains undeclared entry %q", relative)
		}
		actual, _, _, inspectErr := inspectDirectArchiveEntry(current, relative)
		if inspectErr != nil {
			return fmt.Errorf("inspect extracted archive entry %q: %w", relative, inspectErr)
		}
		if err := compareExtractedDirectArchiveEntry(actual, want); err != nil {
			return fmt.Errorf("extracted archive entry %q: %w", relative, err)
		}
		info, statErr := os.Lstat(current)
		if statErr != nil {
			return statErr
		}
		actualInfo[relative] = info
		seen[relative] = struct{}{}
		return nil
	})
	if err != nil {
		verification.Errors = append(verification.Errors, err.Error())
		return verification, nil
	}
	for relative := range expected {
		if _, ok := seen[relative]; !ok {
			verification.Errors = append(verification.Errors, "extracted archive is missing "+relative)
		}
	}
	if len(verification.Errors) > 0 {
		return verification, nil
	}
	if err := verifyExtractedDirectArchiveHardlinks(expected, actualInfo); err != nil {
		verification.Errors = append(verification.Errors, err.Error())
		return verification, nil
	}
	verification.Checks["manifest_identity"] = "succeeded"
	verification.Checks["declared_entries"] = "succeeded"
	verification.Checks["metadata_and_payload"] = "succeeded"
	verification.Checks["hardlink_topology"] = "succeeded"
	verification.Status = "succeeded"
	return verification, nil
}

func verifyExtractedDirectArchiveV2(ctx context.Context, root string, prepared PreparedDirectArchiveManifest, verification DirectArchiveExtractionVerification) (DirectArchiveExtractionVerification, error) {
	manifest := prepared.ManifestV2
	if manifest == nil {
		return verification, fmt.Errorf("%w: v2 archive manifest is missing", ErrDirectArchiveRequestInvalid)
	}
	if err := validateDirectArchiveManifestV2(*manifest); err != nil {
		return verification, err
	}
	digest := sha256.Sum256(prepared.ManifestBytes)
	if prepared.ManifestSHA256 != hex.EncodeToString(digest[:]) || string(prepared.HashFileBytes) != prepared.ManifestSHA256+"  "+DirectArchiveManifestFile+"\n" {
		return verification, fmt.Errorf("%w: extracted v2 archive manifest identity is invalid", ErrDirectArchiveRequestInvalid)
	}

	expected, userRoots, ancestors, err := extractedDirectArchiveV2Contract(prepared)
	if err != nil {
		return verification, err
	}
	actualInfo := make(map[string]os.FileInfo, len(expected))
	seenExpected := make(map[string]struct{}, len(expected))
	seenUserRoots := make(map[string]struct{}, len(userRoots))
	seenUserSymlinks := make(map[string]struct{}, len(prepared.V2UserSymlinkTargets))
	err = filepath.WalkDir(root, func(current string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		relative, err := filepath.Rel(root, current)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		if relative == "." {
			return nil
		}
		if path.IsAbs(relative) || relative != path.Clean(relative) || relative == ".." || strings.HasPrefix(relative, "../") {
			return fmt.Errorf("extracted v2 archive path %q escapes the extraction root", relative)
		}

		if want, declared := expected[relative]; declared {
			actual, _, _, inspectErr := inspectDirectArchiveEntry(current, relative)
			if inspectErr != nil {
				return fmt.Errorf("inspect extracted v2 package entry %q: %w", relative, inspectErr)
			}
			if err := compareExtractedDirectArchiveEntry(actual, want); err != nil {
				return fmt.Errorf("extracted v2 package entry %q: %w", relative, err)
			}
			info, statErr := os.Lstat(current)
			if statErr != nil {
				return statErr
			}
			actualInfo[relative] = info
			seenExpected[relative] = struct{}{}
			return nil
		}

		if userRoot, allowed := containingDirectArchiveV2Root(relative, userRoots); allowed {
			target, isSymlink, err := verifyExtractedDirectArchiveV2UserEntry(current, relative, userRoot)
			if err != nil {
				return err
			}
			if prepared.V2UserSymlinkTargets != nil {
				expectedTarget, expectedSymlink := prepared.V2UserSymlinkTargets[relative]
				if isSymlink != expectedSymlink || (isSymlink && target != expectedTarget) {
					return fmt.Errorf("extracted v2 user symlink %q does not match authenticated archive evidence", relative)
				}
				if isSymlink {
					seenUserSymlinks[relative] = struct{}{}
				}
			}
			if relative == userRoot {
				seenUserRoots[userRoot] = struct{}{}
			}
			return nil
		}

		if _, allowed := ancestors[relative]; allowed && entry.IsDir() {
			info, statErr := os.Lstat(current)
			if statErr != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
				return fmt.Errorf("synthetic v2 archive ancestor %q is unsafe", relative)
			}
			return nil
		}
		return fmt.Errorf("extracted v2 archive contains unexpected payload root or path %q", relative)
	})
	if err != nil {
		verification.Errors = append(verification.Errors, err.Error())
		return verification, nil
	}
	for relative := range expected {
		if _, ok := seenExpected[relative]; !ok {
			verification.Errors = append(verification.Errors, "extracted v2 archive is missing "+relative)
		}
	}
	for relative := range userRoots {
		if _, ok := seenUserRoots[relative]; !ok {
			verification.Errors = append(verification.Errors, "extracted v2 archive is missing declared user root "+relative)
		}
	}
	for relative := range prepared.V2UserSymlinkTargets {
		if _, ok := seenUserSymlinks[relative]; !ok {
			verification.Errors = append(verification.Errors, "extracted v2 archive is missing authenticated user symlink "+relative)
		}
	}
	if len(verification.Errors) > 0 {
		return verification, nil
	}
	if err := verifyExtractedDirectArchiveHardlinks(expected, actualInfo); err != nil {
		verification.Errors = append(verification.Errors, err.Error())
		return verification, nil
	}

	operationalDir := filepath.Join(root, filepath.FromSlash(manifest.OperationalPackage.ArchivePath))
	schemaHead := manifest.OperationalPackage.SchemaHead
	operational, err := VerifyOperationalPackage(ctx, OperationalPackageVerificationInput{
		PackageDir: operationalDir, ExpectedManifestSHA256: manifest.OperationalPackage.ManifestSHA256,
		ExpectedPackageID: manifest.OperationalPackage.PackageID, ExpectedSchemaHead: &schemaHead,
	})
	if err != nil {
		return verification, fmt.Errorf("reverify extracted v2 operational package: %w", err)
	}
	if operational.Status != "succeeded" || operational.Manifest == nil ||
		operational.PackageID != manifest.OperationalPackage.PackageID ||
		operational.ManifestSHA256 != manifest.OperationalPackage.ManifestSHA256 ||
		operational.SchemaHead != manifest.OperationalPackage.SchemaHead ||
		!operational.CreatedAt.Equal(manifest.OperationalPackage.CreatedAt) ||
		operational.ArtifactCount != manifest.OperationalPackage.ArtifactCount ||
		operational.TotalBytes != manifest.OperationalPackage.TotalBytes {
		verification.Errors = append(verification.Errors, "extracted v2 operational package does not match authenticated package identity")
		return verification, nil
	}

	verification.Checks["manifest_identity"] = "succeeded"
	verification.Checks["declared_payload_roots"] = "succeeded"
	verification.Checks["user_payload_confinement"] = "succeeded"
	verification.Checks["bounded_package_entries"] = "succeeded"
	verification.Checks["operational_package"] = "succeeded"
	verification.Status = "succeeded"
	return verification, nil
}

func verifyExtractedDirectArchiveV2UserEntry(current, relative, userRoot string) (string, bool, error) {
	info, err := os.Lstat(current)
	if err != nil {
		return "", false, err
	}
	if relative == userRoot {
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return "", false, fmt.Errorf("declared v2 user root %q is not a real directory", relative)
		}
		return "", false, nil
	}
	switch {
	case info.IsDir(), info.Mode().IsRegular():
		return "", false, nil
	case info.Mode()&os.ModeSymlink != 0:
		// Readlink authenticates the entry text without resolving or opening the
		// referent. Absolute and root-external targets are deliberately valid for
		// user-data leaves; pre-extraction archive validation guarantees that no
		// archived descendant can turn this inert link into an extraction path.
		target, readErr := os.Readlink(current)
		if readErr != nil {
			return "", false, readErr
		}
		if !utf8.ValidString(target) || target == "" || strings.IndexByte(target, 0) >= 0 {
			return "", false, fmt.Errorf("extracted v2 symlink %q has an invalid target", relative)
		}
		return target, true, nil
	default:
		return "", false, fmt.Errorf("extracted v2 user payload %q has unsupported file type", relative)
	}
}

func extractedDirectArchiveV2Contract(prepared PreparedDirectArchiveManifest) (map[string]DirectArchiveEntry, map[string]struct{}, map[string]struct{}, error) {
	manifest := prepared.ManifestV2
	if manifest == nil {
		return nil, nil, nil, fmt.Errorf("%w: v2 archive manifest is missing", ErrDirectArchiveRequestInvalid)
	}
	expected := make(map[string]DirectArchiveEntry)
	add := func(archiveRoot string, entries []DirectArchiveEntry) error {
		for _, entry := range entries {
			relative := directArchiveEntryPath(archiveRoot, entry.Path)
			if _, exists := expected[relative]; exists {
				return fmt.Errorf("duplicate extracted v2 package path %q", relative)
			}
			expected[relative] = entry
		}
		return nil
	}
	if err := add(manifest.OperationalPackage.ArchivePath, manifest.OperationalPackage.Entries); err != nil {
		return nil, nil, nil, err
	}
	if err := add(manifest.ProvenancePackage.ArchivePath, manifest.ProvenancePackage.Entries); err != nil {
		return nil, nil, nil, err
	}
	evidenceTime := DirectArchiveEvidenceModifiedUnixNano(manifest.CreatedAt)
	manifestDigest := sha256.Sum256(prepared.ManifestBytes)
	hashDigest := sha256.Sum256(prepared.HashFileBytes)
	expected[DirectArchiveEvidenceDir] = DirectArchiveEntry{Path: ".", Type: "directory", Mode: 0o700, ModifiedUnixNano: evidenceTime}
	expected[path.Join(DirectArchiveEvidenceDir, DirectArchiveManifestFile)] = DirectArchiveEntry{Path: DirectArchiveManifestFile, Type: "file", Mode: 0o600, ModifiedUnixNano: evidenceTime, SizeBytes: int64(len(prepared.ManifestBytes)), SHA256: hex.EncodeToString(manifestDigest[:])}
	expected[path.Join(DirectArchiveEvidenceDir, DirectArchiveManifestHashFile)] = DirectArchiveEntry{Path: DirectArchiveManifestHashFile, Type: "file", Mode: 0o600, ModifiedUnixNano: evidenceTime, SizeBytes: int64(len(prepared.HashFileBytes)), SHA256: hex.EncodeToString(hashDigest[:])}

	userRoots := make(map[string]struct{}, len(manifest.Roots))
	for _, archiveRoot := range manifest.Roots {
		if _, exists := expected[archiveRoot.ArchivePath]; exists {
			return nil, nil, nil, fmt.Errorf("declared v2 user root collides with bounded package path %q", archiveRoot.ArchivePath)
		}
		userRoots[archiveRoot.ArchivePath] = struct{}{}
	}
	ancestors := make(map[string]struct{})
	addAncestors := func(relative string) {
		parent := path.Dir(relative)
		for parent != "." && parent != "/" {
			if _, declared := expected[parent]; declared {
				break
			}
			if _, declared := userRoots[parent]; declared {
				break
			}
			ancestors[parent] = struct{}{}
			parent = path.Dir(parent)
		}
	}
	for relative := range expected {
		addAncestors(relative)
	}
	for relative := range userRoots {
		addAncestors(relative)
	}
	return expected, userRoots, ancestors, nil
}

func containingDirectArchiveV2Root(relative string, roots map[string]struct{}) (string, bool) {
	for root := range roots {
		if directArchiveV2PathWithin(relative, root) {
			return root, true
		}
	}
	return "", false
}

func directArchiveV2PathWithin(relative, root string) bool {
	return relative == root || strings.HasPrefix(relative, root+"/")
}

func extractedDirectArchiveExpected(prepared PreparedDirectArchiveManifest) (map[string]DirectArchiveEntry, map[string]struct{}, error) {
	expected := make(map[string]DirectArchiveEntry)
	add := func(archiveRoot string, entries []DirectArchiveEntry) error {
		for _, entry := range entries {
			relative := directArchiveEntryPath(archiveRoot, entry.Path)
			if _, exists := expected[relative]; exists {
				return fmt.Errorf("duplicate extracted archive path %q", relative)
			}
			expected[relative] = entry
		}
		return nil
	}
	for _, root := range prepared.Manifest.Roots {
		if err := add(root.ArchivePath, root.Entries); err != nil {
			return nil, nil, err
		}
	}
	if err := add(prepared.Manifest.OperationalPackage.ArchivePath, prepared.Manifest.OperationalPackage.Entries); err != nil {
		return nil, nil, err
	}
	if err := add(prepared.Manifest.ProvenancePackage.ArchivePath, prepared.Manifest.ProvenancePackage.Entries); err != nil {
		return nil, nil, err
	}
	evidenceTime := DirectArchiveEvidenceModifiedUnixNano(prepared.Manifest.CreatedAt)
	manifestDigest := sha256.Sum256(prepared.ManifestBytes)
	hashDigest := sha256.Sum256(prepared.HashFileBytes)
	expected[DirectArchiveEvidenceDir] = DirectArchiveEntry{Path: ".", Type: "directory", Mode: 0o700, ModifiedUnixNano: evidenceTime}
	expected[path.Join(DirectArchiveEvidenceDir, DirectArchiveManifestFile)] = DirectArchiveEntry{Path: DirectArchiveManifestFile, Type: "file", Mode: 0o600, ModifiedUnixNano: evidenceTime, SizeBytes: int64(len(prepared.ManifestBytes)), SHA256: hex.EncodeToString(manifestDigest[:])}
	expected[path.Join(DirectArchiveEvidenceDir, DirectArchiveManifestHashFile)] = DirectArchiveEntry{Path: DirectArchiveManifestHashFile, Type: "file", Mode: 0o600, ModifiedUnixNano: evidenceTime, SizeBytes: int64(len(prepared.HashFileBytes)), SHA256: hex.EncodeToString(hashDigest[:])}
	ancestors := make(map[string]struct{})
	for relative := range expected {
		parent := path.Dir(relative)
		for parent != "." && parent != "/" {
			if _, declared := expected[parent]; declared {
				break
			}
			ancestors[parent] = struct{}{}
			parent = path.Dir(parent)
		}
	}
	return expected, ancestors, nil
}

func compareExtractedDirectArchiveEntry(actual, expected DirectArchiveEntry) error {
	if actual.Type != expected.Type || actual.Mode != expected.Mode || actual.ModifiedUnixNano != expected.ModifiedUnixNano {
		return fmt.Errorf("type, mode, or mtime does not match authenticated evidence (actual type=%s mode=%#o mtime=%d; expected type=%s mode=%#o mtime=%d)", actual.Type, actual.Mode, actual.ModifiedUnixNano, expected.Type, expected.Mode, expected.ModifiedUnixNano)
	}
	switch expected.Type {
	case "file":
		if actual.SizeBytes != expected.SizeBytes || actual.SHA256 != expected.SHA256 {
			return fmt.Errorf("file size or SHA-256 does not match authenticated evidence")
		}
	case "symlink":
		if actual.LinkTarget != expected.LinkTarget || actual.SizeBytes != expected.SizeBytes || actual.SHA256 != expected.SHA256 {
			return fmt.Errorf("symlink target does not match authenticated evidence")
		}
	}
	return nil
}

func verifyExtractedDirectArchiveHardlinks(expected map[string]DirectArchiveEntry, actual map[string]os.FileInfo) error {
	identityOwner := make(map[directArchiveHardlinkKey]string)
	for relative, want := range expected {
		if want.Type != "file" {
			continue
		}
		info := actual[relative]
		device, inode := fileIdentity(info)
		key := directArchiveHardlinkKey{device: device, inode: inode}
		if want.HardlinkTarget != "" {
			target := actual[want.HardlinkTarget]
			targetDevice, targetInode := fileIdentity(target)
			if device == 0 || inode == 0 || device != targetDevice || inode != targetInode {
				return fmt.Errorf("hard-link %q does not share identity with %q", relative, want.HardlinkTarget)
			}
			continue
		}
		if owner, exists := identityOwner[key]; exists && key != (directArchiveHardlinkKey{}) {
			return fmt.Errorf("undeclared hard-link identity is shared by %q and %q", owner, relative)
		}
		identityOwner[key] = relative
	}
	return nil
}

func normalizeDirectArchiveRequest(request DirectArchiveRequest) (DirectArchiveRequest, error) {
	return normalizeDirectArchiveRequestWithBoxRoots(request, false)
}

func normalizeDirectArchiveRequestWithBoxRoots(request DirectArchiveRequest, allowEmptyBoxRoots bool) (DirectArchiveRequest, error) {
	if request.Schema != DirectArchiveRequestSchema {
		return request, fmt.Errorf("%w: unsupported request schema %q", ErrDirectArchiveRequestInvalid, request.Schema)
	}
	if err := validateArchiveIdentity(request.NodeID); err != nil {
		return request, err
	}
	if err := validateArchiveIdentity(request.ArchiveRef); err != nil {
		return request, err
	}
	if strings.TrimSpace(request.ArchiveClass) != "" {
		archiveClass, err := NormalizeDirectArchiveClass(request.ArchiveClass)
		if err != nil {
			return request, err
		}
		request.ArchiveClass = archiveClass
	}
	if request.CreatedAt.IsZero() || request.CreatedAt.Location() != time.UTC {
		return request, fmt.Errorf("%w: created_at must be a non-zero UTC timestamp", ErrDirectArchiveRequestInvalid)
	}
	if len(request.Roots) == 0 {
		return request, fmt.Errorf("%w: at least one canonical root is required", ErrDirectArchiveRequestInvalid)
	}
	seenNames := make(map[string]struct{}, len(request.Roots))
	seenPaths := make(map[string]struct{}, len(request.Roots))
	for index := range request.Roots {
		root := request.Roots[index]
		if err := validateArchiveIdentity(root.Name); err != nil {
			return request, err
		}
		path, err := directArchiveAbsoluteDirectory(root.Path)
		if err != nil {
			return request, fmt.Errorf("%w: canonical root %q: %v", ErrDirectArchiveRequestInvalid, root.Name, err)
		}
		allowEmpty := allowEmptyBoxRoots && (root.Name == "box_topics" || root.Name == "box_library" || root.Name == "application_data")
		if err := requireReadableDirectArchiveDirectory(path, allowEmpty); err != nil {
			return request, fmt.Errorf("%w: canonical root %q: %v", ErrDirectArchiveRequestInvalid, root.Name, err)
		}
		if directArchivePortablePathsOverlap(directArchivePath(path), DirectArchiveEvidenceDir) {
			return request, fmt.Errorf("%w: canonical root %q collides with reserved archive evidence", ErrDirectArchiveRequestInvalid, root.Name)
		}
		if _, exists := seenNames[root.Name]; exists {
			return request, fmt.Errorf("%w: duplicate canonical root name %q", ErrDirectArchiveRequestInvalid, root.Name)
		}
		if _, exists := seenPaths[path]; exists {
			return request, fmt.Errorf("%w: duplicate canonical root path", ErrDirectArchiveRequestInvalid)
		}
		seenNames[root.Name] = struct{}{}
		seenPaths[path] = struct{}{}
		request.Roots[index].Path = path
	}
	sort.Slice(request.Roots, func(i, j int) bool { return request.Roots[i].Name < request.Roots[j].Name })
	for i := range request.Roots {
		for j := i + 1; j < len(request.Roots); j++ {
			if directArchivePathsOverlap(request.Roots[i].Path, request.Roots[j].Path) {
				return request, fmt.Errorf("%w: canonical roots overlap", ErrDirectArchiveRequestInvalid)
			}
		}
	}

	packagePath, err := directArchiveAbsoluteDirectory(request.OperationalPackage.Path)
	if err != nil {
		return request, fmt.Errorf("%w: operational package: %v", ErrDirectArchiveRequestInvalid, err)
	}
	request.OperationalPackage.Path = packagePath
	if directArchivePortablePathsOverlap(directArchivePath(packagePath), DirectArchiveEvidenceDir) {
		return request, fmt.Errorf("%w: operational package collides with reserved archive evidence", ErrDirectArchiveRequestInvalid)
	}
	if normalizeSHA256(request.OperationalPackage.ManifestSHA256) == "" {
		return request, fmt.Errorf("%w: operational package manifest identity is required", ErrDirectArchiveRequestInvalid)
	}
	if err := validatePackageID(request.OperationalPackage.PackageID); err != nil {
		return request, err
	}
	for _, root := range request.Roots {
		if directArchivePathsOverlap(root.Path, packagePath) {
			return request, fmt.Errorf("%w: operational package must not overlap a canonical root", ErrDirectArchiveRequestInvalid)
		}
	}
	provenancePath, err := directArchiveAbsoluteDirectory(request.ProvenancePackage.Path)
	if err != nil {
		return request, fmt.Errorf("%w: provenance package: %v", ErrDirectArchiveRequestInvalid, err)
	}
	request.ProvenancePackage.Path = provenancePath
	if directArchivePortablePathsOverlap(directArchivePath(provenancePath), DirectArchiveEvidenceDir) {
		return request, fmt.Errorf("%w: provenance package collides with reserved archive evidence", ErrDirectArchiveRequestInvalid)
	}
	if normalizeSHA256(request.ProvenancePackage.ManifestSHA256) == "" {
		return request, fmt.Errorf("%w: provenance package manifest identity is required", ErrDirectArchiveRequestInvalid)
	}
	if request.ProvenancePackage.Verify == nil {
		return request, fmt.Errorf("%w: provenance package independent verifier is required", ErrDirectArchiveRequestInvalid)
	}
	if err := validatePackageID(request.ProvenancePackage.PackageID); err != nil {
		return request, err
	}
	if filepath.Base(provenancePath) != request.ProvenancePackage.PackageID {
		return request, fmt.Errorf("%w: provenance package path does not match package id", ErrDirectArchiveRequestInvalid)
	}
	if directArchivePathsOverlap(packagePath, provenancePath) {
		return request, fmt.Errorf("%w: operational and provenance packages overlap", ErrDirectArchiveRequestInvalid)
	}
	for _, root := range request.Roots {
		if directArchivePathsOverlap(root.Path, provenancePath) {
			return request, fmt.Errorf("%w: provenance package must not overlap a canonical root", ErrDirectArchiveRequestInvalid)
		}
	}

	for index := range request.Exclusions {
		exclusion := request.Exclusions[index]
		if _, ok := seenNames[exclusion.Root]; !ok {
			return request, fmt.Errorf("%w: exclusion names unknown root %q", ErrDirectArchiveRequestInvalid, exclusion.Root)
		}
		relative, err := directArchiveRelativePath(exclusion.RelativePath)
		if err != nil {
			return request, fmt.Errorf("%w: exclusion for %q: %v", ErrDirectArchiveRequestInvalid, exclusion.Root, err)
		}
		request.Exclusions[index].RelativePath = relative
	}
	sort.Slice(request.Exclusions, func(i, j int) bool {
		if request.Exclusions[i].Root == request.Exclusions[j].Root {
			return request.Exclusions[i].RelativePath < request.Exclusions[j].RelativePath
		}
		return request.Exclusions[i].Root < request.Exclusions[j].Root
	})
	for index := 1; index < len(request.Exclusions); index++ {
		if request.Exclusions[index] == request.Exclusions[index-1] {
			return request, fmt.Errorf("%w: duplicate exclusion", ErrDirectArchiveRequestInvalid)
		}
	}
	return request, nil
}

func NormalizeDirectArchiveClass(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.ReplaceAll(value, "-", "_")
	if value == "" {
		return DirectArchiveClassUserData, nil
	}
	switch value {
	case DirectArchiveClassUserData, DirectArchiveClassAcceptance, DirectArchiveClassMilestone:
		return value, nil
	default:
		return "", fmt.Errorf("%w: unsupported archive class %q", ErrDirectArchiveRequestInvalid, value)
	}
}

type directArchiveHardlinkKey struct {
	device uint64
	inode  uint64
}

func inspectDirectArchiveRoot(ctx context.Context, root DirectArchiveRoot, exclusions []string, hardlinks map[directArchiveHardlinkKey]string) (DirectArchiveRootManifest, error) {
	manifest := DirectArchiveRootManifest{
		Name:        root.Name,
		SourcePath:  root.Path,
		ArchivePath: directArchivePath(root.Path),
	}
	entries, totalBytes, err := inspectDirectArchiveTree(ctx, root.Path, manifest.ArchivePath, exclusions, hardlinks)
	if err != nil {
		return DirectArchiveRootManifest{}, fmt.Errorf("inspect canonical root %q: %w", root.Name, err)
	}
	manifest.Entries = entries
	manifest.EntryCount = int64(len(entries))
	manifest.TotalBytes = totalBytes
	manifest.EntriesSHA256, err = directArchiveEntriesDigest(entries)
	if err != nil {
		return DirectArchiveRootManifest{}, err
	}
	return manifest, nil
}

func inspectDirectArchiveTree(ctx context.Context, sourcePath, archivePath string, exclusions []string, hardlinks map[directArchiveHardlinkKey]string) ([]DirectArchiveEntry, int64, error) {
	entries := []DirectArchiveEntry{}
	var totalBytes int64
	err := filepath.WalkDir(sourcePath, func(currentPath string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		relative, err := filepath.Rel(sourcePath, currentPath)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		if !utf8.ValidString(relative) {
			return fmt.Errorf("root-relative path is not valid UTF-8")
		}
		if relative != "." && directArchivePathExcluded(relative, exclusions) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		inspected, device, inode, err := inspectDirectArchiveEntry(currentPath, relative)
		if err != nil {
			return err
		}
		actualArchivePath := archivePath
		if relative != "." {
			actualArchivePath = filepath.ToSlash(filepath.Join(archivePath, relative))
		}
		if inspected.Type == "file" && device != 0 && inode != 0 {
			key := directArchiveHardlinkKey{device: device, inode: inode}
			if target, exists := hardlinks[key]; exists {
				inspected.HardlinkTarget = target
			} else {
				hardlinks[key] = actualArchivePath
			}
		}
		entries = append(entries, inspected)
		if inspected.Type == "file" {
			totalBytes += inspected.SizeBytes
		}
		return nil
	})
	if err != nil {
		return nil, 0, err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	return entries, totalBytes, nil
}

func inspectDirectArchiveEntry(path, relative string) (DirectArchiveEntry, uint64, uint64, error) {
	before, err := os.Lstat(path)
	if err != nil {
		return DirectArchiveEntry{}, 0, 0, err
	}
	entry := DirectArchiveEntry{
		Path: relative, Mode: directArchiveMode(before.Mode()), ModifiedUnixNano: directArchiveModifiedUnixNano(before.ModTime()),
	}
	switch {
	case before.IsDir():
		entry.Type = "directory"
		return entry, 0, 0, nil
	case before.Mode()&os.ModeSymlink != 0:
		target, err := os.Readlink(path)
		if err != nil {
			return DirectArchiveEntry{}, 0, 0, err
		}
		if !utf8.ValidString(target) {
			return DirectArchiveEntry{}, 0, 0, fmt.Errorf("symbolic link target is not valid UTF-8")
		}
		after, err := os.Lstat(path)
		if err != nil || !sameFileSnapshot(before, after) {
			return DirectArchiveEntry{}, 0, 0, fmt.Errorf("symbolic link changed while being inspected")
		}
		digest := sha256.Sum256([]byte(target))
		entry.Type = "symlink"
		entry.LinkTarget = target
		entry.SizeBytes = int64(len(target))
		entry.SHA256 = hex.EncodeToString(digest[:])
		return entry, 0, 0, nil
	case before.Mode().IsRegular():
		fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		if err != nil {
			return DirectArchiveEntry{}, 0, 0, err
		}
		file := os.NewFile(uintptr(fd), path)
		if file == nil {
			_ = unix.Close(fd)
			return DirectArchiveEntry{}, 0, 0, fmt.Errorf("open regular file")
		}
		defer file.Close()
		opened, err := file.Stat()
		if err != nil || !opened.Mode().IsRegular() || !os.SameFile(before, opened) {
			return DirectArchiveEntry{}, 0, 0, fmt.Errorf("regular file changed while opening")
		}
		hash := sha256.New()
		size, err := io.Copy(hash, file)
		if err != nil {
			return DirectArchiveEntry{}, 0, 0, err
		}
		after, err := file.Stat()
		if err != nil {
			return DirectArchiveEntry{}, 0, 0, err
		}
		pathAfter, err := os.Lstat(path)
		if err != nil || size != opened.Size() || !sameFileSnapshot(opened, after) || !sameFileSnapshot(after, pathAfter) {
			return DirectArchiveEntry{}, 0, 0, fmt.Errorf("regular file changed while being read")
		}
		entry.Type = "file"
		entry.SizeBytes = size
		entry.SHA256 = hex.EncodeToString(hash.Sum(nil))
		entry.Mode = directArchiveMode(after.Mode())
		entry.ModifiedUnixNano = directArchiveModifiedUnixNano(after.ModTime())
		device, inode := fileIdentity(after)
		return entry, device, inode, nil
	default:
		return DirectArchiveEntry{}, 0, 0, fmt.Errorf("unsupported special file at %s", path)
	}
}

func validateDirectArchiveManifest(manifest DirectArchiveManifest) error {
	hermesRoots := make([]DirectArchiveRoot, 0, len(manifest.Roots))
	for _, root := range manifest.Roots {
		hermesRoots = append(hermesRoots, DirectArchiveRoot{root.Name, root.SourcePath})
	}
	if err := validateHermesEvidence(manifest.HermesRecovery, hermesRoots, manifest.Exclusions); err != nil {
		return err
	}

	if manifest.Schema != DirectArchiveManifestSchema || manifest.Backend != DirectArchiveBackendBorg {
		return fmt.Errorf("%w: unsupported archive manifest schema or backend", ErrDirectArchiveRequestInvalid)
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
	if manifest.ArchiveClass == "" {
		// v1 manifests written before archive classes were introduced remain
		// readable, but retention treats them as legacy/protected inventory.
		if manifest.ArchiveName != "__loom-direct-"+manifest.NodeID+"-"+manifest.ArchiveRef {
			return fmt.Errorf("%w: legacy archive name is not bound to node and reference", ErrDirectArchiveRequestInvalid)
		}
	} else {
		archiveClass, err := NormalizeDirectArchiveClass(manifest.ArchiveClass)
		if err != nil || archiveClass != manifest.ArchiveClass {
			return fmt.Errorf("%w: direct archive class is invalid", ErrDirectArchiveRequestInvalid)
		}
		classToken := strings.ReplaceAll(archiveClass, "_", "-")
		if manifest.ArchiveName != "__loom-direct-"+classToken+"-"+manifest.NodeID+"-"+manifest.ArchiveRef {
			return fmt.Errorf("%w: archive name is not bound to class, node, and reference", ErrDirectArchiveRequestInvalid)
		}
	}
	if manifest.CreatedAt.IsZero() || manifest.CreatedAt.Location() != time.UTC || manifest.ExclusionPolicy != DirectArchiveExclusionPolicy || len(manifest.Roots) == 0 {
		return fmt.Errorf("%w: incomplete archive manifest identity", ErrDirectArchiveRequestInvalid)
	}
	rootNames := make(map[string]struct{}, len(manifest.Roots))
	rootPaths := make([]string, 0, len(manifest.Roots))
	archiveEntries := make(map[string]DirectArchiveEntry)
	exclusionsByRoot := directArchiveExclusionsByRoot(manifest.Exclusions)
	for rootIndex, root := range manifest.Roots {
		if err := validateArchiveIdentity(root.Name); err != nil {
			return err
		}
		sourcePath, err := exactAbsolutePath(root.SourcePath)
		if err != nil || sourcePath == string(filepath.Separator) || directArchivePath(sourcePath) != root.ArchivePath || directArchivePortablePathsOverlap(root.ArchivePath, DirectArchiveEvidenceDir) || root.EntryCount != int64(len(root.Entries)) {
			return fmt.Errorf("%w: root %q has invalid path or count evidence", ErrDirectArchiveRequestInvalid, root.Name)
		}
		if rootIndex > 0 && manifest.Roots[rootIndex-1].Name >= root.Name {
			return fmt.Errorf("%w: manifest roots are not uniquely sorted", ErrDirectArchiveRequestInvalid)
		}
		if _, exists := rootNames[root.Name]; exists {
			return fmt.Errorf("%w: duplicate root in manifest", ErrDirectArchiveRequestInvalid)
		}
		rootNames[root.Name] = struct{}{}
		rootPaths = append(rootPaths, sourcePath)
		digest, err := directArchiveEntriesDigest(root.Entries)
		if err != nil || digest != root.EntriesSHA256 {
			return fmt.Errorf("%w: root %q entries digest mismatch", ErrDirectArchiveRequestInvalid, root.Name)
		}
		var total int64
		for entryIndex, entry := range root.Entries {
			if err := validateDirectArchiveEntry(entry); err != nil {
				return err
			}
			archiveEntryPath := directArchiveEntryPath(root.ArchivePath, entry.Path)
			if err := validateDirectArchiveHardlink(entry, archiveEntries); err != nil {
				return err
			}
			if _, exists := archiveEntries[archiveEntryPath]; exists {
				return fmt.Errorf("%w: duplicate archive entry path", ErrDirectArchiveRequestInvalid)
			}
			archiveEntries[archiveEntryPath] = entry
			if entryIndex > 0 && root.Entries[entryIndex-1].Path >= entry.Path {
				return fmt.Errorf("%w: root %q entries are not uniquely sorted", ErrDirectArchiveRequestInvalid, root.Name)
			}
			if directArchivePathExcluded(entry.Path, exclusionsByRoot[root.Name]) {
				return fmt.Errorf("%w: excluded path is present in root evidence", ErrDirectArchiveRequestInvalid)
			}
			if entryIndex == 0 && (entry.Path != "." || entry.Type != "directory") {
				return fmt.Errorf("%w: root evidence does not start with the root directory", ErrDirectArchiveRequestInvalid)
			}
			if entry.Type == "file" {
				total += entry.SizeBytes
			}
		}
		if total != root.TotalBytes {
			return fmt.Errorf("%w: root %q byte total mismatch", ErrDirectArchiveRequestInvalid, root.Name)
		}
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
	op := manifest.OperationalPackage
	opSourcePath, err := exactAbsolutePath(op.SourcePath)
	if err != nil || opSourcePath == string(filepath.Separator) || directArchivePath(opSourcePath) != op.ArchivePath || directArchivePortablePathsOverlap(op.ArchivePath, DirectArchiveEvidenceDir) || normalizeSHA256(op.ManifestSHA256) == "" || op.TotalBytes <= 0 || op.ArtifactCount <= 0 || op.EntryCount != int64(len(op.Entries)) || op.CreatedAt.IsZero() || op.CreatedAt.Location() != time.UTC {
		return fmt.Errorf("%w: operational package evidence is incomplete", ErrDirectArchiveRequestInvalid)
	}
	for _, rootPath := range rootPaths {
		if directArchivePathsOverlap(rootPath, opSourcePath) {
			return fmt.Errorf("%w: operational package overlaps a canonical root", ErrDirectArchiveRequestInvalid)
		}
	}
	if err := validatePackageID(op.PackageID); err != nil {
		return err
	}
	opDigest, err := directArchiveEntriesDigest(op.Entries)
	if err != nil || opDigest != op.EntriesSHA256 {
		return fmt.Errorf("%w: operational package entries digest mismatch", ErrDirectArchiveRequestInvalid)
	}
	var operationalTotal int64
	for index, entry := range op.Entries {
		if err := validateDirectArchiveEntry(entry); err != nil {
			return err
		}
		archiveEntryPath := directArchiveEntryPath(op.ArchivePath, entry.Path)
		if err := validateDirectArchiveHardlink(entry, archiveEntries); err != nil {
			return err
		}
		if _, exists := archiveEntries[archiveEntryPath]; exists {
			return fmt.Errorf("%w: duplicate archive entry path", ErrDirectArchiveRequestInvalid)
		}
		archiveEntries[archiveEntryPath] = entry
		if index > 0 && op.Entries[index-1].Path >= entry.Path {
			return fmt.Errorf("%w: operational package entries are not uniquely sorted", ErrDirectArchiveRequestInvalid)
		}
		if index == 0 && (entry.Path != "." || entry.Type != "directory") {
			return fmt.Errorf("%w: operational package evidence does not start with the package directory", ErrDirectArchiveRequestInvalid)
		}
		if entry.Type == "file" {
			operationalTotal += entry.SizeBytes
		}
	}
	if operationalTotal != op.TotalBytes {
		return fmt.Errorf("%w: operational package byte total mismatch", ErrDirectArchiveRequestInvalid)
	}
	if !directArchiveEntriesContainSHA(op.Entries, OperationalManifestFile, op.ManifestSHA256) {
		return fmt.Errorf("%w: operational package manifest bytes are not bound", ErrDirectArchiveRequestInvalid)
	}
	prov := manifest.ProvenancePackage
	provSourcePath, err := exactAbsolutePath(prov.SourcePath)
	if err != nil || provSourcePath == string(filepath.Separator) || directArchivePath(provSourcePath) != prov.ArchivePath || directArchivePortablePathsOverlap(prov.ArchivePath, DirectArchiveEvidenceDir) || normalizeSHA256(prov.ManifestSHA256) == "" || prov.SchemaHead != provenance.SchemaHead || !validDirectArchiveGraphDigest(prov.GraphDigest) || prov.CompletedAt.IsZero() || prov.CompletedAt.Location() != time.UTC || prov.DumpSizeBytes <= 0 || prov.TotalBytes <= 0 || prov.EntryCount != int64(len(prov.Entries)) {
		return fmt.Errorf("%w: provenance package evidence is incomplete", ErrDirectArchiveRequestInvalid)
	}
	if err := validatePackageID(prov.PackageID); err != nil {
		return err
	}
	if directArchivePathsOverlap(opSourcePath, provSourcePath) {
		return fmt.Errorf("%w: operational and provenance packages overlap", ErrDirectArchiveRequestInvalid)
	}
	for _, rootPath := range rootPaths {
		if directArchivePathsOverlap(rootPath, provSourcePath) {
			return fmt.Errorf("%w: provenance package overlaps a canonical root", ErrDirectArchiveRequestInvalid)
		}
	}
	provDigest, err := directArchiveEntriesDigest(prov.Entries)
	if err != nil || provDigest != prov.EntriesSHA256 {
		return fmt.Errorf("%w: provenance package entries digest mismatch", ErrDirectArchiveRequestInvalid)
	}
	var provenanceTotal int64
	for index, entry := range prov.Entries {
		if err := validateDirectArchiveEntry(entry); err != nil {
			return err
		}
		archiveEntryPath := directArchiveEntryPath(prov.ArchivePath, entry.Path)
		if err := validateDirectArchiveHardlink(entry, archiveEntries); err != nil {
			return err
		}
		if _, exists := archiveEntries[archiveEntryPath]; exists {
			return fmt.Errorf("%w: duplicate archive entry path", ErrDirectArchiveRequestInvalid)
		}
		archiveEntries[archiveEntryPath] = entry
		if index > 0 && prov.Entries[index-1].Path >= entry.Path {
			return fmt.Errorf("%w: provenance package entries are not uniquely sorted", ErrDirectArchiveRequestInvalid)
		}
		if index == 0 && (entry.Path != "." || entry.Type != "directory") {
			return fmt.Errorf("%w: provenance package evidence does not start with the package directory", ErrDirectArchiveRequestInvalid)
		}
		if entry.Type == "file" {
			provenanceTotal += entry.SizeBytes
		}
	}
	if provenanceTotal != prov.TotalBytes || !directArchiveEntriesContainSHA(prov.Entries, provenance.RecoveryManifestFile, prov.ManifestSHA256) {
		return fmt.Errorf("%w: provenance package bytes or manifest identity are not bound", ErrDirectArchiveRequestInvalid)
	}
	digest, err := directArchiveSourceSnapshotDigest(manifest)
	if err != nil || digest != manifest.SourceSnapshotSHA256 {
		return fmt.Errorf("%w: source snapshot digest mismatch", ErrDirectArchiveRequestInvalid)
	}
	return nil
}

func validDirectArchiveGraphDigest(value string) bool {
	return strings.HasPrefix(value, "sha256:") && normalizeSHA256(strings.TrimPrefix(value, "sha256:")) != ""
}

func validateDirectArchiveEntry(entry DirectArchiveEntry) error {
	if !utf8.ValidString(entry.Path) || !utf8.ValidString(entry.LinkTarget) || !utf8.ValidString(entry.HardlinkTarget) || entry.Path == "" || entry.Path != filepath.ToSlash(filepath.Clean(entry.Path)) || filepath.IsAbs(entry.Path) || strings.HasPrefix(entry.Path, "../") || entry.Mode&^directArchiveModeMask() != 0 {
		return fmt.Errorf("%w: archive entry path or mode is invalid", ErrDirectArchiveRequestInvalid)
	}
	switch entry.Type {
	case "directory":
		if entry.SizeBytes != 0 || entry.SHA256 != "" || entry.LinkTarget != "" || entry.HardlinkTarget != "" {
			return fmt.Errorf("%w: directory entry has file evidence", ErrDirectArchiveRequestInvalid)
		}
	case "file":
		if entry.SizeBytes < 0 || normalizeSHA256(entry.SHA256) == "" || entry.LinkTarget != "" {
			return fmt.Errorf("%w: regular-file evidence is incomplete", ErrDirectArchiveRequestInvalid)
		}
	case "symlink":
		digest := sha256.Sum256([]byte(entry.LinkTarget))
		if entry.SizeBytes != int64(len(entry.LinkTarget)) || entry.SHA256 != hex.EncodeToString(digest[:]) || entry.HardlinkTarget != "" {
			return fmt.Errorf("%w: symbolic-link evidence is incomplete", ErrDirectArchiveRequestInvalid)
		}
	default:
		return fmt.Errorf("%w: unsupported archive entry type", ErrDirectArchiveRequestInvalid)
	}
	return nil
}

func directArchiveMode(mode os.FileMode) uint32 {
	result := uint32(mode.Perm())
	if mode&os.ModeSetuid != 0 {
		result |= 0o4000
	}
	if mode&os.ModeSetgid != 0 {
		result |= 0o2000
	}
	if mode&os.ModeSticky != 0 {
		result |= 0o1000
	}
	return result
}

func directArchiveModeMask() uint32 {
	return 0o7777
}

// DirectArchiveEvidenceModifiedUnixNano returns the canonical mtime for
// LOOM-generated .loom-direct-archive evidence. Evidence publication and all
// verifier paths truncate the authenticated archive CreatedAt to whole
// microseconds with integer arithmetic.
func DirectArchiveEvidenceModifiedUnixNano(value time.Time) int64 {
	return value.UnixNano() / int64(time.Microsecond) * int64(time.Microsecond)
}

func directArchiveModifiedUnixNano(value time.Time) int64 {
	// Borg 1.4's supported list formatter exposes item timestamps at
	// microsecond precision after converting nanoseconds through a floating
	// point Unix timestamp. Authenticate that exact representation so the
	// pending archive can be compared without extracting a staging tree.
	seconds := float64(value.UnixNano()) / float64(time.Second)
	whole, fraction := math.Modf(seconds)
	microseconds := math.Round(fraction * float64(time.Second/time.Microsecond))
	return int64(whole)*int64(time.Second) + int64(microseconds)*int64(time.Microsecond)
}

func directArchiveEntriesContainSHA(entries []DirectArchiveEntry, path, expectedSHA string) bool {
	for _, entry := range entries {
		if entry.Path == path && entry.Type == "file" && entry.SHA256 == expectedSHA {
			return true
		}
	}
	return false
}

func directArchiveEntryPath(archiveRoot, relative string) string {
	if relative == "." {
		return archiveRoot
	}
	return filepath.ToSlash(filepath.Join(archiveRoot, relative))
}

func validateDirectArchiveHardlink(entry DirectArchiveEntry, prior map[string]DirectArchiveEntry) error {
	if entry.HardlinkTarget == "" {
		return nil
	}
	if entry.Type != "file" || filepath.IsAbs(entry.HardlinkTarget) || entry.HardlinkTarget != filepath.ToSlash(filepath.Clean(entry.HardlinkTarget)) || strings.HasPrefix(entry.HardlinkTarget, "../") {
		return fmt.Errorf("%w: hard-link target is invalid", ErrDirectArchiveRequestInvalid)
	}
	target, exists := prior[entry.HardlinkTarget]
	if !exists || target.Type != "file" || target.SHA256 != entry.SHA256 || target.SizeBytes != entry.SizeBytes || target.Mode != entry.Mode || target.ModifiedUnixNano != entry.ModifiedUnixNano {
		return fmt.Errorf("%w: hard-link target evidence does not match", ErrDirectArchiveRequestInvalid)
	}
	return nil
}

func directArchiveEntriesDigest(entries []DirectArchiveEntry) (string, error) {
	raw, err := json.Marshal(entries)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:]), nil
}

func directArchiveSourceSnapshotDigest(manifest DirectArchiveManifest) (string, error) {
	type rootDigest struct {
		Name          string `json:"name"`
		SourcePath    string `json:"source_path"`
		ArchivePath   string `json:"archive_path"`
		EntryCount    int64  `json:"entry_count"`
		TotalBytes    int64  `json:"total_bytes"`
		EntriesSHA256 string `json:"entries_sha256"`
	}
	type sourceIdentity struct {
		ArchiveClass       string                                  `json:"archive_class,omitempty"`
		ExclusionPolicy    string                                  `json:"exclusion_policy"`
		Exclusions         []DirectArchiveExclusion                `json:"exclusions"`
		Roots              []rootDigest                            `json:"roots"`
		OperationalPackage DirectArchiveOperationalPackageManifest `json:"operational_package"`
		ProvenancePackage  DirectArchiveProvenancePackageManifest  `json:"provenance_package"`
	}
	identity := sourceIdentity{
		ArchiveClass:       manifest.ArchiveClass,
		ExclusionPolicy:    manifest.ExclusionPolicy,
		Exclusions:         manifest.Exclusions,
		OperationalPackage: manifest.OperationalPackage,
		ProvenancePackage:  manifest.ProvenancePackage,
	}
	for _, root := range manifest.Roots {
		identity.Roots = append(identity.Roots, rootDigest{
			Name: root.Name, SourcePath: root.SourcePath, ArchivePath: root.ArchivePath,
			EntryCount: root.EntryCount, TotalBytes: root.TotalBytes, EntriesSHA256: root.EntriesSHA256,
		})
	}
	raw, err := json.Marshal(identity)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:]), nil
}

func directArchiveAbsoluteDirectory(value string) (string, error) {
	if !utf8.ValidString(value) {
		return "", fmt.Errorf("absolute path is not valid UTF-8")
	}
	path, err := exactAbsolutePath(value)
	if err != nil {
		return "", err
	}
	if path == string(filepath.Separator) {
		return "", fmt.Errorf("filesystem root cannot be archived directly")
	}
	if err := requireRealDirectory(path); err != nil {
		return "", err
	}
	return path, nil
}

func requireReadableDirectArchiveDirectory(value string, allowEmpty bool) error {
	before, err := os.Lstat(value)
	if err != nil {
		return err
	}
	directory, err := os.Open(value)
	if err != nil {
		return err
	}
	defer directory.Close()
	opened, err := directory.Stat()
	if err != nil || !opened.IsDir() || !os.SameFile(before, opened) {
		return fmt.Errorf("directory changed while opening")
	}
	if _, err := directory.Readdirnames(1); errors.Is(err, io.EOF) {
		if !allowEmpty {
			return fmt.Errorf("directory must not be empty")
		}
	} else if err != nil {
		return err
	}
	return nil
}

func directArchiveRelativePath(value string) (string, error) {
	if !utf8.ValidString(value) || value == "" || value != strings.TrimSpace(value) || filepath.IsAbs(value) || strings.ContainsRune(value, '\x00') || strings.Contains(value, "\\") {
		return "", fmt.Errorf("relative path must be exact and portable")
	}
	clean := filepath.ToSlash(filepath.Clean(value))
	if clean != value || clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("relative path must already be clean and confined")
	}
	return clean, nil
}

func directArchivePath(source string) string {
	return strings.TrimPrefix(filepath.ToSlash(filepath.Clean(source)), "/")
}

func directArchivePathsOverlap(left, right string) bool {
	left = filepath.Clean(left)
	right = filepath.Clean(right)
	if left == right {
		return true
	}
	return strings.HasPrefix(left, right+string(filepath.Separator)) || strings.HasPrefix(right, left+string(filepath.Separator))
}

func directArchivePortablePathsOverlap(left, right string) bool {
	left = path.Clean(filepath.ToSlash(left))
	right = path.Clean(filepath.ToSlash(right))
	return left == right || strings.HasPrefix(left, right+"/") || strings.HasPrefix(right, left+"/")
}

func directArchiveExclusionsByRoot(exclusions []DirectArchiveExclusion) map[string][]string {
	result := make(map[string][]string)
	for _, exclusion := range exclusions {
		result[exclusion.Root] = append(result[exclusion.Root], exclusion.RelativePath)
	}
	return result
}

func directArchivePathExcluded(relative string, exclusions []string) bool {
	for _, exclusion := range exclusions {
		if relative == exclusion || strings.HasPrefix(relative, exclusion+"/") {
			return true
		}
	}
	return false
}

func validateArchiveIdentity(value string) error {
	if value == "" || value != strings.TrimSpace(value) || value == "." || value == ".." || strings.Trim(value, ".-") != value || len(value) > 160 {
		return fmt.Errorf("%w: invalid archive identity", ErrDirectArchiveRequestInvalid)
	}
	for _, char := range value {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') || (char >= '0' && char <= '9') || char == '-' || char == '_' || char == '.' {
			continue
		}
		return fmt.Errorf("%w: invalid archive identity", ErrDirectArchiveRequestInvalid)
	}
	return nil
}
