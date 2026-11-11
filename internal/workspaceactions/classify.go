package workspaceactions

import (
	"strings"

	"loom.local/loom/internal/boxregistry"
)

type Input struct {
	Action string `json:"action"`
	Root   string `json:"root"`
	Path   string `json:"path"`
	ToRoot string `json:"to_root,omitempty"`
	ToPath string `json:"to_path,omitempty"`
}

type Review struct {
	Input
	Decision string    `json:"decision"`
	Ordinary bool      `json:"ordinary"`
	Project  *Project  `json:"project,omitempty"`
	Related  []Project `json:"related,omitempty"`
	Message  string    `json:"message"`
}

func Managed(root, relative string) bool {
	if root != "storage" {
		return false
	}
	part := strings.Split(relative, "/")[0]
	return part == "" || part == ".loom" || part == "imports" || part == "backups" || part == "archive"
}

func Classify(in Input, from, to string, items []Project) Review {
	r := Review{Input: in, Decision: "ordinary", Ordinary: true, Related: items, Message: "Ordinary source-bound file operation."}
	if (in.Action == "move" || in.Action == "copy") && boxregistry.IsBoxRef(in.Root) && boxregistry.IsBoxRef(in.ToRoot) && in.Root != in.ToRoot {
		r.Decision, r.Ordinary = "cross_box_unsupported", false
		r.Message = "Cross-Box transfer requires an explicit source and destination review and is not supported by this operation. No files were changed."
		return r
	}
	var exact *Project
	for i := range items {
		p := &items[i]
		if p.Root == from {
			if exact != nil {
				r.Decision = "ambiguous_project"
				r.Ordinary = false
				r.Message = "Multiple registered identities occupy this path; resolve the registry mismatch first."
				return r
			}
			exact = p
		}
	}
	for _, p := range items {
		if p.Root != from && Within(p.Root, from) || to != "" && Within(p.Root, to) {
			r.Decision = "nested_projects"
			r.Ordinary = false
			r.Message = "The selected path contains or would replace a registered project. Select the exact project for a lifecycle operation."
			return r
		}
	}
	if exact != nil {
		p := *exact
		r.Project = &p
		r.Ordinary = false
		if in.Action == "copy" {
			r.Decision = "project_export"
			r.Message = "Export a snapshot without changing the original project lifecycle or registering a duplicate identity."
			return r
		}
		if p.Status == "archived" {
			if in.Action == "move" && boxregistry.IsBoxRef(in.ToRoot) {
				r.Decision = "restore_project"
				r.Message = "Restore this project to Box. Its services and schedules remain stopped."
			} else {
				r.Decision = "archived_project"
				r.Message = "This is a retained project. Browse it read-only, restore it, or explicitly export a working copy."
			}
			return r
		}
		if in.Action == "delete" || in.Action == "move" && in.ToRoot == "storage" {
			r.Decision = "archive_project"
			r.Message = "Archive this registered project with its runtime and recovery state, rather than moving its folder directly."
		} else if in.Action == "inspect" {
			r.Decision = "project"
			r.Message = "Registered live project; ordinary child files remain editable."
		} else {
			r.Decision = "relocate_project"
			r.Message = "Relocating a live project must update its registration and known runtime references."
		}
		return r
	}
	if in.Action == "move" && Managed(in.ToRoot, in.ToPath) {
		r.Decision = "managed_destination"
		r.Ordinary = false
		r.Message = "Choose a named content destination. Imports, backups and custody records are managed, not general writable folders."
		return r
	}
	if in.Root == "storage" {
		if Managed(in.Root, in.Path) && !(strings.HasPrefix(in.Path, "imports/") && len(strings.Split(in.Path, "/")) >= 5) {
			r.Decision = "managed_custody"
			r.Ordinary = false
			r.Message = "This is a managed recovery or transfer container. Browse it read-only; moving it requires its custody owner."
			return r
		}
		r.Decision = "retained_content"
		r.Ordinary = false
		r.Message = "Retained content is read-only. Use a reviewed relocation, restore or working-copy operation."
		return r
	}
	if in.Action == "move" && in.ToRoot == "storage" {
		r.Decision = "archive_collection"
		r.Ordinary = false
		r.Message = "Retain this collection in named Storage without archiving its enclosing project."
		for i := range items {
			if Within(from, items[i].Root) && from != items[i].Root && (r.Project == nil || len(items[i].Root) > len(r.Project.Root)) {
				p := items[i]
				r.Project = &p
			}
		}
	}
	return r
}
