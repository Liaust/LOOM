package knowledge

import (
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func TestHeavyResourcePolicyEnforcesUnitAndByteBudgets(t *testing.T) {
	policy := DefaultHeavyResourcePolicy()
	if err := policy.ValidateWork(policy.MaxUnits+1, 1); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unit error = %v", err)
	}
	if err := policy.ValidateWork(1, policy.MaxInputBytes+1); !errors.Is(err, ErrInvalid) {
		t.Fatalf("byte error = %v", err)
	}
}

func TestHeavyResourceSoftTurnKeepsHardTimeoutAndSlowUnit(t *testing.T) {
	p, err := ParseHeavyResourcePolicy(json.RawMessage(`{"timeout_seconds":600,"max_units":200}`))
	if err != nil || p.TurnSeconds != 30 || p.Timeout() != 600*time.Second {
		t.Fatalf("%+v %v", p, err)
	}
	start := time.Now()
	if p.TurnExpired(start, start.Add(time.Minute), 0) {
		t.Fatal("soft turn interrupted unfinished unit")
	}
	if !p.TurnExpired(start, start.Add(time.Minute), 1) {
		t.Fatal("slow completed unit did not yield")
	}
	if p.TurnExpired(start, start.Add(29*time.Second), 8) {
		t.Fatal("yielded before budget")
	}
}
