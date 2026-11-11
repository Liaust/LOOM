package workspaceactions

import "testing"

func TestLifecycleClassificationUsesActualRegisteredRoot(t *testing.T) {
	active := Project{ID: "registered", Slug: "service", Root: "/box/custom/service", Status: "active"}
	archived := Project{ID: "old", Slug: "old", Root: "/storage/archive/projects/old/project", Status: "archived"}
	tests := []struct {
		in             Input
		from, to, want string
		items          []Project
	}{
		{Input{Action: "move", Root: "box", ToRoot: "storage"}, active.Root, "/storage/Projects/service", "archive_project", []Project{active}},
		{Input{Action: "move", Root: "storage", ToRoot: "box"}, archived.Root, "/box/Projects/old", "restore_project", []Project{archived}},
		{Input{Action: "move", Root: "box", ToRoot: "box"}, "/box/custom", "/box/other", "nested_projects", []Project{active}},
		{Input{Action: "move", Root: "box", ToRoot: "box"}, active.Root + "/notes", "/box/Notes/moved", "ordinary", []Project{active}},
		{Input{Action: "copy", Root: "box", ToRoot: "storage"}, active.Root, "/storage/Projects/snapshot", "project_export", []Project{active}},
		{Input{Action: "move", Root: "box", ToRoot: "storage", ToPath: "backups/foo"}, "/box/document", "/storage/backups/foo", "managed_destination", nil},
		{Input{Action: "move", Root: "box", ToRoot: "storage", ToPath: "Library/foo"}, "/box/document", "/storage/Library/foo", "archive_collection", nil},
		{Input{Action: "move", Root: "box", ToRoot: "box"}, "/box/Projects/unregistered", "/box/Notes/unregistered", "ordinary", nil},
		{Input{Action: "move", Root: "box", ToRoot: "box"}, active.Root, "/box/custom/service-2", "relocate_project", []Project{active}},
		{Input{Action: "inspect", Root: "storage", Path: "backups/node"}, "/storage/backups/node", "", "managed_custody", nil},
		{Input{Action: "inspect", Root: "storage", Path: "imports/mac/date/batch"}, "/storage/imports/mac/date/batch", "", "managed_custody", nil},
		{Input{Action: "inspect", Root: "storage", Path: "imports/mac/date/batch/research"}, "/storage/imports/mac/date/batch/research", "", "retained_content", nil},
		{Input{Action: "inspect", Root: "storage", Path: "Projects/research"}, "/storage/Projects/research", "", "retained_content", nil},
	}
	for _, tt := range tests {
		if got := Classify(tt.in, tt.from, tt.to, tt.items); got.Decision != tt.want {
			t.Fatalf("%+v: got %s want %s", tt.in, got.Decision, tt.want)
		}
	}
}

func TestProjectContainmentUsesComponents(t *testing.T) {
	if Within("/box/project-copy", "/box/project") || Within("/box/project", "") {
		t.Fatal("prefix is not containment")
	}
	if !Within("/box/project/notes", "/box/project") {
		t.Fatal("descendant missed")
	}
}

func TestCrossBoxTransferIsNeverAnOrdinaryRenameOrProjectRelocation(t *testing.T) {
	for _, action := range []string{"move", "copy"} {
		for _, roots := range [][2]string{{"box", "box:box_company"}, {"box:box_company", "box"}, {"box:box_company", "box:box_other"}} {
			for _, registered := range []bool{false, true} {
				var projects []Project
				if registered {
					projects = []Project{{ID: "project", Root: "/source/Projects/one", Status: "active"}}
				}
				review := Classify(Input{Action: action, Root: roots[0], ToRoot: roots[1]}, "/source/Projects/one", "/target/Projects/one", projects)
				if review.Ordinary || review.Decision != "cross_box_unsupported" {
					t.Fatalf("unsafe cross-Box classification: %+v", review)
				}
			}
		}
	}
	for _, root := range []string{"box", "box:box_company"} {
		if review := Classify(Input{Action: "move", Root: root, ToRoot: root}, "/source/Notes/a.md", "/source/Notes/b.md", nil); !review.Ordinary {
			t.Fatalf("same-Box move was blocked: %+v", review)
		}
	}
}
