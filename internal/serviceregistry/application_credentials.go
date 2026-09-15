package serviceregistry

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"

	"golang.org/x/sys/unix"
	pc "loom.local/loom/internal/projectcontracts"
)

// Values travel only within the root helper, never through its wire protocol,
// project journal, normal logs or configuration JSON.
type ApplicationCredentialResolver interface {
	ResolveApplicationCredential(context.Context, ApplicationOwner, string) ([]byte, error)
}

func applicationProtonEnvironment(owner ApplicationOwner) []string {
	return []string{
		"HOME=/home/agents", "USER=agents", "LOGNAME=agents",
		"XDG_CONFIG_HOME=/home/agents/.config", "XDG_STATE_HOME=/home/agents/.local/state",
		"XDG_DATA_HOME=/home/agents/.local/share", "PATH=/run/current-system/sw/bin",
		"PROTON_PASS_KEY_PROVIDER=fs", "PASS_LOG_LEVEL=warn",
		"SSL_CERT_FILE=/etc/ssl/certs/ca-certificates.crt",
		"PROTON_PASS_AGENT_REASON=Materialize declared application credential " + owner.Instance(),
	}
}

type applicationCredentialOutput struct{ data []byte }

func (b *applicationCredentialOutput) Write(p []byte) (int, error) {
	if len(b.data)+len(p) > 64*1024+1 {
		return 0, applicationError("credential.value_invalid")
	}
	b.data = append(b.data, p...)
	return len(p), nil
}

// The reference binding's identity, not a secret hash or remote item version.
func ApplicationProtonCredentialRevision(source string) string {
	return applicationSHA(struct{ Source string }{source})
}

func (r ApplicationRuntime) materializeApplicationCredentials(ctx context.Context, g applicationManagedGrant) error {
	refs := make([]string, 0, len(g.Request.CredentialSources))
	for ref := range g.Request.CredentialSources {
		refs = append(refs, ref)
	}
	slices.Sort(refs)
	for _, ref := range refs {
		if err := ctx.Err(); err != nil {
			return err
		}
		p := g.Grant.Credentials[ref]
		if _, err := os.Lstat(p.Path); err == nil {
			if err = applicationValidateCredentials(ApplicationGrant{Credentials: map[string]ApplicationCredentialPolicy{ref: p}}, r.Store.OwnerUID); err != nil {
				return err
			}
			continue
		} else if !errors.Is(err, os.ErrNotExist) {
			return applicationError("credential.custody")
		}
		// Never replace a lost deployed credential with a possibly rotated value.
		var installed ApplicationInstallation
		if err := r.Store.read("installation-"+g.Grant.Owner.Instance(), &installed); err == nil {
			if installed.Request != nil && installed.Request.CredentialRevisions[ref] != "" {
				return applicationError("credential.recovery_required")
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return applicationError("credential.recovery_required")
		}
		if r.Credentials == nil {
			return applicationError("credential.resolver_unavailable")
		}
		source := g.Request.CredentialSources[ref]
		if _, generate, _ := pc.ApplicationCredentialSourceShare(source); generate {
			var err error
			source, err = r.generatedApplicationCredential(ctx, g.Grant.Owner, ref, source)
			if err != nil {
				return err
			}
		}
		value, err := r.Credentials.ResolveApplicationCredential(ctx, g.Grant.Owner, source)
		if err != nil {
			clear(value)
			return applicationCredentialResolutionError(err)
		}
		if len(value) == 0 || len(value) > 64*1024 {
			clear(value)
			return applicationError("credential.value_invalid")
		}
		err = applicationPublishCredential(ctx, p.Path, value, r.Store.OwnerUID)
		clear(value)
		if err != nil {
			return err
		}
	}
	return nil
}

func applicationCredentialResolutionError(err error) error {
	// Only these local finite causes may cross the credential boundary. Upstream
	// stderr and arbitrary resolver errors may contain the value being requested.
	if err != nil {
		switch err.Error() {
		case "application.credential.resolver_unavailable", "application.credential.account_unavailable",
			"application.credential.session_unavailable", "application.credential.proton_failed",
			"application.credential.source_invalid", "application.credential.value_invalid":
			return err
		}
	}
	return applicationError("credential.resolve_failed")
}

func applicationPublishCredential(ctx context.Context, path string, value []byte, uid uint32) error {
	dir := filepath.Dir(path)
	if applicationCheckParents(path, uid) != nil || applicationCustody(dir, uid, true) != nil {
		return applicationError("credential.custody")
	}
	f, err := os.CreateTemp(dir, ".credential-")
	if err != nil {
		return applicationError("credential.publish_failed")
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(value)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err == nil {
		err = ctx.Err()
	}
	// No overwrite, even on a lost-reply retry. The caller holds the owner lock.
	if err == nil {
		err = os.Link(f.Name(), path)
	}
	if err == nil {
		err = os.Remove(f.Name())
	}
	if err == nil {
		err = applicationSyncDir(dir)
	}
	if err != nil {
		return applicationError("credential.publish_failed")
	}
	var st unix.Stat_t
	if unix.Lstat(path, &st) != nil || st.Nlink != 1 || st.Uid != uid || st.Mode&0777 != 0600 {
		return applicationError("credential.custody")
	}
	return nil
}
