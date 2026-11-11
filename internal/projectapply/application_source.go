package projectapply

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"golang.org/x/sys/unix"
	ab "loom.local/loom/internal/applicationbuild"
	pc "loom.local/loom/internal/projectcontracts"
	sr "loom.local/loom/internal/serviceregistry"
)

const sourceOutputMaxBytes = 2 * 1024 * 1024
const sourceDefaultMemoryBytes = 256 * 1024 * 1024

var ErrSourceApplicationOutputMissing = errors.New("application source: retained output missing")

type ApplicationSourceBuilder interface {
	Snapshot(context.Context, ab.Input) (ab.Snapshot, error)
	Build(context.Context, ab.Input) (ab.Output, error)
}

// Planning exposes no provision, publication, credential acquisition or runtime
// effects. The existing application preparer continues to own those operations.
type ApplicationSourcePlanner interface {
	ApplicationPrerequisiteReader
	PlanProvision(context.Context, sr.ApplicationProvisionRequest) (sr.ApplicationProvisionPlan, error)
}

type SourceApplicationAdapter struct {
	Builder    ApplicationSourceBuilder
	Helper     ApplicationSourcePlanner
	OutputRoot string
	// Trusted node/grant ceiling, not application-authored memory authority.
	MemoryMaxBytes uint64
}

type ApplicationBuildQueue interface {
	Ensure(context.Context, ActionCall, applicationSourcePayload) (bool, error)
	Observe(context.Context, ActionCall) (Observation, error)
}

type applicationSourcePayload struct {
	Target               pc.DeclarationTarget               `json:"target"`
	Resource             pc.ResourceKey                     `json:"resource"`
	Declaration          pc.ApplicationDeclaration          `json:"declaration"`
	ExpectedSourceDigest string                             `json:"expected_source_digest"`
	SourceSnapshot       ab.Snapshot                        `json:"source_snapshot"`
	ProvisionPlan        sr.ApplicationProvisionPlan        `json:"provision_plan"`
	InitialPrerequisites sr.ApplicationPrerequisiteSnapshot `json:"initial_prerequisites"`
	MemoryMaxBytes       uint64                             `json:"memory_max_bytes"`
}

// The outer owner decoder predates interface-valued source config. Preserve
// exact integers even when it does not itself use json.Decoder.UseNumber.
func (p *applicationSourcePayload) UnmarshalJSON(raw []byte) error {
	if _, err := CanonicalJSON(raw); err != nil {
		return err
	}
	type plain applicationSourcePayload
	var value plain
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	d.DisallowUnknownFields()
	if err := d.Decode(&value); err != nil {
		return fail(pc.DeclarationInvalid, "application_source_payload_invalid")
	}
	*p = applicationSourcePayload(value)
	return nil
}

