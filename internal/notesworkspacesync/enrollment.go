package notesworkspacesync

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"

	"golang.org/x/sys/unix"
	"golang.org/x/text/unicode/norm"

	"loom.local/loom/internal/knowledge"
	pc "loom.local/loom/internal/projectcontracts"
)

const enrollmentSchema = "notes_workspace.enrollment.v1"

type EnrollmentCollection struct {
	RuntimeSelection
	RecoveryMissingDeferred int    `json:"recovery_missing_deferred,omitempty"`
	SourcePath              string `json:"source_path,omitempty"`
	SourceGeneration        string `json:"source_generation,omitempty"`
	Status                  string `json:"status"`
	SourceIntakeStage       string `json:"source_intake_stage,omitempty"`
	ArchiveOperation        string `json:"archive_operation,omitempty"`
	ArchiveManifest         string `json:"archive_manifest,omitempty"`
	ReferenceFiles          int    `json:"reference_files,omitempty"`
	ReferenceBytes          int64  `json:"reference_bytes,omitempty"`
}

type EnrollmentHold struct {
	SourceCollection string `json:"source_collection"`
	Reason           string `json:"reason"`
	Phase            string `json:"phase,omitempty"`
	Failure          string `json:"failure,omitempty"`
}

func enrollmentAdmissionHold(collection string, err error) EnrollmentHold {
	hold := EnrollmentHold{SourceCollection: collection, Reason: "source_admission_unavailable", Failure: "admission_failed"}
	var admission *knowledge.NotesAdmissionError
	if errors.As(err, &admission) {
		hold.Phase = admission.Phase
		switch admission.Reason {
		case "owner_report_unavailable", "source_scan_evidence_unavailable", "source_unreachable", "source_scan_failed", "source_missing_evidence", "owner_configuration_mismatch":
			hold.Failure = admission.Reason
			return hold
		}
	}
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		hold.Failure = "deadline_exceeded"
	case errors.Is(err, context.Canceled):
		hold.Failure = "cancelled"
	case errors.Is(err, knowledge.ErrNotesCustodyPaused):
		hold.Failure = "custody_unavailable"
	case errors.Is(err, knowledge.ErrNotesPathExcluded):
		hold.Failure = "source_excluded"
	}
	return hold
}

// This is the runtime's resolved membership, not a client-delivery receipt.
type EnrollmentInventory struct {
	SchemaVersion string                 `json:"schema_version"`
	Replica       string                 `json:"replica"`
	NodeKey       string                 `json:"node_key"`
	ConfigSHA256  string                 `json:"config_sha256"`
	Collections   []EnrollmentCollection `json:"collections"`
	Holds         []EnrollmentHold       `json:"holds,omitempty"`
}

func enrollmentPath(cfg RuntimeConfig) string {
	return filepath.Join(filepath.Dir(cfg.ReplicaDir), "enrollment.json")
}

func enrollmentConfigHash(cfg RuntimeConfig) string {
	raw, _ := json.Marshal(cfg)
	var canonical map[string]any
	_ = json.Unmarshal(raw, &canonical)
	raw, _ = json.Marshal(canonical)
	return digest(raw)
}

func readEnrollment(path string) (EnrollmentInventory, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if err != nil {
		return EnrollmentInventory{}, err
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	var st unix.Stat_t
	// Managed agent-read ACLs may add read/search bits to this non-secret snapshot.
	// Only the owning runtime may write it; file identity checks still apply.
	if err := unix.Fstat(fd, &st); err != nil || st.Mode&unix.S_IFMT != unix.S_IFREG || st.Nlink != 1 || st.Mode&0022 != 0 {
		return EnrollmentInventory{}, errors.New("invalid Notes enrollment inventory file")
	}
	raw, err := io.ReadAll(io.LimitReader(f, (4<<20)+1))
	if err != nil || len(raw) > 4<<20 {
		return EnrollmentInventory{}, errors.New("Notes enrollment inventory unavailable")
	}
	var v EnrollmentInventory
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&v); err != nil {
		return v, errors.New("invalid Notes enrollment inventory")
	}
	if err := d.Decode(new(any)); err != io.EOF || v.SchemaVersion != enrollmentSchema || !token(v.Replica) || v.NodeKey == "" {
		return v, errors.New("invalid Notes enrollment inventory identity")
	}
	return v, nil
}

