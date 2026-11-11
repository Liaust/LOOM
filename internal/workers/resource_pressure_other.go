//go:build !linux

package workers

import "time"

func SampleResourcePressure(now time.Time) ResourcePressureSnapshot {
	return ResourcePressureSnapshot{SampledAt: now.UTC(), UnknownMetrics: []string{"cpu_pressure", "memory_pressure", "io_pressure", "available_memory", "available_disk"}}
}
