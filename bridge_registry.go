package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// ---------- Types ----------

// bridgeRegisteredChannel is a channel with a TLTV identity and cached signed documents.
// Once placed in the registry map, a bridgeRegisteredChannel must never be mutated.
// Updates use immutable replacement: build a new struct and swap the map entry.
type bridgeRegisteredChannel struct {
	ChannelID  string
	PublicKey  ed25519.PublicKey
	PrivateKey ed25519.PrivateKey
	UpstreamID string

	Name            string
	Description     string
	Language        string
	Timezone        string
	IconFileName    string
	Logo            string
	StreamURL       string
	Access          string
	Token           string
	Tags            []string
	OnDemand        bool
	SourceChannelID string // upstream TLTV channel ID (for automatic relay_from)

	Guide    []guideEntry // source guide entries (may be empty)
	metadata []byte       // cached signed metadata JSON
	guideDoc []byte       // cached signed guide JSON
}

// IsPrivate returns true if the channel requires token authentication.
func (ch *bridgeRegisteredChannel) IsPrivate() bool {
	return ch.Access == "token" && ch.Token != ""
}

// bridgeRegistry manages channel identities and signed documents.
// Thread-safe via sync.RWMutex with immutable replacement pattern.
type bridgeRegistry struct {
	mu            sync.RWMutex
	channels      map[string]*bridgeRegisteredChannel // TLTV channel ID -> channel
	byUpstream    map[string]string                   // upstream ID -> TLTV channel ID
	keysDir       string
	hostname      string
	iconFileName  string // icon file name for all channels (e.g. "icon.svg")
	singleKeyPath string // --key file for single-channel mode (overrides keysDir)
}

// ---------- Constructor ----------

func newBridgeRegistry(keysDir, hostname string) *bridgeRegistry {
	return &bridgeRegistry{
		channels:   make(map[string]*bridgeRegisteredChannel),
		byUpstream: make(map[string]string),
		keysDir:    keysDir,
		hostname:   hostname,
	}
}

// ---------- Read Methods (RLock) ----------

// GetChannel returns a channel by TLTV channel ID, or nil if not found.
// The returned pointer is to an immutable struct -- safe to use without locks.
func (r *bridgeRegistry) GetChannel(id string) *bridgeRegisteredChannel {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.channels[id]
}

// ListChannels returns all registered channels.
func (r *bridgeRegistry) ListChannels() []*bridgeRegisteredChannel {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]*bridgeRegisteredChannel, 0, len(r.channels))
	for _, ch := range r.channels {
		result = append(result, ch)
	}
	return result
}

// PublicChannelCount returns the number of non-private channels.
func (r *bridgeRegistry) PublicChannelCount() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	n := 0
	for _, ch := range r.channels {
		if !ch.IsPrivate() {
			n++
		}
	}
	return n
}

// ---------- Write Methods (Lock) ----------

// UpdateChannels syncs the registry with a new channel list.
// Adds new channels, updates existing ones, removes stale ones.
func (r *bridgeRegistry) UpdateChannels(channels []bridgeChannel) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	seen := make(map[string]bool, len(channels))

	for _, ch := range channels {
		seen[ch.ID] = true

		if tltvID, ok := r.byUpstream[ch.ID]; ok {
			// Update existing channel -- build new immutable struct
			old := r.channels[tltvID]
			updated := &bridgeRegisteredChannel{
				ChannelID:       old.ChannelID,
				PublicKey:       old.PublicKey,
				PrivateKey:      old.PrivateKey,
				UpstreamID:      old.UpstreamID,
				Name:            ch.Name,
				Description:     ch.Description,
				Tags:            ch.Tags,
				Language:        ch.Language,
				Timezone:        ch.Timezone,
				IconFileName:    r.iconFileName,
				Logo:            ch.Logo,
				StreamURL:       ch.Stream,
				Access:          ch.Access,
				Token:           ch.Token,
				OnDemand:        ch.OnDemand,
				SourceChannelID: ch.SourceChannelID,
				Guide:           old.Guide, // preserve existing guide
			}
			if err := r.signChannel(updated); err != nil {
				return fmt.Errorf("signing channel %s: %w", ch.Name, err)
			}
			r.channels[tltvID] = updated
		} else {
			// Register new channel
			priv, pub, err := r.loadOrCreateKey(ch.ID)
			if err != nil {
				return fmt.Errorf("key for %s: %w", ch.ID, err)
			}
			channelID := makeChannelID(pub)

			registered := &bridgeRegisteredChannel{
				ChannelID:       channelID,
				PublicKey:       pub,
				PrivateKey:      priv,
				UpstreamID:      ch.ID,
				Name:            ch.Name,
				Description:     ch.Description,
				Tags:            ch.Tags,
				Language:        ch.Language,
				Timezone:        ch.Timezone,
				IconFileName:    r.iconFileName,
				Logo:            ch.Logo,
				StreamURL:       ch.Stream,
				Access:          ch.Access,
				Token:           ch.Token,
				OnDemand:        ch.OnDemand,
				SourceChannelID: ch.SourceChannelID,
			}
			if err := r.signChannel(registered); err != nil {
				return fmt.Errorf("signing channel %s: %w", ch.Name, err)
			}
			r.channels[channelID] = registered
			r.byUpstream[ch.ID] = channelID
		}
	}

	// Remove stale channels
	for upstreamID, tltvID := range r.byUpstream {
		if !seen[upstreamID] {
			delete(r.channels, tltvID)
			delete(r.byUpstream, upstreamID)
		}
	}

	return nil
}

