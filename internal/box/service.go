package box

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"loom.local/loom/internal/backupcontracts"
	"loom.local/loom/internal/ids"
	agentwatchedroots "loom.local/loom/internal/nodeagent/watchedroots"
	"loom.local/loom/internal/nodes"
	"loom.local/loom/internal/projectcontracts"
	"loom.local/loom/internal/requestctx"
	"loom.local/loom/internal/watchedroots"
)

const boxAreaScopeType = "box_area"

type Service struct {
	DB *sql.DB
}

func NewService(db *sql.DB) Service {
	return Service{DB: db}
}

func (s Service) ApplyWatchPolicy(ctx context.Context, req requestctx.Context, input WatchApplyInput) (WatchApplyResult, error) {
	plan, err := planFromWatchInput(input.Resolved, input.Plan)
	if err != nil {
		return WatchApplyResult{DryRun: input.DryRun, Plan: plan}, err
	}
	if input.DryRun {
		return WatchApplyResult{DryRun: true, Plan: plan}, nil
	}
	boxNode, err := s.resolveOwnerNode(ctx, plan.OwnerNode, input.Resolved)
	if err != nil {
		return WatchApplyResult{Plan: plan}, fmt.Errorf("resolve Box owner node %s: %w", plan.OwnerNode, err)
	}
	if boxNode.Status != "active" || boxNode.RetiredAt != nil {
		return WatchApplyResult{Plan: plan}, fmt.Errorf("Box owner node %s is not active", plan.OwnerNode)
	}
	scopes, err := s.ensureBoxWatchScopes(ctx, req, plan, boxNode)
	if err != nil {
		return WatchApplyResult{Plan: plan}, err
	}
	existing, err := s.listWatchRootRegistrations(ctx, plan.BoxID)
	if err != nil {
		return WatchApplyResult{Plan: plan, Scopes: scopes}, err
	}
	groups, err := groupWatchRootsByOwner(plan, existing)
	if err != nil {
		return WatchApplyResult{Plan: plan, Scopes: scopes}, err
	}
	resolvedNodes := map[string]nodes.Node{}
	for ownerRef := range groups {
		node, err := nodes.NewService(s.DB).GetNode(ctx, ownerRef)
		if err != nil {
			return WatchApplyResult{Plan: plan, Scopes: scopes}, fmt.Errorf("resolve watched-root owner node %s: %w", ownerRef, err)
		}
		if node.Status != "active" || node.RetiredAt != nil {
			return WatchApplyResult{Plan: plan, Scopes: scopes}, fmt.Errorf("watched-root owner node %s is not active", ownerRef)
		}
		resolvedNodes[ownerRef] = node
	}
	registrations := make([]WatchRootRegistration, 0, len(plan.WatchedRoots))
	nodeResults := make([]WatchNodeApplyResult, 0, len(groups))
	for _, ownerRef := range sortedWatchOwnerRefs(groups) {
		group := groups[ownerRef]
		node := resolvedNodes[ownerRef]
		nodeResult := WatchNodeApplyResult{NodeID: node.NodeID, OwnerNodeKey: node.NodeKey}
		for _, root := range group.Items {
			registration, err := s.upsertWatchRootRegistration(ctx, req, plan, node, root)
			if err != nil {
				return WatchApplyResult{Plan: plan, Scopes: scopes, Registrations: registrations, Nodes: nodeResults}, err
			}
			registrations = append(registrations, registration)
			nodeResult.Registrations = append(nodeResult.Registrations, registration)
		}
		for _, sourceKind := range sortedSourceKinds(group.SourceKinds) {
			nodeResult.SourceKinds = append(nodeResult.SourceKinds, sourceKind)
			if err := s.markStaleWatchRootRegistrations(ctx, plan, node.NodeID, sourceKind, activeBackendKeysForSource(group.Items, sourceKind)); err != nil {
				return WatchApplyResult{Plan: plan, Scopes: scopes, Registrations: registrations, Nodes: nodeResults}, err
			}
		}
		nodeResults = append(nodeResults, nodeResult)
	}
	activeKeys := activeBackendKeys(plan.WatchedRoots)
	if err := s.correlateWatchRootReports(ctx, plan.BoxID, activeKeys); err != nil {
		return WatchApplyResult{Plan: plan, Scopes: scopes, Registrations: registrations, Nodes: nodeResults}, err
	}
	registrations, err = s.listWatchRootRegistrations(ctx, plan.BoxID)
	if err != nil {
		return WatchApplyResult{Plan: plan, Scopes: scopes}, err
	}
	return WatchApplyResult{Plan: plan, Scopes: scopes, Registrations: registrations, Nodes: nodeResults}, nil
}

type watchOwnerGroup struct {
	Items       []projectcontracts.ProjectWatchedRootItem
	SourceKinds map[string]bool
}

