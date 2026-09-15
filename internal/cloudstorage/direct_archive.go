package cloudstorage

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"loom.local/loom/internal/backupstrategy"
	"loom.local/loom/internal/config"
	"loom.local/loom/internal/hermesprofile"
)

const (
	DirectArchiveStatusSucceeded = "succeeded"
	DirectArchiveStatusFailed    = "failed"
	DirectArchiveStatusConflict  = "conflict"

	DirectArchiveCodeConflict       = "cloud.direct_archive.manifest_conflict"
	DirectArchiveCodeSourceUnstable = "cloud.direct_archive.source_unstable"
	DirectArchiveCodeInterrupted    = "cloud.direct_archive.interrupted"
	DirectArchiveCodeRemoteFailure  = "cloud.direct_archive.remote_failure"
	DirectArchiveCodeArchiveInvalid = "cloud.direct_archive.archive_invalid"

	directArchiveResumeEnvelopeSchema = "loom.direct_archive.resume_envelope.v1"
	directArchiveResumeMarkerSchema   = "loom.direct_archive.resume_marker.v1"
)

var (
	ErrDirectArchiveConflict  = errors.New("direct archive manifest conflict")
	ErrDirectArchiveRetryable = errors.New("direct archive retryable failure")
)

type DirectArchiveInput struct {
	// Trusted injection for isolated acceptance; runtime callers use daemon config.
	HermesPolicy *hermesprofile.Policy `json:"-"`
	Config       Config
	Request      backupstrategy.DirectArchiveRequest
	RequestV2    *backupstrategy.DirectArchiveRequestV2
}

type DirectArchiveResult struct {
	Status                           string                                  `json:"status"`
	Code                             string                                  `json:"code,omitempty"`
	Backend                          string                                  `json:"backend"`
	Repository                       string                                  `json:"repository"`
	Archive                          string                                  `json:"archive"`
	ArchiveRef                       string                                  `json:"archive_ref"`
	ArchiveClass                     string                                  `json:"archive_class"`
	RemoteURI                        string                                  `json:"remote_uri"`
	PendingArchive                   string                                  `json:"pending_archive,omitempty"`
	ResumedPending                   bool                                    `json:"resumed_pending"`
	VerificationPhase                string                                  `json:"verification_phase,omitempty"`
	ManifestSchema                   string                                  `json:"manifest_schema"`
	VerificationProfile              string                                  `json:"verification_profile,omitempty"`
	ManifestSHA256                   string                                  `json:"manifest_sha256"`
	Manifest                         *backupstrategy.DirectArchiveManifest   `json:"manifest,omitempty"`
	ManifestV2                       *backupstrategy.DirectArchiveManifestV2 `json:"manifest_v2,omitempty"`
	Checks                           map[string]string                       `json:"checks"`
	Idempotent                       bool                                    `json:"idempotent"`
	Committed                        bool                                    `json:"committed"`
	Retryable                        bool                                    `json:"retryable"`
	Error                            string                                  `json:"error,omitempty"`
	PackedBytes                      int64                                   `json:"packed_bytes,omitempty"`
	DeduplicatedBytes                int64                                   `json:"deduplicated_bytes,omitempty"`
	BorgCommandCounts                map[string]int64                        `json:"borg_command_counts"`
	StageDurationsMS                 map[string]int64                        `json:"stage_durations_ms"`
	OperationalPackageID             string                                  `json:"operational_package_id"`
	OperationalPackageManifestSHA256 string                                  `json:"operational_package_manifest_sha256"`
	ProvenancePackageID              string                                  `json:"provenance_package_id"`
	ProvenancePackageManifestSHA256  string                                  `json:"provenance_package_manifest_sha256"`
}

var directPendingArchivePattern = regexp.MustCompile(`^__loom-direct-pending-([0-9a-f]{12})-([0-9a-f]{8})$`)

type directArchiveResumeEnvelopeRoot struct {
	Name        string `json:"name"`
	SourcePath  string `json:"source_path"`
	ArchivePath string `json:"archive_path"`
}

// directArchiveResumeEnvelope binds every immutable request, policy, path, and
// recovery-package field needed to decide whether an authenticated frozen Borg
// archive belongs to the current logical archive request. Live root entry
// inventories and SourceSnapshotSHA256 are deliberately absent: a completed
// pending archive is verified against its own authenticated frozen manifest,
// not against a later sampling of mutable source roots.
type directArchiveResumeEnvelope struct {
	HermesRecovery     []hermesprofile.Evidence                               `json:"hermes_recovery,omitempty"`
	Schema             string                                                 `json:"schema"`
	ManifestSchema     string                                                 `json:"manifest_schema"`
	Backend            string                                                 `json:"backend"`
	Repository         string                                                 `json:"repository"`
	ArchiveName        string                                                 `json:"archive_name"`
	NodeID             string                                                 `json:"node_id"`
	ArchiveRef         string                                                 `json:"archive_ref"`
	ArchiveClass       string                                                 `json:"archive_class"`
	CreatedAt          time.Time                                              `json:"created_at"`
	ExclusionPolicy    string                                                 `json:"exclusion_policy"`
	Exclusions         []backupstrategy.DirectArchiveExclusion                `json:"exclusions"`
	Roots              []directArchiveResumeEnvelopeRoot                      `json:"roots"`
	OperationalPackage backupstrategy.DirectArchiveOperationalPackageManifest `json:"operational_package"`
	ProvenancePackage  backupstrategy.DirectArchiveProvenancePackageManifest  `json:"provenance_package"`
}

type directArchiveResumeEnvelopeV2 struct {
	HermesRecovery      []hermesprofile.Evidence                                 `json:"hermes_recovery,omitempty"`
	Schema              string                                                   `json:"schema"`
	ManifestSchema      string                                                   `json:"manifest_schema"`
	VerificationProfile string                                                   `json:"verification_profile"`
	Backend             string                                                   `json:"backend"`
	Repository          string                                                   `json:"repository"`
	ArchiveName         string                                                   `json:"archive_name"`
	NodeID              string                                                   `json:"node_id"`
	ArchiveRef          string                                                   `json:"archive_ref"`
	ArchiveClass        string                                                   `json:"archive_class"`
	CreatedAt           time.Time                                                `json:"created_at"`
	ExclusionPolicy     string                                                   `json:"exclusion_policy"`
	Exclusions          []backupstrategy.DirectArchiveExclusion                  `json:"exclusions"`
	Roots               []backupstrategy.DirectArchiveRootManifestV2             `json:"roots"`
	OperationalPackage  backupstrategy.DirectArchiveOperationalPackageManifestV2 `json:"operational_package"`
	ProvenancePackage   backupstrategy.DirectArchiveProvenancePackageManifestV2  `json:"provenance_package"`
}

type directArchiveResumeMarker struct {
	Schema         string `json:"schema"`
	Repository     string `json:"repository"`
	ArchiveName    string `json:"archive_name"`
	PendingArchive string `json:"pending_archive"`
	ManifestSHA256 string `json:"manifest_sha256"`
}

func preparedDirectArchiveIsV2(prepared backupstrategy.PreparedDirectArchiveManifest) bool {
	return prepared.ManifestV2 != nil
}

func preparedDirectArchiveSchema(prepared backupstrategy.PreparedDirectArchiveManifest) string {
	if prepared.ManifestV2 != nil {
		return prepared.ManifestV2.Schema
	}
	return prepared.Manifest.Schema
}

func preparedDirectArchiveVerificationProfile(prepared backupstrategy.PreparedDirectArchiveManifest) string {
	if prepared.ManifestV2 != nil {
		return prepared.ManifestV2.VerificationProfile
	}
	return ""
}

func preparedDirectArchiveBackend(prepared backupstrategy.PreparedDirectArchiveManifest) string {
	if prepared.ManifestV2 != nil {
		return prepared.ManifestV2.Backend
	}
	return prepared.Manifest.Backend
}

func preparedDirectArchiveRepository(prepared backupstrategy.PreparedDirectArchiveManifest) string {
	if prepared.ManifestV2 != nil {
		return prepared.ManifestV2.Repository
	}
	return prepared.Manifest.Repository
}

func preparedDirectArchiveName(prepared backupstrategy.PreparedDirectArchiveManifest) string {
	if prepared.ManifestV2 != nil {
		return prepared.ManifestV2.ArchiveName
	}
	return prepared.Manifest.ArchiveName
}

func preparedDirectArchiveCreatedAt(prepared backupstrategy.PreparedDirectArchiveManifest) time.Time {
	if prepared.ManifestV2 != nil {
		return prepared.ManifestV2.CreatedAt
	}
	return prepared.Manifest.CreatedAt
}

