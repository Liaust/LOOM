package provenance

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSealRecoveryDumpNormalizesProducerModeBeforeHash(t *testing.T) {
	path := filepath.Join(t.TempDir(), RecoveryDumpFile)
	if err := os.WriteFile(path, []byte("PGDMP-provenance"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := sealRecoveryDump(path); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("sealed dump mode = %04o, want 0600", info.Mode().Perm())
	}
	if _, _, prefix, err := hashRecoveryFile(context.Background(), path, RecoveryDumpMaxBytes); err != nil || string(prefix) != "PGDMP" {
		t.Fatalf("hash sealed dump prefix=%q err=%v", prefix, err)
	}
}

func TestSealRecoveryDumpRejectsSymlinkWithoutChangingExternalTarget(t *testing.T) {
	root := t.TempDir()
	external := filepath.Join(t.TempDir(), "external.dump")
	if err := os.WriteFile(external, []byte("PGDMP-external"), 0o640); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, RecoveryDumpFile)
	if err := os.Symlink(external, path); err != nil {
		t.Fatal(err)
	}
	if err := sealRecoveryDump(path); err == nil || !strings.Contains(err.Error(), "no-follow regular file") {
		t.Fatalf("symlink seal error = %v", err)
	}
	info, err := os.Lstat(external)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o640 {
		t.Fatalf("external target mode changed to %04o", info.Mode().Perm())
	}
}

func TestSealRecoveryDumpRejectsNamedPathReplacement(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, RecoveryDumpFile)
	held := path + ".held"
	if err := os.WriteFile(path, []byte("PGDMP-original"), 0o640); err != nil {
		t.Fatal(err)
	}
	err := sealRecoveryDumpWithHook(path, func() error {
		if err := os.Rename(path, held); err != nil {
			return err
		}
		return os.WriteFile(path, []byte("PGDMP-replacement"), 0o640)
	})
	if err == nil || !strings.Contains(err.Error(), "path changed while sealing") {
		t.Fatalf("replacement seal error = %v", err)
	}
	replacement, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if replacement.Mode().Perm() != 0o640 {
		t.Fatalf("replacement target mode changed to %04o", replacement.Mode().Perm())
	}
	sealed, err := os.Lstat(held)
	if err != nil {
		t.Fatal(err)
	}
	if sealed.Mode().Perm() != 0o600 {
		t.Fatalf("held producer inode mode = %04o, want 0600", sealed.Mode().Perm())
	}
}

func TestHashRecoveryFileRequiresNoFollowExact0600AndStableName(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, RecoveryDumpFile)
	if err := os.WriteFile(path, []byte("PGDMP-original"), 0o640); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := hashRecoveryFile(context.Background(), path, RecoveryDumpMaxBytes); err == nil || !strings.Contains(err.Error(), "exact mode 0600") {
		t.Fatalf("0640 hash error = %v", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	held := path + ".held"
	_, _, _, err := hashRecoveryFileWithHook(context.Background(), path, RecoveryDumpMaxBytes, func() error {
		if err := os.Rename(path, held); err != nil {
			return err
		}
		return os.WriteFile(path, []byte("PGDMP-replacement"), 0o600)
	})
	if err == nil || !strings.Contains(err.Error(), "changed while hashing") {
		t.Fatalf("replacement hash error = %v", err)
	}
}
