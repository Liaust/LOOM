package minidashboard

import (
	"strings"
	"testing"
	"time"
)

func TestProjectRequiredFixtures(t *testing.T) {
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name       string
		mutateHost func(*HostSnapshot)
		mutate     func(*DomainSnapshot, *Config)
		domainNil  bool
		want       Severity
		attention  string
		additional int
	}{
		{name: "healthy idle", want: SeverityHealthy, attention: "No attention required"},
		{name: "healthy active", mutate: func(d *DomainSnapshot, _ *Config) {
			started := now.Add(-2 * time.Minute)
			d.Activity.Running = &Activity{Kind: "main_backup", State: "running", StartedAt: &started}
		}, want: SeverityActive, attention: "No attention required"},
		{name: "one warning", mutateHost: func(h *HostSnapshot) { h.Storage.Severity = SeverityWarning }, want: SeverityWarning, attention: "WARNING Storage"},
		{name: "several equal severity warnings", mutate: func(d *DomainSnapshot, _ *Config) {
			d.Runtime.DegradedWorkers = 1
			d.LocalBackup.State = "failed"
			d.LocalBackup.VerificationState = "ok"
		}, mutateHost: func(h *HostSnapshot) { h.Storage.Severity = SeverityWarning }, want: SeverityCritical, attention: "Local protection FAILED", additional: 2},
		{name: "critical overrides active", mutate: func(d *DomainSnapshot, _ *Config) {
			started := now.Add(-time.Minute)
			d.Activity.Running = &Activity{Kind: "job", State: "running", StartedAt: &started}
			d.Runtime.DatabaseState = "failed"
		}, want: SeverityCritical, attention: "Database FAILED"},
		{name: "stale last-good data", mutate: func(d *DomainSnapshot, _ *Config) { d.Freshness.SourceUpdatedAt = now.Add(-2 * time.Minute) }, want: SeverityWarning, attention: "Domain data STALE"},
		{name: "no previous data", domainNil: true, want: SeverityUnknown, attention: "No attention required"},
		{name: "absent temperature", mutateHost: func(h *HostSnapshot) { h.Temperature = Metric{Available: false, SourceState: SourceUnavailable} }, want: SeverityHealthy, attention: "No attention required"},
		{name: "intermittent offline node", mutate: func(d *DomainSnapshot, _ *Config) {
			d.Network.Nodes = append(d.Network.Nodes, NodePresence{Key: "macbook", Label: "MACBOOK", Online: false})
		}, want: SeverityHealthy, attention: "No attention required"},
		{name: "expected offline node", mutate: func(d *DomainSnapshot, c *Config) {
			d.Network.Nodes[0].Online = false
			c.ExpectedOnlineNodes = []string{"main"}
		}, want: SeverityWarning, attention: "WARNING expected node OFFLINE"},
		{name: "cached healthy cloud", mutate: func(d *DomainSnapshot, _ *Config) { d.Network.Cloud.Cached = true; d.Network.Cloud.State = "healthy" }, want: SeverityHealthy, attention: "No attention required"},
		{name: "stale cached cloud", mutate: func(d *DomainSnapshot, _ *Config) {
			d.Network.Cloud.Cached = true
			d.Network.Cloud.CacheUpdatedAt = now.Add(-13 * time.Hour)
		}, want: SeverityWarning, attention: "Cloud cache STALE"},
		{name: "explicit cloud failure", mutate: func(d *DomainSnapshot, _ *Config) { d.CloudSnapshot.State = "failed" }, want: SeverityCritical, attention: "Cloud protection FAILED"},
		{name: "local backup never run in grace", mutate: func(d *DomainSnapshot, _ *Config) {
			grace := now.Add(time.Hour)
			d.LocalBackup.LastSuccessAt = nil
			d.LocalBackup.InitializationGraceEnd = &grace
		}, want: SeverityWarning, attention: "WARNING Local backup never run"},
		{name: "local backup never run after grace", mutate: func(d *DomainSnapshot, _ *Config) {
			grace := now.Add(-time.Hour)
			d.LocalBackup.LastSuccessAt = nil
			d.LocalBackup.InitializationGraceEnd = &grace
		}, want: SeverityCritical, attention: "CRITICAL Local backup never run"},
		{name: "backup interval changed from defaults", mutate: func(d *DomainSnapshot, _ *Config) {
			success := now.Add(-150 * time.Minute)
			d.LocalBackup.LastSuccessAt = &success
			d.LocalBackup.IntervalSeconds = int64(time.Hour / time.Second)
		}, want: SeverityWarning, attention: "WARNING Local protection stale"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			host := healthyHostFixture(now)
			if tt.mutateHost != nil {
				tt.mutateHost(&host)
			}
			cfg := DefaultConfig()
			var domain *DomainSnapshot
			if !tt.domainNil {
				value := healthyDomainFixture(now)
				domain = &value
				if tt.mutate != nil {
					tt.mutate(domain, &cfg)
				}
			}
			view := Project(now, host, domain, cfg)
			if view.Header.Overall != tt.want {
				t.Fatalf("overall = %q, want %q; view=%#v", view.Header.Overall, tt.want, view)
			}
			if view.Attention.Message != tt.attention {
				t.Fatalf("attention = %q, want %q", view.Attention.Message, tt.attention)
			}
			if view.Attention.AdditionalCount != tt.additional {
				t.Fatalf("additional = %d, want %d", view.Attention.AdditionalCount, tt.additional)
			}
		})
	}
}

