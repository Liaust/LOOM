// Package notesworkspacesync joins native transport evidence to the source journal.
// It does not implement replication, source filesystems or a second content queue.
package notesworkspacesync

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"path"
	"regexp"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
	"loom.local/loom/internal/notesworkspace"
)

const Prefix = "LOOM-Control-v1/"
const StructurePrefix = Prefix + "payload/structure/"
const LayoutPrefix = Prefix + "payload/layout/"
const SourceMovePrefix = Prefix + "payload/source-move/"
const MaxBytes = notesworkspace.MaxContentBytes
const MaxControlBytes = 1 << 20
const MaxReferenceBytes = notesworkspace.MaxReferenceBytes

var ErrHeld = errors.New("notes sync evidence held")
var tokenRE = regexp.MustCompile(`^[A-Za-z0-9_-]{1,160}$`)
var revRE = regexp.MustCompile(`^[1-9][0-9]*-[A-Za-z0-9]+$`)
var hashRE = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)

func token(s string) bool {
	return tokenRE.MatchString(s) && s != "__proto__" && s != "constructor" && s != "prototype"
}
func digest(b []byte) string { h := sha256.Sum256(b); return "sha256:" + hex.EncodeToString(h[:]) }
func key(v ...string) string {
	b, _ := json.Marshal(v)
	return strings.TrimPrefix(digest(b), "sha256:")
}
func textPath(p string) bool {
	return workspacePath(p, false)
}

func referencePath(p string) bool {
	return workspacePath(p, true) && !textPath(p)
}

func workspacePath(p string, reference bool) bool {
	if !directoryPath(p) {
		return false
	}
	switch strings.ToLower(path.Ext(p)) {
	case ".md", ".markdown", ".txt", ".canvas", ".base":
		return true
	case ".pdf", ".png", ".jpg", ".jpeg", ".gif", ".webp", ".wav", ".svg", ".csv", ".xlsx", ".dat", ".json":
		return reference
	}
	return false
}

func directoryPath(p string) bool {
	if p == "" || len(p) > 1024 || !utf8.ValidString(p) || !norm.NFC.IsNormalString(p) || path.Clean(p) != p || strings.ContainsAny(p, "\\:") || strings.HasPrefix(p, Prefix) {
		return false
	}
	for _, r := range p {
		if r < 32 || r == 127 {
			return false
		}
	}
	for _, s := range strings.Split(p, "/") {
		if s == "" || strings.HasPrefix(s, ".") || strings.TrimSpace(s) != s || s == "credentials" || s == "private_no_index" {
			return false
		}
	}
	return true
}

type Header struct {
	Version int    `json:"version"`
	Kind    string `json:"kind"`
	ID      string `json:"id"`
}
type Binding struct {
	Header
	Workspace      string `json:"workspace"`
	Collection     string `json:"collection"`
	Generation     string `json:"generation"`
	FileID         string `json:"fileId"`
	CollectionRoot string `json:"collectionRoot"`
	Path           string `json:"path"`
	SourceBase     string `json:"sourceBase"`
	NativeRevision string `json:"nativeRevision"`
	SHA256         string `json:"sha256"`
	Writable       bool   `json:"writable"`
	SourceSequence uint64 `json:"sourceSequence,omitempty"`
	EntryKind      string `json:"entryKind,omitempty"`
	Deleted        bool   `json:"deleted,omitempty"`
	PreviousPath   string `json:"previousPath,omitempty"`
}
type Intent struct {
	Header
	Device       string   `json:"device"`
	Operation    string   `json:"operation"`
	Workspace    string   `json:"workspace"`
	Collection   string   `json:"collection"`
	Generation   string   `json:"generation"`
	FileID       string   `json:"fileId"`
	Base         *Binding `json:"base"`
	Predecessor  string   `json:"predecessor,omitempty"`
	Resolves     []string `json:"resolves,omitempty"`
	Path         string   `json:"path"`
	Target       string   `json:"target,omitempty"`
	SHA256       string   `json:"sha256"`
	Length       int      `json:"length"`
	EntryKind    string   `json:"entryKind,omitempty"`
	Dependencies []string `json:"dependencies,omitempty"`
}
type Revision struct {
	Path     string `json:"path"`
	Revision string `json:"revision"`
	Role     string `json:"role"`
}
type Publication struct {
	Header
	IntentID     string     `json:"intentId"`
	IntentDigest string     `json:"intentDigest"`
	Revisions    []Revision `json:"revisions"`
	EntryKind    string     `json:"entryKind,omitempty"`
}
type Ack struct {
	Header
	IntentID      string   `json:"intentId"`
	IntentDigest  string   `json:"intentDigest"`
	PublicationID string   `json:"publicationId"`
	Status        string   `json:"status"`
	Reason        string   `json:"reason"`
	Binding       *Binding `json:"binding,omitempty"`
	Resolves      []string `json:"resolves,omitempty"`
}

