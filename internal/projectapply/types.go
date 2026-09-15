// Package projectapply coordinates durable declaration operations. It never
// executes shell commands, installs applications or deletes managed payloads.
package projectapply

import (
	"context"
	"database/sql"
	"encoding/json"

	pc "loom.local/loom/internal/projectcontracts"
)

// Principal must come from authenticated transport, never request JSON.
type Principal struct {
	ActorID      string `json:"actor_id"`
	OriginNodeID string `json:"origin_node_id"`
}

// Resolution contains reference-only normalized owner input, not source file
// inventories, credential values or arbitrary operational output. Resolver and
// Owner.Validate jointly own the action's typed input schema.
type Resolution struct {
	Plan     pc.DeclarationPlan         `json:"plan"`
	Payloads map[string]json.RawMessage `json:"payloads"`
}

type Prerequisites struct {
	Target    pc.DeclarationTarget                     `json:"target"`
	Sources   []pc.DeclarationSource                   `json:"sources"`
	Bindings  map[pc.ResourceKey]pc.DeclarationBinding `json:"bindings"`
	Revisions map[string]string                        `json:"revisions"`
}

// Both methods are strictly read-only. Current resolves the original target and
// full source/prerequisite closure, even when some original resources retired.
// It must not mask drift using operation existence or completed receipts.
type Resolver interface {
	Resolve(context.Context, Principal, pc.DeclarationPlanRequest) (Resolution, error)
	Current(context.Context, Principal, Resolution) (Prerequisites, error)
}

type Authorizer interface {
	Read(context.Context, Principal, pc.DeclarationTarget) error
	// Execute checks current actor+origin+target access, policy revision, required
	// level and exact action/plan/approval scope. The snapshot is not authority.
	Execute(context.Context, Principal, pc.DeclarationPlanBasis, pc.DeclarationAction, []string) error
}

type ObservationState string

const (
	Absent    ObservationState = "absent"
	Committed ObservationState = "committed"
	Pending   ObservationState = "pending"
	Uncertain ObservationState = "uncertain"
	// Resumable is an exact owner-journaled partial effect, not absent delivery.
	// A fresh authorized Apply may continue it with the original owner token.
	Resumable ObservationState = "resumable"
)

type RevisionChange struct {
	Before string `json:"before"`
	After  string `json:"after"`
}
type BindingChange struct {
	Before pc.DeclarationBinding `json:"before"`
	After  pc.DeclarationBinding `json:"after"`
}

// Receipt is durable evidence from the owner, recoverable by the exact token.
// Changes describe only this action's committed mutation. Core checks their
// namespace, before value and resource; sources/target/auth cannot be rebased.
type Receipt struct {
	Token     string                           `json:"token"`
	ActionID  string                           `json:"action_id"`
	Owner     pc.DeclarationOwner              `json:"owner"`
	InputHash string                           `json:"input_hash"`
	EffectRef string                           `json:"effect_ref"`
	Revisions map[string]RevisionChange        `json:"revisions"`
	Bindings  map[pc.ResourceKey]BindingChange `json:"bindings"`
}

type Observation struct {
	State     ObservationState
	Receipt   *Receipt
	CauseCode string
}

// Fence.Check refuses when the owned PostgreSQL session/lock is lost or the
// request is cancelled. Owners must honor context and check immediately before
// their commit; remote adapters additionally own token dedup and commit fencing.
// Core checks after every callback and before each dispatch. In-flight external
// commits remain uncertain until the same token can be observed.
type Fence interface{ Check(context.Context) error }

type ActionCall struct {
	Principal   Principal
	OperationID string
	PlanID      string
	Target      pc.DeclarationTarget
	Action      pc.DeclarationAction
	Payload     json.RawMessage
	Token       string
	// Expected is the exact prerequisite state before this action. Owners must
	// atomically compare their relevant current revision/binding against it when
	// committing the effect, in the same durable transaction as token dedup.
	// A callback fence or cancelled context alone cannot fence external delivery.
	Expected     Prerequisites
	Dependencies map[string]Receipt
	Fence        Fence
}

type Owner interface {
	Validate(context.Context, pc.DeclarationAction, json.RawMessage) error
	Observe(context.Context, ActionCall) (Observation, error)
	// Apply is idempotent by Token, including after process loss. An error says
	// nothing about commit; Observe must establish absence before another apply.
	Apply(context.Context, ActionCall) (Observation, error)
}

// Failure intentionally carries only a stable public code; raw dependency
// errors are never copied into results, logs or durable journal payloads.
type Failure struct {
	Code      pc.DeclarationErrorCode
	Cause     string
	Preflight []pc.DeclarationPreflightIssue
}

func (e *Failure) Error() string { return string(e.Code) + ": " + e.Cause }

// Service uses an operation journal, not jobs.jobs. Its job_ IDs are opaque D0
// operation references from ids.NewJobID, never runnable script/workflow jobs.
type Service struct {
	db       *sql.DB
	resolver Resolver
	auth     Authorizer
	owners   map[pc.DeclarationOwner]Owner
}

func NewService(db *sql.DB, resolver Resolver, auth Authorizer, owners map[pc.DeclarationOwner]Owner) *Service {
	copyOwners := make(map[pc.DeclarationOwner]Owner, len(owners))
	for k, v := range owners {
		copyOwners[k] = v
	}
	return &Service{db: db, resolver: resolver, auth: auth, owners: copyOwners}
}
