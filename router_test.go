package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// ---------- parseRoutePair ----------

func TestParseRoutePair_Valid(t *testing.T) {
	host, backend, ok := parseRoutePair("demo.tv=localhost:8001")
	if !ok {
		t.Fatal("expected ok=true")
	}
	if host != "demo.tv" {
		t.Errorf("host = %q, want %q", host, "demo.tv")
	}
	if backend != "localhost:8001" {
		t.Errorf("backend = %q, want %q", backend, "localhost:8001")
	}
}

func TestParseRoutePair_CaseNormalization(t *testing.T) {
	host, backend, ok := parseRoutePair("Demo.TV=localhost:8001")
	if !ok {
		t.Fatal("expected ok=true")
	}
	if host != "demo.tv" {
		t.Errorf("host = %q, want lowercase %q", host, "demo.tv")
	}
	if backend != "localhost:8001" {
		t.Errorf("backend = %q, want %q", backend, "localhost:8001")
	}
}

func TestParseRoutePair_NoEquals(t *testing.T) {
	_, _, ok := parseRoutePair("demo.tv")
	if ok {
		t.Error("expected ok=false for input without '='")
	}
}

func TestParseRoutePair_EmptyHost(t *testing.T) {
	_, _, ok := parseRoutePair("=localhost:8001")
	if ok {
		t.Error("expected ok=false for empty host")
	}
}

func TestParseRoutePair_EmptyBackend(t *testing.T) {
	_, _, ok := parseRoutePair("demo.tv=")
	if ok {
		t.Error("expected ok=false for empty backend")
	}
}

func TestParseRoutePair_Spaces(t *testing.T) {
	host, backend, ok := parseRoutePair("  demo.tv = localhost:8001  ")
	if !ok {
		t.Fatal("expected ok=true after trimming spaces")
	}
	if host != "demo.tv" {
		t.Errorf("host = %q, want %q", host, "demo.tv")
	}
	if backend != "localhost:8001" {
		t.Errorf("backend = %q, want %q", backend, "localhost:8001")
	}
}

// ---------- routerHandler ----------

func TestRouterHandler_ValidRoute(t *testing.T) {
	// Start a test backend that returns a known response.
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Backend", "test")
		w.WriteHeader(200)
		w.Write([]byte("hello from backend"))
	}))
	defer backend.Close()

	// Extract backend address (host:port) from test server URL.
	backendAddr := backend.Listener.Addr().String()

	entry := &routeEntry{
		hostname: "demo.tv",
		backend:  backendAddr,
		proxy:    buildReverseProxy(backendAddr),
	}
	entry.healthy.Store(true)

	entries := map[string]*routeEntry{"demo.tv": entry}
	handler := routerHandler(entries)

	req := httptest.NewRequest("GET", "/some/path", nil)
	req.Host = "demo.tv"
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Errorf("status = %d, want 200", w.Code)
	}
	if w.Body.String() != "hello from backend" {
		t.Errorf("body = %q, want %q", w.Body.String(), "hello from backend")
	}
}

func TestRouterHandler_UnknownHost(t *testing.T) {
	entries := map[string]*routeEntry{}
	handler := routerHandler(entries)

	req := httptest.NewRequest("GET", "/", nil)
	req.Host = "unknown.tv"
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusBadGateway {
		t.Errorf("status = %d, want %d", w.Code, http.StatusBadGateway)
	}

	var resp map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("invalid JSON response: %v", err)
	}
	if resp["error"] != "no_route" {
		t.Errorf("error = %q, want %q", resp["error"], "no_route")
	}
}

func TestRouterHandler_HostWithPort(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		w.Write([]byte("ok"))
	}))
	defer backend.Close()

	backendAddr := backend.Listener.Addr().String()

	entry := &routeEntry{
		hostname: "demo.tv",
		backend:  backendAddr,
		proxy:    buildReverseProxy(backendAddr),
	}
	entry.healthy.Store(true)

	entries := map[string]*routeEntry{"demo.tv": entry}
	handler := routerHandler(entries)

	// Send request with port in Host header — should be stripped for lookup.
	req := httptest.NewRequest("GET", "/", nil)
	req.Host = "demo.tv:443"
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Errorf("status = %d, want 200 (port should be stripped from Host)", w.Code)
	}
}

