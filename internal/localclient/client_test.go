package localclient

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"loom.local/loom/internal/automation"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"loom.local/loom/internal/backupcontracts"
	"loom.local/loom/internal/backupcoverage"
	"loom.local/loom/internal/box"
	"loom.local/loom/internal/capabilities"
	"loom.local/loom/internal/cloudstorage"
	"loom.local/loom/internal/correlation"
	loomerrors "loom.local/loom/internal/errors"
	"loom.local/loom/internal/filetransfer"
	"loom.local/loom/internal/idempotency"
	"loom.local/loom/internal/jobs"
	"loom.local/loom/internal/knowledge"
	"loom.local/loom/internal/mainstorage"
	"loom.local/loom/internal/maintenance"
	"loom.local/loom/internal/minidashboard"
	"loom.local/loom/internal/modules"
	"loom.local/loom/internal/nodes"
	"loom.local/loom/internal/policy"
	"loom.local/loom/internal/projectcontracts"
	"loom.local/loom/internal/projectexport"
	"loom.local/loom/internal/projects"
	"loom.local/loom/internal/projectwatch"
	"loom.local/loom/internal/response"
	"loom.local/loom/internal/routing"
	"loom.local/loom/internal/serviceregistry"
	"loom.local/loom/internal/storagearchive"
	"loom.local/loom/internal/storagecatalog"
	"loom.local/loom/internal/storagefidelity"
	"loom.local/loom/internal/storageretention"
	loomsync "loom.local/loom/internal/sync"
	mainwatchedroots "loom.local/loom/internal/watchedroots"
	"loom.local/loom/internal/workers"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestCloudRestoreDrillClientPreservesWireTargetsContextAndTypedFailure(t *testing.T) {
	input := CloudSnapshotRestoreDrillInput{Ref: "history-exact", NodeID: "main", TargetDatabase: "loom_restore_drill_exact", ProvenanceTargetDatabase: "loom_provenance_restore_drill_exact", DryRun: true}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Hour)
	defer cancel()
	deadline, _ := ctx.Deadline()
	for _, fail := range []bool{false, true} {
		calls := 0
		client := Client{client: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			calls++
			if req.Method != http.MethodPost || req.URL.Path != "/v1/cloud/snapshot/restore-drill/live" || req.Header.Get(correlation.Header) != "corr_restore_client" {
				t.Fatal("restore method/path/correlation changed")
			}
			if got, ok := req.Context().Deadline(); !ok || !got.Equal(deadline) {
				t.Fatal("request deadline changed")
			}
			var got CloudSnapshotRestoreDrillInput
			decoder := json.NewDecoder(req.Body)
			decoder.DisallowUnknownFields()
			if err := decoder.Decode(&got); err != nil {
				t.Fatal(err)
			}
			if got != input {
				t.Fatalf("wire input = %+v", got)
			}
			if !fail {
				return okEnvelope(`{"status":"succeeded","dry_run":true,"ref":"history-exact"}`), nil
			}
			payload, _ := json.Marshal(response.Failure("corr_restore_service", loomerrors.New("backup.restore_authority.database_restore_failed", "backup", input.TargetDatabase, "Strict restore authority failed at the database_restore stage.")))
			return &http.Response{StatusCode: 500, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(payload))}, nil
		})}}
		got, err := client.CloudSnapshotRestoreDrillLive(ctx, "corr_restore_client", input)
		if calls != 1 {
			t.Fatalf("restore retried %d times", calls)
		}
		if !fail {
			if err != nil || got.Data.Status != "succeeded" || !got.Data.DryRun || got.Data.Ref != input.Ref {
				t.Fatalf("result=%+v err=%v", got, err)
			}
		} else {
			var typed *RequestError
			if !errors.As(err, &typed) || typed.LoomError().Code != "backup.restore_authority.database_restore_failed" || typed.LoomError().Domain != "backup" || typed.LoomError().Target != input.TargetDatabase || typed.CorrelationID("") != "corr_restore_service" {
				t.Fatalf("typed failure lost: %v", err)
			}
		}
	}
}

func TestMiniDashboardStatusUsesVersionedReadOnlyEndpoint(t *testing.T) {
	client := Client{BaseURL: "http://loom", client: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.Method != http.MethodGet || req.URL.Path != "/v1/mini-dashboard/status" {
			t.Fatalf("request = %s %s", req.Method, req.URL.Path)
		}
		return okEnvelope(`{"schema_version":"` + minidashboard.SchemaVersion + `"}`), nil
	})}}
	result, err := client.MiniDashboardStatus(context.Background(), "corr_test")
	if err != nil {
		t.Fatal(err)
	}
	if result.Data.SchemaVersion != minidashboard.SchemaVersion {
		t.Fatalf("result = %+v", result)
	}
}

func TestWorkerPolicyClientUsesTypedPolicyRouteAndIdempotency(t *testing.T) {
	oldFingerprint := "sha256:" + strings.Repeat("a", 64)
	newFingerprint := "sha256:" + strings.Repeat("b", 64)
	requests := 0
	client := Client{BaseURL: "http://loom", client: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		requests++
		if req.URL.Path != "/v1/workers/main.main_backup/policy" {
			t.Fatalf("path = %q", req.URL.Path)
		}
		if requests == 1 {
			if req.Method != http.MethodGet || req.Header.Get(idempotency.Header) != "" {
				t.Fatalf("inspect request = %s idempotency=%q", req.Method, req.Header.Get(idempotency.Header))
			}
			return okEnvelope(`{"worker_instance_id":"worker_instance_test","worker_key":"main.main_backup","locality":"main_owned","lifecycle_status":"active","enabled":true,"policy":{"schema_version":"worker_tick_policy.v0.2","mode":"manual"},"policy_fingerprint":"` + oldFingerprint + `","captured_at":"2026-08-28T00:00:00Z"}`), nil
		}
		if req.Method != http.MethodPost || req.Header.Get(idempotency.Header) != "policy-idem" {
			t.Fatalf("set request = %s idempotency=%q", req.Method, req.Header.Get(idempotency.Header))
		}
		var input workers.SetWorkerPolicyInput
		if err := json.NewDecoder(req.Body).Decode(&input); err != nil {
			t.Fatal(err)
		}
		if input.Policy.Mode != workers.TickModeDailyLocal || input.Policy.LocalTime != "03:15" || input.Policy.Timezone != "Europe/Amsterdam" || input.Policy.IntervalSeconds != 0 || input.Policy.RunOnStartup || input.ExpectedPolicyFingerprint != oldFingerprint || !input.Confirm || input.DryRun || input.Reason != "acceptance" {
			t.Fatalf("set input = %#v", input)
		}
		return okEnvelope(`{"worker_instance_id":"worker_instance_test","worker_key":"main.main_backup","applied":true,"changed":true,"old":{"worker_instance_id":"worker_instance_test","worker_key":"main.main_backup","policy":{"schema_version":"worker_tick_policy.v0.2","mode":"manual"},"policy_fingerprint":"` + oldFingerprint + `","captured_at":"2026-08-28T00:00:00Z"},"new":{"worker_instance_id":"worker_instance_test","worker_key":"main.main_backup","policy":{"schema_version":"worker_tick_policy.v0.2","mode":"daily_local","local_time":"03:15","timezone":"Europe/Amsterdam"},"policy_fingerprint":"` + newFingerprint + `","captured_at":"2026-08-28T00:00:00Z"},"next_run_evidence":{"derived_at":"2026-08-28T00:00:00Z","derivation_basis":"daily_local"}}`), nil
	})}}

	inspected, err := client.InspectWorkerPolicy(context.Background(), "corr_test", "main.main_backup")
	if err != nil || inspected.Data.PolicyFingerprint != oldFingerprint {
		t.Fatalf("inspect = %#v err=%v", inspected.Data, err)
	}
	updated, err := client.WithIdempotencyKey("policy-idem").SetWorkerPolicy(context.Background(), "corr_test", "main.main_backup", workers.SetWorkerPolicyInput{
		Policy:                    workers.TickPolicy{SchemaVersion: workers.TickPolicySchemaVersion, Mode: workers.TickModeDailyLocal, LocalTime: "03:15", Timezone: "Europe/Amsterdam"},
		ExpectedPolicyFingerprint: oldFingerprint,
		Reason:                    "acceptance",
		Confirm:                   true,
	})
	if err != nil || !updated.Data.Applied || updated.Data.New.PolicyFingerprint != newFingerprint {
		t.Fatalf("set = %#v err=%v", updated.Data, err)
	}
}

func TestSearchKnowledgeNotesCarriesAbsoluteTimeContract(t *testing.T) {
	client := Client{BaseURL: "http://loom", client: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.Method != http.MethodPost || req.URL.Path != "/v1/knowledge/notes/search" {
			t.Fatalf("request = %s %s", req.Method, req.URL.Path)
		}
		var input knowledge.NotesSearchInput
		if err := json.NewDecoder(req.Body).Decode(&input); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if input.After != "2026-01-01" || input.Before != "2026-02-01" || input.Sort != knowledge.NotesSearchSortNewest {
			t.Fatalf("absolute-time request = %#v", input)
		}
		return okEnvelope(`{
			"query":"loom",
			"result_count":1,
			"results":[{
				"search_document_id":"search_document_test",
				"knowledge_object_id":"knowledge_object_test",
				"knowledge_chunk_id":"knowledge_chunk_test",
				"notes_source_root_id":"notes_root_test",
				"root_kind":"box_notes",
				"relative_path":"Notes/loom.md",
				"file_class":"markdown",
				"source_created_at":"2025-12-31T09:00:00Z",
				"source_modified_at":"2026-01-02T10:00:00Z",
				"recency_at":"2026-01-02T10:00:00Z",
				"recency_basis":"source_filesystem_mtime",
				"recency_score":0.8,
				"recency_rank":1,
				"observed_at":"2026-08-16T12:00:00Z",
				"indexed_at":"2026-08-17T12:00:00Z",
				"citation":{"label":"Notes/loom.md","source_ref":"knowledge_object_test#knowledge_chunk_test"}
			}]
		}`), nil
	})}}
	envelope, err := client.SearchKnowledgeNotes(context.Background(), "corr_test", knowledge.NotesSearchInput{
		Query: "loom", After: "2026-01-01", Before: "2026-02-01", Sort: knowledge.NotesSearchSortNewest,
	})
	if err != nil {
		t.Fatalf("SearchKnowledgeNotes returned error: %v", err)
	}
	result := envelope.Data.Results[0]
	if result.SourceCreatedAt == nil || result.SourceModifiedAt == nil || result.RecencyBasis != knowledge.AbsoluteTimeBasisSourceFilesystemMtime || result.RecencyRank != 1 || result.ObservedAt.IsZero() || result.IndexedAt.IsZero() {
		t.Fatalf("absolute-time response = %#v", result)
	}
}

type cancelAfterReader struct{ sent bool }

func (reader *cancelAfterReader) Read(payload []byte) (int, error) {
	if reader.sent {
		return 0, context.Canceled
	}
	reader.sent = true
	return copy(payload, []byte("partial")), nil
}

func TestExportProjectCleansPartialOutputAfterCancelledDownload(t *testing.T) {
	summary := projectexport.Summary{SchemaVersion: projectexport.SchemaVersion, ProjectSlug: "backend-project", Mode: projectexport.ModePortable, ArchiveBytes: 10, ArchiveChecksum: "sha256:cancelled"}
	summaryPayload, err := json.Marshal(summary)
	if err != nil {
		t.Fatal(err)
	}
	client := Client{
		BaseURL: "http://loom",
		client: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if req.Method != http.MethodPost || req.URL.Path != "/v1/project-exports" {
				t.Fatalf("unexpected request: %s %s", req.Method, req.URL.Path)
			}
			return &http.Response{StatusCode: http.StatusOK, Header: http.Header{projectexport.SummaryHeader: []string{base64.RawURLEncoding.EncodeToString(summaryPayload)}}, Body: io.NopCloser(&cancelAfterReader{})}, nil
		})},
	}
	directory := t.TempDir()
	output := filepath.Join(directory, "backend.tar")
	_, err = client.ExportProject(context.Background(), "corr_export", projectexport.Request{ProjectRef: "backend-project", Mode: projectexport.ModePortable}, output, false)
	if err == nil || !strings.Contains(err.Error(), "context canceled") {
		t.Fatalf("cancelled download error = %v", err)
	}
	if _, err := os.Stat(output); !os.IsNotExist(err) {
		t.Fatalf("partial output installed: %v", err)
	}
	partials, err := filepath.Glob(filepath.Join(directory, ".backend.tar.partial-*"))
	if err != nil || len(partials) != 0 {
		t.Fatalf("partial outputs = %#v, err=%v", partials, err)
	}
}

func okEnvelope(data string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body: io.NopCloser(bytes.NewBufferString(`{
			"ok": true,
			"data": ` + data + `,
			"meta": {"correlation_id": "corr_test", "generated_at": "2026-05-05T00:00:00Z"}
		}`)),
	}
}

