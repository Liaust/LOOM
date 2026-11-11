package workspacenotes

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"loom.local/loom/internal/workspacefiles"
)

type CatalogInput struct {
	Root, Path, After string
	Query, Tag, Type  string
	Limit             int
	Compact           bool
}

type CatalogItem struct {
	Document
	Forward          []Reference `json:"forward"`
	ForwardTruncated bool        `json:"forward_truncated,omitempty"`
}

type CatalogPage struct {
	Root      string        `json:"root"`
	Path      string        `json:"path"`
	Items     []CatalogItem `json:"items"`
	Coverage  string        `json:"coverage"`
	Truncated bool          `json:"truncated"`
	Next      string        `json:"next"`
}

// After is the last catalog document ID examined, not a path or offset. This
// avoids exposing a restricted path when that candidate ends a bounded page.
// The filters are exact: query matches a path/name/alias, tag a normalized tag,
// and type a string property. No expression language is evaluated.
func ParseCatalogInput(q url.Values) (CatalogInput, error) {
	for key, values := range q {
		switch key {
		case "action", "root", "path", "after", "limit", "query", "tag", "type", "compact":
			if len(values) != 1 {
				return CatalogInput{}, workspacefiles.ErrInvalidPath
			}
		default:
			return CatalogInput{}, workspacefiles.ErrInvalidPath
		}
	}
	in := CatalogInput{Root: q.Get("root"), Path: q.Get("path"), After: q.Get("after"), Query: q.Get("query"), Tag: q.Get("tag"), Type: q.Get("type"), Limit: 100}
	if q.Has("compact") {
		if q.Get("compact") != "true" && q.Get("compact") != "false" {
			return CatalogInput{}, workspacefiles.ErrInvalidPath
		}
		in.Compact = q.Get("compact") == "true"
	}
	if q.Has("limit") {
		var err error
		in.Limit, err = strconv.Atoi(q.Get("limit"))
		if err != nil || in.Limit < 1 {
			return CatalogInput{}, workspacefiles.ErrInvalidPath
		}
	}
	return normalizeCatalogInput(in)
}

func normalizeCatalogInput(in CatalogInput) (CatalogInput, error) {
	if in.Limit == 0 {
		in.Limit = 100
	}
	if in.Limit < 1 || in.Limit > 200 || in.Root == "" || len(in.Root) > 200 {
		return in, workspacefiles.ErrInvalidPath
	}
	if in.Path == "." {
		in.Path = ""
	}
	if in.Path != "" && (!fs.ValidPath(in.Path) || len(in.Path) > 4096 || !utf8.ValidString(in.Path) || strings.ContainsAny(in.Path, "\\\x00")) {
		return in, workspacefiles.ErrInvalidPath
	}
	if in.After != "" {
		if !strings.HasPrefix(in.After, "workspace_note_") || len(in.After) <= len("workspace_note_") || len(in.After) > 200 {
			return in, workspacefiles.ErrInvalidCursor
		}
		for _, r := range strings.TrimPrefix(in.After, "workspace_note_") {
			if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9') {
				return in, workspacefiles.ErrInvalidCursor
			}
		}
	}
	for _, value := range []string{in.Root, in.Query, in.Tag, in.Type} {
		if len(value) > 200 || !safePropertyString(value) {
			return in, workspacefiles.ErrInvalidPath
		}
	}
	return in, nil
}

func catalogWithin(prefix, relative string) bool {
	return prefix == "" || relative == prefix || strings.HasPrefix(relative, prefix+"/")
}

func catalogQuery(in CatalogInput, afterPath string) (string, []any) {
	args := []any{in.Root}
	bind := func(value any) string {
		args = append(args, value)
		return "$" + strconv.Itoa(len(args))
	}
	where := []string{"d.root_ref=$1", "NOT d.retired"}
	if in.Path != "" {
		exact, prefix := bind(in.Path), bind(escapedLike(in.Path)+"/%")
		where = append(where, "(d.path="+exact+" OR d.path LIKE "+prefix+")")
	}
	if afterPath != "" {
		where = append(where, "d.path>"+bind(afterPath))
	}
	if in.Query != "" {
		exact, name := bind(in.Query), bind(strings.ToLower(in.Query))
		where = append(where, "(d.path="+exact+" OR EXISTS(SELECT 1 FROM knowledge.workspace_note_names n WHERE n.document_id=d.document_id AND n.root_ref=d.root_ref AND n.kind='link' AND n.normalized="+name+"))")
	}
	if in.Tag != "" {
		where = append(where, "EXISTS(SELECT 1 FROM knowledge.workspace_note_names n WHERE n.document_id=d.document_id AND n.root_ref=d.root_ref AND n.kind='tag' AND n.normalized="+bind(strings.ToLower(strings.TrimPrefix(in.Tag, "#")))+")")
	}
	if in.Type != "" {
		where = append(where, "d.metadata_version>="+bind(metadataVersion), "jsonb_typeof(d.property_values->'type')='string'", "d.property_values->>'type'="+bind(in.Type))
	}
	limit := bind(in.Limit + 1)
	if in.Compact {
		return `SELECT d.document_id,d.root_ref,d.path,d.source_revision,d.aliases,d.tags,d.properties,d.property_values,d.metadata_version,d.observed_at,'[]'::jsonb
FROM knowledge.workspace_note_documents d WHERE ` + strings.Join(where, " AND ") + ` ORDER BY d.path LIMIT ` + limit, args
	}
	// Limit documents before collecting references. Both source identities are
	// indexed; seeded references are qualified hints until an exact observation.
	query := `WITH page AS MATERIALIZED (
SELECT d.* FROM knowledge.workspace_note_documents d WHERE ` + strings.Join(where, " AND ") + ` ORDER BY d.path LIMIT ` + limit + `
)
SELECT d.document_id,d.root_ref,d.path,d.source_revision,d.aliases,d.tags,d.properties,d.property_values,d.metadata_version,d.observed_at,
COALESCE(f.forward,'[]'::jsonb)
FROM page d LEFT JOIN LATERAL (
 SELECT jsonb_agg(jsonb_build_object('kind',r.link_kind,'target',r.raw_target) ORDER BY r.link_kind,r.raw_target) AS forward FROM (
  SELECT link_kind,raw_target FROM (
   (SELECT link_kind,raw_target FROM knowledge.object_links WHERE source_workspace_document_id=d.document_id ORDER BY knowledge_object_link_id LIMIT 501)
   UNION ALL
   (SELECT link_kind,raw_target FROM knowledge.object_links WHERE d.observed_at IS NULL AND source_knowledge_object_id=d.knowledge_object_id ORDER BY knowledge_object_link_id LIMIT 501)
  ) known ORDER BY link_kind,raw_target LIMIT 501
 ) r
) f ON true ORDER BY d.path`
	return query, args
}

