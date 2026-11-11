package serviceregistry

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/mail"
	"net/url"
	"os"
	"os/user"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

const applicationRuntimeCredentialLimit = 16 * 1024

// Import carries custody and authorization expectations, never credential bytes.
type ApplicationCredentialImport struct {
	Owner           ApplicationOwner `json:"owner"`
	SourcePath      string           `json:"source_path"`
	ExpectedAccount string           `json:"expected_account"`
	ExpectedScopes  []string         `json:"expected_scopes"`
}

type ApplicationCredentialImportReceipt struct {
	Owner     ApplicationOwner `json:"owner"`
	Reference string           `json:"reference"`
	Revision  string           `json:"revision"`
}

// Optional protected-helper adapters on ApplicationRuntime.Credentials. They
// are startup dependencies, never request/policy-selected provider endpoints.
type ApplicationRuntimeCredentialVerifier interface {
	VerifyApplicationRuntimeCredential(context.Context, []byte, string, []string) error
}

type ApplicationRuntimeCredentialSourceOwner interface {
	ApplicationRuntimeCredentialSourceUID() (uint32, error)
}

type applicationRuntimeCredentialFileIdentity struct {
	Device, Inode, Links uint64
	UID, GID, Mode       uint32
	Size                 int64
	Modified, Changed    unix.Timespec
}

func applicationRuntimeCredentialIdentity(st unix.Stat_t) applicationRuntimeCredentialFileIdentity {
	return applicationRuntimeCredentialFileIdentity{uint64(st.Dev), st.Ino, uint64(st.Nlink), st.Uid, st.Gid, uint32(st.Mode), st.Size, st.Mtim, st.Ctim}
}

// Both records live in the existing private store. Intent and lookup precede
// publication; a crash cannot publish bytes without a durable retry identity.
type applicationRuntimeCredentialRecord struct {
	Owner           ApplicationOwner                         `json:"owner"`
	Reference       string                                   `json:"reference"`
	ExpectedAccount string                                   `json:"expected_account"`
	Scopes          []string                                 `json:"scopes"`
	ImportIdentity  string                                   `json:"import_identity"`
	FileIdentity    applicationRuntimeCredentialFileIdentity `json:"file_identity"`
	Ready           bool                                     `json:"ready"`
}

type applicationRuntimeCredentialLookup struct {
	ImportIdentity string `json:"import_identity"`
	Reference      string `json:"reference"`
}

func applicationRuntimeCredentialID(ref string) (string, bool) {
	id, ok := strings.CutPrefix(ref, "runtime://")
	return id, ok && len(id) == 64 && strings.Trim(id, "0123456789abcdef") == ""
}

func applicationRuntimeCredentialExpectations(account string, scopes []string) ([]string, bool) {
	a, err := mail.ParseAddress(account)
	if err != nil || a.Address != account || len(account) > 254 || len(scopes) == 0 || len(scopes) > 32 {
		return nil, false
	}
	out := slices.Clone(scopes)
	slices.Sort(out)
	total := 0
	for i, scope := range out {
		total += len(scope)
		if len(scope) == 0 || len(scope) > 512 || strings.ContainsAny(scope, " \t\r\n\x00") || (i > 0 && scope == out[i-1]) {
			return nil, false
		}
	}
	return out, total <= 8192
}

func (r ApplicationRuntime) runtimeCredentialPolicy(owner ApplicationOwner) (ApplicationHostPolicy, string, error) {
	p, err := r.policy()
	if err != nil {
		return p, "", err
	}
	if !owner.valid() || owner.NodeID != p.NodeID {
		return p, "", applicationError("credential.import_owner_denied")
	}
	if _, err = p.provisioningRevision(); err != nil {
		return p, "", err
	}
	root := p.Provisioning.RuntimeCredentialRoot
	if !applicationAbsolutePath(root) || root == "/" || strings.HasPrefix(root, "/nix/store/") {
		return p, "", applicationError("credential.import_disabled")
	}
	// Reuse the descriptor-relative, no-follow custody walker. Nothing is made
	// here, including on Resolve's missing-state paths.
	f, err := applicationPrerequisiteParent(filepath.Join(root, "credential"), r.Store.OwnerUID, false)
	if err != nil {
		return p, "", applicationError("credential.import_root_custody")
	}
	defer f.Close()
	var st unix.Stat_t
	if unix.Fstat(int(f.Fd()), &st) != nil || st.Uid != r.Store.OwnerUID || st.Mode&07777 != 0700 {
		return p, "", applicationError("credential.import_root_custody")
	}
	return p, root, nil
}

