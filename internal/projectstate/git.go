package projectstate

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	gitCommandOutputLimit = 2 * 1024 * 1024
	gitCommandErrorLimit  = 4 * 1024
	gitReferenceLimit     = 1024
)

type boundedBuffer struct {
	buffer    bytes.Buffer
	limit     int
	truncated bool
}

func (b *boundedBuffer) Write(payload []byte) (int, error) {
	remaining := b.limit - b.buffer.Len()
	if remaining > 0 {
		keep := len(payload)
		if keep > remaining {
			keep = remaining
		}
		_, _ = b.buffer.Write(payload[:keep])
	}
	if len(payload) > remaining {
		b.truncated = true
	}
	return len(payload), nil
}

func (b *boundedBuffer) Bytes() []byte  { return b.buffer.Bytes() }
func (b *boundedBuffer) String() string { return b.buffer.String() }

type GitCommandRunner interface {
	Run(context.Context, string, ...string) ([]byte, error)
}

type OSGitCommandRunner struct{}

func (OSGitCommandRunner) Run(ctx context.Context, directory string, args ...string) ([]byte, error) {
	commandArgs := append([]string{"-c", "core.fsmonitor=false", "-C", directory}, args...)
	command := exec.CommandContext(ctx, "git", commandArgs...)
	command.Env = gitObservationEnvironment(os.Environ())
	stdout := boundedBuffer{limit: gitCommandOutputLimit}
	stderr := boundedBuffer{limit: gitCommandErrorLimit}
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		return nil, &gitCommandError{err: err, stderr: boundedGitDiagnostic(stderr.String())}
	}
	if stdout.truncated {
		return nil, &gitCommandError{err: ErrGitUnavailable, stderr: "git output exceeded bounded observation limit"}
	}
	return append([]byte(nil), stdout.Bytes()...), nil
}

func gitObservationEnvironment(environment []string) []string {
	locked := make([]string, 0, len(environment)+4)
	for _, entry := range environment {
		key := entry
		if separator := strings.IndexByte(entry, '='); separator >= 0 {
			key = entry[:separator]
		}
		switch key {
		case "GIT_OPTIONAL_LOCKS", "GIT_TERMINAL_PROMPT", "LC_ALL", "LANG":
			continue
		default:
			locked = append(locked, entry)
		}
	}
	return append(locked,
		"GIT_OPTIONAL_LOCKS=0",
		"GIT_TERMINAL_PROMPT=0",
		"LC_ALL=C",
		"LANG=C",
	)
}

type GitAdapter struct {
	Runner GitCommandRunner
}

type GitObservationError struct {
	Code string
	err  error
}

func (e *GitObservationError) Error() string {
	return "git observation unavailable: " + e.Code
}

func (e *GitObservationError) Unwrap() error { return e.err }

type gitCommandError struct {
	err    error
	stderr string
}

func (e *gitCommandError) Error() string {
	if e.stderr == "" {
		return e.err.Error()
	}
	return e.err.Error() + ": " + e.stderr
}

func (e *gitCommandError) Unwrap() error { return e.err }

