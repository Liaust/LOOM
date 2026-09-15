package watchedroots

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"loom.local/loom/internal/communication"
	"loom.local/loom/internal/filesystemconnector"
	"loom.local/loom/internal/ids"
	noderuntime "loom.local/loom/internal/nodeagent/runtime"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type Store struct {
	DataDir string
}

const ProtectedFolderReconcileStateSchemaVersion = "protected_folder.reconcile_state.v1"

type ProtectedFolderReconcileState struct {
	SchemaVersion   string    `json:"schema_version"`
	AppliedRevision int64     `json:"applied_revision"`
	ConfigHash      string    `json:"config_hash"`
	RootKeys        []string  `json:"root_keys"`
	UpdatedAt       time.Time `json:"updated_at"`
}

func NewStore(dataDir string) Store {
	return Store{DataDir: dataDir}
}

func (s Store) LoadProtectedFolderReconcileState() (ProtectedFolderReconcileState, error) {
	var state ProtectedFolderReconcileState
	err := readJSONFile(filepath.Join(s.DataDir, "watched-roots", "protected-folder-reconcile.json"), &state)
	return state, err
}

func (s Store) SaveProtectedFolderReconcileState(state ProtectedFolderReconcileState) error {
	if strings.TrimSpace(s.DataDir) == "" {
		return errors.New("watched-root data dir is required")
	}
	if state.SchemaVersion == "" {
		state.SchemaVersion = ProtectedFolderReconcileStateSchemaVersion
	}
	if state.SchemaVersion != ProtectedFolderReconcileStateSchemaVersion {
		return errors.New("unsupported protected-folder reconcile state schema")
	}
	if state.AppliedRevision <= 0 || strings.TrimSpace(state.ConfigHash) == "" {
		return errors.New("protected-folder reconcile state requires revision and config hash")
	}
	sort.Strings(state.RootKeys)
	state.UpdatedAt = time.Now().UTC()
	return writeJSONFile(filepath.Join(s.DataDir, "watched-roots", "protected-folder-reconcile.json"), state, 0o600)
}

func (s Store) DeleteProtectedFolderReconcileState() error {
	err := os.Remove(filepath.Join(s.DataDir, "watched-roots", "protected-folder-reconcile.json"))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

func (s Store) EnsureRoot(rootKey string) error {
	if strings.TrimSpace(s.DataDir) == "" {
		return errors.New("watched-root data dir is required")
	}
	rootKey = normalizeRootKey(rootKey)
	if rootKey == "" {
		return errors.New("watched-root root key is required")
	}
	for _, dir := range []string{
		s.RootDir(rootKey),
		s.pathsDir(rootKey),
		s.findingsDir(rootKey),
	} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
	}
	return nil
}

func (s Store) RootDir(rootKey string) string {
	return filepath.Join(s.DataDir, "watched-roots", safeFileSegment(rootKey))
}

func (s Store) RootPaths(rootKey string) RootPaths {
	return RootPaths{
		RootDir:       s.RootDir(rootKey),
		Checkpoint:    filepath.Join(s.RootDir(rootKey), "checkpoint.json"),
		DirtyHints:    filepath.Join(s.RootDir(rootKey), "dirty-hints.json"),
		LatestSummary: filepath.Join(s.RootDir(rootKey), "latest-summary.json"),
		PathsDir:      s.pathsDir(rootKey),
		FindingsDir:   s.findingsDir(rootKey),
	}
}

func (s Store) SaveLatestSummary(rootKey string, summary RootSummary) error {
	if err := s.EnsureRoot(rootKey); err != nil {
		return err
	}
	if summary.SchemaVersion == "" {
		summary.SchemaVersion = SummarySchemaVersion
	}
	if summary.RootKey == "" {
		summary.RootKey = normalizeRootKey(rootKey)
	}
	if summary.GeneratedAt.IsZero() {
		summary.GeneratedAt = time.Now().UTC()
	}
	return writeJSONFile(filepath.Join(s.RootDir(rootKey), "latest-summary.json"), summary, 0o600)
}

func (s Store) LoadLatestSummary(rootKey string) (RootSummary, error) {
	var summary RootSummary
	err := readJSONFile(filepath.Join(s.RootDir(rootKey), "latest-summary.json"), &summary)
	return summary, err
}

