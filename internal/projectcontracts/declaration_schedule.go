package projectcontracts

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"loom.local/loom/internal/automation"
	"loom.local/loom/internal/capabilities"
)

func ScheduleDeclarationInput(project ProjectSpec, key ResourceKey, s ScheduleDeclaration) (automation.CreateScheduleInput, error) {
	fail := func(message string) (automation.CreateScheduleInput, error) {
		return automation.CreateScheduleInput{}, fmt.Errorf("%s", message)
	}
	target, err := capabilities.NormalizeAddress(s.Target)
	if err != nil {
		return fail("target must be a capability address")
	}
	kind, expr, count := "", "", 0
	for _, pair := range [][2]string{{automation.ScheduleKindCron, s.Cron}, {automation.ScheduleKindInterval, s.Every}, {automation.ScheduleKindOneShot, s.At}} {
		if pair[1] != "" {
			kind, expr = pair[0], pair[1]
			count++
		}
	}
	if count != 1 {
		return fail("provide exactly one of cron, every or at")
	}
	if kind == automation.ScheduleKindOneShot {
		if _, err := time.Parse(time.RFC3339, expr); err != nil {
			return fail("at must be an absolute RFC3339 time, not now")
		}
	}
	zone := s.Timezone
	if zone == "" && kind != automation.ScheduleKindCron {
		zone = "UTC"
	}
	if zone == "Local" {
		return fail("timezone must be an explicit IANA zone")
	}
	if zone != "" {
		if _, err := time.LoadLocation(zone); err != nil {
			return fail("timezone must be a loadable IANA zone")
		}
	}
	if _, err := automation.ParseScheduleExpressionInTimezone(kind, expr, zone, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)); err != nil {
		return fail(err.Error())
	}
	status := s.Status
	if status == "" {
		status = automation.ScheduleStatusDisabled
	}
	if status != "active" && status != "paused" && status != "disabled" {
		return fail("status must be active, paused or disabled")
	}
	input := s.InputJSON
	if input == "" {
		input = "{}"
	}
	value, err := DecodeDeclarationEvidenceJSON([]byte(input))
	if _, ok := value.(map[string]any); err != nil || !ok || len(input) > 65536 {
		return fail("input_json must be a JSON object of at most 65536 bytes")
	}
	// Compact without decoding numbers through float64.
	raw, err := json.Marshal(value)
	if err != nil {
		return fail("input_json is invalid")
	}
	runAs := s.RunAs
	if runAs == "" || runAs == "scheduler" {
		runAs = "scheduler:loom"
	}
	if !runAsPattern.MatchString(runAs) || runAs == "owner" {
		return fail("run_as must name an actor, or scheduler")
	}
	if s.TimeoutSeconds < 0 || s.TimeoutSeconds > 86400 || s.MaxAttempts < 0 || s.MaxAttempts > 10 || s.LatenessWindowSeconds < 0 || s.LatenessWindowSeconds > 86400 {
		return fail("runtime limits are out of range")
	}
	timeout, attempts, late := s.TimeoutSeconds, s.MaxAttempts, s.LatenessWindowSeconds
	if timeout == 0 {
		timeout = 300
	}
	if attempts == 0 {
		attempts = 1
	}
	if late == 0 {
		late = 300
	}
	return automation.CreateScheduleInput{
		ScheduleKey: ProjectScheduleKey(project.Slug, string(key)), DisplayName: project.Name + " / " + string(key),
		ProjectRef: project.ID, ScopeRef: "project:" + project.Slug, RunAsActorRef: runAs,
		TargetCapability: target, InputJSON: raw, ScheduleKind: kind, ScheduleExpr: strings.TrimSpace(expr), Timezone: zone,
		Status: status, TimeoutSeconds: timeout, MaxAttempts: attempts,
		MisfirePolicy: automation.MisfireRunIfLateWithin, LatenessWindowSecs: late, ConcurrencyPolicy: automation.ConcurrencySkipIfPending,
	}, nil
}
