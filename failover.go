package main

import (
	"sync"
)

// failoverPool tracks all known hosts for a channel and provides
// tier-aware failover ordering. Thread-safe for concurrent use.
type failoverPool struct {
	mu sync.Mutex

	channelID string
	token     string // access token, carried across failover

	// Current connection.
	currentHost    string
	currentIsRelay bool // true if current host is a relay (not in origins)

	// Candidate pools (deduplicated, excluding currentHost).
	origins  []string // from signed metadata origins field (unforgeable)
	relays   []string // from peer exchange hints
	uriHints []string // from original tltv:// URI ?via= hints

	// Rotation state.
	tried map[string]bool // hosts already tried in this failover round
}

// newFailoverPool creates a pool seeded with the initial host and URI hints.
func newFailoverPool(channelID, currentHost, token string, uriHints []string) *failoverPool {
	fp := &failoverPool{
		channelID:   channelID,
		token:       token,
		currentHost: currentHost,
		tried:       make(map[string]bool),
	}
	// Deduplicate URI hints, excluding current host.
	for _, h := range uriHints {
		if h != "" && h != currentHost {
			fp.uriHints = append(fp.uriHints, h)
		}
	}
	return fp
}

// UpdateFromMetadata refreshes the origins list from verified metadata.
// Call after each successful metadata verification.
func (fp *failoverPool) UpdateFromMetadata(doc map[string]interface{}) {
	origins := extractOrigins(doc)
	fp.mu.Lock()
	defer fp.mu.Unlock()

	// Deduplicate origins, excluding current host.
	fp.origins = fp.origins[:0]
	seen := make(map[string]bool)
	for _, o := range origins {
		norm := normalizeOriginHost(o)
		if norm != "" && !seen[norm] && norm != fp.currentHost {
			fp.origins = append(fp.origins, norm)
			seen[norm] = true
		}
	}

	// Determine if current host is an origin.
	currentNorm := normalizeOriginHost(fp.currentHost)
	fp.currentIsRelay = true
	for _, o := range origins {
		if normalizeOriginHost(o) == currentNorm {
			fp.currentIsRelay = false
			break
		}
	}
}

// UpdateFromPeers refreshes the relay list from peer exchange data.
// Call after fetching /tltv/v1/peers.
func (fp *failoverPool) UpdateFromPeers(peers []Peer) {
	fp.mu.Lock()
	defer fp.mu.Unlock()

	fp.relays = fp.relays[:0]
	seen := make(map[string]bool)
	for _, p := range peers {
		if p.ID != fp.channelID {
			continue
		}
		for _, hint := range p.Hints {
			if hint != "" && !seen[hint] && hint != fp.currentHost {
				fp.relays = append(fp.relays, hint)
				seen[hint] = true
			}
		}
	}
}

// NextHost returns the next host to try during failover. Returns empty string
// and false when all candidates are exhausted.
//
// Tier-aware ordering:
//   - If currently on a relay: try other relays -> origins -> URI hints
//   - If currently on an origin: try other origins -> relays -> URI hints
//   - Relay upstream (forOrigin=true): try origins first -> relays -> URI hints
func (fp *failoverPool) NextHost(forOrigin bool) (host string, ok bool) {
	fp.mu.Lock()
	defer fp.mu.Unlock()

	// Build ordered candidate list based on context.
	var candidates []string
	if forOrigin {
		// Relay upstream: prefer origins (need authoritative stream).
		candidates = append(candidates, fp.origins...)
		candidates = append(candidates, fp.relays...)
		candidates = append(candidates, fp.uriHints...)
	} else if fp.currentIsRelay {
		// Client was on relay: try other relays first (topologically close).
		candidates = append(candidates, fp.relays...)
		candidates = append(candidates, fp.origins...)
		candidates = append(candidates, fp.uriHints...)
	} else {
		// Client was on origin: try other origins first.
		candidates = append(candidates, fp.origins...)
		candidates = append(candidates, fp.relays...)
		candidates = append(candidates, fp.uriHints...)
	}

	for _, c := range candidates {
		if !fp.tried[c] {
			fp.tried[c] = true
			return c, true
		}
	}

	return "", false
}

// SwitchTo marks a successful failover to a new host.
// Resets the tried set and updates the current host.
func (fp *failoverPool) SwitchTo(host string) {
	fp.mu.Lock()
	defer fp.mu.Unlock()

	fp.currentHost = host
	fp.tried = make(map[string]bool)
	// Don't know if new host is relay or origin until next metadata update.
}

// Reset clears the tried set without changing the current host.
// Call after a successful reconnection to the current host.
func (fp *failoverPool) Reset() {
	fp.mu.Lock()
	defer fp.mu.Unlock()
	fp.tried = make(map[string]bool)
}

// CurrentHost returns the current host.
func (fp *failoverPool) CurrentHost() string {
	fp.mu.Lock()
	defer fp.mu.Unlock()
	return fp.currentHost
}

// Token returns the access token.
func (fp *failoverPool) Token() string {
	fp.mu.Lock()
	defer fp.mu.Unlock()
	return fp.token
}

// HasCandidates returns true if there are untried failover candidates.
func (fp *failoverPool) HasCandidates() bool {
	fp.mu.Lock()
	defer fp.mu.Unlock()

	for _, c := range fp.origins {
		if !fp.tried[c] {
			return true
		}
	}
	for _, c := range fp.relays {
		if !fp.tried[c] {
			return true
		}
	}
	for _, c := range fp.uriHints {
		if !fp.tried[c] {
			return true
		}
	}
	return false
}
