package loomcli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/spf13/cobra"
	pc "loom.local/loom/internal/projectcontracts"
	"loom.local/loom/internal/response"
)

func TestProjectContextCurrentDevelopmentPrecedesRepeatedEvidence(t *testing.T) {
	status := webDAVContextFixture()
	status.Development.Purpose = projectPurposeScaffold
	status.Development.Progress = "Archive and restore complete; useful work preserved. " + strings.Repeat("retained evidence ", 80)
	status.Development.CurrentFocus = "Continue only the next selected episode."
	status.Development.NextAction = "No pending archive action."
	status.Development.Conversations = &pc.DeclarationConversationIndex{Path: ".project/CONVERSATIONS.md", Hash: "source-hash", Excerpt: "Coding | exact-thread | main | repos/code\n" + strings.Repeat("context ", 100)}
	for i := 0; i < 8; i++ {
		status.Resources[pc.ResourceKey(fmt.Sprintf("lab_%d", i))] = status.Resources["webdav"]
	}
	view := newProjectContext(status)
	if view.Development == nil || view.Development.Purpose != "" || view.Development.PurposeStatus != "unset_scaffold" || view.Development.CurrentFocus != status.Development.CurrentFocus || !strings.HasPrefix(view.Development.Progress, "Archive and restore complete") || view.Omitted["development_excerpt"] == 0 || view.Omitted["resources"] == 0 || view.Omitted["development_detail"] != 0 || view.Development.Conversations == nil {
		t.Fatalf("optional detail displaced current context: %+v", view)
	}
	for _, jsonMode := range []bool{false, true} {
		var out bytes.Buffer
		cmd := &cobra.Command{}
		cmd.SetOut(&out)
		if err := renderProjectContext(cmd, &options{jsonOutput: jsonMode}, view); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out.String(), "Archive and restore complete") || !strings.Contains(out.String(), "Continue only the next selected episode.") || strings.Contains(out.String(), projectPurposeScaffold) {
			t.Fatal("current state absent or scaffold presented as purpose", out.String())
		}
		if jsonMode {
			decodeContext(t, out.String())
		} else if out.Len() > 4096 || strings.Count(out.String(), "\n") > 16 {
			t.Fatal("human budget exceeded", out.String())
		}
	}
	// Exact generated prose only: real user purposes containing similar words survive.
	status.Development.Purpose = projectPurposeScaffold + " This is a writing exercise."
	view = newProjectContext(status)
	if view.Development.Purpose != status.Development.Purpose || view.Development.PurposeStatus != "source_declared" {
		t.Fatal("user text mistaken for scaffold")
	}
	raw, err := os.ReadFile("../projectcontracts/project_development_pack/templates/.project/OVERVIEW.md")
	if err != nil || !strings.Contains(string(raw), projectPurposeScaffold) {
		t.Fatal("scaffold recognition no longer matches generated source", err)
	}
}

func TestProjectContextCarriesExactExcerptDisclosure(t *testing.T) {
	status := webDAVContextFixture()
	status.Development.Progress = "Backend source excerpt..."
	status.Development.TruncatedFields = []string{"progress", "progress", "untrusted\nfield"}
	view := newProjectContext(status)
	if !reflect.DeepEqual(view.Development.TruncatedFields, []string{"progress"}) {
		t.Fatal("upstream disclosure lost or unsafe fields copied", view.Development)
	}
	var out bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&out)
	if err := renderProjectContext(cmd, &options{}, view); err != nil || !strings.Contains(out.String(), "excerpts: progress (read .project sources for full text)") {
		t.Fatal("human excerpt not disclosed", err, out.String())
	}
	status.Development.TruncatedFields = nil
	view = newProjectContext(status)
	if len(view.Development.TruncatedFields) != 0 {
		t.Fatal("authored ellipsis mistaken for truncation")
	}
	status.Development.Progress = strings.Repeat("字", 1000)
	view = newProjectContext(status)
	if !reflect.DeepEqual(view.Development.TruncatedFields, []string{"progress"}) || !utf8.ValidString(view.Development.Progress) {
		t.Fatal("CLI truncation not disclosed", view.Development)
	}
}

