package projectapply

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"syscall"

	"loom.local/loom/internal/box"
	"loom.local/loom/internal/capabilities"
	"loom.local/loom/internal/config"
	"loom.local/loom/internal/knowledge/migrationread"
	aw "loom.local/loom/internal/nodeagent/watchedroots"
	pc "loom.local/loom/internal/projectcontracts"
	"loom.local/loom/internal/projects"
	"loom.local/loom/internal/watchedroots"
)

type migrationObservation struct {
	Project  projects.DeclarationMigrationRead
	Roots    watchedroots.DeclarationMigrationRead
	Box      box.DeclarationMigrationRead
	Services capabilities.DeclarationMigrationRead
	Notes    migrationread.DeclarationMigrationRead
	NodeID   string
	NodeKey  string
	Errors   []string
}

// ConversionAssessment is an optional read interface on the existing service.
// It never resolves executable actions or invokes journal/owner mutations.
func (s *Service) ConversionAssessment(ctx context.Context, p Principal, req pc.DeclarationPlanRequest) (pc.DeclarationMigrationAssessment, error) {
	return s.conversionAssessment(ctx, p, req, nil)
}

// The hook is package-private and used only to exercise races between read phases.
func (s *Service) conversionAssessment(ctx context.Context, p Principal, req pc.DeclarationPlanRequest, between func()) (pc.DeclarationMigrationAssessment, error) {
	var empty pc.DeclarationMigrationAssessment
	r, ok := s.resolver.(*LocalResolver)
	if !ok || r == nil || r.Config == nil || r.DB == nil || s.auth == nil {
		return empty, fail(pc.DeclarationUnsupported, "conversion_source_owner_required")
	}
	if err := validPrincipal(p); err != nil {
		return empty, err
	}
	if req.SchemaVersion != pc.DeclarationRequestSchemaV05 || !migrationSelector(req.ProjectRef) || (req.NodeRef != "" && !migrationSelector(req.NodeRef)) || len(req.Effects) != 0 {
		return empty, fail(pc.DeclarationInvalid, "registered_conversion_selector_required")
	}
	cfg, err := r.Config()
	if err != nil {
		return empty, fail(pc.DeclarationTargetUnavailable, "local_config_unavailable")
	}
	first, err := r.migrationRead(ctx, p, req, cfg)
	if err != nil {
		return empty, err
	}
	target, err := migrationLocation(first, cfg)
	if err != nil {
		return empty, err
	}
	if err = s.auth.Read(ctx, p, target); err != nil {
		return empty, err
	}
	source, err := pc.CollectDeclarationMigrationSources(target.ProjectRoot)
	if err != nil {
		return empty, fail(pc.DeclarationTargetUnavailable, "project_source_unavailable")
	}
	defer source.Close()
	external := migrationCaptureExternal(first, cfg.BoxPath, target.ProjectRoot)
	defer external.Close()
	result, input := buildMigrationAssessment(first, source, target, external)
	if between != nil {
		between()
	}
	sourceErr := source.Check()
	currentConfig, e := r.Config()
	if e != nil {
		return empty, fail(pc.DeclarationTargetUnavailable, "local_config_unavailable")
	}
	current, err := r.migrationRead(ctx, p, req, currentConfig)
	if err != nil {
		return empty, err
	}
	now, err := migrationLocation(current, currentConfig)
	if err != nil {
		return empty, err
	}
	if err = s.auth.Read(ctx, p, now); err != nil {
		return empty, err
	}
	// Same complete selected owner closure, current configuration and physical
	// location, then a final source check. There is deliberately no retry loop.
	if sourceErr != nil || source.Check() != nil || !external.Check() || !reflect.DeepEqual(cfg, currentConfig) || now != target || migrationObservationDigest(first) != migrationObservationDigest(current) {
		result.State = "incomplete"
		for i := range result.Families {
			result.Families[i].Completeness = "unavailable"
		}
		for _, basis := range []*pc.MigrationFactBasis{&input.Project.Basis, &input.Membership.Basis, &input.Watches.Basis, &input.Coverage.Basis, &input.Legacy.Basis, &input.Evidence.Basis} {
			basis.Completeness = "unavailable"
		}
		result.Preview = pc.PreviewDeclarationMigration(input)
		if len(result.Issues) == 64 {
			result.Issues = result.Issues[:63]
		}
		result.Issues = append(result.Issues, pc.MigrationAssessmentIssue{Family: "sources", Code: "observed_generation_changed"})
	}
	return result, nil
}

