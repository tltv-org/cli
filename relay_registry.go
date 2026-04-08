package main

import (
	"sync"
	"time"
)

// ---------- Types ----------

// relayRegisteredChannel is a channel being relayed with cached verified documents.
// Once placed in the registry map, must never be mutated. Uses immutable replacement.
type relayRegisteredChannel struct {
	ChannelID    string
	Name         string
	Hints        []string           // upstream host:port sources
	Metadata     []byte             // raw verified metadata JSON (served verbatim)
	Guide        []byte             // raw verified guide JSON (served verbatim)
	GuideEntries []guideEntry // parsed entries (for XMLTV generation)
	StreamHint   string             // best upstream for stream proxying
	LastVerified time.Time
}

// relayRegistry manages relayed channels and peer state.
// Thread-safe via sync.RWMutex with immutable replacement.
type relayRegistry struct {
	mu            sync.RWMutex
	channels      map[string]*relayRegisteredChannel // channelID -> channel
	peers         map[string]*peerEntry          // channelID -> peer info (from gossip)
	hostname      string
	gossipEnabled bool // include gossip-discovered peers in the peers response
	maxPeers      int
	staleDays     int
}

// ---------- Constructor ----------

func newRelayRegistry(hostname string, gossipEnabled bool, maxPeers, staleDays int) *relayRegistry {
	if maxPeers <= 0 {
		maxPeers = 100
	}
	if staleDays <= 0 {
		staleDays = 7
	}
	return &relayRegistry{
		channels:      make(map[string]*relayRegisteredChannel),
		peers:         make(map[string]*peerEntry),
		hostname:      hostname,
		gossipEnabled: gossipEnabled,
		maxPeers:      maxPeers,
		staleDays:     staleDays,
	}
}

// ---------- Read Methods (RLock) ----------

// GetChannel returns a relayed channel by ID, or nil.
func (r *relayRegistry) GetChannel(id string) *relayRegisteredChannel {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.channels[id]
}

// ListChannels returns all relayed channels.
func (r *relayRegistry) ListChannels() []*relayRegisteredChannel {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]*relayRegisteredChannel, 0, len(r.channels))
	for _, ch := range r.channels {
		result = append(result, ch)
	}
	return result
}

// ChannelCount returns the number of relayed channels.
func (r *relayRegistry) ChannelCount() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.channels)
}

// ListPeers returns peer entries for the gossip exchange response.
// Always includes relayed channels with our hostname as hint.
// Gossip-discovered peers are only included when gossipEnabled is true.
// Applies staleness cutoff and max limit.
func (r *relayRegistry) ListPeers() []peerEntry {
	r.mu.RLock()
	defer r.mu.RUnlock()

	cutoff := time.Now().Add(-time.Duration(r.staleDays) * 24 * time.Hour)
	var result []peerEntry

	// Our own relayed channels
	for _, ch := range r.channels {
		var hints []string
		if r.hostname != "" {
			hints = []string{r.hostname}
		}
		result = append(result, peerEntry{
			ChannelID: ch.ChannelID,
			Name:      ch.Name,
			Hints:     hints,
			LastSeen:  ch.LastVerified,
		})
	}

	// Gossip-discovered peers (only when --gossip is enabled)
	if r.gossipEnabled {
		for _, p := range r.peers {
			if _, relaying := r.channels[p.ChannelID]; relaying {
				continue // already included from our own channels
			}
			if p.LastSeen.Before(cutoff) {
				continue // stale
			}
			result = append(result, *p)
		}
	}

	// Apply max limit
	if len(result) > r.maxPeers {
		result = result[:r.maxPeers]
	}

	return result
}

// ---------- Write Methods (Lock) ----------

// UpdateChannel adds or updates a relayed channel with verified metadata.
// The raw bytes are served verbatim; the doc is used for field extraction only.
func (r *relayRegistry) UpdateChannel(channelID string, raw []byte, doc map[string]interface{}, hints []string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	name := getString(doc, "name")

	// Pick best stream hint (first available)
	streamHint := ""
	if len(hints) > 0 {
		streamHint = hints[0]
	}

	// Preserve existing guide if we have one
	var guide []byte
	var guideEntries []guideEntry
	if old, ok := r.channels[channelID]; ok {
		guide = old.Guide
		guideEntries = old.GuideEntries
	}

	r.channels[channelID] = &relayRegisteredChannel{
		ChannelID:    channelID,
		Name:         name,
		Hints:        hints,
		Metadata:     raw,
		Guide:        guide,
		GuideEntries: guideEntries,
		StreamHint:   streamHint,
		LastVerified: time.Now(),
	}
}

// UpdateGuide updates the cached guide for a relayed channel.
func (r *relayRegistry) UpdateGuide(channelID string, raw []byte, entries []guideEntry) {
	r.mu.Lock()
	defer r.mu.Unlock()

	old, ok := r.channels[channelID]
	if !ok {
		return
	}

	// Immutable replacement
	updated := &relayRegisteredChannel{
		ChannelID:    old.ChannelID,
		Name:         old.Name,
		Hints:        old.Hints,
		Metadata:     old.Metadata,
		Guide:        raw,
		GuideEntries: entries,
		StreamHint:   old.StreamHint,
		LastVerified: old.LastVerified,
	}
	r.channels[channelID] = updated
}

// RemoveChannel removes a channel from the relay.
func (r *relayRegistry) RemoveChannel(channelID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.channels, channelID)
}

// StoreMigration stores a migration document at the old channel's ID
// and removes the old channel from active relay.
func (r *relayRegistry) StoreMigration(channelID string, raw []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Store the migration doc so it can be served at the old endpoint
	r.channels[channelID] = &relayRegisteredChannel{
		ChannelID:    channelID,
		Name:         "(migrated)",
		Metadata:     raw,
		LastVerified: time.Now(),
	}
}

// MergePeers adds validated peer entries from gossip exchange.
func (r *relayRegistry) MergePeers(peers []peerEntry) {
	r.mu.Lock()
	defer r.mu.Unlock()

	for i := range peers {
		p := peers[i]
		r.peers[p.ChannelID] = &p
	}

	// Prune stale peers
	cutoff := time.Now().Add(-time.Duration(r.staleDays) * 24 * time.Hour)
	for id, p := range r.peers {
		if p.LastSeen.Before(cutoff) {
			delete(r.peers, id)
		}
	}
}
