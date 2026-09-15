//go:build linux

package preparation

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sys/unix"
	"loom.local/loom/internal/objectstore"
)

func peerUID(conn *net.UnixConn) (uint32, error) {
	raw, err := conn.SyscallConn()
	if err != nil {
		return 0, err
	}
	var uid uint32
	var socketErr error
	err = raw.Control(func(fd uintptr) {
		cred, e := unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
		socketErr = e
		if e == nil {
			uid = cred.Uid
		}
	})
	if err != nil {
		return 0, err
	}
	return uid, socketErr
}

// RunActivated has no direct-root or alternate-unit execution mode.
func RunActivated(ctx context.Context) error {
	if os.Geteuid() != 0 || os.Getenv("LISTEN_PID") != strconv.Itoa(os.Getpid()) || os.Getenv("LISTEN_FDS") != "1" || os.Getenv("LISTEN_FDNAMES") != SocketName {
		return ErrUnavailable
	}
	if invocation := os.Getenv("INVOCATION_ID"); len(invocation) != 32 || strings.Trim(invocation, "0123456789abcdef") != "" {
		return ErrUnavailable
	}
	for _, key := range []string{"LISTEN_PID", "LISTEN_FDS", "LISTEN_FDNAMES"} {
		if err := os.Unsetenv(key); err != nil {
			return err
		}
	}
	unix.CloseOnExec(3)
	var cfg HostConfig
	if err := readRootJSON(ConfigPath, &cfg); err != nil {
		return err
	}
	if cfg.Schema != Schema || cfg.CallerUID == 0 || cfg.UIDBase < 65536 || cfg.UIDBase%65536 != 0 || uint64(cfg.UIDBase)+65536 > uint64(^uint32(0)) {
		return ErrUnavailable
	}
	for _, p := range []string{cfg.Nspawn, cfg.Getent, cfg.RuntimeManifest} {
		if !storePath(p) {
			return ErrUnavailable
		}
	}
	var manifest runtimeManifest
	if err := readRootJSON(cfg.RuntimeManifest, &manifest); err != nil {
		return err
	}
	if err := validateRuntime(manifest); err != nil {
		return err
	}
	listener, err := activationListener(cfg.CallerUID)
	if err != nil {
		return err
	}
	defer listener.Close()
	group, err := serviceGroup()
	if err != nil {
		return err
	}
	defer group.Close()
	if err := group.startupFence(); err != nil {
		return err
	}
	if err := auditRange(ctx, cfg); err != nil {
		return err
	}
	workspace, err := newWorkspace()
	if err != nil {
		return err
	}
	defer workspace.close()
	return serveOne(ctx, listener, func(conn *net.UnixConn) error {
		uid, err := peerUID(conn)
		if err != nil || uid != cfg.CallerUID {
			return ErrRefused
		}
		return nil
	}, func(runCtx context.Context, conn *net.UnixConn, req Request) error {
		if req.Platform != manifest.Platform || req.ToolchainIdentity != manifest.ToolchainIdentity {
			return ErrRefused
		}
		return runPreparation(runCtx, conn, req, cfg, manifest, group, workspace)
	})
}

func activationListener(uid uint32) (*net.UnixListener, error) {
	parent, err := objectstore.OpenPackageDirectory(filepath.Dir(SocketPath))
	if err != nil {
		return nil, err
	}
	var parentStat unix.Stat_t
	err = unix.Fstat(int(parent.Fd()), &parentStat)
	parent.Close()
	if err != nil || parentStat.Uid != 0 || parentStat.Mode&022 != 0 {
		return nil, ErrUnavailable
	}

	typ, err := unix.GetsockoptInt(3, unix.SOL_SOCKET, unix.SO_TYPE)
	if err != nil || typ != unix.SOCK_STREAM {
		return nil, ErrUnavailable
	}
	accept, err := unix.GetsockoptInt(3, unix.SOL_SOCKET, unix.SO_ACCEPTCONN)
	if err != nil || accept != 1 {
		return nil, ErrUnavailable
	}
	addr, err := unix.Getsockname(3)
	if err != nil {
		return nil, err
	}
	ua, ok := addr.(*unix.SockaddrUnix)
	if !ok || ua.Name != SocketPath {
		return nil, ErrUnavailable
	}
	var st unix.Stat_t
	if unix.Lstat(SocketPath, &st) != nil || st.Mode&unix.S_IFMT != unix.S_IFSOCK || st.Uid != uid || st.Mode&0777 != 0600 {
		return nil, ErrUnavailable
	}
	f := os.NewFile(3, SocketPath)
	defer f.Close()
	l, err := net.FileListener(f)
	if err != nil {
		return nil, err
	}
	u, ok := l.(*net.UnixListener)
	if !ok {
		l.Close()
		return nil, ErrUnavailable
	}
	u.SetUnlinkOnClose(false)
	return u, nil
}

