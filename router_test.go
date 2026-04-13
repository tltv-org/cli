package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// ---------- parseRoutePair ----------

func TestParseRoutePair_Valid(t *testing.T) {
	host, prefix, backends, ok := parseRoutePair("demo.tv=localhost:8001")
	if !ok {
		t.Fatal("expected ok=true")
	}
	if host != "demo.tv" {
		t.Errorf("host = %q, want %q", host, "demo.tv")
	}
	if prefix != "" {
		t.Errorf("prefix = %q, want empty", prefix)
	}
	if len(backends) != 1 || backends[0] != "localhost:8001" {
		t.Errorf("backends = %v, want [localhost:8001]", backends)
	}
}

func TestParseRoutePair_CaseNormalization(t *testing.T) {
	host, _, _, ok := parseRoutePair("Demo.TV=localhost:8001")
	if !ok {
		t.Fatal("expected ok=true")
	}
	if host != "demo.tv" {
		t.Errorf("host = %q, want lowercase %q", host, "demo.tv")
	}
}

func TestParseRoutePair_PathPrefix(t *testing.T) {
	host, prefix, backends, ok := parseRoutePair("demo.tv/tltv=relay:8000")
	if !ok {
		t.Fatal("expected ok=true")
	}
	if host != "demo.tv" {
		t.Errorf("host = %q, want %q", host, "demo.tv")
	}
	if prefix != "/tltv" {
		t.Errorf("prefix = %q, want %q", prefix, "/tltv")
	}
	if len(backends) != 1 || backends[0] != "relay:8000" {
		t.Errorf("backends = %v, want [relay:8000]", backends)
	}
}

func TestParseRoutePair_DeepPathPrefix(t *testing.T) {
	host, prefix, _, ok := parseRoutePair("demo.tv/.well-known/tltv=relay:8000")
	if !ok {
		t.Fatal("expected ok=true")
	}
	if host != "demo.tv" {
		t.Errorf("host = %q, want %q", host, "demo.tv")
	}
	if prefix != "/.well-known/tltv" {
		t.Errorf("prefix = %q, want %q", prefix, "/.well-known/tltv")
	}
}

func TestParseRoutePair_PrefixCasePreserved(t *testing.T) {
	_, prefix, _, ok := parseRoutePair("Demo.TV/MyApp=backend:80")
	if !ok {
		t.Fatal("expected ok=true")
	}
	if prefix != "/MyApp" {
		t.Errorf("prefix = %q, want %q (case should be preserved)", prefix, "/MyApp")
	}
}

func TestParseRoutePair_MultiBackend(t *testing.T) {
	host, prefix, backends, ok := parseRoutePair("lb.tv=bridge1:8000,bridge2:8000")
	if !ok {
		t.Fatal("expected ok=true")
	}
	if host != "lb.tv" {
		t.Errorf("host = %q, want %q", host, "lb.tv")
	}
	if prefix != "" {
		t.Errorf("prefix = %q, want empty", prefix)
	}
	if len(backends) != 2 || backends[0] != "bridge1:8000" || backends[1] != "bridge2:8000" {
		t.Errorf("backends = %v, want [bridge1:8000 bridge2:8000]", backends)
	}
}

func TestParseRoutePair_MultiBackendWithPrefix(t *testing.T) {
	_, prefix, backends, ok := parseRoutePair("lb.tv/api=svc1:8000,svc2:8000,svc3:8000")
	if !ok {
		t.Fatal("expected ok=true")
	}
	if prefix != "/api" {
		t.Errorf("prefix = %q, want %q", prefix, "/api")
	}
	if len(backends) != 3 {
		t.Errorf("len(backends) = %d, want 3", len(backends))
	}
}

func TestParseRoutePair_NoEquals(t *testing.T) {
	_, _, _, ok := parseRoutePair("demo.tv")
	if ok {
		t.Error("expected ok=false for input without '='")
	}
}

func TestParseRoutePair_EmptyHost(t *testing.T) {
	_, _, _, ok := parseRoutePair("=localhost:8001")
	if ok {
		t.Error("expected ok=false for empty host")
	}
}

func TestParseRoutePair_EmptyBackend(t *testing.T) {
	_, _, _, ok := parseRoutePair("demo.tv=")
	if ok {
		t.Error("expected ok=false for empty backend")
	}
}

