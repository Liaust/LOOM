package notesworkspacesync

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"loom.local/loom/internal/notesworkspace"
)

type referenceDeliveryHold struct {
	reason string
	cause  error
}

type referenceAttemptInput struct {
	Fingerprint, PriorReason string
	Layout                   *FileMapping
	Size                     int64
}
type referenceAttempt struct {
	Scope                                                      Scope
	Fingerprint, FileID, ParentBinding                         string
	PathVersion                                                uint64
	Request                                                    map[string]any
	FailureMapping, FailureExport, FailureBinding, FailureHash string
	FailureRetryAt                                             time.Time
}

func referenceAttemptKey(scope Scope, fileID string) string {
	return "reference_attempt_" + key(scope.Workspace, scope.Collection, fileID)
}

func (e referenceDeliveryHold) Error() string { return e.reason }
func (e referenceDeliveryHold) Unwrap() error { return e.cause }
func referenceHold(reason string) error       { return referenceDeliveryHold{reason, ErrHeld} }

func deliveryFailureReason(err error, fallback string) string {
	if errors.Is(err, errMappingIndexPending) {
		return "binding_index_pending"
	}
	var hold referenceDeliveryHold
	if errors.As(err, &hold) {
		return hold.reason
	}
	return fallback
}

func metadataOnlyReferenceFailure(err error) bool {
	var hold referenceDeliveryHold
	return errors.As(err, &hold) || errors.Is(err, errMappingIndexPending)
}

func referencePublicationHold(reason string) error {
	switch reason {
	case "path_has_history", "base_unavailable", "base_not_live_file", "base_diverged", "publication_outcome_unknown":
		return referenceHold("reference_" + reason)
	default:
		return referenceHold("reference_publication_evidence_held")
	}
}

func stableReferenceHold(reason string) bool {
	switch reason {
	case "reference_path_has_history", "reference_base_unavailable", "reference_base_not_live_file", "reference_base_diverged", "reference_publication_outcome_unknown":
		return true
	default:
		return false
	}
}

func referenceEvidenceHold(reason string) bool {
	return stableReferenceHold(reason) || reason == "reference_export_evidence_held"
}

// This can refuse impossible work before a whole-PDF snapshot. Success is not
// authorization: the original source and exact publication fences still run.
func (s *Service) referencePreflight(ctx context.Context, scope Scope, relative string, input referenceAttemptInput) error {
	reader, ok := s.Source.(interface {
		ReferenceMetadata(context.Context, string, string) (notesworkspace.File, error)
	})
	if !ok {
		return nil
	}
	file, err := reader.ReferenceMetadata(ctx, scope.SourceCollection, relative)
	if err != nil && !missing(err) {
		return referenceDeliveryHold{"reference_source_metadata_unavailable", err}
	}
	return s.Store.WithReplica(ctx, s.Replica, s.Epoch, func(r Records) error {
		var mapping FileMapping
		if err == nil {
			var e error
			mapping, e = s.outgoingExportMapping(ctx, r, scope, file.ID)
			if errors.Is(e, errMappingIndexPending) {
				return e
			}
			if e != nil && !missing(e) {
				if errors.Is(e, ErrHeld) {
					return referenceHold("reference_mapping_evidence_held")
				}
				return e
			}
		}
		path := scope.Root + "/" + relative
		request := map[string]any{"path": path, "create": true}
		parentBinding := ""
		if mapping.Latest != nil {
			if mapping.Scope.Root != scope.Root {
				if !layoutScopeAllowed(ctx, r, mapping.Scope, scope) && !s.outgoingLayoutScopeAllowed(ctx, r, mapping.Scope, scope) {
					return referenceHold("reference_layout_evidence_held")
				}
				path = mapping.Scope.Root + "/" + relative
			}
			if mapping.Latest.Path != path {
				return referenceHold("reference_path_evidence_held")
			}
			request = map[string]any{"path": path, "baseRevision": mapping.Latest.NativeRevision, "soleParent": mapping.Latest.Generation != scope.Generation}
			parentBinding = mapping.Latest.ID
		}
		certified := false
		if file.ID != "" && input.Fingerprint != "" {
			attempt, e := load[referenceAttempt](ctx, r, "cursor", referenceAttemptKey(scope, file.ID))
			if e != nil && !missing(e) {
				return e
			}
			if e == nil && attempt.Scope == scope && attempt.Fingerprint == input.Fingerprint && attempt.FileID == file.ID && attempt.PathVersion == file.PathVersion && attempt.ParentBinding == parentBinding && attempt.Request["path"] == path {
				request = attempt.Request
				certified = true
			}
			if e == nil && attempt.Scope == scope && attempt.Fingerprint == input.Fingerprint && attempt.FileID == file.ID && attempt.PathVersion == file.PathVersion && attempt.ParentBinding == parentBinding && attempt.FailureHash != "" {
				now := time.Now()
				if s.Now != nil {
					now = s.Now()
				}
				cacheKey := layoutDependencyKey(scope, "reference_"+file.ID)
				if needed, _ := s.dependencyCache().needs(cacheKey, attempt.FailureHash, now, attempt.FailureRetryAt); !needed {
					return referenceHold("reference_export_evidence_held")
				}
				hash, err := s.referenceFailureHash(ctx, r, scope, attempt)
				if err != nil {
					return err
				}
				if hash == attempt.FailureHash {
					attempt.FailureRetryAt = now.Add(layoutEvidenceRetry)
					if err := r.Put(ctx, "cursor", referenceAttemptKey(scope, file.ID), attempt); err != nil {
						return err
					}
					s.dependencyCache().settled(cacheKey)
					return referenceHold("reference_export_evidence_held")
				}
			}
		}
		var result struct{ Status, Reason string }
		if err := s.call(ctx, "publication.preflight", request, &result); err != nil {
			return err
		}
		if result.Status != "ready" {
			hold := referencePublicationHold(result.Reason)
			// A matching completed receipt can recover a crash even if its old
			// parent is gone. The first uncertified probe cannot veto that replay.
			if certified || input.PriorReason == deliveryFailureReason(hold, "") {
				return hold
			}
		}
		return nil
	})
}

