package macmount

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"time"
)

const (
	SchemaVersion = "loom.storage.mount_policy.v0.7"

	DesiredMounted   = "mounted"
	DesiredUnmounted = "unmounted"

	ActualMounted   = "mounted"
	ActualUnmounted = "unmounted"

	CheckOK      = "ok"
	CheckWarning = "warning"
	CheckError   = "error"
	CheckSkipped = "skipped"

	defaultProtocol            = "smb"
	defaultSMBHost             = "loom-storage"
	defaultSMBIP               = "10.44.0.2"
	defaultSMBShare            = "loom-storage"
	defaultMainBoxSMBShare     = "loom-main-box"
	defaultSMBUser             = "loomshare"
	defaultCloudSMBHost        = "u613836.your-storagebox.de"
	defaultCloudSMBShare       = "backup"
	defaultCloudSMBUser        = "u613836"
	defaultCloudFinderSubpath  = "loom-cloud"
	defaultRetryBackoffSeconds = 60
)

type Policy struct {
	SchemaVersion string             `json:"schema_version"`
	MainStorage   MainStoragePolicy  `json:"main_storage"`
	MainBox       MainStoragePolicy  `json:"main_box"`
	CloudStorage  CloudStoragePolicy `json:"cloud_storage"`
}

type MainStoragePolicy struct {
	DesiredState        string `json:"desired_state"`
	Protocol            string `json:"protocol"`
	MountPath           string `json:"mount_path"`
	FinderMountPath     string `json:"finder_mount_path,omitempty"`
	Host                string `json:"host"`
	ExpectedIP          string `json:"expected_ip,omitempty"`
	Share               string `json:"share"`
	User                string `json:"user"`
	RetryBackoffSeconds int    `json:"retry_backoff_seconds,omitempty"`
}

type CloudStoragePolicy struct {
	DesiredState        string `json:"desired_state"`
	Protocol            string `json:"protocol"`
	MountPath           string `json:"mount_path"`
	FinderMountPath     string `json:"finder_mount_path,omitempty"`
	Host                string `json:"host"`
	Share               string `json:"share"`
	User                string `json:"user"`
	FinderSubpath       string `json:"finder_subpath,omitempty"`
	RetryBackoffSeconds int    `json:"retry_backoff_seconds,omitempty"`
}

type Status struct {
	SchemaVersion       string       `json:"schema_version"`
	DesiredState        string       `json:"desired_state"`
	ActualState         string       `json:"actual_state"`
	Protocol            string       `json:"protocol"`
	MountPath           string       `json:"mount_path"`
	FinderMountPath     string       `json:"finder_mount_path,omitempty"`
	Host                string       `json:"host"`
	HostIP              string       `json:"host_ip,omitempty"`
	ExpectedIP          string       `json:"expected_ip,omitempty"`
	Share               string       `json:"share"`
	User                string       `json:"user"`
	MountedPaths        []string     `json:"mounted_paths,omitempty"`
	Checks              []Check      `json:"checks,omitempty"`
	LastCheckAt         time.Time    `json:"last_check_at"`
	LastSuccessfulMount *time.Time   `json:"last_successful_mount,omitempty"`
	LastErrorCategory   string       `json:"last_error_category,omitempty"`
	LastErrorMessage    string       `json:"last_error_message,omitempty"`
	NextRetryAt         *time.Time   `json:"next_retry_at,omitempty"`
	ConsecutiveFailures int          `json:"consecutive_failures"`
	PolicyPath          string       `json:"policy_path,omitempty"`
	StatusPath          string       `json:"status_path,omitempty"`
	MainBox             *MountStatus `json:"main_box,omitempty"`
	CloudStorage        *MountStatus `json:"cloud_storage,omitempty"`
}

type MountStatus struct {
	Name                string     `json:"name"`
	DesiredState        string     `json:"desired_state"`
	ActualState         string     `json:"actual_state"`
	Protocol            string     `json:"protocol"`
	MountPath           string     `json:"mount_path"`
	FinderMountPath     string     `json:"finder_mount_path,omitempty"`
	Host                string     `json:"host"`
	HostIP              string     `json:"host_ip,omitempty"`
	ExpectedIP          string     `json:"expected_ip,omitempty"`
	Share               string     `json:"share"`
	User                string     `json:"user"`
	FinderSubpath       string     `json:"finder_subpath,omitempty"`
	MountedPaths        []string   `json:"mounted_paths,omitempty"`
	Checks              []Check    `json:"checks,omitempty"`
	LastCheckAt         time.Time  `json:"last_check_at"`
	LastSuccessfulMount *time.Time `json:"last_successful_mount,omitempty"`
	LastErrorCategory   string     `json:"last_error_category,omitempty"`
	LastErrorMessage    string     `json:"last_error_message,omitempty"`
	NextRetryAt         *time.Time `json:"next_retry_at,omitempty"`
	ConsecutiveFailures int        `json:"consecutive_failures"`
}

type Check struct {
	ID      string `json:"id"`
	Status  string `json:"status"`
	Summary string `json:"summary"`
	Detail  string `json:"detail,omitempty"`
}

type Manager struct {
	PolicyPath string
	StatusPath string
	Now        func() time.Time
}

type RepairOptions struct {
	Force bool
}

func NewManager(dataDir string) (Manager, error) {
	baseDir, err := resolveStateDir(dataDir)
	if err != nil {
		return Manager{}, err
	}
	policyPath := strings.TrimSpace(os.Getenv("LOOM_STORAGE_MOUNT_POLICY_PATH"))
	if policyPath == "" {
		policyPath = filepath.Join(baseDir, "storage-mount-policy.json")
	}
	statusPath := strings.TrimSpace(os.Getenv("LOOM_STORAGE_MOUNT_STATUS_PATH"))
	if statusPath == "" {
		statusPath = filepath.Join(baseDir, "storage-mount-status.json")
	}
	return Manager{
		PolicyPath: expandHome(policyPath),
		StatusPath: expandHome(statusPath),
		Now:        func() time.Time { return time.Now().UTC() },
	}, nil
}

func (m Manager) LoadPolicy() (Policy, error) {
	var policy Policy
	err := readJSONFile(m.PolicyPath, &policy)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			policy = DefaultPolicy(DesiredUnmounted)
		} else {
			return Policy{}, err
		}
	}
	policy = NormalizePolicy(policy)
	if err := validatePolicyMountIdentities(policy); err != nil {
		return Policy{}, err
	}
	return policy, nil
}

func (m Manager) SavePolicy(policy Policy) error {
	policy = NormalizePolicy(policy)
	if err := validatePolicyMountIdentities(policy); err != nil {
		return err
	}
	return writeJSONFile(m.PolicyPath, policy, 0o600)
}

func (m Manager) Status(ctx context.Context) (Status, error) {
	policy, err := m.LoadPolicy()
	if err != nil {
		return Status{}, err
	}
	status := m.inspect(ctx, policy, nil)
	status = m.withSecondaryInspect(ctx, policy, status)
	_ = m.saveStatus(status)
	return status, nil
}

func (m Manager) Enable(ctx context.Context) (Status, error) {
	policy, err := m.LoadPolicy()
	if err != nil {
		return Status{}, err
	}
	policy.MainStorage.DesiredState = DesiredMounted
	if err := m.SavePolicy(policy); err != nil {
		return Status{}, err
	}
	status := m.inspect(ctx, policy, nil)
	status.LastErrorCategory = ""
	status.LastErrorMessage = ""
	status.NextRetryAt = nil
	_ = m.saveStatus(status)
	return status, nil
}

func (m Manager) Disable(ctx context.Context, unmount bool) (Status, error) {
	policy, err := m.LoadPolicy()
	if err != nil {
		return Status{}, err
	}
	policy.MainStorage.DesiredState = DesiredUnmounted
	if err := m.SavePolicy(policy); err != nil {
		return Status{}, err
	}
	if unmount {
		_ = m.unmountMounted(ctx, policy)
	}
	status := m.inspect(ctx, policy, nil)
	status.LastErrorCategory = ""
	status.LastErrorMessage = ""
	status.NextRetryAt = nil
	_ = m.saveStatus(status)
	return status, nil
}

