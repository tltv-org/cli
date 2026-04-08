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

// addViewerFlag registers --viewer with VIEWER=1 env var on a FlagSet.
func addViewerFlag(fs *flag.FlagSet) *bool {
	return fs.Bool("viewer", os.Getenv("VIEWER") == "1", "serve built-in web player at /")
}

// viewerEmbedRoutes registers the viewer HTML, static assets, and /api/info
// on an existing mux. infoFn is called per-request to get current channel state.
//
// Routes registered:
//
//	GET /            → viewer HTML (path "/" only, returns 404 for other paths)
//	GET /favicon.svg → SVG icon
//	GET /hls.min.js  → vendored HLS.js
//	GET /api/info    → JSON channel info
//
// Protocol endpoints (/.well-known/tltv, /tltv/v1/...) registered separately
// by the daemon take routing priority over the "/" subtree pattern.
func viewerEmbedRoutes(mux *http.ServeMux, infoFn func() map[string]interface{}) {
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
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
		info := infoFn()
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		enc := json.NewEncoder(w)
		enc.SetEscapeHTML(false)
		enc.Encode(info)
	})
}

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

// ---------- Embedded HTML ----------

const viewerHTML = `<!DOCTYPE html>
<html>
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>tltv debug viewer</title>
<link rel="icon" type="image/svg+xml" href="/favicon.svg">
<style>
*{margin:0;padding:0;box-sizing:border-box}
body{
  background:#000;color:#fff;
  font-family:'SFMono-Regular',Consolas,'Liberation Mono',Menlo,monospace;
  font-size:14px;line-height:1.6;
}
.c{max-width:72rem;width:100%;margin:0 auto}
.hd{
  display:flex;align-items:center;gap:1.5rem;
  padding:1rem 2rem;border-bottom:1px solid #333;
  font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,'Helvetica Neue',Arial,sans-serif;
}
.hd svg{height:24px;width:auto;flex-shrink:0}
.hdl{font-size:.9rem;color:#999;margin-left:auto}
.inner{max-width:calc(1100px + 2rem);width:100%;margin:0 auto;padding:1.25rem 1rem 0}
.vw{position:relative;width:100%;padding-bottom:56.25%;background:#000}
.vw video{position:absolute;top:0;left:0;width:100%;height:100%;object-fit:contain;background:#000}
.ov{
  position:absolute;top:0;left:0;width:100%;height:100%;
  display:flex;align-items:center;justify-content:center;
  background:rgba(0,0,0,.88);z-index:1;
}
.ov.h{display:none}
.sp{
  width:14px;height:14px;
  border:1.5px solid rgba(255,255,255,.2);border-top-color:rgba(255,255,255,.5);
  border-radius:50%;animation:spin 1s linear infinite;margin-right:8px;
}
@keyframes spin{to{transform:rotate(360deg)}}
.ov span{color:rgba(255,255,255,.5);font-size:14px}
.ctrl{display:flex;align-items:center;gap:.5rem;height:36px;padding:0}
.cn{font-size:.85rem;font-weight:600;color:#fff;white-space:nowrap}
.sep{font-size:.8rem;color:#666}
.prg{font-size:.8rem;color:#999;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;min-width:0}
.spacer{flex:1}
.cb{
  background:none;border:1px solid #333;color:#666;
  font-family:inherit;font-size:.7rem;padding:2px 8px;cursor:pointer;border-radius:0;
}
.cb:hover{color:#999;border-color:#666}
.ubar{padding:.2rem 0 .6rem;border-bottom:1px solid #333}
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
</style>
</head>
<body>
<div class="c">
  <div class="hd">
    <svg viewBox="0 0 1020 350" fill="#fff" aria-hidden="true"><svg x="0" y="0" width="491" height="350" viewBox="0 0 491.321548 349.773636"><g transform="translate(-0.176918,349.811893) scale(0.1,-0.1)" stroke="none"><path d="M2050 3493 c-881 -31 -1492 -102 -1671 -194 -95 -48 -171 -145 -214 -272 -98 -292 -154 -705 -162 -1192 -8 -497 27 -842 127 -1233 48 -187 74 -246 140 -316 96 -102 195 -137 505 -181 756 -105 1734 -133 2665 -75 330 21 800 78 959 117 195 47 311 159 370 358 52 179 98 442 128 735 24 242 24 784 0 1030 -40 397 -116 746 -191 874 -34 58 -117 133 -179 161 -243 111 -1187 198 -2092 193 -170 -1 -344 -3 -385 -5z m-246 -993 c33 -5 92 -25 132 -44 68 -32 101 -64 609 -571 296 -295 547 -543 559 -550 17 -11 80 -15 299 -15 392 -2 361 -37 365 410 3 365 0 389 -57 427 -33 23 -39 23 -303 23 -177 0 -277 -4 -291 -11 -12 -6 -95 -84 -185 -172 l-162 -162 -113 113 -112 112 170 170 c178 178 221 211 320 250 57 22 75 24 326 28 170 2 293 0 340 -7 193 -30 343 -175 378 -365 14 -76 15 -704 1 -777 -15 -79 -67 -177 -124 -235 -58 -57 -156 -109 -235 -124 -70 -13 -552 -13 -622 0 -30 5 -85 26 -124 45 -64 31 -111 75 -580 545 -280 282 -532 529 -558 551 l-49 39 -278 0 -278 0 -30 -25 c-52 -43 -53 -53 -50 -424 l3 -343 37 -34 c22 -20 49 -35 65 -35 100 -6 532 5 548 13 11 6 92 82 180 169 l160 159 113 -113 112 -113 -183 -182 c-262 -260 -269 -262 -677 -262 -141 0 -281 5 -311 10 -170 32 -310 164 -355 335 -10 37 -14 143 -14 423 0 351 1 376 21 434 52 156 176 269 332 303 75 16 530 20 621 5z"/></g></svg><svg x="530" y="15" width="480" height="320" viewBox="0 -984 1726 1276"><g transform="scale(1,-1)"><g transform="translate(0,0)"><path d="M260 0Q211 0 180.5 30.5Q150 61 150 112V392H26V496H150V650H276V496H412V392H276V134Q276 104 304 104H400V0Z"/></g><g transform="translate(456,0)"><path d="M70 0V700H196V0Z"/></g><g transform="translate(722,0)"><path d="M260 0Q211 0 180.5 30.5Q150 61 150 112V392H26V496H150V650H276V496H412V392H276V134Q276 104 304 104H400V0Z"/></g><g transform="translate(1178,0)"><path d="M174 0 16 496H150L265 92H283L398 496H532L374 0Z"/></g></g></svg></svg>
    <span class="hdl">debug viewer</span>
  </div>

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

  <div class="sl" style="margin-top:12px">guide</div>
  <div id="gd"></div>
  <hr>

  <div class="sl">stream</div>
  <div class="db">
    <div class="dr">
      <div class="di"><span>status </span><span id="ds">connecting</span></div>
      <div class="di"><span>segment </span><span id="dsg">-</span></div>
      <div class="di"><span>bitrate </span><span id="dbw">-</span></div>
    </div>
    <div class="dr">
      <div class="di"><span>buffer </span><span id="dbu">-</span></div>
      <div class="di"><span>resolution </span><span id="dre">-</span></div>
    </div>
  </div>
  <hr>

  <div class="sl">channel</div>
  <div class="db">
    <div class="dr">
      <div class="di"><span>verified </span><span id="dvr" class="ok"></span></div>
    </div>
    <div id="chd"></div>
  </div>
  </div>
</div>
</div>

<script src="/hls.min.js"></script>
<script>
var inf={};

function esc(s){var d=document.createElement('div');d.textContent=s;return d.innerHTML}

fetch('/api/info').then(function(r){return r.json()}).then(function(d){
  inf=d;
  document.getElementById('cn').textContent=d.channel_name;
  document.getElementById('uri').textContent=d.tltv_uri;
  document.title=d.channel_name+' \u2014 tltv debug viewer';

  document.getElementById('dvr').textContent=d.verified?'\u2713':'?';

  // Render all metadata fields dynamically
  var m=d.metadata||{};
  var base=d.base_url||'';
  var skip={signature:1,v:1};
  var chd=document.getElementById('chd');
  var keys=Object.keys(m).sort();
  keys.forEach(function(k){
    if(skip[k]) return;
    var val=m[k];
    if(Array.isArray(val)) val=val.join(', ');
    else if(val!==null&&typeof val==='object') val=JSON.stringify(val);
    else val=String(val);
    // Expand path-only values to full URLs
    if((k==='guide'||k==='stream'||k==='icon')&&val.charAt(0)==='/') val=base+val;
    var div=document.createElement('div');
    div.className='dr';
    var isUrl=val.indexOf('http')===0;
    var valHtml=isUrl?'<a class="uri" href="'+esc(val)+'" target="_blank">'+esc(val)+'</a>':'<span class="uri">'+esc(val)+'</span>';
    div.innerHTML='<div class="di"><span>'+esc(k)+' </span>'+valHtml+'</div>';
    chd.appendChild(div);
  });
  // Add xmltv url after metadata fields
  var xdiv=document.createElement('div');
  xdiv.className='dr';
  xdiv.innerHTML='<div class="di"><span>xmltv </span><a class="uri" target="_blank" href="'+esc(d.xmltv_url||'')+'">'+esc(d.xmltv_url||'-')+'</a></div>';
  chd.appendChild(xdiv);

  // Find current program from guide
  if(d.guide&&d.guide.entries){
    var nowISO=new Date().toISOString();
    for(var i=0;i<d.guide.entries.length;i++){
      var ge=d.guide.entries[i];
      if(ge.start&&ge.end&&ge.start<=nowISO&&ge.end>nowISO){
        document.getElementById('prg').textContent=ge.title||'';
        document.getElementById('ps').style.display='';
        break;
      }
    }
  }

  // Guide
  var gd=document.getElementById('gd');
  if(d.guide&&d.guide.entries&&d.guide.entries.length){
    d.guide.entries.forEach(function(e){
      var div=document.createElement('div');
      div.className='ge';
      var s=e.start?e.start.substring(11,16):'';
      var n=e.end?e.end.substring(11,16):'';
      div.innerHTML='<span class="gt">'+esc(s)+'\u2013'+esc(n)+'</span>  '+esc(e.title||'');
      gd.appendChild(div);
    });
  } else {
    gd.innerHTML='<div class="ge" style="color:#666">no guide data</div>';
  }

  initPlayer(d.stream_src);
});

function initPlayer(src){
  var video=document.getElementById('v');
  var ov=document.getElementById('ov');

  if(typeof Hls!=='undefined'&&Hls.isSupported()){
    var hls=new Hls({liveSyncDurationCount:3,liveMaxLatencyDurationCount:6});
    hls.loadSource(src);
    hls.attachMedia(video);

    hls.on(Hls.Events.MANIFEST_PARSED,function(){
      video.play().catch(function(){});
      ov.classList.add('h');
      ss('playing','ok');
    });

    hls.on(Hls.Events.FRAG_LOADED,function(e,data){
      var f=data.frag;
      if(f&&typeof f.sn==='number'){
        document.getElementById('dsg').textContent=f.sn;
      }
      if(f&&f.stats){
        var bytes=f.stats.loaded||f.stats.total||0;
        if(bytes>0&&f.duration>0){
          document.getElementById('dbw').textContent=((bytes*8/f.duration)/1e6).toFixed(1)+' Mbps';
        }
      }
    });

    hls.on(Hls.Events.ERROR,function(e,data){
      if(data.fatal) ss('error: '+data.type,'er');
    });

    setInterval(function(){
      if(video.buffered.length>0){
        var b=video.buffered.end(video.buffered.length-1)-video.currentTime;
        document.getElementById('dbu').textContent=b.toFixed(1)+'s';
      }
      if(video.videoWidth>0){
        document.getElementById('dre').textContent=video.videoWidth+'x'+video.videoHeight;
      }
    },1000);

  } else if(video.canPlayType('application/vnd.apple.mpegurl')){
    video.src=src;
    video.addEventListener('loadedmetadata',function(){
      video.play().catch(function(){});
      ov.classList.add('h');
      ss('playing','ok');
    });
  } else {
    ss('HLS not supported','er');
  }
}

function ss(t,l){
  var e=document.getElementById('ds');
  e.textContent=t;
  e.className=l==='ok'?'ok':l==='er'?'er':'wn';
}

function tmu(){
  var v=document.getElementById('v');
  v.muted=!v.muted;
  document.getElementById('mb').textContent=v.muted?'unmute':'mute';
}
</script>
</body>
</html>`