func (r ApplicationRuntime) runtimeCredentialSourceUID() (uint32, error) {
	if adapter, ok := r.Credentials.(ApplicationRuntimeCredentialSourceOwner); ok {
		uid, err := adapter.ApplicationRuntimeCredentialSourceUID()
		if err == nil && uid != 0 {
			return uid, nil
		}
		return 0, applicationError("credential.import_account_unavailable")
	}
	a, err := user.Lookup("agents")
	if err != nil || a.HomeDir != "/home/agents" {
		return 0, applicationError("credential.import_account_unavailable")
	}
	uid, err := strconv.ParseUint(a.Uid, 10, 32)
	if err != nil || uid == 0 {
		return 0, applicationError("credential.import_account_unavailable")
	}
	return uint32(uid), nil
}

func applicationRuntimeCredentialOpen(path string, uid uint32, sourceRoots ...string) (*os.File, applicationRuntimeCredentialFileIdentity, error) {
	var identity applicationRuntimeCredentialFileIdentity
	var parent *os.File
	var err error
	if len(sourceRoots) == 0 {
		parent, err = applicationPrerequisiteParent(path, uid, false)
	} else {
		parent, err = applicationRuntimeCredentialSourceParent(path, uid, sourceRoots)
	}
	if err != nil {
		return nil, identity, applicationError("credential.import_source_custody")
	}
	defer parent.Close()
	fd, err := unix.Openat(int(parent.Fd()), filepath.Base(path), unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, identity, applicationError("credential.import_source_unavailable")
	}
	f := os.NewFile(uintptr(fd), path)
	var st unix.Stat_t
	if unix.Fstat(fd, &st) != nil || st.Uid != uid || st.Mode&unix.S_IFMT != unix.S_IFREG || (st.Mode&07777 != 0600 && st.Mode&07777 != 0400) || st.Nlink != 1 {
		f.Close()
		return nil, identity, applicationError("credential.import_source_custody")
	}
	if st.Size <= 0 || st.Size > applicationRuntimeCredentialLimit {
		f.Close()
		return nil, identity, applicationError("credential.import_source_size")
	}
	return f, applicationRuntimeCredentialIdentity(st), nil
}

func applicationRuntimeCredentialRead(f *os.File, identity applicationRuntimeCredentialFileIdentity) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(f, applicationRuntimeCredentialLimit+1))
	var st unix.Stat_t
	if err != nil || len(b) != int(identity.Size) || len(b) > applicationRuntimeCredentialLimit || unix.Fstat(int(f.Fd()), &st) != nil || applicationRuntimeCredentialIdentity(st) != identity {
		clear(b)
		return nil, applicationError("credential.import_source_changed")
	}
	return b, nil
}

func (r ApplicationRuntime) runtimeCredentialReadRecord(id string, out any) error {
	path := filepath.Join(r.Store.Root, "runtime-credential-"+id+".json")
	// The standard store reader is complemented with owner-only/single-link
	// custody; even the metadata and retry identities remain root-private.
	f, identity, err := applicationRuntimeCredentialOpen(path, r.Store.OwnerUID)
	if err != nil {
		if _, statErr := os.Lstat(path); errors.Is(statErr, os.ErrNotExist) {
			return os.ErrNotExist
		}
		return applicationError("credential.import_state_invalid")
	}
	defer f.Close()
	b, err := applicationRuntimeCredentialRead(f, identity)
	defer clear(b)
	if err != nil || len(b) > applicationRuntimeCredentialLimit || applicationDecodeJSON(b, out) != nil {
		return applicationError("credential.import_state_invalid")
	}
	return nil
}