func (m Manager) RepairOnce(ctx context.Context, opts RepairOptions) (Status, error) {
	policy, err := m.LoadPolicy()
	if err != nil {
		return Status{}, err
	}
	status := m.inspect(ctx, policy, nil)
	if policy.MainStorage.DesiredState != DesiredMounted {
		status.Checks = append(status.Checks, Check{
			ID:      "desired_state",
			Status:  CheckSkipped,
			Summary: "storage mount policy is not mounted; repair skipped",
			Detail:  policy.MainStorage.DesiredState,
		})
		return m.withSecondaryRepair(ctx, policy, status, opts), nil
	}
	if status.ActualState == ActualMounted {
		status.LastErrorCategory = ""
		status.LastErrorMessage = ""
		status.NextRetryAt = nil
		status.ConsecutiveFailures = 0
		return m.withSecondaryRepair(ctx, policy, status, opts), nil
	}
	if !opts.Force && status.NextRetryAt != nil && m.now().Before(*status.NextRetryAt) {
		status.Checks = append(status.Checks, Check{
			ID:      "backoff",
			Status:  CheckWarning,
			Summary: "mount repair is in backoff",
			Detail:  status.NextRetryAt.Format(time.RFC3339),
		})
		return m.withSecondaryRepair(ctx, policy, status, opts), nil
	}
	if failed := m.mountPreflight(ctx, policy, &status); failed != nil {
		return m.withSecondaryRepair(ctx, policy, m.recordFailure(status, failed.ID, failed.Summary), opts), nil
	}
	if err := ensureEmptyMountPath(policy.MainStorage.MountPath); err != nil {
		return m.withSecondaryRepair(ctx, policy, m.recordFailure(status, "mount_path", err.Error()), opts), nil
	}
	mountURL := smbMountURL(ctx, policy.MainStorage.User, policy.MainStorage.Host, policy.MainStorage.Share)
	if output, err := runCommand(ctx, "mount_smbfs", mountURL, policy.MainStorage.MountPath); err != nil {
		message := strings.TrimSpace(string(output))
		if message == "" {
			message = err.Error()
		}
		category := "mount_smbfs"
		if strings.Contains(strings.ToLower(message), "authentication") {
			category = "smb_auth"
		}
		return m.withSecondaryRepair(ctx, policy, m.recordFailure(status, category, "SMB mount failed: "+message), opts), nil
	}
	status = m.inspect(ctx, policy, nil)
	if status.ActualState != ActualMounted {
		return m.withSecondaryRepair(ctx, policy, m.recordFailure(status, "mounted", "SMB mount command returned but mount did not appear"), opts), nil
	}
	now := m.now()
	status.LastSuccessfulMount = &now
	status.LastErrorCategory = ""
	status.LastErrorMessage = ""
	status.NextRetryAt = nil
	status.ConsecutiveFailures = 0
	status.Checks = append(status.Checks, Check{ID: "repair", Status: CheckOK, Summary: "LOOM Main storage is mounted"})
	return m.withSecondaryRepair(ctx, policy, status, opts), nil
}

func (m Manager) MainBoxStatus(ctx context.Context) (MountStatus, error) {
	policy, err := m.LoadPolicy()
	if err != nil {
		return MountStatus{}, err
	}
	status := m.inspectDirectSMB(ctx, "LOOM Main Box mount", policy.MainBox, m.previousMainBoxStatus())
	full := m.inspect(ctx, policy, nil)
	full.MainBox = &status
	full.CloudStorage = statusPointer(m.inspectCloud(ctx, policy, m.previousCloudStatus()))
	_ = m.saveStatus(full)
	return status, nil
}

func (m Manager) EnableMainBox(ctx context.Context) (MountStatus, error) {
	policy, err := m.LoadPolicy()
	if err != nil {
		return MountStatus{}, err
	}
	policy.MainBox.DesiredState = DesiredMounted
	if err := m.SavePolicy(policy); err != nil {
		return MountStatus{}, err
	}
	return m.MainBoxStatus(ctx)
}

func (m Manager) DisableMainBox(ctx context.Context, unmount bool) (MountStatus, error) {
	policy, err := m.LoadPolicy()
	if err != nil {
		return MountStatus{}, err
	}
	policy.MainBox.DesiredState = DesiredUnmounted
	if err := m.SavePolicy(policy); err != nil {
		return MountStatus{}, err
	}
	if unmount {
		_ = m.unmountMainBoxMounted(ctx, policy)
	}
	return m.MainBoxStatus(ctx)
}

func (m Manager) RepairMainBoxOnce(ctx context.Context, opts RepairOptions) (MountStatus, error) {
	policy, err := m.LoadPolicy()
	if err != nil {
		return MountStatus{}, err
	}
	status := m.repairDirectSMB(ctx, policy, "LOOM Main Box mount", policy.MainBox, m.previousMainBoxStatus(), opts)
	full := m.inspect(ctx, policy, nil)
	full.MainBox = &status
	full.CloudStorage = statusPointer(m.inspectCloud(ctx, policy, m.previousCloudStatus()))
	_ = m.saveStatus(full)
	return status, nil
}

func (m Manager) CloudStatus(ctx context.Context) (MountStatus, error) {
	policy, err := m.LoadPolicy()
	if err != nil {
		return MountStatus{}, err
	}
	previous := m.previousCloudStatus()
	status := m.inspectCloud(ctx, policy, previous)
	full := m.inspect(ctx, policy, nil)
	full.CloudStorage = &status
	_ = m.saveStatus(full)
	return status, nil
}

func (m Manager) EnableCloud(ctx context.Context) (MountStatus, error) {
	policy, err := m.LoadPolicy()
	if err != nil {
		return MountStatus{}, err
	}
	policy.CloudStorage.DesiredState = DesiredMounted
	if err := m.SavePolicy(policy); err != nil {
		return MountStatus{}, err
	}
	status := m.inspectCloud(ctx, policy, nil)
	status.LastErrorCategory = ""
	status.LastErrorMessage = ""
	status.NextRetryAt = nil
	full := m.inspect(ctx, policy, nil)
	full.CloudStorage = &status
	_ = m.saveStatus(full)
	return status, nil
}

func (m Manager) DisableCloud(ctx context.Context, unmount bool) (MountStatus, error) {
	policy, err := m.LoadPolicy()
	if err != nil {
		return MountStatus{}, err
	}
	policy.CloudStorage.DesiredState = DesiredUnmounted
	if err := m.SavePolicy(policy); err != nil {
		return MountStatus{}, err
	}
	if unmount {
		_ = m.unmountCloudMounted(ctx, policy)
	}
	status := m.inspectCloud(ctx, policy, nil)
	status.LastErrorCategory = ""
	status.LastErrorMessage = ""
	status.NextRetryAt = nil
	full := m.inspect(ctx, policy, nil)
	full.CloudStorage = &status
	_ = m.saveStatus(full)
	return status, nil
}

func (m Manager) RepairCloudOnce(ctx context.Context, opts RepairOptions) (MountStatus, error) {
	policy, err := m.LoadPolicy()
	if err != nil {
		return MountStatus{}, err
	}
	status := m.repairCloud(ctx, policy, opts)
	full := m.inspect(ctx, policy, nil)
	full.CloudStorage = &status
	_ = m.saveStatus(full)
	return status, nil
}

