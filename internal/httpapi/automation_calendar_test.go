package httpapi

import (
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCalendarScheduleHTTPRejectsUnknownOrMalformedInput(t *testing.T) {
	for _, body := range []string{`{"schedule_kind":"cron","dry_run":true,"cron_tz":"UTC"}`, `{"schedule_kind":"cron","dry_run":"true"}`, `{"schedule_kind":`} {
		req := httptest.NewRequest("POST", "/v1/schedules", strings.NewReader(body))
		out := httptest.NewRecorder()
		NewServer(Services{}, slog.Default()).handleScheduleCreate(out, req)
		if out.Code != 400 || !strings.Contains(out.Body.String(), "request.invalid_json") {
			t.Fatalf("invalid contract got %d %s", out.Code, out.Body.String())
		}
	}
}