func applyPreparedDirectArchiveResult(result *DirectArchiveResult, prepared backupstrategy.PreparedDirectArchiveManifest) {
	result.ManifestSHA256 = prepared.ManifestSHA256
	result.ManifestSchema = preparedDirectArchiveSchema(prepared)
	result.VerificationProfile = preparedDirectArchiveVerificationProfile(prepared)
	if prepared.ManifestV2 != nil {
		manifest := *prepared.ManifestV2
		result.ManifestV2 = &manifest
		result.Manifest = nil
		result.OperationalPackageID = manifest.OperationalPackage.PackageID
		result.OperationalPackageManifestSHA256 = manifest.OperationalPackage.ManifestSHA256
		result.ProvenancePackageID = manifest.ProvenancePackage.PackageID
		result.ProvenancePackageManifestSHA256 = manifest.ProvenancePackage.ManifestSHA256
		return
	}
	manifest := prepared.Manifest
	result.Manifest = &manifest
	result.ManifestV2 = nil
	result.OperationalPackageID = manifest.OperationalPackage.PackageID
	result.OperationalPackageManifestSHA256 = manifest.OperationalPackage.ManifestSHA256
	result.ProvenancePackageID = manifest.ProvenancePackage.PackageID
	result.ProvenancePackageManifestSHA256 = manifest.ProvenancePackage.ManifestSHA256
}

func ArchiveCanonicalRoots(ctx context.Context, input DirectArchiveInput) (DirectArchiveResult, error) {
	cfg, err := NormalizeConfig(input.Config)
	if err != nil {
		return DirectArchiveResult{}, err
	}
	if !cfg.Enabled {
		return DirectArchiveResult{}, fmt.Errorf("cloud storage is disabled")
	}
	if cfg.Snapshots.Backend != SnapshotBackendBorg {
		return DirectArchiveResult{}, fmt.Errorf("direct canonical-root archives require the existing Borg snapshot backend")
	}
	backend := NewBorgSnapshotBackend(cfg)
	return backend.ArchiveCanonicalRoots(ctx, DirectArchiveInput{Config: cfg, Request: input.Request, RequestV2: input.RequestV2})
}

