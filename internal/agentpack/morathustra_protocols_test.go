package agentpack

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func morathustraInstructions(t *testing.T) map[string][]byte {
	t.Helper()
	pack := loadRepositoryPack(t)
	template, err := findWorkspaceTemplate(pack, "morathustra")
	if err != nil {
		t.Fatal(err)
	}
	payloads, err := inspectWorkspaceTemplate(pack, template)
	if err != nil {
		t.Fatal(err)
	}
	return payloads
}

// These are source-contract regression checks, not a natural-language security
// sandbox or proof of a live actor. Live authentication remains operator-owned.
func TestMorathustraPersonalityDoesNotReplaceOperatingAuthority(t *testing.T) {
	payloads := morathustraInstructions(t)
	soul := strings.Join(strings.Fields(string(payloads[".hermes/SOUL.md"])), " ")
	for _, required := range []string{
		"intellectually adventurous", "perceptive and independent", "aesthetic sensitivity",
		"quietly mischievous", "candor and warmth", "dependable", "right hand",
		"what evidence would change your mind", "Revise with confidence",
		"Personality never grants authority", "every one remains binding",
		"Mischief belongs in the voice", "never means deception",
	} {
		if !strings.Contains(soul, required) {
			t.Errorf("SOUL lost accepted character/boundary %q", required)
		}
	}
	for _, forbidden := range []string{"Apollo", "--profile", "```", "GH_CONFIG_DIR", "OAuth", "memory/", "You may deploy", "ignore permissions"} {
		if strings.Contains(soul, forbidden) {
			t.Errorf("SOUL contains procedural, borrowed identity or authority content %q", forbidden)
		}
	}
	if regexp.MustCompile(`\b(?:basecamp|gh|git|nix|sudo)\s`).MatchString(soul) {
		t.Error("SOUL contains an operating command")
	}
	for name, payload := range payloads {
		if name != ".hermes/SOUL.md" && (strings.Contains(string(payload), "You are Morathustra") || strings.Contains(string(payload), "Be intellectually adventurous")) {
			t.Errorf("personality duplicated in %s", name)
		}
	}

	// Check portable boundaries, not hashes tied to a private acceptance receipt.
	for name, required := range map[string][]string{
		"OPERATING-POLICY.md":                       {"Require explicit authorization", "Preserve user edits", "Keep values out"},
		"WORKSPACE-MAP.md":                          {"preserved on scaffold replay", "does not initialize Hermes", "does not transfer ownership"},
		"protocols/CREDENTIALS.md":                  {"approved non-secret reference contract", "Missing access stops that operation", "never fall back to another identity"},
		"protocols/EXTERNAL-MESSAGING.md":           {"separately approved platform", "allowlist", "Do not create accounts, incur seats, publish or contact others"},
		"protocols/LOOM-ROUTING.md":                 {"Source documents and repository contracts", "Never read database tables", "ORCA is retired"},
		"protocols/PROJECT-DELEGATION.md":           {"Do not give the worker Morathustra's SOUL", "not an external commitment"},
		"protocols/PROVENANCE-AND-MEMORY.md":        {"Candidates are clues, not facts or permission", "Current sources and explicit instructions remain authoritative"},
		"protocols/SESSION-RETRIEVAL.md":            {"source visibility", "Never query a session database directly", "Do not change retention"},
		"protocols/SKILL-CREATION-AND-PROMOTION.md": {"Never approve your own pending changes", "Category alone does not transfer curator ownership", "no private inventory or live count"},
	} {
		if failures := instructionBoundaryFailures(string(payloads[name]), required, nil); len(failures) != 0 {
			t.Errorf("%s: %v", name, failures)
		}
	}
}

