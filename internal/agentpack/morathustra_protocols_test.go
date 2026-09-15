package agentpack

import (
	"crypto/sha256"
	"fmt"
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

	// Public baseline fingerprints preserve the operating rules after replacing
	// the private operator's name with generic operator wording.
	for name, want := range map[string]string{
		"OPERATING-POLICY.md":                       "4066ac84a48b2e36aee46d0ea008fef05d9bc0fe2a1528f68d658a4e2abd193a",
		"WORKSPACE-MAP.md":                          "c19f1ebb7068027b62bc5354a5b75fdb6317f6042919467449482964e6e7d690",
		"protocols/CREDENTIALS.md":                  "a5fa7fe12aa4f9ec064347c92268377371ec6de424a4f4fd3662c4743a69ac4d",
		"protocols/EXTERNAL-MESSAGING.md":           "d121fdc8e735baf6a71019a7e749f9a2b218ffa94931cec82cf6a99a9f100d79",
		"protocols/LOOM-ROUTING.md":                 "7fc3db17d326259f02aabc5829e89c535b8bbf67d0af07aa4572bc1f8852e6c6",
		"protocols/PROJECT-DELEGATION.md":           "6ad5e0932f53da45c51b054dd950372353f715f39b979bf9aad93a95b8547268",
		"protocols/PROVENANCE-AND-MEMORY.md":        "4e66df1906a8c46c48a67af1739709dbb28820a4956e99843585aa144d8094bb",
		"protocols/SESSION-RETRIEVAL.md":            "ae6c1ded645b8e153d72fdf2a8ebc445ca829b558a611f981bbe75ec744b896d",
		"protocols/SKILL-CREATION-AND-PROMOTION.md": "05f19c7764f51fcb800b735cc037776903f11e13d6074ad034575cc1c8957de6",
	} {
		if got := fmt.Sprintf("%x", sha256.Sum256(payloads[name])); got != want {
			t.Errorf("accepted operating boundary changed: %s", name)
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
		"operator binding documented in release-matched ORCA Main Operations", "shared agents UID is not per-agent OS isolation",
		"no automatic named-account environment", "never creates or repairs the store",
		"Do not rely on the default profile, set `BASECAMP_PROFILE`, change the shared default",
		"no authenticated identity binding", "after successful OAuth", "Capture and freeze",
		"global `identity.id`", "example-operator `accounts[].id`", "account-scoped recording actor ID",
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
		"`BASECAMP-ORGANIZATION.md` in the active `CODEX_HOME`",
		"$HOME/.codex/BASECAMP-ORGANIZATION.md", "literal `basecamp --profile codex`",
		"Never change the shared default", "`BASECAMP_PROFILE`",
		"basecamp --profile codex me --agent", "`identity.id` exactly `<CODEX_IDENTITY_ID>`",
		"exactly one current account", "`<BASECAMP_ACCOUNT_ID>`", "`<CODEX_PERSON_ID>`", "<CODEX_EMAIL>",
		"before installing it", "Unconfigured placeholders authorize no Basecamp access",
		"stop before task reads or writes", "Do not rebind expected IDs",
		"no new action authority", "profiles are not per-agent OS isolation",
		"Follow the project's own `AGENTS.md`, `.loom/`, `.project/` and any legacy `.repo/`",
		"For analysis-only work",
		"*Written by Codex*", "Archivist's specialized operating scope remains narrower",
	} {
		if !strings.Contains(text, required) {
			t.Errorf("Codex bootstrap lost rule %q", required)
		}
	}
	if !strings.Contains(text, "its original creator does not identify the current updater") {
		t.Error("Codex bootstrap confuses original creator with updater")
	}
	for _, forbidden := range []string{"--profile morathustra", "--profile apollo", "--profile personal", "SOUL.md", "GH_CONFIG_DIR"} {
		if strings.Contains(text, forbidden) {
			t.Errorf("Codex bootstrap inherited named-account instructions: %q", forbidden)
		}
	}
	if strings.Contains(string(bootstrap), "<!-- BEGIN LOOM SHARED BASECAMP ORGANIZATION -->") {
		t.Error("bootstrap must reference the source-derived companion, not duplicate its source")
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
		"loom-morathustra-gh api user", "operator binding documented in release-matched ORCA Main Operations",
		"shared agents UID is not per-agent OS isolation", "no automatic named-account environment",
		"API authentication does not prove Git fetch/push transport",
		"it must contain only the operator's verified personal identity under this fresh grant",
		"immutable numeric actor ID", "matching both the exact login and numeric ID",
		"on the bound host", "Missing binding, failed authentication, unexpected actor, host or scope means stop with no fallback",
		"`GH_TOKEN`, `GITHUB_TOKEN`, `GH_ENTERPRISE_TOKEN` and `GITHUB_ENTERPRISE_TOKEN` must be absent",
		"Git's actual fetch/push transport must use the same verified personal actor and isolated grant",
		"no inherited credential or transport fallback", "stop before any fetch or push",
		"the operator's complete account, repositories and organisations as their actual granted permissions and SSO policies permit",
		"Do not impose a example-operator-only or private-only access limit",
		"one destination, not the boundary of account access", "Verify the permissions needed for the exact repository and task",
		"Account access does not grant blanket action authority", "Use only approved scopes needed for the operator's authorized work",
		"do not automatically request every admin OAuth scope or change organisation policies to obtain access",
		"does not establish repository creation, publication or organization-owner authority",
		"never widen access to make a command succeed",
		"non-Git ORCA folder project", "Never initialize Git there",
		"private `example-operator/morathustra-workspace`", "before every push",
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
	// the operator's Phase 9C amendment supersedes the dedicated GitHub actor only;
	// fresh grant isolation and the private projection remain separate contracts.
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