func TestDoJSONDecodesErrorEnvelope(t *testing.T) {
	client := Client{
		client: &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				if got := req.Header.Get(correlation.Header); got != "corr_test" {
					t.Fatalf("correlation header = %q, want corr_test", got)
				}
				return &http.Response{
					StatusCode: http.StatusNotFound,
					Header:     make(http.Header),
					Body: io.NopCloser(bytes.NewBufferString(`{
						"ok": false,
						"error": {
							"code": "objects.not_found",
							"summary": "Object was not found.",
							"domain": "objects",
							"target": "object_missing",
							"correlation_id": "corr_test"
						},
						"meta": {
							"correlation_id": "corr_test",
							"generated_at": "2026-05-05T00:00:00Z"
						}
					}`)),
				}, nil
			}),
		},
	}

	_, err := doJSON[map[string]any](client, context.Background(), http.MethodGet, "/v1/objects/object_missing", "corr_test", nil)
	if err == nil {
		t.Fatal("expected error")
	}
	requestErr, ok := err.(*RequestError)
	if !ok {
		t.Fatalf("error type = %T, want *RequestError", err)
	}
	if requestErr.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", requestErr.StatusCode)
	}
	if requestErr.Envelope.Error.Code != "objects.not_found" {
		t.Fatalf("code = %q, want objects.not_found", requestErr.Envelope.Error.Code)
	}
	if requestErr.CorrelationID("") != "corr_test" {
		t.Fatalf("correlation = %q, want corr_test", requestErr.CorrelationID(""))
	}
	if requestErr.LoomError().Code != "objects.not_found" {
		t.Fatalf("loom error code = %q, want objects.not_found", requestErr.LoomError().Code)
	}
}

func TestDoJSONSendsIdempotencyHeader(t *testing.T) {
	client := Client{
		client: &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				if got := req.Header.Get(idempotency.Header); got != "idem_test" {
					t.Fatalf("idempotency header = %q, want idem_test", got)
				}
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     make(http.Header),
					Body: io.NopCloser(bytes.NewBufferString(`{
						"ok": true,
						"data": {"status": "ok"},
						"meta": {
							"correlation_id": "corr_test",
							"generated_at": "2026-05-05T00:00:00Z",
							"idempotency_key": "idem_test"
						}
					}`)),
				}, nil
			}),
		},
	}.WithIdempotencyKey(" idem_test ")

	envelope, err := doJSON[map[string]string](client, context.Background(), http.MethodPost, "/v1/test", "corr_test", map[string]string{"status": "ok"})
	if err != nil {
		t.Fatalf("doJSON returned error: %v", err)
	}
	if envelope.Meta.IdempotencyKey != "idem_test" {
		t.Fatalf("meta idempotency = %q, want idem_test", envelope.Meta.IdempotencyKey)
	}
}

func TestCompactMaintenanceDatabaseSendsReviewedPlanFields(t *testing.T) {
	client := Client{
		client: &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				if req.Method != http.MethodPost || req.URL.Path != "/v1/maintenance/db/compact" {
					t.Fatalf("request = %s %s, want POST /v1/maintenance/db/compact", req.Method, req.URL.Path)
				}
				raw, err := io.ReadAll(req.Body)
				if err != nil {
					t.Fatalf("read request body: %v", err)
				}
				body := string(raw)
				for _, want := range []string{`"plan_hash":"sha256:test"`, `"max_total_rows":100`, `"reason":"operator review"`} {
					if !strings.Contains(body, want) {
						t.Fatalf("request body missing %q:\n%s", want, body)
					}
				}
				return okEnvelope(`{"status":"dry_run","dry_run":true,"plan_hash":"sha256:test"}`), nil
			}),
		},
	}
	envelope, err := client.CompactMaintenanceDatabase(context.Background(), "corr_test", maintenance.DatabaseCompactInput{
		DryRun:          true,
		PlanHash:        "sha256:test",
		MaxTotalRows:    100,
		MaxRowsPerBatch: 25,
		Reason:          "operator review",
	})
	if err != nil {
		t.Fatalf("CompactMaintenanceDatabase returned error: %v", err)
	}
	if envelope.Data.PlanHash != "sha256:test" || !envelope.Data.DryRun {
		t.Fatalf("unexpected response: %#v", envelope.Data)
	}
}

func TestJobListValuesIncludesAttentionFilters(t *testing.T) {
	values := jobListValues(jobs.ListFilter{
		Limit:               25,
		ManualOnly:          true,
		AttentionStatus:     jobs.FailureAttentionStatusAll,
		IncludeAcknowledged: true,
		IncludeArchived:     true,
	})
	for key, want := range map[string]string{
		"limit":                "25",
		"manual":               "true",
		"attention_status":     jobs.FailureAttentionStatusAll,
		"include_acknowledged": "true",
		"include_archived":     "true",
	} {
		if got := values.Get(key); got != want {
			t.Fatalf("%s = %q, want %q in %s", key, got, want, values.Encode())
		}
	}
}

func TestSyncListValuesIncludesDeletionRequestLifecycleFilters(t *testing.T) {
	values := syncListValues(loomsync.ListFilter{
		NodeRef:         "main",
		Status:          loomsync.DeletionRequestStatusAll,
		ActiveOnly:      true,
		IncludeResolved: true,
		Limit:           30,
	})
	for key, want := range map[string]string{
		"node":             "main",
		"status":           loomsync.DeletionRequestStatusAll,
		"active_only":      "true",
		"include_resolved": "true",
		"limit":            "30",
	} {
		if got := values.Get(key); got != want {
			t.Fatalf("%s = %q, want %q in %s", key, got, want, values.Encode())
		}
	}
}

func TestRunCloudSnapshotUsesExistingCloudCommandFamily(t *testing.T) {
	client := Client{client: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.Method != http.MethodPost || req.URL.Path != "/v1/cloud/snapshot/run" {
			t.Fatalf("request = %s %s", req.Method, req.URL.Path)
		}
		var input workers.RunOnceInput
		if err := json.NewDecoder(req.Body).Decode(&input); err != nil {
			t.Fatal(err)
		}
		if input.Reason != "manual direct archive" || input.IdempotencyKey != "archive-once" {
			t.Fatalf("input = %#v", input)
		}
		return okEnvelope(`{"run":{"worker_run_id":"worker_run_cloud","result_summary_json":{"schema_version":"cloud_snapshot_upload.result.v1","phase":"direct_borg_archive","committed":true}}}`), nil
	})}}
	envelope, err := client.RunCloudSnapshot(context.Background(), "corr_test", workers.RunOnceInput{Reason: "manual direct archive", IdempotencyKey: "archive-once"})
	if err != nil {
		t.Fatal(err)
	}
	if envelope.Data.Run.WorkerRunID != "worker_run_cloud" {
		t.Fatalf("run = %#v", envelope.Data.Run)
	}
}

func TestNewHTTPBuildsRequestsAgainstBaseURL(t *testing.T) {
	client, err := NewHTTP("http://10.44.0.2:8080/")
	if err != nil {
		t.Fatalf("NewHTTP returned error: %v", err)
	}
	client.client = &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if got := req.URL.String(); got != "http://10.44.0.2:8080/v1/health" {
				t.Fatalf("request URL = %q, want main HTTP URL", got)
			}
			return okEnvelope(`{"status":"ok"}`), nil
		}),
	}

	if _, err := client.Health(context.Background(), "corr_test"); err != nil {
		t.Fatalf("Health returned error: %v", err)
	}
}

func TestCloudLiveMethodsUseDaemonEndpoints(t *testing.T) {
	t.Parallel()

	type expectedRequest struct {
		method string
		path   string
		body   string
		data   string
	}
	requests := []expectedRequest{
		{
			method: http.MethodPost,
			path:   "/v1/cloud/status/live",
			body:   `"force_live":true`,
			data:   `{"status":"reachable","mode":"live","checked_at":"2026-06-20T00:00:00Z","config":{"enabled":true,"remote_name":"loom-cloud","remote_root":"loom"}}`,
		},
		{
			method: http.MethodPost,
			path:   "/v1/cloud/doctor/live",
			body:   `"force_live":true`,
			data:   `{"status":"ok","findings":[],"config":{"enabled":true}}`,
		},
		{
			method: http.MethodPost,
			path:   "/v1/cloud/snapshot/status/live",
			body:   `"node_id":"main"`,
			data:   `{"cloud":{"status":"reachable","mode":"live","checked_at":"2026-06-20T00:00:00Z","config":{"enabled":true}},"snapshots":{"status":"ok","node_id":"main","remote_root":"loom","snapshots":[]}}`,
		},
		{
			method: http.MethodPost,
			path:   "/v1/cloud/snapshot/backend/status/live",
			body:   `"force_live":true`,
			data:   `{"backend":"borg","status":"ok","checked_at":"2026-06-20T00:00:00Z","initialized":true,"checks":{}}`,
		},
		{
			method: http.MethodPost,
			path:   "/v1/cloud/snapshot/backend/init/live",
			body:   `"confirm":true`,
			data:   `{"backend":"borg","status":"succeeded","initialized":true,"checks":{"borg_init":"succeeded"}}`,
		},
		{
			method: http.MethodPost,
			path:   "/v1/cloud/snapshot/list/live",
			body:   `"node_id":"main"`,
			data:   `{"status":"ok","node_id":"main","remote_root":"loom","snapshots":[]}`,
		},
		{
			method: http.MethodPost,
			path:   "/v1/cloud/snapshot/verify/live",
			body:   `"profile":"rolling_repository","max_duration_seconds":600`,
			data:   `{"status":"succeeded","profile":"rolling_repository","coverage":"repository_check_time_bounded","max_duration_seconds":600,"remote_uri":"borg:ssh://example","checked_at":"2026-06-20T00:00:00Z","checks":{"borg_repository_check":"succeeded"}}`,
		},
		{
			method: http.MethodPost,
			path:   "/v1/cloud/snapshot/retention/plan/live",
			body:   `"keep_latest":3`,
			data:   `{"status":"empty","node_id":"main","backend":"borg","remote_root":"loom","keep_latest":3,"planned_at":"2026-06-20T00:00:00Z","kept":[],"remove":[],"ignored":[]}`,
		},
		{
			method: http.MethodPost,
			path:   "/v1/cloud/snapshot/retention/apply/live",
			body:   `"confirm_digest":"reviewed-digest"`,
			data:   `{"status":"succeeded","plan":{"status":"empty","node_id":"main","backend":"borg","remote_root":"loom","keep_latest":3,"planned_at":"2026-06-20T00:00:00Z","kept":[],"remove":[],"ignored":[]},"moved":[],"applied_at":"2026-06-20T00:00:00Z"}`,
		},
	}
	index := 0
	client := Client{
		client: &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				if index >= len(requests) {
					t.Fatalf("unexpected request %s %s", req.Method, req.URL.Path)
				}
				expected := requests[index]
				index++
				if req.Method != expected.method {
					t.Fatalf("request %d method = %s, want %s", index, req.Method, expected.method)
				}
				if req.URL.Path != expected.path {
					t.Fatalf("request %d path = %q, want %q", index, req.URL.Path, expected.path)
				}
				payload, err := io.ReadAll(req.Body)
				if err != nil {
					t.Fatalf("request %d body read failed: %v", index, err)
				}
				if !strings.Contains(string(payload), expected.body) {
					t.Fatalf("request %d body = %s, want to contain %s", index, payload, expected.body)
				}
				return okEnvelope(expected.data), nil
			}),
		},
	}

	if envelope, err := client.CloudStatusLive(context.Background(), "corr_test", cloudstorage.CloudStatusLiveInput{ForceLive: true}); err != nil {
		t.Fatalf("CloudStatusLive returned error: %v", err)
	} else if envelope.Data.Status != "reachable" {
		t.Fatalf("CloudStatusLive status = %q", envelope.Data.Status)
	}
	if envelope, err := client.CloudDoctorLive(context.Background(), "corr_test", cloudstorage.CloudDoctorLiveInput{ForceLive: true}); err != nil {
		t.Fatalf("CloudDoctorLive returned error: %v", err)
	} else if envelope.Data.Status != "ok" {
		t.Fatalf("CloudDoctorLive status = %q", envelope.Data.Status)
	}
	if envelope, err := client.CloudSnapshotStatusLive(context.Background(), "corr_test", cloudstorage.CloudSnapshotStatusLiveInput{NodeID: "main"}); err != nil {
		t.Fatalf("CloudSnapshotStatusLive returned error: %v", err)
	} else if envelope.Data.Snapshots == nil || envelope.Data.Snapshots.NodeID != "main" {
		t.Fatalf("CloudSnapshotStatusLive snapshots = %#v", envelope.Data.Snapshots)
	}
	if envelope, err := client.CloudSnapshotBackendStatusLive(context.Background(), "corr_test", cloudstorage.CloudSnapshotBackendStatusLiveInput{ForceLive: true}); err != nil {
		t.Fatalf("CloudSnapshotBackendStatusLive returned error: %v", err)
	} else if envelope.Data.Status != "ok" {
		t.Fatalf("CloudSnapshotBackendStatusLive status = %q", envelope.Data.Status)
	}
	if envelope, err := client.CloudSnapshotBackendInitLive(context.Background(), "corr_test", cloudstorage.CloudSnapshotBackendInitLiveInput{Confirm: true}); err != nil {
		t.Fatalf("CloudSnapshotBackendInitLive returned error: %v", err)
	} else if envelope.Data.Status != cloudstorage.SnapshotStatusSucceeded {
		t.Fatalf("CloudSnapshotBackendInitLive status = %q", envelope.Data.Status)
	}
	if envelope, err := client.CloudSnapshotListLive(context.Background(), "corr_test", cloudstorage.CloudSnapshotListLiveInput{NodeID: "main"}); err != nil {
		t.Fatalf("CloudSnapshotListLive returned error: %v", err)
	} else if envelope.Data.NodeID != "main" {
		t.Fatalf("CloudSnapshotListLive node = %q", envelope.Data.NodeID)
	}
	if envelope, err := client.CloudSnapshotVerifyLive(context.Background(), "corr_test", cloudstorage.SnapshotVerifyLiveInput{CloudSnapshotVerifyLiveInput: cloudstorage.CloudSnapshotVerifyLiveInput{NodeID: "main"}, SnapshotVerifyOptions: cloudstorage.SnapshotVerifyOptions{Profile: cloudstorage.SnapshotVerifyProfileRollingRepository, MaxDurationSeconds: 600}}); err != nil {
		t.Fatalf("CloudSnapshotVerifyLive returned error: %v", err)
	} else if envelope.Data.Status != cloudstorage.SnapshotStatusSucceeded {
		t.Fatalf("CloudSnapshotVerifyLive status = %q", envelope.Data.Status)
	} else if envelope.Data.Profile != cloudstorage.SnapshotVerifyProfileRollingRepository || envelope.Data.Coverage != cloudstorage.SnapshotVerifyCoverageRepositoryTimeBounded || envelope.Data.MaxDurationSeconds != 600 {
		t.Fatalf("CloudSnapshotVerifyLive result = %#v", envelope.Data)
	}
	if envelope, err := client.CloudSnapshotRetentionPlanLive(context.Background(), "corr_test", cloudstorage.CloudSnapshotRetentionPlanLiveInput{NodeID: "main", KeepLatest: 3}); err != nil {
		t.Fatalf("CloudSnapshotRetentionPlanLive returned error: %v", err)
	} else if envelope.Data.KeepLatest != 3 {
		t.Fatalf("CloudSnapshotRetentionPlanLive keep_latest = %d", envelope.Data.KeepLatest)
	}
	if envelope, err := client.CloudSnapshotRetentionApplyLive(context.Background(), "corr_test", cloudstorage.CloudSnapshotRetentionApplyLiveInput{
		NodeID: "main", KeepLatest: 3, Confirm: true, ConfirmDigest: "reviewed-digest", Compact: true,
		Plan: cloudstorage.SnapshotRetentionPlan{Status: cloudstorage.RetentionStatusPlanned, PlanDigest: "reviewed-digest"},
	}); err != nil {
		t.Fatalf("CloudSnapshotRetentionApplyLive returned error: %v", err)
	} else if envelope.Data.Status != "succeeded" {
		t.Fatalf("CloudSnapshotRetentionApplyLive status = %q", envelope.Data.Status)
	}
	if index != len(requests) {
		t.Fatalf("handled %d requests, want %d", index, len(requests))
	}
}