func groupWatchRootsByOwner(plan WatchPlan, existing []WatchRootRegistration) (map[string]*watchOwnerGroup, error) {
	groups := map[string]*watchOwnerGroup{}
	for _, item := range plan.WatchedRoots {
		owner := strings.TrimSpace(item.OwnerNode)
		if owner == "" {
			owner = strings.TrimSpace(plan.OwnerNode)
		}
		if owner == "" {
			return nil, fmt.Errorf("watched-root %s has no owner node", item.BackendRootKey)
		}
		group := groups[owner]
		if group == nil {
			group = &watchOwnerGroup{SourceKinds: map[string]bool{}}
			groups[owner] = group
		}
		group.Items = append(group.Items, item)
		group.SourceKinds[watchRegistrationSourceKind(item)] = true
	}
	for _, registration := range existing {
		if registrationSourceKind(registration) != backupcontracts.SourceKindBoxBackupContract {
			continue
		}
		owner := strings.TrimSpace(registration.OwnerNodeKey)
		if owner == "" {
			owner = strings.TrimSpace(registration.NodeID)
		}
		if owner == "" {
			continue
		}
		group := groups[owner]
		if group == nil {
			group = &watchOwnerGroup{SourceKinds: map[string]bool{}}
			groups[owner] = group
		}
		group.SourceKinds[backupcontracts.SourceKindBoxBackupContract] = true
	}
	return groups, nil
}

func sortedWatchOwnerRefs(groups map[string]*watchOwnerGroup) []string {
	refs := make([]string, 0, len(groups))
	for ref := range groups {
		refs = append(refs, ref)
	}
	sort.Strings(refs)
	return refs
}

func sortedSourceKinds(kinds map[string]bool) []string {
	values := make([]string, 0, len(kinds))
	for kind := range kinds {
		values = append(values, kind)
	}
	sort.Strings(values)
	return values
}

func watchRegistrationSourceKind(item projectcontracts.ProjectWatchedRootItem) string {
	if backupcontracts.IsBackupRootItem(item) {
		return backupcontracts.SourceKindBoxBackupContract
	}
	return "box_policy"
}

func registrationSourceKind(registration WatchRootRegistration) string {
	if strings.TrimSpace(registration.SourceKind) != "" {
		return registration.SourceKind
	}
	if backupcontracts.IsBackupAreaKey(registration.AreaKey) {
		return backupcontracts.SourceKindBoxBackupContract
	}
	return "box_policy"
}

func activeBackendKeysForSource(items []projectcontracts.ProjectWatchedRootItem, sourceKind string) []string {
	filtered := make([]projectcontracts.ProjectWatchedRootItem, 0, len(items))
	for _, item := range items {
		if watchRegistrationSourceKind(item) == sourceKind {
			filtered = append(filtered, item)
		}
	}
	return activeBackendKeys(filtered)
}

func (s Service) WatchStatus(ctx context.Context, input WatchStatusInput) (WatchStatusResult, error) {
	plan, err := planFromWatchInput(input.Resolved, input.Plan)
	if err != nil {
		return WatchStatusResult{Plan: plan}, err
	}
	if err := s.correlateWatchRootReports(ctx, plan.BoxID, activeBackendKeys(plan.WatchedRoots)); err != nil {
		return WatchStatusResult{Plan: plan}, err
	}
	scopes, err := s.listBoxWatchScopes(ctx, plan.BoxID)
	if err != nil {
		return WatchStatusResult{Plan: plan}, err
	}
	registrations, err := s.listWatchRootRegistrations(ctx, plan.BoxID)
	if err != nil {
		return WatchStatusResult{Plan: plan, Scopes: scopes}, err
	}
	statuses := []watchedroots.RootStatus{}
	for _, registration := range registrations {
		rootStatuses, err := watchedroots.NewService(s.DB).ListStatus(ctx, watchedroots.StatusFilter{
			NodeRef: registration.NodeID,
			RootKey: registration.BackendRootKey,
			Limit:   1,
		})
		if err == nil && len(rootStatuses) > 0 {
			statuses = append(statuses, rootStatuses[0])
		}
	}
	return WatchStatusResult{Plan: plan, Scopes: scopes, Registrations: registrations, Statuses: statuses}, nil
}

func (s Service) resolveOwnerNode(ctx context.Context, ownerRef string, resolved Resolved) (nodes.Node, error) {
	nodeService := nodes.NewService(s.DB)
	node, err := nodeService.GetNode(ctx, ownerRef)
	if err == nil {
		return node, nil
	}
	if strings.EqualFold(strings.TrimSpace(resolved.NodeRole), ProfileMain) && !strings.EqualFold(strings.TrimSpace(ownerRef), "main") {
		fallback, fallbackErr := nodeService.GetNode(ctx, "main")
		if fallbackErr == nil {
			return fallback, nil
		}
	}
	return nodes.Node{}, err
}

