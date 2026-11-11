package workspacenotes

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"strings"
	"time"

	"loom.local/loom/internal/workspacefiles"
)

const maxMarkdownBytes = 4 << 20
const metadataVersion = 1

// Document identity survives a path move; a newly created file at the old path
// must not reuse the moved document's primary key.
func newDocumentID() string { return "workspace_note_" + rand.Text() }

type Service struct {
	DB    *sql.DB
	Files *workspacefiles.Service
	// Optional verified source/owning-project membership for dynamic roots. This
	// must never return the caller's default Box merely because a root is unknown.
	RootBoxID func(context.Context, string) (string, error)
}
type Document struct {
	ID              string         `json:"id"`
	Root            string         `json:"root"`
	Path            string         `json:"path"`
	Revision        string         `json:"revision"`
	Aliases         []string       `json:"aliases"`
	Tags            []string       `json:"tags"`
	Properties      []string       `json:"properties"`
	Values          map[string]any `json:"values"`
	MetadataVersion int            `json:"metadata_version"`
	ObservedAt      *time.Time     `json:"observed_at,omitempty"`
}

func (d Document) current(revision string) bool {
	return d.ObservedAt != nil && d.Revision == revision && d.MetadataVersion == metadataVersion
}

type Item struct {
	Root      string `json:"root"`
	Path      string `json:"path"`
	Name      string `json:"name"`
	Kind      string `json:"kind"`
	Reference string `json:"reference,omitempty"`
	Heading   string `json:"heading,omitempty"`
}
type Graph struct {
	Document  Document    `json:"document"`
	Forward   []Reference `json:"forward"`
	Backlinks []Item      `json:"backlinks"`
	Coverage  string      `json:"coverage"`
	Truncated bool        `json:"truncated"`
	Warnings  []string    `json:"warnings"`
}
type queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func decodePropertyValues(raw []byte, target *map[string]any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if *target == nil {
		*target = map[string]any{}
	}
	return nil
}

