package projectwatch

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"loom.local/loom/internal/nodes"
	"loom.local/loom/internal/projectcontracts"
	"loom.local/loom/internal/projects"
	"loom.local/loom/internal/requestctx"
	loomsync "loom.local/loom/internal/sync"
	"loom.local/loom/internal/watchedroots"
)

type projectStore interface {
	GetProjectRegistrationStatus(context.Context, string) (projects.ProjectRegistrationDetail, error)
	ActivateProjectBase(context.Context, requestctx.Context, string) (projects.ProjectRegistrationDetail, error)
	ListProjectWatchedRootRegistrations(context.Context, string) ([]projects.ProjectWatchedRootRegistration, error)
	UpsertProjectWatchedRootRegistration(context.Context, requestctx.Context, projects.UpsertProjectWatchedRootRegistrationInput) (projects.ProjectWatchedRootRegistration, error)
	MarkStaleProjectWatchedRoots(context.Context, requestctx.Context, string, string, []string) error
	CorrelateProjectWatchedRootReports(context.Context, string) error
	MarkProjectFacetActivated(context.Context, requestctx.Context, string, string, string, json.RawMessage) error
}

type nodeStore interface {
	GetNode(context.Context, string) (nodes.Node, error)
}

type watchedRootStore interface {
	ListStatus(context.Context, watchedroots.StatusFilter) ([]watchedroots.RootStatus, error)
	GetBackupStatus(context.Context, watchedroots.BackupFilter) (watchedroots.BackupStatus, error)
	ListBackupBatches(context.Context, watchedroots.BackupFilter) ([]watchedroots.BackupBatch, error)
	ListBackupItems(context.Context, watchedroots.BackupItemFilter) ([]watchedroots.BackupItem, error)
}

type syncStore interface {
	GetStatus(context.Context, string) (loomsync.SyncStatus, error)
	ListReplicas(context.Context, loomsync.ListFilter) ([]loomsync.SyncReplica, error)
}

type Deps struct {
	Projects     projectStore
	Nodes        nodeStore
	WatchedRoots watchedRootStore
	Sync         syncStore
}

type Service struct {
	projects     projectStore
	nodes        nodeStore
	watchedRoots watchedRootStore
	sync         syncStore
}

type BuildPlanInput struct {
	ProjectRoot           string `json:"project_root,omitempty"`
	UseRegisteredSnapshot bool   `json:"use_registered_snapshot,omitempty"`
}

type ProjectWatchPlan struct {
	Detail       *projects.ProjectRegistrationDetail       `json:"detail,omitempty"`
	ProjectRoot  string                                    `json:"project_root"`
	ContractHash string                                    `json:"contract_hash,omitempty"`
	Report       projectcontracts.ValidationReport         `json:"report"`
	WatchedRoots []projectcontracts.ProjectWatchedRootItem `json:"watched_roots"`
	Commands     []projects.ProjectWatchedRootCommand      `json:"commands"`
}

type ProjectSyncStatus struct {
	ProjectRef        string                                    `json:"project_ref"`
	WatchedRoots      []projects.ProjectWatchedRootRegistration `json:"watched_roots"`
	Statuses          []watchedroots.RootStatus                 `json:"statuses,omitempty"`
	NodeStatuses      []loomsync.SyncStatus                     `json:"node_statuses,omitempty"`
	Replicas          []loomsync.SyncReplica                    `json:"replicas,omitempty"`
	SyncRoots         int                                       `json:"sync_roots"`
	PendingAgentApply int                                       `json:"pending_agent_apply"`
	Reported          int                                       `json:"reported"`
	Stale             int                                       `json:"stale"`
	Blocked           int                                       `json:"blocked"`
}

type ProjectBackupStatus struct {
	ProjectRef        string                                    `json:"project_ref"`
	WatchedRoots      []projects.ProjectWatchedRootRegistration `json:"watched_roots"`
	Backups           []watchedroots.BackupStatus               `json:"backups,omitempty"`
	BackupBatches     []watchedroots.BackupBatch                `json:"backup_batches,omitempty"`
	BackupItems       []watchedroots.BackupItem                 `json:"backup_items,omitempty"`
	BackupRoots       int                                       `json:"backup_roots"`
	PendingAgentApply int                                       `json:"pending_agent_apply"`
	Reported          int                                       `json:"reported"`
	Stale             int                                       `json:"stale"`
	Blocked           int                                       `json:"blocked"`
}

