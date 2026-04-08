package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"flag"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// ---------- parsePeerTargets ----------

func TestParsePeerTargets_Empty(t *testing.T) {
	targets, err := parsePeerTargets("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if targets != nil {
		t.Errorf("expected nil, got %v", targets)
	}
}

func TestParsePeerTargets_ValidURI(t *testing.T) {
	targets, err := parsePeerTargets("tltv://TVabc123@host.tv:443")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(targets) != 1 {
		t.Fatalf("expected 1 target, got %d", len(targets))
	}
	if targets[0].ChannelID != "TVabc123" {
		t.Errorf("channelID = %q", targets[0].ChannelID)
	}
	if len(targets[0].Hints) != 1 || targets[0].Hints[0] != "host.tv:443" {
		t.Errorf("hints = %v", targets[0].Hints)
	}
}

func TestParsePeerTargets_MultipleURIs(t *testing.T) {
	targets, err := parsePeerTargets("tltv://TVa@a.tv:443,tltv://TVb@b.tv:443")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(targets) != 2 {
		t.Fatalf("expected 2 targets, got %d", len(targets))
	}
	if targets[0].ChannelID != "TVa" {
		t.Errorf("first channelID = %q", targets[0].ChannelID)
	}
	if targets[1].ChannelID != "TVb" {
		t.Errorf("second channelID = %q", targets[1].ChannelID)
	}
}

func TestParsePeerTargets_BareHostRejected(t *testing.T) {
	_, err := parsePeerTargets("relay.example.com:443")
	if err == nil {
		t.Fatal("expected error for bare host:port")
	}
	// Error message should guide the user
	if got := err.Error(); !contains(got, "tltv://") {
		t.Errorf("error should mention tltv://, got: %s", got)
	}
}

func TestParsePeerTargets_NoHints(t *testing.T) {
	_, err := parsePeerTargets("tltv://TVabc123")
	if err == nil {
		t.Fatal("expected error for URI without hints")
	}
}

func TestParsePeerTargets_BadScheme(t *testing.T) {
	_, err := parsePeerTargets("http://example.com")
	if err == nil {
		t.Fatal("expected error for non-tltv:// scheme")
	}
}

func TestParsePeerTargets_SpacesAndCommas(t *testing.T) {
	targets, err := parsePeerTargets(" tltv://TVa@a.tv:443 , tltv://TVb@b.tv:443 ,")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(targets) != 2 {
		t.Fatalf("expected 2 targets, got %d", len(targets))
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(s) > 0 && containsStr(s, sub))
}