func (m Manager) inspect(ctx context.Context, policy Policy, previous *Status) Status {
	policy = NormalizePolicy(policy)
	if previous == nil {
		loaded, err := m.loadStatus()
		if err == nil {
			previous = &loaded
		}
	}
	now := m.now()
	mountedPaths, mountErr := mountedStoragePaths(ctx, policy)
	actualState := ActualUnmounted
	if len(mountedPaths) > 0 {
		actualState = ActualMounted
	}
	hostIP := resolveFirstIP(policy.MainStorage.Host)
	status := Status{
		SchemaVersion:       SchemaVersion,
		DesiredState:        policy.MainStorage.DesiredState,
		ActualState:         actualState,
		Protocol:            policy.MainStorage.Protocol,
		MountPath:           policy.MainStorage.MountPath,
		FinderMountPath:     policy.MainStorage.FinderMountPath,
		Host:                policy.MainStorage.Host,
		HostIP:              hostIP,
		ExpectedIP:          policy.MainStorage.ExpectedIP,
		Share:               policy.MainStorage.Share,
		User:                policy.MainStorage.User,
		MountedPaths:        mountedPaths,
		LastCheckAt:         now,
		PolicyPath:          m.PolicyPath,
		StatusPath:          m.StatusPath,
		ConsecutiveFailures: 0,
	}
	if previous != nil {
		status.LastSuccessfulMount = previous.LastSuccessfulMount
		status.LastErrorCategory = previous.LastErrorCategory
		status.LastErrorMessage = previous.LastErrorMessage
		status.NextRetryAt = previous.NextRetryAt
		status.ConsecutiveFailures = previous.ConsecutiveFailures
	}
	if status.ActualState == ActualMounted || (status.DesiredState == DesiredUnmounted && status.ActualState == ActualUnmounted) {
		status.LastErrorCategory = ""
		status.LastErrorMessage = ""
		status.NextRetryAt = nil
		status.ConsecutiveFailures = 0
		if status.ActualState == ActualMounted && status.LastSuccessfulMount == nil {
			mountedAt := now
			status.LastSuccessfulMount = &mountedAt
		}
	}
	status.Checks = append(status.Checks, Check{ID: "policy", Status: CheckOK, Summary: "storage mount policy loaded", Detail: status.DesiredState})
	if goruntime.GOOS == "darwin" {
		status.Checks = append(status.Checks, Check{ID: "platform", Status: CheckOK, Summary: "macOS mount management is available"})
	} else {
		status.Checks = append(status.Checks, Check{ID: "platform", Status: CheckSkipped, Summary: "SMB mount management only applies on macOS", Detail: goruntime.GOOS})
	}
	if mountErr != nil {
		status.Checks = append(status.Checks, Check{ID: "mount_table", Status: CheckWarning, Summary: mountErr.Error()})
	} else if actualState == ActualMounted {
		status.Checks = append(status.Checks, Check{ID: "mounted", Status: CheckOK, Summary: "LOOM Main storage is mounted", Detail: strings.Join(mountedPaths, ", ")})
	} else {
		status.Checks = append(status.Checks, Check{ID: "mounted", Status: CheckWarning, Summary: "LOOM Main storage is not mounted"})
	}
	if hostIP == "" {
		status.Checks = append(status.Checks, Check{ID: "host_resolution", Status: CheckWarning, Summary: "SMB host did not resolve", Detail: policy.MainStorage.Host})
	} else if policy.MainStorage.ExpectedIP != "" && hostIP != policy.MainStorage.ExpectedIP {
		status.Checks = append(status.Checks, Check{ID: "host_resolution", Status: CheckWarning, Summary: "SMB host resolved to unexpected IP", Detail: hostIP})
	} else {
		status.Checks = append(status.Checks, Check{ID: "host_resolution", Status: CheckOK, Summary: "SMB host resolves", Detail: hostIP})
	}
	return status
}

func (m Manager) mountPreflight(ctx context.Context, policy Policy, status *Status) *Check {
	if err := validatePolicyMountIdentities(policy); err != nil {
		check := Check{ID: "mount_identity", Status: CheckError, Summary: err.Error()}
		status.Checks = append(status.Checks, check)
		return &check
	}
	checks, failed := m.directSMBPreflight(ctx, policy.MainStorage)
	status.Checks = append(status.Checks, checks...)
	return failed
}

func (m Manager) directSMBPreflight(ctx context.Context, mountPolicy MainStoragePolicy) ([]Check, *Check) {
	checks := []Check{}
	fail := func(check Check) ([]Check, *Check) {
		checks = append(checks, check)
		return checks, &checks[len(checks)-1]
	}
	if goruntime.GOOS != "darwin" {
		check := Check{ID: "platform", Status: CheckSkipped, Summary: "SMB mount repair only runs on macOS"}
		return fail(check)
	}
	mountOutput, err := runOutput(ctx, "mount")
	if err != nil {
		return fail(Check{ID: "mount_table", Status: CheckError, Summary: "could not inspect mounted filesystem identities: " + err.Error()})
	}
	if conflicts := mountIdentityConflictsFromOutput(string(mountOutput), mountPolicy); len(conflicts) > 0 {
		conflict := conflicts[0]
		return fail(Check{ID: "mount_identity", Status: CheckError, Summary: fmt.Sprintf("mount path %s is owned by %s, not //%s/%s", conflict.Target, conflict.Source, mountPolicy.Host, mountPolicy.Share)})
	}
	checks = append(checks, Check{ID: "mount_identity", Status: CheckOK, Summary: "configured mount paths have no foreign mount identity"})
	for _, name := range []string{"mount_smbfs", "security"} {
		if _, err := exec.LookPath(name); err != nil {
			check := Check{ID: name, Status: CheckError, Summary: "missing command: " + name}
			return fail(check)
		}
		checks = append(checks, Check{ID: name, Status: CheckOK, Summary: name + " is available"})
	}
	if hostIP := resolveFirstIP(mountPolicy.Host); hostIP == "" {
		return fail(Check{ID: "host_resolution", Status: CheckError, Summary: "SMB host does not resolve: " + mountPolicy.Host})
	} else if mountPolicy.ExpectedIP != "" && hostIP != mountPolicy.ExpectedIP {
		return fail(Check{ID: "host_resolution", Status: CheckError, Summary: fmt.Sprintf("SMB host resolves to %s, expected %s", hostIP, mountPolicy.ExpectedIP)})
	}
	if isWireGuardHost(mountPolicy.Host) || strings.HasPrefix(resolveFirstIP(mountPolicy.Host), "10.44.") {
		if !hasPrivateRoute(ctx) {
			return fail(Check{ID: "wireguard_route", Status: CheckError, Summary: "missing 10.44/24 WireGuard route"})
		}
		checks = append(checks, Check{ID: "wireguard_route", Status: CheckOK, Summary: "10.44/24 WireGuard route is present"})
	}
	if err := tcpReachable(ctx, mountPolicy.Host, "445", 2*time.Second); err != nil {
		return fail(Check{ID: "tcp_445", Status: CheckError, Summary: "SMB TCP 445 is not reachable: " + err.Error()})
	}
	checks = append(checks, Check{ID: "tcp_445", Status: CheckOK, Summary: "SMB TCP 445 is reachable"})
	if !keychainHasSMBCredential(ctx, mountPolicy.User, mountPolicy.Host, mountPolicy.Share) {
		return fail(Check{ID: "smb_keychain", Status: CheckError, Summary: "SMB credential is not available in Keychain; mount once manually with Finder and save the password"})
	}
	checks = append(checks, Check{ID: "smb_keychain", Status: CheckOK, Summary: "SMB credential is available in Keychain"})
	return checks, nil
}

func (m Manager) recordFailure(status Status, category string, message string) Status {
	now := m.now()
	status.LastCheckAt = now
	status.LastErrorCategory = category
	status.LastErrorMessage = message
	status.ConsecutiveFailures++
	backoff := defaultRetryBackoffSeconds
	if status.ConsecutiveFailures > 3 {
		backoff = defaultRetryBackoffSeconds * 5
	}
	next := now.Add(time.Duration(backoff) * time.Second)
	status.NextRetryAt = &next
	status.Checks = append(status.Checks, Check{ID: category, Status: CheckError, Summary: message})
	_ = m.saveStatus(status)
	return status
}

func (m Manager) withSecondaryInspect(ctx context.Context, policy Policy, status Status) Status {
	mainBox := m.inspectDirectSMB(ctx, "LOOM Main Box mount", policy.MainBox, m.previousMainBoxStatus())
	cloud := m.inspectCloud(ctx, policy, m.previousCloudStatus())
	status.MainBox = &mainBox
	status.CloudStorage = &cloud
	return status
}

