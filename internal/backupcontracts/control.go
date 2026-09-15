package backupcontracts

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"loom.local/loom/internal/communication"
	noderuntime "loom.local/loom/internal/nodeagent/runtime"
)

const (
	ProtectedFolderControlSchemaVersion   = "loom.backup.protected-folder.control.v1"
	ProtectedFolderPreflightResultVersion = "loom.backup.protected-folder.preflight-result.v1"

	MessageKindProtectedFolderPreflight = communication.KindProtectedFolderPreflight
	MessageKindProtectedFolderReconcile = communication.KindProtectedFolderReconcile

	DefaultPreflightMaxEntries  = 100_000
	DefaultPreflightMaxBytes    = int64(100 * 1024 * 1024 * 1024)
	DefaultPreflightMaxDuration = 30 * time.Second
)

type PreflightBudget struct {
	MaxEntries        int   `json:"max_entries"`
	MaxApparentBytes  int64 `json:"max_apparent_bytes"`
	MaxDurationMillis int64 `json:"max_duration_millis"`
}

type ProtectedFolderPreflightPayload struct {
	SchemaVersion string                          `json:"schema_version"`
	PreflightID   string                          `json:"preflight_id"`
	TargetNode    string                          `json:"target_node"`
	RequestedPath string                          `json:"requested_path"`
	Recheck       *ProtectedFolderRecheckIdentity `json:"recheck,omitempty"`
	Ignore        IgnorePolicy                    `json:"ignore"`
	Include       []string                        `json:"include,omitempty"`
	Exclude       []string                        `json:"exclude,omitempty"`
	Budget        PreflightBudget                 `json:"budget"`
	ExpiresAt     time.Time                       `json:"expires_at"`
}

// ProtectedFolderRecheckIdentity is a bounded identity for the one managed
// safe-root/runtime pair that an existing contract may overlap during recheck.
// It contains no path and cannot designate an arbitrary runtime resource.
type ProtectedFolderRecheckIdentity struct {
	ContractKey string `json:"contract_key"`
	RootKey     string `json:"root_key"`
	SafeRootKey string `json:"safe_root_key"`
	WorkerKey   string `json:"worker_key"`
}

func RecheckIdentityForContractKey(contractKey string) ProtectedFolderRecheckIdentity {
	rootKey := BackendRootKey(contractKey)
	return ProtectedFolderRecheckIdentity{
		ContractKey: contractKey,
		RootKey:     rootKey,
		SafeRootKey: SafeRootKey(contractKey),
		WorkerKey:   noderuntime.WatchedRootWorkerKey(rootKey),
	}
}

func (i ProtectedFolderRecheckIdentity) Validate() error {
	if err := ValidateKey(i.ContractKey); err != nil {
		return fmt.Errorf("protected-folder recheck contract_key: %w", err)
	}
	expected := RecheckIdentityForContractKey(i.ContractKey)
	if i != expected {
		return fmt.Errorf("protected-folder recheck identity does not match its contract key")
	}
	return nil
}

type ProtectedFolderReconcilePayload struct {
	SchemaVersion string                             `json:"schema_version"`
	Evidence      communication.DesiredStateEvidence `json:"evidence"`
	Roots         []ProtectedFolderDesiredRoot       `json:"roots"`
}

type ProtectedFolderDesiredRoot struct {
	ContractKey string          `json:"contract_key"`
	RootKey     string          `json:"root_key"`
	SafeRootKey string          `json:"safe_root_key"`
	DisplayName string          `json:"display_name"`
	TargetPath  string          `json:"target_path"`
	Enabled     bool            `json:"enabled"`
	ConfigHash  string          `json:"config_hash"`
	ConfigJSON  json.RawMessage `json:"config_json"`
	Backup      BackupPolicy    `json:"backup"`
	Ignore      IgnorePolicy    `json:"ignore"`
	Include     []string        `json:"include,omitempty"`
	Exclude     []string        `json:"exclude,omitempty"`
}

type ProtectedFolderAck struct {
	SchemaVersion string                             `json:"schema_version"`
	Evidence      communication.AppliedStateEvidence `json:"evidence"`
	Roots         []ProtectedFolderRootAck           `json:"roots,omitempty"`
	ErrorCode     string                             `json:"error_code,omitempty"`
	ErrorMessage  string                             `json:"error_message,omitempty"`
}

type ProtectedFolderRootAck struct {
	ContractKey string `json:"contract_key"`
	ConfigHash  string `json:"config_hash"`
	Applied     bool   `json:"applied"`
	ErrorCode   string `json:"error_code,omitempty"`
}

