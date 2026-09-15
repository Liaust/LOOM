package storagearchive

import (
	"context"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func TestWorkspaceManifestCredentialExactSystemdReaderACL(t *testing.T) {
	// Metadata from Main's systemd LoadCredential publication; no key bytes.
	acl, err := hex.DecodeString("0200000001000400ffffffff02000400de03000004000000ffffffff10000400ffffffff20000000ffffffff")
	if err != nil {
		t.Fatal(err)
	}
	identity := systemdCredentialIdentity{mode: unix.S_IFREG | 0o440, uid: 0, gid: 0, links: 1, size: 64}
	if !exactSystemdManifestReaderACL(identity, acl, 990) {
		t.Fatal("exact systemd root-owned, service-only read ACL was refused")
	}
	for _, uid := range []uint32{0, 1, 989, 991, ^uint32(0)} {
		if exactSystemdManifestReaderACL(identity, acl, uid) {
			t.Fatalf("wrong service UID %d accepted", uid)
		}
	}
	// Every header, permission, identity, tag and ordering byte is protected.
	for i := range acl {
		for bit := byte(1); bit != 0; bit <<= 1 {
			changed := append([]byte(nil), acl...)
			changed[i] ^= bit
			if exactSystemdManifestReaderACL(identity, changed, 990) {
				t.Fatalf("ACL drift at byte %d bit %d accepted", i, bit)
			}
		}
	}
	for _, changed := range [][]byte{nil, acl[:len(acl)-1], append(append([]byte(nil), acl...), acl[12:20]...)} {
		if exactSystemdManifestReaderACL(identity, changed, 990) {
			t.Fatal("missing, truncated, or extra-reader ACL accepted")
		}
	}
	for _, mutate := range []func(*systemdCredentialIdentity){
		func(i *systemdCredentialIdentity) { i.uid = 990 },
		func(i *systemdCredentialIdentity) { i.gid = 990 },
		func(i *systemdCredentialIdentity) { i.mode = unix.S_IFDIR | 0o440 },
		func(i *systemdCredentialIdentity) { i.mode = unix.S_IFLNK | 0o440 },
		func(i *systemdCredentialIdentity) { i.mode |= 0o200 },
		func(i *systemdCredentialIdentity) { i.mode |= 0o004 },
		func(i *systemdCredentialIdentity) { i.mode |= 0o100 },
		func(i *systemdCredentialIdentity) { i.mode |= 0o4000 },
	} {
		changed := identity
		mutate(&changed)
		if exactSystemdManifestReaderACL(changed, acl, 990) {
			t.Fatal("unsafe ownership, type, or mode accepted")
		}
	}
	if err := validateManifestCredentialAccess(-1, identity); err == nil {
		t.Fatal("unreadable ACL accepted")
	}
}

func TestWorkspaceManifestCredentialReturnsPinnedPrivateCopies(t *testing.T) {
	directory, encoded, decoded := writeWorkspaceManifestCredential(t)
	provider, err := NewSystemdWorkspaceManifestKeyProvider(directory, "workspace-archive-test-v1")
	if err != nil {
		t.Fatal(err)
	}
	first, err := provider.Lookup(context.Background(), "workspace-archive-test-v1")
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(decoded) || string(first) == encoded {
		t.Fatal("credential was not decoded to the exact 32-byte key")
	}
	first[0] ^= 0xff
	second, err := provider.Lookup(context.Background(), "workspace-archive-test-v1")
	if err != nil {
		t.Fatal(err)
	}
	if string(second) != string(decoded) {
		t.Fatal("caller mutation changed the provider's pinned key")
	}
	if _, err := provider.Lookup(context.Background(), "workspace-archive-other-v1"); err == nil {
		t.Fatal("provider accepted an unknown key ID")
	}
}

func TestWorkspaceManifestCredentialRejectsFilesystemAndEncodingAttacks(t *testing.T) {
	valid := workspaceManifestTestHex()
	tests := []struct {
		name    string
		prepare func(*testing.T, string)
	}{
		{"missing", func(*testing.T, string) {}},
		{"symlink", func(t *testing.T, dir string) {
			target := filepath.Join(t.TempDir(), "target")
			writeManifestCredentialFile(t, target, valid, 0o400)
			if err := os.Symlink(target, filepath.Join(dir, WorkspaceArchiveManifestCredentialName)); err != nil {
				t.Fatal(err)
			}
		}},
		{"hard_link", func(t *testing.T, dir string) {
			target := filepath.Join(t.TempDir(), "target")
			writeManifestCredentialFile(t, target, valid, 0o400)
			if err := os.Link(target, filepath.Join(dir, WorkspaceArchiveManifestCredentialName)); err != nil {
				t.Fatal(err)
			}
		}},
		{"directory", func(t *testing.T, dir string) {
			if err := os.Mkdir(filepath.Join(dir, WorkspaceArchiveManifestCredentialName), 0o400); err != nil {
				t.Fatal(err)
			}
		}},
		{"permissive_mode", func(t *testing.T, dir string) {
			writeManifestCredentialFile(t, filepath.Join(dir, WorkspaceArchiveManifestCredentialName), valid, 0o440)
		}},
		{"truncated", func(t *testing.T, dir string) {
			writeManifestCredentialFile(t, filepath.Join(dir, WorkspaceArchiveManifestCredentialName), valid[:62], 0o400)
		}},
		{"trailing_newline", func(t *testing.T, dir string) {
			writeManifestCredentialFile(t, filepath.Join(dir, WorkspaceArchiveManifestCredentialName), valid+"\n", 0o400)
		}},
		{"uppercase", func(t *testing.T, dir string) {
			writeManifestCredentialFile(t, filepath.Join(dir, WorkspaceArchiveManifestCredentialName), strings.ToUpper(valid), 0o400)
		}},
		{"weak", func(t *testing.T, dir string) {
			writeManifestCredentialFile(t, filepath.Join(dir, WorkspaceArchiveManifestCredentialName), strings.Repeat("00", 32), 0o400)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			directory := t.TempDir()
			var err error
			directory, err = filepath.EvalSymlinks(directory)
			if err != nil {
				t.Fatal(err)
			}
			test.prepare(t, directory)
			provider, err := NewSystemdWorkspaceManifestKeyProvider(directory, "workspace-archive-test-v1")
			if err != nil {
				t.Fatal(err)
			}
			if key, err := provider.Lookup(context.Background(), "workspace-archive-test-v1"); err == nil {
				t.Fatalf("unsafe credential accepted: %x", key)
			}
		})
	}
}

func TestWorkspaceManifestCredentialRejectsPathAliasesAndReplacement(t *testing.T) {
	directory, _, _ := writeWorkspaceManifestCredential(t)
	for _, path := range []string{"relative", directory + string(filepath.Separator) + ".", directory + string(filepath.Separator) + ".." + string(filepath.Separator) + filepath.Base(directory)} {
		if _, err := NewSystemdWorkspaceManifestKeyProvider(path, "workspace-archive-test-v1"); err == nil {
			t.Fatalf("credential directory alias accepted: %q", path)
		}
	}
	linkParent, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(linkParent, "credentials-link")
	if err := os.Symlink(directory, link); err != nil {
		t.Fatal(err)
	}
	provider, err := NewSystemdWorkspaceManifestKeyProvider(link, "workspace-archive-test-v1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Lookup(context.Background(), "workspace-archive-test-v1"); err == nil {
		t.Fatal("symlinked credential directory was accepted")
	}

	provider, err = NewSystemdWorkspaceManifestKeyProvider(directory, "workspace-archive-test-v1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Lookup(context.Background(), "workspace-archive-test-v1"); err != nil {
		t.Fatal(err)
	}
	credential := filepath.Join(directory, WorkspaceArchiveManifestCredentialName)
	if err := os.Remove(credential); err != nil {
		t.Fatal(err)
	}
	replacement := hex.EncodeToString([]byte{31, 30, 29, 28, 27, 26, 25, 24, 23, 22, 21, 20, 19, 18, 17, 16, 15, 14, 13, 12, 11, 10, 9, 8, 7, 6, 5, 4, 3, 2, 1, 0})
	writeManifestCredentialFile(t, credential, replacement, 0o400)
	if _, err := provider.Lookup(context.Background(), "workspace-archive-test-v1"); err == nil {
		t.Fatal("credential replacement was accepted after process binding")
	}
}

func writeWorkspaceManifestCredential(t *testing.T) (string, string, []byte) {
	t.Helper()
	directory := t.TempDir()
	directory, err := filepath.EvalSymlinks(directory)
	if err != nil {
		t.Fatal(err)
	}
	encoded := workspaceManifestTestHex()
	decoded, err := hex.DecodeString(encoded)
	if err != nil {
		t.Fatal(err)
	}
	writeManifestCredentialFile(t, filepath.Join(directory, WorkspaceArchiveManifestCredentialName), encoded, 0o400)
	return directory, encoded, decoded
}

func workspaceManifestTestHex() string {
	return "000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f"
}

func writeManifestCredentialFile(t *testing.T, path, payload string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(payload), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}
