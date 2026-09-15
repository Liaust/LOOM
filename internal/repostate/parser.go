package repostate

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"golang.org/x/sys/unix"
	"gopkg.in/yaml.v3"
)

const (
	maxRepositoryStateFiles = 4096
	maxRepositoryStateFile  = 2 * 1024 * 1024
	maxRepositoryStateBytes = 16 * 1024 * 1024
	maxRepositoryStateNodes = 16384
	maxRepositoryStateDepth = 16
	maxRepositoryTreeNodes  = 16384
)

type ParseIssueStage string

const (
	ParseIssueEnvelope ParseIssueStage = "envelope"
	ParseIssueBody     ParseIssueStage = "body"
)

// ParseIssue is a stable, non-sensitive description of malformed source.
// Detail is intentionally bounded and never includes file contents.
type ParseIssue struct {
	Stage  ParseIssueStage `json:"stage"`
	Code   string          `json:"code"`
	Path   string          `json:"path,omitempty"`
	Detail string          `json:"detail,omitempty"`
}

// ParsedSource retains the exact source identity needed for field-level
// provenance. Content is private to this package and is never projected.
type ParsedSource struct {
	Path   string `json:"path"`
	Digest string `json:"digest"`
	data   []byte
}

type ParsedSection struct {
	Heading string   `json:"heading"`
	Text    string   `json:"text,omitempty"`
	Items   []string `json:"items"`
}

type ParsedDocument struct {
	Title        string                   `json:"title,omitempty"`
	Sections     map[string]ParsedSection `json:"sections"`
	LastReviewed *time.Time               `json:"last_reviewed,omitempty"`
	Source       ParsedSource             `json:"source"`
}

type ParsedLifecycleObject struct {
	Kind       ObjectKind     `json:"kind"`
	Directory  string         `json:"directory"`
	Fields     map[string]any `json:"fields"`
	Source     ParsedSource   `json:"source"`
	ParseIssue *ParseIssue    `json:"parse_issue,omitempty"`
	raw        map[string]any
}

// ParsedRepositoryState is a read-only view of the portable source. Absent
// .repo is represented by Enabled=false and is not an error.
type ParsedRepositoryState struct {
	Enabled            bool                    `json:"enabled"`
	EnvelopeKind       string                  `json:"envelope_kind,omitempty"`
	SchemaVersion      string                  `json:"schema_version,omitempty"`
	UnsupportedVersion bool                    `json:"unsupported_version"`
	Manifest           *RepositoryManifest     `json:"manifest,omitempty"`
	RepositorySummary  *ParsedDocument         `json:"repository_summary,omitempty"`
	State              *ParsedDocument         `json:"state,omitempty"`
	Roadmap            *ParsedDocument         `json:"roadmap,omitempty"`
	Lifecycle          []ParsedLifecycleObject `json:"lifecycle"`
	Integrations       []ParsedDocument        `json:"integrations"`
	Handoffs           []ParsedDocument        `json:"handoffs"`
	Acceptance         []ParsedDocument        `json:"acceptance"`
	Issues             []ParseIssue            `json:"issues"`
	Sources            map[string]ParsedSource `json:"sources"`
	gitObservationSafe bool
}

type Parser struct {
	afterStateRootOpen func()
	beforeSourceOpen   func(string)
}

