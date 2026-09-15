package preparation

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"time"

	"loom.local/loom/internal/objectstore"
)

// Client authenticates the root supervisor and retains verified returned bytes
// in caller-private temporary custody. A future jobs adapter owns retention.
type Client struct {
	Socket  string
	TempDir string
}

type PreparedOutput struct {
	Result    Result
	File      *os.File // Read-only. Close removes the caller-owned temporary custody.
	directory string
}

func (o *PreparedOutput) Close() error {
	err := o.File.Close()
	if e := os.RemoveAll(o.directory); err == nil {
		err = e
	}
	return err
}

func (c Client) Prepare(ctx context.Context, req Request, source, producer io.Reader) (_ *PreparedOutput, err error) {
	if _, err := req.Validate(); err != nil {
		return nil, err
	}
	if source == nil || producer == nil {
		return nil, ErrRefused
	}
	socket := c.Socket
	if socket == "" {
		socket = SocketPath
	}
	ctx, cancel := context.WithTimeout(ctx, WallTimeout)
	defer cancel()
	connection, err := (&net.Dialer{Timeout: HeaderTimeout}).DialContext(ctx, "unix", socket)
	if err != nil {
		return nil, fmt.Errorf("%w: connect", ErrUnavailable)
	}
	defer connection.Close()
	conn, ok := connection.(*net.UnixConn)
	if !ok {
		return nil, ErrUnavailable
	}
	if uid, err := peerUID(conn); err != nil || uid != 0 {
		return nil, ErrUnavailable
	}
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	_ = conn.SetDeadline(time.Now().Add(HeaderTimeout))
	if err := writeFrame(conn, req); err != nil {
		return nil, ErrUnavailable
	}
	var admission control
	if err := readFrame(conn, &admission); err != nil {
		return nil, ErrUnavailable
	}
	if admission.Schema != Schema {
		return nil, ErrUnavailable
	}
	if admission.Code == ErrBusy.Error() {
		return nil, ErrBusy
	}
	if admission.Status != "accepted" {
		return nil, ErrRefused
	}
	deadline, _ := ctx.Deadline()
	_ = conn.SetDeadline(deadline)
	if _, err := io.CopyN(conn, source, req.Source.SizeBytes); err != nil {
		return nil, ErrFailed
	}
	if _, err := io.CopyN(conn, producer, req.Producer.SizeBytes); err != nil {
		return nil, ErrFailed
	}
	// Keep the write half open. Closing/cancelling this connection is the
	// supervisor's liveness signal throughout execution and delivery.
	var response control
	if err := readFrame(conn, &response); err != nil {
		return nil, ErrFailed
	}
	if response.Schema != Schema || response.Status != "output" || response.Result == nil {
		return nil, ErrFailed
	}
	result := *response.Result
	if validatePackage(result.Output, MaxOutputBytes) != nil || result != resultFor(req, result.Output) {
		return nil, ErrFailed
	}
	directory, err := os.MkdirTemp(c.TempDir, "loom-prepared-output-")
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			_ = os.RemoveAll(directory)
		}
	}()
	name := filepath.Join(directory, "output.tar")
	f, err := os.OpenFile(name, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if _, err := io.CopyN(f, conn, result.Output.SizeBytes); err != nil {
		return nil, ErrFailed
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	actual, err := objectstore.InspectPackage(ctx, f)
	if err != nil || actual != result.Output {
		return nil, ErrFailed
	}
	var challenge control
	if readFrame(conn, &challenge) != nil || challenge.Schema != Schema || challenge.Status != "verify" || !validDigest("sha256:"+challenge.Receipt) {
		return nil, ErrFailed
	}
	if err := writeFrame(conn, control{Schema: Schema, Status: "received", Receipt: challenge.Receipt}); err != nil {
		return nil, ErrFailed
	}
	var complete control
	if readFrame(conn, &complete) != nil || complete.Schema != Schema || complete.Status != "complete" || complete.Receipt != challenge.Receipt {
		return nil, ErrFailed
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := f.Chmod(0400); err != nil {
		return nil, err
	}
	if err := f.Close(); err != nil {
		return nil, err
	}
	readonly, err := os.Open(name)
	if err != nil {
		return nil, err
	}
	return &PreparedOutput{Result: result, File: readonly, directory: directory}, nil
}
