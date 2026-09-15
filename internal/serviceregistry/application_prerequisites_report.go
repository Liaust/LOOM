package serviceregistry

import (
	"bytes"
	"context"
	"encoding/json"
	"loom.local/loom/internal/routing"
	"reflect"
	"slices"
)

func applicationReportError(reason string) error { return applicationError("prerequisites." + reason) }

// DecodeApplicationPrerequisiteQuery validates the existing E3a wire type.
func DecodeApplicationPrerequisiteQuery(raw []byte) (ApplicationPrerequisiteQuery, error) {
	var q ApplicationPrerequisiteQuery
	if decodeClosedPrerequisite(raw, &q) != nil || q.SchemaVersion != ApplicationPrerequisiteSchema ||
		!q.Owner.valid() {
		return q, applicationReportError("query_invalid")
	}
	return q, nil
}

// Strictly preserve the E3a shape, including required false/zero fields. The
// round trip also rejects case-insensitive Go field aliases and explicit null
// for optional fields which the helper omits. Numbers compare losslessly.
func decodeClosedPrerequisite(raw []byte, out any) error {
	if err := applicationDecodeJSON(raw, out); err != nil {
		return err
	}
	canonical, err := json.Marshal(out)
	if err != nil {
		return err
	}
	decode := func(b []byte) any {
		d := json.NewDecoder(bytes.NewReader(b))
		d.UseNumber()
		var v any
		_ = d.Decode(&v)
		return v
	}
	if !reflect.DeepEqual(decode(raw), decode(canonical)) {
		return applicationReportError("schema")
	}
	return nil
}

func decodeApplicationPrerequisiteSnapshot(raw []byte, q ApplicationPrerequisiteQuery) (ApplicationPrerequisiteSnapshot, error) {
	var s ApplicationPrerequisiteSnapshot
	if decodeClosedPrerequisite(raw, &s) != nil || s.SchemaVersion != q.SchemaVersion || s.Owner != q.Owner ||
		!applicationDigestPattern.MatchString(s.IdentityRevision) || s.CollectedAt.IsZero() ||
		(s.Platform != "x86_64-linux" && s.Platform != "aarch64-linux") || s.Data == nil || s.Credentials == nil || len(s.Data) > 16 || len(s.Credentials) > 16 {
		return s, applicationReportError("report_invalid")
	}
	if s.GrantMissing {
		if s.PolicyRevision != "" || s.LocationRevision != "" || s.RepositoryID != "" || len(s.Data) != 0 || len(s.Credentials) != 0 || s.ApprovedEdgeRef != nil || (s.Publication.GrantMatches != nil && *s.Publication.GrantMatches) {
			return s, applicationReportError("report_invalid")
		}
	} else if !applicationRevisionPattern.MatchString(s.PolicyRevision) || !applicationRevisionPattern.MatchString(s.LocationRevision) || !applicationRepoPattern.MatchString(s.RepositoryID) {
		return s, applicationReportError("report_invalid")
	}
	if s.Publication.Present {
		p := s.Publication
		if !applicationRevisionPattern.MatchString(p.Revision) || !applicationRevisionPattern.MatchString(p.PolicyRevision) || !applicationRevisionPattern.MatchString(p.LocationRevision) || p.GrantMatches == nil || p.Artifact == nil {
			return s, applicationReportError("publication_invalid")
		}
		if *p.GrantMatches && (p.Artifact.RepositoryID != s.RepositoryID || p.Artifact.Artifact.Platform != s.Platform || p.PolicyRevision != s.PolicyRevision || p.LocationRevision != s.LocationRevision) {
			return s, applicationReportError("repository_mismatch")
		}
	} else if s.Publication != (ApplicationPrerequisitePublication{}) {
		return s, applicationReportError("publication_invalid")
	}
	if s.Installation.Present {
		p := s.Installation
		if !applicationRevisionPattern.MatchString(p.Revision) || (p.Generation != "" && !applicationRevisionPattern.MatchString(p.Generation)) || p.Committed == nil || p.Applied == nil || p.Retired == nil || p.Fenced == nil {
			return s, applicationReportError("installation_invalid")
		}
	} else if s.Installation != (ApplicationPrerequisiteInstallation{}) {
		return s, applicationReportError("installation_invalid")
	}
	for _, a := range []*ApplicationPrerequisiteArtifact{s.Publication.Artifact, s.Installation.Artifact} {
		if a != nil && (!applicationRepoPattern.MatchString(a.RepositoryID) || !applicationDigestPattern.MatchString(a.DescriptorDigest) || !applicationDigestPattern.MatchString(a.SourceDigest) || !applicationDigestPattern.MatchString(a.ConfigurationDigest) || !applicationDigestPattern.MatchString(a.Artifact.Digest) || !applicationRef(a.Artifact.Ref) || !applicationRef(a.ConfigurationSchema) || !applicationSourceRevisionPattern.MatchString(a.SourceRevision) || !applicationRevisionPattern.MatchString(a.DataFormat) || (a.Artifact.Platform != "x86_64-linux" && a.Artifact.Platform != "aarch64-linux")) {
			return s, applicationReportError("artifact_invalid")
		}
	}
	available := func(v string) bool { return v == "available" || v == "missing" || v == "unavailable" }
	for alias, data := range s.Data {
		if !applicationKeyPattern.MatchString(alias) || !available(data.Availability) || !applicationPrerequisiteBindingRef(data.BindingRef) {
			return s, applicationReportError("data_invalid")
		}
		switch data.Custody {
		case "unknown", "unclaimed", "matches", "conflict":
		default:
			return s, applicationReportError("data_invalid")
		}
		if data.Availability == "available" {
			if data.Identity == nil || !applicationDigestPattern.MatchString(data.PoolIdentity) || data.Identity.Mode > 07777 || data.Custody == "unknown" {
				return s, applicationReportError("data_invalid")
			}
		} else if data.Identity != nil || data.PoolIdentity != "" || data.Custody != "unknown" {
			return s, applicationReportError("data_invalid")
		}
	}
	for ref, credential := range s.Credentials {
		if !applicationRef(ref) || !applicationRevisionPattern.MatchString(credential.Revision) || !available(credential.Availability) {
			return s, applicationReportError("credential_invalid")
		}
	}
	if s.ApprovedEdgeRef != nil && (!applicationDigestPattern.MatchString(*s.ApprovedEdgeRef)) {
		return s, applicationReportError("edge_invalid")
	}
	if o := s.Observation; o != nil {
		if o.ObservedAt.IsZero() {
			return s, applicationReportError("observation_invalid")
		}
		for _, v := range []struct {
			value string
			known []string
		}{
			{o.State, []string{"unknown", "observed", "failed", "skipped"}},
			{o.ProcessState, []string{"unknown", "active", "inactive", "failed", "activating", "deactivating", "reloading", "maintenance", "refreshing"}},
			{o.Readiness.Process, []string{"unknown", "satisfied", "pending", "stopped"}},
			{o.Readiness.Protocol, []string{"unknown", "satisfied", "pending", "not_applicable"}},
			{o.Readiness.Ingress, []string{"unknown", "configured", "pending", "not_applicable"}},
			{o.Readiness.Client, []string{"unknown", "satisfied", "pending", "not_applicable"}},
			{o.Readiness.Protection, []string{"unknown", "satisfied", "pending", "not_applicable"}},
		} {
			if !slices.Contains(v.known, v.value) {
				return s, applicationReportError("observation_invalid")
			}
		}
	}

	return s, nil
}

