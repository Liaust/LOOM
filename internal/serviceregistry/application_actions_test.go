package serviceregistry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

type applicationActionFixture struct {
	*applicationFixtureHost
	runtime      *ApplicationRuntime
	phase        ApplicationActionPhase
	exit         int
	actions      int
	stops        int
	resumes      int
	observations int
	submitError  bool
	resumeError  bool
	execution    applicationActionExecution
}

func (f *applicationActionFixture) Stop(ctx context.Context, g ApplicationGeneration) error {
	f.stops++
	if err := f.applicationFixtureHost.Stop(ctx, g); err != nil {
		return err
	}
	f.process.PID = 0
	return nil
}

func (f *applicationActionFixture) Start(ctx context.Context, g ApplicationGeneration) error {
	f.resumes++
	if f.resumeError {
		return errors.New("resume delivery uncertain")
	}
	return f.applicationFixtureHost.Start(ctx, g)
}

func (f *applicationActionFixture) startApplicationAction(_ context.Context, e applicationActionExecution) error {
	j, err := f.runtime.readAction(e.Request)
	if err != nil || j.Receipt.Phase != ApplicationActionSubmitting || !j.Receipt.ServicePaused {
		return errors.New("submission before durable stopped intent")
	}
	active, err := f.runtime.applicationActionActive(e.Request.Owner)
	if err != nil || !active || f.process.State != "inactive" || f.process.PID != 0 {
		return errors.New("submission without exclusive owner fence")
	}
	f.actions++
	f.execution, f.phase = e, ApplicationActionPending
	if f.submitError {
		return errors.New("possibly delivered, secret command output must not escape")
	}
	return nil
}

func (f *applicationActionFixture) observeApplicationAction(_ context.Context, _ applicationActionExecution) (applicationActionObservation, error) {
	f.observations++
	if f.actions == 0 {
		return applicationActionObservation{Phase: ApplicationActionUncertain}, nil
	}
	var code *int
	if f.phase == ApplicationActionSucceeded || f.phase == ApplicationActionFailed {
		code = &f.exit
	}
	return applicationActionObservation{Phase: f.phase, ExitCode: code}, nil
}

func applicationActionTestRuntime(t *testing.T) (ApplicationRuntime, *applicationActionFixture, ApplicationActionRequest) {
	t.Helper()
	r, base, request, publication := applicationTestRuntime(t)
	policy, err := r.policy()
	if err != nil {
		t.Fatal(err)
	}
	d := publication.Descriptor
	d.SchemaVersion, d.RepositoryID, d.SourceIdentity = "application.artifact.v2", "", request.Owner.ProjectID+"/"+request.Owner.Resource
	d.Actions = map[string]ApplicationActionDefinition{"bootstrap": {}, "resync": {}, "inspect": {ReadOnly: true}}
	policy.Grants[0].RepositoryID, policy.Grants[0].SourceIdentity = "", d.SourceIdentity
	raw, err := json.Marshal(policy)
	if err != nil {
		t.Fatal(err)
	}
	if err = applicationAtomic(r.PolicyPath, raw, 0600); err != nil {
		t.Fatal(err)
	}
	publication.ExpectedRevision, publication.Revision, publication.Descriptor = publication.Revision, "revision-actions", d
	if err = r.Publish(t.Context(), policy.PublisherUID, publication); err != nil {
		t.Fatal(err)
	}
	request.ExpectedRevision, request.DescriptorDigest = publication.Revision, applicationSHA(d)
	f := &applicationActionFixture{applicationFixtureHost: base}
	r.System = f
	receipt, err := r.Execute(t.Context(), policy.InstallerUID, request)
	if err != nil {
		t.Fatal(err)
	}
	f.runtime = &r
	return r, f, ApplicationActionRequest{Owner: request.Owner, Name: "bootstrap", Token: "bootstrap-a", ExpectedInstallationRevision: receipt.InstallationRevision}
}

