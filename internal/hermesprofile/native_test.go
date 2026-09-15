package hermesprofile

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestMinaNativeProductionAndFixtureBoundary(t *testing.T) {
	for _, root := range []string{WorkspaceRoot, MinaWorkspaceRoot, WorkspaceRoot + "/.loom-acceptance/x", MinaWorkspaceRoot + "/.loom-acceptance/x", "/tmp/arbitrary"} {
		profile := root + "/recovery/.staging/attempt/.capture"
		if _, err := RunFixtureBackup(context.Background(), "/nonexistent", profile, filepath.Dir(profile)+"/profile.zip"); err == nil {
			t.Fatal("nonfixture admitted")
		}
	}
	for _, tc := range []struct {
		id   RecoveryIdentity
		root string
	}{{"", MinaWorkspaceRoot}, {MinaIdentity, WorkspaceRoot}, {"unknown", MinaWorkspaceRoot}} {
		profile := tc.root + "/recovery/.staging/attempt/.capture"
		if _, err := RunNativeBackupForIdentity(context.Background(), tc.id, "/nonexistent", profile, filepath.Dir(profile)+"/profile.zip"); err == nil {
			t.Fatal("native boundary admitted")
		}
	}
	in, _ := fixture(t)
	profile := filepath.Join(in.Workspace, "recovery/.staging/attempt/.capture")
	if err := os.MkdirAll(profile, 0700); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(filepath.Dir(profile), PayloadFile)
	if _, err := RunFixtureBackup(context.Background(), "/bin/true", profile, output); err == nil {
		t.Fatal("unpinned fixture executable accepted")
	}
	saved := profile + "-saved"
	if err := os.Rename(profile, saved); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(saved, profile); err != nil {
		t.Fatal(err)
	}
	if _, err := RunFixtureBackup(context.Background(), "/nonexistent", profile, output); err == nil {
		t.Fatal("substituted capture accepted")
	}
}
