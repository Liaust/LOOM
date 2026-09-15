package minidashboard

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

func Project(now time.Time, host HostSnapshot, domain *DomainSnapshot, cfg Config) ViewSnapshot {
	if now.IsZero() {
		now = time.Now().UTC()
	} else {
		now = now.UTC()
	}
	view := ViewSnapshot{
		SchemaVersion: SchemaVersion,
		GeneratedAt:   now,
		Header: HeaderView{
			Product:   "LOOM",
			NodeLabel: SafeLabel(cfg.NodeLabel, "MAIN", MaxNodeLabelLength),
			Overall:   SeverityUnknown,
		},
		Freshness: map[string]Freshness{},
	}
	conditions := make([]Condition, 0, 12)
	hostFreshness := effectiveFreshness(now, Freshness{
		SourceUpdatedAt:   host.GeneratedAt,
		StaleAfterSeconds: int64(cfg.HostStaleAfter / time.Second),
		SourceState:       SourceLive,
	}, cfg.HostStaleAfter)
	view.Freshness["host"] = hostFreshness
	conditions = appendFreshnessCondition(conditions, hostFreshness, "freshness.host", "Host data")
	view.System, conditions = projectSystem(now, host, hostFreshness, cfg, conditions)

	domainAvailable := domain != nil && domain.SchemaVersion == SchemaVersion
	active := false
	if !domainAvailable {
		view.Header.FreshnessState = SourceUnavailable
		view.Runtime = unavailableRows([4]string{"loomd", "database", "workers", "queue"}, [4]string{"loomd", "Database", "Workers", "Queue"})
		view.Protection = unavailableRows([4]string{"local", "cloud", "coverage", "findings"}, [4]string{"Local", "Cloud", "Coverage", "Findings"})
		view.Network = SummaryView{State: "UNKNOWN", Detail: "Network unavailable", Severity: SeverityUnknown}
		view.Activity = SummaryView{State: "IDLE", Detail: "No activity snapshot", Severity: SeverityUnknown}
	} else {
		domainFreshness := effectiveFreshness(now, domain.Freshness, cfg.DomainStaleAfter)
		view.Freshness["domain"] = domainFreshness
		runtimeFreshness := effectiveFreshness(now, domain.Runtime.Freshness, cfg.DomainStaleAfter)
		networkFreshness := effectiveFreshness(now, domain.Network.Freshness, cfg.DomainStaleAfter)
		activityFreshness := effectiveFreshness(now, domain.Activity.Freshness, cfg.DomainStaleAfter)
		localFreshness := effectiveFreshness(now, Freshness{
			SourceUpdatedAt: domain.LocalBackup.SourceUpdatedAt, StaleAfterSeconds: domain.LocalBackup.StaleAfterSeconds, SourceState: domain.LocalBackup.SourceState,
		}, cfg.DomainStaleAfter)
		cloudFreshness := effectiveFreshness(now, Freshness{
			SourceUpdatedAt: domain.CloudSnapshot.SourceUpdatedAt, StaleAfterSeconds: domain.CloudSnapshot.StaleAfterSeconds, SourceState: domain.CloudSnapshot.SourceState,
		}, cfg.DomainStaleAfter)
		if strings.EqualFold(domain.CloudSnapshot.State, "disabled") {
			cloudFreshness.SourceState = SourceDisabled
		}
		coverageFreshness := effectiveFreshness(now, Freshness{
			SourceUpdatedAt: domain.Coverage.CapturedAt, StaleAfterSeconds: domain.Coverage.StaleAfterSeconds, SourceState: domain.Coverage.SourceState,
		}, cfg.DomainStaleAfter)
		view.Freshness["runtime"] = runtimeFreshness
		view.Freshness["network"] = networkFreshness
		view.Freshness["activity"] = activityFreshness
		view.Freshness["local_backup"] = localFreshness
		view.Freshness["cloud_snapshot"] = cloudFreshness
		view.Freshness["coverage"] = coverageFreshness
		view.Header.SourceUpdatedAt = domainFreshness.SourceUpdatedAt
		view.Header.StaleAfterSec = domainFreshness.StaleAfterSeconds
		view.Header.FreshnessState = headerFreshnessState(domainFreshness.SourceState)
		if domainFreshness.SourceState == SourceOffline || domainFreshness.SourceState == SourceFailed {
			conditions = append(conditions, condition("runtime.loomd.offline", "loomd OFFLINE", SeverityCritical, AttentionRuntime))
		} else {
			conditions = appendFreshnessCondition(conditions, domainFreshness, "freshness.domain", "Domain data")
		}
		domainFreshnessDominates := degradedFreshnessState(domainFreshness.SourceState)
		if !domainFreshnessDominates {
			runtimeContractState := normalizeSourceState(domain.Runtime.State, SourceUnknown)
			if !domain.Runtime.Available && !degradedRuntimeState(runtimeContractState) {
				runtimeContractState = SourceUnavailable
			}
			if !degradedRuntimeState(runtimeContractState) {
				conditions = appendFreshnessCondition(conditions, runtimeFreshness, "freshness.runtime", "Runtime data")
			}
			conditions = appendFreshnessCondition(conditions, networkFreshness, "freshness.network", "Network data")
			conditions = appendFreshnessCondition(conditions, activityFreshness, "freshness.activity", "Activity data")
			conditions = appendFreshnessCondition(conditions, localFreshness, "freshness.local", "Local data")
			conditions = appendFreshnessCondition(conditions, cloudFreshness, "freshness.cloud", "Cloud data")
			conditions = appendFreshnessCondition(conditions, coverageFreshness, "freshness.coverage", "Coverage data")
		}
		runtimeState := projectedGroupState(domainFreshness, runtimeFreshness)
		networkState := projectedGroupState(domainFreshness, networkFreshness)
		activityState := projectedGroupState(domainFreshness, activityFreshness)
		localState := projectedGroupState(domainFreshness, localFreshness)
		cloudState := projectedGroupState(domainFreshness, cloudFreshness)
		coverageState := projectedGroupState(domainFreshness, coverageFreshness)
		cloudCacheFreshness := effectiveFreshness(now, Freshness{
			SourceUpdatedAt:   domain.Network.Cloud.CacheUpdatedAt,
			StaleAfterSeconds: int64(cfg.CloudCacheStaleAfter / time.Second),
			SourceState:       domain.Network.Cloud.SourceState,
		}, cfg.CloudCacheStaleAfter)
		if !domain.Network.Cloud.Enabled {
			cloudCacheFreshness.SourceState = SourceDisabled
		}
		view.Freshness["cloud_cache"] = cloudCacheFreshness
		cloudCacheState := cloudCacheFreshness.SourceState
		if !domainFreshnessDominates && !degradedFreshnessState(networkFreshness.SourceState) {
			conditions = appendFreshnessCondition(conditions, cloudCacheFreshness, "freshness.cloud_cache", "Cloud cache")
		}
		view.Runtime, conditions = projectRuntime(domain.Runtime, domainFreshness.SourceState, runtimeState, cfg, conditions)
		view.Protection, conditions = projectProtection(now, *domain, localState, cloudState, coverageState, projectedGroupState(domainFreshness, Freshness{SourceState: SourceLive}), conditions)
		view.Network, conditions = projectNetwork(now, domain.Network, networkState, cloudCacheState, cfg, conditions)
		view.Activity, active, conditions = projectActivity(now, domain.Activity, activityFreshness, activityState, conditions)
	}

	conditions = uniqueConditions(conditions)
	view.Header.Overall = OverallSeverity(conditions, active, domainAvailable)
	if selected, additional, ok := SelectAttention(conditions); ok {
		view.Attention = AttentionView{Message: selected.Label, Severity: selected.Severity, AdditionalCount: additional}
	} else {
		view.Attention = AttentionView{Message: "No attention required", Severity: SeverityHealthy}
	}
	return view
}

