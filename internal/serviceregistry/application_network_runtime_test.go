package serviceregistry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"
)

func applicationNetworkRuntimeTestInputs() (ApplicationGeneration, ApplicationGrant) {
	s := applicationNetworkTestSpec()
	g := ApplicationGeneration{Owner: s.Owner, UID: s.UID}
	grant := ApplicationGrant{Owner: s.Owner, Network: s.Profile, NetworkPolicy: &ApplicationNetworkPolicy{Profiles: []ApplicationNetworkProfile{ApplicationNetworkLoopback, ApplicationNetworkOutboundHTTPS}, Resolvers: s.Resolvers, DeniedCIDRs: s.DeniedCIDRs}}
	return g, grant
}

func TestApplicationNetworkRuntimePolicy(t *testing.T) {
	_, grant := applicationNetworkRuntimeTestInputs()
	if err := ValidateApplicationNetworkPolicy(nil); err != nil {
		t.Fatal(err)
	}
	if err := ValidateApplicationNetworkPolicy(grant.NetworkPolicy); err != nil {
		t.Fatal(err)
	}
	for _, policy := range []*ApplicationNetworkPolicy{
		{}, {Profiles: []ApplicationNetworkProfile{"unrestricted"}},
		{Profiles: []ApplicationNetworkProfile{ApplicationNetworkLoopback, ApplicationNetworkLoopback}},
		{Profiles: []ApplicationNetworkProfile{ApplicationNetworkOutboundHTTPS}},
		{Profiles: []ApplicationNetworkProfile{ApplicationNetworkLoopback}, Resolvers: []string{"resolver.local"}},
		{Profiles: []ApplicationNetworkProfile{ApplicationNetworkLoopback}, DeniedCIDRs: []string{"10.0.0.1/8"}},
	} {
		if ValidateApplicationNetworkPolicy(policy) == nil {
			t.Fatalf("accepted invalid host policy: %+v", policy)
		}
	}
	copy := grant.NetworkPolicy.Clone()
	copy.Profiles[0], copy.Resolvers[0], copy.DeniedCIDRs[0] = "changed", "changed", "changed"
	if ValidateApplicationNetworkPolicy(grant.NetworkPolicy) != nil {
		t.Fatal("trusted policy copy aliases input")
	}
	var absent *ApplicationNetworkPolicy
	if absent.Clone() != nil {
		t.Fatal("nil policy changed")
	}
}

func TestApplicationNetworkRuntimeAdmissionAndLegacy(t *testing.T) {
	g, grant := applicationNetworkRuntimeTestInputs()
	for _, profile := range []ApplicationNetworkProfile{"", ApplicationNetworkLoopback} {
		legacy := ApplicationGrant{Owner: g.Owner, Network: profile}
		// No tool paths, callbacks, process scans or commands for legacy grants.
		if err := (applicationNetworkRuntime{}).ensure(t.Context(), g, legacy); err != nil {
			t.Fatal(err)
		}
		if err := (applicationNetworkRuntime{}).check(t.Context(), g, legacy); err != nil {
			t.Fatal(err)
		}
	}
	for _, kind := range []string{"owner", "uid", "noPolicy", "notAllowed", "unknown"} {
		t.Run(kind, func(t *testing.T) {
			g, grant := applicationNetworkRuntimeTestInputs()
			switch kind {
			case "owner":
				grant.Owner.Resource = "other"
			case "uid":
				g.UID = 0
			case "noPolicy":
				grant.NetworkPolicy = nil
			case "notAllowed":
				grant.NetworkPolicy.Profiles = []ApplicationNetworkProfile{ApplicationNetworkLoopback}
			case "unknown":
				grant.Network = "unrestricted"
			}
			if _, _, err := applicationNetworkPlanForGrant(g, grant); err == nil {
				t.Fatal("grant admitted")
			}
		})
	}
	if _, managed, err := applicationNetworkPlanForGrant(g, grant); err != nil || !managed {
		t.Fatalf("trusted grant not compiled: %v", err)
	}
}

