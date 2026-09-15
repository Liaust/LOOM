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
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
	"loom.local/loom/internal/redaction"
)

const (
	OperationalPackageSchema = "loom.operational_backup.v1"

	OperationalManifestFile     = "manifest.json"
	OperationalManifestHashFile = "manifest.sha256"

	OperationalArtifactPostgresDump  = "postgres_dump"
	OperationalArtifactServiceConfig = "service_configuration"
	OperationalArtifactInstallConfig = "install_configuration"
	OperationalArtifactReleaseConfig = "release_configuration"
	OperationalArtifactMigration     = "migration_state"
	OperationalArtifactUpdate        = "update_state"
	OperationalArtifactHealth        = "health"

	OperationalMigrationStateSchema = "loom.operational_backup.migration_state.v1"
	OperationalRedactionPolicy      = "loom.internal.redaction.default.v1"

	OperationalRetentionCount  = 3
	OperationalPackageMaxBytes = int64(2 << 30)

	operationalManifestMaxBytes = int64(4 << 20)
	operationalDumpMaxBytes     = int64(1 << 30)
	operationalConfigMaxBytes   = int64(4 << 20)
	operationalStateMaxBytes    = int64(16 << 20)
	operationalHealthMaxBytes   = int64(1 << 20)

	OperationalFindingInvalidPath              = "invalid_path"
	OperationalFindingLink                     = "link_not_allowed"
	OperationalFindingMissingArtifact          = "missing_artifact"
	OperationalFindingUnexpectedArtifact       = "unexpected_artifact"
	OperationalFindingManifestInvalid          = "manifest_invalid"
	OperationalFindingManifestHashMismatch     = "manifest_hash_mismatch"
	OperationalFindingManifestIdentityMismatch = "manifest_identity_mismatch"
	OperationalFindingArtifactHashMismatch     = "artifact_hash_mismatch"
	OperationalFindingArtifactSizeMismatch     = "artifact_size_mismatch"
	OperationalFindingArtifactModeMismatch     = "artifact_mode_mismatch"
	OperationalFindingPostgresFormat           = "postgres_format_invalid"
	OperationalFindingRedactionMissing         = "redaction_missing"
	OperationalFindingSecretPresent            = "secret_present"
	OperationalFindingSchemaHeadMismatch       = "schema_head_mismatch"
	OperationalFindingSourceEvidence           = "source_evidence_invalid"
	OperationalFindingSourceChanged            = "source_changed"
	OperationalFindingPackageTooLarge          = "package_too_large"
)

var (
	ErrOperationalPackageInvalid   = errors.New("invalid operational package")
	ErrOperationalPackageChanged   = errors.New("operational package source changed")
	ErrOperationalPackageExists    = errors.New("operational package already exists")
	ErrOperationalPackageRedaction = errors.New("operational package redaction failed")
)

var operationalArtifactSpecs = []operationalArtifactSpec{
	{Kind: OperationalArtifactPostgresDump, Path: "postgres.dump", MaxBytes: operationalDumpMaxBytes, Format: "postgres_custom"},
	{Kind: OperationalArtifactServiceConfig, Path: "service-config.redacted", MaxBytes: operationalConfigMaxBytes, Redacted: true},
	{Kind: OperationalArtifactInstallConfig, Path: "install-config.redacted", MaxBytes: operationalConfigMaxBytes, Redacted: true},
	{Kind: OperationalArtifactReleaseConfig, Path: "release-config.redacted", MaxBytes: operationalConfigMaxBytes, Redacted: true},
	{Kind: OperationalArtifactMigration, Path: "migration-state.json", MaxBytes: operationalStateMaxBytes},
	{Kind: OperationalArtifactUpdate, Path: "update-state.json", MaxBytes: operationalStateMaxBytes},
	{Kind: OperationalArtifactHealth, Path: "health.json", MaxBytes: operationalHealthMaxBytes},
}

type operationalArtifactSpec struct {
	Kind     string
	Path     string
	MaxBytes int64
	Format   string
	Redacted bool
}

// OperationalConfigRedactor is an optional supplemental redactor. LOOM's
// semantic redaction policy always runs before and after this callback; the
// callback's presence is never treated as redaction evidence.
type OperationalConfigRedactor func(kind string, source []byte) ([]byte, error)

type OperationalPackageCreateInput struct {
	PackagesRoot    string
	PackageID       string
	CreatedAt       time.Time
	SchemaHead      int64
	PostgresDump    func(context.Context, string) error
	ServiceConfig   string
	InstallConfig   string
	ReleaseConfig   string
	MigrationState  string
	UpdateState     string
	Health          string
	RedactConfig    OperationalConfigRedactor
	ForbiddenValues []string
}

type OperationalPackageManifest struct {
	Schema                string                       `json:"schema"`
	PackageID             string                       `json:"package_id"`
	CreatedAt             time.Time                    `json:"created_at"`
	SchemaHead            int64                        `json:"schema_head"`
	PostgresFormat        string                       `json:"postgres_format"`
	Artifacts             []OperationalPackageArtifact `json:"artifacts"`
	Redaction             OperationalRedactionEvidence `json:"redaction"`
	SourceStabilitySHA256 string                       `json:"source_stability_sha256"`
}

type OperationalPackageArtifact struct {
	Kind      string                     `json:"kind"`
	Path      string                     `json:"path"`
	SizeBytes int64                      `json:"size_bytes"`
	SHA256    string                     `json:"sha256"`
	Mode      uint32                     `json:"mode"`
	Format    string                     `json:"format,omitempty"`
	Redacted  bool                       `json:"redacted,omitempty"`
	Source    *OperationalSourceEvidence `json:"source,omitempty"`
}

type OperationalSourceEvidence struct {
	Path             string `json:"path"`
	SizeBytes        int64  `json:"size_bytes"`
	SHA256           string `json:"sha256"`
	Mode             uint32 `json:"mode"`
	Device           uint64 `json:"device"`
	Inode            uint64 `json:"inode"`
	ModifiedUnixNano int64  `json:"modified_unix_nano"`
	Stable           bool   `json:"stable"`
}

type OperationalRedactionEvidence struct {
	Policy            string                                 `json:"policy"`
	ArtifactCount     int                                    `json:"artifact_count"`
	TotalRedactions   int                                    `json:"total_redactions"`
	ForbiddenValueNum int                                    `json:"forbidden_value_count"`
	Artifacts         []OperationalArtifactRedactionEvidence `json:"artifacts"`
}

type OperationalArtifactRedactionEvidence struct {
	Kind                   string         `json:"kind"`
	SourceFormat           string         `json:"source_format"`
	OutputFormat           string         `json:"output_format"`
	SourceSHA256           string         `json:"source_sha256"`
	OutputSHA256           string         `json:"output_sha256"`
	SemanticRedactions     int            `json:"semantic_redactions"`
	SemanticCategories     map[string]int `json:"semantic_categories,omitempty"`
	VerificationRedactions int            `json:"verification_redactions"`
	VerificationCategories map[string]int `json:"verification_categories,omitempty"`
}