func projectSystem(now time.Time, host HostSnapshot, hostFreshness Freshness, cfg Config, conditions []Condition) ([4]RowView, []Condition) {
	metrics := []struct {
		key      string
		label    string
		metric   Metric
		unit     string
		priority int
		required bool
	}{
		{"cpu", "CPU", host.CPU, "%", AttentionFreshness, true},
		{"temperature", "Temp", host.Temperature, " C", AttentionResource, cfg.TemperatureRequired},
		{"memory", "Memory", host.Memory, "%", AttentionFreshness, true},
		{"storage", "Storage", host.Storage, "%", AttentionResource, true},
	}
	var rows [4]RowView
	hostDominates := degradedFreshnessState(hostFreshness.SourceState)
	for i, item := range metrics {
		row := RowView{Key: item.key, Label: item.label, Value: "Unavailable", Severity: SeverityUnknown, State: SourceUnavailable}
		metric := item.metric
		if metric.Available && metric.Value != nil && !math.IsNaN(*metric.Value) && !math.IsInf(*metric.Value, 0) {
			staleAfter := time.Duration(metric.StaleAfterSeconds) * time.Second
			if staleAfter <= 0 {
				staleAfter = cfg.HostStaleAfter
			}
			metricFreshness := effectiveFreshness(now, Freshness{
				SourceUpdatedAt: metric.SampledAt, StaleAfterSeconds: int64(staleAfter / time.Second), SourceState: metric.SourceState,
			}, staleAfter)
			state := metricFreshness.SourceState
			if hostDominates && !degradedFreshnessState(state) {
				state = projectedGroupState(hostFreshness, metricFreshness)
			}
			row.Value = fmt.Sprintf("%.0f%s", *metric.Value, item.unit)
			row.State = state
			severityKnown := validMetricSeverity(metric.Severity)
			if severityKnown {
				row.Severity = metric.Severity
			} else {
				row.Severity = SeverityWarning
				conditions = append(conditions, condition("system."+item.key+".severity_unknown", item.label+" severity UNKNOWN", SeverityWarning, AttentionFreshness))
			}
			thresholdSeverity := row.Severity
			if !hostDominates {
				conditions = appendFreshnessCondition(conditions, Freshness{SourceState: state}, "system."+item.key+".sensor", item.label+" sensor")
			}
			applyRowFreshness(&row, state)
			if severityKnown && (thresholdSeverity == SeverityWarning || thresholdSeverity == SeverityCritical) {
				conditions = append(conditions, condition("system."+item.key+"."+string(thresholdSeverity), strings.ToUpper(string(thresholdSeverity))+" "+item.label, thresholdSeverity, item.priority))
			}
		} else if item.required {
			conditions = append(conditions, condition("system."+item.key+".unavailable", item.label+" Unavailable", SeverityWarning, AttentionFreshness))
		}
		rows[i] = row
	}
	return rows, conditions
}

