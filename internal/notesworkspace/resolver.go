package notesworkspace

import (
	"context"

	"loom.local/loom/internal/knowledge"
)

// KnowledgeResolver consumes existing declaration roots. WithSelection holds the
// runtime's selection fence throughout its callback, or returns ErrMembership.
// The runtime must advance the generation on withdrawal/re-enrollment. This seam
// deliberately does not invent a second source-root registry.
type KnowledgeResolver struct {
	Knowledge     *knowledge.Service
	NodeKey       string
	WithSelection func(context.Context, string, func(generation string) error) error
}

func (r KnowledgeResolver) WithSource(ctx context.Context, id, relative string, fn func(Source) error) error {
	return r.WithPaths(ctx, id, []string{relative}, fn)
}

func (r KnowledgeResolver) WithPaths(ctx context.Context, id string, relatives []string, fn func(Source) error) error {
	if r.Knowledge == nil || r.WithSelection == nil {
		return ErrMembership
	}
	return r.WithSelection(ctx, id, func(generation string) error {
		if generation == "" {
			return ErrMembership
		}
		callback := func(root knowledge.SourceRoot, ownerGeneration string) error {
			return fn(Source{CollectionID: id, Generation: identity([]string{generation, ownerGeneration}), Path: root.SourcePath})
		}
		if len(relatives) == 1 && IsReferencePath(relatives[0]) {
			return r.Knowledge.WithReferenceNotesSource(ctx, id, r.NodeKey, relatives[0], callback)
		}
		return r.Knowledge.WithWritableNotesSourcePaths(ctx, id, r.NodeKey, relatives, callback)
	})
}
