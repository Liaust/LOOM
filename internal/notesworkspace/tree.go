package notesworkspace

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
	"loom.local/loom/internal/boxregistry"
)

const maxTreeEntries = 50000

type TreeBinding struct {
	File         File   `json:"file"`
	SourcePath   string `json:"source_path"`
	Destination  string `json:"destination"`
	Retire       bool   `json:"retire,omitempty"`
	RetireReason string `json:"retire_reason,omitempty"`
}
type TreeJournal struct {
	OperationID string          `json:"operation_id"`
	Location    JournalLocation `json:"location"`
}
type TreeMove struct {
	BoxID          string        `json:"box_id,omitempty"`
	ID             string        `json:"operation_id"`
	ScopeID        string        `json:"scope_id"`
	Generation     string        `json:"generation"`
	SourcePath     string        `json:"source_root"`
	From           string        `json:"from"`
	To             string        `json:"to"`
	State          string        `json:"state"`
	Hash           string        `json:"plan_hash"`
	Reason         string        `json:"reason,omitempty"`
	Inode          inodeIdentity `json:"source_identity"`
	Manifest       string        `json:"manifest"`
	Entries        int           `json:"entries"`
	Bytes          int64         `json:"bytes"`
	Bindings       []TreeBinding `json:"bindings"`
	Journals       []TreeJournal `json:"journals"`
	File           File          `json:"file"`
	SourceKey      string        `json:"source_key"`
	DestinationKey string        `json:"destination_key"`
}

func (m TreeMove) Summary() map[string]any {
	return map[string]any{"operation_id": m.ID, "from": m.From, "to": m.To, "state": m.State, "plan_hash": m.Hash,
		"entries": m.Entries, "bytes": m.Bytes, "bound_files": len(m.Bindings), "retained_journals": len(m.Journals), "reason": m.Reason,
		"qualification": "Main-side same-filesystem move; files are not downloaded, indexed or enrolled. Other devices' unsent edits remain separate."}
}
func within(relative, prefix string) bool {
	return relative == prefix || strings.HasPrefix(relative, prefix+"/")
}

func inspectTree(ctx context.Context, root, relative string) (inodeIdentity, string, int, int64, error) {
	parent, name, err := openSourceParent(root, relative)
	if err != nil {
		return inodeIdentity{}, "", 0, 0, err
	}
	defer parent.Close()
	inode, err := sourceDirectoryIdentity(parent, name)
	if err != nil || inode == nil {
		return inodeIdentity{}, "", 0, 0, fmt.Errorf("move source must be an existing directory: %w", err)
	}
	var records []string
	var bytes int64
	err = filepath.WalkDir(filepath.Join(root, filepath.FromSlash(relative)), func(absolute string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if len(records) >= maxTreeEntries {
			return fmt.Errorf("directory move preview exceeds %d entries; use a smaller move", maxTreeEntries)
		}
		info, e := entry.Info()
		if e != nil {
			return e
		}
		var physical unix.Stat_t
		if e = unix.Lstat(absolute, &physical); e != nil {
			return e
		}
		if uint64(physical.Dev) != inode.Device || entry.Type()&os.ModeSymlink != 0 || !info.IsDir() && !info.Mode().IsRegular() {
			return fmt.Errorf("move contains an unsupported mount, link or special file at %s", absolute)
		}
		local, e := filepath.Rel(filepath.Join(root, filepath.FromSlash(relative)), absolute)
		if e != nil {
			return e
		}
		if local != "." && entry.IsDir() && strings.HasPrefix(entry.Name(), ".loom-notes-") {
			records = append(records, fmt.Sprintf("journal:%s:%d:%d", local, physical.Dev, physical.Ino))
			return filepath.SkipDir
		}
		if entry.Name() == ".loom" && entry.IsDir() {
			if _, e := os.Lstat(filepath.Join(absolute, "project.yaml")); e == nil {
				return fmt.Errorf("moving a registered project root requires its relocation owner")
			}
		}
		records = append(records, fmt.Sprintf("%s:%d:%d:%d:%d:%d", local, physical.Dev, physical.Ino, info.Mode(), info.Size(), info.ModTime().UnixNano()))
		if info.Mode().IsRegular() {
			bytes += info.Size()
		}
		return nil
	})
	if err != nil {
		return *inode, "", len(records), bytes, err
	}
	if err = parent.check(); err != nil {
		return *inode, "", len(records), bytes, err
	}
	return *inode, identity(records), len(records), bytes, nil
}

func treeDigest(m TreeMove) string { m.Hash = ""; m.State = ""; m.Reason = ""; return identity(m) }

