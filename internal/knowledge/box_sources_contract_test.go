package knowledge

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"path"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// This is an independent acceptance oracle, not a mock search implementation.
// Fixture keys bind to runtime IDs observed through ingestion in later slices.
// Media fixture_text is an extraction expectation, never a PDF/DOCX byte hash.
//
//go:embed testdata/box_sources/*.json
var boxSourcesContractFS embed.FS

type boxContractRoot struct {
	Key          string `json:"key"`
	Category     string `json:"category"`
	RootKind     string `json:"root_kind"`
	NodeKey      string `json:"node_key"`
	ProjectKey   string `json:"project_key"`
	RelativePath string `json:"relative_path"`
	Registered   bool   `json:"registered"`
	OwnerOnline  bool   `json:"owner_online"`
}

type boxContractLocator struct {
	Kind    string `json:"kind"`
	Heading string `json:"heading"`
	Page    int    `json:"page"`
}

type boxContractPassage struct {
	Key     string             `json:"key"`
	Text    string             `json:"text"`
	Locator boxContractLocator `json:"locator"`
}

type boxContractVersion struct {
	Key         string               `json:"key"`
	FixtureText string               `json:"fixture_text"`
	Passages    []boxContractPassage `json:"passages"`
}

type boxContractSource struct {
	Key            string               `json:"key"`
	RootKey        string               `json:"root_key"`
	RelativePath   string               `json:"relative_path"`
	Media          string               `json:"media"`
	Posture        string               `json:"posture"`
	Selection      string               `json:"selection"`
	EntryType      string               `json:"entry_type"`
	LinkTarget     string               `json:"link_target"`
	Declaration    string               `json:"declaration"`
	TopicKey       string               `json:"topic_key"`
	CollectionKey  string               `json:"collection_key"`
	Lifecycle      string               `json:"lifecycle"`
	Extraction     string               `json:"extraction"`
	CurrentVersion string               `json:"current_version"`
	Versions       []boxContractVersion `json:"versions"`
}

type boxContractRecord struct {
	Key           string `json:"key"`
	Collection    string `json:"collection"`
	Posture       string `json:"posture"`
	SourceVersion string `json:"source_version"`
	PassageKey    string `json:"passage_key"`
	Text          string `json:"text"`
}

type boxContractCorpus struct {
	SchemaVersion   string              `json:"schema_version"`
	IdentityBinding string              `json:"identity_binding"`
	Projects        []string            `json:"projects"`
	Roots           []boxContractRoot   `json:"roots"`
	Sources         []boxContractSource `json:"sources"`
	SemanticRecords []boxContractRecord `json:"semantic_records"`
}

type boxContractFilters struct {
	ProjectKey     string `json:"project_key"`
	SourceCategory string `json:"source_category"`
	OwnerNode      string `json:"owner_node"`
}

type boxContractQuery struct {
	Key             string             `json:"key"`
	Text            string             `json:"text"`
	FirstEngine     string             `json:"first_engine"`
	Request         boxContractRequest `json:"request"`
	Scenario        string             `json:"scenario"`
	Filters         boxContractFilters `json:"filters"`
	IncludePending  bool               `json:"include_pending"`
	IncludeArchived bool               `json:"include_archived"`
	Dependency      string             `json:"dependency"`
	Expected        []string           `json:"expected"`
	Prohibited      []string           `json:"prohibited"`
	ExpectedState   string             `json:"expected_state"`
}

type boxContractRequest struct {
	Operation string `json:"operation"`
	Query     string `json:"query"`
	Target    string `json:"target"`
}

type boxContractQueries struct {
	SchemaVersion string             `json:"schema_version"`
	Queries       []boxContractQuery `json:"queries"`
}

type boxContractSourceRule struct {
	Category       string   `json:"category"`
	Admission      string   `json:"admission"`
	RootKinds      []string `json:"root_kinds"`
	DefaultPosture string   `json:"default_posture"`
}

type boxContractScenario struct {
	Key               string   `json:"key"`
	Source            string   `json:"source"`
	Action            string   `json:"action"`
	Identity          string   `json:"identity"`
	ExtractionJobs    int      `json:"extraction_jobs"`
	GeneratedCopies   int      `json:"generated_copies"`
	SemanticMutations int      `json:"semantic_mutations"`
	Required          []string `json:"required"`
}

type boxContractPolicies struct {
	SchemaVersion string                  `json:"schema_version"`
	SourceMatrix  []boxContractSourceRule `json:"source_matrix"`
	Exclusions    []string                `json:"exclusions"`
	HeavyPolicy   struct {
		PDFOCR     bool `json:"pdf_ocr_enabled"`
		Vision     bool `json:"image_descriptions_enabled"`
		Embeddings bool `json:"embeddings_enabled"`
	} `json:"heavy_policy"`
	Authority struct {
		Promote      bool `json:"semantic_promotion_on_ingest"`
		Supersede    bool `json:"semantic_supersession_on_edit"`
		RemoteCrawl  bool `json:"main_crawls_remote_paths"`
		FollowLinks  bool `json:"follow_links_outside_authority"`
		CrossProject bool `json:"cross_project_content_authority"`
	} `json:"authority"`
	CitationFields []string `json:"citation_fields"`
	Acceptance     struct {
		EntryBoundary            string   `json:"entry_boundary"`
		ManualKnowledgeSeed      bool     `json:"manual_knowledge_seed"`
		ManualPipelineRun        bool     `json:"manual_pipeline_run"`
		FixtureHashContract      string   `json:"fixture_hash_contract"`
		RuntimeIdentityBinding   string   `json:"runtime_identity_binding"`
		ArchiveDependency        string   `json:"archive_dependency"`
		BaselinePilotMaxBytes    int      `json:"baseline_pilot_max_source_bytes"`
		LatencyMeasurements      []string `json:"latency_measurements"`
		WorkMeasurements         []string `json:"work_measurements"`
		BaselineObservedSeconds  []int    `json:"baseline_observed_seconds"`
		BaselineTimingsAreNotSLA bool     `json:"baseline_timings_are_not_sla"`
	} `json:"acceptance"`
	Scenarios []boxContractScenario `json:"scenarios"`
}

