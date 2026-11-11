package serviceregistry

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

const applicationRuntimeOAuthFixture = `{"type":"authorized_user","client_id":"fixture-client","client_secret":"private-fixture-client","refresh_token":"private-fixture-refresh","token_uri":"https://oauth2.googleapis.com/token"}`
const applicationRuntimeOAuthScope = "https://www.googleapis.com/auth/gmail.readonly"

type applicationRuntimeCredentialVerifierFixture struct {
	calls int
	err   error
	hook  func()
	value []byte
}

func (v *applicationRuntimeCredentialVerifierFixture) ResolveApplicationCredential(context.Context, ApplicationOwner, string) ([]byte, error) {
	panic("runtime imports must not use the Proton resolver")
}
func (v *applicationRuntimeCredentialVerifierFixture) ApplicationRuntimeCredentialSourceUID() (uint32, error) {
	return uint32(os.Geteuid()), nil
}
func (v *applicationRuntimeCredentialVerifierFixture) VerifyApplicationRuntimeCredential(_ context.Context, value []byte, account string, scopes []string) error {
	v.calls++
	v.value = value
	if v.hook != nil {
		v.hook()
	}
	if string(value) != applicationRuntimeOAuthFixture || account != "fixture@example.com" || !slices.Equal(scopes, []string{applicationRuntimeOAuthScope}) {
		return errors.New("private-fixture bad verifier input")
	}
	return v.err
}

func applicationRuntimeImportFixture(t *testing.T) (ApplicationRuntime, ApplicationHostPolicy, ApplicationCredentialImport, *applicationRuntimeCredentialVerifierFixture) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("non-root fixture uses its own UID in place of the fixed agents account")
	}
	r, p, provision := applicationProvisionFixture(t)
	root := filepath.Dir(r.PolicyPath)
	p.Provisioning.RuntimeCredentialRoot = filepath.Join(root, "credentials")
	p.Provisioning.RuntimeCredentialImportRoots = []string{filepath.Join(root, "imports")}
	for _, path := range []string{p.Provisioning.RuntimeCredentialRoot, p.Provisioning.RuntimeCredentialImportRoots[0]} {
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	q := ApplicationCredentialImport{Owner: provision.Owner, SourcePath: filepath.Join(p.Provisioning.RuntimeCredentialImportRoots[0], "oauth.json"), ExpectedAccount: "fixture@example.com", ExpectedScopes: []string{applicationRuntimeOAuthScope}}
	if err := os.WriteFile(q.SourcePath, []byte(applicationRuntimeOAuthFixture), 0600); err != nil {
		t.Fatal(err)
	}
	v := &applicationRuntimeCredentialVerifierFixture{}
	r.Credentials = v
	applicationPrerequisitesWrite(t, r.PolicyPath, p)
	return r, p, q, v
}

func TestApplicationRuntimeCredentialImportAndResolve(t *testing.T) {
	r, p, q, v := applicationRuntimeImportFixture(t)
	receipt, err := r.ImportCredential(t.Context(), p.InstallerUID, q)
	if err != nil {
		t.Fatal(err)
	}
	id, valid := applicationRuntimeCredentialID(receipt.Reference)
	if !valid || receipt.Owner != q.Owner || receipt.Revision != ApplicationProtonCredentialRevision(receipt.Reference) || v.calls != 1 {
		t.Fatalf("invalid receipt %+v, calls %d", receipt, v.calls)
	}
	if strings.Trim(string(v.value), "\x00") != "" {
		t.Fatal("protected verification buffer was not cleared")
	}
	before := applicationPrerequisitesTree(t, filepath.Dir(r.PolicyPath))
	policy, err := r.ResolveRuntimeCredential(q.Owner, receipt.Reference)
	if err != nil || policy.Path != filepath.Join(p.Provisioning.RuntimeCredentialRoot, id+".credential") || policy.Revision != receipt.Revision {
		t.Fatal("wrong runtime binding", err)
	}
	value, err := os.ReadFile(policy.Path)
	if err != nil || string(value) != applicationRuntimeOAuthFixture {
		t.Fatal("snapshot bytes differ", err)
	}
	var st unix.Stat_t
	if unix.Lstat(policy.Path, &st) != nil || st.Mode&07777 != 0600 || st.Nlink != 1 || st.Uid != r.Store.OwnerUID {
		t.Fatal("snapshot custody")
	}
	v.err = errors.New("private-fixture should not be called on replay")
	for range 2 {
		got, err := r.ImportCredential(t.Context(), p.InstallerUID, q)
		if err != nil || got != receipt || v.calls != 1 {
			t.Fatal("replay changed binding or reverified", err)
		}
	}
	if !reflect.DeepEqual(before, applicationPrerequisitesTree(t, filepath.Dir(r.PolicyPath))) {
		t.Fatal("resolve/replay changed persistent state")
	}
	other := q.Owner
	other.Resource = "other"
	if _, err = r.ResolveRuntimeCredential(other, receipt.Reference); err == nil || err.Error() != "application.credential.runtime_owner_denied" {
		t.Fatal("foreign owner resolved", err)
	}
	for _, public := range []any{q, receipt, policy} {
		b, _ := json.Marshal(public)
		if strings.Contains(string(b), "private-fixture") {
			t.Fatal("public secret")
		}
	}
	entries, _ := os.ReadDir(r.Store.Root)
	for _, entry := range entries {
		b, err := os.ReadFile(filepath.Join(r.Store.Root, entry.Name()))
		if err != nil || strings.Contains(string(b), "private-fixture") {
			t.Fatal("secret in private metadata", err)
		}
	}
}

