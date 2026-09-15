package serviceregistry

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

func TestApplicationEdgeRequiresExactPolicyAndDNS(t *testing.T) {
	r, _, q, _ := applicationTestRuntime(t)
	p, _ := r.policy()
	g := p.Grants[0]
	m := *q.Manifest
	m.Listener = &ApplicationListener{Address: "127.0.0.1", Port: 8088}
	g.Edge = &ApplicationEdgePolicy{Owner: q.Owner, Hostname: "fixture.invalid", BackendPort: 8088, Revision: "edge-a", TLS: "managed"}
	if _, e := applicationEdgeFragment(g, m); e == nil {
		t.Fatal("missing DNS accepted")
	}
	g.Edge.DNSReady = true
	b, e := applicationEdgeFragment(g, m)
	if e != nil || !strings.Contains(string(b), "127.0.0.1:8088") {
		t.Fatalf("fragment=%s err=%v", b, e)
	}
	g.Edge.Hostname = "fixture.invalid { import /etc/passwd }"
	if _, e = applicationEdgeFragment(g, m); e == nil {
		t.Fatal("untyped Caddy accepted")
	}
}

type applicationEdgeFixtureHost struct {
	*applicationFixtureHost
	fragment     string
	publications int
}

func (h *applicationEdgeFixtureHost) PublishEdge(_ context.Context, _ ApplicationGeneration, b []byte) error {
	h.fragment = string(b)
	h.publications++
	return nil
}

func TestApplicationStartupPartialResumesWithoutRestart(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(401) }))
	defer server.Close()
	port := server.Listener.Addr().(*net.TCPAddr).Port
	r, host, q, publication := applicationTestRuntime(t)
	edgeHost := &applicationEdgeFixtureHost{applicationFixtureHost: host}
	r.System = edgeHost
	policy, _ := r.policy()
	q.Endpoint = &ApplicationEndpointRequest{Hostname: "fixture.invalid", BackendPort: uint16(port)}
	q.Manifest.Listener = &ApplicationListener{Address: "127.0.0.1", Port: uint16(port)}
	q.Manifest.Health = ApplicationHealth{Kind: HealthKindHTTP, Method: "OPTIONS", Path: "/", ExpectedStatus: 401, TimeoutSeconds: 1}
	policy.Grants[0].Edge = &ApplicationEdgePolicy{Owner: q.Owner, Hostname: q.Endpoint.Hostname, BackendPort: uint16(port), IngressIPv4: "192.0.2.1", Revision: "edge-a", TLS: "managed"}
	applicationPrerequisitesWrite(t, r.PolicyPath, policy)
	publication.ExpectedRevision = publication.Revision
	publication.Revision = "revision-public"
	q.ExpectedRevision = publication.Revision
	if err := r.Publish(t.Context(), policy.PublisherUID, publication); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	r.FailureHook = func(stage string) error {
		if stage == "recorded:applied" {
			cancel()
		}
		return nil
	}
	partial, err := r.Execute(ctx, policy.InstallerUID, q)
	if err == nil || partial.State != "partial" || !partial.Applied || partial.ErrorCode != "application.operation.cancelled" || host.restarts != 1 || edgeHost.publications != 0 {
		t.Fatalf("partial=%+v err=%v restarts=%d edge=%d", partial, err, host.restarts, edgeHost.publications)
	}
	process, reloads := host.process, host.reloads
	r.FailureHook = nil
	complete, err := r.Execute(t.Context(), policy.InstallerUID, q)
	if err != nil || complete.State != "succeeded" || complete.OperationToken != partial.OperationToken || complete.InstallationRevision != partial.InstallationRevision || host.process != process || host.restarts != 1 || host.reloads != reloads || edgeHost.publications != 1 {
		t.Fatalf("resume=%+v err=%v restarts=%d reloads=%d edge=%d", complete, err, host.restarts, host.reloads, edgeHost.publications)
	}
	if _, err = r.Execute(t.Context(), policy.InstallerUID, q); err != nil || edgeHost.publications != 1 || host.restarts != 1 {
		t.Fatal("completed replay repeated effects", err)
	}
}

func TestApplicationPublicEdgeApplyInspectBootAndWithdraw(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(401) }))
	defer server.Close()
	_, portText, _ := net.SplitHostPort(server.Listener.Addr().String())
	port, _ := strconv.Atoi(portText)
	r, host, q, publication := applicationTestRuntime(t)
	edgeHost := &applicationEdgeFixtureHost{applicationFixtureHost: host}
	r.System = edgeHost
	p, _ := r.policy()
	q.Endpoint = &ApplicationEndpointRequest{Hostname: "fixture.invalid", BackendPort: uint16(port)}
	q.Manifest.Listener = &ApplicationListener{Address: "127.0.0.1", Port: uint16(port)}
	q.Manifest.Health = ApplicationHealth{Kind: HealthKindHTTP, Method: "GET", Path: "/", ExpectedStatus: 401, TimeoutSeconds: 1}
	p.Grants[0].Edge = &ApplicationEdgePolicy{Owner: q.Owner, Hostname: q.Endpoint.Hostname, BackendPort: uint16(port), IngressIPv4: "192.0.2.1", Revision: "edge-a", TLS: "managed"}
	applicationPrerequisitesWrite(t, r.PolicyPath, p)
	publication.ExpectedRevision = publication.Revision
	publication.Revision = "revision-public"
	q.ExpectedRevision = publication.Revision
	if err := r.Publish(t.Context(), p.PublisherUID, publication); err != nil {
		t.Fatal(err)
	}
	receipt, err := r.Execute(t.Context(), p.InstallerUID, q)
	if err != nil || !receipt.Applied || receipt.Endpoint == nil || receipt.Endpoint.DNS != "pending" || receipt.Endpoint.TLS == "satisfied" || receipt.Readiness.Client != "unknown" || !strings.Contains(edgeHost.fragment, "https://fixture.invalid") {
		t.Fatalf("%+v %v", receipt, err)
	}
	inspect := q
	inspect.Operation = "inspect"
	observed, err := r.Execute(t.Context(), p.InstallerUID, inspect)
	if err != nil || observed.Current == nil || observed.Current.Endpoint == nil || observed.Current.Readiness.Ingress != "pending" {
		t.Fatal("pending public readiness lost", err)
	}
	edgeHost.fragment = ""
	if _, err := r.RestoreCommitted(t.Context()); err != nil || !strings.Contains(edgeHost.fragment, "https://fixture.invalid") {
		t.Fatal("boot failed to restore edge", err)
	}
	// Update to loopback withdraws the prior route without removing the application.
	p.Grants[0].Edge = nil
	applicationPrerequisitesWrite(t, r.PolicyPath, p)
	publication.ExpectedRevision = publication.Revision
	publication.Revision = "revision-private"
	if err := r.Publish(t.Context(), p.PublisherUID, publication); err != nil {
		t.Fatal(err)
	}
	q.Endpoint = nil
	q.ExpectedRevision = publication.Revision
	q.OperationToken = "withdraw-edge"
	q.ExpectedInstallationRevision = receipt.InstallationRevision
	withdrawn, err := r.Execute(t.Context(), p.InstallerUID, q)
	if err != nil || !withdrawn.Applied || edgeHost.fragment != "" {
		t.Fatal("withdraw failed", err, edgeHost.fragment)
	}
}
