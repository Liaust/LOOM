package box

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"loom.local/loom/internal/backupcontracts"
	"loom.local/loom/internal/filepolicy"
	"loom.local/loom/internal/filesystemconnector"
	noderuntime "loom.local/loom/internal/nodeagent/runtime"
	agentwatchedroots "loom.local/loom/internal/nodeagent/watchedroots"
	"loom.local/loom/internal/projectcontracts"
	"loom.local/loom/internal/projects"
	loomsync "loom.local/loom/internal/sync"
)

const (
	WatchPolicySchemaVersion       = "loom.box.watch_policy.v0.6"
	LegacyWatchPolicySchemaVersion = "loom.box.watch_policy.v0.4.1"

	BoxWatchStatusRegistered        = "registered"
	BoxWatchStatusPendingAgentApply = "pending_agent_apply"
	BoxWatchStatusApplied           = "applied"
	BoxWatchStatusReported          = "reported"
	BoxWatchStatusBlocked           = "blocked"
	BoxWatchStatusDisabled          = "disabled"
	BoxWatchStatusStale             = "stale"

	boxWatchSafeRootKey = "loom_box"

	boxWatchFullRescanInterval = "6h"
	boxWatchNotesMaxFileBytes  = int64(50 * 1024 * 1024)
	boxWatchDocsMaxFileBytes   = int64(60 * 1024 * 1024 * 1024)
)

func BuildWatchPlan(input WatchStatusInput) (WatchPlan, error) {
	if input.Resolved.BoxID != "" && input.Resolved.RootRef == "" {
		return WatchPlan{}, fmt.Errorf("selected Box watch plan requires its immutable root reference")
	}
	status := Inspect(input.Resolved)
	plan := WatchPlan{
		SchemaVersion: SchemaVersion,
		ProjectRoot:   status.RootPath,
		RootPath:      status.RootPath,
		Profile:       status.Profile,
		OwnerNode:     status.OwnerNode,
		ContractPath:  status.ContractPath,
		Excluded:      boxWatchExcludedAreas(status.Contract),
	}
	if status.Contract != nil {
		plan.BoxID = status.Contract.BoxID
	}
	if input.Resolved.BoxID != "" && input.Resolved.BoxID != plan.BoxID {
		return plan, fmt.Errorf("Box descriptor identity changed")
	}
	if status.State != "ok" || status.Contract == nil {
		plan.Diagnostics = append(plan.Diagnostics, Diagnostic{
			Severity:   "error",
			Code:       "box.watch.box_not_ready",
			Message:    fmt.Sprintf("Box must be initialized and complete before watch policies can be planned; current state is %s", status.State),
			Path:       status.RootPath,
			Suggestion: fmt.Sprintf("run loom box init --path %s --profile %s", shellQuotePlanPath(status.RootPath), status.Profile),
		})
		return plan, fmt.Errorf("box watch plan requires Box state ok, got %s", status.State)
	}

	for _, area := range boxWatchPlanAreas(*status.Contract) {
		item, diagnostics := watchRootItemForArea(*status.Contract, area)
		plan.Diagnostics = append(plan.Diagnostics, diagnostics...)
		if hasErrorDiagnostics(diagnostics) {
			continue
		}
		if item.BackendRootKey != "" {
			plan.WatchedRoots = append(plan.WatchedRoots, item)
		}
	}
	backupPlan, err := backupcontracts.PlanWatchedRoots(backupcontracts.WatchPlanOptions{
		BoxRoot:          status.Contract.RootPath,
		BoxID:            status.Contract.BoxID,
		OwnerNode:        status.Contract.OwnerNode,
		DirectoryRelPath: status.Contract.Policies[PolicyBackupContracts],
	})
	if err != nil {
		plan.Diagnostics = append(plan.Diagnostics, Diagnostic{
			Severity:   "error",
			Code:       "box.watch.backup_contracts_unavailable",
			Message:    err.Error(),
			Path:       filepath.Join(status.Contract.RootPath, filepath.FromSlash(status.Contract.Policies[PolicyBackupContracts])),
			Suggestion: "run loom box init again if the backup contract metadata path is invalid",
		})
		return plan, err
	}
	for _, problem := range backupPlan.Problems {
		plan.Diagnostics = append(plan.Diagnostics, Diagnostic{
			Severity:   "error",
			Code:       "box.watch.backup_contract_invalid",
			Message:    problem.Message,
			Path:       problem.Path,
			Suggestion: "fix or disable the backup contract before applying the Box watch plan",
		})
	}
	for _, item := range backupPlan.Items {
		item.AgentCommands = boxWatchCommands(status.Contract.RootPath, []projectcontracts.ProjectWatchedRootItem{{BackendRootKey: item.BackendRootKey}})
		plan.WatchedRoots = append(plan.WatchedRoots, item)
	}
	if input.Resolved.RootRef != "" && input.Resolved.RootRef != "box" {
		if input.Resolved.RootRef != "box:"+plan.BoxID {
			return plan, fmt.Errorf("Box root reference disagrees with descriptor identity")
		}
		for i, item := range plan.WatchedRoots {
			item, err = namespaceBoxWatchItem(item, plan.BoxID)
			if err != nil {
				return plan, err
			}
			item.AgentCommands = boxWatchCommands(plan.RootPath, []projectcontracts.ProjectWatchedRootItem{item})
			plan.WatchedRoots[i] = item
		}
	}
	sort.SliceStable(plan.WatchedRoots, func(i, j int) bool {
		return plan.WatchedRoots[i].BackendRootKey < plan.WatchedRoots[j].BackendRootKey
	})
	plan.Commands = boxWatchPlanCommands(plan.WatchedRoots)
	if hasErrorDiagnostics(plan.Diagnostics) {
		return plan, fmt.Errorf("box watch plan has %d error diagnostic(s)", countErrorDiagnostics(plan.Diagnostics))
	}
	return plan, nil
}