func TestApplicationRuntimeCredentialLostReply(t *testing.T) {
	for _, boundary := range []string{"credential:runtime_import_intent", "credential:runtime_import_snapshot", "credential:runtime_import_verified"} {
		t.Run(boundary, func(t *testing.T) {
			r, p, q, v := applicationRuntimeImportFixture(t)
			r.FailureHook = func(stage string) error {
				if stage == boundary {
					return errors.New("private-fixture interruption")
				}
				return nil
			}
			if _, err := r.ImportCredential(t.Context(), p.InstallerUID, q); err == nil || err.Error() != "application.credential.import_interrupted" {
				t.Fatal("boundary did not interrupt", err)
			}
			entries, _ := os.ReadDir(r.Store.Root)
			var intent applicationRuntimeCredentialRecord
			for _, entry := range entries {
				if strings.HasPrefix(entry.Name(), "runtime-credential-") && !strings.HasPrefix(entry.Name(), "runtime-credential-import-") {
					b, _ := os.ReadFile(filepath.Join(r.Store.Root, entry.Name()))
					if err := json.Unmarshal(b, &intent); err != nil {
						t.Fatal(err)
					}
				}
			}
			r.FailureHook = nil
			receipt, err := r.ImportCredential(t.Context(), p.InstallerUID, q)
			if err != nil || receipt.Reference != intent.Reference || receipt.Owner != q.Owner || v.calls != 1 {
				t.Fatal("lost reply did not reuse intent", err, v.calls)
			}
			files, _ := os.ReadDir(p.Provisioning.RuntimeCredentialRoot)
			if len(files) != 1 {
				t.Fatal("duplicate snapshot")
			}
		})
	}
}

