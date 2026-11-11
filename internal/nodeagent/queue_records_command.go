package nodeagent

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	bolt "go.etcd.io/bbolt"
	"loom.local/loom/internal/correlation"
)

func newQueueRecordsCommand(opts *rootOptions) *cobra.Command {
	cmd := &cobra.Command{Use: "records", Short: "Migrate, inspect or export the transactional local queue"}
	var yes bool
	migrate := &cobra.Command{Use: "migrate", Short: "Import and verify all legacy history, then fence legacy writers (stop the node-agent owner first)", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if !yes {
			return fmt.Errorf("migration requires --yes and a stopped node-agent owner")
		}
		s, _, _, err := opts.loadAll()
		if err != nil {
			return err
		}
		receipt, err := s.MigrateQueueRecords(cmd.Context())
		if err != nil {
			return err
		}
		return renderJSON(opts.out, localSuccess(correlation.Normalize(opts.correlationID), receipt))
	}}
	migrate.Flags().BoolVar(&yes, "yes", false, "Confirm stopped-owner local queue migration, preserving original history")
	status := &cobra.Command{Use: "status", Short: "Inspect record counts and the preserved migration receipt", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		s, _, _, err := opts.loadAll()
		if err != nil {
			return err
		}
		var result any
		err = s.withRecordAccess(func(s Store) error {
			if s.queueDB == nil {
				result = map[string]any{"format": "legacy-json"}
				return nil
			}
			return s.queueDB.View(func(tx *bolt.Tx) error {
				counts := map[string]int{}
				for _, c := range recordCollections {
					counts[c] = recordBucket(tx, c, "rows").Stats().KeyN
				}
				result = map[string]any{"format": queueRecordsFormat, "path": s.queueRecordsPath(), "counts": counts, "migration": json.RawMessage(bytes.Clone(tx.Bucket([]byte("meta")).Get([]byte("migration")))), "legacy_fenced": string(tx.Bucket([]byte("meta")).Get([]byte("fenced"))) == "yes"}
				return nil
			})
		})
		if err != nil {
			return err
		}
		return renderJSON(opts.out, localSuccess(correlation.Normalize(opts.correlationID), result))
	}}
	export := &cobra.Command{Use: "export <new-directory>", Short: "Export a consistent full-history compatibility copy; never switch active storage", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		s, _, _, err := opts.loadAll()
		if err != nil {
			return err
		}
		dir, err := filepath.Abs(args[0])
		if err != nil {
			return err
		}
		if err := os.Mkdir(dir, 0700); err != nil {
			return fmt.Errorf("export requires a new directory: %w", err)
		}
		err = s.withRecordAccess(func(s Store) error {
			if s.queueDB == nil {
				return fmt.Errorf("record store is not active")
			}
			for _, c := range recordCollections {
				rows, order, err := s.readRecordRows(c)
				if err != nil {
					return err
				}
				var raw bytes.Buffer
				raw.WriteByte('[')
				for i, id := range order {
					if i > 0 {
						raw.WriteByte(',')
					}
					raw.Write(rows[id])
				}
				raw.WriteString("]\n")
				path := filepath.Join(dir, strings.ReplaceAll(c, "/", ".")+".json")
				if err := writeDurableQueueBytes(path, raw.Bytes()); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			return err
		}
		return renderJSON(opts.out, localSuccess(correlation.Normalize(opts.correlationID), map[string]any{"export_path": dir, "format": "legacy-json", "active_store_changed": false}))
	}}
	verify := &cobra.Command{Use: "verify-import", Short: "Verify exact imported content before resuming the stopped owner", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		s, _, _, err := opts.loadAll()
		if err != nil {
			return err
		}
		err = s.withRecordAccess(func(s Store) error {
			if s.queueDB == nil {
				return fmt.Errorf("record store is not active")
			}
			return s.queueDB.View(func(tx *bolt.Tx) error {
				var receipt queueRecordReceipt
				if err := json.Unmarshal(tx.Bucket([]byte("meta")).Get([]byte("migration")), &receipt); err != nil {
					return err
				}
				return verifyRecordImport(tx, receipt)
			})
		})
		if err != nil {
			return err
		}
		return renderJSON(opts.out, localSuccess(correlation.Normalize(opts.correlationID), map[string]any{"import_matches": true}))
	}}
	cmd.AddCommand(migrate, status, export, verify)
	return cmd
}
