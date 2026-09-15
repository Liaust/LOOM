package serviceregistry

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"regexp"
	"strings"
)

const ApplicationRuntimeSchema = "application.runtime.v1"
const ApplicationSocketPath = "/run/loom-project-applications/helper.sock"
const ApplicationPolicyPath = "/etc/loom/project-applications.json"
const applicationMaxBytes = 256 * 1024

// ApplicationOwner is the immutable custody identity. Location/source changes
// are revisions, not a new operating-system account or data owner.
type ApplicationOwner struct {
	ProjectID string `json:"project_id"`
	NodeID    string `json:"node_id"`
	Resource  string `json:"resource"`
}

func (o ApplicationOwner) Instance() string     { return "la-" + applicationSHA(o)[7:31] }
func (o ApplicationOwner) Unit() string         { return "loom-application@" + o.Instance() + ".service" }
func (o ApplicationOwner) AllowlistKey() string { return "application." + o.Instance() }
func (o ApplicationOwner) valid() bool {
	return applicationProjectPattern.MatchString(o.ProjectID) && applicationNodePattern.MatchString(o.NodeID) && applicationKeyPattern.MatchString(o.Resource)
}

// The complete closure is sorted by store path before hashing. The trusted
// publisher attests provenance; installation independently verifies local NARs.
type ApplicationClosurePath struct {
	Path    string `json:"path"`
	NARHash string `json:"nar_hash"`
}
type ApplicationArtifactDescriptor struct {
	SchemaVersion         string                         `json:"schema_version"`
	RepositoryID          string                         `json:"repository_id"`
	SourceRevision        string                         `json:"source_revision"`
	SourceDigest          string                         `json:"source_digest"`
	Artifact              ApplicationArtifact            `json:"artifact"`
	StoreRoot             string                         `json:"store_root"`
	Launcher              string                         `json:"launcher"`
	Executable            string                         `json:"executable"`
	Closure               []ApplicationClosurePath       `json:"closure"`
	ClosureDigest         string                         `json:"closure_digest"`
	Configuration         ApplicationConfigurationSchema `json:"configuration"`
	ConfigurationDigest   string                         `json:"configuration_digest"`
	DataFormat            string                         `json:"data_format"`
	CompatibleDataFormats []string                       `json:"compatible_data_formats"`
}

