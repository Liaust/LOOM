package backupcoverage

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"loom.local/loom/internal/backupstrategy"
	"loom.local/loom/internal/boxregistry"
)

func TestBoxCoverageMissingActivationIsNotProtected(t *testing.T) {
	catalog := boxregistry.Catalog{Boxes: []boxregistry.Box{
		{Key: "personal", Primary: true, Available: true},
		{Key: "company", Label: "Company", RootPath: filepath.Join(t.TempDir(), "absent"), Reason: "root_missing"},
	}}
	entries := boxCoverageEntries(context.Background(), Options{Boxes: catalog}, time.Now())
	if len(entries) != 2 || entries[0].BoxKey != "company" || entries[0].Status != StatusMissing || entries[0].BoxID != "" || entries[1].Status != StatusUnknown {
		t.Fatalf("unactivated Box claimed protection or identity: %+v", entries)
	}
	if got := contractFromEntry(entries[0]); got.ContractStatus == "satisfied" || got.BoxKey != "company" {
		t.Fatalf("missing source satisfied contract: %+v", got)
	}
}

func TestBoxCoverageRequiresExactDatedCloudEvidence(t *testing.T) {
	root := t.TempDir()
	box := boxregistry.Box{ID: "box_company", Key: "company", Label: "Company", Node: "main", RootPath: root, StateRoot: filepath.Join(root, "missing-state"), ArchiveRoot: filepath.Join(root, "unused-archive"), Available: true}
	now := time.Date(2026, 11, 8, 10, 0, 0, 0, time.UTC)
	proof := CloudRootEvidence{Name: "box_company_source", PathSHA256: CloudRootPathSHA256(root), NodeID: "main", SnapshotAt: now.Add(-time.Hour), ManifestSHA256: strings.Repeat("a", 64)}
	for _, test := range []struct {
		name   string
		mutate func(*CloudRootEvidence)
		want   string
	}{
		{"exact", func(*CloudRootEvidence) {}, StatusCovered},
		{"name", func(e *CloudRootEvidence) { e.Name = "box_other_source" }, StatusMissing},
		{"path", func(e *CloudRootEvidence) { e.PathSHA256 = CloudRootPathSHA256("/other") }, StatusMissing},
		{"node", func(e *CloudRootEvidence) { e.NodeID = "other" }, StatusMissing},
		{"future", func(e *CloudRootEvidence) { e.SnapshotAt = now.Add(time.Hour) }, StatusMissing},
		{"unauthenticated", func(e *CloudRootEvidence) { e.ManifestSHA256 = "" }, StatusMissing},
	} {
		t.Run(test.name, func(t *testing.T) {
			evidence := proof
			test.mutate(&evidence)
			entries := boxCoverageEntries(context.Background(), Options{Boxes: boxregistry.Catalog{Boxes: []boxregistry.Box{box}}, CloudRoots: []CloudRootEvidence{evidence}}, now)
			if len(entries) != 4 || entries[0].Status != StatusCovered || entries[1].Status != test.want || entries[2].Status != StatusMissing || entries[3].Status != StatusMissing {
				t.Fatalf("coverage did not separate source, state and exact cloud proof: %+v", entries)
			}
			if test.want == StatusCovered && contractFromEntry(entries[1]).ContractStatus != "satisfied" {
				t.Fatal("dated authenticated evidence did not satisfy dated contract")
			}
		})
	}
	if _, err := os.Stat(box.ArchiveRoot); !os.IsNotExist(err) {
		t.Fatal("coverage created unused archive")
	}
}

func TestBoxProtectionCatalogRefresh(t *testing.T) {
	calls := 0
	opts := Options{LoadBoxes: func() (boxregistry.Catalog, error) {
		calls++
		return boxregistry.Catalog{DefaultBox: "current"}, nil
	}}
	if catalog, err := opts.ProtectionBoxes(); err != nil || catalog.DefaultBox != "current" || calls != 1 {
		t.Fatalf("catalog refresh: %+v %v calls=%d", catalog, err, calls)
	}
}

func TestBoxCloudEvidenceExcludesOnlyOwnedScratch(t *testing.T) {
	for _, test := range []struct {
		name, path string
		want       bool
	}{
		{"box_company_source", ".loom-acceptance", true},
		{"box_company_source", "Notes", false},
		{"box_company_state", ".loom-acceptance", false},
		{"application_data", ".loom-acceptance", false},
	} {
		if got := cloudRootEvidenceIncluded(test.name, []backupstrategy.DirectArchiveExclusion{{Root: test.name, RelativePath: test.path}}); got != test.want {
			t.Fatalf("root=%s exclusion=%s got=%t want=%t", test.name, test.path, got, test.want)
		}
	}
	if !cloudRootEvidenceIncluded("box_notes", nil) {
		t.Fatal("historical complete root lost evidence")
	}
}
