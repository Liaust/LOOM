package workers

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"
)

const (
	TickModeManual     = "manual"
	TickModeInterval   = "interval"
	TickModeDailyLocal = "daily_local"

	MinimumIntervalSeconds  = 5
	TickPolicySchemaVersion = "worker_tick_policy.v0.2"
)

type TickPolicy struct {
	SchemaVersion   string `json:"schema_version"`
	Mode            string `json:"mode"`
	IntervalSeconds int    `json:"interval_seconds,omitempty"`
	RunOnStartup    bool   `json:"run_on_startup,omitempty"`
	LocalTime       string `json:"local_time,omitempty"`
	Timezone        string `json:"timezone,omitempty"`
}

type FreshnessDeadlines struct {
	WarningAfter  time.Duration
	CriticalAfter time.Duration
}

// FreshnessWindow derives deterministic display deadlines without consulting
// worker runtime state. Manual workers have no schedule-derived freshness.
func (p TickPolicy) FreshnessWindow() FreshnessDeadlines {
	if p.Mode != TickModeInterval || p.IntervalSeconds <= 0 {
		return FreshnessDeadlines{}
	}
	interval := time.Duration(p.IntervalSeconds) * time.Second
	return FreshnessDeadlines{WarningAfter: 2 * interval, CriticalAfter: 3 * interval}
}

