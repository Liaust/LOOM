package minidashboard

import "sort"

type Severity string

const (
	SeverityHealthy  Severity = "healthy"
	SeverityActive   Severity = "active"
	SeverityWarning  Severity = "warning"
	SeverityCritical Severity = "critical"
	SeverityUnknown  Severity = "unknown"
)

const (
	AttentionResource   = 1
	AttentionProtection = 2
	AttentionRuntime    = 3
	AttentionWork       = 4
	AttentionNode       = 5
	AttentionFreshness  = 6
)

func severityRank(value Severity) int {
	switch value {
	case SeverityCritical:
		return 5
	case SeverityWarning:
		return 4
	case SeverityActive:
		return 3
	case SeverityHealthy:
		return 2
	default:
		return 1
	}
}

func OverallSeverity(conditions []Condition, active bool, snapshotAvailable bool) Severity {
	overall := SeverityHealthy
	if !snapshotAvailable {
		overall = SeverityUnknown
	}
	for _, condition := range conditions {
		if condition.Severity != SeverityWarning && condition.Severity != SeverityCritical {
			continue
		}
		if severityRank(condition.Severity) > severityRank(overall) {
			overall = condition.Severity
		}
	}
	if overall == SeverityHealthy && active {
		return SeverityActive
	}
	return overall
}

func SelectAttention(conditions []Condition) (Condition, int, bool) {
	actionable := make([]Condition, 0, len(conditions))
	for _, condition := range conditions {
		if !condition.Actionable || (condition.Severity != SeverityWarning && condition.Severity != SeverityCritical) {
			continue
		}
		actionable = append(actionable, condition)
	}
	if len(actionable) == 0 {
		return Condition{}, 0, false
	}
	sort.SliceStable(actionable, func(i, j int) bool {
		left, right := actionable[i], actionable[j]
		if severityRank(left.Severity) != severityRank(right.Severity) {
			return severityRank(left.Severity) > severityRank(right.Severity)
		}
		if left.Priority != right.Priority {
			return left.Priority < right.Priority
		}
		return left.Code < right.Code
	})
	return actionable[0], len(actionable) - 1, true
}

func validSeverity(value Severity) bool {
	switch value {
	case SeverityHealthy, SeverityActive, SeverityWarning, SeverityCritical, SeverityUnknown:
		return true
	default:
		return false
	}
}

func validMetricSeverity(value Severity) bool {
	switch value {
	case SeverityHealthy, SeverityWarning, SeverityCritical:
		return true
	default:
		return false
	}
}
