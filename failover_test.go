package main

import (
	"testing"
)

// ---------- NewFailoverPool ----------

func TestFailoverPool_New(t *testing.T) {
	fp := newFailoverPool("TVabc123", "origin.tv:443", "secret", []string{
		"hint1.tv:443",
		"hint2.tv:443",
		"origin.tv:443", // should be excluded (same as currentHost)
		"hint1.tv:443",  // should be deduplicated (Go slice — not deduplicated at creation, only excluded if equal to current)
		"",              // empty should be excluded
	})

	if fp.currentHost != "origin.tv:443" {
		t.Errorf("currentHost = %q, want %q", fp.currentHost, "origin.tv:443")
	}
	if fp.channelID != "TVabc123" {
		t.Errorf("channelID = %q, want %q", fp.channelID, "TVabc123")
	}
	if fp.token != "secret" {
		t.Errorf("token = %q, want %q", fp.token, "secret")
	}

	// Current host and empty should be excluded from hints.
	// Note: the implementation excludes currentHost and empty, but does not
	// deduplicate within hints at creation time (only currentHost is excluded).
	for _, h := range fp.uriHints {
		if h == "origin.tv:443" {
			t.Error("uriHints should not contain currentHost")
		}
		if h == "" {
			t.Error("uriHints should not contain empty strings")
		}
	}
	// hint1.tv:443 appears twice in input, both non-current, so both are kept.
	// The pool deduplication happens at NextHost via the tried map.
}

// ---------- NextHost: Relay Tier ----------

func TestFailoverPool_NextHost_RelayTier(t *testing.T) {
	fp := newFailoverPool("TVabc", "relay1.tv:443", "", nil)
	fp.currentIsRelay = true
	fp.relays = []string{"relay2.tv:443"}
	fp.origins = []string{"origin1.tv:443"}
	fp.uriHints = []string{"hint1.tv:443"}

	// On relay: order should be relays → origins → hints.
	host, ok := fp.NextHost(false)
	if !ok || host != "relay2.tv:443" {
		t.Errorf("first = (%q, %v), want (%q, true)", host, ok, "relay2.tv:443")
	}
	host, ok = fp.NextHost(false)
	if !ok || host != "origin1.tv:443" {
		t.Errorf("second = (%q, %v), want (%q, true)", host, ok, "origin1.tv:443")
	}
	host, ok = fp.NextHost(false)
	if !ok || host != "hint1.tv:443" {
		t.Errorf("third = (%q, %v), want (%q, true)", host, ok, "hint1.tv:443")
	}
}

// ---------- NextHost: Origin Tier ----------

func TestFailoverPool_NextHost_OriginTier(t *testing.T) {
	fp := newFailoverPool("TVabc", "origin1.tv:443", "", nil)
	fp.currentIsRelay = false
	fp.origins = []string{"origin2.tv:443"}
	fp.relays = []string{"relay1.tv:443"}
	fp.uriHints = []string{"hint1.tv:443"}

	// On origin: order should be origins → relays → hints.
	host, ok := fp.NextHost(false)
	if !ok || host != "origin2.tv:443" {
		t.Errorf("first = (%q, %v), want (%q, true)", host, ok, "origin2.tv:443")
	}
	host, ok = fp.NextHost(false)
	if !ok || host != "relay1.tv:443" {
		t.Errorf("second = (%q, %v), want (%q, true)", host, ok, "relay1.tv:443")
	}
	host, ok = fp.NextHost(false)
	if !ok || host != "hint1.tv:443" {
		t.Errorf("third = (%q, %v), want (%q, true)", host, ok, "hint1.tv:443")
	}
}

// ---------- NextHost: ForOrigin ----------

