package setup

import (
	"bytes"
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"loom.local/loom/internal/bootstrap"
	"loom.local/loom/internal/box"
	"loom.local/loom/internal/config"
	"loom.local/loom/internal/db"
	"loom.local/loom/internal/migrations"
	"loom.local/loom/internal/nodeprofiles"
)

func Apply(input ApplyInput) (ApplyResult, error) {
	plan, err := resolveApplyPlan(input)
	if err != nil {
		return ApplyResult{}, err
	}
	result := ApplyResult{
		DryRun:   input.DryRun,
		PlanID:   plan.PlanID,
		PlanHash: plan.PlanHash,
		Resume:   input.Resume,
	}
	if plan.Spec.NodeKind != "main" {
		return applyNonMainPlan(plan, input, result)
	}
	if blocked := validateMainApplyPlan(plan); len(blocked) > 0 {
		result.Blocked = blocked
		result.Manifest = ManifestFromPlan(plan)
		return result, nil
	}
	if !input.DryRun && !input.Yes {
		result.Refused = true
		if input.NoInteractive {
			result.Refusal = "setup apply is non-interactive; pass --yes to configure the local main node"
		} else {
			result.Refusal = "setup apply requires --yes before mutating local setup state"
		}
		result.Manifest = ManifestFromPlan(plan)
		return result, nil
	}

	manifest := ManifestFromPlan(plan)
	serviceEnvPath := filepath.Join(plan.Paths.ConfigDir, "loom.env")
	result.ServiceEnv = serviceEnvPath
	if input.DryRun {
		result.Manifest = manifest
	}

	pathChanges, err := applyMainPaths(plan, input.DryRun)
	result.Changed = append(result.Changed, pathChanges.changed...)
	result.Skipped = append(result.Skipped, pathChanges.skipped...)
	result.Blocked = append(result.Blocked, pathChanges.blocked...)
	if err != nil {
		return result, err
	}
	if len(result.Blocked) > 0 {
		result.Manifest = manifest
		return result, nil
	}

	envBody := renderServiceEnv(plan, serviceEnvPath)
	envChange, err := writeFileChange("write_loomd_env", "config", serviceEnvPath, []byte(envBody), 0o600, input.DryRun)
	if err != nil {
		return result, err
	}
	appendApplyChange(&result, envChange)
	appendApplyChange(&result, validateServicePermissionPlan(plan))

	migrationChange, err := runApplyMigrations(context.Background(), plan, input.DryRun)
	result.Migrations = migrationChange
	appendApplyChange(&result, migrationChange)
	if err != nil {
		return result, err
	}

	bootstrapChange, bootstrapManifest, err := runApplyProductionBootstrap(context.Background(), plan, manifest, input.DryRun)
	result.Bootstrap = bootstrapChange
	manifest.ProductionBootstrap = bootstrapManifest
	appendApplyChange(&result, bootstrapChange)
	if err != nil {
		return result, err
	}

	manifest.LastStatus = applyManifestStatus(manifest)
	manifestChange, err := writeManifestChange(plan.Paths.ManifestPath, manifest, input.DryRun)
	if err != nil {
		return result, err
	}
	appendApplyChange(&result, manifestChange)
	result.Manifest = manifest

	statusInput := StatusInput{
		ManifestPath: plan.Paths.ManifestPath,
		Spec:         plan.Spec,
		Facts:        input.Facts,
		Now:          input.Now,
		CollectFacts: input.CollectFacts,
	}
	status, statusErr := Status(statusInput)
	if statusErr == nil {
		result.Status = status
		doctor, doctorErr := Doctor(DoctorInput{StatusInput: statusInput})
		if doctorErr == nil {
			result.Doctor = doctor
		}
	}
	return result, statusErr
}

type applyChanges struct {
	changed []ApplyChange
	skipped []ApplyChange
	blocked []ApplyChange
}

