package notesworkspace

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"

	"golang.org/x/sys/unix"
)

const directoryEntry = "directory"

func makeDirectoryBase(file File, generation string, inode *inodeIdentity) Base {
	b := makeBase(file, generation, inode != nil, nil)
	b.DirectoryIdentity = inode
	b.ID = ""
	b.ID = "notes_base_" + identity(b)
	return b
}

// ReadDirectory records presence and exact inode identity, never file bytes.
// Reading an absent nested path does not create any of its parents.
func (s Service) ReadDirectory(ctx context.Context, collection, relative string) (out ReadResult, err error) {
	resolver, ok := s.Sources.(DirectoryResolver)
	if s.Store == nil || !ok {
		return out, fmt.Errorf("notes workspace directory resolver is not configured")
	}
	if err := validateDirectoryPath(relative); err != nil {
		return out, err
	}
	readPaths := resolver.WithDirectoryPaths
	if reader, ok := s.Sources.(DirectoryReadResolver); ok {
		readPaths = reader.WithReadDirectoryPaths
	}
	err = s.Store.WithLock(ctx, "collection:"+collection, func(store Store) error {
		if busy, e := store.PathBusy(ctx, collection, pathKey(relative), ""); e != nil {
			return e
		} else if busy {
			return ErrPathBusy
		}
		return readPaths(ctx, collection, []string{relative}, func(src Source) error {
			if src.CollectionID != collection || src.Generation == "" {
				return ErrMembership
			}
			parent, name, err := openSourceParent(src.Path, relative)
			var inode *inodeIdentity
			if !errors.Is(err, errMissingSourceDirectory) {
				if err != nil {
					return err
				}
				defer parent.Close()
				inode, err = sourceDirectoryIdentity(parent, name)
				if err != nil {
					return err
				}
			}
			id, err := newFileID()
			if err != nil {
				return err
			}
			file, err := store.Bind(ctx, File{ID: id, CollectionID: collection, RelativePath: relative, PathKey: pathKey(relative), EntryKind: directoryEntry})
			if err != nil {
				return err
			}
			if file.RelativePath != relative || file.EntryKind != directoryEntry {
				return fmt.Errorf("directory path collides with another binding")
			}
			base := makeDirectoryBase(file, src.Generation, inode)
			if err := store.SaveBase(ctx, base); err != nil {
				return err
			}
			out = ReadResult{File: file, Base: base}
			return nil
		})
	})
	return
}

func validateDirectoryRequest(r Request) error {
	if r.OperationID == "" || len(r.OperationID) > 1024 || len(r.Content) != 0 {
		return fmt.Errorf("directory operation requires identity (at most 1024 bytes) and no content")
	}
	switch requestKind(r) {
	case KindEdit, KindDelete:
		if r.DestinationPath != "" || len(r.DirectoryTargets) != 0 {
			return fmt.Errorf("directory ensure/delete cannot include a destination or descendants")
		}
	case KindRename:
		if err := validateDirectoryPath(r.DestinationPath); err != nil {
			return err
		}
		seen := map[string]bool{}
		for _, relative := range r.DirectoryTargets {
			if err := validateDirectoryPath(relative); err != nil {
				return err
			}
			if !strings.HasPrefix(relative, r.DestinationPath+"/") || seen[pathKey(relative)] {
				return fmt.Errorf("directory targets must be distinct collection-relative destination descendants")
			}
			seen[pathKey(relative)] = true
		}
	default:
		return fmt.Errorf("unsupported directory operation kind")
	}
	return nil
}

