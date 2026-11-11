//go:build darwin || linux

package main

import (
	"archive/tar"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/sys/unix"
)

const (
	importMaxBytes    = int64(512 << 20)
	importMaxMembers  = 10000
	importReceiptName = ".loom-working-copy.json"
)

type importRequest struct {
	RootIdentity  string `json:"root_identity"`
	Archive       string `json:"archive"`
	ArchiveSHA256 string `json:"archive_sha256"`
	ArchiveBytes  int64  `json:"archive_bytes"`
	Target        string `json:"target"`
	ID            string `json:"id"`
}

// Published means the target was published (possibly by an earlier invocation),
// not that its postconditions were verified.
// Uncertain requires inspection or exact receipt replay, never a fresh import ID
// or removal of either visible or staged data.
type importReply struct {
	OK                bool   `json:"ok"`
	Replayed          bool   `json:"replayed"`
	ID                string `json:"id"`
	Target            string `json:"target"`
	Published         bool   `json:"published"`
	Uncertain         bool   `json:"uncertain"`
	DirectoryIdentity string `json:"directory_identity,omitempty"`
	Stage             string `json:"stage,omitempty"`
	Members           int    `json:"members"`
	PayloadBytes      int64  `json:"payload_bytes"`
	SymlinkPolicy     string `json:"symlink_policy"`
	Error             string `json:"error,omitempty"`
}

type importReceipt struct {
	importRequest
	DirectoryIdentity string `json:"directory_identity"`
	Members           int    `json:"members"`
	PayloadBytes      int64  `json:"payload_bytes"`
}

