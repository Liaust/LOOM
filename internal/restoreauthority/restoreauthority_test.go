package restoreauthority

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

type recordedCommand struct {
	name  string
	args  []string
	stdin []byte
}

func TestRestoreAuthorityDefaultSocketPathUsesDedicatedRuntimeDirectory(t *testing.T) {
	const oldSharedSocketPath = "/run/loom/restore-authority.sock"
	if DefaultSocketPath != "/run/loom-restore-authority/restore-authority.sock" {
		t.Fatalf("default socket path = %q", DefaultSocketPath)
	}
	if DefaultSocketPath == oldSharedSocketPath {
		t.Fatal("restore authority default still selects loomd's shared runtime directory")
	}
}

func TestRestoreAuthorityFailureContractIsClosedBoundedAndSecretFree(t *testing.T) {
	expected := requestHeader{Schema: ProtocolSchema, Operation: OperationRestoreOperational, Kind: KindOperational, Database: OperationalPrefix + "contract"}
	for code, definition := range failureDefinitions {
		response := responseHeader{
			Schema: ProtocolSchema, Status: "failed", Operation: expected.Operation,
			Kind: expected.Kind, Database: expected.Database, FailureStage: definition.Stage,
			ErrorCode: code, Message: definition.Message,
		}
		if err := validateResponseHeader(response, expected); err != nil {
			t.Fatalf("valid failure %s/%s rejected: %v", definition.Stage, code, err)
		}
		if len(definition.Message) > 120 || strings.ContainsAny(definition.Message, "\n\r") || strings.Contains(definition.Message, "/") || strings.Contains(definition.Message, "://") {
			t.Fatalf("failure %s message is not bounded generic text: %q", code, definition.Message)
		}
		response.FailureStage = FailureStageDatabaseDrop
		if response.FailureStage == definition.Stage {
			response.FailureStage = FailureStagePeer
		}
		if err := validateResponseHeader(response, expected); err == nil {
			t.Fatalf("failure %s accepted with a mismatched stage", code)
		}
	}
	for _, response := range []responseHeader{
		{Schema: ProtocolSchema, Status: "failed", Operation: expected.Operation, Kind: expected.Kind, Database: expected.Database, ErrorCode: ErrorDatabaseCreateFailed, Message: failureDefinitions[ErrorDatabaseCreateFailed].Message},
		{Schema: ProtocolSchema, Status: "failed", Operation: expected.Operation, Kind: expected.Kind, Database: expected.Database, FailureStage: FailureStageDatabaseCreate, ErrorCode: "unknown", Message: "Generic."},
		{Schema: ProtocolSchema, Status: "failed", Operation: expected.Operation, Kind: expected.Kind, Database: expected.Database, FailureStage: FailureStageDatabaseCreate, ErrorCode: ErrorDatabaseCreateFailed, Message: "raw secret MUST-NOT-LEAK"},
		{Schema: ProtocolSchema, Status: "succeeded", Operation: expected.Operation, Kind: expected.Kind, Database: expected.Database, FailureStage: FailureStageDatabaseCreate, ErrorCode: ErrorDatabaseCreateFailed, Message: failureDefinitions[ErrorDatabaseCreateFailed].Message},
	} {
		if err := validateResponseHeader(response, expected); err == nil {
			t.Fatalf("invalid response accepted: %#v", response)
		}
	}
	for _, code := range []string{ErrorSocketIdentityInvalid, ErrorPeerIdentityInvalid} {
		definition := failureDefinitions[code]
		response := responseHeader{
			Schema: ProtocolSchema, Status: "failed", FailureStage: definition.Stage,
			ErrorCode: code, Message: definition.Message,
		}
		if err := validateResponseHeader(response, expected); err != nil {
			t.Fatalf("pre-identity %s response rejected: %v", code, err)
		}
	}
}

func TestRestoreAuthorityClientFailureStagesSurviveWrappingAndJoin(t *testing.T) {
	header := requestHeader{Operation: OperationRestoreOperational, Kind: KindOperational, Database: OperationalPrefix + "client_stages"}
	for _, test := range []struct {
		code  string
		stage FailureStage
	}{
		{ErrorClientSocketInvalid, FailureStageSocket},
		{ErrorClientConnectFailed, FailureStageSocket},
		{ErrorClientPeerInvalid, FailureStagePeer},
		{ErrorClientSendFailed, FailureStageSend},
		{ErrorClientPayloadFailed, FailureStageSend},
		{ErrorClientReceiveFailed, FailureStageReceive},
		{ErrorClientResponseInvalid, FailureStageReceive},
		{ErrorClientCancelled, FailureStageCancellation},
	} {
		t.Run(test.code, func(t *testing.T) {
			cause := context.Canceled
			failure := clientFailure(header, test.code, cause)
			joined := errors.Join(errors.New("ordinary wrapper"), fmt.Errorf("backup wrapper: %w", failure))
			var preserved *AuthorityError
			if !errors.As(joined, &preserved) || preserved != failure || preserved.Stage != test.stage || preserved.Code != test.code || preserved.Result.FailureStage != test.stage || preserved.Result.ErrorCode != test.code || !errors.Is(joined, context.Canceled) {
				t.Fatalf("client failure was not preserved: %#v err=%v", preserved, joined)
			}
		})
	}
}

type recordingExecutor struct {
	mu       sync.Mutex
	commands []recordedCommand
	run      func(context.Context, string, []string, io.Reader) error
	inspect  func(context.Context, string, []string, io.Reader, int64) ([]byte, error)
	restore  func(context.Context, string, []string, io.Reader, *os.File) error
}

func (executor *recordingExecutor) InspectArchive(ctx context.Context, name string, args []string, stdin io.Reader, limit int64) ([]byte, error) {
	if executor.inspect != nil {
		return executor.inspect(ctx, name, args, stdin, limit)
	}
	raw, err := io.ReadAll(stdin)
	if err != nil {
		return nil, err
	}
	executor.mu.Lock()
	executor.commands = append(executor.commands, recordedCommand{name: name, args: append([]string(nil), args...), stdin: raw})
	executor.mu.Unlock()
	return []byte("; synthetic authenticated archive TOC\n1; 1259 1 TABLE public synthetic loom\n"), nil
}

func (executor *recordingExecutor) RestoreArchive(ctx context.Context, name string, args []string, stdin io.Reader, restoreList *os.File) error {
	if executor.restore != nil {
		return executor.restore(ctx, name, args, stdin, restoreList)
	}
	if restoreList == nil {
		return fmt.Errorf("restore list descriptor is missing")
	}
	if _, err := os.Lstat(restoreList.Name()); !os.IsNotExist(err) {
		return fmt.Errorf("restore list descriptor remains linked")
	}
	return executor.Run(ctx, name, args, stdin)
}

func (executor *recordingExecutor) Run(ctx context.Context, name string, args []string, stdin io.Reader) error {
	var raw []byte
	var err error
	if stdin != nil {
		raw, err = io.ReadAll(stdin)
		if err != nil {
			return err
		}
	}
	executor.mu.Lock()
	executor.commands = append(executor.commands, recordedCommand{name: name, args: append([]string(nil), args...), stdin: raw})
	executor.mu.Unlock()
	if executor.run != nil {
		return executor.run(ctx, name, args, bytes.NewReader(raw))
	}
	return nil
}

func (executor *recordingExecutor) snapshot() []recordedCommand {
	executor.mu.Lock()
	defer executor.mu.Unlock()
	return append([]recordedCommand(nil), executor.commands...)
}

type authorityFixture struct {
	client    *Client
	server    *Server
	listener  *net.UnixListener
	cancel    context.CancelFunc
	done      chan error
	path      string
	executor  *recordingExecutor
	socketUID uint32
	socketGID uint32
}

