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
	return s.run(ctx, 5*time.Second)
}

func (s Supervisor) run(ctx context.Context, refreshInterval time.Duration) error {
	if err := s.Store.Ensure(); err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	type schedule struct {
		started time.Time
		done    chan struct{}
	}
	schedules := map[string]*schedule{}
	var wg sync.WaitGroup
	defer func() {
		cancel()
		wg.Wait()
	}()
	ticker := time.NewTicker(refreshInterval)
	defer ticker.Stop()
	for ctx.Err() == nil {
		instances, err := s.instances()
		if err != nil {
			return err
		}
		current := map[string]bool{}
		for _, instance := range instances {
			current[instance.WorkerKey] = instance.Enabled
			if !instance.Enabled {
				continue
			}
			state := schedules[instance.WorkerKey]
			if state == nil {
				state = &schedule{}
				schedules[instance.WorkerKey] = state
			}
			if state.done != nil {
				select {
				case <-state.done:
					state.done = nil
				default:
					continue
				}
			}
			interval := time.Duration(instance.IntervalSeconds) * time.Second
			if interval <= 0 {
				interval = 30 * time.Second
			}
			if time.Since(state.started) < interval {
				continue
			}
			state.started, state.done = time.Now(), make(chan struct{})
			wg.Add(1)
			go func(instance WorkerInstance, done chan struct{}) {
				defer wg.Done()
				defer close(done)
				s.runWorker(ctx, instance)
			}(instance, state.done)
		}
		// Keep an in-flight identity until it finishes, even if temporarily disabled.
		for key, state := range schedules {
			if current[key] {
				continue
			}
			if state.done != nil {
				select {
				case <-state.done:
				default:
					continue
				}
			}
			delete(schedules, key)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
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
