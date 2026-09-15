package runtime

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math/rand"
	"os"
	"path/filepath"
	goruntime "runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"

	"loom.local/loom/internal/projectquiescence"
)

type Store struct {
	DataDir string

	// Test-only hooks make the security-relevant publication and read windows
	// deterministic without widening the production API.
	beforeProjectArchivePublish func(path string)
	afterProjectArchiveReadOpen func(path string)
}

var ErrProjectArchiveFenced = errors.New("project archive target is durably fenced")
var ErrProjectArchivePublicationCommitted = errors.New("project archive state publication committed but durability confirmation failed")
var ErrProjectArchivePublicationIndeterminate = errors.New("project archive state publication outcome is indeterminate")

type SecureFileIdentity struct {
	Path  string
	Mode  fs.FileMode
	Size  int64
	Dev   uint64
	Ino   uint64
	Nlink uint64
}

func NewStore(dataDir string) Store {
	return Store{DataDir: dataDir}
}

func (s Store) Ensure() error {
	if strings.TrimSpace(s.DataDir) == "" {
		return errors.New("node-agent runtime data dir is required")
	}
	rootFD, _, _, err := openDirectoryPathNoFollow(s.DataDir, true)
	if err != nil {
		return err
	}
	if err := unix.Close(rootFD); err != nil {
		return err
	}
	for _, dir := range []string{
		filepath.Join(s.DataDir, "inbox"),
		filepath.Join(s.DataDir, "inbox", OutboxStatusPending),
		filepath.Join(s.DataDir, "inbox", OutboxStatusDone),
		filepath.Join(s.DataDir, "inbox", OutboxStatusFailed),
		filepath.Join(s.DataDir, "outbox"),
		filepath.Join(s.DataDir, "outbox", OutboxStatusPending),
		filepath.Join(s.DataDir, "outbox", OutboxStatusInflight),
		filepath.Join(s.DataDir, "outbox", OutboxStatusDone),
		filepath.Join(s.DataDir, "outbox", OutboxStatusFailed),
		filepath.Join(s.DataDir, "outbox", OutboxStatusManualAction),
		filepath.Join(s.DataDir, "outbox", "payloads"),
		filepath.Join(s.DataDir, "workers"),
		filepath.Join(s.DataDir, "workers", "instances"),
		filepath.Join(s.DataDir, "workers", "checkpoints"),
		filepath.Join(s.DataDir, "workers", "runs"),
		filepath.Join(s.DataDir, "workers", "health"),
		filepath.Join(s.DataDir, "workers", "controls"),
		filepath.Join(s.DataDir, "workers", "findings"),
		filepath.Join(s.DataDir, "workers", "locks"),
		filepath.Join(s.DataDir, "summaries"),
		filepath.Join(s.DataDir, "logs"),
	} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
	}
	for _, leaf := range []string{"locks", "fences", "receipts"} {
		directory, err := s.openProjectArchiveDirectory(leaf)
		if err != nil {
			return err
		}
		if err := directory.close(); err != nil {
			return err
		}
	}
	return nil
}

func DefaultInstances(input DefaultInstanceInput) []WorkerInstance {
	heartbeatInterval := input.HeartbeatIntervalSeconds
	if heartbeatInterval <= 0 {
		heartbeatInterval = defaultHeartbeatIntervalSeconds
	}
	pollInterval := input.PollIntervalSeconds
	if pollInterval <= 0 {
		pollInterval = defaultPollIntervalSeconds
	}
	now := time.Now().UTC()
	return []WorkerInstance{
		defaultInstance(WorkerKeySupervisorSelfcheck, KindSupervisorSelfcheck, "Node Supervisor Selfcheck", defaultSelfcheckIntervalSeconds, now),
		defaultInstance(WorkerKeyHeartbeat, KindHeartbeat, "Node Heartbeat", heartbeatInterval, now),
		defaultInstance(WorkerKeyPoll, KindPoll, "Node Poll", pollInterval, now),
		defaultInstance(WorkerKeyOutboxFlusher, KindOutboxFlusher, "Node Outbox Flusher", defaultFlusherIntervalSeconds, now),
		defaultInstance(WorkerKeyLocalQueueReporter, KindLocalQueueReporter, "Local Queue Reporter", defaultReporterIntervalSeconds, now),
		defaultInstance(WorkerKeyStorageMount, KindStorageMount, "LOOM Main Storage Mount", 60, now),
		defaultInstance(WorkerKeyLaneHousekeeping, KindLaneHousekeeping, "LOOM Lane Recovery Housekeeping", defaultLaneHousekeepingIntervalSeconds, now),
	}
}

func defaultInstance(workerKey, kind, displayName string, intervalSeconds int, now time.Time) WorkerInstance {
	return WorkerInstance{
		WorkerKey:           workerKey,
		Kind:                kind,
		DisplayName:         displayName,
		Enabled:             true,
		IntervalSeconds:     intervalSeconds,
		LeaseTimeoutSeconds: defaultLeaseTimeoutSeconds,
		ConfigJSON:          json.RawMessage(`{}`),
		CreatedAt:           now,
		UpdatedAt:           now,
	}
}

func (s Store) EnsureDefaultInstances(input DefaultInstanceInput) error {
	if err := s.Ensure(); err != nil {
		return err
	}
	for _, instance := range DefaultInstances(input) {
		if _, err := s.LoadInstance(instance.WorkerKey); err == nil {
			continue
		} else if !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		if err := s.SaveInstance(instance); err != nil {
			return err
		}
	}
	return nil
}

func (s Store) LoadInstances() ([]WorkerInstance, error) {
	if err := s.Ensure(); err != nil {
		return nil, err
	}
	dir := s.instancesDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	instances := make([]WorkerInstance, 0, len(entries))
	for _, entry := range entries {
		if !entry.Type().IsRegular() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		var instance WorkerInstance
		if err := readJSONFile(filepath.Join(dir, entry.Name()), &instance); err != nil {
			return nil, err
		}
		instances = append(instances, instance)
	}
	sort.Slice(instances, func(i, j int) bool {
		return instances[i].WorkerKey < instances[j].WorkerKey
	})
	return instances, nil
}

