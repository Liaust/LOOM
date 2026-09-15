package httpapi

import (
	"database/sql"
	"encoding/json"
	"gopkg.in/yaml.v3"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"loom.local/loom/internal/communication"
	"loom.local/loom/internal/config"
	"loom.local/loom/internal/projectapply"
	pc "loom.local/loom/internal/projectcontracts"
	"loom.local/loom/internal/projects"
	"loom.local/loom/internal/projectwatch"
	dx "loom.local/loom/tests/acceptance/project_dx"
)

func TestProjectDXProjectsFixture(t *testing.T) {
	f := dx.Open(t, "B")
	db := declarationHTTPDatabase(t)
	local := projectapply.NewLocalResolver(db, func() (config.Config, error) { return config.Config{NodeID: "main", BoxPath: f.Participant}, nil })
	watch := projectapply.WatchOwner{Resolver: local, Reconciler: projectwatch.NewDeclarationReconciler(db)}
	svc := projectapply.NewService(db, local, projectapply.NewCurrentAuthority(db), map[pc.DeclarationOwner]projectapply.Owner{pc.DeclarationOwnerProjects: projectapply.ProjectsOwner{Resolver: local, Projects: projects.NewService(db)}, pc.DeclarationOwnerKnowledge: watch, pc.DeclarationOwnerProtection: watch})
	h := NewServer(Services{DB: db, ProjectDeclaration: svc, Projects: projects.NewService(db), Communication: communication.NewService(db), RuntimeConfig: config.Config{NodeID: "main", BoxPath: f.Participant}}, slog.New(slog.NewTextHandler(io.Discard, nil))).Handler()
	var observedMu sync.Mutex
	plans := map[string][]byte{}
	var lastStatus []byte
	applyRequests := 0
	f.Serve(t, h, func(r *http.Request, body []byte) {
		observedMu.Lock()
		defer observedMu.Unlock()
		if r.Method == "POST" && r.URL.Path == "/v1/project-declaration-operations" {
			applyRequests++
		}
		var envelope struct {
			OK   bool            `json:"ok"`
			Data json.RawMessage `json:"data"`
		}
		if json.Unmarshal(body, &envelope) != nil || !envelope.OK {
			return
		}
		if r.Method == "POST" && r.URL.Path == "/v1/project-declaration-plans" {
			var plan pc.DeclarationPlan
			if json.Unmarshal(envelope.Data, &plan) == nil && plan.PlanID != "" {
				plans[plan.PlanID] = append([]byte(nil), envelope.Data...)
			}
		}
		if r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/declaration-status") {
			lastStatus = append([]byte(nil), envelope.Data...)
		}
	})
	assertFinal := func(body, status []byte) {
		var plan pc.DeclarationPlan
		if e := json.Unmarshal(body, &plan); e != nil || plan.PlanID == "" {
			t.Fatal("final oracle requires the participant’s actual plan")
		}
		var e error
		var observed pc.DeclarationStatus
		if e = json.Unmarshal(status, &observed); e != nil {
			t.Fatal(e)
		}
		if observed.Target != plan.Basis.Target || observed.Operation == nil || observed.Operation.PlanID != plan.PlanID || observed.Operation.State != pc.DeclarationOperationPartial || len(observed.Operation.Actions) != 2 || observed.Operation.Actions[0].State != pc.DeclarationOperationSucceeded || observed.Operation.Actions[1].Error == nil || observed.Operation.Actions[1].Error.CauseCode != "owner_pending" {
			t.Fatalf("real registration/watch pending oracle: %s", status)
		}
		resource, ok := observed.Resources["journal"]
		if !ok || resource.Binding.Kind != "knowledge" || resource.Readiness.Healthy.State == pc.DeclarationSatisfied || resource.Readiness.Protected.State == pc.DeclarationSatisfied {
			t.Fatalf("unexpected journal readiness: %s", status)
		}
		var deliveries int
		if e = db.QueryRow(`SELECT count(*) FROM projects.declaration_watch_deliveries WHERE operation_id=$1`, observed.Operation.OperationID).Scan(&deliveries); e != nil || deliveries != 1 {
			t.Fatalf("one authoritative watch delivery: %d %v", deliveries, e)
		}

		f.Write(t, "oracle.json", map[string]any{"status": json.RawMessage(status), "plan": json.RawMessage(body), "phase1_zero_enrollment": true})
	}
	initial := dx.Snapshot(t, db)
	phase1 := func() {
		after := dx.Snapshot(t, db)
		if after != initial {
			t.Fatal("local create/edit enrolled or otherwise mutated owner DB")
		}
		f.Write(t, "phase1.json", map[string]any{"db_before": initial, "db_after": after, "zero_enrollment": true})
	}
	if os.Getenv("LOOM_DX_SERVE") == "1" {
		f.Hold(t, map[string]func(){"phase1": phase1, "final": func() {
			observedMu.Lock()
			defer observedMu.Unlock()
			var status pc.DeclarationStatus
			if json.Unmarshal(lastStatus, &status) != nil || status.Operation == nil || applyRequests != 1 {
				t.Fatal("final oracle requires one actual apply and participant status observation")
			}
			root := filepath.Join(f.Participant, "field-notebook")
			journal, err := os.ReadFile(filepath.Join(root, "journal", "entry.md"))
			if err != nil || strings.TrimSpace(string(journal)) != "First field observation." {
				t.Fatal("participant journal content differs from frozen task")
			}
			if _, err = os.Stat(filepath.Join(f.Evidence, "phase1.done.json")); err != nil {
				t.Fatal("final oracle requires completed evaluator phase1 control")
			}
			assertFinal(plans[status.Operation.PlanID], lastStatus)
		}})
		return
	}
	f.MustCommand(t, "project", "create", "Field Notebook", "--directory", f.Participant, "--owner-node", "main")
	root := filepath.Join(f.Participant, "field-notebook")
	if e := os.MkdirAll(filepath.Join(root, "journal"), 0700); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(filepath.Join(root, "journal", "entry.md"), []byte("First field observation.\n"), 0600); e != nil {
		t.Fatal(e)
	}
	for _, name := range []string{"notes", "repos", ".loom"} {
		if _, e := os.Stat(filepath.Join(root, name)); e != nil {
			t.Fatal(e)
		}
	}
	phase1()
	file := filepath.Join(root, pc.CanonicalRootContractPath)
	raw, e := os.ReadFile(file)
	if e != nil {
		t.Fatal(e)
	}
	var declaration map[string]any
	if e = yaml.Unmarshal(raw, &declaration); e != nil {
		t.Fatal(e)
	}
	resources, _ := declaration["resources"].(map[string]any)
	if len(resources) != 0 {
		t.Fatal("minimal source has declared resources")
	}
	declaration["resources"] = map[string]any{"journal": map[string]any{"kind": "knowledge", "knowledge": map[string]any{"path": "journal", "category": "notes"}}}
	raw, e = yaml.Marshal(declaration)
	if e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(file, raw, 0600); e != nil {
		t.Fatal(e)
	}
	body := f.MustCommand(t, "project", "plan", root, "--node", "main")
	var plan pc.DeclarationPlan
	if e = json.Unmarshal(body, &plan); e != nil || plan.PlanID == "" {
		t.Fatalf("no typed real plan: %s %v", body, e)
	}
	for _, action := range plan.Basis.Actions {
		if action.Authorization.ApprovalRequired {
			t.Fatal("fixture prerequisite: typed approval owner required")
		}
	}
	args := []string{"project", "apply", root, "--node", "main", "--plan-id", plan.PlanID, "--idempotency-key", "dx-fixture-b"}

	f.MustCommand(t, args...)
	status := f.MustCommand(t, "project", "status", root, "--node", "main")
	assertFinal(body, status)

	t.Log("PASS real local create, zero phase-1 enrollment, real ProjectsOwner/WatchOwner plan/apply/status")
}

