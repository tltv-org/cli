package main

import (
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

//go:embed hls.min.js
var hlsJSData []byte

const viewerFavicon = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 491.322 349.774"><style>g{fill:#000}@media(prefers-color-scheme:dark){g{fill:#fff}}</style><g transform="translate(-0.177,349.812) scale(0.1,-0.1)" stroke="none"><path d="M2050 3493 c-881 -31 -1492 -102 -1671 -194 -95 -48 -171 -145 -214 -272 -98 -292 -154 -705 -162 -1192 -8 -497 27 -842 127 -1233 48 -187 74 -246 140 -316 96 -102 195 -137 505 -181 756 -105 1734 -133 2665 -75 330 21 800 78 959 117 195 47 311 159 370 358 52 179 98 442 128 735 24 242 24 784 0 1030 -40 397 -116 746 -191 874 -34 58 -117 133 -179 161 -243 111 -1187 198 -2092 193 -170 -1 -344 -3 -385 -5z m-246 -993 c33 -5 92 -25 132 -44 68 -32 101 -64 609 -571 296 -295 547 -543 559 -550 17 -11 80 -15 299 -15 392 -2 361 -37 365 410 3 365 0 389 -57 427 -33 23 -39 23 -303 23 -177 0 -277 -4 -291 -11 -12 -6 -95 -84 -185 -172 l-162 -162 -113 113 -112 112 170 170 c178 178 221 211 320 250 57 22 75 24 326 28 170 2 293 0 340 -7 193 -30 343 -175 378 -365 14 -76 15 -704 1 -777 -15 -79 -67 -177 -124 -235 -58 -57 -156 -109 -235 -124 -70 -13 -552 -13 -622 0 -30 5 -85 26 -124 45 -64 31 -111 75 -580 545 -280 282 -532 529 -558 551 l-49 39 -278 0 -278 0 -30 -25 c-52 -43 -53 -53 -50 -424 l3 -343 37 -34 c22 -20 49 -35 65 -35 100 -6 532 5 548 13 11 6 92 82 180 169 l160 159 113 -113 112 -113 -183 -182 c-262 -260 -269 -262 -677 -262 -141 0 -281 5 -311 10 -170 32 -310 164 -355 335 -10 37 -14 143 -14 423 0 351 1 376 21 434 52 156 176 269 332 303 75 16 530 20 621 5z"/></g></svg>`

// defaultIconSVG is the TLTV logo with a fixed white fill for use as a standalone icon.
const defaultIconSVG = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 491.322 349.774"><g transform="translate(-0.177,349.812) scale(0.1,-0.1)" fill="#fff" stroke="none"><path d="M2050 3493 c-881 -31 -1492 -102 -1671 -194 -95 -48 -171 -145 -214 -272 -98 -292 -154 -705 -162 -1192 -8 -497 27 -842 127 -1233 48 -187 74 -246 140 -316 96 -102 195 -137 505 -181 756 -105 1734 -133 2665 -75 330 21 800 78 959 117 195 47 311 159 370 358 52 179 98 442 128 735 24 242 24 784 0 1030 -40 397 -116 746 -191 874 -34 58 -117 133 -179 161 -243 111 -1187 198 -2092 193 -170 -1 -344 -3 -385 -5z m-246 -993 c33 -5 92 -25 132 -44 68 -32 101 -64 609 -571 296 -295 547 -543 559 -550 17 -11 80 -15 299 -15 392 -2 361 -37 365 410 3 365 0 389 -57 427 -33 23 -39 23 -303 23 -177 0 -277 -4 -291 -11 -12 -6 -95 -84 -185 -172 l-162 -162 -113 113 -112 112 170 170 c178 178 221 211 320 250 57 22 75 24 326 28 170 2 293 0 340 -7 193 -30 343 -175 378 -365 14 -76 15 -704 1 -777 -15 -79 -67 -177 -124 -235 -58 -57 -156 -109 -235 -124 -70 -13 -552 -13 -622 0 -30 5 -85 26 -124 45 -64 31 -111 75 -580 545 -280 282 -532 529 -558 551 l-49 39 -278 0 -278 0 -30 -25 c-52 -43 -53 -53 -50 -424 l3 -343 37 -34 c22 -20 49 -35 65 -35 100 -6 532 5 548 13 11 6 92 82 180 169 l160 159 113 -113 112 -113 -183 -182 c-262 -260 -269 -262 -677 -262 -141 0 -281 5 -311 10 -170 32 -310 164 -355 335 -10 37 -14 143 -14 423 0 351 1 376 21 434 52 156 176 269 332 303 75 16 530 20 621 5z"/></g></svg>`

// loadIcon returns icon data and content type. If iconPath is empty, returns
// the default TLTV logo SVG. If iconPath is a file, reads and validates it.
func loadIcon(iconPath string) ([]byte, string) {
	if iconPath == "" {
		return []byte(defaultIconSVG), "image/svg+xml"
	}
	data, err := os.ReadFile(iconPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: cannot read icon file: %v\n", err)
		os.Exit(1)
	}
	if len(data) > 1<<20 { // 1 MB
		fmt.Fprintf(os.Stderr, "error: icon file exceeds 1 MB\n")
		os.Exit(1)
	}
	ct := iconContentType(iconPath)
	if ct == "" {
		fmt.Fprintf(os.Stderr, "error: icon must be PNG, JPEG, or SVG\n")
		os.Exit(1)
	}
	return data, ct
}

// iconContentType returns the content type for an icon file based on extension.
func iconContentType(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".svg":
		return "image/svg+xml"
	default:
		return ""
	}
}

// iconExtension returns the file extension for an icon content type.
func iconExtension(contentType string) string {
	switch contentType {
	case "image/png":
		return "png"
	case "image/jpeg":
		return "jpg"
	case "image/svg+xml":
		return "svg"
	default:
		return "svg"
	}
}

// resolveErrorMessage converts raw Go errors from the resolve pipeline into
// user-friendly messages suitable for display in the portal UI.
func resolveErrorMessage(raw string) string {
	// Connection refused
	if strings.Contains(raw, "connection refused") {
		return "could not connect to host"
	}
	// DNS resolution failure
	if strings.Contains(raw, "no such host") || strings.Contains(raw, "Temporary failure in name resolution") || strings.Contains(raw, "server misbehaving") {
		return "host not found"
	}
	// Timeout
	if strings.Contains(raw, "context deadline exceeded") || strings.Contains(raw, "i/o timeout") {
		return "connection timed out"
	}
	// Local address blocked by SSRF protection
	if strings.Contains(raw, "blocked: local/private address") || strings.Contains(raw, "connects to local address") {
		return "local address blocked (use --local to allow)"
	}
	// Non-TLTV HTTP response (HTML 404 pages, etc.)
	if strings.Contains(raw, "invalid JSON") || strings.Contains(raw, "invalid character '<'") {
		return "not a TLTV node"
	}
	// HTTP error codes
	if strings.Contains(raw, "HTTP 404") {
		return "no TLTV channel found at this host"
	}
	if strings.Contains(raw, "HTTP 403") {
		return "access denied"
	}
	// TLS errors
	if strings.Contains(raw, "certificate") || strings.Contains(raw, "tls:") {
		return "TLS connection failed"
	}
	// Strip verbose Go error wrapping for remaining cases
	msg := raw
	// Remove "not a valid target and discovery failed on host: " prefix
	if idx := strings.Index(msg, "discovery failed on "); idx >= 0 {
		if colon := strings.Index(msg[idx:], ": "); colon >= 0 {
			msg = msg[idx+colon+2:]
		}
	}
	// Remove "request failed: Get "url": " wrapper
	if idx := strings.Index(msg, "request failed: "); idx >= 0 {
		msg = msg[idx+len("request failed: "):]
	}
	if idx := strings.Index(msg, "Get \""); idx >= 0 {
		if end := strings.Index(msg[idx:], "\": "); end >= 0 {
			msg = msg[idx+end+3:]
		}
	}
	// Remove "fetch metadata: " prefix
	msg = strings.TrimPrefix(msg, "fetch metadata: ")
	return msg
}

