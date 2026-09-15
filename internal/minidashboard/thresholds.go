package minidashboard

import (
	"sync"
	"time"
)

type thresholdState struct {
	warningSince  time.Time
	criticalSince time.Time
	warningCount  int
	criticalCount int
}

// ThresholdEvaluator stabilizes noisy readings independently for warning and
// critical levels. It is safe for a collector and HTTP reader to share.
type ThresholdEvaluator struct {
	mu     sync.Mutex
	states map[string]thresholdState
}

func (e *ThresholdEvaluator) Evaluate(key string, value float64, threshold Threshold, now time.Time) Severity {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.states == nil {
		e.states = map[string]thresholdState{}
	}
	state := e.states[key]
	if value < threshold.Warning {
		delete(e.states, key)
		return SeverityHealthy
	}
	state.warningCount++
	if state.warningSince.IsZero() {
		state.warningSince = now
	}
	if value >= threshold.Critical {
		state.criticalCount++
		if state.criticalSince.IsZero() {
			state.criticalSince = now
		}
	} else {
		state.criticalCount = 0
		state.criticalSince = time.Time{}
	}
	e.states[key] = state
	if value >= threshold.Critical && stabilized(now, state.criticalSince, state.criticalCount, threshold.CriticalFor, threshold.CriticalSamples) {
		return SeverityCritical
	}
	if stabilized(now, state.warningSince, state.warningCount, threshold.WarningFor, threshold.WarningSamples) {
		return SeverityWarning
	}
	return SeverityHealthy
}

func stabilized(now, since time.Time, samples int, duration time.Duration, requiredSamples int) bool {
	durationReady := duration == 0 || (!since.IsZero() && now.Sub(since) >= duration)
	samplesReady := requiredSamples == 0 || samples >= requiredSamples
	return durationReady && samplesReady
}
