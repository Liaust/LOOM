package restoreauthority

import (
	"context"
	"fmt"
	"io"
	"regexp"
	"strings"
)

const (
	ProtocolSchema           = "loom.restore_authority.v1"
	DefaultSocketPath        = "/run/loom-restore-authority/restore-authority.sock"
	DefaultPostgresSocketDir = "/run/postgresql"
	DefaultPostgresPort      = uint16(5432)
	DefaultMaxDumpBytes      = int64(64 << 30)
	MaxDatabaseNameBytes     = 63
	OperationalPrefix        = "loom_restore_drill_"
	ProvenancePrefix         = "loom_provenance_restore_drill_"
	maxProtocolHeaderBytes   = 4096
	maxProtocolControlBytes  = 4096
	maxProtocolResponseBytes = 4096
)

type Kind string

const (
	KindOperational Kind = "operational"
	KindProvenance  Kind = "provenance"
)

type Operation string

const (
	OperationRestoreOperational Operation = "restore_operational"
	OperationRestoreProvenance  Operation = "restore_provenance"
	OperationDropDisposable     Operation = "drop_disposable"
)

// FailureStage is the closed, protocol-stable point at which restore authority
// processing failed. Values are safe to persist in restore failure evidence.
type FailureStage string

const (
	FailureStageSocket            FailureStage = "socket"
	FailureStagePeer              FailureStage = "peer"
	FailureStageSend              FailureStage = "send"
	FailureStageReceive           FailureStage = "receive"
	FailureStageRequest           FailureStage = "request"
	FailureStagePayload           FailureStage = "payload"
	FailureStageCancellation      FailureStage = "cancellation"
	FailureStageDatabaseCreate    FailureStage = "database_create"
	FailureStageArchiveInspection FailureStage = "archive_inspection"
	FailureStageArchiveTOC        FailureStage = "archive_toc"
	FailureStageDatabaseRestore   FailureStage = "database_restore"
	FailureStageDatabaseCleanup   FailureStage = "database_cleanup"
	FailureStageDatabaseDrop      FailureStage = "database_drop"
)

const (
	ErrorRequestRefused                  = "request_refused"
	ErrorSocketIdentityInvalid           = "socket_identity_invalid"
	ErrorPeerIdentityInvalid             = "peer_identity_invalid"
	ErrorProtocolInvalid                 = "protocol_invalid"
	ErrorRequestInvalid                  = "request_invalid"
	ErrorDatabaseRefused                 = "database_refused"
	ErrorPayloadInvalid                  = "payload_invalid"
	ErrorRequestCancelled                = "request_cancelled"
	ErrorDatabaseCreateFailed            = "database_create_failed"
	ErrorDatabaseCreateAndCleanupFailed  = "database_create_and_cleanup_failed"
	ErrorPayloadUnavailable              = "payload_unavailable"
	ErrorArchiveInspectionFailed         = "archive_inspection_failed"
	ErrorArchiveTOCRefused               = "archive_toc_refused"
	ErrorArchiveTOCUnavailable           = "archive_toc_unavailable"
	ErrorDatabaseRestoreFailed           = "database_restore_failed"
	ErrorDatabaseRestoreAndCleanupFailed = "database_restore_and_cleanup_failed"
	ErrorDatabaseDropFailed              = "database_drop_failed"
	ErrorClientSocketInvalid             = "client_socket_invalid"
	ErrorClientConnectFailed             = "client_connect_failed"
	ErrorClientPeerInvalid               = "client_peer_invalid"
	ErrorClientSendFailed                = "client_send_failed"
	ErrorClientPayloadFailed             = "client_payload_failed"
	ErrorClientReceiveFailed             = "client_receive_failed"
	ErrorClientResponseInvalid           = "client_response_invalid"
	ErrorClientCancelled                 = "client_cancelled"
)

type failureDefinition struct {
	Stage   FailureStage
	Message string
}

var failureDefinitions = map[string]failureDefinition{
	ErrorRequestRefused:                  {FailureStageRequest, "Restore authority request was refused."},
	ErrorSocketIdentityInvalid:           {FailureStageSocket, "Restore authority socket identity was refused."},
	ErrorPeerIdentityInvalid:             {FailureStagePeer, "Restore authority peer identity was refused."},
	ErrorProtocolInvalid:                 {FailureStageRequest, "Restore authority protocol was refused."},
	ErrorRequestInvalid:                  {FailureStageRequest, "Restore authority request was invalid."},
	ErrorDatabaseRefused:                 {FailureStageRequest, "Disposable database target was refused."},
	ErrorPayloadInvalid:                  {FailureStagePayload, "Authenticated restore payload was not received exactly."},
	ErrorRequestCancelled:                {FailureStageCancellation, "Restore authority request was cancelled."},
	ErrorDatabaseCreateFailed:            {FailureStageDatabaseCreate, "Disposable database creation failed."},
	ErrorDatabaseCreateAndCleanupFailed:  {FailureStageDatabaseCleanup, "Disposable database creation and cleanup failed."},
	ErrorPayloadUnavailable:              {FailureStagePayload, "Authenticated restore payload became unavailable."},
	ErrorArchiveInspectionFailed:         {FailureStageArchiveInspection, "Authenticated restore archive inspection failed."},
	ErrorArchiveTOCRefused:               {FailureStageArchiveTOC, "Authenticated restore archive table of contents was refused."},
	ErrorArchiveTOCUnavailable:           {FailureStageArchiveTOC, "Authenticated restore archive table of contents became unavailable."},
	ErrorDatabaseRestoreFailed:           {FailureStageDatabaseRestore, "Disposable database restore failed."},
	ErrorDatabaseRestoreAndCleanupFailed: {FailureStageDatabaseCleanup, "Disposable database restore and cleanup failed."},
	ErrorDatabaseDropFailed:              {FailureStageDatabaseDrop, "Disposable database cleanup failed."},
	ErrorClientSocketInvalid:             {FailureStageSocket, "Restore authority socket validation failed."},
	ErrorClientConnectFailed:             {FailureStageSocket, "Restore authority connection failed."},
	ErrorClientPeerInvalid:               {FailureStagePeer, "Restore authority peer validation failed."},
	ErrorClientSendFailed:                {FailureStageSend, "Restore authority request transmission failed."},
	ErrorClientPayloadFailed:             {FailureStageSend, "Restore authority payload transmission failed."},
	ErrorClientReceiveFailed:             {FailureStageReceive, "Restore authority response was unavailable."},
	ErrorClientResponseInvalid:           {FailureStageReceive, "Restore authority response was invalid."},
	ErrorClientCancelled:                 {FailureStageCancellation, "Restore authority request was cancelled."},
}