func TestBackupCoverageUsesDaemonEndpoint(t *testing.T) {
	t.Parallel()

	client := Client{
		client: &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				if req.Method != http.MethodGet {
					t.Fatalf("method = %s, want GET", req.Method)
				}
				if req.URL.Path != "/v1/backup/coverage" {
					t.Fatalf("path = %q, want /v1/backup/coverage", req.URL.Path)
				}
				return okEnvelope(`{"schema_version":"loom.backup.coverage.v0.8","mode":"main_backed","status":"ok","generated_at":"2026-07-05T00:00:00Z","entries":[],"summary":{}}`), nil
			}),
		},
	}

	envelope, err := client.BackupCoverage(context.Background(), "corr_test")
	if err != nil {
		t.Fatalf("BackupCoverage returned error: %v", err)
	}
	if envelope.Data.Mode != backupcoverage.ModeMainBacked || envelope.Data.Status != backupcoverage.OverallOK {
		t.Fatalf("unexpected backup coverage report: %#v", envelope.Data)
	}
}

func TestBackupContractMethodsUseDaemonEndpoints(t *testing.T) {
	t.Parallel()

	type expectedRequest struct {
		method string
		path   string
		body   string
		data   string
	}
	requests := []expectedRequest{
		{
			method: http.MethodGet,
			path:   "/v1/backup/contracts",
			data:   `{"contracts":[{"key":"field-data","path":"/box/.loom/contracts/backup/field-data.yaml","status":"active"}]}`,
		},
		{
			method: http.MethodGet,
			path:   "/v1/backup/contracts/field-data",
			data:   `{"key":"field-data","path":"/box/.loom/contracts/backup/field-data.yaml","status":"active"}`,
		},
		{
			method: http.MethodPost,
			path:   "/v1/backup/contracts",
			body:   `"target_path":"/srv/field-data"`,
			data:   `{"action":"create","dry_run":true,"path":"/box/.loom/contracts/backup/field-data.yaml","contract":{"schema_version":"loom.backup.contract.v0.9.9","key":"field-data","owner_node":"main","status":"active","target":{"scope":"owner_node_absolute","path":"/srv/field-data"},"backup":{"mode":"incremental_raw"}}}`,
		},
		{
			method: http.MethodPost,
			path:   "/v1/backup/contracts/field-data/disable",
			body:   `"dry_run":true`,
			data:   `{"action":"disable","dry_run":true,"path":"/box/.loom/contracts/backup/field-data.yaml","contract":{"schema_version":"loom.backup.contract.v0.9.9","key":"field-data","owner_node":"main","status":"disabled","target":{"scope":"owner_node_absolute","path":"/srv/field-data"},"backup":{"mode":"incremental_raw"}}}`,
		},
		{
			method: http.MethodDelete,
			path:   "/v1/backup/contracts/field-data",
			body:   `"dry_run":true`,
			data:   `{"action":"delete","dry_run":true,"path":"/box/.loom/contracts/backup/field-data.yaml","contract":{"schema_version":"","key":"","owner_node":"","status":"","target":{"scope":"","path":""},"backup":{"mode":""}}}`,
		},
		{
			method: http.MethodPost,
			path:   "/v1/backup/contracts/migrate-ignore-policy",
			body:   `"dry_run":true`,
			data:   `{"migration":{"dry_run":true,"applied":false,"changed_count":1,"attention_count":0,"items":[{"key":"field-data","path":"/box/.loom/contracts/backup/field-data.yaml","status":"migrated","to_schema":"loom.backup.contract.v1"}]},"reconciled":false}`,
		},
	}
	index := 0
	client := Client{
		client: &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				if index >= len(requests) {
					t.Fatalf("unexpected request %s %s", req.Method, req.URL.Path)
				}
				expected := requests[index]
				index++
				if req.Method != expected.method {
					t.Fatalf("request %d method = %s, want %s", index, req.Method, expected.method)
				}
				if req.URL.Path != expected.path {
					t.Fatalf("request %d path = %q, want %q", index, req.URL.Path, expected.path)
				}
				payload, err := io.ReadAll(req.Body)
				if err != nil {
					t.Fatalf("request %d body read failed: %v", index, err)
				}
				if expected.body != "" && !strings.Contains(string(payload), expected.body) {
					t.Fatalf("request %d body = %s, want to contain %s", index, payload, expected.body)
				}
				return okEnvelope(expected.data), nil
			}),
		},
	}

	if envelope, err := client.ListBackupContracts(context.Background(), "corr_test"); err != nil {
		t.Fatalf("ListBackupContracts returned error: %v", err)
	} else if len(envelope.Data.Contracts) != 1 || envelope.Data.Contracts[0].Key != "field-data" {
		t.Fatalf("unexpected contract list: %#v", envelope.Data)
	}
	if envelope, err := client.GetBackupContract(context.Background(), "corr_test", "field-data"); err != nil {
		t.Fatalf("GetBackupContract returned error: %v", err)
	} else if envelope.Data.Key != "field-data" {
		t.Fatalf("unexpected contract record: %#v", envelope.Data)
	}
	if envelope, err := client.CreateBackupContract(context.Background(), "corr_test", backupcontracts.CreateRequest{Key: "field-data", TargetPath: "/srv/field-data", DryRun: true}); err != nil {
		t.Fatalf("CreateBackupContract returned error: %v", err)
	} else if envelope.Data.Action != "create" || !envelope.Data.DryRun {
		t.Fatalf("unexpected create result: %#v", envelope.Data)
	}
	if envelope, err := client.DisableBackupContract(context.Background(), "corr_test", "field-data", backupcontracts.DisableRequest{DryRun: true}); err != nil {
		t.Fatalf("DisableBackupContract returned error: %v", err)
	} else if envelope.Data.Action != "disable" || !envelope.Data.DryRun {
		t.Fatalf("unexpected disable result: %#v", envelope.Data)
	}
	if envelope, err := client.DeleteBackupContract(context.Background(), "corr_test", "field-data", backupcontracts.DeleteRequest{DryRun: true}); err != nil {
		t.Fatalf("DeleteBackupContract returned error: %v", err)
	} else if envelope.Data.Action != "delete" || !envelope.Data.DryRun {
		t.Fatalf("unexpected delete result: %#v", envelope.Data)
	}
	if envelope, err := client.MigrateBackupContractIgnorePolicy(context.Background(), "corr_test", backupcontracts.MigrateIgnorePolicyRequest{DryRun: true}); err != nil {
		t.Fatalf("MigrateBackupContractIgnorePolicy returned error: %v", err)
	} else if envelope.Data.Migration.ChangedCount != 1 || envelope.Data.Reconciled {
		t.Fatalf("unexpected migration result: %#v", envelope.Data)
	}
	if index != len(requests) {
		t.Fatalf("handled %d requests, want %d", index, len(requests))
	}
}

func TestBackupContractPreflightMethodsUseDaemonEndpoints(t *testing.T) {
	t.Parallel()
	requests := []struct{ method, path string }{
		{http.MethodPost, "/v1/backup/contracts/preflights"},
		{http.MethodGet, "/v1/backup/contracts/preflights"},
		{http.MethodGet, "/v1/backup/contracts/preflights/backup_preflight_test"},
		{http.MethodPost, "/v1/backup/contracts/preflights/backup_preflight_test/retry"},
	}
	index := 0
	client := Client{client: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if index >= len(requests) {
			t.Fatalf("unexpected request %s %s", req.Method, req.URL.Path)
		}
		want := requests[index]
		index++
		if req.Method != want.method || req.URL.Path != want.path {
			t.Fatalf("request %d = %s %s, want %s %s", index, req.Method, req.URL.Path, want.method, want.path)
		}
		if index == 2 && (req.URL.Query().Get("node") != "macbook" || req.URL.Query().Get("status") != "pending" || req.URL.Query().Get("limit") != "10") {
			t.Fatalf("unexpected list query %s", req.URL.RawQuery)
		}
		data := `{"preflight_id":"backup_preflight_test","target_node_id":"node_test","requested_path":"/Users/test/Documents","status":"pending","request_schema_version":"loom.backup.protected-folder.control.v1","expires_at":"2026-08-17T12:00:00Z","created_at":"2026-08-17T11:45:00Z"}`
		if index == 2 {
			data = `[` + data + `]`
		}
		return okEnvelope(data), nil
	})}}
	if result, err := client.CreateBackupContractPreflight(context.Background(), "corr", backupcontracts.PreflightCreateRequest{NodeRef: "macbook", Path: "/Users/test/Documents"}); err != nil || result.Data.PreflightID == "" {
		t.Fatalf("create result=%#v err=%v", result, err)
	}
	if result, err := client.ListBackupContractPreflights(context.Background(), "corr", backupcontracts.PreflightFilter{NodeRef: "macbook", Status: "pending", Limit: 10}); err != nil || len(result.Data) != 1 {
		t.Fatalf("list result=%#v err=%v", result, err)
	}
	if _, err := client.GetBackupContractPreflight(context.Background(), "corr", "backup_preflight_test"); err != nil {
		t.Fatal(err)
	}
	if _, err := client.RetryBackupContractPreflight(context.Background(), "corr", "backup_preflight_test"); err != nil {
		t.Fatal(err)
	}
	if index != len(requests) {
		t.Fatalf("handled %d requests, want %d", index, len(requests))
	}
}