func (a GitAdapter) Observe(ctx context.Context, memberRoot string) (GitProjection, error) {
	runner := a.Runner
	if runner == nil {
		runner = OSGitCommandRunner{}
	}
	memberRoot = filepath.Clean(strings.TrimSpace(memberRoot))
	if memberRoot == "" || !filepath.IsAbs(memberRoot) {
		return GitProjection{}, &GitObservationError{Code: "invalid_member_root", err: ErrGitUnavailable}
	}

	gitDir, err := runGitPath(ctx, runner, memberRoot, "rev-parse", "--path-format=absolute", "--git-dir")
	if err != nil {
		return GitProjection{}, &GitObservationError{Code: "not_git_repository", err: err}
	}
	commonDir, err := runGitPath(ctx, runner, memberRoot, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return GitProjection{}, &GitObservationError{Code: "git_common_dir_unavailable", err: err}
	}
	bareOutput, err := runner.Run(ctx, memberRoot, "rev-parse", "--is-bare-repository")
	if err != nil {
		return GitProjection{}, &GitObservationError{Code: "git_identity_unavailable", err: err}
	}
	bare := strings.TrimSpace(string(bareOutput)) == "true"
	if bare {
		return GitProjection{}, &GitObservationError{Code: "bare_repository", err: ErrGitUnavailable}
	}
	topLevel, err := runGitPath(ctx, runner, memberRoot, "rev-parse", "--path-format=absolute", "--show-toplevel")
	if err != nil {
		return GitProjection{}, &GitObservationError{Code: "git_top_level_unavailable", err: err}
	}
	rootMatches := sameResolvedPath(memberRoot, topLevel)
	if !rootMatches {
		return GitProjection{}, &GitObservationError{Code: "git_root_mismatch", err: ErrGitUnavailable}
	}

	statusOutput, err := runner.Run(ctx, memberRoot, "status", "--porcelain=v2", "--branch", "-z", "--untracked-files=all", "--ignore-submodules=none")
	if err != nil {
		return GitProjection{}, &GitObservationError{Code: "git_status_unavailable", err: err}
	}
	projection, err := parseGitStatusPorcelainV2(statusOutput)
	if err != nil {
		return GitProjection{}, &GitObservationError{Code: "git_status_malformed", err: err}
	}
	identity := sha256.Sum256([]byte("git-common-dir\x00" + filepath.Clean(commonDir)))
	projection.LocalIdentityDigest = fmt.Sprintf("sha256:%x", identity[:])
	projection.Worktree = !sameResolvedPath(gitDir, commonDir)
	projection.Bare = bare
	projection.RootMatchesMember = rootMatches

	defaultBranch, defaultErr := runner.Run(ctx, memberRoot, "symbolic-ref", "--quiet", "--short", "refs/remotes/origin/HEAD")
	if defaultErr == nil && strings.TrimSpace(string(defaultBranch)) != "" {
		value := strings.TrimSpace(string(defaultBranch))
		if len(value) > gitReferenceLimit {
			return GitProjection{}, &GitObservationError{Code: "git_default_branch_malformed", err: ErrGitUnavailable}
		}
		projection.DefaultBranch = value
		projection.DefaultBranchPosture = GitDefaultBranchObserved
	} else {
		projection.DefaultBranchPosture = GitDefaultBranchNotObserved
	}
	return projection, nil
}

func runGitPath(ctx context.Context, runner GitCommandRunner, root string, args ...string) (string, error) {
	output, err := runner.Run(ctx, root, args...)
	if err != nil {
		return "", err
	}
	value := strings.TrimSpace(string(output))
	if value == "" {
		return "", fmt.Errorf("git returned an empty path")
	}
	if !filepath.IsAbs(value) {
		value = filepath.Join(root, value)
	}
	return filepath.Clean(value), nil
}

