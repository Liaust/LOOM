package backupcontracts

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"loom.local/loom/internal/communication"
)

const (
	ProtectedFolderStatusWaitingForNode = "waiting_for_node"
	ProtectedFolderStatusReady          = "ready"
	ProtectedFolderStatusActivating     = "activating"
	ProtectedFolderStatusActive         = "active"
	ProtectedFolderStatusProtected      = "protected"
	ProtectedFolderStatusAttention      = "attention"
	ProtectedFolderStatusDisabled       = "disabled"

	PreflightStatusPending   = "pending"
	PreflightStatusCompleted = "completed"
	PreflightStatusFailed    = "failed"
	PreflightStatusExpired   = "expired"
)

// StatusEvidence is a transport-independent lifecycle projection input. Slice
// 5 supplies database-backed evidence; keeping precedence pure makes it
// exhaustively testable.
type StatusEvidence struct {
	ContractDisabled   bool
	BlockingAttention  bool
	NodeUnavailable    bool
	PreflightPending   bool
	PreflightCompleted bool
	ReconcilePending   bool
	DesiredRegistered  bool
	NodeApplied        bool
	RootReported       bool
	ConfigHashMatches  bool
	AcceptedBackup     bool
}

func ProjectProtectedFolderStatus(e StatusEvidence) string {
	switch {
	case e.ContractDisabled:
		return ProtectedFolderStatusDisabled
	case e.BlockingAttention:
		return ProtectedFolderStatusAttention
	case e.NodeUnavailable && (e.PreflightPending || e.ReconcilePending):
		return ProtectedFolderStatusWaitingForNode
	case e.PreflightCompleted && !e.DesiredRegistered:
		return ProtectedFolderStatusReady
	case !e.NodeApplied || !e.RootReported || !e.ConfigHashMatches:
		return ProtectedFolderStatusActivating
	case e.AcceptedBackup:
		return ProtectedFolderStatusProtected
	default:
		return ProtectedFolderStatusActive
	}
}

type ProtectedFolderFilter struct {
	Lifecycle string
	NodeRef   string
	Limit     int
}

type StatusService struct {
	DB *sql.DB
}

func NewStatusService(db *sql.DB) StatusService { return StatusService{DB: db} }

type registrationEvidence struct {
	ContractKey, ContractPath, TargetPath, NodeID, NodeKey, Presence, RootKey string
	ActivationStatus, DesiredHash, AppliedHash, MessageID                     string
	MessageStatus, LastAckStatus, ApplyErrorCode, ApplyErrorMessage           string
	DesiredRevision, AppliedRevision                                          int64
	RootID, RootConfigHash, RootStatus                                        string
	DeletedAt, LastAckAt, RootReportedAt                                      *time.Time
	OpenFindingCount                                                          int
	LatestBackupStatus                                                        string
	LatestBackupAt, AcceptedBackupAt                                          *time.Time
}

type preflightEvidence struct {
	NodeID, NodeKey, Path string
	Record                PreflightRecord
}

