package loomcli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"loom.local/loom/internal/objects"
)

func TestObjectIngestLabelPDFNavigation(t *testing.T) {
	mime, path := "application/pdf", "/managed/research.pdf"
	detail := objects.ObjectDetail{File: &objects.FileMetadata{MimeType: &mime, ManagedSourcePath: &path, TextExtractable: false}}
	var out bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&out)
	renderObjectDetail(cmd, detail)
	for _, want := range []string{"Direct text-ingest support: false", "not Notes extraction availability", "availability not checked", "loom notes read '/managed/research.pdf'"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("missing %q: %s", want, out.String())
		}
	}
	if strings.Contains(out.String(), "notes objects show") {
		t.Fatal("global Objects ID used as Notes object")
	}
	path = "unsafe\npath"
	out.Reset()
	renderObjectDetail(cmd, detail)
	if strings.Contains(out.String(), path) || !strings.Contains(out.String(), "loom notes read --help") {
		t.Fatal("unsafe reader route")
	}
}
