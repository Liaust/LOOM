package notesworkspacesync

import "context"

// claimResolutions runs under the existing replica lock, after evidence and
// source-base validation but before Source.Save. A retry retains the same
// claim, including across the source-commit/receipt gap. Original receipts and
// competing payloads are never rewritten to fabricate successful acceptance.
func (s *Service) claimResolutions(ctx context.Context, r Records, i Intent, j Join) (string, error) {
	if len(i.Resolves) == 0 {
		return "", nil
	}
	conflicts := make([]Join, 0, len(i.Resolves))
	for _, id := range i.Resolves {
		old, err := load[Join](ctx, r, "operation", id)
		if missing(err) {
			return "resolution_conflict_unavailable", nil
		}
		if err != nil {
			return "", err
		}
		original, err := parseIntent([]byte(old.IntentRaw))
		publication, pe := parsePublication([]byte(old.PublicationRaw))
		if err != nil || pe != nil || old.Reason == "control_collision" ||
			original.ID != id || original.Operation != "edit" || original.Path != i.Path ||
			mapKey(original) != mapKey(i) || old.SourceFileID != j.SourceFileID ||
			old.Ack == nil || old.Ack.Status != "conflict" || old.Ack.Reason != "source_conflict" ||
			!paired(original, publication) || publication.IntentDigest != digest([]byte(old.IntentRaw)) ||
			old.Ack.IntentID != id || old.Ack.IntentDigest != publication.IntentDigest || old.Ack.PublicationID != publication.ID {
			return "resolution_conflict_mismatch", nil
		}
		if old.ResolutionID != "" && old.ResolutionID != i.ID {
			attempt, err := load[Join](ctx, r, "operation", old.ResolutionID)
			if err != nil && !missing(err) {
				return "", err
			}
			if attempt.Ack != nil && attempt.Ack.Status == "applied" {
				return "conflict_already_resolved", nil
			}
			// A failed stale-source choice remains history and can be reviewed
			// again. An unfinished attempt must recover, not be raced past.
			if attempt.Ack == nil || attempt.Ack.Status != "conflict" {
				return "resolution_in_progress", nil
			}
		}
		conflicts = append(conflicts, old)
	}
	// Validate the complete set before changing any claim. Partial durable
	// writes after an I/O failure are safely finished by this same intent ID.
	for index, old := range conflicts {
		old.ResolutionID = i.ID
		if err := r.Put(ctx, "operation", i.Resolves[index], old); err != nil {
			return "", err
		}
	}
	return "", nil
}
