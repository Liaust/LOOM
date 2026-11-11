package hermesschedules

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
	"loom.local/loom/internal/boxregistry"
)

var boxIdentity = regexp.MustCompile(`^box_[A-Za-z0-9_-]+$`)

// BoxOwnership is saved execution context, never the desktop's current Box.
type BoxOwnership struct {
	BoxID           string `json:"box_id"`
	Profile         string `json:"profile"`
	Workdir         string `json:"workdir"`
	ContextRevision string `json:"context_revision"`
	Retired         bool   `json:"retired"`
}

func validBoxOwnership(b *BoxOwnership, profile string) bool {
	return b != nil && boxIdentity.MatchString(b.BoxID) && projectKey.MatchString(b.Profile) && b.Profile == profile && filepath.IsAbs(b.Workdir) && b.Workdir != "/" && !strings.ContainsAny(b.Workdir, "\x00\r\n") && filepath.Clean(b.Workdir) == b.Workdir && projectDigest.MatchString(b.ContextRevision)
}

func validateBoxCatalog(path, key string, owner *BoxOwnership) error {
	read := func(path string, nixLink bool) ([]byte, error) {
		resolved, err := filepath.EvalSymlinks(path)
		if err != nil || (resolved != path && !(nixLink && strings.HasPrefix(resolved, "/nix/store/"))) {
			return nil, ErrSource
		}
		info, err := os.Lstat(resolved)
		if err != nil || !info.Mode().IsRegular() || info.Size() > 1<<20 {
			return nil, ErrSource
		}
		return os.ReadFile(resolved)
	}
	raw, err := read(path, true)
	var catalog boxregistry.Document
	if err != nil || json.Unmarshal(raw, &catalog) != nil || catalog.SchemaVersion != boxregistry.SchemaVersion {
		return ErrSource
	}
	var entry *boxregistry.Entry
	for i := range catalog.Boxes {
		if catalog.Boxes[i].Key == key && catalog.Boxes[i].RootPath == owner.Workdir {
			if entry != nil {
				return ErrSource
			}
			entry = &catalog.Boxes[i]
		}
	}
	if entry == nil {
		return ErrSource
	}
	raw, err = read(filepath.Join(owner.Workdir, ".loom", "box.yaml"), false)
	var descriptor struct {
		ID     string `yaml:"box_id"`
		Node   string `yaml:"owner_node"`
		Root   string `yaml:"root_path"`
		Schema string `yaml:"schema_version"`
	}
	if err != nil || yaml.Unmarshal(raw, &descriptor) != nil || descriptor.ID != owner.BoxID || descriptor.Node != entry.Node || descriptor.Root != owner.Workdir || (descriptor.Schema != "loom.box.v0.4.1" && descriptor.Schema != "loom.box.v0.5") {
		return ErrSource
	}
	return nil
}

// BoxRequest uses the native cron owner without inventing a project. Callers
// must validate any real ProjectID against the selected Box before dispatch.
type BoxRequest struct {
	Operation        string       `json:"operation"`
	Source           Source       `json:"source"`
	BoxID            string       `json:"box_id"`
	Profile          string       `json:"profile"`
	ProjectID        string       `json:"project_id,omitempty"`
	Resource         string       `json:"resource,omitempty"`
	ExpectedRevision string       `json:"expected_revision,omitempty"`
	ExpectedID       string       `json:"expected_id,omitempty"`
	Token            string       `json:"token,omitempty"`
	InputHash        string       `json:"input_hash,omitempty"`
	Spec             *ProjectSpec `json:"spec,omitempty"`
}

func (c ProjectClient) CallBox(ctx context.Context, req BoxRequest) (ProjectResponse, error) {
	if c.Source.Revision != PlatformRevision || c.Source.Profile != "mina" || c.Source.Host == "" || req.Source != c.Source || !boxIdentity.MatchString(req.BoxID) || !projectKey.MatchString(req.Profile) || (req.ProjectID != "" && !projectID.MatchString(req.ProjectID)) {
		return ProjectResponse{}, ErrSource
	}
	if req.Operation != "box_list" && req.Operation != "box_reconcile" && req.Operation != "box_retire" {
		return ProjectResponse{}, ErrSource
	}
	raw, err := c.roundTrip(ctx, req)
	if err != nil {
		return ProjectResponse{}, err
	}
	var out ProjectResponse
	if decodeStrict(raw, &out) != nil || out.Source != c.Source {
		return out, ErrMalformed
	}
	if out.Error == "conflict" {
		return out, ErrProjectConflict
	}
	if out.Error == "terminal_requires_native_resume" {
		return out, ErrTerminalRequiresResume
	}
	if out.Error != "" {
		return out, ErrUnavailable
	}
	if out.Jobs == nil {
		return out, ErrMalformed
	}
	seen := map[string]bool{}
	for _, job := range out.Jobs {
		if !validBoxOwnership(job.Box, req.Profile) || job.Box.BoxID != req.BoxID || job.Profile != req.Profile || job.ProjectID != req.ProjectID || !identifier.MatchString(job.ID) || !projectKey.MatchString(job.Resource) || seen[job.Resource] || (req.Resource != "" && req.Resource != job.Resource) || !projectDigest.MatchString(job.Revision) || !projectDigest.MatchString(job.DesiredHash) || !projectDigest.MatchString(job.CommittedRevision) || !projectDigest.MatchString(job.InputHash) || (job.BeforeRevision != "absent" && !projectDigest.MatchString(job.BeforeRevision)) || job.Token == "" {
			return out, ErrMalformed
		}
		seen[job.Resource] = true
	}
	return out, nil
}
