package box

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"loom.local/loom/internal/dropzone"
	"loom.local/loom/internal/lane"
)

func Inspect(resolved Resolved) Status {
	expected := DefaultContract(resolved)
	runtimeState, runtimeStateErr := ResolveRuntimeState(resolved)
	status := Status{
		SchemaVersion:      SchemaVersion,
		RootPath:           resolved.RootPath,
		PathSource:         resolved.PathSource,
		Profile:            resolved.Profile,
		ProfileSource:      resolved.ProfileSource,
		OwnerNode:          resolved.OwnerNode,
		NodeRole:           resolved.NodeRole,
		RuntimeStateRoot:   resolved.RuntimeStateRoot,
		State:              "missing",
		Initialized:        false,
		ContractPath:       ContractPath(resolved.RootPath),
		ContractState:      "missing",
		DefaultProjectPath: filepath.Join(resolved.RootPath, filepath.FromSlash(expected.DefaultProjectPath)),
		LaneState:          lane.StateNotInitialized,
		DropzoneState:      "not_initialized",
		InspectedAt:        time.Now().UTC(),
	}
	if runtimeStateErr != nil {
		status.State = "invalid"
		status.RuntimeStateSource = "conflict"
		status.Diagnostics = append(status.Diagnostics, Diagnostic{
			Severity:   "error",
			Code:       "box.runtime_state_divergent",
			Message:    runtimeStateErr.Error(),
			Suggestion: "run the dry-run Box-state migration plan and reconcile the conflicting records explicitly",
		})
		// Do not invoke any child status reader with an empty state root. In
		// particular, filepath.Join("", "lane") would silently inspect a
		// process-relative path after a divergent-root failure.
		return status
	} else {
		status.RuntimeStateReadRoot = runtimeState.ReadRoot
		status.RuntimeStateWriteRoot = runtimeState.WriteRoot
		status.RuntimeStateRoot = runtimeState.WriteRoot
		status.RuntimeStateSource = runtimeState.Source
		status.RuntimeStateMigrationRequired = runtimeState.MigrationRequired
	}

	contract := expected
	if loaded, err := LoadContract(status.ContractPath); err == nil {
		status.Contract = &loaded
		status.ContractState = "valid"
		status.Initialized = true
		contract = loaded
		status.Profile = loaded.Profile
		status.OwnerNode = loaded.OwnerNode
		status.DefaultProjectPath = filepath.Join(loaded.RootPath, filepath.FromSlash(loaded.DefaultProjectPath))
		if filepath.Clean(loaded.RootPath) != filepath.Clean(resolved.RootPath) {
			status.Diagnostics = append(status.Diagnostics, Diagnostic{
				Severity:   "warning",
				Code:       "box.root_path_mismatch",
				Message:    "box contract root_path differs from the resolved Box path",
				Path:       status.ContractPath,
				Suggestion: "inspect the Box path or re-initialize the Box contract intentionally",
			})
		}
	} else if errors.Is(err, os.ErrNotExist) {
		status.ContractState = "missing"
	} else {
		status.ContractState = "invalid"
		status.State = "invalid"
		status.Diagnostics = append(status.Diagnostics, Diagnostic{
			Severity:   "error",
			Code:       "box.contract_invalid",
			Message:    err.Error(),
			Path:       status.ContractPath,
			Suggestion: "fix .loom/box.yaml or run loom box init once initialization is available",
		})
	}

	status.Areas = inspectPaths(ExpectedDirectories(contract))
	status.Policies = inspectPaths(ExpectedPolicyFiles(contract))
	status.Lane = buildLaneStatus(status, contract)
	status.LaneState = laneState(status.Lane, status.Initialized, contract)
	status.DropzoneState = dropzoneState(contract, status.Initialized)
	status.DropzoneTransfers = buildDropzoneTransferStatus(status, contract)
	status.WouldCreateFolders = missingPaths(status.Areas)
	status.WouldCreateFiles = missingPaths(status.Policies)

	if status.State != "invalid" {
		status.State = deriveState(status)
	}
	return status
}