func storePath(p string) bool {
	if !filepath.IsAbs(p) || filepath.Clean(p) != p || strings.ContainsAny(p, "\n\r\x00:\\") || !strings.HasPrefix(p, "/nix/store/") {
		return false
	}
	part := strings.Split(strings.TrimPrefix(p, "/nix/store/"), "/")[0]
	return len(part) > 33 && part[32] == '-' && strings.Trim(part[:32], "0123456789abcdfghijklmnpqrsvwxyz") == ""
}
func readRootJSON(name string, target any) error {
	resolved, err := filepath.EvalSymlinks(name)
	if err != nil || !storePath(resolved) {
		return ErrUnavailable
	}
	parent, err := objectstore.OpenPackageDirectory(filepath.Dir(resolved))
	if err != nil {
		return err
	}
	defer parent.Close()
	fd, err := unix.Openat(int(parent.Fd()), filepath.Base(resolved), unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	f := os.NewFile(uintptr(fd), resolved)
	defer f.Close()
	var st unix.Stat_t
	if unix.Fstat(fd, &st) != nil || st.Mode&unix.S_IFMT != unix.S_IFREG || st.Uid != 0 || st.Mode&022 != 0 || st.Size > MaxControlBytes {
		return ErrUnavailable
	}
	d := json.NewDecoder(io.LimitReader(f, MaxControlBytes+1))
	d.DisallowUnknownFields()
	if err := d.Decode(target); err != nil {
		return err
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return ErrUnavailable
	}
	return nil
}
func validateRuntime(m runtimeManifest) error {
	platform := map[string]string{"amd64": "x86_64-linux", "arm64": "aarch64-linux"}[runtime.GOARCH]
	if m.Schema != Schema || m.Platform != platform || !storePath(m.Root) || len(m.Closure) == 0 || len(m.Closure) > 512 || !sort.StringsAreSorted(m.Closure) {
		return ErrUnavailable
	}
	last := ""
	rootPresent := false
	for _, p := range m.Closure {
		if !storePath(p) || filepath.Dir(p) != "/nix/store" || p == last {
			return ErrUnavailable
		}
		last = p
		if p == m.Root {
			rootPresent = true
		}
	}
	if !rootPresent || digest([]byte(strings.Join(m.Closure, "\n")+"\n")) != m.ToolchainIdentity {
		return ErrUnavailable
	}
	return nil
}

type cgroup struct{ root *os.File }

func (g *cgroup) Close() error { return g.root.Close() }
func serviceGroup() (*cgroup, error) {
	b, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		return nil, err
	}
	s := strings.TrimSpace(string(b))
	if !strings.HasPrefix(s, "0::/") || strings.Contains(s, "\n") {
		return nil, ErrUnavailable
	}
	p := strings.TrimPrefix(s, "0::")
	suffix := "/" + UnitName + "/supervisor"
	if !strings.HasSuffix(p, suffix) || filepath.Clean(p) != p {
		return nil, ErrUnavailable
	}
	unit := strings.TrimSuffix(p, "/supervisor")
	f, err := objectstore.OpenPackageDirectory("/sys/fs/cgroup" + unit)
	if err != nil {
		return nil, err
	}
	var fs unix.Statfs_t
	if unix.Fstatfs(int(f.Fd()), &fs) != nil || fs.Type != unix.CGROUP2_SUPER_MAGIC {
		f.Close()
		return nil, ErrUnavailable
	}
	g := &cgroup{f}
	for name, want := range map[string]string{"memory.max": strconv.Itoa(MaxMemoryBytes), "memory.swap.max": "0", "pids.max": strconv.Itoa(MaxTasks)} {
		got, err := g.read(name)
		if err != nil || strings.TrimSpace(got) != want {
			g.Close()
			return nil, ErrUnavailable
		}
	}
	cpu, err := g.read("cpu.max")
	fields := strings.Fields(cpu)
	if err != nil || len(fields) != 2 {
		g.Close()
		return nil, ErrUnavailable
	}
	q, e1 := strconv.ParseUint(fields[0], 10, 64)
	period, e2 := strconv.ParseUint(fields[1], 10, 64)
	if e1 != nil || e2 != nil || q == 0 || q != period {
		g.Close()
		return nil, ErrUnavailable
	}
	return g, nil
}
func (g *cgroup) read(name string) (string, error) { return readAt(g.root, name, MaxControlBytes) }
func readAt(parent *os.File, name string, limit int64) (string, error) {
	if name == "" || strings.Contains(name, "/") {
		return "", ErrUnavailable
	}
	fd, err := unix.Openat(int(parent.Fd()), name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return "", err
	}
	f := os.NewFile(uintptr(fd), name)
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil || int64(len(b)) > limit {
		return "", ErrUnavailable
	}
	return string(b), nil
}
func childDir(parent *os.File, name string) (*os.File, error) {
	if name == "" || name == ".." || strings.Contains(name, "/") {
		return nil, ErrUnavailable
	}
	fd, err := unix.Openat(int(parent.Fd()), name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), filepath.Join(parent.Name(), name)), nil
}
func cgroupPIDs(root *os.File) (map[int]bool, error) {
	result := map[int]bool{}
	count := 0
	var walk func(*os.File, int) error
	walk = func(dir *os.File, depth int) error {
		count++
		if count > 256 || depth > 32 {
			return ErrUnavailable
		}
		content, err := readAt(dir, "cgroup.procs", MaxControlBytes)
		if err != nil {
			return err
		}
		for _, token := range strings.Fields(content) {
			pid, err := strconv.Atoi(token)
			if err != nil || pid <= 0 {
				return ErrUnavailable
			}
			result[pid] = true
			if len(result) > MaxTasks {
				return ErrUnavailable
			}
		}
		listing, err := childDir(dir, ".")
		if err != nil {
			return err
		}
		defer listing.Close()
		entries, err := listing.ReadDir(-1)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if entry.Type()&os.ModeSymlink != 0 {
				return ErrUnavailable
			}
			if entry.IsDir() {
				child, err := childDir(dir, entry.Name())
				if err != nil {
					return err
				}
				err = walk(child, depth+1)
				child.Close()
				if err != nil {
					return err
				}
			}
		}
		return nil
	}
	return result, walk(root, 0)
}
func (g *cgroup) startupFence() error {
	pids, err := cgroupPIDs(g.root)
	if err != nil || len(pids) != 1 || !pids[os.Getpid()] {
		return ErrUnavailable
	}
	payload, err := childDir(g.root, "payload")
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	defer payload.Close()
	events, err := readAt(payload, "cgroup.events", MaxControlBytes)
	if err != nil {
		return err
	}
	fields, err := counterFields(events)
	populated, known := fields["populated"]
	if err != nil || !known || populated != 0 {
		return ErrUnavailable
	}
	return nil
}
func (g *cgroup) quiesce(ctx context.Context) error {
	payload, err := childDir(g.root, "payload")
	if os.IsNotExist(err) {
		return g.startupFence()
	}
	if err != nil {
		return err
	}
	defer payload.Close()
	fd, err := unix.Openat(int(payload.Fd()), "cgroup.kill", unix.O_WRONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	f := os.NewFile(uintptr(fd), "cgroup.kill")
	_, err = f.WriteString("1")
	f.Close()
	if err != nil {
		return err
	}
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		events, err := readAt(payload, "cgroup.events", MaxControlBytes)
		if err != nil {
			return err
		}
		fields, err := counterFields(events)
		populated, known := fields["populated"]
		if err != nil || !known || populated > 1 {
			return ErrUnavailable
		}
		if populated == 0 {
			pids, err := cgroupPIDs(payload)
			if err != nil {
				return err
			}
			if len(pids) == 0 {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return ErrUnavailable
		case <-ticker.C:
		}
	}
}
func counterFields(s string) (map[string]uint64, error) {
	m := map[string]uint64{}
	for _, line := range strings.Split(strings.TrimSpace(s), "\n") {
		f := strings.Fields(line)
		if len(f) != 2 {
			return nil, ErrUnavailable
		}
		n, err := strconv.ParseUint(f[1], 10, 64)
		if err != nil {
			return nil, err
		}
		if _, ok := m[f[0]]; ok {
			return nil, ErrUnavailable
		}
		m[f[0]] = n
	}
	return m, nil
}
func (g *cgroup) pressure() (string, error) {
	var result []string
	for _, file := range []string{"memory.events", "pids.events"} {
		b, err := g.read(file)
		if err != nil {
			return "", err
		}
		c, err := counterFields(b)
		if err != nil {
			return "", err
		}
		keys := []string{"max"}
		if file == "memory.events" {
			keys = append(keys, "oom", "oom_kill")
		}
		for _, key := range keys {
			n, ok := c[key]
			if !ok {
				return "", ErrUnavailable
			}
			result = append(result, fmt.Sprint(n))
		}
	}
	return strings.Join(result, ":"), nil
}

func auditRange(ctx context.Context, cfg HostConfig) error {
	end := uint64(cfg.UIDBase) + 65536
	overlaps := func(base, count uint64) bool {
		return count == 0 || base+count < base || base+count > 1<<32 || base < end && base+count > uint64(cfg.UIDBase)
	}
	if overlaps(uint64(cfg.CallerUID), 1) {
		return ErrUnavailable
	}
	for _, r := range cfg.Reservations {
		if r.Name == "" || overlaps(uint64(r.Base), uint64(r.Count)) {
			return ErrUnavailable
		}
	}
	// Only the complete files account source is supported. NSS iterators can
	// silently end on an unavailable provider, so a successful getent command
	// would not prove that systemd/remote user databases were fully audited.
	// Other allocator reservations are audited separately above.
	nss, err := boundedReadFile("/etc/nsswitch.conf", MaxControlBytes)
	if err != nil {
		return err
	}
	providers := map[string]bool{}
	for _, line := range strings.Split(string(nss), "\n") {
		f := strings.Fields(strings.SplitN(line, "#", 2)[0])
		if len(f) > 0 && (f[0] == "passwd:" || f[0] == "group:" || f[0] == "subuid:" || f[0] == "subgid:") {
			if providers[f[0]] || len(f) < 2 {
				return ErrUnavailable
			}
			providers[f[0]] = true
			for _, p := range f[1:] {
				if p != "files" {
					return ErrUnavailable
				}
			}
		}
	}
	if !providers["passwd:"] || !providers["group:"] {
		return ErrUnavailable
	}
	for _, kind := range []string{"passwd", "group"} {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		output, err := boundedReadFile("/etc/"+kind, 4<<20)
		if err != nil {
			return ErrUnavailable
		}
		for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
			f := strings.Split(line, ":")
			if len(f) < 4 {
				return ErrUnavailable
			}
			indexes := []int{2}
			if kind == "passwd" {
				indexes = append(indexes, 3)
			}
			for _, index := range indexes {
				id, err := strconv.ParseUint(f[index], 10, 32)
				if err != nil || overlaps(id, 1) {
					return ErrUnavailable
				}
			}
		}
	}
	for _, name := range []string{"/etc/subuid", "/etc/subgid"} {
		b, err := boundedReadFile(name, 4<<20)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil || len(b) > 4<<20 {
			return ErrUnavailable
		}
		for _, line := range strings.Split(string(b), "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			f := strings.Split(line, ":")
			if len(f) != 3 {
				return ErrUnavailable
			}
			base, e1 := strconv.ParseUint(f[1], 10, 32)
			count, e2 := strconv.ParseUint(f[2], 10, 32)
			if e1 != nil || e2 != nil || overlaps(base, count) {
				return ErrUnavailable
			}
		}
	}
	return nil
}