func TestApplicationNetworkRuntimeEnsureAndReadOnlyStart(t *testing.T) {
	_, fake := newApplicationNetworkFake(t)
	g, grant := applicationNetworkRuntimeTestInputs()
	probes := 0
	r := applicationNetworkRuntime{tools: ApplicationNetworkTools{IPTables: "/fixed/iptables", IP6Tables: "/fixed/ip6tables"}, run: fake.run, quiescent: func(context.Context, ApplicationGeneration) error {
		probes++
		fake.fenced = true
		return nil
	}}
	if r.check(t.Context(), g, grant) == nil || probes != 0 {
		t.Fatal("check installed or scanned")
	}
	if err := r.ensure(t.Context(), g, grant); err != nil || probes != 4 {
		t.Fatalf("ensure did not verify the whole installation: probes=%d error=%v", probes, err)
	}
	probes = 0
	r.quiescent = func(context.Context, ApplicationGeneration) error {
		probes++
		return errors.New("application is now running")
	}
	fake.calls = nil
	if err := r.ensure(t.Context(), g, grant); err != nil {
		t.Fatal("unchanged installed network should not reinstall:", err)
	}
	if err := r.check(t.Context(), g, grant); err != nil || probes != 0 {
		t.Fatal("read-only start check scanned or altered live process state")
	}
	for _, call := range fake.calls {
		if !strings.HasSuffix(call, "[-S]") {
			t.Fatal("unchanged policy mutated:", call)
		}
	}
	delete(fake.chains[0], "LNA"+g.Owner.Instance()[3:])
	fake.chains[0]["OUTPUT"] = fake.chains[0]["OUTPUT"][1:]
	if err := r.ensure(t.Context(), g, grant); err == nil || probes != 1 {
		t.Fatal("missing rules repaired despite live application")
	}
}

func TestApplicationNetworkRuntimeQuiescenceLossRetainsGuards(t *testing.T) {
	for _, failedProbe := range []int{1, 2, 3, 4} {
		t.Run(fmt.Sprint(failedProbe), func(t *testing.T) {
			_, fake := newApplicationNetworkFake(t)
			g, grant := applicationNetworkRuntimeTestInputs()
			calls := 0
			r := applicationNetworkRuntime{tools: ApplicationNetworkTools{IPTables: "/fixed/iptables", IP6Tables: "/fixed/ip6tables"}, run: fake.run, quiescent: func(context.Context, ApplicationGeneration) error {
				calls++
				if calls == failedProbe {
					return errors.New("same UID action appeared")
				}
				fake.fenced = true
				return nil
			}}
			if r.ensure(t.Context(), g, grant) == nil {
				t.Fatal("uncertain live UID admitted")
			}
			if failedProbe == 1 {
				if len(fake.chains[0]) != 2 || len(fake.chains[1]) != 2 {
					t.Fatal("mutation before quiescence")
				}
				return
			}
			for _, chains := range fake.chains {
				rules := chains["LNA"+g.Owner.Instance()[3:]]
				if len(rules) == 0 || len(rules[0]) != 6 || rules[0][len(rules[0])-1] != "DROP" {
					t.Fatal("quiescence loss did not retain unconditional DROP")
				}
			}
			if r.check(t.Context(), g, grant) == nil {
				t.Fatal("guarded partial installation passed start check")
			}
		})
	}
}

func applicationNetworkStoppedUnitTestValues() map[string]string {
	v := map[string]string{}
	for _, k := range strings.Split(applicationNetworkUnitProperties, ",") {
		v[k] = ""
	}
	for k, value := range map[string]string{
		"LoadState": "loaded", "ActiveState": "inactive", "SubState": "dead", "MainPID": "0", "ControlPID": "0", "NeedDaemonReload": "no",
		"IPAddressDeny": "0.0.0.0/0 ::/0", "IPAddressAllow": "127.0.0.0/8 ::1/128", "PrivateNetwork": "no", "PrivateUsers": "no", "DynamicUser": "no", "RestrictNamespaces": "yes", "NoNewPrivileges": "yes",
		"RestrictAddressFamilies": "AF_UNIX AF_INET AF_INET6", "FileDescriptorStoreMax": "0", "NFileDescriptorStore": "0",
	} {
		v[k] = value
	}
	return v
}

func applicationNetworkUnitTestRaw(v map[string]string) []byte {
	var out strings.Builder
	for _, key := range strings.Split(applicationNetworkUnitProperties, ",") {
		if value, ok := v[key]; ok {
			fmt.Fprintf(&out, "%s=%s\n", key, value)
		}
	}
	return []byte(out.String())
}

