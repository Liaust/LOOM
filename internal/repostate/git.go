package repostate

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	gitObservationOutputLimit = 16 * 1024 * 1024
	gitObservationErrorLimit  = 4 * 1024
	gitObservationPathLimit   = 4096
)

type GitTrackedState string

const (
	GitTrackedClean       GitTrackedState = "clean"
	GitTrackedDirty       GitTrackedState = "dirty"
	GitTrackedUnavailable GitTrackedState = "unavailable"
)

type GitEntry struct {
	Path    string `json:"path"`
	Mode    string `json:"mode"`
	Content []byte `json:"-"`
}

type GitObservation struct {
	ObservedAt         time.Time       `json:"observed_at"`
	HeadCommit         string          `json:"head_commit"`
	SourceCommit       string          `json:"source_commit,omitempty"`
	LatestRepoCommitAt time.Time       `json:"latest_repo_commit_at,omitempty"`
	TrackedState       GitTrackedState `json:"tracked_state"`
	Entries            []GitEntry      `json:"entries"`
	UntrackedPaths     []string        `json:"untracked_paths"`
}

type GitObserver interface {
	Observe(context.Context, string) (GitObservation, error)
}

type GitCommandRunner interface {
	Run(context.Context, string, ...string) ([]byte, error)
}

type GitObservationError struct {
	Code string
	err  error
}

func (err *GitObservationError) Error() string {
	return "repository Git observation unavailable: " + err.Code
}

func (err *GitObservationError) Unwrap() error { return err.err }

type OSGitObserver struct {
	Runner             GitCommandRunner
	Now                func() time.Time
	afterStateRootOpen func()
	beforeTrackedOpen  func(string)
}

// Observe performs bounded, non-locking Git reads and reads only tracked
// regular worktree files beneath .repo. It does not update the index.
func (observer OSGitObserver) Observe(ctx context.Context, repositoryRoot string) (GitObservation, error) {
	runner := observer.Runner
	if runner == nil {
		runner = OSGitCommandRunner{}
	}
	repositoryRoot = filepath.Clean(strings.TrimSpace(repositoryRoot))
	if !filepath.IsAbs(repositoryRoot) {
		return GitObservation{}, gitObservationError("invalid_repository_root", errors.New("repository root must be absolute"))
	}
	topLevel, err := runner.Run(ctx, repositoryRoot, "rev-parse", "--path-format=absolute", "--show-toplevel")
	if err != nil {
		return GitObservation{}, gitObservationError("not_git_repository", err)
	}
	if !samePath(repositoryRoot, strings.TrimSpace(string(topLevel))) {
		return GitObservation{}, gitObservationError("git_root_mismatch", errors.New("Git top-level differs from repository root"))
	}
	stateRoot, present, err := openStateRoot(repositoryRoot, observer.afterStateRootOpen)
	if !present {
		return GitObservation{}, gitObservationError("tracked_file_missing", errors.New(".repo is absent"))
	}
	if err != nil {
		return GitObservation{}, gitConfinedReadError(err)
	}
	defer stateRoot.Close()
	headOutput, err := runner.Run(ctx, repositoryRoot, "rev-parse", "--verify", "HEAD")
	if err != nil {
		return GitObservation{}, gitObservationError("head_unavailable", err)
	}
	head := strings.TrimSpace(string(headOutput))
	if !validGitObjectID(head) {
		return GitObservation{}, gitObservationError("head_malformed", errors.New("Git returned an invalid HEAD object ID"))
	}

	indexOutput, err := runner.Run(ctx, repositoryRoot, "ls-files", "--stage", "-z", "--", StateRoot)
	if err != nil {
		return GitObservation{}, gitObservationError("tracked_sources_unavailable", err)
	}
	entries, err := parseGitIndex(stateRoot, indexOutput, observer.beforeTrackedOpen)
	if err != nil {
		return GitObservation{}, err
	}
	statusOutput, err := runner.Run(ctx, repositoryRoot, "status", "--porcelain=v2", "-z", "--untracked-files=all", "--ignore-submodules=none", "--", StateRoot)
	if err != nil {
		return GitObservation{}, gitObservationError("status_unavailable", err)
	}
	dirty, untracked, err := parseRepoStatus(statusOutput)
	if err != nil {
		return GitObservation{}, gitObservationError("status_malformed", err)
	}

	sourceCommit := ""
	latestRepoCommitAt := time.Time{}
	logOutput, logErr := runner.Run(ctx, repositoryRoot, "log", "-1", "--format=%H%x00%cI", "--", StateRoot)
	if logErr != nil {
		return GitObservation{}, gitObservationError("source_history_unavailable", logErr)
	}
	if len(bytes.TrimSpace(logOutput)) > 0 {
		parts := bytes.SplitN(bytes.TrimSpace(logOutput), []byte{0}, 2)
		if len(parts) != 2 || !validGitObjectID(string(parts[0])) {
			return GitObservation{}, gitObservationError("source_commit_malformed", errors.New("Git returned malformed .repo history"))
		}
		sourceCommit = string(parts[0])
		latestRepoCommitAt, err = time.Parse(time.RFC3339, string(parts[1]))
		if err != nil {
			return GitObservation{}, gitObservationError("source_commit_time_malformed", err)
		}
		latestRepoCommitAt = latestRepoCommitAt.UTC()
	}

	now := time.Now().UTC()
	if observer.Now != nil {
		now = observer.Now().UTC()
	}
	if err := stateRoot.verifyRootBinding(); err != nil {
		return GitObservation{}, gitConfinedReadError(err)
	}
	trackedState := GitTrackedClean
	if dirty {
		trackedState = GitTrackedDirty
	}
	return GitObservation{
		ObservedAt:         now,
		HeadCommit:         head,
		SourceCommit:       sourceCommit,
		LatestRepoCommitAt: latestRepoCommitAt,
		TrackedState:       trackedState,
		Entries:            entries,
		UntrackedPaths:     untracked,
	}, nil
}

