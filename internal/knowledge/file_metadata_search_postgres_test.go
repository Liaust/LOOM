package knowledge

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/pressly/goose/v3"

	"loom.local/loom/internal/migrations"
)

func TestFileMetadataSearchIndexUpgradePostgres(t *testing.T) {
	db, url := boxSourcesDatabase(t)
	dir := filepath.Join("..", "..", "migrations")
	if err := goose.SetDialect("postgres"); err != nil {
		t.Fatal(err)
	}
	if err := goose.UpToContext(t.Context(), db, dir, 114); err != nil {
		t.Fatal(err)
	}
	fixtures := []struct {
		name, presence, policy, metadata string
		path                             any
	}{
		{"cobalt-map.md", "present", "metadata_only", `{}`, "/notes/cobalt-map.md"},
		{"cobalt-null.md", "present", "metadata_only", `{}`, nil},
		{"cobalt-missing.md", "missing", "metadata_only", `{}`, "/notes/cobalt-missing.md"},
		{"cobalt-unavailable.md", "unavailable", "metadata_only", `{}`, "/notes/cobalt-unavailable.md"},
		{"cobalt-unobserved.md", "unobserved", "metadata_only", `{}`, "/notes/cobalt-unobserved.md"},
		{"cobalt-private.md", "present", "private_no_index", `{}`, "/notes/cobalt-private.md"},
		{"cobalt-flag.md", "present", "metadata_only", `{"private_no_index":true}`, "/notes/cobalt-flag.md"},
		{"cobalt-policy.md", "present", "metadata_only", `{"index_policy":"private_no_index"}`, "/notes/cobalt-policy.md"},
		{"Étude niño.md", "present", "metadata_only", `{}`, "/notes/Português/Étude niño.md"},
		{"hello-world.md", "present", "metadata_only", `{}`, "/notes/two_words/hello-world.md"},
		{"cobalt-atlas.md", "present", "none", `{}`, "/notes/cobalt-atlas.md"},
	}
	for i, f := range fixtures {
		id := fmt.Sprintf("object_metadata_search_%02d", i)
		if _, err := db.ExecContext(t.Context(), `INSERT INTO objects.objects(object_id,object_type,name,state_class,status)
 VALUES($1,'file',$2,'canonical','active')`, id, f.name); err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(t.Context(), `INSERT INTO files.file_metadata
 (object_id,logical_name,managed_source_path,source_presence,index_policy,metadata) VALUES($1,$2,$3,$4,$5,$6::jsonb)`,
			id, f.name, f.path, f.presence, f.policy, f.metadata); err != nil {
			t.Fatal(err)
		}
	}
	// Use exactly the live query expression and file-level eligibility predicates.
	const query = `SELECT f.object_id FROM files.file_metadata f
 WHERE f.source_presence='present' AND f.index_policy IS DISTINCT FROM 'private_no_index'
 AND f.metadata->>'private_no_index' IS DISTINCT FROM 'true'
 AND f.metadata->>'index_policy' IS DISTINCT FROM 'private_no_index'
 AND to_tsvector('simple',regexp_replace(f.logical_name||' '||f.managed_source_path,'[[:punct:]]',' ','g'))
 @@ websearch_to_tsquery('simple',$1) ORDER BY f.object_id`
	search := func(text string) []string {
		t.Helper()
		rows, err := db.QueryContext(t.Context(), query, text)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		ids := []string{}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				t.Fatal(err)
			}
			ids = append(ids, id)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		return ids
	}
	queries := []string{"cobalt", `"two words"`, "hello-world", "niño", "Étude", "atlas OR cobalt", "cobalt -map", "zzqyxvplrtsnonexistent"}
	collect := func() map[string][]string {
		out := map[string][]string{}
		for _, text := range queries {
			out[text] = search(text)
		}
		return out
	}
	before := collect()
	if !reflect.DeepEqual(before["cobalt"], []string{"object_metadata_search_00", "object_metadata_search_10"}) || len(before["zzqyxvplrtsnonexistent"]) != 0 {
		t.Fatalf("bad baseline or privacy/presence/NULL semantics: %+v", before)
	}
	if _, err := migrations.Up(t.Context(), url, dir); err != nil {
		t.Fatal(err)
	}
	if after := collect(); !reflect.DeepEqual(before, after) {
		t.Fatalf("index changed query identities: before=%+v after=%+v", before, after)
	}
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	// Tiny fixtures normally favour a sequential scan. Verify index eligibility
	// here; actual production acceptance must use the normal planner settings.
	if _, err := tx.ExecContext(t.Context(), "SET LOCAL enable_seqscan=off"); err != nil {
		t.Fatal(err)
	}
	var raw []byte
	if err := tx.QueryRowContext(t.Context(), "EXPLAIN (FORMAT JSON) "+query, "cobalt").Scan(&raw); err != nil {
		t.Fatal(err)
	}
	type planNode struct {
		Index string     `json:"Index Name"`
		Plans []planNode `json:"Plans"`
	}
	var plans []struct{ Plan planNode }
	if err := json.Unmarshal(raw, &plans); err != nil || len(plans) != 1 {
		t.Fatalf("invalid query plan: %s %v", raw, err)
	}
	var usesIndex func(planNode) bool
	usesIndex = func(p planNode) bool {
		if p.Index == "files_file_metadata_search_idx" {
			return true
		}
		for _, child := range p.Plans {
			if usesIndex(child) {
				return true
			}
		}
		return false
	}
	if !usesIndex(plans[0].Plan) {
		t.Fatalf("live expression/predicate cannot use metadata index: %s", raw)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	check := func(update string, want int) {
		t.Helper()
		if _, err := db.ExecContext(t.Context(), update+" WHERE object_id='object_metadata_search_00'"); err != nil {
			t.Fatal(err)
		}
		if got := search("unicorn"); len(got) != want {
			t.Fatalf("metadata mutation left stale index results: got=%v want=%d", got, want)
		}
	}
	check(`UPDATE files.file_metadata SET logical_name='renamed-unicorn.md',managed_source_path='/notes/renamed-unicorn.md'`, 1)
	if got := search("cobalt"); !reflect.DeepEqual(got, []string{"object_metadata_search_10"}) {
		t.Fatalf("rename retained old filename terms: %v", got)
	}
	check(`UPDATE files.file_metadata SET source_presence='missing'`, 0)
	check(`UPDATE files.file_metadata SET source_presence='present',index_policy='private_no_index'`, 0)
	check(`UPDATE files.file_metadata SET index_policy='metadata_only',metadata='{"private_no_index":true}'`, 0)
	check(`UPDATE files.file_metadata SET metadata='{"index_policy":"private_no_index"}'`, 0)
	check(`UPDATE files.file_metadata SET metadata='{}'`, 1)
	current := collect()
	if err := goose.DownContext(t.Context(), db, dir); err != nil {
		t.Fatal(err)
	}
	if got := collect(); !reflect.DeepEqual(got, current) {
		t.Fatalf("dropping the derived index changed retained catalog results: %+v", got)
	}
	if _, err := migrations.Up(t.Context(), url, dir); err != nil {
		t.Fatal(err)
	}
	if got := collect(); !reflect.DeepEqual(got, current) {
		t.Fatalf("reapplying the index changed retained catalog results: %+v", got)
	}
	var files, versions, documents int
	if err := db.QueryRowContext(t.Context(), `SELECT (SELECT count(*) FROM files.file_metadata),
 (SELECT count(*) FROM objects.object_versions),(SELECT count(*) FROM search.search_documents)`).Scan(&files, &versions, &documents); err != nil || files != len(fixtures) || versions != 0 || documents != 0 {
		t.Fatalf("index migration changed data or created body processing: files=%d versions=%d documents=%d err=%v", files, versions, documents, err)
	}
}
