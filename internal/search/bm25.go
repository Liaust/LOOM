package search

import (
	"math"
	"sort"
	"strings"
)

const (
	DefaultBM25K1 = 1.2
	DefaultBM25B  = 0.75
)

type BM25Parameters struct {
	K1 float64
	B  float64
}

type BM25CorpusStats struct {
	DocumentCount         int
	AverageDocumentLength float64
	DocumentFrequency     map[string]int
}

type BM25Document struct {
	ID            string
	Length        int
	TermFrequency map[string]int
}

type BM25Result struct {
	DocumentID string
	Score      float64
}

func DefaultBM25Parameters() BM25Parameters {
	return BM25Parameters{K1: DefaultBM25K1, B: DefaultBM25B}
}

func NewBM25Document(id string, tokens []string) BM25Document {
	return BM25Document{
		ID:            strings.TrimSpace(id),
		Length:        len(tokens),
		TermFrequency: TermFrequencies(tokens),
	}
}

func BM25StatsFromDocuments(documents []BM25Document) BM25CorpusStats {
	stats := BM25CorpusStats{
		DocumentCount:     len(documents),
		DocumentFrequency: map[string]int{},
	}
	totalLength := 0
	for _, document := range documents {
		totalLength += document.Length
		for term, frequency := range document.TermFrequency {
			if term == "" || frequency <= 0 {
				continue
			}
			stats.DocumentFrequency[term]++
		}
	}
	if len(documents) > 0 {
		stats.AverageDocumentLength = float64(totalLength) / float64(len(documents))
	}
	return stats
}

func BM25Score(queryTerms []string, document BM25Document, stats BM25CorpusStats, params BM25Parameters) float64 {
	params = normalizeBM25Parameters(params)
	if stats.DocumentCount <= 0 || stats.AverageDocumentLength <= 0 || document.Length <= 0 {
		return 0
	}
	score := 0.0
	for _, term := range UniqueTerms(queryTerms) {
		tf := document.TermFrequency[term]
		if tf <= 0 {
			continue
		}
		idf := BM25IDF(stats.DocumentCount, stats.DocumentFrequency[term])
		tfFloat := float64(tf)
		lengthNorm := float64(document.Length) / stats.AverageDocumentLength
		denominator := tfFloat + params.K1*(1-params.B+params.B*lengthNorm)
		if denominator <= 0 {
			continue
		}
		score += idf * ((tfFloat * (params.K1 + 1)) / denominator)
	}
	return score
}

func BM25IDF(documentCount int, documentFrequency int) float64 {
	if documentCount <= 0 || documentFrequency <= 0 {
		return 0
	}
	return math.Log(1 + (float64(documentCount)-float64(documentFrequency)+0.5)/(float64(documentFrequency)+0.5))
}

func ScoreBM25Documents(queryTerms []string, documents []BM25Document, stats BM25CorpusStats, params BM25Parameters) []BM25Result {
	results := make([]BM25Result, 0, len(documents))
	for _, document := range documents {
		score := BM25Score(queryTerms, document, stats, params)
		if score <= 0 {
			continue
		}
		results = append(results, BM25Result{DocumentID: document.ID, Score: score})
	}
	sort.SliceStable(results, func(i, j int) bool {
		if results[i].Score == results[j].Score {
			return results[i].DocumentID < results[j].DocumentID
		}
		return results[i].Score > results[j].Score
	})
	return results
}

func normalizeBM25Parameters(params BM25Parameters) BM25Parameters {
	if params.K1 <= 0 {
		params.K1 = DefaultBM25K1
	}
	if params.B < 0 || params.B > 1 {
		params.B = DefaultBM25B
	}
	return params
}
