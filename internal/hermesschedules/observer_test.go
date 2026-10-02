package hermesschedules

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func observer(data string) Observer {
	return Observer{
		Source: Source{Host: "main", Profile: "mina", Revision: NativeRevision},
		Read: func(ctx context.Context, limit int64) ([]byte, error) {
			return []byte(data), nil
		},
	}
}

// Canonical save_jobs/create_job shape, with allowlisted values from the pinned
// native contract. No real profile or prompt data is used in these fixtures.
func TestNativeInventory(t *testing.T) {
	input := `{"updated_at":"2026-09-28T17:00:00.123456+02:00","jobs":[
		{"id":"aabbccddeeff","enabled":true,"state":"scheduled",
		 "schedule":{"kind":"cron","expr":"0 9 * * MON-FRI","display":"weekdays at 9am"},
		 "next_run_at":"2026-09-29T09:00:00+02:00","last_run_at":"2026-09-28T09:02:00+02:00",
		 "last_status":"ok","last_delivery_error":null},
		{"id":"112233445566","enabled":false,"state":"paused","paused_at":"2026-09-28T16:00:00Z",
		 "schedule":{"kind":"interval","minutes":30},"next_run_at":"2026-09-28T17:30:00Z",
		 "last_status":"error","last_delivery_error":"private delivery failure"},
		{"id":"998877665544","enabled":false,"state":"completed","repeat":{"times":1,"completed":1},
		 "schedule":{"kind":"once","run_at":"2026-09-28T17:00:00Z"},"last_status":null,"last_run_at":null}
	]}`
	out, err := observer(input).Observe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if out.Owner != "hermes" || out.Source.Profile != "mina" || out.Source.Host != "main" || out.Availability != Available || len(out.Jobs) != 3 {
		t.Fatalf("unexpected inventory: %+v", out)
	}
	if out.ObservedAt == nil || out.ObservedAt.Before(out.AttemptedAt) || out.StoreUpdatedAt == nil || out.StoreUpdatedAt.Nanosecond() != 123456000 {
		t.Fatalf("freshness lost: %+v", out)
	}
	cron, paused, dispatched := out.Jobs[0], out.Jobs[1], out.Jobs[2]
	if cron.Enabled == nil || !*cron.Enabled || cron.NativeState != Scheduled || cron.Schedule.Kind != Cron || *cron.Schedule.Expression != "0 9 * * MON-FRI" || cron.LastResult != OK || cron.LastRunAt == nil || cron.NextRunAt == nil {
		t.Fatalf("cron evidence lost: %+v", cron)
	}
	if cron.Schedule.Timezone != nil || cron.DeliveryErrorReported != nil {
		t.Fatal("inferred timezone or delivery success")
	}
	if paused.Enabled == nil || *paused.Enabled || paused.NativeState != Paused || paused.PausedAt == nil || paused.NextRunAt == nil || paused.Schedule.Kind != Interval || *paused.Schedule.Minutes != 30 || paused.LastResult != Error || paused.DeliveryErrorReported == nil || !*paused.DeliveryErrorReported {
		t.Fatalf("paused evidence lost: %+v", paused)
	}
	if dispatched.NativeState != Completed || dispatched.Schedule.Kind != Once || dispatched.Schedule.RunAt == nil || dispatched.LastResult != ResultUnknown || dispatched.LastRunAt != nil {
		t.Fatalf("dispatch treated as completion: %+v", dispatched)
	}
}

func TestUnknownAndContradictoryEvidence(t *testing.T) {
	out, err := observer(`{"jobs":[
		{"id":"minimal"},
		{"id":"half-paused","enabled":true,"state":"paused","paused_at":"2026-09-28T17:00:00Z","last_status":"blocked_config"},
		{"id":"future","state":"new-state-secret","last_status":"new-status-secret","schedule":{"kind":"future-kind-secret"}},
		{"id":"partial","schedule":{"kind":"cron"},"last_delivery_error":""}
	]}`).Observe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	minimal := out.Jobs[0]
	if minimal.Enabled != nil || minimal.NativeState != StateUnknown || minimal.LastResult != ResultUnknown || minimal.Schedule.Kind != ScheduleUnknown || minimal.NextRunAt != nil {
		t.Fatalf("missing fields defaulted to known: %+v", minimal)
	}
	contradictory := out.Jobs[1]
	if !*contradictory.Enabled || contradictory.NativeState != Paused || contradictory.PausedAt == nil || contradictory.LastResult != BlockedConfig {
		t.Fatalf("contradictory native evidence overwritten: %+v", contradictory)
	}
	future := out.Jobs[2]
	if future.NativeState != StateUnknown || future.LastResult != ResultUnknown || future.Schedule.Kind != ScheduleUnknown {
		t.Fatalf("unknown enum leaked: %+v", future)
	}
	if out.Jobs[3].Schedule.Expression != nil || out.Jobs[3].DeliveryErrorReported != nil {
		t.Fatal("partial evidence invented")
	}
}

