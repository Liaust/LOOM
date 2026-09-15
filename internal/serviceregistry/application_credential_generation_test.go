package serviceregistry

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type applicationGenerationFixture struct {
	applicationCredentialFixture
	create, find         int
	viewer, lost, absent bool
	title                string
}

func (f *applicationGenerationFixture) CheckApplicationCredentialGeneration(context.Context, ApplicationOwner, string) error {
	if f.viewer {
		return applicationError("credential.generation_requires_editor")
	}
	return nil
}
func (f *applicationGenerationFixture) CreateApplicationCredential(_ context.Context, _ ApplicationOwner, _, title string) (string, error) {
	f.create++
	f.title = title
	if f.lost {
		return "", errors.New("uncertain remote response with secret")
	}
	return "created-item==", nil
}
func (f *applicationGenerationFixture) FindApplicationCredential(_ context.Context, _ ApplicationOwner, _, title string) (string, error) {
	f.find++
	if f.absent || title != f.title {
		return "", nil
	}
	return "created-item==", nil
}

func TestApplicationGeneratedCredentialsRetryWithoutRotation(t *testing.T) {
	for _, name := range []string{"normal", "lost_reply", "intent_interruption", "post_create_interruption", "viewer", "disabled"} {
		t.Run(name, func(t *testing.T) {
			r, p, q, _ := applicationProtonFixture(t)
			p.Provisioning.Proton.AllowGeneration = name != "disabled"
			q.CredentialSources["fixture.auth"] = "pass+generate://share-id/password"
			f := &applicationGenerationFixture{viewer: name == "viewer", lost: name == "lost_reply"}
			r.Credentials = f
			applicationPrerequisitesWrite(t, r.PolicyPath, p)
			plan, err := r.PlanProvision(t.Context(), p.InstallerUID, q)
			if name == "disabled" {
				if err == nil {
					t.Fatal("disabled generation accepted")
				}
				return
			}
			if err != nil || f.create != 0 || f.find != 0 {
				t.Fatal("plan has credential effect", err)
			}
			r.FailureHook = func(stage string) error {
				if (name == "intent_interruption" && stage == "credential:generation_intent") || (name == "post_create_interruption" && stage == "credential:generated") {
					return errors.New("interrupted")
				}
				return nil
			}
			_, err = r.Provision(t.Context(), p.InstallerUID, plan)
			if name == "normal" && err != nil {
				t.Fatal(err)
			}
			if name != "normal" && err == nil {
				t.Fatal("failure hidden")
			}
			if err != nil && strings.Contains(err.Error(), "secret") {
				t.Fatal("secret in error")
			}
			if name == "viewer" {
				if f.create != 0 {
					t.Fatal("viewer created item")
				}
				entries, _ := filepath.Glob(filepath.Join(r.Store.Root, "credential-*.json"))
				if len(entries) != 0 {
					t.Fatal("viewer created generation intent")
				}
				return
			}
			r.FailureHook = nil
			_, err = r.Provision(t.Context(), p.InstallerUID, plan)
			if name == "intent_interruption" {
				if err == nil || f.create != 0 || f.calls != 0 {
					t.Fatal("uncertain intent retried create", err)
				}
				return
			}
			if err != nil || f.create != 1 || f.calls != 1 {
				t.Fatal("replay failed or duplicated", err, f)
			}
			g, _ := r.resolveGrant(p, q.Owner)
			b, err := os.ReadFile(g.Credentials["fixture.auth"].Path)
			if err != nil || string(b) != "private-fixture-value\n" {
				t.Fatal("missing private materialization")
			}
			if _, err = r.Provision(t.Context(), p.InstallerUID, plan); err != nil || f.create != 1 || f.calls != 1 {
				t.Fatal("second replay rotated", err)
			}
			for _, state := range []string{"grant-" + q.Owner.Instance(), "credential-" + q.Owner.Instance() + "-" + applicationCredentialName("fixture.auth")} {
				raw, err := os.ReadFile(filepath.Join(r.Store.Root, state+".json"))
				if err != nil || strings.Contains(string(raw), "private-fixture-value") {
					t.Fatal("secret in metadata", err)
				}
			}
		})
	}
}
