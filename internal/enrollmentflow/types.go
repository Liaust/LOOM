package enrollmentflow

import (
	"context"
	"time"
)

const (
	StateNotStarted         = "not_started"
	StateTokenCreated       = "token_created"
	StateRequestSubmitted   = "request_submitted"
	StateApproved           = "approved"
	StateCredentialImported = "credential_imported"
	StateHeartbeatVerified  = "heartbeat_verified"
	StateVerifiedOnMain     = "verified_on_main"
	StateFailed             = "failed"
	StateUnrecoverable      = "unrecoverable"

	StepStatusSucceeded     = "succeeded"
	StepStatusSkipped       = "skipped"
	StepStatusFailed        = "failed"
	StepStatusUnrecoverable = "unrecoverable"

	FailureValidation                 = "validation_failed"
	FailureTokenCreate                = "token_create_failed"
	FailureRequestSubmit              = "request_submit_failed"
	FailureApproval                   = "approval_failed"
	FailureCredentialImport           = "credential_import_failed"
	FailureHeartbeat                  = "heartbeat_failed"
	FailureMainVerification           = "main_verification_failed"
	FailureCredentialTokenUnavailable = "credential_token_unavailable"
)

type MainRunner interface {
	CreateEnrollmentToken(context.Context, CreateTokenInput) (CreateTokenResult, error)
	ApproveEnrollment(context.Context, string) (ApprovalResult, error)
	GetNodeHealth(context.Context, string) (NodeHealthResult, error)
}

type TargetRunner interface {
	EnsureNodeAgentInitialized(context.Context, NodeAgentInitInput) error
	SubmitEnrollmentRequest(context.Context, string) (EnrollmentRequestResult, error)
	ImportCredential(context.Context, CredentialImportInput) error
	HeartbeatOnce(context.Context) (HeartbeatResult, error)
}

type Spec struct {
	NodeKey      string
	DisplayName  string
	NodeKind     string
	NodeRole     string
	RuntimeClass string

	TokenTTLSeconds int
	Approve         bool
	VerifyHeartbeat bool
	VerifyMain      bool
	Resume          bool
	State           ResumeState
}

type ResumeState struct {
	Status              string
	TokenExpiresAt      time.Time
	EnrollmentRequestID string
	NodeID              string
	NodeCredentialID    string
	CredentialHint      string
	CredentialImported  bool
	HeartbeatID         string
	VerifiedOnMain      bool
	CredentialTokenHeld bool
}

type Secrets struct {
	EnrollmentToken string
	CredentialToken string
}

type Result struct {
	Status string `json:"status"`

	TokenID        string    `json:"token_id,omitempty"`
	TokenHint      string    `json:"token_hint,omitempty"`
	TokenExpiresAt time.Time `json:"token_expires_at,omitempty"`

	EnrollmentRequestID string `json:"enrollment_request_id,omitempty"`
	NodeID              string `json:"node_id,omitempty"`
	NodeCredentialID    string `json:"node_credential_id,omitempty"`
	CredentialHint      string `json:"credential_hint,omitempty"`

	HeartbeatID    string    `json:"heartbeat_id,omitempty"`
	PresenceState  string    `json:"presence_state,omitempty"`
	HeartbeatAt    time.Time `json:"heartbeat_at,omitempty"`
	VerifiedOnMain bool      `json:"verified_on_main,omitempty"`

	FailureCode    string       `json:"failure_code,omitempty"`
	FailureMessage string       `json:"failure_message,omitempty"`
	Steps          []StepResult `json:"steps,omitempty"`
	Redacted       bool         `json:"redacted"`
}

type StepResult struct {
	ID      string `json:"id"`
	Status  string `json:"status"`
	Message string `json:"message,omitempty"`
}

type CreateTokenInput struct {
	TTLSeconds int
}

type CreateTokenResult struct {
	TokenID    string
	TokenHint  string
	TokenValue string
	ExpiresAt  time.Time
}

type NodeAgentInitInput struct {
	NodeKey      string
	DisplayName  string
	NodeKind     string
	NodeRole     string
	RuntimeClass string
}

type EnrollmentRequestResult struct {
	EnrollmentRequestID string
	Status              string
}

type ApprovalResult struct {
	EnrollmentRequestID string
	NodeID              string
	NodeCredentialID    string
	CredentialHint      string
	CredentialToken     string
	ApprovedAt          time.Time
}

type CredentialImportInput struct {
	NodeID           string
	NodeCredentialID string
	CredentialToken  string
}

type HeartbeatResult struct {
	HeartbeatID   string
	NodeID        string
	PresenceState string
	ReceivedAt    time.Time
}

type NodeHealthResult struct {
	NodeID        string
	NodeKey       string
	PresenceState string
	HeartbeatID   string
	LastSeenAt    *time.Time
}