type boxContract struct {
	Corpus   boxContractCorpus
	Queries  boxContractQueries
	Policies boxContractPolicies
}

func loadBoxSourcesContract(t *testing.T) boxContract {
	t.Helper()
	var c boxContract
	for name, target := range map[string]any{"corpus": &c.Corpus, "queries": &c.Queries, "policies": &c.Policies} {
		b, err := boxSourcesContractFS.ReadFile("testdata/box_sources/" + name + ".json")
		if err != nil {
			t.Fatal(err)
		}
		if err := decodeBoxSourcesContract(b, target); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
	return c
}

func decodeBoxSourcesContract(b []byte, target any) error {
	// Reject duplicate keys before typed decoding can silently keep the last one.
	d := json.NewDecoder(bytes.NewReader(b))
	if err := boxContractJSONValue(d); err != nil {
		return err
	}
	if _, err := d.Token(); err != io.EOF {
		return fmt.Errorf("trailing JSON input")
	}
	d = json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if err := d.Decode(target); err != nil {
		return err
	}
	var original, normalized any
	if err := json.Unmarshal(b, &original); err != nil {
		return err
	}
	canonical, err := json.Marshal(target)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(canonical, &normalized); err != nil {
		return err
	}
	// No omitempty fields: absent false/empty nested fields are not accepted as
	// a weaker contract. Null collections are also forbidden, including empty ones.
	if !reflect.DeepEqual(original, normalized) || boxContractHasNull(original) {
		return fmt.Errorf("missing or null required contract field")
	}
	return nil
}

func boxContractJSONValue(d *json.Decoder) error {
	token, err := d.Token()
	if err != nil {
		return err
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	seen := map[string]bool{}
	for d.More() {
		if delim == '{' {
			key, err := d.Token()
			if err != nil {
				return err
			}
			name, ok := key.(string)
			if !ok || seen[name] {
				return fmt.Errorf("duplicate or invalid JSON key %v", key)
			}
			seen[name] = true
		}
		if err := boxContractJSONValue(d); err != nil {
			return err
		}
	}
	_, err = d.Token()
	return err
}

func boxContractHasNull(v any) bool {
	switch x := v.(type) {
	case nil:
		return true
	case map[string]any:
		for _, child := range x {
			if boxContractHasNull(child) {
				return true
			}
		}
	case []any:
		for _, child := range x {
			if boxContractHasNull(child) {
				return true
			}
		}
	}
	return false
}

var boxContractKey = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

func boxContractPath(s string) bool {
	return s != "" && s != "." && !path.IsAbs(s) && path.Clean(s) == s &&
		!strings.ContainsAny(s, "\\:\x00") && s != ".." && !strings.HasPrefix(s, "../")
}

func validateBoxSourcesContract(c boxContract) error {
	if c.Corpus.SchemaVersion != "loom.box_sources.corpus.v1" || c.Queries.SchemaVersion != "loom.box_sources.queries.v1" || c.Policies.SchemaVersion != "loom.box_sources.policies.v1" || c.Corpus.IdentityBinding != "fixture_keys_are_not_runtime_ids" {
		return fmt.Errorf("schema or identity binding")
	}
	if err := validateBoxContractPolicies(c.Policies); err != nil {
		return err
	}
	if !slices.Equal(c.Corpus.Projects, []string{"atlas", "other"}) {
		return fmt.Errorf("project fixtures")
	}
	roots := map[string]boxContractRoot{}
	rootLocations := map[string]bool{}
	for _, r := range c.Corpus.Roots {
		if !boxContractKey.MatchString(r.Key) || roots[r.Key].Key != "" || !boxContractPath(r.RelativePath) || !slices.Contains([]string{"main", "macbook"}, r.NodeKey) {
			return fmt.Errorf("root identity/path: %s", r.Key)
		}
		if r.ProjectKey != "" && !slices.Contains(c.Corpus.Projects, r.ProjectKey) {
			return fmt.Errorf("unknown project: %s", r.Key)
		}
		ruleIndex := slices.IndexFunc(c.Policies.SourceMatrix, func(rule boxContractSourceRule) bool { return rule.Category == r.Category })
		if ruleIndex < 0 || (r.RootKind != "" && !slices.Contains(c.Policies.SourceMatrix[ruleIndex].RootKinds, r.RootKind)) || (r.Registered && r.RootKind == "") {
			return fmt.Errorf("root kind/category: %s", r.Key)
		}
		if (r.Category == "projects") != (r.ProjectKey != "") || ((r.Category == "documents" || r.Category == "imports") && r.Registered) {
			return fmt.Errorf("undeclared root authority: %s", r.Key)
		}
		location := r.NodeKey + ":" + r.RelativePath
		if rootLocations[location] {
			return fmt.Errorf("duplicate registered location: %s", location)
		}
		rootLocations[location], roots[r.Key] = true, r
	}
	sources := map[string]boxContractSource{}
	versions := map[string]boxContractSource{}
	passages := map[string]boxContractSource{}
	passageVersions := map[string]string{}
	locations := map[string]bool{}
	for _, s := range c.Corpus.Sources {
		r, ok := roots[s.RootKey]
		if !ok || !boxContractKey.MatchString(s.Key) || sources[s.Key].Key != "" || !boxContractPath(s.RelativePath) {
			return fmt.Errorf("source identity/root/path: %s", s.Key)
		}
		location := s.RootKey + ":" + s.RelativePath
		if locations[location] || !slices.Contains([]string{"source_material", "draft", "attributed_claim"}, s.Posture) {
			return fmt.Errorf("source identity/posture: %s", s.Key)
		}
		if !slices.Contains([]string{"active", "archived", "offline"}, s.Lifecycle) || !slices.Contains([]string{"markdown", "text", "code", "pdf_native", "docx", "pdf_scanned", "image", "unsupported"}, s.Media) {
			return fmt.Errorf("source lifecycle/media: %s", s.Key)
		}
		if !slices.Contains([]string{"native_text", "needs_ocr", "needs_image_description", "metadata_only"}, s.Extraction) || (s.Selection != "eligible" && !slices.Contains(c.Policies.Exclusions, s.Selection)) {
			return fmt.Errorf("source selection/extraction: %s", s.Key)
		}
		selection := map[string]string{
			"private-topic": "private_no_index", "credential": "credentials",
			"agent-state": "agent_runtime", "cache": "cache", "dependency": "dependency",
			"build": "build", "ignored": "loomignore", "link": "link_outside_authority",
			"undeclared-code": "undeclared_repository", "documents-default": "registration_required",
			"imports-default": "registration_required",
		}[s.Key]
		if selection == "" {
			selection = "eligible"
		}
		if s.Selection != selection {
			return fmt.Errorf("frozen source selection: %s", s.Key)
		}
		if s.EntryType == "symlink" {
			if s.Selection != "link_outside_authority" || s.LinkTarget == "" || path.IsAbs(s.LinkTarget) {
				return fmt.Errorf("symlink authority: %s", s.Key)
			}
		} else if s.EntryType != "regular_file" || s.LinkTarget != "" {
			return fmt.Errorf("source type: %s", s.Key)
		}
		if s.Selection == "eligible" {
			if !r.Registered || (r.Category == "topics" && s.Posture != "draft") || (r.Category == "library" && s.Posture != "attributed_claim") {
				return fmt.Errorf("source policy/posture: %s", s.Key)
			}
			if r.RootKind == "project_notes" && s.Declaration != "legacy_notes" {
				return fmt.Errorf("legacy project declaration: %s", s.Key)
			}
			if r.RootKind == "project_material" && (!slices.Contains([]string{"notes", "docs", "research"}, s.Declaration) || !strings.HasPrefix(s.RelativePath, s.Declaration+"/")) {
				return fmt.Errorf("project material declaration: %s", s.Key)
			}
			extraction := map[string]string{"pdf_scanned": "needs_ocr", "image": "needs_image_description", "unsupported": "metadata_only"}[s.Media]
			if extraction == "" {
				extraction = "native_text"
			}
			if s.Extraction != extraction {
				return fmt.Errorf("media extraction qualification: %s", s.Key)
			}
		}
		if (r.Category == "topics" && (s.TopicKey == "" || !strings.HasPrefix(s.RelativePath, s.TopicKey+"/"))) || (r.Category != "topics" && s.TopicKey != "") || (r.Category == "library" && (s.CollectionKey == "" || !strings.HasPrefix(s.RelativePath, s.CollectionKey+"/"))) || (r.Category != "library" && s.CollectionKey != "") {
			return fmt.Errorf("declared context: %s", s.Key)
		}
		if (s.Lifecycle == "offline") != !r.OwnerOnline {
			return fmt.Errorf("owner freshness: %s", s.Key)
		}
		currentFound := false
		for _, v := range s.Versions {
			if !boxContractKey.MatchString(v.Key) || versions[v.Key].Key != "" || len(v.FixtureText) > 65536 {
				return fmt.Errorf("version identity/size: %s", v.Key)
			}
			currentFound = currentFound || v.Key == s.CurrentVersion
			versions[v.Key] = s
			if s.Selection == "eligible" && s.Extraction != "native_text" && (v.FixtureText != "" || len(v.Passages) != 0) {
				return fmt.Errorf("fabricated extraction: %s", s.Key)
			}
			if s.Selection == "eligible" && s.Extraction == "native_text" && len(v.Passages) == 0 {
				return fmt.Errorf("missing native passage: %s", s.Key)
			}
			for _, p := range v.Passages {
				if !boxContractKey.MatchString(p.Key) || passages[p.Key].Key != "" || p.Text == "" || !strings.Contains(v.FixtureText, p.Text) {
					return fmt.Errorf("passage identity/text: %s", p.Key)
				}
				if p.Locator.Kind == "page" {
					if s.Media != "pdf_native" || p.Locator.Page < 1 || p.Locator.Heading != "" {
						return fmt.Errorf("page locator: %s", p.Key)
					}
				} else if p.Locator.Kind != "heading" || p.Locator.Heading == "" || p.Locator.Page != 0 || s.Media == "pdf_native" {
					return fmt.Errorf("heading locator: %s", p.Key)
				}
				passages[p.Key], passageVersions[p.Key] = s, v.Key
			}
		}
		if !currentFound {
			return fmt.Errorf("current version not owned: %s", s.Key)
		}
		sources[s.Key], locations[location] = s, true
	}
	if len(roots) != 9 || len(sources) != 26 || len(versions) != 27 || len(c.Corpus.SemanticRecords) != 2 {
		return fmt.Errorf("missing corpus coverage")
	}
	a, b := sources["duplicate-atlas"], sources["duplicate-other"]
	if len(a.Versions) != 1 || len(b.Versions) != 1 || a.Versions[0].FixtureText != b.Versions[0].FixtureText || roots[a.RootKey].ProjectKey == roots[b.RootKey].ProjectKey {
		return fmt.Errorf("duplicate-byte isolation")
	}
	if sources["archived"].RootKey != "main-topics" || sources["archived"].Lifecycle != "archived" {
		return fmt.Errorf("archive must preserve source root identity")
	}
	records := map[string]boxContractRecord{}
	for _, r := range c.Corpus.SemanticRecords {
		if !boxContractKey.MatchString(r.Key) || records[r.Key].Key != "" || r.Text == "" || passageVersions[r.PassageKey] != r.SourceVersion || versions[r.SourceVersion].Key == "" {
			return fmt.Errorf("semantic evidence join: %s", r.Key)
		}
		if (r.Collection != "accepted_records" || r.Posture != "accepted_decision") && (r.Collection != "pending_candidates" || r.Posture != "unaccepted_proposal") {
			return fmt.Errorf("semantic posture: %s", r.Key)
		}
		records[r.Key] = r
	}
	if records["accepted-cadence"].SourceVersion != "note-v1" || records["accepted-cadence"].Collection != "accepted_records" || records["pending-hourly"].SourceVersion != "topic-v1" || records["pending-hourly"].Collection != "pending_candidates" {
		return fmt.Errorf("historical semantic evidence moved")
	}
	for _, scenario := range c.Policies.Scenarios {
		if sources[scenario.Source].Key == "" {
			return fmt.Errorf("scenario source: %s", scenario.Key)
		}
	}
	return validateBoxContractQueries(c, roots, sources, versions, passages, passageVersions, records)
}

func validateBoxContractQueries(c boxContract, roots map[string]boxContractRoot, sources, versions, passages map[string]boxContractSource, passageVersions map[string]string, records map[string]boxContractRecord) error {
	seen := map[string]bool{}
	for _, q := range c.Queries.Queries {
		if !boxContractKey.MatchString(q.Key) || seen[q.Key] || q.Text == "" || !slices.Contains([]string{"objects", "notes", "provenance"}, q.FirstEngine) {
			return fmt.Errorf("query identity/engine: %s", q.Key)
		}
		seen[q.Key] = true
		if !slices.Contains([]string{"baseline", "historical_exact_get", "exclusion-change", "permission-change"}, q.Scenario) || !slices.Contains([]string{"current", "historical", "archived", "offline", "needs_ocr", "needs_image_description", "metadata_only"}, q.ExpectedState) {
			return fmt.Errorf("query scenario/state: %s", q.Key)
		}
		if q.Filters.ProjectKey != "" && !slices.Contains(c.Corpus.Projects, q.Filters.ProjectKey) {
			return fmt.Errorf("query project filter: %s", q.Key)
		}
		if q.IncludeArchived != (q.Dependency == "notes-archive-lifecycle") || (q.IncludePending && q.FirstEngine != "provenance") {
			return fmt.Errorf("query opt-in/dependency: %s", q.Key)
		}
		refs := map[string]bool{}
		for _, ref := range append(slices.Clone(q.Expected), q.Prohibited...) {
			if refs[ref] || (sources[ref].Key == "" && passages[ref].Key == "" && records[ref].Key == "") {
				return fmt.Errorf("query reference/contradiction: %s/%s", q.Key, ref)
			}
			refs[ref] = true
		}
		for _, ref := range q.Expected {
			if q.FirstEngine == "provenance" {
				r, ok := records[ref]
				if !ok || (r.Collection == "pending_candidates" && !q.IncludePending) {
					return fmt.Errorf("unaccepted or wrong-engine result: %s", q.Key)
				}
				continue
			}
			s := sources[ref]
			if q.FirstEngine == "notes" {
				if p, ok := passages[ref]; ok {
					s = p
					if q.Scenario != "historical_exact_get" && passageVersions[ref] != s.CurrentVersion {
						return fmt.Errorf("stale passage returned: %s", q.Key)
					}
				} else if s.Extraction == "native_text" || q.ExpectedState != s.Extraction {
					return fmt.Errorf("missing passage or metadata qualification: %s", q.Key)
				}
				if s.Selection != "eligible" || (s.Lifecycle == "archived" && !q.IncludeArchived) || s.Lifecycle == "offline" || q.Scenario == "exclusion-change" || q.Scenario == "permission-change" {
					return fmt.Errorf("excluded/stale source result: %s", q.Key)
				}
			}
			r, ok := roots[s.RootKey]
			if !ok || (q.Filters.ProjectKey != "" && q.Filters.ProjectKey != r.ProjectKey) || (q.Filters.SourceCategory != "" && q.Filters.SourceCategory != r.Category) || (q.Filters.OwnerNode != "" && q.Filters.OwnerNode != r.NodeKey) {
				return fmt.Errorf("result ownership/filter: %s", q.Key)
			}
		}
	}
	return validateBoxContractQueryCoverage(c.Queries.Queries)
}

func TestBoxSourcesContract(t *testing.T) {
	if err := validateBoxSourcesContract(loadBoxSourcesContract(t)); err != nil {
		t.Fatal(err)
	}
}

func validateBoxContractPolicies(p boxContractPolicies) error {
	wantMatrix := []boxContractSourceRule{
		{"notes", "registered_only", []string{"box_notes"}, "source_material"},
		{"topics", "registered_only", []string{"box_topics"}, "draft"},
		{"library", "registered_only", []string{"box_library"}, "attributed_claim"},
		{"projects", "explicit_declarations_only", []string{"project_notes", "project_material"}, "source_material"},
		{"documents", "opt_in_not_default", []string{}, "source_material"},
		{"imports", "explicit_registration_required", []string{}, "source_material"},
		{"archive", "explicit_lifecycle_dependency", []string{"box_topics"}, "preserve_source_posture"},
	}
	if !reflect.DeepEqual(p.SourceMatrix, wantMatrix) || !slices.Equal(p.Exclusions, []string{"private_no_index", "credentials", "agent_runtime", "cache", "dependency", "build", "loomignore", "undeclared_repository", "link_outside_authority", "registration_required"}) {
		return fmt.Errorf("source matrix or privacy exclusions weakened")
	}
	if p.HeavyPolicy.PDFOCR || p.HeavyPolicy.Vision || p.HeavyPolicy.Embeddings || p.Authority.Promote || p.Authority.Supersede || p.Authority.RemoteCrawl || p.Authority.FollowLinks || p.Authority.CrossProject {
		return fmt.Errorf("heavy or semantic/source authority widened")
	}
	if !slices.Equal(p.CitationFields, []string{"knowledge_object_id", "knowledge_object_version_id", "knowledge_chunk_id", "source_hash", "source_category", "owner_node", "declared_context", "extraction_status", "source_lifecycle", "passage_locator"}) {
		return fmt.Errorf("citation field contract weakened")
	}
	a := p.Acceptance
	if a.EntryBoundary != "owner_watched_source" || a.ManualKnowledgeSeed || a.ManualPipelineRun || a.FixtureHashContract != "materialized_source_bytes_not_extracted_text" || a.RuntimeIdentityBinding != "observed_object_version_chunk_ids" || a.ArchiveDependency != "notes-archive-lifecycle" || a.BaselinePilotMaxBytes != 1024 || !a.BaselineTimingsAreNotSLA || !slices.Equal(a.BaselineObservedSeconds, []int{352, 432}) {
		return fmt.Errorf("unattended acceptance, evidence hash or timing boundary weakened")
	}
	if !slices.Equal(a.LatencyMeasurements, []string{"source_change_to_upload", "upload_to_admission", "admission_to_search", "query_p50", "query_p95"}) || !slices.Equal(a.WorkMeasurements, []string{"eligible_bytes", "extracted_bytes", "extraction_jobs", "chunk_count", "queue_depth", "cpu_time", "peak_memory", "generated_copy_count"}) {
		return fmt.Errorf("measurement contract incomplete")
	}
	// -1 means measure, not a promised count. Metadata updates and compatible
	// reuse do not prescribe a materializer's physical copy implementation.
	wantScenarios := []boxContractScenario{
		{"unattended-create", "note", "create_source_v1", "new_observed_identity", 1, 1, 0, []string{"watched_upload", "catalog_admission", "native_text", "exact_lexical_citation", "read_only_projection"}},
		{"unchanged-replay", "note", "repeat_unchanged_ticks", "same_object_version_chunk", 0, 0, 0, []string{"same_pipeline_count", "same_generated_inode_mtime"}},
		{"metadata-edit", "note", "change_non_content_metadata", "same_content_identity", 0, -1, 0, []string{"updated_source_metadata", "no_redundant_extraction"}},
		{"content-edit", "note", "replace_v1_with_v2", "same_object_new_version", 1, 1, 0, []string{"new_current_chunk", "old_body_not_searchable", "historical_citation_pinned", "obsolete_publication_refused"}},
		{"duplicate-bytes", "duplicate-atlas", "compare_with_duplicate_other", "distinct_source_permission_identity", -1, -1, 0, []string{"same_fixture_bytes", "distinct_project_identity", "reuse_only_if_policy_and_extractor_match"}},
		{"exclusion-change", "topic", "set_private_no_index", "preserved_history_not_currently_readable", 0, 0, 0, []string{"no_current_passages", "no_permission_bypass_via_history"}},
		{"permission-change", "project-doc", "revoke_source_access", "preserved_history_not_currently_readable", 0, 0, 0, []string{"no_current_passages", "no_permission_bypass_via_history"}},
		{"linux-default-acl", "topic", "publish_under_inherited_read_only_default_acl", "same_object_version_chunk", 0, 1, 0, []string{"non_root_writer", "source_mode_unchanged", "agents_read_only", "exact_nested_output"}},
		{"failed-projection", "topic", "inject_copy_failure_then_supported_retry", "same_object_version_chunk", 0, -1, 0, []string{"partial_state_visible", "failure_receipt_retained", "no_success_checkpoint_before_repair", "replay_no_extra_extraction"}},
		{"orphaned-upload", "note", "ingest_with_unrelated_missing_project_upload", "new_observed_identity", 1, 1, 0, []string{"fresh_source_progress", "orphan_stays_pending", "aggregate_degradation_visible", "no_queue_discard"}},
		{"restart", "note", "restart_daemon_after_completion", "same_object_version_chunk", 0, 0, 0, []string{"durable_admission_cursor", "same_pipeline_count", "same_generated_inode_mtime", "exact_current_citation"}},
		{"offline-owner", "offline", "owner_unreachable", "retained_observation_not_live_truth", 0, 0, 0, []string{"explicit_offline_state", "no_main_remote_path_crawl"}},
		{"archive-restore", "archived", "consume_trusted_archive_restore_events", "same_object_version_chunk", 0, -1, 0, []string{"notes_archive_lifecycle_required", "no_path_prefix_inference", "explicit_archive_search", "source_posture_unchanged"}},
	}
	if !reflect.DeepEqual(p.Scenarios, wantScenarios) {
		return fmt.Errorf("runtime scenario obligations changed")
	}
	return nil
}

func validateBoxContractQueryCoverage(queries []boxContractQuery) error {
	type oracle struct {
		engine     string
		expected   []string
		prohibited []string
	}
	want := map[string]oracle{
		"notes-current":                      {"notes", []string{"note-p2"}, []string{"note-p1"}},
		"topic-draft":                        {"notes", []string{"topic-p1"}, []string{"accepted-cadence"}},
		"library-native-pdf":                 {"notes", []string{"library-a-p2"}, nil},
		"library-conflicting-claims":         {"notes", []string{"library-a-p1", "library-b-p1"}, []string{"accepted-cadence"}},
		"library-office":                     {"notes", []string{"library-office-p1"}, nil},
		"library-scanned":                    {"notes", []string{"library-scan"}, nil},
		"library-image":                      {"notes", []string{"library-image"}, nil},
		"library-unsupported":                {"objects", []string{"library-binary"}, nil},
		"legacy-project-notes":               {"notes", []string{"legacy-project-note-p1"}, nil},
		"declared-project-doc":               {"notes", []string{"project-doc-p1"}, []string{"undeclared-code-p1"}},
		"declared-project-research":          {"notes", []string{"project-research-p1"}, nil},
		"undeclared-repository":              {"notes", nil, []string{"undeclared-code-p1"}},
		"duplicate-project-isolation":        {"notes", []string{"duplicate-atlas-p1"}, []string{"duplicate-other-p1"}},
		"documents-default":                  {"notes", nil, []string{"documents-default-p1"}},
		"imports-custody":                    {"objects", []string{"imports-default"}, nil},
		"privacy-exclusions":                 {"notes", nil, []string{"private-topic-p1", "credential-p1", "agent-state-p1", "cache-p1", "dependency-p1", "build-p1", "ignored-p1", "link"}},
		"offline-owner":                      {"objects", []string{"offline"}, nil},
		"archive-default":                    {"notes", nil, []string{"archived-p1"}},
		"archive-explicit":                   {"notes", []string{"archived-p1"}, nil},
		"accepted-decision":                  {"provenance", []string{"accepted-cadence"}, []string{"pending-hourly", "topic-p1", "library-a-p1"}},
		"pending-opt-in":                     {"provenance", []string{"accepted-cadence", "pending-hourly"}, nil},
		"historical-citation":                {"notes", []string{"note-p1"}, []string{"note-p2"}},
		"policy-exclusion-removes-passages":  {"notes", nil, []string{"topic-p1"}},
		"permission-change-removes-passages": {"notes", nil, []string{"project-doc-p1"}},
	}
	if len(queries) != len(want) {
		return fmt.Errorf("query coverage count")
	}
	// Routing prompts are not literal lexical requests. Freeze the latter too,
	// so later acceptance cannot select requests after seeing returned results.
	requests := map[string]boxContractRequest{
		"notes-current":                      {"search", "revised", ""},
		"topic-draft":                        {"search", "brainstorm", ""},
		"library-native-pdf":                 {"search", `"write contention"`, ""},
		"library-conflicting-claims":         {"search", "argues", ""},
		"library-office":                     {"search", `"reference outline"`, ""},
		"library-scanned":                    {"search", "scanned", ""},
		"library-image":                      {"search", "diagram", ""},
		"library-unsupported":                {"inspect", "", "library-binary"},
		"legacy-project-notes":               {"search", "legacy", ""},
		"declared-project-doc":               {"search", "architecture", ""},
		"declared-project-research":          {"search", "hypothesis", ""},
		"undeclared-repository":              {"search", "implementation", ""},
		"duplicate-project-isolation":        {"search", `"copied sentence"`, ""},
		"documents-default":                  {"search", "Documents", ""},
		"imports-custody":                    {"inspect", "", "imports-default"},
		"privacy-exclusions":                 {"search", "private SYNTHETIC_CREDENTIAL_MARKER_NOT_A_SECRET SYNTHETIC_RUNTIME_MARKER cached dependency generated loomignore linked", ""},
		"offline-owner":                      {"inspect", "", "offline"},
		"archive-default":                    {"search", `"Archived Atlas"`, ""},
		"archive-explicit":                   {"search", `"Archived Atlas"`, ""},
		"accepted-decision":                  {"search", "decision", ""},
		"pending-opt-in":                     {"search", "decision", ""},
		"historical-citation":                {"exact_get", "", "note-p1"},
		"policy-exclusion-removes-passages":  {"search", "brainstorm", ""},
		"permission-change-removes-passages": {"search", "architecture", ""},
	}
	for _, q := range queries {
		o, ok := want[q.Key]
		if !ok || q.FirstEngine != o.engine || q.Request != requests[q.Key] || !slices.Equal(q.Expected, o.expected) || !slices.Equal(q.Prohibited, o.prohibited) {
			return fmt.Errorf("frozen query oracle: %s", q.Key)
		}
		state, scenario := "current", "baseline"
		switch q.Key {
		case "library-scanned":
			state = "needs_ocr"
		case "library-image":
			state = "needs_image_description"
		case "library-unsupported":
			state = "metadata_only"
		case "offline-owner":
			state = "offline"
		case "archive-explicit":
			state = "archived"
		case "historical-citation":
			state, scenario = "historical", "historical_exact_get"
		case "policy-exclusion-removes-passages":
			scenario = "exclusion-change"
		case "permission-change-removes-passages":
			scenario = "permission-change"
		}
		filters := boxContractFilters{}
		switch q.Key {
		case "topic-draft":
			filters.SourceCategory = "topics"
		case "library-native-pdf", "library-image":
			filters.SourceCategory = "library"
		case "legacy-project-notes", "declared-project-doc", "declared-project-research", "duplicate-project-isolation":
			filters.ProjectKey = "atlas"
		case "offline-owner":
			filters.OwnerNode = "macbook"
		}
		if q.ExpectedState != state || q.Scenario != scenario || q.Filters != filters || q.IncludePending != (q.Key == "pending-opt-in") || q.IncludeArchived != (q.Key == "archive-explicit") || (q.Key != "archive-explicit" && q.Dependency != "") {
			return fmt.Errorf("query posture/filter/opt-in: %s", q.Key)
		}
	}
	return nil
}

func TestBoxSourcesContractRejectsSemanticWeakening(t *testing.T) {
	source := func(c *boxContract, key string) *boxContractSource {
		return &c.Corpus.Sources[slices.IndexFunc(c.Corpus.Sources, func(s boxContractSource) bool { return s.Key == key })]
	}
	query := func(c *boxContract, key string) *boxContractQuery {
		return &c.Queries.Queries[slices.IndexFunc(c.Queries.Queries, func(q boxContractQuery) bool { return q.Key == key })]
	}
	tests := []struct {
		name   string
		mutate func(*boxContract)
	}{
		{"unknown root", func(c *boxContract) { source(c, "note").RootKey = "missing" }},
		{"absolute root", func(c *boxContract) { c.Corpus.Roots[0].RelativePath = "/srv/private" }},
		{"parent traversal", func(c *boxContract) { source(c, "note").RelativePath = "../private.md" }},
		{"windows path", func(c *boxContract) { source(c, "note").RelativePath = `C:\private.md` }},
		{"duplicate root", func(c *boxContract) { c.Corpus.Roots[1] = c.Corpus.Roots[0] }},
		{"wrong root kind", func(c *boxContract) { c.Corpus.Roots[0].RootKind = "box_library" }},
		{"unknown project", func(c *boxContract) { c.Corpus.Roots[3].ProjectKey = "unknown" }},
		{"documents implicitly enabled", func(c *boxContract) { c.Corpus.Roots[6].Registered = true }},
		{"duplicate source", func(c *boxContract) { c.Corpus.Sources[1].Key = "note" }},
		{"foreign current version", func(c *boxContract) { source(c, "note").CurrentVersion = "topic-v1" }},
		{"missing source version", func(c *boxContract) { source(c, "note").Versions = nil }},
		{"draft claims acceptance", func(c *boxContract) { source(c, "topic").Posture = "accepted_decision" }},
		{"claim loses attribution", func(c *boxContract) { source(c, "library-a").Posture = "source_material" }},
		{"project declaration removed", func(c *boxContract) { source(c, "project-doc").Declaration = "" }},
		{"topic context changed", func(c *boxContract) { source(c, "topic").TopicKey = "other" }},
		{"link admitted", func(c *boxContract) { source(c, "link").Selection = "eligible" }},
		{"private source admitted", func(c *boxContract) { source(c, "private-topic").Selection = "eligible" }},
		{"scanned source mislabeled", func(c *boxContract) { source(c, "library-scan").Extraction = "metadata_only" }},
		{"fabricated scanned text", func(c *boxContract) { source(c, "library-scan").Versions[0].FixtureText = "fabricated OCR" }},
		{"invalid PDF page", func(c *boxContract) { source(c, "library-a").Versions[0].Passages[1].Locator.Page = 0 }},
		{"unbound quotation", func(c *boxContract) { source(c, "library-a").Versions[0].Passages[1].Text = "not in this source" }},
		{"duplicate bytes changed", func(c *boxContract) { source(c, "duplicate-other").Versions[0].FixtureText = "different" }},
		{"semantic evidence rebound", func(c *boxContract) { c.Corpus.SemanticRecords[0].SourceVersion = "note-v2" }},
		{"candidate promoted", func(c *boxContract) {
			c.Corpus.SemanticRecords[1].Collection = "accepted_records"
			c.Corpus.SemanticRecords[1].Posture = "accepted_decision"
		}},
		{"missing case", func(c *boxContract) { c.Queries.Queries = c.Queries.Queries[1:] }},
		{"empty weakened expectation", func(c *boxContract) { query(c, "topic-draft").Expected = []string{} }},
		{"privacy assertion removed", func(c *boxContract) { query(c, "privacy-exclusions").Prohibited = []string{} }},
		{"unknown expected ID", func(c *boxContract) { query(c, "topic-draft").Expected = []string{"missing-passage"} }},
		{"cross project contamination", func(c *boxContract) {
			query(c, "duplicate-project-isolation").Expected = []string{"duplicate-other-p1"}
			query(c, "duplicate-project-isolation").Prohibited = []string{}
		}},
		{"filter weakened", func(c *boxContract) { query(c, "duplicate-project-isolation").Filters.ProjectKey = "" }},
		{"stale current result", func(c *boxContract) {
			query(c, "notes-current").Expected = []string{"note-p1"}
			query(c, "notes-current").Prohibited = []string{}
		}},
		{"wrong first engine", func(c *boxContract) { query(c, "accepted-decision").FirstEngine = "notes" }},
		{"routing prompt used as lexical request", func(c *boxContract) { query(c, "notes-current").Request.Query = query(c, "notes-current").Text }},
		{"historical request rebound", func(c *boxContract) { query(c, "historical-citation").Request.Target = "note-p2" }},
		{"pending becomes default", func(c *boxContract) { query(c, "accepted-decision").IncludePending = true }},
		{"archive dependency removed", func(c *boxContract) { query(c, "archive-explicit").Dependency = "" }},
		{"historical citation replaced", func(c *boxContract) {
			query(c, "historical-citation").Expected = []string{"note-p2"}
			query(c, "historical-citation").Prohibited = []string{}
		}},
		{"privacy policy removed", func(c *boxContract) { c.Policies.Exclusions = c.Policies.Exclusions[1:] }},
		{"automatic promotion", func(c *boxContract) { c.Policies.Authority.Promote = true }},
		{"automatic supersession", func(c *boxContract) { c.Policies.Authority.Supersede = true }},
		{"remote crawl", func(c *boxContract) { c.Policies.Authority.RemoteCrawl = true }},
		{"heavy stage activated", func(c *boxContract) { c.Policies.HeavyPolicy.PDFOCR = true }},
		{"manual seed substituted", func(c *boxContract) { c.Policies.Acceptance.ManualKnowledgeSeed = true }},
		{"extracted text used as source hash", func(c *boxContract) { c.Policies.Acceptance.FixtureHashContract = "hash_extracted_text" }},
		{"runtime IDs preseeded", func(c *boxContract) { c.Policies.Acceptance.RuntimeIdentityBinding = "preseeded_knowledge_ids" }},
		{"citation hash omitted", func(c *boxContract) { c.Policies.CitationFields = c.Policies.CitationFields[1:] }},
		{"ingestion metric omitted", func(c *boxContract) {
			c.Policies.Acceptance.LatencyMeasurements = c.Policies.Acceptance.LatencyMeasurements[1:]
		}},
		{"observation sold as SLA", func(c *boxContract) { c.Policies.Acceptance.BaselineTimingsAreNotSLA = false }},
		{"unchanged extraction repeated", func(c *boxContract) { c.Policies.Scenarios[1].ExtractionJobs = 1 }},
		{"restart obligations omitted", func(c *boxContract) { c.Policies.Scenarios[10].Required = []string{} }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := loadBoxSourcesContract(t)
			tt.mutate(&c)
			if err := validateBoxSourcesContract(c); err == nil {
				t.Fatal("weakened contract was accepted")
			}
		})
	}
}

func TestBoxSourcesContractRequiresCompleteJSONShape(t *testing.T) {
	tests := []struct {
		name string
		file string
		edit func(map[string]any)
	}{
		{"missing false policy", "policies", func(x map[string]any) { delete(x["authority"].(map[string]any), "semantic_promotion_on_ingest") }},
		{"unknown policy", "policies", func(x map[string]any) { x["authority"].(map[string]any)["extra"] = false }},
		{"missing nullable-looking context", "queries", func(x map[string]any) {
			delete(x["queries"].([]any)[0].(map[string]any)["filters"].(map[string]any), "project_key")
		}},
		{"null empty list", "queries", func(x map[string]any) { x["queries"].([]any)[2].(map[string]any)["prohibited"] = nil }},
		{"missing nested locator", "corpus", func(x map[string]any) {
			delete(x["sources"].([]any)[0].(map[string]any)["versions"].([]any)[0].(map[string]any)["passages"].([]any)[0].(map[string]any)["locator"].(map[string]any), "page")
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b, err := boxSourcesContractFS.ReadFile("testdata/box_sources/" + tt.file + ".json")
			if err != nil {
				t.Fatal(err)
			}
			var raw map[string]any
			if err := json.Unmarshal(b, &raw); err != nil {
				t.Fatal(err)
			}
			tt.edit(raw)
			b, err = json.Marshal(raw)
			if err != nil {
				t.Fatal(err)
			}
			var target any
			switch tt.file {
			case "corpus":
				target = &boxContractCorpus{}
			case "queries":
				target = &boxContractQueries{}
			case "policies":
				target = &boxContractPolicies{}
			}
			if err := decodeBoxSourcesContract(b, target); err == nil {
				t.Fatal("incomplete JSON shape was accepted")
			}
		})
	}
	for _, input := range []string{`{"a":1,"a":2}`, `{"a":{"b":1,"b":2}}`, `{"a":1} {"a":2}`, `{"a":null}`} {
		var target map[string]any
		if err := decodeBoxSourcesContract([]byte(input), &target); err == nil {
			t.Fatalf("malformed JSON accepted: %s", input)
		}
	}
}

func TestBoxSourcesContractFrozenFiles(t *testing.T) {
	// Later product work must satisfy this corpus, not change its expectations.
	want := map[string]string{
		"corpus":   "db0e209fa02d15a56ec60c9293c86893587ebde16dbb7938ef2ca11be27941f0",
		"queries":  "c40cb08c8c387683838b611ae9d03b77813ae3ce0de1202fbe180ee9db7b7cca",
		"policies": "493bd5cddfb8da071dddcd2f2afba5dbed8b5768ad4451df6d413d7ccce650c3",
	}
	for name, digest := range want {
		b, err := boxSourcesContractFS.ReadFile("testdata/box_sources/" + name + ".json")
		if err != nil {
			t.Fatal(err)
		}
		if got := fmt.Sprintf("%x", sha256.Sum256(b)); got != digest {
			t.Errorf("%s frozen digest: got %s want %s", name, got, digest)
		}
	}
}
