package backupcoverage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"loom.local/loom/internal/backupcontracts"
	"loom.local/loom/internal/backupstrategy"
	"loom.local/loom/internal/box"
	"loom.local/loom/internal/config"
	"loom.local/loom/internal/hermesprofile"
	"loom.local/loom/internal/maintenance"
)

const (
	SchemaVersion = "loom.backup.coverage.v0.10"

	ModeLocal      = "local"
	ModeMainBacked = "main_backed"

	OverallOK       = "ok"
	OverallWarning  = "warning"
	OverallCritical = "critical"

	StatusCovered            = "covered"
	StatusMissing            = "missing"
	StatusUnknown            = "unknown"
	StatusExcludedExplicitly = "excluded_explicitly"
	StatusRequiresDecision   = "requires_decision"

	boxNotesSnapshotArtifactKind           = "box_notes_snapshot"
	maxPrivateBackupsCoverageEvidenceBytes = 64 << 20
)

type Options struct {
	ApplicationDataRoot    string
	HermesRecovery         hermesprofile.Policy
	HermesPolicyError      error
	Mode                   string
	DataDir                string
	ObjectStoreRoot        string
	ImportsRoot            string
	UserBackupsRoot        string
	PrivateBackupsRoot     string
	MainDocumentsRoot      string
	StorageRetentionRoot   string
	StorageArchiveRoot     string
	StorageExportRoot      string
	MainBoxPath            string
	BoxNotesRoot           string
	NotesProjectionRoot    string
	MainBoxPolicy          string
	BackupRoot             string
	ManifestPath           string
	ProvenanceManifestPath string
	// TrustedProvenanceManifestSHA256 must come from operation evidence retained
	// independently of ManifestPath. Check never derives this trust input itself.
	TrustedProvenanceManifestSHA256 string
	// MigrationEligibility is an optional, already planned read-only contract.
	// Coverage reports it but never derives cleanup authority from it.
	MigrationEligibility *backupstrategy.MigrationEligibilityReport
	Now                  func() time.Time
}

type Report struct {
	SchemaVersion      string    `json:"schema_version"`
	Mode               string    `json:"mode"`
	Status             string    `json:"status"`
	GeneratedAt        time.Time `json:"generated_at"`
	BackupRoot         string    `json:"backup_root,omitempty"`
	LatestManifestPath string    `json:"latest_manifest_path,omitempty"`
	Entries            []Entry   `json:"entries"`
	Summary            Summary   `json:"summary"`
	CriticalMissing    []string  `json:"critical_missing,omitempty"`
}

type Summary struct {
	Covered            int `json:"covered"`
	Missing            int `json:"missing"`
	Unknown            int `json:"unknown"`
	ExcludedExplicitly int `json:"excluded_explicitly"`
	RequiresDecision   int `json:"requires_decision"`
	CriticalMissing    int `json:"critical_missing"`
}

type BoundedSummary struct {
	SchemaVersion string    `json:"schema_version"`
	Status        string    `json:"status"`
	CapturedAt    time.Time `json:"captured_at"`
	Covered       int       `json:"covered"`
	Missing       int       `json:"missing"`
	Unknown       int       `json:"unknown"`
	Critical      int       `json:"critical"`
}

// Bounded strips paths and entry detail before backup evidence is persisted.
func Bounded(report Report) BoundedSummary {
	return BoundedSummary{
		SchemaVersion: SchemaVersion,
		Status:        report.Status,
		CapturedAt:    report.GeneratedAt.UTC(),
		Covered:       report.Summary.Covered,
		Missing:       report.Summary.Missing + report.Summary.RequiresDecision,
		Unknown:       report.Summary.Unknown,
		Critical:      report.Summary.CriticalMissing,
	}
}

type Entry struct {
	Key        string `json:"key"`
	Label      string `json:"label"`
	Category   string `json:"category"`
	Path       string `json:"path,omitempty"`
	Status     string `json:"status"`
	Critical   bool   `json:"critical"`
	Exists     bool   `json:"exists,omitempty"`
	FileCount  int64  `json:"file_count,omitempty"`
	TotalBytes int64  `json:"total_bytes,omitempty"`
	Policy     string `json:"policy,omitempty"`
	Message    string `json:"message,omitempty"`
}

type ContractPlan struct {
	SchemaVersion string           `json:"schema_version"`
	Status        string           `json:"status"`
	Mode          string           `json:"mode"`
	GeneratedAt   time.Time        `json:"generated_at"`
	Contracts     []BackupContract `json:"contracts"`
	Summary       ContractSummary  `json:"summary"`
}

type BackupContract struct {
	Key            string `json:"key"`
	Label          string `json:"label"`
	Category       string `json:"category"`
	Requirement    string `json:"requirement"`
	CoverageStatus string `json:"coverage_status"`
	ContractStatus string `json:"contract_status"`
	Critical       bool   `json:"critical"`
	Path           string `json:"path,omitempty"`
	Action         string `json:"action,omitempty"`
	Detail         string `json:"detail,omitempty"`
}

type ContractSummary struct {
	Satisfied      int `json:"satisfied"`
	NeedsAttention int `json:"needs_attention"`
	Critical       int `json:"critical"`
}

