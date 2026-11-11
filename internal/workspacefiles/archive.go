package workspacefiles

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"hash"
	"hash/crc32"
	"io"
	"io/fs"
	"os"
	"path"
	"sort"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"
)

const (
	archiveMaxEntries   = 4096
	archiveMaxDirectory = 8 << 20
	archiveMaxNames     = 1 << 20
	archiveMaxMember    = 256 << 20
	archiveMaxRatio     = 200
	archiveReadChunk    = 32 << 10
	archiveEndLen       = 22
	archiveHeaderLen    = 46
)

// ArchiveMember describes a regular file, not an extracted filesystem path.
// ID is stable for unchanged ZIP metadata, not a content hash or authorization;
// callers must also bind it to the admitted archive and its source revision.
type ArchiveMember struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Size  int64  `json:"size"`
	CRC32 uint32 `json:"crc32"`
}

// ListArchiveMembers validates a single ZIP32 archive and lists its regular
// members in central-directory order without decoding any payload. Store and
// Deflate are supported; ZIP64, split archives and noncanonical slash paths are
// rejected. Directory entries count toward limits but are omitted from the list.
// Limits are 4096 entries, 8 MiB of directory data, 1 MiB of total names,
// 256 MiB decoded per member and a 200:1 declared compression ratio.
// Names are UTF-8 and case-sensitive. The caller retains ownership of file.
func ListArchiveMembers(ctx context.Context, file *os.File) ([]ArchiveMember, error) {
	archive, err := readArchive(ctx, file)
	if err != nil {
		return nil, err
	}
	members := make([]ArchiveMember, 0, len(archive.members))
	for _, member := range archive.members {
		members = append(members, member.info)
	}
	return members, nil
}

// OpenArchiveMember validates the archive and opens exactly one listed member.
// A successful read through EOF verifies decoded size and CRC, including CRC=0.
// Close never drains the stream: early close is NOT integrity verification.
// Cancellation is checked during metadata I/O and bounded payload reads. Neither
// cancellation nor Close closes file; the caller must keep it open while reading.
// Nested archives are ordinary bytes and are never automatically expanded.
func OpenArchiveMember(ctx context.Context, file *os.File, memberID string) (io.ReadCloser, ArchiveMember, error) {
	archive, err := readArchive(ctx, file)
	if err != nil {
		return nil, ArchiveMember{}, err
	}
	for _, member := range archive.members {
		if member.info.ID != memberID {
			continue
		}
		if err := archive.checkLocalHeader(member); err != nil {
			return nil, ArchiveMember{}, err
		}
		reader, err := member.file.Open()
		if err != nil {
			return nil, ArchiveMember{}, err
		}
		return &archiveMemberReader{
			ctx: ctx, reader: reader, remaining: member.info.Size,
			crc: crc32.NewIEEE(), expectedCRC: member.info.CRC32,
		}, member.info, nil
	}
	return nil, ArchiveMember{}, fmt.Errorf("ZIP member not found: %w", fs.ErrNotExist)
}

type archiveMember struct {
	info   ArchiveMember
	file   *zip.File
	offset int64
}

type safeArchive struct {
	reader  *archiveReaderAt
	members []archiveMember
}