func TestProtectedFolderLifecycleMethodsUseDaemonEndpoints(t *testing.T) {
	t.Parallel()
	requests := []struct{ method, path string }{
		{http.MethodGet, "/v1/backup/contracts"},
		{http.MethodGet, "/v1/backup/contracts/field-data"},
		{http.MethodPost, "/v1/backup/contracts/field-data/enable"},
		{http.MethodPost, "/v1/backup/contracts/field-data/retry-activation"},
		{http.MethodPost, "/v1/backup/contracts/field-data/recheck"},
	}
	index := 0
	client := Client{client: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		want := requests[index]
		index++
		if req.Method != want.method || req.URL.Path != want.path {
			t.Fatalf("request %d = %s %s, want %s %s", index, req.Method, req.URL.Path, want.method, want.path)
		}
		if index == 1 && (req.URL.Query().Get("status") != "attention" || req.URL.Query().Get("node") != "macbook" || req.URL.Query().Get("limit") != "20") {
			t.Fatalf("unexpected query %s", req.URL.RawQuery)
		}
		switch index {
		case 1:
			return okEnvelope(`{"folders":[{"key":"field-data","status":"attention","lifecycle":"attention","contract":{"key":"field-data"}}],"counts":{"attention":1}}`), nil
		case 2:
			return okEnvelope(`{"key":"field-data","status":"waiting_for_node","lifecycle":"waiting_for_node","contract":{"key":"field-data"}}`), nil
		case 3:
			return okEnvelope(`{"action":"enable","node_apply_queued":true,"contract":{"key":"field-data"}}`), nil
		case 4:
			return okEnvelope(`{"node_id":"node_test","desired_revision":2,"desired_hash":"sha256:test","message":{"communication_message_id":"communication_message_test"}}`), nil
		default:
			return okEnvelope(`{"preflight_id":"backup_preflight_test","status":"pending","expires_at":"2026-08-17T12:00:00Z","created_at":"2026-08-17T11:45:00Z"}`), nil
		}
	})}}
	if result, err := client.ListProtectedFolders(context.Background(), "corr", backupcontracts.ProtectedFolderFilter{Lifecycle: "attention", NodeRef: "macbook", Limit: 20}); err != nil || len(result.Data.Folders) != 1 {
		t.Fatalf("list result=%#v err=%v", result, err)
	}
	if result, err := client.GetProtectedFolder(context.Background(), "corr", "field-data"); err != nil || result.Data.Lifecycle != backupcontracts.ProtectedFolderStatusWaitingForNode {
		t.Fatalf("detail result=%#v err=%v", result, err)
	}
	if result, err := client.EnableBackupContract(context.Background(), "corr", "field-data", backupcontracts.EnableRequest{}); err != nil || !result.Data.NodeApplyQueued {
		t.Fatalf("enable result=%#v err=%v", result, err)
	}
	if result, err := client.RetryBackupContractActivation(context.Background(), "corr", "field-data", backupcontracts.RetryActivationRequest{Reason: "operator retry"}); err != nil || result.Data.DesiredRevision != 2 {
		t.Fatalf("retry result=%#v err=%v", result, err)
	}
	if result, err := client.RecheckBackupContract(context.Background(), "corr", "field-data"); err != nil || result.Data.PreflightID == "" {
		t.Fatalf("recheck result=%#v err=%v", result, err)
	}
}

func TestListProvidersEncodesProjectFilter(t *testing.T) {
	client := Client{
		client: &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				if req.Method != http.MethodGet {
					t.Fatalf("method = %s, want GET", req.Method)
				}
				if req.URL.Path != "/v1/providers" {
					t.Fatalf("path = %q, want /v1/providers", req.URL.Path)
				}
				query := req.URL.Query()
				wants := map[string]string{
					"limit":   "25",
					"node":    "main",
					"scope":   "project/connector-smoke",
					"project": "connector-smoke",
					"type":    "connector",
					"status":  "active",
				}
				for key, want := range wants {
					if got := query.Get(key); got != want {
						t.Fatalf("query %s = %q, want %q", key, got, want)
					}
				}
				return okEnvelope(`[]`), nil
			}),
		},
	}

	_, err := client.ListProviders(context.Background(), "corr_test", capabilities.ProviderFilter{
		Limit:        25,
		NodeRef:      "main",
		ScopeRef:     "project/connector-smoke",
		ProjectRef:   "connector-smoke",
		ProviderType: "connector",
		Status:       "active",
	})
	if err != nil {
		t.Fatalf("ListProviders returned error: %v", err)
	}
}

func TestServiceClientMethodsAreTypedAndBounded(t *testing.T) {
	requests := 0
	client := Client{client: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		requests++
		switch requests {
		case 1:
			if req.Method != http.MethodGet || req.URL.Path != "/v1/services" || req.URL.Query().Get("project") != "example" || req.URL.Query().Get("status") != "active" {
				t.Fatalf("list request: %s %s?%s", req.Method, req.URL.Path, req.URL.RawQuery)
			}
			return okEnvelope(`[]`), nil
		case 2:
			if req.Method != http.MethodPost || req.URL.Path != "/v1/services/macbook@example/logs" {
				t.Fatalf("operation request: %s %s", req.Method, req.URL.Path)
			}
			var input map[string]any
			if err := json.NewDecoder(req.Body).Decode(&input); err != nil || input["lines"] != float64(20) {
				t.Fatalf("operation input: %#v %v", input, err)
			}
			return okEnvelope(`{"capability_call":{"capability_call_id":"capability_call_test"},"status":"dispatched"}`), nil
		default:
			t.Fatalf("unexpected request")
			return nil, nil
		}
	})}}
	if _, err := client.ListServices(context.Background(), "corr", serviceregistry.ServiceFilter{ProjectRef: "example", RegistryState: serviceregistry.ProviderStateActive}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.OperateService(context.Background(), "corr", "macbook@example", serviceregistry.OperationLogs, map[string]any{"lines": 20}); err != nil {
		t.Fatal(err)
	}
}

func TestListCapabilitiesEncodesFilters(t *testing.T) {
	client := Client{
		client: &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				if req.Method != http.MethodGet {
					t.Fatalf("method = %s, want GET", req.Method)
				}
				if req.URL.Path != "/v1/capabilities" {
					t.Fatalf("path = %q, want /v1/capabilities", req.URL.Path)
				}
				query := req.URL.Query()
				wants := map[string]string{
					"limit":               "17",
					"provider":            "main@system",
					"class":               "system.status.read",
					"node":                "main",
					"scope":               "system",
					"project":             "connector-smoke",
					"form":                "query",
					"status":              "active",
					"risk":                "low",
					"authorization_level": "1",
				}
				for key, want := range wants {
					if got := query.Get(key); got != want {
						t.Fatalf("query %s = %q, want %q", key, got, want)
					}
				}
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     make(http.Header),
					Body: io.NopCloser(bytes.NewBufferString(`{
						"ok": true,
						"data": [],
						"meta": {"correlation_id": "corr_test", "generated_at": "2026-05-05T00:00:00Z"}
					}`)),
				}, nil
			}),
		},
	}

	_, err := client.ListCapabilities(context.Background(), "corr_test", capabilities.CapabilityFilter{
		Limit:              17,
		ProviderRef:        "main@system",
		ClassRef:           "system.status.read",
		NodeRef:            "main",
		ScopeRef:           "system",
		ProjectRef:         "connector-smoke",
		Form:               "query",
		Status:             "active",
		Risk:               "low",
		AuthorizationLevel: 1,
	})
	if err != nil {
		t.Fatalf("ListCapabilities returned error: %v", err)
	}
}

func TestDropzoneHistoricalInspectionClientPaths(t *testing.T) {
	seen := []string{}
	client := Client{
		client: &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				seen = append(seen, req.Method+" "+req.URL.RequestURI())
				switch req.URL.Path {
				case "/v1/box/dropzone/upload-sessions":
					return okEnvelope(`[]`), nil
				case "/v1/box/dropzone/upload-sessions/drop_session_test":
					return okEnvelope(`{"session_id":"drop_session_test"}`), nil
				default:
					t.Fatalf("unexpected path: %s", req.URL.RequestURI())
				}
				return nil, nil
			}),
		},
	}
	if _, err := client.ListDropzoneUploadSessions(context.Background(), "corr_test", 7); err != nil {
		t.Fatalf("ListDropzoneUploadSessions returned error: %v", err)
	}
	if _, err := client.GetDropzoneUploadSession(context.Background(), "corr_test", "drop_session_test"); err != nil {
		t.Fatalf("GetDropzoneUploadSession returned error: %v", err)
	}
	want := []string{
		"GET /v1/box/dropzone/upload-sessions?limit=7",
		"GET /v1/box/dropzone/upload-sessions/drop_session_test",
	}
	if strings.Join(seen, "\n") != strings.Join(want, "\n") {
		t.Fatalf("requests =\n%s\nwant\n%s", strings.Join(seen, "\n"), strings.Join(want, "\n"))
	}
}

