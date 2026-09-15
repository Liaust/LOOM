package estatemigration

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"golang.org/x/sys/unix"

	"loom.local/loom/internal/filepolicy"
	"loom.local/loom/internal/filesystemmeta"
)

func TestInventoryDisposableEstateIsDeterministicRedactedAndReadOnly(t *testing.T) {
	t.Parallel()
	outer := t.TempDir()
	estate := filepath.Join(outer, "estate")
	projectsRoot := filepath.Join(estate, "Projects")
	documentsRoot := filepath.Join(estate, "Documents")
	repositoryRoot := filepath.Join(projectsRoot, "sample")
	linkedRoot := filepath.Join(outer, "linked-sensitive-name")
	submoduleSource := filepath.Join(outer, "submodule-sensitive-name")
	mustMkdirAll(t, projectsRoot)
	mustMkdirAll(t, documentsRoot)
	if err := os.CopyFS(projectsRoot, os.DirFS("testdata/estate-policy")); err != nil {
		t.Fatalf("copy policy fixture: %v", err)
	}
	mustMkdirAll(t, filepath.Join(projectsRoot, "user-excluded", "nested"))
	mustWriteFile(t, filepath.Join(projectsRoot, "user-excluded", "nested", "payload.txt"), "ignored payload")
	if err := os.Link(filepath.Join(projectsRoot, "included.txt"), filepath.Join(projectsRoot, "user-excluded", "nested", "included-alias.txt")); err != nil {
		t.Fatalf("create ignored hard link: %v", err)
	}
	mustWriteFile(t, filepath.Join(projectsRoot, "contract.tmp"), "contract ignored")

	mustWriteFile(t, filepath.Join(documentsRoot, "original.txt"), "hard-linked content")
	if err := os.Link(filepath.Join(documentsRoot, "original.txt"), filepath.Join(documentsRoot, "alias.txt")); err != nil {
		t.Fatalf("create hard link: %v", err)
	}
	if err := os.Symlink("original.txt", filepath.Join(documentsRoot, "internal-link")); err != nil {
		t.Fatalf("create internal symlink: %v", err)
	}
	outsideTarget := filepath.Join(outer, "outside-sensitive-name")
	mustWriteFile(t, outsideTarget, "must not be scanned")
	if err := os.Symlink(outsideTarget, filepath.Join(documentsRoot, "external-link")); err != nil {
		t.Fatalf("create external symlink: %v", err)
	}
	if err := unix.Mkfifo(filepath.Join(documentsRoot, "named-pipe"), 0o600); err != nil {
		t.Fatalf("create FIFO: %v", err)
	}

	initGitRepository(t, submoduleSource)
	mustWriteFile(t, filepath.Join(submoduleSource, "module.txt"), "module")
	gitFixture(t, submoduleSource, "add", "module.txt")
	gitFixture(t, submoduleSource, "commit", "-m", "module")

	initGitRepository(t, repositoryRoot)
	mustWriteFile(t, filepath.Join(repositoryRoot, "tracked.txt"), "tracked")
	gitFixture(t, repositoryRoot, "add", "tracked.txt")
	gitFixture(t, repositoryRoot, "commit", "-m", "initial")
	gitFixture(t, repositoryRoot, "remote", "add", "origin", "https://secret-user:secret-pass@example.invalid/org/repo.git?token=hidden#fragment")
	gitFixture(t, repositoryRoot, "-c", "protocol.file.allow=always", "submodule", "add", submoduleSource, "vendor/sub")
	gitFixture(t, repositoryRoot, "commit", "-am", "add submodule")
	gitFixture(t, repositoryRoot, "tag", "v1")
	gitFixture(t, repositoryRoot, "branch", "topic")
	gitFixture(t, repositoryRoot, "worktree", "add", "-b", "linked-branch", linkedRoot)
	mustWriteFile(t, filepath.Join(repositoryRoot, "tracked.txt"), "dirty")
	mustWriteFile(t, filepath.Join(repositoryRoot, "untracked.txt"), "untracked")

	beforeGit := gitStateSnapshot(t, repositoryRoot)
	beforeFiles := filesystemSnapshot(t, estate)
	config := Config{Roots: []RootSpec{
		{ID: "sample_repo", Path: repositoryRoot, Domain: DomainRepositories, Ignore: IgnoreConfig{DiscoverUserRules: true}},
		{ID: "documents", Path: documentsRoot, Domain: DomainDocuments},
		{ID: "projects", Path: projectsRoot, Domain: DomainProjects, Ignore: IgnoreConfig{DiscoverUserRules: true, ContractExcludes: []string{"contract.tmp"}}},
		{ID: "linked_repo", Path: linkedRoot, Domain: DomainRepositories},
	}}
	report, err := (Service{}).Inventory(context.Background(), config)
	if err != nil {
		t.Fatalf("inventory disposable estate: %v: %v", err, errors.Unwrap(err))
	}
	reversed := config
	reversed.Roots = append([]RootSpec{}, config.Roots...)
	for left, right := 0, len(reversed.Roots)-1; left < right; left, right = left+1, right-1 {
		reversed.Roots[left], reversed.Roots[right] = reversed.Roots[right], reversed.Roots[left]
	}
	again, err := (Service{}).Inventory(context.Background(), reversed)
	if err != nil {
		t.Fatalf("repeat inventory: %v", err)
	}
	if !reflect.DeepEqual(report, again) {
		t.Fatalf("inventory changed with input order\nfirst=%#v\nsecond=%#v", report, again)
	}
	if report.Digest == "" || !validDigest(report.Digest) {
		t.Fatalf("invalid digest %q", report.Digest)
	}

	payload, err := json.Marshal(report)
	if err != nil {
		t.Fatalf("marshal inventory: %v", err)
	}
	for _, secret := range []string{outer, "secret-user", "secret-pass", "token=hidden", "outside-sensitive-name", "submodule-sensitive-name"} {
		if bytes.Contains(payload, []byte(secret)) {
			t.Fatalf("redacted inventory leaked %q: %s", secret, payload)
		}
	}
	for _, forbiddenAction := range []string{`"destination"`, `"delete"`, `"registration"`, `"publication"`} {
		if bytes.Contains(payload, []byte(forbiddenAction)) {
			t.Fatalf("read-only inventory emitted action field %s", forbiddenAction)
		}
	}

	if got := report.Overlaps; len(got) != 1 || got[0] != (RootOverlap{AncestorRootID: "projects", DescendantRootID: "sample_repo"}) {
		t.Fatalf("overlaps=%#v", got)
	}
	documents := findRoot(t, report, "documents")
	original := findEntry(t, documents, "root://documents/original.txt")
	alias := findEntry(t, documents, "root://documents/alias.txt")
	if !original.HardLink || !alias.HardLink || original.ObjectIdentity == "" || original.ObjectIdentity != alias.ObjectIdentity {
		t.Fatalf("hard-link identity missing: original=%#v alias=%#v", original, alias)
	}
	if !duplicateContains(report.DuplicateObjects, original.ObjectIdentity, original.Locator, alias.Locator) {
		t.Fatalf("hard-link duplicate group missing: %#v", report.DuplicateObjects)
	}
	if target := findEntry(t, documents, "root://documents/internal-link").SymlinkTarget; target != "root://documents/original.txt" {
		t.Fatalf("internal symlink target=%q", target)
	}
	if target := findEntry(t, documents, "root://documents/external-link").SymlinkTarget; !strings.HasPrefix(target, "external://sha256:") {
		t.Fatalf("external symlink target was not opaque: %q", target)
	}
	if kind := findEntry(t, documents, "root://documents/named-pipe").Kind; kind != filesystemmeta.ObjectKindSpecial {
		t.Fatalf("FIFO kind=%q", kind)
	}

	projects := findRoot(t, report, "projects")
	ignored := findEntry(t, projects, "root://projects/ignored.txt")
	if ignored.Ignore.Included || ignored.Ignore.RuleCategory != filepolicy.RuleCategoryUser || ignored.Ignore.Source != "root://projects/.loomignore" {
		t.Fatalf("per-root ignore decision=%#v", ignored.Ignore)
	}
	reconstructible := findEntry(t, projects, "root://projects/node_modules")
	if reconstructible.Ignore.Included || reconstructible.Ignore.RuleCategory != filepolicy.RuleCategoryReconstructible {
		t.Fatalf("generalized ignore decision=%#v", reconstructible.Ignore)
	}
	reconstructibleChild := findEntry(t, projects, "root://projects/node_modules/cache.txt")
	if reconstructibleChild.Ignore.Included || reconstructibleChild.Ignore.RuleCategory != filepolicy.RuleCategoryReconstructible {
		t.Fatalf("generalized ignored descendant=%#v", reconstructibleChild.Ignore)
	}
	userExcludedChild := findEntry(t, projects, "root://projects/user-excluded/nested/payload.txt")
	if userExcludedChild.Ignore.Included || userExcludedChild.Ignore.RuleCategory != filepolicy.RuleCategoryUser {
		t.Fatalf("user ignored descendant=%#v", userExcludedChild.Ignore)
	}
	includedSource := findEntry(t, projects, "root://projects/included.txt")
	excludedAlias := findEntry(t, projects, "root://projects/user-excluded/nested/included-alias.txt")
	if includedSource.ObjectIdentity == "" || includedSource.ObjectIdentity != excludedAlias.ObjectIdentity || excludedAlias.Ignore.Included {
		t.Fatalf("ignored duplicate identity missing: source=%#v alias=%#v", includedSource, excludedAlias)
	}
	if !duplicateContains(report.DuplicateObjects, includedSource.ObjectIdentity, includedSource.Locator, excludedAlias.Locator) {
		t.Fatalf("ignored duplicate group missing: %#v", report.DuplicateObjects)
	}
	if projects.Summary.Accounting.LogicalBytes <= projects.Summary.Accounting.IncludedLogicalBytes || projects.Summary.Accounting.LogicalBytes-projects.Summary.Accounting.IncludedLogicalBytes < reconstructibleChild.LogicalBytes+userExcludedChild.LogicalBytes+excludedAlias.LogicalBytes {
		t.Fatalf("ignored bytes missing from total accounting: %#v", projects.Summary.Accounting)
	}
	contractIgnored := findEntry(t, projects, "root://projects/contract.tmp")
	if contractIgnored.Ignore.Included || contractIgnored.Ignore.RuleCategory != filepolicy.RuleCategoryContract {
		t.Fatalf("contract ignore decision=%#v", contractIgnored.Ignore)
	}
	gitEntry := findEntry(t, findRoot(t, report, "sample_repo"), "root://sample_repo/.git")
	if !gitEntry.Ignore.Included || gitEntry.Ignore.RuleCategory != filepolicy.RuleCategoryContract {
		t.Fatalf(".git was excluded by user policy: %#v", gitEntry.Ignore)
	}

	mainRepo := findRepository(t, report, "root://sample_repo")
	if !mainRepo.Dirty || !contains(mainRepo.Untracked, "root://sample_repo/untracked.txt") {
		t.Fatalf("working state=%#v", mainRepo)
	}
	for _, ref := range []string{"refs/heads/main", "refs/heads/topic", "refs/heads/linked-branch", "refs/tags/v1"} {
		if !hasRef(mainRepo.Refs, ref) {
			t.Fatalf("missing ref %q in %#v", ref, mainRepo.Refs)
		}
	}
	if len(mainRepo.Submodules) != 1 || mainRepo.Submodules[0].Path != "root://sample_repo/vendor/sub" || mainRepo.Submodules[0].State != "clean" {
		t.Fatalf("submodules=%#v", mainRepo.Submodules)
	}
	if !hasWorktree(mainRepo.Worktrees, "root://sample_repo") || !hasWorktree(mainRepo.Worktrees, "root://linked_repo") {
		t.Fatalf("worktrees=%#v", mainRepo.Worktrees)
	}
	linkedRepo := findRepository(t, report, "root://linked_repo")
	if !linkedRepo.LinkedWorktree || linkedRepo.IdentityDigest != mainRepo.IdentityDigest {
		t.Fatalf("linked worktree posture mismatch: main=%#v linked=%#v", mainRepo, linkedRepo)
	}
	if report.Totals.LogicalBytes == 0 || report.Totals.AllocatedBytes == 0 || report.Totals.DuplicateAllocatedBytes == 0 {
		t.Fatalf("byte accounting=%#v", report.Totals)
	}

	afterFiles := filesystemSnapshot(t, estate)
	if afterGit := gitStateSnapshot(t, repositoryRoot); !bytes.Equal(beforeGit, afterGit) {
		t.Fatalf("Git state changed during inventory\nbefore=%q\nafter=%q", beforeGit, afterGit)
	}
	if !reflect.DeepEqual(beforeFiles, afterFiles) {
		t.Fatalf("filesystem state changed during inventory\nbefore=%#v\nafter=%#v", beforeFiles, afterFiles)
	}
}

