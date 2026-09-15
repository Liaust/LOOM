package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"loom.local/loom/internal/migrations"
	"loom.local/loom/internal/version"
)

type PlannerInput struct {
	Spec UpdateSpec
	Now  func() time.Time
}

func Plan(ctx context.Context, input PlannerInput) (UpdatePlan, error) {
	now := currentTime(input.Now)
	spec, diagnostics, err := normalizeSpec(input.Spec)
	if err != nil {
		return UpdatePlan{}, err
	}

	active := discoverActiveRelease(spec)
	target, targetDiagnostics, err := discoverTargetRelease(spec)
	diagnostics = append(diagnostics, targetDiagnostics...)
	if err != nil {
		return UpdatePlan{}, err
	}
	backupScope, backupScopeDiagnostic := planBackupScope(target.BackupScope)
	diagnostics = append(diagnostics, backupScopeDiagnostic)

	migrationPlan, migrationDiagnostics := planMigrations(ctx, spec, active, target)
	diagnostics = append(diagnostics, migrationDiagnostics...)
	rollback := rollbackPlanForMigrations(migrationPlan)
	steps := updateStepsForPlan(migrationPlan, rollback)
	status := planStatus(diagnostics)

	plan := UpdatePlan{
		SchemaVersion: SchemaVersion,
		Status:        status,
		CreatedAt:     now,
		Spec:          spec,
		Active:        active,
		Target:        target,
		Migrations:    migrationPlan,
		Nix: NixPlan{
			CurrentGeneration: currentNixGenerationPath(),
			TargetFlakeOutput: firstNonEmpty(spec.FlakeOutput, target.FlakeOutput, DefaultProductionFlakeOutput),
			RebuildExpected:   true,
		},
		Backup: BackupRequirement{
			Required:       true,
			Reason:         "Production updates must be protected by a verified main-node backup.",
			MinimumCommand: "loom backup create --production",
		},
		BackupScope: backupScope,
		ServiceImpact: ServiceImpact{
			LoomdRestartExpected:           true,
			PostgresRemainUpExpected:       true,
			SchedulerPauseRecommended:      true,
			DirectEventPauseRecommended:    true,
			OptionalWorkerPauseRecommended: true,
		},
		Rollback:    rollback,
		Steps:       steps,
		Diagnostics: diagnostics,
	}
	plan.PlanHash = HashPlan(plan)
	plan.PlanID = planIDFromHash(plan.PlanHash)
	return plan, nil
}

func normalizeSpec(spec UpdateSpec) (UpdateSpec, []UpdateDiagnostic, error) {
	diagnostics := []UpdateDiagnostic{}
	spec.ReleasePath = strings.TrimSpace(spec.ReleasePath)
	if spec.ReleasePath == "" {
		return UpdateSpec{}, nil, fmt.Errorf("release path is required")
	}
	releasePath, err := filepath.Abs(spec.ReleasePath)
	if err != nil {
		return UpdateSpec{}, nil, err
	}
	spec.ReleasePath = filepath.Clean(releasePath)
	if err := validateExactDirectoryMode(spec.ReleasePath, "release path", releaseRootMode); err != nil {
		return UpdateSpec{}, nil, err
	}
	if strings.TrimSpace(spec.StateDir) == "" {
		spec.StateDir = DefaultStateDir("")
	} else {
		spec.StateDir = filepath.Clean(spec.StateDir)
	}
	if strings.TrimSpace(spec.ManifestPath) != "" {
		spec.ManifestPath = filepath.Clean(spec.ManifestPath)
	}
	if strings.TrimSpace(spec.ActivePath) != "" {
		if abs, err := filepath.Abs(spec.ActivePath); err == nil {
			spec.ActivePath = filepath.Clean(abs)
		}
	} else {
		spec.ActivePath = "/srv/loom/current"
	}
	if strings.TrimSpace(spec.ActiveMigrationsDir) == "" && strings.TrimSpace(spec.ActivePath) != "" {
		spec.ActiveMigrationsDir = filepath.Join(spec.ActivePath, "migrations")
	}
	if strings.TrimSpace(spec.TargetMigrationsDir) != "" && !filepath.IsAbs(spec.TargetMigrationsDir) {
		spec.TargetMigrationsDir = filepath.Join(spec.ReleasePath, spec.TargetMigrationsDir)
	}
	if _, err := os.Stat(spec.ActivePath); err != nil {
		diagnostics = append(diagnostics, UpdateDiagnostic{
			Severity: DiagnosticWarning,
			Code:     "update.active_path_unreadable",
			Message:  "Active source path is not readable; update planning will use install metadata and defaults.",
			Path:     spec.ActivePath,
		})
	}
	return spec, diagnostics, nil
}

