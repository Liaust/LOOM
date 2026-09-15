package cloudstorage

type CloudStatusLiveInput struct {
	ConfigPath string `json:"config_path,omitempty"`
	ForceLive  bool   `json:"force_live,omitempty"`
	Cached     bool   `json:"cached,omitempty"`
}

type CloudDoctorLiveInput struct {
	ConfigPath string `json:"config_path,omitempty"`
	ForceLive  bool   `json:"force_live,omitempty"`
}

type CloudSnapshotStatusLiveInput struct {
	ConfigPath string `json:"config_path,omitempty"`
	NodeID     string `json:"node_id,omitempty"`
	ForceLive  bool   `json:"force_live,omitempty"`
}

type CloudSnapshotStatusReport struct {
	Cloud     StatusReport        `json:"cloud"`
	Snapshots *SnapshotListResult `json:"snapshots,omitempty"`
}

type CloudSnapshotListLiveInput struct {
	ConfigPath string `json:"config_path,omitempty"`
	NodeID     string `json:"node_id,omitempty"`
}

type CloudSnapshotVerifyLiveInput struct {
	ConfigPath string `json:"config_path,omitempty"`
	NodeID     string `json:"node_id,omitempty"`
	Ref        string `json:"ref,omitempty"`
	StateDir   string `json:"state_dir,omitempty"`
}

type CloudSnapshotRetentionPlanLiveInput struct {
	ConfigPath string `json:"config_path,omitempty"`
	NodeID     string `json:"node_id,omitempty"`
	StateDir   string `json:"state_dir,omitempty"`
	KeepLatest int    `json:"keep_latest,omitempty"`
}

type CloudSnapshotRetentionApplyLiveInput struct {
	ConfigPath    string                `json:"config_path,omitempty"`
	NodeID        string                `json:"node_id,omitempty"`
	StateDir      string                `json:"state_dir,omitempty"`
	KeepLatest    int                   `json:"keep_latest,omitempty"`
	Plan          SnapshotRetentionPlan `json:"plan,omitempty"`
	Confirm       bool                  `json:"confirm,omitempty"`
	ConfirmDigest string                `json:"confirm_digest,omitempty"`
	Compact       bool                  `json:"compact,omitempty"`
}

type CloudSnapshotBackendStatusLiveInput struct {
	ConfigPath string `json:"config_path,omitempty"`
	ForceLive  bool   `json:"force_live,omitempty"`
	Cached     bool   `json:"cached,omitempty"`
}

type CloudSnapshotBackendInitLiveInput struct {
	ConfigPath string `json:"config_path,omitempty"`
	Confirm    bool   `json:"confirm,omitempty"`
}
