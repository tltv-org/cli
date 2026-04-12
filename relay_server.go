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
	client   *Client             // for proxying streams from upstream
	cache    *hlsCache           // optional HLS cache (nil = disabled)
	peerReg  *peerRegistry       // optional external peers from --peers (nil = disabled)
	bufMgr   *relayBufferManager // optional buffer manager (nil = no buffering)
	mux      *http.ServeMux
}

// newRelayServer creates a relay HTTP server with all protocol endpoints.
// Pass cache=nil to disable caching, peerReg=nil to disable external peers,
// bufMgr=nil to disable buffering.
func newRelayServer(registry *relayRegistry, client *Client, cache *hlsCache, peerReg *peerRegistry, bufMgr *relayBufferManager) *relayServer {
	s := &relayServer{
		registry: registry,
		client:   client,
		cache:    cache,
		peerReg:  peerReg,
		bufMgr:   bufMgr,
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

	var relaying []interface{}
	for _, ch := range channels {
		// Skip migrated entries (they have no stream, just the migration doc)
		if ch.Name == "(migrated)" {
			continue
		}
		entry := map[string]interface{}{
			"id":   ch.ChannelID,
			"name": ch.Name,
		}
		// Include delay field when buffer+delay is active
		if s.bufMgr != nil && s.bufMgr.delay > 0 {
			entry["delay"] = int(s.bufMgr.delay.Seconds())
		}
		relaying = append(relaying, entry)
	}
	if relaying == nil {
		relaying = []interface{}{}
	}

	w.Header().Set("Cache-Control", "max-age=60")
	writeJSON(w, map[string]interface{}{
		"protocol": "tltv",
		"versions": []int{1},
		"channels": []interface{}{},
		"relaying": relaying,
	}, http.StatusOK)
}

// handlePeers serves GET /tltv/v1/peers with full gossip exchange.
// Own relayed channels are visible in /.well-known/tltv; peers endpoint
// shows the network around this node — gossip (if --gossip) + external peers (--peers).
func (s *relayServer) handlePeers(w http.ResponseWriter, r *http.Request) {
	// External: gossip-discovered peers + verified --peers
	// Own relayed channels are visible in /.well-known/tltv, not here.
	var external []peerEntry
	external = append(external, s.registry.ListGossipPeers()...)
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
// Serves the raw verified metadata bytes verbatim.
func (s *relayServer) handleChannelMeta(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ch := s.registry.GetChannel(id)
	if ch == nil || ch.Metadata == nil {
		jsonError(w, "channel_not_found", http.StatusNotFound)
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
		jsonError(w, "channel_not_found", http.StatusNotFound)
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
	writeJSON(w, map[string]interface{}{
		"status":   "ok",
		"relaying": s.registry.ChannelCount(),
	}, http.StatusOK)
}

// handleMethodNotAllowed returns 400 for non-GET methods.
func (s *relayServer) handleMethodNotAllowed(w http.ResponseWriter, r *http.Request) {
	jsonError(w, "invalid_request", http.StatusBadRequest)
}

// ---------- Guide Serving ----------

// serveGuideJSON serves the raw verified guide bytes verbatim.
func (s *relayServer) serveGuideJSON(w http.ResponseWriter, ch *relayRegisteredChannel) {
	if ch.Guide == nil {
		jsonError(w, "channel_not_found", http.StatusNotFound)
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

// ---------- Stream Proxying ----------

// serveStream proxies HLS content from the upstream origin.
// Rewrites absolute URLs to relative so viewers fetch through the relay.
// When cache is enabled, uses singleflight deduplication and protocol-compliant
// TTLs (1s for manifests, 3600s for segments) per spec section 9.10.
func (s *relayServer) serveStream(w http.ResponseWriter, r *http.Request, ch *relayRegisteredChannel, subPath string) {
	if !validateSubPath(subPath) {
		jsonError(w, "invalid_request", http.StatusBadRequest)
		return
	}

	// Buffer path: serve from proactive buffers if available.
	// - stream.m3u8 serves the cached root master playlist when upstream is multivariant,
	//   or a generated buffered media playlist when upstream is single-variant.
	// - child media playlists (stream_*.m3u8, audio_*.m3u8, subs_*.m3u8) serve generated
	//   buffered manifests.
	// - segment and subtitle files serve directly from buffered bytes.
	// Falls through to upstream proxy if a buffer is empty or unavailable.
	if s.bufMgr != nil {
		if subPath == "stream.m3u8" {
			if manifest := s.bufMgr.GetRootManifest(ch.ChannelID); manifest != "" {
				setStreamHeaders(w, subPath, false)
				w.Write([]byte(manifest))
				return
			}
		}
		if strings.HasSuffix(subPath, ".m3u8") {
			if manifest := s.bufMgr.GetManifest(ch.ChannelID, subPath); manifest != "" {
				setStreamHeaders(w, subPath, false)
				w.Write([]byte(manifest))
				return
			}
		} else if data := s.bufMgr.GetSegmentByPath(ch.ChannelID, subPath); data != nil {
			setStreamHeaders(w, subPath, false)
			w.Write(data)
			return
		}
	}

	if ch.StreamHint == "" {
		jsonError(w, "stream_unavailable", http.StatusServiceUnavailable)
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
				rewritten := rewriteManifest(manifestURL, fr.data, "")
				fr.data = rewritten
			}
			return fr, nil
		})
		if err != nil {
			jsonError(w, "stream_unavailable", http.StatusServiceUnavailable)
			return
		}

		setStreamHeaders(w, subPath, false)
		if hit {
			w.Header().Set("Cache-Status", "HIT")
		} else {
			w.Header().Set("Cache-Status", "MISS")
		}
		w.Write(data)
		_ = contentType // headers set by setStreamHeaders
		return
	}

	// Non-cache path: direct proxy
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

	// Set headers (no private channel handling -- relay never has private channels)
	setStreamHeaders(w, subPath, false)

	if strings.HasSuffix(subPath, ".m3u8") {
		body, err := io.ReadAll(io.LimitReader(resp.Body, maxManifestSize))
		if err != nil {
			jsonError(w, "stream_unavailable", http.StatusServiceUnavailable)
			return
		}
		// Rewrite absolute-to-relative, no token (relay has no private channels)
		rewritten := rewriteManifest(manifestURL, body, "")
		w.Write(rewritten)
		return
	}

	io.Copy(w, resp.Body)
}