func resolveApplyPlan(input ApplyInput) (SetupPlan, error) {
	var plan SetupPlan
	var err error
	switch {
	case strings.TrimSpace(input.PlanPath) != "":
		plan, err = LoadPlanFile(input.PlanPath)
	case input.Plan != nil:
		plan = *input.Plan
	default:
		plan, err = Plan(PlannerInput{
			Spec:         input.Spec,
			Facts:        input.Facts,
			Now:          input.Now,
			CollectFacts: input.CollectFacts,
			ManifestPath: input.ManifestPath,
		})
	}
	if err != nil {
		return SetupPlan{}, err
	}
	if strings.TrimSpace(input.ManifestPath) != "" {
		manifestPath, _, err := ResolveManifestPath(ManifestPathInput{ExplicitPath: input.ManifestPath, Spec: plan.Spec})
		if err != nil {
			return SetupPlan{}, err
		}
		plan.Paths.ManifestPath = manifestPath
	}
	if strings.TrimSpace(plan.SchemaVersion) == "" {
		plan.SchemaVersion = SchemaVersion
	}
	if plan.SchemaVersion != SchemaVersion {
		return SetupPlan{}, fmt.Errorf("unsupported setup plan schema_version %q", plan.SchemaVersion)
	}
	computed := HashPlan(plan)
	if plan.PlanHash != "" && plan.PlanHash != computed {
		return SetupPlan{}, fmt.Errorf("setup plan hash mismatch: got %s, computed %s", plan.PlanHash, computed)
	}
	plan.PlanHash = computed
	hash := strings.TrimPrefix(plan.PlanHash, "sha256:")
	if plan.PlanID == "" && len(hash) >= 16 {
		plan.PlanID = "setup_plan_" + hash[:16]
	}
	if plan.CreatedAt.IsZero() {
		now := time.Now
		if input.Now != nil {
			now = input.Now
		}
		plan.CreatedAt = now().UTC()
	}
	return plan, nil
}

func validateMainApplyPlan(plan SetupPlan) []ApplyChange {
	blocked := []ApplyChange{}
	if plan.Spec.NodeKind != "main" {
		blocked = append(blocked, applyBlocked("validate_main_node", "preflight", fmt.Sprintf("setup apply currently supports local main nodes only, got node_kind %q", plan.Spec.NodeKind)))
	}
	if plan.Spec.NodeRole != "main" {
		blocked = append(blocked, applyBlocked("validate_main_role", "preflight", fmt.Sprintf("setup apply currently supports node_role main only, got %q", plan.Spec.NodeRole)))
	}
	if plan.Spec.RuntimeClass != nodeprofiles.RuntimeMainFull {
		blocked = append(blocked, applyBlocked("validate_main_runtime", "preflight", fmt.Sprintf("setup apply currently supports runtime_class %s only, got %q", nodeprofiles.RuntimeMainFull, plan.Spec.RuntimeClass)))
	}
	if !plan.Spec.EnableLoomd {
		blocked = append(blocked, applyBlocked("validate_loomd_enabled", "preflight", "main setup requires enable_loomd=true"))
	}
	if plan.Spec.BoxProfile != box.ProfileMain {
		blocked = append(blocked, applyBlocked("validate_box_profile", "box", fmt.Sprintf("main setup requires box_profile %q", box.ProfileMain)))
	}
	if strings.TrimSpace(plan.Paths.BoxPath) == "" {
		blocked = append(blocked, applyBlocked("validate_box_path", "box", "main setup requires a Box path"))
	}
	if strings.TrimSpace(plan.Paths.ConfigDir) == "" || strings.TrimSpace(plan.Paths.DataDir) == "" || strings.TrimSpace(plan.Paths.ObjectStorePath) == "" || strings.TrimSpace(plan.Paths.ServiceRoot) == "" || strings.TrimSpace(plan.Paths.StorageRoot) == "" || strings.TrimSpace(plan.Paths.BoxStateRoot) == "" {
		blocked = append(blocked, applyBlocked("validate_paths", "paths", "main setup requires config, data, object-store, service, storage, and Box state paths"))
	}
	for _, diagnostic := range plan.Diagnostics {
		if diagnostic.Severity == DiagnosticError || diagnostic.Severity == DiagnosticBlocking {
			blocked = append(blocked, ApplyChange{
				ID:       "diagnostic_" + diagnostic.Code,
				Category: "preflight",
				Status:   ApplyStatusBlocked,
				Path:     diagnostic.Path,
				Message:  diagnostic.Message,
			})
		}
	}
	return blocked
}

