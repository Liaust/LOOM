package jobs

import (
	"testing"

	"loom.local/loom/internal/ids"
)

func TestRetainedProducerVersionBinding(t *testing.T) {
	for _, selector := range []RetainedProducerSelector{{Kind: "command"}, {Kind: "script", ProducerID: "slug", VersionID: ids.NewScriptVersionID()}, {Kind: "workflow", ProducerID: ids.NewWorkflowID(), VersionID: "latest"}, {Kind: "script", ProducerID: ids.NewScriptID(), VersionID: ids.NewWorkflowVersionID()}, {Kind: "workflow", ProducerID: ids.NewScriptID(), VersionID: ids.NewWorkflowVersionID()}} {
		t.Run(selector.Kind+"/"+selector.ProducerID, func(t *testing.T) {
			if _, e := (Service{}).resolveRetainedProducer(t.Context(), ids.NewScopeID(), selector); e == nil {
				t.Fatal("invalid exact producer selector accepted")
			}
		})
	}
}
func TestRetainedPackageProducerSelector(t *testing.T) { TestRetainedProducerVersionBinding(t) }
