package knowledge

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestRetryableKnowledgeErrorClassification(t *testing.T) {
	if RetryableKnowledgeError(sql.ErrNoRows) {
		t.Fatal("sql.ErrNoRows should not be retryable")
	}
	if RetryableKnowledgeError(fmt.Errorf("%w: source_path is required for text extraction", ErrInvalid)) {
		t.Fatal("invalid source path should not be retryable")
	}
	if !RetryableKnowledgeError(context.DeadlineExceeded) {
		t.Fatal("deadline exceeded should be retryable")
	}
	if !RetryableKnowledgeError(fmt.Errorf("temporary lock timeout")) {
		t.Fatal("temporary lock timeout should be retryable")
	}
}

func TestKnowledgeRetryDelayCapsAtMax(t *testing.T) {
	delay := knowledgeRetryDelay(time.Second, 5*time.Second, 10)
	if delay != 5*time.Second {
		t.Fatalf("delay = %s, want 5s cap", delay)
	}
}

func TestNormalizeKnowledgeIndexerRunInputDefaults(t *testing.T) {
	input := normalizeKnowledgeIndexerRunInput(KnowledgeIndexerRunInput{Limit: 500})
	if input.Limit != maxKnowledgeClaimLimit {
		t.Fatalf("limit = %d, want %d", input.Limit, maxKnowledgeClaimLimit)
	}
	if input.LeaseDuration != defaultKnowledgeLeaseDuration {
		t.Fatalf("lease = %s, want %s", input.LeaseDuration, defaultKnowledgeLeaseDuration)
	}
	if input.MaxTextBytesPerObject != 5*1024*1024 {
		t.Fatalf("max bytes = %d, want 5 MiB", input.MaxTextBytesPerObject)
	}
	if input.MaxExtractedTextBytes != 10*1024*1024 {
		t.Fatalf("max extracted bytes = %d, want 10 MiB", input.MaxExtractedTextBytes)
	}
	if input.RetryMaxAttempts != defaultKnowledgeRetryMaxAttempts {
		t.Fatalf("retry attempts = %d, want %d", input.RetryMaxAttempts, defaultKnowledgeRetryMaxAttempts)
	}
}

func TestKnowledgeIndexerRunResultExtractionCounters(t *testing.T) {
	var result KnowledgeIndexerRunResult
	for _, status := range []string{
		ExtractionStatusExtracted,
		ExtractionStatusMetadataOnly,
		ExtractionStatusUnsupportedBodyExtraction,
		ExtractionStatusTooLarge,
		ExtractionStatusPasswordRequired,
		ExtractionStatusNoEmbeddedText,
		ExtractionStatusOCRDeferred,
		ExtractionStatusSourceUnavailable,
	} {
		result.incrementExtractionCounter(status)
	}
	if result.Extracted != 1 ||
		result.MetadataOnly != 2 ||
		result.TooLarge != 1 ||
		result.PasswordRequired != 1 ||
		result.NoEmbeddedText != 2 ||
		result.SourceUnavailable != 1 {
		t.Fatalf("counters = %#v", result)
	}
}

func TestClaimKnowledgeWorkRequiresWorkerRunID(t *testing.T) {
	_, err := NewService(new(sql.DB)).ClaimKnowledgeWork(context.Background(), "", KnowledgeWorkClaimOptions{})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("error = %v, want ErrInvalid", err)
	}
}
