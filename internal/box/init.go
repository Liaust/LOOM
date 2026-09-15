package box

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"loom.local/loom/internal/ids"
)

func Init(input InitInput) (InitResult, error) {
	now := time.Now().UTC()
	if input.Now != nil {
		now = input.Now().UTC()
	}
	newBoxID := ids.NewBoxID
	if input.NewBoxID != nil {
		newBoxID = input.NewBoxID
	}

	before := Inspect(input.Resolved)
	contract, err := initContract(input.Resolved, before, now, newBoxID)
	if err != nil {
		result := baseInitResult(input, before, before)
		result.Diagnostics = append(result.Diagnostics, Diagnostic{
			Severity: "error",
			Code:     "box.contract_unavailable",
			Message:  err.Error(),
			Path:     ContractPath(input.Resolved.RootPath),
		})
		return result, err
	}

	result := baseInitResult(input, before, before)
	dirOps, fileOps := plannedInitOperations(contract)
	if err := checkInitConflicts(input.Resolved.RootPath, dirOps, fileOps, &result); err != nil {
		return result, err
	}

	if input.DryRun {
		result.PlannedDirs = missingInitDirPaths(dirOps)
		result.PlannedFiles = missingInitFilePaths(fileOps)
		return result, nil
	}

	for _, op := range dirOps {
		if existsAsDir(op.Path) {
			result.SkippedDirs = append(result.SkippedDirs, op.Path)
			continue
		}
		if err := os.MkdirAll(op.Path, 0o755); err != nil {
			return result, fmt.Errorf("create box directory %s: %w", op.Path, err)
		}
		result.CreatedDirs = append(result.CreatedDirs, op.Path)
	}

	for _, op := range fileOps {
		if existsAsRegularFile(op.Path) && (!op.Update || fileContentMatches(op.Path, op.Content)) {
			result.SkippedFiles = append(result.SkippedFiles, op.Path)
			continue
		}
		if backupPath, err := backupInitFileBeforeUpdate(input.Resolved.RootPath, op, now); err != nil {
			return result, fmt.Errorf("backup box file %s: %w", op.Path, err)
		} else if backupPath != "" {
			result.BackupFiles = append(result.BackupFiles, backupPath)
		}
		if err := os.MkdirAll(filepath.Dir(op.Path), 0o755); err != nil {
			return result, fmt.Errorf("create parent directory for %s: %w", op.Path, err)
		}
		if err := os.WriteFile(op.Path, []byte(op.Content), os.FileMode(op.Mode)); err != nil {
			return result, fmt.Errorf("write box file %s: %w", op.Path, err)
		}
		result.CreatedFiles = append(result.CreatedFiles, op.Path)
	}

	result.StatusAfter = Inspect(input.Resolved)
	normalizeInitResultIdentity(&result)
	return result, nil
}

type initDirOperation struct {
	Key  string
	Path string
}

type initFileOperation struct {
	Key     string
	Path    string
	Mode    fileMode
	Content string
	Update  bool
}

func baseInitResult(input InitInput, before Status, after Status) InitResult {
	result := InitResult{
		SchemaVersion: SchemaVersion,
		DryRun:        input.DryRun,
		RootPath:      input.Resolved.RootPath,
		Profile:       input.Resolved.Profile,
		OwnerNode:     input.Resolved.OwnerNode,
		StatusBefore:  before,
		StatusAfter:   after,
	}
	normalizeInitResultIdentity(&result)
	return result
}

func normalizeInitResultIdentity(result *InitResult) {
	if applyInitResultIdentity(result, result.StatusAfter) {
		return
	}
	applyInitResultIdentity(result, result.StatusBefore)
}

func applyInitResultIdentity(result *InitResult, status Status) bool {
	if !status.Initialized && status.ContractState != "valid" {
		return false
	}
	applied := false
	if strings.TrimSpace(status.Profile) != "" {
		result.Profile = status.Profile
		applied = true
	}
	if strings.TrimSpace(status.OwnerNode) != "" {
		result.OwnerNode = status.OwnerNode
		applied = true
	}
	return applied
}

func initContract(resolved Resolved, before Status, now time.Time, newBoxID func() string) (Contract, error) {
	if before.ContractState == "valid" && before.Contract != nil {
		return upgradeExistingContract(cloneContract(*before.Contract), resolved, before, now), nil
	}
	if before.ContractState == "invalid" {
		return Contract{}, fmt.Errorf("existing box contract is invalid")
	}
	contract := DefaultContract(resolved)
	contract.BoxID = newBoxID()
	timestamp := now.Format(time.RFC3339)
	contract.Metadata.CreatedAt = timestamp
	contract.Metadata.UpdatedAt = timestamp
	contract.Metadata.CreatedBy = "loom box init"
	return contract, nil
}