func projectRuntime(runtime RuntimeState, domainState, freshnessState SourceState, cfg Config, conditions []Condition) ([4]RowView, []Condition) {
	rows := unavailableRows([4]string{"loomd", "database", "workers", "queue"}, [4]string{"loomd", "Database", "Workers", "Queue"})
	runtimeState := normalizeSourceState(runtime.State, SourceUnknown)
	if !runtime.Available && !degradedRuntimeState(runtimeState) {
		runtimeState = SourceUnavailable
	}
	domainState = normalizeSourceState(domainState, SourceUnknown)
	loomdRow := runtimeRow(runtimeState)
	if !degradedFreshnessState(runtimeState) && degradedFreshnessState(freshnessState) {
		loomdRow.State = normalizeSourceState(freshnessState, SourceUnknown)
		applyRowFreshness(&loomdRow, loomdRow.State)
	}
	if domainState == SourceOffline || domainState == SourceFailed {
		loomdRow = RowView{Key: "loomd", Label: "loomd", Value: "OFFLINE", Severity: SeverityCritical, State: SourceOffline}
	}
	if degradedRuntimeState(runtimeState) {
		domainCritical := domainState == SourceOffline || domainState == SourceFailed
		runtimeCritical := runtimeState == SourceOffline || runtimeState == SourceFailed
		if !degradedFreshnessState(domainState) || (runtimeCritical && !domainCritical) {
			severity := SeverityWarning
			if runtimeCritical {
				severity = SeverityCritical
			}
			state := strings.ToUpper(string(runtimeState))
			conditions = append(conditions, condition("runtime.loomd."+string(runtimeState), "loomd "+state, severity, AttentionRuntime))
		}
	}
	if !runtime.Available {
		applyRowsFreshness(&rows, freshnessState)
		rows[0] = loomdRow
		return rows, conditions
	}
	dbState := strings.ToLower(strings.TrimSpace(runtime.DatabaseState))
	if dbState == "ok" || dbState == "healthy" {
		rows[1] = RowView{Key: "database", Label: "Database", Value: "OK", Severity: SeverityHealthy, State: SourceLive}
	} else {
		rows[1] = RowView{Key: "database", Label: "Database", Value: "FAILED", Severity: SeverityCritical, State: SourceFailed}
		conditions = append(conditions, condition("runtime.database.failed", "Database FAILED", SeverityCritical, AttentionRuntime))
	}
	if !runtime.MigrationsCurrent {
		rows[1] = RowView{Key: "database", Label: "Database", Value: "MIGRATIONS", Severity: SeverityCritical, State: SourceFailed}
		conditions = append(conditions, condition("runtime.migrations.behind", "Database migrations FAILED", SeverityCritical, AttentionRuntime))
	}
	rows[2] = RowView{Key: "workers", Label: "Workers", Value: fmt.Sprintf("%d/%d", runtime.HealthyWorkers, runtime.EnabledWorkers), Severity: SeverityHealthy, State: SourceLive}
	if runtime.CriticalWorkers > 0 {
		rows[2].Severity, rows[2].State = SeverityCritical, SourceFailed
		conditions = append(conditions, condition("runtime.workers.critical", "CRITICAL worker failure", SeverityCritical, AttentionRuntime))
	} else if runtime.DegradedWorkers > 0 || runtime.OfflineRunners > 0 {
		rows[2].Severity = SeverityWarning
		conditions = append(conditions, condition("runtime.workers.degraded", "WARNING worker degraded", SeverityWarning, AttentionRuntime))
	}
	rows[3] = RowView{Key: "queue", Label: "Queue", Value: fmt.Sprintf("%d", runtime.Queued), Severity: SeverityHealthy, State: SourceLive}
	if runtime.DeadLetter > 0 {
		rows[3].Value, rows[3].Severity, rows[3].State = fmt.Sprintf("%d DEAD", runtime.DeadLetter), SeverityCritical, SourceFailed
		conditions = append(conditions, condition("runtime.queue.dead_letter", "CRITICAL dead-letter work", SeverityCritical, AttentionWork))
	} else if runtime.Failed > 0 || runtime.ManualAction > 0 {
		rows[3].Value, rows[3].Severity = fmt.Sprintf("%d FAILED", runtime.Failed+runtime.ManualAction), SeverityWarning
		conditions = append(conditions, condition("runtime.queue.failed", "WARNING failed work", SeverityWarning, AttentionWork))
	} else if runtime.OldestQueuedAgeSec != nil && time.Duration(*runtime.OldestQueuedAgeSec)*time.Second > cfg.QueueWarningAfter {
		rows[3].Severity = SeverityWarning
		conditions = append(conditions, condition("runtime.queue.stale", "WARNING queued work delayed", SeverityWarning, AttentionWork))
	}
	applyRowsFreshness(&rows, freshnessState)
	rows[0] = loomdRow
	return rows, conditions
}