type OperationalMigrationState struct {
	Schema         string `json:"schema"`
	CurrentVersion int64  `json:"current_version"`
	LatestVersion  int64  `json:"latest_version"`
	Status         string `json:"status"`
}

type OperationalPackageVerificationInput struct {
	PackageDir             string
	ExpectedManifestSHA256 string
	ExpectedPackageID      string
	ExpectedSchemaHead     *int64
	ForbiddenValues        []string
}

type OperationalPackageVerification struct {
	Status          string                      `json:"status"`
	PackageDir      string                      `json:"package_dir"`
	PackageID       string                      `json:"package_id,omitempty"`
	ManifestPath    string                      `json:"manifest_path,omitempty"`
	ManifestSHA256  string                      `json:"manifest_sha256,omitempty"`
	SchemaHead      int64                       `json:"schema_head"`
	LatestMigration int64                       `json:"latest_migration"`
	CreatedAt       time.Time                   `json:"created_at,omitempty"`
	TotalBytes      int64                       `json:"total_bytes"`
	ArtifactCount   int                         `json:"artifact_count"`
	Findings        []OperationalPackageFinding `json:"findings"`
	Manifest        *OperationalPackageManifest `json:"-"`
}

type OperationalPackageFinding struct {
	Code     string `json:"code"`
	Artifact string `json:"artifact,omitempty"`
	Detail   string `json:"detail"`
}

type OperationalPackageCreateResult struct {
	PackageDir     string                         `json:"package_dir"`
	ManifestSHA256 string                         `json:"manifest_sha256"`
	Verification   OperationalPackageVerification `json:"verification"`
	RetentionPlan  OperationalRetentionPlan       `json:"retention_plan"`
}

type OperationalRetentionPlan struct {
	Root       string                      `json:"root"`
	KeepLatest int                         `json:"keep_latest"`
	Keep       []OperationalRetentionEntry `json:"keep"`
	Remove     []OperationalRetentionEntry `json:"remove"`
	Findings   []OperationalPackageFinding `json:"findings,omitempty"`
}

type OperationalRetentionEntry struct {
	PackageID      string    `json:"package_id"`
	Path           string    `json:"path"`
	CreatedAt      time.Time `json:"created_at"`
	ManifestSHA256 string    `json:"manifest_sha256"`
	Reason         string    `json:"reason"`
}

type operationalFileInspection struct {
	Raw      []byte
	Prefix   []byte
	Evidence OperationalSourceEvidence
}