// ValidateFailure identifies the exact generic message for an allowed stage
// and stable code pair. Callers must not substitute raw runtime error text.
func ValidateFailure(stage FailureStage, code, message string) error {
	if err := ValidateFailurePair(stage, code); err != nil {
		return err
	}
	definition := failureDefinitions[code]
	if definition.Message != message {
		return fmt.Errorf("restore authority failure stage, code, or message is invalid")
	}
	return nil
}

// ValidateFailurePair permits downstream evidence to validate the closed
// stage/code contract without copying authority messages.
func ValidateFailurePair(stage FailureStage, code string) error {
	definition, ok := failureDefinitions[code]
	if !ok || definition.Stage != stage {
		return fmt.Errorf("restore authority failure stage or code is invalid")
	}
	return nil
}

func failureForCode(code string) (FailureStage, string, bool) {
	definition, ok := failureDefinitions[code]
	return definition.Stage, definition.Message, ok
}

type RestoreRequest struct {
	Kind       Kind
	Database   string
	DumpSize   int64
	DumpSHA256 string
	Dump       io.Reader
}

type DropRequest struct {
	Kind     Kind
	Database string
}

type Result struct {
	Status           string
	Kind             Kind
	Database         string
	FailureStage     FailureStage
	ErrorCode        string
	CleanupAttempted bool
	CleanupSucceeded bool
}

type RestoreDatabaseAuthority interface {
	Restore(context.Context, RestoreRequest) (Result, error)
	Drop(context.Context, DropRequest) (Result, error)
}

type DatabasePolicy struct {
	ActiveDatabase string
	Owner          string
}

var databaseIdentifierPattern = regexp.MustCompile(`^[a-z_][a-z0-9_]*$`)
var digestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

func ValidateDisposableDatabase(kind Kind, database string, activeDatabases ...string) error {
	database = strings.TrimSpace(database)
	if len(database) == 0 || len(database) > MaxDatabaseNameBytes || !databaseIdentifierPattern.MatchString(database) {
		return fmt.Errorf("invalid disposable database name")
	}
	prefix := ""
	switch kind {
	case KindOperational:
		prefix = OperationalPrefix
	case KindProvenance:
		prefix = ProvenancePrefix
	default:
		return fmt.Errorf("unsupported restore kind")
	}
	if !strings.HasPrefix(database, prefix) || len(database) == len(prefix) {
		return fmt.Errorf("database is outside the disposable namespace")
	}
	for _, forbidden := range append([]string{"postgres", "template0", "template1"}, activeDatabases...) {
		if database == strings.TrimSpace(forbidden) {
			return fmt.Errorf("database is active or system-owned")
		}
	}
	return nil
}

func validateDatabasePolicy(policy DatabasePolicy) error {
	active := strings.TrimSpace(policy.ActiveDatabase)
	owner := strings.TrimSpace(policy.Owner)
	if len(active) == 0 || len(active) > MaxDatabaseNameBytes || !databaseIdentifierPattern.MatchString(active) {
		return fmt.Errorf("invalid active database configuration")
	}
	if active == "postgres" || active == "template0" || active == "template1" {
		return fmt.Errorf("active database configuration names a system database")
	}
	if len(owner) == 0 || len(owner) > MaxDatabaseNameBytes || !databaseIdentifierPattern.MatchString(owner) || owner == "postgres" {
		return fmt.Errorf("invalid database owner configuration")
	}
	return nil
}

func validateRestoreRequest(request RestoreRequest, maxDumpBytes int64) error {
	if err := ValidateDisposableDatabase(request.Kind, request.Database); err != nil {
		return err
	}
	if request.Dump == nil || request.DumpSize <= 0 || request.DumpSize > maxDumpBytes {
		return fmt.Errorf("invalid bounded restore payload")
	}
	if !digestPattern.MatchString(request.DumpSHA256) {
		return fmt.Errorf("invalid restore payload digest")
	}
	return nil
}
