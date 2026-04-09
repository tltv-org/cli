package main

import (
	_ "embed"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path"
	"strings"
	"time"
)

//go:embed hls.min.js
var hlsJSData []byte

const viewerFavicon = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 491.322 349.774"><style>g{fill:#000}@media(prefers-color-scheme:dark){g{fill:#fff}}</style><g transform="translate(-0.177,349.812) scale(0.1,-0.1)" stroke="none"><path d="M2050 3493 c-881 -31 -1492 -102 -1671 -194 -95 -48 -171 -145 -214 -272 -98 -292 -154 -705 -162 -1192 -8 -497 27 -842 127 -1233 48 -187 74 -246 140 -316 96 -102 195 -137 505 -181 756 -105 1734 -133 2665 -75 330 21 800 78 959 117 195 47 311 159 370 358 52 179 98 442 128 735 24 242 24 784 0 1030 -40 397 -116 746 -191 874 -34 58 -117 133 -179 161 -243 111 -1187 198 -2092 193 -170 -1 -344 -3 -385 -5z m-246 -993 c33 -5 92 -25 132 -44 68 -32 101 -64 609 -571 296 -295 547 -543 559 -550 17 -11 80 -15 299 -15 392 -2 361 -37 365 410 3 365 0 389 -57 427 -33 23 -39 23 -303 23 -177 0 -277 -4 -291 -11 -12 -6 -95 -84 -185 -172 l-162 -162 -113 113 -112 112 170 170 c178 178 221 211 320 250 57 22 75 24 326 28 170 2 293 0 340 -7 193 -30 343 -175 378 -365 14 -76 15 -704 1 -777 -15 -79 -67 -177 -124 -235 -58 -57 -156 -109 -235 -124 -70 -13 -552 -13 -622 0 -30 5 -85 26 -124 45 -64 31 -111 75 -580 545 -280 282 -532 529 -558 551 l-49 39 -278 0 -278 0 -30 -25 c-52 -43 -53 -53 -50 -424 l3 -343 37 -34 c22 -20 49 -35 65 -35 100 -6 532 5 548 13 11 6 92 82 180 169 l160 159 113 -113 112 -113 -183 -182 c-262 -260 -269 -262 -677 -262 -141 0 -281 5 -311 10 -170 32 -310 164 -355 335 -10 37 -14 143 -14 423 0 351 1 376 21 434 52 156 176 269 332 303 75 16 530 20 621 5z"/></g></svg>`

// viewerServer serves the local viewer page and proxies HLS to the upstream target.
type viewerServer struct {
	channelID   string
	channelName string
	streamDir   string // upstream stream directory URL (e.g., "https://host/tltv/v1/channels/id/")
	streamFile  string // manifest filename (e.g., "stream.m3u8")
	streamURL   string // full upstream stream URL for display
	xmltvURL    string // upstream XMLTV guide URL
	baseURL     string // upstream base URL (e.g., "https://host:port")
	token       string
	client      *http.Client
	metadata    map[string]interface{}
	guide       map[string]interface{} // may be nil
	tltvURI     string
}

// ---------- Shared Viewer Helpers ----------

// viewerConfig holds the parsed --viewer flag state.
type viewerConfig struct {
	enabled  bool   // viewer is on
	selector string // channel ID or tltv:// URI (empty = auto-select first)
	fromCLI  bool   // true if --viewer appeared in CLI args (prevents config override)
}

// parseViewerArg pre-processes --viewer from args before fs.Parse().
// --viewer without a value enables auto-selection of the first channel.
// --viewer followed by a channel ID (TV...) or tltv:// URI selects that channel.
// Also reads the VIEWER env var: "1" enables auto-select, any other non-empty
// value is treated as a channel selector. CLI overrides env.
func parseViewerArg(args []string) (remaining []string, vc viewerConfig) {
	// Env var default
	if env := os.Getenv("VIEWER"); env != "" {
		switch env {
		case "0", "false":
			// explicitly disabled
		case "1", "true":
			vc.enabled = true
		default:
			vc.enabled = true
			vc.selector = env
		}
	}

	// Scan CLI args
	remaining = make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		arg := args[i]

		// --viewer=value
		if strings.HasPrefix(arg, "--viewer=") {
			val := arg[len("--viewer="):]
			vc.enabled = true
			vc.fromCLI = true
			if val != "1" && val != "true" {
				vc.selector = val
			} else {
				vc.selector = ""
			}
			continue
		}

		// --viewer [optional channel selector]
		if arg == "--viewer" {
			vc.enabled = true
			vc.fromCLI = true
			vc.selector = ""
			// Peek: if next arg looks like a channel ref, consume it
			if i+1 < len(args) {
				next := args[i+1]
				if strings.HasPrefix(next, "tltv://") || strings.HasPrefix(next, "TV") {
					vc.selector = next
					i++
				}
			}
			continue
		}

		remaining = append(remaining, arg)
	}

	return
}

