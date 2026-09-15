package setup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"loom.local/loom/internal/box"
	"loom.local/loom/internal/enrollmentflow"
	"loom.local/loom/internal/version"
)

const (
	DefaultWorkspaceNodeKey     = "macbook"
	DefaultWorkspaceDisplayName = "MacBook Primary Workspace"
	DefaultWorkspaceMainURL     = "http://10.44.0.2:8080"
)

type WorkspaceInstallInput struct {
	NodeKey      string
	DisplayName  string
	MainHost     string
	MainURL      string
	BoxPath      string
	HomeDir      string
	UserName     string
	SourcePath   string
	ReleaseID    string
	ManifestPath string

	InstallMode    string
	ServiceManager string

	DryRun               bool
	Yes                  bool
	Resume               bool
	NoInteractive        bool
	SkipEnroll           bool
	EnrollmentTTLSeconds int

	BuildRunner            WorkspaceCommandRunner
	EnrollmentMainRunner   enrollmentflow.MainRunner
	EnrollmentTargetRunner enrollmentflow.TargetRunner
	LaunchdRunner          LaunchdRunner
	Now                    func() time.Time
}

type WorkspaceCommandRunner interface {
	Run(ctx context.Context, dir string, name string, args ...string) error
}

type DefaultWorkspaceCommandRunner struct{}

