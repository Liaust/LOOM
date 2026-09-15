package dropzone

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestHistoricalFixtureCorpusDecodes(t *testing.T) {
	t.Parallel()
	fixtures := []struct {
		path string
		into any
	}{
		{"testdata/historical/transfer-v0.4.2.json", &TransferRecord{}},
		{"testdata/historical/status-v0.4.2.json", &Status{}},
		{"testdata/historical/upload-session-v0.4.2.json", &UploadSession{}},
		{"testdata/historical/custody-v0.4.2.json", &CustodyRecord{}},
	}
	for _, fixture := range fixtures {
		payload, err := os.ReadFile(fixture.path)
		if err != nil {
			t.Fatalf("read %s: %v", fixture.path, err)
		}
		if err := json.Unmarshal(payload, fixture.into); err != nil {
			t.Fatalf("decode %s: %v", fixture.path, err)
		}
	}
}

func TestReaderInspectsHistoricalTransferWithoutCreatingState(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	stateRoot := filepath.Join(root, "box-state", "dropzone")
	transfersRoot := filepath.Join(stateRoot, "transfers")
	if err := os.MkdirAll(transfersRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	payload, err := os.ReadFile("testdata/historical/transfer-v0.4.2.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(transfersRoot, "drop_historical_transfer.json"), payload, 0o644); err != nil {
		t.Fatal(err)
	}

	reader := NewReader(root, DefaultPolicy("workspace"), stateRoot)
	records, err := reader.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].TransferID != "drop_historical_transfer" {
		t.Fatalf("unexpected historical records: %#v", records)
	}
	status := BuildStatus(StatusInput{RootPath: root, StateRoot: stateRoot, Profile: "workspace", Policy: DefaultPolicy("workspace")})
	if status.RuntimeState != RuntimeRetired || status.Counts[StatusAccepted] != 1 || status.PolicyEnabled {
		t.Fatalf("historical status is not bounded and retired: %#v", status)
	}
	if _, err := os.Stat(filepath.Join(root, "Dropzone")); !os.IsNotExist(err) {
		t.Fatalf("inspection created a Dropzone path: %v", err)
	}
}