func migrationSelector(ref string) bool {
	if ref == "" || len(ref) > 128 {
		return false
	}
	for _, c := range ref {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}

func (r *LocalResolver) migrationRead(ctx context.Context, p Principal, req pc.DeclarationPlanRequest, cfg config.Config) (out migrationObservation, err error) {
	out.Errors = []string{}
	tx, err := r.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return out, fail(pc.DeclarationTargetUnavailable, "owner_snapshot_unavailable")
	}
	defer tx.Rollback()
	var status string
	err = tx.QueryRowContext(ctx, `SELECT node_id,node_key,status FROM nodes.nodes WHERE node_id=$1 OR node_key=$1`, cfg.NodeID).Scan(&out.NodeID, &out.NodeKey, &status)
	if err != nil || status != "active" || !migrationSelector(out.NodeID) || !migrationSelector(out.NodeKey) {
		return out, fail(pc.DeclarationTargetUnavailable, "configured_node_unavailable")
	}
	if req.NodeRef != "" && req.NodeRef != out.NodeID && req.NodeRef != out.NodeKey {
		return out, fail(pc.DeclarationTargetUnavailable, "remote_source_owner_required")
	}
	project, err := projects.ReadDeclarationMigrationTargetTx(ctx, tx, req.ProjectRef)
	if err != nil {
		return out, projectReferenceLookupError(err)
	}
	if project.HomeNodeID == nil || *project.HomeNodeID != out.NodeID {
		return out, fail(pc.DeclarationTargetUnavailable, "remote_source_owner_required")
	}
	out.Project, err = projects.ReadDeclarationMigrationProjectTx(ctx, tx, declarationRequest(p), project.ProjectID, out.NodeID)
	if !out.Project.Authority.CanRead {
		return out, fail(pc.DeclarationUnauthorized, "current_project_read_access_required")
	}
	if err != nil {
		if out.Project.Registration.ProjectRoot == "" {
			return out, fail(pc.DeclarationTargetUnavailable, "registered_project_location_required")
		}
		out.Errors = append(out.Errors, "projects")
	}
	out.Roots, err = watchedroots.ReadDeclarationMigrationWatchedrootsTx(ctx, tx, project.ProjectID, out.NodeID)
	if err != nil {
		out.Errors = append(out.Errors, "watchedroots")
	}
	out.Box, err = box.ReadDeclarationMigrationBoxTx(ctx, tx, project.ProjectID, out.NodeID)
	if err != nil {
		out.Errors = append(out.Errors, "box")
	}
	out.Services, err = capabilities.ReadDeclarationMigrationCapabilitiesTx(ctx, tx, project.ProjectID, out.NodeID)
	if err != nil {
		out.Errors = append(out.Errors, "services")
	}
	out.Notes, err = migrationread.ReadDeclarationMigrationKnowledgeTx(ctx, tx, project.ProjectID, out.NodeID)
	if err != nil {
		out.Errors = append(out.Errors, "notes")
	}
	if err = tx.Commit(); err != nil {
		return out, fail(pc.DeclarationTargetUnavailable, "owner_snapshot_unavailable")
	}
	return out, nil
}

func migrationLocation(o migrationObservation, cfg config.Config) (pc.DeclarationTarget, error) {
	var target pc.DeclarationTarget
	registered := o.Project.Registration.ProjectRoot
	if cfg.BoxPath == "" || !filepath.IsAbs(registered) || registered != filepath.Clean(registered) {
		return target, fail(pc.DeclarationTargetUnavailable, "registered_project_location_required")
	}
	trusted, e := filepath.EvalSymlinks(cfg.BoxPath)
	if e != nil {
		return target, fail(pc.DeclarationTargetUnavailable, "configured_box_unavailable")
	}
	physical, e := filepath.EvalSymlinks(registered)
	if e != nil {
		return target, fail(pc.DeclarationTargetUnavailable, "project_source_unavailable")
	}
	rel, e := filepath.Rel(trusted, physical)
	if e != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return target, fail(pc.DeclarationUnauthorized, "source_outside_configured_box")
	}
	raw, _ := canonicalValue(map[string]string{"node_id": o.NodeID, "box": trusted, "project_root": registered, "physical_root": physical})
	return pc.DeclarationTarget{ProjectID: o.Project.Project.ProjectID, OwnerNodeID: o.NodeID, ProjectRoot: physical, LocationRevision: hashBytes(raw)}, nil
}

