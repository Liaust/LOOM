package backupstrategy

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestVerificationReceiptHoldsHistoryUntilExplicitVerification(t *testing.T) {
	f := newOperationalFixture(t)
	created := createOperationalFixture(t, f, "operational-old", time.Now(), nil)
	ctx := context.Background()
	path := filepath.Join(verificationReceiptRoot(f.packagesRoot), "operational-old.json")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	p, err := PlanLocalOperationalRetention(ctx, f.packagesRoot, nil)
	if err != nil || len(p.Selection.Findings) != 1 || len(p.Selection.Keep) != 0 {
		t.Fatalf("missing receipt not held: %+v err=%v", p, err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("read-only planning created a verification receipt")
	}
	if pinned, err := PlanLocalOperationalRetention(ctx, f.packagesRoot, map[string]string{"operational-old": "rollback"}); err != nil || len(pinned.Selection.Remove) != 0 || len(pinned.Selection.Findings) == 0 {
		t.Fatalf("unverified pinned history should be inspectable and held: %+v %v", pinned, err)
	}
	verified, err := VerifyAndRecordOperationalPackage(ctx, OperationalPackageVerificationInput{PackageDir: created.PackageDir})
	if err != nil || verified.Status != "succeeded" {
		t.Fatalf("explicit verification failed: %+v %v", verified, err)
	}
	p, err = PlanLocalOperationalRetention(ctx, f.packagesRoot, nil)
	if err != nil || len(p.Selection.Keep) != 1 || len(p.Selection.Findings) != 0 {
		t.Fatalf("verified package not recognized: %+v %v", p, err)
	}
}

func TestVerificationReceiptInvalidatesSameSizeRewriteWithRestoredMtime(t *testing.T) {
	f := newOperationalFixture(t)
	created := createOperationalFixture(t, f, "operational-old", time.Now(), nil)
	path := filepath.Join(created.PackageDir, "postgres.dump")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	raw[len(raw)-1] ^= 1
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	if _, err := inspectOperationalVerificationReceipt(context.Background(), created.PackageDir); err == nil {
		t.Fatal("same-size content rewrite with restored mtime accepted")
	}
}

func TestVerificationReceiptRejectsChangedCustodyAndReplacement(t *testing.T) {
	for _, kind := range []string{"permissions", "symlink", "inode"} {
		t.Run(kind, func(t *testing.T) {
			f := newOperationalFixture(t)
			created := createOperationalFixture(t, f, "operational-old", time.Now(), nil)
			path := filepath.Join(verificationReceiptRoot(f.packagesRoot), "operational-old.json")
			switch kind {
			case "permissions":
				if err := os.Chmod(path, 0644); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Rename(path, path+".saved"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(path+".saved", path); err != nil {
					t.Fatal(err)
				}
			case "inode":
				dump := filepath.Join(created.PackageDir, "postgres.dump")
				raw, err := os.ReadFile(dump)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(dump+".new", raw, 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(dump+".new", dump); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := inspectOperationalVerificationReceipt(context.Background(), created.PackageDir); err == nil {
				t.Fatal("changed custody or package identity accepted")
			}
		})
	}
}

func TestRetentionReceiptInspectionDoesNotPerformDeepVerification(t *testing.T) {
	f := newOperationalFixture(t)
	created := createOperationalFixture(t, f, "operational-one", time.Now(), nil)
	verified, err := inspectOperationalVerificationReceipt(context.Background(), created.PackageDir)
	if err != nil || verified.Status != "succeeded" || verified.Manifest != nil {
		t.Fatalf("expected saved verification metadata, not a freshly decoded package: %+v %v", verified, err)
	}
	if info, err := os.Stat(filepath.Join(verificationReceiptRoot(f.packagesRoot), "operational-one.json")); err != nil || info.Size() > 64<<10 {
		t.Fatalf("receipt is not bounded metadata: %+v %v", info, err)
	}
}

func TestOperatorVerificationPreservesNormalProducerReceiptOwnership(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("root-only cache bootstrap ownership check")
	}
	f := newOperationalFixture(t)
	created := createOperationalFixture(t, f, "operational-one", time.Now(), nil)
	root := verificationReceiptRoot(f.packagesRoot)
	if err := os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(f.packagesRoot, 65534, 65534); err != nil {
		t.Fatal(err)
	}
	verified, err := VerifyAndRecordOperationalPackage(context.Background(), OperationalPackageVerificationInput{PackageDir: created.PackageDir})
	if err != nil || verified.Status != "succeeded" {
		t.Fatalf("operator verification: %+v %v", verified, err)
	}
	for _, path := range []string{root, filepath.Join(root, "operational-one.json")} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Sys().(*syscall.Stat_t).Uid != 65534 {
			t.Fatalf("operator left normal producer unable to use %s", path)
		}
	}
}