func TestParseRoutePair_Spaces(t *testing.T) {
	host, _, backends, ok := parseRoutePair("  demo.tv = localhost:8001  ")
	if !ok {
		t.Fatal("expected ok=true after trimming spaces")
	}
	if host != "demo.tv" {
		t.Errorf("host = %q, want %q", host, "demo.tv")
	}
	if len(backends) != 1 || backends[0] != "localhost:8001" {
		t.Errorf("backends = %v, want [localhost:8001]", backends)
	}
}

// ---------- splitRouteEnvPairs ----------

func TestSplitRouteEnvPairs_Commas(t *testing.T) {
	parts := splitRouteEnvPairs("demo.tv=relay:8000,origin.tv=bridge:8000")
	if len(parts) != 2 {
		t.Fatalf("len = %d, want 2", len(parts))
	}
	if parts[0] != "demo.tv=relay:8000" || parts[1] != "origin.tv=bridge:8000" {
		t.Errorf("parts = %v", parts)
	}
}

func TestSplitRouteEnvPairs_Semicolons(t *testing.T) {
	parts := splitRouteEnvPairs("lb.tv=bridge1:8000,bridge2:8000;demo.tv=phosphor:80")
	if len(parts) != 2 {
		t.Fatalf("len = %d, want 2", len(parts))
	}
	if parts[0] != "lb.tv=bridge1:8000,bridge2:8000" {
		t.Errorf("parts[0] = %q", parts[0])
	}
	if parts[1] != "demo.tv=phosphor:80" {
		t.Errorf("parts[1] = %q", parts[1])
	}
}

func TestSplitRouteEnvPairs_SemicolonsOnly(t *testing.T) {
	parts := splitRouteEnvPairs("demo.tv=relay:8000;origin.tv=bridge:8000")
	if len(parts) != 2 {
		t.Fatalf("len = %d, want 2", len(parts))
	}
}

// ---------- parseConfigRoutes ----------

func TestParseConfigRoutes_StringBackend(t *testing.T) {
	cfg := map[string]interface{}{
		"routes": map[string]interface{}{
			"demo.tv": "relay:8000",
		},
	}
	routes := parseConfigRoutes(cfg)
	r, ok := routes["demo.tv"]
	if !ok {
		t.Fatal("expected route for demo.tv")
	}
	if len(r.backends) != 1 || r.backends[0] != "relay:8000" {
		t.Errorf("backends = %v", r.backends)
	}
}

func TestParseConfigRoutes_ArrayBackend(t *testing.T) {
	cfg := map[string]interface{}{
		"routes": map[string]interface{}{
			"lb.tv": []interface{}{"bridge1:8000", "bridge2:8000"},
		},
	}
	routes := parseConfigRoutes(cfg)
	r, ok := routes["lb.tv"]
	if !ok {
		t.Fatal("expected route for lb.tv")
	}
	if len(r.backends) != 2 {
		t.Fatalf("len(backends) = %d, want 2", len(r.backends))
	}
	if r.backends[0] != "bridge1:8000" || r.backends[1] != "bridge2:8000" {
		t.Errorf("backends = %v", r.backends)
	}
}

func TestParseConfigRoutes_PathPrefix(t *testing.T) {
	cfg := map[string]interface{}{
		"routes": map[string]interface{}{
			"demo.tv/tltv": "relay:8000",
			"demo.tv":      "phosphor:80",
		},
	}
	routes := parseConfigRoutes(cfg)

	r1, ok := routes["demo.tv/tltv"]
	if !ok {
		t.Fatal("expected route for demo.tv/tltv")
	}
	if r1.hostname != "demo.tv" || r1.prefix != "/tltv" {
		t.Errorf("r1 = %+v", r1)
	}

	r2, ok := routes["demo.tv"]
	if !ok {
		t.Fatal("expected route for demo.tv")
	}
	if r2.hostname != "demo.tv" || r2.prefix != "" {
		t.Errorf("r2 = %+v", r2)
	}
}

func TestParseConfigRoutes_NoRoutesKey(t *testing.T) {
	routes := parseConfigRoutes(map[string]interface{}{})
	if len(routes) != 0 {
		t.Errorf("expected empty routes, got %d", len(routes))
	}
}

// ---------- routeTable.match ----------

