package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"loom.local/loom/internal/automation"
	"loom.local/loom/internal/boxregistry"
	"loom.local/loom/internal/jobs"
	"loom.local/loom/internal/minidashboard"
	"loom.local/loom/internal/projects"
	"loom.local/loom/internal/response"
	"loom.local/loom/internal/serviceregistry"
)

// These reads share the existing trusted operator socket. They never dispatch work.
func (s Server) handleWorkspaceOverview(w http.ResponseWriter, r *http.Request) {
	cid, ctx := requestMeta(r)
	if r.Method != http.MethodGet {
		s.writeError(w, cid, 405, "method.not_allowed", "workspace", r.URL.Path, "Use GET.", nil)
		return
	}
	if trusted, _ := ctx.Value(localFileTransportKey{}).(bool); !trusted {
		s.writeError(w, cid, 403, "workspace.local_transport_required", "workspace", r.URL.Path, "Use the existing operator Unix socket.", nil)
		return
	}
	allowed := map[string]bool{}
	switch r.URL.Path {
	case "/v1/projects/overview":
		for _, k := range []string{"limit", "cursor", "state", "query"} {
			allowed[k] = true
		}
	case "/v1/jobs/overview":
		for _, k := range []string{"limit", "hours", "project", "state", "since", "until"} {
			allowed[k] = true
		}
	case "/v1/schedules/overview":
		for _, k := range []string{"limit", "cursor", "days", "status"} {
			allowed[k] = true
		}
	}
	q, err := parseOverviewQuery(r, allowed)
	if err != nil {
		s.writeError(w, cid, 400, "request.invalid_query", "workspace", r.URL.Path, "Invalid or unsupported overview query.", nil)
		return
	}
	if s.services.DB == nil {
		s.writeError(w, cid, 503, "workspace.overview_unavailable", "workspace", r.URL.Path, "Owner summary unavailable.", nil)
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	number := func(key string, fallback int) int {
		if q[key] == "" {
			return fallback
		}
		v, _ := strconv.Atoi(q[key])
		return v
	}
	var data any
	switch r.URL.Path {
	case "/v1/projects/overview":
		data, err = s.services.Projects.Overview(ctx, projects.OverviewFilter{Limit: number("limit", 24), Cursor: q["cursor"], WorkState: q["state"], Query: q["query"]})
	case "/v1/jobs/overview":
		var since, until *time.Time
		if q["since"] != "" {
			v, _ := time.Parse(time.RFC3339Nano, q["since"])
			since = &v
		}
		if q["until"] != "" {
			v, _ := time.Parse(time.RFC3339Nano, q["until"])
			until = &v
		}
		data, err = s.services.Jobs.Overview(ctx, jobs.OverviewFilter{Limit: number("limit", 30), Hours: number("hours", 24), ProjectRef: q["project"], State: q["state"], Since: since, Until: until})
	case "/v1/schedules/overview":
		data, err = s.services.Automation.Overview(ctx, automation.OverviewFilter{Limit: number("limit", 50), Days: number("days", 7), Cursor: q["cursor"], Status: q["status"]})
	case "/v1/system/runtime":
		result := map[string]any{"observed_at": time.Now().UTC()}
		if v, e := s.services.Workers.Overview(ctx); e == nil {
			result["workers"] = v
		} else {
			result["workers_error"] = "Worker inventory unavailable"
		}
		if v, e := serviceregistry.ReadOverview(ctx, s.services.DB); e == nil {
			result["services"] = v
		} else {
			result["services_error"] = "Service inventory unavailable"
		}
		data = result
	case "/v1/system/protection":
		var p minidashboard.ProtectionSnapshot
		p, err = minidashboard.ProtectionStatus(ctx, s.services.Maintenance, time.Now().UTC())
		data = map[string]any{"observed_at": time.Now().UTC(), "local": p.Local, "cloud": p.Cloud, "coverage": p.Coverage, "findings": p.Findings, "notes_recovery": map[string]any{"available": false, "reason": "Exact Notes cohort and client capture evidence is not exposed by this summary"}}
	default:
		err = fmt.Errorf("unknown overview")
	}
	if err != nil {
		if errors.Is(err, automation.ErrOverviewCursor) {
			s.writeError(w, cid, 400, "request.invalid_cursor", "workspace", r.URL.Path, "Reload the schedule list; this cursor does not match its Box or filters.", nil)
			return
		}
		s.writeError(w, cid, 503, "workspace.overview_unavailable", "workspace", r.URL.Path, "Owner summary unavailable; existing inventory remains available.", nil)
		return
	}
	payload, err := json.Marshal(response.Success(cid, data))
	if err != nil || len(payload) > 128*1024 {
		s.writeError(w, cid, 503, "workspace.overview_limit", "workspace", r.URL.Path, "Summary exceeds its bounded response.", nil)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "private, no-store")
	w.WriteHeader(200)
	_, _ = w.Write(payload)
}

func parseOverviewQuery(r *http.Request, allowed map[string]bool) (map[string]string, error) {
	out := map[string]string{}
	values, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return nil, err
	}
	for k, v := range values {
		if _, bound := boxregistry.FromContext(r.Context()); bound && (k == "box_id" || k == "box" || k == "all_boxes") {
			continue // Already validated by the Box middleware, not summary filters.
		}
		if !allowed[k] || len(v) != 1 || len(v[0]) > 1024 {
			return nil, fmt.Errorf("unsupported query")
		}
		out[k] = v[0]
	}
	for _, k := range []string{"limit", "days", "hours"} {
		if out[k] != "" {
			n, e := strconv.Atoi(out[k])
			if e != nil || n < 1 || k == "limit" && n > 100 || k == "days" && n > 7 || k == "hours" && n != 1 && n != 24 {
				return nil, fmt.Errorf("invalid range")
			}
		}
	}
	if len(out["query"]) > 160 {
		return nil, fmt.Errorf("query too long")
	}
	states := map[string]bool{"all": true, "working": true, "passive": true, "archived": true, "unclassified": true}
	if r.URL.Path == "/v1/jobs/overview" {
		states = map[string]bool{"all": true, "running": true, "queued": true, "attention": true, "completed": true}
	}
	if v := out["state"]; v != "" && !states[v] {
		return nil, fmt.Errorf("invalid state")
	}
	for _, k := range []string{"since", "until"} {
		if out[k] != "" {
			if _, err := time.Parse(time.RFC3339Nano, out[k]); err != nil {
				return nil, fmt.Errorf("invalid timestamp")
			}
		}
	}
	if (out["since"] == "") != (out["until"] == "") {
		return nil, fmt.Errorf("both bounds required")
	}
	if v := out["status"]; v != "" && v != "active" && v != "paused" && v != "disabled" {
		return nil, fmt.Errorf("invalid schedule status")
	}
	return out, nil
}
