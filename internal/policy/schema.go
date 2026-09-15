package policy

func ValidDecision(value string) bool {
	switch value {
	case DecisionAllow, DecisionDeny, DecisionApprovalRequired:
		return true
	default:
		return false
	}
}

func ValidApprovalStatus(value string) bool {
	switch value {
	case ApprovalPending, ApprovalApproved, ApprovalDenied, ApprovalExpired, ApprovalCancelled, ApprovalSuperseded:
		return true
	default:
		return false
	}
}

func ValidApprovalDecision(value string) bool {
	switch value {
	case ApprovalDecisionApprove, ApprovalDecisionDeny:
		return true
	default:
		return false
	}
}

func ValidGrantStatus(value string) bool {
	switch value {
	case GrantActive, GrantConsumed, GrantExpired, GrantRevoked, GrantSuspended:
		return true
	default:
		return false
	}
}

func ValidGrantType(value string) bool {
	switch value {
	case GrantOneShot, GrantElevation, GrantJob, GrantWorkflow, GrantProject, GrantRoute, GrantTransfer, GrantCredentialUse, GrantBreakGlass:
		return true
	default:
		return false
	}
}

func ValidRiskLevel(value string) bool {
	switch value {
	case RiskLow, RiskMedium, RiskHigh, RiskCritical:
		return true
	default:
		return false
	}
}

func ValidAuthorizationLevel(value int) bool {
	return value >= 1 && value <= 5
}
