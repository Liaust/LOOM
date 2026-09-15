package storagedoctor

import "testing"

func TestRunMountPreflightDefaultsToSMB(t *testing.T) {
	status := RunMountPreflight(MountOptions{MountPath: t.TempDir(), SMBHost: "127.0.0.1"})
	if status.Protocol != "smb" {
		t.Fatalf("protocol = %q, want smb", status.Protocol)
	}
	if status.SMBHost != "127.0.0.1" || status.SMBShare != defaultSMBShare || status.SMBUser != defaultSMBUser {
		t.Fatalf("unexpected SMB fields: %#v", status)
	}
	if !mountStatusHasCheck(status, "mount_smbfs") || !mountStatusHasCheck(status, "smbutil") || !mountStatusHasCheck(status, "tcp_445") {
		t.Fatalf("SMB status missing expected checks: %#v", status.Checks)
	}
	if mountStatusHasCheck(status, "rclone_config") {
		t.Fatalf("SMB status should not include rclone config check: %#v", status.Checks)
	}
}

func TestRunMountPreflightSupportsRclone(t *testing.T) {
	status := RunMountPreflight(MountOptions{Protocol: "rclone", MountPath: t.TempDir(), RcloneConfig: "/tmp/rclone.conf", RemoteName: "loom-main-storage"})
	if status.Protocol != "rclone" {
		t.Fatalf("protocol = %q, want rclone", status.Protocol)
	}
	if status.RcloneConfig != "/tmp/rclone.conf" || status.RemoteName != "loom-main-storage" {
		t.Fatalf("unexpected rclone fields: %#v", status)
	}
	if !mountStatusHasCheck(status, "rclone_config") {
		t.Fatalf("rclone status missing rclone config check: %#v", status.Checks)
	}
	if mountStatusHasCheck(status, "tcp_445") {
		t.Fatalf("rclone status should not include SMB TCP check: %#v", status.Checks)
	}
}

func TestRunMountPreflightReportsInvalidProtocol(t *testing.T) {
	status := RunMountPreflight(MountOptions{Protocol: "nfs", MountPath: t.TempDir()})
	if status.Status != StatusError {
		t.Fatalf("status = %q, want error", status.Status)
	}
	if !mountStatusHasCheck(status, "protocol") {
		t.Fatalf("invalid protocol status missing protocol check: %#v", status.Checks)
	}
}

func mountStatusHasCheck(status MountStatus, id string) bool {
	for _, check := range status.Checks {
		if check.ID == id {
			return true
		}
	}
	return false
}