// Hash the immutable identity before normalization: case-sensitive Box IDs must
// not collapse onto the same node-agent root/worker key.
func WatchSafeRootKey(boxID, rootRef string) string {
	if rootRef == "" || rootRef == "box" {
		return boxWatchSafeRootKey
	}
	return fmt.Sprintf("loom_box_id_%x", sha256.Sum256([]byte(boxID)))
}

func namespaceBoxWatchItem(item projectcontracts.ProjectWatchedRootItem, boxID string) (projectcontracts.ProjectWatchedRootItem, error) {
	var cfg agentwatchedroots.RootConfig
	if err := json.Unmarshal(item.ConfigJSON, &cfg); err != nil {
		return item, err
	}
	prefix := WatchSafeRootKey(boxID, "box:"+boxID)
	cfg.RootKey = prefix + "__" + strings.TrimPrefix(cfg.RootKey, "loom_box__")
	if cfg.SafeRootKey == boxWatchSafeRootKey {
		cfg.SafeRootKey = prefix
	} else {
		cfg.SafeRootKey = prefix + "__" + cfg.SafeRootKey
	}
	validated, err := agentwatchedroots.ValidatePortableRootConfig(cfg)
	if err != nil {
		return item, err
	}
	item.BackendRootKey = validated.RootKey
	item.WorkerKey = noderuntime.WatchedRootWorkerKey(validated.RootKey)
	item.SafeRootKey = validated.SafeRootKey
	item.ConfigJSON, err = json.Marshal(validated)
	item.ConfigHash = agentwatchedroots.ConfigHash(validated)
	return item, err
}

func boxWatchExcludedAreas(contract *Contract) []WatchExcludedArea {
	excluded := []WatchExcludedArea{{Area: AreaProjects, Reason: "projects own their own project-level watch policies"}}
	if contract == nil {
		return excluded
	}
	if _, ok := contract.Areas[AreaLane]; ok {
		excluded = append(excluded, WatchExcludedArea{Area: AreaLane, Reason: "LOOM Lane is explicit custody transfer and is not a watched mirror"})
	}
	if _, ok := contract.Areas[AreaDropzone]; ok {
		excluded = append(excluded, WatchExcludedArea{Area: AreaDropzone, Reason: "legacy Dropzone custody transfer is not a watched mirror"})
	}
	return excluded
}