func OptionsFromConfig(cfg config.Config) Options {
	hermesPolicy, hermesErr := cfg.HermesRecoveryPolicy()
	dataDir := strings.TrimSpace(cfg.DataDir)
	if dataDir == "" {
		dataDir = config.DefaultDataDir
	}
	objectStore := strings.TrimSpace(cfg.ObjectStore)
	if objectStore == "" {
		objectStore = filepath.Join(dataDir, "object-store")
	}
	mainDocuments := cfg.MainDocumentsRoot()
	storageRetention := strings.TrimSpace(cfg.StorageRetention)
	if storageRetention == "" {
		storageRetention = filepath.Join(dataDir, "storage-retention")
	}
	storageExport := strings.TrimSpace(cfg.StorageExport)
	if storageExport == "" {
		storageExport = filepath.Join(dataDir, "storage-views", "main-export")
	}
	mainBox := strings.TrimSpace(cfg.BoxPath)
	if mainBox == "" {
		mainBox = filepath.Join("/home", "loomadmin", box.DefaultRootDirName)
	}
	return Options{
		ApplicationDataRoot: cfg.ApplicationDataBackupRoot,
		HermesRecovery:      hermesPolicy, HermesPolicyError: hermesErr,
		DataDir:              dataDir,
		ObjectStoreRoot:      objectStore,
		ImportsRoot:          cfg.ImportsRoot,
		UserBackupsRoot:      cfg.UserBackupsRoot,
		PrivateBackupsRoot:   cfg.UserBackupsRoot,
		MainDocumentsRoot:    mainDocuments,
		StorageRetentionRoot: storageRetention,
		StorageArchiveRoot:   cfg.ArchiveRoot,
		StorageExportRoot:    storageExport,
		MainBoxPath:          mainBox,
		BoxNotesRoot:         cfg.BoxNotesRoot(),
		NotesProjectionRoot:  cfg.NotesProjectionRoot(),
		MainBoxPolicy:        "selected_canonical_roots_copied_once",
		BackupRoot:           filepath.Join(dataDir, "backups", "main"),
	}
}

func PlanContracts(ctx context.Context, opts Options) (ContractPlan, error) {
	report, err := Check(ctx, opts)
	if err != nil {
		return ContractPlan{}, err
	}
	return PlanContractsFromReport(report), nil
}

func PlanContractsFromReport(report Report) ContractPlan {
	plan := ContractPlan{
		SchemaVersion: SchemaVersion,
		Status:        OverallOK,
		Mode:          firstNonEmpty(report.Mode, ModeLocal),
		GeneratedAt:   report.GeneratedAt,
		Contracts:     make([]BackupContract, 0, len(report.Entries)),
	}
	for _, entry := range report.Entries {
		contract := contractFromEntry(entry)
		plan.Contracts = append(plan.Contracts, contract)
		if contract.ContractStatus == "satisfied" {
			plan.Summary.Satisfied++
			continue
		}
		plan.Summary.NeedsAttention++
		if contract.Critical {
			plan.Summary.Critical++
		}
	}
	if plan.Summary.Critical > 0 {
		plan.Status = OverallCritical
	} else if plan.Summary.NeedsAttention > 0 {
		plan.Status = OverallWarning
	}
	return plan
}

func contractFromEntry(entry Entry) BackupContract {
	status := "needs_attention"
	if entry.Status == StatusCovered || entry.Status == StatusExcludedExplicitly {
		status = "satisfied"
	}
	return BackupContract{
		Key:            entry.Key,
		Label:          entry.Label,
		Category:       entry.Category,
		Requirement:    backupRequirementForEntry(entry),
		CoverageStatus: entry.Status,
		ContractStatus: status,
		Critical:       entry.Critical,
		Path:           entry.Path,
		Action:         backupActionForEntry(entry),
		Detail:         firstNonEmpty(entry.Message, entry.Policy),
	}
}

func backupRequirementForEntry(entry Entry) string {
	switch entry.Category {
	case "runtime_config":
		return "must be restorable from the PostgreSQL dump or runtime backup manifest"
	case "runtime_root":
		return "must exist and be copied by main backup snapshots"
	case "authoritative_notes_root":
		return "must remain an authoritative notes input and be protected by watched-root or private-backup policy"
	case "excluded_view":
		return "must be explicitly excluded as rebuildable generated output"
	case "latest_backup":
		return "latest backup manifest must declare this captured path or policy"
	case "policy":
		return "backup policy must be explicit so operators know whether the path is canonical or generated"
	case "standalone_backup_contract":
		return "active standalone contract target must exist and be protected by watched-root private backups"
	default:
		return "must have an explicit backup policy"
	}
}

func backupActionForEntry(entry Entry) string {
	switch entry.Status {
	case StatusCovered:
		return "none"
	case StatusExcludedExplicitly:
		if entry.Category == "standalone_backup_contract" {
			return "keep disabled unless this path should be protected again"
		}
		return "keep exclusion documented"
	case StatusMissing:
		if entry.Category == "standalone_backup_contract" {
			return "restore the source root, update the contract target, or disable/delete the stale contract"
		}
		return "create the source root, add it to backup coverage, or update the policy"
	case StatusUnknown:
		if entry.Category == "standalone_backup_contract" {
			return "fix the contract YAML or verify the owner-node target"
		}
		return "inspect permissions and rerun backup coverage"
	case StatusRequiresDecision:
		return "record an explicit operator policy decision"
	default:
		return "review backup coverage status"
	}
}

