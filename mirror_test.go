package main

import (
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// ---------- Test Helpers ----------

// testMirrorOrigin creates a test TLTV origin server with the given key and origins list.
// Returns (server, channelID, cleanup).
func testMirrorOrigin(t *testing.T, priv ed25519.PrivateKey, pub ed25519.PublicKey, originsList []string) (*httptest.Server, string, func()) {
	t.Helper()
	channelID := makeChannelID(pub)

	// Build signed metadata document
	metaDoc := map[string]interface{}{
		"v":       json.Number("1"),
		"seq":     json.Number("1000"),
		"id":      channelID,
		"name":    "Primary Channel",
		"stream":  "/tltv/v1/channels/" + channelID + "/stream.m3u8",
		"access":  "public",
		"status":  "active",
		"updated": "2026-04-01T00:00:00Z",
		"guide":   "/tltv/v1/channels/" + channelID + "/guide.json",
	}
	if len(originsList) > 0 {
		origins := make([]interface{}, len(originsList))
		for i, o := range originsList {
			origins[i] = o
		}
		metaDoc["origins"] = origins
	}
	signedMeta, err := signDocument(metaDoc, priv)
	if err != nil {
		t.Fatal(err)
	}
	metaBytes, _ := json.Marshal(signedMeta)

	// Build signed guide
	now := time.Now().UTC()
	guideDoc := map[string]interface{}{
		"v":       json.Number("1"),
		"seq":     json.Number("1000"),
		"id":      channelID,
		"from":    now.Truncate(24 * time.Hour).Format(timestampFormat),
		"until":   now.Truncate(24 * time.Hour).Add(24 * time.Hour).Format(timestampFormat),
		"updated": "2026-04-01T00:00:00Z",
		"entries": []interface{}{
			map[string]interface{}{
				"start": now.Truncate(24 * time.Hour).Format(timestampFormat),
				"end":   now.Truncate(24 * time.Hour).Add(24 * time.Hour).Format(timestampFormat),
				"title": "Primary Programming",
			},
		},
	}
	signedGuide, _ := signDocument(guideDoc, priv)
	guideBytes, _ := json.Marshal(signedGuide)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/.well-known/tltv":
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]interface{}{
				"protocol": "tltv",
				"versions": []int{1},
				"channels": []map[string]interface{}{
					{"id": channelID, "name": "Primary Channel"},
				},
			})
		case r.URL.Path == "/tltv/v1/channels/"+channelID:
			w.Header().Set("Content-Type", "application/json")
			w.Write(metaBytes)
		case r.URL.Path == "/tltv/v1/channels/"+channelID+"/guide.json":
			w.Header().Set("Content-Type", "application/json")
			w.Write(guideBytes)
		case strings.HasSuffix(r.URL.Path, "/icon.svg"):
			w.Header().Set("Content-Type", "image/svg+xml")
			w.Write([]byte("<svg></svg>"))
		case strings.HasSuffix(r.URL.Path, "/stream.m3u8"):
			w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
			w.Write([]byte("#EXTM3U\n#EXT-X-TARGETDURATION:2\n#EXTINF:2.0,\nseg0.ts\n"))
		case strings.HasSuffix(r.URL.Path, ".ts"):
			w.Header().Set("Content-Type", "video/mp2t")
			w.Write([]byte("test-segment-data"))
		default:
			http.NotFound(w, r)
		}
	}))

	return srv, channelID, func() { srv.Close() }
}

// ---------- State File Tests ----------

func TestMirrorState_SaveLoad(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")

	s := loadMirrorState(path, "TVtest123")
	if s.ChannelID != "TVtest123" {
		t.Errorf("ChannelID = %q, want TVtest123", s.ChannelID)
	}
	if s.Promoted {
		t.Error("fresh state should not be promoted")
	}
	if s.SeqFloor != 0 {
		t.Errorf("fresh SeqFloor = %d, want 0", s.SeqFloor)
	}

	// Update and save
	s.updateVerified(5000)
	if s.SeqFloor != 5000 {
		t.Errorf("after updateVerified(5000), SeqFloor = %d", s.SeqFloor)
	}

	// Reload
	s2 := loadMirrorState(path, "TVtest123")
	if s2.SeqFloor != 5000 {
		t.Errorf("reloaded SeqFloor = %d, want 5000", s2.SeqFloor)
	}
	if s2.LastVerified == "" {
		t.Error("reloaded LastVerified should not be empty")
	}
}