func TestApplicationRuntimeCredentialImportAdmission(t *testing.T) {
	for _, name := range []string{"peer", "node", "account", "scopes", "outside", "prefix", "empty", "oversize", "mode", "readonly", "symlink", "ancestor_symlink", "hardlink", "directory", "fifo", "root_symlink", "root_mode"} {
		t.Run(name, func(t *testing.T) {
			r, p, q, v := applicationRuntimeImportFixture(t)
			peer := p.InstallerUID
			var err error
			switch name {
			case "peer":
				peer = p.PublisherUID
			case "node":
				q.Owner.NodeID = "node_01ARZ3NDEKTSV4RRFFQ69G5FAW"
			case "account":
				q.ExpectedAccount = "not-an-account"
			case "scopes":
				q.ExpectedScopes = append(q.ExpectedScopes, q.ExpectedScopes[0])
			case "outside":
				q.SourcePath = r.PolicyPath
			case "prefix":
				q.SourcePath = p.Provisioning.RuntimeCredentialImportRoots[0] + "-other/oauth.json"
			case "empty":
				err = os.WriteFile(q.SourcePath, nil, 0600)
			case "oversize":
				err = os.WriteFile(q.SourcePath, make([]byte, applicationRuntimeCredentialLimit+1), 0600)
			case "mode":
				err = os.Chmod(q.SourcePath, 0644)
			case "readonly":
				err = os.Chmod(q.SourcePath, 0400)
			case "symlink":
				original := q.SourcePath
				q.SourcePath += ".link"
				err = os.Symlink(original, q.SourcePath)
			case "ancestor_symlink":
				link := filepath.Join(p.Provisioning.RuntimeCredentialImportRoots[0], "link")
				err = os.Symlink(p.Provisioning.RuntimeCredentialImportRoots[0], link)
				q.SourcePath = filepath.Join(link, "oauth.json")
			case "hardlink":
				err = os.Link(q.SourcePath, q.SourcePath+".link")
			case "directory":
				q.SourcePath += ".dir"
				err = os.Mkdir(q.SourcePath, 0700)
			case "fifo":
				q.SourcePath += ".fifo"
				err = unix.Mkfifo(q.SourcePath, 0600)
			case "root_symlink":
				original := p.Provisioning.RuntimeCredentialRoot
				p.Provisioning.RuntimeCredentialRoot += ".link"
				err = os.Symlink(original, p.Provisioning.RuntimeCredentialRoot)
			case "root_mode":
				err = os.Chmod(p.Provisioning.RuntimeCredentialRoot, 0755)
			}
			if err != nil {
				t.Fatal(err)
			}
			applicationPrerequisitesWrite(t, r.PolicyPath, p)
			_, err = r.ImportCredential(t.Context(), peer, q)
			if name == "readonly" {
				if err != nil {
					t.Fatal("owner-only readonly source refused", err)
				}
				return
			}
			if err == nil || v.calls != 0 || strings.Contains(err.Error(), "private-fixture") {
				t.Fatal("unsafe import accepted or disclosed", err)
			}
		})
	}
}

func TestApplicationRuntimeCredentialVerificationAndMutation(t *testing.T) {
	for _, name := range []string{"arbitrary_error", "account", "scopes", "provider", "source_changed", "policy_changed"} {
		t.Run(name, func(t *testing.T) {
			r, p, q, v := applicationRuntimeImportFixture(t)
			want := "application.credential.oauth_verification_failed"
			switch name {
			case "arbitrary_error":
				v.err = errors.New("private-fixture raw upstream error")
			case "account", "scopes":
				want = "application.credential.oauth_" + name + "_mismatch"
				v.err = errors.New(want)
			case "provider":
				want = "application.credential.oauth_provider_unavailable"
				v.err = errors.New(want)
			case "source_changed":
				want = "application.credential.import_source_changed"
				v.hook = func() {
					if err := os.WriteFile(q.SourcePath, []byte("changed"), 0600); err != nil {
						t.Fatal(err)
					}
				}
			case "policy_changed":
				want = "application.credential.import_policy_changed"
				v.hook = func() { p.InstallerUID++; applicationPrerequisitesWrite(t, r.PolicyPath, p) }
			}
			if _, err := r.ImportCredential(t.Context(), p.InstallerUID, q); err == nil || err.Error() != want {
				t.Fatal("wrong finite error", err)
			}
			if strings.Trim(string(v.value), "\x00") != "" {
				t.Fatal("verification failure retained bytes")
			}
		})
	}
}

