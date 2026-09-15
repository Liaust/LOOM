package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

func main() {
	if len(os.Args) == 2 && !strings.HasPrefix(os.Args[1], "-") {
		if err := runApplicationFixture(os.Args[1]); err != nil {
			log.Fatal("typed application fixture failed")
		}
		return
	}
	listen := flag.String("listen", "127.0.0.1:0", "loopback address for the test fixture")
	pidFile := flag.String("pid-file", "", "owned acceptance PID file")
	logFile := flag.String("log-file", "", "owned acceptance log file")
	flag.Parse()

	if *pidFile == "" || *logFile == "" {
		log.Fatal("--pid-file and --log-file are required")
	}
	if err := os.WriteFile(*pidFile, []byte(fmt.Sprintf("%d\n", os.Getpid())), 0o600); err != nil {
		log.Fatal(err)
	}
	file, err := os.OpenFile(*logFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		log.Fatal(err)
	}
	defer file.Close()
	logger := log.New(file, "fixture ", log.LstdFlags|log.LUTC)
	logger.Printf("ready pid=%d token=synthetic-acceptance-token path:/private/acceptance-only", os.Getpid())

	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok", "pid": os.Getpid(), "checked_at": time.Now().UTC()})
	})
	mux.HandleFunc("/logs", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = fmt.Fprintln(w, "bounded fixture log")
	})
	server := &http.Server{Addr: *listen, Handler: mux, ReadHeaderTimeout: 2 * time.Second}
	log.Printf("loom service fixture listening on %s", *listen)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}

// The two fixture schemas demonstrate artifact-owned configuration. Neither
// root nor a project-specific renderer executes application config code.
func runApplicationFixture(path string) error {
	raw, e := os.ReadFile(path)
	if e != nil || len(raw) > 65536 {
		return fmt.Errorf("configuration unavailable")
	}
	var c struct {
		Schema string          `json:"schema"`
		Values json.RawMessage `json:"values"`
	}
	if e = decodeFixtureJSON(raw, &c); e != nil {
		return e
	}
	var listen string
	var large int64
	switch c.Schema {
	case "fixture.echo":
		var values struct {
			Listen string `json:"listen"`
			Large  int64  `json:"large"`
		}
		if e = decodeFixtureJSON(c.Values, &values); e != nil {
			return e
		}
		listen = values.Listen
		large = values.Large
	case "fixture.indexer":
		type dataRef struct {
			Path string `json:"data_path"`
		}
		type credentialRef struct {
			File string `json:"credential_file"`
		}
		var values struct {
			Listen string        `json:"listen"`
			Files  dataRef       `json:"files"`
			Index  dataRef       `json:"index"`
			First  credentialRef `json:"first"`
			Second credentialRef `json:"second"`
		}
		if e = decodeFixtureJSON(c.Values, &values); e != nil {
			return e
		}
		listen = values.Listen
		for _, ref := range []credentialRef{values.First, values.Second} {
			f, e := os.Open(ref.File)
			if e != nil {
				return fmt.Errorf("credential unavailable")
			}
			b, e := io.ReadAll(io.LimitReader(f, 65537))
			f.Close()
			if e != nil || len(b) == 0 || len(b) > 65536 {
				return fmt.Errorf("credential invalid")
			}
		}
		for _, ref := range []dataRef{values.Files, values.Index} {
			if !filepath.IsAbs(ref.Path) {
				return fmt.Errorf("invalid data reference")
			}
			sentinel := filepath.Join(ref.Path, "fixture-sentinel")
			f, e := os.OpenFile(sentinel, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
			if os.IsExist(e) {
				continue
			}
			if e != nil {
				return fmt.Errorf("data unavailable")
			}
			_, e = f.Write([]byte("retained application fixture data\n"))
			if e == nil {
				e = f.Sync()
			}
			f.Close()
			if e != nil {
				return e
			}
		}
	default:
		return fmt.Errorf("unknown artifact schema")
	}
	host, port, e := net.SplitHostPort(listen)
	if e != nil || (host != "127.0.0.1" && host != "::1") {
		return fmt.Errorf("loopback listener required")
	}
	n, e := strconv.Atoi(port)
	if e != nil || n < 1024 || n > 65535 {
		return fmt.Errorf("invalid listener")
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(struct {
			Schema string `json:"schema"`
			Large  int64  `json:"large"`
		}{c.Schema, large})
	})
	return (&http.Server{Addr: listen, Handler: mux, ReadHeaderTimeout: 2 * time.Second}).ListenAndServe()
}
func decodeFixtureJSON(raw []byte, out any) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if e := d.Decode(out); e != nil {
		return fmt.Errorf("invalid fixture config")
	}
	var tail any
	if d.Decode(&tail) != io.EOF {
		return fmt.Errorf("invalid fixture config")
	}
	return nil
}
