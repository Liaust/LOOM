package applicationlaunch

import (
	"slices"
	"testing"

	pc "loom.local/loom/internal/projectcontracts"
)

func TestTypedArgumentsRemainLiteralAndActionsAreClosed(t *testing.T) {
	m := Metadata{Schema: "application.launch.v1", Programs: map[string]string{"app": "/nix/store/example/bin/app"}, Run: pc.ApplicationRun{Binary: "app", Args: []pc.ApplicationArgument{pc.LiteralApplicationArgument("a b;$(no)"), {ConfigFile: true}}}, Actions: map[string]pc.ApplicationRun{"status": {Binary: "app", Args: []pc.ApplicationArgument{pc.LiteralApplicationArgument("--status")}}}}
	program, args, err := Resolve(m, "", "/run/config.json")
	if err != nil || !slices.Equal(args, []string{program, "a b;$(no)", "/run/config.json"}) {
		t.Fatalf("argv: %v %v", args, err)
	}
	_, args, err = Resolve(m, "status", "/run/config.json")
	if err != nil || !slices.Equal(args, []string{program, "--status"}) {
		t.Fatalf("action: %v %v", args, err)
	}
	if _, _, err := Resolve(m, "undeclared", "/run/config.json"); err == nil {
		t.Fatal("undeclared action")
	}
	if _, err := Decode([]byte(`{"schema":"application.launch.v1","schema":"other"}`)); err == nil {
		t.Fatal("duplicate metadata")
	}
}

func TestExplicitPythonGetsIsolatedModuleArgv(t *testing.T) {
	m := Metadata{Programs: map[string]string{"python": "/nix/store/python/bin/python"}, PythonRoot: "/nix/store/source", PythonBootstrap: "/nix/store/bootstrap", Run: pc.ApplicationRun{PythonModule: "app.main", Args: []pc.ApplicationArgument{{ConfigFile: true}}}}
	_, args, err := Resolve(m, "", "/run/config.json")
	if err != nil || !slices.Equal(args[1:], []string{"-I", m.PythonBootstrap, m.PythonRoot, "app.main", "/run/config.json"}) {
		t.Fatalf("Python argv: %v %v", args, err)
	}
}
