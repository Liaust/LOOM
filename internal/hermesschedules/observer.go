// Package hermesschedules observes the pinned Hermes cron store without running
// Hermes, repairing its files, or taking ownership of its timers.
package hermesschedules

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	NativeRevision       = "29112bef099274229cadff79cdff7bf7b99c4b77"
	MaxBytes       int64 = 4 << 20
	ReadTimeout          = 5 * time.Second
)

var (
	ErrUnavailable = errors.New("Hermes schedule source unavailable")
	ErrMalformed   = errors.New("Hermes schedule source malformed")
	ErrTooLarge    = errors.New("Hermes schedule source exceeds size limit")
	ErrRevision    = errors.New("Hermes schedule source revision unsupported")
	ErrSource      = errors.New("Hermes schedule source requires host, profile, and reader")
)

// Source is supplied by the integrator from trusted runtime configuration.
// Host and Profile are identity labels, not values inferred from job payloads.
// Revision must be the deployed source revision; jobs.json has no version field.
type Source struct {
	Host     string `json:"host"`
	Profile  string `json:"profile"`
	Revision string `json:"revision"`
}

// Reader reads one selected profile's cron/jobs.json. It must honor ctx, enforce
// limit while reading, and perform no writes. Observe also checks the returned
// size and context. Remote implementations must bound their transport themselves.
type Reader func(ctx context.Context, limit int64) ([]byte, error)

type Availability string

const (
	Available   Availability = "available"
	Unavailable Availability = "unavailable"
	Malformed   Availability = "malformed"
	Unsupported Availability = "unsupported"
)

// Observation is an inventory snapshot, not scheduler liveness. ObservedAt is
// set only after a successful read and parse; StoreUpdatedAt is native evidence
// of a store write, not proof that a gateway is running. Jobs is nil on failure
// and a non-nil empty slice only for an explicit {"jobs":[]}.
type Observation struct {
	Owner          string       `json:"owner"`
	Source         Source       `json:"source"`
	Availability   Availability `json:"availability"`
	AttemptedAt    time.Time    `json:"attempted_at"`
	ObservedAt     *time.Time   `json:"observed_at"`
	StoreUpdatedAt *time.Time   `json:"store_updated_at"`
	Jobs           []Job        `json:"jobs"`
}

type State string

const (
	StateUnknown State = "unknown"
	Scheduled    State = "scheduled"
	Paused       State = "paused"
	Completed    State = "completed"
	StateError   State = "error"
)

type ScheduleKind string

const (
	ScheduleUnknown ScheduleKind = "unknown"
	Cron            ScheduleKind = "cron"
	Interval        ScheduleKind = "interval"
	Once            ScheduleKind = "once"
)

type Schedule struct {
	Kind       ScheduleKind `json:"kind"`
	Expression *string      `json:"expression"`
	Minutes    *int64       `json:"minutes"`
	RunAt      *time.Time   `json:"run_at"`
	// The native job record does not store the effective named timezone.
	Timezone *string `json:"timezone"`
}

type Result string

const (
	ResultUnknown Result = "unknown"
	OK            Result = "ok"
	Error         Result = "error"
	BlockedConfig Result = "blocked_config"
)

// Job exposes only inventory fields. NativeState and Enabled are independent
// stored evidence, not reconstructed scheduler eligibility. A completed state
// or dispatch counter never supplies LastResult. LastResult is Hermes's report,
// not independent verification of the task's effects or the current execution.
type ProjectOwnership struct {
	ProjectID string `json:"project_id"`
	Resource  string `json:"resource"`
	Profile   string `json:"profile"`
	Retired   bool   `json:"retired"`
}

type Job struct {
	Project     *ProjectOwnership `json:"project,omitempty"`
	ID          string            `json:"id"`
	Enabled     *bool             `json:"enabled"`
	NativeState State             `json:"native_state"`
	PausedAt    *time.Time        `json:"paused_at"`
	Schedule    Schedule          `json:"schedule"`
	NextRunAt   *time.Time        `json:"next_run_at"`
	LastRunAt   *time.Time        `json:"last_run_at"`
	LastResult  Result            `json:"last_result"`
	// True means native delivery error evidence exists. Nil means unknown;
	// an absent/null/empty error never proves successful delivery.
	DeliveryErrorReported *bool `json:"delivery_error_reported"`
}

