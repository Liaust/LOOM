package restoreauthority

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"time"
)

const defaultCancellationCleanupTimeout = 35 * time.Second

type ClientConfig struct {
	SocketPath string
	SocketUID  uint32
	SocketGID  uint32
	// SocketActivatorUID is the one peer identity expected on a connection
	// created through an inherited listening socket. It is deliberately
	// independent from the authority process's execution identity.
	SocketActivatorUID *uint32
	// ServerUID remains for direct-listener compatibility. New
	// socket-activated callers must set SocketActivatorUID instead.
	ServerUID                  uint32
	MaxDumpBytes               int64
	DialTimeout                time.Duration
	CancellationCleanupTimeout time.Duration
}

type Client struct {
	config          ClientConfig
	expectedPeerUID uint32
}

type AuthorityError struct {
	Stage   FailureStage
	Code    string
	Message string
	Result  Result
	Cause   error
}

func (err *AuthorityError) Error() string {
	if err == nil {
		return ""
	}
	return fmt.Sprintf("restore authority %s at %s: %s", err.Code, err.Stage, err.Message)
}

func (err *AuthorityError) Unwrap() error {
	if err == nil {
		return nil
	}
	return err.Cause
}

func clientFailure(header requestHeader, code string, cause error) *AuthorityError {
	stage, message, ok := failureForCode(code)
	if !ok {
		panic("restoreauthority: unregistered client failure code")
	}
	result := Result{Status: "failed", Kind: header.Kind, Database: header.Database, FailureStage: stage, ErrorCode: code}
	return &AuthorityError{Stage: stage, Code: code, Message: message, Result: result, Cause: cause}
}

func NewClient(config ClientConfig) (*Client, error) {
	config.SocketPath = strings.TrimSpace(config.SocketPath)
	if config.SocketPath == "" {
		config.SocketPath = DefaultSocketPath
	}
	if config.MaxDumpBytes == 0 {
		config.MaxDumpBytes = DefaultMaxDumpBytes
	}
	if config.MaxDumpBytes <= 0 {
		return nil, fmt.Errorf("restore authority payload bound must be positive")
	}
	if config.DialTimeout <= 0 {
		config.DialTimeout = 5 * time.Second
	}
	if config.CancellationCleanupTimeout <= 0 {
		config.CancellationCleanupTimeout = defaultCancellationCleanupTimeout
	}
	if config.SocketActivatorUID != nil && config.ServerUID != 0 {
		return nil, fmt.Errorf("restore authority client requires exactly one peer identity source")
	}
	expectedPeerUID := config.ServerUID
	if config.SocketActivatorUID != nil {
		expectedPeerUID = *config.SocketActivatorUID
	}
	return &Client{config: config, expectedPeerUID: expectedPeerUID}, nil
}

func (client *Client) Restore(ctx context.Context, request RestoreRequest) (Result, error) {
	if err := validateRestoreRequest(request, client.config.MaxDumpBytes); err != nil {
		return Result{}, err
	}
	operation, err := operationForKind(request.Kind)
	if err != nil {
		return Result{}, err
	}
	header := requestHeader{
		Schema: ProtocolSchema, Operation: operation, Kind: request.Kind,
		Database: request.Database, DumpSize: request.DumpSize, DumpSHA256: request.DumpSHA256,
	}
	return client.exchange(ctx, header, request.Dump)
}

func (client *Client) Drop(ctx context.Context, request DropRequest) (Result, error) {
	if err := ValidateDisposableDatabase(request.Kind, request.Database); err != nil {
		return Result{}, err
	}
	return client.exchange(ctx, requestHeader{
		Schema: ProtocolSchema, Operation: OperationDropDisposable,
		Kind: request.Kind, Database: request.Database,
	}, nil)
}

