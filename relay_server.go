package main

import (
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
)

// ---------- Server ----------

// relayServer implements the TLTV protocol HTTP endpoints for relayed channels.
type relayServer struct {
	registry *relayRegistry
	client   *Client    // for proxying streams from upstream
	cache    *hlsCache  // optional HLS cache (nil = disabled)
	mux      *http.ServeMux
}

// newRelayServer creates a relay HTTP server with all protocol endpoints.
// Pass cache=nil to disable caching.
func newRelayServer(registry *relayRegistry, client *Client, cache *hlsCache) *relayServer {
	s := &relayServer{
		registry: registry,
		client:   client,
		cache:    cache,
		mux:      http.NewServeMux(),
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

// ServeHTTP adds CORS headers and handles OPTIONS preflight.
func (s *relayServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
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
// Relay channels appear in "relaying", not "channels".
func (s *relayServer) handleNodeInfo(w http.ResponseWriter, r *http.Request) {
	channels := s.registry.ListChannels()

	type channelEntry struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}

	var relaying []channelEntry
	for _, ch := range channels {
		// Skip migrated entries (they have no stream, just the migration doc)
		if ch.Name == "(migrated)" {
			continue
		}
		relaying = append(relaying, channelEntry{ID: ch.ChannelID, Name: ch.Name})
	}
	if relaying == nil {
		relaying = []channelEntry{}
	}

	w.Header().Set("Cache-Control", "max-age=60")
	bridgeWriteJSON(w, map[string]interface{}{
		"protocol": "tltv",
		"versions": []int{1},
		"channels": []interface{}{},
		"relaying": relaying,
	}, http.StatusOK)
}

// handlePeers serves GET /tltv/v1/peers with full gossip exchange.
func (s *relayServer) handlePeers(w http.ResponseWriter, r *http.Request) {
	peerInfos := s.registry.ListPeers()

	type peerEntry struct {
		ID       string   `json:"id"`
		Name     string   `json:"name"`
		Hints    []string `json:"hints"`
		LastSeen string   `json:"last_seen"`
	}

	peers := make([]peerEntry, 0, len(peerInfos))
	for _, p := range peerInfos {
		hints := p.Hints
		if hints == nil {
			hints = []string{}
		}
		peers = append(peers, peerEntry{
			ID:       p.ChannelID,
			Name:     p.Name,
			Hints:    hints,
			LastSeen: p.LastSeen.UTC().Format(timestampFormat),
		})
	}

	w.Header().Set("Cache-Control", "max-age=300")
	bridgeWriteJSON(w, map[string]interface{}{
		"peers": peers,
	}, http.StatusOK)
}

// handleChannelMeta serves GET /tltv/v1/channels/{id}
// Serves the raw verified metadata bytes verbatim.
func (s *relayServer) handleChannelMeta(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ch := s.registry.GetChannel(id)
	if ch == nil || ch.Metadata == nil {
		bridgeJSONError(w, "channel_not_found", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "max-age=60")
	w.Write(ch.Metadata)
}

// handleChannelPath serves GET /tltv/v1/channels/{id}/{path...}
func (s *relayServer) handleChannelPath(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	subPath := r.PathValue("path")

	ch := s.registry.GetChannel(id)
	if ch == nil {
		bridgeJSONError(w, "channel_not_found", http.StatusNotFound)
		return
	}

	switch subPath {
	case "guide.json":
		s.serveGuideJSON(w, ch)
	case "guide.xml":
		s.serveGuideXML(w, ch)
	default:
		s.serveStream(w, r, ch, subPath)
	}
}

// handleHealth serves GET /health
func (s *relayServer) handleHealth(w http.ResponseWriter, r *http.Request) {
	bridgeWriteJSON(w, map[string]interface{}{
		"status":   "ok",
		"relaying": s.registry.ChannelCount(),
	}, http.StatusOK)
}

// handleMethodNotAllowed returns 400 for non-GET methods.
func (s *relayServer) handleMethodNotAllowed(w http.ResponseWriter, r *http.Request) {
	bridgeJSONError(w, "invalid_request", http.StatusBadRequest)
}

// ---------- Guide Serving ----------

// serveGuideJSON serves the raw verified guide bytes verbatim.
func (s *relayServer) serveGuideJSON(w http.ResponseWriter, ch *relayRegisteredChannel) {
	if ch.Guide == nil {
		bridgeJSONError(w, "channel_not_found", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "max-age=300")
	w.Write(ch.Guide)
}

// serveGuideXML generates XMLTV from parsed guide entries.
func (s *relayServer) serveGuideXML(w http.ResponseWriter, ch *relayRegisteredChannel) {
	entries := ch.GuideEntries
	if len(entries) == 0 && ch.Guide == nil {
		bridgeJSONError(w, "channel_not_found", http.StatusNotFound)
		return
	}
	if len(entries) == 0 {
		entries = bridgeDefaultGuideEntries(ch.Name)
	}

	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.Header().Set("Cache-Control", "max-age=300")
	w.Write([]byte(bridgeGuideToXMLTV(ch.ChannelID, ch.Name, entries)))
}

// ---------- Stream Proxying ----------

// serveStream proxies HLS content from the upstream origin.
// Rewrites absolute URLs to relative so viewers fetch through the relay.
// When cache is enabled, uses singleflight deduplication and protocol-compliant
// TTLs (1s for manifests, 3600s for segments) per spec section 9.10.
func (s *relayServer) serveStream(w http.ResponseWriter, r *http.Request, ch *relayRegisteredChannel, subPath string) {
	if !bridgeValidateSubPath(subPath) {
		bridgeJSONError(w, "invalid_request", http.StatusBadRequest)
		return
	}

	if ch.StreamHint == "" {
		bridgeJSONError(w, "stream_unavailable", http.StatusServiceUnavailable)
		return
	}

	// Construct upstream URL
	base := s.client.baseURL(ch.StreamHint)
	manifestURL := base + "/tltv/v1/channels/" + ch.ChannelID + "/stream.m3u8"

	var fetchURL string
	if subPath == "stream.m3u8" {
		fetchURL = manifestURL
	} else {
		parsed, err := url.Parse(manifestURL)
		if err != nil {
			bridgeJSONError(w, "stream_unavailable", http.StatusServiceUnavailable)
			return
		}
		ref, err := url.Parse(subPath)
		if err != nil {
			bridgeJSONError(w, "stream_unavailable", http.StatusServiceUnavailable)
			return
		}
		parsed.Path = path.Dir(parsed.Path) + "/"
		fetchURL = parsed.ResolveReference(ref).String()
	}

	// Cache path: use full request path as cache key
	if s.cache != nil {
		cacheKey := r.URL.Path
		data, contentType, hit, err := s.cache.getOrFetch(cacheKey, func() (*hlsCacheFetchResult, error) {
			fr, err := hlsCacheFetchUpstream(s.client.http, fetchURL, r)
			if err != nil {
				return nil, err
			}
			// Rewrite manifests before caching
			if strings.HasSuffix(subPath, ".m3u8") {
				rewritten := bridgeRewriteManifest(manifestURL, fr.data, "")
				fr.data = rewritten
			}
			return fr, nil
		})
		if err != nil {
			bridgeJSONError(w, "stream_unavailable", http.StatusServiceUnavailable)
			return
		}

		bridgeSetStreamHeaders(w, subPath, false)
		if hit {
			w.Header().Set("Cache-Status", "HIT")
		} else {
			w.Header().Set("Cache-Status", "MISS")
		}
		w.Write(data)
		_ = contentType // headers set by bridgeSetStreamHeaders
		return
	}

	// Non-cache path: direct proxy
	req, err := http.NewRequestWithContext(r.Context(), "GET", fetchURL, nil)
	if err != nil {
		bridgeJSONError(w, "stream_unavailable", http.StatusServiceUnavailable)
		return
	}
	req.Header.Set("User-Agent", "tltv-cli/"+version)

	resp, err := s.client.http.Do(req)
	if err != nil {
		bridgeJSONError(w, "stream_unavailable", http.StatusServiceUnavailable)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bridgeJSONError(w, "stream_unavailable", http.StatusServiceUnavailable)
		return
	}

	// Set headers (no private channel handling -- relay never has private channels)
	bridgeSetStreamHeaders(w, subPath, false)

	if strings.HasSuffix(subPath, ".m3u8") {
		body, err := io.ReadAll(io.LimitReader(resp.Body, bridgeMaxManifestSize))
		if err != nil {
			bridgeJSONError(w, "stream_unavailable", http.StatusServiceUnavailable)
			return
		}
		// Rewrite absolute-to-relative, no token (relay has no private channels)
		rewritten := bridgeRewriteManifest(manifestURL, body, "")
		w.Write(rewritten)
		return
	}

	io.Copy(w, resp.Body)
}


