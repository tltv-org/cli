package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// NodeInfo represents the response from /.well-known/tltv
type NodeInfo struct {
	Protocol string       `json:"protocol"`
	Versions []int        `json:"versions"`
	Channels []ChannelRef `json:"channels"`
	Relaying []ChannelRef `json:"relaying"`
}

// ChannelRef is a channel reference in node info or peer exchange.
type ChannelRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// PeerExchange represents the response from /tltv/v1/peers
type PeerExchange struct {
	Peers []Peer `json:"peers"`
}

// Peer represents a single peer in the peer exchange.
type Peer struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Hints    []string `json:"hints"`
	LastSeen string   `json:"last_seen"`
}

// Client is an HTTP client for TLTV protocol operations.
type Client struct {
	http     *http.Client
	insecure bool // when true, baseURL defaults to HTTP instead of HTTPS
}

// newClient creates a new TLTV HTTP client.
// Use for user-chosen targets (node, fetch, guide, peers, stream).
func newClient(insecure bool) *Client {
	tr := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: insecure,
		},
		DialContext: (&net.Dialer{
			Timeout: 5 * time.Second,
		}).DialContext,
		TLSHandshakeTimeout: 5 * time.Second,
	}

	return &Client{
		http: &http.Client{
			Timeout:   15 * time.Second,
			Transport: tr,
		},
		insecure: insecure,
	}
}

// newClientWithProxy creates a new TLTV HTTP client that routes through the
// given proxy URL. If proxyURL is nil, behaves like newClient.
func newClientWithProxy(insecure bool, proxyURL *url.URL) *Client {
	proxyFunc := http.ProxyFromEnvironment
	if proxyURL != nil {
		proxyFunc = http.ProxyURL(proxyURL)
	}
	tr := &http.Transport{
		Proxy: proxyFunc,
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: insecure,
		},
		DialContext: (&net.Dialer{
			Timeout: 5 * time.Second,
		}).DialContext,
		TLSHandshakeTimeout: 5 * time.Second,
	}

	return &Client{
		http: &http.Client{
			Timeout:   15 * time.Second,
			Transport: tr,
		},
		insecure: insecure,
	}
}

// newHTTPClientWithProxy creates a bare *http.Client that routes through the
// given proxy URL. Used for bridge source/stream clients.
func newHTTPClientWithProxy(proxyURL *url.URL) *http.Client {
	tr := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
	}
	if proxyURL != nil {
		tr.Proxy = http.ProxyURL(proxyURL)
	}
	return &http.Client{
		Timeout:   30 * time.Second,
		Transport: tr,
	}
}

// newSSRFSafeClient creates a client that blocks connections to local/private
// addresses at DNS resolution time, preventing SSRF via DNS rebinding.
// Used for untrusted hints in resolve and crawl (spec section 3.1).
func newSSRFSafeClient(insecure bool) *Client {
	tr := &http.Transport{
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: insecure,
		},
		DialContext:         ssrfSafeDialContext,
		TLSHandshakeTimeout: 5 * time.Second,
	}

	return &Client{
		http: &http.Client{
			Timeout:   15 * time.Second,
			Transport: tr,
		},
		insecure: insecure,
	}
}

// ssrfSafeDialContext resolves hostnames and blocks connections to
// local/private/loopback/link-local/CGN addresses. Connects directly to the
// resolved IP to prevent TOCTOU between DNS lookup and connection.
func ssrfSafeDialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}

	ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}

	dialer := &net.Dialer{Timeout: 5 * time.Second}
	var lastErr error
	for _, ip := range ips {
		if isLocalAddress(ip.IP.String() + ":0") {
			lastErr = fmt.Errorf("hint resolves to local address %s (%s), blocked per spec section 3.1", host, ip.IP)
			continue
		}
		conn, err := dialer.DialContext(ctx, network, net.JoinHostPort(ip.IP.String(), port))
		if err != nil {
			lastErr = err
			continue
		}
		return conn, nil
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, fmt.Errorf("no addresses found for %s", host)
}

