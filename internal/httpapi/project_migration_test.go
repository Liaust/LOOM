package httpapi

import (
	"context"
	"net/http"
	"testing"

	"loom.local/loom/internal/projectapply"
	pc "loom.local/loom/internal/projectcontracts"
)

type migrationHTTPStub struct {
	declarationServiceStub
	conversionCalls int
}

func (s *migrationHTTPStub) ConversionAssessment(context.Context, projectapply.Principal, pc.DeclarationPlanRequest) (pc.DeclarationMigrationAssessment, error) {
	s.conversionCalls++
	return pc.DeclarationMigrationAssessment{}, declarationFailure(pc.DeclarationUnsupported, "fixture_conversion")
}
func TestDeclarationMigrationHTTPAdmission(t *testing.T) {
	for _, query := range []string{"conversion_preview=1", "conversion_preview=True", "conversion_preview=", "conversion_preview=true&conversion_preview=false", "conversion_preview=true&private=yes", "conversion_preview=true&node_ref=main&node_ref=other", "conversion_preview=true&node_ref=", "conversion_preview=true;private=yes"} {
		t.Run(query, func(t *testing.T) {
			s := &migrationHTTPStub{}
			h, _ := declarationTestHandler(s)
			w := declarationHTTP(t, h, "GET", "/v1/projects/example/declaration-status?"+query, "")
			if w.Code != http.StatusUnprocessableEntity || s.calls != 0 || s.conversionCalls != 0 {
				t.Fatalf("admitted query %d %s", w.Code, w.Body.String())
			}
		})
	}
	for _, value := range []string{"true", "false"} {
		t.Run(value, func(t *testing.T) {
			s := &migrationHTTPStub{}
			h, _ := declarationTestHandler(s)
			w := declarationHTTP(t, h, "GET", "/v1/projects/example/declaration-status?conversion_preview="+value, "")
			if value == "true" {
				if w.Code != 422 || s.conversionCalls != 1 || s.calls != 0 {
					t.Fatal("optional route lost")
				}
			} else if w.Code != 200 || s.calls != 1 || s.conversionCalls != 0 {
				t.Fatal("ordinary status changed")
			}
		})
	}
	s := &declarationServiceStub{}
	h, _ := declarationTestHandler(s)
	w := declarationHTTP(t, h, "GET", "/v1/projects/example/declaration-status?conversion_preview=true", "")
	if w.Code != 422 || s.calls != 0 {
		t.Fatal("missing optional interface fell back")
	}
}