func TestProjectOfflinePreservesHostAndMarksDomain(t *testing.T) {
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	host := healthyHostFixture(now)
	domain := healthyDomainFixture(now.Add(-time.Minute))
	domain.Freshness.SourceState = SourceOffline
	view := Project(now, host, &domain, DefaultConfig())
	if view.Header.Overall != SeverityCritical || view.Header.FreshnessState != SourceOffline || view.Runtime[0].Value != "OFFLINE" {
		t.Fatalf("unexpected offline projection: %#v", view)
	}
	if view.System[0].Value != "18%" || view.System[3].Value != "61%" {
		t.Fatalf("host metrics were not preserved: %#v", view.System)
	}
}

func TestProjectAppliesEffectiveGroupFreshness(t *testing.T) {
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name      string
		group     string
		mutate    func(*DomainSnapshot)
		attention string
		assert    func(*testing.T, ViewSnapshot)
	}{
		{
			name: "runtime", group: "runtime", attention: "Runtime data STALE",
			mutate: func(d *DomainSnapshot) { d.Runtime.Freshness.SourceUpdatedAt = now.Add(-31 * time.Second) },
			assert: func(t *testing.T, view ViewSnapshot) {
				for _, row := range view.Runtime {
					if row.State != SourceStale || row.Value == "Unavailable" {
						t.Fatalf("runtime last-good row not preserved as stale: %#v", row)
					}
				}
			},
		},
		{
			name: "network", group: "network", attention: "Network data STALE",
			mutate: func(d *DomainSnapshot) { d.Network.Freshness.SourceUpdatedAt = now.Add(-31 * time.Second) },
			assert: func(t *testing.T, view ViewSnapshot) {
				if view.Network.State != "NETWORK STALE" || view.Network.Detail != "1 online / 1 known - cloud reachable (cached 0s)" {
					t.Fatalf("network last-good summary not preserved as stale: %#v", view.Network)
				}
			},
		},
		{
			name: "activity", group: "activity", attention: "Activity data STALE",
			mutate: func(d *DomainSnapshot) { d.Activity.Freshness.SourceUpdatedAt = now.Add(-31 * time.Second) },
			assert: func(t *testing.T, view ViewSnapshot) {
				if view.Activity.Detail != "last activity: backup completed 18m ago (stale)" || view.Activity.Severity != SeverityWarning {
					t.Fatalf("activity last-good summary not preserved as stale: %#v", view.Activity)
				}
			},
		},
		{
			name: "local protection", group: "local_backup", attention: "Local data STALE",
			mutate: func(d *DomainSnapshot) { d.LocalBackup.SourceUpdatedAt = now.Add(-31 * time.Second) },
			assert: func(t *testing.T, view ViewSnapshot) {
				if view.Protection[0].Value != "24m ago" || view.Protection[0].State != SourceStale || view.Protection[0].Severity != SeverityWarning {
					t.Fatalf("local last-good row not preserved as stale: %#v", view.Protection[0])
				}
			},
		},
		{
			name: "cloud protection", group: "cloud_snapshot", attention: "Cloud data STALE",
			mutate: func(d *DomainSnapshot) { d.CloudSnapshot.SourceUpdatedAt = now.Add(-31 * time.Second) },
			assert: func(t *testing.T, view ViewSnapshot) {
				if view.Protection[1].Value != "3h ago" || view.Protection[1].State != SourceStale || view.Protection[1].Severity != SeverityWarning {
					t.Fatalf("cloud last-good row not preserved as stale: %#v", view.Protection[1])
				}
			},
		},
		{
			name: "coverage", group: "coverage", attention: "Coverage data STALE",
			mutate: func(d *DomainSnapshot) { d.Coverage.CapturedAt = now.Add(-49 * time.Hour) },
			assert: func(t *testing.T, view ViewSnapshot) {
				if view.Protection[2].Value != "Complete" || view.Protection[2].State != SourceStale || view.Protection[2].Severity != SeverityWarning {
					t.Fatalf("coverage last-good row not preserved as stale: %#v", view.Protection[2])
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			domain := healthyDomainFixture(now)
			tt.mutate(&domain)
			view := Project(now, healthyHostFixture(now), &domain, DefaultConfig())
			if view.Header.Overall != SeverityWarning || view.Attention.Message != tt.attention || view.Attention.AdditionalCount != 0 {
				t.Fatalf("unexpected freshness attention: overall=%q attention=%#v", view.Header.Overall, view.Attention)
			}
			if view.Freshness[tt.group].SourceState != SourceStale {
				t.Fatalf("%s freshness = %#v", tt.group, view.Freshness[tt.group])
			}
			tt.assert(t, view)
		})
	}
}

