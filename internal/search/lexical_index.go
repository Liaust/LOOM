package search

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

const (
	LexicalFieldTitle      = "title"
	LexicalFieldHeading    = "heading"
	LexicalFieldPath       = "path"
	LexicalFieldTag        = "tag"
	LexicalFieldBody       = "body"
	lexicalWriteBatchRows  = 256
	lexicalWriteBatchBytes = 256 * 1024
)

type LexicalFieldInput struct {
	Key  string
	Text string
}

type LexicalDocumentInput struct {
	SearchDocumentID string
	IndexVersion     string
	SourceKind       string
	SourceID         string
	SourceVersionID  string
	ObjectID         string
	Fields           []LexicalFieldInput
	Metadata         json.RawMessage
}

type LexicalDocument struct {
	SearchDocumentID string
	IndexVersion     string
	SourceKind       string
	SourceID         string
	SourceVersionID  string
	ObjectID         string
	FieldLengths     map[string]int
	DocumentLength   int
	Terms            []LexicalTerm
	Metadata         json.RawMessage
}

type LexicalTerm struct {
	FieldKey      string
	Term          string
	TermFrequency int
}

func BuildLexicalDocument(input LexicalDocumentInput) (LexicalDocument, error) {
	input.SearchDocumentID = strings.TrimSpace(input.SearchDocumentID)
	input.IndexVersion = strings.TrimSpace(input.IndexVersion)
	input.SourceKind = strings.TrimSpace(input.SourceKind)
	input.SourceID = strings.TrimSpace(input.SourceID)
	if input.SearchDocumentID == "" {
		return LexicalDocument{}, fmt.Errorf("search document id is required")
	}
	if input.IndexVersion == "" {
		return LexicalDocument{}, fmt.Errorf("index version is required")
	}
	if input.SourceKind == "" || input.SourceID == "" {
		return LexicalDocument{}, fmt.Errorf("source kind and source id are required")
	}
	fieldTokens := map[string][]string{}
	for _, field := range input.Fields {
		key := strings.ToLower(strings.TrimSpace(field.Key))
		if key == "" {
			continue
		}
		fieldTokens[key] = append(fieldTokens[key], TokenizeLexical(field.Text)...)
	}
	fieldLengths := map[string]int{}
	terms := []LexicalTerm{}
	documentLength := 0
	fieldKeys := make([]string, 0, len(fieldTokens))
	for key := range fieldTokens {
		fieldKeys = append(fieldKeys, key)
	}
	sort.Strings(fieldKeys)
	for _, fieldKey := range fieldKeys {
		tokens := fieldTokens[fieldKey]
		if len(tokens) == 0 {
			continue
		}
		documentLength += len(tokens)
		fieldLengths[fieldKey] = len(tokens)
		frequencies := TermFrequencies(tokens)
		tokenKeys := make([]string, 0, len(frequencies))
		for term := range frequencies {
			tokenKeys = append(tokenKeys, term)
		}
		sort.Strings(tokenKeys)
		for _, term := range tokenKeys {
			terms = append(terms, LexicalTerm{
				FieldKey:      fieldKey,
				Term:          term,
				TermFrequency: frequencies[term],
			})
		}
	}
	return LexicalDocument{
		SearchDocumentID: input.SearchDocumentID,
		IndexVersion:     input.IndexVersion,
		SourceKind:       input.SourceKind,
		SourceID:         input.SourceID,
		SourceVersionID:  strings.TrimSpace(input.SourceVersionID),
		ObjectID:         strings.TrimSpace(input.ObjectID),
		FieldLengths:     fieldLengths,
		DocumentLength:   documentLength,
		Terms:            terms,
		Metadata:         jsonObjectOrEmpty(input.Metadata),
	}, nil
}

func UpsertLexicalDocumentTx(ctx context.Context, tx *sql.Tx, input LexicalDocumentInput) (LexicalDocument, error) {
	document, err := BuildLexicalDocument(input)
	if err != nil {
		return LexicalDocument{}, err
	}
	return upsertBuiltLexicalDocumentTx(ctx, tx, document)
}