func Check(ctx context.Context, opts Options) (Report, error) {
	opts = normalizeOptions(opts)
	now := time.Now().UTC()
	if opts.Now != nil {
		now = opts.Now().UTC()
	}
	report := Report{
		SchemaVersion: SchemaVersion,
		Mode:          opts.Mode,
		Status:        OverallOK,
		GeneratedAt:   now,
		BackupRoot:    opts.BackupRoot,
	}
	add := func(entry Entry) {
		report.Entries = append(report.Entries, entry)
	}

	if opts.HermesRecovery.Enabled || opts.HermesPolicyError != nil {
		entry := Entry{Key: "morathustra-recovery", Label: "Morathustra Hermes recovery", Category: "recovery_package", Critical: true, Status: StatusCovered, Policy: "authenticated_native_snapshot", Message: "fresh authenticated native Hermes recovery is available"}
		if opts.HermesRecovery.Identity == hermesprofile.MinaIdentity {
			entry.Key, entry.Label = "mina-recovery", "MINA Hermes recovery"
		}
		_, err := hermesprofile.Check(ctx, opts.HermesRecovery, now)
		if opts.HermesPolicyError != nil {
			err = opts.HermesPolicyError
		}
		if err != nil {
			entry.Status = StatusMissing
			entry.Message = "enabled " + entry.Label + " lacks fresh valid recovery evidence"
		}
		add(entry)
	}
	add(Entry{
		Key:      "postgres",
		Label:    "PostgreSQL",
		Category: "runtime_config",
		Status:   StatusCovered,
		Critical: true,
		Message:  "captured by main_backup as a custom-format pg_dump",
	})
	add(Entry{
		Key:      "provenance-postgres",
		Label:    "Provenance PostgreSQL ledger",
		Category: "runtime_config",
		Status:   StatusCovered,
		Critical: true,
		Policy:   "requires_complete_isolated_dump_and_logical_recovery_contract",
		Message:  "isolated provenance recovery is proven by the latest backup manifest entry below",
	})
	add(directoryEntry(ctx, "object-store", "object-store", opts.ObjectStoreRoot, true))
	if opts.ApplicationDataRoot != "" {
		entry := custodyDirectoryEntry(ctx, "application-data", "managed application data", opts.ApplicationDataRoot, true)
		entry.Policy = "cloud_history"
		if entry.Status == StatusCovered {
			entry.Status = StatusUnknown
			entry.Message = "allocation pool is enrolled in cloud history; directory readability is not a completed archive or application-consistent recovery"
		}
		add(entry)
	}
	add(custodyDirectoryEntry(ctx, "imports", "Lane Imports custody", opts.ImportsRoot, true))
	add(custodyDirectoryEntry(ctx, "user-backups", "user backups", opts.UserBackupsRoot, true))
	add(directoryEntry(ctx, "main-documents", "main-documents", opts.MainDocumentsRoot, true))
	add(directoryEntry(ctx, "storage-retention", "storage-retention", opts.StorageRetentionRoot, true))
	add(custodyDirectoryEntry(ctx, "storage-archive", "storage archive", opts.StorageArchiveRoot, true))
	if strings.TrimSpace(opts.BoxNotesRoot) != "" {
		add(notesSourceRootEntry(ctx, "box-notes-root", "Box Notes source root", opts.BoxNotesRoot, true))
	} else {
		add(Entry{
			Key:      "box-notes-root",
			Label:    "Box Notes source root",
			Category: "authoritative_notes_root",
			Status:   StatusCovered,
			Critical: true,
			Policy:   "box_notes_root_recorded_by_configured_source_root_reconciliation",
			Message:  "pass an explicit box notes root to inventory the source path",
		})
	}
	add(Entry{
		Key:      "project-notes-source-roots",
		Label:    "project notes source roots",
		Category: "authoritative_notes_root",
		Status:   StatusCovered,
		Critical: true,
		Policy:   "project_notes_are_protected_by_private_backups_and_watched_root_snapshots",
		Message:  "project-owned notes remain canonical in each project notes/ folder",
	})
	add(excludedGeneratedDirectoryEntry("notes-projection", "generated Notes projection", opts.NotesProjectionRoot, "rebuild_from_canonical_notes_roots"))
	add(Entry{
		Key:      "knowledge-index-db",
		Label:    "knowledge index database",
		Category: "runtime_config",
		Status:   StatusCovered,
		Critical: true,
		Policy:   "captured_by_postgres_dump",
		Message:  "knowledge schema tables are captured by main_backup PostgreSQL dumps",
	})
	add(Entry{
		Key:      "storage-export",
		Label:    "generated storage export",
		Category: "excluded_view",
		Path:     opts.StorageExportRoot,
		Status:   StatusExcludedExplicitly,
		Policy:   "rebuild_from_canonical_roots",
		Message:  "generated storage view is not backed up as a source root",
	})
	add(mainBoxEntry(opts))
	for _, entry := range standaloneBackupContractEntries(ctx, opts) {
		add(entry)
	}

	manifestPath, manifest, found, err := resolveManifest(opts)
	if err != nil {
		add(Entry{
			Key:      "latest-backup-manifest",
			Label:    "latest backup manifest",
			Category: "latest_backup",
			Path:     firstNonEmpty(opts.ManifestPath, opts.BackupRoot),
			Status:   StatusUnknown,
			Message:  err.Error(),
		})
		add(resolveProvenanceManifestEntry(ctx, opts, "", maintenance.BackupManifest{}))
	} else if !found {
		add(Entry{
			Key:      "latest-backup-manifest",
			Label:    "latest backup manifest",
			Category: "latest_backup",
			Path:     firstNonEmpty(opts.ManifestPath, opts.BackupRoot),
			Status:   StatusUnknown,
			Message:  "no backup manifest found yet",
		})
		add(resolveProvenanceManifestEntry(ctx, opts, "", maintenance.BackupManifest{}))
	} else {
		report.LatestManifestPath = manifestPath
		add(resolveProvenanceManifestEntry(ctx, opts, manifestPath, manifest))
		add(manifestPathEntry("latest-main-documents", "latest backup includes main-documents", manifest.Paths.MainDocuments, "main-documents", true))
		add(manifestPathEntry("latest-imports", "latest backup includes Imports custody", manifest.Paths.Imports, "imports", true))
		add(manifestPathEntry("latest-imports-evidence", "latest backup includes Imports portable evidence", manifest.Paths.ImportsEvidence, "imports-evidence.json", true))
		userBackupsPath := strings.TrimSpace(manifest.Paths.UserBackups)
		privateBackupsPath := strings.TrimSpace(manifest.Paths.PrivateBackups)
		if userBackupsPath != "" {
			add(manifestPathEntry("latest-user-backups", "latest backup includes canonical user-backups", userBackupsPath, "user-backups", true))
		} else {
			add(manifestPathEntry("latest-user-backups", "latest backup includes user-backups", privateBackupsPath, "private-backups", true))
		}
		if userBackupsPath != "" && privateBackupsPath != "" {
			add(manifestPathEntry("latest-private-backups", "latest backup also includes retained legacy private-backups", privateBackupsPath, "private-backups", true))
			add(manifestPathEntry("latest-private-backups-evidence", "latest backup includes retained legacy private-backup byte evidence", manifest.Paths.PrivateBackupsEvidence, maintenance.PrivateBackupsEvidencePath, true))
			add(manifestAuthenticatedPrivateBackupsEvidenceEntry(ctx, manifestPath, manifest, "latest-private-backups-evidence-artifact", "latest backup authenticates retained legacy private-backup evidence", true))
		}
		add(manifestPathEntry("latest-storage-retention", "latest backup includes storage-retention", manifest.Paths.StorageRetention, "storage-retention", true))
		add(manifestPathEntry("latest-storage-archive", "latest backup includes storage-archive", manifest.Paths.StorageArchive, "storage-archive", true))
		add(manifestArtifactEntry("latest-box-notes", "latest backup includes canonical Box Notes", manifest.Artifacts, boxNotesSnapshotArtifactKind, "box-notes", true))
		add(manifestExcludedPathEntry("latest-notes-projection", "latest backup excludes generated Notes projection", manifest.Paths.NotesProjection, true))
		add(manifestPolicyEntry("latest-main-box-policy", "latest backup declares main Box policy", manifest.Policies.MainBox, true))
		add(manifestPolicyEntry("latest-notes-source-roots-policy", "latest backup declares notes source roots policy", manifest.Policies.NotesSourceRoots, true))
		add(manifestPolicyEntry("latest-notes-projection-policy", "latest backup declares notes projection policy", manifest.Policies.NotesProjection, true))
		add(manifestPolicyEntry("latest-knowledge-index-policy", "latest backup declares knowledge index policy", manifest.Policies.KnowledgeIndex, true))
		add(manifestPolicyEntry("latest-imports-policy", "latest backup declares Imports custody policy", manifest.Policies.Imports, true))
		add(manifestPolicyEntry("latest-imports-snapshot-policy", "latest backup declares Imports snapshot method", manifest.Policies.ImportsSnapshot, true))
	}
	if opts.MigrationEligibility != nil {
		add(migrationEligibilityEntry(*opts.MigrationEligibility))
	}

	report.Summary, report.CriticalMissing = summarize(report.Entries)
	report.Status = overallStatus(report.Summary)
	return report, nil
}

