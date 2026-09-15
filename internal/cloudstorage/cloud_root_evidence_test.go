package cloudstorage

import (
	"encoding/json"
	"strings"
	"testing"

	"loom.local/loom/internal/backupcoverage"
	"loom.local/loom/internal/backupstrategy"
)

func TestDirectArchiveCloudRootEvidenceUsesFrozenManifest(t *testing.T) {
	f := newDirectArchiveCloudFixture(t)
	q := directArchiveV2Request(f)
	q.Roots[0].Name = "application_data"
	q.Exclusions = nil
	name := "__loom-direct-user-data-" + q.NodeID + "-" + q.ArchiveRef
	p, err := backupstrategy.PrepareDirectArchiveManifestV2(t.Context(), backupstrategy.DirectArchiveManifestPrepareInputV2{Backend: backupstrategy.DirectArchiveBackendBorg, Repository: f.cfg.Snapshots.Borg.Repository, ArchiveName: name, Request: q})
	if err != nil {
		t.Fatal(err)
	}
	evidence := backupcoverage.CloudRootsFromManifest(p.ManifestV2, p.ManifestSHA256)
	found := false
	for _, e := range evidence {
		if e.Name == "application_data" {
			found = e.PathSHA256 == backupcoverage.CloudRootPathSHA256(q.Roots[0].Path) && e.SnapshotAt.Equal(q.CreatedAt) && e.NodeID == q.NodeID
		}
	}
	if !found {
		t.Fatal("authenticated root absent", evidence)
	}
	raw, _ := json.Marshal(evidence)
	if strings.Contains(string(raw), q.Roots[0].Path) {
		t.Fatal("physical path leaked")
	}
	if len(backupcoverage.CloudRootsFromManifest(p.ManifestV2, strings.Repeat("a", 64))) != 0 {
		t.Fatal("hash mismatch accepted")
	}
	p.ManifestV2.CreatedAt = p.ManifestV2.CreatedAt.AddDate(0, 0, 1)
	if len(backupcoverage.CloudRootsFromManifest(p.ManifestV2, p.ManifestSHA256)) != 0 {
		t.Fatal("live timestamp substituted for frozen snapshot")
	}
	q.Exclusions = []backupstrategy.DirectArchiveExclusion{{Root: "application_data", RelativePath: ".loom-acceptance"}}
	p, err = backupstrategy.PrepareDirectArchiveManifestV2(t.Context(), backupstrategy.DirectArchiveManifestPrepareInputV2{Backend: backupstrategy.DirectArchiveBackendBorg, Repository: f.cfg.Snapshots.Borg.Repository, ArchiveName: name, Request: q})
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range backupcoverage.CloudRootsFromManifest(p.ManifestV2, p.ManifestSHA256) {
		if e.Name == "application_data" {
			t.Fatal("partial root represented as complete coverage")
		}
	}
}
