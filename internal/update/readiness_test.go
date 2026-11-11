package update

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

const testReadinessExecutable = "/nix/store/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-loomd/bin/loomd"

func TestDefaultCommandRunnerSeparatesDiagnosticsFromData(t *testing.T) {
	output, err := defaultCommandRunner(context.Background(), "sh", "-c", "printf 'expected-path'; printf 'fetch warning' >&2")
	if err != nil || string(output) != "expected-path" {
		t.Fatalf("stdout contaminated: %q %v", output, err)
	}
	output, err = defaultCommandRunner(context.Background(), "sh", "-c", "printf 'partial'; printf 'actual failure' >&2; exit 1")
	if err == nil || !strings.Contains(err.Error(), "actual failure") || string(output) != "partial" {
		t.Fatalf("failure diagnostics lost: %q %v", output, err)
	}
}

func testReadinessTarget() ReadinessTarget {
	return ReadinessTarget{ReleaseID: "release-reviewed", ReleasePath: "/srv/loom/releases/reviewed", Executable: testReadinessExecutable, MigrationHead: 36}
}

func testReadinessProcess() ProcessIdentity {
	return ProcessIdentity{PID: 42, StartTicks: "12345", Executable: testReadinessExecutable}
}

func testTargetHealth(process ProcessIdentity, head int64) []byte {
	raw, _ := json.Marshal(map[string]any{"ok": true, "data": map[string]any{
		"service": "loomd", "status": "ok", "version": "0.0.0-dev", "instance": process,
		"checks": map[string]any{"migrations": map[string]any{"status": "ok", "current_version": head, "latest_version": head}},
	}})
	return raw
}

func testProcessStat(process ProcessIdentity) []byte {
	fields := strings.Fields("S 1 1 1 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 " + process.StartTicks)
	return []byte(fmt.Sprintf("%d (loomd (serve)) %s", process.PID, strings.Join(fields, " ")))
}

func readinessFixtureRunner(ctx context.Context, name string, args ...string) ([]byte, error) {
	process := testReadinessProcess()
	switch name {
	case "nix":
		return []byte(strings.TrimSuffix(testReadinessExecutable, "/bin/loomd")), nil
	case "sudo":
		return []byte("switched"), nil
	case "systemctl":
		return []byte(fmt.Sprintf("MainPID=%d\nActiveState=active\nSubState=running\n", process.PID)), nil
	case "cat":
		return testProcessStat(process), nil
	case "readlink":
		return []byte(process.Executable), nil
	case "loom":
		return testTargetHealth(process, 36), nil
	default:
		return nil, fmt.Errorf("unexpected command %s %v", name, args)
	}
}

func TestReadinessResolvesReviewedPackageWithoutBuild(t *testing.T) {
	var called string
	target, err := resolveReadinessTarget(context.Background(), func(_ context.Context, name string, args ...string) ([]byte, error) {
		called = name + " " + strings.Join(args, " ")
		return []byte(strings.TrimSuffix(testReadinessExecutable, "/bin/loomd")), nil
	}, ReleaseState{ReleaseID: "reviewed", Path: "/srv/release"}, ".#hardware-main", 83)
	if err != nil || target.Executable != testReadinessExecutable || target.MigrationHead != 83 {
		t.Fatalf("target=%+v error=%v", target, err)
	}
	if !strings.Contains(called, "nix eval --raw --no-write-lock-file --option allow-import-from-derivation false /srv/release#nixosConfigurations.\"hardware-main\".config.loom.service.package.outPath") {
		t.Fatal(called)
	}
	_, err = resolveReadinessTarget(context.Background(), readinessFixtureRunner, ReleaseState{Path: "/reviewed"}, "/other#main", 83)
	if err == nil {
		t.Fatal("accepted a different release flake")
	}
}

func TestReadinessCrossUserExecutableUsesOnlyFixedRead(t *testing.T) {
	for _, allowed := range []bool{true, false} {
		sudoCalls := 0
		runner := func(ctx context.Context, name string, args ...string) ([]byte, error) {
			if name == "readlink" && len(args) == 1 {
				return nil, errors.New("permission denied")
			}
			if name == "sudo" {
				sudoCalls++
				if strings.Join(args, " ") != "-n readlink /proc/42/exe" {
					t.Fatalf("unexpected privileged command %v", args)
				}
				if !allowed {
					return nil, errors.New("operator read unavailable")
				}
				return []byte(testReadinessExecutable), nil
			}
			return readinessFixtureRunner(ctx, name, args...)
		}
		_, err := runTargetReadiness(context.Background(), runner, testReadinessTarget())
		if (err == nil) != allowed || sudoCalls == 0 {
			t.Fatalf("allowed=%v calls=%d err=%v", allowed, sudoCalls, err)
		}
	}
}

