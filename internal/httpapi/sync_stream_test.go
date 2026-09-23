package httpapi

import (
	"bytes"
	"io"
	"mime/multipart"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSyncedMultipartMetadataAndExactContent(t *testing.T) {
	for _, tc := range []struct {
		name, metadata             string
		firstContent, extra, valid bool
	}{
		{"valid", `{"size_bytes":3}`, false, false, true},
		{"order", `{}`, true, false, false},
		{"unknown", `{"unknown":true}`, false, false, false},
		{"two-json", `{} {}`, false, false, false},
		{"inline", `{"content_base64":"YWJj"}`, false, false, false},
		{"large-metadata", `{"logical_name":"` + strings.Repeat("x", 1<<20) + `"}`, false, false, false},
		{"trailing", `{}`, false, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var b bytes.Buffer
			m := multipart.NewWriter(&b)
			if tc.firstContent {
				p, _ := m.CreateFormField("content")
				_, _ = io.WriteString(p, "abc")
			}
			p, _ := m.CreateFormField("metadata")
			_, _ = io.WriteString(p, tc.metadata)
			p, _ = m.CreateFormFile("content", "a.pdf")
			_, _ = io.WriteString(p, "abc")
			if tc.extra {
				p, _ = m.CreateFormField("extra")
				_, _ = io.WriteString(p, "extra")
			}
			_ = m.Close()
			r := httptest.NewRequest("POST", "/", &b)
			r.Header.Set("Content-Type", m.FormDataContentType())
			_, content, err := decodeSyncedObjectStream(r)
			if err == nil {
				var raw []byte
				raw, err = io.ReadAll(content)
				if tc.valid && string(raw) != "abc" {
					t.Fatal("changed content")
				}
			}
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%t: %v", tc.valid, err)
			}
		})
	}
}
