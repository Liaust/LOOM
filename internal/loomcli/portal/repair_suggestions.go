package portal

import "strings"

const (
	FailureClassCloudCooldown         = "cloud_cooldown"
	FailureClassCloudAuth             = "cloud_auth"
	FailureClassCloudUnreachable      = "cloud_unreachable"
	FailureClassSMBUnavailable        = "smb_unavailable"
	FailureClassWireGuardDown         = "wireguard_down"
	FailureClassWatchedRootStale      = "watched_root_stale"
	FailureClassWatchedRootScanFailed = "watched_root_scan_failed"
	FailureClassStorageExport         = "storage_export"
	FailureClassStorageFidelity       = "storage_fidelity"
	FailureClassRetentionPayload      = "retention_payload_missing"
	FailureClassIndexFailure          = "index_failure"
	FailureClassWorkerFailed          = "worker_failed"
	FailureClassJobFailed             = "job_failed"
	FailureClassNodeOffline           = "node_offline"
	FailureClassDatabaseMigration     = "database_migration"
	FailureClassBoxContractInvalid    = "box_contract_invalid"
	FailureClassApprovalPending       = "approval_pending"
)

type repairSuggestion struct {
	FailureClass string
	Suggestion   string
	TargetScreen string
	InspectID    string
	RepairID     string
}

func applyAttentionRepairSuggestion(item AttentionItem) AttentionItem {
	suggestion := repairSuggestionForAttention(item)
	if suggestion.FailureClass == "" {
		return item
	}
	item.FailureClass = suggestion.FailureClass
	if item.Suggestion == "" || attentionSuggestionIsGeneric(item.Suggestion) {
		item.Suggestion = suggestion.Suggestion
	}
	if item.TargetKind == "" && suggestion.TargetScreen != "" {
		item.TargetKind = "screen"
		item.TargetRef = suggestion.TargetScreen
	}
	if item.TargetRef == "" && suggestion.TargetScreen != "" {
		item.TargetRef = suggestion.TargetScreen
	}
	item.InspectActionID = firstNonEmpty(item.InspectActionID, suggestion.InspectID)
	item.RepairActionID = firstNonEmpty(item.RepairActionID, suggestion.RepairID)
	item.RelatedActionID = firstNonEmpty(item.RelatedActionID, item.RepairActionID, item.InspectActionID)
	return item
}

func repairSuggestionForAttention(item AttentionItem) repairSuggestion {
	class := classifyAttentionFailure(item)
	switch class {
	case FailureClassCloudCooldown:
		return repairSuggestion{FailureClass: class, Suggestion: "wait for cooldown, then open cloud status before retrying", TargetScreen: ScreenStorage, InspectID: "storage.open", RepairID: "cloud.status.live"}
	case FailureClassCloudAuth:
		return repairSuggestion{FailureClass: class, Suggestion: "check cloud credentials, then run cloud status live", TargetScreen: ScreenStorage, InspectID: "storage.open", RepairID: "cloud.status.live"}
	case FailureClassCloudUnreachable:
		return repairSuggestion{FailureClass: class, Suggestion: "check network route and cloud status before retrying", TargetScreen: ScreenStorage, InspectID: "storage.open", RepairID: "cloud.status.live"}
	case FailureClassSMBUnavailable:
		return repairSuggestion{FailureClass: class, Suggestion: "bring LOOM network up, then mount storage", TargetScreen: ScreenStorage, InspectID: "storage.open", RepairID: "storage.mount.helper.inspect"}
	case FailureClassWireGuardDown:
		return repairSuggestion{FailureClass: class, Suggestion: "run LOOM Network Up and verify the 10.44 route", TargetScreen: ScreenStorage, InspectID: "storage.open", RepairID: "storage.mount.helper.inspect"}
	case FailureClassWatchedRootStale:
		return repairSuggestion{FailureClass: class, Suggestion: "open watched roots and inspect the stale node/root", TargetScreen: ScreenNodes, InspectID: "nodes_watched_roots.open"}
	case FailureClassWatchedRootScanFailed:
		return repairSuggestion{FailureClass: class, Suggestion: "fix path or permissions, then rerun the watcher when safe", TargetScreen: ScreenNodes, InspectID: "nodes_watched_roots.open"}
	case FailureClassStorageExport:
		return repairSuggestion{FailureClass: class, Suggestion: "open Storage and inspect canonical filesystem and catalog status", TargetScreen: ScreenStorage, InspectID: "storage.open"}
	case FailureClassStorageFidelity:
		return repairSuggestion{FailureClass: class, Suggestion: "inspect storage fidelity before modifying files", TargetScreen: ScreenStorage, InspectID: "storage.open"}
	case FailureClassRetentionPayload:
		return repairSuggestion{FailureClass: class, Suggestion: "do not delete source files; inspect retention status first", TargetScreen: ScreenStorage, InspectID: "storage.open", RepairID: "storage.retention.status"}
	case FailureClassIndexFailure:
		return repairSuggestion{FailureClass: class, Suggestion: "open Jobs and retry failed index work", TargetScreen: ScreenJobs, InspectID: "jobs_search.open", RepairID: "index.retry_failed"}
	case FailureClassWorkerFailed:
		return repairSuggestion{FailureClass: class, Suggestion: "open Background and inspect the worker before running it once", TargetScreen: ScreenBackground, InspectID: "background.open"}
	case FailureClassJobFailed:
		return repairSuggestion{FailureClass: class, Suggestion: "open Jobs, inspect logs, retry only when retryable", TargetScreen: ScreenJobs, InspectID: "jobs_search.open"}
	case FailureClassNodeOffline:
		return repairSuggestion{FailureClass: class, Suggestion: "bring the node online and verify heartbeat", TargetScreen: ScreenNodes, InspectID: "nodes_watched_roots.open"}
	case FailureClassDatabaseMigration:
		return repairSuggestion{FailureClass: class, Suggestion: "check migrations and apply only after backup", TargetScreen: ScreenJobs, InspectID: "jobs_search.open"}
	case FailureClassBoxContractInvalid:
		return repairSuggestion{FailureClass: class, Suggestion: "open LOOM Box, fix contracts, then apply watch policy", TargetScreen: ScreenBox, InspectID: "box.open", RepairID: "box.watch_policy.apply"}
	case FailureClassApprovalPending:
		return repairSuggestion{FailureClass: class, Suggestion: "open the owning workflow and approve or reject explicitly", TargetScreen: ScreenAutomations, InspectID: "automations.open"}
	default:
		return repairSuggestion{}
	}
}

