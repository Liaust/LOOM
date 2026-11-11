package serviceregistry

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
)

func applicationNetworkTestSpec() ApplicationNetworkSpec {
	o := ApplicationOwner{ProjectID: "project_01ARZ3NDEKTSV4RRFFQ69G5FAV", NodeID: "node_01ARZ3NDEKTSV4RRFFQ69G5FAV", Resource: "worker"}
	return ApplicationNetworkSpec{Owner: o, UID: applicationAccountUID(o), Profile: ApplicationNetworkOutboundHTTPS, Resolvers: []string{"127.0.0.53", "10.2.0.53", "fd00::53", "2606:4700:4700::1111"}, DeniedCIDRs: []string{"8.8.8.8/32", "2606:4700::/48"}}
}

func applicationNetworkTestPlan(t *testing.T, s ApplicationNetworkSpec) ApplicationNetworkPlan {
	t.Helper()
	p, err := CompileApplicationNetworkPlan(s)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestApplicationNetworkCompileImmutableDeterministic(t *testing.T) {
	s := applicationNetworkTestSpec()
	p := applicationNetworkTestPlan(t, s)
	if p.Owner() != s.Owner || p.UID() != s.UID || p.Profile() != s.Profile || !strings.Contains(p.Scope(), "not Google-only") {
		t.Fatal("lost effective scope/identity")
	}
	families := p.Families()
	if families[0].IPv6 || !families[1].IPv6 || len(families[0].Chain) > 28 || families[0].Chain != families[1].Chain {
		t.Fatal("invalid dual-stack chains")
	}
	s.Resolvers[0], s.Resolvers[1] = s.Resolvers[1], s.Resolvers[0]
	s.Resolvers = append(s.Resolvers, s.Resolvers[0])
	s.DeniedCIDRs[0], s.DeniedCIDRs[1] = s.DeniedCIDRs[1], s.DeniedCIDRs[0]
	if !reflect.DeepEqual(p.Families(), applicationNetworkTestPlan(t, s).Families()) {
		t.Fatal("policy depends on input order/duplicates")
	}
	families[0].Hook[3], families[0].Rules[0][0], families[0].Chain = "0", "corrupt", "OUTPUT"
	if reflect.DeepEqual(p.Families(), families) || p.Families()[0].Hook[3] != strconv.FormatUint(uint64(s.UID), 10) {
		t.Fatal("projection mutated immutable plan")
	}
	s.Profile = ApplicationNetworkLoopback
	loop := applicationNetworkTestPlan(t, s)
	if loop.Families()[0].Chain != p.Families()[0].Chain {
		t.Fatal("chain changed with profile")
	}
	s.Owner.Resource = "other"
	s.UID = applicationAccountUID(s.Owner)
	if applicationNetworkTestPlan(t, s).Families()[0].Chain == p.Families()[0].Chain {
		t.Fatal("owner collision")
	}
}

func TestApplicationNetworkValidation(t *testing.T) {
	for _, raw := range []string{"", "resolver.local", "https://1.1.1.1", "1.1.1.1:53", "1.1.1.1/32", "1.1.1.1 -j ACCEPT", "0.0.0.0", "::", "224.0.0.1", "ff02::1", "169.254.169.254", "fe80::1", "fe80::1%eth0", "::ffff:10.0.0.1", "255.255.255.255", "2001:db8::53", "64:ff9b::101:101"} {
		t.Run(raw, func(t *testing.T) {
			s := applicationNetworkTestSpec()
			s.Resolvers = []string{raw}
			if _, err := CompileApplicationNetworkPlan(s); err == nil {
				t.Fatal("invalid resolver accepted")
			}
		})
	}
	for name, change := range map[string]func(*ApplicationNetworkSpec){
		"root":       func(s *ApplicationNetworkSpec) { s.UID = 0 },
		"foreignUID": func(s *ApplicationNetworkSpec) { s.UID++ },
		"owner":      func(s *ApplicationNetworkSpec) { s.Owner.Resource = "bad name" },
		"profile":    func(s *ApplicationNetworkSpec) { s.Profile = "unrestricted" },
		"noDNS":      func(s *ApplicationNetworkSpec) { s.Resolvers = nil },
		"manyDNS":    func(s *ApplicationNetworkSpec) { s.Resolvers = make([]string, 9) },
		"manyDenies": func(s *ApplicationNetworkSpec) { s.DeniedCIDRs = make([]string, 65) },
		"denyHost":   func(s *ApplicationNetworkSpec) { s.DeniedCIDRs = []string{"10.0.0.1/8"} },
		"denyName":   func(s *ApplicationNetworkSpec) { s.DeniedCIDRs = []string{"control.local"} },
		"denyMapped": func(s *ApplicationNetworkSpec) { s.DeniedCIDRs = []string{"::ffff:10.0.0.0/104"} },
		"deniedDNS":  func(s *ApplicationNetworkSpec) { s.DeniedCIDRs = []string{"10.0.0.0/8"} },
	} {
		t.Run(name, func(t *testing.T) {
			s := applicationNetworkTestSpec()
			change(&s)
			if _, err := CompileApplicationNetworkPlan(s); err == nil {
				t.Fatal("invalid spec accepted")
			}
		})
	}
	s := applicationNetworkTestSpec()
	s.Profile, s.Resolvers = ApplicationNetworkLoopback, nil
	applicationNetworkTestPlan(t, s)
}

// Evaluate only the matches emitted by this compiler, to check first-match
// policy ordering rather than merely the presence of a particular DROP token.
type applicationNetworkPacket struct {
	source, dest, protocol, port, state, direction, device string
	local                                                  bool
}

func applicationNetworkVerdict(t *testing.T, f ApplicationNetworkFamilyPlan, p applicationNetworkPacket) string {
	t.Helper()
	for _, r := range f.Rules {
		match := true
		for i := 0; i < len(r); i += 2 {
			key, val := r[i], r[i+1]
			switch key {
			case "-m", "--comment":
			case "-d", "-s":
				addr := p.dest
				if key == "-s" {
					addr = p.source
				}
				a, err := netip.ParseAddr(addr)
				match = match && err == nil && netip.MustParsePrefix(val).Contains(a)
			case "-p":
				match = match && p.protocol == val
			case "--dport":
				match = match && p.port == val
			case "--ctstate":
				match = match && strings.Contains(","+val+",", ","+p.state+",")
			case "--ctdir":
				match = match && p.direction == val
			case "-o":
				match = match && p.device == val
			case "--dst-type":
				match = match && val == "LOCAL" && p.local
			case "-j":
				if match {
					return val
				}
			default:
				t.Fatalf("unexpected match %q", key)
			}
		}
	}
	t.Fatal("missing terminal verdict")
	return ""
}

func TestApplicationNetworkPolicyOrderingAndIPv6(t *testing.T) {
	p := applicationNetworkTestPlan(t, applicationNetworkTestSpec())
	for _, tc := range []struct {
		dest, port, protocol, state, direction, verdict string
		ipv6, local                                     bool
	}{
		{"1.1.1.1", "443", "tcp", "NEW", "ORIGINAL", "RETURN", false, false},
		{"1.1.1.1", "443", "tcp", "ESTABLISHED", "ORIGINAL", "RETURN", false, false},
		{"1.1.1.1", "443", "tcp", "ESTABLISHED", "REPLY", "DROP", false, false},
		{"1.1.1.1", "443", "tcp", "RELATED", "ORIGINAL", "DROP", false, false},
		{"1.1.1.1", "443", "tcp", "INVALID", "ORIGINAL", "DROP", false, false},
		{"1.1.1.1", "443", "udp", "ESTABLISHED", "ORIGINAL", "DROP", false, false},
		{"1.1.1.1", "80", "tcp", "ESTABLISHED", "ORIGINAL", "DROP", false, false},
		{"10.1.1.1", "443", "tcp", "ESTABLISHED", "ORIGINAL", "DROP", false, false},
		{"100.64.1.1", "443", "tcp", "NEW", "ORIGINAL", "DROP", false, false},
		{"169.254.169.254", "443", "tcp", "NEW", "ORIGINAL", "DROP", false, false},
		{"8.8.8.8", "443", "tcp", "ESTABLISHED", "ORIGINAL", "DROP", false, false},
		{"1.1.1.1", "443", "tcp", "NEW", "ORIGINAL", "DROP", false, true},
		{"10.2.0.53", "53", "udp", "NEW", "ORIGINAL", "RETURN", false, false},
		{"10.2.0.53", "53", "tcp", "ESTABLISHED", "ORIGINAL", "RETURN", false, false},
		{"10.2.0.53", "443", "tcp", "NEW", "ORIGINAL", "DROP", false, false},
		{"10.2.0.54", "53", "udp", "NEW", "ORIGINAL", "DROP", false, false},
		{"127.0.0.53", "53", "udp", "NEW", "ORIGINAL", "RETURN", false, true},
		{"127.0.0.1", "443", "tcp", "NEW", "ORIGINAL", "DROP", false, true},
		{"2606:4700:4700::1111", "443", "tcp", "ESTABLISHED", "ORIGINAL", "RETURN", true, false},
		{"2606:4700::1", "443", "tcp", "ESTABLISHED", "ORIGINAL", "DROP", true, false},
		{"fd00::53", "53", "udp", "NEW", "ORIGINAL", "RETURN", true, false},
		{"fd00::53", "443", "tcp", "ESTABLISHED", "ORIGINAL", "DROP", true, false},
		{"fe80::1", "443", "tcp", "NEW", "ORIGINAL", "DROP", true, false},
		{"::ffff:169.254.169.254", "443", "tcp", "ESTABLISHED", "ORIGINAL", "DROP", true, false},
		{"64:ff9b::a00:1", "443", "tcp", "NEW", "ORIGINAL", "DROP", true, false},
		{"2002:a00:1::1", "443", "tcp", "NEW", "ORIGINAL", "DROP", true, false},
		{"2001:0:1234::1", "443", "tcp", "NEW", "ORIGINAL", "DROP", true, false},
		{"ff02::1", "443", "tcp", "NEW", "ORIGINAL", "DROP", true, false},
	} {
		t.Run(fmt.Sprint(tc), func(t *testing.T) {
			f := p.Families()[0]
			if tc.ipv6 {
				f = p.Families()[1]
			}
			packet := applicationNetworkPacket{dest: tc.dest, port: tc.port, protocol: tc.protocol, state: tc.state, direction: tc.direction, local: tc.local}
			if got := applicationNetworkVerdict(t, f, packet); got != tc.verdict {
				t.Fatalf("got %s want %s", got, tc.verdict)
			}
		})
	}
	for _, profile := range []ApplicationNetworkProfile{ApplicationNetworkLoopback, ApplicationNetworkOutboundHTTPS} {
		s := applicationNetworkTestSpec()
		s.Profile = profile
		for _, f := range applicationNetworkTestPlan(t, s).Families() {
			loop := "127.0.0.1"
			if f.IPv6 {
				loop = "::1"
			}
			packet := applicationNetworkPacket{source: loop, dest: loop, port: "50000", protocol: "tcp", state: "ESTABLISHED", direction: "REPLY", device: "lo", local: true}
			if applicationNetworkVerdict(t, f, packet) != "RETURN" {
				t.Fatal("lost local listener response")
			}
			packet.state, packet.direction = "NEW", "ORIGINAL"
			want := "DROP"
			if profile == ApplicationNetworkLoopback {
				want = "RETURN"
			}
			if applicationNetworkVerdict(t, f, packet) != want {
				t.Fatal("loopback profile boundary")
			}
			for _, r := range f.Rules {
				if r[len(r)-1] == "ACCEPT" || strings.Contains(strings.Join(r, " "), "RELATED") {
					t.Fatal("host policy bypass")
				}
			}
		}
	}
}

type applicationNetworkFake struct {
	chains [2]map[string][][]string
	calls  []string
	fail   func(int, []string) bool
	fenced bool
	fences int
}

func newApplicationNetworkFake(t *testing.T) (*ApplicationNetworkFirewall, *applicationNetworkFake) {
	t.Helper()
	f := &applicationNetworkFake{}
	for i := range f.chains {
		f.chains[i] = map[string][][]string{"OUTPUT": {{"-m", "conntrack", "--ctstate", "ESTABLISHED,RELATED", "-j", "ACCEPT"}}, "unrelated": {{"-j", "RETURN"}}}
	}
	h, err := NewApplicationNetworkFirewall(ApplicationNetworkTools{IPTables: "/fixed/iptables", IP6Tables: "/fixed/ip6tables"}, f.run, func(context.Context, ApplicationOwner) error {
		f.fenced = true
		f.fences++
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return h, f
}

func (f *applicationNetworkFake) run(ctx context.Context, tool string, raw ...string) ([]byte, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	i := 0
	if tool == "/fixed/ip6tables" {
		i = 1
	} else if tool != "/fixed/iptables" {
		return nil, errors.New("bad tool")
	}
	if len(raw) < 5 || !reflect.DeepEqual(raw[:4], []string{"-w", "5", "-t", "filter"}) {
		return nil, errors.New("bad fixed arguments")
	}
	a := raw[4:]
	f.calls = append(f.calls, fmt.Sprint(i, ":", a))
	if f.fail != nil && f.fail(i, a) {
		return nil, errors.New("injected failure")
	}
	if a[0] != "-S" && !f.fenced {
		return nil, errors.New("mutation before fence")
	}
	c := f.chains[i]
	if a[0] == "-S" {
		out := "-P INPUT DROP\n-P FORWARD DROP\n-P OUTPUT ACCEPT\n"
		keys := make([]string, 0, len(c))
		for k := range c {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if k != "OUTPUT" {
				out += "-N " + k + "\n"
			}
			for _, r := range c[k] {
				copy := append([]string(nil), r...)
				for j := 0; j+1 < len(copy); j++ {
					if copy[j] == "--comment" {
						copy[j+1] = strconv.Quote(copy[j+1])
					}
				}
				out += "-A " + k + " " + strings.Join(copy, " ") + "\n"
			}
		}
		return []byte(out), nil
	}
	if len(a) < 2 || (a[1] != "OUTPUT" && !strings.HasPrefix(a[1], "LNA")) {
		return nil, errors.New("unrelated mutation")
	}
	rules, exists := c[a[1]]
	switch a[0] {
	case "-N":
		if exists {
			return nil, errors.New("exists")
		}
		c[a[1]] = nil
	case "-A":
		c[a[1]] = append(rules, append([]string(nil), a[2:]...))
	case "-I":
		n, err := strconv.Atoi(a[2])
		if !exists || err != nil || n < 1 || n > len(rules)+1 {
			return nil, errors.New("bad insertion")
		}
		r := append([][]string(nil), rules[:n-1]...)
		r = append(r, append([]string(nil), a[3:]...))
		c[a[1]] = append(r, rules[n-1:]...)
	case "-D":
		n := -1
		if len(a) == 3 {
			if number, err := strconv.Atoi(a[2]); err == nil {
				n = number - 1
			}
		} else {
			for j, r := range rules {
				if reflect.DeepEqual(r, a[2:]) {
					n = j
					break
				}
			}
		}
		if n < 0 || n >= len(rules) {
			return nil, errors.New("missing rule")
		}
		c[a[1]] = append(rules[:n], rules[n+1:]...)
	case "-X":
		if !exists || len(rules) != 0 {
			return nil, errors.New("not empty")
		}
		delete(c, a[1])
	default:
		return nil, errors.New("unbounded/unsupported command")
	}
	return nil, nil
}

func TestApplicationNetworkInstallCheckRemove(t *testing.T) {
	h, f := newApplicationNetworkFake(t)
	p := applicationNetworkTestPlan(t, applicationNetworkTestSpec())
	ctx := t.Context()
	if h.Check(ctx, p) == nil || f.fences != 0 {
		t.Fatal("check must be read only and absent initially")
	}
	if err := h.Install(ctx, p); err != nil {
		t.Fatal(err)
	}
	if err := h.Check(ctx, p); err != nil {
		t.Fatal(err)
	}
	f.calls = nil
	if err := h.Install(ctx, p); err != nil {
		t.Fatal(err)
	}
	for _, call := range f.calls {
		if !strings.HasSuffix(call, "[-S]") {
			t.Fatalf("unchanged install mutated firewall: %s", call)
		}
	}
	for i, family := range p.Families() {
		f.chains[i]["OUTPUT"] = append(f.chains[i]["OUTPUT"], append([]string(nil), family.Hook...))
	}
	if h.Check(ctx, p) == nil {
		t.Fatal("duplicate admission accepted")
	}
	if err := h.Install(ctx, p); err != nil {
		t.Fatal("duplicate admission not reconciled:", err)
	}
	// A different owner can precede our hook only when its UID is disjoint.
	s := applicationNetworkTestSpec()
	s.Owner.Resource = "another"
	s.UID = applicationAccountUID(s.Owner)
	other := applicationNetworkTestPlan(t, s)
	if err := h.Install(ctx, other); err != nil {
		t.Fatal(err)
	}
	if err := h.Check(ctx, p); err != nil {
		t.Fatal(err)
	}
	s = applicationNetworkTestSpec()
	s.Profile = ApplicationNetworkLoopback
	loop := applicationNetworkTestPlan(t, s)
	if err := h.Install(ctx, loop); err != nil || h.Check(ctx, p) == nil {
		t.Fatalf("profile reconciliation: %v", err)
	}
	if err := h.Remove(ctx, loop); err != nil {
		t.Fatal(err)
	}
	if err := h.Remove(ctx, loop); err != nil {
		t.Fatal("remove not idempotent:", err)
	}
	if err := h.Check(ctx, other); err != nil {
		t.Fatal("unrelated owner damaged:", err)
	}
	for _, c := range f.chains {
		if !reflect.DeepEqual(c["unrelated"], [][]string{{"-j", "RETURN"}}) || !reflect.DeepEqual(c["OUTPUT"][len(c["OUTPUT"])-1], []string{"-m", "conntrack", "--ctstate", "ESTABLISHED,RELATED", "-j", "ACCEPT"}) {
			t.Fatal("unrelated host policy changed")
		}
	}
}

func TestApplicationNetworkFailureAndCleanup(t *testing.T) {
	p := applicationNetworkTestPlan(t, applicationNetworkTestSpec())
	for _, point := range []string{"append", "activate", "check", "cancelled"} {
		t.Run(point, func(t *testing.T) {
			h, f := newApplicationNetworkFake(t)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			failed := false
			f.fail = func(i int, a []string) bool {
				fail := i == 1 && ((point == "append" && a[0] == "-A") || (point == "activate" && a[0] == "-D" && a[1] != "OUTPUT") || (point == "check" && a[0] == "-S" && len(f.chains[1][p.Families()[1].Chain]) > 1) || (point == "cancelled" && a[0] == "-A"))
				if fail && !failed {
					failed = true
					if point == "cancelled" {
						cancel()
					}
					return true
				}
				return false
			}
			if err := h.Install(ctx, p); err == nil || !failed || !f.fenced {
				t.Fatalf("failure did not retain fence: %v", err)
			}
			for _, c := range f.chains {
				rules := c[p.Families()[0].Chain]
				if len(rules) == 0 || !reflect.DeepEqual(rules[0], p.Families()[0].Rules[len(p.Families()[0].Rules)-1]) {
					t.Fatal("failure did not retain DROP guard")
				}
			}
			if h.Check(t.Context(), p) == nil {
				t.Fatal("failed installation admitted")
			}
			f.fail = nil
			if err := h.Install(t.Context(), p); err != nil {
				t.Fatal("retry failed:", err)
			}
		})
	}
}

func TestApplicationNetworkRemoveFailureRetry(t *testing.T) {
	p := applicationNetworkTestPlan(t, applicationNetworkTestSpec())
	for _, point := range []string{"hook", "rule", "chain"} {
		t.Run(point, func(t *testing.T) {
			h, f := newApplicationNetworkFake(t)
			if err := h.Install(t.Context(), p); err != nil {
				t.Fatal(err)
			}
			failed := false
			f.fail = func(i int, a []string) bool {
				fail := i == 1 && ((point == "hook" && a[0] == "-D" && a[1] == "OUTPUT") || (point == "rule" && a[0] == "-D" && a[1] != "OUTPUT") || (point == "chain" && a[0] == "-X"))
				if fail && !failed {
					failed = true
					return true
				}
				return false
			}
			if h.Remove(t.Context(), p) == nil || !failed || !f.fenced {
				t.Fatal("remove failure ignored or fence removed")
			}
			f.fail = nil
			if err := h.Remove(t.Context(), p); err != nil {
				t.Fatal("failed removal not retryable:", err)
			}
		})
	}
}

func TestApplicationNetworkRefusesUnsafeState(t *testing.T) {
	p := applicationNetworkTestPlan(t, applicationNetworkTestSpec())
	for _, kind := range []string{"fence", "missingIPv6", "foreignChain", "foreignReference", "lateHook", "wrongOrder", "extraAllow"} {
		t.Run(kind, func(t *testing.T) {
			h, f := newApplicationNetworkFake(t)
			if kind == "fence" {
				h.fence = func(context.Context, ApplicationOwner) error { return errors.New("not fenced") }
				if h.Install(t.Context(), p) == nil || h.Remove(t.Context(), p) == nil || len(f.calls) != 0 {
					t.Fatal("mutation despite failed fence")
				}
				return
			}
			if kind == "missingIPv6" {
				f.fail = func(i int, _ []string) bool { return i == 1 }
				if h.Install(t.Context(), p) == nil || len(f.chains[0]) != 2 {
					t.Fatal("missing IPv6 not fail closed")
				}
				return
			}
			chain := p.Families()[0].Chain
			if kind == "foreignChain" {
				f.chains[0][chain] = [][]string{{"-j", "ACCEPT"}}
				if h.Install(t.Context(), p) == nil || h.Remove(t.Context(), p) == nil {
					t.Fatal("claimed foreign chain")
				}
				return
			}
			if err := h.Install(t.Context(), p); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "foreignReference":
				f.chains[0]["unrelated"] = append(f.chains[0]["unrelated"], []string{"-j", chain})
			case "lateHook":
				r := f.chains[0]["OUTPUT"]
				r[0], r[1] = r[1], r[0]
			case "wrongOrder":
				r := f.chains[0][chain]
				r[1], r[len(r)-2] = r[len(r)-2], r[1]
			case "extraAllow":
				f.chains[0][chain] = append([][]string{{"-j", "ACCEPT"}}, f.chains[0][chain]...)
			}
			if h.Check(t.Context(), p) == nil {
				t.Fatal("unsafe rules admitted")
			}
			if kind == "lateHook" || kind == "wrongOrder" {
				if err := h.Install(t.Context(), p); err != nil {
					t.Fatal("could not reconcile owned drift:", err)
				}
			} else if h.Install(t.Context(), p) == nil || h.Remove(t.Context(), p) == nil {
				t.Fatal("modified unowned content")
			}
		})
	}
	if _, err := NewApplicationNetworkFirewall(ApplicationNetworkTools{IPTables: "iptables", IP6Tables: "/fixed/ip6tables"}, func(context.Context, string, ...string) ([]byte, error) { return nil, nil }, func(context.Context, ApplicationOwner) error { return nil }); err == nil {
		t.Fatal("PATH lookup allowed")
	}
	h, _ := newApplicationNetworkFake(t)
	if h.Install(t.Context(), ApplicationNetworkPlan{}) == nil || h.Remove(t.Context(), ApplicationNetworkPlan{}) == nil || h.Check(t.Context(), ApplicationNetworkPlan{}) == nil {
		t.Fatal("zero plan admitted")
	}
}
