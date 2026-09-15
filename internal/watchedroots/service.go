package watchedroots

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"loom.local/loom/internal/filesystemmeta"
	"loom.local/loom/internal/filetransfer"
	"loom.local/loom/internal/ids"
	"loom.local/loom/internal/nodes"
	"loom.local/loom/internal/projects"
	"loom.local/loom/internal/storagecatalog"
)

var ErrInvalid = errors.New("invalid watched-root input")

type Service struct {
	DB         *sql.DB
	BackupRoot string
}

func NewService(db *sql.DB) Service {
	return Service{DB: db}
}

func NewServiceWithBackupRoot(db *sql.DB, backupRoot string) Service {
	return Service{DB: db, BackupRoot: strings.TrimSpace(backupRoot)}
}

func (s Service) Report(ctx context.Context, input ReportInput) (ReportResult, error) {
	input, err := normalizeReportInput(input)
	if err != nil {
		return ReportResult{}, err
	}
	if input.CredentialToken == "" {
		return ReportResult{}, fmt.Errorf("%w: credential_token is required", ErrInvalid)
	}
	nodeService := nodes.NewService(s.DB)
	_, node, err := nodeService.AuthenticateCredential(ctx, input.CredentialToken)
	if err != nil {
		return ReportResult{}, err
	}
	if input.NodeRef != "" && input.NodeRef != node.NodeID && input.NodeRef != node.NodeKey {
		return ReportResult{}, fmt.Errorf("%w: credential does not belong to node %s", ErrInvalid, input.NodeRef)
	}

	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return ReportResult{}, err
	}
	defer tx.Rollback()

	root, err := upsertRootTx(ctx, tx, node.NodeID, input)
	if err != nil {
		return ReportResult{}, err
	}
	findings := make([]Finding, 0, len(input.Findings))
	reportedKeys := map[string]struct{}{}
	for _, findingInput := range input.Findings {
		finding, err := upsertFindingTx(ctx, tx, root.WatchedRootID, node.NodeID, input.RootKey, findingInput)
		if err != nil {
			return ReportResult{}, err
		}
		reportedKeys[finding.FindingKey] = struct{}{}
		findings = append(findings, finding)
	}
	resolvedCount := 0
	if input.ResolveMissingFindings {
		resolvedCount, err = resolveMissingFindingsTx(ctx, tx, root.WatchedRootID, reportedKeys)
		if err != nil {
			return ReportResult{}, err
		}
	}
	if err := correlateOrdinaryBoxReportTx(ctx, tx, node, root); err != nil {
		return ReportResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return ReportResult{}, err
	}
	return ReportResult{Root: root, Findings: findings, ResolvedCount: resolvedCount}, nil
}

// Ordinary Box policies have no backup-contract revision acknowledgement.
// Commit their exact owner report and activation together; a later mismatched
// report withdraws activation until the current configuration is reported.
func correlateOrdinaryBoxReportTx(ctx context.Context, tx *sql.Tx, node nodes.Node, root WatchedRoot) error {
	_, err := tx.ExecContext(ctx, `
		UPDATE box.watch_root_registrations
		SET watched_root_id = $1, last_reported_at = $2,
		    activation_status = CASE
		      WHEN config_hash <> '' AND config_hash = $3 THEN 'reported'
		      ELSE 'pending_agent_apply'
		    END,
		    updated_at = now()
		WHERE node_id = $4 AND owner_node_key = $5 AND backend_root_key = $6
		  AND source_kind = 'box_policy' AND area_key NOT LIKE 'backup\_%' ESCAPE '\'
		  AND source_contract_deleted_at IS NULL
		  AND activation_status IN ('registered', 'pending_agent_apply', 'applied', 'reported')
	`, root.WatchedRootID, root.LastReportedAt, root.ConfigHash, node.NodeID, node.NodeKey, root.RootKey)
	return err
}

func (s Service) ListStatus(ctx context.Context, filter StatusFilter) ([]RootStatus, error) {
	roots, err := s.listRoots(ctx, filter)
	if err != nil {
		return nil, err
	}
	statuses := make([]RootStatus, 0, len(roots))
	for _, root := range roots {
		findings, err := s.listFindingsForRoot(ctx, root.WatchedRootID, FindingFilter{Status: FindingStatusOpen, Limit: 10})
		if err != nil {
			return nil, err
		}
		statuses = append(statuses, RootStatus{Root: root, LatestFindings: findings})
	}
	return statuses, nil
}

// GetLatestEvidence selects exactly one node/root generation and at most its
// latest backup. Callers cannot accidentally correlate another node's report.
func (s Service) GetLatestEvidence(ctx context.Context, nodeRef, rootKey string) (LatestEvidence, error) {
	nodeID, err := nodes.NewService(s.DB).ResolveNodeRef(ctx, strings.TrimSpace(nodeRef))
	if err != nil {
		return LatestEvidence{}, err
	}
	roots, err := s.listRoots(ctx, StatusFilter{NodeRef: nodeID, RootKey: strings.TrimSpace(rootKey), Limit: 1})
	if err != nil {
		return LatestEvidence{}, err
	}
	if len(roots) == 0 {
		return LatestEvidence{}, sql.ErrNoRows
	}
	root := roots[0]
	var open int
	if err := s.DB.QueryRowContext(ctx, `SELECT count(*) FROM watched_roots.findings WHERE watched_root_id = $1 AND status = 'open' AND severity IN ('error','critical')`, root.WatchedRootID).Scan(&open); err != nil {
		return LatestEvidence{}, err
	}
	evidence := LatestEvidence{Root: root, OpenFindings: open}
	batch, err := s.latestBackupBatchForRoot(ctx, root.WatchedRootID)
	if err == nil {
		evidence.LatestBackup = &batch
	} else if err != sql.ErrNoRows {
		return LatestEvidence{}, err
	}
	return evidence, nil
}

func (s Service) ListFindings(ctx context.Context, filter FindingFilter) ([]Finding, error) {
	filter.NodeRef = strings.TrimSpace(filter.NodeRef)
	filter.RootKey = strings.TrimSpace(filter.RootKey)
	filter.ProjectRef = strings.TrimSpace(filter.ProjectRef)
	filter.Status = strings.TrimSpace(filter.Status)
	filter.Severity = strings.TrimSpace(filter.Severity)
	if filter.Limit <= 0 {
		filter.Limit = 50
	}
	if filter.Limit > 200 {
		filter.Limit = 200
	}

	args := []any{}
	where := []string{"1 = 1"}
	projectJoin := ""
	if filter.ProjectRef != "" {
		projectID, err := s.resolveProjectID(ctx, filter.ProjectRef)
		if err != nil {
			return nil, err
		}
		args = append(args, projectID)
		where = append(where, fmt.Sprintf("pwr.project_id = $%d", len(args)))
		projectJoin = `
		JOIN projects.project_watched_root_registrations pwr
		  ON pwr.node_id = f.node_id
		 AND pwr.backend_root_key = f.root_key`
	}
	if filter.NodeRef != "" {
		nodeID, err := nodes.NewService(s.DB).ResolveNodeRef(ctx, filter.NodeRef)
		if err != nil {
			return nil, err
		}
		args = append(args, nodeID)
		where = append(where, fmt.Sprintf("f.node_id = $%d", len(args)))
	}
	if filter.RootKey != "" {
		args = append(args, filter.RootKey)
		where = append(where, fmt.Sprintf("f.root_key = $%d", len(args)))
	}
	if filter.Status != "" {
		args = append(args, filter.Status)
		where = append(where, fmt.Sprintf("f.status = $%d", len(args)))
	}
	if filter.Severity != "" {
		args = append(args, filter.Severity)
		where = append(where, fmt.Sprintf("f.severity = $%d", len(args)))
	}
	args = append(args, filter.Limit)
	query := `SELECT ` + findingColumns("f") + `
		FROM watched_roots.findings f` + projectJoin + `
		WHERE ` + strings.Join(where, " AND ") + `
		ORDER BY f.updated_at DESC
		LIMIT $` + fmt.Sprintf("%d", len(args))
	rows, err := s.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanFindings(rows)
}

func (s Service) RecordBackupBatch(ctx context.Context, input BackupBatchInput) (BackupBatchResult, error) {
	input, err := normalizeBackupBatchInput(input)
	if err != nil {
		return BackupBatchResult{}, err
	}
	if input.CredentialToken == "" {
		return BackupBatchResult{}, fmt.Errorf("%w: credential_token is required", ErrInvalid)
	}
	nodeService := nodes.NewService(s.DB)
	_, node, err := nodeService.AuthenticateCredential(ctx, input.CredentialToken)
	if err != nil {
		return BackupBatchResult{}, err
	}
	if input.NodeRef != "" && input.NodeRef != node.NodeID && input.NodeRef != node.NodeKey {
		return BackupBatchResult{}, fmt.Errorf("%w: credential does not belong to node %s", ErrInvalid, input.NodeRef)
	}
	if input.IdempotencyKey != "" {
		if existing, err := s.getBackupBatchByIdempotency(ctx, node.NodeID, input.IdempotencyKey); err == nil {
			root, rootErr := s.getRootByID(ctx, existing.WatchedRootID)
			if rootErr != nil {
				return BackupBatchResult{}, rootErr
			}
			items, itemsErr := s.listBackupItemsForBatch(ctx, existing.WatchedRootBackupBatchID)
			if itemsErr != nil {
				return BackupBatchResult{}, itemsErr
			}
			localBatchID, localBatchErr := backupInputLocalBatchID(existing.Metadata)
			if localBatchErr != nil {
				return BackupBatchResult{}, localBatchErr
			}
			if err := verifyBackupArtifacts(ctx, s.DB, s.BackupRoot, root.RootKey, localBatchID, backupInputsFromStoredItems(items), node); err != nil {
				return BackupBatchResult{}, err
			}
			if err := writeBackupCustodyManifest(s.BackupRoot, node, root, existing, items); err != nil {
				return BackupBatchResult{}, err
			}
			return BackupBatchResult{Root: root, Batch: existing, Items: items}, nil
		} else if err != sql.ErrNoRows {
			return BackupBatchResult{}, err
		}
	}

	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return BackupBatchResult{}, err
	}
	defer tx.Rollback()

	root, err := ensureRootForBackupTx(ctx, tx, node.NodeID, input)
	if err != nil {
		return BackupBatchResult{}, err
	}
	storageContext, err := resolveBackupStorageContextTx(ctx, tx, node, root)
	if err != nil {
		return BackupBatchResult{}, err
	}
	localBatchID, err := backupInputLocalBatchID(input.Metadata)
	if err != nil {
		return BackupBatchResult{}, err
	}
	if err := verifyBackupArtifacts(ctx, tx, s.BackupRoot, input.RootKey, localBatchID, input.Items, node); err != nil {
		return BackupBatchResult{}, err
	}
	counts := backupBatchCounts(input.Items)
	batchStatus := backupBatchStatus(counts, len(input.Items))
	batch, err := insertBackupBatchTx(ctx, tx, root, node.NodeID, input, counts, batchStatus)
	if err != nil {
		return BackupBatchResult{}, err
	}
	items := make([]BackupItem, 0, len(input.Items))
	for _, itemInput := range input.Items {
		item, err := insertBackupItemTx(ctx, tx, root, node.NodeID, batch.WatchedRootBackupBatchID, itemInput)
		if err != nil {
			return BackupBatchResult{}, err
		}
		if err := registerBackupItemStorageCatalogTx(ctx, tx, node, root, batch, item, storageContext); err != nil {
			return BackupBatchResult{}, err
		}
		items = append(items, item)
	}
	if err := tx.Commit(); err != nil {
		return BackupBatchResult{}, err
	}
	if err := writeBackupCustodyManifest(s.BackupRoot, node, root, batch, items); err != nil {
		return BackupBatchResult{}, err
	}
	return BackupBatchResult{Root: root, Batch: batch, Items: items}, nil
}

