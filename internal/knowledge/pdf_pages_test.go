package knowledge

import "testing"

func TestSplitPDFPagesSelectsOnlyScannedOrSparsePagesForOCR(t *testing.T) {
	pages := SplitPDFPages("This page has enough useful embedded words for indexing.\f  x \fAnother useful embedded page with sufficient content.", 20)
	if len(pages) != 3 {
		t.Fatalf("pages = %d", len(pages))
	}
	if !pages[0].UsefulEmbeddedText || pages[1].UsefulEmbeddedText || !pages[2].UsefulEmbeddedText {
		t.Fatalf("usefulness = %#v", pages)
	}
}