func TestInventoryUsesInjectedAdaptersAndRedactsTheirPaths(t *testing.T) {
	t.Parallel()
	rootPath := t.TempDir()
	mustMkdirAll(t, filepath.Join(rootPath, ".git"))
	mustWriteFile(t, filepath.Join(rootPath, "meta.txt"), "metadata")
	filesystem := &recordingFilesystem{delegate: OSFilesystem{}}
	digest := sha256.Sum256([]byte("remote"))
	git := &staticGitAdapter{observation: RawGitObservation{
		CommonDirectory: "/private/sensitive/common.git",
		RemoteDigests:   []string{"sha256:" + hex.EncodeToString(digest[:])},
		Head:            GitHead{Commit: strings.Repeat("a", 40), Branch: "main"},
		Refs:            []GitRef{{Name: "refs/heads/main", Object: strings.Repeat("a", 40)}},
		Dirty:           true,
		UntrackedPaths:  []string{"private-name.txt"},
		Submodules:      []RawGitSubmodule{},
		Worktrees:       []RawGitWorktree{{Path: "/private/sensitive/worktree", Head: strings.Repeat("a", 40), Branch: "refs/heads/main"}},
	}}
	report, err := (Service{
		Filesystem: filesystem,
		Git:        git,
		Policies:   staticPolicyFactory{policy: staticPolicy{fingerprint: "/private/sensitive/policy"}},
	}).Inventory(context.Background(), Config{Roots: []RootSpec{{ID: "fixture", Path: rootPath, Domain: DomainDocuments}}})
	if err != nil {
		t.Fatalf("inventory with injected adapters: %v", err)
	}
	if !reflect.DeepEqual(filesystem.opened, []string{rootPath}) {
		t.Fatalf("filesystem opened non-configured roots: %#v", filesystem.opened)
	}
	canonicalRoot, canonicalErr := filepath.EvalSymlinks(rootPath)
	if canonicalErr != nil {
		t.Fatalf("canonicalize fixture root: %v", canonicalErr)
	}
	if !reflect.DeepEqual(git.roots, []string{canonicalRoot}) {
		t.Fatalf("Git adapter observed non-configured roots: %#v", git.roots)
	}
	metadata := findEntry(t, findRoot(t, report, "fixture"), "root://fixture/meta.txt").Metadata
	if !metadata.HasXattrs || !metadata.HasACL || !metadata.HasResourceFork || !metadata.HasFinderTags || !metadata.HasQuarantine || !metadata.Sparse {
		t.Fatalf("metadata posture=%#v", metadata)
	}
	payload, _ := json.Marshal(report)
	if bytes.Contains(payload, []byte("/private/sensitive")) || bytes.Contains(payload, []byte(rootPath)) || !bytes.Contains(payload, []byte("external://sha256:")) {
		t.Fatalf("injected adapter paths were not redacted: %s", payload)
	}
}

