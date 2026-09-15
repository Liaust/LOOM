package storageexport

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"loom.local/loom/internal/filesystemmeta"
	"loom.local/loom/internal/storagecatalog"
	"loom.local/loom/internal/storageview"
)

const (
	RestoreModeSafe     = "safe"
	RestoreModeFaithful = "faithful"
	RestoreModeRaw      = "raw"
)

type RestorePlanInput struct {
	Ref         string
	Mode        string
	TargetPath  string
	ViewEntry   *storageview.ViewEntry
	EntryDetail storagecatalog.EntryDetail
	Now         time.Time
}

type RestorePlan struct {
	PlanID                string                                `json:"plan_id"`
	Ref                   string                                `json:"ref"`
	Mode                  string                                `json:"mode"`
	TargetPath            string                                `json:"target_path"`
	StorageEntryID        string                                `json:"storage_entry_id,omitempty"`
	ViewPath              string                                `json:"view_path,omitempty"`
	BytesToRestore        int64                                 `json:"bytes_to_restore"`
	DirectoriesToCreate   []string                              `json:"directories_to_create,omitempty"`
	SymlinksToRecreate    []RestoreSymlinkPlan                  `json:"symlinks_to_recreate,omitempty"`
	ModesToApply          []RestoreModePlan                     `json:"modes_to_apply,omitempty"`
	XattrsAvailable       []string                              `json:"xattrs_available,omitempty"`
	ACLsAvailable         bool                                  `json:"acls_available,omitempty"`
	ResourceForkAvailable bool                                  `json:"resource_fork_available,omitempty"`
	FinderTagsAvailable   bool                                  `json:"finder_tags_available,omitempty"`
	Risks                 []string                              `json:"risks,omitempty"`
	ManualConfirmations   []string                              `json:"manual_confirmations,omitempty"`
	Steps                 []RestorePlanStep                     `json:"steps"`
	CanApply              bool                                  `json:"can_apply"`
	RequiresConfirmation  bool                                  `json:"requires_confirmation"`
	SourceURI             string                                `json:"source_uri,omitempty"`
	SourceRefKind         string                                `json:"source_ref_kind,omitempty"`
	Observation           *storagecatalog.FilesystemObservation `json:"filesystem_observation,omitempty"`
	CreatedAt             time.Time                             `json:"created_at"`
}

type RestorePlanStep struct {
	Kind    string `json:"kind"`
	Status  string `json:"status"`
	Path    string `json:"path,omitempty"`
	Source  string `json:"source,omitempty"`
	Summary string `json:"summary"`
}

type RestoreSymlinkPlan struct {
	Path   string `json:"path"`
	Target string `json:"target"`
}

type RestoreModePlan struct {
	Path       string `json:"path"`
	SourceMode int    `json:"source_mode,omitempty"`
	SafeMode   int    `json:"safe_mode,omitempty"`
	ApplyMode  int    `json:"apply_mode,omitempty"`
}

type RestoreApplyResult struct {
	PlanID     string            `json:"plan_id"`
	Applied    bool              `json:"applied"`
	Refused    bool              `json:"refused,omitempty"`
	Refusal    string            `json:"refusal,omitempty"`
	TargetPath string            `json:"target_path"`
	Steps      []RestorePlanStep `json:"steps,omitempty"`
	AppliedAt  time.Time         `json:"applied_at,omitempty"`
}

