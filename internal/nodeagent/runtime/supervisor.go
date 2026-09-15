package runtime

import (
	"context"
	"fmt"
	"io"
	"sync"
	"time"
)

type EnvProvider func() (Env, error)

type Supervisor struct {
	Store         Store
	Registry      Registry
	EnvProvider   EnvProvider
	WorkerKeys    []string
	CorrelationID string
	ErrOut        io.Writer
}

func (s Supervisor) Run(ctx context.Context) error {
	if err := s.Store.Ensure(); err != nil {
		return err
	}
	instances, err := s.instances()
	if err != nil {
		return err
	}
	var wg sync.WaitGroup
	for _, instance := range instances {
		if !instance.Enabled {
			continue
		}
		instance := instance
		s.runWorker(ctx, instance)
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.workerLoop(ctx, instance)
		}()
	}
	<-ctx.Done()
	wg.Wait()
	return nil
}

func (s Supervisor) instances() ([]WorkerInstance, error) {
	if len(s.WorkerKeys) == 0 {
		return s.Store.LoadInstances()
	}
	instances := make([]WorkerInstance, 0, len(s.WorkerKeys))
	for _, workerKey := range s.WorkerKeys {
		instance, err := s.Store.LoadInstance(workerKey)
		if err != nil {
			return nil, err
		}
		instances = append(instances, instance)
	}
	return instances, nil
}

func (s Supervisor) workerLoop(ctx context.Context, instance WorkerInstance) {
	interval := time.Duration(instance.IntervalSeconds) * time.Second
	if interval <= 0 {
		interval = 30 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.runWorker(ctx, instance)
		}
	}
}

func (s Supervisor) runWorker(ctx context.Context, instance WorkerInstance) {
	env, err := s.EnvProvider()
	if err != nil {
		s.logf("%s env failed: %v\n", instance.WorkerKey, err)
		return
	}
	if _, err := s.Registry.RunOnce(ctx, s.Store, env, instance.WorkerKey, s.CorrelationID); err != nil {
		s.logf("%s failed: %v\n", instance.WorkerKey, err)
	}
}

func (s Supervisor) logf(format string, args ...any) {
	if s.ErrOut == nil {
		return
	}
	_, _ = fmt.Fprintf(s.ErrOut, format, args...)
}
