package loomcli

import (
	"bytes"
	"strings"
	"testing"
)

func TestDeclarationMigrationCLIConflicts(t *testing.T) {
	for _, flags := range [][]string{{"--conversion-preview", "--legacy"}, {"--legacy=false", "--conversion-preview=false"}, {"--conversion-preview", "--refresh-projections"}, {"--conversion-preview=maybe"}} {
		t.Run(strings.Join(flags, "_"), func(t *testing.T) {
			cmd := NewRootCommand()
			var out bytes.Buffer
			cmd.SetOut(&out)
			cmd.SetErr(&out)
			cmd.SetArgs(append([]string{"project", "status", "registered"}, flags...))
			if e := cmd.ExecuteContext(t.Context()); e == nil {
				t.Fatal("accepted conflicting flags")
			}
			if strings.Contains(out.String(), "loom project apply") {
				t.Fatal("suggested apply")
			}
		})
	}
}