func runImport(root string, input io.Reader, out io.Writer) int {
	var request importRequest
	data, err := io.ReadAll(io.LimitReader(input, (64<<10)+1))
	if err == nil && len(data) > 64<<10 {
		err = fmt.Errorf("import request exceeds 64 KiB")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err == nil {
		err = decoder.Decode(&request)
	}
	if err == nil {
		var extra any
		if decoder.Decode(&extra) != io.EOF {
			err = fmt.Errorf("expected one import request")
		}
	}
	result := importReply{ID: request.ID, Target: request.Target, SymlinkPolicy: "reject"}
	if err == nil {
		result, err = importDirectory(root, request)
	}
	result.OK = err == nil
	if err != nil {
		result.Error = err.Error()
	}
	if json.NewEncoder(out).Encode(result) != nil || err != nil {
		return 1
	}
	return 0
}

func importAbsolute(value string) bool {
	return filepath.IsAbs(value) && filepath.Clean(value) == value && !strings.ContainsRune(value, '\x00')
}

func (r importRequest) validate(root string) (inode, error) {
	expected, err := parseInode(r.RootIdentity)
	if err != nil {
		return inode{}, err
	}
	if !importAbsolute(root) || !importAbsolute(r.Archive) {
		return inode{}, fmt.Errorf("root and archive must be absolute and clean")
	}
	if err := validateRelative(r.Target); err != nil {
		return inode{}, err
	}
	if len(r.ID) < 1 || len(r.ID) > 64 || strings.IndexFunc(r.ID, func(c rune) bool {
		return !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_')
	}) >= 0 {
		return inode{}, fmt.Errorf("id must contain 1-64 ASCII letters, digits, hyphens or underscores; never reuse an id")
	}
	digest, err := hex.DecodeString(r.ArchiveSHA256)
	if err != nil || len(digest) != sha256.Size || len(r.ArchiveSHA256) != 64 {
		return inode{}, fmt.Errorf("archive_sha256 must be 64 hexadecimal characters")
	}
	if r.ArchiveBytes < 1024 || r.ArchiveBytes > importMaxBytes || r.ArchiveBytes%512 != 0 {
		return inode{}, fmt.Errorf("archive_bytes must be a tar block multiple between 1024 and %d", importMaxBytes)
	}
	return expected, nil
}

type importNode struct {
	identity inode
	dir      bool
	explicit bool
	mode     uint32
	stat     unix.Stat_t
}

type importStage struct {
	root    *directoryChain
	fd      int
	name    string
	nodes   map[string]*importNode // Includes implicit parents and the stage itself ("").
	aliases map[string]string
}

func importIdentity(s *unix.Stat_t) inode { return inode{uint64(s.Dev), uint64(s.Ino)} }

func (n *importNode) matches(s *unix.Stat_t) bool {
	if s == nil || importIdentity(s) != n.identity {
		return false
	}
	if n.dir {
		return s.Mode&unix.S_IFMT == unix.S_IFDIR
	}
	return s.Mode&unix.S_IFMT == unix.S_IFREG && s.Nlink == 1 && s.Size == n.stat.Size && s.Mtim == n.stat.Mtim && s.Ctim == n.stat.Ctim
}

func (s *importStage) check() error {
	if err := s.root.check(); err != nil {
		return err
	}
	st, err := statAt(s.root.fd(), s.name)
	if err != nil || !s.nodes[""].matches(st) {
		return fmt.Errorf("owned stage identity changed: %w", errors.Join(err, unix.ESTALE))
	}
	return nil
}

// Every opened member parent must be one of our recorded directories, not just
// a directory that happens to occupy the same pathname now.
func (s *importStage) parent(name string, create bool) (*directoryChain, string, error) {
	fd, err := unix.Dup(s.fd)
	if err != nil {
		return nil, "", err
	}
	c := &directoryChain{fds: []int{fd}}
	current := ""
	parts := strings.Split(name, "/")
	for _, part := range parts[:len(parts)-1] {
		current = path.Join(current, part)
		node := s.nodes[current]
		if node == nil && create {
			if len(s.nodes) > importMaxMembers {
				err = fmt.Errorf("too many imported entries including implicit parents")
			} else {
				err = unix.Mkdirat(c.fd(), part, 0700)
			}
			if err == nil {
				var st *unix.Stat_t
				st, err = statAt(c.fd(), part)
				if err == nil && st != nil && st.Mode&unix.S_IFMT == unix.S_IFDIR {
					node = &importNode{identity: importIdentity(st), dir: true, mode: 0755}
					s.nodes[current] = node
				} else if err == nil {
					err = unix.ESTALE
				}
			}
		}
		if err != nil || node == nil || !node.dir {
			c.close()
			return nil, "", fmt.Errorf("unsafe member parent %q: %w", current, errors.Join(err, unix.ESTALE))
		}
		child, openErr := unix.Openat(c.fd(), part, directoryFlags, 0)
		if openErr != nil {
			c.close()
			return nil, "", openErr
		}
		var st unix.Stat_t
		err = unix.Fstat(child, &st)
		if err != nil || !node.matches(&st) {
			unix.Close(child)
			c.close()
			return nil, "", fmt.Errorf("member parent identity changed")
		}
		c.names = append(c.names, part)
		c.fds = append(c.fds, child)
	}
	return c, parts[len(parts)-1], nil
}

func importMemberName(h *tar.Header) (string, error) {
	name := h.Name
	if h.Typeflag == tar.TypeDir {
		name = strings.TrimSuffix(name, "/")
	}
	if name == "" || name == "." || name == ".." || path.IsAbs(name) || path.Clean(name) != name || strings.HasPrefix(name, "../") || strings.ContainsAny(name, "\\\x00:") || !utf8.ValidString(name) || len(name) > 4096 || strings.Count(name, "/") >= 64 {
		return "", fmt.Errorf("unsafe tar member name %q", h.Name)
	}
	if strings.EqualFold(strings.SplitN(name, "/", 2)[0], importReceiptName) {
		return "", fmt.Errorf("tar member collides with reserved working-copy receipt")
	}
	// Match the exporter's self-contained Git policy, including nested repos.
	parts := strings.Split(strings.ToLower(name), "/")
	for i, part := range parts {
		if part != ".git" {
			continue
		}
		rest := strings.Join(parts[i+1:], "/")
		if rest == "" && h.Typeflag != tar.TypeDir || rest == "commondir" || rest == "gitdir" || rest == "config.worktree" || rest == "worktrees" || strings.HasPrefix(rest, "worktrees/") || rest == "modules" || strings.HasPrefix(rest, "modules/") || rest == "objects/info/alternates" || rest == "objects/info/http-alternates" {
			return "", fmt.Errorf("external Git metadata is not supported: %q", name)
		}
	}
	return name, nil
}

func (s *importStage) claimName(name string) error {
	if s.aliases == nil {
		s.aliases = make(map[string]string)
	}
	current := ""
	for _, part := range strings.Split(name, "/") {
		current = path.Join(current, part)
		folded := strings.Map(func(r rune) rune {
			lowest := r
			for next := unicode.SimpleFold(r); next != r; next = unicode.SimpleFold(next) {
				if next < lowest {
					lowest = next
				}
			}
			return lowest
		}, current)
		if prior, exists := s.aliases[folded]; exists && prior != current {
			return fmt.Errorf("case-alias tar member %q conflicts with %q", current, prior)
		}
		s.aliases[folded] = current
	}
	return nil
}

func (s *importStage) member(tr *tar.Reader, h *tar.Header, name string) (err error) {
	if h.Typeflag != tar.TypeDir && h.Typeflag != tar.TypeReg && h.Typeflag != tar.TypeRegA || h.Linkname != "" || h.Size < 0 || h.Typeflag == tar.TypeDir && h.Size != 0 {
		return fmt.Errorf("unsupported tar member type for %q (all links and special files are rejected)", name)
	}
	for key := range h.PAXRecords {
		if strings.HasPrefix(key, "GNU.sparse") || key == "SCHILY.filetype" || key == "SCHILY.realsize" {
			return fmt.Errorf("sparse or special tar members are not supported")
		}
	}
	if err := s.claimName(name); err != nil {
		return err
	}
	parent, leaf, err := s.parent(name, true)
	if err != nil {
		return err
	}
	defer parent.close()
	if err := errors.Join(s.check(), parent.check()); err != nil {
		return err
	}
	if node := s.nodes[name]; node != nil {
		st, err := statAt(parent.fd(), leaf)
		if err != nil || !node.matches(st) || node.explicit || !node.dir || h.Typeflag != tar.TypeDir {
			return fmt.Errorf("duplicate or conflicting tar member %q", name)
		}
		node.explicit, node.mode = true, uint32(h.Mode)&0777|0700
		return nil
	}
	if len(s.nodes) > importMaxMembers {
		return fmt.Errorf("too many imported entries including implicit parents")
	}
	if h.Typeflag == tar.TypeDir {
		// Mkdirat/O_EXCL below also reject filesystem case/Unicode aliases.
		if err := unix.Mkdirat(parent.fd(), leaf, 0700); err != nil {
			return fmt.Errorf("create unique member %q: %w", name, err)
		}
		st, err := statAt(parent.fd(), leaf)
		if err != nil || st == nil || st.Mode&unix.S_IFMT != unix.S_IFDIR {
			return fmt.Errorf("new directory identity unavailable: %w", errors.Join(err, unix.ESTALE))
		}
		s.nodes[name] = &importNode{identity: importIdentity(st), dir: true, explicit: true, mode: uint32(h.Mode)&0777 | 0700}
		return nil
	}
	fd, err := unix.Openat(parent.fd(), leaf, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if err != nil {
		return fmt.Errorf("create unique member %q: %w", name, err)
	}
	file := os.NewFile(uintptr(fd), name)
	defer func() { err = errors.Join(err, file.Close()) }()
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return err
	}
	node := &importNode{identity: importIdentity(&st), explicit: true, stat: st}
	s.nodes[name] = node
	// Git's config parser has includes, escaped continuations and external
	// worktree/hook settings. Hold these conservatively instead of approximating it.
	var config bytes.Buffer
	var writer io.Writer = file
	lower := strings.ToLower(name)
	if strings.HasSuffix(lower, "/.git/config") || lower == ".git/config" {
		if h.Size > 1<<20 {
			return fmt.Errorf("Git config exceeds import inspection limit")
		}
		writer = io.MultiWriter(file, &config)
	}
	_, copyErr := io.CopyN(writer, tr, h.Size)
	if copyErr == nil && writer != file {
		text := strings.ToLower(config.String())
		for _, unsafe := range []string{"worktree", "include", "hookspath", "fsmonitor", "\\"} {
			if strings.Contains(text, unsafe) {
				copyErr = fmt.Errorf("Git config with external/escaped settings is not supported")
				break
			}
		}
	}
	err = errors.Join(copyErr, unix.Fchmod(fd, uint32(h.Mode)&0777|0600), file.Sync())
	statErr := unix.Fstat(fd, &node.stat)
	return errors.Join(err, statErr)
}

func (s *importStage) names() []string {
	names := make([]string, 0, len(s.nodes)-1)
	for name := range s.nodes {
		if name != "" {
			names = append(names, name)
		}
	}
	// Descendants precede their parents for both directory fsync and cleanup.
	sort.Sort(sort.Reverse(sort.StringSlice(names)))
	return names
}

func (s *importStage) checkContents(fd int, name string) error {
	// A fresh description avoids sharing directory offsets with a pinned fd.
	child, err := unix.Openat(fd, ".", directoryFlags, 0)
	if err != nil {
		return err
	}
	file := os.NewFile(uintptr(child), name)
	defer file.Close()
	entries, err := file.ReadDir(importMaxMembers + 2)
	if err != nil && err != io.EOF {
		return err
	}
	if len(entries) > importMaxMembers+1 { // Snapshot entries plus the native receipt.
		return fmt.Errorf("unexpected stage inventory")
	}
	for _, entry := range entries {
		relative := path.Join(name, entry.Name())
		node := s.nodes[relative]
		st, err := statAt(fd, entry.Name())
		if err != nil || node == nil || !node.matches(st) {
			return fmt.Errorf("unexpected or changed stage entry %q", relative)
		}
	}
	return nil
}

func (s *importStage) finish(remove bool) error {
	if err := s.check(); err != nil {
		return err
	}
	for _, name := range s.names() {
		parent, leaf, err := s.parent(name, false)
		if err != nil {
			return err
		}
		node := s.nodes[name]
		st, err := statAt(parent.fd(), leaf)
		if err == nil && !node.matches(st) {
			err = fmt.Errorf("member identity changed: %q", name)
		}
		if err == nil {
			err = errors.Join(s.check(), parent.check())
		}
		if err == nil && remove {
			flags := 0
			if node.dir {
				flags = unix.AT_REMOVEDIR
			}
			err = unix.Unlinkat(parent.fd(), leaf, flags)
		} else if err == nil && node.dir {
			var fd int
			fd, err = unix.Openat(parent.fd(), leaf, directoryFlags, 0)
			if err == nil {
				var pinned unix.Stat_t
				err = unix.Fstat(fd, &pinned)
				if err == nil && !node.matches(&pinned) {
					err = unix.ESTALE
				}
				if err == nil {
					err = s.checkContents(fd, name)
				}
				if err == nil {
					err = errors.Join(unix.Fchmod(fd, node.mode), unix.Fsync(fd))
				}
				unix.Close(fd)
			}
		}
		parent.close()
		if err != nil {
			return err
		}
	}
	if remove {
		if err := s.check(); err != nil {
			return err
		}
		if err := unix.Unlinkat(s.root.fd(), s.name, unix.AT_REMOVEDIR); err != nil {
			return err
		}
		return unix.Fsync(s.root.fd())
	}
	return errors.Join(s.checkContents(s.fd, ""), unix.Fsync(s.fd))
}

func (s *importStage) writeReceipt(r importRequest, result importReply) (err error) {
	if err := s.check(); err != nil {
		return err
	}
	data, err := json.Marshal(importReceipt{r, result.DirectoryIdentity, result.Members, result.PayloadBytes})
	if err != nil {
		return err
	}
	fd, err := unix.Openat(s.fd, importReceiptName, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if err != nil {
		return fmt.Errorf("create private import receipt: %w", err)
	}
	file := os.NewFile(uintptr(fd), importReceiptName)
	defer func() { err = errors.Join(err, file.Close()) }()
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return err
	}
	node := &importNode{identity: importIdentity(&st), explicit: true, stat: st}
	s.nodes[importReceiptName] = node
	_, writeErr := file.Write(append(data, '\n'))
	err = errors.Join(writeErr, unix.Fchmod(fd, 0600), file.Sync())
	return errors.Join(err, unix.Fstat(fd, &node.stat))
}

// Replay only reads the receipt and directory identities. In particular it does
// not walk, re-hash, chmod or otherwise modify the user's current files.
func replayImport(root, parent *directoryChain, name string, owner uint32, r importRequest, result importReply) (importReply, error) {
	collision := func(err error) (importReply, error) {
		return result, fmt.Errorf("destination collision; exact private import receipt required: %w", err)
	}
	fd, err := unix.Openat(parent.fd(), name, directoryFlags, 0)
	if err != nil {
		return collision(err)
	}
	defer unix.Close(fd)
	var directory unix.Stat_t
	expected, _ := parseInode(r.RootIdentity) // The request was validated before replay.
	if err := unix.Fstat(fd, &directory); err != nil || directory.Uid != owner || uint64(directory.Dev) != expected.device {
		return collision(errors.Join(err, unix.ESTALE))
	}
	identity := fmt.Sprintf("%d:%d", directory.Dev, directory.Ino)
	receiptFD, err := unix.Openat(fd, importReceiptName, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return collision(err)
	}
	file := os.NewFile(uintptr(receiptFD), importReceiptName)
	defer file.Close()
	var initial unix.Stat_t
	if err := unix.Fstat(receiptFD, &initial); err != nil || initial.Mode&unix.S_IFMT != unix.S_IFREG || initial.Mode&07777 != 0600 || initial.Nlink != 1 || initial.Uid != owner || initial.Size <= 0 || initial.Size > 64<<10 {
		return collision(fmt.Errorf("unsafe receipt mode, ownership, type or length"))
	}
	data, err := io.ReadAll(io.LimitReader(file, (64<<10)+1))
	if err != nil || int64(len(data)) != initial.Size {
		return collision(fmt.Errorf("receipt changed during read"))
	}
	var receipt importReceipt
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&receipt); err != nil {
		return collision(err)
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF || receipt.importRequest != r || receipt.DirectoryIdentity != identity || receipt.Members < 0 || receipt.Members > importMaxMembers || receipt.PayloadBytes < 0 || receipt.PayloadBytes > importMaxBytes {
		return collision(fmt.Errorf("receipt request or inode binding mismatch"))
	}
	canonical, err := json.Marshal(receipt)
	if err != nil || !bytes.Equal(data, append(canonical, '\n')) {
		return collision(fmt.Errorf("receipt is not the exact native record"))
	}
	var final unix.Stat_t
	entry, entryErr := statAt(fd, importReceiptName)
	target, targetErr := statAt(parent.fd(), name)
	// Complete parent durability if the previous process stopped after rename.
	if err := errors.Join(entryErr, targetErr, unix.Fstat(receiptFD, &final), unix.Fsync(root.fd()), unix.Fsync(parent.fd()), root.check(), parent.check()); err != nil {
		return collision(err)
	}
	proof := importNode{identity: importIdentity(&initial), stat: initial}
	if !proof.matches(&final) || !proof.matches(entry) || final.Mode&07777 != 0600 || final.Uid != owner || !(importIdentity(&directory)).matchesDirectory(target) || target.Uid != owner {
		return collision(fmt.Errorf("receipt or destination changed during verification"))
	}
	result.Replayed, result.Published = true, true
	result.DirectoryIdentity, result.Members, result.PayloadBytes = identity, receipt.Members, receipt.PayloadBytes
	return result, nil
}

func importDirectory(root string, r importRequest) (result importReply, err error) {
	result = importReply{ID: r.ID, Target: r.Target, SymlinkPolicy: "reject"}
	expected, err := r.validate(root)
	if err != nil {
		return result, err
	}
	rootChain, _, err := openParent(root, "unused")
	if err != nil {
		return result, err
	}
	defer rootChain.close()
	var rootStat unix.Stat_t
	if err := unix.Fstat(rootChain.fd(), &rootStat); err != nil || !expected.matchesDirectory(&rootStat) {
		return result, fmt.Errorf("root identity mismatch: %w", errors.Join(err, unix.ESTALE))
	}
	if rootStat.Uid != uint32(os.Geteuid()) {
		return result, fmt.Errorf("import root must be owned by the effective user")
	}
	targetParent, targetName, err := openParent(root, r.Target)
	if err != nil {
		return result, err
	}
	defer targetParent.close()
	checkParents := func() error {
		if err := errors.Join(rootChain.check(), targetParent.check()); err != nil {
			return err
		}
		for _, fd := range targetParent.fds[len(rootChain.fds)-1:] {
			var st unix.Stat_t
			if err := unix.Fstat(fd, &st); err != nil || uint64(st.Dev) != expected.device {
				return fmt.Errorf("destination must remain on the root filesystem")
			}
		}
		var st unix.Stat_t
		if err := unix.Fstat(targetParent.fds[len(rootChain.fds)-1], &st); err != nil || !expected.matchesDirectory(&st) {
			return fmt.Errorf("destination root identity changed")
		}
		return nil
	}
	checkTarget := func() error {
		if err := checkParents(); err != nil {
			return err
		}
		stTarget, err := statAt(targetParent.fd(), targetName)
		if err != nil {
			return err
		}
		if stTarget != nil {
			return fmt.Errorf("destination already exists; inspect any prior import, never overwrite or automatically replay")
		}
		return nil
	}
	if err := checkParents(); err != nil {
		return result, err
	}
	target, err := statAt(targetParent.fd(), targetName)
	if err != nil {
		return result, err
	}
	if target != nil {
		if uint64(target.Dev) != expected.device {
			return result, fmt.Errorf("destination collision on a different filesystem")
		}
		return replayImport(rootChain, targetParent, targetName, rootStat.Uid, r, result)
	}
	archiveParent, archiveName, err := openParent(filepath.Dir(r.Archive), filepath.Base(r.Archive))
	if err != nil {
		return result, err
	}
	defer archiveParent.close()
	fd, err := unix.Openat(archiveParent.fd(), archiveName, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return result, err
	}
	archive := os.NewFile(uintptr(fd), r.Archive)
	defer archive.Close()
	var original unix.Stat_t
	if err := unix.Fstat(fd, &original); err != nil || original.Mode&unix.S_IFMT != unix.S_IFREG || original.Nlink != 1 || original.Size != r.ArchiveBytes {
		return result, fmt.Errorf("archive must be a single-link regular file of exactly archive_bytes")
	}
	if err := archiveParent.check(); err != nil {
		return result, err
	}
	stageName := ".loom-working-copy-" + r.ID + ".partial"
	if err := unix.Mkdirat(rootChain.fd(), stageName, 0700); err != nil {
		if errors.Is(err, unix.EEXIST) {
			// It may be an interrupted request, but its ownership is unproven.
			result.Stage, result.Uncertain = stageName, true
		}
		return result, fmt.Errorf("create exclusive stage (never reuse or remove an existing stage): %w", err)
	}
	result.Stage, result.Uncertain = stageName, true
	stageFD, err := unix.Openat(rootChain.fd(), stageName, directoryFlags, 0)
	if err != nil {
		return result, err
	}
	defer unix.Close(stageFD)
	var stageStat unix.Stat_t
	if err := unix.Fstat(stageFD, &stageStat); err != nil || stageStat.Uid != uint32(os.Geteuid()) || stageStat.Mode&0777 != 0700 || uint64(stageStat.Dev) != expected.device {
		return result, fmt.Errorf("cannot prove private stage ownership")
	}
	stage := &importStage{root: rootChain, fd: stageFD, name: stageName, nodes: map[string]*importNode{
		"": {identity: importIdentity(&stageStat), dir: true},
	}}
	result.DirectoryIdentity = fmt.Sprintf("%d:%d", stageStat.Dev, stageStat.Ino)
	result.Uncertain = false
	defer func() {
		if err != nil && !result.Published {
			if cleanupErr := stage.finish(true); cleanupErr != nil {
				result.Uncertain = true
				err = errors.Join(err, fmt.Errorf("stage retained for inspection: %w", cleanupErr))
			} else {
				result.Stage = ""
			}
		}
	}()
	if err := errors.Join(stage.check(), unix.Fsync(stageFD), unix.Fsync(rootChain.fd())); err != nil {
		return result, err
	}
	hash := sha256.New()
	limited := &io.LimitedReader{R: archive, N: r.ArchiveBytes + 1}
	stream := io.TeeReader(limited, hash)
	tr := tar.NewReader(stream)
	for {
		h, nextErr := tr.Next()
		if nextErr == io.EOF {
			break
		}
		if nextErr != nil {
			return result, fmt.Errorf("read tar: %w", nextErr)
		}
		result.Members++
		if result.Members > importMaxMembers || h.Size < 0 || h.Size > importMaxBytes-result.PayloadBytes {
			return result, fmt.Errorf("tar exceeds member or payload limit")
		}
		name, nameErr := importMemberName(h)
		if nameErr != nil {
			return result, nameErr
		}
		if err := stage.member(tr, h, name); err != nil {
			return result, err
		}
		result.PayloadBytes += h.Size
	}
	// tar.Reader stops at its end marker. Hash/count ALL remaining archive bytes,
	// accepting only zero padding, not a concatenated archive or hidden payload.
	padding := make([]byte, 32<<10)
	for {
		n, readErr := stream.Read(padding)
		if bytes.Count(padding[:n], []byte{0}) != n {
			return result, fmt.Errorf("nonzero data after tar end marker")
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return result, readErr
		}
	}
	if limited.N != 1 || !strings.EqualFold(hex.EncodeToString(hash.Sum(nil)), r.ArchiveSHA256) {
		return result, fmt.Errorf("archive length or SHA-256 mismatch")
	}
	var final unix.Stat_t
	archivePath, pathErr := statAt(archiveParent.fd(), archiveName)
	if err := errors.Join(pathErr, archiveParent.check(), unix.Fstat(fd, &final)); err != nil {
		return result, err
	}
	source := importNode{identity: importIdentity(&original), stat: original}
	if !source.matches(&final) || !source.matches(archivePath) {
		return result, fmt.Errorf("archive identity or contents changed during import")
	}
	if err := stage.writeReceipt(r, result); err != nil {
		return result, err
	}
	if err := stage.finish(false); err != nil {
		return result, err
	}
	if err := errors.Join(stage.check(), checkTarget()); err != nil {
		return result, err
	}
	if err := renameNoReplace(rootChain.fd(), stageName, targetParent.fd(), targetName); err != nil {
		return result, fmt.Errorf("publish without replacement: %w", err)
	}
	result.Published, result.Uncertain, result.Stage = true, true, ""
	syncErr := errors.Join(unix.Fsync(rootChain.fd()), unix.Fsync(targetParent.fd()))
	if err := errors.Join(syncErr, rootChain.check(), targetParent.check()); err != nil {
		return result, fmt.Errorf("publication requires inspection: %w", err)
	}
	target, targetErr := statAt(targetParent.fd(), targetName)
	remaining, stageErr := statAt(rootChain.fd(), stageName)
	if err := errors.Join(targetErr, stageErr); err != nil || remaining != nil || !stage.nodes[""].matches(target) {
		return result, fmt.Errorf("publication outcome unverified: %w", errors.Join(err, unix.ESTALE))
	}
	result.Uncertain = false
	return result, nil
}