func (DefaultWorkspaceCommandRunner) Run(ctx context.Context, dir string, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

type WorkspaceInstallPlan struct {
	CreatedAt time.Time        `json:"created_at"`
	Release   ReleaseStagePlan `json:"release"`
	Setup     SetupPlan        `json:"setup"`
}

type ReleaseStagePlan struct {
	ReleaseID      string            `json:"release_id"`
	ReleasePath    string            `json:"release_path"`
	CurrentPath    string            `json:"current_path"`
	LocalBinDir    string            `json:"local_bin_dir"`
	SourcePath     string            `json:"source_path"`
	Commit         string            `json:"commit,omitempty"`
	Version        string            `json:"version"`
	BinaryPaths    map[string]string `json:"binary_paths"`
	LinkPaths      map[string]string `json:"link_paths"`
	ManifestPath   string            `json:"manifest_path"`
	MigrationsPath string            `json:"migrations_path,omitempty"`
}

type WorkspaceInstallResult struct {
	Status     string                 `json:"status"`
	DryRun     bool                   `json:"dry_run"`
	Refused    bool                   `json:"refused"`
	Refusal    string                 `json:"refusal,omitempty"`
	Plan       WorkspaceInstallPlan   `json:"plan"`
	Release    ReleaseStageResult     `json:"release"`
	Setup      ApplyResult            `json:"setup"`
	Enrollment *enrollmentflow.Result `json:"enrollment,omitempty"`
}

type ReleaseStageResult struct {
	ReleaseID   string        `json:"release_id"`
	ReleasePath string        `json:"release_path"`
	CurrentPath string        `json:"current_path"`
	Changed     []ApplyChange `json:"changed,omitempty"`
	Skipped     []ApplyChange `json:"skipped,omitempty"`
	Blocked     []ApplyChange `json:"blocked,omitempty"`
}

func PlanWorkspaceInstall(input WorkspaceInstallInput) (WorkspaceInstallPlan, error) {
	now := time.Now
	if input.Now != nil {
		now = input.Now
	}
	createdAt := now().UTC()
	facts := CollectLocalFacts()
	specInput, err := workspaceSetupSpec(input)
	if err != nil {
		return WorkspaceInstallPlan{}, err
	}
	setupPlan, err := Plan(PlannerInput{
		Spec:         specInput,
		Facts:        facts,
		Now:          now,
		ManifestPath: input.ManifestPath,
	})
	if err != nil {
		return WorkspaceInstallPlan{}, err
	}
	releasePlan, err := planReleaseStage(input, setupPlan.Spec.HomeDir, createdAt)
	if err != nil {
		return WorkspaceInstallPlan{}, err
	}
	setupPlan.Spec.SourcePath = releasePlan.ReleasePath
	setupPlan.Spec.SourceCommit = releasePlan.Commit
	setupPlan.Spec.PackageMode = PackageModePrebuilt
	setupPlan.PlanHash = HashPlan(setupPlan)
	setupPlan.PlanID = "setup_plan_" + strings.TrimPrefix(setupPlan.PlanHash, "sha256:")[:16]
	return WorkspaceInstallPlan{
		CreatedAt: createdAt,
		Release:   releasePlan,
		Setup:     setupPlan,
	}, nil
}

func RunWorkspaceInstall(ctx context.Context, input WorkspaceInstallInput) (WorkspaceInstallResult, error) {
	plan, err := PlanWorkspaceInstall(input)
	if err != nil {
		return WorkspaceInstallResult{}, err
	}
	result := WorkspaceInstallResult{
		Status: "planned",
		DryRun: input.DryRun,
		Plan:   plan,
	}
	if !input.DryRun && !input.Yes {
		result.Status = "refused"
		result.Refused = true
		if input.NoInteractive {
			result.Refusal = "workspace install is non-interactive; pass --yes to install LOOM locally"
		} else {
			result.Refusal = "workspace install requires --yes before mutating local setup state"
		}
		return result, nil
	}
	release, err := StageWorkspaceRelease(ctx, plan.Release, StageReleaseInput{
		DryRun: input.DryRun,
		Runner: input.BuildRunner,
	})
	result.Release = release
	if err != nil {
		result.Status = "failed"
		return result, err
	}
	if len(release.Blocked) > 0 {
		result.Status = "blocked"
		return result, nil
	}

	setupSpec := plan.Setup.Spec
	setupSpec.SourcePath = plan.Release.ReleasePath
	setupSpec.SourceCommit = plan.Release.Commit
	setupSpec.PackageMode = PackageModePrebuilt
	setupResult, err := Apply(ApplyInput{
		Spec:          setupSpec,
		ManifestPath:  input.ManifestPath,
		DryRun:        input.DryRun,
		Yes:           input.Yes,
		Resume:        input.Resume,
		NoInteractive: input.NoInteractive,
		CollectFacts:  true,
		Now:           input.Now,
		EnrollmentMainRunner: firstMainRunner(
			input.EnrollmentMainRunner,
			setupSpec.MainURL,
			"setup.workspace.install."+setupSpec.NodeKey,
		),
		EnrollmentTargetRunner: input.EnrollmentTargetRunner,
		LaunchdRunner:          input.LaunchdRunner,
	})
	result.Setup = setupResult
	result.Enrollment = setupResult.Enrollment
	switch {
	case err != nil:
		result.Status = "failed"
	case setupResult.Refused:
		result.Status = "refused"
	case len(setupResult.Blocked) > 0:
		result.Status = "blocked"
	case input.DryRun:
		result.Status = "dry_run"
	default:
		result.Status = "applied"
	}
	return result, err
}

type StageReleaseInput struct {
	DryRun bool
	Runner WorkspaceCommandRunner
}

func StageWorkspaceRelease(ctx context.Context, plan ReleaseStagePlan, input StageReleaseInput) (ReleaseStageResult, error) {
	result := ReleaseStageResult{
		ReleaseID:   plan.ReleaseID,
		ReleasePath: plan.ReleasePath,
		CurrentPath: plan.CurrentPath,
	}
	if input.DryRun {
		for _, path := range []string{plan.ReleasePath, filepath.Join(plan.ReleasePath, "bin"), plan.LocalBinDir} {
			result.Changed = append(result.Changed, ApplyChange{ID: "stage_release_dir", Category: "release", Status: ApplyStatusWouldChange, Path: path})
		}
		for name, path := range plan.BinaryPaths {
			result.Changed = append(result.Changed, ApplyChange{ID: "build_" + name, Category: "release", Status: ApplyStatusWouldChange, Path: path})
		}
		for name, path := range plan.LinkPaths {
			result.Changed = append(result.Changed, ApplyChange{ID: "link_" + name, Category: "release", Status: ApplyStatusWouldChange, Path: path})
		}
		result.Changed = append(result.Changed, ApplyChange{ID: "write_release_manifest", Category: "release", Status: ApplyStatusWouldChange, Path: plan.ManifestPath})
		return result, nil
	}
	for _, path := range []string{plan.ReleasePath, filepath.Join(plan.ReleasePath, "bin"), plan.LocalBinDir} {
		change, err := ensureDirChange("stage_release_dir", "release", path, 0o755, false)
		if err != nil {
			return result, err
		}
		appendReleaseChange(&result, change)
	}
	runner := input.Runner
	if runner == nil {
		runner = DefaultWorkspaceCommandRunner{}
	}
	for name, outputPath := range plan.BinaryPaths {
		if err := runner.Run(ctx, plan.SourcePath, "go", "build", "-o", outputPath, "./cmd/"+name); err != nil {
			result.Blocked = append(result.Blocked, ApplyChange{
				ID:       "build_" + name,
				Category: "release",
				Status:   ApplyStatusBlocked,
				Path:     outputPath,
				Message:  err.Error(),
			})
			return result, nil
		}
		result.Changed = append(result.Changed, ApplyChange{ID: "build_" + name, Category: "release", Status: ApplyStatusChanged, Path: outputPath})
	}
	if err := writeWorkspaceReleaseManifest(plan); err != nil {
		return result, err
	}
	result.Changed = append(result.Changed, ApplyChange{ID: "write_release_manifest", Category: "release", Status: ApplyStatusChanged, Path: plan.ManifestPath})
	if change, err := replaceSymlinkChange("link_current_release", plan.CurrentPath, plan.ReleasePath); err != nil {
		return result, err
	} else {
		appendReleaseChange(&result, change)
	}
	for name, linkPath := range plan.LinkPaths {
		target := filepath.Join(plan.CurrentPath, "bin", name)
		change, err := replaceSymlinkChange("link_"+name, linkPath, target)
		if err != nil {
			return result, err
		}
		appendReleaseChange(&result, change)
	}
	return result, nil
}

func workspaceSetupSpec(input WorkspaceInstallInput) (SetupSpec, error) {
	home, err := workspaceHomeDir(input.HomeDir)
	if err != nil {
		return SetupSpec{}, err
	}
	userName := strings.TrimSpace(input.UserName)
	if userName == "" {
		if current, currentErr := user.Current(); currentErr == nil {
			userName = current.Username
		}
	}
	mainURL := strings.TrimSpace(input.MainURL)
	if mainURL == "" && strings.TrimSpace(input.MainHost) != "" {
		mainURL = "http://" + strings.TrimSpace(input.MainHost) + ":8080"
	}
	if mainURL == "" {
		mainURL = DefaultWorkspaceMainURL
	}
	nodeKey := strings.TrimSpace(input.NodeKey)
	if nodeKey == "" {
		nodeKey = DefaultWorkspaceNodeKey
	}
	displayName := strings.TrimSpace(input.DisplayName)
	if displayName == "" {
		displayName = DefaultWorkspaceDisplayName
	}
	boxPath := strings.TrimSpace(input.BoxPath)
	if boxPath == "" {
		boxPath = filepath.Join(home, box.DefaultRootDirName)
	}
	installMode := strings.TrimSpace(input.InstallMode)
	if installMode == "" {
		installMode = InstallModeService
	}
	serviceManager := strings.TrimSpace(input.ServiceManager)
	if serviceManager == "" {
		serviceManager = ServiceManagerLaunchd
	}
	spec := SetupSpec{
		SchemaVersion:        SchemaVersion,
		NodeKey:              nodeKey,
		DisplayName:          displayName,
		NodeKind:             "workspace",
		NodeRole:             "primary_workspace",
		RuntimeClass:         "workspace_full",
		MainURL:              mainURL,
		InstallMode:          installMode,
		ServiceManager:       serviceManager,
		PackageMode:          PackageModePrebuilt,
		UserName:             userName,
		HomeDir:              home,
		BoxPath:              boxPath,
		BoxProfile:           "workspace",
		BootstrapMode:        "none",
		RunEnrollment:        !input.SkipEnroll,
		SkipEnroll:           input.SkipEnroll,
		EnrollmentTTLSeconds: input.EnrollmentTTLSeconds,
		ApproveEnrollment:    true,
		VerifyHeartbeat:      true,
		EnableNodeAgent:      true,
		EnableBox:            true,
		EnableDropzone:       false,
		EnableWatchedRoots:   true,
		EnableProviders:      true,
		EnableLoomd:          false,
		AutoMigrate:          false,
		ProductionBootstrap:  false,
		ProviderMode:         "enabled",
		NodeAgentConfigPath:  filepath.Join(home, ".config", "loom-node-agent", "config.json"),
		NodeAgentStatePath:   filepath.Join(home, ".local", "state", "loom-node-agent", "state.json"),
		NodeAgentDataDir:     filepath.Join(home, ".local", "state", "loom-node-agent"),
	}
	if spec.EnrollmentTTLSeconds <= 0 {
		spec.EnrollmentTTLSeconds = 1800
	}
	return spec, nil
}

func planReleaseStage(input WorkspaceInstallInput, home string, now time.Time) (ReleaseStagePlan, error) {
	sourcePath := strings.TrimSpace(input.SourcePath)
	if sourcePath == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return ReleaseStagePlan{}, err
		}
		sourcePath = cwd
	}
	absSource, err := filepath.Abs(sourcePath)
	if err != nil {
		return ReleaseStagePlan{}, err
	}
	commit := gitShortCommit(absSource)
	info := version.Current()
	releaseID := strings.TrimSpace(input.ReleaseID)
	if releaseID == "" {
		releaseID = workspaceReleaseID(info.Version, commit, absSource)
	}
	dataRoot := filepath.Join(home, ".local", "share", "loom")
	releasePath := filepath.Join(dataRoot, "releases", releaseID)
	binaryPaths := map[string]string{
		"loom":            filepath.Join(releasePath, "bin", "loom"),
		"loom-node-agent": filepath.Join(releasePath, "bin", "loom-node-agent"),
	}
	linkPaths := map[string]string{
		"loom":            filepath.Join(home, ".local", "bin", "loom"),
		"loom-node-agent": filepath.Join(home, ".local", "bin", "loom-node-agent"),
	}
	migrationsPath := ""
	if pathExists(filepath.Join(absSource, "migrations")) {
		migrationsPath = filepath.Join(absSource, "migrations")
	}
	return ReleaseStagePlan{
		ReleaseID:      releaseID,
		ReleasePath:    releasePath,
		CurrentPath:    filepath.Join(dataRoot, "current"),
		LocalBinDir:    filepath.Join(home, ".local", "bin"),
		SourcePath:     absSource,
		Commit:         commit,
		Version:        info.Version,
		BinaryPaths:    binaryPaths,
		LinkPaths:      linkPaths,
		ManifestPath:   filepath.Join(releasePath, "loom-release.yaml"),
		MigrationsPath: migrationsPath,
	}, nil
}