func (s *Service) referenceFailureHash(ctx context.Context, r Records, scope Scope, attempt referenceAttempt) (string, error) {
	cache := s.dependencyCache()
	version := cache.version()
	_, mapping, err := s.layoutDependency(ctx, r, scope, attempt.FailureMapping)
	if err != nil {
		return "", err
	}
	file, err := s.Source.File(ctx, attempt.FileID)
	if err != nil {
		return "", err
	}
	keys := append(cache.keys(layoutDependencyKey(scope, attempt.FailureMapping)), "source:"+attempt.FileID, layoutPathKey(file.CollectionID, file.RelativePath))
	values := []any{mapping, file}
	for _, id := range []struct{ kind, id string }{{"export", attempt.FailureExport}, {"binding", attempt.FailureBinding}, {"export", "published_" + key(attempt.FailureBinding)}} {
		if id.id == "" {
			values = append(values, nil)
			continue
		}
		keys = append(keys, "record:"+id.kind+":"+id.id)
		var raw json.RawMessage
		err := r.Get(ctx, id.kind, id.id, &raw)
		if missing(err) {
			values = append(values, "missing")
			continue
		}
		if err != nil {
			return "", err
		}
		var value any
		if err := json.Unmarshal(raw, &value); err != nil {
			return "", err
		}
		values = append(values, value)
	}
	raw, err := json.Marshal(values)
	hash := digest(raw)
	if err == nil {
		cache.record(layoutDependencyKey(scope, "reference_"+attempt.FileID), keys, hash, version)
	}
	return hash, err
}

func (s *Service) readExportReference(ctx context.Context, scope Scope, relative string, inputs ...referenceAttemptInput) (notesworkspace.ReadResult, string, error) {
	var input referenceAttemptInput
	if len(inputs) > 0 {
		input = inputs[0]
	}
	preflight := func() error { return s.referencePreflight(ctx, scope, relative, input) }
	if input.Layout != nil {
		preflight = func() error {
			return s.Store.WithReplica(ctx, s.Replica, s.Epoch, func(r Records) error {
				_, _, err := s.referenceRelocationReceipt(ctx, r, *input.Layout)
				if err != nil {
					return err
				}
				probe, err := s.referenceLayoutProbe(ctx, r, scope, *input.Layout, input.Fingerprint)
				if err == nil && probe.Status != "ready" {
					return referenceLayoutHold(probe.Reason)
				}
				return err
			})
		}
	}
	if err := preflight(); err != nil {
		s.ReferenceMetadataHeld++
		if !metadataOnlyReferenceFailure(err) {
			err = referenceDeliveryHold{"reference_preflight_unavailable", err}
		}
		return notesworkspace.ReadResult{}, "", err
	}
	if err := s.admitLayoutReference(input.Size); err != nil {
		return notesworkspace.ReadResult{}, "", err
	}
	started := time.Now()
	s.ReferenceReadAttempts++
	read, snapshot, err := readExportSource(ctx, s.Source, scope.SourceCollection, relative, s.ReferenceSpool)
	s.ReferenceReadMS += time.Since(started).Milliseconds()
	s.ReferenceReadBytes += read.ReferenceReadBytes
	if s.layoutBudget != nil {
		s.layoutBudget.bytes += read.ReferenceReadBytes
		s.layoutBudget.attempts++
	}
	if err == nil {
		s.ReferenceStagedBytes += read.Base.ReferenceSize
	}
	return read, snapshot, err
}

func (s *Service) referenceRelocationReceipt(ctx context.Context, r Records, mapping FileMapping) (FileMapping, BindingRecord, error) {
	current, err := load[FileMapping](ctx, r, "file", mapKey(Intent{Workspace: mapping.Scope.Workspace, Collection: mapping.Scope.Collection, Generation: mapping.Scope.Generation, FileID: mapping.ClientFileID}))
	if err != nil {
		return current, BindingRecord{}, err
	}
	if current.Latest == nil || mapping.Latest == nil || *current.Latest != *mapping.Latest || current.SourceFileID != mapping.SourceFileID || current.ClientFileID != mapping.ClientFileID || !sameExportScope(current.Scope, mapping.Scope) || current.Latest.Writable {
		return current, BindingRecord{}, referenceHold("reference_mapping_evidence_held")
	}
	record, err := load[BindingRecord](ctx, r, "binding", current.Latest.ID)
	if err != nil {
		return current, record, err
	}
	if record.Binding != *current.Latest || record.SourceFileID != current.SourceFileID || record.SourceBaseID == "" || record.Binding.SourceBase != "base_"+key(s.Replica, record.SourceBaseID) {
		return current, record, referenceHold("reference_binding_evidence_held")
	}
	return current, record, nil
}
