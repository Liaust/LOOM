package agentpack

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Current task scenarios retain the original boundaries, not retired wording.
// These check instructions, not an agent's decisions or runtime enforcement.
func TestProjectProtocolTaskCases(t *testing.T) {
	cases := []struct {
		name, file, section string
		want, reject        []string
	}{
		{"small_edit", "templates/mina/protocols/PROJECT-DELEGATION.md", "Ordinary edits", []string{"bounded outcome", "appropriate tests", "concise handoff", "actual repository instructions", "No new planning files or formal file inventories", "unless required there", "absent optional `.repo/` needs no installation", "Context reads do not enroll, refresh projections or accept claims"}, []string{"exact accepted base", "expected files, forbidden", "before every edit"}},
		{"planned_feature", "templates/mina/protocols/PROJECT-DELEGATION.md", "Planned or high-impact work", []string{"accepted plan/base", "validation", "stop conditions", "coordinate actual shared owners", "never an immutable deployed release"}, nil},
		{"harness_not_vendor", "templates/mina/protocols/PROJECT-DELEGATION.md", "Harness and authority", []string{"native Hermes delegation or independent Codex Desktop/direct CLI", "one lifecycle owner", "other clients open its existing path", "Do not give the worker MINA's SOUL", "credentials, permissions or operational authority", "Delegation changes neither identity nor publication, contact, merge, deployment or account authority"}, []string{"opened by ORCA"}},
		{"ordinary_source_work", "skills/manage-loom-projects/SKILL.md", "Ordinary edits", []string{"Follow `AGENTS.md`, read files directly, test and hand off", "Source work needs no enrollment, activation or semantic acceptance", "Git and `.repo/` are optional", "Use known commands", "optionally reads context/status"}, []string{"Inspect, Plan, Apply, Verify", "before every edit"}},
		{"explicit_management", "skills/manage-loom-projects/SKILL.md", "When asking LOOM to manage a resource", []string{"canonical source", "selects backend creation/registration", "`owner_node` does not route commands", "Registration isn't activation/health", "refreshes never accept semantic context", "Saving source does not enroll it"}, nil},
		{"node_scope", "skills/operate-loom-nodes/SKILL.md", "Bounded diagnosis", []string{"ordinary source edit needs no node preflight", "relevant", "original", "authorization"}, []string{"health from broad to narrow"}},
		{"coordination_current_task", "skills/orchestrate-loom-work/SKILL.md", "Work In The Current Task", []string{"Continue in the current task", "Worktrees are optional", "Create a separate chat only when the operator requests one"}, []string{"belongs to a worker", "delegate narrow work through", "Morathustra"}},
		{"coordination_native_harness", "skills/orchestrate-loom-work/SKILL.md", "Harness And Worktree Ownership", []string{"Codex owns its threads and worktrees", "Hermes owns its native agent sessions and routines", "one lifecycle owner", "LOOM adds no worktree or chat registry", "ORCA and its legacy skills are retired, not an availability fallback", "preserve device and permission boundaries"}, []string{"Hermes is the harness", "1.4.183", "remain deferred"}},
		{"coordination_proportionate_handoff", "skills/orchestrate-loom-work/references/routing.md", "Proportionate Handoffs", []string{"A small delegated edit", "No new planning files", "task-relevant context", "does not initialise MINA"}, []string{"expected and forbidden files", "private memory as task truth"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			body := projectProtocolSection(t, projectProtocolRead(t, c.file), c.section)
			for _, s := range c.want {
				if !strings.Contains(body, s) {
					t.Errorf("%s lacks %q", c.file, s)
				}
			}
			for _, s := range c.reject {
				if strings.Contains(body, s) {
					t.Errorf("%s reintroduced %q", c.file, s)
				}
			}
		})
	}
}

