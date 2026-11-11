package notesworkspacesync

import (
	"context"
	"encoding/json"
	"testing"
)

func TestReflectedControlIsNotUsefulIncomingWork(t *testing.T) {
	s, native, _, _, binding := fixture(t)
	native.add(binding)
	progress, err := s.Step(t.Context(), 128)
	if err != nil || progress.Imported != 1 || progress.Incoming != 0 || progress.MoreWork {
		t.Fatal("reflected binding prolonged source sweep", progress, err)
	}
	intent, publication := operation(binding, "new-input", "B", "2-edit")
	native.content(binding.Path, "2-edit", binding.NativeRevision, "B")
	native.add(intent)
	native.add(publication)
	progress, err = s.Step(t.Context(), 128)
	if err != nil || progress.Incoming != 2 || progress.Applied != 1 {
		t.Fatal("real client input did not remain actionable", progress, err)
	}
}

type emptyRawPage struct {
	Native
	first bool
}

func (n *emptyRawPage) Call(ctx context.Context, request map[string]any, out any) error {
	if request["method"] == "changes" && !n.first {
		n.first = true
		more := true
		raw, _ := json.Marshal(NativeChanges{Status: "available", Next: json.RawMessage(`0`), HasMore: &more})
		return json.Unmarshal(raw, out)
	}
	return n.Native.Call(ctx, request, out)
}

func TestFilteredRawPageStillDrainsFollowingClientInput(t *testing.T) {
	s, native, _, _, binding := fixture(t)
	intent, publication := operation(binding, "after-raw-page", "B", "2-edit")
	native.content(binding.Path, "2-edit", binding.NativeRevision, "B")
	native.add(intent)
	native.add(publication)
	s.Native = &emptyRawPage{Native: native}
	progress, err := s.Step(t.Context(), 128)
	if err != nil || !progress.MoreWork || progress.Imported != 0 {
		t.Fatal("filtered raw page falsely reported idle", progress, err)
	}
	progress, err = s.Step(t.Context(), 128)
	if err != nil || progress.Applied != 1 || progress.MoreWork {
		t.Fatal("following input did not drain to idle", progress, err)
	}
}