func TestMirrorState_Promote(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")

	s := loadMirrorState(path, "TVtest123")
	s.updateVerified(1000)

	seq := s.promote()
	if seq <= 1000 {
		t.Errorf("promoted seq = %d, should be > 1000", seq)
	}
	if !s.isPromoted() {
		t.Error("should be promoted after promote()")
	}
	if s.PromotedAt == "" {
		t.Error("PromotedAt should be set")
	}

	// Reload and check persistence
	s2 := loadMirrorState(path, "TVtest123")
	if !s2.Promoted {
		t.Error("promoted flag should persist")
	}
	if s2.SeqFloor != seq {
		t.Errorf("reloaded SeqFloor = %d, want %d", s2.SeqFloor, seq)
	}
}

func TestMirrorState_SeqFloorNeverDecreases(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")

	s := loadMirrorState(path, "TVtest123")
	s.updateVerified(5000)
	s.updateVerified(3000) // lower seq should be ignored
	if s.SeqFloor != 5000 {
		t.Errorf("SeqFloor should not decrease: got %d, want 5000", s.SeqFloor)
	}
}

func TestMirrorState_CorruptFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	os.WriteFile(path, []byte("not json"), 0600)

	s := loadMirrorState(path, "TVtest123")
	if s.ChannelID != "TVtest123" {
		t.Error("corrupt file should result in fresh state")
	}
	if s.SeqFloor != 0 {
		t.Error("corrupt file should result in zero seq floor")
	}
}

func TestMirrorState_ChannelIDMismatch(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")

	s := loadMirrorState(path, "TVfirst")
	s.updateVerified(5000)

	// Load with different channel ID
	s2 := loadMirrorState(path, "TVsecond")
	if s2.SeqFloor != 0 {
		t.Error("mismatched channel ID should result in fresh state")
	}
}

func TestMirrorState_MissingFile(t *testing.T) {
	s := loadMirrorState("/nonexistent/path.json", "TVtest")
	if s.ChannelID != "TVtest" {
		t.Error("missing file should produce fresh state")
	}
}

func TestMirrorState_MediaSeq(t *testing.T) {
	s := &mirrorState{ChannelID: "TVtest"}
	s.updateMediaSeq(100)
	s.updateMediaSeq(50) // should not decrease
	if s.getLastMediaSeq() != 100 {
		t.Errorf("LastMediaSeq = %d, want 100", s.getLastMediaSeq())
	}
	s.updateMediaSeq(200)
	if s.getLastMediaSeq() != 200 {
		t.Errorf("LastMediaSeq = %d, want 200", s.getLastMediaSeq())
	}
}

// ---------- Mirror Server Tests ----------

