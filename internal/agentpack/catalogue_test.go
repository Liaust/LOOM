package agentpack_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestBoxSourceRoutingAndCitationProtocol(t *testing.T) {
	read := func(path string) string {
		t.Helper()
		raw, err := os.ReadFile(filepath.Join("..", "..", path))
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}
	routing := read("ai-loom-pack/skills/use-loom-provenance/references/retrieval-routing.md")
	review := read("ai-loom-pack/skills/use-loom-provenance/references/reconciliation.md")
	for _, required := range []string{"loom object inspect", "loom notes search", "loom notes passage get", "--object <object-id>", "--version <version-id>", "--source-hash sha256:<digest>", "loom provenance search", "--include-pending", "current_source_context", "Topics remain drafts", "Library passages remain attributed claims", "search one engine first"} {
		if !strings.Contains(routing, required) {
			t.Fatalf("routing lost %q", required)
		}
	}
	for _, required := range []string{"Indexing a file does not register or accept", "knowledge_object_id", "knowledge_object_version_id", "knowledge_chunk_id", "source_hash", "passage_locator", "canonical_locator", "version_address", "content_digest", "Source edits neither rewrite", "Current\nprivacy", "Do not substitute a current passage"} {
		if !strings.Contains(review, required) {
			t.Fatalf("reconciliation lost %q", required)
		}
	}
	// This checks coverage of the written routing contract, not model accuracy.
	var fixture struct {
		Queries []struct {
			Key     string `json:"key"`
			Engine  string `json:"first_engine"`
			Request struct {
				Operation string `json:"operation"`
			} `json:"request"`
		} `json:"queries"`
	}
	if err := json.Unmarshal([]byte(read("internal/knowledge/testdata/box_sources/queries.json")), &fixture); err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	for _, q := range fixture.Queries {
		counts[q.Engine]++
		switch q.Engine {
		case "objects":
			if q.Request.Operation != "inspect" {
				t.Fatalf("technical route drift: %s", q.Key)
			}
		case "notes":
			if q.Request.Operation != "search" && q.Request.Operation != "exact_get" {
				t.Fatalf("source route drift: %s", q.Key)
			}
		case "provenance":
			if q.Key != "accepted-decision" && q.Key != "pending-opt-in" {
				t.Fatalf("semantic route drift: %s", q.Key)
			}
		default:
			t.Fatalf("unsupported first engine %s", q.Engine)
		}
	}
	if len(fixture.Queries) != 24 || counts["objects"] != 3 || counts["provenance"] != 2 || counts["notes"] != 19 {
		t.Fatalf("routing matrix incomplete: %v", counts)
	}
}

type catalogueContract struct {
	Roles             []string `yaml:"roles"`
	VisibilityClasses []string `yaml:"visibility_classes"`
	SafetyClasses     []string `yaml:"safety_classes"`
	Invariants        []struct {
		ID   string `yaml:"id"`
		Rule string `yaml:"rule"`
	} `yaml:"invariants"`
	Skills []struct {
		Name             string   `yaml:"name"`
		Status           string   `yaml:"status"`
		Visibility       string   `yaml:"visibility"`
		IntendedRoles    []string `yaml:"intended_roles"`
		PositiveTriggers []string `yaml:"positive_triggers"`
		NegativeTriggers []string `yaml:"negative_triggers"`
		SafetyClass      string   `yaml:"safety_class"`
	} `yaml:"skills"`
}