func webDAVContextFixture() pc.DeclarationStatus {
	s := contextFixture()
	s.ObservedAt = "2026-10-12T08:00:00Z"
	s.Development = &pc.DeclarationDevelopmentContext{Posture: "ready", Purpose: "Serve Zotero files", CurrentFocus: "Routine use", Progress: "Deployed", NextAction: "Continue ordinary project work"}
	s.Readiness.Queued.State, s.Readiness.Applied.State = pc.DeclarationUnknown, pc.DeclarationUnknown
	s.Resources = map[pc.ResourceKey]pc.DeclarationResourceStatus{
		"source": {Path: "/owner/quote's project/src", Binding: pc.DeclarationBinding{Kind: pc.DeclarationRepository, RepositoryID: "verified-repository"}},
		"files":  {Binding: pc.DeclarationBinding{Kind: pc.DeclarationKnowledge}},
		"webdav": {Binding: pc.DeclarationBinding{Kind: pc.DeclarationApplication}, Readiness: pc.DeclarationReadiness{
			Desired:   pc.DeclarationFact{State: pc.DeclarationSatisfied, Revision: "desired-application-input"},
			Applied:   pc.DeclarationFact{State: pc.DeclarationSatisfied, Revision: "installation-revision", EvidenceRef: "application-installation:webdav"},
			Healthy:   pc.DeclarationFact{State: pc.DeclarationSatisfied, Revision: "generation-revision", EvidenceRef: "application-installation:webdav"},
			Protected: pc.DeclarationFact{State: pc.DeclarationSatisfied, Revision: "backup-manifest-revision", EvidenceRef: "backup-operation"},
			Verified:  pc.DeclarationFact{State: pc.DeclarationUnknown},
			Endpoint:  &pc.ApplicationEndpointStatus{URL: "https://webdav.example.test", DNS: "satisfied", TLS: "satisfied", Routing: "satisfied", HTTPStatus: 207, ObservedAt: time.Date(2026, 10, 12, 7, 59, 0, 0, time.UTC)},
		}},
	}
	return s
}

func TestProjectContextWorkStateIndependentOfReadiness(t *testing.T) {
	for _, state := range []string{"", "working", "passive", "archived", "passive\ncommand"} {
		status := webDAVContextFixture()
		status.WorkState = state
		before := newProjectContext(status)
		status.WorkState = ""
		baseline := newProjectContext(status)
		want := ""
		if pc.ValidProjectWorkState(state) {
			want = state
		}
		if before.WorkState != want || before.Readiness != baseline.Readiness || before.Lifecycle != baseline.Lifecycle {
			t.Fatal("classification changed readiness/lifecycle", before)
		}
		raw, err := json.Marshal(before)
		if err != nil || bytes.Contains(raw, []byte(`"work_state":"`)) != (want != "") {
			t.Fatalf("context JSON %s: %v", raw, err)
		}
		cmd := &cobra.Command{}
		var output bytes.Buffer
		cmd.SetOut(&output)
		if err := renderProjectContext(cmd, &options{}, before); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(output.String(), "Work state: "+state+" (independent of runtime status)") != (want != "") {
			t.Fatal(output.String())
		}
		if len(raw)+1 > 8192 || output.Len() > 4096 || strings.Count(output.String(), "\n") > 16 {
			t.Fatal("work state exceeded context budget")
		}
	}
}

