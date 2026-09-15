package serviceregistry

import (
	"context"
	"io"
	"net"
	"net/http"
	"strconv"
	"time"
)

type ApplicationProcessObservation struct {
	ListenerOwned bool   `json:"listener_owned"`
	State         string `json:"state"`
	PID           int    `json:"pid"`
	UID           uint32 `json:"uid"`
	InvocationID  string `json:"invocation_id"`
	Executable    string `json:"executable"`
	ControlGroup  string `json:"control_group"`
	Generation    string `json:"generation"`
}
type ApplicationReadiness struct {
	Process    string `json:"process"`
	Protocol   string `json:"protocol"`
	Ingress    string `json:"ingress"`
	Client     string `json:"client"`
	Protection string `json:"protection"`
}

func applicationUnknownReadiness() ApplicationReadiness {
	return ApplicationReadiness{Process: "unknown", Protocol: "unknown", Ingress: "not_applicable", Client: "unknown", Protection: "unknown"}
}
func applicationProcessMatches(p ApplicationProcessObservation, owner ApplicationOwner, generation, executable string, uid uint32) bool {
	return p.State == "active" && p.PID > 0 && p.UID == uid && len(p.InvocationID) == 32 && p.Executable == executable && p.Generation == generation && p.ControlGroup == "/system.slice/"+owner.Unit()
}

// Type=exec confirms the launcher started, not that it has execed the service
// or opened its listener. Inspect remains a single probe; only apply waits.
func waitApplicationHealth(ctx context.Context, budget time.Duration, m ApplicationManifest, owner ApplicationOwner, generation, executable string, uid uint32, observe func(context.Context) (ApplicationProcessObservation, error)) (ApplicationProcessObservation, ApplicationReadiness, error) {
	wait, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	for {
		process, readiness, err := ProbeApplicationHealth(wait, m, owner, generation, executable, uid, observe)
		if ctx.Err() != nil {
			return process, readiness, applicationError("operation.cancelled")
		}
		if wait.Err() != nil {
			return process, readiness, applicationError("health.startup_timeout")
		}
		if err == nil {
			return process, readiness, nil
		}
		switch err.Error() {
		case "application.health.process_unproven", "application.health.listener_unproven", "application.health.probe_failed":
		default:
			return process, readiness, err
		}
		timer := time.NewTimer(100 * time.Millisecond)
		select {
		case <-wait.Done():
			timer.Stop()
			if ctx.Err() != nil {
				return process, readiness, applicationError("operation.cancelled")
			}
			return process, readiness, applicationError("health.startup_timeout")
		case <-timer.C:
		}
	}
}

// Probe only after exact process ownership evidence, and require the same
// InvocationID afterwards. No ambient proxy, redirects, response bodies or
// server-controlled diagnostics enter receipts.
func ProbeApplicationHealth(ctx context.Context, m ApplicationManifest, owner ApplicationOwner, generation, executable string, uid uint32, observe func(context.Context) (ApplicationProcessObservation, error)) (ApplicationProcessObservation, ApplicationReadiness, error) {
	readiness := applicationUnknownReadiness()
	before, err := observe(ctx)
	if err != nil || !applicationProcessMatches(before, owner, generation, executable, uid) {
		readiness.Process = "pending"
		return before, readiness, applicationError("health.process_unproven")
	}
	readiness.Process = "satisfied"
	if m.Health.Kind == HealthKindManager {
		readiness.Protocol = "not_applicable"
		return before, readiness, nil
	}
	if m.Listener == nil || (m.Listener.Address != "127.0.0.1" && m.Listener.Address != "::1") || m.Listener.Port < 1024 || !applicationHealthPath(m.Health.Path) || (m.Health.Method != "GET" && m.Health.Method != "HEAD" && m.Health.Method != "OPTIONS") || m.Health.TimeoutSeconds < 1 || m.Health.TimeoutSeconds > 60 {
		return before, readiness, applicationError("health.probe_invalid")
	}
	if !before.ListenerOwned {
		return before, readiness, applicationError("health.listener_unproven")
	}
	transport := &http.Transport{Proxy: nil, DialContext: (&net.Dialer{Timeout: time.Second}).DialContext, DisableKeepAlives: true, MaxResponseHeaderBytes: 8192}
	defer transport.CloseIdleConnections()
	client := http.Client{Transport: transport, Timeout: time.Duration(m.Health.TimeoutSeconds) * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	request, err := http.NewRequestWithContext(ctx, m.Health.Method, "http://"+net.JoinHostPort(m.Listener.Address, strconv.Itoa(int(m.Listener.Port)))+m.Health.Path, nil)
	if err != nil {
		return before, readiness, applicationError("health.probe_invalid")
	}
	response, err := client.Do(request)
	readiness.Protocol = "pending"
	if err != nil {
		return before, readiness, applicationError("health.probe_failed")
	}
	n, readErr := io.Copy(io.Discard, io.LimitReader(response.Body, 4097))
	_ = response.Body.Close()
	after, observeErr := observe(ctx)
	if readErr != nil || n > 4096 || response.StatusCode != m.Health.ExpectedStatus || observeErr != nil || after.InvocationID != before.InvocationID || !after.ListenerOwned || !applicationProcessMatches(after, owner, generation, executable, uid) {
		return after, readiness, applicationError("health.probe_failed")
	}
	readiness.Protocol = "satisfied"
	return after, readiness, nil
}