func CreateOperationalPackage(ctx context.Context, input OperationalPackageCreateInput) (OperationalPackageCreateResult, error) {
	if err := ctx.Err(); err != nil {
		return OperationalPackageCreateResult{}, err
	}
	root, err := exactAbsolutePath(input.PackagesRoot)
	if err != nil {
		return OperationalPackageCreateResult{}, err
	}
	if err := requireRealDirectory(root); err != nil {
		return OperationalPackageCreateResult{}, fmt.Errorf("%w: packages root: %v", ErrOperationalPackageInvalid, err)
	}
	if err := validatePackageID(input.PackageID); err != nil {
		return OperationalPackageCreateResult{}, err
	}
	if input.PostgresDump == nil {
		return OperationalPackageCreateResult{}, fmt.Errorf("%w: PostgreSQL dump capture is required", ErrOperationalPackageInvalid)
	}
	createdAt := input.CreatedAt.UTC()
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}
	finalDir := filepath.Join(root, input.PackageID)
	if _, err := os.Lstat(finalDir); err == nil {
		return OperationalPackageCreateResult{}, fmt.Errorf("%w: %s", ErrOperationalPackageExists, finalDir)
	} else if !os.IsNotExist(err) {
		return OperationalPackageCreateResult{}, err
	}

	stage, err := os.MkdirTemp(root, ".operational-staging-")
	if err != nil {
		return OperationalPackageCreateResult{}, err
	}
	published := false
	defer func() {
		if !published {
			_ = os.RemoveAll(stage)
		}
	}()
	if err := os.Chmod(stage, 0o700); err != nil {
		return OperationalPackageCreateResult{}, err
	}

	artifacts := make([]OperationalPackageArtifact, 0, len(operationalArtifactSpecs))
	redactionArtifacts := make([]OperationalArtifactRedactionEvidence, 0, 3)
	totalRedactions := 0
	dumpPath := filepath.Join(stage, operationalArtifactSpecs[0].Path)
	if err := input.PostgresDump(ctx, dumpPath); err != nil {
		return OperationalPackageCreateResult{}, fmt.Errorf("capture custom PostgreSQL dump: %w", err)
	}
	if err := normalizeCreatedArtifact(dumpPath); err != nil {
		return OperationalPackageCreateResult{}, err
	}
	dump, err := inspectStableFile(dumpPath, operationalDumpMaxBytes, false)
	if err != nil {
		return OperationalPackageCreateResult{}, fmt.Errorf("%w: PostgreSQL dump: %v", ErrOperationalPackageInvalid, err)
	}
	if !bytes.HasPrefix(dump.Prefix, []byte("PGDMP")) {
		return OperationalPackageCreateResult{}, fmt.Errorf("%w: PostgreSQL dump is not custom format", ErrOperationalPackageInvalid)
	}
	artifacts = append(artifacts, artifactFromInspection(operationalArtifactSpecs[0], dump, nil))

	type sourceInput struct {
		spec operationalArtifactSpec
		path string
	}
	sources := []sourceInput{
		{operationalArtifactSpecs[1], input.ServiceConfig},
		{operationalArtifactSpecs[2], input.InstallConfig},
		{operationalArtifactSpecs[3], input.ReleaseConfig},
		{operationalArtifactSpecs[4], input.MigrationState},
		{operationalArtifactSpecs[5], input.UpdateState},
		{operationalArtifactSpecs[6], input.Health},
	}
	for _, source := range sources {
		if err := ctx.Err(); err != nil {
			return OperationalPackageCreateResult{}, err
		}
		inspected, err := inspectStableSource(source.path, source.spec.MaxBytes)
		if err != nil {
			return OperationalPackageCreateResult{}, fmt.Errorf("%w: %s: %v", ErrOperationalPackageInvalid, source.spec.Kind, err)
		}
		output := inspected.Raw
		if source.spec.Redacted {
			semanticOutput, sourceFormat, sourceReport := semanticRedactConfiguration(inspected.Raw)
			output = semanticOutput
			if input.RedactConfig != nil {
				output, err = input.RedactConfig(source.spec.Kind, append([]byte(nil), semanticOutput...))
				if err != nil {
					return OperationalPackageCreateResult{}, fmt.Errorf("%w: supplemental redaction failed for %s", ErrOperationalPackageRedaction, source.spec.Kind)
				}
			}
			if len(output) == 0 || int64(len(output)) > source.spec.MaxBytes {
				return OperationalPackageCreateResult{}, fmt.Errorf("%w: %s produced empty or oversized output", ErrOperationalPackageRedaction, source.spec.Kind)
			}
			output, outputFormat, outputReport := semanticRedactConfiguration(output)
			verifiedOutput, verificationFormat, verificationReport := semanticRedactConfiguration(output)
			if !bytes.Equal(verifiedOutput, output) || verificationFormat != outputFormat {
				return OperationalPackageCreateResult{}, fmt.Errorf("%w: semantic redaction is not stable for %s", ErrOperationalPackageRedaction, source.spec.Kind)
			}
			semanticReport := mergeRedactionReports(sourceReport, outputReport)
			redactionArtifacts = append(redactionArtifacts, OperationalArtifactRedactionEvidence{
				Kind:                   source.spec.Kind,
				SourceFormat:           sourceFormat,
				OutputFormat:           outputFormat,
				SourceSHA256:           inspected.Evidence.SHA256,
				SemanticRedactions:     semanticReport.Redactions,
				SemanticCategories:     redactionCategoryEvidence(semanticReport),
				VerificationRedactions: verificationReport.Redactions,
				VerificationCategories: redactionCategoryEvidence(verificationReport),
			})
			totalRedactions += semanticReport.Redactions
		}
		if err := validateStateArtifact(source.spec.Kind, output, input.SchemaHead); err != nil {
			return OperationalPackageCreateResult{}, err
		}
		if containsForbidden(output, input.ForbiddenValues) {
			return OperationalPackageCreateResult{}, fmt.Errorf("%w: forbidden value remains in %s", ErrOperationalPackageRedaction, source.spec.Kind)
		}
		target := filepath.Join(stage, source.spec.Path)
		if err := writeExclusiveFile(target, output, 0o600); err != nil {
			return OperationalPackageCreateResult{}, err
		}
		written, err := inspectStableFile(target, source.spec.MaxBytes, false)
		if err != nil {
			return OperationalPackageCreateResult{}, err
		}
		evidence := inspected.Evidence
		evidence.Stable = true
		artifacts = append(artifacts, artifactFromInspection(source.spec, written, &evidence))
		if source.spec.Redacted {
			redactionArtifacts[len(redactionArtifacts)-1].OutputSHA256 = written.Evidence.SHA256
		}
	}

	for _, artifact := range artifacts {
		if containsForbiddenFile(filepath.Join(stage, artifact.Path), input.ForbiddenValues) {
			return OperationalPackageCreateResult{}, fmt.Errorf("%w: forbidden value remains in %s", ErrOperationalPackageRedaction, artifact.Kind)
		}
	}
	if err := revalidateSources(artifacts); err != nil {
		return OperationalPackageCreateResult{}, err
	}

	manifest := OperationalPackageManifest{
		Schema:         OperationalPackageSchema,
		PackageID:      input.PackageID,
		CreatedAt:      createdAt,
		SchemaHead:     input.SchemaHead,
		PostgresFormat: "custom",
		Artifacts:      artifacts,
		Redaction: OperationalRedactionEvidence{
			Policy:            OperationalRedactionPolicy,
			ArtifactCount:     len(redactionArtifacts),
			TotalRedactions:   totalRedactions,
			ForbiddenValueNum: len(nonEmptyForbiddenValues(input.ForbiddenValues)),
			Artifacts:         redactionArtifacts,
		},
	}
	manifest.SourceStabilitySHA256, err = sourceStabilityDigest(manifest.Artifacts)
	if err != nil {
		return OperationalPackageCreateResult{}, err
	}
	manifestRaw, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return OperationalPackageCreateResult{}, err
	}
	manifestRaw = append(manifestRaw, '\n')
	if containsForbidden(manifestRaw, input.ForbiddenValues) {
		return OperationalPackageCreateResult{}, fmt.Errorf("%w: forbidden value would enter the operational manifest", ErrOperationalPackageRedaction)
	}
	if int64(len(manifestRaw)) > operationalManifestMaxBytes {
		return OperationalPackageCreateResult{}, fmt.Errorf("%w: manifest exceeds byte bound", ErrOperationalPackageInvalid)
	}
	manifestHash := sha256.Sum256(manifestRaw)
	manifestSHA := hex.EncodeToString(manifestHash[:])
	if err := writeExclusiveFile(filepath.Join(stage, OperationalManifestFile), manifestRaw, 0o600); err != nil {
		return OperationalPackageCreateResult{}, err
	}
	if err := writeExclusiveFile(filepath.Join(stage, OperationalManifestHashFile), []byte(manifestSHA+"  "+OperationalManifestFile+"\n"), 0o600); err != nil {
		return OperationalPackageCreateResult{}, err
	}

	expectedHead := input.SchemaHead
	verification, err := VerifyOperationalPackage(ctx, OperationalPackageVerificationInput{
		PackageDir:             stage,
		ExpectedManifestSHA256: manifestSHA,
		ExpectedPackageID:      input.PackageID,
		ExpectedSchemaHead:     &expectedHead,
		ForbiddenValues:        input.ForbiddenValues,
	})
	if err != nil {
		return OperationalPackageCreateResult{}, err
	}
	if verification.Status != "succeeded" {
		return OperationalPackageCreateResult{}, fmt.Errorf("%w: staging verification failed: %s", ErrOperationalPackageInvalid, summarizeOperationalFindings(verification.Findings))
	}
	if err := revalidateSources(artifacts); err != nil {
		return OperationalPackageCreateResult{}, err
	}
	if _, err := os.Lstat(finalDir); err == nil {
		return OperationalPackageCreateResult{}, fmt.Errorf("%w: %s", ErrOperationalPackageExists, finalDir)
	} else if !os.IsNotExist(err) {
		return OperationalPackageCreateResult{}, err
	}
	if err := os.Rename(stage, finalDir); err != nil {
		return OperationalPackageCreateResult{}, fmt.Errorf("atomically publish operational package: %w", err)
	}
	published = true
	verification.PackageDir = finalDir
	verification.ManifestPath = filepath.Join(finalDir, OperationalManifestFile)
	retention, err := PlanOperationalPackageRetention(ctx, root, input.ForbiddenValues)
	if err != nil {
		return OperationalPackageCreateResult{PackageDir: finalDir, ManifestSHA256: manifestSHA, Verification: verification}, err
	}
	return OperationalPackageCreateResult{
		PackageDir:     finalDir,
		ManifestSHA256: manifestSHA,
		Verification:   verification,
		RetentionPlan:  retention,
	}, nil
}