func TestApplicationNetworkRuntimeStoppedFence(t *testing.T) {
	if err := applicationNetworkStoppedUnit(applicationNetworkUnitTestRaw(applicationNetworkStoppedUnitTestValues())); err != nil {
		t.Fatal(err)
	}
	for key, bad := range map[string]string{
		"LoadState": "not-found", "ActiveState": "active", "SubState": "auto-restart", "MainPID": "32", "ControlPID": "33", "Job": "99", "TriggeredBy": "worker.socket", "NeedDaemonReload": "yes",
		"IPAddressDeny": "0.0.0.0/0", "IPAddressAllow": "127.0.0.0/8 ::/0", "PrivateNetwork": "yes", "PrivateUsers": "yes", "DynamicUser": "yes", "NetworkNamespacePath": "/run/netns/worker", "JoinsNamespaceOf": "other.service",
		"RestrictNamespaces": "no", "CapabilityBoundingSet": "cap_net_admin", "AmbientCapabilities": "cap_net_raw", "NoNewPrivileges": "no", "RestrictAddressFamilies": "AF_UNIX AF_PACKET", "FileDescriptorStoreMax": "1", "NFileDescriptorStore": "1",
	} {
		t.Run(key, func(t *testing.T) {
			v := applicationNetworkStoppedUnitTestValues()
			v[key] = bad
			if applicationNetworkStoppedUnit(applicationNetworkUnitTestRaw(v)) == nil {
				t.Fatal("unsafe effective unit accepted")
			}
		})
	}
	v := applicationNetworkStoppedUnitTestValues()
	delete(v, "Job")
	if applicationNetworkStoppedUnit(applicationNetworkUnitTestRaw(v)) == nil {
		t.Fatal("missing property accepted")
	}
	raw := applicationNetworkUnitTestRaw(applicationNetworkStoppedUnitTestValues())
	if applicationNetworkStoppedUnit(append(raw, []byte("MainPID=0\n")...)) == nil {
		t.Fatal("duplicate property accepted")
	}
	for _, allowed := range []string{"::ffff:127.0.0.1/128", "127.0.0.0/7", "10.0.0.0/8"} {
		v := applicationNetworkStoppedUnitTestValues()
		v["IPAddressAllow"] = allowed
		if applicationNetworkStoppedUnit(applicationNetworkUnitTestRaw(v)) == nil {
			t.Fatal("expanded address fence accepted")
		}
	}
}

const applicationNetworkSocketTestHeader = "sl local_address rem_address st tx_queue rx_queue tr tm->when retrnsmt uid timeout inode\n"

func applicationNetworkProcFixture() (applicationNetworkProc, fstest.MapFS) {
	f := fstest.MapFS{
		"self/mountinfo":  {Data: []byte("1 0 0:1 / /proc rw,nosuid,nodev,noexec - proc proc rw\n")},
		"1/task/1/status": {Data: []byte("Name:\tsystemd\nUid:\t0\t0\t0\t0\n")},
	}
	for _, name := range []string{"tcp", "tcp6", "udp", "udp6"} {
		f["1/net/"+name] = &fstest.MapFile{Data: []byte(applicationNetworkSocketTestHeader)}
	}
	p := applicationNetworkProc{fs: f, readlink: func(path string) (string, error) {
		parts := strings.Split(path, "/")
		return parts[len(parts)-1] + ":[1]", nil
	}}
	return p, f
}

func TestApplicationNetworkRuntimeProcQuiescence(t *testing.T) {
	g, _ := applicationNetworkRuntimeTestInputs()
	p, _ := applicationNetworkProcFixture()
	if err := p.quiescent(t.Context(), g.UID); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"real", "effective", "saved", "filesystem", "thread", "socket4", "socket6", "malformedSocket", "unsupportedSocket", "missingIPv6", "hidden", "namespace", "missingUid", "truncated", "cancelled"} {
		t.Run(kind, func(t *testing.T) {
			p, f := applicationNetworkProcFixture()
			switch kind {
			case "real", "effective", "saved", "filesystem", "thread":
				index := map[string]int{"real": 0, "effective": 1, "saved": 2, "filesystem": 3, "thread": 1}[kind]
				uids := []string{"0", "0", "0", "0"}
				uids[index] = fmt.Sprint(g.UID)
				f["82/task/83/status"] = &fstest.MapFile{Data: []byte("Uid:\t" + strings.Join(uids, "\t") + "\n")}
			case "socket4", "socket6":
				table := "tcp"
				if kind == "socket6" {
					table = "udp6"
				}
				f["1/net/"+table] = &fstest.MapFile{Data: []byte(applicationNetworkSocketTestHeader + fmt.Sprintf("0: 0100007F:0001 00000000:0000 0A 0:0 0:0 0 %d 0 42\n", g.UID))}
			case "malformedSocket":
				f["1/net/tcp"].Data = []byte("unknown format\n")
			case "unsupportedSocket":
				f["1/net/sctp/assocs"] = &fstest.MapFile{Data: []byte("unparsed ownership\n")}
			case "missingIPv6":
				delete(f, "1/net/tcp6")
			case "hidden":
				f["self/mountinfo"].Data = []byte("1 0 0:1 / /proc rw - proc proc rw,hidepid=2\n")
			case "namespace":
				old := p.readlink
				p.readlink = func(path string) (string, error) {
					if path == "self/ns/net" {
						return "net:[2]", nil
					}
					return old(path)
				}
			case "missingUid":
				f["1/task/1/status"].Data = []byte("Name:\tsystemd\n")
			case "truncated":
				f["1/task/1/status"].Data = []byte(strings.Repeat("x", 65537))
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if kind == "cancelled" {
				cancel()
			}
			if p.quiescent(ctx, g.UID) == nil {
				t.Fatal("uncertain proc/namespace/socket state admitted")
			}
		})
	}
	p, _ = applicationNetworkProcFixture()
	oldReadlink := p.readlink
	p.readlink = func(path string) (string, error) {
		if strings.Contains(path, "/task/") {
			return "net:[unrelated-desktop]", nil
		}
		return oldReadlink(path)
	}
	if err := p.quiescent(t.Context(), g.UID); err != nil {
		t.Fatal("unrelated service namespaces blocked the application UID", err)
	}
	p, _ = applicationNetworkProcFixture()
	p.fs = applicationNetworkUnreadableFS{p.fs}
	if p.quiescent(t.Context(), g.UID) == nil {
		t.Fatal("unreadable process accepted")
	}
}

