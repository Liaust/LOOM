package agents

import "encoding/json"

type operatingToolSpec struct {
	Name         string
	DisplayName  string
	Description  string
	InputSchema  json.RawMessage
	OutputSchema json.RawMessage
}

func operatingToolSpecs() []operatingToolSpec {
	return []operatingToolSpec{
		{
			Name:         "loom.tool_view.inspect",
			DisplayName:  "Inspect LOOM tool view",
			Description:  "Inspect the compact LOOM tool view currently exposed to this access context.",
			InputSchema:  json.RawMessage(`{"type":"object","properties":{"tool_view_ref":{"type":"string"}},"additionalProperties":false}`),
			OutputSchema: json.RawMessage(`{"type":"object","properties":{"tool_view":{"type":"object"},"entries":{"type":"array"}},"additionalProperties":true}`),
		},
		{
			Name:         "loom.capability.search",
			DisplayName:  "Search LOOM capabilities",
			Description:  "Search policy-visible capability candidates by intent and return compact results only.",
			InputSchema:  json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"},"limit":{"type":"integer"}},"required":["query"],"additionalProperties":false}`),
			OutputSchema: json.RawMessage(`{"type":"object","properties":{"candidates":{"type":"array"}},"additionalProperties":true}`),
		},
		{
			Name:         "loom.capability.inspect",
			DisplayName:  "Inspect LOOM capability",
			Description:  "Inspect one selected capability and retrieve its exact model-facing schema and selected usage documentation.",
			InputSchema:  json.RawMessage(`{"type":"object","properties":{"capability_ref":{"type":"string"},"query":{"type":"string"},"usage_section_label":{"type":"string"},"max_sections":{"type":"integer"},"max_chars_per_section":{"type":"integer"}},"required":["capability_ref"],"additionalProperties":false}`),
			OutputSchema: json.RawMessage(`{"type":"object","properties":{"tool_schema":{"type":"object"},"usage_sections":{"type":"array"}},"additionalProperties":true}`),
		},
		{
			Name:         "loom.capability.call",
			DisplayName:  "Call LOOM capability",
			Description:  "Call a selected LOOM capability through the normal policy and routing path.",
			InputSchema:  json.RawMessage(`{"type":"object","properties":{"capability_ref":{"type":"string"},"input":{"type":"object"},"request_approval":{"type":"boolean"},"approval_reason":{"type":"string"},"idempotency_key":{"type":"string"},"metadata":{"type":"object"}},"required":["capability_ref"],"additionalProperties":false}`),
			OutputSchema: json.RawMessage(`{"type":"object","properties":{"status":{"type":"string"},"capability_call_id":{"type":"string"},"route_id":{"type":"string"}},"additionalProperties":true}`),
		},
		{
			Name:         "loom.progress.inspect",
			DisplayName:  "Inspect LOOM progress",
			Description:  "Inspect an existing LOOM progress feed for a job, route, capability call, sync batch, or backup operation.",
			InputSchema:  json.RawMessage(`{"type":"object","properties":{"source":{"type":"string"},"source_kind":{"type":"string"},"source_ref":{"type":"string"}},"additionalProperties":false}`),
			OutputSchema: json.RawMessage(`{"type":"object","properties":{"feed":{"type":"object"},"updates":{"type":"array"}},"additionalProperties":true}`),
		},
		{
			Name:         "loom.worklog.write",
			DisplayName:  "Write LOOM worklog",
			Description:  "Write an inspectable LOOM worklog entry for this access context without creating agent memory.",
			InputSchema:  json.RawMessage(`{"type":"object","properties":{"entry_kind":{"type":"string","enum":["note","observation","decision","result","artifact_ref"]},"summary":{"type":"string"},"body":{"type":"string"},"object_ref":{"type":"string"},"metadata":{"type":"object"}},"required":["entry_kind","summary"],"additionalProperties":false}`),
			OutputSchema: json.RawMessage(`{"type":"object","properties":{"worklog_entry_id":{"type":"string"},"status":{"type":"string"}},"additionalProperties":true}`),
		},
	}
}