func TestEmptyVersusInvalid(t *testing.T) {
	for _, input := range []string{`{"jobs":[]}`, "\ufeff{\"jobs\":[]}"} {
		out, err := observer(input).Observe(context.Background())
		if err != nil || out.Availability != Available || out.Jobs == nil || len(out.Jobs) != 0 || out.ObservedAt == nil {
			t.Fatalf("explicit empty failed: %+v, %v", out, err)
		}
	}
	for name, input := range map[string]string{
		"empty bytes": "", "absent list": `{}`, "null store": `null`, "null list": `{"jobs":null}`,
		"repairable bare list": `[]`, "repairable map": `{"jobs":{"id":{}}}`,
		"truncated": `{"jobs":[`, "invalid row": `{"jobs":[null]}`, "missing id": `{"jobs":[{}]}`,
		"wrong enabled type":  `{"jobs":[{"id":"a","enabled":"true"}]}`,
		"wrong schedule type": `{"jobs":[{"id":"a","schedule":"every 30m"}]}`,
		"bad time":            `{"jobs":[{"id":"a","next_run_at":"yesterday-secret"}]}`,
		"naive time":          `{"jobs":[{"id":"a","next_run_at":"2026-09-28T17:00:00"}]}`,
		"bad store time":      `{"jobs":[],"updated_at":"bad"}`,
		"negative interval":   `{"jobs":[{"id":"a","schedule":{"kind":"interval","minutes":-1}}]}`,
		"bad cron text":       `{"jobs":[{"id":"a","schedule":{"kind":"cron","expr":"https://secret"}}]}`,
		"duplicate id":        `{"jobs":[{"id":"a"},{"id":"a"}]}`,
		"partial list":        `{"jobs":[{"id":"good"},{"id":12}]}`,
		"extra JSON":          `{"jobs":[]} {"jobs":[]}`,
		"invalid UTF8":        "{\"jobs\":[],\"prompt\":\"\xff\"}",
	} {
		t.Run(name, func(t *testing.T) {
			out, err := observer(input).Observe(context.Background())
			if !errors.Is(err, ErrMalformed) || out.Availability != Malformed || out.Jobs != nil || out.ObservedAt != nil || out.StoreUpdatedAt != nil {
				t.Fatalf("invalid input became usable: %+v, %v", out, err)
			}
		})
	}
}

func TestReadBoundsAndFailures(t *testing.T) {
	o := observer("")
	o.Read = func(ctx context.Context, limit int64) ([]byte, error) {
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > ReadTimeout || limit != MaxBytes {
			t.Fatal("unbounded read")
		}
		return nil, errors.New("credential-secret /private/profile")
	}
	out, err := o.Observe(context.Background())
	if !errors.Is(err, ErrUnavailable) || out.Availability != Unavailable || out.Jobs != nil || out.ObservedAt != nil || strings.Contains(err.Error(), "secret") {
		t.Fatalf("read failure not sanitized: %+v, %v", out, err)
	}
	o.Read = func(context.Context, int64) ([]byte, error) { return make([]byte, MaxBytes+1), nil }
	out, err = o.Observe(context.Background())
	if !errors.Is(err, ErrTooLarge) || out.Jobs != nil || out.ObservedAt != nil {
		t.Fatalf("oversized read accepted: %+v, %v", out, err)
	}
	o.Source.Revision = "uninspected-revision"
	o.Read = func(context.Context, int64) ([]byte, error) { t.Fatal("read unsupported revision"); return nil, nil }
	out, err = o.Observe(context.Background())
	if !errors.Is(err, ErrRevision) || out.Availability != Unsupported {
		t.Fatalf("version gate failed: %+v, %v", out, err)
	}
	o.Source.Revision, o.Source.Profile = NativeRevision, ""
	if _, err = o.Observe(context.Background()); !errors.Is(err, ErrSource) {
		t.Fatal(err)
	}
}

func TestCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	o := observer("")
	o.Read = func(context.Context, int64) ([]byte, error) { t.Fatal("read after cancellation"); return nil, nil }
	out, err := o.Observe(ctx)
	if !errors.Is(err, context.Canceled) || out.ObservedAt != nil {
		t.Fatal(out, err)
	}
	ctx, cancel = context.WithCancel(context.Background())
	defer cancel()
	o.Read = func(ctx context.Context, _ int64) ([]byte, error) {
		cancel()
		return []byte(`{"jobs":[]}`), nil // Even a successful late read is rejected.
	}
	out, err = o.Observe(ctx)
	if !errors.Is(err, context.Canceled) || out.Jobs != nil || out.ObservedAt != nil {
		t.Fatal(out, err)
	}
	ctx, stop := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer stop()
	o.Read = func(ctx context.Context, _ int64) ([]byte, error) { <-ctx.Done(); return nil, ctx.Err() }
	out, err = o.Observe(ctx)
	if !errors.Is(err, context.DeadlineExceeded) || out.Jobs != nil {
		t.Fatal(out, err)
	}
}

func TestInventoryOmitsPrivateContent(t *testing.T) {
	input := `{"jobs":[{"id":"abcdef123456","enabled":true,"state":"scheduled",
		"name":"secret name from prompt","prompt":"secret prompt body","manual_run_prompt":"secret conversation",
		"schedule_display":"secret display","schedule":{"kind":"interval","minutes":30,"display":"secret display"},
		"last_status":"error","last_error":"secret failure text","last_delivery_error":"secret credentials in URL",
		"paused_reason":"secret pause","origin":{"chat_id":"secret chat"},"deliver":"secret address",
		"base_url":"https://secret","context_from":["secret session"],"script":"secret script",
		"latest_execution":{"status":"secret payload"},"future_field":{"password":"secret password"}}]}`
	out, err := observer(input).Observe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "secret") {
		t.Fatalf("private content leaked: %s", encoded)
	}
	if out.Jobs[0].LastResult != Error || out.Jobs[0].DeliveryErrorReported == nil {
		t.Fatal("safe failure evidence omitted")
	}
}

func TestNativeCronWhitespace(t *testing.T) {
	// Native parse_schedule uses split() for validation but persists the original
	// expression, which can contain tabs or other ASCII whitespace.
	out, err := observer(`{"jobs":[{"id":"whitespace","schedule":{"kind":"cron","expr":"0\t9 * * MON-FRI"}}]}`).Observe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if *out.Jobs[0].Schedule.Expression != "0\t9 * * MON-FRI" {
		t.Fatal("native expression changed")
	}
}
