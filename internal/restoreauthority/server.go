package restoreauthority

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const defaultCleanupTimeout = 30 * time.Second
const maxRestoreTOCBytes = int64(32 << 20)
const inheritedRestoreListPath = "/proc/self/fd/3"
const darwinInheritedRestoreListPath = "/dev/fd/3"

type CommandExecutor interface {
	Run(context.Context, string, []string, io.Reader) error
	InspectArchive(context.Context, string, []string, io.Reader, int64) ([]byte, error)
	RestoreArchive(context.Context, string, []string, io.Reader, *os.File) error
}

type ServerConfig struct {
	SocketPath              string
	SocketUID               uint32
	SocketGID               uint32
	ClientUID               uint32
	Operational             DatabasePolicy
	Provenance              DatabasePolicy
	CreatedbPath            string
	PGRestorePath           string
	DropdbPath              string
	PostgresSocketDirectory string
	PostgresPort            uint16
	RestoreListPath         string
	MaxDumpBytes            int64
	CleanupTimeout          time.Duration
	TempDir                 string
	Executor                CommandExecutor
}

type Server struct {
	config ServerConfig
	locks  targetLocks
}

type targetLock struct {
	token chan struct{}
	refs  int
}

type targetLocks struct {
	mu      sync.Mutex
	targets map[string]*targetLock
}

func NewServer(config ServerConfig) (*Server, error) {
	config.SocketPath = strings.TrimSpace(config.SocketPath)
	if config.SocketPath == "" {
		config.SocketPath = DefaultSocketPath
	}
	if err := validateDatabasePolicy(config.Operational); err != nil {
		return nil, fmt.Errorf("operational restore policy: %w", err)
	}
	if err := validateDatabasePolicy(config.Provenance); err != nil {
		return nil, fmt.Errorf("provenance restore policy: %w", err)
	}
	if config.Operational.ActiveDatabase == config.Provenance.ActiveDatabase || config.Operational.Owner == config.Provenance.Owner {
		return nil, fmt.Errorf("operational and provenance restore policies must remain distinct")
	}
	for label, path := range map[string]string{
		"createdb": config.CreatedbPath, "pg_restore": config.PGRestorePath, "dropdb": config.DropdbPath,
	} {
		if strings.TrimSpace(path) == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path {
			return nil, fmt.Errorf("%s path must be an exact absolute path", label)
		}
	}
	config.PostgresSocketDirectory = strings.TrimSpace(config.PostgresSocketDirectory)
	if config.PostgresSocketDirectory == "" {
		config.PostgresSocketDirectory = DefaultPostgresSocketDir
	}
	if !filepath.IsAbs(config.PostgresSocketDirectory) || filepath.Clean(config.PostgresSocketDirectory) != config.PostgresSocketDirectory {
		return nil, fmt.Errorf("PostgreSQL socket directory must be an exact absolute path")
	}
	if config.PostgresPort == 0 {
		config.PostgresPort = DefaultPostgresPort
	}
	if config.RestoreListPath == "" {
		config.RestoreListPath = inheritedRestoreListPath
	}
	if config.RestoreListPath != inheritedRestoreListPath && config.RestoreListPath != darwinInheritedRestoreListPath {
		return nil, fmt.Errorf("inherited restore list path is not reviewed")
	}
	if config.MaxDumpBytes == 0 {
		config.MaxDumpBytes = DefaultMaxDumpBytes
	}
	if config.MaxDumpBytes <= 0 {
		return nil, fmt.Errorf("restore authority payload bound must be positive")
	}
	if config.CleanupTimeout <= 0 {
		config.CleanupTimeout = defaultCleanupTimeout
	}
	if config.Executor == nil {
		config.Executor = boundedCommandExecutor{}
	}
	return &Server{config: config, locks: targetLocks{targets: make(map[string]*targetLock)}}, nil
}

func (server *Server) Serve(ctx context.Context, listener *net.UnixListener) error {
	if listener == nil {
		return fmt.Errorf("restore authority Unix listener is required")
	}
	if err := validateListenerPath(listener, server.config.SocketPath, server.config.SocketUID, server.config.SocketGID); err != nil {
		return err
	}
	go func() {
		<-ctx.Done()
		_ = listener.Close()
	}()
	var handlers sync.WaitGroup
	defer handlers.Wait()
	for {
		connection, err := listener.AcceptUnix()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("accept restore authority connection: %w", err)
		}
		handlers.Add(1)
		go func() {
			defer handlers.Done()
			defer connection.Close()
			server.handleConnection(ctx, listener, connection)
		}()
	}
}

