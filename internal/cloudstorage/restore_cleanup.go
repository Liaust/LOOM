package cloudstorage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/sys/unix"
	"loom.local/loom/internal/backupstrategy"
	loomerrors "loom.local/loom/internal/errors"
	"loom.local/loom/internal/restoreauthority"
)

const (
	RestoreCleanupSchema        = "loom.cloud_restore_cleanup.v1"
	RestoreCleanupPlanFile      = "restore-cleanup-plan.json"
	RestoreCleanupReceiptFile   = "restore-cleanup-receipt.jsonl"
	RestoreCleanupPreflightFile = "restore-cleanup-preflight.json"
	RestoreCleanupLegacyFile    = "restore-legacy-failure.json"
	restoreAttemptLockFile      = ".restore-attempt.lock"
	restoreAttemptLockBytes     = "loom.cloud_restore_attempt_lock.v1\n"
	cleanupMaxEntries           = 100000
	cleanupMaxMetadata          = 64 << 20
	cleanupMaxBytes             = 1 << 40
	cleanupMaxDepth             = 128
	cleanupCustodyProtocol      = "private_quarantine_seal.v1"
	cleanupQuarantineName       = ".restore-cleanup-private"
	cleanupMaxJournal           = 128 << 20
	cleanupHardlinkWorkLimit    = 2_000_000
)

// Selectors only: the service supplies Config, including StateDir and authority.
type RestoreCleanupPlanInput struct {
	Attempt string `json:"attempt"`
}

type RestoreCleanupApplyInput struct {
	Attempt       string `json:"attempt"`
	ConfirmDigest string `json:"confirm_digest"`
	DryRun        bool   `json:"dry_run"`
	Yes           bool   `json:"yes"`
}

// Full payload names and configured paths are kept only in the private plan.
type RestoreCleanupResult struct {
	Schema           string `json:"schema"`
	Status           string `json:"status"`
	Attempt          string `json:"attempt"`
	PlanDigest       string `json:"plan_digest"`
	Entries          int    `json:"entries"`
	LogicalBytes     int64  `json:"logical_bytes"`
	ConfirmedRemoved int    `json:"confirmed_removed"`
	FailureSHA256    string `json:"failure_sha256"`
	PreflightSHA256  string `json:"preflight_sha256"`
	Ref              string `json:"ref"`
	Archive          string `json:"archive"`
	ManifestSHA256   string `json:"manifest_sha256"`
	Legacy           bool   `json:"legacy"`
	DryRun           bool   `json:"dry_run"`
}

// These are operator observations, never inferred from payload verification,
// elapsed time, process names, or fetch's "not_started" fields. SourceSHA256
// references the separately reviewed evidence of the authorized external check.
type RestoreCleanupObservation struct {
	Status       string    `json:"status"`
	ObservedAt   time.Time `json:"observed_at"`
	SourceSHA256 string    `json:"source_sha256"`
}

type RestoreCleanupPreflight struct {
	Schema            string                    `json:"schema"`
	Attempt           string                    `json:"attempt"`
	Device            uint64                    `json:"device"`
	Inode             uint64                    `json:"inode"`
	OperationalTarget string                    `json:"operational_target"`
	ProvenanceTarget  string                    `json:"provenance_target"`
	Inactive          RestoreCleanupObservation `json:"inactive"`
	DatabaseAbsence   RestoreCleanupObservation `json:"database_absence"`
	HealthQuiet       RestoreCleanupObservation `json:"health_quiet"`
}

// Legacy adoption is an explicit operator-authored failure record, not a v2
// fetch diagnostic. Installing it and preflight evidence is integrator-owned.
type RestoreCleanupLegacyEvidence struct {
	Schema            string `json:"schema"`
	Status            string `json:"status"`
	Diagnostic        string `json:"diagnostic"`
	Attempt           string `json:"attempt"`
	Ref               string `json:"ref"`
	Archive           string `json:"archive"`
	ManifestSHA256    string `json:"manifest_sha256"`
	OperationalTarget string `json:"operational_target"`
	ProvenanceTarget  string `json:"provenance_target"`
}

type cleanupIdentity struct {
	Device uint64 `json:"device"`
	Inode  uint64 `json:"inode"`
	Mode   uint32 `json:"mode"`
	UID    uint32 `json:"uid"`
	GID    uint32 `json:"gid"`
	Links  uint64 `json:"links"`
	Size   int64  `json:"size"`
	MTime  int64  `json:"mtime_ns"`
	CTime  int64  `json:"ctime_ns"`
	Mount  string `json:"mount"`
}
type cleanupACLState struct {
	Access  []byte `json:"access,omitempty"`
	Default []byte `json:"default,omitempty"`
}
type cleanupEntry struct {
	ACL      *cleanupACLState `json:"acl,omitempty"`
	Path     string           `json:"path"`
	Identity cleanupIdentity  `json:"identity"`
	Link     string           `json:"link,omitempty"`
}
type cleanupBlob struct {
	Name     string          `json:"name"`
	Identity cleanupIdentity `json:"identity"`
	Bytes    []byte          `json:"bytes"`
	SHA256   string          `json:"sha256"`
}
type cleanupPlan struct {
	Protocol       string         `json:"protocol"`
	QuarantineName string         `json:"quarantine_name"`
	Schema         string         `json:"schema"`
	Root           string         `json:"root"`
	Attempt        string         `json:"attempt"`
	Ancestors      []cleanupEntry `json:"ancestors"`
	Failure        cleanupBlob    `json:"failure"`
	Preflight      cleanupBlob    `json:"preflight"`
	Lifecycle      *cleanupBlob   `json:"lifecycle,omitempty"`
	Ref            string         `json:"ref"`
	Archive        string         `json:"archive"`
	ManifestSHA256 string         `json:"manifest_sha256"`
	Legacy         bool           `json:"legacy"`
	Entries        []cleanupEntry `json:"entries"`
	LogicalBytes   int64          `json:"logical_bytes"`
}
type cleanupJournal struct {
	Quarantine       *cleanupEntry `json:"quarantine,omitempty"`
	Entry            *cleanupEntry `json:"entry,omitempty"`
	After            *cleanupEntry `json:"after,omitempty"`
	Schema           string        `json:"schema"`
	PlanDigest       string        `json:"plan_digest"`
	Status           string        `json:"status"`
	Index            int           `json:"index"`
	ConfirmedRemoved int           `json:"confirmed_removed"`
	Failure          *cleanupBlob  `json:"failure,omitempty"`
	Preflight        *cleanupBlob  `json:"preflight,omitempty"`
}
type cleanupHeld struct {
	fd    int
	entry cleanupEntry
}
type cleanupSession struct {
	quarantine        *cleanupHeld
	moved             bool
	quarantineRemoved bool
	anchors           []cleanupHeld
	lockFD            int
	lifecycle         *cleanupBlob
	root              string
	attempt           string
}
type cleanupHooks struct{ beforeMutation func(string, int) error }

func cleanupDigest(raw []byte) string { sum := sha256.Sum256(raw); return hex.EncodeToString(sum[:]) }
func cleanupJSON(v any) []byte        { raw, _ := json.Marshal(v); return append(raw, '\n') }
func cleanupDecode(raw []byte, v any) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return err
	}
	if d.Decode(new(any)) != io.EOF {
		return fmt.Errorf("trailing JSON")
	}
	return nil
}
func cleanupPublicError(err error) error {
	if err == nil {
		return nil
	}
	return loomerrors.New("cloud.restore_cleanup_refused", "cloud", "restore-cleanup", "Cleanup refused: confirmation, evidence, lifecycle, inventory or directory custody is not exact. Preserve the attempt and review its private evidence.")
}

