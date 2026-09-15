package agentpack

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Frozen F1 source cases. These check instructions, not an agent's decisions or runtime.
func TestProjectProtocolTaskCases(t *testing.T) {
	cases := []struct {
		name, file, section string
		want, reject        []string
	}{
		{"small_edit", "templates/mina/protocols/PROJECT-DELEGATION.md", "Ordinary edits", []string{"bounded outcome", "appropriate tests", "concise handoff", "actual repository instructions", "No new planning files or formal file inventories"}, []string{"exact accepted base", "expected files, forbidden", "before every edit"}},
		{"planned_feature", "templates/mina/protocols/PROJECT-DELEGATION.md", "Planned or high-impact work", []string{"exact accepted base", "accepted plan", "validation", "stop conditions"}, nil},
		{"harness_not_vendor", "templates/mina/protocols/PROJECT-DELEGATION.md", "Harness and authority", []string{"Codex model inside ORCA remains ORCA-owned", "Codex-managed", "native", "Do not give the worker MINA's SOUL", "credentials, permissions or operational authority"}, nil},
		{"ordinary_source_work", "skills/manage-loom-projects/SKILL.md", "Ordinary edits", []string{"actual repository instructions", "appropriate tests", "No LOOM registration, activation, projection refresh or semantic acceptance", "Do not install optional `.repo/`", "known commands"}, []string{"Inspect, Plan, Apply, Verify", "before every edit"}},
		{"explicit_management", "skills/manage-loom-projects/SKILL.md", "When asking LOOM to manage a resource", []string{"canonical source", "configured backend", "owner_node", "does not route", "Registration and activation are separate", "not accepted semantic context"}, nil},
		{"node_scope", "skills/operate-loom-nodes/SKILL.md", "Bounded diagnosis", []string{"ordinary source edit needs no node preflight", "relevant", "original", "authorization"}, []string{"health from broad to narrow"}},
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
	files := []string{"templates/mina/protocols/PROJECT-DELEGATION.md", "skills/manage-loom-projects/SKILL.md", "skills/manage-loom-projects/references/project-commands.md", "skills/operate-loom-nodes/SKILL.md"}
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
	for _, file := range files[3:] {
		if strings.HasSuffix(file, "project-commands.md") {
			continue
		}
		body := strings.Join(strings.Fields(projectProtocolRead(t, file)), " ")
		for _, term := range []string{"1.4.191", "committed", "non-Git", "current health"} {
			if !strings.Contains(body, term) {
				t.Errorf("%s lacks qualified ORCA evidence %q", file, term)
			}
		}
	}
}

// These source assertions extend the frozen scenarios; G owns behavioral acceptance.
func TestVerifiedProjectGuidanceRoutes(t *testing.T) {
	for _, c := range []struct {
		file string
		want []string
	}{
		{"skills/manage-loom-projects/SKILL.md", []string{"Optional `loom project context", "Neither is a prerequisite", "Saving source does not enroll it", "live health", "Public HTTPS beneath `apps.example.com` is part of that same workflow", "automatic metadata refresh"}},
		{"skills/manage-loom-projects/references/managed-applications.md", []string{"artifact_descriptor", "backup: cloud_history", "pass://SHARE_ID/ITEM_ID/FIELD", "the operator creates or registers", "Automatic generation is deferred", "exposure: public_https", "hostname: webdav.apps.example.com", "127.0.0.1", "DNS, trusted TLS, route", "not the LOOM integrator"}},
		{"skills/manage-loom-projects/references/project-commands.md", []string{"knowledge: {path: journal, category: notes}", "Saving source does not enroll it", "selected owner node", "repeatable `--approval`", "Historical operation inspection does not resume it", "preserving original selectors, effects, plan, key and approval references", "orca repo add --path <existing-folder> --kind folder --json", "neither creates missing directories nor validates existence", "Native polling may convert its kind if Git appears", "Do not work around absent composition with manual owner calls"}},
		{"skills/operate-loom-nodes/SKILL.md", []string{"missing reported backup root is not data-loss evidence", "read-only next step", "does not justify backup or repair", "not installed patch availability"}},
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
		// Deployment-only reference now includes the shipped public endpoint flow.
		{"skills/manage-loom-projects/references/managed-applications.md", 550, 4000},
		{"skills/operate-loom-nodes/SKILL.md", 400, 3200},
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
	_, tail, ok := strings.Cut(raw, "## "+title+"\n")
	if !ok {
		t.Fatalf("missing task route %q", title)
	}
	body, _, _ := strings.Cut(tail, "\n## ")
	return strings.Join(strings.Fields(body), " ")
}
