package cloudstorage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path"
	"sort"
	"strings"
	"time"

	"loom.local/loom/internal/backupstrategy"
)

const (
	DefaultSnapshotKeepLatest = 14
	DefaultRetentionDaily     = 14
	DefaultRetentionWeekly    = 8
	DefaultRetentionMonthly   = 6

	SnapshotRetentionPlanSchema = "loom.cloud.borg_retention_plan.v1"

	RetentionStatusEmpty                = "empty"
	RetentionStatusPlanned              = "planned"
	RetentionStatusSucceeded            = "succeeded"
	RetentionStatusPartial              = "partial"
	RetentionStatusConfirmationRequired = "confirmation_required"
	RetentionStatusRefused              = "refused"

	RetentionActionKeep   = "keep"
	RetentionActionMove   = "move_to_trash"
	RetentionActionDelete = "delete_archive"
	RetentionActionPrune  = "prune_archive"
	RetentionActionIgnore = "ignore"

	RetentionStagePending   = "pending"
	RetentionStageSucceeded = "succeeded"
	RetentionStageSkipped   = "skipped"
	RetentionStageFailed    = "failed"
)

type SnapshotRetentionInput struct {
	Config     Config
	Driver     Driver
	NodeID     string
	StateDir   string
	KeepLatest int
	Now        func() time.Time
}

type SnapshotRetentionApplyInput struct {
	SnapshotRetentionInput
	Plan          SnapshotRetentionPlan `json:"plan"`
	Confirm       bool                  `json:"confirm"`
	ConfirmDigest string                `json:"confirm_digest"`
	Compact       bool                  `json:"compact"`
}

type SnapshotRetentionPolicy struct {
	Daily   int `json:"daily"`
	Weekly  int `json:"weekly"`
	Monthly int `json:"monthly"`
}

type SnapshotRetentionArchive struct {
	Archive        string    `json:"archive"`
	Ref            string    `json:"ref,omitempty"`
	NodeID         string    `json:"node_id,omitempty"`
	ArchiveClass   string    `json:"archive_class"`
	CreatedAt      time.Time `json:"created_at"`
	ManifestSHA256 string    `json:"manifest_sha256,omitempty"`
	Authenticated  bool      `json:"authenticated"`
}

type SnapshotRetentionPlan struct {
	Schema          string                      `json:"schema,omitempty"`
	Status          string                      `json:"status"`
	NodeID          string                      `json:"node_id"`
	Backend         string                      `json:"backend,omitempty"`
	Repository      string                      `json:"repository,omitempty"`
	RepositoryID    string                      `json:"repository_id,omitempty"`
	RemoteRoot      string                      `json:"remote_root"`
	KeepLatest      int                         `json:"keep_latest"`
	Policy          SnapshotRetentionPolicy     `json:"policy"`
	ArchiveGlob     string                      `json:"archive_glob,omitempty"`
	Inventory       []SnapshotRetentionArchive  `json:"inventory,omitempty"`
	InventoryDigest string                      `json:"inventory_digest,omitempty"`
	PlanDigest      string                      `json:"plan_digest,omitempty"`
	RepositoryCheck string                      `json:"repository_check,omitempty"`
	PlannedAt       time.Time                   `json:"planned_at"`
	LatestKeptRef   string                      `json:"latest_kept_ref,omitempty"`
	Kept            []SnapshotRetentionDecision `json:"kept"`
	Remove          []SnapshotRetentionDecision `json:"remove"`
	Ignored         []SnapshotRetentionDecision `json:"ignored"`
}

type SnapshotRetentionDecision struct {
	Ref               string    `json:"ref"`
	Backend           string    `json:"backend,omitempty"`
	Repository        string    `json:"repository,omitempty"`
	Archive           string    `json:"archive,omitempty"`
	ArchiveClass      string    `json:"archive_class,omitempty"`
	ManifestSHA256    string    `json:"manifest_sha256,omitempty"`
	RemotePrefix      string    `json:"remote_prefix"`
	RemoteURI         string    `json:"remote_uri"`
	Action            string    `json:"action"`
	Reason            string    `json:"reason"`
	UploadID          string    `json:"upload_id,omitempty"`
	BackupOperationID string    `json:"backup_operation_id,omitempty"`
	CompletedAt       time.Time `json:"completed_at,omitempty"`
	FileCount         int64     `json:"file_count,omitempty"`
	TotalBytes        int64     `json:"total_bytes,omitempty"`
}

type SnapshotRetentionApplyResult struct {
	Status         string                       `json:"status"`
	Plan           SnapshotRetentionPlan        `json:"plan"`
	Moved          []SnapshotRetentionMove      `json:"moved"`
	Errors         []SnapshotRetentionMoveError `json:"errors,omitempty"`
	Prune          SnapshotRetentionStage       `json:"prune"`
	Compact        SnapshotRetentionStage       `json:"compact"`
	ReclaimedBytes *int64                       `json:"reclaimed_bytes,omitempty"`
	AppliedAt      time.Time                    `json:"applied_at"`
}

type SnapshotRetentionStage struct {
	Status       string `json:"status"`
	Command      string `json:"command,omitempty"`
	Output       string `json:"output,omitempty"`
	Verification string `json:"verification,omitempty"`
	Error        string `json:"error,omitempty"`
}