// viewerServer serves the local viewer page and proxies HLS to the upstream target.
type viewerServer struct {
	channelID   string
	channelName string
	streamDir   string // upstream stream directory URL (e.g., "https://host/tltv/v1/channels/id/")
	streamFile  string // manifest filename (e.g., "stream.m3u8")
	baseURL     string // upstream base URL (e.g., "https://host:port")
	token       string
	client      *http.Client
	metadata    map[string]interface{}
	guide       map[string]interface{} // may be nil
	tltvURI     string
}

type viewerSavedGuideEntry struct {
	Title     string `json:"title,omitempty"`
	Start     string `json:"start,omitempty"`
	End       string `json:"end,omitempty"`
	RelayFrom string `json:"relay_from,omitempty"`
}

type viewerSavedGuide struct {
	Entries []viewerSavedGuideEntry `json:"entries,omitempty"`
}

type viewerSavedChannel struct {
	ID       string            `json:"id,omitempty"`
	Name     string            `json:"name,omitempty"`
	URI      string            `json:"uri,omitempty"`
	IconData string            `json:"icon_data,omitempty"`
	Guide    *viewerSavedGuide `json:"guide,omitempty"`
}

type viewerSavedChannelsResponse struct {
	Enabled  bool                 `json:"enabled"`
	Channels []viewerSavedChannel `json:"channels,omitempty"`
}

type viewerSavedChannelsRequest struct {
	Channels []viewerSavedChannel `json:"channels"`
}

type viewerSavedChannelStore struct {
	mu       sync.Mutex
	path     string
	channels []viewerSavedChannel
}

// ---------- Shared Viewer Helpers ----------

// viewerConfig holds the parsed --viewer / --debug-viewer flag state.
type viewerConfig struct {
	mode     string // "", "viewer", "debug"
	selector string // channel ID or tltv:// URI (empty = auto-select first)
	fromCLI  bool   // true if --viewer or --debug-viewer appeared in CLI args (prevents config override)
}

// enabled returns true if any viewer mode is active.
func (vc viewerConfig) enabled() bool { return vc.mode != "" }