func ParseTickPolicy(raw json.RawMessage) (TickPolicy, error) {
	raw = json.RawMessage(strings.TrimSpace(string(raw)))
	if len(raw) == 0 || string(raw) == "{}" {
		return TickPolicy{SchemaVersion: TickPolicySchemaVersion, Mode: TickModeManual}, nil
	}

	var payload struct {
		Mode            string  `json:"mode"`
		IntervalSeconds *int    `json:"interval_seconds"`
		RunOnStartup    *bool   `json:"run_on_startup"`
		LocalTime       *string `json:"local_time"`
		Timezone        *string `json:"timezone"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return TickPolicy{}, fmt.Errorf("%w: tick_policy_json is invalid JSON: %w", ErrInvalid, err)
	}

	policy := TickPolicy{
		SchemaVersion: TickPolicySchemaVersion,
		Mode:          strings.TrimSpace(payload.Mode),
	}
	if payload.IntervalSeconds != nil {
		policy.IntervalSeconds = *payload.IntervalSeconds
	}
	if payload.RunOnStartup != nil {
		policy.RunOnStartup = *payload.RunOnStartup
	}
	if payload.LocalTime != nil {
		policy.LocalTime = *payload.LocalTime
	}
	if payload.Timezone != nil {
		policy.Timezone = *payload.Timezone
	}
	if policy.Mode == "" {
		policy.Mode = TickModeManual
	}

	switch policy.Mode {
	case TickModeManual:
		policy.IntervalSeconds = 0
		policy.RunOnStartup = false
		policy.LocalTime = ""
		policy.Timezone = ""
		return policy, nil
	case TickModeInterval:
		if payload.LocalTime != nil || payload.Timezone != nil {
			return TickPolicy{}, fmt.Errorf("%w: interval tick policy forbids local_time and timezone", ErrInvalid)
		}
		if policy.IntervalSeconds <= 0 {
			return TickPolicy{}, fmt.Errorf("%w: interval tick policy requires interval_seconds > 0", ErrInvalid)
		}
		if policy.IntervalSeconds < MinimumIntervalSeconds {
			return TickPolicy{}, fmt.Errorf("%w: interval tick policy interval_seconds must be at least %d", ErrInvalid, MinimumIntervalSeconds)
		}
		if uint64(policy.IntervalSeconds) > uint64(math.MaxInt64/(3*int64(time.Second))) {
			return TickPolicy{}, fmt.Errorf("%w: interval tick policy interval_seconds overflows derived scheduling durations", ErrInvalid)
		}
		return policy, nil
	case TickModeDailyLocal:
		if payload.IntervalSeconds != nil || payload.RunOnStartup != nil {
			return TickPolicy{}, fmt.Errorf("%w: daily_local tick policy forbids interval_seconds and run_on_startup", ErrInvalid)
		}
		if _, _, err := parseDailyLocalTime(policy.LocalTime); err != nil {
			return TickPolicy{}, err
		}
		if policy.Timezone == "" || policy.Timezone == "Local" || policy.Timezone != strings.TrimSpace(policy.Timezone) {
			return TickPolicy{}, fmt.Errorf("%w: daily_local tick policy requires an exact IANA timezone", ErrInvalid)
		}
		if _, err := time.LoadLocation(policy.Timezone); err != nil {
			return TickPolicy{}, fmt.Errorf("%w: daily_local tick policy timezone %q is not loadable: %v", ErrInvalid, policy.Timezone, err)
		}
		return policy, nil
	default:
		return TickPolicy{}, fmt.Errorf("%w: tick policy mode %q is invalid", ErrInvalid, policy.Mode)
	}
}

func NormalizeTickPolicy(policy TickPolicy) (TickPolicy, json.RawMessage, error) {
	if strings.TrimSpace(policy.Mode) == "" {
		return TickPolicy{}, nil, fmt.Errorf("%w: tick policy mode is required", ErrInvalid)
	}
	raw, err := json.Marshal(policy)
	if err != nil {
		return TickPolicy{}, nil, fmt.Errorf("%w: encode tick policy: %v", ErrInvalid, err)
	}
	normalized, err := ParseTickPolicy(raw)
	if err != nil {
		return TickPolicy{}, nil, err
	}
	raw, err = json.Marshal(normalized)
	if err != nil {
		return TickPolicy{}, nil, fmt.Errorf("%w: encode normalized tick policy: %v", ErrInvalid, err)
	}
	return normalized, raw, nil
}

func TickPolicyFingerprint(raw json.RawMessage) (string, error) {
	policy, err := ParseTickPolicy(raw)
	if err != nil {
		return "", err
	}
	_, normalized, err := NormalizeTickPolicy(policy)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(normalized)
	return fmt.Sprintf("sha256:%x", sum[:]), nil
}

func (p TickPolicy) NextAfter(now time.Time) *time.Time {
	switch p.Mode {
	case TickModeInterval:
		if p.IntervalSeconds <= 0 {
			return nil
		}
		next := now.UTC().Add(time.Duration(p.IntervalSeconds) * time.Second)
		return &next
	case TickModeDailyLocal:
		hour, minute, err := parseDailyLocalTime(p.LocalTime)
		if err != nil {
			return nil
		}
		location, err := time.LoadLocation(p.Timezone)
		if err != nil {
			return nil
		}
		next, ok := nextDailyLocalOccurrence(now.UTC(), location, hour, minute)
		if !ok {
			return nil
		}
		return &next
	default:
		return nil
	}
}

func (p TickPolicy) Due(now time.Time, nextRunAfter *time.Time) bool {
	if p.Mode != TickModeInterval && p.Mode != TickModeDailyLocal {
		return false
	}
	if nextRunAfter == nil {
		return p.Mode == TickModeInterval && p.RunOnStartup
	}
	return !nextRunAfter.After(now.UTC())
}

func parseDailyLocalTime(value string) (int, int, error) {
	if len(value) != len("HH:MM") || value[2] != ':' || value != strings.TrimSpace(value) ||
		value[0] < '0' || value[0] > '9' || value[1] < '0' || value[1] > '9' ||
		value[3] < '0' || value[3] > '9' || value[4] < '0' || value[4] > '9' {
		return 0, 0, fmt.Errorf("%w: daily_local tick policy local_time must use strict HH:MM", ErrInvalid)
	}
	hour := int(value[0]-'0')*10 + int(value[1]-'0')
	minute := int(value[3]-'0')*10 + int(value[4]-'0')
	if hour > 23 || minute > 59 {
		return 0, 0, fmt.Errorf("%w: daily_local tick policy local_time must use strict HH:MM", ErrInvalid)
	}
	return hour, minute, nil
}

// nextDailyLocalOccurrence returns at most one occurrence per local calendar
// day. On a spring-forward gap the nonexistent wall time is skipped; on a
// fall-back day the first occurrence owns that calendar day's fire.
func nextDailyLocalOccurrence(now time.Time, location *time.Location, hour, minute int) (time.Time, bool) {
	localNow := now.In(location)
	for dayOffset := 0; dayOffset < 8; dayOffset++ {
		date := time.Date(localNow.Year(), localNow.Month(), localNow.Day()+dayOffset, 12, 0, 0, 0, location)
		candidate, ok := firstLocalWallClockOccurrence(date, location, hour, minute)
		if ok && candidate.After(now) {
			return candidate.UTC(), true
		}
	}
	return time.Time{}, false
}

func firstLocalWallClockOccurrence(date time.Time, location *time.Location, hour, minute int) (time.Time, bool) {
	year, month, day := date.In(location).Date()
	anchor := time.Date(year, month, day, 12, 0, 0, 0, time.UTC)
	for candidate := anchor.Add(-36 * time.Hour); !candidate.After(anchor.Add(36 * time.Hour)); candidate = candidate.Add(time.Minute) {
		local := candidate.In(location)
		if local.Year() == year && local.Month() == month && local.Day() == day && local.Hour() == hour && local.Minute() == minute {
			return candidate, true
		}
	}
	return time.Time{}, false
}
