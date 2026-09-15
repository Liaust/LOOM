package dropzone

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/sys/unix"
)

const (
	// HistoricalRecordByteLimit bounds each retained compatibility decode.
	HistoricalRecordByteLimit int64 = 1 << 20
	// HistoricalTransferCorpusLimit bounds the transfer directory inspected by one list call.
	HistoricalTransferCorpusLimit = 1024
	// HistoricalUploadSessionCorpusLimit bounds the session directory inspected by one list call.
	HistoricalUploadSessionCorpusLimit = 1024
)

var (
	ErrHistoricalCorpusLimitExceeded = errors.New("historical Dropzone corpus limit exceeded")
	ErrHistoricalRecordTooLarge      = errors.New("historical Dropzone record exceeds byte limit")
	ErrHistoricalUnsafePath          = errors.New("historical Dropzone path is not a confined regular path")
	ErrHistoricalIdentityMismatch    = errors.New("historical Dropzone decoded identity does not match its path")
)

// Reader provides bounded, read-only compatibility access to historical
// Dropzone transfer records. It intentionally exposes no save/delete methods.
type Reader struct {
	RootPath     string
	StatusDir    string
	TransfersDir string
}

func NewReader(rootPath string, policy Policy, runtimeStateRoot ...string) Reader {
	statusDir := filepath.Join(rootPath, filepath.FromSlash(policy.StatusDir))
	transfersDir := filepath.Join(rootPath, filepath.FromSlash(policy.TransfersDir))
	if len(runtimeStateRoot) > 0 && strings.TrimSpace(runtimeStateRoot[0]) != "" {
		statusDir = filepath.Clean(runtimeStateRoot[0])
		transfersDir = filepath.Join(statusDir, "transfers")
	}
	return Reader{RootPath: rootPath, StatusDir: statusDir, TransfersDir: transfersDir}
}

func (r Reader) Load(transferID string) (TransferRecord, error) {
	path, err := r.RecordPath(transferID)
	if err != nil {
		return TransferRecord{}, err
	}
	root, components, err := r.transferDirectoryComponents()
	if err != nil {
		return TransferRecord{}, err
	}
	directory, err := openHistoricalDirectory(root, components...)
	if err != nil {
		return TransferRecord{}, err
	}
	defer directory.Close()
	record, err := loadTransferRecordAt(directory, transferID+".json", path, transferID)
	if err != nil {
		return TransferRecord{}, err
	}
	if err := directory.Verify(); err != nil {
		return TransferRecord{}, err
	}
	return record, nil
}

