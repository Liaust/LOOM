// Package notesworkspace owns source-bound Notes saves, not device replication.
package notesworkspace

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"strings"
	"unicode/utf8"

	"loom.local/loom/internal/objectstore"

	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

const MaxContentBytes = 8 << 20
const MaxReferenceBytes = 64 << 20
const MaxStreamReferenceBytes int64 = 2 << 30
const (
	KindEdit   = "edit"
	KindRename = "rename"
	KindDelete = "delete"
	KindImport = "import"
	Pending    = "pending"
	Accepted   = "accepted"
	Conflict   = "conflict"
	Held       = "held"
)

var (
	ErrMembership          = errors.New("notes workspace membership unavailable or changed")
	ErrReferenceOnly       = errors.New("notes workspace source is reference-only")
	ErrIntentMismatch      = errors.New("notes workspace operation identity reused with different intent")
	ErrPathCommitUncertain = errors.New("notes workspace path commit outcome uncertain")
	ErrPathBusy            = errors.New("notes workspace path reserved by pending operation")
	ErrStalePath           = errors.New("notes workspace file path version changed or deleted")
	ErrUnsafePath          = errors.New("notes workspace unsafe source path")
	ErrReferenceLimit      = errors.New("notes workspace reference exceeds supported size")
)

type Source struct{ CollectionID, Generation, Path string }

// WithSource must hold current source/declaration/lifecycle fences throughout fn.
// Path is resolved by the trusted owner, never supplied by an edit request.
type Resolver interface {
	WithSource(context.Context, string, string, func(Source) error) error
}
type PathResolver interface {
	WithPaths(context.Context, string, []string, func(Source) error) error
}
type DirectoryResolver interface {
	// An empty list admits only the writable collection scope, not a mutable root.
	WithDirectoryPaths(context.Context, string, []string, func(Source) error) error
}

type DirectoryReadResolver interface {
	WithReadDirectoryPaths(context.Context, string, []string, func(Source) error) error
}

// RetainedResolver permits observation of finalized journals across an explicit
// enrollment transition. Receive/apply still require current Resolver fences.
type RetainedResolver interface {
	WithRetainedSource(context.Context, string, string, string, func(Source) error) error
}

type File struct {
	ID           string `json:"file_id"`
	CollectionID string `json:"collection_id"`
	RelativePath string `json:"relative_path"`
	PathKey      string `json:"path_key"`
	PathVersion  uint64 `json:"path_version,omitempty"`
	Deleted      bool   `json:"deleted,omitempty"`
	EntryKind    string `json:"entry_kind,omitempty"`
	// Deleted releases the writable binding; Retained distinguishes custody from deletion.
	Retained *RetainedLocation `json:"retained,omitempty"`
}

type RetainedLocation struct {
	OperationID string `json:"operation_id"`
	ScopeID     string `json:"scope_id"`
	Root        string `json:"root"`
	Path        string `json:"path"`
}
type Base struct {
	ID                string                  `json:"base_id"`
	FileID            string                  `json:"file_id"`
	Generation        string                  `json:"membership_generation"`
	Hash              string                  `json:"hash"`
	PathVersion       uint64                  `json:"path_version,omitempty"`
	Exists            bool                    `json:"exists"`
	Content           []byte                  `json:"content"`
	ReferenceSize     int64                   `json:"reference_size,omitempty"`
	DirectoryIdentity *inodeIdentity          `json:"directory_identity,omitempty"`
	MetadataAction    *MetadataActionIdentity `json:"metadata_action,omitempty"`
}
type Request struct {
	BoxID           string `json:"box_id,omitempty"`
	OperationID     string `json:"operation_id"`
	FileID          string `json:"file_id"`
	Generation      string `json:"membership_generation"`
	BaseID          string `json:"base_id"`
	Content         []byte `json:"content"`
	Kind            string `json:"kind,omitempty"`
	DestinationPath string `json:"destination_path,omitempty"`
	// DirectoryTargets lists collection-relative acknowledged destination children,
	// including their parent directories, for a rename after child operations.
	DirectoryTargets []string `json:"directory_targets,omitempty"`
	ImportHash       string   `json:"import_hash,omitempty"`
	ImportSize       int64    `json:"import_size,omitempty"`
}
type Operation struct {
	Request                 Request        `json:"request"`
	File                    File           `json:"file"`
	Base                    Base           `json:"base"`
	State                   string         `json:"state"`
	Reason                  string         `json:"reason,omitempty"`
	ResultBaseID            string         `json:"result_base_id,omitempty"`
	ResultFile              *File          `json:"result_file,omitempty"`
	PathStage               string         `json:"path_stage,omitempty"`
	DestinationKey          string         `json:"destination_key,omitempty"`
	MoveIdentity            *inodeIdentity `json:"move_identity,omitempty"`
	DirectoryTargetIdentity *inodeIdentity `json:"directory_target_identity,omitempty"`
	// Journal is relative to the source file's parent, never a client destination.
	Journal string `json:"journal,omitempty"`
	// Relocation changes only journal custody, never the original intent/receipt.
	JournalLocation *JournalLocation        `json:"journal_location,omitempty"`
	Observed        []byte                  `json:"observed_content,omitempty"`
	ObservedExists  bool                    `json:"observed_exists"`
	RetainedHash    string                  `json:"retained_hash,omitempty"`
	ImportBlob      *objectstore.StoredBlob `json:"import_blob,omitempty"`
	ImportParent    *inodeIdentity          `json:"import_parent,omitempty"`
}

type JournalLocation struct {
	ScopeID      string `json:"scope_id"`
	RelativePath string `json:"relative_path"`
}