func TestProjectContextConversationIndexPreservesPointerAndBounds(t *testing.T) {
	status := webDAVContextFixture()
	index := &pc.DeclarationConversationIndex{Path: ".project/CONVERSATIONS.md", Hash: "exact-source-hash", Excerpt: "# Conversations\nCoding | exact-thread | main | repos/code | neutral\n"}
	status.Development.Conversations = index
	view := newProjectContext(status)
	if view.Development.Conversations == nil || !reflect.DeepEqual(view.Development.Conversations, index) || !view.fits() {
		t.Fatal("known conversation pointer lost")
	}
	var out bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&out)
	if err := renderProjectContext(cmd, &options{}, view); err != nil || !strings.Contains(out.String(), "exact-thread") {
		t.Fatal("human context hides conversation pointer")
	}
	index.Excerpt = "bad\x1b[31m"
	view = newProjectContext(status)
	if view.Development.Conversations == nil || view.Development.Conversations.Hash != index.Hash || view.Development.Conversations.Excerpt != "" || !view.Development.Conversations.Truncated || view.Omitted["conversation_excerpt"] != 1 {
		t.Fatal("unsafe excerpt lost safe file pointer or was rendered")
	}
}

func TestProjectContextConversationNavigationPrecedesOptionalProse(t *testing.T) {
	status := webDAVContextFixture()
	status.Development.Purpose = strings.Repeat("p", 1800)
	status.Development.CurrentFocus = strings.Repeat("f", 1800)
	status.Development.Progress = strings.Repeat("h", 1800)
	status.Development.Conversations = &pc.DeclarationConversationIndex{Path: ".project/CONVERSATIONS.md", Hash: "exact-hash", Excerpt: "Coding | exact-thread | main | repos/code | ordinary\n" + strings.Repeat("context ", 100)}
	view := newProjectContext(status)
	if view.Development == nil || view.Development.Conversations == nil || view.Development.Conversations.Truncated || !strings.Contains(view.Development.Conversations.Excerpt, "exact-thread") || !view.fits() {
		t.Fatalf("optional detail crowded out conversation navigation: %+v", view)
	}
}

func TestProjectContextWebDAVResourceQualificationsAndNavigation(t *testing.T) {
	status := webDAVContextFixture()
	view := newProjectContext(status)
	if len(view.Resources) != 3 || len(view.Repositories) != 1 || view.Omitted["resources"] != 0 || view.Development.Posture != "ready" {
		t.Fatalf("inspected shape hidden: %+v", view)
	}
	if view.NextAction.Kind != "none" || strings.Contains(view.NextAction.Command, "old-operation") || strings.Contains(view.NextAction.Command, "apply") {
		t.Fatalf("historical success became action: %+v", view.NextAction)
	}
	var webdav contextResource
	for _, r := range view.Resources {
		if r.Key == "webdav" {
			webdav = r
		}
	}
	if webdav.Applied.Revision != "installation-revision" || webdav.Healthy.Revision != "generation-revision" || webdav.Protected.Revision != "backup-manifest-revision" || webdav.Protected.RevisionBasis != "owner_revision_not_declaration" || webdav.Protected.Coverage != "resource_only" || webdav.Protected.EvidenceTime != "not_reported" || webdav.Protected.ReadAt != status.ObservedAt || webdav.Endpoint == nil || !webdav.Endpoint.ObservedAt.Equal(status.Resources["webdav"].Readiness.Endpoint.ObservedAt) {
		t.Fatalf("mixed revision/time/coverage: %+v", webdav)
	}
	if view.Readiness.Healthy.State != pc.DeclarationUnknown || view.Readiness.Protected.State != pc.DeclarationUnknown {
		t.Fatalf("invented overall green: %+v", view.Readiness)
	}
	for _, jsonMode := range []bool{false, true} {
		var out bytes.Buffer
		cmd := &cobra.Command{}
		cmd.SetOut(&out)
		if err := renderProjectContext(cmd, &options{jsonOutput: jsonMode}, view); err != nil {
			t.Fatal(err)
		}
		if jsonMode {
			got := decodeContext(t, out.String())
			if len(got["resources"].([]any)) != 3 {
				t.Fatalf("resources: %s", out.String())
			}
		} else {
			for _, value := range []string{"Root: /owner/quote's project", "Repository: source; path /owner/quote's project/src", "Development: ready", "Resource: webdav", "installation-revision", "generation-revision", "backup-manifest-revision", "Publication: webdav", "DNS=satisfied TLS=satisfied Route=satisfied", "fact observation times not reported"} {
				if !strings.Contains(out.String(), value) {
					t.Fatalf("missing %q: %s", value, out.String())
				}
			}
			if out.Len() > 4096 || strings.Count(out.String(), "\n") > 16 {
				t.Fatalf("human bounds: %s", out.String())
			}
		}
	}
}