type OSGitCommandRunner struct{}

func (OSGitCommandRunner) Run(ctx context.Context, directory string, args ...string) ([]byte, error) {
	commandArgs := append([]string{"-c", "core.fsmonitor=false", "-C", directory}, args...)
	command := exec.CommandContext(ctx, "git", commandArgs...)
	command.Env = repositoryGitEnvironment(os.Environ())
	stdout := limitedBuffer{limit: gitObservationOutputLimit}
	stderr := limitedBuffer{limit: gitObservationErrorLimit}
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		return nil, fmt.Errorf("%w: %s", err, boundedDiagnostic(stderr.String()))
	}
	if stdout.truncated {
		return nil, errors.New("Git output exceeded bounded observation limit")
	}
	return append([]byte(nil), stdout.Bytes()...), nil
}

type limitedBuffer struct {
	buffer    bytes.Buffer
	limit     int
	truncated bool
}

func (buffer *limitedBuffer) Write(payload []byte) (int, error) {
	remaining := buffer.limit - buffer.buffer.Len()
	if remaining > 0 {
		keep := len(payload)
		if keep > remaining {
			keep = remaining
		}
		_, _ = buffer.buffer.Write(payload[:keep])
	}
	if len(payload) > remaining {
		buffer.truncated = true
	}
	return len(payload), nil
}

func (buffer *limitedBuffer) Bytes() []byte  { return buffer.buffer.Bytes() }
func (buffer *limitedBuffer) String() string { return buffer.buffer.String() }

func parseGitIndex(stateRoot *stateRootHandle, payload []byte, beforeOpen func(string)) ([]GitEntry, error) {
	records := bytes.Split(payload, []byte{0})
	entries := make([]GitEntry, 0, len(records))
	seen := map[string]struct{}{}
	totalBytes := 0
	for _, record := range records {
		if len(record) == 0 {
			continue
		}
		tab := bytes.IndexByte(record, '\t')
		if tab < 0 {
			return nil, gitObservationError("index_malformed", errors.New("tracked source record lacks a path"))
		}
		metadata := strings.Fields(string(record[:tab]))
		if len(metadata) != 3 || metadata[2] != "0" {
			return nil, gitObservationError("index_malformed", errors.New("tracked source has an unsupported index stage"))
		}
		mode := metadata[0]
		if !regularGitMode(mode) {
			return nil, gitObservationError("non_regular_entry", errors.New("non-regular Git entry beneath .repo"))
		}
		sourcePath := string(record[tab+1:])
		if !validRepoSourcePath(sourcePath) {
			return nil, gitObservationError("index_path_invalid", errors.New("tracked source path is not portable"))
		}
		if _, duplicate := seen[sourcePath]; duplicate {
			return nil, gitObservationError("index_malformed", errors.New("duplicate tracked source path"))
		}
		seen[sourcePath] = struct{}{}
		if len(seen) > gitObservationPathLimit {
			return nil, gitObservationError("index_too_large", errors.New("tracked source count exceeds bounded limit"))
		}
		content, _, err := stateRoot.readRegularFile(sourcePath, nil, maxRepositoryStateFile, beforeOpen)
		if err != nil {
			return nil, gitConfinedReadError(err)
		}
		totalBytes += len(content)
		if totalBytes > maxRepositoryStateBytes {
			return nil, gitObservationError("tracked_sources_too_large", errors.New("tracked sources exceed bounded byte limit"))
		}
		entries = append(entries, GitEntry{Path: sourcePath, Mode: mode, Content: content})
	}
	sort.Slice(entries, func(left, right int) bool { return entries[left].Path < entries[right].Path })
	return entries, nil
}