func TestApplicationActionPendingInspectAndOnceOnlySuccess(t *testing.T) {
	r, f, q := applicationActionTestRuntime(t)
	receipt, err := r.RunAction(t.Context(), 1234, q)
	if err != nil || receipt.Phase != ApplicationActionPending || !receipt.ServicePaused || receipt.ServiceResumed || receipt.Finalized || f.actions != 1 || f.stops != 1 {
		t.Fatalf("pending receipt=%+v err=%v effects=%d/%d", receipt, err, f.actions, f.stops)
	}
	if active, err := r.applicationActionActive(q.Owner); err != nil || !active {
		t.Fatal("pending unit has no durable fence", err)
	}
	release, err := r.Store.lock(t.Context(), q.Owner.Instance())
	if err != nil {
		t.Fatal("async unit retained flock:", err)
	}
	release()
	if _, err = r.RunAction(t.Context(), 1234, q); err != nil || f.actions != 1 || f.stops != 1 {
		t.Fatal("pending token resubmitted action:", err)
	}
	f.phase, f.exit = ApplicationActionSucceeded, 0
	before, err := os.ReadFile(filepath.Join(r.Store.Root, applicationActionKey(q)+".json"))
	if err != nil {
		t.Fatal(err)
	}
	receipt, err = r.InspectAction(t.Context(), 1234, q)
	if err != nil || receipt.Phase != ApplicationActionSucceeded || !receipt.ServicePaused || receipt.ServiceResumed || receipt.Finalized || f.resumes != 0 {
		t.Fatalf("read-only inspection finalized action: %+v %v", receipt, err)
	}
	after, _ := os.ReadFile(filepath.Join(r.Store.Root, applicationActionKey(q)+".json"))
	if !reflect.DeepEqual(before, after) {
		t.Fatal("InspectAction wrote journal")
	}
	if active, _ := r.applicationActionActive(q.Owner); !active {
		t.Fatal("inspection released lease")
	}
	receipt, err = r.RunAction(t.Context(), 1234, q)
	if err != nil || receipt.Phase != ApplicationActionSucceeded || !receipt.Finalized || !receipt.ServiceResumed || f.resumes != 1 || f.actions != 1 {
		t.Fatalf("success did not reconcile exactly once: %+v %v", receipt, err)
	}
	if active, err := r.applicationActionActive(q.Owner); active || err != nil {
		t.Fatal("completed lease not released", err)
	}
	if _, err = r.RunAction(t.Context(), 1234, q); err != nil || f.actions != 1 || f.resumes != 1 {
		t.Fatal("lost success reply duplicated effects", err)
	}
}

func TestApplicationActionFailedAndUncertainStayHeld(t *testing.T) {
	for _, name := range []string{"failed", "lostSubmitReply", "lostResumeReply"} {
		t.Run(name, func(t *testing.T) {
			r, f, q := applicationActionTestRuntime(t)
			f.submitError = name == "lostSubmitReply"
			first, err := r.RunAction(t.Context(), 1234, q)
			if f.actions != 1 || !first.ServicePaused {
				t.Fatalf("not submitted under paused owner: %+v %v", first, err)
			}
			if name == "lostSubmitReply" {
				if err == nil || first.Phase != ApplicationActionUncertain || strings.Contains(first.ErrorCode, "secret") {
					t.Fatal("uncertain submission leaked or was retried")
				}
				f.submitError = false
				if _, err = r.RunAction(t.Context(), 1234, q); err != nil || f.actions != 1 {
					t.Fatal("lost submission reply replayed action", err)
				}
			}
			f.phase, f.exit = ApplicationActionFailed, 17
			if name == "lostResumeReply" {
				f.phase, f.exit, f.resumeError = ApplicationActionSucceeded, 0, true
			}
			receipt, err := r.RunAction(t.Context(), 1234, q)
			if name == "lostResumeReply" {
				if err == nil || receipt.Phase != ApplicationActionUncertain || f.resumes != 1 {
					t.Fatalf("resume uncertainty not held: %+v %v", receipt, err)
				}
				f.resumeError = false
			} else if err != nil || receipt.Phase != ApplicationActionFailed || receipt.ExitCode == nil || *receipt.ExitCode != 17 {
				t.Fatalf("failed receipt=%+v %v", receipt, err)
			}
			_, _ = r.RunAction(t.Context(), 1234, q)
			if f.actions != 1 || f.resumes > 1 || f.process.State != "inactive" {
				t.Fatal("failed/uncertain token replayed or resumed service")
			}
			if active, err := r.applicationActionActive(q.Owner); !active || err != nil {
				t.Fatal("failed action lost its hold", err)
			}
			other := q
			other.Token = "another-action"
			if _, err := r.RunAction(t.Context(), 1234, other); err == nil || f.actions != 1 {
				t.Fatal("competing action bypassed held lease")
			}
		})
	}
}