func (b BorgSnapshotBackend) ArchiveCanonicalRoots(ctx context.Context, input DirectArchiveInput) (DirectArchiveResult, error) {
	policy, err := config.LoadHermesRecoveryPolicy()
	if input.HermesPolicy != nil {
		policy = *input.HermesPolicy
		err = nil
	}
	if err != nil {
		return DirectArchiveResult{}, err
	}
	now := time.Now().UTC()
	var hermesEvidence []hermesprofile.Evidence
	if input.RequestV2 != nil {
		request := *input.RequestV2
		request.Exclusions, hermesEvidence, err = backupstrategy.HermesArchiveBoundary(ctx, request.Roots, request.Exclusions, policy, now)
		request.HermesRecovery = hermesEvidence
		input.RequestV2 = &request
	} else {
		input.Request.Exclusions, hermesEvidence, err = backupstrategy.HermesArchiveBoundary(ctx, input.Request.Roots, input.Request.Exclusions, policy, now)
		input.Request.HermesRecovery = hermesEvidence
	}
	if err != nil {
		return DirectArchiveResult{}, err
	}

	cfg, err := NormalizeConfig(input.Config)
	if err != nil {
		return DirectArchiveResult{}, err
	}
	if !cfg.Enabled || cfg.Snapshots.Backend != SnapshotBackendBorg {
		return DirectArchiveResult{}, fmt.Errorf("direct canonical-root archives require an enabled Borg backend")
	}
	if err := validateBorgCommandConfig(cfg); err != nil {
		return DirectArchiveResult{}, err
	}
	var (
		archiveClass   string
		archive        string
		requestNodeID  string
		requestRef     string
		prepareInput   backupstrategy.DirectArchiveManifestPrepareInput
		prepareInputV2 backupstrategy.DirectArchiveManifestPrepareInputV2
		prepared       backupstrategy.PreparedDirectArchiveManifest
		v2             = input.RequestV2 != nil
	)
	if v2 {
		if input.Request.Schema != "" {
			return DirectArchiveResult{}, fmt.Errorf("direct archive input must contain exactly one request schema")
		}
		request := *input.RequestV2
		archiveClass, err = backupstrategy.NormalizeDirectArchiveClass(request.ArchiveClass)
		if err != nil {
			return DirectArchiveResult{}, err
		}
		request.ArchiveClass = archiveClass
		requestNodeID, requestRef = request.NodeID, request.ArchiveRef
		archive = directBorgArchiveName(request.NodeID, request.ArchiveRef, archiveClass)
		prepareInputV2 = backupstrategy.DirectArchiveManifestPrepareInputV2{
			Request: request, Backend: backupstrategy.DirectArchiveBackendBorg,
			Repository: cfg.Snapshots.Borg.Repository, ArchiveName: archive,
		}
	} else {
		request := input.Request
		archiveClass, err = backupstrategy.NormalizeDirectArchiveClass(request.ArchiveClass)
		if err != nil {
			return DirectArchiveResult{}, err
		}
		request.ArchiveClass = archiveClass
		requestNodeID, requestRef = request.NodeID, request.ArchiveRef
		archive = directBorgArchiveName(request.NodeID, request.ArchiveRef, archiveClass)
		prepareInput = backupstrategy.DirectArchiveManifestPrepareInput{
			Request: request, Backend: backupstrategy.DirectArchiveBackendBorg,
			Repository: cfg.Snapshots.Borg.Repository, ArchiveName: archive,
		}
	}
	result := DirectArchiveResult{
		Status: DirectArchiveStatusFailed, Backend: SnapshotBackendBorg,
		Repository: cfg.Snapshots.Borg.Repository, Archive: archive,
		ArchiveRef: requestRef, ArchiveClass: archiveClass, RemoteURI: borgRemoteURI(cfg, archive),
		Checks:            map[string]string{},
		VerificationPhase: "pending_discovery",
		BorgCommandCounts: map[string]int64{
			"check": 0, "compact": 0, "create": 0, "delete": 0, "export-tar": 0,
			"extract": 0, "info": 0, "list": 0, "prune": 0, "rename": 0,
		},
		StageDurationsMS: map[string]int64{
			"package_preparation": 0, "envelope_preparation": 0, "source_manifest_preparation": 0,
			"create": 0, "package_stability": 0, "metadata_check": 0, "envelope_authentication": 0, "rename": 0,
		},
	}
	prepareStarted := time.Now()
	if v2 {
		prepared, err = backupstrategy.PrepareDirectArchiveManifestV2(ctx, prepareInputV2)
	} else {
		prepared, err = backupstrategy.PrepareDirectArchiveManifest(ctx, prepareInput)
	}
	if err != nil {
		result.StageDurationsMS["source_manifest_preparation"] = time.Since(prepareStarted).Milliseconds()
		return result, directArchiveFailure(&result, DirectArchiveCodeArchiveInvalid, false, err)
	}
	if v2 {
		result.StageDurationsMS["package_preparation"] = prepared.PackagePreparationDurationMS
		result.StageDurationsMS["envelope_preparation"] = prepared.EnvelopePreparationDurationMS
	} else {
		result.StageDurationsMS["source_manifest_preparation"] = time.Since(prepareStarted).Milliseconds()
	}
	applyPreparedDirectArchiveResult(&result, prepared)

	evidenceRoot, err := writeDirectArchiveEvidence(cfg.StateDir, prepared)
	if err != nil {
		return result, directArchiveFailure(&result, DirectArchiveCodeArchiveInvalid, false, err)
	}
	defer os.RemoveAll(evidenceRoot)

	runner := b.runner(cfg)
	runner.DisableRemoteLock = true
	previousObserver := runner.observeCommand
	runner.observeCommand = func(command BorgCommand) {
		if previousObserver != nil {
			previousObserver(command)
		}
		result.BorgCommandCounts[command.Name()]++
	}
	operationErr := WithRemoteLock(ctx, cfg, RemoteLockOptions{
		Operation: "borg.direct_archive", Wait: RemoteLockEffectfulWait(cfg),
	}, func(ctx context.Context) error {
		archives, listErr := listBorgArchives(ctx, runner)
		if listErr != nil {
			return directArchiveFailure(&result, DirectArchiveCodeRemoteFailure, true, listErr)
		}
		result.Checks["borg_list"] = DirectArchiveStatusSucceeded
		if borgArchiveExists(archives, archive) {
			result.VerificationPhase = "canonical_verification"
			remote, verification, verifyErr := readAndVerifyDirectArchiveForExpected(ctx, runner, cfg, archive, prepared)
			applyDirectArchiveVerificationEvidence(&result, verification)
			if verifyErr != nil {
				return directArchiveFailure(&result, DirectArchiveCodeConflict, false, fmt.Errorf("%w: existing archive is not an authenticated direct archive: %v", ErrDirectArchiveConflict, verifyErr))
			}
			if identityErr := verifyCompatibleDirectArchiveResumeEnvelope(remote, prepared); identityErr != nil {
				return directArchiveFailure(&result, DirectArchiveCodeConflict, false, fmt.Errorf("%w: existing canonical archive does not match the immutable resume envelope: %v", ErrDirectArchiveConflict, identityErr))
			}
			result.Checks["resume_envelope"] = DirectArchiveStatusSucceeded
			result.Checks["source_stability"] = "frozen_canonical_not_rechecked"
			result.Checks["manifest_identity"] = "authenticated_frozen"
			result.Status = DirectArchiveStatusSucceeded
			result.Idempotent = true
			result.Committed = true
			applyPreparedDirectArchiveResult(&result, remote)
			result.VerificationPhase = "committed"
			if v2 {
				_ = removeDirectArchiveResumeMarker(cfg, prepared)
			}
			return nil
		}

		pendingArchive, frozenPending, resumed, discoverErr := discoverDirectPendingArchive(ctx, runner, cfg, archives, prepared)
		if discoverErr != nil {
			return directArchiveFailure(&result, DirectArchiveCodeConflict, false, fmt.Errorf("%w: %v", ErrDirectArchiveConflict, discoverErr))
		}
		if resumed {
			result.ResumedPending = true
			result.PendingArchive = pendingArchive
			applyPreparedDirectArchiveResult(&result, frozenPending)
			result.Checks["pending_archive_discovery"] = "resumed_authenticated"
			result.Checks["borg_create_pending"] = "not_run_resumed"
			result.Checks["resume_envelope"] = DirectArchiveStatusSucceeded
		} else {
			result.Checks["pending_archive_discovery"] = "none"
			if v2 {
				if markerErr := removeDirectArchiveResumeMarker(cfg, prepared); markerErr != nil {
					return directArchiveFailure(&result, DirectArchiveCodeArchiveInvalid, false, markerErr)
				}
			}
			var nameErr error
			pendingArchive, nameErr = pendingBorgArchiveName(archive, prepared.ManifestSHA256)
			if nameErr != nil {
				return directArchiveFailure(&result, DirectArchiveCodeArchiveInvalid, false, nameErr)
			}
			result.PendingArchive = pendingArchive
			createArgs := directArchiveCreateArgs(cfg, pendingArchive, prepared)
			createStarted := time.Now()
			createOut, createErr := runner.Run(ctx, BorgCommand{Args: createArgs, Dir: evidenceRoot})
			result.StageDurationsMS["create"] = time.Since(createStarted).Milliseconds()
			if createErr != nil {
				result.Checks["borg_create_pending"] = DirectArchiveStatusFailed
				if v2 && borgCommandHasExitCode(createErr) {
					result.VerificationPhase = "create_failed"
					return directArchiveFailure(&result, DirectArchiveCodeSourceUnstable, false, createErr)
				}
				if v2 {
					if markerErr := writeDirectArchiveResumeMarker(cfg, pendingArchive, prepared); markerErr != nil {
						createErr = errors.Join(createErr, fmt.Errorf("persist ambiguous pending resume marker: %w", markerErr))
						result.VerificationPhase = "create_interrupted"
						return directArchiveFailure(&result, DirectArchiveCodeInterrupted, true, createErr)
					}
				}
				archivesAfterCreate, listErr := listBorgArchives(ctx, runner)
				if listErr != nil || !borgArchiveExists(archivesAfterCreate, pendingArchive) {
					if listErr != nil {
						createErr = errors.Join(createErr, fmt.Errorf("inspect ambiguous pending archive: %w", listErr))
					}
					result.VerificationPhase = "create_interrupted"
					return directArchiveFailure(&result, DirectArchiveCodeInterrupted, true, createErr)
				}
				result.Checks["borg_create_pending"] = "ambiguous_durable"
			} else {
				result.Checks["borg_create_pending"] = DirectArchiveStatusSucceeded
				if v2 {
					packed, deduplicated, statsErr := parseDirectArchiveQuickStats(createOut)
					if statsErr != nil {
						return directArchiveFailure(&result, DirectArchiveCodeArchiveInvalid, false, statsErr)
					}
					result.PackedBytes, result.DeduplicatedBytes = packed, deduplicated
					if markerErr := writeDirectArchiveResumeMarker(cfg, pendingArchive, prepared); markerErr != nil {
						return directArchiveFailure(&result, DirectArchiveCodeArchiveInvalid, false, markerErr)
					}
				} else {
					result.PackedBytes, result.DeduplicatedBytes, _ = parseBorgMetadata(createOut)
				}
			}
		}

		if v2 {
			result.VerificationPhase = "package_stability"
			stabilityStarted := time.Now()
			stabilityErr := backupstrategy.VerifyDirectArchiveV2PackageStability(ctx, prepareInputV2, prepared)
			result.StageDurationsMS["package_stability"] = time.Since(stabilityStarted).Milliseconds()
			if stabilityErr != nil {
				result.Checks["package_stability"] = DirectArchiveStatusFailed
				return directArchiveFailure(&result, DirectArchiveCodeSourceUnstable, true, stabilityErr)
			}
			result.Checks["package_stability"] = DirectArchiveStatusSucceeded
			result.Checks["source_stability"] = "borg_create_no_second_source_pass"
		} else if resumed {
			result.Checks["source_stability"] = "frozen_pending_not_rechecked"
		} else {
			result.VerificationPhase = "source_stability"
			if stabilityErr := backupstrategy.VerifyDirectArchiveSourceStability(ctx, prepareInput, prepared); stabilityErr != nil {
				result.Checks["source_stability"] = DirectArchiveStatusFailed
				return directArchiveFailure(&result, DirectArchiveCodeSourceUnstable, true, stabilityErr)
			}
			result.Checks["source_stability"] = DirectArchiveStatusSucceeded
		}

		currentHermes, checkErr := hermesprofile.Check(ctx, policy, time.Now().UTC())
		originalJSON, _ := json.Marshal(hermesEvidence)
		currentJSON, _ := json.Marshal(currentHermes)
		if checkErr != nil || !bytes.Equal(originalJSON, currentJSON) {
			return directArchiveFailure(&result, DirectArchiveCodeSourceUnstable, true, fmt.Errorf("Hermes recovery evidence changed during cloud creation"))
		}
		result.VerificationPhase = "pending_verification"
		pendingManifest, verification, verifyErr := readAndVerifyDirectArchiveForExpected(ctx, runner, cfg, pendingArchive, prepared)
		applyDirectArchiveVerificationEvidence(&result, verification)
		if verifyErr != nil {
			return directArchiveFailure(&result, DirectArchiveCodeArchiveInvalid, true, verifyErr)
		}
		if resumed {
			if pendingManifest.ManifestSHA256 != frozenPending.ManifestSHA256 {
				return directArchiveFailure(&result, DirectArchiveCodeArchiveInvalid, true, fmt.Errorf("authenticated pending manifest changed during verification"))
			}
			if identityErr := verifyCompatibleDirectArchiveResumeEnvelope(pendingManifest, prepared); identityErr != nil {
				return directArchiveFailure(&result, DirectArchiveCodeArchiveInvalid, true, identityErr)
			}
		} else if identityErr := verifyExpectedDirectArchiveIdentity(pendingManifest, prepared); identityErr != nil {
			return directArchiveFailure(&result, DirectArchiveCodeArchiveInvalid, true, identityErr)
		}
		result.Checks["pending_archive_verify"] = DirectArchiveStatusSucceeded
		applyPreparedDirectArchiveResult(&result, pendingManifest)
		result.VerificationPhase = "pending_verified"

		archivesBeforeCommit, listErr := listBorgArchives(ctx, runner)
		if listErr != nil {
			return directArchiveFailure(&result, DirectArchiveCodeRemoteFailure, true, listErr)
		}
		if borgArchiveExists(archivesBeforeCommit, archive) {
			canonical, canonicalVerification, canonicalErr := readAndVerifyDirectArchiveForExpected(ctx, runner, cfg, archive, prepared)
			applyDirectArchiveVerificationEvidence(&result, canonicalVerification)
			identityErr := verifyExpectedDirectArchiveIdentity(canonical, prepared)
			if resumed {
				identityErr = verifyCompatibleDirectArchiveResumeEnvelope(canonical, prepared)
				if identityErr == nil && canonical.ManifestSHA256 != pendingManifest.ManifestSHA256 {
					identityErr = fmt.Errorf("canonical archive does not match the selected frozen pending manifest")
				}
			}
			if canonicalErr != nil || identityErr != nil {
				return directArchiveFailure(&result, DirectArchiveCodeConflict, false, fmt.Errorf("%w: canonical archive appeared before commit", ErrDirectArchiveConflict))
			}
			if resumed {
				result.Checks["resume_envelope"] = DirectArchiveStatusSucceeded
			}
			result.Checks["manifest_identity"] = DirectArchiveStatusSucceeded
			result.Status = DirectArchiveStatusSucceeded
			result.Idempotent = true
			result.Committed = true
			applyPreparedDirectArchiveResult(&result, canonical)
			result.VerificationPhase = "committed"
			if v2 {
				_ = removeDirectArchiveResumeMarker(cfg, prepared)
			}
			return nil
		}

		result.VerificationPhase = "canonical_commit"
		renameStarted := time.Now()
		if _, renameErr := runner.Run(ctx, BorgCommand{Args: []string{"rename", "::" + pendingArchive, archive}}); renameErr != nil {
			result.StageDurationsMS["rename"] = time.Since(renameStarted).Milliseconds()
			return directArchiveFailure(&result, DirectArchiveCodeRemoteFailure, true, renameErr)
		}
		result.StageDurationsMS["rename"] = time.Since(renameStarted).Milliseconds()
		if !v2 {
			infoOut, infoErr := runner.Run(ctx, BorgCommand{Args: []string{"info", "--json", "::" + archive}})
			if infoErr != nil {
				return directArchiveFailure(&result, DirectArchiveCodeRemoteFailure, true, infoErr)
			}
			if packed, deduplicated, _ := parseBorgMetadata(infoOut); packed > 0 || deduplicated > 0 {
				result.PackedBytes, result.DeduplicatedBytes = packed, deduplicated
			}
		}
		result.Checks["canonical_archive_commit"] = DirectArchiveStatusSucceeded
		result.Status = DirectArchiveStatusSucceeded
		result.Committed = true
		applyPreparedDirectArchiveResult(&result, pendingManifest)
		result.VerificationPhase = "committed"
		if v2 {
			_ = removeDirectArchiveResumeMarker(cfg, prepared)
		}
		_ = removeBorgInventoryCache(cfg, safeRemoteSegment(requestNodeID))
		return nil
	})
	if operationErr != nil {
		if result.Error == "" {
			return result, directArchiveFailure(&result, DirectArchiveCodeRemoteFailure, true, operationErr)
		}
		return result, operationErr
	}
	return result, nil
}

