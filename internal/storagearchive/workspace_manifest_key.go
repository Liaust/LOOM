package storagearchive

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"golang.org/x/sys/unix"
)

const (
	WorkspaceArchiveManifestCredentialName = "workspace-archive-manifest-key"
	workspaceManifestEncodedKeyBytes       = 64
)

type systemdCredentialIdentity struct {
	device uint64
	inode  uint64
	mode   uint32
	uid    uint32
	gid    uint32
	links  uint64
	size   int64
}

type systemdManifestKeySnapshot struct {
	directory systemdCredentialIdentity
	file      systemdCredentialIdentity
	key       []byte
}

// SystemdWorkspaceManifestKeyProvider binds one manifest key ID to the fixed
// systemd credential name. The first valid read pins both file identity and key
// bytes; later reads must match exactly, so a running process never accepts
// mixed authentication evidence after replacement or metadata drift.
type SystemdWorkspaceManifestKeyProvider struct {
	directory string
	keyID     string
	mu        sync.Mutex
	pinned    *systemdManifestKeySnapshot
}

func NewSystemdWorkspaceManifestKeyProvider(credentialsDirectory, keyID string) (*SystemdWorkspaceManifestKeyProvider, error) {
	directory := strings.TrimSpace(credentialsDirectory)
	if directory == "" {
		return nil, fmt.Errorf("systemd credentials directory is unavailable")
	}
	if !filepath.IsAbs(directory) || filepath.Clean(directory) != directory {
		return nil, fmt.Errorf("systemd credentials directory must be one exact absolute path")
	}
	if !manifestKeyIDPattern.MatchString(strings.TrimSpace(keyID)) {
		return nil, fmt.Errorf("workspace archive manifest key ID is invalid")
	}
	return &SystemdWorkspaceManifestKeyProvider{directory: directory, keyID: strings.TrimSpace(keyID)}, nil
}

