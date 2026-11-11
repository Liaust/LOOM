package backupstrategy

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func referenceNotesFixture(t *testing.T, root, id string, operational OperationalPackageCreateResult) NotesRecoveryPublication {
	t.Helper()
	capture := filepath.Join(root, "notes-recovery", id)
	if err := os.MkdirAll(capture, 0700); err != nil {
		t.Fatal(err)
	}
	ref := NotesRecoveryOperationalReference{Schema: NotesRecoveryReferenceSchema, PackageID: operational.Verification.PackageID, ManifestSHA256: operational.ManifestSHA256}
	raw, _ := json.Marshal(ref)
	mustWriteOperational(t, filepath.Join(capture, "operational-reference.json"), raw)
	tar := []byte("tiny selected Notes state")
	mustWriteOperational(t, filepath.Join(capture, "main-state.tar.gz"), tar)
	cohort, _ := json.Marshal(map[string]any{"schema": "loom.notes_recovery.v2", "id": id, "captured_at": time.Date(2026, 10, 19, 2, 0, 0, 0, time.UTC),
		"package_id": ref.PackageID, "manifest_sha256": ref.ManifestSHA256, "main_state_sha256": notesRecoveryHash(tar), "operational_reference_sha256": notesRecoveryHash(raw)})
	mustWriteOperational(t, filepath.Join(capture, "cohort.json"), cohort)
	pub, err := RecordNotesRecoveryPublication(context.Background(), filepath.Dir(capture), id, filepath.Join(filepath.Dir(root), "publications"), false, filepath.Dir(operational.PackageDir))
	if err != nil {
		t.Fatal(err)
	}
	return pub
}

func TestDirectArchiveV3ReferencesDeduplicateAndResolve(t *testing.T) {
	fixture := newOperationalFixture(t)
	when := time.Date(2026, 10, 19, 1, 0, 0, 0, time.UTC)
	primary := createOperationalFixture(t, fixture, "operational-primary", when, nil)
	other := createOperationalFixture(t, fixture, "operational-distinct", when.Add(time.Minute), nil)
	app := filepath.Join(t.TempDir(), "application-data")
	if err := os.Mkdir(app, 0700); err != nil {
		t.Fatal(err)
	}
	first := referenceNotesFixture(t, app, "notes-20261017T020000Z-12345678", primary)
	shared := referenceNotesFixture(t, app, "notes-20261018T020000Z-12345678", primary)
	distinct := referenceNotesFixture(t, app, "notes-20261019T020000Z-12345678", other)
	base := directArchiveV2TestInput(app, primary, fixture.schemaHead)
	base.Request.Roots = []DirectArchiveRoot{{Name: "application_data", Path: app}}
	request := DirectArchiveRequestV3{Schema: DirectArchiveRequestSchemaV3, Archive: base.Request, NotesRecovery: []NotesRecoveryPublication{distinct, shared, first}}
	prepared, err := PrepareDirectArchiveManifestV3(context.Background(), base, request)
	if err != nil {
		t.Fatal(err)
	}
	if len(prepared.ManifestV3.OperationalDependencies) != 2 || len(AdditionalNotesRecoveryPackages(prepared)) != 1 {
		t.Fatal("shared dependency archived more than once")
	}
	parsed, err := ParseAuthenticatedDirectArchiveManifest(prepared.ManifestBytes, prepared.HashFileBytes)
	if err != nil || parsed.ManifestV3 == nil {
		t.Fatalf("v3 parse: %v", err)
	}
	if err := VerifyDirectArchiveV3PackageStability(context.Background(), base, parsed); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(app, "notes-recovery", first.ID, "operational-package")); !os.IsNotExist(err) {
		t.Fatal("reference capture duplicated the package")
	}

	extracted := t.TempDir()
	for _, source := range []string{app, primary.PackageDir, other.PackageDir, base.Request.ProvenancePackage.Path} {
		copyRecoveryFixtureTree(t, source, filepath.Join(extracted, filepath.FromSlash(directArchivePath(source))))
	}
	evidence := filepath.Join(extracted, DirectArchiveEvidenceDir)
	if err := os.Mkdir(evidence, 0700); err != nil {
		t.Fatal(err)
	}
	mustWriteOperational(t, filepath.Join(evidence, DirectArchiveManifestFile), prepared.ManifestBytes)
	mustWriteOperational(t, filepath.Join(evidence, DirectArchiveManifestHashFile), prepared.HashFileBytes)
	evidenceTime := time.Unix(0, DirectArchiveEvidenceModifiedUnixNano(prepared.ManifestV2.CreatedAt))
	for _, name := range []string{DirectArchiveManifestFile, DirectArchiveManifestHashFile, "."} {
		if err := os.Chtimes(filepath.Join(evidence, name), evidenceTime, evidenceTime); err != nil {
			t.Fatal(err)
		}
	}
	verification, err := VerifyExtractedDirectArchive(context.Background(), extracted, parsed)
	if err != nil || verification.Status != "succeeded" {
		t.Fatalf("extracted v3: %+v %v", verification, err)
	}
	resolved, err := ResolveExtractedNotesRecovery(context.Background(), extracted, parsed, distinct.ID)
	if err != nil || !strings.HasPrefix(resolved.OperationalPackagePath, extracted+string(filepath.Separator)) || filepath.Base(resolved.OperationalPackagePath) != other.Verification.PackageID {
		t.Fatalf("resolved %+v %v", resolved, err)
	}
	if err := os.Remove(filepath.Join(other.PackageDir, "postgres.dump")); err != nil {
		t.Fatal(err)
	}
	if _, err := PrepareDirectArchiveManifestV3(context.Background(), base, request); err == nil {
		t.Fatal("missing distinct dependency accepted")
	}
	var forged map[string]any
	if err := json.Unmarshal(prepared.ManifestBytes, &forged); err != nil {
		t.Fatal(err)
	}
	forged["unknown"] = "not allowed"
	raw, _ := json.Marshal(forged)
	if _, err := ParseAuthenticatedDirectArchiveManifest(raw, []byte(notesRecoveryHash(raw)+"  manifest.json\n")); err == nil {
		t.Fatal("v3 accepted unknown fields")
	}
}

func copyRecoveryFixtureTree(t *testing.T, source, destination string) {
	t.Helper()
	type metadata struct {
		name string
		info os.FileInfo
	}
	var all []metadata
	err := filepath.Walk(source, func(name string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(source, name)
		if err != nil {
			return err
		}
		target := filepath.Join(destination, relative)
		if info.IsDir() {
			err = os.MkdirAll(target, info.Mode().Perm())
		} else {
			var raw []byte
			raw, err = os.ReadFile(name)
			if err == nil {
				err = os.WriteFile(target, raw, info.Mode().Perm())
			}
		}
		if err == nil {
			all = append(all, metadata{target, info})
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	for i := len(all) - 1; i >= 0; i-- {
		if err := os.Chtimes(all[i].name, all[i].info.ModTime(), all[i].info.ModTime()); err != nil {
			t.Fatal(err)
		}
	}
}