func discoverDirectPendingArchive(ctx context.Context, runner BorgCommandRunner, cfg Config, archives []borgArchiveItem, expected backupstrategy.PreparedDirectArchiveManifest) (string, backupstrategy.PreparedDirectArchiveManifest, bool, error) {
	type pendingMatch struct {
		name     string
		manifest backupstrategy.PreparedDirectArchiveManifest
	}
	matches := make([]pendingMatch, 0, 1)
	for _, archive := range archives {
		if !isDirectPendingBorgArchive(archive.Name) {
			continue
		}
		parts := directPendingArchivePattern.FindStringSubmatch(archive.Name)
		if parts == nil {
			return "", backupstrategy.PreparedDirectArchiveManifest{}, false, fmt.Errorf("reserved pending archive name %q is malformed", archive.Name)
		}
		remote, err := readAuthenticatedDirectArchiveManifest(ctx, runner, cfg, archive.Name)
		if err != nil {
			return "", backupstrategy.PreparedDirectArchiveManifest{}, false, fmt.Errorf("reserved pending archive %q is unauthenticated: %w", archive.Name, err)
		}
		if parts[1] != remote.ManifestSHA256[:12] {
			return "", backupstrategy.PreparedDirectArchiveManifest{}, false, fmt.Errorf("reserved pending archive %q does not match its authenticated manifest prefix", archive.Name)
		}
		sameCanonical := preparedDirectArchiveName(remote) == preparedDirectArchiveName(expected)
		expectedPrefix := parts[1] == expected.ManifestSHA256[:12]
		if !sameCanonical && !expectedPrefix {
			continue
		}
		if preparedDirectArchiveIsV2(remote) && preparedDirectArchiveIsV2(expected) {
			if err := verifyDirectArchiveResumeMarker(cfg, archive.Name, expected); err != nil {
				return "", backupstrategy.PreparedDirectArchiveManifest{}, false, fmt.Errorf("reserved pending archive %q has no trusted local resume evidence: %w", archive.Name, err)
			}
		}
		if err := verifyCompatibleDirectArchiveResumeEnvelope(remote, expected); err != nil {
			return "", backupstrategy.PreparedDirectArchiveManifest{}, false, fmt.Errorf("reserved pending archive %q conflicts with the immutable resume envelope: %w", archive.Name, err)
		}
		matches = append(matches, pendingMatch{name: archive.Name, manifest: remote})
	}
	if len(matches) > 1 {
		return "", backupstrategy.PreparedDirectArchiveManifest{}, false, fmt.Errorf("multiple authenticated pending archives match the immutable resume envelope")
	}
	if len(matches) == 1 {
		return matches[0].name, matches[0].manifest, true, nil
	}
	return "", backupstrategy.PreparedDirectArchiveManifest{}, false, nil
}

func readAuthenticatedDirectArchiveManifest(ctx context.Context, runner BorgCommandRunner, cfg Config, archive string) (backupstrategy.PreparedDirectArchiveManifest, error) {
	manifestPath := path.Join(backupstrategy.DirectArchiveEvidenceDir, backupstrategy.DirectArchiveManifestFile)
	hashPath := path.Join(backupstrategy.DirectArchiveEvidenceDir, backupstrategy.DirectArchiveManifestHashFile)
	manifestRaw, err := runner.Run(ctx, BorgCommand{Args: []string{"extract", "--stdout", "::" + archive, manifestPath}})
	if err != nil {
		return backupstrategy.PreparedDirectArchiveManifest{}, err
	}
	hashRaw, err := runner.Run(ctx, BorgCommand{Args: []string{"extract", "--stdout", "::" + archive, hashPath}})
	if err != nil {
		return backupstrategy.PreparedDirectArchiveManifest{}, err
	}
	prepared, err := backupstrategy.ParseAuthenticatedDirectArchiveManifest(manifestRaw, hashRaw)
	if err != nil {
		return backupstrategy.PreparedDirectArchiveManifest{}, err
	}
	if preparedDirectArchiveBackend(prepared) != SnapshotBackendBorg || preparedDirectArchiveRepository(prepared) != cfg.Snapshots.Borg.Repository {
		return backupstrategy.PreparedDirectArchiveManifest{}, fmt.Errorf("archive manifest repository identity mismatch")
	}
	return prepared, nil
}

func verifyExpectedDirectArchiveIdentity(actual, expected backupstrategy.PreparedDirectArchiveManifest) error {
	if actual.ManifestSHA256 != expected.ManifestSHA256 {
		return fmt.Errorf("direct archive manifest identity mismatch")
	}
	return verifyDirectArchiveResumeEnvelope(actual, expected)
}

func verifyDirectArchiveResumeEnvelope(actual, expected backupstrategy.PreparedDirectArchiveManifest) error {
	actualEnvelope := immutableDirectArchiveResumeEnvelope(actual)
	expectedEnvelope := immutableDirectArchiveResumeEnvelope(expected)
	actualRaw, err := json.Marshal(actualEnvelope)
	if err != nil {
		return fmt.Errorf("encode authenticated resume envelope: %w", err)
	}
	expectedRaw, err := json.Marshal(expectedEnvelope)
	if err != nil {
		return fmt.Errorf("encode prepared resume envelope: %w", err)
	}
	if !bytes.Equal(actualRaw, expectedRaw) {
		return fmt.Errorf("direct archive immutable resume envelope mismatch")
	}
	return nil
}

func verifyCompatibleDirectArchiveResumeEnvelope(actual, expected backupstrategy.PreparedDirectArchiveManifest) error {
	if preparedDirectArchiveIsV2(actual) == preparedDirectArchiveIsV2(expected) {
		return verifyDirectArchiveResumeEnvelope(actual, expected)
	}
	if preparedDirectArchiveIsV2(actual) || !preparedDirectArchiveIsV2(expected) {
		return fmt.Errorf("direct archive manifest schema transition is not compatible")
	}
	actualRaw, err := json.Marshal(compatibleDirectArchiveResumeEnvelope(actual))
	if err != nil {
		return err
	}
	expectedRaw, err := json.Marshal(compatibleDirectArchiveResumeEnvelope(expected))
	if err != nil {
		return err
	}
	if !bytes.Equal(actualRaw, expectedRaw) {
		return fmt.Errorf("v1 archive does not match the v2 immutable resume envelope")
	}
	return nil
}

