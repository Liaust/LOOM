package knowledge

import (
	"errors"
	"testing"
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