func TestStorageClientPaths(t *testing.T) {
	seen := []string{}
	client := Client{
		client: &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				seen = append(seen, req.Method+" "+req.URL.RequestURI())
				switch req.URL.Path {
				case "/v1/storage/entries":
					return okEnvelope(`[]`), nil
				case "/v1/storage/filesystem/status":
					return okEnvelope(`{"schema_version":"v0.7","status":"ok","roots":[],"catalog":{"query_limit":500}}`), nil
				case "/v1/file-transfers":
					query := req.URL.Query()
					if query.Get("status") != "failed" || query.Get("transfer_kind") != filetransfer.KindWatchedRootBackup || query.Get("source_node_key") != "macbook" || query.Get("source_root_key") != "documents" || query.Get("limit") != "9" {
						t.Fatalf("file transfer query = %s", req.URL.RawQuery)
					}
					return okEnvelope(`[]`), nil
				case "/v1/storage/entries/storage_entry_test":
					return okEnvelope(`{"entry":{"storage_entry_id":"storage_entry_test"},"physical_refs":[]}`), nil
				case "/v1/storage/resolve":
					if req.URL.Query().Get("path") != "main/Documents/report.md" {
						t.Fatalf("resolve path query = %s", req.URL.RawQuery)
					}
					return okEnvelope(`{"path":"main/Documents/report.md","view_entry":{"view_path":"main/Documents/report.md","storage_entry_id":"storage_entry_test"}}`), nil
				case "/v1/storage/inspect-path":
					if req.URL.Query().Get("path") != "main/Documents/report.md" {
						t.Fatalf("inspect path query = %s", req.URL.RawQuery)
					}
					return okEnvelope(`{"accepted_by_catalog":true,"availability_state":"available","current_source_present":true,"retained_payload_present":true,"retained_payload_verified":true,"safe_to_delete_source":false,"unsafe_reason":"retained payload has not been confirmed in a verified cloud snapshot"}`), nil
				case "/v1/storage/main-documents/status":
					return okEnvelope(`{"backing_root":"/tmp/main-documents","exists":true,"stable_window_seconds":15}`), nil
				case "/v1/storage/main-documents/reconcile":
					if req.Method != http.MethodPost {
						t.Fatalf("main-documents reconcile method = %s", req.Method)
					}
					return okEnvelope(`{"backing_root":"/tmp/main-documents","dry_run":true,"missing_cataloged":1}`), nil
				case "/v1/storage/main-documents/retention/backfill":
					if req.Method != http.MethodPost {
						t.Fatalf("main-documents retention backfill method = %s", req.Method)
					}
					return okEnvelope(`{"backing_root":"/tmp/main-documents","retention_root":"/tmp/storage-retention","dry_run":true,"would_create":1}`), nil
				case "/v1/storage/fidelity/backfill":
					if req.Method != http.MethodPost {
						t.Fatalf("storage fidelity backfill method = %s", req.Method)
					}
					return okEnvelope(`{"source":"watched-roots","dry_run":true,"scanned":1,"payload_rewrites":0,"export_refresh_requested":false}`), nil
				case "/v1/storage/main-documents/protection":
					if req.Method != http.MethodPost {
						t.Fatalf("main-documents protection method = %s", req.Method)
					}
					return okEnvelope(`{"accepted_by_catalog":true,"availability_state":"available","current_source_present":true,"retained_payload_present":true,"retained_payload_verified":true,"safe_to_delete_source":false,"unsafe_reason":"retained payload has not been confirmed in a verified cloud snapshot"}`), nil
				case "/v1/storage/main-documents/safe-delete":
					if req.Method != http.MethodPost {
						t.Fatalf("main-documents safe-delete method = %s", req.Method)
					}
					return okEnvelope(`{"accepted_by_catalog":true,"availability_state":"available","current_source_present":true,"retained_payload_present":true,"retained_payload_verified":true,"safe_to_delete_source":false,"unsafe_reason":"retained payload has not been confirmed in a verified cloud snapshot"}`), nil
				case "/v1/storage/export/status":
					return okEnvelope(`{"export_root":"/tmp/export","view_key":"loom-main","exists":true}`), nil
				case "/v1/storage/retention/status":
					return okEnvelope(`{"entries":1,"snapshots":1}`), nil
				case "/v1/storage/archive":
					if req.Method != http.MethodPost {
						t.Fatalf("archive method = %s", req.Method)
					}
					return okEnvelope(`{"archive_manifest":{"archive_manifest_id":"storage_archive_manifest_test","archive_key":"documents-report","archive_kind":"document_archive","status":"complete"},"manifest":{"archive_manifest_id":"storage_archive_manifest_test","archive_key":"documents-report","archive_kind":"document_archive","source_ref":"storage_entry_test","target_path":"main/Archive/Documents/report.md","owner_node_key":"main","entries":[]},"entries":[]}`), nil
				case "/v1/storage/safe-to-delete":
					if req.Method != http.MethodPost {
						t.Fatalf("safe-to-delete method = %s", req.Method)
					}
					return okEnvelope(`{"ref":"storage_entry_test","decision":"safe","safe":true}`), nil
				case "/v1/storage/fetch":
					if req.Method != http.MethodPost {
						t.Fatalf("fetch method = %s", req.Method)
					}
					return okEnvelope(`{"ref":"storage_entry_test","destination_path":"/tmp/fetched","bytes_written":4,"entry":{"storage_entry_id":"storage_entry_test"}}`), nil
				case "/v1/storage/restore":
					if req.Method != http.MethodPost {
						t.Fatalf("restore method = %s", req.Method)
					}
					return okEnvelope(`{"fetch_result":{"ref":"storage_entry_test","destination_path":"/tmp/restored","bytes_written":4,"entry":{"storage_entry_id":"storage_entry_test"}}}`), nil
				case "/v1/storage/tombstones":
					if req.Method != http.MethodPost {
						t.Fatalf("tombstone method = %s", req.Method)
					}
					return okEnvelope(`{"entry":{"storage_entry_id":"storage_entry_test"},"tombstone":{"storage_tombstone_id":"storage_tombstone_test","storage_entry_id":"storage_entry_test","tombstone_kind":"source_deleted"}}`), nil
				default:
					t.Fatalf("unexpected path %s", req.URL.RequestURI())
				}
				return nil, nil
			}),
		},
	}

	if _, err := client.ListStorageEntries(context.Background(), "corr_test", storagecatalog.ListFilter{}); err != nil {
		t.Fatalf("ListStorageEntries returned error: %v", err)
	}
	if _, err := client.ListFileTransfers(context.Background(), "corr_test", filetransfer.ListFilter{Status: filetransfer.StatusFailed, TransferKind: filetransfer.KindWatchedRootBackup, SourceNodeKey: "macbook", SourceRootKey: "documents", Limit: 9}); err != nil {
		t.Fatalf("ListFileTransfers returned error: %v", err)
	}
	if _, err := client.InspectStorageEntry(context.Background(), "corr_test", "storage_entry_test"); err != nil {
		t.Fatalf("InspectStorageEntry returned error: %v", err)
	}
	if _, err := client.ResolveStoragePath(context.Background(), "corr_test", "main/Documents/report.md"); err != nil {
		t.Fatalf("ResolveStoragePath returned error: %v", err)
	}
	if _, err := client.InspectStoragePath(context.Background(), "corr_test", "main/Documents/report.md"); err != nil {
		t.Fatalf("InspectStoragePath returned error: %v", err)
	}
	if _, err := client.GetMainDocumentsStatus(context.Background(), "corr_test"); err != nil {
		t.Fatalf("GetMainDocumentsStatus returned error: %v", err)
	}
	if _, err := client.ReconcileMainDocuments(context.Background(), "corr_test", mainstorage.ReconcileInput{DryRun: true}); err != nil {
		t.Fatalf("ReconcileMainDocuments returned error: %v", err)
	}
	if _, err := client.BackfillMainDocumentsRetention(context.Background(), "corr_test", mainstorage.RetentionBackfillInput{DryRun: true}); err != nil {
		t.Fatalf("BackfillMainDocumentsRetention returned error: %v", err)
	}
	if _, err := client.BackfillStorageFidelity(context.Background(), "corr_test", storagefidelity.BackfillInput{Source: storagefidelity.BackfillSourceWatchedRoots, DryRun: true}); err != nil {
		t.Fatalf("BackfillStorageFidelity returned error: %v", err)
	}
	if _, err := client.GetMainDocumentProtection(context.Background(), "corr_test", storagecatalog.MainDocumentProtectionInput{RelativePath: "report.md"}); err != nil {
		t.Fatalf("GetMainDocumentProtection returned error: %v", err)
	}
	if _, err := client.CheckMainDocumentSafeDelete(context.Background(), "corr_test", storagecatalog.MainDocumentProtectionInput{RelativePath: "report.md"}); err != nil {
		t.Fatalf("CheckMainDocumentSafeDelete returned error: %v", err)
	}
	if _, err := client.GetStorageExportStatus(context.Background(), "corr_test"); err != nil {
		t.Fatalf("GetStorageExportStatus returned error: %v", err)
	}
	if _, err := client.GetStorageFilesystemStatus(context.Background(), "corr_test"); err != nil {
		t.Fatalf("GetStorageFilesystemStatus returned error: %v", err)
	}
	if _, err := client.GetStorageRetentionStatus(context.Background(), "corr_test"); err != nil {
		t.Fatalf("GetStorageRetentionStatus returned error: %v", err)
	}
	if _, err := client.ArchiveStorage(context.Background(), "corr_test", storagearchive.ArchiveInput{SourceRef: "storage_entry_test", TargetPath: "main/Archive/Documents/report.md"}); err != nil {
		t.Fatalf("ArchiveStorage returned error: %v", err)
	}
	if _, err := client.CheckStorageSafeToDelete(context.Background(), "corr_test", storageretention.SafeToDeleteInput{Ref: "storage_entry_test"}); err != nil {
		t.Fatalf("CheckStorageSafeToDelete returned error: %v", err)
	}
	if _, err := client.FetchStorage(context.Background(), "corr_test", storageretention.FetchInput{Ref: "storage_entry_test", DestinationPath: "/tmp/fetched"}); err != nil {
		t.Fatalf("FetchStorage returned error: %v", err)
	}
	if _, err := client.RestoreStorage(context.Background(), "corr_test", storageretention.RestoreInput{Ref: "storage_entry_test", DestinationPath: "/tmp/restored"}); err != nil {
		t.Fatalf("RestoreStorage returned error: %v", err)
	}
	if _, err := client.RecordStorageTombstone(context.Background(), "corr_test", storageretention.RecordTombstoneInput{Ref: "storage_entry_test"}); err != nil {
		t.Fatalf("RecordStorageTombstone returned error: %v", err)
	}
	want := strings.Join([]string{
		"GET /v1/storage/entries",
		"GET /v1/file-transfers?limit=9&source_node_key=macbook&source_root_key=documents&status=failed&transfer_kind=watched_root_backup",
		"GET /v1/storage/entries/storage_entry_test",
		"GET /v1/storage/resolve?path=main%2FDocuments%2Freport.md",
		"GET /v1/storage/inspect-path?path=main%2FDocuments%2Freport.md",
		"GET /v1/storage/main-documents/status",
		"POST /v1/storage/main-documents/reconcile",
		"POST /v1/storage/main-documents/retention/backfill",
		"POST /v1/storage/fidelity/backfill",
		"POST /v1/storage/main-documents/protection",
		"POST /v1/storage/main-documents/safe-delete",
		"GET /v1/storage/export/status",
		"GET /v1/storage/filesystem/status",
		"GET /v1/storage/retention/status",
		"POST /v1/storage/archive",
		"POST /v1/storage/safe-to-delete",
		"POST /v1/storage/fetch",
		"POST /v1/storage/restore",
		"POST /v1/storage/tombstones",
	}, "\n")
	if strings.Join(seen, "\n") != want {
		t.Fatalf("seen paths:\n%s\nwant:\n%s", strings.Join(seen, "\n"), want)
	}
}

func TestRepairStaleWorkerRunsPath(t *testing.T) {
	client := Client{
		client: &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				if req.Method != http.MethodPost || req.URL.Path != "/v1/workers/repair-stale" {
					t.Fatalf("unexpected request %s %s", req.Method, req.URL.RequestURI())
				}
				return okEnvelope(`{"checked_at":"2026-06-13T19:20:00Z","timed_out_runs":1,"expired_leases":1,"repaired_instances":1,"run_ids":["worker_run_test"]}`), nil
			}),
		},
	}

	envelope, err := client.RepairStaleWorkerRuns(context.Background(), "corr_test")
	if err != nil {
		t.Fatalf("RepairStaleWorkerRuns returned error: %v", err)
	}
	if envelope.Data.RepairedInstances != 1 || len(envelope.Data.RunIDs) != 1 || envelope.Data.RunIDs[0] != "worker_run_test" {
		t.Fatalf("unexpected repair result: %#v", envelope.Data)
	}
	var _ workers.StaleRunRepairResult = envelope.Data
}

func TestListModulesEncodesProjectFilter(t *testing.T) {
	client := Client{
		client: &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				if req.Method != http.MethodGet {
					t.Fatalf("method = %s, want GET", req.Method)
				}
				if req.URL.Path != "/v1/modules" {
					t.Fatalf("path = %q, want /v1/modules", req.URL.Path)
				}
				query := req.URL.Query()
				wants := map[string]string{
					"limit":     "11",
					"status":    "valid",
					"module_id": "loom.example",
					"project":   "module-smoke",
				}
				for key, want := range wants {
					if got := query.Get(key); got != want {
						t.Fatalf("query %s = %q, want %q", key, got, want)
					}
				}
				return okEnvelope(`[]`), nil
			}),
		},
	}

	_, err := client.ListModules(context.Background(), "corr_test", modules.ModuleFilter{
		Limit:      11,
		Status:     modules.ModuleVersionStatusValid,
		ModuleID:   "loom.example",
		ProjectRef: "module-smoke",
	})
	if err != nil {
		t.Fatalf("ListModules returned error: %v", err)
	}
}

func TestProjectWatchClientPaths(t *testing.T) {
	client := Client{
		client: &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				if req.Method != http.MethodGet {
					t.Fatalf("method = %s, want GET", req.Method)
				}
				if req.URL.Path != "/v1/projects/research-notes/watch-plan" {
					t.Fatalf("path = %q", req.URL.Path)
				}
				if got := req.URL.Query().Get("project_root"); got != "/tmp/research-notes" {
					t.Fatalf("project_root query = %q", got)
				}
				if got := req.URL.Query().Get("use_registered_snapshot"); got != "true" {
					t.Fatalf("use_registered_snapshot query = %q", got)
				}
				return okEnvelope(`{"project_root":"/tmp/research-notes","watched_roots":[],"commands":[]}`), nil
			}),
		},
	}

	if _, err := client.BuildProjectWatchPlan(context.Background(), "corr_test", "research-notes", projectwatch.BuildPlanInput{
		ProjectRoot:           "/tmp/research-notes",
		UseRegisteredSnapshot: true,
	}); err != nil {
		t.Fatalf("BuildProjectWatchPlan returned error: %v", err)
	}
}

func TestBoxWatchClientPaths(t *testing.T) {
	var sawApply, sawStatus bool
	client := Client{
		client: &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				switch {
				case req.Method == http.MethodPost && req.URL.Path == "/v1/box/watch-policy/apply":
					sawApply = true
					var input box.WatchApplyInput
					if err := json.NewDecoder(req.Body).Decode(&input); err != nil {
						t.Fatalf("decode apply body: %v", err)
					}
					if input.Resolved.RootPath != "/tmp/LOOM Box" || input.Resolved.Profile != box.ProfileWorkspace || !input.DryRun {
						t.Fatalf("unexpected apply body: %#v", input)
					}
					return okEnvelope(`{"dry_run":true,"plan":{"root_path":"/tmp/LOOM Box","watched_roots":[]}}`), nil
				case req.Method == http.MethodPost && req.URL.Path == "/v1/box/watch-status":
					sawStatus = true
					var input box.WatchStatusInput
					if err := json.NewDecoder(req.Body).Decode(&input); err != nil {
						t.Fatalf("decode status body: %v", err)
					}
					if input.Resolved.RootPath != "/tmp/LOOM Box" || input.Resolved.Profile != box.ProfileWorkspace {
						t.Fatalf("unexpected status body: %#v", input)
					}
					return okEnvelope(`{"plan":{"root_path":"/tmp/LOOM Box","watched_roots":[]},"registrations":[]}`), nil
				default:
					t.Fatalf("unexpected request %s %s", req.Method, req.URL.String())
				}
				return nil, nil
			}),
		},
	}

	input := box.WatchApplyInput{
		Resolved: box.Resolved{RootPath: "/tmp/LOOM Box", Profile: box.ProfileWorkspace, OwnerNode: "macbook"},
		DryRun:   true,
	}
	if _, err := client.ApplyBoxWatchPolicy(context.Background(), "corr_test", input); err != nil {
		t.Fatalf("ApplyBoxWatchPolicy returned error: %v", err)
	}
	if _, err := client.GetBoxWatchStatus(context.Background(), "corr_test", box.WatchStatusInput{Resolved: input.Resolved}); err != nil {
		t.Fatalf("GetBoxWatchStatus returned error: %v", err)
	}
	if !sawApply || !sawStatus {
		t.Fatalf("expected both Box watch paths, saw apply=%v status=%v", sawApply, sawStatus)
	}
}

func TestWatchedRootFiltersEncodeProject(t *testing.T) {
	values := watchedRootBackupItemValues(mainwatchedroots.BackupItemFilter{
		NodeRef:    "workspace",
		RootKey:    "research_notes__notes",
		ProjectRef: "research-notes",
		Status:     "accepted",
		BatchRef:   "batch_123",
		Path:       "notes/a.md",
		Limit:      25,
	})

	wants := map[string]string{
		"node":    "workspace",
		"root":    "research_notes__notes",
		"project": "research-notes",
		"status":  "accepted",
		"batch":   "batch_123",
		"path":    "notes/a.md",
		"limit":   "25",
	}
	for key, want := range wants {
		if got := values.Get(key); got != want {
			t.Fatalf("query %s = %q, want %q", key, got, want)
		}
	}
}

