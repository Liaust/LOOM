package serviceregistry

import (
	"context"
	"encoding/json"
	"loom.local/loom/internal/projectquiescence"
	"strings"
	"testing"
)

func TestApplicationPublicationSeparatesInstallerAndPublisher(t *testing.T) {
	r, _, q, p := applicationTestRuntime(t)
	ctx := context.Background()
	if e := r.Publish(ctx, 1234, p); e == nil {
		t.Fatal("installer manufactured trust")
	}
	if _, e := r.Execute(ctx, 0, q); e == nil {
		t.Fatal("publisher obtained execution authority")
	}
	p.ExpectedRevision = "wrong"
	p.Revision = "new"
	if r.Publish(ctx, 0, p) == nil {
		t.Fatal("stale publication accepted")
	}
}

func TestApplicationArchiveRevokedInstallerWhileWaiting(t *testing.T) {
	r, h, q, p := applicationTestRuntime(t)
	ctx := context.Background()
	if _, e := r.Execute(ctx, 1234, q); e != nil {
		t.Fatal(e)
	}
	target := projectquiescence.Target{Facet: "service", Kind: projectquiescence.TargetKindService, OwnerNode: "fixture", ProviderID: "provider-fixture", ProviderAddress: "workspace/fixture@app", ProviderKey: "app", RuntimeProfileDigest: "sha256:" + strings.Repeat("3", 64), AllowlistKey: q.Owner.AllowlistKey(), Manager: "systemd", Unit: q.Owner.Unit()}
	p.ExpectedRevision = p.Revision
	p.Revision = "revision-b"
	p.ArchiveTarget = &target
	if e := r.Publish(ctx, 0, p); e != nil {
		t.Fatal(e)
	}
	release, e := r.Store.lock(ctx, q.Owner.Instance())
	if e != nil {
		t.Fatal(e)
	}
	read := make(chan struct{})
	r.FailureHook = func(stage string) error {
		if stage == "policy_read" {
			close(read)
		}
		return nil
	}
	done := make(chan error, 1)
	go func() {
		_, e := r.ArchiveControl(ctx, 1234, ApplicationArchiveControl{Target: target, Operation: OperationStop})
		done <- e
	}()
	<-read
	policy, e := r.policy()
	if e != nil {
		t.Fatal(e)
	}
	policy.InstallerUID = 2345
	raw, _ := json.Marshal(policy)
	if e = applicationAtomic(r.PolicyPath, raw, 0600); e != nil {
		t.Fatal(e)
	}
	release()
	if e = <-done; e == nil || !strings.Contains(e.Error(), "peer_denied") || h.process.State != "active" {
		t.Fatalf("revoked archive caller affected process: %v state=%s", e, h.process.State)
	}
}
