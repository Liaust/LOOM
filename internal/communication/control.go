package communication

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strings"
)

const (
	ControlEvidenceSchemaVersion = "loom.control.evidence.v1"

	RevisionDecisionApply      = "apply"
	RevisionDecisionIdempotent = "idempotent"
	RevisionDecisionStale      = "stale"
	RevisionDecisionConflict   = "conflict"

	ControlOutcomeCompleted = "completed"
	ControlOutcomeStale     = "stale"
	ControlOutcomeConflict  = "conflict"
	ControlOutcomeFailed    = "failed"
)

var configHashPattern = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)

// DesiredStateEvidence is the deliberately small, domain-neutral portion of a
// main-to-node desired-state request. Domains own their payload and lifecycle.
type DesiredStateEvidence struct {
	SchemaVersion   string `json:"schema_version"`
	TargetNode      string `json:"target_node"`
	DesiredRevision int64  `json:"desired_revision"`
	ConfigHash      string `json:"config_hash"`
}

// AppliedStateEvidence is the reusable acknowledgement evidence. Domain
// handlers keep their own bounded result and error fields beside this value.
type AppliedStateEvidence struct {
	SchemaVersion   string `json:"schema_version"`
	DesiredRevision int64  `json:"desired_revision"`
	AppliedRevision int64  `json:"applied_revision"`
	ConfigHash      string `json:"config_hash"`
	Outcome         string `json:"outcome"`
}

func NewDesiredStateEvidence(targetNode string, revision int64, configHash string) DesiredStateEvidence {
	return DesiredStateEvidence{
		SchemaVersion:   ControlEvidenceSchemaVersion,
		TargetNode:      strings.TrimSpace(targetNode),
		DesiredRevision: revision,
		ConfigHash:      strings.TrimSpace(configHash),
	}
}

func (e DesiredStateEvidence) Validate() error {
	if e.SchemaVersion != ControlEvidenceSchemaVersion {
		return fmt.Errorf("control evidence schema_version must be %q", ControlEvidenceSchemaVersion)
	}
	if strings.TrimSpace(e.TargetNode) == "" {
		return fmt.Errorf("control evidence target_node is required")
	}
	if e.DesiredRevision <= 0 {
		return fmt.Errorf("control evidence desired_revision must be positive")
	}
	if !configHashPattern.MatchString(strings.TrimSpace(e.ConfigHash)) {
		return fmt.Errorf("control evidence config_hash must be a sha256 digest")
	}
	return nil
}

func (e AppliedStateEvidence) Validate() error {
	if e.SchemaVersion != ControlEvidenceSchemaVersion {
		return fmt.Errorf("applied evidence schema_version must be %q", ControlEvidenceSchemaVersion)
	}
	if e.DesiredRevision <= 0 || e.AppliedRevision < 0 {
		return fmt.Errorf("applied evidence revisions are invalid")
	}
	if !configHashPattern.MatchString(strings.TrimSpace(e.ConfigHash)) {
		return fmt.Errorf("applied evidence config_hash must be a sha256 digest")
	}
	switch e.Outcome {
	case ControlOutcomeCompleted, ControlOutcomeStale, ControlOutcomeConflict, ControlOutcomeFailed:
		return nil
	default:
		return fmt.Errorf("unsupported control outcome %q", e.Outcome)
	}
}

// CompareRevision implements the shared stale/equal/conflict convention. A
// domain handler still decides how to stage and apply its own payload.
func CompareRevision(appliedRevision int64, appliedHash string, desired DesiredStateEvidence) (string, error) {
	if err := desired.Validate(); err != nil {
		return "", err
	}
	if appliedRevision < 0 {
		return "", fmt.Errorf("applied revision must not be negative")
	}
	switch {
	case desired.DesiredRevision > appliedRevision:
		return RevisionDecisionApply, nil
	case desired.DesiredRevision < appliedRevision:
		return RevisionDecisionStale, nil
	case strings.TrimSpace(appliedHash) == desired.ConfigHash:
		return RevisionDecisionIdempotent, nil
	default:
		return RevisionDecisionConflict, nil
	}
}

// ConfigHash returns the deterministic digest used for desired-state evidence.
// encoding/json sorts map keys, making semantically identical object values
// stable across callers.
func ConfigHash(value any) (string, error) {
	payload, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("marshal desired configuration: %w", err)
	}
	sum := sha256.Sum256(payload)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

// DecodeStrictJSONObject rejects unknown fields and trailing JSON. Domain
// packages use it for versioned built-in node-control payloads.
func DecodeStrictJSONObject(payload []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return fmt.Errorf("control payload contains trailing JSON")
		}
		return err
	}
	return nil
}
