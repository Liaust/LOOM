package minidashboard

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCacheRoundTripPermissionsAndValidation(t *testing.T) {
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "state", "last-good.json")
	cache := DomainCache{Path: path}
	want := healthyDomainFixture(now)
	if err := cache.Save(want); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o640 {
		t.Fatalf("mode = %o", info.Mode().Perm())
	}
	got, err := cache.Load()
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.SchemaVersion != SchemaVersion {
		t.Fatalf("cache = %+v", got)
	}
	if err := os.WriteFile(path, []byte(`{"schema_version":"foreign"}`), 0o640); err != nil {
		t.Fatal(err)
	}
	if _, err := cache.Load(); err == nil {
		t.Fatal("foreign cache accepted")
	}
}
