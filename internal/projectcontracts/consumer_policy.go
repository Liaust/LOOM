package projectcontracts

import (
	"fmt"
	"reflect"
	"sort"

	"loom.local/loom/internal/nodeagent/watchedroots"
)

func mergeConsumerSelections(target, incoming *watchedroots.ConsumerSelections) error {
	if target == nil || incoming == nil {
		return fmt.Errorf("independent root requires consumer selections")
	}
	for _, pair := range []struct {
		name string
		dst  **watchedroots.PathSelection
		src  *watchedroots.PathSelection
	}{
		{"backup", &target.Backup, incoming.Backup}, {"sync", &target.Sync, incoming.Sync}, {"index", &target.Index, incoming.Index},
	} {
		if pair.src == nil {
			continue
		}
		if *pair.dst == nil {
			*pair.dst = pair.src
			continue
		}
		if !reflect.DeepEqual(*pair.dst, pair.src) {
			return fmt.Errorf("incompatible %s selections for the same root", pair.name)
		}
	}
	return nil
}

// Observe the union, then let each consumer select independently. Only exclusions
// common to every consumer can safely narrow the observation envelope.
func observationEnvelope(selections *watchedroots.ConsumerSelections) ([]string, []string) {
	if selections == nil {
		return []string{"**/*"}, []string{}
	}
	include := map[string]bool{}
	var exclude map[string]bool
	for _, selection := range []*watchedroots.PathSelection{selections.Backup, selections.Sync, selections.Index} {
		if selection == nil {
			continue
		}
		for _, pattern := range selection.Include {
			include[pattern] = true
		}
		next := map[string]bool{}
		for _, pattern := range selection.Exclude {
			if exclude == nil || exclude[pattern] {
				next[pattern] = true
			}
		}
		exclude = next
	}
	keys := func(values map[string]bool) []string {
		out := make([]string, 0, len(values))
		for v := range values {
			out = append(out, v)
		}
		sort.Strings(out)
		return out
	}
	if len(include) == 0 {
		include["**/*"] = true
	}
	return keys(include), keys(exclude)
}
