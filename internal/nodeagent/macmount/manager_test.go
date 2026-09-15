package macmount

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestSMBURL(t *testing.T) {
	t.Run("without password", func(t *testing.T) {
		got := smbURL("loomshare", "", "loom-storage", "loom-storage")
		want := "//loomshare@loom-storage/loom-storage"
		if got != want {
			t.Fatalf("smbURL() = %q, want %q", got, want)
		}
	})

	t.Run("with escaped password and share", func(t *testing.T) {
		got := smbURL("loomshare", "p@ss word/with:symbols", "loom-storage", "LOOM Main")
		want := "//loomshare:p%40ss%20word%2Fwith%3Asymbols@loom-storage/LOOM%20Main"
		if got != want {
			t.Fatalf("smbURL() = %q, want %q", got, want)
		}
	})
}

func TestDefaultPolicySeparatesStorageBoxAndCloudIdentities(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	for _, key := range []string{
		"LOOM_MAIN_STORAGE_MOUNT", "LOOM_MAIN_SMB_SHARE", "LOOM_MAIN_SMB_FINDER_MOUNT",
		"LOOM_MAIN_BOX_MOUNT", "LOOM_MAIN_BOX_SMB_SHARE", "LOOM_MAIN_BOX_SMB_FINDER_MOUNT",
		"LOOM_CLOUD_FOLDER_MOUNT", "LOOM_CLOUD_FOLDER_SMB_SHARE", "LOOM_CLOUD_FOLDER_SMB_FINDER_MOUNT",
	} {
		t.Setenv(key, "")
	}

	policy := DefaultPolicy(DesiredMounted)
	if policy.SchemaVersion != SchemaVersion {
		t.Fatalf("schema = %q, want %q", policy.SchemaVersion, SchemaVersion)
	}
	if policy.MainStorage.DesiredState != DesiredMounted {
		t.Fatalf("storage desired = %q", policy.MainStorage.DesiredState)
	}
	if policy.MainBox.DesiredState != DesiredUnmounted {
		t.Fatalf("main box desired = %q, want optional/unmounted", policy.MainBox.DesiredState)
	}
	if policy.MainStorage.Share != "loom-storage" || policy.MainBox.Share != "loom-main-box" {
		t.Fatalf("shares = storage %q box %q", policy.MainStorage.Share, policy.MainBox.Share)
	}
	if policy.MainStorage.MountPath == policy.MainBox.MountPath || policy.MainStorage.FinderMountPath == policy.MainBox.FinderMountPath {
		t.Fatalf("storage and box mount identities collide: %#v", policy)
	}
	if policy.CloudStorage.MountPath != policy.CloudStorage.FinderMountPath {
		t.Fatalf("SMB cloud should use the real Finder mount path: %#v", policy.CloudStorage)
	}
	if err := validatePolicyMountIdentities(policy); err != nil {
		t.Fatalf("default policy invalid: %v", err)
	}
}

func TestNormalizeLegacyPolicyAddsOptionalMainBox(t *testing.T) {
	legacy := `{
		"schema_version":"loom.storage.mount_policy.v0.6.3",
		"main_storage":{"desired_state":"mounted","protocol":"smb","mount_path":"/tmp/loom-storage","finder_mount_path":"/Volumes/loom-storage","host":"loom-storage","expected_ip":"10.44.0.2","share":"loom-storage","user":"loomshare"},
		"cloud_storage":{"desired_state":"unmounted","protocol":"smb","mount_path":"/tmp/loom-cloud","finder_mount_path":"/Volumes/backup","host":"cloud.example","share":"backup","user":"cloud-user"}
	}`
	var policy Policy
	if err := json.Unmarshal([]byte(legacy), &policy); err != nil {
		t.Fatal(err)
	}
	policy = NormalizePolicy(policy)
	if policy.SchemaVersion != SchemaVersion {
		t.Fatalf("schema = %q", policy.SchemaVersion)
	}
	if policy.MainBox.Share != defaultMainBoxSMBShare || policy.MainBox.DesiredState != DesiredUnmounted {
		t.Fatalf("legacy main box = %#v", policy.MainBox)
	}
	if policy.CloudStorage.MountPath != "/Volumes/backup" {
		t.Fatalf("legacy SMB cloud path was not normalized to the real Finder mount: %#v", policy.CloudStorage)
	}
	if err := validatePolicyMountIdentities(policy); err != nil {
		t.Fatalf("normalized legacy policy invalid: %v", err)
	}
}