func discoverActiveRelease(spec UpdateSpec) ReleaseState {
	state := ReleaseState{
		Path:          spec.ActivePath,
		MigrationsDir: spec.ActiveMigrationsDir,
		Source:        "active_path",
	}
	if strings.TrimSpace(spec.ManifestPath) != "" {
		if manifest, err := os.ReadFile(spec.ManifestPath); err == nil && len(manifest) > 0 {
			state.ManifestPath = spec.ManifestPath
			state.Source = "install_manifest"
		}
	}
	state.Commit = gitCommit(spec.ActivePath)
	if state.Commit == "" {
		state.Commit = version.Current().Commit
	}
	state.Version = version.Current().Version
	state.ReleaseID = releaseID(state.Path, state.Version, state.Commit)
	return state
}

func discoverTargetRelease(spec UpdateSpec) (ReleaseState, []UpdateDiagnostic, error) {
	diagnostics := []UpdateDiagnostic{}
	manifest, manifestPath, err := LoadReleaseManifest(spec.ReleasePath)
	switch {
	case err == nil:
		if err := validateReleaseManifestTarget(spec.ReleasePath, manifest.SourcePath); err != nil {
			return ReleaseState{}, diagnostics, err
		}
		state := ReleaseState{
			ReleaseID:     firstNonEmpty(manifest.ReleaseID, releaseID(spec.ReleasePath, manifest.Version, manifest.Commit)),
			Path:          firstNonEmpty(manifest.SourcePath, spec.ReleasePath),
			Version:       manifest.Version,
			Commit:        manifest.Commit,
			FlakeOutput:   firstNonEmpty(manifest.FlakeOutput, spec.FlakeOutput, DefaultProductionFlakeOutput),
			ManifestPath:  manifestPath,
			MigrationsDir: firstNonEmpty(spec.TargetMigrationsDir, manifest.MigrationsDir, filepath.Join(spec.ReleasePath, "migrations")),
			Source:        "release_manifest",
			BackupScope:   cloneReleaseBackupScope(manifest.BackupScope),
		}
		if state.Commit == "" {
			state.Commit = gitCommit(spec.ReleasePath)
		}
		if state.Version == "" {
			state.Version = version.Current().Version
		}
		return state, diagnostics, nil
	case errors.Is(err, fs.ErrNotExist):
		diagnostics = append(diagnostics, UpdateDiagnostic{
			Severity: DiagnosticInfo,
			Code:     "update.release_manifest_missing",
			Message:  "No loom-release manifest was found; inferred release metadata from the release path.",
			Path:     spec.ReleasePath,
		})
		commit := gitCommit(spec.ReleasePath)
		state := ReleaseState{
			ReleaseID:     releaseID(spec.ReleasePath, version.Current().Version, commit),
			Path:          spec.ReleasePath,
			Version:       version.Current().Version,
			Commit:        commit,
			FlakeOutput:   firstNonEmpty(spec.FlakeOutput, DefaultProductionFlakeOutput),
			MigrationsDir: firstNonEmpty(spec.TargetMigrationsDir, filepath.Join(spec.ReleasePath, "migrations")),
			Source:        "inferred",
		}
		return state, diagnostics, nil
	default:
		return ReleaseState{}, diagnostics, err
	}
}

func validateReleaseManifestTarget(releasePath, manifestTarget string) error {
	releasePath = filepath.Clean(strings.TrimSpace(releasePath))
	manifestTarget = strings.TrimSpace(manifestTarget)
	if manifestTarget == "" || !filepath.IsAbs(manifestTarget) {
		return fmt.Errorf("release manifest target path must be an exact absolute release root: %q", manifestTarget)
	}
	manifestTarget = filepath.Clean(manifestTarget)
	releaseResolved, err := filepath.EvalSymlinks(releasePath)
	if err != nil {
		return fmt.Errorf("release path could not be resolved: %w", err)
	}
	manifestResolved, err := filepath.EvalSymlinks(manifestTarget)
	if err != nil {
		return fmt.Errorf("release manifest target path could not be resolved: %w", err)
	}
	if manifestTarget != releasePath || manifestResolved != releaseResolved {
		return fmt.Errorf("release manifest target path does not match release root: manifest=%s release=%s", manifestTarget, releasePath)
	}
	return nil
}

