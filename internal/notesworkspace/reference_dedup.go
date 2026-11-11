package notesworkspace

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"
)

type PDFDedupEntry struct {
	BaseID       string `json:"base_id"`
	FileID       string `json:"file_id"`
	PayloadHash  string `json:"payload_hash"`
	PayloadBytes int64  `json:"payload_bytes"`
	StoredBytes  int64  `json:"stored_json_bytes"`
	OriginalHash string `json:"original_record_hash"`
	LogicalHash  string `json:"logical_record_hash"`
	MetadataHash string `json:"metadata_hash"`
}

type PDFDedupPlan struct {
	Schema             string          `json:"schema"`
	CreatedAt          time.Time       `json:"created_at"`
	Entries            []PDFDedupEntry `json:"entries"`
	InlinePayloadBytes int64           `json:"inline_payload_bytes"`
	UniquePayloadBytes int64           `json:"unique_payload_bytes"`
	UniquePayloads     int             `json:"unique_payloads"`
	PlanHash           string          `json:"plan_hash"`
}

type PDFDedupResult struct {
	Status    string    `json:"status"`
	PlanHash  string    `json:"plan_hash"`
	Verified  int       `json:"verified"`
	Converted int       `json:"converted"`
	Replayed  int       `json:"replayed"`
	Finished  time.Time `json:"finished_at"`
}

func jsonHash(v any) string { raw, _ := json.Marshal(v); return digest(raw) }

func metadataHash(raw []byte) (string, error) {
	var fields map[string]any
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	if err := d.Decode(&fields); err != nil || fields == nil {
		return "", fmt.Errorf("invalid base metadata")
	}
	delete(fields, "content")
	return jsonHash(fields), nil
}

func (p PDFDedupPlan) Hash() string { p.PlanHash = ""; return jsonHash(p) }

func (p PDFDedupPlan) Validate(expected string) error {
	if p.Schema != "loom.notes_pdf_dedup.v1" || p.PlanHash == "" || p.PlanHash != expected || p.Hash() != expected || len(p.Entries) == 0 || len(p.Entries) > 100000 {
		return fmt.Errorf("reviewed PDF plan/hash required")
	}
	last := ""
	for _, e := range p.Entries {
		if e.BaseID <= last || e.FileID == "" || e.PayloadBytes <= 0 || e.PayloadBytes > MaxReferenceBytes || e.PayloadHash == "" || e.OriginalHash == "" || e.LogicalHash == "" || e.MetadataHash == "" {
			return fmt.Errorf("invalid PDF plan entry")
		}
		last = e.BaseID
	}
	return nil
}

// Discovery reads one PDF record at a time. No content is put in the plan.
func (s SQLStore) PlanPDFDedup(ctx context.Context) (p PDFDedupPlan, err error) {
	p = PDFDedupPlan{Schema: "loom.notes_pdf_dedup.v1", CreatedAt: time.Now().UTC(), Entries: []PDFDedupEntry{}}
	unique := map[string]int64{}
	after := ""
	for {
		var id, fileID string
		var raw []byte
		var shared sql.NullString
		var stored int64
		err = s.DB.QueryRowContext(ctx, `SELECT b.base_id,b.file_id,b.record,b.reference_payload_hash,pg_column_size(b.record)
 FROM notes_workspace.bases b JOIN notes_workspace.files f USING(file_id)
 WHERE b.base_id>$1 AND lower(f.record->>'relative_path') LIKE '%.pdf'
 ORDER BY b.base_id LIMIT 1`, after).Scan(&id, &fileID, &raw, &shared, &stored)
		if errors.Is(err, sql.ErrNoRows) {
			break
		}
		if err != nil {
			return p, err
		}
		after = id
		if shared.Valid {
			continue
		}
		var b Base
		if err := json.Unmarshal(raw, &b); err != nil {
			return p, err
		}
		if len(b.Content) == 0 {
			continue
		}
		if b.ID != id || b.FileID != fileID || !b.Exists || len(b.Content) > MaxReferenceBytes || digest(b.Content) != b.Hash {
			return p, fmt.Errorf("PDF base integrity mismatch: %s", id)
		}
		meta, err := metadataHash(raw)
		if err != nil {
			return p, err
		}
		e := PDFDedupEntry{id, fileID, b.Hash, int64(len(b.Content)), stored, digest(raw), jsonHash(b), meta}
		p.Entries = append(p.Entries, e)
		if len(p.Entries) > 100000 {
			return p, fmt.Errorf("PDF plan exceeds bounded entry limit")
		}
		p.InlinePayloadBytes += e.PayloadBytes
		unique[b.Hash] = e.PayloadBytes
	}
	for _, size := range unique {
		p.UniquePayloadBytes += size
	}
	p.UniquePayloads = len(unique)
	sort.Slice(p.Entries, func(i, j int) bool { return p.Entries[i].BaseID < p.Entries[j].BaseID })
	p.PlanHash = p.Hash()
	return p, nil
}