func readDocument(ctx context.Context, q queryer, root, relative string) (Document, error) {
	d := Document{Root: root, Path: relative}
	var aliases, tags, properties, values []byte
	err := q.QueryRowContext(ctx, `SELECT document_id,source_revision,aliases,tags,properties,property_values,metadata_version,observed_at FROM knowledge.workspace_note_documents WHERE root_ref=$1 AND path=$2 AND NOT retired`, root, relative).Scan(&d.ID, &d.Revision, &aliases, &tags, &properties, &values, &d.MetadataVersion, &d.ObservedAt)
	if err == nil {
		if json.Unmarshal(aliases, &d.Aliases) != nil || json.Unmarshal(tags, &d.Tags) != nil || json.Unmarshal(properties, &d.Properties) != nil || decodePropertyValues(values, &d.Values) != nil {
			return d, fmt.Errorf("invalid graph metadata")
		}
	}
	return d, err
}
func namesFor(d Document) []map[string]string {
	rows := []map[string]string{}
	add := func(kind string, names []string) {
		for _, name := range unique(names) {
			rows = append(rows, map[string]string{"kind": kind, "name": name, "normalized": strings.ToLower(name)})
		}
	}
	add("link", append([]string{stem(d.Path), strings.TrimSuffix(d.Path, path.Ext(d.Path))}, d.Aliases...))
	add("tag", d.Tags)
	add("property", d.Properties)
	return rows
}
func (s Service) writeNames(ctx context.Context, tx *sql.Tx, d Document) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM knowledge.workspace_note_names WHERE document_id=$1`, d.ID); err != nil {
		return err
	}
	raw, _ := json.Marshal(namesFor(d))
	_, err := tx.ExecContext(ctx, `INSERT INTO knowledge.workspace_note_names(document_id,root_ref,kind,name,normalized) SELECT $1,$2,v.kind,v.name,v.normalized FROM jsonb_to_recordset($3::jsonb) AS v(kind text,name text,normalized text) ON CONFLICT DO NOTHING`, d.ID, d.Root, string(raw))
	return err
}

// Register records names from an already-requested directory page. It never
// traverses children or reads Markdown bodies just to populate completion.
func (s Service) Register(ctx context.Context, root string, entries []workspacefiles.Entry) error {
	if err := s.checkRoot(ctx, root); err != nil {
		return err
	}
	values := []map[string]string{}
	for _, entry := range entries {
		if entry.Kind == "file" && notePath(entry.RelativePath) {
			values = append(values, map[string]string{"id": newDocumentID(), "path": entry.RelativePath})
		}
	}
	if len(values) == 0 {
		return nil
	}
	raw, _ := json.Marshal(values)
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `INSERT INTO knowledge.workspace_note_documents(document_id,root_ref,path) SELECT v.id,$1,v.path FROM jsonb_to_recordset($2::jsonb) v(id text,path text) ON CONFLICT(root_ref,path) DO UPDATE SET retired=false,observed_at=NULL,source_revision='',knowledge_object_id=NULL,aliases='[]',tags='[]',properties='[]',property_values='{}',metadata_version=0 WHERE knowledge.workspace_note_documents.retired RETURNING document_id,path`, root, string(raw))
	if err != nil {
		return err
	}
	ids := []string{}
	names := []map[string]string{}
	for rows.Next() {
		d := Document{Root: root}
		if err = rows.Scan(&d.ID, &d.Path); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, d.ID)
		for _, name := range namesFor(d) {
			name["id"] = d.ID
			names = append(names, name)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if len(ids) > 0 {
		keys, _ := json.Marshal(ids)
		if _, err = tx.ExecContext(ctx, `DELETE FROM knowledge.workspace_note_names WHERE document_id IN(SELECT jsonb_array_elements_text($1::jsonb))`, string(keys)); err != nil {
			return err
		}
		data, _ := json.Marshal(names)
		if _, err = tx.ExecContext(ctx, `INSERT INTO knowledge.workspace_note_names(document_id,root_ref,kind,name,normalized) SELECT v.id,$1,v.kind,v.name,v.normalized FROM jsonb_to_recordset($2::jsonb) v(id text,kind text,name text,normalized text) ON CONFLICT DO NOTHING`, root, string(data)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s Service) TagFiles(ctx context.Context, root, tag string) ([]Item, error) {
	if err := s.checkRoot(ctx, root); err != nil {
		return nil, err
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT DISTINCT d.path FROM knowledge.workspace_note_names n JOIN knowledge.workspace_note_documents d USING(document_id) WHERE n.root_ref=$1 AND n.kind='tag' AND n.normalized=$2 AND NOT d.retired ORDER BY d.path LIMIT 101`, root, strings.ToLower(strings.TrimPrefix(tag, "#")))
	if err != nil {
		return nil, err
	}
	paths := []string{}
	for rows.Next() {
		var value string
		if err = rows.Scan(&value); err != nil {
			rows.Close()
			return nil, err
		}
		paths = append(paths, value)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	result := []Item{}
	for _, relative := range paths[:min(len(paths), 100)] {
		d, e := s.Observe(ctx, root, relative)
		if e != nil {
			continue
		}
		for _, value := range d.Tags {
			if strings.EqualFold(value, strings.TrimPrefix(tag, "#")) {
				result = append(result, Item{Root: root, Path: relative, Name: path.Base(relative), Kind: "tag"})
				break
			}
		}
	}
	return result, nil
}

