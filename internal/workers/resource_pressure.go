package workers

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Missing values are unknown, never zero. Only a ready owner samples these.
type ResourcePressureSnapshot struct {
	SampledAt            time.Time `json:"sampled_at"`
	CPUSomeAvg10         *float64  `json:"cpu_some_avg10,omitempty"`
	MemoryFullAvg10      *float64  `json:"memory_full_avg10,omitempty"`
	IOFullAvg10          *float64  `json:"io_full_avg10,omitempty"`
	AvailableMemoryBytes *int64    `json:"available_memory_bytes,omitempty"`
	AvailableDiskBytes   *int64    `json:"available_disk_bytes,omitempty"`
	DiskPath             string    `json:"disk_path,omitempty"`
	UnknownMetrics       []string  `json:"unknown_metrics,omitempty"`
}

type ResourcePressurePolicy struct {
	CPUHigh             float64 `json:"cpu_high"`
	CPULow              float64 `json:"cpu_low"`
	MemoryHigh          float64 `json:"memory_high"`
	MemoryLow           float64 `json:"memory_low"`
	IOHigh              float64 `json:"io_high"`
	IOLow               float64 `json:"io_low"`
	SustainedSeconds    int     `json:"sustained_seconds"`
	ReconsiderSeconds   int     `json:"reconsider_seconds"`
	MemoryHeadroomBytes int64   `json:"memory_headroom_bytes"`
	DiskHeadroomBytes   int64   `json:"disk_headroom_bytes"`
}

type ResourcePressureState struct {
	SampledAt         *time.Time `json:"sampled_at,omitempty"`
	Constrained       bool       `json:"constrained"`
	HighSince         *time.Time `json:"high_since,omitempty"`
	LowSince          *time.Time `json:"low_since,omitempty"`
	OptionalNotBefore *time.Time `json:"optional_not_before,omitempty"`
	BlockedUntil      *time.Time `json:"blocked_until,omitempty"`
}

type ResourceTurnAdmission struct {
	Admitted          bool                     `json:"admitted"`
	AllowOptional     bool                     `json:"allow_optional"`
	Reason            string                   `json:"reason"`
	SampledAt         time.Time                `json:"sampled_at"`
	BudgetMaxUnits    int                      `json:"budget_max_units"`
	NextEligibleAt    *time.Time               `json:"next_eligible_at,omitempty"`
	NeededMemoryBytes int64                    `json:"needed_memory_bytes"`
	NeededDiskBytes   int64                    `json:"needed_disk_bytes"`
	Snapshot          ResourcePressureSnapshot `json:"snapshot"`
}

func defaultResourcePressurePolicy() ResourcePressurePolicy {
	// Starting thresholds are persisted in resource policy and calibrated by the
	// operator on Main. They are PSI stall percentages, not core/load usage.
	return ResourcePressurePolicy{CPUHigh: 40, CPULow: 20, MemoryHigh: 10, MemoryLow: 2, IOHigh: 20, IOLow: 5, SustainedSeconds: 10, ReconsiderSeconds: 30, MemoryHeadroomBytes: 256 << 20}
}

func parseResourcePressurePolicy(raw json.RawMessage) (ResourcePressurePolicy, ResourcePressureState, error) {
	value := struct {
		Policy ResourcePressurePolicy `json:"pressure_policy"`
		State  ResourcePressureState  `json:"pressure_state"`
	}{Policy: defaultResourcePressurePolicy()}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &value); err != nil {
			return value.Policy, value.State, err
		}
	}
	p := value.Policy
	if p.CPUHigh <= p.CPULow || p.MemoryHigh <= p.MemoryLow || p.IOHigh <= p.IOLow || p.CPULow < 0 || p.MemoryLow < 0 || p.IOLow < 0 || p.CPUHigh > 100 || p.MemoryHigh > 100 || p.IOHigh > 100 || p.SustainedSeconds <= 0 || p.ReconsiderSeconds <= 0 || p.ReconsiderSeconds > 30 || p.MemoryHeadroomBytes < 0 || p.DiskHeadroomBytes < 0 {
		return p, value.State, fmt.Errorf("%w: invalid resource pressure policy", ErrInvalid)
	}
	return p, value.State, nil
}

