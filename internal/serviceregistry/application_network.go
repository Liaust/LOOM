package serviceregistry

import (
	"context"
	"encoding/csv"
	"net/netip"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type ApplicationNetworkProfile string

const (
	ApplicationNetworkLoopback      ApplicationNetworkProfile = "loopback"
	ApplicationNetworkOutboundHTTPS ApplicationNetworkProfile = "outbound_https"
)

// ApplicationNetworkSpec is trusted, resolved host policy, not an authorization
// request. The caller must admit the profile and check dedicated UID custody.
// DeniedCIDRs includes the node's public/private control-plane addresses and
// any site-specific translation prefixes; it takes precedence over DNS too.
type ApplicationNetworkSpec struct {
	Owner       ApplicationOwner
	UID         uint32
	Profile     ApplicationNetworkProfile
	Resolvers   []string
	DeniedCIDRs []string
}

type ApplicationNetworkFamilyPlan struct {
	IPv6  bool
	Chain string
	Hook  []string
	Rules [][]string
}

// ApplicationNetworkPlan has no writable exported state. Families returns a
// detached argument-vector projection; changing it cannot change enforcement.
type ApplicationNetworkPlan struct {
	owner    ApplicationOwner
	uid      uint32
	profile  ApplicationNetworkProfile
	families [2]ApplicationNetworkFamilyPlan
}

func (p ApplicationNetworkPlan) Owner() ApplicationOwner            { return p.owner }
func (p ApplicationNetworkPlan) UID() uint32                        { return p.uid }
func (p ApplicationNetworkPlan) Profile() ApplicationNetworkProfile { return p.profile }
func (p ApplicationNetworkPlan) Scope() string {
	if p.profile == ApplicationNetworkOutboundHTTPS {
		return "Public-destination TCP/443 (not Google-only), configured resolver TCP/UDP 53, and established loopback TCP replies; no public inbound ports. Host denials still apply."
	}
	return "Loopback only; no public inbound ports. Host denials still apply."
}
func (p ApplicationNetworkPlan) Families() []ApplicationNetworkFamilyPlan {
	out := make([]ApplicationNetworkFamilyPlan, len(p.families))
	for i, f := range p.families {
		out[i] = f
		out[i].Hook = append([]string(nil), f.Hook...)
		out[i].Rules = make([][]string, len(f.Rules))
		for j, r := range f.Rules {
			out[i].Rules[j] = append([]string(nil), r...)
		}
	}
	return out
}

// CompileApplicationNetworkPlan does no discovery, DNS, IO or execution.
// Resolver inputs must be unzoned IP literals, not names or URLs. Link-local,
// multicast, unspecified, mapped and reserved resolver addresses are refused.
func CompileApplicationNetworkPlan(s ApplicationNetworkSpec) (ApplicationNetworkPlan, error) {
	var p ApplicationNetworkPlan
	if !s.Owner.valid() || s.UID != applicationAccountUID(s.Owner) || (s.Profile != ApplicationNetworkLoopback && s.Profile != ApplicationNetworkOutboundHTTPS) || len(s.Resolvers) > 8 || len(s.DeniedCIDRs) > 64 {
		return p, applicationError("network.spec_invalid")
	}
	denied := make(map[string]netip.Prefix)
	for _, raw := range s.DeniedCIDRs {
		n, err := netip.ParsePrefix(raw)
		if err != nil || n.Addr().Is4In6() || n != n.Masked() {
			return p, applicationError("network.deny_invalid")
		}
		denied[n.String()] = n
	}
	resolvers := make(map[string]netip.Addr)
	for _, raw := range s.Resolvers {
		a, err := netip.ParseAddr(raw)
		if err != nil || a.Zone() != "" || a.Is4In6() || !applicationNetworkResolver(a) {
			return p, applicationError("network.resolver_invalid")
		}
		for _, n := range denied {
			if n.Contains(a) {
				return p, applicationError("network.resolver_denied")
			}
		}
		resolvers[a.String()] = a
	}
	if s.Profile == ApplicationNetworkOutboundHTTPS && len(resolvers) == 0 {
		return p, applicationError("network.resolver_required")
	}
	p.owner, p.uid, p.profile = s.Owner, s.UID, s.Profile
	chain := "LNA" + s.Owner.Instance()[3:]
	tag := "loom-net-" + s.Owner.Instance()[3:]
	for i := range p.families {
		f := &p.families[i]
		f.IPv6, f.Chain = i == 1, chain
		f.Hook = []string{"-m", "owner", "--uid-owner", strconv.FormatUint(uint64(s.UID), 10), "-m", "comment", "--comment", tag, "-j", chain}
		add := func(target string, args ...string) {
			r := append([]string(nil), args...)
			f.Rules = append(f.Rules, append(r, "-m", "comment", "--comment", tag, "-j", target))
		}
		add("DROP", "-m", "conntrack", "--ctstate", "INVALID")
		for _, key := range applicationNetworkSortedKeys(denied) {
			if denied[key].Addr().Is6() == f.IPv6 {
				add("DROP", "-d", key)
			}
		}
		loop := "127.0.0.0/8"
		if f.IPv6 {
			loop = "::1/128"
		}
		if s.Profile == ApplicationNetworkLoopback {
			add("RETURN", "-s", loop, "-d", loop, "-o", "lo")
		} else {
			// Only replies to a local listener survive, never arbitrary existing
			// private connections or public inbound sessions. No RELATED bypass.
			add("RETURN", "-s", loop, "-d", loop, "-o", "lo", "-p", "tcp", "-m", "tcp", "-m", "conntrack", "--ctstate", "ESTABLISHED", "--ctdir", "REPLY")
			for _, key := range applicationNetworkSortedKeys(resolvers) {
				a := resolvers[key]
				if a.Is6() != f.IPv6 {
					continue
				}
				bits := 32
				if f.IPv6 {
					bits = 128
				}
				for _, protocol := range []string{"tcp", "udp"} {
					add("RETURN", "-d", netip.PrefixFrom(a, bits).String(), "-p", protocol, "-m", protocol, "--dport", "53", "-m", "conntrack", "--ctstate", "NEW,ESTABLISHED", "--ctdir", "ORIGINAL")
				}
			}
			add("DROP", "-m", "addrtype", "--dst-type", "LOCAL")
			for _, cidr := range applicationNetworkReserved(f.IPv6) {
				add("DROP", "-d", cidr)
			}
			args := []string{}
			if f.IPv6 {
				// Positive global-unicast boundary also excludes future/special
				// IPv6 ranges, NAT64, mapped IPv4 and local-only addressing.
				args = append(args, "-d", "2000::/3")
			}
			add("RETURN", append(args, "-p", "tcp", "-m", "tcp", "--dport", "443", "-m", "conntrack", "--ctstate", "NEW,ESTABLISHED", "--ctdir", "ORIGINAL")...)
		}
		add("DROP")
	}
	return p, nil
}

func applicationNetworkReserved(ipv6 bool) []string {
	if ipv6 {
		return []string{"::/96", "::ffff:0.0.0.0/96", "64:ff9b::/96", "64:ff9b:1::/48", "100::/64", "2001::/23", "2001:db8::/32", "2002::/16", "3fff::/20", "fc00::/7", "fe80::/10", "fec0::/10", "ff00::/8"}
	}
	return []string{"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8", "169.254.0.0/16", "172.16.0.0/12", "192.0.0.0/24", "192.0.2.0/24", "192.88.99.0/24", "192.168.0.0/16", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "224.0.0.0/4", "240.0.0.0/4"}
}

func applicationNetworkResolver(a netip.Addr) bool {
	if !a.IsValid() || a.IsUnspecified() || a.IsMulticast() || a.IsLinkLocalUnicast() {
		return false
	}
	if a.IsLoopback() || a.IsPrivate() {
		return true
	}
	if a.Is6() && !netip.MustParsePrefix("2000::/3").Contains(a) {
		return false
	}
	for _, cidr := range applicationNetworkReserved(a.Is6()) {
		if netip.MustParsePrefix(cidr).Contains(a) {
			return false
		}
	}
	return true
}

func applicationNetworkSortedKeys[T any](m map[string]T) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

type ApplicationNetworkTools struct {
	IPTables  string
	IP6Tables string
}

// ApplicationNetworkCommandRunner executes the injected executable directly,
// with bounded output and a fixed environment (LANG=C, LC_ALL=C), never a shell.
// It must preserve errors, including missing backends/unsupported extensions.
type ApplicationNetworkCommandRunner func(context.Context, string, ...string) ([]byte, error)

// ApplicationNetworkFence must establish AND verify an owner-scoped deny fence
// covering both families, or quiesce every process/socket of this UID (including
// named actions) and prevent their restart. It must survive helper failure and
// remain effective after return, including on errors.
// Install never releases it. Only the caller may relax the template after a
// successful Install and Check under the existing application-owner lock.
type ApplicationNetworkFence func(context.Context, ApplicationOwner) error

// ApplicationNetworkFirewall is used only by the root application effect owner.
// Tool custody and cross-process serialization remain that owner's responsibility.
// The host firewall reload/boot path must retain the template fence until these
// rules have been restored; Check is a snapshot, not a firewall-change watcher.
// Its own mutex also serializes concurrent users of the same adapter.
type ApplicationNetworkFirewall struct {
	tools ApplicationNetworkTools
	run   ApplicationNetworkCommandRunner
	fence ApplicationNetworkFence
	mu    sync.Mutex
}

func NewApplicationNetworkFirewall(t ApplicationNetworkTools, run ApplicationNetworkCommandRunner, fence ApplicationNetworkFence) (*ApplicationNetworkFirewall, error) {
	if !applicationAbsolutePath(t.IPTables) || !applicationAbsolutePath(t.IP6Tables) || t.IPTables == t.IP6Tables || run == nil || fence == nil {
		return nil, applicationError("network.backend_invalid")
	}
	return &ApplicationNetworkFirewall{tools: t, run: run, fence: fence}, nil
}

func (h *ApplicationNetworkFirewall) command(ctx context.Context, f ApplicationNetworkFamilyPlan, args ...string) ([]byte, error) {
	tool := h.tools.IPTables
	if f.IPv6 {
		tool = h.tools.IP6Tables
	}
	out, err := h.run(ctx, tool, append([]string{"-w", "5", "-t", "filter"}, args...)...)
	if err != nil || len(out) > 1<<20 {
		return nil, applicationError("network.command_failed")
	}
	return out, nil
}

type applicationNetworkSnapshot struct {
	exists bool
	rules  [][]string
	output [][]string
	hooks  int
}

// Only our chains and OUTPUT admission prefix are interpreted. Other rules are
// opaque. Reading all -S output avoids treating a backend error as "not found".
func (h *ApplicationNetworkFirewall) snapshot(ctx context.Context, f ApplicationNetworkFamilyPlan) (applicationNetworkSnapshot, error) {
	var s applicationNetworkSnapshot
	out, err := h.command(ctx, f, "-S")
	if err != nil {
		return s, err
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) > 16384 {
		return s, applicationError("network.snapshot_limit")
	}
	outputPresent := false
	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			return s, applicationError("network.snapshot_invalid")
		}
		if fields[0] == "-P" && fields[1] == "OUTPUT" {
			outputPresent = true
		}
		if fields[0] == "-N" && fields[1] == f.Chain {
			s.exists = true
			continue
		}
		if fields[0] != "-A" {
			continue
		}
		var tokens []string
		if fields[1] == f.Chain || strings.Contains(line, f.Chain) || strings.HasPrefix(line, "-A OUTPUT -m owner --uid-owner ") {
			r := csv.NewReader(strings.NewReader(line))
			r.Comma, r.TrimLeadingSpace = ' ', true
			tokens, err = r.Read()
			if err != nil || len(tokens) < 2 {
				return s, applicationError("network.snapshot_invalid")
			}
		}
		if fields[1] == f.Chain {
			if !applicationNetworkTagged(tokens[2:], f.Hook[7]) {
				return s, applicationError("network.chain_conflict")
			}
			s.rules = append(s.rules, tokens[2:])
		} else if strings.Contains(line, f.Chain) && !(fields[1] == "OUTPUT" && reflect.DeepEqual(tokens[2:], f.Hook)) {
			return s, applicationError("network.reference_conflict")
		}
		if fields[1] == "OUTPUT" {
			if tokens == nil {
				tokens = []string{"-A", "OUTPUT", line}
			}
			s.output = append(s.output, tokens[2:])
			if reflect.DeepEqual(tokens[2:], f.Hook) {
				s.hooks++
			}
		}
	}
	if !outputPresent || (!s.exists && (len(s.rules) != 0 || s.hooks != 0)) || (s.exists && len(s.rules) == 0) {
		return s, applicationError("network.chain_conflict")
	}
	return s, nil
}

