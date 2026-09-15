package nodeagent

import (
	"archive/tar"
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"loom.local/loom/internal/correlation"
	loomsync "loom.local/loom/internal/sync"
)

type LocalPrivateBackupResult struct {
	Operation loomsync.PrivateBackupOperation `json:"operation"`
	SavedAt   time.Time                       `json:"saved_at"`
}

func newPrivateBackupCommand(opts *rootOptions) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "private-backup",
		Short: "Push private raw backup payloads without indexing",
	}
	cmd.AddCommand(newPrivateBackupPushCommand(opts))
	return cmd
}

func newPrivateBackupPushCommand(opts *rootOptions) *cobra.Command {
	var path string
	var once bool
	cmd := &cobra.Command{
		Use:   "push --path <folder>",
		Short: "Push a tiny private folder as a raw backup archive",
		RunE: func(cmd *cobra.Command, args []string) error {
			if !once {
				return errors.New("private-backup push currently requires --once")
			}
			store, config, state, err := opts.loadAll()
			if err != nil {
				return err
			}
			if strings.TrimSpace(state.NodeID) == "" {
				return errors.New("node credential is not imported: missing node_id")
			}
			if strings.TrimSpace(state.CredentialToken) == "" {
				return errors.New("node credential is not imported: missing credential_token")
			}
			payload, itemCount, err := archivePrivateBackupPath(path)
			if err != nil {
				return err
			}
			sourcePath, rootKey, err := privateBackupSourceIdentity(path)
			if err != nil {
				return err
			}
			if len(payload) > loomsync.MaxPrivateBackupBytes {
				return fmt.Errorf("private backup archive exceeds %d byte limit", loomsync.MaxPrivateBackupBytes)
			}
			client, err := NewClient(config.MainURL)
			if err != nil {
				return err
			}
			generatedAt := time.Now().UTC()
			idempotencyKey := privateBackupIdempotencyKey(state.NodeID, payload)
			input := loomsync.PrivateBackupInput{
				NodeRef:         state.NodeID,
				CredentialToken: state.CredentialToken,
				IdempotencyKey:  idempotencyKey,
				PayloadBase64:   base64.StdEncoding.EncodeToString(payload),
				CoarseSizeBytes: int64(len(payload)),
				Metadata: objectJSON(map[string]any{
					"source":              "loom-node-agent",
					"backup_kind":         "private_raw_folder",
					"root_key":            rootKey,
					"source_path":         sourcePath,
					"client_generated_at": generatedAt.Format(time.RFC3339Nano),
					"item_count_coarse":   itemCount,
				}),
			}
			envelope, err := client.PushPrivateBackup(cmd.Context(), correlation.Normalize(opts.correlationID), idempotencyKey, input)
			if err != nil {
				return err
			}
			if err := store.SavePrivateBackupResult(envelope.Data); err != nil {
				return err
			}
			if opts.jsonOutput {
				return renderJSON(opts.out, envelope)
			}
			_, err = fmt.Fprintf(opts.out, "private_backup=%s status=%s coarse_size=%d\n",
				envelope.Data.Operation.PrivateBackupOperationID,
				envelope.Data.Operation.Status,
				envelope.Data.Operation.CoarseSizeBytes,
			)
			return err
		},
	}
	cmd.Flags().StringVar(&path, "path", "", "Private folder path to archive")
	cmd.Flags().BoolVar(&once, "once", false, "Push one private backup and exit")
	_ = cmd.MarkFlagRequired("path")
	return cmd
}

func privateBackupSourceIdentity(path string) (string, string, error) {
	root, err := filepath.Abs(strings.TrimSpace(path))
	if err != nil {
		return "", "", err
	}
	root = filepath.Clean(root)
	rootKey := filepath.Base(root)
	if rootKey == "." || rootKey == string(filepath.Separator) || strings.TrimSpace(rootKey) == "" {
		return "", "", fmt.Errorf("private backup path needs a named root directory")
	}
	return root, rootKey, nil
}

func archivePrivateBackupPath(path string) ([]byte, int, error) {
	root, err := filepath.Abs(strings.TrimSpace(path))
	if err != nil {
		return nil, 0, err
	}
	info, err := os.Stat(root)
	if err != nil {
		return nil, 0, err
	}
	if !info.IsDir() {
		return nil, 0, fmt.Errorf("private backup path must be a directory")
	}
	var buffer bytes.Buffer
	writer := tar.NewWriter(&buffer)
	itemCount := 0
	err = filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		header, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		header.Name = filepath.ToSlash(rel)
		header.Mode = 0o600
		if err := writer.WriteHeader(header); err != nil {
			return err
		}
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(writer, file)
		closeErr := file.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
		itemCount++
		if buffer.Len() > loomsync.MaxPrivateBackupBytes {
			return fmt.Errorf("private backup archive exceeds %d byte limit", loomsync.MaxPrivateBackupBytes)
		}
		return nil
	})
	if closeErr := writer.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return nil, 0, err
	}
	if itemCount == 0 {
		return nil, 0, fmt.Errorf("private backup path has no regular files")
	}
	return buffer.Bytes(), itemCount, nil
}

func privateBackupIdempotencyKey(nodeID string, payload []byte) string {
	sum := sha256.Sum256(payload)
	return "node-agent.private-backup." + strings.TrimSpace(nodeID) + "." + hex.EncodeToString(sum[:])[:24]
}

func (s Store) SavePrivateBackupResult(result loomsync.PrivateBackupResult) error {
	if err := s.EnsureDataDirs(); err != nil {
		return err
	}
	dir := filepath.Join(s.DataDir, "private-backups")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	local := LocalPrivateBackupResult{
		Operation: result.Operation,
		SavedAt:   time.Now().UTC(),
	}
	return writeJSONFile(filepath.Join(dir, result.Operation.PrivateBackupOperationID+".json"), local, 0o600)
}