func (s Store) SaveCheckpoint(rootKey string, checkpoint RootCheckpoint) error {
	if err := s.EnsureRoot(rootKey); err != nil {
		return err
	}
	if checkpoint.SchemaVersion == "" {
		checkpoint.SchemaVersion = CheckpointSchemaVersion
	}
	if checkpoint.RootKey == "" {
		checkpoint.RootKey = normalizeRootKey(rootKey)
	}
	return writeJSONFile(filepath.Join(s.RootDir(rootKey), "checkpoint.json"), checkpoint, 0o600)
}

func (s Store) LoadCheckpoint(rootKey string) (RootCheckpoint, error) {
	var checkpoint RootCheckpoint
	err := readJSONFile(filepath.Join(s.RootDir(rootKey), "checkpoint.json"), &checkpoint)
	return checkpoint, err
}

func (s Store) SavePathState(state PathState) error {
	if state.RootKey == "" {
		return errors.New("path state root key is required")
	}
	if state.RelativePath == "" {
		return errors.New("path state relative path is required")
	}
	if state.SchemaVersion == "" {
		state.SchemaVersion = PathStateSchemaVersion
	}
	state.RootKey = normalizeRootKey(state.RootKey)
	state.PathKey = PathKey(state.RootKey, state.RelativePath)
	if state.HashStatus == "" {
		state.HashStatus = HashStatusNotNeeded
	}
	if state.FirstSeenAt.IsZero() {
		state.FirstSeenAt = time.Now().UTC()
	}
	if state.LastSeenAt.IsZero() {
		state.LastSeenAt = state.FirstSeenAt
	}
	if state.LastScannedAt.IsZero() {
		state.LastScannedAt = state.LastSeenAt
	}
	if err := s.EnsureRoot(state.RootKey); err != nil {
		return err
	}
	return writeJSONFile(s.pathStatePath(state.RootKey, state.PathKey), state, 0o600)
}

func (s Store) LoadPathState(rootKey, relativePath string) (PathState, error) {
	rootKey = normalizeRootKey(rootKey)
	pathKey := PathKey(rootKey, relativePath)
	return s.LoadPathStateByKey(rootKey, pathKey)
}

func (s Store) LoadPathStateByKey(rootKey, pathKey string) (PathState, error) {
	var state PathState
	err := readJSONFile(s.pathStatePath(rootKey, pathKey), &state)
	return state, err
}

func (s Store) ListPathStates(rootKey string, limit int) ([]PathState, error) {
	if err := s.EnsureRoot(rootKey); err != nil {
		return nil, err
	}
	states := []PathState{}
	root := s.pathsDir(rootKey)
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		if !entry.Type().IsRegular() || !strings.HasSuffix(entry.Name(), ".json") {
			return nil
		}
		var state PathState
		if err := readJSONFile(path, &state); err != nil {
			return err
		}
		states = append(states, state)
		return nil
	})
	if errors.Is(err, fs.ErrNotExist) {
		return []PathState{}, nil
	}
	if err != nil {
		return nil, err
	}
	sort.Slice(states, func(i, j int) bool {
		return states[i].RelativePath < states[j].RelativePath
	})
	if limit > 0 && len(states) > limit {
		states = states[:limit]
	}
	return states, nil
}

func (s Store) ListPathStatesByStatus(rootKey, status string, limit int) ([]PathState, error) {
	states, err := s.ListPathStates(rootKey, 0)
	if err != nil {
		return nil, err
	}
	filtered := make([]PathState, 0, len(states))
	for _, state := range states {
		if state.Status == status {
			filtered = append(filtered, state)
		}
	}
	if limit > 0 && len(filtered) > limit {
		filtered = filtered[:limit]
	}
	return filtered, nil
}

