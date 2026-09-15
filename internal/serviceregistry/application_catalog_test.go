package serviceregistry

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestApplicationDescriptorClosedTrustBoundary(t *testing.T) {
	d := applicationTestDescriptor()
	if e := d.Validate(); e != nil {
		t.Fatal(e)
	}
	for name, change := range map[string]func(*ApplicationArtifactDescriptor){"store_escape": func(d *ApplicationArtifactDescriptor) { d.StoreRoot = "/tmp/evil" }, "launcher": func(d *ApplicationArtifactDescriptor) { d.Launcher = "/bin/sh" }, "closure": func(d *ApplicationArtifactDescriptor) { d.Closure = nil }, "schema": func(d *ApplicationArtifactDescriptor) {
		d.Configuration.Parameters["large"] = ApplicationParameterSpec{Type: "shell"}
	}, "source": func(d *ApplicationArtifactDescriptor) { d.SourceDigest = "unbound" }} {
		t.Run(name, func(t *testing.T) {
			v := applicationTestDescriptor()
			change(&v)
			if v.Validate() == nil {
				t.Fatal("invalid descriptor accepted")
			}
		})
	}
	_, _, q, _ := applicationTestRuntime(t)
	raw, _ := json.Marshal(q)
	for _, in := range [][]byte{[]byte(strings.Replace(string(raw), `"operation":"apply"`, `"operation":"apply","operation":"retire"`, 1)), []byte(strings.TrimSuffix(string(raw), "}") + `,"reviewed":true}`), append(raw, []byte(" {}")...)} {
		if _, e := DecodeApplicationRuntimeRequest(in); e == nil {
			t.Fatal("open or ambiguous request accepted")
		}
	}
}