func NewService(deps Deps) Service {
	return Service{projects: deps.Projects, nodes: deps.Nodes, watchedRoots: deps.WatchedRoots, sync: deps.Sync}
}

func (s Service) BuildPlan(ctx context.Context, ref string, input BuildPlanInput) (ProjectWatchPlan, error) {
	ref = strings.TrimSpace(ref)
	root := strings.TrimSpace(input.ProjectRoot)
	var detail *projects.ProjectRegistrationDetail
	if root == "" && looksLikeDirectory(ref) {
		root = ref
	}
	if root == "" {
		if s.projects == nil {
			return ProjectWatchPlan{}, fmt.Errorf("project service is not configured")
		}
		projectDetail, err := s.projects.GetProjectRegistrationStatus(ctx, ref)
		if err != nil {
			return ProjectWatchPlan{}, err
		}
		detail = &projectDetail
		if projectDetail.Registration == nil {
			return ProjectWatchPlan{}, fmt.Errorf("project has no registered project contract")
		}
		root = projectDetail.Registration.ProjectRoot
	}
	if root == "" {
		return ProjectWatchPlan{}, fmt.Errorf("project root is required")
	}
	analysis := projectcontracts.Analyze(root)
	if analysis.Loaded == nil {
		if input.UseRegisteredSnapshot && detail != nil && detail.Registration != nil {
			return planFromRegisteredSnapshot(*detail), nil
		}
		return ProjectWatchPlan{Detail: detail, ProjectRoot: root, Report: analysis.Report}, fmt.Errorf("project contract could not be loaded from %s", root)
	}
	hash := hashBytesURI(analysis.Loaded.Raw)
	if detail != nil && detail.Registration != nil && detail.Registration.ContractHash != "" && detail.Registration.ContractHash != hash {
		if input.UseRegisteredSnapshot {
			return planFromRegisteredSnapshot(*detail), nil
		}
		return ProjectWatchPlan{Detail: detail, ProjectRoot: root, ContractHash: hash, Report: analysis.Report, WatchedRoots: analysis.Plan.WatchedRoots, Commands: commandsFromPlan(analysis.Plan)}, fmt.Errorf("project contract hash is stale: current %s registered %s", hash, detail.Registration.ContractHash)
	}
	return ProjectWatchPlan{
		Detail:       detail,
		ProjectRoot:  root,
		ContractHash: hash,
		Report:       analysis.Report,
		WatchedRoots: append([]projectcontracts.ProjectWatchedRootItem{}, analysis.Plan.WatchedRoots...),
		Commands:     commandsFromPlan(analysis.Plan),
	}, nil
}