func PlanRestore(input RestorePlanInput) (RestorePlan, error) {
	input.Ref = strings.TrimSpace(input.Ref)
	input.TargetPath = strings.TrimSpace(input.TargetPath)
	if input.TargetPath == "" {
		return RestorePlan{}, fmt.Errorf("target path is required")
	}
	mode := normalizeRestoreMode(input.Mode)
	now := input.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	detail := input.EntryDetail
	entry := detail.Entry
	observation, hasObservation := observationForDetail(detail)
	ref, sourcePath, hasSource := chooseSourceRef(detail)
	plan := RestorePlan{
		Ref:                  input.Ref,
		Mode:                 mode,
		TargetPath:           filepath.Clean(input.TargetPath),
		StorageEntryID:       entry.StorageEntryID,
		BytesToRestore:       int64Value(entry.SizeBytes),
		CanApply:             hasSource || mode == RestoreModeFaithful && hasObservation && observation.ObjectKind == filesystemmeta.ObjectKindSymlink,
		RequiresConfirmation: mode != RestoreModeSafe,
		CreatedAt:            now,
	}
	if input.ViewEntry != nil {
		plan.ViewPath = input.ViewEntry.ViewPath
	}
	if hasSource {
		plan.SourceURI = ref.URI
		plan.SourceRefKind = ref.RefKind
	}
	if hasObservation {
		plan.Observation = &observation
		plan.XattrsAvailable = append([]string(nil), observation.XattrNames...)
		plan.ACLsAvailable = observation.HasACL
		plan.ResourceForkAvailable = observation.HasResourceFork
		plan.FinderTagsAvailable = observation.HasFinderTags
		plan.Risks = append(plan.Risks, observation.Risks...)
		if observation.SourceMode != nil {
			sourceMode := *observation.SourceMode
			safeMode := int(exportFileMode(os.FileMode(sourceMode)))
			applyMode := safeMode
			if mode == RestoreModeFaithful {
				applyMode = sourceMode & 0o777
			}
			plan.ModesToApply = append(plan.ModesToApply, RestoreModePlan{
				Path:       plan.TargetPath,
				SourceMode: sourceMode,
				SafeMode:   safeMode,
				ApplyMode:  applyMode,
			})
		}
	}
	plan.DirectoriesToCreate = append(plan.DirectoriesToCreate, filepath.Dir(plan.TargetPath))
	addMetadataSteps(&plan, observation, hasObservation, mode)
	switch {
	case hasObservation && observation.ObjectKind == filesystemmeta.ObjectKindSymlink:
		if mode == RestoreModeFaithful {
			plan.SymlinksToRecreate = append(plan.SymlinksToRecreate, RestoreSymlinkPlan{Path: plan.TargetPath, Target: observation.SymlinkTarget})
			plan.Steps = append(plan.Steps, RestorePlanStep{Kind: "symlink", Status: "would_recreate", Path: plan.TargetPath, Source: observation.SymlinkTarget, Summary: "Faithful restore will recreate the source symlink."})
		} else {
			plan.Steps = append(plan.Steps, RestorePlanStep{Kind: "sidecar", Status: "would_write", Path: plan.TargetPath + ".loom-meta.json", Summary: "Safe/raw restore represents the source symlink as metadata unless faithful mode is selected."})
		}
	case hasObservation && observation.ObjectKind == filesystemmeta.ObjectKindSpecial:
		plan.CanApply = false
		plan.Steps = append(plan.Steps, RestorePlanStep{Kind: "special_file", Status: "manual_action_required", Path: plan.TargetPath, Summary: "Special filesystem entries are not recreated automatically."})
	case hasObservation && (observation.IsPackage || observation.ObjectKind == filesystemmeta.ObjectKindPackage) && !hasSource:
		plan.CanApply = false
		plan.Risks = appendUnique(plan.Risks, "package_payload_incomplete")
		plan.Steps = append(plan.Steps, RestorePlanStep{Kind: "package", Status: "blocked", Path: plan.TargetPath, Summary: "Package directory restore is blocked because LOOM does not have a complete payload."})
	case hasSource:
		status := "would_copy_safe"
		if mode == RestoreModeFaithful {
			status = "would_copy_faithful"
		} else if mode == RestoreModeRaw {
			status = "would_copy_raw"
		}
		plan.Steps = append(plan.Steps, RestorePlanStep{Kind: "file", Status: status, Path: plan.TargetPath, Source: sourcePath, Summary: "Restore payload from the selected local physical ref."})
	default:
		plan.CanApply = false
		plan.Steps = append(plan.Steps, RestorePlanStep{Kind: "source", Status: "missing", Path: plan.TargetPath, Summary: "No available local physical ref was found for this entry."})
	}
	plan.PlanID = restorePlanID(plan)
	return plan, nil
}

