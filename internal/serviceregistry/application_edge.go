package serviceregistry

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"os"
	"regexp"
	"time"

	pc "loom.local/loom/internal/projectcontracts"
)

type ApplicationEdgePolicy struct {
	IngressIPv4 string           `json:"ingress_ipv4,omitempty"`
	Owner       ApplicationOwner `json:"owner"`
	Hostname    string           `json:"hostname"`
	BackendPort uint16           `json:"backend_port"`
	Revision    string           `json:"revision"`
	TLS         string           `json:"tls"`
	DNSReady    bool             `json:"dns_ready"`
}

var applicationHostnamePattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$`)

func applicationEdgeFragment(grant ApplicationGrant, m ApplicationManifest) ([]byte, error) {
	p := grant.Edge
	if p == nil {
		return nil, nil
	}
	if p.Owner != grant.Owner || !applicationHostnamePattern.MatchString(p.Hostname) || len(p.Hostname) > 253 || p.TLS != "managed" || !applicationRevisionPattern.MatchString(p.Revision) || m.Listener == nil || m.Listener.Address != "127.0.0.1" || m.Listener.Port != p.BackendPort {
		return nil, applicationError("edge.policy_denied")
	}
	if !p.DNSReady && !applicationIngressIPv4(p.IngressIPv4) {
		return nil, applicationError("edge.dns_prerequisite")
	}
	return []byte(fmt.Sprintf("https://%s {\n header X-Loom-Application %s\n reverse_proxy 127.0.0.1:%d\n}\n", p.Hostname, grant.Owner.Instance(), p.BackendPort)), nil
}

func applicationIngressIPv4(s string) bool {
	a, err := netip.ParseAddr(s)
	return err == nil && a.Is4() && a.IsGlobalUnicast() && !a.IsPrivate() && !a.IsLoopback()
}

func (p ApplicationPublicEndpointPolicy) valid() bool {
	return pc.ValidApplicationHostname(p.Suffix) && applicationIngressIPv4(p.IngressIPv4)
}

func (q ApplicationEndpointRequest) valid() bool {
	return pc.ValidApplicationHostname(q.Hostname) && q.BackendPort >= 1024
}

// Provision() repeats this under the existing global inventory lock. Retained
// installed names stay reserved until their owning route is replaced/withdrawn.
func (r ApplicationRuntime) checkEndpointOwnership(p ApplicationHostPolicy, q ApplicationProvisionRequest) error {
	if q.Endpoint == nil {
		return nil
	}
	owners, err := r.grantOwners(p)
	if err != nil {
		return err
	}
	for _, owner := range owners {
		if owner == q.Owner {
			continue
		}
		for _, g := range p.Grants {
			if g.Owner == owner && g.Edge != nil && g.Edge.Hostname == q.Endpoint.Hostname {
				return applicationError("edge.hostname_in_use")
			}
		}
		stored, err := r.readManagedGrant(owner)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if stored.Request.Endpoint != nil && stored.Request.Endpoint.Hostname == q.Endpoint.Hostname {
			return applicationError("edge.hostname_in_use")
		}
		var i ApplicationInstallation
		err = r.Store.read("installation-"+owner.Instance(), &i)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if i.Request != nil && i.Request.Endpoint != nil && !i.Retired && i.Request.Endpoint.Hostname == q.Endpoint.Hostname {
			return applicationError("edge.hostname_in_use")
		}
		if i.PreviousEndpoint != nil && !i.Retired && i.PreviousEndpoint.Hostname == q.Endpoint.Hostname {
			return applicationError("edge.hostname_in_use")
		}
	}
	return nil
}

// An ordinary unauthenticated probe through the configured public ingress.
// DNS, trusted TLS, route identity and application status are distinct facts.
// Never follow redirects, ambient proxies or DNS to a different host address.
func probeApplicationEdge(ctx context.Context, grant ApplicationGrant, m ApplicationManifest) *pc.ApplicationEndpointStatus {
	p := grant.Edge
	if p == nil {
		return nil
	}
	out := &pc.ApplicationEndpointStatus{URL: "https://" + p.Hostname, DNS: "pending", TLS: "pending", Routing: "pending", ObservedAt: time.Now().UTC()}
	if !applicationIngressIPv4(p.IngressIPv4) {
		out.DNS = "unknown"
		return out
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, p.Hostname)
	if err != nil || len(ips) == 0 {
		return out
	}
	for _, ip := range ips {
		if ip.IP.String() != p.IngressIPv4 {
			out.DNS = "mismatch"
			return out
		}
	}
	out.DNS = "satisfied"
	transport := &http.Transport{Proxy: nil, DisableKeepAlives: true, MaxResponseHeaderBytes: 8192, DialContext: func(c context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{Timeout: 2 * time.Second}).DialContext(c, "tcp4", net.JoinHostPort(p.IngressIPv4, "443"))
	}}
	defer transport.CloseIdleConnections()
	client := http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	method, path := "HEAD", "/"
	if m.Health.Kind == HealthKindHTTP {
		method, path = m.Health.Method, m.Health.Path
	}
	req, err := http.NewRequestWithContext(ctx, method, out.URL+path, nil)
	if err != nil {
		return out
	}
	resp, err := client.Do(req)
	if err != nil {
		return out
	}
	_ = resp.Body.Close()
	out.TLS = "satisfied"
	out.HTTPStatus = resp.StatusCode
	if resp.Header.Get("X-Loom-Application") == grant.Owner.Instance() && ((m.Health.Kind == HealthKindHTTP && resp.StatusCode == m.Health.ExpectedStatus) || (m.Health.Kind == HealthKindManager && resp.StatusCode < 500)) {
		out.Routing = "satisfied"
	}
	return out
}
