package backupstrategy

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestOperationalPackageNormalIsCompleteVerifiedAndIndependentOfUserData(t *testing.T) {
	fixture := newOperationalFixture(t)
	first := createOperationalFixture(t, fixture, "operational-001", time.Date(2026, 8, 29, 10, 0, 0, 0, time.UTC), nil)

	userRoot := filepath.Join(t.TempDir(), "canonical-user-data")
	if err := os.Mkdir(userRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	large := filepath.Join(userRoot, "large-user-object")
	file, err := os.OpenFile(large, os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(1 << 40); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	second := createOperationalFixture(t, fixture, "operational-002", time.Date(2026, 8, 29, 11, 0, 0, 0, time.UTC), nil)
	if first.Verification.Status != "succeeded" || second.Verification.Status != "succeeded" {
		t.Fatalf("verification failed: first=%#v second=%#v", first.Verification.Findings, second.Verification.Findings)
	}
	if first.Verification.ArtifactCount != 7 || second.Verification.ArtifactCount != 7 {
		t.Fatalf("artifact counts = %d/%d, want 7", first.Verification.ArtifactCount, second.Verification.ArtifactCount)
	}
	if first.Verification.TotalBytes != second.Verification.TotalBytes {
		t.Fatalf("package bytes changed with unrelated 1 TiB user root: first=%d second=%d", first.Verification.TotalBytes, second.Verification.TotalBytes)
	}
	if second.Verification.TotalBytes >= 1<<20 || second.Verification.TotalBytes >= OperationalPackageMaxBytes {
		t.Fatalf("fixture package bytes = %d, want a small bounded package", second.Verification.TotalBytes)
	}
	manifest := second.Verification.Manifest
	if manifest == nil || manifest.Schema != OperationalPackageSchema || manifest.PostgresFormat != "custom" || manifest.SchemaHead != fixture.schemaHead {
		t.Fatalf("unexpected manifest: %#v", manifest)
	}
	if manifest.Redaction.Policy != OperationalRedactionPolicy || manifest.Redaction.ArtifactCount != 3 || len(manifest.Redaction.Artifacts) != 3 || manifest.SourceStabilitySHA256 == "" {
		t.Fatalf("redaction/source evidence incomplete: %#v", manifest)
	}
	packageRaw := readAllPackageFiles(t, second.PackageDir)
	if bytes.Contains(packageRaw, []byte(fixture.secret)) || bytes.Contains(packageRaw, []byte(userRoot)) {
		t.Fatal("secret or unrelated user-data root entered operational package")
	}
	if len(second.RetentionPlan.Keep) != 2 || len(second.RetentionPlan.Remove) != 0 {
		t.Fatalf("retention plan = %#v, want two kept and no removals", second.RetentionPlan)
	}
}

func TestOperationalPackageSemanticRedactionCannotBeBypassedByNoopCallbackOrForbiddenLists(t *testing.T) {
	tests := []struct {
		name      string
		forbidden []string
	}{
		{name: "empty forbidden list"},
		{name: "partial forbidden list", forbidden: []string{"yaml-password-secret"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newOperationalFixture(t)
			secrets := []string{"database-password-secret", "env-token-secret", "yaml-password-secret", "json-credential-secret"}
			mustWriteOperational(t, fixture.serviceConfig, []byte("DATABASE_URL=postgres://loom:"+secrets[0]+"@database.example/loom\nTOKEN="+secrets[1]+"\n"))
			mustWriteOperational(t, fixture.installConfig, []byte("profile: main\npassword: "+secrets[2]+"\n"))
			mustWriteOperational(t, fixture.releaseConfig, []byte(`{"release":"current","credential":"`+secrets[3]+`"}`))
			input := fixture.input("operational-semantic", time.Date(2026, 8, 29, 10, 0, 0, 0, time.UTC))
			input.RedactConfig = func(_ string, source []byte) ([]byte, error) { return source, nil }
			input.ForbiddenValues = test.forbidden
			result, err := CreateOperationalPackage(context.Background(), input)
			if err != nil {
				t.Fatalf("semantic creation failed: %v", err)
			}
			packageRaw := readAllPackageFiles(t, result.PackageDir)
			for _, secret := range secrets {
				if bytes.Contains(packageRaw, []byte(secret)) {
					t.Fatal("semantic secret entered operational package")
				}
				if strings.Contains(result.ManifestSHA256, secret) {
					t.Fatal("semantic secret entered package evidence")
				}
			}
			if result.Verification.Manifest == nil || result.Verification.Manifest.Redaction.TotalRedactions == 0 {
				t.Fatalf("actual semantic redaction evidence missing: %#v", result.Verification.Manifest)
			}
		})
	}
}

func TestOperationalPackageAllowsGenuinelyNonSensitiveConfigWithoutCallback(t *testing.T) {
	fixture := newOperationalFixture(t)
	mustWriteOperational(t, fixture.serviceConfig, []byte("LOOM_ENV=production\nLOG_LEVEL=info\n"))
	mustWriteOperational(t, fixture.installConfig, []byte("profile: main\nregion: local\n"))
	mustWriteOperational(t, fixture.releaseConfig, []byte(`{"release":"current","channel":"stable"}`))
	input := fixture.input("operational-nonsensitive", time.Date(2026, 8, 29, 10, 0, 0, 0, time.UTC))
	input.RedactConfig = nil
	input.ForbiddenValues = nil
	result, err := CreateOperationalPackage(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	manifest := result.Verification.Manifest
	if manifest == nil || manifest.Redaction.Policy != OperationalRedactionPolicy || manifest.Redaction.TotalRedactions != 0 {
		t.Fatalf("non-sensitive evidence = %#v", manifest)
	}
	packageRaw := readAllPackageFiles(t, result.PackageDir)
	if !bytes.Contains(packageRaw, []byte("LOOM_ENV=production")) || !bytes.Contains(packageRaw, []byte(`"channel":"stable"`)) {
		t.Fatal("genuinely non-sensitive configuration was not preserved")
	}
	if bytes.Contains(packageRaw, []byte("fixture-secret-value")) {
		t.Fatal("unrelated fixture secret entered non-sensitive package")
	}
}

func TestOperationalPackageCreationFailsClosedAndPublishesNothing(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*OperationalPackageCreateInput, *operationalFixture)
		want   error
	}{
		{
			name: "secret survives redaction",
			mutate: func(input *OperationalPackageCreateInput, _ *operationalFixture) {
				input.RedactConfig = func(_ string, source []byte) ([]byte, error) { return source, nil }
			},
			want: ErrOperationalPackageRedaction,
		},
		{
			name: "secret in source path",
			mutate: func(input *OperationalPackageCreateInput, fixture *operationalFixture) {
				path := filepath.Join(fixture.sourceRoot, "service-"+fixture.secret)
				if err := os.Rename(fixture.serviceConfig, path); err != nil {
					t.Fatal(err)
				}
				input.ServiceConfig = path
			},
			want: ErrOperationalPackageRedaction,
		},
		{
			name: "dump capture fails",
			mutate: func(input *OperationalPackageCreateInput, _ *operationalFixture) {
				input.PostgresDump = func(context.Context, string) error { return errors.New("synthetic dump failure") }
			},
		},
		{
			name: "dump is not custom format",
			mutate: func(input *OperationalPackageCreateInput, _ *operationalFixture) {
				input.PostgresDump = func(_ context.Context, destination string) error {
					return os.WriteFile(destination, []byte("plain SQL"), 0o600)
				}
			},
			want: ErrOperationalPackageInvalid,
		},
		{
			name: "dump link",
			mutate: func(input *OperationalPackageCreateInput, _ *operationalFixture) {
				outside := filepath.Join(t.TempDir(), "outside.dump")
				if err := os.WriteFile(outside, []byte("PGDMP outside"), 0o600); err != nil {
					t.Fatal(err)
				}
				input.PostgresDump = func(_ context.Context, destination string) error { return os.Symlink(outside, destination) }
			},
			want: ErrOperationalPackageInvalid,
		},
		{
			name: "source link",
			mutate: func(input *OperationalPackageCreateInput, fixture *operationalFixture) {
				link := filepath.Join(fixture.sourceRoot, "service-link")
				if err := os.Symlink(fixture.serviceConfig, link); err != nil {
					t.Fatal(err)
				}
				input.ServiceConfig = link
			},
			want: ErrOperationalPackageInvalid,
		},
		{
			name: "path escape package identity",
			mutate: func(input *OperationalPackageCreateInput, _ *operationalFixture) {
				input.PackageID = "../escape"
			},
			want: ErrOperationalPackageInvalid,
		},
		{
			name: "linked packages root",
			mutate: func(input *OperationalPackageCreateInput, fixture *operationalFixture) {
				link := filepath.Join(filepath.Dir(fixture.packagesRoot), "packages-link")
				if err := os.Symlink(fixture.packagesRoot, link); err != nil {
					t.Fatal(err)
				}
				input.PackagesRoot = link
			},
			want: ErrOperationalPackageInvalid,
		},
		{
			name: "wrong migration head",
			mutate: func(input *OperationalPackageCreateInput, fixture *operationalFixture) {
				writeMigrationState(t, fixture.migrationState, fixture.schemaHead+1)
			},
			want: ErrOperationalPackageInvalid,
		},
		{
			name: "health exceeds fixed bound",
			mutate: func(input *OperationalPackageCreateInput, fixture *operationalFixture) {
				oversized := append([]byte(`{"status":"ok","padding":"`), bytes.Repeat([]byte("x"), int(operationalHealthMaxBytes))...)
				oversized = append(oversized, []byte(`"}`)...)
				mustWriteOperational(t, fixture.health, oversized)
				input.Health = fixture.health
			},
			want: ErrOperationalPackageInvalid,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newOperationalFixture(t)
			input := fixture.input("operational-failed", time.Date(2026, 8, 29, 10, 0, 0, 0, time.UTC))
			test.mutate(&input, fixture)
			_, err := CreateOperationalPackage(context.Background(), input)
			if err == nil {
				t.Fatal("creation succeeded, want fail-closed error")
			}
			if test.want != nil && !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want errors.Is(%v)", err, test.want)
			}
			if strings.Contains(err.Error(), fixture.secret) {
				t.Fatal("creation error leaked a forbidden value")
			}
			assertPackagesRootEmpty(t, fixture.packagesRoot)
		})
	}
}