func PlanRestoreCleanup(ctx context.Context, cfg Config, attempt string) (RestoreCleanupResult, error) {
	result, err := planRestoreCleanup(ctx, cfg, attempt)
	return result, cleanupPublicError(err)
}
func planRestoreCleanup(ctx context.Context, cfg Config, attempt string) (RestoreCleanupResult, error) {
	s, err := openCleanupSession(cfg, attempt)
	if err != nil {
		return RestoreCleanupResult{}, err
	}
	defer s.close()
	plan, err := s.buildPlan(ctx)
	if err != nil {
		return RestoreCleanupResult{}, err
	}
	return cleanupSummary(plan, "planned"), nil
}
func ApplyRestoreCleanup(ctx context.Context, cfg Config, in RestoreCleanupApplyInput) (RestoreCleanupResult, error) {
	result, err := applyRestoreCleanup(ctx, cfg, in, cleanupHooks{})
	return result, cleanupPublicError(err)
}
func cleanupSummary(p cleanupPlan, status string) RestoreCleanupResult {
	return RestoreCleanupResult{Schema: RestoreCleanupSchema, Status: status, Attempt: p.Attempt, PlanDigest: cleanupDigest(cleanupJSON(p)), Entries: len(p.Entries), LogicalBytes: p.LogicalBytes, FailureSHA256: p.Failure.SHA256, PreflightSHA256: p.Preflight.SHA256, Ref: p.Ref, Archive: p.Archive, ManifestSHA256: p.ManifestSHA256, Legacy: p.Legacy}
}
func cleanupStat(fd int) (cleanupIdentity, error) {
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return cleanupIdentity{}, err
	}
	mount, err := cleanupMountID(fd)
	if err != nil {
		return cleanupIdentity{}, err
	}
	return cleanupIdentity{uint64(st.Dev), uint64(st.Ino), uint32(st.Mode), st.Uid, st.Gid, uint64(st.Nlink), st.Size, st.Mtim.Nano(), st.Ctim.Nano(), mount}, nil
}
func cleanupStable(i cleanupIdentity) cleanupIdentity {
	i.Size = 0
	i.Links = 0
	i.MTime = 0
	i.CTime = 0
	return i
}
func cleanupDirectory(fd int, owned bool) (cleanupIdentity, error) {
	i, err := cleanupStat(fd)
	if err != nil {
		return i, err
	}
	uid := uint32(os.Geteuid())
	if i.Mode&unix.S_IFMT != unix.S_IFDIR || (i.UID != uid && (owned || i.UID != 0)) {
		return i, fmt.Errorf("unsafe directory authority")
	}
	if err := cleanupDirectoryACL(fd, false); err != nil {
		return i, err
	}
	return i, nil
}
func cleanupLeafAuthority(fd int, i cleanupIdentity) error {
	switch i.Mode & unix.S_IFMT {
	case unix.S_IFDIR:
		_, err := cleanupDirectory(fd, true)
		return err
	case unix.S_IFREG:
		if i.UID != uint32(os.Geteuid()) || i.Links == 0 || i.Size < 0 {
			return fmt.Errorf("unsafe regular payload authority")
		}
		return cleanupPayloadACL(fd)
	case unix.S_IFLNK:
		if i.UID != uint32(os.Geteuid()) {
			return fmt.Errorf("unsafe symlink authority")
		}
		// Linux symlinks have no access ACL; their mode is ignored. O_PATH
		// pins the link itself but cannot be used with fgetxattr.
		if runtime.GOOS == "linux" {
			return nil
		}
	default:
		return fmt.Errorf("unsupported payload type")
	}
	return cleanupDirectoryACL(fd, false)
}
func cleanupOpenDir(parent int, name string) (int, error) {
	return unix.Openat(parent, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
}
func (s *cleanupSession) fd() int { return s.anchors[len(s.anchors)-1].fd }
func (s *cleanupSession) close() {
	if s.quarantine != nil {
		_ = unix.Close(s.quarantine.fd)
	}
	if s.lockFD >= 0 {
		_ = unix.Close(s.lockFD)
	}
	for i := len(s.anchors) - 1; i >= 0; i-- {
		_ = unix.Close(s.anchors[i].fd)
	}
}
func openCleanupSession(cfg Config, attempt string) (_ *cleanupSession, err error) {
	if len(attempt) == 0 || len(attempt) > 255 || attempt == "." || attempt == ".." || !restoreFailureTokenPattern.MatchString(attempt) || strings.ContainsAny(attempt, "/\\*?[]") {
		return nil, fmt.Errorf("invalid attempt selector")
	}
	cfg, err = NormalizeConfig(cfg)
	if err != nil {
		return nil, err
	}
	root := cfg.StateDir
	if !filepath.IsAbs(root) || root == "/" {
		return nil, fmt.Errorf("invalid configured root")
	}
	s := &cleanupSession{lockFD: -1, root: root, attempt: attempt}
	defer func() {
		if err != nil {
			s.close()
		}
	}()
	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	s.anchors = append(s.anchors, cleanupHeld{fd: fd, entry: cleanupEntry{Path: "/"}})
	i, err := cleanupDirectory(fd, false)
	if err != nil {
		return nil, err
	}
	s.anchors[0].entry.Identity = cleanupStable(i)
	components := strings.Split(strings.TrimPrefix(filepath.Join(root, "restore-drills", attempt), "/"), "/")
	if len(components) > cleanupMaxDepth {
		return nil, fmt.Errorf("root depth exceeded")
	}
	for n, name := range components {
		next, e := cleanupOpenDir(fd, name)
		if e != nil {
			return nil, e
		}
		s.anchors = append(s.anchors, cleanupHeld{fd: next, entry: cleanupEntry{Path: name}})
		i, e = cleanupDirectory(next, n >= len(components)-2)
		if e != nil {
			return nil, e
		}
		if n >= len(components)-2 && (i.Device != s.anchors[len(s.anchors)-2].entry.Identity.Device || i.Mount != s.anchors[len(s.anchors)-2].entry.Identity.Mount) {
			return nil, fmt.Errorf("restore root or attempt is a mount boundary")
		}
		s.anchors[len(s.anchors)-1].entry.Identity = cleanupStable(i)
		fd = next
	}
	// New fetches acquire this file lock before any archive operation. Merely
	// opening/flocking existing files is read-only, including plan and dry-run.
	blob, lock, e := cleanupReadBlob(fd, restoreAttemptLockFile, 4096)
	if e == nil {
		s.lockFD = lock
		s.lifecycle = &blob
		if string(blob.Bytes) != restoreAttemptLockBytes {
			return nil, fmt.Errorf("invalid lifecycle")
		}
		if e = unix.Flock(lock, unix.LOCK_EX|unix.LOCK_NB); e != nil {
			return nil, fmt.Errorf("active attempt")
		}
	} else if !errors.Is(e, unix.ENOENT) {
		return nil, e
	}
	if e = unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); e != nil {
		return nil, fmt.Errorf("attempt evidence busy")
	}
	if e = s.revalidate(); e != nil {
		return nil, e
	}
	return s, nil
}
func (s *cleanupSession) revalidate() error {
	for n, held := range s.anchors {
		i, err := cleanupDirectory(held.fd, n >= len(s.anchors)-2)
		if err != nil || cleanupStable(i) != held.entry.Identity {
			return fmt.Errorf("held ancestor changed")
		}
		if n > 0 {
			if err = cleanupNamed(s.anchors[n-1].fd, held.entry.Path, i); err != nil {
				return err
			}
		}
	}
	if s.lifecycle != nil {
		return cleanupCheckBlob(s.fd(), s.lockFD, *s.lifecycle)
	}
	var st unix.Stat_t
	if err := unix.Fstatat(s.fd(), restoreAttemptLockFile, &st, unix.AT_SYMLINK_NOFOLLOW); !errors.Is(err, unix.ENOENT) {
		return fmt.Errorf("legacy lifecycle changed")
	}
	return nil
}
func cleanupNamed(parent int, name string, want cleanupIdentity) error {
	var st unix.Stat_t
	if err := unix.Fstatat(parent, name, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return err
	}
	if uint64(st.Dev) != want.Device || uint64(st.Ino) != want.Inode || uint32(st.Mode) != want.Mode || st.Uid != want.UID || st.Gid != want.GID || (want.Links != 0 && uint64(st.Nlink) != want.Links) || (want.CTime != 0 && (st.Ctim.Nano() != want.CTime || st.Mtim.Nano() != want.MTime || st.Size != want.Size)) {
		return fmt.Errorf("named identity changed")
	}
	return nil
}
func cleanupReadBlob(parent int, name string, limit int64) (cleanupBlob, int, error) {
	return cleanupReadEvidence(parent, name, limit, true)
}
func cleanupReadPayloadEvidence(parent int, name string, limit int64) (cleanupBlob, int, error) {
	return cleanupReadEvidence(parent, name, limit, false)
}
func cleanupEvidenceAuthority(fd int, i cleanupIdentity, private bool) error {
	if i.Mode&unix.S_IFMT != unix.S_IFREG || i.UID != uint32(os.Geteuid()) || i.Links != 1 || i.Size < 0 || (private && i.Mode&07777 != 0600) {
		return fmt.Errorf("unsafe evidence authority")
	}
	return cleanupDirectoryACL(fd, false)
}
func cleanupReadEvidence(parent int, name string, limit int64, private bool) (_ cleanupBlob, fd int, err error) {
	fd, err = unix.Openat(parent, name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return cleanupBlob{}, -1, err
	}
	defer func() {
		if err != nil {
			_ = unix.Close(fd)
		}
	}()
	i, err := cleanupStat(fd)
	if err != nil {
		return cleanupBlob{}, fd, err
	}
	if cleanupEvidenceAuthority(fd, i, private) != nil || i.Size <= 0 || i.Size > limit {
		return cleanupBlob{}, fd, fmt.Errorf("unsafe control evidence")
	}
	raw := make([]byte, i.Size)
	n, err := unix.Pread(fd, raw, 0)
	if err != nil || n != len(raw) {
		return cleanupBlob{}, fd, fmt.Errorf("control read incomplete")
	}
	blob := cleanupBlob{Name: name, Identity: i, Bytes: raw, SHA256: cleanupDigest(raw)}
	if err = cleanupCheckEvidence(parent, fd, blob, private); err != nil {
		return cleanupBlob{}, fd, err
	}
	return blob, fd, nil
}
func cleanupCheckBlob(parent, fd int, b cleanupBlob) error {
	return cleanupCheckEvidence(parent, fd, b, true)
}
func cleanupCheckEvidence(parent, fd int, b cleanupBlob, private bool) error {
	i, err := cleanupStat(fd)
	if err != nil || i != b.Identity {
		return fmt.Errorf("control evidence changed")
	}
	if err := cleanupEvidenceAuthority(fd, i, private); err != nil {
		return err
	}
	return cleanupNamed(parent, b.Name, i)
}