func TestSyncListValuesEncodeProject(t *testing.T) {
	values := syncListValues(loomsync.ListFilter{
		NodeRef:    "workspace",
		ProjectRef: "research-notes",
		Status:     "fresh",
		Limit:      25,
	})

	wants := map[string]string{
		"node":    "workspace",
		"project": "research-notes",
		"status":  "fresh",
		"limit":   "25",
	}
	for key, want := range wants {
		if got := values.Get(key); got != want {
			t.Fatalf("query %s = %q, want %q", key, got, want)
		}
	}
}

func TestRuntimeBindingClientMethods(t *testing.T) {
	var sawList, sawRegister, sawInspect, sawValidate, sawTest bool
	client := Client{
		client: &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				switch {
				case req.Method == http.MethodGet && req.URL.Path == "/v1/capability-runtime-bindings":
					sawList = true
					query := req.URL.Query()
					wants := map[string]string{
						"limit":            "11",
						"capability":       "main@system.status.read",
						"endpoint_version": "endpv_test",
						"provider":         "main@system",
						"runtime_kind":     "script",
						"status":           "active",
					}
					for key, want := range wants {
						if got := query.Get(key); got != want {
							t.Fatalf("query %s = %q, want %q", key, got, want)
						}
					}
					return okEnvelope(`[]`), nil
				case req.Method == http.MethodPost && req.URL.Path == "/v1/capability-runtime-bindings":
					sawRegister = true
					var input capabilities.RegisterRuntimeBindingInput
					if err := json.NewDecoder(req.Body).Decode(&input); err != nil {
						t.Fatalf("decode register body: %v", err)
					}
					if input.EndpointVersionRef != "endpv_test" || input.RuntimeKind != capabilities.RuntimeKindScript {
						t.Fatalf("unexpected register body: %#v", input)
					}
					return okEnvelope(`{}`), nil
				case req.Method == http.MethodGet && req.URL.Path == "/v1/capability-runtime-bindings/runtime_binding_test":
					sawInspect = true
					return okEnvelope(`{}`), nil
				case req.Method == http.MethodGet && req.URL.Path == "/v1/capability-runtime-bindings/runtime_binding_test/validate":
					sawValidate = true
					return okEnvelope(`{}`), nil
				case req.Method == http.MethodPost && req.URL.Path == "/v1/capability-runtime-bindings/runtime_binding_test/test":
					sawTest = true
					var input routing.RuntimeBindingTestInput
					if err := json.NewDecoder(req.Body).Decode(&input); err != nil {
						t.Fatalf("decode runtime binding test body: %v", err)
					}
					if !input.AllowInactive || string(input.Input) != `{"ok":true}` {
						t.Fatalf("unexpected runtime binding test body: %#v", input)
					}
					return okEnvelope(`{}`), nil
				default:
					t.Fatalf("unexpected request %s %s", req.Method, req.URL.String())
				}
				return nil, nil
			}),
		},
	}

	_, err := client.ListRuntimeBindings(context.Background(), "corr_test", capabilities.RuntimeBindingFilter{
		Limit:              11,
		CapabilityRef:      "main@system.status.read",
		EndpointVersionRef: "endpv_test",
		ProviderRef:        "main@system",
		RuntimeKind:        capabilities.RuntimeKindScript,
		Status:             capabilities.RuntimeBindingStatusActive,
	})
	if err != nil {
		t.Fatalf("ListRuntimeBindings returned error: %v", err)
	}
	_, err = client.RegisterRuntimeBinding(context.Background(), "corr_test", capabilities.RegisterRuntimeBindingInput{
		EndpointVersionRef: "endpv_test",
		RuntimeKind:        capabilities.RuntimeKindScript,
	})
	if err != nil {
		t.Fatalf("RegisterRuntimeBinding returned error: %v", err)
	}
	_, err = client.GetRuntimeBinding(context.Background(), "corr_test", "runtime_binding_test")
	if err != nil {
		t.Fatalf("GetRuntimeBinding returned error: %v", err)
	}
	_, err = client.ValidateRuntimeBinding(context.Background(), "corr_test", "runtime_binding_test")
	if err != nil {
		t.Fatalf("ValidateRuntimeBinding returned error: %v", err)
	}
	_, err = client.TestRuntimeBinding(context.Background(), "corr_test", "runtime_binding_test", routing.RuntimeBindingTestInput{
		Input:         json.RawMessage(`{"ok":true}`),
		AllowInactive: true,
	})
	if err != nil {
		t.Fatalf("TestRuntimeBinding returned error: %v", err)
	}
	if !sawList || !sawRegister || !sawInspect || !sawValidate || !sawTest {
		t.Fatalf("expected all runtime binding paths, saw list=%v register=%v inspect=%v validate=%v test=%v", sawList, sawRegister, sawInspect, sawValidate, sawTest)
	}
}

func TestProjectRegistrationClientMethods(t *testing.T) {
	var sawScaffold, sawAddFacets, sawLayoutMigration, sawAnalyzeBackend, sawRegister, sawRegisterBackend, sawStatus, sawActivate, sawDeactivate, sawArchive, sawArchiveInspect, sawArchiveRestore, sawRuntimeMigration bool
	client := Client{
		client: &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				switch {
				case req.Method == http.MethodPost && req.URL.Path == "/v1/project-scaffolds":
					sawScaffold = true
					var input projectcontracts.ScaffoldOptions
					if err := json.NewDecoder(req.Body).Decode(&input); err != nil {
						t.Fatalf("decode scaffold body: %v", err)
					}
					if input.Name != "Gmail Automation" || input.OwnerNode != "main" || input.Preset != projectcontracts.PresetAutomation {
						t.Fatalf("unexpected scaffold body: %#v", input)
					}
					return okEnvelope(`{"ok":true,"project_root":"/home/loomadmin/loom-box/Projects/gmail-automation","name":"Gmail Automation","slug":"gmail-automation","owner_node":"main","preset":"automation"}`), nil
				case req.Method == http.MethodPost && req.URL.Path == "/v1/project-facet-additions":
					sawAddFacets = true
					var input projectcontracts.AddProjectFacetsOptions
					if err := json.NewDecoder(req.Body).Decode(&input); err != nil {
						t.Fatalf("decode facet add body: %v", err)
					}
					if input.ProjectRef != "gmail-automation" || strings.Join(input.Facets, ",") != "notes" || !input.DryRun {
						t.Fatalf("unexpected facet add body: %#v", input)
					}
					return okEnvelope(`{"ok":true,"dry_run":true,"project_root":"/home/loomadmin/loom-box/Projects/gmail-automation","slug":"gmail-automation","requested_facets":["notes"],"added_facets":["notes"],"facets":["notes"],"files":[],"validation":{"state":"planned_only"}}`), nil
				case req.Method == http.MethodPost && req.URL.Path == "/v1/project-layout-migrations":
					sawLayoutMigration = true
					var input projectcontracts.LayoutMigrationOptions
					if err := json.NewDecoder(req.Body).Decode(&input); err != nil {
						t.Fatalf("decode layout migration body: %v", err)
					}
					if input.ProjectRef != "gmail-automation" || input.Apply || input.Yes {
						t.Fatalf("unexpected layout migration body: %#v", input)
					}
					return okEnvelope(`{"ok":true,"dry_run":true,"project_root":"/home/loomadmin/loom-box/Projects/gmail-automation","before_layout":"legacy","actions":[],"collisions":[],"skips":[],"next_actions":[]}`), nil
				case req.Method == http.MethodPost && req.URL.Path == "/v1/project-contract-analyses":
					sawAnalyzeBackend = true
					var input projectcontracts.BackendAnalysisInput
					if err := json.NewDecoder(req.Body).Decode(&input); err != nil {
						t.Fatalf("decode backend analysis body: %v", err)
					}
					if input.ProjectRef != "gmail-automation" {
						t.Fatalf("unexpected backend analysis body: %#v", input)
					}
					return okEnvelope(`{"project_ref":"gmail-automation","project_root":"/home/loomadmin/loom-box/Projects/gmail-automation","analysis":{"report":{"ok":true,"registerable":true},"plan":{"registerable":true}},"diff":{"project_ref":"gmail-automation","summary":{"unchanged":1}},"doctor":{"project_ref":"gmail-automation","source":"project.backend_analysis","summary":{"ok":1},"checks":[]},"drift_status":"current","current":true,"next_action":"no registration needed"}`), nil
				case req.Method == http.MethodPost && req.URL.Path == "/v1/project-contract-registrations":
					sawRegister = true
					var input projects.RegisterProjectContractInput
					if err := json.NewDecoder(req.Body).Decode(&input); err != nil {
						t.Fatalf("decode register body: %v", err)
					}
					if input.Project.Slug != "gmail-automation" || input.ContractHash != "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" || len(input.Facets) != 1 {
						t.Fatalf("unexpected register body: %#v", input)
					}
					return okEnvelope(`{}`), nil
				case req.Method == http.MethodPost && req.URL.Path == "/v1/project-contract-registrations/from-backend":
					sawRegisterBackend = true
					var input projects.RegisterProjectContractFromBackendInput
					if err := json.NewDecoder(req.Body).Decode(&input); err != nil {
						t.Fatalf("decode backend register body: %v", err)
					}
					if input.ProjectRef != "gmail-automation" || !input.Strict {
						t.Fatalf("unexpected backend register body: %#v", input)
					}
					return okEnvelope(`{"updated":true}`), nil
				case req.Method == http.MethodGet && req.URL.Path == "/v1/project-contract-registrations/gmail-automation":
					sawStatus = true
					return okEnvelope(`{}`), nil
				case req.Method == http.MethodPost && req.URL.Path == "/v1/projects/gmail-automation/activate":
					sawActivate = true
					var input projects.ActivateProjectInput
					if err := json.NewDecoder(req.Body).Decode(&input); err != nil {
						t.Fatalf("decode activate body: %v", err)
					}
					if input.Facet != "scripts" || input.ProjectRoot != "/tmp/gmail-automation" {
						t.Fatalf("unexpected activate body: %#v", input)
					}
					return okEnvelope(`{}`), nil
				case req.Method == http.MethodPost && req.URL.Path == "/v1/projects/gmail-automation/deactivate":
					sawDeactivate = true
					var input projects.DeactivateProjectInput
					if err := json.NewDecoder(req.Body).Decode(&input); err != nil {
						t.Fatalf("decode deactivate body: %v", err)
					}
					if input.Facet != "scripts" || input.ProjectRoot != "/tmp/gmail-automation" || input.Reason != "test" || !input.DryRun {
						t.Fatalf("unexpected deactivate body: %#v", input)
					}
					return okEnvelope(`{"facet":"scripts","dry_run":true}`), nil
				case req.Method == http.MethodPost && req.URL.Path == "/v1/projects/gmail-automation/archive":
					sawArchive = true
					var input storagearchive.ProjectArchiveInput
					if err := json.NewDecoder(req.Body).Decode(&input); err != nil {
						t.Fatalf("decode archive body: %v", err)
					}
					if input.SourceRef != "macbook/Backups/Projects/gmail-automation/current" || input.TargetPath != "main/Archive/Projects/gmail-automation" || input.Reason != "done" || input.DryRun {
						t.Fatalf("unexpected archive body: %#v", input)
					}
					return okEnvelope(`{"dry_run":false}`), nil
				case req.Method == http.MethodGet && req.URL.Path == "/v1/projects/gmail-automation/archive/inspect":
					sawArchiveInspect = true
					return okEnvelope(`{}`), nil
				case req.Method == http.MethodPost && req.URL.Path == "/v1/projects/gmail-automation/archive/restore":
					sawArchiveRestore = true
					var input storagearchive.ProjectArchiveRestoreInput
					if err := json.NewDecoder(req.Body).Decode(&input); err != nil {
						t.Fatalf("decode archive restore body: %v", err)
					}
					if input.ToNode != "macbook" || !input.DryRun {
						t.Fatalf("unexpected archive restore body: %#v", input)
					}
					return okEnvelope(`{"dry_run":true}`), nil
				case req.Method == http.MethodPost && req.URL.Path == "/v1/projects/gmail-automation/archive/migrate-runtime":
					sawRuntimeMigration = true
					var input storagearchive.ProjectRuntimeMigrationInput
					if err := json.NewDecoder(req.Body).Decode(&input); err != nil {
						t.Fatalf("decode runtime migration body: %v", err)
					}
					if input.ToNode != "main" || !input.DryRun {
						t.Fatalf("unexpected runtime migration body: %#v", input)
					}
					return okEnvelope(`{"dry_run":true}`), nil
				default:
					t.Fatalf("unexpected request %s %s", req.Method, req.URL.String())
				}
				return nil, nil
			}),
		},
	}

	if _, err := client.ScaffoldProject(context.Background(), "corr_test", projectcontracts.ScaffoldOptions{Name: "Gmail Automation", OwnerNode: "main", Preset: projectcontracts.PresetAutomation}); err != nil {
		t.Fatalf("ScaffoldProject returned error: %v", err)
	}
	if _, err := client.AddProjectFacets(context.Background(), "corr_test", projectcontracts.AddProjectFacetsOptions{ProjectRef: "gmail-automation", Facets: []string{"notes"}, DryRun: true}); err != nil {
		t.Fatalf("AddProjectFacets returned error: %v", err)
	}
	if _, err := client.MigrateProjectLayout(context.Background(), "corr_test", projectcontracts.LayoutMigrationOptions{ProjectRef: "gmail-automation"}); err != nil {
		t.Fatalf("MigrateProjectLayout returned error: %v", err)
	}
	analysisEnvelope, err := client.AnalyzeProjectContractBackend(context.Background(), "corr_test", projectcontracts.BackendAnalysisInput{ProjectRef: "gmail-automation"})
	if err != nil {
		t.Fatalf("AnalyzeProjectContractBackend returned error: %v", err)
	}
	if !analysisEnvelope.Data.Current || analysisEnvelope.Data.DriftStatus != "current" || analysisEnvelope.Data.Analysis.Plan.Registerable != true {
		t.Fatalf("unexpected backend analysis result: %#v", analysisEnvelope.Data)
	}
	_, err = client.RegisterProjectContract(context.Background(), "corr_test", projects.RegisterProjectContractInput{
		ProjectRoot:           "/tmp/gmail-automation",
		ContractPath:          "/tmp/gmail-automation/loom.project.yaml",
		ContractHash:          "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		ContractSchemaVersion: "project.contract.v0.3",
		Contract:              json.RawMessage(`{}`),
		ValidationReport:      json.RawMessage(`{}`),
		RegistrationPlan:      json.RawMessage(`{}`),
		Project:               projects.ProjectContractProjectInput{Slug: "gmail-automation", Name: "Gmail Automation", OwnerNode: "main"},
		Facets:                []projects.ProjectContractFacetInput{{Key: "notes", Enabled: true, Present: true}},
	})
	if err != nil {
		t.Fatalf("RegisterProjectContract returned error: %v", err)
	}
	if _, err := client.RegisterProjectContractFromBackend(context.Background(), "corr_test", projects.RegisterProjectContractFromBackendInput{ProjectRef: "gmail-automation", Strict: true}); err != nil {
		t.Fatalf("RegisterProjectContractFromBackend returned error: %v", err)
	}
	if _, err := client.GetProjectRegistrationStatus(context.Background(), "corr_test", "gmail-automation"); err != nil {
		t.Fatalf("GetProjectRegistrationStatus returned error: %v", err)
	}
	if _, err := client.ActivateProject(context.Background(), "corr_test", "gmail-automation", projects.ActivateProjectInput{Facet: "scripts", ProjectRoot: "/tmp/gmail-automation"}); err != nil {
		t.Fatalf("ActivateProject returned error: %v", err)
	}
	if _, err := client.DeactivateProject(context.Background(), "corr_test", "gmail-automation", projects.DeactivateProjectInput{Facet: "scripts", ProjectRoot: "/tmp/gmail-automation", Reason: "test", DryRun: true}); err != nil {
		t.Fatalf("DeactivateProject returned error: %v", err)
	}
	if _, err := client.ArchiveProject(context.Background(), "corr_test", "gmail-automation", storagearchive.ProjectArchiveInput{
		SourceRef:  "macbook/Backups/Projects/gmail-automation/current",
		TargetPath: "main/Archive/Projects/gmail-automation",
		Reason:     "done",
	}); err != nil {
		t.Fatalf("ArchiveProject returned error: %v", err)
	}
	if _, err := client.InspectProjectArchive(context.Background(), "corr_test", "gmail-automation"); err != nil {
		t.Fatalf("InspectProjectArchive returned error: %v", err)
	}
	if _, err := client.PlanProjectArchiveRestore(context.Background(), "corr_test", "gmail-automation", storagearchive.ProjectArchiveRestoreInput{ToNode: "macbook", DryRun: true}); err != nil {
		t.Fatalf("PlanProjectArchiveRestore returned error: %v", err)
	}
	if _, err := client.PlanProjectRuntimeMigration(context.Background(), "corr_test", "gmail-automation", storagearchive.ProjectRuntimeMigrationInput{ToNode: "main", DryRun: true}); err != nil {
		t.Fatalf("PlanProjectRuntimeMigration returned error: %v", err)
	}
	if !sawScaffold || !sawAddFacets || !sawLayoutMigration || !sawAnalyzeBackend || !sawRegister || !sawRegisterBackend || !sawStatus || !sawActivate || !sawDeactivate || !sawArchive || !sawArchiveInspect || !sawArchiveRestore || !sawRuntimeMigration {
		t.Fatalf("expected all project registration paths, saw scaffold=%v add_facets=%v layout_migration=%v analyze_backend=%v register=%v register_backend=%v status=%v activate=%v deactivate=%v archive=%v inspect=%v restore=%v runtime_migrate=%v", sawScaffold, sawAddFacets, sawLayoutMigration, sawAnalyzeBackend, sawRegister, sawRegisterBackend, sawStatus, sawActivate, sawDeactivate, sawArchive, sawArchiveInspect, sawArchiveRestore, sawRuntimeMigration)
	}
}

