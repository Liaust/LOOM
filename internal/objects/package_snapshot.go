package objects

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"loom.local/loom/internal/ids"
	"loom.local/loom/internal/objectstore"
	"loom.local/loom/internal/projects"
	"loom.local/loom/internal/requestctx"
)

const PackageSnapshotSchema = "loom.package.snapshot.v1"

type PackageRef struct {
	objectstore.PackageInfo
	ObjectID        string `json:"object_id"`
	ObjectVersionID string `json:"object_version_id"`
	BlobID          string `json:"blob_id"`
}
type ProducerPackageBinding struct {
	Kind         string `json:"kind"`
	ProducerID   string `json:"producer_id"`
	VersionID    string `json:"version_id"`
	VersionLabel string `json:"version_label"`
	// A string preserves the exact normalized manifest bytes across JSONB storage.
	ManifestJSON string `json:"manifest_json"`
	ManifestHash string `json:"manifest_hash"`
	ContentHash  string `json:"content_hash"`
}
type PackageBinding struct {
	Role        string                        `json:"role"`
	ProjectID   string                        `json:"project_id"`
	ScopeID     string                        `json:"scope_id"`
	OwnerNodeID string                        `json:"owner_node_id"`
	Source      *projects.RetainedSourceFacts `json:"source,omitempty"`
	Producer    *ProducerPackageBinding       `json:"producer,omitempty"`
}
type PackageSnapshot struct {
	Ref     PackageRef     `json:"ref"`
	Binding PackageBinding `json:"binding"`
}
type packageVersionMetadata struct {
	Schema  string                  `json:"schema"`
	Package objectstore.PackageInfo `json:"package"`
	Binding PackageBinding          `json:"binding"`
}

func validPackageDigest(s string) bool {
	return len(s) == 71 && strings.HasPrefix(s, "sha256:") && strings.Trim(s[7:], "0123456789abcdef") == ""
}
func validatePackageInfo(info objectstore.PackageInfo) error {
	if info.Format != objectstore.PackageFormat || !validPackageDigest(info.SHA256) || info.SizeBytes < 1024 || info.SizeBytes > objectstore.PackageMaxArchiveBytes || info.EntryCount < 0 || info.EntryCount > objectstore.PackageMaxEntries || info.ContentBytes < 0 || info.ContentBytes > objectstore.PackageMaxContentBytes {
		return fmt.Errorf("invalid retained package identity")
	}
	return nil
}
func ValidatePackageBinding(b PackageBinding) error {
	if ids.Validate(ids.ProjectPrefix, b.ProjectID) != nil || ids.Validate(ids.ScopePrefix, b.ScopeID) != nil || ids.Validate(ids.NodePrefix, b.OwnerNodeID) != nil {
		return fmt.Errorf("invalid retained package owner")
	}
	switch b.Role {
	case "source":
		if b.Source == nil || b.Producer != nil || b.Source.ProjectID != b.ProjectID || b.Source.ProjectScopeID != b.ScopeID || b.Source.OwnerNodeID != b.OwnerNodeID {
			return fmt.Errorf("invalid source package binding")
		}
		return projects.ValidateRetainedSourceFacts(*b.Source)
	case "producer":
		p := b.Producer
		if p == nil || b.Source != nil {
			return fmt.Errorf("invalid producer package binding")
		}
		kind, version := "", ""
		switch p.Kind {
		case "script":
			kind = ids.ScriptPrefix
			version = ids.ScriptVersionPrefix
		case "workflow":
			kind = ids.WorkflowPrefix
			version = ids.WorkflowVersionPrefix
		default:
			return fmt.Errorf("invalid producer kind")
		}
		if ids.Validate(kind, p.ProducerID) != nil || ids.Validate(version, p.VersionID) != nil || strings.TrimSpace(p.VersionLabel) == "" || !validPackageDigest(p.ContentHash) || !json.Valid([]byte(p.ManifestJSON)) || len(p.ManifestJSON) > int(objectstore.PackageMaxProducerBytes) || fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(p.ManifestJSON))) != p.ManifestHash {
			return fmt.Errorf("invalid producer package identity")
		}
		var manifest map[string]json.RawMessage
		if json.Unmarshal([]byte(p.ManifestJSON), &manifest) != nil || manifest == nil {
			return fmt.Errorf("producer manifest must be an object")
		}
		return nil
	default:
		return fmt.Errorf("invalid package binding role")
	}
}
func validatePackageMetadata(m packageVersionMetadata) error {
	if m.Schema != PackageSnapshotSchema {
		return fmt.Errorf("invalid package snapshot schema")
	}
	if err := validatePackageInfo(m.Package); err != nil {
		return err
	}
	if m.Binding.Role == "producer" && m.Package.ContentBytes > objectstore.PackageMaxProducerBytes {
		return fmt.Errorf("producer package limit")
	}
	return ValidatePackageBinding(m.Binding)
}
func decodePackageMetadata(raw []byte) (packageVersionMetadata, error) {
	var m packageVersionMetadata
	if len(raw) > int(objectstore.PackageMaxProducerBytes)+16384 {
		return m, fmt.Errorf("package metadata limit")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&m); err != nil {
		return m, err
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return m, fmt.Errorf("trailing package metadata")
	}
	return m, validatePackageMetadata(m)
}

