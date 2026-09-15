package serviceregistry

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestApplicationHealthRequiresExactProcessAndBoundedProtocol(t *testing.T) {
	r, _, q, p := applicationTestRuntime(t)
	_ = r
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/redirect" {
			w.Header().Set("Location", "https://invalid.example/")
			w.WriteHeader(302)
			return
		}
		if r.URL.Path == "/large" {
			w.Write([]byte(strings.Repeat("x", 4097)))
			return
		}
		w.WriteHeader(204)
	}))
	defer server.Close()
	_, port, _ := net.SplitHostPort(strings.TrimPrefix(server.URL, "http://"))
	n, _ := strconv.Atoi(port)
	m := *q.Manifest
	m.Listener = &ApplicationListener{Address: "127.0.0.1", Port: uint16(n)}
	m.Health = ApplicationHealth{Kind: HealthKindHTTP, Method: "GET", Path: "/health", ExpectedStatus: 204, TimeoutSeconds: 1}
	observation := ApplicationProcessObservation{ListenerOwned: true, State: "active", PID: 12, UID: applicationAccountUID(q.Owner), InvocationID: strings.Repeat("1", 32), Executable: p.Descriptor.Executable, ControlGroup: "/system.slice/" + q.Owner.Unit(), Generation: "generation-a"}
	observe := func(context.Context) (ApplicationProcessObservation, error) { return observation, nil }
	if _, ready, e := ProbeApplicationHealth(context.Background(), m, q.Owner, "generation-a", p.Descriptor.Executable, observation.UID, observe); e != nil || ready.Protocol != "satisfied" {
		t.Fatalf("health=%+v error=%v", ready, e)
	}
	for _, state := range []string{"activating", "inactive", "failed"} {
		observation.State = state
		if _, _, e := ProbeApplicationHealth(context.Background(), m, q.Owner, "generation-a", p.Descriptor.Executable, observation.UID, observe); e == nil {
			t.Fatal("wrong process accepted")
		}
	}
	observation.State = "active"
	for _, path := range []string{"/redirect", "/large"} {
		m.Health.Path = path
		if _, _, e := ProbeApplicationHealth(context.Background(), m, q.Owner, "generation-a", p.Descriptor.Executable, observation.UID, observe); e == nil {
			t.Fatal("invalid response accepted")
		}
	}
	observation.Executable = "/bin/unrelated"
	if _, _, e := ProbeApplicationHealth(context.Background(), m, q.Owner, "generation-a", p.Descriptor.Executable, observation.UID, observe); e == nil {
		t.Fatal("occupied port from unrelated process accepted")
	}
}

func TestApplicationStartupWaitsForProcessListenerAndProtocol(t *testing.T) {
	_, _, q, p := applicationTestRuntime(t)
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if requests.Add(1) == 1 {
			w.WriteHeader(503)
			return
		}
		w.WriteHeader(401)
	}))
	defer server.Close()
	port := server.Listener.Addr().(*net.TCPAddr).Port
	m := *q.Manifest
	m.Listener = &ApplicationListener{Address: "127.0.0.1", Port: uint16(port)}
	m.Health = ApplicationHealth{Kind: HealthKindHTTP, Method: "OPTIONS", Path: "/", ExpectedStatus: 401, TimeoutSeconds: 1}
	process := ApplicationProcessObservation{State: "active", PID: 12, UID: 1234, InvocationID: strings.Repeat("1", 32), Executable: p.Descriptor.Executable, ControlGroup: "/system.slice/" + q.Owner.Unit(), Generation: "generation-a", ListenerOwned: true}
	observations := 0
	observe := func(context.Context) (ApplicationProcessObservation, error) {
		observations++
		current := process
		switch observations {
		case 1:
			current.Executable = "/bin/startup-launcher"
		case 2:
			current.ListenerOwned = false
		}
		if observations <= 2 && requests.Load() != 0 {
			t.Fatal("HTTP probe preceded process/listener ownership")
		}
		return current, nil
	}
	got, ready, err := waitApplicationHealth(t.Context(), 2*time.Second, m, q.Owner, process.Generation, process.Executable, process.UID, observe)
	if err != nil || ready.Process != "satisfied" || ready.Protocol != "satisfied" || got.PID != process.PID || requests.Load() != 2 {
		t.Fatalf("startup=%+v readiness=%+v requests=%d err=%v", got, ready, requests.Load(), err)
	}
}

func TestApplicationStartupTimeoutAndCancellation(t *testing.T) {
	_, _, q, p := applicationTestRuntime(t)
	for _, cancelled := range []bool{false, true} {
		ctx, cancel := context.WithCancel(t.Context())
		if cancelled {
			cancel()
		}
		_, ready, err := waitApplicationHealth(ctx, 20*time.Millisecond, *q.Manifest, q.Owner, "generation-a", p.Descriptor.Executable, 1234, func(context.Context) (ApplicationProcessObservation, error) {
			return ApplicationProcessObservation{State: "failed"}, nil
		})
		cancel()
		want := "application.health.startup_timeout"
		if cancelled {
			want = "application.operation.cancelled"
		}
		if err == nil || err.Error() != want || ready.Process == "satisfied" {
			t.Fatalf("cancelled=%v readiness=%+v err=%v", cancelled, ready, err)
		}
	}
}