func TestRouterHandler_CaseInsensitive(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		w.Write([]byte("ok"))
	}))
	defer backend.Close()

	backendAddr := backend.Listener.Addr().String()

	entry := &routeEntry{
		hostname: "demo.tv",
		backend:  backendAddr,
		proxy:    buildReverseProxy(backendAddr),
	}
	entry.healthy.Store(true)

	entries := map[string]*routeEntry{"demo.tv": entry}
	handler := routerHandler(entries)

	// Send request with uppercase Host — handler lowercases before lookup.
	req := httptest.NewRequest("GET", "/", nil)
	req.Host = "DEMO.TV"
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Errorf("status = %d, want 200 (case-insensitive host lookup)", w.Code)
	}
}

func TestRouterHandler_BackendDown(t *testing.T) {
	entry := &routeEntry{
		hostname: "demo.tv",
		backend:  "localhost:1",
		proxy:    buildReverseProxy("localhost:1"),
	}
	entry.healthy.Store(false) // Mark as unhealthy

	entries := map[string]*routeEntry{"demo.tv": entry}
	handler := routerHandler(entries)

	req := httptest.NewRequest("GET", "/", nil)
	req.Host = "demo.tv"
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want %d", w.Code, http.StatusServiceUnavailable)
	}

	var resp map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("invalid JSON response: %v", err)
	}
	if resp["error"] != "backend_unavailable" {
		t.Errorf("error = %q, want %q", resp["error"], "backend_unavailable")
	}
}

// ---------- routerHealthCheck ----------

func TestRouterHealthCheck_Healthy(t *testing.T) {
	setupLogging("error", "", "", "test")

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			w.WriteHeader(200)
			return
		}
		w.WriteHeader(404)
	}))
	defer backend.Close()

	entry := &routeEntry{
		hostname: "demo.tv",
		backend:  backend.Listener.Addr().String(),
	}
	entry.healthy.Store(true)

	client := &http.Client{}
	checkURL := backend.URL + "/health"
	routerHealthCheck(client, checkURL, entry)

	if !entry.healthy.Load() {
		t.Error("expected healthy=true after 200 response")
	}
}

func TestRouterHealthCheck_Unhealthy(t *testing.T) {
	setupLogging("error", "", "", "test")

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(503)
	}))
	defer backend.Close()

	entry := &routeEntry{
		hostname: "demo.tv",
		backend:  backend.Listener.Addr().String(),
	}
	entry.healthy.Store(true)

	client := &http.Client{}
	checkURL := backend.URL + "/health"
	routerHealthCheck(client, checkURL, entry)

	if entry.healthy.Load() {
		t.Error("expected healthy=false after 503 response")
	}
}

func TestRouterHealthCheck_Recovery(t *testing.T) {
	setupLogging("error", "", "", "test")

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	}))
	defer backend.Close()

	entry := &routeEntry{
		hostname: "demo.tv",
		backend:  backend.Listener.Addr().String(),
	}
	entry.healthy.Store(false) // Start unhealthy

	client := &http.Client{}
	checkURL := backend.URL + "/health"
	routerHealthCheck(client, checkURL, entry)

	if !entry.healthy.Load() {
		t.Error("expected healthy=true after recovery (200 response)")
	}
}

func TestRouterHealthCheck_ConnectionRefused(t *testing.T) {
	setupLogging("error", "", "", "test")

	entry := &routeEntry{
		hostname: "demo.tv",
		backend:  "127.0.0.1:1", // nothing listening
	}
	entry.healthy.Store(true)

	client := &http.Client{}
	checkURL := "http://127.0.0.1:1/health"
	routerHealthCheck(client, checkURL, entry)

	if entry.healthy.Load() {
		t.Error("expected healthy=false when backend is unreachable")
	}
}

// ---------- buildReverseProxy ----------

func TestBuildReverseProxy_Headers(t *testing.T) {
	// Start a backend that echoes received headers.
	var gotProto, gotRealIP string
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotProto = r.Header.Get("X-Forwarded-Proto")
		gotRealIP = r.Header.Get("X-Real-IP")
		w.WriteHeader(200)
	}))
	defer backend.Close()

	backendAddr := backend.Listener.Addr().String()
	proxy := buildReverseProxy(backendAddr)

	req := httptest.NewRequest("GET", "/test", nil)
	req.RemoteAddr = "203.0.113.42:12345"
	w := httptest.NewRecorder()
	proxy.ServeHTTP(w, req)

	if gotProto != "https" {
		t.Errorf("X-Forwarded-Proto = %q, want %q", gotProto, "https")
	}
	if gotRealIP != "203.0.113.42" {
		t.Errorf("X-Real-IP = %q, want %q", gotRealIP, "203.0.113.42")
	}
}
