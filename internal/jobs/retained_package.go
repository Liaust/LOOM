package jobs

import (
	"context"
	"fmt"
	"os"
	"reflect"

	"loom.local/loom/internal/ids"
	"loom.local/loom/internal/objects"
	"loom.local/loom/internal/objectstore"
	"loom.local/loom/internal/projects"
	"loom.local/loom/internal/requestctx"
	"loom.local/loom/internal/scripts"
	"loom.local/loom/internal/workflows"
)

type RetainedProducerSelector struct {
	Kind       string `json:"kind"`
	ProducerID string `json:"producer_id"`
	VersionID  string `json:"version_id"`
}
type RetainedInputsRequest struct {
	Source   projects.RetainedSourceSelector `json:"source"`
	Producer RetainedProducerSelector        `json:"producer"`
}
type RetainedInputs struct {
	Source   objects.PackageSnapshot `json:"source"`
	Producer objects.PackageSnapshot `json:"producer"`
}
type retainedProducer struct {
	binding objects.ProducerPackageBinding
	root    string
}

func (s Service) resolveRetainedProducer(ctx context.Context, scope string, selector RetainedProducerSelector) (retainedProducer, error) {
	var result retainedProducer
	var owner *string
	var status, versionStatus string
	var active *string
	p := objects.ProducerPackageBinding{Kind: selector.Kind, ProducerID: selector.ProducerID, VersionID: selector.VersionID}
	switch selector.Kind {
	case "script":
		if ids.Validate(ids.ScriptPrefix, selector.ProducerID) != nil || ids.Validate(ids.ScriptVersionPrefix, selector.VersionID) != nil {
			return result, fmt.Errorf("exact script identity required")
		}
		detail, err := s.Scripts.GetScript(ctx, selector.ProducerID)
		if err != nil {
			return result, err
		}
		v, err := s.Scripts.GetScriptVersion(ctx, selector.VersionID)
		if err != nil {
			return result, err
		}
		if v.ScriptID != selector.ProducerID || detail.Script.ScriptID != selector.ProducerID {
			return result, fmt.Errorf("producer version parent mismatch")
		}
		manifest, err := v.Manifest()
		if err != nil {
			return result, err
		}
		if err := scripts.ValidateManifest(manifest); err != nil {
			return result, err
		}
		raw, err := scripts.NormalizeManifestJSON(manifest)
		if err != nil {
			return result, err
		}
		hash, err := scripts.HashManifest(manifest)
		if err != nil || hash != v.ManifestHash || manifest.Version != v.VersionLabel {
			return result, fmt.Errorf("producer manifest mismatch")
		}
		p.VersionLabel = v.VersionLabel
		p.ManifestJSON = string(raw)
		p.ManifestHash = hash
		p.ContentHash = v.ContentHash
		owner = detail.Script.OwnerScopeID
		status = detail.Script.Status
		active = detail.Script.ActiveVersionID
		versionStatus = v.Status
		result.root = v.PackageRoot
	case "workflow":
		if ids.Validate(ids.WorkflowPrefix, selector.ProducerID) != nil || ids.Validate(ids.WorkflowVersionPrefix, selector.VersionID) != nil {
			return result, fmt.Errorf("exact workflow identity required")
		}
		detail, err := s.Workflows.GetWorkflow(ctx, selector.ProducerID)
		if err != nil {
			return result, err
		}
		v, err := s.Workflows.GetWorkflowVersion(ctx, selector.VersionID)
		if err != nil {
			return result, err
		}
		if v.WorkflowID != selector.ProducerID || detail.Workflow.WorkflowID != selector.ProducerID {
			return result, fmt.Errorf("producer version parent mismatch")
		}
		manifest, err := v.Manifest()
		if err != nil {
			return result, err
		}
		manifest = workflows.NormalizeManifest(manifest)
		if err := workflows.ValidateManifest(manifest); err != nil {
			return result, err
		}
		raw, err := workflows.NormalizeManifestJSON(manifest)
		if err != nil {
			return result, err
		}
		p.VersionLabel = v.VersionLabel
		p.ManifestJSON = string(raw)
		hash, err := workflows.HashManifest(manifest)
		if err != nil || hash != v.ManifestHash {
			return result, fmt.Errorf("producer manifest mismatch")
		}
		p.ManifestHash = hash
		p.ContentHash = v.ContentHash
		if manifest.Workflow.Version != v.VersionLabel {
			return result, fmt.Errorf("producer manifest version mismatch")
		}
		owner = detail.Workflow.OwnerScopeID
		status = detail.Workflow.Status
		active = detail.Workflow.ActiveVersionID
		versionStatus = v.Status
		result.root = v.PackageRoot
	default:
		return result, fmt.Errorf("unsupported retained producer kind")
	}
	if scope == "" || owner == nil || *owner != scope || status != "active" || versionStatus != "active" || active == nil || *active != selector.VersionID || result.root == "" {
		return result, fmt.Errorf("active project-owned producer required")
	}
	result.binding = p
	return result, nil
}