type SnapshotRetentionMove struct {
	Ref           string `json:"ref"`
	Backend       string `json:"backend,omitempty"`
	Repository    string `json:"repository,omitempty"`
	Archive       string `json:"archive,omitempty"`
	FromPrefix    string `json:"from_prefix"`
	ToPrefix      string `json:"to_prefix"`
	FromRemoteURI string `json:"from_remote_uri"`
	ToRemoteURI   string `json:"to_remote_uri"`
}

type SnapshotRetentionMoveError struct {
	Ref     string `json:"ref"`
	Archive string `json:"archive,omitempty"`
	Prefix  string `json:"prefix"`
	Error   string `json:"error"`
}

type validRetentionSnapshot struct {
	Item     SnapshotItem
	Verify   SnapshotVerifyResult
	Manifest SnapshotUploadManifest
}

func PlanSnapshotRetention(ctx context.Context, input SnapshotRetentionInput) (SnapshotRetentionPlan, error) {
	cfg, err := NormalizeConfig(input.Config)
	if err != nil {
		return SnapshotRetentionPlan{}, err
	}
	if !cfg.Enabled {
		return SnapshotRetentionPlan{}, fmt.Errorf("cloud storage is disabled")
	}
	backend, err := resolveSnapshotBackend(cfg, input.Driver)
	if err != nil {
		return SnapshotRetentionPlan{}, err
	}
	input.Config = cfg
	return backend.PlanRetention(ctx, input)
}

func planLegacyTreeSnapshotRetention(ctx context.Context, input SnapshotRetentionInput) (SnapshotRetentionPlan, error) {
	now := normalizeNow(input.Now)
	cfg, err := NormalizeConfig(input.Config)
	if err != nil {
		return SnapshotRetentionPlan{}, err
	}
	if !cfg.Enabled {
		return SnapshotRetentionPlan{}, fmt.Errorf("cloud storage is disabled")
	}
	driver, err := normalizeDriver(cfg, input.Driver)
	if err != nil {
		return SnapshotRetentionPlan{}, err
	}
	nodeID := safeRemoteSegment(firstNonEmpty(input.NodeID, "main"))
	keepLatest := normalizeKeepLatest(input.KeepLatest)
	list, err := ListSnapshots(ctx, SnapshotListInput{Config: cfg, Driver: driver, NodeID: nodeID})
	if err != nil {
		return SnapshotRetentionPlan{}, err
	}
	plan := SnapshotRetentionPlan{
		Status:     RetentionStatusEmpty,
		NodeID:     nodeID,
		Backend:    SnapshotBackendLegacyTree,
		RemoteRoot: list.RemoteRoot,
		KeepLatest: keepLatest,
		PlannedAt:  now(),
	}
	if len(list.Snapshots) == 0 {
		return plan, nil
	}

	valid := make([]validRetentionSnapshot, 0, len(list.Snapshots))
	for _, item := range list.Snapshots {
		verify, err := VerifySnapshot(ctx, SnapshotVerifyInput{
			Config:   cfg,
			Driver:   driver,
			NodeID:   nodeID,
			Ref:      item.Ref,
			StateDir: firstNonEmpty(input.StateDir, cfg.StateDir),
			Now:      now,
		})
		if err != nil {
			plan.Ignored = append(plan.Ignored, retentionIgnored(item, err.Error()))
			continue
		}
		if verify.Status != SnapshotStatusSucceeded {
			plan.Ignored = append(plan.Ignored, retentionIgnored(item, strings.Join(verify.Errors, "; ")))
			continue
		}
		valid = append(valid, validRetentionSnapshot{Item: item, Verify: verify, Manifest: verify.Manifest})
	}
	if len(valid) == 0 {
		return plan, nil
	}
	sort.SliceStable(valid, func(i, j int) bool {
		left := valid[i].Manifest.CompletedAt
		right := valid[j].Manifest.CompletedAt
		switch {
		case left.IsZero() && right.IsZero():
			return valid[i].Item.Ref < valid[j].Item.Ref
		case left.IsZero():
			return true
		case right.IsZero():
			return false
		case left.Equal(right):
			return valid[i].Item.Ref < valid[j].Item.Ref
		default:
			return left.Before(right)
		}
	})
	latestIndex := len(valid) - 1
	plan.LatestKeptRef = valid[latestIndex].Item.Ref
	cutoff := len(valid) - keepLatest
	if cutoff < 0 {
		cutoff = 0
	}
	for idx, snapshot := range valid {
		decision := retentionDecision(snapshot)
		if idx < cutoff && idx != latestIndex {
			decision.Action = RetentionActionMove
			decision.Reason = fmt.Sprintf("older than latest %d valid snapshots", keepLatest)
			plan.Remove = append(plan.Remove, decision)
			continue
		}
		decision.Action = RetentionActionKeep
		if idx == latestIndex {
			decision.Reason = "latest successful snapshot is always retained"
		} else {
			decision.Reason = fmt.Sprintf("within latest %d valid snapshots", keepLatest)
		}
		plan.Kept = append(plan.Kept, decision)
	}
	plan.Status = RetentionStatusPlanned
	return plan, nil
}

