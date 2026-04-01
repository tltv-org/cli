package main

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// ---------- Types ----------

// bridgeChannel represents a discovered channel from a stream source.
type bridgeChannel struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Stream      string   `json:"stream"`
	Description string   `json:"description,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	Language    string   `json:"language,omitempty"`
	Logo        string   `json:"logo,omitempty"`
	Access      string   `json:"access,omitempty"`
	Token       string   `json:"token,omitempty"`
	OnDemand    bool     `json:"on_demand,omitempty"`
}

// bridgeGuideEntry represents a programme in a channel guide.
type bridgeGuideEntry struct {
	Channel     string `json:"channel,omitempty"` // only in JSON guide input
	Start       string `json:"start"`
	End         string `json:"end"`
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
	Category    string `json:"category,omitempty"`
}

// bridgeSidecar is the JSON schema for directory-mode sidecar files.
type bridgeSidecar struct {
	Name        string             `json:"name"`
	Description string             `json:"description"`
	Tags        []string           `json:"tags"`
	Language    string             `json:"language"`
	Logo        string             `json:"logo"`
	Access      string             `json:"access"`
	Token       string             `json:"token"`
	OnDemand    bool               `json:"on_demand"`
	Guide       []bridgeGuideEntry `json:"guide"`
}

// ---------- Source Client ----------

var bridgeSourceClient = &http.Client{Timeout: 30 * time.Second}

// ---------- Source Polling ----------

// bridgePollSource discovers channels from the --stream source.
// Returns channels and any embedded guide data (from sidecar JSON in directory mode).
func bridgePollSource(source, name string, onDemand bool) ([]bridgeChannel, map[string][]bridgeGuideEntry, error) {
	// Check if source is a local directory
	info, err := os.Stat(source)
	if err == nil && info.IsDir() {
		channels, guide, err := bridgeScanDirectory(source)
		if err != nil {
			return nil, nil, err
		}
		if onDemand {
			for i := range channels {
				channels[i].OnDemand = true
			}
		}
		return channels, guide, nil
	}

	// Fetch content (HTTP or local file)
	content, err := bridgeFetchContent(source)
	if err != nil {
		return nil, nil, fmt.Errorf("fetching stream source: %w", err)
	}

	// Detect format from content
	trimmed := strings.TrimSpace(string(content))

	var channels []bridgeChannel

	if len(trimmed) > 0 && (trimmed[0] == '[' || trimmed[0] == '{') {
		// JSON channel list
		channels, err = bridgeParseJSONChannels(content)
		if err != nil {
			return nil, nil, fmt.Errorf("parsing JSON channels: %w", err)
		}
	} else if bridgeIsM3UPlaylist(trimmed) {
		// M3U playlist
		channels = bridgeParseM3U(string(content), source)
	} else {
		// Single HLS stream
		if name == "" {
			return nil, nil, fmt.Errorf("--name is required for single-stream mode")
		}
		channels = []bridgeChannel{{
			ID:     bridgeSanitizeFilename(name),
			Name:   name,
			Stream: source,
		}}
	}

	if onDemand {
		for i := range channels {
			channels[i].OnDemand = true
		}
	}

	return channels, nil, nil
}

// bridgeIsM3UPlaylist checks if content looks like an IPTV M3U playlist
// (has #EXTINF lines but not HLS-specific tags).
func bridgeIsM3UPlaylist(content string) bool {
	if !strings.Contains(content, "#EXTINF:") {
		return false
	}
	// HLS manifests have these tags -- IPTV M3U playlists don't
	if strings.Contains(content, "#EXT-X-TARGETDURATION") ||
		strings.Contains(content, "#EXT-X-MEDIA-SEQUENCE") ||
		strings.Contains(content, "#EXT-X-STREAM-INF") {
		return false
	}
	return true
}

// ---------- M3U Parsing ----------

var bridgeM3UAttrRegex = regexp.MustCompile(`([\w-]+)="([^"]*)"`)

