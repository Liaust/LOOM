package storagearchive

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"
)

func TestBoxWorkspaceReviewRoundTripKeepsSignedRootsAndIdentity(t *testing.T) {
	for _, id := range []string{"", "box_company"} {
		t.Run("box="+id, func(t *testing.T) {
			f := newWorkspaceMoveFixture(t)
			f.roots.BoxID, f.service.Roots.BoxID = id, id
			plan := f.plan(t)
			review, err := NewWorkspaceMovePlanReview(plan)
			if err != nil {
				t.Fatal(err)
			}
			raw, err := json.Marshal(review)
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Contains(raw, []byte(`"box_id"`)) != (id != "") {
				t.Fatalf("optional identity changed historical JSON: %s", raw)
			}
			var roundTrip WorkspaceMovePlanReview
			if err := json.Unmarshal(raw, &roundTrip); err != nil || !reflect.DeepEqual(roundTrip, review) {
				t.Fatal("review round trip changed", err)
			}
			if review.BoxID != id || review.Source.Root != WorkspaceRootBox || review.Destination.Root != WorkspaceRootStorage {
				t.Fatalf("logical refs or identity lost: %+v", review)
			}
			replanned, err := f.service.ReplanReviewed(t.Context(), roundTrip)
			if err != nil || replanned.PlanDigest != plan.PlanDigest || replanned.BoxID != id {
				t.Fatal("signed digest changed after review round trip", err)
			}
			if id != "" {
				roundTrip.BoxID = "box_foreign"
				if _, err := f.service.ReplanReviewed(t.Context(), roundTrip); err == nil {
					t.Fatal("review identity substitution accepted")
				}
			}
		})
	}
}

func TestBoxWorkspaceReviewRejectsInvalidIdentityWithoutChangingLegacyValidation(t *testing.T) {
	f := newWorkspaceMoveFixture(t)
	review, err := NewWorkspaceMovePlanReview(f.plan(t))
	if err != nil {
		t.Fatal(err)
	}
	review.BoxID = "../company"
	if err := ValidateWorkspaceMovePlanReview(review); err == nil {
		t.Fatal("invalid Box ID accepted")
	}
}
