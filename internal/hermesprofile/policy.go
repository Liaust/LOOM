package hermesprofile

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"time"
)

// RecoveryReadmeFile is an ordinary versioned workspace contract, never signed
// recovery evidence. These bounds also govern the frozen Borg namespace.
const RecoveryReadmeFile = "README.md"
const RecoveryReadmeMode = 0644
const RecoveryReadmeMaxBytes = int64(1 << 20)

type recoveryReadme struct {
	file    *os.File
	parent  *os.File
	initial Identity
	digest  [32]byte
}

func validRecoveryReadme(info Identity, owner uint32) bool {
	return regular(info) && info.Mode&07777 == RecoveryReadmeMode && info.Owner == owner && info.Size >= 0 && info.Size <= RecoveryReadmeMaxBytes
}

func openRecoveryReadme(parent *os.File, info Identity, owner uint32) (*recoveryReadme, error) {
	if !validRecoveryReadme(info, owner) {
		return nil, fmt.Errorf("unsafe recovery README")
	}
	f, err := openAt(parent, RecoveryReadmeFile, false)
	if err != nil {
		return nil, err
	}
	r := &recoveryReadme{file: f, parent: parent, initial: info}
	held, err := statFD(f)
	if err != nil || held != info {
		f.Close()
		return nil, fmt.Errorf("recovery README substituted while opening")
	}
	raw, err := readBounded(f, RecoveryReadmeMaxBytes)
	if err != nil {
		f.Close()
		return nil, err
	}
	r.digest = sha256.Sum256(raw)
	if err := r.revalidate(); err != nil {
		f.Close()
		return nil, err
	}
	return r, nil
}

func (r *recoveryReadme) revalidate() error {
	named, err := statAt(r.parent, RecoveryReadmeFile)
	if err != nil || named != r.initial {
		return fmt.Errorf("recovery README namespace changed")
	}
	held, err := statFD(r.file)
	if err != nil || held != r.initial {
		return fmt.Errorf("held recovery README changed")
	}
	if _, err := r.file.Seek(0, 0); err != nil {
		return err
	}
	raw, err := readBounded(r.file, RecoveryReadmeMaxBytes)
	if err != nil || sha256.Sum256(raw) != r.digest {
		return fmt.Errorf("recovery README bytes changed")
	}
	held, err = statFD(r.file)
	if err != nil || held != r.initial {
		return fmt.Errorf("held recovery README changed")
	}
	named, err = statAt(r.parent, RecoveryReadmeFile)
	if err != nil || named != r.initial {
		return fmt.Errorf("recovery README namespace changed")
	}
	return nil
}

type Policy struct {
	Enabled bool
	// Workspace is physical custody. Identity selects the current producer.
	Workspace string
	PublicKey ed25519.PublicKey
	Identity  RecoveryIdentity
	// RetainedMorathustra explicitly binds these exact package IDs to the old
	// signed origin under a MINA policy. It never makes old evidence current.
	RetainedMorathustra []string
}

// Resolve validates identity selection even for disabled policies, without
// touching live state. A transition also protects the retired private tree.
func (p Policy) Resolve() (Policy, string, error) {
	// Historical disabled callers may supply an exclusion-only location. They
	// neither publish nor authenticate evidence. Preserve that path-only use;
	// an explicit producer or retained binding still requires exact selection.
	if !p.Enabled && p.Identity == "" && len(p.RetainedMorathustra) == 0 && exactWorkspacePath(p.Workspace) && p.Workspace != MinaWorkspaceRoot {
		return p, WorkspaceRoot, nil
	}
	location, origin, err := ResolveWorkspace(p.Identity, p.Workspace)
	if err != nil {
		return p, "", err
	}
	p.Workspace = location
	if len(p.RetainedMorathustra) > maxEntries || len(p.RetainedMorathustra) > 0 && p.Identity != MinaIdentity {
		return p, "", fmt.Errorf("retained Morathustra binding requires explicit MINA policy")
	}
	seen := map[string]bool{}
	for _, id := range p.RetainedMorathustra {
		if !idPattern.MatchString(id) || seen[id] {
			return p, "", fmt.Errorf("invalid or duplicate retained recovery binding")
		}
		seen[id] = true
	}
	return p, origin, nil
}