// resolveViewerChannelID extracts a channel ID from the viewer selector.
// Returns "" for auto-select (empty selector). Validates channel IDs and
// parses tltv:// URIs to extract the channel component.
func resolveViewerChannelID(selector string) (string, error) {
	if selector == "" {
		return "", nil
	}
	if strings.HasPrefix(selector, "tltv://") {
		uri, err := parseTLTVUri(selector)
		if err != nil {
			return "", fmt.Errorf("invalid viewer URI: %v", err)
		}
		return uri.ChannelID, nil
	}
	if err := validateChannelID(selector); err != nil {
		return "", fmt.Errorf("invalid viewer channel: %v", err)
	}
	return selector, nil
}

// applyViewerConfig applies the "viewer" field from a daemon config file.
// Only applies if --viewer was not set on the CLI. Config accepts:
//
//	true       → enable auto-select
//	"TVabc..." → enable with channel selector
//	"tltv://..." → enable with URI selector
func applyViewerConfig(vc *viewerConfig, cfg map[string]interface{}) {
	if vc.fromCLI {
		return
	}
	val, ok := cfg["viewer"]
	if !ok {
		return
	}
	switch v := val.(type) {
	case bool:
		vc.enabled = v
		vc.selector = ""
	case string:
		if v == "" || v == "0" || v == "false" {
			return
		}
		vc.enabled = true
		if v != "1" && v != "true" {
			vc.selector = v
		}
	}
}

// viewerEmbedRoutes registers the viewer HTML, static assets, and /api/info
// on an existing mux. infoFn is called per-request to get current channel state.
//
// Routes registered:
//
//	GET /{$}         → viewer HTML (exact root path only)
//	GET /favicon.svg → SVG icon
//	GET /hls.min.js  → vendored HLS.js
//	GET /api/info    → JSON channel info
//
// Protocol endpoints (/.well-known/tltv, /tltv/v1/...) registered separately
// by the daemon take routing priority over the "/" subtree pattern.
func viewerEmbedRoutes(mux *http.ServeMux, infoFn func() map[string]interface{}) {
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(viewerHTML))
	})

	mux.HandleFunc("GET /favicon.svg", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/svg+xml")
		w.Header().Set("Cache-Control", "max-age=86400")
		w.Write([]byte(viewerFavicon))
	})

	mux.HandleFunc("GET /hls.min.js", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/javascript")
		w.Header().Set("Cache-Control", "max-age=86400")
		w.Write(hlsJSData)
	})

	mux.HandleFunc("GET /api/info", func(w http.ResponseWriter, r *http.Request) {
		info := infoFn()
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		enc := json.NewEncoder(w)
		enc.SetEscapeHTML(false)
		enc.Encode(info)
	})
}

// statusPageRoutes registers the static status page at GET /{$} when --viewer
// is NOT enabled. Shows nav bar + node section (protocol, channel list).
// No video, no JS. Same Phosphor Dark design.
func statusPageRoutes(mux *http.ServeMux, nodeInfoFn func() *NodeInfo) {
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		info := nodeInfoFn()
		var proto string
		if info != nil && len(info.Versions) > 0 {
			proto = fmt.Sprintf("%s protocol v%d", info.Protocol, info.Versions[0])
		}

		var sb strings.Builder
		sb.WriteString(statusPageHead)

		if info != nil {
			sb.WriteString(`<div class="ge" style="color:#999">`)
			sb.WriteString(proto)
			sb.WriteString(`</div>`)

			if len(info.Channels) > 0 {
				sb.WriteString(fmt.Sprintf(`<div class="ge" style="color:#666;margin-top:10px">Origin Channels (%d)</div>`, len(info.Channels)))
				for _, ch := range info.Channels {
					sb.WriteString(`<div class="ge"><span class="uri">`)
					sb.WriteString(ch.ID)
					sb.WriteString(`</span>  <span class="gt">`)
					sb.WriteString(ch.Name)
					sb.WriteString(`</span></div>`)
				}
			}
			if len(info.Relaying) > 0 {
				sb.WriteString(fmt.Sprintf(`<div class="ge" style="color:#666;margin-top:10px">Relay Channels (%d)</div>`, len(info.Relaying)))
				for _, ch := range info.Relaying {
					sb.WriteString(`<div class="ge"><span class="uri">`)
					sb.WriteString(ch.ID)
					sb.WriteString(`</span>  <span class="gt">`)
					sb.WriteString(ch.Name)
					sb.WriteString(`</span></div>`)
				}
			}
		}

		sb.WriteString(statusPageTail)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(sb.String()))
	})

	mux.HandleFunc("GET /favicon.svg", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/svg+xml")
		w.Header().Set("Cache-Control", "max-age=86400")
		w.Write([]byte(viewerFavicon))
	})
}

