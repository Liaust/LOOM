package supportbundle

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path"
	"sort"
	"strings"
)

func WriteArchive(outputPath string, files []File) error {
	if strings.TrimSpace(outputPath) == "" {
		return fmt.Errorf("output path is required")
	}
	out, err := os.Create(outputPath)
	if err != nil {
		return err
	}
	defer func() { _ = out.Close() }()
	return WriteArchiveTo(out, files)
}

func WriteArchiveTo(w io.Writer, files []File) error {
	normalized, err := normalizeArchiveFiles(files)
	if err != nil {
		return err
	}
	gw, err := gzip.NewWriterLevel(w, gzip.BestCompression)
	if err != nil {
		return err
	}
	gw.Name = ArchiveRoot + ".tar"
	gw.ModTime = fixedTime()
	tw := tar.NewWriter(gw)
	for _, file := range normalized {
		name := path.Join(ArchiveRoot, file.Path)
		header := &tar.Header{
			Name:    name,
			Mode:    0o644,
			Size:    int64(len(file.Data)),
			ModTime: fixedTime(),
		}
		if err := tw.WriteHeader(header); err != nil {
			_ = tw.Close()
			_ = gw.Close()
			return err
		}
		if _, err := tw.Write(file.Data); err != nil {
			_ = tw.Close()
			_ = gw.Close()
			return err
		}
	}
	if err := tw.Close(); err != nil {
		_ = gw.Close()
		return err
	}
	return gw.Close()
}

func normalizeArchiveFiles(files []File) ([]File, error) {
	result := make([]File, 0, len(files))
	seen := map[string]bool{}
	for _, file := range files {
		clean := path.Clean(strings.TrimSpace(strings.ReplaceAll(file.Path, "\\", "/")))
		if clean == "." || clean == "/" || strings.HasPrefix(clean, "../") || strings.HasPrefix(clean, "/") {
			return nil, fmt.Errorf("invalid archive file path %q", file.Path)
		}
		if seen[clean] {
			return nil, fmt.Errorf("duplicate archive file path %q", clean)
		}
		seen[clean] = true
		file.Path = clean
		result = append(result, file)
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].Path < result[j].Path
	})
	return result, nil
}
