package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

// setServerPrivateHeaders sets Referrer-Policy and overrides Cache-Control
// for private server channels.
func setServerPrivateHeaders(w http.ResponseWriter, isPrivate bool) {
	if !isPrivate {
		return
	}
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Cache-Control", "private, no-store")
}

// serverCacheStatus returns the Cache-Status header value for a hit/miss.
func serverCacheStatus(hit bool) string {
	if hit {
		return "HIT"
	}
	return "MISS"
}

func serverManifestBytes(manifest, token string) []byte {
	data := []byte(manifest)
	if token == "" {
		return data
	}
	return rewriteManifest("", data, token)
}

// serveSegment writes an HLS segment response for the given sequence number.
func serveSegment(w http.ResponseWriter, r *http.Request, seg *hlsSegmenter, numStr string) {
	seqNum, err := strconv.ParseUint(numStr, 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	data := seg.getSegment(seqNum)
	if data == nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "video/mp2t")
	w.Header().Set("Cache-Control", "max-age=10")
	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(data)))
	w.Write(data)
}

// serverHTTP sets up HTTP handlers for the server command.
// Serves HLS stream and TLTV protocol endpoints.
// Pass cache=nil to disable caching. Pass gossipReg=nil to disable gossip.
// serverToken is the expected access token (empty string = public channel).
// serverIsPrivate controls well-known listing exclusion.
func serverHTTP(mux *http.ServeMux, seg *hlsSegmenter, channelID, channelName string, metadata, guide []byte, cache *hlsCache, peerReg *peerRegistry, gossipReg *peerRegistry, serverToken string, serverIsPrivate bool, iconData []byte, iconCT string) {
	// Store initial docs atomically
	serverDocsState.Store(&serverDocs{
		channelID:   channelID,
		channelName: channelName,
		metadata:    metadata,
		guide:       guide,
	})

	// --- TLTV Protocol Endpoints ---

	// Node info: GET /.well-known/tltv
	mux.HandleFunc("GET /.well-known/tltv", func(w http.ResponseWriter, r *http.Request) {
		docs := serverDocsState.Load()
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Cache-Control", "max-age=60")
		var channels []interface{}
		if !serverIsPrivate {
			channels = []interface{}{
				map[string]interface{}{
					"id":   docs.channelID,
					"name": docs.channelName,
				},
			}
		} else {
			channels = []interface{}{}
		}
		writeJSON(w, map[string]interface{}{
			"protocol": "tltv",
			"versions": []int{1},
			"channels": channels,
			"relaying": []interface{}{},
		}, http.StatusOK)
	})

	// Channel metadata: GET /tltv/v1/channels/{id}
	mux.HandleFunc("GET /tltv/v1/channels/{id}", func(w http.ResponseWriter, r *http.Request) {
		docs := serverDocsState.Load()
		w.Header().Set("Access-Control-Allow-Origin", "*")
		id := r.PathValue("id")
		if id != docs.channelID {
			jsonError(w, "channel_not_found", http.StatusNotFound)
			return
		}
		if !checkRequestToken(w, r, serverToken) {
			return
		}
		if docs.metadata == nil {
			jsonError(w, "channel_not_found", http.StatusNotFound)
			return
		}
		if cache != nil {
			data, _, hit, err := cache.getOrFetch(r.URL.Path, func() (*hlsCacheFetchResult, error) {
				d := serverDocsState.Load()
				if d.metadata == nil {
					return nil, &hlsCacheUpstreamError{status: http.StatusNotFound}
				}
				return &hlsCacheFetchResult{data: d.metadata, contentType: "application/json; charset=utf-8"}, nil
			})
			if err == nil {
				w.Header().Set("Content-Type", "application/json; charset=utf-8")
				w.Header().Set("Cache-Control", "max-age=60")
				w.Header().Set("Cache-Status", serverCacheStatus(hit))
				setServerPrivateHeaders(w, serverIsPrivate)
				w.Write(data)
				return
			}
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Cache-Control", "max-age=60")
		setServerPrivateHeaders(w, serverIsPrivate)
		w.Write(docs.metadata)
	})

	// Channel sub-paths: guide.json, stream.m3u8, segments
	mux.HandleFunc("GET /tltv/v1/channels/{id}/{path...}", func(w http.ResponseWriter, r *http.Request) {
		docs := serverDocsState.Load()
		w.Header().Set("Access-Control-Allow-Origin", "*")
		id := r.PathValue("id")
		subPath := r.PathValue("path")

		if id != docs.channelID {
			jsonError(w, "channel_not_found", http.StatusNotFound)
			return
		}
		if !checkRequestToken(w, r, serverToken) {
			return
		}

		switch subPath {
		case "guide.json":
			if docs.guide == nil {
				jsonError(w, "channel_not_found", http.StatusNotFound)
				return
			}
			if cache != nil {
				data, _, hit, err := cache.getOrFetch(r.URL.Path, func() (*hlsCacheFetchResult, error) {
					d := serverDocsState.Load()
					if d.guide == nil {
						return nil, &hlsCacheUpstreamError{status: http.StatusNotFound}
					}
					return &hlsCacheFetchResult{data: d.guide, contentType: "application/json; charset=utf-8"}, nil
				})
				if err == nil {
					w.Header().Set("Content-Type", "application/json; charset=utf-8")
					w.Header().Set("Cache-Control", "max-age=300")
					w.Header().Set("Cache-Status", serverCacheStatus(hit))
					setServerPrivateHeaders(w, serverIsPrivate)
					w.Write(data)
					return
				}
			}
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.Header().Set("Cache-Control", "max-age=300")
			setServerPrivateHeaders(w, serverIsPrivate)
			w.Write(docs.guide)

		case "guide.xml":
			if docs.guide == nil {
				jsonError(w, "channel_not_found", http.StatusNotFound)
				return
			}
			if cache != nil {
				data, _, hit, err := cache.getOrFetch(r.URL.Path, func() (*hlsCacheFetchResult, error) {
					d := serverDocsState.Load()
					if d.guide == nil {
						return nil, &hlsCacheUpstreamError{status: http.StatusNotFound}
					}
					xml := serverGuideToXMLTV(d.guide, d.channelID, d.channelName)
					return &hlsCacheFetchResult{data: []byte(xml), contentType: "application/xml; charset=utf-8"}, nil
				})
				if err == nil {
					w.Header().Set("Content-Type", "application/xml; charset=utf-8")
					w.Header().Set("Cache-Control", "max-age=300")
					w.Header().Set("Cache-Status", serverCacheStatus(hit))
					setServerPrivateHeaders(w, serverIsPrivate)
					w.Write(data)
					return
				}
			}
			xml := serverGuideToXMLTV(docs.guide, docs.channelID, docs.channelName)
			w.Header().Set("Content-Type", "application/xml; charset=utf-8")
			w.Header().Set("Cache-Control", "max-age=300")
			setServerPrivateHeaders(w, serverIsPrivate)
			w.Write([]byte(xml))

		case "stream.m3u8":
			if cache != nil {
				data, _, hit, err := cache.getOrFetch(r.URL.Path, func() (*hlsCacheFetchResult, error) {
					m := seg.getManifest()
					if m == "" {
						return nil, &hlsCacheUpstreamError{status: http.StatusServiceUnavailable}
					}
					return &hlsCacheFetchResult{data: serverManifestBytes(m, serverToken), contentType: "application/vnd.apple.mpegurl"}, nil
				})
				if err == nil {
					w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
					w.Header().Set("Cache-Control", "no-cache, no-store")
					w.Header().Set("Cache-Status", serverCacheStatus(hit))
					setServerPrivateHeaders(w, serverIsPrivate)
					w.Write(data)
					return
				}
			}
			manifest := seg.getManifest()
			if manifest == "" {
				http.Error(w, "stream not ready", http.StatusServiceUnavailable)
				return
			}
			w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
			w.Header().Set("Cache-Control", "no-cache, no-store")
			setServerPrivateHeaders(w, serverIsPrivate)
			w.Write(serverManifestBytes(manifest, serverToken))

		case "icon.svg", "icon.png", "icon.jpg":
			if len(iconData) > 0 {
				w.Header().Set("Content-Type", iconCT)
				w.Header().Set("Cache-Control", "max-age=86400")
				setServerPrivateHeaders(w, serverIsPrivate)
				w.Write(iconData)
			} else {
				http.NotFound(w, r)
			}

		default:
			// Segment files via protocol path: /tltv/v1/channels/{id}/seg{N}.ts
			if strings.HasPrefix(subPath, "seg") && strings.HasSuffix(subPath, ".ts") {
				if cache != nil {
					data, _, hit, err := cache.getOrFetch(r.URL.Path, func() (*hlsCacheFetchResult, error) {
						numStr := subPath[3 : len(subPath)-3]
						seqNum, parseErr := strconv.ParseUint(numStr, 10, 64)
						if parseErr != nil {
							return nil, &hlsCacheUpstreamError{status: http.StatusNotFound}
						}
						segData := seg.getSegment(seqNum)
						if segData == nil {
							return nil, &hlsCacheUpstreamError{status: http.StatusNotFound}
						}
						return &hlsCacheFetchResult{data: segData, contentType: "video/mp2t"}, nil
					})
					if err == nil {
						w.Header().Set("Content-Type", "video/mp2t")
						w.Header().Set("Cache-Control", "max-age=10")
						w.Header().Set("Content-Length", fmt.Sprintf("%d", len(data)))
						w.Header().Set("Cache-Status", serverCacheStatus(hit))
						setServerPrivateHeaders(w, serverIsPrivate)
						w.Write(data)
						return
					}
					// Cache fetch error (segment not found) — fall through to 404
				}
				setServerPrivateHeaders(w, serverIsPrivate)
				serveSegment(w, r, seg, subPath[3:len(subPath)-3])
				return
			}
			http.NotFound(w, r)
		}
	})

	// Peers
	mux.HandleFunc("GET /tltv/v1/peers", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Cache-Control", "max-age=300")
		var external []peerEntry
		if peerReg != nil {
			external = peerReg.ListPeers()
		}
		if gossipReg != nil {
			external = append(external, gossipReg.ListPeers()...)
		}
		peers := buildPeersResponse(nil, external)
		writeJSON(w, map[string]interface{}{
			"peers": peers,
		}, http.StatusOK)
	})

	// Health
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		writeJSON(w, map[string]interface{}{
			"status":   "ok",
			"version":  version,
			"channels": 1,
		}, http.StatusOK)
	})

	// Method not allowed for non-GET on protocol endpoints
	mux.HandleFunc("/.well-known/tltv", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		if r.Method == "OPTIONS" {
			w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
			w.WriteHeader(http.StatusNoContent)
			return
		}
		jsonError(w, "invalid_request", http.StatusBadRequest)
	})

	mux.HandleFunc("/tltv/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		if r.Method == "OPTIONS" {
			w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method != "GET" {
			jsonError(w, "invalid_request", http.StatusBadRequest)
			return
		}
		http.NotFound(w, r)
	})

	// Catch-all: 404 for unknown paths
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		if r.Method == "OPTIONS" {
			w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
			w.WriteHeader(http.StatusNoContent)
			return
		}
		http.NotFound(w, r)
	})
}