// baseURL constructs the base URL for a host.
// Defaults to HTTPS, but uses HTTP for localhost/loopback or when --insecure is set.
func (c *Client) baseURL(host string) string {
	if strings.HasPrefix(host, "http://") || strings.HasPrefix(host, "https://") {
		return strings.TrimRight(host, "/")
	}

	// Normalize host: add default port if missing
	if _, _, err := net.SplitHostPort(host); err != nil {
		if c.insecure {
			host = host + ":80"
		} else {
			host = host + ":443"
		}
	}

	// Use HTTP for --insecure or local addresses
	scheme := "https"
	if c.insecure || isLocalAddress(host) {
		scheme = "http"
	}

	return scheme + "://" + host
}

// get performs a GET request and returns the response body.
func (c *Client) get(url string) ([]byte, int, error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("User-Agent", "tltv-cli/"+version)
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 10*1024*1024)) // 10MB max
	if err != nil {
		return nil, resp.StatusCode, err
	}

	return body, resp.StatusCode, nil
}

// FetchNodeInfo fetches /.well-known/tltv from a host.
func (c *Client) FetchNodeInfo(host string) (*NodeInfo, error) {
	url := c.baseURL(host) + "/.well-known/tltv"
	body, status, err := c.get(url)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	if status != 200 {
		return nil, fmt.Errorf("HTTP %d: %s", status, truncateBody(body))
	}

	var info NodeInfo
	if err := json.Unmarshal(body, &info); err != nil {
		return nil, fmt.Errorf("invalid JSON: %w", err)
	}
	return &info, nil
}

// FetchMetadata fetches and optionally verifies channel metadata.
func (c *Client) FetchMetadata(host, channelID, token string) (map[string]interface{}, error) {
	url := c.baseURL(host) + "/tltv/v1/channels/" + channelID
	if token != "" {
		url += "?token=" + token
	}

	body, status, err := c.get(url)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	if status == 403 {
		return nil, fmt.Errorf("access denied (token may be required)")
	}
	if status == 404 {
		return nil, fmt.Errorf("channel not found")
	}
	if status != 200 {
		return nil, fmt.Errorf("HTTP %d: %s", status, truncateBody(body))
	}

	doc, err := readDocumentFromString(string(body))
	if err != nil {
		return nil, err
	}
	return doc, nil
}

// FetchGuide fetches and optionally verifies a channel guide.
func (c *Client) FetchGuide(host, channelID, token string) (map[string]interface{}, error) {
	url := c.baseURL(host) + "/tltv/v1/channels/" + channelID + "/guide.json"
	if token != "" {
		url += "?token=" + token
	}

	body, status, err := c.get(url)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	if status == 404 {
		return nil, fmt.Errorf("guide not found")
	}
	if status != 200 {
		return nil, fmt.Errorf("HTTP %d: %s", status, truncateBody(body))
	}

	doc, err := readDocumentFromString(string(body))
	if err != nil {
		return nil, err
	}
	return doc, nil
}

// FetchPeers fetches the peer exchange list from a host.
func (c *Client) FetchPeers(host string) (*PeerExchange, error) {
	url := c.baseURL(host) + "/tltv/v1/peers"
	body, status, err := c.get(url)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	if status != 200 {
		return nil, fmt.Errorf("HTTP %d: %s", status, truncateBody(body))
	}

	var exchange PeerExchange
	if err := json.Unmarshal(body, &exchange); err != nil {
		return nil, fmt.Errorf("invalid JSON: %w", err)
	}
	return &exchange, nil
}

// CheckStream checks if a channel's HLS stream is available.
// Returns the status code, content type, and first bytes of the manifest.
func (c *Client) CheckStream(host, channelID, token string) (int, string, string, error) {
	url := c.baseURL(host) + "/tltv/v1/channels/" + channelID + "/stream.m3u8"
	if token != "" {
		url += "?token=" + token
	}

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return 0, "", "", err
	}
	req.Header.Set("User-Agent", "tltv-cli/"+version)

	resp, err := c.http.Do(req)
	if err != nil {
		return 0, "", "", err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	contentType := resp.Header.Get("Content-Type")

	return resp.StatusCode, contentType, string(body), nil
}