type applicationNetworkUnreadableFS struct{ fs.FS }

func (f applicationNetworkUnreadableFS) Open(path string) (fs.File, error) {
	if strings.HasSuffix(path, "/status") {
		return nil, fs.ErrPermission
	}
	return f.FS.Open(path)
}

func TestApplicationNetworkRuntimeDropInFenceScope(t *testing.T) {
	g, grant := applicationNetworkRuntimeTestInputs()
	m := ApplicationManifest{Process: ApplicationProcess{MemoryMaxBytes: 1024, Restart: "on-failure"}}
	for _, profile := range []ApplicationNetworkProfile{"", ApplicationNetworkLoopback, ApplicationNetworkOutboundHTTPS} {
		grant.Network = profile
		raw := applicationDropIn(grant, m, applicationTestDescriptor(), "/config/generation/config.json", g.UID)
		clears := strings.Contains(raw, "\nIPAddressDeny=\nIPAddressAllow=\n")
		if clears != (profile == ApplicationNetworkOutboundHTTPS) {
			t.Fatalf("template fence reset for profile %q: %s", profile, raw)
		}
		if strings.Contains(raw, "IPForward=") || strings.Contains(raw, "ListenStream=") {
			t.Fatal("network profile introduced inbound configuration")
		}
	}
}

func TestApplicationNetworkRuntimePreservedOutputReload(t *testing.T) {
	_, fake := newApplicationNetworkFake(t)
	g, grant := applicationNetworkRuntimeTestInputs()
	probes := 0
	r := applicationNetworkRuntime{tools: ApplicationNetworkTools{IPTables: "/fixed/iptables", IP6Tables: "/fixed/ip6tables"}, run: fake.run, quiescent: func(context.Context, ApplicationGeneration) error {
		probes++
		fake.fenced = true
		return nil
	}}
	if err := r.ensure(t.Context(), g, grant); err != nil {
		t.Fatal(err)
	}
	// Model the inspected Main reload's ownership boundary, not a new firewall
	// implementation: replacing INPUT-owned chains leaves OUTPUT/UID rules intact.
	for _, chains := range fake.chains {
		chains["nixos-fw"], chains["nixos-drop"] = [][]string{{"-j", "RETURN"}}, [][]string{{"-j", "DROP"}}
	}
	fake.calls, probes = nil, 0
	if err := r.ensure(t.Context(), g, grant); err != nil || probes != 0 {
		t.Fatalf("preserved rules reinstalled or live processes scanned: %v", err)
	}
	for _, call := range fake.calls {
		if !strings.HasSuffix(call, "[-S]") {
			t.Fatal("preserved OUTPUT mutated after reload:", call)
		}
	}
}

type applicationNetworkOrderedHost struct {
	*applicationFixtureHost
	events []string
	fail   bool
}

func applicationNetworkEventOrder(events []string) string {
	var order []string
	for _, event := range events {
		if event == "network" && len(order) > 0 && order[len(order)-1] == "network" {
			continue
		}
		order = append(order, event)
	}
	return strings.Join(order, ",")
}

func (h *applicationNetworkOrderedHost) ensureApplicationNetwork(context.Context, ApplicationGeneration, ApplicationGrant) error {
	h.events = append(h.events, "network")
	if h.fail {
		return applicationError("network.fixture_refused")
	}
	return nil
}