func TestOperationalPackageCreationNeverOverwritesPublishedIdentity(t *testing.T) {
	fixture := newOperationalFixture(t)
	first := createOperationalFixture(t, fixture, "operational-stable", time.Date(2026, 8, 29, 10, 0, 0, 0, time.UTC), nil)
	before := readAllPackageFiles(t, first.PackageDir)
	_, err := CreateOperationalPackage(context.Background(), fixture.input("operational-stable", time.Date(2026, 8, 29, 11, 0, 0, 0, time.UTC)))
	if !errors.Is(err, ErrOperationalPackageExists) {
		t.Fatalf("second creation error = %v, want existing-package refusal", err)
	}
	after := readAllPackageFiles(t, first.PackageDir)
	if !bytes.Equal(before, after) {
		t.Fatal("existing published package changed after identity collision")
	}
	verification, err := VerifyOperationalPackage(context.Background(), OperationalPackageVerificationInput{
		PackageDir: first.PackageDir, ExpectedManifestSHA256: first.ManifestSHA256, ExpectedPackageID: "operational-stable",
		ForbiddenValues: []string{fixture.secret},
	})
	if err != nil || verification.Status != "succeeded" {
		t.Fatalf("existing package verification = %#v err=%v", verification, err)
	}
}

func TestOperationalPackageDetectsSourceChangeBeforePublish(t *testing.T) {
	fixture := newOperationalFixture(t)
	input := fixture.input("operational-source-change", time.Date(2026, 8, 29, 10, 0, 0, 0, time.UTC))
	input.RedactConfig = func(kind string, source []byte) ([]byte, error) {
		if kind == OperationalArtifactServiceConfig {
			if err := os.WriteFile(fixture.serviceConfig, []byte("LOOM_DB_URL=changed\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		return bytes.ReplaceAll(source, []byte(fixture.secret), []byte("[REDACTED]")), nil
	}
	_, err := CreateOperationalPackage(context.Background(), input)
	if !errors.Is(err, ErrOperationalPackageChanged) {
		t.Fatalf("error = %v, want source-changed refusal", err)
	}
	assertPackagesRootEmpty(t, fixture.packagesRoot)
}

func TestOperationalPackageVerificationFailsOnIncompleteAndTamperedArtifacts(t *testing.T) {
	tests := []struct {
		name     string
		mutate   func(*testing.T, OperationalPackageCreateResult)
		verify   func(OperationalPackageCreateResult) OperationalPackageVerificationInput
		wantCode string
	}{
		{
			name: "missing dump",
			mutate: func(t *testing.T, result OperationalPackageCreateResult) {
				if err := os.Remove(filepath.Join(result.PackageDir, "postgres.dump")); err != nil {
					t.Fatal(err)
				}
			},
			wantCode: OperationalFindingMissingArtifact,
		},
		{
			name: "missing migration evidence",
			mutate: func(t *testing.T, result OperationalPackageCreateResult) {
				if err := os.Remove(filepath.Join(result.PackageDir, "migration-state.json")); err != nil {
					t.Fatal(err)
				}
			},
			wantCode: OperationalFindingMissingArtifact,
		},
		{
			name: "artifact bytes",
			mutate: func(t *testing.T, result OperationalPackageCreateResult) {
				file, err := os.OpenFile(filepath.Join(result.PackageDir, "health.json"), os.O_APPEND|os.O_WRONLY, 0)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := file.WriteString("\n"); err != nil {
					_ = file.Close()
					t.Fatal(err)
				}
				if err := file.Close(); err != nil {
					t.Fatal(err)
				}
			},
			wantCode: OperationalFindingArtifactSizeMismatch,
		},
		{
			name: "artifact mode",
			mutate: func(t *testing.T, result OperationalPackageCreateResult) {
				if err := os.Chmod(filepath.Join(result.PackageDir, "update-state.json"), 0o640); err != nil {
					t.Fatal(err)
				}
			},
			wantCode: OperationalFindingArtifactModeMismatch,
		},
		{
			name: "manifest bytes",
			mutate: func(t *testing.T, result OperationalPackageCreateResult) {
				file, err := os.OpenFile(filepath.Join(result.PackageDir, OperationalManifestFile), os.O_APPEND|os.O_WRONLY, 0)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := file.WriteString(" "); err != nil {
					_ = file.Close()
					t.Fatal(err)
				}
				if err := file.Close(); err != nil {
					t.Fatal(err)
				}
			},
			wantCode: OperationalFindingManifestHashMismatch,
		},
		{
			name: "exact manifest identity",
			verify: func(result OperationalPackageCreateResult) OperationalPackageVerificationInput {
				return OperationalPackageVerificationInput{PackageDir: result.PackageDir, ExpectedManifestSHA256: strings.Repeat("0", 64)}
			},
			wantCode: OperationalFindingManifestIdentityMismatch,
		},
		{
			name: "wrong schema head",
			verify: func(result OperationalPackageCreateResult) OperationalPackageVerificationInput {
				head := int64(62)
				return OperationalPackageVerificationInput{PackageDir: result.PackageDir, ExpectedSchemaHead: &head}
			},
			wantCode: OperationalFindingSchemaHeadMismatch,
		},
		{
			name: "manifest path escape",
			mutate: func(t *testing.T, result OperationalPackageCreateResult) {
				manifest := readOperationalManifestForTest(t, result.PackageDir)
				manifest.Artifacts[0].Path = "../outside.dump"
				rewriteOperationalManifestForTest(t, result.PackageDir, manifest)
			},
			wantCode: OperationalFindingInvalidPath,
		},
		{
			name: "missing redaction evidence",
			mutate: func(t *testing.T, result OperationalPackageCreateResult) {
				manifest := readOperationalManifestForTest(t, result.PackageDir)
				manifest.Redaction.Policy = ""
				rewriteOperationalManifestForTest(t, result.PackageDir, manifest)
			},
			wantCode: OperationalFindingRedactionMissing,
		},
		{
			name: "false redaction totals",
			mutate: func(t *testing.T, result OperationalPackageCreateResult) {
				manifest := readOperationalManifestForTest(t, result.PackageDir)
				manifest.Redaction.TotalRedactions++
				rewriteOperationalManifestForTest(t, result.PackageDir, manifest)
			},
			wantCode: OperationalFindingRedactionMissing,
		},
		{
			name: "raw semantic secret with forged hashes",
			mutate: func(t *testing.T, result OperationalPackageCreateResult) {
				payload := []byte("TOKEN=tampered-semantic-secret\n")
				path := filepath.Join(result.PackageDir, "service-config.redacted")
				if err := os.WriteFile(path, payload, 0o600); err != nil {
					t.Fatal(err)
				}
				digest := sha256.Sum256(payload)
				hash := hex.EncodeToString(digest[:])
				manifest := readOperationalManifestForTest(t, result.PackageDir)
				for index := range manifest.Artifacts {
					if manifest.Artifacts[index].Kind == OperationalArtifactServiceConfig {
						manifest.Artifacts[index].SHA256 = hash
						manifest.Artifacts[index].SizeBytes = int64(len(payload))
					}
				}
				for index := range manifest.Redaction.Artifacts {
					if manifest.Redaction.Artifacts[index].Kind == OperationalArtifactServiceConfig {
						manifest.Redaction.Artifacts[index].OutputSHA256 = hash
					}
				}
				rewriteOperationalManifestForTest(t, result.PackageDir, manifest)
			},
			wantCode: OperationalFindingRedactionMissing,
		},
		{
			name: "linked artifact",
			mutate: func(t *testing.T, result OperationalPackageCreateResult) {
				artifact := filepath.Join(result.PackageDir, "health.json")
				outside := filepath.Join(t.TempDir(), "health.json")
				if err := os.Rename(artifact, outside); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outside, artifact); err != nil {
					t.Fatal(err)
				}
			},
			wantCode: OperationalFindingLink,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newOperationalFixture(t)
			result := createOperationalFixture(t, fixture, "operational-valid", time.Date(2026, 8, 29, 10, 0, 0, 0, time.UTC), nil)
			if test.mutate != nil {
				test.mutate(t, result)
			}
			input := OperationalPackageVerificationInput{
				PackageDir: result.PackageDir, ExpectedPackageID: "operational-valid",
				ForbiddenValues: []string{fixture.secret},
			}
			if test.verify != nil {
				input = test.verify(result)
			}
			verification, err := VerifyOperationalPackage(context.Background(), input)
			if err != nil {
				t.Fatal(err)
			}
			if verification.Status != "failed" || !hasOperationalFinding(verification.Findings, test.wantCode) {
				t.Fatalf("verification = %#v, want failed finding %q", verification, test.wantCode)
			}
			for _, finding := range verification.Findings {
				if strings.Contains(finding.Detail, fixture.secret) {
					t.Fatal("finding leaked forbidden value")
				}
			}
		})
	}
}

func TestOperationalPackageRetentionPlansLatestThreeWithoutMutation(t *testing.T) {
	fixture := newOperationalFixture(t)
	results := make([]OperationalPackageCreateResult, 0, 4)
	for index := 0; index < 4; index++ {
		results = append(results, createOperationalFixture(t, fixture, "operational-00"+string(rune('1'+index)), time.Date(2026, 8, 29, 10+index, 0, 0, 0, time.UTC), nil))
	}
	before := packageIdentities(t, fixture.packagesRoot)
	plan, err := PlanOperationalPackageRetention(context.Background(), fixture.packagesRoot, []string{fixture.secret})
	if err != nil {
		t.Fatal(err)
	}
	after := packageIdentities(t, fixture.packagesRoot)
	if len(plan.Keep) != 3 || len(plan.Remove) != 1 {
		t.Fatalf("retention plan = %#v, want latest three and one exact removal", plan)
	}
	if plan.Remove[0].PackageID != "operational-001" || plan.Remove[0].Path != results[0].PackageDir || plan.Remove[0].Reason != "older_than_latest_three_verified" {
		t.Fatalf("removal candidate = %#v, want exact oldest package", plan.Remove[0])
	}
	if len(before) != len(after) {
		t.Fatalf("package set changed during plan: before=%#v after=%#v", before, after)
	}
	for path, identity := range before {
		if after[path] != identity {
			t.Fatalf("package %s identity changed during non-mutating plan", path)
		}
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("planned package was removed: %v", err)
		}
	}
}