func TestApplicationActionIntentCrashNeverResubmits(t *testing.T) {
	for _, stage := range []string{"action:intent", "action:submit_intent", "action:submitted"} {
		t.Run(stage, func(t *testing.T) {
			r, f, q := applicationActionTestRuntime(t)
			r.FailureHook = func(at string) error {
				if at == stage {
					return errors.New("interrupted")
				}
				return nil
			}
			if _, err := r.RunAction(t.Context(), 1234, q); err == nil {
				t.Fatal("did not interrupt")
			}
			initial := f.actions
			r.FailureHook = nil
			_, _ = r.RunAction(t.Context(), 1234, q)
			if f.actions != initial || f.actions > 1 || f.resumes != 0 {
				t.Fatal("uncertain intent resubmitted action")
			}
			if active, err := r.applicationActionActive(q.Owner); !active || err != nil {
				t.Fatal("interrupted action lost fence", err)
			}
		})
	}
}

func TestApplicationActionAdmissionAndTokenConflict(t *testing.T) {
	r, f, q := applicationActionTestRuntime(t)
	if _, err := r.InspectAction(t.Context(), 1234, q); err == nil || err.Error() != "application.action.missing" {
		t.Fatal("absence is not finite", err)
	}
	if _, err := r.RunAction(t.Context(), 0, q); err == nil || f.actions != 0 {
		t.Fatal("publisher gained action authority")
	}
	for _, name := range []string{"not-declared", "bootstrap --shell", "../bootstrap"} {
		bad := q
		bad.Name = name
		if _, err := r.RunAction(t.Context(), 1234, bad); err == nil {
			t.Fatal("arbitrary action admitted")
		}
	}
	if _, err := r.RunAction(t.Context(), 1234, q); err != nil {
		t.Fatal(err)
	}
	changed := q
	changed.Name = "resync"
	if _, err := r.RunAction(t.Context(), 1234, changed); err == nil || err.Error() != "application.action.token_conflict" || f.actions != 1 {
		t.Fatal("token was rebound", err)
	}
}

func TestApplicationActionPolicyChangePreventsStartOrResume(t *testing.T) {
	for _, before := range []bool{false, true} {
		t.Run(fmt.Sprint(before), func(t *testing.T) {
			r, f, q := applicationActionTestRuntime(t)
			change := func() {
				policy, err := r.policy()
				if err != nil {
					t.Fatal(err)
				}
				policy.Grants[0].PolicyRevision = "revoked"
				raw, _ := json.Marshal(policy)
				if err = applicationAtomic(r.PolicyPath, raw, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if before {
				r.FailureHook = func(stage string) error {
					if stage == "action:submit_intent" {
						change()
					}
					return nil
				}
				if _, err := r.RunAction(t.Context(), 1234, q); err == nil || f.actions != 0 {
					t.Fatal("changed policy submitted an action")
				}
			} else {
				if _, err := r.RunAction(t.Context(), 1234, q); err != nil {
					t.Fatal(err)
				}
				f.phase, f.exit = ApplicationActionSucceeded, 0
				change()
				if _, err := r.RunAction(t.Context(), 1234, q); err == nil || f.resumes != 0 {
					t.Fatal("changed policy resumed service")
				}
			}
			if active, _ := r.applicationActionActive(q.Owner); !active {
				t.Fatal("changed policy released action hold")
			}
		})
	}
}

func TestApplicationActionConfigChangesOnlyTypedCredential(t *testing.T) {
	r, _, q := applicationActionTestRuntime(t)
	e, _, err := r.actionContext(t.Context(), 1234, q, false)
	if err != nil {
		t.Fatal(err)
	}
	d := e.Generation.Descriptor
	m := ApplicationManifest{Config: ApplicationConfiguration{Schema: d.Configuration.Schema, Values: map[string]ApplicationParameter{}}}
	literal := "/run/credentials/" + q.Owner.Unit() + "/literal-must-stay"
	ref := "oauth.token"
	large := int64(9007199254740993)
	d.Configuration.Parameters = map[string]ApplicationParameterSpec{"literal": {Type: ApplicationString}, "token": {Type: ApplicationCredentialReference}, "large": {Type: ApplicationInteger}}
	m.Config.Values = map[string]ApplicationParameter{"literal": {Type: ApplicationString, String: &literal}, "token": {Type: ApplicationCredentialReference, CredentialRef: &ref}, "large": {Type: ApplicationInteger, Integer: &large}}
	e.Grant.Credentials = map[string]ApplicationCredentialPolicy{ref: {Path: "/private/token", Revision: "v1"}}
	raw, err := applicationActionConfiguration(m, d, e.Grant, e.Unit)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), literal) || !strings.Contains(string(raw), "/run/credentials/"+e.Unit+"/"+applicationCredentialName(ref)) || !strings.Contains(string(raw), "9007199254740993") {
		t.Fatal("rewrote literal config or lost numeric precision:", string(raw))
	}
}