// buildTestTable creates a routeTable for testing with the given routes.
func buildTestTable(routes map[string]parsedRoute) *routeTable {
	return buildRouteTable(routes, false)
}

func TestRouteTable_PathMatch(t *testing.T) {
	table := buildTestTable(map[string]parsedRoute{
		"demo.tv/tltv": {hostname: "demo.tv", prefix: "/tltv", backends: []string{"relay:8000"}},
		"demo.tv":      {hostname: "demo.tv", prefix: "", backends: []string{"phosphor:80"}},
	})

	entry, backend := table.match("demo.tv", "/tltv/v1/channels/foo")
	if entry == nil || backend == nil {
		t.Fatal("expected match for /tltv/v1/channels/foo")
	}
	if entry.prefix != "/tltv" {
		t.Errorf("matched prefix = %q, want /tltv", entry.prefix)
	}
	if backend.address != "relay:8000" {
		t.Errorf("backend = %q, want relay:8000", backend.address)
	}
}

func TestRouteTable_CatchAll(t *testing.T) {
	table := buildTestTable(map[string]parsedRoute{
		"demo.tv/tltv": {hostname: "demo.tv", prefix: "/tltv", backends: []string{"relay:8000"}},
		"demo.tv":      {hostname: "demo.tv", prefix: "", backends: []string{"phosphor:80"}},
	})

	entry, backend := table.match("demo.tv", "/styles.css")
	if entry == nil || backend == nil {
		t.Fatal("expected catch-all match for /styles.css")
	}
	if entry.prefix != "" {
		t.Errorf("matched prefix = %q, want empty (catch-all)", entry.prefix)
	}
	if backend.address != "phosphor:80" {
		t.Errorf("backend = %q, want phosphor:80", backend.address)
	}
}

func TestRouteTable_LongestPrefixWins(t *testing.T) {
	table := buildTestTable(map[string]parsedRoute{
		"demo.tv/tltv":          {hostname: "demo.tv", prefix: "/tltv", backends: []string{"relay:8000"}},
		"demo.tv/tltv/v1/peers": {hostname: "demo.tv", prefix: "/tltv/v1/peers", backends: []string{"peers:9000"}},
		"demo.tv":               {hostname: "demo.tv", prefix: "", backends: []string{"phosphor:80"}},
	})

	// /tltv/v1/peers should match the longer prefix route.
	entry, backend := table.match("demo.tv", "/tltv/v1/peers")
	if entry == nil || backend == nil {
		t.Fatal("expected match")
	}
	if entry.prefix != "/tltv/v1/peers" {
		t.Errorf("matched prefix = %q, want /tltv/v1/peers", entry.prefix)
	}
	if backend.address != "peers:9000" {
		t.Errorf("backend = %q, want peers:9000", backend.address)
	}

	// /tltv/v1/channels should match /tltv (not /tltv/v1/peers).
	entry2, _ := table.match("demo.tv", "/tltv/v1/channels/abc")
	if entry2 == nil {
		t.Fatal("expected match")
	}
	if entry2.prefix != "/tltv" {
		t.Errorf("matched prefix = %q, want /tltv", entry2.prefix)
	}
}

func TestRouteTable_PrefixBoundary(t *testing.T) {
	table := buildTestTable(map[string]parsedRoute{
		"demo.tv/tltv": {hostname: "demo.tv", prefix: "/tltv", backends: []string{"relay:8000"}},
		"demo.tv":      {hostname: "demo.tv", prefix: "", backends: []string{"phosphor:80"}},
	})

	// /tltv-admin must NOT match /tltv (boundary check).
	entry, backend := table.match("demo.tv", "/tltv-admin")
	if entry == nil || backend == nil {
		t.Fatal("expected catch-all match")
	}
	if entry.prefix != "" {
		t.Errorf("matched prefix = %q, want empty (catch-all); /tltv-admin should not match /tltv", entry.prefix)
	}
}

func TestRouteTable_ExactPrefix(t *testing.T) {
	table := buildTestTable(map[string]parsedRoute{
		"demo.tv/tltv": {hostname: "demo.tv", prefix: "/tltv", backends: []string{"relay:8000"}},
		"demo.tv":      {hostname: "demo.tv", prefix: "", backends: []string{"phosphor:80"}},
	})

	// Exact path /tltv (no trailing slash) matches /tltv route.
	entry, backend := table.match("demo.tv", "/tltv")
	if entry == nil || backend == nil {
		t.Fatal("expected match for exact /tltv")
	}
	if entry.prefix != "/tltv" {
		t.Errorf("matched prefix = %q, want /tltv", entry.prefix)
	}
}

