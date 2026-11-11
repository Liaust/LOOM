package boxregistry

import "context"

type selectionKey struct{}

// WithSelection binds discovery to this request/run, never a process-wide default.
func WithSelection(ctx context.Context, selection ResolvedSelection) context.Context {
	selection.BoxIDs = append([]string(nil), selection.BoxIDs...)
	return context.WithValue(ctx, selectionKey{}, selection)
}

// FromContext is absent for intentional internal/global reconciliation callers.
func FromContext(ctx context.Context) (ResolvedSelection, bool) {
	selection, ok := ctx.Value(selectionKey{}).(ResolvedSelection)
	selection.BoxIDs = append([]string(nil), selection.BoxIDs...)
	return selection, ok
}