func TestMirrorServer_PassiveMetadata(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	channelID := makeChannelID(pub)

	// Create signed metadata
	metaDoc := map[string]interface{}{
		"v":       json.Number("1"),
		"seq":     json.Number("1000"),
		"id":      channelID,
		"name":    "Test Channel",
		"stream":  "/tltv/v1/channels/" + channelID + "/stream.m3u8",
		"access":  "public",
		"status":  "active",
		"updated": "2026-04-01T00:00:00Z",
	}
	signed, _ := signDocument(metaDoc, priv)
	metaBytes, _ := json.Marshal(signed)

	registry := &mirrorRegistry{}
	registry.SetChannel(&mirrorChannel{
		ChannelID: channelID,
		Name:      "Test Channel",
		Metadata:  metaBytes,
	})

	promoted := &atomic.Bool{}
	state := &mirrorState{ChannelID: channelID}
	srv := newMirrorServer(registry, newClient(true), nil, nil, nil, "mirror.test", channelID, promoted, state, priv)

	// GET metadata should return upstream bytes verbatim
	req := httptest.NewRequest("GET", "/tltv/v1/channels/"+channelID, nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if string(w.Body.Bytes()) != string(metaBytes) {
		t.Error("passive mode should serve upstream metadata verbatim")
	}
}

func TestMirrorServer_WellKnownListsInChannels(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	channelID := makeChannelID(pub)

	registry := &mirrorRegistry{}
	registry.SetChannel(&mirrorChannel{
		ChannelID: channelID,
		Name:      "Test Channel",
		Metadata:  []byte(`{}`),
	})

	promoted := &atomic.Bool{}
	state := &mirrorState{ChannelID: channelID}
	srv := newMirrorServer(registry, newClient(true), nil, nil, nil, "mirror.test", channelID, promoted, state, priv)

	req := httptest.NewRequest("GET", "/.well-known/tltv", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)

	channels, _ := resp["channels"].([]interface{})
	if len(channels) != 1 {
		t.Fatalf("channels count = %d, want 1", len(channels))
	}
	ch, _ := channels[0].(map[string]interface{})
	if ch["id"] != channelID {
		t.Errorf("channel id = %v, want %s", ch["id"], channelID)
	}

	relaying, _ := resp["relaying"].([]interface{})
	if len(relaying) != 0 {
		t.Errorf("relaying count = %d, want 0 (mirror is origin, not relay)", len(relaying))
	}
}

func TestMirrorServer_PrivateExcludedFromWellKnown(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	channelID := makeChannelID(pub)

	registry := &mirrorRegistry{}
	registry.SetChannel(&mirrorChannel{
		ChannelID:  channelID,
		Name:       "Private Channel",
		IsPrivate:  true,
		ServeToken: "secret",
		Metadata:   []byte(`{}`),
	})

	promoted := &atomic.Bool{}
	state := &mirrorState{ChannelID: channelID}
	srv := newMirrorServer(registry, newClient(true), nil, nil, nil, "mirror.test", channelID, promoted, state, priv)

	req := httptest.NewRequest("GET", "/.well-known/tltv", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)
	channels, _ := resp["channels"].([]interface{})
	if len(channels) != 0 {
		t.Errorf("private channel should be excluded from well-known, got %d", len(channels))
	}
}

func TestMirrorServer_PrivateTokenRequired(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	channelID := makeChannelID(pub)

	metaDoc := map[string]interface{}{
		"v":       json.Number("1"),
		"seq":     json.Number("1000"),
		"id":      channelID,
		"name":    "Private Channel",
		"stream":  "/tltv/v1/channels/" + channelID + "/stream.m3u8",
		"access":  "token",
		"status":  "active",
		"updated": "2026-04-01T00:00:00Z",
	}
	signed, _ := signDocument(metaDoc, priv)
	metaBytes, _ := json.Marshal(signed)

	registry := &mirrorRegistry{}
	registry.SetChannel(&mirrorChannel{
		ChannelID:  channelID,
		Name:       "Private Channel",
		IsPrivate:  true,
		ServeToken: "secret123",
		Metadata:   metaBytes,
	})

	promoted := &atomic.Bool{}
	state := &mirrorState{ChannelID: channelID}
	srv := newMirrorServer(registry, newClient(true), nil, nil, nil, "mirror.test", channelID, promoted, state, priv)

	// Without token → 403
	req := httptest.NewRequest("GET", "/tltv/v1/channels/"+channelID, nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != 403 {
		t.Errorf("without token: status = %d, want 403", w.Code)
	}

	// With correct token → 200
	req = httptest.NewRequest("GET", "/tltv/v1/channels/"+channelID+"?token=secret123", nil)
	w = httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Errorf("with correct token: status = %d, want 200", w.Code)
	}

	// With wrong token → 403
	req = httptest.NewRequest("GET", "/tltv/v1/channels/"+channelID+"?token=wrong", nil)
	w = httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != 403 {
		t.Errorf("with wrong token: status = %d, want 403", w.Code)
	}
}

func TestMirrorServer_WrongChannelID_404(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	channelID := makeChannelID(pub)

	registry := &mirrorRegistry{}
	registry.SetChannel(&mirrorChannel{
		ChannelID: channelID,
		Name:      "Test Channel",
		Metadata:  []byte(`{}`),
	})

	promoted := &atomic.Bool{}
	state := &mirrorState{ChannelID: channelID}
	srv := newMirrorServer(registry, newClient(true), nil, nil, nil, "mirror.test", channelID, promoted, state, priv)

	req := httptest.NewRequest("GET", "/tltv/v1/channels/TVwrongid123", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != 404 {
		t.Errorf("wrong channel ID: status = %d, want 404", w.Code)
	}
}

func TestMirrorServer_Health(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	channelID := makeChannelID(pub)

	registry := &mirrorRegistry{}
	registry.SetChannel(&mirrorChannel{ChannelID: channelID})

	promoted := &atomic.Bool{}
	state := &mirrorState{ChannelID: channelID}
	srv := newMirrorServer(registry, newClient(true), nil, nil, nil, "mirror.test", channelID, promoted, state, priv)

	// Passive mode
	req := httptest.NewRequest("GET", "/health", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	var health map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &health)
	if health["version"] != version {
		t.Errorf("version = %v, want %s", health["version"], version)
	}
	if health["mode"] != "passive" {
		t.Errorf("mode = %v, want passive", health["mode"])
	}

	// Promoted mode
	promoted.Store(true)
	w = httptest.NewRecorder()
	srv.ServeHTTP(w, httptest.NewRequest("GET", "/health", nil))
	json.Unmarshal(w.Body.Bytes(), &health)
	if health["mode"] != "promoted" {
		t.Errorf("mode = %v, want promoted", health["mode"])
	}
}

// ---------- Promotion Tests ----------

func TestMirrorPromote_SignsMetadata(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	channelID := makeChannelID(pub)

	// Create upstream metadata
	metaDoc := map[string]interface{}{
		"v":           json.Number("1"),
		"seq":         json.Number("1000"),
		"id":          channelID,
		"name":        "Test Channel",
		"description": "A test channel",
		"stream":      "/tltv/v1/channels/" + channelID + "/stream.m3u8",
		"access":      "public",
		"status":      "active",
		"updated":     "2026-04-01T00:00:00Z",
		"origins":     []interface{}{"primary.tv", "mirror.tv"},
	}
	signed, _ := signDocument(metaDoc, priv)
	metaBytes, _ := json.Marshal(signed)

	registry := &mirrorRegistry{}
	registry.SetChannel(&mirrorChannel{
		ChannelID:    channelID,
		Name:         "Test Channel",
		Metadata:     metaBytes,
		GuideEntries: defaultGuideEntries("Test Channel"),
	})

	dir := t.TempDir()
	state := loadMirrorState(filepath.Join(dir, "state.json"), channelID)
	state.updateVerified(1000)

	err := mirrorPromote(registry, state, priv, "mirror.tv", "")
	if err != nil {
		t.Fatalf("mirrorPromote: %v", err)
	}

	ch := registry.GetChannel()
	if ch.PromotedMeta == nil {
		t.Fatal("PromotedMeta should be set after promotion")
	}

	// Verify the promoted metadata (use UseNumber for seq parsing)
	promDoc, parseErr := readDocumentFromString(string(ch.PromotedMeta))
	if parseErr != nil {
		t.Fatalf("parsing promoted metadata: %v", parseErr)
	}

	// Verify signature
	if err := verifyDocument(promDoc, channelID); err != nil {
		t.Errorf("promoted metadata should verify: %v", err)
	}

	// Check seq > upstream
	promSeq, _ := promDoc["seq"].(json.Number)
	seqVal, _ := promSeq.Int64()
	if seqVal <= 1000 {
		t.Errorf("promoted seq = %d, should be > 1000", seqVal)
	}

	// Check upstream fields preserved
	if promDoc["name"] != "Test Channel" {
		t.Errorf("name not preserved: %v", promDoc["name"])
	}
	if promDoc["description"] != "A test channel" {
		t.Errorf("description not preserved: %v", promDoc["description"])
	}
}

func TestMirrorPromote_GuideReSigned(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	channelID := makeChannelID(pub)

	registry := &mirrorRegistry{}
	registry.SetChannel(&mirrorChannel{
		ChannelID:    channelID,
		Name:         "Test Channel",
		Metadata:     []byte(`{}`),
		GuideEntries: defaultGuideEntries("Test Channel"),
	})

	dir := t.TempDir()
	state := loadMirrorState(filepath.Join(dir, "state.json"), channelID)
	state.updateVerified(1000)

	err := mirrorPromote(registry, state, priv, "mirror.tv", "")
	if err != nil {
		t.Fatalf("mirrorPromote: %v", err)
	}

	ch := registry.GetChannel()
	if ch.PromotedGuide == nil {
		t.Fatal("PromotedGuide should be set after promotion")
	}

	// Verify the promoted guide
	var guideDoc map[string]interface{}
	json.Unmarshal(ch.PromotedGuide, &guideDoc)
	if err := verifyDocument(guideDoc, channelID); err != nil {
		t.Errorf("promoted guide should verify: %v", err)
	}
}

func TestMirrorServer_PromotedServesSignedMetadata(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	channelID := makeChannelID(pub)

	// Upstream metadata
	metaDoc := map[string]interface{}{
		"v":       json.Number("1"),
		"seq":     json.Number("1000"),
		"id":      channelID,
		"name":    "Test Channel",
		"stream":  "/tltv/v1/channels/" + channelID + "/stream.m3u8",
		"access":  "public",
		"status":  "active",
		"updated": "2026-04-01T00:00:00Z",
	}
	signed, _ := signDocument(metaDoc, priv)
	upstreamBytes, _ := json.Marshal(signed)

	// Promoted metadata (with higher seq)
	metaDoc2 := map[string]interface{}{
		"v":       json.Number("1"),
		"seq":     json.Number("2000"),
		"id":      channelID,
		"name":    "Test Channel",
		"stream":  "/tltv/v1/channels/" + channelID + "/stream.m3u8",
		"access":  "public",
		"status":  "active",
		"updated": time.Now().UTC().Format(timestampFormat),
	}
	signed2, _ := signDocument(metaDoc2, priv)
	promotedBytes, _ := json.Marshal(signed2)

	registry := &mirrorRegistry{}
	registry.SetChannel(&mirrorChannel{
		ChannelID:    channelID,
		Name:         "Test Channel",
		Metadata:     upstreamBytes,
		PromotedMeta: promotedBytes,
	})

	promoted := &atomic.Bool{}
	promoted.Store(true)
	state := &mirrorState{ChannelID: channelID}
	srv := newMirrorServer(registry, newClient(true), nil, nil, nil, "mirror.test", channelID, promoted, state, priv)

	req := httptest.NewRequest("GET", "/tltv/v1/channels/"+channelID, nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	// Should serve promoted metadata, not upstream
	if string(w.Body.Bytes()) != string(promotedBytes) {
		t.Error("promoted mode should serve self-signed metadata, not upstream")
	}
}

// ---------- Icon Tests ----------

func TestMirrorFetchIcon_DefaultIcon(t *testing.T) {
	data, ct, fn := mirrorFetchIcon(newClient(true), "localhost:9999", "TVtest", "", map[string]interface{}{})
	if ct != "image/svg+xml" {
		t.Errorf("content type = %q, want image/svg+xml", ct)
	}
	if fn != "icon.svg" {
		t.Errorf("filename = %q, want icon.svg", fn)
	}
	if len(data) == 0 {
		t.Error("default icon should not be empty")
	}
}

func TestMirrorServer_IconServing(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	channelID := makeChannelID(pub)

	registry := &mirrorRegistry{}
	registry.SetChannel(&mirrorChannel{
		ChannelID:    channelID,
		Name:         "Test Channel",
		Metadata:     []byte(`{}`),
		IconData:     []byte("<svg>test</svg>"),
		IconCT:       "image/svg+xml",
		IconFileName: "icon.svg",
	})

	promoted := &atomic.Bool{}
	state := &mirrorState{ChannelID: channelID}
	srv := newMirrorServer(registry, newClient(true), nil, nil, nil, "mirror.test", channelID, promoted, state, priv)

	req := httptest.NewRequest("GET", "/tltv/v1/channels/"+channelID+"/icon.svg", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if w.Header().Get("Content-Type") != "image/svg+xml" {
		t.Errorf("Content-Type = %q, want image/svg+xml", w.Header().Get("Content-Type"))
	}
	if string(w.Body.Bytes()) != "<svg>test</svg>" {
		t.Errorf("icon body mismatch")
	}
}

// ---------- Guide Tests ----------

func TestMirrorServer_PassiveGuide(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	channelID := makeChannelID(pub)

	// Create signed guide
	guideDoc := map[string]interface{}{
		"v":       json.Number("1"),
		"seq":     json.Number("1000"),
		"id":      channelID,
		"from":    "2026-04-01T00:00:00Z",
		"until":   "2026-04-02T00:00:00Z",
		"updated": "2026-04-01T00:00:00Z",
		"entries": []interface{}{
			map[string]interface{}{
				"start":      "2026-04-01T00:00:00Z",
				"end":        "2026-04-02T00:00:00Z",
				"title":      "Test Show",
				"relay_from": "TVsource123",
			},
		},
	}
	signedGuide, _ := signDocument(guideDoc, priv)
	guideBytes, _ := json.Marshal(signedGuide)

	registry := &mirrorRegistry{}
	registry.SetChannel(&mirrorChannel{
		ChannelID: channelID,
		Name:      "Test Channel",
		Metadata:  []byte(`{}`),
		Guide:     guideBytes,
		GuideEntries: []guideEntry{{
			Start:     "2026-04-01T00:00:00Z",
			End:       "2026-04-02T00:00:00Z",
			Title:     "Test Show",
			RelayFrom: "TVsource123",
		}},
	})

	promoted := &atomic.Bool{}
	state := &mirrorState{ChannelID: channelID}
	srv := newMirrorServer(registry, newClient(true), nil, nil, nil, "mirror.test", channelID, promoted, state, priv)

	req := httptest.NewRequest("GET", "/tltv/v1/channels/"+channelID+"/guide.json", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	// Passive mode should serve upstream guide verbatim (including relay_from)
	if string(w.Body.Bytes()) != string(guideBytes) {
		t.Error("passive mode should serve upstream guide verbatim")
	}
}

func TestMirrorServer_XMLTV(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	channelID := makeChannelID(pub)

	registry := &mirrorRegistry{}
	registry.SetChannel(&mirrorChannel{
		ChannelID: channelID,
		Name:      "Test Channel",
		Metadata:  []byte(`{}`),
		Guide:     []byte(`{}`),
		GuideEntries: []guideEntry{{
			Start: "2026-04-01T00:00:00Z",
			End:   "2026-04-02T00:00:00Z",
			Title: "Test Show",
		}},
	})

	promoted := &atomic.Bool{}
	state := &mirrorState{ChannelID: channelID}
	srv := newMirrorServer(registry, newClient(true), nil, nil, nil, "mirror.test", channelID, promoted, state, priv)

	req := httptest.NewRequest("GET", "/tltv/v1/channels/"+channelID+"/guide.xml", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if !strings.Contains(w.Header().Get("Content-Type"), "xml") {
		t.Error("Content-Type should contain xml")
	}
	if !strings.Contains(w.Body.String(), "Test Show") {
		t.Error("XMLTV output should contain show title")
	}
}

// ---------- Sign Guide Tests ----------

func TestMirrorSignGuide_Valid(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	channelID := makeChannelID(pub)

	entries := []guideEntry{{
		Start: "2026-04-01T00:00:00Z",
		End:   "2026-04-02T00:00:00Z",
		Title: "Test Show",
	}}

	data, err := mirrorSignGuide(channelID, 1001, entries, priv)
	if err != nil {
		t.Fatalf("mirrorSignGuide: %v", err)
	}

	var doc map[string]interface{}
	json.Unmarshal(data, &doc)

	if err := verifyDocument(doc, channelID); err != nil {
		t.Errorf("signed guide should verify: %v", err)
	}
}

// ---------- CORS Tests ----------

func TestMirrorServer_CORS(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	channelID := makeChannelID(pub)

	registry := &mirrorRegistry{}
	registry.SetChannel(&mirrorChannel{
		ChannelID: channelID,
		Metadata:  []byte(`{}`),
	})

	promoted := &atomic.Bool{}
	state := &mirrorState{ChannelID: channelID}
	srv := newMirrorServer(registry, newClient(true), nil, nil, nil, "mirror.test", channelID, promoted, state, priv)

	req := httptest.NewRequest("OPTIONS", "/tltv/v1/channels/"+channelID, nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != 204 {
		t.Errorf("OPTIONS: status = %d, want 204", w.Code)
	}
	if w.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Error("missing CORS header")
	}
}

func TestMirrorServer_MethodNotAllowed(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	channelID := makeChannelID(pub)

	registry := &mirrorRegistry{}
	registry.SetChannel(&mirrorChannel{ChannelID: channelID})

	promoted := &atomic.Bool{}
	state := &mirrorState{ChannelID: channelID}
	srv := newMirrorServer(registry, newClient(true), nil, nil, nil, "mirror.test", channelID, promoted, state, priv)

	req := httptest.NewRequest("POST", "/tltv/v1/foo", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != 400 {
		t.Errorf("POST to protocol path: status = %d, want 400", w.Code)
	}
}

// ---------- /api/info Normalization ----------

func TestMirror_APIInfo_Passive(t *testing.T) {
	setupLogging("error", "", "", "test")
	pub, priv, _ := ed25519.GenerateKey(nil)
	channelID := makeChannelID(pub)

	// Build signed metadata + guide with known fields.
	metaDoc := map[string]interface{}{
		"v":       json.Number("1"),
		"seq":     json.Number("100"),
		"id":      channelID,
		"name":    "Test Mirror",
		"stream":  "/tltv/v1/channels/" + channelID + "/stream.m3u8",
		"updated": time.Now().UTC().Add(-1 * time.Minute).Format(timestampFormat),
	}
	signedMeta, _ := signDocument(metaDoc, priv)
	metaBytes, _ := json.Marshal(signedMeta)

	guideDoc := map[string]interface{}{
		"v":  json.Number("1"),
		"id": channelID,
		"entries": []map[string]interface{}{
			{
				"start": time.Now().Truncate(24 * time.Hour).Format(timestampFormat),
				"end":   time.Now().Truncate(24 * time.Hour).Add(24 * time.Hour).Format(timestampFormat),
				"title": "Mirror Programming",
			},
		},
	}
	signedGuide, _ := signDocument(guideDoc, priv)
	guideBytes, _ := json.Marshal(signedGuide)

	registry := &mirrorRegistry{}
	registry.SetChannel(&mirrorChannel{
		ChannelID: channelID,
		Name:      "Test Mirror",
		Metadata:  metaBytes,
		Guide:     guideBytes,
	})

	infoFn := mirrorViewerBuildInfo(registry, channelID, "mirror.example.com")
	info := infoFn("")

	// Check fields from viewerBuildInfo (channel_name, verified, guide).
	if info["channel_id"] != channelID {
		t.Errorf("channel_id = %v, want %s", info["channel_id"], channelID)
	}
	if info["channel_name"] != "Test Mirror" {
		t.Errorf("channel_name = %v, want Test Mirror", info["channel_name"])
	}
	if info["verified"] != true {
		t.Errorf("verified = %v, want true", info["verified"])
	}
	if info["guide"] == nil {
		t.Error("guide should be present")
	}
	if info["metadata"] == nil {
		t.Error("metadata should be present")
	}

	// Check mirror-specific fields.
	if info["stream_src"] == nil {
		t.Error("stream_src should be present")
	}
	if info["xmltv_url"] == nil {
		t.Error("xmltv_url should be present")
	}
	if info["tltv_uri"] == nil {
		t.Error("tltv_uri should be present")
	}
	uri, _ := info["tltv_uri"].(string)
	if !strings.Contains(uri, channelID) || !strings.Contains(uri, "mirror.example.com") {
		t.Errorf("tltv_uri = %q, expected to contain channel ID and hostname", uri)
	}

	// Channels list.
	chs, ok := info["channels"].([]interface{})
	if !ok || len(chs) != 1 {
		t.Fatalf("channels = %v, want 1-element slice", info["channels"])
	}
}

func TestMirror_APIInfo_Promoted(t *testing.T) {
	setupLogging("error", "", "", "test")
	pub, priv, _ := ed25519.GenerateKey(nil)
	channelID := makeChannelID(pub)

	// Build upstream (passive) metadata.
	metaDoc := map[string]interface{}{
		"v":       json.Number("1"),
		"seq":     json.Number("100"),
		"id":      channelID,
		"name":    "Test Mirror",
		"stream":  "/tltv/v1/channels/" + channelID + "/stream.m3u8",
		"updated": time.Now().UTC().Add(-10 * time.Minute).Format(timestampFormat),
	}
	signedMeta, _ := signDocument(metaDoc, priv)
	metaBytes, _ := json.Marshal(signedMeta)

	// Build promoted metadata (different seq).
	promotedMetaDoc := map[string]interface{}{
		"v":       json.Number("1"),
		"seq":     json.Number("200"),
		"id":      channelID,
		"name":    "Test Mirror",
		"stream":  "/tltv/v1/channels/" + channelID + "/stream.m3u8",
		"updated": time.Now().UTC().Add(-1 * time.Minute).Format(timestampFormat),
	}
	signedPromoted, _ := signDocument(promotedMetaDoc, priv)
	promotedBytes, _ := json.Marshal(signedPromoted)

	// Build promoted guide.
	guideDoc := map[string]interface{}{
		"v":       json.Number("1"),
		"id":      channelID,
		"entries": []map[string]interface{}{},
	}
	signedGuide, _ := signDocument(guideDoc, priv)
	guideBytes, _ := json.Marshal(signedGuide)

	registry := &mirrorRegistry{}
	registry.SetChannel(&mirrorChannel{
		ChannelID:     channelID,
		Name:          "Test Mirror",
		Metadata:      metaBytes,
		PromotedMeta:  promotedBytes,
		PromotedGuide: guideBytes,
	})

	infoFn := mirrorViewerBuildInfo(registry, channelID, "mirror.example.com")
	info := infoFn("")

	// In promoted mode, should use promoted metadata.
	meta, ok := info["metadata"].(map[string]interface{})
	if !ok {
		t.Fatal("metadata not a map")
	}
	// Promoted metadata should have seq=200.
	if seq, ok := meta["seq"]; ok {
		seqStr := fmt.Sprintf("%v", seq)
		if seqStr != "200" {
			t.Errorf("promoted metadata seq = %v, want 200", seq)
		}
	}
}

func TestMirror_APIInfo_NoChannel(t *testing.T) {
	setupLogging("error", "", "", "test")
	registry := &mirrorRegistry{}

	infoFn := mirrorViewerBuildInfo(registry, "TVtest123", "mirror.example.com")
	info := infoFn("")

	// Should return empty channels list.
	chs, ok := info["channels"].([]interface{})
	if !ok || len(chs) != 0 {
		t.Errorf("channels = %v, want empty slice", info["channels"])
	}
}