func ApplySnapshotRetention(ctx context.Context, input SnapshotRetentionApplyInput) (SnapshotRetentionApplyResult, error) {
	cfg, err := NormalizeConfig(input.Config)
	if err != nil {
		return SnapshotRetentionApplyResult{}, err
	}
	if !cfg.Enabled {
		return SnapshotRetentionApplyResult{}, fmt.Errorf("cloud storage is disabled")
	}
	backend, err := resolveSnapshotBackend(cfg, input.Driver)
	if err != nil {
		return SnapshotRetentionApplyResult{}, err
	}
	input.Config = cfg
	input.SnapshotRetentionInput.Config = cfg
	return backend.ApplyRetention(ctx, input)
}

func applyLegacyTreeSnapshotRetention(ctx context.Context, input SnapshotRetentionApplyInput) (SnapshotRetentionApplyResult, error) {
	now := normalizeNow(input.Now)
	plan, err := planLegacyTreeSnapshotRetention(ctx, input.SnapshotRetentionInput)
	if err != nil {
		return SnapshotRetentionApplyResult{}, err
	}
	result := SnapshotRetentionApplyResult{
		Status:    RetentionStatusConfirmationRequired,
		Plan:      plan,
		AppliedAt: now(),
	}
	if !input.Confirm {
		return result, nil
	}
	cfg, err := NormalizeConfig(input.Config)
	if err != nil {
		return SnapshotRetentionApplyResult{}, err
	}
	driver, err := normalizeDriver(cfg, input.Driver)
	if err != nil {
		return SnapshotRetentionApplyResult{}, err
	}
	mover, ok := driver.(RemoteMover)
	if !ok {
		return SnapshotRetentionApplyResult{}, fmt.Errorf("configured cloud driver does not support remote retention moves")
	}
	result.Status = RetentionStatusSucceeded
	for _, decision := range plan.Remove {
		if err := ensureSnapshotPrefix(cfg, plan.NodeID, decision.Ref, decision.RemotePrefix); err != nil {
			result.Status = RetentionStatusPartial
			result.Errors = append(result.Errors, SnapshotRetentionMoveError{Ref: decision.Ref, Prefix: decision.RemotePrefix, Error: err.Error()})
			continue
		}
		trashPrefix := path.Join("_system", "retention", "trash", now().Format("20060102T150405Z"), cfg.Roots.MainSnapshots, plan.NodeID, decision.Ref)
		if _, err := mover.MoveRemote(ctx, decision.RemotePrefix, trashPrefix, CopyOptions{}); err != nil {
			result.Status = RetentionStatusPartial
			result.Errors = append(result.Errors, SnapshotRetentionMoveError{Ref: decision.Ref, Prefix: decision.RemotePrefix, Error: err.Error()})
			continue
		}
		result.Moved = append(result.Moved, SnapshotRetentionMove{
			Ref:           decision.Ref,
			FromPrefix:    decision.RemotePrefix,
			ToPrefix:      trashPrefix,
			FromRemoteURI: cfg.RemoteURI(decision.RemotePrefix),
			ToRemoteURI:   cfg.RemoteURI(trashPrefix),
		})
	}
	if len(plan.Remove) == 0 {
		result.Status = RetentionStatusSucceeded
	}
	return result, nil
}

func normalizeKeepLatest(value int) int {
	if value <= 0 {
		return DefaultSnapshotKeepLatest
	}
	return value
}

func retentionDecision(snapshot validRetentionSnapshot) SnapshotRetentionDecision {
	completedAt := snapshot.Manifest.CompletedAt
	if completedAt.IsZero() {
		completedAt = snapshot.Item.DiscoveredAt
	}
	return SnapshotRetentionDecision{
		Ref:               snapshot.Item.Ref,
		Backend:           snapshot.Item.Backend,
		Repository:        snapshot.Item.Repository,
		Archive:           snapshot.Item.Archive,
		RemotePrefix:      snapshot.Item.RemotePrefix,
		RemoteURI:         snapshot.Item.RemoteURI,
		UploadID:          snapshot.Manifest.UploadID,
		BackupOperationID: snapshot.Manifest.BackupOperationID,
		CompletedAt:       completedAt,
		FileCount:         snapshot.Manifest.FileCount,
		TotalBytes:        snapshot.Manifest.TotalBytes,
	}
}

func retentionIgnored(item SnapshotItem, reason string) SnapshotRetentionDecision {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "snapshot did not expose a valid LOOM cloud upload manifest"
	}
	return SnapshotRetentionDecision{
		Ref:          item.Ref,
		Backend:      item.Backend,
		Repository:   item.Repository,
		Archive:      item.Archive,
		RemotePrefix: item.RemotePrefix,
		RemoteURI:    item.RemoteURI,
		Action:       RetentionActionIgnore,
		Reason:       reason,
	}
}

func ensureSnapshotPrefix(cfg Config, nodeID, ref, prefix string) error {
	expected := path.Join(cfg.Roots.MainSnapshots, safeRemoteSegment(nodeID), safeRemoteSegment(ref))
	if cleanRemotePrefix(prefix) != expected {
		return fmt.Errorf("refusing to move prefix %q; expected %q", prefix, expected)
	}
	return nil
}

func defaultGFSRetentionPolicy() SnapshotRetentionPolicy {
	return SnapshotRetentionPolicy{Daily: DefaultRetentionDaily, Weekly: DefaultRetentionWeekly, Monthly: DefaultRetentionMonthly}
}