// isLocalAddress checks if a hint refers to a loopback, private, link-local,
// unspecified, or CGN address. Per spec section 3.1, clients MUST NOT contact
// such hints unless --local is set.
func isLocalAddress(host string) bool {
	h, _, err := net.SplitHostPort(host)
	if err != nil {
		h = host
	}
	// Strip IPv6 brackets
	h = strings.TrimPrefix(strings.TrimSuffix(h, "]"), "[")

	if h == "localhost" {
		return true
	}

	ip := net.ParseIP(h)
	if ip == nil {
		return false // DNS name, can't check without resolving
	}

	// Normalize IPv4-mapped IPv6 (::ffff:127.0.0.1 -> 127.0.0.1)
	if v4 := ip.To4(); v4 != nil {
		ip = v4
	}

	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() {
		return true
	}

	// CGN/shared address space (100.64.0.0/10, RFC 6598)
	if ip4 := ip.To4(); ip4 != nil {
		if ip4[0] == 100 && ip4[1]&0xc0 == 0x40 {
			return true
		}
	}

	return false
}

// validateHint checks that a hint is a valid host[:port] and does not contain
// URL components that could enable SSRF bypass (scheme, userinfo, path, etc.).
func validateHint(hint string) error {
	if hint == "" {
		return fmt.Errorf("empty hint")
	}
	if strings.Contains(hint, "://") {
		return fmt.Errorf("hint must be host[:port], not a URL")
	}
	if strings.Contains(hint, "@") {
		return fmt.Errorf("hint must not contain userinfo (@)")
	}
	if strings.Contains(hint, "/") {
		return fmt.Errorf("hint must not contain a path")
	}
	if strings.Contains(hint, "?") {
		return fmt.Errorf("hint must not contain a query string")
	}
	if strings.Contains(hint, "#") {
		return fmt.Errorf("hint must not contain a fragment")
	}
	// Validate bracketed IPv6
	if strings.HasPrefix(hint, "[") {
		if !strings.Contains(hint, "]") {
			return fmt.Errorf("malformed IPv6 hint: missing closing bracket")
		}
	}
	return nil
}

// normalizeHint validates and normalizes a hint from untrusted sources.
// Rejects malformed hints and adds default port 443 if missing.
func normalizeHint(s string) (string, error) {
	if err := validateHint(s); err != nil {
		return "", err
	}
	if _, _, err := net.SplitHostPort(s); err != nil {
		return s + ":443", nil
	}
	return s, nil
}

// truncateBody returns the first 200 bytes of a response body for error display.
func truncateBody(body []byte) string {
	s := strings.TrimSpace(string(body))
	if len(s) > 200 {
		return s[:200] + "..."
	}
	return s
}

// parseTarget parses a channel target, accepting either:
//   - tltv://TVxxx@host:port  (URI format, uses first hint as host)
//   - TVxxx@host:port         (compact format)
func parseTarget(s string) (channelID, host string, err error) {
	// Accept tltv:// URIs
	if strings.HasPrefix(s, tltvScheme) {
		uri, err := parseTLTVUri(s)
		if err != nil {
			return "", "", err
		}
		if len(uri.Hints) == 0 {
			return "", "", fmt.Errorf("URI has no host hint -- use tltv://ID@host:port")
		}
		return uri.ChannelID, uri.Hints[0], nil
	}

	idx := strings.Index(s, "@")
	if idx < 0 {
		return "", "", fmt.Errorf("expected format: tltv://ID@host[:port] or ID@host[:port]")
	}
	channelID = s[:idx]
	host = s[idx+1:]
	if host == "" {
		return "", "", fmt.Errorf("empty host")
	}
	return channelID, host, nil
}

// parseTargetOrDiscover tries parseTarget first. If that fails, treats the
// target as a bare hostname: fetches /.well-known/tltv and picks the first channel.
func parseTargetOrDiscover(s string, client *Client) (channelID, host string, err error) {
	channelID, host, err = parseTarget(s)
	if err == nil {
		return
	}

	// Try as bare hostname — discover first channel
	host = normalizeHost(s)
	info, discErr := client.FetchNodeInfo(host)
	if discErr != nil {
		return "", "", fmt.Errorf("not a valid target and discovery failed on %s: %w", s, discErr)
	}
	if err := checkV1Support(info); err != nil {
		return "", "", fmt.Errorf("%s: %w", s, err)
	}
	// Check both origin channels and relayed channels (prefer origins first)
	allRefs := make([]ChannelRef, 0, len(info.Channels)+len(info.Relaying))
	allRefs = append(allRefs, info.Channels...)
	allRefs = append(allRefs, info.Relaying...)
	if len(allRefs) == 0 {
		return "", "", fmt.Errorf("no channels found on %s", s)
	}
	if len(allRefs) > 1 {
		fmt.Fprintf(os.Stderr, "note: %s has %d channels, using %s (%s)\n",
			s, len(allRefs), allRefs[0].ID, allRefs[0].Name)
	}
	return allRefs[0].ID, host, nil
}

