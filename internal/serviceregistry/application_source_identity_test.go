package serviceregistry

import (
	"encoding/json"
	"testing"
)

func TestApplicationSourcePolicyPreservesExistingPreparedGrant(t *testing.T) {
	_, policy, request := applicationProvisionFixture(t)
	before, err := policy.deriveGrant(request)
	if err != nil {
		t.Fatal(err)
	}
	policy.Provisioning.ProjectRoot = "/srv/loom/box/Projects"
	policy.Provisioning.RuntimeCredentialRoot = "/var/lib/loom-project-application-credentials"
	policy.Provisioning.RuntimeCredentialImportRoots = []string{"/private/oauth"}
	policy.Provisioning.Network = &ApplicationNetworkPolicy{Profiles: []ApplicationNetworkProfile{ApplicationNetworkLoopback, ApplicationNetworkOutboundHTTPS}, Resolvers: []string{"10.82.0.1"}}
	after, err := policy.deriveGrant(request)
	if err != nil || before.Revision != after.Revision || applicationSHA(before.Grant) != applicationSHA(after.Grant) {
		t.Fatalf("legacy grant drifted: %s / %s / %v", before.Revision, after.Revision, err)
	}
	request.RepositoryID, request.SourceIdentity = "", request.Owner.ProjectID+"/"+request.Owner.Resource
	request.Network = ApplicationNetworkOutboundHTTPS
	source, err := policy.deriveGrant(request)
	if err != nil || source.Grant.RepositoryID != "" || source.Grant.NetworkPolicy == nil || source.Grant.Network != ApplicationNetworkOutboundHTTPS {
		t.Fatalf("source grant: %+v %v", source.Grant, err)
	}
}

func TestApplicationArtifactV2KeepsActualProjectIdentity(t *testing.T) {
	d := applicationTestDescriptor()
	legacy, _ := json.Marshal(d)
	d.SchemaVersion, d.RepositoryID, d.SourceIdentity = "application.artifact.v2", "", "project_01ARZ3NDEKTSV4RRFFQ69G5FAV/intake"
	if err := d.Validate(); err != nil {
		t.Fatal(err)
	}
	d.RepositoryID = applicationTestDescriptor().RepositoryID
	if err := d.Validate(); err == nil {
		t.Fatal("ambiguous repository/project identity accepted")
	}
	var restored ApplicationArtifactDescriptor
	if json.Unmarshal(legacy, &restored) != nil || restored.Validate() != nil {
		t.Fatal("v1 artifact no longer readable")
	}
	roundtrip, _ := json.Marshal(restored)
	if string(roundtrip) != string(legacy) {
		t.Fatal("legacy artifact bytes changed")
	}
}
