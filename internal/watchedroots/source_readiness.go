package watchedroots

import "encoding/json"

// SourceAdmission describes owner evidence, not indexing or client delivery.
type SourceAdmission struct {
	Allowed         bool
	Stage           string
	Reason          string
	MissingDeferred int
}

// SourceRecoveryAdmissionState protects the filesystem as it exists now. It
// cannot approve old absences, authorize writes or admit collection delivery.
func SourceRecoveryAdmissionState(status string, summary, metadata []byte) SourceAdmission {
	admission := SourceEntryAdmissionState(status, summary, metadata)
	if !admission.Allowed {
		return admission
	}
	var report struct {
		Summary struct {
			RootReachable *bool `json:"root_reachable"`
		} `json:"summary"`
		Counts struct {
			Errors          *int `json:"errors"`
			BudgetExhausted *int `json:"budget_exhausted"`
			MissingDeferred *int `json:"missing_deferred"`
			HashDeferred    *int `json:"hash_deferred"`
		} `json:"scan_counts"`
	}
	var owner struct {
		RootReachable *bool `json:"root_reachable"`
	}
	_ = json.Unmarshal(summary, &report)
	_ = json.Unmarshal(metadata, &owner)
	c := report.Counts
	if owner.RootReachable == nil || !*owner.RootReachable || report.Summary.RootReachable == nil || !*report.Summary.RootReachable ||
		c.Errors == nil || *c.Errors != 0 || c.BudgetExhausted == nil || *c.BudgetExhausted != 0 ||
		c.HashDeferred == nil || *c.HashDeferred != 0 || c.MissingDeferred == nil || *c.MissingDeferred < 0 {
		return SourceAdmission{Stage: "blocked", Reason: "source_scan_evidence_unavailable"}
	}
	return SourceAdmission{Allowed: true, Stage: "current_filesystem_snapshot", MissingDeferred: *c.MissingDeferred}
}

func SourceAdmissionState(status string, summary, metadata []byte) SourceAdmission {
	return sourceAdmissionState(status, summary, metadata, false)
}

// SourceEntryAdmissionState is only for an exact path that the source owner
// subsequently opens and fences. Unrelated catalog absence checks cannot prove
// that path missing, and must not prevent reading or saving its actual bytes.
// Collection enumeration still uses SourceAdmissionState and its absence gate.
func SourceEntryAdmissionState(status string, summary, metadata []byte) SourceAdmission {
	return sourceAdmissionState(status, summary, metadata, true)
}

func sourceAdmissionState(status string, summary, metadata []byte, exactEntry bool) SourceAdmission {
	var report struct {
		Summary struct {
			RootReachable *bool `json:"root_reachable"`
		} `json:"summary"`
		Counts struct {
			Errors          *int `json:"errors"`
			BudgetExhausted *int `json:"budget_exhausted"`
			MissingDeferred *int `json:"missing_deferred"`
			HashDeferred    int  `json:"hash_deferred"`
		} `json:"scan_counts"`
		OutputPlan struct {
			Counts struct {
				Deferred int `json:"deferred"`
			} `json:"counts"`
		} `json:"output_plan"`
	}
	var owner struct {
		RootReachable *bool `json:"root_reachable"`
	}
	deny := func(reason string) SourceAdmission { return SourceAdmission{Stage: "blocked", Reason: reason} }
	if status != StatusHealthy && status != StatusDegraded {
		return deny("owner_report_unavailable")
	}
	if json.Unmarshal(summary, &report) != nil || json.Unmarshal(metadata, &owner) != nil {
		return deny("source_scan_evidence_unavailable")
	}
	if owner.RootReachable != nil && !*owner.RootReachable || report.Summary.RootReachable != nil && !*report.Summary.RootReachable {
		return deny("source_unreachable")
	}
	if report.Counts.Errors != nil && *report.Counts.Errors != 0 {
		return deny("source_scan_failed")
	}
	if report.Counts.MissingDeferred != nil && *report.Counts.MissingDeferred != 0 {
		if !exactEntry || owner.RootReachable == nil || !*owner.RootReachable ||
			report.Summary.RootReachable == nil || !*report.Summary.RootReachable ||
			report.Counts.Errors == nil || *report.Counts.Errors != 0 || *report.Counts.MissingDeferred < 0 {
			return deny("source_missing_evidence")
		}
	}
	if status == StatusDegraded && (owner.RootReachable == nil || report.Summary.RootReachable == nil ||
		report.Counts.Errors == nil || report.Counts.BudgetExhausted == nil || report.Counts.MissingDeferred == nil) {
		return deny("source_scan_evidence_unavailable")
	}
	stage := "admitted"
	switch {
	case report.Counts.BudgetExhausted != nil && *report.Counts.BudgetExhausted > 0:
		stage = "scanning"
	case report.Counts.HashDeferred > 0:
		stage = "hashing"
	case report.OutputPlan.Counts.Deferred > 0:
		stage = "publishing"
	}
	return SourceAdmission{Allowed: true, Stage: stage}
}