func (client *Client) exchange(ctx context.Context, header requestHeader, payload io.Reader) (Result, error) {
	if _, err := ValidateSocketPath(client.config.SocketPath, client.config.SocketUID, client.config.SocketGID); err != nil {
		failure := clientFailure(header, ErrorClientSocketInvalid, err)
		return failure.Result, failure
	}
	dialer := net.Dialer{Timeout: client.config.DialTimeout}
	connection, err := dialer.DialContext(ctx, "unix", client.config.SocketPath)
	if err != nil {
		failure := clientFailure(header, ErrorClientConnectFailed, err)
		return failure.Result, failure
	}
	unixConnection, ok := connection.(*net.UnixConn)
	if !ok {
		_ = connection.Close()
		failure := clientFailure(header, ErrorClientConnectFailed, nil)
		return failure.Result, failure
	}
	defer unixConnection.Close()
	peer, err := unixConnectionPeerUID(unixConnection)
	if err != nil || peer != client.expectedPeerUID {
		failure := clientFailure(header, ErrorClientPeerInvalid, err)
		return failure.Result, failure
	}
	if deadline, ok := ctx.Deadline(); ok {
		if err := unixConnection.SetDeadline(deadline); err != nil {
			failure := clientFailure(header, ErrorClientSendFailed, err)
			return failure.Result, failure
		}
	}
	cancelWatchDone := make(chan struct{})
	cancelWatchExited := make(chan struct{})
	var cancelWatchOnce sync.Once
	stopCancelWatch := func() {
		cancelWatchOnce.Do(func() { close(cancelWatchDone) })
		<-cancelWatchExited
	}
	go func() {
		defer close(cancelWatchExited)
		select {
		case <-ctx.Done():
			_ = unixConnection.SetDeadline(time.Now())
		case <-cancelWatchDone:
		}
	}()
	defer stopCancelWatch()

	if err := encodeFrame(unixConnection, header, maxProtocolHeaderBytes); err != nil {
		failure := clientFailure(header, ErrorClientSendFailed, err)
		return failure.Result, failure
	}
	var localPayloadErr error
	if payload != nil {
		hash := sha256.New()
		written, copyErr := io.CopyN(io.MultiWriter(unixConnection, hash), payload, header.DumpSize)
		if copyErr != nil || written != header.DumpSize {
			failure := clientFailure(header, ErrorClientPayloadFailed, firstError(copyErr, io.ErrUnexpectedEOF))
			return failure.Result, failure
		}
		var extra [1]byte
		count, readErr := payload.Read(extra[:])
		if count != 0 || (readErr != nil && !errors.Is(readErr, io.EOF)) {
			// Transmit one bounded rejection marker so the authority itself also
			// observes non-exact input and cannot restore after the client closes.
			marker := extra[:count]
			if len(marker) == 0 {
				marker = []byte{0}
			}
			if _, err := unixConnection.Write(marker); err != nil {
				failure := clientFailure(header, ErrorClientPayloadFailed, err)
				return failure.Result, failure
			}
			localPayloadErr = clientFailure(header, ErrorPayloadInvalid, nil)
		}
		if fmt.Sprintf("%x", hash.Sum(nil)) != header.DumpSHA256 {
			localPayloadErr = clientFailure(header, ErrorPayloadInvalid, nil)
		}
	}
	trailer := requestTrailer{
		Schema: ProtocolSchema, Stage: "request_complete", Operation: header.Operation, Kind: header.Kind, Database: header.Database,
	}
	if err := encodeFrame(unixConnection, trailer, maxProtocolControlBytes); err != nil {
		failure := clientFailure(header, ErrorClientSendFailed, err)
		return failure.Result, failure
	}
	var response responseHeader
	if err := decodeFrame(unixConnection, &response, maxProtocolResponseBytes); err != nil {
		failure := clientFailure(header, ErrorClientReceiveFailed, err)
		return failure.Result, failure
	}
	if response.Status != "accepted" {
		return client.finishResponse(unixConnection, header, response, localPayloadErr, nil)
	}
	if err := validateAcceptedHeader(response, header); err != nil {
		failure := clientFailure(header, ErrorClientResponseInvalid, err)
		return failure.Result, failure
	}
	stopCancelWatch()
	if err := unixConnection.SetDeadline(time.Time{}); err != nil {
		failure := clientFailure(header, ErrorClientReceiveFailed, err)
		return failure.Result, failure
	}
	type responseResult struct {
		response responseHeader
		err      error
	}
	responseCh := make(chan responseResult, 1)
	go func() {
		var final responseHeader
		err := decodeFrame(unixConnection, &final, maxProtocolResponseBytes)
		responseCh <- responseResult{response: final, err: err}
	}()
	var cancellationErr error
	if ctx.Err() != nil {
		cancellationErr = ctx.Err()
	} else {
		select {
		case received := <-responseCh:
			if received.err != nil {
				failure := clientFailure(header, ErrorClientReceiveFailed, received.err)
				return failure.Result, failure
			}
			return client.finishResponse(unixConnection, header, received.response, localPayloadErr, nil)
		case <-ctx.Done():
			cancellationErr = ctx.Err()
		}
	}

	cleanupDeadline := time.Now().Add(client.config.CancellationCleanupTimeout)
	_ = unixConnection.SetWriteDeadline(cleanupDeadline)
	_ = encodeFrame(unixConnection, controlFrame{
		Schema: ProtocolSchema, Action: "cancel", Operation: header.Operation, Kind: header.Kind, Database: header.Database,
	}, maxProtocolControlBytes)
	_ = unixConnection.CloseWrite()
	_ = unixConnection.SetReadDeadline(cleanupDeadline)
	received := <-responseCh
	if received.err != nil {
		failure := clientFailure(header, ErrorClientReceiveFailed, cancellationErr)
		return failure.Result, failure
	}
	result, responseErr := client.finishResponse(unixConnection, header, received.response, localPayloadErr, cancellationErr)
	if received.response.Status == "succeeded" && header.Operation != OperationDropDisposable {
		cleanupCtx, cancelCleanup := context.WithTimeout(context.WithoutCancel(ctx), client.config.CancellationCleanupTimeout)
		defer cancelCleanup()
		cleanupResult, cleanupErr := client.Drop(cleanupCtx, DropRequest{Kind: header.Kind, Database: header.Database})
		result.Status = "failed"
		result.CleanupAttempted = true
		result.CleanupSucceeded = cleanupErr == nil && cleanupResult.CleanupAttempted && cleanupResult.CleanupSucceeded
		var cancellationFailure *AuthorityError
		if errors.As(responseErr, &cancellationFailure) && cancellationFailure.Code == ErrorClientCancelled {
			cancellationFailure.Result = result
		}
		if cleanupErr != nil {
			return result, errors.Join(responseErr, fmt.Errorf("cancelled restore cleanup failed: %w", cleanupErr))
		}
	}
	return result, responseErr
}

