package applicationlaunch

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	pc "loom.local/loom/internal/projectcontracts"
)

type Metadata struct {
	Schema          string                       `json:"schema"`
	Programs        map[string]string            `json:"programs"`
	Run             pc.ApplicationRun            `json:"run"`
	Actions         map[string]pc.ApplicationRun `json:"actions"`
	PythonRoot      string                       `json:"python_root,omitempty"`
	PythonBootstrap string                       `json:"python_bootstrap,omitempty"`
}

func Decode(raw []byte) (Metadata, error) {
	var m Metadata
	if len(raw) > 512*1024 {
		return m, fmt.Errorf("launch metadata too large")
	}
	if _, err := pc.DecodeDeclarationEvidenceJSON(raw); err != nil {
		return m, err
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&m); err != nil {
		return m, err
	}
	if d.Decode(new(any)) != io.EOF || m.Schema != "application.launch.v1" || len(m.Programs) == 0 || len(m.Programs) > 17 || len(m.Actions) > 16 {
		return m, fmt.Errorf("invalid launch metadata")
	}
	for name, path := range m.Programs {
		if name == "" || strings.ContainsAny(name, "/\\\x00") || !filepath.IsAbs(path) || filepath.Clean(path) != path || strings.ContainsAny(path, "\x00\r\n") {
			return m, fmt.Errorf("invalid launch program")
		}
	}
	return m, nil
}

func Resolve(m Metadata, action, config string) (string, []string, error) {
	if !filepath.IsAbs(config) || filepath.Clean(config) != config || strings.ContainsRune(config, 0) {
		return "", nil, fmt.Errorf("expected absolute resolved-config file")
	}
	r := m.Run
	if action != "" {
		var ok bool
		r, ok = m.Actions[action]
		if !ok {
			return "", nil, fmt.Errorf("unknown application action")
		}
	}
	name := r.Binary
	if r.PythonModule != "" {
		name = "python"
	}
	if name == "" {
		if len(m.Programs) != 1 {
			return "", nil, fmt.Errorf("ambiguous application program")
		}
		for k := range m.Programs {
			name = k
		}
	}
	program, ok := m.Programs[name]
	if !ok || len(r.Args) > 32 {
		return "", nil, fmt.Errorf("invalid application run")
	}
	argv := []string{program}
	if r.PythonModule != "" {
		if !filepath.IsAbs(m.PythonRoot) || !filepath.IsAbs(m.PythonBootstrap) || r.Binary != "" {
			return "", nil, fmt.Errorf("invalid Python run")
		}
		argv = append(argv, "-I", m.PythonBootstrap, m.PythonRoot, r.PythonModule)
	}
	for _, arg := range r.Args {
		if !arg.Valid() {
			return "", nil, fmt.Errorf("invalid application argument")
		}
		if arg.ConfigFile {
			argv = append(argv, config)
		} else {
			argv = append(argv, *arg.Literal)
		}
	}
	return program, argv, nil
}