func runtimeRow(state SourceState) RowView {
	row := RowView{Key: "loomd", Label: "loomd", Value: "OK", Severity: SeverityHealthy, State: state}
	if !degradedRuntimeState(state) {
		return row
	}
	row.Value = strings.ToUpper(string(normalizeSourceState(state, SourceUnknown)))
	row.Severity = SeverityWarning
	if state == SourceFailed || state == SourceOffline {
		row.Severity = SeverityCritical
	}
	return row
}

func degradedRuntimeState(state SourceState) bool {
	state = normalizeSourceState(state, SourceUnknown)
	return state != SourceLive && state != SourceCached
}

func projectProtection(now time.Time, domain DomainSnapshot, localFreshness, cloudFreshness, coverageFreshness, findingsFreshness SourceState, conditions []Condition) ([4]RowView, []Condition) {
	var rows [4]RowView
	rows[0], conditions = protectionRow(now, "local", "Local", domain.LocalBackup, false, localFreshness, conditions)
	rows[1], conditions = protectionRow(now, "cloud", "Cloud", domain.CloudSnapshot, true, cloudFreshness, conditions)
	rows[2] = RowView{Key: "coverage", Label: "Coverage", Value: "Unavailable", Severity: SeverityUnknown, State: SourceUnavailable}
	if domain.Coverage.Available {
		value := coverageLabel(domain.Coverage.State)
		rows[2] = RowView{Key: "coverage", Label: "Coverage", Value: value, Severity: SeverityHealthy, State: coverageFreshness}
		if value != "Complete" {
			rows[2].Severity = SeverityWarning
			conditions = append(conditions, condition("protection.coverage.incomplete", "WARNING backup coverage", SeverityWarning, AttentionProtection))
		}
		applyRowFreshness(&rows[2], coverageFreshness)
	}
	rows[3] = RowView{Key: "findings", Label: "Findings", Value: "Unavailable", Severity: SeverityUnknown, State: SourceUnavailable}
	if domain.Findings.Available {
		rows[3] = RowView{Key: "findings", Label: "Findings", Value: fmt.Sprintf("%d", domain.Findings.Open), Severity: SeverityHealthy, State: SourceLive}
		if domain.Findings.Critical+domain.Findings.Error > 0 {
			rows[3].Severity = SeverityCritical
			conditions = append(conditions, condition("protection.findings.critical", "CRITICAL maintenance findings", SeverityCritical, AttentionProtection))
		} else if domain.Findings.Warning > 0 {
			rows[3].Severity = SeverityWarning
			conditions = append(conditions, condition("protection.findings.warning", "WARNING maintenance findings", SeverityWarning, AttentionProtection))
		}
		applyRowFreshness(&rows[3], findingsFreshness)
	}
	return rows, conditions
}

