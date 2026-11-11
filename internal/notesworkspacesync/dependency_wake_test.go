package notesworkspacesync

import (
	"testing"
	"time"
)

func TestDependencyCacheIgnoresUnrelatedWorkAndTracksMissingKeys(t *testing.T) {
	var c layoutDependencyCache
	now := time.Now()
	c.record("hold-a", []string{"record:binding:missing", "source:a", layoutPathKey("notes", "a.md")}, "a", c.version())
	c.record("hold-b", []string{"source:b"}, "b", c.version())
	for i := 0; i < 100; i++ {
		if c.invalidate("record:binding:unrelated", false, false) {
			t.Fatal("unrelated record scheduled a held graph")
		}
		if needed, _ := c.needs("hold-a", "a", now, now.Add(time.Hour)); needed {
			t.Fatal("unrelated work caused held metadata reads")
		}
	}
	if !c.invalidate("record:binding:missing", false, false) {
		t.Fatal("missing dependency insertion did not wake exact hold")
	}
	if needed, _ := c.needs("hold-b", "b", now, now.Add(time.Hour)); needed {
		t.Fatal("exact dependency woke unrelated hold")
	}
	c.record("hold-a", c.keys("hold-a"), "a", c.version())
	if !c.invalidate(layoutPathKey("notes", ""), true, true) {
		t.Fatal("root prefix hint did not reach held source path")
	}
	if needed, force := c.needs("hold-a", "a", now, now.Add(time.Hour)); !needed || !force {
		t.Fatal("source-content hint lost its exact read requirement")
	}
}

func TestDependencyChangeDuringCaptureIsNotAcknowledgedAway(t *testing.T) {
	var c layoutDependencyCache
	version := c.version()
	c.invalidate("source:a", false, false)
	c.record("hold", []string{"source:a"}, "same", version)
	if needed, _ := c.needs("hold", "same", time.Now(), time.Now().Add(time.Hour)); !needed {
		t.Fatal("concurrent committed change was swallowed by capture")
	}
}

func TestUnrelatedChangeDuringCaptureDoesNotCreateRetryLoop(t *testing.T) {
	var c layoutDependencyCache
	version := c.version()
	c.invalidate("source:unrelated", false, false)
	c.record("hold", []string{"source:a"}, "same", version)
	if needed, _ := c.needs("hold", "same", time.Now(), time.Now().Add(time.Hour)); needed {
		t.Fatal("unrelated concurrent activity invalidated a stable captured graph")
	}
}

func TestRetiredScopeCannotKeepWakingCurrentRuntime(t *testing.T) {
	var c layoutDependencyCache
	old := Scope{Collection: "old"}
	current := Scope{Collection: "current"}
	c.record(layoutDependencyKey(old, "held"), []string{"source:old"}, "a", c.version())
	c.record(layoutDependencyKey(current, "held"), []string{"source:current"}, "b", c.version())
	c.retainScopes([]Scope{current})
	if c.invalidate("source:old", false, false) || !c.invalidate("source:current", false, false) {
		t.Fatal("retired cache continued to schedule work or current dependency was lost")
	}
}