// ReceiveDirectory retains the exact reviewed base and ordered child-operation
// allowlist. It does not authorize deletion of any unacknowledged descendants.
func (s Service) ReceiveDirectory(ctx context.Context, request Request) (Operation, error) {
	if s.Store == nil {
		return Operation{}, fmt.Errorf("notes workspace is not configured")
	}
	if err := validateDirectoryRequest(request); err != nil {
		return Operation{}, err
	}
	if old, err := s.Store.Operation(ctx, request.OperationID); err == nil {
		if old.File.EntryKind != directoryEntry || !sameIntent(old, Operation{Request: request}) {
			return Operation{}, ErrIntentMismatch
		}
		return old, nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return Operation{}, err
	}
	file, err := s.Store.File(ctx, request.FileID)
	if err != nil {
		return Operation{}, err
	}
	if file.EntryKind != directoryEntry {
		return Operation{}, fmt.Errorf("directory operation requires a directory binding")
	}
	if err := validateDirectoryPath(file.RelativePath); err != nil {
		return Operation{}, err
	}
	if requestKind(request) == KindRename {
		from, to := file.PathKey, pathKey(request.DestinationPath)
		if from == to || strings.HasPrefix(to, from+"/") || strings.HasPrefix(from, to+"/") {
			return Operation{}, fmt.Errorf("directory rename requires distinct, non-overlapping paths")
		}
	}
	base, err := s.Store.Base(ctx, request.BaseID)
	if err != nil {
		return Operation{}, err
	}
	if base.FileID != file.ID || base.Generation != request.Generation {
		return Operation{}, ErrMembership
	}
	if len(base.Content) != 0 || base.Hash != digest(nil) || base.Exists != (base.DirectoryIdentity != nil) {
		return Operation{}, fmt.Errorf("directory operation requires an exact directory base")
	}
	if requestKind(request) != KindEdit && !base.Exists {
		return Operation{}, fmt.Errorf("directory rename/delete requires an existing base")
	}
	return s.Store.Receive(ctx, Operation{Request: request, File: file, Base: base, State: Pending})
}

func (s Service) ApplyDirectory(ctx context.Context, id string) (out Operation, err error) {
	resolver, ok := s.Sources.(DirectoryResolver)
	if s.Store == nil || !ok {
		return out, fmt.Errorf("notes workspace directory resolver is not configured")
	}
	op, err := s.Store.Operation(ctx, id)
	if err != nil {
		return out, err
	}
	err = s.Store.WithLock(ctx, "collection:"+op.File.CollectionID, func(store Store) error {
		op, err = store.Operation(ctx, id)
		if err != nil {
			return err
		}
		if op.File.EntryKind != directoryEntry {
			return fmt.Errorf("operation is not a directory operation")
		}
		if op.State == Accepted || op.State == Conflict {
			out = op
			return nil
		}
		if err := validateDirectoryRequest(op.Request); err != nil {
			return err
		}
		if err := validateDirectoryPath(op.File.RelativePath); err != nil {
			return err
		}
		current, err := store.File(ctx, op.File.ID)
		if err != nil {
			return err
		}
		if current.Deleted || current.EntryKind != directoryEntry || current.RelativePath != op.File.RelativePath || current.PathVersion != op.Base.PathVersion {
			out, err = pathConflict(ctx, store, op, ErrStalePath.Error(), nil, false)
			return err
		}
		paths := []string{op.File.RelativePath}
		if requestKind(op.Request) == KindRename {
			paths = append(paths, op.Request.DestinationPath)
		}
		for _, relative := range paths {
			if busy, err := store.PathBusy(ctx, op.File.CollectionID, pathKey(relative), id); err != nil {
				return err
			} else if busy {
				return ErrPathBusy
			}
		}
		guardErr := resolver.WithDirectoryPaths(ctx, op.File.CollectionID, paths, func(src Source) error {
			if src.CollectionID != op.File.CollectionID || src.Generation != op.Request.Generation {
				return ErrMembership
			}
			if requestKind(op.Request) != KindEdit {
				if protected, ok := store.(SQLStore); ok {
					if err := protected.guardTreeSourceRoots(ctx, src, op.File.RelativePath, op.Request.DestinationPath); err != nil {
						return err
					}
				}
			}
			var e error
			out, e = applyDirectory(ctx, store, src, op)
			return e
		})
		if guardErr == nil {
			return nil
		}
		latest, err := store.Operation(ctx, id)
		if err != nil {
			return errors.Join(guardErr, err)
		}
		if latest.State == Accepted || latest.State == Conflict {
			out = latest
			return guardErr
		}
		latest.State, latest.Reason = Held, guardErr.Error()
		out = latest
		if err := store.SaveOperation(ctx, latest); err != nil {
			return errors.Join(guardErr, err)
		}
		return nil
	})
	return
}

