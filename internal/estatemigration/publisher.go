package estatemigration

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/sys/unix"
	"loom.local/loom/internal/filesystemmeta"
)

const (
	acceptanceRecordSchemaVersion = "loom.digital_estate_publication_record.v1"
	publicationControlDirectory   = ".loom-migrations"
	maxAcceptanceRecordBytes      = 4 * 1024 * 1024
	maxSymlinkTargetBytes         = 64 * 1024

	publicationStatePrepared  = "prepared"
	publicationStatePublished = "published_pending_acceptance"
	publicationStateUncertain = "publication_uncertain"
)

type StageRequest struct {
	// StagingPath is an adapter-only path under the already-bound disposable or
	// canonical domain root. It is never serialized or returned.
	StagingPath    string            `json:"-"`
	StagingLocator string            `json:"staging_locator"`
	Manifest       MigrationManifest `json:"manifest"`
}

// Stager is the narrow transport boundary. Implementations may populate only
// the supplied empty staging directory and must treat every source as read-only.
// Existing Lane file-tree or bundle mechanics can satisfy this interface;
// this package intentionally does not implement a second transfer engine.
type Stager interface {
	Stage(context.Context, StageRequest) error
}

type StageFunc func(context.Context, StageRequest) error

func (function StageFunc) Stage(ctx context.Context, request StageRequest) error {
	return function(ctx, request)
}

type SpaceProbe interface {
	AvailableBytes(context.Context, string) (uint64, error)
}

type SpaceProbeFunc func(context.Context, string) (uint64, error)

func (function SpaceProbeFunc) AvailableBytes(ctx context.Context, root string) (uint64, error) {
	return function(ctx, root)
}

type OSSpaceProbe struct{}

func (OSSpaceProbe) AvailableBytes(ctx context.Context, root string) (uint64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	var status unix.Statfs_t
	if err := unix.Statfs(root, &status); err != nil {
		return 0, err
	}
	if status.Bsize <= 0 {
		return 0, ErrMetadataOverflow
	}
	blockSize := uint64(status.Bsize)
	availableBlocks := uint64(status.Bavail)
	if availableBlocks > math.MaxUint64/blockSize {
		return 0, ErrMetadataOverflow
	}
	return availableBlocks * blockSize, nil
}

type Publisher struct {
	Stager     Stager
	SpaceProbe SpaceProbe
	hooks      *publicationTestHooks
}

type PublicationInput struct {
	DomainRoot             string `json:"-"`
	AttemptID              string `json:"attempt_id"`
	ReviewedManifestDigest string `json:"reviewed_manifest_digest"`
	Confirmed              bool   `json:"confirmed"`
}

type PublicationResult struct {
	Status         string                `json:"status"`
	ManifestID     string                `json:"manifest_id"`
	ManifestDigest string                `json:"manifest_digest"`
	Destination    ManifestDestination   `json:"destination"`
	StagingLocator string                `json:"staging_locator,omitempty"`
	Acceptance     *AcceptanceRecord     `json:"acceptance_record,omitempty"`
	Rollback       []RollbackInstruction `json:"rollback"`
	Idempotent     bool                  `json:"idempotent"`
	Recovered      bool                  `json:"recovered"`
}

type AcceptanceRecord struct {
	SchemaVersion              string                `json:"schema_version"`
	RecordDigest               string                `json:"record_digest"`
	ManifestID                 string                `json:"manifest_id"`
	ManifestDigest             string                `json:"manifest_digest"`
	InventoryDigest            string                `json:"inventory_digest"`
	SourceDigest               string                `json:"source_digest"`
	Destination                ManifestDestination   `json:"destination"`
	State                      string                `json:"state"`
	StageName                  string                `json:"stage_name"`
	StagingLocator             string                `json:"staging_locator"`
	StageObjectDigest          string                `json:"stage_object_digest"`
	Verification               StageVerification     `json:"verification"`
	CompletedGates             []string              `json:"completed_gates"`
	PendingGates               []string              `json:"pending_gates"`
	CanonicalCustodyAccepted   bool                  `json:"canonical_custody_accepted"`
	ORCARegistrationAuthorized bool                  `json:"orca_registration_authorized"`
	SourceDeletionAuthorized   bool                  `json:"source_deletion_authorized"`
	Rollback                   []RollbackInstruction `json:"rollback"`
}

type StageVerification struct {
	SourceDigest     string `json:"source_digest"`
	EntryCount       int    `json:"entry_count"`
	RegularFileCount int    `json:"regular_file_count"`
	DirectoryCount   int    `json:"directory_count"`
	SymlinkCount     int    `json:"symlink_count"`
	LogicalBytes     uint64 `json:"logical_bytes"`
	RootMode         uint32 `json:"root_mode"`
}

type publicationTestHooks struct {
	afterPreparedRecord func() error
	afterPromote        func() error
}

type publicationOperationError struct {
	operation string
	cause     error
}

func (err *publicationOperationError) Error() string {
	return "digital estate publication: " + err.operation
}

func (err *publicationOperationError) Unwrap() error { return err.cause }