func (a *SourceApplicationAdapter) Snapshot(ctx context.Context, target pc.DeclarationTarget, key pc.ResourceKey, declaration pc.ApplicationDeclaration) (applicationSourcePayload, error) {
	var p applicationSourcePayload
	if err := ctx.Err(); err != nil {
		return p, err
	}
	input, err := sourceApplicationInput(target, key, declaration)
	if err != nil {
		return p, err
	}
	if a == nil || nilInterface(a.Builder) || nilInterface(a.Helper) || a.MemoryMaxBytes == 0 {
		return p, fail(pc.DeclarationTargetUnavailable, "application_source_not_configured")
	}
	if err := sourceApplicationPath(input.SourceRoot); err != nil {
		return p, err
	}
	snapshot, err := a.Builder.Snapshot(ctx, input)
	if err != nil {
		return p, sourceApplicationFailure(ctx, err)
	}
	if !digestPattern.MatchString(snapshot.ProducerDigest) {
		return p, fail(pc.DeclarationOwnerFailed, "application_producer_identity_invalid")
	}
	owner := sr.ApplicationOwner{ProjectID: target.ProjectID, NodeID: target.OwnerNodeID, Resource: string(key)}
	facts, err := a.Helper.QueryPrerequisites(ctx, sr.ApplicationPrerequisiteQuery{SchemaVersion: sr.ApplicationPrerequisiteSchema, Owner: owner})
	if ctx.Err() != nil {
		return p, ctx.Err()
	}
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return p, err
		}
		return p, applicationFailure(err)
	}
	// Collection time and startup presentation do not change deployment intent.
	// The helper's stable identity revision still pins the actual prerequisites.
	facts.CollectedAt = time.Time{}
	facts.Observation = nil
	request := sr.ApplicationProvisionRequest{SchemaVersion: sr.ApplicationProvisioningSchema, Owner: owner,
		SourceIdentity: target.ProjectID + "/" + string(key), LocationRevision: target.LocationRevision,
		Data: map[string]string{}, CredentialSources: map[string]string{}, Network: sourceApplicationNetwork(declaration),
		ProjectState: map[string]string{}}
	for key, data := range declaration.Data {
		ref := data.BindingRef
		if ref == "" {
			ref = sourceApplicationBinding(input.Source.Resource, string(key))
		}
		request.Data[string(key)] = ref
		request.CloudBackup = request.CloudBackup || data.Backup == "cloud_history"
		if data.Path != "" {
			request.ProjectRoot = target.ProjectRoot
			request.ProjectState[string(key)] = data.Path
		}
	}
	for key, source := range declaration.CredentialSources {
		request.CredentialSources[sourceApplicationBinding(input.Source.Resource, key)] = source
	}
	plan, err := a.Helper.PlanProvision(ctx, request)
	if ctx.Err() != nil {
		return p, ctx.Err()
	}
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return p, err
		}
		return p, applicationFailure(err)
	}
	p = applicationSourcePayload{Target: target, Resource: key, Declaration: declaration,
		ExpectedSourceDigest: snapshot.SourceDigest, SourceSnapshot: snapshot,
		ProvisionPlan: plan, InitialPrerequisites: facts, MemoryMaxBytes: a.MemoryMaxBytes}
	if _, err := validateSourceApplicationPayload(p); err != nil {
		return applicationSourcePayload{}, err
	}
	// Return owned maps/pointers; retained payloads must not alias planner inputs.
	raw, _ := json.Marshal(p)
	if err := json.Unmarshal(raw, &p); err != nil {
		return applicationSourcePayload{}, err
	}
	return p, nil
}

// Resolve(false) only reads retained output: it neither snapshots live source
// nor invokes the producer/helper. Resolve(true) fences source even on cache
// hits; only a missing output permits a build. No deployment DB is introduced.
func (a *SourceApplicationAdapter) Resolve(ctx context.Context, p applicationSourcePayload, build bool) (applicationPayload, error) {
	return a.resolve(ctx, p, build, "")
}

func (a *SourceApplicationAdapter) resolve(ctx context.Context, p applicationSourcePayload, build bool, frozenRoot string) (applicationPayload, error) {
	if err := ctx.Err(); err != nil {
		return applicationPayload{}, err
	}
	input, err := validateSourceApplicationPayload(p)
	if err != nil {
		return applicationPayload{}, err
	}
	if a == nil {
		return applicationPayload{}, fail(pc.DeclarationTargetUnavailable, "application_source_not_configured")
	}
	if !filepath.IsAbs(a.OutputRoot) || filepath.Clean(a.OutputRoot) != a.OutputRoot || a.OutputRoot == "/" || sourcePathContains(p.Target.ProjectRoot, a.OutputRoot) {
		return applicationPayload{}, fail(pc.DeclarationInvalid, "application_source_output_root_invalid")
	}
	if build && frozenRoot == "" {
		if nilInterface(a.Builder) {
			return applicationPayload{}, fail(pc.DeclarationTargetUnavailable, "application_source_not_configured")
		}
		if err := sourceApplicationPath(input.SourceRoot); err != nil {
			return applicationPayload{}, err
		}
		current, err := a.Builder.Snapshot(ctx, input)
		if err != nil {
			return applicationPayload{}, sourceApplicationFailure(ctx, err)
		}
		if current.SourceDigest != p.ExpectedSourceDigest {
			return applicationPayload{}, fail(pc.DeclarationPlanStale, "application_source_changed")
		}
		if p.SourceSnapshot.ProducerDigest == "" || current.ProducerDigest != p.SourceSnapshot.ProducerDigest {
			return applicationPayload{}, fail(pc.DeclarationPlanStale, "application_producer_changed")
		}
	}
	raw, err := canonicalValue(struct {
		Schema   string
		Target   pc.DeclarationTarget
		Input    ab.Input
		Producer string `json:",omitempty"`
	}{"application.source.input.v1", p.Target, input, p.SourceSnapshot.ProducerDigest})
	if err != nil {
		return applicationPayload{}, err
	}
	key := hashBytes(raw)
	output, err := a.readSourceOutput(ctx, key)
	if err == nil {
		return materializeSourceApplication(p, input, output)
	}
	if !errors.Is(err, ErrSourceApplicationOutputMissing) || !build {
		return applicationPayload{}, err
	}
	buildInput := input
	if frozenRoot != "" {
		buildInput.SourceRoot = frozenRoot
	}
	output, err = a.Builder.Build(ctx, buildInput)
	if err != nil {
		if frozenRoot != "" {
			return applicationPayload{}, err
		}
		return applicationPayload{}, sourceApplicationFailure(ctx, err)
	}
	result, err := materializeSourceApplication(p, input, output)
	if err != nil {
		return applicationPayload{}, err
	}
	if err := a.retainSourceOutput(ctx, key, output); err != nil {
		return applicationPayload{}, err
	}
	return result, nil
}

