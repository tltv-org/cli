package main

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

// NodeInfo represents the response from /.well-known/tltv
type NodeInfo struct {
	Protocol string        `json:"protocol"`
	Versions []int         `json:"versions"`
	Channels []ChannelRef  `json:"channels"`
	Relaying []ChannelRef  `json:"relaying"`
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
	http *http.Client
}

// newClient creates a new TLTV HTTP client.
func newClient(insecure bool) *Client {
	tr := &http.Transport{
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
	}
}

// baseURL constructs the base URL for a host.
// Defaults to HTTPS, but uses HTTP for localhost/loopback.
func (c *Client) baseURL(host string) string {
	if strings.HasPrefix(host, "http://") || strings.HasPrefix(host, "https://") {
		return strings.TrimRight(host, "/")
	}

	// Normalize host: add default port if missing
	if _, _, err := net.SplitHostPort(host); err != nil {
		host = host + ":443"
	}

	// Detect local addresses -> use HTTP
	scheme := "https"
	hostPart, _, _ := net.SplitHostPort(host)
	if hostPart == "localhost" || hostPart == "127.0.0.1" || hostPart == "::1" || hostPart == "[::1]" {
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

// truncateBody returns the first 200 bytes of a response body for error display.
func truncateBody(body []byte) string {
	s := strings.TrimSpace(string(body))
	if len(s) > 200 {
		return s[:200] + "..."
	}
	return s
}

// parseTarget parses "channel_id@host:port" into components.
func parseTarget(s string) (channelID, host string, err error) {
	idx := strings.Index(s, "@")
	if idx < 0 {
		return "", "", fmt.Errorf("expected format: <channel_id>@<host[:port]>")
	}
	channelID = s[:idx]
	host = s[idx+1:]
	if host == "" {
		return "", "", fmt.Errorf("empty host")
	}
	return channelID, host, nil
}

// normalizeHost ensures a host has a port.
func normalizeHost(s string) string {
	if strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://") {
		return s
	}
	if _, _, err := net.SplitHostPort(s); err != nil {
		return s + ":443"
	}
	return s
}
