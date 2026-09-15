//go:build linux

package serviceregistry

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

func applicationPeerUID(fd int) (uint32, error) {
	p, e := unix.GetsockoptUcred(fd, unix.SOL_SOCKET, unix.SO_PEERCRED)
	if e != nil {
		return 0, e
	}
	return p.Uid, nil
}
func (c ApplicationHelperClient) Execute(ctx context.Context, q ApplicationRuntimeRequest) (ApplicationRuntimeReceipt, error) {
	response, e := c.exchange(ctx, ApplicationHelperEnvelope{Request: &q})
	if response.Receipt == nil {
		return ApplicationRuntimeReceipt{}, e
	}
	return *response.Receipt, e
}
func (c ApplicationHelperClient) Publish(ctx context.Context, p ApplicationPublication) error {
	_, e := c.exchange(ctx, ApplicationHelperEnvelope{Publication: &p})
	return e
}
func (c ApplicationHelperClient) PublishPrepared(ctx context.Context, p ApplicationPreparedPublication) error {
	_, err := c.exchange(ctx, ApplicationHelperEnvelope{Prepared: &p})
	return err
}
func (c ApplicationHelperClient) exchange(ctx context.Context, envelope ApplicationHelperEnvelope) (ApplicationHelperResponse, error) {
	var response ApplicationHelperResponse
	path := c.SocketPath
	if path == "" {
		path = ApplicationSocketPath
	}
	if !applicationAbsolutePath(path) || applicationCheckParents(path, 0) != nil {
		return response, applicationError("socket.custody")
	}
	var st unix.Stat_t
	if unix.Lstat(path, &st) != nil || st.Uid != 0 || st.Mode&unix.S_IFMT != unix.S_IFSOCK || st.Mode&0007 != 0 {
		return response, applicationError("socket.custody")
	}
	conn, e := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "unix", path)
	if e != nil {
		return response, applicationError("socket.unavailable")
	}
	defer conn.Close()
	unixConn, ok := conn.(*net.UnixConn)
	if !ok {
		return response, applicationError("socket.type")
	}
	raw, e := unixConn.SyscallConn()
	if e != nil {
		return response, applicationError("socket.peer")
	}
	var peer uint32
	var peerErr error
	if raw.Control(func(fd uintptr) { peer, peerErr = applicationPeerUID(int(fd)) }) != nil || peerErr != nil || peer != 0 {
		return response, applicationError("socket.activator_peer")
	}
	deadline := time.Now().Add(90 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = conn.SetDeadline(deadline)
	payload, e := json.Marshal(envelope)
	if e != nil || len(payload) > applicationMaxBytes {
		return response, applicationError("request.size")
	}
	if _, e = conn.Write(append(payload, '\n')); e != nil {
		return response, applicationError("socket.delivery_uncertain")
	}
	_ = unixConn.CloseWrite()
	result, e := io.ReadAll(io.LimitReader(conn, applicationMaxBytes+1))
	if e != nil || applicationDecodeJSON(result, &response) != nil {
		return response, applicationError("socket.delivery_uncertain")
	}
	if response.ErrorCode != "" {
		return response, errors.New(response.ErrorCode)
	}
	return response, nil
}

