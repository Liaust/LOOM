package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"loom.local/loom/internal/filetransfer"
	"loom.local/loom/internal/response"
)

const (
	fileTransferChecksumAlgorithmHeader = "X-LOOM-Checksum-Algorithm"
	fileTransferChecksumHexHeader       = "X-LOOM-Checksum-Hex"
)

func (s Server) handleFileTransfers(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	switch r.Method {
	case http.MethodGet:
		filter, err := fileTransferFilterFromRequest(r)
		if err != nil {
			s.writeError(w, correlationID, http.StatusBadRequest, "file_transfer.invalid_filter", "file_transfer", "filter", "File transfer filter is invalid.", err)
			return
		}
		statuses, err := s.services.FileTransfer.List(ctx, filter)
		if err != nil {
			s.writeFileTransferError(w, correlationID, "file_transfers", "file_transfer.list_failed", "Could not list file transfers.", err)
			return
		}
		response.WriteJSON(w, http.StatusOK, response.Success(correlationID, statuses))
	case http.MethodPost:
		var input filetransfer.Manifest
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&input); err != nil && !errors.Is(err, io.EOF) {
			s.writeError(w, correlationID, http.StatusBadRequest, "request.invalid_json", "file_transfer", "body", "Request body is not valid file-transfer manifest JSON.", err)
			return
		}
		status, err := s.services.FileTransfer.Initiate(ctx, input)
		if err != nil {
			s.writeFileTransferError(w, correlationID, input.TransferID, "file_transfer.create_failed", "Could not create file transfer.", err)
			return
		}
		response.WriteJSON(w, http.StatusCreated, response.Success(correlationID, status))
	default:
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "file_transfer", r.URL.Path, "Method is not allowed.", nil)
	}
}

func fileTransferFilterFromRequest(r *http.Request) (filetransfer.ListFilter, error) {
	query := r.URL.Query()
	filter := filetransfer.ListFilter{
		Status:        strings.TrimSpace(query.Get("status")),
		TransferKind:  strings.TrimSpace(query.Get("transfer_kind")),
		SourceNodeKey: strings.TrimSpace(query.Get("source_node_key")),
		SourceRootKey: strings.TrimSpace(query.Get("source_root_key")),
		Limit:         100,
	}
	if raw := strings.TrimSpace(query.Get("limit")); raw != "" {
		limit, err := strconv.Atoi(raw)
		if err != nil {
			return filetransfer.ListFilter{}, err
		}
		if limit < 0 {
			return filetransfer.ListFilter{}, errors.New("limit must be non-negative")
		}
		filter.Limit = limit
	}
	return filter, nil
}

