package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// ---------- Test Helpers ----------

// testRelayUpstream creates a mock upstream TLTV node serving one signed channel.
// Returns the test server, channel ID, and private key.
func testRelayUpstream(t *testing.T) (*httptest.Server, string, ed25519.PrivateKey) {
	t.Helper()

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	channelID := makeChannelID(pub)

	now := time.Now().UTC()

	// Build and sign metadata
	meta := map[string]interface{}{
		"v":       json.Number("1"),
		"seq":     json.Number(fmt.Sprintf("%d", now.Unix())),
		"id":      channelID,
		"name":    "Upstream Channel",
		"stream":  "/tltv/v1/channels/" + channelID + "/stream.m3u8",
		"access":  "public",
		"status":  "active",
		"updated": now.Format(timestampFormat),
	}
	signedMeta, _ := signDocument(meta, priv)
	metaBytes, _ := json.Marshal(signedMeta)

	// Build and sign guide
	from := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	until := from.Add(24 * time.Hour)
	guide := map[string]interface{}{
		"v":   json.Number("1"),
		"seq": json.Number(fmt.Sprintf("%d", now.Unix())),
		"id":  channelID,
		"from":  from.Format(timestampFormat),
		"until": until.Format(timestampFormat),
		"entries": []interface{}{
			map[string]interface{}{
				"start": from.Format(timestampFormat),
				"end":   until.Format(timestampFormat),
				"title": "Test Show",
			},
		},
		"updated": now.Format(timestampFormat),
	}
	signedGuide, _ := signDocument(guide, priv)
	guideBytes, _ := json.Marshal(signedGuide)

	// Manifest
	manifest := "#EXTM3U\n#EXT-X-TARGETDURATION:2\n#EXTINF:2.0,\nseg-000.ts\n"

	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/tltv", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"protocol": "tltv",
			"versions": []int{1},
			"channels": []map[string]string{{"id": channelID, "name": "Upstream Channel"}},
			"relaying": []interface{}{},
		})
	})
	mux.HandleFunc("GET /tltv/v1/channels/"+channelID, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Write(metaBytes)
	})
	mux.HandleFunc("GET /tltv/v1/channels/"+channelID+"/guide.json", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Write(guideBytes)
	})
	mux.HandleFunc("GET /tltv/v1/channels/"+channelID+"/stream.m3u8", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		w.Write([]byte(manifest))
	})
	mux.HandleFunc("GET /tltv/v1/channels/"+channelID+"/seg-000.ts", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "video/mp2t")
		w.Write([]byte("fake-ts-data"))
	})
	mux.HandleFunc("GET /tltv/v1/peers", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		json.NewEncoder(w).Encode(map[string]interface{}{"peers": []interface{}{}})
	})

	ts := httptest.NewServer(mux)
	return ts, channelID, priv
}

// hostFromURL extracts host:port from a test server URL.
func hostFromURL(rawURL string) string {
	// "http://127.0.0.1:PORT" -> "127.0.0.1:PORT"
	s := strings.TrimPrefix(rawURL, "http://")
	s = strings.TrimPrefix(s, "https://")
	return s
}

// ---------- Upstream Fetch + Verify ----------

func TestRelayFetchAndVerifyMetadata(t *testing.T) {
	upstream, channelID, _ := testRelayUpstream(t)
	defer upstream.Close()

	client := newClient(false)
	res, err := relayFetchAndVerifyMetadata(client, channelID, []string{hostFromURL(upstream.URL)})
	if err != nil {
		t.Fatal(err)
	}

	if res.IsMigration {
		t.Error("should not be a migration")
	}
	if getString(res.Doc, "name") != "Upstream Channel" {
		t.Errorf("name = %v", res.Doc["name"])
	}
	if len(res.Raw) == 0 {
		t.Error("raw bytes should be non-empty")
	}
}

func TestRelayFetchAndVerifyMetadata_BadSignature(t *testing.T) {
	// Create a server that returns metadata signed by the wrong key
	_, wrongPriv, _ := ed25519.GenerateKey(rand.Reader)
	pub2, _, _ := ed25519.GenerateKey(rand.Reader)
	channelID := makeChannelID(pub2)

	now := time.Now().UTC()
	meta := map[string]interface{}{
		"v":       json.Number("1"),
		"seq":     json.Number(fmt.Sprintf("%d", now.Unix())),
		"id":      channelID,
		"name":    "Bad Channel",
		"stream":  "/tltv/v1/channels/" + channelID + "/stream.m3u8",
		"access":  "public",
		"status":  "active",
		"updated": now.Format(timestampFormat),
	}
	signed, _ := signDocument(meta, wrongPriv) // signed by wrong key
	metaBytes, _ := json.Marshal(signed)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(metaBytes)
	}))
	defer ts.Close()

	client := newClient(false)
	_, err := relayFetchAndVerifyMetadata(client, channelID, []string{hostFromURL(ts.URL)})
	if err == nil {
		t.Error("should fail with bad signature")
	}
	if !strings.Contains(err.Error(), "verification failed") {
		t.Errorf("error = %v, want verification failure", err)
	}
}