func TestListPolicyDecisionsEncodesFilters(t *testing.T) {
	client := Client{
		client: &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				if req.Method != http.MethodGet {
					t.Fatalf("method = %s, want GET", req.Method)
				}
				if req.URL.Path != "/v1/policy/decisions" {
					t.Fatalf("path = %q, want /v1/policy/decisions", req.URL.Path)
				}
				query := req.URL.Query()
				wants := map[string]string{
					"limit":       "13",
					"actor":       "agent:dev-low",
					"origin_node": "main",
					"target_node": "node_main",
					"operation":   "capability:main@system.status.read",
					"decision":    "allow",
					"capability":  "main@system.status.read",
					"approval":    "approval_test",
					"grant":       "grant_test",
				}
				for key, want := range wants {
					if got := query.Get(key); got != want {
						t.Fatalf("query %s = %q, want %q", key, got, want)
					}
				}
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     make(http.Header),
					Body: io.NopCloser(bytes.NewBufferString(`{
						"ok": true,
						"data": [],
						"meta": {"correlation_id": "corr_test", "generated_at": "2026-05-05T00:00:00Z"}
					}`)),
				}, nil
			}),
		},
	}

	_, err := client.ListPolicyDecisions(context.Background(), "corr_test", policy.DecisionFilter{
		Limit:                 13,
		ActorRef:              "agent:dev-low",
		OriginNodeRef:         "main",
		TargetNodeRef:         "node_main",
		Operation:             "capability:main@system.status.read",
		Decision:              policy.DecisionAllow,
		CapabilityEndpointRef: "main@system.status.read",
		ApprovalRef:           "approval_test",
		GrantRef:              "grant_test",
	})
	if err != nil {
		t.Fatalf("ListPolicyDecisions returned error: %v", err)
	}
}

func TestExplainPolicyEncodesRequestBody(t *testing.T) {
	client := Client{
		client: &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				if req.Method != http.MethodPost {
					t.Fatalf("method = %s, want POST", req.Method)
				}
				if req.URL.Path != "/v1/policy/explain" {
					t.Fatalf("path = %q, want /v1/policy/explain", req.URL.Path)
				}
				var input policy.DecisionInput
				if err := json.NewDecoder(req.Body).Decode(&input); err != nil {
					t.Fatalf("decode request body: %v", err)
				}
				if input.Operation != "capability:main@script-runner.script.run" {
					t.Fatalf("operation = %q", input.Operation)
				}
				if input.ActorRef != "agent:dev-low" {
					t.Fatalf("actor_ref = %q", input.ActorRef)
				}
				if !input.CreateApprovalRequest {
					t.Fatal("create_approval_request should be true")
				}
				if input.ApprovalReason != "test approval" {
					t.Fatalf("approval_reason = %q", input.ApprovalReason)
				}
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     make(http.Header),
					Body: io.NopCloser(bytes.NewBufferString(`{
						"ok": true,
						"data": {
							"decision": {
								"policy_decision_id": "policy_decision_test",
								"resource_kind": "capability",
								"operation": "capability:main@script-runner.script.run",
								"decision": "approval_required",
								"reason_code": "actor_level_below_capability_level",
								"safe_explanation": "approval required",
								"context_hash": "sha256:0000000000000000000000000000000000000000000000000000000000000000",
								"context_summary": {},
								"created_at": "2026-05-05T00:00:00Z",
								"metadata": {}
							},
							"target": {
								"operation": "capability:main@script-runner.script.run",
								"resource_kind": "capability"
							},
							"context": {
								"resource_kind": "capability",
								"operation": "capability:main@script-runner.script.run"
							}
						},
						"meta": {"correlation_id": "corr_test", "generated_at": "2026-05-05T00:00:00Z"}
					}`)),
				}, nil
			}),
		},
	}

	_, err := client.ExplainPolicy(context.Background(), "corr_test", policy.DecisionInput{
		Operation:             "capability:main@script-runner.script.run",
		ActorRef:              "agent:dev-low",
		CreateApprovalRequest: true,
		ApprovalReason:        "test approval",
	})
	if err != nil {
		t.Fatalf("ExplainPolicy returned error: %v", err)
	}
}

func TestCallCapabilityEncodesRequestBody(t *testing.T) {
	client := Client{
		client: &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				if req.Method != http.MethodPost {
					t.Fatalf("method = %s, want POST", req.Method)
				}
				if req.URL.Path != "/v1/capability-calls" {
					t.Fatalf("path = %q, want /v1/capability-calls", req.URL.Path)
				}
				if got := req.Header.Get(idempotency.Header); got != "idem_call" {
					t.Fatalf("idempotency header = %q, want idem_call", got)
				}
				var input routing.CapabilityCallInput
				if err := json.NewDecoder(req.Body).Decode(&input); err != nil {
					t.Fatalf("decode request body: %v", err)
				}
				if input.Target != "main@system.status.read" {
					t.Fatalf("target = %q", input.Target)
				}
				if input.ActorRef != "owner" {
					t.Fatalf("actor_ref = %q", input.ActorRef)
				}
				return &http.Response{
					StatusCode: http.StatusCreated,
					Header:     make(http.Header),
					Body: io.NopCloser(bytes.NewBufferString(`{
						"ok": true,
						"data": {
							"route": {"route_id":"route_test"},
							"capability_call": {"capability_call_id":"capability_call_test"},
							"status": "completed"
						},
						"meta": {
							"correlation_id": "corr_test",
							"generated_at": "2026-05-05T00:00:00Z",
							"idempotency_key": "idem_call"
						}
					}`)),
				}, nil
			}),
		},
	}.WithIdempotencyKey("idem_call")

	envelope, err := client.CallCapability(context.Background(), "corr_test", routing.CapabilityCallInput{
		Target:   "main@system.status.read",
		ActorRef: "owner",
		Input:    json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatalf("CallCapability returned error: %v", err)
	}
	if envelope.Data.Status != "completed" {
		t.Fatalf("status = %q, want completed", envelope.Data.Status)
	}
}

func TestListRoutesAndCapabilityCallsEncodeFilters(t *testing.T) {
	requests := 0
	client := Client{
		client: &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				requests++
				query := req.URL.Query()
				if query.Get("limit") != "9" || query.Get("status") != "completed" || query.Get("capability") != "main@system.status.read" || query.Get("correlation") != "corr_filter" {
					t.Fatalf("unexpected query for %s: %s", req.URL.Path, req.URL.RawQuery)
				}
				if req.URL.Path == "/v1/capability-calls" && query.Get("idempotency_key") != "idem_filter" {
					t.Fatalf("idempotency query = %q, want idem_filter", query.Get("idempotency_key"))
				}
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     make(http.Header),
					Body: io.NopCloser(bytes.NewBufferString(`{
						"ok": true,
						"data": [],
						"meta": {"correlation_id": "corr_test", "generated_at": "2026-05-05T00:00:00Z"}
					}`)),
				}, nil
			}),
		},
	}

	if _, err := client.ListRoutes(context.Background(), "corr_test", routing.RouteFilter{
		Limit:                 9,
		Status:                routing.RouteStatusCompleted,
		CapabilityEndpointRef: "main@system.status.read",
		CorrelationID:         "corr_filter",
	}); err != nil {
		t.Fatalf("ListRoutes returned error: %v", err)
	}
	if _, err := client.ListCapabilityCalls(context.Background(), "corr_test", routing.CapabilityCallFilter{
		Limit:                 9,
		Status:                routing.CapabilityCallStatusCompleted,
		CapabilityEndpointRef: "main@system.status.read",
		CorrelationID:         "corr_filter",
		IdempotencyKey:        "idem_filter",
	}); err != nil {
		t.Fatalf("ListCapabilityCalls returned error: %v", err)
	}
	if requests != 2 {
		t.Fatalf("requests = %d, want 2", requests)
	}
}

