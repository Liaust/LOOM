package minidashboard

import (
	"os"
	"path/filepath"
	"sync"
	"time"
)

type HostCollector struct {
	ReadFile        func(string) ([]byte, error)
	Glob            func(string) ([]string, error)
	FilesystemUsage func(string) (float64, error)
	Now             func() time.Time
	ProcRoot        string
	SysRoot         string
	DataPath        string

	mu        sync.Mutex
	previous  cpuCounters
	haveCPU   bool
	threshold ThresholdEvaluator
}

func NewHostCollector(dataPath string) *HostCollector {
	return &HostCollector{ReadFile: os.ReadFile, Glob: filepath.Glob, FilesystemUsage: platformFilesystemUsage, Now: time.Now, ProcRoot: "/proc", SysRoot: "/sys", DataPath: dataPath}
}

func (c *HostCollector) Collect(cfg Config) HostSnapshot {
	now := c.now().UTC()
	return HostSnapshot{
		GeneratedAt: now,
		CPU:         c.cpuMetric(now, cfg),
		Temperature: c.temperatureMetric(now, cfg),
		Memory:      c.memoryMetric(now, cfg),
		Storage:     c.storageMetric(now, cfg),
	}
}

func (c *HostCollector) cpuMetric(now time.Time, cfg Config) Metric {
	data, err := c.readFile(filepath.Join(c.procRoot(), "stat"))
	if err != nil {
		return unavailableMetric("%", now, cfg.HostStaleAfter)
	}
	current, err := parseProcStat(data)
	if err != nil {
		return unavailableMetric("%", now, cfg.HostStaleAfter)
	}
	c.mu.Lock()
	previous, havePrevious := c.previous, c.haveCPU
	c.previous, c.haveCPU = current, true
	c.mu.Unlock()
	if !havePrevious {
		return unavailableMetric("%", now, cfg.HostStaleAfter)
	}
	value, ok := cpuUtilization(previous, current)
	if !ok {
		return unavailableMetric("%", now, cfg.HostStaleAfter)
	}
	return c.availableMetric("cpu", value, "%", now, cfg.HostStaleAfter, cfg.Thresholds.CPU)
}

func (c *HostCollector) memoryMetric(now time.Time, cfg Config) Metric {
	data, err := c.readFile(filepath.Join(c.procRoot(), "meminfo"))
	if err != nil {
		return unavailableMetric("%", now, cfg.HostStaleAfter)
	}
	value, err := parseMeminfo(data)
	if err != nil {
		return unavailableMetric("%", now, cfg.HostStaleAfter)
	}
	return c.availableMetric("memory", value, "%", now, cfg.HostStaleAfter, cfg.Thresholds.Memory)
}

func (c *HostCollector) temperatureMetric(now time.Time, cfg Config) Metric {
	value, err := selectTemperature(c.readFile, c.glob, c.sysRoot())
	if err == nil {
		return c.availableMetric("temperature", value, "°C", now, cfg.HostStaleAfter, cfg.Thresholds.Temperature)
	}
	metric := unavailableMetric("°C", now, cfg.HostStaleAfter)
	if cfg.TemperatureRequired {
		metric.Severity = SeverityWarning
	}
	return metric
}

func (c *HostCollector) storageMetric(now time.Time, cfg Config) Metric {
	value, err := c.filesystemUsage(c.DataPath)
	if err != nil {
		return unavailableMetric("%", now, cfg.HostStaleAfter)
	}
	return c.availableMetric("storage", value, "%", now, cfg.HostStaleAfter, cfg.Thresholds.Storage)
}

func (c *HostCollector) availableMetric(key string, value float64, unit string, now time.Time, staleAfter time.Duration, threshold Threshold) Metric {
	severity := c.threshold.Evaluate(key, value, threshold, now)
	return Metric{Available: true, Value: &value, Unit: unit, SampledAt: now, StaleAfterSeconds: int64(staleAfter / time.Second), SourceState: SourceLive, Severity: severity}
}

func unavailableMetric(unit string, now time.Time, staleAfter time.Duration) Metric {
	return Metric{Available: false, Unit: unit, SampledAt: now, StaleAfterSeconds: int64(staleAfter / time.Second), SourceState: SourceUnavailable, Severity: SeverityUnknown}
}

func (c *HostCollector) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}
func (c *HostCollector) procRoot() string {
	if c.ProcRoot != "" {
		return c.ProcRoot
	}
	return "/proc"
}
func (c *HostCollector) sysRoot() string {
	if c.SysRoot != "" {
		return c.SysRoot
	}
	return "/sys"
}
func (c *HostCollector) readFile(path string) ([]byte, error) {
	if c.ReadFile != nil {
		return c.ReadFile(path)
	}
	return os.ReadFile(path)
}
func (c *HostCollector) glob(pattern string) ([]string, error) {
	if c.Glob != nil {
		return c.Glob(pattern)
	}
	return filepath.Glob(pattern)
}
func (c *HostCollector) filesystemUsage(path string) (float64, error) {
	if c.FilesystemUsage != nil {
		return c.FilesystemUsage(path)
	}
	return platformFilesystemUsage(path)
}