func TestRelayFetchAndVerifyGuide(t *testing.T) {
	upstream, channelID, _ := testRelayUpstream(t)
	defer upstream.Close()

	client := newClient(false)
	raw, entries, err := relayFetchAndVerifyGuide(client, channelID, []string{hostFromURL(upstream.URL)})
	if err != nil {
		t.Fatal(err)
	}
	if raw == nil {
		t.Error("raw guide bytes should be non-nil")
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 guide entry, got %d", len(entries))
	}
	if entries[0].Title != "Test Show" {
		t.Errorf("entry title = %q", entries[0].Title)
	}
}

// ---------- Access Checks ----------

func TestRelayCheckAccess_Public(t *testing.T) {
	doc := map[string]interface{}{"access": "public", "status": "active"}
	if err := relayCheckAccess(doc); err != nil {
		t.Errorf("public channel should be allowed: %v", err)
	}
}

func TestRelayCheckAccess_Private(t *testing.T) {
	doc := map[string]interface{}{"access": "token", "status": "active"}
	if err := relayCheckAccess(doc); err == nil {
		t.Error("private channel should be rejected")
	}
}

func TestRelayCheckAccess_OnDemand(t *testing.T) {
	doc := map[string]interface{}{"access": "public", "on_demand": true}
	if err := relayCheckAccess(doc); err == nil {
		t.Error("on-demand channel should be rejected")
	}
}

func TestRelayCheckAccess_Retired(t *testing.T) {
	doc := map[string]interface{}{"access": "public", "status": "retired"}
	if err := relayCheckAccess(doc); err == nil {
		t.Error("retired channel should be rejected")
	}
}

// ---------- Migration ----------

func TestRelayIsMigration(t *testing.T) {
	if relayIsMigration(map[string]interface{}{"name": "test"}) {
		t.Error("regular metadata should not be migration")
	}
	if !relayIsMigration(map[string]interface{}{"type": "migration"}) {
		t.Error("migration doc should be detected")
	}
}

// ---------- Config File ----------

func TestRelayLoadConfig(t *testing.T) {
	f := t.TempDir() + "/relay.json"
	os.WriteFile(f, []byte(`{
		"channels": ["tltv://TVabc@origin.example.com:443"],
		"nodes": ["origin.example.com:443", "backup.example.com:443"]
	}`), 0644)

	cfg, err := relayLoadConfig(f)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Channels) != 1 {
		t.Errorf("channels: %d", len(cfg.Channels))
	}
	if len(cfg.Nodes) != 2 {
		t.Errorf("nodes: %d", len(cfg.Nodes))
	}
}

// ---------- Target Discovery ----------

func TestRelayDiscoverTargets_FromNode(t *testing.T) {
	upstream, channelID, _ := testRelayUpstream(t)
	defer upstream.Close()

	client := newClient(false)
	targets, err := relayDiscoverTargets(client, nil, []string{hostFromURL(upstream.URL)})
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 1 {
		t.Fatalf("expected 1 target, got %d", len(targets))
	}
	if targets[0].ChannelID != channelID {
		t.Errorf("channelID = %q, want %q", targets[0].ChannelID, channelID)
	}
}

// ---------- Registry ----------

func TestRelayRegistry_UpdateAndGet(t *testing.T) {
	r := newRelayRegistry("", nil, 100, 7)

	doc := map[string]interface{}{"name": "Test Channel"}
	raw := []byte(`{"name":"Test Channel"}`)

	r.UpdateChannel("TVtest123", raw, doc, []string{"host:443"})

	ch := r.GetChannel("TVtest123")
	if ch == nil {
		t.Fatal("channel should exist")
	}
	if ch.Name != "Test Channel" {
		t.Errorf("name = %q", ch.Name)
	}
	if string(ch.Metadata) != string(raw) {
		t.Error("raw bytes should be preserved exactly")
	}
}