// Parse reads only repositoryRoot/.repo. It never creates, rewrites, stages,
// or commits source files.
func (parser Parser) Parse(repositoryRoot string) ParsedRepositoryState {
	result := ParsedRepositoryState{
		Lifecycle:    []ParsedLifecycleObject{},
		Integrations: []ParsedDocument{},
		Handoffs:     []ParsedDocument{},
		Acceptance:   []ParsedDocument{},
		Issues:       []ParseIssue{},
		Sources:      map[string]ParsedSource{},
	}
	repositoryRoot = filepath.Clean(strings.TrimSpace(repositoryRoot))
	if repositoryRoot == "." || !filepath.IsAbs(repositoryRoot) {
		result.Issues = append(result.Issues, parseIssue(ParseIssueEnvelope, "invalid_repository_root", "", "repository root must be absolute"))
		return result
	}

	stateRoot, present, err := openStateRoot(repositoryRoot, parser.afterStateRootOpen)
	if !present {
		return result
	}
	result.Enabled = true
	if err != nil {
		code := "state_root_unreadable"
		detail := "unable to inspect the repository state root"
		if confinedErrorHasKind(err, confinedRootNotDirectory) {
			code = "state_root_not_directory"
			detail = "state root must be a real directory"
		} else if confinedErrorHasKind(err, confinedReplaced) {
			code = "state_root_replaced"
			detail = "state root changed while it was being inspected"
		}
		result.Issues = append(result.Issues, parseIssue(ParseIssueEnvelope, code, StateRoot, detail))
		return result
	}
	defer stateRoot.Close()

	manifestPayload, manifestIdentity, err := stateRoot.readRegularFile(RepositoryManifestPath, nil, maxRepositoryStateFile, parser.beforeSourceOpen)
	if err != nil {
		if confinedErrorHasKind(err, confinedMissing) {
			result.Issues = append(result.Issues, parseIssue(ParseIssueEnvelope, "manifest_missing", RepositoryManifestPath, "required repository manifest is absent"))
		} else {
			result.Issues = append(result.Issues, parseIssue(ParseIssueEnvelope, "manifest_envelope_malformed", RepositoryManifestPath, "required repository manifest is not a stable regular file"))
		}
		return result
	}
	manifestDigest := sha256.Sum256(manifestPayload)
	manifestSource := ParsedSource{
		Path:   RepositoryManifestPath,
		Digest: fmt.Sprintf("sha256:%x", manifestDigest[:]),
		data:   manifestPayload,
	}
	result.Sources[RepositoryManifestPath] = manifestSource
	if len(manifestPayload) == 0 {
		result.Issues = append(result.Issues, parseIssue(ParseIssueEnvelope, "manifest_missing", RepositoryManifestPath, "required repository manifest is absent"))
		return result
	}
	document, envelopeKind, schemaVersion, err := decodeManifestEnvelope(manifestSource.data)
	if err != nil {
		result.Issues = append(result.Issues, parseIssue(ParseIssueEnvelope, "manifest_envelope_malformed", RepositoryManifestPath, err.Error()))
		return result
	}
	result.EnvelopeKind = envelopeKind
	result.SchemaVersion = schemaVersion
	if result.EnvelopeKind != RepositoryManifestKind || result.SchemaVersion == "" {
		result.Issues = append(result.Issues, parseIssue(ParseIssueEnvelope, "manifest_envelope_malformed", RepositoryManifestPath, "kind and schema_version must identify the repository state contract"))
		return result
	}
	if result.SchemaVersion != RepositorySchemaVersion {
		result.UnsupportedVersion = true
		return result
	}
	document, err = decodeYAMLDocument(manifestSource.data)
	if err != nil {
		result.Issues = append(result.Issues, parseIssue(ParseIssueBody, "manifest_body_malformed", RepositoryManifestPath, err.Error()))
		return result
	}
	root := yamlRootMapping(document)

	var manifest RepositoryManifest
	if err := decodeKnownYAML(manifestSource.data, &manifest); err != nil {
		result.Issues = append(result.Issues, parseIssue(ParseIssueBody, "manifest_body_malformed", RepositoryManifestPath, err.Error()))
		return result
	} else if err := validateRepositoryManifestShape(root); err != nil {
		result.Issues = append(result.Issues, parseIssue(ParseIssueBody, "manifest_body_malformed", RepositoryManifestPath, err.Error()))
		return result
	}
	result.Manifest = &manifest

	if !result.readSources(stateRoot, manifestSource, manifestIdentity, parser.beforeSourceOpen) {
		return result
	}
	result.gitObservationSafe = true

	result.RepositorySummary = result.documentAt(".repo/REPOSITORY.md")
	result.State = result.documentAt(".repo/STATE.md")
	result.Roadmap = result.documentAt(".repo/ROADMAP.md")
	result.parseLifecycle()
	result.parseEvidenceDocuments()
	sort.Slice(result.Issues, func(left, right int) bool {
		if result.Issues[left].Stage != result.Issues[right].Stage {
			return result.Issues[left].Stage < result.Issues[right].Stage
		}
		if result.Issues[left].Path != result.Issues[right].Path {
			return result.Issues[left].Path < result.Issues[right].Path
		}
		return result.Issues[left].Code < result.Issues[right].Code
	})
	return result
}

func (result *ParsedRepositoryState) readSources(stateRoot *stateRootHandle, manifest ParsedSource, manifestIdentity confinedIdentity, beforeOpen func(string)) bool {
	sources := map[string]ParsedSource{RepositoryManifestPath: manifest}
	issues := []ParseIssue{}
	totalBytes := 0
	fileCount := 0
	err := stateRoot.walkFiles(func(sourcePath string, identity confinedIdentity) error {
		fileCount++
		if fileCount > maxRepositoryStateFiles {
			return newConfinedError(confinedTooLarge, sourcePath, fmt.Errorf("repository state exceeds %d files", maxRepositoryStateFiles))
		}
		if sourcePath == RepositoryManifestPath && !sameConfinedIdentity(identity, manifestIdentity) {
			return newConfinedError(confinedReplaced, sourcePath, errors.New("manifest identity changed after envelope validation"))
		}
		payload, _, err := stateRoot.readRegularFile(sourcePath, &identity, maxRepositoryStateFile, beforeOpen)
		if err != nil {
			return err
		}
		if sourcePath == RepositoryManifestPath && !bytes.Equal(payload, manifest.data) {
			return newConfinedError(confinedReplaced, sourcePath, errors.New("manifest changed after envelope validation"))
		}
		totalBytes += len(payload)
		if totalBytes > maxRepositoryStateBytes {
			return newConfinedError(confinedTooLarge, sourcePath, fmt.Errorf("repository state exceeds %d bytes", maxRepositoryStateBytes))
		}
		digest := sha256.Sum256(payload)
		sources[sourcePath] = ParsedSource{Path: sourcePath, Digest: fmt.Sprintf("sha256:%x", digest[:]), data: payload}
		if containsHostAbsolutePath(payload) {
			issues = append(issues, parseIssue(ParseIssueBody, "absolute_path_forbidden", sourcePath, "portable repository state contains a host-absolute path"))
		}
		return nil
	}, func(sourcePath string) {
		issues = append(issues, parseIssue(ParseIssueBody, "non_regular_source", sourcePath, "only real regular files are portable source"))
	})
	if err != nil {
		code := "state_root_unreadable"
		path := StateRoot
		detail := "unable to read the bounded repository state source set"
		if confinedErrorHasKind(err, confinedReplaced) {
			code = "source_replaced"
			path = confinedErrorPath(err, StateRoot)
			detail = "portable source changed while it was being inspected"
		}
		result.Issues = append(result.Issues, parseIssue(ParseIssueBody, code, path, detail))
		return false
	}
	result.Sources = sources
	result.Issues = append(result.Issues, issues...)
	return true
}

