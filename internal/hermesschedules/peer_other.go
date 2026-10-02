//go:build !linux

package hermesschedules

import "net"

// The deployment boundary is Linux/systemd only; never omit peer checks.
func requireCallerPeer(int, string) error { return ErrUnavailable }
func requireActivatorPeer(net.Conn) error { return ErrUnavailable }
