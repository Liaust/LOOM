package notesprojection

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"
)

const MaxFailureDependencies = 8

// These are refusal dependencies, never publication or withdrawal evidence.
type FailureDependency struct {
	ObjectID string `json:"object_id"`
	Revision string `json:"revision"`
}

type InvalidSourcePathsError struct {
	Dependencies []FailureDependency `json:"dependencies"`
}

func (*InvalidSourcePathsError) Error() string {
	return "Notes projection has invalid source paths; refusing partial inventory"
}

func invalidSourcePaths(sources []SourceObject) error {
	failure := &InvalidSourcePathsError{}
	for _, source := range sources {
		if _, err := projectedPathForSource(source); err != nil {
			failure.Dependencies = append(failure.Dependencies, sourceDependency(source))
			if len(failure.Dependencies) == MaxFailureDependencies {
				break
			}
		}
	}
	return failure
}

func sourceDependency(source SourceObject) FailureDependency {
	// Observing a source again is not a change to its failed path/configuration.
	source.LastSeenAt, source.LastMaterializedAt = time.Time{}, nil
	raw, _ := json.Marshal(source)
	hash := sha256.Sum256(raw)
	return FailureDependency{ObjectID: source.KnowledgeObjectID, Revision: hex.EncodeToString(hash[:])}
}

// FailureChanged reuses the bounded targeted metadata reader. It does not list
// the corpus, stat/copy payloads, acknowledge work or grant export authority.
func (s Service) FailureChanged(ctx context.Context, dependencies []FailureDependency) (bool, error) {
	provider, ok := s.Sources.(TargetedSourceProvider)
	if !ok || len(dependencies) == 0 || len(dependencies) > MaxFailureDependencies {
		return false, fmt.Errorf("projection failure dependencies unavailable")
	}
	ids := make([]string, 0, len(dependencies))
	wanted := map[string]string{}
	for _, dependency := range dependencies {
		if dependency.ObjectID == "" || dependency.Revision == "" || wanted[dependency.ObjectID] != "" {
			return false, fmt.Errorf("invalid projection failure dependency")
		}
		ids = append(ids, dependency.ObjectID)
		wanted[dependency.ObjectID] = dependency.Revision
	}
	delta, err := provider.ReadProjectionSources(ctx, ids)
	if err != nil {
		return false, err
	}
	changed := false
	for _, source := range delta.Sources {
		prior, ok := wanted[source.KnowledgeObjectID]
		if !ok {
			return false, ErrDeltaUncertain
		}
		_, pathErr := projectedPathForSource(source)
		// A compatible reader upgrade can repair the predicate without changing
		// source bytes or timestamps. Validation remains metadata-only here.
		changed = changed || pathErr == nil || prior != sourceDependency(source).Revision
		delete(wanted, source.KnowledgeObjectID)
	}
	for id, reason := range delta.Withdrawn {
		if wanted[id] == "" || reason == "" {
			return false, ErrDeltaUncertain
		}
		changed = true
		delete(wanted, id)
	}
	if len(wanted) != 0 {
		return false, ErrDeltaUncertain
	}
	return changed, nil
}