func migrationEligibilityEntry(report backupstrategy.MigrationEligibilityReport) Entry {
	entry := Entry{
		Key: "migration-eligibility", Label: "replacement strict restore and migration eligibility",
		Category: "migration_eligibility", Critical: true,
		Policy: "read_only_digest_bound_report_cleanup_apply_forbidden",
	}
	if report.Schema == backupstrategy.MigrationEligibilitySchema && report.Status == backupstrategy.MigrationEligibilityEligible && report.Digest != "" && !report.CleanupApplyAllowed {
		entry.Status = StatusCovered
		entry.Message = fmt.Sprintf("strict restore accepted; %d exact legacy candidate(s), %d expected allocated bytes; cleanup still requires Slice 6 review", len(report.Candidates), report.ExpectedReclaimableBytes)
		return entry
	}
	entry.Status = StatusRequiresDecision
	entry.Message = "migration remains blocked: " + strings.Join(report.Blockers, ", ")
	return entry
}

func provenanceManifestEntry(ctx context.Context, manifestPath string, manifest maintenance.BackupManifest, trustedManifestSHA256 string) Entry {
	entry := Entry{
		Key:      "latest-provenance-ledger",
		Label:    "latest backup includes the complete provenance ledger",
		Category: "latest_backup",
		Critical: true,
	}
	if _, err := maintenance.ValidateProvenanceBackupManifest(manifest); err != nil {
		entry.Status = StatusMissing
		entry.Message = err.Error()
		return entry
	}
	entry.Path = manifest.Provenance.Database.DumpFile
	trustedManifestSHA256 = strings.TrimSpace(trustedManifestSHA256)
	if trustedManifestSHA256 == "" {
		entry.Status = StatusUnknown
		entry.Message = "independently retained provenance manifest sha256 is unavailable"
		return entry
	}
	verification, err := maintenance.VerifyProvenanceBackupPackage(ctx, filepath.Dir(manifestPath), trustedManifestSHA256)
	if err != nil {
		entry.Status = StatusMissing
		entry.Message = err.Error()
		return entry
	}
	if verification.Status != maintenance.VerificationSucceeded {
		entry.Status = StatusMissing
		entry.Message = "provenance recovery package verification failed"
		return entry
	}
	entry.Status = StatusCovered
	entry.Exists = true
	entry.TotalBytes = verification.DumpSizeBytes
	entry.Policy = "complete_schema_counts_graph_digest_and_authenticated_dump"
	entry.Message = "complete isolated provenance recovery package is present"
	return entry
}