func (result *ParsedRepositoryState) documentAt(path string) *ParsedDocument {
	source, ok := result.Sources[path]
	if !ok {
		return nil
	}
	document := parseMarkdownDocument(source)
	return &document
}

func (result *ParsedRepositoryState) parseLifecycle() {
	schemas := map[string]LifecycleSchema{}
	for _, schema := range SchemaV1().LifecycleSchemas {
		schemas[string(schema.Kind)] = schema
	}
	paths := sortedSourcePaths(result.Sources)
	for _, sourcePath := range paths {
		parts := strings.Split(sourcePath, "/")
		if len(parts) != 4 || parts[0] != StateRoot {
			continue
		}
		kind := ObjectKind(strings.TrimSuffix(parts[1], "s"))
		schema, ok := schemas[string(kind)]
		if !ok || parts[3] != lifecycleManifestName(kind) {
			continue
		}
		source := result.Sources[sourcePath]
		object := ParsedLifecycleObject{Kind: kind, Directory: parts[2], Fields: map[string]any{}, Source: source, raw: map[string]any{}}
		if err := decodeKnownLifecycleYAML(source.data, schema, &object.raw); err != nil {
			issue := parseIssue(ParseIssueBody, "lifecycle_manifest_malformed", sourcePath, err.Error())
			object.ParseIssue = &issue
			result.Issues = append(result.Issues, issue)
		} else {
			object.Fields = cloneAnyMap(object.raw)
		}
		result.Lifecycle = append(result.Lifecycle, object)
	}
}

func (result *ParsedRepositoryState) parseEvidenceDocuments() {
	paths := sortedSourcePaths(result.Sources)
	for _, sourcePath := range paths {
		if !strings.HasSuffix(sourcePath, ".md") {
			continue
		}
		source := result.Sources[sourcePath]
		document := parseMarkdownDocument(source)
		switch {
		case strings.HasPrefix(sourcePath, ".repo/integrations/"):
			result.Integrations = append(result.Integrations, document)
		case strings.HasPrefix(sourcePath, ".repo/features/") && strings.HasSuffix(sourcePath, "/handoff.md"):
			result.Handoffs = append(result.Handoffs, document)
		case (strings.HasPrefix(sourcePath, ".repo/features/") || strings.HasPrefix(sourcePath, ".repo/releases/")) && isAcceptanceDocument(sourcePath):
			result.Acceptance = append(result.Acceptance, document)
		}
	}
}

func parseMarkdownDocument(source ParsedSource) ParsedDocument {
	document := ParsedDocument{Sections: map[string]ParsedSection{}, Source: source}
	lines := strings.Split(strings.ReplaceAll(string(source.data), "\r\n", "\n"), "\n")
	current := ""
	sectionLines := map[string][]string{}
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if document.Title == "" && strings.HasPrefix(trimmed, "# ") {
			document.Title = strings.TrimSpace(strings.TrimPrefix(trimmed, "# "))
			continue
		}
		if strings.HasPrefix(trimmed, "## ") {
			current = normalizeHeading(strings.TrimSpace(strings.TrimPrefix(trimmed, "## ")))
			if _, exists := sectionLines[current]; !exists {
				sectionLines[current] = []string{}
			}
			continue
		}
		if reviewed, ok := parseLastReviewed(trimmed); ok {
			document.LastReviewed = &reviewed
		}
		if current != "" {
			sectionLines[current] = append(sectionLines[current], line)
		}
	}
	for key, rawLines := range sectionLines {
		section := ParsedSection{Heading: key, Items: []string{}}
		paragraphs := make([]string, 0, len(rawLines))
		for _, line := range rawLines {
			trimmed := strings.TrimSpace(line)
			if trimmed == "" {
				continue
			}
			if item, ok := markdownListItem(trimmed); ok {
				section.Items = append(section.Items, item)
				continue
			}
			paragraphs = append(paragraphs, trimmed)
		}
		section.Text = strings.Join(paragraphs, "\n")
		document.Sections[key] = section
	}
	return document
}

func decodeYAMLDocument(payload []byte) (*yaml.Node, error) {
	decoder := yaml.NewDecoder(bytes.NewReader(payload))
	var document yaml.Node
	if err := decoder.Decode(&document); err != nil {
		return nil, err
	}
	var trailing yaml.Node
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, fmt.Errorf("YAML must contain exactly one document")
	}
	count := 0
	if err := validateYAMLNode(&document, &count); err != nil {
		return nil, err
	}
	return &document, nil
}