func (s Store) LoadInstance(workerKey string) (WorkerInstance, error) {
	var instance WorkerInstance
	if strings.TrimSpace(workerKey) == "" {
		return WorkerInstance{}, errors.New("worker key is required")
	}
	err := readJSONFile(s.instancePath(workerKey), &instance)
	return instance, err
}

func (s Store) SaveInstance(instance WorkerInstance) error {
	if instance.Kind == KindWatchedRoot {
		release, err := s.AcquireWorkerExecutionLock(context.Background(), instance.WorkerKey)
		if err != nil {
			return err
		}
		defer func() { _ = release() }()
		return s.SaveInstanceWhileLocked(instance)
	}
	return s.saveInstance(instance)
}

// SaveInstanceWhileLocked is reserved for callers already holding the exact
// worker execution lock. It keeps quiescence and every watched-root writer on
// one lock domain without recursively taking flock.
func (s Store) SaveInstanceWhileLocked(instance WorkerInstance) error {
	if instance.Kind == KindWatchedRoot && instance.Enabled {
		fenced, err := s.ProjectArchiveFenceActive(projectquiescence.TargetKindWatchedRoot, instance.WorkerKey)
		if err != nil {
			return fmt.Errorf("inspect watched-root archive fence: %w", err)
		}
		if fenced {
			return ErrProjectArchiveFenced
		}
	}
	return s.saveInstance(instance)
}

func (s Store) saveInstance(instance WorkerInstance) error {
	if err := s.Ensure(); err != nil {
		return err
	}
	if strings.TrimSpace(instance.WorkerKey) == "" {
		return errors.New("worker key is required")
	}
	if strings.TrimSpace(instance.Kind) == "" {
		return errors.New("worker kind is required")
	}
	now := time.Now().UTC()
	if instance.CreatedAt.IsZero() {
		instance.CreatedAt = now
	}
	instance.UpdatedAt = now
	if len(instance.ConfigJSON) == 0 {
		instance.ConfigJSON = json.RawMessage(`{}`)
	}
	return writeJSONFile(s.instancePath(instance.WorkerKey), instance, 0o600)
}

func (s Store) AcquireWorkerExecutionLock(ctx context.Context, workerKey string) (func() error, error) {
	return s.acquireProjectArchiveLock(ctx, "worker:"+workerKey)
}

func (s Store) AcquireServiceExecutionLock(ctx context.Context, allowlistKey string) (func() error, error) {
	return s.acquireProjectArchiveLock(ctx, "service:"+allowlistKey)
}

func (s Store) AcquireProjectArchiveOperationLock(ctx context.Context, operationID string) (func() error, error) {
	return s.acquireProjectArchiveLock(ctx, "operation:"+operationID)
}

func (s Store) ProjectArchiveFenceActive(kind, targetIdentity string) (bool, error) {
	raw, _, err := s.ReadProjectArchiveFence(kind, targetIdentity)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return true, err
	}
	var fence projectquiescence.Fence
	if err := decodeCanonicalSecureJSON(raw, &fence); err != nil {
		return true, err
	}
	if fence.State != projectquiescence.FenceStateActive || fence.Target.Kind != kind {
		return true, fmt.Errorf("project archive fence identity is invalid")
	}
	identity, err := projectquiescence.TargetLockIdentity(fence.Target)
	if err != nil {
		return true, err
	}
	want := kind + ":" + targetIdentity
	if identity != want {
		return true, fmt.Errorf("project archive fence target does not match its location")
	}
	return true, nil
}

func (s Store) ReadProjectArchiveFence(kind, targetIdentity string) ([]byte, SecureFileIdentity, error) {
	location, err := s.projectArchiveFenceLocation(kind, targetIdentity)
	if err != nil {
		return nil, SecureFileIdentity{}, err
	}
	defer location.directory.close()
	return s.readSecureBoundedFile(location, projectquiescence.MaxFenceBytes)
}

func (s Store) PublishProjectArchiveFence(kind, targetIdentity string, raw []byte) error {
	location, err := s.projectArchiveFenceLocation(kind, targetIdentity)
	if err != nil {
		return err
	}
	defer location.directory.close()
	return s.writeSecureDurableFile(location, raw, projectquiescence.MaxFenceBytes)
}

func (s Store) ReadProjectArchiveReceipt(operationID string) ([]byte, SecureFileIdentity, error) {
	location, err := s.projectArchiveReceiptLocation(operationID)
	if err != nil {
		return nil, SecureFileIdentity{}, err
	}
	defer location.directory.close()
	return s.readSecureBoundedFile(location, projectquiescence.MaxReceiptBytes)
}

func (s Store) PublishProjectArchiveReceipt(operationID string, raw []byte) error {
	location, err := s.projectArchiveReceiptLocation(operationID)
	if err != nil {
		return err
	}
	defer location.directory.close()
	return s.writeSecureDurableFile(location, raw, projectquiescence.MaxReceiptBytes)
}

type secureDirectory struct {
	fd         int
	root       string
	components []string
	rootStat   unix.Stat_t
	leafStat   unix.Stat_t
}

type projectArchiveFileLocation struct {
	directory *secureDirectory
	name      string
	path      string
}

func (s Store) projectArchiveFenceLocation(kind, targetIdentity string) (projectArchiveFileLocation, error) {
	if kind != projectquiescence.TargetKindWatchedRoot && kind != projectquiescence.TargetKindService {
		return projectArchiveFileLocation{}, fmt.Errorf("unsupported project archive fence kind")
	}
	name, err := boundedStateFilename(kind + "\x00" + targetIdentity)
	if err != nil {
		return projectArchiveFileLocation{}, err
	}
	directory, err := s.openProjectArchiveDirectory("fences")
	if err != nil {
		return projectArchiveFileLocation{}, err
	}
	filename := "target-" + name + ".json"
	return projectArchiveFileLocation{directory: directory, name: filename, path: filepath.Join(s.DataDir, "project-archive-quiescence", "fences", filename)}, nil
}