func TestDefaultRcloneCloudKeepsRealHomeMount(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("LOOM_CLOUD_FOLDER_PROTOCOL", "rclone")
	t.Setenv("LOOM_CLOUD_FOLDER_MOUNT", "")

	policy := DefaultPolicy(DesiredUnmounted)
	want := filepath.Join(home, "loom-cloud")
	if policy.CloudStorage.MountPath != want {
		t.Fatalf("rclone cloud mount = %q, want %q", policy.CloudStorage.MountPath, want)
	}
}

func TestEnsureCloudFinderSubpathCreatesRealDirectory(t *testing.T) {
	root := t.TempDir()
	policy := DefaultPolicy(DesiredUnmounted)
	policy.CloudStorage.FinderMountPath = root
	policy.CloudStorage.FinderSubpath = "loom-cloud"

	if err := ensureCloudFinderSubpath(policy); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "loom-cloud")
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		t.Fatalf("Finder cloud path is not a real directory: %v", info.Mode())
	}
}

func TestValidatePolicyMountIdentitiesRejectsLocalAndRemoteCollisions(t *testing.T) {
	base := DefaultPolicy(DesiredMounted)

	t.Run("storage and box local path", func(t *testing.T) {
		policy := base
		policy.MainBox.MountPath = policy.MainStorage.MountPath
		err := validatePolicyMountIdentities(policy)
		if err == nil || !strings.Contains(err.Error(), "mount path collision") {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("box and cloud Finder path", func(t *testing.T) {
		policy := base
		policy.CloudStorage.FinderMountPath = policy.MainBox.FinderMountPath
		err := validatePolicyMountIdentities(policy)
		if err == nil || !strings.Contains(err.Error(), "mount path collision") {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("relative direct cloud path", func(t *testing.T) {
		policy := base
		policy.CloudStorage.MountPath = "relative/cloud"
		err := validatePolicyMountIdentities(policy)
		if err == nil || !strings.Contains(err.Error(), "absolute and clean") {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("storage and box remote share", func(t *testing.T) {
		policy := base
		policy.MainBox.Host = policy.MainStorage.Host
		policy.MainBox.Share = policy.MainStorage.Share
		err := validatePolicyMountIdentities(policy)
		if err == nil || !strings.Contains(err.Error(), "SMB identity collision") {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("host aliases with the same expected IP and share", func(t *testing.T) {
		policy := base
		policy.MainBox.Host = "loom-main-box-alias"
		policy.MainBox.ExpectedIP = policy.MainStorage.ExpectedIP
		policy.MainBox.Share = policy.MainStorage.Share
		err := validatePolicyMountIdentities(policy)
		if err == nil || !strings.Contains(err.Error(), "SMB identity collision") {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("distinct main SMB users", func(t *testing.T) {
		policy := base
		policy.MainBox.User = "another-user"
		err := validatePolicyMountIdentities(policy)
		if err == nil || !strings.Contains(err.Error(), "same dedicated SMB user") {
			t.Fatalf("error = %v", err)
		}
	})
}

func TestStatusIncludesOptionalMainBoxIdentity(t *testing.T) {
	dir := t.TempDir()
	manager := Manager{
		PolicyPath: filepath.Join(dir, "policy.json"),
		StatusPath: filepath.Join(dir, "status.json"),
	}
	policy := DefaultPolicy(DesiredUnmounted)
	policy.MainBox.Host = "127.0.0.1"
	policy.MainBox.ExpectedIP = "127.0.0.1"
	policy.MainBox.MountPath = filepath.Join(dir, "main-box")
	policy.MainBox.FinderMountPath = filepath.Join(dir, "main-box-finder")
	policy.MainStorage.MountPath = filepath.Join(dir, "storage")
	policy.MainStorage.FinderMountPath = filepath.Join(dir, "storage-finder")
	policy.CloudStorage.MountPath = filepath.Join(dir, "cloud")
	policy.CloudStorage.FinderMountPath = filepath.Join(dir, "cloud-finder")
	if err := manager.SavePolicy(policy); err != nil {
		t.Fatal(err)
	}
	status, err := manager.Status(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if status.MainBox == nil {
		t.Fatal("status omitted optional main Box identity")
	}
	if status.MainBox.Share != "loom-main-box" || status.MainBox.MountPath != policy.MainBox.MountPath {
		t.Fatalf("main Box status = %#v", status.MainBox)
	}
}

func TestSavePolicyRejectsCollisionBeforeWriting(t *testing.T) {
	dir := t.TempDir()
	manager := Manager{PolicyPath: filepath.Join(dir, "policy.json")}
	policy := DefaultPolicy(DesiredMounted)
	policy.MainBox.FinderMountPath = policy.CloudStorage.FinderMountPath
	if err := manager.SavePolicy(policy); err == nil {
		t.Fatal("SavePolicy accepted colliding mount paths")
	}
	if _, err := os.Stat(manager.PolicyPath); !os.IsNotExist(err) {
		t.Fatalf("invalid policy was written, err=%v", err)
	}
}

func TestLoadPolicyRejectsEnvironmentCollisionWithoutPolicyFile(t *testing.T) {
	dir := t.TempDir()
	colliding := filepath.Join(dir, "collision")
	t.Setenv("LOOM_MAIN_STORAGE_MOUNT", colliding)
	t.Setenv("LOOM_MAIN_BOX_MOUNT", colliding)
	manager := Manager{PolicyPath: filepath.Join(dir, "missing-policy.json")}
	if _, err := manager.LoadPolicy(); err == nil || !strings.Contains(err.Error(), "mount path collision") {
		t.Fatalf("LoadPolicy error = %v", err)
	}
}

func TestMountedSMBPathsRequireExactRemoteIdentity(t *testing.T) {
	policy := MainStoragePolicy{
		MountPath:       "/Users/test/loom-storage",
		FinderMountPath: "/Volumes/loom-storage",
		Host:            "loom-storage",
		Share:           "loom-storage",
		User:            "loomshare",
	}
	output := strings.Join([]string{
		"//loomshare@loom-storage/loom-storage on /Volumes/loom-storage (smbfs, nodev, nosuid)",
		"//cloud-user@cloud.example/backup on /Users/test/loom-storage (smbfs, nodev, nosuid)",
		"//loomshare@loom-storage/loom-main-box on /Volumes/loom-main-box (smbfs, nodev, nosuid)",
	}, "\n")
	if got, want := mountedSMBPathsFromOutput(output, policy), []string{"/Volumes/loom-storage"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("mounted paths = %#v, want %#v", got, want)
	}
	conflicts := mountIdentityConflictsFromOutput(output, policy)
	if len(conflicts) != 1 || conflicts[0].Target != policy.MountPath || !strings.Contains(conflicts[0].Source, "cloud.example") {
		t.Fatalf("conflicts = %#v", conflicts)
	}
}

func TestMountIdentityParserPreservesPathsWithSpaces(t *testing.T) {
	policy := MainStoragePolicy{MountPath: "/Volumes/LOOM Main Box", FinderMountPath: "/Volumes/LOOM Main Box", Host: "loom-storage", Share: "LOOM Main", User: "loomshare"}
	output := "//loomshare@loom-storage/LOOM%20Main on /Volumes/LOOM Main Box (smbfs, nodev)\n"
	if got := mountedSMBPathsFromOutput(output, policy); !reflect.DeepEqual(got, []string{"/Volumes/LOOM Main Box"}) {
		t.Fatalf("mounted paths = %#v", got)
	}
}

func TestEnsureEmptyMountPathRejectsSymlinkAndPayload(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	symlink := filepath.Join(root, "link")
	if err := os.Symlink(target, symlink); err != nil {
		t.Fatal(err)
	}
	if err := ensureEmptyMountPath(symlink); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("symlink error = %v", err)
	}
	payloadDir := filepath.Join(root, "payload")
	if err := os.Mkdir(payloadDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(payloadDir, "sentinel"), []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ensureEmptyMountPath(payloadDir); err == nil || !strings.Contains(err.Error(), "not empty") {
		t.Fatalf("payload error = %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(payloadDir, "sentinel")); err != nil || string(got) != "keep" {
		t.Fatalf("sentinel changed: %q err=%v", got, err)
	}
}

func TestKeychainServerCandidatesRemainShareSpecific(t *testing.T) {
	storage := smbKeychainServers("loomshare", "loom-storage", "loom-storage")
	box := smbKeychainServers("loomshare", "loom-storage", "loom-main-box")
	if storage[0] != box[0] || storage[1] != box[1] {
		t.Fatalf("host-level Keychain compatibility changed: storage=%#v box=%#v", storage, box)
	}
	if storage[2] == box[2] || !strings.Contains(storage[2], "/loom-storage") || !strings.Contains(box[2], "/loom-main-box") {
		t.Fatalf("share-specific Keychain candidates collide: storage=%#v box=%#v", storage, box)
	}
}