type operationalFixture struct {
	packagesRoot   string
	sourceRoot     string
	serviceConfig  string
	installConfig  string
	releaseConfig  string
	migrationState string
	updateState    string
	health         string
	secret         string
	schemaHead     int64
}

func newOperationalFixture(t *testing.T) *operationalFixture {
	t.Helper()
	root := t.TempDir()
	packages := filepath.Join(root, "packages")
	sources := filepath.Join(root, "sources")
	if err := os.Mkdir(packages, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(sources, 0o700); err != nil {
		t.Fatal(err)
	}
	fixture := &operationalFixture{
		packagesRoot: packages, sourceRoot: sources, secret: "fixture-secret-value", schemaHead: 61,
		serviceConfig: filepath.Join(sources, "loom.env"), installConfig: filepath.Join(sources, "install.yaml"),
		releaseConfig: filepath.Join(sources, "release.yaml"), migrationState: filepath.Join(sources, "migration.json"),
		updateState: filepath.Join(sources, "update.json"), health: filepath.Join(sources, "health.json"),
	}
	mustWriteOperational(t, fixture.serviceConfig, []byte("LOOM_DB_URL="+fixture.secret+"\nLOOM_ENV=production\n"))
	mustWriteOperational(t, fixture.installConfig, []byte("profile: main\ncredential: "+fixture.secret+"\n"))
	mustWriteOperational(t, fixture.releaseConfig, []byte("release: current\ntoken: "+fixture.secret+"\n"))
	writeMigrationState(t, fixture.migrationState, fixture.schemaHead)
	mustWriteOperational(t, fixture.updateState, []byte(`{"schema":"loom.update.state.v1","status":"idle"}`))
	mustWriteOperational(t, fixture.health, []byte(`{"status":"ok","checks":{"database":"ok"}}`))
	return fixture
}

func (fixture *operationalFixture) input(packageID string, createdAt time.Time) OperationalPackageCreateInput {
	return OperationalPackageCreateInput{
		PackagesRoot: fixture.packagesRoot, PackageID: packageID, CreatedAt: createdAt, SchemaHead: fixture.schemaHead,
		PostgresDump: func(_ context.Context, destination string) error {
			return os.WriteFile(destination, []byte("PGDMP\x01fixture-custom-dump"), 0o640)
		},
		ServiceConfig: fixture.serviceConfig, InstallConfig: fixture.installConfig, ReleaseConfig: fixture.releaseConfig,
		MigrationState: fixture.migrationState, UpdateState: fixture.updateState, Health: fixture.health,
		RedactConfig: func(_ string, source []byte) ([]byte, error) {
			return bytes.ReplaceAll(source, []byte(fixture.secret), []byte("[REDACTED]")), nil
		},
		ForbiddenValues: []string{fixture.secret},
	}
}

func createOperationalFixture(t *testing.T, fixture *operationalFixture, packageID string, createdAt time.Time, mutate func(*OperationalPackageCreateInput)) OperationalPackageCreateResult {
	t.Helper()
	input := fixture.input(packageID, createdAt)
	if mutate != nil {
		mutate(&input)
	}
	result, err := CreateOperationalPackage(context.Background(), input)
	if err != nil {
		t.Fatalf("CreateOperationalPackage: %v", err)
	}
	return result
}

func writeMigrationState(t *testing.T, path string, head int64) {
	t.Helper()
	raw, err := json.Marshal(OperationalMigrationState{Schema: OperationalMigrationStateSchema, CurrentVersion: head, LatestVersion: head, Status: "ok"})
	if err != nil {
		t.Fatal(err)
	}
	mustWriteOperational(t, path, raw)
}

func mustWriteOperational(t *testing.T, path string, payload []byte) {
	t.Helper()
	if err := os.WriteFile(path, payload, 0o600); err != nil {
		t.Fatal(err)
	}
}

func assertPackagesRootEmpty(t *testing.T, root string) {
	t.Helper()
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("failed creation left package entries: %#v", entries)
	}
}