type Observer struct {
	Source Source
	Read   Reader
	// ReadSanitized is the fixed helper IPC path. Exactly one reader is allowed.
	ReadSanitized Reader
}

// Observe never returns raw reader/JSON errors, which may contain paths, prompt
// fragments, or credentials. Context cancellation remains recognizable.
func (o Observer) Observe(ctx context.Context) (Observation, error) {
	out := Observation{Owner: "hermes", Source: o.Source, Availability: Unavailable, AttemptedAt: time.Now().UTC()}
	if err := ctx.Err(); err != nil {
		return out, err
	}
	if o.Source.Revision != NativeRevision {
		out.Availability = Unsupported
		return out, ErrRevision
	}
	if strings.TrimSpace(o.Source.Host) == "" || strings.TrimSpace(o.Source.Profile) == "" || (o.Read == nil && o.ReadSanitized == nil) || (o.Read != nil && o.ReadSanitized != nil) {
		return out, ErrSource
	}
	ctx, cancel := context.WithTimeout(ctx, ReadTimeout)
	defer cancel()
	reader := o.Read
	if o.ReadSanitized != nil {
		reader = o.ReadSanitized
	}
	data, err := reader(ctx, MaxBytes)
	if ctx.Err() != nil {
		return out, ctx.Err()
	}
	if err != nil {
		if errors.Is(err, ErrTooLarge) {
			return out, ErrTooLarge
		}
		return out, ErrUnavailable
	}
	if int64(len(data)) > MaxBytes {
		return out, ErrTooLarge
	}
	if o.ReadSanitized != nil {
		observation, err := decodeObservation(data, o.Source)
		if err != nil {
			out.Availability = Malformed
			return out, ErrMalformed
		}
		if ctx.Err() != nil {
			return out, ctx.Err()
		}
		switch observation.Availability {
		case Available:
			return observation, nil
		case Malformed:
			return observation, ErrMalformed
		case Unsupported:
			return observation, ErrRevision
		default:
			return observation, ErrUnavailable
		}
	}
	jobs, updated, err := parse(ctx, data)
	if err != nil {
		if ctx.Err() != nil {
			return out, ctx.Err()
		}
		out.Availability = Malformed
		return out, ErrMalformed
	}
	if ctx.Err() != nil {
		return out, ctx.Err()
	}
	for _, job := range jobs {
		if job.Project != nil && job.Project.Profile != o.Source.Profile {
			out.Availability = Malformed
			return out, ErrMalformed
		}
	}
	now := time.Now().UTC()
	out.Availability, out.ObservedAt, out.StoreUpdatedAt, out.Jobs = Available, &now, updated, jobs
	return out, nil
}

// This allowlist deliberately excludes name (often prompt-derived), display,
// prompt, error bodies, delivery addresses, origin, model/provider URLs, scripts,
// context_from, and all execution/conversation payloads.
type nativeJob struct {
	Origin *struct {
		Project *ProjectOwnership `json:"loom_project_declaration"`
	} `json:"origin"`
	ID       string  `json:"id"`
	Enabled  *bool   `json:"enabled"`
	State    *string `json:"state"`
	PausedAt *string `json:"paused_at"`
	Schedule *struct {
		Kind    *string `json:"kind"`
		Expr    *string `json:"expr"`
		Minutes *int64  `json:"minutes"`
		RunAt   *string `json:"run_at"`
	} `json:"schedule"`
	NextRunAt         *string `json:"next_run_at"`
	LastRunAt         *string `json:"last_run_at"`
	LastStatus        *string `json:"last_status"`
	LastDeliveryError *string `json:"last_delivery_error"`
}

