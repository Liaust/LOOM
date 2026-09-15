package cloudstorage

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

func TestPlanSnapshotRetentionKeepsLatestAndIgnoresUnknown(t *testing.T) {
	t.Parallel()

	cfg := enabledTestCloudConfig(t, t.TempDir())
	driver := newRetentionTestDriver(cfg, "loom-main", []string{"old-a", "old-b", "new-c", "unknown-folder"})
	driver.manifests["old-a"] = retentionManifest(cfg, "loom-main", "old-a", time.Date(2026, 6, 1, 8, 0, 0, 0, time.UTC))
	driver.manifests["old-b"] = retentionManifest(cfg, "loom-main", "old-b", time.Date(2026, 6, 2, 8, 0, 0, 0, time.UTC))
	driver.manifests["new-c"] = retentionManifest(cfg, "loom-main", "new-c", time.Date(2026, 6, 3, 8, 0, 0, 0, time.UTC))

	plan, err := PlanSnapshotRetention(context.Background(), SnapshotRetentionInput{
		Config:     cfg,
		Driver:     driver,
		NodeID:     "loom-main",
		StateDir:   filepath.Join(t.TempDir(), "state"),
		KeepLatest: 2,
		Now:        fixedCloudNow,
	})
	if err != nil {
		t.Fatalf("PlanSnapshotRetention returned error: %v", err)
	}
	if plan.Status != RetentionStatusPlanned {
		t.Fatalf("status = %q", plan.Status)
	}
	if len(plan.Remove) != 1 || plan.Remove[0].Ref != "old-a" {
		t.Fatalf("remove = %#v, want old-a only", plan.Remove)
	}
	if len(plan.Kept) != 2 {
		t.Fatalf("kept = %#v, want 2", plan.Kept)
	}
	if plan.LatestKeptRef != "new-c" {
		t.Fatalf("latest = %q, want new-c", plan.LatestKeptRef)
	}
	if len(plan.Ignored) != 1 || plan.Ignored[0].Ref != "unknown-folder" {
		t.Fatalf("ignored = %#v, want unknown-folder", plan.Ignored)
	}
}

func TestApplySnapshotRetentionRequiresConfirm(t *testing.T) {
	t.Parallel()

	cfg := enabledTestCloudConfig(t, t.TempDir())
	driver := newRetentionTestDriver(cfg, "loom-main", []string{"old-a", "new-b"})
	driver.manifests["old-a"] = retentionManifest(cfg, "loom-main", "old-a", time.Date(2026, 6, 1, 8, 0, 0, 0, time.UTC))
	driver.manifests["new-b"] = retentionManifest(cfg, "loom-main", "new-b", time.Date(2026, 6, 2, 8, 0, 0, 0, time.UTC))

	result, err := ApplySnapshotRetention(context.Background(), SnapshotRetentionApplyInput{
		SnapshotRetentionInput: SnapshotRetentionInput{
			Config:     cfg,
			Driver:     driver,
			NodeID:     "loom-main",
			StateDir:   filepath.Join(t.TempDir(), "state"),
			KeepLatest: 1,
			Now:        fixedCloudNow,
		},
		Confirm: false,
	})
	if err != nil {
		t.Fatalf("ApplySnapshotRetention returned error: %v", err)
	}
	if result.Status != RetentionStatusConfirmationRequired {
		t.Fatalf("status = %q", result.Status)
	}
	if len(driver.moves) != 0 {
		t.Fatalf("unexpected moves without confirmation: %#v", driver.moves)
	}
}

func TestApplySnapshotRetentionMovesOnlyOldValidSnapshots(t *testing.T) {
	t.Parallel()

	cfg := enabledTestCloudConfig(t, t.TempDir())
	driver := newRetentionTestDriver(cfg, "loom-main", []string{"old-a", "new-b", "unknown-folder"})
	driver.manifests["old-a"] = retentionManifest(cfg, "loom-main", "old-a", time.Date(2026, 6, 1, 8, 0, 0, 0, time.UTC))
	driver.manifests["new-b"] = retentionManifest(cfg, "loom-main", "new-b", time.Date(2026, 6, 2, 8, 0, 0, 0, time.UTC))

	result, err := ApplySnapshotRetention(context.Background(), SnapshotRetentionApplyInput{
		SnapshotRetentionInput: SnapshotRetentionInput{
			Config:     cfg,
			Driver:     driver,
			NodeID:     "loom-main",
			StateDir:   filepath.Join(t.TempDir(), "state"),
			KeepLatest: 1,
			Now:        fixedCloudNow,
		},
		Confirm: true,
	})
	if err != nil {
		t.Fatalf("ApplySnapshotRetention returned error: %v", err)
	}
	if result.Status != RetentionStatusSucceeded {
		t.Fatalf("status = %q errors=%#v", result.Status, result.Errors)
	}
	if len(result.Moved) != 1 || result.Moved[0].Ref != "old-a" {
		t.Fatalf("moved = %#v, want old-a only", result.Moved)
	}
	if len(driver.moves) != 1 {
		t.Fatalf("driver moves = %#v", driver.moves)
	}
	if !strings.Contains(driver.moves[0], "main-snapshots/loom-main/old-a->_system/retention/trash/20260614T100000Z/main-snapshots/loom-main/old-a") {
		t.Fatalf("move did not use confined trash prefix: %s", driver.moves[0])
	}
}

