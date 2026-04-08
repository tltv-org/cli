package main

import (
	"encoding/json"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"
)

// ---------- Server ----------

// bridgeServer implements the TLTV protocol HTTP endpoints for bridged channels.
type bridgeServer struct {
	registry  *bridgeRegistry
	cache     *hlsCache        // optional response cache (nil = disabled)
	peerReg   *peerRegistry    // optional external peers (nil = no --peers)
	gossipReg *peerRegistry    // optional gossip-discovered peers (nil = no --gossip)
	mux       *http.ServeMux
}

// newBridgeServer creates a bridge HTTP server with all protocol endpoints registered.
// Pass cache/peerReg/gossipReg=nil to disable the corresponding feature.
func newBridgeServer(registry *bridgeRegistry, cache *hlsCache, peerReg *peerRegistry, gossipReg *peerRegistry) *bridgeServer {
	s := &bridgeServer{
		registry:  registry,
		cache:     cache,
		peerReg:   peerReg,
		gossipReg: gossipReg,
		mux:       http.NewServeMux(),
	}

	// GET handlers for protocol endpoints
	s.mux.HandleFunc("GET /.well-known/tltv", s.handleNodeInfo)
	s.mux.HandleFunc("GET /tltv/v1/channels/{id}", s.handleChannelMeta)
	s.mux.HandleFunc("GET /tltv/v1/channels/{id}/{path...}", s.handleChannelPath)
	s.mux.HandleFunc("GET /tltv/v1/peers", s.handlePeers)
	s.mux.HandleFunc("GET /health", s.handleHealth)

	// Catch-all for non-GET methods on protocol endpoints
	s.mux.HandleFunc("/.well-known/tltv", s.handleMethodNotAllowed)
	s.mux.HandleFunc("/tltv/", s.handleMethodNotAllowed)

	return s
}

// ServeHTTP adds CORS headers to every response and handles OPTIONS preflight.
func (s *bridgeServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")

	if r.Method == "OPTIONS" {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	s.mux.ServeHTTP(w, r)
}

// ---------- Handlers ----------

// handleNodeInfo serves GET /.well-known/tltv
func (s *bridgeServer) handleNodeInfo(w http.ResponseWriter, r *http.Request) {
	channels := s.registry.ListChannels()

	type channelEntry struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}

	var list []channelEntry
	for _, ch := range channels {
		if !ch.IsPrivate() {
			list = append(list, channelEntry{ID: ch.ChannelID, Name: ch.Name})
		}
	}
	if list == nil {
		list = []channelEntry{}
	}

	w.Header().Set("Cache-Control", "max-age=60")
	bridgeWriteJSON(w, map[string]interface{}{
		"protocol": "tltv",
		"versions": []int{1},
		"channels": list,
		"relaying": []interface{}{},
	}, http.StatusOK)
}

// handlePeers serves GET /tltv/v1/peers
// Returns originated public channels + verified external peers from --peers.
func (s *bridgeServer) handlePeers(w http.ResponseWriter, r *http.Request) {
	// Own originated channels (public only, with hostname as hint)
	var own []peerEntry
	hostname := s.registry.hostname
	if hostname != "" {
		now := time.Now()
		for _, ch := range s.registry.ListChannels() {
			if ch.IsPrivate() {
				continue
			}
			own = append(own, peerEntry{
				ChannelID: ch.ChannelID,
				Name:      ch.Name,
				Hints:     []string{hostname},
				LastSeen:  now,
			})
		}
	}

	// External verified peers + gossip-discovered peers
	var external []peerEntry
	if s.peerReg != nil {
		external = s.peerReg.ListPeers()
	}
	if s.gossipReg != nil {
		external = append(external, s.gossipReg.ListPeers()...)
	}

	peers := buildPeersResponse(own, external)
	w.Header().Set("Cache-Control", "max-age=300")
	bridgeWriteJSON(w, map[string]interface{}{
		"peers": peers,
	}, http.StatusOK)
}

