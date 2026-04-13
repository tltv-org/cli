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
	"sort"
	"strings"
	"sync"
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

// ---------- Route Data Structures ----------

// routeBackend holds a single backend with its reverse proxy and health state.
type routeBackend struct {
	address string
	proxy   *httputil.ReverseProxy
	healthy atomic.Bool
}

// routeEntry holds a parsed route with optional path prefix and one or more backends.
type routeEntry struct {
	hostname string
	prefix   string           // "" for catch-all, "/tltv" for path routes
	backends []*routeBackend  // slice for round-robin, usually len 1
	next     atomic.Uint64    // round-robin counter
}

// pickBackend returns the next healthy backend via round-robin.
// Returns nil if all backends are unhealthy.
func (e *routeEntry) pickBackend() *routeBackend {
	n := len(e.backends)
	if n == 0 {
		return nil
	}
	if n == 1 {
		if e.backends[0].healthy.Load() {
			return e.backends[0]
		}
		return nil
	}
	start := int(e.next.Add(1)-1) % n
	for i := 0; i < n; i++ {
		b := e.backends[(start+i)%n]
		if b.healthy.Load() {
			return b
		}
	}
	return nil
}

// routeTable holds all routes grouped by hostname with prefix-sorted entries.
type routeTable struct {
	byHost map[string][]*routeEntry // hostname -> entries sorted by prefix length desc
}

// match finds the best matching route entry and a healthy backend for the
// given hostname and request path. Returns (nil, nil) if no route matches.
func (t *routeTable) match(host, path string) (*routeEntry, *routeBackend) {
	entries, ok := t.byHost[host]
	if !ok {
		return nil, nil
	}
	// Entries are sorted by prefix length descending (longest first).
	// Catch-all (prefix="") is always last.
	for _, entry := range entries {
		if entry.prefix == "" {
			// Catch-all: always matches
			return entry, entry.pickBackend()
		}
		if path == entry.prefix || strings.HasPrefix(path, entry.prefix+"/") {
			return entry, entry.pickBackend()
		}
	}
	return nil, nil
}

// hostnames returns unique hostnames from all routes (for TLS setup).
func (t *routeTable) hostnames() []string {
	hosts := make([]string, 0, len(t.byHost))
	for h := range t.byHost {
		hosts = append(hosts, h)
	}
	return hosts
}

// ---------- Route Parsing ----------

// parsedRoute is an intermediate representation during route merging.
type parsedRoute struct {
	hostname string
	prefix   string
	backends []string
}

// routeKey returns the merge key for a parsed route.
func routeKey(host, prefix string) string {
	if prefix == "" {
		return host
	}
	return host + prefix
}

// parseRoutePair splits "host[/prefix]=backend[,backend...]" into components.
// Hostname is lowercased. Prefix is NOT lowercased (paths are case-sensitive).
func parseRoutePair(s string) (host, prefix string, backends []string, ok bool) {
	idx := strings.IndexByte(s, '=')
	if idx <= 0 || idx >= len(s)-1 {
		return "", "", nil, false
	}
	left := strings.TrimSpace(s[:idx])
	right := strings.TrimSpace(s[idx+1:])
	if left == "" || right == "" {
		return "", "", nil, false
	}

	// Split left into hostname and optional prefix.
	if slashIdx := strings.IndexByte(left, '/'); slashIdx >= 0 {
		host = strings.ToLower(left[:slashIdx])
		prefix = left[slashIdx:] // keeps the leading /
	} else {
		host = strings.ToLower(left)
		prefix = ""
	}

	if host == "" {
		return "", "", nil, false
	}

	// Split right into backends (comma-separated).
	for _, b := range strings.Split(right, ",") {
		b = strings.TrimSpace(b)
		if b != "" {
			backends = append(backends, b)
		}
	}
	if len(backends) == 0 {
		return "", "", nil, false
	}

	return host, prefix, backends, true
}

// splitRouteEnvPairs splits a ROUTES env var value into route pair strings.
// Uses semicolons if present (for multi-backend routes), otherwise commas
// (backward compatible with existing single-backend deployments).
func splitRouteEnvPairs(s string) []string {
	if strings.Contains(s, ";") {
		return strings.Split(s, ";")
	}
	return strings.Split(s, ",")
}