func sourceApplicationInput(target pc.DeclarationTarget, key pc.ResourceKey, declaration pc.ApplicationDeclaration) (ab.Input, error) {
	var input ab.Input
	if validTarget(target) != nil || !keyPattern.MatchString(string(key)) || pc.ValidateApplicationSource(declaration) != "" {
		return input, fail(pc.DeclarationInvalid, "application_source_payload_invalid")
	}
	input = ab.Input{Source: ab.SourceIdentity{ProjectID: target.ProjectID, Resource: string(key)},
		SourceRoot: filepath.Join(target.ProjectRoot, declaration.Source.Path), PythonModule: declaration.Source.PythonModule,
		Config: map[string]any{}, DataParameters: []string{}, CredentialParameters: []string{}, Actions: map[string]ab.Action{}}
	input.Build, input.Run, input.NixAttribute = declaration.Source.Build, declaration.Run, declaration.Source.NixAttribute
	legacy := input.PythonModule != "" && input.Build == nil && input.Run == nil && input.NixAttribute == ""
	input.PackageFiles = append([]string{}, declaration.Source.Files...)
	sort.Strings(input.PackageFiles)
	if declaration.Config != nil {
		input.ConfigurationSchema = declaration.Config.Schema
		for k, v := range declaration.Config.Values {
			input.Config[k] = v
		}
	}
	for key := range declaration.Data {
		input.DataParameters = append(input.DataParameters, string(key))
	}
	for key := range declaration.CredentialSources {
		input.CredentialParameters = append(input.CredentialParameters, key)
	}
	for name, action := range declaration.Actions {
		if !legacy {
			input.Actions[name] = ab.Action{PythonModule: action.PythonModule, Binary: action.Binary, Arguments: append([]ab.Argument{}, action.Args...), ReadOnly: action.ReadOnly}
			continue
		}
		args := make([]string, 0, len(action.Args))
		for _, arg := range action.Args {
			if arg.Literal == nil || arg.ConfigFile {
				return ab.Input{}, fail(pc.DeclarationUnsupported, "application_source_argument_unsupported")
			}
			args = append(args, *arg.Literal)
		}
		input.Actions[name] = ab.Action{PythonModule: action.PythonModule, Args: args, ReadOnly: action.ReadOnly}
	}
	sort.Strings(input.DataParameters)
	sort.Strings(input.CredentialParameters)
	if !sourcePathContains(target.ProjectRoot, input.SourceRoot) || len(input.Config)+len(input.DataParameters)+len(input.CredentialParameters) > 64 {
		return ab.Input{}, fail(pc.DeclarationInvalid, "application_source_payload_invalid")
	}
	return input, nil
}

func sourceApplicationBinding(resource, key string) string {
	return "application." + resource + "." + key
}