func applyMainPaths(plan SetupPlan, dryRun bool) (applyChanges, error) {
	out := applyChanges{}
	runtimeState, err := resolvePlanBoxRuntimeState(plan)
	if err != nil {
		out.blocked = append(out.blocked, ApplyChange{ID: "resolve_box_runtime_state", Category: "box", Status: ApplyStatusBlocked, Path: plan.Paths.BoxPath, Message: err.Error()})
		return out, nil
	}
	for _, dir := range mainSetupDirs(plan) {
		if dir.id == "ensure_box_state_root" && runtimeState.Source == "legacy" {
			out.skipped = append(out.skipped, ApplyChange{ID: dir.id, Category: dir.category, Status: ApplyStatusSkipped, Path: dir.path, Message: "legacy Box runtime state remains the effective read/write root until explicit cutover"})
			continue
		}
		change, err := ensureDirChange(dir.id, dir.category, dir.path, dir.mode, dryRun)
		if err != nil {
			return out, err
		}
		appendToApplyChanges(&out, change)
	}
	if plan.Spec.EnableBox {
		boxResult, err := box.Init(box.InitInput{
			Resolved: box.Resolved{
				RootPath:         plan.Paths.BoxPath,
				Profile:          box.ProfileMain,
				OwnerNode:        plan.Spec.NodeKey,
				NodeRole:         plan.Spec.NodeRole,
				RuntimeStateRoot: plan.Paths.BoxStateRoot,
				LegacyStateRoot:  plan.Paths.LegacyBoxStateRoot,
			},
			DryRun: dryRun,
		})
		if err != nil {
			out.blocked = append(out.blocked, ApplyChange{
				ID:       "init_box",
				Category: "box",
				Status:   ApplyStatusBlocked,
				Path:     plan.Paths.BoxPath,
				Message:  err.Error(),
			})
			return out, nil
		}
		for _, path := range boxResult.CreatedDirs {
			out.changed = append(out.changed, ApplyChange{ID: "init_box_dir", Category: "box", Status: ApplyStatusChanged, Path: path})
		}
		for _, path := range boxResult.CreatedFiles {
			out.changed = append(out.changed, ApplyChange{ID: "init_box_file", Category: "box", Status: ApplyStatusChanged, Path: path})
		}
		for _, path := range boxResult.PlannedDirs {
			out.changed = append(out.changed, ApplyChange{ID: "init_box_dir", Category: "box", Status: ApplyStatusWouldChange, Path: path})
		}
		for _, path := range boxResult.PlannedFiles {
			out.changed = append(out.changed, ApplyChange{ID: "init_box_file", Category: "box", Status: ApplyStatusWouldChange, Path: path})
		}
		if len(boxResult.CreatedDirs)+len(boxResult.CreatedFiles)+len(boxResult.PlannedDirs)+len(boxResult.PlannedFiles) == 0 {
			out.skipped = append(out.skipped, ApplyChange{ID: "init_box", Category: "box", Status: ApplyStatusAlreadySatisfied, Path: plan.Paths.BoxPath})
		}
	}
	humanLinkChanges, err := applyHumanLinks(plan, dryRun)
	out.changed = append(out.changed, humanLinkChanges.changed...)
	out.skipped = append(out.skipped, humanLinkChanges.skipped...)
	for _, change := range humanLinkChanges.blocked {
		change.Status = ApplyStatusSkipped
		change.Message = "human-facing link needs manual repair: " + change.Message
		out.skipped = append(out.skipped, change)
	}
	if err != nil {
		return out, err
	}
	return out, nil
}

