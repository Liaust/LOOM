package knowledge

import (
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"runtime"
	"slices"
	"testing"
	"time"
	"unicode/utf8"

	"golang.org/x/sys/unix"
)

const boxPerformanceWarmups, boxPerformanceSamples = 5, 50

type boxPerformanceCase struct {
	Key      string
	Input    NotesSearchInput
	Expected NotesSearchResultSet
}

type boxPerformance struct {
	current map[string][]time.Duration
}

func newBoxPerformance(t *testing.T) *boxPerformance {
	t.Helper()
	if os.Getenv("LOOM_BOX_PERFORMANCE") == "" {
		return nil
	}
	if os.Getenv("LOOM_BOX_PERFORMANCE") != "1" {
		t.Fatal("LOOM_BOX_PERFORMANCE must be exactly 1 when enabled")
	}
	for _, key := range []string{"LOOM_TEST_DB_URL", "LOOM_BOX_SURFACE_HELPER", "LOOM_BOX_SURFACE_CLI"} {
		if os.Getenv(key) == "" {
			t.Fatalf("performance acceptance requires %s; refusing partial/skip evidence", key)
		}
	}
	return &boxPerformance{}
}

func (p *boxPerformance) observeSurface(surface string, duration time.Duration) {
	if p != nil && p.current != nil {
		p.current[surface] = append(p.current[surface], duration)
	}
}

// Exclude volatile ranking/observation times, but retain content identity,
// exact navigation, body preview, extraction state and qualified source context.
func boxPerformanceIdentity(set NotesSearchResultSet) string {
	items := make([]any, 0, len(set.Results))
	for _, hit := range set.Results {
		items = append(items, []any{hit.KnowledgeObjectID, hit.KnowledgeObjectVersionID,
			hit.KnowledgeChunkID, hit.StructuralPath, hit.Citation, hit.SourceContext,
			hit.MetadataOnly, hit.ExtractionStatus, hit.SourceKind, hit.RootKind,
			hit.SourceNodeKey, hit.ProjectID, hit.RelativePath, hit.Snippet})
	}
	raw, err := json.Marshal([]any{set.ResultCount, items})
	if err != nil {
		panic(err)
	}
	return fmt.Sprintf("%x", sha256.Sum256(raw))
}

func boxPerformancePercentile(samples []time.Duration, percentile int) (time.Duration, error) {
	if len(samples) == 0 || percentile < 1 || percentile > 100 {
		return 0, fmt.Errorf("invalid percentile sample set")
	}
	ordered := slices.Clone(samples)
	for _, sample := range ordered {
		if sample <= 0 {
			return 0, fmt.Errorf("invalid non-positive duration")
		}
	}
	slices.Sort(ordered)
	return ordered[(len(ordered)*percentile+99)/100-1], nil
}

type boxPerformanceCounts struct {
	Versions      int64 `json:"versions"`
	Runs          int64 `json:"runs"`
	Artifacts     int64 `json:"artifacts"`
	Chunks        int64 `json:"chunks"`
	ArtifactBytes int64 `json:"artifact_text_bytes"`
	Attempts      int64 `json:"stage_attempts"`
}

func boxPerformanceReadCounts(t *testing.T, db *sql.DB) boxPerformanceCounts {
	t.Helper()
	var counts boxPerformanceCounts
	err := db.QueryRowContext(t.Context(), `SELECT
		(SELECT count(*) FROM knowledge.knowledge_object_versions),
		(SELECT count(*) FROM knowledge.pipeline_runs),
		(SELECT count(*) FROM knowledge.derived_artifacts),
		(SELECT count(*) FROM knowledge.knowledge_chunks),
		(SELECT COALESCE(sum(octet_length(text_content)),0) FROM knowledge.derived_artifacts),
		(SELECT COALESCE(sum(attempt_count),0) FROM knowledge.pipeline_stage_runs)`).Scan(
		&counts.Versions, &counts.Runs, &counts.Artifacts, &counts.Chunks, &counts.ArtifactBytes, &counts.Attempts)
	if err != nil {
		t.Fatal(err)
	}
	return counts
}

type boxPerformanceResources struct {
	CPU             time.Duration
	DriverPeakBytes int64 `json:"driver_lifetime_peak_rss_bytes"`
	ChildPeakBytes  int64 `json:"reaped_child_peak_rss_bytes"`
}

