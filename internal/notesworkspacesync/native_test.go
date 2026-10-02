package notesworkspacesync

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestJSONLCancellationPoisonsStream(t *testing.T) {
	inputReader, inputWriter := io.Pipe()
	outputReader, outputWriter := io.Pipe()
	defer inputReader.Close()
	defer inputWriter.Close()
	defer outputReader.Close()
	defer outputWriter.Close()
	go func() { _, _ = io.Copy(io.Discard, inputReader) }()
	c := &JSONLClient{Input: inputWriter, Output: outputReader}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	var out map[string]any
	if e := c.Call(ctx, map[string]any{"method": "status"}, &out); e == nil {
		t.Fatal("expected cancellation")
	}
	if e := c.Call(context.Background(), nil, &out); e == nil || !strings.Contains(e.Error(), "restart") {
		t.Fatal("cancelled stream reused")
	}
}
func TestNativeIPCBridgeToActualSource(t *testing.T) {
	upstream := os.Getenv("LOOM_SYNC_UPSTREAM")
	if upstream == "" {
		t.Skip("set LOOM_SYNC_UPSTREAM to the prepared native build; no service is created")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "node", "--input-type=module", "-e", `import {createNewVaultSettings} from './node_modules/@vrtmrz/livesync-commonlib/dist/settings.js';process.stdout.write(JSON.stringify({...createNewVaultSettings(),isConfigured:false,useIndexedDBAdapter:false,encrypt:true,passphrase:'ephemeral-bridge-test-only',liveSync:false,syncOnStart:false,periodicReplication:false,syncOnSave:false,syncOnEditorSave:false,syncOnFileOpen:false,syncAfterMerge:false,deleteMetadataOfDeletedFiles:false,automaticallyDeleteMetadataOfDeletedFiles:0}));`)
	cmd.Dir = upstream
	settings, err := cmd.Output()
	if err != nil {
		t.Fatal("native settings fixture unavailable")
	}
	temp := t.TempDir()
	config := filepath.Join(temp, "settings.json")
	if err = os.WriteFile(config, settings, 0600); err != nil {
		t.Fatal(err)
	}
	replica := filepath.Join(temp, "replica")
	if err = os.Mkdir(replica, 0700); err != nil {
		t.Fatal(err)
	}
	child := exec.CommandContext(ctx, "node", filepath.Join(upstream, "src/apps/cli/dist/index.cjs"), replica, "--settings", config)
	input, err := child.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	output, err := child.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = child.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { input.Close(); child.Wait() }()
	native := &JSONLClient{Input: input, Output: output}
	var status NativeStatus
	if err = native.Call(ctx, map[string]any{"method": "status"}, &status); err != nil {
		t.Fatal(err)
	}
	s, _, source, root, _ := fixture(t)
	s.Native = native
	s.Epoch = status.Epoch
	s.Store = &memoryRecords{data: map[string]json.RawMessage{}}
	var sourceFile, sourceBase string
	for _, b := range source.bases {
		sourceFile = b.FileID
		sourceBase = b.ID
		break
	}
	b, err := s.Export(ctx, "c", sourceFile, sourceBase)
	if err != nil {
		t.Fatal(err)
	}
	// Publish an exact child through actual native revision machinery, then client
	// intent/publication controls through the same encrypted native note pipeline.
	var content Evidence
	if err = s.call(ctx, "publish", map[string]any{"operationId": "device_edit", "path": b.Path, "baseRevision": b.NativeRevision, "contentBase64": base64.StdEncoding.EncodeToString([]byte("B"))}, &content); err != nil {
		t.Fatal(err)
	}
	if content.Status != "published" {
		t.Fatalf("native publish %s", content.Status)
	}
	i, p := operation(b, "client_edit", "B", content.Revision)
	for _, v := range []any{p, i} {
		raw, _ := marshalControl(v)
		var result Evidence
		if err = s.call(ctx, "control.put", map[string]any{"contentBase64": raw}, &result); err != nil || result.Status != "published" {
			t.Fatalf("control publication %v / %s", err, result.Status)
		}
	}
	for range 4 {
		if _, err = s.Step(ctx, 128); err != nil {
			t.Fatal(err)
		}
	}
	bytes, err := os.ReadFile(filepath.Join(root, "a.md"))
	if err != nil || string(bytes) != "B" {
		t.Fatalf("source was not updated: %s / %v", bytes, err)
	}
	var join Join
	if err = s.Store.WithReplica(ctx, s.Replica, s.Epoch, func(r Records) error { return r.Get(ctx, "operation", i.ID, &join) }); err != nil {
		t.Fatal(err)
	}
	if !join.AckPublished || join.Ack == nil || join.Ack.Status != "applied" {
		t.Fatalf("source acknowledgement not published: %+v", join)
	}
	var page NativeChanges
	if err = s.call(ctx, "changes", map[string]any{"since": 0, "limit": 128}, &page); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, change := range page.Changes {
		for _, leaf := range change.Leaves {
			if leaf.Path == controlPath(join.Ack.Header) {
				var exact Evidence
				if err = s.call(ctx, "control.read", map[string]any{"id": leaf.ID, "revision": leaf.Revision}, &exact); err != nil {
					t.Fatal(err)
				}
				var ack Ack
				if json.Unmarshal(exact.Content, &ack) != nil || ack.Status != "applied" || ack.Binding.SourceBase == b.SourceBase {
					t.Fatal("invalid returned acknowledgement")
				}
				found = true
			}
		}
	}
	if !found {
		t.Fatal("missing native acknowledgement")
	}
	if _, err = os.Stat(filepath.Join(replica, "LOOM-Control-v1")); !os.IsNotExist(err) {
		t.Fatal("control reflected to filesystem")
	}
}