func planBackupScope(declaration *ReleaseBackupScope) (UpdateBackupScope, UpdateDiagnostic) {
	result := UpdateBackupScope{
		DeclarationStatus: BackupScopeDeclarationAbsent,
		RequiredBackup:    UpdateBackupRequirementComplete,
		Reason:            "The release has no trusted backup-scope declaration; a complete backup is required.",
	}
	diagnostic := UpdateDiagnostic{
		Severity: DiagnosticWarning,
		Code:     "update.backup_scope_absent",
		Message:  result.Reason,
	}
	if declaration == nil {
		return result, diagnostic
	}

	result.DeclaredSchema = strings.TrimSpace(declaration.SchemaVersion)
	result.DeclaredClass = strings.TrimSpace(declaration.Class)
	if result.DeclaredSchema == "" || result.DeclaredClass == "" {
		result.DeclarationStatus = BackupScopeDeclarationMalformed
		result.Reason = "The release backup-scope declaration is malformed; a complete backup is required."
		diagnostic.Code = "update.backup_scope_malformed"
		diagnostic.Message = result.Reason
		return result, diagnostic
	}
	if result.DeclaredSchema != ReleaseBackupScopeSchemaVersion {
		result.DeclarationStatus = BackupScopeDeclarationUnknownSchema
		result.Reason = "The release backup-scope schema is not supported; a complete backup is required."
		diagnostic.Code = "update.backup_scope_unknown_schema"
		diagnostic.Message = result.Reason
		return result, diagnostic
	}

	result.DeclarationStatus = BackupScopeDeclarationValid
	switch result.DeclaredClass {
	case ReleaseBackupScopeOperationalOnly:
		result.RequiredBackup = UpdateBackupRequirementOperational
		result.OperationalPackageAllowed = true
		result.Reason = "The trusted release declares that the update does not mutate canonical user-data roots."
		diagnostic.Severity = DiagnosticInfo
		diagnostic.Code = "update.backup_scope_operational_only"
		diagnostic.Message = result.Reason
	case ReleaseBackupScopeCanonicalUserData:
		result.Reason = "The trusted release declares possible canonical user-data mutation; a complete backup is required."
		diagnostic.Severity = DiagnosticInfo
		diagnostic.Code = "update.backup_scope_canonical_user_data"
		diagnostic.Message = result.Reason
	default:
		result.DeclarationStatus = BackupScopeDeclarationUnknownClass
		result.Reason = "The release backup-scope class is not supported; a complete backup is required."
		diagnostic.Code = "update.backup_scope_unknown_class"
		diagnostic.Message = result.Reason
	}
	return result, diagnostic
}

func cloneReleaseBackupScope(scope *ReleaseBackupScope) *ReleaseBackupScope {
	if scope == nil {
		return nil
	}
	cloned := *scope
	return &cloned
}

