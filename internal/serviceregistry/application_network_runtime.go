package serviceregistry

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"net/netip"
	"strconv"
	"strings"
	"time"
)

// ApplicationNetworkPolicy is a trusted host policy snapshot, not project input.
type ApplicationNetworkPolicy struct {
	Profiles    []ApplicationNetworkProfile `json:"profiles"`
	Resolvers   []string                    `json:"resolvers"`
	DeniedCIDRs []string                    `json:"denied_cidrs"`
}

func (p *ApplicationNetworkPolicy) Clone() *ApplicationNetworkPolicy {
	if p == nil {
		return nil
	}
	return &ApplicationNetworkPolicy{Profiles: append([]ApplicationNetworkProfile(nil), p.Profiles...), Resolvers: append([]string(nil), p.Resolvers...), DeniedCIDRs: append([]string(nil), p.DeniedCIDRs...)}
}

// Nil preserves legacy loopback-only admission. An explicit policy must name
// its profiles; outbound_https also requires the compiler's validated resolvers.
func ValidateApplicationNetworkPolicy(p *ApplicationNetworkPolicy) error {
	if p == nil {
		return nil
	}
	if len(p.Profiles) == 0 || len(p.Profiles) > 2 {
		return applicationError("network.policy_invalid")
	}
	owner := ApplicationOwner{ProjectID: "project_00000000000000000000000000", NodeID: "node_00000000000000000000000000", Resource: "validation"}
	seen := map[ApplicationNetworkProfile]bool{}
	for _, profile := range p.Profiles {
		if seen[profile] {
			return applicationError("network.policy_invalid")
		}
		seen[profile] = true
		if _, err := CompileApplicationNetworkPlan(ApplicationNetworkSpec{Owner: owner, UID: applicationAccountUID(owner), Profile: profile, Resolvers: p.Resolvers, DeniedCIDRs: p.DeniedCIDRs}); err != nil {
			return err
		}
	}
	return nil
}

func applicationNetworkPlanForGrant(g ApplicationGeneration, grant ApplicationGrant) (ApplicationNetworkPlan, bool, error) {
	var empty ApplicationNetworkPlan
	if !g.Owner.valid() || g.Owner != grant.Owner || g.UID != applicationAccountUID(g.Owner) {
		return empty, false, applicationError("network.owner_invalid")
	}
	profile := grant.Network
	if profile == "" {
		profile = ApplicationNetworkLoopback
	}
	policy := grant.NetworkPolicy.Clone()
	if err := ValidateApplicationNetworkPolicy(policy); err != nil {
		return empty, false, err
	}
	if policy == nil {
		if profile == ApplicationNetworkLoopback {
			return empty, false, nil
		}
		return empty, false, applicationError("network.profile_denied")
	}
	allowed := false
	for _, v := range policy.Profiles {
		allowed = allowed || profile == v
	}
	if !allowed {
		return empty, false, applicationError("network.profile_denied")
	}
	p, err := CompileApplicationNetworkPlan(ApplicationNetworkSpec{Owner: g.Owner, UID: g.UID, Profile: profile, Resolvers: policy.Resolvers, DeniedCIDRs: policy.DeniedCIDRs})
	return p, true, err
}

type applicationNetworkRuntime struct {
	tools     ApplicationNetworkTools
	run       ApplicationNetworkCommandRunner
	quiescent func(context.Context, ApplicationGeneration) error
}

func (r applicationNetworkRuntime) firewall(g ApplicationGeneration, p ApplicationNetworkPlan) (*ApplicationNetworkFirewall, error) {
	if r.run == nil || r.quiescent == nil {
		return nil, applicationError("network.backend_invalid")
	}
	// Recheck quiescence before either staging guard is removed. Cleanup guard
	// insertions must still work if this check fails or the request is cancelled.
	run := func(ctx context.Context, path string, args ...string) ([]byte, error) {
		if len(args) == 7 && args[4] == "-D" && args[5] == p.families[0].Chain && args[6] == "1" {
			if err := r.quiescent(ctx, g); err != nil {
				return nil, err
			}
		}
		return r.run(ctx, path, args...)
	}
	return NewApplicationNetworkFirewall(r.tools, run, func(ctx context.Context, owner ApplicationOwner) error {
		if owner != g.Owner {
			return applicationError("network.owner_invalid")
		}
		return r.quiescent(ctx, g)
	})
}

// check does not install, scan processes, release fences or change services.
func (r applicationNetworkRuntime) check(ctx context.Context, g ApplicationGeneration, grant ApplicationGrant) error {
	p, managed, err := applicationNetworkPlanForGrant(g, grant)
	if err != nil || !managed {
		return err
	}
	h, err := r.firewall(g, p)
	if err != nil {
		return err
	}
	return h.Check(ctx, p)
}

