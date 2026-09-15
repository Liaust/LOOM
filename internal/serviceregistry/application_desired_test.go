package serviceregistry

import "testing"

func TestApplicationInspectDesiredConfiguration(t *testing.T) {
	r, h, q, _ := applicationTestRuntime(t)
	applied, err := r.Execute(t.Context(), 1234, q)
	if err != nil {
		t.Fatal(err)
	}
	if applied.InputDigest != q.Digest() || applied.InstallationRevision != q.Digest() {
		t.Fatal("public request digest differs from installer journal")
	}
	q.Operation = "inspect"
	q.OperationToken = "status"
	result, err := r.Execute(t.Context(), 1234, q)
	if err != nil || result.DesiredMatches == nil || !*result.DesiredMatches || result.Current == nil {
		t.Fatalf("inspect=%+v %v", result, err)
	}
	q.Manifest.Process.MemoryMaxBytes++
	result, err = r.Execute(t.Context(), 1234, q)
	if err != nil || result.DesiredMatches == nil || *result.DesiredMatches {
		t.Fatalf("different desired configuration claimed applied: %+v %v", result, err)
	}
	if h.restarts != 1 {
		t.Fatal("inspection changed the service")
	}
}