func (r ApplicationRuntime) ImportCredential(ctx context.Context, peer uint32, q ApplicationCredentialImport) (ApplicationCredentialImportReceipt, error) {
	var empty ApplicationCredentialImportReceipt
	if ctx.Err() != nil {
		return empty, applicationError("credential.import_cancelled")
	}
	p, root, err := r.runtimeCredentialPolicy(q.Owner)
	if err != nil {
		return empty, err
	}
	if peer != p.InstallerUID {
		return empty, applicationError("credential.import_peer_denied")
	}
	scopes, valid := applicationRuntimeCredentialExpectations(q.ExpectedAccount, q.ExpectedScopes)
	if !valid || !applicationAbsolutePath(q.SourcePath) || len(q.SourcePath) > 4096 {
		return empty, applicationError("credential.import_request_invalid")
	}
	allowed := false
	if len(p.Provisioning.RuntimeCredentialImportRoots) > 32 {
		return empty, applicationError("credential.import_policy_invalid")
	}
	for _, sourceRoot := range p.Provisioning.RuntimeCredentialImportRoots {
		if !applicationAbsolutePath(sourceRoot) || sourceRoot == "/" || strings.HasPrefix(sourceRoot, "/nix/store/") {
			return empty, applicationError("credential.import_policy_invalid")
		}
		allowed = allowed || strings.HasPrefix(q.SourcePath, sourceRoot+"/")
	}
	if !allowed {
		return empty, applicationError("credential.import_source_denied")
	}
	uid, err := r.runtimeCredentialSourceUID()
	if err != nil {
		return empty, err
	}
	f, sourceIdentity, err := applicationRuntimeCredentialOpen(q.SourcePath, uid, p.Provisioning.RuntimeCredentialImportRoots...)
	if err != nil {
		return empty, err
	}
	defer f.Close()
	importIdentity := applicationSHA(struct {
		Owner    ApplicationOwner
		Path     string
		Identity applicationRuntimeCredentialFileIdentity
		Account  string
		Scopes   []string
	}{q.Owner, q.SourcePath, sourceIdentity, q.ExpectedAccount, scopes})
	release, err := r.Store.lock(ctx, q.Owner.Instance())
	if err != nil {
		return empty, applicationError("credential.import_lock_unavailable")
	}
	defer release()
	lookupKey := "import-" + importIdentity[7:]
	var lookup applicationRuntimeCredentialLookup
	err = r.runtimeCredentialReadRecord(lookupKey, &lookup)
	if errors.Is(err, os.ErrNotExist) {
		var random [32]byte
		if _, err = rand.Read(random[:]); err != nil {
			return empty, applicationError("credential.import_unavailable")
		}
		lookup = applicationRuntimeCredentialLookup{importIdentity, "runtime://" + hex.EncodeToString(random[:])}
		id, _ := applicationRuntimeCredentialID(lookup.Reference)
		for _, path := range []string{filepath.Join(root, id+".credential"), filepath.Join(r.Store.Root, "runtime-credential-"+id+".json")} {
			if _, e := os.Lstat(path); !errors.Is(e, os.ErrNotExist) {
				return empty, applicationError("credential.import_state_invalid")
			}
		}
		record := applicationRuntimeCredentialRecord{Owner: q.Owner, Reference: lookup.Reference, ExpectedAccount: q.ExpectedAccount, Scopes: scopes, ImportIdentity: importIdentity}
		if err = r.Store.write("runtime-credential-"+id, record); err == nil {
			err = r.Store.write("runtime-credential-"+lookupKey, lookup)
		}
		if err != nil {
			return empty, applicationError("credential.import_state_unavailable")
		}
		if r.fail("credential:runtime_import_intent") != nil {
			return empty, applicationError("credential.import_interrupted")
		}
	} else if err != nil {
		return empty, err
	}
	id, valid := applicationRuntimeCredentialID(lookup.Reference)
	if !valid || lookup.ImportIdentity != importIdentity {
		return empty, applicationError("credential.import_state_invalid")
	}
	var record applicationRuntimeCredentialRecord
	if r.runtimeCredentialReadRecord(id, &record) != nil || record.Owner != q.Owner || record.Reference != lookup.Reference || record.ImportIdentity != importIdentity || record.ExpectedAccount != q.ExpectedAccount || !slices.Equal(record.Scopes, scopes) {
		return empty, applicationError("credential.import_state_invalid")
	}
	// Reopen the source pathname rather than trusting a held but unlinked inode.
	fence := func() error {
		fresh, _, e := r.runtimeCredentialPolicy(q.Owner)
		if e != nil || applicationSHA(fresh) != applicationSHA(p) {
			return applicationError("credential.import_policy_changed")
		}
		current, identity, e := applicationRuntimeCredentialOpen(q.SourcePath, uid, p.Provisioning.RuntimeCredentialImportRoots...)
		if e != nil {
			return applicationError("credential.import_source_changed")
		}
		current.Close()
		if identity != sourceIdentity {
			return applicationError("credential.import_source_changed")
		}
		if ctx.Err() != nil {
			return applicationError("credential.import_cancelled")
		}
		return nil
	}
	if err = fence(); err != nil {
		return empty, err
	}
	if record.Ready {
		if _, err = r.ResolveRuntimeCredential(q.Owner, record.Reference); err != nil {
			return empty, err
		}
		return ApplicationCredentialImportReceipt{q.Owner, record.Reference, ApplicationProtonCredentialRevision(record.Reference)}, nil
	}
	path := filepath.Join(root, id+".credential")
	if _, err = os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		if record.FileIdentity.Inode != 0 {
			return empty, applicationError("credential.import_recovery_required")
		}
		value, e := applicationRuntimeCredentialRead(f, sourceIdentity)
		if e != nil {
			return empty, e
		}
		err = applicationPublishCredential(ctx, path, value, r.Store.OwnerUID)
		clear(value)
		if err != nil {
			return empty, applicationError("credential.import_publish_failed")
		}
		if r.fail("credential:runtime_import_snapshot") != nil {
			return empty, applicationError("credential.import_interrupted")
		}
	} else if err != nil {
		return empty, applicationError("credential.import_snapshot_custody")
	}
	protected, identity, err := applicationRuntimeCredentialOpen(path, r.Store.OwnerUID)
	if err != nil || identity.Mode&07777 != 0600 {
		if protected != nil {
			protected.Close()
		}
		return empty, applicationError("credential.import_snapshot_custody")
	}
	defer protected.Close()
	if record.FileIdentity.Inode != 0 && record.FileIdentity != identity {
		return empty, applicationError("credential.import_snapshot_changed")
	}
	record.FileIdentity = identity
	if r.Store.write("runtime-credential-"+id, record) != nil {
		return empty, applicationError("credential.import_state_unavailable")
	}
	value, err := applicationRuntimeCredentialRead(protected, identity)
	if err != nil {
		return empty, applicationError("credential.import_snapshot_changed")
	}
	defer clear(value)
	verifier, ok := r.Credentials.(ApplicationRuntimeCredentialVerifier)
	if !ok {
		verifier = ApplicationGoogleOAuthVerifier{}
	}
	if err = verifier.VerifyApplicationRuntimeCredential(ctx, value, q.ExpectedAccount, slices.Clone(scopes)); err != nil {
		return empty, applicationRuntimeCredentialVerificationError(err)
	}
	if err = fence(); err != nil {
		return empty, err
	}
	var st unix.Stat_t
	if unix.Fstat(int(protected.Fd()), &st) != nil || applicationRuntimeCredentialIdentity(st) != identity {
		return empty, applicationError("credential.import_snapshot_changed")
	}
	record.Ready = true
	if r.Store.write("runtime-credential-"+id, record) != nil {
		return empty, applicationError("credential.import_state_unavailable")
	}
	if r.fail("credential:runtime_import_verified") != nil {
		return empty, applicationError("credential.import_interrupted")
	}
	if _, err = r.ResolveRuntimeCredential(q.Owner, record.Reference); err != nil {
		return empty, err
	}
	return ApplicationCredentialImportReceipt{q.Owner, record.Reference, ApplicationProtonCredentialRevision(record.Reference)}, nil
}

