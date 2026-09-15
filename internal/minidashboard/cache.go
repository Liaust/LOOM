package minidashboard

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

type DomainCache struct{ Path string }

func (c DomainCache) Load() (*DomainSnapshot, error) {
	payload, err := os.ReadFile(c.Path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	if len(payload) > MaxDomainPayloadSize {
		return nil, fmt.Errorf("cache exceeds %d bytes", MaxDomainPayloadSize)
	}
	var snapshot DomainSnapshot
	if err := json.Unmarshal(payload, &snapshot); err != nil {
		return nil, err
	}
	if err := snapshot.Validate(); err != nil {
		return nil, err
	}
	return &snapshot, nil
}

func (c DomainCache) Save(snapshot DomainSnapshot) error {
	if err := snapshot.Validate(); err != nil {
		return err
	}
	payload, err := json.Marshal(snapshot)
	if err != nil {
		return err
	}
	if len(payload) > MaxDomainPayloadSize {
		return fmt.Errorf("cache exceeds %d bytes", MaxDomainPayloadSize)
	}
	directory := filepath.Dir(c.Path)
	if err := os.MkdirAll(directory, 0o750); err != nil {
		return err
	}
	if err := os.Chmod(directory, 0o750); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(directory, ".last-good-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o640); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(payload); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, c.Path)
}
