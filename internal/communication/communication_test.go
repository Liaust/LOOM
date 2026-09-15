package communication

import (
	"loom.local/loom/internal/requestctx"
	"strings"
	"testing"
)

func TestProtectedFolderMessageKindsAreRegisteredAndBounded(t *testing.T) {
	for _, kind := range []string{KindProtectedFolderPreflight, KindProtectedFolderReconcile} {
		if !validMessageKind(kind) || !isProtectedFolderControlKind(kind) {
			t.Fatalf("protected-folder kind %q is not registered", kind)
		}
	}
	if validMessageKind("backup.protected_folder.unknown") {
		t.Fatal("unknown protected-folder kind was accepted")
	}
	payload := strings.Repeat("x", MaxProtectedFolderControlPayloadBytes+1)
	if len(payload) <= MaxProtectedFolderControlPayloadBytes {
		t.Fatal("invalid payload-size fixture")
	}
}

func TestDeclarationWatchRequiresAtomicOwner(t *testing.T) {
	if !validMessageKind(KindProjectWatchReconcile) || isProtectedFolderControlKind(KindProjectWatchReconcile) {
		t.Fatal("project watch kind inherits Box ownership")
	}
	if _, err := NewService(nil).Enqueue(t.Context(), requestctx.Context{}, EnqueueInput{Kind: KindProjectWatchReconcile}); err == nil || !strings.Contains(err.Error(), "atomic declaration owner") {
		t.Fatalf("generic queue bypass: %v", err)
	}
}