// List joins file-backed intent with one bounded database evidence query. A
// registration without its canonical YAML remains visible as Attention unless
// an intentional deletion has durably converged on its owner node.
func (s StatusService) List(ctx context.Context, boxRoot, directoryRelPath string, filter ProtectedFolderFilter) (ProtectedFolderListResult, error) {
	stored, err := List(boxRoot, directoryRelPath)
	if err != nil {
		return ProtectedFolderListResult{}, err
	}
	registrations := []registrationEvidence{}
	preflights := []preflightEvidence{}
	if s.DB != nil {
		registrations, err = s.loadRegistrations(ctx, boxRoot)
		if err != nil {
			return ProtectedFolderListResult{}, err
		}
		preflights, err = s.loadPreflights(ctx)
		if err != nil {
			return ProtectedFolderListResult{}, err
		}
	}
	byKey := make(map[string]registrationEvidence, len(registrations))
	for _, evidence := range registrations {
		byKey[evidence.ContractKey] = evidence
	}
	result := ProtectedFolderListResult{Folders: []ProtectedFolderRecord{}, Counts: map[string]int{}, Contracts: []ContractRecord{}}
	seen := map[string]struct{}{}
	for _, item := range stored {
		seen[item.Key] = struct{}{}
		if item.Error != "" {
			record := ProtectedFolderRecord{Key: item.Key, ContractPath: item.Path, Path: item.Path, Lifecycle: ProtectedFolderStatusAttention, Status: ProtectedFolderStatusAttention, AttentionReason: item.Error, MissingContract: true}
			appendProtectedFolder(&result, record, filter)
			continue
		}
		record := projectRecord(item.Contract, item.Path, byKey[item.Contract.Key], latestPreflight(preflights, item.Contract.OwnerNode, item.Contract.Target.Path))
		appendProtectedFolder(&result, record, filter)
	}
	for _, evidence := range registrations {
		if _, ok := seen[evidence.ContractKey]; ok {
			continue
		}
		if evidence.DeletedAt != nil {
			if intentionalDeletionConverged(evidence) {
				continue
			}
			appendProtectedFolder(&result, intentionalDeletionRecord(evidence), filter)
			continue
		}
		record := ProtectedFolderRecord{Key: evidence.ContractKey, ContractPath: evidence.ContractPath, Path: evidence.ContractPath, Lifecycle: ProtectedFolderStatusAttention, Status: ProtectedFolderStatusAttention, AttentionReason: "canonical contract YAML is missing", MissingContract: true}
		applyRegistrationEvidence(&record, evidence)
		appendProtectedFolder(&result, record, filter)
	}
	sort.Slice(result.Folders, func(i, j int) bool { return result.Folders[i].Key < result.Folders[j].Key })
	if filter.Limit > 0 && len(result.Folders) > filter.Limit {
		result.Folders = result.Folders[:filter.Limit]
	}
	return result, nil
}

func intentionalDeletionConverged(e registrationEvidence) bool {
	return e.DeletedAt != nil && e.ActivationStatus == "disabled" && e.DesiredRevision > 0 && e.AppliedRevision == e.DesiredRevision && e.LastAckStatus == communication.AckStatusCompleted
}

func intentionalDeletionRecord(e registrationEvidence) ProtectedFolderRecord {
	record := ProtectedFolderRecord{
		Key:          e.ContractKey,
		Path:         e.ContractPath,
		ContractPath: e.ContractPath,
		Contract: Contract{
			Key:       e.ContractKey,
			OwnerNode: e.NodeKey,
			Status:    StatusDisabled,
			Target:    TargetSpec{Scope: TargetScopeOwnerNodeAbsolute, Path: e.TargetPath},
		},
		Deleting: true,
	}
	applyRegistrationEvidence(&record, e)
	record.DesiredRegistered = false
	record.NodeApplied = false
	record.ConfigHashMatches = false
	switch {
	case e.ApplyErrorCode != "":
		record.Lifecycle = ProtectedFolderStatusAttention
		record.AttentionReason = firstStatusNonEmpty(e.ApplyErrorMessage, e.ApplyErrorCode)
	case e.Presence == "offline" || e.Presence == "unreachable" || e.Presence == "unknown":
		record.Lifecycle = ProtectedFolderStatusWaitingForNode
	default:
		record.Lifecycle = ProtectedFolderStatusActivating
	}
	record.Status = record.Lifecycle
	record.Details, _ = json.Marshal(map[string]any{"deleting": true, "contract_deleted_at": e.DeletedAt, "node_presence": e.Presence, "message_status": e.MessageStatus, "activation_status": e.ActivationStatus})
	return record
}