func protectionRow(now time.Time, key, label string, state ProtectionState, disabledAllowed bool, freshnessState SourceState, conditions []Condition) (RowView, []Condition) {
	row := RowView{Key: key, Label: label, Value: "Unavailable", Severity: SeverityUnknown, State: SourceUnavailable}
	if !state.Available {
		return row, conditions
	}
	if disabledAllowed && strings.EqualFold(state.State, "disabled") {
		return RowView{Key: key, Label: label, Value: "DISABLED", Severity: SeverityHealthy, State: SourceDisabled}, conditions
	}
	row.State = freshnessState
	if state.LastSuccessAt != nil {
		row.Value = ageLabel(now.Sub(state.LastSuccessAt.UTC())) + " ago"
		row.Severity = SeverityHealthy
	} else {
		row.Value = "Never"
		row.Severity = SeverityWarning
		if state.InitializationGraceEnd != nil && now.After(state.InitializationGraceEnd.UTC()) {
			row.Severity = SeverityCritical
		}
		conditions = append(conditions, condition("protection."+key+".never", strings.ToUpper(string(row.Severity))+" "+label+" backup never run", row.Severity, AttentionProtection))
	}
	if strings.EqualFold(state.State, "failed") || strings.EqualFold(state.VerificationState, "failed") {
		row.Value, row.Severity, row.State = "FAILED", SeverityCritical, SourceFailed
		conditions = append(conditions, condition("protection."+key+".failed", label+" protection FAILED", SeverityCritical, AttentionProtection))
	} else if state.LastSuccessAt != nil && state.IntervalSeconds > 0 {
		age := now.Sub(state.LastSuccessAt.UTC())
		interval := time.Duration(state.IntervalSeconds) * time.Second
		if age > 3*interval {
			row.Severity = SeverityCritical
			conditions = append(conditions, condition("protection."+key+".critical_age", "CRITICAL "+label+" protection stale", SeverityCritical, AttentionProtection))
		} else if age > 2*interval {
			row.Severity = SeverityWarning
			conditions = append(conditions, condition("protection."+key+".warning_age", "WARNING "+label+" protection stale", SeverityWarning, AttentionProtection))
		}
	}
	applyRowFreshness(&row, freshnessState)
	return row, conditions
}