func sourceApplicationNetwork(d pc.ApplicationDeclaration) sr.ApplicationNetworkProfile {
	if d.Network == "" {
		return sr.ApplicationNetworkLoopback
	}
	return sr.ApplicationNetworkProfile(d.Network)
}

func validateSourceApplicationPayload(p applicationSourcePayload) (ab.Input, error) {
	if p.SourceSnapshot.ProducerDigest != "" && !digestPattern.MatchString(p.SourceSnapshot.ProducerDigest) {
		return ab.Input{}, fail(pc.DeclarationInvalid, "application_source_payload_invalid")
	}
	input, err := sourceApplicationInput(p.Target, p.Resource, p.Declaration)
	if err != nil {
		return input, err
	}
	input.ExpectedSourceDigest = p.ExpectedSourceDigest
	q, f := p.ProvisionPlan.Request, p.InitialPrerequisites
	owner := sr.ApplicationOwner{ProjectID: p.Target.ProjectID, NodeID: p.Target.OwnerNodeID, Resource: string(p.Resource)}
	identity := p.Target.ProjectID + "/" + string(p.Resource)
	if !digestPattern.MatchString(p.ExpectedSourceDigest) || p.SourceSnapshot.SourceDigest != p.ExpectedSourceDigest || !digestPattern.MatchString(p.SourceSnapshot.PolicyDigest) || len(p.SourceSnapshot.Files) == 0 || len(p.SourceSnapshot.Files) > 2048 || p.MemoryMaxBytes == 0 || p.ProvisionPlan.Validate() != nil || q.Owner != owner || q.RepositoryID != "" || q.SourceIdentity != identity || q.LocationRevision != p.Target.LocationRevision || q.Endpoint != nil || f.SchemaVersion != sr.ApplicationPrerequisiteSchema || f.Owner != owner || (f.Platform != "x86_64-linux" && f.Platform != "aarch64-linux") || (!f.GrantMissing && (f.ProducerIdentity() != identity || f.LocationRevision != p.Target.LocationRevision)) || len(q.Data) != len(p.Declaration.Data) || len(q.CredentialSources) != len(p.Declaration.CredentialSources) {
		return input, fail(pc.DeclarationIdentityConflict, "application_source_plan_mismatch")
	}
	if f.Installation.Present && (f.Installation.Fenced == nil || *f.Installation.Fenced || f.Installation.Retired == nil || *f.Installation.Retired) {
		return input, fail(pc.DeclarationTargetUnavailable, "application_installation_fenced")
	}
	if q.Network != sourceApplicationNetwork(p.Declaration) {
		return input, fail(pc.DeclarationIdentityConflict, "application_source_plan_mismatch")
	}
	wantsBackup := false
	stateCount := 0
	for key, value := range p.Declaration.Data {
		ref := q.Data[string(key)]
		if ref == "" || (value.BindingRef != "" && value.BindingRef != ref) {
			return input, fail(pc.DeclarationIdentityConflict, "application_source_plan_mismatch")
		}
		wantsBackup = wantsBackup || value.Backup == "cloud_history"
		if value.Path != "" {
			stateCount++
			state, ok := q.StateIdentities[string(key)]
			if !ok || q.ProjectRoot != p.Target.ProjectRoot || q.ProjectState[string(key)] != value.Path || state.ProjectRoot != p.Target.ProjectRoot || state.RelativePath != value.Path || state.Owner != owner || state.RootIdentity.Inode == 0 || state.DirectoryIdentity.Inode == 0 {
				return input, fail(pc.DeclarationIdentityConflict, "application_source_plan_mismatch")
			}
		} else if q.ProjectState[string(key)] != "" {
			return input, fail(pc.DeclarationIdentityConflict, "application_source_plan_mismatch")
		}
		if fact, exists := f.Data[string(key)]; exists && (fact.Availability == "unavailable" || fact.Custody == "conflict" || fact.BindingRef != ref) {
			return input, fail(pc.DeclarationTargetUnavailable, "application_data_unavailable")
		}
	}
	if len(q.ProjectState) != stateCount || len(q.StateIdentities) != stateCount || (stateCount == 0 && q.ProjectRoot != "") {
		return input, fail(pc.DeclarationIdentityConflict, "application_source_plan_mismatch")
	}
	if wantsBackup != q.CloudBackup {
		return input, fail(pc.DeclarationIdentityConflict, "application_source_plan_mismatch")
	}
	for key, source := range p.Declaration.CredentialSources {
		if q.CredentialSources[sourceApplicationBinding(string(p.Resource), key)] != source {
			return input, fail(pc.DeclarationIdentityConflict, "application_source_plan_mismatch")
		}
	}
	return input, nil
}