func VerifyOperationalPackage(ctx context.Context, input OperationalPackageVerificationInput) (OperationalPackageVerification, error) {
	verification := OperationalPackageVerification{Status: "failed", Findings: []OperationalPackageFinding{}}
	if err := ctx.Err(); err != nil {
		return verification, err
	}
	packageDir, err := exactAbsolutePath(input.PackageDir)
	if err != nil {
		verification.Findings = append(verification.Findings, operationalFinding(OperationalFindingInvalidPath, "", "package directory must be an exact absolute path"))
		return verification, nil
	}
	verification.PackageDir = packageDir
	verification.ManifestPath = filepath.Join(packageDir, OperationalManifestFile)
	info, err := os.Lstat(packageDir)
	if err != nil {
		verification.Findings = append(verification.Findings, operationalFinding(OperationalFindingInvalidPath, "", "package directory is not readable"))
		return verification, nil
	}
	if info.Mode()&os.ModeSymlink != 0 {
		verification.Findings = append(verification.Findings, operationalFinding(OperationalFindingLink, "", "package directory must not be a link"))
		return verification, nil
	}
	if !info.IsDir() {
		verification.Findings = append(verification.Findings, operationalFinding(OperationalFindingInvalidPath, "", "package path is not a directory"))
		return verification, nil
	}
	if info.Mode().Perm() != 0o700 {
		verification.Findings = append(verification.Findings, operationalFinding(OperationalFindingArtifactModeMismatch, "", "package directory mode must be 0700"))
	}

	entries, err := os.ReadDir(packageDir)
	if err != nil {
		verification.Findings = append(verification.Findings, operationalFinding(OperationalFindingInvalidPath, "", "package directory cannot be listed"))
		return verification, nil
	}
	expectedEntries := map[string]struct{}{
		OperationalManifestFile: {}, OperationalManifestHashFile: {},
	}
	for _, spec := range operationalArtifactSpecs {
		expectedEntries[spec.Path] = struct{}{}
	}
	for _, entry := range entries {
		if _, ok := expectedEntries[entry.Name()]; !ok {
			verification.Findings = append(verification.Findings, operationalFinding(OperationalFindingUnexpectedArtifact, entry.Name(), "package contains an undeclared entry"))
		}
	}
	for name := range expectedEntries {
		if _, err := os.Lstat(filepath.Join(packageDir, name)); os.IsNotExist(err) {
			verification.Findings = append(verification.Findings, operationalFinding(OperationalFindingMissingArtifact, name, "required package entry is missing"))
		}
	}

	manifestInspection, err := inspectStableFile(filepath.Join(packageDir, OperationalManifestFile), operationalManifestMaxBytes, true)
	if err != nil {
		verification.Findings = append(verification.Findings, classifyOperationalFileError(OperationalManifestFile, err))
		return verification, nil
	}
	if manifestInspection.Evidence.Mode != 0o600 {
		verification.Findings = append(verification.Findings, operationalFinding(OperationalFindingArtifactModeMismatch, OperationalManifestFile, "manifest mode must be 0600"))
	}
	verification.ManifestSHA256 = manifestInspection.Evidence.SHA256
	if containsForbidden(manifestInspection.Raw, input.ForbiddenValues) {
		verification.Findings = append(verification.Findings, operationalFinding(OperationalFindingSecretPresent, OperationalManifestFile, "forbidden value is present in operational manifest"))
	}
	hashInspection, err := inspectStableFile(filepath.Join(packageDir, OperationalManifestHashFile), 256, true)
	if err != nil {
		verification.Findings = append(verification.Findings, classifyOperationalFileError(OperationalManifestHashFile, err))
		return verification, nil
	}
	if hashInspection.Evidence.Mode != 0o600 {
		verification.Findings = append(verification.Findings, operationalFinding(OperationalFindingArtifactModeMismatch, OperationalManifestHashFile, "manifest hash mode must be 0600"))
	}
	wantHashLine := verification.ManifestSHA256 + "  " + OperationalManifestFile + "\n"
	if string(hashInspection.Raw) != wantHashLine {
		verification.Findings = append(verification.Findings, operationalFinding(OperationalFindingManifestHashMismatch, OperationalManifestHashFile, "manifest authentication hash does not match exact manifest bytes"))
	}
	if expected := normalizeSHA256(input.ExpectedManifestSHA256); input.ExpectedManifestSHA256 != "" && expected == "" {
		verification.Findings = append(verification.Findings, operationalFinding(OperationalFindingManifestIdentityMismatch, OperationalManifestFile, "expected manifest identity is invalid"))
	} else if expected != "" && expected != verification.ManifestSHA256 {
		verification.Findings = append(verification.Findings, operationalFinding(OperationalFindingManifestIdentityMismatch, OperationalManifestFile, "manifest identity does not match the expected package"))
	}

	var manifest OperationalPackageManifest
	decoder := json.NewDecoder(bytes.NewReader(manifestInspection.Raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		verification.Findings = append(verification.Findings, operationalFinding(OperationalFindingManifestInvalid, OperationalManifestFile, "manifest is not valid loom.operational_backup.v1 JSON"))
		return verification, nil
	}
	if err := ensureJSONEOF(decoder); err != nil {
		verification.Findings = append(verification.Findings, operationalFinding(OperationalFindingManifestInvalid, OperationalManifestFile, "manifest has trailing JSON content"))
		return verification, nil
	}
	verification.Manifest = &manifest
	verification.PackageID = manifest.PackageID
	verification.SchemaHead = manifest.SchemaHead
	verification.CreatedAt = manifest.CreatedAt
	if err := validateOperationalManifest(manifest); err != nil {
		verification.Findings = append(verification.Findings, operationalFinding(OperationalFindingManifestInvalid, OperationalManifestFile, err.Error()))
	}
	if err := validateRedactionEvidence(manifest); err != nil {
		verification.Findings = append(verification.Findings, operationalFinding(OperationalFindingRedactionMissing, OperationalManifestFile, "manifest has incomplete configuration redaction evidence"))
	}
	if expected := strings.TrimSpace(input.ExpectedPackageID); expected != "" && manifest.PackageID != expected {
		verification.Findings = append(verification.Findings, operationalFinding(OperationalFindingManifestIdentityMismatch, OperationalManifestFile, "package identity does not match expected identity"))
	}
	if input.ExpectedSchemaHead != nil && manifest.SchemaHead != *input.ExpectedSchemaHead {
		verification.Findings = append(verification.Findings, operationalFinding(OperationalFindingSchemaHeadMismatch, OperationalArtifactMigration, "package schema head does not match expected head"))
	}

	artifactByKind := make(map[string]OperationalPackageArtifact, len(manifest.Artifacts))
	for _, artifact := range manifest.Artifacts {
		artifactByKind[artifact.Kind] = artifact
	}
	redactionByKind := make(map[string]OperationalArtifactRedactionEvidence, len(manifest.Redaction.Artifacts))
	for _, evidence := range manifest.Redaction.Artifacts {
		redactionByKind[evidence.Kind] = evidence
	}
	var total int64
	for _, spec := range operationalArtifactSpecs {
		if err := ctx.Err(); err != nil {
			return verification, err
		}
		artifact, ok := artifactByKind[spec.Kind]
		if !ok {
			verification.Findings = append(verification.Findings, operationalFinding(OperationalFindingMissingArtifact, spec.Kind, "manifest does not declare required artifact"))
			continue
		}
		if artifact.Path != spec.Path {
			verification.Findings = append(verification.Findings, operationalFinding(OperationalFindingInvalidPath, spec.Kind, "artifact path does not match the fixed package layout"))
			continue
		}
		artifactPath, pathErr := confinedOperationalPath(packageDir, artifact.Path)
		if pathErr != nil {
			verification.Findings = append(verification.Findings, operationalFinding(OperationalFindingInvalidPath, spec.Kind, "artifact path escapes package"))
			continue
		}
		inspected, inspectErr := inspectStableFile(artifactPath, spec.MaxBytes, spec.Redacted)
		if inspectErr != nil {
			verification.Findings = append(verification.Findings, classifyOperationalFileError(spec.Kind, inspectErr))
			continue
		}
		if inspected.Evidence.SizeBytes != artifact.SizeBytes {
			verification.Findings = append(verification.Findings, operationalFinding(OperationalFindingArtifactSizeMismatch, spec.Kind, "artifact size does not match manifest"))
		}
		if inspected.Evidence.SHA256 != normalizeSHA256(artifact.SHA256) {
			verification.Findings = append(verification.Findings, operationalFinding(OperationalFindingArtifactHashMismatch, spec.Kind, "artifact hash does not match manifest"))
		}
		if inspected.Evidence.Mode != artifact.Mode || artifact.Mode != 0o600 {
			verification.Findings = append(verification.Findings, operationalFinding(OperationalFindingArtifactModeMismatch, spec.Kind, "artifact mode does not match manifest and required 0600 mode"))
		}
		if spec.Kind == OperationalArtifactPostgresDump && (!bytes.HasPrefix(inspected.Prefix, []byte("PGDMP")) || artifact.Format != "postgres_custom") {
			verification.Findings = append(verification.Findings, operationalFinding(OperationalFindingPostgresFormat, spec.Kind, "PostgreSQL dump is not custom format"))
		}
		if spec.Redacted && !artifact.Redacted {
			verification.Findings = append(verification.Findings, operationalFinding(OperationalFindingRedactionMissing, spec.Kind, "configuration artifact lacks redaction evidence"))
		}
		if spec.Redacted {
			evidence, ok := redactionByKind[spec.Kind]
			if !ok || !verifySemanticRedactionArtifact(inspected.Raw, artifact, evidence) {
				verification.Findings = append(verification.Findings, operationalFinding(OperationalFindingRedactionMissing, spec.Kind, "configuration artifact does not match verified semantic redaction evidence"))
			}
		}
		if containsForbiddenFile(artifactPath, input.ForbiddenValues) {
			verification.Findings = append(verification.Findings, operationalFinding(OperationalFindingSecretPresent, spec.Kind, "forbidden value is present in package artifact"))
		}
		if artifact.Source != nil {
			if err := validateSourceEvidence(*artifact.Source); err != nil {
				verification.Findings = append(verification.Findings, operationalFinding(OperationalFindingSourceEvidence, spec.Kind, err.Error()))
			}
		} else if spec.Kind != OperationalArtifactPostgresDump {
			verification.Findings = append(verification.Findings, operationalFinding(OperationalFindingSourceEvidence, spec.Kind, "source stability evidence is missing"))
		}
		if total > OperationalPackageMaxBytes-inspected.Evidence.SizeBytes {
			verification.Findings = append(verification.Findings, operationalFinding(OperationalFindingPackageTooLarge, spec.Kind, "package exceeds fixed total byte bound"))
		} else {
			total += inspected.Evidence.SizeBytes
		}
		verification.ArtifactCount++
	}
	verification.TotalBytes = total + manifestInspection.Evidence.SizeBytes + hashInspection.Evidence.SizeBytes
	if verification.TotalBytes > OperationalPackageMaxBytes {
		verification.Findings = append(verification.Findings, operationalFinding(OperationalFindingPackageTooLarge, "", "package exceeds fixed total byte bound"))
	}
	if migrationArtifact, ok := artifactByKind[OperationalArtifactMigration]; ok {
		migrationPath, pathErr := confinedOperationalPath(packageDir, migrationArtifact.Path)
		if pathErr == nil {
			migrationInspection, readErr := inspectStableFile(migrationPath, operationalStateMaxBytes, true)
			if readErr == nil {
				migration, validationErr := decodeMigrationState(migrationInspection.Raw)
				if validationErr != nil {
					verification.Findings = append(verification.Findings, operationalFinding(OperationalFindingManifestInvalid, OperationalArtifactMigration, validationErr.Error()))
				} else {
					verification.LatestMigration = migration.LatestVersion
					if migration.CurrentVersion != manifest.SchemaHead {
						verification.Findings = append(verification.Findings, operationalFinding(OperationalFindingSchemaHeadMismatch, OperationalArtifactMigration, "migration evidence does not match manifest schema head"))
					}
				}
			}
		}
	}
	if digest, digestErr := sourceStabilityDigest(manifest.Artifacts); digestErr != nil || digest != manifest.SourceStabilitySHA256 {
		verification.Findings = append(verification.Findings, operationalFinding(OperationalFindingSourceEvidence, OperationalManifestFile, "source stability evidence digest does not match manifest"))
	}
	if len(verification.Findings) == 0 {
		verification.Status = "succeeded"
	}
	return verification, nil
}

// PlanOperationalPackageRetention identifies the latest three verified
// packages and exact older candidates. It never renames, removes, chmods, or
// otherwise mutates any package.
func PlanOperationalPackageRetention(ctx context.Context, root string, forbiddenValues []string) (OperationalRetentionPlan, error) {
	root, err := exactAbsolutePath(root)
	if err != nil {
		return OperationalRetentionPlan{}, err
	}
	if err := requireRealDirectory(root); err != nil {
		return OperationalRetentionPlan{}, err
	}
	plan := OperationalRetentionPlan{
		Root: root, KeepLatest: OperationalRetentionCount,
		Keep: []OperationalRetentionEntry{}, Remove: []OperationalRetentionEntry{}, Findings: []OperationalPackageFinding{},
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return OperationalRetentionPlan{}, err
	}
	candidates := []OperationalRetentionEntry{}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return OperationalRetentionPlan{}, err
		}
		if strings.HasPrefix(entry.Name(), ".operational-staging-") {
			plan.Findings = append(plan.Findings, operationalFinding(OperationalFindingUnexpectedArtifact, entry.Name(), "active or incomplete staging is not retention eligible"))
			continue
		}
		path := filepath.Join(root, entry.Name())
		info, statErr := os.Lstat(path)
		if statErr != nil {
			plan.Findings = append(plan.Findings, operationalFinding(OperationalFindingInvalidPath, entry.Name(), "retention entry cannot be inspected"))
			continue
		}
		if info.Mode()&os.ModeSymlink != 0 {
			plan.Findings = append(plan.Findings, operationalFinding(OperationalFindingLink, entry.Name(), "linked retention entry is never eligible"))
			continue
		}
		if !info.IsDir() {
			plan.Findings = append(plan.Findings, operationalFinding(OperationalFindingUnexpectedArtifact, entry.Name(), "non-package entry is never retention eligible"))
			continue
		}
		verification, verifyErr := VerifyOperationalPackage(ctx, OperationalPackageVerificationInput{PackageDir: path, ForbiddenValues: forbiddenValues})
		if verifyErr != nil {
			return OperationalRetentionPlan{}, verifyErr
		}
		if verification.Status != "succeeded" {
			plan.Findings = append(plan.Findings, operationalFinding(OperationalFindingManifestInvalid, entry.Name(), "unverified package is never retention eligible"))
			continue
		}
		candidates = append(candidates, OperationalRetentionEntry{
			PackageID: verification.PackageID, Path: path, CreatedAt: verification.CreatedAt,
			ManifestSHA256: verification.ManifestSHA256,
		})
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].CreatedAt.Equal(candidates[j].CreatedAt) {
			return candidates[i].PackageID > candidates[j].PackageID
		}
		return candidates[i].CreatedAt.After(candidates[j].CreatedAt)
	})
	for index, candidate := range candidates {
		if index < OperationalRetentionCount {
			candidate.Reason = "latest_three_verified"
			plan.Keep = append(plan.Keep, candidate)
		} else {
			candidate.Reason = "older_than_latest_three_verified"
			plan.Remove = append(plan.Remove, candidate)
		}
	}
	return plan, nil
}

