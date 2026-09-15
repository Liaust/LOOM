package projectquiescence

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
	"time"
)

const (
	RequestSchemaVersion = "project.quiescence_request.v1"
	ReceiptSchemaVersion = "project.quiescence_receipt.v1"
	FenceSchemaVersion   = "project.quiescence_fence.v1"

	TargetKindWatchedRoot = "watched_root_supervisor"
	TargetKindService     = "service_process"
	FacetWatchedRoots     = "watched_roots"
	FacetServices         = "services"
	TargetStateStopped    = "stopped"
	FenceStateActive      = "active"

	MaxRequestBytes = 256 * 1024
	MaxReceiptBytes = 512 * 1024
	MaxFenceBytes   = 32 * 1024
)

var (
	digestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	tokenPattern  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:@/-]{0,255}$`)
	slugPattern   = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,127}$`)
)

// Target is the neutral, typed identity of one owner-node process that must be
// stopped and durably fenced before project custody can move.
type ProjectRuntimeQuiescenceTarget struct {
	Facet     string `json:"facet"`
	Kind      string `json:"kind"`
	OwnerNode string `json:"owner_node"`
	// Key and Ref are temporary aggregate compatibility fields. Exact node
	// requests leave them empty and use the typed variant fields below.
	Key string `json:"key,omitempty"`
	Ref string `json:"ref,omitempty"`

	LocalRootKey   string `json:"local_root_key,omitempty"`
	BackendRootRef string `json:"backend_root_ref,omitempty"`
	WorkerKey      string `json:"worker_key,omitempty"`
	ConfigHash     string `json:"config_hash,omitempty"`

	ProviderKey          string `json:"provider_key,omitempty"`
	ProviderAddress      string `json:"provider_address,omitempty"`
	ProviderID           string `json:"provider_id,omitempty"`
	RuntimeProfileDigest string `json:"runtime_profile_digest,omitempty"`
	AllowlistKey         string `json:"allowlist_key,omitempty"`
	Manager              string `json:"manager,omitempty"`
	Unit                 string `json:"unit,omitempty"`
}

type Target = ProjectRuntimeQuiescenceTarget

type Request struct {
	SchemaVersion string   `json:"schema_version"`
	ProjectID     string   `json:"project_id"`
	ProjectSlug   string   `json:"project_slug"`
	OperationID   string   `json:"operation_id"`
	PlanDigest    string   `json:"plan_digest"`
	NodeKey       string   `json:"node_key,omitempty"`
	Targets       []Target `json:"targets"`
	RequestDigest string   `json:"request_digest,omitempty"`
}

type Evidence struct {
	ProjectRuntimeQuiescenceTarget
	State           string `json:"state"`
	FenceState      string `json:"fence_state,omitempty"`
	TargetReceiptID string `json:"target_receipt_id,omitempty"`
	// ReceiptID is retained as the aggregate-receipt compatibility field used
	// by the fail-closed Main coordinator until Slice 2R-B replaces aggregation.
	ReceiptID  string    `json:"receipt_id,omitempty"`
	ObservedAt time.Time `json:"observed_at"`
}

type Receipt struct {
	SchemaVersion string     `json:"schema_version"`
	RequestDigest string     `json:"request_digest,omitempty"`
	NodeKey       string     `json:"node_key,omitempty"`
	ProjectID     string     `json:"project_id"`
	ProjectSlug   string     `json:"project_slug"`
	OperationID   string     `json:"operation_id"`
	PlanDigest    string     `json:"plan_digest"`
	Evidence      []Evidence `json:"evidence"`
	ReceiptID     string     `json:"receipt_id,omitempty"`
}

type Fence struct {
	SchemaVersion string    `json:"schema_version"`
	RequestDigest string    `json:"request_digest"`
	NodeKey       string    `json:"node_key"`
	ProjectID     string    `json:"project_id"`
	ProjectSlug   string    `json:"project_slug"`
	OperationID   string    `json:"operation_id"`
	PlanDigest    string    `json:"plan_digest"`
	Target        Target    `json:"target"`
	State         string    `json:"state"`
	FencedAt      time.Time `json:"fenced_at"`
}

func CanonicalTargetKey(target Target) string {
	return strings.Join([]string{
		target.Facet, target.Kind, target.OwnerNode, target.Key, target.Ref,
		target.LocalRootKey, target.BackendRootRef, target.WorkerKey, target.ConfigHash,
		target.ProviderKey, target.ProviderAddress, target.ProviderID,
		target.RuntimeProfileDigest, target.AllowlistKey, target.Manager, target.Unit,
	}, "\x00")
}