func materializeSourceApplication(p applicationSourcePayload, input ab.Input, out ab.Output) (applicationPayload, error) {
	var result applicationPayload
	x := out.Descriptor
	if len(x.Actions) != len(input.Actions) {
		return result, fail(pc.DeclarationIdentityConflict, "application_source_output_invalid")
	}
	for name, action := range input.Actions {
		definition, ok := x.Actions[name]
		if !ok || definition.ReadOnly != action.ReadOnly {
			return result, fail(pc.DeclarationIdentityConflict, "application_source_output_invalid")
		}
	}
	schema := input.ConfigurationSchema
	if schema == "" {
		schema = "application." + string(p.Resource)
	}
	mraw, _ := json.Marshal(out.Manifest)
	draw, _ := json.Marshal(out.Descriptor)
	legacyOutput := x.SchemaVersion == "application.build.artifact.v1" && out.Manifest.SchemaVersion == "application.build.manifest.v1" && out.Manifest.Program == nil && p.SourceSnapshot.Program == nil
	programEqual := func() bool {
		a, _ := json.Marshal(out.Manifest.Program)
		b, _ := json.Marshal(p.SourceSnapshot.Program)
		return bytes.Equal(a, b)
	}
	nativeOutput := x.SchemaVersion == "application.build.artifact.v2" && out.Manifest.SchemaVersion == "application.build.manifest.v2" && out.Manifest.Program != nil && p.SourceSnapshot.Program != nil && programEqual()
	if !(legacyOutput || nativeOutput) || x.Source.ProjectID != p.Target.ProjectID || x.Source.Resource != string(p.Resource) || x.Source.RepositoryID != "" || x.Source.Revision != p.ExpectedSourceDigest || x.SourceDigest != p.ExpectedSourceDigest || x.Artifact.Platform != p.InitialPrerequisites.Platform || out.Manifest.Source != x.Source || out.Manifest.PythonModule != input.PythonModule || out.Manifest.Artifact != x.Artifact || out.Manifest.Configuration.Schema != schema || x.Configuration.Schema != schema || !digestPattern.MatchString(x.ToolchainDigest) || (p.SourceSnapshot.ProducerDigest != "" && x.ToolchainDigest != p.SourceSnapshot.ProducerDigest) || !digestPattern.MatchString(x.BuildDigest) || !bytes.Equal(mraw, out.ManifestJSON) || !bytes.Equal(draw, out.DescriptorJSON) || hashBytes(mraw) != out.ManifestDigest || hashBytes(draw) != out.DescriptorDigest || !slices.Equal(out.Manifest.Files, p.SourceSnapshot.Files) || !slices.Equal(out.Manifest.DataParameters, input.DataParameters) || !slices.Equal(out.Manifest.CredentialParameters, input.CredentialParameters) {
		return result, fail(pc.DeclarationIdentityConflict, "application_source_output_invalid")
	}
	actualValues := map[string]any{}
	for key, value := range out.Manifest.Configuration.Values {
		switch {
		case value.Type == sr.ApplicationString && value.String != nil:
			actualValues[key] = *value.String
		case value.Type == sr.ApplicationInteger && value.Integer != nil:
			actualValues[key] = *value.Integer
		case value.Type == sr.ApplicationBoolean && value.Boolean != nil:
			actualValues[key] = *value.Boolean
		default:
			return result, fail(pc.DeclarationIdentityConflict, "application_source_output_invalid")
		}
	}
	wanted, err := canonicalValue(input.Config)
	if err != nil {
		return result, err
	}
	actual, err := canonicalValue(actualValues)
	if err != nil || !bytes.Equal(wanted, actual) {
		return result, fail(pc.DeclarationIdentityConflict, "application_source_output_invalid")
	}
	values := make(map[string]sr.ApplicationParameter, len(out.Manifest.Configuration.Values)+len(input.DataParameters)+len(input.CredentialParameters))
	for key, value := range out.Manifest.Configuration.Values {
		values[key] = value
	}
	for _, key := range input.DataParameters {
		ref := pc.ResourceKey(key)
		values[key] = sr.ApplicationParameter{Type: sr.ApplicationDataReference, DataRef: &ref}
	}
	for _, key := range input.CredentialParameters {
		ref := sourceApplicationBinding(string(p.Resource), key)
		values[key] = sr.ApplicationParameter{Type: sr.ApplicationCredentialReference, CredentialRef: &ref}
	}
	memory := min(p.MemoryMaxBytes, uint64(sourceDefaultMemoryBytes))
	manifest := sr.ApplicationManifest{Kind: "loom.application", SchemaVersion: sr.ApplicationContractSchema,
		Artifact: x.Artifact, Process: sr.ApplicationProcess{Manager: sr.ManagerSystemd, User: "app", Restart: "on-failure", MemoryMaxBytes: memory},
		Config: sr.ApplicationConfiguration{Schema: schema, Values: values}, Health: sr.ApplicationHealth{Kind: sr.HealthKindManager},
		Data: map[pc.ResourceKey]sr.ApplicationDataRequirements{}}
	for key := range p.Declaration.Data {
		manifest.Data[key] = sr.ApplicationDataRequirements{}
	}
	raw, _ := json.Marshal(manifest)
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(raw, &fields)
	delete(fields, "listener")
	raw, _ = json.Marshal(fields)
	manifest, err = sr.ParseApplicationManifest(raw)
	if err != nil {
		return result, fail(pc.DeclarationInvalid, "application_source_manifest_invalid")
	}
	descriptor := sr.ApplicationArtifactDescriptor{SchemaVersion: "application.artifact.v2", SourceIdentity: p.Target.ProjectID + "/" + string(p.Resource),
		SourceRevision: x.Source.Revision, SourceDigest: x.SourceDigest, Artifact: x.Artifact,
		StoreRoot: x.StoreRoot, Launcher: x.Launcher, Executable: x.Executable, Closure: x.Closure, ClosureDigest: x.ClosureDigest,
		Configuration: x.Configuration, ConfigurationDigest: x.ConfigurationDigest, DataFormat: x.DataFormat, CompatibleDataFormats: x.CompatibleDataFormats, Actions: x.Actions}
	q := sr.ApplicationRuntimeRequest{SchemaVersion: sr.ApplicationRuntimeSchema, Operation: "apply", Owner: p.ProvisionPlan.Request.Owner,
		ExpectedInstallationRevision: p.InitialPrerequisites.Installation.Revision, LocationRevision: p.Target.LocationRevision,
		PolicyRevision: p.ProvisionPlan.PolicyRevision, ExpectedRevision: sr.ApplicationPreparedRevision(p.ProvisionPlan, descriptor),
		DescriptorDigest: sr.ApplicationDescriptorDigest(descriptor), Manifest: &manifest, Data: map[string]sr.ApplicationDataRequest{}, CredentialRevisions: map[string]string{}}
	for key, data := range p.Declaration.Data {
		request := sr.ApplicationDataRequest{BindingRef: p.ProvisionPlan.Request.Data[string(key)], Backup: data.Backup}
		if data.Capacity != nil {
			request.PlannedBytes = data.Capacity.PlannedBytes
		}
		q.Data[string(key)] = request
	}
	for ref, source := range p.ProvisionPlan.Request.CredentialSources {
		q.CredentialRevisions[ref] = sr.ApplicationProtonCredentialRevision(source)
	}
	result = applicationPayload{Request: q, Preparation: &applicationPreparation{Grant: p.ProvisionPlan, Descriptor: descriptor, ExpectedPublication: p.InitialPrerequisites.Publication.Revision}}
	if err := validateApplicationPreparation(result); err != nil {
		return applicationPayload{}, err
	}
	return result, nil
}