func TestRouteTable_NoPathRoutes(t *testing.T) {
	table := buildTestTable(map[string]parsedRoute{
		"demo.tv": {hostname: "demo.tv", prefix: "", backends: []string{"backend:80"}},
	})

	entry, backend := table.match("demo.tv", "/anything/goes")
	if entry == nil || backend == nil {
		t.Fatal("expected catch-all match")
	}
	if backend.address != "backend:80" {
		t.Errorf("backend = %q, want backend:80", backend.address)
	}
}

func TestRouteTable_UnknownHost(t *testing.T) {
	table := buildTestTable(map[string]parsedRoute{
		"demo.tv": {hostname: "demo.tv", prefix: "", backends: []string{"backend:80"}},
	})

	entry, _ := table.match("unknown.tv", "/")
	if entry != nil {
		t.Error("expected no match for unknown host")
	}
}

func TestRouteTable_MixedHosts(t *testing.T) {
	table := buildTestTable(map[string]parsedRoute{
		"demo.tv/tltv": {hostname: "demo.tv", prefix: "/tltv", backends: []string{"relay:8000"}},
		"demo.tv":      {hostname: "demo.tv", prefix: "", backends: []string{"phosphor:80"}},
		"other.tv":     {hostname: "other.tv", prefix: "", backends: []string{"other:80"}},
	})

	// demo.tv/tltv/... → relay:8000
	_, b1 := table.match("demo.tv", "/tltv/v1/foo")
	if b1 == nil || b1.address != "relay:8000" {
		t.Error("expected relay:8000")
	}

	// demo.tv/other → phosphor:80
	_, b2 := table.match("demo.tv", "/other")
	if b2 == nil || b2.address != "phosphor:80" {
		t.Error("expected phosphor:80")
	}

	// other.tv/anything → other:80
	_, b3 := table.match("other.tv", "/anything")
	if b3 == nil || b3.address != "other:80" {
		t.Error("expected other:80")
	}
}

// ---------- pickBackend (round-robin) ----------

func TestPickBackend_Single(t *testing.T) {
	e := &routeEntry{
		backends: []*routeBackend{
			{address: "a:80"},
		},
	}
	e.backends[0].healthy.Store(true)

	b := e.pickBackend()
	if b == nil || b.address != "a:80" {
		t.Errorf("expected a:80, got %v", b)
	}
}

func TestPickBackend_SingleUnhealthy(t *testing.T) {
	e := &routeEntry{
		backends: []*routeBackend{
			{address: "a:80"},
		},
	}
	e.backends[0].healthy.Store(false)

	if b := e.pickBackend(); b != nil {
		t.Errorf("expected nil for unhealthy backend, got %v", b.address)
	}
}

func TestPickBackend_RoundRobin(t *testing.T) {
	b1 := &routeBackend{address: "a:80"}
	b2 := &routeBackend{address: "b:80"}
	b1.healthy.Store(true)
	b2.healthy.Store(true)

	e := &routeEntry{backends: []*routeBackend{b1, b2}}

	// Should alternate between backends.
	got := make(map[string]int)
	for i := 0; i < 10; i++ {
		b := e.pickBackend()
		if b == nil {
			t.Fatal("unexpected nil backend")
		}
		got[b.address]++
	}
	if got["a:80"] != 5 || got["b:80"] != 5 {
		t.Errorf("round-robin distribution = %v, want 5/5", got)
	}
}

func TestPickBackend_SkipUnhealthy(t *testing.T) {
	b1 := &routeBackend{address: "a:80"}
	b2 := &routeBackend{address: "b:80"}
	b1.healthy.Store(false) // down
	b2.healthy.Store(true)

	e := &routeEntry{backends: []*routeBackend{b1, b2}}

	for i := 0; i < 5; i++ {
		b := e.pickBackend()
		if b == nil {
			t.Fatal("unexpected nil backend")
		}
		if b.address != "b:80" {
			t.Errorf("expected b:80 (a:80 is down), got %s", b.address)
		}
	}
}