func TestFailoverPool_NextHost_ForOrigin(t *testing.T) {
	fp := newFailoverPool("TVabc", "relay1.tv:443", "", nil)
	fp.currentIsRelay = true
	fp.origins = []string{"origin1.tv:443"}
	fp.relays = []string{"relay2.tv:443"}
	fp.uriHints = []string{"hint1.tv:443"}

	// forOrigin=true: order should be origins → relays → hints (regardless of current tier).
	host, ok := fp.NextHost(true)
	if !ok || host != "origin1.tv:443" {
		t.Errorf("first = (%q, %v), want (%q, true)", host, ok, "origin1.tv:443")
	}
	host, ok = fp.NextHost(true)
	if !ok || host != "relay2.tv:443" {
		t.Errorf("second = (%q, %v), want (%q, true)", host, ok, "relay2.tv:443")
	}
	host, ok = fp.NextHost(true)
	if !ok || host != "hint1.tv:443" {
		t.Errorf("third = (%q, %v), want (%q, true)", host, ok, "hint1.tv:443")
	}
}

// ---------- NextHost: Exhausted ----------

func TestFailoverPool_NextHost_Exhausted(t *testing.T) {
	fp := newFailoverPool("TVabc", "origin.tv:443", "", []string{"hint1.tv:443"})
	fp.origins = []string{"origin2.tv:443"}

	// Drain all candidates.
	fp.NextHost(false) // origin2
	fp.NextHost(false) // hint1

	host, ok := fp.NextHost(false)
	if ok {
		t.Errorf("expected exhausted, got (%q, true)", host)
	}
	if host != "" {
		t.Errorf("host = %q, want empty string", host)
	}
}

// ---------- SwitchTo + Reset ----------

func TestFailoverPool_SwitchTo(t *testing.T) {
	fp := newFailoverPool("TVabc", "origin.tv:443", "", nil)
	fp.origins = []string{"origin2.tv:443", "origin3.tv:443"}

	// Try one candidate.
	host, ok := fp.NextHost(false)
	if !ok || host != "origin2.tv:443" {
		t.Fatalf("first = (%q, %v), want (%q, true)", host, ok, "origin2.tv:443")
	}

	// Switch to the new host.
	fp.SwitchTo("origin2.tv:443")

	if fp.CurrentHost() != "origin2.tv:443" {
		t.Errorf("CurrentHost() = %q, want %q", fp.CurrentHost(), "origin2.tv:443")
	}

	// After SwitchTo, tried is reset — Reset also clears tried.
	fp.Reset()

	// Now candidates should be available again (origin2 is now currentHost,
	// but origin3 should still be in origins — though UpdateFromMetadata wasn't
	// called, the origins slice still contains the old values; origin2 is now
	// currentHost but the origins slice wasn't rebuilt).
	host, ok = fp.NextHost(false)
	if !ok {
		t.Error("expected candidates after Reset, got none")
	}
}

// ---------- UpdateFromMetadata ----------

func TestFailoverPool_UpdateFromMetadata(t *testing.T) {
	// normalizeOriginHost strips :443 (default HTTPS port), so the pool stores
	// normalized hostnames. Use port 8000 to keep values unambiguous in tests.
	fp := newFailoverPool("TVabc", "relay1.tv:8000", "", nil)

	doc := map[string]interface{}{
		"origins": []interface{}{"origin1.tv:443", "origin2.tv:8000"},
	}
	fp.UpdateFromMetadata(doc)

	// Origins should be populated (excluding currentHost which is not an origin).
	// normalizeOriginHost("origin1.tv:443") → "origin1.tv"
	// normalizeOriginHost("origin2.tv:8000") → "origin2.tv:8000"
	host, ok := fp.NextHost(false)
	if !ok {
		t.Fatal("expected candidates after UpdateFromMetadata")
	}
	// Current host is relay1, so the first candidate with forOrigin=false and
	// currentIsRelay=true should be from relays tier first — but we have no
	// relays, so it falls through to origins.
	if host != "origin1.tv" && host != "origin2.tv:8000" {
		t.Errorf("got %q, want one of the normalized origins", host)
	}

	// Verify currentIsRelay: relay1 is not in origins → should be relay.
	fp.mu.Lock()
	isRelay := fp.currentIsRelay
	fp.mu.Unlock()
	if !isRelay {
		t.Error("currentIsRelay should be true (relay1 not in origins)")
	}

	// Now test with currentHost as an origin.
	// normalizeOriginHost("origin1.tv:443") == normalizeOriginHost("origin1.tv:443") → "origin1.tv"
	fp2 := newFailoverPool("TVabc", "origin1.tv:443", "", nil)
	fp2.UpdateFromMetadata(doc)

	fp2.mu.Lock()
	isRelay2 := fp2.currentIsRelay
	fp2.mu.Unlock()
	if isRelay2 {
		t.Error("currentIsRelay should be false (origin1.tv:443 matches origins)")
	}
}