type backupCustodyManifest struct {
	SchemaVersion string       `json:"schema_version"`
	SourceNodeID  string       `json:"source_node_id"`
	SourceNodeKey string       `json:"source_node_key"`
	RootKey       string       `json:"root_key"`
	LocalBatchID  string       `json:"local_batch_id"`
	MainBatchID   string       `json:"watched_root_backup_batch_id"`
	BatchKind     string       `json:"batch_kind"`
	BackupMode    string       `json:"backup_mode"`
	Status        string       `json:"status"`
	ItemCount     int          `json:"item_count"`
	ReceivedAt    time.Time    `json:"received_at"`
	Items         []BackupItem `json:"items"`
}

func writeBackupCustodyManifest(backupRoot string, node nodes.Node, root WatchedRoot, batch BackupBatch, items []BackupItem) error {
	backupRoot = strings.TrimSpace(backupRoot)
	if backupRoot == "" {
		return nil
	}
	nodeKey, err := canonicalBackupPathSegment("source node key", node.NodeKey)
	if err != nil {
		return err
	}
	rootKey, err := canonicalBackupPathSegment("watched root key", root.RootKey)
	if err != nil {
		return err
	}
	batchKey, err := canonicalBackupPathSegment("backup batch key", backupBatchKey(batch))
	if err != nil {
		return err
	}
	document := backupCustodyManifest{
		SchemaVersion: "storage.watched_root_backup_manifest.v0.7",
		SourceNodeID:  node.NodeID,
		SourceNodeKey: node.NodeKey,
		RootKey:       root.RootKey,
		LocalBatchID:  batchKey,
		MainBatchID:   batch.WatchedRootBackupBatchID,
		BatchKind:     batch.BatchKind,
		BackupMode:    batch.BackupMode,
		Status:        batch.Status,
		ItemCount:     len(items),
		ReceivedAt:    batch.ReceivedAt.UTC(),
		Items:         items,
	}
	payload, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return fmt.Errorf("encode backup custody manifest: %w", err)
	}
	payload = append(payload, '\n')
	if err := os.MkdirAll(backupRoot, 0o770); err != nil {
		return fmt.Errorf("create backup custody root: %w", err)
	}
	rootInfo, err := os.Lstat(backupRoot)
	if err != nil {
		return fmt.Errorf("inspect backup custody root: %w", err)
	}
	if rootInfo.Mode()&os.ModeSymlink != 0 || !rootInfo.IsDir() {
		return fmt.Errorf("backup custody root must be a real directory")
	}
	confined, err := os.OpenRoot(backupRoot)
	if err != nil {
		return fmt.Errorf("open backup custody root: %w", err)
	}
	defer confined.Close()
	directory := filepath.Join(nodeKey, rootKey, batchKey)
	if err := confined.MkdirAll(directory, 0o770); err != nil {
		return fmt.Errorf("create backup custody batch directory: %w", err)
	}
	for _, component := range []string{nodeKey, filepath.Join(nodeKey, rootKey), directory} {
		info, err := confined.Lstat(component)
		if err != nil {
			return fmt.Errorf("inspect backup custody component: %w", err)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("backup custody component %q must be a real directory", component)
		}
	}
	manifestPath := filepath.Join(directory, "manifest.json")
	if existing, err := confined.ReadFile(manifestPath); err == nil {
		if !bytes.Equal(existing, payload) {
			return fmt.Errorf("backup custody manifest already exists with different evidence")
		}
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect backup custody manifest: %w", err)
	}
	tempSuffix := make([]byte, 8)
	if _, err := rand.Read(tempSuffix); err != nil {
		return fmt.Errorf("create backup custody manifest temp identity: %w", err)
	}
	tempPath := filepath.Join(directory, ".manifest.tmp-"+hex.EncodeToString(tempSuffix))
	temp, err := confined.OpenFile(tempPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create backup custody manifest temp: %w", err)
	}
	if _, err := temp.Write(payload); err != nil {
		_ = temp.Close()
		_ = confined.Remove(tempPath)
		return fmt.Errorf("write backup custody manifest temp: %w", err)
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		_ = confined.Remove(tempPath)
		return fmt.Errorf("sync backup custody manifest temp: %w", err)
	}
	if err := temp.Close(); err != nil {
		_ = confined.Remove(tempPath)
		return fmt.Errorf("close backup custody manifest temp: %w", err)
	}
	if err := confined.Link(tempPath, manifestPath); err != nil {
		if !errors.Is(err, os.ErrExist) {
			_ = confined.Remove(tempPath)
			return fmt.Errorf("publish backup custody manifest: %w", err)
		}
		existing, readErr := confined.ReadFile(manifestPath)
		if readErr != nil || !bytes.Equal(existing, payload) {
			_ = confined.Remove(tempPath)
			return fmt.Errorf("backup custody manifest concurrently published with different evidence")
		}
	}
	if err := confined.Remove(tempPath); err != nil {
		return fmt.Errorf("remove backup custody manifest temp: %w", err)
	}
	return nil
}

func canonicalBackupPathSegment(label, value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || value == "." || value == ".." || strings.ContainsAny(value, "/\\\x00") || filepath.Base(value) != value {
		return "", fmt.Errorf("%w: %s is not a safe path segment", ErrInvalid, label)
	}
	return value, nil
}

func backupBatchKey(batch BackupBatch) string {
	var metadata struct {
		LocalBatchID string `json:"local_batch_id"`
	}
	_ = json.Unmarshal(batch.Metadata, &metadata)
	return firstNonEmpty(metadata.LocalBatchID, batch.WatchedRootBackupBatchID)
}

func backupInputLocalBatchID(raw json.RawMessage) (string, error) {
	var metadata struct {
		LocalBatchID string `json:"local_batch_id"`
	}
	if err := json.Unmarshal(raw, &metadata); err != nil {
		return "", fmt.Errorf("%w: metadata.local_batch_id is invalid: %v", ErrInvalid, err)
	}
	localBatchID := strings.TrimSpace(metadata.LocalBatchID)
	if localBatchID == "" {
		return "", nil
	}
	return canonicalBackupPathSegment("local batch ID", localBatchID)
}

func (s Service) GetBackupStatus(ctx context.Context, filter BackupFilter) (BackupStatus, error) {
	filter = normalizeBackupFilter(filter)
	roots, err := s.listRoots(ctx, StatusFilter{NodeRef: filter.NodeRef, RootKey: filter.RootKey, ProjectRef: filter.ProjectRef, Limit: 1})
	if err != nil {
		return BackupStatus{}, err
	}
	if len(roots) == 0 {
		return BackupStatus{}, sql.ErrNoRows
	}
	root := roots[0]
	latest, err := s.latestBackupBatchForRoot(ctx, root.WatchedRootID)
	if err != nil && err != sql.ErrNoRows {
		return BackupStatus{}, err
	}
	status := BackupStatus{Root: root, Status: StatusUnknown}
	if err == nil {
		status.LatestBatch = &latest
		status.Status = statusFromBackupBatch(latest)
	}
	if err := s.populateBackupStatusCounts(ctx, &status); err != nil {
		return BackupStatus{}, err
	}
	findings, err := s.listFindingsForRoot(ctx, root.WatchedRootID, FindingFilter{Status: FindingStatusOpen, Limit: 10})
	if err != nil {
		return BackupStatus{}, err
	}
	status.LatestFindings = backupRelatedFindings(findings)
	status.Status = deriveBackupHealth(status)
	return status, nil
}

func (s Service) ListBackupBatches(ctx context.Context, filter BackupFilter) ([]BackupBatch, error) {
	filter = normalizeBackupFilter(filter)
	args := []any{}
	where := []string{"1 = 1"}
	projectJoin := ""
	if filter.ProjectRef != "" {
		projectID, err := s.resolveProjectID(ctx, filter.ProjectRef)
		if err != nil {
			return nil, err
		}
		args = append(args, projectID)
		where = append(where, fmt.Sprintf("pwr.project_id = $%d", len(args)))
		projectJoin = `
		JOIN projects.project_watched_root_registrations pwr
		  ON pwr.node_id = b.node_id
		 AND pwr.backend_root_key = b.root_key`
	}
	if filter.NodeRef != "" {
		nodeID, err := nodes.NewService(s.DB).ResolveNodeRef(ctx, filter.NodeRef)
		if err != nil {
			return nil, err
		}
		args = append(args, nodeID)
		where = append(where, fmt.Sprintf("b.node_id = $%d", len(args)))
	}
	if filter.RootKey != "" {
		args = append(args, filter.RootKey)
		where = append(where, fmt.Sprintf("b.root_key = $%d", len(args)))
	}
	if filter.Status != "" {
		args = append(args, filter.Status)
		where = append(where, fmt.Sprintf("b.status = $%d", len(args)))
	}
	args = append(args, filter.Limit)
	query := `SELECT ` + backupBatchColumns("b") + `
		FROM watched_roots.backup_batches b` + projectJoin + `
		WHERE ` + strings.Join(where, " AND ") + `
		ORDER BY b.received_at DESC
		LIMIT $` + fmt.Sprintf("%d", len(args))
	rows, err := s.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanBackupBatches(rows)
}

func (s Service) ListBackupItems(ctx context.Context, filter BackupItemFilter) ([]BackupItem, error) {
	filter = normalizeBackupItemFilter(filter)
	args := []any{}
	where := []string{"1 = 1"}
	projectJoin := ""
	if filter.ProjectRef != "" {
		projectID, err := s.resolveProjectID(ctx, filter.ProjectRef)
		if err != nil {
			return nil, err
		}
		args = append(args, projectID)
		where = append(where, fmt.Sprintf("pwr.project_id = $%d", len(args)))
		projectJoin = `
		JOIN projects.project_watched_root_registrations pwr
		  ON pwr.node_id = i.node_id
		 AND pwr.backend_root_key = i.root_key`
	}
	if filter.NodeRef != "" {
		nodeID, err := nodes.NewService(s.DB).ResolveNodeRef(ctx, filter.NodeRef)
		if err != nil {
			return nil, err
		}
		args = append(args, nodeID)
		where = append(where, fmt.Sprintf("i.node_id = $%d", len(args)))
	}
	if filter.RootKey != "" {
		args = append(args, filter.RootKey)
		where = append(where, fmt.Sprintf("i.root_key = $%d", len(args)))
	}
	if filter.Status != "" {
		args = append(args, filter.Status)
		where = append(where, fmt.Sprintf("i.status = $%d", len(args)))
	}
	if filter.BatchRef != "" {
		args = append(args, filter.BatchRef)
		where = append(where, fmt.Sprintf("i.watched_root_backup_batch_id = $%d", len(args)))
	}
	if filter.Path != "" {
		args = append(args, filter.Path)
		where = append(where, fmt.Sprintf("i.relative_path = $%d", len(args)))
	}
	args = append(args, filter.Limit)
	query := `SELECT ` + backupItemColumns("i") + `
		FROM watched_roots.backup_items i` + projectJoin + `
		WHERE ` + strings.Join(where, " AND ") + `
		ORDER BY i.created_at DESC
		LIMIT $` + fmt.Sprintf("%d", len(args))
	rows, err := s.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanBackupItems(rows)
}

