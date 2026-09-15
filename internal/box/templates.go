package box

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type templateFile struct {
	Key            string
	RelPath        string
	Mode           fileMode
	Render         func(Contract) string
	Required       bool
	Area           string
	UpdateExisting func(string) bool
}

type fileMode uint32

const defaultFileMode fileMode = 0o644

func defaultTemplateFiles() []templateFile {
	return []templateFile{
		{Key: "root_readme", RelPath: "README.md", Mode: defaultFileMode, Render: rootReadme, Required: false},
		{Key: "metadata_readme", RelPath: ".loom/README.md", Mode: defaultFileMode, Render: metadataReadme, Required: false},
		{Key: "projects_readme", RelPath: "Projects/README.md", Mode: defaultFileMode, Render: projectsReadme, Required: false},
		{Key: "notes_readme", RelPath: "Notes/README.md", Mode: defaultFileMode, Render: notesReadme, Required: false},
		{Key: "notes_agents", RelPath: "Notes/AGENTS.md", Mode: defaultFileMode, Render: notesAgents, Required: false},
		{Key: "documents_readme", RelPath: "Documents/README.md", Mode: defaultFileMode, Render: documentsReadme, Required: false, Area: AreaDocuments},
		{Key: "documents_agents", RelPath: "Documents/AGENTS.md", Mode: defaultFileMode, Render: documentsAgents, Required: false, Area: AreaDocuments},
		{Key: "topics_readme", RelPath: "Topics/README.md", Mode: defaultFileMode, Render: topicsReadme, Required: false, Area: areaTopics},
		{Key: "library_readme", RelPath: "Library/README.md", Mode: defaultFileMode, Render: libraryReadme, Required: false, Area: areaLibrary},
		{Key: "lane_readme", RelPath: DefaultLaneDirName + "/README.md", Mode: defaultFileMode, Render: laneReadme, Required: false, Area: AreaLane},
		{Key: "launchpad_readme", RelPath: "Launchpad/README.md", Mode: defaultFileMode, Render: launchpadReadme, Required: false, Area: AreaLaunchpad},
		{Key: "dropzone_readme", RelPath: "Dropzone/README.md", Mode: defaultFileMode, Render: dropzoneReadme, Required: false, Area: AreaDropzone},
		{Key: "notes_policy", RelPath: ".loom/policies/notes.watch.yaml", Mode: defaultFileMode, Render: notesPolicy, Required: true},
		{Key: "documents_policy", RelPath: ".loom/policies/documents.watch.yaml", Mode: defaultFileMode, Render: documentsPolicy, Required: true, Area: AreaDocuments},
		{Key: "lane_policy", RelPath: ".loom/policies/lane.transfer.yaml", Mode: defaultFileMode, Render: lanePolicy, Required: true, Area: AreaLane},
		{Key: "launchpad_policy", RelPath: ".loom/policies/launchpad.watch.yaml", Mode: defaultFileMode, Render: launchpadPolicy, Required: true, Area: AreaLaunchpad},
		{Key: "dropzone_policy", RelPath: ".loom/policies/dropzone.transfer.yaml", Mode: defaultFileMode, Render: dropzonePolicy, Required: true, Area: AreaDropzone, UpdateExisting: shouldUpgradeDropzonePolicyTemplate},
	}
}

func templatePath(root, rel string) string {
	return filepath.Join(root, filepath.FromSlash(rel))
}

func rootReadme(contract Contract) string {
	visibleAreas := []string{"`Projects/` for LOOM projects", "`Notes/` for markdown-first knowledge", "`Documents/` for broad local files that should be backed up"}
	if _, ok := contract.Areas[areaTopics]; ok {
		visibleAreas = append([]string{"`Topics/` for active topic work"}, visibleAreas...)
	}
	if _, ok := contract.Areas[areaLibrary]; ok {
		visibleAreas = append(visibleAreas, "`Library/` for active library material")
	}
	if _, ok := contract.Areas[AreaLane]; ok {
		visibleAreas = append(visibleAreas, fmt.Sprintf("`%s/` for explicit fast send-to-main transfers", laneAreaPath(contract)))
	}
	return "# loom-box\n\n" +
		"This folder is the local filesystem home base for LOOM-relevant work on this node.\n\n" +
		"Use " + strings.Join(visibleAreas, ", ") + ".\n\n" +
		fmt.Sprintf("- Owner node: `%s`\n- Profile: `%s`\n- Box contract: `.loom/box.yaml`\n", contract.OwnerNode, contract.Profile)
}

func metadataReadme(contract Contract) string {
	return "# .loom\n\n" +
		"This hidden folder stores durable LOOM Box contracts and generated policy files. Runtime state belongs to the node and lives outside the visible Box.\n\n" +
		"- `box.yaml`: machine-readable Box contract.\n" +
		"- `policies/`: Box area policies that LOOM can translate into backend behavior.\n\n" +
		"Agents may inspect these files, but normal user work should happen in the visible Box folders.\n"
}