// bridgeParseM3U parses an IPTV M3U playlist into channels.
// sourceURL is used to resolve relative stream URLs.
func bridgeParseM3U(content, sourceURL string) []bridgeChannel {
	var channels []bridgeChannel
	lines := strings.Split(content, "\n")

	var current *bridgeChannel
	for _, line := range lines {
		line = strings.TrimRight(line, "\r")
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		if strings.HasPrefix(line, "#EXTINF:") {
			ch := bridgeChannel{}

			// Parse attributes: tvg-id="...", tvg-name="...", etc.
			attrs := bridgeM3UAttrRegex.FindAllStringSubmatch(line, -1)
			for _, match := range attrs {
				key, val := match[1], match[2]
				switch key {
				case "tvg-id":
					ch.ID = val
				case "tvg-name":
					ch.Name = val
				case "tvg-logo":
					ch.Logo = val
				case "group-title":
					if val != "" {
						ch.Tags = []string{val}
					}
				}
			}

			// Display name is after the last comma
			if idx := strings.LastIndex(line, ","); idx >= 0 {
				displayName := strings.TrimSpace(line[idx+1:])
				if ch.Name == "" {
					ch.Name = displayName
				}
			}

			current = &ch
			continue
		}

		// Non-comment, non-empty line after #EXTINF is the stream URL
		if current != nil && !strings.HasPrefix(line, "#") {
			current.Stream = bridgeResolveStreamURL(line, sourceURL)
			if current.ID == "" {
				current.ID = bridgeSanitizeFilename(current.Name)
			}
			if current.Name == "" {
				current.Name = current.ID
			}
			if current.Stream != "" && current.Name != "" {
				channels = append(channels, *current)
			}
			current = nil
		}
	}

	return channels
}

// bridgeResolveStreamURL resolves a stream URL relative to the source URL.
func bridgeResolveStreamURL(rawURL, sourceURL string) string {
	// Already absolute HTTP
	if strings.HasPrefix(rawURL, "http://") || strings.HasPrefix(rawURL, "https://") {
		return rawURL
	}

	// Resolve against HTTP source
	if strings.HasPrefix(sourceURL, "http://") || strings.HasPrefix(sourceURL, "https://") {
		base, err := url.Parse(sourceURL)
		if err == nil {
			ref, err := url.Parse(rawURL)
			if err == nil {
				return base.ResolveReference(ref).String()
			}
		}
		return rawURL
	}

	// Already absolute local path
	if filepath.IsAbs(rawURL) {
		return rawURL
	}

	// Resolve relative to local source directory
	dir := filepath.Dir(sourceURL)
	return filepath.Join(dir, rawURL)
}

// ---------- JSON Channel Parsing ----------

// bridgeParseJSONChannels parses a JSON channel list (array or single object).
func bridgeParseJSONChannels(data []byte) ([]bridgeChannel, error) {
	trimmed := strings.TrimSpace(string(data))
	if len(trimmed) == 0 {
		return nil, fmt.Errorf("empty JSON")
	}

	if trimmed[0] == '[' {
		var channels []bridgeChannel
		if err := json.Unmarshal(data, &channels); err != nil {
			return nil, err
		}
		return channels, nil
	}

	// Single channel object
	var ch bridgeChannel
	if err := json.Unmarshal(data, &ch); err != nil {
		return nil, err
	}
	return []bridgeChannel{ch}, nil
}

// ---------- Directory Scanning ----------

// bridgeScanDirectory scans a directory for .m3u8 files and optional sidecar .json files.
func bridgeScanDirectory(dir string) ([]bridgeChannel, map[string][]bridgeGuideEntry, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, nil, err
	}

	var channels []bridgeChannel
	guideMap := make(map[string][]bridgeGuideEntry)

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".m3u8") {
			continue
		}

		baseName := strings.TrimSuffix(entry.Name(), ".m3u8")
		absPath, err := filepath.Abs(filepath.Join(dir, entry.Name()))
		if err != nil {
			continue
		}

		ch := bridgeChannel{
			ID:     baseName,
			Name:   baseName,
			Stream: absPath,
		}

		// Check for sidecar JSON
		sidecarPath := filepath.Join(dir, baseName+".json")
		if data, err := os.ReadFile(sidecarPath); err == nil {
			var sc bridgeSidecar
			if json.Unmarshal(data, &sc) == nil {
				if sc.Name != "" {
					ch.Name = sc.Name
				}
				if sc.Description != "" {
					ch.Description = sc.Description
				}
				if len(sc.Tags) > 0 {
					ch.Tags = sc.Tags
				}
				if sc.Language != "" {
					ch.Language = sc.Language
				}
				if sc.Logo != "" {
					ch.Logo = sc.Logo
				}
				if sc.Access != "" {
					ch.Access = sc.Access
				}
				if sc.Token != "" {
					ch.Token = sc.Token
				}
				ch.OnDemand = sc.OnDemand
				if len(sc.Guide) > 0 {
					guideMap[baseName] = sc.Guide
				}
			}
		}

		channels = append(channels, ch)
	}

	return channels, guideMap, nil
}

// ---------- Guide Polling ----------

