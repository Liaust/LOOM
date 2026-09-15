package serviceregistry

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"syscall"
	"testing"
)

func TestApplicationPrerequisitesEnvelope(t *testing.T) {
	_, q, _ := applicationPrerequisitesFixture(t)
	valid, _ := json.Marshal(ApplicationHelperEnvelope{Prerequisites: &q})
	if _, e := decodeApplicationPrerequisiteEnvelope(valid); e != nil {
		t.Fatal(e)
	}
	for _, raw := range []string{`{}`, `null`, `[]`, string(valid) + ` {}`, `{"prerequisites":null}`, `{"prerequisites":{},"prerequisites":{}}`, `{"prerequisites":{"schema_version":"application.prerequisites.v1","owner":{},"actor_id":"privileged"}}`, `{"prerequisites":{},"request":{}}`, `{"prerequisites":{},"publication":{}}`, `{"prerequisites":{},"archive":{}}`, `{"prerequisites":{},"unknown":true}`} {
		t.Run(raw, func(t *testing.T) {
			if _, e := decodeApplicationPrerequisiteEnvelope([]byte(raw)); e == nil {
				t.Fatal("unsafe envelope accepted")
			}
		})
	}
	for _, envelope := range []ApplicationHelperEnvelope{{Request: &ApplicationRuntimeRequest{}}, {Publication: &ApplicationPublication{}}, {Archive: &ApplicationArchiveControl{}}} {
		raw, _ := json.Marshal(envelope)
		if _, e := decodeApplicationPrerequisiteEnvelope(raw); e != nil {
			t.Fatalf("existing variant changed: %v", e)
		}
	}
}
func TestApplicationPrerequisitesHelperUnavailable(t *testing.T) {
	_, q, _ := applicationPrerequisitesFixture(t)
	_, e := (ApplicationHelperClient{SocketPath: filepath.Join(applicationFixtureDir(t), "missing.sock")}).QueryPrerequisites(t.Context(), q)
	if e == nil {
		t.Fatal("unavailable helper accepted")
	}
	if runtime.GOOS != "linux" && e.Error() != "application.helper.linux_required" {
		t.Fatal(e)
	}
}

