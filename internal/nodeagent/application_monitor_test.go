package nodeagent

import (
	"context"
	"encoding/json"
	noderuntime "loom.local/loom/internal/nodeagent/runtime"
	"testing"
)

func TestApplicationCapacityMonitorDistinguishesUnknown(t *testing.T) {
	raw, _ := json.Marshal(map[string]any{"pools": []string{t.TempDir(), "/missing-loom-fixture-pool"}})
	result, e := (applicationCapacityRuntime{}).RunOnce(context.Background(), noderuntime.Store{}, noderuntime.Env{}, noderuntime.WorkerInstance{ConfigJSON: raw})
	if e != nil || result.HealthStatus != noderuntime.WorkerStatusDegraded {
		t.Fatalf("missing pool must be unknown: %+v %v", result, e)
	}
}