func watchRootItemForArea(contract Contract, area string) (projectcontracts.ProjectWatchedRootItem, []Diagnostic) {
	if area == areaTopics || area == areaLibrary {
		declared, ok := contract.Areas[area]
		if !ok || !declared.Enabled {
			return projectcontracts.ProjectWatchedRootItem{}, nil
		}
	}
	policyRel := contract.Policies[area]
	inline := contract.Areas[area].Watch
	if strings.TrimSpace(policyRel) == "" && inline == nil {
		return projectcontracts.ProjectWatchedRootItem{}, nil
	}
	policyPath := filepath.Join(contract.RootPath, filepath.FromSlash(policyRel))
	var policy WatchPolicy
	var diagnostics []Diagnostic
	if inline != nil {
		policyRel = contractRelPath
		policyPath = ContractPath(contract.RootPath)
		if contract.SchemaVersion != FolderPolicySchemaVersion || contract.Policies[area] != "" || !isBoxWatchArea(area) {
			return projectcontracts.ProjectWatchedRootItem{}, []Diagnostic{{Severity: "error", Code: "box.watch.inline_invalid", Message: "inline watch requires v0.5 and exactly one policy source for a watched area", Path: policyPath}}
		}
		if !contract.Areas[area].Enabled {
			return projectcontracts.ProjectWatchedRootItem{}, nil
		}
		policy = inlineWatchPolicy(*inline, area)
		diagnostics = validateWatchPolicy(policy, area, policyPath)
		if policy.Path != "" && policy.Path != contract.Areas[area].Path {
			diagnostics = append(diagnostics, Diagnostic{Severity: "error", Code: "box.watch.source_area_escape", Message: "inline watch path must match its declared Box area", Path: policyPath})
		}
	} else {
		policy, diagnostics = loadWatchPolicy(policyPath, area)
	}
	if hasErrorDiagnostics(diagnostics) || !policy.Enabled {
		return projectcontracts.ProjectWatchedRootItem{}, diagnostics
	}
	rootRelativePath := policy.Path
	if rootRelativePath == "" {
		rootRelativePath = contract.Areas[area].Path
	}
	if err := validateRelativePath(rootRelativePath); err != nil {
		diagnostics = append(diagnostics, Diagnostic{
			Severity:   "error",
			Code:       "box.watch.path_invalid",
			Message:    "Box watch policy path is invalid: " + err.Error(),
			Path:       policyPath,
			Suggestion: "use a relative path inside the Box",
		})
		return projectcontracts.ProjectWatchedRootItem{}, diagnostics
	}
	if area == areaTopics || area == areaLibrary {
		base := filepath.Clean(filepath.FromSlash(contract.Areas[area].Path))
		rel, err := filepath.Rel(base, filepath.Clean(filepath.FromSlash(rootRelativePath)))
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return projectcontracts.ProjectWatchedRootItem{}, append(diagnostics, Diagnostic{Severity: "error", Code: "box.watch.source_area_escape", Message: "knowledge source policy must stay inside its declared Box area", Path: policyPath})
		}
	}

	backendRootKey := boxWatchRootKey(area)
	displayName := boxWatchDisplayName(area)
	syncEnabled := policy.Sync.Enabled || policy.Object.Enabled || policy.Text.Enabled
	syncMode := agentwatchedroots.SyncModeNone
	if syncEnabled {
		syncMode = agentwatchedroots.SyncModeSelectedFiles
	}
	backupMode := agentwatchedroots.BackupModeNone
	if policy.Backup.Enabled {
		backupMode = agentwatchedroots.BackupModeIncrementalRaw
	}
	indexMode := agentwatchedroots.IndexModeNone
	if policy.Index.Enabled || policy.Text.Enabled {
		indexMode = agentwatchedroots.IndexModeMarkdownText
	} else if policy.Catalog.Enabled {
		indexMode = agentwatchedroots.IndexModeMetadataOnly
	}
	deleteMode := agentwatchedroots.DeleteModeLocalStateOnly
	if syncMode != agentwatchedroots.SyncModeNone {
		deleteMode = agentwatchedroots.DeleteModeTombstone
	}
	syncPolicy := agentwatchedroots.SyncPolicy{
		Mode:                syncMode,
		LogicalNameStrategy: agentwatchedroots.SyncLogicalNameRelativePath,
	}
	if syncMode != agentwatchedroots.SyncModeNone {
		syncPolicy.ScopeRef = boxWatchScopeRef(contract.BoxID, area)
	}

	config := agentwatchedroots.RootConfig{
		SchemaVersion:    agentwatchedroots.ConfigSchemaVersion,
		RootKey:          backendRootKey,
		DisplayName:      displayName,
		SafeRootKey:      boxWatchSafeRootKey,
		RootRelativePath: rootRelativePath,
		Include:          boxWatchIncludes(area),
		Exclude:          boxWatchExcludes(),
		IgnorePolicy: agentwatchedroots.IgnorePolicy{
			Profile:                string(filepolicy.ProfileManaged),
			DiscoverUserRules:      true,
			PolicyRootRelativePath: ".",
		},
		Watch: agentwatchedroots.WatchConfig{Enabled: true, Backend: agentwatchedroots.WatchBackendAuto, DebounceWindow: "2s"},
		Scan: agentwatchedroots.ScanConfig{
			FullRescanInterval: boxWatchFullRescanInterval,
			MaxHashFileBytes:   boxWatchMaxFileBytes(area),
			HiddenPolicy:       agentwatchedroots.HiddenPolicyPolicyControlled,
		},
		BackupPolicy: agentwatchedroots.BackupPolicy{
			Mode:          backupMode,
			MaxFileBytes:  boxWatchMaxFileBytes(area),
			MaxBatchBytes: boxWatchMaxFileBytes(area),
		},
		SyncPolicy:   syncPolicy,
		IndexPolicy:  agentwatchedroots.IndexPolicy{Mode: indexMode, MaxTextBytes: policy.Index.MaxTextBytes},
		DeletePolicy: agentwatchedroots.DeletePolicy{Mode: deleteMode},
	}
	if config.SyncPolicy.Mode != agentwatchedroots.SyncModeNone {
		config.SyncPolicy.MaxFileBytes = boxWatchSyncMaxFileBytes(area)
	}
	consumerFiles, err := watchConsumerFiles(contract.Files)
	if err != nil {
		return projectcontracts.ProjectWatchedRootItem{}, append(diagnostics, Diagnostic{Severity: "error", Code: "box.watch.files_invalid", Message: err.Error(), Path: ContractPath(contract.RootPath)})
	}
	if inline != nil || consumerFiles != nil {
		config = independentBoxConsumers(config, consumerFiles)
	}
	config, err = agentwatchedroots.ValidatePortableRootConfig(config)
	if err != nil {
		diagnostics = append(diagnostics, Diagnostic{
			Severity: "error",
			Code:     "box.watch.config_invalid",
			Message:  "compiled watched-root config is invalid: " + err.Error(),
			Path:     policyPath,
		})
		return projectcontracts.ProjectWatchedRootItem{}, diagnostics
	}
	configJSON, err := json.Marshal(config)
	if err != nil {
		diagnostics = append(diagnostics, Diagnostic{Severity: "error", Code: "box.watch.config_encode_failed", Message: err.Error(), Path: policyPath})
		return projectcontracts.ProjectWatchedRootItem{}, diagnostics
	}
	sourceKinds := []string{"box_policy", "box_" + area}
	metadata := map[string]any{
		"box_id":        contract.BoxID,
		"box_root_path": contract.RootPath,
		"box_area":      area,
		"policy":        policyRel,
		"semantics":     policy.Semantics,
		"catalog":       policy.Catalog.Enabled,
		"object":        policy.Object.Enabled,
		"text":          policy.Text.Enabled,
	}
	if policy.Metadata != nil {
		for key, value := range policy.Metadata {
			metadata[key] = value
		}
	}
	metadata["box_id"], metadata["box_root_path"], metadata["box_area"] = contract.BoxID, contract.RootPath, area
	if area == areaTopics || area == areaLibrary {
		// Compiler-owned classification cannot be supplied by free-form metadata.
		metadata["knowledge_source"] = map[string]any{
			"root_kind": "box_" + area, "category": area,
			"enabled":            policy.Enabled && (policy.Text.Enabled || policy.Index.Enabled) && syncEnabled,
			"root_relative_path": config.RootRelativePath,
			"include":            config.Include,
			"exclude":            config.Exclude,
		}
	}
	folderPolicy := contract.Files
	if folderPolicy != nil && len(folderPolicy.Rules) == 0 {
		folderPolicy = nil
	}
	if policy.Processing != nil || folderPolicy != nil {
		source, _ := metadata["knowledge_source"].(map[string]any)
		if source == nil {
			source = map[string]any{"include": config.Include, "exclude": config.Exclude}
			metadata["knowledge_source"] = source
		}
		source["policy"] = projectcontracts.KnowledgeSourcePolicy{Processing: policy.Processing, Files: folderPolicy}
	}
	return projectcontracts.ProjectWatchedRootItem{
		Key:              area,
		BackendRootKey:   backendRootKey,
		WorkerKey:        noderuntime.WatchedRootWorkerKey(backendRootKey),
		SourceKinds:      sourceKinds,
		OwnerNode:        contract.OwnerNode,
		SafeRootKey:      config.SafeRootKey,
		RootRelativePath: config.RootRelativePath,
		DisplayName:      config.DisplayName,
		Include:          append([]string{}, config.Include...),
		Exclude:          append([]string{}, config.Exclude...),
		SyncMode:         config.SyncPolicy.Mode,
		BackupMode:       config.BackupPolicy.Mode,
		IndexMode:        config.IndexPolicy.Mode,
		DeleteMode:       config.DeletePolicy.Mode,
		ConfigHash:       agentwatchedroots.ConfigHash(config),
		ConfigJSON:       json.RawMessage(configJSON),
		AgentCommands:    boxWatchCommands(contract.RootPath, []projectcontracts.ProjectWatchedRootItem{{BackendRootKey: backendRootKey}}),
		ActivationStatus: BoxWatchStatusPendingAgentApply,
		Metadata:         metadata,
	}, diagnostics
}