func (server *Server) handleConnection(ctx context.Context, listener *net.UnixListener, connection *net.UnixConn) {
	failure := responseHeader{Schema: ProtocolSchema, Status: "failed"}
	failure.setFailure(ErrorRequestRefused)
	if err := validateListenerPath(listener, server.config.SocketPath, server.config.SocketUID, server.config.SocketGID); err != nil {
		failure.setFailure(ErrorSocketIdentityInvalid)
		_ = encodeFrame(connection, failure, maxProtocolResponseBytes)
		return
	}
	uid, err := unixConnectionPeerUID(connection)
	if err != nil || uid != server.config.ClientUID {
		failure.setFailure(ErrorPeerIdentityInvalid)
		_ = encodeFrame(connection, failure, maxProtocolResponseBytes)
		return
	}
	if deadline, ok := ctx.Deadline(); ok {
		_ = connection.SetDeadline(deadline)
	}
	var request requestHeader
	if err := decodeFrame(connection, &request, maxProtocolHeaderBytes); err != nil {
		failure.setFailure(ErrorProtocolInvalid)
		_ = encodeFrame(connection, failure, maxProtocolResponseBytes)
		return
	}
	failure.Operation = request.Operation
	failure.Kind = request.Kind
	failure.Database = request.Database
	if err := validateRequestHeader(request, server.config.MaxDumpBytes); err != nil {
		failure.setFailure(ErrorRequestInvalid)
		_ = encodeFrame(connection, failure, maxProtocolResponseBytes)
		return
	}
	if err := server.validateConfiguredTarget(request.Kind, request.Database); err != nil {
		failure.setFailure(ErrorDatabaseRefused)
		_ = encodeFrame(connection, failure, maxProtocolResponseBytes)
		return
	}

	var dump *os.File
	if request.Operation != OperationDropDisposable {
		dump, err = server.receiveDump(connection, request)
		if err != nil {
			failure.setFailure(ErrorPayloadInvalid)
			_ = encodeFrame(connection, failure, maxProtocolResponseBytes)
			return
		}
		defer dump.Close()
	}
	var trailer requestTrailer
	if err := decodeFrame(connection, &trailer, maxProtocolControlBytes); err != nil || validateRequestTrailer(trailer, request) != nil {
		failure.setFailure(ErrorProtocolInvalid)
		_ = encodeFrame(connection, failure, maxProtocolResponseBytes)
		return
	}

	requestCtx, cancelRequest := context.WithCancel(ctx)
	defer cancelRequest()
	go server.monitorRequest(connection, request, cancelRequest)
	release, err := server.locks.acquire(requestCtx, request.Database)
	if err != nil {
		failure.setFailure(ErrorRequestCancelled)
		_ = encodeFrame(connection, failure, maxProtocolResponseBytes)
		return
	}
	defer release()
	accepted := responseHeader{Schema: ProtocolSchema, Status: "accepted", Operation: request.Operation, Kind: request.Kind, Database: request.Database}
	if err := encodeFrame(connection, accepted, maxProtocolResponseBytes); err != nil {
		cancelRequest()
		return
	}

	var response responseHeader
	if request.Operation == OperationDropDisposable {
		response = server.drop(requestCtx, request)
	} else {
		response = server.restore(requestCtx, dump, request)
	}
	_ = encodeFrame(connection, response, maxProtocolResponseBytes)
}

func (server *Server) monitorRequest(connection *net.UnixConn, request requestHeader, cancel context.CancelFunc) {
	var control controlFrame
	if err := decodeFrame(connection, &control, maxProtocolControlBytes); err != nil || validateControlFrame(control, request) != nil {
		cancel()
		return
	}
	cancel()
}

func (server *Server) validateConfiguredTarget(kind Kind, database string) error {
	active := []string{server.config.Operational.ActiveDatabase, server.config.Provenance.ActiveDatabase}
	return ValidateDisposableDatabase(kind, database, active...)
}

func (response *responseHeader) setFailure(code string) {
	stage, message, ok := failureForCode(code)
	if !ok {
		panic("restoreauthority: unregistered server failure code")
	}
	response.Status = "failed"
	response.FailureStage = stage
	response.ErrorCode = code
	response.Message = message
}