func (s Service) listRoots(ctx context.Context, filter StatusFilter) ([]WatchedRoot, error) {
	filter.NodeRef = strings.TrimSpace(filter.NodeRef)
	filter.RootKey = strings.TrimSpace(filter.RootKey)
	filter.ProjectRef = strings.TrimSpace(filter.ProjectRef)
	filter.Status = strings.TrimSpace(filter.Status)
	if filter.Limit <= 0 {
		filter.Limit = 50
	}
	if filter.Limit > 200 {
		filter.Limit = 200
	}
	args := []any{}
	where := []string{"1 = 1"}
	projectJoin := ""
	if filter.ProjectRef != "" {
		projectID, err := s.resolveProjectID(ctx, filter.ProjectRef)
		if err != nil {
			return nil, err
		}
		args = append(args, projectID)
		where = append(where, fmt.Sprintf("pwr.project_id = $%d", len(args)))
		projectJoin = `
		JOIN projects.project_watched_root_registrations pwr
		  ON pwr.node_id = wr.node_id
		 AND pwr.backend_root_key = wr.root_key`
	}
	if filter.NodeRef != "" {
		nodeID, err := nodes.NewService(s.DB).ResolveNodeRef(ctx, filter.NodeRef)
		if err != nil {
			return nil, err
		}
		args = append(args, nodeID)
		where = append(where, fmt.Sprintf("wr.node_id = $%d", len(args)))
	}
	if filter.RootKey != "" {
		args = append(args, filter.RootKey)
		where = append(where, fmt.Sprintf("wr.root_key = $%d", len(args)))
	}
	if filter.Status != "" {
		args = append(args, filter.Status)
		where = append(where, fmt.Sprintf("wr.status = $%d", len(args)))
	}
	args = append(args, filter.Limit)
	query := rootSelectSQL(true) + projectJoin + `
		WHERE ` + strings.Join(where, " AND ") + `
		ORDER BY wr.updated_at DESC
		LIMIT $` + fmt.Sprintf("%d", len(args))
	rows, err := s.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	roots := []WatchedRoot{}
	for rows.Next() {
		root, err := scanRootWithNode(rows)
		if err != nil {
			return nil, err
		}
		roots = append(roots, root)
	}
	return roots, rows.Err()
}

func (s Service) listFindingsForRoot(ctx context.Context, rootID string, filter FindingFilter) ([]Finding, error) {
	args := []any{rootID}
	where := []string{"watched_root_id = $1"}
	if strings.TrimSpace(filter.Status) != "" {
		args = append(args, strings.TrimSpace(filter.Status))
		where = append(where, fmt.Sprintf("status = $%d", len(args)))
	}
	if strings.TrimSpace(filter.Severity) != "" {
		args = append(args, strings.TrimSpace(filter.Severity))
		where = append(where, fmt.Sprintf("severity = $%d", len(args)))
	}
	limit := filter.Limit
	if limit <= 0 {
		limit = 10
	}
	args = append(args, limit)
	query := findingSelectSQL() + `
		WHERE ` + strings.Join(where, " AND ") + `
		ORDER BY updated_at DESC
		LIMIT $` + fmt.Sprintf("%d", len(args))
	rows, err := s.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanFindings(rows)
}

func normalizeReportInput(input ReportInput) (ReportInput, error) {
	input.NodeRef = strings.TrimSpace(input.NodeRef)
	input.CredentialToken = strings.TrimSpace(input.CredentialToken)
	input.RootKey = strings.TrimSpace(input.RootKey)
	input.WorkerKey = strings.TrimSpace(input.WorkerKey)
	input.DisplayName = strings.TrimSpace(input.DisplayName)
	input.SafeRootKey = strings.TrimSpace(input.SafeRootKey)
	input.Status = normalizeStatus(input.Status)
	input.ConfigHash = strings.TrimSpace(input.ConfigHash)
	if input.RootKey == "" {
		return ReportInput{}, fmt.Errorf("%w: root_key is required", ErrInvalid)
	}
	if input.WorkerKey == "" {
		input.WorkerKey = "node-agent.watched_root." + input.RootKey
	}
	if input.DisplayName == "" {
		input.DisplayName = input.RootKey
	}
	var err error
	if input.ConfigJSON, err = normalizeJSON(input.ConfigJSON, "config_json"); err != nil {
		return ReportInput{}, err
	}
	if input.SummaryJSON, err = normalizeJSON(input.SummaryJSON, "summary_json"); err != nil {
		return ReportInput{}, err
	}
	if input.Metadata, err = normalizeJSON(input.Metadata, "metadata"); err != nil {
		return ReportInput{}, err
	}
	for idx := range input.Findings {
		finding, err := normalizeFindingInput(input.RootKey, input.Findings[idx])
		if err != nil {
			return ReportInput{}, err
		}
		input.Findings[idx] = finding
	}
	return input, nil
}

func normalizeFindingInput(rootKey string, input FindingInput) (FindingInput, error) {
	input.FindingKey = strings.TrimSpace(input.FindingKey)
	input.Severity = normalizeSeverity(input.Severity)
	input.Status = normalizeFindingStatus(input.Status)
	input.Kind = strings.TrimSpace(input.Kind)
	input.RelativePath = strings.TrimSpace(input.RelativePath)
	input.Summary = strings.TrimSpace(input.Summary)
	if input.Kind == "" {
		return FindingInput{}, fmt.Errorf("%w: finding kind is required", ErrInvalid)
	}
	if input.FindingKey == "" {
		input.FindingKey = defaultFindingKey(rootKey, input.Kind, input.RelativePath)
	}
	if input.Summary == "" {
		input.Summary = input.Kind
	}
	var err error
	if input.DetailsJSON, err = normalizeJSON(input.DetailsJSON, "details_json"); err != nil {
		return FindingInput{}, err
	}
	return input, nil
}

func normalizeBackupBatchInput(input BackupBatchInput) (BackupBatchInput, error) {
	input.NodeRef = strings.TrimSpace(input.NodeRef)
	input.CredentialToken = strings.TrimSpace(input.CredentialToken)
	input.IdempotencyKey = strings.TrimSpace(input.IdempotencyKey)
	input.RootKey = strings.TrimSpace(input.RootKey)
	input.WorkerKey = strings.TrimSpace(input.WorkerKey)
	input.BatchKind = strings.TrimSpace(input.BatchKind)
	input.BackupMode = strings.TrimSpace(input.BackupMode)
	if input.RootKey == "" {
		return BackupBatchInput{}, fmt.Errorf("%w: root_key is required", ErrInvalid)
	}
	if input.WorkerKey == "" {
		input.WorkerKey = "node-agent.watched_root." + input.RootKey
	}
	if input.BatchKind == "" {
		input.BatchKind = BackupBatchKindWatchedRoot
	}
	if input.BatchKind != BackupBatchKindWatchedRoot {
		return BackupBatchInput{}, fmt.Errorf("%w: batch_kind must be %q", ErrInvalid, BackupBatchKindWatchedRoot)
	}
	if input.BackupMode == "" {
		input.BackupMode = "incremental_raw"
	}
	if len(input.Items) == 0 {
		return BackupBatchInput{}, fmt.Errorf("%w: at least one backup item is required", ErrInvalid)
	}
	var err error
	if input.Metadata, err = normalizeJSON(input.Metadata, "metadata"); err != nil {
		return BackupBatchInput{}, err
	}
	localBatchID, err := backupInputLocalBatchID(input.Metadata)
	if err != nil {
		return BackupBatchInput{}, err
	}
	for idx := range input.Items {
		item, err := normalizeBackupBatchItemInput(input.RootKey, input.BackupMode, idx, input.Items[idx])
		if err != nil {
			return BackupBatchInput{}, err
		}
		if err := validateMetadataOnlyBackupInput(localBatchID, item); err != nil {
			return BackupBatchInput{}, err
		}
		input.Items[idx] = item
	}
	return input, nil
}

func validateMetadataOnlyBackupInput(localBatchID string, input BackupBatchItemInput) error {
	switch input.ItemKind {
	case BackupItemKindFile:
		return nil
	case BackupItemKindDirectory, BackupItemKindMetadata, BackupItemKindDeletionMarker:
	default:
		return fmt.Errorf("%w: backup item kind %q is unsupported", ErrInvalid, input.ItemKind)
	}
	if input.Status == BackupItemStatusSkipped || input.Status == BackupItemStatusFailed {
		return nil
	}
	if localBatchID == "" || input.RelativePath == "" || input.ArtifactKind != "" || input.ArtifactRef != "" || input.PrivateBackupOperationID != "" {
		return fmt.Errorf("%w: metadata-only backup item has inconsistent batch, path, or artifact evidence", ErrInvalid)
	}
	if input.SizeBytes < 0 || !validBackupHashURI(input.ContentHashURI) || (input.PreviousHashURI != "" && !validBackupHashURI(input.PreviousHashURI)) {
		return fmt.Errorf("%w: metadata-only backup item has invalid size or hash evidence", ErrInvalid)
	}
	var metadata struct {
		Source                     string                      `json:"source"`
		LocalBatchID               string                      `json:"local_batch_id"`
		LocalItemID                string                      `json:"local_item_id"`
		LocalArtifactID            string                      `json:"local_artifact_id"`
		FileTransferID             string                      `json:"file_transfer_id"`
		FileTransferStorageEntryID string                      `json:"file_transfer_storage_entry_id"`
		FileTransferAcceptedPath   string                      `json:"file_transfer_accepted_path"`
		FilesystemObservation      *filesystemmeta.Observation `json:"filesystem_observation"`
	}
	if err := json.Unmarshal(input.Metadata, &metadata); err != nil {
		return fmt.Errorf("%w: metadata-only backup item metadata is invalid: %v", ErrInvalid, err)
	}
	if metadata.Source != "loom-node-agent" || metadata.LocalBatchID != localBatchID || metadata.LocalItemID != input.LocalItemRef || metadata.LocalArtifactID != "" || metadata.FileTransferID != "" || metadata.FileTransferStorageEntryID != "" || metadata.FileTransferAcceptedPath != "" {
		return fmt.Errorf("%w: metadata-only backup item identity or transport evidence is inconsistent", ErrInvalid)
	}
	observation := metadata.FilesystemObservation
	if observation == nil || !filesystemmeta.ValidObjectKind(observation.Kind) || observation.LogicalSizeBytes != input.SizeBytes {
		return fmt.Errorf("%w: metadata-only backup item filesystem observation is inconsistent", ErrInvalid)
	}
	switch input.ItemKind {
	case BackupItemKindDirectory:
		if input.ModifiedAt == nil || input.DeletedAt != nil || observation.Kind != filesystemmeta.ObjectKindDirectory {
			return fmt.Errorf("%w: directory backup item lifecycle evidence is inconsistent", ErrInvalid)
		}
	case BackupItemKindMetadata:
		if input.ModifiedAt == nil || input.DeletedAt != nil || observation.Kind != filesystemmeta.ObjectKindRegularFile {
			return fmt.Errorf("%w: metadata backup item lifecycle evidence is inconsistent", ErrInvalid)
		}
	case BackupItemKindDeletionMarker:
		if input.DeletedAt == nil || input.PreviousHashURI == "" {
			return fmt.Errorf("%w: deletion-marker backup item lifecycle evidence is inconsistent", ErrInvalid)
		}
	}
	return nil
}