func migrationObservationDigest(o migrationObservation) string {
	raw, _ := canonicalValue(o)
	return hashBytes(raw)
}
func migrationValueDigest(v any) string { raw, _ := canonicalValue(v); return hashBytes(raw) }

func buildMigrationAssessment(o migrationObservation, sources *pc.DeclarationMigrationSources, target pc.DeclarationTarget, external migrationExternalBindings) (pc.DeclarationMigrationAssessment, pc.DeclarationMigrationInput) {
	in := sources.Input()
	snapshot := migrationValueDigest(struct{ Owners, Source, Location, External string }{migrationObservationDigest(o), sources.Fingerprint(), target.LocationRevision, external.Fingerprint()})
	basis := pc.MigrationFactBasis{SchemaVersion: pc.DeclarationMigrationFactsSchema, Completeness: "complete", ProjectID: target.ProjectID, OwnerNode: o.NodeKey, LocationRevision: target.LocationRevision, RegistryRevision: o.Project.State.RegistryRevision, LifecycleRevision: o.Project.State.LifecycleRevision, SnapshotRevision: snapshot, SourceFingerprint: sources.Fingerprint()}
	in.Project = pc.MigrationProjectFacts{Basis: basis, LocationRef: target.LocationRevision}
	in.Membership = pc.MigrationMembershipFacts{Basis: basis, Members: []pc.MigrationMember{}}
	in.Watches = pc.MigrationWatchFacts{Basis: basis, Roots: []pc.MigrationWatch{}}
	in.Coverage = pc.MigrationCoverageFacts{Basis: basis, Coverage: []pc.MigrationCoverage{}}
	in.Legacy = pc.MigrationLegacyFacts{Basis: basis, Facts: []pc.MigrationLegacyFact{}}
	in.Evidence = pc.MigrationEvidenceFacts{Basis: basis, Evidence: []pc.MigrationEvidence{}}
	a := pc.DeclarationMigrationAssessment{SchemaVersion: pc.DeclarationMigrationAssessmentSchema, ProjectID: target.ProjectID, OwnerNode: o.NodeKey, LocationRevision: target.LocationRevision, SnapshotRevision: snapshot, RegistryRevision: o.Project.State.RegistryRevision, LifecycleRevision: o.Project.State.LifecycleRevision, State: "refused", Consistency: "observed_reads_not_publication_fence", Families: []pc.MigrationAssessmentFamily{}, Issues: sources.Issues()}
	family := map[string]*pc.MigrationFactBasis{"project": &in.Project.Basis, "membership": &in.Membership.Basis, "watches": &in.Watches.Basis, "coverage": &in.Coverage.Basis, "legacy": &in.Legacy.Basis, "evidence": &in.Evidence.Basis}
	incomplete := func(name, code string) {
		family[name].Completeness = "unavailable"
		if len(a.Issues) < 64 {
			a.Issues = append(a.Issues, pc.MigrationAssessmentIssue{Family: name, Code: code})
		}
	}
	for _, name := range o.Errors {
		switch name {
		case "projects":
			for k := range family {
				incomplete(k, "project_owner_unavailable")
			}
		case "watchedroots", "box":
			incomplete("coverage", "coverage_owner_unavailable")
		case "services", "notes":
			incomplete("legacy", "legacy_owner_unavailable")
		}
	}
	for _, issue := range sources.Issues() {
		if issue.Code != "" {
			incomplete("project", "source_closure_incomplete")
			break
		}
	}
	// The stored contract and registry supply existing identity; current source is
	// not allowed to repair or replace those facts.
	if o.Project.Registration.RegistrationStatus != "registered" {
		incomplete("project", "registration_not_current")
	}
	var registered pc.ProjectContract
	decodeErr := migrationDecode(o.Project.Registration.Contract, &registered)
	if sources.AlreadyDeclaration() {
		var declaration pc.ProjectDeclaration
		decodeErr = migrationDecode(o.Project.Registration.Contract, &declaration)
		registered = pc.ProjectContract{SchemaVersion: declaration.SchemaVersion, Project: declaration.Project}
	}
	if decodeErr != nil {
		incomplete("project", "registered_source_invalid")
	} else {
		registered = pc.NormalizeContract(registered)
		in.Project.Project = registered.Project
		if in.Project.Project.ID == "" && registered.SchemaVersion == pc.ProjectSchemaV03 {
			in.Project.Project.ID = target.ProjectID
		}
		p := in.Project.Project
		status := p.Status
		if status != "paused" && status != "archived" {
			status = "active"
		}
		if p.ID != target.ProjectID || p.Name != o.Project.Project.Name || p.Slug != o.Project.Project.Slug || p.Description != o.Project.Project.Description || status != o.Project.Project.Status || p.OwnerNode != o.NodeKey {
			incomplete("project", "registered_identity_conflict")
		}
	}
	rootRef, e := filepath.Rel(o.Project.Registration.ProjectRoot, o.Project.Registration.ContractPath)
	rootRef = filepath.ToSlash(rootRef)
	if e != nil || (rootRef != pc.CanonicalRootContractPath && rootRef != pc.LegacyRootContractPath) {
		incomplete("project", "registered_source_binding_invalid")
	}
	byRef := map[string]pc.MigrationSource{}
	for _, source := range in.Sources {
		byRef[source.Ref] = source
	}
	root := byRef[rootRef]
	in.Project.Source = root.MigrationSourceClaim
	if root.State != "present" || root.Digest != o.Project.Registration.ContractHash || root.SchemaVersion != o.Project.Registration.ContractSchemaVersion {
		incomplete("project", "registered_source_changed")
	}
	// Canonical wins for equal dual roots. Preserve the actual registered claim
	// only after unchanged D4a confirms semantic equality; both byte claims remain.
	_, selected, selectedOK := sources.ProjectSource()
	if selectedOK && selected.Ref != root.Ref {
		if root.State == "present" && selected.SchemaVersion == root.SchemaVersion {
			in.Project.Source = selected
		} else {
			incomplete("project", "registered_source_selection_changed")
		}
	}
	if r := o.Project.Repository; r != nil {
		rel, err := filepath.Rel(r.ProjectRoot, r.ReposContractPath)
		ref := filepath.ToSlash(rel)
		source := byRef[ref]
		if err != nil || r.ProjectRoot != o.Project.Registration.ProjectRoot || r.ProjectContractDigest != root.Digest || r.ReposContractDigest != source.Digest || source.State != "present" {
			incomplete("membership", "repository_source_changed")
		}
		for _, m := range o.Project.Members {
			location := m.Path
			if r.ProjectContractSchemaVersion != pc.ProjectSchemaV05 {
				location = "repos/" + m.Path
			}
			in.Membership.Members = append(in.Membership.Members, pc.MigrationMember{ID: m.RepositoryID, Key: m.Key, Path: location, Role: string(m.Role), StateRoot: m.StateRoot, OwnerProjectID: m.RepositoryOwnerProjectID, Lifecycle: string(m.MembershipLifecycle), Revision: migrationValueDigest(m), Source: source.MigrationSourceClaim})
		}
	}
	for _, f := range o.Project.Facets {
		state := "disabled"
		if f.Enabled {
			state = "enabled"
		}
		in.Legacy.Facts = append(in.Legacy.Facts, pc.MigrationLegacyFact{Kind: "facet", Key: f.FacetKey, State: state, Revision: migrationValueDigest(f), Source: in.Project.Source})
	}
	if len(o.Services.Providers) > 0 {
		incomplete("legacy", "service_behavior_unmapped")
	}
	if len(o.Notes.Roots) > 0 {
		incomplete("legacy", "notes_behavior_unmapped")
	}
	if o.Project.Unsupported.Count() > 0 {
		incomplete("legacy", "registered_behavior_unmapped")
	}
	if o.Project.Project.Status == "archived" || string(o.Project.Project.ArchiveState) != "{}" && string(o.Project.Project.ArchiveState) != "null" && len(o.Project.Project.ArchiveState) != 0 {
		incomplete("evidence", "archive_evidence_owner_required")
	}
	recorded := map[string]watchedroots.WatchedRoot{}
	for _, r := range o.Roots.Roots {
		recorded[r.RootKey] = r
	}
	owned := map[string]bool{}
	for _, w := range o.Project.Watches {
		cfg, ok := migrationRootConfig(w.ConfigJSON, w.ConfigHash)
		if !ok || w.NodeID != o.NodeID || w.OwnerNodeKey != o.NodeKey || w.ProjectContractRegistrationID != o.Project.Registration.ProjectContractRegistrationID || cfg.RootKey != w.BackendRootKey || cfg.SafeRootKey != w.SafeRootKey || cfg.RootRelativePath != w.RootRelativePath {
			incomplete("watches", "watch_config_binding_invalid")
			continue
		}
		kinds := []string{}
		if migrationDecode(w.SourceKinds, &kinds) != nil {
			incomplete("watches", "watch_source_kinds_invalid")
			continue
		}
		intent := pc.MigrationRootIntent{Key: w.LocalRootKey, BackendRootKey: w.BackendRootKey, WorkerKey: w.WorkerKey, OwnerNode: w.OwnerNodeKey, SourceKinds: kinds, Config: cfg, ConfigHash: w.ConfigHash}
		revision := migrationValueDigest(w)
		owner := "project:" + target.ProjectID
		in.Watches.Roots = append(in.Watches.Roots, pc.MigrationWatch{Owner: owner, Revision: revision, Root: intent})
		owned[w.BackendRootKey] = true
		if w.ActivationStatus != "applied" && w.ActivationStatus != "reported" {
			incomplete("watches", "watch_generation_unconfirmed")
		}
		if cfg.BackupPolicy.Mode != aw.BackupModeNone {
			observed, exists := recorded[w.BackendRootKey]
			rc, valid := migrationRootConfig(observed.ConfigJSON, observed.ConfigHash)
			if !exists || !valid || !reflect.DeepEqual(rc, cfg) || observed.NodeID != o.NodeID || observed.SafeRootKey != cfg.SafeRootKey || observed.WorkerKey != w.WorkerKey || w.WatchedRootID == nil || *w.WatchedRootID != observed.WatchedRootID || w.LastAppliedAt == nil || observed.LastReportedAt.Before(*w.LastAppliedAt) {
				incomplete("coverage", "coverage_generation_unconfirmed")
			} else {
				in.Coverage.Coverage = append(in.Coverage.Coverage, pc.MigrationCoverage{Owner: owner, Revision: revision, Root: intent})
			}
		}
	}
	for _, state := range o.Project.WatchStates {
		if state.Pending || state.Queued && !state.Applied {
			incomplete("watches", "declaration_watch_pending")
		} else if !state.Retire {
			incomplete("watches", "declaration_watch_source_unmapped")
		}
	}
	// A node-wide contributor with no known physical binding cannot be discarded
	// by a project filter. Disjoint Box roots are proven privately by metadata;
	// overlapping or unbound contributors remain unavailable, never re-parented.
	for _, b := range o.Box.Registrations {
		owned[b.BackendRootKey] = true
		if external[b.BoxWatchRootRegistrationID] == nil {
			incomplete("coverage", "external_coverage_binding_unavailable")
		}
	}
	for _, r := range o.Roots.Roots {
		if !owned[r.RootKey] {
			incomplete("coverage", "external_coverage_binding_unavailable")
		}
	}
	count := o.Project.FactCount + len(o.Roots.Roots) + len(o.Box.Registrations) + len(o.Services.Providers) + len(o.Notes.Roots) + len(o.Notes.Custody)
	if count > 4096 {
		for name := range family {
			incomplete(name, "fact_bound_exceeded")
		}
	}
	for _, name := range []string{"project", "membership", "watches", "coverage", "legacy", "evidence"} {
		a.Families = append(a.Families, pc.MigrationAssessmentFamily{Family: name, Completeness: family[name].Completeness, Revision: migrationFamilyDigest(name, o, sources.Fingerprint(), external.Fingerprint()), RevisionKind: "observed_content_digest"})
	}
	a.Preview = pc.PreviewDeclarationMigration(in)
	if a.Preview.Candidate != nil {
		a.State = "eligible"
	}
	if sources.AlreadyDeclaration() {
		a.State = "already_declaration"
	}
	for _, f := range a.Families {
		if f.Completeness != "complete" {
			a.State = "incomplete"
		}
	}
	if a.State != "eligible" {
		a.Preview.Candidate = nil
		a.Preview.Disposition = "refused"
	}
	unknown := pc.DeclarationFact{State: pc.DeclarationUnknown, Revision: snapshot}
	a.Readiness = pc.DeclarationReadiness{Desired: unknown, Queued: unknown, Applied: unknown, Processing: unknown, Healthy: unknown, Protected: unknown, Verified: unknown}
	sort.Slice(a.Issues, func(i, j int) bool {
		return a.Issues[i].Family+":"+a.Issues[i].Code+":"+a.Issues[i].Ref < a.Issues[j].Family+":"+a.Issues[j].Code+":"+a.Issues[j].Ref
	})
	return a, in
}