func (s *cleanupSession) evidence(p *cleanupPlan) error {
	failure, fd, err := cleanupReadBlob(s.fd(), RestoreFailureReceiptFile, maxRestoreFailureReceiptBytes)
	if err == nil {
		defer unix.Close(fd)
		var r RestoreFailureReceipt
		if err = cleanupDecode(failure.Bytes, &r); err != nil {
			return err
		}
		if err = validateRestoreFailureReceipt(r); err != nil {
			return err
		}
		if r.Schema != RestoreFetchFailureReceiptSchema || r.FetchFailure == nil || s.lifecycle == nil {
			return fmt.Errorf("not a lifecycle-bound fetch failure")
		}
		p.Ref, p.Archive, p.ManifestSHA256 = r.Ref, r.Archive, r.DirectArchiveManifestSHA256
	} else if errors.Is(err, unix.ENOENT) {
		failure, fd, err = cleanupReadBlob(s.fd(), RestoreCleanupLegacyFile, 16<<10)
		if err != nil {
			return err
		}
		defer unix.Close(fd)
		var r RestoreCleanupLegacyEvidence
		if err = cleanupDecode(failure.Bytes, &r); err != nil {
			return err
		}
		if r.Schema != "loom.cloud_restore_legacy_failure.v1" || r.Status != "failed" || r.Diagnostic != "diagnostic_unavailable" || r.Attempt != s.attempt || !cleanupToken(r.Ref) || !cleanupToken(r.Archive) || !restoreFailureDigestPattern.MatchString(r.ManifestSHA256) {
			return fmt.Errorf("legacy failure adoption invalid")
		}
		p.Ref, p.Archive, p.ManifestSHA256, p.Legacy = r.Ref, r.Archive, r.ManifestSHA256, true
	} else {
		return err
	}
	p.Failure = failure
	preflight, pfd, err := cleanupReadBlob(s.fd(), RestoreCleanupPreflightFile, 16<<10)
	if err != nil {
		return err
	}
	defer unix.Close(pfd)
	var pre RestoreCleanupPreflight
	if err = cleanupDecode(preflight.Bytes, &pre); err != nil {
		return err
	}
	identity := s.anchors[len(s.anchors)-1].entry.Identity
	if pre.Schema != "loom.cloud_restore_cleanup_preflight.v1" || pre.Attempt != s.attempt || pre.Device != identity.Device || pre.Inode != identity.Inode {
		return fmt.Errorf("preflight identity invalid")
	}
	if err = restoreauthority.ValidateDisposableDatabase(restoreauthority.KindOperational, pre.OperationalTarget); err != nil {
		return err
	}
	if err = restoreauthority.ValidateDisposableDatabase(restoreauthority.KindProvenance, pre.ProvenanceTarget); err != nil {
		return err
	}
	for _, item := range []struct {
		o      RestoreCleanupObservation
		status string
	}{{pre.Inactive, "inactive"}, {pre.DatabaseAbsence, "both_absent"}, {pre.HealthQuiet, "healthy_no_restore_borg_backup"}} {
		if item.o.Status != item.status || item.o.ObservedAt.IsZero() || item.o.ObservedAt.Location() != time.UTC || !restoreFailureDigestPattern.MatchString(item.o.SourceSHA256) {
			return fmt.Errorf("preflight observation invalid")
		}
	}
	if pre.Inactive.SourceSHA256 == pre.DatabaseAbsence.SourceSHA256 {
		return fmt.Errorf("independent observations required")
	}
	if p.Legacy {
		var r RestoreCleanupLegacyEvidence
		_ = cleanupDecode(failure.Bytes, &r)
		if pre.OperationalTarget != r.OperationalTarget || pre.ProvenanceTarget != r.ProvenanceTarget {
			return fmt.Errorf("legacy target mismatch")
		}
	} else {
		var r RestoreFailureReceipt
		_ = cleanupDecode(failure.Bytes, &r)
		if pre.OperationalTarget != r.FetchFailure.OperationalTarget || pre.ProvenanceTarget != r.FetchFailure.ProvenanceTarget {
			return fmt.Errorf("failure target mismatch")
		}
		var st unix.Stat_t
		if e := unix.Fstatat(s.fd(), RestoreCleanupLegacyFile, &st, unix.AT_SYMLINK_NOFOLLOW); !errors.Is(e, unix.ENOENT) {
			return fmt.Errorf("ambiguous failure evidence")
		}
	}
	p.Preflight = preflight
	return nil
}
func cleanupToken(s string) bool {
	return len(s) > 0 && len(s) <= 255 && restoreFailureTokenPattern.MatchString(s)
}

func (s *cleanupSession) buildPlan(ctx context.Context) (cleanupPlan, error) {
	p := cleanupPlan{Protocol: cleanupCustodyProtocol, QuarantineName: cleanupQuarantineName, Schema: RestoreCleanupSchema, Root: s.root, Attempt: s.attempt, Lifecycle: s.lifecycle}
	for _, a := range s.anchors {
		p.Ancestors = append(p.Ancestors, a.entry)
	}
	if err := s.evidence(&p); err != nil {
		return p, err
	}
	var pre RestoreCleanupPreflight
	if err := cleanupDecode(p.Preflight.Bytes, &pre); err != nil {
		return p, err
	}
	now := time.Now()
	for _, observation := range []RestoreCleanupObservation{pre.Inactive, pre.DatabaseAbsence, pre.HealthQuiet} {
		if observation.ObservedAt.After(now) || now.Sub(observation.ObservedAt) > 15*time.Minute {
			return p, fmt.Errorf("preflight observation is stale or future dated")
		}
	}
	for _, name := range []string{RestoreCleanupPlanFile, RestoreCleanupReceiptFile, cleanupQuarantineName} {
		var st unix.Stat_t
		if err := unix.Fstatat(s.fd(), name, &st, unix.AT_SYMLINK_NOFOLLOW); !errors.Is(err, unix.ENOENT) {
			return p, fmt.Errorf("prior cleanup evidence requires exact replay")
		}
	}
	entries, total, err := s.inventory(ctx)
	if err != nil {
		return p, err
	}
	p.Entries, p.LogicalBytes = entries, total
	if p.ManifestSHA256 != "" {
		if err = s.checkManifest(p); err != nil {
			return p, err
		}
	}
	journalBound := 256 << 10
	for _, entry := range p.Entries {
		journalBound += 512
		if entry.Identity.Mode&unix.S_IFMT == unix.S_IFREG && entry.Identity.Links > 1 {
			journalBound += 2*len(cleanupJSON(entry)) + 512
		}
		if entry.Path == "backup" {
			journalBound += 3 * len(cleanupJSON(entry))
		}
		if entry.Identity.Mode&unix.S_IFMT == unix.S_IFDIR {
			journalBound += 4*len(cleanupJSON(entry)) + 1024
		}
	}
	if journalBound > cleanupMaxJournal {
		return p, fmt.Errorf("planned journal bound exceeded")
	}
	if len(cleanupJSON(p)) > cleanupMaxMetadata {
		return p, fmt.Errorf("plan metadata bound exceeded")
	}
	if err = s.revalidate(); err != nil {
		return p, err
	}
	return p, nil
}

func (s *cleanupSession) inventory(ctx context.Context) ([]cleanupEntry, int64, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	var entries []cleanupEntry
	var total int64
	metadata := 0
	device := s.anchors[len(s.anchors)-1].entry.Identity
	var walk func(int, string, string, int) error
	walk = func(parent int, name, path string, depth int) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if depth > cleanupMaxDepth || len(path) > 4096 || !utf8.ValidString(path) || len(entries) >= cleanupMaxEntries {
			return fmt.Errorf("inventory bound exceeded")
		}
		var named unix.Stat_t
		if err := unix.Fstatat(parent, name, &named, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			return err
		}
		typ := uint32(named.Mode) & unix.S_IFMT
		if typ != unix.S_IFDIR && typ != unix.S_IFREG && typ != unix.S_IFLNK {
			return fmt.Errorf("special payload entry")
		}
		if path == "backup" && typ != unix.S_IFDIR {
			return fmt.Errorf("payload is not a directory")
		}
		fd, err := unix.Openat(parent, name, cleanupLeafFlags(uint32(named.Mode)), 0)
		if err != nil {
			return err
		}
		defer unix.Close(fd)
		i, err := cleanupStat(fd)
		if err != nil {
			return err
		}
		if i.Device != device.Device || i.Mount != device.Mount || i.Inode != uint64(named.Ino) || i.Mode != uint32(named.Mode) {
			return fmt.Errorf("payload replacement or mount boundary")
		}
		entry := cleanupEntry{Path: path, Identity: i}
		if typ == unix.S_IFDIR {
			got, acl, e := cleanupPayloadDirectory(fd, path == "backup")
			if e != nil {
				return e
			}
			if got != i {
				return fmt.Errorf("directory changed")
			}
			entry.ACL = &acl
		} else if err = cleanupLeafAuthority(fd, i); err != nil {
			return err
		}
		if typ == unix.S_IFLNK {
			var b [4097]byte
			n, e := unix.Readlinkat(parent, name, b[:])
			if e != nil || n >= len(b)-1 || !utf8.Valid(b[:n]) {
				return fmt.Errorf("unsupported symlink")
			}
			entry.Link = string(b[:n])
		}
		metadata += len(cleanupJSON(entry))
		if metadata > cleanupMaxMetadata {
			return fmt.Errorf("metadata bound exceeded")
		}
		entries = append(entries, entry)
		if typ == unix.S_IFREG {
			if i.Size > cleanupMaxBytes-total {
				return fmt.Errorf("logical byte bound exceeded")
			}
			total += i.Size
		}
		if typ == unix.S_IFDIR {
			children, e := cleanupNames(fd, cleanupMaxEntries-len(entries))
			if e != nil {
				return e
			}
			for _, child := range children {
				if e = walk(fd, child, path+"/"+child, depth+1); e != nil {
					return e
				}
			}
		}
		after, e := cleanupStat(fd)
		if e != nil || after != i {
			return fmt.Errorf("inventory changed while reading")
		}
		return cleanupNamed(parent, name, i)
	}
	if err := walk(s.payloadParent(), "backup", "backup", 0); err != nil {
		return nil, 0, err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	groups, err := cleanupHardlinkGroups(entries)
	if err != nil {
		return nil, 0, err
	}
	if len(groups) > 0 {
		expected := map[string]cleanupEntry{}
		for _, entry := range entries {
			expected[entry.Path] = entry
		}
		for _, paths := range groups {
			if err := s.checkHardlinkGroup(ctx, paths, expected); err != nil {
				return nil, 0, err
			}
		}
	}
	return entries, total, nil
}
func cleanupNames(fd, limit int) ([]string, error) {
	copy, err := cleanupOpenDir(fd, ".")
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(copy), "cleanup-directory")
	defer f.Close()
	var names []string
	for {
		chunk, e := f.Readdirnames(256)
		names = append(names, chunk...)
		if len(names) > limit {
			return nil, fmt.Errorf("directory inventory bound exceeded")
		}
		if e == io.EOF {
			break
		}
		if e != nil {
			return nil, e
		}
	}
	sort.Strings(names)
	return names, nil
}
func (s *cleanupSession) checkManifest(p cleanupPlan) error {
	fd, err := cleanupOpenDir(s.payloadParent(), "backup")
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	evidence, err := cleanupOpenDir(fd, backupstrategy.DirectArchiveEvidenceDir)
	if err != nil {
		return err
	}
	defer unix.Close(evidence)
	blob, mfd, err := cleanupReadPayloadEvidence(evidence, backupstrategy.DirectArchiveManifestFile, 8<<20)
	if err != nil {
		return err
	}
	defer unix.Close(mfd)
	if blob.SHA256 != p.ManifestSHA256 {
		return fmt.Errorf("retained manifest mismatch")
	}
	for _, entry := range p.Entries {
		if entry.Path == "backup/"+backupstrategy.DirectArchiveEvidenceDir+"/"+backupstrategy.DirectArchiveManifestFile && entry.Identity == blob.Identity {
			return nil
		}
	}
	return fmt.Errorf("retained manifest inventory identity changed")
}

