package notesworkspace

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

const maxLocalAttachmentBytes int64 = 2 << 30

// import_attachment copies a main-process-vetted private staging file to the
// exact Path; it does not authorize renderer paths or choose collision names.
// SHA256 accepts 64 hex digits, optionally prefixed by "sha256:". Normalize both
// forms (and hex case) before binding the immutable local intent.
func validateLocalAttachmentRequest(r *LocalClientRequest) error {
	if !filepath.IsAbs(r.Source) || filepath.Clean(r.Source) != r.Source || r.Source == "/" || strings.ContainsRune(r.Source, 0) {
		return ErrUnsafePath
	}
	hash := strings.TrimPrefix(r.SHA256, "sha256:")
	if decoded, err := hex.DecodeString(hash); err != nil || len(decoded) != sha256.Size {
		return fmt.Errorf("attachment requires an exact SHA-256 digest")
	}
	if r.Size < 0 || r.Size > maxLocalAttachmentBytes {
		return fmt.Errorf("attachment size must be between 0 and 2 GiB")
	}
	if r.Text != "" || r.Target != "" || r.Base != "" || r.Identity != "" {
		return ErrIntentMismatch
	}
	r.SHA256 = "sha256:" + strings.ToLower(hash)
	return nil
}

type localAttachmentSource struct {
	parent   *sourceDirectory
	name     string
	file     *os.File
	observed MetadataActionIdentity
}

type localAttachmentSourceReceipt struct {
	File    MetadataActionIdentity `json:"file"`
	Parents []inodeIdentity        `json:"parents"`
}

type localAttachmentPrepared struct {
	File   MetadataActionIdentity `json:"file"`
	Parent inodeIdentity          `json:"parent"`
}

func localAttachmentStat(file *os.File) (MetadataActionIdentity, error) {
	var st unix.Stat_t
	if err := unix.Fstat(int(file.Fd()), &st); err != nil {
		return MetadataActionIdentity{}, err
	}
	if st.Mode&unix.S_IFMT != unix.S_IFREG || st.Nlink != 1 {
		return MetadataActionIdentity{}, ErrUnsafePath
	}
	return MetadataActionIdentity{Device: uint64(st.Dev), Inode: st.Ino, Size: st.Size,
		Modified: st.Mtim.Nano(), Changed: st.Ctim.Nano(), Mode: uint32(st.Mode), Links: uint64(st.Nlink)}, nil
}

func localAttachmentDirectoryID(file *os.File) (inodeIdentity, error) {
	var st unix.Stat_t
	if err := unix.Fstat(int(file.Fd()), &st); err != nil {
		return inodeIdentity{}, err
	}
	if st.Mode&unix.S_IFMT != unix.S_IFDIR {
		return inodeIdentity{}, ErrUnsafePath
	}
	return inodeIdentity{Device: uint64(st.Dev), Inode: st.Ino}, nil
}

func openLocalAttachmentFile(directory *os.File, name string) (*os.File, MetadataActionIdentity, error) {
	// Inspect before opening too: even a nonblocking open must not touch a device.
	named, err := metadataActionAt(int(directory.Fd()), name)
	if err != nil {
		return nil, MetadataActionIdentity{}, err
	}
	if named == nil {
		return nil, MetadataActionIdentity{}, os.ErrNotExist
	}
	if named.Links != 1 {
		return nil, MetadataActionIdentity{}, ErrUnsafePath
	}
	fd, err := unix.Openat(int(directory.Fd()), name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, MetadataActionIdentity{}, err
	}
	file := os.NewFile(uintptr(fd), name)
	observed, err := localAttachmentStat(file)
	if err == nil && observed != *named {
		err = ErrStalePath
	}
	if err != nil {
		file.Close()
		return nil, MetadataActionIdentity{}, err
	}
	return file, observed, nil
}

func checkLocalAttachmentFile(directory *os.File, name string, file *os.File, expected MetadataActionIdentity) error {
	actual, err := localAttachmentStat(file)
	if err != nil {
		return err
	}
	named, err := metadataActionAt(int(directory.Fd()), name)
	if err != nil {
		return err
	}
	if actual != expected || named == nil || *named != expected {
		return ErrStalePath
	}
	return nil
}