func (s StatusService) Get(ctx context.Context, boxRoot, directoryRelPath, key string) (ProtectedFolderRecord, error) {
	result, err := s.List(ctx, boxRoot, directoryRelPath, ProtectedFolderFilter{})
	if err != nil {
		return ProtectedFolderRecord{}, err
	}
	for _, record := range result.Folders {
		if record.Key == strings.TrimSpace(key) {
			return record, nil
		}
	}
	return ProtectedFolderRecord{}, sql.ErrNoRows
}

func appendProtectedFolder(result *ProtectedFolderListResult, record ProtectedFolderRecord, filter ProtectedFolderFilter) {
	result.Counts[record.Lifecycle]++
	legacy := ContractRecord{Key: record.Key, Path: record.Path, Status: record.Status}
	if record.MissingContract {
		legacy.Error = record.AttentionReason
		result.Problems = append(result.Problems, WatchPlanProblem{Key: record.Key, Path: record.Path, Message: record.AttentionReason})
	} else {
		contract := record.Contract
		legacy.Contract = &contract
	}
	result.Contracts = append(result.Contracts, legacy)
	if strings.TrimSpace(filter.Lifecycle) != "" && record.Lifecycle != strings.TrimSpace(filter.Lifecycle) {
		return
	}
	if strings.TrimSpace(filter.NodeRef) != "" && record.Contract.OwnerNode != strings.TrimSpace(filter.NodeRef) && record.OwnerNodeID != strings.TrimSpace(filter.NodeRef) {
		return
	}
	result.Folders = append(result.Folders, record)
}

func projectRecord(contract Contract, path string, registration registrationEvidence, preflight *PreflightRecord) ProtectedFolderRecord {
	record := ProtectedFolderRecord{Key: contract.Key, Path: path, Status: contract.Status, Contract: contract, ContractPath: path, Preflight: preflight}
	applyRegistrationEvidence(&record, registration)
	evidence := StatusEvidence{ContractDisabled: contract.Status == StatusDisabled, DesiredRegistered: registration.ContractKey != "", NodeApplied: record.NodeApplied, RootReported: record.RootReported, ConfigHashMatches: record.ConfigHashMatches, AcceptedBackup: record.LastBackupAcceptedAt != nil}
	if preflight != nil {
		evidence.PreflightPending = preflight.Status == PreflightStatusPending
		evidence.PreflightCompleted = preflight.Status == PreflightStatusCompleted
		if preflight.Status == PreflightStatusFailed || preflight.Status == PreflightStatusExpired {
			evidence.BlockingAttention = true
			record.AttentionReason = firstStatusNonEmpty(preflight.ErrorMessage, "folder preflight did not complete")
		}
		if preflight.Result != nil {
			for _, finding := range preflight.Result.Findings {
				if finding.Blocking {
					evidence.BlockingAttention = true
					record.AttentionReason = finding.Message
					break
				}
			}
		}
	}
	evidence.NodeUnavailable = registration.Presence == "offline" || registration.Presence == "unreachable" || registration.Presence == "unknown"
	evidence.ReconcilePending = registration.ContractKey != "" && !record.NodeApplied
	if registration.ApplyErrorCode != "" || registration.OpenFindingCount > 0 || registration.LatestBackupStatus == "failed" {
		evidence.BlockingAttention = true
		record.AttentionReason = firstStatusNonEmpty(registration.ApplyErrorMessage, registration.ApplyErrorCode, findingReason(registration.OpenFindingCount), backupReason(registration.LatestBackupStatus))
	}
	if record.RootReported && !record.ConfigHashMatches {
		evidence.BlockingAttention = true
		record.AttentionReason = "owner-node report does not match the current desired configuration"
	}
	record.Lifecycle = ProjectProtectedFolderStatus(evidence)
	record.Status = record.Lifecycle
	record.Details, _ = json.Marshal(map[string]any{"node_presence": registration.Presence, "message_status": registration.MessageStatus, "activation_status": registration.ActivationStatus, "root_status": registration.RootStatus, "root_reported_at": registration.RootReportedAt, "open_finding_count": registration.OpenFindingCount, "latest_backup_status": registration.LatestBackupStatus, "latest_backup_at": registration.LatestBackupAt})
	return record
}

