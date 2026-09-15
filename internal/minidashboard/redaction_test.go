package minidashboard

import (
	"strings"
	"testing"
)

func TestSanitizeLabelRejectsSensitiveOrUnboundedText(t *testing.T) {
	tests := []struct {
		name  string
		value string
	}{
		{"unix path", "/var/lib/loom/private"},
		{"embedded path", "failed at /home/leonardo/file.txt"},
		{"colon path", "path:/srv/loom"},
		{"bracketed path", "[/home/user]"},
		{"parenthesized path", "(/var/lib/loom)"},
		{"uri", "sftp://user@example/private"},
		{"windows path", `C:\\Users\\the operator\\secret`},
		{"UNC path", `\\server\share`},
		{"embedded UNC path", `source:(\\server\share)`},
		{"control", "failed\nstack trace"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := SanitizeLabel(tt.value, MaxPublicLabel); err == nil {
				t.Fatalf("unsafe label accepted: %q", tt.value)
			}
		})
	}
	for _, value := range []string{"MAIN", "Backup and restore", "CPU package 0", "online/offline policy"} {
		if _, err := SanitizeLabel(value, MaxPublicLabel); err != nil {
			t.Fatalf("ordinary label rejected: %q: %v", value, err)
		}
	}

	long := strings.Repeat("a", MaxPublicLabel+20)
	clean, err := SanitizeLabel(long, MaxPublicLabel)
	if err != nil {
		t.Fatal(err)
	}
	if len([]rune(clean)) != MaxPublicLabel || !strings.HasSuffix(clean, "…") {
		t.Fatalf("long label was not bounded: %q", clean)
	}
}

func TestSafeLabelAndConditionAllowlist(t *testing.T) {
	if got := SafeLabel("/secret/path", "MAIN", MaxNodeLabelLength); got != "MAIN" {
		t.Fatalf("fallback = %q", got)
	}
	if ValidCondition(Condition{Code: "runtime.db.failed", Label: "/var/lib/postgresql failed", Severity: SeverityCritical}) {
		t.Fatal("condition with path label accepted")
	}
	if !ValidCondition(Condition{Code: "runtime.database.failed", Label: "Database FAILED", Severity: SeverityCritical}) {
		t.Fatal("safe predefined condition rejected")
	}
}