func TestMorathustraNamedBasecampProtocolFailsClosed(t *testing.T) {
	payloads := morathustraInstructions(t)
	content := strings.Join(strings.Fields(string(payloads["protocols/BASECAMP.md"])), " ")
	for _, required := range []string{
		"literal `--profile morathustra`", "Ordinary coding agents retain literal `--profile codex`",
		"loom-morathustra-basecamp --profile morathustra me --agent",
		"loom-morathustra-basecamp --profile morathustra skill --agent",
		"prints the native skill without installing it",
		"normal Hermes skill discovery", "Missing installed skill or tooling stops task use",
		"Missing binding holds account work", "shared agents UID is not per-agent OS isolation",
		"no automatic named-account environment", "never creates or repairs the store",
		"Do not rely on the default profile, set `BASECAMP_PROFILE`, change the shared default",
		"no authenticated identity binding", "after successful OAuth", "Capture and freeze",
		"global `identity.id`", "configured `accounts[].id`", "account-scoped recording actor ID",
		"Do not guess IDs, reuse Codex's IDs", "Require exactly one current account",
		"Missing, incomplete or ambiguous binding means stop", "parse failure means stop",
		"Do not continue to task commands after a failed preflight",
		"Morathustra must never use the operator's, Codex's, Apollo's or any other actor's authentication",
		"never compare it to global `identity.id`", "original creator for the current actor",
		"one bounded authorized read", "Search the exact destination", "approved mapping",
		"first external writes require separate integrator authorization with the operator present",
		"Task capture never authorizes publication, contact", "no credential values",
	} {
		if !strings.Contains(strings.ToLower(content), strings.ToLower(required)) {
			t.Errorf("Basecamp protocol lost fail-closed rule %q", required)
		}
	}
	// Inspect every executable Basecamp example across the named workspace;
	// generic project instructions cannot silently become named-agent examples.
	commands := regexp.MustCompile("(?m)(?:^|`)(?:loom-morathustra-)?basecamp[ \\t]+([^`\\n]+)")
	count := 0
	for name, payload := range payloads {
		for _, match := range commands.FindAllSubmatch(payload, -1) {
			count++
			if command := strings.TrimSpace(string(match[1])); command != "--profile morathustra me --agent" && command != "--profile morathustra skill --agent" {
				t.Errorf("unreviewed Basecamp invocation in %s", name)
			}
		}
	}
	if count != 2 {
		t.Fatalf("expected exact named preflight and read-only embedded-skill examples, found %d", count)
	}
	// Actor IDs are intentionally unbound until the separate live OAuth gate.
	if regexp.MustCompile(`\b[0-9]{6,}\b`).MatchString(content) {
		t.Fatal("portable protocol contains a frozen or borrowed live ID")
	}
}

