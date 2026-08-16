package main

import (
	"context"
	"crypto/subtle"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"
)

//go:embed dashboard.html
var dashboardFS embed.FS

type App struct {
	cfg      Config
	store    *Store
	apiToken string

	scanMu   sync.Mutex
	scanning bool
	started  time.Time
	lastErr  string

	sourceMu       sync.RWMutex
	sourceStatuses []SourceStatus
}

func NewApp(cfg Config, store *Store, apiToken string) *App {
	return &App{
		cfg:            cfg,
		store:          store,
		apiToken:       apiToken,
		sourceStatuses: InitialSourceStatuses(),
	}
}

func (a *App) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", a.handleDashboard)
	mux.HandleFunc("GET /healthz", a.handleHealth)
	mux.HandleFunc("GET /api/stats", a.handleStats)
	mux.HandleFunc("GET /api/proxies", a.handleProxies)
	mux.HandleFunc("GET /api/status", a.handleStatus)
	mux.HandleFunc("GET /api/sources", a.handleSources)
	mux.HandleFunc("POST /api/scan", a.withAuth(a.handleScan))
	mux.HandleFunc("POST /api/sources/refresh", a.withAuth(a.handleSourceRefresh))
	return securityHeaders(mux)
}

func (a *App) handleDashboard(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	b, err := dashboardFS.ReadFile("dashboard.html")
	if err != nil {
		http.Error(w, "dashboard unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(b)
}

func (a *App) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "version": version})
}

func (a *App) handleStats(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, BuildStats(a.store.All()))
}

func (a *App) handleProxies(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, a.store.All())
}

func (a *App) handleStatus(w http.ResponseWriter, r *http.Request) {
	a.scanMu.Lock()
	defer a.scanMu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{
		"scanning":   a.scanning,
		"started":    a.started,
		"last_error": a.lastErr,
	})
}

func (a *App) handleSources(w http.ResponseWriter, r *http.Request) {
	a.sourceMu.RLock()
	statuses := append([]SourceStatus(nil), a.sourceStatuses...)
	a.sourceMu.RUnlock()
	writeJSON(w, http.StatusOK, statuses)
}

type scanRequest struct {
	Targets   string   `json:"targets"`
	Protocols []string `json:"protocols,omitempty"`
}

func (a *App) handleScan(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 2<<20)
	defer r.Body.Close()
	var req scanRequest
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	targets := ParseTargetLines(req.Targets)
	if len(targets) == 0 {
		http.Error(w, "no valid targets", http.StatusBadRequest)
		return
	}
	if len(targets) > a.cfg.MaxTargets {
		http.Error(w, fmt.Sprintf("too many targets: max %d", a.cfg.MaxTargets), http.StatusBadRequest)
		return
	}

	cfg := a.cfg
	if len(req.Protocols) > 0 {
		cfg.Protocols = req.Protocols
		var err error
		cfg, err = normalizeConfig(cfg)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	}

	if !a.beginScan() {
		http.Error(w, "scan already running", http.StatusConflict)
		return
	}

	// Detach from the request while retaining a bounded total runtime.
	maxRuntime := time.Duration(len(targets)+1) * cfg.Timeout
	if maxRuntime < 30*time.Second {
		maxRuntime = 30 * time.Second
	}
	if maxRuntime > 30*time.Minute {
		maxRuntime = 30 * time.Minute
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), maxRuntime)
		defer cancel()
		_, err := a.startScanWithConfig(ctx, targets, cfg)
		a.finishScan(err)
	}()

	writeJSON(w, http.StatusAccepted, map[string]any{"accepted": true, "targets": len(targets)})
}

func (a *App) handleSourceRefresh(w http.ResponseWriter, r *http.Request) {
	if !a.beginScan() {
		http.Error(w, "scan already running", http.StatusConflict)
		return
	}

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
		defer cancel()

		targets, statuses := FetchPublicProxySources(ctx, a.cfg.MaxTargets)
		a.sourceMu.Lock()
		a.sourceStatuses = statuses
		a.sourceMu.Unlock()

		var err error
		if len(targets) == 0 {
			err = errors.New("public sources returned no valid targets")
		} else {
			_, err = a.startScanWithConfig(ctx, targets, a.cfg)
		}
		a.finishScan(err)
	}()

	writeJSON(w, http.StatusAccepted, map[string]any{
		"accepted": true,
		"sources":  len(DefaultPublicSources()),
		"limit":    a.cfg.MaxTargets,
	})
}

func (a *App) beginScan() bool {
	a.scanMu.Lock()
	defer a.scanMu.Unlock()
	if a.scanning {
		return false
	}
	a.scanning = true
	a.started = time.Now().UTC()
	a.lastErr = ""
	return true
}

func (a *App) finishScan(err error) {
	a.scanMu.Lock()
	defer a.scanMu.Unlock()
	a.scanning = false
	if err != nil && !errors.Is(err, context.Canceled) {
		a.lastErr = err.Error()
		log.Printf("scan: %v", err)
	}
}

func (a *App) StartScan(ctx context.Context, targets []Target) ([]Result, error) {
	return a.startScanWithConfig(ctx, targets, a.cfg)
}

func (a *App) startScanWithConfig(ctx context.Context, targets []Target, cfg Config) ([]Result, error) {
	scanner, err := NewScanner(cfg)
	if err != nil {
		return nil, err
	}
	results, err := scanner.Scan(ctx, targets)
	if len(results) > 0 {
		if persistErr := a.store.Replace(results); persistErr != nil && err == nil {
			err = persistErr
		}
	}
	return results, err
}

func (a *App) withAuth(next http.HandlerFunc) http.HandlerFunc {
	if a.apiToken == "" {
		return next
	}
	return func(w http.ResponseWriter, r *http.Request) {
		token := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		if token == "" {
			token = r.Header.Get("X-PSOC-Token")
		}
		if subtle.ConstantTimeCompare([]byte(token), []byte(a.apiToken)) != 1 {
			w.Header().Set("WWW-Authenticate", "Bearer")
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self' 'unsafe-inline'; script-src 'self' 'unsafe-inline'; connect-src 'self'; img-src 'self' data:")
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
