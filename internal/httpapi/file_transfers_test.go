package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"loom.local/loom/internal/filetransfer"
	"loom.local/loom/internal/ids"
	"loom.local/loom/internal/response"
	"loom.local/loom/internal/storagecatalog"
)

func TestFileTransferHTTPChunkUploadResumeAndComplete(t *testing.T) {
	server, catalog, _, acceptedRoot := newFileTransferTestServer(t)
	payload := []byte("helloworld")
	status := createHTTPFileTransfer(t, server.URL, filetransfer.Manifest{
		SourceNodeKey:          "macbook",
		SourceRootKey:          "loom_box__documents",
		SourceRelativePath:     "Reports/report.txt",
		DestinationLogicalPath: "Reports/report.txt",
		TransferKind:           filetransfer.KindWatchedRootBackup,
		CustodyMode:            filetransfer.CustodyModeBackupCopy,
		FileSizeBytes:          int64(len(payload)),
		ChecksumAlgorithm:      filetransfer.ChecksumSHA256,
		ChecksumHex:            filetransfer.SHA256Hex(payload),
		ChunkSizeBytes:         5,
	})
	transferID := status.Manifest.TransferID
	if len(status.Chunks) != 2 || !reflect.DeepEqual(status.MissingChunks, []int64{0, 1}) {
		t.Fatalf("unexpected initial transfer status: %#v", status)
	}

	putHTTPFileTransferChunk(t, server.URL, transferID, 1, payload[5:], true)
	status = getHTTPFileTransfer(t, server.URL, transferID)
	if !reflect.DeepEqual(status.MissingChunks, []int64{0}) {
		t.Fatalf("missing chunks after out-of-order upload = %#v, want [0]", status.MissingChunks)
	}
	if entries, _ := os.ReadDir(acceptedRoot); len(entries) != 0 {
		t.Fatalf("accepted root should stay empty before completion, got %d entries", len(entries))
	}

	putHTTPFileTransferChunk(t, server.URL, transferID, 0, payload[:5], true)
	complete := completeHTTPFileTransfer(t, server.URL, transferID)
	if complete.Manifest.Status != filetransfer.StatusAccepted {
		t.Fatalf("transfer status = %s, want accepted", complete.Manifest.Status)
	}
	if complete.StorageEntryID == "" || complete.StoragePhysicalRefID == "" {
		t.Fatalf("complete result did not include storage refs: %#v", complete)
	}
	acceptedPath := filepath.Join(acceptedRoot, filepath.FromSlash(complete.AcceptedPath))
	raw, err := os.ReadFile(acceptedPath)
	if err != nil {
		t.Fatalf("read accepted file: %v", err)
	}
	if string(raw) != string(payload) {
		t.Fatalf("accepted payload = %q, want %q", raw, payload)
	}
	if len(catalog.entries) != 1 {
		t.Fatalf("catalog entries = %d, want 1", len(catalog.entries))
	}
	entry := catalog.entries[0]
	if entry.SourceArea != storagecatalog.SourceAreaDocuments || entry.StorageClass != storagecatalog.StorageClassPrivateBackup {
		t.Fatalf("catalog classification = %s/%s, want documents/private_backup", entry.SourceArea, entry.StorageClass)
	}
	wantViewPath := "backups/macbook/loom_box__documents/" + transferID + "/payload/Reports/report.txt"
	if entry.CurrentViewPath != wantViewPath {
		t.Fatalf("current view path = %q", entry.CurrentViewPath)
	}
	if strings.Contains(entry.CurrentViewPath, "_System/Transfers") {
		t.Fatalf("watched-root catalog entry retained synthetic transfer path: %q", entry.CurrentViewPath)
	}
	wantPhysicalRef := filepath.ToSlash(filepath.Join(acceptedRoot, filepath.FromSlash(complete.AcceptedPath)))
	if got := catalog.refs[0].URI; got != wantPhysicalRef {
		t.Fatalf("physical ref URI = %q, want exact accepted path %q", got, wantPhysicalRef)
	}
}

