// Package projectdx contains disposable acceptance glue, never runtime owners.
package projectdx

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

//go:embed cold_cases.json
var Corpus []byte

type Fixture struct {
	Root, Evidence, Case, Socket, Participant, CLI, Config string
	mu                                                     sync.Mutex
}

func Open(t *testing.T, c string) *Fixture {
	t.Helper()
	root := os.Getenv("LOOM_DX_ROOT")
	if root == "" {
		t.Skip("requires the owned project DX smoke")
	}
	if filepath.Dir(root) != "/tmp" || !strings.HasPrefix(root, "/tmp/loom-dx-") {
		t.Fatal("DX requires its own direct /tmp root")
	}
	u, e := url.Parse(os.Getenv("LOOM_TEST_DB_URL"))
	if e != nil || u.Host != "" || u.Path != "/postgres" || u.User == nil || u.User.Username() != "postgres" || u.Query().Get("host") != filepath.Join(root, "socket") {
		t.Fatal("DX requires its owned socket-only admin URL")
	}
	evidence := os.Getenv("LOOM_DX_EVIDENCE")
	if !filepath.IsAbs(evidence) || !strings.Contains(evidence, "/.loom-acceptance/") {
		t.Fatal("DX evidence must be under .loom-acceptance")
	}
	f := &Fixture{Root: root, Evidence: filepath.Join(evidence, c), Case: c, Socket: filepath.Join(root, c+".sock"), Participant: filepath.Join(root, "participants", c), CLI: os.Getenv("LOOM_DX_CLI"), Config: filepath.Join(root, c+".conf")}
	for _, p := range []string{f.Evidence, f.Participant} {
		if e := os.MkdirAll(p, 0700); e != nil {
			t.Fatal(e)
		}
	}
	if e := os.WriteFile(f.Config, []byte("LOOM_NODE_ID=main\nLOOM_BOX_PATH="+f.Participant+"\n"), 0600); e != nil {
		t.Fatal(e)
	}
	return f
}
func (f *Fixture) Write(t *testing.T, name string, v any) {
	t.Helper()
	b, e := json.MarshalIndent(v, "", "  ")
	if e == nil {
		e = os.WriteFile(filepath.Join(f.Evidence, name), append(b, '\n'), 0600)
	}
	if e != nil {
		t.Fatal(e)
	}
}
func (f *Fixture) Log(name string, v any) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	b, e := json.Marshal(v)
	if e != nil {
		return e
	}
	file, e := os.OpenFile(filepath.Join(f.Evidence, name), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if e != nil {
		return e
	}
	defer file.Close()
	_, e = file.Write(append(b, '\n'))
	return e
}
func (f *Fixture) Command(t *testing.T, args ...string) ([]byte, error) {
	t.Helper()
	argv := append([]string{"--config", f.Config, "--socket", f.Socket, "--json"}, args...)
	cmd := exec.CommandContext(t.Context(), f.CLI, argv...)
	cmd.Dir = f.Participant
	cmd.Env = []string{"PATH=/opt/homebrew/bin:/usr/bin:/bin", "HOME=" + f.Participant, "XDG_CONFIG_HOME=" + f.Participant}
	start := time.Now()
	body, e := cmd.CombinedOutput()
	code := 0
	if e != nil {
		code = -1
		if x, ok := e.(*exec.ExitError); ok {
			code = x.ExitCode()
		}
	}
	if err := f.Log("fixture-cli.jsonl", map[string]any{"argv": argv, "output": string(body), "exit_code": code, "started_at": start.UTC(), "elapsed_ms": time.Since(start).Milliseconds()}); err != nil {
		t.Fatal(err)
	}
	return body, e
}
func (f *Fixture) MustCommand(t *testing.T, args ...string) []byte {
	t.Helper()
	b, e := f.Command(t, args...)
	if e != nil {
		t.Fatalf("CLI %v: %v\n%s", args, e, b)
	}
	return b
}