func (h *applicationNetworkOrderedHost) PublishGeneration(ctx context.Context, g ApplicationGeneration) error {
	h.events = append(h.events, "publish")
	return h.applicationFixtureHost.PublishGeneration(ctx, g)
}

func (h *applicationNetworkOrderedHost) Restart(ctx context.Context, g ApplicationGeneration) error {
	h.events = append(h.events, "restart")
	return h.applicationFixtureHost.Restart(ctx, g)
}

func (h *applicationNetworkOrderedHost) Start(ctx context.Context, g ApplicationGeneration) error {
	h.events = append(h.events, "start")
	return h.applicationFixtureHost.Start(ctx, g)
}

func applicationNetworkOrderedRuntime(t *testing.T) (ApplicationRuntime, *applicationNetworkOrderedHost, ApplicationRuntimeRequest) {
	t.Helper()
	r, base, q, publication := applicationTestRuntime(t)
	policy, err := r.policy()
	if err != nil {
		t.Fatal(err)
	}
	_, networkGrant := applicationNetworkRuntimeTestInputs()
	policy.Grants[0].Network = networkGrant.Network
	policy.Grants[0].NetworkPolicy = networkGrant.NetworkPolicy.Clone()
	raw, err := json.Marshal(policy)
	if err != nil {
		t.Fatal(err)
	}
	if err = applicationAtomic(r.PolicyPath, raw, 0600); err != nil {
		t.Fatal(err)
	}
	publication.ExpectedRevision, publication.Revision = publication.Revision, "revision-network"
	if err = r.Publish(t.Context(), policy.PublisherUID, publication); err != nil {
		t.Fatal(err)
	}
	q.ExpectedRevision = publication.Revision
	h := &applicationNetworkOrderedHost{applicationFixtureHost: base}
	r.System = h
	return r, h, q
}

func TestApplicationNetworkRuntimeApplyAndRestoreOrder(t *testing.T) {
	for _, refusal := range []bool{false, true} {
		t.Run(fmt.Sprint(refusal), func(t *testing.T) {
			r, h, q := applicationNetworkOrderedRuntime(t)
			h.fail = refusal
			_, err := r.Execute(t.Context(), 1234, q)
			if refusal {
				if err == nil || applicationNetworkEventOrder(h.events) != "network" {
					t.Fatalf("published or started after refused network: %v %v", h.events, err)
				}
				return
			}
			if err != nil || applicationNetworkEventOrder(h.events) != "network,publish,restart" {
				t.Fatalf("apply relaxed template before network: %v %v", h.events, err)
			}
			// Restore may recreate volatile generation state, but must enforce
			// the network first and must not start when that enforcement refuses.
			h.generation, h.loaded = ApplicationGeneration{}, ""
			h.process = ApplicationProcessObservation{State: "inactive"}
			h.events, h.fail = nil, true
			observations, _ := r.RestoreCommitted(t.Context())
			if len(observations) != 1 || observations[0].State != "failed" || applicationNetworkEventOrder(h.events) != "network" {
				t.Fatalf("restore bypassed failed enforcement: %v %+v", h.events, observations)
			}
			h.events, h.fail = nil, false
			observations, err = r.RestoreCommitted(t.Context())
			if err != nil || len(observations) != 1 || observations[0].State != "observed" || applicationNetworkEventOrder(h.events) != "network,publish,start" {
				t.Fatalf("restore ordering: %v %+v %v", h.events, observations, err)
			}
			h.events = nil
			if _, err = r.RestoreCommitted(t.Context()); err != nil || applicationNetworkEventOrder(h.events) != "network" {
				t.Fatalf("preserved restore restarted application: %v %v", h.events, err)
			}
		})
	}
}

func TestApplicationNetworkRuntimeResumeRechecksBeforePublish(t *testing.T) {
	r, h, q := applicationNetworkOrderedRuntime(t)
	interrupted := false
	r.FailureHook = func(stage string) error {
		if stage == "recorded:network" && !interrupted {
			interrupted = true
			return errors.New("helper interrupted after network journal commit")
		}
		return nil
	}
	if _, err := r.Execute(t.Context(), 1234, q); err == nil || !interrupted || applicationNetworkEventOrder(h.events) != "network" {
		t.Fatalf("did not interrupt before publishing relaxed unit: %v %v", h.events, err)
	}
	r.FailureHook, h.events, h.fail = nil, nil, true
	if _, err := r.Execute(t.Context(), 1234, q); err == nil || strings.Contains(strings.Join(h.events, ","), "publish") || strings.Contains(strings.Join(h.events, ","), "restart") {
		t.Fatalf("resumed done network effect bypassed current policy: %v %v", h.events, err)
	}
}