// An Accept=yes service receives the already-connected stream as fd0/fd1.
// SO_PEERCRED here identifies the node-agent caller, not pid1's listener UID.
func applicationHelperRuntime(configPath string, fixture bool) (ApplicationRuntime, error) {
	if os.Geteuid() != 0 || os.Getuid() != 0 {
		return ApplicationRuntime{}, applicationError("helper.root_required")
	}
	if !fixture && configPath != "/etc/loom/project-applications-helper.json" {
		return ApplicationRuntime{}, applicationError("helper.fixed_config")
	}
	resolvedConfig, resolveErr := filepath.EvalSymlinks(configPath)
	if resolveErr != nil || (resolvedConfig != configPath && !strings.HasPrefix(resolvedConfig, "/nix/store/")) || applicationImmutableParents(resolvedConfig, 0) != nil || applicationCustody(resolvedConfig, 0, false) != nil {
		return ApplicationRuntime{}, applicationError("helper.config_custody")
	}
	raw, e := os.ReadFile(resolvedConfig)
	if e != nil {
		return ApplicationRuntime{}, applicationError("helper.config")
	}
	var cfg ApplicationHelperConfig
	if applicationDecodeJSON(raw, &cfg) != nil {
		return ApplicationRuntime{}, applicationError("helper.config")
	}
	for path := range cfg.TrustedParentOwners {
		if !applicationAbsolutePath(path) || path == "/" {
			return ApplicationRuntime{}, applicationError("helper.path")
		}
	}
	if !fixture && (cfg.PolicyPath != ApplicationPolicyPath || cfg.StateRoot != "/var/lib/loom-project-applications" || cfg.ConfigRoot != "/var/lib/loom-project-application-configs" || cfg.UnitRoot != "/run/systemd/system" || cfg.GCRoot != "/nix/var/nix/gcroots/loom-project-applications") {
		return ApplicationRuntime{}, applicationError("helper.fixed_paths")
	}
	for _, p := range []string{cfg.PolicyPath, cfg.StateRoot, cfg.ConfigRoot, cfg.UnitRoot, cfg.GCRoot, cfg.Systemctl, cfg.Sysusers, cfg.Nix} {
		if !applicationAbsolutePath(p) {
			return ApplicationRuntime{}, applicationError("helper.path")
		}
	}
	for _, p := range []string{cfg.Systemctl, cfg.Sysusers, cfg.Nix} {
		resolved, e := filepath.EvalSymlinks(p)
		if e != nil || applicationImmutableParents(resolved, 0) != nil || applicationCustody(resolved, 0, false) != nil {
			return ApplicationRuntime{}, applicationError("helper.tool_custody")
		}
	}
	if !fixture && cfg.FixtureFaultPath != "" {
		return ApplicationRuntime{}, applicationError("helper.fixture_forbidden")
	}
	var hook func(string) error
	if fixture && cfg.FixtureFaultPath != "" {
		if !applicationAbsolutePath(cfg.FixtureFaultPath) || filepath.Base(cfg.FixtureFaultPath) != "fault.json" || !strings.HasPrefix(cfg.FixtureFaultPath, "/run/loom-application-acceptance-") || applicationCheckParents(cfg.FixtureFaultPath, 0) != nil {
			return ApplicationRuntime{}, applicationError("helper.fixture_custody")
		}
		hook = applicationFixtureCrashHook(cfg.FixtureFaultPath)
	}
	system := &ApplicationHostSystem{Config: cfg, FailureHook: hook}
	runtime := ApplicationRuntime{Store: ApplicationStateStore{Root: cfg.StateRoot, OwnerUID: 0, TrustedParentOwners: cfg.TrustedParentOwners}, PolicyPath: cfg.PolicyPath, System: system, FailureHook: hook}
	if cfg.PassCLI != "" || cfg.PassSessionEnsure != "" {
		for _, path := range []string{cfg.PassCLI, cfg.PassSessionEnsure} {
			resolved, err := filepath.EvalSymlinks(path)
			if !applicationAbsolutePath(path) || err != nil || !strings.HasPrefix(resolved, "/nix/store/") || applicationImmutableParents(resolved, 0) != nil || applicationCustody(resolved, 0, false) != nil {
				return ApplicationRuntime{}, applicationError("credential.tool_custody")
			}
		}
		runtime.Credentials = applicationProtonResolver{CLI: cfg.PassCLI, SessionEnsure: cfg.PassSessionEnsure, Run: applicationProtonCommand}
	}
	return runtime, nil
}