func validBackupHashURI(value string) bool {
	algorithm, checksum, found := strings.Cut(strings.TrimSpace(value), ":")
	if !found || algorithm != "sha256" || len(checksum) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(checksum)
	return err == nil
}

func normalizeBackupBatchItemInput(rootKey, batchBackupMode string, index int, input BackupBatchItemInput) (BackupBatchItemInput, error) {
	input.LocalItemRef = strings.TrimSpace(input.LocalItemRef)
	input.ItemKind = strings.TrimSpace(input.ItemKind)
	input.Status = normalizeBackupItemStatus(input.Status)
	input.BackupMode = strings.TrimSpace(input.BackupMode)
	input.RelativePath = strings.TrimSpace(input.RelativePath)
	input.ContentHashURI = strings.TrimSpace(input.ContentHashURI)
	input.PreviousHashURI = strings.TrimSpace(input.PreviousHashURI)
	input.ArtifactKind = strings.TrimSpace(input.ArtifactKind)
	input.ArtifactRef = strings.TrimSpace(input.ArtifactRef)
	input.PrivateBackupOperationID = strings.TrimSpace(input.PrivateBackupOperationID)
	input.ErrorCode = strings.TrimSpace(input.ErrorCode)
	input.ErrorMessage = strings.TrimSpace(input.ErrorMessage)
	if input.ItemKind == "" {
		return BackupBatchItemInput{}, fmt.Errorf("%w: backup item kind is required", ErrInvalid)
	}
	cleanRelativePath := filepath.ToSlash(filepath.Clean(filepath.FromSlash(input.RelativePath)))
	if input.RelativePath == "" || filepath.IsAbs(filepath.FromSlash(input.RelativePath)) || cleanRelativePath != filepath.ToSlash(input.RelativePath) || cleanRelativePath == ".." || strings.HasPrefix(cleanRelativePath, "../") || strings.ContainsRune(input.RelativePath, '\x00') {
		return BackupBatchItemInput{}, fmt.Errorf("%w: backup item relative_path must be a clean confined path", ErrInvalid)
	}
	if input.BackupMode == "" {
		input.BackupMode = batchBackupMode
	}
	if input.LocalItemRef == "" {
		input.LocalItemRef = defaultBackupLocalItemRef(rootKey, index, input)
	}
	if input.PrivateBackupOperationID != "" {
		input.ArtifactKind = BackupArtifactKindPrivateBackupOperation
		if input.ArtifactRef == "" {
			input.ArtifactRef = input.PrivateBackupOperationID
		}
	}
	if input.ArtifactKind == BackupArtifactKindPrivateBackupOperation && input.PrivateBackupOperationID == "" {
		input.PrivateBackupOperationID = input.ArtifactRef
	}
	if input.ArtifactKind == BackupArtifactKindFileTransfer && input.ArtifactRef == "" {
		return BackupBatchItemInput{}, fmt.Errorf("%w: backup item file_transfer artifact_ref is required", ErrInvalid)
	}
	var err error
	if input.Metadata, err = normalizeJSON(input.Metadata, "metadata"); err != nil {
		return BackupBatchItemInput{}, err
	}
	return input, nil
}

func normalizeBackupItemStatus(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case BackupItemStatusAccepted, BackupItemStatusDuplicate, BackupItemStatusSkipped, BackupItemStatusFailed:
		return strings.ToLower(strings.TrimSpace(status))
	default:
		return BackupItemStatusAccepted
	}
}

func normalizeBackupFilter(filter BackupFilter) BackupFilter {
	filter.NodeRef = strings.TrimSpace(filter.NodeRef)
	filter.RootKey = strings.TrimSpace(filter.RootKey)
	filter.ProjectRef = strings.TrimSpace(filter.ProjectRef)
	filter.Status = strings.TrimSpace(filter.Status)
	if filter.Limit <= 0 {
		filter.Limit = 50
	}
	if filter.Limit > 200 {
		filter.Limit = 200
	}
	return filter
}

func normalizeBackupItemFilter(filter BackupItemFilter) BackupItemFilter {
	filter.NodeRef = strings.TrimSpace(filter.NodeRef)
	filter.RootKey = strings.TrimSpace(filter.RootKey)
	filter.ProjectRef = strings.TrimSpace(filter.ProjectRef)
	filter.Status = strings.TrimSpace(filter.Status)
	filter.BatchRef = strings.TrimSpace(filter.BatchRef)
	filter.Path = strings.TrimSpace(filter.Path)
	if filter.Limit <= 0 {
		filter.Limit = 50
	}
	if filter.Limit > 200 {
		filter.Limit = 200
	}
	return filter
}

func normalizeStatus(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case StatusHealthy, StatusDegraded, StatusBlocked:
		return strings.ToLower(strings.TrimSpace(status))
	case "":
		return StatusUnknown
	default:
		return strings.ToLower(strings.TrimSpace(status))
	}
}

func normalizeSeverity(severity string) string {
	switch strings.ToLower(strings.TrimSpace(severity)) {
	case FindingSeverityInfo, FindingSeverityWarning, FindingSeverityError, FindingSeverityCritical:
		return strings.ToLower(strings.TrimSpace(severity))
	default:
		return FindingSeverityWarning
	}
}

func normalizeFindingStatus(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case FindingStatusOpen, FindingStatusResolved, FindingStatusIgnored:
		return strings.ToLower(strings.TrimSpace(status))
	default:
		return FindingStatusOpen
	}
}

func normalizeJSON(raw json.RawMessage, field string) (json.RawMessage, error) {
	if len(strings.TrimSpace(string(raw))) == 0 || strings.TrimSpace(string(raw)) == "null" {
		return json.RawMessage(`{}`), nil
	}
	var decoded any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil, fmt.Errorf("%w: %s must be valid JSON", ErrInvalid, field)
	}
	return raw, nil
}

func defaultFindingKey(rootKey, kind, relativePath string) string {
	raw := strings.TrimSpace(rootKey) + "\n" + strings.TrimSpace(kind) + "\n" + strings.TrimSpace(relativePath)
	sum := sha256.Sum256([]byte(raw))
	return "finding_" + hex.EncodeToString(sum[:])[:24]
}

func defaultBackupLocalItemRef(rootKey string, index int, input BackupBatchItemInput) string {
	raw := strings.TrimSpace(rootKey) + "\n" +
		fmt.Sprintf("%d", index) + "\n" +
		strings.TrimSpace(input.ItemKind) + "\n" +
		strings.TrimSpace(input.RelativePath) + "\n" +
		strings.TrimSpace(input.ContentHashURI) + "\n" +
		strings.TrimSpace(input.PreviousHashURI)
	sum := sha256.Sum256([]byte(raw))
	return "backup_item_" + hex.EncodeToString(sum[:])[:24]
}

type backupCounts struct {
	AcceptedCount       int
	DuplicateCount      int
	SkippedCount        int
	FailedCount         int
	ArtifactCount       int
	DeletionMarkerCount int
	TotalBytes          int64
}

func backupBatchCounts(items []BackupBatchItemInput) backupCounts {
	var counts backupCounts
	for _, item := range items {
		switch item.Status {
		case BackupItemStatusDuplicate:
			counts.DuplicateCount++
		case BackupItemStatusSkipped:
			counts.SkippedCount++
		case BackupItemStatusFailed:
			counts.FailedCount++
		default:
			counts.AcceptedCount++
		}
		if item.ArtifactRef != "" || item.PrivateBackupOperationID != "" {
			counts.ArtifactCount++
		}
		if item.ItemKind == BackupItemKindDeletionMarker {
			counts.DeletionMarkerCount++
		}
		if item.SizeBytes > 0 {
			counts.TotalBytes += item.SizeBytes
		}
	}
	return counts
}

func backupBatchStatus(counts backupCounts, itemCount int) string {
	if itemCount > 0 && counts.FailedCount == itemCount {
		return BackupBatchStatusFailed
	}
	if counts.FailedCount > 0 || counts.SkippedCount > 0 {
		return BackupBatchStatusPartial
	}
	return BackupBatchStatusAccepted
}

func statusFromBackupBatch(batch BackupBatch) string {
	switch batch.Status {
	case BackupBatchStatusAccepted:
		return StatusHealthy
	case BackupBatchStatusPartial:
		return StatusDegraded
	case BackupBatchStatusFailed:
		return StatusBlocked
	default:
		return StatusUnknown
	}
}

func backupRelatedFindings(findings []Finding) []Finding {
	out := []Finding{}
	for _, finding := range findings {
		if strings.HasPrefix(finding.Kind, "backup_") || finding.Kind == "root_unavailable" {
			out = append(out, finding)
		}
	}
	return out
}

func deriveBackupHealth(status BackupStatus) string {
	if status.LatestBatch == nil {
		if len(status.LatestFindings) > 0 {
			return backupFindingHealth(status.LatestFindings)
		}
		return StatusUnknown
	}
	if status.FailedCount > 0 {
		return StatusDegraded
	}
	if len(status.LatestFindings) > 0 || status.SkippedCount > 0 {
		return backupFindingHealth(status.LatestFindings)
	}
	return statusFromBackupBatch(*status.LatestBatch)
}

func backupFindingHealth(findings []Finding) string {
	for _, finding := range findings {
		if finding.Kind == "root_unavailable" || finding.Kind == "backup_queue_limit_reached" {
			return StatusBlocked
		}
	}
	return StatusDegraded
}

func upsertRootTx(ctx context.Context, tx *sql.Tx, nodeID string, input ReportInput) (WatchedRoot, error) {
	query := rootInsertSQL() + `
		ON CONFLICT (node_id, root_key) DO UPDATE SET
			worker_key = EXCLUDED.worker_key,
			display_name = EXCLUDED.display_name,
			safe_root_key = EXCLUDED.safe_root_key,
			status = EXCLUDED.status,
			config_hash = EXCLUDED.config_hash,
			config_json = EXCLUDED.config_json,
			summary_json = EXCLUDED.summary_json,
			metadata = EXCLUDED.metadata,
			last_reported_at = now(),
			updated_at = now()
		RETURNING ` + rootColumns("")
	return scanRoot(tx.QueryRowContext(ctx, query,
		ids.NewWatchedRootID(),
		nodeID,
		input.RootKey,
		input.WorkerKey,
		input.DisplayName,
		input.SafeRootKey,
		input.Status,
		input.ConfigHash,
		input.ConfigJSON,
		input.SummaryJSON,
		input.Metadata,
	))
}