func compatibleDirectArchiveResumeEnvelope(prepared backupstrategy.PreparedDirectArchiveManifest) directArchiveResumeEnvelope {
	if prepared.ManifestV2 == nil {
		envelope := immutableDirectArchiveResumeEnvelope(prepared).(directArchiveResumeEnvelope)
		envelope.ManifestSchema = ""
		return envelope
	}
	manifest := prepared.ManifestV2
	roots := make([]directArchiveResumeEnvelopeRoot, len(manifest.Roots))
	for index, root := range manifest.Roots {
		roots[index] = directArchiveResumeEnvelopeRoot{Name: root.Name, SourcePath: root.SourcePath, ArchivePath: root.ArchivePath}
	}
	return directArchiveResumeEnvelope{
		HermesRecovery:  append([]hermesprofile.Evidence(nil), manifest.HermesRecovery...),
		Schema:          directArchiveResumeEnvelopeSchema,
		ManifestSchema:  "",
		Backend:         manifest.Backend,
		Repository:      manifest.Repository,
		ArchiveName:     manifest.ArchiveName,
		NodeID:          manifest.NodeID,
		ArchiveRef:      manifest.ArchiveRef,
		ArchiveClass:    manifest.ArchiveClass,
		CreatedAt:       manifest.CreatedAt,
		ExclusionPolicy: manifest.ExclusionPolicy,
		Exclusions:      append([]backupstrategy.DirectArchiveExclusion(nil), manifest.Exclusions...),
		Roots:           roots,
		OperationalPackage: backupstrategy.DirectArchiveOperationalPackageManifest{
			SourcePath: manifest.OperationalPackage.SourcePath, ArchivePath: manifest.OperationalPackage.ArchivePath,
			PackageID: manifest.OperationalPackage.PackageID, ManifestSHA256: manifest.OperationalPackage.ManifestSHA256,
			SchemaHead: manifest.OperationalPackage.SchemaHead, CreatedAt: manifest.OperationalPackage.CreatedAt,
			ArtifactCount: manifest.OperationalPackage.ArtifactCount, EntryCount: manifest.OperationalPackage.EntryCount,
			TotalBytes: manifest.OperationalPackage.TotalBytes, EntriesSHA256: manifest.OperationalPackage.EntriesSHA256,
			Entries: append([]backupstrategy.DirectArchiveEntry(nil), manifest.OperationalPackage.Entries...),
		},
		ProvenancePackage: backupstrategy.DirectArchiveProvenancePackageManifest{
			SourcePath: manifest.ProvenancePackage.SourcePath, ArchivePath: manifest.ProvenancePackage.ArchivePath,
			PackageID: manifest.ProvenancePackage.PackageID, ManifestSHA256: manifest.ProvenancePackage.ManifestSHA256,
			SchemaHead: manifest.ProvenancePackage.SchemaHead, GraphDigest: manifest.ProvenancePackage.GraphDigest,
			CompletedAt: manifest.ProvenancePackage.CompletedAt, DumpSizeBytes: manifest.ProvenancePackage.DumpSizeBytes,
			EntryCount: manifest.ProvenancePackage.EntryCount, TotalBytes: manifest.ProvenancePackage.TotalBytes,
			EntriesSHA256: manifest.ProvenancePackage.EntriesSHA256,
			Entries:       append([]backupstrategy.DirectArchiveEntry(nil), manifest.ProvenancePackage.Entries...),
		},
	}
}

func immutableDirectArchiveResumeEnvelope(prepared backupstrategy.PreparedDirectArchiveManifest) any {
	if prepared.ManifestV2 != nil {
		manifest := prepared.ManifestV2
		return directArchiveResumeEnvelopeV2{
			HermesRecovery:      append([]hermesprofile.Evidence(nil), manifest.HermesRecovery...),
			Schema:              directArchiveResumeEnvelopeSchema,
			ManifestSchema:      manifest.Schema,
			VerificationProfile: manifest.VerificationProfile,
			Backend:             manifest.Backend,
			Repository:          manifest.Repository,
			ArchiveName:         manifest.ArchiveName,
			NodeID:              manifest.NodeID,
			ArchiveRef:          manifest.ArchiveRef,
			ArchiveClass:        manifest.ArchiveClass,
			CreatedAt:           manifest.CreatedAt,
			ExclusionPolicy:     manifest.ExclusionPolicy,
			Exclusions:          append([]backupstrategy.DirectArchiveExclusion(nil), manifest.Exclusions...),
			Roots:               append([]backupstrategy.DirectArchiveRootManifestV2(nil), manifest.Roots...),
			OperationalPackage:  manifest.OperationalPackage,
			ProvenancePackage:   manifest.ProvenancePackage,
		}
	}
	manifest := prepared.Manifest
	roots := make([]directArchiveResumeEnvelopeRoot, len(manifest.Roots))
	for index, root := range manifest.Roots {
		roots[index] = directArchiveResumeEnvelopeRoot{Name: root.Name, SourcePath: root.SourcePath, ArchivePath: root.ArchivePath}
	}
	return directArchiveResumeEnvelope{
		HermesRecovery:     append([]hermesprofile.Evidence(nil), manifest.HermesRecovery...),
		Schema:             directArchiveResumeEnvelopeSchema,
		ManifestSchema:     manifest.Schema,
		Backend:            manifest.Backend,
		Repository:         manifest.Repository,
		ArchiveName:        manifest.ArchiveName,
		NodeID:             manifest.NodeID,
		ArchiveRef:         manifest.ArchiveRef,
		ArchiveClass:       manifest.ArchiveClass,
		CreatedAt:          manifest.CreatedAt,
		ExclusionPolicy:    manifest.ExclusionPolicy,
		Exclusions:         append([]backupstrategy.DirectArchiveExclusion(nil), manifest.Exclusions...),
		Roots:              roots,
		OperationalPackage: manifest.OperationalPackage,
		ProvenancePackage:  manifest.ProvenancePackage,
	}
}

func directArchiveResumeMarkerPath(cfg Config, prepared backupstrategy.PreparedDirectArchiveManifest) string {
	return filepath.Join(cfg.StateDir, "direct-archive-resume", prepared.ManifestSHA256+".json")
}

func expectedDirectArchiveResumeMarker(cfg Config, pendingArchive string, prepared backupstrategy.PreparedDirectArchiveManifest) directArchiveResumeMarker {
	return directArchiveResumeMarker{
		Schema:         directArchiveResumeMarkerSchema,
		Repository:     cfg.Snapshots.Borg.Repository,
		ArchiveName:    preparedDirectArchiveName(prepared),
		PendingArchive: pendingArchive,
		ManifestSHA256: prepared.ManifestSHA256,
	}
}

func writeDirectArchiveResumeMarker(cfg Config, pendingArchive string, prepared backupstrategy.PreparedDirectArchiveManifest) error {
	if !preparedDirectArchiveIsV2(prepared) || directPendingArchivePattern.FindStringSubmatch(pendingArchive) == nil {
		return fmt.Errorf("v2 pending archive resume marker identity is invalid")
	}
	marker := expectedDirectArchiveResumeMarker(cfg, pendingArchive, prepared)
	raw, err := json.Marshal(marker)
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	markerPath := directArchiveResumeMarkerPath(cfg, prepared)
	if err := os.MkdirAll(filepath.Dir(markerPath), 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(markerPath), ".resume-marker-*.json")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	cleanup := true
	defer func() {
		_ = temporary.Close()
		if cleanup {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return err
	}
	if _, err := temporary.Write(raw); err != nil {
		return err
	}
	if err := temporary.Sync(); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, markerPath); err != nil {
		return err
	}
	cleanup = false
	return nil
}

func verifyDirectArchiveResumeMarker(cfg Config, pendingArchive string, prepared backupstrategy.PreparedDirectArchiveManifest) error {
	raw, err := os.ReadFile(directArchiveResumeMarkerPath(cfg, prepared))
	if err != nil {
		return err
	}
	var marker directArchiveResumeMarker
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&marker); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("pending archive resume marker has trailing JSON")
		}
		return err
	}
	if marker != expectedDirectArchiveResumeMarker(cfg, pendingArchive, prepared) {
		return fmt.Errorf("pending archive resume marker mismatch")
	}
	return nil
}

func removeDirectArchiveResumeMarker(cfg Config, prepared backupstrategy.PreparedDirectArchiveManifest) error {
	err := os.Remove(directArchiveResumeMarkerPath(cfg, prepared))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func borgCommandHasExitCode(err error) bool {
	type exitCoder interface {
		ExitCode() int
	}
	var exit exitCoder
	return errors.As(err, &exit)
}

func writeDirectArchiveEvidence(stateDir string, prepared backupstrategy.PreparedDirectArchiveManifest) (string, error) {
	stateDir = filepath.Clean(strings.TrimSpace(stateDir))
	if !filepath.IsAbs(stateDir) {
		return "", fmt.Errorf("direct archive state directory must be absolute")
	}
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return "", err
	}
	root, err := os.MkdirTemp(stateDir, ".direct-archive-evidence-")
	if err != nil {
		return "", err
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.RemoveAll(root)
		}
	}()
	evidenceDir := filepath.Join(root, backupstrategy.DirectArchiveEvidenceDir)
	if err := os.Mkdir(evidenceDir, 0o700); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(evidenceDir, backupstrategy.DirectArchiveManifestFile), prepared.ManifestBytes, 0o600); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(evidenceDir, backupstrategy.DirectArchiveManifestHashFile), prepared.HashFileBytes, 0o600); err != nil {
		return "", err
	}
	createdAt := preparedDirectArchiveCreatedAt(prepared)
	evidenceTime := time.Unix(0, backupstrategy.DirectArchiveEvidenceModifiedUnixNano(createdAt)).UTC()
	for _, evidencePath := range []string{
		filepath.Join(evidenceDir, backupstrategy.DirectArchiveManifestFile),
		filepath.Join(evidenceDir, backupstrategy.DirectArchiveManifestHashFile),
		evidenceDir,
	} {
		if err := os.Chtimes(evidencePath, evidenceTime, evidenceTime); err != nil {
			return "", err
		}
	}
	cleanup = false
	return root, nil
}