func loadWatchPolicy(path string, expectedArea string) (WatchPolicy, []Diagnostic) {
	payload, err := os.ReadFile(path)
	if err != nil {
		return WatchPolicy{}, []Diagnostic{{
			Severity:   "error",
			Code:       "box.watch.policy_unavailable",
			Message:    err.Error(),
			Path:       path,
			Suggestion: "run loom box init again if generated policy files are missing",
		}}
	}
	policy, err := decodeWatchPolicy(payload)
	if err != nil {
		return WatchPolicy{}, []Diagnostic{{Severity: "error", Code: "box.watch.policy_invalid_yaml", Message: err.Error(), Path: path}}
	}
	policy = normalizeWatchPolicy(policy)
	return policy, validateWatchPolicy(policy, expectedArea, path)
}

func decodeWatchPolicy(payload []byte) (WatchPolicy, error) {
	var policy WatchPolicy
	decoder := yaml.NewDecoder(bytes.NewReader(payload))
	decoder.KnownFields(true)
	if err := decoder.Decode(&policy); err != nil {
		return policy, err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return policy, fmt.Errorf("watch policy must contain exactly one YAML document")
	}
	var fields map[string]any
	if err := yaml.Unmarshal(payload, &fields); err != nil {
		return policy, err
	}
	raw, err := json.Marshal(fields)
	if err != nil {
		return policy, err
	}
	strict := json.NewDecoder(bytes.NewReader(raw))
	strict.DisallowUnknownFields()
	if err := strict.Decode(&policy); err != nil {
		return policy, err
	}
	return policy, nil
}

