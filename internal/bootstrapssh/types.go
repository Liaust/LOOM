package bootstrapssh

import (
	"context"
	"time"

	"loom.local/loom/internal/enrollmentflow"
	"loom.local/loom/internal/setup"
)

const (
	SourceModeCurrentRsync = "current-rsync"
	SourceModeGitClone     = "git-clone"

	DefaultRemoteSourceService   = "/srv/loom/current"
	DefaultRemoteSourceUser      = ".local/share/loom/source/current"
	DefaultRemoteSourceDeveloper = "loom/current"
)

type Spec struct {
	TargetHost string `json:"target_host"`
	SSHUser    string `json:"ssh_user,omitempty"`
	SSHPort    int    `json:"ssh_port,omitempty"`
	SSHKeyPath string `json:"-"`

	NodeKey      string `json:"node_key,omitempty"`
	DisplayName  string `json:"display_name,omitempty"`
	NodeKind     string `json:"node_kind,omitempty"`
	NodeRole     string `json:"node_role,omitempty"`
	RuntimeClass string `json:"runtime_class,omitempty"`

	MainHost       string `json:"main_host,omitempty"`
	MainURL        string `json:"main_url,omitempty"`
	MainSSHUser    string `json:"main_ssh_user,omitempty"`
	MainSSHPort    int    `json:"main_ssh_port,omitempty"`
	MainSSHKeyPath string `json:"-"`
	MainSocket     string `json:"main_socket,omitempty"`

	SourceMode string `json:"source_mode,omitempty"`
	SourcePath string `json:"source_path,omitempty"`
	GitURL     string `json:"git_url,omitempty"`
	GitRef     string `json:"git_ref,omitempty"`

	RemoteSourceDir   string `json:"remote_source_dir,omitempty"`
	PackageMode       string `json:"package_mode,omitempty"`
	InstallMode       string `json:"install_mode,omitempty"`
	ServiceManager    string `json:"service_manager,omitempty"`
	BoxPath           string `json:"box_path,omitempty"`
	BoxProfile        string `json:"box_profile,omitempty"`
	HomeDir           string `json:"home_dir,omitempty"`
	UserName          string `json:"user_name,omitempty"`
	ConfigDir         string `json:"config_dir,omitempty"`
	DataDir           string `json:"data_dir,omitempty"`
	StateDir          string `json:"state_dir,omitempty"`
	LogDir            string `json:"log_dir,omitempty"`
	ServiceRoot       string `json:"service_root,omitempty"`
	StorageRoot       string `json:"storage_root,omitempty"`
	ImportsRoot       string `json:"imports_root,omitempty"`
	UserBackupsRoot   string `json:"user_backups_root,omitempty"`
	ArchiveRoot       string `json:"archive_root,omitempty"`
	GeneratedRoot     string `json:"generated_root,omitempty"`
	BoxStateRoot      string `json:"box_state_root,omitempty"`
	ObjectStorePath   string `json:"object_store_path,omitempty"`
	MainDocumentsPath string `json:"main_documents_path,omitempty"`
	// StorageExportRoot is accepted only as legacy manifest/migration evidence.
	// BuildSetupSpec deliberately does not activate it for new installations.
	StorageExportRoot    string `json:"storage_export_root,omitempty"`
	SocketPath           string `json:"socket_path,omitempty"`
	HTTPListenAddr       string `json:"http_listen_addr,omitempty"`
	DBURL                string `json:"db_url,omitempty"`
	MigrationsDir        string `json:"migrations_dir,omitempty"`
	BootstrapMode        string `json:"bootstrap_mode,omitempty"`
	EnrollmentTTLSeconds int    `json:"enrollment_ttl_seconds,omitempty"`

	DryRun bool `json:"dry_run"`
	Yes    bool `json:"yes,omitempty"`
	Resume bool `json:"resume,omitempty"`
}

type SSHTarget struct {
	Host              string `json:"host"`
	User              string `json:"user,omitempty"`
	Port              int    `json:"port,omitempty"`
	KeyPathConfigured bool   `json:"key_path_configured,omitempty"`

	keyPath string
}

type RemoteCommand struct {
	Command string
	Stdin   string
}

type RemoteResult struct {
	Stdout   string `json:"stdout,omitempty"`
	Stderr   string `json:"stderr,omitempty"`
	ExitCode int    `json:"exit_code"`
}

type CopyOptions struct {
	Excludes []string
}

type Runner interface {
	Run(ctx context.Context, command RemoteCommand) (RemoteResult, error)
	CopyTo(ctx context.Context, localPath string, remotePath string, opts CopyOptions) error
}