// validateToken checks that a token conforms to spec §5.7:
// URL-safe, max 256 chars, unreserved characters only (RFC 3986: A-Z a-z 0-9 - . _ ~).
func validateToken(token string) error {
	if len(token) > 256 {
		return fmt.Errorf("token exceeds 256-character limit (%d chars)", len(token))
	}
	for i, c := range token {
		if !((c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') ||
			c == '-' || c == '.' || c == '_' || c == '~') {
			return fmt.Errorf("token contains character %q at position %d (only A-Za-z0-9._~- allowed)", c, i)
		}
	}
	return nil
}

// checkV1Support checks that a node supports protocol v1.
// Returns an error if v1 is not in the versions array.
func checkV1Support(info *NodeInfo) error {
	for _, v := range info.Versions {
		if v == 1 {
			return nil
		}
	}
	return fmt.Errorf("node does not support protocol v1 (versions: %v)", info.Versions)
}

// checkAccessMode checks that the metadata access mode is supported by this client.
// Returns an error if the access mode is unknown (not "public" or "token").
// Per spec §5.2: unknown access values must be treated as inaccessible.
func checkAccessMode(doc map[string]interface{}) error {
	access, _ := doc["access"].(string)
	if access == "" || access == "public" || access == "token" {
		return nil
	}
	return fmt.Errorf("channel uses an access mode this client doesn't support: %s", access)
}

// extractOrigins extracts the origins array from a verified metadata document.
// Returns nil if the origins field is absent or not an array of strings.
func extractOrigins(doc map[string]interface{}) []string {
	v, ok := doc["origins"]
	if !ok {
		return nil
	}
	arr, ok := v.([]interface{})
	if !ok {
		return nil
	}
	var origins []string
	for _, item := range arr {
		if s, ok := item.(string); ok {
			origins = append(origins, s)
		}
	}
	return origins
}

// hostnameMatchesOrigin checks if the connected hostname matches any entry
// in the signed origins array. Handles port normalization: the default HTTPS
// port (:443) is stripped from both sides before comparison. Case-insensitive.
func hostnameMatchesOrigin(hostname string, origins []string) bool {
	norm := normalizeOriginHost(hostname)
	for _, o := range origins {
		if normalizeOriginHost(o) == norm {
			return true
		}
	}
	return false
}

// normalizeOriginHost strips the default HTTPS port (:443) and lowercases
// for comparison. "example.com:443" → "example.com", "example.com:8000" →
// "example.com:8000", "Example.COM" → "example.com".
func normalizeOriginHost(s string) string {
	host, port, err := net.SplitHostPort(s)
	if err != nil {
		return strings.ToLower(s)
	}
	if port == "443" {
		return strings.ToLower(host)
	}
	return strings.ToLower(host) + ":" + port
}

// originCheck holds the result of verifying origin status from signed metadata.
type originCheck struct {
	Origins    []string // signed origins array (may be nil if field was absent)
	HasOrigins bool     // true if metadata contained an origins field
	IsOrigin   bool     // true if connected hostname is in signed origins
}

// checkOrigin extracts origins from verified metadata and checks if the
// connected hostname is listed. Returns nil if doc is nil.
func checkOrigin(doc map[string]interface{}, hostname string) *originCheck {
	if doc == nil {
		return nil
	}
	_, exists := doc["origins"]
	origins := extractOrigins(doc)
	if !exists {
		return &originCheck{HasOrigins: false}
	}
	return &originCheck{
		Origins:    origins,
		HasOrigins: true,
		IsOrigin:   hostnameMatchesOrigin(hostname, origins),
	}
}

// extractToken extracts the access token from a tltv:// URI target string.
// Returns "" if the target is not a URI or has no token.
func extractToken(target string) string {
	if strings.HasPrefix(target, tltvScheme) {
		if uri, err := parseTLTVUri(target); err == nil {
			return uri.Token
		}
	}
	return ""
}

// normalizeHost ensures a host has a port.
// Accepts full URLs for user-provided hosts (direct commands).
func normalizeHost(s string) string {
	if strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://") {
		return s
	}
	if _, _, err := net.SplitHostPort(s); err != nil {
		return s + ":443"
	}
	return s
}