// Observe reads only one permitted original; unchanged revisions do no content
// work. This derived metadata never authorizes or repeats a source mutation.
func (s Service) Observe(ctx context.Context, root, relative string) (Document, error) {
	if err := s.checkRoot(ctx, root); err != nil {
		return Document{}, err
	}
	metadata, err := s.Files.Stat(ctx, root, relative)
	// A failed read alone cannot distinguish a missing file from a replaced or
	// unavailable root. Retirement belongs to a source mutation or pinned refresh.
	if err != nil {
		return Document{}, err
	}
	if metadata.Kind != "file" {
		return Document{}, workspacefiles.ErrNotRegular
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return Document{}, err
	}
	defer tx.Rollback()
	id := newDocumentID()
	if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, documentID(root, relative)); err != nil {
		return Document{}, err
	}
	old, lookup := readDocument(ctx, tx, root, relative)
	if lookup == nil && old.current(metadata.Revision) {
		return old, tx.Commit()
	}
	if lookup != nil && !errors.Is(lookup, sql.ErrNoRows) {
		return Document{}, lookup
	}
	m := propertyMetadata(nil)
	hash := ""
	if notePath(relative) {
		if metadata.Size > maxMarkdownBytes {
			return Document{}, fmt.Errorf("Markdown link parsing is limited to 4 MiB; original content remains available")
		}
		file, before, e := s.Files.Open(ctx, root, relative)
		if e != nil {
			return Document{}, e
		}
		raw, e := io.ReadAll(io.LimitReader(file, maxMarkdownBytes+1))
		_ = file.Close()
		if e != nil {
			return Document{}, e
		}
		after, e := s.Files.Stat(ctx, root, relative)
		if e != nil {
			return Document{}, e
		}
		if before.Revision != after.Revision || int64(len(raw)) != before.Size || len(raw) > maxMarkdownBytes {
			return Document{}, fmt.Errorf("note changed while reading navigation metadata")
		}
		metadata = after
		m = Parse(raw)
		sum := sha256.Sum256(raw)
		hash = hex.EncodeToString(sum[:])
	}
	d := Document{ID: id, Root: root, Path: relative, Revision: metadata.Revision, Aliases: m.Aliases, Tags: m.Tags, Properties: m.Properties, Values: m.Values, MetadataVersion: metadataVersion}
	a, _ := json.Marshal(d.Aliases)
	t, _ := json.Marshal(d.Tags)
	p, _ := json.Marshal(d.Properties)
	v, _ := json.Marshal(d.Values)
	err = tx.QueryRowContext(ctx, `INSERT INTO knowledge.workspace_note_documents(document_id,root_ref,path,source_revision,source_hash,aliases,tags,properties,property_values,metadata_version,observed_at) VALUES($1,$2,$3,$4,$5,$6::jsonb,$7::jsonb,$8::jsonb,$9::jsonb,$10,now()) ON CONFLICT(root_ref,path) DO UPDATE SET source_revision=excluded.source_revision,source_hash=excluded.source_hash,aliases=excluded.aliases,tags=excluded.tags,properties=excluded.properties,property_values=excluded.property_values,metadata_version=excluded.metadata_version,observed_at=now(),retired=false RETURNING document_id,observed_at`, id, root, relative, d.Revision, hash, string(a), string(t), string(p), string(v), d.MetadataVersion).Scan(&d.ID, &d.ObservedAt)
	if err != nil {
		return d, err
	}
	if err = s.writeNames(ctx, tx, d); err != nil {
		return d, err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM knowledge.object_links WHERE source_workspace_document_id=$1`, d.ID); err != nil {
		return d, err
	}
	for _, link := range m.Links {
		sum := sha256.Sum256([]byte(d.ID + "\x00" + link.Kind + "\x00" + link.Target))
		linkID := "knowledge_object_link_" + hex.EncodeToString(sum[:16])
		if _, err = tx.ExecContext(ctx, `INSERT INTO knowledge.object_links(knowledge_object_link_id,source_workspace_document_id,link_kind,raw_target,normalized_target) VALUES($1,$2,$3,$4,$4)`, linkID, d.ID, link.Kind, link.Target); err != nil {
			return d, err
		}
	}
	return d, tx.Commit()
}
func (s Service) Retire(ctx context.Context, root, relative string) error {
	if err := s.checkRoot(ctx, root); err != nil {
		return err
	}
	_, err := s.DB.ExecContext(ctx, `UPDATE knowledge.workspace_note_documents SET retired=true WHERE root_ref=$1 AND (path=$2 OR left(path,length($2)+1)=$2||'/')`, root, relative)
	return err
}
func (s Service) Move(ctx context.Context, root, from, to string) error {
	if err := s.checkRoot(ctx, root); err != nil {
		return err
	}
	// The source owner's accepted move is authoritative. Retarget derived names
	// in one transaction, without reading or copying the moved tree's contents.
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "workspace-note-move:"+root); err != nil {
		return err
	}
	var exists bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM knowledge.workspace_note_documents WHERE root_ref=$1 AND NOT retired AND(path=$2 OR left(path,length($2)+1)=$2||'/'))`, root, from).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return tx.Commit()
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM knowledge.workspace_note_documents WHERE root_ref=$1 AND (path=$3 OR left(path,length($3)+1)=$3||'/') AND NOT(path=$2 OR left(path,length($2)+1)=$2||'/')`, root, from, to); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE knowledge.workspace_note_documents SET path=$3||substring(path from length($2)+1),source_revision='',observed_at=NULL WHERE root_ref=$1 AND NOT retired AND(path=$2 OR left(path,length($2)+1)=$2||'/')`, root, from, to); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM knowledge.workspace_note_names n USING knowledge.workspace_note_documents d WHERE n.document_id=d.document_id AND d.root_ref=$1 AND(path=$2 OR left(path,length($2)+1)=$2||'/')`, root, to); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO knowledge.workspace_note_names(document_id,root_ref,kind,name,normalized) SELECT d.document_id,d.root_ref,v.kind,v.name,lower(v.name) FROM knowledge.workspace_note_documents d CROSS JOIN LATERAL(SELECT 'link'::text AS kind,regexp_replace(regexp_replace(d.path,'^.*/',''),'\.(md|markdown)$','','i') AS name UNION SELECT 'link',regexp_replace(d.path,'\.(md|markdown)$','','i') UNION SELECT 'link',jsonb_array_elements_text(d.aliases) UNION SELECT 'tag',jsonb_array_elements_text(d.tags) UNION SELECT 'property',jsonb_array_elements_text(d.properties))v WHERE d.root_ref=$1 AND NOT d.retired AND(path=$2 OR left(path,length($2)+1)=$2||'/') ON CONFLICT DO NOTHING`, root, to)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (s Service) Seed(ctx context.Context, root string) (int, error) {
	if err := s.checkRoot(ctx, root); err != nil {
		return 0, err
	}
	base, _, err := s.Files.SourcePath(ctx, root, "")
	if err != nil {
		return 0, err
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT DISTINCT ON(ko.source_path) ko.knowledge_object_id,substring(ko.source_path from length($1)+2),COALESCE(v.metadata->'frontmatter','{}'::jsonb) FROM knowledge.knowledge_objects ko LEFT JOIN LATERAL(SELECT metadata FROM knowledge.knowledge_object_versions v WHERE v.knowledge_object_id=ko.knowledge_object_id ORDER BY version_number DESC LIMIT 1)v ON true WHERE ko.file_class='markdown' AND ko.deleted_at IS NULL AND ko.processing_state<>'deleted' AND left(ko.source_path,length($1)+1)=$1||'/' ORDER BY ko.source_path,ko.last_seen_at DESC LIMIT 50001`, base)
	if err != nil {
		return 0, err
	}
	type seed struct {
		id, path string
		meta     []byte
	}
	values := []seed{}
	for rows.Next() {
		var v seed
		if err = rows.Scan(&v.id, &v.path, &v.meta); err != nil {
			rows.Close()
			return 0, err
		}
		values = append(values, v)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return 0, err
	}
	if len(values) > 50000 {
		return 0, fmt.Errorf("catalog exceeds bounded seed; no corpus walk was started")
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	count := 0
	for start := 0; start < len(values); start += 500 {
		batch := values[start:min(start+500, len(values))]
		documents := map[string]Document{}
		references := map[string][]Reference{}
		inputs := []map[string]any{}
		for _, v := range batch {
			var meta map[string]any
			if decodePropertyValues(v.meta, &meta) != nil {
				continue
			}
			m := propertyMetadata(meta)
			d := Document{ID: newDocumentID(), Root: root, Path: v.path, Aliases: m.Aliases, Tags: m.Tags, Properties: m.Properties, Values: m.Values, MetadataVersion: metadataVersion}
			documents[v.path] = d
			references[v.path] = m.Links
			inputs = append(inputs, map[string]any{"id": d.ID, "path": v.path, "knowledge_id": v.id, "aliases": d.Aliases, "tags": d.Tags, "properties": d.Properties, "property_values": d.Values})
		}
		raw, _ := json.Marshal(inputs)
		rows, e := tx.QueryContext(ctx, `INSERT INTO knowledge.workspace_note_documents(document_id,root_ref,path,knowledge_object_id,aliases,tags,properties,property_values,metadata_version) SELECT v.id,$1,v.path,v.knowledge_id,v.aliases,v.tags,v.properties,v.property_values,$3 FROM jsonb_to_recordset($2::jsonb) v(id text,path text,knowledge_id text,aliases jsonb,tags jsonb,properties jsonb,property_values jsonb) ON CONFLICT(root_ref,path) DO UPDATE SET knowledge_object_id=excluded.knowledge_object_id,aliases=excluded.aliases,tags=excluded.tags,properties=excluded.properties,property_values=excluded.property_values,metadata_version=excluded.metadata_version WHERE knowledge.workspace_note_documents.observed_at IS NULL AND NOT knowledge.workspace_note_documents.retired RETURNING document_id,path`, root, string(raw), metadataVersion)
		if e != nil {
			return count, e
		}
		keys := []string{}
		names := []map[string]string{}
		links := []map[string]string{}
		for rows.Next() {
			var id, relative string
			if e = rows.Scan(&id, &relative); e != nil {
				rows.Close()
				return count, e
			}
			d := documents[relative]
			d.ID = id
			keys = append(keys, id)
			for _, name := range namesFor(d) {
				name["id"] = id
				names = append(names, name)
			}
			for _, link := range references[relative] {
				sum := sha256.Sum256([]byte(id + "\x00" + link.Kind + "\x00" + link.Target))
				links = append(links, map[string]string{"id": "knowledge_object_link_" + hex.EncodeToString(sum[:16]), "source": id, "kind": link.Kind, "target": link.Target})
			}
			count++
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return count, e
		}
		if len(keys) == 0 {
			continue
		}
		keyJSON, _ := json.Marshal(keys)
		if _, err = tx.ExecContext(ctx, `DELETE FROM knowledge.workspace_note_names WHERE document_id IN(SELECT jsonb_array_elements_text($1::jsonb))`, string(keyJSON)); err != nil {
			return count, err
		}
		nameJSON, _ := json.Marshal(names)
		if _, err = tx.ExecContext(ctx, `INSERT INTO knowledge.workspace_note_names(document_id,root_ref,kind,name,normalized) SELECT v.id,$1,v.kind,v.name,v.normalized FROM jsonb_to_recordset($2::jsonb) v(id text,kind text,name text,normalized text) ON CONFLICT DO NOTHING`, root, string(nameJSON)); err != nil {
			return count, err
		}
		if _, err = tx.ExecContext(ctx, `DELETE FROM knowledge.object_links WHERE metadata->>'workspace_origin'='catalog' AND source_workspace_document_id IN(SELECT jsonb_array_elements_text($1::jsonb))`, string(keyJSON)); err != nil {
			return count, err
		}
		linkJSON, _ := json.Marshal(links)
		if _, err = tx.ExecContext(ctx, `INSERT INTO knowledge.object_links(knowledge_object_link_id,source_workspace_document_id,link_kind,raw_target,normalized_target,metadata) SELECT v.id,v.source,v.kind,v.target,v.target,'{"workspace_origin":"catalog"}'::jsonb FROM jsonb_to_recordset($1::jsonb) v(id text,source text,kind text,target text) ON CONFLICT DO NOTHING`, string(linkJSON)); err != nil {
			return count, err
		}
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO knowledge.workspace_note_catalogs(root_ref,source_path,seeded_count) VALUES($1,$2,$3) ON CONFLICT(root_ref) DO UPDATE SET source_path=excluded.source_path,seeded_at=now(),seeded_count=excluded.seeded_count`, root, base, len(values)); err != nil {
		return count, err
	}
	return count, tx.Commit()
}