var statusPageHead = pageHead("tltv", "") + `
<body><div class="c">
` + pageNav("status") + `
<div class="inner"><div class="sl" style="margin-top:8px">node</div><div class="db">`

const statusPageTail = `</div></div></div></body></html>`

// viewerNavSVG is the TLTV wordmark (logo mark + "tltv" text paths) used in all page nav bars.
const viewerNavSVG = `<svg viewBox="0 0 1020 350" fill="#fff" aria-hidden="true"><svg x="0" y="0" width="491" height="350" viewBox="0 0 491.321548 349.773636"><g transform="translate(-0.176918,349.811893) scale(0.1,-0.1)" stroke="none"><path d="M2050 3493 c-881 -31 -1492 -102 -1671 -194 -95 -48 -171 -145 -214 -272 -98 -292 -154 -705 -162 -1192 -8 -497 27 -842 127 -1233 48 -187 74 -246 140 -316 96 -102 195 -137 505 -181 756 -105 1734 -133 2665 -75 330 21 800 78 959 117 195 47 311 159 370 358 52 179 98 442 128 735 24 242 24 784 0 1030 -40 397 -116 746 -191 874 -34 58 -117 133 -179 161 -243 111 -1187 198 -2092 193 -170 -1 -344 -3 -385 -5z m-246 -993 c33 -5 92 -25 132 -44 68 -32 101 -64 609 -571 296 -295 547 -543 559 -550 17 -11 80 -15 299 -15 392 -2 361 -37 365 410 3 365 0 389 -57 427 -33 23 -39 23 -303 23 -177 0 -277 -4 -291 -11 -12 -6 -95 -84 -185 -172 l-162 -162 -113 113 -112 112 170 170 c178 178 221 211 320 250 57 22 75 24 326 28 170 2 293 0 340 -7 193 -30 343 -175 378 -365 14 -76 15 -704 1 -777 -15 -79 -67 -177 -124 -235 -58 -57 -156 -109 -235 -124 -70 -13 -552 -13 -622 0 -30 5 -85 26 -124 45 -64 31 -111 75 -580 545 -280 282 -532 529 -558 551 l-49 39 -278 0 -278 0 -30 -25 c-52 -43 -53 -53 -50 -424 l3 -343 37 -34 c22 -20 49 -35 65 -35 100 -6 532 5 548 13 11 6 92 82 180 169 l160 159 113 -113 112 -113 -183 -182 c-262 -260 -269 -262 -677 -262 -141 0 -281 5 -311 10 -170 32 -310 164 -355 335 -10 37 -14 143 -14 423 0 351 1 376 21 434 52 156 176 269 332 303 75 16 530 20 621 5z"/></g></svg><svg x="530" y="15" width="480" height="320" viewBox="0 -984 1726 1276"><g transform="scale(1,-1)"><g transform="translate(0,0)"><path d="M260 0Q211 0 180.5 30.5Q150 61 150 112V392H26V496H150V650H276V496H412V392H276V134Q276 104 304 104H400V0Z"/></g><g transform="translate(456,0)"><path d="M70 0V700H196V0Z"/></g><g transform="translate(722,0)"><path d="M260 0Q211 0 180.5 30.5Q150 61 150 112V392H26V496H150V650H276V496H412V392H276V134Q276 104 304 104H400V0Z"/></g><g transform="translate(1178,0)"><path d="M174 0 16 496H150L265 92H283L398 496H532L374 0Z"/></g></g></svg></svg>`

// viewerBuildInfo builds the /api/info JSON base from signed document bytes.
// Callers add context-specific fields (stream_src, tltv_uri, etc.) to the result.
func viewerBuildInfo(channelID, channelName string, metadataJSON, guideJSON []byte) map[string]interface{} {
	info := map[string]interface{}{
		"channel_id":   channelID,
		"channel_name": channelName,
		"verified":     true,
	}

	if metadataJSON != nil {
		var meta map[string]interface{}
		if json.Unmarshal(metadataJSON, &meta) == nil {
			info["metadata"] = meta
		}
	}

	if guideJSON != nil {
		var guide map[string]interface{}
		if json.Unmarshal(guideJSON, &guide) == nil {
			info["guide"] = guide
		}
	}

	return info
}