// EvaluateResourcePressure is pure; durable state remains with the existing
// resource owner. Soft pressure cannot remove optional work permanently.
func EvaluateResourcePressure(snapshot ResourcePressureSnapshot, p ResourcePressurePolicy, state ResourcePressureState, maxUnits int, maxTempBytes int64) (ResourceTurnAdmission, ResourcePressureState) {
	now := snapshot.SampledAt.UTC()
	if state.SampledAt != nil && now.Sub(*state.SampledAt) > 2*time.Duration(p.ReconsiderSeconds)*time.Second {
		state.HighSince, state.LowSince = nil, nil
	}
	state.SampledAt = &now
	retry := now.Add(time.Duration(p.ReconsiderSeconds) * time.Second)
	disk := maxTempBytes
	if p.DiskHeadroomBytes > disk {
		disk = p.DiskHeadroomBytes
	}
	a := ResourceTurnAdmission{Admitted: true, AllowOptional: true, Reason: "headroom_available", SampledAt: now, BudgetMaxUnits: maxUnits, NeededMemoryBytes: p.MemoryHeadroomBytes, NeededDiskBytes: disk, Snapshot: snapshot}
	state.BlockedUntil = nil
	if snapshot.AvailableMemoryBytes != nil && *snapshot.AvailableMemoryBytes < p.MemoryHeadroomBytes {
		a.Admitted, a.AllowOptional, a.Reason, a.NextEligibleAt = false, false, "memory_headroom", &retry
		state.BlockedUntil = &retry
		return a, state
	}
	if snapshot.AvailableDiskBytes != nil && *snapshot.AvailableDiskBytes < disk {
		a.Admitted, a.AllowOptional, a.Reason, a.NextEligibleAt = false, false, "disk_headroom", &retry
		state.BlockedUntil = &retry
		return a, state
	}
	if snapshot.CPUSomeAvg10 == nil || snapshot.MemoryFullAvg10 == nil || snapshot.IOFullAvg10 == nil || snapshot.AvailableMemoryBytes == nil || snapshot.AvailableDiskBytes == nil {
		// The existing single-slot, bounded turn/batch policy is the fallback.
		a.Reason = "pressure_metrics_unknown"
		state.Constrained, state.HighSince, state.LowSince, state.OptionalNotBefore = false, nil, nil, nil
		return a, state
	}
	high := *snapshot.CPUSomeAvg10 >= p.CPUHigh || *snapshot.MemoryFullAvg10 >= p.MemoryHigh || *snapshot.IOFullAvg10 >= p.IOHigh
	low := *snapshot.CPUSomeAvg10 <= p.CPULow && *snapshot.MemoryFullAvg10 <= p.MemoryLow && *snapshot.IOFullAvg10 <= p.IOLow
	if high {
		state.LowSince = nil
		if state.HighSince == nil {
			state.HighSince = &now
		}
		if now.Sub(*state.HighSince) >= time.Duration(p.SustainedSeconds)*time.Second {
			state.Constrained = true
		}
	} else if low {
		state.HighSince = nil
		if state.Constrained {
			if state.LowSince == nil {
				state.LowSince = &now
			}
			if now.Sub(*state.LowSince) >= time.Duration(p.SustainedSeconds)*time.Second {
				state.Constrained = false
				state.OptionalNotBefore = nil
				state.LowSince = nil
			}
		}
	} else {
		state.LowSince = nil
		if !state.Constrained {
			state.HighSince = nil
		}
	}
	if state.Constrained {
		a.Reason = "sustained_pressure"
		if a.BudgetMaxUnits > 8 {
			a.BudgetMaxUnits = 8
		}
		if state.OptionalNotBefore == nil {
			state.OptionalNotBefore = &retry
		}
		a.AllowOptional = !now.Before(*state.OptionalNotBefore)
		if !a.AllowOptional {
			a.NextEligibleAt = state.OptionalNotBefore
		}
	}
	return a, state
}
func parsePSIAvg10(data, kind string) *float64 {
	for _, line := range strings.Split(data, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 || fields[0] != kind {
			continue
		}
		for _, field := range fields[1:] {
			key, raw, ok := strings.Cut(field, "=")
			if !ok || key != "avg10" {
				continue
			}
			value, err := strconv.ParseFloat(raw, 64)
			if err == nil && value >= 0 && value <= 100 {
				return &value
			}
		}
	}
	return nil
}

func parseAvailableMemory(data string) *int64 {
	for _, line := range strings.Split(data, "\n") {
		f := strings.Fields(line)
		if len(f) != 3 || f[0] != "MemAvailable:" || f[2] != "kB" {
			continue
		}
		value, err := strconv.ParseInt(f[1], 10, 64)
		if err == nil && value >= 0 && value <= ((1<<63)-1)/1024 {
			bytes := value * 1024
			return &bytes
		}
	}
	return nil
}