// All mutations are descriptor-relative. Prepared rows reserve paths before
// even parent creation, and observed identities are durable before movement.
func applyDirectory(ctx context.Context, store Store, src Source, op Operation) (Operation, error) {
	if err := ctx.Err(); err != nil {
		return op, err
	}
	if op.PathStage == "" {
		op.PathStage = "prepared"
		op.Journal = ".loom-notes-" + identity(op.Request.OperationID)
		if requestKind(op.Request) == KindRename {
			op.DestinationKey = pathKey(op.Request.DestinationPath)
		}
		if err := store.SaveOperation(ctx, op); err != nil {
			return op, err
		}
	}
	parent, name, err := sourceParent(src.Path, op.File.RelativePath, requestKind(op.Request) == KindEdit && !op.Base.Exists)
	if err != nil {
		return op, err
	}
	defer parent.Close()
	if requestKind(op.Request) == KindEdit {
		return ensureDirectory(ctx, store, parent, name, op)
	}
	if op.MoveIdentity == nil {
		inode, err := sourceDirectoryIdentity(parent, name)
		if err != nil || !sameDirectoryIdentity(inode, op.Base.DirectoryIdentity) {
			return directoryConflict(ctx, store, op, "source directory differs from exact base", err)
		}
		if err := directoryContents(parent.current(), name, op.File.RelativePath, nil); err != nil {
			return directoryConflict(ctx, store, op, "source directory has unacknowledged children", err)
		}
		op.MoveIdentity, op.PathStage = inode, "source_observed"
		if err := store.SaveOperation(ctx, op); err != nil {
			return op, err
		}
	}
	var destination *sourceDirectory
	var target string
	if requestKind(op.Request) == KindRename {
		if _, err := store.FileAt(ctx, op.File.CollectionID, op.DestinationKey); err == nil {
			return pathConflict(ctx, store, op, "directory destination has an active binding", nil, true)
		} else if !errors.Is(err, sql.ErrNoRows) {
			return op, err
		}
		destination, target, err = sourceParent(src.Path, op.Request.DestinationPath, true)
		if err != nil {
			return op, err
		}
		defer destination.Close()
	}
	if op.PathStage == "source_observed" {
		if destination != nil {
			inode, err := sourceDirectoryIdentity(destination, target)
			if err != nil {
				return directoryConflict(ctx, store, op, "directory destination collision", err)
			}
			if inode != nil {
				if err := admittedDirectoryTarget(destination, target, op); err != nil {
					return directoryConflict(ctx, store, op, "directory destination has unacknowledged children", err)
				}
				op.DirectoryTargetIdentity = inode
			}
		}
		op.PathStage = "observed"
		if err := store.SaveOperation(ctx, op); err != nil {
			return op, err
		}
	}
	journal, err := openJournal(parent, op)
	if err != nil {
		return op, err
	}
	defer journal.Close()
	return moveDirectory(ctx, store, parent, name, destination, target, journal, op)
}