func TestInventoryObservesDisposableBareRepository(t *testing.T) {
	t.Parallel()
	parent := t.TempDir()
	bareRoot := filepath.Join(parent, "archive.git")
	gitFixture(t, parent, "init", "--bare", "-b", "main", bareRoot)
	report, err := (Service{}).Inventory(context.Background(), Config{Roots: []RootSpec{{ID: "repos", Path: parent, Domain: DomainRepositories}}})
	if err != nil {
		t.Fatalf("inventory bare repository: %v: %v", err, errors.Unwrap(err))
	}
	repository := findRepository(t, report, "root://repos/archive.git")
	if !repository.Bare || !repository.Head.Unborn || repository.Head.Branch != "main" || repository.Dirty || len(repository.Untracked) != 0 || len(repository.Submodules) != 0 {
		t.Fatalf("bare repository posture=%#v", repository)
	}
	if !hasWorktree(repository.Worktrees, "root://repos/archive.git") || !repository.Worktrees[0].Bare {
		t.Fatalf("bare worktree posture=%#v", repository.Worktrees)
	}
}

func TestInventoryRejectsUnboundedInvalidAndChangingInputs(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	mustWriteFile(t, filepath.Join(root, "one"), "1")
	mustMkdirAll(t, filepath.Join(root, "nested", "too-deep"))

	tests := []struct {
		name   string
		config Config
		want   error
	}{
		{name: "entry bound", config: Config{Roots: []RootSpec{{ID: "root", Path: root, Domain: DomainDocuments}}, Bounds: Bounds{MaxEntries: 1}}, want: ErrBudgetExhausted},
		{name: "depth bound", config: Config{Roots: []RootSpec{{ID: "root", Path: root, Domain: DomainDocuments}}, Bounds: Bounds{MaxDepth: 1}}, want: ErrBudgetExhausted},
		{name: "relative root", config: Config{Roots: []RootSpec{{ID: "root", Path: "relative", Domain: DomainDocuments}}}, want: ErrInvalidConfig},
		{
			name: "unsafe pattern",
			config: Config{Roots: []RootSpec{{
				ID: "root", Path: root, Domain: DomainDocuments,
				Ignore: IgnoreConfig{ContractExcludes: []string{"../escape"}},
			}}},
			want: ErrInvalidConfig,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := (Service{}).Inventory(context.Background(), test.config)
			if !errors.Is(err, test.want) {
				t.Fatalf("error=%v want %v", err, test.want)
			}
		})
	}

	symlink := filepath.Join(filepath.Dir(root), "root-link")
	if err := os.Symlink(root, symlink); err != nil {
		t.Fatalf("create root symlink: %v", err)
	}
	_, err := (Service{}).Inventory(context.Background(), Config{Roots: []RootSpec{{ID: "root", Path: symlink, Domain: DomainDocuments}}})
	if !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("symlink root error=%v", err)
	}

	gitRoot := t.TempDir()
	mustMkdirAll(t, filepath.Join(gitRoot, ".git"))
	tooManyRefs := []GitRef{{Name: "refs/heads/a", Object: strings.Repeat("a", 40)}, {Name: "refs/heads/b", Object: strings.Repeat("b", 40)}}
	_, err = (Service{Git: &staticGitAdapter{observation: RawGitObservation{CommonDirectory: "/tmp/common", Refs: tooManyRefs}}}).Inventory(
		context.Background(),
		Config{Roots: []RootSpec{{ID: "root", Path: gitRoot, Domain: DomainRepositories}}, Bounds: Bounds{MaxGitRefs: 1}},
	)
	if !errors.Is(err, ErrBudgetExhausted) {
		t.Fatalf("Git ref bound error=%v", err)
	}

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = (Service{}).Inventory(cancelled, Config{Roots: []RootSpec{{ID: "root", Path: root, Domain: DomainDocuments}}})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled error=%v", err)
	}

	_, err = (Service{Filesystem: changingFilesystem{delegate: OSFilesystem{}}}).Inventory(
		context.Background(),
		Config{Roots: []RootSpec{{ID: "root", Path: root, Domain: DomainDocuments}}},
	)
	if !errors.Is(err, ErrSourceChanged) {
		t.Fatalf("changed-source error=%v", err)
	}

	_, err = calculateAccounting([]Entry{
		{LogicalBytes: ^uint64(0), Ignore: IgnoreDecision{Included: true}},
		{LogicalBytes: 1, Ignore: IgnoreDecision{Included: true}},
	})
	if !errors.Is(err, ErrMetadataOverflow) {
		t.Fatalf("overflow error=%v", err)
	}

	unsafeGitRoot := t.TempDir()
	unsafeGitTarget := t.TempDir()
	if err := os.Symlink(unsafeGitTarget, filepath.Join(unsafeGitRoot, ".git")); err != nil {
		t.Fatalf("create unsafe .git marker: %v", err)
	}
	unsafeGit := &staticGitAdapter{}
	_, err = (Service{Git: unsafeGit}).Inventory(context.Background(), Config{Roots: []RootSpec{{ID: "root", Path: unsafeGitRoot, Domain: DomainRepositories}}})
	if !errors.Is(err, ErrGitUnavailable) || len(unsafeGit.roots) != 0 {
		t.Fatalf("unsafe .git marker error=%v calls=%#v", err, unsafeGit.roots)
	}

	submoduleSource := t.TempDir()
	initGitRepository(t, submoduleSource)
	mustWriteFile(t, filepath.Join(submoduleSource, "source.txt"), "source")
	gitFixture(t, submoduleSource, "add", "source.txt")
	gitFixture(t, submoduleSource, "commit", "-m", "source")
	commit := strings.TrimSpace(string(gitFixture(t, submoduleSource, "rev-parse", "HEAD")))
	unsafeSubmoduleRoot := t.TempDir()
	initGitRepository(t, unsafeSubmoduleRoot)
	mustWriteFile(t, filepath.Join(unsafeSubmoduleRoot, "tracked.txt"), "tracked")
	gitFixture(t, unsafeSubmoduleRoot, "add", "tracked.txt")
	gitFixture(t, unsafeSubmoduleRoot, "commit", "-m", "tracked")
	gitFixture(t, unsafeSubmoduleRoot, "update-index", "--add", "--cacheinfo", "160000", commit, "vendor/sub")
	mustMkdirAll(t, filepath.Join(unsafeSubmoduleRoot, "vendor"))
	if err := os.Symlink(submoduleSource, filepath.Join(unsafeSubmoduleRoot, "vendor", "sub")); err != nil {
		t.Fatalf("create unsafe submodule symlink: %v", err)
	}
	_, err = (Service{}).Inventory(context.Background(), Config{Roots: []RootSpec{{ID: "root", Path: unsafeSubmoduleRoot, Domain: DomainRepositories}}})
	if !errors.Is(err, ErrGitUnavailable) {
		t.Fatalf("unsafe submodule error=%v", err)
	}

	policyRoot := t.TempDir()
	mustWriteFile(t, filepath.Join(policyRoot, ".loomignore"), "too-large\n")
	_, err = (Service{}).Inventory(context.Background(), Config{
		Roots:  []RootSpec{{ID: "root", Path: policyRoot, Domain: DomainDocuments, Ignore: IgnoreConfig{DiscoverUserRules: true}}},
		Bounds: Bounds{MaxPolicyBytes: 1},
	})
	if !errors.Is(err, ErrBudgetExhausted) {
		t.Fatalf("policy byte bound error=%v", err)
	}
}