func containsStr(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// ---------- peerRegistry ----------

func TestPeerRegistry_UpdateAndList(t *testing.T) {
	r := newPeerRegistry()
	r.Update("TVa", "Alice", []string{"alice.tv:443"})
	r.Update("TVb", "Bob", []string{"bob.tv:443"})

	peers := r.ListPeers()
	if len(peers) != 2 {
		t.Fatalf("expected 2 peers, got %d", len(peers))
	}

	// Verify entries exist (order is map-dependent)
	found := map[string]bool{}
	for _, p := range peers {
		found[p.ChannelID] = true
	}
	if !found["TVa"] || !found["TVb"] {
		t.Errorf("missing peer entries: %v", found)
	}
}

func TestPeerRegistry_Remove(t *testing.T) {
	r := newPeerRegistry()
	r.Update("TVa", "Alice", []string{"alice.tv:443"})
	r.Update("TVb", "Bob", []string{"bob.tv:443"})

	r.Remove("TVa")
	peers := r.ListPeers()
	if len(peers) != 1 {
		t.Fatalf("expected 1 peer after remove, got %d", len(peers))
	}
	if peers[0].ChannelID != "TVb" {
		t.Errorf("remaining peer = %q, want TVb", peers[0].ChannelID)
	}
}

func TestPeerRegistry_UpdateOverwrite(t *testing.T) {
	r := newPeerRegistry()
	r.Update("TVa", "Alice", []string{"alice.tv:443"})
	r.Update("TVa", "Alice Updated", []string{"alice2.tv:443"})

	peers := r.ListPeers()
	if len(peers) != 1 {
		t.Fatalf("expected 1 peer after overwrite, got %d", len(peers))
	}
	if peers[0].Name != "Alice Updated" {
		t.Errorf("name = %q, want Alice Updated", peers[0].Name)
	}
}

func TestPeerRegistry_ThreadSafety(t *testing.T) {
	r := newPeerRegistry()
	done := make(chan bool, 10)
	for i := 0; i < 5; i++ {
		go func(n int) {
			for j := 0; j < 100; j++ {
				r.Update("TVa", "Alice", []string{"alice.tv:443"})
				r.ListPeers()
			}
			done <- true
		}(i)
	}
	for i := 0; i < 5; i++ {
		<-done
	}
}

// ---------- addPeersFlag ----------

func TestAddPeersFlag(t *testing.T) {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	p := addPeersFlag(fs)

	err := fs.Parse([]string{"--peers", "tltv://TVa@host:443"})
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	if *p != "tltv://TVa@host:443" {
		t.Errorf("peers = %q", *p)
	}
}

func TestAddPeersFlag_ShortAlias(t *testing.T) {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	p := addPeersFlag(fs)

	err := fs.Parse([]string{"-P", "tltv://TVa@host:443"})
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	if *p != "tltv://TVa@host:443" {
		t.Errorf("peers = %q", *p)
	}
}

// ---------- buildPeersResponse ----------

func TestBuildPeersResponse_Empty(t *testing.T) {
	result := buildPeersResponse(nil, nil)
	if len(result) != 0 {
		t.Errorf("expected empty, got %d", len(result))
	}
}

func TestBuildPeersResponse_OwnOnly(t *testing.T) {
	own := []peerEntry{
		{ChannelID: "TVa", Name: "A", Hints: []string{"a.tv:443"}, LastSeen: time.Now()},
	}
	result := buildPeersResponse(own, nil)
	if len(result) != 1 {
		t.Fatalf("expected 1, got %d", len(result))
	}
	if result[0]["id"] != "TVa" {
		t.Errorf("id = %v", result[0]["id"])
	}
}

func TestBuildPeersResponse_Dedup(t *testing.T) {
	own := []peerEntry{
		{ChannelID: "TVa", Name: "A Own", Hints: []string{"own.tv:443"}, LastSeen: time.Now()},
	}
	ext := []peerEntry{
		{ChannelID: "TVa", Name: "A External", Hints: []string{"ext.tv:443"}, LastSeen: time.Now()},
		{ChannelID: "TVb", Name: "B", Hints: []string{"b.tv:443"}, LastSeen: time.Now()},
	}
	result := buildPeersResponse(own, ext)
	if len(result) != 2 {
		t.Fatalf("expected 2 (deduped), got %d", len(result))
	}
	// TVa should be the own version (added first)
	if result[0]["name"] != "A Own" {
		t.Errorf("first entry name = %v, want A Own", result[0]["name"])
	}
}

// ---------- Server peers endpoint ----------

func TestServerPeersEndpoint_Empty(t *testing.T) {
	mux := http.NewServeMux()
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	channelID := makeChannelID(pub)
	_ = priv
	seg := newHLSSegmenter(5, 2)
	serverHTTP(mux, seg, channelID, "Test", nil, nil, nil, nil)

	req := httptest.NewRequest("GET", "/tltv/v1/peers", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}

	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)
	peers := resp["peers"].([]interface{})
	if len(peers) != 0 {
		t.Errorf("expected empty peers, got %d", len(peers))
	}
}

func TestServerPeersEndpoint_WithPeerReg(t *testing.T) {
	mux := http.NewServeMux()
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	channelID := makeChannelID(pub)
	_ = priv
	seg := newHLSSegmenter(5, 2)

	peerReg := newPeerRegistry()
	peerReg.Update("TVfriend", "Friend Channel", []string{"friend.tv:443"})

	serverHTTP(mux, seg, channelID, "Test", nil, nil, nil, peerReg)

	req := httptest.NewRequest("GET", "/tltv/v1/peers", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)
	peers := resp["peers"].([]interface{})
	if len(peers) != 1 {
		t.Fatalf("expected 1 peer, got %d", len(peers))
	}
	peer := peers[0].(map[string]interface{})
	if peer["id"] != "TVfriend" {
		t.Errorf("peer id = %v", peer["id"])
	}
	if peer["name"] != "Friend Channel" {
		t.Errorf("peer name = %v", peer["name"])
	}
}

// ---------- Bridge peers endpoint ----------

func TestBridgePeersEndpoint_WithPeerReg(t *testing.T) {
	dir := t.TempDir()
	r := newBridgeRegistry(dir, "bridge.example.com:8000")
	r.UpdateChannels([]bridgeChannel{{ID: "ch1", Name: "Own Channel", Stream: "http://example.com/stream.m3u8"}})

	peerReg := newPeerRegistry()
	peerReg.Update("TVfriend", "Friend Channel", []string{"friend.tv:443"})

	srv := newBridgeServer(r, nil, peerReg)

	req := httptest.NewRequest("GET", "/tltv/v1/peers", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)
	peers := resp["peers"].([]interface{})
	if len(peers) != 2 {
		t.Fatalf("expected 2 peers (own + external), got %d", len(peers))
	}
}