// ---------- Standalone Viewer ----------

func cmdViewer(args []string) {
	fs := flag.NewFlagSet("viewer", flag.ExitOnError)

	defaultListen := "127.0.0.1:9000"
	if v := os.Getenv("LISTEN"); v != "" {
		defaultListen = v
	}
	listen := fs.String("listen", defaultListen, "listen address")
	fs.StringVar(listen, "l", defaultListen, "alias for --listen")

	token := fs.String("token", "", "access token for private channels")
	fs.StringVar(token, "t", "", "alias for --token")

	logLevel, logFormat, logFile := addLogFlags(fs)
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Start a local web viewer for a TLTV channel\n\n")
		fmt.Fprintf(os.Stderr, "Usage: tltv viewer [flags] <target>\n\n")
		fmt.Fprintf(os.Stderr, "Target can be a tltv:// URI, compact ID@host, or bare hostname:\n")
		fmt.Fprintf(os.Stderr, "  tltv viewer demo.timelooptv.org\n")
		fmt.Fprintf(os.Stderr, "  tltv viewer TVabc...@demo.timelooptv.org:443\n")
		fmt.Fprintf(os.Stderr, "  tltv viewer \"tltv://TVabc...@demo.timelooptv.org:443\"\n")
		fmt.Fprintf(os.Stderr, "  tltv viewer --token secret TVabc...@localhost:8000\n\n")
		fmt.Fprintf(os.Stderr, "Flags:\n")
		fmt.Fprintf(os.Stderr, "  -t, --token string    access token for private channels\n")
		fmt.Fprintf(os.Stderr, "  -l, --listen string   listen address (default: 127.0.0.1:9000)\n\n")
		fmt.Fprintf(os.Stderr, "Environment variables: LISTEN, LOG_LEVEL, LOG_FORMAT, LOG_FILE.\n")
		fmt.Fprintf(os.Stderr, "Flags override env vars.\n")
	}
	fs.Parse(args)

	if fs.NArg() < 1 {
		fs.Usage()
		os.Exit(1)
	}

	if err := setupLogging(*logLevel, *logFormat, *logFile, "viewer"); err != nil {
		fatal("%v", err)
	}

	target := fs.Arg(0)

	// Token: flag overrides URI-embedded token
	tok := extractToken(target)
	if *token != "" {
		tok = *token
	}

	client := newClient(flagInsecure)

	channelID, host, err := parseTargetOrDiscover(target, client)
	if err != nil {
		fatal("%v", err)
	}

	// Fetch and verify metadata
	logInfof("fetching metadata for %s from %s", channelID, host)
	doc, err := client.FetchMetadata(host, channelID, tok)
	if err != nil {
		fatal("fetch metadata: %v", err)
	}
	if err := verifyDocument(doc, channelID); err != nil {
		fatal("verify metadata: %v", err)
	}
	logInfof("metadata verified: %s", getString(doc, "name"))

	// Fetch guide (non-fatal)
	var guide map[string]interface{}
	guide, err = client.FetchGuide(host, channelID, tok)
	if err != nil {
		logInfof("guide not available: %v", err)
	} else if err := verifyDocument(guide, channelID); err != nil {
		logInfof("guide verification failed: %v", err)
		guide = nil
	}

	// Build stream URL components
	streamPath := getString(doc, "stream")
	if streamPath == "" {
		fatal("metadata has no stream path")
	}

	base := client.baseURL(host)
	streamDir := base + path.Dir(streamPath) + "/"
	streamFile := path.Base(streamPath)
	streamURL := base + streamPath
	if tok != "" {
		streamURL += "?token=" + tok
	}

	xmltvURL := base + "/tltv/v1/channels/" + channelID + "/guide.xml"
	if tok != "" {
		xmltvURL += "?token=" + tok
	}

	// Build tltv URI for display
	tltvURI := formatTLTVUri(channelID, []string{host}, "")

	// Warn if listen address is non-local
	if listenHost, _, splitErr := net.SplitHostPort(*listen); splitErr == nil {
		if listenHost != "" && listenHost != "127.0.0.1" && listenHost != "localhost" && listenHost != "::1" {
			logInfof("WARNING: listening on non-local address %s", *listen)
		}
	}

	srv := &viewerServer{
		channelID:   channelID,
		channelName: getString(doc, "name"),
		streamDir:   streamDir,
		streamFile:  streamFile,
		streamURL:   streamURL,
		xmltvURL:    xmltvURL,
		baseURL:     base,
		token:       tok,
		client:      client.http,
		metadata:    doc,
		guide:       guide,
		tltvURI:     tltvURI,
	}

	mux := http.NewServeMux()

	// Shared viewer routes (HTML, assets, /api/info)
	viewerEmbedRoutes(mux, func() map[string]interface{} {
		info := map[string]interface{}{
			"channel_id":   srv.channelID,
			"channel_name": srv.channelName,
			"tltv_uri":     srv.tltvURI,
			"stream_file":  srv.streamFile,
			"stream_src":   "/stream/" + srv.streamFile,
			"stream_url":   srv.streamURL,
			"xmltv_url":    srv.xmltvURL,
			"base_url":     srv.baseURL,
			"verified":     true,
			"metadata":     srv.metadata,
		}
		if srv.guide != nil {
			info["guide"] = srv.guide
		}
		return info
	})

	// Standalone-only: stream proxy to remote upstream
	mux.HandleFunc("/stream/", srv.handleStream)

	httpSrv := &http.Server{
		Addr:              *listen,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	logInfof("viewer: http://%s", displayListenAddr(*listen))

	// Graceful shutdown
	sigCh := make(chan os.Signal, 1)
	signalNotify(sigCh)
	go func() {
		<-sigCh
		logInfof("shutting down")
		httpSrv.Close()
	}()

	if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		fatal("server: %v", err)
	}
}