func ensureRootForBackupTx(ctx context.Context, tx *sql.Tx, nodeID string, input BackupBatchInput) (WatchedRoot, error) {
	query := rootInsertSQL() + `
		ON CONFLICT (node_id, root_key) DO UPDATE SET
			worker_key = CASE
				WHEN EXCLUDED.worker_key <> '' THEN EXCLUDED.worker_key
				ELSE watched_roots.roots.worker_key
			END,
			updated_at = now()
		RETURNING ` + rootColumns("")
	return scanRoot(tx.QueryRowContext(ctx, query,
		ids.NewWatchedRootID(),
		nodeID,
		input.RootKey,
		input.WorkerKey,
		input.RootKey,
		"",
		StatusUnknown,
		"",
		json.RawMessage(`{}`),
		json.RawMessage(`{}`),
		objectOrDefault(json.RawMessage(`{"source":"watched_root_backup_ingest"}`)),
	))
}

type privateBackupArtifactEvidence struct {
	OperationID  string
	OriginNodeID string
	Status       string
	SizeBytes    int64
	StorageRef   string
	Metadata     json.RawMessage
}

const maxWatchedRootPrivateArtifactBytes = 1 << 20

type backupArtifactEvidenceQuerier interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func verifyBackupArtifacts(ctx context.Context, queryer backupArtifactEvidenceQuerier, backupRoot, rootKey, localBatchID string, items []BackupBatchItemInput, node nodes.Node) error {
	for _, item := range items {
		if item.PrivateBackupOperationID != "" {
			if localBatchID == "" {
				return fmt.Errorf("%w: metadata.local_batch_id is required for private backup artifacts", ErrInvalid)
			}
			if err := verifyPrivateBackupOperationNodeTx(ctx, queryer, backupRoot, rootKey, localBatchID, item, node); err != nil {
				return err
			}
		}
		if item.ArtifactKind == BackupArtifactKindFileTransfer {
			if localBatchID == "" {
				return fmt.Errorf("%w: metadata.local_batch_id is required for file transfer artifacts", ErrInvalid)
			}
			if err := verifyFileTransferArtifactTx(ctx, queryer, backupRoot, rootKey, localBatchID, item, node); err != nil {
				return err
			}
		}
	}
	return nil
}

func backupInputsFromStoredItems(items []BackupItem) []BackupBatchItemInput {
	inputs := make([]BackupBatchItemInput, 0, len(items))
	for _, item := range items {
		operationID := ""
		if item.PrivateBackupOperationID != nil {
			operationID = *item.PrivateBackupOperationID
		}
		inputs = append(inputs, BackupBatchItemInput{
			LocalItemRef: item.LocalItemRef, ItemKind: item.ItemKind, Status: item.Status,
			BackupMode: item.BackupMode, RelativePath: item.RelativePath,
			ContentHashURI: item.ContentHashURI, PreviousHashURI: item.PreviousHashURI,
			SizeBytes: item.SizeBytes, ModifiedAt: item.ModifiedAt, DeletedAt: item.DeletedAt,
			ArtifactKind: item.ArtifactKind, ArtifactRef: item.ArtifactRef,
			PrivateBackupOperationID: operationID, ErrorCode: item.ErrorCode,
			ErrorMessage: item.ErrorMessage, Metadata: item.Metadata,
		})
	}
	return inputs
}

func verifyPrivateBackupOperationNodeTx(ctx context.Context, queryer backupArtifactEvidenceQuerier, backupRoot, rootKey, localBatchID string, item BackupBatchItemInput, node nodes.Node) error {
	evidence := privateBackupArtifactEvidence{OperationID: item.PrivateBackupOperationID}
	err := queryer.QueryRowContext(ctx, `
		SELECT origin_node_id, status, coarse_size_bytes, storage_ref, metadata
		FROM sync.private_backup_operations
		WHERE private_backup_operation_id = $1
	`, item.PrivateBackupOperationID).Scan(&evidence.OriginNodeID, &evidence.Status, &evidence.SizeBytes, &evidence.StorageRef, &evidence.Metadata)
	if err != nil {
		if err == sql.ErrNoRows {
			return fmt.Errorf("%w: private backup operation %s was not found", ErrInvalid, item.PrivateBackupOperationID)
		}
		return err
	}
	return validatePrivateBackupArtifact(backupRoot, rootKey, localBatchID, item, node, evidence)
}

func validatePrivateBackupArtifact(backupRoot, rootKey, localBatchID string, item BackupBatchItemInput, node nodes.Node, evidence privateBackupArtifactEvidence) error {
	operationID := strings.TrimSpace(item.PrivateBackupOperationID)
	if strings.TrimSpace(item.ArtifactRef) != operationID || item.ArtifactKind != BackupArtifactKindPrivateBackupOperation {
		return fmt.Errorf("%w: private backup operation %s has inconsistent artifact identity", ErrInvalid, operationID)
	}
	if evidence.OriginNodeID != node.NodeID {
		return fmt.Errorf("%w: private backup operation %s belongs to a different node", ErrInvalid, operationID)
	}
	if evidence.Status != "stored" {
		return fmt.Errorf("%w: private backup operation %s is not stored", ErrInvalid, operationID)
	}
	var metadata struct {
		BackupKind      string `json:"backup_kind"`
		SourceNodeKey   string `json:"source_node_key"`
		RootKey         string `json:"root_key"`
		LocalBatchID    string `json:"local_batch_id"`
		CustodyBatchKey string `json:"custody_batch_key"`
		RelativePath    string `json:"relative_path"`
		ContentHashURI  string `json:"content_hash_uri"`
		BackupMode      string `json:"backup_mode"`
		LocalItemID     string `json:"local_item_id"`
		PayloadSHA256   string `json:"payload_sha256"`
	}
	if err := json.Unmarshal(evidence.Metadata, &metadata); err != nil {
		return fmt.Errorf("%w: private backup operation %s metadata is invalid", ErrInvalid, operationID)
	}
	if metadata.BackupKind != "watched_root_file" || metadata.SourceNodeKey != node.NodeKey || metadata.RootKey != rootKey || metadata.LocalBatchID != localBatchID || metadata.CustodyBatchKey != localBatchID {
		return fmt.Errorf("%w: private backup operation %s identity does not match backup batch", ErrInvalid, operationID)
	}
	if metadata.RelativePath != item.RelativePath || metadata.ContentHashURI != item.ContentHashURI || metadata.BackupMode != item.BackupMode || metadata.LocalItemID != item.LocalItemRef {
		return fmt.Errorf("%w: private backup operation %s evidence does not match backup item", ErrInvalid, operationID)
	}
	if strings.TrimSpace(metadata.PayloadSHA256) == "" {
		return fmt.Errorf("%w: private backup operation %s has no payload checksum", ErrInvalid, operationID)
	}
	rootPath := filepath.Clean(strings.TrimSpace(backupRoot))
	if rootPath == "." || rootPath == "" {
		return fmt.Errorf("%w: backup root is required to verify private backup custody", ErrInvalid)
	}
	nodeSegment, err := canonicalBackupPathSegment("source node key", node.NodeKey)
	if err != nil {
		return err
	}
	rootSegment, err := canonicalBackupPathSegment("root key", rootKey)
	if err != nil {
		return err
	}
	batchSegment, err := canonicalBackupPathSegment("local batch ID", localBatchID)
	if err != nil {
		return err
	}
	operationSegment, err := canonicalBackupPathSegment("private backup operation ID", operationID)
	if err != nil {
		return err
	}
	expected := filepath.Join(rootPath, nodeSegment, rootSegment, batchSegment)
	if operationSegment != batchSegment {
		expected = filepath.Join(expected, "artifacts", operationSegment)
	}
	expected = filepath.Join(expected, "payload.tar")
	if filepath.Clean(strings.TrimSpace(evidence.StorageRef)) != expected {
		return fmt.Errorf("%w: private backup operation %s storage path does not match batch custody", ErrInvalid, operationID)
	}
	return verifyWatchedRootPrivateBackupPayload(rootPath, expected, rootKey, localBatchID, item, evidence, metadata.PayloadSHA256)
}

func verifyWatchedRootPrivateBackupPayload(backupRoot, payloadPath, rootKey, localBatchID string, item BackupBatchItemInput, evidence privateBackupArtifactEvidence, payloadSHA256 string) error {
	if evidence.SizeBytes <= 0 || evidence.SizeBytes > maxWatchedRootPrivateArtifactBytes {
		return fmt.Errorf("%w: private backup payload size is outside the accepted transport bound", ErrInvalid)
	}
	relative, err := filepath.Rel(backupRoot, payloadPath)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return fmt.Errorf("%w: private backup payload escapes backup root", ErrInvalid)
	}
	root, err := os.OpenRoot(backupRoot)
	if err != nil {
		return err
	}
	defer root.Close()
	components := strings.Split(filepath.ToSlash(relative), "/")
	current := ""
	for index, component := range components {
		current = filepath.Join(current, component)
		info, err := root.Lstat(current)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%w: private backup custody contains a symlink", ErrInvalid)
		}
		if index < len(components)-1 && !info.IsDir() {
			return fmt.Errorf("%w: private backup custody parent is not a directory", ErrInvalid)
		}
		if index == len(components)-1 && !info.Mode().IsRegular() {
			return fmt.Errorf("%w: private backup payload is not a regular file", ErrInvalid)
		}
	}
	file, err := root.Open(relative)
	if err != nil {
		return err
	}
	payload, readErr := io.ReadAll(io.LimitReader(file, evidence.SizeBytes+1))
	closeErr := file.Close()
	if readErr != nil {
		return readErr
	}
	if closeErr != nil {
		return closeErr
	}
	if int64(len(payload)) != evidence.SizeBytes {
		return fmt.Errorf("%w: private backup payload size does not match stored operation", ErrInvalid)
	}
	payloadHash := sha256.Sum256(payload)
	if !strings.EqualFold(hex.EncodeToString(payloadHash[:]), strings.TrimSpace(payloadSHA256)) {
		return fmt.Errorf("%w: private backup payload checksum does not match stored operation", ErrInvalid)
	}
	reader := tar.NewReader(bytes.NewReader(payload))
	var artifactManifest struct {
		SchemaVersion  string `json:"schema_version"`
		RootKey        string `json:"root_key"`
		RelativePath   string `json:"relative_path"`
		ContentHashURI string `json:"content_hash_uri"`
		SizeBytes      int64  `json:"size_bytes"`
		BackupMode     string `json:"backup_mode"`
		LocalBatchID   string `json:"local_batch_id"`
		LocalItemID    string `json:"local_item_id"`
	}
	foundManifest, foundContent := false, false
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("%w: read private backup artifact: %v", ErrInvalid, err)
		}
		if header == nil || header.Typeflag != tar.TypeReg || (header.Name != "manifest.json" && header.Name != "content") {
			return fmt.Errorf("%w: private backup artifact contains an unexpected member", ErrInvalid)
		}
		switch header.Name {
		case "manifest.json":
			if foundManifest || header.Size > 1<<20 {
				return fmt.Errorf("%w: private backup artifact manifest is duplicated or oversized", ErrInvalid)
			}
			manifestPayload, err := io.ReadAll(io.LimitReader(reader, header.Size+1))
			if err != nil || int64(len(manifestPayload)) != header.Size || json.Unmarshal(manifestPayload, &artifactManifest) != nil {
				return fmt.Errorf("%w: private backup artifact manifest is invalid", ErrInvalid)
			}
			foundManifest = true
		case "content":
			if foundContent || header.Size != item.SizeBytes {
				return fmt.Errorf("%w: private backup artifact content size does not match backup item", ErrInvalid)
			}
			hash := sha256.New()
			written, err := io.Copy(hash, reader)
			if err != nil || written != header.Size {
				return fmt.Errorf("%w: private backup artifact content is truncated", ErrInvalid)
			}
			algorithm, checksum := checksumParts(item.ContentHashURI)
			if algorithm != "sha256" || !strings.EqualFold(hex.EncodeToString(hash.Sum(nil)), checksum) {
				return fmt.Errorf("%w: private backup artifact content checksum does not match backup item", ErrInvalid)
			}
			foundContent = true
		}
	}
	if !foundManifest || !foundContent || artifactManifest.SchemaVersion != "watched_root.backup_artifact.v0.2" ||
		artifactManifest.RootKey != rootKey || artifactManifest.RelativePath != item.RelativePath ||
		artifactManifest.ContentHashURI != item.ContentHashURI || artifactManifest.SizeBytes != item.SizeBytes ||
		artifactManifest.BackupMode != item.BackupMode || artifactManifest.LocalBatchID != localBatchID || artifactManifest.LocalItemID != item.LocalItemRef {
		return fmt.Errorf("%w: private backup artifact manifest does not match backup item", ErrInvalid)
	}
	return nil
}