func TestReadOnlyGitEnvironmentDropsAmbientOverrides(t *testing.T) {
	t.Parallel()
	environment := readOnlyGitEnvironment([]string{
		"PATH=/bin",
		"GIT_DIR=/private/repo",
		"GIT_INDEX_FILE=/private/index",
		"GIT_OPTIONAL_LOCKS=1",
		"GIT_TERMINAL_PROMPT=1",
		"GIT_EXTERNAL_DIFF=/private/executable",
		"GIT_CONFIG_COUNT=1",
	})
	joined := strings.Join(environment, "\n")
	for _, forbidden := range []string{"GIT_DIR=/private", "GIT_INDEX_FILE=/private", "GIT_OPTIONAL_LOCKS=1", "GIT_TERMINAL_PROMPT=1", "GIT_EXTERNAL_DIFF=", "GIT_CONFIG_COUNT="} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("ambient override survived: %s", joined)
		}
	}
	for _, required := range []string{"GIT_OPTIONAL_LOCKS=0", "GIT_TERMINAL_PROMPT=0", "GCM_INTERACTIVE=never", "LC_ALL=C"} {
		if !strings.Contains(joined, required) {
			t.Fatalf("required read-only environment missing %q: %s", required, joined)
		}
	}
}

func TestRootLocatorEscapesAmbiguousAndInvalidPathBytes(t *testing.T) {
	t.Parallel()
	locator := rootLocator("fixture", "folder/name #"+string([]byte{0xff})+".txt")
	if locator != "root://fixture/folder/name%20%23%FF.txt" || strings.ContainsRune(locator, '\ufffd') {
		t.Fatalf("locator=%q", locator)
	}
	payload, err := json.Marshal(Entry{Locator: locator})
	if err != nil || bytes.Contains(payload, []byte("\\ufffd")) {
		t.Fatalf("locator JSON=%q error=%v", payload, err)
	}
}