func TestApplicationActionBoundedArgsAndUnitObservation(t *testing.T) {
	r, _, q := applicationActionTestRuntime(t)
	e, _, err := r.actionContext(t.Context(), 1234, q, false)
	if err != nil {
		t.Fatal(err)
	}
	path := "/private/config/actions/config.json"
	args, err := applicationActionCommandArgs(e, path)
	if err != nil {
		t.Fatal(err)
	}
	last := args[len(args)-5:]
	if !reflect.DeepEqual(last, []string{"--", e.Generation.Descriptor.Launcher, "--action", q.Name, path}) {
		t.Fatal("not a direct fixed launcher", last)
	}
	joined := strings.Join(args, "\n")
	for _, required := range []string{"--no-block", "RemainAfterExit=yes", "RuntimeMaxSec=300", "StandardOutput=null", "StandardError=null", "IPAddressDeny=any", "ProtectSystem=strict", "CapabilityBoundingSet=", "SSL_CERT_FILE=/etc/ssl/certs/ca-certificates.crt"} {
		if !strings.Contains(joined, required) {
			t.Fatal("missing fixed safety property:", required)
		}
	}
	if strings.Contains(joined, "--collect") || strings.Contains(joined, "/bin/sh") {
		t.Fatal("action uses a shell or unloads recovery evidence")
	}
	environments := 0
	for _, arg := range args {
		if strings.HasPrefix(arg, "--property=Environment=") {
			environments++
			if !strings.Contains(arg, "LOOM_APPLICATION_GENERATION="+e.Generation.ID) || !strings.Contains(arg, "SSL_CERT_FILE=/etc/ssl/certs/ca-certificates.crt") {
				t.Fatal("source action environment replaced generation or CA binding")
			}
		}
	}
	if environments != 1 {
		t.Fatal("environment was assigned more than once")
	}
	readOnly := e
	readOnly.Request.Name = "inspect"
	readOnly.Unit = applicationActionUnit(readOnly.Request)
	readOnly.Grant.Data = map[string]ApplicationDataPolicy{"state": {Path: "/state/data", Pool: "/state"}}
	readOnlyArgs, err := applicationActionCommandArgs(readOnly, path)
	if err != nil || !strings.Contains(strings.Join(readOnlyArgs, "\n"), "--property=ReadOnlyPaths=/state/data") || strings.Contains(strings.Join(readOnlyArgs, "\n"), "--property=ReadWritePaths=/state/data") {
		t.Fatal("read-only package action received writable state", err)
	}
	v := map[string]string{}
	for _, key := range strings.Split(applicationActionUnitProperties, ",") {
		v[key] = ""
	}
	v["LoadState"], v["Transient"], v["Type"] = "loaded", "yes", "exec"
	v["Description"] = "LOOM action " + applicationSHA(q)
	v["User"], v["Group"] = fmt.Sprint(e.Generation.UID), fmt.Sprint(e.Generation.UID)
	v["ExecStart"] = "{ path=" + e.Generation.Descriptor.Launcher + " ; argv[]=" + e.Generation.Descriptor.Launcher + " --action " + q.Name + " " + path + " ; ignore_errors=no ; }"
	v["ActiveState"], v["SubState"], v["MainPID"], v["ControlPID"], v["TasksCurrent"] = "active", "exited", "0", "0", "0"
	v["ExecMainCode"], v["ExecMainStatus"], v["Result"] = "1", "0", "success"
	render := func() []byte {
		var b strings.Builder
		for _, key := range strings.Split(applicationActionUnitProperties, ",") {
			fmt.Fprintf(&b, "%s=%s\n", key, v[key])
		}
		return []byte(b.String())
	}
	if got := applicationActionUnitObservation(render(), e, path); got.Phase != ApplicationActionSucceeded || got.ExitCode == nil || *got.ExitCode != 0 {
		t.Fatal("lost retained unit result", got)
	}
	v["TasksCurrent"] = "[not set]"
	if got := applicationActionUnitObservation(render(), e, path); got.Phase != ApplicationActionSucceeded {
		t.Fatal("lost completed result after empty cgroup was released", got)
	}
	v["ControlGroup"] = "/system.slice/retained-action.service"
	if got := applicationActionUnitObservation(render(), e, path); got.Phase != ApplicationActionUncertain {
		t.Fatal("missing task count accepted for an existing cgroup", got)
	}
	v["TasksCurrent"], v["ControlGroup"] = "0", ""
	for key, bad := range map[string]string{"User": "0", "Description": "other unit", "TasksCurrent": "1", "ExecStart": "{ path=/bin/sh ; argv[]=/bin/sh -c bootstrap ; }", "Transient": "no", "ExecMainCode": "0"} {
		old := v[key]
		v[key] = bad
		if got := applicationActionUnitObservation(render(), e, path); got.Phase != ApplicationActionUncertain {
			t.Fatal("unproven unit accepted:", key, got)
		}
		v[key] = old
	}
	v["ActiveState"], v["SubState"], v["MainPID"] = "active", "running", "234"
	if got := applicationActionUnitObservation(render(), e, path); got.Phase != ApplicationActionPending {
		t.Fatal("live asynchronous action not pending")
	}
	v["ActiveState"], v["SubState"], v["MainPID"], v["Result"], v["ExecMainStatus"] = "failed", "failed", "0", "exit-code", strconv.Itoa(23)
	if got := applicationActionUnitObservation(render(), e, path); got.Phase != ApplicationActionFailed || got.ExitCode == nil || *got.ExitCode != 23 {
		t.Fatal("lost failed result", got)
	}
	v["ActiveState"], v["SubState"], v["Job"], v["ExecMainCode"] = "inactive", "dead", "42", "0"
	if got := applicationActionUnitObservation(render(), e, path); got.Phase != ApplicationActionPending {
		t.Fatal("queued asynchronous start not pending")
	}
}

