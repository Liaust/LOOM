//go:build linux

package httpapi

import (
	"golang.org/x/sys/unix"
	"net"
)

func unixRootPeer(connection net.Conn) bool {
	c, ok := connection.(*net.UnixConn)
	if !ok {
		return false
	}
	raw, err := c.SyscallConn()
	if err != nil {
		return false
	}
	root := false
	err = raw.Control(func(fd uintptr) {
		credential, e := unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
		root = e == nil && credential.Uid == 0
	})
	return err == nil && root
}
