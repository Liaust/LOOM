package loomdapp

import (
	"errors"
	"reflect"
	"testing"
)

type recordingMainDatabaseCloser struct {
	order *[]string
	err   error
}

func (c recordingMainDatabaseCloser) Close() error {
	*c.order = append(*c.order, "main")
	return c.err
}

type recordingProvenanceRuntimeCloser struct {
	order *[]string
}

func (c recordingProvenanceRuntimeCloser) Close() {
	*c.order = append(*c.order, "provenance")
}

func TestCloseRuntimeDatabasesUsesReverseStartupOrder(t *testing.T) {
	var order []string
	wantErr := errors.New("main close failed")
	err := closeRuntimeDatabases(
		recordingMainDatabaseCloser{order: &order, err: wantErr},
		recordingProvenanceRuntimeCloser{order: &order},
	)
	if !errors.Is(err, wantErr) {
		t.Fatalf("closeRuntimeDatabases() error = %v, want %v", err, wantErr)
	}
	if want := []string{"provenance", "main"}; !reflect.DeepEqual(order, want) {
		t.Fatalf("close order = %v, want %v", order, want)
	}
}

func TestCloseRuntimeDatabasesAcceptsPartiallyOpenedRuntime(t *testing.T) {
	var order []string
	if err := closeRuntimeDatabases(recordingMainDatabaseCloser{order: &order}, nil); err != nil {
		t.Fatal(err)
	}
	if want := []string{"main"}; !reflect.DeepEqual(order, want) {
		t.Fatalf("close order = %v, want %v", order, want)
	}
}