// UpdateStreamURL updates the stream URL for a channel identified by upstream ID.
// Used when TLTV source re-resolution detects a stream path change.
func (r *bridgeRegistry) UpdateStreamURL(upstreamID, newStreamURL string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	tltvID, ok := r.byUpstream[upstreamID]
	if !ok {
		return
	}
	old := r.channels[tltvID]
	if old.StreamURL == newStreamURL {
		return
	}

	updated := &bridgeRegisteredChannel{
		ChannelID:       old.ChannelID,
		PublicKey:       old.PublicKey,
		PrivateKey:      old.PrivateKey,
		UpstreamID:      old.UpstreamID,
		Name:            old.Name,
		Description:     old.Description,
		Tags:            old.Tags,
		Language:        old.Language,
		Timezone:        old.Timezone,
		IconFileName:    old.IconFileName,
		Logo:            old.Logo,
		StreamURL:       newStreamURL,
		Access:          old.Access,
		Token:           old.Token,
		OnDemand:        old.OnDemand,
		SourceChannelID: old.SourceChannelID,
		Guide:           old.Guide,
	}
	if err := r.signChannel(updated); err != nil {
		logErrorf("re-sign after stream URL change for %s: %v", upstreamID, err)
		return
	}
	r.channels[tltvID] = updated
}

// UpdateGuide replaces guide entries for all registered channels.
// Keys are upstream channel IDs. Any registered channel missing from the map has
// its guide cleared and falls back to the default ephemeral guide on re-sign.
func (r *bridgeRegistry) UpdateGuide(guide map[string][]guideEntry) {
	r.mu.Lock()
	defer r.mu.Unlock()

	for upstreamID, tltvID := range r.byUpstream {
		entries := guide[upstreamID]
		old := r.channels[tltvID]

		// Build new immutable struct with updated guide
		updated := &bridgeRegisteredChannel{
			ChannelID:       old.ChannelID,
			PublicKey:       old.PublicKey,
			PrivateKey:      old.PrivateKey,
			UpstreamID:      old.UpstreamID,
			Name:            old.Name,
			Description:     old.Description,
			Tags:            old.Tags,
			Language:        old.Language,
			Timezone:        old.Timezone,
			IconFileName:    old.IconFileName,
			Logo:            old.Logo,
			StreamURL:       old.StreamURL,
			Access:          old.Access,
			Token:           old.Token,
			OnDemand:        old.OnDemand,
			SourceChannelID: old.SourceChannelID,
			Guide:           entries,
		}
		// Re-sign guide and metadata
		if err := r.signChannel(updated); err != nil {
			logErrorf("guide signing error for %s: %v", upstreamID, err)
			continue
		}
		r.channels[tltvID] = updated
	}
}

// ---------- Key Management ----------

// loadOrCreateKey loads an existing key or generates a new one for a channel.
// When singleKeyPath is set, uses that file for the first (only) channel
// instead of auto-generating into keysDir.
func (r *bridgeRegistry) loadOrCreateKey(upstreamID string) (ed25519.PrivateKey, ed25519.PublicKey, error) {
	// Single-key mode: use explicit key file (for single-channel sources)
	if r.singleKeyPath != "" {
		seed, err := readSeed(r.singleKeyPath)
		if err != nil {
			return nil, nil, fmt.Errorf("reading key %s: %w", r.singleKeyPath, err)
		}
		priv, pub := keyFromSeed(seed)
		return priv, pub, nil
	}

	filename := bridgeSanitizeFilename(upstreamID) + ".key"
	keyPath := filepath.Join(r.keysDir, filename)

	// Try to load existing key
	seed, err := readSeed(keyPath)
	if err == nil {
		priv, pub := keyFromSeed(seed)
		return priv, pub, nil
	}

	if !os.IsNotExist(err) {
		return nil, nil, fmt.Errorf("reading key %s: %w", keyPath, err)
	}

	// Generate new keypair
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("generating key: %w", err)
	}

	if err := writeSeed(keyPath, priv.Seed()); err != nil {
		return nil, nil, fmt.Errorf("writing key %s: %w", keyPath, err)
	}

	return priv, pub, nil
}

// ---------- Signing ----------