func classifyAttentionFailure(item AttentionItem) string {
	text := attentionSearchText(item)
	switch {
	case containsAny(text, "approval", "manual action") && strings.Contains(text, "pending"):
		return FailureClassApprovalPending
	case strings.Contains(text, "wireguard") || strings.Contains(text, "10.44") || strings.Contains(text, "private route"):
		return FailureClassWireGuardDown
	case strings.Contains(text, "smb") || strings.Contains(text, "mount"):
		return FailureClassSMBUnavailable
	case strings.Contains(text, "cloud") && containsAny(text, "cooldown", "rate limit", "429", "too many"):
		return FailureClassCloudCooldown
	case strings.Contains(text, "cloud") && containsAny(text, "auth", "credential", "permission", "denied", "forbidden", "unauthorized"):
		return FailureClassCloudAuth
	case strings.Contains(text, "cloud") && containsAny(text, "unreachable", "timeout", "dial", "connection", "refused", "network"):
		return FailureClassCloudUnreachable
	case strings.Contains(text, "retention") && containsAny(text, "payload", "missing", "not found"):
		return FailureClassRetentionPayload
	case strings.Contains(text, "fidelity"):
		return FailureClassStorageFidelity
	case strings.Contains(text, "storage") && strings.Contains(text, "export") && containsAny(text, "stale", "failed", "error"):
		return FailureClassStorageExport
	case strings.Contains(text, "migration"):
		return FailureClassDatabaseMigration
	case strings.Contains(text, "box") && containsAny(text, "contract", "invalid", "diagnostic", "policy"):
		return FailureClassBoxContractInvalid
	case strings.Contains(text, "watched") && strings.Contains(text, "stale"):
		return FailureClassWatchedRootStale
	case strings.Contains(text, "watched") && containsAny(text, "scan", "failed", "error"):
		return FailureClassWatchedRootScanFailed
	case strings.HasPrefix(item.ID, "indexes.") || strings.Contains(text, "index"):
		return FailureClassIndexFailure
	case strings.Contains(text, "worker"):
		return FailureClassWorkerFailed
	case strings.Contains(text, "job"):
		return FailureClassJobFailed
	case containsAny(item.Domain, "network") || strings.Contains(text, "node status is offline") || strings.Contains(text, "node status is stale"):
		return FailureClassNodeOffline
	default:
		return ""
	}
}

func attentionSearchText(item AttentionItem) string {
	return strings.ToLower(strings.Join([]string{
		item.ID,
		item.Domain,
		item.Title,
		item.Reason,
		item.Suggestion,
		item.TargetKind,
		item.TargetRef,
	}, " "))
}

func attentionSuggestionIsGeneric(suggestion string) bool {
	value := strings.ToLower(strings.TrimSpace(suggestion))
	if value == "" {
		return true
	}
	return strings.HasPrefix(value, "open ") ||
		strings.HasPrefix(value, "run ") ||
		strings.Contains(value, "refresh or inspect")
}

func containsAny(value string, needles ...string) bool {
	value = strings.ToLower(value)
	for _, needle := range needles {
		if strings.Contains(value, strings.ToLower(needle)) {
			return true
		}
	}
	return false
}
