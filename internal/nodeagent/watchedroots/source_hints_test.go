package watchedroots

import "testing"

func TestSourceHintsPublicationDoesNotDependOnScannerQueue(t *testing.T) {
	s := NewStore(t.TempDir())
	root := ValidatedRoot{RootPath: t.TempDir(), ConfigHash: "current", Config: RootConfig{RootKey: "notes"}}
	if err := s.EnsureRoot("notes"); err != nil {
		t.Fatal(err)
	}
	if err := s.ClearDirtyHints("notes", nil); err != nil {
		t.Fatal(err)
	}
	if err := s.publishSourceHints(root, []DirtyHint{{RelativePath: "a.md", HintKind: DirtyHintModified}}); err != nil {
		t.Fatal(err)
	}
	first, err := s.LoadSourceHints("notes")
	if err != nil || first.Hints["a.md"].Generation == "" {
		t.Fatal(first, err)
	}
	if err := s.publishSourceHints(root, []DirtyHint{{RelativePath: "a.md", HintKind: DirtyHintModified}}); err != nil {
		t.Fatal(err)
	}
	second, err := s.LoadSourceHints("notes")
	if err != nil || first.Hints["a.md"].Generation == second.Hints["a.md"].Generation {
		t.Fatal("same-path change was lost", err)
	}
	root.ConfigHash = "replacement"
	if err := s.publishSourceHints(root, []DirtyHint{{HintKind: DirtyHintRescanRequired}}); err != nil {
		t.Fatal(err)
	}
	replaced, err := s.LoadSourceHints("notes")
	if err != nil || len(replaced.Hints) != 1 {
		t.Fatal("old scope hints retained", replaced, err)
	}
}
