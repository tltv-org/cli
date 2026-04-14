package main

import (
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// ---------- Constants ----------

const maxManifestSize = 10 * 1024 * 1024 // 10 MB

var bridgeStreamClient = &http.Client{Timeout: 30 * time.Second}

// bridgeTagURIRegex matches URI="..." attributes in HLS tags.
var bridgeTagURIRegex = regexp.MustCompile(`(URI=")([^"]*)(")`)

// bridgeURITags are the HLS tags whose URI attributes need rewriting.
var bridgeURITags = []string{
	"#EXT-X-MAP:",
	"#EXT-X-KEY:",
	"#EXT-X-MEDIA:",
	"#EXT-X-I-FRAME-STREAM-INF:",
	"#EXT-X-SESSION-KEY:",
}

// ---------- Path Validation ----------

// validateSubPath rejects path traversal attempts in stream sub-paths.
// Go's http.ServeMux cleans ".." from URL paths before routing, but this is
// defense-in-depth for deployments without a reverse proxy in front.
func validateSubPath(subPath string) bool {
	for _, seg := range strings.Split(subPath, "/") {
		if seg == ".." {
			return false
		}
	}
	return subPath != ""
}

// ---------- Main Entry ----------

// bridgeServeStream dispatches to local or upstream stream serving.
func bridgeServeStream(w http.ResponseWriter, r *http.Request, ch *bridgeRegisteredChannel, subPath, token string) {
	if !validateSubPath(subPath) {
		jsonError(w, "invalid_request", http.StatusBadRequest)
		return
	}

	if strings.HasPrefix(ch.StreamURL, "http://") || strings.HasPrefix(ch.StreamURL, "https://") {
		bridgeServeUpstreamStream(w, r, ch.StreamURL, subPath, token, ch.IsPrivate())
	} else {
		bridgeServeLocalStream(w, r, ch.StreamURL, subPath, token, ch.IsPrivate())
	}
}

// ---------- Local File Serving ----------

// bridgeServeLocalStream serves HLS content from local filesystem.
func bridgeServeLocalStream(w http.ResponseWriter, r *http.Request, manifestPath, subPath, token string, private bool) {
	var filePath string
	if subPath == "stream.m3u8" {
		filePath = manifestPath
	} else {
		filePath = filepath.Join(filepath.Dir(manifestPath), subPath)
	}

	// Path traversal protection: resolve symlinks and check prefix
	absPath, err := filepath.EvalSymlinks(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			jsonError(w, "stream_unavailable", http.StatusServiceUnavailable)
		} else {
			jsonError(w, "invalid_request", http.StatusBadRequest)
		}
		return
	}

	baseDir, err := filepath.EvalSymlinks(filepath.Dir(manifestPath))
	if err != nil {
		jsonError(w, "stream_unavailable", http.StatusServiceUnavailable)
		return
	}

	// Append separator to prevent /data/ch1_evil matching /data/ch1
	baseDirPrefix := baseDir + string(filepath.Separator)
	if absPath != baseDir && !strings.HasPrefix(absPath, baseDirPrefix) {
		jsonError(w, "invalid_request", http.StatusBadRequest)
		return
	}

	setStreamHeaders(w, subPath, private)

	// For m3u8 with token, rewrite manifest to inject tokens
	if strings.HasSuffix(subPath, ".m3u8") && token != "" {
		data, err := os.ReadFile(absPath)
		if err != nil {
			jsonError(w, "stream_unavailable", http.StatusServiceUnavailable)
			return
		}
		rewritten := rewriteManifest("", data, token)
		w.Write(rewritten)
		return
	}

	http.ServeFile(w, r, absPath)
}

// ---------- Upstream HTTP Serving ----------

// bridgeServeUpstreamStream proxies HLS content from an upstream HTTP source.
func bridgeServeUpstreamStream(w http.ResponseWriter, r *http.Request, manifestURL, subPath, token string, private bool) {
	var fetchURL string
	if subPath == "stream.m3u8" {
		fetchURL = manifestURL
	} else {
		// Resolve subPath relative to manifest directory
		base, err := url.Parse(manifestURL)
		if err != nil {
			jsonError(w, "stream_unavailable", http.StatusServiceUnavailable)
			return
		}
		ref, err := url.Parse(subPath)
		if err != nil {
			jsonError(w, "stream_unavailable", http.StatusServiceUnavailable)
			return
		}
		// Set base path to the directory of the manifest
		base.Path = path.Dir(base.Path) + "/"
		fetchURL = base.ResolveReference(ref).String()
	}

	req, err := http.NewRequestWithContext(r.Context(), "GET", fetchURL, nil)
	if err != nil {
		jsonError(w, "stream_unavailable", http.StatusServiceUnavailable)
		return
	}

	resp, err := bridgeStreamClient.Do(req)
	if err != nil {
		jsonError(w, "stream_unavailable", http.StatusServiceUnavailable)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		jsonError(w, "stream_unavailable", http.StatusServiceUnavailable)
		return
	}

	setStreamHeadersWithContentType(w, subPath, private, resp.Header.Get("Content-Type"))

	if strings.HasSuffix(subPath, ".m3u8") {
		// Read and rewrite manifest
		body, err := io.ReadAll(io.LimitReader(resp.Body, maxManifestSize))
		if err != nil {
			jsonError(w, "stream_unavailable", http.StatusServiceUnavailable)
			return
		}
		rewritten := rewriteManifest(manifestURL, body, token)
		w.Write(rewritten)
		return
	}

	// Stream segment through directly
	io.Copy(w, resp.Body)
}