func TestProjectContextGroupsUnknownFactsWithoutInventingEvidence(t *testing.T) {
	view := projectContext{
		Revision: "current-declaration",
		Resources: []contextResource{{
			Key: "notes", Kind: pc.DeclarationKnowledge,
			Applied:   contextFact{DeclarationFact: pc.DeclarationFact{State: pc.DeclarationUnknown, Revision: "unproven-revision"}},
			Healthy:   contextFact{DeclarationFact: pc.DeclarationFact{State: pc.DeclarationUnknown, Revision: "unproven-revision"}},
			Protected: contextFact{DeclarationFact: pc.DeclarationFact{State: pc.DeclarationUnknown, Revision: "unproven-revision"}},
			Verified:  contextFact{DeclarationFact: pc.DeclarationFact{State: pc.DeclarationUnknown, Revision: "unproven-revision"}},
		}},
	}
	before, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&out)
	if err := renderProjectContext(cmd, &options{}, view); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Resource: notes (knowledge); applied/healthy/protected/verified=unknown") || strings.Contains(out.String(), "unproven-revision") {
		t.Fatalf("unknown facts suggest revision evidence: %s", out.String())
	}
	after, err := json.Marshal(view)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("human rendering changed JSON facts")
	}
	view.Resources[0].Applied.State = pc.DeclarationSatisfied
	view.Resources[0].Applied.Revision = "actual-installation"
	out.Reset()
	if err := renderProjectContext(cmd, &options{}, view); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "applied=satisfied@actual-installation; healthy=unknown; protected=unknown; verified=unknown") || strings.Contains(out.String(), "unknown@") {
		t.Fatalf("mixed state lost its independent evidence: %s", out.String())
	}
}

func TestProjectContextResourcesBoundedBeforeHistoryAndProse(t *testing.T) {
	status := webDAVContextFixture()
	status.Development.Purpose = strings.Repeat("p", 4096)
	status.Development.Progress = strings.Repeat("<", 4096)
	status.Operation.OperationID = strings.Repeat("h", 4096)
	for i := 0; i < 100; i++ {
		status.Resources[pc.ResourceKey(fmt.Sprintf("z%03d", i))] = pc.DeclarationResourceStatus{Binding: pc.DeclarationBinding{Kind: pc.DeclarationKnowledge}}
	}
	view := newProjectContext(status)
	if len(view.Resources)+view.Omitted["resources"] != len(status.Resources) || view.Omitted["resources"] == 0 || len(view.Repositories) != 1 || view.Target == nil {
		t.Fatalf("omission/navigation: %+v", view)
	}
	for _, jsonMode := range []bool{false, true} {
		var out bytes.Buffer
		cmd := &cobra.Command{}
		cmd.SetOut(&out)
		if err := renderProjectContext(cmd, &options{jsonOutput: jsonMode}, view); err != nil {
			t.Fatal(err)
		}
		if jsonMode {
			decodeContext(t, out.String())
		} else {
			if out.Len() > 4096 || strings.Count(out.String(), "\n") > 16 || !strings.Contains(out.String(), "Root: /owner/quote's project") || !strings.Contains(out.String(), "path /owner/quote's project/src") || !strings.Contains(out.String(), "Resource: webdav") || !strings.Contains(out.String(), "detail: loom project status 'verified-project' --node 'verified-owner' --json") {
				t.Fatalf("navigation/resource/detail bounds: %s", out.String())
			}
		}
	}
	status = webDAVContextFixture()
	r := status.Resources["webdav"]
	r.Readiness.Protected.EvidenceRef = "unsafe\nINJECTED"
	r.Readiness.Endpoint.URL = "unsafe\x1b[31m"
	status.Resources["webdav"] = r
	status.ObservedAt = strings.Repeat("<", 4096)
	view = newProjectContext(status)
	for _, r := range view.Resources {
		if r.Key == "webdav" && (r.Protected.State != pc.DeclarationUnknown || r.Endpoint != nil || r.Protected.ReadAt != "") {
			t.Fatalf("unsafe fact/time: %+v", r)
		}
	}
	if view.Omitted["resource_detail"] == 0 || view.Omitted["publication"] == 0 || view.Omitted["read_time"] == 0 {
		t.Fatalf("unsafe omission: %+v", view.Omitted)
	}
}