func (s Server) handleFileTransfer(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	ref := pathRef(r.URL.Path, "/v1/file-transfers/")
	transferID, action, subref := splitFileTransferAction(ref)
	if transferID == "" {
		s.writeError(w, correlationID, http.StatusNotFound, "file_transfer.ref_required", "file_transfer", "file_transfer", "File transfer reference is required.", nil)
		return
	}
	switch {
	case action == "":
		if r.Method != http.MethodGet {
			s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "file_transfer", r.URL.Path, "Method is not allowed.", nil)
			return
		}
		status, err := s.services.FileTransfer.Get(ctx, transferID)
		if err != nil {
			s.writeFileTransferError(w, correlationID, transferID, "file_transfer.get_failed", "Could not inspect file transfer.", err)
			return
		}
		response.WriteJSON(w, http.StatusOK, response.Success(correlationID, status))
	case action == "chunks":
		if r.Method != http.MethodPut {
			s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "file_transfer", r.URL.Path, "Method is not allowed.", nil)
			return
		}
		index, err := strconv.ParseInt(subref, 10, 64)
		if err != nil || index < 0 {
			s.writeError(w, correlationID, http.StatusBadRequest, "file_transfer.chunk_index_invalid", "file_transfer", subref, "Chunk index must be a non-negative integer.", err)
			return
		}
		body := http.MaxBytesReader(w, r.Body, filetransfer.MaxChunkSizeBytes+1)
		payload, err := io.ReadAll(body)
		if err != nil {
			s.writeError(w, correlationID, http.StatusBadRequest, "file_transfer.chunk_read_failed", "file_transfer", transferID, "Could not read file-transfer chunk body.", err)
			return
		}
		if int64(len(payload)) > filetransfer.MaxChunkSizeBytes {
			s.writeError(w, correlationID, http.StatusBadRequest, "file_transfer.chunk_too_large", "file_transfer", transferID, "Chunk exceeds the maximum supported chunk size.", nil)
			return
		}
		result, err := s.services.FileTransfer.UploadChunk(ctx, filetransfer.UploadChunkInput{
			TransferID:        transferID,
			Index:             index,
			Payload:           payload,
			ChecksumAlgorithm: firstQuery(r.Header.Get(fileTransferChecksumAlgorithmHeader), r.URL.Query().Get("checksum_algorithm")),
			ChecksumHex:       firstQuery(r.Header.Get(fileTransferChecksumHexHeader), r.URL.Query().Get("checksum_hex")),
		})
		if err != nil {
			s.writeFileTransferError(w, correlationID, transferID, "file_transfer.chunk_upload_failed", "Could not accept file-transfer chunk.", err)
			return
		}
		response.WriteJSON(w, http.StatusOK, response.Success(correlationID, result))
	case action == "complete":
		if r.Method != http.MethodPost {
			s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "file_transfer", r.URL.Path, "Method is not allowed.", nil)
			return
		}
		result, err := s.services.FileTransfer.Complete(ctx, transferID)
		if err != nil {
			s.writeFileTransferError(w, correlationID, transferID, "file_transfer.complete_failed", "Could not complete file transfer.", err)
			return
		}
		response.WriteJSON(w, http.StatusOK, response.Success(correlationID, result))
	case action == "abort":
		if r.Method != http.MethodPost {
			s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "file_transfer", r.URL.Path, "Method is not allowed.", nil)
			return
		}
		var input filetransfer.AbortInput
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&input); err != nil && !errors.Is(err, io.EOF) {
			s.writeError(w, correlationID, http.StatusBadRequest, "request.invalid_json", "file_transfer", "body", "Request body is not valid file-transfer abort JSON.", err)
			return
		}
		input.TransferID = transferID
		status, err := s.services.FileTransfer.Abort(ctx, input)
		if err != nil {
			s.writeFileTransferError(w, correlationID, transferID, "file_transfer.abort_failed", "Could not abort file transfer.", err)
			return
		}
		response.WriteJSON(w, http.StatusOK, response.Success(correlationID, status))
	default:
		s.writeError(w, correlationID, http.StatusNotFound, "file_transfer.action_unknown", "file_transfer", action, "Unknown file-transfer action.", nil)
	}
}

func splitFileTransferAction(ref string) (string, string, string) {
	ref = strings.Trim(ref, "/")
	if ref == "" {
		return "", "", ""
	}
	parts := strings.Split(ref, "/")
	transferID := strings.TrimSpace(parts[0])
	if len(parts) == 1 {
		return transferID, "", ""
	}
	action := strings.TrimSpace(parts[1])
	if action == "chunks" && len(parts) >= 3 {
		return transferID, action, strings.TrimSpace(parts[2])
	}
	return transferID, action, ""
}

func (s Server) writeFileTransferError(w http.ResponseWriter, correlationID, target, code, summary string, err error) {
	status := http.StatusBadRequest
	if errors.Is(err, filetransfer.ErrTransferRetired) {
		status = http.StatusGone
		code = "dropzone.runtime_retired"
		summary = "Dropzone transfer admission and mutation are retired; historical transfers remain read-only."
	} else if errors.Is(err, filetransfer.ErrNotFound) {
		status = http.StatusNotFound
		code = "file_transfer.not_found"
		summary = "File transfer was not found."
	}
	s.writeError(w, correlationID, status, code, "file_transfer", target, summary, err)
}