func applyRegistrationEvidence(record *ProtectedFolderRecord, e registrationEvidence) {
	if e.ContractKey == "" {
		return
	}
	record.OwnerNodeID, record.OwnerNodeKey, record.RootKey = e.NodeID, e.NodeKey, e.RootKey
	record.DesiredRegistered = true
	record.DesiredRevision, record.AppliedRevision = e.DesiredRevision, e.AppliedRevision
	record.DesiredConfigHash, record.AppliedConfigHash = e.DesiredHash, e.AppliedHash
	record.ReconciliationMessage = e.MessageID
	record.NodeApplied = e.DesiredRevision > 0 && e.AppliedRevision == e.DesiredRevision && e.AppliedHash != "" && e.AppliedHash == e.DesiredHash
	record.RootReported = e.RootID != ""
	record.ConfigHashMatches = record.RootReported && e.RootConfigHash != "" && e.RootConfigHash == e.DesiredHash
	reportIsCurrent := e.RootReportedAt != nil && (e.LastAckAt == nil || !e.RootReportedAt.Before(*e.LastAckAt))
	if record.NodeApplied && record.ConfigHashMatches && reportIsCurrent && e.AcceptedBackupAt != nil {
		record.LastBackupAcceptedAt = e.AcceptedBackupAt
	}
}

func latestPreflight(items []preflightEvidence, nodeRef, path string) *PreflightRecord {
	for _, item := range items {
		if (item.NodeID == nodeRef || item.NodeKey == nodeRef) && item.Path == path {
			copy := item.Record
			return &copy
		}
	}
	return nil
}