// ---------- Standalone Stream Proxy ----------

func (s *viewerServer) handleStream(w http.ResponseWriter, r *http.Request) {
	subPath := strings.TrimPrefix(r.URL.Path, "/stream/")
	if !validateSubPath(subPath) {
		http.NotFound(w, r)
		return
	}

	// Build upstream URL
	upstreamURL := s.streamDir + subPath
	if s.token != "" {
		if strings.Contains(upstreamURL, "?") {
			upstreamURL += "&token=" + s.token
		} else {
			upstreamURL += "?token=" + s.token
		}
	}

	req, err := http.NewRequestWithContext(r.Context(), "GET", upstreamURL, nil)
	if err != nil {
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}
	req.Header.Set("User-Agent", "tltv-cli/"+version)

	resp, err := s.client.Do(req)
	if err != nil {
		logDebugf("upstream error: %v", err)
		http.Error(w, "Upstream error", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 50*1024*1024))
	if err != nil {
		http.Error(w, "Read error", http.StatusBadGateway)
		return
	}

	if resp.StatusCode != http.StatusOK {
		w.WriteHeader(resp.StatusCode)
		w.Write(body)
		return
	}

	// Set content type and cache headers
	w.Header().Set("Content-Type", streamContentType(subPath))
	if strings.HasSuffix(subPath, ".m3u8") {
		body = rewriteManifest(upstreamURL, body, "")
		w.Header().Set("Cache-Control", "max-age=1, no-cache")
	} else {
		w.Header().Set("Cache-Control", "max-age=3600")
	}

	w.Write(body)
}

// ---------- Shared Web Template ----------

// pageHead returns the shared HTML <head> with base CSS. title is the <title> content,
// extraCSS is appended after the base styles.
func pageHead(title, extraCSS string) string {
	return `<!DOCTYPE html>
<html>
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>` + title + `</title>
<link rel="icon" type="image/svg+xml" href="/favicon.svg">
<style>
` + baseCSS + extraCSS + `
</style>
</head>`
}

// pageNav returns the shared nav bar. label appears right-aligned (e.g. "debug viewer", "status").
func pageNav(label string) string {
	return `<div class="hd">` + viewerNavSVG + `<span class="hdl">` + label + `</span></div>`
}

const baseCSS = `*{margin:0;padding:0;box-sizing:border-box;scrollbar-width:none}
::-webkit-scrollbar{width:0;height:0}
body{background:#000;color:#fff;font-family:'SFMono-Regular',Consolas,'Liberation Mono',Menlo,monospace;font-size:14px;line-height:1.6}
.c{max-width:72rem;width:100%;margin:0 auto}
.hd{display:flex;align-items:center;gap:1.5rem;padding:1rem 2rem;border-bottom:1px solid #333;font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,'Helvetica Neue',Arial,sans-serif}
.hd svg{height:24px;width:auto;flex-shrink:0}
.hdl{font-size:.9rem;color:#999;margin-left:auto}
.inner{max-width:calc(1100px + 2rem);width:100%;margin:0 auto;padding:1.25rem 1rem 0}
hr{border:none;border-top:1px solid #333;margin:12px 0}
.sl{font-size:.7rem;text-transform:uppercase;letter-spacing:.06em;color:#666;margin-bottom:4px}
.uri{font-size:.8rem;color:#666;word-break:break-all}
.ge{font-size:.8rem;color:#999;padding:2px 0}
.gt{color:#666}
.db{font-size:.75rem;color:#999}
.dr{display:flex;flex-wrap:wrap;gap:16px;padding:2px 0}
.di span:first-child{color:#666}
.ok{color:#4ade80}.wn{color:#fbbf24}.er{color:#ef4444}
a.uri{color:#666;text-decoration:none}
a.uri:hover{color:#999}
`