type fileTransferArtifactEvidence struct {
	SourceNodeID           sql.NullString
	SourceNodeKey          string
	SourceRootKey          string
	SourceRelativePath     string
	DestinationLogicalPath string
	TransferKind           string
	CustodyMode            string
	Status                 string
	ChecksumAlgorithm      string
	ChecksumHex            string
	FileSizeBytes          int64
	AcceptedPath           string
	Metadata               json.RawMessage
}

func verifyFileTransferArtifactTx(ctx context.Context, queryer backupArtifactEvidenceQuerier, backupRoot, rootKey, localBatchID string, item BackupBatchItemInput, node nodes.Node) error {
	evidence := fileTransferArtifactEvidence{}
	err := queryer.QueryRowContext(ctx, `
		SELECT source_node_id, source_node_key, source_root_key, source_relative_path,
		       destination_logical_path, transfer_kind, custody_mode, status,
		       checksum_algorithm, checksum_hex, file_size_bytes, accepted_path, metadata
		FROM storage.file_transfers
		WHERE file_transfer_id = $1
	`, item.ArtifactRef).Scan(
		&evidence.SourceNodeID,
		&evidence.SourceNodeKey,
		&evidence.SourceRootKey,
		&evidence.SourceRelativePath,
		&evidence.DestinationLogicalPath,
		&evidence.TransferKind,
		&evidence.CustodyMode,
		&evidence.Status,
		&evidence.ChecksumAlgorithm,
		&evidence.ChecksumHex,
		&evidence.FileSizeBytes,
		&evidence.AcceptedPath,
		&evidence.Metadata,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			return fmt.Errorf("%w: file transfer %s was not found", ErrInvalid, item.ArtifactRef)
		}
		return err
	}
	return validateFileTransferArtifact(backupRoot, rootKey, localBatchID, item, node, evidence)
}

func validateFileTransferArtifact(backupRoot, rootKey, localBatchID string, item BackupBatchItemInput, node nodes.Node, evidence fileTransferArtifactEvidence) error {
	if !evidence.SourceNodeID.Valid || evidence.SourceNodeID.String != node.NodeID {
		return fmt.Errorf("%w: file transfer %s belongs to a different node", ErrInvalid, item.ArtifactRef)
	}
	if strings.TrimSpace(evidence.SourceNodeKey) != node.NodeKey {
		return fmt.Errorf("%w: file transfer %s uses a different source node key", ErrInvalid, item.ArtifactRef)
	}
	if evidence.TransferKind != filetransfer.KindWatchedRootBackup {
		return fmt.Errorf("%w: file transfer %s is not a watched-root backup", ErrInvalid, item.ArtifactRef)
	}
	if evidence.CustodyMode != filetransfer.CustodyModeBackupCopy {
		return fmt.Errorf("%w: file transfer %s is not a backup-copy transfer", ErrInvalid, item.ArtifactRef)
	}
	if evidence.Status != filetransfer.StatusAccepted {
		return fmt.Errorf("%w: file transfer %s is not accepted", ErrInvalid, item.ArtifactRef)
	}
	if strings.TrimSpace(evidence.SourceRootKey) != strings.TrimSpace(rootKey) {
		return fmt.Errorf("%w: file transfer %s root key does not match backup batch", ErrInvalid, item.ArtifactRef)
	}
	if strings.TrimSpace(evidence.SourceRelativePath) != strings.TrimSpace(item.RelativePath) {
		return fmt.Errorf("%w: file transfer %s source path does not match backup item", ErrInvalid, item.ArtifactRef)
	}
	if strings.TrimSpace(evidence.DestinationLogicalPath) != strings.TrimSpace(item.RelativePath) {
		return fmt.Errorf("%w: file transfer %s destination path does not match backup item", ErrInvalid, item.ArtifactRef)
	}
	algorithm, checksum := checksumParts(item.ContentHashURI)
	decodedChecksum, decodeErr := hex.DecodeString(checksum)
	if algorithm != filetransfer.ChecksumSHA256 || decodeErr != nil || len(decodedChecksum) != sha256.Size {
		return fmt.Errorf("%w: file transfer %s backup item has no valid SHA-256 evidence", ErrInvalid, item.ArtifactRef)
	}
	if !strings.EqualFold(evidence.ChecksumAlgorithm, algorithm) || !strings.EqualFold(evidence.ChecksumHex, checksum) {
		return fmt.Errorf("%w: file transfer %s checksum does not match backup item", ErrInvalid, item.ArtifactRef)
	}
	if item.SizeBytes >= 0 && evidence.FileSizeBytes != item.SizeBytes {
		return fmt.Errorf("%w: file transfer %s size does not match backup item", ErrInvalid, item.ArtifactRef)
	}
	var metadata struct {
		LocalBatchID string `json:"local_batch_id"`
		LocalItemID  string `json:"local_item_id"`
		RootKey      string `json:"root_key"`
		RelativePath string `json:"relative_path"`
		ContentHash  string `json:"content_hash_uri"`
		BackupMode   string `json:"backup_mode"`
		BackupKind   string `json:"backup_kind"`
	}
	if err := json.Unmarshal(evidence.Metadata, &metadata); err != nil || strings.TrimSpace(metadata.LocalBatchID) != localBatchID ||
		metadata.LocalItemID != item.LocalItemRef || metadata.RootKey != rootKey || metadata.RelativePath != item.RelativePath ||
		metadata.ContentHash != item.ContentHashURI || metadata.BackupMode != item.BackupMode || metadata.BackupKind != "watched_root_file" {
		return fmt.Errorf("%w: file transfer %s local batch does not match backup batch", ErrInvalid, item.ArtifactRef)
	}
	rootPath := filepath.Clean(strings.TrimSpace(backupRoot))
	if rootPath == "." || rootPath == "" {
		return fmt.Errorf("%w: backup root is required to verify file transfer custody", ErrInvalid)
	}
	nodeSegment, err := canonicalBackupPathSegment("source node key", node.NodeKey)
	if err != nil {
		return err
	}
	rootSegment, err := canonicalBackupPathSegment("root key", rootKey)
	if err != nil {
		return err
	}
	batchSegment, err := canonicalBackupPathSegment("local batch ID", localBatchID)
	if err != nil {
		return err
	}
	relativePath, err := storagecatalog.NormalizeLogicalPath(item.RelativePath)
	if err != nil {
		return fmt.Errorf("%w: file transfer %s item path is invalid", ErrInvalid, item.ArtifactRef)
	}
	expectedRelative := filepath.ToSlash(filepath.Join(nodeSegment, rootSegment, batchSegment, "payload", filepath.FromSlash(relativePath)))
	if filepath.ToSlash(filepath.Clean(filepath.FromSlash(evidence.AcceptedPath))) != expectedRelative {
		return fmt.Errorf("%w: file transfer %s accepted path does not match batch custody", ErrInvalid, item.ArtifactRef)
	}
	return verifyWatchedRootFileTransferPayload(rootPath, filepath.FromSlash(expectedRelative), item, evidence)
}

