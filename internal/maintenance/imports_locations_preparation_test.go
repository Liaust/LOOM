package maintenance

import (
	"context"
	"errors"
	"testing"
)

type importsPreparationKey struct{}

type preparingImportsResolver struct {
	importsLocationsTestResolver
	prepare func(context.Context, string, string) (context.Context, error)
}

func (r *preparingImportsResolver) PrepareLaneCustodyResolution(ctx context.Context, source, batch string) (context.Context, error) {
	return r.prepare(ctx, source, batch)
}

func TestImportsLocationsDelegatesResolutionPreparation(t *testing.T) {
	calls := 0
	delegate := &preparingImportsResolver{prepare: func(ctx context.Context, source, batch string) (context.Context, error) {
		calls++
		if source != "source" || batch != "batch" {
			t.Fatal("wrong identity reached preparer")
		}
		return context.WithValue(ctx, importsPreparationKey{}, true), nil
	}}
	bounded := &importsLocationsResolver{resolver: delegate, source: "source", batch: "batch"}
	ctx, err := bounded.PrepareLaneCustodyResolution(context.Background(), "source", "batch")
	if err != nil || ctx.Value(importsPreparationKey{}) != true || calls != 1 {
		t.Fatalf("preparation not delegated: %v", err)
	}
	if _, err = bounded.PrepareLaneCustodyResolution(ctx, "other", "batch"); err == nil || calls != 1 {
		t.Fatal("bounded identity guard was bypassed")
	}
	failure := errors.New("root lookup refused")
	delegate.prepare = func(context.Context, string, string) (context.Context, error) { return nil, failure }
	if _, err = bounded.PrepareLaneCustodyResolution(ctx, "source", "batch"); !errors.Is(err, failure) {
		t.Fatalf("preparation error was lost: %v", err)
	}
	bounded.resolver = &importsLocationsTestResolver{}
	if same, err := bounded.PrepareLaneCustodyResolution(ctx, "source", "batch"); err != nil || same != ctx {
		t.Fatal("legacy resolver compatibility changed")
	}
}