// PlanGFSRetention is the pure, deterministic policy contract used by the
// Borg adapter. The plan time is deliberately excluded from both digests: the
// same authenticated repository inventory always produces the same plan.
func PlanGFSRetention(nodeID, repository, repositoryID string, inventory []SnapshotRetentionArchive, plannedAt time.Time) (SnapshotRetentionPlan, error) {
	nodeID = safeRemoteSegment(firstNonEmpty(nodeID, "main"))
	repository = strings.TrimSpace(repository)
	repositoryID = strings.TrimSpace(repositoryID)
	if repository == "" || repositoryID == "" {
		return SnapshotRetentionPlan{}, fmt.Errorf("Borg repository path and authenticated repository identity are required")
	}
	policy := defaultGFSRetentionPolicy()
	inventory = append([]SnapshotRetentionArchive(nil), inventory...)
	seen := make(map[string]struct{}, len(inventory))
	for index := range inventory {
		item := &inventory[index]
		item.Archive = strings.TrimSpace(item.Archive)
		item.Ref = strings.TrimSpace(item.Ref)
		item.NodeID = strings.TrimSpace(item.NodeID)
		item.ArchiveClass = strings.TrimSpace(item.ArchiveClass)
		item.ManifestSHA256 = strings.ToLower(strings.TrimSpace(item.ManifestSHA256))
		if item.Archive == "" || item.CreatedAt.IsZero() || item.CreatedAt.Location() != time.UTC {
			return SnapshotRetentionPlan{}, fmt.Errorf("Borg retention inventory has an incomplete archive identity")
		}
		if _, exists := seen[item.Archive]; exists {
			return SnapshotRetentionPlan{}, fmt.Errorf("Borg retention inventory duplicates archive %q", item.Archive)
		}
		seen[item.Archive] = struct{}{}
		if item.Authenticated && item.ManifestSHA256 == "" {
			return SnapshotRetentionPlan{}, fmt.Errorf("authenticated archive %q lacks manifest identity", item.Archive)
		}
	}
	sort.Slice(inventory, func(i, j int) bool { return inventory[i].Archive < inventory[j].Archive })
	inventoryDigest, err := snapshotRetentionInventoryDigest(repositoryID, inventory)
	if err != nil {
		return SnapshotRetentionPlan{}, err
	}
	plan := SnapshotRetentionPlan{
		Schema: SnapshotRetentionPlanSchema, Status: RetentionStatusPlanned,
		NodeID: nodeID, Backend: SnapshotBackendBorg, Repository: repository,
		RepositoryID: repositoryID, RemoteRoot: "borg:" + repository,
		Policy: policy, ArchiveGlob: directArchiveRetentionGlob(nodeID),
		Inventory: inventory, InventoryDigest: inventoryDigest,
		PlannedAt: plannedAt.UTC(), Kept: []SnapshotRetentionDecision{},
		Remove: []SnapshotRetentionDecision{}, Ignored: []SnapshotRetentionDecision{},
	}

	eligible := make([]SnapshotRetentionArchive, 0, len(inventory))
	for _, item := range inventory {
		switch {
		case item.Authenticated && item.ArchiveClass == backupstrategy.DirectArchiveClassUserData && item.NodeID == nodeID:
			eligible = append(eligible, item)
		case item.Authenticated && (item.ArchiveClass == backupstrategy.DirectArchiveClassAcceptance || item.ArchiveClass == backupstrategy.DirectArchiveClassMilestone):
			plan.Kept = append(plan.Kept, retentionDecisionFromArchive(item, RetentionActionKeep, "protected explicit "+item.ArchiveClass+" archive class"))
		case item.Authenticated && item.ArchiveClass == backupstrategy.DirectArchiveClassUserData:
			plan.Kept = append(plan.Kept, retentionDecisionFromArchive(item, RetentionActionKeep, "user-data archive belongs to another node"))
		default:
			plan.Ignored = append(plan.Ignored, retentionDecisionFromArchive(item, RetentionActionIgnore, "archive is not an authenticated explicitly classified direct user-data archive"))
		}
	}
	sort.Slice(eligible, func(i, j int) bool {
		if eligible[i].CreatedAt.Equal(eligible[j].CreatedAt) {
			return eligible[i].Archive > eligible[j].Archive
		}
		return eligible[i].CreatedAt.After(eligible[j].CreatedAt)
	})
	if len(eligible) > 0 {
		plan.LatestKeptRef = eligible[0].Ref
	}
	reasons := make(map[string][]string, len(eligible))
	selectRetentionBuckets(eligible, policy.Daily, func(value time.Time) string { return value.Format("2006-01-02") }, "daily", reasons)
	selectRetentionBuckets(eligible, policy.Weekly, isoWeekBucket, "weekly", reasons)
	selectRetentionBuckets(eligible, policy.Monthly, func(value time.Time) string { return value.Format("2006-01") }, "monthly", reasons)
	for _, item := range eligible {
		itemReasons := reasons[item.Archive]
		if len(itemReasons) == 0 {
			plan.Remove = append(plan.Remove, retentionDecisionFromArchive(item, RetentionActionPrune, "outside 14-daily/8-weekly/6-monthly GFS selection"))
			continue
		}
		plan.Kept = append(plan.Kept, retentionDecisionFromArchive(item, RetentionActionKeep, strings.Join(itemReasons, ", ")))
	}
	sortRetentionDecisions(plan.Kept)
	sortRetentionDecisions(plan.Remove)
	sortRetentionDecisions(plan.Ignored)
	plan.PlanDigest, err = snapshotRetentionPlanDigest(plan)
	if err != nil {
		return SnapshotRetentionPlan{}, err
	}
	return plan, nil
}