func TestProjectProtocolCurrentCommands(t *testing.T) {
	files := []string{"templates/mina/protocols/PROJECT-DELEGATION.md", "skills/manage-loom-projects/SKILL.md", "skills/manage-loom-projects/references/project-commands.md", "skills/operate-loom-nodes/SKILL.md", "skills/manage-loom-projects/references/project-lifecycle.md", "skills/manage-loom-projects/references/managed-applications.md", "skills/manage-loom-projects/references/managed-application-recipes.md"}
	// Catch indented fences and inline recipes as well as commands at column zero.
	future := regexp.MustCompile(`\bloom\s+(?:project\s+(?:declare|application)|application)\b`)
	refreshRecipe := regexp.MustCompile("(?m)\\bloom\\s+project\\s+(?:plan|apply)\\b[^\\n`]*--refresh-projections\\b")
	for _, file := range files {
		t.Run(file, func(t *testing.T) {
			raw := projectProtocolRead(t, file)
			if future.MatchString(raw) || refreshRecipe.MatchString(raw) {
				t.Error("unshipped declare/application/refresh command recipe")
			}
			body := strings.Join(strings.Fields(raw), " ")
			for _, stale := range []string{"main/headless, browser/mobile/Relay, restart, and Mac-off behavior remain deferred", "Main/headless deployment,", "Orca `1.4.183` has a completed"} {
				if strings.Contains(body, stale) {
					t.Errorf("stale readiness: %q", stale)
				}
			}
		})
	}
	commands := strings.Join(strings.Fields(projectProtocolRead(t, files[2])), " ")
	for _, term := range []string{"loom project create", "loom project plan", "loom project apply", "loom project status", "loom project operation", "--plan-id <reviewed-plan-id>", "--idempotency-key <stable-key>", "parsed `--refresh-projections` flag is rejected", "Prepared application installation uses the same plan/apply/status route"} {
		if !strings.Contains(commands, term) {
			t.Errorf("verified command boundary lacks %q", term)
		}
	}
	for _, term := range []string{"automatically connects", "source_created/context_pending", "--register` is backend-only", "has no general dry-run", "public HTTPS beneath `apps.example.com`", "configured backend"} {
		if !strings.Contains(commands, term) {
			t.Errorf("command boundary lacks %q", term)
		}
	}
	node := strings.Join(strings.Fields(projectProtocolRead(t, "skills/operate-loom-nodes/SKILL.md")), " ")
	for _, term := range []string{"ORCA and its legacy skills are retired", "Historical non-Git registration evidence", "not installed patch availability or current health", "actual native harness"} {
		if !strings.Contains(node, term) {
			t.Errorf("native node guidance lost %q", term)
		}
	}
}

// These source assertions extend the frozen scenarios; G owns behavioral acceptance.
func TestVerifiedProjectGuidanceRoutes(t *testing.T) {
	for _, c := range []struct {
		file string
		want []string
	}{
		{"skills/manage-loom-projects/SKILL.md", []string{"`loom project context <ref> --node <node>` optionally reads context/status", "Source work needs no enrollment, activation or semantic acceptance", "Saving source does not enroll it", "Registration isn't activation/health", "Public HTTPS beneath `apps.example.com` is part of that same workflow", "Project metadata refreshes automatically; provenance sync is diagnostic"}},
		{"skills/manage-loom-projects/references/managed-applications.md", []string{"artifact_descriptor", "backup: cloud_history", "pass://SHARE_ID/ITEM_ID/FIELD", "the operator creates or registers", "Automatic generation is deferred", "exposure: public_https", "hostname: webdav.apps.example.com", "127.0.0.1", "DNS, trusted TLS, route", "another agent or integrator is not a prerequisite", "No mandatory all-recipes read before ordinary work"}},
		{"skills/manage-loom-projects/references/project-commands.md", []string{"knowledge: {path: journal, category: notes}", "Saving source does not enroll it", "selected owner node", "repeatable `--approval`", "Historical operation inspection does not resume it", "preserving original selectors, effects, plan, key and approval references", "Client sidebar membership is separate from LOOM registration", "no ORCA registration", "Do not work around absent composition with manual owner calls"}},
		{"skills/operate-loom-nodes/SKILL.md", []string{"missing reported backup root is not data-loss evidence", "read-only next step", "does not justify backup or repair", "not installed patch availability"}},
		{"skills/manage-loom-projects/references/project-lifecycle.md", []string{"Passive is not pause", "do not use it merely to classify", "not permission for SQL or watch-owner bypasses", "reactivate <project> --restore-operation <restore-operation-id>", "does not start watchers", "Deliberately paused or completed schedules stay stopped", "never re-include `.loom/state/` or `.loom/tmp/`", "reject escaping paths/symlinks"}},
		{"skills/manage-loom-projects/references/managed-application-recipes.md", []string{"failed receipt", "Do not release unknown/running work", "copy its main file without its WAL", "not a domain allowlist", "does not repeat consent", "No lock rewriting", "hostname collision leaves the other app unchanged", "old hostname until the replacement answers through trusted HTTPS", "does not preserve the original client IP"}},
	} {
		t.Run(c.file, func(t *testing.T) {
			raw := strings.Join(strings.Fields(projectProtocolRead(t, c.file)), " ")
			for _, term := range c.want {
				if !strings.Contains(raw, term) {
					t.Errorf("verified route lacks %q", term)
				}
			}
			for _, stale := range []string{"workflow remains future work at", "CLI still needs separate parity proof", "folder project is not proven", "Public endpoint provisioning remains separate", "Public endpoint provisioning and the declaration", "loopback-only"} {
				if strings.Contains(raw, stale) {
					t.Errorf("obsolete route: %q", stale)
				}
			}
		})
	}
}

