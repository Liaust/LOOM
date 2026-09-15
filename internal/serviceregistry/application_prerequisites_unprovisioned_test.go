package serviceregistry

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"testing"
)

func TestApplicationPrerequisitesWithoutGrant(t *testing.T) {
	r, q, policy := applicationPrerequisitesFixture(t)
	configured := policy
	policy.Grants = nil
	applicationPrerequisitesWrite(t, r.PolicyPath, policy)
	before := applicationPrerequisitesTree(t, filepath.Dir(r.PolicyPath))
	s, err := r.QueryPrerequisites(t.Context(), policy.InstallerUID, q)
	if err != nil || !s.GrantMissing || s.RepositoryID != "" || s.Publication.Present || s.Installation.Present || s.IdentityRevision == "" {
		t.Fatalf("missing grant was not a scoped fact: %+v %v", s, err)
	}
	raw, _ := json.Marshal(s)
	if decoded, err := decodeApplicationPrerequisiteSnapshot(raw, q); err != nil || !reflect.DeepEqual(decoded, s) {
		t.Fatalf("wire report: %+v %v", decoded, err)
	}
	if !reflect.DeepEqual(before, applicationPrerequisitesTree(t, filepath.Dir(r.PolicyPath))) {
		t.Fatal("read created or changed runtime state")
	}
	if _, err := policy.grant(q.Owner); err == nil {
		t.Fatal("query supplied execution authority")
	}
	applicationPrerequisitesWrite(t, r.PolicyPath, configured)
	next, err := r.QueryPrerequisites(t.Context(), policy.InstallerUID, q)
	if err != nil || next.GrantMissing || next.IdentityRevision == s.IdentityRevision {
		t.Fatalf("grant creation not observed: %+v %v", next, err)
	}
}

func TestApplicationPrerequisitesRemovedGrantPreservesHistory(t *testing.T) {
	r, q, p := applicationPrerequisitesFixture(t)
	d := applicationTestDescriptor()
	publication := ApplicationPublication{Owner: q.Owner, Revision: "pub-a", PolicyRevision: "policy-a", LocationRevision: "location-a", GrantDigest: applicationSHA(p.Grants[0]), Descriptor: d}
	installation := ApplicationInstallation{Owner: q.Owner, Revision: "install-a", Descriptor: d, Committed: true, Applied: true}
	applicationPrerequisitesWrite(t, filepath.Join(r.Store.Root, "publication-"+q.Owner.Instance()+".json"), publication)
	applicationPrerequisitesWrite(t, filepath.Join(r.Store.Root, "installation-"+q.Owner.Instance()+".json"), installation)
	p.Grants = nil
	applicationPrerequisitesWrite(t, r.PolicyPath, p)
	s, err := r.QueryPrerequisites(t.Context(), p.InstallerUID, q)
	if err != nil || !s.GrantMissing || !s.Publication.Present || !s.Installation.Present || s.Publication.GrantMatches == nil || *s.Publication.GrantMatches {
		t.Fatalf("grant removal erased retained history or authorized it: %+v %v", s, err)
	}
	raw, _ := json.Marshal(s)
	if _, err := decodeApplicationPrerequisiteSnapshot(raw, q); err != nil {
		t.Fatal(err)
	}
	for _, edit := range []func(*ApplicationPrerequisiteSnapshot){
		func(s *ApplicationPrerequisiteSnapshot) { s.GrantMissing = false },
		func(s *ApplicationPrerequisiteSnapshot) { s.RepositoryID = d.RepositoryID },
		func(s *ApplicationPrerequisiteSnapshot) { yes := true; s.Publication.GrantMatches = &yes },
		func(s *ApplicationPrerequisiteSnapshot) { s.Data["invented"] = ApplicationPrerequisiteData{} },
	} {
		var altered ApplicationPrerequisiteSnapshot
		_ = json.Unmarshal(raw, &altered)
		edit(&altered)
		b, _ := json.Marshal(altered)
		if _, err := decodeApplicationPrerequisiteSnapshot(b, q); err == nil {
			t.Fatal("inconsistent missing-grant report accepted")
		}
	}
}

func TestApplicationPrerequisitesMalformedGrantIsNotAbsent(t *testing.T) {
	for _, edit := range []func(*ApplicationHostPolicy){
		func(p *ApplicationHostPolicy) { p.Grants[0].MaxMemoryBytes = 0 },
		func(p *ApplicationHostPolicy) { p.Grants[0].RepositoryID = "invalid" },
		func(p *ApplicationHostPolicy) { p.Grants = append(p.Grants, p.Grants[0]) },
		func(p *ApplicationHostPolicy) { p.Platform = "invalid"; p.Grants = nil },
	} {
		r, q, p := applicationPrerequisitesFixture(t)
		edit(&p)
		applicationPrerequisitesWrite(t, r.PolicyPath, p)
		if s, err := r.QueryPrerequisites(t.Context(), p.InstallerUID, q); err == nil || s.GrantMissing {
			t.Fatalf("invalid policy treated as empty: %+v %v", s, err)
		}
	}
}