func planFromWatchInput(resolved Resolved, provided *WatchPlan) (WatchPlan, error) {
	if provided == nil || (strings.TrimSpace(provided.BoxID) == "" && len(provided.WatchedRoots) == 0) {
		return BuildWatchPlan(WatchStatusInput{Resolved: resolved})
	}
	plan := *provided
	if strings.TrimSpace(plan.RootPath) == "" {
		plan.RootPath = resolved.RootPath
	}
	if strings.TrimSpace(plan.ProjectRoot) == "" {
		plan.ProjectRoot = plan.RootPath
	}
	if strings.TrimSpace(plan.Profile) == "" {
		plan.Profile = resolved.Profile
	}
	if strings.TrimSpace(plan.OwnerNode) == "" {
		plan.OwnerNode = resolved.OwnerNode
	}
	if strings.TrimSpace(plan.ContractPath) == "" && strings.TrimSpace(plan.RootPath) != "" {
		plan.ContractPath = ContractPath(plan.RootPath)
	}
	if strings.TrimSpace(plan.SchemaVersion) == "" {
		plan.SchemaVersion = SchemaVersion
	}
	if len(plan.Commands) == 0 && len(plan.WatchedRoots) > 0 {
		plan.Commands = boxWatchPlanCommands(plan.WatchedRoots)
	}
	if strings.TrimSpace(plan.BoxID) == "" {
		return plan, fmt.Errorf("box watch plan box_id is required")
	}
	if strings.TrimSpace(plan.OwnerNode) == "" {
		return plan, fmt.Errorf("box watch plan owner_node is required")
	}
	if len(plan.WatchedRoots) == 0 {
		return plan, fmt.Errorf("box watch plan has no watched roots")
	}
	return plan, nil
}

type boxWatchScopePlan struct {
	AreaKey     string
	ScopeKey    string
	Slug        string
	DisplayName string
	Metadata    json.RawMessage
}

func (s Service) ensureBoxWatchScopes(ctx context.Context, req requestctx.Context, plan WatchPlan, node nodes.Node) ([]WatchScope, error) {
	plans, err := boxWatchScopePlans(plan, node)
	if err != nil {
		return nil, err
	}
	scopes := make([]WatchScope, 0, len(plans))
	for _, scopePlan := range plans {
		scope, err := s.ensureBoxWatchScope(ctx, req, node, plan.BoxID, scopePlan)
		if err != nil {
			return scopes, err
		}
		scopes = append(scopes, scope)
	}
	return scopes, nil
}

func boxWatchScopePlans(plan WatchPlan, node nodes.Node) ([]boxWatchScopePlan, error) {
	plans := []boxWatchScopePlan{}
	seen := map[string]struct{}{}
	for _, root := range plan.WatchedRoots {
		config, err := boxWatchRootConfig(root)
		if err != nil {
			return nil, err
		}
		if config.SyncPolicy.Mode == agentwatchedroots.SyncModeNone && strings.TrimSpace(config.SyncPolicy.ScopeRef) == "" {
			continue
		}
		area, err := boxWatchAreaForItem(root)
		if err != nil {
			return nil, err
		}
		if config.SyncPolicy.Mode != agentwatchedroots.SyncModeSelectedFiles {
			return nil, fmt.Errorf("Box watched-root %s uses unsupported sync mode %q", root.BackendRootKey, config.SyncPolicy.Mode)
		}
		scopeKey := strings.TrimSpace(config.SyncPolicy.ScopeRef)
		expected := boxWatchScopeRef(plan.BoxID, area)
		if scopeKey != expected {
			return nil, fmt.Errorf("Box watched-root %s sync scope %q does not match expected %q", root.BackendRootKey, scopeKey, expected)
		}
		if _, exists := seen[scopeKey]; exists {
			continue
		}
		seen[scopeKey] = struct{}{}
		metadata, err := json.Marshal(map[string]any{
			"source":             "box.watch_policy",
			"box_id":             plan.BoxID,
			"box_area":           area,
			"box_root_path":      plan.RootPath,
			"box_contract_path":  plan.ContractPath,
			"owner_node_id":      node.NodeID,
			"owner_node_key":     node.NodeKey,
			"backend_root_key":   root.BackendRootKey,
			"local_root_key":     root.Key,
			"root_relative_path": root.RootRelativePath,
			"sync_mode":          root.SyncMode,
			"backup_mode":        root.BackupMode,
			"index_mode":         root.IndexMode,
			"delete_mode":        root.DeleteMode,
			"config_hash":        root.ConfigHash,
		})
		if err != nil {
			return nil, err
		}
		plans = append(plans, boxWatchScopePlan{
			AreaKey:     area,
			ScopeKey:    scopeKey,
			Slug:        boxWatchScopeSlug(plan.BoxID, area),
			DisplayName: root.DisplayName,
			Metadata:    metadata,
		})
	}
	return plans, nil
}

