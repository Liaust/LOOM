package runtimes

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"loom.local/loom/internal/jobs"
	"loom.local/loom/internal/projectapply"
	"loom.local/loom/internal/workers"
)

type ApplicationBuilderRuntime struct {
	Coordinator *projectapply.ApplicationBuildCoordinator
}

func (r ApplicationBuilderRuntime) Kind() string { return workers.KindApplicationBuilder }
func (r ApplicationBuilderRuntime) Describe() workers.KindDescriptor {
	d := NewJobRunnerRuntime(jobs.Service{}, "").Describe()
	d.WorkerKind = r.Kind()
	d.DisplayName = "Application builder"
	d.Description = "Builds accepted frozen project source and resumes its original declaration operation."
	d.RuntimePackage = "loom.core.applications"
	d.ConfigSchemaJSON = json.RawMessage(`{"schema_version":"application_builder.config.v1","type":"object"}`)
	return d
}
func (r ApplicationBuilderRuntime) DefaultConfig() json.RawMessage { return json.RawMessage(`{}`) }
func (r ApplicationBuilderRuntime) DefaultInstances() []workers.InstanceDescriptor {
	d := NewJobRunnerRuntime(jobs.Service{}, "").DefaultInstances()[0]
	d.WorkerKey = "main.application_builder"
	d.WorkerKind = r.Kind()
	d.DisplayName = "Application builder"
	d.Description = "Event-driven native application builds; no scan or build at idle."
	d.ConfigJSON = r.DefaultConfig()
	return []workers.InstanceDescriptor{d}
}
func (r ApplicationBuilderRuntime) ValidateConfig(_ context.Context, raw json.RawMessage) error {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil || len(fields) != 0 {
		return fmt.Errorf("application builder has no project-controlled worker options")
	}
	return nil
}
func (r ApplicationBuilderRuntime) AutomaticReady(ctx context.Context, i workers.WorkerInstance, now time.Time) (bool, *time.Time, error) {
	if r.Coordinator == nil {
		return false, nil, nil
	}
	node := i.HostNodeID
	if node == "" {
		node = i.OwnerNodeID
	}
	next, err := r.Coordinator.Next(ctx, node, now)
	return err == nil && next != nil && !next.After(now), next, err
}
func (r ApplicationBuilderRuntime) RunOnce(ctx context.Context, run workers.RunContext) (workers.RunResult, error) {
	if r.Coordinator == nil {
		return workers.RunResult{}, fmt.Errorf("application builder unavailable")
	}
	worked, err := r.Coordinator.RunOne(ctx, run.Request, run.Run.WorkerRunID)
	node := run.Instance.HostNodeID
	if node == "" {
		node = run.Instance.OwnerNodeID
	}
	next, e := r.Coordinator.Next(ctx, node, time.Now().UTC())
	if err == nil {
		err = e
	}
	raw, _ := json.Marshal(map[string]any{"schema_version": "application_builder.result.v1", "worked": worked})
	return workers.RunResult{Status: workers.RunStatusSucceeded, ResultSummary: raw, NextRunAfter: next}, err
}