// C's seeder is in knowledge's test package. This helper owns only the real
// public HTTP surface; its DB URL and barrier are evaluator-private files.
func TestProjectDXNotesHTTPHelper(t *testing.T) {
	if os.Getenv("LOOM_DX_NOTES_HTTP") != "1" {
		t.Skip("launched only by guarded Notes fixture")
	}
	f := dx.Open(t, "C")
	raw := os.Getenv("LOOM_DX_NOTES_DB")
	u, e := url.Parse(raw)
	if e != nil || u.Host != "" || u.Query().Get("host") != filepath.Join(f.Root, "socket") || !strings.HasPrefix(u.Path, "/box_sources_") {
		t.Fatal("invalid owned Notes DB")
	}
	db, e := sql.Open("pgx", raw)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	hook := func(r *http.Request, b []byte) {
		if !strings.HasSuffix(r.URL.Path, "/search") {
			return
		}
		if _, e := os.Stat(filepath.Join(f.Root, "C.barrier.done")); e == nil {
			return
		}
		if e := os.WriteFile(filepath.Join(f.Root, "C.query"), b, 0600); e != nil {
			panic(e)
		}
		deadline := time.Now().Add(30 * time.Second)
		for time.Now().Before(deadline) {
			if _, e := os.Stat(filepath.Join(f.Root, "C.barrier.done")); e == nil {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		panic("Notes barrier timed out")
	}
	f.Serve(t, NewServer(Services{DB: db}, slog.New(slog.NewTextHandler(io.Discard, nil))).Handler(), hook)
	f.Hold(t, nil)
}