type RemoteFacts struct {
	Hostname string `json:"hostname,omitempty"`
	OS       string `json:"os,omitempty"`
	Arch     string `json:"arch,omitempty"`
	User     string `json:"user,omitempty"`
	UID      string `json:"uid,omitempty"`
	HomeDir  string `json:"home_dir,omitempty"`

	HasSudo    bool `json:"has_sudo"`
	HasSystemd bool `json:"has_systemd"`
	HasLaunchd bool `json:"has_launchd"`
	HasNix     bool `json:"has_nix"`
	HasGit     bool `json:"has_git"`
	HasGo      bool `json:"has_go"`
	HasRsync   bool `json:"has_rsync"`
	HasTar     bool `json:"has_tar"`

	ExistingLoom         string `json:"existing_loom,omitempty"`
	ExistingLoomd        string `json:"existing_loomd,omitempty"`
	ExistingNodeAgent    string `json:"existing_node_agent,omitempty"`
	ExistingManifestPath string `json:"existing_manifest_path,omitempty"`
}

type SourcePlan struct {
	Mode       string   `json:"mode"`
	LocalPath  string   `json:"local_path,omitempty"`
	RemotePath string   `json:"remote_path,omitempty"`
	GitURL     string   `json:"git_url,omitempty"`
	GitRef     string   `json:"git_ref,omitempty"`
	Excludes   []string `json:"excludes,omitempty"`
}

type RemoteSetupPlan struct {
	SpecPath                string   `json:"spec_path,omitempty"`
	EnrollmentResultPath    string   `json:"enrollment_result_path,omitempty"`
	PackageMode             string   `json:"package_mode,omitempty"`
	RequiredTools           []string `json:"required_tools,omitempty"`
	BuildCommand            string   `json:"build_command,omitempty"`
	ApplyCommand            string   `json:"apply_command,omitempty"`
	StatusCommand           string   `json:"status_command,omitempty"`
	DoctorCommand           string   `json:"doctor_command,omitempty"`
	RecordEnrollmentCommand string   `json:"record_enrollment_command,omitempty"`
}

type Warning struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Field   string `json:"field,omitempty"`
}

type Plan struct {
	CreatedAt           time.Time       `json:"created_at"`
	Spec                Spec            `json:"spec"`
	Target              SSHTarget       `json:"target"`
	Facts               RemoteFacts     `json:"facts"`
	Source              SourcePlan      `json:"source"`
	SetupSpec           setup.SetupSpec `json:"setup_spec"`
	SetupPlan           setup.SetupPlan `json:"setup_plan"`
	RemoteSetup         RemoteSetupPlan `json:"remote_setup"`
	Warnings            []Warning       `json:"warnings,omitempty"`
	WouldRunRemoteSetup bool            `json:"would_run_remote_setup"`
}

type Result struct {
	Status          string                 `json:"status"`
	DryRun          bool                   `json:"dry_run"`
	SourceCopied    bool                   `json:"source_copied"`
	SetupSpecCopied bool                   `json:"setup_spec_copied"`
	Plan            Plan                   `json:"plan"`
	RemoteApply     *setup.ApplyResult     `json:"remote_apply,omitempty"`
	Enrollment      *enrollmentflow.Result `json:"enrollment,omitempty"`
	RemoteStatus    *setup.SetupStatus     `json:"remote_status,omitempty"`
	RemoteDoctor    *setup.DoctorReport    `json:"remote_doctor,omitempty"`
	Steps           []StepResult           `json:"steps,omitempty"`
	Warnings        []Warning              `json:"warnings,omitempty"`
	RemoteSetupNote string                 `json:"remote_setup_note"`
}

type RunInput struct {
	Spec                   Spec
	Runner                 Runner
	EnrollmentMainRunner   enrollmentflow.MainRunner
	EnrollmentTargetRunner enrollmentflow.TargetRunner
	Now                    func() time.Time
}

type StepResult struct {
	ID       string `json:"id"`
	Status   string `json:"status"`
	Command  string `json:"command,omitempty"`
	Message  string `json:"message,omitempty"`
	ExitCode int    `json:"exit_code,omitempty"`
}

type BootstrapError struct {
	Code          string `json:"code"`
	Phase         string `json:"phase"`
	Message       string `json:"message"`
	ExitCode      int    `json:"exit_code,omitempty"`
	StdoutExcerpt string `json:"stdout_excerpt,omitempty"`
	StderrExcerpt string `json:"stderr_excerpt,omitempty"`
}

func (e BootstrapError) Error() string {
	if e.Message != "" {
		return e.Code + ": " + e.Message
	}
	return e.Code
}