func TestInventoryEnumeratesIgnoredDescendantsWithinBounds(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	mustWriteFile(t, filepath.Join(root, ".loomignore"), "excluded/\n")
	mustWriteFile(t, filepath.Join(root, "included.txt"), "included")
	mustWriteFile(t, filepath.Join(root, "excluded", "nested", "payload.txt"), "ignored")
	if err := os.Link(filepath.Join(root, "included.txt"), filepath.Join(root, "excluded", "nested", "alias.txt")); err != nil {
		t.Fatalf("create ignored alias: %v", err)
	}
	config := Config{Roots: []RootSpec{{
		ID: "fixture", Path: root, Domain: DomainDocuments,
		Ignore: IgnoreConfig{DiscoverUserRules: true},
	}}}
	report, err := (Service{}).Inventory(context.Background(), config)
	if err != nil {
		t.Fatalf("inventory ignored descendants: %v", err)
	}
	rootReport := findRoot(t, report, "fixture")
	payload := findEntry(t, rootReport, "root://fixture/excluded/nested/payload.txt")
	alias := findEntry(t, rootReport, "root://fixture/excluded/nested/alias.txt")
	included := findEntry(t, rootReport, "root://fixture/included.txt")
	for _, entry := range []Entry{payload, alias} {
		if entry.Ignore.Included || entry.Ignore.RuleCategory != filepolicy.RuleCategoryUser {
			t.Fatalf("ignored descendant decision=%#v", entry.Ignore)
		}
	}
	if rootReport.Summary.Accounting.LogicalBytes <= rootReport.Summary.Accounting.IncludedLogicalBytes || rootReport.Summary.Accounting.LogicalBytes-rootReport.Summary.Accounting.IncludedLogicalBytes < payload.LogicalBytes+alias.LogicalBytes {
		t.Fatalf("total/included accounting=%#v", rootReport.Summary.Accounting)
	}
	if rootReport.Summary.EntryCount != 7 || rootReport.Summary.IgnoredCount != 4 || rootReport.Summary.IncludedCount != 3 {
		t.Fatalf("root summary omitted ignored descendants: %#v", rootReport.Summary)
	}
	if !reflect.DeepEqual(report.Totals, rootReport.Summary.Accounting) {
		t.Fatalf("inventory totals do not include the complete root: totals=%#v root=%#v", report.Totals, rootReport.Summary.Accounting)
	}
	if !duplicateContains(report.DuplicateObjects, included.ObjectIdentity, included.Locator, alias.Locator) {
		t.Fatalf("duplicate below ignored directory absent: %#v", report.DuplicateObjects)
	}

	config.Bounds.MaxEntries = rootReport.Summary.EntryCount - 1
	if _, err := (Service{}).Inventory(context.Background(), config); !errors.Is(err, ErrBudgetExhausted) {
		t.Fatalf("ignored descendant bound error=%v", err)
	}
}

