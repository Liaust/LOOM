package projectstate

import (
	"bytes"
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
	"loom.local/loom/internal/ids"
	"loom.local/loom/internal/projects"
)

const (
	ProjectDevelopmentFileLimit    = 64 << 10
	ProjectDevelopmentFilesLimit   = 128
	ProjectDevelopmentBytesLimit   = 2 << 20
	ProjectDevelopmentExcerptLimit = 4 << 10
	projectIdentityPath            = ".loom/project.yaml"
)

type FileProjectDevelopmentInspector struct{}

func (FileProjectDevelopmentInspector) Inspect(ctx context.Context, input ProjectDevelopmentInput) ProjectDevelopmentState {
	return ReadProjectDevelopment(ctx, input)
}

type projectDevelopmentReader struct {
	ctx      context.Context
	root     *os.Root
	state    ProjectDevelopmentState
	observed map[string]os.FileInfo
	blobs    map[string][2]string
}

// ReadProjectDevelopment reads selected metadata only. It neither registers a
// project nor infers repository membership, and works without Git or .repo.
func ReadProjectDevelopment(ctx context.Context, input ProjectDevelopmentInput) ProjectDevelopmentState {
	r := projectDevelopmentReader{ctx: ctx, state: emptyProjectDevelopment(input), observed: map[string]os.FileInfo{}, blobs: map[string][2]string{}}
	if input.Lifecycle == "archived" {
		return r.finish(ProjectDevelopmentArchived, "project_archived")
	}
	if input.LocalNode == "" || input.OwnerNode == "" || input.LocalNode != input.OwnerNode {
		return r.finish(ProjectDevelopmentUnavailable, "owner_node_not_local")
	}
	if input.ProjectID == "" || !filepath.IsAbs(input.ProjectRoot) || filepath.Clean(input.ProjectRoot) != input.ProjectRoot {
		return r.finish(ProjectDevelopmentInvalid, "invalid_project_identity_or_root")
	}
	if ctx.Err() != nil {
		return r.finish(ProjectDevelopmentUnavailable, "inspection_cancelled")
	}
	physical, err := filepath.EvalSymlinks(input.ProjectRoot)
	if err != nil {
		return r.finish(ProjectDevelopmentUnavailable, "project_root_unavailable")
	}
	r.state.RootDigest = projectDevelopmentHash([]byte(physical))
	r.root, err = os.OpenRoot(physical)
	if err != nil {
		return r.finish(ProjectDevelopmentUnavailable, "project_root_unavailable")
	}
	defer r.root.Close()

	raw, identity := r.capture(projectIdentityPath)
	if identity.Posture != "present" {
		posture := ProjectDevelopmentInvalid
		if identity.Posture == "missing" {
			posture = ProjectDevelopmentMissing
		}
		if identity.Posture == "unreadable" {
			posture = ProjectDevelopmentUnavailable
		}
		return r.finish(posture, "identity_"+identity.Posture)
	}
	node, err := decodeBoundedDevelopmentState(raw)
	root := yamlDocumentMapping(node)
	project := yamlMappingValue(root, "project")
	schema := yamlScalarValue(root, "schema_version")
	validSchema := schema == projects.ProjectRepositoryProjectSchemaV03 || schema == projects.ProjectRepositoryProjectSchemaV04 || schema == projects.ProjectRepositoryProjectSchemaV05
	if err != nil || !validSchema || yamlScalarValue(root, "kind") != "loom.project" || ids.Validate(ids.ProjectPrefix, yamlScalarValue(project, "id")) != nil || yamlScalarValue(project, "owner_node") == "" {
		r.malformed(identity, "identity_malformed")
		return r.finish(ProjectDevelopmentInvalid, "identity_malformed")
	}
	if yamlScalarValue(project, "id") != input.ProjectID || yamlScalarValue(project, "owner_node") != input.OwnerNode {
		return r.finish(ProjectDevelopmentMismatch, "identity_owner_mismatch")
	}
	if yamlScalarValue(project, "status") == "archived" {
		return r.finish(ProjectDevelopmentArchived, "source_archived")
	}
	info, err := r.safeStat(".project")
	if errors.Is(err, os.ErrNotExist) {
		return r.finish(ProjectDevelopmentMissing, "development_source_missing")
	}
	if err != nil || !info.IsDir() {
		if errors.Is(err, ErrPathEscape) || err == nil {
			return r.finish(ProjectDevelopmentInvalid, "development_source_unsafe")
		}
		return r.finish(ProjectDevelopmentUnavailable, "development_source_unreadable")
	}

	fixed := []string{".project/OVERVIEW.md", ".project/STATE.md", ".project/ROADMAP.md", ".project/MAP.md", ".project/protocols/WORKFLOW.md", "AGENTS.md"}
	for _, name := range fixed {
		payload, doc := r.capture(name)
		if name == ".project/OVERVIEW.md" && doc.Posture == "missing" {
			payload, doc = r.capture(".project/PROJECT.md")
		}
		r.summarize(payload, doc)
	}
	for _, name := range r.selectMetadata() {
		payload, doc := r.capture(name)
		r.summarize(payload, doc)
	}
	// Recheck captured file identities, not later contents presented as the same
	// observation. A disappearing source remains unavailable, never a deletion.
	for name, before := range r.observed {
		after, err := r.safeStat(name)
		if err != nil || !os.SameFile(before, after) || before.Size() != after.Size() || before.ModTime() != after.ModTime() {
			return r.finish(ProjectDevelopmentUnavailable, "source_changed_during_read")
		}
	}
	posture, reason := ProjectDevelopmentReady, ""
	present := 0
	for _, doc := range r.state.Documents {
		if doc.Posture == "present" && strings.HasPrefix(doc.Path, ".project/") {
			present++
		}
		switch doc.Posture {
		case "malformed", "unsafe":
			return r.finish(ProjectDevelopmentInvalid, doc.ReasonCode)
		case "unreadable":
			posture, reason = ProjectDevelopmentUnavailable, doc.ReasonCode
		}
	}
	if posture == ProjectDevelopmentReady {
		if r.state.OmittedFiles > 0 {
			posture, reason = ProjectDevelopmentPartial, "source_limits_exceeded"
		} else if present == 0 {
			posture, reason = ProjectDevelopmentMissing, "development_documents_missing"
		}
	}
	r.state.Complete = posture == ProjectDevelopmentReady
	if r.state.Complete && input.ObserveGit {
		r.state.Git = r.observeGit(physical)
	}
	return r.finish(posture, reason)
}

