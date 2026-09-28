package nodeagent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"strings"
	"time"

	noderuntime "loom.local/loom/internal/nodeagent/runtime"
	"loom.local/loom/internal/projectquiescence"
	"loom.local/loom/internal/serviceregistry"
)

// Release removes only this archive's fences. The persisted intent makes a
// partial release retryable and prevents a delayed quiesce from fencing again.
func (s ProjectArchiveQuiescenceService) Release(ctx context.Context, request projectquiescence.Request) (projectquiescence.Receipt, error) {
	var zero projectquiescence.Receipt
	if projectquiescence.ValidateRequest(request) != nil || request.Release == nil || request.NodeKey != s.Config.NodeKey {
		return zero, quiescenceFailure("node_agent.project_archive_release.invalid_request")
	}
	store := noderuntime.NewStore(s.Store.DataDir)
	unlock, err := store.AcquireProjectArchiveOperationLock(ctx, request.OperationID)
	if err != nil {
		return zero, err
	}
	defer unlock()
	if raw, _, err := store.ReadProjectArchiveReceipt(request.OperationID + "-released"); err == nil {
		return projectquiescence.DecodeReceipt(raw, request, time.Time{}, s.now())
	} else if !errors.Is(err, fs.ErrNotExist) {
		return zero, err
	}
	original := request
	original.Release = nil
	if err := projectquiescence.SealRequest(&original); err != nil {
		return zero, err
	}
	raw, _, err := store.ReadProjectArchiveReceipt(request.OperationID)
	if err != nil {
		return zero, err
	}
	if _, err := projectquiescence.DecodeReceipt(raw, original, time.Time{}, s.now()); err != nil {
		return zero, err
	}
	intent, err := json.Marshal(request)
	if err != nil {
		return zero, err
	}
	if previous, _, err := store.ReadProjectArchiveReceipt(request.OperationID + "-release-intent"); err == nil {
		if !bytes.Equal(previous, intent) {
			return zero, quiescenceFailure("node_agent.project_archive_release.intent_conflict")
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return zero, err
	} else if err := store.PublishProjectArchiveReceipt(request.OperationID+"-release-intent", intent); err != nil {
		return zero, err
	}
	receipt := projectquiescence.Receipt{}
	for _, target := range request.Targets {
		evidence, err := s.releaseTarget(ctx, store, original, target)
		if err != nil {
			return zero, err
		}
		receipt.Evidence = append(receipt.Evidence, evidence)
	}
	if err := projectquiescence.SealReceipt(request, &receipt); err != nil {
		return zero, err
	}
	raw, err = projectquiescence.CanonicalReceiptBytes(receipt)
	if err != nil {
		return zero, err
	}
	if err := store.PublishProjectArchiveReceipt(request.OperationID+"-released", raw); err != nil {
		return zero, err
	}
	return receipt, nil
}

func (s ProjectArchiveQuiescenceService) releaseTarget(ctx context.Context, store noderuntime.Store, original projectquiescence.Request, target projectquiescence.Target) (projectquiescence.Evidence, error) {
	var zero projectquiescence.Evidence
	unlock, err := s.acquireTargetLock(ctx, store, target)
	if err != nil {
		return zero, err
	}
	defer unlock()
	key, _ := projectquiescence.TargetLockIdentity(target)
	key = strings.TrimPrefix(key, target.Kind+":")
	raw, _, err := store.ReadProjectArchiveFence(target.Kind, key)
	exists := err == nil
	if exists {
		if _, err := projectquiescence.DecodeFence(raw, original, target, s.now()); err != nil {
			return zero, err
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return zero, err
	}
	identity, err := s.observeTarget(ctx, store, target)
	if err != nil {
		return zero, err
	}
	if serviceregistry.IsApplicationUnitBinding(target.AllowlistKey, target.Unit) {
		manager, err := s.exactServiceManager(target)
		if err != nil {
			return zero, err
		}
		result, err := manager.Execute(ctx, serviceregistry.ManagerRequest{AllowlistKey: target.AllowlistKey, Operation: serviceregistry.OperationArchiveRelease})
		if err != nil {
			return zero, err
		}
		if !result.Success || result.ProcessState != serviceregistry.ProcessStateStopped {
			return zero, quiescenceFailure("node_agent.project_archive_release.application_not_stopped")
		}
	}
	if exists {
		if err := store.RemoveProjectArchiveFence(target.Kind, key, raw); err != nil {
			return zero, err
		}
	}
	if err := s.fail("after_release", &target); err != nil {
		return zero, err
	}
	return projectquiescence.Evidence{ProjectRuntimeQuiescenceTarget: target, State: projectquiescence.TargetStateStopped, FenceState: projectquiescence.FenceStateReleased, TargetReceiptID: identity, ObservedAt: s.now()}, nil
}

func projectArchiveReleaseInputSchema() json.RawMessage {
	var schema map[string]any
	_ = json.Unmarshal(projectArchiveQuiescenceInputSchema(), &schema)
	schema["properties"].(map[string]any)["release"] = map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{"operation_id": map[string]any{"type": "string", "maxLength": 256}, "plan_digest": map[string]any{"type": "string", "pattern": "^sha256:[0-9a-f]{64}$"}},
		"required":   []string{"operation_id", "plan_digest"},
	}
	schema["required"] = append(schema["required"].([]any), "release")
	raw, _ := json.Marshal(schema)
	return raw
}

func projectArchiveReleaseOutputSchema() json.RawMessage {
	var schema map[string]any
	_ = json.Unmarshal(projectArchiveQuiescenceOutputSchema(), &schema)
	items := schema["properties"].(map[string]any)["evidence"].(map[string]any)["items"].(map[string]any)["oneOf"].([]any)
	for _, item := range items {
		item.(map[string]any)["properties"].(map[string]any)["fence_state"] = map[string]any{"const": projectquiescence.FenceStateReleased}
	}
	raw, _ := json.Marshal(schema)
	return raw
}