func (r Reader) List() ([]TransferRecord, error) {
	root, components, err := r.transferDirectoryComponents()
	if err != nil {
		return nil, err
	}
	directory, err := openHistoricalDirectory(root, components...)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer directory.Close()
	entries, err := readBoundedDirectory(directory.File(), HistoricalTransferCorpusLimit, "transfer")
	if err != nil {
		return nil, err
	}
	records := make([]TransferRecord, 0, len(entries))
	for _, entry := range entries {
		if filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		transferID := strings.TrimSuffix(entry.Name(), ".json")
		if err := validateHistoricalID("transfer_id", transferID); err != nil {
			return nil, err
		}
		path := filepath.Join(r.TransfersDir, entry.Name())
		record, err := loadTransferRecordAt(directory, entry.Name(), path, transferID)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	if err := directory.Verify(); err != nil {
		return nil, err
	}
	sort.SliceStable(records, func(i, j int) bool {
		return records[i].UpdatedAt.After(records[j].UpdatedAt)
	})
	return records, nil
}

func (r Reader) RecordPath(transferID string) (string, error) {
	transferID = strings.TrimSpace(transferID)
	if err := validateHistoricalID("transfer_id", transferID); err != nil {
		return "", err
	}
	return filepath.Join(r.TransfersDir, transferID+".json"), nil
}

func (r Reader) transferDirectoryComponents() (string, []string, error) {
	return confinedPathComponents(r.StatusDir, r.TransfersDir)
}

func loadTransferRecordAt(directory *historicalDirectory, name, path, transferID string) (TransferRecord, error) {
	payload, err := readHistoricalJSONAt(directory, name, path)
	if err != nil {
		return TransferRecord{}, err
	}
	var record TransferRecord
	if err := json.Unmarshal(payload, &record); err != nil {
		return TransferRecord{}, fmt.Errorf("decode historical Dropzone transfer record %s: %w", path, err)
	}
	if record.SchemaVersion != TransferSchemaVersion {
		return TransferRecord{}, fmt.Errorf("historical Dropzone transfer schema_version must be %s", TransferSchemaVersion)
	}
	if record.TransferID != transferID {
		return TransferRecord{}, fmt.Errorf("%w: transfer file %q decoded transfer_id %q", ErrHistoricalIdentityMismatch, transferID, record.TransferID)
	}
	return record, nil
}

// Service exposes only historical upload-session inspection for one
// compatibility release.
type Service struct {
	MainBoxRootPath  string
	RuntimeStateRoot string
}

func NewInspectionService(mainBoxRootPath string, runtimeStateRoot ...string) Service {
	mainBoxRootPath = filepath.Clean(strings.TrimSpace(mainBoxRootPath))
	service := Service{
		MainBoxRootPath:  mainBoxRootPath,
		RuntimeStateRoot: filepath.Join(filepath.Dir(mainBoxRootPath), ".loom-box-state"),
	}
	if len(runtimeStateRoot) > 0 && strings.TrimSpace(runtimeStateRoot[0]) != "" {
		service.RuntimeStateRoot = filepath.Clean(strings.TrimSpace(runtimeStateRoot[0]))
	}
	return service
}

func (s Service) GetUploadSession(_ context.Context, sessionID string) (UploadSession, error) {
	path, err := s.sessionPath(sessionID)
	if err != nil {
		return UploadSession{}, err
	}
	root, err := s.resolveRuntimeStateRoot()
	if err != nil {
		return UploadSession{}, err
	}
	incoming, err := openHistoricalDirectory(root, "dropzone", "incoming")
	if err != nil {
		return UploadSession{}, err
	}
	defer incoming.Close()
	sessionDirectory, err := incoming.OpenDirectory(sessionID)
	if err != nil {
		return UploadSession{}, err
	}
	defer sessionDirectory.Close()
	session, err := loadUploadSessionAt(sessionDirectory, path, sessionID)
	if err != nil {
		return UploadSession{}, err
	}
	if err := sessionDirectory.Verify(); err != nil {
		return UploadSession{}, err
	}
	if err := incoming.Verify(); err != nil {
		return UploadSession{}, err
	}
	return session, nil
}

func (s Service) ListUploadSessions(_ context.Context, limit int) ([]UploadSession, error) {
	if limit <= 0 {
		return nil, fmt.Errorf("limit must be positive")
	}
	root, err := s.resolveRuntimeStateRoot()
	if err != nil {
		return nil, err
	}
	incoming, err := openHistoricalDirectory(root, "dropzone", "incoming")
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer incoming.Close()
	entries, err := readBoundedDirectory(incoming.File(), HistoricalUploadSessionCorpusLimit, "upload-session")
	if err != nil {
		return nil, err
	}
	sessions := make([]UploadSession, 0, len(entries))
	for _, entry := range entries {
		if err := validateHistoricalID("session_id", entry.Name()); err != nil {
			return nil, err
		}
		sessionDirectory, err := incoming.OpenDirectory(entry.Name())
		if err != nil {
			return nil, err
		}
		path := filepath.Join(root, "dropzone", "incoming", entry.Name(), "session.json")
		session, loadErr := loadUploadSessionAt(sessionDirectory, path, entry.Name())
		verifyErr := sessionDirectory.Verify()
		closeErr := sessionDirectory.Close()
		if loadErr != nil {
			return nil, loadErr
		}
		if verifyErr != nil {
			return nil, verifyErr
		}
		if closeErr != nil {
			return nil, closeErr
		}
		sessions = append(sessions, session)
	}
	if err := incoming.Verify(); err != nil {
		return nil, err
	}
	sort.SliceStable(sessions, func(i, j int) bool {
		return sessions[i].UpdatedAt.After(sessions[j].UpdatedAt)
	})
	if len(sessions) > limit {
		sessions = sessions[:limit]
	}
	return sessions, nil
}

func (s Service) sessionPath(sessionID string) (string, error) {
	sessionID = strings.TrimSpace(sessionID)
	if err := validateHistoricalID("session_id", sessionID); err != nil {
		return "", err
	}
	root, err := s.resolveRuntimeStateRoot()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "dropzone", "incoming", sessionID, "session.json"), nil
}

