package maintenance

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"loom.local/loom/internal/provenance"
)

func TestVerifyProvenanceBackupPackageBindsCompleteLedgerAndManifestIdentity(t *testing.T) {
	dir, manifest, identity := validProvenanceBackupPackage(t)
	verification, err := VerifyProvenanceBackupPackage(context.Background(), dir, identity)
	if err != nil {
		t.Fatal(err)
	}
	if verification.Status != VerificationSucceeded || verification.SchemaHead != provenance.SchemaHead || verification.GraphDigest != manifest.Provenance.GraphDigest {
		t.Fatalf("verification = %#v", verification)
	}
	if len(verification.LogicalCounts) != len(provenance.RecoveryRelations()) || verification.DumpSizeBytes != manifest.Provenance.DumpSizeBytes {
		t.Fatalf("incomplete provenance verification = %#v", verification)
	}

	raw, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	raw = bytes.Replace(raw, []byte("loom_main_and_provenance_state"), []byte("loom_tampered_provenance_state"), 1)
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyProvenanceBackupPackage(context.Background(), dir, identity); err == nil || !strings.Contains(err.Error(), "manifest identity mismatch") {
		t.Fatalf("tampered manifest error = %v", err)
	}
}

func TestVerifyProvenanceBackupPackageFailsClosedOnIncompleteCoverageAndDump(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, string, *BackupManifest)
		want   string
	}{
		{
			name: "interrupted backup",
			mutate: func(_ *testing.T, _ string, manifest *BackupManifest) {
				manifest.Provenance.State = provenance.BackupStateInterrupted
			},
			want: "not complete",
		},
		{
			name: "schema mismatch",
			mutate: func(_ *testing.T, _ string, manifest *BackupManifest) {
				manifest.Provenance.SchemaHead++
			},
			want: "incompatible",
		},
		{
			name: "missing source relation",
			mutate: func(_ *testing.T, _ string, manifest *BackupManifest) {
				delete(manifest.Provenance.LogicalCounts, "source_references")
			},
			want: "logical count coverage is incomplete",
		},
		{
			name: "missing evidence relation declaration",
			mutate: func(_ *testing.T, _ string, manifest *BackupManifest) {
				manifest.Provenance.RequiredRelations = manifest.Provenance.RequiredRelations[1:]
			},
			want: "required relation coverage is incomplete",
		},
		{
			name: "missing authenticated artifact",
			mutate: func(_ *testing.T, _ string, manifest *BackupManifest) {
				manifest.Artifacts = nil
			},
			want: "exactly one authenticated dump artifact",
		},
		{
			name: "artifact contract mismatch",
			mutate: func(_ *testing.T, _ string, manifest *BackupManifest) {
				manifest.Artifacts[0].SHA256 = strings.Repeat("f", 64)
			},
			want: "does not match its recovery contract",
		},
		{
			name: "tampered dump",
			mutate: func(t *testing.T, dir string, _ *BackupManifest) {
				if err := os.WriteFile(filepath.Join(dir, "loom_provenance.dump"), []byte("changed dump bytes"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
			want: "dump size or sha256 mismatch",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dir, manifest, _ := validProvenanceBackupPackage(t)
			test.mutate(t, dir, &manifest)
			writeProvenanceManifest(t, dir, manifest)
			identity, _, err := HashFile(filepath.Join(dir, "manifest.json"))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := VerifyProvenanceBackupPackage(context.Background(), dir, identity); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestVerifyProvenanceBackupPackageAcceptsExactHistoricalHeadSixAndRejectsCrossHeadCoverage(t *testing.T) {
	dir, manifest, identity := validProvenanceBackupPackageAtHead(t, 6)
	verification, err := VerifyProvenanceBackupPackage(context.Background(), dir, identity)
	if err != nil {
		t.Fatalf("verify exact head-6 package: %v", err)
	}
	if verification.SchemaHead != 6 || len(verification.LogicalCounts) != 19 || verification.DumpFile != provenance.RecoveryDumpFile || verification.DumpSHA256 != manifest.Provenance.DumpSHA256 {
		t.Fatalf("historical verification = %#v", verification)
	}

	tests := []struct {
		name   string
		mutate func(*BackupManifest)
		want   string
	}{
		{name: "zero head", mutate: func(item *BackupManifest) { item.Provenance.SchemaHead = 0 }, want: "incompatible"},
		{name: "unknown old head", mutate: func(item *BackupManifest) { item.Provenance.SchemaHead = 5 }, want: "incompatible"},
		{name: "future head", mutate: func(item *BackupManifest) { item.Provenance.SchemaHead = provenance.SchemaHead + 1 }, want: "incompatible"},
		{name: "duplicate relation", mutate: func(item *BackupManifest) {
			item.Provenance.RequiredRelations = append(item.Provenance.RequiredRelations, item.Provenance.RequiredRelations[0])
		}, want: "required relation coverage is incomplete"},
		{name: "missing relation", mutate: func(item *BackupManifest) {
			delete(item.Provenance.LogicalCounts, "source_references")
		}, want: "logical count coverage is incomplete"},
		{name: "head-7 relation at head 6", mutate: func(item *BackupManifest) {
			item.Provenance.RequiredRelations = append(item.Provenance.RequiredRelations, "project_projection_snapshots")
			item.Provenance.LogicalCounts["project_projection_snapshots"] = 0
		}, want: "required relation coverage is incomplete"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dir, candidate, _ := validProvenanceBackupPackageAtHead(t, 6)
			test.mutate(&candidate)
			writeProvenanceManifest(t, dir, candidate)
			candidateIdentity, _, err := HashFile(filepath.Join(dir, provenance.RecoveryManifestFile))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := VerifyProvenanceBackupPackage(context.Background(), dir, candidateIdentity); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestVerifyProvenanceBackupPackageRejectsSymlinkedDump(t *testing.T) {
	dir, manifest, _ := validProvenanceBackupPackage(t)
	external := filepath.Join(t.TempDir(), "external.dump")
	if err := os.WriteFile(external, []byte("provenance dump fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	dumpPath := filepath.Join(dir, manifest.Provenance.Database.DumpFile)
	if err := os.Remove(dumpPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, dumpPath); err != nil {
		t.Fatal(err)
	}
	identity, _, err := HashFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyProvenanceBackupPackage(context.Background(), dir, identity); err == nil || !strings.Contains(err.Error(), "no-follow regular file") {
		t.Fatalf("symlinked dump error = %v", err)
	}
}

func TestVerifyProvenanceBackupPackageRejectsPermissionDrift(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, string)
		want   string
	}{
		{
			name: "package root",
			mutate: func(t *testing.T, dir string) {
				if err := os.Chmod(dir, 0o750); err != nil {
					t.Fatal(err)
				}
			},
			want: "package root must be a real directory at exact mode 0700",
		},
		{
			name: "manifest",
			mutate: func(t *testing.T, dir string) {
				if err := os.Chmod(filepath.Join(dir, provenance.RecoveryManifestFile), 0o640); err != nil {
					t.Fatal(err)
				}
			},
			want: "backup manifest must be a no-follow regular file at exact mode 0600",
		},
		{
			name: "dump",
			mutate: func(t *testing.T, dir string) {
				if err := os.Chmod(filepath.Join(dir, provenance.RecoveryDumpFile), 0o640); err != nil {
					t.Fatal(err)
				}
			},
			want: "file must be a no-follow regular file at exact mode 0600",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dir, _, identity := validProvenanceBackupPackage(t)
			test.mutate(t, dir)
			if _, err := VerifyProvenanceBackupPackage(context.Background(), dir, identity); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("permission drift error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestVerifyProvenanceBackupPackageRejectsSymlinkedRootAndManifest(t *testing.T) {
	t.Run("root", func(t *testing.T) {
		dir, _, identity := validProvenanceBackupPackage(t)
		link := filepath.Join(t.TempDir(), "provenance-package")
		if err := os.Symlink(dir, link); err != nil {
			t.Fatal(err)
		}
		if _, err := VerifyProvenanceBackupPackage(context.Background(), link, identity); err == nil || !strings.Contains(err.Error(), "package root must be a real directory") {
			t.Fatalf("symlinked root error = %v", err)
		}
	})
	t.Run("manifest", func(t *testing.T) {
		dir, _, identity := validProvenanceBackupPackage(t)
		manifestPath := filepath.Join(dir, provenance.RecoveryManifestFile)
		raw, err := os.ReadFile(manifestPath)
		if err != nil {
			t.Fatal(err)
		}
		external := filepath.Join(t.TempDir(), provenance.RecoveryManifestFile)
		if err := os.WriteFile(external, raw, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(manifestPath); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(external, manifestPath); err != nil {
			t.Fatal(err)
		}
		if _, err := VerifyProvenanceBackupPackage(context.Background(), dir, identity); err == nil || !strings.Contains(err.Error(), "backup manifest must be a no-follow regular file") {
			t.Fatalf("symlinked manifest error = %v", err)
		}
	})
}

func validProvenanceBackupPackage(t *testing.T) (string, BackupManifest, string) {
	return validProvenanceBackupPackageAtHead(t, provenance.SchemaHead)
}

func validProvenanceBackupPackageAtHead(t *testing.T, schemaHead int) (string, BackupManifest, string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	dumpPath := filepath.Join(dir, "loom_provenance.dump")
	if err := os.WriteFile(dumpPath, []byte("provenance dump fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	dumpHash, dumpSize, err := HashFile(dumpPath)
	if err != nil {
		t.Fatal(err)
	}
	relations, err := provenance.RecoveryRelationsForSchemaHead(schemaHead)
	if err != nil {
		t.Fatal(err)
	}
	counts := make(map[string]int64, len(relations))
	for _, relation := range relations {
		counts[relation] = 0
	}
	startedAt := time.Date(2026, 8, 29, 18, 0, 0, 0, time.UTC)
	manifest := BackupManifest{
		Schema:     BackupManifestSchemaV010,
		BackupKind: "loom_main_and_provenance_state",
		CreatedAt:  startedAt.Add(time.Minute).Format(time.RFC3339Nano),
		Provenance: &BackupManifestProvenance{
			State:       provenance.BackupStateComplete,
			StartedAt:   startedAt.Format(time.RFC3339Nano),
			CompletedAt: startedAt.Add(time.Minute).Format(time.RFC3339Nano),
			Database: BackupManifestDatabase{
				Name:     provenance.DatabaseName,
				DumpFile: "loom_provenance.dump",
				Format:   "pg_dump_custom",
			},
			SchemaHead:        schemaHead,
			RequiredRelations: relations,
			LogicalCounts:     counts,
			GraphDigest:       "sha256:" + strings.Repeat("a", 64),
			DumpSizeBytes:     dumpSize,
			DumpSHA256:        dumpHash,
		},
		Artifacts: []BackupManifestArtifact{{Kind: ArtifactKindProvenanceDump, Path: "loom_provenance.dump", FileCount: int64TestPtr(1), SizeBytes: &dumpSize, SHA256: dumpHash}},
	}
	writeProvenanceManifest(t, dir, manifest)
	identity, _, err := HashFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	return dir, manifest, identity
}

func int64TestPtr(value int64) *int64 {
	return &value
}

func writeProvenanceManifest(t *testing.T, dir string, manifest BackupManifest) {
	t.Helper()
	raw, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), append(raw, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
}