// Integrator-only: consumes an already provisioned tiny Linux namespace fixture.
// Creates no socket, unit, account or application state, and performs no effects.
// Manifest: socket, installer_uid/gid, policy_path, state_root, owners (nonempty).
func TestApplicationPrerequisitesLinuxHelper(t *testing.T) {
	root := os.Getenv("LOOM_APPLICATION_PREREQUISITES_FIXTURE")
	if runtime.GOOS != "linux" || root == "" {
		t.Skip("requires integrator-supplied disposable Linux root helper fixture")
	}
	if os.Geteuid() != 0 || !strings.HasPrefix(root, "/run/loom-application-acceptance-") {
		t.Fatal("owned disposable /run fixture required")
	}
	var fixture struct {
		Socket       string             `json:"socket"`
		InstallerUID uint32             `json:"installer_uid"`
		InstallerGID uint32             `json:"installer_gid"`
		PolicyPath   string             `json:"policy_path"`
		StateRoot    string             `json:"state_root"`
		Owners       []ApplicationOwner `json:"owners"`
	}
	raw, e := os.ReadFile(filepath.Join(root, "prerequisites-fixture.json"))
	if e != nil || applicationDecodeJSON(raw, &fixture) != nil || fixture.InstallerUID == 0 || fixture.InstallerGID == 0 || len(fixture.Owners) == 0 {
		t.Fatal("exact prerequisite fixture manifest required")
	}
	for _, path := range []string{fixture.Socket, fixture.PolicyPath, fixture.StateRoot} {
		if !strings.HasPrefix(path, root+"/") {
			t.Fatal("fixture path escaped reviewed root")
		}
	}
	before := applicationPrerequisitesTree(t, root)
	r := ApplicationRuntime{Store: ApplicationStateStore{Root: fixture.StateRoot, OwnerUID: 0}, PolicyPath: fixture.PolicyPath}
	for _, owner := range fixture.Owners {
		t.Run(owner.Resource, func(t *testing.T) {
			q := ApplicationPrerequisiteQuery{SchemaVersion: ApplicationPrerequisiteSchema, Owner: owner}
			expected, e := r.QueryPrerequisites(t.Context(), fixture.InstallerUID, q)
			if e != nil {
				t.Fatal(e)
			}
			if _, e = (ApplicationHelperClient{SocketPath: fixture.Socket}).QueryPrerequisites(t.Context(), q); e == nil {
				t.Fatal("publisher/root acquired query authority")
			}
			actual, e := applicationPrerequisitesChild(fixture.Socket, fixture.InstallerUID, fixture.InstallerGID, q)
			if e != nil {
				t.Fatal(e)
			}
			if actual.Owner != owner || actual.IdentityRevision != expected.IdentityRevision || !reflect.DeepEqual(actual.Data, expected.Data) || !reflect.DeepEqual(actual.Credentials, expected.Credentials) {
				t.Fatal("actual distinct-UID socket query differs from scoped root observation")
			}
			again, e := applicationPrerequisitesChild(fixture.Socket, fixture.InstallerUID, fixture.InstallerGID, q)
			if e != nil || again.IdentityRevision != actual.IdentityRevision {
				t.Fatal("query unstable over socket")
			}
			wrong := q
			wrong.Owner.Resource = "ungranted-e3a"
			if _, e = applicationPrerequisitesChild(fixture.Socket, fixture.InstallerUID, fixture.InstallerGID, wrong); e == nil {
				t.Fatal("ungranted owner accepted")
			}
			if _, e = applicationPrerequisitesChild(fixture.Socket, fixture.InstallerUID+1, fixture.InstallerGID, q); e == nil {
				t.Fatal("wrong OS peer accepted")
			}
		})
	}
	if !reflect.DeepEqual(before, applicationPrerequisitesTree(t, root)) {
		t.Fatal("socket query changed fixture files/directories/modes/mtimes")
	}
}
func applicationPrerequisitesChild(socket string, uid, gid uint32, q ApplicationPrerequisiteQuery) (ApplicationPrerequisiteSnapshot, error) {
	raw, _ := json.Marshal(q)
	cmd := exec.Command(os.Args[0], "-test.run=^TestApplicationPrerequisitesHelperChild$")
	cmd.Env = []string{"LOOM_PREREQUISITES_CHILD=" + base64.StdEncoding.EncodeToString(raw), "LOOM_PREREQUISITES_SOCKET=" + socket}
	cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: uid, Gid: gid, NoSetGroups: true}}
	raw, e := cmd.Output()
	if e != nil {
		return ApplicationPrerequisiteSnapshot{}, e
	}
	var response ApplicationHelperResponse
	if applicationDecodeJSON(raw, &response) != nil {
		return ApplicationPrerequisiteSnapshot{}, fmt.Errorf("fixture response invalid")
	}
	if response.ErrorCode != "" {
		return ApplicationPrerequisiteSnapshot{}, fmt.Errorf("%s", response.ErrorCode)
	}
	if response.Prerequisites == nil {
		return ApplicationPrerequisiteSnapshot{}, fmt.Errorf("fixture response absent")
	}
	return *response.Prerequisites, nil
}
func TestApplicationPrerequisitesHelperChild(t *testing.T) {
	encoded := os.Getenv("LOOM_PREREQUISITES_CHILD")
	if encoded == "" {
		return
	}
	if os.Geteuid() == 0 || applicationFixtureNoNewPrivileges() != nil {
		os.Exit(3)
	}
	raw, e := base64.StdEncoding.DecodeString(encoded)
	if e != nil {
		os.Exit(4)
	}
	var q ApplicationPrerequisiteQuery
	if applicationDecodeJSON(raw, &q) != nil {
		os.Exit(5)
	}
	out, e := (ApplicationHelperClient{SocketPath: os.Getenv("LOOM_PREREQUISITES_SOCKET")}).QueryPrerequisites(context.Background(), q)
	response := ApplicationHelperResponse{}
	if e != nil {
		response.ErrorCode = applicationSafeError(e)
	} else {
		response.Prerequisites = &out
	}
	_ = json.NewEncoder(os.Stdout).Encode(response)
	os.Exit(0)
}