func applicationNetworkTagged(r []string, tag string) bool {
	for i := 0; i+3 < len(r); i++ {
		if r[i] == "-m" && r[i+1] == "comment" && r[i+2] == "--comment" && r[i+3] == tag {
			return true
		}
	}
	return false
}

func applicationNetworkHookOrdered(s applicationNetworkSnapshot, f ApplicationNetworkFamilyPlan) bool {
	if s.hooks != 1 {
		return false
	}
	for _, r := range s.output {
		if reflect.DeepEqual(r, f.Hook) {
			return true
		}
		// Other applications may precede us, but only exact, disjoint UID
		// hooks. A host ESTABLISHED/ACCEPT rule before us fails verification.
		if len(r) != 10 || r[0] != "-m" || r[1] != "owner" || r[2] != "--uid-owner" || r[4] != "-m" || r[5] != "comment" || r[6] != "--comment" || r[8] != "-j" || len(r[9]) != 27 || !strings.HasPrefix(r[9], "LNA") || r[7] != "loom-net-"+r[9][3:] {
			return false
		}
		uid, err := strconv.ParseUint(r[3], 10, 32)
		if err != nil || uid == 0 || r[3] == f.Hook[3] {
			return false
		}
	}
	return false
}

func (p ApplicationNetworkPlan) valid() bool {
	return p.owner.valid() && p.uid == applicationAccountUID(p.owner) && (p.profile == ApplicationNetworkLoopback || p.profile == ApplicationNetworkOutboundHTTPS) && len(p.families[0].Rules) > 1 && len(p.families[1].Rules) > 1
}

