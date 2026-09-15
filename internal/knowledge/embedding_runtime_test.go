package knowledge

import (
	"context"
	"errors"
	"testing"
)

func TestBuildEmbeddingQueryInputUsesRetrievalPrompt(t *testing.T) {
	input, err := BuildEmbeddingQueryInput(" loom notes search ")
	if err != nil {
		t.Fatalf("BuildEmbeddingQueryInput returned error: %v", err)
	}
	want := "Represent this sentence for searching relevant passages: loom notes search"
	if input != want {
		t.Fatalf("query input = %q, want %q", input, want)
	}
}

func TestBuildEmbeddingQueryInputRejectsEmptyQuery(t *testing.T) {
	if _, err := BuildEmbeddingQueryInput("  "); !errors.Is(err, ErrInvalid) {
		t.Fatalf("BuildEmbeddingQueryInput error = %v, want ErrInvalid", err)
	}
}

func TestEmbeddingRuntimeErrorClassification(t *testing.T) {
	timeoutErr := &EmbeddingRuntimeError{Kind: EmbeddingRuntimeErrorTimeout, Err: context.DeadlineExceeded}
	if !IsEmbeddingRuntimeTimeout(timeoutErr) {
		t.Fatal("expected timeout runtime error to classify as timeout")
	}
	if IsEmbeddingRuntimeUnavailable(timeoutErr) {
		t.Fatal("timeout runtime error must not classify as unavailable")
	}

	unavailableErr := &EmbeddingRuntimeError{Kind: EmbeddingRuntimeErrorUnavailable}
	if !IsEmbeddingRuntimeUnavailable(unavailableErr) {
		t.Fatal("expected unavailable runtime error to classify as unavailable")
	}
}

func TestNormalizeEmbeddingRuntimeRequestDefaultsModelAndRejectsEmptyInputs(t *testing.T) {
	request, err := normalizeEmbeddingRuntimeRequest(EmbeddingRuntimeRequest{
		Inputs: []string{" passage "},
	})
	if err != nil {
		t.Fatalf("normalizeEmbeddingRuntimeRequest returned error: %v", err)
	}
	if request.Model != EmbeddingModelMXBAIEmbedLarge {
		t.Fatalf("Model = %q, want %q", request.Model, EmbeddingModelMXBAIEmbedLarge)
	}
	if request.Inputs[0] != "passage" {
		t.Fatalf("input = %q, want trimmed passage", request.Inputs[0])
	}

	if _, err := normalizeEmbeddingRuntimeRequest(EmbeddingRuntimeRequest{Inputs: []string{""}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("normalizeEmbeddingRuntimeRequest empty input error = %v, want ErrInvalid", err)
	}
}