// ApplicationPrerequisiteReportReader projects recorded transport evidence into
// existing E3a facts. Callers own read scope, temporal applicability and future
// execution authority; this method cannot refresh facts or grant permission.
type ApplicationPrerequisiteReportReader struct{ Routing routing.Service }

func (r ApplicationPrerequisiteReportReader) Read(ctx context.Context, callID string, expected ApplicationPrerequisiteQuery, repositoryID, identityRevision string) (ApplicationPrerequisiteSnapshot, routing.ApplicationPrerequisiteEvidence, error) {
	var empty ApplicationPrerequisiteSnapshot
	var noEvidence routing.ApplicationPrerequisiteEvidence
	raw, err := json.Marshal(expected)
	if err != nil {
		return empty, noEvidence, err
	}
	if _, err := DecodeApplicationPrerequisiteQuery(raw); err != nil {
		return empty, noEvidence, err
	}
	body, evidence, err := r.Routing.ReadApplicationPrerequisiteReport(ctx, callID, raw)
	if err != nil {
		return empty, noEvidence, err
	}
	snapshot, err := decodeApplicationPrerequisiteSnapshot(body, expected)
	if err != nil {
		return empty, noEvidence, err
	}
	if snapshot.Owner.NodeID != evidence.NodeID {
		return empty, noEvidence, applicationReportError("owner_node_mismatch")
	}
	if repositoryID != "" && repositoryID != snapshot.RepositoryID {
		return empty, noEvidence, applicationReportError("repository_mismatch")
	}
	if identityRevision != "" && identityRevision != snapshot.IdentityRevision {
		return empty, noEvidence, applicationReportError("stale")
	}
	return snapshot, evidence, nil
}