// Resolve is metadata-only: no lock, mkdir, provider call or credential read.
func (r ApplicationRuntime) ResolveRuntimeCredential(owner ApplicationOwner, ref string) (ApplicationCredentialPolicy, error) {
	var empty ApplicationCredentialPolicy
	id, ok := applicationRuntimeCredentialID(ref)
	if !ok {
		return empty, applicationError("credential.runtime_reference_invalid")
	}
	_, root, err := r.runtimeCredentialPolicy(owner)
	if err != nil {
		return empty, err
	}
	var record applicationRuntimeCredentialRecord
	if err = r.runtimeCredentialReadRecord(id, &record); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return empty, applicationError("credential.runtime_missing")
		}
		return empty, err
	}
	if record.Owner != owner || record.Reference != ref {
		return empty, applicationError("credential.runtime_owner_denied")
	}
	scopes, valid := applicationRuntimeCredentialExpectations(record.ExpectedAccount, record.Scopes)
	if !valid || !slices.Equal(scopes, record.Scopes) || !applicationDigestPattern.MatchString(record.ImportIdentity) {
		return empty, applicationError("credential.import_state_invalid")
	}
	if !record.Ready {
		return empty, applicationError("credential.runtime_unverified")
	}
	path := filepath.Join(root, id+".credential")
	st, err := applicationPrerequisiteStat(path, r.Store.OwnerUID)
	if err != nil || st.Uid != r.Store.OwnerUID || st.Mode&unix.S_IFMT != unix.S_IFREG || st.Mode&07777 != 0600 || st.Nlink != 1 || st.Size <= 0 || st.Size > applicationRuntimeCredentialLimit || applicationRuntimeCredentialIdentity(st) != record.FileIdentity {
		return empty, applicationError("credential.runtime_custody")
	}
	return ApplicationCredentialPolicy{Path: path, Revision: ApplicationProtonCredentialRevision(ref)}, nil
}

