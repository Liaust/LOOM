package projectapply

import (
	"encoding/json"
	pc "loom.local/loom/internal/projectcontracts"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestDeclarationMigrationReadBoundary(t *testing.T) {
	for _, s := range []string{"", "/tmp/private", "../project", "a/b", "a\\b", "bad\nselector", strings.Repeat("x", 129)} {
		if migrationSelector(s) {
			t.Fatalf("unsafe selector %q", s)
		}
	}
	for _, s := range []string{"registered-project", "project_01ARZ3NDEKTSV4RRFFQ69G5FAV"} {
		if !migrationSelector(s) {
			t.Fatal("safe selector refused")
		}
	}
	service := NewService(nil, nil, nil, nil)
	if _, e := service.ConversionAssessment(t.Context(), Principal{}, pc.DeclarationPlanRequest{}); e == nil {
		t.Fatal("unconfigured conversion")
	}
	for _, raw := range []string{`{} {}`, `{"unknown":true}`, `null {}`, `[]`} {
		var p pc.ProjectContract
		if e := migrationDecode([]byte(raw), &p); e == nil {
			t.Fatalf("unsafe owner JSON %s", raw)
		}
	}
	var p json.RawMessage
	if migrationDecode([]byte(`{}`), &p) != nil {
		t.Fatal("valid JSON refused")
	}
}

func TestDeclarationMigrationDirectoryDescriptor(t *testing.T) {
	root := t.TempDir()
	fifo := filepath.Join(root, "fifo")
	if e := syscall.Mkfifo(fifo, 0600); e != nil {
		t.Fatal(e)
	}
	file := filepath.Join(root, "file")
	if e := os.WriteFile(file, []byte("fixture"), 0600); e != nil {
		t.Fatal(e)
	}
	link := filepath.Join(root, "link")
	if e := os.Symlink(root, link); e != nil {
		t.Fatal(e)
	}
	for _, path := range []string{fifo, file, link} {
		t.Run(filepath.Base(path), func(t *testing.T) {
			done := make(chan error, 1)
			go func() {
				f, e := migrationOpenDirectory(path)
				if f != nil {
					f.Close()
				}
				done <- e
			}()
			select {
			case e := <-done:
				if e == nil {
					t.Fatal("non-directory/symlink admitted")
				}
			case <-time.After(time.Second):
				if filepath.Base(path) == "fifo" {
					fd, e := syscall.Open(path, syscall.O_RDWR|syscall.O_NONBLOCK, 0)
					if e == nil {
						syscall.Close(fd)
					}
				}
				t.Fatal("metadata open blocked on non-directory")
			}
		})
	}
	f, e := migrationOpenDirectory(root)
	if e != nil {
		t.Fatal(e)
	}
	defer f.Close()
	info, e := f.Stat()
	if e != nil || !info.IsDir() {
		t.Fatal("directory control failed")
	}
}