func ServeApplicationHelper(configPath string, fixture bool) error {
	runtime, e := applicationHelperRuntime(configPath, fixture)
	if e != nil {
		return e
	}

	peer, e := applicationPeerUID(0)
	if e != nil {
		return applicationError("helper.caller_peer")
	}
	_ = unix.SetsockoptTimeval(0, unix.SOL_SOCKET, unix.SO_RCVTIMEO, &unix.Timeval{Sec: 10})
	raw, e := io.ReadAll(io.LimitReader(os.Stdin, applicationMaxBytes+1))
	if e != nil {
		return applicationError("request.read")
	}
	envelope, e := decodeApplicationPrerequisiteEnvelope(raw)
	if e != nil {
		return e
	}
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Second)
	defer cancel()
	response := ApplicationHelperResponse{}
	if envelope.Prepared != nil {
		e = runtime.PublishPrepared(ctx, peer, *envelope.Prepared)
	} else if envelope.ProvisionPlan != nil {
		plan, err := runtime.PlanProvision(ctx, peer, *envelope.ProvisionPlan)
		e = err
		if err == nil {
			response.Provision = &plan
		}
	} else if envelope.Provision != nil {
		plan, err := runtime.Provision(ctx, peer, *envelope.Provision)
		e = err
		if err == nil {
			response.Provision = &plan
		}
	} else if envelope.Prerequisites != nil {
		snapshot, err := runtime.QueryPrerequisites(ctx, peer, *envelope.Prerequisites)
		e = err
		if err == nil {
			response.Prerequisites = &snapshot
		}
	} else if envelope.Archive != nil {
		manager, err := runtime.ArchiveControl(ctx, peer, *envelope.Archive)
		e = err
		response.Manager = &manager
	} else if envelope.Publication != nil {
		e = runtime.Publish(ctx, peer, *envelope.Publication)
	} else {
		receipt, err := runtime.Execute(ctx, peer, *envelope.Request)
		e = err
		response.Receipt = &receipt
	}
	if e != nil {
		response.ErrorCode = applicationSafeError(e)
	}
	return json.NewEncoder(os.Stdout).Encode(response)
}

type ApplicationHostSystem struct {
	Config      ApplicationHelperConfig
	FailureHook func(string) error
}

