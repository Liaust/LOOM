//go:build linux

package hermesschedules

import (
	"net"
	"os/user"
	"strconv"

	"golang.org/x/sys/unix"
)

func peerUID(fd int) (uint32, error) {
	credential, err := unix.GetsockoptUcred(fd, unix.SOL_SOCKET, unix.SO_PEERCRED)
	if err != nil {
		return 0, ErrUnavailable
	}
	return credential.Uid, nil
}

// Accepted fd0 preserves the connecting loom process credentials. The bound
// account is resolved locally; no caller-provided identity is accepted.
func requireCallerPeer(fd int, account string) error {
	owner, err := user.Lookup(account)
	if err != nil {
		return ErrSource
	}
	uid, err := strconv.ParseUint(owner.Uid, 10, 32)
	if err != nil || uid == 0 {
		return ErrSource
	}
	peer, err := peerUID(fd)
	if err != nil || peer != uint32(uid) {
		return ErrSource
	}
	return nil
}

// SO_PEERCRED on the client sees systemd's listener, not the agents worker.
func requireActivatorPeer(conn net.Conn) error {
	socket, ok := conn.(*net.UnixConn)
	if !ok {
		return ErrUnavailable
	}
	raw, err := socket.SyscallConn()
	if err != nil {
		return ErrUnavailable
	}
	var peer uint32
	var peerErr error
	if raw.Control(func(fd uintptr) { peer, peerErr = peerUID(int(fd)) }) != nil || peerErr != nil || peer != 0 {
		return ErrUnavailable
	}
	return nil
}
