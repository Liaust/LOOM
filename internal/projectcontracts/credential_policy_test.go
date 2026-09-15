package projectcontracts

import "testing"

func TestParseCredentialPolicy(t *testing.T) {
	policy, err := ParseCredentialPolicy([]byte(`kind: loom.credentials_policy
schema_version: credentials.policy.v0.3
credentials:
  inline_secrets_allowed: false
  references:
    - ref: telegram.bot_token
      source:
        kind: env
        env: TELEGRAM_BOT_TOKEN
      status: required
`))
	if err != nil {
		t.Fatalf("ParseCredentialPolicy returned error: %v", err)
	}
	diagnostics := ValidateCredentialPolicy(policy, "policies/credentials.yaml")
	if len(diagnostics) != 0 {
		t.Fatalf("expected valid credential policy, got diagnostics: %#v", diagnostics)
	}
	refs := policy.ReferenceMap()
	if refs["telegram.bot_token"].Source.Env != "TELEGRAM_BOT_TOKEN" {
		t.Fatalf("credential ref map = %#v", refs)
	}
}

func TestValidateCredentialPolicyRejectsInlineSecretsAndRelativeFiles(t *testing.T) {
	policy, err := ParseCredentialPolicy([]byte(`kind: loom.credentials_policy
schema_version: credentials.policy.v0.3
credentials:
  inline_secrets_allowed: true
  references:
    - ref: telegram.chat_id
      source:
        kind: file
        path: secrets/chat-id.txt
`))
	if err != nil {
		t.Fatalf("ParseCredentialPolicy returned error: %v", err)
	}
	diagnostics := ValidateCredentialPolicy(policy, "policies/credentials.yaml")
	assertDiagnostic(t, diagnostics, "credentials_policy.inline_secrets_forbidden")
	assertDiagnostic(t, diagnostics, "credentials_policy.source_path_relative")
}