func sourcePathContains(root, candidate string) bool {
	return candidate == root || strings.HasPrefix(candidate, root+string(filepath.Separator))
}

func sourceApplicationPath(root string) error {
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil || resolved != root {
		return fail(pc.DeclarationInvalid, "application_source_path_invalid")
	}
	return nil
}

func sourceApplicationFailure(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	if errors.Is(err, ab.ErrSourceChanged) {
		return fail(pc.DeclarationPlanStale, "application_source_changed")
	}
	if errors.Is(err, ab.ErrInvalidInput) || errors.Is(err, ab.ErrUnsafeSource) {
		return fail(pc.DeclarationInvalid, "application_source_invalid")
	}
	var diagnostic *ab.Diagnostic
	if errors.As(err, &diagnostic) {
		return fail(pc.DeclarationUnsupported, "application_"+diagnostic.Code)
	}
	return fail(pc.DeclarationOwnerFailed, "application_source_build_failed")
}

type sourceOutputRecord struct {
	SchemaVersion string    `json:"schema_version"`
	InputHash     string    `json:"input_hash"`
	Output        ab.Output `json:"output"`
}

func (a *SourceApplicationAdapter) readSourceOutput(ctx context.Context, key string) (ab.Output, error) {
	if err := ctx.Err(); err != nil {
		return ab.Output{}, err
	}
	root, err := openSourceOutputRoot(a.OutputRoot, false)
	if err != nil {
		return ab.Output{}, err
	}
	defer root.Close()
	if err := lockSourceOutput(ctx, root, false); err != nil {
		return ab.Output{}, err
	}
	defer unix.Flock(int(root.Fd()), unix.LOCK_UN)
	return readSourceOutputFile(ctx, root, key)
}