func directArchiveCreateArgs(cfg Config, pendingArchive string, prepared backupstrategy.PreparedDirectArchiveManifest) []string {
	archiveTimestamp := preparedDirectArchiveCreatedAt(prepared).UTC().Truncate(time.Second).Format(time.RFC3339)
	args := []string{"create", "--json", "--stats", "--timestamp", archiveTimestamp}
	if prepared.ManifestV2 != nil {
		args = append(args, "--files-cache", "ctime,size,inode", "--files-changed", "ctime")
	}
	if cfg.Snapshots.Borg.Compression != "" {
		args = append(args, "--compression", cfg.Snapshots.Borg.Compression)
	}
	type sourceRoot struct {
		name        string
		sourcePath  string
		archivePath string
	}
	var (
		roots             []sourceRoot
		exclusions        []backupstrategy.DirectArchiveExclusion
		operationalSource string
		provenanceSource  string
	)
	if prepared.ManifestV2 != nil {
		manifest := prepared.ManifestV2
		roots = make([]sourceRoot, 0, len(manifest.Roots))
		for _, root := range manifest.Roots {
			roots = append(roots, sourceRoot{name: root.Name, sourcePath: root.SourcePath, archivePath: root.ArchivePath})
		}
		exclusions = manifest.Exclusions
		operationalSource = manifest.OperationalPackage.SourcePath
		provenanceSource = manifest.ProvenancePackage.SourcePath
	} else {
		manifest := prepared.Manifest
		roots = make([]sourceRoot, 0, len(manifest.Roots))
		for _, root := range manifest.Roots {
			roots = append(roots, sourceRoot{name: root.Name, sourcePath: root.SourcePath, archivePath: root.ArchivePath})
		}
		exclusions = manifest.Exclusions
		operationalSource = manifest.OperationalPackage.SourcePath
		provenanceSource = manifest.ProvenancePackage.SourcePath
	}
	rootArchivePaths := make(map[string]string, len(roots))
	for _, root := range roots {
		rootArchivePaths[root.name] = root.archivePath
	}
	for _, exclusion := range exclusions {
		args = append(args, "--exclude", "pp:"+path.Join(rootArchivePaths[exclusion.Root], exclusion.RelativePath))
	}
	args = append(args, "::"+pendingArchive, backupstrategy.DirectArchiveEvidenceDir)
	for _, root := range roots {
		args = append(args, root.sourcePath)
	}
	args = append(args, operationalSource, provenanceSource)
	return args
}

type directArchiveVerificationEvidence struct {
	MetadataDurationMS int64
	EnvelopeDurationMS int64
	PackedBytes        int64
	DeduplicatedBytes  int64
	HasQuickStats      bool
}

func readAndVerifyDirectArchiveForExpected(ctx context.Context, runner BorgCommandRunner, cfg Config, archive string, expected backupstrategy.PreparedDirectArchiveManifest) (backupstrategy.PreparedDirectArchiveManifest, directArchiveVerificationEvidence, error) {
	evidence := directArchiveVerificationEvidence{}
	infoStarted := time.Now()
	infoOut, err := runner.Run(ctx, BorgCommand{Args: []string{"info", "--json", "::" + archive}})
	evidence.MetadataDurationMS = time.Since(infoStarted).Milliseconds()
	if err != nil {
		return backupstrategy.PreparedDirectArchiveManifest{}, evidence, err
	}
	if preparedDirectArchiveIsV2(expected) {
		packed, deduplicated, statsErr := parseDirectArchiveQuickStats(infoOut)
		if statsErr != nil {
			return backupstrategy.PreparedDirectArchiveManifest{}, evidence, statsErr
		}
		evidence.PackedBytes = packed
		evidence.DeduplicatedBytes = deduplicated
		evidence.HasQuickStats = true
	}

	envelopeStarted := time.Now()
	prepared, err := readAuthenticatedDirectArchiveManifest(ctx, runner, cfg, archive)
	evidence.EnvelopeDurationMS = time.Since(envelopeStarted).Milliseconds()
	if err != nil {
		return backupstrategy.PreparedDirectArchiveManifest{}, evidence, err
	}

	checkArgs := []string{"check"}
	if preparedDirectArchiveIsV2(prepared) {
		checkArgs = append(checkArgs, "--archives-only")
	}
	checkArgs = append(checkArgs, "::"+archive)
	checkStarted := time.Now()
	if _, err := runner.Run(ctx, BorgCommand{Args: checkArgs}); err != nil {
		evidence.MetadataDurationMS += time.Since(checkStarted).Milliseconds()
		return backupstrategy.PreparedDirectArchiveManifest{}, evidence, err
	}
	evidence.MetadataDurationMS += time.Since(checkStarted).Milliseconds()
	if !preparedDirectArchiveIsV2(prepared) {
		if err := verifyDirectArchivePayload(ctx, runner, archive, prepared); err != nil {
			return backupstrategy.PreparedDirectArchiveManifest{}, evidence, err
		}
	}

	if err := verifyArchivedHermesRecovery(ctx, runner, archive, prepared); err != nil {
		return backupstrategy.PreparedDirectArchiveManifest{}, evidence, err
	}
	return prepared, evidence, nil
}

// readAndVerifyDirectArchive preserves the existing v1 reader contract for
// fetch and retention callers outside Slice 2. New routine creates use the
// expected-schema variant above so v2 can take its bounded verification path.
func readAndVerifyDirectArchive(ctx context.Context, runner BorgCommandRunner, cfg Config, archive string) (backupstrategy.PreparedDirectArchiveManifest, error) {
	if _, err := runner.Run(ctx, BorgCommand{Args: []string{"info", "--json", "::" + archive}}); err != nil {
		return backupstrategy.PreparedDirectArchiveManifest{}, err
	}
	if _, err := runner.Run(ctx, BorgCommand{Args: []string{"check", "::" + archive}}); err != nil {
		return backupstrategy.PreparedDirectArchiveManifest{}, err
	}
	prepared, err := readAuthenticatedDirectArchiveManifest(ctx, runner, cfg, archive)
	if err != nil {
		return backupstrategy.PreparedDirectArchiveManifest{}, err
	}
	if prepared.ManifestV2 != nil {
		return backupstrategy.PreparedDirectArchiveManifest{}, fmt.Errorf("v2 direct archive fetch and restore verification is not available before Slice 3")
	}
	if err := verifyDirectArchivePayload(ctx, runner, archive, prepared); err != nil {
		return backupstrategy.PreparedDirectArchiveManifest{}, err
	}
	return prepared, nil
}

func applyDirectArchiveVerificationEvidence(result *DirectArchiveResult, evidence directArchiveVerificationEvidence) {
	result.StageDurationsMS["metadata_check"] += evidence.MetadataDurationMS
	result.StageDurationsMS["envelope_authentication"] += evidence.EnvelopeDurationMS
	if evidence.HasQuickStats {
		result.PackedBytes = evidence.PackedBytes
		result.DeduplicatedBytes = evidence.DeduplicatedBytes
	}
}

func parseDirectArchiveQuickStats(payload []byte) (int64, int64, error) {
	type stats struct {
		CompressedSize    *int64 `json:"compressed_size"`
		CompressedCSize   *int64 `json:"compressed_csize"`
		DeduplicatedSize  *int64 `json:"deduplicated_size"`
		DeduplicatedCSize *int64 `json:"deduplicated_csize"`
	}
	type archive struct {
		Stats stats `json:"stats"`
	}
	var document struct {
		Archive  *archive  `json:"archive"`
		Archives []archive `json:"archives"`
	}
	if err := json.Unmarshal(payload, &document); err != nil {
		return 0, 0, fmt.Errorf("Borg 1.4.3 quick statistics JSON is invalid: %w", err)
	}
	var selected *stats
	if document.Archive != nil {
		selected = &document.Archive.Stats
	} else if len(document.Archives) == 1 {
		selected = &document.Archives[0].Stats
	}
	if selected == nil {
		return 0, 0, fmt.Errorf("Borg 1.4.3 quick statistics JSON has no exact archive stats")
	}
	packed := selected.CompressedSize
	if packed == nil {
		packed = selected.CompressedCSize
	}
	deduplicated := selected.DeduplicatedSize
	if deduplicated == nil {
		deduplicated = selected.DeduplicatedCSize
	}
	if packed == nil || deduplicated == nil || *packed < 0 || *deduplicated < 0 {
		return 0, 0, fmt.Errorf("Borg 1.4.3 quick statistics JSON is incomplete")
	}
	return *packed, *deduplicated, nil
}

type directArchiveExpectedEntry struct {
	Path  string
	Entry backupstrategy.DirectArchiveEntry
}

type directArchiveListItem struct {
	Type       string `json:"type"`
	Mode       string `json:"mode"`
	Path       string `json:"path"`
	LinkTarget string `json:"linktarget"`
	Size       int64  `json:"size"`
	SHA256     string `json:"sha256"`
	ISOMTime   string `json:"isomtime"`
	Healthy    bool   `json:"healthy"`
}

