package main

import (
	"context"
	"flag"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

// ---------- Types ----------

// peerTarget represents a parsed --peers entry: a channel to verify and advertise.
type peerTarget struct {
	ChannelID string
	Hints     []string
}

// peerEntry is a verified peer channel for the peers response.
type peerEntry struct {
	ChannelID string
	Name      string
	Hints     []string
	LastSeen  time.Time
}

// ---------- Registry ----------

// peerRegistry tracks verified external peer channels for the peers endpoint.
// Thread-safe via sync.RWMutex with immutable read snapshots.
type peerRegistry struct {
	mu      sync.RWMutex
	entries map[string]*peerEntry // channelID → verified info
}

func newPeerRegistry() *peerRegistry {
	return &peerRegistry{
		entries: make(map[string]*peerEntry),
	}
}

// ListPeers returns all verified peer entries as a snapshot.
func (r *peerRegistry) ListPeers() []peerEntry {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]peerEntry, 0, len(r.entries))
	for _, e := range r.entries {
		result = append(result, *e)
	}
	return result
}

// Update adds or refreshes a verified peer entry.
func (r *peerRegistry) Update(channelID, name string, hints []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.entries[channelID] = &peerEntry{
		ChannelID: channelID,
		Name:      name,
		Hints:     hints,
		LastSeen:  time.Now(),
	}
}

// Remove drops a peer entry (migrated, private, retired, or unreachable).
func (r *peerRegistry) Remove(channelID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.entries, channelID)
}

// ---------- Flags ----------

// addPeersFlag registers --peers / -P / PEERS env var on a FlagSet.
// Takes comma-separated tltv:// URIs.
func addPeersFlag(fs *flag.FlagSet) *string {
	peersStr := fs.String("peers", os.Getenv("PEERS"), "tltv:// URIs to advertise in peer exchange")
	fs.StringVar(peersStr, "P", os.Getenv("PEERS"), "alias for --peers")
	return peersStr
}

// addGossipFlag registers --gossip / -g / GOSSIP=1 env var on a FlagSet.
// When enabled, discovered channels from peers' /tltv/v1/peers endpoints
// are validated and included in this node's own peers response.
func addGossipFlag(fs *flag.FlagSet) *bool {
	gossipEnabled := fs.Bool("gossip", os.Getenv("GOSSIP") == "1", "re-advertise validated gossip-discovered channels")
	fs.BoolVar(gossipEnabled, "g", os.Getenv("GOSSIP") == "1", "alias for --gossip")
	return gossipEnabled
}

// addProxyFlag registers --proxy/-x / PROXY env var on a FlagSet.
// Accepts socks5://, http://, https:// proxy URLs.
func addProxyFlag(fs *flag.FlagSet) *string {
	p := fs.String("proxy", os.Getenv("PROXY"), "proxy URL (socks5://, http://, https://)")
	fs.StringVar(p, "x", os.Getenv("PROXY"), "alias for --proxy")
	return p
}

// parseProxyURL validates and parses a proxy URL string.
// Returns nil if the string is empty.
func parseProxyURL(s string) (*url.URL, error) {
	if s == "" {
		return nil, nil
	}
	u, err := url.Parse(s)
	if err != nil {
		return nil, fmt.Errorf("invalid proxy URL: %v", err)
	}
	switch u.Scheme {
	case "socks5", "http", "https":
	default:
		return nil, fmt.Errorf("unsupported proxy scheme %q (use socks5://, http://, or https://)", u.Scheme)
	}
	return u, nil
}

type configFlagOpts struct {
	ConfigShort string
	DumpShort   string
}

// addConfigFlags registers --config and --dump-config flags on a FlagSet.
// Env vars: CONFIG.
func addConfigFlags(fs *flag.FlagSet, opts configFlagOpts) (configPath *string, dumpConfig *bool) {
	configPath = fs.String("config", os.Getenv("CONFIG"), "config file (JSON)")
	dumpConfig = fs.Bool("dump-config", false, "print resolved config as JSON and exit")
	if opts.ConfigShort != "" {
		fs.StringVar(configPath, opts.ConfigShort, os.Getenv("CONFIG"), "alias for --config")
	}
	if opts.DumpShort != "" {
		fs.BoolVar(dumpConfig, opts.DumpShort, false, "alias for --dump-config")
	}
	return
}

// ---------- Parsing ----------

// parsePeerTargets parses comma-separated tltv:// URIs into targets.
// Returns nil for empty input. Rejects bare host:port values.
func parsePeerTargets(raw string) ([]peerTarget, error) {
	if raw == "" {
		return nil, nil
	}

	var targets []peerTarget
	for _, entry := range strings.Split(raw, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}

		// Must be a tltv:// URI
		if !strings.HasPrefix(entry, "tltv://") {
			return nil, fmt.Errorf("--peers requires tltv:// URIs (e.g. \"tltv://TVabc...@host:443\"), got %q", entry)
		}

		uri, err := parseTLTVUri(entry)
		if err != nil {
			return nil, fmt.Errorf("invalid peer URI %q: %v", entry, err)
		}

		if len(uri.Hints) == 0 {
			return nil, fmt.Errorf("peer URI %q has no hints (need @host:port)", entry)
		}

		targets = append(targets, peerTarget{
			ChannelID: uri.ChannelID,
			Hints:     uri.Hints,
		})
	}
	return targets, nil
}

