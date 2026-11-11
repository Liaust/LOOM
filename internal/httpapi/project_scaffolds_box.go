package httpapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"loom.local/loom/internal/box"
	"loom.local/loom/internal/boxregistry"
	pc "loom.local/loom/internal/projectcontracts"
	"loom.local/loom/internal/projects"
	"loom.local/loom/internal/response"
)

var scaffoldBoxSlug = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,62}[a-z0-9]$`)

// Rootless creation cannot establish placement. Validate an existing exact
// selected project before the legacy handler can replay an idempotency receipt.
func (s Server) handleProjectsInBox(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || s.services.Boxes == nil {
		s.handleProjects(w, r)
		return
	}
	cid, _ := requestMeta(r)
	if trusted, _ := r.Context().Value(localFileTransportKey{}).(bool); !trusted {
		s.writeError(w, cid, 403, "files.local_transport_required", "projects", "create", "Project creation requires the operator Unix socket.", nil)
		return
	}
	var input projects.CreateInput
	if err := decodeBoxMembership(r, &input, "name", "slug", "description", "home_node_ref", "if_not_exists", "metadata"); err != nil {
		s.writeError(w, cid, 400, "request.invalid_json", "projects", "create", "Use exact bounded project JSON.", nil)
		return
	}
	selection, err := s.boxSelection(r)
	if err != nil || selection == nil {
		s.writeError(w, cid, 400, "box.selection_invalid", "projects", "create", "Select an available Box.", nil)
		return
	}
	if !input.IfNotExists || !scaffoldBoxSlug.MatchString(input.Slug) {
		s.writeError(w, cid, 409, "project.placement_required", "projects", "create", "Use the scoped scaffold route for new projects; if-not-exists requires an exact existing machine slug.", nil)
		return
	}
	if s.services.Projects.DB == nil {
		s.writeError(w, cid, 503, "project.scaffold_unavailable", "projects", "create", "Project registry is unavailable.", nil)
		return
	}
	r = r.WithContext(boxregistry.WithSelection(r.Context(), *selection))
	if _, err := s.services.Projects.ResolveProjectRef(r.Context(), input.Slug); err != nil {
		s.writeError(w, cid, 409, "project.box_mismatch", "projects", "create", "The existing project is not available in the selected Box; do not retry globally.", nil)
		return
	}
	raw, _ := json.Marshal(input)
	r.Body = io.NopCloser(bytes.NewReader(raw))
	r.ContentLength = int64(len(raw))
	s.handleProjects(w, r)
}

// Register this handler instead of the legacy unscoped scaffold route.
func (s Server) handleProjectScaffoldsInBox(w http.ResponseWriter, r *http.Request) {
	cid, _ := requestMeta(r)
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		s.writeError(w, cid, 405, "method.not_allowed", "projects", "scaffold", "Use POST to scaffold a project.", nil)
		return
	}
	if s.services.Boxes == nil {
		s.handleProjectScaffolds(w, r)
		return
	}
	if trusted, _ := r.Context().Value(localFileTransportKey{}).(bool); !trusted {
		s.writeError(w, cid, 403, "files.local_transport_required", "projects", "scaffold", "Project creation requires the operator Unix socket.", nil)
		return
	}
	var input pc.ScaffoldOptions
	if err := decodeBoxMembership(r, &input, "mode", "name", "slug", "owner_node", "preset", "facets", "directory", "directory_source", "box_default_used", "box_root", "box_profile", "box_contract_path", "force", "dry_run", "project_id", "repository_members"); err != nil {
		s.writeError(w, cid, 400, "project.scaffold_invalid", "projects", "scaffold", "Supply exact bounded project scaffold JSON.", nil)
		return
	}
	selection, err := s.boxSelection(r)
	if err != nil || selection == nil {
		s.writeError(w, cid, 400, "box.selection_invalid", "projects", "scaffold", "Select an available destination Box.", nil)
		return
	}
	r = r.WithContext(boxregistry.WithSelection(r.Context(), *selection))
	input, root, err := s.normalizeProjectScaffoldInBox(r, input, *selection)
	if err != nil {
		s.writeError(w, cid, 400, "project.scaffold_invalid", "projects", "scaffold", err.Error(), nil)
		return
	}
	if s.services.Projects.DB == nil && !input.DryRun {
		s.writeError(w, cid, 503, "project.scaffold_unavailable", "projects", "scaffold", "Project registry is required before creating source files.", nil)
		return
	}
	if s.services.Projects.DB != nil {
		if err := s.services.Projects.ValidateScaffoldTarget(r.Context(), input.Slug, root); err != nil {
			s.writeError(w, cid, 409, "project.scaffold_identity_conflict", "projects", "scaffold", err.Error(), nil)
			return
		}
	}
	result, err := pc.ScaffoldProject(input)
	if err != nil {
		if result.SourceState == "source_created" {
			s.writeProjectCreatePending(w, r, result, "project.scaffold_failed", "Project source was retained, but creation did not finish.", err)
			return
		}
		s.writeError(w, cid, 400, "project.scaffold_failed", "projects", "scaffold", "Could not scaffold the selected project.", err)
		return
	}
	if input.Mode == pc.ScaffoldModeDeclaration && !input.DryRun && !s.connectCreatedProject(w, r, &result) {
		return
	}
	status := http.StatusCreated
	if input.DryRun {
		status = http.StatusOK
	}
	response.WriteJSON(w, status, response.Success(cid, result))
}

func (s Server) normalizeProjectScaffoldInBox(r *http.Request, input pc.ScaffoldOptions, selection boxregistry.ResolvedSelection) (pc.ScaffoldOptions, string, error) {
	input.Name = strings.TrimSpace(input.Name)
	input.Slug = strings.TrimSpace(input.Slug)
	if input.Slug == "" {
		input.Slug = pc.DeriveProjectSlug(input.Name)
	}
	if input.Name == "" || !scaffoldBoxSlug.MatchString(input.Slug) {
		return input, "", fmt.Errorf("use a project name and a valid machine slug")
	}
	var binding boxregistry.Box
	var err error
	if input.Directory != "" {
		if !filepath.IsAbs(input.Directory) || filepath.Clean(input.Directory) != input.Directory {
			return input, "", fmt.Errorf("use a clean absolute destination directory")
		}
		physical, e := filepath.EvalSymlinks(input.Directory)
		if e != nil || physical != input.Directory {
			return input, "", fmt.Errorf("destination parent must exist without symbolic-link aliases")
		}
		binding, err = s.services.Boxes.ForPath(physical)
	} else if input.BoxRoot != "" {
		binding, err = s.services.Boxes.ForPath(input.BoxRoot)
	} else if !selection.AllBoxes && len(selection.BoxIDs) == 1 {
		binding, err = s.services.Boxes.Get(selection.BoxIDs[0])
	} else {
		return input, "", fmt.Errorf("choose one destination Box or an explicit directory")
	}
	if err != nil || !selection.Contains(binding.ID) {
		return input, "", fmt.Errorf("destination is outside the selected Box or is unavailable")
	}
	if input.BoxRoot != "" && input.BoxRoot != binding.RootPath {
		return input, "", fmt.Errorf("Box root contradicts selected placement")
	}
	if input.OwnerNode != "" && input.OwnerNode != binding.Node {
		return input, "", fmt.Errorf("owner node contradicts selected Box")
	}
	if binding.Node != "" {
		input.OwnerNode = binding.Node
	} else if input.OwnerNode == "" {
		input.OwnerNode = s.services.RuntimeConfig.NodeID
	}
	if input.Preset == "" {
		input.Preset = pc.PresetMinimal
	}
	input.BoxRoot = binding.RootPath
	contract, err := box.LoadContract(box.ContractPath(binding.RootPath))
	if err != nil || contract.BoxID != binding.ID || contract.RootPath != binding.RootPath {
		return input, "", fmt.Errorf("selected Box descriptor changed or is unavailable")
	}
	input.BoxProfile, input.BoxContractPath = contract.Profile, box.ContractPath(binding.RootPath)
	if input.Directory == "" {
		input.Directory = filepath.Join(binding.RootPath, filepath.FromSlash(contract.DefaultProjectPath))
		input.BoxProfile, input.BoxContractPath = contract.Profile, box.ContractPath(binding.RootPath)
		input.DirectorySource, input.BoxDefaultUsed = pc.ScaffoldDirectoryBox, true
	} else {
		input.DirectorySource, input.BoxDefaultUsed = pc.ScaffoldDirectoryExplicit, false
	}
	parent, err := filepath.EvalSymlinks(input.Directory)
	if err != nil || parent != input.Directory {
		return input, "", fmt.Errorf("destination parent is unavailable or has a symbolic-link alias")
	}
	parentBox, err := s.services.Boxes.ForPath(parent)
	if err != nil || parentBox.ID != binding.ID {
		return input, "", fmt.Errorf("destination parent crosses Box membership")
	}
	root := filepath.Join(parent, input.Slug)
	if stat, err := os.Lstat(root); err == nil && stat.Mode()&os.ModeSymlink != 0 {
		return input, "", fmt.Errorf("project target must not be a symbolic link")
	} else if err != nil && !os.IsNotExist(err) {
		return input, "", err
	}
	return input, root, nil
}
