package hermesschedules

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func TestFileReaderReadOnly(t *testing.T) {
	home := t.TempDir()
	cron := filepath.Join(home, "cron")
	if err := os.Mkdir(cron, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(cron, "jobs.json")
	input := []byte(`{"jobs":[],"updated_at":"2026-09-28T17:00:00Z"}`)
	if err := os.WriteFile(path, input, 0400); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	o := observer("")
	o.Read = FileReader(home)
	out, err := o.Observe(context.Background())
	if err != nil || out.Availability != Available || out.Jobs == nil {
		t.Fatal(out, err)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(cron)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data, input) || !before.ModTime().Equal(after.ModTime()) || before.Mode() != after.Mode() || len(entries) != 1 {
		t.Fatal("observation changed native files")
	}
	if _, err := o.Read(context.Background(), 8); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("file size not bounded: %v", err)
	}
}

func TestFileReaderUnavailableDoesNotCreateOrRepair(t *testing.T) {
	home := t.TempDir()
	o := observer("")
	o.Read = FileReader(home)
	out, err := o.Observe(context.Background())
	if !errors.Is(err, ErrUnavailable) || out.Availability != Unavailable || out.Jobs != nil {
		t.Fatal(out, err)
	}
	entries, err := os.ReadDir(home)
	if err != nil || len(entries) != 0 {
		t.Fatal("missing profile was provisioned", err)
	}
	cron := filepath.Join(home, "cron")
	if err := os.Mkdir(cron, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(cron, "jobs.json")
	for _, data := range []string{`[{"id":"a"}]`, `{"jobs":{"a":{"prompt":"private"}}}`, `{"jobs":[`} {
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		out, err := o.Observe(context.Background())
		if !errors.Is(err, ErrMalformed) || out.Jobs != nil {
			t.Fatal(out, err)
		}
		after, err := os.ReadFile(path)
		if err != nil || string(after) != data {
			t.Fatal("native repair occurred", err)
		}
	}
}

func TestFileReaderRejectsSpecialFiles(t *testing.T) {
	for _, kind := range []string{"directory", "symlink", "fifo", "unreadable"} {
		t.Run(kind, func(t *testing.T) {
			home := t.TempDir()
			cron := filepath.Join(home, "cron")
			if err := os.Mkdir(cron, 0700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(cron, "jobs.json")
			var err error
			switch kind {
			case "directory":
				err = os.Mkdir(path, 0700)
			case "symlink":
				err = os.Symlink("missing", path)
			case "fifo":
				err = unix.Mkfifo(path, 0600)
			case "unreadable":
				if os.Geteuid() == 0 {
					t.Skip("root bypasses file permissions")
				}
				err = os.WriteFile(path, []byte(`{"jobs":[]}`), 0000)
			}
			if err != nil {
				t.Fatal(err)
			}
			o := observer("")
			o.Read = FileReader(home)
			out, err := o.Observe(context.Background())
			if !errors.Is(err, ErrUnavailable) || out.Availability != Unavailable || out.Jobs != nil {
				t.Fatal(out, err)
			}
		})
	}
}