func verifyDirectArchivePayload(ctx context.Context, runner BorgCommandRunner, archive string, prepared backupstrategy.PreparedDirectArchiveManifest) error {
	expected, err := directArchiveExpectedEntries(prepared)
	if err != nil {
		return err
	}
	if err := verifyDirectArchiveList(ctx, runner, archive, expected); err != nil {
		return fmt.Errorf("verify Borg archive item metadata: %w", err)
	}
	return nil
}

func directArchiveExpectedEntries(prepared backupstrategy.PreparedDirectArchiveManifest) (map[string]directArchiveExpectedEntry, error) {
	expected := make(map[string]directArchiveExpectedEntry)
	add := func(archiveRoot string, entries []backupstrategy.DirectArchiveEntry) error {
		for _, entry := range entries {
			entryPath := archiveRoot
			if entry.Path != "." {
				entryPath = path.Join(archiveRoot, entry.Path)
			}
			if _, exists := expected[entryPath]; exists {
				return fmt.Errorf("duplicate expected archive path %q", entryPath)
			}
			expected[entryPath] = directArchiveExpectedEntry{Path: entryPath, Entry: entry}
		}
		return nil
	}
	for _, root := range prepared.Manifest.Roots {
		if err := add(root.ArchivePath, root.Entries); err != nil {
			return nil, err
		}
	}
	if err := add(prepared.Manifest.OperationalPackage.ArchivePath, prepared.Manifest.OperationalPackage.Entries); err != nil {
		return nil, err
	}
	if err := add(prepared.Manifest.ProvenancePackage.ArchivePath, prepared.Manifest.ProvenancePackage.Entries); err != nil {
		return nil, err
	}
	evidenceTime := backupstrategy.DirectArchiveEvidenceModifiedUnixNano(prepared.Manifest.CreatedAt)
	evidence := []struct {
		name    string
		mode    uint32
		payload []byte
	}{
		{name: backupstrategy.DirectArchiveEvidenceDir, mode: 0o700},
		{name: path.Join(backupstrategy.DirectArchiveEvidenceDir, backupstrategy.DirectArchiveManifestFile), mode: 0o600, payload: prepared.ManifestBytes},
		{name: path.Join(backupstrategy.DirectArchiveEvidenceDir, backupstrategy.DirectArchiveManifestHashFile), mode: 0o600, payload: prepared.HashFileBytes},
	}
	for index, item := range evidence {
		entry := backupstrategy.DirectArchiveEntry{Path: ".", Type: "directory", Mode: item.mode, ModifiedUnixNano: evidenceTime}
		if index > 0 {
			digest := sha256.Sum256(item.payload)
			entry.Type = "file"
			entry.SizeBytes = int64(len(item.payload))
			entry.SHA256 = hex.EncodeToString(digest[:])
		}
		if _, exists := expected[item.name]; exists {
			return nil, fmt.Errorf("reserved archive evidence path %q collides with a source entry", item.name)
		}
		expected[item.name] = directArchiveExpectedEntry{Path: item.name, Entry: entry}
	}
	return expected, nil
}

func verifyDirectArchiveList(ctx context.Context, runner BorgCommandRunner, archive string, expected map[string]directArchiveExpectedEntry) error {
	seen := make(map[string]struct{}, len(expected))
	command := BorgCommand{Args: []string{
		"list", "--json-lines", "--format", "{path}{type}{mode}{size}{isomtime}{linktarget}{sha256}{health}", "::" + archive,
	}}
	err := consumeBorgStream(ctx, runner, command, func(reader io.Reader) error {
		decoder := json.NewDecoder(bufio.NewReader(reader))
		for {
			var item directArchiveListItem
			if err := decoder.Decode(&item); errors.Is(err, io.EOF) {
				break
			} else if err != nil {
				return err
			}
			if !utf8.ValidString(item.Path) || !utf8.ValidString(item.LinkTarget) {
				return fmt.Errorf("archive item identity is not valid UTF-8")
			}
			want, ok := expected[item.Path]
			if !ok {
				return fmt.Errorf("unexpected archive item %q", item.Path)
			}
			if _, duplicate := seen[item.Path]; duplicate {
				return fmt.Errorf("duplicate archive item %q", item.Path)
			}
			if err := compareDirectArchiveListItem(item, want.Entry); err != nil {
				return fmt.Errorf("archive item %q: %w", item.Path, err)
			}
			seen[item.Path] = struct{}{}
		}
		return nil
	})
	if err != nil {
		return err
	}
	return directArchiveMissingEntries(expected, seen)
}

func compareDirectArchiveListItem(actual directArchiveListItem, expected backupstrategy.DirectArchiveEntry) error {
	if !actual.Healthy {
		return fmt.Errorf("Borg reports unhealthy content")
	}
	mode, kind, err := parseDirectArchiveBorgMode(actual.Mode)
	if err != nil {
		return err
	}
	wantKind := expected.Type
	if expected.HardlinkTarget != "" {
		wantKind = "hardlink"
	}
	if kind != wantKind || mode != expected.Mode {
		return fmt.Errorf("type or mode mismatch")
	}
	modified, err := time.ParseInLocation("2006-01-02T15:04:05.999999999", actual.ISOMTime, time.Local)
	if err != nil || modified.UnixNano() != expected.ModifiedUnixNano {
		return fmt.Errorf("mtime mismatch: got %q (%d) want %d", actual.ISOMTime, modified.UnixNano(), expected.ModifiedUnixNano)
	}
	switch wantKind {
	case "directory":
		if actual.Size != 0 || actual.SHA256 != "" || actual.LinkTarget != "" {
			return fmt.Errorf("directory metadata mismatch")
		}
	case "file":
		if actual.Size != expected.SizeBytes || actual.SHA256 != expected.SHA256 || actual.LinkTarget != "" {
			return fmt.Errorf("regular-file size, hash, or link evidence mismatch")
		}
	case "hardlink":
		if actual.Size != 0 || actual.SHA256 != "" || actual.LinkTarget != expected.HardlinkTarget {
			return fmt.Errorf("hard-link target mismatch")
		}
	case "symlink":
		if actual.Size != expected.SizeBytes || actual.SHA256 != "" || actual.LinkTarget != expected.LinkTarget {
			return fmt.Errorf("symbolic-link target or size mismatch")
		}
	default:
		return fmt.Errorf("unsupported expected item type %q", wantKind)
	}
	return nil
}

func parseDirectArchiveBorgMode(value string) (uint32, string, error) {
	if len(value) != 10 {
		return 0, "", fmt.Errorf("invalid Borg mode %q", value)
	}
	kind := map[byte]string{'d': "directory", '-': "file", 'h': "hardlink", 'l': "symlink"}[value[0]]
	if kind == "" {
		return 0, "", fmt.Errorf("unsupported Borg item type %q", value[:1])
	}
	var mode uint32
	for index, bit := range []uint32{0o400, 0o200, 0o100, 0o040, 0o020, 0o010, 0o004, 0o002, 0o001} {
		char := value[index+1]
		if char != '-' {
			switch index {
			case 2:
				if char == 's' || char == 'S' {
					mode |= 0o4000
				}
			case 5:
				if char == 's' || char == 'S' {
					mode |= 0o2000
				}
			case 8:
				if char == 't' || char == 'T' {
					mode |= 0o1000
				}
			}
			if char == 'r' || char == 'w' || char == 'x' || char == 's' || char == 't' {
				mode |= bit
			} else if char != 'S' && char != 'T' {
				return 0, "", fmt.Errorf("invalid Borg mode %q", value)
			}
		}
	}
	return mode, kind, nil
}

func consumeBorgStream(ctx context.Context, runner BorgCommandRunner, command BorgCommand, consume func(io.Reader) error) error {
	reader, writer := io.Pipe()
	runResult := make(chan error, 1)
	go func() {
		err := runner.RunStream(ctx, command, writer)
		_ = writer.CloseWithError(err)
		runResult <- err
	}()
	consumeErr := consume(reader)
	if consumeErr != nil {
		_ = reader.CloseWithError(consumeErr)
	} else {
		_ = reader.Close()
	}
	runErr := <-runResult
	if consumeErr != nil {
		return consumeErr
	}
	return runErr
}

func directArchiveMissingEntries(expected map[string]directArchiveExpectedEntry, seen map[string]struct{}) error {
	for itemPath := range expected {
		if _, ok := seen[itemPath]; !ok {
			return fmt.Errorf("archive item %q is missing", itemPath)
		}
	}
	return nil
}

func pendingBorgArchiveName(_ string, manifestSHA string) (string, error) {
	random := make([]byte, 4)
	if _, err := rand.Read(random); err != nil {
		return "", err
	}
	return "__loom-direct-pending-" + manifestSHA[:12] + "-" + hex.EncodeToString(random), nil
}

func directBorgArchiveName(nodeID, archiveRef string, archiveClasses ...string) string {
	archiveClass := backupstrategy.DirectArchiveClassUserData
	if len(archiveClasses) > 0 && strings.TrimSpace(archiveClasses[0]) != "" {
		archiveClass = strings.ReplaceAll(strings.TrimSpace(archiveClasses[0]), "-", "_")
	}
	classToken := strings.ReplaceAll(archiveClass, "_", "-")
	return "__loom-direct-" + safeRemoteSegment(classToken) + "-" + safeRemoteSegment(nodeID) + "-" + safeRemoteSegment(archiveRef)
}