func controlPath(h Header) string {
	prefix := Prefix
	if h.Kind == "layout" && strings.HasPrefix(h.ID, "layout_source_move_") {
		prefix = SourceMovePrefix
	} else if h.Kind == "layout" || h.Kind == "binding" && strings.HasPrefix(h.ID, "layout_") {
		prefix = LayoutPrefix
	} else if h.Kind == "scope" || strings.HasPrefix(h.ID, "dir_") || strings.HasPrefix(h.ID, "canvas_") {
		prefix = StructurePrefix
	}
	return prefix + h.Kind + "/" + h.ID + ".md"
}

// Reject duplicate keys too: JS and Go must not interpret different evidence.
func strictJSON(raw []byte, v any) error {
	if len(raw) > MaxControlBytes || !utf8.Valid(raw) {
		return ErrHeld
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	if err := uniqueJSON(d); err != nil {
		return err
	}
	if _, err := d.Token(); err != io.EOF {
		return ErrHeld
	}
	d = json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return ErrHeld
	}
	return nil
}
func uniqueJSON(d *json.Decoder) error {
	t, e := d.Token()
	if e != nil {
		return ErrHeld
	}
	delim, ok := t.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := map[string]bool{}
		for d.More() {
			k, e := d.Token()
			s, ok := k.(string)
			if e != nil || !ok || seen[s] {
				return ErrHeld
			}
			seen[s] = true
			if e = uniqueJSON(d); e != nil {
				return e
			}
		}
	case '[':
		for d.More() {
			if e = uniqueJSON(d); e != nil {
				return e
			}
		}
	default:
		return ErrHeld
	}
	_, e = d.Token()
	return e
}
func (b Binding) valid() bool {
	if b.SourceSequence > 9007199254740991 {
		return false
	}
	validPath := workspacePath(b.Path, !b.Writable)
	if b.EntryKind == "directory" {
		validPath = directoryPath(b.Path)
	}
	if b.EntryKind != "" && b.EntryKind != "directory" || b.Deleted && b.EntryKind != "directory" || b.PreviousPath != "" && (b.EntryKind != "directory" || !directoryPath(b.PreviousPath)) {
		return false
	}
	return b.Version == 1 && b.Kind == "binding" && token(b.ID) && token(b.Workspace) && token(b.Collection) && token(b.Generation) && token(b.FileID) && token(b.SourceBase) && textPath(b.CollectionRoot+"/x.md") && validPath && strings.HasPrefix(b.Path, b.CollectionRoot+"/") && revRE.MatchString(b.NativeRevision) && hashRE.MatchString(b.SHA256)
}
func parseIntent(raw []byte) (i Intent, err error) {
	var present map[string]json.RawMessage
	if json.Unmarshal(raw, &present) != nil || len(present["base"]) == 0 || len(present["length"]) == 0 || string(present["length"]) == "null" {
		return i, ErrHeld
	}
	if err = strictJSON(raw, &i); err != nil {
		return
	}
	validPath := textPath(i.Path)
	if i.EntryKind == "directory" {
		validPath = directoryPath(i.Path)
	}
	if i.EntryKind != "" && i.EntryKind != "directory" || i.EntryKind == "directory" && (i.Length != 0 || i.SHA256 != digest(nil) || i.Operation == "edit") || i.EntryKind == "" && len(i.Dependencies) > 0 || len(i.Dependencies) > 10000 {
		return i, ErrHeld
	}
	for _, id := range i.Dependencies {
		if !token(id) || id == i.ID {
			return i, ErrHeld
		}
	}
	if i.Version != 1 || i.Kind != "intent" || !token(i.ID) || !token(i.Device) || !token(i.Workspace) || !token(i.Collection) || !token(i.Generation) || !token(i.FileID) || !validPath || !hashRE.MatchString(i.SHA256) || i.Length < 0 || i.Length > MaxBytes || i.Predecessor != "" && !token(i.Predecessor) {
		err = ErrHeld
		return
	}
	if _, present := present["resolves"]; present {
		if i.Operation != "edit" || i.Base == nil || i.Predecessor != "" || !validResolves(i.Resolves, i.ID) {
			return i, ErrHeld
		}
	}
	if i.Operation == "create" {
		if i.Base != nil || i.Predecessor != "" {
			err = ErrHeld
			return
		}
	} else if i.Base == nil {
		if i.Predecessor == "" {
			err = ErrHeld
			return
		}
	} else {
		b := i.Base
		if !b.valid() || !b.Writable || b.Workspace != i.Workspace || b.Collection != i.Collection || b.Generation != i.Generation || b.FileID != i.FileID || b.EntryKind != i.EntryKind || b.Deleted {
			err = ErrHeld
			return
		}
	}
	switch i.Operation {
	case "edit", "create":
		if i.Target != "" {
			err = ErrHeld
		}
	case "delete":
		if i.Target != "" || i.Length != 0 || i.SHA256 != digest(nil) {
			err = ErrHeld
		}
	case "rename":
		if !(textPath(i.Target) || i.EntryKind == "directory" && directoryPath(i.Target)) || i.Path == i.Target {
			err = ErrHeld
		}
	default:
		err = ErrHeld
	}
	return
}