func writeEnrollment(path string, v EnrollmentInventory) error {
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil || len(raw) > 4<<20 {
		return errors.New("Notes enrollment inventory exceeds limit")
	}
	if previous, err := readEnrollment(path); err == nil && reflect.DeepEqual(previous, v) {
		return nil // Do not dirty the recovery cohort on unchanged sync turns.
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".enrollment-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(append(raw, '\n')); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil || closeErr != nil {
		return errors.Join(err, closeErr)
	}
	if err := os.Rename(f.Name(), path); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

type enrollmentCandidate struct {
	ID, Root, ProjectID, RelativePath, ExplicitRoot string
	WorkState                                       string
	Archived                                        bool
}

type placementError string

func (e placementError) Error() string         { return string(e) }
func (e placementError) PlacementCode() string { return string(e) }

func mergeEnrollmentSelections(cfg RuntimeConfig, previous EnrollmentInventory, candidates []enrollmentCandidate) ([]RuntimeSelection, error) {
	candidates, err := resolveCandidatePlacements(previous, candidates)
	if err != nil {
		return nil, err
	}
	if !cfg.LayoutPublication {
		for _, c := range candidates {
			if c.ExplicitRoot == "" {
				continue
			}
			for _, old := range previous.Collections {
				root := old.WorkingRoot
				if root == "" {
					root = old.Root
				}
				if old.SourceCollection == c.ID && root != c.Root {
					return nil, placementError("notes_workspace_layout_publication_required")
				}
			}
		}
	}
	selections := append([]RuntimeSelection(nil), cfg.Scopes...)
	workspace := selections[0].Workspace
	known := map[string]bool{}
	retained := map[string]RuntimeSelection{}
	for _, s := range selections {
		if s.Workspace != workspace {
			return nil, errors.New("automatic Notes enrollment requires one workspace")
		}
		known[s.SourceCollection] = true
	}
	for _, c := range previous.Collections {
		if c.Workspace != workspace {
			return nil, errors.New("Notes enrollment workspace changed")
		}
		retained[c.SourceCollection] = c.RuntimeSelection
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].ID < candidates[j].ID })
	for _, c := range candidates {
		if known[c.ID] {
			for i := range selections {
				if selections[i].SourceCollection == c.ID {
					if old, ok := retained[c.ID]; ok {
						selections[i].WorkingRoot, selections[i].Root, selections[i].LayoutFrom = old.WorkingRoot, old.Root, old.LayoutFrom
					}
					selections[i] = placeSelection(selections[i], c, cfg.LayoutPublication)
					selections[i] = classifySelection(selections[i], c.WorkState, cfg.LayoutPublication)
					selections[i] = archiveSelection(selections[i], c.Archived, cfg.LayoutPublication)
				}
			}
			continue
		}
		s, ok := retained[c.ID]
		if !ok {
			s = RuntimeSelection{Scope: Scope{Workspace: workspace,
				Collection: "notes_" + key(c.ID)[:24], SourceCollection: c.ID,
				Generation: "automatic_enrollment_v1", Root: c.Root}}
		}
		s = placeSelection(s, c, cfg.LayoutPublication)
		s = classifySelection(s, c.WorkState, cfg.LayoutPublication)
		s = archiveSelection(s, c.Archived, cfg.LayoutPublication)
		selections = append(selections, s)
		known[c.ID] = true
	}
	// Admission paths are retained compatibility hints, not membership. A newly
	// registered empty folder is valid; per-file admission runs on every operation.
	for i := range selections {
		old, ok := retained[selections[i].SourceCollection]
		if !ok {
			continue
		}
		if selections[i].Root == old.Root {
			selections[i].LayoutGeneration, selections[i].LayoutReadOnly = old.LayoutGeneration, old.LayoutReadOnly
		} else {
			for _, entry := range previous.Collections {
				if entry.SourceCollection == selections[i].SourceCollection {
					selections[i].LayoutGeneration, selections[i].LayoutReadOnly = entry.SourceGeneration, entry.Archived
				}
			}
		}
	}
	check := cfg
	check.Scopes = append([]RuntimeSelection(nil), selections...)
	for i := range check.Scopes {
		if check.Scopes[i].AdmissionPath == "" {
			check.Scopes[i].AdmissionPath = "enrollment.md"
		}
	}
	if err := check.Validate(); err != nil {
		return nil, placementError("notes_workspace_collection_collision_or_invalid_path")
	}
	return selections, nil
}