type privateWorkspace struct {
	path   string
	root   *os.File
	closed bool
}

func newWorkspace() (*privateWorkspace, error) {
	self, err := os.Stat("/proc/self/ns/mnt")
	if err != nil {
		return nil, err
	}
	init, err := os.Stat("/proc/1/ns/mnt")
	if err != nil || os.SameFile(self, init) {
		return nil, ErrUnavailable
	}
	parent := filepath.Dir(SocketPath)
	// A killed activation leaves only this empty host mountpoint. Reuse its
	// fixed name after the whole-unit residue fence; never sweep arbitrary
	// directories or accumulate a directory per failed activation.
	path := filepath.Join(parent, "workspace")
	if err := os.Mkdir(path, 0700); err != nil && !os.IsExist(err) {
		return nil, err
	}
	mountpoint, err := objectstore.OpenPackageDirectory(path)
	if err != nil {
		return nil, err
	}
	var st unix.Stat_t
	statErr := unix.Fstat(int(mountpoint.Fd()), &st)
	names, readErr := mountpoint.Readdirnames(1)
	mountpoint.Close()
	if statErr != nil || st.Uid != 0 || st.Mode&07777 != 0700 || len(names) != 0 || readErr != io.EOF {
		return nil, ErrUnavailable
	}
	w := &privateWorkspace{path: path}
	if err := unix.Mount("tmpfs", path, "tmpfs", unix.MS_NODEV|unix.MS_NOSUID, "size=2147483648,nr_inodes=65536,mode=0700,noswap"); err != nil {
		os.Remove(path)
		return nil, err
	}
	f, err := objectstore.OpenPackageDirectory(path)
	if err != nil {
		unix.Unmount(path, 0)
		os.Remove(path)
		return nil, err
	}
	w.root = f
	var fs unix.Statfs_t
	if unix.Fstatfs(int(f.Fd()), &fs) != nil || fs.Type != unix.TMPFS_MAGIC || fs.Blocks == 0 || fs.Files == 0 || fs.Blocks*uint64(fs.Bsize) > MaxWorkspaceBytes || fs.Files > MaxWorkspaceInodes || fs.Flags&unix.MS_NOSUID == 0 || fs.Flags&unix.MS_NODEV == 0 {
		w.close()
		return nil, ErrUnavailable
	}
	return w, nil
}
func (w *privateWorkspace) close() error {
	if w.closed {
		return nil
	}
	if w.root != nil {
		w.root.Close()
		w.root = nil
	}
	if err := unix.Unmount(w.path, 0); err != nil {
		return err
	}
	if err := os.Remove(w.path); err != nil {
		return err
	}
	w.closed = true
	return nil
}
func (w *privateWorkspace) mkdir(name string, mode os.FileMode) (*os.File, error) {
	if strings.Contains(name, "/") || name == "" || name == "." || name == ".." {
		return nil, ErrUnavailable
	}
	if err := unix.Mkdirat(int(w.root.Fd()), name, uint32(mode)); err != nil {
		return nil, err
	}
	return childDir(w.root, name)
}