func TestInspectionServiceReadsOnlyHistoricalUploadSessions(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	sessionRoot := filepath.Join(root, "dropzone", "incoming", "drop_session_historical")
	if err := os.MkdirAll(sessionRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	payload, err := os.ReadFile("testdata/historical/upload-session-v0.4.2.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sessionRoot, "session.json"), payload, 0o644); err != nil {
		t.Fatal(err)
	}

	service := NewInspectionService(filepath.Join(t.TempDir(), "box"), root)
	session, err := service.GetUploadSession(context.Background(), "drop_session_historical")
	if err != nil {
		t.Fatal(err)
	}
	if session.TransferID != "drop_historical_transfer" || session.Status != UploadSessionStatusAccepted {
		t.Fatalf("unexpected historical upload session: %#v", session)
	}
	sessions, err := service.ListUploadSessions(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 || sessions[0].SessionID != session.SessionID {
		t.Fatalf("unexpected historical sessions: %#v", sessions)
	}
}

func TestHistoricalPolicyCannotEnableRuntime(t *testing.T) {
	t.Parallel()
	policy, err := ParsePolicy([]byte("schema_version: loom.box.transfer_policy.v0.4.2\narea: dropzone\npath: Dropzone\nenabled: true\nruntime_status: active\nstatus_dir: .loom/state/dropzone\ntransfers_dir: .loom/state/dropzone/transfers\n"), "workspace")
	if err != nil {
		t.Fatal(err)
	}
	if policy.Enabled || policy.RuntimeStatus != RuntimeRetired || policy.Target != RuntimeRetired {
		t.Fatalf("historical policy re-enabled runtime: %#v", policy)
	}
}

func TestHistoricalTransferReaderRejectsOversizedJSON(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	stateRoot := filepath.Join(root, "state", "dropzone")
	transfersRoot := filepath.Join(stateRoot, "transfers")
	if err := os.MkdirAll(transfersRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(transfersRoot, "drop_oversized.json"), bytes.Repeat([]byte("x"), int(HistoricalRecordByteLimit+1)), 0o644); err != nil {
		t.Fatal(err)
	}

	reader := NewReader(root, DefaultPolicy("workspace"), stateRoot)
	if _, err := reader.Load("drop_oversized"); !errors.Is(err, ErrHistoricalRecordTooLarge) {
		t.Fatalf("Load error = %v, want ErrHistoricalRecordTooLarge", err)
	}
	if _, err := reader.List(); !errors.Is(err, ErrHistoricalRecordTooLarge) {
		t.Fatalf("List error = %v, want ErrHistoricalRecordTooLarge", err)
	}
}

func TestHistoricalUploadSessionReaderRejectsOversizedJSON(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	sessionRoot := filepath.Join(root, "dropzone", "incoming", "drop_session_oversized")
	if err := os.MkdirAll(sessionRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sessionRoot, "session.json"), bytes.Repeat([]byte("x"), int(HistoricalRecordByteLimit+1)), 0o644); err != nil {
		t.Fatal(err)
	}

	service := NewInspectionService(filepath.Join(t.TempDir(), "box"), root)
	if _, err := service.GetUploadSession(context.Background(), "drop_session_oversized"); !errors.Is(err, ErrHistoricalRecordTooLarge) {
		t.Fatalf("GetUploadSession error = %v, want ErrHistoricalRecordTooLarge", err)
	}
	if _, err := service.ListUploadSessions(context.Background(), 1); !errors.Is(err, ErrHistoricalRecordTooLarge) {
		t.Fatalf("ListUploadSessions error = %v, want ErrHistoricalRecordTooLarge", err)
	}
}

func TestHistoricalTransferListRejectsOverCapCorpusBeforeDecode(t *testing.T) {
	root := t.TempDir()
	stateRoot := filepath.Join(root, "state", "dropzone")
	transfersRoot := filepath.Join(stateRoot, "transfers")
	if err := os.MkdirAll(transfersRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	for index := 0; index <= HistoricalTransferCorpusLimit; index++ {
		if err := os.WriteFile(filepath.Join(transfersRoot, fmt.Sprintf("entry-%04d.invalid", index)), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	reader := NewReader(root, DefaultPolicy("workspace"), stateRoot)
	records, err := reader.List()
	if !errors.Is(err, ErrHistoricalCorpusLimitExceeded) {
		t.Fatalf("List error = %v, want ErrHistoricalCorpusLimitExceeded", err)
	}
	if records != nil {
		t.Fatalf("over-cap list returned a partial corpus: %#v", records)
	}
}

func TestHistoricalUploadSessionListRejectsOverCapCorpusBeforeDecode(t *testing.T) {
	root := t.TempDir()
	incomingRoot := filepath.Join(root, "dropzone", "incoming")
	if err := os.MkdirAll(incomingRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	for index := 0; index <= HistoricalUploadSessionCorpusLimit; index++ {
		if err := os.Mkdir(filepath.Join(incomingRoot, fmt.Sprintf("drop_session_%04d", index)), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	service := NewInspectionService(filepath.Join(t.TempDir(), "box"), root)
	sessions, err := service.ListUploadSessions(context.Background(), 1)
	if !errors.Is(err, ErrHistoricalCorpusLimitExceeded) {
		t.Fatalf("ListUploadSessions error = %v, want ErrHistoricalCorpusLimitExceeded", err)
	}
	if sessions != nil {
		t.Fatalf("over-cap list returned a partial corpus: %#v", sessions)
	}
}

func TestHistoricalTransferReaderRejectsSymlinkWithoutReadingExternalTarget(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	stateRoot := filepath.Join(root, "state", "dropzone")
	transfersRoot := filepath.Join(stateRoot, "transfers")
	if err := os.MkdirAll(transfersRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	external := filepath.Join(t.TempDir(), "external.json")
	if err := os.WriteFile(external, bytes.Repeat([]byte("x"), int(HistoricalRecordByteLimit+1)), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, filepath.Join(transfersRoot, "drop_symlink.json")); err != nil {
		t.Fatal(err)
	}

	reader := NewReader(root, DefaultPolicy("workspace"), stateRoot)
	for operation, err := range map[string]error{
		"Load": func() error {
			_, err := reader.Load("drop_symlink")
			return err
		}(),
		"List": func() error {
			_, err := reader.List()
			return err
		}(),
	} {
		if !errors.Is(err, ErrHistoricalUnsafePath) || errors.Is(err, ErrHistoricalRecordTooLarge) {
			t.Fatalf("%s error = %v, want unsafe path without reading oversized external target", operation, err)
		}
	}
}

func TestHistoricalUploadSessionReaderRejectsSymlinkedDirectory(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	incomingRoot := filepath.Join(root, "dropzone", "incoming")
	if err := os.MkdirAll(incomingRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	externalSessionRoot := filepath.Join(t.TempDir(), "drop_session_historical")
	if err := os.MkdirAll(externalSessionRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	writeHistoricalUploadSession(t, filepath.Join(externalSessionRoot, "session.json"), "drop_session_historical", time.Time{})
	if err := os.Symlink(externalSessionRoot, filepath.Join(incomingRoot, "drop_session_historical")); err != nil {
		t.Fatal(err)
	}

	service := NewInspectionService(filepath.Join(t.TempDir(), "box"), root)
	for operation, err := range map[string]error{
		"GetUploadSession": func() error {
			_, err := service.GetUploadSession(context.Background(), "drop_session_historical")
			return err
		}(),
		"ListUploadSessions": func() error {
			_, err := service.ListUploadSessions(context.Background(), 1)
			return err
		}(),
	} {
		if !errors.Is(err, ErrHistoricalUnsafePath) {
			t.Fatalf("%s error = %v, want ErrHistoricalUnsafePath", operation, err)
		}
	}
}

func TestHistoricalUploadSessionReaderRejectsSymlinkedFileWithoutReadingExternalTarget(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	sessionRoot := filepath.Join(root, "dropzone", "incoming", "drop_session_symlink")
	if err := os.MkdirAll(sessionRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	external := filepath.Join(t.TempDir(), "external-session.json")
	if err := os.WriteFile(external, bytes.Repeat([]byte("x"), int(HistoricalRecordByteLimit+1)), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, filepath.Join(sessionRoot, "session.json")); err != nil {
		t.Fatal(err)
	}

	service := NewInspectionService(filepath.Join(t.TempDir(), "box"), root)
	for operation, err := range map[string]error{
		"GetUploadSession": func() error {
			_, err := service.GetUploadSession(context.Background(), "drop_session_symlink")
			return err
		}(),
		"ListUploadSessions": func() error {
			_, err := service.ListUploadSessions(context.Background(), 1)
			return err
		}(),
	} {
		if !errors.Is(err, ErrHistoricalUnsafePath) || errors.Is(err, ErrHistoricalRecordTooLarge) {
			t.Fatalf("%s error = %v, want unsafe path without reading oversized external target", operation, err)
		}
	}
}

func TestHistoricalReadersBindDecodedIdentityToPath(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	stateRoot := filepath.Join(root, "state", "dropzone")
	transfersRoot := filepath.Join(stateRoot, "transfers")
	if err := os.MkdirAll(transfersRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	writeHistoricalTransfer(t, filepath.Join(transfersRoot, "drop_path_identity.json"), "drop_decoded_identity")

	reader := NewReader(root, DefaultPolicy("workspace"), stateRoot)
	if _, err := reader.Load("drop_path_identity"); !errors.Is(err, ErrHistoricalIdentityMismatch) {
		t.Fatalf("Load error = %v, want ErrHistoricalIdentityMismatch", err)
	}
	if _, err := reader.List(); !errors.Is(err, ErrHistoricalIdentityMismatch) {
		t.Fatalf("List error = %v, want ErrHistoricalIdentityMismatch", err)
	}

	sessionRuntimeRoot := t.TempDir()
	sessionRoot := filepath.Join(sessionRuntimeRoot, "dropzone", "incoming", "drop_session_path_identity")
	if err := os.MkdirAll(sessionRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	writeHistoricalUploadSession(t, filepath.Join(sessionRoot, "session.json"), "drop_session_decoded_identity", time.Time{})
	service := NewInspectionService(filepath.Join(t.TempDir(), "box"), sessionRuntimeRoot)
	if _, err := service.GetUploadSession(context.Background(), "drop_session_path_identity"); !errors.Is(err, ErrHistoricalIdentityMismatch) {
		t.Fatalf("GetUploadSession error = %v, want ErrHistoricalIdentityMismatch", err)
	}
	if _, err := service.ListUploadSessions(context.Background(), 1); !errors.Is(err, ErrHistoricalIdentityMismatch) {
		t.Fatalf("ListUploadSessions error = %v, want ErrHistoricalIdentityMismatch", err)
	}
}

func TestHistoricalTransferReaderRejectsDirectoryOutsideConfiguredStateRoot(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	stateRoot := filepath.Join(root, "state", "dropzone")
	if err := os.MkdirAll(stateRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	externalTransfersRoot := filepath.Join(t.TempDir(), "transfers")
	if err := os.MkdirAll(externalTransfersRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	writeHistoricalTransfer(t, filepath.Join(externalTransfersRoot, "drop_external.json"), "drop_external")

	reader := NewReader(root, DefaultPolicy("workspace"), stateRoot)
	reader.TransfersDir = externalTransfersRoot
	if _, err := reader.Load("drop_external"); !errors.Is(err, ErrHistoricalUnsafePath) {
		t.Fatalf("Load error = %v, want ErrHistoricalUnsafePath", err)
	}
	if _, err := reader.List(); !errors.Is(err, ErrHistoricalUnsafePath) {
		t.Fatalf("List error = %v, want ErrHistoricalUnsafePath", err)
	}
}

func TestHistoricalUploadSessionListAppliesResponseLimitAfterBoundedInspection(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	incomingRoot := filepath.Join(root, "dropzone", "incoming")
	older := filepath.Join(incomingRoot, "drop_session_older")
	newer := filepath.Join(incomingRoot, "drop_session_newer")
	if err := os.MkdirAll(older, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(newer, 0o755); err != nil {
		t.Fatal(err)
	}
	writeHistoricalUploadSession(t, filepath.Join(older, "session.json"), "drop_session_older", time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC))
	writeHistoricalUploadSession(t, filepath.Join(newer, "session.json"), "drop_session_newer", time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC))

	service := NewInspectionService(filepath.Join(t.TempDir(), "box"), root)
	sessions, err := service.ListUploadSessions(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 || sessions[0].SessionID != "drop_session_newer" {
		t.Fatalf("limited historical session list = %#v, want newest inspected session", sessions)
	}
}

func writeHistoricalTransfer(t *testing.T, path, transferID string) {
	t.Helper()
	payload, err := os.ReadFile("testdata/historical/transfer-v0.4.2.json")
	if err != nil {
		t.Fatal(err)
	}
	var record TransferRecord
	if err := json.Unmarshal(payload, &record); err != nil {
		t.Fatal(err)
	}
	record.TransferID = transferID
	payload, err = json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, payload, 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeHistoricalUploadSession(t *testing.T, path, sessionID string, updatedAt time.Time) {
	t.Helper()
	payload, err := os.ReadFile("testdata/historical/upload-session-v0.4.2.json")
	if err != nil {
		t.Fatal(err)
	}
	var session UploadSession
	if err := json.Unmarshal(payload, &session); err != nil {
		t.Fatal(err)
	}
	session.SessionID = sessionID
	if !updatedAt.IsZero() {
		session.UpdatedAt = updatedAt
	}
	payload, err = json.Marshal(session)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, payload, 0o644); err != nil {
		t.Fatal(err)
	}
}