func artifactFromInspection(spec operationalArtifactSpec, inspected operationalFileInspection, source *OperationalSourceEvidence) OperationalPackageArtifact {
	return OperationalPackageArtifact{
		Kind: spec.Kind, Path: spec.Path, SizeBytes: inspected.Evidence.SizeBytes,
		SHA256: inspected.Evidence.SHA256, Mode: inspected.Evidence.Mode,
		Format: spec.Format, Redacted: spec.Redacted, Source: source,
	}
}

func validateOperationalManifest(manifest OperationalPackageManifest) error {
	if manifest.Schema != OperationalPackageSchema {
		return fmt.Errorf("unsupported operational package schema %q", manifest.Schema)
	}
	if err := validatePackageID(manifest.PackageID); err != nil {
		return err
	}
	if manifest.CreatedAt.IsZero() || manifest.CreatedAt.Location() != time.UTC {
		return fmt.Errorf("manifest created_at must be a non-zero UTC timestamp")
	}
	if manifest.PostgresFormat != "custom" {
		return fmt.Errorf("manifest PostgreSQL format must be custom")
	}
	if len(manifest.Artifacts) != len(operationalArtifactSpecs) {
		return fmt.Errorf("manifest requires exactly %d artifacts", len(operationalArtifactSpecs))
	}
	seen := make(map[string]struct{}, len(manifest.Artifacts))
	for _, artifact := range manifest.Artifacts {
		if _, exists := seen[artifact.Kind]; exists {
			return fmt.Errorf("manifest duplicates artifact kind %q", artifact.Kind)
		}
		seen[artifact.Kind] = struct{}{}
		if artifact.SizeBytes <= 0 || normalizeSHA256(artifact.SHA256) == "" || artifact.Mode != 0o600 {
			return fmt.Errorf("artifact %q has incomplete size, hash, or mode evidence", artifact.Kind)
		}
	}
	if normalizeSHA256(manifest.SourceStabilitySHA256) == "" {
		return fmt.Errorf("manifest has invalid source stability evidence digest")
	}
	return nil
}