const viewerExtraCSS = `.vw{position:relative;width:100%;padding-bottom:56.25%;background:#000}
.vw video{position:absolute;top:0;left:0;width:100%;height:100%;object-fit:contain;background:#000}
.ov{position:absolute;top:0;left:0;width:100%;height:100%;display:flex;align-items:center;justify-content:center;background:rgba(0,0,0,.88);z-index:1}
.ov.h{display:none}
.sp{width:14px;height:14px;border:1.5px solid rgba(255,255,255,.2);border-top-color:rgba(255,255,255,.5);border-radius:50%;animation:spin 1s linear infinite;margin-right:8px}
@keyframes spin{to{transform:rotate(360deg)}}
.ov span{color:rgba(255,255,255,.5);font-size:14px}
.ctrl{display:flex;align-items:center;gap:.5rem;height:36px;padding:0}
.cn{font-size:.85rem;font-weight:600;color:#fff;white-space:nowrap}
.sep{font-size:.8rem;color:#666}
.prg{font-size:.8rem;color:#999;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;min-width:0}
.spacer{flex:1}
.cb{background:none;border:1px solid #333;color:#666;font-family:inherit;font-size:.7rem;padding:2px 8px;cursor:pointer;border-radius:0}
.cb:hover{color:#999;border-color:#666}
.ubar{padding:.2rem 0 .6rem;border-bottom:1px solid #333}
`

// ---------- Embedded HTML ----------

