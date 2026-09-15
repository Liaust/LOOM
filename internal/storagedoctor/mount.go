package storagedoctor

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const (
	defaultMountProtocol = "smb"
	defaultMountPath     = "~/loom-storage"
	defaultRcloneConfig  = "~/.loom/rclone/loom-main-storage.conf"
	defaultRemoteName    = "loom-main-storage"
	defaultSMBHost       = "loom-storage"
	defaultSMBShare      = "loom-storage"
	defaultSMBUser       = "loomshare"
)

func RunMountPreflight(opts MountOptions) MountStatus {
	protocol, protocolErr := ResolveMountProtocol(opts.Protocol)
	mountPath := expandHome(firstNonEmpty(opts.MountPath, os.Getenv("LOOM_MAIN_STORAGE_MOUNT"), defaultMountPath))
	rcloneConfig := expandHome(firstNonEmpty(opts.RcloneConfig, os.Getenv("LOOM_RCLONE_CONFIG"), defaultRcloneConfig))
	remoteName := firstNonEmpty(opts.RemoteName, os.Getenv("LOOM_MAIN_STORAGE_REMOTE"), defaultRemoteName)
	smbHost := firstNonEmpty(opts.SMBHost, os.Getenv("LOOM_MAIN_SMB_HOST"), defaultSMBHost)
	smbShare := firstNonEmpty(opts.SMBShare, os.Getenv("LOOM_MAIN_SMB_SHARE"), defaultSMBShare)
	smbUser := firstNonEmpty(opts.SMBUser, os.Getenv("LOOM_MAIN_SMB_USER"), defaultSMBUser)
	status := MountStatus{
		Status:       StatusOK,
		Protocol:     protocol,
		MountPath:    mountPath,
		RcloneConfig: rcloneConfig,
		RemoteName:   remoteName,
		SMBHost:      smbHost,
		SMBShare:     smbShare,
		SMBUser:      smbUser,
		GeneratedAt:  time.Now().UTC(),
	}
	if protocolErr != nil {
		status.Status = StatusError
		status.Checks = append(status.Checks, MountCheck{ID: "protocol", Status: StatusError, Summary: protocolErr.Error()})
		return status
	}

	switch protocol {
	case "smb":
		status.Checks = append(status.Checks,
			checkDarwinCommand("mount_smbfs", "mount_smbfs command is available"),
			checkDarwinCommand("smbutil", "smbutil command is available"),
			checkLocalMountPath(mountPath),
			checkMounted(mountPath),
			checkSMBTCP(smbHost),
		)
	case "rclone":
		status.Checks = append(status.Checks,
			checkCommand("rclone", "rclone binary is available"),
			checkMacFUSE(),
			checkLocalMountPath(mountPath),
			checkRcloneConfig(rcloneConfig),
			checkMounted(mountPath),
		)
	}
	status.Status = statusFromMountChecks(status.Checks)
	return status
}

func ResolveMountProtocol(value string) (string, error) {
	protocol := strings.ToLower(firstNonEmpty(value, os.Getenv("LOOM_MAIN_STORAGE_PROTOCOL"), defaultMountProtocol))
	switch protocol {
	case "smb", "rclone":
		return protocol, nil
	default:
		return protocol, fmt.Errorf("unsupported mount protocol %q; expected smb or rclone", protocol)
	}
}

func statusFromMountChecks(checks []MountCheck) string {
	status := StatusOK
	for _, check := range checks {
		switch check.Status {
		case StatusError:
			status = StatusError
		case StatusWarning:
			if status != StatusError {
				status = StatusWarning
			}
		}
	}
	return status
}

func checkCommand(name, okSummary string) MountCheck {
	path, err := exec.LookPath(name)
	if err != nil {
		return MountCheck{ID: name, Status: StatusError, Summary: "missing command: " + name}
	}
	return MountCheck{ID: name, Status: StatusOK, Summary: okSummary, Detail: path}
}

