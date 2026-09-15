package knowledge

import (
	"encoding/json"
	"fmt"
	"time"
)

type HeavyResourcePolicy struct {
	SchemaVersion  string `json:"schema_version"`
	TimeoutSeconds int    `json:"timeout_seconds"`
	MaxUnits       int    `json:"max_units"`
	MaxInputBytes  int64  `json:"max_input_bytes"`
	MaxTempBytes   int64  `json:"max_temp_bytes"`
}

func DefaultHeavyResourcePolicy() HeavyResourcePolicy {
	return HeavyResourcePolicy{SchemaVersion: "knowledge.heavy_resource_policy.v1", TimeoutSeconds: 600, MaxUnits: 200, MaxInputBytes: 128 << 20, MaxTempBytes: 512 << 20}
}

func ParseHeavyResourcePolicy(raw json.RawMessage) (HeavyResourcePolicy, error) {
	policy := DefaultHeavyResourcePolicy()
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &policy); err != nil {
			return HeavyResourcePolicy{}, fmt.Errorf("%w: invalid heavy resource policy: %v", ErrInvalid, err)
		}
	}
	if policy.TimeoutSeconds <= 0 || policy.MaxUnits <= 0 || policy.MaxInputBytes <= 0 || policy.MaxTempBytes <= 0 {
		return HeavyResourcePolicy{}, fmt.Errorf("%w: heavy resource budgets must be positive", ErrInvalid)
	}
	return policy, nil
}

func (p HeavyResourcePolicy) Timeout() time.Duration {
	return time.Duration(p.TimeoutSeconds) * time.Second
}

func (p HeavyResourcePolicy) ValidateWork(units int, inputBytes int64) error {
	if units > p.MaxUnits {
		return fmt.Errorf("%w: unit budget exceeded (%d > %d)", ErrInvalid, units, p.MaxUnits)
	}
	if inputBytes > p.MaxInputBytes {
		return fmt.Errorf("%w: input byte budget exceeded (%d > %d)", ErrInvalid, inputBytes, p.MaxInputBytes)
	}
	return nil
}