func TestPickBackend_AllDown(t *testing.T) {
	b1 := &routeBackend{address: "a:80"}
	b2 := &routeBackend{address: "b:80"}
	b1.healthy.Store(false)
	b2.healthy.Store(false)

	e := &routeEntry{backends: []*routeBackend{b1, b2}}

	if b := e.pickBackend(); b != nil {
		t.Errorf("expected nil when all backends down, got %s", b.address)
	}
}

func TestPickBackend_Recovery(t *testing.T) {
	b1 := &routeBackend{address: "a:80"}
	b2 := &routeBackend{address: "b:80"}
	b1.healthy.Store(false)
	b2.healthy.Store(true)

	e := &routeEntry{backends: []*routeBackend{b1, b2}}

	// All traffic to b while a is down.
	b := e.pickBackend()
	if b.address != "b:80" {
		t.Errorf("expected b:80, got %s", b.address)
	}

	// Recover a.
	b1.healthy.Store(true)

	// Both should now participate.
	got := make(map[string]int)
	for i := 0; i < 10; i++ {
		bb := e.pickBackend()
		if bb == nil {
			t.Fatal("unexpected nil")
		}
		got[bb.address]++
	}
	if got["a:80"] == 0 || got["b:80"] == 0 {
		t.Errorf("after recovery, expected both backends to receive traffic: %v", got)
	}
}

// ---------- routerHandler ----------

// newTestHandler creates a handler backed by a route table for testing.
func newTestHandler(table *routeTable) http.Handler {
	var tablePtr atomic.Pointer[routeTable]
	tablePtr.Store(table)
	return routerHandler(&tablePtr)
}

func TestRouterHandler_ValidRoute(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Backend", "test")
		w.WriteHeader(200)
		w.Write([]byte("hello from backend"))
	}))
	defer backend.Close()

	backendAddr := backend.Listener.Addr().String()
	table := buildTestTable(map[string]parsedRoute{
		"demo.tv": {hostname: "demo.tv", prefix: "", backends: []string{backendAddr}},
	})
	handler := newTestHandler(table)

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
	table := buildTestTable(map[string]parsedRoute{})
	handler := newTestHandler(table)

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
	table := buildTestTable(map[string]parsedRoute{
		"demo.tv": {hostname: "demo.tv", prefix: "", backends: []string{backendAddr}},
	})
	handler := newTestHandler(table)

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
	table := buildTestTable(map[string]parsedRoute{
		"demo.tv": {hostname: "demo.tv", prefix: "", backends: []string{backendAddr}},
	})
	handler := newTestHandler(table)

	req := httptest.NewRequest("GET", "/", nil)
	req.Host = "DEMO.TV"
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Errorf("status = %d, want 200 (case-insensitive host lookup)", w.Code)
	}
}

func TestRouterHandler_BackendDown(t *testing.T) {
	table := buildTestTable(map[string]parsedRoute{
		"demo.tv": {hostname: "demo.tv", prefix: "", backends: []string{"localhost:1"}},
	})
	// Mark backend as unhealthy.
	for _, entries := range table.byHost {
		for _, entry := range entries {
			for _, b := range entry.backends {
				b.healthy.Store(false)
			}
		}
	}
	handler := newTestHandler(table)

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

func TestRouterHandler_PathPrefixDispatch(t *testing.T) {
	relayBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		w.Write([]byte("relay"))
	}))
	defer relayBackend.Close()

	phosphorBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		w.Write([]byte("phosphor"))
	}))
	defer phosphorBackend.Close()

	table := buildTestTable(map[string]parsedRoute{
		"demo.tv/tltv": {hostname: "demo.tv", prefix: "/tltv", backends: []string{relayBackend.Listener.Addr().String()}},
		"demo.tv":      {hostname: "demo.tv", prefix: "", backends: []string{phosphorBackend.Listener.Addr().String()}},
	})
	handler := newTestHandler(table)

	// Protocol path → relay.
	req1 := httptest.NewRequest("GET", "/tltv/v1/channels/abc", nil)
	req1.Host = "demo.tv"
	w1 := httptest.NewRecorder()
	handler.ServeHTTP(w1, req1)
	if w1.Body.String() != "relay" {
		t.Errorf("protocol path: body = %q, want %q", w1.Body.String(), "relay")
	}

	// Static file → phosphor.
	req2 := httptest.NewRequest("GET", "/styles.css", nil)
	req2.Host = "demo.tv"
	w2 := httptest.NewRecorder()
	handler.ServeHTTP(w2, req2)
	if w2.Body.String() != "phosphor" {
		t.Errorf("static file: body = %q, want %q", w2.Body.String(), "phosphor")
	}
}