func boxWatchAreaForItem(item projectcontracts.ProjectWatchedRootItem) (string, error) {
	area := strings.ToLower(strings.TrimSpace(item.Key))
	if area == "" {
		area = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(item.BackendRootKey)), "loom_box__")
	}
	switch area {
	case AreaNotes, AreaDocuments, AreaLaunchpad, areaTopics, areaLibrary:
		return area, nil
	default:
		return "", fmt.Errorf("Box watched-root %s has unsupported area %q", item.BackendRootKey, area)
	}
}

func boxWatchRegistrationAreaKey(item projectcontracts.ProjectWatchedRootItem) (string, error) {
	area := strings.ToLower(strings.TrimSpace(item.Key))
	if area == "" {
		backendRootKey := strings.ToLower(strings.TrimSpace(item.BackendRootKey))
		if strings.HasPrefix(backendRootKey, "loom_box_backup__") {
			area = backupcontracts.AreaKey(strings.TrimPrefix(backendRootKey, "loom_box_backup__"))
		} else {
			area = strings.TrimPrefix(backendRootKey, "loom_box__")
		}
	}
	switch area {
	case AreaNotes, AreaDocuments, AreaLaunchpad, areaTopics, areaLibrary:
		return area, nil
	default:
		if backupcontracts.IsBackupAreaKey(area) {
			return area, nil
		}
		return "", fmt.Errorf("Box watched-root %s has unsupported registration area %q", item.BackendRootKey, area)
	}
}

func boxWatchRootConfig(item projectcontracts.ProjectWatchedRootItem) (agentwatchedroots.RootConfig, error) {
	if len(item.ConfigJSON) == 0 {
		return agentwatchedroots.RootConfig{}, fmt.Errorf("Box watched-root %s has no config_json", item.BackendRootKey)
	}
	var config agentwatchedroots.RootConfig
	if err := json.Unmarshal(item.ConfigJSON, &config); err != nil {
		return agentwatchedroots.RootConfig{}, fmt.Errorf("decode Box watched-root %s config_json: %w", item.BackendRootKey, err)
	}
	return agentwatchedroots.NormalizeRootConfig(config), nil
}

func boxWatchScopeSlug(boxID, area string) string {
	raw := "box-" + strings.TrimPrefix(strings.ToLower(strings.TrimSpace(boxID)), "box_") + "-" + strings.ToLower(strings.TrimSpace(area))
	var builder strings.Builder
	lastDash := false
	for _, ch := range raw {
		isAlnum := (ch >= 'a' && ch <= 'z') || (ch >= '0' && ch <= '9')
		if isAlnum {
			builder.WriteRune(ch)
			lastDash = false
			continue
		}
		if !lastDash {
			builder.WriteByte('-')
			lastDash = true
		}
	}
	slug := strings.Trim(builder.String(), "-")
	if len(slug) > 64 {
		slug = strings.Trim(slug[:64], "-")
	}
	if len(slug) < 3 {
		return "box-area"
	}
	return slug
}

func (s Service) ensureBoxWatchScope(ctx context.Context, req requestctx.Context, node nodes.Node, boxID string, plan boxWatchScopePlan) (WatchScope, error) {
	existing, err := s.getBoxWatchScopeByKey(ctx, plan.ScopeKey)
	if err == nil {
		if existing.ScopeType != boxAreaScopeType {
			return WatchScope{}, fmt.Errorf("scope %s exists with type %q, expected %q", plan.ScopeKey, existing.ScopeType, boxAreaScopeType)
		}
		return s.updateBoxWatchScope(ctx, req, node, boxID, plan, existing.ScopeID)
	}
	if err != sql.ErrNoRows {
		return WatchScope{}, err
	}
	return s.insertBoxWatchScope(ctx, req, node, boxID, plan)
}

func (s Service) insertBoxWatchScope(ctx context.Context, req requestctx.Context, node nodes.Node, boxID string, plan boxWatchScopePlan) (WatchScope, error) {
	ownerActorID := ""
	if node.OwnerActorID != nil {
		ownerActorID = *node.OwnerActorID
	}
	parentScopeID := any(nil)
	if node.HomeScopeID != nil {
		parentScopeID = *node.HomeScopeID
	}
	scope, err := scanWatchScope(s.DB.QueryRowContext(ctx, `
		INSERT INTO scopes.scopes (
			scope_id, scope_type, scope_key, slug, display_name,
			owner_actor_id, home_node_id, parent_scope_id, status,
			created_by_actor_id, metadata
		)
		VALUES ($1, $2, $3, $4, $5, nullif($6, ''), nullif($7, ''), $8, 'active', nullif($9, ''), $10)
		RETURNING scope_id, scope_type, scope_key, slug, display_name, metadata
	`, ids.NewScopeID(), boxAreaScopeType, plan.ScopeKey, plan.Slug, plan.DisplayName, ownerActorID, node.NodeID, parentScopeID, req.ActorID, plan.Metadata), true)
	if err != nil {
		return WatchScope{}, err
	}
	scope.AreaKey = plan.AreaKey
	scope.BoxID = boxID
	return scope, nil
}

