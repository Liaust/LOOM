package supportbundle

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"loom.local/loom/internal/redaction"
)

func TestRedactFileRedactsJSONAndAliasesPaths(t *testing.T) {
	root := filepath.Clean("/Users/tester/loom-box")
	file := File{
		Path:        "summaries/leaky.json",
		ContentType: "application/json",
		Data: []byte(`{
			"password": "hidden",
			"message": "Authorization: Bearer abc123",
			"path": "` + filepath.ToSlash(filepath.Join(root, "Notes", "a.md")) + `"
		}`),
	}
	out, report := RedactFile(file, Options{
		PathAliases: []PathAlias{{Label: "$LOOM_BOX", Root: root}},
	})
	text := string(out.Data)
	for _, leaked := range []string{"hidden", "abc123", root} {
		if strings.Contains(text, leaked) {
			t.Fatalf("sensitive value %q leaked in %s", leaked, text)
		}
	}
	if !strings.Contains(text, redaction.Replacement) || !strings.Contains(text, "$LOOM_BOX/Notes/a.md") {
		t.Fatalf("redaction or alias marker missing: %s", text)
	}
	if report.Redactions != 2 || report.PathAliases != 1 {
		t.Fatalf("report = %#v, want 2 redactions and 1 alias", report)
	}
}

func TestCreateRecordsRedactionsAndWritesRedactedArchive(t *testing.T) {
	root := filepath.Clean("/Users/tester/loom-box")
	output := filepath.Join(t.TempDir(), "support.tar.gz")
	collector := Collector{
		Key:          "leaky",
		Title:        "Leaky",
		Profiles:     []Profile{ProfileDefault},
		PrivacyClass: PrivacyDiagnosticSummary,
		Collect: func(context.Context, CollectionContext) (CollectorOutput, error) {
			return CollectorOutput{Files: []File{{
				Path:        "summaries/leaky.json",
				ContentType: "application/json",
				Data: []byte(`{
					"token": "secret-token",
					"database": "postgres://loom:secret@example/loom",
					"path": "` + filepath.ToSlash(filepath.Join(root, "Notes", "a.md")) + `"
				}`),
			}}}, nil
		},
	}
	result, err := Create(context.Background(), Options{
		OutputPath:       output,
		PathAliases:      []PathAlias{{Label: "$LOOM_BOX", Root: root}},
		Now:              fixedTestTime(),
		RedactionProfile: RedactionProfileDefault,
	}, []Collector{collector})
	if err != nil {
		t.Fatalf("Create returned error: %v", err)
	}
	if result.Manifest.Counts.Redactions != 2 {
		t.Fatalf("manifest redactions = %d, want 2; sections=%#v", result.Manifest.Counts.Redactions, result.Sections)
	}
	text := readArchiveText(t, output, "loom-support/summaries/leaky.json")
	for _, leaked := range []string{"secret-token", "loom:secret", root} {
		if strings.Contains(text, leaked) {
			t.Fatalf("sensitive value %q leaked in archive file: %s", leaked, text)
		}
	}
	if !strings.Contains(text, "$LOOM_BOX/Notes/a.md") {
		t.Fatalf("path alias missing in archive file: %s", text)
	}
}

func readArchiveText(t *testing.T, archivePath, wantName string) string {
	t.Helper()
	file, err := os.Open(archivePath)
	if err != nil {
		t.Fatalf("open archive: %v", err)
	}
	defer file.Close()
	gz, err := gzip.NewReader(file)
	if err != nil {
		t.Fatalf("gzip reader: %v", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		header, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("tar next: %v", err)
		}
		if header.Name != wantName {
			continue
		}
		data, err := io.ReadAll(tr)
		if err != nil {
			t.Fatalf("read archive entry: %v", err)
		}
		return string(data)
	}
	t.Fatalf("archive entry %s not found", wantName)
	return ""
}
