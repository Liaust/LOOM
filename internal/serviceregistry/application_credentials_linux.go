//go:build linux

package serviceregistry

import (
	"context"
	"io"
	"os/exec"
	"os/user"
	"strconv"
	"syscall"
	"time"
)

func applicationProtonCommand(ctx context.Context, owner ApplicationOwner, program string, args []string, stdout io.Writer) error {
	a, err := user.Lookup("agents")
	if err != nil || a.HomeDir != "/home/agents" {
		return applicationError("credential.account_unavailable")
	}
	uid, e1 := strconv.ParseUint(a.Uid, 10, 32)
	gid, e2 := strconv.ParseUint(a.Gid, 10, 32)
	if e1 != nil || e2 != nil || uid == 0 || gid == 0 {
		return applicationError("credential.account_unavailable")
	}
	cmd := exec.CommandContext(ctx, program, args...)
	cmd.Env = applicationProtonEnvironment(owner)
	cmd.Dir = "/"
	cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: uint32(uid), Gid: uint32(gid)}}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = nil, stdout, io.Discard
	cmd.WaitDelay = time.Second
	return cmd.Run()
}