// parseConfigRoutes extracts route entries from a config map.
// Handles both string values (single backend) and array values (multi-backend).
// Keys may include path prefixes: "host/prefix".
func parseConfigRoutes(cfg map[string]interface{}) map[string]parsedRoute {
	routes := make(map[string]parsedRoute)
	routesCfg, ok := cfg["routes"]
	if !ok {
		return routes
	}
	rm, ok := routesCfg.(map[string]interface{})
	if !ok {
		return routes
	}
	for key, val := range rm {
		// Parse key as host[/prefix]. Lowercase hostname, preserve prefix case.
		var host, prefix string
		if slashIdx := strings.IndexByte(key, '/'); slashIdx >= 0 {
			host = strings.ToLower(key[:slashIdx])
			prefix = key[slashIdx:]
		} else {
			host = strings.ToLower(key)
			prefix = ""
		}
		if host == "" {
			continue
		}

		var backends []string
		switch v := val.(type) {
		case string:
			v = strings.TrimSpace(v)
			if v != "" {
				backends = []string{v}
			}
		case []interface{}:
			for _, item := range v {
				if s, ok := item.(string); ok {
					s = strings.TrimSpace(s)
					if s != "" {
						backends = append(backends, s)
					}
				}
			}
		}
		if len(backends) == 0 {
			continue
		}

		rk := routeKey(host, prefix)
		routes[rk] = parsedRoute{hostname: host, prefix: prefix, backends: backends}
	}
	return routes
}

// ---------- Route Table Construction ----------

// buildRouteTable constructs a routeTable from parsed routes.
// Entries per hostname are sorted by prefix length descending (longest first).
func buildRouteTable(routes map[string]parsedRoute, useTLS bool) *routeTable {
	byHost := make(map[string][]*routeEntry)
	for _, r := range routes {
		backends := make([]*routeBackend, len(r.backends))
		for i, addr := range r.backends {
			b := &routeBackend{
				address: addr,
				proxy:   buildReverseProxy(addr, useTLS),
			}
			b.healthy.Store(true)
			backends[i] = b
		}
		entry := &routeEntry{
			hostname: r.hostname,
			prefix:   r.prefix,
			backends: backends,
		}
		byHost[r.hostname] = append(byHost[r.hostname], entry)
	}

	// Sort entries per hostname by prefix length descending.
	// Catch-all (empty prefix) comes last.
	for _, entries := range byHost {
		sort.Slice(entries, func(i, j int) bool {
			return len(entries[i].prefix) > len(entries[j].prefix)
		})
	}

	return &routeTable{byHost: byHost}
}

// ---------- Reverse Proxy ----------

// buildReverseProxy creates an httputil.ReverseProxy for a backend address.
// useTLS controls the X-Forwarded-Proto header value.
func buildReverseProxy(backend string, useTLS bool) *httputil.ReverseProxy {
	target := &url.URL{
		Scheme: "http",
		Host:   backend,
	}
	proxy := httputil.NewSingleHostReverseProxy(target)

	proto := "http"
	if useTLS {
		proto = "https"
	}

	// Customize the director to set forwarding headers.
	origDirector := proxy.Director
	proxy.Director = func(req *http.Request) {
		origDirector(req)
		req.Header.Set("X-Forwarded-Proto", proto)
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

// ---------- Request Handler ----------

// routerHandler returns the main HTTP handler that dispatches by Host header
// and path prefix. Reads the current route table from the atomic pointer on
// every request (supports hot-reload).
func routerHandler(tablePtr *atomic.Pointer[routeTable]) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Strip port from Host header for lookup.
		host := r.Host
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
		host = strings.ToLower(host)

		table := tablePtr.Load()
		entry, backend := table.match(host, r.URL.Path)
		if entry == nil {
			logDebugf("no route for host %q path %q", host, r.URL.Path)
			jsonError(w, "no_route", http.StatusBadGateway)
			return
		}
		if backend == nil {
			logDebugf("all backends down for %s%s", host, entry.prefix)
			jsonError(w, "backend_unavailable", http.StatusServiceUnavailable)
			return
		}

		backend.proxy.ServeHTTP(w, r)
	})
}

// ---------- Health Checks ----------