func selectRetentionBuckets(inventory []SnapshotRetentionArchive, limit int, bucket func(time.Time) string, label string, reasons map[string][]string) {
	seen := make(map[string]struct{}, limit)
	for _, item := range inventory {
		key := bucket(item.CreatedAt)
		if _, exists := seen[key]; exists {
			continue
		}
		if len(seen) >= limit {
			continue
		}
		seen[key] = struct{}{}
		reasons[item.Archive] = append(reasons[item.Archive], label+":"+key)
	}
}

func isoWeekBucket(value time.Time) string {
	year, week := value.ISOWeek()
	return fmt.Sprintf("%04d-W%02d", year, week)
}

func retentionDecisionFromArchive(item SnapshotRetentionArchive, action, reason string) SnapshotRetentionDecision {
	return SnapshotRetentionDecision{
		Ref: item.Ref, Backend: SnapshotBackendBorg, Archive: item.Archive,
		ArchiveClass: item.ArchiveClass, ManifestSHA256: item.ManifestSHA256,
		RemotePrefix: "::" + item.Archive, Action: action, Reason: reason,
		CompletedAt: item.CreatedAt,
	}
}

func sortRetentionDecisions(items []SnapshotRetentionDecision) {
	sort.Slice(items, func(i, j int) bool { return items[i].Archive < items[j].Archive })
}

func snapshotRetentionInventoryDigest(repositoryID string, inventory []SnapshotRetentionArchive) (string, error) {
	payload := struct {
		RepositoryID string                     `json:"repository_id"`
		Inventory    []SnapshotRetentionArchive `json:"inventory"`
	}{RepositoryID: repositoryID, Inventory: inventory}
	return canonicalRetentionDigest(payload)
}

func snapshotRetentionPlanDigest(plan SnapshotRetentionPlan) (string, error) {
	payload := struct {
		Schema          string                      `json:"schema"`
		NodeID          string                      `json:"node_id"`
		Backend         string                      `json:"backend"`
		Repository      string                      `json:"repository"`
		RepositoryID    string                      `json:"repository_id"`
		Policy          SnapshotRetentionPolicy     `json:"policy"`
		ArchiveGlob     string                      `json:"archive_glob"`
		InventoryDigest string                      `json:"inventory_digest"`
		Kept            []SnapshotRetentionDecision `json:"kept"`
		Remove          []SnapshotRetentionDecision `json:"remove"`
		Ignored         []SnapshotRetentionDecision `json:"ignored"`
	}{plan.Schema, plan.NodeID, plan.Backend, plan.Repository, plan.RepositoryID, plan.Policy, plan.ArchiveGlob, plan.InventoryDigest, plan.Kept, plan.Remove, plan.Ignored}
	return canonicalRetentionDigest(payload)
}

func canonicalRetentionDigest(value any) (string, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:]), nil
}

func planBorgSnapshotRetention(ctx context.Context, backend BorgSnapshotBackend, input SnapshotRetentionInput) (SnapshotRetentionPlan, error) {
	cfg, err := NormalizeConfig(input.Config)
	if err != nil {
		return SnapshotRetentionPlan{}, err
	}
	if !cfg.Enabled || cfg.Snapshots.Backend != SnapshotBackendBorg {
		return SnapshotRetentionPlan{}, fmt.Errorf("Borg cloud storage is disabled")
	}
	runner, retentionCfg, err := backend.retentionRunner(cfg)
	if err != nil {
		return SnapshotRetentionPlan{}, err
	}
	repositoryID, err := borgRepositoryIdentity(ctx, runner)
	if err != nil {
		return SnapshotRetentionPlan{}, fmt.Errorf("read Borg repository identity: %w", err)
	}
	inventory, err := borgRetentionInventory(ctx, runner, retentionCfg)
	if err != nil {
		return SnapshotRetentionPlan{}, err
	}
	plan, err := PlanGFSRetention(input.NodeID, retentionCfg.Snapshots.Borg.Repository, repositoryID, inventory, normalizeNow(input.Now)())
	if err != nil {
		return SnapshotRetentionPlan{}, err
	}
	if err := validateBorgPruneGlobInventory(plan, inventory); err != nil {
		return SnapshotRetentionPlan{}, err
	}
	if _, err := runRequiredBorgRetentionCheck(ctx, runner, retentionCfg); err != nil {
		return SnapshotRetentionPlan{}, fmt.Errorf("verify Borg repository for retention plan: %w", err)
	}
	return bindBorgRetentionPlanLocations(plan, retentionCfg)
}

func bindBorgRetentionPlanLocations(plan SnapshotRetentionPlan, cfg Config) (SnapshotRetentionPlan, error) {
	plan.RepositoryCheck = SnapshotStatusSucceeded
	for index := range plan.Kept {
		plan.Kept[index].Repository = plan.Repository
		plan.Kept[index].RemoteURI = borgRemoteURI(cfg, plan.Kept[index].Archive)
	}
	for index := range plan.Remove {
		plan.Remove[index].Repository = plan.Repository
		plan.Remove[index].RemoteURI = borgRemoteURI(cfg, plan.Remove[index].Archive)
	}
	for index := range plan.Ignored {
		plan.Ignored[index].Repository = plan.Repository
		plan.Ignored[index].RemoteURI = borgRemoteURI(cfg, plan.Ignored[index].Archive)
	}
	var err error
	plan.PlanDigest, err = snapshotRetentionPlanDigest(plan)
	if err != nil {
		return SnapshotRetentionPlan{}, err
	}
	return plan, nil
}