func resolveProvenanceManifestEntry(ctx context.Context, opts Options, fallbackPath string, fallback maintenance.BackupManifest) Entry {
	manifestPath := strings.TrimSpace(opts.ProvenanceManifestPath)
	manifest := fallback
	if manifestPath != "" {
		var err error
		manifest, err = maintenance.ReadBackupManifest(manifestPath)
		if err != nil {
			return Entry{Key: "latest-provenance-ledger", Label: "latest backup includes the complete provenance ledger", Category: "latest_backup", Critical: true, Status: StatusMissing, Path: manifestPath, Message: err.Error()}
		}
	} else if fallback.Schema != maintenance.BackupManifestSchemaV010 {
		return Entry{Key: "latest-provenance-ledger", Label: "latest backup includes the complete provenance ledger", Category: "latest_backup", Critical: true, Status: StatusUnknown, Message: "independently retained provenance recovery operation evidence is unavailable"}
	} else {
		manifestPath = fallbackPath
	}
	return provenanceManifestEntry(ctx, manifestPath, manifest, opts.TrustedProvenanceManifestSHA256)
}

func normalizeOptions(opts Options) Options {
	opts.Mode = strings.TrimSpace(opts.Mode)
	if opts.Mode == "" {
		opts.Mode = ModeLocal
	}
	if strings.TrimSpace(opts.DataDir) == "" {
		opts.DataDir = config.DefaultDataDir
	}
	if strings.TrimSpace(opts.ObjectStoreRoot) == "" {
		opts.ObjectStoreRoot = filepath.Join(opts.DataDir, "object-store")
	}
	if strings.TrimSpace(opts.ImportsRoot) == "" {
		opts.ImportsRoot = filepath.Join(opts.DataDir, "lane", "accepted")
	}
	if strings.TrimSpace(opts.PrivateBackupsRoot) != "" {
		opts.UserBackupsRoot = opts.PrivateBackupsRoot
	} else if strings.TrimSpace(opts.UserBackupsRoot) == "" {
		opts.UserBackupsRoot = filepath.Join(opts.DataDir, "private-backups")
	}
	if strings.TrimSpace(opts.StorageRetentionRoot) == "" {
		opts.StorageRetentionRoot = filepath.Join(opts.DataDir, "storage-retention")
	}
	if strings.TrimSpace(opts.StorageArchiveRoot) == "" {
		opts.StorageArchiveRoot = filepath.Join(opts.DataDir, "storage-archive")
	}
	if strings.TrimSpace(opts.StorageExportRoot) == "" {
		opts.StorageExportRoot = filepath.Join(opts.DataDir, "storage-views", "main-export")
	}
	if strings.TrimSpace(opts.MainBoxPath) == "" {
		opts.MainBoxPath = filepath.Join("/home", "loomadmin", box.DefaultRootDirName)
	}
	if strings.TrimSpace(opts.MainDocumentsRoot) == "" {
		opts.MainDocumentsRoot = filepath.Join(opts.MainBoxPath, "Documents")
	}
	if strings.TrimSpace(opts.BoxNotesRoot) == "" {
		opts.BoxNotesRoot = filepath.Join(opts.MainBoxPath, "Notes")
	}
	if strings.TrimSpace(opts.NotesProjectionRoot) == "" {
		opts.NotesProjectionRoot = filepath.Join(opts.DataDir, "generated", config.DefaultNotesProjection)
	}
	if strings.TrimSpace(opts.MainBoxPolicy) == "" {
		opts.MainBoxPolicy = "selected_canonical_roots_copied_once"
	}
	if strings.TrimSpace(opts.BackupRoot) == "" {
		opts.BackupRoot = filepath.Join(opts.DataDir, "backups", "main")
	}
	opts.DataDir = filepath.Clean(opts.DataDir)
	opts.ObjectStoreRoot = filepath.Clean(opts.ObjectStoreRoot)
	opts.ImportsRoot = filepath.Clean(opts.ImportsRoot)
	opts.UserBackupsRoot = filepath.Clean(opts.UserBackupsRoot)
	opts.PrivateBackupsRoot = opts.UserBackupsRoot
	opts.MainDocumentsRoot = filepath.Clean(opts.MainDocumentsRoot)
	opts.StorageRetentionRoot = filepath.Clean(opts.StorageRetentionRoot)
	opts.StorageArchiveRoot = filepath.Clean(opts.StorageArchiveRoot)
	opts.StorageExportRoot = filepath.Clean(opts.StorageExportRoot)
	opts.MainBoxPath = filepath.Clean(opts.MainBoxPath)
	if strings.TrimSpace(opts.BoxNotesRoot) != "" {
		opts.BoxNotesRoot = filepath.Clean(opts.BoxNotesRoot)
	}
	opts.NotesProjectionRoot = filepath.Clean(opts.NotesProjectionRoot)
	opts.BackupRoot = filepath.Clean(opts.BackupRoot)
	opts.ManifestPath = strings.TrimSpace(opts.ManifestPath)
	opts.ProvenanceManifestPath = strings.TrimSpace(opts.ProvenanceManifestPath)
	opts.TrustedProvenanceManifestSHA256 = strings.TrimSpace(opts.TrustedProvenanceManifestSHA256)
	return opts
}

