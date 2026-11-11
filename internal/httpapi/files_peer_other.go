//go:build !linux

package httpapi

import "net"

func unixRootPeer(net.Conn) bool { return false }
