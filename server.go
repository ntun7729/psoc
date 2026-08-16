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

//go:embed dashboard.html results.html
var dashboardFS embed.FS

const (
	proxyMeasurementConcurrency = 5
	maxDownloadMB               = 100
)

type App struct {
	cfg      Config
	store    *Store
	apiToken string

	scanMu        sync.Mutex
	scanning      bool
	scanCancel    context.CancelFunc
	started       time.Time
	lastErr       string
	scanCompleted int
	scanTotal     int

	measuring         bool
	measureCancel     context.CancelFunc
	measureStarted    time.Time
	measureLastErr    string
	measureCompleted  int
	measureTotal      int
	measureDownloadMB int

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
	mux.HandleFunc("GET /results", a.handleResults)
	mux.HandleFunc("GET /healthz", a.handleHealth)
	mux.HandleFunc("GET /api/stats", a.handleStats)
	mux.HandleFunc("GET /api/proxies", a.handleProxies)
	mux.HandleFunc("GET /api/status", a.handleStatus)
	mux.HandleFunc("GET /api/sources", a.handleSources)
	mux.HandleFunc("POST /api/scan", a.withAuth(a.handleScan))
	mux.HandleFunc("POST /api/scan/stop", a.withAuth(a.handleStopScan))
	mux.HandleFunc("POST /api/measure", a.withAuth(a.handleMeasure))
	mux.HandleFunc("POST /api/measure/stop", a.withAuth(a.handleStopMeasure))
	mux.HandleFunc("POST /api/sources/refresh", a.withAuth(a.handleSourceRefresh))
	return securityHeaders(mux)
}

func (a *App) handleDashboard(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	a.serveEmbeddedHTML(w, "dashboard.html")
}

func (a *App) handleResults(w http.ResponseWriter, r *http.Request) {
	a.serveEmbeddedHTML(w, "results.html")
}

func (a *App) serveEmbeddedHTML(w http.ResponseWriter, name string) {
	b, err := dashboardFS.ReadFile(name)
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

	scanPercent := 0
	if a.scanTotal > 0 {
		scanPercent = a.scanCompleted * 100 / a.scanTotal
	}
	measurePercent := 0
	if a.measureTotal > 0 {
		measurePercent = a.measureCompleted * 100 / a.measureTotal
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"scanning":           a.scanning,
		"started":            a.started,
		"last_error":         a.lastErr,
		"completed":          a.scanCompleted,
		"total":              a.scanTotal,
		"percent":            scanPercent,
		"measuring":          a.measuring,
		"measure_started":    a.measureStarted,
		"last_measure_error": a.measureLastErr,
		"measure_completed":  a.measureCompleted,
		"measure_total":      a.measureTotal,
		"measure_percent":    measurePercent,
		"download_mb":        a.measureDownloadMB,
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

	maxRuntime := time.Duration(len(targets)+1) * cfg.Timeout
	if maxRuntime < 30*time.Second {
		maxRuntime = 30 * time.Second
	}
	if maxRuntime > 30*time.Minute {
		maxRuntime = 30 * time.Minute
	}
	ctx, cancel := context.WithTimeout(context.Background(), maxRuntime)
	if !a.beginScan(scanJobCount(targets, cfg), cancel) {
		cancel()
		http.Error(w, "proxy checking or downspeed measurement already running", http.StatusConflict)
		return
	}

	go func() {
		defer cancel()
		_, err := a.startScanWithConfig(ctx, targets, cfg)
		a.finishScan(err)
	}()

	writeJSON(w, http.StatusAccepted, map[string]any{
		"accepted": true,
		"targets":  len(targets),
		"checks":   scanJobCount(targets, cfg),
	})
}

func (a *App) handleStopScan(w http.ResponseWriter, r *http.Request) {
	if !a.stopScan() {
		http.Error(w, "no proxy check is running", http.StatusConflict)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"accepted": true, "stopping": true})
}

type measureRequest struct {
	DownloadMB int `json:"download_mb"`
}

func (a *App) handleMeasure(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	defer r.Body.Close()
	var req measureRequest
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if req.DownloadMB == 0 {
		req.DownloadMB = 1
	}
	if req.DownloadMB < 1 || req.DownloadMB > maxDownloadMB {
		http.Error(w, fmt.Sprintf("download_mb must be between 1 and %d", maxDownloadMB), http.StatusBadRequest)
		return
	}

	proxies := a.store.All()
	if len(proxies) == 0 {
		http.Error(w, "no alive proxies to measure", http.StatusBadRequest)
		return
	}
	scanner, err := NewScanner(a.cfg)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Hour)
	if !a.beginMeasure(len(proxies), req.DownloadMB, cancel) {
		cancel()
		http.Error(w, "proxy checking or downspeed measurement already running", http.StatusConflict)
		return
	}

	go func() {
		defer cancel()
		err := a.enrichAliveProxies(ctx, scanner, proxies, speedSampleBytesForMB(req.DownloadMB))
		if persistErr := a.store.Save(); persistErr != nil && err == nil {
			err = persistErr
		}
		a.finishMeasure(err)
	}()

	writeJSON(w, http.StatusAccepted, map[string]any{
		"accepted":    true,
		"proxies":     len(proxies),
		"download_mb": req.DownloadMB,
		"concurrency": proxyMeasurementConcurrency,
	})
}