// handleChannelMeta serves GET /tltv/v1/channels/{id}
func (s *bridgeServer) handleChannelMeta(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ch := s.registry.GetChannel(id)
	if ch == nil {
		bridgeJSONError(w, "channel_not_found", http.StatusNotFound)
		return
	}

	if !bridgeCheckToken(w, r, ch) {
		return
	}

	if ch.metadata == nil {
		bridgeJSONError(w, "channel_not_found", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "max-age=60")
	bridgeSetPrivateHeaders(w, ch)
	w.Write(ch.metadata)
}

// handleChannelPath serves GET /tltv/v1/channels/{id}/{path...}
func (s *bridgeServer) handleChannelPath(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	subPath := r.PathValue("path")

	ch := s.registry.GetChannel(id)
	if ch == nil {
		bridgeJSONError(w, "channel_not_found", http.StatusNotFound)
		return
	}

	if !bridgeCheckToken(w, r, ch) {
		return
	}

	// Get token for stream rewriting
	token := ""
	if ch.IsPrivate() {
		token = r.URL.Query().Get("token")
	}

	switch subPath {
	case "guide.json":
		s.serveGuideJSON(w, r, ch)
	case "guide.xml":
		s.serveGuideXML(w, r, ch)
	default:
		// stream.m3u8 and all sub-paths (segments, sub-manifests)
		// Cache upstream HTTP streams (not local file streams)
		isUpstream := strings.HasPrefix(ch.StreamURL, "http://") || strings.HasPrefix(ch.StreamURL, "https://")
		if s.cache != nil && isUpstream {
			s.serveCachedStream(w, r, ch, subPath, token)
			return
		}
		bridgeServeStream(w, r, ch, subPath, token)
	}
}

// handleHealth serves GET /health
func (s *bridgeServer) handleHealth(w http.ResponseWriter, r *http.Request) {
	bridgeWriteJSON(w, map[string]interface{}{
		"status":   "ok",
		"channels": s.registry.PublicChannelCount(),
	}, http.StatusOK)
}

// handleMethodNotAllowed returns 400 for non-GET methods on protocol endpoints.
func (s *bridgeServer) handleMethodNotAllowed(w http.ResponseWriter, r *http.Request) {
	bridgeJSONError(w, "invalid_request", http.StatusBadRequest)
}

// ---------- Guide Serving ----------

// serveGuideJSON serves the signed guide JSON document.
func (s *bridgeServer) serveGuideJSON(w http.ResponseWriter, r *http.Request, ch *bridgeRegisteredChannel) {
	if ch.guideDoc == nil {
		bridgeJSONError(w, "channel_not_found", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "max-age=300")
	bridgeSetPrivateHeaders(w, ch)
	w.Write(ch.guideDoc)
}

// serveGuideXML generates and serves an XMLTV guide document.
func (s *bridgeServer) serveGuideXML(w http.ResponseWriter, r *http.Request, ch *bridgeRegisteredChannel) {
	if ch.guideDoc == nil {
		bridgeJSONError(w, "channel_not_found", http.StatusNotFound)
		return
	}

	entries := ch.Guide
	if len(entries) == 0 {
		entries = bridgeDefaultGuideEntries(ch.Name)
	}

	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.Header().Set("Cache-Control", "max-age=300")
	bridgeSetPrivateHeaders(w, ch)
	w.Write([]byte(bridgeGuideToXMLTV(ch.ChannelID, ch.Name, entries)))
}

// ---------- Cached Stream Serving ----------

// serveCachedStream serves upstream HLS content through the cache.
// Same pattern as relay_server.go's serveStream — singleflight dedup,
// protocol-compliant TTLs, manifests rewritten before caching.
func (s *bridgeServer) serveCachedStream(w http.ResponseWriter, r *http.Request, ch *bridgeRegisteredChannel, subPath, token string) {
	if !bridgeValidateSubPath(subPath) {
		bridgeJSONError(w, "invalid_request", http.StatusBadRequest)
		return
	}

	manifestURL := ch.StreamURL

	var fetchURL string
	if subPath == "stream.m3u8" {
		fetchURL = manifestURL
	} else {
		base, err := url.Parse(manifestURL)
		if err != nil {
			bridgeJSONError(w, "stream_unavailable", http.StatusServiceUnavailable)
			return
		}
		ref, err := url.Parse(subPath)
		if err != nil {
			bridgeJSONError(w, "stream_unavailable", http.StatusServiceUnavailable)
			return
		}
		base.Path = path.Dir(base.Path) + "/"
		fetchURL = base.ResolveReference(ref).String()
	}

	cacheKey := r.URL.Path
	data, _, hit, err := s.cache.getOrFetch(cacheKey, func() (*hlsCacheFetchResult, error) {
		fr, err := hlsCacheFetchUpstream(bridgeStreamClient, fetchURL, r)
		if err != nil {
			return nil, err
		}
		// Rewrite manifests before caching (same as relay)
		if strings.HasSuffix(subPath, ".m3u8") {
			rewritten := bridgeRewriteManifest(manifestURL, fr.data, token)
			fr.data = rewritten
		}
		return fr, nil
	})
	if err != nil {
		bridgeJSONError(w, "stream_unavailable", http.StatusServiceUnavailable)
		return
	}

	bridgeSetStreamHeaders(w, subPath, ch.IsPrivate())
	if hit {
		w.Header().Set("Cache-Status", "HIT")
	} else {
		w.Header().Set("Cache-Status", "MISS")
	}
	w.Write(data)
}

// ---------- Helpers ----------

// bridgeWriteJSON writes a JSON response with the given status code.
func bridgeWriteJSON(w http.ResponseWriter, v interface{}, status int) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	enc.Encode(v)
}

// bridgeJSONError writes a JSON error response.
func bridgeJSONError(w http.ResponseWriter, code string, status int) {
	bridgeWriteJSON(w, map[string]string{"error": code}, status)
}

// bridgeCheckToken validates the access token for private channels.
// Returns true if access is allowed. Writes 403 and returns false if denied.
func bridgeCheckToken(w http.ResponseWriter, r *http.Request, ch *bridgeRegisteredChannel) bool {
	if !ch.IsPrivate() {
		return true
	}
	token := r.URL.Query().Get("token")
	if token != ch.Token {
		bridgeJSONError(w, "access_denied", http.StatusForbidden)
		return false
	}
	return true
}

// bridgeSetPrivateHeaders sets Referrer-Policy and overrides Cache-Control for private channels.
func bridgeSetPrivateHeaders(w http.ResponseWriter, ch *bridgeRegisteredChannel) {
	if !ch.IsPrivate() {
		return
	}
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Cache-Control", "private, no-store")
}

// bridgeXMLEscape escapes special XML characters.
func bridgeXMLEscape(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	s = strings.ReplaceAll(s, "\"", "&quot;")
	return s
}

// bridgeGuideToXMLTV generates an XMLTV document from guide entries.
// Used by bridge, relay, and server XMLTV endpoints.
func bridgeGuideToXMLTV(channelID, channelName string, entries []bridgeGuideEntry) string {
	var sb strings.Builder
	sb.WriteString("<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n")
	sb.WriteString("<tv>\n")
	sb.WriteString("  <channel id=\"" + bridgeXMLEscape(channelID) + "\">\n")
	sb.WriteString("    <display-name>" + bridgeXMLEscape(channelName) + "</display-name>\n")
	sb.WriteString("  </channel>\n")

	for _, e := range entries {
		sb.WriteString("  <programme start=\"" + bridgeISOToXMLTV(e.Start) + "\" stop=\"" + bridgeISOToXMLTV(e.End) + "\" channel=\"" + bridgeXMLEscape(channelID) + "\">\n")
		sb.WriteString("    <title>" + bridgeXMLEscape(e.Title) + "</title>\n")
		if e.Description != "" {
			sb.WriteString("    <desc>" + bridgeXMLEscape(e.Description) + "</desc>\n")
		}
		if e.Category != "" {
			sb.WriteString("    <category>" + bridgeXMLEscape(e.Category) + "</category>\n")
		}
		sb.WriteString("  </programme>\n")
	}

	sb.WriteString("</tv>\n")
	return sb.String()
}