func applyRestoreCleanup(ctx context.Context, cfg Config, in RestoreCleanupApplyInput, hooks cleanupHooks) (result RestoreCleanupResult, err error) {
	if !restoreFailureDigestPattern.MatchString(in.ConfirmDigest) || (!in.DryRun && !in.Yes) {
		return result, fmt.Errorf("exact digest and explicit confirmation required")
	}
	s, err := openCleanupSession(cfg, in.Attempt)
	if err != nil {
		return result, err
	}
	defer s.close()
	stored, storedFD, e := cleanupReadBlob(s.fd(), RestoreCleanupPlanFile, cleanupMaxMetadata)
	if e == nil {
		defer unix.Close(storedFD)
		return s.replay(in, stored)
	} else if !errors.Is(e, unix.ENOENT) {
		return result, e
	}
	p, err := s.buildPlan(ctx)
	if err != nil {
		return result, err
	}
	result = cleanupSummary(p, "planned")
	result.DryRun = in.DryRun
	if result.PlanDigest != in.ConfirmDigest {
		return result, fmt.Errorf("reviewed plan stale")
	}
	// Reopen and retain evidence descriptors for the entire apply. The plan's
	// exact bytes plus inode, metadata and namespace binding must still match.
	var controls []cleanupHeldBlob
	defer func() {
		for _, c := range controls {
			_ = unix.Close(c.fd)
		}
	}()
	for _, b := range []cleanupBlob{p.Failure, p.Preflight} {
		current, fd, e := cleanupReadBlob(s.fd(), b.Name, 16<<10)
		if e != nil {
			return result, e
		}
		controls = append(controls, cleanupHeldBlob{fd, current})
		if !bytes.Equal(cleanupJSON(b), cleanupJSON(current)) {
			return result, fmt.Errorf("reviewed evidence stale")
		}
	}
	barrier := func(point string, index int) error {
		if hooks.beforeMutation != nil {
			if e := hooks.beforeMutation(point, index); e != nil {
				return e
			}
		}
		if e := ctx.Err(); e != nil {
			return e
		}
		if e := s.revalidate(); e != nil {
			return e
		}
		if e := s.checkQuarantine(); e != nil {
			return e
		}
		if s.moved {
			if e := cleanupAbsent(s.fd(), "backup"); e != nil {
				return e
			}
		}
		if s.quarantineRemoved {
			if e := cleanupAbsent(s.fd(), cleanupQuarantineName); e != nil {
				return e
			}
		}
		for _, c := range controls {
			if e := cleanupCheckBlob(s.fd(), c.fd, c.blob); e != nil {
				return e
			}
		}
		return nil
	}
	entries, total, err := s.inventory(ctx)
	if err != nil {
		return result, err
	}
	if total != p.LogicalBytes || !bytes.Equal(cleanupJSON(entries), cleanupJSON(p.Entries)) {
		return result, fmt.Errorf("inventory stale")
	}
	if err = barrier("validated", -1); err != nil {
		return result, err
	}
	if in.DryRun {
		return result, nil
	}
	if err = barrier("plan", -1); err != nil {
		return result, err
	}
	planControl, err := s.publishControl(RestoreCleanupPlanFile, cleanupJSON(p))
	if err != nil {
		return result, err
	}
	controls = append(controls, planControl)
	if err = barrier("receipt", -1); err != nil {
		return result, err
	}
	initial := cleanupJournal{Schema: RestoreCleanupSchema, PlanDigest: result.PlanDigest, Status: "prepared", Index: -1, Failure: &p.Failure, Preflight: &p.Preflight}
	journal, err := s.publishControl(RestoreCleanupReceiptFile, cleanupJSON(initial))
	if err != nil {
		return result, err
	}
	controls = append(controls, journal)
	journalIndex := len(controls) - 1
	var verifyRemoval func() error
	appendRecord := func(event cleanupJournal) error {
		event.Schema = RestoreCleanupSchema
		event.PlanDigest = result.PlanDigest
		if e := barrier(event.Status, event.Index); e != nil {
			return e
		}
		if event.Status == "removed" && verifyRemoval != nil {
			if e := verifyRemoval(); e != nil {
				return e
			}
		}
		if event.Status == "payload_removed" {
			if e := cleanupAbsent(s.payloadParent(), "backup"); e != nil {
				return e
			}
			if names, e := cleanupNames(s.payloadParent(), 0); e != nil || len(names) != 0 {
				return fmt.Errorf("payload absence not exact")
			}
		}
		c := &controls[journalIndex]
		raw := cleanupJSON(event)
		if c.blob.Identity.Size+int64(len(raw)) > cleanupMaxJournal {
			return fmt.Errorf("journal bound exceeded")
		}
		if e := cleanupWrite(c.fd, raw); e != nil {
			return e
		}
		if e := unix.Fsync(c.fd); e != nil {
			return e
		}
		i, e := cleanupStat(c.fd)
		if e != nil {
			return e
		}
		// The held append-only journal may grow only by this exact record.
		if cleanupStable(i) != cleanupStable(c.blob.Identity) || i.Links != 1 || i.Size != c.blob.Identity.Size+int64(len(raw)) {
			return fmt.Errorf("journal changed")
		}
		c.blob.Identity = i
		return cleanupCheckBlob(s.fd(), c.fd, c.blob)
	}
	appendEvent := func(status string, index, confirmed int) error {
		return appendRecord(cleanupJournal{Status: status, Index: index, ConfirmedRemoved: confirmed})
	}
	result.Status = "partial"
	defer func() {
		if err != nil {
			_ = appendEvent("partial", -1, result.ConfirmedRemoved)
		}
	}()
	// Removal order is deterministic, deepest first. Each directory is empty
	// before rmdir. A failed/crashed run is retained for review, never resumed
	// by guessing which absent entry this operation may have removed.
	order := cleanupRemovalOrder(p.Entries)
	groups, err := cleanupHardlinkGroups(p.Entries)
	if err != nil {
		return result, err
	}
	expected := map[string]cleanupEntry{}
	for _, entry := range p.Entries {
		expected[entry.Path] = entry
	}
	if err = s.quarantineAndSeal(ctx, p, expected, barrier, appendRecord); err != nil {
		return result, err
	}
	// The sealed-record barrier itself may reveal new activity after the full
	// sealed inventory. Recheck every group before deleting even an unrelated
	// singleton, then check its remaining membership again at each alias unlink.
	for _, paths := range groups {
		if err = s.checkHardlinkGroup(ctx, paths, expected); err != nil {
			return result, err
		}
	}
	for index, entry := range order {
		if err = appendEvent("pending", index, result.ConfirmedRemoved); err != nil {
			return result, err
		}
		err = func() error {
			target, e := s.openTarget(entry.Path, expected)
			if e != nil {
				return e
			}
			defer target.close()
			paths, multiple := groups[cleanupHardlinkID(entry.Identity)]
			if entry.Identity.Mode&unix.S_IFMT != unix.S_IFREG {
				multiple = false
			}
			if e = barrier("unlink", index); e != nil {
				return e
			}
			if multiple {
				if e = s.checkHardlinkGroup(ctx, paths, expected); e != nil {
					return e
				}
			}
			if e = target.revalidate(); e != nil {
				return e
			}
			flags := 0
			if entry.Identity.Mode&unix.S_IFMT == unix.S_IFDIR {
				flags = unix.AT_REMOVEDIR
				names, e := cleanupNames(target.leaf.fd, 0)
				if e != nil || len(names) != 0 {
					return fmt.Errorf("directory not empty")
				}
			}
			if e = target.revalidate(); e != nil {
				return e
			}
			before := target.leaf.entry
			// All cooperating writers retain the lifecycle/custody model.
			// Name-based unlink is not atomic with the expected-inode check.
			if e = unix.Unlinkat(target.parent, filepath.Base(entry.Path), flags); e != nil {
				return e
			}
			if e = unix.Fsync(target.parent); e != nil {
				return e
			}
			parentPath := filepath.Dir(entry.Path)
			if parentPath != "." {
				i, acl, e := cleanupPayloadDirectory(target.parent, parentPath == "backup")
				if e != nil || cleanupStable(i) != cleanupStable(expected[parentPath].Identity) {
					return fmt.Errorf("parent identity changed")
				}
				updated := expected[parentPath]
				updated.Identity = i
				if !bytes.Equal(cleanupJSON(updated.ACL), cleanupJSON(&acl)) {
					return fmt.Errorf("parent ACL changed")
				}
				expected[parentPath] = updated
			}
			event := cleanupJournal{Status: "removed", Index: index, ConfirmedRemoved: result.ConfirmedRemoved + 1}
			if multiple {
				if e = barrier("hardlink_unlinked", index); e != nil {
					return e
				}
				got, e := cleanupStat(target.leaf.fd)
				if e != nil {
					return e
				}
				after := before
				after.Identity = got
				if !cleanupHardlinkTransition(before, after) {
					return fmt.Errorf("hardlink inode changed after unlink")
				}
				delete(expected, entry.Path)
				for _, path := range paths {
					if survivor, ok := expected[path]; ok {
						survivor.Identity = got
						expected[path] = survivor
					}
				}
				verifyRemoval = func() error { return s.checkHardlinkRemoval(ctx, target, after, paths, expected) }
				if e = verifyRemoval(); e != nil {
					return e
				}
				event.Entry = &before
				event.After = &after
			}
			if e = appendRecord(event); e != nil {
				return e
			}
			result.ConfirmedRemoved++
			delete(expected, entry.Path)
			return nil
		}()
		verifyRemoval = nil
		if err != nil {
			return result, err
		}
	}
	var st unix.Stat_t
	if e = unix.Fstatat(s.payloadParent(), "backup", &st, unix.AT_SYMLINK_NOFOLLOW); !errors.Is(e, unix.ENOENT) {
		return result, fmt.Errorf("payload still present")
	}
	if err = appendEvent("payload_removed", -1, result.ConfirmedRemoved); err != nil {
		return result, err
	}
	if err = s.removeQuarantine(result.ConfirmedRemoved, barrier, appendRecord); err != nil {
		return result, err
	}
	if err = appendEvent("completed", -1, result.ConfirmedRemoved); err != nil {
		return result, err
	}
	result.Status = "completed"
	return result, nil
}