func TestProjectAppliesExplicitDegradedGroupFreshness(t *testing.T) {
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	states := []SourceState{SourceFailed, SourceOffline, SourceUnavailable}
	groups := []struct {
		name      string
		freshness string
		label     string
		mutate    func(*DomainSnapshot, SourceState)
		assert    func(*testing.T, ViewSnapshot, SourceState)
	}{
		{
			name: "runtime", freshness: "runtime", label: "Runtime data",
			mutate: func(d *DomainSnapshot, state SourceState) { d.Runtime.Freshness.SourceState = state },
			assert: func(t *testing.T, view ViewSnapshot, state SourceState) {
				if view.Runtime[1].Value != "OK" || view.Runtime[1].State != state || view.Runtime[1].Severity != SeverityWarning {
					t.Fatalf("runtime last-good value was not degraded truthfully: %#v", view.Runtime[1])
				}
			},
		},
		{
			name: "network", freshness: "network", label: "Network data",
			mutate: func(d *DomainSnapshot, state SourceState) { d.Network.Freshness.SourceState = state },
			assert: func(t *testing.T, view ViewSnapshot, state SourceState) {
				if view.Network.State != "NETWORK "+strings.ToUpper(string(state)) || view.Network.Detail != "1 online / 1 known - cloud reachable (cached 0s)" || view.Network.Severity != SeverityWarning {
					t.Fatalf("network last-good summary was not degraded truthfully: %#v", view.Network)
				}
			},
		},
		{
			name: "activity", freshness: "activity", label: "Activity data",
			mutate: func(d *DomainSnapshot, state SourceState) { d.Activity.Freshness.SourceState = state },
			assert: func(t *testing.T, view ViewSnapshot, state SourceState) {
				want := "last activity: backup completed 18m ago (" + string(state) + ")"
				if view.Activity.Detail != want || view.Activity.Severity != SeverityWarning {
					t.Fatalf("activity last-good summary was not degraded truthfully: %#v", view.Activity)
				}
			},
		},
		{
			name: "local protection", freshness: "local_backup", label: "Local data",
			mutate: func(d *DomainSnapshot, state SourceState) { d.LocalBackup.SourceState = state },
			assert: func(t *testing.T, view ViewSnapshot, state SourceState) {
				if view.Protection[0].Value != "24m ago" || view.Protection[0].State != state || view.Protection[0].Severity != SeverityWarning {
					t.Fatalf("local last-good row was not degraded truthfully: %#v", view.Protection[0])
				}
			},
		},
		{
			name: "cloud protection", freshness: "cloud_snapshot", label: "Cloud data",
			mutate: func(d *DomainSnapshot, state SourceState) { d.CloudSnapshot.SourceState = state },
			assert: func(t *testing.T, view ViewSnapshot, state SourceState) {
				if view.Protection[1].Value != "3h ago" || view.Protection[1].State != state || view.Protection[1].Severity != SeverityWarning {
					t.Fatalf("cloud last-good row was not degraded truthfully: %#v", view.Protection[1])
				}
			},
		},
		{
			name: "coverage", freshness: "coverage", label: "Coverage data",
			mutate: func(d *DomainSnapshot, state SourceState) { d.Coverage.SourceState = state },
			assert: func(t *testing.T, view ViewSnapshot, state SourceState) {
				if view.Protection[2].Value != "Complete" || view.Protection[2].State != state || view.Protection[2].Severity != SeverityWarning {
					t.Fatalf("coverage last-good row was not degraded truthfully: %#v", view.Protection[2])
				}
			},
		},
	}

	for _, group := range groups {
		for _, state := range states {
			t.Run(group.name+" "+string(state), func(t *testing.T) {
				domain := healthyDomainFixture(now)
				group.mutate(&domain, state)
				view := Project(now, healthyHostFixture(now), &domain, DefaultConfig())
				wantAttention := group.label + " " + strings.ToUpper(string(state))
				if view.Header.Overall != SeverityWarning || view.Attention.Message != wantAttention || view.Attention.AdditionalCount != 0 {
					t.Fatalf("unexpected freshness attention: overall=%q attention=%#v", view.Header.Overall, view.Attention)
				}
				if view.Freshness[group.freshness].SourceState != state {
					t.Fatalf("%s freshness = %#v", group.freshness, view.Freshness[group.freshness])
				}
				group.assert(t, view, state)
			})
		}
	}
}

