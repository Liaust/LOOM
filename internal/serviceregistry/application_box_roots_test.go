package serviceregistry

import "testing"

func TestApplicationBoxRootsKeepPrimaryGrantRevision(t *testing.T) {
	_, p, q := applicationProvisionFixture(t)
	p.Provisioning.ProjectRoot = "/srv/loom/box/Projects"
	q.ProjectRoot = "/srv/loom/box/Projects/worker"
	before, err := p.deriveGrant(q)
	if err != nil {
		t.Fatal(err)
	}
	p.Provisioning.ProjectRoots = []string{"/srv/loom/boxes/company/Projects"}
	after, err := p.deriveGrant(q)
	if err != nil {
		t.Fatal(err)
	}
	if before.Grant.PolicyRevision != after.Grant.PolicyRevision {
		t.Fatal("new Box invalidated existing grant")
	}
	if parent, ok := p.Provisioning.projectParent("/srv/loom/boxes/company/Projects/worker"); !ok || parent != p.Provisioning.ProjectRoots[0] {
		t.Fatal("company project not admitted")
	}
	for _, path := range []string{"/srv/loom/boxes/company/Projects", "/srv/loom/boxes/company/Projects-other/worker", "/srv/loom/agents/mina"} {
		if _, ok := p.Provisioning.projectParent(path); ok {
			t.Fatalf("foreign root allowed: %s", path)
		}
	}
}
