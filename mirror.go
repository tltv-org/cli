package main

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// ---------- Mirror Channel State ----------

// mirrorChannel holds per-channel state for the mirror.
// Immutable once placed in mirrorRegistry. Uses copy-on-write replacement.
type mirrorChannel struct {
	ChannelID    string
	Name         string
	Hints        []string // upstream host:port sources
	StreamHint   string   // best upstream for stream proxying
	Token        string   // upstream access token (for fetching)
	ServeToken   string   // token for serving to viewers (--token flag)
	IsPrivate    bool     // access == "token"
	Metadata     []byte   // raw verified metadata bytes (served verbatim in passive mode)
	Guide        []byte   // raw verified guide bytes (served verbatim in passive mode)
	GuideEntries []guideEntry
	IconData     []byte // cached upstream icon
	IconCT       string // icon content type
	IconFileName string // e.g. "icon.svg"
	LastVerified time.Time
	// Promoted mode fields
	PromotedMeta  []byte // self-signed metadata (only in promoted mode)
	PromotedGuide []byte // self-signed guide (only in promoted mode)
}

// mirrorRegistry manages the single mirrored channel.
// Thread-safe via sync.RWMutex with immutable replacement.
type mirrorRegistry struct {
	mu      sync.RWMutex
	channel *mirrorChannel
}

func (r *mirrorRegistry) GetChannel() *mirrorChannel {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.channel
}

func (r *mirrorRegistry) SetChannel(ch *mirrorChannel) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.channel = ch
}

// ---------- Mirror Server ----------

// mirrorServer handles HTTP requests for the mirrored channel.
type mirrorServer struct {
	registry  *mirrorRegistry
	client    *Client
	cache     *hlsCache
	bufMgr    *relayBufferManager
	peerReg   *peerRegistry
	hostname  string
	channelID string
	promoted  *atomic.Bool
	state     *mirrorState
	privKey   ed25519.PrivateKey
	// Fallback stream
	fallbackStream string
	fallbackGuide  string
	localIconData  []byte
	localIconCT    string
	mux            *http.ServeMux
}

func newMirrorServer(registry *mirrorRegistry, client *Client, cache *hlsCache, bufMgr *relayBufferManager, peerReg *peerRegistry, hostname, channelID string, promoted *atomic.Bool, state *mirrorState, privKey ed25519.PrivateKey) *mirrorServer {
	s := &mirrorServer{
		registry:  registry,
		client:    client,
		cache:     cache,
		bufMgr:    bufMgr,
		peerReg:   peerReg,
		hostname:  hostname,
		channelID: channelID,
		promoted:  promoted,
		state:     state,
		privKey:   privKey,
		mux:       http.NewServeMux(),
	}

	s.mux.HandleFunc("GET /.well-known/tltv", s.handleNodeInfo)
	s.mux.HandleFunc("GET /tltv/v1/channels/{id}", s.handleChannelMeta)
	s.mux.HandleFunc("GET /tltv/v1/channels/{id}/{path...}", s.handleChannelPath)
	s.mux.HandleFunc("GET /tltv/v1/peers", s.handlePeers)
	s.mux.HandleFunc("GET /health", s.handleHealth)

	s.mux.HandleFunc("/.well-known/tltv", s.handleMethodNotAllowed)
	s.mux.HandleFunc("/tltv/", s.handleMethodNotAllowed)

	return s
}