func TestProjectUnavailableGroupsKeepMissingValuesUnavailable(t *testing.T) {
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name   string
		mutate func(*DomainSnapshot)
		assert func(*testing.T, ViewSnapshot)
	}{
		{
			name: "runtime", mutate: func(d *DomainSnapshot) {
				d.Runtime.Available = false
				d.Runtime.Freshness = Freshness{SourceState: SourceUnavailable}
			},
			assert: func(t *testing.T, view ViewSnapshot) {
				if view.Runtime[1].Value != "Unavailable" || view.Runtime[1].State != SourceUnavailable {
					t.Fatalf("missing runtime value leaked: %#v", view.Runtime[1])
				}
			},
		},
		{
			name: "network", mutate: func(d *DomainSnapshot) {
				d.Network.Available = false
				d.Network.Freshness = Freshness{SourceState: SourceUnavailable}
			},
			assert: func(t *testing.T, view ViewSnapshot) {
				if view.Network.Detail != "Network unavailable" || view.Network.Severity != SeverityUnknown {
					t.Fatalf("missing network value leaked: %#v", view.Network)
				}
			},
		},
		{
			name: "activity", mutate: func(d *DomainSnapshot) {
				d.Activity = ActivityState{Freshness: Freshness{SourceState: SourceUnavailable}}
			},
			assert: func(t *testing.T, view ViewSnapshot) {
				if view.Activity.Detail != "No activity snapshot" || view.Activity.Severity != SeverityUnknown {
					t.Fatalf("missing activity value leaked: %#v", view.Activity)
				}
			},
		},
		{
			name: "local protection", mutate: func(d *DomainSnapshot) { d.LocalBackup = ProtectionState{SourceState: SourceUnavailable} },
			assert: func(t *testing.T, view ViewSnapshot) {
				if view.Protection[0].Value != "Unavailable" || view.Protection[0].State != SourceUnavailable {
					t.Fatalf("missing protection value leaked: %#v", view.Protection[0])
				}
			},
		},
		{
			name: "coverage", mutate: func(d *DomainSnapshot) { d.Coverage = CoverageState{SourceState: SourceUnavailable} },
			assert: func(t *testing.T, view ViewSnapshot) {
				if view.Protection[2].Value != "Unavailable" || view.Protection[2].State != SourceUnavailable {
					t.Fatalf("missing coverage value leaked: %#v", view.Protection[2])
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			domain := healthyDomainFixture(now)
			tt.mutate(&domain)
			tt.assert(t, Project(now, healthyHostFixture(now), &domain, DefaultConfig()))
		})
	}
}

func TestProjectDisabledCloudProtectionRemainsNeutral(t *testing.T) {
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	domain := healthyDomainFixture(now)
	domain.CloudSnapshot.State = "disabled"
	domain.CloudSnapshot.SourceState = SourceDisabled
	domain.CloudSnapshot.SourceUpdatedAt = time.Time{}

	view := Project(now, healthyHostFixture(now), &domain, DefaultConfig())
	if view.Header.Overall != SeverityHealthy || view.Attention.Message != "No attention required" {
		t.Fatalf("disabled cloud was not neutral: overall=%q attention=%#v", view.Header.Overall, view.Attention)
	}
	if view.Protection[1].Value != "DISABLED" || view.Protection[1].State != SourceDisabled || view.Protection[1].Severity != SeverityHealthy {
		t.Fatalf("disabled cloud row = %#v", view.Protection[1])
	}
}

func TestProjectFreshnessDoesNotDowngradeUnderlyingCritical(t *testing.T) {
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	domain := healthyDomainFixture(now)
	domain.Runtime.Freshness.SourceState = SourceUnavailable
	domain.Runtime.DatabaseState = "failed"

	view := Project(now, healthyHostFixture(now), &domain, DefaultConfig())
	if view.Runtime[1].Value != "FAILED" || view.Runtime[1].State != SourceUnavailable || view.Runtime[1].Severity != SeverityCritical {
		t.Fatalf("critical runtime evidence was downgraded: %#v", view.Runtime[1])
	}
	if view.Header.Overall != SeverityCritical || view.Attention.Message != "Database FAILED" {
		t.Fatalf("critical attention was downgraded: overall=%q attention=%#v", view.Header.Overall, view.Attention)
	}
}

func TestProjectDomainFreshnessSuppressesDuplicateGroupWarnings(t *testing.T) {
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		state     SourceState
		attention string
		detail    string
	}{
		{SourceStale, "Domain data STALE", "last activity: backup completed 18m ago (stale)"},
		{SourceFailed, "loomd OFFLINE", "last activity: backup completed 18m ago (stale)"},
		{SourceOffline, "loomd OFFLINE", "last activity: backup completed 18m ago (stale)"},
		{SourceUnavailable, "Domain data UNAVAILABLE", "last activity: backup completed 18m ago (unavailable)"},
	}
	for _, tt := range tests {
		t.Run(string(tt.state), func(t *testing.T) {
			domain := healthyDomainFixture(now)
			domain.Freshness.SourceState = tt.state
			domain.Runtime.Freshness.SourceState = SourceFailed
			domain.Network.Freshness.SourceState = SourceFailed
			domain.Activity.Freshness.SourceState = SourceFailed
			domain.LocalBackup.SourceState = SourceFailed
			domain.CloudSnapshot.SourceState = SourceFailed
			domain.Coverage.SourceState = SourceFailed
			domain.Network.Cloud.SourceState = SourceFailed

			view := Project(now, healthyHostFixture(now), &domain, DefaultConfig())
			if view.Attention.Message != tt.attention || view.Attention.AdditionalCount != 0 {
				t.Fatalf("duplicate freshness warnings were not suppressed: %#v", view.Attention)
			}
			if view.Activity.Detail != tt.detail {
				t.Fatalf("activity freshness marker duplicated or missing: %q", view.Activity.Detail)
			}
		})
	}
}