func TestRouterHandler_RoundRobinDispatch(t *testing.T) {
	b1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("b1"))
	}))
	defer b1.Close()

	b2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("b2"))
	}))
	defer b2.Close()

	table := buildTestTable(map[string]parsedRoute{
		"lb.tv": {hostname: "lb.tv", prefix: "", backends: []string{
			b1.Listener.Addr().String(),
			b2.Listener.Addr().String(),
		}},
	})
	handler := newTestHandler(table)

	got := make(map[string]int)
	for i := 0; i < 10; i++ {
		req := httptest.NewRequest("GET", "/", nil)
		req.Host = "lb.tv"
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		got[w.Body.String()]++
	}

	if got["b1"] != 5 || got["b2"] != 5 {
		t.Errorf("round-robin distribution = %v, want 5/5", got)
	}
}

func TestRouterHandler_AtomicTableSwap(t *testing.T) {
	// Start with backend A.
	backendA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("A"))
	}))
	defer backendA.Close()

	backendB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("B"))
	}))
	defer backendB.Close()

	tableA := buildTestTable(map[string]parsedRoute{
		"demo.tv": {hostname: "demo.tv", prefix: "", backends: []string{backendA.Listener.Addr().String()}},
	})
	var tablePtr atomic.Pointer[routeTable]
	tablePtr.Store(tableA)
	handler := routerHandler(&tablePtr)

	// Request goes to A.
	req1 := httptest.NewRequest("GET", "/", nil)
	req1.Host = "demo.tv"
	w1 := httptest.NewRecorder()
	handler.ServeHTTP(w1, req1)
	if w1.Body.String() != "A" {
		t.Errorf("before swap: body = %q, want %q", w1.Body.String(), "A")
	}

	// Swap to table B.
	tableB := buildTestTable(map[string]parsedRoute{
		"demo.tv": {hostname: "demo.tv", prefix: "", backends: []string{backendB.Listener.Addr().String()}},
	})
	tablePtr.Store(tableB)

	// Request goes to B.
	req2 := httptest.NewRequest("GET", "/", nil)
	req2.Host = "demo.tv"
	w2 := httptest.NewRecorder()
	handler.ServeHTTP(w2, req2)
	if w2.Body.String() != "B" {
		t.Errorf("after swap: body = %q, want %q", w2.Body.String(), "B")
	}
}

func TestRouterHandler_ReloadRemovesRoute(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	}))
	defer backend.Close()

	table1 := buildTestTable(map[string]parsedRoute{
		"demo.tv": {hostname: "demo.tv", prefix: "", backends: []string{backend.Listener.Addr().String()}},
		"old.tv":  {hostname: "old.tv", prefix: "", backends: []string{backend.Listener.Addr().String()}},
	})
	var tablePtr atomic.Pointer[routeTable]
	tablePtr.Store(table1)
	handler := routerHandler(&tablePtr)

	// old.tv works.
	req1 := httptest.NewRequest("GET", "/", nil)
	req1.Host = "old.tv"
	w1 := httptest.NewRecorder()
	handler.ServeHTTP(w1, req1)
	if w1.Code != 200 {
		t.Errorf("before reload: old.tv status = %d, want 200", w1.Code)
	}

	// Remove old.tv.
	table2 := buildTestTable(map[string]parsedRoute{
		"demo.tv": {hostname: "demo.tv", prefix: "", backends: []string{backend.Listener.Addr().String()}},
	})
	tablePtr.Store(table2)

	// old.tv returns 502.
	req2 := httptest.NewRequest("GET", "/", nil)
	req2.Host = "old.tv"
	w2 := httptest.NewRecorder()
	handler.ServeHTTP(w2, req2)
	if w2.Code != http.StatusBadGateway {
		t.Errorf("after reload: old.tv status = %d, want %d", w2.Code, http.StatusBadGateway)
	}
}

