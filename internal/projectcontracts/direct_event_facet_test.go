package projectcontracts

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestDirectEventFacetDiscoversValidEvent(t *testing.T) {
	root := directEventProjectFixture(t, true)
	writeDirectEventPackage(t, root, "gmail_message", validDirectEventContract("gmail_message", "gmail", "main@direct-event-smoke.hello_world"), `{"id":"msg-1","url":"https://example.invalid"}`, `{"input":{"url":"https://example.invalid"}}`)

	analysis := Analyze(root)
	if !analysis.Report.OK {
		t.Fatalf("expected report ok, diagnostics=%#v", analysis.Report.Diagnostics)
	}
	if len(analysis.Report.DirectEvents) != 1 {
		t.Fatalf("direct events = %#v", analysis.Report.DirectEvents)
	}
	event := analysis.Report.DirectEvents[0]
	if event.Key != "gmail_message" {
		t.Fatalf("event key = %q", event.Key)
	}
	if event.BackendIntegrationKey != "direct_event_smoke__gmail" {
		t.Fatalf("backend integration key = %q", event.BackendIntegrationKey)
	}
	if event.BackendEndpointSlug != "direct_event_smoke__gmail_message" {
		t.Fatalf("backend endpoint slug = %q", event.BackendEndpointSlug)
	}
	if event.EndpointPath != "/v1/direct-events/ingest/direct_event_smoke__gmail_message" {
		t.Fatalf("endpoint path = %q", event.EndpointPath)
	}
	if event.TargetCapability != "main@direct-event-smoke.hello_world" {
		t.Fatalf("target = %q", event.TargetCapability)
	}
	if event.ActivationStatus != DirectEventActivationStatusPending {
		t.Fatalf("activation = %q", event.ActivationStatus)
	}
	assertPlanAction(t, analysis.Plan.Actions, "would_register_integration", "direct_event_smoke__gmail")
	assertPlanAction(t, analysis.Plan.Actions, "would_register_direct_event_endpoint", "direct_event_smoke__gmail_message")
	assertPlanAction(t, analysis.Plan.Actions, "would_pause_project_direct_event", "direct_event_smoke__gmail_message")
}

func TestDirectEventFacetDisabledDoesNotScan(t *testing.T) {
	root := directEventProjectFixture(t, false)
	writeDirectEventPackage(t, root, "broken", "bad: [", `{}`, `{}`)

	analysis := Analyze(root)
	if !analysis.Report.OK {
		t.Fatalf("expected disabled direct-event facet to skip scan: %#v", analysis.Report.Diagnostics)
	}
	if len(analysis.Report.DirectEvents) != 0 {
		t.Fatalf("direct events = %#v", analysis.Report.DirectEvents)
	}
}

func TestDirectEventFacetDiagnostics(t *testing.T) {
	t.Run("missing manifest", func(t *testing.T) {
		root := directEventProjectFixture(t, true)
		mkdir(t, root, "direct_events/gmail_message")
		analysis := Analyze(root)
		if analysis.Report.OK {
			t.Fatal("expected missing manifest to fail")
		}
		assertDiagnostic(t, analysis.Report.Diagnostics, "direct_event.manifest_missing")
	})

	t.Run("invalid target", func(t *testing.T) {
		root := directEventProjectFixture(t, true)
		writeDirectEventPackage(t, root, "gmail_message", validDirectEventContract("gmail_message", "gmail", "not a capability"), `{}`, `{}`)
		analysis := Analyze(root)
		if analysis.Report.OK {
			t.Fatal("expected invalid target to fail")
		}
		assertDiagnostic(t, analysis.Report.Diagnostics, "direct_event.target_invalid")
	})

	t.Run("secret value forbidden", func(t *testing.T) {
		root := directEventProjectFixture(t, true)
		contract := strings.Replace(validDirectEventContract("gmail_message", "gmail", "main@direct-event-smoke.hello_world"), "create_if_missing: true", "token: abc123", 1)
		writeDirectEventPackage(t, root, "gmail_message", contract, `{}`, `{}`)
		analysis := Analyze(root)
		if analysis.Report.OK {
			t.Fatal("expected inline token to fail")
		}
		assertDiagnostic(t, analysis.Report.Diagnostics, "direct_event.secret_value_forbidden")
	})

	t.Run("mapping mismatch", func(t *testing.T) {
		root := directEventProjectFixture(t, true)
		writeDirectEventPackage(t, root, "gmail_message", validDirectEventContract("gmail_message", "gmail", "main@direct-event-smoke.hello_world"), `{"id":"msg-1","url":"https://example.invalid"}`, `{"input":{"url":"wrong"}}`)
		analysis := Analyze(root)
		if analysis.Report.OK {
			t.Fatal("expected mapping mismatch to fail")
		}
		assertDiagnostic(t, analysis.Report.Diagnostics, "direct_event.mapping_expected_mismatch")
	})
}