func validateRedactionEvidence(manifest OperationalPackageManifest) error {
	evidence := manifest.Redaction
	if evidence.Policy != OperationalRedactionPolicy || evidence.ArtifactCount != 3 || len(evidence.Artifacts) != 3 || evidence.ForbiddenValueNum < 0 || evidence.TotalRedactions < 0 {
		return ErrOperationalPackageRedaction
	}
	artifacts := make(map[string]OperationalPackageArtifact, len(manifest.Artifacts))
	for _, artifact := range manifest.Artifacts {
		artifacts[artifact.Kind] = artifact
	}
	want := map[string]struct{}{
		OperationalArtifactServiceConfig: {},
		OperationalArtifactInstallConfig: {},
		OperationalArtifactReleaseConfig: {},
	}
	seen := make(map[string]struct{}, len(evidence.Artifacts))
	total := 0
	for _, artifactEvidence := range evidence.Artifacts {
		if _, ok := want[artifactEvidence.Kind]; !ok {
			return ErrOperationalPackageRedaction
		}
		if _, duplicate := seen[artifactEvidence.Kind]; duplicate {
			return ErrOperationalPackageRedaction
		}
		seen[artifactEvidence.Kind] = struct{}{}
		artifact, ok := artifacts[artifactEvidence.Kind]
		if !ok || artifact.Source == nil || !artifact.Redacted {
			return ErrOperationalPackageRedaction
		}
		if !validRedactionFormat(artifactEvidence.SourceFormat) || !validRedactionFormat(artifactEvidence.OutputFormat) ||
			normalizeSHA256(artifactEvidence.SourceSHA256) == "" || artifactEvidence.SourceSHA256 != artifact.Source.SHA256 ||
			normalizeSHA256(artifactEvidence.OutputSHA256) == "" || artifactEvidence.OutputSHA256 != artifact.SHA256 ||
			artifactEvidence.SemanticRedactions < 0 || artifactEvidence.VerificationRedactions < 0 ||
			!validRedactionCategories(artifactEvidence.SemanticCategories, artifactEvidence.SemanticRedactions) ||
			!validRedactionCategories(artifactEvidence.VerificationCategories, artifactEvidence.VerificationRedactions) {
			return ErrOperationalPackageRedaction
		}
		total += artifactEvidence.SemanticRedactions
	}
	if len(seen) != len(want) || total != evidence.TotalRedactions {
		return ErrOperationalPackageRedaction
	}
	return nil
}

func verifySemanticRedactionArtifact(raw []byte, artifact OperationalPackageArtifact, evidence OperationalArtifactRedactionEvidence) bool {
	if evidence.OutputSHA256 != artifact.SHA256 {
		return false
	}
	redacted, format, report := semanticRedactConfiguration(raw)
	return bytes.Equal(redacted, raw) && format == evidence.OutputFormat &&
		report.Redactions == evidence.VerificationRedactions &&
		equalRedactionCategories(redactionCategoryEvidence(report), evidence.VerificationCategories)
}

func semanticRedactConfiguration(raw []byte) ([]byte, string, redaction.Report) {
	profile := redaction.DefaultProfile()
	if json.Valid(raw) {
		redacted, report := redaction.JSONWithReport(json.RawMessage(raw), profile)
		return append([]byte(nil), redacted...), "json", report
	}
	redacted, report := redaction.Text(string(raw), profile)
	return []byte(redacted), "text", report
}