func boxPerformanceRSSBytes(goos string, value int64) (int64, error) {
	switch goos {
	case "darwin":
		return value, nil
	case "linux":
		return value * 1024, nil
	default:
		return 0, fmt.Errorf("unsupported getrusage RSS units on %s", goos)
	}
}

func boxPerformanceUsage(t *testing.T) boxPerformanceResources {
	t.Helper()
	var result boxPerformanceResources
	for _, who := range []int{unix.RUSAGE_SELF, unix.RUSAGE_CHILDREN} {
		var usage unix.Rusage
		if err := unix.Getrusage(who, &usage); err != nil {
			t.Fatal(err)
		}
		result.CPU += time.Duration(usage.Utime.Sec+usage.Stime.Sec)*time.Second +
			time.Duration(usage.Utime.Usec+usage.Stime.Usec)*time.Microsecond
		peak, err := boxPerformanceRSSBytes(runtime.GOOS, usage.Maxrss)
		if err != nil {
			t.Fatal(err)
		}
		if who == unix.RUSAGE_SELF {
			result.DriverPeakBytes = peak
		} else {
			result.ChildPeakBytes = peak
		}
	}
	return result
}

func boxPerformanceLog(t *testing.T, kind string, value any) {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("BOX_PERFORMANCE_%s %s", kind, raw)
}

func (p *boxPerformance) measureWork(t *testing.T, db *sql.DB, phase string, advance func() int64) int64 {
	t.Helper()
	if p == nil {
		return advance()
	}
	before, usage := boxPerformanceReadCounts(t, db), boxPerformanceUsage(t)
	start := time.Now()
	jobs := advance()
	duration := time.Since(start)
	afterUsage := boxPerformanceUsage(t)
	boxPerformanceLog(t, "WORK", map[string]any{
		"phase": phase, "stage_jobs": jobs, "wall_ms": float64(duration) / float64(time.Millisecond),
		"driver_reaped_children_cpu_ms": float64(afterUsage.CPU-usage.CPU) / float64(time.Millisecond),
		"before":                        before, "after": boxPerformanceReadCounts(t, db),
		"driver_lifetime_peak_rss_bytes": afterUsage.DriverPeakBytes, "reaped_child_peak_rss_bytes": afterUsage.ChildPeakBytes,
	})
	return jobs
}

func (p *boxPerformance) measureQueries(t *testing.T, db *sql.DB, service *Service, phase string,
	cases []boxPerformanceCase, surfaces func(NotesSearchInput, NotesSearchResultSet)) {
	t.Helper()
	if p == nil {
		return
	}
	if len(cases) != 12 || surfaces == nil {
		t.Fatal("requires all twelve frozen native queries and both supported surfaces")
	}
	before, usage := boxPerformanceReadCounts(t, db), boxPerformanceUsage(t)
	timings := make([]map[string][]time.Duration, len(cases))
	maxBytes, maxCodepoints := make([]int, len(cases)), make([]int, len(cases))
	for i := range timings {
		timings[i] = map[string][]time.Duration{}
	}
	start := time.Now()
	for sample := -boxPerformanceWarmups; sample < boxPerformanceSamples; sample++ {
		for i, query := range cases {
			if sample >= 0 {
				p.current = timings[i]
			}
			began := time.Now()
			result, err := service.SearchNotes(t.Context(), query.Input)
			elapsed := time.Since(began)
			if err != nil || result.ResultCount != len(result.Results) || boxPerformanceIdentity(result) != boxPerformanceIdentity(query.Expected) {
				t.Fatalf("%s/%s sample %d changed exact result/count: %v", phase, query.Key, sample, err)
			}
			p.observeSurface("service", elapsed)
			surfaces(query.Input, result)
			if sample >= 0 {
				raw, err := json.Marshal(result)
				if err != nil {
					t.Fatal(err)
				}
				maxBytes[i] = max(maxBytes[i], len(raw))
				maxCodepoints[i] = max(maxCodepoints[i], utf8.RuneCount(raw))
			}
			p.current = nil
		}
	}
	duration, afterUsage := time.Since(start), boxPerformanceUsage(t)
	after := boxPerformanceReadCounts(t, db)
	if before != after {
		t.Fatalf("search measurement changed derived work: %+v -> %+v", before, after)
	}
	reports := make([]map[string]any, 0, len(cases))
	for i, query := range cases {
		report := map[string]any{"query": query.Key, "first_engine": "notes", "result_count": query.Expected.ResultCount,
			"corpus_missing_count": 0, "max_response_bytes": maxBytes[i], "max_response_codepoints": maxCodepoints[i]}
		for _, surface := range []string{"service", "http", "cli"} {
			values := timings[i][surface]
			if len(values) != boxPerformanceSamples {
				t.Fatalf("%s/%s has %d samples", query.Key, surface, len(values))
			}
			var stats []float64
			for _, percentile := range []int{50, 95, 100} {
				value, err := boxPerformancePercentile(values, percentile)
				if err != nil {
					t.Fatal(err)
				}
				stats = append(stats, float64(value)/float64(time.Millisecond))
			}
			report[surface+"_p50_p95_max_ms"] = stats
		}
		reports = append(reports, report)
	}
	boxPerformanceLog(t, "QUERIES", map[string]any{
		"phase": phase, "warmups": boxPerformanceWarmups, "samples_per_query_surface": boxPerformanceSamples,
		"wall_ms":                        float64(duration) / float64(time.Millisecond),
		"driver_reaped_children_cpu_ms":  float64(afterUsage.CPU-usage.CPU) / float64(time.Millisecond),
		"driver_lifetime_peak_rss_bytes": afterUsage.DriverPeakBytes, "reaped_child_peak_rss_bytes": afterUsage.ChildPeakBytes,
		"counts": after, "api_truncation_field_available": false, "queries": reports,
	})
}