func (h *ApplicationHostSystem) command(ctx context.Context, path string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Env = []string{"PATH=/nonexistent", "LANG=C", "LC_ALL=C", "HOME=/var/empty"}
	out := limitedBuffer{limit: applicationMaxBytes}
	cmd.Stdout = &out
	cmd.Stderr = io.Discard
	if cmd.Run() != nil || out.truncated {
		return nil, applicationError("host.command_failed")
	}
	return out.Bytes(), nil
}
func (h *ApplicationHostSystem) VerifyArtifact(ctx context.Context, d ApplicationArtifactDescriptor) error {
	if e := d.Validate(); e != nil {
		return e
	}
	// Verification and path-info cannot evaluate/build a flake or select caches.
	if _, e := h.command(ctx, h.Config.Nix, "--offline", "--extra-experimental-features", "nix-command", "store", "verify", "--no-trust", "--recursive", d.StoreRoot); e != nil {
		return applicationError("artifact.realized_verification_prerequisite")
	}
	raw, e := h.command(ctx, h.Config.Nix, "--offline", "--extra-experimental-features", "nix-command", "path-info", "--json", "--recursive", d.StoreRoot)
	if e != nil {
		return applicationError("artifact.closure_unavailable")
	}
	var entries map[string]struct {
		NARHash string `json:"narHash"`
	}
	if json.Unmarshal(raw, &entries) != nil {
		return applicationError("artifact.closure_format")
	}
	paths := make([]ApplicationClosurePath, 0, len(entries))
	for p, v := range entries {
		paths = append(paths, ApplicationClosurePath{Path: p, NARHash: v.NARHash})
	}
	sort.Slice(paths, func(i, j int) bool { return paths[i].Path < paths[j].Path })
	if applicationSHA(paths) != d.ClosureDigest {
		return applicationError("artifact.closure_mismatch")
	}
	for _, p := range []string{d.Launcher, d.Executable} {
		resolved, e := filepath.EvalSymlinks(p)
		if e != nil {
			return applicationError("artifact.executable_missing")
		}
		inClosure := false
		for _, c := range paths {
			inClosure = inClosure || strings.HasPrefix(resolved, c.Path+"/")
		}
		if !inClosure || applicationImmutableParents(resolved, 0) != nil || applicationCustody(resolved, 0, false) != nil {
			return applicationError("artifact.executable_custody")
		}
		info, e := os.Stat(resolved)
		if e != nil || info.Mode().Perm()&0111 == 0 {
			return applicationError("artifact.executable_missing")
		}
	}
	return nil
}
func (h *ApplicationHostSystem) EnsureAccount(ctx context.Context, o ApplicationOwner, uid uint32, _ bool) error {
	store := ApplicationStateStore{Root: h.Config.StateRoot, OwnerUID: 0}
	release, e := store.lock(ctx, "account:"+strconv.FormatUint(uint64(uid), 10))
	if e != nil {
		return e
	}
	defer release()
	key := "account-" + strconv.FormatUint(uint64(uid), 10)
	var claimed ApplicationOwner
	claimErr := store.read(key, &claimed)
	if claimErr == nil && claimed != o {
		return applicationError("account.collision")
	}
	if claimErr != nil && !os.IsNotExist(claimErr) {
		return claimErr
	}
	name := o.Instance()
	id := strconv.FormatUint(uint64(uid), 10)
	byName, nameErr := user.Lookup(name)
	byID, idErr := user.LookupId(id)
	group, groupErr := user.LookupGroupId(id)
	namedGroup, namedGroupErr := user.LookupGroup(name)

	if (nameErr == nil || idErr == nil || groupErr == nil || namedGroupErr == nil) && claimErr != nil {
		return applicationError("account.collision")
	}
	if nameErr == nil && (byName.Username != name || byName.Uid != id || byName.Gid != id || byName.HomeDir != "/var/empty") {
		return applicationError("account.collision")
	}
	if idErr == nil && (byID.Username != name || byID.Uid != id || byID.Gid != id || byID.HomeDir != "/var/empty") {
		return applicationError("account.collision")
	}
	if groupErr == nil && (group.Name != name || group.Gid != id) {
		return applicationError("account.collision")
	}
	if namedGroupErr == nil && (namedGroup.Name != name || namedGroup.Gid != id) {
		return applicationError("account.collision")
	}
	if nameErr != nil {
		if _, ok := nameErr.(user.UnknownUserError); !ok {
			return applicationError("account.lookup")
		}
	}
	if idErr != nil {
		if _, ok := idErr.(user.UnknownUserIdError); !ok {
			return applicationError("account.lookup")
		}
	}
	if groupErr != nil {
		if _, ok := groupErr.(user.UnknownGroupIdError); !ok {
			return applicationError("account.lookup")
		}
	}
	if namedGroupErr != nil {
		if _, ok := namedGroupErr.(user.UnknownGroupError); !ok {
			return applicationError("account.lookup")
		}
	}
	if nameErr == nil && idErr == nil && groupErr == nil && namedGroupErr == nil {
		return nil
	}
	// A claimed matching group left by an interrupted sysusers write may be
	// completed. An existing unclaimed or mismatched user/group is never adopted.

	if claimErr != nil {
		if e = store.write(key, o); e != nil {
			return e
		}
	}
	// Claim is durable before sysusers and is never recycled. Unknown delivery
	// reconciles the exact username, UID and GID against this root-owned claim.
	cmd := exec.CommandContext(ctx, h.Config.Sysusers, "-")
	cmd.Env = []string{"PATH=/nonexistent", "LANG=C"}
	cmd.Stdin = strings.NewReader(fmt.Sprintf("g %s %s\nu %s %s \"LOOM application\" /var/empty /run/current-system/sw/bin/nologin\n", name, id, name, id+":"+id))
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if cmd.Run() != nil {
		return applicationError("account.create")
	}
	account, e := user.Lookup(name)
	if e != nil || account.Uid != id || account.Gid != id {
		return applicationError("account.postcondition")
	}
	return nil
}