func TestPlanGFSRetentionIsDeterministicAndProtectsExplicitClasses(t *testing.T) {
	base := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	inventory := make([]SnapshotRetentionArchive, 0, 82)
	for index := 0; index < 80; index++ {
		createdAt := base.AddDate(0, 0, -index)
		inventory = append(inventory, SnapshotRetentionArchive{
			Archive: fmt.Sprintf("__loom-direct-user-data-loom-main-%03d", index), Ref: fmt.Sprintf("ref-%03d", index),
			NodeID: "loom-main", ArchiveClass: "user_data", CreatedAt: createdAt,
			ManifestSHA256: fmt.Sprintf("%064x", index+1), Authenticated: true,
		})
	}
	inventory = append(inventory,
		SnapshotRetentionArchive{Archive: "__loom-direct-acceptance-loom-main-a", Ref: "a", NodeID: "loom-main", ArchiveClass: "acceptance", CreatedAt: base.AddDate(-2, 0, 0), ManifestSHA256: strings.Repeat("a", 64), Authenticated: true},
		SnapshotRetentionArchive{Archive: "__loom-direct-milestone-loom-main-m", Ref: "m", NodeID: "loom-main", ArchiveClass: "milestone", CreatedAt: base.AddDate(-3, 0, 0), ManifestSHA256: strings.Repeat("b", 64), Authenticated: true},
	)
	plan, err := PlanGFSRetention("loom-main", "repo", "repo-id", inventory, base)
	if err != nil {
		t.Fatal(err)
	}
	for left, right := 0, len(inventory)-1; left < right; left, right = left+1, right-1 {
		inventory[left], inventory[right] = inventory[right], inventory[left]
	}
	replay, err := PlanGFSRetention("loom-main", "repo", "repo-id", inventory, base.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if plan.PlanDigest != replay.PlanDigest || plan.InventoryDigest != replay.InventoryDigest {
		t.Fatalf("deterministic digests changed: %#v %#v", plan, replay)
	}
	if plan.Policy != (SnapshotRetentionPolicy{Daily: 14, Weekly: 8, Monthly: 6}) || len(plan.Remove) == 0 {
		t.Fatalf("GFS policy/selection = %#v remove=%d", plan.Policy, len(plan.Remove))
	}
	protected := map[string]bool{}
	for _, decision := range plan.Kept {
		protected[decision.Archive] = true
	}
	if !protected["__loom-direct-acceptance-loom-main-a"] || !protected["__loom-direct-milestone-loom-main-m"] {
		t.Fatalf("explicit protected classes were not retained: %#v", plan.Kept)
	}
	assertGFSBuckets(t, plan)
}

func TestRoutineBorgWriterCannotDeletePruneOrCompact(t *testing.T) {
	fixture := newDirectArchiveCloudFixture(t)
	calls := 0
	runner := BorgCommandRunner{Config: fixture.cfg, DisableRemoteLock: true, Exec: func(context.Context, string, BorgCommand, []string) ([]byte, error) {
		calls++
		return nil, nil
	}}
	for _, command := range []string{"delete", "prune", "compact"} {
		if _, err := runner.Run(context.Background(), BorgCommand{Args: []string{command}}); err == nil || !strings.Contains(err.Error(), "separately configured retention authority") {
			t.Fatalf("writer %s error = %v", command, err)
		}
	}
	if calls != 0 {
		t.Fatalf("denied writer mutation reached Borg %d time(s)", calls)
	}
}

func TestBorgRetentionRequiresSeparatelyConfiguredAuthority(t *testing.T) {
	fixture := newDirectArchiveCloudFixture(t)
	backend := BorgSnapshotBackend{Runner: BorgCommandRunner{Config: fixture.cfg, DisableRemoteLock: true, Exec: func(context.Context, string, BorgCommand, []string) ([]byte, error) {
		t.Fatal("Borg command ran without retention authority")
		return nil, nil
	}}}
	if _, err := backend.PlanRetention(context.Background(), SnapshotRetentionInput{Config: fixture.cfg, NodeID: "loom-main"}); err == nil || !strings.Contains(err.Error(), "retention authority is not configured") {
		t.Fatalf("missing retention authority error = %v", err)
	}
	cfg := fixture.cfg
	cfg.Snapshots.Borg.Retention = BorgRetentionConfig{
		Enabled: true, AuthorityID: "operator-retention", Repository: cfg.Snapshots.Borg.Repository,
		PassphraseFile: cfg.Snapshots.Borg.PassphraseFile,
	}
	if _, err := NormalizeConfig(cfg); err == nil || !strings.Contains(err.Error(), "must be separate") {
		t.Fatalf("shared writer/retention credential path error = %v", err)
	}
}

func TestBorgRetentionRejectsAliasedCredentialAndStateAuthority(t *testing.T) {
	base := testBorgStatusConfig(t)
	makeConfig := func(t *testing.T) Config {
		t.Helper()
		cfg := base
		root := t.TempDir()
		retentionPassphrase := filepath.Join(root, "retention.passphrase")
		if err := os.WriteFile(retentionPassphrase, []byte("retention-only\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		cfg.Snapshots.Borg.Retention = BorgRetentionConfig{
			Enabled: true, AuthorityID: "operator-retention", Repository: cfg.Snapshots.Borg.Repository,
			PassphraseFile: retentionPassphrase, CacheDir: filepath.Join(root, "cache"), SecurityDir: filepath.Join(root, "security"),
		}
		return cfg
	}

	t.Run("hard-linked passphrase", func(t *testing.T) {
		cfg := makeConfig(t)
		alias := filepath.Join(t.TempDir(), "retention-hardlink")
		if err := os.Link(cfg.Snapshots.Borg.PassphraseFile, alias); err != nil {
			t.Fatal(err)
		}
		cfg.Snapshots.Borg.Retention.PassphraseFile = alias
		if _, err := NormalizeConfig(cfg); err == nil || !strings.Contains(err.Error(), "separate underlying files") {
			t.Fatalf("hard-link alias error = %v", err)
		}
	})

	t.Run("symlinked passphrase", func(t *testing.T) {
		cfg := makeConfig(t)
		alias := filepath.Join(t.TempDir(), "retention-symlink")
		if err := os.Symlink(cfg.Snapshots.Borg.PassphraseFile, alias); err != nil {
			t.Fatal(err)
		}
		cfg.Snapshots.Borg.Retention.PassphraseFile = alias
		if _, err := NormalizeConfig(cfg); err == nil || !strings.Contains(err.Error(), "must not be a symlink") {
			t.Fatalf("symlink alias error = %v", err)
		}
	})

	for _, test := range []struct {
		name  string
		alter func(*Config)
	}{
		{"writer cache reused", func(cfg *Config) { cfg.Snapshots.Borg.Retention.CacheDir = cfg.Snapshots.Borg.CacheDir }},
		{"writer security nested", func(cfg *Config) {
			cfg.Snapshots.Borg.Retention.SecurityDir = filepath.Join(cfg.Snapshots.Borg.SecurityDir, "retention")
		}},
		{"retention roots overlap", func(cfg *Config) {
			cfg.Snapshots.Borg.Retention.SecurityDir = filepath.Join(cfg.Snapshots.Borg.Retention.CacheDir, "security")
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			cfg := makeConfig(t)
			test.alter(&cfg)
			if _, err := NormalizeConfig(cfg); err == nil || !strings.Contains(err.Error(), "non-overlapping state roots") {
				t.Fatalf("state-root alias error = %v", err)
			}
		})
	}

	t.Run("state root through symlinked parent", func(t *testing.T) {
		cfg := makeConfig(t)
		if err := os.MkdirAll(cfg.Snapshots.Borg.CacheDir, 0o700); err != nil {
			t.Fatal(err)
		}
		aliasParent := filepath.Join(t.TempDir(), "writer-state-alias")
		if err := os.Symlink(filepath.Dir(cfg.Snapshots.Borg.CacheDir), aliasParent); err != nil {
			t.Fatal(err)
		}
		cfg.Snapshots.Borg.Retention.CacheDir = filepath.Join(aliasParent, filepath.Base(cfg.Snapshots.Borg.CacheDir))
		if _, err := NormalizeConfig(cfg); err == nil || !strings.Contains(err.Error(), "non-overlapping state roots") {
			t.Fatalf("state-root parent alias error = %v", err)
		}
	})
}

func TestBorgRetentionRequiresDistinctExplicitSSHIdentity(t *testing.T) {
	cfg := testBorgStatusConfig(t)
	cfg.Snapshots.Borg.Repository = "ssh://backup@example.invalid/./loom"
	root := t.TempDir()
	writerKey := filepath.Join(root, "writer-key")
	retentionKey := filepath.Join(root, "retention-key")
	retentionPassphrase := filepath.Join(root, "retention.passphrase")
	for path, payload := range map[string]string{writerKey: "writer-key\n", retentionKey: "retention-key\n", retentionPassphrase: "retention-passphrase\n"} {
		if err := os.WriteFile(path, []byte(payload), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cfg.Snapshots.Borg.RSH = "ssh -i " + writerKey
	cfg.Snapshots.Borg.Retention = BorgRetentionConfig{
		Enabled: true, AuthorityID: "operator-retention", Repository: cfg.Snapshots.Borg.Repository,
		PassphraseFile: retentionPassphrase, RSH: "ssh -o 'IdentityFile " + retentionKey + "'",
		CacheDir: filepath.Join(root, "retention-cache"), SecurityDir: filepath.Join(root, "retention-security"),
	}
	if _, err := NormalizeConfig(cfg); err != nil {
		t.Fatalf("distinct SSH identities rejected: %v", err)
	}

	hardlink := filepath.Join(root, "writer-key-hardlink")
	if err := os.Link(writerKey, hardlink); err != nil {
		t.Fatal(err)
	}
	cfg.Snapshots.Borg.Retention.RSH = "ssh -oIdentityFile=" + hardlink
	if _, err := NormalizeConfig(cfg); err == nil || !strings.Contains(err.Error(), "separate underlying files") {
		t.Fatalf("equivalent SSH identity error = %v", err)
	}

	cfg.Snapshots.Borg.Retention.RSH = "ssh -o BatchMode=yes"
	if _, err := NormalizeConfig(cfg); err == nil || !strings.Contains(err.Error(), "exactly one SSH identity") {
		t.Fatalf("missing explicit retention identity error = %v", err)
	}

	cfg.Snapshots.Borg.Retention.RSH = "ssh -i " + retentionKey + " -o 'IdentitiesOnly no'"
	if _, err := NormalizeConfig(cfg); err == nil || !strings.Contains(err.Error(), "must not disable IdentitiesOnly") {
		t.Fatalf("non-exclusive identity error = %v", err)
	}
}

func TestBorgPruneGlobRequiresAuthenticatedExactSameNodeInventory(t *testing.T) {
	createdAt := time.Date(2026, 8, 29, 10, 0, 0, 0, time.UTC)
	unsafe := SnapshotRetentionArchive{
		Archive: "__loom-direct-user-data-loom-main-malformed", Ref: "malformed", NodeID: "loom-main",
		ArchiveClass: "unclassified", CreatedAt: createdAt, Authenticated: false,
	}
	plan, err := PlanGFSRetention("loom-main", "repo", "repo-id", []SnapshotRetentionArchive{unsafe}, createdAt)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Ignored) != 1 {
		t.Fatalf("unsafe matching archive was not classified ignored: %#v", plan)
	}
	if err := validateBorgPruneGlobInventory(plan, plan.Inventory); err == nil || !strings.Contains(err.Error(), "outside the authenticated exact same-node user-data plan") {
		t.Fatalf("unsafe prune-glob inventory error = %v", err)
	}
}

func TestBorgRetentionNeverPrunesMalformedCanonicalLookingArchive(t *testing.T) {
	cfg, backend, fake, plan := newBorgRetentionTestPlan(t, 18)
	fake.calls = nil
	malformed := "__loom-direct-user-data-loom-main-malformed"
	fake.archives[malformed] = true
	fake.timeByArchive[malformed] = time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)
	fake.pruneRemove = append(decisionArchives(plan.Remove), malformed)
	result, err := backend.ApplyRetention(context.Background(), SnapshotRetentionApplyInput{
		SnapshotRetentionInput: SnapshotRetentionInput{Config: cfg, NodeID: "loom-main", Now: fixedCloudNow},
		Plan:                   plan, Confirm: true, ConfirmDigest: plan.PlanDigest,
	})
	if err == nil || result.Status != RetentionStatusRefused || result.Prune.Status != RetentionStageSkipped || result.Compact.Status != RetentionStageSkipped {
		t.Fatalf("malformed canonical-looking archive result=%#v err=%v", result, err)
	}
	repositoryCheckRan := false
	for _, call := range fake.calls {
		if strings.HasPrefix(call, "check:") && !strings.Contains(call, "::") {
			repositoryCheckRan = true
		}
	}
	if repositoryCheckRan || fake.commandCount("prune") != 0 || !fake.archives[malformed] {
		t.Fatalf("unsafe archive crossed pre-check/prune gate: calls=%v present=%v", fake.calls, fake.archives[malformed])
	}
}

func TestBorgRetentionApplyHoldsSharedRemoteLockAcrossTransaction(t *testing.T) {
	cfg, backend, fake, plan := newBorgRetentionTestPlan(t, 18)
	fake.calls = nil
	fake.pruneRemove = decisionArchives(plan.Remove)

	checkEntered := make(chan struct{})
	allowCheck := make(chan struct{})
	pruneEntered := make(chan struct{})
	allowPrune := make(chan struct{})
	compactEntered := make(chan struct{})
	allowCompact := make(chan struct{})
	baseExec := fake.exec
	backend.Runner.DisableRemoteLock = false
	zeroWait := time.Duration(0)
	backend.Runner.RemoteLockWait = &zeroWait
	backend.Runner.Exec = func(ctx context.Context, binary string, command BorgCommand, env []string) ([]byte, error) {
		switch command.Name() {
		case "check":
			if borgArchiveArg(command.Args) != "" {
				break
			}
			close(checkEntered)
			select {
			case <-allowCheck:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		case "prune":
			close(pruneEntered)
			select {
			case <-allowPrune:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		case "compact":
			close(compactEntered)
			select {
			case <-allowCompact:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		return baseExec(ctx, binary, command, env)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	applyDone := make(chan struct {
		result SnapshotRetentionApplyResult
		err    error
	}, 1)
	go func() {
		result, err := backend.ApplyRetention(ctx, SnapshotRetentionApplyInput{
			SnapshotRetentionInput: SnapshotRetentionInput{Config: cfg, NodeID: "loom-main", Now: fixedCloudNow},
			Plan:                   plan, Confirm: true, ConfirmDigest: plan.PlanDigest, Compact: true,
		})
		applyDone <- struct {
			result SnapshotRetentionApplyResult
			err    error
		}{result, err}
	}()

	select {
	case <-checkEntered:
	case <-ctx.Done():
		t.Fatal("retention did not reach its repository check")
	}
	writerExec := make(chan struct{}, 1)
	writerDir := t.TempDir()
	writer := BorgCommandRunner{
		Config: cfg, RemoteLockWait: &zeroWait,
		Exec: func(context.Context, string, BorgCommand, []string) ([]byte, error) {
			writerExec <- struct{}{}
			return []byte(`{}`), nil
		},
	}
	writerCommand := BorgCommand{Args: []string{"create", "::concurrent-writer", "."}, Dir: writerDir}
	if _, err := writer.Run(ctx, writerCommand); !errors.Is(err, ErrRemoteLockBusy) {
		t.Fatalf("concurrent writer error during retention check = %v, want ErrRemoteLockBusy", err)
	}
	select {
	case <-writerExec:
		t.Fatal("writer executed while retention repository check was held")
	default:
	}
	close(allowCheck)

	select {
	case <-pruneEntered:
	case <-ctx.Done():
		t.Fatal("retention did not reach prune")
	}
	select {
	case <-writerExec:
		t.Fatal("writer executed between retention check and prune")
	default:
	}
	close(allowPrune)

	select {
	case <-compactEntered:
	case <-ctx.Done():
		t.Fatal("retention did not reach compact after post-prune verification")
	}
	select {
	case <-writerExec:
		t.Fatal("writer executed before retention post-prune verification and compact")
	default:
	}
	if _, err := writer.Run(ctx, writerCommand); !errors.Is(err, ErrRemoteLockBusy) {
		t.Fatalf("concurrent writer error after retention post-prune verification = %v, want ErrRemoteLockBusy", err)
	}
	select {
	case <-writerExec:
		t.Fatal("writer executed while retention compact was held")
	default:
	}
	close(allowCompact)

	applied := <-applyDone
	if applied.err != nil || applied.result.Status != RetentionStatusSucceeded || applied.result.Prune.Verification != RetentionStageSucceeded || applied.result.Compact.Status != RetentionStageSucceeded {
		t.Fatalf("retention apply result=%#v err=%v", applied.result, applied.err)
	}
	if _, err := writer.Run(ctx, writerCommand); err != nil {
		t.Fatalf("writer did not execute after retention released the lock: %v", err)
	}
	select {
	case <-writerExec:
	default:
		t.Fatal("writer command never reached execution after retention completed")
	}
}

func TestBorgRetentionApplyLockRefusalRunsNoBorgCommand(t *testing.T) {
	cfg, backend, fake, plan := newBorgRetentionTestPlan(t, 18)
	fake.calls = nil
	backend.Runner.DisableRemoteLock = false
	zeroWait := time.Duration(0)
	backend.Runner.RemoteLockWait = &zeroWait
	release, waitHeld := holdRemoteLockForTest(t, cfg)
	defer release()
	waitHeld()

	result, err := backend.ApplyRetention(context.Background(), SnapshotRetentionApplyInput{
		SnapshotRetentionInput: SnapshotRetentionInput{Config: cfg, NodeID: "loom-main", Now: fixedCloudNow},
		Plan:                   plan, Confirm: true, ConfirmDigest: plan.PlanDigest, Compact: true,
	})
	if !errors.Is(err, ErrRemoteLockBusy) {
		t.Fatalf("lock refusal error = %v, want ErrRemoteLockBusy", err)
	}
	if result.Status != RetentionStatusRefused || result.Prune.Status != RetentionStageSkipped || result.Prune.Error != "shared remote lock unavailable" || result.Compact.Status != RetentionStageSkipped || len(result.Moved) != 0 {
		t.Fatalf("lock refusal result = %#v", result)
	}
	if len(fake.calls) != 0 {
		t.Fatalf("Borg commands ran despite retention lock refusal: %v", fake.calls)
	}
}

func TestBorgRetentionApplyCancellationReleasesSharedRemoteLock(t *testing.T) {
	cfg, backend, fake, plan := newBorgRetentionTestPlan(t, 18)
	fake.calls = nil
	backend.Runner.DisableRemoteLock = false
	zeroWait := time.Duration(0)
	backend.Runner.RemoteLockWait = &zeroWait
	baseExec := fake.exec
	ctx, cancel := context.WithCancel(context.Background())
	backend.Runner.Exec = func(ctx context.Context, binary string, command BorgCommand, env []string) ([]byte, error) {
		if command.Name() == "check" && borgArchiveArg(command.Args) == "" {
			cancel()
			return nil, ctx.Err()
		}
		return baseExec(ctx, binary, command, env)
	}

	result, err := backend.ApplyRetention(ctx, SnapshotRetentionApplyInput{
		SnapshotRetentionInput: SnapshotRetentionInput{Config: cfg, NodeID: "loom-main", Now: fixedCloudNow},
		Plan:                   plan, Confirm: true, ConfirmDigest: plan.PlanDigest, Compact: true,
	})
	if !errors.Is(err, context.Canceled) || result.Status != RetentionStatusRefused || result.Prune.Status != RetentionStageSkipped || fake.commandCount("prune") != 0 || fake.commandCount("compact") != 0 {
		t.Fatalf("cancelled retention result=%#v err=%v calls=%v", result, err, fake.calls)
	}

	executed := false
	writer := BorgCommandRunner{
		Config: cfg, RemoteLockWait: &zeroWait,
		Exec: func(context.Context, string, BorgCommand, []string) ([]byte, error) {
			executed = true
			return []byte(`{}`), nil
		},
	}
	if _, err := writer.Run(context.Background(), BorgCommand{Args: []string{"info", "--json"}}); err != nil || !executed {
		t.Fatalf("writer could not acquire released retention lock: executed=%v err=%v", executed, err)
	}
}

func TestBorgRetentionApplyPreservesNoCandidateTruth(t *testing.T) {
	cfg, backend, fake, plan := newBorgRetentionTestPlan(t, 1)
	if len(plan.Remove) != 0 {
		t.Fatalf("fixture unexpectedly has prune candidates: %#v", plan.Remove)
	}
	fake.calls = nil
	backend.Runner.DisableRemoteLock = false
	zeroWait := time.Duration(0)
	backend.Runner.RemoteLockWait = &zeroWait

	result, err := backend.ApplyRetention(context.Background(), SnapshotRetentionApplyInput{
		SnapshotRetentionInput: SnapshotRetentionInput{Config: cfg, NodeID: "loom-main", Now: fixedCloudNow},
		Plan:                   plan, Confirm: true, ConfirmDigest: plan.PlanDigest, Compact: true,
	})
	if err != nil || result.Status != RetentionStatusSucceeded || result.Prune.Status != RetentionStageSkipped || result.Prune.Verification != "no_prune_candidates" || result.Compact.Status != RetentionStageSkipped || result.Compact.Verification != "no_prune_candidates" || fake.commandCount("prune") != 0 || fake.commandCount("compact") != 0 {
		t.Fatalf("no-candidate result=%#v err=%v calls=%v", result, err, fake.calls)
	}
}

func TestBorgRetentionBindsPlanInventoryChecksConfirmationAndStageTruth(t *testing.T) {
	cfg, backend, fake, plan := newBorgRetentionTestPlan(t, 18)
	if len(plan.Remove) == 0 || plan.RepositoryCheck != SnapshotStatusSucceeded {
		t.Fatalf("retention plan = %#v", plan)
	}
	fake.calls = nil

	unconfirmed, err := backend.ApplyRetention(context.Background(), SnapshotRetentionApplyInput{
		SnapshotRetentionInput: SnapshotRetentionInput{Config: cfg, NodeID: "loom-main", Now: fixedCloudNow},
		Plan:                   plan, Confirm: true, ConfirmDigest: "wrong",
	})
	if err != nil || unconfirmed.Status != RetentionStatusConfirmationRequired || fake.commandCount("prune") != 0 {
		t.Fatalf("confirmation gate result=%#v err=%v calls=%v", unconfirmed, err, fake.calls)
	}

	tampered := plan
	tampered.Remove = append([]SnapshotRetentionDecision(nil), plan.Remove...)
	tampered.Remove[0].Archive = "__loom-direct-user-data-loom-main-tampered"
	if _, err := backend.ApplyRetention(context.Background(), SnapshotRetentionApplyInput{
		SnapshotRetentionInput: SnapshotRetentionInput{Config: cfg, NodeID: "loom-main"}, Plan: tampered, Confirm: true, ConfirmDigest: tampered.PlanDigest,
	}); err == nil || fake.commandCount("prune") != 0 {
		t.Fatalf("tampered plan was not refused before mutation: err=%v calls=%v", err, fake.calls)
	}

	forged := plan
	forged.Kept = append([]SnapshotRetentionDecision(nil), plan.Kept...)
	for _, decision := range plan.Remove {
		decision.Action = RetentionActionKeep
		decision.Reason = "caller-reclassified"
		forged.Kept = append(forged.Kept, decision)
	}
	forged.Remove = []SnapshotRetentionDecision{}
	sortRetentionDecisions(forged.Kept)
	forged.PlanDigest, err = snapshotRetentionPlanDigest(forged)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := backend.ApplyRetention(context.Background(), SnapshotRetentionApplyInput{
		SnapshotRetentionInput: SnapshotRetentionInput{Config: cfg, NodeID: "loom-main"}, Plan: forged, Confirm: true, ConfirmDigest: forged.PlanDigest,
	}); err == nil || fake.commandCount("prune") != 0 {
		t.Fatalf("re-digested noncanonical plan was not refused before mutation: err=%v calls=%v", err, fake.calls)
	}

	wrongNode := plan
	wrongNode.NodeID = "other-node"
	wrongNode.ArchiveGlob = directArchiveRetentionGlob(wrongNode.NodeID)
	wrongNode.PlanDigest, err = snapshotRetentionPlanDigest(wrongNode)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := backend.ApplyRetention(context.Background(), SnapshotRetentionApplyInput{
		SnapshotRetentionInput: SnapshotRetentionInput{Config: cfg, NodeID: "loom-main"}, Plan: wrongNode, Confirm: true, ConfirmDigest: wrongNode.PlanDigest,
	}); err == nil || fake.commandCount("prune") != 0 {
		t.Fatalf("wrong-node plan was not refused before mutation: err=%v calls=%v", err, fake.calls)
	}

	fake.repositoryID = "different-repository-id"
	if _, err := backend.ApplyRetention(context.Background(), SnapshotRetentionApplyInput{
		SnapshotRetentionInput: SnapshotRetentionInput{Config: cfg, NodeID: "loom-main"}, Plan: plan, Confirm: true, ConfirmDigest: plan.PlanDigest,
	}); err == nil || fake.commandCount("prune") != 0 {
		t.Fatalf("repository identity change was not refused: err=%v calls=%v", err, fake.calls)
	}
	fake.repositoryID = "disposable-repository-id"
	fake.archives["foreign-archive"] = true
	fake.timeByArchive["foreign-archive"] = time.Date(2026, 8, 30, 0, 0, 0, 0, time.UTC)
	if _, err := backend.ApplyRetention(context.Background(), SnapshotRetentionApplyInput{
		SnapshotRetentionInput: SnapshotRetentionInput{Config: cfg, NodeID: "loom-main"}, Plan: plan, Confirm: true, ConfirmDigest: plan.PlanDigest,
	}); err == nil || fake.commandCount("prune") != 0 {
		t.Fatalf("inventory change was not refused: err=%v calls=%v", err, fake.calls)
	}
	fake.archives["foreign-archive"] = false
	fake.failCheck = fmt.Errorf("check failed")
	if _, err := backend.ApplyRetention(context.Background(), SnapshotRetentionApplyInput{
		SnapshotRetentionInput: SnapshotRetentionInput{Config: cfg, NodeID: "loom-main"}, Plan: plan, Confirm: true, ConfirmDigest: plan.PlanDigest,
	}); err == nil || fake.commandCount("prune") != 0 {
		t.Fatalf("repository check failure was not refused: err=%v calls=%v", err, fake.calls)
	}
	fake.failCheck = nil
	fake.pruneRemove = decisionArchives(plan.Remove)
	result, err := backend.ApplyRetention(context.Background(), SnapshotRetentionApplyInput{
		SnapshotRetentionInput: SnapshotRetentionInput{Config: cfg, NodeID: "loom-main", Now: fixedCloudNow},
		Plan:                   plan, Confirm: true, ConfirmDigest: plan.PlanDigest, Compact: false,
	})
	if err != nil || result.Status != RetentionStatusPartial || result.Prune.Status != RetentionStageSucceeded || result.Prune.Verification != RetentionStageSucceeded || result.Compact.Status != RetentionStageSkipped || result.ReclaimedBytes != nil {
		t.Fatalf("prune/compact stage truth = %#v err=%v", result, err)
	}
}

func TestBorgRetentionReportsCompactFailureAndSuccessSeparately(t *testing.T) {
	for _, test := range []struct {
		name       string
		compactErr error
		wantStatus string
		wantStage  string
	}{{"failure", fmt.Errorf("compact failed"), RetentionStatusPartial, RetentionStageFailed}, {"success", nil, RetentionStatusSucceeded, RetentionStageSucceeded}} {
		t.Run(test.name, func(t *testing.T) {
			cfg, backend, fake, plan := newBorgRetentionTestPlan(t, 18)
			fake.calls = nil
			fake.pruneRemove = decisionArchives(plan.Remove)
			fake.failCompact = test.compactErr
			result, err := backend.ApplyRetention(context.Background(), SnapshotRetentionApplyInput{
				SnapshotRetentionInput: SnapshotRetentionInput{Config: cfg, NodeID: "loom-main", Now: fixedCloudNow},
				Plan:                   plan, Confirm: true, ConfirmDigest: plan.PlanDigest, Compact: true,
			})
			if err != nil || result.Status != test.wantStatus || result.Prune.Status != RetentionStageSucceeded || result.Compact.Status != test.wantStage || result.ReclaimedBytes != nil || fake.commandCount("prune") != 1 || fake.commandCount("compact") != 1 {
				t.Fatalf("compact stage result=%#v err=%v calls=%v", result, err, fake.calls)
			}
		})
	}
}

func newBorgRetentionTestPlan(t *testing.T, count int) (Config, BorgSnapshotBackend, *fakeDirectBorg, SnapshotRetentionPlan) {
	t.Helper()
	fixture := newDirectArchiveCloudFixture(t)
	fake := newFakeDirectBorg(t)
	backend := BorgSnapshotBackend{Runner: BorgCommandRunner{Config: fixture.cfg, Exec: fake.exec, StreamExec: fake.streamExec, DisableRemoteLock: true}}
	base := time.Date(2026, 8, 29, 10, 0, 0, 0, time.UTC)
	for index := count - 1; index >= 0; index-- {
		request := fixture.request
		request.ArchiveRef = fmt.Sprintf("history-%03d", index)
		request.CreatedAt = base.AddDate(0, 0, -index)
		if _, err := backend.ArchiveCanonicalRoots(context.Background(), DirectArchiveInput{Config: fixture.cfg, Request: request}); err != nil {
			t.Fatalf("create retention fixture archive %d: %v", index, err)
		}
	}
	cfg := fixture.cfg
	retentionPassphrase := filepath.Join(t.TempDir(), "retention.passphrase")
	if err := os.WriteFile(retentionPassphrase, []byte("disposable-passphrase\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg.Snapshots.Borg.Retention = BorgRetentionConfig{
		Enabled: true, AuthorityID: "operator-retention", Binary: "borg",
		Repository: cfg.Snapshots.Borg.Repository, PassphraseFile: retentionPassphrase,
		CheckMode: "repository", CacheDir: filepath.Join(t.TempDir(), "retention-cache"), SecurityDir: filepath.Join(t.TempDir(), "retention-security"),
	}
	var err error
	cfg, err = NormalizeConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	backend.Runner.Config = cfg
	plan, err := backend.PlanRetention(context.Background(), SnapshotRetentionInput{Config: cfg, NodeID: "loom-main", Now: fixedCloudNow})
	if err != nil {
		t.Fatalf("plan Borg retention fixture: %v", err)
	}
	return cfg, backend, fake, plan
}

func decisionArchives(decisions []SnapshotRetentionDecision) []string {
	result := make([]string, 0, len(decisions))
	for _, decision := range decisions {
		result = append(result, decision.Archive)
	}
	return result
}

func assertGFSBuckets(t *testing.T, plan SnapshotRetentionPlan) {
	t.Helper()
	kept := make(map[string]bool, len(plan.Kept))
	for _, decision := range plan.Kept {
		kept[decision.Archive] = true
	}
	eligible := make([]SnapshotRetentionArchive, 0, len(plan.Inventory))
	for _, item := range plan.Inventory {
		if item.Authenticated && item.ArchiveClass == "user_data" && item.NodeID == plan.NodeID {
			eligible = append(eligible, item)
		}
	}
	sort.Slice(eligible, func(i, j int) bool { return eligible[i].CreatedAt.After(eligible[j].CreatedAt) })
	for _, check := range []struct {
		limit  int
		bucket func(time.Time) string
	}{{14, func(value time.Time) string { return value.Format("2006-01-02") }}, {8, isoWeekBucket}, {6, func(value time.Time) string { return value.Format("2006-01") }}} {
		seen := map[string]bool{}
		selected := 0
		for _, item := range eligible {
			key := check.bucket(item.CreatedAt)
			if seen[key] {
				continue
			}
			seen[key] = true
			if selected < check.limit {
				if !kept[item.Archive] {
					t.Fatalf("GFS bucket %s did not retain %s", key, item.Archive)
				}
				selected++
			}
		}
	}
}

type retentionTestDriver struct {
	cfg       Config
	nodeID    string
	entries   []RemoteEntry
	manifests map[string]SnapshotUploadManifest
	moves     []string
}

func newRetentionTestDriver(cfg Config, nodeID string, refs []string) *retentionTestDriver {
	entries := make([]RemoteEntry, 0, len(refs))
	for _, ref := range refs {
		entries = append(entries, RemoteEntry{Path: ref, IsDir: true})
	}
	return &retentionTestDriver{cfg: cfg, nodeID: nodeID, entries: entries, manifests: map[string]SnapshotUploadManifest{}}
}

func (d *retentionTestDriver) Status(context.Context) (RemoteStatus, error) {
	return RemoteStatus{Reachable: true, RemoteURI: d.cfg.RemoteURI(""), CheckedAt: fixedCloudNow(), Entries: len(d.entries)}, nil
}

func (d *retentionTestDriver) List(_ context.Context, prefix string) ([]RemoteEntry, error) {
	if prefix == path.Join(d.cfg.Roots.MainSnapshots, d.nodeID) {
		return d.entries, nil
	}
	return []RemoteEntry{{Path: "cloud-upload.json", IsDir: false}}, nil
}

func (d *retentionTestDriver) CopyToRemote(context.Context, string, string, CopyOptions) (CopyResult, error) {
	return CopyResult{}, nil
}

func (d *retentionTestDriver) CopyFromRemote(_ context.Context, remotePath, localPath string, _ CopyOptions) (CopyResult, error) {
	ref := path.Base(path.Dir(remotePath))
	manifest, ok := d.manifests[ref]
	if !ok {
		return CopyResult{}, fmt.Errorf("remote manifest missing for %s", ref)
	}
	switch path.Base(remotePath) {
	case "cloud-upload.json":
		if err := WriteSnapshotUploadManifest(localPath, manifest); err != nil {
			return CopyResult{}, err
		}
	case "manifest.json":
		if err := os.MkdirAll(filepath.Dir(localPath), 0o750); err != nil {
			return CopyResult{}, err
		}
		if err := os.WriteFile(localPath, retentionBackupManifestPayload, 0o640); err != nil {
			return CopyResult{}, err
		}
	default:
		return CopyResult{}, fmt.Errorf("unexpected remote snapshot member %s", remotePath)
	}
	return CopyResult{Command: "copyto", Source: d.cfg.RemoteURI(remotePath), Dest: localPath, FinishedAt: fixedCloudNow()}, nil
}

func (d *retentionTestDriver) Check(context.Context, string, string) (CheckResult, error) {
	return CheckResult{Matched: true, CheckedAt: fixedCloudNow()}, nil
}

func (d *retentionTestDriver) MoveRemote(_ context.Context, fromRemotePath, toRemotePath string, _ CopyOptions) (CopyResult, error) {
	d.moves = append(d.moves, fromRemotePath+"->"+toRemotePath)
	return CopyResult{Command: "moveto", Source: d.cfg.RemoteURI(fromRemotePath), Dest: d.cfg.RemoteURI(toRemotePath), FinishedAt: fixedCloudNow()}, nil
}

func retentionManifest(cfg Config, nodeID, ref string, completedAt time.Time) SnapshotUploadManifest {
	remotePrefix := path.Join(cfg.Roots.MainSnapshots, nodeID, ref)
	manifestDigest := sha256.Sum256(retentionBackupManifestPayload)
	return SnapshotUploadManifest{
		SchemaVersion:              SnapshotUploadManifestSchema,
		UploadID:                   "upload_" + ref,
		Backend:                    SnapshotBackendLegacyTree,
		BackupOperationID:          "maintenance_" + ref,
		SourceBackupDir:            "/var/lib/loom/backups/main/" + ref,
		SourceBackupManifestSHA256: fmt.Sprintf("%x", manifestDigest),
		RemoteURI:                  cfg.RemoteURI(remotePrefix),
		RemotePrefix:               remotePrefix,
		SnapshotRef:                ref,
		CreatedAt:                  completedAt,
		CompletedAt:                completedAt,
		Driver:                     "rclone",
		VerifyBeforeUpload:         SnapshotStatusSucceeded,
		VerifyAfterUpload:          SnapshotStatusSucceeded,
		FileCount:                  1,
		TotalBytes:                 10,
		Checks:                     map[string]string{"cloud_upload_manifest": SnapshotStatusSucceeded},
	}
}

var retentionBackupManifestPayload = []byte("{\"schema_version\":\"loom.main_backup.v0.9\"}\n")