func startAuthorityFixture(t *testing.T, mutate func(*ServerConfig, *recordingExecutor)) *authorityFixture {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "loom-ra-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	path := filepath.Join(dir, "restore-authority.sock")
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	listener.SetUnlinkOnClose(true)
	if err := os.Chmod(path, 0o660); err != nil {
		t.Fatal(err)
	}
	socketInfo, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	socketStat := socketInfo.Sys().(*syscall.Stat_t)
	executor := &recordingExecutor{}
	config := ServerConfig{
		SocketPath: path, SocketUID: socketStat.Uid, SocketGID: socketStat.Gid, ClientUID: uint32(os.Getuid()),
		Operational:   DatabasePolicy{ActiveDatabase: "loom_main", Owner: "loom"},
		Provenance:    DatabasePolicy{ActiveDatabase: "loom_provenance", Owner: "loom_provenance"},
		CreatedbPath:  "/reviewed/postgresql/bin/createdb",
		PGRestorePath: "/reviewed/postgresql/bin/pg_restore",
		DropdbPath:    "/reviewed/postgresql/bin/dropdb",
		MaxDumpBytes:  1024 * 1024, CleanupTimeout: time.Second, TempDir: dir, Executor: executor,
	}
	if mutate != nil {
		mutate(&config, executor)
	}
	server, err := NewServer(config)
	if err != nil {
		t.Fatal(err)
	}
	expectedPeerUID := uint32(os.Getuid())
	client, err := NewClient(ClientConfig{
		SocketPath: path, SocketUID: socketStat.Uid, SocketGID: socketStat.Gid, SocketActivatorUID: &expectedPeerUID,
		MaxDumpBytes: config.MaxDumpBytes,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- server.Serve(ctx, listener) }()
	waitForAuthorityFixture(t, path)
	fixture := &authorityFixture{client: client, server: server, listener: listener, cancel: cancel, done: done, path: path, executor: executor, socketUID: socketStat.Uid, socketGID: socketStat.Gid}
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("restore authority server stopped with error: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("restore authority server did not stop")
		}
	})
	return fixture
}

func waitForAuthorityFixture(t *testing.T, socketPath string) {
	t.Helper()
	connection, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: socketPath, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	if _, err := connection.Write([]byte{0, 0, 0, 0}); err != nil && !errors.Is(err, syscall.EPIPE) && !errors.Is(err, syscall.ENOTCONN) {
		t.Fatal(err)
	}
	if err := connection.CloseWrite(); err != nil && !errors.Is(err, syscall.ENOTCONN) {
		t.Fatal(err)
	}
	var response responseHeader
	if err := decodeFrame(connection, &response, maxProtocolResponseBytes); err != nil {
		t.Fatalf("wait for restore authority fixture: %v", err)
	}
	if response.ErrorCode != "protocol_invalid" && response.ErrorCode != "peer_identity_invalid" {
		t.Fatalf("restore authority fixture readiness response=%#v", response)
	}
}

func TestRestoreAuthorityExecutesOnlyFixedOperationalAndProvenanceVectors(t *testing.T) {
	fixture := startAuthorityFixture(t, nil)
	for _, test := range []struct {
		kind     Kind
		database string
		payload  []byte
	}{
		{KindOperational, OperationalPrefix + "fixed_1", []byte("PGDMP-operational")},
		{KindProvenance, ProvenancePrefix + "fixed_1", []byte("PGDMP-provenance")},
	} {
		digest := sha256.Sum256(test.payload)
		result, err := fixture.client.Restore(context.Background(), RestoreRequest{
			Kind: test.kind, Database: test.database, DumpSize: int64(len(test.payload)),
			DumpSHA256: fmt.Sprintf("%x", digest[:]), Dump: bytes.NewReader(test.payload),
		})
		if err != nil || result.Status != "succeeded" || result.Kind != test.kind || result.Database != test.database {
			t.Fatalf("restore result=%#v err=%v", result, err)
		}
		if _, err := fixture.client.Drop(context.Background(), DropRequest{Kind: test.kind, Database: test.database}); err != nil {
			t.Fatal(err)
		}
	}
	commands := fixture.executor.snapshot()
	want := []recordedCommand{
		{name: "/reviewed/postgresql/bin/createdb", args: []string{"--host", "/run/postgresql", "--port", "5432", "--username", "postgres", "--no-password", "--maintenance-db", "postgres", "--owner", "loom", "--", OperationalPrefix + "fixed_1"}},
		{name: "/reviewed/postgresql/bin/pg_restore", args: []string{"--list"}, stdin: []byte("PGDMP-operational")},
		{name: "/reviewed/postgresql/bin/pg_restore", args: []string{"--host", "/run/postgresql", "--port", "5432", "--username", "loom", "--no-password", "--no-owner", "--no-acl", "--use-list", "/proc/self/fd/3", "--dbname", OperationalPrefix + "fixed_1"}, stdin: []byte("PGDMP-operational")},
		{name: "/reviewed/postgresql/bin/dropdb", args: []string{"--host", "/run/postgresql", "--port", "5432", "--username", "postgres", "--no-password", "--maintenance-db", "postgres", "--force", "--if-exists", "--", OperationalPrefix + "fixed_1"}},
		{name: "/reviewed/postgresql/bin/createdb", args: []string{"--host", "/run/postgresql", "--port", "5432", "--username", "postgres", "--no-password", "--maintenance-db", "postgres", "--owner", "loom_provenance", "--", ProvenancePrefix + "fixed_1"}},
		{name: "/reviewed/postgresql/bin/pg_restore", args: []string{"--list"}, stdin: []byte("PGDMP-provenance")},
		{name: "/reviewed/postgresql/bin/pg_restore", args: []string{"--host", "/run/postgresql", "--port", "5432", "--username", "loom_provenance", "--no-password", "--no-owner", "--no-acl", "--use-list", "/proc/self/fd/3", "--dbname", ProvenancePrefix + "fixed_1"}, stdin: []byte("PGDMP-provenance")},
		{name: "/reviewed/postgresql/bin/dropdb", args: []string{"--host", "/run/postgresql", "--port", "5432", "--username", "postgres", "--no-password", "--maintenance-db", "postgres", "--force", "--if-exists", "--", ProvenancePrefix + "fixed_1"}},
	}
	if len(commands) != len(want) {
		t.Fatalf("commands=%#v", commands)
	}
	for index := range want {
		if commands[index].name != want[index].name || strings.Join(commands[index].args, "\x00") != strings.Join(want[index].args, "\x00") || !bytes.Equal(commands[index].stdin, want[index].stdin) {
			t.Fatalf("command[%d]=%#v want %#v", index, commands[index], want[index])
		}
	}
}

func TestRestoreAuthorityTOCFilteringOmitsOnlyPrecreatedVectorExtensionComment(t *testing.T) {
	raw := []byte(`; Archive created at 2026-09-02
2; 3079 16385 EXTENSION - vector
4034; 0 0 COMMENT - EXTENSION vector
4035; 0 0 COMMENT public TABLE sample loom
4036; 1259 16390 TABLE public sample loom
`)
	filtered, err := filterRestoreTOC(raw)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(filtered), "COMMENT - EXTENSION vector") {
		t.Fatalf("vector extension comment survived: %s", filtered)
	}
	for _, preserved := range []string{
		"EXTENSION - vector", "COMMENT public TABLE sample loom", "TABLE public sample loom",
	} {
		if !strings.Contains(string(filtered), preserved) {
			t.Fatalf("ordinary TOC entry %q was not preserved: %s", preserved, filtered)
		}
	}
	for _, refused := range [][]byte{
		[]byte("4034; 0 0 COMMENT - EXTENSION vector postgres\n"),
		[]byte("2; 3079 1 EXTENSION - vector\n3; 3079 2 EXTENSION - vector\n"),
		[]byte("4034; 0 0 COMMENT - EXTENSION vector\n"),
	} {
		if _, err := filterRestoreTOC(refused); err == nil {
			t.Fatalf("ambiguous vector TOC was accepted: %q", refused)
		}
	}
}