func TestRouterHandler_ReloadAddsPathRoute(t *testing.T) {
	backendA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("catch-all"))
	}))
	defer backendA.Close()

	backendB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("prefix"))
	}))
	defer backendB.Close()

	// Start with catch-all only.
	table1 := buildTestTable(map[string]parsedRoute{
		"demo.tv": {hostname: "demo.tv", prefix: "", backends: []string{backendA.Listener.Addr().String()}},
	})
	var tablePtr atomic.Pointer[routeTable]
	tablePtr.Store(table1)
	handler := routerHandler(&tablePtr)

	req1 := httptest.NewRequest("GET", "/tltv/foo", nil)
	req1.Host = "demo.tv"
	w1 := httptest.NewRecorder()
	handler.ServeHTTP(w1, req1)
	if w1.Body.String() != "catch-all" {
		t.Errorf("before reload: /tltv/foo → %q, want catch-all", w1.Body.String())
	}

	// Add path-prefix route.
	table2 := buildTestTable(map[string]parsedRoute{
		"demo.tv/tltv": {hostname: "demo.tv", prefix: "/tltv", backends: []string{backendB.Listener.Addr().String()}},
		"demo.tv":      {hostname: "demo.tv", prefix: "", backends: []string{backendA.Listener.Addr().String()}},
	})
	tablePtr.Store(table2)

	req2 := httptest.NewRequest("GET", "/tltv/foo", nil)
	req2.Host = "demo.tv"
	w2 := httptest.NewRecorder()
	handler.ServeHTTP(w2, req2)
	if w2.Body.String() != "prefix" {
		t.Errorf("after reload: /tltv/foo → %q, want prefix", w2.Body.String())
	}
}

// ---------- routerBackendHealthCheck ----------

func TestRouterBackendHealthCheck_Healthy(t *testing.T) {
	setupLogging("error", "", "", "test")

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			w.WriteHeader(200)
			return
		}
		w.WriteHeader(404)
	}))
	defer backend.Close()

	b := &routeBackend{address: backend.Listener.Addr().String()}
	b.healthy.Store(true)

	client := &http.Client{}
	routerBackendHealthCheck(client, backend.URL+"/health", b, "test")

	if !b.healthy.Load() {
		t.Error("expected healthy=true after 200 response")
	}
}

func TestRouterBackendHealthCheck_Unhealthy(t *testing.T) {
	setupLogging("error", "", "", "test")

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(503)
	}))
	defer backend.Close()

	b := &routeBackend{address: backend.Listener.Addr().String()}
	b.healthy.Store(true)

	client := &http.Client{}
	routerBackendHealthCheck(client, backend.URL+"/health", b, "test")

	if b.healthy.Load() {
		t.Error("expected healthy=false after 503 response")
	}
}

func TestRouterBackendHealthCheck_Recovery(t *testing.T) {
	setupLogging("error", "", "", "test")

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	}))
	defer backend.Close()

	b := &routeBackend{address: backend.Listener.Addr().String()}
	b.healthy.Store(false) // Start unhealthy

	client := &http.Client{}
	routerBackendHealthCheck(client, backend.URL+"/health", b, "test")

	if !b.healthy.Load() {
		t.Error("expected healthy=true after recovery (200 response)")
	}
}

func TestRouterBackendHealthCheck_ConnectionRefused(t *testing.T) {
	setupLogging("error", "", "", "test")

	b := &routeBackend{address: "127.0.0.1:1"}
	b.healthy.Store(true)

	client := &http.Client{}
	routerBackendHealthCheck(client, "http://127.0.0.1:1/health", b, "test")

	if b.healthy.Load() {
		t.Error("expected healthy=false when backend is unreachable")
	}
}

// ---------- buildReverseProxy ----------

func TestBuildReverseProxy_HeadersHTTPS(t *testing.T) {
	var gotProto, gotRealIP string
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotProto = r.Header.Get("X-Forwarded-Proto")
		gotRealIP = r.Header.Get("X-Real-IP")
		w.WriteHeader(200)
	}))
	defer backend.Close()

	backendAddr := backend.Listener.Addr().String()
	proxy := buildReverseProxy(backendAddr, true) // TLS enabled

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