func resolvePlanBoxRuntimeState(plan SetupPlan) (box.RuntimeStateResolution, error) {
	if !plan.Spec.EnableBox || strings.TrimSpace(plan.Paths.BoxPath) == "" {
		return box.RuntimeStateResolution{ReadRoot: plan.Paths.BoxStateRoot, WriteRoot: plan.Paths.BoxStateRoot, Source: "canonical_empty"}, nil
	}
	return box.ResolveRuntimeState(box.Resolved{
		RootPath:         plan.Paths.BoxPath,
		Profile:          plan.Spec.BoxProfile,
		RuntimeStateRoot: plan.Paths.BoxStateRoot,
		LegacyStateRoot:  plan.Paths.LegacyBoxStateRoot,
	})
}

type applyDir struct {
	id       string
	category string
	path     string
	mode     os.FileMode
}

func mainSetupDirs(plan SetupPlan) []applyDir {
	dirs := []applyDir{
		{id: "ensure_config_dir", category: "paths", path: plan.Paths.ConfigDir, mode: 0o750},
		{id: "ensure_data_dir", category: "paths", path: plan.Paths.DataDir, mode: 0o750},
		{id: "ensure_state_dir", category: "paths", path: plan.Paths.StateDir, mode: 0o750},
		{id: "ensure_log_dir", category: "paths", path: plan.Paths.LogDir, mode: 0o750},
		{id: "ensure_object_store", category: "database", path: plan.Paths.ObjectStorePath, mode: 0o750},
		{id: "ensure_object_store_blobs", category: "database", path: filepath.Join(plan.Paths.ObjectStorePath, "blobs"), mode: 0o750},
		{id: "ensure_object_store_temp", category: "database", path: filepath.Join(plan.Paths.ObjectStorePath, "temp"), mode: 0o700},
		{id: "ensure_service_root", category: "paths", path: plan.Paths.ServiceRoot, mode: 0o755},
		{id: "ensure_storage_root", category: "storage", path: plan.Paths.StorageRoot, mode: 0o2750},
		{id: "ensure_imports_root", category: "storage", path: plan.Paths.ImportsRoot, mode: 0o2770},
		{id: "ensure_user_backups_root", category: "storage", path: plan.Paths.UserBackupsRoot, mode: 0o2770},
		{id: "ensure_archive_root", category: "storage", path: plan.Paths.ArchiveRoot, mode: 0o2750},
		{id: "ensure_generated_root", category: "paths", path: plan.Paths.GeneratedRoot, mode: 0o750},
		{id: "ensure_box_state_root", category: "box", path: plan.Paths.BoxStateRoot, mode: 0o750},
		{id: "ensure_data_indexes", category: "database", path: filepath.Join(plan.Paths.DataDir, "indexes"), mode: 0o750},
		{id: "ensure_data_packages", category: "paths", path: filepath.Join(plan.Paths.DataDir, "packages"), mode: 0o750},
		{id: "ensure_data_temp", category: "paths", path: filepath.Join(plan.Paths.DataDir, "temp"), mode: 0o700},
	}
	if plan.Paths.SocketPath != "" {
		dirs = append(dirs, applyDir{id: "ensure_socket_parent", category: "paths", path: filepath.Dir(plan.Paths.SocketPath), mode: 0o750})
	}
	if plan.Spec.EnableCloud {
		dirs = append(dirs,
			applyDir{id: "ensure_cloud_config_dir", category: "cloud", path: plan.Paths.CloudConfigDir, mode: 0o750},
			applyDir{id: "ensure_cloud_state_dir", category: "cloud", path: plan.Paths.CloudStateDir, mode: 0o750},
			applyDir{id: "ensure_cloud_state_borg", category: "cloud", path: filepath.Join(plan.Paths.CloudStateDir, "borg"), mode: 0o750},
			applyDir{id: "ensure_cloud_borg_cache_dir", category: "cloud", path: plan.Paths.CloudBorgCacheDir, mode: 0o700},
			applyDir{id: "ensure_cloud_borg_security_dir", category: "cloud", path: plan.Paths.CloudBorgSecurityDir, mode: 0o700},
			applyDir{id: "ensure_cloud_state_snapshots", category: "cloud", path: filepath.Join(plan.Paths.CloudStateDir, "snapshots"), mode: 0o750},
			applyDir{id: "ensure_cloud_state_offload", category: "cloud", path: filepath.Join(plan.Paths.CloudStateDir, "offload"), mode: 0o750},
			applyDir{id: "ensure_cloud_state_manifests", category: "cloud", path: filepath.Join(plan.Paths.CloudStateDir, "manifests"), mode: 0o750},
			applyDir{id: "ensure_cloud_state_restore_drills", category: "cloud", path: filepath.Join(plan.Paths.CloudStateDir, "restore-drills"), mode: 0o750},
			applyDir{id: "ensure_cloud_state_temp", category: "cloud", path: filepath.Join(plan.Paths.CloudStateDir, "temp"), mode: 0o700},
		)
	}
	sort.SliceStable(dirs, func(i, j int) bool {
		return dirs[i].path < dirs[j].path
	})
	return dirs
}

