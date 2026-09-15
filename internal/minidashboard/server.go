package minidashboard

import (
	"encoding/json"
	"io/fs"
	"net/http"
)

func NewDisplayHandler(snapshot func() ViewSnapshot, diagnostics ...func() PollerDiagnostic) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/status", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(snapshot())
	})
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if len(diagnostics) == 0 || diagnostics[0] == nil {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			_, _ = w.Write([]byte("ok\n"))
			return
		}
		diagnostic := diagnostics[0]()
		status := "ok"
		if diagnostic.SourceState == SourceOffline || diagnostic.SourceState == SourceFailed || diagnostic.CacheState == SourceFailed {
			status = "degraded"
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(struct {
			Status     string           `json:"status"`
			Diagnostic PollerDiagnostic `json:"poller"`
		}{Status: status, Diagnostic: diagnostic})
	})
	assets := WebAssets()
	mux.Handle("/", http.FileServer(http.FS(assets)))
	return securityHeaders(mux, assets)
}

func securityHeaders(next http.Handler, _ fs.FS) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", "default-src 'self'; connect-src 'self'; font-src 'self'; img-src 'self'; style-src 'self'; script-src 'self'")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		next.ServeHTTP(w, r)
	})
}