func (s Service) updateBoxWatchScope(ctx context.Context, req requestctx.Context, node nodes.Node, boxID string, plan boxWatchScopePlan, scopeID string) (WatchScope, error) {
	ownerActorID := ""
	if node.OwnerActorID != nil {
		ownerActorID = *node.OwnerActorID
	}
	parentScopeID := any(nil)
	if node.HomeScopeID != nil {
		parentScopeID = *node.HomeScopeID
	}
	scope, err := scanWatchScope(s.DB.QueryRowContext(ctx, `
		UPDATE scopes.scopes
		SET slug = $2,
		    display_name = $3,
		    owner_actor_id = nullif($4, ''),
		    home_node_id = nullif($5, ''),
		    parent_scope_id = $6,
		    created_by_actor_id = COALESCE(created_by_actor_id, nullif($7, '')),
		    metadata = metadata || $8::jsonb,
		    updated_at = now()
		WHERE scope_id = $1
		RETURNING scope_id, scope_type, scope_key, slug, display_name, metadata
	`, scopeID, plan.Slug, plan.DisplayName, ownerActorID, node.NodeID, parentScopeID, req.ActorID, plan.Metadata), false)
	if err != nil {
		return WatchScope{}, err
	}
	scope.AreaKey = plan.AreaKey
	scope.BoxID = boxID
	return scope, nil
}

func (s Service) getBoxWatchScopeByKey(ctx context.Context, scopeKey string) (WatchScope, error) {
	return scanWatchScope(s.DB.QueryRowContext(ctx, `
		SELECT scope_id, scope_type, scope_key, slug, display_name, metadata
		FROM scopes.scopes
		WHERE scope_key = $1
	`, scopeKey), false)
}

func (s Service) listBoxWatchScopes(ctx context.Context, boxID string) ([]WatchScope, error) {
	rows, err := s.DB.QueryContext(ctx, `
		SELECT scope_id, scope_type, scope_key, slug, display_name, metadata
		FROM scopes.scopes
		WHERE scope_type = $1
		  AND metadata->>'box_id' = $2
		ORDER BY metadata->>'box_area', scope_key
	`, boxAreaScopeType, strings.TrimSpace(boxID))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	scopes := []WatchScope{}
	for rows.Next() {
		scope, err := scanWatchScope(rows, false)
		if err != nil {
			return nil, err
		}
		scopes = append(scopes, scope)
	}
	return scopes, rows.Err()
}

type watchScopeScanner interface {
	Scan(dest ...any) error
}

func scanWatchScope(scanner watchScopeScanner, created bool) (WatchScope, error) {
	var scope WatchScope
	var metadata []byte
	if err := scanner.Scan(
		&scope.ScopeID,
		&scope.ScopeType,
		&scope.ScopeKey,
		&scope.Slug,
		&scope.DisplayName,
		&metadata,
	); err != nil {
		return WatchScope{}, err
	}
	scope.Created = created
	scope.Metadata = json.RawMessage(metadata)
	if len(scope.Metadata) == 0 {
		scope.Metadata = json.RawMessage(`{}`)
	}
	var meta map[string]any
	if err := json.Unmarshal(scope.Metadata, &meta); err == nil {
		if value, ok := meta["box_id"].(string); ok {
			scope.BoxID = value
		}
		if value, ok := meta["box_area"].(string); ok {
			scope.AreaKey = value
		}
	}
	return scope, nil
}