// CaptureRetainedInputs is an internal library boundary. It creates only retained
// Objects. E3e must supply isolated execution and queue admission separately.
func (s Service) CaptureRetainedInputs(ctx context.Context, req requestctx.Context, local projects.RetainedSourceLocal, input RetainedInputsRequest) (RetainedInputs, error) {
	projectOwner := projects.NewService(s.DB)
	source, err := projectOwner.ResolveRetainedSource(ctx, req, local, input.Source)
	if err != nil {
		return RetainedInputs{}, err
	}
	defer source.Close()
	producer, err := s.resolveRetainedProducer(ctx, source.Facts.ProjectScopeID, input.Producer)
	if err != nil {
		return RetainedInputs{}, err
	}
	producerBinding := objects.PackageBinding{Role: "producer", ProjectID: source.Facts.ProjectID, ScopeID: source.Facts.ProjectScopeID, OwnerNodeID: source.Facts.OwnerNodeID, Producer: &producer.binding}
	if err := objects.ValidatePackageBinding(producerBinding); err != nil {
		return RetainedInputs{}, err
	}
	stage, err := os.MkdirTemp("", "loom-retained-inputs-")
	if err != nil {
		return RetainedInputs{}, err
	}
	defer os.RemoveAll(stage)
	sourceCapture, err := objectstore.CapturePackage(ctx, source.Root, stage, objectstore.PackageCaptureOptions{Selection: source.Facts.Selection})
	if err != nil {
		return RetainedInputs{}, err
	}
	defer sourceCapture.Close()
	sourceSnapshot, err := s.Objects.RetainPackage(ctx, req, sourceCapture, objects.PackageBinding{Role: "source", ProjectID: source.Facts.ProjectID, ScopeID: source.Facts.ProjectScopeID, OwnerNodeID: source.Facts.OwnerNodeID, Source: &source.Facts})
	if err != nil {
		return RetainedInputs{}, err
	}
	root, err := objectstore.OpenPackageDirectory(producer.root)
	if err != nil {
		return RetainedInputs{}, err
	}
	defer root.Close()
	captured, err := objectstore.CapturePackage(ctx, root, stage, objectstore.PackageCaptureOptions{Selection: []string{"."}, ExcludeDirectories: []string{".git", ".direnv", ".cache", "node_modules", "tmp", "result"}, ExcludeFiles: []string{".DS_Store"}, MaxContentBytes: objectstore.PackageMaxProducerBytes})
	if err != nil {
		return RetainedInputs{}, err
	}
	defer captured.Close()
	hash, err := scripts.HashPackage(captured.TreePath)
	if err != nil {
		return RetainedInputs{}, err
	}
	if hash != producer.binding.ContentHash {
		return RetainedInputs{}, fmt.Errorf("registered producer bytes unavailable")
	}
	// Capture cannot create approval or silently substitute newly active versions.
	current, err := s.resolveRetainedProducer(ctx, source.Facts.ProjectScopeID, input.Producer)
	if err != nil {
		return RetainedInputs{}, err
	}
	if !reflect.DeepEqual(current, producer) {
		return RetainedInputs{}, fmt.Errorf("producer changed during capture")
	}
	producerSnapshot, err := s.Objects.RetainPackage(ctx, req, captured, producerBinding)
	if err != nil {
		return RetainedInputs{}, err
	}
	if err := projectOwner.RecheckRetainedSource(ctx, req, local, source); err != nil {
		return RetainedInputs{}, err
	}
	current, err = s.resolveRetainedProducer(ctx, source.Facts.ProjectScopeID, input.Producer)
	if err != nil {
		return RetainedInputs{}, err
	}
	if !reflect.DeepEqual(current, producer) {
		return RetainedInputs{}, fmt.Errorf("producer changed during retention")
	}
	return RetainedInputs{Source: sourceSnapshot, Producer: producerSnapshot}, nil
}
