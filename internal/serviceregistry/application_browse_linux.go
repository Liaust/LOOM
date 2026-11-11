//go:build linux

package serviceregistry

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"time"

	"golang.org/x/sys/unix"
)

func (c ApplicationHelperClient) Browse(ctx context.Context, q ApplicationBrowseRequest) (ApplicationBrowseReceipt, error) {
	x, e := c.exchange(ctx, ApplicationHelperEnvelope{Browse: &q})
	if x.Browse == nil {
		return ApplicationBrowseReceipt{}, e
	}
	return *x.Browse, e
}
func (c ApplicationHelperClient) ReadBrowse(ctx context.Context, q ApplicationBrowseRead) (ApplicationBrowseResult, error) {
	if q.Operation == "open" {
		return ApplicationBrowseResult{}, applicationError("browse.descriptor_exchange_required")
	}
	x, e := c.exchange(ctx, ApplicationHelperEnvelope{BrowseRead: &q})
	if x.BrowseResult == nil {
		return ApplicationBrowseResult{}, e
	}
	return *x.BrowseResult, e
}
func (c ApplicationHelperClient) OpenBrowse(ctx context.Context, q ApplicationBrowseRead) (*os.File, ApplicationBrowseRoot, error) {
	q.Operation = "open"
	address := c.SocketPath
	if address == "" {
		address = ApplicationSocketPath
	}
	var st unix.Stat_t
	if !applicationAbsolutePath(address) || applicationCheckParents(address, 0) != nil || unix.Lstat(address, &st) != nil || st.Uid != 0 || st.Mode&unix.S_IFMT != unix.S_IFSOCK || st.Mode&0007 != 0 {
		return nil, ApplicationBrowseRoot{}, applicationError("socket.custody")
	}
	conn, e := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "unix", address)
	if e != nil {
		return nil, ApplicationBrowseRoot{}, applicationError("socket.unavailable")
	}
	defer conn.Close()
	u := conn.(*net.UnixConn)
	sc, e := u.SyscallConn()
	if e != nil {
		return nil, ApplicationBrowseRoot{}, e
	}
	var peer uint32
	var pe error
	if sc.Control(func(fd uintptr) { peer, pe = applicationPeerUID(int(fd)) }) != nil || pe != nil || peer != 0 {
		return nil, ApplicationBrowseRoot{}, applicationError("socket.activator_peer")
	}
	deadline := time.Now().Add(10 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	u.SetDeadline(deadline)
	stop := context.AfterFunc(ctx, func() { u.Close() })
	defer stop()
	payload, e := json.Marshal(ApplicationHelperEnvelope{BrowseRead: &q})
	if e != nil || len(payload) > applicationMaxBytes {
		return nil, ApplicationBrowseRoot{}, applicationError("request.size")
	}
	if _, e = u.Write(payload); e != nil {
		return nil, ApplicationBrowseRoot{}, e
	}
	u.CloseWrite()
	marker := make([]byte, 1)
	oob := make([]byte, unix.CmsgSpace(4*16))
	n, on, flags, _, e := u.ReadMsgUnix(marker, oob)
	fds := []int{}
	messages, parseErr := unix.ParseSocketControlMessage(oob[:on])
	for _, message := range messages {
		rights, err := unix.ParseUnixRights(&message)
		if err != nil {
			parseErr = err
		}
		fds = append(fds, rights...)
	}
	defer func() {
		for _, fd := range fds {
			unix.Close(fd)
		}
	}()
	if e != nil || parseErr != nil || flags&unix.MSG_CTRUNC != 0 || n != 1 || marker[0] != 0 || len(fds) > 1 {
		return nil, ApplicationBrowseRoot{}, applicationError("browse.descriptor_invalid")
	}
	for _, fd := range fds {
		unix.CloseOnExec(fd)
	}
	raw, e := io.ReadAll(io.LimitReader(u, applicationMaxBytes+1))
	var result ApplicationHelperResponse
	if e != nil || applicationDecodeJSON(raw, &result) != nil {
		return nil, ApplicationBrowseRoot{}, applicationError("browse.response_invalid")
	}
	if result.ErrorCode != "" {
		return nil, ApplicationBrowseRoot{}, errors.New(result.ErrorCode)
	}
	if len(fds) != 1 || result.BrowseResult == nil || result.BrowseResult.Root == nil {
		return nil, ApplicationBrowseRoot{}, applicationError("browse.descriptor_missing")
	}
	fd := fds[0]
	fds = nil
	return os.NewFile(uintptr(fd), q.Path), *result.BrowseResult.Root, nil
}
