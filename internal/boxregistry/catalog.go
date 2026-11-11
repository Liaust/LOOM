// Package boxregistry resolves workspace identity without projecting its files.
package boxregistry

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

const SchemaVersion = "loom.box_catalog.v1"

var (
	ErrUnknown     = errors.New("box: unknown workspace")
	ErrUnavailable = errors.New("box: workspace unavailable")
	ErrSelection   = errors.New("box: invalid selection")
	keyPattern     = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,62}$`)
	idPattern      = regexp.MustCompile(`^box_[A-Za-z0-9][A-Za-z0-9_-]{1,100}$`)
)

type Entry struct {
	Key      string `json:"key"`
	Label    string `json:"label"`
	Node     string `json:"node"`
	RootPath string `json:"root_path"`
}

type Document struct {
	SchemaVersion string  `json:"schema_version"`
	DefaultBox    string  `json:"default_box"`
	Boxes         []Entry `json:"boxes"`
}

type Box struct {
	ID          string `json:"box_id"`
	Key         string `json:"box_key"`
	Label       string `json:"label"`
	Node        string `json:"node"`
	RootPath    string `json:"root_path"`
	RootRef     string `json:"root_ref"`
	StateRoot   string `json:"state_root"`
	ArchiveRoot string `json:"archive_root,omitempty"`
	Available   bool   `json:"available"`
	Reason      string `json:"reason,omitempty"`
	Primary     bool   `json:"primary"`
}

type Catalog struct {
	SchemaVersion string `json:"schema_version"`
	DefaultBox    string `json:"default_box"`
	Boxes         []Box  `json:"boxes"`
}

type Options struct {
	File             string
	PrimaryPath      string
	PrimaryStateRoot string
	Node             string
	DataRoot         string
	StorageRoot      string
}

type descriptor struct {
	SchemaVersion string `yaml:"schema_version"`
	ID            string `yaml:"box_id"`
	OwnerNode     string `yaml:"owner_node"`
	RootPath      string `yaml:"root_path"`
}

func Load(opts Options) (Catalog, error) {
	doc := Document{SchemaVersion: SchemaVersion, DefaultBox: "personal", Boxes: []Entry{{Key: "personal", Label: "Personal", Node: opts.Node, RootPath: opts.PrimaryPath}}}
	if opts.File != "" {
		catalogPath := opts.File
		if resolved, err := filepath.EvalSymlinks(catalogPath); err == nil && resolved != catalogPath {
			if !strings.HasPrefix(resolved, "/nix/store/") {
				return Catalog{}, fmt.Errorf("Box catalog symlink must select immutable Nix configuration")
			}
			catalogPath = resolved
		}
		raw, err := readRegular(catalogPath, 1<<20)
		if err != nil {
			return Catalog{}, fmt.Errorf("read Box catalog: %w", err)
		}
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&doc); err != nil {
			return Catalog{}, fmt.Errorf("decode Box catalog: %w", err)
		}
		if decoder.Decode(new(any)) != io.EOF {
			return Catalog{}, fmt.Errorf("Box catalog must contain exactly one document")
		}
	}
	return Resolve(doc, opts)
}

func Resolve(doc Document, opts Options) (Catalog, error) {
	if doc.SchemaVersion != SchemaVersion || len(doc.Boxes) == 0 || len(doc.Boxes) > 128 || !keyPattern.MatchString(doc.DefaultBox) {
		return Catalog{}, fmt.Errorf("invalid Box catalog schema/default/size")
	}
	if !absolute(opts.PrimaryPath) || opts.Node == "" || !absolute(opts.DataRoot) || !absolute(opts.PrimaryStateRoot) || !absolute(opts.StorageRoot) {
		return Catalog{}, fmt.Errorf("Box catalog requires configured absolute ownership roots")
	}
	out := Catalog{SchemaVersion: SchemaVersion, DefaultBox: doc.DefaultBox, Boxes: make([]Box, 0, len(doc.Boxes))}
	seenKeys, seenIDs := map[string]bool{}, map[string]bool{}
	primaryCount := 0
	for _, entry := range doc.Boxes {
		if !keyPattern.MatchString(entry.Key) || strings.TrimSpace(entry.Label) == "" || len(entry.Label) > 128 || strings.ContainsAny(entry.Label, "\x00\r\n") || !absolute(entry.RootPath) || entry.Node != opts.Node || seenKeys[entry.Key] {
			return Catalog{}, fmt.Errorf("invalid or duplicate Box binding %q", entry.Key)
		}
		seenKeys[entry.Key] = true
		for _, reserved := range []string{opts.DataRoot, opts.StorageRoot, filepath.Join(filepath.Dir(opts.PrimaryPath), "agents")} {
			if overlap(entry.RootPath, reserved) {
				return Catalog{}, fmt.Errorf("Box %q overlaps retained/internal state", entry.Key)
			}
		}
		for _, previous := range out.Boxes {
			if overlap(entry.RootPath, previous.RootPath) {
				return Catalog{}, fmt.Errorf("Box roots must not overlap")
			}
		}
		box := Box{Key: entry.Key, Label: entry.Label, Node: entry.Node, RootPath: entry.RootPath, Primary: entry.RootPath == opts.PrimaryPath}
		if box.Primary {
			primaryCount++
			if entry.Key != doc.DefaultBox {
				return Catalog{}, fmt.Errorf("legacy primary Box must remain the default")
			}
			box.RootRef, box.StateRoot, box.ArchiveRoot = "box", opts.PrimaryStateRoot, opts.StorageRoot
		}
		info, err := os.Lstat(entry.RootPath)
		if err != nil {
			if !os.IsNotExist(err) {
				return Catalog{}, err
			}
			box.Reason = "root_missing"
		} else {
			resolved, resolveErr := filepath.EvalSymlinks(entry.RootPath)
			if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || resolveErr != nil || resolved != entry.RootPath {
				return Catalog{}, fmt.Errorf("Box %q must be a real canonical directory", entry.Key)
			}
			raw, readErr := readRegular(filepath.Join(entry.RootPath, ".loom", "box.yaml"), 1<<20)
			if readErr != nil {
				if !os.IsNotExist(readErr) {
					return Catalog{}, fmt.Errorf("Box %q descriptor: %w", entry.Key, readErr)
				}
				box.Reason = "descriptor_missing"
				// Legacy no-catalog file APIs historically needed no descriptor.
				box.Available = box.Primary && opts.File == ""
			} else {
				var value descriptor
				if err := yaml.Unmarshal(raw, &value); err != nil || (value.SchemaVersion != "loom.box.v0.4.1" && value.SchemaVersion != "loom.box.v0.5") || !idPattern.MatchString(value.ID) || value.RootPath != entry.RootPath || value.OwnerNode != entry.Node {
					return Catalog{}, fmt.Errorf("Box %q descriptor identity disagrees with its configured root", entry.Key)
				}
				if seenIDs[value.ID] {
					return Catalog{}, fmt.Errorf("duplicate Box identity")
				}
				seenIDs[value.ID] = true
				box.ID, box.Available = value.ID, true
				if !box.Primary {
					box.RootRef = "box:" + box.ID
					box.StateRoot = filepath.Join(opts.DataRoot, "boxes", box.ID)
					box.ArchiveRoot = filepath.Join(opts.StorageRoot, "Boxes", box.Key)
				}
			}
		}
		out.Boxes = append(out.Boxes, box)
	}
	if primaryCount != 1 || !seenKeys[doc.DefaultBox] {
		return Catalog{}, fmt.Errorf("catalog must preserve exactly one primary Box")
	}
	return out, nil
}

func (c Catalog) Lookup(ref string) (Box, error) {
	if ref == "" {
		ref = c.DefaultBox
	}
	for _, box := range c.Boxes {
		if ref == box.Key || ref == box.ID && box.ID != "" || ref == box.RootRef && box.RootRef != "" {
			return box, nil
		}
	}
	return Box{}, fmt.Errorf("%w: %s", ErrUnknown, ref)
}

func (c Catalog) Get(ref string) (Box, error) {
	box, err := c.Lookup(ref)
	if err != nil {
		return Box{}, err
	}
	if !box.Available {
		return Box{}, fmt.Errorf("%w: %s (%s)", ErrUnavailable, box.Key, box.Reason)
	}
	return box, nil
}

func (c Catalog) ForPath(path string) (Box, error) {
	if !absolute(path) {
		return Box{}, ErrSelection
	}
	for _, box := range c.Boxes {
		if within(path, box.RootPath) {
			return c.Get(box.Key)
		}
	}
	return Box{}, ErrUnknown
}

func (c Catalog) RefForID(id string) (string, error) {
	box, err := c.Get(id)
	return box.RootRef, err
}

// Digest binds deferred work to actual membership, not the current UI selection.
func Digest(boxID, sourceRoot, revision string) string {
	raw, _ := json.Marshal([]string{boxID, sourceRoot, revision})
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func absolute(path string) bool {
	return filepath.IsAbs(path) && path != "/" && filepath.Clean(path) == path && !strings.ContainsAny(path, "\x00\r\n")
}
func within(path, root string) bool {
	return path == root || strings.HasPrefix(path, root+string(filepath.Separator))
}
func overlap(left, right string) bool { return within(left, right) || within(right, left) }

func readRegular(path string, limit int64) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > limit {
		return nil, fmt.Errorf("configuration must be a bounded regular file")
	}
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil || canonical != path {
		return nil, fmt.Errorf("configuration path must not contain symlinks")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return nil, fmt.Errorf("configuration changed while opening")
	}
	raw, err := io.ReadAll(io.LimitReader(f, limit+1))
	if int64(len(raw)) > limit {
		return nil, fmt.Errorf("configuration exceeds limit")
	}
	return raw, err
}

type Selection struct {
	BoxIDs   []string `json:"box_ids,omitempty"`
	AllBoxes bool     `json:"all_boxes,omitempty"`
}

type ResolvedSelection struct {
	BoxIDs   []string `json:"box_ids"`
	AllBoxes bool     `json:"all_boxes"`
	Origin   string   `json:"selection_origin"`
	Digest   string   `json:"membership_digest"`
}

func (c Catalog) Select(input Selection, defaultRef string) (ResolvedSelection, error) {
	if input.AllBoxes && len(input.BoxIDs) > 0 || len(input.BoxIDs) > 128 {
		return ResolvedSelection{}, ErrSelection
	}
	out := ResolvedSelection{BoxIDs: []string{}, Origin: "explicit", AllBoxes: input.AllBoxes}
	refs := input.BoxIDs
	if input.AllBoxes {
		for _, box := range c.Boxes {
			if box.Available && box.ID != "" {
				out.BoxIDs = append(out.BoxIDs, box.ID)
			}
		}
	} else {
		if len(refs) == 0 {
			refs = []string{defaultRef}
			out.Origin = "default"
		}
		seen := map[string]bool{}
		for _, ref := range refs {
			box, err := c.Get(ref)
			if err != nil {
				return ResolvedSelection{}, err
			}
			if box.ID == "" {
				return ResolvedSelection{}, fmt.Errorf("%w: descriptor required", ErrUnavailable)
			}
			if !seen[box.ID] {
				out.BoxIDs = append(out.BoxIDs, box.ID)
				seen[box.ID] = true
			}
		}
	}
	sort.Strings(out.BoxIDs)
	raw, _ := json.Marshal(out.BoxIDs)
	out.Digest = Digest(string(raw), "", "")
	return out, nil
}

func (s ResolvedSelection) Contains(id string) bool {
	for _, selected := range s.BoxIDs {
		if selected == id {
			return true
		}
	}
	return false
}

func IsBoxRef(ref string) bool {
	return ref == "box" || strings.HasPrefix(ref, "box:") && idPattern.MatchString(strings.TrimPrefix(ref, "box:"))
}