func (client *Client) finishResponse(connection *net.UnixConn, header requestHeader, response responseHeader, localPayloadErr, cancellationErr error) (Result, error) {
	if err := validateResponseHeader(response, header); err != nil {
		failure := clientFailure(header, ErrorClientResponseInvalid, err)
		return failure.Result, failure
	}
	_ = connection.CloseWrite()
	var trailing [1]byte
	if count, readErr := connection.Read(trailing[:]); count != 0 || (readErr != nil && !errors.Is(readErr, io.EOF)) {
		failure := clientFailure(header, ErrorClientResponseInvalid, readErr)
		return failure.Result, failure
	}
	result := Result{
		Status: response.Status, Kind: response.Kind, Database: response.Database,
		FailureStage: response.FailureStage, ErrorCode: response.ErrorCode,
		CleanupAttempted: response.CleanupAttempted, CleanupSucceeded: response.CleanupSucceeded,
	}
	if result.Kind == "" && result.Database == "" {
		result.Kind = header.Kind
		result.Database = header.Database
	}
	if localPayloadErr != nil {
		if response.Status == "succeeded" {
			failure := clientFailure(header, ErrorClientResponseInvalid, localPayloadErr)
			return result, failure
		}
		return result, errors.Join(&AuthorityError{Stage: response.FailureStage, Code: response.ErrorCode, Message: response.Message, Result: result}, localPayloadErr)
	}
	if response.Status != "succeeded" {
		authorityErr := &AuthorityError{Stage: response.FailureStage, Code: response.ErrorCode, Message: response.Message, Result: result}
		if cancellationErr != nil {
			return result, errors.Join(cancellationErr, authorityErr)
		}
		return result, authorityErr
	}
	if cancellationErr != nil {
		failure := clientFailure(header, ErrorClientCancelled, cancellationErr)
		failure.Result = result
		failure.Result.Status = "failed"
		failure.Result.FailureStage = failure.Stage
		failure.Result.ErrorCode = failure.Code
		return failure.Result, failure
	}
	return result, nil
}

func firstError(primary, fallback error) error {
	if primary != nil {
		return primary
	}
	return fallback
}

var _ RestoreDatabaseAuthority = (*Client)(nil)