func TestBasecampSharedOrganizationAndCodexBootstrap(t *testing.T) {
	content := string(morathustraInstructions(t)["protocols/BASECAMP.md"])
	begin := "<!-- BEGIN LOOM SHARED BASECAMP ORGANIZATION -->"
	end := "<!-- END LOOM SHARED BASECAMP ORGANIZATION -->"
	if strings.Count(content, begin) != 1 || strings.Count(content, end) != 1 {
		t.Fatal("shared organization must have exactly one extraction boundary")
	}
	_, rest, _ := strings.Cut(content, begin)
	organization, _, found := strings.Cut(rest, end)
	if !found || len(strings.TrimSpace(organization)) == 0 {
		t.Fatal("shared organization is empty or boundaries are out of order")
	}
	normalized := strings.Join(strings.Fields(organization), " ")
	for _, required := range []string{
		"never maintain a fixed project inventory", "unique active HQ",
		"personal Home-screen stacks are not API folders",
		`"OPS \u2014 "`, `"BET \u2014 "`, `"ARCHIVED \u2014 "`, `"Backlog \u2014"`,
		"project is still API-active", "do not reactivate it",
		"`CAPTURE` and `WEB CLIPS` lists stay unassigned",
		"`HQ ACTIONS`", "`DECISIONS`", "`FOCUS`", "`VISION`",
		"Agents must not create, edit, comment on or otherwise write to VISION",
		"trusted owner evidence, not a display name", "superseded by him",
		"VISION supplies relevance, not task activation",
		"Agent-added research, comments or VISION alignment do not activate it",
		"must not change the operator's Up Next or use his personal profile",
		"Select the exact tool from the live dock and pass its ID",
		"Do not invent dates", "a native Doc for longer material",
		"structured research, raw material and machine-readable contracts",
		"an agent cannot complete it by preparing a summary",
		"`.basecamp/config.json`", "`.basecamp/project-url`", "`todolist_id`",
		"These files select a destination, not an actor",
		"GitHub or External Service Door", "explicitly scoped action",
		"exact top-level `AGENT WORK` folder", "Docs & Files is already enabled",
		"does not install or start Night Shift", "automatic Basecamp projection",
		"read back the intended result before any retry",
		"Retry only if it is demonstrably absent and the failure is transient",
	} {
		if !strings.Contains(strings.ToLower(normalized), strings.ToLower(required)) {
			t.Errorf("shared organization lost rule %q", required)
		}
	}
	for _, forbidden := range []string{"Morathustra", "Apollo", "--profile", "SOUL.md", "GH_CONFIG_DIR", "/srv/", "/home/", "/Users/"} {
		if strings.Contains(organization, forbidden) {
			t.Errorf("shared organization leaks named identity or host state: %q", forbidden)
		}
	}
	if regexp.MustCompile(`\b[0-9]{6,}\b`).MatchString(organization) {
		t.Error("shared organization contains live actor, project or tool IDs")
	}

	root := loadRepositoryPack(t).Root.Path
	bootstrap, err := os.ReadFile(filepath.Join(root, "host-instructions/codex/AGENTS.md"))
	if err != nil {
		t.Fatal(err)
	}
	text := strings.Join(strings.Fields(string(bootstrap)), " ")
	for _, required := range []string{
		"identity-neutral coding tasks", "explicitly bootstrapped named agent",
		"does not change an ordinary task's actor", "installed official `basecamp` skill",
		"Only Personal policy loads", "`BASECAMP-ORGANIZATION.md`", "Codex is the execution harness",
		"loom box context --json", "every Company Basecamp write needs the operator's approval",
		"trusted workspace `AGENTS.md`", "personality, signature and account protocols",
		"`CODEX-IDENTITY.md` in the active `CODEX_HOME` once per session",
		"$HOME/.codex/CODEX-IDENTITY.md", "do not load it into a named session",
		"Never change the shared default", "`BASECAMP_PROFILE`", "current actor's own account protocol",
		"no new action authority", "profiles are not per-agent OS isolation",
		"Follow the project's own `AGENTS.md`, `.loom/` and `.project/` contracts",
		"opted-in legacy `.repo/`", "project instructions govern the work",
		"For analysis-only work",
		"current actor's local authorship rule", "Archivist's specialized operating scope remains narrower",
	} {
		if !strings.Contains(text, required) {
			t.Errorf("Codex bootstrap lost rule %q", required)
		}
	}
	for _, forbidden := range []string{"--profile codex", "--profile morathustra", "--profile apollo", "--profile personal", "SOUL.md", "GH_CONFIG_DIR", "Written by Codex", "200003", "400003"} {
		if strings.Contains(text, forbidden) {
			t.Errorf("Codex bootstrap inherited named-account instructions: %q", forbidden)
		}
	}
	if strings.Contains(string(bootstrap), "<!-- BEGIN LOOM SHARED BASECAMP ORGANIZATION -->") {
		t.Error("bootstrap must reference the source-derived companion, not duplicate its source")
	}

	identity, err := os.ReadFile(filepath.Join(root, "host-instructions/codex/CODEX-IDENTITY.md"))
	if err != nil {
		t.Fatal(err)
	}
	text = strings.Join(strings.Fields(string(identity)), " ")
	for _, required := range []string{
		"fallback for identity-neutral coding tasks", "Named agents use their workspace's identity protocol instead",
		"Never change the shared default", "`BASECAMP_PROFILE`",
		"selected Box's operating policy", "Missing Company access", "*Written by Codex*",
		"Do not borrow a named agent's signature", "grants no action authority",
	} {
		if !strings.Contains(text, required) {
			t.Errorf("ordinary Codex identity lost rule %q", required)
		}
	}
	for _, forbidden := range []string{"--profile codex", "300001", "200003", "BASECAMP-ORGANIZATION.md"} {
		if strings.Contains(text, forbidden) {
			t.Errorf("global identity contains Personal account policy %q", forbidden)
		}
	}
	for _, forbidden := range []string{"MINA", "Morathustra", "Apollo", "--profile mina", "SOUL.md", "loom-mina-"} {
		if strings.Contains(text, forbidden) {
			t.Errorf("ordinary identity inherited named instructions: %q", forbidden)
		}
	}
}