func TestInventoryRejectsInvalidUTF8SerializedNames(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	mustWriteFile(t, filepath.Join(root, "meta.txt"), "metadata")
	gitRoot := t.TempDir()
	mustMkdirAll(t, filepath.Join(gitRoot, ".git"))
	objectID := strings.Repeat("a", 40)
	baseGit := RawGitObservation{
		CommonDirectory: filepath.Join(gitRoot, ".git"),
		Head:            GitHead{Commit: objectID, Branch: "main"},
		Refs:            []GitRef{{Name: "refs/heads/main", Object: objectID}},
		Worktrees:       []RawGitWorktree{{Path: gitRoot, Head: objectID, Branch: "refs/heads/main"}},
	}
	if report, err := (Service{Filesystem: invalidXattrFilesystem{delegate: OSFilesystem{}, name: "user.\ufffd"}}).Inventory(
		context.Background(),
		Config{Roots: []RootSpec{{ID: "fixture", Path: root, Domain: DomainDocuments}}},
	); err != nil || report.Digest == "" {
		t.Fatalf("valid replacement-rune xattr must remain distinguishable: digest=%q error=%v", report.Digest, err)
	}
	validReplacementGit := baseGit
	validReplacementGit.Refs = []GitRef{{Name: "refs/heads/bad\ufffd", Object: objectID}}
	validReplacementGit.Head.Branch = "bad\ufffd"
	validReplacementGit.Worktrees = []RawGitWorktree{{Path: gitRoot, Head: objectID, Branch: "refs/heads/bad\ufffd"}}
	if report, err := (Service{Git: &staticGitAdapter{observation: validReplacementGit}}).Inventory(
		context.Background(),
		Config{Roots: []RootSpec{{ID: "fixture", Path: gitRoot, Domain: DomainRepositories}}},
	); err != nil || report.Digest == "" {
		t.Fatalf("valid replacement-rune Git names must remain distinguishable: digest=%q error=%v", report.Digest, err)
	}

	for _, invalid := range []string{string([]byte{0xff}), string([]byte{0xfe})} {
		_, err := (Service{Filesystem: invalidXattrFilesystem{delegate: OSFilesystem{}, name: "user." + invalid}}).Inventory(
			context.Background(),
			Config{Roots: []RootSpec{{ID: "fixture", Path: root, Domain: DomainDocuments}}},
		)
		if !errors.Is(err, ErrSourceChanged) {
			t.Fatalf("invalid xattr name %x error=%v", []byte(invalid), err)
		}

		mutations := []struct {
			name   string
			mutate func(*RawGitObservation)
		}{
			{name: "ref", mutate: func(raw *RawGitObservation) { raw.Refs[0].Name = "refs/heads/bad" + invalid }},
			{name: "head branch", mutate: func(raw *RawGitObservation) { raw.Head.Branch = "bad" + invalid }},
			{name: "worktree branch", mutate: func(raw *RawGitObservation) { raw.Worktrees[0].Branch = "refs/heads/bad" + invalid }},
		}
		for _, mutation := range mutations {
			raw := baseGit
			raw.Refs = append([]GitRef{}, baseGit.Refs...)
			raw.Worktrees = append([]RawGitWorktree{}, baseGit.Worktrees...)
			mutation.mutate(&raw)
			_, err := (Service{Git: &staticGitAdapter{observation: raw}}).Inventory(
				context.Background(),
				Config{Roots: []RootSpec{{ID: "fixture", Path: gitRoot, Domain: DomainRepositories}}},
			)
			if !errors.Is(err, ErrGitUnavailable) {
				t.Fatalf("invalid %s name %x error=%v", mutation.name, []byte(invalid), err)
			}
		}
	}
}