func (s *mirrorServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")

	if r.Method == "OPTIONS" {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	s.mux.ServeHTTP(w, r)
}

// handleNodeInfo serves GET /.well-known/tltv
// Mirror lists channel in "channels" (it IS an origin), not "relaying".
func (s *mirrorServer) handleNodeInfo(w http.ResponseWriter, r *http.Request) {
	ch := s.registry.GetChannel()

	var channels []interface{}
	if ch != nil && !ch.IsPrivate {
		channels = append(channels, map[string]interface{}{
			"id":   ch.ChannelID,
			"name": ch.Name,
		})
	}
	if channels == nil {
		channels = []interface{}{}
	}

	w.Header().Set("Cache-Control", "max-age=60")
	writeJSON(w, map[string]interface{}{
		"protocol": "tltv",
		"versions": []int{1},
		"channels": channels,
		"relaying": []interface{}{},
	}, http.StatusOK)
}

// handlePeers serves GET /tltv/v1/peers
func (s *mirrorServer) handlePeers(w http.ResponseWriter, r *http.Request) {
	var external []peerEntry
	if s.peerReg != nil {
		external = append(external, s.peerReg.ListPeers()...)
	}

	peers := buildPeersResponse(nil, external)
	w.Header().Set("Cache-Control", "max-age=300")
	writeJSON(w, map[string]interface{}{
		"peers": peers,
	}, http.StatusOK)
}

// handleChannelMeta serves GET /tltv/v1/channels/{id}
func (s *mirrorServer) handleChannelMeta(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ch := s.registry.GetChannel()
	if ch == nil || id != s.channelID {
		jsonError(w, "channel_not_found", http.StatusNotFound)
		return
	}

	// Token check for private channels
	if ch.IsPrivate && !checkRequestToken(w, r, ch.ServeToken) {
		return
	}
	if ch.IsPrivate {
		setServerPrivateHeaders(w, true)
	}

	// Choose metadata source based on mode
	meta := ch.Metadata
	if s.promoted.Load() && ch.PromotedMeta != nil {
		meta = ch.PromotedMeta
	}
	if meta == nil {
		jsonError(w, "channel_not_found", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "max-age=60")
	w.Write(meta)
}

// handleChannelPath serves GET /tltv/v1/channels/{id}/{path...}
func (s *mirrorServer) handleChannelPath(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	subPath := r.PathValue("path")

	ch := s.registry.GetChannel()
	if ch == nil || id != s.channelID {
		jsonError(w, "channel_not_found", http.StatusNotFound)
		return
	}

	// Token check for private channels
	if ch.IsPrivate && !checkRequestToken(w, r, ch.ServeToken) {
		return
	}
	if ch.IsPrivate {
		setServerPrivateHeaders(w, true)
	}

	switch subPath {
	case "guide.json":
		s.serveGuideJSON(w, ch)
	case "guide.xml":
		s.serveGuideXML(w, ch)
	default:
		// Check for icon path
		if ch.IconFileName != "" && subPath == ch.IconFileName {
			s.serveIcon(w, ch)
			return
		}
		s.serveStream(w, r, ch, subPath)
	}
}

func (s *mirrorServer) handleHealth(w http.ResponseWriter, r *http.Request) {
	status := "passive"
	if s.promoted.Load() {
		status = "promoted"
	}
	writeJSON(w, map[string]interface{}{
		"status":    "ok",
		"mode":      status,
		"channel":   s.channelID,
		"mirroring": 1,
	}, http.StatusOK)
}

func (s *mirrorServer) handleMethodNotAllowed(w http.ResponseWriter, r *http.Request) {
	jsonError(w, "invalid_request", http.StatusBadRequest)
}

// ---------- Guide Serving ----------

func (s *mirrorServer) serveGuideJSON(w http.ResponseWriter, ch *mirrorChannel) {
	guide := ch.Guide
	if s.promoted.Load() && ch.PromotedGuide != nil {
		guide = ch.PromotedGuide
	}
	if guide == nil {
		jsonError(w, "channel_not_found", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "max-age=300")
	w.Write(guide)
}

func (s *mirrorServer) serveGuideXML(w http.ResponseWriter, ch *mirrorChannel) {
	entries := ch.GuideEntries
	if len(entries) == 0 && ch.Guide == nil {
		jsonError(w, "channel_not_found", http.StatusNotFound)
		return
	}
	if len(entries) == 0 {
		entries = defaultGuideEntries(ch.Name)
	}

	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.Header().Set("Cache-Control", "max-age=300")
	w.Write([]byte(guideToXMLTV(ch.ChannelID, ch.Name, entries)))
}

// ---------- Icon Serving ----------

func (s *mirrorServer) serveIcon(w http.ResponseWriter, ch *mirrorChannel) {
	// Promoted mode: prefer local override
	if s.promoted.Load() && s.localIconData != nil {
		w.Header().Set("Content-Type", s.localIconCT)
		w.Header().Set("Cache-Control", "max-age=86400")
		w.Write(s.localIconData)
		return
	}
	// Passive mode: serve cached upstream icon
	if ch.IconData != nil {
		w.Header().Set("Content-Type", ch.IconCT)
		w.Header().Set("Cache-Control", "max-age=86400")
		w.Write(ch.IconData)
		return
	}
	// Default icon
	w.Header().Set("Content-Type", "image/svg+xml")
	w.Header().Set("Cache-Control", "max-age=86400")
	w.Write([]byte(defaultIconSVG))
}

// ---------- Stream Proxying ----------

func (s *mirrorServer) serveStream(w http.ResponseWriter, r *http.Request, ch *mirrorChannel, subPath string) {
	if !validateSubPath(subPath) {
		jsonError(w, "invalid_request", http.StatusBadRequest)
		return
	}

	// Buffer path: same pattern as relay
	if s.bufMgr != nil {
		if subPath == "stream.m3u8" {
			if manifest := s.bufMgr.GetRootManifest(ch.ChannelID); manifest != "" {
				setStreamHeaders(w, subPath, ch.IsPrivate)
				w.Write([]byte(manifest))
				return
			}
		}
		if strings.HasSuffix(subPath, ".m3u8") {
			if manifest := s.bufMgr.GetManifest(ch.ChannelID, subPath); manifest != "" {
				setStreamHeaders(w, subPath, ch.IsPrivate)
				w.Write([]byte(manifest))
				return
			}
		} else if data := s.bufMgr.GetSegmentByPath(ch.ChannelID, subPath); data != nil {
			setStreamHeaders(w, subPath, ch.IsPrivate)
			w.Write(data)
			return
		}
	}

	if ch.StreamHint == "" {
		jsonError(w, "stream_unavailable", http.StatusServiceUnavailable)
		return
	}

	// Upstream proxy
	base := s.client.baseURL(ch.StreamHint)
	manifestURL := base + "/tltv/v1/channels/" + ch.ChannelID + "/stream.m3u8"
	if ch.Token != "" {
		manifestURL += "?token=" + ch.Token
	}

	var fetchURL string
	if subPath == "stream.m3u8" {
		fetchURL = manifestURL
	} else {
		parsed, err := url.Parse(manifestURL)
		if err != nil {
			jsonError(w, "stream_unavailable", http.StatusServiceUnavailable)
			return
		}
		ref, err := url.Parse(subPath)
		if err != nil {
			jsonError(w, "stream_unavailable", http.StatusServiceUnavailable)
			return
		}
		parsed.Path = path.Dir(parsed.Path) + "/"
		fetchURL = parsed.ResolveReference(ref).String()
	}

	// Cache path
	if s.cache != nil {
		cacheKey := r.URL.Path
		data, _, hit, err := s.cache.getOrFetch(cacheKey, func() (*hlsCacheFetchResult, error) {
			fr, err := hlsCacheFetchUpstream(s.client.http, fetchURL, r)
			if err != nil {
				return nil, err
			}
			if strings.HasSuffix(subPath, ".m3u8") {
				rewritten := rewriteManifest(manifestURL, fr.data, "")
				fr.data = rewritten
			}
			return fr, nil
		})
		if err != nil {
			jsonError(w, "stream_unavailable", http.StatusServiceUnavailable)
			return
		}

		setStreamHeaders(w, subPath, ch.IsPrivate)
		if hit {
			w.Header().Set("Cache-Status", "HIT")
		} else {
			w.Header().Set("Cache-Status", "MISS")
		}
		w.Write(data)
		return
	}

	// Direct proxy
	req, err := http.NewRequestWithContext(r.Context(), "GET", fetchURL, nil)
	if err != nil {
		jsonError(w, "stream_unavailable", http.StatusServiceUnavailable)
		return
	}
	req.Header.Set("User-Agent", "tltv-cli/"+version)

	resp, err := s.client.http.Do(req)
	if err != nil {
		jsonError(w, "stream_unavailable", http.StatusServiceUnavailable)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		jsonError(w, "stream_unavailable", http.StatusServiceUnavailable)
		return
	}

	setStreamHeaders(w, subPath, ch.IsPrivate)

	if strings.HasSuffix(subPath, ".m3u8") {
		body, err := io.ReadAll(io.LimitReader(resp.Body, maxManifestSize))
		if err != nil {
			jsonError(w, "stream_unavailable", http.StatusServiceUnavailable)
			return
		}
		rewritten := rewriteManifest(manifestURL, body, "")
		w.Write(rewritten)
		return
	}

	io.Copy(w, resp.Body)
}

// ---------- Mirror Promotion ----------

// mirrorPromote transitions the mirror to active signer mode.
// Signs new metadata preserving upstream fields, with seq > primary's last seq.
func mirrorPromote(registry *mirrorRegistry, state *mirrorState, privKey ed25519.PrivateKey, hostname string, localIconPath string) error {
	ch := registry.GetChannel()
	if ch == nil {
		return fmt.Errorf("no channel in registry")
	}

	// Get new seq from state (guaranteed > any upstream seq seen)
	newSeq := state.promote()

	logInfof("mirror promoting to active signer (seq=%d)", newSeq)

	// Preserve fields from upstream metadata
	now := time.Now().UTC()
	upstreamDoc := make(map[string]interface{})
	if ch.Metadata != nil {
		json.Unmarshal(ch.Metadata, &upstreamDoc)
	}

	// Build new metadata preserving upstream fields
	meta := map[string]interface{}{
		"v":       json.Number("1"),
		"seq":     json.Number(fmt.Sprintf("%d", newSeq)),
		"id":      ch.ChannelID,
		"name":    ch.Name,
		"stream":  "/tltv/v1/channels/" + ch.ChannelID + "/stream.m3u8",
		"updated": now.Format(timestampFormat),
	}

	// Copy optional fields from upstream
	for _, field := range []string{"description", "tags", "language", "timezone", "on_demand", "icon", "origins"} {
		if v, ok := upstreamDoc[field]; ok {
			meta[field] = v
		}
	}

	// Access
	if ch.IsPrivate {
		meta["access"] = "token"
	} else {
		meta["access"] = "public"
	}
	meta["status"] = "active"
	meta["guide"] = "/tltv/v1/channels/" + ch.ChannelID + "/guide.json"

	// Sign
	signed, err := signDocument(meta, privKey)
	if err != nil {
		return fmt.Errorf("signing metadata: %w", err)
	}
	metaBytes, err := json.Marshal(signed)
	if err != nil {
		return fmt.Errorf("marshaling metadata: %w", err)
	}

	// Sign guide: re-sign the last known upstream guide entries
	guideEntries := ch.GuideEntries
	if len(guideEntries) == 0 {
		guideEntries = defaultGuideEntries(ch.Name)
	}
	guideBytes, err := mirrorSignGuide(ch.ChannelID, newSeq, guideEntries, privKey)
	if err != nil {
		return fmt.Errorf("signing guide: %w", err)
	}

	// Update registry with promoted docs
	updated := &mirrorChannel{
		ChannelID:     ch.ChannelID,
		Name:          ch.Name,
		Hints:         ch.Hints,
		StreamHint:    ch.StreamHint,
		Token:         ch.Token,
		ServeToken:    ch.ServeToken,
		IsPrivate:     ch.IsPrivate,
		Metadata:      ch.Metadata,
		Guide:         ch.Guide,
		GuideEntries:  ch.GuideEntries,
		IconData:      ch.IconData,
		IconCT:        ch.IconCT,
		IconFileName:  ch.IconFileName,
		LastVerified:  ch.LastVerified,
		PromotedMeta:  metaBytes,
		PromotedGuide: guideBytes,
	}
	registry.SetChannel(updated)

	return nil
}

// mirrorSignGuide builds and signs a guide document for promoted mode.
func mirrorSignGuide(channelID string, seq int64, entries []guideEntry, privKey ed25519.PrivateKey) ([]byte, error) {
	now := time.Now().UTC()

	if len(entries) == 0 {
		return nil, fmt.Errorf("no guide entries")
	}

	// Compute from/until from entry bounds
	from := entries[0].Start
	until := entries[0].End
	for _, e := range entries[1:] {
		if e.Start < from {
			from = e.Start
		}
		if e.End > until {
			until = e.End
		}
	}

	// Build entries as []interface{}
	var entryList []interface{}
	for _, e := range entries {
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
		entryList = append(entryList, entry)
	}

	doc := map[string]interface{}{
		"v":       json.Number("1"),
		"seq":     json.Number(fmt.Sprintf("%d", seq)),
		"id":      channelID,
		"from":    from,
		"until":   until,
		"entries": entryList,
		"updated": now.Format(timestampFormat),
	}

	signed, err := signDocument(doc, privKey)
	if err != nil {
		return nil, err
	}
	data, err := json.Marshal(signed)
	if err != nil {
		return nil, err
	}
	return data, nil
}

// ---------- Icon Fetching ----------

// mirrorFetchIcon fetches the icon from upstream based on the metadata icon path.
func mirrorFetchIcon(client *Client, hint, channelID, token string, doc map[string]interface{}) (data []byte, contentType string, fileName string) {
	iconPath, _ := doc["icon"].(string)
	if iconPath == "" {
		return []byte(defaultIconSVG), "image/svg+xml", "icon.svg"
	}

	iconURL := client.baseURL(hint) + iconPath
	if token != "" {
		iconURL += "?token=" + token
	}

	body, status, err := client.get(iconURL)
	if err != nil || status != 200 {
		logDebugf("mirror: icon fetch failed: %v (status=%d)", err, status)
		return []byte(defaultIconSVG), "image/svg+xml", "icon.svg"
	}

	// Determine content type from path
	ct := iconContentType(iconPath)
	if ct == "" {
		ct = "image/svg+xml"
	}
	ext := iconExtension(ct)
	return body, ct, "icon." + ext
}

// ---------- Mirror Re-Sign ----------

// mirrorResignPromoted re-signs metadata and guide in promoted mode.
// Called periodically (same as server's resign ticker).
func mirrorResignPromoted(registry *mirrorRegistry, state *mirrorState, privKey ed25519.PrivateKey, hostname string) {
	ch := registry.GetChannel()
	if ch == nil {
		return
	}

	now := time.Now().UTC()
	seq := state.getSeqFloor()
	// Use a fresh seq > floor (same pattern as bridge)
	newSeq := now.Unix()
	if newSeq <= seq {
		newSeq = seq + 1
	}
	state.mu.Lock()
	state.SeqFloor = newSeq
	state.saveLocked()
	state.mu.Unlock()

	// Preserve upstream metadata fields
	upstreamDoc := make(map[string]interface{})
	if ch.Metadata != nil {
		json.Unmarshal(ch.Metadata, &upstreamDoc)
	}

	meta := map[string]interface{}{
		"v":       json.Number("1"),
		"seq":     json.Number(fmt.Sprintf("%d", newSeq)),
		"id":      ch.ChannelID,
		"name":    ch.Name,
		"stream":  "/tltv/v1/channels/" + ch.ChannelID + "/stream.m3u8",
		"updated": now.Format(timestampFormat),
	}

	for _, field := range []string{"description", "tags", "language", "timezone", "on_demand", "icon", "origins"} {
		if v, ok := upstreamDoc[field]; ok {
			meta[field] = v
		}
	}

	if ch.IsPrivate {
		meta["access"] = "token"
	} else {
		meta["access"] = "public"
	}
	meta["status"] = "active"
	meta["guide"] = "/tltv/v1/channels/" + ch.ChannelID + "/guide.json"

	signed, err := signDocument(meta, privKey)
	if err != nil {
		logErrorf("mirror resign metadata: %v", err)
		return
	}
	metaBytes, err := json.Marshal(signed)
	if err != nil {
		logErrorf("mirror resign metadata marshal: %v", err)
		return
	}

	// Re-sign guide
	guideEntries := ch.GuideEntries
	if len(guideEntries) == 0 {
		guideEntries = defaultGuideEntries(ch.Name)
	}
	guideBytes, err := mirrorSignGuide(ch.ChannelID, newSeq, guideEntries, privKey)
	if err != nil {
		logErrorf("mirror resign guide: %v", err)
		return
	}

	// Update registry
	updated := &mirrorChannel{
		ChannelID:     ch.ChannelID,
		Name:          ch.Name,
		Hints:         ch.Hints,
		StreamHint:    ch.StreamHint,
		Token:         ch.Token,
		ServeToken:    ch.ServeToken,
		IsPrivate:     ch.IsPrivate,
		Metadata:      ch.Metadata,
		Guide:         ch.Guide,
		GuideEntries:  ch.GuideEntries,
		IconData:      ch.IconData,
		IconCT:        ch.IconCT,
		IconFileName:  ch.IconFileName,
		LastVerified:  ch.LastVerified,
		PromotedMeta:  metaBytes,
		PromotedGuide: guideBytes,
	}
	registry.SetChannel(updated)
}

// ---------- Viewer Info Helper ----------

// mirrorViewerBuildInfo builds the /api/info response for the embedded viewer.
func mirrorViewerBuildInfo(registry *mirrorRegistry, channelID string) func(reqChannelID string) map[string]interface{} {
	return func(reqChannelID string) map[string]interface{} {
		ch := registry.GetChannel()
		if ch == nil {
			return map[string]interface{}{
				"channels": []interface{}{},
			}
		}

		meta := ch.Metadata
		if ch.PromotedMeta != nil {
			meta = ch.PromotedMeta
		}

		info := map[string]interface{}{
			"channel_id": ch.ChannelID,
			"stream_src": "/tltv/v1/channels/" + ch.ChannelID + "/stream.m3u8",
		}

		if meta != nil {
			var doc map[string]interface{}
			json.Unmarshal(meta, &doc)
			info["metadata"] = doc
		}

		channels := []interface{}{
			map[string]interface{}{
				"id":   ch.ChannelID,
				"name": ch.Name,
			},
		}
		info["channels"] = channels

		return info
	}
}

// ---------- Command ----------

func cmdMirror(args []string) {
	fs := flag.NewFlagSet("mirror", flag.ExitOnError)

	// Required flags
	sourceStr := fs.String("source", os.Getenv("SOURCE"), "tltv:// URI of the primary origin (required)")
	fs.StringVar(sourceStr, "s", os.Getenv("SOURCE"), "alias for --source")
	keyPath := fs.String("key", os.Getenv("KEY"), "channel key file (required, same key as primary)")
	fs.StringVar(keyPath, "k", os.Getenv("KEY"), "alias for --key")

	// Server flags
	defaultListen := ":8000"
	if v := os.Getenv("LISTEN"); v != "" {
		defaultListen = v
	}
	listenAddr := fs.String("listen", defaultListen, "listen address")
	fs.StringVar(listenAddr, "l", defaultListen, "alias for --listen")

	hostnameArg := fs.String("hostname", os.Getenv("HOSTNAME"), "public hostname (required, must be in primary's origins)")
	fs.StringVar(hostnameArg, "H", os.Getenv("HOSTNAME"), "alias for --hostname")

	// Token flags
	tokenStr := fs.String("token", os.Getenv("TOKEN"), "access token (required when upstream is private)")
	fs.StringVar(tokenStr, "t", os.Getenv("TOKEN"), "alias for --token")

	// Peers
	peersStr := addPeersFlag(fs)
	gossipEnabled := addGossipFlag(fs)
	proxyStr := addProxyFlag(fs)

	// Buffer flags
	bufferStr := fs.String("buffer", os.Getenv("BUFFER"), "proactive buffer duration (e.g. 2h, 30m)")
	fs.StringVar(bufferStr, "b", os.Getenv("BUFFER"), "alias for --buffer")
	bufferMaxMemStr := fs.String("buffer-max-memory", os.Getenv("BUFFER_MAX_MEMORY"), "max total buffer memory (default: 1g)")
	fs.StringVar(bufferMaxMemStr, "B", os.Getenv("BUFFER_MAX_MEMORY"), "alias for --buffer-max-memory")

	// Fallback
	fallbackStreamStr := fs.String("fallback-stream", os.Getenv("FALLBACK_STREAM"), "local HLS source after buffer drains")
	fallbackGuideStr := fs.String("fallback-guide", os.Getenv("FALLBACK_GUIDE"), "guide file for fallback content")

	// Icon
	iconPathFlag := fs.String("icon", os.Getenv("ICON"), "local icon override for promoted mode")

	// Promotion
	defaultPromoteAfter := 3
	if v := os.Getenv("PROMOTE_AFTER"); v != "" {
		fmt.Sscanf(v, "%d", &defaultPromoteAfter)
	}
	promoteAfter := fs.Int("promote-after", defaultPromoteAfter, "consecutive failures before promotion")

	// State file
	defaultStatePath := "mirror-state.json"
	if v := os.Getenv("STATE_FILE"); v != "" {
		defaultStatePath = v
	}
	stateFile := fs.String("state-file", defaultStatePath, "persisted state file path")

	// Config
	configPath := fs.String("config", os.Getenv("CONFIG"), "config file (JSON)")
	fs.StringVar(configPath, "f", os.Getenv("CONFIG"), "alias for --config")
	dumpConfig := fs.Bool("dump-config", false, "print resolved config as JSON and exit")
	fs.BoolVar(dumpConfig, "D", false, "alias for --dump-config")

	// Cache
	cacheEnabled, cacheMaxEntries, cacheStatsInterval := addCacheFlags(fs)

	// TLS
	tlsEnabled, tlsCert, tlsKey, acmeEmail, tlsStaging := addTLSFlags(fs)

	// Viewer (parsed manually before fs.Parse)
	var viewer viewerConfig

	// Tuning
	defaultMetaPoll := "60s"
	if v := os.Getenv("META_POLL"); v != "" {
		defaultMetaPoll = v
	}
	metaPollStr := fs.String("meta-poll", defaultMetaPoll, "metadata poll interval")
	fs.StringVar(metaPollStr, "m", defaultMetaPoll, "alias for --meta-poll")

	defaultGuidePoll := "15m"
	if v := os.Getenv("GUIDE_POLL"); v != "" {
		defaultGuidePoll = v
	}
	guidePollStr := fs.String("guide-poll", defaultGuidePoll, "guide poll interval")
	fs.StringVar(guidePollStr, "G", defaultGuidePoll, "alias for --guide-poll")

	// Logging
	logLvl, logFmt, logPath := addLogFlags(fs)

	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Mirror a TLTV channel (same key, origin-level replication)\n\n")
		fmt.Fprintf(os.Stderr, "Usage: tltv mirror [flags]\n\n")
		fmt.Fprintf(os.Stderr, "Replicates a channel from a primary origin. The mirror holds the same\n")
		fmt.Fprintf(os.Stderr, "private key and is listed in the primary's signed origins. In passive\n")
		fmt.Fprintf(os.Stderr, "mode, serves the primary's signed documents verbatim. On primary\n")
		fmt.Fprintf(os.Stderr, "failure, promotes to active signer with seq continuity.\n\n")
		fmt.Fprintf(os.Stderr, "Required:\n")
		fmt.Fprintf(os.Stderr, "  -s, --source URI         tltv:// URI of the primary origin\n")
		fmt.Fprintf(os.Stderr, "  -k, --key FILE           channel key file (same key as primary)\n")
		fmt.Fprintf(os.Stderr, "  -H, --hostname HOST      public hostname (must be in primary's origins)\n\n")
		fmt.Fprintf(os.Stderr, "Server:\n")
		fmt.Fprintf(os.Stderr, "  -l, --listen ADDR        listen address (default: :8000, :443 with --tls)\n")
		fmt.Fprintf(os.Stderr, "  -t, --token STRING       access token (required for private channels)\n\n")
		fmt.Fprintf(os.Stderr, "Buffer:\n")
		fmt.Fprintf(os.Stderr, "  -b, --buffer DUR         proactive buffer duration (e.g. 2h)\n")
		fmt.Fprintf(os.Stderr, "  -B, --buffer-max-memory  max total buffer memory (default: 1g)\n\n")
		fmt.Fprintf(os.Stderr, "Failover:\n")
		fmt.Fprintf(os.Stderr, "      --fallback-stream    local HLS source after buffer drains\n")
		fmt.Fprintf(os.Stderr, "      --fallback-guide     guide file for fallback content\n")
		fmt.Fprintf(os.Stderr, "      --promote-after N    consecutive failures before promotion (default: 3)\n")
		fmt.Fprintf(os.Stderr, "      --state-file PATH    persisted state file (default: mirror-state.json)\n")
		fmt.Fprintf(os.Stderr, "      --icon PATH          local icon override for promoted mode\n\n")
		fmt.Fprintf(os.Stderr, "Peers:\n")
		fmt.Fprintf(os.Stderr, "  -P, --peers LIST         tltv:// URIs to advertise in peer exchange\n")
		fmt.Fprintf(os.Stderr, "  -g, --gossip             re-advertise validated gossip-discovered channels\n")
		fmt.Fprintf(os.Stderr, "  -x, --proxy URL          proxy URL (socks5://, http://, https://)\n\n")
		fmt.Fprintf(os.Stderr, "Config:\n")
		fmt.Fprintf(os.Stderr, "  -f, --config PATH        config file (JSON)\n")
		fmt.Fprintf(os.Stderr, "  -D, --dump-config        print resolved config as JSON and exit\n\n")
		fmt.Fprintf(os.Stderr, "Cache:\n")
		fmt.Fprintf(os.Stderr, "      --cache              enable in-memory HLS stream cache\n")
		fmt.Fprintf(os.Stderr, "      --cache-max-entries  max cached items (default: 100)\n")
		fmt.Fprintf(os.Stderr, "      --cache-stats N      log cache stats every N seconds (0 = off)\n\n")
		fmt.Fprintf(os.Stderr, "TLS:\n")
		fmt.Fprintf(os.Stderr, "      --tls                enable TLS (autocert via Let's Encrypt)\n")
		fmt.Fprintf(os.Stderr, "      --tls-cert FILE      TLS certificate file (PEM)\n")
		fmt.Fprintf(os.Stderr, "      --tls-key FILE       TLS private key file (PEM)\n")
		fmt.Fprintf(os.Stderr, "      --tls-staging        use Let's Encrypt staging\n")
		fmt.Fprintf(os.Stderr, "      --acme-email EMAIL   email for ACME account\n\n")
		fmt.Fprintf(os.Stderr, "Viewer:\n")
		fmt.Fprintf(os.Stderr, "      --viewer             serve built-in web player at /\n\n")
		fmt.Fprintf(os.Stderr, "Tuning:\n")
		fmt.Fprintf(os.Stderr, "  -m, --meta-poll DUR      metadata poll interval (default: 60s)\n")
		fmt.Fprintf(os.Stderr, "  -G, --guide-poll DUR     guide poll interval (default: 15m)\n\n")
		fmt.Fprintf(os.Stderr, "Logging:\n")
		fmt.Fprintf(os.Stderr, "      --log-level LEVEL    log level: debug, info, error (default: info)\n")
		fmt.Fprintf(os.Stderr, "      --log-format FORMAT  log format: human, json (default: human)\n")
		fmt.Fprintf(os.Stderr, "      --log-file PATH      log to file instead of stderr\n\n")
		fmt.Fprintf(os.Stderr, "Environment variables: SOURCE, KEY, HOSTNAME, LISTEN, TOKEN,\n")
		fmt.Fprintf(os.Stderr, "BUFFER, BUFFER_MAX_MEMORY, FALLBACK_STREAM, FALLBACK_GUIDE,\n")
		fmt.Fprintf(os.Stderr, "PROMOTE_AFTER, STATE_FILE, ICON, PEERS, GOSSIP=1, PROXY,\n")
		fmt.Fprintf(os.Stderr, "CONFIG, TLS=1, TLS_CERT, TLS_KEY, TLS_STAGING=1, TLS_DIR,\n")
		fmt.Fprintf(os.Stderr, "ACME_EMAIL, CACHE=1, CACHE_MAX_ENTRIES, CACHE_STATS, VIEWER=1,\n")
		fmt.Fprintf(os.Stderr, "META_POLL, GUIDE_POLL, LOG_LEVEL, LOG_FORMAT, LOG_FILE.\n\n")
		fmt.Fprintf(os.Stderr, "Examples:\n")
		fmt.Fprintf(os.Stderr, "  tltv mirror --source \"tltv://TVabc@primary.tv\" --key channel.key --hostname mirror.tv\n")
		fmt.Fprintf(os.Stderr, "  tltv mirror --source \"tltv://TVabc@primary.tv\" --key channel.key --hostname mirror.tv --buffer 2h --tls\n")
	}
	args, viewer = parseViewerArg(args)
	fs.Parse(args)

	// Override default listen port for TLS
	if *tlsEnabled || *tlsCert != "" {
		tlsOverrideListenPort(fs, listenAddr)
	}

	// Load config
	if *configPath != "" {
		cfg, err := loadDaemonConfig(*configPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: loading config: %v\n", err)
			os.Exit(1)
		}
		applyConfigToFlags(fs, cfg)
	}

	// Dump config
	if *dumpConfig {
		out := make(map[string]interface{})
		if *sourceStr != "" {
			out["source"] = *sourceStr
		}
		if *keyPath != "" {
			out["key"] = *keyPath
		}
		if *hostnameArg != "" {
			out["hostname"] = *hostnameArg
		}
		if *listenAddr != defaultListen {
			out["listen"] = *listenAddr
		}
		if *tokenStr != "" {
			out["token"] = *tokenStr
		}
		if *bufferStr != "" {
			out["buffer"] = *bufferStr
		}
		if *fallbackStreamStr != "" {
			out["fallback_stream"] = *fallbackStreamStr
		}
		if *fallbackGuideStr != "" {
			out["fallback_guide"] = *fallbackGuideStr
		}
		if *promoteAfter != 3 {
			out["promote_after"] = *promoteAfter
		}
		if *cacheEnabled {
			out["cache"] = true
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.SetEscapeHTML(false)
		enc.Encode(out)
		return
	}

	// Set up logging
	if err := setupLogging(*logLvl, *logFmt, *logPath, "mirror"); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	// Validate required flags
	if *sourceStr == "" {
		fmt.Fprintf(os.Stderr, "error: --source is required\n")
		os.Exit(1)
	}
	if *keyPath == "" {
		fmt.Fprintf(os.Stderr, "error: --key is required\n")
		os.Exit(1)
	}
	if *hostnameArg == "" {
		fmt.Fprintf(os.Stderr, "error: --hostname is required\n")
		os.Exit(1)
	}

	// Validate token format
	if *tokenStr != "" {
		if err := validateToken(*tokenStr); err != nil {
			fmt.Fprintf(os.Stderr, "error: invalid token: %v\n", err)
			os.Exit(1)
		}
	}

	// Parse source URI
	uri, err := parseTLTVUri(*sourceStr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: parsing source URI: %v\n", err)
		os.Exit(1)
	}
	if len(uri.Hints) == 0 {
		fmt.Fprintf(os.Stderr, "error: source URI has no host hint\n")
		os.Exit(1)
	}
	sourceChannelID := uri.ChannelID
	sourceHints := uri.Hints
	sourceToken := uri.Token

	// Load key and derive channel ID
	seed, err := readSeed(*keyPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: reading key: %v\n", err)
		os.Exit(1)
	}
	privKey, pubKey := keyFromSeed(seed)
	channelID := makeChannelID(pubKey)

	// Step 3: Key/source ID match
	if channelID != sourceChannelID {
		fmt.Fprintf(os.Stderr, "error: key does not match source channel ID\n")
		fmt.Fprintf(os.Stderr, "  key derives:  %s\n", channelID)
		fmt.Fprintf(os.Stderr, "  source URI:   %s\n", sourceChannelID)
		os.Exit(1)
	}

	logInfof("channel ID: %s", channelID)
	logInfof("source: %s", *sourceStr)

	// Set up HTTP client (with proxy if configured)
	var client *Client
	if *proxyStr != "" {
		proxyURL, err := parseProxyURL(*proxyStr)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: invalid proxy URL: %v\n", err)
			os.Exit(1)
		}
		client = newClientWithProxy(flagInsecure, proxyURL)
	} else {
		client = newClient(flagInsecure)
	}

	// Step 4: Fetch and verify source metadata
	result, err := fetchAndVerifyMetadata(client, channelID, sourceHints)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: cannot verify source: %v\n", err)
		os.Exit(1)
	}
	if result.IsMigration {
		fmt.Fprintf(os.Stderr, "error: source channel has migrated to %s\n", result.MigratedTo)
		os.Exit(1)
	}

	// Step 5: Verify hostname is in source's signed origins
	origins := extractOrigins(result.Doc)
	if !hostnameMatchesOrigin(*hostnameArg, origins) {
		fmt.Fprintf(os.Stderr, "error: hostname %s not listed in source origins\n", *hostnameArg)
		fmt.Fprintf(os.Stderr, "  signed origins: %v\n", origins)
		fmt.Fprintf(os.Stderr, "  add this hostname to the primary's metadata first\n")
		os.Exit(1)
	}

	// Step 6: Check access — private upstream requires --token
	access, _ := result.Doc["access"].(string)
	isPrivate := access == "token"
	if isPrivate && *tokenStr == "" {
		fmt.Fprintf(os.Stderr, "error: upstream is a private channel — --token is required\n")
		os.Exit(1)
	}

	// Extract upstream metadata fields
	upstreamName, _ := result.Doc["name"].(string)
	if upstreamName == "" {
		upstreamName = channelID
	}

	// Step 7: Load persisted state
	state := loadMirrorState(*stateFile, channelID)

	// Track upstream seq
	if seqNum, ok := result.Doc["seq"].(json.Number); ok {
		if v, err := seqNum.Int64(); err == nil && v > state.SeqFloor {
			state.SeqFloor = v
		}
	}

	// Step 8: Warnings
	if *bufferStr == "" && *fallbackStreamStr == "" {
		logInfof("warning: mirror has no --buffer or --fallback-stream — promotion will immediately 503 the stream")
	}

	// Fetch upstream icon
	iconData, iconCT, iconFileName := mirrorFetchIcon(client, result.Hint, channelID, sourceToken, result.Doc)

	// Fetch upstream guide
	guideRaw, guideEntries, _ := relayFetchAndVerifyGuide(client, channelID, sourceHints)

	// Build initial channel state
	registry := &mirrorRegistry{}
	mirrorCh := &mirrorChannel{
		ChannelID:    channelID,
		Name:         upstreamName,
		Hints:        sourceHints,
		StreamHint:   result.Hint,
		Token:        sourceToken,
		ServeToken:   *tokenStr,
		IsPrivate:    isPrivate,
		Metadata:     result.Raw,
		Guide:        guideRaw,
		GuideEntries: guideEntries,
		IconData:     iconData,
		IconCT:       iconCT,
		IconFileName: iconFileName,
		LastVerified: time.Now(),
	}
	registry.SetChannel(mirrorCh)

	// If state says we were promoted on last run, re-promote
	promoted := &atomic.Bool{}
	if state.isPromoted() {
		promoted.Store(true)
		logInfof("resuming in promoted mode (from state file)")
		if err := mirrorPromote(registry, state, privKey, *hostnameArg, *iconPathFlag); err != nil {
			logErrorf("failed to re-sign on startup: %v", err)
		}
	}

	state.updateVerified(state.SeqFloor)

	// Parse poll intervals
	metaPoll, err := time.ParseDuration(*metaPollStr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: invalid meta-poll duration: %v\n", err)
		os.Exit(1)
	}
	guidePoll, err := time.ParseDuration(*guidePollStr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: invalid guide-poll duration: %v\n", err)
		os.Exit(1)
	}

	// Set up cache
	var cache *hlsCache
	if *cacheEnabled {
		cache = newHLSCache(*cacheMaxEntries)
	}

	// Set up buffer
	var bufMgr *relayBufferManager
	if *bufferStr != "" {
		bufDur, err := time.ParseDuration(*bufferStr)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: invalid buffer duration: %v\n", err)
			os.Exit(1)
		}
		maxMem, memErr := parseMemorySize(*bufferMaxMemStr)
		if memErr != nil || maxMem <= 0 {
			maxMem = 1 << 30 // default 1 GB
		}
		segDuration := 2 * time.Second
		maxSegs := int(bufDur / segDuration)
		if maxSegs < 10 {
			maxSegs = 10
		}
		bufMgr = newRelayBufferManager(maxMem, 0) // delay=0 for mirror
		bufMgr.AddBuffer(channelID, maxSegs)
	}

	// Set up peers
	var peerReg *peerRegistry
	if *peersStr != "" {
		_, peerErr := parsePeerTargets(*peersStr)
		if peerErr != nil {
			fmt.Fprintf(os.Stderr, "error: invalid peers: %v\n", peerErr)
			os.Exit(1)
		}
		peerReg = newPeerRegistry()
	}

	// Build HTTP server
	srv := newMirrorServer(registry, client, cache, bufMgr, peerReg, *hostnameArg, channelID, promoted, state, privKey)
	srv.fallbackStream = *fallbackStreamStr
	srv.fallbackGuide = *fallbackGuideStr

	// Load local icon override for promoted mode
	if *iconPathFlag != "" {
		data, ct := loadIcon(*iconPathFlag)
		srv.localIconData = data
		srv.localIconCT = ct
	}

	// Viewer or status page
	if viewer.enabled {
		infoFn := mirrorViewerBuildInfo(registry, channelID)
		channelsFn := func() []ChannelRef {
			ch := registry.GetChannel()
			if ch == nil {
				return nil
			}
			return []ChannelRef{{ID: ch.ChannelID, Name: ch.Name}}
		}
		if isPrivate {
			viewerEmbedRoutes(srv.mux, infoFn, channelsFn, viewerRouteOptions{authToken: *tokenStr, private: true})
		} else {
			viewerEmbedRoutes(srv.mux, infoFn, channelsFn)
		}
	} else {
		statusPageRoutes(srv.mux, func() *NodeInfo {
			ch := registry.GetChannel()
			if ch == nil {
				return &NodeInfo{Versions: []int{1}}
			}
			mode := "passive"
			if promoted.Load() {
				mode = "promoted"
			}
			channels := []ChannelRef{{ID: ch.ChannelID, Name: ch.Name + " (" + mode + ")"}}
			return &NodeInfo{
				Protocol: "tltv",
				Versions: []int{1},
				Channels: channels,
			}
		})
	}

	// TLS setup
	tlsCfg, tlsCleanup, tlsErr := tlsSetup(*hostnameArg, *tlsEnabled, *tlsCert, *tlsKey, *acmeEmail, *tlsStaging)
	if tlsErr != nil {
		fmt.Fprintf(os.Stderr, "error: TLS setup: %v\n", tlsErr)
		os.Exit(1)
	}
	if tlsCleanup != nil {
		defer tlsCleanup()
	}

	// Start HTTP server
	httpSrv := &http.Server{
		Addr:              *listenAddr,
		Handler:           srv,
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	ln, err := net.Listen("tcp", *listenAddr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: listen: %v\n", err)
		os.Exit(1)
	}

	displayAddr := displayListenAddr(ln.Addr().String())
	logInfof("mirror listening on %s (channel %s)", displayAddr, channelID)
	if promoted.Load() {
		logInfof("mode: promoted (active signer)")
	} else {
		logInfof("mode: passive (re-serving primary)")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start server in background
	go func() {
		var srvErr error
		if tlsCfg != nil {
			httpSrv.TLSConfig = tlsCfg
			srvErr = httpSrv.ServeTLS(ln, "", "")
		} else {
			srvErr = httpSrv.Serve(ln)
		}
		if srvErr != nil && srvErr != http.ErrServerClosed {
			logErrorf("server error: %v", srvErr)
		}
	}()

	// Start cache goroutines
	if cache != nil {
		done := make(chan struct{})
		go func() {
			<-ctx.Done()
			close(done)
		}()
		startCacheGoroutines(cache, *cacheStatsInterval, done)
	}

	// Start buffer fetch loop
	if bufMgr != nil {
		bufMgr.StartBuffering(ctx, channelID, nil, client.http)
	}

	// Start metadata poll loop
	consecutiveFailures := 0
	go func() {
		ticker := time.NewTicker(metaPoll)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if promoted.Load() {
					mirrorResignPromoted(registry, state, privKey, *hostnameArg)
					continue
				}

				res, err := fetchAndVerifyMetadata(client, channelID, sourceHints)
				if err != nil {
					consecutiveFailures++
					logErrorf("metadata poll failed (%d/%d): %v", consecutiveFailures, *promoteAfter, err)

					if consecutiveFailures >= *promoteAfter && !promoted.Load() {
						logInfof("primary unreachable after %d consecutive failures — promoting", consecutiveFailures)
						promoted.Store(true)
						if promErr := mirrorPromote(registry, state, privKey, *hostnameArg, *iconPathFlag); promErr != nil {
							logErrorf("promotion failed: %v", promErr)
						}
					}
					continue
				}

				consecutiveFailures = 0

				if res.IsMigration {
					logErrorf("source channel has migrated to %s", res.MigratedTo)
					continue
				}

				if seqNum, ok := res.Doc["seq"].(json.Number); ok {
					if v, seqErr := seqNum.Int64(); seqErr == nil {
						state.updateVerified(v)
					}
				}

				newOrigins := extractOrigins(res.Doc)
				if len(newOrigins) > 0 && !hostnameMatchesOrigin(*hostnameArg, newOrigins) {
					logErrorf("warning: mirror hostname %s no longer in upstream origins %v", *hostnameArg, newOrigins)
				}

				newIconData, newIconCT, newIconFileName := mirrorFetchIcon(client, res.Hint, channelID, sourceToken, res.Doc)

				old := registry.GetChannel()
				updated := &mirrorChannel{
					ChannelID:    channelID,
					Name:         upstreamName,
					Hints:        sourceHints,
					StreamHint:   res.Hint,
					Token:        sourceToken,
					ServeToken:   *tokenStr,
					IsPrivate:    isPrivate,
					Metadata:     res.Raw,
					Guide:        old.Guide,
					GuideEntries: old.GuideEntries,
					IconData:     newIconData,
					IconCT:       newIconCT,
					IconFileName: newIconFileName,
					LastVerified: time.Now(),
				}
				registry.SetChannel(updated)
			}
		}
	}()

	// Start guide poll loop
	go func() {
		ticker := time.NewTicker(guidePoll)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if promoted.Load() {
					continue
				}
				raw, entries, guideErr := relayFetchAndVerifyGuide(client, channelID, sourceHints)
				if guideErr != nil {
					logErrorf("guide poll failed: %v", guideErr)
					continue
				}
				if raw == nil {
					continue
				}

				old := registry.GetChannel()
				updated := &mirrorChannel{
					ChannelID:    old.ChannelID,
					Name:         old.Name,
					Hints:        old.Hints,
					StreamHint:   old.StreamHint,
					Token:        old.Token,
					ServeToken:   old.ServeToken,
					IsPrivate:    old.IsPrivate,
					Metadata:     old.Metadata,
					Guide:        raw,
					GuideEntries: entries,
					IconData:     old.IconData,
					IconCT:       old.IconCT,
					IconFileName: old.IconFileName,
					LastVerified: old.LastVerified,
				}
				registry.SetChannel(updated)
			}
		}
	}()

	// Gossip + peer polling
	if *gossipEnabled || *peersStr != "" {
		peerTargets, _ := parsePeerTargets(*peersStr)
		gossipNodes := gossipNodesFromPeers(peerTargets)
		if len(gossipNodes) > 0 && *gossipEnabled {
			go gossipPollLoop(ctx, client, gossipNodes, func(id, name string, hints []string) {
				if peerReg != nil {
					peerReg.Update(id, name, hints)
				}
			}, 30*time.Minute)
		}
		if peerReg != nil && len(peerTargets) > 0 {
			go peerPollLoop(ctx, client, peerTargets, peerReg, 30*time.Minute)
		}
	}

	// Wait for shutdown signal
	sigCh := make(chan os.Signal, 1)
	signalNotify(sigCh)
	<-sigCh

	logInfof("shutting down mirror...")
	cancel()

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	httpSrv.Shutdown(shutdownCtx)

	state.save()
	logInfof("mirror stopped")
}