func TargetLockIdentity(target Target) (string, error) {
	if err := ValidateTarget(target); err != nil {
		return "", err
	}
	switch target.Kind {
	case TargetKindWatchedRoot:
		return target.Kind + ":" + target.WorkerKey, nil
	case TargetKindService:
		return target.Kind + ":" + target.AllowlistKey, nil
	default:
		return "", fmt.Errorf("unsupported quiescence target kind")
	}
}

func ValidateTarget(target Target) error {
	if !boundedToken(target.OwnerNode) || target.Key != "" || target.Ref != "" {
		return fmt.Errorf("quiescence target identity is invalid")
	}
	switch target.Kind {
	case TargetKindWatchedRoot:
		if target.Facet != FacetWatchedRoots || !boundedToken(target.LocalRootKey) || !boundedToken(target.BackendRootRef) || !boundedToken(target.WorkerKey) || !digestPattern.MatchString(target.ConfigHash) {
			return fmt.Errorf("watched-root quiescence target is incomplete")
		}
		if target.ProviderKey != "" || target.ProviderAddress != "" || target.ProviderID != "" || target.RuntimeProfileDigest != "" || target.AllowlistKey != "" || target.Manager != "" || target.Unit != "" {
			return fmt.Errorf("watched-root quiescence target carries service fields")
		}
	case TargetKindService:
		if target.Facet != FacetServices || !boundedToken(target.ProviderKey) || !boundedToken(target.ProviderAddress) || !boundedToken(target.ProviderID) || !digestPattern.MatchString(target.RuntimeProfileDigest) || !boundedToken(target.AllowlistKey) || !boundedToken(target.Unit) {
			return fmt.Errorf("service quiescence target is incomplete")
		}
		if target.Manager != "systemd" && target.Manager != "launchd" {
			return fmt.Errorf("service quiescence target manager is invalid")
		}
		if target.LocalRootKey != "" || target.BackendRootRef != "" || target.WorkerKey != "" || target.ConfigHash != "" {
			return fmt.Errorf("service quiescence target carries watched-root fields")
		}
	default:
		return fmt.Errorf("unsupported quiescence target kind")
	}
	return nil
}

func SealRequest(request *Request) error {
	if request == nil {
		return fmt.Errorf("quiescence request is required")
	}
	request.SchemaVersion = RequestSchemaVersion
	request.Targets = append([]Target(nil), request.Targets...)
	sort.Slice(request.Targets, func(i, j int) bool {
		return CanonicalTargetKey(request.Targets[i]) < CanonicalTargetKey(request.Targets[j])
	})
	request.RequestDigest = ""
	if err := validateRequestIdentity(*request, false); err != nil {
		return err
	}
	digest, err := requestIdentityDigest(*request)
	if err != nil {
		return err
	}
	request.RequestDigest = digest
	return nil
}

func ValidateRequest(request Request) error {
	if err := validateRequestIdentity(request, true); err != nil {
		return err
	}
	digest, err := requestIdentityDigest(request)
	if err != nil {
		return err
	}
	if request.RequestDigest != digest {
		return fmt.Errorf("quiescence request digest mismatch")
	}
	return nil
}

func validateRequestIdentity(request Request, requireDigest bool) error {
	if request.SchemaVersion != RequestSchemaVersion || !boundedToken(request.ProjectID) || !slugPattern.MatchString(request.ProjectSlug) || !boundedToken(request.OperationID) || !digestPattern.MatchString(request.PlanDigest) || !boundedToken(request.NodeKey) {
		return fmt.Errorf("quiescence request identity is invalid")
	}
	if requireDigest && !digestPattern.MatchString(request.RequestDigest) {
		return fmt.Errorf("quiescence request digest is invalid")
	}
	if len(request.Targets) == 0 || len(request.Targets) > 256 {
		return fmt.Errorf("quiescence request targets must be non-empty and bounded")
	}
	for index, target := range request.Targets {
		if err := ValidateTarget(target); err != nil {
			return fmt.Errorf("quiescence target %d: %w", index, err)
		}
		if target.OwnerNode != request.NodeKey {
			return fmt.Errorf("quiescence target owner does not match request node")
		}
		if index > 0 && CanonicalTargetKey(request.Targets[index-1]) >= CanonicalTargetKey(target) {
			return fmt.Errorf("quiescence request targets are not canonical and unique")
		}
	}
	return nil
}

func requestIdentityDigest(request Request) (string, error) {
	identity := struct {
		SchemaVersion string   `json:"schema_version"`
		ProjectID     string   `json:"project_id"`
		ProjectSlug   string   `json:"project_slug"`
		OperationID   string   `json:"operation_id"`
		PlanDigest    string   `json:"plan_digest"`
		NodeKey       string   `json:"node_key"`
		Targets       []Target `json:"targets"`
	}{request.SchemaVersion, request.ProjectID, request.ProjectSlug, request.OperationID, request.PlanDigest, request.NodeKey, request.Targets}
	raw, err := json.Marshal(identity)
	if err != nil {
		return "", err
	}
	return digest(raw), nil
}

