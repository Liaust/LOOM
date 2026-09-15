//go:build linux

package preparation

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
	"loom.local/loom/internal/objectstore"
)

func TestPreparationLimits(t *testing.T) {
	t.Run("log budget cancels at first excess byte", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		l := &logLimit{cancel: cancel}
		block := make([]byte, 64<<10)
		for i := int64(0); i < MaxLogBytes; i += int64(len(block)) {
			if _, err := l.Write(block); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := l.Write([]byte{0}); err == nil || ctx.Err() == nil {
			t.Fatal("log overflow not cancelled")
		}
	})
	t.Run("unknown cgroup counters refuse", func(t *testing.T) {
		for _, s := range []string{"", "max invalid", "max 0\nmax 1", "max 0 extra"} {
			if _, err := counterFields(s); err == nil {
				t.Fatalf("accepted %q", s)
			}
		}
		dir := t.TempDir()
		os.WriteFile(filepath.Join(dir, "memory.events"), []byte("max 0\noom 0\n"), 0600)
		os.WriteFile(filepath.Join(dir, "pids.events"), []byte("max 0\n"), 0600)
		f, _ := objectstore.OpenPackageDirectory(dir)
		defer f.Close()
		if _, err := (&cgroup{f}).pressure(); err == nil {
			t.Fatal("missing oom_kill accepted")
		}
	})
	t.Run("fixed namespace arguments do not interpolate producer", func(t *testing.T) {
		m := runtimeManifest{Root: "/nix/store/00000000000000000000000000000000-root", Closure: []string{"/nix/store/00000000000000000000000000000000-root"}}
		command := []string{"/bin/sh", "-c", "touch /should-never-run"}
		args := nspawnArgs(HostConfig{UIDBase: 268435456}, m, "/owned", command)
		joined := strings.Join(args, "\n")
		for _, want := range []string{"--private-users=268435456:65536", "--settings=no", "--register=no", "--keep-unit", "--private-network", "--user=builder", "--drop-capability=all", "--no-new-privileges=yes", "--bind=/owned/shm:/dev/shm:norbind"} {
			if !strings.Contains(joined, want) {
				t.Fatal(want)
			}
		}
		if strings.Join(args[len(args)-len(command):], "\n") != strings.Join(command, "\n") {
			t.Fatal("argv changed")
		}
	})
	t.Run("actual CPU quota and cancellation", func(t *testing.T) {
		_, m := requireLiveFixture(t)
		req, source, producer := livePackages(t, m, "red", "i=0; while [ $i -lt 8 ]; do /bin/sh -c 'while :; do :; done' & i=$((i+1)); done; wait")
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		done := make(chan error, 1)
		go func() {
			out, err := (Client{}).Prepare(ctx, req, bytes.NewReader(source), bytes.NewReader(producer))
			if out != nil {
				out.Close()
			}
			done <- err
		}()
		waitFixturePayload(t)
		f := fixtureUnitRoot(t)
		if f == nil {
			t.Fatal("missing running cgroup")
		}
		defer f.Close()
		quota, err := readAt(f, "cpu.max", MaxControlBytes)
		if err != nil {
			t.Fatal(err)
		}
		q := strings.Fields(quota)
		if len(q) != 2 || q[0] != q[1] {
			t.Fatalf("uncapped CPU: %q", quota)
		}
		throttled := false
		for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
			value, err := readAt(f, "cpu.stat", MaxControlBytes)
			if err != nil {
				t.Fatal(err)
			}
			c, err := counterFields(value)
			if err != nil {
				t.Fatal(err)
			}
			if c["nr_throttled"] > 0 {
				throttled = true
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		cancel()
		if err := <-done; err == nil {
			t.Fatal("CPU cancellation returned output")
		}
		waitFixtureEmpty(t)
		if !throttled {
			t.Fatal("no actual CPU throttling observed")
		}
	})
	t.Run("actual Linux resource pressure", func(t *testing.T) {
		cfg, m := requireLiveFixture(t)
		t.Run("compiler failure is not pressure evidence", func(t *testing.T) {
			body := "set -eu\nmkdir -p /work/build\ncp -R /source/. /work/build/\ncd /work/build\nprintf 'invalid Go source' > cmd/pressure/main.go\ngo build -mod=vendor -o /work/pressure ./cmd/pressure"
			req, source, producer := livePackages(t, m, "red", body)
			witness, err := observeResourceFixture(t, req, source, producer, "memory")
			if err == nil || witness {
				t.Fatal("compiler failure misclassified as resource pressure")
			}
			waitFixtureEmpty(t)
		})
		if t.Failed() {
			return
		}
		for _, name := range []string{"logs", "bytes", "inodes", "forks", "memory"} {
			t.Run(name, func(t *testing.T) {
				// Build a real native probe before applying pressure. A compile
				// error cannot satisfy its independently observed witness.
				body := "set -eu\nmkdir -p /work/build /output/bin\ncp -R /source/. /work/build/\ncd /work/build\ngo build -mod=vendor -o /work/pressure ./cmd/pressure\ncp /work/pressure /output/bin/loom-application-launcher\nexec /work/pressure " + name
				req, source, producer := livePackages(t, m, "red", body)
				witness, err := observeResourceFixture(t, req, source, producer, name)
				// Preserve both original assertions: no successful output and
				// a quiescent unit followed by a healthy fresh activation.
				if err == nil {
					t.Fatal("resource overflow returned output")
				}
				waitFixtureEmpty(t)
				if !witness {
					t.Fatal("request failed without observing the requested pressure; not acceptance evidence")
				}
				assertHealthyAfter(t, cfg, m)
			})
			if t.Failed() {
				return
			}
		}
	})
}
func TestPreparationCancellationAndQuiescence(t *testing.T) {
	t.Run("residue and symlink evidence refuse", func(t *testing.T) {
		dir := t.TempDir()
		os.WriteFile(filepath.Join(dir, "cgroup.procs"), []byte(fmt.Sprint(os.Getpid())+"\n"), 0600)
		os.Mkdir(filepath.Join(dir, "supervisor"), 0700)
		os.WriteFile(filepath.Join(dir, "supervisor", "cgroup.procs"), []byte("999999\n"), 0600)
		f, _ := objectstore.OpenPackageDirectory(dir)
		defer f.Close()
		g := &cgroup{f}
		if g.startupFence() == nil {
			t.Fatal("old supervisor descendant accepted")
		}
		os.RemoveAll(filepath.Join(dir, "supervisor"))
		os.Mkdir(filepath.Join(dir, "payload"), 0700)
		os.WriteFile(filepath.Join(dir, "payload", "cgroup.procs"), nil, 0600)
		os.WriteFile(filepath.Join(dir, "payload", "cgroup.events"), []byte("populated 1\n"), 0600)
		if g.startupFence() == nil {
			t.Fatal("populated residue with empty PID listing accepted")
		}
		os.WriteFile(filepath.Join(dir, "payload", "cgroup.events"), []byte("frozen 0\n"), 0600)
		if g.startupFence() == nil {
			t.Fatal("missing populated evidence accepted")
		}
		os.RemoveAll(filepath.Join(dir, "payload"))
		os.Symlink(t.TempDir(), filepath.Join(dir, "payload"))
		if g.startupFence() == nil {
			t.Fatal("unknown symlink evidence accepted")
		}
	})
	t.Run("direct invocation unavailable", func(t *testing.T) {
		if os.Getenv("LISTEN_FDS") != "" {
			t.Skip("already inside socket activation")
		}
		if RunActivated(context.Background()) == nil {
			t.Fatal("direct invocation accepted")
		}
	})
	t.Run("actual disconnect kills descendants and prevents reuse", func(t *testing.T) {
		_, m := requireLiveFixture(t)
		req, source, producer := livePackages(t, m, "red", "(sleep 300 &) ; sleep 300")
		c, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: SocketPath, Net: "unix"})
		if err != nil {
			t.Fatal(err)
		}
		c.SetDeadline(time.Now().Add(30 * time.Second))
		defer c.Close()
		if err := writeFrame(c, req); err != nil {
			t.Fatal(err)
		}
		var status control
		if err := readFrame(c, &status); err != nil || status.Status != "accepted" {
			t.Fatalf("admission: %+v %v", status, err)
		}
		if _, err := c.Write(append(source, producer...)); err != nil {
			t.Fatal(err)
		}
		waitFixturePayload(t)
		// A second authenticated caller supplies no ingress and must be busy.
		second, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: SocketPath, Net: "unix"})
		if err != nil {
			t.Fatal(err)
		}
		second.SetDeadline(time.Now().Add(HeaderTimeout))
		var busy control
		err = readFrame(second, &busy)
		second.Close()
		if err != nil || busy.Code != ErrBusy.Error() {
			t.Fatalf("busy: %+v %v", busy, err)
		}
		c.Close()
		waitFixtureEmpty(t)
		assertHealthyAfter(t, HostConfig{}, m)
	})
}
func TestPreparationNativeBuild(t *testing.T) {
	_, m := requireLiveFixture(t)
	var previous string
	for _, word := range []string{"red", "blue"} {
		t.Run(word, func(t *testing.T) {
			req, source, producer := livePackages(t, m, word, buildProducer+"\ngo build -mod=vendor -o /work/detach ./cmd/detach\n/work/detach\n")
			output, err := (Client{}).Prepare(context.Background(), req, bytes.NewReader(source), bytes.NewReader(producer))
			if err != nil {
				t.Fatal(err)
			}
			defer output.Close()
			if _, err := output.File.Write([]byte("not writable")); err == nil {
				t.Fatal("result descriptor writable")
			}
			if output.Result.Source != req.Source || output.Result.Producer != req.Producer || output.Result.ToolchainIdentity != m.ToolchainIdentity {
				t.Fatal("result identity mismatch")
			}
			if output.Result.Output.SHA256 == previous {
				t.Fatal("changed source produced same package")
			}
			previous = output.Result.Output.SHA256
			dest := t.TempDir()
			d, err := objectstore.OpenPackageDirectory(dest)
			if err != nil {
				t.Fatal(err)
			}
			defer d.Close()
			if err := objectstore.ExtractPackage(context.Background(), output.File, d, output.Result.Output); err != nil {
				t.Fatal(err)
			}
			proof, err := os.ReadFile(filepath.Join(dest, "proof"))
			if err != nil || string(proof) != word+"-vendored\n" {
				t.Fatalf("actual compiled behavior: %q %v", proof, err)
			}
			exe, err := os.ReadFile(filepath.Join(dest, "bin/loom-application-launcher"))
			if err != nil || !bytes.HasPrefix(exe, []byte("\x7fELF")) {
				t.Fatal("no native executable")
			}
			waitFixtureEmpty(t)
		})
	}
}