func (s Service) packageAuthority(ctx context.Context, req requestctx.Context, b PackageBinding, scope string) error {
	if err := ValidatePackageBinding(b); err != nil {
		return err
	}
	if scope == "" || scope != b.ScopeID {
		return fmt.Errorf("retained package expected scope mismatch")
	}
	if s.DB == nil {
		return fmt.Errorf("package database required")
	}
	project, err := projects.NewService(s.DB).ResolveProjectRef(ctx, b.ProjectID)
	if err != nil {
		return err
	}
	if project.ProjectID != b.ProjectID || project.ProjectScopeID != scope || project.HomeNodeID == nil || *project.HomeNodeID != b.OwnerNodeID {
		return fmt.Errorf("retained package project scope mismatch")
	}
	if err := projects.EnsureRuntimeActive(ctx, s.DB, projects.RuntimeRef{ProjectID: b.ProjectID, ScopeID: scope, ResourceKind: "package_snapshot"}); err != nil {
		return err
	}
	auth, err := projects.NewService(s.DB).GetDeclarationAuthority(ctx, req, b.ProjectID, b.OwnerNodeID)
	if err != nil {
		return err
	}
	if !auth.CanRead {
		return fmt.Errorf("retained package read authority required")
	}
	return nil
}

// RetainPackage accepts only a sealed private capture. Initial-version metadata
// and ordinary ingestion records commit together; verification precedes receipt.
func (s Service) RetainPackage(ctx context.Context, req requestctx.Context, capture *objectstore.CapturedPackage, binding PackageBinding) (PackageSnapshot, error) {
	if capture == nil {
		return PackageSnapshot{}, fmt.Errorf("package capture required")
	}
	metadata := packageVersionMetadata{PackageSnapshotSchema, capture.Info, binding}
	if err := validatePackageMetadata(metadata); err != nil {
		return PackageSnapshot{}, err
	}
	if req.OriginNodeID != binding.OwnerNodeID {
		return PackageSnapshot{}, fmt.Errorf("package capture owner node mismatch")
	}
	if err := s.packageAuthority(ctx, req, binding, binding.ScopeID); err != nil {
		return PackageSnapshot{}, err
	}
	f, err := os.Open(capture.ArchivePath)
	if err != nil {
		return PackageSnapshot{}, err
	}
	info, err := objectstore.InspectPackage(ctx, f)
	f.Close()
	if err != nil {
		return PackageSnapshot{}, err
	}
	if info != capture.Info {
		return PackageSnapshot{}, fmt.Errorf("package capture identity mismatch")
	}
	raw, err := json.Marshal(map[string]any{"retained_package": metadata})
	if err != nil {
		return PackageSnapshot{}, err
	}
	result, err := s.ingestFile(ctx, req, IngestFileInput{Path: capture.ArchivePath, ScopeRef: binding.ScopeID, Name: "retained-" + binding.Role + ".tar", ObjectType: "package", StateClass: "canonical", Metadata: raw}, raw)
	if err != nil {
		return PackageSnapshot{}, err
	}
	v := result.Object.LatestVersion
	if v == nil || v.BlobID == nil {
		return PackageSnapshot{}, fmt.Errorf("retained package version missing")
	}
	ref := PackageRef{capture.Info, result.Object.Object.ObjectID, v.ObjectVersionID, *v.BlobID}
	return s.ReadPackageSnapshot(ctx, req, binding.ScopeID, ref)
}