func emptyProjectDevelopment(input ProjectDevelopmentInput) ProjectDevelopmentState {
	return ProjectDevelopmentState{ProjectID: input.ProjectID, OwnerNode: input.OwnerNode, SourceRevision: input.SourceRevision, Documents: []ProjectDevelopmentDocument{}, Features: []ProjectDevelopmentFeature{}, Decisions: []ProjectDevelopmentDecision{}}
}

func projectDevelopmentHash(raw []byte) string { return fmt.Sprintf("sha256:%x", sha256.Sum256(raw)) }

func (r *projectDevelopmentReader) finish(posture ProjectDevelopmentPosture, reason string) ProjectDevelopmentState {
	r.state.Posture, r.state.ReasonCode = posture, reason
	if posture != ProjectDevelopmentReady {
		r.state.Complete = false
	}
	sort.Slice(r.state.Documents, func(i, j int) bool { return r.state.Documents[i].Path < r.state.Documents[j].Path })
	// Exclude clock time, registration revision and Git HEAD: identical selected
	// source has a stable identity even when unrelated code or observations change.
	digest, _ := json.Marshal(struct {
		ProjectID  string
		RootDigest string
		Posture    ProjectDevelopmentPosture
		Reason     string
		Omitted    int
		Documents  []ProjectDevelopmentDocument
	}{r.state.ProjectID, r.state.RootDigest, posture, reason, r.state.OmittedFiles, r.state.Documents})
	r.state.SourceDigest = projectDevelopmentHash(digest)
	return r.state
}

