package nodeagent

import (
	"context"
	"encoding/json"
	"errors"

	noderuntime "loom.local/loom/internal/nodeagent/runtime"
	"loom.local/loom/internal/serviceregistry"
)

// Scheduled by the existing node supervisor only after an explicit instance is
// configured. E3 supplies exact policy-owned pools; no new poller or registry.
type applicationCapacityRuntime struct{}

func (applicationCapacityRuntime) Kind() string { return "application_capacity" }
func (applicationCapacityRuntime) RunOnce(ctx context.Context, _ noderuntime.Store, _ noderuntime.Env, instance noderuntime.WorkerInstance) (noderuntime.RunResult, error) {
	var cfg struct {
		Pools []string `json:"pools"`
	}
	if json.Unmarshal(instance.ConfigJSON, &cfg) != nil || len(cfg.Pools) == 0 || len(cfg.Pools) > 16 {
		return noderuntime.RunResult{}, errors.New("application capacity configuration invalid")
	}
	observations := []serviceregistry.ApplicationCapacityObservation{}
	health := noderuntime.WorkerStatusHealthy
	for _, pool := range cfg.Pools {
		if e := ctx.Err(); e != nil {
			return noderuntime.RunResult{}, e
		}
		o := serviceregistry.ObserveApplicationCapacity(pool)
		observations = append(observations, o)
		if o.State != "observed" {
			health = noderuntime.WorkerStatusDegraded
		}
	}
	raw, _ := json.Marshal(observations)
	return noderuntime.RunResult{Status: noderuntime.RunStatusSucceeded, HealthStatus: health, Message: "bounded capacity observations; no reservation or protection assertion", ResultJSON: raw}, nil
}
