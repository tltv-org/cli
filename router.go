package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"time"
)

// routeSlice collects repeatable --route / -r flag values.
type routeSlice []string

func (r *routeSlice) String() string { return strings.Join(*r, ",") }
func (r *routeSlice) Set(s string) error {
	*r = append(*r, s)
	return nil
}

// routeEntry holds a parsed route with its reverse proxy and health state.
type routeEntry struct {
	hostname string
	backend  string
	proxy    *httputil.ReverseProxy
	healthy  atomic.Bool
}

// cmdRouter implements the "tltv router" subcommand -- a lightweight L7
// reverse proxy with multi-domain SNI routing and built-in ACME TLS.
func cmdRouter(args []string) {
	fs := flag.NewFlagSet("router", flag.ExitOnError)

	// Route flags (repeatable)
	var routes routeSlice
	fs.Var(&routes, "route", "route mapping HOST=BACKEND (repeatable)")
	fs.Var(&routes, "r", "alias for --route")

	// Server flags
	defaultListen := ":443"
	if v := os.Getenv("LISTEN"); v != "" {
		defaultListen = v
	}
	listenAddr := fs.String("listen", defaultListen, "HTTPS listen address")
	fs.StringVar(listenAddr, "l", defaultListen, "alias for --listen")

	defaultHTTPListen := ":80"
	if v := os.Getenv("HTTP_LISTEN"); v != "" {
		defaultHTTPListen = v
	}
	httpListenAddr := fs.String("http-listen", defaultHTTPListen, "HTTP listen address (challenges + redirect)")

	// Health check flags
	healthPath := fs.String("health-check", os.Getenv("HEALTH_CHECK"), "health check path on backends")
	fs.StringVar(healthPath, "H", os.Getenv("HEALTH_CHECK"), "alias for --health-check")

	defaultHealthInterval := "30s"
	if v := os.Getenv("HEALTH_INTERVAL"); v != "" {
		defaultHealthInterval = v
	}
	healthIntervalStr := fs.String("health-interval", defaultHealthInterval, "health check interval")

	// Config
	configPath := fs.String("config", os.Getenv("CONFIG"), "path to router config file (JSON)")
	fs.StringVar(configPath, "f", os.Getenv("CONFIG"), "alias for --config")
	dumpConfig := fs.Bool("dump-config", false, "print resolved config as JSON and exit")
	fs.BoolVar(dumpConfig, "D", false, "alias for --dump-config")

	// TLS
	tlsEnabled, tlsCert, tlsKey, acmeEmail, tlsStaging := addTLSFlags(fs)

	// Logging
	logLvl, logFmt, logPath := addLogFlags(fs)

	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "SNI routing reverse proxy with built-in ACME TLS\n\n")
		fmt.Fprintf(os.Stderr, "Usage: tltv router [flags]\n\n")
		fmt.Fprintf(os.Stderr, "Routes incoming HTTPS requests to backends based on the Host header.\n")
		fmt.Fprintf(os.Stderr, "Obtains and renews TLS certificates automatically via Let's Encrypt.\n\n")
		fmt.Fprintf(os.Stderr, "Routes:\n")
		fmt.Fprintf(os.Stderr, "  -r, --route HOST=BACKEND  route mapping (repeatable)\n\n")
		fmt.Fprintf(os.Stderr, "Server:\n")
		fmt.Fprintf(os.Stderr, "  -l, --listen ADDR         HTTPS listen address (default: :443)\n")
		fmt.Fprintf(os.Stderr, "      --http-listen ADDR    HTTP listen for challenges + redirect (default: :80)\n\n")
		fmt.Fprintf(os.Stderr, "Health:\n")
		fmt.Fprintf(os.Stderr, "  -H, --health-check PATH   health check path on backends (e.g. /health)\n")
		fmt.Fprintf(os.Stderr, "      --health-interval DUR health check interval (default: 30s)\n\n")
		fmt.Fprintf(os.Stderr, "Config:\n")
		fmt.Fprintf(os.Stderr, "  -f, --config PATH         router config file (JSON)\n")
		fmt.Fprintf(os.Stderr, "  -D, --dump-config         print resolved config as JSON and exit\n\n")
		fmt.Fprintf(os.Stderr, "TLS:\n")
		fmt.Fprintf(os.Stderr, "      --tls                 enable TLS (autocert via Let's Encrypt if no cert/key)\n")
		fmt.Fprintf(os.Stderr, "      --tls-cert FILE       TLS certificate file (PEM)\n")
		fmt.Fprintf(os.Stderr, "      --tls-key FILE        TLS private key file (PEM)\n")
		fmt.Fprintf(os.Stderr, "      --tls-staging         use Let's Encrypt staging (for testing)\n")
		fmt.Fprintf(os.Stderr, "      --acme-email EMAIL    email for ACME account (optional)\n\n")
		fmt.Fprintf(os.Stderr, "Logging:\n")
		fmt.Fprintf(os.Stderr, "      --log-level LEVEL     log level: debug, info, error (default: info)\n")
		fmt.Fprintf(os.Stderr, "      --log-format FORMAT   log format: human, json (default: human)\n")
		fmt.Fprintf(os.Stderr, "      --log-file PATH       log to file instead of stderr\n\n")
		fmt.Fprintf(os.Stderr, "Environment variables: ROUTES (comma-separated host=backend pairs),\n")
		fmt.Fprintf(os.Stderr, "LISTEN, HTTP_LISTEN, HEALTH_CHECK, HEALTH_INTERVAL, CONFIG,\n")
		fmt.Fprintf(os.Stderr, "TLS=1, TLS_CERT, TLS_KEY, TLS_STAGING=1, TLS_DIR, ACME_EMAIL,\n")
		fmt.Fprintf(os.Stderr, "LOG_LEVEL, LOG_FORMAT, LOG_FILE.\n")
		fmt.Fprintf(os.Stderr, "Flags override env vars.\n\n")
		fmt.Fprintf(os.Stderr, "Examples:\n")
		fmt.Fprintf(os.Stderr, "  tltv router --tls -r demo.tv=localhost:8001 -r api.demo.tv=localhost:8002\n")
		fmt.Fprintf(os.Stderr, "  tltv router --tls --config router.json\n")
		fmt.Fprintf(os.Stderr, "  ROUTES=\"demo.tv=localhost:8001,api.tv=localhost:8002\" tltv router --tls\n")
	}

	fs.Parse(args)

	// Load config file and apply to unset flags.
	var cfg map[string]interface{}
	if *configPath != "" {
		var err error
		cfg, err = loadDaemonConfig(*configPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: load config: %v\n", err)
			os.Exit(1)
		}
		applyConfigToFlags(fs, cfg)
	}

	// Merge routes from all sources: --route flags, ROUTES env, config file.
	routeMap := make(map[string]string)

	// (1) Config file routes.
	if cfg != nil {
		if routesCfg, ok := cfg["routes"]; ok {
			if rm, ok := routesCfg.(map[string]interface{}); ok {
				for host, backend := range rm {
					if bs, ok := backend.(string); ok {
						routeMap[strings.ToLower(host)] = bs
					}
				}
			}
		}
	}

	// (2) ROUTES env var (comma-separated host=backend).
	if envRoutes := os.Getenv("ROUTES"); envRoutes != "" {
		for _, pair := range strings.Split(envRoutes, ",") {
			pair = strings.TrimSpace(pair)
			if pair == "" {
				continue
			}
			host, backend, ok := parseRoutePair(pair)
			if !ok {
				fmt.Fprintf(os.Stderr, "error: invalid route %q (expected host=backend)\n", pair)
				os.Exit(1)
			}
			routeMap[host] = backend
		}
	}

	// (3) --route / -r flags (highest priority, override config and env).
	for _, pair := range routes {
		host, backend, ok := parseRoutePair(pair)
		if !ok {
			fmt.Fprintf(os.Stderr, "error: invalid route %q (expected host=backend)\n", pair)
			os.Exit(1)
		}
		routeMap[host] = backend
	}

	// Dump config and exit if requested.
	if *dumpConfig {
		dumpCfg := make(map[string]interface{})
		if len(routeMap) > 0 {
			dumpCfg["routes"] = routeMap
		}
		if *listenAddr != ":443" {
			dumpCfg["listen"] = *listenAddr
		}
		if *httpListenAddr != ":80" {
			dumpCfg["http_listen"] = *httpListenAddr
		}
		if *healthPath != "" {
			dumpCfg["health_check"] = *healthPath
		}
		if *healthIntervalStr != "30s" {
			dumpCfg["health_interval"] = *healthIntervalStr
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.SetEscapeHTML(false)
		enc.Encode(dumpCfg)
		os.Exit(0)
	}

	if len(routeMap) == 0 {
		fmt.Fprintf(os.Stderr, "error: no routes defined\n")
		fmt.Fprintf(os.Stderr, "Use --route HOST=BACKEND, ROUTES env var, or --config\n")
		os.Exit(1)
	}

	// Parse health interval.
	healthInterval, err := time.ParseDuration(*healthIntervalStr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: invalid health-interval: %v\n", err)
		os.Exit(1)
	}

	// Setup logging.
	if err := setupLogging(*logLvl, *logFmt, *logPath, "router"); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	// Build route entries with reverse proxies.
	entries := make(map[string]*routeEntry, len(routeMap))
	hostnames := make([]string, 0, len(routeMap))
	for host, backend := range routeMap {
		entry := &routeEntry{
			hostname: host,
			backend:  backend,
			proxy:    buildReverseProxy(backend),
		}
		entry.healthy.Store(true) // assume healthy until first check
		entries[host] = entry
		hostnames = append(hostnames, host)
	}

	// Log startup info.
	logInfof("starting router with %d route(s)", len(entries))
	for host, entry := range entries {
		logInfof("  %s → %s", host, entry.backend)
	}

	// TLS setup.
	var tlsConfig *tls.Config
	var httpSrv *http.Server
	var tlsCleanup func()

	if *tlsEnabled || *tlsCert != "" || *tlsKey != "" {
		store, httpServer, cleanup, err := tlsSetupMulti(hostnames, *tlsEnabled, *tlsCert, *tlsKey, *acmeEmail, *tlsStaging, *httpListenAddr)
		if err != nil {
			logFatalf("tls setup: %v", err)
		}
		if store != nil {
			tlsConfig = store.TLSConfig()
		}
		httpSrv = httpServer
		tlsCleanup = cleanup
	} else {
		tlsCleanup = func() {}
	}

	// Start health check goroutines.
	var healthCtx context.Context
	var healthCancel context.CancelFunc
	if *healthPath != "" {
		healthCtx, healthCancel = context.WithCancel(context.Background())
		for _, entry := range entries {
			go routerHealthLoop(healthCtx, entry, *healthPath, healthInterval)
		}
		logInfof("health checks enabled: %s every %s", *healthPath, healthInterval)
	}

	// Build main handler.
	handler := routerHandler(entries)

	// Start main server.
	ln, err := net.Listen("tcp", *listenAddr)
	if err != nil {
		logFatalf("listen %s: %v", *listenAddr, err)
	}

	srv := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
		TLSConfig:         tlsConfig,
	}

	go func() {
		var err error
		if tlsConfig != nil {
			logInfof("listening on %s (HTTPS)", displayListenAddr(*listenAddr))
			err = srv.ServeTLS(ln, "", "")
		} else {
			logInfof("listening on %s (HTTP)", displayListenAddr(*listenAddr))
			err = srv.Serve(ln)
		}
		if err != nil && err != http.ErrServerClosed {
			logFatalf("server: %v", err)
		}
	}()

	// Wait for shutdown signal.
	sig := make(chan os.Signal, 1)
	signalNotify(sig)
	<-sig

	logInfof("shutting down...")

	// Graceful shutdown.
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()

	srv.Shutdown(shutdownCtx)

	if healthCancel != nil {
		healthCancel()
	}

	if httpSrv != nil {
		httpCtx, httpCancel := context.WithTimeout(context.Background(), 3*time.Second)
		httpSrv.Shutdown(httpCtx)
		httpCancel()
	}

	tlsCleanup()

	logInfof("stopped")
}