func (s Store) projectArchiveReceiptLocation(operationID string) (projectArchiveFileLocation, error) {
	name, err := boundedStateFilename(operationID)
	if err != nil {
		return projectArchiveFileLocation{}, err
	}
	directory, err := s.openProjectArchiveDirectory("receipts")
	if err != nil {
		return projectArchiveFileLocation{}, err
	}
	filename := "operation-" + name + ".json"
	return projectArchiveFileLocation{directory: directory, name: filename, path: filepath.Join(s.DataDir, "project-archive-quiescence", "receipts", filename)}, nil
}

func (s Store) acquireProjectArchiveLock(ctx context.Context, identity string) (func() error, error) {
	name, err := boundedStateFilename(identity)
	if err != nil {
		return nil, err
	}
	directory, err := s.openProjectArchiveDirectory("locks")
	if err != nil {
		return nil, err
	}
	filename := "lock-" + name + ".lock"
	path := filepath.Join(s.DataDir, "project-archive-quiescence", "locks", filename)
	fd, err := unix.Openat(directory.fd, filename, unix.O_CREAT|unix.O_EXCL|unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
	created := err == nil
	if errors.Is(err, unix.EEXIST) {
		fd, err = unix.Openat(directory.fd, filename, unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	}
	if err != nil {
		_ = directory.close()
		return nil, err
	}
	file := os.NewFile(uintptr(fd), path)
	if file == nil {
		_ = unix.Close(fd)
		_ = directory.close()
		return nil, fmt.Errorf("open project archive lock")
	}
	if created {
		if err := unix.Fchmod(fd, 0o600); err != nil {
			_ = file.Close()
			_ = directory.close()
			return nil, err
		}
	}
	lockedStat, err := secureRegularFileStat(fd, 0, false)
	if err != nil {
		_ = file.Close()
		_ = directory.close()
		return nil, err
	}
	if err := directory.verify(); err != nil {
		_ = file.Close()
		_ = directory.close()
		return nil, err
	}
	for {
		if err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err == nil {
			break
		} else if err != unix.EWOULDBLOCK && err != unix.EAGAIN {
			_ = file.Close()
			_ = directory.close()
			return nil, err
		}
		select {
		case <-ctx.Done():
			_ = file.Close()
			_ = directory.close()
			return nil, ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
	var pathStat unix.Stat_t
	if err := unix.Fstatat(directory.fd, filename, &pathStat, unix.AT_SYMLINK_NOFOLLOW); err != nil || !sameUnixFile(lockedStat, pathStat) {
		_ = unix.Flock(fd, unix.LOCK_UN)
		_ = file.Close()
		_ = directory.close()
		return nil, fmt.Errorf("project archive lock changed while waiting")
	}
	if err := directory.verify(); err != nil {
		_ = unix.Flock(fd, unix.LOCK_UN)
		_ = file.Close()
		_ = directory.close()
		return nil, err
	}
	var once sync.Once
	var releaseErr error
	return func() error {
		once.Do(func() {
			if err := unix.Flock(fd, unix.LOCK_UN); err != nil {
				releaseErr = err
			}
			if err := file.Close(); releaseErr == nil {
				releaseErr = err
			}
			if err := directory.close(); releaseErr == nil {
				releaseErr = err
			}
		})
		return releaseErr
	}, nil
}

func boundedStateFilename(identity string) (string, error) {
	identity = strings.TrimSpace(identity)
	if identity == "" || len(identity) > 1024 || strings.ContainsAny(identity, "\r\n") {
		return "", fmt.Errorf("project archive state identity is invalid")
	}
	sum := sha256.Sum256([]byte(identity))
	return fmt.Sprintf("%x", sum[:]), nil
}

func (s Store) openProjectArchiveDirectory(leaf string) (*secureDirectory, error) {
	if leaf != "locks" && leaf != "fences" && leaf != "receipts" {
		return nil, fmt.Errorf("unsupported project archive state directory")
	}
	rootFD, root, rootStat, err := openDirectoryPathNoFollow(s.DataDir, true)
	if err != nil {
		return nil, err
	}
	components := []string{"project-archive-quiescence", leaf}
	parentFD := rootFD
	for index, component := range components {
		if err := unix.Mkdirat(parentFD, component, 0o700); err != nil && !errors.Is(err, unix.EEXIST) {
			if parentFD != rootFD {
				_ = unix.Close(parentFD)
			}
			_ = unix.Close(rootFD)
			return nil, err
		}
		childFD, err := unix.Openat(parentFD, component, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
		if err != nil {
			if parentFD != rootFD {
				_ = unix.Close(parentFD)
			}
			_ = unix.Close(rootFD)
			return nil, err
		}
		if err := unix.Fchmod(childFD, 0o700); err != nil {
			_ = unix.Close(childFD)
			if parentFD != rootFD {
				_ = unix.Close(parentFD)
			}
			_ = unix.Close(rootFD)
			return nil, err
		}
		var childStat unix.Stat_t
		if err := unix.Fstat(childFD, &childStat); err != nil || uint32(childStat.Mode)&unix.S_IFMT != unix.S_IFDIR || uint32(childStat.Mode)&0o777 != 0o700 {
			_ = unix.Close(childFD)
			if parentFD != rootFD {
				_ = unix.Close(parentFD)
			}
			_ = unix.Close(rootFD)
			return nil, fmt.Errorf("project archive state directory is invalid")
		}
		if parentFD != rootFD {
			_ = unix.Close(parentFD)
		}
		parentFD = childFD
		if index == len(components)-1 {
			directory := &secureDirectory{fd: childFD, root: root, components: components, rootStat: rootStat, leafStat: childStat}
			_ = unix.Close(rootFD)
			if err := directory.verify(); err != nil {
				_ = directory.close()
				return nil, err
			}
			return directory, nil
		}
	}
	_ = unix.Close(rootFD)
	return nil, fmt.Errorf("project archive state directory could not be opened")
}

func (directory *secureDirectory) verify() error {
	rootFD, _, rootStat, err := openDirectoryPathNoFollow(directory.root, false)
	if err != nil {
		return err
	}
	defer unix.Close(rootFD)
	if !sameUnixDirectory(directory.rootStat, rootStat) {
		return fmt.Errorf("project archive state root identity changed")
	}
	parentFD := rootFD
	for index, component := range directory.components {
		childFD, err := unix.Openat(parentFD, component, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
		if err != nil {
			if parentFD != rootFD {
				_ = unix.Close(parentFD)
			}
			return err
		}
		var childStat unix.Stat_t
		if err := unix.Fstat(childFD, &childStat); err != nil || uint32(childStat.Mode)&unix.S_IFMT != unix.S_IFDIR || uint32(childStat.Mode)&0o777 != 0o700 {
			_ = unix.Close(childFD)
			if parentFD != rootFD {
				_ = unix.Close(parentFD)
			}
			return fmt.Errorf("project archive state directory identity changed")
		}
		if parentFD != rootFD {
			_ = unix.Close(parentFD)
		}
		parentFD = childFD
		if index == len(directory.components)-1 {
			defer unix.Close(childFD)
			if !sameUnixDirectory(directory.leafStat, childStat) {
				return fmt.Errorf("project archive state directory identity changed")
			}
		}
	}
	return nil
}

func (directory *secureDirectory) close() error {
	if directory == nil || directory.fd < 0 {
		return nil
	}
	err := unix.Close(directory.fd)
	directory.fd = -1
	return err
}

func (s Store) readSecureBoundedFile(location projectArchiveFileLocation, limit int) ([]byte, SecureFileIdentity, error) {
	var pathBefore unix.Stat_t
	if err := unix.Fstatat(location.directory.fd, location.name, &pathBefore, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return nil, SecureFileIdentity{}, err
	}
	if _, err := validateSecureRegularStat(pathBefore, limit, true); err != nil {
		return nil, SecureFileIdentity{}, err
	}
	fd, err := unix.Openat(location.directory.fd, location.name, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, SecureFileIdentity{}, err
	}
	file := os.NewFile(uintptr(fd), location.path)
	if file == nil {
		_ = unix.Close(fd)
		return nil, SecureFileIdentity{}, fmt.Errorf("open project archive state file")
	}
	defer file.Close()
	openedStat, err := secureRegularFileStat(fd, limit, true)
	if err != nil || !sameUnixFile(pathBefore, openedStat) {
		return nil, SecureFileIdentity{}, fmt.Errorf("project archive state file changed during open")
	}
	if s.afterProjectArchiveReadOpen != nil {
		s.afterProjectArchiveReadOpen(location.path)
	}
	raw, err := io.ReadAll(io.LimitReader(file, int64(limit)+1))
	if err != nil {
		return nil, SecureFileIdentity{}, err
	}
	var afterRead unix.Stat_t
	if err := unix.Fstat(fd, &afterRead); err != nil || !sameUnixFile(openedStat, afterRead) || len(raw) == 0 || len(raw) > limit || int64(len(raw)) != afterRead.Size {
		return nil, SecureFileIdentity{}, fmt.Errorf("project archive state file changed during read")
	}
	var pathAfter unix.Stat_t
	if err := unix.Fstatat(location.directory.fd, location.name, &pathAfter, unix.AT_SYMLINK_NOFOLLOW); err != nil || !sameUnixFile(afterRead, pathAfter) {
		return nil, SecureFileIdentity{}, fmt.Errorf("project archive state file path changed during read")
	}
	if err := location.directory.verify(); err != nil {
		return nil, SecureFileIdentity{}, err
	}
	identity, _ := validateSecureRegularStat(afterRead, limit, true)
	identity.Path = location.path
	return raw, identity, nil
}

func secureRegularFileStat(fd, limit int, requireNonEmpty bool) (unix.Stat_t, error) {
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return unix.Stat_t{}, err
	}
	if _, err := validateSecureRegularStat(stat, limit, requireNonEmpty); err != nil {
		return unix.Stat_t{}, err
	}
	return stat, nil
}

func validateSecureRegularStat(stat unix.Stat_t, limit int, requireNonEmpty bool) (SecureFileIdentity, error) {
	if uint32(stat.Mode)&unix.S_IFMT != unix.S_IFREG || uint32(stat.Mode)&0o777 != 0o600 || uint64(stat.Nlink) != 1 || (requireNonEmpty && stat.Size == 0) || (limit > 0 && stat.Size > int64(limit)) {
		return SecureFileIdentity{}, fmt.Errorf("project archive state file is not a regular single-link mode-0600 bounded file")
	}
	return SecureFileIdentity{Mode: fs.FileMode(uint32(stat.Mode) & 0o777), Size: stat.Size, Dev: uint64(stat.Dev), Ino: uint64(stat.Ino), Nlink: uint64(stat.Nlink)}, nil
}

func sameUnixDirectory(left, right unix.Stat_t) bool {
	return uint64(left.Dev) == uint64(right.Dev) && uint64(left.Ino) == uint64(right.Ino) && uint32(right.Mode)&unix.S_IFMT == unix.S_IFDIR
}

func sameUnixFile(left, right unix.Stat_t) bool {
	return uint64(left.Dev) == uint64(right.Dev) && uint64(left.Ino) == uint64(right.Ino) && uint64(left.Nlink) == uint64(right.Nlink) && left.Size == right.Size && uint32(left.Mode) == uint32(right.Mode)
}

func openDirectoryPathNoFollow(path string, create bool) (int, string, unix.Stat_t, error) {
	if strings.TrimSpace(path) == "" {
		return -1, "", unix.Stat_t{}, fmt.Errorf("project archive state root is invalid")
	}
	absolute, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return -1, "", unix.Stat_t{}, err
	}
	absolute = canonicalDarwinSystemPath(absolute)
	if absolute == string(filepath.Separator) {
		return -1, "", unix.Stat_t{}, fmt.Errorf("project archive state root is invalid")
	}
	fd, err := unix.Open(string(filepath.Separator), unix.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return -1, "", unix.Stat_t{}, err
	}
	components := strings.Split(strings.TrimPrefix(absolute, string(filepath.Separator)), string(filepath.Separator))
	for _, component := range components {
		if component == "" || component == "." || component == ".." {
			_ = unix.Close(fd)
			return -1, "", unix.Stat_t{}, fmt.Errorf("project archive state root is invalid")
		}
		if create {
			if err := unix.Mkdirat(fd, component, 0o700); err != nil && !errors.Is(err, unix.EEXIST) {
				_ = unix.Close(fd)
				return -1, "", unix.Stat_t{}, err
			}
		}
		nextFD, err := unix.Openat(fd, component, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
		_ = unix.Close(fd)
		if err != nil {
			return -1, "", unix.Stat_t{}, err
		}
		fd = nextFD
	}
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil || uint32(stat.Mode)&unix.S_IFMT != unix.S_IFDIR {
		_ = unix.Close(fd)
		return -1, "", unix.Stat_t{}, fmt.Errorf("project archive state root is not a real directory")
	}
	return fd, absolute, stat, nil
}

func canonicalDarwinSystemPath(path string) string {
	if goruntime.GOOS != "darwin" {
		return path
	}
	for alias, canonical := range map[string]string{
		"/etc": "/private/etc",
		"/tmp": "/private/tmp",
		"/var": "/private/var",
	} {
		if path == alias {
			return canonical
		}
		if strings.HasPrefix(path, alias+string(filepath.Separator)) {
			return canonical + strings.TrimPrefix(path, alias)
		}
	}
	return path
}

func (s Store) writeSecureDurableFile(location projectArchiveFileLocation, raw []byte, limit int) error {
	if len(raw) == 0 || len(raw) > limit {
		return fmt.Errorf("project archive state payload is empty or oversized")
	}
	tmpFD := -1
	var tmpName string
	for attempt := 0; attempt < 128; attempt++ {
		tmpName = fmt.Sprintf(".publish-%016x", rand.Uint64())
		var err error
		tmpFD, err = unix.Openat(location.directory.fd, tmpName, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
		if err == nil {
			break
		}
		if !errors.Is(err, unix.EEXIST) {
			return err
		}
	}
	if tmpFD < 0 {
		return fmt.Errorf("could not allocate project archive publication inode")
	}
	tmp := os.NewFile(uintptr(tmpFD), filepath.Join(filepath.Dir(location.path), tmpName))
	if tmp == nil {
		_ = unix.Close(tmpFD)
		_ = unix.Unlinkat(location.directory.fd, tmpName, 0)
		return fmt.Errorf("open project archive publication inode")
	}
	defer func() {
		_ = tmp.Close()
		_ = unix.Unlinkat(location.directory.fd, tmpName, 0)
	}()
	if err := unix.Fchmod(tmpFD, 0o600); err != nil {
		return err
	}
	if _, err := tmp.Write(raw); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	tmpStat, err := secureRegularFileStat(tmpFD, limit, true)
	if err != nil || tmpStat.Size != int64(len(raw)) {
		return fmt.Errorf("project archive publication inode is invalid")
	}
	if s.beforeProjectArchivePublish != nil {
		s.beforeProjectArchivePublish(location.path)
	}
	if err := location.directory.verify(); err != nil {
		return err
	}
	var tmpPathStat unix.Stat_t
	if err := unix.Fstatat(location.directory.fd, tmpName, &tmpPathStat, unix.AT_SYMLINK_NOFOLLOW); err != nil || !sameUnixFile(tmpStat, tmpPathStat) {
		return fmt.Errorf("project archive publication inode path changed")
	}
	if err := unix.Linkat(location.directory.fd, tmpName, location.directory.fd, location.name, 0); err != nil {
		if errors.Is(err, unix.EEXIST) {
			return fs.ErrExist
		}
		return err
	}
	var linkedStat unix.Stat_t
	if err := unix.Fstatat(location.directory.fd, location.name, &linkedStat, unix.AT_SYMLINK_NOFOLLOW); err != nil || uint64(linkedStat.Dev) != uint64(tmpStat.Dev) || uint64(linkedStat.Ino) != uint64(tmpStat.Ino) || uint64(linkedStat.Nlink) != 2 {
		return rollbackProjectArchivePublication(location, tmpStat, fmt.Errorf("project archive publication link identity is invalid"))
	}
	if err := unix.Unlinkat(location.directory.fd, tmpName, 0); err != nil {
		return rollbackProjectArchivePublication(location, tmpStat, err)
	}
	tmpName = ""
	var publishedStat unix.Stat_t
	if err := unix.Fstat(tmpFD, &publishedStat); err != nil || !sameUnixFile(tmpStat, publishedStat) {
		return rollbackProjectArchivePublication(location, tmpStat, fmt.Errorf("project archive published inode identity is invalid"))
	}
	if err := location.directory.verify(); err != nil {
		return rollbackProjectArchivePublication(location, tmpStat, err)
	}
	if err := unix.Fsync(location.directory.fd); err != nil {
		return fmt.Errorf("%w: %v", ErrProjectArchivePublicationCommitted, err)
	}
	if err := location.directory.verify(); err != nil {
		return rollbackProjectArchivePublication(location, tmpStat, err)
	}
	return nil
}

func rollbackProjectArchivePublication(location projectArchiveFileLocation, published unix.Stat_t, cause error) error {
	var current unix.Stat_t
	if err := unix.Fstatat(location.directory.fd, location.name, &current, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return fmt.Errorf("%w: %v; destination inspection failed: %v", ErrProjectArchivePublicationIndeterminate, cause, err)
	}
	if uint64(current.Dev) != uint64(published.Dev) || uint64(current.Ino) != uint64(published.Ino) {
		return fmt.Errorf("%w: %v; destination identity changed", ErrProjectArchivePublicationIndeterminate, cause)
	}
	if err := unix.Unlinkat(location.directory.fd, location.name, 0); err != nil {
		return fmt.Errorf("%w: %v; rollback failed: %v", ErrProjectArchivePublicationIndeterminate, cause, err)
	}
	if err := unix.Fsync(location.directory.fd); err != nil {
		return fmt.Errorf("%w: %v; rollback durability failed: %v", ErrProjectArchivePublicationIndeterminate, cause, err)
	}
	return cause
}

func decodeCanonicalSecureJSON(raw []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return fmt.Errorf("project archive state JSON must contain one object")
	}
	canonical, err := json.Marshal(target)
	if err != nil || !bytes.Equal(raw, canonical) {
		return fmt.Errorf("project archive state JSON is not canonical")
	}
	return nil
}

func (s Store) DeleteInstance(workerKey string) error {
	if strings.TrimSpace(workerKey) == "" {
		return errors.New("worker key is required")
	}
	err := os.Remove(s.instancePath(workerKey))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

func (s Store) LoadCheckpoint(workerKey string) (WorkerCheckpoint, error) {
	var checkpoint WorkerCheckpoint
	err := readJSONFile(s.checkpointPath(workerKey), &checkpoint)
	return checkpoint, err
}

func (s Store) SaveCheckpoint(checkpoint WorkerCheckpoint) error {
	if err := s.Ensure(); err != nil {
		return err
	}
	if strings.TrimSpace(checkpoint.WorkerKey) == "" {
		return errors.New("worker key is required")
	}
	checkpoint.UpdatedAt = time.Now().UTC()
	return writeJSONFile(s.checkpointPath(checkpoint.WorkerKey), checkpoint, 0o600)
}

func (s Store) AppendRun(run WorkerRun) error {
	if err := s.Ensure(); err != nil {
		return err
	}
	if strings.TrimSpace(run.WorkerKey) == "" {
		return errors.New("worker key is required")
	}
	if strings.TrimSpace(run.LocalRunID) == "" {
		run.LocalRunID = NewLocalRunID()
	}
	dir := filepath.Join(s.runsDir(), safeFileSegment(run.WorkerKey))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return writeJSONFile(filepath.Join(dir, run.LocalRunID+".json"), run, 0o600)
}

func (s Store) ListRuns(workerKey string, limit int) ([]WorkerRun, error) {
	if err := s.Ensure(); err != nil {
		return nil, err
	}
	dir := filepath.Join(s.runsDir(), safeFileSegment(workerKey))
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return []WorkerRun{}, nil
	}
	if err != nil {
		return nil, err
	}
	runs := make([]WorkerRun, 0, len(entries))
	for _, entry := range entries {
		if !entry.Type().IsRegular() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		var run WorkerRun
		if err := readJSONFile(filepath.Join(dir, entry.Name()), &run); err != nil {
			return nil, err
		}
		runs = append(runs, run)
	}
	sort.Slice(runs, func(i, j int) bool {
		return runs[i].StartedAt.After(runs[j].StartedAt)
	})
	if limit > 0 && len(runs) > limit {
		runs = runs[:limit]
	}
	return runs, nil
}

func (s Store) LoadHealth(workerKey string) (WorkerHealth, error) {
	var health WorkerHealth
	err := readJSONFile(s.healthPath(workerKey), &health)
	return health, err
}

func (s Store) SaveHealth(health WorkerHealth) error {
	if err := s.Ensure(); err != nil {
		return err
	}
	if strings.TrimSpace(health.WorkerKey) == "" {
		return errors.New("worker key is required")
	}
	health.UpdatedAt = time.Now().UTC()
	return writeJSONFile(s.healthPath(health.WorkerKey), health, 0o600)
}

func (s Store) QueueOutbox(item OutboxItem) (OutboxItem, error) {
	if err := s.Ensure(); err != nil {
		return OutboxItem{}, err
	}
	if strings.TrimSpace(item.Kind) == "" {
		return OutboxItem{}, errors.New("outbox kind is required")
	}
	if strings.TrimSpace(item.LocalOutboxID) == "" {
		item.LocalOutboxID = NewLocalOutboxID()
	}
	if strings.TrimSpace(item.Status) == "" {
		item.Status = OutboxStatusPending
	}
	if item.CreatedAt.IsZero() {
		item.CreatedAt = time.Now().UTC()
	}
	if err := writeJSONFile(s.outboxPath(item.Status, item.LocalOutboxID), item, 0o600); err != nil {
		return OutboxItem{}, err
	}
	return item, nil
}

func (s Store) UpdateOutboxItem(item OutboxItem, previousStatus string) (string, error) {
	if err := s.Ensure(); err != nil {
		return "", err
	}
	if strings.TrimSpace(item.LocalOutboxID) == "" {
		return "", errors.New("local outbox id is required")
	}
	if strings.TrimSpace(item.Status) == "" {
		return "", errors.New("outbox status is required")
	}
	if strings.TrimSpace(previousStatus) != "" && previousStatus != item.Status {
		_ = os.Remove(s.outboxPath(previousStatus, item.LocalOutboxID))
	}
	path := s.outboxPath(item.Status, item.LocalOutboxID)
	return path, writeJSONFile(path, item, 0o600)
}

func (s Store) ListDueOutbox(now time.Time, limit int) ([]OutboxItem, error) {
	items, err := s.ListOutbox([]string{OutboxStatusPending, OutboxStatusFailed}, 0)
	if err != nil {
		return nil, err
	}
	due := make([]OutboxItem, 0, len(items))
	for _, item := range items {
		if item.NextAttemptAt == nil || !item.NextAttemptAt.After(now) {
			due = append(due, item)
		}
	}
	if limit > 0 && len(due) > limit {
		due = due[:limit]
	}
	return due, nil
}

func (s Store) MarkOutboxInflight(item OutboxItem) (OutboxItem, string, error) {
	previousStatus := item.Status
	now := time.Now().UTC()
	item.Status = OutboxStatusInflight
	item.AttemptCount++
	item.LastAttemptAt = &now
	path, err := s.UpdateOutboxItem(item, previousStatus)
	return item, path, err
}

func (s Store) MarkOutboxDone(item OutboxItem, result json.RawMessage) (OutboxItem, string, error) {
	previousStatus := item.Status
	item.Status = OutboxStatusDone
	item.ResultJSON = result
	item.NextAttemptAt = nil
	item.LastErrorCode = ""
	item.LastErrorMessage = ""
	path, err := s.UpdateOutboxItem(item, previousStatus)
	return item, path, err
}

func (s Store) MarkOutboxFailed(item OutboxItem, code, message string, nextAttemptAt *time.Time) (OutboxItem, string, error) {
	previousStatus := item.Status
	item.Status = OutboxStatusFailed
	item.LastErrorCode = strings.TrimSpace(code)
	item.LastErrorMessage = strings.TrimSpace(message)
	item.NextAttemptAt = nextAttemptAt
	path, err := s.UpdateOutboxItem(item, previousStatus)
	return item, path, err
}

func (s Store) MarkOutboxManualAction(item OutboxItem, code, message string) (OutboxItem, string, error) {
	previousStatus := item.Status
	item.Status = OutboxStatusManualAction
	item.LastErrorCode = strings.TrimSpace(code)
	item.LastErrorMessage = strings.TrimSpace(message)
	item.NextAttemptAt = nil
	path, err := s.UpdateOutboxItem(item, previousStatus)
	return item, path, err
}

func (s Store) RequeueStaleInflight(staleAfter time.Duration) (int, error) {
	items, err := s.ListOutbox([]string{OutboxStatusInflight}, 0)
	if err != nil {
		return 0, err
	}
	cutoff := time.Now().UTC().Add(-staleAfter)
	requeued := 0
	for _, item := range items {
		if item.LastAttemptAt != nil && item.LastAttemptAt.After(cutoff) {
			continue
		}
		previousStatus := item.Status
		item.Status = OutboxStatusPending
		now := time.Now().UTC()
		item.NextAttemptAt = &now
		if _, err := s.UpdateOutboxItem(item, previousStatus); err != nil {
			return requeued, err
		}
		requeued++
	}
	return requeued, nil
}

func (s Store) ListOutbox(statuses []string, limit int) ([]OutboxItem, error) {
	if err := s.Ensure(); err != nil {
		return nil, err
	}
	if len(statuses) == 0 {
		statuses = []string{OutboxStatusPending, OutboxStatusInflight, OutboxStatusFailed, OutboxStatusManualAction, OutboxStatusDone}
	}
	items := []OutboxItem{}
	for _, status := range statuses {
		status = strings.TrimSpace(status)
		if status == "" {
			continue
		}
		dir := s.outboxStatusDir(status)
		entries, err := os.ReadDir(dir)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		for _, entry := range entries {
			if !entry.Type().IsRegular() || !strings.HasSuffix(entry.Name(), ".json") {
				continue
			}
			var item OutboxItem
			if err := readJSONFile(filepath.Join(dir, entry.Name()), &item); err != nil {
				return nil, err
			}
			items = append(items, item)
		}
	}
	sort.Slice(items, func(i, j int) bool {
		return items[i].CreatedAt.Before(items[j].CreatedAt)
	})
	if limit > 0 && len(items) > limit {
		items = items[:limit]
	}
	return items, nil
}

func (s Store) OutboxSummary() (OutboxSummary, error) {
	if err := s.Ensure(); err != nil {
		return OutboxSummary{}, err
	}
	summary := OutboxSummary{
		Counts: map[string]int{
			OutboxStatusPending:      0,
			OutboxStatusInflight:     0,
			OutboxStatusDone:         0,
			OutboxStatusFailed:       0,
			OutboxStatusManualAction: 0,
		},
	}
	for status := range summary.Counts {
		count, oldest, nextDue, pendingBytes, err := s.outboxStatusSummary(status)
		if err != nil {
			return OutboxSummary{}, err
		}
		summary.Counts[status] = count
		if status == OutboxStatusPending {
			summary.OldestPendingAt = oldest
			summary.NextDueAt = nextDue
			summary.TotalPendingBytes = pendingBytes
		}
	}
	return summary, nil
}

func (s Store) InboxSummary() (InboxSummary, error) {
	if err := s.Ensure(); err != nil {
		return InboxSummary{}, err
	}
	counts := map[string]int{
		OutboxStatusPending: 0,
		OutboxStatusDone:    0,
		OutboxStatusFailed:  0,
	}
	for status := range counts {
		count, err := countJSONFiles(filepath.Join(s.DataDir, "inbox", status))
		if err != nil {
			return InboxSummary{}, err
		}
		counts[status] = count
	}
	legacy, err := countLegacyJSONFiles(filepath.Join(s.DataDir, "inbox"))
	if err != nil {
		return InboxSummary{}, err
	}
	return InboxSummary{Counts: counts, LegacyRecords: legacy}, nil
}

func (s Store) SaveInboxItem(item InboxItem) (string, error) {
	if err := s.Ensure(); err != nil {
		return "", err
	}
	if strings.TrimSpace(item.MessageID) == "" {
		return "", errors.New("inbox message id is required")
	}
	if strings.TrimSpace(item.LocalInboxID) == "" {
		item.LocalInboxID = item.MessageID
	}
	if strings.TrimSpace(item.Status) == "" {
		item.Status = OutboxStatusPending
	}
	now := time.Now().UTC()
	if item.CreatedAt.IsZero() {
		item.CreatedAt = now
	}
	item.UpdatedAt = now
	path := s.inboxPath(item.Status, item.LocalInboxID)
	return path, writeJSONFile(path, item, 0o600)
}

func (s Store) MoveInboxItem(item InboxItem, previousStatus, nextStatus string) (string, error) {
	item.Status = nextStatus
	if strings.TrimSpace(item.LocalInboxID) == "" {
		item.LocalInboxID = item.MessageID
	}
	if strings.TrimSpace(previousStatus) != "" && previousStatus != nextStatus {
		_ = os.Remove(s.inboxPath(previousStatus, item.LocalInboxID))
	}
	return s.SaveInboxItem(item)
}

func (s Store) SaveLatestSummary(summary LocalSummary) error {
	if err := s.Ensure(); err != nil {
		return err
	}
	if summary.GeneratedAt.IsZero() {
		summary.GeneratedAt = time.Now().UTC()
	}
	return writeJSONFile(s.latestSummaryPath(), summary, 0o600)
}

func (s Store) LoadLatestSummary() (LocalSummary, error) {
	var summary LocalSummary
	err := readJSONFile(s.latestSummaryPath(), &summary)
	return summary, err
}

func (s Store) Status() (Status, error) {
	if err := s.Ensure(); err != nil {
		return Status{}, err
	}
	instances, err := s.LoadInstances()
	if err != nil {
		return Status{}, err
	}
	workerStatuses := make([]WorkerStatus, 0, len(instances))
	for _, instance := range instances {
		status := WorkerStatus{Instance: instance}
		if health, err := s.LoadHealth(instance.WorkerKey); err == nil {
			status.Health = &health
		} else if !errors.Is(err, fs.ErrNotExist) {
			return Status{}, err
		}
		if checkpoint, err := s.LoadCheckpoint(instance.WorkerKey); err == nil {
			status.Checkpoint = &checkpoint
		} else if !errors.Is(err, fs.ErrNotExist) {
			return Status{}, err
		}
		workerStatuses = append(workerStatuses, status)
	}
	summary, err := s.BuildLocalSummary()
	if err != nil {
		return Status{}, err
	}
	outbox, err := s.OutboxSummary()
	if err != nil {
		return Status{}, err
	}
	inbox, err := s.InboxSummary()
	if err != nil {
		return Status{}, err
	}
	return Status{
		DataDir: s.DataDir,
		Workers: workerStatuses,
		Summary: summary,
		Outbox:  outbox,
		Inbox:   inbox,
	}, nil
}

func (s Store) outboxStatusSummary(status string) (count int, oldest *time.Time, nextDue *time.Time, pendingBytes int64, err error) {
	dir := s.outboxStatusDir(status)
	entries, readErr := os.ReadDir(dir)
	if errors.Is(readErr, fs.ErrNotExist) {
		return 0, nil, nil, 0, nil
	}
	if readErr != nil {
		return 0, nil, nil, 0, readErr
	}
	for _, entry := range entries {
		if !entry.Type().IsRegular() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		count++
		path := filepath.Join(dir, entry.Name())
		info, statErr := entry.Info()
		if statErr == nil && status == OutboxStatusPending {
			pendingBytes += info.Size()
		}
		var item OutboxItem
		if readErr := readJSONFile(path, &item); readErr != nil {
			return 0, nil, nil, 0, readErr
		}
		if status == OutboxStatusPending {
			if oldest == nil || item.CreatedAt.Before(*oldest) {
				created := item.CreatedAt
				oldest = &created
			}
			if item.NextAttemptAt != nil && (nextDue == nil || item.NextAttemptAt.Before(*nextDue)) {
				due := *item.NextAttemptAt
				nextDue = &due
			}
		}
	}
	return count, oldest, nextDue, pendingBytes, nil
}

func (s Store) instancesDir() string {
	return filepath.Join(s.DataDir, "workers", "instances")
}

func (s Store) checkpointsDir() string {
	return filepath.Join(s.DataDir, "workers", "checkpoints")
}

func (s Store) runsDir() string {
	return filepath.Join(s.DataDir, "workers", "runs")
}

func (s Store) healthDir() string {
	return filepath.Join(s.DataDir, "workers", "health")
}

func (s Store) instancePath(workerKey string) string {
	return filepath.Join(s.instancesDir(), safeFileSegment(workerKey)+".json")
}

func (s Store) checkpointPath(workerKey string) string {
	return filepath.Join(s.checkpointsDir(), safeFileSegment(workerKey)+".json")
}

func (s Store) healthPath(workerKey string) string {
	return filepath.Join(s.healthDir(), safeFileSegment(workerKey)+".json")
}

func (s Store) outboxStatusDir(status string) string {
	return filepath.Join(s.DataDir, "outbox", safeFileSegment(status))
}

func (s Store) outboxPath(status, localOutboxID string) string {
	return filepath.Join(s.outboxStatusDir(status), safeFileSegment(localOutboxID)+".json")
}

func (s Store) inboxStatusDir(status string) string {
	return filepath.Join(s.DataDir, "inbox", safeFileSegment(status))
}

func (s Store) inboxPath(status, localInboxID string) string {
	return filepath.Join(s.inboxStatusDir(status), safeFileSegment(localInboxID)+".json")
}

func (s Store) latestSummaryPath() string {
	return filepath.Join(s.DataDir, "summaries", "latest.json")
}

func countJSONFiles(dir string) (int, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	count := 0
	for _, entry := range entries {
		if entry.Type().IsRegular() && strings.HasSuffix(entry.Name(), ".json") {
			count++
		}
	}
	return count, nil
}

func countLegacyJSONFiles(dir string) (int, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	count := 0
	for _, entry := range entries {
		if entry.Type().IsRegular() && strings.HasSuffix(entry.Name(), ".json") {
			count++
		}
	}
	return count, nil
}

func readJSONFile(path string, target any) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	return decoder.Decode(target)
}

func writeJSONFile(path string, value any, perm fs.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-"+filepath.Base(path)+"-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpPath)
		}
	}()
	if err := tmp.Chmod(perm); err != nil {
		_ = tmp.Close()
		return err
	}
	encoder := json.NewEncoder(tmp)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return err
	}
	if err := os.Chmod(path, perm); err != nil {
		return err
	}
	cleanup = false
	return nil
}

func safeFileSegment(value string) string {
	value = strings.TrimSpace(value)
	replacer := strings.NewReplacer("/", "_", "\\", "_", ":", "_", " ", "_")
	value = replacer.Replace(value)
	if value == "" {
		return "unknown"
	}
	return value
}

func NewLocalRunID() string {
	return fmt.Sprintf("local_worker_run_%s_%06d", time.Now().UTC().Format("20060102150405"), rand.Intn(1000000))
}

func NewLocalOutboxID() string {
	return fmt.Sprintf("local_outbox_%s_%06d", time.Now().UTC().Format("20060102150405"), rand.Intn(1000000))
}

func ComputeBackoff(attemptCount int) time.Duration {
	switch {
	case attemptCount <= 1:
		return 0
	case attemptCount == 2:
		return 5 * time.Second
	case attemptCount == 3:
		return 30 * time.Second
	case attemptCount == 4:
		return 2 * time.Minute
	default:
		return 5 * time.Minute
	}
}