// routerBackendHealthLoop polls a backend's health check endpoint periodically.
func routerBackendHealthLoop(ctx context.Context, backend *routeBackend, label, path string, interval time.Duration) {
	client := &http.Client{Timeout: 5 * time.Second}
	checkURL := "http://" + backend.address + path

	// Initial check immediately.
	routerBackendHealthCheck(client, checkURL, backend, label)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			routerBackendHealthCheck(client, checkURL, backend, label)
		}
	}
}

// routerBackendHealthCheck performs a single health check and updates the backend's healthy flag.
func routerBackendHealthCheck(client *http.Client, checkURL string, backend *routeBackend, label string) {
	resp, err := client.Get(checkURL)
	if err != nil {
		if backend.healthy.Load() {
			logErrorf("health: %s (%s) is down: %v", label, backend.address, err)
		}
		backend.healthy.Store(false)
		return
	}
	resp.Body.Close()

	if resp.StatusCode >= 200 && resp.StatusCode < 400 {
		if !backend.healthy.Load() {
			logInfof("health: %s (%s) is up (status %d)", label, backend.address, resp.StatusCode)
		}
		backend.healthy.Store(true)
	} else {
		if backend.healthy.Load() {
			logErrorf("health: %s (%s) is down (status %d)", label, backend.address, resp.StatusCode)
		}
		backend.healthy.Store(false)
	}
}

// startRouterHealthChecks starts health check goroutines for all backends
// in the route table. Returns a cancel function to stop all goroutines.
func startRouterHealthChecks(table *routeTable, healthPath string, healthInterval time.Duration) context.CancelFunc {
	if healthPath == "" {
		return func() {}
	}
	ctx, cancel := context.WithCancel(context.Background())
	for _, entries := range table.byHost {
		for _, entry := range entries {
			label := entry.hostname
			if entry.prefix != "" {
				label += entry.prefix
			}
			for _, backend := range entry.backends {
				go routerBackendHealthLoop(ctx, backend, label, healthPath, healthInterval)
			}
		}
	}
	return cancel
}

// routerHealthState manages health check goroutine lifecycle across reloads.
type routerHealthState struct {
	mu     sync.Mutex
	cancel context.CancelFunc
}

func (s *routerHealthState) replace(table *routeTable, healthPath string, healthInterval time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cancel != nil {
		s.cancel()
	}
	s.cancel = startRouterHealthChecks(table, healthPath, healthInterval)
}

func (s *routerHealthState) stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cancel != nil {
		s.cancel()
	}
}

// ---------- Dump Config ----------

// dumpRouterConfig builds the config map for --dump-config output.
// Path routes use "host/prefix" keys. Multi-backend routes use array values.
func dumpRouterConfig(routes map[string]parsedRoute, listenAddr, httpListenAddr, healthPath, healthIntervalStr string) map[string]interface{} {
	cfg := make(map[string]interface{})
	if len(routes) > 0 {
		rm := make(map[string]interface{})
		for _, r := range routes {
			key := r.hostname
			if r.prefix != "" {
				key += r.prefix
			}
			if len(r.backends) == 1 {
				rm[key] = r.backends[0]
			} else {
				// Convert to []interface{} for JSON marshaling consistency.
				arr := make([]interface{}, len(r.backends))
				for i, b := range r.backends {
					arr[i] = b
				}
				rm[key] = arr
			}
		}
		cfg["routes"] = rm
	}
	if listenAddr != ":443" {
		cfg["listen"] = listenAddr
	}
	if httpListenAddr != ":80" {
		cfg["http_listen"] = httpListenAddr
	}
	if healthPath != "" {
		cfg["health_check"] = healthPath
	}
	if healthIntervalStr != "30s" {
		cfg["health_interval"] = healthIntervalStr
	}
	return cfg
}

// ---------- Command ----------