func (h *ApplicationNetworkFirewall) check(ctx context.Context, p ApplicationNetworkPlan, staged bool) error {
	for _, f := range p.families {
		s, err := h.snapshot(ctx, f)
		if err != nil {
			return err
		}
		rules := f.Rules
		if staged {
			rules = append([][]string{f.Rules[len(f.Rules)-1]}, rules...)
		}
		if !s.exists || !reflect.DeepEqual(s.rules, rules) || !applicationNetworkHookOrdered(s, f) {
			return applicationError("network.not_installed")
		}
	}
	return nil
}

// Check is read-only and verifies complete ordered rules and admission ahead of
// any potentially matching host OUTPUT rule, in BOTH families.
func (h *ApplicationNetworkFirewall) Check(ctx context.Context, p ApplicationNetworkPlan) error {
	if h == nil || h.run == nil || h.fence == nil || !p.valid() {
		return applicationError("network.plan_invalid")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.check(ctx, p, false)
}

func applicationNetworkFenceFailure(err error) error {
	if err != nil {
		code, ok := strings.CutPrefix(err.Error(), "application.network.")
		if ok && code != "" && len(code) <= 64 && strings.Trim(code, "abcdefghijklmnopqrstuvwxyz_") == "" {
			return applicationError("network." + code)
		}
	}
	return applicationError("network.fence_failed")
}

// Install stages behind a DROP guard and verifies both families before admitting
// either. Failure leaves the external fence in place and best-effort DROP guards
// on touched chains; a later Install can reconcile those tagged partial rules.
func (h *ApplicationNetworkFirewall) Install(ctx context.Context, p ApplicationNetworkPlan) (err error) {
	if h == nil || h.run == nil || h.fence == nil || !p.valid() {
		return applicationError("network.plan_invalid")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if err = h.fence(ctx, p.owner); err != nil {
		return applicationNetworkFenceFailure(err)
	}
	var snapshots [2]applicationNetworkSnapshot
	for i, f := range p.families {
		snapshots[i], err = h.snapshot(ctx, f)
		if err != nil {
			return err
		}
	}
	unchanged := true
	for i, f := range p.families {
		unchanged = unchanged && snapshots[i].exists && reflect.DeepEqual(snapshots[i].rules, f.Rules) && applicationNetworkHookOrdered(snapshots[i], f)
	}
	if unchanged {
		return nil
	}
	var touched [2]bool
	defer func() {
		if err == nil {
			return
		}
		if h.guardTouched(ctx, p, touched) != nil {
			err = applicationError("network.cleanup_failed_fence_required")
		}
	}()
	for i, f := range p.families {
		s := snapshots[i]
		guard := f.Rules[len(f.Rules)-1]
		if !s.exists {
			if _, err = h.command(ctx, f, "-N", f.Chain); err != nil {
				return err
			}
		}
		touched[i] = true
		if _, err = h.command(ctx, f, append([]string{"-I", f.Chain, "1"}, guard...)...); err != nil {
			return err
		}
		// Delete only observed owned rows, from the end, preserving the guard.
		for j := len(s.rules) + 1; j >= 2; j-- {
			if _, err = h.command(ctx, f, "-D", f.Chain, strconv.Itoa(j)); err != nil {
				return err
			}
		}
		for j := 0; j < s.hooks; j++ {
			if _, err = h.command(ctx, f, append([]string{"-D", "OUTPUT"}, f.Hook...)...); err != nil {
				return err
			}
		}
		if _, err = h.command(ctx, f, append([]string{"-I", "OUTPUT", "1"}, f.Hook...)...); err != nil {
			return err
		}
		for _, r := range f.Rules {
			if _, err = h.command(ctx, f, append([]string{"-A", f.Chain}, r...)...); err != nil {
				return err
			}
		}
	}
	if err = h.check(ctx, p, true); err != nil {
		return err
	}
	for _, f := range p.families {
		if _, err = h.command(ctx, f, "-D", f.Chain, "1"); err != nil {
			return err
		}
	}
	return h.check(ctx, p, false)
}

func (h *ApplicationNetworkFirewall) guardTouched(ctx context.Context, p ApplicationNetworkPlan, touched [2]bool) error {
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	var err error
	for i, f := range p.families {
		if touched[i] {
			if _, e := h.command(cleanup, f, append([]string{"-I", f.Chain, "1"}, f.Rules[len(f.Rules)-1]...)...); e != nil {
				err = e
			}
		}
	}
	return err
}

// Remove requires a verified retained fence BEFORE removing any enforcement.
// It removes only this owner's exact hooks and tagged chain rows. On error the
// caller must retain the fence; neither Remove nor Install ever releases it.
func (h *ApplicationNetworkFirewall) Remove(ctx context.Context, p ApplicationNetworkPlan) (err error) {
	if h == nil || h.run == nil || h.fence == nil || !p.valid() {
		return applicationError("network.plan_invalid")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if err = h.fence(ctx, p.owner); err != nil {
		return applicationNetworkFenceFailure(err)
	}
	var snapshots [2]applicationNetworkSnapshot
	for i, f := range p.families {
		snapshots[i], err = h.snapshot(ctx, f)
		if err != nil {
			return err
		}
	}
	var touched [2]bool
	defer func() {
		if err != nil && h.guardTouched(ctx, p, touched) != nil {
			err = applicationError("network.cleanup_failed_fence_required")
		}
	}()
	for i, f := range p.families {
		s := snapshots[i]
		if !s.exists {
			continue
		}
		touched[i] = true
		for j := 0; j < s.hooks; j++ {
			if _, err := h.command(ctx, f, append([]string{"-D", "OUTPUT"}, f.Hook...)...); err != nil {
				return err
			}
		}
		for j := len(s.rules); j > 0; j-- {
			if _, err := h.command(ctx, f, "-D", f.Chain, strconv.Itoa(j)); err != nil {
				return err
			}
		}
		if _, err := h.command(ctx, f, "-X", f.Chain); err != nil {
			return err
		}
		touched[i] = false
	}
	for _, f := range p.families {
		s, err := h.snapshot(ctx, f)
		if err != nil {
			return err
		}
		if s.exists || s.hooks != 0 {
			return applicationError("network.remove_incomplete")
		}
	}
	return nil
}