// ensure is called under the existing owner lock BEFORE publishing a relaxed
// unit. Exact rule equality is the retained profile/policy identity; a stored
// digest alone never proves that rules survived a firewall reload.
func (r applicationNetworkRuntime) ensure(ctx context.Context, g ApplicationGeneration, grant ApplicationGrant) error {
	p, managed, err := applicationNetworkPlanForGrant(g, grant)
	if err != nil || !managed {
		return err
	}
	h, err := r.firewall(g, p)
	if err != nil {
		return err
	}
	if err = h.Check(ctx, p); err == nil {
		return nil
	}
	// Only absence/drift of our own complete rules is repairable. Backend,
	// ownership and parse failures must not turn into an installation attempt.
	if err.Error() != "application.network.not_installed" {
		return err
	}
	if err = h.Install(ctx, p); err != nil {
		return err
	}
	if err = r.quiescent(ctx, g); err != nil {
		if h.guardTouched(ctx, p, [2]bool{true, true}) != nil {
			return applicationError("network.cleanup_failed_fence_required")
		}
		return err
	}
	return h.Check(ctx, p)
}

const applicationNetworkUnitProperties = "LoadState,ActiveState,SubState,MainPID,ControlPID,Job,TriggeredBy,NeedDaemonReload,IPAddressDeny,IPAddressAllow,PrivateNetwork,PrivateUsers,DynamicUser,NetworkNamespacePath,JoinsNamespaceOf,RestrictNamespaces,CapabilityBoundingSet,AmbientCapabilities,NoNewPrivileges,RestrictAddressFamilies,FileDescriptorStoreMax,NFileDescriptorStore"

func applicationNetworkStoppedUnit(raw []byte) error {
	if len(raw) == 0 || len(raw) > 16384 {
		return applicationError("network.unit_uncertain")
	}
	values := map[string]string{}
	for _, line := range strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n") {
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			return applicationError("network.unit_uncertain")
		}
		if _, duplicate := values[key]; duplicate {
			return applicationError("network.unit_uncertain")
		}
		values[key] = value
	}
	for _, key := range strings.Split(applicationNetworkUnitProperties, ",") {
		if _, present := values[key]; !present {
			return applicationError("network.unit_uncertain")
		}
	}
	stateOK := values["ActiveState"] == "inactive" && values["SubState"] == "dead" || values["ActiveState"] == "failed" && values["SubState"] == "failed"
	if values["LoadState"] != "loaded" || !stateOK || values["MainPID"] != "0" || values["ControlPID"] != "0" || values["Job"] != "" || values["TriggeredBy"] != "" || values["NeedDaemonReload"] != "no" {
		return applicationError("network.unit_not_quiescent")
	}
	if values["PrivateNetwork"] != "no" || values["PrivateUsers"] != "no" || values["DynamicUser"] != "no" || values["NetworkNamespacePath"] != "" || values["JoinsNamespaceOf"] != "" || values["RestrictNamespaces"] != "yes" || values["CapabilityBoundingSet"] != "" || values["AmbientCapabilities"] != "" || values["NoNewPrivileges"] != "yes" || values["FileDescriptorStoreMax"] != "0" || values["NFileDescriptorStore"] != "0" {
		return applicationError("network.namespace_or_socket_custody")
	}
	families := strings.Fields(values["RestrictAddressFamilies"])
	if len(families) == 0 || len(families) > 3 {
		return applicationError("network.namespace_or_socket_custody")
	}
	for _, family := range families {
		if family != "AF_UNIX" && family != "AF_INET" && family != "AF_INET6" {
			return applicationError("network.namespace_or_socket_custody")
		}
	}
	deny4, deny6 := false, false
	for _, value := range strings.Fields(values["IPAddressDeny"]) {
		deny4 = deny4 || value == "any" || value == "0.0.0.0/0"
		deny6 = deny6 || value == "any" || value == "::/0"
	}
	if !deny4 || !deny6 {
		return applicationError("network.template_fence_required")
	}
	for _, value := range strings.Fields(values["IPAddressAllow"]) {
		if value == "localhost" {
			continue
		}
		n, err := netip.ParsePrefix(value)
		if err != nil || n.Addr().Is4In6() || !n.Addr().IsLoopback() || n.Bits() < 8 || n.Addr().Is6() && n.Bits() != 128 {
			return applicationError("network.template_fence_required")
		}
	}
	return nil
}

// The bounded proc probe supports the native host PID/user/network namespaces,
// not containers or hidden processes. Other owners may use isolated namespaces;
// the application's UID must be absent from every visible thread and socket.
// The caller owns exclusive UID start admission under the application lock;
// observations alone cannot exclude an independent root launcher racing them.
type applicationNetworkProc struct {
	fs       fs.FS
	readlink func(string) (string, error)
}

func (p applicationNetworkProc) read(path string, limit int64) ([]byte, error) {
	f, err := p.fs.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil || int64(len(raw)) > limit {
		return nil, applicationError("network.proc_limit_or_unreadable")
	}
	return raw, nil
}

