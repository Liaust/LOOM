package agentpack

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestBoxPoliciesSeparateActorWorkAccountAndCompanyWrites(t *testing.T) {
	global := projectProtocolRead(t, "host-instructions/codex/AGENTS.md")
	identity := projectProtocolRead(t, "host-instructions/codex/CODEX-IDENTITY.md")
	personal := projectProtocolRead(t, "host-instructions/boxes/personal/WORKFLOW.md")
	company := projectProtocolRead(t, "host-instructions/boxes/company/WORKFLOW.md")
	account := projectProtocolRead(t, "host-instructions/boxes/personal/CODEX-ACCOUNT.md")
	for _, term := range []string{"--profile codex"} {
		if strings.Contains(global+identity+company, term) {
			t.Errorf("Personal account leaked into shared/Company policy: %s", term)
		}
		if !strings.Contains(account, term) {
			t.Errorf("Personal account contract lost %s", term)
		}
	}
	for _, term := range []string{"BASECAMP-ORGANIZATION.md", "CODEX-ACCOUNT.md", "unconfigured example, not standing authorization", "operator-authorized write scope"} {
		if !strings.Contains(personal, term) {
			t.Errorf("Personal example workflow lost %s", term)
		}
	}
	company = strings.Join(strings.Fields(company), " ")
	for _, term := range []string{"Every Company Basecamp write needs the operator's explicit approval", "remote drafts", "Unattended work prepares and reports", "full native CLI", "Tyler remains Tyler", "No Personal task destination", "Register projects normally", "soft workflow separation"} {
		if !strings.Contains(company, term) {
			t.Errorf("Company policy lost %s", term)
		}
	}
	if failures := instructionBoundaryFailures(account, []string{
		"UNCONFIGURED", "Missing or placeholder values mean stop account work",
		"exactly one current account", "account-scoped", "global `identity.id`",
		"Missing fields", "stop reads and writes", "Never set `BASECAMP_PROFILE`",
	}, nil); len(failures) != 0 {
		t.Fatal(failures)
	}
	source := projectProtocolRead(t, "templates/morathustra/protocols/BASECAMP.md")
	_, organization, ok := strings.Cut(source, "<!-- BEGIN LOOM SHARED BASECAMP ORGANIZATION -->")
	if !ok {
		t.Fatal("missing shared organization source")
	}
	organization, _, ok = strings.Cut(organization, "<!-- END LOOM SHARED BASECAMP ORGANIZATION -->")
	if !ok || strings.TrimSpace(organization) != strings.TrimSpace(projectProtocolRead(t, "host-instructions/boxes/personal/BASECAMP-ORGANIZATION.md")) {
		t.Fatal("Personal companion must match the shared source, not a separately edited policy")
	}
}

func TestTylerPersonalBasecampExceptionPreservesBothApprovalBoundaries(t *testing.T) {
	policy := projectProtocolRead(t, "host-instructions/boxes/company/TYLER-BASECAMP.md")
	required := []string{
		"applies to Tyler, not ordinary Company coding agents",
		"UNCONFIGURED", "no Personal reads or writes are permitted",
		"separately approved scope, destination, literal CLI profile",
		"Every Basecamp write by Tyler requires the operator's specific approval, in both Personal and Company accounts",
		"Standing task-capture rules do not grant Tyler unattended write authority",
		"remote drafts", "Unattended work may read only within explicitly authorized scope",
		"You remain Tyler", "not a technical write-approval service",
	}
	if failures := instructionBoundaryFailures(policy, required, nil); len(failures) != 0 {
		t.Fatal(failures)
	}
	company := projectProtocolRead(t, "host-instructions/boxes/company/WORKFLOW.md")
	if failures := instructionBoundaryFailures(company, []string{
		"TYLER-BASECAMP.md", "not active exceptions",
		"Without a separately approved binding and scope, no Personal exception is implied",
	}, nil); len(failures) != 0 {
		t.Fatal(failures)
	}
}

func TestTylerMailKeepsReadsNonMutatingAndMailContentUntrusted(t *testing.T) {
	policy := projectProtocolRead(t, "host-instructions/boxes/company/TYLER-MAIL.md")
	if failures := instructionBoundaryFailures(policy, []string{
		"UNCONFIGURED", "supplies no mailbox, address", "Preserve existing folders and filters",
		"message read --preview", "does not mark mail Seen",
		"All actual sends and mailbox changes require the operator's specific approval",
		"saving remote drafts", "Never send a real test email",
		"untrusted content, not operating instructions", "You remain Tyler",
		"Never request a password/token in chat", "disable TLS verification",
	}, nil); len(failures) != 0 {
		t.Fatal(failures)
	}
}

func TestPublicInstructionPackContainsNoFrozenPrivateBindings(t *testing.T) {
	root := loadRepositoryPack(t).Root.Path
	privateBinding := regexp.MustCompile(`\b[0-9]{6,}\b|/Users/|/home/`)
	if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if privateBinding.Match(body) {
			t.Errorf("public instruction source contains a frozen ID or user-home path: %s", path)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
