package knowledge

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"
)

var ErrFileSourceUnavailable = errors.New("current source is unavailable on this node")
var ErrFileSourceMissing = errors.New("current source file is missing")
var ErrFileSourceChanged = errors.New("current source changed during read; retry")
var ErrFileSourceNotRegular = errors.New("source is not a regular file")

const DefaultFileReadBytes = 64 * 1024
const MaxFileReadBytes = 1024 * 1024

type FileReadInput struct {
	Ref    string `json:"ref"`
	Offset int64  `json:"offset,omitempty"`
	Limit  int    `json:"limit,omitempty"`
}
type FileReadResult struct {
	Processing *FileReadProcessing `json:"processing,omitempty"`
	FileID     string              `json:"file_id"`
	Path       string              `json:"path"`
	Node       string              `json:"node"`
	Status     string              `json:"status"`
	Text       string              `json:"text,omitempty"`
	Offset     int64               `json:"offset"`
	NextOffset int64               `json:"next_offset"`
	Truncated  bool                `json:"truncated"`
}

type FileReadProcessing struct {
	KnowledgeObjectID  string `json:"knowledge_object_id"`
	PublishedVersionID string `json:"published_version_id,omitempty"`
	Publication        string `json:"publication"`
	InspectCommand     string `json:"inspect_command"`
}

func fileReadProcessing(target fileReadTarget) *FileReadProcessing {
	if target.objectID == "" {
		return nil
	}
	state := "unpublished"
	if target.version != "" {
		state = "not_current"
		if target.current {
			state = "current"
		}
	}
	return &FileReadProcessing{KnowledgeObjectID: target.objectID, PublishedVersionID: target.version,
		Publication: state, InspectCommand: fmt.Sprintf("loom notes pipelines inspect %q --summary --json", target.objectID)}
}

type NativeFileRead struct {
	Text       string
	NextOffset int64
	Truncated  bool
}
type FileSourceReader interface {
	ReadNotesFile(context.Context, string, string, string, int64, int) (NativeFileRead, error)
}

type FileSourceObservation struct {
	Presence   string
	Size       int64
	ModifiedAt *time.Time
}
type FileSourceObserver interface {
	ObserveNotesFile(context.Context, string, string, string) (FileSourceObservation, error)
}

func WithFileSourceReader(reader FileSourceReader) Option {
	return func(s *Service) { s.fileSourceReader = reader }
}

type fileReadTarget struct {
	fileID, objectID, rootID, rootPath, relative, path, node, class, version, presence string
	current                                                                            bool
}

func (s *Service) resolveFileReadTarget(ctx context.Context, ref string) (fileReadTarget, error) {
	var target fileReadTarget
	// Resolve the cheap exact reference before expanding custody/publication
	// checks. Otherwise the planner can evaluate those checks across the corpus.
	args := []any{ref}
	selection := notesSelectedRootSQL(notesBoxSelection(ctx), "r", &args)
	rows, err := s.store.db.QueryContext(ctx, fileReadTargetSQL(selection), args...)
	if err != nil {
		return target, err
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return target, err
		}
		rows.Close()
		target, err = s.resolveMetadataFileRead(ctx, ref)
		if errors.Is(err, sql.ErrNoRows) {
			return target, s.classifyFileAdmission(ctx, ref)
		}
		return target, err
	}
	if err = rows.Scan(&target.fileID, &target.objectID, &target.rootID, &target.rootPath, &target.relative, &target.path, &target.node, &target.class, &target.version, &target.presence, &target.current); err != nil {
		return target, err
	}
	if rows.Next() {
		var other fileReadTarget
		if err = rows.Scan(&other.fileID, &other.objectID, &other.rootID, &other.rootPath, &other.relative, &other.path, &other.node, &other.class, &other.version, &other.presence, &other.current); err != nil {
			return target, err
		}
		if other.fileID != target.fileID {
			return target, fmt.Errorf("%w: path is ambiguous; use the file_id", ErrInvalid)
		}
	}
	if err = rows.Err(); err != nil {
		return target, err
	}
	if !filepath.IsAbs(target.path) && filepath.IsAbs(target.rootPath) {
		target.path = filepath.Join(target.rootPath, filepath.FromSlash(target.relative))
	}
	if !filepath.IsAbs(target.path) || filepath.Clean(target.path) != target.path {
		return target, ErrFileSourceUnavailable
	}
	// Archived custody supplies its actual path; use the same relative file
	// under that verified archive root rather than the old Box location.
	suffix := string(filepath.Separator) + filepath.FromSlash(target.relative)
	if strings.HasSuffix(target.path, suffix) {
		target.rootPath = strings.TrimSuffix(target.path, suffix)
	}
	return target, nil
}

func fileReadTargetSQL(selections ...string) string {
	selection := "true"
	if len(selections) > 0 {
		selection = selections[0]
	}
	return `WITH matching_files AS MATERIALIZED (
	 SELECT ko.*,r.source_path AS read_root_path,custody.canonical_path AS read_canonical_path
	 FROM knowledge.knowledge_objects ko JOIN knowledge.notes_source_roots r USING(notes_source_root_id)
	 JOIN knowledge.notes_object_custody custody USING(knowledge_object_id)
	 LEFT JOIN files.file_metadata f ON f.object_id=ko.canonical_file_id
	 WHERE (ko.canonical_file_id=$1 OR ko.knowledge_object_id=$1 OR custody.canonical_path=$1 OR ko.source_path=$1 OR f.managed_source_path=$1
	 OR (starts_with(r.source_path,'/') AND rtrim(r.source_path,'/')||'/'||ko.relative_path=$1)) AND ` + selection + `)
	 SELECT COALESCE(ko.canonical_file_id,ko.knowledge_object_id),ko.knowledge_object_id,
	 ko.notes_source_root_id,ko.read_root_path,ko.relative_path,ko.read_canonical_path,ko.source_node_key,ko.file_class,
	 COALESCE(ko.lexical_version_id,''),COALESCE(f.source_presence,'unobserved'),` + notesPublicationCurrentSQL("ko", "ko") + `
	 FROM matching_files ko
	 LEFT JOIN files.file_metadata f ON f.object_id=ko.canonical_file_id
	 WHERE ` + visibleNotesCustodyObjectSQL("ko", false) + ` AND ` + visibleNotesKnowledgeRelativePathSQL("ko.relative_path") + `
	 ORDER BY (ko.lexical_version_id IS NOT NULL) DESC,ko.knowledge_object_id LIMIT 2`
}