func readArchive(ctx context.Context, file *os.File) (*safeArchive, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if file == nil {
		return nil, os.ErrInvalid
	}
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() < archiveEndLen {
		return nil, zip.ErrFormat
	}
	raw := archiveContextReaderAt{ctx: ctx, reader: file}
	tail := make([]byte, min(info.Size(), archiveEndLen+65535))
	if _, err := raw.ReadAt(tail, info.Size()-int64(len(tail))); err != nil {
		return nil, err
	}
	endIndex := -1
	for i := len(tail) - archiveEndLen; i >= 0; i-- {
		if binary.LittleEndian.Uint32(tail[i:]) == 0x06054b50 &&
			i+archiveEndLen+int(binary.LittleEndian.Uint16(tail[i+20:])) == len(tail) {
			endIndex = i
			break
		}
	}
	if endIndex < 0 {
		return nil, zip.ErrFormat
	}
	end := tail[endIndex : endIndex+archiveEndLen]
	count := int(binary.LittleEndian.Uint16(end[10:]))
	size := int64(binary.LittleEndian.Uint32(end[12:]))
	offset := int64(binary.LittleEndian.Uint32(end[16:]))
	endOffset := info.Size() - int64(len(tail)) + int64(endIndex)
	if count == 0xffff || size == 0xffffffff || offset == 0xffffffff {
		return nil, fmt.Errorf("ZIP64 is unsupported: %w", zip.ErrFormat)
	}
	if count > archiveMaxEntries || size > archiveMaxDirectory {
		return nil, fmt.Errorf("ZIP directory limit exceeded: %w", zip.ErrFormat)
	}
	if binary.LittleEndian.Uint16(end[4:]) != 0 || binary.LittleEndian.Uint16(end[6:]) != 0 ||
		int(binary.LittleEndian.Uint16(end[8:])) != count || offset+size != endOffset {
		return nil, fmt.Errorf("split or inconsistent ZIP directory: %w", zip.ErrFormat)
	}
	// Freeze the bounded directory and a comment-free EOCD before zip.NewReader.
	// Its permissive record-count handling must not reparse mutable/unbounded
	// metadata or discover a different EOCD embedded inside an archive comment.
	metadata := make([]byte, size+archiveEndLen)
	if _, err := raw.ReadAt(metadata[:size], offset); err != nil {
		return nil, err
	}
	copy(metadata[size:], end)
	binary.LittleEndian.PutUint16(metadata[size+20:], 0)
	offsets, err := checkArchiveDirectory(ctx, metadata[:size], count, offset)
	if err != nil {
		return nil, err
	}
	reader := &archiveReaderAt{raw: raw, offset: offset, metadata: metadata}
	zr, err := zip.NewReader(reader, offset+int64(len(metadata)))
	if err != nil {
		return nil, err
	}
	if len(zr.File) != count {
		return nil, zip.ErrFormat
	}
	digest := sha256.Sum256(metadata)
	archive := &safeArchive{reader: reader}
	names := make(map[string]bool, count)
	for i, file := range zr.File {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		name := strings.TrimSuffix(file.Name, "/")
		if !safeArchiveName(name) || strings.Contains(file.Name, "//") {
			return nil, fmt.Errorf("unsafe ZIP member name: %w", zip.ErrFormat)
		}
		if _, exists := names[name]; exists {
			return nil, fmt.Errorf("duplicate ZIP member name: %w", zip.ErrFormat)
		}
		mode := file.Mode()
		isDir := strings.HasSuffix(file.Name, "/")
		host, unixType := file.CreatorVersion>>8, file.ExternalAttrs>>16&0xf000
		if (isDir && (mode&fs.ModeType != fs.ModeDir || file.UncompressedSize64 != 0)) ||
			(!isDir && !mode.IsRegular()) || file.ExternalAttrs&8 != 0 ||
			((host == 3 || host == 19) && unixType != 0 && unixType != 0x8000 && unixType != 0x4000) {
			return nil, fmt.Errorf("special ZIP member: %w", zip.ErrFormat)
		}
		names[name] = isDir
		if file.Flags&(1|0x40|0x2000) != 0 || (file.Method != zip.Store && file.Method != zip.Deflate) {
			return nil, fmt.Errorf("encrypted or unsupported ZIP member: %w", zip.ErrAlgorithm)
		}
		if file.UncompressedSize64 > archiveMaxMember ||
			file.UncompressedSize64 > file.CompressedSize64*archiveMaxRatio {
			return nil, fmt.Errorf("ZIP member size or ratio limit exceeded: %w", zip.ErrFormat)
		}
		if file.Method == zip.Store && file.CompressedSize64 != file.UncompressedSize64 {
			return nil, zip.ErrFormat
		}
		if isDir {
			continue
		}
		archive.members = append(archive.members, archiveMember{
			info: ArchiveMember{
				ID: fmt.Sprintf("zip_%x_%x", i, digest[:16]), Name: file.Name,
				Size: int64(file.UncompressedSize64), CRC32: file.CRC32,
			},
			file: file, offset: offsets[i],
		})
	}
	orderedNames := make([]string, 0, len(names))
	for name := range names {
		orderedNames = append(orderedNames, name)
	}
	sort.Strings(orderedNames)
	for name, isDir := range names {
		if isDir {
			continue
		}
		prefix := name + "/"
		index := sort.SearchStrings(orderedNames, prefix)
		if index < len(orderedNames) && strings.HasPrefix(orderedNames[index], prefix) {
			return nil, fmt.Errorf("ambiguous ZIP file/directory name: %w", zip.ErrFormat)
		}
	}
	return archive, nil
}