func (s Store) DeletePathState(rootKey, relativePath string) error {
	rootKey = normalizeRootKey(rootKey)
	pathKey := PathKey(rootKey, relativePath)
	err := os.Remove(s.pathStatePath(rootKey, pathKey))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

func (s Store) AddDirtyHint(rootKey string, hint DirtyHint) error {
	if err := s.EnsureRoot(rootKey); err != nil {
		return err
	}
	hints, err := s.loadDirtyHintMap(rootKey)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	hint.RootKey = normalizeRootKey(rootKey)
	hint.RelativePath = normalizeDirtyPath(hint.RelativePath)
	if strings.TrimSpace(hint.HintKind) == "" {
		hint.HintKind = DirtyHintModified
	}
	if strings.TrimSpace(hint.Source) == "" {
		hint.Source = DirtyHintSourceManual
	}
	if hint.FirstSeenAt.IsZero() {
		hint.FirstSeenAt = now
	}
	if hint.LastSeenAt.IsZero() {
		hint.LastSeenAt = now
	}
	if hint.Count <= 0 {
		hint.Count = 1
	}
	key := dirtyHintKey(hint.RelativePath)
	if existing, ok := hints[key]; ok {
		existing.HintKind = hint.HintKind
		existing.Source = hint.Source
		existing.Count += hint.Count
		existing.LastSeenAt = hint.LastSeenAt
		existing.LastErrorCode = hint.LastErrorCode
		hints[key] = existing
	} else {
		hints[key] = hint
	}
	return s.saveDirtyHintMap(rootKey, hints)
}

func (s Store) ListDirtyHints(rootKey string) ([]DirtyHint, error) {
	if err := s.EnsureRoot(rootKey); err != nil {
		return nil, err
	}
	hints, err := s.loadDirtyHintMap(rootKey)
	if err != nil {
		return nil, err
	}
	out := make([]DirtyHint, 0, len(hints))
	for _, hint := range hints {
		out = append(out, hint)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].RelativePath < out[j].RelativePath
	})
	return out, nil
}

func (s Store) ClearDirtyHints(rootKey string, paths []string) error {
	if err := s.EnsureRoot(rootKey); err != nil {
		return err
	}
	if len(paths) == 0 {
		return s.saveDirtyHintMap(rootKey, map[string]DirtyHint{})
	}
	hints, err := s.loadDirtyHintMap(rootKey)
	if err != nil {
		return err
	}
	for _, path := range paths {
		delete(hints, dirtyHintKey(normalizeDirtyPath(path)))
	}
	return s.saveDirtyHintMap(rootKey, hints)
}

func (s Store) MarkRescanRequired(rootKey, reason string) error {
	return s.AddDirtyHint(rootKey, DirtyHint{
		RelativePath:  "",
		HintKind:      DirtyHintRescanRequired,
		Source:        DirtyHintSourceScanner,
		LastErrorCode: strings.TrimSpace(reason),
	})
}

func (s Store) SaveFinding(finding Finding) error {
	if strings.TrimSpace(finding.RootKey) == "" {
		return errors.New("finding root key is required")
	}
	if strings.TrimSpace(finding.Kind) == "" {
		return errors.New("finding kind is required")
	}
	finding.RootKey = normalizeRootKey(finding.RootKey)
	if strings.TrimSpace(finding.FindingID) == "" {
		finding.FindingID = FindingID(finding.RootKey, finding.Kind, finding.RelativePath)
	}
	if strings.TrimSpace(finding.Severity) == "" {
		finding.Severity = FindingSeverityWarning
	}
	finding.Status = NormalizeFindingStatus(finding.Status)
	now := time.Now().UTC()
	if finding.FirstSeenAt.IsZero() {
		finding.FirstSeenAt = now
	}
	if finding.LastSeenAt.IsZero() {
		finding.LastSeenAt = now
	}
	if err := s.EnsureRoot(finding.RootKey); err != nil {
		return err
	}
	return writeJSONFile(filepath.Join(s.findingsDir(finding.RootKey), safeFileSegment(finding.FindingID)+".json"), finding, 0o600)
}

func (s Store) ListFindings(rootKey string, limit int) ([]Finding, error) {
	if err := s.EnsureRoot(rootKey); err != nil {
		return nil, err
	}
	dir := s.findingsDir(rootKey)
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return []Finding{}, nil
	}
	if err != nil {
		return nil, err
	}
	findings := make([]Finding, 0, len(entries))
	for _, entry := range entries {
		if !entry.Type().IsRegular() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		var finding Finding
		if err := readJSONFile(filepath.Join(dir, entry.Name()), &finding); err != nil {
			return nil, err
		}
		findings = append(findings, finding)
	}
	sort.Slice(findings, func(i, j int) bool {
		return findings[i].LastSeenAt.After(findings[j].LastSeenAt)
	})
	if limit > 0 && len(findings) > limit {
		findings = findings[:limit]
	}
	return findings, nil
}