func TestProjectHostMetricSourceStatesFailClosed(t *testing.T) {
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name      string
		state     SourceState
		wantState SourceState
	}{
		{"stale", SourceStale, SourceStale},
		{"failed", SourceFailed, SourceFailed},
		{"offline", SourceOffline, SourceOffline},
		{"unavailable", SourceUnavailable, SourceUnavailable},
		{"unknown", SourceUnknown, SourceUnknown},
		{"invalid", SourceState("invented"), SourceUnknown},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			host := healthyHostFixture(now)
			host.CPU.SourceState = tt.state
			view := Project(now, host, ptrDomain(healthyDomainFixture(now)), DefaultConfig())
			row := view.System[0]
			if row.Value != "18%" || row.State != tt.wantState || row.Severity != SeverityWarning {
				t.Fatalf("host last-good metric was not projected truthfully: %#v", row)
			}
			wantAttention := "CPU sensor " + strings.ToUpper(string(tt.wantState))
			if view.Header.Overall != SeverityWarning || view.Attention.Message != wantAttention || view.Attention.AdditionalCount != 0 {
				t.Fatalf("unexpected host freshness attention: overall=%q attention=%#v", view.Header.Overall, view.Attention)
			}
		})
	}
}

func TestProjectHostMissingMetricsRemainUnavailable(t *testing.T) {
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name   string
		metric Metric
	}{
		{"not available", Metric{Available: false, SourceState: SourceUnavailable}},
		{"available without value", Metric{Available: true, SampledAt: now, StaleAfterSeconds: 6, SourceState: SourceLive}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			host := healthyHostFixture(now)
			host.CPU = tt.metric
			view := Project(now, host, ptrDomain(healthyDomainFixture(now)), DefaultConfig())
			if view.System[0].Value != "Unavailable" || view.System[0].State != SourceUnavailable || view.System[0].Severity != SeverityUnknown {
				t.Fatalf("missing host metric leaked a value: %#v", view.System[0])
			}
			if view.Attention.Message != "CPU Unavailable" || view.Header.Overall != SeverityWarning {
				t.Fatalf("missing host metric attention = %#v", view.Attention)
			}
		})
	}
}

func TestProjectHostSnapshotFreshnessDominatesSensorFreshness(t *testing.T) {
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	host := healthyHostFixture(now)
	host.GeneratedAt = now.Add(-7 * time.Second)
	host.CPU.SourceState = SourceFailed

	view := Project(now, host, ptrDomain(healthyDomainFixture(now)), DefaultConfig())
	if view.Freshness["host"].SourceState != SourceStale || view.Attention.Message != "Host data STALE" || view.Attention.AdditionalCount != 0 {
		t.Fatalf("host freshness did not dominate sensor conditions: freshness=%#v attention=%#v", view.Freshness["host"], view.Attention)
	}
	if view.System[0].State != SourceFailed || view.System[0].Severity != SeverityWarning {
		t.Fatalf("explicit sensor state was not retained: %#v", view.System[0])
	}
	for i := 1; i < len(view.System); i++ {
		if view.System[i].State != SourceStale || view.System[i].Severity != SeverityWarning {
			t.Fatalf("host-stale row %d remained healthy: %#v", i, view.System[i])
		}
	}
}

func TestProjectHostFreshnessPreservesCriticalThreshold(t *testing.T) {
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	host := healthyHostFixture(now)
	host.GeneratedAt = now.Add(-7 * time.Second)
	host.CPU.Severity = SeverityCritical

	view := Project(now, host, ptrDomain(healthyDomainFixture(now)), DefaultConfig())
	if view.System[0].State != SourceStale || view.System[0].Severity != SeverityCritical {
		t.Fatalf("host freshness downgraded a critical threshold: %#v", view.System[0])
	}
	if view.Header.Overall != SeverityCritical || view.Attention.Message != "CRITICAL CPU" || view.Attention.AdditionalCount != 1 {
		t.Fatalf("critical host threshold attention = %#v", view.Attention)
	}
}

func TestProjectCloudCacheFreshnessAffectsNetworkBand(t *testing.T) {
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name      string
		state     SourceState
		updatedAt time.Time
		wantState SourceState
	}{
		{"stale", SourceCached, now.Add(-13 * time.Hour), SourceStale},
		{"failed", SourceFailed, now, SourceFailed},
		{"offline", SourceOffline, now, SourceOffline},
		{"unavailable", SourceUnavailable, now, SourceUnavailable},
		{"invalid", SourceState("invented"), now, SourceUnknown},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			domain := healthyDomainFixture(now)
			domain.Network.Cloud.Cached = true
			domain.Network.Cloud.SourceState = tt.state
			domain.Network.Cloud.CacheUpdatedAt = tt.updatedAt
			view := Project(now, healthyHostFixture(now), &domain, DefaultConfig())
			if view.Freshness["cloud_cache"].SourceState != tt.wantState {
				t.Fatalf("cloud cache freshness = %#v", view.Freshness["cloud_cache"])
			}
			marker := "CACHE " + strings.ToUpper(string(tt.wantState))
			if view.Network.Severity != SeverityWarning || !strings.Contains(view.Network.Detail, "last-good reachable") || !strings.Contains(view.Network.Detail, marker) {
				t.Fatalf("cloud cache was not projected into network: %#v", view.Network)
			}
			if view.Attention.Message != "Cloud cache "+strings.ToUpper(string(tt.wantState)) || view.Attention.AdditionalCount != 0 {
				t.Fatalf("cloud cache attention = %#v", view.Attention)
			}
		})
	}
}