// os.Root confines the eventual open even across concurrent renames. Reject
// symlinked metadata as well, so the allowlist cannot alias arbitrary payloads.
func (r *projectDevelopmentReader) safeStat(name string) (os.FileInfo, error) {
	if !safeRepositoryRelativePath(name) {
		return nil, ErrPathInvalid
	}
	parts := strings.Split(name, "/")
	var info os.FileInfo
	for i := range parts {
		if err := r.ctx.Err(); err != nil {
			return nil, err
		}
		var err error
		info, err = r.root.Lstat(strings.Join(parts[:i+1], "/"))
		if err != nil {
			return nil, err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil, ErrPathEscape
		}
		if i < len(parts)-1 && !info.IsDir() {
			return nil, ErrPathInvalid
		}
	}
	return info, nil
}

func (r *projectDevelopmentReader) capture(name string) ([]byte, *ProjectDevelopmentDocument) {
	doc := ProjectDevelopmentDocument{Path: name, Posture: "missing"}
	if len(r.state.Documents) >= ProjectDevelopmentFilesLimit {
		r.state.OmittedFiles++
		doc.Posture, doc.ReasonCode = "omitted", "file_count_limit"
		return nil, &doc
	}
	r.state.Documents = append(r.state.Documents, doc)
	d := &r.state.Documents[len(r.state.Documents)-1]
	info, err := r.safeStat(name)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			d.Posture, d.ReasonCode = "unreadable", "source_unreadable"
			if errors.Is(err, ErrPathEscape) || errors.Is(err, ErrPathInvalid) {
				d.Posture, d.ReasonCode = "unsafe", "path_escape"
			}
			if r.ctx.Err() != nil {
				d.ReasonCode = "inspection_cancelled"
			}
		}
		return nil, d
	}
	d.SizeBytes = info.Size()
	if !info.Mode().IsRegular() {
		d.Posture, d.ReasonCode = "unsafe", "source_not_regular"
		return nil, d
	}
	remaining := ProjectDevelopmentBytesLimit - r.state.CapturedBytes
	if info.Size() > ProjectDevelopmentFileLimit || info.Size() > int64(remaining) {
		d.Posture, d.ReasonCode = "omitted", "source_byte_limit"
		r.state.OmittedFiles++
		return nil, d
	}
	// O_NONBLOCK prevents a regular-file-to-FIFO race from hanging a refresh.
	file, err := r.root.OpenFile(name, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		d.Posture, d.ReasonCode = "unreadable", "source_unreadable"
		return nil, d
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(info, opened) {
		d.Posture, d.ReasonCode = "unreadable", "source_changed_during_read"
		return nil, d
	}
	limit := min(ProjectDevelopmentFileLimit, remaining)
	payload, err := io.ReadAll(io.LimitReader(file, int64(limit)))
	r.state.CapturedBytes += len(payload)
	if err != nil || int64(len(payload)) != opened.Size() {
		d.Posture, d.ReasonCode = "unreadable", "source_changed_during_read"
		return nil, d
	}
	d.SizeBytes = int64(len(payload))
	d.Hash, d.Posture = projectDevelopmentHash(payload), "present"
	r.observed[name] = opened
	if !utf8.Valid(payload) || bytes.IndexByte(payload, 0) >= 0 {
		r.malformed(d, "source_not_text")
		return nil, d
	}
	d.Excerpt = projectDevelopmentExcerpt(string(payload))
	d.Truncated = len(d.Excerpt) < len(payload)
	blob := append([]byte(fmt.Sprintf("blob %d\x00", len(payload))), payload...)
	r.blobs[name] = [2]string{fmt.Sprintf("%x", sha1.Sum(blob)), fmt.Sprintf("%x", sha256.Sum256(blob))}
	return payload, d
}