func TestRelayRegistry_RemoveChannel(t *testing.T) {
	r := newRelayRegistry("", nil, 100, 7)
	r.UpdateChannel("TVtest123", []byte(`{}`), map[string]interface{}{"name": "x"}, nil)
	r.RemoveChannel("TVtest123")
	if r.GetChannel("TVtest123") != nil {
		t.Error("channel should be removed")
	}
}

func TestRelayRegistry_GuideUpdate(t *testing.T) {
	r := newRelayRegistry("", nil, 100, 7)
	r.UpdateChannel("TVtest123", []byte(`{}`), map[string]interface{}{"name": "x"}, nil)

	entries := []bridgeGuideEntry{{Start: "2026-01-01T00:00:00Z", End: "2026-01-02T00:00:00Z", Title: "Show"}}
	r.UpdateGuide("TVtest123", []byte(`{"entries":[]}`), entries)

	ch := r.GetChannel("TVtest123")
	if len(ch.GuideEntries) != 1 {
		t.Errorf("guide entries: %d", len(ch.GuideEntries))
	}
}

func TestRelayRegistry_ListPeers(t *testing.T) {
	r := newRelayRegistry("relay.example.com:443", []string{"relay.example.com:443"}, 100, 7)
	r.UpdateChannel("TVtest123", []byte(`{}`), map[string]interface{}{"name": "Test"}, nil)

	peers := r.ListPeers()
	if len(peers) != 1 {
		t.Fatalf("expected 1 peer, got %d", len(peers))
	}
	if peers[0].ChannelID != "TVtest123" {
		t.Errorf("peer channel = %q", peers[0].ChannelID)
	}
	if len(peers[0].Hints) != 1 || peers[0].Hints[0] != "relay.example.com:443" {
		t.Errorf("peer hints = %v", peers[0].Hints)
	}
}

// ---------- Relay Server Endpoints ----------

func TestRelayServerNodeInfo(t *testing.T) {
	r := newRelayRegistry("", nil, 100, 7)
	r.UpdateChannel("TVtest123", []byte(`{}`), map[string]interface{}{"name": "Test"}, nil)
	srv := newRelayServer(r, newClient(false), nil)

	req := httptest.NewRequest("GET", "/.well-known/tltv", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}

	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)

	// Relay channels should be in "relaying", not "channels"
	channels := resp["channels"].([]interface{})
	if len(channels) != 0 {
		t.Errorf("channels should be empty for relay, got %d", len(channels))
	}

	relaying := resp["relaying"].([]interface{})
	if len(relaying) != 1 {
		t.Fatalf("expected 1 relaying, got %d", len(relaying))
	}
	entry := relaying[0].(map[string]interface{})
	if entry["name"] != "Test" {
		t.Errorf("relaying name = %v", entry["name"])
	}
}

func TestRelayServerMetadata_RawBytesPreserved(t *testing.T) {
	// Create metadata with an unknown field -- relay must preserve it
	rawMeta := `{"v":1,"id":"TVtest123","name":"Test","unknown_field":"preserve_me","signature":"abc"}`

	r := newRelayRegistry("", nil, 100, 7)
	r.UpdateChannel("TVtest123", []byte(rawMeta), map[string]interface{}{"name": "Test"}, nil)
	srv := newRelayServer(r, newClient(false), nil)

	req := httptest.NewRequest("GET", "/tltv/v1/channels/TVtest123", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}

	// The exact raw bytes should be returned, preserving unknown fields
	if w.Body.String() != rawMeta {
		t.Errorf("raw bytes not preserved:\ngot:  %s\nwant: %s", w.Body.String(), rawMeta)
	}
}