func parseGitStatusPorcelainV2(payload []byte) (GitProjection, error) {
	projection := GitProjection{
		DefaultBranchPosture: GitDefaultBranchNotObserved,
		HeadPosture:          GitHeadUnborn,
	}
	records := bytes.Split(payload, []byte{0})
	for index := 0; index < len(records); index++ {
		record := string(records[index])
		if record == "" {
			continue
		}
		switch {
		case strings.HasPrefix(record, "# branch.oid "):
			value := strings.TrimSpace(strings.TrimPrefix(record, "# branch.oid "))
			if value != "(initial)" {
				if !isGitObjectID(value) {
					return GitProjection{}, fmt.Errorf("invalid branch oid")
				}
				projection.Head = value
				projection.HeadPosture = GitHeadObserved
			}
		case strings.HasPrefix(record, "# branch.head "):
			value := strings.TrimSpace(strings.TrimPrefix(record, "# branch.head "))
			if len(value) > gitReferenceLimit {
				return GitProjection{}, fmt.Errorf("current branch exceeds bounded field limit")
			}
			if value == "(detached)" {
				projection.Detached = true
			} else {
				projection.CurrentBranch = value
			}
		case strings.HasPrefix(record, "# branch.upstream "):
			value := strings.TrimSpace(strings.TrimPrefix(record, "# branch.upstream "))
			if len(value) > gitReferenceLimit {
				return GitProjection{}, fmt.Errorf("upstream branch exceeds bounded field limit")
			}
			projection.Upstream = value
		case strings.HasPrefix(record, "# branch.ab "):
			fields := strings.Fields(strings.TrimSpace(strings.TrimPrefix(record, "# branch.ab ")))
			if len(fields) != 2 {
				return GitProjection{}, fmt.Errorf("invalid ahead/behind record")
			}
			ahead, err := parseGitCount(fields[0], "+")
			if err != nil {
				return GitProjection{}, err
			}
			behind, err := parseGitCount(fields[1], "-")
			if err != nil {
				return GitProjection{}, err
			}
			projection.Ahead = ahead
			projection.Behind = behind
			projection.AheadBehindObserved = true
		case strings.HasPrefix(record, "1 "), strings.HasPrefix(record, "2 "):
			fields := strings.Fields(record)
			if len(fields) < 3 || len(fields[1]) != 2 {
				return GitProjection{}, fmt.Errorf("invalid tracked change record")
			}
			projection.Dirty.TrackedChanges = true
			if fields[1] == "UU" || strings.Contains(fields[1], "U") {
				projection.Dirty.Conflicts = true
			}
			if fields[2] != "N..." {
				projection.Dirty.SubmoduleChanges = true
			}
			if strings.HasPrefix(record, "2 ") && index+1 < len(records) {
				index++ // porcelain v2 -z emits the original rename path separately.
			}
		case strings.HasPrefix(record, "u "):
			projection.Dirty.TrackedChanges = true
			projection.Dirty.Conflicts = true
			fields := strings.Fields(record)
			if len(fields) >= 3 && fields[2] != "N..." {
				projection.Dirty.SubmoduleChanges = true
			}
		case strings.HasPrefix(record, "? "):
			projection.Dirty.UntrackedChanges = true
		case strings.HasPrefix(record, "! "):
			// Ignored files do not make a repository dirty.
		default:
			return GitProjection{}, fmt.Errorf("unsupported porcelain v2 record")
		}
	}
	projection.Dirty.Dirty = projection.Dirty.TrackedChanges || projection.Dirty.UntrackedChanges || projection.Dirty.Conflicts || projection.Dirty.SubmoduleChanges
	if projection.HeadPosture == GitHeadObserved && projection.Head == "" {
		return GitProjection{}, fmt.Errorf("observed head is empty")
	}
	return projection, nil
}

func parseGitCount(value, prefix string) (int, error) {
	if !strings.HasPrefix(value, prefix) {
		return 0, fmt.Errorf("invalid ahead/behind count")
	}
	count, err := strconv.Atoi(strings.TrimPrefix(value, prefix))
	if err != nil || count < 0 {
		return 0, fmt.Errorf("invalid ahead/behind count")
	}
	return count, nil
}

func isGitObjectID(value string) bool {
	if len(value) < 40 || len(value) > 64 {
		return false
	}
	for _, character := range value {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func sameResolvedPath(left, right string) bool {
	leftResolved, leftErr := filepath.EvalSymlinks(filepath.Clean(left))
	rightResolved, rightErr := filepath.EvalSymlinks(filepath.Clean(right))
	if leftErr == nil {
		left = leftResolved
	}
	if rightErr == nil {
		right = rightResolved
	}
	return filepath.Clean(left) == filepath.Clean(right)
}

func gitObservationReason(err error) string {
	var observationError *GitObservationError
	if errors.As(err, &observationError) && strings.TrimSpace(observationError.Code) != "" {
		return observationError.Code
	}
	return "git_unavailable"
}

func boundedGitDiagnostic(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > 256 {
		return value[:256]
	}
	return value
}
