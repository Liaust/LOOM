package capabilities

import "testing"

func TestValidationHelpersAcceptKnownValues(t *testing.T) {
	tests := []struct {
		name string
		ok   bool
	}{
		{name: "provider type", ok: ValidProviderType(ProviderTypeConnector)},
		{name: "provider status", ok: ValidProviderStatus(ProviderStatusActive)},
		{name: "health status", ok: ValidHealthStatus(HealthStatusOK)},
		{name: "availability status", ok: ValidAvailabilityStatus(AvailabilityStatusAvailable)},
		{name: "capability form", ok: ValidCapabilityForm(CapabilityFormJob)},
		{name: "risk level", ok: ValidRiskLevel(RiskLevelHigh)},
		{name: "capability class status", ok: ValidCapabilityClassStatus(CapabilityClassStatusActive)},
		{name: "endpoint status", ok: ValidEndpointStatus(EndpointStatusRegistered)},
		{name: "endpoint version status", ok: ValidEndpointVersionStatus(EndpointVersionStatusPendingReview)},
		{name: "runtime kind", ok: ValidRuntimeKind(RuntimeKindScript)},
		{name: "runtime binding status", ok: ValidRuntimeBindingStatus(RuntimeBindingStatusActive)},
		{name: "usage target kind", ok: ValidUsageTargetKind(UsageTargetKindCapabilityEndpoint)},
		{name: "usage review status", ok: ValidUsageReviewStatus(UsageReviewStatusApproved)},
		{name: "usage source kind", ok: ValidUsageSourceKind(UsageSourceKindManual)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if !tt.ok {
				t.Fatal("validation helper rejected known value")
			}
		})
	}
}

func TestValidationHelpersRejectUnknownValues(t *testing.T) {
	tests := []struct {
		name string
		ok   bool
	}{
		{name: "provider type", ok: ValidProviderType("external_app")},
		{name: "provider status", ok: ValidProviderStatus("ready")},
		{name: "health status", ok: ValidHealthStatus("healthy")},
		{name: "availability status", ok: ValidAvailabilityStatus("online")},
		{name: "capability form", ok: ValidCapabilityForm("tool")},
		{name: "risk level", ok: ValidRiskLevel("dangerous")},
		{name: "capability class status", ok: ValidCapabilityClassStatus("registered")},
		{name: "endpoint status", ok: ValidEndpointStatus("pending_review")},
		{name: "endpoint version status", ok: ValidEndpointVersionStatus("registered")},
		{name: "runtime kind", ok: ValidRuntimeKind("lambda")},
		{name: "runtime binding status", ok: ValidRuntimeBindingStatus("pending_review")},
		{name: "usage target kind", ok: ValidUsageTargetKind("app")},
		{name: "usage review status", ok: ValidUsageReviewStatus("active")},
		{name: "usage source kind", ok: ValidUsageSourceKind("runtime")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.ok {
				t.Fatal("validation helper accepted unknown value")
			}
		})
	}
}

func TestRuntimeBindingKnownKinds(t *testing.T) {
	for _, kind := range []string{
		RuntimeKindScript,
		RuntimeKindCommand,
		RuntimeKindHTTP,
		RuntimeKindNodeAgent,
		RuntimeKindNative,
		RuntimeKindModule,
		RuntimeKindWorkflow,
		RuntimeKindExternalProcess,
	} {
		if !ValidRuntimeKind(kind) {
			t.Fatalf("runtime kind %q should be valid", kind)
		}
	}
}

func TestRuntimeBindingKnownStatuses(t *testing.T) {
	for _, status := range []string{
		RuntimeBindingStatusRegistered,
		RuntimeBindingStatusActive,
		RuntimeBindingStatusDisabled,
		RuntimeBindingStatusDeprecated,
		RuntimeBindingStatusRevoked,
	} {
		if !ValidRuntimeBindingStatus(status) {
			t.Fatalf("runtime binding status %q should be valid", status)
		}
	}
}

func TestNormalizeJSONObjectDefaultsEmptyValues(t *testing.T) {
	for _, raw := range []string{"", "   "} {
		if got := string(normalizeJSONObject([]byte(raw))); got != "{}" {
			t.Fatalf("normalizeJSONObject(%q) = %q, want {}", raw, got)
		}
	}
	if got := string(normalizeJSONObject([]byte(`{"ok":true}`))); got != `{"ok":true}` {
		t.Fatalf("normalizeJSONObject changed object JSON: %s", got)
	}
}

func TestValidateJSONObject(t *testing.T) {
	if err := validateJSONObject("field", []byte(`{"ok":true}`)); err != nil {
		t.Fatalf("validateJSONObject rejected object: %v", err)
	}
	for _, raw := range []string{`[]`, `"nope"`, `null`, `{`} {
		if err := validateJSONObject("field", []byte(raw)); err == nil {
			t.Fatalf("validateJSONObject accepted %s", raw)
		}
	}
}

func TestValidExecutionAuthorizationLevel(t *testing.T) {
	for _, level := range []int{1, 2, 3, 4, 5} {
		if !ValidExecutionAuthorizationLevel(level) {
			t.Fatalf("level %d should be valid", level)
		}
	}

	for _, level := range []int{0, 6, -1} {
		if ValidExecutionAuthorizationLevel(level) {
			t.Fatalf("level %d should be invalid", level)
		}
	}
}