func (s Service) resolveRuntimeStateRoot() (string, error) {
	root := strings.TrimSpace(s.RuntimeStateRoot)
	if root == "" {
		return "", fmt.Errorf("historical Dropzone runtime state root is required")
	}
	if !filepath.IsAbs(root) {
		return "", fmt.Errorf("historical Dropzone runtime state root must be absolute: %s", root)
	}
	root = filepath.Clean(root)
	if root == string(filepath.Separator) {
		return "", fmt.Errorf("historical Dropzone runtime state root cannot be the filesystem root")
	}
	return root, nil
}

func loadUploadSessionAt(directory *historicalDirectory, path, sessionID string) (UploadSession, error) {
	payload, err := readHistoricalJSONAt(directory, "session.json", path)
	if err != nil {
		return UploadSession{}, err
	}
	var session UploadSession
	if err := json.Unmarshal(payload, &session); err != nil {
		return UploadSession{}, fmt.Errorf("decode historical Dropzone upload session %s: %w", path, err)
	}
	if session.SchemaVersion != UploadSessionSchemaVersion {
		return UploadSession{}, fmt.Errorf("historical Dropzone upload session schema_version must be %s", UploadSessionSchemaVersion)
	}
	if session.SessionID != sessionID {
		return UploadSession{}, fmt.Errorf("%w: session directory %q decoded session_id %q", ErrHistoricalIdentityMismatch, sessionID, session.SessionID)
	}
	return session, nil
}