// Recovery is append-only evidence; it never changes an accepted operation's
// receipt or writes either inode. A conflict includes both readable variants.
type Recovery struct {
	ID             string `json:"recovery_id"`
	OperationID    string `json:"operation_id"`
	State          string `json:"state"`
	Reason         string `json:"reason,omitempty"`
	Retained       []byte `json:"retained_content,omitempty"`
	RetainedExists bool   `json:"retained_exists"`
	Current        []byte `json:"current_content,omitempty"`
	CurrentExists  bool   `json:"current_exists"`
}

// DiscoveryPage uses operation ID keyset order. Pass Next to advance, including
// past held entries; an empty page ends a sweep. Start the next sweep at "".
// IDs added behind the cursor are seen on the next sweep, not skipped forever.
type DiscoveryPage struct {
	IDs  []string `json:"operation_ids"`
	Next string   `json:"next,omitempty"`
}

type PathChange struct {
	FileID      string            `json:"file_id"`
	OperationID string            `json:"operation_id"`
	Version     uint64            `json:"path_version"`
	FromPath    string            `json:"from_path"`
	ToPath      string            `json:"to_path,omitempty"`
	Deleted     bool              `json:"deleted"`
	Retained    *RetainedLocation `json:"retained,omitempty"`
}

type ReadResult struct {
	File File `json:"file"`
	Base Base `json:"base"`
	// Transient I/O accounting, including a failed streamed read. Not history.
	ReferenceReadBytes int64 `json:"-"`
}

// All methods must be durable before returning success. WithLock serializes
// workers/processes, but does not pretend to lock independent filesystem writers.
type Store interface {
	WithLock(context.Context, string, func(Store) error) error
	Bind(context.Context, File) (File, error)
	File(context.Context, string) (File, error)
	SaveBase(context.Context, Base) error
	Base(context.Context, string) (Base, error)
	Receive(context.Context, Operation) (Operation, error)
	Operation(context.Context, string) (Operation, error)
	SaveOperation(context.Context, Operation) error
	SaveRecovery(context.Context, Recovery) error
	FileAt(context.Context, string, string) (File, error)
	PathBusy(context.Context, string, string, string) (bool, error)
	CommitPath(context.Context, Operation, File, Base) error
}

func digest(b []byte) string { h := sha256.Sum256(b); return "sha256:" + hex.EncodeToString(h[:]) }
func identity(v any) string  { b, _ := json.Marshal(v); return strings.TrimPrefix(digest(b), "sha256:") }
func newFileID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "notes_file_" + hex.EncodeToString(b), nil
}
func pathKey(p string) string { return cases.Fold().String(norm.NFC.String(p)) }
func validatePath(p string) error {
	return validateSourcePath(p, false)
}

func IsReferencePath(p string) bool {
	switch strings.ToLower(path.Ext(p)) {
	case ".pdf", ".png", ".jpg", ".jpeg", ".gif", ".webp", ".wav", ".svg", ".csv", ".xlsx", ".dat", ".json":
		return validateSourcePath(p, true) == nil
	}
	return false
}

func validateSourcePath(p string, reference bool) error {
	if err := validateDirectoryPath(p); err != nil {
		return err
	}
	if IsEditableTextPath(p) {
		return nil
	}
	switch strings.ToLower(path.Ext(p)) {
	case ".pdf", ".png", ".jpg", ".jpeg", ".gif", ".webp", ".wav", ".svg", ".xlsx", ".dat":
		if reference {
			return nil
		}
	}
	return ErrReferenceOnly
}

func IsEditableTextPath(p string) bool {
	base := strings.ToLower(path.Base(p))
	if base == "makefile" || base == "dockerfile" {
		return true
	}
	ext := strings.TrimPrefix(strings.ToLower(path.Ext(p)), ".")
	return ext != "" && strings.Contains(" md markdown mdown mkd mkdn mdwn txt text log csv tsv rst adoc asciidoc json jsonc jsonl ndjson geojson canvas yaml yml base toml ini cfg conf config properties env js mjs cjs jsx ts mts cts tsx mdx py pyi pyw go rs java kt kts scala sc swift c h cpp cc cxx hpp hxx hh m mm cs fs fsx rb rake gemspec php r rmd lua pl pm sh bash zsh fish ps1 psm1 psd1 bat cmd sql graphql gql proto nix mk make cmake dockerfile html htm xhtml css scss sass less xml svg xsd xsl xslt plist vue svelte astro tex sty cls bib typ diff patch lock ", " "+ext+" ")
}

func validateDirectoryPath(p string) error {
	if p == "" || !utf8.ValidString(p) || !norm.NFC.IsNormalString(p) || path.IsAbs(p) || path.Clean(p) != p || strings.ContainsAny(p, "\\\x00") {
		return ErrUnsafePath
	}
	for _, part := range strings.Split(p, "/") {
		if strings.HasPrefix(part, ".") || strings.TrimSpace(part) != part || part == "credentials" || part == "private_no_index" {
			return ErrUnsafePath
		}
	}
	return nil
}
func validateContent(b []byte) error {
	if len(b) > MaxContentBytes || !utf8.Valid(b) || strings.ContainsRune(string(b), 0) {
		return fmt.Errorf("notes content must be UTF-8 text up to %d bytes", MaxContentBytes)
	}
	return nil
}
func makeBase(file File, generation string, exists bool, content []byte) Base {
	b := Base{PathVersion: file.PathVersion, FileID: file.ID, Generation: generation, Exists: exists, Hash: digest(content), Content: append([]byte(nil), content...)}
	b.ID = "notes_base_" + identity(b)
	return b
}

func requestKind(r Request) string {
	if r.Kind == "" {
		return KindEdit
	}
	return r.Kind
}
