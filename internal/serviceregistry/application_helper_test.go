package serviceregistry

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestApplicationHelperUnavailableNeverFallsBack(t *testing.T) {
	_, _, q, _ := applicationTestRuntime(t)
	if _, e := (ApplicationHelperClient{SocketPath: applicationFixtureDir(t) + "/missing.sock"}).Execute(context.Background(), q); e == nil {
		t.Fatal("missing root helper accepted")
	}
}

// No namespace, host unit, account, policy, package or socket is created here.
// The integrator must supply a reviewed, already-running disposable fixture
// built from the rendered Nix units and realized closure descriptors.
func TestApplicationLinuxRenderedUnitsFixture(t *testing.T) {
	root := os.Getenv("LOOM_APPLICATION_LINUX_FIXTURE")
	if runtime.GOOS != "linux" || root == "" {
		t.Skip("requires integrator-supplied disposable Linux/systemd rendered-unit fixture")
	}
	if os.Geteuid() != 0 || !strings.HasPrefix(root, "/run/loom-application-acceptance-") {
		t.Fatal("owned root fixture under /run is required")
	}
	var fixture struct {
		Socket           string `json:"socket"`
		InstallerUID     uint32 `json:"installer_uid"`
		InstallerGID     uint32 `json:"installer_gid"`
		RenderedUnitRoot string `json:"rendered_unit_root"`
		FaultPath        string `json:"fault_path"`
		Systemctl        string `json:"systemctl"`
		Scenarios        []struct {
			Name        string                    `json:"name"`
			Publication ApplicationPublication    `json:"publication"`
			Request     ApplicationRuntimeRequest `json:"request"`
			Update      ApplicationPublication    `json:"update"`
			Rollback    ApplicationPublication    `json:"rollback"`
		} `json:"scenarios"`
	}
	raw, e := os.ReadFile(filepath.Join(root, "runtime-fixture.json"))
	if e != nil || applicationDecodeJSON(raw, &fixture) != nil || len(fixture.Scenarios) != 2 || fixture.InstallerUID == 0 || fixture.FaultPath != filepath.Join(root, "fault.json") {
		t.Fatal("exact two-application fixture configuration required")
	}
	for _, unit := range []string{"loom-project-applications@.service", "loom-application@.service", "loom-project-applications-restore.service"} {
		expected, e := os.ReadFile(filepath.Join(fixture.RenderedUnitRoot, unit))
		if e != nil {
			t.Fatal(e)
		}
		actual, e := exec.Command(fixture.Systemctl, "cat", unit).Output()
		if e != nil || !bytes.Contains(actual, bytes.TrimSpace(expected)) {
			t.Fatalf("actual unit differs from Nix-rendered fixture: %s", unit)
		}
	}
	client := ApplicationHelperClient{SocketPath: fixture.Socket}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	uids := map[uint32]bool{}
	schemas := map[string]bool{}
	type retirement struct {
		request   ApplicationRuntimeRequest
		sentinels []string
	}
	retirements := []retirement{}
	for _, scenario := range fixture.Scenarios {
		t.Run(scenario.Name, func(t *testing.T) {
			publication := scenario.Publication
			if e := applicationAtomic(fixture.FaultPath, []byte(`{"stage":"publication:committed"}`), 0600); e != nil {
				t.Fatal(e)
			}
			if e := client.Publish(ctx, publication); e == nil {
				t.Fatal("publication lost reply did not execute")
			}
			if e := client.Publish(ctx, publication); e != nil {
				t.Fatal(e)
			}
			q := scenario.Request
			q.DescriptorDigest = applicationSHA(publication.Descriptor)
			q.ExpectedRevision = publication.Revision
			q.Manifest.Artifact = publication.Descriptor.Artifact
			if _, e := client.Execute(ctx, q); e == nil {
				t.Fatal("publisher/root acquired installer authority")
			}
			if e := applicationAtomic(fixture.FaultPath, []byte(`{"stage":"generation:files_published"}`), 0600); e != nil {
				t.Fatal(e)
			}
			if _, e := applicationUnprivilegedFixtureResponse(fixture.Socket, fixture.InstallerUID, fixture.InstallerGID, q); e == nil {
				t.Fatal("publication-before-reload interruption did not execute")
			}
			first := applicationRunUnprivilegedFixture(t, fixture.Socket, fixture.InstallerUID, fixture.InstallerGID, q)
			if !first.Applied || first.Readiness.Process != "satisfied" || first.Readiness.Protocol != "satisfied" || first.Process.UID != applicationAccountUID(q.Owner) || !first.Process.ListenerOwned || uids[first.Process.UID] {
				t.Fatalf("application isolation/identity unproven: %+v", first)
			}
			uids[first.Process.UID] = true
			schemas[q.Manifest.Config.Schema] = true
			sentinelPaths := []string{}
			for _, data := range first.Data {
				path := filepath.Join(data.Path, "fixture-sentinel")
				b, e := os.ReadFile(path)
				if e != nil || string(b) != "retained application fixture data\n" {
					t.Fatal("fixture data sentinel missing")
				}
				sentinelPaths = append(sentinelPaths, path)
			}
			prior := first
			for index, p := range []ApplicationPublication{scenario.Update, scenario.Rollback} {
				p.ExpectedRevision = publication.Revision
				if e := client.Publish(ctx, p); e != nil {
					t.Fatal(e)
				}
				q.OperationToken = fmt.Sprintf("%s-revision-%d", scenario.Name, index)
				q.ExpectedRevision = p.Revision
				q.ExpectedInstallationRevision = prior.InstallationRevision
				q.DescriptorDigest = applicationSHA(p.Descriptor)
				q.Manifest.Artifact = p.Descriptor.Artifact
				q.Rollback = index == 1
				if index == 0 {
					if e := applicationAtomic(fixture.FaultPath, []byte(`{"stage":"effect:restart"}`), 0600); e != nil {
						t.Fatal(e)
					}
					if _, e := applicationUnprivilegedFixtureResponse(fixture.Socket, fixture.InstallerUID, fixture.InstallerGID, q); e == nil {
						t.Fatal("restart reply-loss boundary did not execute")
					}
				}
				next := applicationRunUnprivilegedFixture(t, fixture.Socket, fixture.InstallerUID, fixture.InstallerGID, q)
				if next.Process.InvocationID == prior.Process.InvocationID || next.Process.UID != first.Process.UID || !applicationFixtureSameData(next.Data, first.Data) {
					t.Fatal("update/rollback identity or data custody changed")
				}
				if index == 0 {
					for key, before := range first.Data {
						after := next.Data[key]
						if before.MountID == 0 || after.MountID == 0 || before.MountID == after.MountID {
							t.Fatal("independent helper mount namespaces were not demonstrated")
						}
					}
				}
				publication = p
				prior = next
			}
			q.Operation = "retire"
			q.OperationToken = scenario.Name + "-retire"
			q.ExpectedInstallationRevision = prior.InstallationRevision
			q.Manifest = nil
			q.Rollback = false
			retirements = append(retirements, retirement{request: q, sentinels: sentinelPaths})
		})
	}
	// Both independently owned instances must still run at the same time.
	for _, retirement := range retirements {
		q := retirement.request
		q.Operation = "inspect"
		receipt := applicationRunUnprivilegedFixture(t, fixture.Socket, fixture.InstallerUID, fixture.InstallerGID, q)
		if receipt.Process.State != "active" {
			t.Fatal("two concurrent isolated apps were not running")
		}
	}
	// This is a private-manager reexec with exact owned volatile drop-in loss,
	// not a claim of a cold machine reboot. The integrator separately cold-starts
	// the disposable namespace with durable state retained for that final gate.
	var helperConfig ApplicationHelperConfig
	raw, e = os.ReadFile(filepath.Join(root, "helper-config.json"))
	if e != nil || applicationDecodeJSON(raw, &helperConfig) != nil {
		t.Fatal("boot helper configuration missing")
	}
	for phase := 0; phase < 2; phase++ {
		if phase == 1 {
			retired := applicationRunUnprivilegedFixture(t, fixture.Socket, fixture.InstallerUID, fixture.InstallerGID, retirements[0].request)
			if !retired.Retired {
				t.Fatal("boot retired control missing")
			}
		}
		for _, record := range retirements {
			if e := exec.CommandContext(ctx, fixture.Systemctl, "stop", record.request.Owner.Unit()).Run(); e != nil {
				t.Fatal(e)
			}
			path := filepath.Join(helperConfig.UnitRoot, record.request.Owner.Unit()+".d", "50-loom-application.conf")
			if e := os.Remove(path); e != nil && !os.IsNotExist(e) {
				t.Fatal(e)
			}
		}
		for _, operation := range []string{"daemon-reexec", "daemon-reload"} {
			if e := exec.CommandContext(ctx, fixture.Systemctl, operation).Run(); e != nil {
				t.Fatal(e)
			}
		}
		if phase == 0 {
			if e := applicationAtomic(fixture.FaultPath, []byte(`{"stage":"boot:start"}`), 0600); e != nil {
				t.Fatal(e)
			}
			if e := exec.CommandContext(ctx, fixture.Systemctl, "restart", "loom-project-applications-restore.service").Run(); e == nil {
				t.Fatal("actual boot-start interruption did not execute")
			}
		}
		invocations := map[ApplicationOwner]string{}
		for repeat := 0; repeat < 2; repeat++ {
			if e := exec.CommandContext(ctx, fixture.Systemctl, "restart", "loom-project-applications-restore.service").Run(); e != nil {
				t.Fatal(e)
			}
			for index, record := range retirements {
				inspect := record.request
				inspect.Operation = "inspect"
				receipt := applicationRunUnprivilegedFixture(t, fixture.Socket, fixture.InstallerUID, fixture.InstallerGID, inspect)
				if phase == 1 && index == 0 {
					if receipt.Process.State != "inactive" {
						t.Fatal("boot revived retired control")
					}
					continue
				}
				if receipt.Current == nil || receipt.Current.ErrorCode != "" || receipt.Current.Readiness.Process != "satisfied" || receipt.Current.Readiness.Protocol != "satisfied" {
					t.Fatalf("boot process unproven: %+v", receipt.Current)
				}
				if repeat > 0 && invocations[inspect.Owner] != receipt.Current.Process.InvocationID {
					t.Fatal("repeated boot duplicated instance")
				}
				invocations[inspect.Owner] = receipt.Current.Process.InvocationID
			}
		}
	}
	for _, retirement := range retirements {
		retired := applicationRunUnprivilegedFixture(t, fixture.Socket, fixture.InstallerUID, fixture.InstallerGID, retirement.request)
		if !retired.Retired {
			t.Fatal("retirement not proven")
		}
		for _, path := range retirement.sentinels {
			b, e := os.ReadFile(path)
			if e != nil || string(b) != "retained application fixture data\n" {
				t.Fatal("retirement changed retained sentinel")
			}
		}
	}

	if len(schemas) != 2 {
		t.Fatal("two different artifact schemas required")
	}
}
func applicationRunUnprivilegedFixture(t *testing.T, socket string, uid, gid uint32, q ApplicationRuntimeRequest) ApplicationRuntimeReceipt {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		receipt, e := applicationUnprivilegedFixtureResponse(socket, uid, gid, q)
		if e == nil {
			return receipt
		}
		if strings.HasPrefix(e.Error(), "application.health.") && receipt.Applied && time.Now().Before(deadline) {
			time.Sleep(100 * time.Millisecond)
			continue
		}
		t.Fatalf("unprivileged helper failed: %v receipt=%+v", e, receipt)
		return receipt
	}
}
func applicationUnprivilegedFixtureResponse(socket string, uid, gid uint32, q ApplicationRuntimeRequest) (ApplicationRuntimeReceipt, error) {
	request, _ := json.Marshal(q)
	cmd := exec.Command(os.Args[0], "-test.run=^TestApplicationHelperFixtureChild$")
	cmd.Env = []string{"LOOM_APPLICATION_FIXTURE_CHILD=" + base64.StdEncoding.EncodeToString(request), "LOOM_APPLICATION_FIXTURE_SOCKET=" + socket}
	cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: uid, Gid: gid, NoSetGroups: true}}
	raw, e := cmd.Output()
	if e != nil {
		return ApplicationRuntimeReceipt{}, e
	}
	var response ApplicationHelperResponse
	if e = applicationDecodeJSON(raw, &response); e != nil || response.Receipt == nil {
		return ApplicationRuntimeReceipt{}, fmt.Errorf("fixture response invalid")
	}
	if response.ErrorCode != "" {
		return *response.Receipt, fmt.Errorf("%s", response.ErrorCode)
	}
	return *response.Receipt, nil
}
func TestApplicationHelperFixtureChild(t *testing.T) {
	encoded := os.Getenv("LOOM_APPLICATION_FIXTURE_CHILD")
	if encoded == "" {
		return
	}
	if os.Geteuid() == 0 {
		os.Exit(3)
	}
	if applicationFixtureNoNewPrivileges() != nil {
		os.Exit(4)
	}
	status, e := os.ReadFile("/proc/self/status")
	if e != nil || !bytes.Contains(status, []byte("NoNewPrivs:\t1")) {
		os.Exit(5)
	}
	raw, e := base64.StdEncoding.DecodeString(encoded)
	if e != nil {
		os.Exit(6)
	}
	q, e := DecodeApplicationRuntimeRequest(raw)
	if e != nil {
		os.Exit(7)
	}
	receipt, e := (ApplicationHelperClient{SocketPath: os.Getenv("LOOM_APPLICATION_FIXTURE_SOCKET")}).Execute(context.Background(), q)
	response := ApplicationHelperResponse{Receipt: &receipt}
	if e != nil {
		response.ErrorCode = applicationSafeError(e)
	}
	_ = json.NewEncoder(os.Stdout).Encode(response)
	os.Exit(0)
}

func applicationFixtureSameData(a, b map[string]ApplicationDataIdentity) bool {
	if len(a) != len(b) {
		return false
	}
	for key, value := range a {
		if !applicationSameDataIdentity(value, b[key]) {
			return false
		}
	}
	return true
}