func projectDevelopmentExcerpt(value string) string {
	if len(value) <= ProjectDevelopmentExcerptLimit {
		return value
	}
	end := ProjectDevelopmentExcerptLimit
	for !utf8.ValidString(value[:end]) {
		end--
	}
	return value[:end]
}

func (r *projectDevelopmentReader) malformed(doc *ProjectDevelopmentDocument, reason string) {
	doc.Posture, doc.ReasonCode = "malformed", reason
}

// Stream only the two allowed directory levels, retaining a bounded sorted
// selection while counting every eligible file excluded by the file-count cap.
func (r *projectDevelopmentReader) selectMetadata() []string {
	selected := []string{}
	count := 0
	for _, directory := range []string{".project/features", ".project/decisions"} {
		info, err := r.safeStat(directory)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil || !info.IsDir() {
			r.capture(directory)
			continue
		}
		file, err := r.root.Open(directory)
		if err != nil {
			r.capture(directory)
			continue
		}
		for {
			entries, readErr := file.ReadDir(64)
			for _, entry := range entries {
				if r.ctx.Err() != nil {
					break
				}
				name := directory + "/" + entry.Name()
				if directory == ".project/features" {
					if !entry.IsDir() && entry.Type()&os.ModeSymlink == 0 {
						continue
					}
					name += "/feature.yaml"
					if _, err := r.safeStat(name); errors.Is(err, os.ErrNotExist) {
						continue
					}
				} else if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
					continue
				}
				count++
				selected = append(selected, name)
				sort.Strings(selected)
				if len(selected) > ProjectDevelopmentFilesLimit {
					selected = selected[:ProjectDevelopmentFilesLimit]
				}
			}
			if r.ctx.Err() != nil || readErr != nil {
				if r.ctx.Err() != nil || readErr != io.EOF {
					_, doc := r.capture(directory)
					doc.Posture, doc.ReasonCode = "unreadable", "source_directory_unreadable"
				}
				break
			}
		}
		file.Close()
	}
	remaining := ProjectDevelopmentFilesLimit - len(r.state.Documents)
	if len(selected) > remaining {
		selected = selected[:remaining]
	}
	r.state.OmittedFiles += count - len(selected)
	return selected
}

func (r *projectDevelopmentReader) summarize(raw []byte, doc *ProjectDevelopmentDocument) {
	if doc.Posture != "present" {
		return
	}
	if strings.HasSuffix(doc.Path, "/feature.yaml") {
		node, err := decodeBoundedDevelopmentState(raw)
		fields := yamlDocumentMapping(node)
		if err != nil || fields == nil || !projectMetadataScalars(fields, "slug", "title", "status", "next_action", "blocked_reason") {
			r.malformed(doc, "feature_metadata_malformed")
			return
		}
		slug := path.Base(path.Dir(doc.Path))
		if declared := projectMetadataValue(fields, "slug"); declared != "" && declared != slug {
			r.malformed(doc, "feature_slug_mismatch")
			return
		}
		r.state.Features = append(r.state.Features, ProjectDevelopmentFeature{Slug: slug, Title: projectMetadataValue(fields, "title"), Status: projectMetadataValue(fields, "status"), NextAction: projectMetadataValue(fields, "next_action"), BlockedReason: projectMetadataValue(fields, "blocked_reason"), Path: doc.Path, Hash: doc.Hash})
		return
	}
	body, fields, err := projectMarkdown(raw)
	if err != nil {
		r.malformed(doc, "frontmatter_malformed")
		return
	}
	if !projectMetadataScalars(fields, "purpose", "current_focus", "progress", "blockers", "next_action", "structure") {
		r.malformed(doc, "summary_metadata_malformed")
		return
	}
	summary := func(key string, headings ...string) string {
		return projectDevelopmentExcerpt(firstDevelopmentStateValue(projectMarkdownSection(body, headings...), projectMetadataValue(fields, key)))
	}
	switch doc.Path {
	case ".project/OVERVIEW.md", ".project/PROJECT.md":
		r.state.Purpose = summary("purpose", "purpose", "overview", "purpose and scope", "project purpose")
		if r.state.Purpose == "" {
			r.state.Purpose = projectDevelopmentExcerpt(projectMarkdownIntroduction(body))
		}
	case ".project/STATE.md":
		r.state.CurrentFocus = summary("current_focus", "current focus", "active focus", "focus", "current work")
		r.state.Progress = summary("progress", "progress", "current state", "current position", "status")
		r.state.Blockers = summary("blockers", "blockers", "current blockers")
		r.state.NextAction = summary("next_action", "next action", "next useful action", "next steps")
	case ".project/ROADMAP.md":
		r.state.Roadmap = projectDevelopmentExcerpt(strings.TrimSpace(body))
	case ".project/MAP.md":
		r.state.Structure = summary("structure", "structure", "project structure", "map")
		if r.state.Structure == "" {
			r.state.Structure = projectDevelopmentExcerpt(strings.TrimSpace(body))
		}
	default:
		if strings.HasPrefix(doc.Path, ".project/decisions/") {
			if !projectMetadataScalars(fields, "id", "decision_id", "title", "status", "date") {
				r.malformed(doc, "decision_metadata_malformed")
				return
			}
			r.state.Decisions = append(r.state.Decisions, ProjectDevelopmentDecision{
				ID:     firstDevelopmentStateValue(projectMetadataValue(fields, "id"), projectMetadataValue(fields, "decision_id"), projectMarkdownField(body, "decision id")),
				Title:  firstDevelopmentStateValue(projectMetadataValue(fields, "title"), projectMarkdownTitle(body)),
				Status: firstDevelopmentStateValue(projectMetadataValue(fields, "status"), projectMarkdownField(body, "status")),
				Date:   firstDevelopmentStateValue(projectMetadataValue(fields, "date"), projectMarkdownField(body, "date")), Path: doc.Path, Hash: doc.Hash,
			})
		}
	}
}