func (h *ApplicationHostSystem) EnsureData(ctx context.Context, p ApplicationDataPolicy, uid uint32, prior *ApplicationDataIdentity) (ApplicationDataIdentity, error) {
	if applicationCheckTrustedParents(p.Path, 0, h.Config.TrustedParentOwners) != nil {
		return ApplicationDataIdentity{}, applicationError("data.parent_custody")
	}
	if prior == nil {
		store := ApplicationStateStore{Root: h.Config.StateRoot, OwnerUID: 0, TrustedParentOwners: h.Config.TrustedParentOwners}
		if err := store.ensureClaimedDirectory(ctx, p.Path, "data:"+applicationSHA(p), uid, uid, 0700, false, h.FailureHook); err != nil {
			return ApplicationDataIdentity{}, err
		}
	}
	actual, err := applicationDataIdentity(p.Path, p.Pool, uid)
	if err != nil {
		return actual, err
	}
	if prior != nil && !applicationSameDataIdentity(*prior, actual) {
		return actual, applicationError("data.identity_migration_required")
	}
	return actual, nil
}
func applicationRenameDirectoryNoReplace(from, to string) error {
	return unix.Renameat2(unix.AT_FDCWD, from, unix.AT_FDCWD, to, unix.RENAME_NOREPLACE)
}