func openLocalAttachmentSource(path string) (*localAttachmentSource, error) {
	parent, name, err := openSourceParent(filepath.Dir(path), filepath.Base(path))
	if err != nil {
		return nil, err
	}
	file, observed, err := openLocalAttachmentFile(parent.current(), name)
	if err != nil {
		parent.Close()
		return nil, err
	}
	source := &localAttachmentSource{parent: parent, name: name, file: file, observed: observed}
	if err := source.check(); err != nil {
		source.close()
		return nil, err
	}
	return source, nil
}

func (s *localAttachmentSource) close() {
	s.file.Close()
	s.parent.Close()
}

func (s *localAttachmentSource) check() error {
	return errors.Join(s.parent.check(), checkLocalAttachmentFile(s.parent.current(), s.name, s.file, s.observed))
}

func (s *localAttachmentSource) receipt() (localAttachmentSourceReceipt, error) {
	r := localAttachmentSourceReceipt{File: s.observed}
	for _, dir := range s.parent.dirs {
		id, err := localAttachmentDirectoryID(dir)
		if err != nil {
			return r, err
		}
		r.Parents = append(r.Parents, id)
	}
	return r, nil
}

func checkLocalAttachmentJournal(parent *sourceDirectory, journal *os.File, name string) error {
	if err := parent.check(); err != nil {
		return err
	}
	id, err := localAttachmentDirectoryID(journal)
	if err != nil {
		return err
	}
	var named unix.Stat_t
	if err := unix.Fstatat(parent.fd(), name, &named, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return err
	}
	if named.Mode&unix.S_IFMT != unix.S_IFDIR || uint64(named.Dev) != id.Device || named.Ino != id.Inode || named.Mode&0777 != 0700 {
		return ErrUnsafePath
	}
	return nil
}