func projectMetadataScalars(fields *yaml.Node, keys ...string) bool {
	for _, key := range keys {
		if value := yamlMappingValue(fields, key); value != nil && value.Kind != yaml.ScalarNode {
			return false
		}
	}
	return true
}

func projectMetadataValue(fields *yaml.Node, key string) string {
	value := yamlMappingValue(fields, key)
	if value == nil || value.Tag == "!!null" || value.Kind != yaml.ScalarNode {
		return ""
	}
	return projectDevelopmentExcerpt(strings.TrimSpace(value.Value))
}

func projectMarkdown(raw []byte) (string, *yaml.Node, error) {
	text := strings.ReplaceAll(string(raw), "\r\n", "\n")
	if !strings.HasPrefix(text, "---\n") {
		return text, nil, nil
	}
	lines := strings.Split(text, "\n")
	for i := 1; i < len(lines); i++ {
		if lines[i] != "---" && lines[i] != "..." {
			continue
		}
		frontmatter := strings.Join(lines[1:i], "\n")
		if strings.TrimSpace(frontmatter) == "" {
			return strings.Join(lines[i+1:], "\n"), nil, nil
		}
		node, err := decodeBoundedDevelopmentState([]byte(frontmatter))
		if err != nil || yamlDocumentMapping(node) == nil {
			return "", nil, fmt.Errorf("invalid frontmatter")
		}
		return strings.Join(lines[i+1:], "\n"), yamlDocumentMapping(node), nil
	}
	return "", nil, fmt.Errorf("unterminated frontmatter")
}

func projectMarkdownHeading(line string) (int, string) {
	line = strings.TrimSpace(line)
	level := len(line) - len(strings.TrimLeft(line, "#"))
	if level < 1 || level > 6 || len(line) == level || line[level] != ' ' {
		return 0, ""
	}
	return level, strings.TrimSpace(strings.TrimRight(strings.TrimSpace(line[level:]), "#"))
}