func workingProjectGroup(root string) string {
	root = strings.TrimPrefix(strings.TrimPrefix(root, "Projects/_Passive/"), "Projects/_Archived/")
	root = strings.TrimPrefix(root, "Projects/")
	name, _, _ := strings.Cut(root, "/")
	return "Projects/" + name
}

func resolveCandidatePlacements(previous EnrollmentInventory, candidates []enrollmentCandidate) ([]enrollmentCandidate, error) {
	out := append([]enrollmentCandidate(nil), candidates...)
	projects := map[string]string{}
	groups := map[string]map[string]bool{}
	owners := map[string]map[string]bool{}
	retained := map[string]bool{}
	retainedGroups := map[string]string{}
	for _, c := range candidates {
		projects[c.ID] = c.ProjectID
	}
	for _, old := range previous.Collections {
		project := projects[old.SourceCollection]
		if project == "" {
			continue
		}
		root := old.WorkingRoot
		if root == "" {
			root = old.Root
		}
		group := workingProjectGroup(root)
		if groups[project] == nil {
			groups[project] = map[string]bool{}
		}
		groups[project][group] = true
		if owners[group] == nil {
			owners[group] = map[string]bool{}
		}
		owners[group][project] = true
		retained[old.SourceCollection] = true
		retainedGroups[old.SourceCollection] = group
	}
	for index, c := range out {
		if c.ProjectID == "" {
			continue
		}
		group := workingProjectGroup(c.Root)
		if c.ExplicitRoot != "" {
			if err := pc.ValidateNotesWorkspacePath(c.ExplicitRoot); err != nil {
				return nil, err
			}
			requested := workingProjectGroup(c.ExplicitRoot)
			if requested != group && !groups[c.ProjectID][requested] {
				return nil, placementError("notes_workspace_project_namespace_mismatch")
			}
			group = requested
			out[index].Root = c.ExplicitRoot
		} else if retained[c.ID] {
			group = retainedGroups[c.ID]
		} else {
			if len(groups[c.ProjectID]) > 1 {
				return nil, placementError("notes_workspace_ambiguous_project_group_declare_path")
			}
			for existing := range groups[c.ProjectID] {
				out[index].Root = existing + strings.TrimPrefix(c.Root, group)
				group = existing
			}
		}
		for owner := range owners[group] {
			if owner != c.ProjectID {
				return nil, placementError("notes_workspace_namespace_owned_by_another_project")
			}
		}
		if owners[group] == nil {
			owners[group] = map[string]bool{}
		}
		owners[group][c.ProjectID] = true
	}
	return out, nil
}

func placeSelection(s RuntimeSelection, c enrollmentCandidate, enabled bool) RuntimeSelection {
	if c.ExplicitRoot == "" || !enabled {
		return s
	}
	s.WorkingRoot = c.Root
	desired := c.Root
	if c.Archived {
		desired = "Projects/_Archived/" + strings.TrimPrefix(c.Root, "Projects/")
	} else if c.WorkState == "passive" {
		desired = "Projects/_Passive/" + strings.TrimPrefix(c.Root, "Projects/")
	}
	if s.Root != desired {
		s.LayoutFrom, s.Root = s.Root, desired
	}
	return s
}

func archiveSelection(s RuntimeSelection, archived, enabled bool) RuntimeSelection {
	s.Archived = archived
	if !archived || !enabled {
		return s
	}
	if s.WorkingRoot == "" {
		s.WorkingRoot = s.Root
	}
	s.DesiredRoot = "Projects/_Archived/" + strings.TrimPrefix(s.WorkingRoot, "Projects/")
	if s.Root != s.DesiredRoot {
		s.LayoutFrom, s.Root = s.Root, s.DesiredRoot
	}
	return s
}

func classifySelection(s RuntimeSelection, workState string, enabled bool) RuntimeSelection {
	if !strings.HasPrefix(s.Root, "Projects/") || workState == "" && s.WorkingRoot == "" {
		return s
	}
	if s.WorkingRoot == "" {
		s.WorkingRoot = s.Root
	}
	s.WorkState = workState
	if s.WorkState == "" {
		s.WorkState = "working"
	}
	s.DesiredRoot = s.WorkingRoot
	if s.WorkState == "passive" {
		s.DesiredRoot = "Projects/_Passive/" + strings.TrimPrefix(s.WorkingRoot, "Projects/")
	}
	if enabled && s.Root != s.DesiredRoot {
		s.LayoutFrom, s.Root = s.Root, s.DesiredRoot
	}
	return s
}

