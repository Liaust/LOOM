package minidashboard

import (
	"fmt"
	"math"
	"time"
)

const SchemaVersion = "loom.mini_dashboard.v1"

type SourceState string

const (
	SourceLive        SourceState = "live"
	SourceCached      SourceState = "cached"
	SourceStale       SourceState = "stale"
	SourceOffline     SourceState = "offline"
	SourceUnavailable SourceState = "unavailable"
	SourceFailed      SourceState = "failed"
	SourceDisabled    SourceState = "disabled"
	SourceUnknown     SourceState = "unknown"
)

func validSourceState(state SourceState) bool {
	switch state {
	case SourceLive, SourceCached, SourceStale, SourceOffline, SourceUnavailable, SourceFailed, SourceDisabled, SourceUnknown:
		return true
	default:
		return false
	}
}

func normalizeSourceState(state, emptyDefault SourceState) SourceState {
	if state == "" {
		return emptyDefault
	}
	if !validSourceState(state) {
		return SourceUnknown
	}
	return state
}

func validateSourceState(name string, state SourceState) error {
	if state != "" && !validSourceState(state) {
		return fmt.Errorf("%s source state %q is invalid", name, state)
	}
	return nil
}

type Freshness struct {
	SourceUpdatedAt   time.Time   `json:"source_updated_at"`
	StaleAfterSeconds int64       `json:"stale_after_seconds"`
	SourceState       SourceState `json:"source_state"`
}

type Metric struct {
	Available         bool        `json:"available"`
	Value             *float64    `json:"value,omitempty"`
	Unit              string      `json:"unit,omitempty"`
	SampledAt         time.Time   `json:"sampled_at,omitempty"`
	StaleAfterSeconds int64       `json:"stale_after_seconds"`
	SourceState       SourceState `json:"source_state"`
	Severity          Severity    `json:"severity"`
}

type HostSnapshot struct {
	GeneratedAt time.Time `json:"generated_at"`
	CPU         Metric    `json:"cpu"`
	Temperature Metric    `json:"temperature"`
	Memory      Metric    `json:"memory"`
	Storage     Metric    `json:"storage"`
}

type NodeIdentity struct {
	Key   string `json:"key"`
	Label string `json:"label"`
}

type RuntimeState struct {
	Freshness          Freshness   `json:"freshness"`
	Available          bool        `json:"available"`
	State              SourceState `json:"state"`
	DatabaseState      string      `json:"database_state"`
	MigrationsCurrent  bool        `json:"migrations_current"`
	EnabledWorkers     int         `json:"enabled_workers"`
	HealthyWorkers     int         `json:"healthy_workers"`
	CriticalWorkers    int         `json:"critical_workers"`
	DegradedWorkers    int         `json:"degraded_workers"`
	OfflineRunners     int         `json:"offline_runners"`
	Queued             int         `json:"queued"`
	Running            int         `json:"running"`
	Failed             int         `json:"failed"`
	ManualAction       int         `json:"manual_action"`
	DeadLetter         int         `json:"dead_letter"`
	OldestQueuedAgeSec *int64      `json:"oldest_queued_age_seconds,omitempty"`
}

type ProtectionState struct {
	Available              bool        `json:"available"`
	State                  string      `json:"state"`
	WorkerState            string      `json:"worker_state,omitempty"`
	LastSuccessAt          *time.Time  `json:"last_success_at,omitempty"`
	LastFailureAt          *time.Time  `json:"last_failure_at,omitempty"`
	VerificationState      string      `json:"verification_state,omitempty"`
	IntervalSeconds        int64       `json:"interval_seconds"`
	NextRunAt              *time.Time  `json:"next_run_at,omitempty"`
	InitializationGraceEnd *time.Time  `json:"initialization_grace_ends_at,omitempty"`
	SourceUpdatedAt        time.Time   `json:"source_updated_at"`
	StaleAfterSeconds      int64       `json:"stale_after_seconds"`
	SourceState            SourceState `json:"source_state"`
}

type CoverageState struct {
	Available         bool        `json:"available"`
	State             string      `json:"state"`
	CapturedAt        time.Time   `json:"captured_at,omitempty"`
	StaleAfterSeconds int64       `json:"stale_after_seconds"`
	SourceState       SourceState `json:"source_state"`
}

type FindingSummary struct {
	Available bool `json:"available"`
	Open      int  `json:"open"`
	Warning   int  `json:"warning"`
	Error     int  `json:"error"`
	Critical  int  `json:"critical"`
}

type NodePresence struct {
	Key        string     `json:"key"`
	Label      string     `json:"label"`
	Online     bool       `json:"online"`
	LastSeenAt *time.Time `json:"last_seen_at,omitempty"`
}

type CommunicationSummary struct {
	Available  bool `json:"available"`
	Pending    int  `json:"pending"`
	Failed     int  `json:"failed"`
	DeadLetter int  `json:"dead_letter"`
}

type CloudReachability struct {
	Available      bool        `json:"available"`
	Enabled        bool        `json:"enabled"`
	State          string      `json:"state"`
	Cached         bool        `json:"cached"`
	CheckedAt      time.Time   `json:"checked_at,omitempty"`
	CacheUpdatedAt time.Time   `json:"cache_updated_at,omitempty"`
	SourceState    SourceState `json:"source_state"`
}

type NetworkState struct {
	Freshness     Freshness            `json:"freshness"`
	Available     bool                 `json:"available"`
	Nodes         []NodePresence       `json:"nodes,omitempty"`
	Communication CommunicationSummary `json:"communication"`
	Cloud         CloudReachability    `json:"cloud"`
}

