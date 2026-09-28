package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"

	"loom.local/loom/internal/objects"
	"loom.local/loom/internal/requestctx"
	"loom.local/loom/internal/response"
	loomsync "loom.local/loom/internal/sync"
)

func (s Server) handleNodeAgentSyncObjectStream(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	r.Body = http.MaxBytesReader(w, r.Body, loomsync.MaxStreamObjectUploadBytes+loomsync.MaxObjectUploadMetadataBytes+64*1024)
	input, content, err := decodeSyncedObjectStream(r)
	if err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "request.invalid_stream", "sync", "body", "Expected bounded metadata followed by file content.", err)
		return
	}
	req, err := requestctx.ResolveBootstrap(ctx, s.services.DB, correlationID)
	if err != nil {
		s.writeError(w, correlationID, http.StatusInternalServerError, "request.context_failed", "sync", "bootstrap", "Could not resolve request context.", err)
		return
	}
	result, err := s.services.Sync.IngestSyncedObjectStream(ctx, req, s.services.Objects, s.services.Search, input, content)
	if err != nil {
		s.writeSyncObjectError(w, correlationID, input.LocalObjectRef, err)
		return
	}
	response.WriteJSON(w, http.StatusCreated, response.Success(correlationID, result))
}

func (s Server) writeSyncObjectError(w http.ResponseWriter, correlationID, ref string, err error) {
	if errors.Is(err, objects.ErrProjectNotFound) {
		s.writeError(w, correlationID, http.StatusUnprocessableEntity, "sync.project_not_found", "sync", ref, "The queued object's project does not exist. Repair the project binding before explicitly retrying the retained queue item.", err)
		return
	}
	s.writeError(w, correlationID, http.StatusBadRequest, "sync.object_upload_failed", "sync", ref, "Could not ingest synced object.", err)
}

func decodeSyncedObjectStream(r *http.Request) (loomsync.SyncedObjectInput, io.Reader, error) {
	var input loomsync.SyncedObjectInput
	m, err := r.MultipartReader()
	if err != nil {
		return input, nil, err
	}
	p, err := m.NextRawPart()
	if err != nil || p.FormName() != "metadata" || p.FileName() != "" {
		return input, nil, fmt.Errorf("metadata must be the first multipart field")
	}
	limited := &io.LimitedReader{R: p, N: loomsync.MaxObjectUploadMetadataBytes + 1}
	d := json.NewDecoder(limited)
	d.DisallowUnknownFields()
	if err := d.Decode(&input); err != nil {
		return input, nil, err
	}
	if err := d.Decode(new(any)); err != io.EOF || limited.N <= 0 || input.ContentBase64 != "" {
		return input, nil, fmt.Errorf("invalid or oversized stream metadata")
	}
	p, err = m.NextRawPart()
	if err != nil || p.FormName() != "content" {
		return input, nil, fmt.Errorf("content must follow metadata")
	}
	return input, &syncedMultipartContent{part: p, reader: m}, nil
}

type syncedMultipartContent struct {
	part   *multipart.Part
	reader *multipart.Reader
}

func (r *syncedMultipartContent) Read(p []byte) (int, error) {
	n, err := r.part.Read(p)
	if err == io.EOF {
		if _, nextErr := r.reader.NextRawPart(); nextErr != io.EOF {
			return n, fmt.Errorf("unexpected trailing multipart content")
		}
	}
	return n, err
}