func enrollmentDisplayRoot(root knowledge.SourceRoot, projectName string) (string, error) {
	if root.ProjectID == nil {
		switch root.RootKind {
		case knowledge.RootKindBoxNotes:
			return "Notes", nil
		case knowledge.RootKindBoxTopics:
			return "Topics", nil
		case knowledge.RootKindBoxLibrary:
			return "Library", nil
		}
		return "", errors.New("unsupported Notes root")
	}
	name := norm.NFC.String(strings.TrimSpace(projectName))
	if !textPath("Projects/"+name+"/probe.md") || strings.Contains(name, "/") {
		return "", errors.New("project name is not a Notes display component")
	}
	folder := filepath.ToSlash(root.RootRelativePath)
	var metadata struct {
		Registration struct {
			Source struct {
				Version string `json:"schema_version"`
			} `json:"knowledge_source"`
		} `json:"registration_metadata"`
	}
	if err := json.Unmarshal(root.Metadata, &metadata); err == nil && metadata.Registration.Source.Version == pc.ProjectSchemaV06 {
		display := "Projects/" + name
		if folder != "." {
			display += "/" + folder
		}
		if !textPath(display + "/probe.md") {
			return "", errors.New("invalid Notes source display path")
		}
		return display, nil
	}
	if strings.HasPrefix(folder, "application-data/") {
		folder = strings.TrimPrefix(folder, "application-data/")
		if folder == "notebooks" {
			folder = "Notebooks"
		}
	}
	switch folder {
	case "notes":
		folder = "Notes"
	case "docs", "documentation":
		folder = "Documentation"
	case "research":
		folder = "Research"
	}
	display := "Projects/" + name + "/" + folder
	if !textPath(display + "/probe.md") {
		return "", errors.New("invalid Notes source display path")
	}
	return display, nil
}

func enrollmentCandidateEligible(root knowledge.SourceRoot, projectStatus string, archivesEnabled bool) bool {
	if root.ProjectID != nil {
		if projectStatus == "archived" {
			return archivesEnabled
		}
		if projectStatus != "active" {
			return false
		}
	}
	return root.Status == knowledge.SourceRootStatusActive
}

func (r *Runtime) enrollmentSelections(ctx context.Context) ([]RuntimeSelection, EnrollmentInventory, error) {
	previous, err := readEnrollment(enrollmentPath(r.Config))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, previous, err
	}
	if previous.SchemaVersion != "" && (previous.Replica != r.Config.Replica || previous.NodeKey != r.NodeKey) {
		return nil, previous, errors.New("Notes enrollment replica identity changed")
	}
	candidates, err := r.enrollmentCandidates(ctx)
	if err != nil {
		return nil, previous, err
	}
	selections, err := mergeEnrollmentSelections(r.Config, previous, candidates)
	return selections, previous, err
}

func (r *Runtime) enrollmentCandidates(ctx context.Context) ([]enrollmentCandidate, error) {
	roots, err := r.Knowledge.Store().ListSourceRoots(ctx, knowledge.SourceRootFilter{NodeKey: r.NodeKey, IncludeInactive: r.Config.LayoutPublication && r.Archives != nil})
	if err != nil {
		return nil, err
	}
	var candidates []enrollmentCandidate
	for _, root := range roots {
		var name, status, workState string
		if root.ProjectID != nil {
			if err := r.DB.QueryRowContext(ctx, `SELECT name,status,COALESCE(metadata->>'work_state','') FROM projects.projects WHERE project_id=$1`, *root.ProjectID).Scan(&name, &status, &workState); err != nil {
				return nil, err
			}
		}
		if !enrollmentCandidateEligible(root, status, r.Config.LayoutPublication && r.Archives != nil) {
			continue
		}
		display, err := enrollmentDisplayRoot(root, name)
		if err != nil {
			return nil, err
		}
		candidate := enrollmentCandidate{ID: root.NotesSourceRootID, Root: display, RelativePath: root.RootRelativePath, WorkState: workState, Archived: status == "archived"}
		if root.ProjectID != nil {
			candidate.ProjectID = *root.ProjectID
		}
		policy, err := knowledge.NotesWorkspacePolicy(root)
		if err != nil {
			return nil, err
		}
		if policy != nil {
			candidate.ExplicitRoot = policy.Path
		}
		candidates = append(candidates, candidate)
	}
	return candidates, nil
}
