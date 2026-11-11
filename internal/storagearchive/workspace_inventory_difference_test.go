package storagearchive

import (
	"strings"
	"testing"
)

func TestWorkspaceInventoryDifferenceIsBoundedAndMetadataOnly(t *testing.T) {
	want := NoFollowInventory{Entries: []NoFollowInventoryEntry{{RelativePath: "note.md", Mode: 0660, ContentDigest: "unchanged"}}}
	got := want
	got.Entries = append([]NoFollowInventoryEntry(nil), want.Entries...)
	got.Entries[0].Mode = 0670
	message := workspaceInventoryDifference(want, got)
	if !strings.Contains(message, `entry "note.md" mode 660 -> 670`) || !strings.Contains(message, "payload_changed=false") {
		t.Fatal(message)
	}
	for i := 0; i < 16; i++ {
		want.Entries = append(want.Entries, NoFollowInventoryEntry{RelativePath: "link", SymlinkTarget: "private-before"})
		got.Entries = append(got.Entries, NoFollowInventoryEntry{RelativePath: "link", SymlinkTarget: "private-after"})
	}
	message = workspaceInventoryDifference(want, got)
	if strings.Count(message, "entry ") != 8 || strings.Contains(message, "private-") || strings.Contains(message, "unchanged") {
		t.Fatal(message)
	}
}