func directoryEntry(ctx context.Context, key, label, path string, critical bool) Entry {
	entry := Entry{
		Key:      key,
		Label:    label,
		Category: "runtime_root",
		Path:     filepath.Clean(strings.TrimSpace(path)),
		Critical: critical,
	}
	if err := ctx.Err(); err != nil {
		entry.Status = StatusUnknown
		entry.Message = err.Error()
		return entry
	}
	info, err := os.Stat(entry.Path)
	if err != nil {
		if os.IsNotExist(err) {
			entry.Status = StatusMissing
			entry.Message = "root does not exist"
			return entry
		}
		entry.Status = StatusUnknown
		entry.Message = err.Error()
		return entry
	}
	if !info.IsDir() {
		entry.Status = StatusMissing
		entry.Message = "path exists but is not a directory"
		return entry
	}
	fileCount, totalBytes, err := maintenance.DirectoryInventory(entry.Path)
	if err != nil {
		entry.Status = StatusUnknown
		entry.Exists = true
		if os.IsPermission(err) {
			entry.Policy = "permission_blocked"
			entry.Message = "permission blocked: " + err.Error()
		} else {
			entry.Message = err.Error()
		}
		return entry
	}
	entry.Status = StatusCovered
	entry.Exists = true
	entry.FileCount = fileCount
	entry.TotalBytes = totalBytes
	return entry
}

func custodyDirectoryEntry(ctx context.Context, key, label, pathValue string, critical bool) Entry {
	entry := Entry{
		Key:      key,
		Label:    label,
		Category: "canonical_custody_root",
		Path:     filepath.Clean(strings.TrimSpace(pathValue)),
		Critical: critical,
	}
	if err := ctx.Err(); err != nil {
		entry.Status = StatusUnknown
		entry.Message = err.Error()
		return entry
	}
	info, err := os.Lstat(entry.Path)
	if err != nil {
		if os.IsNotExist(err) {
			entry.Status = StatusMissing
			entry.Message = "root does not exist"
			return entry
		}
		entry.Status = StatusUnknown
		entry.Message = err.Error()
		return entry
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		entry.Status = StatusMissing
		entry.Message = "path exists but is not a real directory"
		return entry
	}
	directory, err := os.Open(entry.Path)
	if err == nil {
		_, err = directory.Readdirnames(1)
		if err == io.EOF {
			err = nil
		}
		if closeErr := directory.Close(); err == nil {
			err = closeErr
		}
	}
	if err != nil {
		entry.Status = StatusUnknown
		entry.Exists = true
		if os.IsPermission(err) {
			entry.Policy = "permission_blocked"
			entry.Message = "permission blocked: " + err.Error()
		} else {
			entry.Message = err.Error()
		}
		return entry
	}
	entry.Status = StatusCovered
	entry.Exists = true
	entry.Policy = "bounded_readability_check"
	entry.Message = "canonical custody root is readable; full inventory is deferred to backup/verification work"
	return entry
}

func notesSourceRootEntry(ctx context.Context, key, label, path string, critical bool) Entry {
	entry := directoryEntry(ctx, key, label, path, critical)
	entry.Category = "authoritative_notes_root"
	return entry
}

func excludedGeneratedDirectoryEntry(key, label, pathValue, policy string) Entry {
	pathValue = filepath.Clean(strings.TrimSpace(pathValue))
	entry := Entry{
		Key:      key,
		Label:    label,
		Category: "generated_artifact",
		Path:     pathValue,
		Status:   StatusExcludedExplicitly,
		Policy:   policy,
		Message:  "generated projection is rebuildable and is not backed up as canonical data",
	}
	if info, err := os.Lstat(pathValue); err == nil {
		entry.Exists = info.IsDir()
	}
	return entry
}

func mainBoxEntry(opts Options) Entry {
	status := StatusExcludedExplicitly
	message := "the whole Box is not duplicated; canonical Documents and Box Notes are copied once as selected backup roots, while project roots follow their own contracts"
	if strings.TrimSpace(opts.MainBoxPolicy) == "requires_decision" {
		status = StatusRequiresDecision
		message = "main Box backup policy still requires an operator decision"
	}
	return Entry{
		Key:      "main-box-policy",
		Label:    "main LOOM Box policy",
		Category: "policy",
		Path:     opts.MainBoxPath,
		Status:   status,
		Policy:   opts.MainBoxPolicy,
		Message:  message,
	}
}

func standaloneBackupContractEntries(ctx context.Context, opts Options) []Entry {
	mainBoxPath := strings.TrimSpace(opts.MainBoxPath)
	if mainBoxPath == "" {
		return nil
	}
	directoryRelPath := backupcontracts.DefaultDirectoryRelPath
	contract, contractErr := box.LoadContract(box.ContractPath(mainBoxPath))
	if contractErr == nil && strings.TrimSpace(contract.Policies[box.PolicyBackupContracts]) != "" {
		directoryRelPath = contract.Policies[box.PolicyBackupContracts]
	}
	items, err := backupcontracts.List(mainBoxPath, directoryRelPath)
	if err != nil {
		if contractErr != nil {
			return nil
		}
		return []Entry{{
			Key:      "standalone-backup-contracts",
			Label:    "standalone backup contracts",
			Category: "standalone_backup_contract",
			Path:     filepath.Join(mainBoxPath, filepath.FromSlash(directoryRelPath)),
			Status:   StatusUnknown,
			Critical: false,
			Policy:   "contract_directory_unavailable",
			Message:  err.Error(),
		}}
	}
	if len(items) == 0 {
		return nil
	}
	entries := make([]Entry, 0, len(items))
	for _, item := range items {
		entries = append(entries, standaloneBackupContractEntry(ctx, opts, item))
	}
	return entries
}