var viewerHTML = pageHead("tltv viewer", viewerExtraCSS) + `
<body>
<div class="c">
` + pageNav("debug viewer") + `

  <div class="inner">
  <div class="vw">
    <video id="v" muted playsinline></video>
    <div id="ov" class="ov"><div class="sp"></div><span>connecting...</span></div>
  </div>
  <div class="ctrl">
    <span id="cn" class="cn"></span>
    <span id="ps" class="sep" style="display:none">/</span>
    <span id="prg" class="prg"></span>
    <span class="spacer"></span>
    <button id="mb" class="cb" onclick="tmu()">unmute</button>
  </div>
  <div class="ubar">
    <span id="uri" class="uri"></span>
  </div>

  <div class="sl" style="margin-top:12px">channel</div>
  <div class="db" id="chd"></div>
  <hr>

  <div class="sl">stream</div>
  <div class="db" id="std"></div>
  <hr>

  <div class="sl">guide</div>
  <div class="db" id="gdd"></div>
  <div id="gd"></div>
  <hr>

  <div class="sl">node</div>
  <div class="db" id="nd"><div class="ge" style="color:#666">loading...</div></div>
  <hr>

  <div class="sl">peers</div>
  <div class="db" id="pd"><div class="ge" style="color:#666">loading...</div></div>
  </div>
</div>
</div>

<script src="/hls.min.js"></script>
<script>
var inf={},hlsInst=null;

function esc(s){var d=document.createElement('div');d.textContent=s;return d.innerHTML}
function kv(p,k,v,isUrl){
  var vh=isUrl?'<a class="uri" href="'+esc(v)+'" target="_blank">'+esc(v)+'</a>':'<span class="uri">'+esc(v)+'</span>';
  var d=document.createElement('div');d.className='dr';
  d.innerHTML='<div class="di"><span>'+esc(k)+' </span>'+vh+'</div>';
  p.appendChild(d);
}

fetch('/api/info').then(function(r){return r.json()}).then(function(d){
  inf=d;
  var base=d.base_url||window.location.origin;
  var m=d.metadata||{};
  document.getElementById('cn').textContent=d.channel_name;
  document.getElementById('uri').textContent=d.tltv_uri;
  document.title=d.channel_name+' \u2014 tltv viewer';

  // === 1. Channel section: curated fields then remaining ===
  var chd=document.getElementById('chd');
  kv(chd,'verified',d.verified?'\u2713 Signature valid':'? Unknown',false);
  if(m.name) kv(chd,'name',m.name,false);
  if(d.tltv_uri) kv(chd,'uri',d.tltv_uri,false);
  kv(chd,'status',m.status||'active',false);
  kv(chd,'access',m.access||'public',false);
  if(m.stream) kv(chd,'stream',base+m.stream,true);
  if(m.guide){
    kv(chd,'guide',base+m.guide,true);
    kv(chd,'xmltv',base+m.guide.replace('guide.json','guide.xml'),true);
  }
  if(m.updated) kv(chd,'updated',m.updated,false);
  if(m.seq!==undefined) kv(chd,'seq',String(m.seq),false);
  // Dump remaining keys
  var skip={v:1,signature:1,id:1,name:1,status:1,access:1,stream:1,guide:1,updated:1,seq:1};
  Object.keys(m).sort().forEach(function(k){
    if(skip[k]) return;
    var val=m[k];
    if(Array.isArray(val)) val=val.join(', ');
    else if(val!==null&&typeof val==='object') val=JSON.stringify(val);
    else val=String(val);
    if((k==='icon')&&val.charAt(0)==='/') val=base+val;
    kv(chd,k,val,val.indexOf('http')===0);
  });

  // === 2. Stream section: curated fields ===
  var std=document.getElementById('std');
  kv(std,'status','connecting',false);
  if(d.stream_url) kv(std,'url',d.stream_url,true);

  // === 3. Guide section ===
  var gdd=document.getElementById('gdd');
  var gd=document.getElementById('gd');
  if(d.guide){
    kv(gdd,'verified',d.verified?'\u2713 Signature valid':'? Unknown',false);
    if(m.guide) kv(gdd,'url',base+m.guide,true);
    if(m.guide) kv(gdd,'xmltv',base+m.guide.replace('guide.json','guide.xml'),true);
    if(d.guide.from) kv(gdd,'from',d.guide.from,false);
    if(d.guide.until) kv(gdd,'until',d.guide.until,false);
    var entries=d.guide.entries||[];
    kv(gdd,'entries',String(entries.length),false);
    if(entries.length){
      var nowISO=new Date().toISOString();
      entries.forEach(function(e){
        var s=e.start?e.start.substring(5,16).replace('T',' '):'';
        var n=e.end?e.end.substring(11,16):'';
        var np=e.start&&e.end&&e.start<=nowISO&&e.end>nowISO;
        if(np){
          document.getElementById('prg').textContent=e.title||'';
          document.getElementById('ps').style.display='';
        }
        var mk=np?'<span class="ok">> </span>':'  ';
        var div=document.createElement('div');div.className='ge';
        var cat=e.category?' <span class="gt">['+esc(e.category)+']</span>':'';
        div.innerHTML=mk+'<span class="gt">'+esc(s)+' \u2013 '+esc(n)+'</span>  '+esc(e.title||'')+cat;
        gd.appendChild(div);
      });
    }
  } else {
    gd.innerHTML='<div class="ge" style="color:#666">no guide data</div>';
  }

  initPlayer(d.stream_src);

  // === 4. Node section ===
  fetch(base+'/.well-known/tltv').then(function(r){return r.json()}).then(function(n){
    var nd=document.getElementById('nd');
    nd.innerHTML='';
    var ver=(n.versions&&n.versions.length)?n.versions[0]:'?';
    kv(nd,'protocol',n.protocol+' protocol v'+ver,false);
    if(n.channels&&n.channels.length){
      var hdr=document.createElement('div');hdr.className='ge';hdr.style.color='#666';hdr.style.marginTop='6px';
      hdr.textContent='Origin Channels ('+n.channels.length+')';nd.appendChild(hdr);
      n.channels.forEach(function(ch){
        var mk=(ch.id===d.channel_id)?'<span class="ok">> </span>':'  ';
        var div=document.createElement('div');div.className='ge';
        div.innerHTML=mk+'<span class="uri">'+esc(ch.id)+'</span>  <span class="gt">'+esc(ch.name)+'</span>';
        nd.appendChild(div);
      });
    }
    if(n.relaying&&n.relaying.length){
      var hdr=document.createElement('div');hdr.className='ge';hdr.style.color='#666';hdr.style.marginTop='6px';
      hdr.textContent='Relay Channels ('+n.relaying.length+')';nd.appendChild(hdr);
      n.relaying.forEach(function(ch){
        var mk=(ch.id===d.channel_id)?'<span class="ok">> </span>':'  ';
        var div=document.createElement('div');div.className='ge';
        div.innerHTML=mk+'<span class="uri">'+esc(ch.id)+'</span>  <span class="gt">'+esc(ch.name)+'</span>';
        nd.appendChild(div);
      });
    }
  }).catch(function(){document.getElementById('nd').innerHTML='<div class="ge" style="color:#666">unavailable</div>'});

  // === 5. Peers section ===
  fetch(base+'/tltv/v1/peers').then(function(r){return r.json()}).then(function(p){
    var pd=document.getElementById('pd');
    pd.innerHTML='';
    var peers=p.peers||[];
    if(!peers.length){pd.innerHTML='<div class="ge" style="color:#666">no peers</div>';return}
    peers.forEach(function(pr){
      var div=document.createElement('div');div.className='ge';
      var hints=(pr.hints||[]).join(', ');
      div.innerHTML='<span class="uri">'+esc(pr.id)+'</span>  <span class="gt">'+esc(pr.name)+'</span>  <span class="gt">'+esc(hints)+'</span>';
      pd.appendChild(div);
    });
  }).catch(function(){document.getElementById('pd').innerHTML='<div class="ge" style="color:#666">unavailable</div>'});
});

function initPlayer(src){
  var video=document.getElementById('v');
  var ov=document.getElementById('ov');
  var std=document.getElementById('std');

  // Build stream section once with stable elements — values updated in-place
  function buildStreamUI(){
    std.innerHTML='';
    kv(std,'status','connecting',false);
    if(inf.stream_url) kv(std,'url',inf.stream_url,true);
    kv(std,'content-type','application/vnd.apple.mpegurl',false);
    kv(std,'segments','-',false);
    kv(std,'target duration','-',false);
    kv(std,'media sequence','-',false);
    kv(std,'bitrate','-',false);
    kv(std,'buffer','-',false);
    kv(std,'resolution','-',false);
    // Tag value spans for in-place updates
    var spans=std.querySelectorAll('.di');
    spans.forEach(function(di){
      var lbl=di.querySelector('span');
      var val=di.querySelectorAll('span')[1]||di.querySelector('.uri');
      if(!lbl||!val) return;
      var k=lbl.textContent.trim();
      if(k==='status') val.id='sv_st';
      else if(k==='segments') val.id='sv_sg';
      else if(k==='target duration') val.id='sv_td';
      else if(k==='media sequence') val.id='sv_ms';
      else if(k==='bitrate') val.id='sv_br';
      else if(k==='buffer') val.id='sv_bu';
      else if(k==='resolution') val.id='sv_re';
    });
  }
  buildStreamUI();

  if(typeof Hls!=='undefined'&&Hls.isSupported()){
    var hls=new Hls({liveSyncDurationCount:3,liveMaxLatencyDurationCount:6});
    hlsInst=hls;
    hls.loadSource(src);
    hls.attachMedia(video);

    hls.on(Hls.Events.MANIFEST_PARSED,function(){
      video.play().catch(function(){});
      ov.classList.add('h');
      var e=document.getElementById('sv_st');
      if(e) e.textContent='\u2713 live';
    });

    hls.on(Hls.Events.LEVEL_LOADED,function(e,data){
      var det=data.details;
      if(!det) return;
      var el;
      el=document.getElementById('sv_sg');if(el) el.textContent=String(det.fragments?det.fragments.length:0);
      el=document.getElementById('sv_td');if(el) el.textContent=det.targetduration?det.targetduration+'s':'-';
      el=document.getElementById('sv_ms');if(el) el.textContent=(det.startSN!==undefined)?String(det.startSN):'-';
    });

    hls.on(Hls.Events.FRAG_LOADED,function(e,data){
      var f=data.frag;
      if(f&&f.stats){
        var bytes=f.stats.loaded||f.stats.total||0;
        if(bytes>0&&f.duration>0){
          var el=document.getElementById('sv_br');
          if(el) el.textContent=((bytes*8/f.duration)/1e6).toFixed(1)+' Mbps';
        }
      }
    });

    hls.on(Hls.Events.ERROR,function(e,data){
      if(data.fatal){var el=document.getElementById('sv_st');if(el) el.textContent='\u2717 error: '+data.type}
    });

    setInterval(function(){
      if(video.buffered.length>0){
        var b=(video.buffered.end(video.buffered.length-1)-video.currentTime).toFixed(1)+'s';
        var el=document.getElementById('sv_bu');if(el) el.textContent=b;
      }
      if(video.videoWidth>0){
        var el=document.getElementById('sv_re');if(el) el.textContent=video.videoWidth+'x'+video.videoHeight;
      }
    },1000);

  } else if(video.canPlayType('application/vnd.apple.mpegurl')){
    video.src=src;
    video.addEventListener('loadedmetadata',function(){
      video.play().catch(function(){});
      ov.classList.add('h');
      var el=document.getElementById('sv_st');if(el) el.textContent='\u2713 live';
    });
  } else {
    var el=document.getElementById('sv_st');if(el) el.textContent='\u2717 HLS not supported';
  }
}

function tmu(){
  var v=document.getElementById('v');
  v.muted=!v.muted;
  document.getElementById('mb').textContent=v.muted?'unmute':'mute';
}
</script>
</body>
</html>`