func applyBorgSnapshotRetention(ctx context.Context, backend BorgSnapshotBackend, input SnapshotRetentionApplyInput) (SnapshotRetentionApplyResult, error) {
	now := normalizeNow(input.Now)
	plan := input.Plan
	result := SnapshotRetentionApplyResult{
		Status: RetentionStatusConfirmationRequired, Plan: plan, AppliedAt: now(),
		Prune:   SnapshotRetentionStage{Status: RetentionStagePending},
		Compact: SnapshotRetentionStage{Status: RetentionStagePending},
	}
	if err := validateSnapshotRetentionPlan(plan); err != nil {
		result.Status = RetentionStatusRefused
		return result, err
	}
	if !input.Confirm || strings.TrimSpace(input.ConfirmDigest) != plan.PlanDigest {
		result.Prune.Status = RetentionStageSkipped
		result.Compact.Status = RetentionStageSkipped
		return result, nil
	}
	cfg, err := NormalizeConfig(input.Config)
	if err != nil {
		return result, err
	}
	runner, retentionCfg, err := backend.retentionRunner(cfg)
	if err != nil {
		result.Status = RetentionStatusRefused
		return result, err
	}
	if retentionCfg.Snapshots.Borg.Repository != plan.Repository {
		result.Status = RetentionStatusRefused
		return result, fmt.Errorf("retention plan repository path does not match configured retention authority")
	}
	requestedNodeID := safeRemoteSegment(firstNonEmpty(input.NodeID, "main"))
	if plan.NodeID != requestedNodeID {
		result.Status = RetentionStatusRefused
		return result, fmt.Errorf("retention plan node does not match the requested node")
	}
	lockedRunner := runner
	lockedRunner.DisableRemoteLock = true
	lockAcquired := false
	lockErr := WithRemoteLock(ctx, retentionCfg, RemoteLockOptions{
		Operation: "borg.retention_apply",
		Wait:      borgRetentionRemoteLockWait(runner, retentionCfg),
	}, func(ctx context.Context) error {
		lockAcquired = true
		return applyBorgSnapshotRetentionLocked(ctx, lockedRunner, retentionCfg, plan, input.Compact, &result)
	})
	if lockErr != nil {
		if !lockAcquired {
			result.Status = RetentionStatusRefused
			result.Prune.Status = RetentionStageSkipped
			result.Prune.Error = "shared remote lock unavailable"
			result.Compact.Status = RetentionStageSkipped
		}
		return result, lockErr
	}
	return result, nil
}

func borgRetentionRemoteLockWait(runner BorgCommandRunner, cfg Config) time.Duration {
	wait := RemoteLockEffectfulWait(cfg)
	if runner.RemoteLockWait != nil {
		wait = *runner.RemoteLockWait
	}
	return wait
}