func TestProjectProtocolContextBudget(t *testing.T) {
	for _, c := range []struct {
		file         string
		words, bytes int
	}{
		{"templates/mina/protocols/PROJECT-DELEGATION.md", 300, 2400},
		{"skills/manage-loom-projects/SKILL.md", 600, 4600},
		{"skills/manage-loom-projects/references/project-commands.md", 500, 4000},
		{"skills/manage-loom-projects/references/project-lifecycle.md", 500, 4000},
		// Deployment-only reference now includes the shipped public endpoint flow.
		{"skills/manage-loom-projects/references/managed-applications.md", 550, 4000},
		{"skills/operate-loom-nodes/SKILL.md", 480, 3700},
	} {
		t.Run(c.file, func(t *testing.T) {
			body := projectProtocolRead(t, c.file)
			words := len(strings.Fields(body))
			t.Logf("words=%d UTF-8 bytes=%d budget=%d/%d", words, len(body), c.words, c.bytes)
			if words > c.words || len(body) > c.bytes {
				t.Errorf("context budget exceeded")
			}
		})
	}
}

func TestSelectiveProvenanceCaptureIsInEntryInstructions(t *testing.T) {
	for _, file := range []string{
		"host-instructions/codex/AGENTS.md", "templates/mina/AGENTS.md",
		"templates/mina/WORKFLOW.md", "templates/project/AGENTS.md",
	} {
		t.Run(file, func(t *testing.T) {
			body := strings.ToLower(strings.Join(strings.Fields(projectProtocolRead(t, file)), " "))
			for _, required := range []string{"source-backed", "use-loom-provenance", "loom provenance candidate register", "producer", "routine"} {
				if !strings.Contains(body, required) {
					t.Errorf("capture entry lacks %q", required)
				}
			}
		})
	}
	protocol := projectProtocolRead(t, "templates/mina/protocols/PROVENANCE-RECONCILIATION.md")
	for _, stale := range []string{"Until the verified cutover receipt", "retained Hermes schedule is", "Prefer `loom provenance candidate list"} {
		if strings.Contains(protocol, stale) {
			t.Errorf("current protocol retains stale ownership/review routing %q", stale)
		}
	}
}

func TestInstalledSchedulingAndArchiveGuidance(t *testing.T) {
	schedules := strings.Join(strings.Fields(projectProtocolRead(t, "skills/manage-loom-projects/references/project-schedules.md")), " ")
	for _, term := range []string{"kind: hermes_schedule", "profile: mina", "status: paused", "loom schedules hermes --json", "without a fabricated project", "Reapply preserves", "target: codex", "operator-configured weekly Provenance routine uses Hermes as its sole clock", "supported conditional LOOM/Codex executor", "An empty eligible queue launches no agent", "no live job ID, timezone, activation receipt or standing reconciliation authority", "Preserve existing pauses and retired jobs"} {
		if !strings.Contains(schedules, term) {
			t.Errorf("installed scheduling route lacks %q", term)
		}
	}
	if strings.Contains(schedules, "remains a separate integration") || strings.Contains(schedules, "remains with its current LOOM/Codex owner until the explicit handover") {
		t.Fatal("installed Hermes adapter described as future work")
	}
	storage := projectProtocolRead(t, "skills/operate-loom-storage/SKILL.md")
	for _, term := range []string{"readable custody", "Main has no Box Dropzone or Lane", "loom project archive plan", "--plan-digest", "leaves a project inactive", "export is optional"} {
		if !strings.Contains(storage, term) {
			t.Errorf("archive guidance lacks %q", term)
		}
	}
}

func projectProtocolRead(t *testing.T, file string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("../../ai-loom-pack", file))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}
func projectProtocolSection(t *testing.T, raw, title string) string {
	t.Helper()
	return strings.Join(strings.Fields(projectProtocolRawSection(t, raw, title)), " ")
}

func projectProtocolRawSection(t *testing.T, raw, title string) string {
	t.Helper()
	_, tail, ok := strings.Cut(raw, "## "+title+"\n")
	if !ok {
		t.Fatalf("missing task route %q", title)
	}
	body, _, _ := strings.Cut(tail, "\n## ")
	return body
}