func runPreparation(ctx context.Context, conn *net.UnixConn, req Request, cfg HostConfig, manifest runtimeManifest, g *cgroup, w *privateWorkspace) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(ctx, func() { _ = conn.SetDeadline(time.Now()) })
	defer stop()
	baseline, err := g.pressure()
	if err != nil {
		return err
	}
	if err := writeFrame(conn, control{Schema: Schema, Status: "accepted"}); err != nil {
		return err
	}
	for _, item := range []struct {
		name string
		info objectstore.PackageInfo
	}{{"source", req.Source}, {"producer", req.Producer}} {
		root, err := w.mkdir(item.name, 0700)
		if err != nil {
			return err
		}
		err = objectstore.ExtractPackage(ctx, io.LimitReader(conn, item.info.SizeBytes), root, item.info)
		if err == nil {
			err = root.Chmod(0755)
		}
		root.Close()
		if err != nil {
			return err
		}
	}
	ack := make(chan control, 1)
	var delivering atomic.Bool
	go func() {
		var c control
		err := readFrame(conn, &c)
		if err != nil || !delivering.Load() || c.Schema != Schema || c.Status != "received" || len(c.Receipt) != 64 {
			cancel()
			return
		}
		ack <- c
	}()
	for _, name := range []string{"work", "home", "cache", "tmp", "vartmp", "run", "shm", "output"} {
		f, err := w.mkdir(name, 0700)
		if err != nil {
			return err
		}
		err = f.Chown(int(cfg.UIDBase)+1000, int(cfg.UIDBase)+1000)
		f.Close()
		if err != nil {
			return err
		}
	}
	// The /run bind replaces nspawn scratch; its masked child must exist.
	if err := os.Mkdir(filepath.Join(w.path, "run", "host"), 0000); err != nil {
		return err
	}
	command, err := req.Validate()
	if err != nil {
		return err
	}
	pressure, err := g.pressure()
	if err != nil || pressure != baseline {
		return ErrFailed
	}
	if err := executePayload(ctx, cancel, cfg, manifest, w, command, g, baseline); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	output, err := childDir(w.root, "output")
	if err != nil {
		return err
	}
	defer output.Close()
	bin, err := childDir(output, "bin")
	if err != nil {
		return err
	}
	var launcher unix.Stat_t
	err = unix.Fstatat(int(bin.Fd()), "loom-application-launcher", &launcher, unix.AT_SYMLINK_NOFOLLOW)
	bin.Close()
	if err != nil || launcher.Mode&unix.S_IFMT != unix.S_IFREG || launcher.Mode&0111 == 0 {
		return ErrFailed
	}
	sealed, err := w.mkdir("sealed", 0700)
	if err != nil {
		return err
	}
	sealed.Close()
	capture, err := objectstore.CapturePackage(ctx, output, filepath.Join(w.path, "sealed"), objectstore.PackageCaptureOptions{Selection: []string{"."}, MaxContentBytes: MaxOutputBytes})
	if err != nil {
		return err
	}
	defer capture.Close()
	f, err := os.Open(capture.ArchivePath)
	if err != nil {
		return err
	}
	info, err := objectstore.InspectPackage(ctx, f)
	if err != nil || info != capture.Info {
		f.Close()
		return ErrFailed
	}
	if _, err = f.Seek(0, io.SeekStart); err != nil {
		f.Close()
		return err
	}
	pressure, err = g.pressure()
	if err != nil || pressure != baseline {
		f.Close()
		return ErrFailed
	}
	result := resultFor(req, info)
	delivering.Store(true)
	if err = writeFrame(conn, control{Schema: Schema, Status: "output", Result: &result}); err == nil {
		_, err = io.CopyN(conn, f, info.SizeBytes)
	}
	f.Close()
	if err != nil {
		return err
	}
	var nonce [32]byte
	if _, err = rand.Read(nonce[:]); err != nil {
		return err
	}
	receipt := hex.EncodeToString(nonce[:])
	if err := writeFrame(conn, control{Schema: Schema, Status: "verify", Receipt: receipt}); err != nil {
		return err
	}
	select {
	case c := <-ack:
		if c.Receipt != receipt {
			return ErrFailed
		}
	case <-ctx.Done():
		return ErrFailed
	}
	// Close every directory/archive handle before destroying the parent's mount.
	output.Close()
	if err := capture.Close(); err != nil {
		return err
	}
	if err := w.close(); err != nil {
		return err
	}
	pressure, err = g.pressure()
	if ctx.Err() != nil || err != nil || pressure != baseline {
		return ErrFailed
	}
	return writeFrame(conn, control{Schema: Schema, Status: "complete", Receipt: receipt})
}