func TestApplicationActionPriorStoppedRemainsStopped(t *testing.T) {
	r, f, q := applicationActionTestRuntime(t)
	f.process.State, f.process.PID = "inactive", 0
	q.Name = "inspect"
	receipt, err := r.RunAction(t.Context(), 1234, q)
	if err != nil || !receipt.ReadOnly || receipt.Phase != ApplicationActionPending {
		t.Fatalf("read-only action was not admitted: %+v %v", receipt, err)
	}
	f.phase, f.exit = ApplicationActionSucceeded, 0
	receipt, err = r.RunAction(t.Context(), 1234, q)
	if err != nil || !receipt.Finalized || receipt.ServiceResumed || f.resumes != 0 || f.process.State != "inactive" {
		t.Fatalf("action revived intentionally stopped service: %+v %v", receipt, err)
	}
}

func TestApplicationActionCollectedFailureRequiresExactRootManagerEvidence(t *testing.T) {
	_, _, q := applicationActionTestRuntime(t)
	e := applicationActionExecution{Request: q, Unit: applicationActionUnit(q)}
	base := map[string]string{"_UID": "0", "_PID": "1", "SYSLOG_IDENTIFIER": "systemd", "UNIT": e.Unit, "INVOCATION_ID": strings.Repeat("a", 32)}
	record := func(changes map[string]string) map[string]string {
		v := map[string]string{}
		for k, value := range base {
			v[k] = value
		}
		for k, value := range changes {
			v[k] = value
		}
		return v
	}
	start := record(map[string]string{"MESSAGE_ID": "7d4958e842da4a758f6c1cdc7b36dcc5", "MESSAGE": "Starting LOOM action " + applicationSHA(q) + "..."})
	end := record(map[string]string{"MESSAGE_ID": "98e322203f7a4ed290d09fe03c09fe15", "EXIT_CODE": "exited", "EXIT_STATUS": "2"})
	render := func(records ...map[string]string) []byte {
		var raw []byte
		for _, v := range records {
			line, _ := json.Marshal(v)
			raw = append(raw, line...)
			raw = append(raw, '\n')
		}
		return raw
	}
	if got := applicationActionJournalFailure(render(start, end), e); got.Phase != ApplicationActionFailed || got.ExitCode == nil || *got.ExitCode != 2 {
		t.Fatal("lost authenticated collected failure", got)
	}
	for key, bad := range map[string]string{"_UID": "1002", "_PID": "99", "UNIT": "other.service", "INVOCATION_ID": strings.Repeat("b", 32), "EXIT_CODE": "killed", "EXIT_STATUS": "0"} {
		old := end[key]
		end[key] = bad
		if got := applicationActionJournalFailure(render(start, end), e); got.Phase != ApplicationActionUncertain {
			t.Fatal("accepted unproven exit", key, got)
		}
		end[key] = old
	}
	for _, raw := range [][]byte{render(end), render(start), render(start, start, end), []byte("invalid"), make([]byte, 65537)} {
		if got := applicationActionJournalFailure(raw, e); got.Phase != ApplicationActionUncertain {
			t.Fatal("accepted incomplete or ambiguous evidence", got)
		}
	}
	start["MESSAGE"] = "Starting a different request..."
	if got := applicationActionJournalFailure(render(start, end), e); got.Phase != ApplicationActionUncertain {
		t.Fatal("accepted another request's result", got)
	}
}