func decodeManifestEnvelope(payload []byte) (*yaml.Node, string, string, error) {
	decoder := yaml.NewDecoder(bytes.NewReader(payload))
	var document yaml.Node
	if err := decoder.Decode(&document); err != nil {
		return nil, "", "", err
	}
	var trailing yaml.Node
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, "", "", fmt.Errorf("YAML must contain exactly one document")
	}
	count := 0
	if err := countBoundedYAMLNodes(&document, &count); err != nil {
		return nil, "", "", err
	}
	root := yamlRootMapping(&document)
	if root == nil {
		return nil, "", "", fmt.Errorf("manifest root must be a mapping")
	}
	values := map[string]string{}
	seen := map[string]bool{}
	for index := 0; index+1 < len(root.Content); index += 2 {
		key := root.Content[index]
		if key.Kind != yaml.ScalarNode || key.Tag != "!!str" || (key.Value != "kind" && key.Value != "schema_version") {
			continue
		}
		if seen[key.Value] {
			return nil, "", "", fmt.Errorf("duplicate YAML field %q", key.Value)
		}
		seen[key.Value] = true
		value := root.Content[index+1]
		if value.Kind != yaml.ScalarNode || value.Tag != "!!str" {
			return nil, "", "", fmt.Errorf("manifest envelope field %q must be a string", key.Value)
		}
		values[key.Value] = value.Value
	}
	return &document, values["kind"], values["schema_version"], nil
}

func countBoundedYAMLNodes(node *yaml.Node, count *int) error {
	if node == nil {
		return nil
	}
	(*count)++
	if *count > maxRepositoryStateNodes {
		return fmt.Errorf("YAML exceeds bounded node limit")
	}
	for _, child := range node.Content {
		if err := countBoundedYAMLNodes(child, count); err != nil {
			return err
		}
	}
	return nil
}

func decodeKnownYAML(payload []byte, target any) error {
	decoder := yaml.NewDecoder(bytes.NewReader(payload))
	decoder.KnownFields(true)
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing yaml.Node
	if err := decoder.Decode(&trailing); err != io.EOF {
		return fmt.Errorf("YAML must contain exactly one document")
	}
	return nil
}

func decodeKnownLifecycleYAML(payload []byte, schema LifecycleSchema, target *map[string]any) error {
	document, err := decodeYAMLDocument(payload)
	if err != nil {
		return err
	}
	root := yamlRootMapping(document)
	if root == nil {
		return fmt.Errorf("lifecycle manifest root must be a mapping")
	}
	allowed := make(map[string]struct{}, len(schema.RequiredFields)+len(schema.OptionalFields))
	for _, field := range append(append([]string(nil), schema.RequiredFields...), schema.OptionalFields...) {
		allowed[field] = struct{}{}
	}
	for index := 0; index < len(root.Content); index += 2 {
		field := root.Content[index].Value
		if _, ok := allowed[field]; !ok {
			return fmt.Errorf("unknown field %q", field)
		}
	}
	if err := root.Decode(target); err != nil {
		return err
	}
	return nil
}

func validateYAMLNode(node *yaml.Node, count *int) error {
	if node == nil {
		return nil
	}
	(*count)++
	if *count > maxRepositoryStateNodes {
		return fmt.Errorf("YAML exceeds bounded node limit")
	}
	if node.Kind == yaml.AliasNode || node.Anchor != "" {
		return fmt.Errorf("YAML aliases and anchors are not allowed")
	}
	if node.Kind == yaml.MappingNode {
		if len(node.Content)%2 != 0 {
			return fmt.Errorf("YAML mapping is malformed")
		}
		seen := make(map[string]struct{}, len(node.Content)/2)
		for index := 0; index < len(node.Content); index += 2 {
			key := node.Content[index]
			if key.Kind != yaml.ScalarNode || key.Tag != "!!str" {
				return fmt.Errorf("YAML mapping keys must be strings")
			}
			if _, duplicate := seen[key.Value]; duplicate {
				return fmt.Errorf("duplicate YAML field %q", key.Value)
			}
			seen[key.Value] = struct{}{}
		}
	}
	for _, child := range node.Content {
		if err := validateYAMLNode(child, count); err != nil {
			return err
		}
	}
	return nil
}

func yamlRootMapping(document *yaml.Node) *yaml.Node {
	if document == nil || document.Kind != yaml.DocumentNode || len(document.Content) != 1 {
		return nil
	}
	root := document.Content[0]
	if root.Kind != yaml.MappingNode {
		return nil
	}
	return root
}

func validateRepositoryManifestShape(root *yaml.Node) error {
	for _, field := range []string{"kind", "schema_version", "repository", "owner_project", "source"} {
		if yamlMappingNode(root, field) == nil {
			return fmt.Errorf("required field %q is missing", field)
		}
	}
	repository := yamlMappingNode(root, "repository")
	if repository.Kind != yaml.MappingNode {
		return fmt.Errorf("repository must be a mapping")
	}
	for _, field := range []string{"id", "name", "aliases", "role", "purpose", "topics"} {
		value := yamlMappingNode(repository, field)
		if value == nil {
			return fmt.Errorf("required field repository.%s is missing", field)
		}
		if (field == "aliases" || field == "topics") && value.Kind != yaml.SequenceNode {
			return fmt.Errorf("repository.%s must be a sequence", field)
		}
	}
	owner := yamlMappingNode(root, "owner_project")
	if owner.Kind != yaml.MappingNode || yamlMappingNode(owner, "id") == nil || yamlMappingNode(owner, "slug") == nil {
		return fmt.Errorf("owner_project must contain id and slug")
	}
	source := yamlMappingNode(root, "source")
	if source.Kind != yaml.MappingNode || yamlMappingNode(source, "tracking") == nil || yamlMappingNode(source, "state_root") == nil {
		return fmt.Errorf("source must contain tracking and state_root")
	}
	return nil
}

