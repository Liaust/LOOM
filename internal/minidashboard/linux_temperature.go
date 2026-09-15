package minidashboard

import (
	"fmt"
	"math"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

type temperatureCandidate struct {
	path     string
	priority int
	value    float64
}

func parseTemperature(data []byte) (float64, error) {
	value, err := strconv.ParseFloat(strings.TrimSpace(string(data)), 64)
	if err != nil {
		return 0, fmt.Errorf("parse temperature: %w", err)
	}
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return 0, fmt.Errorf("temperature is not finite")
	}
	if value > 1000 {
		value /= 1000
	}
	if value < -20 || value > 150 {
		return 0, fmt.Errorf("temperature is outside the sensor range")
	}
	return value, nil
}

func selectTemperature(readFile func(string) ([]byte, error), glob func(string) ([]string, error), sysRoot string) (float64, error) {
	hwmonDirs, _ := glob(filepath.Join(sysRoot, "class", "hwmon", "hwmon*"))
	sort.Strings(hwmonDirs)
	labelled := make([]temperatureCandidate, 0)
	unlabelledCPU := make([]temperatureCandidate, 0)
	for _, directory := range hwmonDirs {
		name := readTrimmed(readFile, filepath.Join(directory, "name"))
		inputs, _ := glob(filepath.Join(directory, "temp*_input"))
		sort.Strings(inputs)
		for _, input := range inputs {
			labelPath := strings.TrimSuffix(input, "_input") + "_label"
			label := readTrimmed(readFile, labelPath)
			priority := temperaturePriority(name, label)
			if priority <= 0 {
				continue
			}
			data, err := readFile(input)
			if err != nil {
				continue
			}
			value, err := parseTemperature(data)
			if err != nil {
				continue
			}
			candidate := temperatureCandidate{path: input, priority: priority, value: value}
			if label != "" {
				labelled = append(labelled, candidate)
			} else {
				unlabelledCPU = append(unlabelledCPU, candidate)
			}
		}
	}
	if candidate, ok := preferredTemperature(labelled); ok {
		return candidate.value, nil
	}
	if candidate, ok := preferredTemperature(unlabelledCPU); ok {
		return candidate.value, nil
	}
	thermalPaths, _ := glob(filepath.Join(sysRoot, "class", "thermal", "thermal_zone*", "temp"))
	sort.Strings(thermalPaths)
	for _, path := range thermalPaths {
		data, err := readFile(path)
		if err != nil {
			continue
		}
		if value, err := parseTemperature(data); err == nil {
			return value, nil
		}
	}
	return 0, fmt.Errorf("CPU temperature is unavailable")
}

func temperaturePriority(device, label string) int {
	device = strings.ToLower(strings.TrimSpace(device))
	label = strings.ToLower(strings.TrimSpace(label))
	cpuDevice := strings.Contains(device, "k10temp") || strings.Contains(device, "zenpower") || strings.Contains(device, "coretemp") || strings.Contains(device, "cpu")
	if strings.Contains(device, "nvme") || strings.Contains(device, "pch") || strings.Contains(device, "chipset") {
		return 0
	}
	priority := 0
	if cpuDevice {
		priority = 100
	}
	switch {
	case label == "tctl" || strings.Contains(label, "package"):
		priority += 60
	case label == "tdie" || strings.Contains(label, "cpu"):
		priority += 50
	case strings.HasPrefix(label, "core") || strings.HasPrefix(label, "tccd"):
		priority += 30
	case label != "" && !cpuDevice:
		return 0
	}
	return priority
}

func preferredTemperature(candidates []temperatureCandidate) (temperatureCandidate, bool) {
	if len(candidates) == 0 {
		return temperatureCandidate{}, false
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].priority != candidates[j].priority {
			return candidates[i].priority > candidates[j].priority
		}
		return candidates[i].path < candidates[j].path
	})
	return candidates[0], true
}

func readTrimmed(readFile func(string) ([]byte, error), path string) string {
	data, err := readFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}