func TestRelayServerChannelNotFound(t *testing.T) {
	r := newRelayRegistry("", nil, 100, 7)
	srv := newRelayServer(r, newClient(false), nil)

	req := httptest.NewRequest("GET", "/tltv/v1/channels/TVnonexistent", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != 404 {
		t.Fatalf("status = %d, want 404", w.Code)
	}
}

func TestRelayServerHealth(t *testing.T) {
	r := newRelayRegistry("", nil, 100, 7)
	r.UpdateChannel("TVtest123", []byte(`{}`), map[string]interface{}{"name": "Test"}, nil)
	srv := newRelayServer(r, newClient(false), nil)

	req := httptest.NewRequest("GET", "/health", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)

	if resp["status"] != "ok" {
		t.Errorf("status = %v", resp["status"])
	}
	// Health shows "relaying" count, not "channels"
	if resp["relaying"] != float64(1) {
		t.Errorf("relaying = %v", resp["relaying"])
	}
}

func TestRelayServerCORS(t *testing.T) {
	r := newRelayRegistry("", nil, 100, 7)
	srv := newRelayServer(r, newClient(false), nil)

	req := httptest.NewRequest("GET", "/.well-known/tltv", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Error("missing CORS header")
	}
}

func TestRelayServerOPTIONS(t *testing.T) {
	r := newRelayRegistry("", nil, 100, 7)
	srv := newRelayServer(r, newClient(false), nil)

	req := httptest.NewRequest("OPTIONS", "/.well-known/tltv", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != 204 {
		t.Errorf("OPTIONS status = %d, want 204", w.Code)
	}
}

func TestRelayServerMethodNotAllowed(t *testing.T) {
	r := newRelayRegistry("", nil, 100, 7)
	srv := newRelayServer(r, newClient(false), nil)

	req := httptest.NewRequest("POST", "/.well-known/tltv", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != 400 {
		t.Errorf("POST status = %d, want 400", w.Code)
	}
}

// ---------- End-to-End: Relay from Mock Upstream ----------

func TestRelayEndToEnd_StreamProxy(t *testing.T) {
	upstream, channelID, _ := testRelayUpstream(t)
	defer upstream.Close()

	client := newClient(false)
	hint := hostFromURL(upstream.URL)

	// Fetch and verify metadata
	res, err := relayFetchAndVerifyMetadata(client, channelID, []string{hint})
	if err != nil {
		t.Fatal(err)
	}

	// Set up relay
	registry := newRelayRegistry("", nil, 100, 7)
	registry.UpdateChannel(channelID, res.Raw, res.Doc, []string{hint})

	srv := newRelayServer(registry, client, nil)

	// Fetch manifest through relay
	req := httptest.NewRequest("GET", "/tltv/v1/channels/"+channelID+"/stream.m3u8", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("manifest status = %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "#EXTM3U") {
		t.Error("manifest should contain #EXTM3U")
	}

	// Fetch segment through relay
	req = httptest.NewRequest("GET", "/tltv/v1/channels/"+channelID+"/seg-000.ts", nil)
	w = httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("segment status = %d", w.Code)
	}
	if w.Body.String() != "fake-ts-data" {
		t.Errorf("segment body = %q", w.Body.String())
	}
}

func TestRelayEndToEnd_GuideServing(t *testing.T) {
	upstream, channelID, _ := testRelayUpstream(t)
	defer upstream.Close()

	client := newClient(false)
	hint := hostFromURL(upstream.URL)

	res, err := relayFetchAndVerifyMetadata(client, channelID, []string{hint})
	if err != nil {
		t.Fatal(err)
	}
	raw, entries, err := relayFetchAndVerifyGuide(client, channelID, []string{hint})
	if err != nil {
		t.Fatal(err)
	}

	registry := newRelayRegistry("", nil, 100, 7)
	registry.UpdateChannel(channelID, res.Raw, res.Doc, []string{hint})
	registry.UpdateGuide(channelID, raw, entries)

	srv := newRelayServer(registry, client, nil)

	// guide.json -- served verbatim
	req := httptest.NewRequest("GET", "/tltv/v1/channels/"+channelID+"/guide.json", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("guide.json status = %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "Test Show") {
		t.Error("guide should contain Test Show")
	}

	// guide.xml -- generated from entries
	req = httptest.NewRequest("GET", "/tltv/v1/channels/"+channelID+"/guide.xml", nil)
	w = httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("guide.xml status = %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "<tv>") {
		t.Error("XMLTV should contain <tv>")
	}
	if !strings.Contains(w.Body.String(), "Test Show") {
		t.Error("XMLTV should contain Test Show")
	}
}

// ---------- Concurrent Access ----------

func TestRelayConcurrentAccess(t *testing.T) {
	r := newRelayRegistry("", nil, 100, 7)
	r.UpdateChannel("TVtest", []byte(`{"name":"test"}`), map[string]interface{}{"name": "test"}, nil)
	srv := newRelayServer(r, newClient(false), nil)

	var wg sync.WaitGroup

	// Concurrent writes
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			name := fmt.Sprintf("test-%d", i)
			r.UpdateChannel("TVtest", []byte(`{"name":"`+name+`"}`), map[string]interface{}{"name": name}, nil)
		}(i)
	}

	// Concurrent reads
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req := httptest.NewRequest("GET", "/.well-known/tltv", nil)
			w := httptest.NewRecorder()
			srv.ServeHTTP(w, req)
			if w.Code != 200 {
				t.Errorf("status = %d during concurrent access", w.Code)
			}
		}()
	}

	wg.Wait()
}