func contextFixture() pc.DeclarationStatus {
	return pc.DeclarationStatus{
		SchemaVersion: pc.DeclarationStatusSchemaV05,
		Target:        pc.DeclarationTarget{ProjectID: "verified-project", OwnerNodeID: "verified-owner", ProjectRoot: "/owner/quote's project"},
		Revision:      "current-revision",
		Readiness: pc.DeclarationReadiness{
			Desired:    pc.DeclarationFact{State: pc.DeclarationSatisfied, Revision: "current-revision"},
			Queued:     pc.DeclarationFact{State: pc.DeclarationPending},
			Applied:    pc.DeclarationFact{State: pc.DeclarationFailed},
			Processing: pc.DeclarationFact{State: pc.DeclarationNotApplicable},
			Healthy:    pc.DeclarationFact{State: pc.DeclarationUnknown},
			Protected:  pc.DeclarationFact{State: pc.DeclarationUnknown},
			Verified:   pc.DeclarationFact{State: pc.DeclarationUnknown},
		},
		Operation: &pc.DeclarationResult{OperationID: "old-operation", PlanID: "old-plan", Revision: "old-revision", State: pc.DeclarationOperationSucceeded, Readiness: pc.DeclarationReadiness{Healthy: pc.DeclarationFact{State: pc.DeclarationSatisfied}}},
	}
}

func decodeContext(t *testing.T, out string) map[string]any {
	t.Helper()
	if len(out) > 8192 {
		t.Fatalf("JSON budget: %d", len(out))
	}
	d := json.NewDecoder(strings.NewReader(out))
	var got map[string]any
	if err := d.Decode(&got); err != nil {
		t.Fatalf("JSON: %v: %s", err, out)
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		t.Fatal("second JSON document")
	}
	if got["schema_version"] != "loom.project.context.v1" {
		t.Fatalf("schema: %v", got)
	}
	return got
}

func TestProjectContextBoundedOwnerAndReadiness(t *testing.T) {
	status := contextFixture()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		response.WriteJSON(w, 200, response.Success("context", status))
	}))
	defer server.Close()
	cfg := writeProjectBackendInstallManifest(t, server.URL)
	out, stderr, err := executeRootCommand("--config", cfg, "--json", "project", "context", "caller-selector", "--node", "selected-owner")
	if err != nil {
		t.Fatalf("%v %s", err, stderr)
	}
	got := decodeContext(t, out)
	if got["revision"] != status.Revision || got["target"].(map[string]any)["owner_node_id"] != status.Target.OwnerNodeID {
		t.Fatalf("wrong authority: %s", out)
	}
	var readiness pc.DeclarationReadiness
	raw, _ := json.Marshal(got["readiness"])
	_ = json.Unmarshal(raw, &readiness)
	if !reflect.DeepEqual(readiness, status.Readiness) {
		t.Fatalf("historical state projected: %+v", readiness)
	}
	if got["historical_operation"].(map[string]any)["revision"] != "old-revision" {
		t.Fatal("history not separate")
	}
	if got["source"].(map[string]any)["ref"] != "/owner/quote's project/.loom/project.yaml" {
		t.Fatalf("source guessed: %s", out)
	}
	out, stderr, err = executeRootCommand("--config", cfg, "project", "context", "caller-selector")
	if err != nil || len(out) > 4096 || strings.Count(out, "\n") > 16 || !strings.Contains(out, "Historical operation:") || !strings.Contains(out, "Healthy: unknown") {
		t.Fatalf("human: %v %s %s", err, out, stderr)
	}
	for _, name := range []string{"Desired:", "Queued:", "Applied:", "Processing:", "Healthy:", "Protected:", "Verified:"} {
		if !strings.Contains(out, name) {
			t.Fatalf("missing %s", name)
		}
	}
}

