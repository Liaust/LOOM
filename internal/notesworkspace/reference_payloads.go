package notesworkspace

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"path"
	"strings"
)

func isPDF(relative string) bool { return strings.EqualFold(path.Ext(relative), ".pdf") }

func ensureReferencePayload(ctx context.Context, q sqlQuery, hash string, content []byte) error {
	if len(content) == 0 || len(content) > MaxReferenceBytes || digest(content) != hash {
		return fmt.Errorf("reference payload hash/size mismatch")
	}
	if _, err := q.ExecContext(ctx, `INSERT INTO notes_workspace.reference_payloads(payload_hash,size_bytes,content)
 VALUES($1,$2,$3) ON CONFLICT(payload_hash) DO NOTHING`, hash, len(content), content); err != nil {
		return err
	}
	var saved []byte
	var size int64
	if err := q.QueryRowContext(ctx, `SELECT content,size_bytes FROM notes_workspace.reference_payloads WHERE payload_hash=$1`, hash).Scan(&saved, &size); err != nil {
		return err
	}
	if size != int64(len(content)) || !bytes.Equal(saved, content) {
		return fmt.Errorf("reference payload conflict")
	}
	return nil
}

// Hydration preserves the original logical Base, including its historical ID.
// Shared storage must never turn a missing payload into an empty file.
func readSQLBase(ctx context.Context, q sqlQuery, id string) (b Base, err error) {
	var raw, content []byte
	var hash sql.NullString
	var size sql.NullInt64
	err = q.QueryRowContext(ctx, `SELECT b.record,b.reference_payload_hash,p.content,p.size_bytes
 FROM notes_workspace.bases b LEFT JOIN notes_workspace.reference_payloads p
 ON p.payload_hash=b.reference_payload_hash WHERE b.base_id=$1`, id).Scan(&raw, &hash, &content, &size)
	if err != nil {
		return
	}
	if err = json.Unmarshal(raw, &b); err != nil || !hash.Valid {
		return
	}
	if len(b.Content) != 0 || !size.Valid || size.Int64 <= 0 || size.Int64 > MaxReferenceBytes ||
		int64(len(content)) != size.Int64 || b.Hash != hash.String || digest(content) != hash.String {
		return Base{}, fmt.Errorf("shared reference payload unavailable or corrupt")
	}
	b.Content = content
	return
}