// ---------- Guide Extraction ----------

// ---------- Access State Transition ----------

func TestRelayRegistry_AccessTransition(t *testing.T) {
	r := newRelayRegistry("", nil, 100, 7)
	r.UpdateChannel("TVtest123", []byte(`{"name":"Test"}`), map[string]interface{}{"name": "Test"}, nil)
	srv := newRelayServer(r, newClient(false), nil)

	// Channel exists
	req := httptest.NewRequest("GET", "/tltv/v1/channels/TVtest123", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("before removal: status = %d", w.Code)
	}

	// Simulate access transition (relay removes channel when it goes private/retired)
	r.RemoveChannel("TVtest123")

	// Channel gone from metadata endpoint
	req = httptest.NewRequest("GET", "/tltv/v1/channels/TVtest123", nil)
	w = httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != 404 {
		t.Fatalf("after removal: status = %d, want 404", w.Code)
	}

	// Gone from node info
	req = httptest.NewRequest("GET", "/.well-known/tltv", nil)
	w = httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)

	relaying := resp["relaying"].([]interface{})
	if len(relaying) != 0 {
		t.Errorf("removed channel should not appear in relaying, got %d", len(relaying))
	}
}

func TestRelayExtractGuideEntries(t *testing.T) {
	doc := map[string]interface{}{
		"entries": []interface{}{
			map[string]interface{}{
				"start":       "2026-03-15T00:00:00Z",
				"end":         "2026-03-15T01:00:00Z",
				"title":       "Show A",
				"description": "A description",
				"category":    "news",
			},
			map[string]interface{}{
				"start": "2026-03-15T01:00:00Z",
				"end":   "2026-03-15T02:00:00Z",
				"title": "Show B",
			},
		},
	}

	entries := relayExtractGuideEntries(doc)
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}
	if entries[0].Title != "Show A" || entries[0].Description != "A description" || entries[0].Category != "news" {
		t.Errorf("entry 0: %+v", entries[0])
	}
	if entries[1].Title != "Show B" || entries[1].Description != "" {
		t.Errorf("entry 1: %+v", entries[1])
	}
}

// ---------- appendUnique ----------

func TestAppendUnique(t *testing.T) {
	s := []string{"a", "b"}
	s = appendUnique(s, "c")
	if len(s) != 3 || s[2] != "c" {
		t.Errorf("should append new: %v", s)
	}
	s = appendUnique(s, "b")
	if len(s) != 3 {
		t.Errorf("should not duplicate: %v", s)
	}
	s = appendUnique(s, "a")
	if len(s) != 3 {
		t.Errorf("should not duplicate first: %v", s)
	}
}

// ---------- StoreMigration ----------

func TestRelayStoreMigration(t *testing.T) {
	r := newRelayRegistry("", nil, 100, 7)
	r.UpdateChannel("TVold123", []byte(`{"name":"Old"}`), map[string]interface{}{"name": "Old"}, nil)

	// Store migration
	migDoc := []byte(`{"from":"TVold123","to":"TVnew456"}`)
	r.StoreMigration("TVold123", migDoc)

	// Channel still in registry but as migrated
	ch := r.GetChannel("TVold123")
	if ch == nil {
		t.Fatal("migrated channel should still be in registry")
	}
	if ch.Name != "(migrated)" {
		t.Errorf("name = %q, want (migrated)", ch.Name)
	}
	if string(ch.Metadata) != string(migDoc) {
		t.Error("migration doc should be served as metadata")
	}

	// Verify it serves through the HTTP endpoint
	srv := newRelayServer(r, newClient(false), nil)
	req := httptest.NewRequest("GET", "/tltv/v1/channels/TVold123", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("migration doc should be served, got %d", w.Code)
	}
}

// ---------- MergePeers ----------

