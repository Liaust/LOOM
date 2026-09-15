package projectquiescence

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestProjectArchiveQuiescenceRequestCanonicalDigestAndVariants(t *testing.T) {
	request := testRequest(t)
	if request.Targets[0].Kind != TargetKindService || request.Targets[1].Kind != TargetKindWatchedRoot {
		t.Fatalf("targets are not canonically sorted: %#v", request.Targets)
	}
	first := request.RequestDigest
	if err := SealRequest(&request); err != nil {
		t.Fatal(err)
	}
	if request.RequestDigest != first {
		t.Fatalf("request digest changed across exact reseal: %s != %s", request.RequestDigest, first)
	}
	changed := request
	changed.Targets = append([]Target(nil), request.Targets...)
	changed.Targets[1].ConfigHash = testDigest("f")
	if err := ValidateRequest(changed); err == nil {
		t.Fatal("changed target retained accepted request digest")
	}

	for _, mutate := range []func(*Target){
		func(target *Target) { target.OwnerNode = "other" },
		func(target *Target) { target.Manager = "shell" },
		func(target *Target) { target.WorkerKey = "worker_wrong" },
	} {
		invalid := request
		invalid.Targets = append([]Target(nil), request.Targets...)
		mutate(&invalid.Targets[0])
		if err := SealRequest(&invalid); err == nil {
			t.Fatal("invalid or substituted target was sealed")
		}
	}
	substituted := request
	substituted.Targets = append([]Target(nil), request.Targets...)
	substituted.Targets[0].ProviderID = "provider_wrong"
	if err := SealRequest(&substituted); err != nil {
		t.Fatal(err)
	}
	if substituted.RequestDigest == request.RequestDigest {
		t.Fatal("provider substitution did not change request digest")
	}
}

func TestProjectArchiveQuiescenceReceiptIdentityTimesAndCanonicalBytes(t *testing.T) {
	request := testRequest(t)
	floor := time.Date(2026, 9, 4, 10, 0, 0, 0, time.UTC)
	now := floor.Add(time.Minute)
	receipt := Receipt{Evidence: []Evidence{
		{ProjectRuntimeQuiescenceTarget: request.Targets[0], State: TargetStateStopped, FenceState: FenceStateActive, TargetReceiptID: "service:service-one:systemd:loom-one.service", ObservedAt: now},
		{ProjectRuntimeQuiescenceTarget: request.Targets[1], State: TargetStateStopped, FenceState: FenceStateActive, TargetReceiptID: "worker:node-agent.watched_root.backend:sha256:exact", ObservedAt: now},
	}}
	if err := SealReceipt(request, &receipt); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(receipt.ReceiptID, "sha256:") {
		t.Fatalf("receipt ID = %q", receipt.ReceiptID)
	}
	raw, err := CanonicalReceiptBytes(receipt)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeReceipt(raw, request, floor, now)
	if err != nil || decoded.ReceiptID != receipt.ReceiptID {
		t.Fatalf("exact receipt decode = %#v, %v", decoded, err)
	}

	for _, mutate := range []func(*Receipt){
		func(value *Receipt) { value.Evidence[0].ObservedAt = floor.Add(-time.Nanosecond) },
		func(value *Receipt) { value.Evidence[0].ObservedAt = now.Add(time.Nanosecond) },
		func(value *Receipt) { value.Evidence[0].State = "running" },
		func(value *Receipt) { value.Evidence[0].FenceState = "missing" },
		func(value *Receipt) { value.RequestDigest = testDigest("e") },
		func(value *Receipt) { value.ReceiptID = testDigest("d") },
	} {
		changed := receipt
		changed.Evidence = append([]Evidence(nil), receipt.Evidence...)
		mutate(&changed)
		payload, _ := json.Marshal(changed)
		if _, err := DecodeReceipt(payload, request, floor, now); err == nil {
			t.Fatal("tampered receipt was accepted")
		}
	}
	pretty, _ := json.MarshalIndent(receipt, "", "  ")
	if _, err := DecodeReceipt(pretty, request, floor, now); err == nil {
		t.Fatal("non-canonical receipt bytes were accepted")
	}
}

func TestProjectArchiveQuiescenceFenceExactBinding(t *testing.T) {
	request := testRequest(t)
	now := time.Date(2026, 9, 4, 10, 1, 0, 0, time.UTC)
	fence, err := NewFence(request, request.Targets[0], now)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := CanonicalFenceBytes(fence)
	if _, err := DecodeFence(raw, request, request.Targets[0], now); err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeFence(raw, request, request.Targets[1], now); err == nil {
		t.Fatal("fence was accepted for another target")
	}
}

func testRequest(t *testing.T) Request {
	t.Helper()
	request := Request{
		ProjectID: "project_test", ProjectSlug: "test", OperationID: "archive_test",
		PlanDigest: testDigest("a"), NodeKey: "node-a",
		Targets: []Target{
			{Facet: FacetWatchedRoots, Kind: TargetKindWatchedRoot, LocalRootKey: "project", BackendRootRef: "backend", OwnerNode: "node-a", WorkerKey: "node-agent.watched_root.backend", ConfigHash: testDigest("b")},
			{Facet: FacetServices, Kind: TargetKindService, ProviderKey: "service-one", ProviderAddress: "node-a@service-one", OwnerNode: "node-a", ProviderID: "provider_one", RuntimeProfileDigest: testDigest("c"), AllowlistKey: "service-one", Manager: "systemd", Unit: "loom-one.service"},
		},
	}
	if err := SealRequest(&request); err != nil {
		t.Fatal(err)
	}
	return request
}

func testDigest(value string) string {
	return "sha256:" + strings.Repeat(value, 64)
}
