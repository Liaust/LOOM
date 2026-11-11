package health

import (
	"encoding/json"
	"runtime"
	"strings"
	"testing"

	"loom.local/loom/internal/config"
	"loom.local/loom/internal/version"
)

func TestInstanceIdentity(t *testing.T) {
	stat := "123 (daemon ) name) S " + strings.Repeat("0 ", 18) + "456 0"
	got := parseInstance([]byte(stat), 123)
	if got == nil || got.PID != 123 || got.StartTicks != "456" {
		t.Fatalf("identity = %#v", got)
	}
	for _, invalid := range []string{"", "123 (daemon) S", strings.Replace(stat, "456", "0", 1), strings.Replace(stat, "456", "bad", 1)} {
		if parseInstance([]byte(invalid), 123) != nil {
			t.Fatalf("accepted invalid stat %q", invalid)
		}
	}
	if parseInstance([]byte(stat), 124) != nil {
		t.Fatal("accepted another process")
	}
	s := NewService(config.Config{}, version.Info{})
	if runtime.GOOS == "linux" && s.instance == nil {
		t.Fatal("self-observed Linux identity missing")
	}
	encoded, err := json.Marshal(Report{Instance: s.instance})
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "linux" && strings.Contains(string(encoded), "instance") {
		t.Fatal("invented non-Linux identity")
	}
}