func mergeRedactionReports(reports ...redaction.Report) redaction.Report {
	merged := redaction.Report{Categories: map[redaction.Category]int{}}
	for _, report := range reports {
		merged.Redactions += report.Redactions
		for category, count := range report.Categories {
			merged.Categories[category] += count
		}
	}
	if len(merged.Categories) == 0 {
		merged.Categories = nil
	}
	return merged
}

func redactionCategoryEvidence(report redaction.Report) map[string]int {
	if len(report.Categories) == 0 {
		return nil
	}
	evidence := make(map[string]int, len(report.Categories))
	for category, count := range report.Categories {
		evidence[string(category)] = count
	}
	return evidence
}

func validRedactionFormat(format string) bool {
	return format == "json" || format == "text"
}

func validRedactionCategories(categories map[string]int, wantTotal int) bool {
	allowed := map[string]struct{}{
		string(redaction.Secret): {}, string(redaction.Credential): {}, string(redaction.Token): {},
		string(redaction.PrivateKey): {}, string(redaction.Password): {}, string(redaction.EnvSecret): {},
		string(redaction.SensitivePath): {}, string(redaction.SensitivePayload): {},
	}
	total := 0
	for category, count := range categories {
		if _, ok := allowed[category]; !ok || count <= 0 {
			return false
		}
		total += count
	}
	return total == wantTotal
}

func equalRedactionCategories(left, right map[string]int) bool {
	if len(left) != len(right) {
		return false
	}
	for category, count := range left {
		if right[category] != count {
			return false
		}
	}
	return true
}

func validateSourceEvidence(evidence OperationalSourceEvidence) error {
	if _, err := exactAbsolutePath(evidence.Path); err != nil {
		return fmt.Errorf("source evidence path is not exact and absolute")
	}
	if evidence.SizeBytes <= 0 || normalizeSHA256(evidence.SHA256) == "" || evidence.Mode == 0 || evidence.Device == 0 || evidence.Inode == 0 || !evidence.Stable {
		return fmt.Errorf("source evidence is incomplete")
	}
	return nil
}

func sourceStabilityDigest(artifacts []OperationalPackageArtifact) (string, error) {
	evidence := make([]OperationalSourceEvidence, 0, len(artifacts))
	for _, artifact := range artifacts {
		if artifact.Source != nil {
			evidence = append(evidence, *artifact.Source)
		}
	}
	sort.Slice(evidence, func(i, j int) bool { return evidence[i].Path < evidence[j].Path })
	raw, err := json.Marshal(evidence)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:]), nil
}

func revalidateSources(artifacts []OperationalPackageArtifact) error {
	for _, artifact := range artifacts {
		if artifact.Source == nil {
			continue
		}
		spec, ok := operationalSpecForKind(artifact.Kind)
		if !ok {
			return fmt.Errorf("%w: unknown source artifact %q", ErrOperationalPackageInvalid, artifact.Kind)
		}
		inspected, err := inspectStableSource(artifact.Source.Path, spec.MaxBytes)
		if err != nil || inspected.Evidence != *artifact.Source {
			return fmt.Errorf("%w: %s", ErrOperationalPackageChanged, artifact.Kind)
		}
	}
	return nil
}

func inspectStableSource(path string, maxBytes int64) (operationalFileInspection, error) {
	exact, err := exactAbsolutePath(path)
	if err != nil {
		return operationalFileInspection{}, err
	}
	return inspectStableFile(exact, maxBytes, true)
}

func inspectStableFile(path string, maxBytes int64, load bool) (operationalFileInspection, error) {
	path = filepath.Clean(path)
	before, err := os.Lstat(path)
	if err != nil {
		return operationalFileInspection{}, err
	}
	if before.Mode()&os.ModeSymlink != 0 {
		return operationalFileInspection{}, fmt.Errorf("file must not be a link")
	}
	if !before.Mode().IsRegular() {
		return operationalFileInspection{}, fmt.Errorf("path must be a regular file")
	}
	if before.Size() <= 0 || before.Size() > maxBytes {
		return operationalFileInspection{}, fmt.Errorf("file is empty or exceeds %d-byte bound", maxBytes)
	}
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return operationalFileInspection{}, err
	}
	file := os.NewFile(uintptr(fd), path)
	if file == nil {
		_ = unix.Close(fd)
		return operationalFileInspection{}, fmt.Errorf("open file")
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil {
		return operationalFileInspection{}, err
	}
	if !opened.Mode().IsRegular() || !os.SameFile(before, opened) || opened.Size() > maxBytes {
		return operationalFileInspection{}, fmt.Errorf("file changed while opening")
	}
	hash := sha256.New()
	prefix := &prefixCapture{limit: 8}
	writers := []io.Writer{hash, prefix}
	var buffer bytes.Buffer
	if load {
		writers = append(writers, &buffer)
	}
	size, err := io.Copy(io.MultiWriter(writers...), io.LimitReader(file, maxBytes+1))
	if err != nil {
		return operationalFileInspection{}, err
	}
	after, err := file.Stat()
	if err != nil {
		return operationalFileInspection{}, err
	}
	pathAfter, err := os.Lstat(path)
	if err != nil {
		return operationalFileInspection{}, err
	}
	if size > maxBytes || size != opened.Size() || !sameFileSnapshot(opened, after) || !sameFileSnapshot(after, pathAfter) {
		return operationalFileInspection{}, fmt.Errorf("file changed while reading")
	}
	device, inode := fileIdentity(after)
	return operationalFileInspection{
		Raw: buffer.Bytes(), Prefix: append([]byte(nil), prefix.data...),
		Evidence: OperationalSourceEvidence{
			Path: path, SizeBytes: size, SHA256: hex.EncodeToString(hash.Sum(nil)),
			Mode: uint32(after.Mode().Perm()), Device: device, Inode: inode,
			ModifiedUnixNano: after.ModTime().UnixNano(), Stable: true,
		},
	}, nil
}

func sameFileSnapshot(left, right os.FileInfo) bool {
	return left.Mode() == right.Mode() && left.Size() == right.Size() && left.ModTime().Equal(right.ModTime()) && os.SameFile(left, right)
}

func fileIdentity(info os.FileInfo) (uint64, uint64) {
	if info == nil {
		return 0, 0
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat == nil {
		return 0, 0
	}
	return uint64(stat.Dev), uint64(stat.Ino)
}

type prefixCapture struct {
	limit int
	data  []byte
}

func (w *prefixCapture) Write(payload []byte) (int, error) {
	remaining := w.limit - len(w.data)
	if remaining > 0 {
		if len(payload) < remaining {
			remaining = len(payload)
		}
		w.data = append(w.data, payload[:remaining]...)
	}
	return len(payload), nil
}

func writeExclusiveFile(path string, payload []byte, mode os.FileMode) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	if _, err := file.Write(payload); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}