func gitConfinedReadError(err error) error {
	code := "tracked_file_unreadable"
	switch {
	case confinedErrorHasKind(err, confinedMissing):
		code = "tracked_file_missing"
	case confinedErrorHasKind(err, confinedNonRegular):
		code = "non_regular_entry"
	case confinedErrorHasKind(err, confinedReplaced):
		code = "tracked_file_unreadable"
	case confinedErrorHasKind(err, confinedTooLarge):
		code = "tracked_file_unreadable"
	}
	return gitObservationError(code, err)
}

func parseRepoStatus(payload []byte) (bool, []string, error) {
	records := bytes.Split(payload, []byte{0})
	dirty := false
	untracked := []string{}
	for index := 0; index < len(records); index++ {
		record := string(records[index])
		if record == "" {
			continue
		}
		switch {
		case strings.HasPrefix(record, "1 "), strings.HasPrefix(record, "u "):
			dirty = true
		case strings.HasPrefix(record, "2 "):
			dirty = true
			if index+1 >= len(records) {
				return false, nil, errors.New("rename record is missing its original path")
			}
			index++
		case strings.HasPrefix(record, "? "):
			dirty = true
			path := strings.TrimPrefix(record, "? ")
			if !validRepoSourcePath(path) {
				return false, nil, errors.New("untracked source path is not portable")
			}
			untracked = append(untracked, path)
		case strings.HasPrefix(record, "! "):
			// Ignored files are runtime-local and excluded from portable source.
		default:
			return false, nil, errors.New("unsupported Git status record")
		}
	}
	sort.Strings(untracked)
	return dirty, untracked, nil
}

func repositoryGitEnvironment(environment []string) []string {
	locked := make([]string, 0, len(environment)+4)
	for _, entry := range environment {
		key := entry
		if separator := strings.IndexByte(entry, '='); separator >= 0 {
			key = entry[:separator]
		}
		switch key {
		case "GIT_OPTIONAL_LOCKS", "GIT_TERMINAL_PROMPT", "GIT_DIR", "GIT_WORK_TREE", "GIT_COMMON_DIR", "GIT_INDEX_FILE", "GIT_OBJECT_DIRECTORY", "GIT_ALTERNATE_OBJECT_DIRECTORIES", "GIT_CEILING_DIRECTORIES", "GIT_DISCOVERY_ACROSS_FILESYSTEM", "LC_ALL", "LANG":
			continue
		default:
			locked = append(locked, entry)
		}
	}
	return append(locked, "GIT_OPTIONAL_LOCKS=0", "GIT_TERMINAL_PROMPT=0", "LC_ALL=C", "LANG=C")
}

func gitObservationError(code string, err error) *GitObservationError {
	return &GitObservationError{Code: code, err: err}
}

func gitObservationErrorCode(err error) string {
	var observationError *GitObservationError
	if errors.As(err, &observationError) {
		return observationError.Code
	}
	return "git_observation_unavailable"
}

func validRepoSourcePath(value string) bool {
	return strings.HasPrefix(value, StateRoot+"/") && !strings.Contains(value, "\\") && filepath.ToSlash(filepath.Clean(value)) == value && !strings.Contains(value, "../")
}

func regularGitMode(value string) bool { return value == "100644" || value == "100755" }

func validGitObjectID(value string) bool {
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

func samePath(left, right string) bool {
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

func boundedDiagnostic(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > 256 {
		return value[:256]
	}
	return value
}