func TestFileTransferHTTPRejectsMissingChecksumAndUnknownTransfer(t *testing.T) {
	server, _, _, acceptedRoot := newFileTransferTestServer(t)
	payload := []byte("helloworld")
	status := createHTTPFileTransfer(t, server.URL, filetransfer.Manifest{
		SourceNodeKey:          "macbook",
		SourceRootKey:          "loom_box__documents",
		SourceRelativePath:     "Reports/partial.txt",
		DestinationLogicalPath: "Reports/partial.txt",
		TransferKind:           filetransfer.KindWatchedRootBackup,
		CustodyMode:            filetransfer.CustodyModeBackupCopy,
		FileSizeBytes:          int64(len(payload)),
		ChecksumAlgorithm:      filetransfer.ChecksumSHA256,
		ChecksumHex:            filetransfer.SHA256Hex(payload),
		ChunkSizeBytes:         5,
	})
	transferID := status.Manifest.TransferID

	req, err := http.NewRequest(http.MethodPut, server.URL+"/v1/file-transfers/"+transferID+"/chunks/0", bytes.NewReader(payload[:5]))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set(fileTransferChecksumAlgorithmHeader, filetransfer.ChecksumSHA256)
	req.Header.Set(fileTransferChecksumHexHeader, "000000")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("bad checksum request: %v", err)
	}
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad checksum status = %d, want 400", resp.StatusCode)
	}
	_ = resp.Body.Close()

	putHTTPFileTransferChunk(t, server.URL, transferID, 0, payload[:5], true)
	resp, err = http.Post(server.URL+"/v1/file-transfers/"+transferID+"/complete", "application/json", bytes.NewReader([]byte(`{}`)))
	if err != nil {
		t.Fatalf("complete partial request: %v", err)
	}
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("partial complete status = %d, want 400", resp.StatusCode)
	}
	_ = resp.Body.Close()
	if entries, _ := os.ReadDir(acceptedRoot); len(entries) != 0 {
		t.Fatalf("accepted root should stay empty after partial completion attempt, got %d entries", len(entries))
	}

	unknownID := filetransfer.NewTransferID()
	resp, err = http.Get(server.URL + "/v1/file-transfers/" + unknownID)
	if err != nil {
		t.Fatalf("unknown transfer request: %v", err)
	}
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown transfer status = %d, want 404", resp.StatusCode)
	}
	_ = resp.Body.Close()
}

func TestFileTransferHTTPCreateIsIdempotentByManifestKey(t *testing.T) {
	server, _, _, _ := newFileTransferTestServer(t)
	payload := []byte("idempotent watched-root transfer")
	manifest := filetransfer.Manifest{
		SourceNodeKey:          "macbook",
		SourceRootKey:          "loom_box__documents",
		SourceRelativePath:     "Videos/clip.mov",
		DestinationLogicalPath: "Videos/clip.mov",
		TransferKind:           filetransfer.KindWatchedRootBackup,
		CustodyMode:            filetransfer.CustodyModeBackupCopy,
		FileSizeBytes:          int64(len(payload)),
		ChecksumAlgorithm:      filetransfer.ChecksumSHA256,
		ChecksumHex:            filetransfer.SHA256Hex(payload),
		ChunkSizeBytes:         5,
	}
	manifest.IdempotencyKey = filetransfer.IdempotencyKey(manifest)

	first := createHTTPFileTransfer(t, server.URL, manifest)
	second := createHTTPFileTransfer(t, server.URL, manifest)

	if first.Manifest.TransferID != second.Manifest.TransferID {
		t.Fatalf("duplicate create returned transfer_id %s, want %s", second.Manifest.TransferID, first.Manifest.TransferID)
	}
	if second.Manifest.TransferKind != filetransfer.KindWatchedRootBackup || second.Manifest.CustodyMode != filetransfer.CustodyModeBackupCopy {
		t.Fatalf("unexpected idempotent manifest: %#v", second.Manifest)
	}
}