func cloneContract(contract Contract) Contract {
	if contract.Areas != nil {
		areas := make(map[string]Area, len(contract.Areas))
		for key, area := range contract.Areas {
			areas[key] = area
		}
		contract.Areas = areas
	}
	if contract.Policies != nil {
		policies := make(map[string]string, len(contract.Policies))
		for key, value := range contract.Policies {
			policies[key] = value
		}
		contract.Policies = policies
	}
	return contract
}

func upgradeExistingContract(contract Contract, resolved Resolved, before Status, now time.Time) Contract {
	changed := false
	if filepath.Clean(contract.RootPath) != filepath.Clean(resolved.RootPath) {
		contract.RootPath = resolved.RootPath
		changed = true
	}
	if contract.Areas == nil {
		contract.Areas = map[string]Area{}
		changed = true
	}
	if _, ok := contract.Areas[AreaDocuments]; !ok {
		contract.Areas[AreaDocuments] = Area{Path: "Documents", Enabled: true}
		changed = true
	}
	if contract.Profile == ProfileMain {
		for key, area := range map[string]Area{
			areaTopics:  {Path: "Topics", Enabled: true},
			areaLibrary: {Path: "Library", Enabled: true},
		} {
			if _, ok := contract.Areas[key]; !ok {
				contract.Areas[key] = area
				changed = true
			}
		}
		if _, ok := contract.Areas[AreaLane]; ok {
			delete(contract.Areas, AreaLane)
			changed = true
		}
	} else {
		if _, ok := contract.Areas[AreaLane]; !ok {
			contract.Areas[AreaLane] = Area{Path: DefaultLaneDirName, Enabled: true}
			changed = true
		}
		if laneArea, ok := contract.Areas[AreaLane]; ok && strings.TrimSpace(laneArea.Path) == LegacyLaneDirName && !statusHasExistingDirectory(before.Areas, AreaLane) {
			laneArea.Path = DefaultLaneDirName
			contract.Areas[AreaLane] = laneArea
			changed = true
		}
	}
	if _, ok := contract.Areas[AreaDropzone]; ok {
		delete(contract.Areas, AreaDropzone)
		changed = true
	}
	if contract.Policies == nil {
		contract.Policies = map[string]string{}
		changed = true
	}
	if strings.TrimSpace(contract.Policies[AreaDocuments]) == "" {
		contract.Policies[AreaDocuments] = ".loom/policies/documents.watch.yaml"
		changed = true
	}
	if contract.Profile == ProfileMain {
		if _, ok := contract.Policies[AreaLane]; ok {
			delete(contract.Policies, AreaLane)
			changed = true
		}
	} else if strings.TrimSpace(contract.Policies[AreaLane]) == "" {
		contract.Policies[AreaLane] = ".loom/policies/lane.transfer.yaml"
		changed = true
	}
	if _, ok := contract.Policies[AreaDropzone]; ok {
		delete(contract.Policies, AreaDropzone)
		changed = true
	}
	if strings.TrimSpace(contract.Policies[PolicyBackupContracts]) == "" {
		contract.Policies[PolicyBackupContracts] = DefaultBackupContractsRelDir
		changed = true
	}
	if _, ok := contract.Areas[AreaLaunchpad]; ok && !statusHasExistingDirectory(before.Areas, AreaLaunchpad) {
		delete(contract.Areas, AreaLaunchpad)
		delete(contract.Policies, AreaLaunchpad)
		changed = true
	}
	if strings.TrimSpace(contract.DefaultProjectPath) == "" {
		contract.DefaultProjectPath = "Projects"
		changed = true
	}
	if changed {
		contract.Metadata.UpdatedAt = now.Format(time.RFC3339)
		if strings.TrimSpace(contract.Metadata.CreatedBy) == "" {
			contract.Metadata.CreatedBy = "loom box init"
		}
	}
	return contract
}

func statusHasExistingDirectory(items []PathStatus, key string) bool {
	for _, item := range items {
		if item.Key == key && item.Exists && item.IsDir && item.Status == "ok" {
			return true
		}
	}
	return false
}

func plannedInitOperations(contract Contract) ([]initDirOperation, []initFileOperation) {
	dirs := []initDirOperation{{Key: "root", Path: contract.RootPath}}
	for _, expected := range ExpectedDirectories(contract) {
		dirs = append(dirs, initDirOperation{Key: expected.Key, Path: expected.Path})
	}

	files := []initFileOperation{{
		Key:     "contract",
		Path:    ContractPath(contract.RootPath),
		Mode:    defaultFileMode,
		Content: renderContractYAML(contract),
		Update:  true,
	}}
	for _, template := range defaultTemplateFiles() {
		relPath := template.RelPath
		if template.Area != "" {
			area, ok := contract.Areas[template.Area]
			if !ok {
				continue
			}
			if strings.HasSuffix(template.RelPath, ".yaml") {
				policyRel, ok := contract.Policies[template.Area]
				if !ok {
					continue
				}
				relPath = policyRel
			} else if _, suffix, ok := strings.Cut(template.RelPath, "/"); ok && !strings.HasPrefix(template.RelPath, ".loom/") {
				relPath = filepath.ToSlash(filepath.Join(filepath.FromSlash(area.Path), filepath.FromSlash(suffix)))
			}
		}
		path := templatePath(contract.RootPath, relPath)
		files = append(files, initFileOperation{
			Key:     template.Key,
			Path:    path,
			Mode:    template.Mode,
			Content: template.Render(contract),
			Update:  template.UpdateExisting != nil && template.UpdateExisting(path),
		})
	}
	return dirs, files
}