func applyLocalAttachment(parent *sourceDirectory, name string, journal *os.File, journalName string, r LocalClientRequest) error {
	// Exact-request retries share one journal. A competing helper must not prepare
	// a second inode while the first helper is between preparation and rename.
	if err := unix.Flock(int(journal.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		return err
	}
	defer unix.Flock(int(journal.Fd()), unix.LOCK_UN)
	checkJournal := func() error { return checkLocalAttachmentJournal(parent, journal, journalName) }
	if err := checkJournal(); err != nil {
		return err
	}
	parentID, err := localAttachmentDirectoryID(parent.current())
	if err != nil {
		return err
	}
	var prepared *localAttachmentPrepared
	payload, exists, err := readAt(journal, "attachment-prepared.json")
	if err != nil {
		return err
	}
	if exists {
		prepared = &localAttachmentPrepared{}
		if err := json.Unmarshal(payload, prepared); err != nil {
			return err
		}
		if prepared.Parent != parentID {
			return ErrStalePath
		}
	}
	if _, recorded, err := readAt(journal, "attachment-source.json"); err != nil {
		return err
	} else if prepared != nil && !recorded {
		return fmt.Errorf("prepared attachment source receipt is absent; recovery required")
	}
	target, err := metadataActionAt(parent.fd(), name)
	if err != nil {
		return err
	}
	if target != nil && (prepared == nil || !prepared.File.matchesMoved(target)) {
		return fmt.Errorf("attachment destination already exists or was replaced")
	}
	source, err := openLocalAttachmentSource(r.Source)
	if err != nil {
		return err
	}
	defer source.close()
	if source.observed.Size != r.Size {
		return fmt.Errorf("attachment source size differs from declared size")
	}
	sourceReceipt, err := source.receipt()
	if err != nil {
		return err
	}
	payload, err = json.Marshal(sourceReceipt)
	if err != nil {
		return err
	}
	if err := writeAt(journal, "attachment-source.json", payload); err != nil {
		return err
	}
	if err := journal.Sync(); err != nil {
		return err
	}
	if target == nil {
		staged, err := metadataActionAt(int(journal.Fd()), "attachment")
		if err != nil {
			return err
		}
		if staged == nil {
			if prepared != nil {
				return fmt.Errorf("prepared attachment inode is absent; recovery required")
			}
			if err := stageLocalAttachment(source, journal, r); err != nil {
				return err
			}
		}
		var expected *MetadataActionIdentity
		if prepared != nil {
			expected = &prepared.File
		}
		staged, err = verifyLocalAttachmentAt(journal, "attachment", r, expected)
		if err != nil {
			return err
		}
		if prepared == nil {
			prepared = &localAttachmentPrepared{File: *staged, Parent: parentID}
			payload, err := json.Marshal(prepared)
			if err != nil {
				return err
			}
			if err := writeAt(journal, "attachment-prepared.json", payload); err != nil {
				return err
			}
		}
		// Persist the published-inode proof before the only move into user space.
		if err := journal.Sync(); err != nil {
			return err
		}
		if err := errors.Join(checkJournal(), source.check(), checkNameAlias(parent.current(), name)); err != nil {
			return err
		}
		actual, exists, err := inodeAt(journal, "attachment")
		if err != nil {
			return err
		}
		if !exists || actual != prepared.File.inode() {
			return ErrStalePath
		}
		if err := renameNoReplace(int(journal.Fd()), "attachment", parent.fd(), name); err != nil {
			return err
		}
	}
	if err := errors.Join(parent.current().Sync(), journal.Sync(), checkJournal(), source.check()); err != nil {
		return err
	}
	published, err := verifyLocalAttachmentAt(parent.current(), name, r, &prepared.File)
	if err != nil {
		return err
	}
	if err := errors.Join(checkJournal(), source.check()); err != nil {
		return err
	}
	actual, err := metadataActionAt(parent.fd(), name)
	if err != nil {
		return err
	}
	if actual == nil || *actual != *published {
		return ErrStalePath
	}
	if err := writeAt(journal, "complete", []byte(r.Path)); err != nil {
		return err
	}
	return journal.Sync()
}

func stageLocalAttachment(source *localAttachmentSource, journal *os.File, r LocalClientRequest) error {
	id, err := newFileID()
	if err != nil {
		return err
	}
	temporary := ".staged-attachment-" + id
	fd, err := unix.Openat(int(journal.Fd()), temporary, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0666)
	if err != nil {
		return err
	}
	file := os.NewFile(uintptr(fd), temporary)
	h := sha256.New()
	count, copyErr := io.CopyBuffer(io.MultiWriter(file, h), io.LimitReader(source.file, r.Size+1), make([]byte, 128<<10))
	// Keep even interrupted/invalid temporary copies and flush their directory
	// entry. Only a complete verified copy receives the reusable staging name.
	err = errors.Join(copyErr, file.Sync(), file.Close(), journal.Sync(), source.check())
	if err != nil {
		return err
	}
	if count != r.Size || "sha256:"+hex.EncodeToString(h.Sum(nil)) != r.SHA256 {
		return fmt.Errorf("attachment SHA-256 or size mismatch")
	}
	if err := renameNoReplace(int(journal.Fd()), temporary, int(journal.Fd()), "attachment"); err != nil {
		return err
	}
	return journal.Sync()
}

func verifyLocalAttachmentAt(directory *os.File, name string, r LocalClientRequest, expected *MetadataActionIdentity) (*MetadataActionIdentity, error) {
	file, observed, err := openLocalAttachmentFile(directory, name)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	if observed.Size != r.Size || (expected != nil && !expected.matchesMoved(&observed)) {
		return nil, ErrStalePath
	}
	h := sha256.New()
	count, err := io.CopyBuffer(h, io.LimitReader(file, r.Size+1), make([]byte, 128<<10))
	if err != nil {
		return nil, err
	}
	if err := checkLocalAttachmentFile(directory, name, file, observed); err != nil {
		return nil, err
	}
	if count != r.Size || "sha256:"+hex.EncodeToString(h.Sum(nil)) != r.SHA256 {
		return nil, fmt.Errorf("attachment SHA-256 or size mismatch")
	}
	return &observed, nil
}