type workspaceReleaseManifest struct {
	SchemaVersion string         `json:"schema_version" yaml:"schema_version"`
	ReleaseID     string         `json:"release_id,omitempty" yaml:"release_id,omitempty"`
	Version       string         `json:"version,omitempty" yaml:"version,omitempty"`
	Commit        string         `json:"commit,omitempty" yaml:"commit,omitempty"`
	SourcePath    string         `json:"source_path,omitempty" yaml:"source_path,omitempty"`
	MigrationsDir string         `json:"migrations_dir,omitempty" yaml:"migrations_dir,omitempty"`
	FlakeOutput   string         `json:"flake_output,omitempty" yaml:"flake_output,omitempty"`
	CreatedAt     time.Time      `json:"created_at,omitempty" yaml:"created_at,omitempty"`
	Metadata      map[string]any `json:"metadata,omitempty" yaml:"metadata,omitempty"`
}

func writeWorkspaceReleaseManifest(plan ReleaseStagePlan) error {
	manifest := workspaceReleaseManifest{
		SchemaVersion: "loom.release.v0.5.1",
		ReleaseID:     plan.ReleaseID,
		Version:       plan.Version,
		Commit:        plan.Commit,
		SourcePath:    plan.ReleasePath,
		MigrationsDir: plan.MigrationsPath,
		FlakeOutput:   ".#loom",
		CreatedAt:     time.Now().UTC(),
		Metadata: map[string]any{
			"install_target": "workspace_local",
			"source_path":    plan.SourcePath,
		},
	}
	payload, err := yaml.Marshal(manifest)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(plan.ManifestPath), 0o755); err != nil {
		return err
	}
	return os.WriteFile(plan.ManifestPath, payload, 0o644)
}

