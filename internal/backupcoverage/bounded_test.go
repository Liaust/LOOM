package backupcoverage

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestBoundedSummaryDropsPathsAndEntryDetails(t *testing.T) {
	report := Report{SchemaVersion: SchemaVersion, Status: OverallWarning, GeneratedAt: time.Now(), BackupRoot: "/private", LatestManifestPath: "/private/manifest.json", Summary: Summary{Covered: 2, Missing: 1, Unknown: 1, RequiresDecision: 1, CriticalMissing: 1}, Entries: []Entry{{Path: "/private/secret", Message: "raw detail"}}}
	payload, err := json.Marshal(Bounded(report))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(payload), "/private") || strings.Contains(string(payload), "raw detail") {
		t.Fatalf("bounded payload leaked detail: %s", payload)
	}
}
