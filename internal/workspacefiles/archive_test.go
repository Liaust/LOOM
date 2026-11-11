package workspacefiles

import (
	"archive/zip"
	"bytes"
	"compress/flate"
	"context"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func archiveTestZIP(t *testing.T, headers ...zip.FileHeader) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	for _, header := range headers {
		member, err := writer.CreateHeader(&header)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasSuffix(header.Name, "/") {
			if _, err := io.WriteString(member, "payload"); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func archiveTestRawZIP(t *testing.T, header zip.FileHeader, body []byte) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	member, err := writer.CreateRaw(&header)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := member.Write(body); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func archiveTestFile(t *testing.T, data []byte) *os.File {
	t.Helper()
	name := filepath.Join(t.TempDir(), "members.zip")
	if err := os.WriteFile(name, data, 0600); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(name)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = file.Close() })
	return file
}

func TestArchiveMembersListAndOpen(t *testing.T) {
	ctx := context.Background()
	file := archiveTestFile(t, archiveTestZIP(t,
		zip.FileHeader{Name: "notes/"},
		zip.FileHeader{Name: "notes/paper.pdf", Method: zip.Deflate},
		zip.FileHeader{Name: "notes/\u00e9tude.txt", Method: zip.Store},
	))
	if _, err := file.Seek(3, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	members, err := ListArchiveMembers(ctx, file)
	if err != nil || len(members) != 2 {
		t.Fatalf("members: %+v, %v", members, err)
	}
	again, err := ListArchiveMembers(ctx, file)
	if err != nil || !reflect.DeepEqual(members, again) || members[0].ID == members[1].ID {
		t.Fatalf("unstable IDs: %+v, %v", again, err)
	}
	for _, member := range members {
		if member.Size != 7 || member.CRC32 != crc32.ChecksumIEEE([]byte("payload")) || member.ID == member.Name {
			t.Fatalf("metadata: %+v", member)
		}
		reader, opened, err := OpenArchiveMember(ctx, file, member.ID)
		if err != nil || opened != member {
			t.Fatalf("open: %+v, %v", opened, err)
		}
		body, err := io.ReadAll(reader)
		if err != nil || string(body) != "payload" {
			t.Fatalf("body: %q, %v", body, err)
		}
		if err := reader.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if offset, err := file.Seek(0, io.SeekCurrent); err != nil || offset != 3 {
		t.Fatalf("caller offset changed: %d, %v", offset, err)
	}
	for _, id := range []string{members[0].Name, "../../etc/passwd", members[0].ID + "x"} {
		if reader, _, err := OpenArchiveMember(ctx, file, id); !errors.Is(err, fs.ErrNotExist) || reader != nil {
			t.Fatalf("unknown ID %q: %v", id, err)
		}
	}
	changed := archiveTestFile(t, archiveTestZIP(t, zip.FileHeader{Name: "other.pdf"}))
	if _, _, err := OpenArchiveMember(ctx, changed, members[0].ID); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("ID accepted for changed metadata: %v", err)
	}
}

func TestArchiveRejectsUnsafeMembers(t *testing.T) {
	for _, name := range []string{
		"../outside", "/absolute", "C:/drive", "C:drive", `folder\file`,
		"a/../file", "./file", "a//file", "a/./file", "a/../../file", "a\x00b", "a\nb", "",
	} {
		t.Run(name, func(t *testing.T) {
			file := archiveTestFile(t, archiveTestZIP(t, zip.FileHeader{Name: name}))
			if _, err := ListArchiveMembers(context.Background(), file); err == nil {
				t.Fatal("unsafe name accepted")
			}
		})
	}
	for _, names := range [][]string{
		{"same", "same"}, {"folder", "folder/"}, {"folder", "folder/child"},
		{"folder/child", "folder", "folder-ignored"},
	} {
		t.Run(strings.Join(names, ","), func(t *testing.T) {
			var headers []zip.FileHeader
			for _, name := range names {
				headers = append(headers, zip.FileHeader{Name: name})
			}
			file := archiveTestFile(t, archiveTestZIP(t, headers...))
			if _, err := ListArchiveMembers(context.Background(), file); err == nil {
				t.Fatal("ambiguous names accepted")
			}
		})
	}
	for _, header := range []zip.FileHeader{
		{Name: "link", CreatorVersion: 3 << 8, ExternalAttrs: 0xa000 << 16},
		{Name: "link/", CreatorVersion: 3 << 8, ExternalAttrs: 0xa000 << 16},
		{Name: "pipe", CreatorVersion: 3 << 8, ExternalAttrs: 0x1000 << 16},
		{Name: "device", CreatorVersion: 3 << 8, ExternalAttrs: 0x6000 << 16},
		{Name: "socket", CreatorVersion: 3 << 8, ExternalAttrs: 0xc000 << 16},
		{Name: "unknown", CreatorVersion: 3 << 8, ExternalAttrs: 0xf000 << 16},
		{Name: "volume", ExternalAttrs: 8},
	} {
		file := archiveTestFile(t, archiveTestZIP(t, header))
		if _, err := ListArchiveMembers(context.Background(), file); err == nil {
			t.Fatalf("special member %s accepted", header.Name)
		}
	}
}

func TestArchiveRejectsUnboundedOrUnsupportedMetadata(t *testing.T) {
	valid := archiveTestZIP(t, zip.FileHeader{Name: "paper.pdf"})
	for _, test := range []struct {
		name   string
		mutate func(data, directory, end []byte)
	}{
		{"too many entries", func(_, _, end []byte) { binary.LittleEndian.PutUint16(end[10:], 4097) }},
		{"large directory", func(_, _, end []byte) { binary.LittleEndian.PutUint32(end[12:], archiveMaxDirectory+1) }},
		{"ZIP64 count", func(_, _, end []byte) { binary.LittleEndian.PutUint16(end[10:], 0xffff) }},
		{"ZIP64 directory size", func(_, _, end []byte) { binary.LittleEndian.PutUint32(end[12:], 0xffffffff) }},
		{"ZIP64 directory offset", func(_, _, end []byte) { binary.LittleEndian.PutUint32(end[16:], 0xffffffff) }},
		{"ZIP64 member", func(_, directory, _ []byte) { binary.LittleEndian.PutUint32(directory[24:], 0xffffffff) }},
		{"split archive", func(_, _, end []byte) { binary.LittleEndian.PutUint16(end[4:], 1) }},
		{"wrong count", func(_, _, end []byte) { binary.LittleEndian.PutUint16(end[10:], 0) }},
		{"wrong extent", func(_, _, end []byte) { binary.LittleEndian.PutUint32(end[16:], 0) }},
		{"encrypted", func(_, directory, _ []byte) { directory[8] |= 1 }},
		{"strong encryption", func(_, directory, _ []byte) { directory[8] |= 0x40 }},
		{"masked headers", func(_, directory, _ []byte) { directory[9] |= 0x20 }},
		{"unsupported method", func(_, directory, _ []byte) { binary.LittleEndian.PutUint16(directory[10:], 99) }},
		{"oversize", func(_, directory, _ []byte) { binary.LittleEndian.PutUint32(directory[24:], archiveMaxMember+1) }},
		{"ratio", func(_, directory, _ []byte) { binary.LittleEndian.PutUint32(directory[24:], 1401) }},
		{"zero compressed", func(_, directory, _ []byte) { binary.LittleEndian.PutUint32(directory[20:], 0) }},
		{"local offset", func(_, directory, _ []byte) { binary.LittleEndian.PutUint32(directory[42:], 0xffffffff) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			data := bytes.Clone(valid)
			end := data[len(data)-archiveEndLen:]
			directory := data[binary.LittleEndian.Uint32(end[16:]):]
			test.mutate(data, directory, end)
			if _, err := ListArchiveMembers(context.Background(), archiveTestFile(t, data)); err == nil {
				t.Fatal("invalid metadata accepted")
			}
		})
	}
	for _, extra := range [][]byte{{1, 0, 0, 0}, {2, 0, 10, 0}, {2}} {
		data := archiveTestZIP(t, zip.FileHeader{Name: "extra", Extra: extra})
		if _, err := ListArchiveMembers(context.Background(), archiveTestFile(t, data)); err == nil {
			t.Fatalf("ZIP64/malformed extra accepted: %x", extra)
		}
	}
	for _, data := range [][]byte{valid[:len(valid)-1], append(bytes.Clone(valid), 0), valid[:10]} {
		if _, err := ListArchiveMembers(context.Background(), archiveTestFile(t, data)); err == nil {
			t.Fatal("truncated/trailing archive accepted")
		}
	}
}

func TestArchiveValidatesDecodedSizeAndCRC(t *testing.T) {
	body := []byte("payload")
	var compressed bytes.Buffer
	deflater, err := flate.NewWriter(&compressed, flate.DefaultCompression)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := deflater.Write(body); err != nil {
		t.Fatal(err)
	}
	if err := deflater.Close(); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name   string
		method uint16
		data   []byte
		size   uint64
		crc    uint32
		want   error
	}{
		{"CRC", zip.Store, body, 7, 123, zip.ErrChecksum},
		{"zero CRC", zip.Store, body, 7, 0, zip.ErrChecksum},
		{"too short", zip.Deflate, compressed.Bytes(), 8, crc32.ChecksumIEEE(body), io.ErrUnexpectedEOF},
		{"too long", zip.Deflate, compressed.Bytes(), 6, crc32.ChecksumIEEE(body), zip.ErrFormat},
		{"truncated deflate", zip.Deflate, compressed.Bytes()[:2], 7, crc32.ChecksumIEEE(body), io.ErrUnexpectedEOF},
	} {
		t.Run(test.name, func(t *testing.T) {
			header := zip.FileHeader{Name: "paper.pdf", Method: test.method, CRC32: test.crc,
				CompressedSize64: uint64(len(test.data)), UncompressedSize64: test.size}
			file := archiveTestFile(t, archiveTestRawZIP(t, header, test.data))
			members, err := ListArchiveMembers(context.Background(), file)
			if err != nil {
				t.Fatal(err)
			}
			reader, _, err := OpenArchiveMember(context.Background(), file, members[0].ID)
			if err != nil {
				t.Fatal(err)
			}
			defer reader.Close()
			decoded, err := io.ReadAll(reader)
			if !errors.Is(err, test.want) || uint64(len(decoded)) > test.size {
				t.Fatalf("read %d bytes, error %v; want %v", len(decoded), err, test.want)
			}
		})
	}
}

func TestArchiveCancellationAndEarlyClose(t *testing.T) {
	file := archiveTestFile(t, archiveTestZIP(t, zip.FileHeader{Name: "paper.pdf", Method: zip.Deflate}))
	ctx, cancel := context.WithCancel(context.Background())
	members, err := ListArchiveMembers(ctx, file)
	if err != nil {
		t.Fatal(err)
	}
	reader, _, err := OpenArchiveMember(ctx, file, members[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reader.Read(make([]byte, 1)); err != nil {
		t.Fatal(err)
	}
	cancel()
	if n, err := reader.Read(make([]byte, 64)); n != 0 || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled read: %d, %v", n, err)
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := ListArchiveMembers(ctx, file); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled listing: %v", err)
	}
	if _, _, err := OpenArchiveMember(ctx, file, members[0].ID); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled open: %v", err)
	}
	reader, _, err = OpenArchiveMember(context.Background(), file, members[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := reader.Read(make([]byte, 1)); !errors.Is(err, fs.ErrClosed) {
		t.Fatalf("read after close: %v", err)
	}
	if _, err := file.Stat(); err != nil {
		t.Fatalf("caller descriptor closed: %v", err)
	}
}

func TestArchiveEmptyAndNestedMembers(t *testing.T) {
	ctx := context.Background()
	if members, err := ListArchiveMembers(ctx, archiveTestFile(t, archiveTestZIP(t))); err != nil || len(members) != 0 {
		t.Fatalf("empty archive: %+v, %v", members, err)
	}
	inner := archiveTestZIP(t, zip.FileHeader{Name: "paper.pdf"})
	header := zip.FileHeader{Name: "nested.zip", Method: zip.Store, CRC32: crc32.ChecksumIEEE(inner),
		CompressedSize64: uint64(len(inner)), UncompressedSize64: uint64(len(inner))}
	file := archiveTestFile(t, archiveTestRawZIP(t, header, inner))
	members, err := ListArchiveMembers(ctx, file)
	if err != nil || len(members) != 1 || members[0].Name != "nested.zip" {
		t.Fatalf("nested archive: %+v, %v", members, err)
	}
	reader, _, err := OpenArchiveMember(ctx, file, members[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	data, err := io.ReadAll(reader)
	if err != nil || !bytes.Equal(data, inner) {
		t.Fatalf("nested bytes changed: %v", err)
	}
}

func TestArchiveSelectedLocalHeaderMustMatch(t *testing.T) {
	valid := archiveTestRawZIP(t, zip.FileHeader{Name: "paper.pdf", Method: zip.Store,
		CompressedSize64: 7, UncompressedSize64: 7, CRC32: crc32.ChecksumIEEE([]byte("payload"))}, []byte("payload"))
	for _, offset := range []int{0, 6, 8, 14, 18, 22, 26, 28, 30} {
		data := bytes.Clone(valid)
		data[offset] ^= 1
		file := archiveTestFile(t, data)
		members, err := ListArchiveMembers(context.Background(), file)
		if err != nil {
			t.Fatalf("listing should not scan local headers: %v", err)
		}
		if reader, _, err := OpenArchiveMember(context.Background(), file, members[0].ID); err == nil || reader != nil {
			t.Fatalf("local mismatch at %d accepted", offset)
		}
	}
}

func TestArchiveCommentAndEmptyMember(t *testing.T) {
	data := archiveTestRawZIP(t, zip.FileHeader{Name: "empty", Method: zip.Store}, nil)
	comment := []byte("comment with PK\x05\x06 and trailing bytes")
	binary.LittleEndian.PutUint16(data[len(data)-2:], uint16(len(comment)))
	data = append(data, comment...)
	file := archiveTestFile(t, data)
	members, err := ListArchiveMembers(context.Background(), file)
	if err != nil || len(members) != 1 || members[0].Size != 0 {
		t.Fatalf("comment/empty member: %+v, %v", members, err)
	}
	reader, _, err := OpenArchiveMember(context.Background(), file, members[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	if body, err := io.ReadAll(reader); err != nil || len(body) != 0 {
		t.Fatalf("empty read: %q, %v", body, err)
	}
}