func (s Service) ApplyDesiredState(ctx context.Context, req requestctx.Context, ref string, input projects.ApplyProjectWatchPolicyInput) (projects.ApplyProjectWatchPolicyResult, error) {
	if s.projects == nil || s.nodes == nil {
		return projects.ApplyProjectWatchPolicyResult{}, fmt.Errorf("project watch service is not fully configured")
	}
	detail, err := s.projects.GetProjectRegistrationStatus(ctx, ref)
	if err != nil {
		return projects.ApplyProjectWatchPolicyResult{}, err
	}
	if detail.Registration == nil {
		return projects.ApplyProjectWatchPolicyResult{}, fmt.Errorf("project has no registered project contract")
	}
	plan, err := s.BuildPlan(ctx, ref, BuildPlanInput{ProjectRoot: input.ProjectRoot, UseRegisteredSnapshot: input.UseRegisteredSnapshot})
	if err != nil {
		return projects.ApplyProjectWatchPolicyResult{}, err
	}
	if plan.Report.SchemaVersion != "" && !plan.Report.OK {
		return projects.ApplyProjectWatchPolicyResult{}, fmt.Errorf("project watch policy has validation errors")
	}
	if input.DryRun {
		return projects.ApplyProjectWatchPolicyResult{Detail: detail, Commands: plan.Commands, DryRun: true}, nil
	}
	if detail.Registration.ActivationStatus != projects.ProjectActivationStatusBaseActive {
		detail, err = s.projects.ActivateProjectBase(ctx, req, ref)
		if err != nil {
			return projects.ApplyProjectWatchPolicyResult{}, err
		}
	}
	activeKeys := make([]string, 0, len(plan.WatchedRoots))
	registrations := make([]projects.ProjectWatchedRootRegistration, 0, len(plan.WatchedRoots))
	for _, root := range plan.WatchedRoots {
		node, err := s.nodes.GetNode(ctx, root.OwnerNode)
		if err != nil {
			return projects.ApplyProjectWatchPolicyResult{}, fmt.Errorf("resolve owner node %s: %w", root.OwnerNode, err)
		}
		if node.Status != "active" || node.RetiredAt != nil {
			return projects.ApplyProjectWatchPolicyResult{}, fmt.Errorf("owner node %s is not active", root.OwnerNode)
		}
		activeKeys = append(activeKeys, root.Key)
		sourceKinds, err := json.Marshal(root.SourceKinds)
		if err != nil {
			return projects.ApplyProjectWatchPolicyResult{}, err
		}
		commands, err := json.Marshal(root.AgentCommands)
		if err != nil {
			return projects.ApplyProjectWatchPolicyResult{}, err
		}
		metadata, err := json.Marshal(root.Metadata)
		if err != nil {
			return projects.ApplyProjectWatchPolicyResult{}, err
		}
		status := root.ActivationStatus
		if status == "" {
			status = projects.ProjectWatchedRootRegistrationStatusPendingAgentApply
		}
		registration, err := s.projects.UpsertProjectWatchedRootRegistration(ctx, req, projects.UpsertProjectWatchedRootRegistrationInput{
			ProjectContractRegistrationID: detail.Registration.ProjectContractRegistrationID,
			ProjectID:                     detail.Project.Project.ProjectID,
			NodeID:                        node.NodeID,
			OwnerNodeKey:                  node.NodeKey,
			LocalRootKey:                  root.Key,
			BackendRootKey:                root.BackendRootKey,
			WorkerKey:                     root.WorkerKey,
			SourceKinds:                   sourceKinds,
			SafeRootKey:                   root.SafeRootKey,
			RootRelativePath:              root.RootRelativePath,
			DisplayName:                   root.DisplayName,
			SyncMode:                      root.SyncMode,
			BackupMode:                    root.BackupMode,
			IndexMode:                     root.IndexMode,
			DeleteMode:                    root.DeleteMode,
			ConfigHash:                    root.ConfigHash,
			ConfigJSON:                    root.ConfigJSON,
			CommandJSON:                   commands,
			ActivationStatus:              status,
			Metadata:                      metadata,
		})
		if err != nil {
			return projects.ApplyProjectWatchPolicyResult{}, err
		}
		registrations = append(registrations, registration)
	}
	sort.Strings(activeKeys)
	if err := s.projects.MarkStaleProjectWatchedRoots(ctx, req, detail.Project.Project.ProjectID, detail.Registration.ProjectContractRegistrationID, activeKeys); err != nil {
		return projects.ApplyProjectWatchPolicyResult{}, err
	}
	if err := s.projects.CorrelateProjectWatchedRootReports(ctx, detail.Project.Project.ProjectID); err != nil {
		return projects.ApplyProjectWatchPolicyResult{}, err
	}
	if err := s.markRepresentedFacets(ctx, req, detail, registrations); err != nil {
		return projects.ApplyProjectWatchPolicyResult{}, err
	}
	refreshed, err := s.projects.GetProjectRegistrationStatus(ctx, detail.Project.Project.ProjectID)
	if err != nil {
		return projects.ApplyProjectWatchPolicyResult{}, err
	}
	return projects.ApplyProjectWatchPolicyResult{Detail: refreshed, WatchedRoots: refreshed.WatchedRootRegistrations, Commands: plan.Commands}, nil
}

