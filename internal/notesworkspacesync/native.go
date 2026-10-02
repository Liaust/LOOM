package notesworkspacesync

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
)

// Native is a trusted local IPC client, never an HTTP endpoint or raw CouchDB API.
type Native interface {
	Call(context.Context, map[string]any, any) error
}

// JSONLClient serializes bounded protocol-1 requests over an already-owned child
// process. Runtime owns starting/terminating the native process and private config.
// A cancelled/failed stream is poisoned: restart it rather than mispairing replies.
type JSONLClient struct {
	Input    io.Writer
	Output   io.Reader
	mu       sync.Mutex
	reader   *bufio.Reader
	seq      uint64
	poisoned bool
}

func (c *JSONLClient) Call(ctx context.Context, request map[string]any, out any) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.poisoned {
		return errors.New("native IPC restart required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if c.reader == nil {
		c.reader = bufio.NewReaderSize(c.Output, 64*1024)
	}
	c.seq++
	id := c.seq
	requestCopy := make(map[string]any, len(request)+1)
	for k, v := range request {
		requestCopy[k] = v
	}
	requestCopy["protocol"] = 1
	raw, err := json.Marshal(struct {
		ID      uint64 `json:"id"`
		Request any    `json:"request"`
	}{id, requestCopy})
	limit := 2 * MaxBytes
	if request["method"] == "publish.reference" {
		limit = 2 * MaxReferenceBytes
	}
	if err != nil || len(raw) > limit {
		return ErrHeld
	}
	// Keep all I/O in the same operation. Cancellation does not permit reusing
	// this stream while its old read or write may still be completing.
	type response struct {
		raw []byte
		err error
	}
	done := make(chan response, 1)
	go func() {
		if _, e := c.Input.Write(append(raw, '\n')); e != nil {
			done <- response{err: e}
			return
		}
		var line []byte
		for {
			part, e := c.reader.ReadSlice('\n')
			if len(line)+len(part) > 2*MaxBytes {
				done <- response{err: ErrHeld}
				return
			}
			line = append(line, part...)
			if e == bufio.ErrBufferFull {
				continue
			}
			if e != nil {
				done <- response{err: e}
				return
			}
			break
		}
		var envelope struct {
			ID     uint64          `json:"id"`
			Result json.RawMessage `json:"result"`
		}
		if e := json.Unmarshal(line, &envelope); e != nil || envelope.ID != id {
			done <- response{err: errors.New("invalid native IPC reply")}
			return
		}
		var status struct{ Status, Code, Reason string }
		if e := json.Unmarshal(envelope.Result, &status); e != nil {
			done <- response{err: e}
			return
		}
		if status.Status == "error" {
			done <- response{err: errors.New("native request failed")}
			return
		}
		// Decode into private data first; cancellation never mutates a caller's out.
		done <- response{raw: envelope.Result}
	}()
	select {
	case reply := <-done:
		if reply.err != nil {
			c.poisoned = true
			return reply.err
		}
		return json.Unmarshal(reply.raw, out)
	case <-ctx.Done():
		c.poisoned = true
		return ctx.Err()
	}
}

type NativeStatus struct {
	Status              string `json:"status"`
	Epoch               string `json:"epoch"`
	EncryptionEnabled   bool   `json:"encryptionEnabled"`
	ClientIntentVersion int    `json:"clientIntentVersion"`
}
type Evidence struct {
	ID                     string   `json:"id"`
	Revision               string   `json:"revision"`
	Path                   string   `json:"path"`
	Kind                   string   `json:"kind"`
	Ancestors              []string `json:"ancestors"`
	AncestryAvailable      bool     `json:"ancestryAvailable"`
	AncestryCompleteToRoot bool     `json:"ancestryCompleteToRoot"`
	LogicalDeleted         bool     `json:"logicalDeleted"`
	BranchDeleted          bool     `json:"branchDeleted"`
	Status                 string   `json:"status"`
	Reason                 string   `json:"reason"`
	SHA256                 string   `json:"sha256"`
	Content                []byte   `json:"contentBase64"`
}
type NativeChanges struct {
	Status  string          `json:"status"`
	Next    json.RawMessage `json:"nextSequence"`
	Changes []struct {
		ID     string     `json:"id"`
		Leaves []Evidence `json:"leaves"`
	} `json:"changes"`
}

func (s *Service) call(ctx context.Context, method string, args map[string]any, out any) error {
	if args == nil {
		args = map[string]any{}
	}
	args["method"] = method
	args["epoch"] = s.Epoch
	return s.Native.Call(ctx, args, out)
}
func (s *Service) ready(ctx context.Context) error {
	var status NativeStatus
	if err := s.call(ctx, "status", nil, &status); err != nil {
		return err
	}
	if status.Status != "ready" || status.Epoch != s.Epoch || !status.EncryptionEnabled || status.ClientIntentVersion != 1 {
		return fmt.Errorf("%w: native_encryption_epoch_or_protocol", ErrHeld)
	}
	return nil
}