func (m Manager) withSecondaryRepair(ctx context.Context, policy Policy, status Status, opts RepairOptions) Status {
	var mainBox MountStatus
	if policy.MainBox.DesiredState == DesiredMounted {
		mainBox = m.repairDirectSMB(ctx, policy, "LOOM Main Box mount", policy.MainBox, m.previousMainBoxStatus(), opts)
	} else {
		mainBox = m.inspectDirectSMB(ctx, "LOOM Main Box mount", policy.MainBox, m.previousMainBoxStatus())
	}
	var cloud MountStatus
	if policy.CloudStorage.DesiredState == DesiredMounted {
		cloud = m.repairCloud(ctx, policy, opts)
	} else {
		cloud = m.inspectCloud(ctx, policy, m.previousCloudStatus())
	}
	status.MainBox = &mainBox
	status.CloudStorage = &cloud
	_ = m.saveStatus(status)
	return status
}

func (m Manager) previousMainBoxStatus() *MountStatus {
	loaded, err := m.loadStatus()
	if err != nil || loaded.MainBox == nil {
		return nil
	}
	return loaded.MainBox
}

func (m Manager) previousCloudStatus() *MountStatus {
	loaded, err := m.loadStatus()
	if err != nil || loaded.CloudStorage == nil {
		return nil
	}
	return loaded.CloudStorage
}

func statusPointer(status MountStatus) *MountStatus {
	return &status
}

func (m Manager) inspectDirectSMB(ctx context.Context, name string, mountPolicy MainStoragePolicy, previous *MountStatus) MountStatus {
	now := m.now()
	mountedPaths, mountErr := mountedSMBPaths(ctx, mountPolicy)
	actualState := ActualUnmounted
	if len(mountedPaths) > 0 {
		actualState = ActualMounted
	}
	hostIP := resolveFirstIP(mountPolicy.Host)
	status := MountStatus{
		Name:                name,
		DesiredState:        mountPolicy.DesiredState,
		ActualState:         actualState,
		Protocol:            mountPolicy.Protocol,
		MountPath:           mountPolicy.MountPath,
		FinderMountPath:     mountPolicy.FinderMountPath,
		Host:                mountPolicy.Host,
		HostIP:              hostIP,
		ExpectedIP:          mountPolicy.ExpectedIP,
		Share:               mountPolicy.Share,
		User:                mountPolicy.User,
		MountedPaths:        mountedPaths,
		LastCheckAt:         now,
		ConsecutiveFailures: 0,
	}
	if previous != nil {
		status.LastSuccessfulMount = previous.LastSuccessfulMount
		status.LastErrorCategory = previous.LastErrorCategory
		status.LastErrorMessage = previous.LastErrorMessage
		status.NextRetryAt = previous.NextRetryAt
		status.ConsecutiveFailures = previous.ConsecutiveFailures
	}
	if status.ActualState == ActualMounted || (status.DesiredState == DesiredUnmounted && status.ActualState == ActualUnmounted) {
		status.LastErrorCategory = ""
		status.LastErrorMessage = ""
		status.NextRetryAt = nil
		status.ConsecutiveFailures = 0
		if status.ActualState == ActualMounted && status.LastSuccessfulMount == nil {
			mountedAt := now
			status.LastSuccessfulMount = &mountedAt
		}
	}
	status.Checks = append(status.Checks, Check{ID: "policy", Status: CheckOK, Summary: name + " policy loaded", Detail: status.DesiredState})
	if goruntime.GOOS == "darwin" {
		status.Checks = append(status.Checks, Check{ID: "platform", Status: CheckOK, Summary: "macOS SMB mount management is available"})
	} else {
		status.Checks = append(status.Checks, Check{ID: "platform", Status: CheckSkipped, Summary: "SMB mount management only applies on macOS", Detail: goruntime.GOOS})
	}
	if mountErr != nil {
		status.Checks = append(status.Checks, Check{ID: "mount_table", Status: CheckWarning, Summary: mountErr.Error()})
	} else if actualState == ActualMounted {
		status.Checks = append(status.Checks, Check{ID: "mounted", Status: CheckOK, Summary: name + " is mounted", Detail: strings.Join(mountedPaths, ", ")})
	} else {
		status.Checks = append(status.Checks, Check{ID: "mounted", Status: CheckWarning, Summary: name + " is not mounted"})
	}
	if hostIP == "" {
		status.Checks = append(status.Checks, Check{ID: "host_resolution", Status: CheckWarning, Summary: "SMB host did not resolve", Detail: mountPolicy.Host})
	} else if mountPolicy.ExpectedIP != "" && hostIP != mountPolicy.ExpectedIP {
		status.Checks = append(status.Checks, Check{ID: "host_resolution", Status: CheckWarning, Summary: "SMB host resolved to unexpected IP", Detail: hostIP})
	} else {
		status.Checks = append(status.Checks, Check{ID: "host_resolution", Status: CheckOK, Summary: "SMB host resolves", Detail: hostIP})
	}
	return status
}

func (m Manager) repairDirectSMB(ctx context.Context, policy Policy, name string, mountPolicy MainStoragePolicy, previous *MountStatus, opts RepairOptions) MountStatus {
	status := m.inspectDirectSMB(ctx, name, mountPolicy, previous)
	if mountPolicy.DesiredState != DesiredMounted {
		status.Checks = append(status.Checks, Check{ID: "desired_state", Status: CheckSkipped, Summary: name + " policy is not mounted; repair skipped", Detail: mountPolicy.DesiredState})
		return status
	}
	if status.ActualState == ActualMounted {
		status.LastErrorCategory = ""
		status.LastErrorMessage = ""
		status.NextRetryAt = nil
		status.ConsecutiveFailures = 0
		return status
	}
	if !opts.Force && status.NextRetryAt != nil && m.now().Before(*status.NextRetryAt) {
		status.Checks = append(status.Checks, Check{ID: "backoff", Status: CheckWarning, Summary: name + " repair is in backoff", Detail: status.NextRetryAt.Format(time.RFC3339)})
		return status
	}
	if err := validatePolicyMountIdentities(policy); err != nil {
		return m.recordDirectFailure(status, "mount_identity", err.Error())
	}
	checks, failed := m.directSMBPreflight(ctx, mountPolicy)
	status.Checks = append(status.Checks, checks...)
	if failed != nil {
		return m.recordDirectFailure(status, failed.ID, failed.Summary)
	}
	if err := ensureEmptyMountPath(mountPolicy.MountPath); err != nil {
		return m.recordDirectFailure(status, "mount_path", err.Error())
	}
	mountURL := smbMountURL(ctx, mountPolicy.User, mountPolicy.Host, mountPolicy.Share)
	if output, err := runCommand(ctx, "mount_smbfs", mountURL, mountPolicy.MountPath); err != nil {
		message := strings.TrimSpace(string(output))
		if message == "" {
			message = err.Error()
		}
		category := "mount_smbfs"
		if strings.Contains(strings.ToLower(message), "authentication") {
			category = "smb_auth"
		}
		return m.recordDirectFailure(status, category, "SMB mount failed: "+message)
	}
	status = m.inspectDirectSMB(ctx, name, mountPolicy, nil)
	if status.ActualState != ActualMounted {
		return m.recordDirectFailure(status, "mounted", "SMB mount command returned but the exact share identity did not appear")
	}
	now := m.now()
	status.LastSuccessfulMount = &now
	status.LastErrorCategory = ""
	status.LastErrorMessage = ""
	status.NextRetryAt = nil
	status.ConsecutiveFailures = 0
	status.Checks = append(status.Checks, Check{ID: "repair", Status: CheckOK, Summary: name + " is mounted"})
	return status
}

func (m Manager) recordDirectFailure(status MountStatus, category string, message string) MountStatus {
	now := m.now()
	status.LastCheckAt = now
	status.LastErrorCategory = category
	status.LastErrorMessage = message
	status.ConsecutiveFailures++
	backoff := defaultRetryBackoffSeconds
	if status.ConsecutiveFailures > 3 {
		backoff = defaultRetryBackoffSeconds * 5
	}
	next := now.Add(time.Duration(backoff) * time.Second)
	status.NextRetryAt = &next
	status.Checks = append(status.Checks, Check{ID: category, Status: CheckError, Summary: message})
	return status
}