func projectNetwork(now time.Time, network NetworkState, freshnessState, cloudCacheState SourceState, cfg Config, conditions []Condition) (SummaryView, []Condition) {
	if !network.Available {
		state := "UNKNOWN"
		if degradedFreshnessState(freshnessState) {
			state = "NETWORK " + strings.ToUpper(string(freshnessState))
		}
		return SummaryView{State: state, Detail: "Network unavailable", Severity: SeverityUnknown}, conditions
	}
	online := 0
	presence := map[string]bool{}
	childWarning := false
	for _, node := range network.Nodes {
		if node.Online {
			online++
		}
		presence[node.Key] = node.Online
	}
	for _, key := range cfg.ExpectedOnlineNodes {
		if !presence[key] {
			childWarning = true
			conditions = append(conditions, condition("network.node."+key+".offline", "WARNING expected node OFFLINE", SeverityWarning, AttentionNode))
		}
	}
	cloud := "unknown"
	if !network.Cloud.Enabled {
		cloud = "disabled"
	} else if degradedFreshnessState(cloudCacheState) {
		marker := "CACHE " + strings.ToUpper(string(normalizeSourceState(cloudCacheState, SourceUnknown)))
		if network.Cloud.Cached && !network.Cloud.CacheUpdatedAt.IsZero() {
			cloud = cloudLabel(network.Cloud.State)
			cloud = "last-good " + cloud + " (cached " + ageLabel(now.Sub(network.Cloud.CacheUpdatedAt.UTC())) + "; " + marker + ")"
		} else {
			cloud = "unavailable (" + marker + ")"
		}
		childWarning = true
	} else if network.Cloud.Available {
		cloud = cloudLabel(network.Cloud.State)
		if network.Cloud.Cached {
			cloud += " (cached " + ageLabel(now.Sub(network.Cloud.CacheUpdatedAt.UTC())) + ")"
		}
		if strings.EqualFold(network.Cloud.State, "unreachable") || strings.EqualFold(network.Cloud.State, "degraded") {
			childWarning = true
			conditions = append(conditions, condition("network.cloud.unreachable", "WARNING cloud unreachable", SeverityWarning, AttentionProtection))
		}
	}
	severity := SeverityHealthy
	if network.Communication.Available && network.Communication.DeadLetter > 0 {
		severity = SeverityCritical
		conditions = append(conditions, condition("network.messages.dead_letter", "CRITICAL dead-letter messages", SeverityCritical, AttentionWork))
	} else if network.Communication.Available && network.Communication.Failed > 0 {
		severity = SeverityWarning
		conditions = append(conditions, condition("network.messages.failed", "WARNING failed messages", SeverityWarning, AttentionWork))
	}
	if childWarning && severity != SeverityCritical {
		severity = SeverityWarning
	}
	state := "NETWORK"
	if degradedFreshnessState(freshnessState) {
		state = "NETWORK " + strings.ToUpper(string(freshnessState))
		if severity == SeverityHealthy {
			severity = SeverityWarning
		}
	}
	return SummaryView{
		State:    state,
		Detail:   fmt.Sprintf("%d online / %d known - cloud %s", online, len(network.Nodes), cloud),
		Severity: severity,
	}, conditions
}