func validResolves(ids []string, self string) bool {
	if len(ids) == 0 || len(ids) > 32 {
		return false
	}
	seen := make(map[string]bool, len(ids))
	for _, id := range ids {
		if !token(id) || id == self || seen[id] {
			return false
		}
		seen[id] = true
	}
	return true
}
func parsePublication(raw []byte) (p Publication, err error) {
	if err = strictJSON(raw, &p); err != nil {
		return
	}
	if p.EntryKind != "" && p.EntryKind != "directory" {
		return p, ErrHeld
	}
	if p.Version != 1 || p.Kind != "publication" || !token(p.ID) || !token(p.IntentID) || !hashRE.MatchString(p.IntentDigest) || len(p.Revisions) < 1 || len(p.Revisions) > 2 {
		err = ErrHeld
		return
	}
	for _, r := range p.Revisions {
		if !(textPath(r.Path) || p.EntryKind == "directory" && directoryPath(r.Path)) || !revRE.MatchString(r.Revision) || (r.Role != "content" && r.Role != "deletion") {
			err = ErrHeld
			return
		}
	}
	return
}
func paired(i Intent, p Publication) bool {
	if i.ID != p.IntentID || i.EntryKind != p.EntryKind {
		return false
	}
	if i.EntryKind == "directory" {
		p := p.Revisions
		target, role := i.Path, "content"
		if i.Operation == "rename" {
			target = i.Target
		}
		if i.Operation == "delete" {
			role = "deletion"
		}
		return len(p) == 1 && p[0].Path == target && p[0].Role == role
	}
	if i.Operation == "rename" {
		return len(p.Revisions) == 2 && findRevision(p, i.Path, "deletion") != "" && findRevision(p, i.Target, "content") != ""
	}
	role := "content"
	if i.Operation == "delete" {
		role = "deletion"
	}
	return len(p.Revisions) == 1 && findRevision(p, i.Path, role) != ""
}
func findRevision(p Publication, path, role string) string {
	for _, r := range p.Revisions {
		if r.Path == path && r.Role == role {
			return r.Revision
		}
	}
	return ""
}