func (s Service) SyncStatus(ctx context.Context, ref string) (ProjectSyncStatus, error) {
	if s.projects == nil {
		return ProjectSyncStatus{}, fmt.Errorf("project service is not configured")
	}
	if err := s.projects.CorrelateProjectWatchedRootReports(ctx, ref); err != nil {
		return ProjectSyncStatus{}, err
	}
	registrations, err := s.projects.ListProjectWatchedRootRegistrations(ctx, ref)
	if err != nil {
		return ProjectSyncStatus{}, err
	}
	status := ProjectSyncStatus{ProjectRef: ref, WatchedRoots: registrations}
	ownerNodes := map[string]struct{}{}
	for _, root := range registrations {
		addStatusCounts(root.ActivationStatus, &status.PendingAgentApply, &status.Reported, &status.Stale, &status.Blocked)
		if root.SyncMode != "" && root.SyncMode != "none" {
			status.SyncRoots++
			if nodeRef := watchedRootOwnerNodeRef(root); nodeRef != "" {
				ownerNodes[nodeRef] = struct{}{}
			}
		}
	}
	if s.watchedRoots != nil {
		rootStatuses, err := s.watchedRoots.ListStatus(ctx, watchedroots.StatusFilter{ProjectRef: ref, Limit: 200})
		if err == nil {
			status.Statuses = rootStatuses
		}
	}
	if s.sync != nil {
		for _, nodeRef := range sortedMapKeys(ownerNodes) {
			nodeStatus, err := s.sync.GetStatus(ctx, nodeRef)
			if err == nil {
				status.NodeStatuses = append(status.NodeStatuses, nodeStatus)
			}
		}
		replicas, err := s.sync.ListReplicas(ctx, loomsync.ListFilter{ProjectRef: ref, Limit: 50})
		if err == nil {
			status.Replicas = replicas
		}
	}
	return status, nil
}

func (s Service) BackupStatus(ctx context.Context, ref string) (ProjectBackupStatus, error) {
	if s.projects == nil {
		return ProjectBackupStatus{}, fmt.Errorf("project service is not configured")
	}
	if err := s.projects.CorrelateProjectWatchedRootReports(ctx, ref); err != nil {
		return ProjectBackupStatus{}, err
	}
	registrations, err := s.projects.ListProjectWatchedRootRegistrations(ctx, ref)
	if err != nil {
		return ProjectBackupStatus{}, err
	}
	status := ProjectBackupStatus{ProjectRef: ref, WatchedRoots: registrations}
	for _, root := range registrations {
		addStatusCounts(root.ActivationStatus, &status.PendingAgentApply, &status.Reported, &status.Stale, &status.Blocked)
		if root.BackupMode != "" && root.BackupMode != "none" {
			status.BackupRoots++
			if s.watchedRoots != nil {
				backup, err := s.watchedRoots.GetBackupStatus(ctx, watchedroots.BackupFilter{ProjectRef: ref, RootKey: root.BackendRootKey, Limit: 1})
				if err == nil {
					status.Backups = append(status.Backups, backup)
				}
				if err != nil && err != sql.ErrNoRows {
					return ProjectBackupStatus{}, err
				}
			}
		}
	}
	if s.watchedRoots != nil {
		batches, err := s.watchedRoots.ListBackupBatches(ctx, watchedroots.BackupFilter{ProjectRef: ref, Limit: 10})
		if err == nil {
			status.BackupBatches = batches
		}
		items, err := s.watchedRoots.ListBackupItems(ctx, watchedroots.BackupItemFilter{ProjectRef: ref, Limit: 25})
		if err == nil {
			status.BackupItems = items
		}
	}
	return status, nil
}