// ReadPackageSnapshot verifies expected scope and typed initial-version binding
// in addition to E3c byte identity. It never reads latest object metadata.
func (s Service) ReadPackageSnapshot(ctx context.Context, req requestctx.Context, expectedScope string, ref PackageRef) (PackageSnapshot, error) {
	if err := validatePackageInfo(ref.PackageInfo); err != nil {
		return PackageSnapshot{}, err
	}
	if ids.Validate(ids.ObjectPrefix, ref.ObjectID) != nil || ids.Validate(ids.ObjectVersionPrefix, ref.ObjectVersionID) != nil || ids.Validate(ids.BlobPrefix, ref.BlobID) != nil || ids.Validate(ids.ScopePrefix, expectedScope) != nil {
		return PackageSnapshot{}, fmt.Errorf("invalid retained package reference")
	}
	source, err := s.ReadObjectVersionSource(ctx, ref.ObjectID, ref.ObjectVersionID)
	if err != nil {
		return PackageSnapshot{}, err
	}
	if source.HomeScopeID == nil || *source.HomeScopeID != expectedScope || source.BlobID != ref.BlobID || source.Blob.HashURI != ref.SHA256 || source.Blob.SizeBytes != ref.SizeBytes {
		return PackageSnapshot{}, fmt.Errorf("retained package tuple or scope mismatch")
	}
	var raw []byte
	var kind, node string
	var versionNumber int
	err = s.DB.QueryRowContext(ctx, `SELECT o.object_type,coalesce(v.source_node_id,''),v.version_number,v.metadata->'retained_package' FROM objects.objects o JOIN objects.object_versions v ON v.object_id=o.object_id WHERE o.object_id=$1 AND v.object_version_id=$2`, ref.ObjectID, ref.ObjectVersionID).Scan(&kind, &node, &versionNumber, &raw)
	if err != nil {
		return PackageSnapshot{}, err
	}
	metadata, err := decodePackageMetadata(raw)
	if err != nil {
		return PackageSnapshot{}, err
	}
	if kind != "package" || versionNumber != 1 || metadata.Package != ref.PackageInfo || metadata.Binding.OwnerNodeID != node {
		return PackageSnapshot{}, fmt.Errorf("retained package version binding mismatch")
	}
	if err := s.packageAuthority(ctx, req, metadata.Binding, expectedScope); err != nil {
		return PackageSnapshot{}, err
	}
	stage, err := os.MkdirTemp("", "loom-package-verify-")
	if err != nil {
		return PackageSnapshot{}, err
	}
	defer os.RemoveAll(stage)
	name := filepath.Join(stage, "package.tar")
	if err := s.Store.MaterializeVerified(ctx, source.Blob, name); err != nil {
		return PackageSnapshot{}, err
	}
	f, err := os.Open(name)
	if err != nil {
		return PackageSnapshot{}, err
	}
	info, err := objectstore.InspectPackage(ctx, f)
	f.Close()
	if err != nil {
		return PackageSnapshot{}, err
	}
	if info != ref.PackageInfo {
		return PackageSnapshot{}, fmt.Errorf("retained package archive mismatch")
	}
	// Authority can change while bytes are read. This remains a read receipt, not
	// the separate effect-time authorization fence E3e must implement.
	if err := s.packageAuthority(ctx, req, metadata.Binding, expectedScope); err != nil {
		return PackageSnapshot{}, err
	}
	return PackageSnapshot{ref, metadata.Binding}, nil
}
