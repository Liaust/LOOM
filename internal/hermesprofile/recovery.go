package hermesprofile

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

const WorkspaceRoot = "/srv/loom/agents/morathustra"
const Version = "0.21.0"
const Revision = "29112bef099274229cadff79cdff7bf7b99c4b77"
const Schema = "loom.hermes.recovery.v1"
const CaptureSchema = "loom.hermes.recovery.v2"
const ManifestFile = "manifest.json"
const PayloadFile = "profile.zip"
const MaxAge = 2 * time.Hour

var idPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,79}$`)

type File struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	Mode   uint32 `json:"mode"`
	SHA256 string `json:"sha256"`
}
type Source struct {
	Path           string   `json:"path"`
	Identity       Identity `json:"identity"`
	SHA256         string   `json:"sha256,omitempty"`
	OnlineDatabase bool     `json:"online_database"`
	OperationalLog bool     `json:"operational_log"`
}
type Manifest struct {
	Schema       string    `json:"schema"`
	ID           string    `json:"id"`
	Version      string    `json:"version"`
	Revision     string    `json:"revision"`
	Binary       string    `json:"binary"`
	BinarySHA256 string    `json:"binary_sha256"`
	Workspace    string    `json:"workspace"`
	Profile      string    `json:"profile"`
	SourceRoot   Identity  `json:"source_root"`
	CreatedAt    time.Time `json:"created_at"`
	Sources      []Source  `json:"sources"`
	Inventory    []File    `json:"inventory"`
	Payload      File      `json:"payload"`
	Capture      *Capture  `json:"capture,omitempty"`
}
type envelope struct {
	Manifest  json.RawMessage `json:"manifest"`
	Signature string          `json:"signature"`
}
type Evidence struct {
	ID             string    `json:"id"`
	Path           string    `json:"path"`
	ManifestSHA256 string    `json:"manifest_sha256"`
	CreatedAt      time.Time `json:"created_at"`
	Files          []File    `json:"files"`
}
type PublishInput struct {
	Identity   RecoveryIdentity
	Workspace  string
	ID         string
	CreatedAt  time.Time
	PrivateKey ed25519.PrivateKey
	Binary     string
	// Runner is the bounded native adapter. The default checks the current
	// agents identity and the exact pinned package. Tests inject a fixture.
	Runner func(context.Context, string, string, string) (string, error)
	// BeforePublish is intentionally private: fault injection cannot become a
	// runtime configuration surface.
	beforePublish     func()
	afterPublish      func()
	snapshotDatabase  databaseSnapshot
	beforeCaptureCopy func(string)
}

func Publish(ctx context.Context, in PublishInput) (Evidence, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	if !idPattern.MatchString(in.ID) || len(in.PrivateKey) != ed25519.PrivateKeySize || in.CreatedAt.IsZero() || in.CreatedAt.Location() != time.UTC {
		return Evidence{}, fmt.Errorf("invalid recovery request identity")
	}
	location, origin, err := ResolveWorkspace(in.Identity, in.Workspace)
	if err != nil {
		return Evidence{}, err
	}
	in.Workspace = location
	chain, err := openCustody(in.Workspace)
	if err != nil {
		return Evidence{}, err
	}
	defer chain.close()
	workspace := chain.leaf()
	workspaceIdentity, err := statFD(workspace)
	if err != nil {
		return Evidence{}, err
	}
	profile, err := openAt(workspace, ".hermes", true)
	if err != nil {
		return Evidence{}, err
	}
	defer profile.Close()
	rootIdentity, err := statFD(profile)
	if err != nil {
		return Evidence{}, err
	}
	recovery, err := chain.child("recovery")
	if err != nil {
		return Evidence{}, err
	}
	key := in.PrivateKey.Public().(ed25519.PublicKey)
	final := filepath.Join(in.Workspace, "recovery", in.ID)
	if _, err := statAt(recovery, in.ID); err == nil {
		evidence, m, err := verify(ctx, final, origin, key)
		if err != nil {
			return Evidence{}, err
		}
		if m.ID != in.ID || !m.CreatedAt.Equal(in.CreatedAt) || m.Binary != in.Binary || !sameObject(m.SourceRoot, rootIdentity) {
			return Evidence{}, fmt.Errorf("recovery replay identity conflict")
		}
		return evidence, chain.revalidate()
	} else if err != unix.ENOENT {
		return Evidence{}, err
	}
	if err := unix.Mkdirat(int(recovery.Fd()), ".staging", 0700); err != nil && err != unix.EEXIST {
		return Evidence{}, err
	}
	staging, err := openAt(recovery, ".staging", true)
	if err != nil {
		return Evidence{}, err
	}
	defer staging.Close()
	// Never reuse or delete interrupted work. Each attempt owns a new name;
	// complete packages are immutable and the logical ID publishes once.
	attempt := in.ID + "-" + fmt.Sprintf("%d", time.Now().UnixNano())
	if err = unix.Mkdirat(int(staging.Fd()), attempt, 0700); err != nil {
		return Evidence{}, err
	}
	dir, err := openAt(staging, attempt, true)
	if err != nil {
		return Evidence{}, err
	}
	defer dir.Close()
	attemptIdentity, _ := statFD(dir)
	stagingIdentity, _ := statFD(staging)
	recoveryIdentity, _ := statFD(recovery)
	var source *sourceTree
	captureRemoved := false
	revalidate := func() error {
		if err := chain.revalidate(); err != nil {
			return err
		}
		w, e := statFD(workspace)
		if e != nil || w != workspaceIdentity {
			return fmt.Errorf("workspace namespace changed")
		}
		r, e := statFD(recovery)
		if e != nil || r != recoveryIdentity {
			return fmt.Errorf("recovery namespace changed")
		}
		p, e := statAt(workspace, ".hermes")
		if e != nil || !sameObject(p, rootIdentity) {
			return fmt.Errorf("profile root substituted")
		}
		st, e := statAt(recovery, ".staging")
		if e != nil || st != stagingIdentity {
			return fmt.Errorf("staging root substituted")
		}
		a, e := statAt(staging, attempt)
		if e != nil || !sameObject(a, attemptIdentity) {
			return fmt.Errorf("staging package substituted")
		}
		if source != nil && !captureRemoved {
			return source.revalidate(ctx)
		}
		return ctx.Err()
	}
	if err = revalidate(); err != nil {
		return Evidence{}, err
	}
	runner := in.Runner
	if runner == nil {
		runner = func(ctx context.Context, binary, profile, output string) (string, error) {
			return RunNativeBackupForIdentity(ctx, in.Identity, binary, profile, output)
		}
	}
	payloadPath := filepath.Join(in.Workspace, "recovery", ".staging", attempt, PayloadFile)
	capturePath := filepath.Join(filepath.Dir(payloadPath), ".capture")
	snapshot := in.snapshotDatabase
	if snapshot == nil {
		snapshot = func(ctx context.Context, parent, file, target *os.File, rel string) error {
			return snapshotNativeDatabase(ctx, in.Binary, parent, file, target,
				filepath.Join(in.Workspace, ".hermes", rel), filepath.Join(capturePath, rel))
		}
	}
	capture, err := newProfileCapture(dir, snapshot)
	if err != nil {
		return Evidence{}, err
	}
	defer capture.close()
	capture.beforeCopy = in.beforeCaptureCopy
	if err = capture.walk(ctx, profile, capture.root, workspace, ".hermes", "", rootIdentity); err != nil {
		return Evidence{}, err
	}
	if err = capture.prepareCommand(); err != nil {
		return Evidence{}, err
	}
	source, err = scanCapturedSource(ctx, capture.root)
	if err != nil {
		return Evidence{}, err
	}
	defer source.close()
	if err = revalidate(); err != nil {
		return Evidence{}, err
	}
	binarySHA, err := runner(ctx, in.Binary, capturePath, payloadPath)
	if err != nil {
		return Evidence{}, fmt.Errorf("native Hermes recovery failed")
	}
	if err = revalidate(); err != nil {
		return Evidence{}, err
	}
	f, err := openAt(dir, PayloadFile, false)
	if err != nil {
		return Evidence{}, err
	}
	defer f.Close()
	if err = f.Chmod(0440); err != nil {
		return Evidence{}, err
	}
	if err = f.Sync(); err != nil {
		return Evidence{}, err
	}
	inventory, err := inspectZIP(ctx, f)
	if err != nil {
		return Evidence{}, err
	}
	sources := make([]Source, 0, len(source.files))
	for name, s := range source.files {
		sources = append(sources, Source{Path: name, Identity: s.initial, SHA256: s.hash, OnlineDatabase: s.db, OperationalLog: s.log})
	}
	sort.Slice(sources, func(i, j int) bool { return sources[i].Path < sources[j].Path })
	if err = matchInventory(inventory, sources, true); err != nil {
		return Evidence{}, err
	}
	digest, size, err := hashFile(ctx, f)
	if err != nil {
		return Evidence{}, err
	}
	m := Manifest{Schema: CaptureSchema, ID: in.ID, Version: Version, Revision: Revision, Binary: in.Binary, BinarySHA256: binarySHA, Workspace: origin, Profile: filepath.Join(origin, ".hermes"), SourceRoot: rootIdentity, CreatedAt: in.CreatedAt, Sources: sources, Inventory: inventory, Payload: File{PayloadFile, size, 0440, digest}, Capture: &capture.receipt}
	if !validCapture(m) {
		return Evidence{}, fmt.Errorf("invalid Hermes capture receipt")
	}
	raw, err := json.Marshal(m)
	if err != nil {
		return Evidence{}, err
	}
	signed, err := json.Marshal(envelope{raw, hex.EncodeToString(ed25519.Sign(in.PrivateKey, raw))})
	if err != nil {
		return Evidence{}, err
	}
	if err = writeNew(dir, ManifestFile, signed, 0440); err != nil {
		return Evidence{}, err
	}
	if err = dir.Sync(); err != nil {
		return Evidence{}, err
	}
	if in.beforePublish != nil {
		in.beforePublish()
	}
	if err = revalidate(); err != nil {
		return Evidence{}, err
	}
	if err = capture.remove(); err != nil {
		return Evidence{}, err
	}
	captureRemoved = true
	if _, _, err = verify(ctx, filepath.Dir(payloadPath), origin, key); err != nil {
		return Evidence{}, err
	}
	if err = dir.Chmod(0750); err != nil {
		return Evidence{}, err
	}
	// Refresh only the mode changed by this operation, retaining inode identity.
	attemptIdentity.Mode = (attemptIdentity.Mode & ^uint32(07777)) | 0750
	if err = revalidate(); err != nil {
		return Evidence{}, err
	}
	if err = renameNoReplace(staging, attempt, recovery, in.ID); err != nil {
		return Evidence{}, err
	}
	if err = recovery.Sync(); err != nil {
		return Evidence{}, err
	}
	if in.afterPublish != nil {
		in.afterPublish()
	}
	if err = chain.revalidate(); err != nil {
		return Evidence{}, err
	}
	named, err := statAt(recovery, in.ID)
	if err != nil || !sameObject(named, attemptIdentity) {
		return Evidence{}, fmt.Errorf("published recovery substituted")
	}
	e, _, err := verify(ctx, final, origin, key)
	return e, err
}
func inspectZIP(ctx context.Context, f *os.File) ([]File, error) {
	info, err := statFD(f)
	if err != nil {
		return nil, err
	}
	z, err := zip.NewReader(f, info.Size)
	if err != nil {
		return nil, fmt.Errorf("invalid recovery ZIP")
	}
	if len(z.File) == 0 || len(z.File) > maxEntries {
		return nil, fmt.Errorf("invalid ZIP inventory size")
	}
	entries := make([]File, 0, len(z.File))
	seen := map[string]bool{}
	var total int64
	for _, entry := range z.File {
		name := entry.Name
		if !portable(name) || seen[name] || excluded(name) || stringsExternal(name) || !entry.Mode().IsRegular() || entry.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 || entry.UncompressedSize64 > uint64(maxBytes) {
			return nil, fmt.Errorf("unsafe recovery ZIP entry")
		}
		seen[name] = true
		r, err := entry.Open()
		if err != nil {
			return nil, err
		}
		h := sha256.New()
		n, err := io.Copy(h, io.LimitReader(&contextReader{ctx, r}, maxBytes+1))
		r.Close()
		if err != nil || n != int64(entry.UncompressedSize64) {
			return nil, fmt.Errorf("invalid recovery ZIP content")
		}
		total += n
		if total > maxBytes {
			return nil, fmt.Errorf("recovery ZIP byte limit exceeded")
		}
		entries = append(entries, File{name, n, uint32(entry.Mode().Perm()), hex.EncodeToString(h.Sum(nil))})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	return entries, nil
}
func stringsExternal(name string) bool {
	return name == "_external" || len(name) > 10 && name[:10] == "_external/"
}
func matchInventory(files []File, sources []Source, frozen bool) error {
	if len(files) != len(sources) {
		return fmt.Errorf("native backup is incomplete or contains undeclared files")
	}
	required := map[string]bool{"SOUL.md": false, "config.yaml": false, "state.db": false}
	for i, f := range files {
		if _, ok := required[f.Path]; ok {
			required[f.Path] = true
		}
		s := sources[i]
		log := strings.HasPrefix(s.Path, "logs/")
		db := strings.HasSuffix(s.Path, ".db")
		if frozen {
			log = log && !db
		} else {
			db = db && !log
		}
		if f.Path != s.Path || !regular(s.Identity) || s.OperationalLog != log || s.OnlineDatabase != db {
			return fmt.Errorf("native backup inventory mismatch")
		}
		if s.OperationalLog {
			if f.Mode != s.Identity.Mode&0777 || !frozen && s.SHA256 != "" || frozen && (f.Size != s.Identity.Size || f.SHA256 != s.SHA256) {
				return fmt.Errorf("native log snapshot mode mismatch")
			}
		} else if s.OnlineDatabase {
			if f.Size <= 0 || f.Mode != 0600 {
				return fmt.Errorf("invalid native database snapshot")
			}
		} else if f.Size != s.Identity.Size || f.Mode != s.Identity.Mode&0777 || f.SHA256 != s.SHA256 {
			return fmt.Errorf("native backup does not match source")
		}
	}
	for _, found := range required {
		if !found {
			return fmt.Errorf("native backup missing required profile entry")
		}
	}
	return nil
}
func verify(ctx context.Context, pkg, workspace string, key ed25519.PublicKey) (Evidence, Manifest, error) {
	fail := func() (Evidence, Manifest, error) {
		return Evidence{}, Manifest{}, fmt.Errorf("invalid authenticated Hermes recovery evidence")
	}
	if len(key) != ed25519.PublicKeySize {
		return fail()
	}
	chain, err := openCustody(pkg)
	if err != nil {
		return Evidence{}, Manifest{}, err
	}
	defer chain.close()
	dir := chain.leaf()
	ns, err := names(dir)
	if err != nil || len(ns) != 2 {
		return fail()
	}
	sort.Strings(ns)
	if ns[0] != ManifestFile || ns[1] != PayloadFile {
		return fail()
	}
	f, err := openAt(dir, ManifestFile, false)
	if err != nil {
		return fail()
	}
	defer f.Close()
	fi, _ := statFD(f)
	if fi.Mode&07777 != 0440 {
		return fail()
	}
	raw, err := readBounded(f, 16<<20)
	if err != nil {
		return fail()
	}
	var env envelope
	if json.Unmarshal(raw, &env) != nil {
		return fail()
	}
	canonical, _ := json.Marshal(env)
	if !bytes.Equal(raw, canonical) {
		return fail()
	}
	signature, err := hex.DecodeString(env.Signature)
	if err != nil || !ed25519.Verify(key, env.Manifest, signature) {
		return fail()
	}
	var m Manifest
	if json.Unmarshal(env.Manifest, &m) != nil {
		return fail()
	}
	canonical, _ = json.Marshal(m)
	if !bytes.Equal(env.Manifest, canonical) {
		return fail()
	}
	if !validCapture(m) || m.Version != Version || m.Revision != Revision || m.Workspace != workspace || m.Profile != filepath.Join(workspace, ".hermes") || !idPattern.MatchString(m.ID) || m.CreatedAt.IsZero() || m.CreatedAt.Location() != time.UTC || len(m.BinarySHA256) != 64 || m.Payload.Path != PayloadFile || m.Payload.Mode != 0440 {
		return fail()
	}
	if filepath.Base(pkg) == m.ID {
		info, err := statFD(dir)
		if err != nil || info.Mode&07777 != 0750 {
			return fail()
		}
	}
	payload, err := openAt(dir, PayloadFile, false)
	if err != nil {
		return fail()
	}
	defer payload.Close()
	pi, _ := statFD(payload)
	if pi.Mode&07777 != 0440 {
		return fail()
	}
	digest, size, err := hashFile(ctx, payload)
	if err != nil || digest != m.Payload.SHA256 || size != m.Payload.Size {
		return fail()
	}
	inventory, err := inspectZIP(ctx, payload)
	if err != nil {
		return fail()
	}
	a, _ := json.Marshal(inventory)
	b, _ := json.Marshal(m.Inventory)
	if !bytes.Equal(a, b) || matchInventory(inventory, m.Sources, m.Schema == CaptureSchema) != nil {
		return fail()
	}
	if err = chain.revalidate(); err != nil {
		return fail()
	}
	named, err := statAt(dir, ManifestFile)
	if err != nil || named != fi {
		return fail()
	}
	named, err = statAt(dir, PayloadFile)
	if err != nil || named != pi {
		return fail()
	}
	h := sha256.Sum256(raw)
	sha := hex.EncodeToString(h[:])
	return Evidence{m.ID, pkg, sha, m.CreatedAt, []File{{ManifestFile, int64(len(raw)), 0440, sha}, m.Payload}}, m, nil
}

// Verify authenticates an exact package without consulting or opening the live
// profile. A restored package retains its original canonical profile identity.
func Verify(ctx context.Context, pkg, workspace string, key ed25519.PublicKey) (Evidence, error) {
	e, m, err := verify(ctx, pkg, workspace, key)
	if err == nil && filepath.Base(pkg) != m.ID {
		return Evidence{}, fmt.Errorf("recovery package ID mismatch")
	}
	return e, err
}