func TestRestoreAuthorityTrailerAndControlFramesAreStrictAndBounded(t *testing.T) {
	t.Run("trailer_identity_mismatch", func(t *testing.T) {
		fixture := startAuthorityFixture(t, nil)
		payload := []byte("PGDMP-trailer")
		digest := sha256.Sum256(payload)
		header := requestHeader{
			Schema: ProtocolSchema, Operation: OperationRestoreOperational, Kind: KindOperational,
			Database: OperationalPrefix + "trailer", DumpSize: int64(len(payload)), DumpSHA256: fmt.Sprintf("%x", digest[:]),
		}
		connection, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: fixture.path, Net: "unix"})
		if err != nil {
			t.Fatal(err)
		}
		defer connection.Close()
		if err := encodeFrame(connection, header, maxProtocolHeaderBytes); err != nil {
			t.Fatal(err)
		}
		if _, err := connection.Write(payload); err != nil {
			t.Fatal(err)
		}
		if err := encodeFrame(connection, requestTrailer{
			Schema: ProtocolSchema, Stage: "request_complete", Operation: header.Operation,
			Kind: header.Kind, Database: OperationalPrefix + "different",
		}, maxProtocolControlBytes); err != nil {
			t.Fatal(err)
		}
		if err := connection.CloseWrite(); err != nil {
			t.Fatal(err)
		}
		var response responseHeader
		if err := decodeFrame(connection, &response, maxProtocolResponseBytes); err != nil {
			t.Fatal(err)
		}
		if response.ErrorCode != "protocol_invalid" || len(fixture.executor.snapshot()) != 0 {
			t.Fatalf("mismatched trailer response=%#v commands=%#v", response, fixture.executor.snapshot())
		}
	})

	t.Run("oversized_trailer", func(t *testing.T) {
		fixture := startAuthorityFixture(t, nil)
		payload := []byte("PGDMP-trailer-bound")
		digest := sha256.Sum256(payload)
		header := requestHeader{
			Schema: ProtocolSchema, Operation: OperationRestoreOperational, Kind: KindOperational,
			Database: OperationalPrefix + "trailer_bound", DumpSize: int64(len(payload)), DumpSHA256: fmt.Sprintf("%x", digest[:]),
		}
		connection, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: fixture.path, Net: "unix"})
		if err != nil {
			t.Fatal(err)
		}
		defer connection.Close()
		if err := encodeFrame(connection, header, maxProtocolHeaderBytes); err != nil {
			t.Fatal(err)
		}
		if _, err := connection.Write(payload); err != nil {
			t.Fatal(err)
		}
		var prefix [4]byte
		binary.BigEndian.PutUint32(prefix[:], maxProtocolControlBytes+1)
		if _, err := connection.Write(prefix[:]); err != nil {
			t.Fatal(err)
		}
		if err := connection.CloseWrite(); err != nil {
			t.Fatal(err)
		}
		var response responseHeader
		if err := decodeFrame(connection, &response, maxProtocolResponseBytes); err != nil {
			t.Fatal(err)
		}
		if response.ErrorCode != "protocol_invalid" || len(fixture.executor.snapshot()) != 0 {
			t.Fatalf("oversized trailer response=%#v commands=%#v", response, fixture.executor.snapshot())
		}
	})

	t.Run("invalid_control_cancels_and_cleans", func(t *testing.T) {
		started := make(chan struct{})
		cleaned := make(chan struct{})
		fixture := startAuthorityFixture(t, func(_ *ServerConfig, executor *recordingExecutor) {
			executor.run = func(ctx context.Context, name string, _ []string, _ io.Reader) error {
				switch filepath.Base(name) {
				case "pg_restore":
					close(started)
					<-ctx.Done()
					return ctx.Err()
				case "dropdb":
					if ctx.Err() != nil {
						return ctx.Err()
					}
					close(cleaned)
				}
				return nil
			}
		})
		payload := []byte("PGDMP-control")
		digest := sha256.Sum256(payload)
		header := requestHeader{
			Schema: ProtocolSchema, Operation: OperationRestoreOperational, Kind: KindOperational,
			Database: OperationalPrefix + "control", DumpSize: int64(len(payload)), DumpSHA256: fmt.Sprintf("%x", digest[:]),
		}
		connection, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: fixture.path, Net: "unix"})
		if err != nil {
			t.Fatal(err)
		}
		defer connection.Close()
		if err := encodeFrame(connection, header, maxProtocolHeaderBytes); err != nil {
			t.Fatal(err)
		}
		if _, err := connection.Write(payload); err != nil {
			t.Fatal(err)
		}
		if err := encodeFrame(connection, requestTrailer{Schema: ProtocolSchema, Stage: "request_complete", Operation: header.Operation, Kind: header.Kind, Database: header.Database}, maxProtocolControlBytes); err != nil {
			t.Fatal(err)
		}
		var accepted responseHeader
		if err := decodeFrame(connection, &accepted, maxProtocolResponseBytes); err != nil || validateAcceptedHeader(accepted, header) != nil {
			t.Fatalf("acknowledgment=%#v err=%v", accepted, err)
		}
		select {
		case <-started:
		case <-time.After(5 * time.Second):
			t.Fatal("pg_restore did not start")
		}
		if err := encodeFrame(connection, map[string]any{
			"schema": ProtocolSchema, "action": "cancel", "operation": header.Operation,
			"kind": header.Kind, "database": header.Database, "sql": "DROP DATABASE loom_main",
		}, maxProtocolControlBytes); err != nil {
			t.Fatal(err)
		}
		if err := connection.CloseWrite(); err != nil {
			t.Fatal(err)
		}
		var response responseHeader
		if err := decodeFrame(connection, &response, maxProtocolResponseBytes); err != nil {
			t.Fatal(err)
		}
		if response.Status != "failed" || !response.CleanupAttempted || !response.CleanupSucceeded {
			t.Fatalf("invalid control response=%#v", response)
		}
		select {
		case <-cleaned:
		default:
			t.Fatal("invalid control did not use detached cleanup")
		}
	})
}