func TestBridgePeersEndpoint_NoHostname(t *testing.T) {
	dir := t.TempDir()
	r := newBridgeRegistry(dir, "") // no hostname
	r.UpdateChannels([]bridgeChannel{{ID: "ch1", Name: "Test", Stream: "http://example.com/stream.m3u8"}})

	srv := newBridgeServer(r, nil, nil)

	req := httptest.NewRequest("GET", "/tltv/v1/peers", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)
	peers := resp["peers"].([]interface{})
	// No hostname = no own channel entries in peers
	if len(peers) != 0 {
		t.Errorf("expected 0 peers (no hostname), got %d", len(peers))
	}
}

// ---------- Relay peers with --gossip ----------

func TestRelayPeers_GossipDisabled(t *testing.T) {
	r := newRelayRegistry("relay.example.com:443", false, 100, 7)
	r.UpdateChannel("TVown", []byte(`{}`), map[string]interface{}{"name": "Own"}, nil)
	r.MergePeers([]relayPeerInfo{
		{ChannelID: "TVgossip", Name: "Gossip", Hints: []string{"gossip.tv:443"}, LastSeen: time.Now()},
	})

	peers := r.ListPeers()
	// Should only have own channel, not gossip
	if len(peers) != 1 {
		t.Fatalf("expected 1 peer (gossip disabled), got %d", len(peers))
	}
	if peers[0].ChannelID != "TVown" {
		t.Errorf("peer = %q, want TVown", peers[0].ChannelID)
	}
}

func TestRelayPeers_GossipEnabled(t *testing.T) {
	r := newRelayRegistry("relay.example.com:443", true, 100, 7)
	r.UpdateChannel("TVown", []byte(`{}`), map[string]interface{}{"name": "Own"}, nil)
	r.MergePeers([]relayPeerInfo{
		{ChannelID: "TVgossip", Name: "Gossip", Hints: []string{"gossip.tv:443"}, LastSeen: time.Now()},
	})

	peers := r.ListPeers()
	// Should have both own + gossip
	if len(peers) != 2 {
		t.Fatalf("expected 2 peers (gossip enabled), got %d", len(peers))
	}
}

func TestRelayPeersEndpoint_WithExternalPeers(t *testing.T) {
	r := newRelayRegistry("relay.example.com:443", false, 100, 7)
	r.UpdateChannel("TVown", []byte(`{}`), map[string]interface{}{"name": "Own"}, nil)

	peerReg := newPeerRegistry()
	peerReg.Update("TVfriend", "Friend", []string{"friend.tv:443"})

	srv := newRelayServer(r, newClient(false), nil, peerReg)

	req := httptest.NewRequest("GET", "/tltv/v1/peers", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)
	peers := resp["peers"].([]interface{})
	// Should have own + external (not gossip)
	if len(peers) != 2 {
		t.Fatalf("expected 2 peers (own + external), got %d", len(peers))
	}
}

// ---------- Global flag short aliases ----------

func TestHoistGlobalFlags_ShortAliases(t *testing.T) {
	flagJSON = false
	flagNoColor = false
	flagInsecure = false
	flagLocal = false

	args := []string{"--channels", "test", "-I", "-L", "--cache"}
	remaining := hoistGlobalFlags(args)

	if !flagInsecure {
		t.Error("-I not hoisted")
	}
	if !flagLocal {
		t.Error("-L not hoisted")
	}

	expected := []string{"--channels", "test", "--cache"}
	if len(remaining) != len(expected) {
		t.Fatalf("remaining: got %v, want %v", remaining, expected)
	}
	for i, arg := range remaining {
		if arg != expected[i] {
			t.Errorf("remaining[%d]: got %q, want %q", i, arg, expected[i])
		}
	}

	flagJSON = false
	flagNoColor = false
	flagInsecure = false
	flagLocal = false
}

func TestHoistGlobalFlags_AllShortFlags(t *testing.T) {
	flagJSON = false
	flagNoColor = false
	flagInsecure = false
	flagLocal = false

	args := []string{"-j", "-C", "-I", "-L", "rest"}
	remaining := hoistGlobalFlags(args)

	if !flagJSON {
		t.Error("-j not hoisted")
	}
	if !flagNoColor {
		t.Error("-C not hoisted")
	}
	if !flagInsecure {
		t.Error("-I not hoisted")
	}
	if !flagLocal {
		t.Error("-L not hoisted")
	}

	if len(remaining) != 1 || remaining[0] != "rest" {
		t.Errorf("remaining = %v", remaining)
	}

	flagJSON = false
	flagNoColor = false
	flagInsecure = false
	flagLocal = false
}