// bridgePollGuide fetches guide data from the --guide source.
// Returns entries grouped by upstream channel ID.
func bridgePollGuide(source string) (map[string][]bridgeGuideEntry, error) {
	content, err := bridgeFetchContent(source)
	if err != nil {
		return nil, fmt.Errorf("fetching guide source: %w", err)
	}

	trimmed := strings.TrimSpace(string(content))
	if len(trimmed) == 0 {
		return nil, nil
	}

	// Auto-detect format
	if trimmed[0] == '<' {
		return bridgeParseXMLTVGuide(content)
	}
	if trimmed[0] == '[' || trimmed[0] == '{' {
		return bridgeParseJSONGuide(content)
	}

	return nil, fmt.Errorf("unrecognized guide format (expected XMLTV or JSON)")
}

// ---------- XMLTV Parsing ----------

type bridgeXMLTVDoc struct {
	XMLName    xml.Name                `xml:"tv"`
	Programmes []bridgeXMLTVProgramme `xml:"programme"`
}

type bridgeXMLTVProgramme struct {
	Start    string `xml:"start,attr"`
	Stop     string `xml:"stop,attr"`
	Channel  string `xml:"channel,attr"`
	Title    string `xml:"title"`
	Desc     string `xml:"desc"`
	Category string `xml:"category"`
}

// bridgeParseXMLTVGuide parses XMLTV data into guide entries grouped by channel.
func bridgeParseXMLTVGuide(data []byte) (map[string][]bridgeGuideEntry, error) {
	var doc bridgeXMLTVDoc
	if err := xml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("parsing XMLTV: %w", err)
	}

	result := make(map[string][]bridgeGuideEntry)
	for _, p := range doc.Programmes {
		start, err := bridgeXMLTVToISO(p.Start)
		if err != nil {
			continue
		}
		end, err := bridgeXMLTVToISO(p.Stop)
		if err != nil {
			continue
		}

		entry := bridgeGuideEntry{
			Start:       start,
			End:         end,
			Title:       p.Title,
			Description: p.Desc,
			Category:    p.Category,
		}
		result[p.Channel] = append(result[p.Channel], entry)
	}

	return result, nil
}

// bridgeXMLTVToISO converts an XMLTV timestamp to ISO 8601 UTC.
// "20260315120000 +0000" -> "2026-03-15T12:00:00Z"
func bridgeXMLTVToISO(ts string) (string, error) {
	ts = strings.TrimSpace(ts)
	t, err := time.Parse("20060102150405 -0700", ts)
	if err != nil {
		return "", fmt.Errorf("invalid XMLTV timestamp %q: %w", ts, err)
	}
	return t.UTC().Format(timestampFormat), nil
}

// bridgeISOToXMLTV converts an ISO 8601 timestamp to XMLTV format.
// "2026-03-15T12:00:00Z" -> "20260315120000 +0000"
func bridgeISOToXMLTV(ts string) string {
	s := strings.TrimSpace(ts)
	s = strings.ReplaceAll(s, "-", "")
	s = strings.ReplaceAll(s, ":", "")
	s = strings.Replace(s, "T", "", 1)
	s = strings.TrimSuffix(s, "Z")
	return s + " +0000"
}

// ---------- JSON Guide Parsing ----------

// bridgeParseJSONGuide parses JSON guide data into entries grouped by channel.
func bridgeParseJSONGuide(data []byte) (map[string][]bridgeGuideEntry, error) {
	var entries []bridgeGuideEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, fmt.Errorf("parsing JSON guide: %w", err)
	}

	result := make(map[string][]bridgeGuideEntry)
	for _, e := range entries {
		if e.Channel != "" {
			result[e.Channel] = append(result[e.Channel], e)
		}
	}

	return result, nil
}

// ---------- Helpers ----------

const bridgeMaxSourceSize = 50 * 1024 * 1024 // 50 MB (XMLTV guides can be large)

// bridgeFetchContent fetches content from an HTTP URL or reads a local file.
func bridgeFetchContent(source string) ([]byte, error) {
	if strings.HasPrefix(source, "http://") || strings.HasPrefix(source, "https://") {
		resp, err := bridgeSourceClient.Get(source)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("HTTP %d from %s", resp.StatusCode, source)
		}
		return io.ReadAll(io.LimitReader(resp.Body, bridgeMaxSourceSize))
	}

	return os.ReadFile(source)
}

var bridgeSanitizeRegex = regexp.MustCompile(`[^a-zA-Z0-9_-]`)

// bridgeSanitizeFilename replaces non-alphanumeric characters with underscores.
func bridgeSanitizeFilename(s string) string {
	return bridgeSanitizeRegex.ReplaceAllString(s, "_")
}