func standaloneBackupContractEntry(ctx context.Context, opts Options, item backupcontracts.StoredContract) Entry {
	itemKey := strings.ToLower(strings.TrimSpace(item.Key))
	if itemKey == "" {
		itemKey = "invalid"
	}
	entry := Entry{
		Key:      "backup-contract-" + itemKey,
		Label:    "backup contract " + itemKey,
		Category: "standalone_backup_contract",
		Path:     item.Path,
		Status:   StatusUnknown,
		Critical: false,
	}
	if item.Error != "" {
		entry.Policy = "invalid_contract_yaml"
		entry.Message = item.Error
		return entry
	}

	contract := backupcontracts.Normalize(item.Contract)
	entry.Key = "backup-contract-" + contract.Key
	entry.Label = firstNonEmpty(contract.DisplayName, "backup contract "+contract.Key)
	entry.Path = backupContractTargetPath(opts, contract)
	entry.Policy = "standalone_backup_contract:" + contract.Backup.Mode
	if contract.Status == backupcontracts.StatusDisabled || contract.Backup.Mode == backupcontracts.BackupModeNone {
		entry.Status = StatusExcludedExplicitly
		entry.Critical = false
		entry.Message = "contract is disabled or backup mode is none"
		return entry
	}
	if contract.Target.Scope == backupcontracts.TargetScopeOwnerNodeAbsolute && !strings.EqualFold(strings.TrimSpace(contract.OwnerNode), "main") {
		entry.Status = StatusUnknown
		entry.Critical = false
		entry.Policy = "remote_owner_node:" + contract.OwnerNode
		entry.Message = "target belongs to a different owner node and cannot be verified from this coverage host"
		return entry
	}

	targetEntry := directoryEntry(ctx, entry.Key, entry.Label, entry.Path, true)
	targetEntry.Category = entry.Category
	targetEntry.Policy = entry.Policy
	if targetEntry.Status == StatusCovered {
		targetEntry.Message = "contract is active and source root exists; canonical user-backups and latest main backup entries indicate payload capture status"
	} else if targetEntry.Status == StatusMissing {
		targetEntry.Message = "source root does not exist; disable or update the backup contract if this path is stale"
	}
	return targetEntry
}

func backupContractTargetPath(opts Options, contract backupcontracts.Contract) string {
	targetPath := strings.TrimSpace(contract.Target.Path)
	if contract.Target.Scope == backupcontracts.TargetScopeBoxRelative {
		return filepath.Join(opts.MainBoxPath, filepath.FromSlash(targetPath))
	}
	return targetPath
}

func resolveManifest(opts Options) (string, maintenance.BackupManifest, bool, error) {
	if strings.TrimSpace(opts.ManifestPath) != "" {
		manifestPath := filepath.Clean(opts.ManifestPath)
		manifest, err := maintenance.ReadBackupManifest(manifestPath)
		return manifestPath, manifest, err == nil, err
	}
	matches, err := filepath.Glob(filepath.Join(opts.BackupRoot, "*", "manifest.json"))
	if err != nil {
		return "", maintenance.BackupManifest{}, false, err
	}
	if len(matches) == 0 {
		return "", maintenance.BackupManifest{}, false, nil
	}
	sort.Strings(matches)
	manifestPath := matches[len(matches)-1]
	manifest, err := maintenance.ReadBackupManifest(manifestPath)
	return manifestPath, manifest, err == nil, err
}

func manifestPathEntry(key, label, value, expected string, critical bool) Entry {
	if strings.TrimSpace(value) == expected {
		return Entry{Key: key, Label: label, Category: "latest_backup", Path: value, Status: StatusCovered, Critical: critical}
	}
	return Entry{
		Key:      key,
		Label:    label,
		Category: "latest_backup",
		Path:     value,
		Status:   StatusMissing,
		Critical: critical,
		Message:  fmt.Sprintf("latest manifest does not list %s", expected),
	}
}

func manifestArtifactEntry(key, label string, artifacts []maintenance.BackupManifestArtifact, kind, expectedPath string, critical bool) Entry {
	for _, artifact := range artifacts {
		if strings.TrimSpace(artifact.Kind) != kind {
			continue
		}
		if strings.TrimSpace(artifact.Path) == expectedPath {
			return Entry{Key: key, Label: label, Category: "latest_backup", Path: artifact.Path, Status: StatusCovered, Critical: critical}
		}
		return Entry{
			Key:      key,
			Label:    label,
			Category: "latest_backup",
			Path:     artifact.Path,
			Status:   StatusMissing,
			Critical: critical,
			Message:  fmt.Sprintf("latest manifest lists %s at unexpected path %q", kind, artifact.Path),
		}
	}
	return Entry{
		Key:      key,
		Label:    label,
		Category: "latest_backup",
		Status:   StatusMissing,
		Critical: critical,
		Message:  fmt.Sprintf("latest manifest does not list %s", kind),
	}
}