func TestApplicationActionExplicitFailureReleasePreservesReceiptAndNeverRunsOrResumes(t *testing.T) {
	for _, journalFailure := range []bool{false, true} {
		t.Run(fmt.Sprint(journalFailure), func(t *testing.T) {
			r, f, q := applicationActionTestRuntime(t)
			if _, err := r.RunAction(t.Context(), 1234, q); err != nil {
				t.Fatal(err)
			}
			release := q
			release.ReleaseFailed = true
			if _, err := r.RunAction(t.Context(), 1234, release); err == nil {
				t.Fatal("released a still-running action")
			}
			f.phase, f.exit = ApplicationActionFailed, 17
			if journalFailure {
				if _, err := r.RunAction(t.Context(), 1234, q); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := r.RunAction(t.Context(), 9999, release); err == nil {
				t.Fatal("unprivileged release succeeded")
			}
			receipt, err := r.RunAction(t.Context(), 1234, release)
			if err != nil || !receipt.Finalized || receipt.Phase != ApplicationActionFailed || receipt.ExitCode == nil || *receipt.ExitCode != 17 || receipt.ServiceResumed || f.actions != 1 || f.resumes != 0 {
				t.Fatalf("release lost failure evidence or executed effects: %+v %v", receipt, err)
			}
			if active, err := r.applicationActionActive(q.Owner); err != nil || active {
				t.Fatal("reviewed failed hold was not released", err)
			}
			if _, err := r.RunAction(t.Context(), 1234, release); err != nil || f.actions != 1 || f.resumes != 0 {
				t.Fatal("release replay was not idempotent", err)
			}
			if _, err := r.RunAction(t.Context(), 1234, q); err != nil || f.actions != 1 || f.resumes != 0 {
				t.Fatal("original failed action was repeated after release", err)
			}
			if _, err := r.InspectAction(t.Context(), 1234, release); err == nil {
				t.Fatal("read-only observer accepted a mutation flag")
			}
		})
	}
}

func TestApplicationActionFenceBlocksApplyAndBootRestore(t *testing.T) {
	r, f, q := applicationActionTestRuntime(t)
	if _, err := r.RunAction(t.Context(), 1234, q); err != nil {
		t.Fatal(err)
	}
	var installation ApplicationInstallation
	if err := r.Store.read("installation-"+q.Owner.Instance(), &installation); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Execute(t.Context(), 1234, *installation.Request); err == nil || err.Error() != "application.action.active" {
		t.Fatal("Execute bypassed asynchronous action lease", err)
	}
	observations, err := r.RestoreCommitted(t.Context())
	if err != nil || len(observations) != 1 || observations[0].State != "held" || observations[0].ErrorCode != "application.action.active" || f.resumes != 0 || f.process.State != "inactive" {
		t.Fatalf("boot bypassed the hold or treated it as a host failure: %+v %v", observations, err)
	}
	if err := r.Store.write("active-action-"+q.Owner.Instance(), applicationActionLease{}); err != nil {
		t.Fatal(err)
	}
	observations, err = r.RestoreCommitted(t.Context())
	if err == nil || len(observations) != 1 || observations[0].State != "failed" || observations[0].ErrorCode != "application.action.lease_uncertain" || f.resumes != 0 {
		t.Fatalf("unreadable action fence was softened into a normal hold: %+v %v", observations, err)
	}
}
