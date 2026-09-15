package minidashboard

import (
	"fmt"
	"strconv"
	"strings"
)

type cpuCounters struct {
	busy  uint64
	total uint64
}

func parseProcStat(data []byte) (cpuCounters, error) {
	line := strings.SplitN(string(data), "\n", 2)[0]
	fields := strings.Fields(line)
	if len(fields) < 5 || fields[0] != "cpu" {
		return cpuCounters{}, fmt.Errorf("aggregate cpu counters are unavailable")
	}
	values := make([]uint64, len(fields)-1)
	for i, field := range fields[1:] {
		value, err := strconv.ParseUint(field, 10, 64)
		if err != nil {
			return cpuCounters{}, fmt.Errorf("parse cpu counter: %w", err)
		}
		values[i] = value
	}
	var total uint64
	for _, value := range values {
		total += value
	}
	idle := values[3]
	if len(values) > 4 {
		idle += values[4]
	}
	return cpuCounters{busy: total - idle, total: total}, nil
}

func cpuUtilization(previous, current cpuCounters) (float64, bool) {
	if current.total <= previous.total || current.busy < previous.busy {
		return 0, false
	}
	totalDelta := current.total - previous.total
	busyDelta := current.busy - previous.busy
	if busyDelta > totalDelta {
		return 0, false
	}
	return float64(busyDelta) * 100 / float64(totalDelta), true
}