func (s Store) ResolveMissingFindings(rootKey string, active []Finding, resolvedAt time.Time) (int, error) {
	rootKey = normalizeRootKey(rootKey)
	if rootKey == "" {
		return 0, errors.New("watched-root root key is required")
	}
	if resolvedAt.IsZero() {
		resolvedAt = time.Now().UTC()
	}
	activeIDs := make(map[string]struct{}, len(active))
	for _, finding := range active {
		id := strings.TrimSpace(finding.FindingID)
		if id == "" && strings.TrimSpace(finding.Kind) != "" {
			id = FindingID(rootKey, finding.Kind, finding.RelativePath)
		}
		if id != "" {
			activeIDs[id] = struct{}{}
		}
	}
	findings, err := s.ListFindings(rootKey, 0)
	if err != nil {
		return 0, err
	}
	resolved := 0
	for _, finding := range findings {
		status := NormalizeFindingStatus(finding.Status)
		if status == FindingStatusResolved || status == FindingStatusIgnored {
			continue
		}
		id := strings.TrimSpace(finding.FindingID)
		if id == "" && strings.TrimSpace(finding.Kind) != "" {
			id = FindingID(rootKey, finding.Kind, finding.RelativePath)
		}
		if id == "" {
			continue
		}
		if _, ok := activeIDs[id]; ok {
			continue
		}
		finding.FindingID = id
		finding.RootKey = rootKey
		finding.Status = FindingStatusResolved
		finding.LastSeenAt = resolvedAt
		if err := s.SaveFinding(finding); err != nil {
			return resolved, err
		}
		resolved++
	}
	return resolved, nil
}

func NormalizeFindingStatus(status string) string {
	switch strings.TrimSpace(status) {
	case FindingStatusResolved:
		return FindingStatusResolved
	case FindingStatusIgnored:
		return FindingStatusIgnored
	default:
		return FindingStatusOpen
	}
}

func FindingIsActive(finding Finding) bool {
	return NormalizeFindingStatus(finding.Status) == FindingStatusOpen
}

func FindingIsReportable(finding Finding) bool {
	switch NormalizeFindingStatus(finding.Status) {
	case FindingStatusOpen, FindingStatusIgnored:
		return true
	default:
		return false
	}
}

func PathKey(rootKey, relativePath string) string {
	raw := normalizeRootKey(rootKey) + "\n" + strings.TrimSpace(filepath.ToSlash(relativePath))
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

func FindingID(rootKey, kind, relativePath string) string {
	raw := normalizeRootKey(rootKey) + "\n" + strings.TrimSpace(kind) + "\n" + strings.TrimSpace(filepath.ToSlash(relativePath))
	sum := sha256.Sum256([]byte(raw))
	return "finding_" + hex.EncodeToString(sum[:])[:24]
}

func IsNotExist(err error) bool {
	return errors.Is(err, fs.ErrNotExist)
}

func (s Store) pathsDir(rootKey string) string {
	return filepath.Join(s.RootDir(rootKey), "paths")
}

func (s Store) findingsDir(rootKey string) string {
	return filepath.Join(s.RootDir(rootKey), "findings")
}

func (s Store) pathStatePath(rootKey, pathKey string) string {
	if len(pathKey) < 2 {
		pathKey = "xx" + pathKey
	}
	return filepath.Join(s.pathsDir(rootKey), pathKey[:2], pathKey+".json")
}

func (s Store) dirtyHintsPath(rootKey string) string {
	return filepath.Join(s.RootDir(rootKey), "dirty-hints.json")
}

func (s Store) loadDirtyHintMap(rootKey string) (map[string]DirtyHint, error) {
	hints := map[string]DirtyHint{}
	err := readJSONFile(s.dirtyHintsPath(rootKey), &hints)
	if errors.Is(err, fs.ErrNotExist) {
		return hints, nil
	}
	if err != nil {
		return nil, err
	}
	return hints, nil
}

func (s Store) saveDirtyHintMap(rootKey string, hints map[string]DirtyHint) error {
	return writeJSONFile(s.dirtyHintsPath(rootKey), hints, 0o600)
}

func normalizeDirtyPath(path string) string {
	path = strings.TrimSpace(filepath.ToSlash(path))
	if path == "" {
		return "__rescan__"
	}
	return path
}

func dirtyHintKey(path string) string {
	return normalizeDirtyPath(path)
}

func normalizeRootKey(rootKey string) string {
	return strings.TrimSpace(rootKey)
}

func safeFileSegment(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "_"
	}
	var builder strings.Builder
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z':
			builder.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			builder.WriteRune(r)
		case r >= '0' && r <= '9':
			builder.WriteRune(r)
		case r == '-' || r == '_' || r == '.':
			builder.WriteRune(r)
		default:
			builder.WriteRune('_')
		}
	}
	out := strings.Trim(builder.String(), "._")
	if out == "" {
		return "_"
	}
	return out
}