func verifyWatchedRootFileTransferPayload(backupRoot, relativePath string, item BackupBatchItemInput, evidence fileTransferArtifactEvidence) error {
	algorithm := strings.ToLower(strings.TrimSpace(evidence.ChecksumAlgorithm))
	checksum := strings.ToLower(strings.TrimSpace(evidence.ChecksumHex))
	decodedChecksum, err := hex.DecodeString(checksum)
	if algorithm != filetransfer.ChecksumSHA256 || len(decodedChecksum) != sha256.Size {
		return fmt.Errorf("%w: file transfer %s has no valid SHA-256 payload evidence", ErrInvalid, item.ArtifactRef)
	}
	rootInfo, err := os.Lstat(backupRoot)
	if err != nil {
		return err
	}
	if rootInfo.Mode()&os.ModeSymlink != 0 || !rootInfo.IsDir() {
		return fmt.Errorf("%w: backup root must be a real directory", ErrInvalid)
	}
	root, err := os.OpenRoot(backupRoot)
	if err != nil {
		return err
	}
	defer root.Close()
	components := strings.Split(filepath.ToSlash(relativePath), "/")
	current := ""
	for index, component := range components {
		current = filepath.Join(current, component)
		info, err := root.Lstat(current)
		if err != nil {
			return fmt.Errorf("%w: inspect accepted file-transfer payload: %v", ErrInvalid, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%w: accepted file-transfer payload contains a symlink", ErrInvalid)
		}
		if index < len(components)-1 && !info.IsDir() {
			return fmt.Errorf("%w: accepted file-transfer payload parent is not a directory", ErrInvalid)
		}
		if index == len(components)-1 && !info.Mode().IsRegular() {
			return fmt.Errorf("%w: accepted file-transfer payload is not a regular file", ErrInvalid)
		}
	}
	file, err := root.Open(relativePath)
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() != evidence.FileSizeBytes || info.Size() != item.SizeBytes {
		return fmt.Errorf("%w: file transfer %s physical payload size does not match accepted evidence", ErrInvalid, item.ArtifactRef)
	}
	hash := sha256.New()
	written, err := io.Copy(hash, file)
	if err != nil {
		return err
	}
	if written != evidence.FileSizeBytes || !strings.EqualFold(hex.EncodeToString(hash.Sum(nil)), checksum) {
		return fmt.Errorf("%w: file transfer %s physical payload checksum does not match accepted evidence", ErrInvalid, item.ArtifactRef)
	}
	return nil
}

func upsertFindingTx(ctx context.Context, tx *sql.Tx, rootID, nodeID, rootKey string, input FindingInput) (Finding, error) {
	now := time.Now().UTC()
	firstSeen := now
	lastSeen := now
	if input.FirstSeenAt != nil && !input.FirstSeenAt.IsZero() {
		firstSeen = input.FirstSeenAt.UTC()
	}
	if input.LastSeenAt != nil && !input.LastSeenAt.IsZero() {
		lastSeen = input.LastSeenAt.UTC()
	}
	query := findingInsertSQL() + `
		ON CONFLICT (watched_root_id, finding_key) DO UPDATE SET
			severity = EXCLUDED.severity,
			status = EXCLUDED.status,
			finding_kind = EXCLUDED.finding_kind,
			relative_path = EXCLUDED.relative_path,
			summary = EXCLUDED.summary,
			details_json = EXCLUDED.details_json,
			last_seen_at = EXCLUDED.last_seen_at,
			resolved_at = CASE
				WHEN EXCLUDED.status = 'open' THEN NULL
				ELSE COALESCE(watched_roots.findings.resolved_at, now())
			END,
			updated_at = now()
		RETURNING ` + findingColumns("")
	return scanFinding(tx.QueryRowContext(ctx, query,
		ids.NewWatchedRootFindingID(),
		rootID,
		nodeID,
		rootKey,
		input.FindingKey,
		input.Severity,
		input.Status,
		input.Kind,
		input.RelativePath,
		input.Summary,
		input.DetailsJSON,
		firstSeen,
		lastSeen,
	))
}

func resolveMissingFindingsTx(ctx context.Context, tx *sql.Tx, rootID string, reported map[string]struct{}) (int, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT finding_key
		FROM watched_roots.findings
		WHERE watched_root_id = $1 AND status = 'open'
	`, rootID)
	if err != nil {
		return 0, err
	}
	staleKeys := []string{}
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			_ = rows.Close()
			return 0, err
		}
		if _, ok := reported[key]; ok {
			continue
		}
		staleKeys = append(staleKeys, key)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return 0, err
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}
	count := 0
	for _, key := range staleKeys {
		if _, err := tx.ExecContext(ctx, `
			UPDATE watched_roots.findings
			SET status = 'resolved', resolved_at = now(), updated_at = now()
			WHERE watched_root_id = $1 AND finding_key = $2 AND status = 'open'
		`, rootID, key); err != nil {
			return 0, err
		}
		count++
	}
	return count, nil
}

func insertBackupBatchTx(ctx context.Context, tx *sql.Tx, root WatchedRoot, nodeID string, input BackupBatchInput, counts backupCounts, status string) (BackupBatch, error) {
	return scanBackupBatch(tx.QueryRowContext(ctx, backupBatchSelectSQL(`
		INSERT INTO watched_roots.backup_batches (
			watched_root_backup_batch_id, watched_root_id, node_id, root_key,
			worker_key, idempotency_key, batch_kind, backup_mode, status,
			item_count, accepted_count, duplicate_count, skipped_count, failed_count,
			artifact_count, deletion_marker_count, total_bytes, completed_at, metadata
		)
		VALUES ($1,$2,$3,$4,$5,nullif($6,''),$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,now(),$18)
		RETURNING
	`),
		ids.NewWatchedRootBackupBatchID(),
		root.WatchedRootID,
		nodeID,
		input.RootKey,
		input.WorkerKey,
		input.IdempotencyKey,
		input.BatchKind,
		input.BackupMode,
		status,
		len(input.Items),
		counts.AcceptedCount,
		counts.DuplicateCount,
		counts.SkippedCount,
		counts.FailedCount,
		counts.ArtifactCount,
		counts.DeletionMarkerCount,
		counts.TotalBytes,
		objectOrDefault(input.Metadata),
	))
}

func insertBackupItemTx(ctx context.Context, tx *sql.Tx, root WatchedRoot, nodeID, batchID string, input BackupBatchItemInput) (BackupItem, error) {
	return scanBackupItem(tx.QueryRowContext(ctx, backupItemSelectSQL(`
		INSERT INTO watched_roots.backup_items (
			watched_root_backup_item_id, watched_root_backup_batch_id, watched_root_id,
			node_id, root_key, local_item_ref, item_kind, status, backup_mode,
			relative_path, content_hash_uri, previous_hash_uri, size_bytes,
			modified_at, deleted_at, artifact_kind, artifact_ref,
			private_backup_operation_id, error_code, error_message, metadata
		)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,nullif($18,''),$19,$20,$21)
		ON CONFLICT (node_id, root_key, local_item_ref) DO UPDATE SET
			status = watched_roots.backup_items.status
		RETURNING
	`),
		ids.NewWatchedRootBackupItemID(),
		batchID,
		root.WatchedRootID,
		nodeID,
		root.RootKey,
		input.LocalItemRef,
		input.ItemKind,
		input.Status,
		input.BackupMode,
		input.RelativePath,
		input.ContentHashURI,
		input.PreviousHashURI,
		input.SizeBytes,
		nullableTime(input.ModifiedAt),
		nullableTime(input.DeletedAt),
		input.ArtifactKind,
		input.ArtifactRef,
		input.PrivateBackupOperationID,
		input.ErrorCode,
		input.ErrorMessage,
		objectOrDefault(input.Metadata),
	))
}

func (s Service) getRootByID(ctx context.Context, rootID string) (WatchedRoot, error) {
	return scanRoot(s.DB.QueryRowContext(ctx, rootSelectSQL(false)+`
		WHERE wr.watched_root_id = $1
	`, rootID))
}

func (s Service) resolveProjectID(ctx context.Context, ref string) (string, error) {
	project, err := projects.NewService(s.DB).ResolveProjectRef(ctx, ref)
	if err != nil {
		return "", err
	}
	return project.ProjectID, nil
}

func (s Service) getBackupBatchByIdempotency(ctx context.Context, nodeID, idempotencyKey string) (BackupBatch, error) {
	return scanBackupBatch(s.DB.QueryRowContext(ctx, backupBatchSelectSQL()+`
		WHERE node_id = $1 AND idempotency_key = $2
		LIMIT 1
	`, nodeID, idempotencyKey))
}

func (s Service) listBackupItemsForBatch(ctx context.Context, batchID string) ([]BackupItem, error) {
	rows, err := s.DB.QueryContext(ctx, backupItemSelectSQL()+`
		WHERE watched_root_backup_batch_id = $1
		ORDER BY created_at ASC
	`, batchID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanBackupItems(rows)
}

func (s Service) latestBackupBatchForRoot(ctx context.Context, rootID string) (BackupBatch, error) {
	return scanBackupBatch(s.DB.QueryRowContext(ctx, backupBatchSelectSQL()+`
		WHERE watched_root_id = $1
		ORDER BY received_at DESC
		LIMIT 1
	`, rootID))
}

func (s Service) populateBackupStatusCounts(ctx context.Context, status *BackupStatus) error {
	var counts backupCounts
	err := s.DB.QueryRowContext(ctx, `
		SELECT
			COUNT(DISTINCT b.watched_root_backup_batch_id),
			COUNT(i.watched_root_backup_item_id),
			COALESCE(SUM(CASE WHEN i.status = 'accepted' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN i.status = 'duplicate' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN i.status = 'skipped' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN i.status = 'failed' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN i.artifact_ref <> '' OR i.private_backup_operation_id IS NOT NULL THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN i.item_kind = 'deletion_marker' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(i.size_bytes), 0)
		FROM watched_roots.backup_batches b
		LEFT JOIN watched_roots.backup_items i ON i.watched_root_backup_batch_id = b.watched_root_backup_batch_id
		WHERE b.watched_root_id = $1
	`, status.Root.WatchedRootID).Scan(
		&status.BatchCount,
		&status.ItemCount,
		&counts.AcceptedCount,
		&counts.DuplicateCount,
		&counts.SkippedCount,
		&counts.FailedCount,
		&counts.ArtifactCount,
		&counts.DeletionMarkerCount,
		&counts.TotalBytes,
	)
	if err != nil {
		return err
	}
	status.AcceptedCount = counts.AcceptedCount
	status.DuplicateCount = counts.DuplicateCount
	status.SkippedCount = counts.SkippedCount
	status.FailedCount = counts.FailedCount
	status.ArtifactCount = counts.ArtifactCount
	status.DeletionMarkerCount = counts.DeletionMarkerCount
	status.TotalBytes = counts.TotalBytes
	if status.Status == StatusUnknown && status.FailedCount > 0 {
		status.Status = StatusDegraded
	}
	return nil
}

func rootInsertSQL() string {
	return `
		INSERT INTO watched_roots.roots (
			watched_root_id, node_id, root_key, worker_key, display_name,
			safe_root_key, status, config_hash, config_json, summary_json, metadata
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
	`
}

func findingInsertSQL() string {
	return `
		INSERT INTO watched_roots.findings (
			watched_root_finding_id, watched_root_id, node_id, root_key, finding_key,
			severity, status, finding_kind, relative_path, summary, details_json,
			first_seen_at, last_seen_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)
	`
}

func rootSelectSQL(withNode bool) string {
	base := `SELECT ` + rootColumns("wr")
	if withNode {
		base += `,
			n.node_key, n.display_name, n.node_kind, n.node_role, n.runtime_class, n.status,
			n.presence_state, n.last_heartbeat_at, n.last_seen_at, n.runtime_version,
			n.enrollment_status, n.credential_status, n.owner_actor_id, n.home_scope_id,
			n.metadata, n.created_at, n.updated_at, n.retired_at`
	}
	base += `
		FROM watched_roots.roots wr`
	if withNode {
		base += `
		JOIN nodes.nodes n ON n.node_id = wr.node_id`
	}
	return base
}

func rootColumns(alias string) string {
	prefix := ""
	if alias != "" {
		prefix = alias + "."
	}
	return prefix + `watched_root_id, ` + prefix + `node_id, ` + prefix + `root_key, ` + prefix + `worker_key,
		` + prefix + `display_name, ` + prefix + `safe_root_key, ` + prefix + `status, ` + prefix + `config_hash,
		` + prefix + `config_json, ` + prefix + `summary_json, ` + prefix + `metadata, ` + prefix + `last_reported_at,
		` + prefix + `created_at, ` + prefix + `updated_at`
}

func findingSelectSQL() string {
	return `SELECT ` + findingColumns("") + `
		FROM watched_roots.findings`
}

func findingColumns(alias string) string {
	prefix := ""
	if alias != "" {
		prefix = alias + "."
	}
	return prefix + `watched_root_finding_id, ` + prefix + `watched_root_id, ` + prefix + `node_id,
		` + prefix + `root_key, ` + prefix + `finding_key, ` + prefix + `severity, ` + prefix + `status,
		` + prefix + `finding_kind, ` + prefix + `relative_path, ` + prefix + `summary, ` + prefix + `details_json,
		` + prefix + `first_seen_at, ` + prefix + `last_seen_at, ` + prefix + `resolved_at, ` + prefix + `created_at,
		` + prefix + `updated_at`
}

func backupBatchSelectSQL(prefix ...string) string {
	if len(prefix) > 0 && strings.TrimSpace(prefix[0]) != "" {
		return prefix[0] + `
		watched_root_backup_batch_id, watched_root_id, node_id, root_key,
		worker_key, idempotency_key, batch_kind, backup_mode, status,
		item_count, accepted_count, duplicate_count, skipped_count, failed_count,
		artifact_count, deletion_marker_count, total_bytes, received_at,
		completed_at, metadata
	`
	}
	return `SELECT ` + backupBatchColumns("") + `
		FROM watched_roots.backup_batches`
}

func backupBatchColumns(alias string) string {
	prefix := ""
	if alias != "" {
		prefix = alias + "."
	}
	return prefix + `watched_root_backup_batch_id, ` + prefix + `watched_root_id, ` + prefix + `node_id,
		` + prefix + `root_key, ` + prefix + `worker_key, ` + prefix + `idempotency_key, ` + prefix + `batch_kind,
		` + prefix + `backup_mode, ` + prefix + `status, ` + prefix + `item_count, ` + prefix + `accepted_count,
		` + prefix + `duplicate_count, ` + prefix + `skipped_count, ` + prefix + `failed_count,
		` + prefix + `artifact_count, ` + prefix + `deletion_marker_count, ` + prefix + `total_bytes,
		` + prefix + `received_at, ` + prefix + `completed_at, ` + prefix + `metadata`
}

func backupItemSelectSQL(prefix ...string) string {
	if len(prefix) > 0 && strings.TrimSpace(prefix[0]) != "" {
		return prefix[0] + `
		watched_root_backup_item_id, watched_root_backup_batch_id, watched_root_id,
		node_id, root_key, local_item_ref, item_kind, status, backup_mode,
		relative_path, content_hash_uri, previous_hash_uri, size_bytes,
		modified_at, deleted_at, artifact_kind, artifact_ref,
		private_backup_operation_id, error_code, error_message, metadata, created_at
	`
	}
	return `SELECT ` + backupItemColumns("") + `
		FROM watched_roots.backup_items`
}

func backupItemColumns(alias string) string {
	prefix := ""
	if alias != "" {
		prefix = alias + "."
	}
	return prefix + `watched_root_backup_item_id, ` + prefix + `watched_root_backup_batch_id,
		` + prefix + `watched_root_id, ` + prefix + `node_id, ` + prefix + `root_key,
		` + prefix + `local_item_ref, ` + prefix + `item_kind, ` + prefix + `status, ` + prefix + `backup_mode,
		` + prefix + `relative_path, ` + prefix + `content_hash_uri, ` + prefix + `previous_hash_uri,
		` + prefix + `size_bytes, ` + prefix + `modified_at, ` + prefix + `deleted_at, ` + prefix + `artifact_kind,
		` + prefix + `artifact_ref, ` + prefix + `private_backup_operation_id, ` + prefix + `error_code,
		` + prefix + `error_message, ` + prefix + `metadata, ` + prefix + `created_at`
}

func objectOrDefault(raw json.RawMessage) json.RawMessage {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return json.RawMessage(`{}`)
	}
	var object map[string]any
	if err := json.Unmarshal(raw, &object); err != nil || object == nil {
		return json.RawMessage(`{}`)
	}
	return raw
}

func nullableTime(value *time.Time) any {
	if value == nil || value.IsZero() {
		return nil
	}
	t := value.UTC()
	return t
}

func stringPtr(value sql.NullString) *string {
	if !value.Valid {
		return nil
	}
	return &value.String
}

func timePtr(value sql.NullTime) *time.Time {
	if !value.Valid {
		return nil
	}
	return &value.Time
}

type scanner interface {
	Scan(dest ...any) error
}

func scanRoot(row scanner) (WatchedRoot, error) {
	var root WatchedRoot
	var configJSON, summaryJSON, metadata []byte
	if err := row.Scan(
		&root.WatchedRootID,
		&root.NodeID,
		&root.RootKey,
		&root.WorkerKey,
		&root.DisplayName,
		&root.SafeRootKey,
		&root.Status,
		&root.ConfigHash,
		&configJSON,
		&summaryJSON,
		&metadata,
		&root.LastReportedAt,
		&root.CreatedAt,
		&root.UpdatedAt,
	); err != nil {
		return WatchedRoot{}, err
	}
	root.ConfigJSON = json.RawMessage(configJSON)
	root.SummaryJSON = json.RawMessage(summaryJSON)
	root.Metadata = json.RawMessage(metadata)
	return root, nil
}

func scanRootWithNode(rows *sql.Rows) (WatchedRoot, error) {
	var root WatchedRoot
	var node nodes.Node
	var configJSON, summaryJSON, metadata, nodeMetadata []byte
	var ownerActorID, homeScopeID, runtimeVersion sql.NullString
	var lastHeartbeatAt, lastSeenAt, retiredAt sql.NullTime
	if err := rows.Scan(
		&root.WatchedRootID,
		&root.NodeID,
		&root.RootKey,
		&root.WorkerKey,
		&root.DisplayName,
		&root.SafeRootKey,
		&root.Status,
		&root.ConfigHash,
		&configJSON,
		&summaryJSON,
		&metadata,
		&root.LastReportedAt,
		&root.CreatedAt,
		&root.UpdatedAt,
		&node.NodeKey,
		&node.DisplayName,
		&node.NodeKind,
		&node.NodeRole,
		&node.RuntimeClass,
		&node.Status,
		&node.PresenceState,
		&lastHeartbeatAt,
		&lastSeenAt,
		&runtimeVersion,
		&node.EnrollmentStatus,
		&node.CredentialStatus,
		&ownerActorID,
		&homeScopeID,
		&nodeMetadata,
		&node.CreatedAt,
		&node.UpdatedAt,
		&retiredAt,
	); err != nil {
		return WatchedRoot{}, err
	}
	root.ConfigJSON = json.RawMessage(configJSON)
	root.SummaryJSON = json.RawMessage(summaryJSON)
	root.Metadata = json.RawMessage(metadata)
	node.NodeID = root.NodeID
	if ownerActorID.Valid {
		node.OwnerActorID = &ownerActorID.String
	}
	if homeScopeID.Valid {
		node.HomeScopeID = &homeScopeID.String
	}
	if lastHeartbeatAt.Valid {
		node.LastHeartbeatAt = &lastHeartbeatAt.Time
	}
	if lastSeenAt.Valid {
		node.LastSeenAt = &lastSeenAt.Time
	}
	if runtimeVersion.Valid {
		node.RuntimeVersion = &runtimeVersion.String
	}
	if retiredAt.Valid {
		node.RetiredAt = &retiredAt.Time
	}
	node.Metadata = json.RawMessage(nodeMetadata)
	root.Node = &node
	return root, nil
}

func scanFindings(rows *sql.Rows) ([]Finding, error) {
	findings := []Finding{}
	for rows.Next() {
		finding, err := scanFinding(rows)
		if err != nil {
			return nil, err
		}
		findings = append(findings, finding)
	}
	return findings, rows.Err()
}

func scanBackupBatches(rows *sql.Rows) ([]BackupBatch, error) {
	batches := []BackupBatch{}
	for rows.Next() {
		batch, err := scanBackupBatch(rows)
		if err != nil {
			return nil, err
		}
		batches = append(batches, batch)
	}
	return batches, rows.Err()
}

func scanBackupBatch(row scanner) (BackupBatch, error) {
	var batch BackupBatch
	var idempotencyKey sql.NullString
	var completedAt sql.NullTime
	var metadata []byte
	if err := row.Scan(
		&batch.WatchedRootBackupBatchID,
		&batch.WatchedRootID,
		&batch.NodeID,
		&batch.RootKey,
		&batch.WorkerKey,
		&idempotencyKey,
		&batch.BatchKind,
		&batch.BackupMode,
		&batch.Status,
		&batch.ItemCount,
		&batch.AcceptedCount,
		&batch.DuplicateCount,
		&batch.SkippedCount,
		&batch.FailedCount,
		&batch.ArtifactCount,
		&batch.DeletionMarkerCount,
		&batch.TotalBytes,
		&batch.ReceivedAt,
		&completedAt,
		&metadata,
	); err != nil {
		return BackupBatch{}, err
	}
	batch.IdempotencyKey = stringPtr(idempotencyKey)
	batch.CompletedAt = timePtr(completedAt)
	batch.Metadata = objectOrDefault(metadata)
	return batch, nil
}

func scanBackupItems(rows *sql.Rows) ([]BackupItem, error) {
	items := []BackupItem{}
	for rows.Next() {
		item, err := scanBackupItem(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func scanBackupItem(row scanner) (BackupItem, error) {
	var item BackupItem
	var privateBackupOperationID sql.NullString
	var modifiedAt, deletedAt sql.NullTime
	var metadata []byte
	if err := row.Scan(
		&item.WatchedRootBackupItemID,
		&item.WatchedRootBackupBatchID,
		&item.WatchedRootID,
		&item.NodeID,
		&item.RootKey,
		&item.LocalItemRef,
		&item.ItemKind,
		&item.Status,
		&item.BackupMode,
		&item.RelativePath,
		&item.ContentHashURI,
		&item.PreviousHashURI,
		&item.SizeBytes,
		&modifiedAt,
		&deletedAt,
		&item.ArtifactKind,
		&item.ArtifactRef,
		&privateBackupOperationID,
		&item.ErrorCode,
		&item.ErrorMessage,
		&metadata,
		&item.CreatedAt,
	); err != nil {
		return BackupItem{}, err
	}
	item.ModifiedAt = timePtr(modifiedAt)
	item.DeletedAt = timePtr(deletedAt)
	item.PrivateBackupOperationID = stringPtr(privateBackupOperationID)
	item.Metadata = objectOrDefault(metadata)
	return item, nil
}

func scanFinding(row scanner) (Finding, error) {
	var finding Finding
	var details []byte
	var resolvedAt sql.NullTime
	if err := row.Scan(
		&finding.WatchedRootFindingID,
		&finding.WatchedRootID,
		&finding.NodeID,
		&finding.RootKey,
		&finding.FindingKey,
		&finding.Severity,
		&finding.Status,
		&finding.Kind,
		&finding.RelativePath,
		&finding.Summary,
		&details,
		&finding.FirstSeenAt,
		&finding.LastSeenAt,
		&resolvedAt,
		&finding.CreatedAt,
		&finding.UpdatedAt,
	); err != nil {
		return Finding{}, err
	}
	finding.DetailsJSON = json.RawMessage(details)
	if resolvedAt.Valid {
		finding.ResolvedAt = &resolvedAt.Time
	}
	return finding, nil
}