func TestApplicationRuntimeCredentialResolveReadOnlyAndCustody(t *testing.T) {
	r, p, q, _ := applicationRuntimeImportFixture(t)
	before := applicationPrerequisitesTree(t, filepath.Dir(r.PolicyPath))
	if _, err := r.ResolveRuntimeCredential(q.Owner, "runtime://"+strings.Repeat("1", 64)); err == nil {
		t.Fatal("missing binding resolved")
	}
	if !reflect.DeepEqual(before, applicationPrerequisitesTree(t, filepath.Dir(r.PolicyPath))) {
		t.Fatal("missing resolve created state")
	}
	receipt, err := r.ImportCredential(t.Context(), p.InstallerUID, q)
	if err != nil {
		t.Fatal(err)
	}
	policy, _ := r.ResolveRuntimeCredential(q.Owner, receipt.Reference)
	if err = os.WriteFile(policy.Path, []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	before = applicationPrerequisitesTree(t, filepath.Dir(r.PolicyPath))
	if _, err = r.ResolveRuntimeCredential(q.Owner, receipt.Reference); err == nil || err.Error() != "application.credential.runtime_custody" {
		t.Fatal("changed snapshot accepted", err)
	}
	if !reflect.DeepEqual(before, applicationPrerequisitesTree(t, filepath.Dir(r.PolicyPath))) {
		t.Fatal("failed resolve rewrote snapshot")
	}
}

func TestApplicationRuntimeCredentialSourceReplacementAndSnapshotLoss(t *testing.T) {
	r, p, q, v := applicationRuntimeImportFixture(t)
	first, err := r.ImportCredential(t.Context(), p.InstallerUID, q)
	if err != nil {
		t.Fatal(err)
	}
	newSource := q.SourcePath + ".new"
	if err := os.WriteFile(newSource, []byte(applicationRuntimeOAuthFixture), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(newSource, q.SourcePath); err != nil {
		t.Fatal(err)
	}
	second, err := r.ImportCredential(t.Context(), p.InstallerUID, q)
	if err != nil || first.Reference == second.Reference || first.Revision == second.Revision || v.calls != 2 {
		t.Fatal("new inode silently reused previous source binding", err)
	}
	policy, err := r.ResolveRuntimeCredential(q.Owner, first.Reference)
	if err != nil {
		t.Fatal("new source invalidated retained snapshot", err)
	}
	if err := os.Remove(policy.Path); err != nil {
		t.Fatal(err)
	}
	// Retrying the new source still resolves only the new reference, while a
	// lost old snapshot is not fetched from an arbitrary current source.
	if _, err := r.ResolveRuntimeCredential(q.Owner, first.Reference); err == nil {
		t.Fatal("lost old snapshot resolved")
	}
	policy, _ = r.ResolveRuntimeCredential(q.Owner, second.Reference)
	if err := os.Remove(policy.Path); err != nil {
		t.Fatal(err)
	}
	before := applicationPrerequisitesTree(t, filepath.Dir(r.PolicyPath))
	if _, err := r.ImportCredential(t.Context(), p.InstallerUID, q); err == nil || v.calls != 2 {
		t.Fatal("lost completed snapshot silently reconstructed", err)
	}
	if !reflect.DeepEqual(before, applicationPrerequisitesTree(t, filepath.Dir(r.PolicyPath))) {
		t.Fatal("recovery failure rewrote state")
	}
}

func TestApplicationRuntimeCredentialSnapshotCustody(t *testing.T) {
	for _, name := range []string{"symlink", "hardlink", "mode", "record_owner", "record_mode"} {
		t.Run(name, func(t *testing.T) {
			r, p, q, _ := applicationRuntimeImportFixture(t)
			receipt, err := r.ImportCredential(t.Context(), p.InstallerUID, q)
			if err != nil {
				t.Fatal(err)
			}
			policy, _ := r.ResolveRuntimeCredential(q.Owner, receipt.Reference)
			id, _ := applicationRuntimeCredentialID(receipt.Reference)
			switch name {
			case "symlink":
				if err = os.Remove(policy.Path); err == nil {
					err = os.Symlink(q.SourcePath, policy.Path)
				}
			case "hardlink":
				err = os.Link(policy.Path, policy.Path+".link")
			case "mode":
				err = os.Chmod(policy.Path, 0644)
			case "record_owner":
				var record applicationRuntimeCredentialRecord
				if err = r.runtimeCredentialReadRecord(id, &record); err == nil {
					record.Owner.Resource = "other"
					err = r.Store.write("runtime-credential-"+id, record)
				}
			case "record_mode":
				err = os.Chmod(filepath.Join(r.Store.Root, "runtime-credential-"+id+".json"), 0644)
			}
			if err != nil {
				t.Fatal(err)
			}
			before := applicationPrerequisitesTree(t, filepath.Dir(r.PolicyPath))
			if _, err = r.ResolveRuntimeCredential(q.Owner, receipt.Reference); err == nil {
				t.Fatal("unsafe custody resolved")
			}
			if !reflect.DeepEqual(before, applicationPrerequisitesTree(t, filepath.Dir(r.PolicyPath))) {
				t.Fatal("resolve changed rejected snapshot")
			}
		})
	}
}

type applicationOAuthTransport func(*http.Request) (*http.Response, error)

func (f applicationOAuthTransport) RoundTrip(q *http.Request) (*http.Response, error) { return f(q) }

func TestApplicationRuntimeCredentialGoogleVerification(t *testing.T) {
	for _, name := range []string{"valid", "extra_scope", "missing_scope", "account", "audience", "revoked", "unavailable", "redirect", "oversize", "raw_error", "bad_endpoint"} {
		t.Run(name, func(t *testing.T) {
			calls := 0
			client := &http.Client{Transport: applicationOAuthTransport(func(q *http.Request) (*http.Response, error) {
				calls++
				if q.URL.Scheme != "https" || q.URL.RawQuery != "" || calls > 3 {
					t.Fatal("unsafe endpoint or redirect")
				}
				status, body := 200, ""
				switch calls {
				case 1:
					if q.URL.String() != "https://oauth2.googleapis.com/token" || q.Method != http.MethodPost {
						t.Fatal("refresh endpoint")
					}
					if err := q.ParseForm(); err != nil || q.PostForm.Get("refresh_token") != "private-fixture-refresh" || q.PostForm.Get("client_secret") != "private-fixture-client" || q.PostForm.Get("scope") != "" {
						t.Fatal("refresh form changed authorization")
					}
					body = `{"access_token":"private-fixture-access","token_type":"Bearer"}`
				case 2:
					if q.URL.String() != "https://oauth2.googleapis.com/tokeninfo" || q.Method != http.MethodPost {
						t.Fatal("tokeninfo endpoint")
					}
					body = `{"aud":"fixture-client","scope":"` + applicationRuntimeOAuthScope + `"}`
					if name == "extra_scope" {
						body = `{"aud":"fixture-client","scope":"` + applicationRuntimeOAuthScope + ` https://mail.google.com/"}`
					}
					if name == "missing_scope" {
						body = `{"aud":"fixture-client","scope":""}`
					}
					if name == "audience" {
						body = `{"aud":"other-client","scope":"` + applicationRuntimeOAuthScope + `"}`
					}
				case 3:
					if q.URL.String() != "https://gmail.googleapis.com/gmail/v1/users/me/profile" || q.Header.Get("Authorization") != "Bearer private-fixture-access" {
						t.Fatal("profile request")
					}
					body = `{"emailAddress":"fixture@example.com"}`
					if name == "account" {
						body = `{"emailAddress":"other@example.com"}`
					}
				}
				if calls == 1 {
					switch name {
					case "revoked":
						status, body = 400, `{"error":"invalid_grant","error_description":"private-fixture"}`
					case "unavailable":
						status, body = 503, "private-fixture"
					case "redirect":
						status = 302
					case "oversize":
						body = strings.Repeat("x", applicationRuntimeCredentialLimit+1)
					case "raw_error":
						return nil, errors.New("private-fixture transport secret")
					}
				}
				return &http.Response{StatusCode: status, Header: http.Header{"Location": {"https://untrusted.example/"}}, Body: io.NopCloser(strings.NewReader(body)), Request: q}, nil
			})}
			value := []byte(applicationRuntimeOAuthFixture)
			if name == "bad_endpoint" {
				value = []byte(strings.ReplaceAll(string(value), "https://oauth2.googleapis.com/token", "https://untrusted.example/token"))
			}
			err := applicationVerifyGoogleOAuth(t.Context(), client, value, "fixture@example.com", []string{applicationRuntimeOAuthScope})
			if name == "valid" {
				if err != nil || calls != 3 {
					t.Fatal("valid credential refused", err, calls)
				}
				return
			}
			want := map[string]string{"extra_scope": "oauth_scopes_mismatch", "missing_scope": "oauth_scopes_mismatch", "account": "oauth_account_mismatch", "audience": "oauth_provider_invalid", "revoked": "oauth_revoked", "unavailable": "oauth_provider_unavailable", "redirect": "oauth_provider_invalid", "oversize": "oauth_provider_invalid", "raw_error": "oauth_provider_unavailable", "bad_endpoint": "oauth_invalid"}[name]
			if err == nil || err.Error() != "application.credential."+want || name == "bad_endpoint" && calls != 0 {
				t.Fatal("wrong provider error", err, calls)
			}
		})
	}
}
