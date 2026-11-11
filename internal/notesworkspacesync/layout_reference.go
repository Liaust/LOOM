package notesworkspacesync

import (
	"context"
	"errors"
	"strings"
	"time"

	"loom.local/loom/internal/notesworkspace"
)

type referenceBudget struct {
	started  time.Time
	bytes    int64
	attempts int
}

// Bounded, payload-free diagnostics in the existing run result. EvidenceKey is
// navigation identity, not a publication receipt or a user-facing source path.
type LayoutAttemptDetail struct {
	Operation    string `json:"operation"`
	EvidenceKey  string `json:"evidence_key"`
	Reason       string `json:"reason,omitempty"`
	ReadAttempts int    `json:"read_attempts"`
	ReadBytes    int64  `json:"read_bytes"`
	ElapsedMS    int64  `json:"elapsed_ms"`
}

var errLayoutBudget = errors.New("layout reference admission budget exhausted")

func (s *Service) admitLayoutReference(size int64) error {
	b := s.layoutBudget
	if b == nil || b.attempts == 0 {
		return nil
	}
	now := time.Now()
	if s.Now != nil {
		now = s.Now()
	}
	if b.bytes >= deliverySoftBytes || b.bytes > 0 && size > deliverySoftBytes-b.bytes {
		s.LayoutPressure = "soft_byte_budget"
		return errLayoutBudget
	}
	if now.Sub(b.started) >= deliveryAdmissionWindow {
		s.LayoutPressure = "admission_window"
		return errLayoutBudget
	}
	return nil
}

func (s *Service) fileObservation(ctx context.Context, file notesworkspace.File) (InventoryEntry, error) {
	if source, ok := s.Source.(interface {
		FileObservation(context.Context, notesworkspace.File) (InventoryEntry, error)
	}); ok {
		return source.FileObservation(ctx, file)
	}
	return InventoryEntry{}, nil // Unknown metadata never grants export authority.
}

// Separate from delivery's reference_attempt cursor: a layout has a different
// immutable native request and must never replace a pending content publication.
type referenceLayoutAttempt struct {
	Scope               Scope
	Fingerprint         string
	Previous            Binding
	BaseID, OperationID string
}

func referenceLayoutAttemptKey(scope Scope, mapping FileMapping) string {
	return "layout_reference_attempt_" + key(scope.Workspace, scope.Collection, scope.Generation, scope.Root, mapping.ClientFileID)
}

type layoutProbe struct{ Status, Reason, Outcome string }

func referenceLayoutHold(reason string) error {
	switch reason {
	case "layout_previous_changed", "layout_destination_collision", "layout_publication_ambiguous":
		return referenceHold("reference_" + reason)
	default:
		return referenceHold("reference_layout_evidence_held")
	}
}

func (s *Service) referenceLayoutProbe(ctx context.Context, r Records, scope Scope, mapping FileMapping, fingerprint string) (probe layoutProbe, err error) {
	if mapping.Latest == nil {
		return probe, referenceHold("reference_mapping_evidence_held")
	}
	request := map[string]any{"previous": mapping.Latest, "path": scope.Root + "/" + trimLayoutPath(mapping)}
	prior, e := load[referenceLayoutAttempt](ctx, r, "cursor", referenceLayoutAttemptKey(scope, mapping))
	if e != nil && !missing(e) {
		return probe, e
	}
	if e == nil && prior.Scope == scope && prior.Previous == *mapping.Latest && prior.Fingerprint == fingerprint {
		request["operationId"] = prior.OperationID
	}
	err = s.call(ctx, "layout.preflight", request, &probe)
	return
}

func trimLayoutPath(mapping FileMapping) string {
	return strings.TrimPrefix(mapping.Latest.Path, mapping.Scope.Root+"/")
}