func TestProjectContextDoesNotAdvanceOrSearch(t *testing.T) {
	for _, selector := range []string{"/owner/quote's project", "/owner/new\nline", "."} {
		t.Run(selector, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Method != "GET" || r.URL.Path != "/v1/projects/"+selector+"/declaration-status" || r.URL.Query().Get("node_ref") != "explicit-owner" {
					t.Errorf("unexpected read/mutation: %s %s", r.Method, r.RequestURI)
				}
				response.WriteJSON(w, 200, response.Success("read", contextFixture()))
			}))
			defer server.Close()
			cfg := writeProjectBackendInstallManifest(t, server.URL)
			_, stderr, err := executeRootCommand("--config", cfg, "project", "context", selector, "--node", "explicit-owner")
			if err != nil || calls != 1 {
				t.Fatalf("reads=%d err=%v %s", calls, err, stderr)
			}
		})
	}
	for _, code := range []pc.DeclarationErrorCode{pc.DeclarationTargetUnavailable, pc.DeclarationUnsupported} {
		t.Run(string(code), func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				response.WriteJSON(w, 422, struct {
					response.ErrorEnvelope
					Detail pc.DeclarationError `json:"declaration_error"`
				}{response.ErrorEnvelope{Error: response.ErrorBody{Code: string(code), Summary: "owner refused"}}, pc.DeclarationError{Code: code, CauseCode: "remote_source_owner_required"}})
			}))
			defer server.Close()
			cfg := writeProjectBackendInstallManifest(t, server.URL)
			out, _, err := executeRootCommand("--config", cfg, "--json", "project", "context", "/remote", "--node", "wrong-owner")
			if err == nil || calls != 1 || !strings.Contains(out, "remote_source_owner_required") || strings.Contains(out, "loom.project.context.v1") {
				t.Fatalf("refusal lost: %v %s", err, out)
			}
		})
	}
}

func TestProjectContextArchivedDoesNotPlanOrPointToRetiredSource(t *testing.T) {
	status := contextFixture()
	status.Lifecycle, status.Revision = "archived", ""
	status.Development = &pc.DeclarationDevelopmentContext{Posture: "partial", SourceDigest: "retained-digest", Archive: &pc.DeclarationArchivedContext{ReadOnlyRoot: "/retained/project", OperationID: "archive-operation", ManifestDigest: "manifest-digest", ArchivedAt: "2026-10-03T00:00:00Z", MissingDocuments: []string{".project/STATE.md"}}}
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != "GET" {
			t.Fatal("archived context attempted planning")
		}
		response.WriteJSON(w, 200, response.Success("archive", status))
	}))
	defer server.Close()
	cfg := writeProjectBackendInstallManifest(t, server.URL)
	out, stderr, err := executeRootCommand("--config", cfg, "--json", "project", "context", "archived", "--verbose")
	if err != nil || calls != 1 {
		t.Fatalf("historical read: %v calls=%d %s", err, calls, stderr)
	}
	got := decodeContext(t, out)
	if got["lifecycle"] != "archived" || got["source"] != nil || got["revision"] != nil || got["source_status"] != "verified_historical" || got["next_action"].(map[string]any)["kind"] != "historical" {
		t.Fatalf("retired source or mutation advice: %s", out)
	}
	if got["development"].(map[string]any)["archive"].(map[string]any)["read_only_root"] != "/retained/project" {
		t.Fatalf("retained root missing: %s", out)
	}
}