func TestBuildReverseProxy_HeadersHTTP(t *testing.T) {
	var gotProto string
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotProto = r.Header.Get("X-Forwarded-Proto")
		w.WriteHeader(200)
	}))
	defer backend.Close()

	backendAddr := backend.Listener.Addr().String()
	proxy := buildReverseProxy(backendAddr, false) // TLS disabled

	req := httptest.NewRequest("GET", "/test", nil)
	req.RemoteAddr = "203.0.113.42:12345"
	w := httptest.NewRecorder()
	proxy.ServeHTTP(w, req)

	if gotProto != "http" {
		t.Errorf("X-Forwarded-Proto = %q, want %q (no TLS)", gotProto, "http")
	}
}

// ---------- dumpRouterConfig ----------

func TestDumpRouterConfig_SingleBackend(t *testing.T) {
	routes := map[string]parsedRoute{
		"demo.tv": {hostname: "demo.tv", prefix: "", backends: []string{"relay:8000"}},
	}
	cfg := dumpRouterConfig(routes, ":443", ":80", "", "30s")

	rm, ok := cfg["routes"].(map[string]interface{})
	if !ok {
		t.Fatal("expected routes map")
	}
	if v, ok := rm["demo.tv"].(string); !ok || v != "relay:8000" {
		t.Errorf("routes[demo.tv] = %v, want string relay:8000", rm["demo.tv"])
	}
}

func TestDumpRouterConfig_MultiBackend(t *testing.T) {
	routes := map[string]parsedRoute{
		"lb.tv": {hostname: "lb.tv", prefix: "", backends: []string{"b1:80", "b2:80"}},
	}
	cfg := dumpRouterConfig(routes, ":443", ":80", "", "30s")

	rm := cfg["routes"].(map[string]interface{})
	arr, ok := rm["lb.tv"].([]interface{})
	if !ok {
		t.Fatalf("routes[lb.tv] = %T, want []interface{}", rm["lb.tv"])
	}
	if len(arr) != 2 {
		t.Errorf("len = %d, want 2", len(arr))
	}
}

func TestDumpRouterConfig_PathPrefix(t *testing.T) {
	routes := map[string]parsedRoute{
		"demo.tv/tltv": {hostname: "demo.tv", prefix: "/tltv", backends: []string{"relay:8000"}},
	}
	cfg := dumpRouterConfig(routes, ":443", ":80", "", "30s")

	rm := cfg["routes"].(map[string]interface{})
	if _, ok := rm["demo.tv/tltv"]; !ok {
		t.Error("expected key demo.tv/tltv in routes map")
	}
}

func TestDumpRouterConfig_OmitsDefaults(t *testing.T) {
	routes := map[string]parsedRoute{
		"demo.tv": {hostname: "demo.tv", prefix: "", backends: []string{"relay:8000"}},
	}
	cfg := dumpRouterConfig(routes, ":443", ":80", "", "30s")

	if _, ok := cfg["listen"]; ok {
		t.Error("listen should be omitted when default")
	}
	if _, ok := cfg["http_listen"]; ok {
		t.Error("http_listen should be omitted when default")
	}
	if _, ok := cfg["health_check"]; ok {
		t.Error("health_check should be omitted when empty")
	}
}

func TestDumpRouterConfig_IncludesNonDefaults(t *testing.T) {
	routes := map[string]parsedRoute{}
	cfg := dumpRouterConfig(routes, ":8443", ":8080", "/health", "10s")

	if cfg["listen"] != ":8443" {
		t.Errorf("listen = %v, want :8443", cfg["listen"])
	}
	if cfg["http_listen"] != ":8080" {
		t.Errorf("http_listen = %v, want :8080", cfg["http_listen"])
	}
	if cfg["health_check"] != "/health" {
		t.Errorf("health_check = %v, want /health", cfg["health_check"])
	}
	if cfg["health_interval"] != "10s" {
		t.Errorf("health_interval = %v, want 10s", cfg["health_interval"])
	}
}

// ---------- routeKey ----------

func TestRouteKey_NoPrefix(t *testing.T) {
	if k := routeKey("demo.tv", ""); k != "demo.tv" {
		t.Errorf("routeKey = %q, want demo.tv", k)
	}
}

func TestRouteKey_WithPrefix(t *testing.T) {
	if k := routeKey("demo.tv", "/tltv"); k != "demo.tv/tltv" {
		t.Errorf("routeKey = %q, want demo.tv/tltv", k)
	}
}