func validateWatchPolicy(policy WatchPolicy, expectedArea, path string) []Diagnostic {
	diagnostics := []Diagnostic{}
	if policy.SchemaVersion != WatchPolicySchemaVersion && policy.SchemaVersion != LegacyWatchPolicySchemaVersion {
		diagnostics = append(diagnostics, Diagnostic{
			Severity:   "error",
			Code:       "box.watch.schema_version_invalid",
			Message:    fmt.Sprintf("Box watch policy schema_version must be %s", WatchPolicySchemaVersion),
			Path:       path,
			Suggestion: "regenerate or update the policy file",
		})
	}
	if policy.Area != expectedArea {
		diagnostics = append(diagnostics, Diagnostic{
			Severity:   "error",
			Code:       "box.watch.area_mismatch",
			Message:    fmt.Sprintf("Box watch policy area must be %s", expectedArea),
			Path:       path,
			Suggestion: "keep each Box area policy in the matching .loom/policies file",
		})
	}
	if policy.Mode != "watched_root" {
		diagnostics = append(diagnostics, Diagnostic{
			Severity:   "error",
			Code:       "box.watch.mode_invalid",
			Message:    "Box watch policy mode must be watched_root",
			Path:       path,
			Suggestion: "Dropzone transfer policies are intentionally not watched-root policies",
		})
	}
	if !policy.Enabled {
		diagnostics = append(diagnostics, Diagnostic{Severity: "warning", Code: "box.watch.policy_disabled", Message: "Box watch policy is disabled", Path: path})
	}
	if policy.Processing != nil {
		if err := projectcontracts.ValidateKnowledgeProcessingPolicy(*policy.Processing); err != nil {
			diagnostics = append(diagnostics, Diagnostic{Severity: "error", Code: "box.watch.processing_invalid", Message: err.Error(), Path: path})
		}
	}
	if policy.Index.MaxTextBytes < 0 || policy.Index.MaxTextBytes > 8*1024*1024 {
		diagnostics = append(diagnostics, Diagnostic{Severity: "error", Code: "box.watch.index_limit_invalid", Message: "index.max_text_bytes must be between 1 and 8388608 when specified", Path: path})
	}
	return diagnostics
}