// cmdRouter implements the "tltv router" subcommand -- a lightweight L7
// reverse proxy with multi-domain SNI routing, path-prefix dispatch,
// multi-backend round-robin, and built-in ACME TLS.
func cmdRouter(args []string) {
	fs := flag.NewFlagSet("router", flag.ExitOnError)

	// Route flags (repeatable)
	var routes routeSlice
	fs.Var(&routes, "route", "route mapping HOST[/PREFIX]=BACKEND[,BACKEND...] (repeatable)")
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
	dumpConfigFlag := fs.Bool("dump-config", false, "print resolved config as JSON and exit")
	fs.BoolVar(dumpConfigFlag, "D", false, "alias for --dump-config")

	// TLS
	tlsEnabled, tlsCert, tlsKey, acmeEmail, tlsStaging := addTLSFlags(fs)

	// Logging
	logLvl, logFmt, logPath := addLogFlags(fs)

	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "SNI routing reverse proxy with built-in ACME TLS\n\n")
		fmt.Fprintf(os.Stderr, "Usage: tltv router [flags]\n\n")
		fmt.Fprintf(os.Stderr, "Routes incoming HTTPS requests to backends based on the Host header\n")
		fmt.Fprintf(os.Stderr, "and optional path prefix. Supports round-robin across multiple backends.\n")
		fmt.Fprintf(os.Stderr, "Obtains and renews TLS certificates automatically via Let's Encrypt.\n\n")
		fmt.Fprintf(os.Stderr, "Routes:\n")
		fmt.Fprintf(os.Stderr, "  -r, --route HOST[/PREFIX]=BACKEND[,BACKEND...]  route mapping (repeatable)\n\n")
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
		fmt.Fprintf(os.Stderr, "Environment variables: ROUTES (semicolon- or comma-separated route pairs),\n")
		fmt.Fprintf(os.Stderr, "LISTEN, HTTP_LISTEN, HEALTH_CHECK, HEALTH_INTERVAL, CONFIG,\n")
		fmt.Fprintf(os.Stderr, "TLS=1, TLS_CERT, TLS_KEY, TLS_STAGING=1, TLS_DIR, ACME_EMAIL,\n")
		fmt.Fprintf(os.Stderr, "LOG_LEVEL, LOG_FORMAT, LOG_FILE.\n")
		fmt.Fprintf(os.Stderr, "Flags override env vars. Config file reloaded on change (30s check).\n\n")
		fmt.Fprintf(os.Stderr, "Examples:\n")
		fmt.Fprintf(os.Stderr, "  tltv router --tls -r demo.tv=localhost:8001 -r api.demo.tv=localhost:8002\n")
		fmt.Fprintf(os.Stderr, "  tltv router --tls -r demo.tv/tltv=relay:8000 -r demo.tv=phosphor:80\n")
		fmt.Fprintf(os.Stderr, "  tltv router --tls -r lb.tv=bridge1:8000,bridge2:8000\n")
		fmt.Fprintf(os.Stderr, "  tltv router --tls --config router.json\n")
		fmt.Fprintf(os.Stderr, "  ROUTES=\"demo.tv=localhost:8001;api.tv=localhost:8002\" tltv router --tls\n")
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

	// Parse routes from all three sources.
	// Priority: CLI flags > env var > config file.
	allRoutes := make(map[string]parsedRoute)

	// (1) Config file routes (lowest priority).
	if cfg != nil {
		for k, v := range parseConfigRoutes(cfg) {
			allRoutes[k] = v
		}
	}

	// (2) ROUTES env var.
	envRoutes := make(map[string]parsedRoute)
	if envStr := os.Getenv("ROUTES"); envStr != "" {
		for _, pair := range splitRouteEnvPairs(envStr) {
			pair = strings.TrimSpace(pair)
			if pair == "" {
				continue
			}
			host, prefix, backends, ok := parseRoutePair(pair)
			if !ok {
				fmt.Fprintf(os.Stderr, "error: invalid route %q (expected host[/prefix]=backend[,backend...])\n", pair)
				os.Exit(1)
			}
			rk := routeKey(host, prefix)
			envRoutes[rk] = parsedRoute{hostname: host, prefix: prefix, backends: backends}
		}
	}
	for k, v := range envRoutes {
		allRoutes[k] = v
	}

	// (3) --route / -r flags (highest priority).
	cliRoutes := make(map[string]parsedRoute)
	for _, pair := range routes {
		host, prefix, backends, ok := parseRoutePair(pair)
		if !ok {
			fmt.Fprintf(os.Stderr, "error: invalid route %q (expected host[/prefix]=backend[,backend...])\n", pair)
			os.Exit(1)
		}
		rk := routeKey(host, prefix)
		cliRoutes[rk] = parsedRoute{hostname: host, prefix: prefix, backends: backends}
	}
	for k, v := range cliRoutes {
		allRoutes[k] = v
	}

	// Dump config and exit if requested.
	if *dumpConfigFlag {
		dumpCfg := dumpRouterConfig(allRoutes, *listenAddr, *httpListenAddr, *healthPath, *healthIntervalStr)
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.SetEscapeHTML(false)
		enc.Encode(dumpCfg)
		os.Exit(0)
	}

	if len(allRoutes) == 0 {
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

	// Determine TLS mode for X-Forwarded-Proto.
	useTLS := *tlsEnabled || *tlsCert != "" || *tlsKey != ""

	// Build initial route table.
	table := buildRouteTable(allRoutes, useTLS)
	var tablePtr atomic.Pointer[routeTable]
	tablePtr.Store(table)

	// Log startup info.
	count := 0
	for _, entries := range table.byHost {
		count += len(entries)
	}
	logInfof("starting router with %d route(s)", count)
	for _, entries := range table.byHost {
		for _, entry := range entries {
			addrs := make([]string, len(entry.backends))
			for i, b := range entry.backends {
				addrs[i] = b.address
			}
			route := entry.hostname
			if entry.prefix != "" {
				route += entry.prefix
			}
			logInfof("  %s → %s", route, strings.Join(addrs, ", "))
		}
	}

	// TLS setup.
	hostnames := table.hostnames()
	var tlsConfig *tls.Config
	var httpSrv *http.Server
	var tlsCleanup func()
	var store *certStore

	if useTLS {
		s, httpServer, cleanup, err := tlsSetupMulti(hostnames, *tlsEnabled, *tlsCert, *tlsKey, *acmeEmail, *tlsStaging, *httpListenAddr)
		if err != nil {
			logFatalf("tls setup: %v", err)
		}
		store = s
		if store != nil {
			tlsConfig = store.TLSConfig()
		}
		httpSrv = httpServer
		tlsCleanup = cleanup
	} else {
		tlsCleanup = func() {}
	}

	// Start health check goroutines.
	healthState := &routerHealthState{}
	healthState.replace(table, *healthPath, healthInterval)
	if *healthPath != "" {
		logInfof("health checks enabled: %s every %s", *healthPath, healthInterval)
	}

	// Config hot-reload.
	var reloadCancel context.CancelFunc
	if *configPath != "" {
		reloadCtx, cancel := context.WithCancel(context.Background())
		reloadCancel = cancel

		// Static routes (CLI + env) that persist across reloads.
		staticRoutes := make(map[string]parsedRoute)
		for k, v := range envRoutes {
			staticRoutes[k] = v
		}
		for k, v := range cliRoutes {
			staticRoutes[k] = v
		}

		hPath := *healthPath
		hInterval := healthInterval

		go configReloadLoop(reloadCtx, newConfigWatcher(*configPath), func(newCfg map[string]interface{}) {
			// Merge: config (new) < static (preserved).
			merged := parseConfigRoutes(newCfg)
			for k, v := range staticRoutes {
				merged[k] = v
			}
			if len(merged) == 0 {
				logErrorf("config reload: no routes after merge (keeping current)")
				return
			}

			// Build new route table.
			newTable := buildRouteTable(merged, useTLS)

			// Ensure TLS certs for any new hostnames.
			if store != nil {
				for _, h := range newTable.hostnames() {
					ctx, ctxCancel := context.WithTimeout(context.Background(), 5*time.Minute)
					if err := store.EnsureHostname(ctx, h); err != nil {
						logErrorf("config reload: cert for %s: %v (keeping current)", h, err)
						ctxCancel()
						return
					}
					ctxCancel()
				}
			}

			// Restart health checks and swap route table.
			healthState.replace(newTable, hPath, hInterval)
			tablePtr.Store(newTable)

			rc := 0
			for _, entries := range newTable.byHost {
				rc += len(entries)
			}
			logInfof("config reload: updated to %d route(s)", rc)
		})
	}

	// Build main handler.
	handler := routerHandler(&tablePtr)

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

	if reloadCancel != nil {
		reloadCancel()
	}

	healthState.stop()

	if httpSrv != nil {
		httpCtx, httpCancel := context.WithTimeout(context.Background(), 3*time.Second)
		httpSrv.Shutdown(httpCtx)
		httpCancel()
	}

	tlsCleanup()

	logInfof("stopped")
}