func projectActivity(now time.Time, activity ActivityState, freshness Freshness, freshnessState SourceState, conditions []Condition) (SummaryView, bool, []Condition) {
	degraded := degradedFreshnessState(freshnessState)
	if activity.Running != nil {
		state, severity := normalizedActivityState(activity.Running.State)
		view := SummaryView{State: "ACTIVE", Detail: activityLabel(activity.Running.Kind) + " " + state, Severity: severity}
		active := state == "in progress"
		if !active {
			view.State = "IDLE"
			conditions = appendActivityCondition(conditions, state, degraded)
		}
		if degraded {
			view.Detail = freshnessDetail(view.Detail, freshnessState)
			if view.Severity != SeverityCritical {
				view.Severity = SeverityWarning
			}
		}
		return view, active && !degraded, conditions
	}
	if activity.Recent != nil {
		when := activity.Recent.CompletedAt
		if when == nil {
			when = activity.Recent.StartedAt
		}
		age := "recently"
		if when != nil {
			age = ageLabel(now.Sub(when.UTC())) + " ago"
		}
		state, severity := normalizedActivityState(activity.Recent.State)
		view := SummaryView{State: "IDLE", Detail: "last activity: " + activityLabel(activity.Recent.Kind) + " " + state + " " + age, Severity: severity}
		conditions = appendActivityCondition(conditions, state, degraded)
		if degraded {
			view.Detail = freshnessDetail(view.Detail, freshnessState)
			if view.Severity != SeverityCritical {
				view.Severity = SeverityWarning
			}
		}
		return view, false, conditions
	}
	view := SummaryView{State: "IDLE", Detail: "No recent activity", Severity: SeverityHealthy}
	if degraded {
		if freshness.SourceUpdatedAt.IsZero() {
			return SummaryView{State: "IDLE", Detail: "No activity snapshot", Severity: SeverityUnknown}, false, conditions
		}
		view.Detail, view.Severity = freshnessDetail(view.Detail, freshnessState), SeverityWarning
	}
	return view, false, conditions
}

func normalizedActivityState(value string) (string, Severity) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "running", "in_progress", "in progress", "active":
		return "in progress", SeverityActive
	case "completed", "complete", "succeeded", "success":
		return "completed", SeverityHealthy
	case "failed", "error":
		return "failed", SeverityCritical
	default:
		return "state unknown", SeverityWarning
	}
}

func appendActivityCondition(conditions []Condition, state string, freshnessDominates bool) []Condition {
	switch state {
	case "failed":
		return append(conditions, condition("activity.failed", "Activity FAILED", SeverityCritical, AttentionWork))
	case "state unknown":
		if !freshnessDominates {
			return append(conditions, condition("activity.state.unknown", "Activity state UNKNOWN", SeverityWarning, AttentionFreshness))
		}
	}
	return conditions
}

func appendFreshnessCondition(conditions []Condition, freshness Freshness, code, label string) []Condition {
	stateValue := normalizeSourceState(freshness.SourceState, SourceUnknown)
	if degradedFreshnessState(stateValue) {
		state := strings.ToLower(string(stateValue))
		return append(conditions, condition(code+"."+state, label+" "+strings.ToUpper(state), SeverityWarning, AttentionFreshness))
	}
	return conditions
}

func applyRowsFreshness(rows *[4]RowView, state SourceState) {
	for i := range rows {
		applyRowFreshness(&rows[i], state)
	}
}

func applyRowFreshness(row *RowView, state SourceState) {
	state = normalizeSourceState(state, SourceUnknown)
	if row.Value == "Unavailable" || !degradedFreshnessState(state) {
		return
	}
	row.State = state
	if row.Severity != SeverityCritical {
		row.Severity = SeverityWarning
	}
}

func freshnessDetail(detail string, state SourceState) string {
	state = normalizeSourceState(state, SourceUnknown)
	suffix := " (" + strings.ToLower(string(state)) + ")"
	if strings.HasSuffix(detail, suffix) {
		return detail
	}
	return detail + suffix
}

func degradedFreshnessState(state SourceState) bool {
	state = normalizeSourceState(state, SourceUnknown)
	switch state {
	case SourceStale, SourceOffline, SourceFailed, SourceUnavailable, SourceUnknown:
		return true
	default:
		return false
	}
}

func projectedGroupState(domain, group Freshness) SourceState {
	domain.SourceState = normalizeSourceState(domain.SourceState, SourceUnknown)
	group.SourceState = normalizeSourceState(group.SourceState, SourceUnknown)
	if group.SourceState == SourceDisabled {
		return SourceDisabled
	}
	if !degradedFreshnessState(domain.SourceState) {
		return group.SourceState
	}
	switch domain.SourceState {
	case SourceOffline, SourceFailed, SourceStale:
		return SourceStale
	default:
		return domain.SourceState
	}
}

