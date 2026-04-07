package main

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

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
func serverHTTP(mux *http.ServeMux, seg *hlsSegmenter, channelID, channelName string, metadata, guide []byte) {
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
		bridgeWriteJSON(w, map[string]interface{}{
			"protocol": "tltv",
			"versions": []int{1},
			"channels": []interface{}{
				map[string]interface{}{
					"id":   docs.channelID,
					"name": docs.channelName,
				},
			},
			"relaying": []interface{}{},
		}, http.StatusOK)
	})

	// Channel metadata: GET /tltv/v1/channels/{id}
	mux.HandleFunc("GET /tltv/v1/channels/{id}", func(w http.ResponseWriter, r *http.Request) {
		docs := serverDocsState.Load()
		w.Header().Set("Access-Control-Allow-Origin", "*")
		id := r.PathValue("id")
		if id != docs.channelID {
			bridgeJSONError(w, "channel_not_found", http.StatusNotFound)
			return
		}
		if docs.metadata == nil {
			bridgeJSONError(w, "channel_not_found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Cache-Control", "max-age=60")
		w.Write(docs.metadata)
	})

	// Channel sub-paths: guide.json, stream.m3u8, segments
	mux.HandleFunc("GET /tltv/v1/channels/{id}/{path...}", func(w http.ResponseWriter, r *http.Request) {
		docs := serverDocsState.Load()
		w.Header().Set("Access-Control-Allow-Origin", "*")
		id := r.PathValue("id")
		subPath := r.PathValue("path")

		if id != docs.channelID {
			bridgeJSONError(w, "channel_not_found", http.StatusNotFound)
			return
		}

		switch subPath {
		case "guide.json":
			if docs.guide == nil {
				bridgeJSONError(w, "channel_not_found", http.StatusNotFound)
				return
			}
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.Header().Set("Cache-Control", "max-age=300")
			w.Write(docs.guide)

		case "stream.m3u8":
			manifest := seg.getManifest()
			if manifest == "" {
				http.Error(w, "stream not ready", http.StatusServiceUnavailable)
				return
			}
			w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
			w.Header().Set("Cache-Control", "no-cache, no-store")
			w.Write([]byte(manifest))

		default:
			// Segment files via protocol path: /tltv/v1/channels/{id}/seg{N}.ts
			if strings.HasPrefix(subPath, "seg") && strings.HasSuffix(subPath, ".ts") {
				serveSegment(w, r, seg, subPath[3:len(subPath)-3])
				return
			}
			http.NotFound(w, r)
		}
	})

	// Peers (empty — server is standalone)
	mux.HandleFunc("GET /tltv/v1/peers", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Cache-Control", "max-age=300")
		bridgeWriteJSON(w, map[string]interface{}{
			"peers": []interface{}{},
		}, http.StatusOK)
	})

	// Health
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		bridgeWriteJSON(w, map[string]interface{}{
			"status":   "ok",
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
		bridgeJSONError(w, "invalid_request", http.StatusBadRequest)
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
			bridgeJSONError(w, "invalid_request", http.StatusBadRequest)
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