func (m Manager) inspectCloud(ctx context.Context, policy Policy, previous *MountStatus) MountStatus {
	policy = NormalizePolicy(policy)
	now := m.now()
	mountedPaths, mountErr := mountedCloudPaths(ctx, policy)
	actualState := ActualUnmounted
	if len(mountedPaths) > 0 {
		actualState = ActualMounted
	}
	hostIP := resolveFirstIP(policy.CloudStorage.Host)
	status := MountStatus{
		Name:            "LOOM Cloud storage mount",
		DesiredState:    policy.CloudStorage.DesiredState,
		ActualState:     actualState,
		Protocol:        policy.CloudStorage.Protocol,
		MountPath:       policy.CloudStorage.MountPath,
		FinderMountPath: policy.CloudStorage.FinderMountPath,
		Host:            policy.CloudStorage.Host,
		HostIP:          hostIP,
		Share:           policy.CloudStorage.Share,
		User:            policy.CloudStorage.User,
		FinderSubpath:   policy.CloudStorage.FinderSubpath,
		MountedPaths:    mountedPaths,
		LastCheckAt:     now,
	}
	if previous != nil {
		status.LastSuccessfulMount = previous.LastSuccessfulMount
		status.LastErrorCategory = previous.LastErrorCategory
		status.LastErrorMessage = previous.LastErrorMessage
		status.NextRetryAt = previous.NextRetryAt
		status.ConsecutiveFailures = previous.ConsecutiveFailures
	}
	if status.ActualState == ActualMounted || (status.DesiredState == DesiredUnmounted && status.ActualState == ActualUnmounted) {
		status.LastErrorCategory = ""
		status.LastErrorMessage = ""
		status.NextRetryAt = nil
		status.ConsecutiveFailures = 0
		if status.ActualState == ActualMounted && status.LastSuccessfulMount == nil {
			mountedAt := now
			status.LastSuccessfulMount = &mountedAt
		}
	}
	status.Checks = append(status.Checks, Check{ID: "policy", Status: CheckOK, Summary: "cloud mount policy loaded", Detail: status.DesiredState})
	if goruntime.GOOS == "darwin" {
		status.Checks = append(status.Checks, Check{ID: "platform", Status: CheckOK, Summary: "macOS Finder SMB mount management is available"})
	} else {
		status.Checks = append(status.Checks, Check{ID: "platform", Status: CheckSkipped, Summary: "cloud SMB mount management only applies on macOS", Detail: goruntime.GOOS})
	}
	if mountErr != nil {
		status.Checks = append(status.Checks, Check{ID: "mount_table", Status: CheckWarning, Summary: mountErr.Error()})
	} else if actualState == ActualMounted {
		status.Checks = append(status.Checks, Check{ID: "mounted", Status: CheckOK, Summary: "LOOM Cloud storage is mounted", Detail: strings.Join(mountedPaths, ", ")})
	} else {
		status.Checks = append(status.Checks, Check{ID: "mounted", Status: CheckWarning, Summary: "LOOM Cloud storage is not mounted"})
	}
	if hostIP == "" {
		status.Checks = append(status.Checks, Check{ID: "host_resolution", Status: CheckWarning, Summary: "Cloud SMB host did not resolve", Detail: policy.CloudStorage.Host})
	} else {
		status.Checks = append(status.Checks, Check{ID: "host_resolution", Status: CheckOK, Summary: "Cloud SMB host resolves", Detail: hostIP})
	}
	return status
}

func (m Manager) repairCloud(ctx context.Context, policy Policy, opts RepairOptions) MountStatus {
	status := m.inspectCloud(ctx, policy, m.previousCloudStatus())
	if policy.CloudStorage.DesiredState != DesiredMounted {
		status.Checks = append(status.Checks, Check{
			ID:      "desired_state",
			Status:  CheckSkipped,
			Summary: "cloud mount policy is not mounted; repair skipped",
			Detail:  policy.CloudStorage.DesiredState,
		})
		return status
	}
	if status.ActualState == ActualMounted {
		status.LastErrorCategory = ""
		status.LastErrorMessage = ""
		status.NextRetryAt = nil
		status.ConsecutiveFailures = 0
		return status
	}
	if !opts.Force && status.NextRetryAt != nil && m.now().Before(*status.NextRetryAt) {
		status.Checks = append(status.Checks, Check{
			ID:      "backoff",
			Status:  CheckWarning,
			Summary: "cloud mount repair is in backoff",
			Detail:  status.NextRetryAt.Format(time.RFC3339),
		})
		return status
	}
	if failed := m.cloudPreflight(ctx, policy, &status); failed != nil {
		return m.recordCloudFailure(status, failed.ID, failed.Summary)
	}
	finderURL := smbFinderURL(policy.CloudStorage.User, policy.CloudStorage.Host, policy.CloudStorage.Share)
	if output, err := runCommand(ctx, "open", finderURL); err != nil {
		message := strings.TrimSpace(string(output))
		if message == "" {
			message = err.Error()
		}
		return m.recordCloudFailure(status, "finder_open", "could not open Finder SMB mount: "+message)
	}
	if !waitForCloudMount(ctx, policy, 20*time.Second) {
		return m.recordCloudFailure(status, "finder_auth", "Finder did not mount LOOM Cloud; complete SMB login and save the credential if prompted")
	}
	if err := ensureCloudFinderSubpath(policy); err != nil {
		return m.recordCloudFailure(status, "cloud_subpath", err.Error())
	}
	status = m.inspectCloud(ctx, policy, nil)
	if status.ActualState != ActualMounted {
		return m.recordCloudFailure(status, "mounted", "Finder mounted cloud share but the mount did not appear")
	}
	now := m.now()
	status.LastSuccessfulMount = &now
	status.LastErrorCategory = ""
	status.LastErrorMessage = ""
	status.NextRetryAt = nil
	status.ConsecutiveFailures = 0
	status.Checks = append(status.Checks, Check{ID: "repair", Status: CheckOK, Summary: "LOOM Cloud storage is mounted"})
	return status
}

func (m Manager) cloudPreflight(ctx context.Context, policy Policy, status *MountStatus) *Check {
	if err := validatePolicyMountIdentities(policy); err != nil {
		check := Check{ID: "mount_identity", Status: CheckError, Summary: err.Error()}
		status.Checks = append(status.Checks, check)
		return &check
	}
	if goruntime.GOOS != "darwin" {
		check := Check{ID: "platform", Status: CheckSkipped, Summary: "cloud SMB repair only runs on macOS"}
		status.Checks = append(status.Checks, check)
		return &check
	}
	mountOutput, err := runOutput(ctx, "mount")
	if err != nil {
		check := Check{ID: "mount_table", Status: CheckError, Summary: "could not inspect mounted filesystem identities: " + err.Error()}
		status.Checks = append(status.Checks, check)
		return &check
	}
	if conflicts := mountIdentityConflictsFromOutput(string(mountOutput), cloudSMBPolicy(policy.CloudStorage)); len(conflicts) > 0 {
		conflict := conflicts[0]
		check := Check{ID: "mount_identity", Status: CheckError, Summary: fmt.Sprintf("mount path %s is owned by %s, not //%s/%s", conflict.Target, conflict.Source, policy.CloudStorage.Host, policy.CloudStorage.Share)}
		status.Checks = append(status.Checks, check)
		return &check
	}
	status.Checks = append(status.Checks, Check{ID: "mount_identity", Status: CheckOK, Summary: "configured cloud mount paths have no foreign mount identity"})
	for _, name := range []string{"open", "security"} {
		if _, err := exec.LookPath(name); err != nil {
			check := Check{ID: name, Status: CheckError, Summary: "missing command: " + name}
			status.Checks = append(status.Checks, check)
			return &check
		}
		status.Checks = append(status.Checks, Check{ID: name, Status: CheckOK, Summary: name + " is available"})
	}
	if hostIP := resolveFirstIP(policy.CloudStorage.Host); hostIP == "" {
		check := Check{ID: "host_resolution", Status: CheckError, Summary: "cloud SMB host does not resolve: " + policy.CloudStorage.Host}
		status.Checks = append(status.Checks, check)
		return &check
	}
	if err := tcpReachable(ctx, policy.CloudStorage.Host, "445", 2*time.Second); err != nil {
		check := Check{ID: "tcp_445", Status: CheckError, Summary: "cloud SMB TCP 445 is not reachable: " + err.Error()}
		status.Checks = append(status.Checks, check)
		return &check
	}
	status.Checks = append(status.Checks, Check{ID: "tcp_445", Status: CheckOK, Summary: "cloud SMB TCP 445 is reachable"})
	if keychainHasSMBCredential(ctx, policy.CloudStorage.User, policy.CloudStorage.Host, policy.CloudStorage.Share) {
		status.Checks = append(status.Checks, Check{ID: "smb_keychain", Status: CheckOK, Summary: "cloud SMB credential is available in Keychain"})
	} else {
		status.Checks = append(status.Checks, Check{ID: "smb_keychain", Status: CheckWarning, Summary: "cloud SMB credential is not available in Keychain; Finder may prompt"})
	}
	return nil
}