func verifyPDFEntry(ctx context.Context, q sqlQuery, e PDFDedupEntry, requireShared bool) error {
	var raw []byte
	var shared sql.NullString
	if err := q.QueryRowContext(ctx, `SELECT record,reference_payload_hash FROM notes_workspace.bases WHERE base_id=$1`, e.BaseID).Scan(&raw, &shared); err != nil {
		return err
	}
	if requireShared && (!shared.Valid || shared.String != e.PayloadHash) {
		return fmt.Errorf("PDF base not converted: %s", e.BaseID)
	}
	meta, err := metadataHash(raw)
	if err != nil || meta != e.MetadataHash {
		return fmt.Errorf("PDF metadata changed: %s", e.BaseID)
	}
	b, err := readSQLBase(ctx, q, e.BaseID)
	if err != nil {
		return err
	}
	if b.ID != e.BaseID || b.FileID != e.FileID || b.Hash != e.PayloadHash || int64(len(b.Content)) != e.PayloadBytes || jsonHash(b) != e.LogicalHash {
		return fmt.Errorf("PDF historical reconstruction mismatch: %s", e.BaseID)
	}
	return nil
}

func (s SQLStore) ApplyPDFDedup(ctx context.Context, p PDFDedupPlan, hash string, progress func(int)) (out PDFDedupResult, err error) {
	out.PlanHash = p.PlanHash
	if err = p.Validate(hash); err != nil {
		return
	}
	for i, e := range p.Entries {
		var replay bool
		replay, err = s.convertPDFEntry(ctx, e)
		if err != nil {
			return
		}
		if replay {
			out.Replayed++
		} else {
			out.Converted++
		}
		if progress != nil {
			progress(i + 1)
		}
	}
	raw, _ := json.Marshal(p)
	_, err = s.DB.ExecContext(ctx, `INSERT INTO notes_workspace.reference_dedup_runs(plan_hash,plan) VALUES($1,$2)
 ON CONFLICT(plan_hash) DO NOTHING`, p.PlanHash, raw)
	if err != nil {
		return
	}
	out.Status, out.Finished = "converted", time.Now().UTC()
	return
}