func checkDarwinCommand(name, okSummary string) MountCheck {
	if runtime.GOOS != "darwin" {
		return MountCheck{ID: name, Status: StatusSkipped, Summary: name + " check only applies on macOS"}
	}
	return checkCommand(name, okSummary)
}

func checkMacFUSE() MountCheck {
	if runtime.GOOS != "darwin" {
		return MountCheck{ID: "macfuse", Status: StatusSkipped, Summary: "macFUSE check only applies on macOS"}
	}
	for _, path := range []string{"/Library/Filesystems/macfuse.fs", "/Library/Filesystems/osxfuse.fs"} {
		if _, err := os.Stat(path); err == nil {
			return MountCheck{ID: "macfuse", Status: StatusOK, Summary: "macFUSE filesystem is installed", Detail: path}
		}
	}
	if err := exec.Command("pkgutil", "--pkg-info", "io.macfuse.installer.components.core").Run(); err == nil {
		return MountCheck{ID: "macfuse", Status: StatusOK, Summary: "macFUSE package is installed"}
	}
	return MountCheck{ID: "macfuse", Status: StatusError, Summary: "macFUSE is not installed"}
}

func checkSMBTCP(host string) MountCheck {
	host = strings.TrimSpace(host)
	if host == "" {
		return MountCheck{ID: "tcp_445", Status: StatusSkipped, Summary: "SMB host is not configured"}
	}
	address := net.JoinHostPort(host, "445")
	conn, err := net.DialTimeout("tcp", address, 750*time.Millisecond)
	if err != nil {
		return MountCheck{ID: "tcp_445", Status: StatusWarning, Summary: "SMB TCP 445 is not reachable", Detail: address}
	}
	_ = conn.Close()
	return MountCheck{ID: "tcp_445", Status: StatusOK, Summary: "SMB TCP 445 is reachable", Detail: address}
}

func checkLocalMountPath(path string) MountCheck {
	info, err := os.Stat(path)
	if err == nil && info.IsDir() {
		return MountCheck{ID: "mount_path", Status: StatusOK, Summary: "local mount path exists", Detail: path}
	}
	if err == nil {
		return MountCheck{ID: "mount_path", Status: StatusError, Summary: "local mount path exists but is not a directory", Detail: path}
	}
	if os.IsNotExist(err) {
		return MountCheck{ID: "mount_path", Status: StatusWarning, Summary: "local mount path does not exist yet", Detail: path}
	}
	return MountCheck{ID: "mount_path", Status: StatusError, Summary: "could not inspect local mount path: " + err.Error(), Detail: path}
}

func checkRcloneConfig(path string) MountCheck {
	info, err := os.Stat(path)
	if err == nil && !info.IsDir() {
		return MountCheck{ID: "rclone_config", Status: StatusOK, Summary: "managed rclone config exists", Detail: path}
	}
	if err == nil {
		return MountCheck{ID: "rclone_config", Status: StatusError, Summary: "rclone config path is a directory", Detail: path}
	}
	if os.IsNotExist(err) {
		return MountCheck{ID: "rclone_config", Status: StatusWarning, Summary: "managed rclone config does not exist yet", Detail: path}
	}
	return MountCheck{ID: "rclone_config", Status: StatusError, Summary: "could not inspect rclone config: " + err.Error(), Detail: path}
}

func checkMounted(path string) MountCheck {
	output, err := exec.Command("mount").Output()
	if err != nil {
		return MountCheck{ID: "mounted", Status: StatusWarning, Summary: "could not inspect mount table: " + err.Error(), Detail: path}
	}
	needle := " on " + path + " "
	if strings.Contains(string(output), needle) {
		return MountCheck{ID: "mounted", Status: StatusOK, Summary: "LOOM Main path is currently mounted", Detail: path}
	}
	return MountCheck{ID: "mounted", Status: StatusWarning, Summary: "LOOM Main path is not currently mounted", Detail: path}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func expandHome(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return path
	}
	if path == "~" {
		if home, err := os.UserHomeDir(); err == nil {
			return home
		}
	}
	if strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, strings.TrimPrefix(path, "~/"))
		}
	}
	return filepath.Clean(path)
}
