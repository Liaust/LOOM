package projectwatch

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"loom.local/loom/internal/communication"
	pc "loom.local/loom/internal/projectcontracts"
	"loom.local/loom/internal/projects"
	"loom.local/loom/internal/requestctx"
)

// DeclarationReconciler validates typed desired input before delegating its
// atomic intent, registration and transport transaction to the project owner.
type DeclarationReconciler struct{ Projects projects.Service }

func NewDeclarationReconciler(db *sql.DB) DeclarationReconciler {
	return DeclarationReconciler{Projects: projects.NewService(db)}
}
func ValidateDeclarationWatchIntent(raw []byte) (projects.DeclarationWatchIntentPayload, error) {
	var payload projects.DeclarationWatchIntentPayload
	if len(raw) > communication.MaxProtectedFolderControlPayloadBytes {
		return payload, fmt.Errorf("watch intent exceeds bound")
	}
	if _, err := pc.DecodeDeclarationEvidenceJSON(raw); err != nil {
		return payload, err
	}
	if err := communication.DecodeStrictJSONObject(raw, &payload); err != nil {
		return payload, err
	}
	if err := ValidateDeclarationWatchGroup(payload.Group); err != nil {
		return payload, err
	}
	hash, err := DeclarationGroupHash(payload.Group)
	if err != nil || hash != payload.GroupHash {
		return payload, fmt.Errorf("watch intent group mismatch")
	}
	for _, c := range payload.Group.Contributors {
		if c.Owner == payload.Owner && c.Resource == payload.Resource && c.Retire == payload.Retire {
			return payload, nil
		}
	}
	return payload, fmt.Errorf("watch intent contributor missing")
}
func (r DeclarationReconciler) Observe(ctx context.Context, req requestctx.Context, call projects.DeclarationWatchIntentRequest) (*projects.DeclarationWatchIntentObservation, error) {
	raw, err := json.Marshal(call.Payload)
	if err != nil {
		return nil, err
	}
	if _, err = ValidateDeclarationWatchIntent(raw); err != nil {
		return nil, err
	}
	return r.Projects.ObserveDeclarationWatchIntent(ctx, req, call)
}
func (r DeclarationReconciler) Commit(ctx context.Context, req requestctx.Context, call projects.DeclarationWatchIntentRequest, fence projects.DeclarationCommitFence) (*projects.DeclarationWatchIntentObservation, error) {
	raw, err := json.Marshal(call.Payload)
	if err != nil {
		return nil, err
	}
	if _, err = ValidateDeclarationWatchIntent(raw); err != nil {
		return nil, err
	}
	return r.Projects.CommitDeclarationWatchIntent(ctx, req, call, fence)
}