func escapedLike(value string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(value)
}
func (s Service) Complete(ctx context.Context, root, kind, query string) ([]Item, error) {
	if err := s.checkRoot(ctx, root); err != nil {
		return nil, err
	}
	if kind != "link" && kind != "tag" && kind != "property" {
		return nil, fmt.Errorf("invalid completion kind")
	}
	if len(query) > 200 {
		return nil, fmt.Errorf("completion query exceeds 200 characters")
	}
	pattern := escapedLike(strings.ToLower(strings.TrimSpace(query)))
	if len([]rune(query)) >= 3 {
		pattern = "%" + pattern
	}
	pattern += "%"
	querySQL := `SELECT d.path,n.name,n.kind FROM knowledge.workspace_note_names n JOIN knowledge.workspace_note_documents d USING(document_id) WHERE n.root_ref=$1 AND n.kind=$2 AND NOT d.retired AND n.normalized LIKE $3 ORDER BY n.normalized,d.path LIMIT 120`
	if kind != "link" {
		querySQL = `SELECT DISTINCT ON(n.normalized) d.path,n.name,n.kind FROM knowledge.workspace_note_names n JOIN knowledge.workspace_note_documents d USING(document_id) WHERE n.root_ref=$1 AND n.kind=$2 AND NOT d.retired AND n.normalized LIKE $3 ORDER BY n.normalized,(d.observed_at IS NULL),d.path LIMIT 120`
	}
	rows, err := s.DB.QueryContext(ctx, querySQL, root, kind, pattern)
	if err != nil {
		return nil, err
	}
	items := []Item{}
	seen := map[string]bool{}
	for rows.Next() {
		item := Item{Root: root}
		if err = rows.Scan(&item.Path, &item.Name, &item.Kind); err != nil {
			rows.Close()
			return nil, err
		}
		key := item.Path
		if kind != "link" {
			key = item.Name
		}
		if !seen[key] {
			seen[key] = true
			items = append(items, item)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	result := []Item{}
	for _, item := range items {
		if len(result) >= 40 {
			break
		}
		if d, e := s.Observe(ctx, root, item.Path); e == nil && completionMatches(d, kind, item.Name) {
			result = append(result, item)
		}
	}
	return result, nil
}

func (s Service) Resolve(ctx context.Context, root, from, reference string) ([]Item, error) {
	return s.resolve(ctx, root, from, reference, false)
}

// ResolveExplicit is for a deliberate open, not hover, embeds or backlinks.
// The caller must also select the target Box; this never broadens context.
func (s Service) ResolveExplicit(ctx context.Context, root, from, reference string) ([]Item, error) {
	return s.resolve(ctx, root, from, reference, true)
}

func (s Service) resolve(ctx context.Context, root, from, reference string, explicit bool) ([]Item, error) {
	if err := s.checkRoot(ctx, root); err != nil {
		return nil, err
	}
	target, heading, err := referenceTarget(reference)
	if err != nil {
		return nil, err
	}
	sameFile := target == ""
	if sameFile {
		target = from
	}
	absolute := strings.HasPrefix(target, "/")
	if strings.HasPrefix(target, "/") {
		found := false
		for _, candidate := range s.Files.Roots {
			prefix := strings.TrimRight(candidate.SourcePath, "/") + "/"
			if strings.HasPrefix(target, prefix) {
				if !s.sameBoxRoots(candidate.Ref, root) && !explicit {
					return nil, fs.ErrPermission
				}
				if err := s.checkRoot(ctx, candidate.Ref); err != nil {
					return nil, err
				}
				root = candidate.Ref
				target = strings.TrimPrefix(target, prefix)
				found = true
				break
			}
		}
		if !found {
			return nil, workspacefiles.ErrInvalidPath
		}
	}
	candidates := []string{}
	add := func(value string) {
		value = path.Clean(value)
		if value == ".." || strings.HasPrefix(value, "../") || strings.HasPrefix(value, "/") {
			return
		}
		candidates = append(candidates, value)
		if path.Ext(value) == "" {
			candidates = append(candidates, value+".md", value+".markdown")
		}
	}
	if !absolute && !sameFile {
		add(path.Join(path.Dir(from), target))
	}
	if !strings.HasPrefix(target, ".") {
		add(target)
	}
	for _, candidate := range unique(candidates) {
		if entry, e := s.Files.Stat(ctx, root, candidate); e == nil {
			return []Item{{Root: root, Path: candidate, Name: entry.Name, Kind: entry.Kind, Heading: heading}}, nil
		} else if !errors.Is(e, fs.ErrNotExist) {
			return nil, e
		}
	}
	title := target
	if notePath(title) {
		title = strings.TrimSuffix(title, path.Ext(title))
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT DISTINCT d.path FROM knowledge.workspace_note_names n JOIN knowledge.workspace_note_documents d USING(document_id) WHERE n.root_ref=$1 AND n.kind='link' AND n.normalized=$2 AND NOT d.retired ORDER BY d.path LIMIT 101`, root, strings.ToLower(title))
	if err != nil {
		return nil, err
	}
	paths := []string{}
	for rows.Next() {
		var p string
		if err = rows.Scan(&p); err != nil {
			rows.Close()
			return nil, err
		}
		paths = append(paths, p)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	result := []Item{}
	for _, p := range paths {
		if d, e := s.Observe(ctx, root, p); e == nil && completionMatches(d, "link", title) {
			entry, e := s.Files.Stat(ctx, root, p)
			if e != nil {
				continue
			}
			result = append(result, Item{Root: root, Path: p, Name: entry.Name, Kind: entry.Kind, Heading: heading})
		}
	}
	return result, nil
}

func completionMatches(d Document, kind, name string) bool {
	var names []string
	switch kind {
	case "tag":
		names = d.Tags
	case "property":
		names = d.Properties
	case "link":
		names = append([]string{stem(d.Path), strings.TrimSuffix(d.Path, path.Ext(d.Path))}, d.Aliases...)
	}
	for _, candidate := range names {
		if strings.EqualFold(candidate, name) {
			return true
		}
	}
	return false
}

func (s Service) candidateBacklinks(ctx context.Context, root string, names []string) ([]Item, bool, error) {
	keys := []string{}
	for _, name := range names {
		keys = append(keys, strings.ToLower(stem(name)))
	}
	tx, err := s.DB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback()
	// These are bounded interactive lookups, not analytical queries. Compiling
	// them on each read costs more than their indexed execution.
	if _, err = tx.ExecContext(ctx, `SET LOCAL jit=off`); err != nil {
		return nil, false, err
	}
	// Filter by the indexed target first, then join each owner independently.
	// An OR across the two source identities can turn a tiny backlink lookup into
	// a nested comparison against the entire document catalog.
	rows, err := tx.QueryContext(ctx, `WITH targets AS MATERIALIZED(SELECT knowledge_object_link_id,raw_target,source_workspace_document_id,source_knowledge_object_id FROM knowledge.object_links WHERE lower(regexp_replace(regexp_replace(split_part(normalized_target,'#',1),'^.*/',''),'\.(md|markdown)$','','i')) = ANY($2::text[])), matches AS(SELECT d.path,l.raw_target,l.knowledge_object_link_id FROM targets l JOIN knowledge.workspace_note_documents d ON l.source_workspace_document_id=d.document_id WHERE d.root_ref=$1 AND NOT d.retired UNION ALL SELECT d.path,l.raw_target,l.knowledge_object_link_id FROM targets l JOIN knowledge.workspace_note_documents d ON l.source_knowledge_object_id=d.knowledge_object_id WHERE d.root_ref=$1 AND NOT d.retired AND d.observed_at IS NULL) SELECT path,raw_target FROM matches ORDER BY path,knowledge_object_link_id LIMIT 201`, root, unique(keys))
	if err != nil {
		return nil, false, err
	}
	result := []Item{}
	for rows.Next() {
		item := Item{Root: root}
		if err = rows.Scan(&item.Path, &item.Reference); err != nil {
			rows.Close()
			return nil, false, err
		}
		result = append(result, item)
	}
	err = rows.Err()
	rows.Close()
	if err == nil {
		err = tx.Commit()
	}
	truncated := len(result) > 200
	if truncated {
		result = result[:200]
	}
	return result, truncated, err
}
func (s Service) Links(ctx context.Context, root, relative string) (Graph, error) {
	d, err := s.Observe(ctx, root, relative)
	result := Graph{Document: d, Forward: []Reference{}, Backlinks: []Item{}, Warnings: []string{}, Coverage: "known Markdown catalog and exact Workspace observations; not an all-files census"}
	if err != nil {
		return result, err
	}
	candidates, truncated, err := s.candidateBacklinks(ctx, root, append([]string{stem(relative)}, d.Aliases...))
	if err != nil {
		return result, err
	}
	result.Truncated = truncated
	refreshed := map[string]bool{}
	failed := map[string]bool{}
	for _, item := range candidates {
		if !refreshed[item.Path] {
			if len(refreshed) >= 100 {
				result.Truncated = true
				failed[item.Path] = true
				continue
			}
			refreshed[item.Path] = true
			if _, e := s.Observe(ctx, root, item.Path); e != nil {
				failed[item.Path] = true
				result.Warnings = append(result.Warnings, "Some candidate sources could not be verified")
			}
		}
	}
	candidates, truncated, err = s.candidateBacklinks(ctx, root, append([]string{stem(relative)}, d.Aliases...))
	if err != nil {
		return result, err
	}
	result.Truncated = result.Truncated || truncated
	seen := map[string]bool{}
	for _, item := range candidates {
		if seen[item.Path] || failed[item.Path] || !refreshed[item.Path] || item.Path == relative {
			continue
		}
		targets, e := s.Resolve(ctx, root, item.Path, item.Reference)
		if e == nil && len(targets) == 1 && targets[0].Root == root && targets[0].Path == relative {
			item.Name = path.Base(item.Path)
			item.Kind = "backlink"
			result.Backlinks = append(result.Backlinks, item)
			seen[item.Path] = true
		}
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT link_kind,raw_target FROM knowledge.object_links WHERE source_workspace_document_id=$1 ORDER BY knowledge_object_link_id LIMIT 501`, d.ID)
	if err != nil {
		return result, err
	}
	for rows.Next() {
		var link Reference
		if err = rows.Scan(&link.Kind, &link.Target); err != nil {
			rows.Close()
			return result, err
		}
		result.Forward = append(result.Forward, link)
	}
	err = rows.Err()
	rows.Close()
	if len(result.Forward) > 500 {
		result.Forward = result.Forward[:500]
		result.Truncated = true
	}
	result.Warnings = unique(result.Warnings)
	return result, err
}