func applicationRuntimeCredentialVerificationError(err error) error {
	if err != nil {
		switch err.Error() {
		case "application.credential.oauth_invalid", "application.credential.oauth_revoked",
			"application.credential.oauth_provider_unavailable", "application.credential.oauth_account_mismatch",
			"application.credential.oauth_scopes_mismatch", "application.credential.oauth_provider_invalid",
			"application.credential.import_cancelled":
			return err
		}
	}
	return applicationError("credential.oauth_verification_failed")
}

// Google is the initial provider, not a caller-controlled HTTP credential proxy.
// No consent flow or refresh-token rotation is initiated by this adapter.
type ApplicationGoogleOAuthVerifier struct{}

// TextUnmarshaler lets encoding/json decode into clearable storage rather than
// retaining immutable token strings. The helper owns both JSON input buffers.
type applicationOAuthSecret []byte

func (s *applicationOAuthSecret) UnmarshalText(value []byte) error {
	defer clear(value)
	clear(*s)
	if len(value) == 0 || len(value) > 8192 || bytes.ContainsAny(value, "\x00\r\n") {
		return applicationError("credential.oauth_invalid")
	}
	*s = append((*s)[:0], value...)
	return nil
}

func (ApplicationGoogleOAuthVerifier) VerifyApplicationRuntimeCredential(ctx context.Context, value []byte, account string, scopes []string) error {
	transport := &http.Transport{
		TLSClientConfig:     &tls.Config{MinVersion: tls.VersionTLS12},
		TLSHandshakeTimeout: 5 * time.Second, ResponseHeaderTimeout: 10 * time.Second,
		MaxResponseHeaderBytes: 8 * 1024, DisableKeepAlives: true,
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	return applicationVerifyGoogleOAuth(ctx, client, value, account, scopes)
}

func applicationGoogleOAuthRequest(ctx context.Context, client *http.Client, endpoint string, form url.Values, token string, out any) error {
	defer clear(form)
	method := http.MethodGet
	var body []byte
	if form != nil {
		method = http.MethodPost
		body = []byte(form.Encode())
		defer clear(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(body))
	if err != nil {
		return applicationError("credential.oauth_provider_invalid")
	}
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
		defer req.Header.Del("Authorization")
	}
	// Even an injected transport cannot cause redirects or change endpoints.
	bounded := *client
	bounded.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := bounded.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return applicationError("credential.import_cancelled")
		}
		return applicationError("credential.oauth_provider_unavailable")
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, applicationRuntimeCredentialLimit+1))
	defer clear(raw)
	if err != nil {
		return applicationError("credential.oauth_provider_unavailable")
	}
	if resp.StatusCode == 429 || resp.StatusCode >= 500 {
		return applicationError("credential.oauth_provider_unavailable")
	}
	if len(raw) > applicationRuntimeCredentialLimit || resp.StatusCode >= 300 && resp.StatusCode < 400 {
		return applicationError("credential.oauth_provider_invalid")
	}
	if resp.StatusCode != http.StatusOK {
		var failure struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(raw, &failure)
		if failure.Error == "invalid_grant" || resp.StatusCode == http.StatusUnauthorized {
			return applicationError("credential.oauth_revoked")
		}
		return applicationError("credential.oauth_invalid")
	}
	if json.Unmarshal(raw, out) != nil {
		return applicationError("credential.oauth_provider_invalid")
	}
	return nil
}