func applyBorgSnapshotRetentionLocked(ctx context.Context, runner BorgCommandRunner, retentionCfg Config, plan SnapshotRetentionPlan, compact bool, result *SnapshotRetentionApplyResult) error {
	repositoryID, err := borgRepositoryIdentity(ctx, runner)
	if err != nil {
		result.Status = RetentionStatusRefused
		return err
	}
	if repositoryID != plan.RepositoryID {
		result.Status = RetentionStatusRefused
		return fmt.Errorf("Borg repository identity changed since retention planning")
	}
	liveInventory, err := borgRetentionInventory(ctx, runner, retentionCfg)
	if err != nil {
		result.Status = RetentionStatusRefused
		result.Prune.Status = RetentionStageSkipped
		result.Compact.Status = RetentionStageSkipped
		return err
	}
	liveDigest, err := snapshotRetentionInventoryDigest(repositoryID, liveInventory)
	if err != nil {
		return err
	}
	if liveDigest != plan.InventoryDigest {
		result.Status = RetentionStatusRefused
		result.Prune.Status = RetentionStageSkipped
		result.Compact.Status = RetentionStageSkipped
		return fmt.Errorf("Borg archive inventory changed since retention planning")
	}
	expectedPlan, err := PlanGFSRetention(plan.NodeID, plan.Repository, repositoryID, liveInventory, plan.PlannedAt)
	if err != nil {
		result.Status = RetentionStatusRefused
		result.Prune.Status = RetentionStageSkipped
		result.Compact.Status = RetentionStageSkipped
		return err
	}
	expectedPlan, err = bindBorgRetentionPlanLocations(expectedPlan, retentionCfg)
	if err != nil {
		result.Status = RetentionStatusRefused
		return err
	}
	if expectedPlan.PlanDigest != plan.PlanDigest {
		result.Status = RetentionStatusRefused
		result.Prune.Status = RetentionStageSkipped
		result.Compact.Status = RetentionStageSkipped
		return fmt.Errorf("Borg retention decisions do not match the canonical GFS plan")
	}
	if err := validateBorgPruneGlobInventory(expectedPlan, liveInventory); err != nil {
		result.Status = RetentionStatusRefused
		result.Prune.Status = RetentionStageSkipped
		result.Compact.Status = RetentionStageSkipped
		return err
	}
	if _, err := runRequiredBorgRetentionCheck(ctx, runner, retentionCfg); err != nil {
		result.Status = RetentionStatusRefused
		result.Prune.Status = RetentionStageSkipped
		result.Prune.Error = "repository check failed"
		result.Compact.Status = RetentionStageSkipped
		return fmt.Errorf("Borg repository check failed before retention mutation: %w", err)
	}
	if len(plan.Remove) == 0 {
		result.Status = RetentionStatusSucceeded
		result.Prune.Status = RetentionStageSkipped
		result.Prune.Verification = "no_prune_candidates"
		result.Compact.Status = RetentionStageSkipped
		result.Compact.Verification = "no_prune_candidates"
		return nil
	}
	args := []string{
		"prune", "--list", "--stats",
		"--keep-daily", fmt.Sprintf("%d", plan.Policy.Daily),
		"--keep-weekly", fmt.Sprintf("%d", plan.Policy.Weekly),
		"--keep-monthly", fmt.Sprintf("%d", plan.Policy.Monthly),
		"--glob-archives", plan.ArchiveGlob,
	}
	result.Prune.Command = strings.Join(args, " ")
	out, pruneErr := runner.Run(ctx, BorgCommand{Args: args})
	result.Prune.Output = strings.TrimSpace(string(out))
	if pruneErr != nil {
		result.Status = RetentionStatusPartial
		result.Prune.Status = RetentionStageFailed
		result.Prune.Error = pruneErr.Error()
		result.Compact.Status = RetentionStageSkipped
		return nil
	}
	result.Prune.Status = RetentionStageSucceeded
	postInventory, inventoryErr := borgRetentionInventory(ctx, runner, retentionCfg)
	if inventoryErr != nil {
		result.Status = RetentionStatusPartial
		result.Prune.Verification = RetentionStageFailed
		result.Prune.Error = inventoryErr.Error()
		result.Compact.Status = RetentionStageSkipped
		return nil
	}
	expected := retentionInventoryAfterPrune(plan.Inventory, plan.Remove)
	expectedDigest, _ := snapshotRetentionInventoryDigest(repositoryID, expected)
	postDigest, _ := snapshotRetentionInventoryDigest(repositoryID, postInventory)
	if postDigest != expectedDigest {
		result.Status = RetentionStatusPartial
		result.Prune.Verification = RetentionStageFailed
		result.Prune.Error = "post-prune archive inventory does not match the digest-bound plan"
		result.Compact.Status = RetentionStageSkipped
		return nil
	}
	result.Prune.Verification = RetentionStageSucceeded
	for _, decision := range plan.Remove {
		result.Moved = append(result.Moved, SnapshotRetentionMove{
			Ref: decision.Ref, Backend: SnapshotBackendBorg, Repository: plan.Repository,
			Archive: decision.Archive, FromPrefix: "::" + decision.Archive,
			ToPrefix: "borg:pruned", FromRemoteURI: borgRemoteURI(retentionCfg, decision.Archive), ToRemoteURI: "borg:pruned",
		})
	}
	if !compact {
		result.Status = RetentionStatusPartial
		result.Compact.Status = RetentionStageSkipped
		result.Compact.Verification = "operator_review_required"
		return nil
	}
	result.Compact.Command = "compact"
	compactOut, compactErr := runner.Run(ctx, BorgCommand{Args: []string{"compact"}})
	result.Compact.Output = strings.TrimSpace(string(compactOut))
	if compactErr != nil {
		result.Status = RetentionStatusPartial
		result.Compact.Status = RetentionStageFailed
		result.Compact.Error = compactErr.Error()
		return nil
	}
	result.Compact.Status = RetentionStageSucceeded
	result.Compact.Verification = "completed_without_reclaim_claim"
	result.Status = RetentionStatusSucceeded
	return nil
}

func validateSnapshotRetentionPlan(plan SnapshotRetentionPlan) error {
	if plan.Schema != SnapshotRetentionPlanSchema || plan.Status != RetentionStatusPlanned || plan.Backend != SnapshotBackendBorg || plan.Policy != defaultGFSRetentionPolicy() || plan.RepositoryCheck != SnapshotStatusSucceeded {
		return fmt.Errorf("unsupported or incomplete Borg retention plan")
	}
	if plan.Repository == "" || plan.RepositoryID == "" || plan.InventoryDigest == "" || plan.PlanDigest == "" || plan.ArchiveGlob != directArchiveRetentionGlob(plan.NodeID) {
		return fmt.Errorf("Borg retention plan identity is incomplete")
	}
	inventoryDigest, err := snapshotRetentionInventoryDigest(plan.RepositoryID, plan.Inventory)
	if err != nil || inventoryDigest != plan.InventoryDigest {
		return fmt.Errorf("Borg retention plan inventory digest mismatch")
	}
	digest, err := snapshotRetentionPlanDigest(plan)
	if err != nil || digest != plan.PlanDigest {
		return fmt.Errorf("Borg retention plan digest mismatch")
	}
	for _, decision := range plan.Remove {
		if decision.Action != RetentionActionPrune || decision.ArchiveClass != backupstrategy.DirectArchiveClassUserData || !strings.HasPrefix(decision.Archive, strings.TrimSuffix(plan.ArchiveGlob, "*")) {
			return fmt.Errorf("Borg retention plan contains an out-of-class prune candidate")
		}
	}
	return nil
}

