package backupstrategy

import (
	"context"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"loom.local/loom/internal/hermesprofile"
)

func TestHermesArchiveBoundaryExcludesMutableAndUnauthenticatedTrees(t *testing.T) {
	w := hermesprofile.WorkspaceRoot
	for _, root := range []string{w, filepath.Dir(w)} {
		rel, _ := filepath.Rel(root, w)
		got, evidence, err := HermesArchiveBoundary(context.Background(), []DirectArchiveRoot{{Name: "agents", Path: root}}, nil, hermesprofile.Policy{}, time.Now().UTC())
		want := []DirectArchiveExclusion{{"agents", filepath.Join(rel, ".hermes")}, {"agents", filepath.Join(rel, "recovery")}}
		if err != nil || !reflect.DeepEqual(got, want) || len(evidence) != 0 {
			t.Fatalf("boundary: %v %v", got, err)
		}
		// Exact boundary insertion is idempotent.
		again, _, err := HermesArchiveBoundary(context.Background(), []DirectArchiveRoot{{Name: "agents", Path: root}}, got, hermesprofile.Policy{}, time.Now().UTC())
		if err != nil || !reflect.DeepEqual(again, want) {
			t.Fatalf("replay: %v %v", again, err)
		}
	}
	for _, root := range []string{w + "/.hermes", w + "/.hermes/sessions", w + "/recovery", w + "/recovery/.staging", w + "/recovery/valid-package"} {
		if _, _, err := HermesArchiveBoundary(context.Background(), []DirectArchiveRoot{{Name: "alias", Path: root}}, nil, hermesprofile.Policy{}, time.Now().UTC()); err == nil {
			t.Fatalf("alias accepted: %s", root)
		}
	}
	for _, exclusion := range []string{".hermes/state.db", ".hermes/logs"} {
		if _, _, err := HermesArchiveBoundary(context.Background(), []DirectArchiveRoot{{Name: "agents", Path: w}}, []DirectArchiveExclusion{{"agents", exclusion}}, hermesprofile.Policy{}, time.Now().UTC()); err == nil {
			t.Fatal("overlapping policy accepted")
		}
	}
	if _, _, err := HermesArchiveBoundary(context.Background(), []DirectArchiveRoot{{Name: "elsewhere", Path: "/unrelated"}}, nil, hermesprofile.Policy{Enabled: true}, time.Now().UTC()); err == nil {
		t.Fatal("enabled missing workspace accepted")
	}
}

func TestHermesArchiveEvidenceIdentityIsBounded(t *testing.T) {
	w := hermesprofile.WorkspaceRoot
	hash := strings.Repeat("a", 64)
	base := hermesprofile.Evidence{ID: "fixture", Path: w + "/recovery/fixture", ManifestSHA256: hash, CreatedAt: time.Now().UTC(), Files: []hermesprofile.File{{Path: "manifest.json", Size: 1, Mode: 0440, SHA256: hash}, {Path: "profile.zip", Size: 1, Mode: 0440, SHA256: hash}}}
	roots := []DirectArchiveRoot{{Name: "agents", Path: w}}
	if err := validateHermesEvidence([]hermesprofile.Evidence{base}, roots, nil); err != nil {
		t.Fatal(err)
	}
	for _, scenario := range []string{"outside", "duplicate", "mode", "hash", "excluded", "identity"} {
		t.Run(scenario, func(t *testing.T) {
			e := base
			e.Files = append([]hermesprofile.File(nil), base.Files...)
			var ex []DirectArchiveExclusion
			switch scenario {
			case "outside":
				e.Path = "/outside/recovery/fixture"
			case "mode":
				e.Files[1].Mode = 0640
			case "hash":
				e.Files[1].SHA256 = "invalid"
			case "identity":
				e.ManifestSHA256 = strings.Repeat("b", 64)
			case "excluded":
				ex = []DirectArchiveExclusion{{"agents", "recovery"}}
			}
			set := []hermesprofile.Evidence{e}
			if scenario == "duplicate" {
				set = append(set, e)
			}
			if err := validateHermesEvidence(set, roots, ex); err == nil {
				t.Fatal("invalid recovery evidence accepted")
			}
		})
	}
}

func TestMinaArchiveIdentityAndRetiredPrivateExclusions(t *testing.T) {
	for _, transition := range []bool{false, true} {
		p := hermesprofile.Policy{Identity: hermesprofile.MinaIdentity}
		if transition {
			p.RetainedMorathustra = []string{"retained"}
		}
		roots := []DirectArchiveRoot{{Name: "agents", Path: "/srv/loom/agents"}}
		want := []DirectArchiveExclusion{{"agents", "mina/.hermes"}, {"agents", "mina/recovery"}}
		// Selecting MINA cannot expose the retired private tree, even when no
		// retained packages are declared in the current recovery directory.
		want = append(want, DirectArchiveExclusion{"agents", "morathustra/.hermes"}, DirectArchiveExclusion{"agents", "morathustra/recovery"})
		got, evidence, err := HermesArchiveBoundary(context.Background(), roots, nil, p, time.Now().UTC())
		if err != nil || len(evidence) != 0 || !reflect.DeepEqual(got, want) {
			t.Fatalf("transition %v: %v %v", transition, got, err)
		}
		replay, _, err := HermesArchiveBoundary(context.Background(), roots, got, p, time.Now().UTC())
		if err != nil || !reflect.DeepEqual(got, replay) {
			t.Fatal("boundary replay")
		}
		protected := []string{hermesprofile.MinaWorkspaceRoot}
		protected = append(protected, hermesprofile.WorkspaceRoot)
		for _, workspace := range protected {
			for _, suffix := range []string{"/.hermes", "/.hermes/sessions", "/recovery", "/recovery/.staging", "/recovery/package"} {
				if _, _, err := HermesArchiveBoundary(context.Background(), []DirectArchiveRoot{{Name: "nested", Path: workspace + suffix}}, nil, p, time.Now().UTC()); err == nil {
					t.Fatalf("protected nested root accepted: %s", workspace+suffix)
				}
			}
			rel := filepath.Base(workspace)
			for _, ex := range []string{rel, rel + "/.hermes/state.db", rel + "/recovery/.staging"} {
				if _, _, err := HermesArchiveBoundary(context.Background(), roots, []DirectArchiveExclusion{{"agents", ex}}, p, time.Now().UTC()); err == nil {
					t.Fatalf("overlap accepted: %s", ex)
				}
			}
		}
	}
	for _, p := range []hermesprofile.Policy{{Identity: "MINA"}, {Identity: hermesprofile.MinaIdentity, Workspace: hermesprofile.WorkspaceRoot}, {RetainedMorathustra: []string{"old"}}} {
		if _, _, err := HermesArchiveBoundary(context.Background(), nil, nil, p, time.Now().UTC()); err == nil {
			t.Fatal("invalid disabled policy accepted")
		}
	}
}
