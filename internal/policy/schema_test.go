package policy

import "testing"

func TestValidationHelpersAcceptKnownValues(t *testing.T) {
	tests := []struct {
		name string
		ok   bool
	}{
		{name: "decision", ok: ValidDecision(DecisionApprovalRequired)},
		{name: "approval decision", ok: ValidApprovalDecision(ApprovalDecisionApprove)},
		{name: "approval status", ok: ValidApprovalStatus(ApprovalPending)},
		{name: "grant status", ok: ValidGrantStatus(GrantActive)},
		{name: "grant type", ok: ValidGrantType(GrantElevation)},
		{name: "risk level", ok: ValidRiskLevel(RiskHigh)},
		{name: "authorization level", ok: ValidAuthorizationLevel(5)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if !tt.ok {
				t.Fatal("validation helper rejected known value")
			}
		})
	}
}

func TestValidationHelpersRejectUnknownValues(t *testing.T) {
	tests := []struct {
		name string
		ok   bool
	}{
		{name: "decision", ok: ValidDecision("requires_approval")},
		{name: "approval decision", ok: ValidApprovalDecision("approved")},
		{name: "approval status", ok: ValidApprovalStatus("active")},
		{name: "grant status", ok: ValidGrantStatus("approved")},
		{name: "grant type", ok: ValidGrantType("global")},
		{name: "risk level", ok: ValidRiskLevel("dangerous")},
		{name: "authorization level low", ok: ValidAuthorizationLevel(0)},
		{name: "authorization level high", ok: ValidAuthorizationLevel(6)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.ok {
				t.Fatal("validation helper accepted unknown value")
			}
		})
	}
}
