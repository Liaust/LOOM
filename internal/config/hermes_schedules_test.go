package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"loom.local/loom/internal/hermesschedules"
)

func TestHermesScheduleConfigurationSources(t *testing.T) {
	for _, key := range []string{"LOOM_CONFIG_FILE", "LOOM_HERMES_SCHEDULES_SOCKET", "LOOM_HERMES_SCHEDULES_PROFILE", "LOOM_HERMES_SCHEDULES_HOME", "LOOM_HERMES_SCHEDULES_REVISION", "LOOM_MINA_SELECTED", "LOOM_MINA_ENABLED", "LOOM_MINA_RECOVERY_ENABLED", "LOOM_MINA_RECOVERY_PUBLIC_KEY", "LOOM_MINA_RECOVERY_RETAINED_MORATHUSTRA_IDS", "LOOM_MORATHUSTRA_ENABLED", "LOOM_MORATHUSTRA_RECOVERY_ENABLED", "LOOM_MORATHUSTRA_RECOVERY_PUBLIC_KEY"} {
		t.Setenv(key, "")
	}
	t.Setenv("HERMES_HOME", "/private/ambient/profile")
	t.Setenv("HERMES_PROFILE", "ambient")
	cfg, err := Load(Overrides{})
	if err != nil {
		t.Fatal(err)
	}
	source, err := cfg.HermesScheduleSource()
	if err != nil || source.Profile != "" || cfg.HermesSchedulesHome != "" {
		t.Fatalf("ambient profile adopted: %+v %v", source, err)
	}
	for _, input := range []string{"file", "environment"} {
		t.Run(input, func(t *testing.T) {
			vals := map[string]string{"LOOM_HERMES_SCHEDULES_SOCKET": "/run/loom-mina-schedules/observe.sock", "LOOM_MINA_SELECTED": "true", "LOOM_HERMES_SCHEDULES_PROFILE": "mina", "LOOM_HERMES_SCHEDULES_HOME": "/explicit/mina/.hermes", "LOOM_HERMES_SCHEDULES_REVISION": hermesschedules.NativeRevision}
			var lines []string
			opts := Overrides{}
			for k, v := range vals {
				if input == "environment" {
					t.Setenv(k, v)
				} else {
					lines = append(lines, k+"="+v)
				}
			}
			if input == "file" {
				opts.ConfigFile = filepath.Join(t.TempDir(), "loom.env")
				if err := os.WriteFile(opts.ConfigFile, []byte(strings.Join(lines, "\n")), 0600); err != nil {
					t.Fatal(err)
				}
			}
			cfg, err := Load(opts)
			if err != nil {
				t.Fatal(err)
			}
			source, err := cfg.HermesScheduleSource()
			if err != nil || source.Profile != "mina" || source.Host != cfg.NodeID || source.Revision != hermesschedules.NativeRevision || cfg.HermesSchedulesSocket != vals["LOOM_HERMES_SCHEDULES_SOCKET"] || cfg.HermesSchedulesHome != "/explicit/mina/.hermes" {
				t.Fatalf("binding lost: %+v %v", source, err)
			}
		})
	}
}

func TestHermesScheduleBindingValidation(t *testing.T) {
	valid := Config{NodeID: "main", MinaSelected: true, HermesSchedulesProfile: "mina", HermesSchedulesHome: "/explicit/mina/.hermes", HermesSchedulesRevision: hermesschedules.NativeRevision}
	for name, mutate := range map[string]func(*Config){
		"other profile socket": func(c *Config) { c.HermesSchedulesSocket = hermesschedules.SocketPath("morathustra") },
		"invalid socket":       func(c *Config) { c.HermesSchedulesSocket = "/tmp/helper" },
		"caller arguments": func(c *Config) {
			c.HermesSchedulesSocket = "/run/loom-mina-schedules/observe.sock --home=/private"
		},
		"missing profile":       func(c *Config) { c.HermesSchedulesProfile = "" },
		"missing home":          func(c *Config) { c.HermesSchedulesHome = "" },
		"relative home":         func(c *Config) { c.HermesSchedulesHome = "relative" },
		"unclean home":          func(c *Config) { c.HermesSchedulesHome = "/explicit/../other" },
		"root home":             func(c *Config) { c.HermesSchedulesHome = "/" },
		"missing revision":      func(c *Config) { c.HermesSchedulesRevision = "" },
		"bad revision":          func(c *Config) { c.HermesSchedulesRevision = strings.Repeat("x", 40) },
		"no host":               func(c *Config) { c.NodeID = "" },
		"wrong selection":       func(c *Config) { c.MinaSelected = false },
		"conflicting selection": func(c *Config) { c.MorathustraEnabled = true },
	} {
		t.Run(name, func(t *testing.T) {
			c := valid
			mutate(&c)
			if _, err := c.HermesScheduleSource(); err == nil {
				t.Fatal("invalid binding accepted")
			}
		})
	}
	// Older selected profile remains supported; a syntactically valid new source
	// revision must survive configuration so observation can report unsupported.
	legacy := valid
	legacy.MinaSelected = false
	legacy.HermesSchedulesProfile = "morathustra"
	legacy.HermesSchedulesHome = "/explicit/morathustra/.hermes"
	legacy.HermesSchedulesRevision = strings.Repeat("a", 40)
	source, err := legacy.HermesScheduleSource()
	if err != nil || source.Profile != "morathustra" || source.Revision != legacy.HermesSchedulesRevision {
		t.Fatalf("legacy/version evidence altered: %+v %v", source, err)
	}
}
