package supportbundle

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const logTruncationMarker = "[truncated: showing tail]\n"

func collectLogs(_ context.Context, collection CollectionContext) (CollectorOutput, error) {
	limit := itemLimit(collection.Options)
	paths := limitItems(collection.Options.LogPaths, limit)
	if len(paths) == 0 {
		return CollectorOutput{SkipReason: "log_path_unavailable"}, nil
	}
	output := CollectorOutput{}
	for i, path := range paths {
		data, truncated, err := readLogTail(path, collection.Options.MaxBytes)
		if err != nil {
			output.Warnings = append(output.Warnings, fmt.Sprintf("%s: %v", path, err))
			continue
		}
		if truncated {
			output.Warnings = append(output.Warnings, fmt.Sprintf("%s: truncated to tail", path))
			output.Truncated = true
		}
		output.Files = append(output.Files, File{
			Path:         fmt.Sprintf("logs/%02d-%s.txt", i+1, safeLogArchiveName(path)),
			ContentType:  "text/plain",
			PrivacyClass: PrivacyLogExcerpt,
			Data:         data,
		})
	}
	if len(output.Files) == 0 {
		output.SkipReason = "log_path_unavailable"
	}
	return output, nil
}

func readLogTail(path string, maxBytes int64) ([]byte, bool, error) {
	if maxBytes <= 0 {
		maxBytes = DefaultMaxBytes
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, false, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, false, err
	}
	truncated := info.Size() > maxBytes
	if truncated {
		budget := maxBytes - int64(len(logTruncationMarker))
		if budget < 1 {
			budget = maxBytes
		}
		if _, err := file.Seek(info.Size()-budget, io.SeekStart); err != nil {
			return nil, false, err
		}
		data, err := io.ReadAll(io.LimitReader(file, budget))
		if err != nil {
			return nil, false, err
		}
		if budget == maxBytes {
			return data, true, nil
		}
		return append([]byte(logTruncationMarker), data...), true, nil
	}
	data, err := io.ReadAll(io.LimitReader(file, maxBytes+1))
	if err != nil {
		return nil, false, err
	}
	if int64(len(data)) > maxBytes {
		return data[:maxBytes], true, nil
	}
	return data, false, nil
}

func safeLogArchiveName(path string) string {
	name := filepath.Base(strings.TrimSpace(path))
	if name == "." || name == string(filepath.Separator) || name == "" {
		name = "loom.log"
	}
	replacer := strings.NewReplacer("/", "_", "\\", "_", ":", "_", " ", "_")
	return replacer.Replace(name)
}
