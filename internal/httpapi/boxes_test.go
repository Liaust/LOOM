package httpapi

import (
	"errors"
	"net/http/httptest"
	"testing"

	"loom.local/loom/internal/boxregistry"
)

func TestBoxSelectionUsesBoundDefaultAndDoesNotBroaden(t *testing.T) {
	c := boxregistry.Catalog{DefaultBox: "personal", Boxes: []boxregistry.Box{
		{ID: "box_personal", Key: "personal", RootRef: "box", Available: true},
		{ID: "box_company", Key: "company", RootRef: "box:box_company", Available: true},
	}}
	s := Server{services: Services{Boxes: &c}}
	r := httptest.NewRequest("GET", "/v1/projects", nil)
	r.Header.Set("X-Loom-Box", "company")
	selection, err := s.boxSelection(r)
	if err != nil || !selection.Contains("box_company") || selection.Contains("box_personal") {
		t.Fatalf("bound default %+v %v", selection, err)
	}
	r = httptest.NewRequest("GET", "/v1/projects?box=missing", nil)
	if _, err := s.boxSelection(r); !errors.Is(err, boxregistry.ErrUnknown) {
		t.Fatal(err)
	}
	r = httptest.NewRequest("GET", "/v1/projects?all_boxes=true&box=company", nil)
	if _, err := s.boxSelection(r); !errors.Is(err, boxregistry.ErrSelection) {
		t.Fatal(err)
	}
}