// The hook observes the real response before it is delivered. It must never
// replace response text. C uses it to wait for the evaluator's committed barrier.
func (f *Fixture) Serve(t *testing.T, h http.Handler, hook func(*http.Request, []byte)) {
	t.Helper()
	l, e := net.Listen("unix", f.Socket)
	if e != nil {
		t.Fatal(e)
	}
	server := &http.Server{ReadHeaderTimeout: 5 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestBody, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "fixture request read failed", 400)
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(requestBody))
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, r)
		b := rr.Body.Bytes()
		if e := f.Log("api.jsonl", map[string]any{"method": r.Method, "request_body": string(requestBody), "path": r.URL.String(), "correlation_id": r.Header.Get("X-Correlation-ID"), "status": rr.Code, "body": string(b), "at": time.Now().UTC()}); e != nil {
			http.Error(w, e.Error(), 500)
			return
		}
		if hook != nil {
			hook(r, b)
		}
		for k, v := range rr.Header() {
			w.Header()[k] = v
		}
		w.WriteHeader(rr.Code)
		_, _ = w.Write(b)
	})}
	done := make(chan error, 1)
	go func() { done <- server.Serve(l) }()
	t.Cleanup(func() {
		_ = server.Close()
		if e := <-done; e != http.ErrServerClosed {
			t.Error(e)
		}
		_ = os.Remove(f.Socket)
	})
}
func (f *Fixture) Hold(t *testing.T, controls map[string]func()) {
	t.Helper()
	f.Write(t, "ready.json", map[string]any{"socket": f.Socket, "participant": f.Participant, "case": f.Case})
	if os.Getenv("LOOM_DX_SERVE") != "1" {
		return
	}
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-t.Context().Done():
			return
		case <-ticker.C:
			if _, e := os.Stat(filepath.Join(f.Root, f.Case+".stop")); e == nil {
				return
			}
			for name, action := range controls {
				p := filepath.Join(f.Root, f.Case+"."+name)
				if _, e := os.Stat(p); e == nil {
					if e = os.Remove(p); e != nil {
						t.Fatal(e)
					}
					action()
					f.Write(t, name+".done.json", map[string]bool{"done": true})
				}
			}
		}
	}
}
func Snapshot(t *testing.T, db *sql.DB) string {
	t.Helper()
	rows, e := db.QueryContext(context.Background(), `SELECT schemaname,tablename FROM pg_tables WHERE schemaname NOT IN ('pg_catalog','information_schema') ORDER BY 1,2`)
	if e != nil {
		t.Fatal(e)
	}
	var tables [][2]string
	for rows.Next() {
		var x [2]string
		if e = rows.Scan(&x[0], &x[1]); e != nil {
			t.Fatal(e)
		}
		tables = append(tables, x)
	}
	if e = rows.Err(); e != nil {
		t.Fatal(e)
	}
	rows.Close()
	h := sha256.New()
	for _, x := range tables {
		var raw string
		q := fmt.Sprintf(`SELECT COALESCE(jsonb_agg(to_jsonb(r) ORDER BY to_jsonb(r)::text),'[]'::jsonb)::text FROM %q.%q r`, x[0], x[1])
		if e = db.QueryRow(q).Scan(&raw); e != nil {
			t.Fatal(e)
		}
		fmt.Fprintf(h, "%s.%s:%s\n", x[0], x[1], raw)
	}
	return fmt.Sprintf("%x", h.Sum(nil))
}
func Source(t *testing.T, c, key string) string {
	t.Helper()
	var x struct {
		Cases map[string]struct {
			Sources map[string]json.RawMessage `json:"sources"`
		} `json:"cases"`
	}
	if e := json.Unmarshal(Corpus, &x); e != nil {
		t.Fatal(e)
	}
	var s string
	if e := json.Unmarshal(x.Cases[c].Sources[key], &s); e != nil {
		t.Fatal(e)
	}
	return s
}