func yamlMappingNode(mapping *yaml.Node, key string) *yaml.Node {
	if mapping == nil || mapping.Kind != yaml.MappingNode {
		return nil
	}
	for index := 0; index+1 < len(mapping.Content); index += 2 {
		if mapping.Content[index].Value == key {
			return mapping.Content[index+1]
		}
	}
	return nil
}

type confinedErrorKind string

const (
	confinedMissing          confinedErrorKind = "missing"
	confinedNonRegular       confinedErrorKind = "non_regular"
	confinedReplaced         confinedErrorKind = "replaced"
	confinedUnreadable       confinedErrorKind = "unreadable"
	confinedTooLarge         confinedErrorKind = "too_large"
	confinedRootNotDirectory confinedErrorKind = "root_not_directory"
)

type confinedPathError struct {
	kind confinedErrorKind
	path string
	err  error
}

func (err *confinedPathError) Error() string {
	return fmt.Sprintf("confined repository state read failed for %s: %s", err.path, err.kind)
}

func (err *confinedPathError) Unwrap() error { return err.err }

func newConfinedError(kind confinedErrorKind, path string, err error) error {
	return &confinedPathError{kind: kind, path: path, err: err}
}

func confinedErrorHasKind(err error, kind confinedErrorKind) bool {
	var confined *confinedPathError
	return errors.As(err, &confined) && confined.kind == kind
}

func confinedErrorPath(err error, fallback string) string {
	var confined *confinedPathError
	if errors.As(err, &confined) && confined.path != "" {
		return confined.path
	}
	return fallback
}

type confinedIdentity struct {
	device uint64
	inode  uint64
	kind   uint32
}

func identityFromStat(stat *unix.Stat_t) confinedIdentity {
	return confinedIdentity{
		device: uint64(stat.Dev),
		inode:  uint64(stat.Ino),
		kind:   uint32(stat.Mode) & unix.S_IFMT,
	}
}

func sameConfinedIdentity(left, right confinedIdentity) bool {
	return left.device == right.device && left.inode == right.inode && left.kind == right.kind
}

type stateRootHandle struct {
	repositoryDirectory *os.File
	stateDirectory      *os.File
	identity            confinedIdentity
}

func openStateRoot(repositoryRoot string, afterOpen func()) (*stateRootHandle, bool, error) {
	repositoryDirectory, err := os.Open(repositoryRoot)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, false, nil
		}
		return nil, true, newConfinedError(confinedUnreadable, StateRoot, err)
	}
	repositoryInfo, err := repositoryDirectory.Stat()
	if err != nil || !repositoryInfo.IsDir() {
		_ = repositoryDirectory.Close()
		if err == nil {
			err = errors.New("repository root is not a directory")
		}
		return nil, true, newConfinedError(confinedUnreadable, StateRoot, err)
	}

	stateFD, err := unix.Openat(int(repositoryDirectory.Fd()), StateRoot, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		_ = repositoryDirectory.Close()
		if errors.Is(err, unix.ENOENT) {
			return nil, false, nil
		}
		kind := confinedUnreadable
		if errors.Is(err, unix.ELOOP) || errors.Is(err, unix.ENOTDIR) {
			kind = confinedRootNotDirectory
		}
		return nil, true, newConfinedError(kind, StateRoot, err)
	}
	stateDirectory := os.NewFile(uintptr(stateFD), StateRoot)
	var stateStat unix.Stat_t
	if err := unix.Fstat(stateFD, &stateStat); err != nil {
		_ = stateDirectory.Close()
		_ = repositoryDirectory.Close()
		return nil, true, newConfinedError(confinedUnreadable, StateRoot, err)
	}
	handle := &stateRootHandle{
		repositoryDirectory: repositoryDirectory,
		stateDirectory:      stateDirectory,
		identity:            identityFromStat(&stateStat),
	}
	if afterOpen != nil {
		afterOpen()
	}
	if err := handle.verifyRootBinding(); err != nil {
		_ = handle.Close()
		return nil, true, err
	}
	return handle, true, nil
}

func (root *stateRootHandle) Close() error {
	stateErr := root.stateDirectory.Close()
	repositoryErr := root.repositoryDirectory.Close()
	if stateErr != nil {
		return stateErr
	}
	return repositoryErr
}

func (root *stateRootHandle) verifyRootBinding() error {
	var held unix.Stat_t
	if err := unix.Fstat(int(root.stateDirectory.Fd()), &held); err != nil {
		return newConfinedError(confinedReplaced, StateRoot, err)
	}
	var current unix.Stat_t
	if err := unix.Fstatat(int(root.repositoryDirectory.Fd()), StateRoot, &current, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return newConfinedError(confinedReplaced, StateRoot, err)
	}
	if !sameConfinedIdentity(root.identity, identityFromStat(&held)) || !sameConfinedIdentity(root.identity, identityFromStat(&current)) || root.identity.kind != unix.S_IFDIR {
		return newConfinedError(confinedReplaced, StateRoot, errors.New("state root identity changed"))
	}
	return nil
}

type directoryBinding struct {
	parentIndex int
	name        string
	identity    confinedIdentity
}

type boundDirectory struct {
	root     *stateRootHandle
	files    []*os.File
	bindings []directoryBinding
}

func (directory *boundDirectory) current() *os.File {
	return directory.files[len(directory.files)-1]
}