func TestCatalogueKeepsArchivistRoleAndVisibilityNarrow(t *testing.T) {
	manifest := loadManifestContract(t)
	var catalogue catalogueContract
	readYAMLContract(t, "catalogue.yaml", &catalogue)
	if !stringSliceContains(catalogue.Roles, "archivist") {
		t.Fatalf("catalogue roles omit archivist: %v", catalogue.Roles)
	}
	if !stringSliceContains(catalogue.VisibilityClasses, "shared_provenance") {
		t.Fatalf("catalogue visibility omits shared_provenance: %v", catalogue.VisibilityClasses)
	}

	set := manifest.RecommendedSkillSets["archivist"].Visibility
	if len(set) != 1 || set[0] != "shared_provenance" {
		t.Fatalf("archivist recommended visibility widened: %v", set)
	}
	allowed := map[string]bool{"use-loom-provenance": true}
	for _, skill := range catalogue.Skills {
		if stringSliceContains(set, skill.Visibility) && !allowed[skill.Name] {
			t.Errorf("unexpected archivist-visible skill %q", skill.Name)
		}
		if skill.Name == "use-loom-provenance" {
			if skill.Visibility != "shared_provenance" || !stringSliceContains(skill.IntendedRoles, "archivist") {
				t.Errorf("provenance skill is not shared with archivist: %#v", skill)
			}
			for role, setName := range map[string]string{"mina": "mina", "morathustra": "morathustra", "project-agent": "project"} {
				if !stringSliceContains(skill.IntendedRoles, role) || !stringSliceContains(manifest.RecommendedSkillSets[setName].Visibility, skill.Visibility) {
					t.Errorf("%s cannot discover shared provenance skill", role)
				}
			}
		}
		if stringSliceContains([]string{"operate-loom-storage", "operate-loom-nodes", "investigate-loom-incidents", "orchestrate-loom-work", "use-proton-pass"}, skill.Name) && stringSliceContains(skill.IntendedRoles, "archivist") {
			t.Errorf("archivist inherited forbidden operational skill %q", skill.Name)
		}
	}
}

func TestSharedProvenanceCaptureDoesNotDispatchArchivist(t *testing.T) {
	read := func(relative string) string {
		t.Helper()
		content, err := os.ReadFile(filepath.Join("..", "..", "ai-loom-pack", relative))
		if err != nil {
			t.Fatal(err)
		}
		return string(content)
	}
	for _, relative := range []string{"SKILL.md", "references/registration.md", "references/reconciliation.md"} {
		content := read("skills/use-loom-provenance/" + relative)
		if strings.Contains(content, "loom worker run") || strings.Contains(content, "loom worker inspect") {
			t.Fatalf("shared capture dispatches worker in %s", relative)
		}
	}
	registration := read("skills/use-loom-provenance/references/registration.md")
	for _, required := range []string{"POST /v1/provenance/candidates", "X-Loom-Idempotency-Key", "--unix-socket", "--fail-with-body", "candidate get <candidate-id> --sources", "--include-pending", "effective_state", "producer labels", "unchanged request/producer"} {
		if !strings.Contains(registration, required) {
			t.Errorf("registration guidance omits %s", required)
		}
	}
	_, example, found := strings.Cut(registration, "```json\n")
	if !found {
		t.Fatal("registration example missing")
	}
	example, _, _ = strings.Cut(example, "\n```")
	var request struct {
		Candidates []struct {
			Producer struct {
				ID string `json:"producer_id"`
			} `json:"producer"`
			Sources []struct {
				Status       string `json:"status"`
				Verification string `json:"verification_posture"`
				Gap          string `json:"gap_reason"`
				Locator      string `json:"canonical_locator"`
				Digest       string `json:"content_digest"`
			} `json:"sources"`
		} `json:"candidates"`
	}
	if err := json.Unmarshal([]byte(example), &request); err != nil {
		t.Fatal(err)
	}
	if len(request.Candidates) != 1 || request.Candidates[0].Producer.ID != "mina" || len(request.Candidates[0].Sources) != 1 {
		t.Fatal("example must show one source-backed MINA candidate")
	}
	source := request.Candidates[0].Sources[0]
	if source.Status != "unresolved" || source.Verification != "unverified" || source.Gap == "" || source.Locator != "" || source.Digest != "" {
		t.Fatal("example invents source verification")
	}
	if !strings.Contains(read("templates/archivist/protocols/CANDIDATE-REVIEW.md"), "loom worker run main.provenance_archivist --once") {
		t.Fatal("Archivist lost its separate manual control path")
	}
	if !strings.Contains(read("templates/mina/protocols/PROVENANCE-AND-MEMORY.md"), "use-loom-provenance") {
		t.Fatal("MINA capture skill discovery missing")
	}
}

