package automation

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"
)

var keyPattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,80}$`)

func NormalizeTargetProfile(raw json.RawMessage) (TargetProfile, error) {
	raw, err := normalizeJSONObject(raw, "target_profile_json")
	if err != nil {
		return TargetProfile{}, err
	}
	var profile TargetProfile
	if err := json.Unmarshal(raw, &profile); err != nil {
		return TargetProfile{}, fmt.Errorf("target profile is invalid JSON: %w", err)
	}
	profile.CapabilityRef = strings.TrimSpace(profile.CapabilityRef)
	profile.ScopeRef = strings.TrimSpace(profile.ScopeRef)
	profile.ProjectRef = strings.TrimSpace(profile.ProjectRef)
	if profile.CapabilityRef == "" {
		return TargetProfile{}, fmt.Errorf("target capability_ref is required")
	}
	return profile, nil
}

func NormalizeMisfireProfile(raw json.RawMessage) (MisfireProfile, error) {
	raw, err := normalizeJSONObject(raw, "misfire_profile_json")
	if err != nil {
		return MisfireProfile{}, err
	}
	profile := MisfireProfile{
		Policy:                MisfireMarkMissed,
		LatenessWindowSeconds: defaultLatenessWindowSecs,
	}
	if err := json.Unmarshal(raw, &profile); err != nil {
		return MisfireProfile{}, fmt.Errorf("misfire profile is invalid JSON: %w", err)
	}
	profile.Policy = strings.TrimSpace(profile.Policy)
	if profile.Policy == "" {
		profile.Policy = MisfireMarkMissed
	}
	switch profile.Policy {
	case MisfireMarkMissed:
		profile.LatenessWindowSeconds = 0
	case MisfireRunIfLateWithin:
		if profile.LatenessWindowSeconds <= 0 {
			return MisfireProfile{}, fmt.Errorf("lateness_window_seconds must be positive for %s", MisfireRunIfLateWithin)
		}
	default:
		return MisfireProfile{}, fmt.Errorf("unsupported misfire policy: %s", profile.Policy)
	}
	return profile, nil
}

func NormalizeConcurrencyProfile(raw json.RawMessage) (ConcurrencyProfile, error) {
	raw, err := normalizeJSONObject(raw, "concurrency_profile_json")
	if err != nil {
		return ConcurrencyProfile{}, err
	}
	profile := ConcurrencyProfile{Policy: ConcurrencyAllowParallel}
	if err := json.Unmarshal(raw, &profile); err != nil {
		return ConcurrencyProfile{}, fmt.Errorf("concurrency profile is invalid JSON: %w", err)
	}
	profile.Policy = strings.TrimSpace(profile.Policy)
	if profile.Policy == "" {
		profile.Policy = ConcurrencyAllowParallel
	}
	switch profile.Policy {
	case ConcurrencyAllowParallel, ConcurrencySkipIfPending:
		return profile, nil
	default:
		return ConcurrencyProfile{}, fmt.Errorf("unsupported concurrency policy: %s", profile.Policy)
	}
}

func NormalizeTimeoutProfile(raw json.RawMessage) (TimeoutProfile, error) {
	raw, err := normalizeJSONObject(raw, "timeout_profile_json")
	if err != nil {
		return TimeoutProfile{}, err
	}
	profile := TimeoutProfile{TimeoutSeconds: defaultTimeoutSeconds}
	if err := json.Unmarshal(raw, &profile); err != nil {
		return TimeoutProfile{}, fmt.Errorf("timeout profile is invalid JSON: %w", err)
	}
	if profile.TimeoutSeconds <= 0 {
		profile.TimeoutSeconds = defaultTimeoutSeconds
	}
	if profile.TimeoutSeconds > maxTimeoutSeconds {
		return TimeoutProfile{}, fmt.Errorf("timeout_seconds must be <= %d", maxTimeoutSeconds)
	}
	return profile, nil
}

func NormalizeRetryProfile(raw json.RawMessage) (RetryProfile, error) {
	raw, err := normalizeJSONObject(raw, "retry_profile_json")
	if err != nil {
		return RetryProfile{}, err
	}
	profile := RetryProfile{MaxAttempts: defaultMaxAttempts}
	if err := json.Unmarshal(raw, &profile); err != nil {
		return RetryProfile{}, fmt.Errorf("retry profile is invalid JSON: %w", err)
	}
	if profile.MaxAttempts <= 0 {
		profile.MaxAttempts = defaultMaxAttempts
	}
	if profile.MaxAttempts > 25 {
		return RetryProfile{}, fmt.Errorf("max_attempts must be <= 25")
	}
	return profile, nil
}

func NormalizeApprovalProfile(raw json.RawMessage) (ApprovalProfile, error) {
	raw, err := normalizeJSONObject(raw, "approval_profile_json")
	if err != nil {
		return ApprovalProfile{}, err
	}
	profile := ApprovalProfile{Mode: ApprovalNone}
	if err := json.Unmarshal(raw, &profile); err != nil {
		return ApprovalProfile{}, fmt.Errorf("approval profile is invalid JSON: %w", err)
	}
	profile.Mode = strings.TrimSpace(profile.Mode)
	if profile.Mode == "" {
		profile.Mode = ApprovalNone
	}
	switch profile.Mode {
	case ApprovalNone, ApprovalCreateAndWait, ApprovalFailIfRequired:
		return profile, nil
	default:
		return ApprovalProfile{}, fmt.Errorf("unsupported approval mode: %s", profile.Mode)
	}
}

// ParseScheduleExpression preserves the original non-calendar API. Cron callers
// must use ParseScheduleExpressionInTimezone with an explicit timezone.
func ParseScheduleExpression(kind, expr string, now time.Time) (SchedulePlan, error) {
	return ParseScheduleExpressionInTimezone(kind, expr, "", now)
}

func ParseScheduleExpressionInTimezone(kind, expr, timezone string, now time.Time) (SchedulePlan, error) {
	if strings.TrimSpace(kind) == ScheduleKindCron && len(expr) > maxCalendarExpressionBytes {
		return SchedulePlan{}, fmt.Errorf("cron schedule_expr must be at most %d bytes", maxCalendarExpressionBytes)
	}
	kind = strings.TrimSpace(kind)
	expr = strings.TrimSpace(expr)
	if now.IsZero() {
		now = time.Now().UTC()
	}
	now = now.UTC()
	switch kind {
	case ScheduleKindCron:
		next, err := nextCalendarFire(expr, timezone, now)
		if err != nil {
			return SchedulePlan{}, err
		}
		return SchedulePlan{Kind: kind, Expr: expr, StartAt: &now, NextFireAt: &next}, nil
	case ScheduleKindOneShot:
		if expr == "" {
			return SchedulePlan{}, fmt.Errorf("one_shot schedule_expr is required")
		}
		var fireAt time.Time
		if expr == "now" {
			fireAt = now
		} else {
			parsed, err := time.Parse(time.RFC3339, expr)
			if err != nil {
				return SchedulePlan{}, fmt.Errorf("one_shot schedule_expr must be RFC3339 or now: %w", err)
			}
			fireAt = parsed.UTC()
		}
		return SchedulePlan{Kind: kind, Expr: expr, StartAt: &fireAt, NextFireAt: &fireAt}, nil
	case ScheduleKindInterval:
		duration, err := time.ParseDuration(expr)
		if err != nil {
			return SchedulePlan{}, fmt.Errorf("interval schedule_expr must be a Go duration: %w", err)
		}
		if duration < time.Second {
			return SchedulePlan{}, fmt.Errorf("interval schedule_expr must be at least 1s")
		}
		fireAt := now.Add(duration).UTC()
		startAt := now
		return SchedulePlan{Kind: kind, Expr: expr, StartAt: &startAt, NextFireAt: &fireAt}, nil
	default:
		return SchedulePlan{}, fmt.Errorf("unsupported schedule_kind: %s", kind)
	}
}

func ComputeNextFire(schedule Schedule, after time.Time) (*time.Time, error) {
	after = after.UTC()
	switch schedule.ScheduleKind {
	case ScheduleKindCron:
		next, err := nextCalendarFire(schedule.ScheduleExpr, schedule.Timezone, after)
		if err != nil {
			return nil, err
		}
		return &next, nil
	case ScheduleKindOneShot:
		return nil, nil
	case ScheduleKindInterval:
		duration, err := time.ParseDuration(strings.TrimSpace(schedule.ScheduleExpr))
		if err != nil {
			return nil, fmt.Errorf("interval schedule_expr must be a Go duration: %w", err)
		}
		if duration < time.Second {
			return nil, fmt.Errorf("interval schedule_expr must be at least 1s")
		}
		next := after.Add(duration).UTC()
		return &next, nil
	default:
		return nil, fmt.Errorf("unsupported schedule_kind: %s", schedule.ScheduleKind)
	}
}

func normalizeJSONObject(raw json.RawMessage, name string) (json.RawMessage, error) {
	raw = json.RawMessage(strings.TrimSpace(string(raw)))
	if len(raw) == 0 || string(raw) == "null" {
		return json.RawMessage(`{}`), nil
	}
	var object map[string]any
	if err := json.Unmarshal(raw, &object); err != nil {
		return nil, fmt.Errorf("%s must be valid JSON object: %w", name, err)
	}
	if object == nil {
		return nil, fmt.Errorf("%s must be a JSON object", name)
	}
	normalized, err := json.Marshal(object)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(normalized), nil
}

func normalizeJSONArray(raw json.RawMessage, name string) (json.RawMessage, error) {
	raw = json.RawMessage(strings.TrimSpace(string(raw)))
	if len(raw) == 0 || string(raw) == "null" {
		return json.RawMessage(`[]`), nil
	}
	var array []any
	if err := json.Unmarshal(raw, &array); err != nil {
		return nil, fmt.Errorf("%s must be valid JSON array: %w", name, err)
	}
	if array == nil {
		return nil, fmt.Errorf("%s must be a JSON array", name)
	}
	normalized, err := json.Marshal(array)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(normalized), nil
}

func mustJSON(value any) json.RawMessage {
	raw, err := json.Marshal(value)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return json.RawMessage(raw)
}

func objectHash(raw json.RawMessage) (string, error) {
	normalized, err := normalizeJSONObject(raw, "input_json")
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(normalized)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func validKey(value string) bool {
	return keyPattern.MatchString(value)
}