func SealReceipt(request Request, receipt *Receipt) error {
	if err := ValidateRequest(request); err != nil {
		return err
	}
	if receipt == nil {
		return fmt.Errorf("quiescence receipt is required")
	}
	receipt.SchemaVersion = ReceiptSchemaVersion
	receipt.RequestDigest = request.RequestDigest
	receipt.NodeKey = request.NodeKey
	receipt.ProjectID = request.ProjectID
	receipt.ProjectSlug = request.ProjectSlug
	receipt.OperationID = request.OperationID
	receipt.PlanDigest = request.PlanDigest
	receipt.ReceiptID = ""
	if err := validateReceiptIdentity(request, *receipt, time.Time{}, time.Time{}, false); err != nil {
		return err
	}
	receipt.ReceiptID = receiptIdentityDigest(*receipt)
	return nil
}

func ValidateReceipt(request Request, receipt Receipt, floor, now time.Time) error {
	if err := ValidateRequest(request); err != nil {
		return err
	}
	if err := validateReceiptIdentity(request, receipt, floor, now, true); err != nil {
		return err
	}
	if receipt.ReceiptID != receiptIdentityDigest(receipt) {
		return fmt.Errorf("quiescence receipt identity mismatch")
	}
	return nil
}

func validateReceiptIdentity(request Request, receipt Receipt, floor, now time.Time, requireID bool) error {
	if receipt.SchemaVersion != ReceiptSchemaVersion || receipt.RequestDigest != request.RequestDigest || receipt.NodeKey != request.NodeKey || receipt.ProjectID != request.ProjectID || receipt.ProjectSlug != request.ProjectSlug || receipt.OperationID != request.OperationID || receipt.PlanDigest != request.PlanDigest || len(receipt.Evidence) != len(request.Targets) {
		return fmt.Errorf("quiescence receipt contradicts request")
	}
	if requireID && !digestPattern.MatchString(receipt.ReceiptID) {
		return fmt.Errorf("quiescence receipt ID is invalid")
	}
	for index, target := range request.Targets {
		evidence := receipt.Evidence[index]
		if evidence.ProjectRuntimeQuiescenceTarget != target || evidence.State != TargetStateStopped || evidence.FenceState != FenceStateActive || !boundedReceiptIdentity(evidence.TargetReceiptID) || evidence.ReceiptID != "" {
			return fmt.Errorf("quiescence receipt target evidence is not exact")
		}
		if evidence.ObservedAt.IsZero() || evidence.ObservedAt.Location() != time.UTC || (!floor.IsZero() && evidence.ObservedAt.Before(floor)) || (!now.IsZero() && evidence.ObservedAt.After(now)) {
			return fmt.Errorf("quiescence receipt observation time is invalid")
		}
	}
	return nil
}

func receiptIdentityDigest(receipt Receipt) string {
	identity := struct {
		SchemaVersion string     `json:"schema_version"`
		RequestDigest string     `json:"request_digest"`
		NodeKey       string     `json:"node_key"`
		ProjectID     string     `json:"project_id"`
		ProjectSlug   string     `json:"project_slug"`
		OperationID   string     `json:"operation_id"`
		PlanDigest    string     `json:"plan_digest"`
		Evidence      []Evidence `json:"evidence"`
	}{receipt.SchemaVersion, receipt.RequestDigest, receipt.NodeKey, receipt.ProjectID, receipt.ProjectSlug, receipt.OperationID, receipt.PlanDigest, receipt.Evidence}
	raw, _ := json.Marshal(identity)
	return digest(raw)
}

func NewFence(request Request, target Target, fencedAt time.Time) (Fence, error) {
	if err := ValidateRequest(request); err != nil {
		return Fence{}, err
	}
	fence := Fence{SchemaVersion: FenceSchemaVersion, RequestDigest: request.RequestDigest, NodeKey: request.NodeKey, ProjectID: request.ProjectID, ProjectSlug: request.ProjectSlug, OperationID: request.OperationID, PlanDigest: request.PlanDigest, Target: target, State: FenceStateActive, FencedAt: fencedAt.UTC()}
	if err := ValidateFence(request, target, fence, fencedAt.UTC()); err != nil {
		return Fence{}, err
	}
	return fence, nil
}

