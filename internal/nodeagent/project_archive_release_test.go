package nodeagent

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"loom.local/loom/internal/projectquiescence"
)

func TestProjectArchiveReleaseRetryAndLateQuiesce(t *testing.T) {
	for _, interrupted := range []bool{false, true} {
		t.Run(map[bool]string{false: "complete", true: "partial"}[interrupted], func(t *testing.T) {
			e := newProjectArchiveQuiescenceEnvironment(t)
			ctx := context.Background()
			if _, err := e.service.Quiesce(ctx, e.request); err != nil {
				t.Fatal(err)
			}
			r := e.request
			r.Release = &projectquiescence.ReleaseBinding{OperationID: "restore-fixture", PlanDigest: "sha256:" + strings.Repeat("b", 64)}
			if err := projectquiescence.SealRequest(&r); err != nil {
				t.Fatal(err)
			}
			if interrupted {
				e.service.FailureHook = func(stage string, _ *projectquiescence.Target) error {
					if stage == "after_release" {
						return errors.New("interrupted")
					}
					return nil
				}
				if _, err := e.service.Release(ctx, r); err == nil {
					t.Fatal("injection ignored")
				}
				e.service.FailureHook = nil
			}
			out, err := e.service.Release(ctx, r)
			if err != nil {
				t.Fatal(err)
			}
			for _, v := range out.Evidence {
				if v.FenceState != "released" || v.State != "stopped" {
					t.Fatalf("bad evidence: %+v", v)
				}
			}
			instance, err := e.runtime.LoadInstance(e.watched.WorkerKey)
			if err != nil || instance.Enabled {
				t.Fatal("release started watcher", err)
			}
			instance.Enabled = true
			if err := e.runtime.SaveInstance(instance); err != nil {
				t.Fatal("normal activation remains fenced", err)
			}
			again, err := e.service.Release(ctx, r)
			if err != nil || !reflect.DeepEqual(out, again) {
				t.Fatal("terminal replay changed", err)
			}
			if _, err := e.service.Quiesce(ctx, e.request); err == nil {
				t.Fatal("late archive re-fenced")
			}
			r.Release.OperationID = "different-restore"
			_ = projectquiescence.SealRequest(&r)
			if _, err := e.service.Release(ctx, r); err == nil {
				t.Fatal("different restore adopted release")
			}
		})
	}
}

func TestProjectArchiveReleaseRefusesUnownedFence(t *testing.T) {
	e := newProjectArchiveQuiescenceEnvironment(t)
	ctx := context.Background()
	if _, err := e.service.Quiesce(ctx, e.request); err != nil {
		t.Fatal(err)
	}
	r := e.request
	r.Release = &projectquiescence.ReleaseBinding{OperationID: "restore-fixture", PlanDigest: "sha256:" + strings.Repeat("b", 64)}
	_ = projectquiescence.SealRequest(&r)
	key, _ := projectquiescence.TargetLockIdentity(e.watched)
	key = strings.TrimPrefix(key, e.watched.Kind+":")
	raw, _, err := e.runtime.ReadProjectArchiveFence(e.watched.Kind, key)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.runtime.RemoveProjectArchiveFence(e.watched.Kind, key, raw); err != nil {
		t.Fatal(err)
	}
	other := e.request
	other.OperationID += "-new"
	_ = projectquiescence.SealRequest(&other)
	fence, err := projectquiescence.NewFence(other, e.watched, e.now)
	if err != nil {
		t.Fatal(err)
	}
	replacement, _ := projectquiescence.CanonicalFenceBytes(fence)
	if err := e.runtime.PublishProjectArchiveFence(e.watched.Kind, key, replacement); err != nil {
		t.Fatal(err)
	}
	if _, err := e.service.Release(ctx, r); err == nil {
		t.Fatal("removed different archive fence")
	}
	got, _, err := e.runtime.ReadProjectArchiveFence(e.watched.Kind, key)
	if err != nil || string(got) != string(replacement) {
		t.Fatal("changed different fence", err)
	}
}