func (a *App) handleStopMeasure(w http.ResponseWriter, r *http.Request) {
	if !a.stopMeasure() {
		http.Error(w, "no downspeed measurement is running", http.StatusConflict)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"accepted": true, "stopping": true})
}

func (a *App) handleSourceRefresh(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	if !a.beginScan(0, cancel) {
		cancel()
		http.Error(w, "proxy checking or downspeed measurement already running", http.StatusConflict)
		return
	}

	go func() {
		defer cancel()
		targets, statuses := FetchPublicProxySources(ctx, a.cfg.MaxTargets)
		a.sourceMu.Lock()
		a.sourceStatuses = statuses
		a.sourceMu.Unlock()

		var err error
		if ctx.Err() != nil {
			err = ctx.Err()
		} else if len(targets) == 0 {
			err = errors.New("public sources returned no valid targets")
		} else {
			a.setScanTotal(scanJobCount(targets, a.cfg))
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

func (a *App) beginScan(total int, cancel context.CancelFunc) bool {
	a.scanMu.Lock()
	defer a.scanMu.Unlock()
	if a.scanning || a.measuring {
		return false
	}
	a.scanning = true
	a.scanCancel = cancel
	a.started = time.Now().UTC()
	a.lastErr = ""
	a.scanCompleted = 0
	a.scanTotal = total
	return true
}

func (a *App) stopScan() bool {
	a.scanMu.Lock()
	defer a.scanMu.Unlock()
	if !a.scanning || a.scanCancel == nil {
		return false
	}
	a.scanCancel()
	return true
}

func (a *App) setScanTotal(total int) {
	a.scanMu.Lock()
	a.scanTotal = total
	a.scanMu.Unlock()
}

func (a *App) incrementScanProgress() {
	a.scanMu.Lock()
	a.scanCompleted++
	a.scanMu.Unlock()
}

func (a *App) finishScan(err error) {
	a.scanMu.Lock()
	defer a.scanMu.Unlock()
	a.scanning = false
	a.scanCancel = nil
	if err != nil && !errors.Is(err, context.Canceled) {
		a.lastErr = err.Error()
		log.Printf("scan: %v", err)
	}
}

func (a *App) beginMeasure(total, downloadMB int, cancel context.CancelFunc) bool {
	a.scanMu.Lock()
	defer a.scanMu.Unlock()
	if a.scanning || a.measuring {
		return false
	}
	a.measuring = true
	a.measureCancel = cancel
	a.measureStarted = time.Now().UTC()
	a.measureLastErr = ""
	a.measureCompleted = 0
	a.measureTotal = total
	a.measureDownloadMB = downloadMB
	return true
}

func (a *App) stopMeasure() bool {
	a.scanMu.Lock()
	defer a.scanMu.Unlock()
	if !a.measuring || a.measureCancel == nil {
		return false
	}
	a.measureCancel()
	return true
}

func (a *App) incrementMeasureProgress() {
	a.scanMu.Lock()
	a.measureCompleted++
	a.scanMu.Unlock()
}

func (a *App) finishMeasure(err error) {
	a.scanMu.Lock()
	defer a.scanMu.Unlock()
	a.measuring = false
	a.measureCancel = nil
	if err != nil && !errors.Is(err, context.Canceled) {
		a.measureLastErr = err.Error()
		log.Printf("measure: %v", err)
	}
}

func (a *App) StartScan(ctx context.Context, targets []Target) ([]Result, error) {
	ctx, cancel := context.WithCancel(ctx)
	if !a.beginScan(scanJobCount(targets, a.cfg), cancel) {
		cancel()
		return nil, errors.New("proxy checking or downspeed measurement already running")
	}
	defer cancel()
	results, err := a.startScanWithConfig(ctx, targets, a.cfg)
	a.finishScan(err)
	return results, err
}

func (a *App) startScanWithConfig(ctx context.Context, targets []Target, cfg Config) ([]Result, error) {
	scanner, err := NewScanner(cfg)
	if err != nil {
		return nil, err
	}

	// Verification runs by itself. Alive proxies are stored immediately. Country,
	// network type and downspeed measurement are a separate explicit operation.
	a.store.Reset()
	results, err := scanner.ScanWithProgress(ctx, targets, func(r Result) {
		if r.Alive {
			a.store.UpsertProxy(ProxyResultFrom(r))
		}
		a.incrementScanProgress()
	})

	if persistErr := a.store.Save(); persistErr != nil && err == nil {
		err = persistErr
	}
	return results, err
}

func (a *App) enrichAliveProxies(ctx context.Context, scanner *Scanner, proxies []ProxyResult, sampleBytes int64) error {
	if len(proxies) == 0 {
		return nil
	}

	workers := proxyMeasurementConcurrency
	if len(proxies) < workers {
		workers = len(proxies)
	}
	jobs := make(chan ProxyResult)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for pr := range jobs {
				country, networkType, downMbps := scanner.MeasureProxyProfileBytes(ctx, pr.Target, pr.Protocol, sampleBytes)
				if country != "" {
					pr.Country = country
				}
				if networkType != "" {
					pr.NetworkType = networkType
				}
				pr.DownMbps = downMbps
				a.store.UpsertProxy(pr)
				a.incrementMeasureProgress()
			}
		}()
	}

sendLoop:
	for _, pr := range proxies {
		select {
		case jobs <- pr:
		case <-ctx.Done():
			break sendLoop
		}
	}
	close(jobs)
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return err
	}
	return nil
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