func isDirectPendingBorgArchive(name string) bool {
	return strings.HasPrefix(name, "__loom-direct-pending-")
}

func isDirectCanonicalBorgArchive(name string) bool {
	return strings.HasPrefix(name, "__loom-direct-") && !isDirectPendingBorgArchive(name)
}

func directArchiveSnapshotItem(cfg Config, nodeID string, archive borgArchiveItem) (SnapshotItem, bool) {
	classes := []string{
		backupstrategy.DirectArchiveClassUserData,
		backupstrategy.DirectArchiveClassAcceptance,
		backupstrategy.DirectArchiveClassMilestone,
	}
	for _, archiveClass := range classes {
		prefix := "__loom-direct-" + strings.ReplaceAll(archiveClass, "_", "-") + "-" + nodeID + "-"
		if strings.HasPrefix(archive.Name, prefix) {
			ref := strings.TrimPrefix(archive.Name, prefix)
			if ref == "" {
				return SnapshotItem{}, false
			}
			return SnapshotItem{
				Ref: ref, Backend: SnapshotBackendBorg, Repository: cfg.Snapshots.Borg.Repository,
				Archive: archive.Name, ArchiveClass: archiveClass, RemotePrefix: "::" + archive.Name,
				RemoteURI: borgRemoteURI(cfg, archive.Name), Status: "discovered", DiscoveredAt: archive.Time,
			}, true
		}
	}
	legacyPrefix := "__loom-direct-" + nodeID + "-"
	if strings.HasPrefix(archive.Name, legacyPrefix) {
		ref := strings.TrimPrefix(archive.Name, legacyPrefix)
		if ref != "" {
			return SnapshotItem{
				Ref: ref, Backend: SnapshotBackendBorg, Repository: cfg.Snapshots.Borg.Repository,
				Archive: archive.Name, ArchiveClass: "legacy_unclassified", RemotePrefix: "::" + archive.Name,
				RemoteURI: borgRemoteURI(cfg, archive.Name), Status: "discovered", DiscoveredAt: archive.Time,
			}, true
		}
	}
	return SnapshotItem{}, false
}

func borgArchiveExists(archives []borgArchiveItem, name string) bool {
	for _, archive := range archives {
		if archive.Name == name {
			return true
		}
	}
	return false
}

func directArchiveFailure(result *DirectArchiveResult, code string, retryable bool, err error) error {
	result.Status = DirectArchiveStatusFailed
	result.Code = code
	result.Retryable = retryable
	result.Committed = false
	result.Error = err.Error()
	if code == DirectArchiveCodeConflict || errors.Is(err, ErrDirectArchiveConflict) {
		result.Status = DirectArchiveStatusConflict
		return fmt.Errorf("%w: %w", ErrDirectArchiveConflict, err)
	}
	if retryable {
		return fmt.Errorf("%w: %w", ErrDirectArchiveRetryable, err)
	}
	return err
}

func verifyArchivedHermesRecovery(ctx context.Context, runner BorgCommandRunner, archive string, prepared backupstrategy.PreparedDirectArchiveManifest) error {
	packages := prepared.Manifest.HermesRecovery
	if prepared.ManifestV2 != nil {
		packages = prepared.ManifestV2.HermesRecovery
	}
	if len(packages) > 0 {
		// Prove the entire recovery namespace first. A transient rogue sibling
		// captured by Borg cannot hide outside the per-package hash queries.
		recoveryRoot := strings.TrimPrefix(filepath.ToSlash(filepath.Dir(packages[0].Path)), "/")
		files := map[string]hermesprofile.File{}
		dirs := map[string]bool{recoveryRoot: true}
		for _, pkg := range packages {
			root := strings.TrimPrefix(filepath.ToSlash(pkg.Path), "/")
			if path.Dir(root) != recoveryRoot || dirs[root] || len(pkg.Files) != 2 {
				return fmt.Errorf("mixed Hermes recovery namespaces")
			}
			dirs[root] = true
			for _, f := range pkg.Files {
				if (f.Path != hermesprofile.ManifestFile && f.Path != hermesprofile.PayloadFile) || f.Mode != 0440 {
					return fmt.Errorf("invalid Hermes package file declaration")
				}
				if _, exists := files[root+"/"+f.Path]; exists {
					return fmt.Errorf("duplicate Hermes package file declaration")
				}
				files[root+"/"+f.Path] = f
			}
		}
		seen := map[string]bool{}
		readmePath := recoveryRoot + "/" + hermesprofile.RecoveryReadmeFile
		var rootOwner, readmeOwner *uint32
		command := BorgCommand{Args: []string{"list", "--json-lines", "--format", "{path}{type}{mode}{size}{uid}{linktarget}{health}", "::" + archive, "pp:" + recoveryRoot}}
		err := consumeBorgStream(ctx, runner, command, func(reader io.Reader) error {
			decoder := json.NewDecoder(io.LimitReader(reader, 16<<20))
			for {
				var item struct {
					directArchiveListItem
					UID *uint32 `json:"uid"`
				}
				if err := decoder.Decode(&item); err == io.EOF {
					break
				} else if err != nil {
					return err
				}
				mode, kind, err := parseDirectArchiveBorgMode(item.Mode)
				if err != nil || seen[item.Path] || !item.Healthy || item.LinkTarget != "" {
					return fmt.Errorf("unsafe frozen Hermes namespace")
				}
				seen[item.Path] = true
				if dirs[item.Path] {
					if kind != "directory" || (item.Path != recoveryRoot && mode != 0750) {
						return fmt.Errorf("invalid frozen Hermes directory")
					}
					if item.Path == recoveryRoot {
						rootOwner = item.UID
					}
				} else if item.Path == readmePath {
					// README is an ordinary workspace contract. Inspect only its
					// bounded metadata here; v1 retains ordinary-source hashes and
					// v2 deliberately does not add an ordinary-source hash pass.
					if kind != "file" || item.Type != "-" || item.Mode != "-rw-r--r--" || mode != hermesprofile.RecoveryReadmeMode || item.Size < 0 || item.Size > hermesprofile.RecoveryReadmeMaxBytes || item.UID == nil {
						return fmt.Errorf("unsafe frozen recovery README")
					}
					readmeOwner = item.UID
				} else if f, ok := files[item.Path]; !ok || kind != "file" || mode != f.Mode || item.Size != f.Size {
					return fmt.Errorf("undeclared frozen Hermes recovery entry")
				}
			}
			expected := len(dirs) + len(files)
			if seen[readmePath] {
				expected++
				if rootOwner == nil || readmeOwner == nil || *readmeOwner != *rootOwner {
					return fmt.Errorf("frozen recovery README owner mismatch")
				}
			}
			if len(seen) != expected {
				return fmt.Errorf("incomplete frozen Hermes namespace")
			}
			return nil
		})
		if err != nil {
			return err
		}
	}
	for _, pkg := range packages {
		archiveRoot := strings.TrimPrefix(filepath.ToSlash(pkg.Path), "/")
		want := make(map[string]hermesprofile.File, 2)
		for _, file := range pkg.Files {
			want[archiveRoot+"/"+file.Path] = file
		}
		seen := map[string]bool{}
		// Borg computes hashes only for the bounded recovery prefix. Listing also
		// proves that no substituted links, special files, modes or extra entries
		// entered the frozen package during its source enumeration.
		command := BorgCommand{Args: []string{"list", "--json-lines", "--format", "{path}{type}{mode}{size}{linktarget}{sha256}{health}", "::" + archive, "pp:" + archiveRoot}}
		err := consumeBorgStream(ctx, runner, command, func(reader io.Reader) error {
			decoder := json.NewDecoder(io.LimitReader(reader, 1<<20))
			for {
				var item directArchiveListItem
				if err := decoder.Decode(&item); err == io.EOF {
					break
				} else if err != nil {
					return err
				}
				if seen[item.Path] {
					return fmt.Errorf("duplicate archived Hermes entry")
				}
				seen[item.Path] = true
				mode, kind, err := parseDirectArchiveBorgMode(item.Mode)
				if err != nil || !item.Healthy || item.LinkTarget != "" {
					return fmt.Errorf("unsafe archived Hermes entry")
				}
				if item.Path == archiveRoot {
					if kind != "directory" || mode != 0750 {
						return fmt.Errorf("invalid archived Hermes directory")
					}
					continue
				}
				file, ok := want[item.Path]
				if !ok || kind != "file" || mode != file.Mode || item.Size != file.Size || item.SHA256 != file.SHA256 {
					return fmt.Errorf("archived Hermes recovery differs from authenticated evidence")
				}
			}
			if len(seen) != 3 {
				return fmt.Errorf("archived Hermes recovery is incomplete")
			}
			return nil
		})
		if err != nil {
			return err
		}
	}
	return nil
}
