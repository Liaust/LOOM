package storagecleanup

import "time"

const (
	SchemaVersion = "loom.storage_cleanup.v0.6.2"

	ActionRemoveFile     = "remove_file"
	ActionRemoveSymlink  = "remove_symlink"
	ActionRemoveEmptyDir = "remove_empty_dir"
	ActionManualReview   = "manual_review"

	CleanupScopeViewOnly         = "view_only"
	CleanupScopeMaterializedFile = "materialized_file"
	CleanupScopeManualReview     = "manual_review"

	CatalogDispositionUnchanged    = "unchanged"
	CatalogDispositionManualReview = "manual_review"
	CatalogDispositionNotTouched   = "not_touched"

	StatusPlanned          = "planned"
	StatusWouldRemove      = "would_remove"
	StatusWouldQuarantine  = "would_quarantine"
	StatusRemoved          = "removed"
	StatusQuarantined      = "quarantined"
	StatusSkipped          = "skipped"
	StatusBlocked          = "blocked"
	StatusAlreadySatisfied = "already_satisfied"

	KindFile    = "file"
	KindDir     = "directory"
	KindSymlink = "symlink"
	KindOther   = "other"
)

type InventoryInput struct {
	Root                     string
	Production               bool
	AllowBroadProductionScan bool
	Now                      func() time.Time
}

type InventoryResult struct {
	SchemaVersion string            `json:"schema_version"`
	Root          string            `json:"root"`
	Production    bool              `json:"production"`
	GeneratedAt   time.Time         `json:"generated_at"`
	Summary       Summary           `json:"summary"`
	Items         []Candidate       `json:"items,omitempty"`
	Warnings      []string          `json:"warnings,omitempty"`
	RuleSet       []RuleDescription `json:"rule_set,omitempty"`
}

type Summary struct {
	Scanned        int   `json:"scanned"`
	Candidates     int   `json:"candidates"`
	AutoApply      int   `json:"auto_apply"`
	ManualReview   int   `json:"manual_review"`
	BytesScanned   int64 `json:"bytes_scanned"`
	CandidateBytes int64 `json:"candidate_bytes"`
}

type Candidate struct {
	RelativePath                   string    `json:"relative_path"`
	FullPath                       string    `json:"full_path"`
	Kind                           string    `json:"kind"`
	SizeBytes                      int64     `json:"size_bytes"`
	ModTime                        time.Time `json:"mod_time"`
	Action                         string    `json:"action"`
	AutoApply                      bool      `json:"auto_apply"`
	CleanupScope                   string    `json:"cleanup_scope"`
	CatalogDisposition             string    `json:"catalog_disposition"`
	Reason                         string    `json:"reason"`
	RuleIDs                        []string  `json:"rule_ids,omitempty"`
	FidelityFindings               []string  `json:"fidelity_findings,omitempty"`
	RequiresMetadataLossAcceptance bool      `json:"requires_metadata_loss_acceptance,omitempty"`
}

type RuleDescription struct {
	ID          string `json:"id"`
	Description string `json:"description"`
}

type PlanInput struct {
	Root                     string
	Production               bool
	AllowBroadProductionScan bool
	Now                      func() time.Time
}

type CleanupPlan struct {
	SchemaVersion string            `json:"schema_version"`
	Root          string            `json:"root"`
	Production    bool              `json:"production"`
	Scope         string            `json:"scope,omitempty"`
	GeneratedAt   time.Time         `json:"generated_at"`
	Summary       Summary           `json:"summary"`
	Items         []Candidate       `json:"items"`
	Warnings      []string          `json:"warnings,omitempty"`
	RuleSet       []RuleDescription `json:"rule_set,omitempty"`
}

type ApplyInput struct {
	PlanPath           string
	Plan               *CleanupPlan
	DryRun             bool
	Yes                bool
	DeleteNow          bool
	AcceptMetadataLoss bool
	Now                func() time.Time
}

type ApplyResult struct {
	SchemaVersion string        `json:"schema_version"`
	Root          string        `json:"root"`
	PlanPath      string        `json:"plan_path,omitempty"`
	DryRun        bool          `json:"dry_run"`
	DeleteNow     bool          `json:"delete_now"`
	QuarantineDir string        `json:"quarantine_dir,omitempty"`
	Refused       bool          `json:"refused"`
	Refusal       string        `json:"refusal,omitempty"`
	GeneratedAt   time.Time     `json:"generated_at"`
	Summary       ApplySummary  `json:"summary"`
	Changes       []ApplyChange `json:"changes,omitempty"`
}

type ApplySummary struct {
	Planned         int `json:"planned"`
	Eligible        int `json:"eligible"`
	Removed         int `json:"removed"`
	Quarantined     int `json:"quarantined"`
	WouldRemove     int `json:"would_remove"`
	WouldQuarantine int `json:"would_quarantine"`
	Skipped         int `json:"skipped"`
	Blocked         int `json:"blocked"`
	ManualReview    int `json:"manual_review"`
}

type ApplyChange struct {
	RelativePath   string `json:"relative_path"`
	FullPath       string `json:"full_path"`
	QuarantinePath string `json:"quarantine_path,omitempty"`
	Action         string `json:"action"`
	Status         string `json:"status"`
	Message        string `json:"message,omitempty"`
}