func workspaceHomeDir(explicit string) (string, error) {
	home := strings.TrimSpace(explicit)
	if home == "" {
		var err error
		home, err = os.UserHomeDir()
		if err != nil {
			return "", err
		}
	}
	if strings.HasPrefix(home, "~") {
		return normalizePath(home, "")
	}
	if !filepath.IsAbs(home) {
		abs, err := filepath.Abs(home)
		if err != nil {
			return "", err
		}
		home = abs
	}
	return filepath.Clean(home), nil
}

func workspaceReleaseID(versionValue, commit, sourcePath string) string {
	seed := strings.Join([]string{versionValue, commit, sourcePath, runtime.GOOS, runtime.GOARCH}, "|")
	sum := sha256.Sum256([]byte(seed))
	suffix := hex.EncodeToString(sum[:])[:12]
	prefix := "release"
	if strings.TrimSpace(versionValue) != "" {
		prefix = filesystemSafeReleaseToken(versionValue)
	}
	if strings.TrimSpace(commit) != "" {
		return prefix + "-" + filesystemSafeReleaseToken(commit) + "-" + suffix
	}
	return prefix + "-" + suffix
}

func filesystemSafeReleaseToken(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var b strings.Builder
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-' || r == '_' || r == '.':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	out := strings.Trim(b.String(), "-_.")
	if out == "" {
		return "release"
	}
	return out
}