func readSourceOutputFile(ctx context.Context, root *os.File, key string) (ab.Output, error) {
	name := strings.TrimPrefix(key, "sha256:") + ".json"
	fd, err := unix.Openat(int(root.Fd()), name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err == unix.ENOENT {
		return ab.Output{}, ErrSourceApplicationOutputMissing
	}
	if err != nil {
		return ab.Output{}, fail(pc.DeclarationIdentityConflict, "application_source_output_custody")
	}
	file := os.NewFile(uintptr(fd), name)
	defer file.Close()
	var st unix.Stat_t
	if unix.Fstat(fd, &st) != nil || st.Mode&unix.S_IFMT != unix.S_IFREG || st.Uid != uint32(os.Geteuid()) || st.Nlink != 1 || st.Mode&07777 != 0400 || st.Size < 1 || st.Size > sourceOutputMaxBytes {
		return ab.Output{}, fail(pc.DeclarationIdentityConflict, "application_source_output_custody")
	}
	raw, err := io.ReadAll(io.LimitReader(file, sourceOutputMaxBytes+1))
	if err != nil || int64(len(raw)) != st.Size {
		return ab.Output{}, fail(pc.DeclarationIdentityConflict, "application_source_output_invalid")
	}
	if _, err := CanonicalJSON(raw); err != nil {
		return ab.Output{}, fail(pc.DeclarationIdentityConflict, "application_source_output_invalid")
	}
	var record sourceOutputRecord
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	d.DisallowUnknownFields()
	if d.Decode(&record) != nil || record.SchemaVersion != "application.source.output.v1" || record.InputHash != key {
		return ab.Output{}, fail(pc.DeclarationIdentityConflict, "application_source_output_invalid")
	}
	if err := ctx.Err(); err != nil {
		return ab.Output{}, err
	}
	return record.Output, nil
}

func (a *SourceApplicationAdapter) retainSourceOutput(ctx context.Context, key string, output ab.Output) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	output.Reused = false
	raw, err := json.Marshal(sourceOutputRecord{SchemaVersion: "application.source.output.v1", InputHash: key, Output: output})
	if err != nil || len(raw) > sourceOutputMaxBytes {
		return fail(pc.DeclarationOwnerFailed, "application_source_output_size")
	}
	root, err := openSourceOutputRoot(a.OutputRoot, true)
	if err != nil {
		return err
	}
	defer root.Close()
	if err := lockSourceOutput(ctx, root, true); err != nil {
		return err
	}
	defer unix.Flock(int(root.Fd()), unix.LOCK_UN)
	retained, readErr := readSourceOutputFile(ctx, root, key)
	if readErr == nil {
		retained.Reused = false
		left, _ := json.Marshal(retained)
		right, _ := json.Marshal(output)
		if !bytes.Equal(left, right) {
			return fail(pc.DeclarationIdentityConflict, "application_source_output_conflict")
		}
		return nil
	}
	if !errors.Is(readErr, ErrSourceApplicationOutputMissing) {
		return readErr
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return fail(pc.DeclarationOwnerFailed, "application_source_output_write_failed")
	}
	temporary := ".pending-" + hex.EncodeToString(nonce[:])
	fd, err := unix.Openat(int(root.Fd()), temporary, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if err != nil {
		return fail(pc.DeclarationOwnerFailed, "application_source_output_write_failed")
	}
	defer unix.Unlinkat(int(root.Fd()), temporary, 0)
	file := os.NewFile(uintptr(fd), temporary)
	defer file.Close()
	if _, err := file.Write(raw); err != nil {
		return fail(pc.DeclarationOwnerFailed, "application_source_output_write_failed")
	}
	if file.Chmod(0400) != nil || file.Sync() != nil {
		return fail(pc.DeclarationOwnerFailed, "application_source_output_write_failed")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	name := strings.TrimPrefix(key, "sha256:") + ".json"
	// The directory flock serializes the absence check and atomic publish. A
	// completed entry is never replaced; rename cannot leave a linked half-entry.
	if file.Close() != nil || unix.Renameat(int(root.Fd()), temporary, int(root.Fd()), name) != nil || root.Sync() != nil {
		return fail(pc.DeclarationOwnerFailed, "application_source_output_write_failed")
	}
	return nil
}

func lockSourceOutput(ctx context.Context, root *os.File, exclusive bool) error {
	mode := unix.LOCK_SH
	if exclusive {
		mode = unix.LOCK_EX
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := unix.Flock(int(root.Fd()), mode|unix.LOCK_NB)
		if err == nil {
			return nil
		}
		if err != unix.EWOULDBLOCK && err != unix.EAGAIN {
			return fail(pc.DeclarationTargetUnavailable, "application_source_output_unavailable")
		}
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

// All ancestors are opened with O_NOFOLLOW. Shared writable directories are
// refused except root-owned sticky system parents; the final cache is private
// to this backend UID. A read never creates directories or lock files.
func openSourceOutputRoot(path string, create bool) (*os.File, error) {
	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fail(pc.DeclarationTargetUnavailable, "application_source_output_unavailable")
	}
	current := os.NewFile(uintptr(fd), "/")
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	for index, part := range parts {
		next, err := unix.Openat(int(current.Fd()), part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
		if err == unix.ENOENT && create {
			if err = unix.Mkdirat(int(current.Fd()), part, 0700); err == nil || err == unix.EEXIST {
				next, err = unix.Openat(int(current.Fd()), part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
			}
		}
		current.Close()
		if err != nil {
			if err == unix.ENOENT && !create {
				return nil, ErrSourceApplicationOutputMissing
			}
			return nil, fail(pc.DeclarationIdentityConflict, "application_source_output_custody")
		}
		current = os.NewFile(uintptr(next), part)
		var st unix.Stat_t
		if unix.Fstat(next, &st) != nil {
			current.Close()
			return nil, fail(pc.DeclarationIdentityConflict, "application_source_output_custody")
		}
		sticky := st.Uid == 0 && st.Mode&unix.S_ISVTX != 0 && index != len(parts)-1
		if (st.Uid != 0 && st.Uid != uint32(os.Geteuid())) || (st.Mode&0022 != 0 && !sticky) || (st.Mode&07000 != 0 && !sticky) || (index == len(parts)-1 && (st.Uid != uint32(os.Geteuid()) || st.Mode&0077 != 0)) {
			current.Close()
			return nil, fail(pc.DeclarationIdentityConflict, "application_source_output_custody")
		}
	}
	return current, nil
}