func boxWatchPlanAreas(contract Contract) []string {
	areas := []string{AreaNotes}
	for _, area := range []string{areaTopics, areaLibrary} {
		if hasAreaWatchSource(contract, area) {
			areas = append(areas, area)
		}
	}
	if hasAreaWatchSource(contract, AreaDocuments) {
		areas = append(areas, AreaDocuments)
	} else if hasAreaWatchSource(contract, AreaLaunchpad) {
		areas = append(areas, AreaLaunchpad)
	}
	return areas
}

func normalizeWatchPolicy(policy WatchPolicy) WatchPolicy {
	policy.SchemaVersion = strings.TrimSpace(policy.SchemaVersion)
	policy.Area = strings.ToLower(strings.TrimSpace(policy.Area))
	policy.Path = filepath.ToSlash(strings.TrimSpace(policy.Path))
	policy.Mode = strings.ToLower(strings.TrimSpace(policy.Mode))
	policy.Semantics = strings.TrimSpace(policy.Semantics)
	policy.Delete = strings.TrimSpace(policy.Delete)
	return policy
}

func boxWatchRootKey(area string) string {
	return "loom_box__" + strings.ToLower(strings.TrimSpace(area))
}

func boxWatchScopeRef(boxID, area string) string {
	boxID = strings.TrimSpace(boxID)
	if boxID == "" {
		boxID = "box"
	}
	area = strings.ToLower(strings.TrimSpace(area))
	if area == "" {
		area = "area"
	}
	return "loom_box:" + boxID + ":" + area
}

func boxWatchDisplayName(area string) string {
	switch area {
	case AreaNotes:
		return "LOOM Box Notes"
	case AreaDocuments:
		return "LOOM Box Documents"
	case AreaLaunchpad:
		return "LOOM Box Launchpad"
	default:
		return "LOOM Box " + strings.Title(area)
	}
}

func boxWatchIncludes(area string) []string {
	switch area {
	case AreaNotes:
		return []string{"**/*"}
	default:
		return []string{"**/*"}
	}
}

func boxWatchExcludes() []string {
	return nil
}

func boxWatchMaxFileBytes(area string) int64 {
	switch area {
	case AreaDocuments:
		return boxWatchDocsMaxFileBytes
	default:
		if filesystemconnector.DefaultMaxFileBytes > boxWatchNotesMaxFileBytes {
			return filesystemconnector.DefaultMaxFileBytes
		}
		return boxWatchNotesMaxFileBytes
	}
}

func boxWatchSyncMaxFileBytes(string) int64 {
	return int64(loomsync.MaxInlineObjectUploadBytes)
}

func boxWatchCommands(root string, roots []projectcontracts.ProjectWatchedRootItem) []projectcontracts.ProjectWatchedRootCommand {
	if len(roots) == 0 {
		return nil
	}
	return []projectcontracts.ProjectWatchedRootCommand{{
		Description: "Apply this Box watch plan on the owner node-agent",
		Command:     []string{"loom-node-agent", "watched-roots", "apply-plan", "/tmp/loom-box-watch-plan.json", "--project-root", root},
		Shell:       "loom-node-agent watched-roots apply-plan /tmp/loom-box-watch-plan.json --project-root " + shellQuotePlanPath(root),
	}}
}

func boxWatchPlanCommands(roots []projectcontracts.ProjectWatchedRootItem) []projects.ProjectWatchedRootCommand {
	commands := []projects.ProjectWatchedRootCommand{}
	for _, root := range roots {
		for _, command := range root.AgentCommands {
			commands = append(commands, projects.ProjectWatchedRootCommand{
				Description: command.Description,
				Command:     append([]string{}, command.Command...),
				Shell:       command.Shell,
			})
		}
	}
	return commands
}

func hasErrorDiagnostics(diagnostics []Diagnostic) bool {
	return countErrorDiagnostics(diagnostics) > 0
}

func countErrorDiagnostics(diagnostics []Diagnostic) int {
	count := 0
	for _, diagnostic := range diagnostics {
		if diagnostic.Severity == "error" {
			count++
		}
	}
	return count
}

func shellQuotePlanPath(path string) string {
	if path == "" {
		return "''"
	}
	if strings.ContainsAny(path, " \t\n'\"`$\\") {
		return "'" + strings.ReplaceAll(path, "'", "'\\''") + "'"
	}
	return path
}
