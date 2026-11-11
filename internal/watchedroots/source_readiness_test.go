package watchedroots

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSourceRecoveryAdmissionPreservesAbsenceHoldWithoutBlockingProtection(t *testing.T) {
	summary := `{"summary":{"root_reachable":true},"scan_counts":{"errors":0,"budget_exhausted":0,"missing_deferred":6898,"hash_deferred":0}}`
	metadata := []byte(`{"root_reachable":true}`)
	got := SourceRecoveryAdmissionState(StatusHealthy, []byte(summary), metadata)
	if !got.Allowed || got.MissingDeferred != 6898 || got.Stage != "current_filesystem_snapshot" {
		t.Fatalf("recovery qualification lost: %+v", got)
	}
	if SourceAdmissionState(StatusHealthy, []byte(summary), metadata).Allowed {
		t.Fatal("backup preparation approved collection deletion/delivery")
	}
	for _, change := range [][2]string{
		{`"errors":0`, `"errors":1`}, {`"budget_exhausted":0`, `"budget_exhausted":1`},
		{`"hash_deferred":0`, `"hash_deferred":1`}, {`"missing_deferred":6898`, `"missing_deferred":-1`},
		{`"root_reachable":true`, `"root_reachable":false`}, {`"errors":0,`, ``},
		{`"hash_deferred":0`, `"ignored":0`},
	} {
		if got := SourceRecoveryAdmissionState(StatusHealthy, []byte(strings.Replace(summary, change[0], change[1], 1)), metadata); got.Allowed {
			t.Fatalf("incomplete evidence admitted: %v %+v", change, got)
		}
	}
	if SourceRecoveryAdmissionState(StatusHealthy, []byte(summary), []byte(`{}`)).Allowed {
		t.Fatal("unknown reachability admitted")
	}
}

func TestSourceAdmissionSeparatesCoverageFromAuthority(t *testing.T) {
	summary := `{"summary":{"root_reachable":true},"scan_counts":{"errors":0,"budget_exhausted":1,"missing_deferred":0,"hash_deferred":12}}`
	metadata := []byte(`{"root_reachable":true}`)
	got := SourceAdmissionState(StatusDegraded, []byte(summary), metadata)
	if !got.Allowed || got.Stage != "scanning" {
		t.Fatalf("partial scan: %+v", got)
	}
	for _, tc := range []struct{ before, after, stage, reason string }{
		{`"budget_exhausted":1`, `"budget_exhausted":0`, "hashing", ""},
		{`"errors":0`, `"errors":1`, "blocked", "source_scan_failed"},
		{`"missing_deferred":0`, `"missing_deferred":1`, "blocked", "source_missing_evidence"},
		{`"root_reachable":true`, `"root_reachable":false`, "blocked", "source_unreachable"},
	} {
		got := SourceAdmissionState(StatusDegraded, []byte(strings.Replace(summary, tc.before, tc.after, 1)), metadata)
		if got.Stage != tc.stage || got.Reason != tc.reason || got.Allowed != (tc.reason == "") {
			t.Fatalf("%s: %+v", tc.before, got)
		}
	}
	if got := SourceAdmissionState(StatusBlocked, []byte(summary), metadata); got.Allowed {
		t.Fatal("blocked owner admitted")
	}
	if got := SourceAdmissionState(StatusDegraded, []byte(`{}`), metadata); got.Allowed {
		t.Fatal("unknown coverage evidence admitted")
	}
}

func TestSourceAdmissionFingerprintOnlyWakesMeaningfulChanges(t *testing.T) {
	root := WatchedRoot{Status: StatusDegraded, ConfigHash: "current", SafeRootKey: "source", Metadata: json.RawMessage(`{"root_reachable":true}`),
		SummaryJSON: json.RawMessage(`{"summary":{"root_reachable":true,"generated_at":"old"},"scan_counts":{"errors":0,"budget_exhausted":1,"missing_deferred":0,"hash_deferred":12}}`)}
	before := sourceAdmissionFingerprint(root)
	root.SummaryJSON = json.RawMessage(strings.Replace(strings.Replace(string(root.SummaryJSON), "old", "new", 1), ":12", ":11", 1))
	if sourceAdmissionFingerprint(root) != before {
		t.Fatal("unchanged posture woke on time/count noise")
	}
	root.SummaryJSON = json.RawMessage(strings.Replace(string(root.SummaryJSON), `"budget_exhausted":1`, `"budget_exhausted":0`, 1))
	if sourceAdmissionFingerprint(root) == before {
		t.Fatal("scan continuation completion did not wake admission")
	}
	before = sourceAdmissionFingerprint(root)
	root.ConfigHash = "new owner config"
	if sourceAdmissionFingerprint(root) == before {
		t.Fatal("owner config change lost")
	}
}

func TestExactSourceEntryDoesNotDependOnUnrelatedAbsenceChecks(t *testing.T) {
	summary := `{"summary":{"root_reachable":true},"scan_counts":{"errors":0,"budget_exhausted":0,"missing_deferred":2}}`
	metadata := []byte(`{"root_reachable":true}`)
	for _, status := range []string{StatusHealthy, StatusDegraded} {
		if got := SourceEntryAdmissionState(status, []byte(summary), metadata); !got.Allowed {
			t.Fatalf("exact path blocked by unrelated missing observations: %+v", got)
		}
		if got := SourceAdmissionState(status, []byte(summary), metadata); got.Allowed || got.Reason != "source_missing_evidence" {
			t.Fatalf("collection absence gate weakened: %+v", got)
		}
	}
	for _, tc := range []struct{ status, summary, metadata string }{
		{StatusBlocked, summary, string(metadata)},
		{StatusHealthy, strings.Replace(summary, `"errors":0`, `"errors":1`, 1), string(metadata)},
		{StatusHealthy, strings.Replace(summary, `"errors":0,`, ``, 1), string(metadata)},
		{StatusHealthy, strings.Replace(summary, `"root_reachable":true`, `"root_reachable":false`, 1), string(metadata)},
		{StatusHealthy, summary, `{}`},
		{StatusHealthy, summary, `{"root_reachable":false}`},
		{StatusDegraded, `{}`, string(metadata)},
		{StatusHealthy, `broken`, string(metadata)},
	} {
		if got := SourceEntryAdmissionState(tc.status, []byte(tc.summary), []byte(tc.metadata)); got.Allowed {
			t.Fatalf("unknown or denied owner admitted: %+v %+v", tc, got)
		}
	}
}