func readJSONFile(path string, target any) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	return decoder.Decode(target)
}

func writeJSONFile(path string, value any, perm fs.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-"+filepath.Base(path)+"-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	encoder := json.NewEncoder(tmp)
	encoder.SetIndent("", "  ")
	encodeErr := encoder.Encode(value)
	closeErr := tmp.Close()
	if encodeErr != nil {
		_ = os.Remove(tmpName)
		return encodeErr
	}
	if closeErr != nil {
		_ = os.Remove(tmpName)
		return closeErr
	}
	if err := os.Chmod(tmpName, perm); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	return nil
}

const ProjectWatchReconcileStateSchemaVersion = "project_watch.reconcile_state.v1"

type ProjectWatchReconcileState struct {
	Legacy          *ProjectWatchLegacyObservation `json:"legacy,omitempty"`
	OperationID     string                         `json:"operation_id"`
	TokenHash       string                         `json:"token_hash"`
	SchemaVersion   string                         `json:"schema_version"`
	ProjectID       string                         `json:"project_id"`
	NodeID          string                         `json:"node_id"`
	ProjectRoot     string                         `json:"project_root"`
	SafeRootKey     string                         `json:"safe_root_key"`
	AppliedRevision int64                          `json:"applied_revision"`
	ConfigHash      string                         `json:"config_hash"`
	PendingRevision int64                          `json:"pending_revision"`
	PendingHash     string                         `json:"pending_hash"`
	RootKeys        []string                       `json:"root_keys"`
}

const ProjectWatchReconcileStateMaxBytes = 1024 * 1024

// This is a checked local observation, not Main's initial ownership evidence.
// The enclosing state and digest bind it to one immutable authenticated control.
type ProjectWatchLegacyObservation struct {
	OperationID       string                       `json:"operation_id"`
	TokenHash         string                       `json:"token_hash"`
	GroupHash         string                       `json:"group_hash"`
	Revision          int64                        `json:"revision"`
	PredecessorDigest string                       `json:"predecessor_digest"`
	SafeRoot          filesystemconnector.SafeRoot `json:"safe_root"`
	SuccessorSafeRoot filesystemconnector.SafeRoot `json:"successor_safe_root"`
	Members           []ProjectWatchLegacyWorker   `json:"members"`
	Digest            string                       `json:"digest"`
}
type ProjectWatchLegacyWorker struct {
	RootKey     string                     `json:"root_key"`
	Predecessor noderuntime.WorkerInstance `json:"predecessor"`
	Successor   noderuntime.WorkerInstance `json:"successor"`
}

func ProjectWatchLegacyObservationHash(value ProjectWatchLegacyObservation) (string, error) {
	value.Digest = ""
	return communication.ConfigHash(value)
}

