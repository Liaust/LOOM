package knowledge

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"loom.local/loom/internal/notesprojection"
)

func projectionTestSources(n int) []notesprojection.SourceObject {
	sources := make([]notesprojection.SourceObject, n)
	for i := range sources {
		sources[i] = notesprojection.SourceObject{KnowledgeObjectID: fmt.Sprintf("object_%05d", i), RelativePath: "collision.md"}
	}
	return sources
}

func TestCollectProjectionSourcesCompletePages(t *testing.T) {
	for _, count := range []int{0, projectionSourcePageSize, 2 * projectionSourcePageSize, 6071} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			want := projectionTestSources(count)
			offset, calls := 0, 0
			got, err := collectProjectionSources(t.Context(), 0, func(context.Context) ([]notesprojection.SourceObject, error) {
				calls++
				end := min(offset+projectionSourcePageSize, len(want))
				page := want[offset:end]
				offset = end
				return page, nil
			})
			if err != nil || len(got) != count || (count > 0 && !reflect.DeepEqual(got, want)) {
				t.Fatalf("inventory count/order: %d, %v", len(got), err)
			}
			if calls != count/projectionSourcePageSize+1 {
				t.Fatalf("fetches = %d; exact boundaries require exhaustion fetch", calls)
			}
		})
	}
}

func TestCollectProjectionSourcesExplicitLimit(t *testing.T) {
	for _, count := range []int{projectionSourcePageSize, projectionSourcePageSize + 1} {
		sources := projectionTestSources(count)
		offset := 0
		got, err := collectProjectionSources(t.Context(), projectionSourcePageSize, func(context.Context) ([]notesprojection.SourceObject, error) {
			end := min(offset+projectionSourcePageSize, len(sources))
			page := sources[offset:end]
			offset = end
			return page, nil
		})
		if count == projectionSourcePageSize {
			if err != nil || len(got) != count {
				t.Fatalf("exact explicit limit: %d %v", len(got), err)
			}
		} else if err == nil || !strings.Contains(err.Error(), "explicit limit") || got != nil {
			t.Fatalf("truncation not reported: %d %v", len(got), err)
		}
	}
}

func TestCollectProjectionSourcesDiscardsFailedInventory(t *testing.T) {
	for _, failure := range []string{"page_error", "cancel", "duplicate", "empty_identity", "oversized_page"} {
		t.Run(failure, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			calls := 0
			got, err := collectProjectionSources(ctx, 0, func(context.Context) ([]notesprojection.SourceObject, error) {
				calls++
				if calls == 1 {
					return projectionTestSources(projectionSourcePageSize), nil
				}
				switch failure {
				case "page_error":
					return projectionTestSources(1), errors.New("failed fetch")
				case "cancel":
					cancel()
					return nil, nil
				case "duplicate":
					return projectionTestSources(1), nil
				case "empty_identity":
					return []notesprojection.SourceObject{{}}, nil
				default:
					return projectionTestSources(projectionSourcePageSize + 1), nil
				}
			})
			if err == nil || got != nil || calls != 2 {
				t.Fatalf("partial inventory escaped: %d sources, %d calls, %v", len(got), calls, err)
			}
			if failure == "cancel" && !errors.Is(err, context.Canceled) {
				t.Fatalf("cancellation lost: %v", err)
			}
		})
	}
}

func TestProjectionPathCollisionsAcrossPages(t *testing.T) {
	want := projectionTestSources(projectionSourcePageSize + 1)
	for i := range want {
		want[i].RootKind, want[i].SourceNodeKey = notesprojection.RootKindBoxNotes, "main"
	}
	offset := 0
	got, err := collectProjectionSources(t.Context(), 0, func(context.Context) ([]notesprojection.SourceObject, error) {
		end := min(offset+projectionSourcePageSize, len(want))
		page := want[offset:end]
		offset = end
		return page, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	root, now := t.TempDir(), time.Now()
	entries, findings := notesprojection.BuildProjectionEntries(root, got, now)
	baseline, _ := notesprojection.BuildProjectionEntries(root, want, now)
	if len(findings) != 0 || !reflect.DeepEqual(entries, baseline) || len(entries) != len(want) {
		t.Fatal("page boundary changed collision assignment")
	}
	paths, identities := map[string]bool{}, map[string]bool{}
	for _, entry := range entries {
		if paths[entry.ProjectedPath] || identities[entry.KnowledgeObjectID] {
			t.Fatal("path collision lost stable identity")
		}
		paths[entry.ProjectedPath], identities[entry.KnowledgeObjectID] = true, true
	}
}