func planMigrations(ctx context.Context, spec UpdateSpec, active ReleaseState, target ReleaseState) (MigrationPlan, []UpdateDiagnostic) {
	diagnostics := []UpdateDiagnostic{}
	current := int64(0)
	activeLatest := int64(0)
	targetLatest := int64(0)
	status := "ok"

	if spec.CurrentMigrationVersion != nil {
		current = *spec.CurrentMigrationVersion
	} else if strings.TrimSpace(spec.DBURL) != "" {
		result := migrations.Status(ctx, spec.DBURL, firstNonEmpty(active.MigrationsDir, target.MigrationsDir))
		current = result.CurrentVersion
		activeLatest = result.LatestVersion
		if result.Status == "unhealthy" || result.Error != "" {
			status = "degraded"
			diagnostics = append(diagnostics, UpdateDiagnostic{
				Severity: DiagnosticWarning,
				Code:     "update.current_migration_unreadable",
				Message:  firstNonEmpty(result.Error, "Current migration status could not be read."),
				Path:     result.Directory,
			})
		}
	} else {
		status = "degraded"
		diagnostics = append(diagnostics, UpdateDiagnostic{
			Severity: DiagnosticWarning,
			Code:     "update.db_url_missing",
			Message:  "LOOM_DB_URL is not configured; current database migration version is unknown.",
		})
	}

	if activeLatest == 0 && strings.TrimSpace(active.MigrationsDir) != "" {
		if latest, err := migrations.LatestVersion(active.MigrationsDir); err == nil {
			activeLatest = latest
		} else {
			status = "degraded"
			diagnostics = append(diagnostics, UpdateDiagnostic{
				Severity: DiagnosticWarning,
				Code:     "update.active_migrations_unreadable",
				Message:  "Active migration directory could not be read.",
				Path:     active.MigrationsDir,
			})
		}
	}
	if strings.TrimSpace(target.MigrationsDir) != "" {
		if latest, err := migrations.LatestVersion(target.MigrationsDir); err == nil {
			targetLatest = latest
		} else {
			status = "blocked"
			diagnostics = append(diagnostics, UpdateDiagnostic{
				Severity: DiagnosticBlocking,
				Code:     "update.target_migrations_unreadable",
				Message:  "Target migration directory could not be read.",
				Path:     target.MigrationsDir,
			})
		}
	}

	pending := targetLatest - current
	if pending < 0 {
		status = "blocked"
		diagnostics = append(diagnostics, UpdateDiagnostic{
			Severity: DiagnosticBlocking,
			Code:     "update.database_ahead_of_target",
			Message:  "Database migration version is ahead of the target release.",
		})
		pending = 0
	}
	return MigrationPlan{
		Status:              status,
		CurrentVersion:      current,
		ActiveLatestVersion: activeLatest,
		TargetLatestVersion: targetLatest,
		Pending:             pending,
		WillApply:           pending > 0,
	}, diagnostics
}

func rollbackPlanForMigrations(migrations MigrationPlan) RollbackPlan {
	if migrations.Status == "blocked" {
		return RollbackPlan{
			Class:           RollbackClassManualOnly,
			Reason:          "Update plan is blocked; rollback cannot be classified safely.",
			RestoreRequired: false,
		}
	}
	if migrations.WillApply {
		return RollbackPlan{
			Class:               RollbackClassRestoreRequired,
			Reason:              "Target release includes migrations ahead of the current database; database restore is required for true rollback.",
			ServiceOnlyPossible: false,
			RestoreRequired:     true,
		}
	}
	return RollbackPlan{
		Class:               RollbackClassServiceOnly,
		Reason:              "No target migrations are pending; source/service rollback should be possible.",
		ServiceOnlyPossible: true,
		RestoreRequired:     false,
	}
}

func updateStepsForPlan(migrations MigrationPlan, rollback RollbackPlan) []UpdateStep {
	return []UpdateStep{
		{ID: "preflight", Title: "Run production preflight checks", Status: StepStatusReady, Mutating: false},
		{ID: "backup", Title: "Create and verify production backup", Status: StepStatusPending, Mutating: true},
		{ID: "maintenance_window", Title: "Pause schedules, direct events, and optional workers", Status: StepStatusPending, Mutating: true},
		{ID: "switch_release", Title: "Switch active LOOM release", Status: StepStatusPending, Mutating: true},
		{ID: "nixos_rebuild", Title: "Run NixOS rebuild for target release", Status: StepStatusPending, Mutating: true},
		{ID: "migrations", Title: fmt.Sprintf("Apply %d pending migration(s)", migrations.Pending), Status: stepStatusForMigration(migrations), Mutating: migrations.WillApply},
		{ID: "health_check", Title: "Verify loomd health after update", Status: StepStatusPending, Mutating: false},
		{ID: "rollback_model", Title: "Record rollback model: " + rollback.Class, Status: StepStatusReady, Mutating: false},
	}
}

func stepStatusForMigration(migrationPlan MigrationPlan) string {
	if migrationPlan.Status == "blocked" {
		return StepStatusBlocked
	}
	if migrationPlan.WillApply {
		return StepStatusPending
	}
	return StepStatusReady
}