func TestMorathustraGitHubSeparatesActorTransportAndPrivateProjection(t *testing.T) {
	payloads := morathustraInstructions(t)
	content := strings.Join(strings.Fields(string(payloads["protocols/GITHUB.md"])), " ")
	for _, required := range []string{
		"the operator's personal GitHub identity through a fresh isolated OAuth grant on Main",
		"Never copy or reuse existing Mac/shared tokens", "shared CLI login, credential helpers, SSH keys or SSH agents",
		"Codex's, Apollo's or another agent's credentials", "Do not switch the shared CLI account or change global Git configuration",
		"outside the live workspace and its projection", "Bind `GH_CONFIG_DIR`",
		"loom-morathustra-gh api user", "operator binding in the configured deployment's operations documentation",
		"shared agents UID is not per-agent OS isolation", "no automatic named-account environment",
		"API authentication does not prove Git fetch/push transport",
		"it must contain only the operator's verified personal identity under this fresh grant",
		"immutable numeric actor ID", "matching both the exact login and numeric ID",
		"on the bound host", "Missing binding, failed authentication, unexpected actor, host or scope means stop with no fallback",
		"`GH_TOKEN`, `GITHUB_TOKEN`, `GH_ENTERPRISE_TOKEN` and `GITHUB_ENTERPRISE_TOKEN` must be absent",
		"Git's actual fetch/push transport must use the same verified personal actor and isolated grant",
		"no inherited credential or transport fallback", "stop before any fetch or push",
		"Configured access is limited by actual granted permissions, SSO policies and the authorized task",
		"not a grant of account-wide authority", "Verify the permissions needed for the exact repository and task",
		"Account access does not grant blanket action authority", "Use only approved scopes needed for the operator's authorized work",
		"do not automatically request every admin OAuth scope or change organisation policies to obtain access",
		"does not establish repository creation, publication or organization-owner authority",
		"never widen access to make a command succeed",
		"non-Git native agent workspace", "Never initialize Git there",
		"private `OWNER/morathustra-workspace`", "this template does not create it", "before every push",
		"verified personal actor's permission before creation and before every push",
		"No public fallback or force push", "explicit reviewed file allowlist and source hashes",
		"Exclude every unlisted file", "every commit reachable from the proposed push",
		"An ignore file alone is insufficient", "separate integrator actions with the operator present",
		"not a second active personality", "Do not copy Apollo's identity, private memory, domain authority or file-backed memory system",
		"symlinks, hardlinks to live files and Git alternates",
	} {
		if !strings.Contains(content, required) {
			t.Errorf("GitHub protocol lost isolation rule %q", required)
		}
	}
	// Actor selection, grant isolation and private projection are separate contracts.
	for _, superseded := range []string{"one dedicated Morathustra GitHub actor", "Never reuse the operator's", "same verified dedicated actor", "dedicated actor's permission"} {
		if strings.Contains(content, superseded) {
			t.Errorf("GitHub protocol retains superseded identity restriction %q", superseded)
		}
	}
	for _, state := range []string{"config", "credentials", "OAuth/cache", "sessions", "memories", "databases", "WAL/SHM", "cron", "logs", "native/created skills", "recovery payloads", "`skills/installed` deployment copies", "investigations", "completed handoffs", "scratch", "machine paths"} {
		if !strings.Contains(content, state) {
			t.Errorf("GitHub exclusion lost %q", state)
		}
	}
	// Independently check that the explicit projection allowlist consists only
	// of source contracts, never the scaffold's reserved runtime placeholders.
	_, list, ok := strings.Cut(content, "Initially allow only these source templates:")
	if !ok {
		t.Fatal("missing projection allowlist")
	}
	list, _, ok = strings.Cut(list, "The projection is versioned source")
	if !ok {
		t.Fatal("missing projection boundary")
	}
	paths := regexp.MustCompile("`([^`]+\\.md)`").FindAllStringSubmatch(list, -1)
	want := map[string]bool{
		".hermes/SOUL.md": true, "AGENTS.md": true, "WORKFLOW.md": true,
		"OPERATING-POLICY.md": true, "WORKSPACE-MAP.md": true, "TOOLING.md": true,
		"protocols/README.md": true, "protocols/BASECAMP.md": true, "protocols/GITHUB.md": true,
		"protocols/CREDENTIALS.md": true, "protocols/EXTERNAL-MESSAGING.md": true,
		"protocols/LOOM-ROUTING.md": true, "protocols/PROJECT-DELEGATION.md": true,
		"protocols/PROVENANCE-AND-MEMORY.md": true, "protocols/SESSION-RETRIEVAL.md": true,
		"protocols/SKILL-CREATION-AND-PROMOTION.md": true, "handoffs/HANDOFF-TEMPLATE.md": true,
	}
	for _, match := range paths {
		name := match[1]
		if !want[name] || len(payloads[name]) == 0 {
			t.Fatalf("unreviewed, duplicate or missing projection source: %s", name)
		}
		delete(want, name)
	}
	if len(want) != 0 {
		t.Fatalf("projection omits reviewed source: %v", want)
	}
}

func TestMorathustraNamedProtocolsAreLinkedWithoutWorkerIdentityLeakage(t *testing.T) {
	payloads := morathustraInstructions(t)
	for _, name := range []string{"AGENTS.md", "WORKFLOW.md", "TOOLING.md", "protocols/README.md"} {
		for _, protocol := range []string{"BASECAMP.md", "GITHUB.md"} {
			if !strings.Contains(string(payloads[name]), protocol) {
				t.Errorf("%s does not route to %s", name, protocol)
			}
		}
	}
	project, err := os.ReadFile(filepath.Join(loadRepositoryPack(t).Root.Path, "templates/project/AGENTS.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"Morathustra", "Apollo", "SOUL.md", "--profile morathustra", "GH_CONFIG_DIR"} {
		if strings.Contains(string(project), forbidden) {
			t.Errorf("project worker inherited named identity: %q", forbidden)
		}
	}
}