func migrationDecode(raw []byte, out any) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(out); err != nil {
		return err
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return errors.New("trailing_json")
	}
	return nil
}
func migrationRootConfig(raw []byte, hash string) (aw.RootConfig, bool) {
	var c aw.RootConfig
	if len(raw) > 4<<20 || migrationDecode(raw, &c) != nil {
		return c, false
	}
	normalized, err := aw.ValidatePortableRootConfig(c)
	return c, err == nil && reflect.DeepEqual(c, normalized) && aw.ConfigHash(c) == hash
}
func migrationBoxConfigBound(b box.WatchRootRegistration, reported watchedroots.WatchedRoot, nodeID, nodeKey string) bool {
	c, ok := migrationRootConfig(b.ConfigJSON, b.ConfigHash)
	rc, valid := migrationRootConfig(reported.ConfigJSON, reported.ConfigHash)
	if !ok || !valid || !reflect.DeepEqual(c, rc) || b.NodeID != nodeID || b.OwnerNodeKey != nodeKey || reported.NodeID != nodeID || c.RootKey != b.BackendRootKey || c.SafeRootKey != b.SafeRootKey || c.RootRelativePath != b.RootRelativePath || b.WorkerKey != reported.WorkerKey || b.WatchedRootID == nil || *b.WatchedRootID != reported.WatchedRootID || b.LastAppliedAt == nil || reported.LastReportedAt.Before(*b.LastAppliedAt) {
		return false
	}
	if b.DesiredRevision < 1 || b.AppliedConfigHash != b.ConfigHash || b.SourceContractDeletedAt != nil || b.LastApplyErrorCode != "" || b.LastApplyErrorMessage != "" || (b.ActivationStatus != "applied" && b.ActivationStatus != "reported") {
		return false
	}
	if b.LastNodeAckStatus != "" && b.LastNodeAckStatus != "applied" {
		return false
	}

	if b.SafeRootKey != "box" || !filepath.IsAbs(b.BoxRootPath) || b.RootRelativePath == "" || filepath.IsAbs(b.RootRelativePath) || (filepath.ToSlash(filepath.Clean(b.RootRelativePath)) != b.RootRelativePath || b.RootRelativePath == ".." || strings.HasPrefix(b.RootRelativePath, "../")) {
		return false
	}
	if b.DesiredRevision != b.AppliedRevision || b.DesiredConfigHash != b.AppliedConfigHash || b.ActivationStatus == "pending_agent_apply" {
		return false
	}
	return true
}