func TestProjectContextArchiveRootBudget(t *testing.T) {
	status := contextFixture()
	status.Lifecycle = "archived"
	status.Development = &pc.DeclarationDevelopmentContext{Posture: "archived", Archive: &pc.DeclarationArchivedContext{OperationID: "archive", ReadOnlyRoot: "/retained/project"}}
	view := newProjectContext(status)
	var out bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&out)
	if err := renderProjectContext(cmd, &options{}, view); err != nil || !strings.Contains(out.String(), "Retained root (read-only; verified custody): /retained/project") || !strings.Contains(out.String(), "Root (historical registration):") {
		t.Fatalf("navigation: %s %v", out.String(), err)
	}
	status.Development.Archive.ReadOnlyRoot = "/" + strings.Repeat("a", 4097)
	view = newProjectContext(status)
	if view.Development.Archive.ReadOnlyRoot != "" || view.Omitted["archive_root"] != 1 || !strings.Contains(view.DetailCommand, "loom project archive inspect") || view.Source != nil {
		t.Fatalf("unsafe/budget root not qualified: %+v", view)
	}
}

func TestProjectContextVerboseSources(t *testing.T) {
	for _, mode := range []string{"matched", "revision_drift", "owner_drift", "root_drift", "unavailable", "oversized", "unsafe"} {
		t.Run(mode, func(t *testing.T) {
			status := contextFixture()
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if calls == 1 {
					if r.Method != "GET" {
						t.Error("default read changed")
					}
					response.WriteJSON(w, 200, response.Success("status", status))
					return
				}
				if calls != 2 || r.Method != "POST" || r.URL.Path != "/v1/project-declaration-plans" {
					t.Errorf("fanout: %s %s", r.Method, r.RequestURI)
				}
				var input pc.DeclarationPlanRequest
				_ = json.NewDecoder(r.Body).Decode(&input)
				if input.ProjectRef != status.Target.ProjectID || input.NodeRef != status.Target.OwnerNodeID || !reflect.DeepEqual(input.Effects, []pc.DeclarationEffect{pc.DeclarationReconcile}) {
					t.Errorf("unverified verbose selectors: %+v", input)
				}
				if mode == "unavailable" {
					response.WriteJSON(w, 503, response.ErrorEnvelope{Error: response.ErrorBody{Code: "declaration.target_unavailable", Summary: "unreadable"}})
					return
				}
				plan := pc.DeclarationPlan{SchemaVersion: pc.DeclarationPlanSchemaV05, PlanID: status.Revision}
				plan.Basis.Target = status.Target
				plan.Basis.Sources = []pc.DeclarationSource{{Ref: "exact-source", Hash: "source-hash", Revision: "source-revision"}}
				switch mode {
				case "oversized":
					for i := 0; i < 1000; i++ {
						plan.Basis.Sources = append(plan.Basis.Sources, pc.DeclarationSource{Ref: "large-source", Hash: strings.Repeat("<", 512)})
					}
				case "unsafe":
					plan.Basis.Sources[0].Ref = "unsafe\nsource"
				case "revision_drift":
					plan.PlanID = "changed"
				case "owner_drift":
					plan.Basis.Target.OwnerNodeID = "other"
				case "root_drift":
					plan.Basis.Target.ProjectRoot = "/other"
				}
				response.WriteJSON(w, 200, response.Success("plan", plan))
			}))
			defer server.Close()
			cfg := writeProjectBackendInstallManifest(t, server.URL)
			out, stderr, err := executeRootCommand("--config", cfg, "--json", "project", "context", "caller", "--verbose")
			if err != nil || calls != 2 {
				t.Fatalf("%v calls=%d %s", err, calls, stderr)
			}
			got := decodeContext(t, out)
			want := mode
			if strings.HasSuffix(mode, "_drift") {
				want = "drift"
			}
			if mode == "oversized" || mode == "unsafe" {
				want = "matched"
				if len(got["omitted"].(map[string]any)) == 0 {
					t.Fatal("source omissions not reported")
				}
			}
			if got["source_status"] != want || strings.Contains(out, "exact-source") != (mode == "matched" || mode == "oversized") {
				t.Fatalf("mixed revisions: %s", out)
			}
		})
	}
}

