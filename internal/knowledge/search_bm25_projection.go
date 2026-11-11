package knowledge

import (
	"context"
	"fmt"
)

const NotesSearchBackendNative = "native"
const NotesSearchBackendBM25 = "pg_search"
const notesPGSearchVersion = "0.25.11"

// Only the explicitly selected backend builds its derived index. Extension
// provisioning belongs to the host, not the unprivileged search service.
func (s *Service) PrepareNotesSearchBackend(ctx context.Context) error {
	if s.notesSearchBackend == "" || s.notesSearchBackend == NotesSearchBackendNative {
		return nil
	}
	if s.notesSearchBackend != NotesSearchBackendBM25 {
		return fmt.Errorf("unknown Notes search backend %q", s.notesSearchBackend)
	}
	var version string
	if err := s.store.db.QueryRowContext(ctx, `SELECT extversion FROM pg_extension WHERE extname='pg_search'`).Scan(&version); err != nil {
		return fmt.Errorf("Notes BM25 extension unavailable: %w", err)
	}
	if version != notesPGSearchVersion {
		return fmt.Errorf("Notes BM25 requires pg_search %s, installed %s", notesPGSearchVersion, version)
	}
	_, err := s.store.db.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS notes_bm25_idx
 ON knowledge.notes_bm25_documents USING bm25
 (id,title,heading,path,body,tags,(project_id::pdb.literal),(root_id::pdb.literal),
 (node_key::pdb.literal),(root_kind::pdb.literal),(file_class::pdb.literal),
 recency_at,(lifecycle::pdb.literal),is_current,(id_sort::pdb.literal)) WITH(key_field='id')`)
	if err != nil {
		return fmt.Errorf("prepare Notes BM25 index: %w", err)
	}
	var valid bool
	err = s.store.db.QueryRowContext(ctx, `SELECT i.indisvalid AND i.indisready AND am.amname='bm25'
 FROM pg_index i JOIN pg_class c ON c.oid=i.indexrelid JOIN pg_am am ON am.oid=c.relam
 WHERE i.indexrelid='knowledge.notes_bm25_idx'::regclass
 AND i.indrelid='knowledge.notes_bm25_documents'::regclass`).Scan(&valid)
	if err != nil || !valid {
		return fmt.Errorf("Notes BM25 index is not ready: %v", err)
	}
	return nil
}