type Activity struct {
	Kind        string     `json:"kind"`
	State       string     `json:"state"`
	StartedAt   *time.Time `json:"started_at,omitempty"`
	CompletedAt *time.Time `json:"completed_at,omitempty"`
}

type ActivityState struct {
	Freshness Freshness `json:"freshness"`
	Running   *Activity `json:"running,omitempty"`
	Recent    *Activity `json:"recent,omitempty"`
}

// DomainSnapshot is the bounded, path-free contract produced by loomd. It
// deliberately contains summaries instead of backend records or error text.
type DomainSnapshot struct {
	SchemaVersion string          `json:"schema_version"`
	GeneratedAt   time.Time       `json:"generated_at"`
	Freshness     Freshness       `json:"freshness"`
	Node          NodeIdentity    `json:"node"`
	Runtime       RuntimeState    `json:"runtime"`
	LocalBackup   ProtectionState `json:"local_backup"`
	CloudSnapshot ProtectionState `json:"cloud_snapshot"`
	Coverage      CoverageState   `json:"coverage"`
	Findings      FindingSummary  `json:"findings"`
	Network       NetworkState    `json:"network"`
	Activity      ActivityState   `json:"activity"`
}

type Condition struct {
	Code       string   `json:"code"`
	Label      string   `json:"label"`
	Severity   Severity `json:"severity"`
	Priority   int      `json:"priority"`
	Actionable bool     `json:"actionable"`
}

type HeaderView struct {
	Product         string      `json:"product"`
	NodeLabel       string      `json:"node_label"`
	Overall         Severity    `json:"overall"`
	SourceUpdatedAt time.Time   `json:"source_updated_at,omitempty"`
	StaleAfterSec   int64       `json:"stale_after_seconds"`
	FreshnessState  SourceState `json:"freshness_state"`
}

type AttentionView struct {
	Message         string   `json:"message"`
	Severity        Severity `json:"severity"`
	AdditionalCount int      `json:"additional_count"`
}

type RowView struct {
	Key      string      `json:"key"`
	Label    string      `json:"label"`
	Value    string      `json:"value"`
	Severity Severity    `json:"severity"`
	State    SourceState `json:"state"`
}

type SummaryView struct {
	State    string   `json:"state"`
	Detail   string   `json:"detail"`
	Severity Severity `json:"severity"`
}

type ViewSnapshot struct {
	SchemaVersion string               `json:"schema_version"`
	GeneratedAt   time.Time            `json:"generated_at"`
	Header        HeaderView           `json:"header"`
	Attention     AttentionView        `json:"attention"`
	System        [4]RowView           `json:"system"`
	Runtime       [4]RowView           `json:"runtime"`
	Protection    [4]RowView           `json:"protection"`
	Network       SummaryView          `json:"network"`
	Activity      SummaryView          `json:"activity"`
	Freshness     map[string]Freshness `json:"freshness"`
}

func (snapshot DomainSnapshot) Validate() error {
	if snapshot.SchemaVersion != SchemaVersion {
		return fmt.Errorf("schema_version must be %q", SchemaVersion)
	}
	if snapshot.GeneratedAt.IsZero() || snapshot.Freshness.SourceUpdatedAt.IsZero() {
		return fmt.Errorf("generated and source timestamps are required")
	}
	if snapshot.Freshness.StaleAfterSeconds <= 0 {
		return fmt.Errorf("domain stale threshold must be positive")
	}
	states := []struct {
		name  string
		state SourceState
	}{
		{"domain", snapshot.Freshness.SourceState},
		{"runtime freshness", snapshot.Runtime.Freshness.SourceState},
		{"runtime", snapshot.Runtime.State},
		{"local backup", snapshot.LocalBackup.SourceState},
		{"cloud snapshot", snapshot.CloudSnapshot.SourceState},
		{"coverage", snapshot.Coverage.SourceState},
		{"network freshness", snapshot.Network.Freshness.SourceState},
		{"cloud cache", snapshot.Network.Cloud.SourceState},
		{"activity freshness", snapshot.Activity.Freshness.SourceState},
	}
	for _, item := range states {
		if err := validateSourceState(item.name, item.state); err != nil {
			return err
		}
	}
	if snapshot.Node.Key == "" || !safeTokenPattern.MatchString(snapshot.Node.Key) || len(snapshot.Node.Key) > MaxNodeKeyLength {
		return fmt.Errorf("node key is invalid")
	}
	if _, err := SanitizeLabel(snapshot.Node.Label, MaxNodeLabelLength); err != nil {
		return fmt.Errorf("node label: %w", err)
	}
	return nil
}

func (metric Metric) Validate(name string) error {
	if err := validateSourceState(name, metric.SourceState); err != nil {
		return err
	}
	if metric.Severity != "" && !validSeverity(metric.Severity) {
		return fmt.Errorf("%s severity %q is invalid", name, metric.Severity)
	}
	if metric.Available {
		if !validMetricSeverity(metric.Severity) {
			return fmt.Errorf("%s available metric requires healthy, warning, or critical severity", name)
		}
		if metric.Value == nil || math.IsNaN(*metric.Value) || math.IsInf(*metric.Value, 0) {
			return fmt.Errorf("%s available metric requires a finite value", name)
		}
		if metric.SampledAt.IsZero() || metric.StaleAfterSeconds <= 0 {
			return fmt.Errorf("%s available metric requires sampling and stale metadata", name)
		}
	} else if metric.Value != nil {
		return fmt.Errorf("%s unavailable metric must not carry a numeric value", name)
	}
	return nil
}