func (s Service) markRepresentedFacets(ctx context.Context, req requestctx.Context, detail projects.ProjectRegistrationDetail, registrations []projects.ProjectWatchedRootRegistration) error {
	if detail.Registration == nil {
		return nil
	}
	facets := map[string]struct{}{}
	for _, registration := range registrations {
		var kinds []string
		if err := json.Unmarshal(registration.SourceKinds, &kinds); err != nil {
			continue
		}
		for _, kind := range kinds {
			switch kind {
			case "notes_contract":
				facets["notes"] = struct{}{}
			case "repos_contract":
				facets["repos"] = struct{}{}
			case "sync_policy":
				facets["sync_policy"] = struct{}{}
			case "backup_policy":
				facets["backup_policy"] = struct{}{}
			}
		}
	}
	for facet := range facets {
		metadata := json.RawMessage(`{"source":"project.watch_policy.apply"}`)
		if err := s.projects.MarkProjectFacetActivated(ctx, req, detail.Project.Project.ProjectID, detail.Registration.ProjectContractRegistrationID, facet, metadata); err != nil {
			return err
		}
	}
	return nil
}

func commandsFromPlan(plan projectcontracts.ProjectPlan) []projects.ProjectWatchedRootCommand {
	if len(plan.WatchedRoots) == 0 {
		return nil
	}
	command := []string{"loom-node-agent", "watched-roots", "apply-plan", "/tmp/loom-project-watch-plan.json"}
	if strings.TrimSpace(plan.ProjectRoot) != "" {
		command = append(command, "--project-root", plan.ProjectRoot)
	}
	return []projects.ProjectWatchedRootCommand{{
		Description: "Apply the exact compiled project watched-root plan on the owner node-agent.",
		Command:     command,
		Shell:       shellJoin(command),
	}}
}

func shellJoin(args []string) string {
	quoted := make([]string, 0, len(args))
	for _, arg := range args {
		if arg == "" {
			quoted = append(quoted, "''")
			continue
		}
		if strings.IndexFunc(arg, func(r rune) bool {
			return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("-_./:=@", r))
		}) == -1 {
			quoted = append(quoted, arg)
			continue
		}
		quoted = append(quoted, "'"+strings.ReplaceAll(arg, "'", "'\"'\"'")+"'")
	}
	return strings.Join(quoted, " ")
}

func planFromRegisteredSnapshot(detail projects.ProjectRegistrationDetail) ProjectWatchPlan {
	var plan projectcontracts.ProjectPlan
	if detail.Registration != nil {
		_ = json.Unmarshal(detail.Registration.RegistrationPlan, &plan)
	}
	return ProjectWatchPlan{
		Detail:       &detail,
		ProjectRoot:  ptrRegistrationRoot(detail),
		ContractHash: ptrRegistrationHash(detail),
		WatchedRoots: append([]projectcontracts.ProjectWatchedRootItem{}, plan.WatchedRoots...),
		Commands:     commandsFromPlan(plan),
	}
}

func ptrRegistrationRoot(detail projects.ProjectRegistrationDetail) string {
	if detail.Registration == nil {
		return ""
	}
	return detail.Registration.ProjectRoot
}

func ptrRegistrationHash(detail projects.ProjectRegistrationDetail) string {
	if detail.Registration == nil {
		return ""
	}
	return detail.Registration.ContractHash
}

func looksLikeDirectory(ref string) bool {
	if strings.TrimSpace(ref) == "" {
		return false
	}
	info, err := os.Stat(ref)
	return err == nil && info.IsDir()
}

func hashBytesURI(payload []byte) string {
	sum := sha256.Sum256(payload)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func addStatusCounts(status string, pending, reported, stale, blocked *int) {
	switch status {
	case projects.ProjectWatchedRootRegistrationStatusPendingAgentApply, projects.ProjectWatchedRootRegistrationStatusRegistered, projects.ProjectWatchedRootRegistrationStatusApplied:
		(*pending)++
	case projects.ProjectWatchedRootRegistrationStatusReported:
		(*reported)++
	case projects.ProjectWatchedRootRegistrationStatusStale:
		(*stale)++
	case projects.ProjectWatchedRootRegistrationStatusBlocked:
		(*blocked)++
	}
}

func watchedRootOwnerNodeRef(root projects.ProjectWatchedRootRegistration) string {
	if strings.TrimSpace(root.OwnerNodeKey) != "" {
		return strings.TrimSpace(root.OwnerNodeKey)
	}
	return strings.TrimSpace(root.NodeID)
}

func sortedMapKeys(values map[string]struct{}) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		if strings.TrimSpace(key) != "" {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	return keys
}