const buildProducer = `set -eu
mkdir -p /work/build /output/bin
cp -R /source/. /work/build/
cd /work/build
test "$GOPROXY" = off
test "$GOSUMDB" = off
test "$GOWORK" = off
test "$GOTOOLCHAIN" = local
test "$CGO_ENABLED" = 0
test "$(id -u)" = 1000
test ! -w /source
test ! -w /producer
test ! -e /run/loom-preparation-fixture/host-sentinel
go build -mod=vendor -o /output/bin/loom-application-launcher ./cmd/launcher
/output/bin/loom-application-launcher > /output/proof
`

// Only an integrator-created, disposable systemd fixture can enable these
// tests. They never install/activate units, alter host config or call Main.
func requireLiveFixture(t *testing.T) (HostConfig, runtimeManifest) {
	t.Helper()
	if os.Getenv("LOOM_PREPARATION_FIXTURE") != "1" {
		t.Skip("requires integrator-owned disposable Linux/systemd fixture; not runtime evidence")
	}
	var st unix.Stat_t
	marker := "/run/loom-preparation-fixture/disposable"
	if err := unix.Lstat(marker, &st); err != nil || st.Mode&unix.S_IFMT != unix.S_IFREG || st.Uid != 0 || st.Mode&022 != 0 {
		t.Fatal("missing root-owned disposable fixture marker")
	}
	b, err := os.ReadFile(marker)
	if err != nil || string(b) != "loom-preparation-disposable-v1\n" {
		t.Fatal("invalid fixture marker")
	}
	var cfg HostConfig
	if err := readRootJSON(ConfigPath, &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.CallerUID != uint32(os.Getuid()) || os.Getuid() == 0 {
		t.Fatal("fixture tests must run as configured nonroot caller")
	}
	var m runtimeManifest
	if err := readRootJSON(cfg.RuntimeManifest, &m); err != nil {
		t.Fatal(err)
	}
	if err := validateRuntime(m); err != nil {
		t.Fatal(err)
	}
	t.Logf("real Linux fixture: arch=%s uid=%d base=%d closure=%s", runtime.GOARCH, os.Getuid(), cfg.UIDBase, m.ToolchainIdentity)
	return cfg, m
}
func livePackages(t *testing.T, m runtimeManifest, word, producerBody string) (Request, []byte, []byte) {
	t.Helper()
	source := t.TempDir()
	producer := t.TempDir()
	files := map[string]string{
		"go.mod":                              "module fixture.invalid/app\n\ngo 1.24\n\nrequire example.invalid/word v0.0.0\n",
		"vendor/modules.txt":                  "# example.invalid/word v0.0.0\n## explicit; go 1.24\nexample.invalid/word\n",
		"vendor/example.invalid/word/word.go": "package word\nconst Value = \"-vendored\"\n",
		"cmd/launcher/main.go":                fmt.Sprintf("package main\nimport (\"fmt\"; \"example.invalid/word\")\nfunc main(){fmt.Println(%q+word.Value)}\n", word),
		"cmd/detach/main.go": `package main
import ("os"; "os/exec"; "syscall")
func main(){
 if os.Getenv("DETACHED_STAGE")=="1" {
  c:=exec.Command("/bin/sh","-c","while :; do printf x > /output/descendant; done")
  c.SysProcAttr=&syscall.SysProcAttr{Setsid:true};if err:=c.Start();err!=nil{panic(err)};return
 }
 c:=exec.Command("/work/detach");c.Env=append(os.Environ(),"DETACHED_STAGE=1")
 c.SysProcAttr=&syscall.SysProcAttr{Setsid:true};if err:=c.Start();err!=nil{panic(err)}
 if err:=c.Wait();err!=nil{panic(err)}
}
`,
		"cmd/pressure/main.go": pressureFixtureSource,
	}
	for name, body := range files {
		p := filepath.Join(source, name)
		if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(producer, "producer.sh"), []byte(producerBody), 0644); err != nil {
		t.Fatal(err)
	}
	capture := func(root string) (objectstore.PackageInfo, []byte) {
		d, err := objectstore.OpenPackageDirectory(root)
		if err != nil {
			t.Fatal(err)
		}
		defer d.Close()
		p, err := objectstore.CapturePackage(context.Background(), d, t.TempDir(), objectstore.PackageCaptureOptions{Selection: []string{"."}})
		if err != nil {
			t.Fatal(err)
		}
		defer p.Close()
		b, err := os.ReadFile(p.ArchivePath)
		if err != nil {
			t.Fatal(err)
		}
		return p.Info, b
	}
	req := fixtureRequest(t)
	req.Platform = m.Platform
	req.ToolchainIdentity = m.ToolchainIdentity
	si, s := capture(source)
	pi, p := capture(producer)
	req.Source = si
	req.Producer = pi
	return req, s, p
}
func fixtureUnitRoot(t *testing.T) *os.File {
	t.Helper() // The fixture module runs the fixed service in system.slice.
	f, err := objectstore.OpenPackageDirectory("/sys/fs/cgroup/system.slice/" + UnitName)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatal(err)
	}
	var fs unix.Statfs_t
	if unix.Fstatfs(int(f.Fd()), &fs) != nil || fs.Type != unix.CGROUP2_SUPER_MAGIC {
		f.Close()
		t.Fatal("fixture cgroup is not cgroup2")
	}
	return f
}
func waitFixtureEmpty(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		f := fixtureUnitRoot(t)
		if f == nil {
			return
		}
		pids, err := cgroupPIDs(f)
		f.Close()
		if err == nil && len(pids) == 0 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("unit did not become empty; no next range reuse permitted")
}
func waitFixturePayload(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		f := fixtureUnitRoot(t)
		if f != nil {
			p, err := childDir(f, "payload")
			f.Close()
			if err == nil {
				pids, err := cgroupPIDs(p)
				p.Close()
				if err == nil && len(pids) > 0 {
					return
				}
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("no actual payload")
}
func assertHealthyAfter(t *testing.T, _ HostConfig, m runtimeManifest) {
	t.Helper()
	req, s, p := livePackages(t, m, "recovered", buildProducer)
	output, err := (Client{}).Prepare(context.Background(), req, bytes.NewReader(s), bytes.NewReader(p))
	if err != nil {
		t.Fatal(err)
	}
	output.Close()
	waitFixtureEmpty(t)
}

// This source runs only inside the real isolated payload. Fixed comm witnesses
// are observed through actual unit PIDs, independently of Prepare's error.
// No receipt is emitted unless the corresponding operation/check really ran.
const pressureFixtureSource = `package main
import ("errors"; "fmt"; "os"; "os/exec"; "syscall"; "time")
var keep [][]byte
func must(err error){if err!=nil {panic(err)}}
func mark(s string){must(os.WriteFile("/proc/self/comm",[]byte(s),0600));time.Sleep(500*time.Millisecond)}
func bounds(p string) syscall.Statfs_t {
 var f syscall.Statfs_t;must(syscall.Statfs(p,&f))
 if f.Type!=0x01021994 || f.Blocks*uint64(f.Bsize)!=2147483648 || f.Files!=65536 || f.Flags&6!=6 {panic("wrong tmpfs bound")}
 return f
}
func main(){
 // Every writable bind, including shm, has the same byte/inode ceiling/device.
 var work syscall.Stat_t;must(syscall.Stat("/work",&work))
 for _,p:=range []string{"/work","/home/builder","/cache","/tmp","/var/tmp","/run","/dev/shm","/output"}{
  bounds(p);var st syscall.Stat_t;must(syscall.Stat(p,&st));if st.Dev!=work.Dev{panic("other writable device")}
 }
 switch os.Args[1] {
 case "logs":
  block:=make([]byte,65536)
  for total:=0;total<8388608;total+=len(block){n,err:=os.Stdout.Write(block);must(err);if n!=len(block){panic("short log write")}}
  mark("e3e-log-8MiB")
  _,err:=os.Stdout.Write([]byte{0});must(err)
 case "bytes":
  f,err:=os.Create("/work/fill");must(err);block:=make([]byte,1048576)
  for {_,err=f.Write(block);if err!=nil{break}}
  if !errors.Is(err,syscall.ENOSPC){panic(err)}
  if bounds("/work").Bavail!=0 {panic("not out of tmpfs bytes")}
  mark("e3e-bytes-full")
 case "inodes":
  must(os.Mkdir("/work/fill",0700))
  for i:=0;;i++ {f,err:=os.Create(fmt.Sprintf("/work/fill/%d",i));if err!=nil{if !errors.Is(err,syscall.ENOSPC){panic(err)};break};must(f.Close())}
  if bounds("/work").Ffree!=0 {panic("not out of tmpfs inodes")}
  mark("e3e-inode-full")
 case "forks":
  mark("e3e-fork-ready")
  for {c:=exec.Command("/bin/sh","-c","sleep 300");if err:=c.Start();err!=nil{if !errors.Is(err,syscall.EAGAIN){panic(err)};break}}
  time.Sleep(5*time.Second)
 case "memory":
  b:=make([]byte,16<<20);for i:=range b {b[i]=1};keep=append(keep,b)
  mark("e3e-mem-ready")
  for {b:=make([]byte,16<<20);for i:=range b {b[i]=1};keep=append(keep,b)}
 default: panic("unknown pressure fixture")
 }
}
`

func observeResourceFixture(t *testing.T, req Request, source, producer []byte, mode string) (bool, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), WallTimeout+StopTimeout)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		out, err := (Client{}).Prepare(ctx, req, bytes.NewReader(source), bytes.NewReader(producer))
		if out != nil {
			out.Close()
		}
		done <- err
	}()
	marker := map[string]string{"logs": "e3e-log-8MiB", "bytes": "e3e-bytes-full", "inodes": "e3e-inode-full", "forks": "e3e-fork-ready", "memory": "e3e-mem-ready"}[mode]
	var group *os.File
	defer func() {
		if group != nil {
			group.Close()
		}
	}()
	seen := false
	eventAfterReady := false
	var beforeMax, afterMax, lastMax uint64
	eventFile := ""
	if mode == "forks" {
		eventFile = "pids.events"
	}
	if mode == "memory" {
		eventFile = "memory.events"
	}
	sample := func() {
		if group == nil {
			group = fixtureUnitRoot(t)
		}
		if group == nil {
			return
		}
		current := lastMax
		if eventFile != "" {
			if b, err := readAt(group, eventFile, MaxControlBytes); err == nil {
				if c, err := counterFields(b); err == nil {
					if n, ok := c["max"]; ok {
						current = n
					}
				}
			}
		}
		if !seen {
			if pids, err := cgroupPIDs(group); err == nil {
				for pid := range pids {
					b, err := os.ReadFile(fmt.Sprintf("/proc/%d/comm", pid))
					if err == nil && strings.TrimSpace(string(b)) == marker {
						seen = true
						beforeMax = current
						break
					}
				}
			}
		} else if current > beforeMax {
			eventAfterReady = true
		}
		lastMax = current
		afterMax = current
	}
	ticker := time.NewTicker(2 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case err := <-done:
			sample()
			proven := seen && (eventFile == "" || eventAfterReady)
			t.Logf("actual pressure witness: kind=%s comm=%s seen=%t %s.max before=%d after=%d", mode, marker, seen, eventFile, beforeMax, afterMax)
			return proven, err
		case <-ticker.C:
			sample()
		case <-ctx.Done():
			return false, ctx.Err()
		}
	}
}