type cleanupHeldBlob struct {
	fd   int
	blob cleanupBlob
}

func cleanupWrite(fd int, raw []byte) error {
	for len(raw) > 0 {
		n, e := unix.Write(fd, raw)
		if errors.Is(e, unix.EINTR) {
			continue
		}
		if e != nil {
			return e
		}
		if n <= 0 {
			return io.ErrShortWrite
		}
		raw = raw[n:]
	}
	return nil
}
func (s *cleanupSession) publishControl(name string, raw []byte) (_ cleanupHeldBlob, err error) {
	fd, err := unix.Openat(s.fd(), name, unix.O_RDWR|unix.O_APPEND|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if err != nil {
		return cleanupHeldBlob{}, err
	}
	defer func() {
		if err != nil {
			_ = unix.Close(fd)
		}
	}()
	// Failed/partial control files are deliberately retained. No scratch-file
	// unlink can accidentally remove a substituted control entry.
	if err = unix.Fchmod(fd, 0600); err != nil {
		return cleanupHeldBlob{}, err
	}
	if err = cleanupWrite(fd, raw); err != nil {
		return cleanupHeldBlob{}, err
	}
	if err = unix.Fsync(fd); err != nil {
		return cleanupHeldBlob{}, err
	}
	if err = unix.Fsync(s.fd()); err != nil {
		return cleanupHeldBlob{}, err
	}
	i, err := cleanupStat(fd)
	if err != nil {
		return cleanupHeldBlob{}, err
	}
	b := cleanupBlob{Name: name, Identity: i, Bytes: raw, SHA256: cleanupDigest(raw)}
	if err = cleanupCheckBlob(s.fd(), fd, b); err != nil {
		return cleanupHeldBlob{}, err
	}
	return cleanupHeldBlob{fd, b}, nil
}

type cleanupTarget struct {
	dirs       []cleanupHeld
	leaf       cleanupHeld
	parent     int
	rootParent int
}

func (t *cleanupTarget) close() {
	_ = unix.Close(t.leaf.fd)
	for i := len(t.dirs) - 1; i >= 0; i-- {
		_ = unix.Close(t.dirs[i].fd)
	}
}
func (s *cleanupSession) openTarget(path string, expected map[string]cleanupEntry) (_ *cleanupTarget, err error) {
	t := &cleanupTarget{parent: s.payloadParent(), rootParent: s.payloadParent(), leaf: cleanupHeld{fd: -1}}
	defer func() {
		if err != nil {
			t.close()
		}
	}()
	parts := strings.Split(path, "/")
	prefix := ""
	for n, name := range parts {
		if prefix != "" {
			prefix += "/"
		}
		prefix += name
		want, ok := expected[prefix]
		if !ok {
			return nil, fmt.Errorf("unreviewed entry")
		}
		fd, e := unix.Openat(t.parent, name, cleanupLeafFlags(want.Identity.Mode), 0)
		if e != nil {
			return nil, e
		}
		got, e := cleanupStat(fd)
		if e != nil || got != want.Identity {
			_ = unix.Close(fd)
			return nil, fmt.Errorf("stale payload entry")
		}
		if e = cleanupNamed(t.parent, name, got); e != nil {
			_ = unix.Close(fd)
			return nil, e
		}
		if n == len(parts)-1 {
			t.leaf = cleanupHeld{fd, want}
			break
		}
		if _, _, e = cleanupPayloadDirectory(fd, prefix == "backup"); e != nil {
			_ = unix.Close(fd)
			return nil, e
		}
		t.dirs = append(t.dirs, cleanupHeld{fd, want})
		t.parent = fd
	}
	return t, nil
}
func (t *cleanupTarget) revalidate() error {
	for _, d := range t.dirs {
		i, acl, e := cleanupPayloadDirectory(d.fd, d.entry.Path == "backup")
		if e != nil || i != d.entry.Identity || !bytes.Equal(cleanupJSON(d.entry.ACL), cleanupJSON(&acl)) {
			return fmt.Errorf("payload ancestor changed")
		}
	}
	// Reopen each parent name from its already-held parent. The session's
	// full external ancestor chain is separately checked at the same barrier.
	for n, d := range t.dirs {
		parent := t.rootParent
		if n > 0 {
			parent = t.dirs[n-1].fd
		}
		if e := cleanupNamed(parent, filepath.Base(d.entry.Path), d.entry.Identity); e != nil {
			return e
		}
	}
	i, e := cleanupStat(t.leaf.fd)
	if e != nil || i != t.leaf.entry.Identity {
		return fmt.Errorf("held payload changed")
	}
	if i.Mode&unix.S_IFMT == unix.S_IFDIR {
		_, acl, err := cleanupPayloadDirectory(t.leaf.fd, t.leaf.entry.Path == "backup")
		if err != nil || !bytes.Equal(cleanupJSON(&acl), cleanupJSON(t.leaf.entry.ACL)) {
			return fmt.Errorf("directory ACL changed")
		}
	} else if e = cleanupLeafAuthority(t.leaf.fd, i); e != nil {
		return e
	}
	return cleanupNamed(t.parent, filepath.Base(t.leaf.entry.Path), i)
}
func (s *cleanupSession) replay(in RestoreCleanupApplyInput, stored cleanupBlob) (RestoreCleanupResult, error) {
	var p cleanupPlan
	if cleanupDecode(stored.Bytes, &p) != nil || p.Schema != RestoreCleanupSchema || p.Protocol != cleanupCustodyProtocol || p.QuarantineName != cleanupQuarantineName || p.Root != s.root || p.Attempt != s.attempt || cleanupDigest(stored.Bytes) != in.ConfirmDigest || !bytes.Equal(cleanupJSON(p), stored.Bytes) || len(p.Entries) == 0 || len(p.Entries) > cleanupMaxEntries {
		return RestoreCleanupResult{}, fmt.Errorf("invalid retained plan")
	}
	groups, groupErr := cleanupHardlinkGroups(p.Entries)
	if groupErr != nil {
		return RestoreCleanupResult{}, groupErr
	}
	order := cleanupRemovalOrder(p.Entries)
	result := cleanupSummary(p, "partial")
	result.DryRun = in.DryRun
	var current cleanupPlan
	if err := s.evidence(&current); err != nil {
		return result, err
	}
	if !bytes.Equal(cleanupJSON(p.Failure), cleanupJSON(current.Failure)) || !bytes.Equal(cleanupJSON(p.Preflight), cleanupJSON(current.Preflight)) || !bytes.Equal(cleanupJSON(p.Lifecycle), cleanupJSON(s.lifecycle)) {
		return result, fmt.Errorf("original evidence changed")
	}
	var anchors []cleanupEntry
	for _, a := range s.anchors {
		anchors = append(anchors, a.entry)
	}
	if !bytes.Equal(cleanupJSON(p.Ancestors), cleanupJSON(anchors)) {
		return result, fmt.Errorf("original custody changed")
	}
	journal, fd, err := cleanupReadBlob(s.fd(), RestoreCleanupReceiptFile, cleanupMaxJournal)
	if err != nil {
		return result, err
	}
	defer unix.Close(fd)
	lines := bytes.Split(journal.Bytes, []byte{'\n'})
	if len(lines) < 3 || len(lines[len(lines)-1]) != 0 {
		return result, fmt.Errorf("interrupted receipt")
	}
	state := ""
	confirmed, sealIndex := 0, 0
	dirs := cleanupSealOrder(p.Entries)
	expected := map[string]cleanupEntry{}
	for _, entry := range p.Entries {
		expected[entry.Path] = entry
	}
	originalBackup := expected["backup"].Identity
	var quarantine, removalQuarantine *cleanupEntry
	validQuarantine := func(q *cleanupEntry) bool {
		if q == nil || q.Path != cleanupQuarantineName || q.ACL != nil {
			return false
		}
		i := q.Identity
		backup := originalBackup
		return i.Mode == unix.S_IFDIR|0700 && i.UID == uint32(os.Geteuid()) && i.Device == backup.Device && i.Mount == backup.Mount && i.Inode != 0
	}
	same := func(a, b any) bool { return bytes.Equal(cleanupJSON(a), cleanupJSON(b)) }
	for index, line := range lines[:len(lines)-1] {
		var event cleanupJournal
		if cleanupDecode(line, &event) != nil || event.Schema != RestoreCleanupSchema || event.PlanDigest != in.ConfirmDigest {
			return result, fmt.Errorf("invalid receipt binding")
		}
		if index == 0 {
			if event.Quarantine != nil || event.Entry != nil || event.After != nil || event.Status != "prepared" || event.Index != -1 || event.ConfirmedRemoved != 0 || event.Failure == nil || event.Preflight == nil || !bytes.Equal(cleanupJSON(*event.Failure), cleanupJSON(p.Failure)) || !bytes.Equal(cleanupJSON(*event.Preflight), cleanupJSON(p.Preflight)) {
				return result, fmt.Errorf("invalid preserved failure evidence")
			}
		} else {
			if event.Failure != nil || event.Preflight != nil {
				return result, fmt.Errorf("repeated receipt header")
			}
			switch event.Status {
			case "quarantine_pending":
				if state != "prepared" || event.Index != -1 || event.ConfirmedRemoved != 0 || event.Quarantine != nil || event.Entry != nil || event.After != nil {
					return result, fmt.Errorf("invalid quarantine intent")
				}
			case "quarantine_created":
				if state != "quarantine_pending" || event.Index != -1 || event.ConfirmedRemoved != 0 || !validQuarantine(event.Quarantine) || event.Entry != nil || event.After != nil {
					return result, fmt.Errorf("invalid quarantine identity")
				}
				quarantine = event.Quarantine
			case "move_pending":
				if state != "quarantine_created" || event.Index != -1 || event.ConfirmedRemoved != 0 || !same(event.Quarantine, quarantine) || !same(event.Entry, expected["backup"]) || event.After != nil {
					return result, fmt.Errorf("invalid move intent")
				}
			case "moved":
				if state != "move_pending" || event.Index != -1 || event.ConfirmedRemoved != 0 || !same(event.Quarantine, quarantine) || !same(event.Entry, expected["backup"]) || event.After == nil || !cleanupRenameIdentity(expected["backup"], *event.After) {
					return result, fmt.Errorf("invalid moved acknowledgement")
				}
				expected["backup"] = *event.After
			case "seal_pending":
				if (state != "moved" && state != "seal_applied") || sealIndex >= len(dirs) || event.Index != sealIndex || event.ConfirmedRemoved != 0 || event.Quarantine != nil {
					return result, fmt.Errorf("invalid seal progress")
				}
				original := expected[dirs[sealIndex].Path]
				if !same(event.Entry, original) || !same(event.After, cleanupSealTarget(original)) {
					return result, fmt.Errorf("invalid seal intent")
				}
			case "seal_applied":
				if state != "seal_pending" || sealIndex >= len(dirs) || event.Index != sealIndex || event.ConfirmedRemoved != 0 || event.Quarantine != nil {
					return result, fmt.Errorf("invalid seal acknowledgement")
				}
				original := expected[dirs[sealIndex].Path]
				if !same(event.Entry, original) || event.After == nil || !cleanupSealIdentity(original, *event.After) {
					return result, fmt.Errorf("invalid sealed metadata")
				}
				expected[dirs[sealIndex].Path] = *event.After
				sealIndex++
			case "sealed":
				if state != "seal_applied" || sealIndex != len(dirs) || event.Index != -1 || event.ConfirmedRemoved != 0 || event.Quarantine != nil || event.Entry != nil || event.After != nil {
					return result, fmt.Errorf("incomplete seal inventory")
				}
			case "pending":
				if (state != "sealed" && state != "removed") || event.Index != confirmed || event.ConfirmedRemoved != confirmed || event.Quarantine != nil || event.Entry != nil || event.After != nil {
					return result, fmt.Errorf("invalid pending progress")
				}
			case "removed":
				if state != "pending" || event.Index != confirmed || confirmed >= len(order) || event.ConfirmedRemoved != confirmed+1 || event.Quarantine != nil {
					return result, fmt.Errorf("invalid removal progress")
				}
				path := order[confirmed].Path
				original := expected[path]
				paths, multiple := groups[cleanupHardlinkID(original.Identity)]
				if original.Identity.Mode&unix.S_IFMT != unix.S_IFREG {
					multiple = false
				}
				if multiple {
					if event.Entry == nil || event.After == nil || !same(event.Entry, original) || !cleanupHardlinkTransition(original, *event.After) {
						return result, fmt.Errorf("invalid hardlink transition acknowledgement")
					}
					delete(expected, path)
					remaining := 0
					for _, member := range paths {
						if survivor, ok := expected[member]; ok {
							survivor.Identity = event.After.Identity
							expected[member] = survivor
							remaining++
						}
					}
					if event.After.Identity.Links != uint64(remaining) {
						return result, fmt.Errorf("invalid hardlink remaining count")
					}
				} else if event.Entry != nil || event.After != nil {
					return result, fmt.Errorf("unexpected singleton transition")
				}
				delete(expected, path)
				confirmed++
				result.ConfirmedRemoved = confirmed
			case "payload_removed":
				if state != "removed" || confirmed != len(p.Entries) || event.Index != -1 || event.ConfirmedRemoved != confirmed || event.Quarantine != nil || event.Entry != nil || event.After != nil {
					return result, fmt.Errorf("invalid payload completion")
				}
			case "quarantine_remove_pending":
				if state != "payload_removed" || event.Index != -1 || event.ConfirmedRemoved != confirmed || !validQuarantine(event.Quarantine) || cleanupStable(event.Quarantine.Identity) != cleanupStable(quarantine.Identity) || event.Entry != nil || event.After != nil {
					return result, fmt.Errorf("invalid quarantine removal intent")
				}
				removalQuarantine = event.Quarantine
			case "quarantine_removed":
				if state != "quarantine_remove_pending" || event.Index != -1 || event.ConfirmedRemoved != confirmed || !same(event.Quarantine, removalQuarantine) || event.Entry != nil || event.After != nil {
					return result, fmt.Errorf("invalid quarantine removal acknowledgement")
				}
			case "completed":
				if state != "quarantine_removed" || confirmed != len(p.Entries) || event.Index != -1 || event.ConfirmedRemoved != confirmed || index != len(lines)-2 || event.Quarantine != nil || event.Entry != nil || event.After != nil {
					return result, fmt.Errorf("invalid completion")
				}
			default:
				return result, fmt.Errorf("partial cleanup requires operator review")
			}
		}
		state = event.Status
	}
	result.ConfirmedRemoved = confirmed
	if state != "completed" {
		return result, fmt.Errorf("interrupted cleanup requires operator review")
	}
	var st unix.Stat_t
	if err = unix.Fstatat(s.fd(), "backup", &st, unix.AT_SYMLINK_NOFOLLOW); !errors.Is(err, unix.ENOENT) {
		return result, fmt.Errorf("payload recreated")
	}
	if err = cleanupAbsent(s.fd(), cleanupQuarantineName); err != nil {
		return result, err
	}
	if err = s.revalidate(); err != nil {
		return result, err
	}
	result.Status = "completed"
	return result, nil
}

// The lifecycle lock is independent of the existing receipt-publication lock
// on the attempt directory, avoiding recursive flock deadlock during failure
// publication. A crash releases it; absence of a failure/adoption record still
// prevents cleanup of an incomplete attempt.
type restoreAttemptLifecycle struct {
	path            string
	fd              int
	identity        cleanupIdentity
	attemptIdentity cleanupIdentity
}

func (l *restoreAttemptLifecycle) close() { _ = unix.Close(l.fd); l.fd = -1 }
func (l *restoreAttemptLifecycle) authorize(target string) error {
	if l.fd < 0 || target != filepath.Join(l.path, "backup") {
		return fmt.Errorf("restore fetch custody mismatch")
	}
	dir, err := openRestoreFailureDirectory(l.path)
	if err != nil {
		return err
	}
	defer unix.Close(dir)
	i, err := cleanupStat(dir)
	if err != nil || cleanupStable(i) != cleanupStable(l.attemptIdentity) {
		return fmt.Errorf("restore attempt replaced")
	}
	return cleanupCheckBlob(dir, l.fd, cleanupBlob{Name: restoreAttemptLockFile, Identity: l.identity})
}

// Public fetch destinations cannot enter marked restore attempts, even through
// an alias or a different caller configuration. An unmarked directory named
// restore-drills remains an ordinary destination, including manual fetches.
// Only RestoreDrill owns the private live capability for its exact backup.
type restoreFetchDestinationCustody struct {
	anchors   []cleanupHeld
	lifecycle *restoreAttemptLifecycle
	target    string
}

func (c *restoreFetchDestinationCustody) close() {
	for n := len(c.anchors) - 1; n >= 0; n-- {
		_ = unix.Close(c.anchors[n].fd)
	}
}
func (c *restoreFetchDestinationCustody) revalidate() error {
	if c.lifecycle != nil {
		return c.lifecycle.authorize(c.target)
	}
	for n, held := range c.anchors {
		got, err := cleanupFetchDirectory(held.fd, n < len(c.anchors)-1)
		if err != nil || cleanupStable(got) != held.entry.Identity {
			return fmt.Errorf("fetch destination custody changed")
		}
		if n > 0 {
			if err := cleanupNamed(c.anchors[n-1].fd, held.entry.Path, held.entry.Identity); err != nil {
				return err
			}
		}
		for _, marker := range []string{restoreAttemptLockFile, RestoreFailureReceiptFile, RestoreCleanupLegacyFile, RestoreCleanupPreflightFile, RestoreCleanupPlanFile, RestoreCleanupReceiptFile} {
			var st unix.Stat_t
			if e := unix.Fstatat(held.fd, marker, &st, unix.AT_SYMLINK_NOFOLLOW); !errors.Is(e, unix.ENOENT) {
				return fmt.Errorf("marked restore attempt requires held lifecycle authority")
			}
		}
	}
	return nil
}
func cleanupFetchDirectory(fd int, hasTrustedChild bool) (cleanupIdentity, error) {
	i, err := cleanupStat(fd)
	if err != nil {
		return i, err
	}
	if i.Mode&unix.S_IFMT != unix.S_IFDIR || (i.UID != 0 && i.UID != uint32(os.Geteuid())) {
		return i, fmt.Errorf("unsafe fetch ancestor owner")
	}
	// A root-owned sticky shared ancestor protects its already-opened trusted
	// child. Never allow a writable final ancestor: an attacker could precreate
	// a missing descendant there after validation.
	stickyAncestor := hasTrustedChild && i.UID == 0 && i.Mode&unix.S_ISVTX != 0
	if err := cleanupDirectoryACL(fd, stickyAncestor); err != nil {
		return i, err
	}
	return i, nil
}
func guardRestoreFetchDestination(input SnapshotFetchInput) error {
	custody, err := beginRestoreFetchDestination(&input)
	if err == nil {
		custody.close()
	}
	return err
}
func beginRestoreFetchDestination(input *SnapshotFetchInput) (_ *restoreFetchDestinationCustody, err error) {
	if strings.TrimSpace(input.To) == "" {
		return nil, fmt.Errorf("target directory is required")
	}
	target, err := filepath.Abs(strings.TrimSpace(input.To))
	if err != nil {
		return nil, fmt.Errorf("unsafe fetch destination")
	}
	if input.restoreLifecycle != nil {
		if err := input.restoreLifecycle.authorize(target); err != nil {
			return nil, err
		}
		input.To = target
		return &restoreFetchDestinationCustody{lifecycle: input.restoreLifecycle, target: target}, nil
	}
	existing := target
	for {
		_, err = os.Lstat(existing)
		if err == nil {
			break
		}
		if !errors.Is(err, os.ErrNotExist) || filepath.Dir(existing) == existing {
			return nil, fmt.Errorf("cannot establish fetch destination custody")
		}
		existing = filepath.Dir(existing)
	}
	resolved, err := filepath.EvalSymlinks(existing)
	if err != nil {
		return nil, fmt.Errorf("unresolved fetch destination")
	}
	suffix, err := filepath.Rel(existing, target)
	if err != nil {
		return nil, err
	}
	c := &restoreFetchDestinationCustody{target: filepath.Join(resolved, suffix)}
	defer func() {
		if err != nil {
			c.close()
		}
	}()
	fd, err := cleanupOpenDir(unix.AT_FDCWD, "/")
	if err != nil {
		return nil, err
	}
	c.anchors = append(c.anchors, cleanupHeld{fd: fd, entry: cleanupEntry{Path: "/"}})
	if resolved != "/" {
		parts := strings.Split(strings.TrimPrefix(resolved, "/"), "/")
		if len(parts) > cleanupMaxDepth {
			return nil, fmt.Errorf("fetch ancestor bound exceeded")
		}
		for _, name := range parts {
			child, e := cleanupOpenDir(fd, name)
			if e != nil {
				return nil, e
			}
			c.anchors = append(c.anchors, cleanupHeld{fd: child, entry: cleanupEntry{Path: name}})
			fd = child
		}
	}
	for n := range c.anchors {
		i, e := cleanupFetchDirectory(c.anchors[n].fd, n < len(c.anchors)-1)
		if e != nil {
			return nil, e
		}
		c.anchors[n].entry.Identity = cleanupStable(i)
	}
	if err := c.revalidate(); err != nil {
		return nil, err
	}
	// All backend filesystem and subprocess operations consume this canonical
	// path, never the original outside alias. Held safe ancestors remain live
	// until the complete backend call returns.
	input.To = c.target
	return c, nil
}

func startRestoreAttemptLifecycle(path string, expected unix.Stat_t) (*restoreAttemptLifecycle, error) {
	dir, err := openRestoreFailureDirectory(path)
	if err != nil {
		return nil, err
	}
	defer unix.Close(dir)
	var st unix.Stat_t
	if unix.Fstat(dir, &st) != nil || st.Dev != expected.Dev || st.Ino != expected.Ino {
		return nil, fmt.Errorf("attempt replaced")
	}
	fd, err := unix.Openat(dir, restoreAttemptLockFile, unix.O_RDWR|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if err != nil {
		return nil, err
	}
	fail := func(e error) (*restoreAttemptLifecycle, error) { _ = unix.Close(fd); return nil, e }
	if err = unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		return fail(err)
	}
	if err = cleanupWrite(fd, []byte(restoreAttemptLockBytes)); err != nil {
		return fail(err)
	}
	if err = unix.Fsync(fd); err != nil {
		return fail(err)
	}
	if err = unix.Fsync(dir); err != nil {
		return fail(err)
	}
	if err = revalidateRestoreFailureDirectory(path, dir); err != nil {
		return fail(err)
	}
	identity, err := cleanupStat(fd)
	if err != nil {
		return fail(err)
	}
	attemptIdentity, err := cleanupStat(dir)
	if err != nil {
		return fail(err)
	}
	return &restoreAttemptLifecycle{path: path, fd: fd, identity: identity, attemptIdentity: attemptIdentity}, nil
}

// Linux POSIX ACL xattrs have a version header and ordered 8-byte entries.
// Keep the pure production decoder portable so every supported test host can
// exercise malformed input and effective-mask rules; descriptor reads stay in
// the Linux platform owner. IDs are numeric, never resolved through NSS.
type cleanupPOSIXAuthority struct {
	mode          uint32
	nonOwnerWrite bool
}

func cleanupParsePOSIXACL(raw []byte, owner uint32, defaults bool) (cleanupPOSIXAuthority, error) {
	var result cleanupPOSIXAuthority
	invalid := func() (cleanupPOSIXAuthority, error) { return result, fmt.Errorf("invalid POSIX ACL") }
	if len(raw) < 4 || len(raw) > 65536 || (len(raw)-4)%8 != 0 || binary.LittleEndian.Uint32(raw) != 2 {
		return invalid()
	}
	if len(raw) == 4 && defaults {
		return result, nil
	}
	const undefined = uint32(0xffffffff)
	stage := 0
	var lastUser, lastGroup uint32
	haveUser, haveGroup, haveMask := false, false, false
	var userObj, groupObj, other, mask, nonOwner uint16
	for offset := 4; offset < len(raw); offset += 8 {
		tag := binary.LittleEndian.Uint16(raw[offset:])
		perm := binary.LittleEndian.Uint16(raw[offset+2:])
		id := binary.LittleEndian.Uint32(raw[offset+4:])
		if perm&^uint16(7) != 0 || (tag != 2 && tag != 8 && id != undefined) || ((tag == 2 || tag == 8) && id == undefined) {
			return invalid()
		}
		switch tag {
		case 1:
			if stage != 0 {
				return invalid()
			}
			userObj = perm
			stage = 1
		case 2:
			if stage != 1 || (haveUser && id <= lastUser) {
				return invalid()
			}
			haveUser = true
			lastUser = id
			if defaults || id != owner {
				nonOwner |= perm
			}
		case 4:
			if stage != 1 {
				return invalid()
			}
			groupObj = perm
			nonOwner |= perm
			stage = 2
		case 8:
			if stage != 2 || (haveGroup && id <= lastGroup) {
				return invalid()
			}
			haveGroup = true
			lastGroup = id
			nonOwner |= perm
		case 16:
			if stage != 2 {
				return invalid()
			}
			haveMask = true
			mask = perm
			stage = 3
		case 32:
			if stage != 2 && stage != 3 {
				return invalid()
			}
			other = perm
			stage = 4
		default:
			return invalid()
		}
	}
	if stage != 4 || ((haveUser || haveGroup) && !haveMask) {
		return invalid()
	}
	effectiveMask := uint16(7)
	groupMode := groupObj
	if haveMask {
		effectiveMask = mask
		groupMode = mask
	}
	result.mode = uint32(userObj)<<6 | uint32(groupMode)<<3 | uint32(other)
	result.nonOwnerWrite = (nonOwner&effectiveMask&2) != 0 || other&2 != 0
	return result, nil
}

func (s *cleanupSession) payloadParent() int {
	if s.quarantine != nil {
		return s.quarantine.fd
	}
	return s.fd()
}
func cleanupPayloadDirectory(fd int, top bool) (cleanupIdentity, cleanupACLState, error) {
	i, err := cleanupStat(fd)
	if err != nil {
		return i, cleanupACLState{}, err
	}
	if i.Mode&unix.S_IFMT != unix.S_IFDIR || i.UID != uint32(os.Geteuid()) || i.Mode&0500 != 0500 || i.Mode&07000 != 0 {
		return i, cleanupACLState{}, fmt.Errorf("unsealable directory authority")
	}
	if top {
		if _, err = cleanupDirectory(fd, true); err != nil {
			return i, cleanupACLState{}, err
		}
	}
	acl, err := cleanupReadPayloadDirACL(fd)
	after, e := cleanupStat(fd)
	if err != nil {
		return i, acl, err
	}
	if e != nil || after != i {
		return i, acl, fmt.Errorf("directory metadata changed during ACL inspection")
	}
	return i, acl, nil
}
func cleanupAbsent(fd int, name string) error {
	var st unix.Stat_t
	if e := unix.Fstatat(fd, name, &st, unix.AT_SYMLINK_NOFOLLOW); !errors.Is(e, unix.ENOENT) {
		return fmt.Errorf("unexpected existing namespace entry")
	}
	return nil
}
func (s *cleanupSession) checkQuarantine() error {
	q := s.quarantine
	if q == nil {
		return nil
	}
	i, err := cleanupDirectory(q.fd, true)
	if err != nil || cleanupStable(i) != cleanupStable(q.entry.Identity) || i.Mode&07777 != 0700 {
		return fmt.Errorf("private quarantine custody changed")
	}
	if err = cleanupPrivateDirectoryACL(q.fd); err != nil {
		return err
	}
	return cleanupNamed(s.fd(), cleanupQuarantineName, cleanupStable(q.entry.Identity))
}

func cleanupSameEntries(entries []cleanupEntry, expected map[string]cleanupEntry) bool {
	if len(entries) != len(expected) {
		return false
	}
	for _, entry := range entries {
		want, ok := expected[entry.Path]
		if !ok || !bytes.Equal(cleanupJSON(entry), cleanupJSON(want)) {
			return false
		}
	}
	return true
}
func cleanupSealOrder(entries []cleanupEntry) []cleanupEntry {
	var dirs []cleanupEntry
	for _, entry := range entries {
		if entry.Identity.Mode&unix.S_IFMT == unix.S_IFDIR {
			dirs = append(dirs, entry)
		}
	}
	sort.Slice(dirs, func(i, j int) bool {
		a, b := strings.Count(dirs[i].Path, "/"), strings.Count(dirs[j].Path, "/")
		if a != b {
			return a < b
		}
		return dirs[i].Path < dirs[j].Path
	})
	return dirs
}
func cleanupSealTarget(original cleanupEntry) cleanupEntry {
	target := original
	target.Identity.Mode = (original.Identity.Mode | 0200) &^ 0022
	target.Identity.CTime = 0
	if original.ACL != nil {
		acl := cleanupSealedACL(*original.ACL, target.Identity.Mode)
		target.ACL = &acl
	}
	return target
}
func cleanupRenameIdentity(before, after cleanupEntry) bool {
	want := before
	want.Identity.CTime = after.Identity.CTime
	return bytes.Equal(cleanupJSON(want), cleanupJSON(after))
}
func cleanupSealIdentity(before, after cleanupEntry) bool {
	want := cleanupSealTarget(before)
	want.Identity.CTime = after.Identity.CTime
	return bytes.Equal(cleanupJSON(want), cleanupJSON(after))
}
func (s *cleanupSession) quarantineAndSeal(ctx context.Context, p cleanupPlan, expected map[string]cleanupEntry, barrier func(string, int) error, record func(cleanupJournal) error) error {
	if err := record(cleanupJournal{Status: "quarantine_pending", Index: -1}); err != nil {
		return err
	}
	if err := barrier("quarantine_create", -1); err != nil {
		return err
	}
	if err := unix.Mkdirat(s.fd(), cleanupQuarantineName, 0700); err != nil {
		return err
	}
	fd, err := cleanupOpenDir(s.fd(), cleanupQuarantineName)
	if err != nil {
		return err
	}
	i, err := cleanupDirectory(fd, true)
	if err != nil {
		unix.Close(fd)
		return err
	}
	s.quarantine = &cleanupHeld{fd: fd, entry: cleanupEntry{Path: cleanupQuarantineName, Identity: i}}
	if err = s.checkQuarantine(); err != nil {
		return err
	}
	if err = unix.Fsync(s.fd()); err != nil {
		return err
	}
	if err = record(cleanupJournal{Status: "quarantine_created", Index: -1, Quarantine: &s.quarantine.entry}); err != nil {
		return err
	}
	original := expected["backup"]
	// Hold the source before relocation; no-replace only protects the destination.
	source, err := s.openSourceBackup(original)
	if err != nil {
		return err
	}
	defer unix.Close(source)
	if err = record(cleanupJournal{Status: "move_pending", Index: -1, Quarantine: &s.quarantine.entry, Entry: &original}); err != nil {
		return err
	}
	if err = barrier("quarantine_move", -1); err != nil {
		return err
	}
	current, acl, err := cleanupPayloadDirectory(source, true)
	if err != nil || current != original.Identity || !bytes.Equal(cleanupJSON(&acl), cleanupJSON(original.ACL)) {
		return fmt.Errorf("backup source changed")
	}
	if err = cleanupNamed(s.fd(), "backup", original.Identity); err != nil {
		return err
	}
	if err = cleanupRenameNoReplace(s.fd(), "backup", fd, "backup"); err != nil {
		return err
	}
	s.moved = true
	after, acl, err := cleanupPayloadDirectory(source, true)
	if err != nil {
		return err
	}
	moved := cleanupEntry{Path: "backup", Identity: after, ACL: &acl}
	if !cleanupRenameIdentity(original, moved) {
		return fmt.Errorf("unexpected backup rename metadata")
	}
	if err = cleanupNamed(fd, "backup", after); err != nil {
		return err
	}
	if err = unix.Fsync(s.fd()); err != nil {
		return err
	}
	if err = unix.Fsync(fd); err != nil {
		return err
	}
	if err = record(cleanupJournal{Status: "moved", Index: -1, Quarantine: &s.quarantine.entry, Entry: &original, After: &moved}); err != nil {
		return err
	}
	expected["backup"] = moved
	entries, total, err := s.inventory(ctx)
	if err != nil || total != p.LogicalBytes || !cleanupSameEntries(entries, expected) {
		return fmt.Errorf("post-quarantine inventory changed")
	}
	if err = s.checkManifest(cleanupPlan{Entries: entries, ManifestSHA256: p.ManifestSHA256}); err != nil {
		return err
	}
	for index, dir := range cleanupSealOrder(entries) {
		before := expected[dir.Path]
		intended := cleanupSealTarget(before)
		if err = record(cleanupJournal{Status: "seal_pending", Index: index, Entry: &before, After: &intended}); err != nil {
			return err
		}
		target, e := s.openTarget(dir.Path, expected)
		if e != nil {
			return e
		}
		err = func() error {
			defer target.close()
			if e := barrier("seal", index); e != nil {
				return e
			}
			if e := target.revalidate(); e != nil {
				return e
			}
			if intended.Identity.Mode != before.Identity.Mode {
				if e := unix.Fchmod(target.leaf.fd, intended.Identity.Mode&0777); e != nil {
					return e
				}
			}
			if e := unix.Fsync(target.leaf.fd); e != nil {
				return e
			}
			got, acl, e := cleanupPayloadDirectory(target.leaf.fd, dir.Path == "backup")
			if e != nil {
				return e
			}
			after := cleanupEntry{Path: dir.Path, Identity: got, ACL: &acl}
			if !cleanupSealIdentity(before, after) {
				return fmt.Errorf("seal result differs from reviewed transformation")
			}
			if e = cleanupNamed(target.parent, filepath.Base(dir.Path), got); e != nil {
				return e
			}
			if e = record(cleanupJournal{Status: "seal_applied", Index: index, Entry: &before, After: &after}); e != nil {
				return e
			}
			expected[dir.Path] = after
			return nil
		}()
		if err != nil {
			return err
		}
	}
	entries, total, err = s.inventory(ctx)
	if err != nil || total != p.LogicalBytes || !cleanupSameEntries(entries, expected) {
		return fmt.Errorf("sealed inventory changed")
	}
	return record(cleanupJournal{Status: "sealed", Index: -1})
}
func (s *cleanupSession) openSourceBackup(original cleanupEntry) (int, error) {
	fd, err := cleanupOpenDir(s.fd(), "backup")
	if err != nil {
		return -1, err
	}
	i, err := cleanupStat(fd)
	if err != nil || i != original.Identity {
		unix.Close(fd)
		return -1, fmt.Errorf("source backup changed")
	}
	return fd, nil
}
func (s *cleanupSession) removeQuarantine(confirmed int, barrier func(string, int) error, record func(cleanupJournal) error) error {
	if s.quarantine == nil {
		return fmt.Errorf("missing quarantine custody")
	}
	q := s.quarantine
	i, err := cleanupStat(q.fd)
	if err != nil {
		return err
	}
	entry := cleanupEntry{Path: cleanupQuarantineName, Identity: i}
	if err = record(cleanupJournal{Status: "quarantine_remove_pending", Index: -1, ConfirmedRemoved: confirmed, Quarantine: &entry}); err != nil {
		return err
	}
	if err = barrier("quarantine_remove", -1); err != nil {
		return err
	}
	names, err := cleanupNames(q.fd, 0)
	if err != nil || len(names) != 0 {
		return fmt.Errorf("quarantine not empty")
	}
	if err = cleanupNamed(s.fd(), cleanupQuarantineName, i); err != nil {
		return err
	}
	if err = unix.Unlinkat(s.fd(), cleanupQuarantineName, unix.AT_REMOVEDIR); err != nil {
		return err
	}
	if err = unix.Fsync(s.fd()); err != nil {
		return err
	}
	unix.Close(q.fd)
	s.quarantine = nil
	s.quarantineRemoved = true
	return record(cleanupJournal{Status: "quarantine_removed", Index: -1, ConfirmedRemoved: confirmed, Quarantine: &entry})
}

// Membership is indexed once; no per-unlink scan of unrelated inventory.
// Work is conservatively bounded before any control publication or quarantine.
type cleanupHardlinkKey struct {
	Device, Inode uint64
	Mount         string
}

func cleanupHardlinkID(i cleanupIdentity) cleanupHardlinkKey {
	return cleanupHardlinkKey{i.Device, i.Inode, i.Mount}
}
func cleanupHardlinkGroups(entries []cleanupEntry) (map[cleanupHardlinkKey][]string, error) {
	groups := map[cleanupHardlinkKey][]string{}
	identities := map[cleanupHardlinkKey]cleanupIdentity{}
	names := map[string]bool{}
	for _, entry := range entries {
		if entry.Identity.Mode&unix.S_IFMT != unix.S_IFREG {
			continue
		}
		i := entry.Identity
		if !strings.HasPrefix(entry.Path, "backup/") || filepath.Clean(entry.Path) != entry.Path || names[entry.Path] || i.UID != uint32(os.Geteuid()) || i.Links == 0 || i.Links > cleanupMaxEntries || i.Size < 0 || entry.ACL != nil || entry.Link != "" {
			return nil, fmt.Errorf("invalid regular inode inventory")
		}
		names[entry.Path] = true
		key := cleanupHardlinkID(i)
		if previous, ok := identities[key]; ok && previous != i {
			return nil, fmt.Errorf("inconsistent hardlink metadata")
		}
		identities[key] = i
		groups[key] = append(groups[key], entry.Path)
	}
	var work uint64
	for key, paths := range groups {
		count := uint64(len(paths))
		if count != identities[key].Links {
			return nil, fmt.Errorf("unaccounted hardlink names")
		}
		if count == 1 {
			delete(groups, key)
			continue
		}
		work += 3*count*count + 8*count
		if work > cleanupHardlinkWorkLimit {
			return nil, fmt.Errorf("hardlink revalidation work bound exceeded")
		}
		sort.Strings(paths)
	}
	return groups, nil
}
func (s *cleanupSession) checkHardlinkGroup(ctx context.Context, paths []string, expected map[string]cleanupEntry) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	remaining := 0
	var identity cleanupIdentity
	for _, path := range paths {
		if entry, ok := expected[path]; ok {
			if remaining > 0 && entry.Identity != identity {
				return fmt.Errorf("inconsistent remaining hardlink metadata")
			}
			identity = entry.Identity
			remaining++
		}
	}
	if remaining == 0 {
		return nil
	}
	if identity.Mode&unix.S_IFMT != unix.S_IFREG || identity.Links != uint64(remaining) {
		return fmt.Errorf("remaining hardlink count mismatch")
	}
	for _, path := range paths {
		if _, ok := expected[path]; !ok {
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		target, err := s.openTarget(path, expected)
		if err != nil {
			return err
		}
		err = target.revalidate()
		target.close()
		if err != nil {
			return err
		}
	}
	return nil
}
func cleanupHardlinkTransition(before, after cleanupEntry) bool {
	if before.Identity.Mode&unix.S_IFMT != unix.S_IFREG || before.Identity.Links == 0 {
		return false
	}
	want := before
	want.Identity.Links--
	want.Identity.CTime = after.Identity.CTime
	return bytes.Equal(cleanupJSON(want), cleanupJSON(after))
}
func (s *cleanupSession) checkHardlinkRemoval(ctx context.Context, target *cleanupTarget, after cleanupEntry, paths []string, expected map[string]cleanupEntry) error {
	got, err := cleanupStat(target.leaf.fd)
	if err != nil || got != after.Identity {
		return fmt.Errorf("hardlink acknowledgement inode changed")
	}
	if err = cleanupAbsent(target.parent, filepath.Base(after.Path)); err != nil {
		return err
	}
	parentPath := filepath.Dir(after.Path)
	parent, err := s.openTarget(parentPath, expected)
	if err != nil {
		return err
	}
	err = parent.revalidate()
	parent.close()
	if err != nil {
		return err
	}
	return s.checkHardlinkGroup(ctx, paths, expected)
}
func cleanupRemovalOrder(entries []cleanupEntry) []cleanupEntry {
	order := append([]cleanupEntry(nil), entries...)
	sort.Slice(order, func(i, j int) bool {
		a, b := strings.Count(order[i].Path, "/"), strings.Count(order[j].Path, "/")
		if a != b {
			return a > b
		}
		return order[i].Path < order[j].Path
	})
	return order
}
