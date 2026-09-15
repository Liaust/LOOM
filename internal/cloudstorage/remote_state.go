package cloudstorage

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	RemoteStateSchemaVersion = "loom.cloud.remote_status.v0.6.8"

	RemoteStateHealthy     = "healthy"
	RemoteStateDegraded    = "degraded"
	RemoteStateCoolingDown = "cooling_down"
	RemoteStateDisabled    = "disabled"
	RemoteStateUnknown     = "unknown"

	RemoteErrorConnectionRefused  = "connection_refused"
	RemoteErrorNetworkUnreachable = "network_unreachable"
	RemoteErrorTimeout            = "timeout"
	RemoteErrorAuthFailed         = "auth_failed"
	RemoteErrorPermissionLocal    = "permission_denied_local"
	RemoteErrorRemoteMissing      = "remote_missing"
	RemoteErrorUnknown            = "unknown"
)

type RemoteState struct {
	SchemaVersion       string       `json:"schema_version"`
	RemoteURI           string       `json:"remote_uri"`
	State               string       `json:"state"`
	LastLiveCheckAt     *time.Time   `json:"last_live_check_at,omitempty"`
	LastSuccessAt       *time.Time   `json:"last_success_at,omitempty"`
	LastFailureAt       *time.Time   `json:"last_failure_at,omitempty"`
	NextLiveCheckAfter  *time.Time   `json:"next_live_check_after,omitempty"`
	FailureCount        int          `json:"failure_count"`
	LastErrorClass      string       `json:"last_error_class,omitempty"`
	LastError           string       `json:"last_error,omitempty"`
	LastRemoteEntries   int          `json:"last_remote_entries,omitempty"`
	LastRoots           []RootStatus `json:"last_roots,omitempty"`
	UpdatedBy           string       `json:"updated_by,omitempty"`
	UpdatedAt           time.Time    `json:"updated_at"`
	SourceCached        bool         `json:"source_cached,omitempty"`
	LiveProbeAllowed    bool         `json:"live_probe_allowed,omitempty"`
	LiveProbeSkipReason string       `json:"live_probe_skip_reason,omitempty"`
}

type RemoteProbeResult struct {
	Status     RemoteStatus
	Entries    []RemoteEntry
	ErrorClass string
}

func RemoteStatePath(cfg Config) string {
	stateDir := strings.TrimSpace(cfg.StateDir)
	if stateDir == "" {
		stateDir = DefaultStateDir
	}
	return filepath.Join(stateDir, "status", "cache.json")
}

func LoadRemoteState(cfg Config) (RemoteState, error) {
	path := RemoteStatePath(cfg)
	payload, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return defaultRemoteState(cfg, time.Time{}), nil
		}
		return RemoteState{}, err
	}
	var state RemoteState
	if err := json.Unmarshal(payload, &state); err != nil {
		return RemoteState{}, err
	}
	normalizeRemoteState(&state, cfg)
	state.SourceCached = true
	return state, nil
}

func SaveRemoteState(cfg Config, state RemoteState) error {
	normalizeRemoteState(&state, cfg)
	path := RemoteStatePath(cfg)
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	payload, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(payload, '\n'), 0o640)
}

func RecordRemoteSuccess(cfg Config, probe RemoteProbeResult) error {
	return RecordRemoteSuccessAt(cfg, probe, time.Now().UTC(), "cloud.live_probe")
}

func RecordRemoteSuccessAt(cfg Config, probe RemoteProbeResult, now time.Time, updatedBy string) error {
	now = now.UTC()
	state := defaultRemoteState(cfg, now)
	state.State = RemoteStateHealthy
	state.LastLiveCheckAt = &now
	state.LastSuccessAt = &now
	state.NextLiveCheckAfter = nil
	state.FailureCount = 0
	state.LastErrorClass = ""
	state.LastError = ""
	state.LastRemoteEntries = probe.Status.Entries
	if state.LastRemoteEntries == 0 && len(probe.Entries) > 0 {
		state.LastRemoteEntries = len(probe.Entries)
	}
	state.LastRoots = rootStatuses(cfg, probe.Entries)
	state.UpdatedBy = firstNonEmptyCloud(updatedBy, "cloud.live_probe")
	return SaveRemoteState(cfg, state)
}

func RecordRemoteTransportSuccessAt(cfg Config, now time.Time, updatedBy string) error {
	now = now.UTC()
	state, loadErr := LoadRemoteState(cfg)
	if loadErr != nil {
		state = defaultRemoteState(cfg, now)
	}
	state.SchemaVersion = RemoteStateSchemaVersion
	state.RemoteURI = cfg.RemoteURI("")
	state.State = RemoteStateHealthy
	state.LastLiveCheckAt = &now
	state.LastSuccessAt = &now
	state.NextLiveCheckAfter = nil
	state.FailureCount = 0
	state.LastErrorClass = ""
	state.LastError = ""
	state.UpdatedAt = now
	state.UpdatedBy = firstNonEmptyCloud(updatedBy, "cloud.live_probe")
	return SaveRemoteState(cfg, state)
}

