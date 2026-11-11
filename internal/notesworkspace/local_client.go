package notesworkspace

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

// LocalClientRequest is a local filesystem operation, not a source enrollment or
// a Main acceptance receipt. Displaced inodes remain in the source-side journal.
type LocalClientRequest struct {
	RootIdentity string `json:"root_identity"`
	ID           string `json:"id"`
	Kind         string `json:"kind"`
	Path         string `json:"path"`
	Target       string `json:"target,omitempty"`
	Text         string `json:"text,omitempty"`
	Base         string `json:"base,omitempty"`
	Identity     string `json:"identity,omitempty"`
	Source       string `json:"source,omitempty"`
	SHA256       string `json:"sha256,omitempty"`
	Size         int64  `json:"size,omitempty"`
}

func ApplyLocalClient(root string, r LocalClientRequest) error {
	if r.ID == "" || len(r.ID) > 80 || strings.ContainsAny(r.ID, "/\\\x00") {
		return ErrUnsafePath
	}
	if err := validateDirectoryPath(r.Path); err != nil {
		return err
	}
	if r.Kind == "import_attachment" {
		if err := validateLocalAttachmentRequest(&r); err != nil {
			return err
		}
	}
	parent, name, err := openSourceParent(root, r.Path)
	if err != nil {
		return err
	}
	defer parent.Close()
	depth := len(strings.Split(strings.TrimPrefix(root, "/"), "/"))
	var rootStat unix.Stat_t
	if err = unix.Fstat(int(parent.dirs[depth].Fd()), &rootStat); err != nil {
		return err
	}
	if fmt.Sprintf("%d:%d", rootStat.Dev, rootStat.Ino) != r.RootIdentity {
		return ErrUnsafePath
	}
	if err = parent.check(); err != nil {
		return err
	}
	op := Operation{Request: Request{OperationID: "mac-" + r.ID, Kind: r.Kind, Content: []byte(r.Text), DestinationPath: r.Target}, Journal: ".loom-notes-" + identity("mac-"+r.ID)}
	journal, err := openJournal(parent, op)
	if err != nil {
		return err
	}
	defer journal.Close()
	payload, _ := json.Marshal(r)
	if err = writeAt(journal, "local-intent.json", payload); err != nil {
		return err
	}
	if err = journal.Sync(); err != nil {
		return err
	}
	switch r.Kind {
	case "import_attachment":
		return applyLocalAttachment(parent, name, journal, op.Journal, r)
	case "save", "create_note":
		if !IsEditableTextPath(r.Path) {
			return ErrReferenceOnly
		}
		if err = validateContent([]byte(r.Text)); err != nil {
			return err
		}
		current, exists, err := readSource(parent, name)
		if err != nil {
			return err
		}
		if r.Kind == "save" && (!exists || strings.TrimPrefix(digest(current), "sha256:") != r.Base) {
			return fmt.Errorf("file changed on disk; draft preserved")
		}
		if r.Kind == "create_note" && exists {
			return fmt.Errorf("destination already exists")
		}
		if exists && bytes.Equal(current, []byte(r.Text)) {
			return nil
		}
		if err = stageProposed(journal, op); err != nil {
			return err
		}
		if exists {
			if err = parent.check(); err != nil {
				return err
			}
			if err = renameNoReplace(parent.fd(), name, int(journal.Fd()), "displaced"); err != nil {
				return err
			}
			restore := func(e error) error { return errors.Join(e, restoreDisplaced(parent, name, journal)) }
			displaced, _, err := readAt(journal, "displaced")
			if err != nil {
				return restore(err)
			}
			if !bytes.Equal(displaced, current) {
				return restore(fmt.Errorf("external edit raced save; original and draft preserved"))
			}
			if err = preserveReplacementMetadata(journal); err != nil {
				return restore(err)
			}
			if err = parent.check(); err != nil {
				return restore(err)
			}
			if err = renameNoReplace(int(journal.Fd()), "proposed", parent.fd(), name); err != nil {
				return restore(err)
			}
		} else {
			if err = parent.check(); err != nil {
				return err
			}
			if err = renameNoReplace(int(journal.Fd()), "proposed", parent.fd(), name); err != nil {
				return err
			}
		}
	case "create_folder":
		if err = unix.Mkdirat(parent.fd(), name, 0755); err != nil {
			return err
		}
	case "rename", "trash":
		fd, err := unix.Openat(parent.fd(), name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
		if err != nil {
			return err
		}
		original := os.NewFile(uintptr(fd), name)
		defer original.Close()
		st, err := original.Stat()
		if err != nil {
			return err
		}
		var s unix.Stat_t
		if err = unix.Fstat(fd, &s); err != nil {
			return err
		}
		if (!st.Mode().IsRegular() && !st.IsDir()) || fmt.Sprintf("%d:%d", s.Dev, s.Ino) != r.Identity {
			return ErrStalePath
		}
		targetParent, targetName := parent, "removed"
		targetFD := int(journal.Fd())
		if r.Kind == "rename" {
			if err = validateDirectoryPath(r.Target); err != nil {
				return err
			}
			if r.Target == r.Path || strings.HasPrefix(r.Target, r.Path+"/") {
				return ErrUnsafePath
			}
			targetParent, targetName, err = openSourceParent(root, r.Target)
			if err != nil {
				return err
			}
			defer targetParent.Close()
			if err = targetParent.check(); err != nil {
				return err
			}
			targetFD = targetParent.fd()
			// Reject physical ancestry (including case aliases), not only string paths.
			if st.IsDir() {
				for _, dir := range targetParent.dirs {
					info, e := dir.Stat()
					if e != nil {
						return e
					}
					if os.SameFile(st, info) {
						return ErrUnsafePath
					}
				}
			}
		}
		if err = parent.check(); err != nil {
			return err
		}
		var now unix.Stat_t
		if err = unix.Fstatat(parent.fd(), name, &now, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			return err
		}
		if now.Dev != s.Dev || now.Ino != s.Ino {
			return ErrStalePath
		}
		if err = renameNoReplace(parent.fd(), name, targetFD, targetName); err != nil {
			return err
		}
		if err = unix.Fsync(targetFD); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unsupported local file operation")
	}
	if err = errors.Join(parent.current().Sync(), journal.Sync()); err != nil {
		return err
	}
	return writeAt(journal, "complete", []byte(filepath.ToSlash(r.Path)))
}