func (p applicationNetworkProc) entries(path string, limit int) ([]fs.DirEntry, error) {
	f, err := p.fs.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	dir, ok := f.(fs.ReadDirFile)
	if !ok {
		return nil, applicationError("network.proc_unreadable")
	}
	entries, err := dir.ReadDir(limit + 1)
	if err != nil && !errors.Is(err, io.EOF) || len(entries) > limit {
		return nil, applicationError("network.proc_limit_or_unreadable")
	}
	return entries, nil
}

func (p applicationNetworkProc) quiescent(ctx context.Context, uid uint32) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if p.fs == nil || p.readlink == nil || uid < 200000 || uid >= 1000000 {
		return applicationError("network.proc_invalid")
	}
	for _, name := range []string{"pid", "user", "net"} {
		a, e1 := p.readlink("self/ns/" + name)
		b, e2 := p.readlink("1/ns/" + name)
		if e1 != nil || e2 != nil || a == "" || a != b {
			return applicationError("network.host_namespace_required")
		}
	}
	mounts, err := p.read("self/mountinfo", 1<<20)
	if err != nil || !applicationNetworkVisibleProc(string(mounts)) {
		return applicationError("network.proc_visibility_uncertain")
	}
	processes, err := p.entries(".", 8192)
	if err != nil {
		return applicationError("network.proc_unreadable")
	}
	threads := 0
	for _, process := range processes {
		if _, err := strconv.ParseUint(process.Name(), 10, 32); err != nil || !process.IsDir() {
			continue
		}
		tasks, err := p.entries(process.Name()+"/task", 4096)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return applicationError("network.proc_unreadable")
		}
		for _, task := range tasks {
			threads++
			if ctx.Err() != nil || threads > 32768 {
				return applicationError("network.proc_limit_or_unreadable")
			}
			base := process.Name() + "/task/" + task.Name()
			raw, err := p.read(base+"/status", 65536)
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			if err != nil {
				return applicationError("network.proc_unreadable")
			}
			if err = applicationNetworkNoUID(raw, uid); err != nil {
				return err
			}
			// Host-visible real/effective/saved/filesystem UIDs are all checked.
			// Unrelated desktop/service namespaces do not belong to this owner.
		}
	}
	if threads == 0 {
		return applicationError("network.proc_visibility_uncertain")
	}
	for _, table := range []string{"tcp", "tcp6", "udp", "udp6", "udplite", "udplite6", "raw", "raw6", "icmp", "icmp6"} {
		if ctx.Err() != nil {
			return applicationError("network.proc_limit_or_unreadable")
		}
		raw, err := p.read("1/net/"+table, 1<<20)
		if errors.Is(err, fs.ErrNotExist) && table != "tcp" && table != "tcp6" && table != "udp" && table != "udp6" {
			continue
		}
		if err != nil || applicationNetworkNoSocketUID(raw, uid) != nil {
			return applicationError("network.socket_custody_uncertain")
		}
	}
	// These optional protocol tables have different ownership layouts. Do not
	// silently claim socket quiescence on a host that exposes an unparsed one.
	for _, table := range []string{"sctp/assocs", "sctp/eps", "dccp"} {
		if _, err := p.read("1/net/"+table, 1<<20); !errors.Is(err, fs.ErrNotExist) {
			return applicationError("network.socket_protocol_uncertain")
		}
	}
	return ctx.Err()
}

func applicationNetworkNoUID(raw []byte, uid uint32) error {
	for _, line := range strings.Split(string(raw), "\n") {
		if !strings.HasPrefix(line, "Uid:") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 5 {
			break
		}
		for _, v := range fields[1:] {
			n, err := strconv.ParseUint(v, 10, 32)
			if err != nil {
				return applicationError("network.proc_uid_uncertain")
			}
			if uint32(n) == uid {
				return applicationError("network.uid_not_quiescent")
			}
		}
		return nil
	}
	return applicationError("network.proc_uid_uncertain")
}

func applicationNetworkVisibleProc(raw string) bool {
	for _, line := range strings.Split(raw, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 10 || fields[4] != "/proc" {
			continue
		}
		before, after, ok := strings.Cut(line, " - ")
		if !ok || !strings.HasPrefix(after, "proc ") {
			return false
		}
		for _, option := range strings.Fields(before + " " + after) {
			for _, value := range strings.Split(option, ",") {
				if strings.HasPrefix(value, "hidepid=") && value != "hidepid=0" || strings.HasPrefix(value, "subset=") {
					return false
				}
			}
		}
		return true
	}
	return false
}

func applicationNetworkNoSocketUID(raw []byte, uid uint32) error {
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) == 0 || !strings.Contains(lines[0], "local_address") || !strings.Contains(lines[0], "uid") || len(lines) > 8192 {
		return applicationError("network.socket_table_uncertain")
	}
	for _, line := range lines[1:] {
		fields := strings.Fields(line)
		if len(fields) < 10 {
			return applicationError("network.socket_table_uncertain")
		}
		n, err := strconv.ParseUint(fields[7], 10, 32)
		if err != nil || uint32(n) == uid {
			return applicationError("network.uid_socket_present")
		}
	}
	return nil
}