func uniqueConditions(conditions []Condition) []Condition {
	seen := make(map[string]bool, len(conditions))
	unique := conditions[:0]
	for _, item := range conditions {
		if seen[item.Code] {
			continue
		}
		seen[item.Code] = true
		unique = append(unique, item)
	}
	return unique
}

func effectiveFreshness(now time.Time, freshness Freshness, fallback time.Duration) Freshness {
	if freshness.StaleAfterSeconds <= 0 {
		freshness.StaleAfterSeconds = int64(fallback / time.Second)
	}
	freshness.SourceState = normalizeSourceState(freshness.SourceState, SourceLive)
	if freshness.SourceState == SourceDisabled {
		return freshness
	}
	if freshness.SourceUpdatedAt.IsZero() {
		if freshness.SourceState == SourceLive || freshness.SourceState == SourceCached {
			freshness.SourceState = SourceUnavailable
		}
		return freshness
	}
	if (freshness.SourceState == SourceLive || freshness.SourceState == SourceCached) && now.Sub(freshness.SourceUpdatedAt.UTC()) > time.Duration(freshness.StaleAfterSeconds)*time.Second {
		freshness.SourceState = SourceStale
	}
	return freshness
}

func headerFreshnessState(state SourceState) SourceState {
	state = normalizeSourceState(state, SourceUnknown)
	switch state {
	case SourceOffline, SourceFailed:
		return SourceOffline
	case SourceStale:
		return SourceStale
	case SourceUnavailable, SourceUnknown, SourceDisabled:
		return state
	default:
		return SourceLive
	}
}

func unavailableRows(keys, labels [4]string) [4]RowView {
	var rows [4]RowView
	for i := range rows {
		rows[i] = RowView{Key: keys[i], Label: labels[i], Value: "Unavailable", Severity: SeverityUnknown, State: SourceUnavailable}
	}
	return rows
}

func condition(code, label string, severity Severity, priority int) Condition {
	return Condition{Code: code, Label: label, Severity: severity, Priority: priority, Actionable: true}
}

func coverageLabel(state string) string {
	switch strings.ToLower(strings.TrimSpace(state)) {
	case "complete", "ok", "healthy":
		return "Complete"
	case "partial", "incomplete":
		return "Incomplete"
	case "failed":
		return "FAILED"
	default:
		return "Unknown"
	}
}

func cloudLabel(state string) string {
	switch strings.ToLower(strings.TrimSpace(state)) {
	case "healthy", "reachable":
		return "reachable"
	case "cooling_down", "cooling down":
		return "cooling down"
	case "unreachable", "degraded":
		return "unreachable"
	case "disabled":
		return "disabled"
	default:
		return "unknown"
	}
}

func activityLabel(kind string) string {
	labels := map[string]string{
		"main_backup": "backup", "cloud_snapshot": "cloud snapshot", "transfer": "transfer",
		"lane": "Lane transfer", "dropzone": "Dropzone transfer", "restore": "restore",
		"storage_import": "Documents import", "job": "job", "maintenance": "maintenance",
		"index": "index", "automation": "automation",
	}
	if label := labels[strings.ToLower(strings.TrimSpace(kind))]; label != "" {
		return label
	}
	return "LOOM operation"
}

func ageLabel(age time.Duration) string {
	if age < 0 {
		age = 0
	}
	if age < time.Minute {
		return fmt.Sprintf("%ds", int(age.Seconds()))
	}
	if age < time.Hour {
		return fmt.Sprintf("%dm", int(age.Minutes()))
	}
	if age < 48*time.Hour {
		return fmt.Sprintf("%dh", int(age.Hours()))
	}
	return fmt.Sprintf("%dd", int(age.Hours()/24))
}

func sortedConditionCodes(conditions []Condition) []string {
	codes := make([]string, 0, len(conditions))
	for _, item := range conditions {
		codes = append(codes, item.Code)
	}
	sort.Strings(codes)
	return codes
}