func safeArchiveName(name string) bool {
	if name == "" || name == "." || !utf8.ValidString(name) || strings.Contains(name, "\\") ||
		path.IsAbs(name) || path.Clean(name) != name || strings.Contains(strings.SplitN(name, "/", 2)[0], ":") {
		return false
	}
	for _, part := range strings.Split(name, "/") {
		if part == ".." || strings.IndexFunc(part, unicode.IsControl) >= 0 {
			return false
		}
	}
	return true
}

func checkArchiveDirectory(ctx context.Context, directory []byte, count int, directoryOffset int64) ([]int64, error) {
	offsets := make([]int64, 0, count)
	names := 0
	for len(directory) > 0 {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if len(offsets) >= count || len(directory) < archiveHeaderLen ||
			binary.LittleEndian.Uint32(directory) != 0x02014b50 {
			return nil, zip.ErrFormat
		}
		nameLen := int(binary.LittleEndian.Uint16(directory[28:]))
		extraLen := int(binary.LittleEndian.Uint16(directory[30:]))
		commentLen := int(binary.LittleEndian.Uint16(directory[32:]))
		length := archiveHeaderLen + nameLen + extraLen + commentLen
		names += nameLen
		offset := int64(binary.LittleEndian.Uint32(directory[42:]))
		if length > len(directory) || names > archiveMaxNames || offset+30 > directoryOffset ||
			binary.LittleEndian.Uint16(directory[34:]) != 0 || offset == 0xffffffff ||
			binary.LittleEndian.Uint32(directory[20:]) == 0xffffffff ||
			binary.LittleEndian.Uint32(directory[24:]) == 0xffffffff {
			return nil, fmt.Errorf("invalid or excessive ZIP directory entry: %w", zip.ErrFormat)
		}
		if err := checkArchiveExtra(directory[archiveHeaderLen+nameLen : archiveHeaderLen+nameLen+extraLen]); err != nil {
			return nil, err
		}
		offsets = append(offsets, offset)
		directory = directory[length:]
	}
	if len(offsets) != count {
		return nil, zip.ErrFormat
	}
	return offsets, nil
}

func checkArchiveExtra(extra []byte) error {
	for len(extra) > 0 {
		if len(extra) < 4 {
			return zip.ErrFormat
		}
		tag := binary.LittleEndian.Uint16(extra)
		length := 4 + int(binary.LittleEndian.Uint16(extra[2:]))
		if tag == 1 || length > len(extra) {
			return fmt.Errorf("ZIP64 or malformed extra field: %w", zip.ErrFormat)
		}
		extra = extra[length:]
	}
	return nil
}

