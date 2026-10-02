//go:build linux

package hermesschedules

import (
	"net"
	"os"
	"os/user"
	"testing"

	"golang.org/x/sys/unix"
)

func TestHelperSocketPeerIdentities(t *testing.T) {
	pair, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(pair[0])
	defer unix.Close(pair[1])
	uid, err := peerUID(pair[0])
	if err != nil || uid != uint32(os.Getuid()) {
		t.Fatal("incorrect actual peer", uid, err)
	}
	caller, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	err = requireCallerPeer(pair[0], caller.Username)
	if os.Getuid() == 0 {
		if err == nil {
			t.Fatal("root caller accepted")
		}
	} else if err != nil {
		t.Fatal("exact unprivileged caller refused", err)
	}
	for _, account := range []string{"root", "loom-no-such-helper-account"} {
		if requireCallerPeer(pair[0], account) == nil {
			t.Fatal("wrong caller accepted", account)
		}
	}
	// Exercise an existing but different unprivileged account when available.
	if other, err := user.Lookup("nobody"); err == nil && other.Uid != caller.Uid {
		if requireCallerPeer(pair[0], other.Username) == nil {
			t.Fatal("different existing UID accepted")
		}
	}
	if requireCallerPeer(-1, caller.Username) == nil {
		t.Fatal("non-socket peer accepted")
	}
	duplicate, err := unix.Dup(pair[1])
	if err != nil {
		t.Fatal(err)
	}
	file := os.NewFile(uintptr(duplicate), "fixture")
	defer file.Close()
	conn, err := net.FileConn(file)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	err = requireActivatorPeer(conn)
	if os.Getuid() == 0 {
		if err != nil {
			t.Fatal("root activator refused", err)
		}
	} else if err == nil {
		t.Fatal("non-root activator accepted")
	}
}
