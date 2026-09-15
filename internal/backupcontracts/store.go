package backupcontracts

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	contractDirectoryMode os.FileMode = 0o750
	contractFileMode      os.FileMode = 0o640
)

func ContractFilePath(boxRoot string, directoryRelPath string, key string) (string, error) {
	if strings.TrimSpace(boxRoot) == "" {
		return "", fmt.Errorf("backup contract box root is required")
	}
	if err := ValidateKey(key); err != nil {
		return "", err
	}
	dirRel, err := normalizeDirectoryRelPath(directoryRelPath)
	if err != nil {
		return "", err
	}
	return filepath.Join(boxRoot, filepath.FromSlash(dirRel), key+".yaml"), nil
}

func LoadFile(path string) (Contract, []byte, error) {
	payload, err := os.ReadFile(path)
	if err != nil {
		return Contract{}, nil, err
	}
	contract, err := Parse(payload)
	if err != nil {
		return Contract{}, payload, err
	}
	return contract, payload, nil
}

func List(boxRoot string, directoryRelPath string) ([]StoredContract, error) {
	dir, err := contractsDirectoryPath(boxRoot, directoryRelPath)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return []StoredContract{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("list backup contract directory %s: %w", dir, err)
	}
	items := []StoredContract{}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".yaml" {
			continue
		}
		key := strings.TrimSuffix(entry.Name(), ".yaml")
		path := filepath.Join(dir, entry.Name())
		item := StoredContract{Key: key, Path: path}
		payload, err := os.ReadFile(path)
		if err != nil {
			item.Error = err.Error()
			items = append(items, item)
			continue
		}
		item.Raw = payload
		contract, err := Parse(payload)
		if err != nil {
			item.Error = err.Error()
			items = append(items, item)
			continue
		}
		item.Key = contract.Key
		item.Contract = contract
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool {
		return items[i].Key < items[j].Key
	})
	return items, nil
}

func Create(input MutateInput) (MutateResult, error) {
	contract := Normalize(input.Contract)
	if err := Validate(contract); err != nil {
		return MutateResult{}, err
	}
	path, err := ContractFilePath(input.BoxRoot, input.DirectoryRelPath, contract.Key)
	if err != nil {
		return MutateResult{}, err
	}
	dir := filepath.Dir(path)
	_, err = os.Stat(path)
	exists := err == nil
	if err != nil && !os.IsNotExist(err) {
		return MutateResult{}, fmt.Errorf("stat backup contract %s: %w", path, err)
	}
	if exists && !input.Replace {
		return MutateResult{}, fmt.Errorf("backup contract %q already exists", contract.Key)
	}
	stamp := timestamp(input.Now)
	if contract.Metadata.CreatedAt == "" {
		contract.Metadata.CreatedAt = stamp
	}
	if contract.Metadata.CreatedBy == "" {
		contract.Metadata.CreatedBy = actorName(input.Actor)
	}
	contract.Metadata.UpdatedAt = stamp
	payload, err := Render(contract)
	if err != nil {
		return MutateResult{}, err
	}
	result := MutateResult{
		DryRun:        input.DryRun,
		Path:          path,
		DirectoryPath: dir,
		Action:        "create",
		Contract:      contract,
		RenderedYAML:  string(payload),
		Created:       !exists,
		Updated:       exists,
	}
	if exists {
		result.Action = "update"
	}
	if input.DryRun {
		return result, nil
	}
	if err := ensureContractsDirectory(dir); err != nil {
		return result, fmt.Errorf("create backup contract directory %s: %w", dir, err)
	}
	if err := writeContractFile(path, payload); err != nil {
		return result, fmt.Errorf("write backup contract %s: %w", path, err)
	}
	return result, nil
}

func Disable(input DisableInput) (MutateResult, error) {
	path, err := ContractFilePath(input.BoxRoot, input.DirectoryRelPath, strings.ToLower(strings.TrimSpace(input.Key)))
	if err != nil {
		return MutateResult{}, err
	}
	contract, _, err := LoadFile(path)
	if err != nil {
		return MutateResult{}, fmt.Errorf("load backup contract %s: %w", path, err)
	}
	stamp := timestamp(input.Now)
	contract.Status = StatusDisabled
	contract.Metadata.UpdatedAt = stamp
	contract.Metadata.DisabledAt = stamp
	contract.Metadata.DisabledBy = actorName(input.Actor)
	payload, err := Render(contract)
	if err != nil {
		return MutateResult{}, err
	}
	result := MutateResult{
		DryRun:        input.DryRun,
		Path:          path,
		DirectoryPath: filepath.Dir(path),
		Action:        "disable",
		Contract:      contract,
		RenderedYAML:  string(payload),
		Updated:       true,
	}
	if input.DryRun {
		return result, nil
	}
	if err := writeContractFile(path, payload); err != nil {
		return result, fmt.Errorf("write disabled backup contract %s: %w", path, err)
	}
	return result, nil
}