func (a *safeArchive) checkLocalHeader(member archiveMember) error {
	var header [30]byte
	if _, err := a.reader.raw.ReadAt(header[:], member.offset); err != nil {
		return err
	}
	nameLen := int64(binary.LittleEndian.Uint16(header[26:]))
	extraLen := int64(binary.LittleEndian.Uint16(header[28:]))
	body := member.offset + 30 + nameLen + extraLen
	if binary.LittleEndian.Uint32(header[:]) != 0x04034b50 ||
		binary.LittleEndian.Uint16(header[6:]) != member.file.Flags ||
		binary.LittleEndian.Uint16(header[8:]) != member.file.Method ||
		nameLen != int64(len(member.file.Name)) || body > a.reader.offset ||
		int64(member.file.CompressedSize64) > a.reader.offset-body {
		return zip.ErrFormat
	}
	data := make([]byte, nameLen+extraLen)
	if _, err := a.reader.raw.ReadAt(data, member.offset+30); err != nil {
		return err
	}
	if string(data[:nameLen]) != member.file.Name {
		return zip.ErrFormat
	}
	if err := checkArchiveExtra(data[nameLen:]); err != nil {
		return err
	}
	if member.file.Flags&8 == 0 && (binary.LittleEndian.Uint32(header[14:]) != member.info.CRC32 ||
		binary.LittleEndian.Uint32(header[18:]) != uint32(member.file.CompressedSize64) ||
		binary.LittleEndian.Uint32(header[22:]) != uint32(member.info.Size)) {
		return zip.ErrFormat
	}
	return nil
}

type archiveContextReaderAt struct {
	ctx    context.Context
	reader io.ReaderAt
}

func (r archiveContextReaderAt) ReadAt(p []byte, offset int64) (int, error) {
	total := 0
	for len(p) > 0 {
		if err := r.ctx.Err(); err != nil {
			return total, err
		}
		n, err := r.reader.ReadAt(p[:min(len(p), archiveReadChunk)], offset)
		total += n
		p, offset = p[n:], offset+int64(n)
		if cancel := r.ctx.Err(); cancel != nil {
			return total, cancel
		}
		if err != nil {
			return total, err
		}
		if n == 0 {
			return total, io.ErrNoProgress
		}
	}
	return total, nil
}

type archiveReaderAt struct {
	raw      archiveContextReaderAt
	offset   int64
	metadata []byte
}

func (r *archiveReaderAt) ReadAt(p []byte, offset int64) (int, error) {
	if err := r.raw.ctx.Err(); err != nil {
		return 0, err
	}
	if offset < 0 {
		return 0, os.ErrInvalid
	}
	total := 0
	if offset < r.offset {
		n := int(min(int64(len(p)), r.offset-offset))
		read, err := r.raw.ReadAt(p[:n], offset)
		total += read
		p, offset = p[read:], offset+int64(read)
		if err != nil {
			return total, err
		}
	}
	if len(p) == 0 {
		return total, nil
	}
	index := offset - r.offset
	if index >= int64(len(r.metadata)) {
		return total, io.EOF
	}
	n := copy(p, r.metadata[index:])
	if n != len(p) {
		return total + n, io.EOF
	}
	return total + n, nil
}

type archiveMemberReader struct {
	mu          sync.Mutex
	ctx         context.Context
	reader      io.ReadCloser
	remaining   int64
	crc         hash.Hash32
	expectedCRC uint32
	terminal    error
	closed      bool
}

func (r *archiveMemberReader) Read(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return 0, fs.ErrClosed
	}
	if r.terminal != nil {
		return 0, r.terminal
	}
	if err := r.ctx.Err(); err != nil {
		r.terminal = err
		_ = r.reader.Close()
		return 0, err
	}
	if len(p) == 0 {
		return 0, nil
	}
	p = p[:min(int64(len(p)), archiveReadChunk, r.remaining+1)]
	n, err := r.reader.Read(p)
	if cancel := r.ctx.Err(); cancel != nil {
		err = cancel
	}
	if int64(n) > r.remaining {
		n, err = 0, zip.ErrFormat
	}
	r.remaining -= int64(n)
	_, _ = r.crc.Write(p[:n])
	if err == io.EOF {
		if r.remaining != 0 {
			err = io.ErrUnexpectedEOF
		} else if r.crc.Sum32() != r.expectedCRC {
			err = zip.ErrChecksum
		}
	}
	if err != nil {
		r.terminal = err
		_ = r.reader.Close()
	}
	return n, err
}

func (r *archiveMemberReader) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil
	}
	r.closed = true
	if r.terminal != nil {
		return nil
	}
	return r.reader.Close()
}
