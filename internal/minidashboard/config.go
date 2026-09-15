package minidashboard

import (
	"fmt"
	"net"
	"path/filepath"
	"strings"
	"time"
)

type Threshold struct {
	Warning         float64       `json:"warning"`
	Critical        float64       `json:"critical"`
	WarningFor      time.Duration `json:"warning_for"`
	CriticalFor     time.Duration `json:"critical_for"`
	WarningSamples  int           `json:"warning_samples"`
	CriticalSamples int           `json:"critical_samples"`
}

type Thresholds struct {
	CPU         Threshold `json:"cpu"`
	Temperature Threshold `json:"temperature"`
	Memory      Threshold `json:"memory"`
	Storage     Threshold `json:"storage"`
}

type Config struct {
	SamplingInterval     time.Duration `json:"sampling_interval"`
	PollInterval         time.Duration `json:"poll_interval"`
	RequestTimeout       time.Duration `json:"request_timeout"`
	HostStaleAfter       time.Duration `json:"host_stale_after"`
	DomainStaleAfter     time.Duration `json:"domain_stale_after"`
	CloudCacheStaleAfter time.Duration `json:"cloud_cache_stale_after"`
	QueueWarningAfter    time.Duration `json:"queue_warning_after"`
	NodeLabel            string        `json:"node_label"`
	ListenAddress        string        `json:"listen_address"`
	StatePath            string        `json:"state_path"`
	ExpectedOnlineNodes  []string      `json:"expected_online_nodes"`
	TemperatureRequired  bool          `json:"temperature_required"`
	Thresholds           Thresholds    `json:"thresholds"`
}

func DefaultConfig() Config {
	return Config{
		SamplingInterval:     2 * time.Second,
		PollInterval:         10 * time.Second,
		RequestTimeout:       2 * time.Second,
		HostStaleAfter:       6 * time.Second,
		DomainStaleAfter:     30 * time.Second,
		CloudCacheStaleAfter: 12 * time.Hour,
		QueueWarningAfter:    15 * time.Minute,
		NodeLabel:            "MAIN",
		ListenAddress:        "127.0.0.1:8090",
		StatePath:            "/var/lib/loom/mini-dashboard/last-good.json",
		ExpectedOnlineNodes:  []string{"main"},
		Thresholds: Thresholds{
			CPU:         Threshold{Warning: 85, Critical: 95, WarningFor: 5 * time.Minute, CriticalFor: 2 * time.Minute},
			Temperature: Threshold{Warning: 75, Critical: 85, WarningSamples: 2, CriticalSamples: 2},
			Memory:      Threshold{Warning: 85, Critical: 95, WarningSamples: 2, CriticalSamples: 2},
			Storage:     Threshold{Warning: 80, Critical: 90, WarningSamples: 1, CriticalSamples: 1},
		},
	}
}

func (c Config) Validate() error {
	if c.SamplingInterval <= 0 || c.PollInterval <= 0 || c.HostStaleAfter <= 0 || c.DomainStaleAfter <= 0 || c.CloudCacheStaleAfter <= 0 {
		return fmt.Errorf("sampling, polling, and stale durations must be positive")
	}
	if c.RequestTimeout <= 0 || c.RequestTimeout > 5*time.Second || c.RequestTimeout > c.PollInterval {
		return fmt.Errorf("request timeout must be positive, at most five seconds, and no greater than the poll interval")
	}
	if c.QueueWarningAfter <= 0 {
		return fmt.Errorf("queue warning duration must be positive")
	}
	if _, err := SanitizeLabel(c.NodeLabel, MaxNodeLabelLength); err != nil {
		return fmt.Errorf("node label: %w", err)
	}
	host, _, err := net.SplitHostPort(strings.TrimSpace(c.ListenAddress))
	if err != nil {
		return fmt.Errorf("listen address: %w", err)
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("listen address must use a loopback IP")
	}
	if !filepath.IsAbs(c.StatePath) || filepath.Clean(c.StatePath) == string(filepath.Separator) {
		return fmt.Errorf("state path must be an absolute file path")
	}
	for name, threshold := range map[string]Threshold{
		"cpu": c.Thresholds.CPU, "temperature": c.Thresholds.Temperature,
		"memory": c.Thresholds.Memory, "storage": c.Thresholds.Storage,
	} {
		if threshold.Warning <= 0 || threshold.Critical <= threshold.Warning {
			return fmt.Errorf("%s thresholds must satisfy 0 < warning < critical", name)
		}
		if err := validateStabilization(name+" warning", threshold.WarningFor, threshold.WarningSamples); err != nil {
			return err
		}
		if err := validateStabilization(name+" critical", threshold.CriticalFor, threshold.CriticalSamples); err != nil {
			return err
		}
	}
	seen := map[string]bool{}
	for _, key := range c.ExpectedOnlineNodes {
		if key == "" || key != strings.TrimSpace(key) || len(key) > MaxNodeKeyLength || !safeTokenPattern.MatchString(key) {
			return fmt.Errorf("expected node key %q is invalid", key)
		}
		if seen[key] {
			return fmt.Errorf("expected node key %q is duplicated", key)
		}
		seen[key] = true
	}
	return nil
}

func validateStabilization(name string, duration time.Duration, samples int) error {
	if duration < 0 || samples < 0 {
		return fmt.Errorf("%s stabilization must not be negative", name)
	}
	if duration == 0 && samples == 0 {
		return fmt.Errorf("%s stabilization requires a duration or sample count", name)
	}
	return nil
}
