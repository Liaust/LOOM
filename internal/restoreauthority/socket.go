package restoreauthority

import (
	"fmt"
	"net"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

type SocketIdentity struct {
	OwnerUID uint32
	GroupGID uint32
	PeerUID  uint32
}

func ResolveUserID(value string) (uint32, error) {
	value = strings.TrimSpace(value)
	if numeric, err := strconv.ParseUint(value, 10, 32); err == nil {
		return uint32(numeric), nil
	}
	entry, err := user.Lookup(value)
	if err != nil {
		return 0, fmt.Errorf("resolve Unix user: %w", err)
	}
	numeric, err := strconv.ParseUint(entry.Uid, 10, 32)
	if err != nil {
		return 0, fmt.Errorf("parse Unix user ID: %w", err)
	}
	return uint32(numeric), nil
}

func ResolveGroupID(value string) (uint32, error) {
	value = strings.TrimSpace(value)
	if numeric, err := strconv.ParseUint(value, 10, 32); err == nil {
		return uint32(numeric), nil
	}
	entry, err := user.LookupGroup(value)
	if err != nil {
		return 0, fmt.Errorf("resolve Unix group: %w", err)
	}
	numeric, err := strconv.ParseUint(entry.Gid, 10, 32)
	if err != nil {
		return 0, fmt.Errorf("parse Unix group ID: %w", err)
	}
	return uint32(numeric), nil
}

func ValidateSocketPath(path string, ownerUID, groupGID uint32) (os.FileInfo, error) {
	path = filepath.Clean(strings.TrimSpace(path))
	if path == "." || !filepath.IsAbs(path) {
		return nil, fmt.Errorf("restore authority socket path must be absolute")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("inspect restore authority socket: %w", err)
	}
	if info.Mode()&os.ModeSocket == 0 || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o660 {
		return nil, fmt.Errorf("restore authority socket type or mode is invalid")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != ownerUID || stat.Gid != groupGID || stat.Nlink != 1 {
		return nil, fmt.Errorf("restore authority socket ownership or link count is invalid")
	}
	return info, nil
}

func validateListenerPath(listener *net.UnixListener, path string, ownerUID, groupGID uint32) error {
	if _, err := ValidateSocketPath(path, ownerUID, groupGID); err != nil {
		return err
	}
	address, ok := listener.Addr().(*net.UnixAddr)
	if !ok || address.Net != "unix" || filepath.Clean(address.Name) != filepath.Clean(path) {
		return fmt.Errorf("restore authority listener address is invalid")
	}
	return nil
}

func unixConnectionPeerUID(conn *net.UnixConn) (uint32, error) {
	raw, err := conn.SyscallConn()
	if err != nil {
		return 0, err
	}
	var uid uint32
	var peerErr error
	if err := raw.Control(func(fd uintptr) {
		uid, peerErr = peerUID(int(fd))
	}); err != nil {
		return 0, err
	}
	return uid, peerErr
}