// parseRoutePair splits "host=backend" into lowercase host and backend.
func parseRoutePair(s string) (host, backend string, ok bool) {
	idx := strings.IndexByte(s, '=')
	if idx <= 0 || idx >= len(s)-1 {
		return "", "", false
	}
	host = strings.ToLower(strings.TrimSpace(s[:idx]))
	backend = strings.TrimSpace(s[idx+1:])
	if host == "" || backend == "" {
		return "", "", false
	}
	return host, backend, true
}

// buildReverseProxy creates an httputil.ReverseProxy for a backend address.
func buildReverseProxy(backend string) *httputil.ReverseProxy {
	target := &url.URL{
		Scheme: "http",
		Host:   backend,
	}
	proxy := httputil.NewSingleHostReverseProxy(target)

	// Customize the director to set forwarding headers.
	origDirector := proxy.Director
	proxy.Director = func(req *http.Request) {
		origDirector(req)
		// X-Forwarded-Proto
		req.Header.Set("X-Forwarded-Proto", "https")
		// X-Real-IP from RemoteAddr
		if clientIP, _, err := net.SplitHostPort(req.RemoteAddr); err == nil {
			req.Header.Set("X-Real-IP", clientIP)
		}
	}

	// Reasonable transport timeouts.
	proxy.Transport = &http.Transport{
		DialContext: (&net.Dialer{
			Timeout:   10 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		MaxIdleConnsPerHost:   100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   5 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
	}

	// Suppress noisy reverse proxy error logging -- use structured logging.
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		logErrorf("proxy %s: %v", r.Host, err)
		jsonError(w, "backend_error", http.StatusBadGateway)
	}

	return proxy
}

// routerHandler returns the main HTTP handler that dispatches by Host header.
func routerHandler(entries map[string]*routeEntry) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Strip port from Host header for lookup.
		host := r.Host
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
		host = strings.ToLower(host)

		entry, ok := entries[host]
		if !ok {
			logDebugf("no route for host %q", host)
			jsonError(w, "no_route", http.StatusBadGateway)
			return
		}

		if !entry.healthy.Load() {
			logDebugf("backend unhealthy for %s", host)
			jsonError(w, "backend_unavailable", http.StatusServiceUnavailable)
			return
		}

		entry.proxy.ServeHTTP(w, r)
	})
}

// routerHealthLoop polls a backend's health check endpoint periodically.
func routerHealthLoop(ctx context.Context, entry *routeEntry, path string, interval time.Duration) {
	client := &http.Client{Timeout: 5 * time.Second}
	checkURL := "http://" + entry.backend + path

	// Initial check immediately.
	routerHealthCheck(client, checkURL, entry)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			routerHealthCheck(client, checkURL, entry)
		}
	}
}

// routerHealthCheck performs a single health check and updates the entry's healthy flag.
func routerHealthCheck(client *http.Client, checkURL string, entry *routeEntry) {
	resp, err := client.Get(checkURL)
	if err != nil {
		if entry.healthy.Load() {
			logErrorf("health: %s is down: %v", entry.hostname, err)
		}
		entry.healthy.Store(false)
		return
	}
	resp.Body.Close()

	if resp.StatusCode >= 200 && resp.StatusCode < 400 {
		if !entry.healthy.Load() {
			logInfof("health: %s is up (status %d)", entry.hostname, resp.StatusCode)
		}
		entry.healthy.Store(true)
	} else {
		if entry.healthy.Load() {
			logErrorf("health: %s is down (status %d)", entry.hostname, resp.StatusCode)
		}
		entry.healthy.Store(false)
	}
}