func migrationFamilyDigest(name string, o migrationObservation, source, external string) string {
	switch name {
	case "project", "membership", "evidence":
		return migrationValueDigest(struct{ Project, Source string }{o.Project.Revision, source})
	case "watches":
		return migrationValueDigest(struct{ Project, Roots string }{o.Project.Revision, o.Roots.Revision})
	case "coverage":
		return migrationValueDigest(struct{ Project, Roots, Box, External string }{o.Project.Revision, o.Roots.Revision, o.Box.Revision, external})
	case "legacy":
		return migrationValueDigest(struct{ Project, Services, Notes string }{o.Project.Revision, o.Services.Revision, o.Notes.Revision})
	}
	return ""
}

// Only metadata of roots actually used to exclude external coverage is held.
// These paths/identities stay private; no directory contents are enumerated.
type migrationPhysicalBinding struct {
	ClaimedBox, ConfiguredBox, Relative, Anchor, Root string
	anchorFile, rootFile                              *os.File
	anchorInfo, rootInfo                              os.FileInfo
}
type migrationExternalBindings map[string]*migrationPhysicalBinding

func migrationCaptureExternal(o migrationObservation, configuredBox, projectRoot string) migrationExternalBindings {
	out := migrationExternalBindings{}
	recorded := map[string]watchedroots.WatchedRoot{}
	for _, r := range o.Roots.Roots {
		recorded[r.RootKey] = r
	}
	for _, b := range o.Box.Registrations {
		out[b.BoxWatchRootRegistrationID] = nil
		if !migrationBoxConfigBound(b, recorded[b.BackendRootKey], o.NodeID, o.NodeKey) {
			continue
		}
		v := &migrationPhysicalBinding{ClaimedBox: b.BoxRootPath, ConfiguredBox: configuredBox, Relative: b.RootRelativePath}
		anchor, root, e := v.resolve()
		if e != nil || migrationOverlaps(root, projectRoot) || migrationOverlaps(projectRoot, root) {
			continue
		}
		v.Anchor, v.Root = anchor, root
		v.anchorFile, e = migrationOpenDirectory(anchor)
		if e != nil {
			continue
		}
		v.rootFile, e = migrationOpenDirectory(root)
		if e != nil {
			v.anchorFile.Close()
			continue
		}
		v.anchorInfo, e = v.anchorFile.Stat()
		if e != nil {
			v.Close()
			continue
		}
		v.rootInfo, e = v.rootFile.Stat()
		if e != nil || !v.anchorInfo.IsDir() || !v.rootInfo.IsDir() {
			v.Close()
			continue
		}
		_, anchorOK := v.anchorInfo.Sys().(*syscall.Stat_t)
		_, rootOK := v.rootInfo.Sys().(*syscall.Stat_t)
		if !anchorOK || !rootOK || !v.Check() {
			v.Close()
			continue
		}
		out[b.BoxWatchRootRegistrationID] = v
	}
	return out
}
func migrationOverlaps(a, b string) bool {
	rel, e := filepath.Rel(a, b)
	return e == nil && (rel == "." || rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}
func (v *migrationPhysicalBinding) resolve() (string, string, error) {
	anchor, e := filepath.EvalSymlinks(v.ConfiguredBox)
	if e != nil {
		return "", "", e
	}
	claimed, e := filepath.EvalSymlinks(v.ClaimedBox)
	if e != nil || claimed != anchor {
		return "", "", errors.New("external_anchor_unavailable")
	}
	root, e := filepath.EvalSymlinks(filepath.Join(anchor, filepath.FromSlash(v.Relative)))
	if e != nil || !migrationOverlaps(anchor, root) {
		return "", "", errors.New("external_root_unavailable")
	}
	return anchor, root, nil
}
func (v *migrationPhysicalBinding) Check() bool {
	anchor, root, e := v.resolve()
	if e != nil || anchor != v.Anchor || root != v.Root {
		return false
	}
	a, e := os.Stat(anchor)
	if e != nil || !os.SameFile(a, v.anchorInfo) || a.Mode() != v.anchorInfo.Mode() {
		return false
	}
	r, e := os.Stat(root)
	return e == nil && os.SameFile(r, v.rootInfo) && r.Mode() == v.rootInfo.Mode()
}
func (v *migrationPhysicalBinding) Close() {
	if v.anchorFile != nil {
		v.anchorFile.Close()
	}
	if v.rootFile != nil {
		v.rootFile.Close()
	}
}
func (b migrationExternalBindings) Close() {
	for _, v := range b {
		if v != nil {
			v.Close()
		}
	}
}
func (b migrationExternalBindings) Check() bool {
	for _, v := range b {
		if v != nil && !v.Check() {
			return false
		}
	}
	return true
}
func (b migrationExternalBindings) Fingerprint() string {
	identities := map[string]string{}
	for key, v := range b {
		if v == nil {
			identities[key] = "unavailable"
			continue
		}
		a, aok := v.anchorInfo.Sys().(*syscall.Stat_t)
		r, rok := v.rootInfo.Sys().(*syscall.Stat_t)
		if !aok || !rok {
			identities[key] = "unavailable"
			continue
		}
		identities[key] = migrationValueDigest(struct {
			ClaimedBox, ConfiguredBox, Relative, Anchor, Root string
			AnchorDevice, AnchorInode, RootDevice, RootInode  uint64
			AnchorMode, RootMode                              uint32
		}{v.ClaimedBox, v.ConfiguredBox, v.Relative, v.Anchor, v.Root, uint64(a.Dev), uint64(a.Ino), uint64(r.Dev), uint64(r.Ino), uint32(v.anchorInfo.Mode()), uint32(v.rootInfo.Mode())})
	}
	return migrationValueDigest(identities)
}

// A raced FIFO/device or final symlink cannot block or become a descriptor used
// as directory evidence. Identity and resolution are still checked afterwards.
func migrationOpenDirectory(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, 0)
}
