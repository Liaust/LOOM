package filetransfer

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type LocalStore struct {
	DataDir string
}

func NewLocalStore(dataDir string) LocalStore {
	return LocalStore{DataDir: dataDir}
}

func (s LocalStore) TransferDir(transferID string) (string, error) {
	if err := ValidateTransferID(transferID); err != nil {
		return "", err
	}
	if strings.TrimSpace(s.DataDir) == "" {
		return "", fmt.Errorf("%w: data dir is required", ErrInvalid)
	}
	return filepath.Join(s.DataDir, "transfers", transferID), nil
}

func (s LocalStore) SaveManifest(manifest Manifest) error {
	manifest, err := NormalizeManifest(manifest, time.Now().UTC())
	if err != nil {
		return err
	}
	dir, err := s.TransferDir(manifest.TransferID)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create transfer state dir: %w", err)
	}
	return writeJSONFile(filepath.Join(dir, "manifest.json"), manifest, 0o600)
}

func (s LocalStore) LoadManifest(transferID string) (Manifest, error) {
	dir, err := s.TransferDir(transferID)
	if err != nil {
		return Manifest{}, err
	}
	var manifest Manifest
	if err := readJSONFile(filepath.Join(dir, "manifest.json"), &manifest); err != nil {
		return Manifest{}, err
	}
	manifest, err = NormalizeManifest(manifest, time.Now().UTC())
	if err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

func (s LocalStore) SaveChunks(manifest Manifest, chunks []Chunk) error {
	manifest, err := NormalizeManifest(manifest, time.Now().UTC())
	if err != nil {
		return err
	}
	chunks, err = NormalizeChunks(manifest, chunks, time.Now().UTC())
	if err != nil {
		return err
	}
	dir, err := s.TransferDir(manifest.TransferID)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create transfer state dir: %w", err)
	}
	payload := ChunkSet{
		SchemaVersion: ChunksSchemaVersion,
		TransferID:    manifest.TransferID,
		Chunks:        chunks,
		UpdatedAt:     time.Now().UTC(),
	}
	return writeJSONFile(filepath.Join(dir, "chunks.json"), payload, 0o600)
}

func (s LocalStore) LoadChunks(transferID string) ([]Chunk, error) {
	manifest, err := s.LoadManifest(transferID)
	if err != nil {
		return nil, err
	}
	dir, err := s.TransferDir(transferID)
	if err != nil {
		return nil, err
	}
	var set ChunkSet
	if err := readJSONFile(filepath.Join(dir, "chunks.json"), &set); err != nil {
		return nil, err
	}
	if set.SchemaVersion != ChunksSchemaVersion {
		return nil, fmt.Errorf("%w: unsupported chunks schema_version %q", ErrInvalid, set.SchemaVersion)
	}
	if set.TransferID != transferID {
		return nil, fmt.Errorf("%w: chunks transfer_id %q does not match %q", ErrInvalid, set.TransferID, transferID)
	}
	return NormalizeChunks(manifest, set.Chunks, time.Now().UTC())
}

func (s LocalStore) SaveState(manifest Manifest, chunks []Chunk) error {
	manifest, err := NormalizeManifest(manifest, time.Now().UTC())
	if err != nil {
		return err
	}
	if err := s.SaveManifest(manifest); err != nil {
		return err
	}
	return s.SaveChunks(manifest, chunks)
}

func (s LocalStore) ListManifests(limit int) ([]Manifest, error) {
	if strings.TrimSpace(s.DataDir) == "" {
		return nil, fmt.Errorf("%w: data dir is required", ErrInvalid)
	}
	root := filepath.Join(s.DataDir, "transfers")
	entries, err := os.ReadDir(root)
	if errors.Is(err, fs.ErrNotExist) {
		return []Manifest{}, nil
	}
	if err != nil {
		return nil, err
	}
	manifests := []Manifest{}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		manifest, err := s.LoadManifest(entry.Name())
		if err != nil {
			return nil, err
		}
		manifests = append(manifests, manifest)
	}
	sort.SliceStable(manifests, func(i, j int) bool {
		return manifests[i].UpdatedAt.After(manifests[j].UpdatedAt)
	})
	if limit > 0 && len(manifests) > limit {
		manifests = manifests[:limit]
	}
	return manifests, nil
}

func (s LocalStore) Delete(transferID string) error {
	dir, err := s.TransferDir(transferID)
	if err != nil {
		return err
	}
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	return nil
}

func readJSONFile(path string, target any) error {
	payload, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(payload, target); err != nil {
		return fmt.Errorf("decode %s: %w", path, err)
	}
	return nil
}

func writeJSONFile(path string, value any, perm fs.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	payload, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	payload = append(payload, '\n')
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpPath)
		}
	}()
	if _, err := tmp.Write(payload); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpPath, perm); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return err
	}
	cleanup = false
	return nil
}