func readAllPackageFiles(t *testing.T, root string) []byte {
	t.Helper()
	var result []byte
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(root, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		result = append(result, raw...)
	}
	return result
}

func readOperationalManifestForTest(t *testing.T, packageDir string) OperationalPackageManifest {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(packageDir, OperationalManifestFile))
	if err != nil {
		t.Fatal(err)
	}
	var manifest OperationalPackageManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	return manifest
}

func rewriteOperationalManifestForTest(t *testing.T, packageDir string, manifest OperationalPackageManifest) {
	t.Helper()
	raw, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	raw = append(raw, '\n')
	manifestPath := filepath.Join(packageDir, OperationalManifestFile)
	if err := os.WriteFile(manifestPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(manifestPath, 0o600); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(raw)
	hashLine := hex.EncodeToString(digest[:]) + "  " + OperationalManifestFile + "\n"
	hashPath := filepath.Join(packageDir, OperationalManifestHashFile)
	if err := os.WriteFile(hashPath, []byte(hashLine), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(hashPath, 0o600); err != nil {
		t.Fatal(err)
	}
}

func hasOperationalFinding(findings []OperationalPackageFinding, code string) bool {
	for _, finding := range findings {
		if finding.Code == code {
			return true
		}
	}
	return false
}

func packageIdentities(t *testing.T, root string) map[string]uint64 {
	t.Helper()
	result := map[string]uint64{}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		path := filepath.Join(root, entry.Name())
		info, err := os.Lstat(path)
		if err != nil {
			t.Fatal(err)
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok {
			t.Fatalf("missing stat identity for %s", path)
		}
		result[path] = uint64(stat.Ino)
	}
	return result
}