func TestBoxPerformanceAccounting(t *testing.T) {
	values := []time.Duration{50, 1, 49, 2, 48, 3, 47, 4, 46, 5}
	before := slices.Clone(values)
	for percentile, want := range map[int]time.Duration{50: 5, 95: 50, 100: 50} {
		if got, err := boxPerformancePercentile(values, percentile); err != nil || got != want {
			t.Fatalf("p%d=%s, want %s: %v", percentile, got, want, err)
		}
	}
	if !slices.Equal(before, values) {
		t.Fatal("percentile reordered caller samples")
	}
	for _, input := range []struct {
		samples    []time.Duration
		percentile int
	}{
		{nil, 95}, {[]time.Duration{0}, 95}, {[]time.Duration{-1}, 95}, {values, 0}, {values, 101},
	} {
		if _, err := boxPerformancePercentile(input.samples, input.percentile); err == nil {
			t.Fatal("invalid sample set accepted")
		}
	}
	for platform, want := range map[string]int64{"darwin": 10, "linux": 10240} {
		if got, err := boxPerformanceRSSBytes(platform, 10); err != nil || got != want {
			t.Fatalf("RSS %s=%d %v", platform, got, err)
		}
	}
	if _, err := boxPerformanceRSSBytes("unknown", 1); err == nil {
		t.Fatal("unknown RSS units accepted")
	}
}

func TestBoxPerformanceIdentity(t *testing.T) {
	base := NotesSearchResultSet{ResultCount: 1, Results: []NotesSearchResult{{
		KnowledgeObjectID: "object", KnowledgeObjectVersionID: "version", KnowledgeChunkID: "chunk",
		Snippet: "source text", Citation: NotesSearchCitation{SourceRef: "exact"}}}}
	for _, mutate := range []func(*NotesSearchResultSet){
		func(x *NotesSearchResultSet) { x.ResultCount++ },
		func(x *NotesSearchResultSet) { x.Results[0].KnowledgeObjectVersionID = "other" },
		func(x *NotesSearchResultSet) { x.Results[0].Citation.SourceRef = "other" },
		func(x *NotesSearchResultSet) { x.Results[0].MetadataOnly = true },
		func(x *NotesSearchResultSet) { x.Results[0].Snippet = "other" },
		func(x *NotesSearchResultSet) { x.Results[0].SourcePosture = "other" },
	} {
		changed := base
		changed.Results = slices.Clone(base.Results)
		mutate(&changed)
		if reflect.DeepEqual(changed, base) || boxPerformanceIdentity(changed) == boxPerformanceIdentity(base) {
			t.Fatal("changed evidence matched benchmark identity")
		}
	}
	changed := base
	changed.Results = slices.Clone(base.Results)
	changed.Results[0].ObservedAt = time.Now()
	changed.Results[0].RankScore = 0.5
	if boxPerformanceIdentity(changed) != boxPerformanceIdentity(base) {
		t.Fatal("volatile observations changed content identity")
	}
}