func DefaultPreflightBudget() PreflightBudget {
	return PreflightBudget{
		MaxEntries:        DefaultPreflightMaxEntries,
		MaxApparentBytes:  DefaultPreflightMaxBytes,
		MaxDurationMillis: DefaultPreflightMaxDuration.Milliseconds(),
	}
}

func (p ProtectedFolderPreflightPayload) Validate() error {
	if p.SchemaVersion != ProtectedFolderControlSchemaVersion {
		return fmt.Errorf("protected-folder preflight schema_version must be %q", ProtectedFolderControlSchemaVersion)
	}
	if strings.TrimSpace(p.PreflightID) == "" || strings.TrimSpace(p.TargetNode) == "" {
		return fmt.Errorf("protected-folder preflight id and target node are required")
	}
	if strings.TrimSpace(p.RequestedPath) == "" {
		return fmt.Errorf("protected-folder preflight requested path is required")
	}
	if p.Recheck != nil {
		if err := p.Recheck.Validate(); err != nil {
			return err
		}
	}
	if p.Budget.MaxEntries <= 0 || p.Budget.MaxEntries > 1_000_000 {
		return fmt.Errorf("protected-folder preflight max_entries is outside the bounded range")
	}
	if p.Budget.MaxApparentBytes <= 0 || p.Budget.MaxApparentBytes > 10*DefaultPreflightMaxBytes {
		return fmt.Errorf("protected-folder preflight max_apparent_bytes is outside the bounded range")
	}
	if p.Budget.MaxDurationMillis <= 0 || p.Budget.MaxDurationMillis > (5*time.Minute).Milliseconds() {
		return fmt.Errorf("protected-folder preflight max_duration_millis is outside the bounded range")
	}
	if p.ExpiresAt.IsZero() {
		return fmt.Errorf("protected-folder preflight expires_at is required")
	}
	return nil
}

func (p ProtectedFolderReconcilePayload) Validate() error {
	if p.SchemaVersion != ProtectedFolderControlSchemaVersion {
		return fmt.Errorf("protected-folder reconcile schema_version must be %q", ProtectedFolderControlSchemaVersion)
	}
	if err := p.Evidence.Validate(); err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, root := range p.Roots {
		if err := ValidateKey(root.ContractKey); err != nil {
			return err
		}
		if seen[root.ContractKey] {
			return fmt.Errorf("duplicate protected-folder contract key %q", root.ContractKey)
		}
		seen[root.ContractKey] = true
		if strings.TrimSpace(root.TargetPath) == "" || strings.TrimSpace(root.ConfigHash) == "" {
			return fmt.Errorf("protected-folder root %q requires target_path and config_hash", root.ContractKey)
		}
		if strings.TrimSpace(root.RootKey) == "" || strings.TrimSpace(root.SafeRootKey) == "" {
			return fmt.Errorf("protected-folder root %q requires root_key and safe_root_key", root.ContractKey)
		}
		if err := communication.DecodeStrictJSONObject(root.ConfigJSON, &map[string]any{}); err != nil {
			return fmt.Errorf("protected-folder root %q config_json: %w", root.ContractKey, err)
		}
	}
	return nil
}

func (a ProtectedFolderAck) Validate() error {
	if a.SchemaVersion != ProtectedFolderControlSchemaVersion {
		return fmt.Errorf("protected-folder acknowledgement schema_version must be %q", ProtectedFolderControlSchemaVersion)
	}
	if err := a.Evidence.Validate(); err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, root := range a.Roots {
		if err := ValidateKey(root.ContractKey); err != nil {
			return err
		}
		if seen[root.ContractKey] {
			return fmt.Errorf("duplicate protected-folder acknowledgement root %q", root.ContractKey)
		}
		seen[root.ContractKey] = true
	}
	return nil
}

func DecodeProtectedFolderAck(payload json.RawMessage) (ProtectedFolderAck, error) {
	var result ProtectedFolderAck
	if err := communication.DecodeStrictJSONObject(payload, &result); err != nil {
		return result, err
	}
	return result, result.Validate()
}

func DecodePreflightPayload(payload json.RawMessage) (ProtectedFolderPreflightPayload, error) {
	var result ProtectedFolderPreflightPayload
	if err := communication.DecodeStrictJSONObject(payload, &result); err != nil {
		return result, err
	}
	return result, result.Validate()
}

func DecodeReconcilePayload(payload json.RawMessage) (ProtectedFolderReconcilePayload, error) {
	var result ProtectedFolderReconcilePayload
	if err := communication.DecodeStrictJSONObject(payload, &result); err != nil {
		return result, err
	}
	return result, result.Validate()
}