func TestReadinessTransportThenExactTarget(t *testing.T) {
	healthCalls := 0
	runner := func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if name == "loom" {
			healthCalls++
			if healthCalls < 3 {
				return []byte(`{"ok":false,"error":{"code":"transport.unavailable"}}`), errors.New("not listening")
			}
		}
		return readinessFixtureRunner(ctx, name, args...)
	}
	evidence, err := runTargetReadinessUntilReady(context.Background(), runner, testReadinessTarget(), time.Second, time.Millisecond)
	if err != nil || healthCalls != 4 || evidence.Process != testReadinessProcess() || evidence.ObservedAt.IsZero() {
		t.Fatalf("evidence=%+v calls=%d error=%v", evidence, healthCalls, err)
	}
}

func TestReadinessRejectsOldDaemonUnknownIdentityAndWrongSchema(t *testing.T) {
	for _, scenario := range []string{"old executable", "unknown instance", "wrong schema", "migration failure", "unhealthy", "wrong health process"} {
		t.Run(scenario, func(t *testing.T) {
			healthCalls := 0
			runner := func(ctx context.Context, name string, args ...string) ([]byte, error) {
				if name == "readlink" && len(args) == 1 && scenario == "old executable" {
					return []byte("/nix/store/bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb-old/bin/loomd"), nil
				}
				if name == "loom" {
					healthCalls++
					process, head := testReadinessProcess(), int64(36)
					switch scenario {
					case "unknown instance":
						process.PID = 0
					case "wrong schema":
						head = 35
					case "wrong health process":
						process.PID = 99
					case "migration failure":
						return []byte(`{"ok":true,"data":{"service":"loomd","status":"ok","checks":{"migrations":{"status":"unhealthy","error":"migration failed"}}}}`), nil
					case "unhealthy":
						return []byte(`{"ok":true,"data":{"service":"loomd","status":"unhealthy"}}`), nil
					}
					return testTargetHealth(process, head), nil
				}
				return readinessFixtureRunner(ctx, name, args...)
			}
			_, err := runTargetReadinessUntilReady(context.Background(), runner, testReadinessTarget(), time.Second, time.Millisecond)
			if err == nil || healthCalls > 2 {
				t.Fatalf("error=%v health calls=%d", err, healthCalls)
			}
		})
	}
}

func TestReadinessRepeatsProcessReplacementObservation(t *testing.T) {
	observations, healthCalls := 0, 0
	process := testReadinessProcess()
	runner := func(ctx context.Context, name string, args ...string) ([]byte, error) {
		switch name {
		case "systemctl":
			observations++
			if observations > 1 {
				process.PID, process.StartTicks = 43, "12346"
			}
			return []byte(fmt.Sprintf("MainPID=%d\nActiveState=active\nSubState=running", process.PID)), nil
		case "cat":
			return testProcessStat(process), nil
		case "loom":
			healthCalls++
			return testTargetHealth(process, 36), nil
		default:
			return readinessFixtureRunner(ctx, name, args...)
		}
	}
	evidence, err := runTargetReadinessUntilReady(context.Background(), runner, testReadinessTarget(), time.Second, time.Millisecond)
	if err != nil || observations != 4 || healthCalls != 4 || evidence.Process.PID != 43 {
		t.Fatalf("evidence=%+v observations=%d health=%d error=%v", evidence, observations, healthCalls, err)
	}
}

func TestReadinessSecondHealthCannotInheritMissingIdentity(t *testing.T) {
	checks := 0
	_, err := runTargetReadinessUntilReady(context.Background(), func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if name == "loom" {
			checks++
			if checks == 2 {
				return []byte(`{"ok":true,"data":{"service":"loomd","status":"ok"}}`), nil
			}
		}
		return readinessFixtureRunner(ctx, name, args...)
	}, testReadinessTarget(), time.Second, time.Millisecond)
	if err == nil || checks != 2 {
		t.Fatalf("checks=%d error=%v", checks, err)
	}
}

func TestReadinessDeadlineCancellationAndInspectFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := runTargetReadinessUntilReady(ctx, readinessFixtureRunner, testReadinessTarget(), time.Second, time.Millisecond)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error=%v", err)
	}
	_, err = runTargetReadinessUntilReady(context.Background(), func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if name == "loom" {
			return []byte(`{"error":{"code":"transport.unavailable"}}`), errors.New("startup")
		}
		return readinessFixtureRunner(ctx, name, args...)
	}, testReadinessTarget(), 5*time.Millisecond, time.Millisecond)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error=%v", err)
	}
	_, err = runTargetReadinessUntilReady(context.Background(), func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if name == "cat" {
			return nil, errors.New("permission denied")
		}
		return readinessFixtureRunner(ctx, name, args...)
	}, testReadinessTarget(), time.Second, time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("error=%v", err)
	}
}