func (server *Server) restore(ctx context.Context, dump *os.File, request requestHeader) responseHeader {
	response := responseHeader{
		Schema: ProtocolSchema, Status: "failed", Operation: request.Operation,
		Kind: request.Kind, Database: request.Database,
	}
	policy := server.policy(request.Kind)
	cleanup := func() error {
		response.CleanupAttempted = true
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), server.config.CleanupTimeout)
		defer cancel()
		err := server.config.Executor.Run(cleanupCtx, server.config.DropdbPath, append(server.clusterConnectionArgs(), "--force", "--if-exists", "--", request.Database), nil)
		response.CleanupSucceeded = err == nil
		return err
	}
	if err := server.config.Executor.Run(ctx, server.config.CreatedbPath, append(server.clusterConnectionArgs(), "--owner", policy.Owner, "--", request.Database), nil); err != nil {
		response.setFailure(ErrorDatabaseCreateFailed)
		if cleanupErr := cleanup(); cleanupErr != nil {
			response.setFailure(ErrorDatabaseCreateAndCleanupFailed)
		}
		return response
	}
	if _, err := dump.Seek(0, io.SeekStart); err != nil {
		response.setFailure(ErrorPayloadUnavailable)
		_ = cleanup()
		return response
	}
	toc, err := server.config.Executor.InspectArchive(ctx, server.config.PGRestorePath, []string{"--list"}, dump, maxRestoreTOCBytes)
	if err != nil {
		response.setFailure(ErrorArchiveInspectionFailed)
		_ = cleanup()
		return response
	}
	filteredTOC, err := filterRestoreTOC(toc)
	if err != nil {
		response.setFailure(ErrorArchiveTOCRefused)
		_ = cleanup()
		return response
	}
	restoreList, err := server.createUnlinkedFile(".loom-restore-list-*", filteredTOC)
	if err != nil {
		response.setFailure(ErrorArchiveTOCUnavailable)
		_ = cleanup()
		return response
	}
	defer restoreList.Close()
	if _, err := dump.Seek(0, io.SeekStart); err != nil {
		response.setFailure(ErrorPayloadUnavailable)
		_ = cleanup()
		return response
	}
	args := []string{
		"--host", server.config.PostgresSocketDirectory,
		"--port", strconv.FormatUint(uint64(server.config.PostgresPort), 10),
		"--username", policy.Owner,
		"--no-password", "--no-owner", "--no-acl", "--use-list", server.config.RestoreListPath, "--dbname", request.Database,
	}
	if err := server.config.Executor.RestoreArchive(ctx, server.config.PGRestorePath, args, dump, restoreList); err != nil || ctx.Err() != nil {
		response.setFailure(ErrorDatabaseRestoreFailed)
		if cleanupErr := cleanup(); cleanupErr != nil {
			response.setFailure(ErrorDatabaseRestoreAndCleanupFailed)
		}
		return response
	}
	response.Status = "succeeded"
	return response
}

func (server *Server) drop(ctx context.Context, request requestHeader) responseHeader {
	response := responseHeader{
		Schema: ProtocolSchema, Status: "failed", Operation: request.Operation,
		Kind: request.Kind, Database: request.Database, CleanupAttempted: true,
	}
	if err := server.config.Executor.Run(ctx, server.config.DropdbPath, append(server.clusterConnectionArgs(), "--force", "--if-exists", "--", request.Database), nil); err != nil {
		response.setFailure(ErrorDatabaseDropFailed)
		return response
	}
	response.Status = "succeeded"
	response.CleanupSucceeded = true
	return response
}

func (server *Server) clusterConnectionArgs() []string {
	return []string{
		"--host", server.config.PostgresSocketDirectory,
		"--port", strconv.FormatUint(uint64(server.config.PostgresPort), 10),
		"--username", "postgres",
		"--no-password",
		"--maintenance-db", "postgres",
	}
}

func (server *Server) policy(kind Kind) DatabasePolicy {
	if kind == KindOperational {
		return server.config.Operational
	}
	return server.config.Provenance
}

func (server *Server) receiveDump(reader io.Reader, request requestHeader) (*os.File, error) {
	dump, err := server.createUnlinkedFile(".loom-restore-authority-*", nil)
	if err != nil {
		return nil, err
	}
	hash := sha256.New()
	written, err := io.CopyN(io.MultiWriter(dump, hash), reader, request.DumpSize)
	if err != nil || written != request.DumpSize {
		_ = dump.Close()
		return nil, firstError(err, io.ErrUnexpectedEOF)
	}
	if fmt.Sprintf("%x", hash.Sum(nil)) != request.DumpSHA256 {
		_ = dump.Close()
		return nil, fmt.Errorf("restore payload digest mismatch")
	}
	if _, err := dump.Seek(0, io.SeekStart); err != nil {
		_ = dump.Close()
		return nil, err
	}
	return dump, nil
}

func (server *Server) createUnlinkedFile(pattern string, contents []byte) (*os.File, error) {
	file, err := os.CreateTemp(server.config.TempDir, pattern)
	if err != nil {
		return nil, err
	}
	path := file.Name()
	closeAndRemove := func() {
		_ = file.Close()
		_ = os.Remove(path)
	}
	if err := file.Chmod(0o600); err != nil {
		closeAndRemove()
		return nil, err
	}
	if len(contents) > 0 {
		if _, err := file.Write(contents); err != nil {
			closeAndRemove()
			return nil, err
		}
	}
	if err := os.Remove(path); err != nil {
		closeAndRemove()
		return nil, err
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		_ = file.Close()
		return nil, err
	}
	return file, nil
}

