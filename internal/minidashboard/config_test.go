package minidashboard

import (
	"testing"
	"time"
)

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	if cfg.SamplingInterval != 2*time.Second || cfg.PollInterval != 10*time.Second || cfg.RequestTimeout != 2*time.Second {
		t.Fatalf("unexpected cadence: sample=%s poll=%s timeout=%s", cfg.SamplingInterval, cfg.PollInterval, cfg.RequestTimeout)
	}
	if cfg.Thresholds.CPU.Warning != 85 || cfg.Thresholds.CPU.Critical != 95 || cfg.Thresholds.Storage.Warning != 80 || cfg.Thresholds.Storage.Critical != 90 {
		t.Fatalf("unexpected thresholds: %#v", cfg.Thresholds)
	}
	if cfg.Thresholds.CPU.WarningFor != 5*time.Minute || cfg.Thresholds.CPU.CriticalFor != 2*time.Minute {
		t.Fatalf("unexpected CPU stabilization: %#v", cfg.Thresholds.CPU)
	}
	if cfg.Thresholds.Temperature.WarningSamples != 2 || cfg.Thresholds.Temperature.CriticalSamples != 2 ||
		cfg.Thresholds.Memory.WarningSamples != 2 || cfg.Thresholds.Memory.CriticalSamples != 2 ||
		cfg.Thresholds.Storage.WarningSamples != 1 || cfg.Thresholds.Storage.CriticalSamples != 1 {
		t.Fatalf("unexpected sample stabilization: %#v", cfg.Thresholds)
	}
	if cfg.ListenAddress != "127.0.0.1:8090" || cfg.StatePath != "/var/lib/loom/mini-dashboard/last-good.json" {
		t.Fatalf("unexpected process defaults: %#v", cfg)
	}
}

func TestConfigValidation(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Config)
	}{
		{"public listen", func(c *Config) { c.ListenAddress = "0.0.0.0:8090" }},
		{"relative state", func(c *Config) { c.StatePath = "last-good.json" }},
		{"unsafe label", func(c *Config) { c.NodeLabel = "/var/lib/loom" }},
		{"invalid threshold", func(c *Config) { c.Thresholds.Storage.Critical = c.Thresholds.Storage.Warning }},
		{"negative warning duration", func(c *Config) { c.Thresholds.CPU.WarningFor = -time.Second }},
		{"negative critical duration", func(c *Config) { c.Thresholds.CPU.CriticalFor = -time.Second }},
		{"negative warning samples", func(c *Config) { c.Thresholds.Memory.WarningSamples = -1 }},
		{"negative critical samples", func(c *Config) { c.Thresholds.Memory.CriticalSamples = -1 }},
		{"missing warning stabilization", func(c *Config) { c.Thresholds.CPU.WarningFor = 0 }},
		{"missing critical stabilization", func(c *Config) { c.Thresholds.CPU.CriticalFor = 0 }},
		{"duplicate expected node", func(c *Config) { c.ExpectedOnlineNodes = []string{"main", "main"} }},
		{"invalid expected node", func(c *Config) { c.ExpectedOnlineNodes = []string{"main node"} }},
		{"padded expected node", func(c *Config) { c.ExpectedOnlineNodes = []string{" main "} }},
		{"missing request timeout", func(c *Config) { c.RequestTimeout = 0 }},
		{"request timeout exceeds maximum", func(c *Config) { c.RequestTimeout = 6 * time.Second }},
		{"request timeout exceeds poll cadence", func(c *Config) { c.RequestTimeout = c.PollInterval + time.Second }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := DefaultConfig()
			tt.mutate(&cfg)
			if err := cfg.Validate(); err == nil {
				t.Fatal("expected validation failure")
			}
		})
	}
}