func ensureDirChange(id, category, path string, mode os.FileMode, dryRun bool) (ApplyChange, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return ApplyChange{ID: id, Category: category, Status: ApplyStatusSkipped, Message: "path is not configured"}, nil
	}
	info, err := os.Stat(path)
	if err == nil {
		if !info.IsDir() {
			return ApplyChange{ID: id, Category: category, Status: ApplyStatusBlocked, Path: path, Message: "path exists but is not a directory"}, nil
		}
		if dirModeNeedsUpdate(info.Mode(), mode) {
			if dryRun {
				return ApplyChange{ID: id, Category: category, Status: ApplyStatusWouldChange, Path: path, Message: "directory mode differs from desired setup mode"}, nil
			}
			if err := chmodSetupPath(path, mode); err != nil {
				return ApplyChange{}, fmt.Errorf("chmod %s: %w", path, err)
			}
			return ApplyChange{ID: id, Category: category, Status: ApplyStatusChanged, Path: path, Message: "directory mode updated"}, nil
		}
		return ApplyChange{ID: id, Category: category, Status: ApplyStatusAlreadySatisfied, Path: path}, nil
	}
	if !os.IsNotExist(err) {
		return ApplyChange{}, fmt.Errorf("stat %s: %w", path, err)
	}
	if dryRun {
		return ApplyChange{ID: id, Category: category, Status: ApplyStatusWouldChange, Path: path}, nil
	}
	if err := os.MkdirAll(path, mode); err != nil {
		return ApplyChange{}, fmt.Errorf("create %s: %w", path, err)
	}
	if err := chmodSetupPath(path, mode); err != nil {
		return ApplyChange{}, fmt.Errorf("chmod %s: %w", path, err)
	}
	return ApplyChange{ID: id, Category: category, Status: ApplyStatusChanged, Path: path}, nil
}

func dirModeNeedsUpdate(actual, desired os.FileMode) bool {
	if actual.Perm() != desired.Perm() {
		return true
	}
	return desired&0o2000 != 0 && actual&os.ModeSetgid == 0
}

func chmodSetupPath(path string, mode os.FileMode) error {
	return syscall.Chmod(path, uint32(mode.Perm()|mode&0o7000))
}