func (s Service) upsertWatchRootRegistration(ctx context.Context, req requestctx.Context, plan WatchPlan, node nodes.Node, item projectcontracts.ProjectWatchedRootItem) (WatchRootRegistration, error) {
	sourceKinds, err := json.Marshal(item.SourceKinds)
	if err != nil {
		return WatchRootRegistration{}, err
	}
	agentCommands := item.AgentCommands
	if agentCommands == nil {
		agentCommands = []projectcontracts.ProjectWatchedRootCommand{}
	}
	commands, err := json.Marshal(agentCommands)
	if err != nil {
		return WatchRootRegistration{}, err
	}
	metadata, err := json.Marshal(item.Metadata)
	if err != nil {
		return WatchRootRegistration{}, err
	}
	status := strings.TrimSpace(item.ActivationStatus)
	if status == "" {
		status = BoxWatchStatusPendingAgentApply
	}
	area, err := boxWatchRegistrationAreaKey(item)
	if err != nil {
		return WatchRootRegistration{}, err
	}
	sourceKind := watchRegistrationSourceKind(item)
	sourceContractKey, _ := item.Metadata["contract_key"].(string)
	sourceContractPath, _ := item.Metadata["contract_path"].(string)
	row := s.DB.QueryRowContext(ctx, `
		INSERT INTO box.watch_root_registrations (
			box_watch_root_registration_id, box_id, box_root_path, box_contract_path,
			node_id, owner_node_key, area_key, local_root_key, backend_root_key,
			worker_key, source_kinds_json, safe_root_key, root_relative_path,
			display_name, sync_mode, backup_mode, index_mode, delete_mode,
			config_hash, config_json, command_json, activation_status,
			source_kind, source_contract_key, source_contract_path,
			desired_revision, desired_config_hash, metadata
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10,
		        $11, $12, $13, $14, $15, $16, $17, $18, $19,
		        $20, $21, $22, $23, $24, $25, 1, $26, $27)
		ON CONFLICT (node_id, box_id, backend_root_key) DO UPDATE
		SET box_root_path = EXCLUDED.box_root_path,
		    box_contract_path = EXCLUDED.box_contract_path,
		    owner_node_key = EXCLUDED.owner_node_key,
		    area_key = EXCLUDED.area_key,
		    local_root_key = EXCLUDED.local_root_key,
		    worker_key = EXCLUDED.worker_key,
		    source_kinds_json = EXCLUDED.source_kinds_json,
		    safe_root_key = EXCLUDED.safe_root_key,
		    root_relative_path = EXCLUDED.root_relative_path,
		    display_name = EXCLUDED.display_name,
		    sync_mode = EXCLUDED.sync_mode,
		    backup_mode = EXCLUDED.backup_mode,
		    index_mode = EXCLUDED.index_mode,
		    delete_mode = EXCLUDED.delete_mode,
		    config_hash = EXCLUDED.config_hash,
		    config_json = EXCLUDED.config_json,
		    command_json = EXCLUDED.command_json,
		    activation_status = CASE
		      WHEN EXCLUDED.activation_status IN ('pending_agent_apply', 'applied', 'reported')
		       AND box.watch_root_registrations.source_kind = 'box_policy'
		       AND EXCLUDED.source_kind = 'box_policy'
		       AND box.watch_root_registrations.area_key NOT LIKE 'backup\_%' ESCAPE '\'
		       AND EXCLUDED.area_key NOT LIKE 'backup\_%' ESCAPE '\'
		       AND box.watch_root_registrations.source_contract_deleted_at IS NULL
		       AND box.watch_root_registrations.config_hash = EXCLUDED.config_hash
		       AND EXCLUDED.config_hash <> ''
		       AND box.watch_root_registrations.activation_status = 'reported'
		      THEN 'reported'
		      WHEN EXCLUDED.activation_status IN ('pending_agent_apply', 'applied', 'reported')
		       AND box.watch_root_registrations.source_kind = EXCLUDED.source_kind
		       AND (EXCLUDED.source_kind = 'box_backup_contract' OR EXCLUDED.area_key LIKE 'backup\_%' ESCAPE '\')
		       AND box.watch_root_registrations.desired_config_hash = EXCLUDED.desired_config_hash
		       AND box.watch_root_registrations.applied_revision = box.watch_root_registrations.desired_revision
		       AND box.watch_root_registrations.applied_config_hash = EXCLUDED.desired_config_hash
		       AND box.watch_root_registrations.activation_status IN ('applied', 'reported')
		      THEN box.watch_root_registrations.activation_status
		      ELSE EXCLUDED.activation_status
		    END,
		    source_kind = EXCLUDED.source_kind,
		    source_contract_key = EXCLUDED.source_contract_key,
		    source_contract_path = EXCLUDED.source_contract_path,
		    source_contract_deleted_at = NULL,
		    desired_revision = CASE
		      WHEN box.watch_root_registrations.desired_config_hash = EXCLUDED.desired_config_hash
		      THEN box.watch_root_registrations.desired_revision
		      ELSE GREATEST(box.watch_root_registrations.desired_revision + 1, 1)
		    END,
		    desired_config_hash = EXCLUDED.desired_config_hash,
		    metadata = EXCLUDED.metadata,
		    updated_at = now()
		RETURNING `+watchRootRegistrationColumns(),
		ids.NewBoxWatchRootRegistrationID(),
		plan.BoxID,
		plan.RootPath,
		plan.ContractPath,
		node.NodeID,
		node.NodeKey,
		area,
		item.Key,
		item.BackendRootKey,
		item.WorkerKey,
		sourceKinds,
		item.SafeRootKey,
		item.RootRelativePath,
		item.DisplayName,
		item.SyncMode,
		item.BackupMode,
		item.IndexMode,
		item.DeleteMode,
		item.ConfigHash,
		item.ConfigJSON,
		commands,
		status,
		sourceKind,
		sourceContractKey,
		sourceContractPath,
		item.ConfigHash,
		metadata,
	)
	return scanWatchRootRegistration(row)
}