func TestKnowledgeRetrievalSkillIsDiscoverableWithoutArchivistAuthority(t *testing.T) {
	manifest := loadManifestContract(t)
	var catalogue catalogueContract
	readYAMLContract(t, "catalogue.yaml", &catalogue)
	found := false
	for _, skill := range catalogue.Skills {
		if skill.Name != "search-loom-knowledge" {
			continue
		}
		found = true
		if skill.SafetyClass != "inspect" || skill.Visibility != "shared_core" || !stringSliceContains(skill.IntendedRoles, "mina") || stringSliceContains(skill.IntendedRoles, "archivist") {
			t.Fatalf("retrieval skill authority drift: %#v", skill)
		}
		if !stringSliceContains(manifest.RecommendedSkillSets["mina"].Visibility, skill.Visibility) {
			t.Fatal("MINA cannot discover retrieval skill")
		}
	}
	if !found {
		t.Fatal("retrieval skill missing")
	}
	read := func(relative string) string {
		t.Helper()
		content, err := os.ReadFile(filepath.Join("..", "..", "ai-loom-pack", relative))
		if err != nil {
			t.Fatal(err)
		}
		return string(content)
	}
	skill := read("skills/search-loom-knowledge/SKILL.md")
	for _, command := range []string{"notes search", "notes objects show", "notes passage get", "object inspect", "provenance search", "provenance record get", "provenance candidate get", "provenance repo list", "--source-hash", "--include-pending", "--repo <repository>"} {
		if !strings.Contains(skill, command) {
			t.Errorf("missing supported retrieval path: %s", command)
		}
	}
	for _, entry := range []string{"templates/mina/AGENTS.md", "templates/mina/protocols/PROVENANCE-AND-MEMORY.md"} {
		if !strings.Contains(read(entry), "search-loom-knowledge") {
			t.Errorf("missing bootstrap discovery: %s", entry)
		}
	}
	if !strings.Contains(read("skills/search-loom-knowledge/references/exact-citations.md"), "metadata.text_pipeline.knowledge_object_version_id") {
		t.Fatal("citation instructions no longer describe the verified object-show shape")
	}
}

func TestVerifiedRetrievalSourceBoundaries(t *testing.T) {
	for _, c := range []struct {
		file string
		want []string
	}{
		{"skills/search-loom-knowledge/SKILL.md", []string{"known exact ID/citation", "directly to its getter", "Prefer the complete `passage_followup` tuple", "--source-lifecycle <tuple-lifecycle>", "Only when no tuple exists", "Never replace a historical match's digest with a current object digest", "current source access", "loom --json provenance record get <record-id> --sources", "loom --json provenance candidate get <candidate-id> --sources", "omit it when only the parent is needed", "not a current source fetch, candidate acceptance or authority", "`sources_truncated` and `incomplete_items` separately from parent truncation", "needed private receipt detail"}},
		{"skills/search-loom-knowledge/references/exact-citations.md", []string{"metadata.text_pipeline.knowledge_object_version_id", "If version metadata is absent or mismatched, do not use its hash", "Archived lifecycle is separate from historical text version", "do not retry another engine to evade it", "without fetching current sources", "`sources_truncated=true`", "`incomplete_items>0`", "Complete disclosure requires both false/zero", "CLI default/maximum is 16 source items", "16 KiB per item and 256 KiB per expansion", "Disclose only the detail needed", "receipt does not change that lifecycle"}},
	} {
		t.Run(c.file, func(t *testing.T) {
			content, err := os.ReadFile(filepath.Join("..", "..", "ai-loom-pack", c.file))
			if err != nil {
				t.Fatal(err)
			}
			raw := strings.Join(strings.Fields(string(content)), " ")
			for _, term := range c.want {
				if !strings.Contains(raw, term) {
					t.Errorf("retrieval boundary lacks %q", term)
				}
			}
			if strings.Contains(raw, "producer_history[].candidate_id") {
				t.Error("manual producer-history reconstruction restored")
			}
		})
	}
}

