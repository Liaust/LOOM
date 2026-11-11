package cloudstorage

import (
	"bytes"
	"encoding/json"
	"time"
)

// Deliberately excludes Borg's current path and arbitrary log messages.
type BorgProgress struct {
	OriginalBytes     int64 `json:"original_bytes"`
	CompressedBytes   int64 `json:"compressed_bytes"`
	DeduplicatedBytes int64 `json:"deduplicated_bytes"`
	Files             int64 `json:"files"`
	Finished          bool  `json:"finished"`
}

type DirectArchiveProgress struct {
	Phase      string        `json:"phase"`
	ObservedAt time.Time     `json:"observed_at"`
	Borg       *BorgProgress `json:"borg,omitempty"`
}

type borgProgressWriter struct {
	diagnostic boundedBorgExtractOutput
	line       []byte
	dropping   bool
	notify     func(BorgProgress)
	latest     BorgProgress
}

func (w *borgProgressWriter) Write(raw []byte) (int, error) {
	if w.notify == nil {
		_, _ = w.diagnostic.Write(raw)
		return len(raw), nil
	}
	for _, b := range raw {
		if b == '\n' || b == '\r' {
			if !w.dropping {
				w.consumeLine()
			} else {
				_, _ = w.diagnostic.Write([]byte("[oversized Borg progress/log line omitted]\n"))
			}
			w.line = w.line[:0]
			w.dropping = false
		} else if !w.dropping {
			if len(w.line) >= 64<<10 {
				w.line = w.line[:0]
				w.dropping = true
			} else {
				w.line = append(w.line, b)
			}
		}
	}
	return len(raw), nil
}

func (w *borgProgressWriter) Bytes() []byte {
	if len(w.line) > 0 && !w.dropping {
		w.consumeLine()
		w.line = nil
	}
	return w.diagnostic.Bytes()
}

func (w *borgProgressWriter) consumeLine() {
	if bytes.Contains(w.line, []byte("archive_progress")) {
		w.emit()
		return
	}
	_, _ = w.diagnostic.Write(w.line)
	_, _ = w.diagnostic.Write([]byte("\n"))
}

func (w *borgProgressWriter) emit() {
	var event struct {
		Type         string `json:"type"`
		Original     *int64 `json:"original_size"`
		Compressed   *int64 `json:"compressed_size"`
		Deduplicated *int64 `json:"deduplicated_size"`
		Files        *int64 `json:"nfiles"`
		Finished     bool   `json:"finished"`
	}
	if !bytes.Contains(w.line, []byte("archive_progress")) || json.Unmarshal(w.line, &event) != nil || event.Type != "archive_progress" {
		return
	}
	if event.Finished && event.Original == nil && event.Compressed == nil && event.Deduplicated == nil && event.Files == nil {
		w.latest.Finished = true
		w.notify(w.latest)
		return
	}
	if event.Original == nil || event.Compressed == nil || event.Deduplicated == nil || event.Files == nil || *event.Original < 0 || *event.Compressed < 0 || *event.Deduplicated < 0 || *event.Files < 0 {
		return
	}
	w.latest = BorgProgress{*event.Original, *event.Compressed, *event.Deduplicated, *event.Files, event.Finished}
	w.notify(w.latest)
}