func projectMarkdownSection(body string, headings ...string) string {
	lines := strings.Split(body, "\n")
	for _, wanted := range headings {
		start, level, fence := -1, 0, ""
		for i, line := range lines {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
				marker := trimmed[:3]
				if fence == "" {
					fence = marker
				} else if fence == marker {
					fence = ""
				}
				continue
			}
			if fence != "" {
				continue
			}
			currentLevel, title := projectMarkdownHeading(line)
			if start >= 0 && currentLevel > 0 && currentLevel <= level {
				return strings.TrimSpace(strings.Join(lines[start:i], "\n"))
			}
			if start < 0 && strings.EqualFold(title, wanted) {
				start, level = i+1, currentLevel
			}
		}
		if start >= 0 {
			return strings.TrimSpace(strings.Join(lines[start:], "\n"))
		}
	}
	return ""
}

func projectMarkdownTitle(body string) string {
	for _, line := range strings.Split(body, "\n") {
		if level, title := projectMarkdownHeading(line); level == 1 {
			return projectDevelopmentExcerpt(title)
		}
	}
	return ""
}

func projectMarkdownIntroduction(body string) string {
	lines := strings.Split(body, "\n")
	start := 0
	for i, line := range lines {
		if level, _ := projectMarkdownHeading(line); level == 1 && strings.TrimSpace(strings.Join(lines[:i], "\n")) == "" {
			start = i + 1
			continue
		} else if level > 0 {
			return strings.TrimSpace(strings.Join(lines[start:i], "\n"))
		}
	}
	return strings.TrimSpace(strings.Join(lines[start:], "\n"))
}

func projectMarkdownField(body, key string) string {
	fence := ""
	for _, line := range strings.Split(body, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			marker := trimmed[:3]
			if fence == "" {
				fence = marker
			} else if fence == marker {
				fence = ""
			}
			continue
		}
		if fence != "" {
			continue
		}
		if level, _ := projectMarkdownHeading(line); level > 1 {
			break
		}
		line = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "- "))
		label, value, found := strings.Cut(line, ":")
		if found && strings.EqualFold(strings.Trim(label, "* "), key) {
			return projectDevelopmentExcerpt(strings.Trim(strings.TrimSpace(value), "`"))
		}
	}
	return ""
}

func (r *projectDevelopmentReader) observeGit(root string) *ProjectDevelopmentGit {
	if _, err := r.root.Lstat(".git"); err != nil {
		return nil
	}
	runner := OSGitCommandRunner{}
	top, err := runGitPath(r.ctx, runner, root, "rev-parse", "--show-toplevel")
	if err != nil || !sameResolvedPath(top, root) {
		return nil
	}
	result := &ProjectDevelopmentGit{Posture: "unavailable"}
	head, err := runner.Run(r.ctx, root, "rev-parse", "--verify", "HEAD")
	if err != nil {
		result.Posture = "uncommitted"
		return result
	}
	result.Head = strings.TrimSpace(string(head))
	if !isGitObjectID(result.Head) {
		return &ProjectDevelopmentGit{Posture: "unavailable"}
	}
	names := make([]string, 0, len(r.state.Documents))
	for _, doc := range r.state.Documents {
		names = append(names, doc.Path)
	}
	sort.Strings(names)
	args := append([]string{"--literal-pathspecs", "ls-tree", "-z", result.Head, "--"}, names...)
	output, err := runner.Run(r.ctx, root, args...)
	if err != nil {
		return result
	}
	matched, deleted := 0, false
	for _, record := range bytes.Split(output, []byte{0}) {
		if len(record) == 0 {
			continue
		}
		prefix, name, ok := strings.Cut(string(record), "\t")
		fields := strings.Fields(prefix)
		if !ok || len(fields) != 3 {
			return result
		}
		blob, exists := r.blobs[name]
		if !exists {
			deleted = true
		}
		if exists && fields[1] == "blob" && (fields[0] == "100644" || fields[0] == "100755") && (fields[2] == blob[0] || fields[2] == blob[1]) {
			matched++
		}
	}
	result.Posture = "uncommitted"
	if matched == len(r.blobs) && !deleted {
		result.Commit, result.Posture = result.Head, "committed"
	}
	return result
}