func TestRestoreAuthorityProtocolAndPayloadBoundsFailBeforeExecution(t *testing.T) {
	fixture := startAuthorityFixture(t, nil)
	payload := []byte("PGDMP-authenticated")
	digest := sha256.Sum256(payload)
	valid := requestHeader{
		Schema: ProtocolSchema, Operation: OperationRestoreOperational, Kind: KindOperational,
		Database: OperationalPrefix + "protocol", DumpSize: int64(len(payload)), DumpSHA256: fmt.Sprintf("%x", digest[:]),
	}
	tests := []struct {
		name    string
		header  any
		payload []byte
	}{
		{"unknown_field", map[string]any{"schema": ProtocolSchema, "operation": OperationRestoreOperational, "kind": KindOperational, "database": OperationalPrefix + "protocol", "dump_size": len(payload), "dump_sha256": fmt.Sprintf("%x", digest[:]), "command": "sh"}, payload},
		{"injected_owner", map[string]any{"schema": ProtocolSchema, "operation": OperationRestoreOperational, "kind": KindOperational, "database": OperationalPrefix + "protocol", "dump_size": len(payload), "dump_sha256": fmt.Sprintf("%x", digest[:]), "owner": "postgres"}, payload},
		{"injected_path", map[string]any{"schema": ProtocolSchema, "operation": OperationRestoreOperational, "kind": KindOperational, "database": OperationalPrefix + "protocol", "dump_size": len(payload), "dump_sha256": fmt.Sprintf("%x", digest[:]), "path": "/tmp/attacker"}, payload},
		{"injected_sql", map[string]any{"schema": ProtocolSchema, "operation": OperationRestoreOperational, "kind": KindOperational, "database": OperationalPrefix + "protocol", "dump_size": len(payload), "dump_sha256": fmt.Sprintf("%x", digest[:]), "sql": "DROP DATABASE loom_main"}, payload},
		{"injected_arguments", map[string]any{"schema": ProtocolSchema, "operation": OperationRestoreOperational, "kind": KindOperational, "database": OperationalPrefix + "protocol", "dump_size": len(payload), "dump_sha256": fmt.Sprintf("%x", digest[:]), "args": []string{"--dbname", "loom_main"}}, payload},
		{"injected_environment", map[string]any{"schema": ProtocolSchema, "operation": OperationRestoreOperational, "kind": KindOperational, "database": OperationalPrefix + "protocol", "dump_size": len(payload), "dump_sha256": fmt.Sprintf("%x", digest[:]), "env": map[string]string{"PGDATABASE": "loom_main"}}, payload},
		{"unsupported_schema", requestHeader{Schema: "future", Operation: valid.Operation, Kind: valid.Kind, Database: valid.Database, DumpSize: valid.DumpSize, DumpSHA256: valid.DumpSHA256}, payload},
		{"empty_schema", requestHeader{Operation: valid.Operation, Kind: valid.Kind, Database: valid.Database, DumpSize: valid.DumpSize, DumpSHA256: valid.DumpSHA256}, payload},
		{"unsupported_operation", requestHeader{Schema: ProtocolSchema, Operation: "sql", Kind: valid.Kind, Database: valid.Database, DumpSize: valid.DumpSize, DumpSHA256: valid.DumpSHA256}, payload},
		{"kind_operation_confusion", requestHeader{Schema: ProtocolSchema, Operation: OperationRestoreOperational, Kind: KindProvenance, Database: ProvenancePrefix + "protocol", DumpSize: valid.DumpSize, DumpSHA256: valid.DumpSHA256}, payload},
		{"empty_database", requestHeader{Schema: ProtocolSchema, Operation: valid.Operation, Kind: valid.Kind, DumpSize: valid.DumpSize, DumpSHA256: valid.DumpSHA256}, payload},
		{"overlong_database", requestHeader{Schema: ProtocolSchema, Operation: valid.Operation, Kind: valid.Kind, Database: OperationalPrefix + strings.Repeat("a", MaxDatabaseNameBytes), DumpSize: valid.DumpSize, DumpSHA256: valid.DumpSHA256}, payload},
		{"truncated", valid, payload[:len(payload)-1]},
		{"trailing", valid, append(append([]byte(nil), payload...), 'x')},
		{"hash_mismatch", requestHeader{Schema: ProtocolSchema, Operation: valid.Operation, Kind: valid.Kind, Database: valid.Database, DumpSize: valid.DumpSize, DumpSHA256: strings.Repeat("0", 64)}, payload},
		{"size_mismatch", requestHeader{Schema: ProtocolSchema, Operation: valid.Operation, Kind: valid.Kind, Database: valid.Database, DumpSize: valid.DumpSize - 1, DumpSHA256: valid.DumpSHA256}, payload},
		{"unbounded", requestHeader{Schema: ProtocolSchema, Operation: valid.Operation, Kind: valid.Kind, Database: valid.Database, DumpSize: 1024*1024 + 1, DumpSHA256: valid.DumpSHA256}, nil},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := rawAuthorityRequest(t, fixture.path, test.header, test.payload)
			if response.Status != "failed" {
				t.Fatalf("response=%#v", response)
			}
		})
	}
	t.Run("trailing_json_in_frame", func(t *testing.T) {
		raw, err := json.Marshal(valid)
		if err != nil {
			t.Fatal(err)
		}
		response := rawAuthorityFrame(t, fixture.path, append(raw, []byte(` {}`)...), payload)
		if response.ErrorCode != "protocol_invalid" {
			t.Fatalf("response=%#v", response)
		}
	})
	t.Run("oversized_frame", func(t *testing.T) {
		connection, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: fixture.path, Net: "unix"})
		if err != nil {
			t.Fatal(err)
		}
		defer connection.Close()
		var prefix [4]byte
		binary.BigEndian.PutUint32(prefix[:], maxProtocolHeaderBytes+1)
		if _, err := connection.Write(prefix[:]); err != nil {
			t.Fatal(err)
		}
		if err := connection.CloseWrite(); err != nil {
			t.Fatal(err)
		}
		var response responseHeader
		if err := decodeFrame(connection, &response, maxProtocolResponseBytes); err != nil {
			t.Fatal(err)
		}
		if response.ErrorCode != "protocol_invalid" {
			t.Fatalf("response=%#v", response)
		}
	})
	t.Run("client_trailing_input", func(t *testing.T) {
		result, err := fixture.client.Restore(context.Background(), RestoreRequest{
			Kind: valid.Kind, Database: valid.Database, DumpSize: valid.DumpSize,
			DumpSHA256: valid.DumpSHA256, Dump: bytes.NewReader(append(append([]byte(nil), payload...), 'x')),
		})
		var authorityErr *AuthorityError
		if err == nil || !errors.As(err, &authorityErr) || authorityErr.Stage != FailureStageRequest || authorityErr.Code != ErrorProtocolInvalid || result.FailureStage != FailureStageRequest || result.ErrorCode != ErrorProtocolInvalid {
			t.Fatalf("client trailing input error=%v", err)
		}
	})
	if commands := fixture.executor.snapshot(); len(commands) != 0 {
		t.Fatalf("invalid protocol reached PostgreSQL execution: %#v", commands)
	}
}

func rawAuthorityFrame(t *testing.T, socketPath string, rawHeader, payload []byte) responseHeader {
	t.Helper()
	connection, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: socketPath, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	var prefix [4]byte
	binary.BigEndian.PutUint32(prefix[:], uint32(len(rawHeader)))
	if _, err := connection.Write(prefix[:]); err != nil {
		t.Fatal(err)
	}
	if _, err := connection.Write(rawHeader); err != nil {
		t.Fatal(err)
	}
	if _, err := connection.Write(payload); err != nil && !isAuthorityEarlyClose(err) {
		t.Fatal(err)
	}
	if err := connection.CloseWrite(); err != nil && !isAuthorityEarlyClose(err) {
		t.Fatal(err)
	}
	var response responseHeader
	if err := decodeFrame(connection, &response, maxProtocolResponseBytes); err != nil {
		t.Fatal(err)
	}
	return response
}

func rawAuthorityRequest(t *testing.T, socketPath string, header any, payload []byte) responseHeader {
	t.Helper()
	connection, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: socketPath, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	raw, err := json.Marshal(header)
	if err != nil {
		t.Fatal(err)
	}
	var prefix [4]byte
	binary.BigEndian.PutUint32(prefix[:], uint32(len(raw)))
	if _, err := connection.Write(prefix[:]); err != nil {
		t.Fatal(err)
	}
	if _, err := connection.Write(raw); err != nil {
		t.Fatal(err)
	}
	if len(payload) > 0 {
		if _, err := connection.Write(payload); err != nil && !isAuthorityEarlyClose(err) {
			t.Fatal(err)
		}
	}
	if err := connection.CloseWrite(); err != nil && !isAuthorityEarlyClose(err) {
		t.Fatal(err)
	}
	var response responseHeader
	if err := decodeFrame(connection, &response, maxProtocolResponseBytes); err != nil {
		t.Fatal(err)
	}
	return response
}

func isAuthorityEarlyClose(err error) bool {
	return errors.Is(err, syscall.EPIPE) || errors.Is(err, syscall.ENOTCONN)
}