func manifestAuthenticatedPrivateBackupsEvidenceEntry(ctx context.Context, manifestPath string, manifest maintenance.BackupManifest, key, label string, critical bool) Entry {
	const kind = maintenance.ArtifactKindPrivateBackupsEvidence
	expectedPath := maintenance.PrivateBackupsEvidencePath
	missing := func(pathValue, message string) Entry {
		return Entry{
			Key:      key,
			Label:    label,
			Category: "latest_backup",
			Path:     pathValue,
			Status:   StatusMissing,
			Critical: critical,
			Message:  message,
		}
	}

	matches := make([]maintenance.BackupManifestArtifact, 0, 1)
	for _, artifact := range manifest.Artifacts {
		if strings.TrimSpace(artifact.Kind) == kind {
			matches = append(matches, artifact)
		}
	}
	if len(matches) != 1 {
		return missing("", fmt.Sprintf("latest manifest requires exactly one %s artifact (got %d)", kind, len(matches)))
	}
	artifact := matches[0]
	declaredPath := strings.TrimSpace(manifest.Paths.PrivateBackupsEvidence)
	if declaredPath != expectedPath || strings.TrimSpace(artifact.Path) != declaredPath {
		return missing(artifact.Path, fmt.Sprintf("latest manifest lists %s at an unexpected or inconsistent path", kind))
	}
	digest, err := hex.DecodeString(strings.TrimSpace(artifact.SHA256))
	if artifact.SizeBytes == nil || *artifact.SizeBytes <= 0 || *artifact.SizeBytes > maxPrivateBackupsCoverageEvidenceBytes || err != nil || len(digest) != sha256.Size {
		return missing(artifact.Path, fmt.Sprintf("latest manifest %s artifact lacks bounded size or sha256 evidence", kind))
	}
	backupDir := filepath.Dir(filepath.Clean(manifestPath))
	evidencePath, err := maintenance.ResolveBackupPath(backupDir, declaredPath)
	if err != nil {
		return missing(declaredPath, fmt.Sprintf("resolve retained legacy evidence: %v", err))
	}
	custodyPath, err := maintenance.ResolveBackupPath(backupDir, manifest.Paths.PrivateBackups)
	if err != nil {
		return missing(declaredPath, fmt.Sprintf("resolve retained legacy custody: %v", err))
	}
	if err := maintenance.ValidatePrivateBackupsEvidence(ctx, custodyPath, manifest.Paths.PrivateBackups, evidencePath); err != nil {
		return missing(declaredPath, fmt.Sprintf("validate retained legacy payload evidence: %v", err))
	}
	actualSHA256, actualSize, err := hashNoFollowRegular(evidencePath, maxPrivateBackupsCoverageEvidenceBytes)
	if err != nil {
		return missing(declaredPath, fmt.Sprintf("authenticate retained legacy evidence: %v", err))
	}
	if actualSize != *artifact.SizeBytes || actualSHA256 != strings.ToLower(strings.TrimSpace(artifact.SHA256)) {
		return missing(declaredPath, "retained legacy evidence bytes do not match the manifest artifact")
	}
	return Entry{Key: key, Label: label, Category: "latest_backup", Path: artifact.Path, Status: StatusCovered, Critical: critical}
}

func hashNoFollowRegular(pathValue string, maxBytes int64) (string, int64, error) {
	pathValue = filepath.Clean(strings.TrimSpace(pathValue))
	if maxBytes <= 0 {
		return "", 0, fmt.Errorf("positive byte limit is required")
	}
	before, err := os.Lstat(pathValue)
	if err != nil {
		return "", 0, err
	}
	if !before.Mode().IsRegular() || before.Size() > maxBytes {
		return "", 0, fmt.Errorf("path is not a no-follow regular file")
	}
	file, err := os.Open(pathValue)
	if err != nil {
		return "", 0, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil {
		return "", 0, err
	}
	if !opened.Mode().IsRegular() || !os.SameFile(before, opened) {
		return "", 0, fmt.Errorf("file changed while opening")
	}
	hash := sha256.New()
	size, err := io.Copy(hash, io.LimitReader(file, maxBytes+1))
	if err != nil {
		return "", 0, err
	}
	if size > maxBytes {
		return "", 0, fmt.Errorf("file exceeds %d-byte limit", maxBytes)
	}
	after, err := file.Stat()
	if err != nil {
		return "", 0, err
	}
	pathAfter, err := os.Lstat(pathValue)
	if err != nil {
		return "", 0, err
	}
	if size != before.Size() || after.Size() != before.Size() || !os.SameFile(before, after) || !os.SameFile(after, pathAfter) {
		return "", 0, fmt.Errorf("file changed while hashing")
	}
	return hex.EncodeToString(hash.Sum(nil)), size, nil
}

func manifestExcludedPathEntry(key, label, value string, critical bool) Entry {
	if strings.TrimSpace(value) == "" {
		return Entry{
			Key:      key,
			Label:    label,
			Category: "latest_backup",
			Status:   StatusExcludedExplicitly,
			Critical: critical,
			Policy:   "rebuild_from_canonical_notes_roots",
		}
	}
	return Entry{
		Key:      key,
		Label:    label,
		Category: "latest_backup",
		Path:     value,
		Status:   StatusRequiresDecision,
		Critical: critical,
		Message:  "latest backup duplicates the generated Notes projection; remove it from the default backup after confirming canonical source coverage",
	}
}

func manifestPolicyEntry(key, label, value string, critical bool) Entry {
	if strings.TrimSpace(value) != "" {
		return Entry{Key: key, Label: label, Category: "latest_backup", Status: StatusCovered, Critical: critical, Policy: value}
	}
	return Entry{
		Key:      key,
		Label:    label,
		Category: "latest_backup",
		Status:   StatusMissing,
		Critical: critical,
		Message:  "latest manifest does not declare this policy",
	}
}

func summarize(entries []Entry) (Summary, []string) {
	var summary Summary
	var criticalMissing []string
	for _, entry := range entries {
		switch entry.Status {
		case StatusCovered:
			summary.Covered++
		case StatusMissing:
			summary.Missing++
		case StatusUnknown:
			summary.Unknown++
		case StatusExcludedExplicitly:
			summary.ExcludedExplicitly++
		case StatusRequiresDecision:
			summary.RequiresDecision++
		}
		if entry.Critical && (entry.Status == StatusMissing || entry.Status == StatusRequiresDecision) {
			summary.CriticalMissing++
			criticalMissing = append(criticalMissing, entry.Key)
		}
	}
	return summary, criticalMissing
}

func overallStatus(summary Summary) string {
	if summary.CriticalMissing > 0 {
		return OverallCritical
	}
	if summary.Missing > 0 || summary.Unknown > 0 || summary.RequiresDecision > 0 {
		return OverallWarning
	}
	return OverallOK
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