func validateHistoricalID(label, value string) error {
	if value == "" || filepath.Base(value) != value || strings.ContainsAny(value, `/\`+"\x00") {
		return fmt.Errorf("invalid historical Dropzone %s %q", label, value)
	}
	return nil
}

func confinedPathComponents(root, target string) (string, []string, error) {
	root = filepath.Clean(strings.TrimSpace(root))
	target = filepath.Clean(strings.TrimSpace(target))
	if root == "." || target == "." || !filepath.IsAbs(root) || !filepath.IsAbs(target) || root == string(filepath.Separator) {
		return "", nil, fmt.Errorf("%w: historical Dropzone state paths must be bounded absolute paths", ErrHistoricalUnsafePath)
	}
	relative, err := filepath.Rel(root, target)
	if err != nil || relative == "." || relative == ".." || filepath.IsAbs(relative) || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", nil, fmt.Errorf("%w: transfer directory %s is outside state root %s", ErrHistoricalUnsafePath, target, root)
	}
	components := strings.Split(relative, string(filepath.Separator))
	for _, component := range components {
		if err := validatePathComponent(component); err != nil {
			return "", nil, err
		}
	}
	return root, components, nil
}

func validatePathComponent(component string) error {
	if component == "" || component == "." || component == ".." || filepath.Base(component) != component || strings.ContainsRune(component, '\x00') {
		return fmt.Errorf("%w: invalid path component %q", ErrHistoricalUnsafePath, component)
	}
	return nil
}

type historicalDirectory struct {
	rootPath   string
	files      []*os.File
	components []string
}

func openHistoricalDirectory(root string, components ...string) (*historicalDirectory, error) {
	root = filepath.Clean(strings.TrimSpace(root))
	if root == "." || !filepath.IsAbs(root) || root == string(filepath.Separator) {
		return nil, fmt.Errorf("%w: runtime-state root must be a bounded absolute path", ErrHistoricalUnsafePath)
	}
	rootFile, err := openHistoricalRoot(root)
	if err != nil {
		return nil, err
	}
	directory := &historicalDirectory{rootPath: root, files: []*os.File{rootFile}}
	for _, component := range components {
		if err := validatePathComponent(component); err != nil {
			_ = directory.Close()
			return nil, err
		}
		child, err := openHistoricalDirectoryAt(directory.File(), component)
		if err != nil {
			_ = directory.Close()
			return nil, err
		}
		directory.files = append(directory.files, child)
		directory.components = append(directory.components, component)
	}
	return directory, nil
}

func (d *historicalDirectory) File() *os.File {
	return d.files[len(d.files)-1]
}

func (d *historicalDirectory) OpenDirectory(name string) (*historicalDirectory, error) {
	if err := validatePathComponent(name); err != nil {
		return nil, err
	}
	duplicateFD, err := unix.Dup(int(d.File().Fd()))
	if err != nil {
		return nil, fmt.Errorf("duplicate historical Dropzone directory: %w", err)
	}
	parent := os.NewFile(uintptr(duplicateFD), d.File().Name())
	if parent == nil {
		_ = unix.Close(duplicateFD)
		return nil, fmt.Errorf("bind historical Dropzone directory descriptor")
	}
	child, err := openHistoricalDirectoryAt(parent, name)
	if err != nil {
		_ = parent.Close()
		return nil, err
	}
	return &historicalDirectory{files: []*os.File{parent, child}, components: []string{name}}, nil
}

func (d *historicalDirectory) Verify() error {
	if d.rootPath != "" {
		if err := verifyHistoricalRoot(d.rootPath, d.files[0]); err != nil {
			return err
		}
	}
	for index, component := range d.components {
		if err := verifyHistoricalPathAt(d.files[index], component, d.files[index+1], unix.S_IFDIR); err != nil {
			return err
		}
	}
	return nil
}

func (d *historicalDirectory) Close() error {
	var errs []error
	for index := len(d.files) - 1; index >= 0; index-- {
		if err := d.files[index].Close(); err != nil {
			errs = append(errs, err)
		}
	}
	d.files = nil
	return errors.Join(errs...)
}

func openHistoricalRoot(path string) (*os.File, error) {
	var before unix.Stat_t
	if err := unix.Lstat(path, &before); err != nil {
		return nil, fmt.Errorf("inspect historical Dropzone root %s: %w", path, err)
	}
	if uint32(before.Mode)&unix.S_IFMT != unix.S_IFDIR {
		return nil, fmt.Errorf("%w: runtime-state root %s is not a real directory", ErrHistoricalUnsafePath, path)
	}
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, fmt.Errorf("open historical Dropzone root without following links: %w", err)
	}
	file := os.NewFile(uintptr(fd), path)
	if file == nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("bind historical Dropzone root descriptor")
	}
	if err := verifyHistoricalRootStat(path, file, before); err != nil {
		_ = file.Close()
		return nil, err
	}
	return file, nil
}

func verifyHistoricalRoot(path string, file *os.File) error {
	var current unix.Stat_t
	if err := unix.Lstat(path, &current); err != nil {
		return fmt.Errorf("%w: re-inspect historical Dropzone root %s: %v", ErrHistoricalUnsafePath, path, err)
	}
	return verifyHistoricalRootStat(path, file, current)
}

func verifyHistoricalRootStat(path string, file *os.File, named unix.Stat_t) error {
	var opened unix.Stat_t
	if err := unix.Fstat(int(file.Fd()), &opened); err != nil {
		return fmt.Errorf("inspect opened historical Dropzone root %s: %w", path, err)
	}
	if !matchingHistoricalStat(opened, named, unix.S_IFDIR) {
		return fmt.Errorf("%w: runtime-state root %s changed or is not a real directory", ErrHistoricalUnsafePath, path)
	}
	return nil
}

func openHistoricalDirectoryAt(parent *os.File, name string) (*os.File, error) {
	var before unix.Stat_t
	if err := unix.Fstatat(int(parent.Fd()), name, &before, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return nil, fmt.Errorf("inspect historical Dropzone directory %s: %w", name, err)
	}
	if uint32(before.Mode)&unix.S_IFMT != unix.S_IFDIR {
		return nil, fmt.Errorf("%w: %s is not a real directory", ErrHistoricalUnsafePath, name)
	}
	fd, err := unix.Openat(int(parent.Fd()), name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, fmt.Errorf("open historical Dropzone directory %s without following links: %w", name, err)
	}
	file := os.NewFile(uintptr(fd), filepath.Join(parent.Name(), name))
	if file == nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("bind historical Dropzone directory descriptor")
	}
	if err := verifyHistoricalPathAt(parent, name, file, unix.S_IFDIR); err != nil || !sameHistoricalIdentity(file, before, unix.S_IFDIR) {
		_ = file.Close()
		if err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("%w: directory %s changed while opening", ErrHistoricalUnsafePath, name)
	}
	return file, nil
}

func readBoundedDirectory(directory *os.File, limit int, corpus string) ([]os.DirEntry, error) {
	entries, err := directory.ReadDir(limit + 1)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("enumerate historical Dropzone %s corpus: %w", corpus, err)
	}
	if len(entries) > limit {
		return nil, fmt.Errorf("%w: %s corpus has more than %d directory entries", ErrHistoricalCorpusLimitExceeded, corpus, limit)
	}
	return entries, nil
}

func readHistoricalJSONAt(directory *historicalDirectory, name, path string) ([]byte, error) {
	if err := validatePathComponent(name); err != nil {
		return nil, err
	}
	parent := directory.File()
	var before unix.Stat_t
	if err := unix.Fstatat(int(parent.Fd()), name, &before, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return nil, fmt.Errorf("inspect historical Dropzone JSON %s: %w", path, err)
	}
	if uint32(before.Mode)&unix.S_IFMT != unix.S_IFREG {
		return nil, fmt.Errorf("%w: %s is not a real regular JSON file", ErrHistoricalUnsafePath, path)
	}
	if before.Size > HistoricalRecordByteLimit {
		return nil, fmt.Errorf("%w: %s is %d bytes (limit %d)", ErrHistoricalRecordTooLarge, path, before.Size, HistoricalRecordByteLimit)
	}
	fd, err := unix.Openat(int(parent.Fd()), name, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, fmt.Errorf("open historical Dropzone JSON %s without following links: %w", path, err)
	}
	file := os.NewFile(uintptr(fd), path)
	if file == nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("bind historical Dropzone JSON descriptor")
	}
	defer file.Close()
	if err := verifyHistoricalPathAt(parent, name, file, unix.S_IFREG); err != nil || !sameHistoricalIdentity(file, before, unix.S_IFREG) {
		if err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("%w: JSON path %s changed while opening", ErrHistoricalUnsafePath, path)
	}
	payload, err := io.ReadAll(io.LimitReader(file, HistoricalRecordByteLimit+1))
	if err != nil {
		return nil, fmt.Errorf("read historical Dropzone JSON %s: %w", path, err)
	}
	if int64(len(payload)) > HistoricalRecordByteLimit {
		return nil, fmt.Errorf("%w: %s grew beyond %d bytes", ErrHistoricalRecordTooLarge, path, HistoricalRecordByteLimit)
	}
	var after unix.Stat_t
	if err := unix.Fstat(int(file.Fd()), &after); err != nil {
		return nil, fmt.Errorf("inspect read historical Dropzone JSON %s: %w", path, err)
	}
	if !matchingHistoricalStat(after, before, unix.S_IFREG) || after.Size != before.Size || after.Size != int64(len(payload)) {
		return nil, fmt.Errorf("%w: JSON path %s changed while reading", ErrHistoricalUnsafePath, path)
	}
	if err := verifyHistoricalPathAt(parent, name, file, unix.S_IFREG); err != nil {
		return nil, err
	}
	if err := directory.Verify(); err != nil {
		return nil, err
	}
	return payload, nil
}

func verifyHistoricalPathAt(parent *os.File, name string, opened *os.File, kind uint32) error {
	var descriptor unix.Stat_t
	if err := unix.Fstat(int(opened.Fd()), &descriptor); err != nil {
		return fmt.Errorf("inspect opened historical Dropzone path %s: %w", name, err)
	}
	var named unix.Stat_t
	if err := unix.Fstatat(int(parent.Fd()), name, &named, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return fmt.Errorf("%w: re-inspect historical Dropzone path %s: %v", ErrHistoricalUnsafePath, name, err)
	}
	if !matchingHistoricalStat(descriptor, named, kind) {
		return fmt.Errorf("%w: path %s changed or has an unsupported type", ErrHistoricalUnsafePath, name)
	}
	return nil
}

func sameHistoricalIdentity(file *os.File, expected unix.Stat_t, kind uint32) bool {
	var actual unix.Stat_t
	return unix.Fstat(int(file.Fd()), &actual) == nil && matchingHistoricalStat(actual, expected, kind)
}

func matchingHistoricalStat(left, right unix.Stat_t, kind uint32) bool {
	return uint32(left.Mode)&unix.S_IFMT == kind &&
		uint32(right.Mode)&unix.S_IFMT == kind &&
		left.Dev == right.Dev && left.Ino == right.Ino
}