type recordingFilesystem struct {
	delegate OSFilesystem
	opened   []string
}

type changingFilesystem struct {
	delegate OSFilesystem
}

func (filesystem changingFilesystem) OpenRoot(value string) (FilesystemRoot, error) {
	root, err := filesystem.delegate.OpenRoot(value)
	if err != nil {
		return nil, err
	}
	return &changingFilesystemRoot{FilesystemRoot: root, calls: map[string]int{}}, nil
}

type changingFilesystemRoot struct {
	FilesystemRoot
	calls map[string]int
}

func (root *changingFilesystemRoot) Lstat(relative string) (fs.FileInfo, error) {
	root.calls[relative]++
	if relative == "one" && root.calls[relative] == 2 {
		return root.FilesystemRoot.Lstat(".")
	}
	return root.FilesystemRoot.Lstat(relative)
}

func (filesystem *recordingFilesystem) OpenRoot(value string) (FilesystemRoot, error) {
	filesystem.opened = append(filesystem.opened, value)
	root, err := filesystem.delegate.OpenRoot(value)
	if err != nil {
		return nil, err
	}
	return &metadataFilesystemRoot{FilesystemRoot: root}, nil
}

type metadataFilesystemRoot struct {
	FilesystemRoot
}

func (root *metadataFilesystemRoot) Metadata(relative string) (filesystemmeta.Observation, error) {
	observation, err := root.FilesystemRoot.Metadata(relative)
	if err != nil || relative != "meta.txt" {
		return observation, err
	}
	observation.HasXattrs = true
	observation.XattrNames = []string{"user.example"}
	observation.HasACL = true
	observation.HasResourceFork = true
	observation.HasFinderTags = true
	observation.HasQuarantine = true
	observation.IsSparse = true
	observation.Risks = []string{filesystemmeta.FidelityRiskMetadataOnly}
	return observation, nil
}

type invalidXattrFilesystem struct {
	delegate OSFilesystem
	name     string
}

func (filesystem invalidXattrFilesystem) OpenRoot(value string) (FilesystemRoot, error) {
	root, err := filesystem.delegate.OpenRoot(value)
	if err != nil {
		return nil, err
	}
	return invalidXattrFilesystemRoot{FilesystemRoot: root, name: filesystem.name}, nil
}

type invalidXattrFilesystemRoot struct {
	FilesystemRoot
	name string
}

func (root invalidXattrFilesystemRoot) Metadata(relative string) (filesystemmeta.Observation, error) {
	observation, err := root.FilesystemRoot.Metadata(relative)
	if err != nil || relative != "meta.txt" {
		return observation, err
	}
	observation.HasXattrs = true
	observation.XattrNames = []string{root.name}
	return observation, nil
}

type staticGitAdapter struct {
	observation RawGitObservation
	roots       []string
}

func (adapter *staticGitAdapter) Observe(_ context.Context, root string, _ Bounds) (RawGitObservation, error) {
	adapter.roots = append(adapter.roots, root)
	return adapter.observation, nil
}

type staticPolicyFactory struct {
	policy staticPolicy
}

func (factory staticPolicyFactory) New(context.Context, string, IgnoreConfig, Bounds) (Policy, error) {
	return factory.policy, nil
}

type staticPolicy struct {
	fingerprint string
}

func (policy staticPolicy) Fingerprint() string { return policy.fingerprint }
func (staticPolicy) Resolve(relative string, _ bool) (filepolicy.Resolution, error) {
	decision := filepolicy.EvaluateBuiltIn(filepolicy.ProfileManaged, relative)
	decision.SourceFile = "/private/sensitive/.loomignore"
	return filepolicy.Resolution{Decision: decision, Trace: []filepolicy.Decision{decision}}, nil
}