func checkInitConflicts(root string, dirs []initDirOperation, files []initFileOperation, result *InitResult) error {
	for _, op := range dirs {
		info, err := os.Stat(op.Path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			result.Diagnostics = append(result.Diagnostics, Diagnostic{Severity: "error", Code: "box.path_unavailable", Message: err.Error(), Path: op.Path})
			continue
		}
		if !info.IsDir() {
			result.Diagnostics = append(result.Diagnostics, Diagnostic{
				Severity:   "error",
				Code:       "box.directory_blocked",
				Message:    "required Box directory path is blocked by a file",
				Path:       op.Path,
				Suggestion: "move or remove the file before running loom box init",
			})
		}
	}
	for _, op := range files {
		info, err := os.Stat(op.Path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			result.Diagnostics = append(result.Diagnostics, Diagnostic{Severity: "error", Code: "box.file_unavailable", Message: err.Error(), Path: op.Path})
			continue
		}
		if !info.Mode().IsRegular() {
			result.Diagnostics = append(result.Diagnostics, Diagnostic{
				Severity:   "error",
				Code:       "box.file_blocked",
				Message:    "required Box file path is blocked by a non-regular file",
				Path:       op.Path,
				Suggestion: "move or remove the path before running loom box init",
			})
		}
	}
	if len(result.Diagnostics) > 0 {
		return fmt.Errorf("box init has %d blocking diagnostic(s) under %s", len(result.Diagnostics), root)
	}
	return nil
}

func renderContractYAML(contract Contract) string {
	payload, err := yaml.Marshal(contract)
	if err != nil {
		return ""
	}
	return string(payload)
}

func backupInitFileBeforeUpdate(root string, op initFileOperation, now time.Time) (string, error) {
	if !op.Update || !existsAsRegularFile(op.Path) || fileContentMatches(op.Path, op.Content) {
		return "", nil
	}
	payload, err := os.ReadFile(op.Path)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(root, op.Path)
	if err != nil {
		rel = filepath.Base(op.Path)
	}
	rel = strings.Trim(filepath.ToSlash(rel), "/")
	if rel == "" || strings.HasPrefix(rel, "../") || rel == ".." {
		rel = filepath.Base(op.Path)
	}
	backupName := strings.ReplaceAll(rel, "/", "__") + "." + now.Format("20060102T150405Z") + ".bak"
	backupDir := filepath.Join(root, ".loom", "backups", "box-init")
	if err := os.MkdirAll(backupDir, 0o770); err != nil {
		return "", err
	}
	backupPath := filepath.Join(backupDir, backupName)
	backupPath, err = nextAvailableBackupPath(backupPath)
	if err != nil {
		return "", err
	}
	return backupPath, os.WriteFile(backupPath, payload, 0o600)
}

func nextAvailableBackupPath(path string) (string, error) {
	if _, err := os.Lstat(path); err != nil {
		if os.IsNotExist(err) {
			return path, nil
		}
		return "", fmt.Errorf("inspect Box init backup path %s: %w", path, err)
	}
	ext := filepath.Ext(path)
	base := strings.TrimSuffix(path, ext)
	for i := 2; ; i++ {
		candidate := fmt.Sprintf("%s.%d%s", base, i, ext)
		if _, err := os.Lstat(candidate); err != nil {
			if os.IsNotExist(err) {
				return candidate, nil
			}
			return "", fmt.Errorf("inspect Box init backup path %s: %w", candidate, err)
		}
	}
}

func existsAsDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func existsAsRegularFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

func fileContentMatches(path string, content string) bool {
	payload, err := os.ReadFile(path)
	return err == nil && string(payload) == content
}

func missingInitDirPaths(ops []initDirOperation) []string {
	paths := []string{}
	for _, op := range ops {
		if !existsAsDir(op.Path) {
			paths = append(paths, op.Path)
		}
	}
	return paths
}

func missingInitFilePaths(ops []initFileOperation) []string {
	paths := []string{}
	for _, op := range ops {
		if !existsAsRegularFile(op.Path) || (op.Update && !fileContentMatches(op.Path, op.Content)) {
			paths = append(paths, op.Path)
		}
	}
	return paths
}