func projectsReadme(contract Contract) string {
	return "# Projects\n\n" +
		"This is the default home for LOOM projects on this node.\n\n" +
		"Project folders keep LOOM control data under `.loom/`, beginning with `.loom/project.yaml`, plus visible work facets such as scripts, workflows, connectors, schedules, direct events, modules, notes, and repos.\n\n" +
		"v0.4.1 Slice 03 makes `loom project scaffold` default into this folder when no explicit directory is provided.\n"
}

func notesReadme(contract Contract) string {
	return "# Notes\n\n" +
		"Put non-project markdown and text knowledge here when it should become searchable and available to LOOM without creating a full project.\n\n" +
		"Markdown and text files are backed up and enter the object/text pipeline. Attachments are still backed up, but they do not enter markdown text indexing by default.\n"
}

func notesAgents(contract Contract) string {
	return `# LOOM Notes Source Guidance

This directory is canonical writable source for non-project notes. Notes enter
backup and indexing pipelines according to the Box policy. Edit source notes
here; do not mutate derived metadata, search projections, or generated storage
views.

Use ` + "`search-loom-docs`" + ` to verify exact commands. Treat bulk or destructive
changes as sensitive and require explicit user scope.
`
}

func documentsReadme(contract Contract) string {
	return "# Documents\n\n" +
		"Put general-purpose local files here when they should be backed up by main but do not belong in a specific LOOM project or markdown-first notes vault.\n\n" +
		"Documents is intentionally broad: PDFs, images, videos, spreadsheets, and arbitrary folders can live here. The default policy backs up files and records metadata without sending everything into the text indexing lane.\n"
}

func documentsAgents(contract Contract) string {
	return `# LOOM Documents Source Guidance

This directory is canonical broad user storage. Backup, metadata, and indexing
behavior varies by file type and Box policy. Edit source files here and treat
generated ` + "`loom-storage`" + ` exports as read-only views.

Use ` + "`search-loom-docs`" + ` to verify exact commands. Preview destructive or bulk
operations and require explicit user scope before applying them.
`
}

func topicsReadme(contract Contract) string {
	return "# Topics\n\n" +
		"Topics contains active, human-readable topic work in the Main Box. Each topic uses its own named folder.\n"
}

func libraryReadme(contract Contract) string {
	return "# Library\n\n" +
		"Library contains active reference and library material in the Main Box. Each library item uses its own named folder.\n"
}

func laneReadme(contract Contract) string {
	lanePath := laneAreaPath(contract)
	return "# " + lanePath + "\n\n" +
		fmt.Sprintf("`%s/` is the explicit fast-send area for moving files or folders from this workspace node to main custody.\n\n", lanePath) +
		"Put files here when you want to send them to main through the LOOM Lane transfer action. LOOM will use rsync/SSH internally, then clear the visible lane only after main has accepted and verified the transfer.\n\n" +
		"Until a transfer is accepted, this workspace node still owns the files. Do not delete pending Lane files manually unless you intentionally want to cancel the send.\n\n" +
		"LOOM keeps Lane status in node-owned runtime state outside the visible Box instead of writing sidecar files beside large user files.\n"
}

func laneAreaPath(contract Contract) string {
	if area, ok := contract.Areas[AreaLane]; ok && strings.TrimSpace(area.Path) != "" {
		return area.Path
	}
	return DefaultLaneDirName
}

func launchpadReadme(contract Contract) string {
	return "# Launchpad\n\n" +
		"Launchpad is a legacy Box area kept for older LOOM Boxes.\n\n" +
		"New Boxes should use `Documents/` for general files and `Dropzone/` for send-to-main custody transfer. If this folder already exists, LOOM can still read its legacy watch policy.\n"
}

func dropzoneReadme(contract Contract) string {
	status := contract.Areas[AreaDropzone].TransferStatus
	if strings.TrimSpace(status) == "" {
		status = DropzoneTransferFuture
	}
	profileNote := "Workspace profile: LOOM can scan this folder, wait for files to settle, upload them to main in chunks, and mark accepted files `safe_to_delete` after main verifies custody."
	if contract.Profile == ProfileMain {
		profileNote = "Main profile: upload runtime stays inactive because this node already owns durable storage. Main still receives Dropzone uploads from workspace nodes."
	}
	return "# Dropzone\n\n" +
		"Dropzone is for sending files or folders to the main node so they can be deleted locally after verification.\n\n" +
		"It is not a watched mirror. Deleting a local Dropzone file before `safe_to_delete: true` can lose the local copy before main has accepted custody.\n\n" +
		"LOOM stores transfer records in node-owned runtime state outside the visible Box. Use `loom box dropzone status`, `loom box dropzone list`, and `loom box dropzone inspect <transfer-id>` to see progress and the final main custody path.\n\n" +
		"LOOM never deletes user files from this folder by default. `loom box dropzone clean --accepted`, `--failed`, or `--abandoned` only removes old LOOM transfer records, not local files.\n\n" +
		profileNote + "\n\n" +
		fmt.Sprintf("Current transfer status: `%s`.\n", status)
}