// Catalog never seeds, observes, reads bodies or enumerates directories. Exact
// Stat checks retain Files' current hidden-path, no-symlink and root fencing.
func (s Service) Catalog(ctx context.Context, input CatalogInput) (CatalogPage, error) {
	in, err := normalizeCatalogInput(input)
	page := CatalogPage{Root: in.Root, Path: in.Path, Items: []CatalogItem{}, Coverage: "known catalog only; potentially stale, not a complete census; metadata_version=0 means unknown values, absent keys are unknown, null is explicit; unobserved rows are indexed hints"}
	if err != nil {
		return page, err
	}
	if err := s.checkRoot(ctx, in.Root); err != nil {
		return page, err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if _, _, err = s.Files.SourcePath(ctx, in.Root, in.Path); err != nil {
		return page, err
	}
	tx, err := s.DB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return page, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `SET LOCAL jit=off`); err != nil {
		return page, err
	}
	if _, err = tx.ExecContext(ctx, `SET LOCAL statement_timeout='3s'`); err != nil {
		return page, err
	}
	afterPath := ""
	if in.After != "" {
		err = tx.QueryRowContext(ctx, `SELECT path FROM knowledge.workspace_note_documents WHERE root_ref=$1 AND document_id=$2 AND NOT retired`, in.Root, in.After).Scan(&afterPath)
		if errors.Is(err, sql.ErrNoRows) || (err == nil && !catalogWithin(in.Path, afterPath)) {
			return page, workspacefiles.ErrInvalidCursor
		}
		if err != nil {
			return page, err
		}
	}
	query, args := catalogQuery(in, afterPath)
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return page, err
	}
	items := []CatalogItem{}
	for rows.Next() {
		var item CatalogItem
		var aliases, tags, properties, values, forward []byte
		if err = rows.Scan(&item.ID, &item.Root, &item.Path, &item.Revision, &aliases, &tags, &properties, &values, &item.MetadataVersion, &item.ObservedAt, &forward); err != nil {
			break
		}
		if json.Unmarshal(aliases, &item.Aliases) != nil || json.Unmarshal(tags, &item.Tags) != nil || json.Unmarshal(properties, &item.Properties) != nil || decodePropertyValues(values, &item.Values) != nil || json.Unmarshal(forward, &item.Forward) != nil {
			err = fmt.Errorf("invalid catalog metadata")
			break
		}
		items = append(items, item)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return page, err
	}
	if err = tx.Commit(); err != nil {
		return page, err
	}
	if len(items) > in.Limit {
		items = items[:in.Limit]
		page.Next = items[len(items)-1].ID
		page.Truncated = true
	}
	for _, item := range items {
		if item.Root != in.Root || !catalogWithin(in.Path, item.Path) {
			return page, fmt.Errorf("invalid catalog scope")
		}
		entry, statErr := s.Files.Stat(ctx, in.Root, item.Path)
		if err = statErr; err != nil {
			if errors.Is(err, fs.ErrNotExist) || errors.Is(err, fs.ErrPermission) || errors.Is(err, workspacefiles.ErrInvalidPath) {
				continue
			}
			return page, err
		}
		if entry.Kind != "file" {
			continue
		}
		if len(item.Forward) > 500 {
			item.Forward = item.Forward[:500]
			item.ForwardTruncated = true
			page.Truncated = true
		}
		seen := map[Reference]bool{}
		forward := []Reference{}
		for _, ref := range item.Forward {
			if !seen[ref] {
				seen[ref] = true
				forward = append(forward, ref)
			}
		}
		item.Forward = forward
		page.Items = append(page.Items, item)
	}
	return page, ctx.Err()
}