func TestRestoreAuthorityRejectsSocketAndPeerSubstitution(t *testing.T) {
	fixture := startAuthorityFixture(t, nil)
	payload := []byte("PGDMP")
	digest := sha256.Sum256(payload)
	request := RestoreRequest{Kind: KindOperational, Database: OperationalPrefix + "socket", DumpSize: int64(len(payload)), DumpSHA256: fmt.Sprintf("%x", digest[:]), Dump: bytes.NewReader(payload)}

	expectedPeerUID := uint32(os.Getuid())
	wrongOwner, err := NewClient(ClientConfig{SocketPath: fixture.path, SocketUID: fixture.socketUID + 1, SocketGID: fixture.socketGID, SocketActivatorUID: &expectedPeerUID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := wrongOwner.Restore(context.Background(), request); err == nil {
		t.Fatal("wrong socket owner was accepted")
	}
	wrongGroup, err := NewClient(ClientConfig{SocketPath: fixture.path, SocketUID: fixture.socketUID, SocketGID: fixture.socketGID + 1, SocketActivatorUID: &expectedPeerUID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := wrongGroup.Restore(context.Background(), request); err == nil {
		t.Fatal("wrong socket group was accepted")
	}
	wrongActivatorUID := expectedPeerUID + 1
	wrongActivator, err := NewClient(ClientConfig{SocketPath: fixture.path, SocketUID: fixture.socketUID, SocketGID: fixture.socketGID, SocketActivatorUID: &wrongActivatorUID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := wrongActivator.Restore(context.Background(), request); err == nil {
		t.Fatal("wrong socket activator peer was accepted")
	} else {
		var authorityErr *AuthorityError
		if !errors.As(err, &authorityErr) || authorityErr.Code != ErrorClientPeerInvalid {
			t.Fatalf("wrong socket activator failure = %v", err)
		}
	}
	if err := os.Chmod(fixture.path, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.client.Restore(context.Background(), request); err == nil {
		t.Fatal("wrong socket mode was accepted")
	}
	if err := os.Chmod(fixture.path, 0o660); err != nil {
		t.Fatal(err)
	}
	linked := filepath.Join(filepath.Dir(fixture.path), "linked.sock")
	if err := os.Link(fixture.path, linked); err != nil {
		t.Fatalf("create linked socket substitution: %v", err)
	}
	if _, err := fixture.client.Restore(context.Background(), request); err == nil {
		t.Fatal("linked socket path was accepted")
	}
	if err := os.Remove(linked); err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	regular := filepath.Join(dir, "regular")
	if err := os.WriteFile(regular, []byte("not a socket"), 0o660); err != nil {
		t.Fatal(err)
	}
	for name, path := range map[string]string{
		"missing":    filepath.Join(dir, "missing.sock"),
		"non_socket": regular,
		"symlink":    filepath.Join(dir, "symlink.sock"),
	} {
		if name == "symlink" {
			if err := os.Symlink(fixture.path, path); err != nil {
				t.Fatal(err)
			}
		}
		client, err := NewClient(ClientConfig{SocketPath: path, SocketUID: fixture.socketUID, SocketGID: fixture.socketGID, SocketActivatorUID: &expectedPeerUID})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := client.Restore(context.Background(), request); err == nil {
			t.Fatalf("%s socket substitution was accepted", name)
		}
	}

	peerFixture := startAuthorityFixture(t, func(config *ServerConfig, _ *recordingExecutor) {
		config.ClientUID = uint32(os.Getuid() + 1)
	})
	peerRequest := request
	peerRequest.Dump = bytes.NewReader(payload)
	if _, err := peerFixture.client.Restore(context.Background(), peerRequest); err == nil {
		t.Fatal("unexpected client peer identity was accepted")
	}
	if len(peerFixture.executor.snapshot()) != 0 {
		t.Fatal("unexpected peer reached PostgreSQL execution")
	}
}

func TestRestoreAuthorityClientRejectsMultiplePeerIdentitySources(t *testing.T) {
	activatorUID := uint32(os.Getuid())
	legacyServerUID := activatorUID + 1
	if _, err := NewClient(ClientConfig{SocketActivatorUID: &activatorUID, ServerUID: legacyServerUID}); err == nil {
		t.Fatal("client accepted alternate activator and server peer identities")
	}
}

func TestRestoreAuthorityClientRejectsProtocolResponseSubstitution(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "loom-ra-response-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	path := filepath.Join(dir, "restore-authority.sock")
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	listener.SetUnlinkOnClose(true)
	t.Cleanup(func() { _ = listener.Close() })
	if err := os.Chmod(path, 0o660); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	stat := info.Sys().(*syscall.Stat_t)
	expectedPeerUID := uint32(os.Getuid())
	client, err := NewClient(ClientConfig{
		SocketPath: path, SocketUID: stat.Uid, SocketGID: stat.Gid, SocketActivatorUID: &expectedPeerUID,
	})
	if err != nil {
		t.Fatal(err)
	}
	serverDone := make(chan error, 1)
	go func() {
		connection, acceptErr := listener.AcceptUnix()
		if acceptErr != nil {
			serverDone <- acceptErr
			return
		}
		defer connection.Close()
		var header requestHeader
		if err := decodeFrame(connection, &header, maxProtocolHeaderBytes); err != nil {
			serverDone <- err
			return
		}
		var trailer requestTrailer
		if err := decodeFrame(connection, &trailer, maxProtocolControlBytes); err != nil {
			serverDone <- err
			return
		}
		serverDone <- encodeFrame(connection, responseHeader{
			Schema: ProtocolSchema, Status: "succeeded", Operation: header.Operation,
			Kind: header.Kind, Database: header.Database + "_substituted",
		}, maxProtocolResponseBytes)
	}()
	result, err := client.Drop(context.Background(), DropRequest{Kind: KindOperational, Database: OperationalPrefix + "response"})
	if err == nil {
		t.Fatalf("substituted protocol response accepted: %#v", result)
	}
	var authorityErr *AuthorityError
	if !errors.As(err, &authorityErr) || authorityErr.Code != ErrorClientResponseInvalid {
		t.Fatalf("substituted response failure = %#v err=%v", authorityErr, err)
	}
	if serveErr := <-serverDone; serveErr != nil {
		t.Fatalf("substitution fixture: %v", serveErr)
	}
}

func TestRestoreAuthorityInheritedListenerSeparatesActivatorPeerFromExecutor(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux SO_PEERCRED socket-activation proof")
	}
	if os.Geteuid() != 0 {
		t.Skip("distinct inherited-listener execution UID proof requires a root-owned disposable fixture")
	}
	const executorUID = uint32(65534)
	const executorGID = uint32(65534)
	dir := newInheritedListenerFixtureDir(t, executorUID, executorGID)
	path := filepath.Join(dir, "restore-authority.sock")
	if len(path) > 100 {
		t.Fatalf("inherited listener fixture socket path is not bounded: %q", path)
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	listener.SetUnlinkOnClose(true)
	t.Cleanup(func() { _ = listener.Close() })
	if err := os.Chmod(path, 0o660); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	stat := info.Sys().(*syscall.Stat_t)
	listenerFile, err := listener.File()
	if err != nil {
		t.Fatal(err)
	}

	testBinary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	childBinary := filepath.Join(dir, "restore-authority-test")
	input, err := os.Open(testBinary)
	if err != nil {
		t.Fatal(err)
	}
	output, err := os.OpenFile(childBinary, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o755)
	if err != nil {
		_ = input.Close()
		t.Fatal(err)
	}
	if _, err := io.Copy(output, input); err != nil {
		_ = input.Close()
		_ = output.Close()
		t.Fatal(err)
	}
	if err := errors.Join(input.Close(), output.Close()); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(childBinary, 0o755); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(childBinary, "-test.run=^TestRestoreAuthorityInheritedListenerHelper$")
	cmd.Env = append(os.Environ(),
		"LOOM_RESTORE_AUTHORITY_INHERITED_HELPER=1",
		"LOOM_RESTORE_AUTHORITY_INHERITED_SOCKET="+path,
		"LOOM_RESTORE_AUTHORITY_INHERITED_SOCKET_UID="+strconv.FormatUint(uint64(stat.Uid), 10),
		"LOOM_RESTORE_AUTHORITY_INHERITED_SOCKET_GID="+strconv.FormatUint(uint64(stat.Gid), 10),
		"LOOM_RESTORE_AUTHORITY_INHERITED_CLIENT_UID="+strconv.Itoa(os.Getuid()),
	)
	cmd.ExtraFiles = []*os.File{listenerFile}
	cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: executorUID, Gid: executorGID}}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = listenerFile.Close()
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		_ = listenerFile.Close()
		t.Fatal(err)
	}
	_ = listenerFile.Close()
	stopped := false
	t.Cleanup(func() {
		if !stopped {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	})
	type readyResult struct {
		line string
		err  error
	}
	readyCh := make(chan readyResult, 1)
	go func() {
		line, err := bufio.NewReader(stdout).ReadString('\n')
		readyCh <- readyResult{line: line, err: err}
	}()
	var ready readyResult
	select {
	case ready = <-readyCh:
	case <-time.After(5 * time.Second):
		t.Fatalf("inherited listener helper did not become ready: %s", stderr.String())
	}
	if ready.err != nil {
		t.Fatalf("read inherited listener helper readiness: %v: %s", ready.err, stderr.String())
	}
	fields := strings.Fields(ready.line)
	if len(fields) != 2 || fields[0] != "READY" || fields[1] != strconv.FormatUint(uint64(executorUID), 10) {
		t.Fatalf("inherited listener helper identity = %q, want executor UID %d: %s", ready.line, executorUID, stderr.String())
	}

	activatorUID := uint32(os.Getuid())
	wrongUID := activatorUID + 1
	wrongClient, err := NewClient(ClientConfig{SocketPath: path, SocketUID: stat.Uid, SocketGID: stat.Gid, SocketActivatorUID: &wrongUID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := wrongClient.Drop(context.Background(), DropRequest{Kind: KindOperational, Database: OperationalPrefix + "inherited_wrong"}); err == nil {
		t.Fatal("inherited listener accepted a substituted activator peer")
	}
	client, err := NewClient(ClientConfig{SocketPath: path, SocketUID: stat.Uid, SocketGID: stat.Gid, SocketActivatorUID: &activatorUID})
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.Drop(context.Background(), DropRequest{Kind: KindOperational, Database: OperationalPrefix + "inherited"})
	if err != nil || result.Status != "succeeded" {
		t.Fatalf("inherited listener result=%#v err=%v", result, err)
	}
	if activatorUID == executorUID {
		t.Fatal("fixture did not separate the socket activator and authority executor UIDs")
	}
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("inherited listener helper exit: %v: %s", err, stderr.String())
	}
	stopped = true
}

func newInheritedListenerFixtureDir(t *testing.T, childUID, childGID uint32) string {
	t.Helper()
	const fixtureParent = "/tmp"
	dir, err := os.MkdirTemp(fixtureParent, "lr6w-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(dir); err != nil {
			t.Errorf("remove inherited listener fixture %q: %v", dir, err)
			return
		}
		if _, err := os.Lstat(dir); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("inherited listener fixture cleanup left %q: %v", dir, err)
		}
	})
	if filepath.Dir(dir) != fixtureParent || !strings.HasPrefix(filepath.Base(dir), "lr6w-") {
		t.Fatalf("unexpected inherited listener fixture root: %q", dir)
	}
	if err := os.Chmod(dir, 0o711); err != nil {
		t.Fatal(err)
	}
	assertDirectoriesSearchableBy(t, dir, childUID, childGID)
	return dir
}

func assertDirectoriesSearchableBy(t *testing.T, leaf string, uid, gid uint32) {
	t.Helper()
	for dir := filepath.Clean(leaf); ; dir = filepath.Dir(dir) {
		info, err := os.Lstat(dir)
		if err != nil {
			t.Fatalf("stat inherited listener fixture path component %q: %v", dir, err)
		}
		if !info.IsDir() {
			t.Fatalf("inherited listener fixture path component is not a directory: %q", dir)
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok {
			t.Fatalf("read inherited listener fixture ownership for %q", dir)
		}
		required := os.FileMode(0o001)
		switch {
		case stat.Uid == uid:
			required = 0o100
		case stat.Gid == gid:
			required = 0o010
		}
		if info.Mode().Perm()&required == 0 {
			t.Fatalf("inherited listener fixture path component %q mode %04o is not searchable by uid=%d gid=%d", dir, info.Mode().Perm(), uid, gid)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return
		}
	}
}

func TestRestoreAuthorityInheritedListenerHelper(t *testing.T) {
	if os.Getenv("LOOM_RESTORE_AUTHORITY_INHERITED_HELPER") != "1" {
		return
	}
	parseUID := func(name string) uint32 {
		value, err := strconv.ParseUint(os.Getenv(name), 10, 32)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		return uint32(value)
	}
	file := os.NewFile(3, "restore-authority-inherited-listener")
	if file == nil {
		t.Fatal("open inherited listener descriptor")
	}
	genericListener, err := net.FileListener(file)
	_ = file.Close()
	if err != nil {
		t.Fatal(err)
	}
	listener, ok := genericListener.(*net.UnixListener)
	if !ok {
		_ = genericListener.Close()
		t.Fatal("inherited listener is not Unix")
	}
	defer listener.Close()
	server, err := NewServer(ServerConfig{
		SocketPath:   os.Getenv("LOOM_RESTORE_AUTHORITY_INHERITED_SOCKET"),
		SocketUID:    parseUID("LOOM_RESTORE_AUTHORITY_INHERITED_SOCKET_UID"),
		SocketGID:    parseUID("LOOM_RESTORE_AUTHORITY_INHERITED_SOCKET_GID"),
		ClientUID:    parseUID("LOOM_RESTORE_AUTHORITY_INHERITED_CLIENT_UID"),
		Operational:  DatabasePolicy{ActiveDatabase: "loom_main", Owner: "loom"},
		Provenance:   DatabasePolicy{ActiveDatabase: "loom_provenance", Owner: "loom_provenance"},
		CreatedbPath: "/reviewed/postgresql/bin/createdb", PGRestorePath: "/reviewed/postgresql/bin/pg_restore", DropdbPath: "/reviewed/postgresql/bin/dropdb",
		MaxDumpBytes: 1024 * 1024, CleanupTimeout: time.Second, Executor: &recordingExecutor{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fmt.Fprintf(os.Stdout, "READY %d\n", os.Getuid()); err != nil {
		t.Fatal(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM)
	defer stop()
	if err := server.Serve(ctx, listener); err != nil {
		t.Fatal(err)
	}
}

func TestRestoreAuthorityDatabaseMatrixRejectsConfusion(t *testing.T) {
	validSuffix := strings.Repeat("a", MaxDatabaseNameBytes-len(OperationalPrefix))
	for _, test := range []struct {
		kind   Kind
		name   string
		active []string
		valid  bool
	}{
		{KindOperational, OperationalPrefix + "ok", []string{"loom_main", "loom_provenance"}, true},
		{KindProvenance, ProvenancePrefix + "ok", []string{"loom_main", "loom_provenance"}, true},
		{KindOperational, OperationalPrefix + validSuffix, nil, true},
		{KindOperational, ProvenancePrefix + "cross", nil, false},
		{KindProvenance, OperationalPrefix + "cross", nil, false},
		{KindOperational, OperationalPrefix + "Bad", nil, false},
		{KindOperational, OperationalPrefix + "bad-name", nil, false},
		{KindOperational, OperationalPrefix, nil, false},
		{KindOperational, OperationalPrefix + validSuffix + "x", nil, false},
		{KindOperational, "postgres", nil, false},
		{KindOperational, "template0", nil, false},
		{KindOperational, "template1", nil, false},
		{KindOperational, "loom_main", []string{"loom_main"}, false},
		{"unknown", OperationalPrefix + "ok", nil, false},
	} {
		err := ValidateDisposableDatabase(test.kind, test.name, test.active...)
		if (err == nil) != test.valid {
			t.Fatalf("ValidateDisposableDatabase(%q,%q) err=%v valid=%t", test.kind, test.name, err, test.valid)
		}
	}
	for _, mutate := range []func(*ServerConfig){
		func(config *ServerConfig) { config.Operational.Owner = config.Provenance.Owner },
		func(config *ServerConfig) { config.Operational.Owner = "postgres" },
		func(config *ServerConfig) { config.Provenance.ActiveDatabase = config.Operational.ActiveDatabase },
		func(config *ServerConfig) { config.Operational.ActiveDatabase = "template1" },
		func(config *ServerConfig) { config.PGRestorePath = "pg_restore" },
		func(config *ServerConfig) { config.PostgresSocketDirectory = "relative/postgresql" },
		func(config *ServerConfig) { config.RestoreListPath = "/tmp/caller-selected-list" },
	} {
		config := ServerConfig{
			Operational:  DatabasePolicy{ActiveDatabase: "loom_main", Owner: "loom"},
			Provenance:   DatabasePolicy{ActiveDatabase: "loom_provenance", Owner: "loom_provenance"},
			CreatedbPath: "/fixed/createdb", PGRestorePath: "/fixed/pg_restore", DropdbPath: "/fixed/dropdb",
		}
		mutate(&config)
		if _, err := NewServer(config); err == nil {
			t.Fatalf("unsafe server policy was accepted: %#v", config)
		}
	}
}

func TestRestoreAuthorityFailureCancellationAndCleanupTruth(t *testing.T) {
	t.Run("restore_and_cleanup_failure", func(t *testing.T) {
		fixture := startAuthorityFixture(t, func(_ *ServerConfig, executor *recordingExecutor) {
			executor.run = func(_ context.Context, name string, _ []string, _ io.Reader) error {
				switch filepath.Base(name) {
				case "pg_restore":
					return errors.New("secret restore detail MUST-NOT-LEAK")
				case "dropdb":
					return errors.New("secret cleanup detail MUST-NOT-LEAK")
				}
				return nil
			}
		})
		payload := []byte("PGDMP")
		digest := sha256.Sum256(payload)
		result, err := fixture.client.Restore(context.Background(), RestoreRequest{
			Kind: KindOperational, Database: OperationalPrefix + "failure", DumpSize: int64(len(payload)),
			DumpSHA256: fmt.Sprintf("%x", digest[:]), Dump: bytes.NewReader(payload),
		})
		if err == nil || !result.CleanupAttempted || result.CleanupSucceeded || !strings.Contains(err.Error(), "database_restore_and_cleanup_failed") {
			t.Fatalf("result=%#v err=%v", result, err)
		}
		var authorityErr *AuthorityError
		if !errors.As(err, &authorityErr) || authorityErr.Stage != FailureStageDatabaseCleanup || authorityErr.Code != ErrorDatabaseRestoreAndCleanupFailed || result.FailureStage != authorityErr.Stage || result.ErrorCode != authorityErr.Code {
			t.Fatalf("typed restore failure was not preserved: result=%#v err=%#v", result, authorityErr)
		}
		if strings.Contains(err.Error(), "MUST-NOT-LEAK") {
			t.Fatalf("authority error leaked command detail: %v", err)
		}
	})

	t.Run("cancelled_restore_uses_live_cleanup_context", func(t *testing.T) {
		started := make(chan struct{})
		cleaned := make(chan struct{})
		fixture := startAuthorityFixture(t, func(_ *ServerConfig, executor *recordingExecutor) {
			executor.run = func(ctx context.Context, name string, _ []string, _ io.Reader) error {
				switch filepath.Base(name) {
				case "pg_restore":
					close(started)
					<-ctx.Done()
					return ctx.Err()
				case "dropdb":
					if ctx.Err() != nil {
						close(cleaned)
						return fmt.Errorf("cleanup inherited interrupted context: %w", ctx.Err())
					}
					close(cleaned)
				}
				return nil
			}
		})
		payload := []byte("PGDMP")
		digest := sha256.Sum256(payload)
		resultCh := make(chan error, 1)
		go func() {
			_, err := fixture.client.Restore(context.Background(), RestoreRequest{
				Kind: KindOperational, Database: OperationalPrefix + "interrupted", DumpSize: int64(len(payload)),
				DumpSHA256: fmt.Sprintf("%x", digest[:]), Dump: bytes.NewReader(payload),
			})
			resultCh <- err
		}()
		<-started
		fixture.cancel()
		select {
		case <-cleaned:
		case <-time.After(5 * time.Second):
			t.Fatal("interrupted restore did not attempt cleanup")
		}
		if err := <-resultCh; err == nil {
			t.Fatal("interrupted restore reported success")
		}
	})

	t.Run("exact_drop_failure", func(t *testing.T) {
		fixture := startAuthorityFixture(t, func(_ *ServerConfig, executor *recordingExecutor) {
			executor.run = func(_ context.Context, name string, _ []string, _ io.Reader) error {
				if filepath.Base(name) == "dropdb" {
					return errors.New("drop failure")
				}
				return nil
			}
		})
		result, err := fixture.client.Drop(context.Background(), DropRequest{Kind: KindProvenance, Database: ProvenancePrefix + "drop_failure"})
		var authorityErr *AuthorityError
		if err == nil || !result.CleanupAttempted || result.CleanupSucceeded || !errors.As(err, &authorityErr) || authorityErr.Stage != FailureStageDatabaseDrop || authorityErr.Code != ErrorDatabaseDropFailed {
			t.Fatalf("drop result=%#v err=%v", result, err)
		}
	})
}

func TestRestoreAuthorityExecutionStagesReturnTypedFailureAndCleanupTruth(t *testing.T) {
	validTOC := []byte("; synthetic authenticated archive TOC\n1; 1259 1 TABLE public synthetic loom\n")
	tests := []struct {
		name    string
		mutate  func(*ServerConfig, *recordingExecutor)
		stage   FailureStage
		code    string
		cleanup bool
		success bool
	}{
		{
			name: "database_create", stage: FailureStageDatabaseCreate, code: ErrorDatabaseCreateFailed, cleanup: true, success: true,
			mutate: func(_ *ServerConfig, executor *recordingExecutor) {
				executor.run = func(_ context.Context, name string, _ []string, _ io.Reader) error {
					if filepath.Base(name) == "createdb" {
						return errors.New("secret create detail")
					}
					return nil
				}
			},
		},
		{
			name: "archive_inspection", stage: FailureStageArchiveInspection, code: ErrorArchiveInspectionFailed, cleanup: true, success: true,
			mutate: func(_ *ServerConfig, executor *recordingExecutor) {
				executor.inspect = func(context.Context, string, []string, io.Reader, int64) ([]byte, error) {
					return nil, errors.New("secret inspect detail")
				}
			},
		},
		{
			name: "archive_toc", stage: FailureStageArchiveTOC, code: ErrorArchiveTOCRefused, cleanup: true, success: true,
			mutate: func(_ *ServerConfig, executor *recordingExecutor) {
				executor.inspect = func(context.Context, string, []string, io.Reader, int64) ([]byte, error) {
					return []byte("malformed"), nil
				}
			},
		},
		{
			name: "archive_toc_publication", stage: FailureStageArchiveTOC, code: ErrorArchiveTOCUnavailable, cleanup: true, success: true,
			mutate: func(config *ServerConfig, executor *recordingExecutor) {
				tempDir := config.TempDir
				executor.inspect = func(_ context.Context, _ string, _ []string, stdin io.Reader, _ int64) ([]byte, error) {
					_, _ = io.Copy(io.Discard, stdin)
					if err := os.RemoveAll(tempDir); err != nil {
						return nil, err
					}
					return validTOC, nil
				}
			},
		},
		{
			name: "payload_rewind", stage: FailureStagePayload, code: ErrorPayloadUnavailable, cleanup: true, success: true,
			mutate: func(_ *ServerConfig, executor *recordingExecutor) {
				executor.inspect = func(_ context.Context, _ string, _ []string, stdin io.Reader, _ int64) ([]byte, error) {
					file := stdin.(*os.File)
					if err := file.Close(); err != nil {
						return nil, err
					}
					return validTOC, nil
				}
			},
		},
		{
			name: "database_restore", stage: FailureStageDatabaseRestore, code: ErrorDatabaseRestoreFailed, cleanup: true, success: true,
			mutate: func(_ *ServerConfig, executor *recordingExecutor) {
				executor.restore = func(context.Context, string, []string, io.Reader, *os.File) error {
					return errors.New("secret restore detail")
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := startAuthorityFixture(t, test.mutate)
			payload := []byte("PGDMP-typed-stage")
			digest := sha256.Sum256(payload)
			result, err := fixture.client.Restore(context.Background(), RestoreRequest{
				Kind: KindOperational, Database: OperationalPrefix + test.name,
				DumpSize: int64(len(payload)), DumpSHA256: fmt.Sprintf("%x", digest[:]), Dump: bytes.NewReader(payload),
			})
			var authorityErr *AuthorityError
			if err == nil || !errors.As(err, &authorityErr) || authorityErr.Stage != test.stage || authorityErr.Code != test.code || result.FailureStage != test.stage || result.ErrorCode != test.code || result.CleanupAttempted != test.cleanup || result.CleanupSucceeded != test.success {
				t.Fatalf("typed stage result=%#v error=%#v raw=%v", result, authorityErr, err)
			}
			if strings.Contains(err.Error(), "secret") {
				t.Fatalf("typed stage leaked executor detail: %v", err)
			}
		})
	}
}

func TestRestoreAuthorityClientCancellationSerializesDetachedCleanupAndExactDrop(t *testing.T) {
	target := OperationalPrefix + "client_cancel"
	other := OperationalPrefix + "other"
	activeOperational := "loom_main"
	activeProvenance := "loom_provenance"
	databaseExists := map[string]bool{
		other: true, activeOperational: true, activeProvenance: true,
	}
	var stateMu sync.Mutex
	var events []string
	started := make(chan struct{})
	fixture := startAuthorityFixture(t, func(_ *ServerConfig, executor *recordingExecutor) {
		executor.run = func(ctx context.Context, name string, args []string, _ io.Reader) error {
			database := args[len(args)-1]
			switch filepath.Base(name) {
			case "createdb":
				stateMu.Lock()
				databaseExists[database] = true
				events = append(events, "created")
				stateMu.Unlock()
			case "pg_restore":
				close(started)
				<-ctx.Done()
				stateMu.Lock()
				events = append(events, "restore_cancelled")
				stateMu.Unlock()
				return ctx.Err()
			case "dropdb":
				if ctx.Err() != nil {
					return fmt.Errorf("drop inherited cancelled request context: %w", ctx.Err())
				}
				stateMu.Lock()
				databaseExists[database] = false
				events = append(events, "dropped")
				stateMu.Unlock()
			}
			return nil
		}
	})

	payload := []byte("PGDMP-client-cancel")
	digest := sha256.Sum256(payload)
	restoreCtx, cancelRestore := context.WithCancel(context.Background())
	type restoreOutcome struct {
		result Result
		err    error
	}
	restoreDone := make(chan restoreOutcome, 1)
	go func() {
		result, err := fixture.client.Restore(restoreCtx, RestoreRequest{
			Kind: KindOperational, Database: target, DumpSize: int64(len(payload)),
			DumpSHA256: fmt.Sprintf("%x", digest[:]), Dump: bytes.NewReader(payload),
		})
		restoreDone <- restoreOutcome{result: result, err: err}
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("pg_restore did not start")
	}

	dropDone := make(chan error, 1)
	go func() {
		_, err := fixture.client.Drop(context.Background(), DropRequest{Kind: KindOperational, Database: target})
		dropDone <- err
	}()
	waitDeadline := time.Now().Add(5 * time.Second)
	for {
		fixture.server.locks.mu.Lock()
		lock := fixture.server.locks.targets[target]
		waiting := lock != nil && lock.refs == 2
		fixture.server.locks.mu.Unlock()
		if waiting {
			break
		}
		if time.Now().After(waitDeadline) {
			t.Fatal("concurrent typed drop did not reach the per-target serialization boundary")
		}
		time.Sleep(time.Millisecond)
	}
	stateMu.Lock()
	if got := append([]string(nil), events...); len(got) != 1 || got[0] != "created" {
		stateMu.Unlock()
		t.Fatalf("concurrent drop passed the live restore lock: events=%v", got)
	}
	stateMu.Unlock()
	cancelRestore()

	var outcome restoreOutcome
	select {
	case outcome = <-restoreDone:
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled client did not receive cleanup truth")
	}
	if !errors.Is(outcome.err, context.Canceled) || !outcome.result.CleanupAttempted || !outcome.result.CleanupSucceeded {
		t.Fatalf("cancelled restore result=%#v err=%v", outcome.result, outcome.err)
	}
	select {
	case err := <-dropDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("serialized exact drop did not complete")
	}

	stateMu.Lock()
	defer stateMu.Unlock()
	if databaseExists[target] || !databaseExists[other] || !databaseExists[activeOperational] || !databaseExists[activeProvenance] {
		t.Fatalf("database state escaped exact target: %#v", databaseExists)
	}
	wantEvents := []string{"created", "restore_cancelled", "dropped", "dropped"}
	if strings.Join(events, ",") != strings.Join(wantEvents, ",") {
		t.Fatalf("lifecycle events=%v want=%v", events, wantEvents)
	}
}

func TestRestoreAuthorityDisconnectCancelsAndCleansExactTarget(t *testing.T) {
	target := OperationalPrefix + "disconnect"
	started := make(chan struct{})
	cleaned := make(chan struct{})
	fixture := startAuthorityFixture(t, func(_ *ServerConfig, executor *recordingExecutor) {
		executor.run = func(ctx context.Context, name string, _ []string, _ io.Reader) error {
			switch filepath.Base(name) {
			case "pg_restore":
				close(started)
				<-ctx.Done()
				return ctx.Err()
			case "dropdb":
				if ctx.Err() != nil {
					return ctx.Err()
				}
				close(cleaned)
			}
			return nil
		}
	})
	payload := []byte("PGDMP-disconnect")
	digest := sha256.Sum256(payload)
	header := requestHeader{
		Schema: ProtocolSchema, Operation: OperationRestoreOperational, Kind: KindOperational,
		Database: target, DumpSize: int64(len(payload)), DumpSHA256: fmt.Sprintf("%x", digest[:]),
	}
	connection, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: fixture.path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	if err := encodeFrame(connection, header, maxProtocolHeaderBytes); err != nil {
		t.Fatal(err)
	}
	if _, err := connection.Write(payload); err != nil {
		t.Fatal(err)
	}
	if err := encodeFrame(connection, requestTrailer{Schema: ProtocolSchema, Stage: "request_complete", Operation: header.Operation, Kind: header.Kind, Database: header.Database}, maxProtocolControlBytes); err != nil {
		t.Fatal(err)
	}
	var accepted responseHeader
	if err := decodeFrame(connection, &accepted, maxProtocolResponseBytes); err != nil || validateAcceptedHeader(accepted, header) != nil {
		t.Fatalf("acknowledgment=%#v err=%v", accepted, err)
	}
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("pg_restore did not start")
	}
	if err := connection.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-cleaned:
	case <-time.After(5 * time.Second):
		t.Fatal("disconnect did not cancel and clean the target")
	}
}

func TestRestoreAuthoritySuccessKeepsTargetUntilExplicitTypedDrop(t *testing.T) {
	target := OperationalPrefix + "verify_then_drop"
	other := OperationalPrefix + "untouched"
	exists := map[string]bool{other: true, "loom_main": true, "loom_provenance": true}
	var stateMu sync.Mutex
	fixture := startAuthorityFixture(t, func(_ *ServerConfig, executor *recordingExecutor) {
		executor.run = func(_ context.Context, name string, args []string, _ io.Reader) error {
			database := args[len(args)-1]
			stateMu.Lock()
			defer stateMu.Unlock()
			switch filepath.Base(name) {
			case "createdb":
				exists[database] = true
			case "dropdb":
				exists[database] = false
			}
			return nil
		}
	})
	payload := []byte("PGDMP-success")
	digest := sha256.Sum256(payload)
	result, err := fixture.client.Restore(context.Background(), RestoreRequest{
		Kind: KindOperational, Database: target, DumpSize: int64(len(payload)),
		DumpSHA256: fmt.Sprintf("%x", digest[:]), Dump: bytes.NewReader(payload),
	})
	if err != nil || result.Status != "succeeded" {
		t.Fatalf("restore result=%#v err=%v", result, err)
	}
	stateMu.Lock()
	availableForVerification := exists[target]
	stateMu.Unlock()
	if !availableForVerification {
		t.Fatal("successful restore did not leave target available for verification")
	}
	if _, err := fixture.client.Drop(context.Background(), DropRequest{Kind: KindOperational, Database: target}); err != nil {
		t.Fatal(err)
	}
	stateMu.Lock()
	defer stateMu.Unlock()
	if exists[target] || !exists[other] || !exists["loom_main"] || !exists["loom_provenance"] {
		t.Fatalf("explicit drop escaped target: %#v", exists)
	}
}