// ---------- Manifest Rewriting ----------

// rewriteManifest rewrites an HLS manifest:
// - Converts absolute URLs to relative (same-origin only)
// - Injects ?token= on every URI for private channels
//
// manifestURL may be empty for local files (no URL rewriting needed).
// token may be empty for public channels (no token injection).
func rewriteManifest(manifestURL string, body []byte, token string) []byte {
	var baseDirStr string
	if manifestURL != "" {
		base, err := url.Parse(manifestURL)
		if err == nil {
			base.Path = path.Dir(base.Path) + "/"
			base.RawQuery = ""
			base.Fragment = ""
			baseDirStr = base.String()
		}
	}

	lines := strings.Split(string(body), "\n")
	var result []string

	for _, line := range lines {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			continue
		}

		if strings.HasPrefix(line, "#") {
			// Check if this tag has URI attributes to rewrite
			line = bridgeRewriteTagURIs(line, baseDirStr, token)
			result = append(result, line)
			continue
		}

		// Bare URI line (segment or sub-manifest)
		uri := line
		if baseDirStr != "" {
			uri = bridgeMakeRelative(uri, baseDirStr)
		}
		if token != "" {
			uri = bridgeAppendToken(uri, token)
		}
		result = append(result, uri)
	}

	return []byte(strings.Join(result, "\n"))
}

// bridgeRewriteTagURIs rewrites URI= attributes in HLS tags.
// Only processes the 5 specific tags that have URI attributes.
func bridgeRewriteTagURIs(line, baseDirStr, token string) string {
	isURITag := false
	for _, tag := range bridgeURITags {
		if strings.HasPrefix(line, tag) {
			isURITag = true
			break
		}
	}
	if !isURITag {
		return line
	}

	return bridgeTagURIRegex.ReplaceAllStringFunc(line, func(match string) string {
		parts := bridgeTagURIRegex.FindStringSubmatch(match)
		if len(parts) != 4 {
			return match
		}
		uri := parts[2]
		if baseDirStr != "" {
			uri = bridgeMakeRelative(uri, baseDirStr)
		}
		if token != "" {
			uri = bridgeAppendToken(uri, token)
		}
		return parts[1] + uri + parts[3]
	})
}

// bridgeMakeRelative converts an absolute URL to relative if it shares the same base directory.
// Different-origin URLs and already-relative URLs pass through unchanged.
func bridgeMakeRelative(uri, baseDirStr string) string {
	if !strings.HasPrefix(uri, "http://") && !strings.HasPrefix(uri, "https://") {
		return uri
	}
	if strings.HasPrefix(uri, baseDirStr) {
		return uri[len(baseDirStr):]
	}
	return uri
}

// bridgeAppendToken appends ?token=value or &token=value to a URI.
func bridgeAppendToken(uri, token string) string {
	if strings.Contains(uri, "?") {
		return uri + "&token=" + token
	}
	return uri + "?token=" + token
}

// ---------- Headers ----------

// setStreamHeaders sets Content-Type and Cache-Control for stream responses.
func setStreamHeaders(w http.ResponseWriter, subPath string, private bool) {
	setStreamHeadersWithContentType(w, subPath, private, "")
}

// setStreamHeadersWithContentType prefers the canonical content type for known
// file extensions and falls back to the upstream content type for unknown ones.
func setStreamHeadersWithContentType(w http.ResponseWriter, subPath string, private bool, contentType string) {
	ct := streamContentType(subPath)
	if ct == "application/octet-stream" && contentType != "" {
		ct = contentType
	}
	w.Header().Set("Content-Type", ct)

	if private {
		w.Header().Set("Cache-Control", "private, no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		return
	}

	if strings.HasSuffix(subPath, ".m3u8") {
		w.Header().Set("Cache-Control", "max-age=1, no-cache")
	} else {
		w.Header().Set("Cache-Control", "max-age=3600")
	}
}

// streamContentType returns the Content-Type for a stream file.
func streamContentType(name string) string {
	switch {
	case strings.HasSuffix(name, ".m3u8"):
		return "application/vnd.apple.mpegurl"
	case strings.HasSuffix(name, ".ts"):
		return "video/mp2t"
	case strings.HasSuffix(name, ".mp4"), strings.HasSuffix(name, ".m4s"):
		return "video/mp4"
	case strings.HasSuffix(name, ".aac"):
		return "audio/aac"
	case strings.HasSuffix(name, ".vtt"):
		return "text/vtt"
	case strings.HasSuffix(name, ".svg"):
		return "image/svg+xml"
	case strings.HasSuffix(name, ".png"):
		return "image/png"
	case strings.HasSuffix(name, ".jpg"), strings.HasSuffix(name, ".jpeg"):
		return "image/jpeg"
	default:
		return "application/octet-stream"
	}
}