// serverMultiHTTP sets up HTTP handlers for multi-channel server mode.
// Each serverChannel has its own segmenter and docs. Shared: cache, peers, gossip, icon, token.
func serverMultiHTTP(mux *http.ServeMux, channels []*serverChannel, cache *hlsCache, peerReg *peerRegistry, gossipReg *peerRegistry, serverToken string, serverIsPrivate bool, iconData []byte, iconCT string) {
	// Build lookup map
	chanMap := make(map[string]*serverChannel, len(channels))
	for _, ch := range channels {
		chanMap[ch.channelID] = ch
	}

	// Node info: GET /.well-known/tltv
	mux.HandleFunc("GET /.well-known/tltv", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Cache-Control", "max-age=60")
		var chList []interface{}
		if !serverIsPrivate {
			for _, ch := range channels {
				docs := ch.docs.Load()
				chList = append(chList, map[string]interface{}{
					"id":   docs.channelID,
					"name": docs.channelName,
				})
			}
		}
		if chList == nil {
			chList = []interface{}{}
		}
		writeJSON(w, map[string]interface{}{
			"protocol": "tltv",
			"versions": []int{1},
			"channels": chList,
			"relaying": []interface{}{},
		}, http.StatusOK)
	})

	// Channel metadata: GET /tltv/v1/channels/{id}
	mux.HandleFunc("GET /tltv/v1/channels/{id}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		id := r.PathValue("id")
		ch, ok := chanMap[id]
		if !ok {
			jsonError(w, "channel_not_found", http.StatusNotFound)
			return
		}
		if !checkRequestToken(w, r, serverToken) {
			return
		}
		docs := ch.docs.Load()
		if docs.metadata == nil {
			jsonError(w, "channel_not_found", http.StatusNotFound)
			return
		}
		if cache != nil {
			data, _, hit, err := cache.getOrFetch(r.URL.Path, func() (*hlsCacheFetchResult, error) {
				d := ch.docs.Load()
				if d.metadata == nil {
					return nil, &hlsCacheUpstreamError{status: http.StatusNotFound}
				}
				return &hlsCacheFetchResult{data: d.metadata, contentType: "application/json; charset=utf-8"}, nil
			})
			if err == nil {
				w.Header().Set("Content-Type", "application/json; charset=utf-8")
				w.Header().Set("Cache-Control", "max-age=60")
				w.Header().Set("Cache-Status", serverCacheStatus(hit))
				setServerPrivateHeaders(w, serverIsPrivate)
				w.Write(data)
				return
			}
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Cache-Control", "max-age=60")
		setServerPrivateHeaders(w, serverIsPrivate)
		w.Write(docs.metadata)
	})

	// Channel sub-paths: guide, stream, segments, icon
	mux.HandleFunc("GET /tltv/v1/channels/{id}/{path...}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		id := r.PathValue("id")
		subPath := r.PathValue("path")
		ch, ok := chanMap[id]
		if !ok {
			jsonError(w, "channel_not_found", http.StatusNotFound)
			return
		}
		if !checkRequestToken(w, r, serverToken) {
			return
		}
		docs := ch.docs.Load()

		switch subPath {
		case "guide.json":
			if docs.guide == nil {
				jsonError(w, "channel_not_found", http.StatusNotFound)
				return
			}
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.Header().Set("Cache-Control", "max-age=300")
			setServerPrivateHeaders(w, serverIsPrivate)
			w.Write(docs.guide)

		case "guide.xml":
			if docs.guide == nil {
				jsonError(w, "channel_not_found", http.StatusNotFound)
				return
			}
			xml := serverGuideToXMLTV(docs.guide, docs.channelID, docs.channelName)
			w.Header().Set("Content-Type", "application/xml; charset=utf-8")
			w.Header().Set("Cache-Control", "max-age=300")
			setServerPrivateHeaders(w, serverIsPrivate)
			w.Write([]byte(xml))

		case "stream.m3u8":
			if len(ch.variants) > 0 || len(ch.audioTracks) > 0 || len(ch.subtitleTracks) > 0 {
				// Master playlist (video variants, audio-only, or both)
				w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
				w.Header().Set("Cache-Control", "no-cache, no-store")
				setServerPrivateHeaders(w, serverIsPrivate)
				w.Write(serverManifestBytes(masterPlaylist(ch.variants, ch.audioTracks, ch.subtitleTracks, ch.state != nil && !ch.state.noAudio), serverToken))
			} else if ch.seg != nil {
				manifest := ch.seg.getManifest()
				if manifest == "" {
					http.Error(w, "stream not ready", http.StatusServiceUnavailable)
					return
				}
				w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
				w.Header().Set("Cache-Control", "no-cache, no-store")
				setServerPrivateHeaders(w, serverIsPrivate)
				w.Write(serverManifestBytes(manifest, serverToken))
			} else {
				http.Error(w, "stream not ready", http.StatusServiceUnavailable)
			}

		case "icon.svg", "icon.png", "icon.jpg":
			if len(iconData) > 0 {
				w.Header().Set("Content-Type", iconCT)
				w.Header().Set("Cache-Control", "max-age=86400")
				setServerPrivateHeaders(w, serverIsPrivate)
				w.Write(iconData)
			} else {
				http.NotFound(w, r)
			}

		default:
			// Subtitle track media playlists: subs_{name}.m3u8
			if strings.HasPrefix(subPath, "subs_") && strings.HasSuffix(subPath, ".m3u8") && len(ch.subtitleTracks) > 0 {
				trackName := subPath[5 : len(subPath)-5] // strip "subs_" and ".m3u8"
				for i := range ch.subtitleTracks {
					if ch.subtitleTracks[i].name == trackName {
						manifest := ch.subtitleTracks[i].seg.getManifest()
						if manifest == "" {
							http.Error(w, "stream not ready", http.StatusServiceUnavailable)
							return
						}
						w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
						w.Header().Set("Cache-Control", "no-cache, no-store")
						setServerPrivateHeaders(w, serverIsPrivate)
						w.Write(serverManifestBytes(manifest, serverToken))
						return
					}
				}
				http.NotFound(w, r)
				return
			}

			// Subtitle VTT segments: subs_{name}_seg{N}.vtt
			if strings.HasPrefix(subPath, "subs_") && strings.HasSuffix(subPath, ".vtt") && len(ch.subtitleTracks) > 0 {
				for i := range ch.subtitleTracks {
					prefix := "subs_" + ch.subtitleTracks[i].name + "_"
					if strings.HasPrefix(subPath, prefix) {
						segName := subPath[len(prefix):]
						if strings.HasPrefix(segName, "seg") {
							numStr := segName[3 : len(segName)-4] // strip "seg" and ".vtt"
							seqNum, parseErr := strconv.ParseUint(numStr, 10, 64)
							if parseErr != nil {
								http.NotFound(w, r)
								return
							}
							vtt := ch.subtitleTracks[i].seg.getSegment(seqNum)
							if vtt == "" {
								http.NotFound(w, r)
								return
							}
							w.Header().Set("Content-Type", "text/vtt; charset=utf-8")
							w.Header().Set("Cache-Control", "max-age=10")
							setServerPrivateHeaders(w, serverIsPrivate)
							w.Write([]byte(vtt))
							return
						}
					}
				}
				http.NotFound(w, r)
				return
			}

			// Audio track media playlists: audio_{name}.m3u8
			if strings.HasPrefix(subPath, "audio_") && strings.HasSuffix(subPath, ".m3u8") && len(ch.audioTracks) > 0 {
				trackName := subPath[6 : len(subPath)-5] // strip "audio_" and ".m3u8"
				for i := range ch.audioTracks {
					if ch.audioTracks[i].name == trackName {
						manifest := ch.audioTracks[i].seg.getManifest()
						if manifest == "" {
							http.Error(w, "stream not ready", http.StatusServiceUnavailable)
							return
						}
						w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
						w.Header().Set("Cache-Control", "no-cache, no-store")
						setServerPrivateHeaders(w, serverIsPrivate)
						w.Write(serverManifestBytes(manifest, serverToken))
						return
					}
				}
				http.NotFound(w, r)
				return
			}

			// Variant media playlists: stream_{label}.m3u8
			if strings.HasPrefix(subPath, "stream_") && strings.HasSuffix(subPath, ".m3u8") && len(ch.variants) > 0 {
				label := subPath[7 : len(subPath)-5] // strip "stream_" and ".m3u8"
				for i := range ch.variants {
					if ch.variants[i].label == label {
						manifest := ch.variants[i].seg.getManifest()
						if manifest == "" {
							http.Error(w, "stream not ready", http.StatusServiceUnavailable)
							return
						}
						w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
						w.Header().Set("Cache-Control", "no-cache, no-store")
						setServerPrivateHeaders(w, serverIsPrivate)
						w.Write(serverManifestBytes(manifest, serverToken))
						return
					}
				}
				http.NotFound(w, r)
				return
			}
			// Segment files: seg{N}.ts, {label}_seg{N}.ts (variants), audio_{name}_seg{N}.ts
			if strings.HasSuffix(subPath, ".ts") {
				segName := subPath
				targetSeg := ch.seg // default segmenter
				// Check for audio track prefix: e.g. "audio_rock_seg0.ts"
				if len(ch.audioTracks) > 0 {
					for i := range ch.audioTracks {
						prefix := "audio_" + ch.audioTracks[i].name + "_"
						if strings.HasPrefix(segName, prefix) {
							targetSeg = ch.audioTracks[i].seg
							segName = segName[len(prefix):]
							break
						}
					}
				}
				// Check for variant prefix: e.g. "720p_seg0.ts"
				if len(ch.variants) > 0 && targetSeg == ch.seg {
					for i := range ch.variants {
						prefix := ch.variants[i].label + "_"
						if strings.HasPrefix(segName, prefix) {
							targetSeg = ch.variants[i].seg
							segName = segName[len(prefix):]
							break
						}
					}
				}
				if strings.HasPrefix(segName, "seg") {
					setServerPrivateHeaders(w, serverIsPrivate)
					serveSegment(w, r, targetSeg, segName[3:len(segName)-3])
					return
				}
			}
			http.NotFound(w, r)
		}
	})

	// Peers
	mux.HandleFunc("GET /tltv/v1/peers", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Cache-Control", "max-age=300")
		var external []peerEntry
		if peerReg != nil {
			external = peerReg.ListPeers()
		}
		if gossipReg != nil {
			external = append(external, gossipReg.ListPeers()...)
		}
		writeJSON(w, map[string]interface{}{
			"peers": buildPeersResponse(nil, external),
		}, http.StatusOK)
	})

	// Health
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]interface{}{
			"status":   "ok",
			"version":  version,
			"channels": len(channels),
		}, http.StatusOK)
	})

	// Method rejection for TLTV paths
	mux.HandleFunc("/tltv/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		if r.Method == "OPTIONS" {
			w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.Header().Set("Allow", "GET, OPTIONS")
		jsonError(w, "method_not_allowed", http.StatusMethodNotAllowed)
	})

	// Catch-all
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		if r.Method == "OPTIONS" {
			w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
			w.WriteHeader(http.StatusNoContent)
			return
		}
		http.NotFound(w, r)
	})
}

// serverGuideToXMLTV generates XMLTV from the signed guide JSON bytes.
// Parses the guide document to extract entries, then formats as XMLTV using
// the shared guideToXMLTV helper.
func serverGuideToXMLTV(guideJSON []byte, channelID, channelName string) string {
	var doc map[string]interface{}
	if err := json.Unmarshal(guideJSON, &doc); err != nil {
		return "<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<tv/>\n"
	}

	entries := extractGuideEntries(doc)
	return guideToXMLTV(channelID, channelName, entries)
}