// MarkBackupContractDeleted preserves the registration as tombstone/audit
// evidence while distinguishing an intentional contract removal from a YAML
// file that disappeared unexpectedly.
func (s Service) MarkBackupContractDeleted(ctx context.Context, boxRoot, contractKey string) error {
	boxRoot = strings.TrimSpace(boxRoot)
	contractKey = strings.ToLower(strings.TrimSpace(contractKey))
	if boxRoot == "" {
		return errors.New("box root path is required")
	}
	if err := backupcontracts.ValidateKey(contractKey); err != nil {
		return err
	}
	_, err := s.DB.ExecContext(ctx, `
		UPDATE box.watch_root_registrations
		SET source_contract_deleted_at = COALESCE(source_contract_deleted_at, now()),
		    updated_at = now()
		WHERE box_root_path = $1
		  AND source_contract_key = $2
		  AND (source_kind = $3 OR area_key LIKE 'backup\_%' ESCAPE '\')
	`, boxRoot, contractKey, backupcontracts.SourceKindBoxBackupContract)
	return err
}

func (s Service) listWatchRootRegistrations(ctx context.Context, boxID string) ([]WatchRootRegistration, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT `+watchRootRegistrationColumns()+`
		FROM box.watch_root_registrations
		WHERE box_id = $1
		ORDER BY backend_root_key`, boxID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []WatchRootRegistration{}
	for rows.Next() {
		registration, err := scanWatchRootRegistration(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, registration)
	}
	return out, rows.Err()
}

func (s Service) markStaleWatchRootRegistrations(ctx context.Context, plan WatchPlan, nodeID string, sourceKind string, activeKeys []string) error {
	sourcePredicate := "source_kind = $4"
	if sourceKind == backupcontracts.SourceKindBoxBackupContract {
		sourcePredicate = "(source_kind = $4 OR area_key LIKE 'backup\\_%' ESCAPE '\\')"
	} else if sourceKind == "box_policy" {
		sourcePredicate = "(source_kind = $4 OR area_key IN ('notes', 'documents', 'launchpad'))"
	}
	if len(activeKeys) == 0 {
		_, err := s.DB.ExecContext(ctx, `
			UPDATE box.watch_root_registrations
			SET activation_status = $1, updated_at = now()
			WHERE box_id = $2 AND node_id = $3 AND `+sourcePredicate+` AND activation_status <> $5
		`, BoxWatchStatusStale, plan.BoxID, nodeID, sourceKind, BoxWatchStatusDisabled)
		return err
	}
	args := []any{BoxWatchStatusStale, plan.BoxID, nodeID, sourceKind, BoxWatchStatusDisabled}
	placeholders := make([]string, 0, len(activeKeys))
	for _, key := range activeKeys {
		args = append(args, key)
		placeholders = append(placeholders, fmt.Sprintf("$%d", len(args)))
	}
	_, err := s.DB.ExecContext(ctx, `
		UPDATE box.watch_root_registrations
		SET activation_status = $1, updated_at = now()
		WHERE box_id = $2
		  AND node_id = $3
		  AND `+sourcePredicate+`
		  AND activation_status <> $5
		  AND backend_root_key NOT IN (`+strings.Join(placeholders, ", ")+`)`, args...)
	return err
}

func (s Service) correlateWatchRootReports(ctx context.Context, boxID string, activeKeys []string) error {
	args := []any{BoxWatchStatusReported, BoxWatchStatusStale, boxID}
	activePredicate := "false"
	if len(activeKeys) > 0 {
		placeholders := make([]string, 0, len(activeKeys))
		for _, key := range activeKeys {
			args = append(args, key)
			placeholders = append(placeholders, fmt.Sprintf("$%d", len(args)))
		}
		activePredicate = "b.backend_root_key IN (" + strings.Join(placeholders, ", ") + ")"
	}
	// Match Report's root-before-registration lock order. An UPDATE waiting on
	// a registration must not correlate a pre-wait snapshot of an older report.
	_, err := s.DB.ExecContext(ctx, `
		WITH reports AS MATERIALIZED (
		  SELECT wr.* FROM watched_roots.roots wr
		  WHERE EXISTS (SELECT 1 FROM box.watch_root_registrations b
		                WHERE b.box_id = $3 AND b.node_id = wr.node_id
		                  AND b.backend_root_key = wr.root_key)
		  ORDER BY wr.watched_root_id
		  FOR UPDATE OF wr
		)
		UPDATE box.watch_root_registrations b
		SET watched_root_id = wr.watched_root_id,
		    last_reported_at = wr.last_reported_at,
		    activation_status = CASE
		      WHEN `+activePredicate+`
		       AND b.activation_status NOT IN ('disabled', 'blocked')
		       AND b.source_contract_deleted_at IS NULL THEN
		        CASE
		          WHEN (
		            (b.source_kind = '`+backupcontracts.SourceKindBoxBackupContract+`' OR b.area_key LIKE 'backup\_%' ESCAPE '\')
		            AND wr.config_hash = b.desired_config_hash
		            AND b.applied_revision = b.desired_revision
		            AND b.applied_config_hash = b.desired_config_hash
		          ) OR (
		            b.source_kind <> '`+backupcontracts.SourceKindBoxBackupContract+`'
		            AND b.area_key NOT LIKE 'backup\_%' ESCAPE '\'
		            AND wr.config_hash = b.config_hash
			          ) THEN $1
			          WHEN b.source_kind = '`+backupcontracts.SourceKindBoxBackupContract+`'
			            OR b.area_key LIKE 'backup\_%' ESCAPE '\'
			          THEN b.activation_status
			          ELSE $2
			        END
		      ELSE b.activation_status
		    END,
		    updated_at = now()
		FROM reports wr
		WHERE b.box_id = $3
		  AND wr.node_id = b.node_id
		  AND wr.root_key = b.backend_root_key
	`, args...)
	return err
}

func watchRootRegistrationColumns() string {
	return `box_watch_root_registration_id, box_id, box_root_path, box_contract_path,
		node_id, owner_node_key, area_key, local_root_key, backend_root_key,
		worker_key, source_kinds_json, safe_root_key, root_relative_path,
		display_name, sync_mode, backup_mode, index_mode, delete_mode,
		config_hash, config_json, command_json, watched_root_id,
		activation_status, last_applied_by_actor_id, last_applied_at,
		last_reported_at, source_kind, source_contract_key, source_contract_path,
		source_contract_deleted_at,
		desired_revision, applied_revision, desired_config_hash, applied_config_hash,
		reconciliation_message_id, last_node_ack_status, last_node_acknowledged_at,
		last_apply_error_code, last_apply_error_message,
		metadata, created_at, updated_at`
}

type watchRootRegistrationScanner interface {
	Scan(dest ...any) error
}

func scanWatchRootRegistration(scanner watchRootRegistrationScanner) (WatchRootRegistration, error) {
	var registration WatchRootRegistration
	if err := scanner.Scan(
		&registration.BoxWatchRootRegistrationID,
		&registration.BoxID,
		&registration.BoxRootPath,
		&registration.BoxContractPath,
		&registration.NodeID,
		&registration.OwnerNodeKey,
		&registration.AreaKey,
		&registration.LocalRootKey,
		&registration.BackendRootKey,
		&registration.WorkerKey,
		&registration.SourceKinds,
		&registration.SafeRootKey,
		&registration.RootRelativePath,
		&registration.DisplayName,
		&registration.SyncMode,
		&registration.BackupMode,
		&registration.IndexMode,
		&registration.DeleteMode,
		&registration.ConfigHash,
		&registration.ConfigJSON,
		&registration.CommandJSON,
		&registration.WatchedRootID,
		&registration.ActivationStatus,
		&registration.LastAppliedByActorID,
		&registration.LastAppliedAt,
		&registration.LastReportedAt,
		&registration.SourceKind,
		&registration.SourceContractKey,
		&registration.SourceContractPath,
		&registration.SourceContractDeletedAt,
		&registration.DesiredRevision,
		&registration.AppliedRevision,
		&registration.DesiredConfigHash,
		&registration.AppliedConfigHash,
		&registration.ReconciliationMessageID,
		&registration.LastNodeAckStatus,
		&registration.LastNodeAcknowledgedAt,
		&registration.LastApplyErrorCode,
		&registration.LastApplyErrorMessage,
		&registration.Metadata,
		&registration.CreatedAt,
		&registration.UpdatedAt,
	); err != nil {
		return WatchRootRegistration{}, err
	}
	return registration, nil
}

func activeBackendKeys(items []projectcontracts.ProjectWatchedRootItem) []string {
	keys := make([]string, 0, len(items))
	for _, item := range items {
		if strings.TrimSpace(item.BackendRootKey) != "" {
			keys = append(keys, item.BackendRootKey)
		}
	}
	sort.Strings(keys)
	return keys
}
