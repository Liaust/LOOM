package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"loom.local/loom/internal/notesworkspace"
)

func TestPlanFileCustodyAndMutationConfirmation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "plan.json")
	p := notesworkspace.PDFDedupPlan{Schema: "loom.notes_pdf_dedup.v1", CreatedAt: time.Now().UTC()}
	p.PlanHash = p.Hash()
	if err := savePlan(path, p); err != nil {
		t.Fatal(err)
	}
	if _, err := readPlan(path); err != nil {
		t.Fatal(err)
	}
	if err := savePlan(path, p); err == nil {
		t.Fatal("plan overwritten")
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := readPlan(path); err == nil {
		t.Fatal("public plan accepted")
	}
	link := filepath.Join(filepath.Dir(path), "link")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, err := readPlan(link); err == nil {
		t.Fatal("linked plan accepted")
	}
	for _, command := range []string{"apply", "reclaim"} {
		if err := run(context.Background(), []string{command}); err == nil {
			t.Fatal("mutation without confirmation accepted")
		}
	}
}