func TestFileTransferHTTPListFiltersTransfers(t *testing.T) {
	server, _, _, _ := newFileTransferTestServer(t)
	createHTTPFileTransfer(t, server.URL, filetransfer.Manifest{
		IdempotencyKey:         "list-manual",
		SourceNodeKey:          "macbook",
		SourceRootKey:          "manual",
		SourceRelativePath:     "send/video.mov",
		DestinationLogicalPath: "send/video.mov",
		TransferKind:           filetransfer.KindManualUpload,
		CustodyMode:            filetransfer.CustodyModeMainOwned,
		FileSizeBytes:          0,
		ChunkSizeBytes:         8,
	})
	createHTTPFileTransfer(t, server.URL, filetransfer.Manifest{
		IdempotencyKey:         "list-documents",
		SourceNodeKey:          "macbook",
		SourceRootKey:          "documents",
		SourceRelativePath:     "report.md",
		DestinationLogicalPath: "report.md",
		TransferKind:           filetransfer.KindWatchedRootBackup,
		CustodyMode:            filetransfer.CustodyModeBackupCopy,
		FileSizeBytes:          0,
		ChunkSizeBytes:         8,
	})

	resp, err := http.Get(server.URL + "/v1/file-transfers?transfer_kind=" + filetransfer.KindManualUpload + "&source_node_key=macbook&limit=5")
	if err != nil {
		t.Fatalf("list transfer request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("list transfer status = %d: %s", resp.StatusCode, body)
	}
	envelope := decodeHTTPEnvelope[[]filetransfer.Status](t, resp)
	if len(envelope.Data) != 1 || envelope.Data[0].Manifest.TransferKind != filetransfer.KindManualUpload {
		t.Fatalf("unexpected transfer list: %#v", envelope.Data)
	}
}

func TestFileTransferHTTPRejectsNewDropzoneCustodyWithoutWriting(t *testing.T) {
	server, catalog, acceptedRoot, _ := newFileTransferTestServer(t)
	payload := []byte("dropzone custody through file transfers")
	raw, err := json.Marshal(filetransfer.Manifest{
		SourceNodeKey:          "macbook",
		SourceRootKey:          "loom_box__dropzone",
		SourceRelativePath:     "Videos/clip.mov",
		DestinationLogicalPath: "Videos/clip.mov",
		TransferKind:           filetransfer.KindDropzoneCustody,
		CustodyMode:            filetransfer.CustodyModeCustodyTransfer,
		FileSizeBytes:          int64(len(payload)),
		ChecksumAlgorithm:      filetransfer.ChecksumSHA256,
		ChecksumHex:            filetransfer.SHA256Hex(payload),
		ChunkSizeBytes:         10,
		DropzoneTransferID:     "drop_slice08_catalog",
	})
	if err != nil {
		t.Fatalf("marshal Dropzone manifest: %v", err)
	}
	resp, err := http.Post(server.URL+"/v1/file-transfers", "application/json", bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("post Dropzone transfer: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusGone {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("Dropzone create status = %d, want 410: %s", resp.StatusCode, body)
	}
	if len(catalog.entries) != 0 || len(catalog.refs) != 0 {
		t.Fatalf("retired Dropzone admission wrote catalog entries/refs = %d/%d", len(catalog.entries), len(catalog.refs))
	}
	if entries, err := os.ReadDir(acceptedRoot); !os.IsNotExist(err) && (err != nil || len(entries) != 0) {
		t.Fatalf("retired Dropzone admission wrote accepted payloads, entries=%d err=%v", len(entries), err)
	}
}

func newFileTransferTestServer(t *testing.T) (*httptest.Server, *fakeTransferCatalog, string, string) {
	t.Helper()
	root := t.TempDir()
	storageRoot := filepath.Join(root, "storage")
	backupsRoot := filepath.Join(storageRoot, "backups")
	store := newMemoryTransferStore()
	catalog := &fakeTransferCatalog{}
	service := filetransfer.NewService(store, catalog, filetransfer.ServiceConfig{
		StagingRoot:              filepath.Join(root, "staging"),
		AcceptedRoot:             filepath.Join(root, "accepted"),
		StorageRoot:              storageRoot,
		CanonicalUserBackupsRoot: backupsRoot,
		UserBackupsRoot:          backupsRoot,
		MainNodeKey:              "main",
	})
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	server := httptest.NewServer(NewServer(Services{FileTransfer: service}, logger).Handler())
	t.Cleanup(server.Close)
	return server, catalog, filepath.Join(root, "accepted"), backupsRoot
}

func createHTTPFileTransfer(t *testing.T, baseURL string, manifest filetransfer.Manifest) filetransfer.Status {
	t.Helper()
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	resp, err := http.Post(baseURL+"/v1/file-transfers", "application/json", bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("create transfer request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("create transfer status = %d: %s", resp.StatusCode, body)
	}
	return decodeHTTPEnvelope[filetransfer.Status](t, resp).Data
}

func getHTTPFileTransfer(t *testing.T, baseURL, transferID string) filetransfer.Status {
	t.Helper()
	resp, err := http.Get(baseURL + "/v1/file-transfers/" + transferID)
	if err != nil {
		t.Fatalf("get transfer request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("get transfer status = %d: %s", resp.StatusCode, body)
	}
	return decodeHTTPEnvelope[filetransfer.Status](t, resp).Data
}

func putHTTPFileTransferChunk(t *testing.T, baseURL, transferID string, index int64, payload []byte, withChecksum bool) filetransfer.UploadChunkResult {
	t.Helper()
	req, err := http.NewRequest(http.MethodPut, baseURL+"/v1/file-transfers/"+transferID+"/chunks/"+strconvFormatInt(index), bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("new chunk request: %v", err)
	}
	if withChecksum {
		req.Header.Set(fileTransferChecksumAlgorithmHeader, filetransfer.ChecksumSHA256)
		req.Header.Set(fileTransferChecksumHexHeader, filetransfer.SHA256Hex(payload))
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("chunk request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("chunk upload status = %d: %s", resp.StatusCode, body)
	}
	return decodeHTTPEnvelope[filetransfer.UploadChunkResult](t, resp).Data
}

func completeHTTPFileTransfer(t *testing.T, baseURL, transferID string) filetransfer.CompleteResult {
	t.Helper()
	resp, err := http.Post(baseURL+"/v1/file-transfers/"+transferID+"/complete", "application/json", bytes.NewReader([]byte(`{}`)))
	if err != nil {
		t.Fatalf("complete request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("complete status = %d: %s", resp.StatusCode, body)
	}
	return decodeHTTPEnvelope[filetransfer.CompleteResult](t, resp).Data
}

func decodeHTTPEnvelope[T any](t *testing.T, resp *http.Response) response.Envelope[T] {
	t.Helper()
	var envelope response.Envelope[T]
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	if !envelope.OK {
		t.Fatalf("envelope not ok: %#v", envelope)
	}
	return envelope
}

func strconvFormatInt(value int64) string {
	return strconv.FormatInt(value, 10)
}

type memoryTransferStore struct {
	mu        sync.Mutex
	manifests map[string]filetransfer.Manifest
	chunks    map[string]map[int64]filetransfer.Chunk
}

func newMemoryTransferStore() *memoryTransferStore {
	return &memoryTransferStore{
		manifests: map[string]filetransfer.Manifest{},
		chunks:    map[string]map[int64]filetransfer.Chunk{},
	}
}

func (s *memoryTransferStore) UpsertManifest(_ context.Context, manifest filetransfer.Manifest) (filetransfer.Manifest, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	manifest, err := filetransfer.NormalizeManifest(manifest, now)
	if err != nil {
		return filetransfer.Manifest{}, err
	}
	if existing, ok := s.manifests[manifest.TransferID]; ok {
		manifest.CreatedAt = existing.CreatedAt
	}
	s.manifests[manifest.TransferID] = manifest
	return manifest, nil
}

func (s *memoryTransferStore) GetManifest(_ context.Context, transferID string) (filetransfer.Manifest, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	manifest, ok := s.manifests[transferID]
	if !ok {
		return filetransfer.Manifest{}, filetransfer.ErrNotFound
	}
	return manifest, nil
}

func (s *memoryTransferStore) GetManifestByIdempotencyKey(_ context.Context, idempotencyKey string) (filetransfer.Manifest, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, manifest := range s.manifests {
		if manifest.IdempotencyKey == idempotencyKey && idempotencyKey != "" {
			return manifest, nil
		}
	}
	return filetransfer.Manifest{}, filetransfer.ErrNotFound
}

func (s *memoryTransferStore) ListManifests(_ context.Context, filter filetransfer.ListFilter) ([]filetransfer.Manifest, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	limit := filter.Limit
	if limit <= 0 {
		limit = 100
	}
	manifests := make([]filetransfer.Manifest, 0, len(s.manifests))
	for _, manifest := range s.manifests {
		if filter.Status != "" && manifest.Status != filter.Status {
			continue
		}
		if filter.TransferKind != "" && manifest.TransferKind != filter.TransferKind {
			continue
		}
		if filter.SourceNodeKey != "" && manifest.SourceNodeKey != filter.SourceNodeKey {
			continue
		}
		if filter.SourceRootKey != "" && manifest.SourceRootKey != filter.SourceRootKey {
			continue
		}
		manifests = append(manifests, manifest)
	}
	sort.Slice(manifests, func(i, j int) bool {
		return manifests[i].UpdatedAt.After(manifests[j].UpdatedAt)
	})
	if len(manifests) > limit {
		manifests = manifests[:limit]
	}
	return manifests, nil
}

func (s *memoryTransferStore) UpsertChunk(_ context.Context, chunk filetransfer.Chunk) (filetransfer.Chunk, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	manifest, ok := s.manifests[chunk.TransferID]
	if !ok {
		return filetransfer.Chunk{}, filetransfer.ErrNotFound
	}
	chunk, err := filetransfer.NormalizeChunk(manifest, chunk, time.Now().UTC())
	if err != nil {
		return filetransfer.Chunk{}, err
	}
	if _, ok := s.chunks[chunk.TransferID]; !ok {
		s.chunks[chunk.TransferID] = map[int64]filetransfer.Chunk{}
	}
	if existing, ok := s.chunks[chunk.TransferID][chunk.Index]; ok {
		chunk.ChunkID = existing.ChunkID
		chunk.CreatedAt = existing.CreatedAt
	}
	s.chunks[chunk.TransferID][chunk.Index] = chunk
	return chunk, nil
}

func (s *memoryTransferStore) ListChunks(_ context.Context, transferID string) ([]filetransfer.Chunk, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.manifests[transferID]; !ok {
		return nil, filetransfer.ErrNotFound
	}
	items := s.chunks[transferID]
	chunks := make([]filetransfer.Chunk, 0, len(items))
	for _, chunk := range items {
		chunks = append(chunks, chunk)
	}
	return chunks, nil
}

type fakeTransferCatalog struct {
	entries []storagecatalog.RegisterEntryInput
	refs    []storagecatalog.RegisterPhysicalRefInput
}

func (c *fakeTransferCatalog) RegisterEntry(_ context.Context, input storagecatalog.RegisterEntryInput) (storagecatalog.Entry, error) {
	if input.StorageEntryID == "" {
		input.StorageEntryID = ids.NewStorageEntryID()
	}
	c.entries = append(c.entries, input)
	return storagecatalog.Entry{
		StorageEntryID:     input.StorageEntryID,
		StorageClass:       input.StorageClass,
		SourceArea:         input.SourceArea,
		OriginNodeKey:      input.OriginNodeKey,
		LogicalPath:        input.LogicalPath,
		OriginalSourcePath: input.OriginalSourcePath,
		CurrentViewPath:    input.CurrentViewPath,
		ChecksumAlgorithm:  input.ChecksumAlgorithm,
		ChecksumHex:        input.ChecksumHex,
		SizeBytes:          input.SizeBytes,
		FileClass:          input.FileClass,
		ProcessingState:    input.ProcessingState,
		AvailabilityState:  input.AvailabilityState,
		RetentionState:     input.RetentionState,
		Metadata:           input.Metadata,
	}, nil
}

func (c *fakeTransferCatalog) RegisterPhysicalRef(_ context.Context, input storagecatalog.RegisterPhysicalRefInput) (storagecatalog.PhysicalRef, error) {
	if input.StoragePhysicalRefID == "" {
		input.StoragePhysicalRefID = ids.NewStoragePhysicalRefID()
	}
	c.refs = append(c.refs, input)
	return storagecatalog.PhysicalRef{
		StoragePhysicalRefID: input.StoragePhysicalRefID,
		StorageEntryID:       input.StorageEntryID,
		RefKind:              input.RefKind,
		URI:                  input.URI,
		NodeKey:              input.NodeKey,
		ContentAddress:       input.ContentAddress,
		Status:               input.Status,
		Metadata:             input.Metadata,
	}, nil
}