func filterRestoreTOC(raw []byte) ([]byte, error) {
	if len(raw) == 0 || int64(len(raw)) > maxRestoreTOCBytes {
		return nil, fmt.Errorf("restore archive table of contents is outside its bound")
	}
	var filtered bytes.Buffer
	extensionCount := 0
	commentCount := 0
	for _, line := range strings.SplitAfter(string(raw), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, ";") {
			filtered.WriteString(line)
			continue
		}
		_, body, ok := strings.Cut(trimmed, ";")
		if !ok {
			return nil, fmt.Errorf("restore archive table of contents entry is malformed")
		}
		fields := strings.Fields(strings.TrimSpace(body))
		if len(fields) >= 5 && fields[2] == "EXTENSION" && fields[3] == "-" && fields[4] == "vector" {
			if len(fields) != 5 {
				return nil, fmt.Errorf("vector extension table of contents entry is not exact")
			}
			extensionCount++
		}
		if len(fields) >= 6 && fields[2] == "COMMENT" && fields[3] == "-" && fields[4] == "EXTENSION" && fields[5] == "vector" {
			if len(fields) != 6 {
				return nil, fmt.Errorf("vector extension comment table of contents entry is not exact")
			}
			commentCount++
			continue
		}
		filtered.WriteString(line)
	}
	if extensionCount > 1 || commentCount > 1 || (commentCount == 1 && extensionCount != 1) {
		return nil, fmt.Errorf("vector extension table of contents entries are ambiguous")
	}
	return filtered.Bytes(), nil
}

func (locks *targetLocks) acquire(ctx context.Context, database string) (func(), error) {
	locks.mu.Lock()
	lock := locks.targets[database]
	if lock == nil {
		lock = &targetLock{token: make(chan struct{}, 1)}
		lock.token <- struct{}{}
		locks.targets[database] = lock
	}
	lock.refs++
	locks.mu.Unlock()

	select {
	case <-ctx.Done():
		locks.releaseReference(database, lock)
		return nil, ctx.Err()
	case <-lock.token:
		return func() {
			lock.token <- struct{}{}
			locks.releaseReference(database, lock)
		}, nil
	}
}

func (locks *targetLocks) releaseReference(database string, lock *targetLock) {
	locks.mu.Lock()
	defer locks.mu.Unlock()
	lock.refs--
	if lock.refs == 0 && locks.targets[database] == lock {
		delete(locks.targets, database)
	}
}

type boundedCommandExecutor struct{}

func (boundedCommandExecutor) Run(ctx context.Context, name string, args []string, stdin io.Reader) error {
	return runBoundedCommand(ctx, name, args, stdin, nil)
}

func (boundedCommandExecutor) InspectArchive(ctx context.Context, name string, args []string, stdin io.Reader, limit int64) ([]byte, error) {
	command := exec.CommandContext(ctx, name, args...)
	command.Stdin = stdin
	output := &boundedCapture{remaining: limit}
	command.Stdout = output
	command.Stderr = &boundedOutput{remaining: 32 << 10}
	if err := command.Run(); err != nil {
		return nil, fmt.Errorf("PostgreSQL archive inspection failed")
	}
	return output.Bytes(), nil
}

func (boundedCommandExecutor) RestoreArchive(ctx context.Context, name string, args []string, stdin io.Reader, restoreList *os.File) error {
	return runBoundedCommand(ctx, name, args, stdin, []*os.File{restoreList})
}

func runBoundedCommand(ctx context.Context, name string, args []string, stdin io.Reader, extraFiles []*os.File) error {
	command := exec.CommandContext(ctx, name, args...)
	command.Stdin = stdin
	command.ExtraFiles = extraFiles
	output := &boundedOutput{remaining: 32 << 10}
	command.Stdout = output
	command.Stderr = output
	if err := command.Run(); err != nil {
		return fmt.Errorf("PostgreSQL utility failed")
	}
	return nil
}

type boundedCapture struct {
	bytes.Buffer
	remaining int64
}

func (output *boundedCapture) Write(raw []byte) (int, error) {
	if int64(len(raw)) > output.remaining {
		return 0, fmt.Errorf("captured command output exceeds its bound")
	}
	output.remaining -= int64(len(raw))
	return output.Buffer.Write(raw)
}

type boundedOutput struct {
	remaining int
}

func (output *boundedOutput) Write(raw []byte) (int, error) {
	length := len(raw)
	if length > output.remaining {
		output.remaining = 0
		return length, nil
	}
	output.remaining -= length
	return length, nil
}