func TestListApprovalsEncodesFilters(t *testing.T) {
	client := Client{
		client: &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				if req.Method != http.MethodGet {
					t.Fatalf("method = %s, want GET", req.Method)
				}
				if req.URL.Path != "/v1/approvals" {
					t.Fatalf("path = %q, want /v1/approvals", req.URL.Path)
				}
				query := req.URL.Query()
				wants := map[string]string{
					"limit":           "11",
					"status":          "pending",
					"actor":           "agent:dev-low",
					"approving_actor": "owner",
					"target_node":     "main",
					"capability":      "main@script-runner.script.run",
				}
				for key, want := range wants {
					if got := query.Get(key); got != want {
						t.Fatalf("query %s = %q, want %q", key, got, want)
					}
				}
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     make(http.Header),
					Body: io.NopCloser(bytes.NewBufferString(`{
						"ok": true,
						"data": [],
						"meta": {"correlation_id": "corr_test", "generated_at": "2026-05-05T00:00:00Z"}
					}`)),
				}, nil
			}),
		},
	}

	_, err := client.ListApprovals(context.Background(), "corr_test", policy.ApprovalFilter{
		Limit:                 11,
		Status:                policy.ApprovalPending,
		ActorRef:              "agent:dev-low",
		ApprovingActorRef:     "owner",
		TargetNodeRef:         "main",
		CapabilityEndpointRef: "main@script-runner.script.run",
	})
	if err != nil {
		t.Fatalf("ListApprovals returned error: %v", err)
	}
}

func TestApprovalDecisionEncodesPathAndBody(t *testing.T) {
	client := Client{
		client: &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				if req.Method != http.MethodPost {
					t.Fatalf("method = %s, want POST", req.Method)
				}
				if req.URL.Path != "/v1/approvals/approval_test/decide" {
					t.Fatalf("path = %q, want /v1/approvals/approval_test/decide", req.URL.Path)
				}
				var input policy.ApprovalDecisionInput
				if err := json.NewDecoder(req.Body).Decode(&input); err != nil {
					t.Fatalf("decode request body: %v", err)
				}
				if input.Decision != policy.ApprovalDecisionApprove {
					t.Fatalf("decision = %q", input.Decision)
				}
				if input.DecidingActorRef != "owner" {
					t.Fatalf("deciding_actor_ref = %q", input.DecidingActorRef)
				}
				if input.DecisionReason != "approved for smoke" {
					t.Fatalf("decision_reason = %q", input.DecisionReason)
				}
				if input.GrantTTLSeconds != 900 {
					t.Fatalf("grant_ttl_seconds = %d", input.GrantTTLSeconds)
				}
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     make(http.Header),
					Body: io.NopCloser(bytes.NewBufferString(`{
						"ok": true,
						"data": {
							"approval": {
								"approval_id": "approval_test",
								"approval_key": "approval_test",
								"requested_by_actor_id": "actor_low",
								"resource_kind": "capability",
								"operation": "capability:main@script-runner.script.run",
								"risk_level": "high",
								"execution_authorization_level": 4,
								"request_reason": "test",
								"action_summary": "test",
								"status": "approved",
								"created_at": "2026-05-05T00:00:00Z",
								"expires_at": "2026-05-05T00:15:00Z",
								"metadata": {}
							}
						},
						"meta": {"correlation_id": "corr_test", "generated_at": "2026-05-05T00:00:00Z"}
					}`)),
				}, nil
			}),
		},
	}

	_, err := client.DecideApproval(context.Background(), "corr_test", "approval_test", policy.ApprovalDecisionInput{
		Decision:         policy.ApprovalDecisionApprove,
		DecidingActorRef: "owner",
		DecisionReason:   "approved for smoke",
		GrantTTLSeconds:  900,
	})
	if err != nil {
		t.Fatalf("DecideApproval returned error: %v", err)
	}
}

func TestListGrantsEncodesFilters(t *testing.T) {
	client := Client{
		client: &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				if req.Method != http.MethodGet {
					t.Fatalf("method = %s, want GET", req.Method)
				}
				if req.URL.Path != "/v1/grants" {
					t.Fatalf("path = %q, want /v1/grants", req.URL.Path)
				}
				query := req.URL.Query()
				wants := map[string]string{
					"limit":       "9",
					"status":      "active",
					"type":        "one_shot",
					"actor":       "agent:dev-low",
					"granted_by":  "owner",
					"approval":    "approval_test",
					"target_node": "main",
					"capability":  "main@script-runner.script.run",
				}
				for key, want := range wants {
					if got := query.Get(key); got != want {
						t.Fatalf("query %s = %q, want %q", key, got, want)
					}
				}
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     make(http.Header),
					Body: io.NopCloser(bytes.NewBufferString(`{
						"ok": true,
						"data": [],
						"meta": {"correlation_id": "corr_test", "generated_at": "2026-05-05T00:00:00Z"}
					}`)),
				}, nil
			}),
		},
	}

	_, err := client.ListGrants(context.Background(), "corr_test", policy.GrantFilter{
		Limit:                 9,
		Status:                policy.GrantActive,
		GrantType:             policy.GrantOneShot,
		GrantedToActorRef:     "agent:dev-low",
		GrantedByActorRef:     "owner",
		ApprovalRef:           "approval_test",
		TargetNodeRef:         "main",
		CapabilityEndpointRef: "main@script-runner.script.run",
	})
	if err != nil {
		t.Fatalf("ListGrants returned error: %v", err)
	}
}

func TestGrantRevokeEncodesPathAndBody(t *testing.T) {
	client := Client{
		client: &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				if req.Method != http.MethodPost {
					t.Fatalf("method = %s, want POST", req.Method)
				}
				if req.URL.Path != "/v1/grants/grant_test/revoke" {
					t.Fatalf("path = %q, want /v1/grants/grant_test/revoke", req.URL.Path)
				}
				var input policy.GrantRevokeInput
				if err := json.NewDecoder(req.Body).Decode(&input); err != nil {
					t.Fatalf("decode request body: %v", err)
				}
				if input.RevokedByActorRef != "owner" {
					t.Fatalf("revoked_by_actor_ref = %q", input.RevokedByActorRef)
				}
				if input.Reason != "smoke reset" {
					t.Fatalf("reason = %q", input.Reason)
				}
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     make(http.Header),
					Body: io.NopCloser(bytes.NewBufferString(`{
						"ok": true,
						"data": {
							"grant_id": "grant_test",
							"grant_key": "grant_test",
							"grant_type": "one_shot",
							"granted_by_actor_id": "actor_owner",
							"granted_to_actor_id": "actor_low",
							"status": "revoked",
							"bypass_confirmation": true,
							"scope_constraints": {},
							"node_constraints": {},
							"capability_constraints": {},
							"credential_constraints": {},
							"object_constraints": {},
							"egress_constraints": {},
							"uses_count": 0,
							"audit_level": "audit",
							"created_at": "2026-05-05T00:00:00Z",
							"expires_at": "2026-05-05T00:15:00Z",
							"metadata": {}
						},
						"meta": {"correlation_id": "corr_test", "generated_at": "2026-05-05T00:00:00Z"}
					}`)),
				}, nil
			}),
		},
	}

	_, err := client.RevokeGrant(context.Background(), "corr_test", "grant_test", policy.GrantRevokeInput{
		RevokedByActorRef: "owner",
		Reason:            "smoke reset",
	})
	if err != nil {
		t.Fatalf("RevokeGrant returned error: %v", err)
	}
}

func TestDecommissionNodeEncodesPathAndBody(t *testing.T) {
	client := Client{
		client: &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				if req.Method != http.MethodPost {
					t.Fatalf("method = %s, want POST", req.Method)
				}
				if req.URL.EscapedPath() != "/v1/nodes/workspace%2Fone/decommission" {
					t.Fatalf("escaped path = %q, want encoded node decommission path", req.URL.EscapedPath())
				}
				var input nodes.DecommissionNodeInput
				if err := json.NewDecoder(req.Body).Decode(&input); err != nil {
					t.Fatalf("decode request body: %v", err)
				}
				if input.Reason != "retire test" {
					t.Fatalf("reason = %q", input.Reason)
				}
				return okEnvelope(`{
					"node": {
						"node_id": "node_workspace",
						"node_key": "workspace/one",
						"display_name": "Workspace",
						"node_kind": "workspace",
						"node_role": "primary_workspace",
						"runtime_class": "workspace_full",
						"status": "retired",
						"presence_state": "revoked",
						"enrollment_status": "revoked",
						"credential_status": "revoked",
						"metadata": {},
						"created_at": "2026-06-03T00:00:00Z",
						"updated_at": "2026-06-03T00:00:00Z"
					},
					"revoked_credentials": 1,
					"reason": "retire test"
				}`), nil
			}),
		},
	}
	envelope, err := client.DecommissionNode(context.Background(), "corr_test", "workspace/one", nodes.DecommissionNodeInput{
		Reason: "retire test",
	})
	if err != nil {
		t.Fatalf("DecommissionNode returned error: %v", err)
	}
	if envelope.Data.RevokedCredentials != 1 {
		t.Fatalf("revoked_credentials = %d, want 1", envelope.Data.RevokedCredentials)
	}
	if envelope.Data.Node.Status != "retired" {
		t.Fatalf("node status = %q, want retired", envelope.Data.Node.Status)
	}
}

func TestCloudRestoreCleanupClientWireAndErrors(t *testing.T) {
	for _, apply := range []bool{false, true} {
		for _, fail := range []bool{false, true} {
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			deadline, _ := ctx.Deadline()
			calls := 0
			input := cloudstorage.RestoreCleanupApplyInput{Attempt: "exact-attempt", ConfirmDigest: strings.Repeat("a", 64), Yes: true, DryRun: true}
			client := Client{client: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				verb := "plan"
				if apply {
					verb = "apply"
				}
				if r.Method != "POST" || r.URL.Path != "/v1/cloud/snapshot/restore-cleanup/"+verb || r.Header.Get(correlation.Header) != "corr_cleanup" {
					t.Fatal("wire route changed")
				}
				if got, ok := r.Context().Deadline(); !ok || !got.Equal(deadline) {
					t.Fatal("context lost")
				}
				var got map[string]json.RawMessage
				if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
					t.Fatal(err)
				}
				if apply && len(got) != 4 || !apply && len(got) != 1 {
					t.Fatal("unexpected authority fields")
				}
				if fail {
					b, _ := json.Marshal(response.Failure("corr_refused", loomerrors.New("cloud.restore_cleanup_refused", "cloud", "restore-cleanup", "Cleanup refused.")))
					return &http.Response{StatusCode: 409, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(b))}, nil
				}
				return okEnvelope(`{"status":"planned","attempt":"exact-attempt"}`), nil
			})}}
			var result response.Envelope[cloudstorage.RestoreCleanupResult]
			var err error
			if apply {
				result, err = client.CloudRestoreCleanupApply(ctx, "corr_cleanup", input)
			} else {
				result, err = client.CloudRestoreCleanupPlan(ctx, "corr_cleanup", cloudstorage.RestoreCleanupPlanInput{Attempt: input.Attempt})
			}
			if calls != 1 {
				t.Fatal("unexpected retry")
			}
			if fail {
				var typed *RequestError
				if !errors.As(err, &typed) || typed.LoomError().Code != "cloud.restore_cleanup_refused" || typed.CorrelationID("") != "corr_refused" {
					t.Fatal("typed refusal lost", err)
				}
			} else if err != nil || result.Data.Status != "planned" {
				t.Fatal(result, err)
			}
		}
	}
}

func TestCalendarCreateScheduleClientContract(t *testing.T) {
	for _, dry := range []bool{true, false} {
		client := Client{BaseURL: "http://loom", client: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if req.Method != "POST" || req.URL.Path != "/v1/schedules" {
				t.Fatalf("schedule request %s %s", req.Method, req.URL.Path)
			}
			var fields map[string]json.RawMessage
			if err := json.NewDecoder(req.Body).Decode(&fields); err != nil {
				t.Fatal(err)
			}
			if string(fields["schedule_kind"]) != `"cron"` || string(fields["schedule_expr"]) != `"15 9 * * *"` || string(fields["timezone"]) != `"Europe/Amsterdam"` || string(fields["run_as_actor_ref"]) != `"owner"` || string(fields["project_ref"]) != `"project_calendar"` {
				t.Fatalf("wire fields %+v", fields)
			}
			if dry && string(fields["dry_run"]) != "true" {
				t.Fatal("preview flag lost")
			}
			if !dry {
				if _, ok := fields["dry_run"]; ok {
					t.Fatal("false dry_run must remain omitted for existing clients")
				}
			}
			return okEnvelope(fmt.Sprintf(`{"dry_run":%t,"schedule":{"schedule_kind":"cron","schedule_expr":"15 9 * * *","timezone":"Europe/Amsterdam","status":"disabled","next_fire_at":"2026-09-10T07:15:00Z"}}`, dry)), nil
		})}}
		got, err := client.CreateSchedule(context.Background(), "corr_calendar_client", automation.CreateScheduleInput{ScheduleKey: "calendar", ScheduleKind: "cron", ScheduleExpr: "15 9 * * *", Timezone: "Europe/Amsterdam", RunAsActorRef: "owner", ProjectRef: "project_calendar", DryRun: dry})
		if err != nil || got.Data.DryRun != dry || got.Data.Schedule.ScheduleKind != "cron" || got.Data.Schedule.Timezone != "Europe/Amsterdam" || got.Data.Schedule.NextFireAt == nil || got.Data.Schedule.NextFireAt.Format(time.RFC3339) != "2026-09-10T07:15:00Z" {
			t.Fatalf("calendar response %+v %v", got, err)
		}
	}
}