type fileState struct {
	Mode    fs.FileMode
	Size    int64
	ModTime int64
	Target  string
}

func filesystemSnapshot(t *testing.T, root string) map[string]fileState {
	t.Helper()
	result := map[string]fileState{}
	err := filepath.WalkDir(root, func(value string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		info, err := os.Lstat(value)
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, value)
		if err != nil {
			return err
		}
		state := fileState{Mode: info.Mode(), Size: info.Size(), ModTime: info.ModTime().UnixNano()}
		if info.Mode()&os.ModeSymlink != 0 {
			state.Target, err = os.Readlink(value)
			if err != nil {
				return err
			}
		}
		result[filepath.ToSlash(relative)] = state
		return nil
	})
	if err != nil {
		t.Fatalf("snapshot filesystem: %v", err)
	}
	return result
}

func gitStateSnapshot(t *testing.T, root string) []byte {
	t.Helper()
	commands := [][]string{
		{"diff-files", "--raw", "-z", "--ignore-submodules=none"},
		{"diff-index", "--cached", "--raw", "-z", "--ignore-submodules=none", "HEAD"},
		{"ls-files", "--others", "--exclude-standard", "-z"},
		{"for-each-ref", "--format=%(refname)%00%(objectname)"},
		{"worktree", "list", "--porcelain", "-z"},
	}
	var result bytes.Buffer
	for _, args := range commands {
		result.Write(gitFixture(t, root, args...))
		result.WriteByte(0)
	}
	return result.Bytes()
}

func initGitRepository(t *testing.T, root string) {
	t.Helper()
	mustMkdirAll(t, root)
	gitFixture(t, root, "init", "-b", "main")
	gitFixture(t, root, "config", "user.name", "Fixture")
	gitFixture(t, root, "config", "user.email", "fixture@example.invalid")
}

func gitFixture(t *testing.T, root string, args ...string) []byte {
	t.Helper()
	commandArgs := append([]string{"-C", root}, args...)
	command := exec.Command("git", commandArgs...)
	home := filepath.Join(filepath.Dir(root), "fixture-home")
	command.Env = append(filteredEnvironment(os.Environ(), "HOME", "XDG_CONFIG_HOME", "GIT_CONFIG_GLOBAL", "GIT_CONFIG_NOSYSTEM"),
		"HOME="+home,
		"XDG_CONFIG_HOME="+filepath.Join(home, ".config"),
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_OPTIONAL_LOCKS=0",
		"GIT_TERMINAL_PROMPT=0",
	)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, output)
	}
	return output
}

func filteredEnvironment(environment []string, keys ...string) []string {
	removed := map[string]bool{}
	for _, key := range keys {
		removed[key] = true
	}
	result := []string{}
	for _, item := range environment {
		key := item
		if index := strings.IndexByte(item, '='); index >= 0 {
			key = item[:index]
		}
		if !removed[key] {
			result = append(result, item)
		}
	}
	return result
}

func mustMkdirAll(t *testing.T, value string) {
	t.Helper()
	if err := os.MkdirAll(value, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", value, err)
	}
}

func mustWriteFile(t *testing.T, value, content string) {
	t.Helper()
	mustMkdirAll(t, filepath.Dir(value))
	if err := os.WriteFile(value, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", value, err)
	}
}

func findRoot(t *testing.T, report Inventory, id string) RootInventory {
	t.Helper()
	for _, root := range report.Roots {
		if root.ID == id {
			return root
		}
	}
	t.Fatalf("root %q not found in %#v", id, report.Roots)
	return RootInventory{}
}

func findEntry(t *testing.T, root RootInventory, locator string) Entry {
	t.Helper()
	for _, entry := range root.Entries {
		if entry.Locator == locator {
			return entry
		}
	}
	t.Fatalf("entry %q not found in root %q", locator, root.ID)
	return Entry{}
}

func findRepository(t *testing.T, report Inventory, locator string) RepositoryObservation {
	t.Helper()
	for _, repository := range report.Repositories {
		if repository.Locator == locator {
			return repository
		}
	}
	t.Fatalf("repository %q not found in %#v", locator, report.Repositories)
	return RepositoryObservation{}
}

func duplicateContains(groups []DuplicateObject, identity string, locators ...string) bool {
	for _, group := range groups {
		if group.ObjectIdentity != identity {
			continue
		}
		for _, locator := range locators {
			if !contains(group.Locators, locator) {
				return false
			}
		}
		return true
	}
	return false
}

func contains(values []string, value string) bool {
	index := sort.SearchStrings(values, value)
	return index < len(values) && values[index] == value
}

func hasRef(refs []GitRef, name string) bool {
	for _, ref := range refs {
		if ref.Name == name {
			return true
		}
	}
	return false
}

func hasWorktree(worktrees []GitWorktree, locator string) bool {
	for _, worktree := range worktrees {
		if worktree.Locator == locator {
			return true
		}
	}
	return false
}