func TestProjectCloudCacheWithoutEvidenceDoesNotClaimReachability(t *testing.T) {
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	domain := healthyDomainFixture(now)
	domain.Network.Cloud.Cached = false
	domain.Network.Cloud.CacheUpdatedAt = time.Time{}
	domain.Network.Cloud.SourceState = SourceUnavailable

	view := Project(now, healthyHostFixture(now), &domain, DefaultConfig())
	if view.Network.Detail != "1 online / 1 known - cloud unavailable (CACHE UNAVAILABLE)" || view.Network.Severity != SeverityWarning {
		t.Fatalf("cache without evidence claimed reachability: %#v", view.Network)
	}
}

func TestProjectUnavailableCloudWithoutAvailableFlagRemainsTruthful(t *testing.T) {
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	for _, state := range []SourceState{SourceUnavailable, SourceFailed} {
		t.Run(string(state), func(t *testing.T) {
			domain := healthyDomainFixture(now)
			domain.Network.Cloud.Available = false
			domain.Network.Cloud.Cached = false
			domain.Network.Cloud.CacheUpdatedAt = time.Time{}
			domain.Network.Cloud.SourceState = state

			view := Project(now, healthyHostFixture(now), &domain, DefaultConfig())
			if view.Freshness["cloud_cache"].SourceState != state {
				t.Fatalf("cloud cache freshness = %#v", view.Freshness["cloud_cache"])
			}
			wantDetail := "1 online / 1 known - cloud unavailable (CACHE " + strings.ToUpper(string(state)) + ")"
			if view.Network.Detail != wantDetail || view.Network.Severity != SeverityWarning {
				t.Fatalf("unavailable cloud was not projected truthfully: %#v", view.Network)
			}
			if view.Attention.Message != "Cloud cache "+strings.ToUpper(string(state)) || view.Attention.AdditionalCount != 0 {
				t.Fatalf("unavailable cloud attention = %#v", view.Attention)
			}
		})
	}
}

func TestProjectCloudCacheConditionIsSuppressedByNetworkFreshness(t *testing.T) {
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	domain := healthyDomainFixture(now)
	domain.Network.Freshness.SourceState = SourceFailed
	domain.Network.Cloud.SourceState = SourceOffline

	view := Project(now, healthyHostFixture(now), &domain, DefaultConfig())
	if view.Attention.Message != "Network data FAILED" || view.Attention.AdditionalCount != 0 {
		t.Fatalf("network/cache duplicate was not suppressed: %#v", view.Attention)
	}
	if !strings.Contains(view.Network.Detail, "cloud last-good reachable") || !strings.Contains(view.Network.Detail, "CACHE OFFLINE") {
		t.Fatalf("dominated cache state was hidden from network detail: %q", view.Network.Detail)
	}
}

func TestProjectDisabledNetworkCloudRemainsNeutral(t *testing.T) {
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	domain := healthyDomainFixture(now)
	domain.Network.Cloud.Enabled = false
	domain.Network.Cloud.SourceState = SourceFailed
	domain.Network.Cloud.CacheUpdatedAt = time.Time{}

	view := Project(now, healthyHostFixture(now), &domain, DefaultConfig())
	if view.Network.Detail != "1 online / 1 known - cloud disabled" || view.Network.Severity != SeverityHealthy || view.Attention.Message != "No attention required" {
		t.Fatalf("disabled network cloud was not neutral: network=%#v attention=%#v", view.Network, view.Attention)
	}
	if view.Freshness["cloud_cache"].SourceState != SourceDisabled {
		t.Fatalf("disabled cloud cache freshness = %#v", view.Freshness["cloud_cache"])
	}
}

func TestProjectNetworkChildConditionsRaiseBandSeverity(t *testing.T) {
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name   string
		mutate func(*DomainSnapshot)
	}{
		{"expected node offline", func(d *DomainSnapshot) { d.Network.Nodes[0].Online = false }},
		{"cloud unreachable", func(d *DomainSnapshot) { d.Network.Cloud.State = "unreachable" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			domain := healthyDomainFixture(now)
			tt.mutate(&domain)
			view := Project(now, healthyHostFixture(now), &domain, DefaultConfig())
			if view.Network.Severity != SeverityWarning {
				t.Fatalf("network child warning did not affect band: %#v", view.Network)
			}
		})
	}

	domain := healthyDomainFixture(now)
	domain.Network.Nodes[0].Online = false
	domain.Network.Communication.DeadLetter = 1
	view := Project(now, healthyHostFixture(now), &domain, DefaultConfig())
	if view.Network.Severity != SeverityCritical {
		t.Fatalf("network child warning downgraded critical communication: %#v", view.Network)
	}
}

