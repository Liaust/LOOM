package notesworkspacesync

import (
	"context"

	"loom.local/loom/internal/notesworkspace"
)

// DesktopSource exposes the existing source journal without starting a native
// replica or changing its epoch. The same current enrollment and custody fences
// still admit every read and mutation, independently of any Obsidian process.
func (r *Runtime) DesktopSource(ctx context.Context) (notesworkspace.Service, error) {
	selections, _, err := r.enrollmentSelections(ctx)
	if err != nil {
		return notesworkspace.Service{}, err
	}
	resolver := notesworkspace.KnowledgeResolver{Knowledge: r.Knowledge, NodeKey: r.NodeKey,
		WithSelection: func(_ context.Context, id string, fn func(string) error) error {
			for _, selection := range selections {
				if selection.SourceCollection != id || selection.Archived || selection.ReadOnly {
					continue
				}
				generation := selection.Generation
				if selection.PathPrefix != "" {
					generation = key(generation, selection.PathPrefix)
				}
				return fn(generation)
			}
			return notesworkspace.ErrMembership
		},
	}
	return notesworkspace.Service{Store: notesworkspace.SQLStore{DB: r.DB},
		Sources: selectedSources{PathResolver: resolver, Selections: selections}}, nil
}
