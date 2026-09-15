package knowledge

import (
	"errors"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

func TestPrepareImageEnforcesPixelLimitBeforeInference(t *testing.T) {
	path := filepath.Join(t.TempDir(), "image.png")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = png.Encode(file, image.NewRGBA(image.Rect(0, 0, 4, 3))); err != nil {
		t.Fatal(err)
	}
	file.Close()
	prepared, err := PrepareImage(path, "", ImagePreparePolicy{MaxBytes: 1024, MaxPixels: 12})
	if err != nil {
		t.Fatal(err)
	}
	if prepared.Width != 4 || prepared.Height != 3 {
		t.Fatalf("prepared=%#v", prepared)
	}
	if _, err = PrepareImage(path, "", ImagePreparePolicy{MaxBytes: 1024, MaxPixels: 11}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("error=%v", err)
	}
}