func (h *ApplicationHostSystem) generationDir(g ApplicationGeneration) string {
	return filepath.Join(h.Config.ConfigRoot, g.Owner.Instance(), g.ID)
}
func (h *ApplicationHostSystem) ConfigurationPath(g ApplicationGeneration) string {
	return filepath.Join(h.generationDir(g), "config.json")
}
func (h *ApplicationHostSystem) dropIn(g ApplicationGeneration) string {
	return filepath.Join(h.Config.UnitRoot, g.Owner.Unit()+".d", "50-loom-application.conf")
}
func (h *ApplicationHostSystem) PublishGeneration(ctx context.Context, g ApplicationGeneration) error {

	// Set precise ownership/modes after mkdir; the rendered helper has UMask0077.
	// Its receipt root stays private. Applications traverse only their own group
	// directory and read only their immutable config, never another app's files.
	for _, spec := range []struct {
		path string
		gid  uint32
		mode os.FileMode
	}{
		{h.Config.ConfigRoot, 0, 0711}, {filepath.Dir(h.generationDir(g)), g.UID, 0710}, {h.generationDir(g), g.UID, 0710}, {h.Config.GCRoot, 0, 0755}, {filepath.Dir(h.dropIn(g)), 0, 0755},
	} {
		infrastructure := spec.path == h.Config.ConfigRoot || spec.path == h.Config.GCRoot
		store := ApplicationStateStore{Root: h.Config.StateRoot, OwnerUID: 0}
		epoch := ""
		if spec.path == filepath.Dir(h.dropIn(g)) {
			// /run is a new filesystem/directory at boot. Keep claims within that
			// exact volatile parent epoch; never relax persistent data/config custody.
			var parent unix.Stat_t
			fs, e := applicationFilesystem(h.Config.UnitRoot)
			if e != nil || unix.Lstat(h.Config.UnitRoot, &parent) != nil {
				return applicationError("generation.unit_parent")
			}
			epoch = applicationSHA([]any{fs, parent.Ino})
		}
		if e := store.ensureClaimedDirectoryEpoch(ctx, spec.path, "generation:"+spec.path, 0, spec.gid, spec.mode, infrastructure, epoch, h.FailureHook); e != nil {
			return e
		}
	}

	// Retain every introduced closure root; retirement never prunes recovery roots.
	descriptors := []ApplicationArtifactDescriptor{g.Descriptor}
	if g.Previous != nil {
		descriptors = append(descriptors, *g.Previous)
	}
	for _, d := range descriptors {
		path := filepath.Join(h.Config.GCRoot, g.Owner.Instance()+"-"+applicationSHA(d)[7:])
		if target, e := os.Readlink(path); e == nil {
			if target != d.StoreRoot {
				return applicationError("artifact.gc_root_conflict")
			}
		} else {
			if e = os.Symlink(d.StoreRoot, path); e != nil {
				return applicationError("artifact.gc_root")
			}
			if e = applicationSyncDir(h.Config.GCRoot); e != nil {
				return e
			}
		}
	}

	if e := applicationAtomicGroup(h.ConfigurationPath(g), g.Config, 0440, int(g.UID)); e != nil {
		return e
	}

	if e := applicationAtomic(h.dropIn(g), []byte(g.DropIn), 0444); e != nil {
		return e
	}
	if h.FailureHook != nil {
		if e := h.FailureHook("generation:files_published"); e != nil {
			return e
		}
	}
	_, e := h.command(ctx, h.Config.Systemctl, "daemon-reload")
	if e == nil && h.FailureHook != nil {
		e = h.FailureHook("generation:manager_loaded")
	}
	return e
}
func (h *ApplicationHostSystem) GenerationPublished(g ApplicationGeneration) bool {
	b, e := os.ReadFile(h.dropIn(g))
	if e != nil || !bytes.Equal(b, []byte(g.DropIn)) {
		return false
	}
	b, e = os.ReadFile(h.ConfigurationPath(g))
	if e != nil || !bytes.Equal(b, g.Config) {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	loaded, e := h.command(ctx, h.Config.Systemctl, "show", g.Owner.Unit(), "--property=NeedDaemonReload,Environment,ExecStart")
	return e == nil && applicationLoadedGeneration(loaded, g, h.ConfigurationPath(g))
}

func (h *ApplicationHostSystem) Observe(ctx context.Context, g ApplicationGeneration) (ApplicationProcessObservation, error) {
	p := ApplicationProcessObservation{State: "unknown"}
	raw, e := h.command(ctx, h.Config.Systemctl, "show", g.Owner.Unit(), "--property=ActiveState,MainPID,InvocationID,ControlGroup")
	if e != nil {
		return p, e
	}
	for _, line := range strings.Split(string(raw), "\n") {
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		switch k {
		case "ActiveState":
			p.State = v
		case "MainPID":
			p.PID, _ = strconv.Atoi(v)
		case "InvocationID":
			p.InvocationID = v
		case "ControlGroup":
			p.ControlGroup = v
		}
	}
	if p.State != "active" || p.PID <= 0 {
		return p, nil
	}
	proc := filepath.Join("/proc", strconv.Itoa(p.PID))
	p.Executable, e = os.Readlink(filepath.Join(proc, "exe"))
	if e != nil {
		return p, applicationError("process.identity")
	}
	status, e := os.ReadFile(filepath.Join(proc, "status"))
	if e != nil {
		return p, applicationError("process.identity")
	}
	for _, line := range strings.Split(string(status), "\n") {
		if strings.HasPrefix(line, "Uid:") {
			fields := strings.Fields(line)
			if len(fields) != 5 {
				return p, applicationError("process.uid")
			}
			uid, e := strconv.ParseUint(fields[2], 10, 32)
			if e != nil {
				return p, applicationError("process.uid")
			}
			p.UID = uint32(uid)
		}
	}
	cgroup, e := os.ReadFile(filepath.Join(proc, "cgroup"))
	if e != nil || !strings.Contains(string(cgroup), "0::"+p.ControlGroup+"\n") {
		return p, applicationError("process.cgroup")
	}
	// Only the generation marker is selected; other environment bytes are never
	// returned, persisted, logged or used as credential input.
	f, e := os.Open(filepath.Join(proc, "environ"))
	if e != nil {
		return p, applicationError("process.generation")
	}
	environment, e := io.ReadAll(io.LimitReader(f, 128*1024))
	f.Close()
	if e != nil {
		return p, applicationError("process.generation")
	}
	for _, item := range bytes.Split(environment, []byte{0}) {
		if bytes.HasPrefix(item, []byte("LOOM_APPLICATION_GENERATION=")) {
			p.Generation = strings.TrimPrefix(string(item), "LOOM_APPLICATION_GENERATION=")
		}
	}
	if g.Listener != nil {
		p.ListenerOwned = applicationPIDOwnsListener(proc, *g.Listener)
	}
	return p, nil
}
func (h *ApplicationHostSystem) Restart(ctx context.Context, g ApplicationGeneration) error {
	_, e := h.command(ctx, h.Config.Systemctl, "restart", g.Owner.Unit())
	return e
}
func (h *ApplicationHostSystem) Start(ctx context.Context, g ApplicationGeneration) error {
	_, err := h.command(ctx, h.Config.Systemctl, "start", g.Owner.Unit())
	return err
}
func (h *ApplicationHostSystem) Stop(ctx context.Context, g ApplicationGeneration) error {
	_, e := h.command(ctx, h.Config.Systemctl, "stop", g.Owner.Unit())
	return e
}
func (h *ApplicationHostSystem) RemoveManagement(ctx context.Context, g ApplicationGeneration) error {
	if e := os.Remove(h.dropIn(g)); e != nil && !os.IsNotExist(e) {
		return applicationError("retire.dropin")
	}
	if e := applicationSyncDir(filepath.Dir(h.dropIn(g))); e != nil && !os.IsNotExist(e) {
		return e
	}
	if h.Config.EdgeRoot != "" {
		if e := h.PublishEdge(ctx, g, nil); e != nil {
			return e
		}
	}
	_, e := h.command(ctx, h.Config.Systemctl, "daemon-reload")
	return e
}
func (h *ApplicationHostSystem) PublishEdge(ctx context.Context, g ApplicationGeneration, fragment []byte) error {
	if h.Config.EdgeRoot == "" || h.Config.Caddy == "" || h.Config.CaddyConfig == "" {
		return applicationError("edge.delivery_prerequisite")
	}
	if applicationCheckParents(h.Config.EdgeRoot, 0) != nil || applicationCustody(h.Config.EdgeRoot, 0, true) != nil {
		return applicationError("edge.custody")
	}
	// Caddy is one shared composed configuration: serialize fragment publication
	// and validation separately from per-application execution (no global app lock).
	release, e := applicationFileLock(ctx, filepath.Join(h.Config.StateRoot, "edge.lock"), 0)
	if e != nil {
		return e
	}
	defer release()
	path := filepath.Join(h.Config.EdgeRoot, g.Owner.Instance()+".caddy")
	old, oldErr := os.ReadFile(path)
	if oldErr != nil && !os.IsNotExist(oldErr) {
		return applicationError("edge.read")
	}
	if len(fragment) == 0 {
		e = os.Remove(path)
		if os.IsNotExist(e) {
			e = nil
		}
	} else {
		e = applicationAtomic(path, fragment, 0644)
	}
	if e != nil {
		return applicationError("edge.publish")
	}
	if _, e = h.command(ctx, h.Config.Caddy, "validate", "--config", h.Config.CaddyConfig, "--adapter", "caddyfile"); e != nil {
		if oldErr == nil {
			_ = applicationAtomic(path, old, 0644)
		} else {
			_ = os.Remove(path)
		}
		return applicationError("edge.validation")
	}
	if _, e = h.command(ctx, h.Config.Systemctl, "reload", "caddy.service"); e != nil {
		return applicationError("edge.reload_uncertain")
	}
	return applicationSyncDir(h.Config.EdgeRoot)
}

func (c ApplicationHelperClient) Archive(ctx context.Context, q ApplicationArchiveControl) (ManagerResult, error) {
	response, e := c.exchange(ctx, ApplicationHelperEnvelope{Archive: &q})
	if response.Manager == nil {
		return ManagerResult{}, e
	}
	return *response.Manager, e
}

// Correlate the loopback LISTEN inode to the exact main process' descriptors.
// A healthy unrelated process occupying the intended port is never sufficient.
func applicationPIDOwnsListener(proc string, listener ApplicationListener) bool {
	expectedAddress := "0100007F"
	table := "tcp"
	if listener.Address == "::1" {
		expectedAddress = "00000000000000000000000001000000"
		table = "tcp6"
	} else if listener.Address != "127.0.0.1" {
		return false
	}
	raw, e := os.ReadFile(filepath.Join(proc, "net", table))
	if e != nil || len(raw) > 1024*1024 {
		return false
	}
	inodes := map[string]bool{}
	for _, line := range strings.Split(string(raw), "\n") {
		fields := strings.Fields(line)
		if len(fields) > 9 && fields[1] == fmt.Sprintf("%s:%04X", expectedAddress, listener.Port) && fields[3] == "0A" {
			inodes[fields[9]] = true
		}
	}
	fds, e := os.ReadDir(filepath.Join(proc, "fd"))
	if e != nil || len(fds) > 4096 {
		return false
	}
	for _, fd := range fds {
		link, e := os.Readlink(filepath.Join(proc, "fd", fd.Name()))
		if e == nil && strings.HasPrefix(link, "socket:[") && strings.HasSuffix(link, "]") && inodes[strings.TrimSuffix(strings.TrimPrefix(link, "socket:["), "]")] {
			return true
		}
	}
	return false
}
func applicationMountID(path string) (uint64, error) {
	var st unix.Statx_t
	if unix.Statx(unix.AT_FDCWD, path, unix.AT_SYMLINK_NOFOLLOW, unix.STATX_MNT_ID, &st) != nil || st.Mask&unix.STATX_MNT_ID == 0 {
		return 0, applicationError("data.mount_identity_unavailable")
	}
	return st.Mnt_id, nil
}

func applicationFixtureNoNewPrivileges() error {
	return unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0)
}

