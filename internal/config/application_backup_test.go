package config

import "testing"

func TestApplicationDataBackupRootConfiguration(t *testing.T) {
	for _, path := range []string{"/srv/loom/application-data", "/", "relative", "/srv/../etc"} {
		cfg := Config{}
		err := applyMap(&cfg, map[string]string{"LOOM_APPLICATION_DATA_BACKUP_ROOT": path})
		if (err == nil) != (path == "/srv/loom/application-data") {
			t.Fatalf("%s: %v", path, err)
		}
	}
	var cfg Config
	t.Setenv("LOOM_APPLICATION_DATA_BACKUP_ROOT", "/srv/loom/application-data")
	if err := applyEnv(&cfg); err != nil || cfg.ApplicationDataBackupRoot != "/srv/loom/application-data" {
		t.Fatal("daemon environment root lost", err)
	}
}