func writeFileChange(id, category, path string, payload []byte, mode os.FileMode, dryRun bool) (ApplyChange, error) {
	existing, err := os.ReadFile(path)
	if err == nil && bytes.Equal(existing, payload) {
		return ApplyChange{ID: id, Category: category, Status: ApplyStatusAlreadySatisfied, Path: path}, nil
	}
	if err != nil && !os.IsNotExist(err) {
		return ApplyChange{}, fmt.Errorf("read %s: %w", path, err)
	}
	if dryRun {
		return ApplyChange{ID: id, Category: category, Status: ApplyStatusWouldChange, Path: path}, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return ApplyChange{}, fmt.Errorf("create parent for %s: %w", path, err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".loom-env-*.tmp")
	if err != nil {
		return ApplyChange{}, err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.Write(payload); err != nil {
		_ = tmp.Close()
		return ApplyChange{}, err
	}
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return ApplyChange{}, err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return ApplyChange{}, err
	}
	if err := tmp.Close(); err != nil {
		return ApplyChange{}, err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return ApplyChange{}, err
	}
	status := ApplyStatusChanged
	message := "wrote file"
	if os.IsNotExist(err) {
		message = "created file"
	}
	return ApplyChange{ID: id, Category: category, Status: status, Path: path, Message: message}, nil
}

func renderServiceEnv(plan SetupPlan, serviceEnvPath string) string {
	boxStateRoot := plan.Paths.BoxStateRoot
	if runtimeState, err := resolvePlanBoxRuntimeState(plan); err == nil && strings.TrimSpace(runtimeState.WriteRoot) != "" {
		boxStateRoot = runtimeState.WriteRoot
	}
	values := []struct {
		key   string
		value string
	}{
		{"LOOM_ENV", "production"},
		{"LOOM_NODE_ID", plan.Spec.NodeKey},
		{"LOOM_NODE_KIND", plan.Spec.NodeKind},
		{"LOOM_NODE_ROLE", plan.Spec.NodeRole},
		{"LOOM_RUNTIME_CLASS", plan.Spec.RuntimeClass},
		{"LOOM_DATA_DIR", plan.Paths.DataDir},
		{"LOOM_OBJECT_STORE", plan.Paths.ObjectStorePath},
		{"LOOM_SERVICE_ROOT", plan.Paths.ServiceRoot},
		{"LOOM_STORAGE_ROOT", plan.Paths.StorageRoot},
		{"LOOM_IMPORTS_ROOT", plan.Paths.ImportsRoot},
		{"LOOM_USER_BACKUPS_ROOT", plan.Paths.UserBackupsRoot},
		{"LOOM_ARCHIVE_ROOT", plan.Paths.ArchiveRoot},
		{"LOOM_GENERATED_ROOT", plan.Paths.GeneratedRoot},
		{"LOOM_BOX_STATE_ROOT", boxStateRoot},
		{"LOOM_MAIN_DOCUMENTS_ROOT", plan.Paths.MainDocumentsPath},
		{"LOOM_BOX_PATH", plan.Paths.BoxPath},
		{"LOOM_BOX_PROFILE", plan.Spec.BoxProfile},
		{"LOOM_DB_URL", serviceEnvDBURL(plan.Spec.DBURL)},
		{"LOOM_SOCKET_PATH", plan.Paths.SocketPath},
		{"LOOM_HTTP_LISTEN_ADDR", plan.Spec.HTTPListenAddr},
		{"LOOM_LOG_LEVEL", config.DefaultLogLevel},
		{"LOOM_MIGRATIONS_DIR", firstNonEmpty(plan.Spec.MigrationsDir, config.DefaultMigrationsDir)},
		{"LOOM_AUTO_MIGRATE", fmt.Sprintf("%t", plan.Spec.AutoMigrate)},
		{"LOOM_BOOTSTRAP_DEV", "false"},
		{"LOOM_BOOTSTRAP_MODE", firstNonEmpty(plan.Spec.BootstrapMode, "production")},
		{"LOOM_CONFIG_FILE", serviceEnvPath},
	}
	if plan.Spec.EnableCloud {
		values = append(values,
			struct {
				key   string
				value string
			}{"LOOM_CLOUD_CONFIG_PATH", plan.Paths.CloudConfigPath},
			struct {
				key   string
				value string
			}{"LOOM_CLOUD_STATE_DIR", plan.Paths.CloudStateDir},
		)
	}
	var builder strings.Builder
	for _, pair := range values {
		builder.WriteString(pair.key)
		builder.WriteString("=")
		builder.WriteString(pair.value)
		builder.WriteString("\n")
	}
	return builder.String()
}

func serviceEnvDBURL(dbURL string) string {
	dbURL = strings.TrimSpace(dbURL)
	lower := strings.ToLower(dbURL)
	if strings.Contains(lower, "password=") || strings.Contains(lower, "apikey=") || strings.Contains(lower, "api_key=") {
		return ""
	}
	if parsed, err := url.Parse(dbURL); err == nil && parsed.User != nil {
		if _, hasPassword := parsed.User.Password(); hasPassword {
			return ""
		}
	}
	return dbURL
}

func validateServicePermissionPlan(plan SetupPlan) ApplyChange {
	if plan.Spec.InstallMode != InstallModeService || plan.Spec.ServiceManager == ServiceManagerNone {
		return ApplyChange{ID: "validate_service_permissions", Category: "service", Status: ApplyStatusSkipped, Message: "service manager is not configured"}
	}
	for _, path := range plan.Paths.ServiceReadWritePaths {
		if filepath.Clean(path) == filepath.Clean(plan.Paths.BoxPath) {
			return ApplyChange{ID: "validate_service_permissions", Category: "service", Status: ApplyStatusBlocked, Path: path, Message: "service must not receive write access to the whole Box"}
		}
	}
	return ApplyChange{
		ID:       "validate_service_permissions",
		Category: "service",
		Status:   ApplyStatusAlreadySatisfied,
		Message:  "service write plan is scoped to runtime paths",
		Metadata: map[string]any{"read_write_paths": append([]string{}, plan.Paths.ServiceReadWritePaths...)},
	}
}

func runApplyMigrations(ctx context.Context, plan SetupPlan, dryRun bool) (ApplyChange, error) {
	if strings.TrimSpace(plan.Spec.DBURL) == "" {
		return ApplyChange{ID: "run_migrations", Category: "database", Status: ApplyStatusSkipped, Message: "db_url is not configured"}, nil
	}
	if !plan.Spec.AutoMigrate {
		return ApplyChange{ID: "run_migrations", Category: "database", Status: ApplyStatusSkipped, Message: "auto_migrate is disabled"}, nil
	}
	dir := firstNonEmpty(plan.Spec.MigrationsDir, config.DefaultMigrationsDir)
	if dryRun {
		return ApplyChange{ID: "run_migrations", Category: "database", Status: ApplyStatusWouldChange, Path: dir}, nil
	}
	result, err := migrations.Up(ctx, plan.Spec.DBURL, dir)
	change := ApplyChange{
		ID:       "run_migrations",
		Category: "database",
		Status:   ApplyStatusChanged,
		Path:     dir,
		Message:  result.Status,
		Metadata: map[string]any{
			"detail":          result.Detail,
			"current_version": result.CurrentVersion,
			"latest_version":  result.LatestVersion,
			"pending":         result.Pending,
		},
	}
	if err != nil {
		change.Status = ApplyStatusBlocked
		change.Message = err.Error()
		return change, err
	}
	return change, nil
}

func runApplyProductionBootstrap(ctx context.Context, plan SetupPlan, manifest InstallManifest, dryRun bool) (ApplyChange, ProductionBootstrapManifest, error) {
	if !plan.Spec.ProductionBootstrap {
		return ApplyChange{ID: "production_bootstrap", Category: "bootstrap", Status: ApplyStatusSkipped, Message: "production bootstrap is disabled"}, ProductionBootstrapManifest{}, nil
	}
	if strings.TrimSpace(plan.Spec.DBURL) == "" {
		return ApplyChange{ID: "production_bootstrap", Category: "bootstrap", Status: ApplyStatusSkipped, Message: "db_url is not configured"}, ProductionBootstrapManifest{Checked: false, Ready: false}, nil
	}
	if dryRun {
		return ApplyChange{ID: "production_bootstrap", Category: "bootstrap", Status: ApplyStatusWouldChange}, ProductionBootstrapManifest{Checked: true, Ready: false}, nil
	}
	sqlDB, err := db.OpenSQL(ctx, plan.Spec.DBURL)
	if err != nil {
		change := ApplyChange{ID: "production_bootstrap", Category: "bootstrap", Status: ApplyStatusBlocked, Message: err.Error()}
		return change, ProductionBootstrapManifest{Checked: true, Ready: false, Warnings: []string{"database unavailable"}}, err
	}
	defer sqlDB.Close()

	summary, err := bootstrap.NewService(sqlDB).EnsureProductionBootstrap(ctx, productionInputFromPlan(plan, manifest))
	change := ApplyChange{
		ID:       "production_bootstrap",
		Category: "bootstrap",
		Status:   ApplyStatusChanged,
		Message:  "production bootstrap ready",
		Metadata: map[string]any{
			"ready":       summary.Ready,
			"created":     summary.Created,
			"event_id":    summary.BootstrapEventID,
			"event_count": summary.BootstrapEventCount,
			"missing":     summary.Missing,
			"warnings":    summary.Warnings,
		},
	}
	bootstrapManifest := ProductionBootstrapManifest{
		Checked:    true,
		Ready:      summary.Ready,
		EventID:    summary.BootstrapEventID,
		EventCount: summary.BootstrapEventCount,
		Warnings:   append([]string{}, summary.Warnings...),
	}
	if err != nil {
		change.Status = ApplyStatusBlocked
		change.Message = err.Error()
		bootstrapManifest.Ready = false
		return change, bootstrapManifest, err
	}
	if !summary.Ready {
		change.Status = ApplyStatusBlocked
		change.Message = "production bootstrap is not ready"
		return change, bootstrapManifest, nil
	}
	return change, bootstrapManifest, nil
}

func productionInputFromPlan(plan SetupPlan, manifest InstallManifest) bootstrap.ProductionInput {
	return bootstrap.ProductionInput{
		NodeKey:             plan.Spec.NodeKey,
		DisplayName:         plan.Spec.DisplayName,
		NodeKind:            plan.Spec.NodeKind,
		NodeRole:            plan.Spec.NodeRole,
		RuntimeClass:        plan.Spec.RuntimeClass,
		AuthorityProfileKey: plan.Profile.AuthorityProfileKey,
		RuntimeProfileKey:   plan.Profile.RuntimeProfileKey,
		InstallID:           manifest.InstallID,
		PlanHash:            plan.PlanHash,
		Source:              "setup-apply",
		CorrelationID:       "setup-apply-" + plan.Spec.NodeKey,
		Metadata: map[string]any{
			"setup_apply": true,
			"install_id":  manifest.InstallID,
			"plan_hash":   plan.PlanHash,
		},
	}
}

func applyManifestStatus(manifest InstallManifest) SetupStatusSummary {
	status := SummaryConfigured
	if manifest.ProductionBootstrap.Checked && !manifest.ProductionBootstrap.Ready {
		status = SummaryPartial
	}
	if !manifest.ProductionBootstrap.Checked {
		status = SummaryPartial
	}
	now := time.Now().UTC()
	return SetupStatusSummary{Status: status, LastCheckedAt: &now}
}

func writeManifestChange(path string, manifest InstallManifest, dryRun bool) (ApplyChange, error) {
	if dryRun {
		return ApplyChange{ID: "write_manifest", Category: "manifest", Status: ApplyStatusWouldChange, Path: path}, nil
	}
	if err := WriteManifest(path, manifest); err != nil {
		return ApplyChange{}, fmt.Errorf("write manifest %s: %w", path, err)
	}
	return ApplyChange{ID: "write_manifest", Category: "manifest", Status: ApplyStatusChanged, Path: path}, nil
}

func applyBlocked(id, category, message string) ApplyChange {
	return ApplyChange{ID: id, Category: category, Status: ApplyStatusBlocked, Message: message}
}

func appendToApplyChanges(changes *applyChanges, change ApplyChange) {
	switch change.Status {
	case ApplyStatusChanged, ApplyStatusWouldChange:
		changes.changed = append(changes.changed, change)
	case ApplyStatusBlocked:
		changes.blocked = append(changes.blocked, change)
	default:
		changes.skipped = append(changes.skipped, change)
	}
}

func appendApplyChange(result *ApplyResult, change ApplyChange) {
	switch change.Status {
	case "":
		return
	case ApplyStatusChanged, ApplyStatusWouldChange:
		result.Changed = append(result.Changed, change)
	case ApplyStatusBlocked:
		result.Blocked = append(result.Blocked, change)
	default:
		result.Skipped = append(result.Skipped, change)
	}
}
