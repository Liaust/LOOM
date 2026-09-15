package knowledge

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestAverageEmbeddingVectors(t *testing.T) {
	vector, err := AverageEmbeddingVectors([][]float32{
		{1, 3, 5},
		{3, 5, 7},
	})
	if err != nil {
		t.Fatalf("AverageEmbeddingVectors returned error: %v", err)
	}
	want := []float32{2, 4, 6}
	for i := range want {
		if vector[i] != want[i] {
			t.Fatalf("vector[%d] = %v, want %v", i, vector[i], want[i])
		}
	}
}

func TestAverageEmbeddingVectorsRejectsInvalidInput(t *testing.T) {
	if _, err := AverageEmbeddingVectors(nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("nil vectors error = %v, want ErrInvalid", err)
	}
	if _, err := AverageEmbeddingVectors([][]float32{{1}, {1, 2}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("dimension mismatch error = %v, want ErrInvalid", err)
	}
}

func TestEmbeddingVectorLiteral(t *testing.T) {
	got := embeddingVectorLiteral([]float32{0.25, -1, 3.5})
	if got != "[0.25,-1,3.5]" {
		t.Fatalf("embeddingVectorLiteral = %q", got)
	}
}

func TestNormalizeEmbeddingWorkerRunInputForcesOneLane(t *testing.T) {
	input := normalizeEmbeddingWorkerRunInput(EmbeddingWorkerRunInput{
		WorkerRunID:      " run ",
		Limit:            10,
		LeaseDuration:    -time.Second,
		RetryMaxAttempts: -1,
		RetryDelay:       -time.Second,
	})
	if input.WorkerRunID != "run" {
		t.Fatalf("worker run id = %q", input.WorkerRunID)
	}
	if input.Limit != 1 {
		t.Fatalf("limit = %d, want 1", input.Limit)
	}
	if input.LeaseDuration != defaultEmbeddingLeaseDuration {
		t.Fatalf("lease duration = %s, want %s", input.LeaseDuration, defaultEmbeddingLeaseDuration)
	}
	if input.RetryMaxAttempts != defaultEmbeddingWorkerMaxAttempts {
		t.Fatalf("retry attempts = %d", input.RetryMaxAttempts)
	}
	if input.RetryDelay != defaultEmbeddingWorkerRetryDelay {
		t.Fatalf("retry delay = %s", input.RetryDelay)
	}
}

func TestClassifyEmbeddingWorkFailure(t *testing.T) {
	now := time.Date(2026, 7, 5, 12, 0, 0, 0, time.UTC)
	failure := classifyEmbeddingWorkFailure(&EmbeddingRuntimeError{Kind: EmbeddingRuntimeErrorUnavailable, Message: "offline"}, EmbeddingWorkerRunInput{RetryMaxAttempts: 5, RetryDelay: 2 * time.Minute}, now)
	if !failure.Retryable {
		t.Fatal("runtime unavailable failure should be retryable")
	}
	if failure.ErrorCode != "embedding_runtime_unavailable" {
		t.Fatalf("error code = %q", failure.ErrorCode)
	}
	if failure.MaxAttempts != 5 {
		t.Fatalf("max attempts = %d", failure.MaxAttempts)
	}
	if failure.RetryDelay != 2*time.Minute {
		t.Fatalf("retry delay = %s", failure.RetryDelay)
	}
	if !failure.Now.Equal(now) {
		t.Fatalf("now = %s, want %s", failure.Now, now)
	}

	terminal := classifyEmbeddingWorkFailure(context.Canceled, EmbeddingWorkerRunInput{}, now)
	if terminal.Retryable {
		t.Fatal("generic failure should be terminal")
	}
	if terminal.ErrorCode != "embedding_failed" {
		t.Fatalf("terminal error code = %q", terminal.ErrorCode)
	}
}

func TestHashEmbeddingInputChangesWithPassageText(t *testing.T) {
	a := hashEmbeddingInput([]EmbeddingPassage{{PassageHash: "a", Text: "hello"}})
	b := hashEmbeddingInput([]EmbeddingPassage{{PassageHash: "a", Text: "hello world"}})
	if a == b {
		t.Fatal("hash should change when passage text changes")
	}
}
