package main

import (
	"context"
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
		"v":     json.Number("1"),
		"seq":   json.Number(fmt.Sprintf("%d", now.Unix())),
		"id":    channelID,
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

func testRelayUpstreamMaster(t *testing.T) (*httptest.Server, string) {
	t.Helper()

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	channelID := makeChannelID(pub)
	now := time.Now().UTC()

	meta := map[string]interface{}{
		"v":       json.Number("1"),
		"seq":     json.Number(fmt.Sprintf("%d", now.Unix())),
		"id":      channelID,
		"name":    "Master Channel",
		"stream":  "/tltv/v1/channels/" + channelID + "/stream.m3u8",
		"access":  "public",
		"status":  "active",
		"updated": now.Format(timestampFormat),
	}
	signedMeta, _ := signDocument(meta, priv)
	metaBytes, _ := json.Marshal(signedMeta)

	from := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	until := from.Add(24 * time.Hour)
	guide := map[string]interface{}{
		"v":       json.Number("1"),
		"seq":     json.Number(fmt.Sprintf("%d", now.Unix())),
		"id":      channelID,
		"from":    from.Format(timestampFormat),
		"until":   until.Format(timestampFormat),
		"entries": []interface{}{map[string]interface{}{"start": from.Format(timestampFormat), "end": until.Format(timestampFormat), "title": "Test Show"}},
		"updated": now.Format(timestampFormat),
	}
	signedGuide, _ := signDocument(guide, priv)
	guideBytes, _ := json.Marshal(signedGuide)

	master := strings.Join([]string{
		"#EXTM3U",
		"#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID=\"audio\",NAME=\"Main\",DEFAULT=YES,AUTOSELECT=YES,URI=\"audio_main.m3u8\"",
		"#EXT-X-MEDIA:TYPE=SUBTITLES,GROUP-ID=\"subs\",NAME=\"Clock\",DEFAULT=YES,AUTOSELECT=YES,URI=\"subs_clock.m3u8\"",
		"#EXT-X-STREAM-INF:BANDWIDTH=2000000,RESOLUTION=1280x720,CODECS=\"avc1.42c01f,mp4a.40.2\",AUDIO=\"audio\",SUBTITLES=\"subs\"",
		"stream_720p.m3u8",
	}, "\n") + "\n"
	variant := "#EXTM3U\n#EXT-X-TARGETDURATION:2\n#EXTINF:2.0,\n720p_seg0.ts\n"
	audio := "#EXTM3U\n#EXT-X-TARGETDURATION:2\n#EXTINF:2.0,\naudio_main_seg0.ts\n"
	subs := "#EXTM3U\n#EXT-X-TARGETDURATION:2\n#EXTINF:2.0,\nsubs_clock_seg0.vtt\n"
	vtt := "WEBVTT\nX-TIMESTAMP-MAP=LOCAL:00:00:00.000,MPEGTS:0\n\n00:00:00.000 --> 00:00:02.000\nhello\n"

	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/tltv", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"protocol": "tltv",
			"versions": []int{1},
			"channels": []map[string]string{{"id": channelID, "name": "Master Channel"}},
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
		w.Write([]byte(master))
	})
	mux.HandleFunc("GET /tltv/v1/channels/"+channelID+"/stream_720p.m3u8", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		w.Write([]byte(variant))
	})
	mux.HandleFunc("GET /tltv/v1/channels/"+channelID+"/audio_main.m3u8", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		w.Write([]byte(audio))
	})
	mux.HandleFunc("GET /tltv/v1/channels/"+channelID+"/subs_clock.m3u8", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		w.Write([]byte(subs))
	})
	mux.HandleFunc("GET /tltv/v1/channels/"+channelID+"/720p_seg0.ts", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "video/mp2t")
		w.Write([]byte("variant-segment"))
	})
	mux.HandleFunc("GET /tltv/v1/channels/"+channelID+"/audio_main_seg0.ts", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "video/mp2t")
		w.Write([]byte("audio-segment"))
	})
	mux.HandleFunc("GET /tltv/v1/channels/"+channelID+"/subs_clock_seg0.vtt", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/vtt")
		w.Write([]byte(vtt))
	})
	mux.HandleFunc("GET /tltv/v1/peers", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		json.NewEncoder(w).Encode(map[string]interface{}{"peers": []interface{}{}})
	})

	return httptest.NewServer(mux), channelID
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
	res, err := fetchAndVerifyMetadata(client, channelID, []string{hostFromURL(upstream.URL)})
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

