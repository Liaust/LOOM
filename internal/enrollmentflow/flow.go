package enrollmentflow

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

func Run(ctx context.Context, spec Spec, main MainRunner, target TargetRunner) (Result, error) {
	spec = normalizeSpec(spec)
	result := Result{Status: StateNotStarted, Redacted: true}
	secrets := Secrets{}
	if err := validateSpec(spec, main, target); err != nil {
		return failResult(result, "validate", StateFailed, FailureValidation, err, secrets), nil
	}

	if spec.Resume {
		seedResumeResult(&result, spec.State)
		if spec.State.Status == StateApproved && !spec.State.CredentialImported && !spec.State.CredentialTokenHeld {
			err := errors.New("approved credential token is no longer available; re-enrollment or credential rotation is required")
			return failResult(result, "resume", StateUnrecoverable, FailureCredentialTokenUnavailable, err, secrets), nil
		}
	}

	if err := target.EnsureNodeAgentInitialized(ctx, NodeAgentInitInput{
		NodeKey:      spec.NodeKey,
		DisplayName:  spec.DisplayName,
		NodeKind:     spec.NodeKind,
		NodeRole:     spec.NodeRole,
		RuntimeClass: spec.RuntimeClass,
	}); err != nil {
		return failResult(result, "ensure_target_initialized", StateFailed, FailureValidation, err, secrets), err
	}
	result.Steps = append(result.Steps, StepResult{ID: "ensure_target_initialized", Status: StepStatusSucceeded})

	if shouldCreateToken(spec, result) {
		token, err := main.CreateEnrollmentToken(ctx, CreateTokenInput{TTLSeconds: spec.TokenTTLSeconds})
		if err != nil {
			return failResult(result, "create_token", StateFailed, FailureTokenCreate, err, secrets), err
		}
		secrets.EnrollmentToken = token.TokenValue
		result.Status = StateTokenCreated
		result.TokenID = token.TokenID
		result.TokenHint = token.TokenHint
		result.TokenExpiresAt = token.ExpiresAt
		result.Steps = append(result.Steps, StepResult{ID: "create_token", Status: StepStatusSucceeded})
	} else {
		result.Steps = append(result.Steps, StepResult{ID: "create_token", Status: StepStatusSkipped, Message: "resumed from submitted request"})
	}

	if shouldSubmitRequest(result) {
		request, err := target.SubmitEnrollmentRequest(ctx, secrets.EnrollmentToken)
		if err != nil {
			return failResult(result, "submit_request", StateFailed, FailureRequestSubmit, err, secrets), err
		}
		result.Status = StateRequestSubmitted
		result.EnrollmentRequestID = request.EnrollmentRequestID
		result.Steps = append(result.Steps, StepResult{ID: "submit_request", Status: StepStatusSucceeded})
	} else {
		result.Steps = append(result.Steps, StepResult{ID: "submit_request", Status: StepStatusSkipped, Message: "request already submitted"})
	}

	if !spec.Approve {
		result.Steps = append(result.Steps, StepResult{ID: "approve_request", Status: StepStatusSkipped, Message: "approval disabled"})
		return result, nil
	}

	if shouldApprove(result) {
		approval, err := main.ApproveEnrollment(ctx, result.EnrollmentRequestID)
		if err != nil {
			return failResult(result, "approve_request", StateFailed, FailureApproval, err, secrets), err
		}
		secrets.CredentialToken = approval.CredentialToken
		result.Status = StateApproved
		result.NodeID = approval.NodeID
		result.NodeCredentialID = approval.NodeCredentialID
		result.CredentialHint = approval.CredentialHint
		result.Steps = append(result.Steps, StepResult{ID: "approve_request", Status: StepStatusSucceeded})
	} else {
		result.Steps = append(result.Steps, StepResult{ID: "approve_request", Status: StepStatusSkipped, Message: "request already approved"})
	}

	if shouldImportCredential(result, spec.State) {
		if strings.TrimSpace(secrets.CredentialToken) == "" {
			err := errors.New("credential token is unavailable after approval")
			return failResult(result, "import_credential", StateUnrecoverable, FailureCredentialTokenUnavailable, err, secrets), nil
		}
		if err := target.ImportCredential(ctx, CredentialImportInput{
			NodeID:           result.NodeID,
			NodeCredentialID: result.NodeCredentialID,
			CredentialToken:  secrets.CredentialToken,
		}); err != nil {
			return failResult(result, "import_credential", StateFailed, FailureCredentialImport, err, secrets), err
		}
		result.Status = StateCredentialImported
		result.Steps = append(result.Steps, StepResult{ID: "import_credential", Status: StepStatusSucceeded})
	} else {
		result.Steps = append(result.Steps, StepResult{ID: "import_credential", Status: StepStatusSkipped, Message: "credential already imported"})
	}

	if !spec.VerifyHeartbeat {
		result.Steps = append(result.Steps, StepResult{ID: "heartbeat_once", Status: StepStatusSkipped, Message: "heartbeat disabled"})
		return result, nil
	}

	if result.HeartbeatID == "" {
		heartbeat, err := target.HeartbeatOnce(ctx)
		if err != nil {
			return failResult(result, "heartbeat_once", StateFailed, FailureHeartbeat, err, secrets), err
		}
		result.Status = StateHeartbeatVerified
		result.HeartbeatID = heartbeat.HeartbeatID
		if result.NodeID == "" {
			result.NodeID = heartbeat.NodeID
		}
		result.PresenceState = heartbeat.PresenceState
		result.HeartbeatAt = heartbeat.ReceivedAt
		result.Steps = append(result.Steps, StepResult{ID: "heartbeat_once", Status: StepStatusSucceeded})
	} else {
		result.Status = StateHeartbeatVerified
		result.Steps = append(result.Steps, StepResult{ID: "heartbeat_once", Status: StepStatusSkipped, Message: "heartbeat already verified"})
	}

	if !spec.VerifyMain {
		result.Steps = append(result.Steps, StepResult{ID: "verify_main", Status: StepStatusSkipped, Message: "main verification disabled"})
		return result, nil
	}
	health, err := main.GetNodeHealth(ctx, firstNonEmpty(result.NodeID, spec.NodeKey))
	if err != nil {
		return failResult(result, "verify_main", StateFailed, FailureMainVerification, err, secrets), err
	}
	result.Status = StateVerifiedOnMain
	result.VerifiedOnMain = true
	if result.NodeID == "" {
		result.NodeID = health.NodeID
	}
	if result.PresenceState == "" {
		result.PresenceState = health.PresenceState
	}
	result.Steps = append(result.Steps, StepResult{ID: "verify_main", Status: StepStatusSucceeded})
	return result, nil
}