// ---------- UpdateFromPeers ----------

func TestFailoverPool_UpdateFromPeers(t *testing.T) {
	fp := newFailoverPool("TVabc", "origin.tv:443", "", nil)

	peers := []Peer{
		{ID: "TVabc", Hints: []string{"relay1.tv:443", "relay2.tv:443"}},
		{ID: "TVother", Hints: []string{"unrelated.tv:443"}},
		{ID: "TVabc", Hints: []string{"relay3.tv:443", "origin.tv:443"}}, // origin.tv is currentHost, excluded
	}
	fp.UpdateFromPeers(peers)

	// Should have relay1, relay2, relay3 (origin.tv excluded as currentHost, TVother filtered out).
	fp.mu.Lock()
	relays := make([]string, len(fp.relays))
	copy(relays, fp.relays)
	fp.mu.Unlock()

	expected := map[string]bool{
		"relay1.tv:443": true,
		"relay2.tv:443": true,
		"relay3.tv:443": true,
	}

	if len(relays) != len(expected) {
		t.Fatalf("relays = %v, want %d entries", relays, len(expected))
	}
	for _, r := range relays {
		if !expected[r] {
			t.Errorf("unexpected relay %q", r)
		}
	}

	// Verify non-matching channel IDs are filtered.
	for _, r := range relays {
		if r == "unrelated.tv:443" {
			t.Error("relays should not contain hints from non-matching channel IDs")
		}
	}
}

// ---------- TokenCarried ----------

func TestFailoverPool_TokenCarried(t *testing.T) {
	fp := newFailoverPool("TVabc", "origin.tv:443", "mytoken", nil)
	if fp.Token() != "mytoken" {
		t.Errorf("Token() = %q, want %q", fp.Token(), "mytoken")
	}

	// Token survives SwitchTo.
	fp.SwitchTo("other.tv:443")
	if fp.Token() != "mytoken" {
		t.Errorf("Token() after SwitchTo = %q, want %q", fp.Token(), "mytoken")
	}
}

// ---------- HasCandidates ----------

func TestFailoverPool_HasCandidates(t *testing.T) {
	fp := newFailoverPool("TVabc", "origin.tv:443", "", []string{"hint1.tv:443"})
	fp.origins = []string{"origin2.tv:443"}

	if !fp.HasCandidates() {
		t.Error("HasCandidates() should be true when untried hosts exist")
	}

	// Drain all.
	fp.NextHost(false) // origin2
	fp.NextHost(false) // hint1

	if fp.HasCandidates() {
		t.Error("HasCandidates() should be false when all candidates are tried")
	}

	// After Reset, should have candidates again.
	fp.Reset()
	if !fp.HasCandidates() {
		t.Error("HasCandidates() should be true after Reset")
	}
}

// ---------- Deduplication ----------

func TestFailoverPool_Deduplication(t *testing.T) {
	fp := newFailoverPool("TVabc", "current.tv:443", "", []string{
		"shared.tv:443",
		"shared.tv:443", // duplicate in hints
	})
	fp.origins = []string{"shared.tv:443"} // same host as a hint
	fp.relays = []string{"shared.tv:443"}  // same host as hint and origin

	// Despite shared.tv:443 appearing in origins, relays, and hints (twice),
	// NextHost should return it only once (via the tried map).
	var hosts []string
	for {
		host, ok := fp.NextHost(false)
		if !ok {
			break
		}
		hosts = append(hosts, host)
	}

	if len(hosts) != 1 {
		t.Errorf("got %d unique hosts %v, want 1 (shared.tv:443 should appear once)", len(hosts), hosts)
	}
	if len(hosts) > 0 && hosts[0] != "shared.tv:443" {
		t.Errorf("host = %q, want %q", hosts[0], "shared.tv:443")
	}
}