func (p *SystemdWorkspaceManifestKeyProvider) Lookup(ctx context.Context, keyID string) ([]byte, error) {
	if p == nil {
		return nil, fmt.Errorf("workspace archive manifest credential provider is unavailable")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if keyID != p.keyID {
		return nil, fmt.Errorf("workspace archive manifest key ID is not configured")
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	snapshot, err := p.readSnapshot()
	if err != nil {
		return nil, err
	}
	if p.pinned == nil {
		p.pinned = &snapshot
	} else if p.pinned.directory != snapshot.directory || p.pinned.file != snapshot.file || subtle.ConstantTimeCompare(p.pinned.key, snapshot.key) != 1 {
		zeroWorkspaceManifestKey(snapshot.key)
		return nil, fmt.Errorf("workspace archive manifest credential changed after process binding")
	} else {
		zeroWorkspaceManifestKey(snapshot.key)
	}
	return append([]byte(nil), p.pinned.key...), nil
}

func (p *SystemdWorkspaceManifestKeyProvider) readSnapshot() (systemdManifestKeySnapshot, error) {
	directoryFD, directoryIdentity, err := openAbsoluteDirectoryNoFollow(p.directory)
	if err != nil {
		return systemdManifestKeySnapshot{}, fmt.Errorf("open systemd credentials directory: %w", err)
	}
	defer unix.Close(directoryFD)

	fd, err := unix.Openat(directoryFD, WorkspaceArchiveManifestCredentialName, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return systemdManifestKeySnapshot{}, fmt.Errorf("open workspace archive manifest credential: %w", err)
	}
	file := os.NewFile(uintptr(fd), WorkspaceArchiveManifestCredentialName)
	if file == nil {
		_ = unix.Close(fd)
		return systemdManifestKeySnapshot{}, fmt.Errorf("open workspace archive manifest credential")
	}
	defer file.Close()

	before, err := credentialIdentity(fd)
	if err != nil {
		return systemdManifestKeySnapshot{}, fmt.Errorf("inspect workspace archive manifest credential: %w", err)
	}
	if err := validateManifestCredentialIdentity(before); err != nil {
		return systemdManifestKeySnapshot{}, err
	}
	if err := validateManifestCredentialAccess(fd, before); err != nil {
		return systemdManifestKeySnapshot{}, err
	}
	payload := make([]byte, workspaceManifestEncodedKeyBytes)
	if _, err := io.ReadFull(file, payload); err != nil {
		return systemdManifestKeySnapshot{}, fmt.Errorf("read workspace archive manifest credential: invalid length")
	}
	var trailing [1]byte
	if count, err := file.Read(trailing[:]); err != io.EOF || count != 0 {
		return systemdManifestKeySnapshot{}, fmt.Errorf("read workspace archive manifest credential: trailing data")
	}
	after, err := credentialIdentity(fd)
	if err != nil || before != after {
		return systemdManifestKeySnapshot{}, fmt.Errorf("workspace archive manifest credential changed while reading")
	}
	if err := validateManifestCredentialAccess(fd, after); err != nil {
		return systemdManifestKeySnapshot{}, err
	}
	var named unix.Stat_t
	if err := unix.Fstatat(directoryFD, WorkspaceArchiveManifestCredentialName, &named, unix.AT_SYMLINK_NOFOLLOW); err != nil || before != identityFromStat(named) {
		return systemdManifestKeySnapshot{}, fmt.Errorf("workspace archive manifest credential was replaced while reading")
	}
	for _, char := range payload {
		if !((char >= '0' && char <= '9') || (char >= 'a' && char <= 'f')) {
			return systemdManifestKeySnapshot{}, fmt.Errorf("workspace archive manifest credential must be lowercase hexadecimal")
		}
	}
	key := make([]byte, 32)
	if _, err := hex.Decode(key, payload); err != nil {
		return systemdManifestKeySnapshot{}, fmt.Errorf("decode workspace archive manifest credential")
	}
	if weakWorkspaceManifestKey(key) {
		zeroWorkspaceManifestKey(key)
		return systemdManifestKeySnapshot{}, fmt.Errorf("workspace archive manifest credential is weak")
	}
	return systemdManifestKeySnapshot{directory: directoryIdentity, file: before, key: key}, nil
}

func openAbsoluteDirectoryNoFollow(path string) (int, systemdCredentialIdentity, error) {
	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, systemdCredentialIdentity{}, err
	}
	for _, component := range strings.Split(strings.TrimPrefix(path, "/"), string(filepath.Separator)) {
		if component == "" {
			continue
		}
		next, openErr := unix.Openat(fd, component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		_ = unix.Close(fd)
		if openErr != nil {
			return -1, systemdCredentialIdentity{}, openErr
		}
		fd = next
	}
	identity, err := credentialIdentity(fd)
	if err != nil {
		_ = unix.Close(fd)
		return -1, systemdCredentialIdentity{}, err
	}
	return fd, identity, nil
}

func credentialIdentity(fd int) (systemdCredentialIdentity, error) {
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return systemdCredentialIdentity{}, err
	}
	return identityFromStat(stat), nil
}

func identityFromStat(stat unix.Stat_t) systemdCredentialIdentity {
	return systemdCredentialIdentity{device: uint64(stat.Dev), inode: stat.Ino, mode: uint32(stat.Mode), uid: stat.Uid, gid: stat.Gid, links: uint64(stat.Nlink), size: stat.Size}
}

func validateManifestCredentialIdentity(identity systemdCredentialIdentity) error {
	if identity.mode&unix.S_IFMT != unix.S_IFREG {
		return fmt.Errorf("workspace archive manifest credential must be a regular file")
	}
	if identity.mode&0o7777 != 0o400 && !(runtime.GOOS == "linux" && identity.mode&0o7777 == 0o440 && identity.uid == 0 && identity.gid == 0) {
		return fmt.Errorf("workspace archive manifest credential must be owner-only or use the exact systemd service-reader ACL")
	}
	if identity.links != 1 {
		return fmt.Errorf("workspace archive manifest credential must have exactly one link")
	}
	if identity.size != workspaceManifestEncodedKeyBytes {
		return fmt.Errorf("workspace archive manifest credential has invalid length")
	}
	return nil
}

func validateManifestCredentialAccess(fd int, identity systemdCredentialIdentity) error {
	if identity.mode&0o7777 == 0o400 {
		return nil
	}
	// systemd grants a non-root service access with a named-user ACL. Its
	// group-mode bits are the ACL mask, not permission for the owning group.
	var acl [45]byte
	size, err := unix.Fgetxattr(fd, "system.posix_acl_access", acl[:])
	if err != nil || size != 44 || !exactSystemdManifestReaderACL(identity, acl[:size], uint32(os.Geteuid())) {
		return fmt.Errorf("workspace archive manifest credential has no exact systemd service-reader ACL")
	}
	return nil
}

func exactSystemdManifestReaderACL(identity systemdCredentialIdentity, acl []byte, readerUID uint32) bool {
	if identity.uid != 0 || identity.gid != 0 || identity.mode != unix.S_IFREG|0o440 || readerUID == 0 || readerUID == ^uint32(0) {
		return false
	}
	var expected [44]byte
	binary.LittleEndian.PutUint32(expected[:4], 2)
	entries := [5]struct {
		tag, permission uint16
		id              uint32
	}{
		{1, 4, ^uint32(0)},  // owner: read
		{2, 4, readerUID},   // exact service user: read
		{4, 0, ^uint32(0)},  // owning group: none
		{16, 4, ^uint32(0)}, // named-user/group mask: read
		{32, 0, ^uint32(0)}, // other: none
	}
	for i, entry := range entries {
		offset := 4 + i*8
		binary.LittleEndian.PutUint16(expected[offset:], entry.tag)
		binary.LittleEndian.PutUint16(expected[offset+2:], entry.permission)
		binary.LittleEndian.PutUint32(expected[offset+4:], entry.id)
	}
	return bytes.Equal(acl, expected[:])
}

func weakWorkspaceManifestKey(key []byte) bool {
	if len(key) != 32 {
		return true
	}
	seen := make(map[byte]struct{}, len(key))
	for _, value := range key {
		seen[value] = struct{}{}
	}
	return len(seen) < 8
}

func zeroWorkspaceManifestKey(key []byte) {
	for index := range key {
		key[index] = 0
	}
}