func normalizeSpec(spec Spec) Spec {
	spec.NodeKey = strings.TrimSpace(spec.NodeKey)
	spec.DisplayName = strings.TrimSpace(spec.DisplayName)
	spec.NodeKind = strings.TrimSpace(spec.NodeKind)
	spec.NodeRole = strings.TrimSpace(spec.NodeRole)
	spec.RuntimeClass = strings.TrimSpace(spec.RuntimeClass)
	if spec.TokenTTLSeconds <= 0 {
		spec.TokenTTLSeconds = 1800
	}
	if spec.State.Status == "" {
		spec.State.Status = StateNotStarted
	}
	return spec
}

func validateSpec(spec Spec, main MainRunner, target TargetRunner) error {
	if spec.NodeKey == "" {
		return errors.New("node key is required")
	}
	if spec.DisplayName == "" {
		return errors.New("display name is required")
	}
	if spec.NodeKind == "" {
		return errors.New("node kind is required")
	}
	if main == nil {
		return errors.New("main enrollment runner is required")
	}
	if target == nil {
		return errors.New("target enrollment runner is required")
	}
	return nil
}

func seedResumeResult(result *Result, state ResumeState) {
	result.Status = state.Status
	result.EnrollmentRequestID = state.EnrollmentRequestID
	result.NodeID = state.NodeID
	result.NodeCredentialID = state.NodeCredentialID
	result.CredentialHint = state.CredentialHint
	result.HeartbeatID = state.HeartbeatID
	result.VerifiedOnMain = state.VerifiedOnMain
	if !state.TokenExpiresAt.IsZero() {
		result.TokenExpiresAt = state.TokenExpiresAt
	}
}

func shouldCreateToken(spec Spec, result Result) bool {
	if !spec.Resume {
		return true
	}
	if result.EnrollmentRequestID != "" {
		return false
	}
	if result.Status != StateTokenCreated {
		return true
	}
	return !result.TokenExpiresAt.IsZero() && result.TokenExpiresAt.Before(time.Now().UTC())
}

func shouldSubmitRequest(result Result) bool {
	return strings.TrimSpace(result.EnrollmentRequestID) == ""
}

func shouldApprove(result Result) bool {
	return strings.TrimSpace(result.NodeCredentialID) == ""
}

func shouldImportCredential(result Result, state ResumeState) bool {
	if state.CredentialImported || result.Status == StateCredentialImported || result.Status == StateHeartbeatVerified || result.Status == StateVerifiedOnMain {
		return false
	}
	return true
}

func failResult(result Result, stepID, status, code string, err error, secrets Secrets) Result {
	stepStatus := StepStatusFailed
	if status == StateUnrecoverable {
		stepStatus = StepStatusUnrecoverable
	}
	result.Status = status
	result.FailureCode = code
	result.FailureMessage = RedactString(fmt.Sprint(err), secrets)
	result.Redacted = true
	result.Steps = append(result.Steps, StepResult{ID: stepID, Status: stepStatus, Message: result.FailureMessage})
	return result
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