func normalizeCreatedArtifact(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return fmt.Errorf("%w: created artifact must be a no-follow regular file", ErrOperationalPackageInvalid)
	}
	return os.Chmod(path, 0o600)
}

func validateStateArtifact(kind string, payload []byte, expectedSchemaHead int64) error {
	switch kind {
	case OperationalArtifactMigration:
		state, err := decodeMigrationState(payload)
		if err != nil {
			return fmt.Errorf("%w: %v", ErrOperationalPackageInvalid, err)
		}
		if state.CurrentVersion != expectedSchemaHead {
			return fmt.Errorf("%w: migration evidence schema head mismatch", ErrOperationalPackageInvalid)
		}
	case OperationalArtifactUpdate, OperationalArtifactHealth:
		if err := validateJSONObject(payload); err != nil {
			return fmt.Errorf("%w: %s must be one bounded JSON object", ErrOperationalPackageInvalid, kind)
		}
	}
	return nil
}

func decodeMigrationState(payload []byte) (OperationalMigrationState, error) {
	var state OperationalMigrationState
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&state); err != nil {
		return state, fmt.Errorf("migration evidence is invalid JSON")
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return state, fmt.Errorf("migration evidence has trailing content")
	}
	if state.Schema != OperationalMigrationStateSchema || strings.TrimSpace(state.Status) == "" || state.CurrentVersion < 0 || state.LatestVersion < state.CurrentVersion {
		return state, fmt.Errorf("migration evidence is incomplete or inconsistent")
	}
	return state, nil
}

func validateJSONObject(payload []byte) error {
	var value map[string]json.RawMessage
	decoder := json.NewDecoder(bytes.NewReader(payload))
	if err := decoder.Decode(&value); err != nil || len(value) == 0 {
		return fmt.Errorf("invalid JSON object")
	}
	return ensureJSONEOF(decoder)
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return fmt.Errorf("unexpected trailing JSON")
		}
		return err
	}
	return nil
}

func containsForbidden(payload []byte, values []string) bool {
	for _, value := range nonEmptyForbiddenValues(values) {
		if bytes.Contains(payload, []byte(value)) {
			return true
		}
	}
	return false
}

func containsForbiddenFile(path string, values []string) bool {
	forbidden := nonEmptyForbiddenValues(values)
	if len(forbidden) == 0 {
		return false
	}
	before, err := inspectStableFile(path, OperationalPackageMaxBytes, false)
	if err != nil {
		return true
	}
	fd, err := unix.Open(filepath.Clean(path), unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return true
	}
	file := os.NewFile(uintptr(fd), path)
	if file == nil {
		_ = unix.Close(fd)
		return true
	}
	defer file.Close()
	maxPattern := 0
	for _, value := range forbidden {
		if len(value) > maxPattern {
			maxPattern = len(value)
		}
	}
	buffer := make([]byte, 64<<10)
	carry := []byte{}
	for {
		read, readErr := file.Read(buffer)
		if read > 0 {
			window := make([]byte, 0, len(carry)+read)
			window = append(window, carry...)
			window = append(window, buffer[:read]...)
			if containsForbidden(window, forbidden) {
				return true
			}
			keep := maxPattern - 1
			if keep > len(window) {
				keep = len(window)
			}
			carry = append(carry[:0], window[len(window)-keep:]...)
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return true
		}
	}
	after, err := inspectStableFile(path, OperationalPackageMaxBytes, false)
	return err != nil || before.Evidence != after.Evidence
}

func nonEmptyForbiddenValues(values []string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value != "" {
			result = append(result, value)
		}
	}
	return result
}

func exactAbsolutePath(value string) (string, error) {
	if value == "" || value != strings.TrimSpace(value) || !filepath.IsAbs(value) || strings.ContainsRune(value, '\x00') {
		return "", fmt.Errorf("%w: path must be exact and absolute", ErrOperationalPackageInvalid)
	}
	clean := filepath.Clean(value)
	if clean != value {
		return "", fmt.Errorf("%w: path must already be clean", ErrOperationalPackageInvalid)
	}
	return clean, nil
}

func validatePackageID(value string) error {
	if value == "" || value != strings.TrimSpace(value) || value == "." || value == ".." || len(value) > 160 {
		return fmt.Errorf("%w: invalid package identity", ErrOperationalPackageInvalid)
	}
	for _, char := range value {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') || (char >= '0' && char <= '9') || char == '-' || char == '_' || char == '.' {
			continue
		}
		return fmt.Errorf("%w: invalid package identity", ErrOperationalPackageInvalid)
	}
	return nil
}

func requireRealDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("path must be a real directory")
	}
	return nil
}

func confinedOperationalPath(root, relative string) (string, error) {
	if relative == "" || relative != filepath.Clean(relative) || filepath.IsAbs(relative) || strings.ContainsRune(relative, '\x00') {
		return "", fmt.Errorf("invalid relative path")
	}
	path := filepath.Join(root, relative)
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path escapes package")
	}
	return path, nil
}

func normalizeSHA256(value string) string {
	value = strings.TrimSpace(value)
	if len(value) != sha256.Size*2 || strings.ToLower(value) != value {
		return ""
	}
	if _, err := hex.DecodeString(value); err != nil {
		return ""
	}
	return value
}

func operationalSpecForKind(kind string) (operationalArtifactSpec, bool) {
	for _, spec := range operationalArtifactSpecs {
		if spec.Kind == kind {
			return spec, true
		}
	}
	return operationalArtifactSpec{}, false
}

func classifyOperationalFileError(artifact string, err error) OperationalPackageFinding {
	message := err.Error()
	switch {
	case strings.Contains(message, "link"):
		return operationalFinding(OperationalFindingLink, artifact, "package entry must be a no-follow regular file")
	case os.IsNotExist(err):
		return operationalFinding(OperationalFindingMissingArtifact, artifact, "required package entry is missing")
	case strings.Contains(message, "exceeds"):
		return operationalFinding(OperationalFindingPackageTooLarge, artifact, "package entry exceeds fixed byte bound")
	case strings.Contains(message, "changed"):
		return operationalFinding(OperationalFindingSourceChanged, artifact, "package entry changed while being verified")
	default:
		return operationalFinding(OperationalFindingManifestInvalid, artifact, "package entry is not a readable bounded regular file")
	}
}

func operationalFinding(code, artifact, detail string) OperationalPackageFinding {
	return OperationalPackageFinding{Code: code, Artifact: artifact, Detail: detail}
}

func summarizeOperationalFindings(findings []OperationalPackageFinding) string {
	parts := make([]string, 0, len(findings))
	for _, finding := range findings {
		part := finding.Code
		if finding.Artifact != "" {
			part += ":" + finding.Artifact
		}
		parts = append(parts, part)
	}
	return strings.Join(parts, ",")
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