func ensureDirectory(ctx context.Context, store Store, parent *sourceDirectory, name string, op Operation) (Operation, error) {
	inode, err := sourceDirectoryIdentity(parent, name)
	if err != nil {
		return directoryConflict(ctx, store, op, "directory ensure collision", err)
	}
	if op.MoveIdentity == nil && inode != nil {
		if op.Base.Exists && !sameDirectoryIdentity(inode, op.Base.DirectoryIdentity) {
			return directoryConflict(ctx, store, op, "source directory differs from exact base", nil)
		}
		op.MoveIdentity, op.PathStage = inode, "adopting"
		if err := store.SaveOperation(ctx, op); err != nil {
			return op, err
		}
	}
	if op.PathStage == "adopting" {
		if err := verifyDirectoryIdentity(parent, name, op.MoveIdentity); err != nil {
			return directoryConflict(ctx, store, op, "adopted directory changed", err)
		}
		if err := errors.Join(parent.current().Sync(), syncDirectoryAt(parent.current(), name)); err != nil {
			return op, err
		}
		if err := verifyDirectoryIdentity(parent, name, op.MoveIdentity); err != nil {
			return op, err
		}
		return commitDirectory(ctx, store, op, op.MoveIdentity)
	}
	if op.Base.Exists && op.MoveIdentity == nil {
		return directoryConflict(ctx, store, op, "source directory is absent", nil)
	}
	journal, err := openJournal(parent, op)
	if err != nil {
		return op, err
	}
	defer journal.Close()
	if op.MoveIdentity == nil {
		if err := unix.Mkdirat(int(journal.Fd()), "created", 0775); err != nil && !errors.Is(err, unix.EEXIST) {
			return op, err
		}
		staged, err := directoryIdentityAt(journal, "created")
		if err != nil || staged == nil {
			return op, errors.Join(err, fmt.Errorf("staged directory missing"))
		}
		if err := errors.Join(syncDirectoryAt(journal, "created"), journal.Sync()); err != nil {
			return op, err
		}
		op.MoveIdentity, op.PathStage = staged, "created"
		if err := store.SaveOperation(ctx, op); err != nil {
			return op, err
		}
	}
	if inode != nil && !sameDirectoryIdentity(inode, op.MoveIdentity) {
		return directoryConflict(ctx, store, op, "directory appeared during create", nil)
	}
	if inode == nil {
		staged, err := directoryIdentityAt(journal, "created")
		if err != nil || !sameDirectoryIdentity(staged, op.MoveIdentity) {
			return op, errors.Join(err, fmt.Errorf("created directory inode missing; recovery required"))
		}
		if err := ctx.Err(); err != nil {
			return op, err
		}
		if err := parent.check(); err != nil {
			return op, err
		}
		if err := checkNameAlias(parent.current(), name); err != nil {
			return op, err
		}
		if err := renameNoReplace(int(journal.Fd()), "created", parent.fd(), name); err != nil {
			if errors.Is(err, unix.EEXIST) {
				return directoryConflict(ctx, store, op, "directory appeared during create", err)
			}
			return op, err
		}
	}
	if err := errors.Join(parent.current().Sync(), journal.Sync()); err != nil {
		return op, err
	}
	if err := verifyDirectoryIdentity(parent, name, op.MoveIdentity); err != nil {
		return op, err
	}
	return commitDirectory(ctx, store, op, op.MoveIdentity)
}