func Enable(input EnableInput) (MutateResult, error) {
	path, err := ContractFilePath(input.BoxRoot, input.DirectoryRelPath, strings.ToLower(strings.TrimSpace(input.Key)))
	if err != nil {
		return MutateResult{}, err
	}
	contract, _, err := LoadFile(path)
	if err != nil {
		return MutateResult{}, fmt.Errorf("load backup contract %s: %w", path, err)
	}
	stamp := timestamp(input.Now)
	contract.Status = StatusActive
	contract.Metadata.UpdatedAt = stamp
	contract.Metadata.DisabledAt = ""
	contract.Metadata.DisabledBy = ""
	payload, err := Render(contract)
	if err != nil {
		return MutateResult{}, err
	}
	result := MutateResult{DryRun: input.DryRun, Path: path, DirectoryPath: filepath.Dir(path), Action: "enable", Contract: contract, RenderedYAML: string(payload), Updated: true}
	if input.DryRun {
		return result, nil
	}
	if err := writeContractFile(path, payload); err != nil {
		return result, fmt.Errorf("write enabled backup contract %s: %w", path, err)
	}
	return result, nil
}

func Delete(input DeleteInput) (MutateResult, error) {
	path, err := ContractFilePath(input.BoxRoot, input.DirectoryRelPath, strings.ToLower(strings.TrimSpace(input.Key)))
	if err != nil {
		return MutateResult{}, err
	}
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return MutateResult{}, fmt.Errorf("backup contract %q does not exist", input.Key)
		}
		return MutateResult{}, fmt.Errorf("stat backup contract %s: %w", path, err)
	}
	result := MutateResult{
		DryRun:        input.DryRun,
		Path:          path,
		DirectoryPath: filepath.Dir(path),
		Action:        "delete",
		Deleted:       true,
	}
	if input.DryRun {
		return result, nil
	}
	if err := os.Remove(path); err != nil {
		return result, fmt.Errorf("delete backup contract %s: %w", path, err)
	}
	return result, nil
}

func contractsDirectoryPath(boxRoot string, directoryRelPath string) (string, error) {
	if strings.TrimSpace(boxRoot) == "" {
		return "", fmt.Errorf("backup contract box root is required")
	}
	dirRel, err := normalizeDirectoryRelPath(directoryRelPath)
	if err != nil {
		return "", err
	}
	return filepath.Join(boxRoot, filepath.FromSlash(dirRel)), nil
}

func normalizeDirectoryRelPath(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		value = DefaultDirectoryRelPath
	}
	value = filepath.ToSlash(value)
	if filepath.IsAbs(value) || strings.HasPrefix(value, "/") {
		return "", fmt.Errorf("backup contract directory must be relative")
	}
	for _, segment := range strings.Split(value, "/") {
		if segment == ".." {
			return "", fmt.Errorf("backup contract directory must not contain ..")
		}
	}
	clean := filepath.ToSlash(filepath.Clean(value))
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("backup contract directory must resolve inside the box")
	}
	return clean, nil
}

func ensureContractsDirectory(dir string) error {
	if err := os.MkdirAll(dir, contractDirectoryMode); err != nil {
		return err
	}
	return os.Chmod(dir, contractDirectoryMode)
}

func writeContractFile(path string, payload []byte) error {
	if err := os.WriteFile(path, payload, contractFileMode); err != nil {
		return err
	}
	return os.Chmod(path, contractFileMode)
}

func timestamp(now func() time.Time) string {
	value := time.Now().UTC()
	if now != nil {
		value = now().UTC()
	}
	return value.Format(time.RFC3339)
}

func actorName(actor string) string {
	actor = strings.TrimSpace(actor)
	if actor == "" {
		return "loom backup contract"
	}
	return actor
}