type logLimit struct {
	mu       sync.Mutex
	n        int64
	exceeded bool
	cancel   context.CancelFunc
}

func (l *logLimit) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if int64(len(p)) > MaxLogBytes-l.n {
		l.exceeded = true
		l.cancel()
		return 0, ErrFailed
	}
	l.n += int64(len(p))
	return len(p), nil
}
func executePayload(ctx context.Context, cancel context.CancelFunc, cfg HostConfig, m runtimeManifest, w *privateWorkspace, command []string, g *cgroup, baseline string) error {
	args := nspawnArgs(cfg, m, w.path, command)
	cmd := exec.Command(cfg.Nspawn, args...)
	cmd.WaitDelay = StopTimeout
	cmd.Env = []string{"PATH=" + filepath.Dir(cfg.Getent), "LANG=C", "SYSTEMD_LOG_LEVEL=warning", "SYSTEMD_NSPAWN_SUPPRESS_SYNC=1"}
	logs := &logLimit{cancel: cancel}
	cmd.Stdout = logs
	cmd.Stderr = logs
	cmd.Stdin = nil
	if err := cmd.Start(); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	var runErr error
	reaped := false
running:
	for {
		select {
		case runErr = <-done:
			reaped = true
			break running
		case <-ctx.Done():
			runErr = ErrFailed
			break running
		case <-ticker.C:
			pressure, err := g.pressure()
			if err != nil || pressure != baseline {
				cancel()
				runErr = ErrFailed
				break running
			}
		}
	}
	if !reaped {
		_ = cmd.Process.Kill()
	}
	cleanupCtx, stop := context.WithTimeout(context.Background(), StopTimeout)
	defer stop()
	quiescence := g.quiesce(cleanupCtx)
	if !reaped {
		select {
		case <-done:
			reaped = true
		case <-cleanupCtx.Done():
			return ErrUnavailable
		}
	}
	if quiescence != nil || g.startupFence() != nil {
		return ErrUnavailable
	}
	pressure, err := g.pressure()
	if err != nil || pressure != baseline {
		return ErrFailed
	}
	if ctx.Err() != nil || runErr != nil || logs.exceeded {
		return ErrFailed
	}
	return nil
}

