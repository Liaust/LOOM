package lane

import (
	"context"
	"errors"
	"testing"
)

type preparedLaneTestKey struct{}

type preparingLaneCatalog struct {
	*resolvingLaneCatalog
	prepares int
	prepare  func(context.Context, string, string) (context.Context, error)
	t        *testing.T
}

func (c *preparingLaneCatalog) PrepareLaneCustodyResolution(ctx context.Context, source, batch string) (context.Context, error) {
	c.prepares++
	return c.prepare(ctx, source, batch)
}

func (c *preparingLaneCatalog) ResolveLaneCustodyMember(ctx context.Context, source, batch, member string) (string, error) {
	if ctx.Value(preparedLaneTestKey{}) != true {
		c.t.Fatal("prepared context did not reach member resolution")
	}
	return c.resolvingLaneCatalog.ResolveLaneCustodyMember(ctx, source, batch, member)
}

func TestCommittedLaneResolutionPreparation(t *testing.T) {
	fixture := newCommittedLaneFixture(t)
	catalog := &preparingLaneCatalog{
		t: t, resolvingLaneCatalog: &resolvingLaneCatalog{resolve: func(_, _, _ string) (string, error) { return "", nil }},
		prepare: func(ctx context.Context, source, batch string) (context.Context, error) {
			if source != fixture.input.SourceNodeKey || batch != fixture.input.BatchID {
				t.Fatal("wrong preparation identity")
			}
			return context.WithValue(ctx, preparedLaneTestKey{}, true), nil
		},
	}
	for pass := 0; pass < 2; pass++ {
		if _, _, err := VerifyCommittedCustody(context.Background(), fixture.root, fixture.input.SourceNodeKey, fixture.input.BatchID, fixture.input.AcceptedDate, catalog); err != nil {
			t.Fatal(err)
		}
	}
	if catalog.prepares != 2 || len(catalog.calls) != 14 {
		t.Fatalf("wrong batch/member counts: %d/%d", catalog.prepares, len(catalog.calls))
	}
	if _, _, err := VerifyCommittedCustody(context.Background(), fixture.root, "wrong-node", fixture.input.BatchID, fixture.input.AcceptedDate, catalog); err == nil || catalog.prepares != 2 {
		t.Fatal("preparation preceded manifest identity validation")
	}
	fixture.assertManifestUnchanged(t)
}

func TestCommittedLaneResolutionPreparationFailsClosed(t *testing.T) {
	fixture := newCommittedLaneFixture(t)
	failure := errors.New("batch root unavailable")
	for _, nilContext := range []bool{false, true} {
		catalog := &preparingLaneCatalog{
			t: t, resolvingLaneCatalog: &resolvingLaneCatalog{},
			prepare: func(context.Context, string, string) (context.Context, error) {
				if nilContext {
					return nil, nil
				}
				return nil, failure
			},
		}
		_, _, err := VerifyCommittedCustody(context.Background(), fixture.root, fixture.input.SourceNodeKey, fixture.input.BatchID, fixture.input.AcceptedDate, catalog)
		if err == nil || !nilContext && !errors.Is(err, failure) || len(catalog.calls) != 0 {
			t.Fatalf("invalid preparation reached members: %v", err)
		}
		fixture.assertManifestUnchanged(t)
	}
}
