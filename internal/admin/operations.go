package admin

import "time"

const (
	OperationJobCancel        = "main@admin.job.cancel"
	OperationIndexRebuild     = "main@admin.index.rebuild"
	OperationNodeQuarantine   = "main@admin.node.quarantine"
	OperationCredentialRevoke = "main@admin.credential.revoke"
)

const (
	StatusRequested = "requested"
	StatusApproved  = "approved"
	StatusRejected  = "rejected"
	StatusRunning   = "running"
	StatusCompleted = "completed"
	StatusFailed    = "failed"
	StatusCancelled = "cancelled"
)

type Operation struct {
	AdminOperationID string
	OperationType    string
	ActorID          string
	NodeID           string
	ScopeID          string
	TargetKind       string
	TargetRef        string
	Authorization    int
	Status           string
	DryRun           bool
	RequestedAt      time.Time
	StartedAt        *time.Time
	CompletedAt      *time.Time
	ErrorCode        string
	ErrorMessage     string
	CorrelationID    string
}

func ValidAuthorizationLevel(level int) bool {
	return level >= 1 && level <= 5
}

func ValidStatus(status string) bool {
	switch status {
	case StatusRequested,
		StatusApproved,
		StatusRejected,
		StatusRunning,
		StatusCompleted,
		StatusFailed,
		StatusCancelled:
		return true
	default:
		return false
	}
}

func KnownOperationType(operationType string) bool {
	switch operationType {
	case OperationJobCancel,
		OperationIndexRebuild,
		OperationNodeQuarantine,
		OperationCredentialRevoke:
		return true
	default:
		return false
	}
}