// EnsureLexicalDocumentTx preserves an unchanged materialization, but repairs
// missing or stale headers/terms. The parent and header locks fence concurrent
// publication while the exact term set is compared in this transaction.
func EnsureLexicalDocumentTx(ctx context.Context, tx *sql.Tx, input LexicalDocumentInput) (LexicalDocument, bool, error) {
	document, err := BuildLexicalDocument(input)
	if err != nil {
		return LexicalDocument{}, false, err
	}
	var parentID string
	if err = tx.QueryRowContext(ctx, `SELECT search_document_id FROM search.search_documents
		WHERE search_document_id=$1 FOR UPDATE`, document.SearchDocumentID).Scan(&parentID); err != nil {
		return LexicalDocument{}, false, err
	}
	fieldLengths, err := json.Marshal(document.FieldLengths)
	if err != nil {
		return LexicalDocument{}, false, err
	}
	var same bool
	err = tx.QueryRowContext(ctx, `SELECT index_version=$2 AND source_kind=$3 AND source_id=$4
		AND source_version_id IS NOT DISTINCT FROM nullif($5::text,'')
		AND object_id IS NOT DISTINCT FROM nullif($6::text,'')
		AND field_lengths=$7::jsonb AND document_length=$8 AND metadata=$9::jsonb
		FROM search.lexical_documents WHERE search_document_id=$1 FOR UPDATE`,
		document.SearchDocumentID, document.IndexVersion, document.SourceKind, document.SourceID,
		document.SourceVersionID, document.ObjectID, fieldLengths, document.DocumentLength, document.Metadata).Scan(&same)
	if err != nil && err != sql.ErrNoRows {
		return LexicalDocument{}, false, err
	}
	if same {
		// BuildLexicalDocument sorts by UTF-8 bytes; match that order regardless
		// of the database locale. One extra row detects surplus materialization.
		rows, queryErr := tx.QueryContext(ctx, `SELECT field_key,term,term_frequency
			FROM search.lexical_terms WHERE search_document_id=$1
			ORDER BY field_key COLLATE "C",term COLLATE "C" LIMIT $2`, document.SearchDocumentID, len(document.Terms)+1)
		if queryErr != nil {
			return LexicalDocument{}, false, queryErr
		}
		index := 0
		for rows.Next() {
			var term LexicalTerm
			if err = rows.Scan(&term.FieldKey, &term.Term, &term.TermFrequency); err != nil {
				rows.Close()
				return LexicalDocument{}, false, err
			}
			if index >= len(document.Terms) || term != document.Terms[index] {
				same = false
				break
			}
			index++
		}
		err = rows.Err()
		closeErr := rows.Close()
		if err != nil {
			return LexicalDocument{}, false, err
		}
		if closeErr != nil {
			return LexicalDocument{}, false, closeErr
		}
		same = same && index == len(document.Terms)
	}
	if same {
		return document, false, nil
	}
	document, err = upsertBuiltLexicalDocumentTx(ctx, tx, document)
	return document, err == nil, err
}

func upsertBuiltLexicalDocumentTx(ctx context.Context, tx *sql.Tx, document LexicalDocument) (LexicalDocument, error) {
	fieldLengths, err := json.Marshal(document.FieldLengths)
	if err != nil {
		return LexicalDocument{}, err
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO search.lexical_documents (
			search_document_id, index_version, source_kind, source_id,
			source_version_id, object_id, field_lengths, document_length,
			metadata, updated_at
		)
		VALUES ($1,$2,$3,$4,nullif($5, ''),nullif($6, ''),$7,$8,$9,now())
		ON CONFLICT (search_document_id)
		DO UPDATE SET
			index_version = EXCLUDED.index_version,
			source_kind = EXCLUDED.source_kind,
			source_id = EXCLUDED.source_id,
			source_version_id = EXCLUDED.source_version_id,
			object_id = EXCLUDED.object_id,
			field_lengths = EXCLUDED.field_lengths,
			document_length = EXCLUDED.document_length,
			metadata = EXCLUDED.metadata,
			updated_at = now()
	`, document.SearchDocumentID,
		document.IndexVersion,
		document.SourceKind,
		document.SourceID,
		document.SourceVersionID,
		document.ObjectID,
		fieldLengths,
		document.DocumentLength,
		document.Metadata,
	)
	if err != nil {
		return LexicalDocument{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		DELETE FROM search.lexical_terms
		WHERE search_document_id = $1
	`, document.SearchDocumentID); err != nil {
		return LexicalDocument{}, err
	}
	for start := 0; start < len(document.Terms); {
		end := lexicalTermBatchEnd(document.Terms, start)
		fields := make([]string, 0, end-start)
		terms := make([]string, 0, end-start)
		frequencies := make([]int64, 0, end-start)
		for _, term := range document.Terms[start:end] {
			fields = append(fields, term.FieldKey)
			terms = append(terms, term.Term)
			frequencies = append(frequencies, int64(term.TermFrequency))
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO search.lexical_terms (
				search_document_id, field_key, term, term_frequency
			)
			SELECT $1, field_key, term, term_frequency
			FROM unnest($2::text[], $3::text[], $4::bigint[])
			AS terms(field_key, term, term_frequency)
		`, document.SearchDocumentID, fields, terms, frequencies); err != nil {
			return LexicalDocument{}, err
		}
		start = end
	}
	return document, nil
}

// Bound array payload and rows independently. A formerly accepted oversized
// field key still makes progress in a singleton rather than changing validation.
func lexicalTermBatchEnd(terms []LexicalTerm, start int) int {
	end, bytes := start, 0
	for end < len(terms) && end-start < lexicalWriteBatchRows {
		size := len(terms[end].FieldKey) + len(terms[end].Term) + 24
		if end > start && size > lexicalWriteBatchBytes-bytes {
			break
		}
		bytes += size
		end++
	}
	return end
}

func jsonObjectOrEmpty(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return json.RawMessage(`{}`)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil || decoded == nil {
		return json.RawMessage(`{}`)
	}
	return raw
}