func TestRelayMergePeers(t *testing.T) {
	r := newRelayRegistry("", nil, 100, 7)

	// Add peers
	r.MergePeers([]relayPeerInfo{
		{ChannelID: "TVa", Name: "A", Hints: []string{"a.example.com:443"}, LastSeen: time.Now()},
		{ChannelID: "TVb", Name: "B", Hints: []string{"b.example.com:443"}, LastSeen: time.Now()},
	})

	peers := r.ListPeers()
	if len(peers) != 2 {
		t.Fatalf("expected 2 peers, got %d", len(peers))
	}

	// Merge with overlap (should update, not duplicate)
	r.MergePeers([]relayPeerInfo{
		{ChannelID: "TVa", Name: "A Updated", Hints: []string{"a2.example.com:443"}, LastSeen: time.Now()},
	})
	peers = r.ListPeers()
	if len(peers) != 2 {
		t.Fatalf("should still have 2 peers after overlap, got %d", len(peers))
	}
}

func TestRelayMergePeers_Staleness(t *testing.T) {
	r := newRelayRegistry("", nil, 100, 1) // 1-day staleness

	staleTime := time.Now().Add(-48 * time.Hour)
	r.MergePeers([]relayPeerInfo{
		{ChannelID: "TVstale", Name: "Stale", Hints: []string{"old.com:443"}, LastSeen: staleTime},
		{ChannelID: "TVfresh", Name: "Fresh", Hints: []string{"new.com:443"}, LastSeen: time.Now()},
	})

	peers := r.ListPeers()
	if len(peers) != 1 {
		t.Fatalf("stale peer should be pruned, got %d peers", len(peers))
	}
	if peers[0].ChannelID != "TVfresh" {
		t.Errorf("remaining peer = %q, want TVfresh", peers[0].ChannelID)
	}
}

// ---------- Relay Server Peers ----------

func TestRelayServerPeers(t *testing.T) {
	r := newRelayRegistry("relay.example.com:443", nil, 100, 7)
	r.UpdateChannel("TVtest1", []byte(`{"name":"Test"}`), map[string]interface{}{"name": "Test"}, []string{"origin.com:443"})
	r.MergePeers([]relayPeerInfo{
		{ChannelID: "TVpeer1", Name: "Peer Channel", Hints: []string{"peer.com:443"}, LastSeen: time.Now()},
	})

	srv := newRelayServer(r, newClient(false), nil)
	req := httptest.NewRequest("GET", "/tltv/v1/peers", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("peers status = %d", w.Code)
	}

	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)

	peers := resp["peers"].([]interface{})
	if len(peers) < 1 {
		t.Error("should have at least 1 peer")
	}

	// Check Cache-Control header
	if cc := w.Header().Get("Cache-Control"); cc != "max-age=300" {
		t.Errorf("Cache-Control = %q, want max-age=300", cc)
	}
}

// ---------- relayFollowMigration ----------

func TestRelayFollowMigration(t *testing.T) {
	now := time.Now().UTC()

	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	oldID := makeChannelID(pub)

	pub2, priv2, _ := ed25519.GenerateKey(rand.Reader)
	newID := makeChannelID(pub2)

	// Create signed migration doc (old -> new)
	migDoc := map[string]interface{}{
		"v":        json.Number("1"),
		"type":     "migration",
		"from":     oldID,
		"to":       newID,
		"migrated": now.Format(timestampFormat),
	}
	migSigned, _ := signDocument(migDoc, priv)
	migBytes, _ := json.Marshal(migSigned)

	// Create signed metadata doc for new channel
	metaDoc := map[string]interface{}{
		"v":       json.Number("1"),
		"seq":     json.Number(fmt.Sprintf("%d", now.Unix())),
		"id":      newID,
		"name":    "New Channel",
		"stream":  "stream.m3u8",
		"access":  "public",
		"status":  "active",
		"updated": now.Format(timestampFormat),
	}
	metaSigned, _ := signDocument(metaDoc, priv2)
	metaBytes, _ := json.Marshal(metaSigned)

	// Mock server with exact path routing
	mux := http.NewServeMux()
	mux.HandleFunc("GET /tltv/v1/channels/"+oldID, func(w http.ResponseWriter, r *http.Request) {
		w.Write(migBytes)
	})
	mux.HandleFunc("GET /tltv/v1/channels/"+newID, func(w http.ResponseWriter, r *http.Request) {
		w.Write(metaBytes)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	host := strings.TrimPrefix(srv.URL, "http://")
	client := newClient(false)
	finalID, result, err := relayFollowMigration(client, oldID, []string{host}, 5)
	if err != nil {
		t.Fatalf("relayFollowMigration: %v", err)
	}
	if finalID != newID {
		t.Errorf("finalID = %q, want %q", finalID, newID)
	}
	if result.IsMigration {
		t.Error("final result should not be a migration")
	}
}