func (m Manager) recordCloudFailure(status MountStatus, category string, message string) MountStatus {
	now := m.now()
	status.LastCheckAt = now
	status.LastErrorCategory = category
	status.LastErrorMessage = message
	status.ConsecutiveFailures++
	backoff := defaultRetryBackoffSeconds
	if status.ConsecutiveFailures > 3 {
		backoff = defaultRetryBackoffSeconds * 5
	}
	next := now.Add(time.Duration(backoff) * time.Second)
	status.NextRetryAt = &next
	status.Checks = append(status.Checks, Check{ID: category, Status: CheckError, Summary: message})
	return status
}

func (m Manager) unmountMounted(ctx context.Context, policy Policy) error {
	paths, _ := mountedStoragePaths(ctx, policy)
	var errs []string
	for _, path := range paths {
		if _, err := runCommand(ctx, "umount", path); err != nil {
			if _, diskErr := runCommand(ctx, "diskutil", "unmount", "force", path); diskErr != nil {
				errs = append(errs, fmt.Sprintf("%s: %v", path, diskErr))
			}
		}
	}
	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "; "))
	}
	return nil
}

func (m Manager) unmountMainBoxMounted(ctx context.Context, policy Policy) error {
	paths, _ := mountedSMBPaths(ctx, policy.MainBox)
	var errs []string
	for _, path := range paths {
		if _, err := runCommand(ctx, "umount", path); err != nil {
			if _, diskErr := runCommand(ctx, "diskutil", "unmount", "force", path); diskErr != nil {
				errs = append(errs, fmt.Sprintf("%s: %v", path, diskErr))
			}
		}
	}
	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "; "))
	}
	return nil
}

func (m Manager) unmountCloudMounted(ctx context.Context, policy Policy) error {
	paths, _ := mountedCloudPaths(ctx, policy)
	var errs []string
	for _, path := range paths {
		if _, err := runCommand(ctx, "umount", path); err != nil {
			if _, diskErr := runCommand(ctx, "diskutil", "unmount", "force", path); diskErr != nil {
				errs = append(errs, fmt.Sprintf("%s: %v", path, diskErr))
			}
		}
	}
	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "; "))
	}
	return nil
}

func (m Manager) loadStatus() (Status, error) {
	var status Status
	err := readJSONFile(m.StatusPath, &status)
	return status, err
}

func (m Manager) now() time.Time {
	if m.Now != nil {
		return m.Now()
	}
	return time.Now().UTC()
}

func (m Manager) saveStatus(status Status) error {
	return writeJSONFile(m.StatusPath, status, 0o600)
}

func NormalizePolicy(policy Policy) Policy {
	defaultPolicy := DefaultPolicy(DesiredUnmounted)
	policy.SchemaVersion = SchemaVersion
	policy.MainStorage = normalizeDirectMountPolicy(policy.MainStorage, defaultPolicy.MainStorage)
	policy.MainBox = normalizeDirectMountPolicy(policy.MainBox, defaultPolicy.MainBox)
	if strings.TrimSpace(policy.CloudStorage.DesiredState) == "" {
		policy.CloudStorage.DesiredState = defaultPolicy.CloudStorage.DesiredState
	}
	switch strings.ToLower(strings.TrimSpace(policy.CloudStorage.DesiredState)) {
	case DesiredMounted:
		policy.CloudStorage.DesiredState = DesiredMounted
	default:
		policy.CloudStorage.DesiredState = DesiredUnmounted
	}
	if strings.TrimSpace(policy.CloudStorage.Protocol) == "" {
		policy.CloudStorage.Protocol = defaultPolicy.CloudStorage.Protocol
	}
	policy.CloudStorage.Protocol = strings.ToLower(strings.TrimSpace(policy.CloudStorage.Protocol))
	if strings.TrimSpace(policy.CloudStorage.Host) == "" {
		policy.CloudStorage.Host = defaultPolicy.CloudStorage.Host
	}
	if strings.TrimSpace(policy.CloudStorage.Share) == "" {
		policy.CloudStorage.Share = defaultPolicy.CloudStorage.Share
	}
	if strings.TrimSpace(policy.CloudStorage.User) == "" {
		policy.CloudStorage.User = defaultPolicy.CloudStorage.User
	}
	if strings.TrimSpace(policy.CloudStorage.FinderMountPath) == "" {
		policy.CloudStorage.FinderMountPath = filepath.Join("/Volumes", firstNonEmpty(policy.CloudStorage.Share, defaultCloudSMBShare))
	}
	policy.CloudStorage.FinderMountPath = expandHome(policy.CloudStorage.FinderMountPath)
	if policy.CloudStorage.Protocol == defaultProtocol {
		// Finder owns the SMB mount. Expose its real path instead of maintaining a
		// second home-directory alias that can become stale while unmounted.
		policy.CloudStorage.MountPath = policy.CloudStorage.FinderMountPath
	} else {
		if strings.TrimSpace(policy.CloudStorage.MountPath) == "" {
			policy.CloudStorage.MountPath = defaultPolicy.CloudStorage.MountPath
		}
		policy.CloudStorage.MountPath = expandHome(policy.CloudStorage.MountPath)
	}
	if strings.TrimSpace(policy.CloudStorage.FinderSubpath) == "" {
		policy.CloudStorage.FinderSubpath = defaultPolicy.CloudStorage.FinderSubpath
	}
	if policy.CloudStorage.RetryBackoffSeconds <= 0 {
		policy.CloudStorage.RetryBackoffSeconds = defaultRetryBackoffSeconds
	}
	return policy
}

func normalizeDirectMountPolicy(policy, defaults MainStoragePolicy) MainStoragePolicy {
	if strings.TrimSpace(policy.DesiredState) == "" {
		policy.DesiredState = defaults.DesiredState
	}
	if strings.EqualFold(strings.TrimSpace(policy.DesiredState), DesiredMounted) {
		policy.DesiredState = DesiredMounted
	} else {
		policy.DesiredState = DesiredUnmounted
	}
	policy.Protocol = strings.ToLower(firstNonEmpty(policy.Protocol, defaults.Protocol))
	policy.MountPath = expandHome(firstNonEmpty(policy.MountPath, defaults.MountPath))
	policy.Host = firstNonEmpty(policy.Host, defaults.Host)
	policy.ExpectedIP = firstNonEmpty(policy.ExpectedIP, defaults.ExpectedIP)
	policy.Share = firstNonEmpty(policy.Share, defaults.Share)
	policy.User = firstNonEmpty(policy.User, defaults.User)
	policy.FinderMountPath = expandHome(firstNonEmpty(policy.FinderMountPath, defaults.FinderMountPath, filepath.Join("/Volumes", policy.Share)))
	if policy.RetryBackoffSeconds <= 0 {
		policy.RetryBackoffSeconds = defaultRetryBackoffSeconds
	}
	return policy
}