func stringSliceContains(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

type scenarioContract struct {
	Roles     []string `yaml:"roles"`
	Scenarios []struct {
		ID              string   `yaml:"id"`
		IntendedRole    string   `yaml:"intended_role"`
		ExpectedSkills  []string `yaml:"expected_skills"`
		ForbiddenSkills []string `yaml:"forbidden_skills"`
		SourceClass     string   `yaml:"expected_source_class"`
		SafetyBehavior  string   `yaml:"expected_safety_behavior"`
	} `yaml:"scenarios"`
}

type scorecardContract struct {
	HardGates   []string       `yaml:"hard_gates"`
	RawCounts   map[string]int `yaml:"raw_counts"`
	Thresholds  map[string]any `yaml:"thresholds"`
	Percentages map[string]any `yaml:"percentages"`
}

func readYAMLContract(t *testing.T, name string, target any) {
	t.Helper()
	payload, err := os.ReadFile(filepath.Join("..", "..", "ai-loom-pack", name))
	if err != nil {
		t.Fatal(err)
	}
	if err := yaml.Unmarshal(payload, target); err != nil {
		t.Fatalf("parse %s: %v", name, err)
	}
}

func TestCatalogueAndEvaluationContractsAgree(t *testing.T) {
	manifest := loadManifestContract(t)
	var catalogue catalogueContract
	readYAMLContract(t, "catalogue.yaml", &catalogue)
	var scenarios scenarioContract
	readYAMLContract(t, filepath.Join("evaluations", "scenarios.yaml"), &scenarios)
	var scorecard scorecardContract
	readYAMLContract(t, filepath.Join("evaluations", "scorecard.yaml"), &scorecard)

	manifestSkills := map[string]bool{}
	for _, skill := range manifest.Skills {
		manifestSkills[skill.Name] = true
	}
	catalogueSkills := map[string]bool{}
	for _, skill := range catalogue.Skills {
		catalogueSkills[skill.Name] = true
		if !manifestSkills[skill.Name] {
			t.Errorf("catalogue skill %q missing from manifest", skill.Name)
		}
		if skill.Status != "candidate" || skill.Visibility == "" || len(skill.IntendedRoles) == 0 || len(skill.PositiveTriggers) == 0 || len(skill.NegativeTriggers) == 0 || skill.SafetyClass == "" {
			t.Errorf("incomplete adaptive catalogue entry: %#v", skill)
		}
	}
	for name := range manifestSkills {
		if !catalogueSkills[name] {
			t.Errorf("manifest skill %q missing from catalogue", name)
		}
	}

	roles := map[string]bool{}
	for _, role := range scenarios.Roles {
		roles[role] = true
	}
	ids := map[string]bool{}
	for _, scenario := range scenarios.Scenarios {
		if scenario.ID == "" || ids[scenario.ID] {
			t.Errorf("missing or duplicate scenario id %q", scenario.ID)
		}
		ids[scenario.ID] = true
		if !roles[scenario.IntendedRole] {
			t.Errorf("scenario %q uses unknown role %q", scenario.ID, scenario.IntendedRole)
		}
		for _, name := range append(append([]string{}, scenario.ExpectedSkills...), scenario.ForbiddenSkills...) {
			if !catalogueSkills[name] {
				t.Errorf("scenario %q references unknown skill %q", scenario.ID, name)
			}
		}
		if scenario.SourceClass == "" || scenario.SafetyBehavior == "" {
			t.Errorf("scenario %q omits source or safety expectation", scenario.ID)
		}
	}

	requiredGates := map[string]bool{"mutation-safety": false, "source-classification": false, "role-boundary": false}
	for _, gate := range scorecard.HardGates {
		if _, ok := requiredGates[gate]; ok {
			requiredGates[gate] = true
		}
	}
	for gate, present := range requiredGates {
		if !present {
			t.Errorf("scorecard missing hard gate %q", gate)
		}
	}
	for _, count := range []string{"selected_correctly", "selected_incorrectly", "no_skill_selected", "unnecessary_co_selection", "task_completed", "command_correct", "source_classification_correct", "safety_compliant", "role_boundary_violations", "irrelevant_references_loaded", "clarification_count", "retry_count", "handoff_fields_present"} {
		if _, ok := scorecard.RawCounts[count]; !ok {
			t.Errorf("scorecard missing raw count %q", count)
		}
	}
}