func ParsePolicy(enabled, key string) (Policy, error) {
	p := Policy{Workspace: WorkspaceRoot}
	if enabled != "" {
		v, e := strconv.ParseBool(enabled)
		if e != nil {
			return p, fmt.Errorf("invalid Morathustra enable flag")
		}
		p.Enabled = v
	}
	if key != "" {
		b, e := hex.DecodeString(key)
		if e != nil || len(b) != ed25519.PublicKeySize {
			return p, fmt.Errorf("invalid Morathustra recovery public key")
		}
		p.PublicKey = b
	}
	if p.Enabled && len(p.PublicKey) != ed25519.PublicKeySize {
		return p, fmt.Errorf("enabled Morathustra requires a recovery public key")
	}
	return p, nil
}

// Check verifies every included committed package, and requires fresh evidence
// when enabled. It never reads live profile state or treats staging as evidence.
func Check(ctx context.Context, p Policy, now time.Time) ([]Evidence, error) {
	p, origin, err := p.Resolve()
	if err != nil {
		return nil, err
	}
	if !p.Enabled {
		return nil, nil
	}
	if len(p.PublicKey) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("recovery verification key missing")
	}
	c, err := openCustody(filepath.Join(p.Workspace, "recovery"))
	if err != nil {
		return nil, err
	}
	defer c.close()
	root, err := statFD(c.leaf())
	if err != nil {
		return nil, err
	}
	ns, err := names(c.leaf())
	if err != nil {
		return nil, err
	}
	sort.Strings(ns)
	var result []Evidence
	var readme *recoveryReadme
	fresh := false
	retained := map[string]bool{}
	for _, id := range p.RetainedMorathustra {
		retained[id] = true
	}
	for _, name := range ns {
		entry, err := statAt(c.leaf(), name)
		if err == nil && name == RecoveryReadmeFile {
			readme, err = openRecoveryReadme(c.leaf(), entry, root.Owner)
			if err != nil {
				return nil, err
			}
			defer readme.file.Close()
			continue
		}
		if err != nil || !directory(entry) {
			return nil, fmt.Errorf("unexpected recovery entry")
		}
		if name == ".staging" {
			continue
		}
		if !idPattern.MatchString(name) || entry.Mode&07777 != 0750 {
			return nil, fmt.Errorf("invalid recovery package custody")
		}
		expectedOrigin := origin
		if retained[name] {
			expectedOrigin = WorkspaceRoot
		}
		e, err := Verify(ctx, filepath.Join(p.Workspace, "recovery", name), expectedOrigin, p.PublicKey)
		if err != nil {
			return nil, err
		}
		if e.CreatedAt.After(now) {
			return nil, fmt.Errorf("future recovery evidence")
		}
		if expectedOrigin == origin && now.Sub(e.CreatedAt) <= MaxAge {
			fresh = true
		}
		delete(retained, name)
		result = append(result, e)
	}
	if len(retained) != 0 {
		return nil, fmt.Errorf("declared retained recovery evidence missing")
	}
	if !fresh {
		return nil, fmt.Errorf("current Hermes producer lacks fresh authenticated recovery evidence")
	}
	if readme != nil {
		if err := readme.revalidate(); err != nil {
			return nil, err
		}
	}
	// Detect insertion, removal and rename/ABA in the root while packages were
	// checked, including an absent README appearing after initial enumeration.
	end, err := statFD(c.leaf())
	if err != nil || end != root {
		return nil, fmt.Errorf("recovery namespace changed during discovery")
	}
	return result, c.revalidate()
}