func notesPolicy(contract Contract) string {
	return `schema_version: loom.box.watch_policy.v0.6
area: notes
path: Notes
enabled: true
mode: watched_root
semantics: knowledge_index
catalog:
  enabled: true
object:
  enabled: true
text:
  enabled: true
index:
  enabled: true
sync:
  enabled: true
backup:
  enabled: true
delete_semantics: local_source_authoritative
notes: Markdown/text enters LOOM search/database; attachments are backed up and cataloged without text indexing by default.
`
}

func documentsPolicy(contract Contract) string {
	return `schema_version: loom.box.watch_policy.v0.6
area: documents
path: Documents
enabled: true
mode: watched_root
semantics: broad_backup
catalog:
  enabled: true
object:
  enabled: false
text:
  enabled: false
index:
  enabled: false
sync:
  enabled: false
backup:
  enabled: true
delete_semantics: local_source_authoritative
notes: Documents is broad local storage backed up by main; metadata/catalog visibility is separate from text indexing.
`
}

func lanePolicy(contract Contract) string {
	lanePath := laneAreaPath(contract)
	laneNotes := lanePath + " is explicit user-triggered transfer. Slice 09 only scans pending files and preflights rsync/SSH; Slice 10 implements send-to-main."
	return `schema_version: loom.box.lane_policy.v0.6.3
area: lane
path: ` + strconv.Quote(lanePath) + `
enabled: true
mode: explicit_rsync_transfer
runtime_status: pending_slice_10
target: main
target_main_node: main
destination_template: "{source_node}/Lane/{date}/{batch_id}"
engine: rsync_ssh
preflight:
  require_rsync: true
  require_ssh: true
  main_host: loom-main
settle_duration: 0s
ignore_patterns:
    - README.md
    - .git/**
    - node_modules/**
    - .venv/**
    - venv/**
    - dist/**
    - build/**
    - target/**
    - .cache/**
    - .data/**
    - .secrets/**
    - __pycache__/**
    - .DS_Store
    - "._*"
    - "*.tmp"
    - "*.part"
    - "*.partial"
    - "*.crdownload"
    - "~$*"
local_safety_copy:
  enabled: true
  default_retention: 24h
delete_semantics: main_becomes_authoritative_after_verified_acceptance
notes: ` + strconv.Quote(laneNotes) + `
`
}

func launchpadPolicy(contract Contract) string {
	return `schema_version: loom.box.watch_policy.v0.4.1
area: launchpad
path: Launchpad
enabled: true
mode: watched_root
semantics: legacy_active_intake
index:
  enabled: true
sync:
  enabled: true
backup:
  enabled: false
delete_semantics: local_source_authoritative
notes: Launchpad is a legacy quick active intake area, not custody transfer.
`
}

func dropzonePolicy(contract Contract) string {
	target := "main"
	enabled := "true"
	runtimeStatus := "active"
	if contract.Profile == ProfileMain {
		target = "self_disabled"
		enabled = "false"
		runtimeStatus = "inactive"
	}
	return fmt.Sprintf(`schema_version: loom.box.transfer_policy.v0.4.2
area: dropzone
path: Dropzone
enabled: %s
mode: custody_transfer
runtime_status: %s
target: %s
target_main_node: main
scan_interval: 1m0s
settle_duration: 1m0s
ignore_patterns:
    - README.md
    - .git/**
    - node_modules/**
    - .venv/**
    - venv/**
    - dist/**
    - build/**
    - target/**
    - .cache/**
    - .data/**
    - .secrets/**
    - __pycache__/**
    - .DS_Store
    - "._*"
    - "*.tmp"
    - "*.part"
    - "*.partial"
    - "*.crdownload"
    - "~$*"
    - .loom/**
max_parallel_transfers: 1
chunk_size_bytes: 67108864
checksum_algorithm: sha256
retry_count: 3
retry_backoff: 30s
accepted_retention: keep_record
automatic_delete_after_safe: false
delete_semantics: main_becomes_authoritative_after_verified_commit
failure_policy: mark_failed_manual_retry
allow_folders: true
notes: Workspace Dropzone scans nested folders, uploads files in chunks, and marks accepted transfers safe_to_delete after main verifies custody. Main keeps upload inactive.
`, enabled, runtimeStatus, target)
}

func shouldUpgradeDropzonePolicyTemplate(path string) bool {
	payload, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	content := string(payload)
	return strings.Contains(content, "schema_version: loom.box.transfer_policy.v0.4.1") ||
		strings.Contains(content, "scaffolded_for_v0.4.2")
}