var identifier = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,128}$`)
var cronText = regexp.MustCompile(`^[A-Za-z0-9*?,/#\s-]{1,256}$`)

func parse(ctx context.Context, data []byte) ([]Job, *time.Time, error) {
	// Hermes reads utf-8-sig; accept its BOM, but never its repairing parser.
	data = bytes.TrimPrefix(data, []byte{0xef, 0xbb, 0xbf})
	if !utf8.Valid(data) {
		return nil, nil, ErrMalformed
	}
	var store struct {
		Jobs      json.RawMessage `json:"jobs"`
		UpdatedAt *string         `json:"updated_at"`
	}
	if json.Unmarshal(data, &store) != nil {
		return nil, nil, ErrMalformed
	}
	raw := bytes.TrimSpace(store.Jobs)
	if len(raw) == 0 || raw[0] != '[' {
		return nil, nil, ErrMalformed
	}
	var rows []nativeJob
	if json.Unmarshal(raw, &rows) != nil {
		return nil, nil, ErrMalformed
	}
	updated, err := timestamp(store.UpdatedAt)
	if err != nil {
		return nil, nil, err
	}
	jobs := make([]Job, 0, len(rows))
	seen := make(map[string]bool, len(rows))
	for _, row := range rows {
		if ctx.Err() != nil {
			return nil, nil, ctx.Err()
		}
		if !identifier.MatchString(row.ID) || seen[row.ID] {
			return nil, nil, ErrMalformed
		}
		seen[row.ID] = true
		job, err := project(row)
		if err != nil {
			return nil, nil, err
		}
		jobs = append(jobs, job)
	}
	return jobs, updated, nil
}

func project(row nativeJob) (Job, error) {
	job := Job{ID: row.ID, Enabled: row.Enabled, NativeState: StateUnknown, LastResult: ResultUnknown, Schedule: Schedule{Kind: ScheduleUnknown}}
	if row.Origin != nil && row.Origin.Project != nil {
		p := row.Origin.Project
		if !projectID.MatchString(p.ProjectID) || !projectKey.MatchString(p.Resource) || p.Profile != "mina" {
			return Job{}, ErrMalformed
		}
		job.Project = p
	}
	if row.State != nil {
		switch State(*row.State) {
		case Scheduled, Paused, Completed, StateError:
			job.NativeState = State(*row.State)
		}
	}
	// Validate timestamps without interpreting naive wall-clock values in this
	// observer's timezone. No schedule calculation or cron parser lives here.
	for _, pair := range []struct {
		raw  *string
		dest **time.Time
	}{
		{row.PausedAt, &job.PausedAt}, {row.NextRunAt, &job.NextRunAt}, {row.LastRunAt, &job.LastRunAt},
	} {
		t, err := timestamp(pair.raw)
		if err != nil {
			return Job{}, err
		}
		*pair.dest = t
	}
	if row.Schedule != nil && row.Schedule.Kind != nil {
		s := row.Schedule
		switch ScheduleKind(*s.Kind) {
		case Cron:
			job.Schedule.Kind = Cron
			if s.Expr != nil && !cronText.MatchString(*s.Expr) {
				return Job{}, ErrMalformed
			}
			job.Schedule.Expression = s.Expr
		case Interval:
			job.Schedule.Kind = Interval
			if s.Minutes != nil && *s.Minutes <= 0 {
				return Job{}, ErrMalformed
			}
			job.Schedule.Minutes = s.Minutes
		case Once:
			job.Schedule.Kind = Once
			t, err := timestamp(s.RunAt)
			if err != nil {
				return Job{}, err
			}
			job.Schedule.RunAt = t
		}
	}
	if row.LastStatus != nil {
		switch Result(*row.LastStatus) {
		case OK, Error, BlockedConfig:
			job.LastResult = Result(*row.LastStatus)
		}
	}
	if row.LastDeliveryError != nil && *row.LastDeliveryError != "" {
		yes := true
		job.DeliveryErrorReported = &yes
	}
	return job, nil
}

func timestamp(raw *string) (*time.Time, error) {
	if raw == nil {
		return nil, nil
	}
	t, err := time.Parse(time.RFC3339Nano, *raw)
	if err != nil {
		return nil, ErrMalformed
	}
	return &t, nil
}
