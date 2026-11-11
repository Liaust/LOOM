package workers

import (
	"encoding/json"
	"testing"
	"time"
)

func pressureFixture(at time.Time, cpu float64) ResourcePressureSnapshot {
	zero := 0.0
	memory, disk := int64(2<<30), int64(4<<30)
	return ResourcePressureSnapshot{SampledAt: at, CPUSomeAvg10: &cpu, MemoryFullAvg10: &zero, IOFullAvg10: &zero, AvailableMemoryBytes: &memory, AvailableDiskBytes: &disk}
}

func TestResourcePressureSustainedHysteresisAndFiniteOptionalRetry(t *testing.T) {
	p := defaultResourcePressurePolicy()
	at := time.Now().UTC()
	a, state := EvaluateResourcePressure(pressureFixture(at, 50), p, ResourcePressureState{}, 200, 512<<20)
	if !a.Admitted || a.BudgetMaxUnits != 200 || state.Constrained {
		t.Fatal("one high sample throttled work")
	}
	a, state = EvaluateResourcePressure(pressureFixture(at.Add(10*time.Second), 50), p, state, 200, 512<<20)
	if !a.Admitted || a.AllowOptional || a.BudgetMaxUnits != 8 || a.NextEligibleAt == nil || a.NextEligibleAt.Sub(a.SampledAt) > 30*time.Second {
		t.Fatalf("%+v", a)
	}
	retry := *a.NextEligibleAt
	a, state = EvaluateResourcePressure(pressureFixture(retry, 50), p, state, 200, 512<<20)
	if !a.AllowOptional {
		t.Fatal("sustained pressure permanently starved optional work")
	}
	// Sampling ordinary traffic does not spend the optional admission deadline.
	a, state = EvaluateResourcePressure(pressureFixture(retry.Add(time.Second), 50), p, state, 200, 512<<20)
	if !a.AllowOptional {
		t.Fatal("ordinary samples spent optional admission")
	}
	a, state = EvaluateResourcePressure(pressureFixture(retry.Add(2*time.Second), 0), p, state, 200, 512<<20)
	if !state.Constrained {
		t.Fatal("one low sample defeated hysteresis")
	}
	a, state = EvaluateResourcePressure(pressureFixture(retry.Add(12*time.Second), 0), p, state, 200, 512<<20)
	if state.Constrained || !a.AllowOptional || a.BudgetMaxUnits != 200 {
		t.Fatal("sustained relief did not release pressure budget")
	}
}

func TestResourcePressureUnknownAndSafetyNeverUseAgeOverride(t *testing.T) {
	p := defaultResourcePressurePolicy()
	at := time.Now().UTC()
	a, state := EvaluateResourcePressure(ResourcePressureSnapshot{SampledAt: at}, p, ResourcePressureState{Constrained: true}, 200, 512<<20)
	if !a.Admitted || !a.AllowOptional || a.Reason != "pressure_metrics_unknown" || a.BudgetMaxUnits != 200 || state.BlockedUntil != nil {
		t.Fatalf("unknown metrics became zero pressure or a pause: %+v", a)
	}
	for _, metric := range []string{"memory", "disk"} {
		sample := pressureFixture(at, 0)
		low := int64(1)
		if metric == "memory" {
			sample.AvailableMemoryBytes = &low
		} else {
			sample.AvailableDiskBytes = &low
		}
		a, state = EvaluateResourcePressure(sample, p, ResourcePressureState{OptionalNotBefore: &at}, 200, 512<<20)
		if a.Admitted || a.NextEligibleAt == nil || state.BlockedUntil == nil || a.NextEligibleAt.Sub(at) > 30*time.Second {
			t.Fatal("age overrode safety or retry was unbounded")
		}
	}
}

func TestResourcePressureParsersAndPolicyBounds(t *testing.T) {
	if v := parsePSIAvg10("some avg10=34.50 avg60=0 total=1\nfull avg10=1.0\n", "some"); v == nil || *v != 34.5 {
		t.Fatal("invalid PSI parsing")
	}
	for _, raw := range []string{"some avg10=NaN", "some avg10=101", "full avg10=0", "garbage"} {
		if parsePSIAvg10(raw, "some") != nil {
			t.Fatal("invalid/missing metric became valid")
		}
	}
	if v := parseAvailableMemory("MemAvailable: 123 kB\n"); v == nil || *v != 123*1024 {
		t.Fatal("invalid available memory")
	}
	if parseAvailableMemory("MemAvailable: -1 kB") != nil {
		t.Fatal("invalid memory became available")
	}
	p, _, err := parseResourcePressurePolicy(json.RawMessage(`{"schema_version":"existing","pressure_policy":{"cpu_high":55}}`))
	if err != nil || p.CPUHigh != 55 || p.ReconsiderSeconds != 30 {
		t.Fatalf("default/mapped policy %+v %v", p, err)
	}
	if _, _, err := parseResourcePressurePolicy(json.RawMessage(`{"pressure_policy":{"reconsider_seconds":31}}`)); err == nil {
		t.Fatal("accepted indefinite reconsideration")
	}
}