func nspawnArgs(cfg HostConfig, m runtimeManifest, workspace string, command []string) []string {
	args := []string{"--quiet", "--settings=no", "--register=no", "--keep-unit", "--machine=loom-preparation", "--as-pid2", "--console=pipe", "--read-only", "--private-network", "--private-users=" + strconv.FormatUint(uint64(cfg.UIDBase), 10) + ":65536", "--private-users-ownership=off", "--user=builder", "--drop-capability=all", "--rlimit=RLIMIT_MSGQUEUE=0", "--no-new-privileges=yes", "--resolv-conf=off", "--timezone=off", "--link-journal=no", "--directory=" + m.Root, "--chdir=/producer"}
	for _, p := range m.Closure {
		args = append(args, "--bind-ro="+p+":"+p+":norbind")
	}
	for _, name := range []string{"source", "producer"} {
		args = append(args, "--bind-ro="+filepath.Join(workspace, name)+":/"+name+":norbind")
	}
	for _, binding := range [][2]string{{"work", "/work"}, {"home", "/home/builder"}, {"cache", "/cache"}, {"tmp", "/tmp"}, {"vartmp", "/var/tmp"}, {"run", "/run"}, {"shm", "/dev/shm"}, {"output", "/output"}} {
		args = append(args, "--bind="+filepath.Join(workspace, binding[0])+":"+binding[1]+":norbind")
	}
	for _, p := range []string{"/run/host"} {
		args = append(args, "--inaccessible="+p)
	}
	args = append(args, "--", "/bin/loom-preparation-entry")
	return append(args, command...)
}

func boundedReadFile(name string, limit int64) ([]byte, error) {
	f, err := os.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil || int64(len(b)) > limit {
		return nil, ErrUnavailable
	}
	return b, nil
}
