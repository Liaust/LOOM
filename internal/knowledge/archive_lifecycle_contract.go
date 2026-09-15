package knowledge

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"path"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

type SourceLifecycle string

const (
	SourceLifecycleActive   SourceLifecycle = "active"
	SourceLifecycleArchived SourceLifecycle = "archived"
)

type SourceLifecycleFilter string

const (
	SourceLifecycleFilterActive   SourceLifecycleFilter = "active"
	SourceLifecycleFilterArchived SourceLifecycleFilter = "archived"
	SourceLifecycleFilterAll      SourceLifecycleFilter = "all"
)

func NormalizeSourceLifecycleFilter(filter SourceLifecycleFilter) (SourceLifecycleFilter, error) {
	if filter == "" {
		return SourceLifecycleFilterActive, nil
	}
	switch filter {
	case SourceLifecycleFilterActive, SourceLifecycleFilterArchived, SourceLifecycleFilterAll:
		return filter, nil
	default:
		return "", fmt.Errorf("%w: unsupported source lifecycle filter", ErrInvalid)
	}
}

// Lifecycle selection never grants access or interprets extraction status.
func SourceLifecycleIncluded(filter SourceLifecycleFilter, lifecycle SourceLifecycle) (bool, error) {
	filter, err := NormalizeSourceLifecycleFilter(filter)
	if err != nil {
		return false, err
	}
	if lifecycle != SourceLifecycleActive && lifecycle != SourceLifecycleArchived {
		return false, fmt.Errorf("%w: unsupported source lifecycle", ErrInvalid)
	}
	return filter == SourceLifecycleFilterAll || string(filter) == string(lifecycle), nil
}

// NotesCustodyTransition binds only custody metadata. The projector must also
// authenticate completed upstream evidence and membership before persisting it.
// Canonical paths do not replace original object/root/version identities.
type NotesCustodyTransition struct {
	KnowledgeObjectID         string `json:"knowledge_object_id"`
	WorkspaceLifecycleEventID string `json:"workspace_lifecycle_event_id"`
	ArchiveOperationID        string `json:"archive_operation_id"`
	ProjectEventID            string `json:"project_event_id,omitempty"`
	PreviousEventID           string `json:"previous_event_id,omitempty"`
	OriginalPath              string `json:"original_path"`
	CanonicalPath             string `json:"canonical_path"`
	WorkspaceRelativePath     string `json:"workspace_relative_path"`
	ManifestDigest            string `json:"manifest_digest"`
	TransitionDigest          string `json:"transition_digest"`
}

var notesCustodyObjectID = regexp.MustCompile(`^knowledge_object_[0-7][0-9A-HJKMNP-TV-Z]{25}$`)
var notesCustodyEventID = regexp.MustCompile(`^workspace_lifecycle_event_[0-7][0-9A-HJKMNP-TV-Z]{25}$`)
var notesCustodyOperationID = regexp.MustCompile(`^workspace_archive_operation_[0-7][0-9A-HJKMNP-TV-Z]{25}$`)
var notesCustodyProjectEventID = regexp.MustCompile(`^event_[0-7][0-9A-HJKMNP-TV-Z]{25}$`)
var notesCustodyDigest = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)

func NotesCustodyTransitionDigest(transition NotesCustodyTransition) string {
	transition.TransitionDigest = ""
	raw, _ := json.Marshal(transition)
	return fmt.Sprintf("sha256:%x", sha256.Sum256(raw))
}

func ValidateNotesCustodyTransition(transition NotesCustodyTransition) error {
	invalid := func() error { return fmt.Errorf("%w: invalid Notes custody transition", ErrInvalid) }
	if !notesCustodyObjectID.MatchString(transition.KnowledgeObjectID) ||
		!notesCustodyEventID.MatchString(transition.WorkspaceLifecycleEventID) ||
		!notesCustodyOperationID.MatchString(transition.ArchiveOperationID) ||
		(transition.ProjectEventID != "" && !notesCustodyProjectEventID.MatchString(transition.ProjectEventID)) ||
		(transition.PreviousEventID != "" && (!notesCustodyEventID.MatchString(transition.PreviousEventID) || transition.PreviousEventID == transition.WorkspaceLifecycleEventID)) ||
		!notesCustodyDigest.MatchString(transition.ManifestDigest) {
		return invalid()
	}
	for _, value := range []string{transition.OriginalPath, transition.CanonicalPath} {
		if !notesCustodyText(value, 8192) {
			return invalid()
		}
	}
	relative := transition.WorkspaceRelativePath
	if !notesCustodyText(relative, 4096) || path.IsAbs(relative) || path.Clean(relative) != relative ||
		relative == "." || relative == ".." || strings.HasPrefix(relative, "../") || strings.Contains(relative, "\\") {
		return invalid()
	}
	if !notesCustodyDigest.MatchString(transition.TransitionDigest) || transition.TransitionDigest != NotesCustodyTransitionDigest(transition) {
		return invalid()
	}
	return nil
}

func notesCustodyText(value string, limit int) bool {
	return value != "" && len(value) <= limit && utf8.ValidString(value) && strings.TrimSpace(value) == value &&
		strings.IndexFunc(value, unicode.IsControl) == -1
}
