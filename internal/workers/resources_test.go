package workers

import "testing"

func TestAvailableResourceSlotEnforcesCapacityOne(t *testing.T) {
	if got := availableResourceSlot(1, map[int]bool{}); got != 1 {
		t.Fatalf("slot = %d", got)
	}
	if got := availableResourceSlot(1, map[int]bool{1: true}); got != 0 {
		t.Fatalf("contended slot = %d", got)
	}
}

func TestAvailableResourceSlotSupportsFutureCapacity(t *testing.T) {
	if got := availableResourceSlot(3, map[int]bool{1: true, 3: true}); got != 2 {
		t.Fatalf("slot = %d", got)
	}
}