func gitShortCommit(sourcePath string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "-C", sourcePath, "rev-parse", "--short", "HEAD")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func firstMainRunner(runner enrollmentflow.MainRunner, mainURL string, correlationID string) enrollmentflow.MainRunner {
	if runner != nil {
		return runner
	}
	return enrollmentflow.NewHTTPMainRunner(mainURL, correlationID)
}

func replaceSymlinkChange(id, linkPath, targetPath string) (ApplyChange, error) {
	linkPath = filepath.Clean(strings.TrimSpace(linkPath))
	targetPath = filepath.Clean(strings.TrimSpace(targetPath))
	if linkPath == "" || targetPath == "" {
		return ApplyChange{}, errors.New("link path and target path are required")
	}
	if current, err := os.Readlink(linkPath); err == nil && filepath.Clean(current) == targetPath {
		return ApplyChange{ID: id, Category: "release", Status: ApplyStatusAlreadySatisfied, Path: linkPath, Message: targetPath}, nil
	}
	if info, err := os.Lstat(linkPath); err == nil && info.Mode()&os.ModeSymlink == 0 {
		return ApplyChange{ID: id, Category: "release", Status: ApplyStatusBlocked, Path: linkPath, Message: "path exists and is not a symlink"}, nil
	} else if err != nil && !os.IsNotExist(err) {
		return ApplyChange{}, err
	}
	if err := os.MkdirAll(filepath.Dir(linkPath), 0o755); err != nil {
		return ApplyChange{}, err
	}
	tmp := linkPath + ".tmp-" + fmt.Sprintf("%d", time.Now().UnixNano())
	_ = os.Remove(tmp)
	if err := os.Symlink(targetPath, tmp); err != nil {
		return ApplyChange{}, err
	}
	if err := os.Rename(tmp, linkPath); err != nil {
		_ = os.Remove(tmp)
		return ApplyChange{}, err
	}
	return ApplyChange{ID: id, Category: "release", Status: ApplyStatusChanged, Path: linkPath, Message: targetPath}, nil
}

func appendReleaseChange(result *ReleaseStageResult, change ApplyChange) {
	switch change.Status {
	case ApplyStatusAlreadySatisfied, ApplyStatusSkipped:
		result.Skipped = append(result.Skipped, change)
	case ApplyStatusBlocked:
		result.Blocked = append(result.Blocked, change)
	default:
		result.Changed = append(result.Changed, change)
	}
}