// Publish creates a unique stage under the destination domain, invokes the
// injected stager, verifies every staged entry without following symlinks, and
// promotes with an atomic no-replace rename. An identical manifest can recover
// or replay; a changed manifest for the same destination fails closed.
func (publisher Publisher) Publish(ctx context.Context, manifest MigrationManifest, input PublicationInput) (PublicationResult, error) {
	result := PublicationResult{
		ManifestID:     manifest.ManifestID,
		ManifestDigest: manifest.ManifestDigest,
		Destination:    manifest.Destination,
		Rollback:       append([]RollbackInstruction{}, manifest.Rollback...),
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if err := ValidateManifest(manifest); err != nil {
		return result, err
	}
	if !input.Confirmed || input.ReviewedManifestDigest == "" || input.ReviewedManifestDigest != manifest.ManifestDigest {
		return result, fmt.Errorf("%w: exact reviewed digest and confirmation are required", ErrInvalidManifest)
	}
	if !validAttemptID(input.AttemptID) {
		return result, fmt.Errorf("%w: attempt id is invalid", ErrInvalidManifest)
	}
	bound, err := openBoundPublicationRoot(input.DomainRoot)
	if err != nil {
		return result, redactedPublicationError("open destination domain", err)
	}
	defer bound.Close()
	if err := bound.verify(); err != nil {
		return result, redactedPublicationError("verify destination domain", err)
	}

	destinationPresent, _, err := inspectNamed(bound.fd(), manifest.Destination.Name)
	if err != nil {
		return result, redactedPublicationError("inspect destination", err)
	}
	control, controlPresent, err := openPublicationControl(bound, !destinationPresent)
	if err != nil {
		return result, redactedPublicationError("open publication control", err)
	}
	if !controlPresent {
		return result, ErrDestinationExists
	}
	defer control.Close()
	if err := control.verify(); err != nil {
		return result, redactedPublicationError("verify publication control binding", err)
	}
	lock, err := lockPublication(ctx, control.fd(), manifest.Destination.IdentityDigest)
	if err != nil {
		return result, redactedPublicationError("lock destination publication", err)
	}
	defer lock.Close()
	if err := control.verify(); err != nil {
		return result, redactedPublicationError("reverify publication control after lock", err)
	}

	recordName := acceptanceRecordName(manifest.Destination.IdentityDigest)
	record, recordPresent, err := readAcceptanceRecordAt(control.fd(), recordName)
	if err != nil {
		return result, redactedPublicationError("read acceptance record", err)
	}
	if err := control.verify(); err != nil {
		return result, redactedPublicationError("reverify publication control after record read", err)
	}
	destinationPresent, _, err = inspectNamed(bound.fd(), manifest.Destination.Name)
	if err != nil {
		return result, redactedPublicationError("reinspect destination", err)
	}
	if recordPresent {
		return publisher.resumePublication(ctx, bound, control, manifest, input, recordName, record, destinationPresent, result)
	}
	if destinationPresent {
		return result, ErrDestinationExists
	}
	if publisher.Stager == nil {
		return result, fmt.Errorf("%w: staging adapter is required", ErrInvalidManifest)
	}
	probe := publisher.SpaceProbe
	if probe == nil {
		probe = OSSpaceProbe{}
	}
	available, err := probe.AvailableBytes(ctx, bound.path)
	if err != nil {
		return result, redactedPublicationError("recheck destination space", err)
	}
	if available < manifest.Space.RequiredBytes {
		return result, ErrInsufficientSpace
	}

	stageName := "stage-" + manifest.ManifestID + "-" + input.AttemptID
	stagingLocator := "migration://" + manifest.ManifestID + "/staging/" + input.AttemptID
	result.StagingLocator = stagingLocator
	if err := control.verify(); err != nil {
		return result, redactedPublicationError("verify publication binding before staging", err)
	}
	if err := unix.Mkdirat(control.fd(), stageName, 0o700); err != nil {
		return result, redactedPublicationError("create unique staging root", err)
	}
	if err := control.verify(); err != nil {
		return result, redactedPublicationError("reverify publication binding after staging root creation", err)
	}
	stage, stageIdentity, err := openNamedDirectory(control.fd(), stageName)
	if err != nil {
		return result, redactedPublicationError("bind unique staging root", err)
	}
	defer stage.Close()
	stagePath := filepath.Join(bound.path, publicationControlDirectory, stageName)
	if err := control.verify(); err != nil {
		return result, redactedPublicationError("verify publication binding before staging payload", err)
	}
	if err := publisher.Stager.Stage(ctx, StageRequest{StagingPath: stagePath, StagingLocator: stagingLocator, Manifest: manifest}); err != nil {
		return result, redactedPublicationError("stage reviewed payload", err)
	}
	if err := control.verify(); err != nil {
		return result, redactedPublicationError("reverify publication binding after staging payload", err)
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if err := control.verify(); err != nil {
		return result, redactedPublicationError("verify publication binding before staged mode", err)
	}
	if err := unix.Fchmod(int(stage.Fd()), manifest.Destination.Mode); err != nil {
		return result, redactedPublicationError("set staged root mode", err)
	}
	if err := control.verify(); err != nil {
		return result, redactedPublicationError("reverify publication binding after staged mode", err)
	}
	if err := verifyNamedIdentity(control.fd(), stageName, stageIdentity); err != nil {
		return result, redactedPublicationError("verify staging root binding", err)
	}
	if err := control.verify(); err != nil {
		return result, redactedPublicationError("verify publication binding before staged verification", err)
	}
	verification, err := verifyStagedTree(ctx, int(stage.Fd()), manifest)
	if err != nil {
		return result, redactedPublicationError("verify staged payload", errors.Join(ErrVerificationFailed, err))
	}
	if err := control.verify(); err != nil {
		return result, redactedPublicationError("reverify publication binding after staged verification", err)
	}
	record = newAcceptanceRecord(manifest, publicationStatePrepared, stageName, stagingLocator, stageIdentity.digest(), verification)
	if err := control.verify(); err != nil {
		return result, redactedPublicationError("verify publication binding before prepared record", err)
	}
	if err := writeAcceptanceRecordExclusiveAt(control.fd(), recordName, record); err != nil {
		return result, redactedPublicationError("write prepared acceptance record", err)
	}
	if err := control.verify(); err != nil {
		return result, redactedPublicationError("reverify publication binding after prepared record", err)
	}
	result.Acceptance = &record
	result.Status = publicationStatePrepared
	if publisher.hooks != nil && publisher.hooks.afterPreparedRecord != nil {
		if err := publisher.hooks.afterPreparedRecord(); err != nil {
			return result, redactedPublicationError("run prepared-publication hook", err)
		}
	}
	if err := control.verify(); err != nil {
		return result, redactedPublicationError("reverify publication binding after prepared hook", err)
	}
	if err := verifyNamedIdentity(control.fd(), stageName, stageIdentity); err != nil {
		return result, redactedPublicationError("reverify staging root binding", err)
	}
	if err := control.verify(); err != nil {
		return result, redactedPublicationError("verify publication binding before final staged verification", err)
	}
	verificationAgain, err := verifyStagedTree(ctx, int(stage.Fd()), manifest)
	if err != nil || verificationAgain != verification {
		return result, redactedPublicationError("reverify staged payload", errors.Join(ErrVerificationFailed, err))
	}
	if err := control.verify(); err != nil {
		return result, redactedPublicationError("reverify publication binding after final staged verification", err)
	}
	if err := verifyNamedIdentity(control.fd(), stageName, stageIdentity); err != nil {
		return result, redactedPublicationError("reverify staged root binding", err)
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if present, _, inspectErr := inspectNamed(bound.fd(), manifest.Destination.Name); inspectErr != nil {
		return result, redactedPublicationError("reinspect absent destination", inspectErr)
	} else if present {
		return result, ErrDestinationExists
	}
	if err := control.verify(); err != nil {
		return result, redactedPublicationError("verify publication binding before staging sync", err)
	}
	if err := unix.Fsync(int(stage.Fd())); err != nil {
		return result, redactedPublicationError("sync staged payload directory", err)
	}
	if err := unix.Fsync(control.fd()); err != nil {
		return result, redactedPublicationError("sync publication control", err)
	}
	if err := control.verify(); err != nil {
		return result, redactedPublicationError("reverify publication binding after staging sync", err)
	}
	if err := verifyNamedIdentity(control.fd(), stageName, stageIdentity); err != nil {
		return result, redactedPublicationError("verify staging root before atomic publication", err)
	}
	if err := control.verify(); err != nil {
		return result, redactedPublicationError("verify publication binding before atomic publication", err)
	}
	if err := renameNoReplaceAt(control.fd(), stageName, bound.fd(), manifest.Destination.Name); err != nil {
		if errors.Is(err, unix.EEXIST) || errors.Is(err, unix.ENOTEMPTY) {
			return result, ErrDestinationExists
		}
		return result, redactedPublicationError("atomically publish absent destination", err)
	}
	result.Status = publicationStateUncertain
	if err := control.verify(); err != nil {
		return result, redactedPublicationError("reverify publication binding after atomic publication", errors.Join(ErrPublicationUncertain, err))
	}
	if err := unix.Fsync(bound.fd()); err != nil {
		return result, redactedPublicationError("sync published destination", errors.Join(ErrPublicationUncertain, err))
	}
	if err := control.verify(); err != nil {
		return result, redactedPublicationError("reverify publication binding after destination sync", errors.Join(ErrPublicationUncertain, err))
	}
	if publisher.hooks != nil && publisher.hooks.afterPromote != nil {
		if err := publisher.hooks.afterPromote(); err != nil {
			return result, redactedPublicationError("finalize publication record", errors.Join(ErrPublicationUncertain, err))
		}
	}
	finished, err := publisher.finishPublished(ctx, bound, control, manifest, input, recordName, record, result, false)
	if err != nil {
		finished.Status = publicationStateUncertain
		return finished, redactedPublicationError("finalize published state", errors.Join(ErrPublicationUncertain, err))
	}
	return finished, nil
}

func (publisher Publisher) resumePublication(ctx context.Context, bound *boundPublicationRoot, control *boundPublicationControl, manifest MigrationManifest, input PublicationInput, recordName string, record AcceptanceRecord, destinationPresent bool, result PublicationResult) (PublicationResult, error) {
	result.StagingLocator = record.StagingLocator
	result.Acceptance = &record
	if err := control.verify(); err != nil {
		return result, redactedPublicationError("verify publication binding before recovery", err)
	}
	if record.ManifestDigest != manifest.ManifestDigest || record.ManifestID != manifest.ManifestID || record.Destination != manifest.Destination {
		return result, ErrChangedReplay
	}
	stagePresent, stageIdentity, err := inspectNamed(control.fd(), record.StageName)
	if err != nil {
		return result, redactedPublicationError("inspect recorded staging root", err)
	}
	if destinationPresent {
		if stagePresent {
			return result, ErrPublicationUncertain
		}
		result.Recovered = record.State == publicationStatePrepared
		result.Status = publicationStateUncertain
		finished, err := publisher.finishPublished(ctx, bound, control, manifest, input, recordName, record, result, true)
		if err != nil {
			finished.Status = publicationStateUncertain
			return finished, redactedPublicationError("recover published state", errors.Join(ErrPublicationUncertain, err))
		}
		return finished, nil
	}
	if record.State == publicationStatePublished || !stagePresent || stageIdentity.kind != unix.S_IFDIR || stageIdentity.digest() != record.StageObjectDigest {
		return result, ErrPublicationUncertain
	}
	stage, openedIdentity, err := openNamedDirectory(control.fd(), record.StageName)
	if err != nil {
		return result, redactedPublicationError("open recorded staging root", err)
	}
	defer stage.Close()
	if openedIdentity != stageIdentity {
		return result, ErrPublicationUncertain
	}
	if err := control.verify(); err != nil {
		return result, redactedPublicationError("verify publication binding before recovered staged verification", err)
	}
	verification, err := verifyStagedTree(ctx, int(stage.Fd()), manifest)
	if err != nil || verification != record.Verification {
		return result, redactedPublicationError("verify recorded staging payload", errors.Join(ErrVerificationFailed, err))
	}
	if err := control.verify(); err != nil {
		return result, redactedPublicationError("reverify publication binding after recovered staged verification", err)
	}
	if err := verifyNamedIdentity(control.fd(), record.StageName, stageIdentity); err != nil {
		return result, redactedPublicationError("reverify recorded staging root binding", err)
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if present, _, inspectErr := inspectNamed(bound.fd(), manifest.Destination.Name); inspectErr != nil {
		return result, redactedPublicationError("reinspect absent destination", inspectErr)
	} else if present {
		return result, ErrDestinationExists
	}
	if err := control.verify(); err != nil {
		return result, redactedPublicationError("verify publication binding before recovered atomic publication", err)
	}
	if err := verifyNamedIdentity(control.fd(), record.StageName, stageIdentity); err != nil {
		return result, redactedPublicationError("verify recovered staging root before atomic publication", err)
	}
	if err := renameNoReplaceAt(control.fd(), record.StageName, bound.fd(), manifest.Destination.Name); err != nil {
		if errors.Is(err, unix.EEXIST) || errors.Is(err, unix.ENOTEMPTY) {
			return result, ErrDestinationExists
		}
		return result, redactedPublicationError("recover atomic publication", err)
	}
	result.Status = publicationStateUncertain
	if err := control.verify(); err != nil {
		return result, redactedPublicationError("reverify publication binding after recovered atomic publication", errors.Join(ErrPublicationUncertain, err))
	}
	if err := unix.Fsync(bound.fd()); err != nil {
		return result, redactedPublicationError("sync recovered destination", errors.Join(ErrPublicationUncertain, err))
	}
	if err := control.verify(); err != nil {
		return result, redactedPublicationError("reverify publication binding after recovered destination sync", errors.Join(ErrPublicationUncertain, err))
	}
	result.Recovered = true
	finished, err := publisher.finishPublished(ctx, bound, control, manifest, input, recordName, record, result, true)
	if err != nil {
		finished.Status = publicationStateUncertain
		return finished, redactedPublicationError("finalize recovered publication", errors.Join(ErrPublicationUncertain, err))
	}
	return finished, nil
}

func (publisher Publisher) finishPublished(ctx context.Context, bound *boundPublicationRoot, control *boundPublicationControl, manifest MigrationManifest, input PublicationInput, recordName string, record AcceptanceRecord, result PublicationResult, replay bool) (PublicationResult, error) {
	if err := control.verify(); err != nil {
		return result, redactedPublicationError("verify publication binding before finalization", err)
	}
	target, targetIdentity, err := openNamedDirectory(bound.fd(), manifest.Destination.Name)
	if err != nil {
		return result, redactedPublicationError("bind published destination", err)
	}
	defer target.Close()
	if targetIdentity.digest() != record.StageObjectDigest {
		return result, ErrPublicationUncertain
	}
	if err := control.verify(); err != nil {
		return result, redactedPublicationError("verify publication binding before published verification", err)
	}
	verification, err := verifyStagedTree(ctx, int(target.Fd()), manifest)
	if err != nil || verification != record.Verification {
		return result, redactedPublicationError("verify published destination", errors.Join(ErrVerificationFailed, err))
	}
	if err := control.verify(); err != nil {
		return result, redactedPublicationError("reverify publication binding after published verification", err)
	}
	if err := verifyNamedIdentity(bound.fd(), manifest.Destination.Name, targetIdentity); err != nil {
		return result, redactedPublicationError("reverify published destination binding", err)
	}
	if err := control.verify(); err != nil {
		return result, redactedPublicationError("verify publication binding before final acceptance record", err)
	}
	published := newAcceptanceRecord(manifest, publicationStatePublished, record.StageName, record.StagingLocator, record.StageObjectDigest, verification)
	if record.State != publicationStatePublished || !reflect.DeepEqual(record, published) {
		if err := replaceAcceptanceRecordAt(control.fd(), recordName, published); err != nil {
			return result, redactedPublicationError("commit published acceptance record", err)
		}
	}
	if err := control.verify(); err != nil {
		return result, redactedPublicationError("reverify publication binding after final acceptance record", err)
	}
	if err := verifyNamedIdentity(bound.fd(), manifest.Destination.Name, targetIdentity); err != nil {
		return result, redactedPublicationError("reverify published destination before success", err)
	}
	if err := control.verify(); err != nil {
		return result, redactedPublicationError("verify publication binding before success", err)
	}
	result.Status = publicationStatePublished
	result.Acceptance = &published
	result.Idempotent = replay
	return result, nil
}

func newAcceptanceRecord(manifest MigrationManifest, state, stageName, stagingLocator, stageObjectDigest string, verification StageVerification) AcceptanceRecord {
	completed, pending := publicationGates(state)
	record := AcceptanceRecord{
		SchemaVersion:              acceptanceRecordSchemaVersion,
		ManifestID:                 manifest.ManifestID,
		ManifestDigest:             manifest.ManifestDigest,
		InventoryDigest:            manifest.InventoryDigest,
		SourceDigest:               manifest.SourceDigest,
		Destination:                manifest.Destination,
		State:                      state,
		StageName:                  stageName,
		StagingLocator:             stagingLocator,
		StageObjectDigest:          stageObjectDigest,
		Verification:               verification,
		CompletedGates:             completed,
		PendingGates:               pending,
		CanonicalCustodyAccepted:   false,
		ORCARegistrationAuthorized: false,
		SourceDeletionAuthorized:   false,
		Rollback:                   append([]RollbackInstruction{}, manifest.Rollback...),
	}
	record.RecordDigest, _ = acceptanceRecordDigest(record)
	return record
}

func publicationGates(state string) ([]string, []string) {
	completed := []string{"entry_fidelity", "space_recheck", "staged_content_digest"}
	if state == publicationStatePublished {
		completed = append(completed, "absent_destination", "atomic_no_replace_publication")
	}
	sort.Strings(completed)
	completeSet := map[string]struct{}{}
	for _, gate := range completed {
		completeSet[gate] = struct{}{}
	}
	pending := []string{}
	for _, gate := range requiredMigrationGates() {
		if _, done := completeSet[gate]; !done {
			pending = append(pending, gate)
		}
	}
	return completed, pending
}

func acceptanceRecordDigest(record AcceptanceRecord) (string, error) {
	record.RecordDigest = ""
	payload, err := json.Marshal(record)
	if err != nil {
		return "", err
	}
	return digestBytes(payload), nil
}

func MarshalAcceptanceRecord(record AcceptanceRecord) ([]byte, error) {
	if err := validateAcceptanceRecord(record); err != nil {
		return nil, err
	}
	payload, err := json.Marshal(record)
	if err != nil {
		return nil, err
	}
	if len(payload) > maxAcceptanceRecordBytes {
		return nil, budgetError("acceptance record bytes", maxAcceptanceRecordBytes)
	}
	return append(payload, '\n'), nil
}

func validateAcceptanceRecord(record AcceptanceRecord) error {
	if record.SchemaVersion != acceptanceRecordSchemaVersion || !validManifestID(record.ManifestID) || !validCanonicalDigest(record.RecordDigest) || !validCanonicalDigest(record.ManifestDigest) || !validCanonicalDigest(record.InventoryDigest) || !validCanonicalDigest(record.SourceDigest) || !validCanonicalDigest(record.StageObjectDigest) || !validRecordText(record) {
		return ErrInvalidManifest
	}
	expectedDestination, err := normalizeDestination(DestinationSpec{Domain: record.Destination.Domain, Name: record.Destination.Name, Mode: record.Destination.Mode})
	if err != nil || expectedDestination != record.Destination || !reflect.DeepEqual(record.Rollback, rollbackInstructions(record.Destination)) {
		return ErrInvalidManifest
	}
	attemptID, ok := stageAttemptID(record.ManifestID, record.StageName)
	if !ok || record.StagingLocator != "migration://"+record.ManifestID+"/staging/"+attemptID {
		return ErrInvalidManifest
	}
	if record.State != publicationStatePrepared && record.State != publicationStatePublished || record.Verification.SourceDigest != record.SourceDigest || record.Verification.EntryCount < 0 || record.Verification.RegularFileCount < 0 || record.Verification.DirectoryCount < 0 || record.Verification.SymlinkCount < 0 || record.Verification.EntryCount != record.Verification.RegularFileCount+record.Verification.DirectoryCount+record.Verification.SymlinkCount || record.Verification.RootMode != record.Destination.Mode {
		return ErrInvalidManifest
	}
	completed, pending := publicationGates(record.State)
	if !reflect.DeepEqual(record.CompletedGates, completed) || !reflect.DeepEqual(record.PendingGates, pending) || record.CanonicalCustodyAccepted || record.ORCARegistrationAuthorized || record.SourceDeletionAuthorized {
		return ErrInvalidManifest
	}
	digest, err := acceptanceRecordDigest(record)
	if err != nil || digest != record.RecordDigest {
		return ErrInvalidManifest
	}
	return nil
}

func validRecordText(record AcceptanceRecord) bool {
	values := []string{record.SchemaVersion, record.RecordDigest, record.ManifestID, record.ManifestDigest, record.InventoryDigest, record.SourceDigest, record.State, record.StageName, record.StagingLocator, record.StageObjectDigest, string(record.Destination.Domain), record.Destination.Name, record.Destination.Locator, record.Destination.IdentityDigest}
	for _, value := range values {
		if !utf8.ValidString(value) {
			return false
		}
	}
	for _, list := range [][]string{record.CompletedGates, record.PendingGates} {
		for _, value := range list {
			if !utf8.ValidString(value) {
				return false
			}
		}
	}
	for _, instruction := range record.Rollback {
		if !utf8.ValidString(instruction.Phase) || !utf8.ValidString(instruction.Action) || !utf8.ValidString(instruction.Target) {
			return false
		}
		for _, condition := range instruction.RequiredConditions {
			if !utf8.ValidString(condition) {
				return false
			}
		}
	}
	return true
}

func readAcceptanceRecordAt(parentFD int, name string) (AcceptanceRecord, bool, error) {
	payload, present, err := readBoundedFileAt(parentFD, name, maxAcceptanceRecordBytes)
	if err != nil || !present {
		return AcceptanceRecord{}, present, err
	}
	if !utf8.Valid(payload) {
		return AcceptanceRecord{}, true, ErrInvalidManifest
	}
	var record AcceptanceRecord
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&record); err != nil {
		return AcceptanceRecord{}, true, ErrInvalidManifest
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return AcceptanceRecord{}, true, ErrInvalidManifest
	}
	if err := validateAcceptanceRecord(record); err != nil {
		return AcceptanceRecord{}, true, err
	}
	return record, true, nil
}

func writeAcceptanceRecordExclusiveAt(parentFD int, name string, record AcceptanceRecord) error {
	payload, err := MarshalAcceptanceRecord(record)
	if err != nil {
		return err
	}
	fd, err := unix.Openat(parentFD, name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return err
	}
	file := os.NewFile(uintptr(fd), name)
	_, writeErr := file.Write(payload)
	if writeErr == nil {
		writeErr = file.Sync()
	}
	closeErr := file.Close()
	if writeErr != nil {
		return writeErr
	}
	if closeErr != nil {
		return closeErr
	}
	return unix.Fsync(parentFD)
}

func replaceAcceptanceRecordAt(parentFD int, name string, record AcceptanceRecord) error {
	payload, err := MarshalAcceptanceRecord(record)
	if err != nil {
		return err
	}
	temporary := name + ".next"
	fd, err := unix.Openat(parentFD, temporary, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err == nil {
		file := os.NewFile(uintptr(fd), temporary)
		_, writeErr := file.Write(payload)
		if writeErr == nil {
			writeErr = file.Sync()
		}
		closeErr := file.Close()
		if writeErr != nil {
			return writeErr
		}
		if closeErr != nil {
			return closeErr
		}
	} else if errors.Is(err, unix.EEXIST) {
		existing, present, readErr := readBoundedFileAt(parentFD, temporary, maxAcceptanceRecordBytes)
		if readErr != nil || !present || !bytes.Equal(existing, payload) {
			return ErrPublicationUncertain
		}
	} else {
		return err
	}
	if err := unix.Renameat(parentFD, temporary, parentFD, name); err != nil {
		return err
	}
	return unix.Fsync(parentFD)
}

func readBoundedFileAt(parentFD int, name string, limit int) ([]byte, bool, error) {
	fd, err := unix.Openat(parentFD, name, unix.O_RDONLY|unix.O_NONBLOCK|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if errors.Is(err, unix.ENOENT) {
		return nil, false, nil
	}
	if err != nil {
		return nil, true, err
	}
	file := os.NewFile(uintptr(fd), name)
	defer file.Close()
	var before unix.Stat_t
	if err := unix.Fstat(fd, &before); err != nil || before.Mode&unix.S_IFMT != unix.S_IFREG || before.Nlink != 1 || before.Size < 0 || before.Size > int64(limit) {
		return nil, true, ErrInvalidManifest
	}
	payload, err := io.ReadAll(io.LimitReader(file, int64(limit)+1))
	if err != nil || len(payload) > limit {
		return nil, true, ErrInvalidManifest
	}
	var after unix.Stat_t
	if err := unix.Fstat(fd, &after); err != nil || !sameFileIdentity(identityFromStat(before), identityFromStat(after)) {
		return nil, true, ErrPublicationUncertain
	}
	if err := verifyNamedIdentity(parentFD, name, identityFromStat(after)); err != nil {
		return nil, true, ErrPublicationUncertain
	}
	return payload, true, nil
}

type boundPublicationRoot struct {
	path       string
	components []boundPublicationPathComponent
}

type boundPublicationPathComponent struct {
	name     string
	file     *os.File
	identity publicationFileIdentity
}

func openBoundPublicationRoot(value string) (*boundPublicationRoot, error) {
	if strings.TrimSpace(value) == "" {
		return nil, ErrInvalidManifest
	}
	absolute, err := filepath.Abs(filepath.Clean(value))
	if err != nil || absolute == string(filepath.Separator) {
		return nil, ErrInvalidManifest
	}
	canonical, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return nil, err
	}
	if !filepath.IsAbs(canonical) || canonical == string(filepath.Separator) {
		return nil, ErrInvalidManifest
	}
	rootPath := string(filepath.Separator)
	fd, err := unix.Open(rootPath, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), rootPath)
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil || stat.Mode&unix.S_IFMT != unix.S_IFDIR {
		file.Close()
		if err != nil {
			return nil, err
		}
		return nil, ErrInvalidManifest
	}
	bound := &boundPublicationRoot{
		path: canonical,
		components: []boundPublicationPathComponent{{
			file:     file,
			identity: identityFromStat(stat),
		}},
	}
	trimmed := strings.TrimPrefix(filepath.Clean(canonical), rootPath)
	for _, name := range strings.Split(trimmed, rootPath) {
		if !validRawPathComponent(name) {
			bound.Close()
			return nil, ErrInvalidManifest
		}
		parentFD := int(bound.components[len(bound.components)-1].file.Fd())
		child, identity, err := openNamedDirectory(parentFD, name)
		if err != nil {
			bound.Close()
			return nil, err
		}
		bound.components = append(bound.components, boundPublicationPathComponent{
			name:     name,
			file:     child,
			identity: identity,
		})
	}
	if err := bound.verify(); err != nil {
		bound.Close()
		return nil, err
	}
	return bound, nil
}

func (root *boundPublicationRoot) fd() int {
	return int(root.components[len(root.components)-1].file.Fd())
}

func (root *boundPublicationRoot) Close() error {
	var result error
	for index := len(root.components) - 1; index >= 0; index-- {
		if err := root.components[index].file.Close(); err != nil && result == nil {
			result = err
		}
	}
	root.components = nil
	return result
}

func (root *boundPublicationRoot) verify() error {
	if len(root.components) < 2 {
		return ErrPublicationUncertain
	}
	for index, component := range root.components {
		var held unix.Stat_t
		if err := unix.Fstat(int(component.file.Fd()), &held); err != nil || !sameObjectIdentity(component.identity, identityFromStat(held)) || component.identity.kind != unix.S_IFDIR {
			return ErrPublicationUncertain
		}
		if index == 0 {
			var named unix.Stat_t
			if err := unix.Lstat(string(filepath.Separator), &named); err != nil || !sameObjectIdentity(component.identity, identityFromStat(named)) {
				return ErrPublicationUncertain
			}
			continue
		}
		parentFD := int(root.components[index-1].file.Fd())
		if err := verifyNamedIdentity(parentFD, component.name, component.identity); err != nil {
			return ErrPublicationUncertain
		}
	}
	return nil
}

type boundPublicationControl struct {
	root     *boundPublicationRoot
	file     *os.File
	identity publicationFileIdentity
}

func (control *boundPublicationControl) fd() int { return int(control.file.Fd()) }

func (control *boundPublicationControl) Close() error { return control.file.Close() }

func (control *boundPublicationControl) verify() error {
	if err := control.root.verify(); err != nil {
		return ErrPublicationUncertain
	}
	var held unix.Stat_t
	if err := unix.Fstat(control.fd(), &held); err != nil || !sameObjectIdentity(control.identity, identityFromStat(held)) || control.identity.kind != unix.S_IFDIR || uint32(held.Mode)&0o777 != 0o700 {
		return ErrPublicationUncertain
	}
	if err := verifyNamedIdentity(control.root.fd(), publicationControlDirectory, control.identity); err != nil {
		return ErrPublicationUncertain
	}
	if err := requireOnlyPlatformProvenance(control.fd()); err != nil {
		return ErrPublicationUncertain
	}
	return nil
}

func openPublicationControl(root *boundPublicationRoot, create bool) (*boundPublicationControl, bool, error) {
	if err := root.verify(); err != nil {
		return nil, false, ErrPublicationUncertain
	}
	present, identity, err := inspectNamed(root.fd(), publicationControlDirectory)
	if err != nil {
		return nil, present, err
	}
	if !present && !create {
		return nil, false, nil
	}
	if !present {
		if err := root.verify(); err != nil {
			return nil, false, ErrPublicationUncertain
		}
		if err := unix.Mkdirat(root.fd(), publicationControlDirectory, 0o700); err != nil && !errors.Is(err, unix.EEXIST) {
			return nil, false, err
		}
		if err := root.verify(); err != nil {
			return nil, false, ErrPublicationUncertain
		}
	}
	control, opened, err := openNamedDirectory(root.fd(), publicationControlDirectory)
	if err != nil {
		return nil, true, err
	}
	if present && !sameObjectIdentity(opened, identity) || opened.mode&0o777 != 0o700 {
		control.Close()
		return nil, true, ErrPublicationUncertain
	}
	bound := &boundPublicationControl{root: root, file: control, identity: opened}
	if err := bound.verify(); err != nil {
		control.Close()
		return nil, true, err
	}
	return bound, true, nil
}

func lockPublication(ctx context.Context, controlFD int, destinationDigest string) (*os.File, error) {
	name := "lock-" + strings.TrimPrefix(destinationDigest, "sha256:") + ".lock"
	fd, err := unix.Openat(controlFD, name, unix.O_RDWR|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if errors.Is(err, unix.ENOENT) {
		fd, err = unix.Openat(controlFD, name, unix.O_CREAT|unix.O_EXCL|unix.O_RDWR|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
		if errors.Is(err, unix.EEXIST) {
			fd, err = unix.Openat(controlFD, name, unix.O_RDWR|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		}
	}
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), name)
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil || stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Nlink != 1 || uint32(stat.Mode)&0o077 != 0 {
		file.Close()
		return nil, ErrPublicationUncertain
	}
	identity := identityFromStat(stat)
	if err := verifyNamedIdentity(controlFD, name, identity); err != nil {
		file.Close()
		return nil, err
	}
	for {
		if err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err == nil {
			if err := verifyNamedIdentity(controlFD, name, identity); err != nil {
				file.Close()
				return nil, err
			}
			return file, nil
		} else if !errors.Is(err, unix.EWOULDBLOCK) {
			file.Close()
			return nil, err
		}
		select {
		case <-ctx.Done():
			file.Close()
			return nil, ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
}

type publicationFileIdentity struct {
	device uint64
	inode  uint64
	mode   uint32
	links  uint64
	size   int64
	kind   uint32
}

func identityFromStat(stat unix.Stat_t) publicationFileIdentity {
	return publicationFileIdentity{
		device: uint64(stat.Dev),
		inode:  uint64(stat.Ino),
		mode:   uint32(stat.Mode),
		links:  uint64(stat.Nlink),
		size:   stat.Size,
		kind:   uint32(stat.Mode) & unix.S_IFMT,
	}
}

func (identity publicationFileIdentity) digest() string {
	return digestText(fmt.Sprintf("publication-object-v1\x00%d\x00%d", identity.device, identity.inode))
}

func sameFileIdentity(left, right publicationFileIdentity) bool {
	return left.device == right.device && left.inode == right.inode && left.mode == right.mode && left.links == right.links && left.size == right.size && left.kind == right.kind
}

func sameObjectIdentity(left, right publicationFileIdentity) bool {
	return left.device == right.device && left.inode == right.inode && left.kind == right.kind
}

func inspectNamed(parentFD int, name string) (bool, publicationFileIdentity, error) {
	if !validRawPathComponent(name) {
		return false, publicationFileIdentity{}, ErrInvalidManifest
	}
	var stat unix.Stat_t
	err := unix.Fstatat(parentFD, name, &stat, unix.AT_SYMLINK_NOFOLLOW)
	if errors.Is(err, unix.ENOENT) {
		return false, publicationFileIdentity{}, nil
	}
	if err != nil {
		return false, publicationFileIdentity{}, err
	}
	return true, identityFromStat(stat), nil
}

func openNamedDirectory(parentFD int, name string) (*os.File, publicationFileIdentity, error) {
	present, before, err := inspectNamed(parentFD, name)
	if err != nil || !present || before.kind != unix.S_IFDIR {
		if err == nil {
			err = ErrPublicationUncertain
		}
		return nil, publicationFileIdentity{}, err
	}
	fd, err := unix.Openat(parentFD, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, publicationFileIdentity{}, err
	}
	file := os.NewFile(uintptr(fd), name)
	var opened unix.Stat_t
	if err := unix.Fstat(fd, &opened); err != nil || !sameObjectIdentity(before, identityFromStat(opened)) {
		file.Close()
		return nil, publicationFileIdentity{}, ErrPublicationUncertain
	}
	return file, before, nil
}

func verifyNamedIdentity(parentFD int, name string, expected publicationFileIdentity) error {
	present, observed, err := inspectNamed(parentFD, name)
	if err != nil || !present || !sameObjectIdentity(expected, observed) {
		return ErrPublicationUncertain
	}
	return nil
}

func validRawPathComponent(value string) bool {
	return value != "" && value != "." && value != ".." && !strings.ContainsRune(value, 0) && !strings.Contains(value, "/")
}

func verifyStagedTree(ctx context.Context, rootFD int, manifest MigrationManifest) (StageVerification, error) {
	if err := ctx.Err(); err != nil {
		return StageVerification{}, err
	}
	var rootStat unix.Stat_t
	if err := unix.Fstat(rootFD, &rootStat); err != nil || rootStat.Mode&unix.S_IFMT != unix.S_IFDIR || uint32(rootStat.Mode)&0o777 != manifest.Destination.Mode {
		return StageVerification{}, ErrVerificationFailed
	}
	if err := requireOnlyPlatformProvenance(rootFD); err != nil {
		return StageVerification{}, err
	}
	expected := make(map[string]ManifestEntry, len(manifest.Entries))
	expectedLinkCounts := map[string]int{}
	for _, entry := range manifest.Entries {
		raw, err := decodeEncodedPath(entry.TargetPath, false)
		if err != nil {
			return StageVerification{}, err
		}
		expected[raw] = entry
		if entry.Kind == filesystemmeta.ObjectKindRegularFile {
			expectedLinkCounts[entry.SourceObjectIdentity]++
		}
	}
	state := stageVerifyState{
		manifest:           manifest,
		expected:           expected,
		seen:               map[string]struct{}{},
		expectedLinkCounts: expectedLinkCounts,
		sourceToTargetLink: map[string]string{},
		targetToSourceLink: map[string]string{},
	}
	if err := state.walk(ctx, rootFD, "", 0); err != nil {
		return StageVerification{}, err
	}
	if len(state.seen) != len(expected) || state.entryCount != manifest.Totals.EntryCount || state.fileCount != manifest.Totals.RegularFileCount || state.directoryCount != manifest.Totals.DirectoryCount || state.symlinkCount != manifest.Totals.SymlinkCount || state.logicalBytes != manifest.Totals.LogicalBytes {
		return StageVerification{}, ErrVerificationFailed
	}
	return StageVerification{
		SourceDigest:     manifest.SourceDigest,
		EntryCount:       state.entryCount,
		RegularFileCount: state.fileCount,
		DirectoryCount:   state.directoryCount,
		SymlinkCount:     state.symlinkCount,
		LogicalBytes:     state.logicalBytes,
		RootMode:         uint32(rootStat.Mode) & 0o777,
	}, nil
}

type stageVerifyState struct {
	manifest           MigrationManifest
	expected           map[string]ManifestEntry
	seen               map[string]struct{}
	expectedLinkCounts map[string]int
	sourceToTargetLink map[string]string
	targetToSourceLink map[string]string
	entryCount         int
	fileCount          int
	directoryCount     int
	symlinkCount       int
	logicalBytes       uint64
}

func (state *stageVerifyState) walk(ctx context.Context, directoryFD int, relative string, depth int) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if depth > state.manifest.Bounds.MaxDepth {
		return budgetError("staged tree depth", state.manifest.Bounds.MaxDepth)
	}
	readerFD, err := unix.Openat(directoryFD, ".", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	reader := os.NewFile(uintptr(readerFD), "staged-directory")
	entries, err := reader.ReadDir(-1)
	reader.Close()
	if err != nil {
		return err
	}
	sort.Slice(entries, func(left, right int) bool { return entries[left].Name() < entries[right].Name() })
	for _, observed := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		name := observed.Name()
		if name == "" || name == "." || name == ".." || strings.Contains(name, "/") || strings.ContainsRune(name, 0) {
			return ErrVerificationFailed
		}
		entryPath := name
		if relative != "" {
			entryPath = relative + "/" + name
		}
		expected, ok := state.expected[entryPath]
		if !ok {
			return fmt.Errorf("%w: unexpected staged path %s", ErrVerificationFailed, encodeObservedPath(entryPath))
		}
		if _, duplicate := state.seen[entryPath]; duplicate {
			return ErrVerificationFailed
		}
		state.seen[entryPath] = struct{}{}
		state.entryCount++
		if state.entryCount > state.manifest.Bounds.MaxEntries {
			return budgetError("staged tree entries", state.manifest.Bounds.MaxEntries)
		}
		present, identity, err := inspectNamed(directoryFD, name)
		if err != nil || !present {
			return ErrVerificationFailed
		}
		switch identity.kind {
		case unix.S_IFDIR:
			if expected.Kind != filesystemmeta.ObjectKindDirectory || uint32(identity.mode)&0o777 != expected.Mode {
				return ErrVerificationFailed
			}
			child, opened, err := openNamedDirectory(directoryFD, name)
			if err != nil {
				return err
			}
			if err := requireOnlyPlatformProvenance(int(child.Fd())); err != nil {
				child.Close()
				return err
			}
			state.directoryCount++
			err = state.walk(ctx, int(child.Fd()), entryPath, depth+1)
			child.Close()
			if err != nil {
				return err
			}
			if err := verifyNamedIdentity(directoryFD, name, opened); err != nil {
				return err
			}
		case unix.S_IFREG:
			if expected.Kind != filesystemmeta.ObjectKindRegularFile || uint32(identity.mode)&0o777 != expected.Mode || identity.size < 0 || uint64(identity.size) != expected.LogicalBytes {
				return ErrVerificationFailed
			}
			digest, verifiedIdentity, err := verifyRegularFileAt(ctx, directoryFD, name, identity, expected.LogicalBytes)
			if err != nil || digest != expected.ContentDigest {
				return ErrVerificationFailed
			}
			if err := state.bindTargetLink(expected, verifiedIdentity); err != nil {
				return err
			}
			state.fileCount++
			state.logicalBytes, err = addUint64(state.logicalBytes, expected.LogicalBytes)
			if err != nil || state.logicalBytes > state.manifest.Bounds.MaxPayloadBytes {
				return budgetError("staged payload bytes", int(state.manifest.Bounds.MaxPayloadBytes))
			}
		case unix.S_IFLNK:
			if expected.Kind != filesystemmeta.ObjectKindSymlink {
				return ErrVerificationFailed
			}
			if err := requireOnlyPlatformProvenanceSymlinkAt(directoryFD, name, identity); err != nil {
				return err
			}
			target, err := readlinkAtBounded(directoryFD, name)
			if err != nil || path.IsAbs(target) {
				return ErrVerificationFailed
			}
			resolved := path.Clean(path.Join(path.Dir(entryPath), target))
			if resolved == "." {
				resolved = ""
			}
			if resolved == ".." || strings.HasPrefix(resolved, "../") || encodeObservedPath(resolved) != expected.TargetSymlinkPath {
				return ErrVerificationFailed
			}
			if err := verifyNamedIdentity(directoryFD, name, identity); err != nil {
				return err
			}
			state.symlinkCount++
		default:
			return ErrVerificationFailed
		}
	}
	// A successful verification is also the durability barrier for this
	// directory's entries. Regular files are flushed individually below, and
	// directories are flushed bottom-up before an acceptance record can advance.
	return unix.Fsync(directoryFD)
}

func (state *stageVerifyState) bindTargetLink(entry ManifestEntry, identity publicationFileIdentity) error {
	targetIdentity := fmt.Sprintf("%d:%d", identity.device, identity.inode)
	if existing, present := state.sourceToTargetLink[entry.SourceObjectIdentity]; present && existing != targetIdentity {
		return ErrVerificationFailed
	}
	if existing, present := state.targetToSourceLink[targetIdentity]; present && existing != entry.SourceObjectIdentity {
		return ErrVerificationFailed
	}
	state.sourceToTargetLink[entry.SourceObjectIdentity] = targetIdentity
	state.targetToSourceLink[targetIdentity] = entry.SourceObjectIdentity
	expectedLinks := state.expectedLinkCounts[entry.SourceObjectIdentity]
	if expectedLinks <= 0 || identity.links != uint64(expectedLinks) || entry.SourceLinkCount != uint64(expectedLinks) {
		return ErrVerificationFailed
	}
	return nil
}

func verifyRegularFileAt(ctx context.Context, parentFD int, name string, expected publicationFileIdentity, maxBytes uint64) (string, publicationFileIdentity, error) {
	fd, err := unix.Openat(parentFD, name, unix.O_RDONLY|unix.O_NONBLOCK|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return "", publicationFileIdentity{}, err
	}
	file := os.NewFile(uintptr(fd), name)
	defer file.Close()
	var opened unix.Stat_t
	if err := unix.Fstat(fd, &opened); err != nil || !sameFileIdentity(expected, identityFromStat(opened)) {
		return "", publicationFileIdentity{}, ErrPublicationUncertain
	}
	if err := requireOnlyPlatformProvenance(fd); err != nil {
		return "", publicationFileIdentity{}, err
	}
	hash := sha256.New()
	buffer := make([]byte, 128*1024)
	var total uint64
	for {
		if err := ctx.Err(); err != nil {
			return "", publicationFileIdentity{}, err
		}
		count, readErr := file.Read(buffer)
		if count > 0 {
			var addErr error
			total, addErr = addUint64(total, uint64(count))
			if addErr != nil || total > maxBytes {
				return "", publicationFileIdentity{}, ErrVerificationFailed
			}
			_, _ = hash.Write(buffer[:count])
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return "", publicationFileIdentity{}, readErr
		}
	}
	if total != maxBytes {
		return "", publicationFileIdentity{}, ErrVerificationFailed
	}
	if err := unix.Fsync(fd); err != nil {
		return "", publicationFileIdentity{}, err
	}
	var after unix.Stat_t
	if err := unix.Fstat(fd, &after); err != nil || !sameFileIdentity(expected, identityFromStat(after)) {
		return "", publicationFileIdentity{}, ErrPublicationUncertain
	}
	if err := verifyNamedIdentity(parentFD, name, identityFromStat(after)); err != nil {
		return "", publicationFileIdentity{}, err
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil)), identityFromStat(after), nil
}

func requireOnlyPlatformProvenance(fd int) error {
	count, err := unix.Flistxattr(fd, nil)
	if err != nil {
		return ErrVerificationFailed
	}
	if count == 0 {
		return nil
	}
	buffer := make([]byte, count)
	count, err = unix.Flistxattr(fd, buffer)
	if err != nil {
		return ErrVerificationFailed
	}
	return validatePlatformProvenanceXattrList(buffer[:count])
}

func validatePlatformProvenanceXattrList(buffer []byte) error {
	for _, raw := range bytes.Split(buffer, []byte{0}) {
		if len(raw) != 0 && string(raw) != "com.apple.provenance" {
			return ErrVerificationFailed
		}
	}
	return nil
}

func readlinkAtBounded(parentFD int, name string) (string, error) {
	buffer := make([]byte, maxSymlinkTargetBytes)
	count, err := unix.Readlinkat(parentFD, name, buffer)
	if err != nil || count == len(buffer) {
		return "", ErrVerificationFailed
	}
	return string(buffer[:count]), nil
}

func encodeObservedPath(value string) string {
	if value == "" {
		return ""
	}
	parts := strings.Split(value, "/")
	for index := range parts {
		parts[index] = url.PathEscape(parts[index])
	}
	return strings.Join(parts, "/")
}

func acceptanceRecordName(destinationDigest string) string {
	return "accept-" + strings.TrimPrefix(destinationDigest, "sha256:") + ".json"
}

func validAttemptID(value string) bool {
	if value == "" || len(value) > 63 {
		return false
	}
	for _, character := range value {
		if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || character == '-' || character == '_' {
			continue
		}
		return false
	}
	return true
}

func validStageName(value string) bool {
	return strings.HasPrefix(value, "stage-migration-") && validPathComponent(value) && len(value) <= 255
}

func validManifestID(value string) bool {
	if !strings.HasPrefix(value, "migration-") || len(value) != len("migration-")+32 {
		return false
	}
	encoded := strings.TrimPrefix(value, "migration-")
	_, err := hex.DecodeString(encoded)
	return err == nil && encoded == strings.ToLower(encoded)
}

func stageAttemptID(manifestID, stageName string) (string, bool) {
	prefix := "stage-" + manifestID + "-"
	if !strings.HasPrefix(stageName, prefix) || !validStageName(stageName) {
		return "", false
	}
	attemptID := strings.TrimPrefix(stageName, prefix)
	return attemptID, validAttemptID(attemptID)
}

func redactedPublicationError(operation string, cause error) error {
	return &publicationOperationError{operation: operation, cause: cause}
}