// signChannel signs both guide and metadata documents for a channel.
// Sets ch.guideDoc and ch.metadata. Must be called before placing ch in the map.
func (r *bridgeRegistry) signChannel(ch *bridgeRegisteredChannel) error {
	guideData, err := bridgeSignGuide(ch)
	if err != nil {
		return fmt.Errorf("signing guide: %w", err)
	}
	ch.guideDoc = guideData

	metaData, err := bridgeSignMetadata(ch, r.hostname)
	if err != nil {
		return fmt.Errorf("signing metadata: %w", err)
	}
	ch.metadata = metaData

	return nil
}

// bridgeSignMetadata builds and signs channel metadata.
func bridgeSignMetadata(ch *bridgeRegisteredChannel, hostname string) ([]byte, error) {
	now := time.Now().UTC()

	access := "public"
	if ch.Access == "token" {
		access = "token"
	}

	doc := map[string]interface{}{
		"v":       json.Number("1"),
		"seq":     json.Number(fmt.Sprintf("%d", now.Unix())),
		"id":      ch.ChannelID,
		"name":    ch.Name,
		"stream":  "/tltv/v1/channels/" + ch.ChannelID + "/stream.m3u8",
		"access":  access,
		"status":  "active",
		"updated": now.Format(timestampFormat),
	}

	// Optional fields
	if ch.Description != "" {
		doc["description"] = ch.Description
	}
	if len(ch.Tags) > 0 {
		tags := ch.Tags
		if len(tags) > 5 {
			tags = tags[:5]
		}
		iface := make([]interface{}, len(tags))
		for i, t := range tags {
			iface[i] = t
		}
		doc["tags"] = iface
	}
	if ch.Language != "" {
		doc["language"] = ch.Language
	}
	if ch.Timezone != "" {
		doc["timezone"] = ch.Timezone
	}
	if ch.OnDemand {
		doc["on_demand"] = true
	}

	// Always include guide and icon paths
	doc["guide"] = "/tltv/v1/channels/" + ch.ChannelID + "/guide.json"
	if ch.IconFileName != "" {
		doc["icon"] = "/tltv/v1/channels/" + ch.ChannelID + "/" + ch.IconFileName
	}

	// Origins
	if hostname != "" {
		doc["origins"] = []interface{}{hostname}
	}

	signed, err := signDocument(doc, ch.PrivateKey)
	if err != nil {
		return nil, err
	}

	data, err := json.Marshal(signed)
	if err != nil {
		return nil, err
	}

	return data, nil
}

// bridgeSignGuide builds and signs a channel guide document.
func bridgeSignGuide(ch *bridgeRegisteredChannel) ([]byte, error) {
	now := time.Now().UTC()

	guideEntries := ch.Guide
	isDefaultGuide := len(guideEntries) == 0
	if isDefaultGuide {
		guideEntries = defaultGuideEntries(ch.Name)
		// Automatic relay_from attribution for TLTV sources:
		// When using the default ephemeral guide and the channel was sourced from
		// a TLTV URI, set relay_from on each entry to attribute the content.
		// Explicit guide entries (from config, sidecar, or external guide) are
		// never modified — their relay_from values (or lack thereof) take precedence.
		if ch.SourceChannelID != "" {
			for i := range guideEntries {
				if guideEntries[i].RelayFrom == "" {
					guideEntries[i].RelayFrom = ch.SourceChannelID
				}
			}
		}
	}

	// Spec section 6.3: entries MUST be ordered by start time
	sort.Slice(guideEntries, func(i, j int) bool {
		return guideEntries[i].Start < guideEntries[j].Start
	})

	// Compute from/until from entry bounds
	from := guideEntries[0].Start
	until := guideEntries[0].End
	for _, e := range guideEntries[1:] {
		if e.Start < from {
			from = e.Start
		}
		if e.End > until {
			until = e.End
		}
	}

	// Build entries as []interface{} for canonicalJSON
	var entries []interface{}
	for _, e := range guideEntries {
		entry := map[string]interface{}{
			"start": e.Start,
			"end":   e.End,
			"title": e.Title,
		}
		if e.Description != "" {
			entry["description"] = e.Description
		}
		if e.Category != "" {
			entry["category"] = e.Category
		}
		if e.RelayFrom != "" {
			entry["relay_from"] = e.RelayFrom
		}
		entries = append(entries, entry)
	}

	doc := map[string]interface{}{
		"v":       json.Number("1"),
		"seq":     json.Number(fmt.Sprintf("%d", now.Unix())),
		"id":      ch.ChannelID,
		"from":    from,
		"until":   until,
		"entries": entries,
		"updated": now.Format(timestampFormat),
	}

	signed, err := signDocument(doc, ch.PrivateKey)
	if err != nil {
		return nil, err
	}

	data, err := json.Marshal(signed)
	if err != nil {
		return nil, err
	}

	return data, nil
}

// defaultGuideEntries generates a single entry spanning today midnight-to-midnight UTC.
func defaultGuideEntries(name string) []guideEntry {
	now := time.Now().UTC()
	todayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	tomorrowStart := todayStart.Add(24 * time.Hour)
	return []guideEntry{{
		Start: todayStart.Format(timestampFormat),
		End:   tomorrowStart.Format(timestampFormat),
		Title: name,
	}}
}
