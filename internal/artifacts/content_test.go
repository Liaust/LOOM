package artifacts

import (
	"errors"
	"testing"

	"loom.local/loom/internal/ids"
	"loom.local/loom/internal/requestctx"
)

func TestArtifactReadRejectsUntrustedIdentityAndInvalidLimitBeforeStorage(t *testing.T) {
	service := Service{}
	if _, err := service.ReadContent(t.Context(), requestctx.Context{}, "artifact", 0, false); !errors.Is(err, ErrReadDenied) {
		t.Fatalf("untrusted identity: %v", err)
	}
	req := requestctx.Context{ActorID: ids.NewActorID(), OriginNodeID: ids.NewNodeID()}
	for _, limit := range []int{-1, MaximumContentBytes + 1} {
		if _, err := service.ReadContent(t.Context(), req, "artifact", limit, false); !errors.Is(err, ErrInvalidRead) {
			t.Fatalf("limit %d: %v", limit, err)
		}
	}
}
