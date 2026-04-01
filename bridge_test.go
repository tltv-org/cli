package main

import (
	"crypto/ed25519"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// ---------- Test Helpers ----------

// testBridgeRegistry creates a registry in a temp dir with one public channel.
func testBridgeRegistry(t *testing.T) *bridgeRegistry {
	t.Helper()
	dir := t.TempDir()
	r := newBridgeRegistry(dir, "", nil)

	channels := []bridgeChannel{{
		ID:       "ch1",
		Name:     "Test Channel",
		Stream:   "http://upstream.example.com/live/stream.m3u8",
		Tags:     []string{"test"},
		Language: "en",
	}}
	if err := r.UpdateChannels(channels); err != nil {
		t.Fatal(err)
	}
	return r
}

// testBridgeChannelID returns the TLTV channel ID for "ch1" in the registry.
func testBridgeChannelID(t *testing.T, r *bridgeRegistry) string {
	t.Helper()
	channels := r.ListChannels()
	if len(channels) == 0 {
		t.Fatal("no channels registered")
	}
	return channels[0].ChannelID
}

// ---------- M3U Parsing ----------

func TestBridgeParseM3U_FullAttributes(t *testing.T) {
	m3u := `#EXTM3U
#EXTINF:-1 tvg-id="ch1" tvg-name="Channel One" tvg-logo="http://logo.png" group-title="News",Channel One
http://example.com/ch1/stream.m3u8
#EXTINF:-1 tvg-id="ch2" tvg-name="Channel Two" group-title="Sports",Channel Two
http://example.com/ch2/stream.m3u8
`
	channels := bridgeParseM3U(m3u, "http://provider.com/playlist.m3u")

	if len(channels) != 2 {
		t.Fatalf("expected 2 channels, got %d", len(channels))
	}

	ch := channels[0]
	if ch.ID != "ch1" {
		t.Errorf("ch1 ID = %q, want %q", ch.ID, "ch1")
	}
	if ch.Name != "Channel One" {
		t.Errorf("ch1 Name = %q, want %q", ch.Name, "Channel One")
	}
	if ch.Logo != "http://logo.png" {
		t.Errorf("ch1 Logo = %q, want %q", ch.Logo, "http://logo.png")
	}
	if len(ch.Tags) != 1 || ch.Tags[0] != "News" {
		t.Errorf("ch1 Tags = %v, want [News]", ch.Tags)
	}
	if ch.Stream != "http://example.com/ch1/stream.m3u8" {
		t.Errorf("ch1 Stream = %q, want absolute URL", ch.Stream)
	}

	if channels[1].ID != "ch2" {
		t.Errorf("ch2 ID = %q, want %q", channels[1].ID, "ch2")
	}
}

func TestBridgeParseM3U_BareEXTINF(t *testing.T) {
	m3u := `#EXTM3U
#EXTINF:-1,My Channel
http://example.com/stream.m3u8
`
	channels := bridgeParseM3U(m3u, "")

	if len(channels) != 1 {
		t.Fatalf("expected 1 channel, got %d", len(channels))
	}
	if channels[0].Name != "My Channel" {
		t.Errorf("Name = %q, want %q", channels[0].Name, "My Channel")
	}
	if channels[0].ID != "My_Channel" {
		t.Errorf("ID = %q, want sanitized name", channels[0].ID)
	}
}

func TestBridgeParseM3U_MissingTvgId(t *testing.T) {
	m3u := `#EXTM3U
#EXTINF:-1 tvg-name="Test",Test
http://example.com/stream.m3u8
`
	channels := bridgeParseM3U(m3u, "")

	if len(channels) != 1 {
		t.Fatalf("expected 1 channel, got %d", len(channels))
	}
	// ID should be generated from name
	if channels[0].ID != "Test" {
		t.Errorf("ID = %q, want sanitized name %q", channels[0].ID, "Test")
	}
}

// ---------- XMLTV Parsing ----------

func TestBridgeParseXMLTVGuide(t *testing.T) {
	xmltv := `<?xml version="1.0" encoding="UTF-8"?>
<tv>
  <channel id="ch1">
    <display-name>Channel One</display-name>
  </channel>
  <programme start="20260315120000 +0000" stop="20260315130000 +0000" channel="ch1">
    <title>News Hour</title>
    <desc>Daily news</desc>
    <category>news</category>
  </programme>
  <programme start="20260315130000 +0000" stop="20260315140000 +0000" channel="ch1">
    <title>Sports</title>
  </programme>
</tv>`

	guide, err := bridgeParseXMLTVGuide([]byte(xmltv))
	if err != nil {
		t.Fatal(err)
	}

	entries, ok := guide["ch1"]
	if !ok {
		t.Fatal("no entries for ch1")
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}

	if entries[0].Start != "2026-03-15T12:00:00Z" {
		t.Errorf("entry 0 Start = %q, want %q", entries[0].Start, "2026-03-15T12:00:00Z")
	}
	if entries[0].End != "2026-03-15T13:00:00Z" {
		t.Errorf("entry 0 End = %q, want %q", entries[0].End, "2026-03-15T13:00:00Z")
	}
	if entries[0].Title != "News Hour" {
		t.Errorf("entry 0 Title = %q, want %q", entries[0].Title, "News Hour")
	}
	if entries[0].Description != "Daily news" {
		t.Errorf("entry 0 Description = %q", entries[0].Description)
	}
	if entries[0].Category != "news" {
		t.Errorf("entry 0 Category = %q", entries[0].Category)
	}
}

func TestBridgeXMLTVToISO(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"20260315120000 +0000", "2026-03-15T12:00:00Z"},
		{"20260101000000 +0000", "2026-01-01T00:00:00Z"},
		{"20261231235959 +0000", "2026-12-31T23:59:59Z"},
		{"20260315120000 +0500", "2026-03-15T07:00:00Z"}, // timezone offset
	}
	for _, tt := range tests {
		got, err := bridgeXMLTVToISO(tt.in)
		if err != nil {
			t.Errorf("bridgeXMLTVToISO(%q) error: %v", tt.in, err)
			continue
		}
		if got != tt.want {
			t.Errorf("bridgeXMLTVToISO(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestBridgeISOToXMLTV(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"2026-03-15T12:00:00Z", "20260315120000 +0000"},
		{"2026-01-01T00:00:00Z", "20260101000000 +0000"},
	}
	for _, tt := range tests {
		got := bridgeISOToXMLTV(tt.in)
		if got != tt.want {
			t.Errorf("bridgeISOToXMLTV(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// ---------- JSON Parsing ----------

func TestBridgeParseJSONChannels(t *testing.T) {
	data := `[
		{"id": "ch1", "name": "Channel One", "stream": "http://example.com/ch1.m3u8"},
		{"id": "ch2", "name": "Channel Two", "stream": "http://example.com/ch2.m3u8", "access": "token", "token": "secret"}
	]`
	channels, err := bridgeParseJSONChannels([]byte(data))
	if err != nil {
		t.Fatal(err)
	}
	if len(channels) != 2 {
		t.Fatalf("expected 2 channels, got %d", len(channels))
	}
	if channels[0].Name != "Channel One" {
		t.Errorf("ch0 Name = %q", channels[0].Name)
	}
	if channels[1].Access != "token" || channels[1].Token != "secret" {
		t.Errorf("ch1 Access=%q Token=%q", channels[1].Access, channels[1].Token)
	}
}

func TestBridgeParseJSONGuide(t *testing.T) {
	data := `[
		{"channel": "ch1", "start": "2026-03-15T12:00:00Z", "end": "2026-03-15T13:00:00Z", "title": "Show A"},
		{"channel": "ch1", "start": "2026-03-15T13:00:00Z", "end": "2026-03-15T14:00:00Z", "title": "Show B"},
		{"channel": "ch2", "start": "2026-03-15T12:00:00Z", "end": "2026-03-15T13:00:00Z", "title": "Show C"}
	]`
	guide, err := bridgeParseJSONGuide([]byte(data))
	if err != nil {
		t.Fatal(err)
	}
	if len(guide["ch1"]) != 2 {
		t.Errorf("ch1 entries: %d, want 2", len(guide["ch1"]))
	}
	if len(guide["ch2"]) != 1 {
		t.Errorf("ch2 entries: %d, want 1", len(guide["ch2"]))
	}
}

// ---------- Source Auto-Detection ----------

func TestBridgeIsM3UPlaylist(t *testing.T) {
	// IPTV M3U
	if !bridgeIsM3UPlaylist("#EXTM3U\n#EXTINF:-1,Channel\nhttp://example.com/stream.m3u8\n") {
		t.Error("should detect IPTV M3U as M3U playlist")
	}

	// HLS media playlist (has TARGETDURATION)
	if bridgeIsM3UPlaylist("#EXTM3U\n#EXT-X-TARGETDURATION:6\n#EXTINF:6.0,\nseg-001.ts\n") {
		t.Error("should NOT detect HLS media playlist as M3U playlist")
	}

	// HLS master playlist (has STREAM-INF)
	if bridgeIsM3UPlaylist("#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=2000000\nvideo.m3u8\n") {
		t.Error("should NOT detect HLS master playlist as M3U playlist")
	}

	// No EXTINF at all
	if bridgeIsM3UPlaylist("just some text content") {
		t.Error("should NOT detect plain text as M3U playlist")
	}
}

// ---------- Directory Scanning ----------

func TestBridgeScanDirectory(t *testing.T) {
	dir := t.TempDir()

	// Create a .m3u8 file
	os.WriteFile(filepath.Join(dir, "test-channel.m3u8"), []byte("#EXTM3U\n#EXTINF:2.0,\nseg.ts\n"), 0644)

	// Create a sidecar JSON
	sidecar := `{"name": "Test Channel", "description": "A test", "tags": ["demo"], "guide": [{"start": "2026-03-31T00:00:00Z", "end": "2026-04-01T00:00:00Z", "title": "Color Bars"}]}`
	os.WriteFile(filepath.Join(dir, "test-channel.json"), []byte(sidecar), 0644)

	// Create another .m3u8 without sidecar
	os.WriteFile(filepath.Join(dir, "bare.m3u8"), []byte("#EXTM3U\n"), 0644)

	channels, guide, err := bridgeScanDirectory(dir)
	if err != nil {
		t.Fatal(err)
	}

	if len(channels) != 2 {
		t.Fatalf("expected 2 channels, got %d", len(channels))
	}

	// Find the test-channel
	var testCh *bridgeChannel
	for i := range channels {
		if channels[i].ID == "test-channel" {
			testCh = &channels[i]
		}
	}
	if testCh == nil {
		t.Fatal("test-channel not found")
	}

	if testCh.Name != "Test Channel" {
		t.Errorf("Name = %q, want %q", testCh.Name, "Test Channel")
	}
	if testCh.Description != "A test" {
		t.Errorf("Description = %q", testCh.Description)
	}
	if len(testCh.Tags) != 1 || testCh.Tags[0] != "demo" {
		t.Errorf("Tags = %v", testCh.Tags)
	}

	// Check guide from sidecar
	if entries, ok := guide["test-channel"]; !ok || len(entries) != 1 {
		t.Errorf("guide entries for test-channel: %v", guide["test-channel"])
	} else if entries[0].Title != "Color Bars" {
		t.Errorf("guide entry title = %q", entries[0].Title)
	}
}

// ---------- HLS Manifest Rewriting ----------

func TestBridgeRewriteManifest_AbsoluteToRelative(t *testing.T) {
	manifest := "#EXTM3U\n#EXTINF:2.0,\nhttp://upstream.example.com/live/seg-001.ts\n#EXTINF:2.0,\nhttp://upstream.example.com/live/seg-002.ts\n"
	result := string(bridgeRewriteManifest("http://upstream.example.com/live/stream.m3u8", []byte(manifest), ""))

	if strings.Contains(result, "http://") {
		t.Error("result still contains absolute URLs")
	}
	if !strings.Contains(result, "seg-001.ts") {
		t.Error("missing seg-001.ts")
	}
	if !strings.Contains(result, "seg-002.ts") {
		t.Error("missing seg-002.ts")
	}
}

func TestBridgeRewriteManifest_RelativePassThrough(t *testing.T) {
	manifest := "#EXTM3U\n#EXTINF:2.0,\nseg-001.ts\n"
	result := string(bridgeRewriteManifest("http://upstream.example.com/live/stream.m3u8", []byte(manifest), ""))

	if !strings.Contains(result, "seg-001.ts") {
		t.Error("relative URI should pass through")
	}
}

func TestBridgeRewriteManifest_DifferentOriginLeftAbsolute(t *testing.T) {
	manifest := "#EXTM3U\n#EXTINF:2.0,\nhttp://cdn.other.com/seg-001.ts\n"
	result := string(bridgeRewriteManifest("http://upstream.example.com/live/stream.m3u8", []byte(manifest), ""))

	if !strings.Contains(result, "http://cdn.other.com/seg-001.ts") {
		t.Error("different-origin URL should be left absolute")
	}
}

func TestBridgeRewriteManifest_TokenInjection(t *testing.T) {
	manifest := "#EXTM3U\n#EXTINF:2.0,\nseg-001.ts\n#EXTINF:2.0,\nseg-002.ts\n"
	result := string(bridgeRewriteManifest("", []byte(manifest), "secret123"))

	if !strings.Contains(result, "seg-001.ts?token=secret123") {
		t.Error("missing token on seg-001.ts")
	}
	if !strings.Contains(result, "seg-002.ts?token=secret123") {
		t.Error("missing token on seg-002.ts")
	}
}

func TestBridgeRewriteManifest_TokenOnAbsoluteRewritten(t *testing.T) {
	manifest := "#EXTM3U\n#EXTINF:2.0,\nhttp://upstream.example.com/live/seg-001.ts\n"
	result := string(bridgeRewriteManifest("http://upstream.example.com/live/stream.m3u8", []byte(manifest), "tok"))

	if strings.Contains(result, "http://") {
		t.Error("should be rewritten to relative")
	}
	if !strings.Contains(result, "seg-001.ts?token=tok") {
		t.Error("should have token appended")
	}
}

func TestBridgeRewriteManifest_NoTokenForPublic(t *testing.T) {
	manifest := "#EXTM3U\n#EXTINF:2.0,\nseg-001.ts\n"
	result := string(bridgeRewriteManifest("", []byte(manifest), ""))

	if strings.Contains(result, "token=") {
		t.Error("public channels should have no token")
	}
}

func TestBridgeRewriteManifest_MapURI(t *testing.T) {
	manifest := "#EXTM3U\n#EXT-X-MAP:URI=\"http://upstream.example.com/live/init.mp4\"\n#EXTINF:2.0,\nseg.ts\n"
	result := string(bridgeRewriteManifest("http://upstream.example.com/live/stream.m3u8", []byte(manifest), ""))

	if !strings.Contains(result, `URI="init.mp4"`) {
		t.Errorf("MAP URI not rewritten to relative: %s", result)
	}
}

func TestBridgeRewriteManifest_KeyURI(t *testing.T) {
	manifest := "#EXTM3U\n#EXT-X-KEY:METHOD=AES-128,URI=\"http://upstream.example.com/live/key.bin\"\n"
	result := string(bridgeRewriteManifest("http://upstream.example.com/live/stream.m3u8", []byte(manifest), ""))

	if !strings.Contains(result, `URI="key.bin"`) {
		t.Errorf("KEY URI not rewritten: %s", result)
	}
}

func TestBridgeRewriteManifest_MediaURI(t *testing.T) {
	manifest := "#EXTM3U\n#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID=\"audio\",URI=\"http://upstream.example.com/live/audio.m3u8\"\n"
	result := string(bridgeRewriteManifest("http://upstream.example.com/live/stream.m3u8", []byte(manifest), ""))

	if !strings.Contains(result, `URI="audio.m3u8"`) {
		t.Errorf("MEDIA URI not rewritten: %s", result)
	}
}

func TestBridgeRewriteManifest_TagURIWithToken(t *testing.T) {
	manifest := "#EXTM3U\n" +
		"#EXT-X-MAP:URI=\"init.mp4\"\n" +
		"#EXT-X-KEY:METHOD=AES-128,URI=\"key.bin\",IV=0x1234\n" +
		"#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID=\"audio\",URI=\"audio.m3u8\"\n" +
		"#EXTINF:2.0,\nseg-001.ts\n"
	result := string(bridgeRewriteManifest("", []byte(manifest), "secret"))

	if !strings.Contains(result, `URI="init.mp4?token=secret"`) {
		t.Errorf("MAP URI missing token: %s", result)
	}
	if !strings.Contains(result, `URI="key.bin?token=secret"`) {
		t.Errorf("KEY URI missing token: %s", result)
	}
	if !strings.Contains(result, `URI="audio.m3u8?token=secret"`) {
		t.Errorf("MEDIA URI missing token: %s", result)
	}
	if !strings.Contains(result, "seg-001.ts?token=secret") {
		t.Errorf("segment missing token: %s", result)
	}
}

func TestBridgeRewriteManifest_NonURITagsUntouched(t *testing.T) {
	manifest := "#EXTM3U\n#EXT-X-VERSION:3\n#EXT-X-TARGETDURATION:2\n#EXT-X-MEDIA-SEQUENCE:42\n#EXTINF:2.0,\nseg.ts\n"
	result := string(bridgeRewriteManifest("", []byte(manifest), "tok"))

	if !strings.Contains(result, "#EXT-X-VERSION:3") {
		t.Error("VERSION tag modified")
	}
	if !strings.Contains(result, "#EXT-X-TARGETDURATION:2") {
		t.Error("TARGETDURATION tag modified")
	}
	if !strings.Contains(result, "#EXT-X-MEDIA-SEQUENCE:42") {
		t.Error("MEDIA-SEQUENCE tag modified")
	}
}

func TestBridgeRewriteManifest_UpstreamQueryStripped(t *testing.T) {
	manifest := "#EXTM3U\n#EXTINF:2.0,\nhttp://upstream.example.com/live/seg-001.ts\n"
	result := string(bridgeRewriteManifest("http://upstream.example.com/live/stream.m3u8?key=abc&auth=xyz", []byte(manifest), ""))

	if strings.Contains(result, "http://") {
		t.Error("should still rewrite to relative even with query on manifest URL")
	}
	if !strings.Contains(result, "seg-001.ts") {
		t.Error("missing segment")
	}
}

func TestBridgeRewriteManifest_TokenOnVariantPlaylist(t *testing.T) {
	manifest := "#EXTM3U\n" +
		"#EXT-X-STREAM-INF:BANDWIDTH=2000000,RESOLUTION=1280x720\n" +
		"video-720p.m3u8\n" +
		"#EXT-X-STREAM-INF:BANDWIDTH=800000,RESOLUTION=640x360\n" +
		"video-360p.m3u8\n"
	result := string(bridgeRewriteManifest("", []byte(manifest), "tok"))

	if !strings.Contains(result, "video-720p.m3u8?token=tok") {
		t.Error("variant 720p missing token")
	}
	if !strings.Contains(result, "video-360p.m3u8?token=tok") {
		t.Error("variant 360p missing token")
	}
}

func TestBridgeRewriteManifest_FullPrivateScenario(t *testing.T) {
	manifest := "#EXTM3U\n" +
		"#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID=\"audio\",URI=\"http://upstream.example.com/live/audio.m3u8\"\n" +
		"#EXT-X-STREAM-INF:BANDWIDTH=2000000\n" +
		"http://upstream.example.com/live/video.m3u8\n" +
		"#EXT-X-STREAM-INF:BANDWIDTH=800000\n" +
		"http://upstream.example.com/live/video-low.m3u8\n"
	result := string(bridgeRewriteManifest("http://upstream.example.com/live/master.m3u8", []byte(manifest), "mytoken"))

	if strings.Contains(result, "http://") {
		t.Error("all absolute URLs should be rewritten to relative")
	}
	if !strings.Contains(result, `URI="audio.m3u8?token=mytoken"`) {
		t.Error("audio URI missing token")
	}
	if !strings.Contains(result, "video.m3u8?token=mytoken") {
		t.Error("video URI missing token")
	}
	if !strings.Contains(result, "video-low.m3u8?token=mytoken") {
		t.Error("video-low URI missing token")
	}
}

func TestBridgeRewriteManifest_FullPrivateMediaPlaylist(t *testing.T) {
	manifest := "#EXTM3U\n" +
		"#EXT-X-TARGETDURATION:2\n" +
		"#EXT-X-MEDIA-SEQUENCE:100\n" +
		"#EXT-X-MAP:URI=\"init.mp4\"\n" +
		"#EXT-X-KEY:METHOD=AES-128,URI=\"key.bin\",IV=0x1234\n" +
		"#EXTINF:2.0,\nseg-100.m4s\n" +
		"#EXTINF:2.0,\nseg-101.m4s\n" +
		"#EXTINF:2.0,\nseg-102.m4s\n"
	result := string(bridgeRewriteManifest("", []byte(manifest), "tok"))

	if !strings.Contains(result, `URI="init.mp4?token=tok"`) {
		t.Error("MAP URI missing token")
	}
	if !strings.Contains(result, `URI="key.bin?token=tok"`) {
		t.Error("KEY URI missing token")
	}
	for _, seg := range []string{"seg-100.m4s", "seg-101.m4s", "seg-102.m4s"} {
		if !strings.Contains(result, seg+"?token=tok") {
			t.Errorf("segment %s missing token", seg)
		}
	}
	// Non-URI tags preserved
	if !strings.Contains(result, "#EXT-X-TARGETDURATION:2") {
		t.Error("TARGETDURATION modified")
	}
	if !strings.Contains(result, "#EXT-X-MEDIA-SEQUENCE:100") {
		t.Error("MEDIA-SEQUENCE modified")
	}
	if !strings.Contains(result, "IV=0x1234") {
		t.Error("KEY IV not preserved")
	}
}

// ---------- Token Helpers ----------

func TestBridgeAppendToken_NoExistingQuery(t *testing.T) {
	got := bridgeAppendToken("seg-001.ts", "abc")
	if got != "seg-001.ts?token=abc" {
		t.Errorf("got %q", got)
	}
}

func TestBridgeAppendToken_ExistingQuery(t *testing.T) {
	got := bridgeAppendToken("seg-001.ts?quality=high", "abc")
	if got != "seg-001.ts?quality=high&token=abc" {
		t.Errorf("got %q", got)
	}
}

func TestBridgeMakeRelative_SameBase(t *testing.T) {
	got := bridgeMakeRelative("http://example.com/live/seg-001.ts", "http://example.com/live/")
	if got != "seg-001.ts" {
		t.Errorf("got %q", got)
	}
}

func TestBridgeMakeRelative_DifferentBase(t *testing.T) {
	got := bridgeMakeRelative("http://cdn.other.com/seg-001.ts", "http://example.com/live/")
	if got != "http://cdn.other.com/seg-001.ts" {
		t.Errorf("got %q, should be unchanged", got)
	}
}

func TestBridgeMakeRelative_AlreadyRelative(t *testing.T) {
	got := bridgeMakeRelative("seg-001.ts", "http://example.com/live/")
	if got != "seg-001.ts" {
		t.Errorf("got %q, should be unchanged", got)
	}
}

// ---------- Protocol Endpoints ----------

func TestBridgeNodeInfo(t *testing.T) {
	r := testBridgeRegistry(t)
	srv := newBridgeServer(r)

	req := httptest.NewRequest("GET", "/.well-known/tltv", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)

	if resp["protocol"] != "tltv" {
		t.Errorf("protocol = %v", resp["protocol"])
	}

	channels, ok := resp["channels"].([]interface{})
	if !ok || len(channels) != 1 {
		t.Fatalf("expected 1 channel, got %v", resp["channels"])
	}

	ch := channels[0].(map[string]interface{})
	if ch["name"] != "Test Channel" {
		t.Errorf("channel name = %v", ch["name"])
	}
	id, _ := ch["id"].(string)
	if !strings.HasPrefix(id, "TV") {
		t.Errorf("channel ID should start with TV, got %q", id)
	}
}

func TestBridgeChannelMetadata(t *testing.T) {
	r := testBridgeRegistry(t)
	srv := newBridgeServer(r)
	id := testBridgeChannelID(t, r)

	req := httptest.NewRequest("GET", "/tltv/v1/channels/"+id, nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	ct := w.Header().Get("Content-Type")
	if !strings.Contains(ct, "application/json") {
		t.Errorf("Content-Type = %q", ct)
	}

	var doc map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &doc)

	if doc["name"] != "Test Channel" {
		t.Errorf("name = %v", doc["name"])
	}
	if doc["id"] != id {
		t.Errorf("id = %v, want %s", doc["id"], id)
	}
	if _, ok := doc["signature"]; !ok {
		t.Error("missing signature field")
	}

	// Verify signature
	pubKey, err := parseChannelID(id)
	if err != nil {
		t.Fatal(err)
	}

	sigStr, _ := doc["signature"].(string)
	sigBytes, err := b58Decode(sigStr)
	if err != nil {
		t.Fatal(err)
	}

	// Remove signature for verification
	delete(doc, "signature")
	payload, err := canonicalJSON(doc)
	if err != nil {
		t.Fatal(err)
	}
	if !ed25519.Verify(pubKey, payload, sigBytes) {
		t.Error("signature verification failed")
	}
}

func TestBridgeChannelMetadata_HasGuide(t *testing.T) {
	r := testBridgeRegistry(t)
	srv := newBridgeServer(r)
	id := testBridgeChannelID(t, r)

	req := httptest.NewRequest("GET", "/tltv/v1/channels/"+id, nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	var doc map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &doc)

	guide, ok := doc["guide"].(string)
	if !ok || guide == "" {
		t.Error("metadata should have non-empty guide field")
	}
}

func TestBridgeDefaultGuide(t *testing.T) {
	r := testBridgeRegistry(t)
	srv := newBridgeServer(r)
	id := testBridgeChannelID(t, r)

	req := httptest.NewRequest("GET", "/tltv/v1/channels/"+id+"/guide.json", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	var doc map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &doc)

	if _, ok := doc["signature"]; !ok {
		t.Error("guide should have signature")
	}

	entries, ok := doc["entries"].([]interface{})
	if !ok || len(entries) == 0 {
		t.Fatal("guide should have entries")
	}

	entry := entries[0].(map[string]interface{})
	if entry["title"] != "Test Channel" {
		t.Errorf("default guide entry title = %v, want channel name", entry["title"])
	}
}

func TestBridgeGuideXML(t *testing.T) {
	r := testBridgeRegistry(t)
	srv := newBridgeServer(r)
	id := testBridgeChannelID(t, r)

	req := httptest.NewRequest("GET", "/tltv/v1/channels/"+id+"/guide.xml", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	body := w.Body.String()
	if !strings.Contains(body, "<tv>") {
		t.Error("missing <tv> tag")
	}
	if !strings.Contains(body, "Test Channel") {
		t.Error("missing channel name in XMLTV")
	}

	ct := w.Header().Get("Content-Type")
	if !strings.Contains(ct, "application/xml") {
		t.Errorf("Content-Type = %q", ct)
	}
}

func TestBridgeChannelNotFound(t *testing.T) {
	r := testBridgeRegistry(t)
	srv := newBridgeServer(r)

	req := httptest.NewRequest("GET", "/tltv/v1/channels/TVfakeChannelIdThatDoesNotExistInRegistryXXXXX", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != 404 {
		t.Fatalf("status = %d, want 404", w.Code)
	}
}

func TestBridgePeers_Empty(t *testing.T) {
	r := testBridgeRegistry(t)
	srv := newBridgeServer(r)

	req := httptest.NewRequest("GET", "/tltv/v1/peers", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)

	peers, _ := resp["peers"].([]interface{})
	if len(peers) != 0 {
		t.Errorf("expected empty peers, got %d", len(peers))
	}
}

func TestBridgePeers_WithConfigured(t *testing.T) {
	dir := t.TempDir()
	r := newBridgeRegistry(dir, "bridge.example.com:8000", []string{"bridge.example.com:8000"})
	r.UpdateChannels([]bridgeChannel{{ID: "ch1", Name: "Test", Stream: "http://example.com/stream.m3u8"}})

	srv := newBridgeServer(r)

	req := httptest.NewRequest("GET", "/tltv/v1/peers", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)

	peers, _ := resp["peers"].([]interface{})
	if len(peers) != 1 {
		t.Fatalf("expected 1 peer, got %d", len(peers))
	}

	peer := peers[0].(map[string]interface{})
	hints, _ := peer["hints"].([]interface{})
	if len(hints) != 1 || hints[0] != "bridge.example.com:8000" {
		t.Errorf("hints = %v", hints)
	}
}

func TestBridgeHealth(t *testing.T) {
	r := testBridgeRegistry(t)
	srv := newBridgeServer(r)

	req := httptest.NewRequest("GET", "/health", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)

	if resp["status"] != "ok" {
		t.Errorf("status = %v, want ok", resp["status"])
	}
	if resp["channels"] != float64(1) {
		t.Errorf("channels = %v, want 1", resp["channels"])
	}
}

func TestBridgeCORSHeaders(t *testing.T) {
	r := testBridgeRegistry(t)
	srv := newBridgeServer(r)

	req := httptest.NewRequest("GET", "/.well-known/tltv", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Error("missing CORS header")
	}
}

func TestBridgeMethodNotAllowed(t *testing.T) {
	r := testBridgeRegistry(t)
	srv := newBridgeServer(r)

	req := httptest.NewRequest("POST", "/.well-known/tltv", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != 400 {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}

func TestBridgeOnDemandMetadata(t *testing.T) {
	dir := t.TempDir()
	r := newBridgeRegistry(dir, "", nil)
	r.UpdateChannels([]bridgeChannel{{
		ID: "ch1", Name: "On Demand Channel", Stream: "http://example.com/stream.m3u8",
		OnDemand: true,
	}})

	srv := newBridgeServer(r)
	id := testBridgeChannelID(t, r)

	req := httptest.NewRequest("GET", "/tltv/v1/channels/"+id, nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	var doc map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &doc)

	if doc["on_demand"] != true {
		t.Errorf("on_demand = %v, want true", doc["on_demand"])
	}
}

func TestBridgeOriginsFromHostname(t *testing.T) {
	dir := t.TempDir()
	r := newBridgeRegistry(dir, "bridge.example.com:8000", nil)
	r.UpdateChannels([]bridgeChannel{{ID: "ch1", Name: "Test", Stream: "http://example.com/stream.m3u8"}})

	srv := newBridgeServer(r)
	id := testBridgeChannelID(t, r)

	req := httptest.NewRequest("GET", "/tltv/v1/channels/"+id, nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	var doc map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &doc)

	origins, ok := doc["origins"].([]interface{})
	if !ok || len(origins) != 1 || origins[0] != "bridge.example.com:8000" {
		t.Errorf("origins = %v", doc["origins"])
	}
}

// ---------- Private Channels ----------

func TestBridgePrivateChannel_HiddenFromNodeInfo(t *testing.T) {
	dir := t.TempDir()
	r := newBridgeRegistry(dir, "", nil)
	r.UpdateChannels([]bridgeChannel{
		{ID: "pub", Name: "Public", Stream: "http://example.com/pub.m3u8"},
		{ID: "priv", Name: "Private", Stream: "http://example.com/priv.m3u8", Access: "token", Token: "secret123"},
	})

	srv := newBridgeServer(r)

	req := httptest.NewRequest("GET", "/.well-known/tltv", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)

	channels := resp["channels"].([]interface{})
	if len(channels) != 1 {
		t.Fatalf("expected 1 public channel in node info, got %d", len(channels))
	}
	ch := channels[0].(map[string]interface{})
	if ch["name"] != "Public" {
		t.Errorf("visible channel name = %v", ch["name"])
	}
}

func TestBridgePrivateChannel_RequiresToken(t *testing.T) {
	dir := t.TempDir()
	r := newBridgeRegistry(dir, "", nil)
	r.UpdateChannels([]bridgeChannel{
		{ID: "priv", Name: "Private", Stream: "http://example.com/priv.m3u8", Access: "token", Token: "secret123"},
	})

	srv := newBridgeServer(r)
	var privID string
	for _, ch := range r.ListChannels() {
		privID = ch.ChannelID
	}

	// No token -> 403
	req := httptest.NewRequest("GET", "/tltv/v1/channels/"+privID, nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != 403 {
		t.Errorf("no token: status = %d, want 403", w.Code)
	}

	// Wrong token -> 403
	req = httptest.NewRequest("GET", "/tltv/v1/channels/"+privID+"?token=wrong", nil)
	w = httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != 403 {
		t.Errorf("wrong token: status = %d, want 403", w.Code)
	}

	// Correct token -> 200
	req = httptest.NewRequest("GET", "/tltv/v1/channels/"+privID+"?token=secret123", nil)
	w = httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Errorf("correct token: status = %d, want 200", w.Code)
	}

	// Check privacy headers
	if w.Header().Get("Referrer-Policy") != "no-referrer" {
		t.Error("missing Referrer-Policy: no-referrer")
	}
	if !strings.Contains(w.Header().Get("Cache-Control"), "private") {
		t.Error("missing Cache-Control: private")
	}
}

func TestBridgePrivateChannel_TokenOnSubPaths(t *testing.T) {
	dir := t.TempDir()

	// Create local stream files for the private channel
	streamDir := t.TempDir()
	os.WriteFile(filepath.Join(streamDir, "stream.m3u8"), []byte("#EXTM3U\n#EXTINF:2.0,\nseg.ts\n"), 0644)
	os.WriteFile(filepath.Join(streamDir, "seg.ts"), []byte("fake-ts"), 0644)

	r := newBridgeRegistry(dir, "", nil)
	r.UpdateChannels([]bridgeChannel{
		{ID: "priv", Name: "Private", Stream: filepath.Join(streamDir, "stream.m3u8"), Access: "token", Token: "secret"},
	})

	srv := newBridgeServer(r)
	var privID string
	for _, ch := range r.ListChannels() {
		privID = ch.ChannelID
	}

	// guide.json without token -> 403
	req := httptest.NewRequest("GET", "/tltv/v1/channels/"+privID+"/guide.json", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != 403 {
		t.Errorf("guide without token: status = %d, want 403", w.Code)
	}

	// stream.m3u8 without token -> 403
	req = httptest.NewRequest("GET", "/tltv/v1/channels/"+privID+"/stream.m3u8", nil)
	w = httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != 403 {
		t.Errorf("stream without token: status = %d, want 403", w.Code)
	}

	// guide.json with correct token -> 200
	req = httptest.NewRequest("GET", "/tltv/v1/channels/"+privID+"/guide.json?token=secret", nil)
	w = httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Errorf("guide with token: status = %d, want 200", w.Code)
	}
}

func TestBridgePeers_PrivateExcluded(t *testing.T) {
	dir := t.TempDir()
	r := newBridgeRegistry(dir, "bridge.example.com:8000", []string{"bridge.example.com:8000"})
	r.UpdateChannels([]bridgeChannel{
		{ID: "priv", Name: "Private", Stream: "http://example.com/priv.m3u8", Access: "token", Token: "secret"},
	})

	srv := newBridgeServer(r)

	req := httptest.NewRequest("GET", "/tltv/v1/peers", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)

	peers := resp["peers"].([]interface{})
	if len(peers) != 0 {
		t.Errorf("private channel should not appear in peers, got %d", len(peers))
	}
}

// ---------- Local File Stream ----------

func TestBridgeLocalFileStream(t *testing.T) {
	dir := t.TempDir()
	streamDir := t.TempDir()

	manifest := "#EXTM3U\n#EXT-X-TARGETDURATION:2\n#EXTINF:2.0,\nseg-000.ts\n"
	os.WriteFile(filepath.Join(streamDir, "stream.m3u8"), []byte(manifest), 0644)
	os.WriteFile(filepath.Join(streamDir, "seg-000.ts"), []byte("fake-ts-data"), 0644)

	r := newBridgeRegistry(dir, "", nil)
	r.UpdateChannels([]bridgeChannel{
		{ID: "local", Name: "Local Channel", Stream: filepath.Join(streamDir, "stream.m3u8")},
	})

	srv := newBridgeServer(r)
	id := testBridgeChannelID(t, r)

	// Fetch manifest
	req := httptest.NewRequest("GET", "/tltv/v1/channels/"+id+"/stream.m3u8", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("manifest: status = %d, want 200", w.Code)
	}
	if !strings.Contains(w.Body.String(), "#EXTM3U") {
		t.Error("manifest should contain #EXTM3U")
	}

	// Fetch segment
	req = httptest.NewRequest("GET", "/tltv/v1/channels/"+id+"/seg-000.ts", nil)
	w = httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("segment: status = %d, want 200", w.Code)
	}
	if w.Body.String() != "fake-ts-data" {
		t.Errorf("segment body = %q", w.Body.String())
	}
}

// ---------- Concurrent Access ----------

func TestBridgeConcurrentAccessDuringUpdate(t *testing.T) {
	dir := t.TempDir()
	r := newBridgeRegistry(dir, "", nil)
	r.UpdateChannels([]bridgeChannel{
		{ID: "ch1", Name: "Test", Stream: "http://example.com/stream.m3u8"},
	})
	srv := newBridgeServer(r)

	var wg sync.WaitGroup

	// Concurrent updates
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r.UpdateChannels([]bridgeChannel{
				{ID: "ch1", Name: "Test", Stream: "http://example.com/stream.m3u8"},
			})
		}()
	}

	// Concurrent reads
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// Node info
			req := httptest.NewRequest("GET", "/.well-known/tltv", nil)
			w := httptest.NewRecorder()
			srv.ServeHTTP(w, req)
			if w.Code != 200 {
				t.Errorf("node info status = %d during concurrent access", w.Code)
			}
		}()
	}

	// Concurrent metadata reads
	id := testBridgeChannelID(t, r)
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req := httptest.NewRequest("GET", "/tltv/v1/channels/"+id, nil)
			w := httptest.NewRecorder()
			srv.ServeHTTP(w, req)
			// Channel might be temporarily missing during update, allow 200 or 404
			if w.Code != 200 && w.Code != 404 {
				t.Errorf("metadata status = %d during concurrent access", w.Code)
			}
		}()
	}

	wg.Wait()
}

// ---------- XML Escape ----------

func TestBridgeXMLEscape(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"hello", "hello"},
		{"a&b", "a&amp;b"},
		{"<tag>", "&lt;tag&gt;"},
		{`he said "hi"`, `he said &quot;hi&quot;`},
	}
	for _, tt := range tests {
		got := bridgeXMLEscape(tt.in)
		if got != tt.want {
			t.Errorf("bridgeXMLEscape(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// ---------- Sanitize Filename ----------

func TestBridgeSanitizeFilename(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"hello", "hello"},
		{"hello world", "hello_world"},
		{"ch@1!2#3", "ch_1_2_3"},
		{"test-channel_1", "test-channel_1"},
	}
	for _, tt := range tests {
		got := bridgeSanitizeFilename(tt.in)
		if got != tt.want {
			t.Errorf("bridgeSanitizeFilename(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// ---------- Stream Content Type ----------

func TestBridgeStreamContentType(t *testing.T) {
	tests := []struct {
		name, want string
	}{
		{"stream.m3u8", "application/vnd.apple.mpegurl"},
		{"seg.ts", "video/mp2t"},
		{"seg.mp4", "video/mp4"},
		{"seg.m4s", "video/mp4"},
		{"audio.aac", "audio/aac"},
		{"sub.vtt", "text/vtt"},
		{"unknown.bin", "application/octet-stream"},
	}
	for _, tt := range tests {
		got := bridgeStreamContentType(tt.name)
		if got != tt.want {
			t.Errorf("bridgeStreamContentType(%q) = %q, want %q", tt.name, got, tt.want)
		}
	}
}

// ---------- OPTIONS / CORS Preflight ----------

func TestBridgeOPTIONS(t *testing.T) {
	r := testBridgeRegistry(t)
	srv := newBridgeServer(r)

	req := httptest.NewRequest("OPTIONS", "/.well-known/tltv", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != 204 {
		t.Errorf("OPTIONS status = %d, want 204", w.Code)
	}
	if w.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Error("missing CORS header on OPTIONS")
	}
}

// ---------- Path Validation ----------

func TestBridgeValidateSubPath(t *testing.T) {
	valid := []string{
		"stream.m3u8", "seg-000.ts", "video/stream.m3u8",
		"video/seg-000.ts", "init.mp4", "foo..bar.ts",
	}
	for _, p := range valid {
		if !bridgeValidateSubPath(p) {
			t.Errorf("should accept %q", p)
		}
	}

	invalid := []string{
		"..", "../etc/passwd", "../../etc/shadow",
		"subdir/../../etc/passwd", "foo/../bar", "",
	}
	for _, p := range invalid {
		if bridgeValidateSubPath(p) {
			t.Errorf("should reject %q", p)
		}
	}
}

func TestBridgeLocalFileStream_PathTraversal(t *testing.T) {
	dir := t.TempDir()
	streamDir := t.TempDir()

	os.WriteFile(filepath.Join(streamDir, "stream.m3u8"), []byte("#EXTM3U\n"), 0644)

	r := newBridgeRegistry(dir, "", nil)
	r.UpdateChannels([]bridgeChannel{
		{ID: "local", Name: "Local", Stream: filepath.Join(streamDir, "stream.m3u8")},
	})

	ch := r.ListChannels()[0]

	// Test path traversal via bridgeServeStream directly (bypasses mux path cleaning)
	traversals := []string{
		"../../etc/passwd",
		"../../../etc/shadow",
		"subdir/../../etc/passwd",
	}
	for _, p := range traversals {
		req := httptest.NewRequest("GET", "/test", nil)
		w := httptest.NewRecorder()
		bridgeServeStream(w, req, ch, p, "")

		if w.Code != 400 {
			t.Errorf("path traversal %q: got status %d, want 400", p, w.Code)
		}
	}
}

// ---------- Upstream Stream Proxy ----------

func TestBridgeUpstreamStream(t *testing.T) {
	manifest := "#EXTM3U\n#EXT-X-TARGETDURATION:2\n#EXTINF:2.0,\nseg-000.ts\n"
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/stream.m3u8"):
			w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
			w.Write([]byte(manifest))
		case strings.HasSuffix(r.URL.Path, "/seg-000.ts"):
			w.Header().Set("Content-Type", "video/mp2t")
			w.Write([]byte("fake-ts-data"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()

	dir := t.TempDir()
	r := newBridgeRegistry(dir, "", nil)
	r.UpdateChannels([]bridgeChannel{
		{ID: "ch1", Name: "Upstream", Stream: upstream.URL + "/live/stream.m3u8"},
	})

	srv := newBridgeServer(r)
	id := testBridgeChannelID(t, r)

	// Fetch manifest through bridge
	req := httptest.NewRequest("GET", "/tltv/v1/channels/"+id+"/stream.m3u8", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("manifest status = %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "#EXTM3U") {
		t.Error("manifest should contain #EXTM3U")
	}
	if !strings.Contains(w.Body.String(), "seg-000.ts") {
		t.Error("manifest should reference segment")
	}

	// Fetch segment through bridge
	req = httptest.NewRequest("GET", "/tltv/v1/channels/"+id+"/seg-000.ts", nil)
	w = httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("segment status = %d", w.Code)
	}
	if w.Body.String() != "fake-ts-data" {
		t.Errorf("segment body = %q", w.Body.String())
	}
}