func ApplyRestorePlan(ctx context.Context, plan RestorePlan, yes bool) (RestoreApplyResult, error) {
	if !yes {
		return RestoreApplyResult{PlanID: plan.PlanID, Refused: true, Refusal: "restore apply requires --yes", TargetPath: plan.TargetPath}, nil
	}
	if !plan.CanApply {
		return RestoreApplyResult{PlanID: plan.PlanID, Refused: true, Refusal: "restore plan is not directly applicable", TargetPath: plan.TargetPath, Steps: plan.Steps}, nil
	}
	if err := ctx.Err(); err != nil {
		return RestoreApplyResult{}, err
	}
	if err := os.MkdirAll(filepath.Dir(plan.TargetPath), 0o755); err != nil {
		return RestoreApplyResult{}, err
	}
	if len(plan.SymlinksToRecreate) > 0 {
		link := plan.SymlinksToRecreate[0]
		if err := os.RemoveAll(link.Path); err != nil {
			return RestoreApplyResult{}, err
		}
		if err := os.Symlink(link.Target, link.Path); err != nil {
			return RestoreApplyResult{}, err
		}
		return RestoreApplyResult{PlanID: plan.PlanID, Applied: true, TargetPath: plan.TargetPath, Steps: plan.Steps, AppliedAt: time.Now().UTC()}, nil
	}
	sourcePath, ok := localPathFromURI(plan.SourceURI)
	if !ok {
		sourcePath = plan.SourceURI
	}
	if sourcePath == "" {
		return RestoreApplyResult{}, fmt.Errorf("restore plan has no source path")
	}
	if err := copyRestorePayload(sourcePath, plan.TargetPath); err != nil {
		return RestoreApplyResult{}, err
	}
	for _, mode := range plan.ModesToApply {
		if mode.Path == plan.TargetPath && mode.ApplyMode > 0 {
			_ = os.Chmod(plan.TargetPath, os.FileMode(mode.ApplyMode))
		}
	}
	return RestoreApplyResult{PlanID: plan.PlanID, Applied: true, TargetPath: plan.TargetPath, Steps: plan.Steps, AppliedAt: time.Now().UTC()}, nil
}

func WriteRestorePlan(path string, plan RestorePlan) error {
	payload, err := json.MarshalIndent(plan, "", "  ")
	if err != nil {
		return err
	}
	payload = append(payload, '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, payload, 0o600)
}

func ReadRestorePlan(path string) (RestorePlan, error) {
	payload, err := os.ReadFile(path)
	if err != nil {
		return RestorePlan{}, err
	}
	var plan RestorePlan
	if err := json.Unmarshal(payload, &plan); err != nil {
		return RestorePlan{}, err
	}
	return plan, nil
}

func DefaultRestorePlanPath(planID string) string {
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		home = "."
	}
	return filepath.Join(home, ".loom", "storage-restore-plans", strings.TrimSpace(planID)+".json")
}

func ResolveRestorePlanPath(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return value
	}
	if strings.Contains(value, string(os.PathSeparator)) || strings.HasSuffix(value, ".json") {
		return value
	}
	return DefaultRestorePlanPath(value)
}

func normalizeRestoreMode(mode string) string {
	switch strings.TrimSpace(strings.ToLower(mode)) {
	case RestoreModeFaithful:
		return RestoreModeFaithful
	case RestoreModeRaw:
		return RestoreModeRaw
	default:
		return RestoreModeSafe
	}
}