// One-shot actual process termination, selected only by root-owned FIXTURE
// startup configuration. The application request has no fault/test selector.
func applicationFixtureCrashHook(path string) func(string) error {
	return func(stage string) error {
		if e := applicationCustody(path, 0, false); os.IsNotExist(e) {
			return nil
		} else if e != nil {
			return applicationError("fixture.fault_custody")
		}
		raw, e := os.ReadFile(path)
		if e != nil {
			return applicationError("fixture.fault_read")
		}
		var fault struct {
			Stage string `json:"stage"`
		}
		if applicationDecodeJSON(raw, &fault) != nil {
			return applicationError("fixture.fault_invalid")
		}
		if fault.Stage != stage {
			return nil
		}
		if e = os.Rename(path, path+".consumed"); e != nil {
			return applicationError("fixture.fault_consume")
		}
		if e = applicationSyncDir(filepath.Dir(path)); e != nil {
			return e
		}
		os.Exit(75)
		return nil
	}
}

// Fixed root-only module boot entry point. It is never exposed on the installer
// socket and cannot select an arbitrary owner, publication or operation token.
func RestoreApplicationHelper(configPath string, fixture bool) error {
	runtime, err := applicationHelperRuntime(configPath, fixture)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	observations, err := runtime.RestoreCommitted(ctx)
	if writeErr := json.NewEncoder(os.Stdout).Encode(observations); writeErr != nil {
		return writeErr
	}
	return err
}