func buildLaneStatus(status Status, contract Contract) *lane.Status {
	if !status.Initialized || status.ContractState != "valid" {
		return nil
	}
	area, ok := contract.Areas[AreaLane]
	if !ok {
		return nil
	}
	relPath := area.Path
	if strings.TrimSpace(relPath) == "" {
		relPath = lane.DefaultLaneRelPath
	}
	laneStatus := lane.BuildStatus(lane.StatusInput{
		RootPath:    contract.RootPath,
		LaneRelPath: relPath,
		StatePath:   filepath.Join(status.RuntimeStateReadRoot, "lane"),
		MainHost:    lane.DefaultMainHost,
	})
	return &laneStatus
}

func laneState(status *lane.Status, initialized bool, contract Contract) string {
	if !initialized {
		return lane.StateNotInitialized
	}
	if _, ok := contract.Areas[AreaLane]; !ok {
		return ""
	}
	if status == nil {
		return lane.StateMissing
	}
	return status.State
}

func buildDropzoneTransferStatus(status Status, contract Contract) *dropzone.Status {
	if !status.Initialized || status.ContractState != "valid" {
		return nil
	}
	if _, ok := contract.Areas[AreaDropzone]; !ok {
		return nil
	}
	policyRel := contract.Policies[AreaDropzone]
	if strings.TrimSpace(policyRel) == "" {
		return nil
	}
	policyPath := filepath.Join(contract.RootPath, filepath.FromSlash(policyRel))
	policy, diagnostics := dropzone.LoadPolicy(policyPath, status.Profile)
	dzStatus := dropzone.BuildStatus(dropzone.StatusInput{
		RootPath:    contract.RootPath,
		Profile:     status.Profile,
		Policy:      policy,
		StateRoot:   filepath.Join(status.RuntimeStateReadRoot, "dropzone"),
		Diagnostics: diagnostics,
	})
	return &dzStatus
}

func inspectPaths(expected []PathStatus) []PathStatus {
	result := make([]PathStatus, 0, len(expected))
	for _, item := range expected {
		info, err := os.Stat(item.Path)
		if errors.Is(err, os.ErrNotExist) {
			item.Exists = false
			item.IsDir = false
			item.Status = "missing"
			result = append(result, item)
			continue
		}
		if err != nil {
			item.Exists = false
			item.IsDir = false
			item.Status = "error"
			result = append(result, item)
			continue
		}
		item.Exists = true
		item.IsDir = info.IsDir()
		switch item.Kind {
		case "directory":
			if info.IsDir() {
				item.Status = "ok"
			} else {
				item.Status = "blocked_by_file"
			}
		case "file":
			if info.Mode().IsRegular() {
				item.Status = "ok"
			} else if info.IsDir() {
				item.Status = "blocked_by_directory"
			} else {
				item.Status = "not_regular"
			}
		default:
			item.Status = "unknown_kind"
		}
		result = append(result, item)
	}
	return result
}

func deriveState(status Status) string {
	if status.ContractState == "missing" && len(status.WouldCreateFolders) == len(status.Areas) && len(status.WouldCreateFiles) == len(status.Policies) {
		return "missing"
	}
	for _, item := range append(append([]PathStatus{}, status.Areas...), status.Policies...) {
		if item.Status != "ok" {
			return "partial"
		}
	}
	if status.ContractState == "valid" {
		return "ok"
	}
	return "partial"
}

func dropzoneState(contract Contract, initialized bool) string {
	if !initialized {
		return "not_initialized"
	}
	area, ok := contract.Areas[AreaDropzone]
	if !ok {
		return ""
	}
	if strings.TrimSpace(area.TransferStatus) != "" {
		return area.TransferStatus
	}
	if area.Enabled {
		return DropzoneTransferFuture
	}
	return DropzoneTransferInactive
}

func missingPaths(items []PathStatus) []string {
	paths := []string{}
	for _, item := range items {
		if item.Status == "missing" {
			paths = append(paths, item.Path)
		}
	}
	return paths
}