func moveDirectory(ctx context.Context, store Store, parent *sourceDirectory, name string, destination *sourceDirectory, target string, journal *os.File, op Operation) (out Operation, resultErr error) {
	committed := false
	defer func() {
		if !committed && !errors.Is(resultErr, ErrPathCommitUncertain) {
			if err := restoreDisplaced(parent, name, journal); err != nil {
				resultErr = errors.Join(resultErr, fmt.Errorf("restore retained directory: %w", err))
			}
		}
	}()
	displaced, err := directoryIdentityAt(journal, "displaced")
	if err != nil {
		return op, err
	}
	if displaced == nil && destination != nil && op.DirectoryTargetIdentity == nil {
		inode, err := sourceDirectoryIdentity(destination, target)
		if err != nil {
			return op, err
		}
		if sameDirectoryIdentity(inode, op.MoveIdentity) {
			if err := directoryContents(destination.current(), target, op.Request.DestinationPath, nil); err != nil {
				return directoryConflict(ctx, store, op, "renamed directory has unacknowledged children", err)
			}
			if err := errors.Join(destination.current().Sync(), journal.Sync()); err != nil {
				return op, err
			}
			out, err = commitDirectory(ctx, store, op, inode)
			committed = err == nil
			return out, err
		}
	}
	if displaced == nil {
		if err := verifyDirectoryIdentity(parent, name, op.MoveIdentity); err != nil {
			return directoryConflict(ctx, store, op, "source directory changed before retention", err)
		}
		if err := directoryContents(parent.current(), name, op.File.RelativePath, nil); err != nil {
			return directoryConflict(ctx, store, op, "source directory has unacknowledged children", err)
		}
		if err := ctx.Err(); err != nil {
			return op, err
		}
		if err := parent.check(); err != nil {
			return op, err
		}
		if err := renameNoReplace(parent.fd(), name, int(journal.Fd()), "displaced"); err != nil {
			return op, err
		}
		if err := errors.Join(parent.current().Sync(), journal.Sync()); err != nil {
			return op, err
		}
	}
	displaced, err = directoryIdentityAt(journal, "displaced")
	if err != nil || !sameDirectoryIdentity(displaced, op.MoveIdentity) {
		return op, errors.Join(err, fmt.Errorf("retained directory differs from observed inode; recovery required"))
	}
	if err := directoryContents(journal, "displaced", op.File.RelativePath, nil); err != nil {
		return directoryConflict(ctx, store, op, "retained directory has unacknowledged children", err)
	}
	op.PathStage = "detached"
	if err := store.SaveOperation(ctx, op); err != nil {
		return op, err
	}
	if err := ctx.Err(); err != nil {
		return op, err
	}
	if err := parent.check(); err != nil {
		return op, err
	}
	if err := directoryContents(journal, "displaced", op.File.RelativePath, nil); err != nil {
		return directoryConflict(ctx, store, op, "retained directory gained unacknowledged children", err)
	}
	var resultIdentity *inodeIdentity
	if destination != nil {
		inode, err := sourceDirectoryIdentity(destination, target)
		if err != nil {
			return directoryConflict(ctx, store, op, "directory destination collision", err)
		}
		if op.DirectoryTargetIdentity != nil {
			if !sameDirectoryIdentity(inode, op.DirectoryTargetIdentity) {
				return directoryConflict(ctx, store, op, "directory destination inode changed", nil)
			}
			if err := admittedDirectoryTarget(destination, target, op); err != nil {
				return directoryConflict(ctx, store, op, "directory destination has unacknowledged children", err)
			}
			resultIdentity = inode
		} else {
			if inode != nil {
				return directoryConflict(ctx, store, op, "directory destination appeared during rename", nil)
			}
			if err := destination.check(); err != nil {
				return op, err
			}
			if err := checkNameAlias(destination.current(), target); err != nil {
				return op, err
			}
			if err := renameNoReplace(int(journal.Fd()), "displaced", destination.fd(), target); err != nil {
				if errors.Is(err, unix.EEXIST) {
					return directoryConflict(ctx, store, op, "directory destination appeared during rename", err)
				}
				return op, err
			}
			if err := errors.Join(destination.current().Sync(), journal.Sync()); err != nil {
				return op, err
			}
			if err := verifyDirectoryIdentity(destination, target, op.MoveIdentity); err != nil {
				return op, err
			}
			if err := directoryContents(destination.current(), target, op.Request.DestinationPath, nil); err != nil {
				return directoryConflict(ctx, store, op, "renamed directory has unacknowledged children", err)
			}
			resultIdentity = op.MoveIdentity
		}
	}
	// Retained source directories (including child-operation journals) are never
	// recursively removed. Open writers retain their original inode in custody.
	if err := parent.check(); err != nil {
		return op, err
	}
	if destination != nil {
		if err := errors.Join(destination.current().Sync(), syncDirectoryAt(destination.current(), target)); err != nil {
			return op, err
		}
		if err := verifyDirectoryIdentity(destination, target, resultIdentity); err != nil {
			return op, err
		}
	}
	if err := errors.Join(parent.current().Sync(), journal.Sync()); err != nil {
		return op, err
	}
	out, err = commitDirectory(ctx, store, op, resultIdentity)
	committed = err == nil
	return out, err
}

func commitDirectory(ctx context.Context, store Store, op Operation, inode *inodeIdentity) (Operation, error) {
	file := op.File
	if file.PathVersion >= uint64(1<<63-1) {
		return op, fmt.Errorf("directory path version exhausted")
	}
	file.PathVersion++
	switch requestKind(op.Request) {
	case KindRename:
		file.RelativePath, file.PathKey = op.Request.DestinationPath, op.DestinationKey
	case KindDelete:
		file.Deleted = true
	}
	base := makeDirectoryBase(file, op.Request.Generation, inode)
	op.State, op.Reason = Accepted, "directory "+requestKind(op.Request)+" committed"
	op.ResultBaseID, op.ResultFile = base.ID, &file
	return op, store.CommitPath(ctx, op, file, base)
}

func directoryConflict(ctx context.Context, store Store, op Operation, reason string, err error) (Operation, error) {
	if err != nil {
		reason += ": " + err.Error()
	}
	return pathConflict(ctx, store, op, reason, nil, false)
}

func sameDirectoryIdentity(a, b *inodeIdentity) bool { return a != nil && b != nil && *a == *b }

