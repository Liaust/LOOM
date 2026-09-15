package minidashboard

import "testing"

func TestOverallSeverityPrecedence(t *testing.T) {
	tests := []struct {
		name      string
		items     []Condition
		active    bool
		available bool
		want      Severity
	}{
		{"healthy idle", nil, false, true, SeverityHealthy},
		{"healthy active", nil, true, true, SeverityActive},
		{"unknown without snapshot", nil, true, false, SeverityUnknown},
		{"warning overrides active", []Condition{condition("test.warning", "WARNING test", SeverityWarning, AttentionWork)}, true, true, SeverityWarning},
		{"critical overrides active", []Condition{condition("test.critical", "CRITICAL test", SeverityCritical, AttentionRuntime)}, true, true, SeverityCritical},
		{"critical overrides warning", []Condition{condition("test.warning", "WARNING test", SeverityWarning, AttentionResource), condition("test.critical", "CRITICAL test", SeverityCritical, AttentionFreshness)}, false, true, SeverityCritical},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := OverallSeverity(tt.items, tt.active, tt.available); got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestAttentionOrdering(t *testing.T) {
	tests := []struct {
		name string
		in   []Condition
		want string
	}{
		{"severity first", []Condition{condition("storage.warning", "WARNING storage", SeverityWarning, AttentionResource), condition("runtime.critical", "loomd OFFLINE", SeverityCritical, AttentionRuntime)}, "runtime.critical"},
		{"same severity priority", []Condition{condition("node.warning", "Node OFFLINE", SeverityWarning, AttentionNode), condition("backup.warning", "WARNING backup", SeverityWarning, AttentionProtection), condition("storage.warning", "WARNING storage", SeverityWarning, AttentionResource)}, "storage.warning"},
		{"stable code tie break", []Condition{condition("z.warning", "WARNING z", SeverityWarning, AttentionWork), condition("a.warning", "WARNING a", SeverityWarning, AttentionWork)}, "a.warning"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, additional, ok := SelectAttention(tt.in)
			if !ok || got.Code != tt.want || additional != len(tt.in)-1 {
				t.Fatalf("got %#v additional=%d ok=%v", got, additional, ok)
			}
		})
	}
}