func TestProjectInvalidSourceStatesFailClosed(t *testing.T) {
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name      string
		mutate    func(*DomainSnapshot)
		attention string
		assert    func(*testing.T, ViewSnapshot)
	}{
		{
			name: "top level", attention: "Domain data UNKNOWN",
			mutate: func(d *DomainSnapshot) { d.Freshness.SourceState = SourceState("invented") },
			assert: func(t *testing.T, view ViewSnapshot) {
				if view.Header.FreshnessState != SourceUnknown {
					t.Fatalf("invalid domain state rendered live: %#v", view.Header)
				}
			},
		},
		{
			name: "nested runtime", attention: "Runtime data UNKNOWN",
			mutate: func(d *DomainSnapshot) { d.Runtime.Freshness.SourceState = SourceState("invented") },
			assert: func(t *testing.T, view ViewSnapshot) {
				if view.Runtime[1].State != SourceUnknown || view.Runtime[1].Severity != SeverityWarning {
					t.Fatalf("invalid runtime state rendered live: %#v", view.Runtime[1])
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			domain := healthyDomainFixture(now)
			tt.mutate(&domain)
			view := Project(now, healthyHostFixture(now), &domain, DefaultConfig())
			if view.Header.Overall != SeverityWarning || view.Attention.Message != tt.attention {
				t.Fatalf("invalid source state attention = %#v", view.Attention)
			}
			tt.assert(t, view)
		})
	}
}

func TestProjectRuntimeStateIsTruthful(t *testing.T) {
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name      string
		state     SourceState
		wantState SourceState
		severity  Severity
		attention string
	}{
		{"failed", SourceFailed, SourceFailed, SeverityCritical, "loomd FAILED"},
		{"offline", SourceOffline, SourceOffline, SeverityCritical, "loomd OFFLINE"},
		{"unavailable", SourceUnavailable, SourceUnavailable, SeverityWarning, "loomd UNAVAILABLE"},
		{"unknown", SourceUnknown, SourceUnknown, SeverityWarning, "loomd UNKNOWN"},
		{"stale", SourceStale, SourceStale, SeverityWarning, "loomd STALE"},
		{"disabled", SourceDisabled, SourceDisabled, SeverityWarning, "loomd DISABLED"},
		{"invalid", SourceState("invented"), SourceUnknown, SeverityWarning, "loomd UNKNOWN"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			domain := healthyDomainFixture(now)
			domain.Runtime.State = tt.state
			view := Project(now, healthyHostFixture(now), &domain, DefaultConfig())
			row := view.Runtime[0]
			if row.Value != strings.ToUpper(string(tt.wantState)) || row.State != tt.wantState || row.Severity != tt.severity {
				t.Fatalf("runtime state was not projected truthfully: %#v", row)
			}
			if view.Attention.Message != tt.attention || view.Attention.AdditionalCount != 0 {
				t.Fatalf("runtime state attention = %#v", view.Attention)
			}
		})
	}
}

func TestProjectRuntimeStateAvoidsDominatedConditions(t *testing.T) {
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	t.Run("domain freshness", func(t *testing.T) {
		domain := healthyDomainFixture(now)
		domain.Freshness.SourceState = SourceStale
		domain.Runtime.State = SourceUnavailable
		view := Project(now, healthyHostFixture(now), &domain, DefaultConfig())
		if view.Attention.Message != "Domain data STALE" || view.Attention.AdditionalCount != 0 {
			t.Fatalf("domain/runtime state conditions duplicated: %#v", view.Attention)
		}
		if view.Runtime[0].Value != "UNAVAILABLE" || view.Runtime[0].Severity != SeverityWarning {
			t.Fatalf("dominated runtime state was hidden: %#v", view.Runtime[0])
		}
	})
	t.Run("group freshness", func(t *testing.T) {
		domain := healthyDomainFixture(now)
		domain.Runtime.Freshness.SourceState = SourceFailed
		view := Project(now, healthyHostFixture(now), &domain, DefaultConfig())
		if view.Attention.Message != "Runtime data FAILED" || view.Attention.AdditionalCount != 0 {
			t.Fatalf("runtime freshness conditions duplicated: %#v", view.Attention)
		}
		if view.Runtime[0].Value != "OK" || view.Runtime[0].State != SourceFailed || view.Runtime[0].Severity != SeverityWarning {
			t.Fatalf("runtime freshness was not projected: %#v", view.Runtime[0])
		}
	})
}

func TestProjectActivityUsesAllowlistedStates(t *testing.T) {
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name      string
		state     string
		detail    string
		severity  Severity
		attention string
	}{
		{"completed", "completed", "last activity: backup completed 18m ago", SeverityHealthy, "No attention required"},
		{"failed", "failed", "last activity: backup failed 18m ago", SeverityCritical, "Activity FAILED"},
		{"unknown", "payload-controlled text", "last activity: backup state unknown 18m ago", SeverityWarning, "Activity state UNKNOWN"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			domain := healthyDomainFixture(now)
			domain.Activity.Recent.State = tt.state
			view := Project(now, healthyHostFixture(now), &domain, DefaultConfig())
			if view.Activity.Detail != tt.detail || view.Activity.Severity != tt.severity {
				t.Fatalf("activity state projection = %#v", view.Activity)
			}
			if strings.Contains(view.Activity.Detail, tt.state) && tt.name == "unknown" {
				t.Fatalf("arbitrary activity state leaked: %q", view.Activity.Detail)
			}
			if view.Attention.Message != tt.attention {
				t.Fatalf("activity attention = %#v", view.Attention)
			}
		})
	}

	domain := healthyDomainFixture(now)
	started := now.Add(-2 * time.Minute)
	domain.Activity.Running = &Activity{Kind: "main_backup", State: "running", StartedAt: &started}
	view := Project(now, healthyHostFixture(now), &domain, DefaultConfig())
	if view.Activity.State != "ACTIVE" || view.Activity.Detail != "backup in progress" || view.Activity.Severity != SeverityActive || view.Header.Overall != SeverityActive {
		t.Fatalf("running activity projection = %#v", view)
	}
}