func (s *Service) ReadFile(ctx context.Context, input FileReadInput) (FileReadResult, error) {
	if strings.TrimSpace(input.Ref) == "" || input.Offset < 0 || input.Limit < 0 || input.Limit > MaxFileReadBytes {
		return FileReadResult{}, fmt.Errorf("%w: ref, nonnegative offset and limit up to %d bytes required", ErrInvalid, MaxFileReadBytes)
	}
	if input.Limit == 0 {
		input.Limit = DefaultFileReadBytes
	}
	target, err := s.resolveFileReadTarget(ctx, input.Ref)
	if err != nil {
		return FileReadResult{}, err
	}
	out := FileReadResult{FileID: target.fileID, Path: target.path, Node: target.node, Offset: input.Offset, NextOffset: input.Offset, Processing: fileReadProcessing(target)}
	if !target.current && (target.presence == "missing" || target.presence == "unavailable") {
		out.Status = target.presence
		return out, nil
	}
	switch strings.ToLower(filepath.Ext(target.path)) {
	case ".md", ".markdown", ".txt", ".csv", ".json", ".yaml", ".yml", ".tex":
		if s.fileSourceReader == nil {
			return out, ErrFileSourceUnavailable
		}
		read, err := s.fileSourceReader.ReadNotesFile(ctx, target.node, target.rootPath, target.relative, input.Offset, input.Limit)
		if errors.Is(err, ErrFileSourceMissing) {
			out.Status = "missing"
			return out, nil
		}
		if err != nil {
			return out, err
		}
		if !utf8.ValidString(read.Text) || strings.IndexByte(read.Text, 0) >= 0 {
			return out, fmt.Errorf("%w: source is not UTF-8 text", ErrInvalid)
		}
		after, err := s.resolveFileReadTarget(ctx, input.Ref)
		if err != nil {
			return out, err
		}
		if after.path != target.path || after.fileID != target.fileID || (!after.current && (after.presence == "missing" || after.presence == "unavailable")) {
			return out, ErrFileSourceChanged
		}
		out.Status, out.Text, out.NextOffset, out.Truncated = "source_text", read.Text, read.NextOffset, read.Truncated
		out.Processing = fileReadProcessing(after)
		return out, nil
	}
	if target.version == "" {
		out.Status = "no_extracted_text"
		return out, nil
	}
	// Only the currently published body is readable here. Exact retained
	// citations remain on the compatibility passage endpoint.
	var body []byte
	args := []any{target.objectID, input.Offset, input.Limit + 1}
	selection := notesSelectedObjectSQL(ctx, "ko", &args)
	err = s.store.db.QueryRowContext(ctx, `SELECT substring(convert_to(a.text_content,'UTF8') FROM LEAST($2::bigint,2147483646)::int+1 FOR $3::int)
	 FROM knowledge.derived_artifacts a JOIN knowledge.knowledge_objects ko USING(knowledge_object_id)
	 JOIN knowledge.knowledge_object_versions v ON v.knowledge_object_version_id=a.knowledge_object_version_id
	 WHERE ko.knowledge_object_id=$1 AND a.knowledge_object_version_id=ko.lexical_version_id
	 AND a.artifact_kind='consolidated_text' AND a.active AND a.text_content IS NOT NULL
	 AND `+notesPublicationCurrentSQL("ko", "v")+` AND `+visibleNotesKnowledgeObjectSQL("ko")+` AND `+selection+`
	 ORDER BY a.created_at DESC LIMIT 1`, args...).Scan(&body)
	if errors.Is(err, sql.ErrNoRows) {
		out.Status = "no_current_extracted_text"
		return out, nil
	}
	if err != nil {
		return out, err
	}
	read, err := FileTextRange(body, input.Offset, input.Limit)
	if err != nil {
		return out, err
	}
	out.Status, out.Text, out.NextOffset, out.Truncated = "extracted_text", read.Text, read.NextOffset, read.Truncated
	return out, nil
}

// The offset is a byte boundary supplied by the previous read. Trim only an
// incomplete final rune, never malformed content elsewhere in the range.
func FileTextRange(body []byte, offset int64, limit int) (NativeFileRead, error) {
	truncated := len(body) > limit
	if truncated {
		body = body[:limit]
	}
	end := 0
	for end < len(body) {
		if truncated && !utf8.FullRune(body[end:]) {
			break
		}
		r, size := utf8.DecodeRune(body[end:])
		if (r == utf8.RuneError && size == 1) || r == 0 {
			return NativeFileRead{}, fmt.Errorf("%w: invalid UTF-8 text range; use next_offset", ErrInvalid)
		}
		end += size
	}
	if end == 0 && len(body) > 0 {
		return NativeFileRead{}, fmt.Errorf("%w: limit is too small for the next character", ErrInvalid)
	}
	return NativeFileRead{Text: string(body[:end]), NextOffset: offset + int64(end), Truncated: truncated}, nil
}
