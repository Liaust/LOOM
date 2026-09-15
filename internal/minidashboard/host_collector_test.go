package minidashboard

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestHostCollectorCPURequiresTwoMonotonicSamples(t *testing.T) {
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	samples := []string{"cpu 100 0 100 800 0 0 0 0\n", "cpu 150 0 150 900 0 0 0 0\n"}
	collector := NewHostCollector("/data")
	collector.Now = func() time.Time { return now }
	collector.ReadFile = func(path string) ([]byte, error) {
		if strings.HasSuffix(path, "/stat") {
			value := samples[0]
			samples = samples[1:]
			return []byte(value), nil
		}
		if strings.HasSuffix(path, "/meminfo") {
			return []byte("MemTotal: 1000 kB\nMemAvailable: 500 kB\n"), nil
		}
		return nil, errors.New("missing")
	}
	collector.Glob = func(string) ([]string, error) { return nil, nil }
	collector.FilesystemUsage = func(string) (float64, error) { return 40, nil }
	first := collector.Collect(DefaultConfig())
	if first.CPU.Available || first.CPU.SourceState != SourceUnavailable {
		t.Fatalf("first CPU sample = %+v", first.CPU)
	}
	second := collector.Collect(DefaultConfig())
	if !second.CPU.Available || second.CPU.Value == nil || *second.CPU.Value != 50 {
		t.Fatalf("second CPU sample = %+v", second.CPU)
	}
}

func TestTemperatureSelectionFixtures(t *testing.T) {
	tests := []struct {
		name string
		want float64
	}{
		{name: "amd", want: 78},
		{name: "unrelated", want: 61},
		{name: "invalid", want: 64},
		{name: "thermal", want: 57},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := filepath.Join("testdata", "host", "temperature", tt.name, "sys")
			got, err := selectTemperature(os.ReadFile, filepath.Glob, root)
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Fatalf("temperature = %.1f, want %.1f", got, tt.want)
			}
		})
	}
}

func TestTemperatureSelectionPrefersCPUAndIsDeterministic(t *testing.T) {
	root := filepath.Join("testdata", "host", "temperature", "amd", "sys")
	for i := 0; i < 5; i++ {
		got, err := selectTemperature(os.ReadFile, filepath.Glob, root)
		if err != nil {
			t.Fatal(err)
		}
		if got != 78 {
			t.Fatalf("run %d selected %.1f instead of AMD Tctl", i, got)
		}
	}
}

func TestHostCollectorMemoryTemperatureAndFilesystem(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Thresholds.Memory.WarningSamples = 1
	cfg.Thresholds.Temperature.WarningSamples = 1
	collector := NewHostCollector("/bounded/data")
	collector.ReadFile = func(path string) ([]byte, error) {
		switch {
		case strings.HasSuffix(path, "/meminfo"):
			return []byte("MemTotal: 1000 kB\nMemAvailable: 100 kB\n"), nil
		case strings.HasSuffix(path, "/name"):
			return []byte("k10temp\n"), nil
		case strings.HasSuffix(path, "/temp1_label"):
			return []byte("Tctl\n"), nil
		case strings.Contains(path, "temp1_input"):
			return []byte("76000\n"), nil
		default:
			return nil, errors.New("missing")
		}
	}
	collector.Glob = func(pattern string) ([]string, error) {
		if strings.HasSuffix(pattern, "hwmon*") {
			return []string{"/sys/class/hwmon/hwmon0"}, nil
		}
		if strings.HasSuffix(pattern, "temp*_input") {
			return []string{"/sys/class/hwmon/hwmon0/temp1_input"}, nil
		}
		return nil, nil
	}
	seenPath := ""
	collector.FilesystemUsage = func(path string) (float64, error) { seenPath = path; return 91, nil }
	snapshot := collector.Collect(cfg)
	if snapshot.Memory.Severity != SeverityWarning || snapshot.Temperature.Severity != SeverityWarning || snapshot.Storage.Severity != SeverityCritical {
		t.Fatalf("unexpected severities: %+v", snapshot)
	}
	if seenPath != "/bounded/data" {
		t.Fatalf("filesystem path = %q", seenPath)
	}
}

func TestTemperatureUnavailableIsInformationalUnlessRequired(t *testing.T) {
	collector := NewHostCollector("/data")
	collector.ReadFile = func(string) ([]byte, error) { return nil, errors.New("missing") }
	collector.Glob = func(string) ([]string, error) { return nil, nil }
	collector.FilesystemUsage = func(string) (float64, error) { return 0, errors.New("missing") }
	cfg := DefaultConfig()
	if got := collector.Collect(cfg).Temperature; got.Available || got.Severity != SeverityUnknown {
		t.Fatalf("optional temperature = %+v", got)
	}
	cfg.TemperatureRequired = true
	if got := collector.Collect(cfg).Temperature; got.Severity != SeverityWarning {
		t.Fatalf("required temperature = %+v", got)
	}
}

func TestThresholdEvaluatorUsesIndependentStabilization(t *testing.T) {
	var evaluator ThresholdEvaluator
	threshold := Threshold{Warning: 85, Critical: 95, WarningFor: 5 * time.Minute, CriticalFor: 2 * time.Minute}
	start := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	if got := evaluator.Evaluate("cpu", 96, threshold, start); got != SeverityHealthy {
		t.Fatalf("initial = %s", got)
	}
	if got := evaluator.Evaluate("cpu", 96, threshold, start.Add(2*time.Minute)); got != SeverityCritical {
		t.Fatalf("critical at 2m = %s", got)
	}
	evaluator.Evaluate("cpu", 80, threshold, start.Add(3*time.Minute))
	if got := evaluator.Evaluate("cpu", 90, threshold, start.Add(4*time.Minute)); got != SeverityHealthy {
		t.Fatalf("warning initial = %s", got)
	}
	if got := evaluator.Evaluate("cpu", 90, threshold, start.Add(9*time.Minute)); got != SeverityWarning {
		t.Fatalf("warning at 5m = %s", got)
	}
}

func TestParsersRejectMalformedAndNonMonotonicInputs(t *testing.T) {
	if _, err := parseProcStat([]byte("intr 1 2\n")); err == nil {
		t.Fatal("accepted missing cpu line")
	}
	if _, ok := cpuUtilization(cpuCounters{busy: 5, total: 10}, cpuCounters{busy: 4, total: 20}); ok {
		t.Fatal("accepted decreasing counters")
	}
	if _, err := parseMeminfo([]byte("MemTotal: 10 kB\n")); err == nil {
		t.Fatal("accepted missing available memory")
	}
	if _, err := parseTemperature([]byte("999999")); err == nil {
		t.Fatal("accepted implausible temperature")
	}
	if _, err := parseTemperature([]byte("NaN")); err == nil {
		t.Fatal("accepted non-finite temperature")
	}
}