// ---------- Poll Loop ----------

// peerPollLoop periodically fetches and verifies metadata for each --peers target.
// Verified channels are added to the registry. Channels that fail verification,
// become private, or become retired are removed.
// Runs an initial fetch immediately, then polls at the given interval.
func peerPollLoop(ctx context.Context, client *Client, targets []peerTarget,
	registry *peerRegistry, interval time.Duration) {

	// Initial fetch for all targets
	for _, t := range targets {
		peerVerifyAndUpdate(client, t, registry)
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			for _, t := range targets {
				peerVerifyAndUpdate(client, t, registry)
			}
		}
	}
}

// peerVerifyAndUpdate fetches metadata for a single peer target, verifies it,
// and updates the registry. On failure, removes the entry.
func peerVerifyAndUpdate(client *Client, target peerTarget, registry *peerRegistry) {
	res, err := fetchAndVerifyMetadata(client, target.ChannelID, target.Hints)
	if err != nil {
		logDebugf("peer %s: verify failed: %v", target.ChannelID, err)
		return // keep stale entry — don't remove on transient failure
	}

	if res.IsMigration {
		logInfof("peer %s: channel migrated, removing from peers", target.ChannelID)
		registry.Remove(target.ChannelID)
		return
	}

	// Check not private/retired/on-demand
	if err := checkChannelAccess(res.Doc); err != nil {
		logInfof("peer %s: %v, removing from peers", target.ChannelID, err)
		registry.Remove(target.ChannelID)
		return
	}

	name := getString(res.Doc, "name")
	registry.Update(target.ChannelID, name, target.Hints)
}

// ---------- Response Builder ----------

// buildPeersResponse creates the JSON peer exchange response from multiple sources.
// Combines originated/relayed channels with verified external peers.
func buildPeersResponse(ownChannels []peerEntry, externalPeers []peerEntry) []map[string]interface{} {
	seen := make(map[string]bool)
	var result []map[string]interface{}

	add := func(entries []peerEntry) {
		for _, e := range entries {
			if seen[e.ChannelID] {
				continue
			}
			seen[e.ChannelID] = true
			hints := e.Hints
			if hints == nil {
				hints = []string{}
			}
			result = append(result, map[string]interface{}{
				"id":        e.ChannelID,
				"name":      e.Name,
				"hints":     hints,
				"last_seen": e.LastSeen.UTC().Format(timestampFormat),
			})
		}
	}

	add(ownChannels)
	add(externalPeers)

	if result == nil {
		result = []map[string]interface{}{}
	}
	return result
}

// ---------- Gossip ----------

// gossipNodesFromPeers extracts unique host:port nodes from --peers targets.
// These nodes are polled for their /tltv/v1/peers to discover additional channels.
func gossipNodesFromPeers(targets []peerTarget) []string {
	seen := make(map[string]bool)
	var nodes []string
	for _, t := range targets {
		for _, h := range t.Hints {
			if !seen[h] {
				seen[h] = true
				nodes = append(nodes, h)
			}
		}
	}
	return nodes
}

// nodeServesChannel checks whether a node's info lists the given channel ID
// in either its channels or relaying arrays.
func nodeServesChannel(info *NodeInfo, channelID string) bool {
	for _, ch := range info.Channels {
		if ch.ID == channelID {
			return true
		}
	}
	for _, ch := range info.Relaying {
		if ch.ID == channelID {
			return true
		}
	}
	return false
}

// gossipPollLoop fetches /tltv/v1/peers from the given nodes, validates
// discovered channels, and stores them via storeFn.
// Used by all three daemons when --gossip is enabled.
// Runs an initial poll immediately, then at the given interval.
func gossipPollLoop(ctx context.Context, client *Client, nodes []string,
	storeFn func(id, name string, hints []string), interval time.Duration) {

	gossipPollOnce(client, nodes, storeFn)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			gossipPollOnce(client, nodes, storeFn)
		}
	}
}

// gossipPollOnce performs a single gossip cycle: fetch peers from each node,
// validate each discovered channel (node info → metadata verify → access check),
// and store via storeFn. Bridge/server pass peerRegistry.Update; relay passes
// a wrapper for relayRegistry.MergePeers.
func gossipPollOnce(client *Client, nodes []string, storeFn func(id, name string, hints []string)) {
	for _, node := range nodes {
		node = normalizeHost(node)
		exchange, err := client.FetchPeers(node)
		if err != nil {
			logDebugf("gossip %s: %v", node, err)
			continue
		}

		for _, p := range exchange.Peers {
			if len(p.Hints) == 0 {
				continue
			}
			hint := p.Hints[0]

			// Step 1: verify channel is listed at the hint
			info, err := client.FetchNodeInfo(hint)
			if err != nil {
				continue
			}
			if !nodeServesChannel(info, p.ID) {
				continue
			}

			// Step 2: fetch and verify signed metadata
			metaRes, err := fetchAndVerifyMetadata(client, p.ID, []string{hint})
			if err != nil {
				continue
			}
			if metaRes.IsMigration {
				continue
			}

			// Step 3: check not private/retired/on-demand
			if err := checkChannelAccess(metaRes.Doc); err != nil {
				continue
			}

			name := getString(metaRes.Doc, "name")
			storeFn(p.ID, name, p.Hints)
		}
	}
}
