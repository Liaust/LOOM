package backupstrategy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const operationalVerificationReceiptSchema = "loom.operational_verification_receipt.v1"

type operationalVerificationReceipt struct {
	Schema       string                         `json:"schema"`
	VerifiedAt   time.Time                      `json:"verified_at"`
	Identity     string                         `json:"filesystem_identity"`
	Verification OperationalPackageVerification `json:"verification"`
}

// VerifyAndRecordOperationalPackage is an explicit deep verification, unlike
// read-only retention planning. Receipts live outside immutable package trees.
func VerifyAndRecordOperationalPackage(ctx context.Context, input OperationalPackageVerificationInput) (OperationalPackageVerification, error) {
	before, _, err := retentionIdentity(input.PackageDir)
	if err != nil {
		return OperationalPackageVerification{}, err
	}
	verified, err := VerifyOperationalPackage(ctx, input)
	if err != nil || verified.Status != "succeeded" {
		return verified, err
	}
	after, _, err := retentionIdentity(input.PackageDir)
	if err != nil || before != after {
		return verified, fmt.Errorf("%w: package changed during verification", ErrOperationalPackageChanged)
	}
	return verified, recordOperationalVerification(ctx, verified)
}

func verificationReceiptRoot(packageRoot string) string {
	return packageRoot + ".verification-receipts"
}

func checkVerificationCustody(path string, owner uint64, directory bool) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	uid, ok := statUint(info.Sys(), "Uid")
	nlink, linkOK := statUint(info.Sys(), "Nlink")
	mode := os.FileMode(0600)
	if directory {
		mode = 0700
	}
	if !ok || (uid != 0 && uid != owner) || info.IsDir() != directory || info.Mode().Perm() != mode ||
		(!directory && (!info.Mode().IsRegular() || !linkOK || nlink != 1)) {
		return fmt.Errorf("verification receipt custody is not trusted")
	}
	return nil
}

func verificationRootOwner(packageRoot string) (uint64, error) {
	if err := requireRealDirectory(packageRoot); err != nil {
		return 0, err
	}
	info, err := os.Lstat(packageRoot)
	if err != nil {
		return 0, err
	}
	uid, ok := statUint(info.Sys(), "Uid")
	if !ok || info.Mode().Perm()&0022 != 0 {
		return 0, fmt.Errorf("operational package root custody is not trusted")
	}
	return uid, nil
}

func recordOperationalVerification(ctx context.Context, verified OperationalPackageVerification) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if verified.Status != "succeeded" || !validPackageID(verified.PackageID) ||
		filepath.Base(verified.PackageDir) != verified.PackageID || normalizeSHA256(verified.ManifestSHA256) == "" {
		return fmt.Errorf("only a verified published package can have a receipt")
	}
	packageRoot := filepath.Dir(verified.PackageDir)
	owner, err := verificationRootOwner(packageRoot)
	if err != nil {
		return err
	}
	root := verificationReceiptRoot(packageRoot)
	if err := os.Mkdir(root, 0700); err != nil {
		if !os.IsExist(err) {
			return err
		}
	} else if os.Geteuid() == 0 && owner != 0 {
		info, err := os.Stat(packageRoot)
		if err != nil {
			return err
		}
		group, ok := statUint(info.Sys(), "Gid")
		if !ok {
			return fmt.Errorf("operational package root group is unavailable")
		}
		// Operator bootstrap must not leave a root-only cache that the normal
		// package producer cannot subsequently maintain.
		if err := os.Chown(root, int(owner), int(group)); err != nil {
			return err
		}
	}
	if err := checkVerificationCustody(root, owner, true); err != nil {
		return err
	}
	identity, _, err := retentionIdentity(verified.PackageDir)
	if err != nil {
		return err
	}
	receipt := operationalVerificationReceipt{Schema: operationalVerificationReceiptSchema, VerifiedAt: time.Now().UTC(), Identity: identity, Verification: verified}
	raw, err := json.Marshal(receipt)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(root, ".verification-")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if os.Geteuid() == 0 && owner != 0 {
		info, err := os.Stat(packageRoot)
		if err != nil {
			tmp.Close()
			return err
		}
		group, ok := statUint(info.Sys(), "Gid")
		if !ok {
			tmp.Close()
			return fmt.Errorf("operational package root group is unavailable")
		}
		if err := tmp.Chown(int(owner), int(group)); err != nil {
			tmp.Close()
			return err
		}
	}
	if _, err = tmp.Write(raw); err == nil {
		err = tmp.Sync()
	}
	closeErr := tmp.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err := os.Rename(tmp.Name(), filepath.Join(root, verified.PackageID+".json")); err != nil {
		return err
	}
	return syncRetentionDirectory(root)
}

// This answers whether a prior verification still covers unchanged file
// identities. It is not fresh cryptographic verification or a restore check.
func inspectOperationalVerificationReceipt(ctx context.Context, packageDir string) (OperationalPackageVerification, error) {
	var receipt operationalVerificationReceipt
	if err := ctx.Err(); err != nil {
		return receipt.Verification, err
	}
	id := filepath.Base(packageDir)
	if !validPackageID(id) {
		return receipt.Verification, fmt.Errorf("invalid operational package identity")
	}
	owner, err := verificationRootOwner(filepath.Dir(packageDir))
	if err != nil {
		return receipt.Verification, err
	}
	root := verificationReceiptRoot(filepath.Dir(packageDir))
	if err := checkVerificationCustody(root, owner, true); err != nil {
		return receipt.Verification, err
	}
	path := filepath.Join(root, id+".json")
	if err := checkVerificationCustody(path, owner, false); err != nil {
		return receipt.Verification, err
	}
	file, err := inspectStableFile(path, 64<<10, true)
	if err != nil {
		return receipt.Verification, err
	}
	decoder := json.NewDecoder(bytes.NewReader(file.Raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&receipt); err != nil {
		return receipt.Verification, err
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return receipt.Verification, err
	}
	v := receipt.Verification
	if receipt.Schema != operationalVerificationReceiptSchema || receipt.VerifiedAt.IsZero() ||
		v.Status != "succeeded" || len(v.Findings) != 0 || v.PackageDir != packageDir || v.PackageID != id ||
		v.CreatedAt.IsZero() || normalizeSHA256(v.ManifestSHA256) == "" {
		return v, fmt.Errorf("invalid operational verification receipt")
	}
	identity, _, err := retentionIdentity(packageDir)
	if err != nil || identity != receipt.Identity {
		return v, fmt.Errorf("operational package changed since verification")
	}
	return v, nil
}