func addMetadataSteps(plan *RestorePlan, observation storagecatalog.FilesystemObservation, ok bool, mode string) {
	if !ok {
		return
	}
	if observation.Executable {
		status := "not_applied_in_safe_mode"
		if mode == RestoreModeFaithful {
			status = "would_apply_in_faithful_mode"
		}
		plan.Steps = append(plan.Steps, RestorePlanStep{Kind: "mode", Status: status, Path: plan.TargetPath, Summary: "Source executable bit is recorded separately from safe-view materialization."})
	}
	if observation.HasXattrs || observation.HasACL || observation.HasResourceFork || observation.HasFinderTags {
		plan.Steps = append(plan.Steps, RestorePlanStep{Kind: "metadata", Status: "available_not_applied_by_default", Path: plan.TargetPath, Summary: "Extended metadata is known but requires faithful/raw handling support on this platform."})
	}
}

func restorePlanID(plan RestorePlan) string {
	raw, _ := json.Marshal(map[string]any{
		"ref":              plan.Ref,
		"mode":             plan.Mode,
		"target_path":      plan.TargetPath,
		"storage_entry_id": plan.StorageEntryID,
		"source_uri":       plan.SourceURI,
	})
	sum := sha256.Sum256(raw)
	return "restore_" + hex.EncodeToString(sum[:])[:24]
}

func int64Value(value *int64) int64 {
	if value == nil {
		return 0
	}
	return *value
}

func appendUnique(values []string, value string) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return values
	}
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func observationForDetail(detail storagecatalog.EntryDetail) (storagecatalog.FilesystemObservation, bool) {
	for _, raw := range append([]json.RawMessage{detail.Entry.Metadata}, physicalRefMetadata(detail.PhysicalRefs)...) {
		var object map[string]json.RawMessage
		if len(raw) == 0 || json.Unmarshal(raw, &object) != nil {
			continue
		}
		for _, key := range []string{"filesystem_observation", "fidelity", "observation"} {
			var observation storagecatalog.FilesystemObservation
			if payload := object[key]; len(payload) > 0 && json.Unmarshal(payload, &observation) == nil && filesystemObservationHasSignal(observation) {
				return observation, true
			}
		}
	}
	return storagecatalog.FilesystemObservation{}, false
}

func physicalRefMetadata(refs []storagecatalog.PhysicalRef) []json.RawMessage {
	out := make([]json.RawMessage, 0, len(refs))
	for _, ref := range refs {
		out = append(out, ref.Metadata)
	}
	return out
}

func filesystemObservationHasSignal(observation storagecatalog.FilesystemObservation) bool {
	return observation.ObjectKind != "" || observation.SymlinkTarget != "" || observation.SourceMode != nil ||
		observation.HasXattrs || observation.HasACL || observation.HasResourceFork || observation.HasFinderTags ||
		observation.IsPackage || observation.GeneratedMetadata || observation.PermissionDenied || len(observation.Risks) > 0
}

func chooseSourceRef(detail storagecatalog.EntryDetail) (storagecatalog.PhysicalRef, string, bool) {
	for _, ref := range storagecatalog.ResolvePhysicalRefs(detail.PhysicalRefs, storagecatalog.ResolvePhysicalRefOptions{LocalOnly: true}) {
		sourcePath, ok := storagecatalog.PhysicalRefLocalPath(ref)
		if !ok {
			continue
		}
		info, err := os.Stat(sourcePath)
		if err == nil && !info.IsDir() {
			return ref, sourcePath, true
		}
	}
	return storagecatalog.PhysicalRef{}, "", false
}

func localPathFromURI(raw string) (string, bool) {
	return storagecatalog.PhysicalRefLocalPath(storagecatalog.PhysicalRef{RefKind: storagecatalog.PhysicalRefKindLocalPath, URI: raw})
}

func exportFileMode(mode os.FileMode) os.FileMode {
	mode &= 0o777
	if mode == 0 {
		mode = 0o640
	}
	mode &^= 0o111
	mode &^= 0o002
	mode |= 0o440
	return mode
}

func copyRestorePayload(sourcePath, targetPath string) error {
	source, err := os.Open(sourcePath)
	if err != nil {
		return err
	}
	defer source.Close()
	target, err := os.OpenFile(targetPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o640)
	if err != nil {
		return err
	}
	if _, err := io.Copy(target, source); err != nil {
		_ = target.Close()
		return err
	}
	return target.Close()
}
