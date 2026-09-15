package jobs

import "strings"

const (
	TypeScriptRun   = "script_run"
	TypeWorkflowRun = "workflow_run"

	StatusCreated   = "created"
	StatusQueued    = "queued"
	StatusRunning   = "running"
	StatusCompleted = "completed"
	StatusFailed    = "failed"
	StatusCancelled = "cancelled"
	StatusTimedOut  = "timed_out"

	FailureAttentionStatusActive       = "active"
	FailureAttentionStatusAcknowledged = "acknowledged"
	FailureAttentionStatusArchived     = "archived"
	FailureAttentionStatusAll          = "all"

	RunnerStatusStarting = "starting"
	RunnerStatusIdle     = "idle"
	RunnerStatusRunning  = "running"
	RunnerStatusDraining = "draining"
	RunnerStatusOffline  = "offline"
	RunnerStatusFailed   = "failed"

	ScriptRunEnqueueOnly       = "enqueue_only"
	ScriptRunWaitUntilStarted  = "wait_until_started"
	ScriptRunWaitForCompletion = "wait_for_completion"

	WaitResultQueued      = "queued"
	WaitResultStarted     = "started"
	WaitResultCompleted   = "completed"
	WaitResultFailed      = "failed"
	WaitResultCancelled   = "cancelled"
	WaitResultTimedOut    = "timed_out"
	WaitResultWaitTimeout = "wait_timeout"
)

var allowedStatuses = map[string]struct{}{
	StatusCreated:   {},
	StatusQueued:    {},
	StatusRunning:   {},
	StatusCompleted: {},
	StatusFailed:    {},
	StatusCancelled: {},
	StatusTimedOut:  {},
}

var allowedFailureAttentionStatuses = map[string]struct{}{
	FailureAttentionStatusActive:       {},
	FailureAttentionStatusAcknowledged: {},
	FailureAttentionStatusArchived:     {},
}

func ValidStatus(status string) bool {
	_, ok := allowedStatuses[status]
	return ok
}

func ValidFailureAttentionStatus(status string) bool {
	_, ok := allowedFailureAttentionStatuses[NormalizeFailureAttentionStatus(status)]
	return ok
}

func NormalizeFailureAttentionStatus(status string) string {
	status = strings.ToLower(strings.TrimSpace(status))
	if status == "" {
		return ""
	}
	if _, ok := allowedFailureAttentionStatuses[status]; ok {
		return status
	}
	return ""
}

func EffectiveFailureAttentionStatus(status string) string {
	if normalized := NormalizeFailureAttentionStatus(status); normalized != "" {
		return normalized
	}
	return FailureAttentionStatusActive
}

func JobNeedsFailureAttention(job Job) bool {
	switch job.Status {
	case StatusFailed, StatusTimedOut:
		return true
	default:
		return job.ManualAction
	}
}

func FailureAttentionStatusForTransition(status string) string {
	switch status {
	case StatusFailed, StatusTimedOut:
		return FailureAttentionStatusActive
	case StatusCompleted, StatusCancelled:
		return FailureAttentionStatusArchived
	default:
		return ""
	}
}

func NormalizeScriptRunMode(mode string) string {
	switch mode {
	case ScriptRunEnqueueOnly, ScriptRunWaitUntilStarted, ScriptRunWaitForCompletion:
		return mode
	case "":
		return ScriptRunWaitForCompletion
	default:
		return ""
	}
}

func IsTerminalStatus(status string) bool {
	switch status {
	case StatusCompleted, StatusFailed, StatusCancelled, StatusTimedOut:
		return true
	default:
		return false
	}
}