func (s Store) LoadProjectWatchReconcileState(projectID string) (ProjectWatchReconcileState, error) {
	var state ProjectWatchReconcileState
	if ids.Validate(ids.ProjectPrefix, projectID) != nil {
		return state, errors.New("invalid project watch state identity")
	}
	file, err := os.Open(filepath.Join(s.DataDir, "watched-roots", "projects", projectID+".json"))
	if err != nil {
		return state, err
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, ProjectWatchReconcileStateMaxBytes+1))
	if err != nil {
		return state, err
	}
	if len(raw) > ProjectWatchReconcileStateMaxBytes || communication.DecodeStrictJSONObject(raw, &state) != nil {
		return state, errors.New("invalid project watch state encoding")
	}
	if err = validateProjectWatchState(state); err != nil {
		return state, err
	}
	if state.ProjectID != projectID {
		return state, errors.New("project watch state identity conflict")
	}
	return state, nil
}
func (s Store) SaveProjectWatchReconcileState(state ProjectWatchReconcileState) error {
	if state.SchemaVersion == "" {
		state.SchemaVersion = ProjectWatchReconcileStateSchemaVersion
	}
	state.RootKeys = append([]string{}, state.RootKeys...)
	sort.Strings(state.RootKeys)
	if err := validateProjectWatchState(state); err != nil {
		return err
	}
	// Count the actual on-disk indented representation, including Encoder's
	// trailing newline, before creating a directory or temporary file.
	raw, err := json.MarshalIndent(state, "", "  ")
	if err != nil || len(raw)+1 > ProjectWatchReconcileStateMaxBytes {
		return errors.New("project watch state exceeds byte bound")
	}
	return writeJSONFile(filepath.Join(s.DataDir, "watched-roots", "projects", state.ProjectID+".json"), state, 0600)
}
func validateProjectWatchState(state ProjectWatchReconcileState) error {
	if ids.Validate(ids.JobPrefix, state.OperationID) != nil || !validProjectWatchDigest(state.TokenHash) || state.SchemaVersion != ProjectWatchReconcileStateSchemaVersion || ids.Validate(ids.ProjectPrefix, state.ProjectID) != nil || ids.Validate(ids.NodePrefix, state.NodeID) != nil || !filepath.IsAbs(state.ProjectRoot) || filepath.Clean(state.ProjectRoot) != state.ProjectRoot || state.SafeRootKey == "" || state.AppliedRevision < 0 || state.PendingRevision < 0 || state.AppliedRevision == 0 && state.PendingRevision == 0 || len(state.RootKeys) > 1000 {
		return errors.New("invalid project watch reconcile state")
	}
	if state.AppliedRevision > 0 && !validProjectWatchDigest(state.ConfigHash) || state.PendingRevision > 0 && (!validProjectWatchDigest(state.PendingHash) || state.PendingRevision < state.AppliedRevision) {
		return errors.New("invalid project watch revision evidence")
	}
	seen := map[string]bool{}
	for _, key := range state.RootKeys {
		if key == "" || normalizeRootKey(key) != key || seen[key] {
			return errors.New("invalid project watch root identities")
		}
		seen[key] = true
	}
	if p := state.Legacy; p != nil {
		hash, err := ProjectWatchLegacyObservationHash(*p)
		if err != nil || hash != p.Digest || !validProjectWatchDigest(p.PredecessorDigest) || p.OperationID != state.OperationID || p.TokenHash != state.TokenHash || p.Revision <= 0 || (state.PendingRevision != p.Revision || state.PendingHash != p.GroupHash) && (state.AppliedRevision != p.Revision || state.ConfigHash != p.GroupHash) || len(p.Members) != len(state.RootKeys) || p.SafeRoot.RootKey != "project" || p.SuccessorSafeRoot.RootKey != state.SafeRootKey {
			return errors.New("invalid project watch local predecessor")
		}
		for i, m := range p.Members {
			if m.RootKey != state.RootKeys[i] || m.Predecessor.WorkerKey != noderuntime.WatchedRootWorkerKey(m.RootKey) || m.Successor.WorkerKey != m.Predecessor.WorkerKey || m.Predecessor.Kind != noderuntime.KindWatchedRoot || m.Successor.Kind != noderuntime.KindWatchedRoot || m.Predecessor.CreatedAt.IsZero() || !m.Successor.CreatedAt.Equal(m.Predecessor.CreatedAt) {
				return errors.New("invalid local predecessor member")
			}
		}
	}
	return nil
}
func validProjectWatchDigest(value string) bool {
	if len(value) != 71 || !strings.HasPrefix(value, "sha256:") {
		return false
	}
	_, err := hex.DecodeString(value[7:])
	return err == nil
}