// parseViewerArg pre-processes --viewer and --debug-viewer from args before fs.Parse().
// --viewer enables the production viewer. --debug-viewer enables the diagnostic viewer.
// Both accept an optional channel selector (channel ID or tltv:// URI).
// Env vars: VIEWER=1 → production, DEBUG_VIEWER=1 → debug.
// Mutually exclusive — both set is a startup error (handled by the caller).
func parseViewerArg(args []string) (remaining []string, vc viewerConfig) {
	// Env var defaults
	if env := os.Getenv("VIEWER"); env != "" {
		switch env {
		case "0", "false":
			// explicitly disabled
		case "1", "true":
			vc.mode = "viewer"
		default:
			vc.mode = "viewer"
			vc.selector = env
		}
	}
	if env := os.Getenv("DEBUG_VIEWER"); env != "" {
		switch env {
		case "0", "false":
			// explicitly disabled
		case "1", "true":
			if vc.mode != "" {
				fmt.Fprintf(os.Stderr, "error: --viewer and --debug-viewer are mutually exclusive\n")
				os.Exit(1)
			}
			vc.mode = "debug"
		default:
			if vc.mode != "" {
				fmt.Fprintf(os.Stderr, "error: --viewer and --debug-viewer are mutually exclusive\n")
				os.Exit(1)
			}
			vc.mode = "debug"
			vc.selector = env
		}
	}

	// Scan CLI args
	remaining = make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		arg := args[i]

		// --viewer=value / -V=value
		if strings.HasPrefix(arg, "--viewer=") || strings.HasPrefix(arg, "-V=") {
			val := arg[strings.IndexByte(arg, '=')+1:]
			vc.mode = "viewer"
			vc.fromCLI = true
			if val != "1" && val != "true" {
				vc.selector = val
			} else {
				vc.selector = ""
			}
			continue
		}

		// --viewer / -V [optional channel selector]
		if arg == "--viewer" || arg == "-V" {
			vc.mode = "viewer"
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

		// --debug-viewer=value
		if strings.HasPrefix(arg, "--debug-viewer=") {
			val := arg[len("--debug-viewer="):]
			vc.mode = "debug"
			vc.fromCLI = true
			if val != "1" && val != "true" {
				vc.selector = val
			} else {
				vc.selector = ""
			}
			continue
		}

		// --debug-viewer [optional channel selector]
		if arg == "--debug-viewer" {
			vc.mode = "debug"
			vc.fromCLI = true
			vc.selector = ""
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

	// Check mutual exclusion (only possible if CLI set one mode and env set another).
	// If CLI is present, it takes priority — already handled by overwriting mode above.
	// The problematic case is both env vars set, handled above in the env block.

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

// applyViewerConfig applies the "viewer" and "debug_viewer" fields from a
// daemon config file. Only applies if --viewer/--debug-viewer was not set on
// the CLI. Config accepts:
//
//	"viewer": true            → production viewer
//	"viewer": "TVabc..."      → production viewer with selector
//	"debug_viewer": true      → debug viewer
//	"debug_viewer": "TVabc..."→ debug viewer with selector
//
// Mutually exclusive — both set logs a warning and uses the production viewer.
func applyViewerConfig(vc *viewerConfig, cfg map[string]interface{}) {
	if vc.fromCLI {
		return
	}

	applyField := func(key, mode string) bool {
		val, ok := cfg[key]
		if !ok {
			return false
		}
		switch v := val.(type) {
		case bool:
			if v {
				vc.mode = mode
				vc.selector = ""
				return true
			}
		case string:
			if v == "" || v == "0" || v == "false" {
				return false
			}
			vc.mode = mode
			if v != "1" && v != "true" {
				vc.selector = v
			} else {
				vc.selector = ""
			}
			return true
		}
		return false
	}

	hasViewer := applyField("viewer", "viewer")
	hasDebug := applyField("debug_viewer", "debug")

	if hasViewer && hasDebug {
		// Mutual exclusion: production wins, log warning.
		applyField("viewer", "viewer")
		logErrorf("config: both viewer and debug_viewer set; using viewer")
	}
}

// viewerChannelRef holds per-channel data for the viewer's guide grid.
// Unlike ChannelRef (protocol type), this carries viewer-only fields that
// are not part of the protocol wire format.
type viewerChannelRef struct {
	ID       string
	Name     string
	Guide    []byte // raw signed guide JSON (nil if not available)
	IconPath string // protocol icon path (e.g. "/tltv/v1/channels/{id}/icon.svg")
}

type viewerRouteOptions struct {
	authToken string
	private   bool
	title     string // nav bar label (default: "" for embedded, "viewer" for portal)
	noFooter  bool   // hide the footer with TLTV links
}

// addViewerDisplayFlags registers --viewer-title/-e and --no-viewer-footer/-Z.
// Returns pointers to the parsed values. These are cosmetic-only flags that
// modify the web player UI; they are not part of the protocol.
func addViewerDisplayFlags(fs *flag.FlagSet) (title *string, noFooter *bool) {
	defaultTitle := os.Getenv("VIEWER_TITLE")
	title = fs.String("viewer-title", defaultTitle, "nav bar label text")
	fs.StringVar(title, "e", defaultTitle, "alias for --viewer-title")

	defaultNoFooter := os.Getenv("VIEWER_FOOTER") == "0"
	noFooter = new(bool)
	*noFooter = defaultNoFooter
	fs.BoolVar(noFooter, "no-viewer-footer", defaultNoFooter, "hide the footer links")
	fs.BoolVar(noFooter, "Z", defaultNoFooter, "alias for --no-viewer-footer")
	return
}

// applyViewerDisplayConfig adds viewer cosmetic config (title, footer) to the
// /api/info response so the JS can apply it on load.
func (o viewerRouteOptions) applyDisplayConfig(info map[string]interface{}) {
	if o.title != "" {
		info["viewer_title"] = o.title
	}
	if o.noFooter {
		info["viewer_footer"] = false
	}
}

func (o viewerRouteOptions) authenticate(w http.ResponseWriter, r *http.Request) bool {
	if o.private {
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Cache-Control", "private, no-store")
	}
	if o.authToken == "" {
		return true
	}
	return checkRequestToken(w, r, o.authToken)
}

// debugViewerRoutes registers the debug viewer HTML, static assets, and /api/info
// on an existing mux. infoFn is called per-request to get current channel state.
//
// Routes registered:
//
//	GET /{$}         → debug viewer HTML (exact root path only)
//	GET /favicon.svg → SVG icon
//	GET /hls.min.js  → vendored HLS.js
//	GET /api/info    → JSON channel info
//
// Protocol endpoints (/.well-known/tltv, /tltv/v1/...) registered separately
// by the daemon take routing priority over the "/" subtree pattern.
func debugViewerRoutes(mux *http.ServeMux, infoFn func(channelID string) map[string]interface{}, channelsFn func() []viewerChannelRef, opts ...viewerRouteOptions) {
	var opt viewerRouteOptions
	if len(opts) > 0 {
		opt = opts[0]
	}

	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		if !opt.authenticate(w, r) {
			return
		}
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
		if !opt.authenticate(w, r) {
			return
		}
		chID := r.URL.Query().Get("channel")
		info := infoFn(chID)
		if channelsFn != nil {
			chList := channelsFn()
			if len(chList) > 1 {
				arr := make([]interface{}, len(chList))
				for i, ch := range chList {
					entry := map[string]interface{}{"id": ch.ID, "name": ch.Name}
					if ch.IconPath != "" {
						entry["icon_path"] = ch.IconPath
					}
					if ch.Guide != nil {
						var g map[string]interface{}
						if json.Unmarshal(ch.Guide, &g) == nil {
							entry["guide"] = g
						}
					}
					arr[i] = entry
				}
				info["channels"] = arr
			}
		}
		opt.applyDisplayConfig(info)
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

// viewerListenIsLoopbackOnly reports whether the standalone viewer is bound
// only to loopback. Wildcard binds like ":9000" or "0.0.0.0:9000" are NOT
// loopback-only, even though they omit or use an unspecified host.
func viewerListenIsLoopbackOnly(listen string) bool {
	host, _, err := net.SplitHostPort(listen)
	if err != nil || host == "" {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// viewerResolveToken resolves the effective token for a viewer tune target.
// The target's embedded tltv:// token is used by default; an explicit token
// override wins when provided.
func viewerResolveToken(target, explicit string) string {
	tok := extractToken(target)
	if explicit != "" {
		tok = explicit
	}
	return tok
}

func stripViewerSavedToken(uri string) string {
	uri = strings.TrimSpace(uri)
	if uri == "" {
		return ""
	}
	if strings.HasPrefix(uri, "tltv://") {
		if parsed, err := parseTLTVUri(uri); err == nil {
			return formatTLTVUri(parsed.ChannelID, parsed.Hints, "")
		}
	}
	return uri
}

func sanitizeViewerSavedChannels(channels []viewerSavedChannel) []viewerSavedChannel {
	if len(channels) > 100 {
		channels = channels[:100]
	}
	out := make([]viewerSavedChannel, 0, len(channels))
	for _, ch := range channels {
		sc := viewerSavedChannel{
			ID:   strings.TrimSpace(ch.ID),
			Name: strings.TrimSpace(ch.Name),
			URI:  stripViewerSavedToken(ch.URI),
		}
		if sc.ID == "" && sc.URI != "" {
			if strings.HasPrefix(sc.URI, "tltv://") {
				if parsed, err := parseTLTVUri(sc.URI); err == nil {
					sc.ID = parsed.ChannelID
				}
			} else if strings.HasPrefix(sc.URI, "TV") {
				if idx := strings.IndexByte(sc.URI, '@'); idx > 0 {
					sc.ID = sc.URI[:idx]
				}
			}
		}
		if len(ch.IconData) <= 1<<20 && strings.HasPrefix(ch.IconData, "data:") {
			sc.IconData = ch.IconData
		}
		if ch.Guide != nil && len(ch.Guide.Entries) > 0 {
			entries := ch.Guide.Entries
			if len(entries) > 50 {
				entries = entries[:50]
			}
			sc.Guide = &viewerSavedGuide{Entries: make([]viewerSavedGuideEntry, 0, len(entries))}
			for _, entry := range entries {
				sc.Guide.Entries = append(sc.Guide.Entries, viewerSavedGuideEntry{
					Title:     strings.TrimSpace(entry.Title),
					Start:     strings.TrimSpace(entry.Start),
					End:       strings.TrimSpace(entry.End),
					RelayFrom: strings.TrimSpace(entry.RelayFrom),
				})
			}
			if len(sc.Guide.Entries) == 0 {
				sc.Guide = nil
			}
		}
		if sc.ID == "" && sc.URI == "" {
			continue
		}
		out = append(out, sc)
	}
	return out
}

func loadViewerSavedChannels(path string) ([]viewerSavedChannel, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return nil, nil
	}
	var channels []viewerSavedChannel
	if err := json.Unmarshal(data, &channels); err == nil {
		return sanitizeViewerSavedChannels(channels), nil
	}
	var uris []string
	if err := json.Unmarshal(data, &uris); err == nil {
		channels = make([]viewerSavedChannel, 0, len(uris))
		for _, uri := range uris {
			channels = append(channels, viewerSavedChannel{URI: uri})
		}
		return sanitizeViewerSavedChannels(channels), nil
	}
	return nil, fmt.Errorf("invalid saved channels JSON")
}

func newViewerSavedChannelStore(path string) (*viewerSavedChannelStore, error) {
	channels, err := loadViewerSavedChannels(path)
	if err != nil {
		return nil, err
	}
	return &viewerSavedChannelStore{path: path, channels: channels}, nil
}

func (s *viewerSavedChannelStore) list() []viewerSavedChannel {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]viewerSavedChannel, len(s.channels))
	copy(out, s.channels)
	return out
}

func (s *viewerSavedChannelStore) save(channels []viewerSavedChannel) error {
	channels = sanitizeViewerSavedChannels(channels)
	data, err := json.MarshalIndent(channels, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	dir := filepath.Dir(s.path)
	if dir != "." {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return err
		}
	}
	tmp, err := os.CreateTemp(dir, ".saved-channels-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, s.path); err != nil {
		return err
	}
	s.mu.Lock()
	s.channels = channels
	s.mu.Unlock()
	return nil
}

func registerViewerSavedChannelRoutes(mux *http.ServeMux, store *viewerSavedChannelStore) {
	mux.HandleFunc("GET /api/saved-channels", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		resp := viewerSavedChannelsResponse{Enabled: store != nil}
		if store != nil {
			resp.Channels = store.list()
		}
		json.NewEncoder(w).Encode(resp)
	})

	mux.HandleFunc("POST /api/saved-channels", func(w http.ResponseWriter, r *http.Request) {
		if store == nil {
			jsonError(w, "saved_channels_disabled", http.StatusNotFound)
			return
		}
		var req viewerSavedChannelsRequest
		dec := json.NewDecoder(io.LimitReader(r.Body, 10<<20))
		if err := dec.Decode(&req); err != nil {
			jsonError(w, "invalid_json", http.StatusBadRequest)
			return
		}
		if err := store.save(req.Channels); err != nil {
			jsonError(w, "save_failed", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		json.NewEncoder(w).Encode(viewerSavedChannelsResponse{Enabled: true, Channels: store.list()})
	})
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

	savedChannelsPath := fs.String("saved-channels", os.Getenv("SAVED_CHANNELS"), "JSON file for saved portal channels")
	fs.StringVar(savedChannelsPath, "E", os.Getenv("SAVED_CHANNELS"), "alias for --saved-channels")

	debugMode := fs.Bool("debug", false, "use the diagnostic debug viewer instead of the production viewer")
	viewerTitle, viewerNoFooter := addViewerDisplayFlags(fs)

	logLevel, logFormat, logFile := addLogFlags(fs)
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Start a local web viewer for a TLTV channel\n\n")
		fmt.Fprintf(os.Stderr, "Usage: tltv viewer [flags] [target]\n\n")
		fmt.Fprintf(os.Stderr, "Target can be a tltv:// URI, compact ID@host, or bare hostname.\n")
		fmt.Fprintf(os.Stderr, "If no target is given, starts as a portal with a tune box.\n\n")
		fmt.Fprintf(os.Stderr, "  tltv viewer demo.timelooptv.org\n")
		fmt.Fprintf(os.Stderr, "  tltv viewer TVabc...@demo.timelooptv.org:443\n")
		fmt.Fprintf(os.Stderr, "  tltv viewer \"tltv://TVabc...@demo.timelooptv.org:443\"\n")
		fmt.Fprintf(os.Stderr, "  tltv viewer --token secret TVabc...@localhost:8000\n")
		fmt.Fprintf(os.Stderr, "  tltv viewer   (portal mode — tune from the browser)\n\n")
		fmt.Fprintf(os.Stderr, "Flags:\n")
		fmt.Fprintf(os.Stderr, "  -t, --token string       access token for private channels\n")
		fmt.Fprintf(os.Stderr, "  -l, --listen string      listen address (default: 127.0.0.1:9000)\n")
		fmt.Fprintf(os.Stderr, "  -E, --saved-channels FILE  JSON file for saved portal channels\n")
		fmt.Fprintf(os.Stderr, "      --debug              use diagnostic debug viewer\n")
		fmt.Fprintf(os.Stderr, "  -e, --viewer-title TEXT   nav bar label text\n")
		fmt.Fprintf(os.Stderr, "  -Z, --no-viewer-footer   hide the footer links\n\n")
		fmt.Fprintf(os.Stderr, "Environment variables: LISTEN, SAVED_CHANNELS, VIEWER_TITLE, VIEWER_FOOTER=0,\n")
		fmt.Fprintf(os.Stderr, "  LOG_LEVEL, LOG_FORMAT, LOG_FILE.\n")
		fmt.Fprintf(os.Stderr, "Flags override env vars.\n")
	}
	fs.Parse(args)

	// Debug mode requires a target (no portal mode).
	if *debugMode && fs.NArg() < 1 {
		fmt.Fprintf(os.Stderr, "error: --debug requires a target argument\n")
		fs.Usage()
		os.Exit(1)
	}

	if err := setupLogging(*logLevel, *logFormat, *logFile, "viewer"); err != nil {
		fatal("%v", err)
	}

	client := newClient(flagInsecure)
	// resolveClient is used by /api/resolve for user-supplied targets.
	// When listening on a non-local address, use SSRF-safe client to prevent
	// the portal from being used to probe internal networks (§11B).
	// When listening on localhost (default), SSRF protection is unnecessary
	// since only the local operator can access the portal.
	resolveClient := client
	if !viewerListenIsLoopbackOnly(*listen) {
		resolveClient = newSSRFSafeClient(flagInsecure)
	}

	var savedStore *viewerSavedChannelStore
	if *savedChannelsPath != "" {
		var err error
		savedStore, err = newViewerSavedChannelStore(*savedChannelsPath)
		if err != nil {
			fatal("load saved channels: %v", err)
		}
	}

	// If a target is given, resolve it at startup.
	var srv *viewerServer
	if fs.NArg() >= 1 {
		target := fs.Arg(0)

		// Token: flag overrides URI-embedded token
		tok := viewerResolveToken(target, *token)

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

		if err := checkAccessMode(doc); err != nil {
			fatal("%v", err)
		}

		// Fetch guide (non-fatal)
		var guide map[string]interface{}
		guide, err = client.FetchGuide(host, channelID, tok)
		if err != nil {
			logInfof("guide not available: %v", err)
		} else if err := verifyDocument(guide, channelID); err != nil {
			logInfof("guide verification failed: %v", err)
			guide = nil
		}

		streamPath := getString(doc, "stream")
		if streamPath == "" {
			fatal("metadata has no stream path")
		}

		base := client.baseURL(host)
		streamDir := base + path.Dir(streamPath) + "/"
		streamFile := path.Base(streamPath)

		tltvURI := formatTLTVUri(channelID, []string{host}, "")

		srv = &viewerServer{
			channelID:   channelID,
			channelName: getString(doc, "name"),
			streamDir:   streamDir,
			streamFile:  streamFile,
			baseURL:     base,
			token:       tok,
			client:      client.http,
			metadata:    doc,
			guide:       guide,
			tltvURI:     tltvURI,
		}
	}

	// Warn if listen address is non-local
	if !viewerListenIsLoopbackOnly(*listen) {
		logInfof("WARNING: listening on non-local address %s", *listen)
	}

	mux := http.NewServeMux()

	// /api/info — serves current channel state (nil-safe for portal mode).
	// Uses local proxy URLs only — never exposes token-bearing upstream URLs
	// to the browser (§11C: standalone token leakage prevention).
	infoFn := func(_ string) map[string]interface{} {
		if srv == nil {
			return map[string]interface{}{"portal": true}
		}
		info := map[string]interface{}{
			"channel_id":   srv.channelID,
			"channel_name": srv.channelName,
			"tltv_uri":     srv.tltvURI,
			"stream_file":  srv.streamFile,
			"stream_src":   "/stream/" + srv.streamFile,
			"base_url":     srv.baseURL,
			"verified":     true,
			"metadata":     srv.metadata,
		}
		// Icon URL via local proxy (avoids cross-origin + wrong content-type issues).
		if getString(srv.metadata, "icon") != "" {
			info["icon_url"] = "/api/icon"
		}
		if srv.guide != nil {
			info["guide"] = srv.guide
		}
		return info
	}

	standaloneOpts := viewerRouteOptions{title: *viewerTitle, noFooter: *viewerNoFooter}
	if *debugMode {
		debugViewerRoutes(mux, infoFn, nil, standaloneOpts)
	} else {
		// Standalone portal is explicitly local/single-user (§11A).
		// Process-global state — one tune changes playback for all clients.
		standalonePortalRoutes(mux, infoFn, nil, standaloneOpts)
	}
	registerViewerSavedChannelRoutes(mux, savedStore)

	// /api/resolve — server-side federated resolution for the portal tune box.
	// Accepts ?target=<host|id@host|tltv://...> and returns resolved channel info.
	// Uses SSRF-safe client to prevent internal network access (§11B).
	mux.HandleFunc("GET /api/resolve", func(w http.ResponseWriter, r *http.Request) {
		target := r.URL.Query().Get("target")
		if target == "" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(400)
			json.NewEncoder(w).Encode(map[string]string{"error": "target parameter required"})
			return
		}

		tok := viewerResolveToken(target, r.URL.Query().Get("token"))

		chID, host, err := parseTargetOrDiscover(target, resolveClient)
		if err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(502)
			json.NewEncoder(w).Encode(map[string]string{"error": resolveErrorMessage(err.Error())})
			return
		}

		doc, err := resolveClient.FetchMetadata(host, chID, tok)
		if err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(502)
			json.NewEncoder(w).Encode(map[string]string{"error": resolveErrorMessage("fetch metadata: " + err.Error())})
			return
		}
		if err := verifyDocument(doc, chID); err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(502)
			json.NewEncoder(w).Encode(map[string]string{"error": "verification failed: " + err.Error()})
			return
		}

		// Fetch guide (non-fatal)
		var guideDoc map[string]interface{}
		guideDoc, err = resolveClient.FetchGuide(host, chID, tok)
		if err == nil {
			if verifyErr := verifyDocument(guideDoc, chID); verifyErr != nil {
				guideDoc = nil
			}
		}

		streamPath := getString(doc, "stream")
		base := client.baseURL(host)
		streamDir := base + path.Dir(streamPath) + "/"
		streamFile := path.Base(streamPath)

		tltvURI := formatTLTVUri(chID, []string{host}, "")

		// Update the server state for stream proxying.
		srv = &viewerServer{
			channelID:   chID,
			channelName: getString(doc, "name"),
			streamDir:   streamDir,
			streamFile:  streamFile,
			baseURL:     base,
			token:       tok,
			client:      client.http,
			metadata:    doc,
			guide:       guideDoc,
			tltvURI:     tltvURI,
		}
		logInfof("tuned to %s (%s) via %s", chID, getString(doc, "name"), host)

		// Use local proxy URLs — never expose token-bearing upstream URLs
		// to the browser (§11C: standalone token leakage prevention).
		result := map[string]interface{}{
			"channel_id":   chID,
			"channel_name": getString(doc, "name"),
			"tltv_uri":     tltvURI,
			"stream_file":  streamFile,
			"stream_src":   "/stream/" + streamFile,
			"base_url":     base,
			"verified":     true,
			"metadata":     doc,
		}
		// Icon URL via local proxy (avoids cross-origin + wrong content-type issues).
		if getString(doc, "icon") != "" {
			result["icon_url"] = "/api/icon"
		}
		if guideDoc != nil {
			result["guide"] = guideDoc
		}

		// Discover all channels from the host so the portal can add them all.
		// Fetch metadata+guide for each so the portal can show icons and guide entries.
		// Non-fatal — only the primary channel is required.
		// Skip when the client already has sibling data (e.g. clicking a saved channel).
		skipDiscover := r.URL.Query().Get("skip_discover") == "1"
		if !skipDiscover {
			if nodeInfo, discErr := resolveClient.FetchNodeInfo(host); discErr == nil {
				var discovered []map[string]interface{}
				allRefs := make([]ChannelRef, 0, len(nodeInfo.Channels)+len(nodeInfo.Relaying))
				allRefs = append(allRefs, nodeInfo.Channels...)
				allRefs = append(allRefs, nodeInfo.Relaying...)
				for _, ref := range allRefs {
					if ref.ID == chID {
						continue // skip the primary — already in the response
					}
					entry := map[string]interface{}{
						"id":   ref.ID,
						"name": ref.Name,
						"uri":  formatTLTVUri(ref.ID, []string{host}, ""),
					}
					// Try to fetch metadata for icon — encode as data-URI to avoid CORS
					if sibDoc, sibErr := resolveClient.FetchMetadata(host, ref.ID, tok); sibErr == nil {
						if verifyDocument(sibDoc, ref.ID) == nil {
							if icon := getString(sibDoc, "icon"); icon != "" {
								iconURL := base + icon
								if tok != "" {
									iconURL += "?token=" + tok
								}
								if req, reqErr := http.NewRequest("GET", iconURL, nil); reqErr == nil {
									req.Header.Set("User-Agent", "tltv-cli/"+version)
									if resp, respErr := resolveClient.http.Do(req); respErr == nil {
										if iconBody, readErr := io.ReadAll(io.LimitReader(resp.Body, 1*1024*1024)); readErr == nil && resp.StatusCode == 200 {
											ct := iconContentType(icon)
											entry["icon_data"] = "data:" + ct + ";base64," + base64.StdEncoding.EncodeToString(iconBody)
										}
										resp.Body.Close()
									}
								}
							}
						}
					}
					// Try to fetch guide for program entries
					if sibGuide, sibErr := resolveClient.FetchGuide(host, ref.ID, tok); sibErr == nil {
						if verifyDocument(sibGuide, ref.ID) == nil {
							entry["guide"] = sibGuide
						}
					}
					discovered = append(discovered, entry)
				}
				if len(discovered) > 0 {
					result["discovered_channels"] = discovered
				}
			}
		} // end skipDiscover

		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		enc := json.NewEncoder(w)
		enc.SetEscapeHTML(false)
		enc.Encode(result)
	})

	// Icon proxy — proxies channel icon from upstream with correct content-type.
	mux.HandleFunc("GET /api/icon", func(w http.ResponseWriter, r *http.Request) {
		if srv == nil {
			http.NotFound(w, r)
			return
		}
		iconPath := getString(srv.metadata, "icon")
		if iconPath == "" {
			http.NotFound(w, r)
			return
		}
		iconURL := srv.baseURL + iconPath
		if srv.token != "" {
			iconURL += "?token=" + srv.token
		}
		req, err := http.NewRequestWithContext(r.Context(), "GET", iconURL, nil)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		req.Header.Set("User-Agent", "tltv-cli/"+version)
		resp, err := srv.client.Do(req)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(io.LimitReader(resp.Body, 1*1024*1024))
		if err != nil || resp.StatusCode != 200 {
			http.NotFound(w, r)
			return
		}
		// Detect content-type from the icon path extension (upstream may serve wrong type).
		ct := iconContentType(iconPath)
		w.Header().Set("Content-Type", ct)
		w.Header().Set("Cache-Control", "max-age=3600")
		w.Write(body)
	})

	// Stream proxy — proxies HLS to the currently-tuned upstream.
	mux.HandleFunc("/stream/", func(w http.ResponseWriter, r *http.Request) {
		if srv == nil {
			http.Error(w, "No channel tuned", http.StatusServiceUnavailable)
			return
		}
		srv.handleStream(w, r)
	})

	httpSrv := &http.Server{
		Addr:              *listen,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	if srv != nil {
		logInfof("viewer: http://%s (tuned to %s)", displayListenAddr(*listen), srv.channelName)
	} else {
		logInfof("viewer portal: http://%s", displayListenAddr(*listen))
	}

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

	// Set content type and cache headers.
	// Standalone viewer proxy uses no-store to prevent browser from serving
	// cached segments from a previous channel when switching channels on the
	// same node (segment names like seg1028.ts collide across channels).
	w.Header().Set("Content-Type", streamContentType(subPath))
	if strings.HasSuffix(subPath, ".m3u8") {
		body = rewriteManifest(upstreamURL, body, "")
	}
	w.Header().Set("Cache-Control", "no-store")

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
html{background:#000;min-height:100%}
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
    <img id="ch-icon" style="display:none;height:20px;width:20px;vertical-align:middle;margin-right:6px;border-radius:3px" alt="">
    <span id="cn" class="cn"></span>
    <span id="ps" class="sep" style="display:none">/</span>
    <span id="prg" class="prg"></span>
    <span id="rb" class="cb" style="display:none;cursor:default;font-size:.65rem;padding:1px 6px;margin-left:6px">relay</span>
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
  <div id="std-variants"></div>
  <div id="std-tracks"></div>
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
  <div style="height:4rem"></div>
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
function kvHtml(p,k,vh){
  var d=document.createElement('div');d.className='dr';
  d.innerHTML='<div class="di"><span>'+esc(k)+' </span>'+vh+'</div>';
  p.appendChild(d);
}

var viewerToken=(new URLSearchParams(window.location.search)).get('token')||'';
function withToken(u){
  if(!viewerToken||!u||u.indexOf('token=')!==-1) return u;
  return u+(u.indexOf('?')!==-1?'&':'?')+'token='+encodeURIComponent(viewerToken);
}

  var infoUrl='/api/info'+window.location.search;
fetch(infoUrl).then(function(r){return r.json()}).then(function(d){
  inf=d;
  var base=d.base_url||window.location.origin;
  var m=d.metadata||{};
  document.getElementById('cn').textContent=d.channel_name;
  var tltvUri=d.tltv_uri||('tltv://'+d.channel_id+'@'+location.host);
  document.getElementById('uri').textContent=tltvUri;
  document.title=d.channel_name+' \u2014 tltv viewer';

  // === Origin check from signed metadata (§11) ===
  var origins=m.origins||[];
  var _isOrg=false,_connHost='';
  try{var _u=new URL(base);_connHost=_u.port===''||_u.port==='443'?_u.hostname:_u.host;
    origins.forEach(function(o){if(o.replace(/:443$/,'')===_connHost||o===_connHost+':443'||o===_connHost)_isOrg=true})}catch(e){}
  var _hasOrigins=origins.length>0;
  // Relay badge in controls bar (from signed origins, not discovery)
  if(_hasOrigins&&!_isOrg){
    var rb=document.getElementById('rb');
    if(rb){rb.style.display='';
      // Check if discovery claims origin — if so, it's spoofed
      fetch(base+'/.well-known/tltv').then(function(r){return r.json()}).then(function(n){
        var claimed=false;
        (n.channels||[]).forEach(function(ch){if(ch.id===d.channel_id)claimed=true});
        if(claimed){rb.style.borderColor='#a16207';rb.style.color='#fbbf24'}
      }).catch(function(){})
    }
  }

  // === 1. Channel section: curated fields then remaining ===
  var chd=document.getElementById('chd');
  kvHtml(chd,'verified',d.verified?'<span class="ok">\u2713 Signature valid</span>':'<span class="wn">? Unknown</span>');
  if(m.name) kv(chd,'name',m.name,false);
  kv(chd,'uri',tltvUri,false);
  kv(chd,'status',m.status||'active',false);
  kv(chd,'access',m.access||'public',false);
  if(m.description) kv(chd,'description',m.description,false);
  if(m.language) kv(chd,'language',m.language,false);
  if(m.timezone) kv(chd,'timezone',m.timezone,false);
  if(m.tags&&m.tags.length) kv(chd,'tags',m.tags.join(', '),false);
  if(m.stream) kv(chd,'stream',base+m.stream,true);
  if(m.guide){
    kv(chd,'guide',base+m.guide,true);
    kv(chd,'xmltv',base+m.guide.replace('guide.json','guide.xml'),true);
  }
  if(m.icon){
    var iconUrl=m.icon.charAt(0)==='/'?base+m.icon:m.icon;
    kv(chd,'icon',iconUrl,true);
    var ic=document.getElementById('ch-icon');
    if(ic){ic.src=iconUrl;ic.style.display='inline-block';}
  }
  if(m.origins&&Array.isArray(m.origins)&&m.origins.length>0) kv(chd,'origins',m.origins.join(', '),false);
  if(m.updated) kv(chd,'updated',m.updated,false);
  if(m.seq!==undefined) kv(chd,'seq',String(m.seq),false);
  // Dump remaining keys
  var skip={v:1,signature:1,id:1,name:1,status:1,access:1,stream:1,guide:1,updated:1,seq:1,
    description:1,language:1,timezone:1,tags:1,icon:1,origins:1,on_demand:1};
  Object.keys(m).sort().forEach(function(k){
    if(skip[k]) return;
    var val=m[k];
    if(Array.isArray(val)) val=val.join(', ');
    else if(val!==null&&typeof val==='object') val=JSON.stringify(val);
    else val=String(val);
    kv(chd,k,val,val.indexOf('http')===0);
  });

  // === 2. Stream section: curated fields ===
  var std=document.getElementById('std');
  kv(std,'status','connecting',false);
  if(m.stream) kv(std,'stream',withToken(base+m.stream),true);
  if(d.stream_url) kv(std,'url',d.stream_url,true);

  // === 3. Guide section ===
  var gdd=document.getElementById('gdd');
  var gd=document.getElementById('gd');
  if(d.guide){
    kvHtml(gdd,'verified',d.verified?'<span class="ok">\u2713 Signature valid</span>':'<span class="wn">? Unknown</span>');
    if(m.guide) kv(gdd,'guide',withToken(base+m.guide),true);
    if(m.guide) kv(gdd,'xmltv',withToken(base+m.guide.replace('guide.json','guide.xml')),true);
    if(d.guide.from) kv(gdd,'from',d.guide.from,false);
    if(d.guide.until) kv(gdd,'until',d.guide.until,false);
    var entries=d.guide.entries||[];
    kv(gdd,'entries',String(entries.length),false);
    if(d.guide.updated) kv(gdd,'updated',d.guide.updated,false);
    if(d.guide.seq!==undefined) kv(gdd,'seq',String(d.guide.seq),false);
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
        var rf=e.relay_from?' <span class="gt">[relay: '+esc(e.relay_from)+']</span>':'';
        div.innerHTML=mk+'<span class="gt">'+esc(s)+' \u2013 '+esc(n)+'</span>  '+esc(e.title||'')+cat+rf;
        gd.appendChild(div);
      });
    }
  } else {
    gd.innerHTML='<div class="ge" style="color:#666">no guide data</div>';
  }

  // === Channel selector (multi-channel) ===
  if(d.channels&&d.channels.length>1){
    var sel=document.createElement('select');
    sel.className='cb';
    sel.style.cssText='margin-left:8px;appearance:none;-webkit-appearance:none;padding:2px 8px';
    d.channels.forEach(function(ch){
      var opt=document.createElement('option');
      opt.value=ch.id;opt.textContent=ch.name;
      if(ch.id===d.channel_id) opt.selected=true;
      sel.appendChild(opt);
    });
    sel.onchange=function(){
      var params=new URLSearchParams(window.location.search);
      params.set('channel',sel.value);
      window.location.search='?'+params.toString();
    };
    document.getElementById('cn').parentNode.appendChild(sel);
  }

  initPlayer(withToken(d.stream_src));

  // === 4. Node section (uses signed origins to verify origin claims) ===
  fetch(base+'/.well-known/tltv').then(function(r){return r.json()}).then(function(n){
    var nd=document.getElementById('nd');
    nd.innerHTML='';
    var ver=(n.versions&&n.versions.length)?n.versions[0]:'?';
    kv(nd,'protocol',n.protocol+' protocol v'+ver,false);
    function chAnn(id,disc){
      if(id!==d.channel_id||!_hasOrigins)return'';
      if(_isOrg)return' <span class="ok">(origin)</span>';
      var hint=origins.length?' \u2014 real origin is '+esc(origins[0]):'';
      if(disc==='channel')return' <span class="wn">(spoofed origin'+hint+')</span>';
      return' <span class="gt">(relay'+hint+')</span>';
    }
    var chLabel=_hasOrigins&&!_isOrg?'Channels':'Origin Channels';
    if(n.channels&&n.channels.length){
      var hdr=document.createElement('div');hdr.className='ge';hdr.style.color='#666';hdr.style.marginTop='6px';
      hdr.textContent=chLabel+' ('+n.channels.length+')';nd.appendChild(hdr);
      n.channels.forEach(function(ch){
        var mk=(ch.id===d.channel_id)?'<span class="ok">> </span>':'  ';
        var div=document.createElement('div');div.className='ge';
        div.innerHTML=mk+'<span class="uri">'+esc(ch.id)+'</span>  <span class="gt">'+esc(ch.name)+'</span>'+chAnn(ch.id,'channel');
        nd.appendChild(div);
      });
    }
    if(n.relaying&&n.relaying.length){
      var hdr=document.createElement('div');hdr.className='ge';hdr.style.color='#666';hdr.style.marginTop='6px';
      hdr.textContent='Relay Channels ('+n.relaying.length+')';nd.appendChild(hdr);
      n.relaying.forEach(function(ch){
        var mk=(ch.id===d.channel_id)?'<span class="ok">> </span>':'  ';
        var div=document.createElement('div');div.className='ge';
        div.innerHTML=mk+'<span class="uri">'+esc(ch.id)+'</span>  <span class="gt">'+esc(ch.name)+'</span>'+chAnn(ch.id,'relay');
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

  var varContainer=document.getElementById('std-variants');
  var trackContainer=document.getElementById('std-tracks');
  // Build stream section once with stable elements — values updated in-place
  function buildStreamUI(){
    std.innerHTML='';
    kv(std,'status','connecting',false);
    var sUrl=inf.stream_url||(inf.metadata&&inf.metadata.stream?(inf.base_url||location.origin)+inf.metadata.stream:'')||inf.stream_src;
    if(sUrl) kv(std,'stream',withToken(sUrl),true);
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
    varContainer.innerHTML='';
    trackContainer.innerHTML='';
  }
  // Build a sub-section with border separator (shared by variants, audio tracks, subtitle tracks)
  function buildSubSection(container,label,fields){
    var wrap=document.createElement('div');wrap.style.cssText='margin-top:8px;padding-top:8px;border-top:1px solid #222';
    var hdr=document.createElement('div');hdr.className='ge';hdr.style.cssText='color:#666;margin-bottom:2px';
    hdr.innerHTML='<span style="color:#666;font-size:.7rem;text-transform:uppercase;letter-spacing:.06em">'+esc(label)+'</span>';
    wrap.appendChild(hdr);
    var sec=document.createElement('div');sec.className='db';
    fields.forEach(function(f){kv(sec,f[0],f[1],f[2])});
    wrap.appendChild(sec);
    container.appendChild(wrap);
    return sec;
  }
  // Build a per-variant stream sub-section
  function buildVariantSection(lv,i,isActive){
    var label=lv.height?lv.height+'p':'level '+i;
    var bw=lv.bitrate?Math.round(lv.bitrate/1000)+'k':'?';
    var heading='variant '+label+(isActive?' (active)':'');
    var varUrl=(lv.url&&lv.url.length)?lv.url[0]:(lv.uri||'');
    var fields=[];
    if(varUrl) fields.push(['stream',varUrl,true]);
    fields.push(['resolution',(lv.width||'?')+'x'+(lv.height||'?'),false]);
    fields.push(['bandwidth',bw,false]);
    if(lv.codecs) fields.push(['codecs',lv.codecs,false]);
    var sec=buildSubSection(varContainer,heading,fields);
    sec.id='sv_var_'+i;
  }
  // Build audio/subtitle track sub-sections (same visual treatment as variants)
  function buildTrackSections(type,tracks){
    tracks.forEach(function(tk,i){
      var name=tk.name||tk.lang||('track '+(i+1));
      var heading=type+' \u2014 '+name;
      var fields=[];
      var url=tk.url||(tk.details&&tk.details.url)||'';
      if(url) fields.push(['stream',url,true]);
      if(tk.lang) fields.push(['language',tk.lang,false]);
      if(tk.default) fields.push(['default','yes',false]);
      buildSubSection(trackContainer,heading,fields);
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
      if(e){e.className='ok';e.textContent='\u2713 live'}
      // Stream type detection — use codec info from master playlist when available,
      // fall back to video element dimensions after playback starts.
      var _hasVideo=false,_hasAudio=false,_typeDetected=false;
      if(hls.levels&&hls.levels.length>0){
        var lv0=hls.levels[0];
        if(lv0.videoCodec) _hasVideo=true;
        if(lv0.audioCodec) _hasAudio=true;
        // Explicit audio-only: has audio codec but no video codec in master playlist
        if(lv0.codecs&&!lv0.videoCodec&&(lv0.audioCodec||/mp4a|aac/i.test(lv0.codecs))){_hasAudio=true;_typeDetected=true}
        // Master playlist with height means video is present
        if(lv0.height>0) _hasVideo=true;
        if(_hasVideo||_hasAudio) _typeDetected=true;
      }
      kv(std,'type',_typeDetected?(_hasVideo&&_hasAudio?'audio + video':_hasVideo?'video only':_hasAudio?'audio only':'unknown'):'detecting...',false);
      // Show audio-only overlay when no video is present
      if(_typeDetected&&_hasAudio&&!_hasVideo){
        ov.classList.remove('h');
        ov.innerHTML='<div style="text-align:center"><svg width="28" height="28" viewBox="0 0 24 24" fill="none" stroke="rgba(255,255,255,.3)" stroke-width="1.5"><path d="M9 18V5l12-2v13"/><circle cx="6" cy="18" r="3"/><circle cx="18" cy="16" r="3"/></svg><div style="margin-top:8px;color:rgba(255,255,255,.4);font-size:.7rem">audio only</div></div>';
      }
      // Tag the type value for later refinement
      var tySpans=std.querySelectorAll('.di');
      tySpans.forEach(function(di){var lb=di.querySelector('span');var vl=di.querySelectorAll('span')[1];if(lb&&vl&&lb.textContent.trim()==='type')vl.id='sv_ty'});
      // Refine type after video metadata loads (handles single-rendition muxed streams)
      if(!_typeDetected){
        video.addEventListener('loadedmetadata',function(){
          var el=document.getElementById('sv_ty');if(!el)return;
          var hv=video.videoWidth>0,ha=!!(hls.audioTracks&&hls.audioTracks.length>0);
          // Single rendition with video = assume muxed audio+video unless audio-only
          if(hv) el.textContent='audio + video';
          else if(ha) el.textContent='audio only';
          else el.textContent='video only';
        },{once:true});
      }
      // Per-variant stream sections — only for master playlists with explicit variant info
      var _hasMaster=hls.levels&&hls.levels.length>0&&(hls.levels.length>1||hls.levels[0].height>0||hls.levels[0].bitrate>0);
      if(_hasMaster){
        if(hls.levels.length>1) kv(std,'variants',String(hls.levels.length),false);
        var activeLvl=hls.currentLevel>=0?hls.currentLevel:(hls.startLevel>=0?hls.startLevel:0);
        hls.levels.forEach(function(lv,i){buildVariantSection(lv,i,i===activeLvl)});
      }
      // Quality selector (only for master playlists with multiple levels)
      if(hls.levels&&hls.levels.length>1){
        var qs=document.createElement('select');
        qs.className='cb';
        qs.style.cssText='margin-left:8px;appearance:none;-webkit-appearance:none;padding:2px 8px';
        var auto=document.createElement('option');
        auto.value='-1';auto.textContent='auto';auto.selected=true;
        qs.appendChild(auto);
        hls.levels.forEach(function(lv,i){
          var opt=document.createElement('option');
          opt.value=String(i);
          var label=lv.height?lv.height+'p':'level '+i;
          if(lv.bitrate) label+=' ('+Math.round(lv.bitrate/1000)+'k)';
          opt.textContent=label;
          qs.appendChild(opt);
        });
        qs.onchange=function(){hls.currentLevel=parseInt(qs.value)};
        var ctrl=document.getElementById('cn').parentNode;
        ctrl.appendChild(qs);
      }
      // Update (active) labels when level switches
      hls.on(Hls.Events.LEVEL_SWITCHED,function(ev,dat){
        var newLvl=dat.level;
        varContainer.querySelectorAll('[style*="text-transform"]').forEach(function(sp){
          var txt=sp.textContent;
          sp.textContent=txt.replace(/ \(active\)$/,'')+(sp.parentNode.parentNode.querySelector('.db')&&sp.parentNode.parentNode.querySelector('.db').id==='sv_var_'+newLvl?' (active)':'');
        });
      });
    });

    // Audio track selector (demuxed audio via EXT-X-MEDIA TYPE=AUDIO)
    hls.on(Hls.Events.AUDIO_TRACKS_UPDATED,function(){
      if(hls.audioTracks&&hls.audioTracks.length>1){
        var as=document.getElementById('_ats');
        if(as) as.remove();
        as=document.createElement('select');
        as.id='_ats';
        as.className='cb';
        as.style.cssText='margin-left:8px;appearance:none;-webkit-appearance:none;padding:2px 8px';
        hls.audioTracks.forEach(function(tk,i){
          var opt=document.createElement('option');
          opt.value=String(i);
          opt.textContent='\u266b '+tk.name;
          if(i===hls.audioTrack) opt.selected=true;
          as.appendChild(opt);
        });
        as.onchange=function(){hls.audioTrack=parseInt(as.value)};
        document.getElementById('cn').parentNode.appendChild(as);
      }
      // Track listing in stream section (same visual treatment as variants)
      if(hls.audioTracks&&hls.audioTracks.length>0) buildTrackSections('audio',hls.audioTracks);
    });

    // Subtitle track selector (WebVTT via EXT-X-MEDIA TYPE=SUBTITLES)
    hls.on(Hls.Events.SUBTITLE_TRACKS_UPDATED,function(){
      if(hls.subtitleTracks&&hls.subtitleTracks.length>0){
        // Ensure subtitles start OFF — HLS.js may auto-select the first track
        hls.subtitleTrack=-1;hls.subtitleDisplay=false;
        var ss=document.getElementById('_sts');
        if(ss) ss.remove();
        ss=document.createElement('select');
        ss.id='_sts';
        ss.className='cb';
        ss.style.cssText='margin-left:8px;appearance:none;-webkit-appearance:none;padding:2px 8px';
        var off=document.createElement('option');
        off.value='-1';off.textContent='CC off';off.selected=true;
        ss.appendChild(off);
        hls.subtitleTracks.forEach(function(tk,i){
          var opt=document.createElement('option');
          opt.value=String(i);
          opt.textContent='\u2261 '+tk.name;
          ss.appendChild(opt);
        });
        ss.onchange=function(){
          var v=parseInt(ss.value);
          hls.subtitleTrack=v;
          hls.subtitleDisplay=(v>=0);
        };
        document.getElementById('cn').parentNode.appendChild(ss);
      }
      // Track listing in stream section (same visual treatment as variants)
      if(hls.subtitleTracks&&hls.subtitleTracks.length>0) buildTrackSections('subtitle',hls.subtitleTracks);
    });

    hls.on(Hls.Events.LEVEL_LOADED,function(e,data){
      var det=data.details;
      if(!det) return;
      var el;
      el=document.getElementById('sv_sg');if(el) el.textContent=String(det.fragments?det.fragments.length:0);
      el=document.getElementById('sv_td');if(el) el.textContent=det.targetduration?det.targetduration+'s':'-';
      el=document.getElementById('sv_ms');if(el) el.textContent=(det.startSN!==undefined)?String(det.startSN):'-';
      // Add live stats to the active variant section (added dynamically on first load)
      var lvl=data.level;
      if(lvl!==undefined){
        var vsec=document.getElementById('sv_var_'+lvl);
        if(vsec&&!document.getElementById('sv_var_'+lvl+'_sg')){
          kv(vsec,'segments',String(det.fragments?det.fragments.length:0),false);
          kv(vsec,'target duration',det.targetduration?det.targetduration+'s':'-',false);
          kv(vsec,'media sequence',(det.startSN!==undefined)?String(det.startSN):'-',false);
          var spans=vsec.querySelectorAll('.di');
          spans.forEach(function(di){var lb=di.querySelector('span');var vl=di.querySelectorAll('span')[1];if(!lb||!vl)return;
            var k=lb.textContent.trim();
            if(k==='segments')vl.id='sv_var_'+lvl+'_sg';
            else if(k==='target duration')vl.id='sv_var_'+lvl+'_td';
            else if(k==='media sequence')vl.id='sv_var_'+lvl+'_ms';
          });
        }else{
          el=document.getElementById('sv_var_'+lvl+'_sg');if(el) el.textContent=String(det.fragments?det.fragments.length:0);
          el=document.getElementById('sv_var_'+lvl+'_td');if(el) el.textContent=det.targetduration?det.targetduration+'s':'-';
          el=document.getElementById('sv_var_'+lvl+'_ms');if(el) el.textContent=(det.startSN!==undefined)?String(det.startSN):'-';
        }
      }
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
      var el=document.getElementById('sv_st');if(el){el.className='ok';el.textContent='\u2713 live'}
    });
  } else {
    var el=document.getElementById('sv_st');if(el) el.innerHTML='<span class="er">\u2717 HLS not supported</span>';
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