func TestProjectDirectEventBackendKeysAreStableAndBounded(t *testing.T) {
	got := ProjectDirectEventEndpointSlug("gmail-automation", "message_received")
	if got != "gmail_automation__message_received" {
		t.Fatalf("ProjectDirectEventEndpointSlug = %q", got)
	}
	long := ProjectDirectEventEndpointSlug("project-"+strings.Repeat("a", 90), "event_"+strings.Repeat("b", 90))
	if len(long) > 81 {
		t.Fatalf("direct-event key length = %d, want <= 81: %q", len(long), long)
	}
	if !scheduleKeyPattern.MatchString(long) {
		t.Fatalf("direct-event key does not match backend key pattern: %q", long)
	}
	if long != ProjectDirectEventEndpointSlug("project-"+strings.Repeat("a", 90), "event_"+strings.Repeat("b", 90)) {
		t.Fatal("direct-event key should be stable")
	}
}

func directEventProjectFixture(t *testing.T, enabled bool) string {
	t.Helper()
	enabledText := "false"
	if enabled {
		enabledText = "true"
	}
	root := projectFixture(t, `
kind: loom.project
schema_version: project.contract.v0.3
project:
  slug: direct-event-smoke
  name: Direct Event Smoke
  owner_node: main
facets:
  direct_events: `+enabledText+`
`)
	mkdir(t, root, "direct_events")
	return root
}

func writeDirectEventPackage(t *testing.T, root, key, contract, payload, expected string) {
	t.Helper()
	packageRoot := filepath.Join(root, "direct_events", key)
	mkdir(t, root, filepath.ToSlash(filepath.Join("direct_events", key, "examples")))
	writeFile(t, packageRoot, "loom.direct_event.yaml", contract, 0o600)
	writeFile(t, packageRoot, "examples/payload.json", payload, 0o600)
	writeFile(t, packageRoot, "examples/expected_mapped_input.json", expected, 0o600)
}

func validDirectEventContract(key, integration, target string) string {
	return `
kind: loom.direct_event
schema_version: direct_event.contract.v0.3
event:
  key: ` + key + `
  display_name: Gmail Message
  description: Test direct event.
  status: draft
integration:
  key: ` + integration + `
  display_name: Gmail
  main_auth_level: 3
endpoint:
  slug: ` + key + `
  display_name: Gmail Message
  event_type: gmail.message.received
  status: draft
target:
  capability: ` + target + `
auth:
  profiles:
    - name: private_network
      kind: private_network
      create_if_missing: true
mapping:
  fields:
    input.url:
      source: body
      expr: $.url
  required:
    - input.url
idempotency:
  strategy: payload_path
  path: $.id
response:
  mode: accepted
timeout:
  seconds: 60
retry:
  max_attempts: 1
storage:
  payload_limit_bytes: 1048576
  store_raw_body: true
examples:
  payload: examples/payload.json
  expected_mapped_input: examples/expected_mapped_input.json
`
}
