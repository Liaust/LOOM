package knowledge

import (
	"context"
	"testing"
)

func TestRunHeavyCommandUsesTemporaryRootAndCleansIt(t *testing.T) {
	result, err := RunHeavyCommand(context.Background(), DefaultHeavyResourcePolicy(), "/bin/sh", "-c", "printf ok > result")
	if err != nil {
		t.Fatal(err)
	}
	if result.TempBytes != 2 {
		t.Fatalf("temp bytes = %d", result.TempBytes)
	}
}
