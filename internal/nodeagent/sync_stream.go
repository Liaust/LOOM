package nodeagent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"

	"golang.org/x/sys/unix"
	"loom.local/loom/internal/correlation"
	"loom.local/loom/internal/idempotency"
	"loom.local/loom/internal/response"
	loomsync "loom.local/loom/internal/sync"
)

// Retain only a classification prefix while hashing the complete source.
func inspectSyncFile(path string) ([]byte, os.FileInfo, string, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, nil, "", err
	}
	defer f.Close()
	before, err := f.Stat()
	if err != nil {
		return nil, nil, "", err
	}
	if !before.Mode().IsRegular() || before.Size() > loomsync.MaxStreamObjectUploadBytes {
		return nil, nil, "", fmt.Errorf("source must be a regular file no larger than %d bytes", loomsync.MaxStreamObjectUploadBytes)
	}
	sample := make([]byte, 512)
	n, err := io.ReadFull(f, sample)
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		return nil, nil, "", err
	}
	sample = sample[:n]
	h := sha256.New()
	h.Write(sample)
	rest, err := io.Copy(h, io.LimitReader(f, before.Size()+1-int64(n)))
	if err != nil {
		return nil, nil, "", err
	}
	after, err := f.Stat()
	if err != nil {
		return nil, nil, "", err
	}
	named, err := os.Lstat(path)
	if err != nil {
		return nil, nil, "", err
	}
	if int64(n)+rest != before.Size() || !os.SameFile(before, named) || fileChangedDuringRead(before, after) {
		return nil, nil, "", fmt.Errorf("source changed during sync inspection")
	}
	return sample, after, fmt.Sprintf("sha256:%x", h.Sum(nil)), nil
}

func uploadQueuedSyncObject(ctx context.Context, c Client, correlationID string, state State, item LocalSyncOutboxItem, object LocalSyncObject) (response.Envelope[loomsync.SyncedObjectResult], error) {
	if object.SizeBytes <= loomsync.MaxInlineObjectUploadBytes {
		input, err := buildSyncedObjectInput(state, item, object)
		if err != nil {
			return response.Envelope[loomsync.SyncedObjectResult]{}, err
		}
		return c.UploadSyncedObject(ctx, correlationID, input.IdempotencyKey, input)
	}
	var out response.Envelope[loomsync.SyncedObjectResult]
	path := object.ContentPath
	if path == "" {
		path = object.SourcePath
	}
	sample, info, hash, err := inspectSyncFile(path)
	if err != nil {
		return out, err
	}
	if hash != object.HashURI || info.Size() != object.SizeBytes {
		return out, fmt.Errorf("queued source content changed before upload")
	}
	f, err := os.OpenFile(path, os.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return out, err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil {
		return out, err
	}
	if !opened.Mode().IsRegular() || !os.SameFile(info, opened) || fileChangedDuringRead(info, opened) {
		return out, fmt.Errorf("queued source identity changed before upload")
	}
	input := syncedObjectMetadata(state, item, object, path, sample, info)
	return c.UploadSyncedObjectStream(ctx, correlationID, input.IdempotencyKey, input, f)
}

func (c Client) UploadSyncedObjectStream(ctx context.Context, correlationID, key string, input loomsync.SyncedObjectInput, content io.Reader) (response.Envelope[loomsync.SyncedObjectResult], error) {
	var out response.Envelope[loomsync.SyncedObjectResult]
	if input.ContentBase64 != "" || input.SizeBytes < 0 || input.SizeBytes > loomsync.MaxStreamObjectUploadBytes {
		return out, fmt.Errorf("invalid streaming upload metadata")
	}
	raw, err := json.Marshal(input)
	if err != nil {
		return out, err
	}
	if len(raw) > loomsync.MaxObjectUploadMetadataBytes {
		return out, fmt.Errorf("upload metadata is too large")
	}
	var framing bytes.Buffer
	m := multipart.NewWriter(&framing)
	p, err := m.CreateFormField("metadata")
	if err != nil {
		return out, err
	}
	if _, err = p.Write(raw); err != nil {
		return out, err
	}
	if _, err = m.CreateFormFile("content", "object"); err != nil {
		return out, err
	}
	prefixLen := framing.Len()
	if err = m.Close(); err != nil {
		return out, err
	}
	data := framing.Bytes()
	body := io.MultiReader(bytes.NewReader(data[:prefixLen]), io.LimitReader(content, input.SizeBytes+1), bytes.NewReader(data[prefixLen:]))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/v1/node-agent/sync/object-upload", body)
	if err != nil {
		return out, err
	}
	req.Header.Set("Content-Type", m.FormDataContentType())
	req.Header.Set("Accept", "application/json")
	req.Header.Set(correlation.Header, correlation.Normalize(correlationID))
	req.Header.Set(idempotency.Header, key)
	client := withFileTransferTimeout(c).HTTPClient
	resp, err := client.Do(req)
	if err != nil {
		return out, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var e response.ErrorEnvelope
		_ = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&e)
		return out, RemoteRequestError{StatusCode: resp.StatusCode, Method: req.Method, Path: req.URL.Path, Envelope: e}
	}
	err = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out)
	return out, err
}
