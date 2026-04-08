package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// ---------- loadDaemonConfig ----------

func TestLoadDaemonConfig_Valid(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	os.WriteFile(path, []byte(`{"name": "Test", "cache": true, "width": 1920}`), 0644)

	cfg, err := loadDaemonConfig(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if s, ok := configGetString(cfg, "name"); !ok || s != "Test" {
		t.Errorf("name = %q, ok = %v", s, ok)
	}
	if b, ok := configGetBool(cfg, "cache"); !ok || !b {
		t.Errorf("cache = %v, ok = %v", b, ok)
	}
	if n, ok := configGetInt(cfg, "width"); !ok || n != 1920 {
		t.Errorf("width = %d, ok = %v", n, ok)
	}
}

func TestLoadDaemonConfig_InvalidJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.json")
	os.WriteFile(path, []byte(`{not json`), 0644)

	_, err := loadDaemonConfig(path)
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestLoadDaemonConfig_MissingFile(t *testing.T) {
	_, err := loadDaemonConfig("/nonexistent/config.json")
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestLoadDaemonConfig_Empty(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "empty.json")
	os.WriteFile(path, []byte(`{}`), 0644)

	cfg, err := loadDaemonConfig(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cfg) != 0 {
		t.Errorf("expected empty config, got %d keys", len(cfg))
	}
}

func TestLoadDaemonConfig_NumberPreservation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	os.WriteFile(path, []byte(`{"seq": 1234567890}`), 0644)

	cfg, err := loadDaemonConfig(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Should be json.Number, not float64
	n, ok := cfg["seq"].(json.Number)
	if !ok {
		t.Fatalf("seq is %T, expected json.Number", cfg["seq"])
	}
	if n.String() != "1234567890" {
		t.Errorf("seq = %s", n)
	}
}

// ---------- configGetStringSlice ----------

func TestConfigGetStringSlice_Array(t *testing.T) {
	cfg := map[string]interface{}{
		"channels": []interface{}{"tltv://a@x.tv", "tltv://b@y.tv"},
	}
	result, ok := configGetStringSlice(cfg, "channels")
	if !ok || len(result) != 2 {
		t.Fatalf("expected 2 strings, got %v (ok=%v)", result, ok)
	}
	if result[0] != "tltv://a@x.tv" || result[1] != "tltv://b@y.tv" {
		t.Errorf("result = %v", result)
	}
}

func TestConfigGetStringSlice_SingleString(t *testing.T) {
	cfg := map[string]interface{}{
		"node": "origin.tv:443",
	}
	result, ok := configGetStringSlice(cfg, "node")
	if !ok || len(result) != 1 || result[0] != "origin.tv:443" {
		t.Errorf("expected [origin.tv:443], got %v", result)
	}
}

func TestConfigGetStringSlice_Missing(t *testing.T) {
	cfg := map[string]interface{}{}
	_, ok := configGetStringSlice(cfg, "channels")
	if ok {
		t.Error("expected ok=false for missing key")
	}
}

// ---------- parseGuideConfig ----------

func TestParseGuideConfig_InlineEntries(t *testing.T) {
	v := map[string]interface{}{
		"entries": []interface{}{
			map[string]interface{}{
				"start": "2026-04-08T00:00:00Z",
				"end":   "2026-04-09T00:00:00Z",
				"title": "Test Show",
			},
			map[string]interface{}{
				"start":       "2026-04-09T00:00:00Z",
				"end":         "2026-04-10T00:00:00Z",
				"title":       "Night Block",
				"description": "Late night programming",
			},
		},
	}
	entries, filePath, err := parseGuideConfig(v)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if filePath != "" {
		t.Errorf("expected empty filePath, got %q", filePath)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}
	if entries[0].Title != "Test Show" {
		t.Errorf("entries[0].Title = %q", entries[0].Title)
	}
	if entries[1].Description != "Late night programming" {
		t.Errorf("entries[1].Description = %q", entries[1].Description)
	}
}

func TestParseGuideConfig_FileRef(t *testing.T) {
	entries, filePath, err := parseGuideConfig("guide.json")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if filePath != "guide.json" {
		t.Errorf("expected filePath=guide.json, got %q", filePath)
	}
	if entries != nil {
		t.Errorf("expected nil entries for file ref, got %v", entries)
	}
}

func TestParseGuideConfig_Nil(t *testing.T) {
	entries, filePath, err := parseGuideConfig(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if entries != nil || filePath != "" {
		t.Errorf("expected nil/empty, got entries=%v filePath=%q", entries, filePath)
	}
}

func TestParseGuideConfig_InvalidType(t *testing.T) {
	_, _, err := parseGuideConfig(42)
	if err == nil {
		t.Fatal("expected error for invalid type")
	}
}

func TestParseGuideConfig_MissingEntries(t *testing.T) {
	_, _, err := parseGuideConfig(map[string]interface{}{"other": "field"})
	if err == nil {
		t.Fatal("expected error for object without entries")
	}
}

func TestParseGuideConfig_SkipsIncomplete(t *testing.T) {
	v := map[string]interface{}{
		"entries": []interface{}{
			map[string]interface{}{"start": "2026-01-01T00:00:00Z", "end": "2026-01-02T00:00:00Z", "title": "Good"},
			map[string]interface{}{"start": "2026-01-01T00:00:00Z"}, // missing end and title
		},
	}
	entries, _, err := parseGuideConfig(v)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(entries) != 1 {
		t.Errorf("expected 1 entry (skipping incomplete), got %d", len(entries))
	}
}

// ---------- configWatcher ----------

func TestConfigWatcher_Changed(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	os.WriteFile(path, []byte(`{}`), 0644)

	w := newConfigWatcher(path)

	// No change yet
	if w.Changed() {
		t.Error("expected no change on first check")
	}

	// Modify the file (advance mtime)
	time.Sleep(50 * time.Millisecond)
	os.WriteFile(path, []byte(`{"name": "updated"}`), 0644)

	if !w.Changed() {
		t.Error("expected change after modification")
	}

	// No change on second check (same mtime)
	if w.Changed() {
		t.Error("expected no change on second check")
	}
}

func TestConfigWatcher_MissingFile(t *testing.T) {
	w := newConfigWatcher("/nonexistent/config.json")
	if w.Changed() {
		t.Error("expected false for missing file")
	}
}

func TestConfigWatcher_FileDeleted(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	os.WriteFile(path, []byte(`{}`), 0644)

	w := newConfigWatcher(path)
	os.Remove(path)

	// Should return false (fail-safe), not panic
	if w.Changed() {
		t.Error("expected false when file deleted")
	}
}

// ---------- dumpDaemonConfig ----------

func TestDumpDaemonConfig_Basic(t *testing.T) {
	cfg := map[string]interface{}{
		"name":    "My Channel",
		"cache":   true,
		"width":   1920,
		"listen":  "",    // should be omitted (empty)
		"viewer":  false, // should be omitted (false)
		"qp":      0,     // should be omitted (zero)
		"tls_key": "",    // should be omitted (empty)
	}

	var buf bytes.Buffer
	if err := dumpDaemonConfig(cfg, &buf); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var result map[string]interface{}
	if err := json.Unmarshal(buf.Bytes(), &result); err != nil {
		t.Fatalf("invalid JSON output: %v", err)
	}

	// Should include non-zero values
	if result["name"] != "My Channel" {
		t.Errorf("name = %v", result["name"])
	}
	if result["cache"] != true {
		t.Errorf("cache = %v", result["cache"])
	}
	// Width comes back as float64 from json.Unmarshal
	if w, ok := result["width"].(float64); !ok || w != 1920 {
		t.Errorf("width = %v", result["width"])
	}

	// Should omit zero/empty values
	for _, key := range []string{"listen", "viewer", "qp", "tls_key"} {
		if _, ok := result[key]; ok {
			t.Errorf("expected %q to be omitted, but it was present", key)
		}
	}
}

func TestDumpDaemonConfig_StringSlice(t *testing.T) {
	cfg := map[string]interface{}{
		"channels": []string{"tltv://a@x.tv", "tltv://b@y.tv"},
	}

	var buf bytes.Buffer
	dumpDaemonConfig(cfg, &buf)

	var result map[string]interface{}
	json.Unmarshal(buf.Bytes(), &result)

	arr, ok := result["channels"].([]interface{})
	if !ok || len(arr) != 2 {
		t.Errorf("channels = %v", result["channels"])
	}
}

func TestDumpDaemonConfig_GuideEntries(t *testing.T) {
	cfg := map[string]interface{}{
		"guide": []guideEntry{
			{Start: "2026-01-01T00:00:00Z", End: "2026-01-02T00:00:00Z", Title: "Show"},
		},
	}

	var buf bytes.Buffer
	dumpDaemonConfig(cfg, &buf)

	var result map[string]interface{}
	json.Unmarshal(buf.Bytes(), &result)

	guide, ok := result["guide"].(map[string]interface{})
	if !ok {
		t.Fatalf("guide = %T %v", result["guide"], result["guide"])
	}
	entries, ok := guide["entries"].([]interface{})
	if !ok || len(entries) != 1 {
		t.Errorf("guide.entries = %v", guide["entries"])
	}
}

func TestDumpDaemonConfig_EmptySlice(t *testing.T) {
	cfg := map[string]interface{}{
		"channels": []string{},
	}

	var buf bytes.Buffer
	dumpDaemonConfig(cfg, &buf)

	var result map[string]interface{}
	json.Unmarshal(buf.Bytes(), &result)

	if _, ok := result["channels"]; ok {
		t.Error("expected empty channels to be omitted")
	}
}

// ---------- applyConfigToFlags ----------

func TestApplyConfigToFlags_Basic(t *testing.T) {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	name := fs.String("name", "", "channel name")
	width := fs.Int("width", 640, "width")
	fs.Parse([]string{}) // no explicit flags

	cfg := map[string]interface{}{
		"name":  "From Config",
		"width": json.Number("1920"),
	}

	applied := applyConfigToFlags(fs, cfg)
	if len(applied) != 2 {
		t.Errorf("expected 2 applied, got %d: %v", len(applied), applied)
	}
	if *name != "From Config" {
		t.Errorf("name = %q", *name)
	}
	if *width != 1920 {
		t.Errorf("width = %d", *width)
	}
}

func TestApplyConfigToFlags_ExplicitFlagOverride(t *testing.T) {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	name := fs.String("name", "", "channel name")
	fs.Parse([]string{"--name", "From CLI"})

	cfg := map[string]interface{}{
		"name": "From Config",
	}

	applyConfigToFlags(fs, cfg)
	if *name != "From CLI" {
		t.Errorf("expected CLI value, got %q", *name)
	}
}

func TestApplyConfigToFlags_UnderscoreMapping(t *testing.T) {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	maxEntries := fs.Int("cache-max-entries", 100, "max entries")
	fs.Parse([]string{})

	cfg := map[string]interface{}{
		"cache_max_entries": json.Number("500"),
	}

	applyConfigToFlags(fs, cfg)
	if *maxEntries != 500 {
		t.Errorf("cache-max-entries = %d, expected 500", *maxEntries)
	}
}

func TestApplyConfigToFlags_BoolValue(t *testing.T) {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	cache := fs.Bool("cache", false, "enable cache")
	fs.Parse([]string{})

	cfg := map[string]interface{}{
		"cache": true,
	}

	applyConfigToFlags(fs, cfg)
	if !*cache {
		t.Error("expected cache=true from config")
	}
}

func TestApplyConfigToFlags_SkipsUnknown(t *testing.T) {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	fs.String("name", "", "name")
	fs.Parse([]string{})

	cfg := map[string]interface{}{
		"unknown_field": "value",
	}

	applied := applyConfigToFlags(fs, cfg)
	if len(applied) != 0 {
		t.Errorf("expected 0 applied for unknown field, got %v", applied)
	}
}

func TestApplyConfigToFlags_SkipsArrays(t *testing.T) {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	fs.String("name", "", "name")
	fs.Parse([]string{})

	cfg := map[string]interface{}{
		"channels": []interface{}{"a", "b"},
	}

	applied := applyConfigToFlags(fs, cfg)
	if len(applied) != 0 {
		t.Errorf("expected 0 applied for array field, got %v", applied)
	}
}

// ---------- gossipNodesFromPeers ----------

func TestGossipNodesFromPeers_Basic(t *testing.T) {
	targets := []peerTarget{
		{ChannelID: "TVa", Hints: []string{"a.tv:443"}},
		{ChannelID: "TVb", Hints: []string{"b.tv:443"}},
		{ChannelID: "TVc", Hints: []string{"a.tv:443"}}, // duplicate hint
	}
	nodes := gossipNodesFromPeers(targets)
	if len(nodes) != 2 {
		t.Errorf("expected 2 unique nodes, got %d: %v", len(nodes), nodes)
	}
}

// ---------- configReloadLoop ----------

func TestConfigReloadLoop_CallsReloadFn(t *testing.T) {
	// Create a config file
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	os.WriteFile(cfgPath, []byte(`{"name": "original"}`), 0644)

	watcher := newConfigWatcher(cfgPath)

	// Touch the file to trigger a change
	time.Sleep(50 * time.Millisecond)
	os.WriteFile(cfgPath, []byte(`{"name": "updated"}`), 0644)

	called := make(chan map[string]interface{}, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go configReloadLoop(ctx, watcher, func(cfg map[string]interface{}) {
		called <- cfg
	})

	// The loop checks every 30s, but we can verify by waiting briefly
	// and then cancelling. Instead, call Changed + load directly to test
	// the watcher/reload integration.
	select {
	case cfg := <-called:
		if name, ok := cfg["name"].(string); !ok || name != "updated" {
			t.Errorf("expected name=updated, got %v", cfg["name"])
		}
	case <-time.After(35 * time.Second):
		t.Fatal("reloadFn not called within 35s")
	}
}

func TestConfigReloadLoop_CancelsCleanly(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	os.WriteFile(cfgPath, []byte(`{}`), 0644)

	watcher := newConfigWatcher(cfgPath)
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		configReloadLoop(ctx, watcher, func(cfg map[string]interface{}) {})
		close(done)
	}()

	cancel()
	select {
	case <-done:
		// ok — exited cleanly
	case <-time.After(2 * time.Second):
		t.Fatal("configReloadLoop did not exit after cancel")
	}
}

// ---------- serverApplyReloadedConfig ----------

func TestServerApplyReloadedConfig_NameChange(t *testing.T) {
	var liveConfig atomic.Pointer[serverReloadableConfig]
	liveConfig.Store(&serverReloadableConfig{
		channelName:  "Original",
		guideEntries: nil,
	})

	cfg := map[string]interface{}{
		"name": "Updated",
	}
	serverApplyReloadedConfig(cfg, &liveConfig)

	result := liveConfig.Load()
	if result.channelName != "Updated" {
		t.Errorf("channelName = %q, want Updated", result.channelName)
	}
}

func TestServerApplyReloadedConfig_GuideEntries(t *testing.T) {
	var liveConfig atomic.Pointer[serverReloadableConfig]
	liveConfig.Store(&serverReloadableConfig{
		channelName:  "Test",
		guideEntries: nil,
	})

	cfg := map[string]interface{}{
		"guide": map[string]interface{}{
			"entries": []interface{}{
				map[string]interface{}{
					"start": "2026-04-08T00:00:00Z",
					"end":   "2026-04-09T00:00:00Z",
					"title": "Day Block",
				},
			},
		},
	}
	serverApplyReloadedConfig(cfg, &liveConfig)

	result := liveConfig.Load()
	if len(result.guideEntries) != 1 {
		t.Fatalf("expected 1 guide entry, got %d", len(result.guideEntries))
	}
	if result.guideEntries[0].Title != "Day Block" {
		t.Errorf("title = %q, want Day Block", result.guideEntries[0].Title)
	}
}

func TestServerApplyReloadedConfig_NoChange(t *testing.T) {
	original := &serverReloadableConfig{
		channelName:  "Test",
		guideEntries: nil,
	}
	var liveConfig atomic.Pointer[serverReloadableConfig]
	liveConfig.Store(original)

	// Empty config — nothing to change
	serverApplyReloadedConfig(map[string]interface{}{}, &liveConfig)

	// Should still be the same pointer (no Store called)
	if liveConfig.Load() != original {
		t.Error("should not have stored new config when nothing changed")
	}
}

// ---------- bridgeApplyReloadedConfig ----------

func TestBridgeApplyReloadedConfig_StreamChange(t *testing.T) {
	var liveConfig atomic.Pointer[bridgeReloadableConfig]
	liveConfig.Store(&bridgeReloadableConfig{
		stream: "http://old.tv/live.m3u8",
		name:   "Test",
		guide:  "",
	})

	cfg := map[string]interface{}{
		"stream": "http://new.tv/live.m3u8",
	}

	registry := newBridgeRegistry("", "")
	bridgeApplyReloadedConfig(cfg, &liveConfig, registry)

	result := liveConfig.Load()
	if result.stream != "http://new.tv/live.m3u8" {
		t.Errorf("stream = %q, want http://new.tv/live.m3u8", result.stream)
	}
	if result.name != "Test" {
		t.Errorf("name should be unchanged, got %q", result.name)
	}
}

func TestBridgeApplyReloadedConfig_NameChange(t *testing.T) {
	var liveConfig atomic.Pointer[bridgeReloadableConfig]
	liveConfig.Store(&bridgeReloadableConfig{
		stream: "http://src.tv/live.m3u8",
		name:   "Old Name",
		guide:  "",
	})

	cfg := map[string]interface{}{
		"name": "New Name",
	}

	registry := newBridgeRegistry("", "")
	bridgeApplyReloadedConfig(cfg, &liveConfig, registry)

	result := liveConfig.Load()
	if result.name != "New Name" {
		t.Errorf("name = %q, want New Name", result.name)
	}
}

func TestBridgeApplyReloadedConfig_GuideFilePath(t *testing.T) {
	var liveConfig atomic.Pointer[bridgeReloadableConfig]
	liveConfig.Store(&bridgeReloadableConfig{
		stream: "http://src.tv/live.m3u8",
		name:   "Test",
		guide:  "old-guide.xml",
	})

	cfg := map[string]interface{}{
		"guide": "new-guide.xml",
	}

	registry := newBridgeRegistry("", "")
	bridgeApplyReloadedConfig(cfg, &liveConfig, registry)

	result := liveConfig.Load()
	if result.guide != "new-guide.xml" {
		t.Errorf("guide = %q, want new-guide.xml", result.guide)
	}
}

func TestBridgeApplyReloadedConfig_NoChange(t *testing.T) {
	original := &bridgeReloadableConfig{
		stream: "http://src.tv/live.m3u8",
		name:   "Test",
		guide:  "",
	}
	var liveConfig atomic.Pointer[bridgeReloadableConfig]
	liveConfig.Store(original)

	registry := newBridgeRegistry("", "")
	bridgeApplyReloadedConfig(map[string]interface{}{}, &liveConfig, registry)

	if liveConfig.Load() != original {
		t.Error("should not have stored new config when nothing changed")
	}
}

func TestGossipNodesFromPeers_Empty(t *testing.T) {
	nodes := gossipNodesFromPeers(nil)
	if len(nodes) != 0 {
		t.Errorf("expected 0 nodes, got %v", nodes)
	}
}

func TestGossipNodesFromPeers_MultipleHints(t *testing.T) {
	targets := []peerTarget{
		{ChannelID: "TVa", Hints: []string{"a.tv:443", "b.tv:443"}},
	}
	nodes := gossipNodesFromPeers(targets)
	if len(nodes) != 2 {
		t.Errorf("expected 2 nodes from multi-hint target, got %d", len(nodes))
	}
}