func DefaultPolicy(desiredState string) Policy {
	home, _ := os.UserHomeDir()
	mountPath := firstNonEmpty(os.Getenv("LOOM_MAIN_STORAGE_MOUNT"), filepath.Join(home, "loom-storage"))
	share := firstNonEmpty(os.Getenv("LOOM_MAIN_SMB_SHARE"), defaultSMBShare)
	mainBoxShare := firstNonEmpty(os.Getenv("LOOM_MAIN_BOX_SMB_SHARE"), defaultMainBoxSMBShare)
	mainHost := firstNonEmpty(os.Getenv("LOOM_MAIN_SMB_HOST"), defaultSMBHost)
	mainExpectedIP := firstNonEmpty(os.Getenv("LOOM_MAIN_SMB_IP"), os.Getenv("LOOM_MAIN_STORAGE_HOST"), defaultSMBIP)
	mainUser := firstNonEmpty(os.Getenv("LOOM_MAIN_SMB_USER"), defaultSMBUser)
	cloudShare := firstNonEmpty(os.Getenv("LOOM_CLOUD_FOLDER_SMB_SHARE"), defaultCloudSMBShare)
	cloudProtocol := strings.ToLower(firstNonEmpty(os.Getenv("LOOM_CLOUD_FOLDER_PROTOCOL"), defaultProtocol))
	cloudFinderMount := firstNonEmpty(os.Getenv("LOOM_CLOUD_FOLDER_SMB_FINDER_MOUNT"), filepath.Join("/Volumes", cloudShare))
	cloudMount := expandHome(firstNonEmpty(os.Getenv("LOOM_CLOUD_FOLDER_MOUNT"), filepath.Join(home, "loom-cloud")))
	if cloudProtocol == defaultProtocol {
		cloudMount = cloudFinderMount
	}
	return Policy{
		SchemaVersion: SchemaVersion,
		MainStorage: MainStoragePolicy{
			DesiredState:        firstNonEmpty(desiredState, DesiredUnmounted),
			Protocol:            defaultProtocol,
			MountPath:           expandHome(mountPath),
			FinderMountPath:     firstNonEmpty(os.Getenv("LOOM_MAIN_SMB_FINDER_MOUNT"), filepath.Join("/Volumes", share)),
			Host:                mainHost,
			ExpectedIP:          mainExpectedIP,
			Share:               share,
			User:                mainUser,
			RetryBackoffSeconds: defaultRetryBackoffSeconds,
		},
		MainBox: MainStoragePolicy{
			DesiredState:        DesiredUnmounted,
			Protocol:            defaultProtocol,
			MountPath:           expandHome(firstNonEmpty(os.Getenv("LOOM_MAIN_BOX_MOUNT"), filepath.Join(home, "loom-main-box"))),
			FinderMountPath:     firstNonEmpty(os.Getenv("LOOM_MAIN_BOX_SMB_FINDER_MOUNT"), filepath.Join("/Volumes", mainBoxShare)),
			Host:                firstNonEmpty(os.Getenv("LOOM_MAIN_BOX_SMB_HOST"), mainHost),
			ExpectedIP:          firstNonEmpty(os.Getenv("LOOM_MAIN_BOX_SMB_IP"), mainExpectedIP),
			Share:               mainBoxShare,
			User:                firstNonEmpty(os.Getenv("LOOM_MAIN_BOX_SMB_USER"), mainUser),
			RetryBackoffSeconds: defaultRetryBackoffSeconds,
		},
		CloudStorage: CloudStoragePolicy{
			DesiredState:        DesiredUnmounted,
			Protocol:            cloudProtocol,
			MountPath:           cloudMount,
			FinderMountPath:     cloudFinderMount,
			Host:                firstNonEmpty(os.Getenv("LOOM_CLOUD_FOLDER_SMB_HOST"), os.Getenv("LOOM_CLOUD_STORAGE_HOST"), defaultCloudSMBHost),
			Share:               cloudShare,
			User:                firstNonEmpty(os.Getenv("LOOM_CLOUD_FOLDER_SMB_USER"), os.Getenv("LOOM_CLOUD_STORAGE_USER"), defaultCloudSMBUser),
			FinderSubpath:       firstNonEmpty(os.Getenv("LOOM_CLOUD_FOLDER_FINDER_SUBPATH"), defaultCloudFinderSubpath),
			RetryBackoffSeconds: defaultRetryBackoffSeconds,
		},
	}
}

