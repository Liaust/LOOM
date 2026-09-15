package workers

import "time"

type ResourceLeaseRequest struct {
	ResourceKey                 string
	HolderID                    string
	WorkerInstanceID            *string
	WorkerRunID                 *string
	KnowledgePipelineRunID      *string
	KnowledgePipelineStageRunID *string
	TTL                         time.Duration
	Now                         time.Time
}

func availableResourceSlot(capacity int, active map[int]bool) int {
	for slot := 1; slot <= capacity; slot++ {
		if !active[slot] {
			return slot
		}
	}
	return 0
}