func (c ApplicationHelperClient) QueryPrerequisites(ctx context.Context, q ApplicationPrerequisiteQuery) (ApplicationPrerequisiteSnapshot, error) {
	response, e := c.exchange(ctx, ApplicationHelperEnvelope{Prerequisites: &q})
	if e != nil {
		if response.ErrorCode == (&ApplicationPrerequisiteUncertainty{}).Error() {
			return ApplicationPrerequisiteSnapshot{}, &ApplicationPrerequisiteUncertainty{}
		}
		return ApplicationPrerequisiteSnapshot{}, e
	}
	if response.Prerequisites == nil || response.Receipt != nil || response.Manager != nil || response.Prerequisites.Owner != q.Owner || response.Prerequisites.SchemaVersion != ApplicationPrerequisiteSchema || response.Prerequisites.IdentityRevision == "" {
		return ApplicationPrerequisiteSnapshot{}, applicationError("prerequisites.response")
	}
	return *response.Prerequisites, nil
}

func (c ApplicationHelperClient) PlanProvision(ctx context.Context, q ApplicationProvisionRequest) (ApplicationProvisionPlan, error) {
	response, err := c.exchange(ctx, ApplicationHelperEnvelope{ProvisionPlan: &q})
	return applicationProvisionResponse(response, q, err)
}

func (c ApplicationHelperClient) Provision(ctx context.Context, p ApplicationProvisionPlan) (ApplicationProvisionPlan, error) {
	response, err := c.exchange(ctx, ApplicationHelperEnvelope{Provision: &p})
	plan, err := applicationProvisionResponse(response, p.Request, err)
	if err == nil && applicationSHA(plan) != applicationSHA(p) {
		return ApplicationProvisionPlan{}, applicationError("provisioning.response_invalid")
	}
	return plan, err
}