func resolveStateDir(dataDir string) (string, error) {
	if strings.TrimSpace(dataDir) != "" {
		return expandHome(dataDir), nil
	}
	if dir := strings.TrimSpace(os.Getenv("XDG_STATE_HOME")); dir != "" {
		return filepath.Join(expandHome(dir), "loom-node-agent"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve user home: %w", err)
	}
	return filepath.Join(home, ".local", "state", "loom-node-agent"), nil
}

func validatePolicyMountIdentities(policy Policy) error {
	for name, mountPolicy := range map[string]MainStoragePolicy{
		"main storage": policy.MainStorage,
		"main box":     policy.MainBox,
	} {
		if mountPolicy.Protocol != defaultProtocol {
			return fmt.Errorf("%s protocol must be smb, got %q", name, mountPolicy.Protocol)
		}
		if strings.TrimSpace(mountPolicy.Host) == "" || strings.TrimSpace(mountPolicy.Share) == "" || strings.TrimSpace(mountPolicy.User) == "" {
			return fmt.Errorf("%s SMB host, share, and user must be configured", name)
		}
		for _, path := range []string{mountPolicy.MountPath, mountPolicy.FinderMountPath} {
			if !filepath.IsAbs(path) || filepath.Clean(path) != path {
				return fmt.Errorf("%s mount path must be absolute and clean: %q", name, path)
			}
		}
	}
	for _, path := range []string{policy.CloudStorage.MountPath, policy.CloudStorage.FinderMountPath} {
		if !filepath.IsAbs(path) || filepath.Clean(path) != path {
			return fmt.Errorf("direct cloud mount path must be absolute and clean: %q", path)
		}
	}
	if policy.MainStorage.User != policy.MainBox.User {
		return fmt.Errorf("main Storage and Box must use the same dedicated SMB user")
	}
	owners := map[string]string{}
	mounts := []struct {
		name   string
		policy MainStoragePolicy
	}{
		{name: "main storage", policy: policy.MainStorage},
		{name: "main box", policy: policy.MainBox},
		{name: "direct cloud", policy: cloudSMBPolicy(policy.CloudStorage)},
	}
	for _, mount := range mounts {
		for _, path := range []string{mount.policy.MountPath, mount.policy.FinderMountPath} {
			path = filepath.Clean(strings.TrimSpace(path))
			if path == "" || path == "." {
				continue
			}
			if owner, exists := owners[path]; exists && owner != mount.name {
				return fmt.Errorf("mount path collision: %s is configured for both %s and %s", path, owner, mount.name)
			}
			owners[path] = mount.name
		}
	}
	remoteOwners := map[string]string{}
	for _, mount := range mounts {
		hostIdentity := firstNonEmpty(strings.TrimSpace(mount.policy.ExpectedIP), strings.TrimSpace(mount.policy.Host))
		identity := strings.ToLower(hostIdentity) + "/" + strings.TrimLeft(strings.TrimSpace(mount.policy.Share), "/")
		if owner, exists := remoteOwners[identity]; exists && owner != mount.name {
			return fmt.Errorf("SMB identity collision: //%s is configured for both %s and %s", identity, owner, mount.name)
		}
		remoteOwners[identity] = mount.name
	}
	return nil
}

func ensureEmptyMountPath(path string) error {
	path = strings.TrimSpace(path)
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return fmt.Errorf("mount path must be absolute and clean: %q", path)
	}
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		if err := os.MkdirAll(path, 0o755); err != nil {
			return fmt.Errorf("could not create mount path: %w", err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("could not inspect mount path: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("mount path must not be a symlink: %s", path)
	}
	if !info.IsDir() {
		return fmt.Errorf("mount path exists and is not a directory: %s", path)
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return fmt.Errorf("could not inspect mount path contents: %w", err)
	}
	if len(entries) != 0 {
		return fmt.Errorf("mount path exists and is not empty: %s", path)
	}
	return nil
}

func mountedStoragePaths(ctx context.Context, policy Policy) ([]string, error) {
	return mountedSMBPaths(ctx, policy.MainStorage)
}

func mountedCloudPaths(ctx context.Context, policy Policy) ([]string, error) {
	return mountedSMBPaths(ctx, cloudSMBPolicy(policy.CloudStorage))
}

func mountedSMBPaths(ctx context.Context, policy MainStoragePolicy) ([]string, error) {
	output, err := runOutput(ctx, "mount")
	if err != nil {
		return nil, err
	}
	if conflicts := mountIdentityConflictsFromOutput(string(output), policy); len(conflicts) > 0 {
		conflict := conflicts[0]
		return nil, fmt.Errorf("mount path %s is owned by %s, not //%s/%s", conflict.Target, conflict.Source, policy.Host, policy.Share)
	}
	return mountedSMBPathsFromOutput(string(output), policy), nil
}

func mountedSMBPathsFromOutput(output string, policy MainStoragePolicy) []string {
	candidates := []string{policy.MountPath, policy.FinderMountPath}
	seen := map[string]bool{}
	var paths []string
	for _, record := range parseMountRecords(output) {
		if !matchesSMBSource(record.Source, policy) {
			continue
		}
		for _, path := range candidates {
			path = strings.TrimSpace(path)
			if path == "" || seen[path] || record.Target != path {
				continue
			}
			seen[path] = true
			paths = append(paths, path)
		}
	}
	return paths
}

type mountRecord struct {
	Source string
	Target string
}

func parseMountRecords(output string) []mountRecord {
	records := []mountRecord{}
	for _, line := range strings.Split(output, "\n") {
		on := strings.Index(line, " on ")
		if on <= 0 {
			continue
		}
		rest := line[on+4:]
		options := strings.LastIndex(rest, " (")
		if options <= 0 {
			continue
		}
		records = append(records, mountRecord{
			Source: strings.TrimSpace(line[:on]),
			Target: strings.TrimSpace(rest[:options]),
		})
	}
	return records
}

func matchesSMBSource(source string, policy MainStoragePolicy) bool {
	if !strings.HasPrefix(source, "//") {
		return false
	}
	parsed, err := url.Parse("smb:" + source)
	if err != nil {
		return false
	}
	share, err := url.PathUnescape(strings.TrimPrefix(parsed.EscapedPath(), "/"))
	if err != nil {
		return false
	}
	sourceHost := parsed.Hostname()
	hostMatches := strings.EqualFold(sourceHost, strings.TrimSpace(policy.Host)) || (policy.ExpectedIP != "" && sourceHost == policy.ExpectedIP)
	if !hostMatches || share != strings.TrimLeft(strings.TrimSpace(policy.Share), "/") {
		return false
	}
	if parsed.User == nil || parsed.User.Username() == "" {
		return true
	}
	user, err := url.PathUnescape(parsed.User.Username())
	return err == nil && user == policy.User
}

func mountIdentityConflictsFromOutput(output string, policy MainStoragePolicy) []mountRecord {
	candidates := map[string]bool{}
	for _, path := range []string{policy.MountPath, policy.FinderMountPath} {
		path = strings.TrimSpace(path)
		if path != "" {
			candidates[path] = true
		}
	}
	conflicts := []mountRecord{}
	for _, record := range parseMountRecords(output) {
		if candidates[record.Target] && !matchesSMBSource(record.Source, policy) {
			conflicts = append(conflicts, record)
		}
	}
	return conflicts
}

func cloudSMBPolicy(policy CloudStoragePolicy) MainStoragePolicy {
	return MainStoragePolicy{
		DesiredState:        policy.DesiredState,
		Protocol:            policy.Protocol,
		MountPath:           policy.MountPath,
		FinderMountPath:     policy.FinderMountPath,
		Host:                policy.Host,
		Share:               policy.Share,
		User:                policy.User,
		RetryBackoffSeconds: policy.RetryBackoffSeconds,
	}
}

func waitForCloudMount(ctx context.Context, policy Policy, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		paths, _ := mountedCloudPaths(ctx, policy)
		if len(paths) > 0 {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(500 * time.Millisecond):
		}
	}
}

func ensureCloudFinderSubpath(policy Policy) error {
	if strings.TrimSpace(policy.CloudStorage.FinderMountPath) == "" {
		return errors.New("cloud Finder mount path is empty")
	}
	if strings.TrimSpace(policy.CloudStorage.FinderSubpath) == "" {
		return nil
	}
	target := filepath.Join(policy.CloudStorage.FinderMountPath, policy.CloudStorage.FinderSubpath)
	if err := os.MkdirAll(target, 0o755); err != nil {
		return fmt.Errorf("could not create cloud Finder subpath: %w", err)
	}
	info, err := os.Lstat(target)
	if err != nil {
		return fmt.Errorf("could not inspect cloud Finder subpath: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("cloud Finder subpath must be a real directory: %s", target)
	}
	return nil
}

func resolveFirstIP(host string) string {
	host = strings.TrimSpace(host)
	if host == "" {
		return ""
	}
	if parsed := net.ParseIP(host); parsed != nil {
		return host
	}
	ips, err := net.LookupHost(host)
	if err != nil || len(ips) == 0 {
		return ""
	}
	return ips[0]
}

func hasPrivateRoute(ctx context.Context) bool {
	output, err := runOutput(ctx, "netstat", "-rn", "-f", "inet")
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(output), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		switch fields[0] {
		case "10.44", "10.44/24", "10.44.0.0/24":
			return true
		}
	}
	return false
}

func isWireGuardHost(host string) bool {
	return strings.HasPrefix(resolveFirstIP(host), "10.44.")
}

func tcpReachable(ctx context.Context, host, port string, timeout time.Duration) error {
	dialer := net.Dialer{Timeout: timeout}
	conn, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(host, port))
	if err != nil {
		return err
	}
	_ = conn.Close()
	return nil
}

func keychainHasSMBCredential(ctx context.Context, user, host, share string) bool {
	if _, err := exec.LookPath("security"); err != nil {
		return false
	}
	for _, server := range smbKeychainServers(user, host, share) {
		if strings.TrimSpace(server) == "" {
			continue
		}
		if _, err := runCommand(ctx, "security", "find-internet-password", "-a", user, "-s", server); err == nil {
			return true
		}
	}
	return false
}

func smbMountURL(ctx context.Context, user, host, share string) string {
	if password, ok := keychainSMBPassword(ctx, user, host, share); ok {
		return smbURL(user, password, host, share)
	}
	return smbURL(user, "", host, share)
}

func smbFinderURL(user, host, share string) string {
	u := url.URL{
		Scheme: "smb",
		Host:   host,
		Path:   "/" + strings.TrimLeft(share, "/"),
		User:   url.User(user),
	}
	return u.String()
}

func smbURL(user, password, host, share string) string {
	u := url.URL{
		Scheme: "smb",
		Host:   host,
		Path:   "/" + strings.TrimLeft(share, "/"),
	}
	if password != "" {
		u.User = url.UserPassword(user, password)
	} else {
		u.User = url.User(user)
	}
	return strings.TrimPrefix(u.String(), "smb:")
}

func keychainSMBPassword(ctx context.Context, user, host, share string) (string, bool) {
	if _, err := exec.LookPath("security"); err != nil {
		return "", false
	}
	passwordCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	for _, server := range smbKeychainServers(user, host, share) {
		if strings.TrimSpace(server) == "" {
			continue
		}
		output, err := runCommand(passwordCtx, "security", "find-internet-password", "-w", "-a", user, "-s", server)
		if err != nil {
			continue
		}
		password := strings.TrimRight(string(output), "\r\n")
		if password != "" {
			return password, true
		}
	}
	return "", false
}

func smbKeychainServers(user, host, share string) []string {
	return []string{
		host,
		"smb://" + host,
		"//" + user + "@" + host + "/" + share,
	}
}

func runOutput(ctx context.Context, name string, args ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, name, args...)
	return command.Output()
}

func runCommand(ctx context.Context, name string, args ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, name, args...)
	return command.CombinedOutput()
}

func readJSONFile(path string, target any) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	return json.NewDecoder(file).Decode(target)
}

func writeJSONFile(path string, value any, perm fs.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if err := os.WriteFile(tmp, data, perm); err != nil {
		return err
	}
	return os.Rename(tmp, path)
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

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