func (s SQLStore) convertPDFEntry(ctx context.Context, e PDFDedupEntry) (replay bool, err error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `SET LOCAL lock_timeout='5s'`); err != nil {
		return
	}
	var raw []byte
	var shared sql.NullString
	var relative string
	err = tx.QueryRowContext(ctx, `SELECT b.record,b.reference_payload_hash,f.record->>'relative_path'
 FROM notes_workspace.bases b JOIN notes_workspace.files f USING(file_id) WHERE b.base_id=$1 FOR UPDATE OF b`, e.BaseID).Scan(&raw, &shared, &relative)
	if err != nil {
		return
	}
	if !isPDF(relative) {
		return false, fmt.Errorf("PDF path classification changed: %s", e.BaseID)
	}
	if err = verifyPDFEntry(ctx, tx, e, shared.Valid); err != nil {
		return
	}
	if shared.Valid {
		return true, tx.Commit()
	}
	if digest(raw) != e.OriginalHash {
		return false, fmt.Errorf("PDF reviewed row changed: %s", e.BaseID)
	}
	var b Base
	if err = json.Unmarshal(raw, &b); err != nil {
		return
	}
	if err = ensureReferencePayload(ctx, tx, e.PayloadHash, b.Content); err != nil {
		return
	}
	// JSONB subtraction preserves every unknown metadata field and original ID.
	_, err = tx.ExecContext(ctx, `UPDATE notes_workspace.bases SET record=record-'content',reference_payload_hash=$2 WHERE base_id=$1`, e.BaseID, e.PayloadHash)
	if err != nil {
		return
	}
	if err = verifyPDFEntry(ctx, tx, e, true); err != nil {
		return
	}
	err = tx.Commit()
	return
}

func (s SQLStore) VerifyPDFDedup(ctx context.Context, p PDFDedupPlan, hash string) (out PDFDedupResult, err error) {
	out.PlanHash = p.PlanHash
	if err = p.Validate(hash); err != nil {
		return
	}
	for _, e := range p.Entries {
		if err = verifyPDFEntry(ctx, s.DB, e, true); err != nil {
			return
		}
		out.Verified++
	}
	out.Status, out.Finished = "verified", time.Now().UTC()
	return
}

// Protection is an existing succeeded maintenance result, not an operator's
// unverified 'latest' string. The selected dump must start after conversion.
func (s SQLStore) CheckPDFDedupProtection(ctx context.Context, p PDFDedupPlan, cloudID string) error {
	var completed time.Time
	var saved []byte
	if err := s.DB.QueryRowContext(ctx, `SELECT completed_at,plan FROM notes_workspace.reference_dedup_runs WHERE plan_hash=$1`, p.PlanHash).Scan(&completed, &saved); err != nil {
		return err
	}
	var savedPlan PDFDedupPlan
	if json.Unmarshal(saved, &savedPlan) != nil || savedPlan.Hash() != p.Hash() {
		return fmt.Errorf("PDF conversion receipt mismatch")
	}
	var status, kind string
	var raw []byte
	if err := s.DB.QueryRowContext(ctx, `SELECT status,operation_kind,result_json FROM maintenance.operations WHERE maintenance_operation_id=$1`, cloudID).Scan(&status, &kind, &raw); err != nil {
		return err
	}
	var cloud struct {
		Committed       bool   `json:"committed"`
		Status          string `json:"status"`
		BackupID        string `json:"backup_operation_id"`
		Selected        string `json:"selected_backup_operation_id"`
		SelectorMatched bool   `json:"backup_operation_selector_matched"`
		Archive         string `json:"archive_ref"`
	}
	if json.Unmarshal(raw, &cloud) != nil || status != "succeeded" || kind != "cloud_snapshot_upload" || !cloud.Committed || cloud.Status != "succeeded" || cloud.BackupID == "" || cloud.Selected != cloud.BackupID || !cloud.SelectorMatched || cloud.Archive == "" {
		return fmt.Errorf("succeeded exact-selected cloud protection required")
	}
	var started time.Time
	if err := s.DB.QueryRowContext(ctx, `SELECT started_at,status,operation_kind,result_json FROM maintenance.operations WHERE maintenance_operation_id=$1`, cloud.BackupID).Scan(&started, &status, &kind, &raw); err != nil {
		return err
	}
	var backup struct {
		Committed    bool   `json:"committed"`
		Verification string `json:"verification_status"`
	}
	if json.Unmarshal(raw, &backup) != nil || status != "succeeded" || kind != "main_backup" || !started.After(completed) || !backup.Committed || backup.Verification != "succeeded" {
		return fmt.Errorf("verified post-conversion backup required")
	}
	return nil
}