func (s StatusService) loadRegistrations(ctx context.Context, boxRoot string) ([]registrationEvidence, error) {
	rows, err := s.DB.QueryContext(ctx, `
		SELECT DISTINCT ON (r.source_contract_key)
		 r.source_contract_key, r.source_contract_path, COALESCE(r.metadata->>'target_path', ''), r.node_id, n.node_key, n.presence_state,
		 r.backend_root_key, r.activation_status, r.desired_revision, r.applied_revision,
		 r.desired_config_hash, r.applied_config_hash, COALESCE(r.reconciliation_message_id, ''),
		 COALESCE(m.status, ''), r.last_node_ack_status, r.last_node_acknowledged_at, r.last_apply_error_code, r.last_apply_error_message,
		 r.source_contract_deleted_at,
		 COALESCE(wr.watched_root_id, ''), COALESCE(wr.config_hash, ''), COALESCE(wr.status, ''), wr.last_reported_at,
		 COALESCE(findings.open_count, 0), COALESCE(latest.status, ''), latest.received_at,
		 CASE WHEN latest.status = 'accepted' THEN latest.received_at END
		FROM box.watch_root_registrations r
		JOIN nodes.nodes n ON n.node_id = r.node_id
		LEFT JOIN communication.messages m ON m.communication_message_id = r.reconciliation_message_id
		LEFT JOIN watched_roots.roots wr ON wr.node_id = r.node_id AND wr.root_key = r.backend_root_key
		LEFT JOIN LATERAL (SELECT count(*)::integer AS open_count FROM watched_roots.findings f WHERE f.watched_root_id = wr.watched_root_id AND f.status = 'open' AND f.severity IN ('error','critical')) findings ON true
		LEFT JOIN LATERAL (SELECT b.status, b.received_at FROM watched_roots.backup_batches b WHERE b.watched_root_id = wr.watched_root_id ORDER BY b.received_at DESC LIMIT 1) latest ON true
		WHERE r.box_root_path = $1 AND (r.source_kind = $2 OR r.area_key LIKE 'backup\_%' ESCAPE '\')
		ORDER BY r.source_contract_key, r.updated_at DESC
		LIMIT 500`, boxRoot, SourceKindBoxBackupContract)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []registrationEvidence{}
	for rows.Next() {
		var e registrationEvidence
		if err := rows.Scan(&e.ContractKey, &e.ContractPath, &e.TargetPath, &e.NodeID, &e.NodeKey, &e.Presence, &e.RootKey, &e.ActivationStatus, &e.DesiredRevision, &e.AppliedRevision, &e.DesiredHash, &e.AppliedHash, &e.MessageID, &e.MessageStatus, &e.LastAckStatus, &e.LastAckAt, &e.ApplyErrorCode, &e.ApplyErrorMessage, &e.DeletedAt, &e.RootID, &e.RootConfigHash, &e.RootStatus, &e.RootReportedAt, &e.OpenFindingCount, &e.LatestBackupStatus, &e.LatestBackupAt, &e.AcceptedBackupAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s StatusService) loadPreflights(ctx context.Context) ([]preflightEvidence, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT DISTINCT ON (p.target_node_id, p.requested_path) n.node_id, n.node_key, p.requested_path, `+preflightColumnsFor("p")+` FROM backup.protected_folder_preflights p JOIN nodes.nodes n ON n.node_id = p.target_node_id ORDER BY p.target_node_id, p.requested_path, p.created_at DESC LIMIT 500`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []preflightEvidence{}
	for rows.Next() {
		var item preflightEvidence
		record, err := scanPreflightWithPrefix(rows, &item.NodeID, &item.NodeKey, &item.Path)
		if err != nil {
			return nil, err
		}
		item.Record = record
		out = append(out, item)
	}
	return out, rows.Err()
}

func preflightColumnsFor(alias string) string {
	p := strings.TrimSpace(alias) + "."
	return p + `protected_folder_preflight_id, ` + p + `target_node_id, ` + p + `requested_path, nullif(` + p + `canonical_path, ''), ` + p + `status, ` + p + `request_schema_version, nullif(` + p + `response_schema_version, ''), ` + p + `communication_message_id, ` + p + `result_json, nullif(` + p + `error_code, ''), nullif(` + p + `error_message, ''), ` + p + `expires_at, ` + p + `retry_of_preflight_id, ` + p + `created_at, ` + p + `completed_at`
}

func scanPreflightWithPrefix(scanner preflightScanner, prefix ...any) (PreflightRecord, error) {
	var result PreflightRecord
	var resultJSON []byte
	var messageID, canonicalPath, responseVersion, errorCode, errorMessage, retryOf sql.NullString
	var completedAt sql.NullTime
	dest := append(prefix, &result.PreflightID, &result.TargetNodeID, &result.RequestedPath, &canonicalPath, &result.Status, &result.RequestSchemaVersion, &responseVersion, &messageID, &resultJSON, &errorCode, &errorMessage, &result.ExpiresAt, &retryOf, &result.CreatedAt, &completedAt)
	if err := scanner.Scan(dest...); err != nil {
		return PreflightRecord{}, err
	}
	result.CanonicalPath, result.ResponseSchemaVersion = nullableString(canonicalPath), nullableString(responseVersion)
	result.CommunicationMessageID, result.ErrorCode, result.ErrorMessage = nullableString(messageID), nullableString(errorCode), nullableString(errorMessage)
	result.RetryOfPreflightID = nullableString(retryOf)
	if completedAt.Valid {
		result.CompletedAt = &completedAt.Time
	}
	if len(resultJSON) > 0 && string(resultJSON) != "{}" {
		var value PreflightResult
		if err := json.Unmarshal(resultJSON, &value); err != nil {
			return PreflightRecord{}, err
		}
		result.Result = &value
	}
	return result, nil
}

func firstStatusNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
func findingReason(count int) string {
	if count > 0 {
		return fmt.Sprintf("%d blocking owner-node finding(s)", count)
	}
	return ""
}
func backupReason(status string) string {
	if status == "failed" {
		return "latest backup batch failed"
	}
	return ""
}