func TestRelayFetchAndVerifyMetadata_ReturnsSuccessfulHint(t *testing.T) {
	upstream, channelID, _ := testRelayUpstream(t)
	defer upstream.Close()

	goodHint := hostFromURL(upstream.URL)
	deadHint := "127.0.0.1:1"

	client := newClient(false)
	res, err := fetchAndVerifyMetadata(client, channelID, []string{deadHint, goodHint})
	if err != nil {
		t.Fatal(err)
	}
	if res.Hint != goodHint {
		t.Fatalf("successful hint = %q, want %q", res.Hint, goodHint)
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
	_, err := fetchAndVerifyMetadata(client, channelID, []string{hostFromURL(ts.URL)})
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
	if err := checkChannelAccess(doc); err != nil {
		t.Errorf("public channel should be allowed: %v", err)
	}
}

func TestRelayCheckAccess_Private(t *testing.T) {
	doc := map[string]interface{}{"access": "token", "status": "active"}
	if err := checkChannelAccess(doc); err == nil {
		t.Error("private channel should be rejected")
	}
}

func TestRelayCheckAccess_OnDemand(t *testing.T) {
	doc := map[string]interface{}{"access": "public", "on_demand": true}
	if err := checkChannelAccess(doc); err == nil {
		t.Error("on-demand channel should be rejected")
	}
}

func TestRelayCheckAccess_Retired(t *testing.T) {
	doc := map[string]interface{}{"access": "public", "status": "retired"}
	if err := checkChannelAccess(doc); err == nil {
		t.Error("retired channel should be rejected")
	}
}

func TestRelayCheckAccess_UnknownAccess(t *testing.T) {
	doc := map[string]interface{}{"access": "delegation", "status": "active"}
	if err := checkChannelAccess(doc); err == nil {
		t.Error("unknown access mode should be rejected")
	}
}

func TestRelayCheckAccess_UnknownStatus(t *testing.T) {
	doc := map[string]interface{}{"access": "public", "status": "archived"}
	if err := checkChannelAccess(doc); err == nil {
		t.Error("unknown status should be rejected")
	}
}

func TestRelayCheckAccess_AbsentDefaults(t *testing.T) {
	// Missing access and status should default to public/active → allowed
	doc := map[string]interface{}{}
	if err := checkChannelAccess(doc); err != nil {
		t.Errorf("absent access/status should default to public/active: %v", err)
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

	cfg, err := loadDaemonConfig(f)
	if err != nil {
		t.Fatal(err)
	}
	channels, _ := cfg["channels"].([]interface{})
	nodes, _ := cfg["nodes"].([]interface{})
	if len(channels) != 1 {
		t.Errorf("channels: %d", len(channels))
	}
	if len(nodes) != 2 {
		t.Errorf("nodes: %d", len(nodes))
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
	r := newRelayRegistry("", false, 100, 7)

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
	r := newRelayRegistry("", false, 100, 7)
	r.UpdateChannel("TVtest123", []byte(`{}`), map[string]interface{}{"name": "x"}, nil)
	r.RemoveChannel("TVtest123")
	if r.GetChannel("TVtest123") != nil {
		t.Error("channel should be removed")
	}
}

func TestRelayRegistry_GuideUpdate(t *testing.T) {
	r := newRelayRegistry("", false, 100, 7)
	r.UpdateChannel("TVtest123", []byte(`{}`), map[string]interface{}{"name": "x"}, nil)

	entries := []guideEntry{{Start: "2026-01-01T00:00:00Z", End: "2026-01-02T00:00:00Z", Title: "Show"}}
	r.UpdateGuide("TVtest123", []byte(`{"entries":[]}`), entries)

	ch := r.GetChannel("TVtest123")
	if len(ch.GuideEntries) != 1 {
		t.Errorf("guide entries: %d", len(ch.GuideEntries))
	}
}

func TestRelayRegistry_ListPeers(t *testing.T) {
	r := newRelayRegistry("relay.example.com:443", false, 100, 7)
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
	r := newRelayRegistry("", false, 100, 7)
	r.UpdateChannel("TVtest123", []byte(`{}`), map[string]interface{}{"name": "Test"}, nil)
	srv := newRelayServer(r, newClient(false), nil, nil, nil)

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

	r := newRelayRegistry("", false, 100, 7)
	r.UpdateChannel("TVtest123", []byte(rawMeta), map[string]interface{}{"name": "Test"}, nil)
	srv := newRelayServer(r, newClient(false), nil, nil, nil)

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
	r := newRelayRegistry("", false, 100, 7)
	srv := newRelayServer(r, newClient(false), nil, nil, nil)

	req := httptest.NewRequest("GET", "/tltv/v1/channels/TVnonexistent", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != 404 {
		t.Fatalf("status = %d, want 404", w.Code)
	}
}

func TestRelayServerHealth(t *testing.T) {
	r := newRelayRegistry("", false, 100, 7)
	r.UpdateChannel("TVtest123", []byte(`{}`), map[string]interface{}{"name": "Test"}, nil)
	srv := newRelayServer(r, newClient(false), nil, nil, nil)

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
	r := newRelayRegistry("", false, 100, 7)
	srv := newRelayServer(r, newClient(false), nil, nil, nil)

	req := httptest.NewRequest("GET", "/.well-known/tltv", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Error("missing CORS header")
	}
}

func TestRelayServerOPTIONS(t *testing.T) {
	r := newRelayRegistry("", false, 100, 7)
	srv := newRelayServer(r, newClient(false), nil, nil, nil)

	req := httptest.NewRequest("OPTIONS", "/.well-known/tltv", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != 204 {
		t.Errorf("OPTIONS status = %d, want 204", w.Code)
	}
}

func TestRelayServerMethodNotAllowed(t *testing.T) {
	r := newRelayRegistry("", false, 100, 7)
	srv := newRelayServer(r, newClient(false), nil, nil, nil)

	req := httptest.NewRequest("POST", "/.well-known/tltv", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != 400 {
		t.Errorf("POST status = %d, want 400", w.Code)
	}
}

// TestRelayServerViewerCoexistence verifies that debugViewerRoutes can be
// registered on the relay's mux without a Go 1.22 ServeMux pattern conflict.
// The viewer's "GET /{$}" must not conflict with the relay's method-less
// "/tltv/" and "/.well-known/tltv" catch-all patterns.
func TestRelayServerViewerCoexistence(t *testing.T) {
	r := newRelayRegistry("", false, 100, 7)
	srv := newRelayServer(r, newClient(false), nil, nil, nil)

	// Register viewer routes on the relay's mux — this used to panic
	// with "GET /" vs "/tltv/" pattern conflict before the fix.
	debugViewerRoutes(srv.mux, func(_ string) map[string]interface{} {
		return map[string]interface{}{"channel_name": "test"}
	}, nil)

	// Viewer root serves HTML
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
	if w.Code != 200 {
		t.Errorf("GET / status = %d, want 200", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); !strings.Contains(ct, "text/html") {
		t.Errorf("GET / content-type = %q, want text/html", ct)
	}

	// Viewer assets work
	w = httptest.NewRecorder()
	srv.ServeHTTP(w, httptest.NewRequest("GET", "/api/info", nil))
	if w.Code != 200 {
		t.Errorf("GET /api/info status = %d, want 200", w.Code)
	}

	// Protocol endpoint still works
	w = httptest.NewRecorder()
	srv.ServeHTTP(w, httptest.NewRequest("GET", "/.well-known/tltv", nil))
	if w.Code != 200 {
		t.Errorf("GET /.well-known/tltv status = %d, want 200", w.Code)
	}

	// Method rejection still works
	w = httptest.NewRecorder()
	srv.ServeHTTP(w, httptest.NewRequest("POST", "/tltv/v1/peers", nil))
	if w.Code != 400 {
		t.Errorf("POST /tltv/ status = %d, want 400", w.Code)
	}

	// Non-root GET returns 404, not viewer HTML
	w = httptest.NewRecorder()
	srv.ServeHTTP(w, httptest.NewRequest("GET", "/nonexistent", nil))
	if w.Code != 404 {
		t.Errorf("GET /nonexistent status = %d, want 404", w.Code)
	}
}

// ---------- End-to-End: Relay from Mock Upstream ----------

func TestRelayEndToEnd_StreamProxy(t *testing.T) {
	upstream, channelID, _ := testRelayUpstream(t)
	defer upstream.Close()

	client := newClient(false)
	hint := hostFromURL(upstream.URL)

	// Fetch and verify metadata
	res, err := fetchAndVerifyMetadata(client, channelID, []string{hint})
	if err != nil {
		t.Fatal(err)
	}

	// Set up relay
	registry := newRelayRegistry("", false, 100, 7)
	registry.UpdateChannel(channelID, res.Raw, res.Doc, []string{hint})

	srv := newRelayServer(registry, client, nil, nil, nil)

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

func TestRelayEndToEnd_StreamProxyUsesSuccessfulHint(t *testing.T) {
	upstream, channelID, _ := testRelayUpstream(t)
	defer upstream.Close()

	client := newClient(false)
	goodHint := hostFromURL(upstream.URL)
	deadHint := "127.0.0.1:1"
	res, err := fetchAndVerifyMetadata(client, channelID, []string{deadHint, goodHint})
	if err != nil {
		t.Fatal(err)
	}

	registry := newRelayRegistry("", false, 100, 7)
	registry.UpdateChannel(channelID, res.Raw, res.Doc, []string{deadHint, goodHint}, res.Hint)

	srv := newRelayServer(registry, client, nil, nil, nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, httptest.NewRequest("GET", "/tltv/v1/channels/"+channelID+"/seg-000.ts", nil))
	if w.Code != 200 {
		t.Fatalf("segment status = %d, want 200", w.Code)
	}
	if w.Body.String() != "fake-ts-data" {
		t.Fatalf("segment body = %q, want fake-ts-data", w.Body.String())
	}
}

func TestRelayEndToEnd_GuideServing(t *testing.T) {
	upstream, channelID, _ := testRelayUpstream(t)
	defer upstream.Close()

	client := newClient(false)
	hint := hostFromURL(upstream.URL)

	res, err := fetchAndVerifyMetadata(client, channelID, []string{hint})
	if err != nil {
		t.Fatal(err)
	}
	raw, entries, err := relayFetchAndVerifyGuide(client, channelID, []string{hint})
	if err != nil {
		t.Fatal(err)
	}

	registry := newRelayRegistry("", false, 100, 7)
	registry.UpdateChannel(channelID, res.Raw, res.Doc, []string{hint})
	registry.UpdateGuide(channelID, raw, entries)

	srv := newRelayServer(registry, client, nil, nil, nil)

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

func TestRelayGuidePollLoop_ClearsGuideOn404(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	channelID := makeChannelID(pub)
	now := time.Now().UTC()
	meta := map[string]interface{}{
		"v":       json.Number("1"),
		"seq":     json.Number(fmt.Sprintf("%d", now.Unix())),
		"id":      channelID,
		"name":    "No Guide Channel",
		"stream":  "/tltv/v1/channels/" + channelID + "/stream.m3u8",
		"access":  "public",
		"status":  "active",
		"updated": now.Format(timestampFormat),
	}
	signedMeta, _ := signDocument(meta, priv)
	metaBytes, _ := json.Marshal(signedMeta)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/tltv/v1/channels/" + channelID:
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.Write(metaBytes)
		case "/tltv/v1/channels/" + channelID + "/guide.json":
			jsonError(w, "channel_not_found", http.StatusNotFound)
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()

	client := newClient(false)
	hint := hostFromURL(ts.URL)
	res, err := fetchAndVerifyMetadata(client, channelID, []string{hint})
	if err != nil {
		t.Fatal(err)
	}

	registry := newRelayRegistry("", false, 100, 7)
	registry.UpdateChannel(channelID, res.Raw, res.Doc, []string{hint}, res.Hint)
	registry.UpdateGuide(channelID, []byte(`{"entries":[{"title":"stale"}]}`), []guideEntry{{Start: "2026-01-01T00:00:00Z", End: "2026-01-01T01:00:00Z", Title: "stale"}})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go relayGuidePollLoop(ctx, 10*time.Millisecond, client, registry)

	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		ch := registry.GetChannel(channelID)
		if ch != nil && ch.Guide == nil && len(ch.GuideEntries) == 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	ch := registry.GetChannel(channelID)
	t.Fatalf("guide was not cleared on 404: guide=%q entries=%d", string(ch.Guide), len(ch.GuideEntries))
}

// ---------- Concurrent Access ----------

func TestRelayConcurrentAccess(t *testing.T) {
	r := newRelayRegistry("", false, 100, 7)
	r.UpdateChannel("TVtest", []byte(`{"name":"test"}`), map[string]interface{}{"name": "test"}, nil)
	srv := newRelayServer(r, newClient(false), nil, nil, nil)

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
	r := newRelayRegistry("", false, 100, 7)
	r.UpdateChannel("TVtest123", []byte(`{"name":"Test"}`), map[string]interface{}{"name": "Test"}, nil)
	srv := newRelayServer(r, newClient(false), nil, nil, nil)

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

	entries := extractGuideEntries(doc)
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
	r := newRelayRegistry("", false, 100, 7)
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
	srv := newRelayServer(r, newClient(false), nil, nil, nil)
	req := httptest.NewRequest("GET", "/tltv/v1/channels/TVold123", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("migration doc should be served, got %d", w.Code)
	}
}

// ---------- MergePeers ----------

func TestRelayMergePeers(t *testing.T) {
	r := newRelayRegistry("", true, 100, 7) // gossip enabled to see merged peers

	// Add peers
	r.MergePeers([]peerEntry{
		{ChannelID: "TVa", Name: "A", Hints: []string{"a.example.com:443"}, LastSeen: time.Now()},
		{ChannelID: "TVb", Name: "B", Hints: []string{"b.example.com:443"}, LastSeen: time.Now()},
	})

	peers := r.ListPeers()
	if len(peers) != 2 {
		t.Fatalf("expected 2 peers, got %d", len(peers))
	}

	// Merge with overlap (should update, not duplicate)
	r.MergePeers([]peerEntry{
		{ChannelID: "TVa", Name: "A Updated", Hints: []string{"a2.example.com:443"}, LastSeen: time.Now()},
	})
	peers = r.ListPeers()
	if len(peers) != 2 {
		t.Fatalf("should still have 2 peers after overlap, got %d", len(peers))
	}
}

func TestRelayMergePeers_Staleness(t *testing.T) {
	r := newRelayRegistry("", true, 100, 1) // gossip enabled, 1-day staleness

	staleTime := time.Now().Add(-48 * time.Hour)
	r.MergePeers([]peerEntry{
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
	// Enable gossip so gossip-discovered peers appear in the response.
	// Own relayed channels are excluded (visible via /.well-known/tltv).
	r := newRelayRegistry("relay.example.com:443", true, 100, 7)
	r.UpdateChannel("TVtest1", []byte(`{"name":"Test"}`), map[string]interface{}{"name": "Test"}, []string{"origin.com:443"})
	r.MergePeers([]peerEntry{
		{ChannelID: "TVpeer1", Name: "Peer Channel", Hints: []string{"peer.com:443"}, LastSeen: time.Now()},
	})

	srv := newRelayServer(r, newClient(false), nil, nil, nil)
	req := httptest.NewRequest("GET", "/tltv/v1/peers", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("peers status = %d", w.Code)
	}

	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)

	peers := resp["peers"].([]interface{})
	// Own relayed channel (TVtest1) excluded; gossip peer (TVpeer1) included
	if len(peers) != 1 {
		t.Errorf("expected 1 gossip peer, got %d", len(peers))
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

// ---------- Buffer Tests ----------

func TestRelayBuffer_PushAndGet(t *testing.T) {
	mgr := newRelayBufferManager(1<<30, 0)
	buf := mgr.AddBuffer("TVtest", 100)

	buf.PushSegment(0, []byte("seg-0-data"), 2.0)
	buf.PushSegment(1, []byte("seg-1-data"), 2.0)
	buf.PushSegment(2, []byte("seg-2-data"), 2.0)

	if buf.SegmentCount() != 3 {
		t.Errorf("count = %d, want 3", buf.SegmentCount())
	}

	if data := buf.GetSegment(0); string(data) != "seg-0-data" {
		t.Errorf("seg 0 = %q", data)
	}
	if data := buf.GetSegment(1); string(data) != "seg-1-data" {
		t.Errorf("seg 1 = %q", data)
	}
	if data := buf.GetSegment(99); data != nil {
		t.Errorf("seg 99 should be nil, got %q", data)
	}
}

func TestRelayBuffer_Eviction(t *testing.T) {
	mgr := newRelayBufferManager(1<<30, 0)
	buf := mgr.AddBuffer("TVtest", 3) // max 3 segments

	buf.PushSegment(0, []byte("a"), 2.0)
	buf.PushSegment(1, []byte("b"), 2.0)
	buf.PushSegment(2, []byte("c"), 2.0)
	buf.PushSegment(3, []byte("d"), 2.0) // should evict seq 0

	if buf.SegmentCount() != 3 {
		t.Errorf("count = %d, want 3", buf.SegmentCount())
	}
	if buf.GetSegment(0) != nil {
		t.Error("seg 0 should have been evicted")
	}
	if data := buf.GetSegment(3); string(data) != "d" {
		t.Errorf("seg 3 = %q, want d", data)
	}
}

func TestRelayBuffer_DuplicateIgnored(t *testing.T) {
	mgr := newRelayBufferManager(1<<30, 0)
	buf := mgr.AddBuffer("TVtest", 100)

	buf.PushSegment(5, []byte("first"), 2.0)
	buf.PushSegment(5, []byte("second"), 2.0) // should be ignored

	if buf.SegmentCount() != 1 {
		t.Errorf("count = %d, want 1 (duplicate ignored)", buf.SegmentCount())
	}
	if data := buf.GetSegment(5); string(data) != "first" {
		t.Errorf("seg 5 = %q, want first (original preserved)", data)
	}
}

func TestRelayBuffer_MemoryTracking(t *testing.T) {
	mgr := newRelayBufferManager(1<<30, 0)
	buf := mgr.AddBuffer("TVtest", 100)

	buf.PushSegment(0, make([]byte, 1000), 2.0)
	buf.PushSegment(1, make([]byte, 2000), 2.0)

	if buf.MemoryBytes() != 3000 {
		t.Errorf("buf mem = %d, want 3000", buf.MemoryBytes())
	}
	if mgr.TotalMemory() != 3000 {
		t.Errorf("mgr total = %d, want 3000", mgr.TotalMemory())
	}
}

func TestRelayBuffer_MemoryLimit(t *testing.T) {
	mgr := newRelayBufferManager(500, 0) // 500 bytes max
	buf := mgr.AddBuffer("TVtest", 100)

	buf.PushSegment(0, make([]byte, 200), 2.0)
	buf.PushSegment(1, make([]byte, 200), 2.0)
	buf.PushSegment(2, make([]byte, 200), 2.0) // would push to 600, over limit

	// Should have evicted oldest to stay under limit
	if mgr.TotalMemory() > 500 {
		t.Errorf("total = %d, should be <= 500", mgr.TotalMemory())
	}
	if buf.GetSegment(0) != nil {
		t.Error("seg 0 should have been evicted to meet memory limit")
	}
}

func TestRelayBuffer_TargetDurationResizesCapacity(t *testing.T) {
	mgr := newRelayBufferManager(1<<30, 0)
	mgr.bufferDuration = time.Minute
	buf := mgr.AddBuffer("TVtest", 10)
	buf.SetTargetDuration(2.0)

	for i := uint64(0); i < 40; i++ {
		buf.PushSegment(i, []byte("x"), 2.0)
	}

	if buf.SegmentCount() != 30 {
		t.Fatalf("count = %d, want 30 segments for 60s buffer at 2s target duration", buf.SegmentCount())
	}
	if buf.GetSegment(9) != nil {
		t.Fatal("oldest retained segment should be seq 10 after resize-based eviction")
	}
	if buf.GetSegment(10) == nil || buf.GetSegment(39) == nil {
		t.Fatal("resized buffer should retain the newest 30 segments")
	}
}

func TestRelayBuffer_GetManifest_Live(t *testing.T) {
	mgr := newRelayBufferManager(1<<30, 0) // no delay
	buf := mgr.AddBuffer("TVtest", 100)
	buf.SetTargetDuration(2.0)

	for i := uint64(0); i < 10; i++ {
		buf.PushSegment(i, []byte(fmt.Sprintf("seg-%d", i)), 2.0)
	}

	manifest := buf.GetManifest()
	if manifest == "" {
		t.Fatal("manifest should not be empty")
	}
	if !strings.Contains(manifest, "#EXTM3U") {
		t.Error("missing EXTM3U")
	}
	if !strings.Contains(manifest, "#EXT-X-TARGETDURATION:2") {
		t.Error("missing target duration")
	}
	// Should show a sliding window of recent segments (not all 10)
	segCount := strings.Count(manifest, "#EXTINF:")
	if segCount > 6 {
		t.Errorf("live manifest should have ~3-5 segments, got %d", segCount)
	}
	// Should include the latest segment
	if !strings.Contains(manifest, "seg9.ts") {
		t.Error("manifest should include latest segment (seg9.ts)")
	}
	t.Logf("Live manifest:\n%s", manifest)
}

func TestRelayBuffer_GetManifest_Delayed(t *testing.T) {
	mgr := newRelayBufferManager(1<<30, 10*time.Second) // 10s delay
	buf := mgr.AddBuffer("TVtest", 100)
	buf.SetTargetDuration(2.0)

	// Push segments with explicit fetch times.
	// Segments 0-4 are "old" (fetched 30s ago), segments 5-9 are "new" (just fetched).
	now := time.Now()
	for i := uint64(0); i < 10; i++ {
		seg := &relayBufferSegment{
			SeqNum:   i,
			Data:     []byte(fmt.Sprintf("seg-%d", i)),
			Duration: 2.0,
		}
		if i < 5 {
			seg.Fetched = now.Add(-30 * time.Second) // old
		} else {
			seg.Fetched = now.Add(-2 * time.Second) // within delay window
		}
		buf.mu.Lock()
		buf.segments[i] = seg
		buf.memBytes += int64(len(seg.Data))
		if !buf.hasData || i > buf.newestSeq {
			buf.newestSeq = i
		}
		if !buf.hasData || i < buf.oldestSeq {
			buf.oldestSeq = i
		}
		buf.hasData = true
		buf.mu.Unlock()
	}

	manifest := buf.GetManifest()
	if manifest == "" {
		t.Fatal("manifest should not be empty")
	}
	// Delayed manifest should NOT include segments 5-9 (within delay window)
	if strings.Contains(manifest, "seg9.ts") {
		t.Error("delayed manifest should NOT include recent segments")
	}
	if strings.Contains(manifest, "seg5.ts") {
		t.Error("delayed manifest should NOT include seg5 (within delay)")
	}
	// Should include older segments
	if !strings.Contains(manifest, "seg4.ts") {
		t.Error("delayed manifest should include seg4 (outside delay)")
	}
	t.Logf("Delayed manifest:\n%s", manifest)
}

func TestRelayBuffer_GetManifest_Empty(t *testing.T) {
	mgr := newRelayBufferManager(1<<30, 0)
	buf := mgr.AddBuffer("TVtest", 100)

	manifest := buf.GetManifest()
	if manifest != "" {
		t.Errorf("empty buffer manifest should be empty string, got %q", manifest)
	}
}

func TestRelayBuffer_RemoveBuffer(t *testing.T) {
	mgr := newRelayBufferManager(1<<30, 0)
	mgr.AddBuffer("TVch1", 100)
	buf := mgr.GetBuffer("TVch1")
	buf.PushSegment(0, make([]byte, 1000), 2.0)

	if mgr.TotalMemory() != 1000 {
		t.Fatalf("total = %d, want 1000", mgr.TotalMemory())
	}

	mgr.RemoveBuffer("TVch1")

	if mgr.GetBuffer("TVch1") != nil {
		t.Error("buffer should be removed")
	}
	if mgr.TotalMemory() != 0 {
		t.Errorf("total = %d after remove, want 0", mgr.TotalMemory())
	}
}

func TestRelayBufferServe_Manifest(t *testing.T) {
	// Set up a relay server with a buffer that has segments.
	// Verify stream.m3u8 serves from the buffer.
	upstream, channelID, _ := testRelayUpstream(t)
	defer upstream.Close()

	client := newClient(false)
	hint := hostFromURL(upstream.URL)
	res, err := fetchAndVerifyMetadata(client, channelID, []string{hint})
	if err != nil {
		t.Fatal(err)
	}

	registry := newRelayRegistry("", false, 100, 7)
	registry.UpdateChannel(channelID, res.Raw, res.Doc, []string{hint})

	mgr := newRelayBufferManager(1<<30, 0)
	buf := mgr.AddBuffer(channelID, 100)
	buf.SetTargetDuration(2.0)
	buf.PushSegment(0, []byte("ts-data-0"), 2.0)
	buf.PushSegment(1, []byte("ts-data-1"), 2.0)
	buf.PushSegment(2, []byte("ts-data-2"), 2.0)

	srv := newRelayServer(registry, client, nil, nil, mgr)

	// stream.m3u8 should serve from buffer
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, httptest.NewRequest("GET", "/tltv/v1/channels/"+channelID+"/stream.m3u8", nil))
	if w.Code != 200 {
		t.Fatalf("stream.m3u8: status = %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "#EXTM3U") {
		t.Error("manifest missing EXTM3U")
	}
	if !strings.Contains(body, "seg0.ts") {
		t.Error("manifest missing seg0.ts")
	}
}

func TestRelayBufferServe_Segment(t *testing.T) {
	upstream, channelID, _ := testRelayUpstream(t)
	defer upstream.Close()

	client := newClient(false)
	hint := hostFromURL(upstream.URL)
	res, err := fetchAndVerifyMetadata(client, channelID, []string{hint})
	if err != nil {
		t.Fatal(err)
	}

	registry := newRelayRegistry("", false, 100, 7)
	registry.UpdateChannel(channelID, res.Raw, res.Doc, []string{hint})

	mgr := newRelayBufferManager(1<<30, 0)
	buf := mgr.AddBuffer(channelID, 100)
	buf.PushSegment(42, []byte("buffered-segment-42"), 2.0)

	srv := newRelayServer(registry, client, nil, nil, mgr)

	// Segment from buffer
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, httptest.NewRequest("GET", "/tltv/v1/channels/"+channelID+"/seg42.ts", nil))
	if w.Code != 200 {
		t.Fatalf("seg42.ts: status = %d", w.Code)
	}
	if w.Body.String() != "buffered-segment-42" {
		t.Errorf("segment data = %q, want buffered-segment-42", w.Body.String())
	}
}

func TestRelayBufferServe_FallbackToUpstream(t *testing.T) {
	// Buffer is empty — should fall through to upstream proxy.
	upstream, channelID, _ := testRelayUpstream(t)
	defer upstream.Close()

	client := newClient(false)
	hint := hostFromURL(upstream.URL)
	res, err := fetchAndVerifyMetadata(client, channelID, []string{hint})
	if err != nil {
		t.Fatal(err)
	}

	registry := newRelayRegistry("", false, 100, 7)
	registry.UpdateChannel(channelID, res.Raw, res.Doc, []string{hint})

	mgr := newRelayBufferManager(1<<30, 0)
	mgr.AddBuffer(channelID, 100) // empty buffer

	srv := newRelayServer(registry, client, nil, nil, mgr)

	// stream.m3u8 with empty buffer should fall through to upstream
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, httptest.NewRequest("GET", "/tltv/v1/channels/"+channelID+"/stream.m3u8", nil))
	if w.Code != 200 {
		t.Fatalf("fallback stream.m3u8: status = %d", w.Code)
	}
	// Should get the upstream manifest (with EXTM3U)
	if !strings.Contains(w.Body.String(), "#EXTM3U") {
		t.Error("fallback manifest missing EXTM3U")
	}
}

func TestRelayBuffer_MasterPlaylistComposition(t *testing.T) {
	upstream, channelID := testRelayUpstreamMaster(t)
	defer upstream.Close()

	client := newClient(false)
	hint := hostFromURL(upstream.URL)
	res, err := fetchAndVerifyMetadata(client, channelID, []string{hint})
	if err != nil {
		t.Fatal(err)
	}

	registry := newRelayRegistry("", false, 100, 7)
	registry.UpdateChannel(channelID, res.Raw, res.Doc, []string{hint})

	mgr := newRelayBufferManager(1<<30, 0)
	mgr.bufferDuration = time.Minute
	mgr.AddBuffer(channelID, 0)

	ctx, cancel := context.WithCancel(context.Background())
	mgr.StartBuffering(ctx, channelID, registry, client.http)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if mgr.GetRootManifest(channelID) != "" &&
			mgr.GetManifest(channelID, "stream_720p.m3u8") != "" &&
			mgr.GetManifest(channelID, "audio_main.m3u8") != "" &&
			mgr.GetManifest(channelID, "subs_clock.m3u8") != "" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if mgr.GetRootManifest(channelID) == "" {
		t.Fatal("buffered root master playlist was never cached")
	}
	if mgr.GetManifest(channelID, "stream_720p.m3u8") == "" || mgr.GetManifest(channelID, "audio_main.m3u8") == "" || mgr.GetManifest(channelID, "subs_clock.m3u8") == "" {
		t.Fatal("buffered child playlists were not generated")
	}

	cancel()
	upstream.Close()

	srv := newRelayServer(registry, client, nil, nil, mgr)

	check := func(path string, want string) {
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, httptest.NewRequest("GET", "/tltv/v1/channels/"+channelID+"/"+path, nil))
		if w.Code != 200 {
			t.Fatalf("%s: status = %d body = %s", path, w.Code, w.Body.String())
		}
		if !strings.Contains(w.Body.String(), want) {
			t.Fatalf("%s: body missing %q\n%s", path, want, w.Body.String())
		}
	}

	check("stream.m3u8", "stream_720p.m3u8")
	check("stream.m3u8", "audio_main.m3u8")
	check("stream_720p.m3u8", "720p_seg0.ts")
	check("audio_main.m3u8", "audio_main_seg0.ts")
	check("subs_clock.m3u8", "subs_clock_seg0.vtt")

	w := httptest.NewRecorder()
	srv.ServeHTTP(w, httptest.NewRequest("GET", "/tltv/v1/channels/"+channelID+"/720p_seg0.ts", nil))
	if w.Code != 200 || w.Body.String() != "variant-segment" {
		t.Fatalf("variant segment: status=%d body=%q", w.Code, w.Body.String())
	}

	w = httptest.NewRecorder()
	srv.ServeHTTP(w, httptest.NewRequest("GET", "/tltv/v1/channels/"+channelID+"/audio_main_seg0.ts", nil))
	if w.Code != 200 || w.Body.String() != "audio-segment" {
		t.Fatalf("audio segment: status=%d body=%q", w.Code, w.Body.String())
	}

	w = httptest.NewRecorder()
	srv.ServeHTTP(w, httptest.NewRequest("GET", "/tltv/v1/channels/"+channelID+"/subs_clock_seg0.vtt", nil))
	if w.Code != 200 || !strings.Contains(w.Body.String(), "WEBVTT") {
		t.Fatalf("subtitle segment: status=%d body=%q", w.Code, w.Body.String())
	}
}

func TestRelayBufferServe_NilBuffer(t *testing.T) {
	// No buffer manager — relay works normally (backward compat).
	upstream, channelID, _ := testRelayUpstream(t)
	defer upstream.Close()

	client := newClient(false)
	hint := hostFromURL(upstream.URL)
	res, err := fetchAndVerifyMetadata(client, channelID, []string{hint})
	if err != nil {
		t.Fatal(err)
	}

	registry := newRelayRegistry("", false, 100, 7)
	registry.UpdateChannel(channelID, res.Raw, res.Doc, []string{hint})

	srv := newRelayServer(registry, client, nil, nil, nil) // no buffer

	w := httptest.NewRecorder()
	srv.ServeHTTP(w, httptest.NewRequest("GET", "/tltv/v1/channels/"+channelID+"/stream.m3u8", nil))
	if w.Code != 200 {
		t.Fatalf("no-buffer stream.m3u8: status = %d", w.Code)
	}
}

func TestRelayBuffer_DelaySignaling(t *testing.T) {
	// When delay is set, /.well-known/tltv should include delay field.
	upstream, channelID, _ := testRelayUpstream(t)
	defer upstream.Close()

	client := newClient(false)
	hint := hostFromURL(upstream.URL)
	res, _ := fetchAndVerifyMetadata(client, channelID, []string{hint})

	registry := newRelayRegistry("", false, 100, 7)
	registry.UpdateChannel(channelID, res.Raw, res.Doc, []string{hint})

	mgr := newRelayBufferManager(1<<30, 30*time.Second) // 30s delay
	mgr.AddBuffer(channelID, 100)

	srv := newRelayServer(registry, client, nil, nil, mgr)

	w := httptest.NewRecorder()
	srv.ServeHTTP(w, httptest.NewRequest("GET", "/.well-known/tltv", nil))
	if w.Code != 200 {
		t.Fatalf("well-known: status = %d", w.Code)
	}

	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)
	relaying, _ := resp["relaying"].([]interface{})
	if len(relaying) == 0 {
		t.Fatal("no relaying channels")
	}
	ch0, _ := relaying[0].(map[string]interface{})
	delay, _ := ch0["delay"].(float64)
	if int(delay) != 30 {
		t.Errorf("delay = %v, want 30", ch0["delay"])
	}
}

func TestRelayBuffer_NoDelaySignaling(t *testing.T) {
	// Without delay, /.well-known/tltv should NOT include delay field.
	upstream, channelID, _ := testRelayUpstream(t)
	defer upstream.Close()

	client := newClient(false)
	hint := hostFromURL(upstream.URL)
	res, _ := fetchAndVerifyMetadata(client, channelID, []string{hint})

	registry := newRelayRegistry("", false, 100, 7)
	registry.UpdateChannel(channelID, res.Raw, res.Doc, []string{hint})

	srv := newRelayServer(registry, client, nil, nil, nil) // no buffer

	w := httptest.NewRecorder()
	srv.ServeHTTP(w, httptest.NewRequest("GET", "/.well-known/tltv", nil))

	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)
	relaying, _ := resp["relaying"].([]interface{})
	if len(relaying) > 0 {
		ch0, _ := relaying[0].(map[string]interface{})
		if _, ok := ch0["delay"]; ok {
			t.Error("delay field should not be present without buffer manager")
		}
	}
}

func TestRelayReloadConfig_BufferLifecycle(t *testing.T) {
	upstream1, channel1, _ := testRelayUpstream(t)
	defer upstream1.Close()
	upstream2, channel2, _ := testRelayUpstream(t)
	defer upstream2.Close()

	client := newClient(false)
	hint1 := hostFromURL(upstream1.URL)
	hint2 := hostFromURL(upstream2.URL)
	res1, err := fetchAndVerifyMetadata(client, channel1, []string{hint1})
	if err != nil {
		t.Fatal(err)
	}

	registry := newRelayRegistry("", false, 100, 7)
	registry.UpdateChannel(channel1, res1.Raw, res1.Doc, []string{hint1})

	mgr := newRelayBufferManager(1<<30, 0)
	mgr.bufferDuration = time.Minute
	mgr.AddBuffer(channel1, 0)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mgr.StartBuffering(ctx, channel1, registry, client.http)

	cfg := map[string]interface{}{
		"channels": []interface{}{fmt.Sprintf("tltv://%s@%s", channel2, hint2)},
	}
	relayReloadConfig(ctx, cfg, client, registry, mgr)

	if registry.GetChannel(channel1) != nil {
		t.Fatal("channel1 should have been removed from registry on reload")
	}
	if registry.GetChannel(channel2) == nil {
		t.Fatal("channel2 should have been added to registry on reload")
	}
	if mgr.GetBuffer(channel1) != nil {
		t.Fatal("channel1 buffer should have been removed on reload")
	}
	if mgr.GetBuffer(channel2) == nil {
		t.Fatal("channel2 buffer should have been created on reload")
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if mgr.GetManifest(channel2, "stream.m3u8") != "" {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("channel2 buffer fetch loop did not start after config reload")
}

func TestRelayReloadConfig_UpdatesExistingChannelHints(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	channelID := makeChannelID(pub)
	now := time.Now().UTC()
	meta := map[string]interface{}{
		"v":       json.Number("1"),
		"seq":     json.Number(fmt.Sprintf("%d", now.Unix())),
		"id":      channelID,
		"name":    "Reloaded Channel",
		"stream":  "/tltv/v1/channels/" + channelID + "/stream.m3u8",
		"access":  "public",
		"status":  "active",
		"updated": now.Format(timestampFormat),
	}
	signedMeta, _ := signDocument(meta, priv)
	metaBytes, _ := json.Marshal(signedMeta)

	newServer := func(segmentBody string) *httptest.Server {
		mux := http.NewServeMux()
		mux.HandleFunc("GET /tltv/v1/channels/"+channelID, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.Write(metaBytes)
		})
		mux.HandleFunc("GET /tltv/v1/channels/"+channelID+"/stream.m3u8", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
			w.Write([]byte("#EXTM3U\n#EXT-X-TARGETDURATION:2\n#EXTINF:2.0,\nseg-000.ts\n"))
		})
		mux.HandleFunc("GET /tltv/v1/channels/"+channelID+"/seg-000.ts", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "video/mp2t")
			w.Write([]byte(segmentBody))
		})
		return httptest.NewServer(mux)
	}

	upstreamA := newServer("from-a")
	defer upstreamA.Close()
	upstreamB := newServer("from-b")
	defer upstreamB.Close()

	client := newClient(false)
	hintA := hostFromURL(upstreamA.URL)
	hintB := hostFromURL(upstreamB.URL)
	res, err := fetchAndVerifyMetadata(client, channelID, []string{hintA})
	if err != nil {
		t.Fatal(err)
	}

	registry := newRelayRegistry("", false, 100, 7)
	registry.UpdateChannel(channelID, res.Raw, res.Doc, []string{hintA}, res.Hint)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cfg := map[string]interface{}{
		"channels": []interface{}{fmt.Sprintf("tltv://%s@%s", channelID, hintB)},
	}
	relayReloadConfig(ctx, cfg, client, registry, nil)

	ch := registry.GetChannel(channelID)
	if ch == nil {
		t.Fatal("channel missing after reload")
	}
	if ch.StreamHint != hintB {
		t.Fatalf("StreamHint = %q, want %q", ch.StreamHint, hintB)
	}

	srv := newRelayServer(registry, client, nil, nil, nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, httptest.NewRequest("GET", "/tltv/v1/channels/"+channelID+"/seg-000.ts", nil))
	if w.Code != 200 {
		t.Fatalf("segment status = %d, want 200", w.Code)
	}
	if w.Body.String() != "from-b" {
		t.Fatalf("segment body = %q, want from-b", w.Body.String())
	}
}

func TestParseMemorySize(t *testing.T) {
	tests := []struct {
		input string
		want  int64
		err   bool
	}{
		{"1g", 1 << 30, false},
		{"512m", 512 << 20, false},
		{"100k", 100 << 10, false},
		{"1073741824", 1073741824, false},
		{"2gb", 2 << 30, false},
		{"", 0, false},
		{"abc", 0, true},
		{"0m", 0, true},
	}
	for _, tt := range tests {
		got, err := parseMemorySize(tt.input)
		if tt.err {
			if err == nil {
				t.Errorf("parseMemorySize(%q) should fail", tt.input)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseMemorySize(%q): %v", tt.input, err)
			continue
		}
		if got != tt.want {
			t.Errorf("parseMemorySize(%q) = %d, want %d", tt.input, got, tt.want)
		}
	}
}