func directoryIdentityAt(parent *os.File, name string) (*inodeIdentity, error) {
	fd, err := unix.Openat(int(parent.Fd()), name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if errors.Is(err, unix.ENOENT) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), name)
	defer f.Close()
	var stat, linked unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return nil, err
	}
	if err := unix.Fstatat(int(parent.Fd()), name, &linked, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return nil, err
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFDIR || linked.Mode&unix.S_IFMT != unix.S_IFDIR || stat.Dev != linked.Dev || stat.Ino != linked.Ino {
		return nil, ErrUnsafePath
	}
	return &inodeIdentity{Device: uint64(stat.Dev), Inode: stat.Ino}, nil
}

func sourceDirectoryIdentity(parent *sourceDirectory, name string) (*inodeIdentity, error) {
	if err := parent.check(); err != nil {
		return nil, err
	}
	if err := checkNameAlias(parent.current(), name); err != nil {
		return nil, err
	}
	inode, err := directoryIdentityAt(parent.current(), name)
	if err != nil {
		return nil, err
	}
	if err := parent.check(); err != nil {
		return nil, err
	}
	return inode, nil
}

func verifyDirectoryIdentity(parent *sourceDirectory, name string, expected *inodeIdentity) error {
	inode, err := sourceDirectoryIdentity(parent, name)
	if err != nil {
		return err
	}
	if !sameDirectoryIdentity(inode, expected) {
		return fmt.Errorf("directory inode changed; recovery required")
	}
	return nil
}

func syncDirectoryAt(parent *os.File, name string) error {
	fd, err := unix.Openat(int(parent.Fd()), name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	f := os.NewFile(uintptr(fd), name)
	defer f.Close()
	return f.Sync()
}

func retainedDirectoryJournal(name string) bool {
	suffix, ok := strings.CutPrefix(name, ".loom-notes-")
	if !ok || len(suffix) != 64 {
		return false
	}
	_, err := hex.DecodeString(suffix)
	return err == nil
}

func admittedDirectoryTarget(parent *sourceDirectory, name string, op Operation) error {
	if len(op.Request.DirectoryTargets) == 0 {
		return fmt.Errorf("existing directory destination requires acknowledged target descendants")
	}
	allowed := make(map[string]bool, len(op.Request.DirectoryTargets))
	for _, relative := range op.Request.DirectoryTargets {
		allowed[relative] = true
	}
	if err := parent.check(); err != nil {
		return err
	}
	if err := directoryContents(parent.current(), name, op.Request.DestinationPath, allowed); err != nil {
		return err
	}
	return parent.check()
}

// Walk only already-acknowledged target paths; source removal supplies no
// allowlist and therefore accepts only retained journals. Recheck each link
// around enumeration so substituted directories/symlinks cannot be traversed.
func directoryContents(parent *os.File, name, relative string, allowed map[string]bool) error {
	before, err := directoryIdentityAt(parent, name)
	if err != nil || before == nil {
		return errors.Join(err, fmt.Errorf("directory unavailable during child check"))
	}
	fd, err := unix.Openat(int(parent.Fd()), name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	dir := os.NewFile(uintptr(fd), name)
	defer dir.Close()
	beforeInfo, err := dir.Stat()
	if err != nil {
		return err
	}
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return err
	}
	if before.Device != uint64(stat.Dev) || before.Inode != stat.Ino {
		return ErrUnsafePath
	}
	entries, err := dir.ReadDir(-1)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		var stat unix.Stat_t
		if err := unix.Fstatat(fd, entry.Name(), &stat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			return err
		}
		isDir := stat.Mode&unix.S_IFMT == unix.S_IFDIR
		if retainedDirectoryJournal(entry.Name()) && isDir {
			continue
		}
		child := relative + "/" + entry.Name()
		if !allowed[child] || validateDirectoryPath(child) != nil {
			return fmt.Errorf("unacknowledged directory descendant %q", child)
		}
		if isDir {
			if err := directoryContents(dir, entry.Name(), child, allowed); err != nil {
				return err
			}
		} else if stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Nlink != 1 {
			return ErrUnsafePath
		}
	}
	after, err := directoryIdentityAt(parent, name)
	if err != nil || !sameDirectoryIdentity(before, after) {
		return errors.Join(err, ErrUnsafePath)
	}
	afterInfo, err := dir.Stat()
	if err != nil {
		return err
	}
	if beforeInfo.Size() != afterInfo.Size() || !beforeInfo.ModTime().Equal(afterInfo.ModTime()) {
		return fmt.Errorf("directory changed during child check")
	}
	return nil
}