func TestProjectContextOversizedAndUnsafe(t *testing.T) {
	for _, field := range []string{"collections", "identity", "control"} {
		t.Run(field, func(t *testing.T) {
			status := contextFixture()
			large := strings.Repeat("界", 9000)
			switch field {
			case "collections":
				status.Readiness.Healthy.EvidenceRef = large
				for i := 0; i < 1000; i++ {
					status.Errors = append(status.Errors, pc.DeclarationError{Code: pc.DeclarationOwnerFailed, CauseCode: "owner_pending", Message: "untrusted free text", CompletedEffects: []string{strings.Repeat("e", 1024)}})
				}
			case "identity":
				status.Target.ProjectID = large
				status.Target.ProjectRoot = "/" + large
				status.Revision = large
				status.Operation.OperationID = large
			case "control":
				status.Target.ProjectID = "--help\nINJECTED"
				status.Operation.OperationID = "bad\x1b[31m"
				status.Errors = []pc.DeclarationError{{Code: pc.DeclarationUnauthorized, CauseCode: "bad\nINJECTED"}}
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				response.WriteJSON(w, 200, response.Success("bounded", status))
			}))
			defer server.Close()
			cfg := writeProjectBackendInstallManifest(t, server.URL)
			for _, jsonMode := range []bool{true, false} {
				args := []string{"--config", cfg, "project", "context", "caller"}
				if jsonMode {
					args = append(args, "--json")
				}
				out, stderr, err := executeRootCommand(args...)
				if err != nil {
					t.Fatalf("%v %s", err, stderr)
				}
				if jsonMode {
					got := decodeContext(t, out)
					if len(got["omitted"].(map[string]any)) == 0 {
						t.Fatal("omissions not reported")
					}
				} else if len(out) > 4096 || strings.Count(out, "\n") > 16 || !strings.Contains(out, "Omitted:") {
					t.Fatalf("human budget/omissions: %d %s", len(out), out)
				}
				if strings.Contains(out, "INJECTED") || strings.Contains(out, "\x1b") || strings.Contains(out, large) {
					t.Fatal("unsafe/oversize field displayed")
				}
			}
		})
	}
	for _, args := range [][]string{{"project", "context", "--help"}, {"project", "--help"}} {
		out, _, err := executeRootCommand(args...)
		if err != nil || !strings.Contains(out, "context") {
			t.Fatalf("help: %v %s", err, out)
		}
	}
}

func TestProjectContextAdversarialBudgetsAndVerboseDrift(t *testing.T) {
	for _, size := range []int{0, 128, 700, 1024, 2048, 4096, 4097} {
		for _, value := range []string{strings.Repeat("界", size), strings.Repeat("<&", size)} {
			status := contextFixture()
			status.Target.ProjectRoot = "/" + value
			status.Target.LocationRevision = value
			status.Revision = value
			for _, fact := range declarationFacts(&status.Readiness) {
				fact.Revision = value
				fact.EvidenceRef = value
			}
			status.Errors = []pc.DeclarationError{{Code: pc.DeclarationOwnerFailed, CauseCode: "owner_pending", CompletedEffects: []string{value}}}
			view := newProjectContext(status)
			view.SourceStatus = "drift"
			before, _ := json.Marshal(view)
			var out bytes.Buffer
			cmd := &cobra.Command{}
			cmd.SetOut(&out)
			if err := renderProjectContext(cmd, &options{}, view); err != nil {
				t.Fatal(err)
			}
			if len(out.String()) > 4096 || strings.Count(out.String(), "\n") > 16 || !strings.Contains(out.String(), "Source detail: drift") {
				t.Fatalf("human budget/drift size=%d: %d %s", size, out.Len(), out.String())
			}
			for _, name := range declarationFactNames {
				if !strings.Contains(out.String(), name+":") {
					t.Fatalf("readiness dropped: %s", out.String())
				}
			}
			after, _ := json.Marshal(view)
			if !bytes.Equal(before, after) {
				t.Fatal("human rendering mutated projection")
			}
			out.Reset()
			if err := renderProjectContext(cmd, &options{jsonOutput: true}, view); err != nil {
				t.Fatal(err)
			}
			decodeContext(t, out.String())
		}
	}
}