func RecordRemoteFailure(cfg Config, err error) error {
	return RecordRemoteFailureAt(cfg, err, time.Now().UTC(), "cloud.live_probe")
}

func RecordRemoteFailureAt(cfg Config, err error, now time.Time, updatedBy string) error {
	now = now.UTC()
	state, loadErr := LoadRemoteState(cfg)
	if loadErr != nil {
		state = defaultRemoteState(cfg, now)
	}
	class := ClassifyRemoteError(err)
	state.SchemaVersion = RemoteStateSchemaVersion
	state.RemoteURI = cfg.RemoteURI("")
	state.LastLiveCheckAt = &now
	state.LastFailureAt = &now
	state.LastErrorClass = class
	if err != nil {
		state.LastError = err.Error()
	}
	state.UpdatedAt = now
	state.UpdatedBy = firstNonEmptyCloud(updatedBy, "cloud.live_probe")
	switch class {
	case RemoteErrorConnectionRefused, RemoteErrorNetworkUnreachable, RemoteErrorTimeout:
		state.State = RemoteStateCoolingDown
		state.FailureCount++
		next := now.Add(cooldownForFailureCount(state.FailureCount))
		state.NextLiveCheckAfter = &next
	case RemoteErrorAuthFailed, RemoteErrorPermissionLocal, RemoteErrorRemoteMissing:
		state.State = RemoteStateDegraded
		state.FailureCount++
		state.NextLiveCheckAfter = nil
	default:
		state.State = RemoteStateDegraded
		state.FailureCount++
		state.NextLiveCheckAfter = nil
	}
	return SaveRemoteState(cfg, state)
}

func CanLiveProbe(state RemoteState, now time.Time) bool {
	if state.State != RemoteStateCoolingDown {
		return true
	}
	if state.NextLiveCheckAfter == nil {
		return true
	}
	return !now.UTC().Before(state.NextLiveCheckAfter.UTC())
}

func ClassifyRemoteError(err error) string {
	if err == nil {
		return ""
	}
	text := strings.ToLower(err.Error())
	switch {
	case strings.Contains(text, "connection refused"):
		return RemoteErrorConnectionRefused
	case strings.Contains(text, "network is unreachable"), strings.Contains(text, "no route to host"):
		return RemoteErrorNetworkUnreachable
	case strings.Contains(text, "i/o timeout"), strings.Contains(text, "timed out"), strings.Contains(text, "timeout"):
		return RemoteErrorTimeout
	case strings.Contains(text, "permission denied") && (strings.Contains(text, "private key") || strings.Contains(text, "key file") || strings.Contains(text, "open /")):
		return RemoteErrorPermissionLocal
	case strings.Contains(text, "permission denied"), strings.Contains(text, "authentication failed"), strings.Contains(text, "auth failed"), strings.Contains(text, "nt_status_logon_failure"):
		return RemoteErrorAuthFailed
	case strings.Contains(text, "file does not exist"), strings.Contains(text, "not found"), strings.Contains(text, "no such file"):
		return RemoteErrorRemoteMissing
	default:
		return RemoteErrorUnknown
	}
}

func ServiceContextHint(err error) string {
	switch ClassifyRemoteError(err) {
	case RemoteErrorPermissionLocal:
		return "Live cloud checks require the LOOM service context because cloud SSH keys or passphrases are not readable by this user. Run this through loomd/service automation, or run the command as the configured LOOM service user after reviewing credential permissions."
	case RemoteErrorAuthFailed:
		return "Cloud authentication failed. Check the service cloud credentials and run live cloud checks from the LOOM service context."
	default:
		return ""
	}
}

func defaultRemoteState(cfg Config, now time.Time) RemoteState {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	state := RemoteState{
		SchemaVersion:    RemoteStateSchemaVersion,
		RemoteURI:        cfg.RemoteURI(""),
		State:            RemoteStateUnknown,
		UpdatedAt:        now.UTC(),
		LiveProbeAllowed: true,
	}
	return state
}

func normalizeRemoteState(state *RemoteState, cfg Config) {
	if strings.TrimSpace(state.SchemaVersion) == "" {
		state.SchemaVersion = RemoteStateSchemaVersion
	}
	if strings.TrimSpace(state.RemoteURI) == "" {
		state.RemoteURI = cfg.RemoteURI("")
	}
	if strings.TrimSpace(state.State) == "" {
		state.State = RemoteStateUnknown
	}
	if state.UpdatedAt.IsZero() {
		state.UpdatedAt = time.Now().UTC()
	}
	state.LiveProbeAllowed = CanLiveProbe(*state, time.Now().UTC())
	if !state.LiveProbeAllowed {
		state.LiveProbeSkipReason = RemoteStateCoolingDown
	}
}

func cooldownForFailureCount(count int) time.Duration {
	switch {
	case count <= 1:
		return 30 * time.Minute
	case count == 2:
		return time.Hour
	case count == 3:
		return 2 * time.Hour
	default:
		return 6 * time.Hour
	}
}

func firstNonEmptyCloud(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