func (b BorgSnapshotBackend) retentionRunner(cfg Config) (BorgCommandRunner, Config, error) {
	retentionCfg, err := retentionAuthorityConfig(cfg)
	if err != nil {
		return BorgCommandRunner{}, Config{}, err
	}
	runner := b.Runner
	if runner.Exec == nil && runner.StreamExec == nil {
		runner = NewBorgCommandRunner(retentionCfg)
	}
	runner.Config = retentionCfg
	runner.authority = borgAuthorityRetention
	return runner, retentionCfg, nil
}

func borgRepositoryIdentity(ctx context.Context, runner BorgCommandRunner) (string, error) {
	out, err := runner.Run(ctx, BorgCommand{Args: []string{"info", "--json"}})
	if err != nil {
		return "", err
	}
	var document struct {
		Repository struct {
			ID string `json:"id"`
		} `json:"repository"`
	}
	if err := json.Unmarshal(out, &document); err != nil {
		return "", fmt.Errorf("Borg repository info is not valid JSON: %w", err)
	}
	id := strings.TrimSpace(document.Repository.ID)
	if id == "" {
		return "", fmt.Errorf("Borg repository info does not contain repository.id")
	}
	return id, nil
}

func borgRetentionInventory(ctx context.Context, runner BorgCommandRunner, cfg Config) ([]SnapshotRetentionArchive, error) {
	archives, err := listBorgArchives(ctx, runner)
	if err != nil {
		return nil, err
	}
	result := make([]SnapshotRetentionArchive, 0, len(archives))
	for _, archive := range archives {
		item := SnapshotRetentionArchive{Archive: archive.Name, ArchiveClass: "unclassified", CreatedAt: archive.Time}
		switch {
		case isDirectPendingBorgArchive(archive.Name):
			item.ArchiveClass = "pending"
		case isDirectCanonicalBorgArchive(archive.Name):
			prepared, verifyErr := readAndVerifyDirectArchive(ctx, runner, cfg, archive.Name)
			if verifyErr != nil {
				return nil, fmt.Errorf("authenticate direct archive %q for retention inventory: %w", archive.Name, verifyErr)
			}
			manifest := prepared.Manifest
			if !archive.Time.Equal(manifest.CreatedAt) {
				return nil, fmt.Errorf("direct archive %q timestamp does not match its authenticated manifest", archive.Name)
			}
			item.Ref = manifest.ArchiveRef
			item.NodeID = manifest.NodeID
			item.ArchiveClass = manifest.ArchiveClass
			if item.ArchiveClass == "" {
				item.ArchiveClass = "legacy_unclassified"
			}
			item.CreatedAt = manifest.CreatedAt
			item.ManifestSHA256 = prepared.ManifestSHA256
			item.Authenticated = true
		}
		result = append(result, item)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Archive < result[j].Archive })
	return result, nil
}

func runRequiredBorgRetentionCheck(ctx context.Context, runner BorgCommandRunner, cfg Config) (bool, error) {
	mode := normalizeToken(cfg.Snapshots.Borg.CheckMode)
	if mode == "repository_only" {
		mode = "repository"
	}
	switch mode {
	case "repository", "repo", "full":
		return runBorgCheck(ctx, runner, mode, "")
	default:
		return false, fmt.Errorf("retention authority requires a repository-wide Borg check mode")
	}
}

func directArchiveRetentionGlob(nodeID string) string {
	return "__loom-direct-user-data-" + safeRemoteSegment(nodeID) + "-*"
}

func validateBorgPruneGlobInventory(plan SnapshotRetentionPlan, inventory []SnapshotRetentionArchive) error {
	prefix := strings.TrimSuffix(plan.ArchiveGlob, "*")
	eligible := make(map[string]struct{}, len(plan.Kept)+len(plan.Remove))
	for _, decision := range append(append([]SnapshotRetentionDecision(nil), plan.Kept...), plan.Remove...) {
		if decision.ArchiveClass == backupstrategy.DirectArchiveClassUserData {
			eligible[decision.Archive] = struct{}{}
		}
	}
	for _, item := range inventory {
		if !strings.HasPrefix(item.Archive, prefix) {
			continue
		}
		exactArchive := directBorgArchiveName(item.NodeID, item.Ref, item.ArchiveClass)
		_, planned := eligible[item.Archive]
		if !item.Authenticated || item.NodeID != plan.NodeID || item.ArchiveClass != backupstrategy.DirectArchiveClassUserData || item.Ref == "" || item.ManifestSHA256 == "" || item.Archive != exactArchive || !planned {
			return fmt.Errorf("refuse Borg retention: prune glob %q matches archive %q outside the authenticated exact same-node user-data plan", plan.ArchiveGlob, item.Archive)
		}
	}
	return nil
}

func retentionInventoryAfterPrune(inventory []SnapshotRetentionArchive, remove []SnapshotRetentionDecision) []SnapshotRetentionArchive {
	removed := make(map[string]struct{}, len(remove))
	for _, decision := range remove {
		removed[decision.Archive] = struct{}{}
	}
	result := make([]SnapshotRetentionArchive, 0, len(inventory)-len(remove))
	for _, item := range inventory {
		if _, ok := removed[item.Archive]; !ok {
			result = append(result, item)
		}
	}
	return result
}