func (directory *boundDirectory) Close() {
	for index := len(directory.files) - 1; index >= 0; index-- {
		_ = directory.files[index].Close()
	}
}

func (directory *boundDirectory) verify() error {
	if err := directory.root.verifyRootBinding(); err != nil {
		return err
	}
	for index, binding := range directory.bindings {
		var current unix.Stat_t
		if err := unix.Fstatat(int(directory.files[binding.parentIndex].Fd()), binding.name, &current, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			return newConfinedError(confinedReplaced, StateRoot, err)
		}
		if !sameConfinedIdentity(binding.identity, identityFromStat(&current)) {
			return newConfinedError(confinedReplaced, StateRoot, errors.New("directory path identity changed"))
		}
		var held unix.Stat_t
		if err := unix.Fstat(int(directory.files[index+1].Fd()), &held); err != nil || !sameConfinedIdentity(binding.identity, identityFromStat(&held)) {
			return newConfinedError(confinedReplaced, StateRoot, errors.New("held directory identity changed"))
		}
	}
	return directory.root.verifyRootBinding()
}

func (root *stateRootHandle) openDirectory(components []string, expected *confinedIdentity) (*boundDirectory, error) {
	rootFD, err := unix.Dup(int(root.stateDirectory.Fd()))
	if err != nil {
		return nil, newConfinedError(confinedUnreadable, StateRoot, err)
	}
	directory := &boundDirectory{root: root, files: []*os.File{os.NewFile(uintptr(rootFD), StateRoot)}}
	for index, component := range components {
		if !validConfinedComponent(component) {
			directory.Close()
			return nil, newConfinedError(confinedUnreadable, StateRoot, errors.New("invalid path component"))
		}
		parent := directory.current()
		var inspected unix.Stat_t
		if err := unix.Fstatat(int(parent.Fd()), component, &inspected, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			directory.Close()
			kind := confinedUnreadable
			if expected != nil && index == len(components)-1 {
				kind = confinedReplaced
			}
			return nil, newConfinedError(kind, StateRoot, err)
		}
		identity := identityFromStat(&inspected)
		if identity.kind != unix.S_IFDIR {
			directory.Close()
			return nil, newConfinedError(confinedNonRegular, StateRoot, errors.New("path component is not a real directory"))
		}
		if expected != nil && index == len(components)-1 && !sameConfinedIdentity(identity, *expected) {
			directory.Close()
			return nil, newConfinedError(confinedReplaced, StateRoot, errors.New("directory identity changed before open"))
		}
		childFD, err := unix.Openat(int(parent.Fd()), component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if err != nil {
			directory.Close()
			return nil, newConfinedError(confinedReplaced, StateRoot, err)
		}
		child := os.NewFile(uintptr(childFD), component)
		var opened unix.Stat_t
		if err := unix.Fstat(childFD, &opened); err != nil || !sameConfinedIdentity(identity, identityFromStat(&opened)) {
			_ = child.Close()
			directory.Close()
			return nil, newConfinedError(confinedReplaced, StateRoot, errors.New("directory identity changed while opening"))
		}
		directory.bindings = append(directory.bindings, directoryBinding{parentIndex: len(directory.files) - 1, name: component, identity: identity})
		directory.files = append(directory.files, child)
	}
	if err := directory.verify(); err != nil {
		directory.Close()
		return nil, err
	}
	return directory, nil
}

func (root *stateRootHandle) readRegularFile(sourcePath string, expected *confinedIdentity, limit int, beforeOpen func(string)) ([]byte, confinedIdentity, error) {
	components, err := confinedSourceComponents(sourcePath)
	if err != nil {
		return nil, confinedIdentity{}, err
	}
	parent, err := root.openDirectory(components[:len(components)-1], nil)
	if err != nil {
		return nil, confinedIdentity{}, err
	}
	defer parent.Close()
	name := components[len(components)-1]
	var inspected unix.Stat_t
	if err := unix.Fstatat(int(parent.current().Fd()), name, &inspected, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		kind := confinedMissing
		if expected != nil {
			kind = confinedReplaced
		}
		return nil, confinedIdentity{}, newConfinedError(kind, sourcePath, err)
	}
	identity := identityFromStat(&inspected)
	if identity.kind != unix.S_IFREG {
		return nil, confinedIdentity{}, newConfinedError(confinedNonRegular, sourcePath, errors.New("source is not a real regular file"))
	}
	if expected != nil && !sameConfinedIdentity(identity, *expected) {
		return nil, confinedIdentity{}, newConfinedError(confinedReplaced, sourcePath, errors.New("source identity changed before open"))
	}
	if beforeOpen != nil {
		beforeOpen(sourcePath)
	}
	if err := parent.verify(); err != nil {
		return nil, confinedIdentity{}, err
	}
	var current unix.Stat_t
	if err := unix.Fstatat(int(parent.current().Fd()), name, &current, unix.AT_SYMLINK_NOFOLLOW); err != nil || !sameConfinedIdentity(identity, identityFromStat(&current)) {
		return nil, confinedIdentity{}, newConfinedError(confinedReplaced, sourcePath, errors.New("source identity changed before open"))
	}
	fileFD, err := unix.Openat(int(parent.current().Fd()), name, unix.O_RDONLY|unix.O_NONBLOCK|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, confinedIdentity{}, newConfinedError(confinedReplaced, sourcePath, err)
	}
	file := os.NewFile(uintptr(fileFD), sourcePath)
	defer file.Close()
	var opened unix.Stat_t
	if err := unix.Fstat(fileFD, &opened); err != nil || !sameConfinedIdentity(identity, identityFromStat(&opened)) || identity.kind != unix.S_IFREG {
		return nil, confinedIdentity{}, newConfinedError(confinedReplaced, sourcePath, errors.New("source identity changed while opening"))
	}
	payload, err := readBoundedReader(file, limit)
	if err != nil {
		kind := confinedUnreadable
		if strings.Contains(err.Error(), "exceeds") {
			kind = confinedTooLarge
		}
		return nil, confinedIdentity{}, newConfinedError(kind, sourcePath, err)
	}
	var after unix.Stat_t
	if err := unix.Fstat(fileFD, &after); err != nil || !sameConfinedIdentity(identity, identityFromStat(&after)) {
		return nil, confinedIdentity{}, newConfinedError(confinedReplaced, sourcePath, errors.New("source identity changed while reading"))
	}
	if err := unix.Fstatat(int(parent.current().Fd()), name, &current, unix.AT_SYMLINK_NOFOLLOW); err != nil || !sameConfinedIdentity(identity, identityFromStat(&current)) {
		return nil, confinedIdentity{}, newConfinedError(confinedReplaced, sourcePath, errors.New("source path changed while reading"))
	}
	if err := parent.verify(); err != nil {
		return nil, confinedIdentity{}, err
	}
	return payload, identity, nil
}

func (root *stateRootHandle) walkFiles(onFile func(string, confinedIdentity) error, onNonRegular func(string)) error {
	nodes := 0
	return root.walkDirectory(nil, "", nil, &nodes, onFile, onNonRegular)
}

func (root *stateRootHandle) walkDirectory(components []string, relative string, expected *confinedIdentity, nodes *int, onFile func(string, confinedIdentity) error, onNonRegular func(string)) error {
	if len(components) > maxRepositoryStateDepth {
		return newConfinedError(confinedTooLarge, StateRoot, fmt.Errorf("repository state exceeds %d directory levels", maxRepositoryStateDepth))
	}
	directory, err := root.openDirectory(components, expected)
	if err != nil {
		return err
	}
	defer directory.Close()
	entries, err := directory.current().ReadDir(-1)
	if err != nil {
		return newConfinedError(confinedUnreadable, StateRoot, err)
	}
	sort.Slice(entries, func(left, right int) bool { return entries[left].Name() < entries[right].Name() })
	for _, entry := range entries {
		(*nodes)++
		if *nodes > maxRepositoryTreeNodes {
			return newConfinedError(confinedTooLarge, StateRoot, fmt.Errorf("repository state exceeds %d tree entries", maxRepositoryTreeNodes))
		}
		name := entry.Name()
		if !validConfinedComponent(name) {
			return newConfinedError(confinedUnreadable, StateRoot, errors.New("invalid directory entry"))
		}
		var inspected unix.Stat_t
		if err := unix.Fstatat(int(directory.current().Fd()), name, &inspected, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			return newConfinedError(confinedReplaced, StateRoot, err)
		}
		identity := identityFromStat(&inspected)
		childRelative := name
		if relative != "" {
			childRelative = relative + "/" + name
		}
		sourcePath := StateRoot + "/" + childRelative
		switch identity.kind {
		case unix.S_IFDIR:
			if err := root.walkDirectory(append(append([]string(nil), components...), name), childRelative, &identity, nodes, onFile, onNonRegular); err != nil {
				return err
			}
		case unix.S_IFREG:
			if err := onFile(sourcePath, identity); err != nil {
				return err
			}
		default:
			onNonRegular(sourcePath)
		}
	}
	return directory.verify()
}

func confinedSourceComponents(sourcePath string) ([]string, error) {
	if !strings.HasPrefix(sourcePath, StateRoot+"/") || strings.Contains(sourcePath, "\\") || strings.ContainsRune(sourcePath, 0) {
		return nil, newConfinedError(confinedUnreadable, sourcePath, errors.New("source path is not confined to .repo"))
	}
	components := strings.Split(strings.TrimPrefix(sourcePath, StateRoot+"/"), "/")
	if len(components) == 0 {
		return nil, newConfinedError(confinedUnreadable, sourcePath, errors.New("source path is empty"))
	}
	for _, component := range components {
		if !validConfinedComponent(component) {
			return nil, newConfinedError(confinedUnreadable, sourcePath, errors.New("source path contains an invalid component"))
		}
	}
	return components, nil
}

func validConfinedComponent(component string) bool {
	return component != "" && component != "." && component != ".." && !strings.Contains(component, "/") && !strings.Contains(component, "\\") && !strings.ContainsRune(component, 0)
}

func readBoundedReader(reader io.Reader, limit int) ([]byte, error) {
	payload, err := io.ReadAll(io.LimitReader(reader, int64(limit)+1))
	if err != nil {
		return nil, err
	}
	if len(payload) > limit {
		return nil, fmt.Errorf("file exceeds %d bytes", limit)
	}
	return payload, nil
}

func parseIssue(stage ParseIssueStage, code, path, detail string) ParseIssue {
	detail = strings.TrimSpace(detail)
	if len(detail) > 256 {
		detail = detail[:256]
	}
	return ParseIssue{Stage: stage, Code: code, Path: path, Detail: detail}
}

func sortedSourcePaths(sources map[string]ParsedSource) []string {
	paths := make([]string, 0, len(sources))
	for path := range sources {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths
}

func lifecycleManifestName(kind ObjectKind) string {
	switch kind {
	case ObjectKindFuture:
		return "future.yaml"
	case ObjectKindInitiative:
		return "initiative.yaml"
	case ObjectKindFeature:
		return "feature.yaml"
	case ObjectKindDecision:
		return "decision.yaml"
	case ObjectKindRelease:
		return "release.yaml"
	default:
		return ""
	}
}

func normalizeHeading(value string) string {
	return strings.ToLower(strings.Join(strings.Fields(value), "_"))
}

var orderedListItem = regexp.MustCompile(`^[0-9]+[.)]\s+(.+)$`)

func markdownListItem(line string) (string, bool) {
	if strings.HasPrefix(line, "- ") || strings.HasPrefix(line, "* ") || strings.HasPrefix(line, "+ ") {
		return strings.TrimSpace(line[2:]), true
	}
	if matches := orderedListItem.FindStringSubmatch(line); len(matches) == 2 {
		return strings.TrimSpace(matches[1]), true
	}
	return "", false
}

func parseLastReviewed(line string) (time.Time, bool) {
	const prefix = "Last reviewed:"
	if !strings.HasPrefix(line, prefix) {
		return time.Time{}, false
	}
	value := strings.TrimSpace(strings.TrimPrefix(line, prefix))
	parsed, err := time.Parse("2006-01-02", strings.Trim(value, "`"))
	return parsed.UTC(), err == nil
}

func isAcceptanceDocument(path string) bool {
	base := strings.ToLower(filepath.Base(path))
	return strings.HasPrefix(base, "acceptance") && strings.HasSuffix(base, ".md")
}

func cloneAnyMap(input map[string]any) map[string]any {
	output := make(map[string]any, len(input))
	for key, value := range input {
		output[key] = value
	}
	return output
}

func anyString(fields map[string]any, key string) string {
	value, _ := fields[key].(string)
	return value
}

func sourceDate(fields map[string]any, key string) (time.Time, bool) {
	raw, ok := fields[key]
	if !ok {
		return time.Time{}, false
	}
	if parsed, ok := raw.(time.Time); ok {
		return parsed.UTC(), true
	}
	value := fmt.Sprint(raw)
	parsed, err := time.Parse("2006-01-02", value)
	return parsed.UTC(), err == nil
}

func containsHostAbsolutePath(payload []byte) bool {
	for index := 0; index < len(payload); index++ {
		if !absolutePathTokenBoundary(payload, index) {
			continue
		}
		if length := normalHTTPURLLength(payload[index:]); length > 0 {
			index += length - 1
			continue
		}
		remaining := payload[index:]
		if hasFoldedPrefix(remaining, "file:/") || hasFoldedPrefix(remaining, "git+file:/") {
			return true
		}
		if len(remaining) >= 3 && isASCIIAlpha(remaining[0]) && remaining[1] == ':' && (remaining[2] == '/' || remaining[2] == '\\') {
			return true
		}
		if len(remaining) >= 2 && ((remaining[0] == '\\' && remaining[1] == '\\') || (remaining[0] == '/' && remaining[1] == '/')) {
			if hasUNCServerAndShare(remaining) {
				return true
			}
			continue
		}
		if len(remaining) >= 2 && remaining[0] == '/' && !absolutePathTerminator(remaining[1]) {
			return true
		}
	}
	return false
}

func absolutePathTokenBoundary(payload []byte, index int) bool {
	if index == 0 {
		return true
	}
	previous := payload[index-1]
	return previous == ' ' || previous == '\t' || previous == '\r' || previous == '\n' || strings.ContainsRune(`"'`+"`"+`=:([{<,;|`, rune(previous))
}

func absolutePathTerminator(value byte) bool {
	return value == ' ' || value == '\t' || value == '\r' || value == '\n' || strings.ContainsRune(`"'`+"`"+`<>)]},;|`, rune(value))
}

func normalHTTPURLLength(payload []byte) int {
	prefixLength := 0
	switch {
	case hasFoldedPrefix(payload, "https://"):
		prefixLength = len("https://")
	case hasFoldedPrefix(payload, "http://"):
		prefixLength = len("http://")
	default:
		return 0
	}
	if len(payload) == prefixLength || absolutePathTerminator(payload[prefixLength]) {
		return 0
	}
	index := prefixLength
	for index < len(payload) && !absolutePathTerminator(payload[index]) {
		index++
	}
	return index
}

func hasFoldedPrefix(payload []byte, prefix string) bool {
	return len(payload) >= len(prefix) && strings.EqualFold(string(payload[:len(prefix)]), prefix)
}

func isASCIIAlpha(value byte) bool {
	return (value >= 'a' && value <= 'z') || (value >= 'A' && value <= 'Z')
}

func hasUNCServerAndShare(payload []byte) bool {
	separator := payload[0]
	index := 2
	serverStart := index
	for index < len(payload) && payload[index] != separator && !absolutePathTerminator(payload[index]) {
		index++
	}
	if index == serverStart || index >= len(payload) || payload[index] != separator {
		return false
	}
	index++
	return index < len(payload) && payload[index] != separator && !absolutePathTerminator(payload[index])
}

func stringIndex(values []string, value string) int {
	for index, candidate := range values {
		if candidate == value {
			return index
		}
	}
	return -1
}
