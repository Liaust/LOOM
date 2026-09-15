//go:build !linux

package preparation

import (
	"context"
	"net"
)

func RunActivated(context.Context) error    { return ErrUnavailable }
func peerUID(*net.UnixConn) (uint32, error) { return 0, ErrUnavailable }