func applicationVerifyGoogleOAuth(ctx context.Context, client *http.Client, value []byte, account string, scopes []string) error {
	wanted, valid := applicationRuntimeCredentialExpectations(account, scopes)
	if !valid || len(value) == 0 || len(value) > applicationRuntimeCredentialLimit {
		return applicationError("credential.oauth_invalid")
	}
	var credential struct {
		Type         string                 `json:"type"`
		ClientID     string                 `json:"client_id"`
		ClientSecret applicationOAuthSecret `json:"client_secret"`
		RefreshToken applicationOAuthSecret `json:"refresh_token"`
		TokenURI     string                 `json:"token_uri"`
	}
	defer func() { clear(credential.ClientSecret); clear(credential.RefreshToken) }()
	if json.Unmarshal(value, &credential) != nil || credential.Type != "" && credential.Type != "authorized_user" || credential.TokenURI != "" && credential.TokenURI != "https://oauth2.googleapis.com/token" {
		return applicationError("credential.oauth_invalid")
	}
	if credential.ClientID == "" || len(credential.ClientID) > 4096 || strings.ContainsAny(credential.ClientID, "\x00\r\n") {
		return applicationError("credential.oauth_invalid")
	}
	for _, field := range [][]byte{credential.ClientSecret, credential.RefreshToken} {
		if len(field) == 0 || len(field) > 4096 {
			return applicationError("credential.oauth_invalid")
		}
	}
	var refreshed struct {
		AccessToken applicationOAuthSecret `json:"access_token"`
		TokenType   string                 `json:"token_type"`
	}
	defer func() { clear(refreshed.AccessToken) }()
	form := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {string(credential.RefreshToken)}, "client_id": {credential.ClientID}, "client_secret": {string(credential.ClientSecret)}}
	if err := applicationGoogleOAuthRequest(ctx, client, "https://oauth2.googleapis.com/token", form, "", &refreshed); err != nil {
		return err
	}
	if len(refreshed.AccessToken) == 0 || !strings.EqualFold(refreshed.TokenType, "Bearer") {
		return applicationError("credential.oauth_provider_invalid")
	}
	var info struct {
		Scope    string `json:"scope"`
		Audience string `json:"aud"`
		IssuedTo string `json:"azp"`
	}
	if err := applicationGoogleOAuthRequest(ctx, client, "https://oauth2.googleapis.com/tokeninfo", url.Values{"access_token": {string(refreshed.AccessToken)}}, "", &info); err != nil {
		return err
	}
	if info.Audience == "" && info.IssuedTo == "" || info.Audience != "" && info.Audience != credential.ClientID || info.IssuedTo != "" && info.IssuedTo != credential.ClientID {
		return applicationError("credential.oauth_provider_invalid")
	}
	actual := strings.Fields(info.Scope)
	slices.Sort(actual)
	if !slices.Equal(wanted, actual) {
		return applicationError("credential.oauth_scopes_mismatch")
	}
	var profile struct {
		Email string `json:"emailAddress"`
	}
	if err := applicationGoogleOAuthRequest(ctx, client, "https://gmail.googleapis.com/gmail/v1/users/me/profile", nil, string(refreshed.AccessToken), &profile); err != nil {
		return err
	}
	if !strings.EqualFold(profile.Email, account) {
		return applicationError("credential.oauth_account_mismatch")
	}
	return nil
}