func planStatus(diagnostics []UpdateDiagnostic) string {
	status := PlanStatusReady
	for _, diagnostic := range diagnostics {
		switch diagnostic.Severity {
		case DiagnosticBlocking:
			return PlanStatusBlocked
		case DiagnosticError, DiagnosticWarning:
			status = PlanStatusDegraded
		}
	}
	return status
}

// updatePlanIdentity is the canonical reviewed identity of an update plan.
// Receipt metadata is deliberately absent; every semantic UpdatePlan section
// must remain represented here so reviewed plan identity changes with behavior.
type updatePlanIdentity struct {
	SchemaVersion string             `json:"schema_version"`
	Status        string             `json:"status"`
	Spec          UpdateSpec         `json:"spec"`
	Active        ReleaseState       `json:"active"`
	Target        ReleaseState       `json:"target"`
	Migrations    MigrationPlan      `json:"migrations"`
	Nix           NixPlan            `json:"nix"`
	Backup        BackupRequirement  `json:"backup"`
	BackupScope   UpdateBackupScope  `json:"backup_scope"`
	ServiceImpact ServiceImpact      `json:"service_impact"`
	Rollback      RollbackPlan       `json:"rollback"`
	Steps         []UpdateStep       `json:"steps"`
	Diagnostics   []UpdateDiagnostic `json:"diagnostics,omitempty"`
}

func HashPlan(plan UpdatePlan) string {
	identity := updatePlanIdentity{
		SchemaVersion: plan.SchemaVersion,
		Status:        plan.Status,
		Spec:          plan.Spec,
		Active:        plan.Active,
		Target:        plan.Target,
		Migrations:    plan.Migrations,
		Nix:           plan.Nix,
		Backup:        plan.Backup,
		BackupScope:   plan.BackupScope,
		ServiceImpact: plan.ServiceImpact,
		Rollback:      plan.Rollback,
		Steps:         plan.Steps,
		Diagnostics:   plan.Diagnostics,
	}
	raw, _ := json.Marshal(identity)
	hash := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(hash[:])
}

func planIDFromHash(hash string) string {
	hash = strings.TrimPrefix(hash, "sha256:")
	if len(hash) < 16 {
		return "update_plan_pending"
	}
	return "update_plan_" + hash[:16]
}

func releaseID(path, releaseVersion, commit string) string {
	seed := strings.Join([]string{filepath.Clean(path), releaseVersion, commit}, "|")
	hash := sha256.Sum256([]byte(seed))
	return "release_" + hex.EncodeToString(hash[:])[:16]
}

func gitCommit(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "-C", path, "rev-parse", "HEAD")
	output, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(output))
}

func currentNixGenerationPath() string {
	value, err := os.Readlink("/run/current-system")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(value)
}

func currentTime(now func() time.Time) time.Time {
	if now == nil {
		return time.Now().UTC()
	}
	value := now()
	if value.IsZero() {
		return time.Now().UTC()
	}
	return value.UTC()
}

func ListHistory(stateDir string, limit int) ([]UpdateManifest, []UpdateDiagnostic) {
	stateDir = filepath.Clean(strings.TrimSpace(stateDir))
	if limit <= 0 {
		limit = 20
	}
	historyDir := HistoryDir(stateDir)
	entries, err := os.ReadDir(historyDir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return []UpdateManifest{}, nil
		}
		return []UpdateManifest{}, []UpdateDiagnostic{{
			Severity: DiagnosticWarning,
			Code:     "update.history_unreadable",
			Message:  "Update history directory could not be read.",
			Path:     historyDir,
		}}
	}
	var manifests []UpdateManifest
	var diagnostics []UpdateDiagnostic
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasSuffix(name, ".yaml") && !strings.HasSuffix(name, ".json") {
			continue
		}
		manifest, err := ReadUpdateManifest(filepath.Join(historyDir, name))
		if err != nil {
			diagnostics = append(diagnostics, UpdateDiagnostic{
				Severity: DiagnosticWarning,
				Code:     "update.history_manifest_invalid",
				Message:  err.Error(),
				Path:     filepath.Join(historyDir, name),
			})
			continue
		}
		manifests = append(manifests, manifest)
	}
	sort.Slice(manifests, func(i, j int) bool {
		return manifests[i].StartedAt.After(manifests[j].StartedAt)
	})
	if len(manifests) > limit {
		manifests = manifests[:limit]
	}
	return manifests, diagnostics
}