func TestProjectHostMetricSeverityFailsClosed(t *testing.T) {
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name      string
		severity  Severity
		want      Severity
		attention string
	}{
		{"healthy", SeverityHealthy, SeverityHealthy, "No attention required"},
		{"warning", SeverityWarning, SeverityWarning, "WARNING CPU"},
		{"critical", SeverityCritical, SeverityCritical, "CRITICAL CPU"},
		{"unknown", SeverityUnknown, SeverityWarning, "CPU severity UNKNOWN"},
		{"invalid", Severity("invented"), SeverityWarning, "CPU severity UNKNOWN"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			host := healthyHostFixture(now)
			host.CPU.Severity = tt.severity
			view := Project(now, host, ptrDomain(healthyDomainFixture(now)), DefaultConfig())
			if view.System[0].Value != "18%" || view.System[0].Severity != tt.want {
				t.Fatalf("metric severity projection = %#v", view.System[0])
			}
			if view.Attention.Message != tt.attention {
				t.Fatalf("metric severity attention = %#v", view.Attention)
			}
		})
	}
}

func TestAttentionSameSeverityUsesPublishedCategoryOrder(t *testing.T) {
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	host := healthyHostFixture(now)
	host.Storage.Severity = SeverityWarning
	domain := healthyDomainFixture(now)
	domain.Runtime.DegradedWorkers = 1
	domain.Network.Nodes[0].Online = false
	view := Project(now, host, &domain, DefaultConfig())
	if view.Attention.Message != "WARNING Storage" || view.Attention.AdditionalCount != 2 {
		t.Fatalf("attention ordering = %#v", view.Attention)
	}
}

func healthyHostFixture(now time.Time) HostSnapshot {
	return HostSnapshot{
		GeneratedAt: now,
		CPU:         metricFixture(now, 18),
		Temperature: metricFixture(now, 43),
		Memory:      metricFixture(now, 37),
		Storage:     metricFixture(now, 61),
	}
}

func metricFixture(now time.Time, value float64) Metric {
	return Metric{Available: true, Value: &value, SampledAt: now, StaleAfterSeconds: 6, SourceState: SourceLive, Severity: SeverityHealthy}
}

func ptrDomain(value DomainSnapshot) *DomainSnapshot {
	return &value
}

func healthyDomainFixture(now time.Time) DomainSnapshot {
	localSuccess := now.Add(-24 * time.Minute)
	cloudSuccess := now.Add(-3 * time.Hour)
	recent := now.Add(-18 * time.Minute)
	return DomainSnapshot{
		SchemaVersion: SchemaVersion,
		GeneratedAt:   now,
		Freshness:     Freshness{SourceUpdatedAt: now, StaleAfterSeconds: 30, SourceState: SourceLive},
		Node:          NodeIdentity{Key: "main", Label: "MAIN"},
		Runtime: RuntimeState{
			Freshness: Freshness{SourceUpdatedAt: now, StaleAfterSeconds: 30, SourceState: SourceLive},
			Available: true, State: SourceLive, DatabaseState: "ok", MigrationsCurrent: true,
			EnabledWorkers: 16, HealthyWorkers: 16,
		},
		LocalBackup: ProtectionState{
			Available: true, State: "healthy", LastSuccessAt: &localSuccess, VerificationState: "succeeded",
			IntervalSeconds: int64((24 * time.Hour) / time.Second), SourceUpdatedAt: now, StaleAfterSeconds: 30, SourceState: SourceLive,
		},
		CloudSnapshot: ProtectionState{
			Available: true, State: "healthy", LastSuccessAt: &cloudSuccess, VerificationState: "succeeded",
			IntervalSeconds: int64((6 * time.Hour) / time.Second), SourceUpdatedAt: now, StaleAfterSeconds: 30, SourceState: SourceLive,
		},
		Coverage: CoverageState{Available: true, State: "complete", CapturedAt: localSuccess, StaleAfterSeconds: int64((48 * time.Hour) / time.Second), SourceState: SourceCached},
		Findings: FindingSummary{Available: true},
		Network: NetworkState{
			Freshness:     Freshness{SourceUpdatedAt: now, StaleAfterSeconds: 30, SourceState: SourceLive},
			Available:     true,
			Nodes:         []NodePresence{{Key: "main", Label: "MAIN", Online: true}},
			Communication: CommunicationSummary{Available: true},
			Cloud:         CloudReachability{Available: true, Enabled: true, State: "healthy", Cached: true, CheckedAt: now, CacheUpdatedAt: now, SourceState: SourceCached},
		},
		Activity: ActivityState{Freshness: Freshness{SourceUpdatedAt: now, StaleAfterSeconds: 30, SourceState: SourceLive}, Recent: &Activity{Kind: "main_backup", State: "completed", CompletedAt: &recent}},
	}
}