var applicationNodePattern = regexp.MustCompile(`^node_[0-7][0-9A-HJKMNP-TV-Z]{25}$`)
var applicationSourceRevisionPattern = regexp.MustCompile(`^([0-9a-f]{40}|[0-9a-f]{64}|sha256:[0-9a-f]{64})$`)
var applicationStorePattern = regexp.MustCompile(`^/nix/store/[0-9a-df-np-sv-z]{32}-[A-Za-z0-9+._?=-]+$`)
var applicationRevisionPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9:._-]{0,127}$`)

func (d ApplicationArtifactDescriptor) Validate() error {
	if d.SchemaVersion != "application.artifact.v1" || !applicationRepoPattern.MatchString(d.RepositoryID) || !applicationSourceRevisionPattern.MatchString(d.SourceRevision) || !applicationDigestPattern.MatchString(d.SourceDigest) || !applicationRef(d.Artifact.Ref) || !applicationDigestPattern.MatchString(d.Artifact.Digest) || (d.Artifact.Platform != "aarch64-linux" && d.Artifact.Platform != "x86_64-linux") || !applicationStorePattern.MatchString(d.StoreRoot) || d.Launcher != d.StoreRoot+"/bin/loom-application-launcher" || !applicationAbsolutePath(d.Executable) || !strings.HasPrefix(d.Executable, "/nix/store/") || len(d.Closure) == 0 || len(d.Closure) > 2048 || !applicationRevisionPattern.MatchString(d.DataFormat) || len(d.CompatibleDataFormats) > 32 {
		return applicationError("artifact.invalid")
	}
	previous := ""
	root := false
	for _, p := range d.Closure {
		if !applicationStorePattern.MatchString(p.Path) || p.Path <= previous || !strings.HasPrefix(p.NARHash, "sha256-") || len(p.NARHash) != 51 {
			return applicationError("artifact.closure_invalid")
		}
		decoded, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(p.NARHash, "sha256-"))
		if err != nil || len(decoded) != 32 {
			return applicationError("artifact.nar_hash")
		}
		previous = p.Path
		root = root || p.Path == d.StoreRoot
	}
	if !root || applicationSHA(d.Closure) != d.ClosureDigest || applicationSHA(d.Configuration) != d.ConfigurationDigest {
		return applicationError("artifact.digest_mismatch")
	}
	if !applicationRef(d.Configuration.Schema) || d.Configuration.Parameters == nil || len(d.Configuration.Parameters) > 64 {
		return applicationError("artifact.schema_invalid")
	}
	for key, spec := range d.Configuration.Parameters {
		if !applicationKeyPattern.MatchString(key) || !applicationParameterType(spec.Type) {
			return applicationError("artifact.schema_invalid")
		}
	}
	for _, f := range d.CompatibleDataFormats {
		if !applicationRevisionPattern.MatchString(f) {
			return applicationError("artifact.data_format_invalid")
		}
	}
	return nil
}

type ApplicationDataPolicy struct {
	CloudBackup bool   `json:"cloud_backup,omitempty"`
	Path        string `json:"path"`
	Pool        string `json:"pool"`
	BindingRef  string `json:"binding_ref,omitempty"`
}
type ApplicationCredentialPolicy struct {
	Path     string `json:"path"`
	Revision string `json:"revision"`
}
type ApplicationGrant struct {
	Owner            ApplicationOwner                       `json:"owner"`
	RepositoryID     string                                 `json:"repository_id"`
	PolicyRevision   string                                 `json:"policy_revision"`
	LocationRevision string                                 `json:"location_revision"`
	MaxMemoryBytes   uint64                                 `json:"max_memory_bytes"`
	MaxPlannedBytes  uint64                                 `json:"max_planned_bytes,omitempty"`
	Data             map[string]ApplicationDataPolicy       `json:"data"`
	Credentials      map[string]ApplicationCredentialPolicy `json:"credentials"`
	Edge             *ApplicationEdgePolicy                 `json:"edge,omitempty"`
}

// Startup policy is root-owned. Publisher and installer are separate identities;
// a producer/delivery adapter, not an apply request, supplies artifact provenance.
type ApplicationHostPolicy struct {
	SchemaVersion string                         `json:"schema_version"`
	NodeID        string                         `json:"node_id"`
	Platform      string                         `json:"platform"`
	InstallerUID  uint32                         `json:"installer_uid"`
	PublisherUID  uint32                         `json:"publisher_uid"`
	Grants        []ApplicationGrant             `json:"grants"`
	Provisioning  *ApplicationProvisioningPolicy `json:"provisioning,omitempty"`
}

func (p ApplicationHostPolicy) grant(o ApplicationOwner) (ApplicationGrant, error) {
	var match *ApplicationGrant
	if p.SchemaVersion != "application.policy.v1" || p.NodeID != o.NodeID || p.InstallerUID == 0 || p.InstallerUID == p.PublisherUID || len(p.Grants) > 128 || (p.Platform != "x86_64-linux" && p.Platform != "aarch64-linux") {
		return ApplicationGrant{}, applicationError("policy.invalid")
	}
	for i := range p.Grants {
		if p.Grants[i].Owner == o {
			if match != nil {
				return ApplicationGrant{}, applicationError("policy.ambiguous")
			}
			match = &p.Grants[i]
		}
	}
	if match == nil || !o.valid() || !applicationRepoPattern.MatchString(match.RepositoryID) || !applicationRevisionPattern.MatchString(match.PolicyRevision) || !applicationRevisionPattern.MatchString(match.LocationRevision) || match.MaxMemoryBytes == 0 || len(match.Data) > 16 || len(match.Credentials) > 16 {
		return ApplicationGrant{}, applicationError("policy.scope_denied")
	}
	for k, v := range match.Data {
		if !applicationKeyPattern.MatchString(k) || !applicationAbsolutePath(v.Path) || !applicationAbsolutePath(v.Pool) || !strings.HasPrefix(v.Path, v.Pool+"/") || strings.Contains(v.Path, "/.git/") || strings.Contains(v.Path, "/.codex/") || strings.Contains(v.Path, "/worktrees/") {
			return ApplicationGrant{}, applicationError("data.policy_invalid")
		}
	}
	for ref, v := range match.Credentials {
		if !applicationRef(ref) || !applicationAbsolutePath(v.Path) || !applicationRevisionPattern.MatchString(v.Revision) {
			return ApplicationGrant{}, applicationError("credential.policy_invalid")
		}
	}
	return *match, nil
}
func applicationAbsolutePath(p string) bool {
	return filepath.IsAbs(p) && filepath.Clean(p) == p && p != "/" && !strings.ContainsAny(p, "\x00\n\r\t %\"\\")
}
func applicationSHA(v any) string {
	b, _ := json.Marshal(v)
	h := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(h[:])
}
func applicationError(code string) error { return errors.New("application." + code) }
func applicationDecodeJSON(raw []byte, out any) error {
	if len(raw) == 0 || len(raw) > applicationMaxBytes {
		return applicationError("request.size")
	}
	// YAML's structural validation rejects duplicate keys before the JSON decoder
	// assigns them; JSON decoding retains int64 values without float conversion.
	var shape any
	if applicationJSONShape(raw) != nil {
		return applicationError("request.shape")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(out) != nil {
		return applicationError("request.fields")
	}
	if d.Decode(&shape) != io.EOF {
		return applicationError("request.trailing")
	}
	return nil
}

func applicationJSONShape(raw []byte) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var walk func(int) error
	walk = func(depth int) error {
		if depth > 32 {
			return applicationError("request.depth")
		}
		t, e := d.Token()
		if e != nil {
			return e
		}
		delim, ok := t.(json.Delim)
		if !ok {
			return nil
		}
		switch delim {
		case '{':
			seen := map[string]bool{}
			for d.More() {
				key, e := d.Token()
				if e != nil {
					return e
				}
				k, ok := key.(string)
				if !ok || seen[k] {
					return applicationError("request.duplicate")
				}
				seen[k] = true
				if e = walk(depth + 1); e != nil {
					return e
				}
			}
		case '[':
			for d.More() {
				if e = walk(depth + 1); e != nil {
					return e
				}
			}
		default:
			return applicationError("request.shape")
		}
		_, e = d.Token()
		return e
	}
	if e := walk(0); e != nil {
		return e
	}
	if _, e := d.Token(); e != io.EOF {
		return applicationError("request.trailing")
	}
	return nil
}

// Producer/composition helpers use the exact wire canonicalization shared with
// publication and installation. Input schemas retain integer types throughout.
func ApplicationDescriptorDigest(d ApplicationArtifactDescriptor) string { return applicationSHA(d) }
func ApplicationConfigurationSchemaDigest(s ApplicationConfigurationSchema) string {
	return applicationSHA(s)
}
func ApplicationClosureDigest(paths []ApplicationClosurePath) string { return applicationSHA(paths) }
func ApplicationAccountUID(owner ApplicationOwner) uint32            { return applicationAccountUID(owner) }

func IsApplicationUnitBinding(key, unit string) bool {
	const prefix = "application.la-"
	if !strings.HasPrefix(key, prefix) || len(key) != len(prefix)+24 {
		return false
	}
	if _, e := hex.DecodeString(strings.TrimPrefix(key, prefix)); e != nil {
		return false
	}
	return unit == "loom-application@"+strings.TrimPrefix(key, "application.")+".service"
}
