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
	LexicalFieldTitle   = "title"
	LexicalFieldHeading = "heading"
	LexicalFieldPath    = "path"
	LexicalFieldTag     = "tag"
	LexicalFieldBody    = "body"
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
	for _, term := range document.Terms {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO search.lexical_terms (
				search_document_id, field_key, term, term_frequency
			)
			VALUES ($1,$2,$3,$4)
		`, document.SearchDocumentID, term.FieldKey, term.Term, term.TermFrequency); err != nil {
			return LexicalDocument{}, err
		}
	}
	return document, nil
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
