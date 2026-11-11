package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"loom.local/loom/internal/boxregistry"
)

func contextCatalog() boxregistry.Catalog {
	return boxregistry.Catalog{DefaultBox: "personal", Boxes: []boxregistry.Box{
		{ID: "box_personal", Key: "personal", RootRef: "box", Primary: true, Available: true},
		{ID: "box_company", Key: "company", RootRef: "box:box_company", Available: true},
	}}
}

func TestBoxMiddlewareBindsExplicitLocationAndLeavesSystemGlobal(t *testing.T) {
	catalog := contextCatalog()
	s := Server{services: Services{Boxes: &catalog}}
	for _, test := range []struct{ url, bound, expected string }{
		{"/v1/projects", "company", "box_company"},
		{"/v1/files/stat?root=box:box_company", "personal", "box_company"},
		{"/v1/files/stat?root=box", "company", "box_personal"},
		{"/v1/storage/workspace-archive/plan?box=company", "", "box_company"},
		{"/v1/project-scaffolds?box=company", "", "box_company"},
		{"/v1/system/host", "company", ""},
	} {
		t.Run(test.url, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, test.url, nil)
			r.Header.Set("X-Loom-Box", test.bound)
			called := false
			s.withBoxContext(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				called = true
				selection, scoped := boxregistry.FromContext(r.Context())
				if test.expected == "" {
					if scoped {
						t.Fatal("system acquired workspace scope")
					}
				} else if !scoped || len(selection.BoxIDs) != 1 || !selection.Contains(test.expected) {
					t.Fatalf("wrong context: %+v %v", selection, scoped)
				}
			})).ServeHTTP(httptest.NewRecorder(), r)
			if !called {
				t.Fatal("handler was not reached")
			}
		})
	}
}

func TestBoxIdempotencyPreservesPersonalAndRejectsScopeReuse(t *testing.T) {
	catalog := contextCatalog()
	s := Server{services: Services{Boxes: &catalog}}
	input := map[string]string{"request": "saved"}
	encode := func(ref string, all bool) string {
		selection, err := catalog.Select(boxregistry.Selection{AllBoxes: all}, ref)
		if err != nil {
			t.Fatal(err)
		}
		v, err := json.Marshal(s.boxIdempotencyInput(boxregistry.WithSelection(context.Background(), selection), input))
		if err != nil {
			t.Fatal(err)
		}
		return string(v)
	}
	legacy, _ := json.Marshal(input)
	personal, company, all := encode("personal", false), encode("company", false), encode("", true)
	if personal != string(legacy) {
		t.Fatal("Personal queued request hash changed")
	}
	if personal == company || personal == all || company == all {
		t.Fatal("different Box scope reused a cached response hash")
	}
}
