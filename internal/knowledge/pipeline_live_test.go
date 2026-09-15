package knowledge

import (
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestOCRLive(t *testing.T) {
	if os.Getenv("LOOM_OCR_LIVE_TEST") != "1" {
		t.Skip("set LOOM_OCR_LIVE_TEST=1 to use the installed Tesseract runtime")
	}
	path := filepath.Join(t.TempDir(), "ocr-live.pgm")
	// A tiny valid PGM keeps this opt-in check bounded; environments may replace
	// it with a richer fixture through the full hardware acceptance script.
	if err := os.WriteFile(path, []byte("P2\n8 8\n255\n0 0 0 0 0 0 0 0\n0 255 255 0 0 255 255 0\n0 255 0 0 0 0 255 0\n0 255 255 0 0 255 255 0\n0 255 0 0 0 0 255 0\n0 255 0 0 0 0 255 0\n0 0 0 0 0 0 0 0\n0 0 0 0 0 0 0 0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, err := (TesseractOCR{}).Recognize(ctx, OCRRequest{ImagePath: path, Language: "eng"})
	if err != nil {
		t.Fatalf("live Tesseract invocation failed: %v", err)
	}
}

func TestVisionLive(t *testing.T) {
	if os.Getenv("LOOM_VISION_LIVE_TEST") != "1" {
		t.Skip("set LOOM_VISION_LIVE_TEST=1 to use the configured local vision model")
	}
	model := os.Getenv("LOOM_VISION_MODEL")
	if model == "" {
		t.Fatal("LOOM_VISION_MODEL is required for the live vision test")
	}
	image, err := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII=")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	result, err := (OllamaVisionRuntime{Endpoint: os.Getenv("LOOM_VISION_OLLAMA_URL"), Model: model}).Describe(ctx, VisionRequest{Image: image, Prompt: ImageDescriptionPrompt()})
	if err != nil || result.Description == "" {
		t.Fatalf("live vision invocation failed: result=%#v err=%v", result, err)
	}
}

func TestEmbeddingLive(t *testing.T) {
	if os.Getenv("LOOM_EMBEDDING_LIVE_TEST") != "1" {
		t.Skip("set LOOM_EMBEDDING_LIVE_TEST=1 to use the configured local embedding model")
	}
	runtime, err := NewOllamaEmbeddingRuntime(OllamaEmbeddingOptions{Endpoint: os.Getenv("LOOM_EMBEDDING_OLLAMA_URL"), Model: os.Getenv("LOOM_EMBEDDING_MODEL")})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	result, err := runtime.Embed(ctx, EmbeddingRuntimeRequest{Inputs: []string{"LOOM Notes local acceptance"}})
	if err != nil || len(result.Embeddings) != 1 || result.Dimensions == 0 {
		t.Fatalf("live embedding invocation failed: dimensions=%d vectors=%d err=%v", result.Dimensions, len(result.Embeddings), err)
	}
}