func ValidateFence(request Request, target Target, fence Fence, now time.Time) error {
	if fence.SchemaVersion != FenceSchemaVersion || fence.RequestDigest != request.RequestDigest || fence.NodeKey != request.NodeKey || fence.ProjectID != request.ProjectID || fence.ProjectSlug != request.ProjectSlug || fence.OperationID != request.OperationID || fence.PlanDigest != request.PlanDigest || fence.Target != target || fence.State != FenceStateActive {
		return fmt.Errorf("quiescence fence contradicts request")
	}
	if fence.FencedAt.IsZero() || fence.FencedAt.Location() != time.UTC || (!now.IsZero() && fence.FencedAt.After(now)) {
		return fmt.Errorf("quiescence fence time is invalid")
	}
	return nil
}

func DecodeRequest(raw []byte) (Request, error) {
	var request Request
	if err := decodeExact(raw, MaxRequestBytes, &request); err != nil {
		return Request{}, err
	}
	if err := ValidateRequest(request); err != nil {
		return Request{}, err
	}
	return request, nil
}

func DecodeReceipt(raw []byte, request Request, floor, now time.Time) (Receipt, error) {
	var receipt Receipt
	if err := decodeExact(raw, MaxReceiptBytes, &receipt); err != nil {
		return Receipt{}, err
	}
	if err := ValidateReceipt(request, receipt, floor, now); err != nil {
		return Receipt{}, err
	}
	canonical, _ := json.Marshal(receipt)
	if !bytes.Equal(raw, canonical) {
		return Receipt{}, fmt.Errorf("quiescence receipt bytes are not canonical")
	}
	return receipt, nil
}

func DecodeFence(raw []byte, request Request, target Target, now time.Time) (Fence, error) {
	var fence Fence
	if err := decodeExact(raw, MaxFenceBytes, &fence); err != nil {
		return Fence{}, err
	}
	if err := ValidateFence(request, target, fence, now); err != nil {
		return Fence{}, err
	}
	canonical, _ := json.Marshal(fence)
	if !bytes.Equal(raw, canonical) {
		return Fence{}, fmt.Errorf("quiescence fence bytes are not canonical")
	}
	return fence, nil
}

func CanonicalReceiptBytes(receipt Receipt) ([]byte, error) {
	return json.Marshal(receipt)
}

func CanonicalFenceBytes(fence Fence) ([]byte, error) {
	return json.Marshal(fence)
}

// ValidateAggregateReceipt is the shared compatibility validation used only
// by the already-integrated fail-closed Main coordinator. Slice 2R-B replaces
// it with routed node receipt aggregation.
func ValidateAggregateReceipt(request Request, receipt Receipt, floor time.Time) error {
	if receipt.SchemaVersion != request.SchemaVersion || receipt.ProjectID != request.ProjectID || receipt.ProjectSlug != request.ProjectSlug || receipt.OperationID != request.OperationID || receipt.PlanDigest != request.PlanDigest || len(receipt.Evidence) != len(request.Targets) {
		return fmt.Errorf("project archive runtime quiescence receipt contradicts the reviewed operation")
	}
	if !sort.SliceIsSorted(receipt.Evidence, func(i, j int) bool {
		return CanonicalTargetKey(receipt.Evidence[i].ProjectRuntimeQuiescenceTarget) < CanonicalTargetKey(receipt.Evidence[j].ProjectRuntimeQuiescenceTarget)
	}) {
		return fmt.Errorf("project archive runtime quiescence receipt is not canonical")
	}
	for index, target := range request.Targets {
		evidence := receipt.Evidence[index]
		identity := evidence.ReceiptID
		if identity == "" {
			identity = evidence.TargetReceiptID
		}
		if evidence.ProjectRuntimeQuiescenceTarget != target || evidence.State != TargetStateStopped || !boundedReceiptIdentity(identity) || evidence.ObservedAt.IsZero() || evidence.ObservedAt.Before(floor) {
			return fmt.Errorf("project archive runtime target %s lacks exact terminal quiescence evidence", aggregateTargetLabel(target))
		}
	}
	return nil
}

func aggregateTargetLabel(target Target) string {
	for _, value := range []string{target.LocalRootKey, target.ProviderKey, target.Key, target.WorkerKey, target.AllowlistKey} {
		if value != "" {
			return value
		}
	}
	return target.Kind
}

func decodeExact(raw []byte, limit int, target any) error {
	if len(raw) == 0 || len(raw) > limit {
		return fmt.Errorf("quiescence JSON is empty or exceeds its bound")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("decode quiescence JSON: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return fmt.Errorf("quiescence JSON must contain one object")
	}
	return nil
}

func boundedToken(value string) bool {
	return value == strings.TrimSpace(value) && tokenPattern.MatchString(value) && !strings.Contains(value, "..")
}

func boundedReceiptIdentity(value string) bool {
	return value == strings.TrimSpace(value) && len(value) > 0 && len(value) <= 1024 && !strings.ContainsAny(value, "\r\n\x00")
}

func digest(raw []byte) string {
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:])
}
