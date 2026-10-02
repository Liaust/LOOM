package notesworkspacesync

import "context"

// OutcomeStatus intentionally excludes native payloads, source paths and raw
// exceptions. Runtime can expose these compact states through its existing UI.
type OutcomeStatus struct {
	IntentID, Status, Reason string
	Acknowledged             bool
}
type OutcomePage struct {
	Items []OutcomeStatus
	Next  string
}

func (s *Service) Outcomes(ctx context.Context, after string, limit int) (out OutcomePage, err error) {
	if err = s.valid(); err != nil {
		return
	}
	if limit < 1 || limit > 128 {
		return out, ErrHeld
	}
	err = s.Store.WithReplica(ctx, s.Replica, s.Epoch, func(r Records) error {
		ids, e := r.List(ctx, "operation", after, limit)
		if e != nil {
			return e
		}
		for _, id := range ids {
			j, e := load[Join](ctx, r, "operation", id)
			if e != nil {
				return e
			}
			out.Items = append(out.Items, OutcomeStatus{IntentID: id, Status: j.Status, Reason: j.Reason, Acknowledged: j.AckPublished})
			out.Next = id
		}
		return nil
	})
	return
}

// EvidenceStatus makes stock/missing/invalid-control holds inspectable without
// handing raw note content to a runtime status endpoint.
type EvidenceStatus struct{ ID, NativeID, Revision, Reason string }
type EvidencePage struct {
	Items []EvidenceStatus
	Next  string
}

func (s *Service) Evidence(ctx context.Context, after string, limit int) (out EvidencePage, err error) {
	if err = s.valid(); err != nil {
		return
	}
	if limit < 1 || limit > 128 {
		return out, ErrHeld
	}
	err = s.Store.WithReplica(ctx, s.Replica, s.Epoch, func(r Records) error {
		ids, e := r.List(ctx, "observation", after, limit)
		if e != nil {
			return e
		}
		for _, id := range ids {
			ob, e := load[Observation](ctx, r, "observation", id)
			if e != nil {
				return e
			}
			out.Items = append(out.Items, EvidenceStatus{ID: id, NativeID: ob.Evidence.ID, Revision: ob.Evidence.Revision, Reason: ob.Reason})
			out.Next = id
		}
		return nil
	})
	return
}