// PreviewTree holds the same physical writer barrier used by ordinary Files and
// legacy Notes. Its plan is durable metadata, never a content-copy job.
func (s Service) PreviewTree(ctx context.Context, collection, id, from, to string) (out TreeMove, err error) {
	if id == "" || len(id) > 160 || validateDirectoryPath(from) != nil || validateDirectoryPath(to) != nil || within(to, from) || within(from, to) {
		return out, ErrUnsafePath
	}
	resolver, ok := s.Sources.(PathResolver)
	if !ok {
		return out, ErrMembership
	}
	err = s.Store.WithLock(ctx, "collection:"+collection, func(raw Store) error {
		store, ok := raw.(SQLStore)
		if !ok || !store.ExclusiveSource {
			return fmt.Errorf("tree moves require the shared exclusive filesystem owner")
		}
		return resolver.WithPaths(ctx, collection, []string{from, to}, func(src Source) error {
			if old, e := store.TreeMove(ctx, id); e == nil {
				if old.From != from || old.To != to || old.ScopeID != collection {
					return ErrIntentMismatch
				}
				if old.State != Accepted && old.State != Conflict {
					if e = store.guardTreeSourceRoots(ctx, src, from, to); e != nil {
						return e
					}
				}
				out = old
				return nil
			} else if !errors.Is(e, fs.ErrNotExist) {
				return e
			}
			if e := store.guardTreeSourceRoots(ctx, src, from, to); e != nil {
				return e
			}
			for _, relative := range []string{from, to} {
				if busy, e := store.PathBusy(ctx, collection, pathKey(relative), ""); e != nil {
					return e
				} else if busy {
					return ErrPathBusy
				}
			}
			p, n, e := openSourceParent(src.Path, to)
			if e != nil {
				return e
			}
			defer p.Close()
			if e = unix.Fstatat(int(p.current().Fd()), n, &unix.Stat_t{}, unix.AT_SYMLINK_NOFOLLOW); e == nil {
				return fmt.Errorf("move destination exists")
			} else if !errors.Is(e, unix.ENOENT) {
				return e
			}
			out = TreeMove{ID: id, ScopeID: collection, Generation: src.Generation, SourcePath: src.Path, From: from, To: to, State: "preview"}
			if selection, scoped := boxregistry.FromContext(ctx); scoped && len(selection.BoxIDs) == 1 {
				out.BoxID = selection.BoxIDs[0]
			}
			out.SourceKey = pathKey(filepath.Join(src.Path, filepath.FromSlash(from)))
			out.DestinationKey = pathKey(filepath.Join(src.Path, filepath.FromSlash(to)))
			out.Inode, out.Manifest, out.Entries, out.Bytes, e = inspectTree(ctx, src.Path, from)
			if e != nil {
				return e
			}
			fileID, e := newFileID()
			if e != nil {
				return e
			}
			out.File, e = store.Bind(ctx, File{ID: fileID, CollectionID: collection, RelativePath: from, PathKey: pathKey(from), EntryKind: directoryEntry})
			if e != nil {
				return e
			}
			if out.File.EntryKind != directoryEntry {
				return ErrUnsafePath
			}
			out.Bindings, out.Journals, e = store.treeBindings(ctx, src, from, to)
			if e != nil {
				return e
			}
			out.Hash = treeDigest(out)
			return store.saveTreeMove(ctx, out, true)
		})
	})
	return
}

func (s Service) ApplyTree(ctx context.Context, id, hash string) (out TreeMove, err error) {
	store, ok := s.Store.(SQLStore)
	if !ok || !store.ExclusiveSource {
		return out, ErrMembership
	}
	initial, err := store.TreeMove(ctx, id)
	if err != nil {
		return out, err
	}
	if hash == "" || initial.Hash != hash {
		return out, ErrIntentMismatch
	}
	err = store.WithLock(ctx, "collection:"+initial.ScopeID, func(raw Store) error {
		locked := raw.(SQLStore)
		current, e := locked.TreeMove(ctx, id)
		if e != nil {
			return e
		}
		out = current
		if current.State == Accepted || current.State == Conflict {
			return nil
		}
		resolver, ok := s.Sources.(PathResolver)
		if !ok {
			return ErrMembership
		}
		return resolver.WithPaths(ctx, current.ScopeID, []string{current.From, current.To}, func(src Source) error {
			if src.Generation != current.Generation || src.Path != current.SourcePath {
				return ErrMembership
			}
			if e := locked.guardTreeSourceRoots(ctx, src, current.From, current.To); e != nil {
				return e
			}
			if current.State == "preview" {
				inode, manifest, _, _, e := inspectTree(ctx, src.Path, current.From)
				if e != nil {
					return e
				}
				bindings, journals, e := locked.treeBindings(ctx, src, current.From, current.To)
				if e != nil {
					return e
				}
				if inode != current.Inode || manifest != current.Manifest || identity(bindings) != identity(current.Bindings) || identity(journals) != identity(current.Journals) {
					current.State, current.Reason = Conflict, "source changed after preview; review a new plan"
					out = current
					return locked.saveTreeMove(ctx, current, false)
				}
				current.State = "prepared"
				if e = locked.saveTreeMove(ctx, current, false); e != nil {
					return e
				}
			}
			from, name, e := openSourceParent(src.Path, current.From)
			if e != nil {
				return e
			}
			defer from.Close()
			to, destination, e := openSourceParent(src.Path, current.To)
			if e != nil {
				return e
			}
			defer to.Close()
			original, e := sourceDirectoryIdentity(from, name)
			if e != nil {
				return e
			}
			landed, e := sourceDirectoryIdentity(to, destination)
			if e != nil {
				return e
			}
			if original != nil && *original == current.Inode && landed == nil {
				if e = from.check(); e != nil {
					return e
				}
				if e = to.check(); e != nil {
					return e
				}
				if e = renameNoReplace(from.fd(), name, to.fd(), destination); e != nil {
					return e
				}
				if e = errors.Join(from.current().Sync(), to.current().Sync()); e != nil {
					return e
				}
			} else if original != nil || landed == nil || *landed != current.Inode {
				return fmt.Errorf("prepared move has an ambiguous physical outcome; paths remain reserved")
			}
			if e = locked.commitTree(ctx, current); e != nil {
				return e
			}
			out.State = Accepted
			out.Reason = "moved original directory inode on Main; descendant identities and journals retained"
			return nil
		})
	})
	return
}
