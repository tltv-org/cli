package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// ---------- HLS Manifest Parser ----------

// hlsSegmentInfo represents a segment extracted from an HLS manifest.
type hlsSegmentInfo struct {
	URI      string
	Duration float64 // seconds (from EXTINF)
	Sequence uint64  // absolute media sequence number
}

// hlsManifest represents a parsed HLS media playlist.
type hlsManifest struct {
	TargetDuration float64
	MediaSequence  uint64
	Segments       []hlsSegmentInfo
	EndList        bool
}

// parseHLSManifest parses a basic HLS media playlist, extracting segments,
// media sequence, and target duration. Follows the same parsing approach as
// ffmpeg's HLS demuxer: track EXT-X-MEDIA-SEQUENCE, enumerate EXTINF + URI pairs.
func parseHLSManifest(body []byte) (*hlsManifest, error) {
	lines := strings.Split(string(body), "\n")

	m := &hlsManifest{}
	var nextDuration float64
	hasExtM3U := false

	for _, line := range lines {
		line = strings.TrimRight(line, "\r")
		line = strings.TrimSpace(line)

		if line == "#EXTM3U" {
			hasExtM3U = true
			continue
		}

		if strings.HasPrefix(line, "#EXT-X-TARGETDURATION:") {
			val := strings.TrimPrefix(line, "#EXT-X-TARGETDURATION:")
			if d, err := strconv.ParseFloat(strings.TrimSpace(val), 64); err == nil {
				m.TargetDuration = d
			}
			continue
		}

		if strings.HasPrefix(line, "#EXT-X-MEDIA-SEQUENCE:") {
			val := strings.TrimPrefix(line, "#EXT-X-MEDIA-SEQUENCE:")
			if seq, err := strconv.ParseUint(strings.TrimSpace(val), 10, 64); err == nil {
				m.MediaSequence = seq
			}
			continue
		}

		if line == "#EXT-X-ENDLIST" {
			m.EndList = true
			continue
		}

		if strings.HasPrefix(line, "#EXTINF:") {
			val := strings.TrimPrefix(line, "#EXTINF:")
			// Format: duration[,title]
			if idx := strings.IndexByte(val, ','); idx >= 0 {
				val = val[:idx]
			}
			if d, err := strconv.ParseFloat(strings.TrimSpace(val), 64); err == nil {
				nextDuration = d
			}
			continue
		}

		// Skip other tags and empty lines
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		// This is a segment URI
		seq := m.MediaSequence + uint64(len(m.Segments))
		m.Segments = append(m.Segments, hlsSegmentInfo{
			URI:      line,
			Duration: nextDuration,
			Sequence: seq,
		})
		nextDuration = 0
	}

	if !hasExtM3U {
		return nil, fmt.Errorf("invalid HLS manifest: missing #EXTM3U")
	}

	return m, nil
}

// resolveSegmentURL resolves a segment URI relative to the manifest URL.
func resolveSegmentURL(manifestURL, segURI string) (string, error) {
	if strings.HasPrefix(segURI, "http://") || strings.HasPrefix(segURI, "https://") {
		return segURI, nil
	}
	parsed, err := url.Parse(manifestURL)
	if err != nil {
		return "", err
	}
	ref, err := url.Parse(segURI)
	if err != nil {
		return "", err
	}
	parsed.Path = path.Dir(parsed.Path) + "/"
	return parsed.ResolveReference(ref).String(), nil
}

// ---------- Master Playlist ----------

// hlsVariant represents a variant stream in an HLS master (multivariant) playlist.
type hlsVariant struct {
	URI        string
	Bandwidth  int
	Resolution string // "1920x1080" format
	Width      int
	Height     int
	Codecs     string
}

// parseMasterPlaylist detects and parses an HLS master (multivariant) playlist.
// Returns nil if the body is a media playlist (no #EXT-X-STREAM-INF tags).
func parseMasterPlaylist(body []byte) []hlsVariant {
	text := string(body)
	if !strings.Contains(text, "#EXT-X-STREAM-INF") {
		return nil
	}

	lines := strings.Split(text, "\n")
	var variants []hlsVariant
	var pending *hlsVariant

	for _, line := range lines {
		line = strings.TrimRight(line, "\r")
		line = strings.TrimSpace(line)

		if strings.HasPrefix(line, "#EXT-X-STREAM-INF:") {
			attrs := line[len("#EXT-X-STREAM-INF:"):]
			v := hlsVariant{}
			if bw := hlsAttr(attrs, "BANDWIDTH"); bw != "" {
				v.Bandwidth, _ = strconv.Atoi(bw)
			}
			if res := hlsAttr(attrs, "RESOLUTION"); res != "" {
				v.Resolution = res
				if parts := strings.SplitN(res, "x", 2); len(parts) == 2 {
					v.Width, _ = strconv.Atoi(parts[0])
					v.Height, _ = strconv.Atoi(parts[1])
				}
			}
			if codecs := hlsAttr(attrs, "CODECS"); codecs != "" {
				v.Codecs = codecs
			}
			pending = &v
			continue
		}

		// URI line following #EXT-X-STREAM-INF
		if pending != nil && line != "" && !strings.HasPrefix(line, "#") {
			pending.URI = line
			variants = append(variants, *pending)
			pending = nil
		}
	}

	return variants
}

// hlsAttr extracts an attribute value from an HLS tag's attribute list.
// Handles both quoted (CODECS="avc1.42c01e") and unquoted (BANDWIDTH=500000) values.
func hlsAttr(attrs, name string) string {
	search := name + "="
	// Scan for the attribute, ensuring we match the full name and not a
	// suffix (e.g. "BANDWIDTH" must not match inside "BANDWIDTHRATE").
	pos := 0
	for {
		idx := strings.Index(attrs[pos:], search)
		if idx < 0 {
			return ""
		}
		idx += pos // absolute position in attrs
		if idx == 0 || attrs[idx-1] == ',' || attrs[idx-1] == ':' || attrs[idx-1] == ' ' {
			// Valid attribute boundary — extract value
			val := attrs[idx+len(search):]
			if len(val) > 0 && val[0] == '"' {
				end := strings.IndexByte(val[1:], '"')
				if end < 0 {
					return "" // malformed: opening quote without closing quote
				}
				return val[1 : end+1]
			}
			end := strings.IndexByte(val, ',')
			if end < 0 {
				return strings.TrimSpace(val)
			}
			return strings.TrimSpace(val[:end])
		}
		pos = idx + len(search) // skip this false match, keep searching
	}
}

// selectVariant selects a variant based on quality preference.
// quality can be: "best" (highest bandwidth), "worst" (lowest bandwidth),
// or a resolution like "720p", "1080p", "360p" (closest match by height).
func selectVariant(variants []hlsVariant, quality string) hlsVariant {
	if len(variants) == 0 {
		return hlsVariant{}
	}

	switch quality {
	case "", "best":
		best := variants[0]
		for _, v := range variants[1:] {
			if v.Bandwidth > best.Bandwidth {
				best = v
			}
		}
		return best
	case "worst":
		worst := variants[0]
		for _, v := range variants[1:] {
			if v.Bandwidth < worst.Bandwidth {
				worst = v
			}
		}
		return worst
	default:
		// Parse resolution like "720p" → target height 720
		target := 0
		if strings.HasSuffix(quality, "p") {
			target, _ = strconv.Atoi(quality[:len(quality)-1])
		}
		if target == 0 {
			return selectVariant(variants, "best")
		}
		closest := variants[0]
		closestDiff := intAbs(closest.Height - target)
		for _, v := range variants[1:] {
			diff := intAbs(v.Height - target)
			if diff < closestDiff || (diff == closestDiff && v.Bandwidth > closest.Bandwidth) {
				closest = v
				closestDiff = diff
			}
		}
		return closest
	}
}

// intAbs returns the absolute value of x.
func intAbs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

// ---------- Receiver Core ----------

// ReceiverSegmentResult is reported for each segment fetch.
type ReceiverSegmentResult struct {
	Sequence    uint64
	Size        int
	Duration    float64 // EXTINF duration
	FetchTimeMs int64   // time to fetch the segment in ms
	CacheStatus string  // Cache-Status header value (HIT/MISS/empty)
	Err         error
}

// ReceiverManifestResult is reported for each manifest poll.
type ReceiverManifestResult struct {
	Segments    int
	NewSegments int
	FetchTimeMs int64
	Err         error
}

// ReceiverStats aggregates receiver statistics.
type ReceiverStats struct {
	mu                sync.Mutex
	SegmentsFetched   int64
	SegmentErrors     int64
	ManifestPolls     int64
	ManifestErrors    int64
	BytesReceived     int64
	CacheHits         int64
	CacheMisses       int64
	SegmentLatencies  []int64 // ms per segment fetch
	ManifestLatencies []int64 // ms per manifest poll
	StartTime         time.Time
	LastSegmentTime   time.Time
}

func (s *ReceiverStats) addSegment(r ReceiverSegmentResult) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r.Err != nil {
		s.SegmentErrors++
		return
	}
	s.SegmentsFetched++
	s.BytesReceived += int64(r.Size)
	s.SegmentLatencies = append(s.SegmentLatencies, r.FetchTimeMs)
	s.LastSegmentTime = time.Now()
	switch r.CacheStatus {
	case "HIT":
		s.CacheHits++
	case "MISS":
		s.CacheMisses++
	}
}

func (s *ReceiverStats) addManifest(r ReceiverManifestResult) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r.Err != nil {
		s.ManifestErrors++
		return
	}
	s.ManifestPolls++
	s.ManifestLatencies = append(s.ManifestLatencies, r.FetchTimeMs)
}

func (s *ReceiverStats) snapshot() *ReceiverStats {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := &ReceiverStats{
		SegmentsFetched: s.SegmentsFetched,
		SegmentErrors:   s.SegmentErrors,
		ManifestPolls:   s.ManifestPolls,
		ManifestErrors:  s.ManifestErrors,
		BytesReceived:   s.BytesReceived,
		CacheHits:       s.CacheHits,
		CacheMisses:     s.CacheMisses,
		StartTime:       s.StartTime,
		LastSegmentTime: s.LastSegmentTime,
	}
	cp.SegmentLatencies = make([]int64, len(s.SegmentLatencies))
	copy(cp.SegmentLatencies, s.SegmentLatencies)
	cp.ManifestLatencies = make([]int64, len(s.ManifestLatencies))
	copy(cp.ManifestLatencies, s.ManifestLatencies)
	return cp
}

// Receiver is a headless HLS stream consumer. It connects to a TLTV channel,
// fetches the manifest, downloads segments, and tracks statistics.
// Used by both `tltv receiver` (single instance) and `tltv loadtest` (N instances).
type Receiver struct {
	// Target is the tltv:// URI or direct HLS URL.
	Target string

	// DirectURL is a direct HLS manifest URL (bypasses tltv:// resolution).
	DirectURL string

	// Client is the HTTP client to use.
	Client *Client

	// OnSegment is called for each segment fetch (optional).
	OnSegment func(ReceiverSegmentResult)

	// OnManifest is called for each manifest poll (optional).
	OnManifest func(ReceiverManifestResult)

	// Stats collects aggregate statistics.
	Stats *ReceiverStats

	// VerifyMetadata enables periodic metadata signature verification.
	VerifyMetadata bool

	// RetryAttempts is the number of retry attempts for failed fetches (default 3).
	RetryAttempts int

	// RecordWriter receives raw segment data (optional).
	RecordWriter io.Writer

	// Quality selects which variant to use from master playlists.
	// "best" (default), "worst", or a resolution like "720p", "1080p".
	Quality string

	// LiveEdge is the number of segments from the live edge to start from on
	// the first manifest poll. 0 means start from all segments (default for
	// stats/record). 3 is the HLS-recommended value for live playback (--pipe).
	LiveEdge int

	// Failover is the node failover pool (nil = no failover).
	// When set, the receiver attempts to reconnect to alternate hosts after
	// consecutive manifest fetch failures.
	Failover *failoverPool

	// stopped is set when the receiver should stop.
	stopped atomic.Bool
}

// Run starts the receiver loop. It blocks until ctx is cancelled or an
// unrecoverable error occurs. Returns nil on clean shutdown.
func (recv *Receiver) Run(ctx context.Context) error {
	if recv.RetryAttempts <= 0 {
		recv.RetryAttempts = 3
	}
	if recv.Stats == nil {
		recv.Stats = &ReceiverStats{StartTime: time.Now()}
	}

	client := recv.Client
	if client == nil {
		client = newClient(flagInsecure)
	}

	// Resolve stream URL
	manifestURL, channelID, err := recv.resolveStreamURL(client)
	if err != nil {
		return fmt.Errorf("resolve: %w", err)
	}

	// Verify metadata if requested
	if recv.VerifyMetadata && channelID != "" {
		if err := recv.verifyChannelMetadata(client, channelID); err != nil {
			return fmt.Errorf("metadata verification: %w", err)
		}
	}

	// Master playlist resolution: if the first fetch returns a master playlist,
	// select a variant and switch to that variant's media playlist URL.
	{
		body, err := recv.fetchWithRetry(ctx, client, manifestURL)
		if err != nil {
			return fmt.Errorf("initial manifest fetch: %w", err)
		}
		if variants := parseMasterPlaylist(body); variants != nil {
			selected := selectVariant(variants, recv.Quality)
			if selected.URI == "" {
				return fmt.Errorf("no suitable variant found in master playlist")
			}
			resolved, err := resolveSegmentURL(manifestURL, selected.URI)
			if err != nil {
				return fmt.Errorf("resolve variant URL %q: %w", selected.URI, err)
			}
			if selected.Resolution != "" {
				logInfof("selected variant: %s (%d bps)", selected.Resolution, selected.Bandwidth)
			} else {
				logInfof("selected variant: %d bps", selected.Bandwidth)
			}
			manifestURL = resolved
		}
	}

	// Main receiver loop
	var lastSeq uint64
	var firstPoll bool = true
	var consecutiveManifestFailures int
	const failoverThreshold = 3 // consecutive manifest failures before attempting failover
	var metaVerifyTimer *time.Ticker
	if recv.VerifyMetadata && channelID != "" {
		metaVerifyTimer = time.NewTicker(5 * time.Minute)
		defer metaVerifyTimer.Stop()
	}

	for {
		if recv.stopped.Load() {
			return nil
		}

		select {
		case <-ctx.Done():
			return nil
		default:
		}

		// Fetch manifest
		start := time.Now()
		body, err := recv.fetchWithRetry(ctx, client, manifestURL)
		fetchMs := time.Since(start).Milliseconds()

		if err != nil {
			mr := ReceiverManifestResult{Err: err, FetchTimeMs: fetchMs}
			recv.Stats.addManifest(mr)
			if recv.OnManifest != nil {
				recv.OnManifest(mr)
			}

			consecutiveManifestFailures++

			// Attempt failover after N consecutive manifest failures.
			if recv.Failover != nil && consecutiveManifestFailures >= failoverThreshold {
				logInfof("node unreachable (%d consecutive failures), attempting failover...", consecutiveManifestFailures)
				failedOver := false
				for {
					candidate, ok := recv.Failover.NextHost(false)
					if !ok {
						break
					}
					candidateURL := recv.replaceManifestHost(manifestURL, candidate)
					logDebugf("trying failover candidate %s", candidate)
					probeBody, probeErr := recv.fetchWithRetry(ctx, client, candidateURL)
					if probeErr != nil {
						logDebugf("failover candidate %s failed: %v", candidate, probeErr)
						continue
					}
					// Verify we got a valid manifest (or master playlist).
					if _, parseErr := parseHLSManifest(probeBody); parseErr != nil {
						if parseMasterPlaylist(probeBody) == nil {
							logDebugf("failover candidate %s returned invalid manifest: %v", candidate, parseErr)
							continue
						}
					}
					// Success — switch to this host.
					recv.Failover.SwitchTo(candidate)
					manifestURL = candidateURL
					consecutiveManifestFailures = 0
					failedOver = true
					logInfof("failover successful: now connected to %s", candidate)
					break
				}
				if !failedOver {
					logErrorf("failover exhausted: all candidates tried, continuing with retry")
					recv.Failover.Reset()
				}
			}

			// Wait before retry
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(1 * time.Second):
			}
			continue
		}

		// Manifest fetched successfully — reset consecutive failure counter.
		consecutiveManifestFailures = 0

		manifest, err := parseHLSManifest(body)
		if err != nil {
			mr := ReceiverManifestResult{Err: err, FetchTimeMs: fetchMs}
			recv.Stats.addManifest(mr)
			if recv.OnManifest != nil {
				recv.OnManifest(mr)
			}
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(1 * time.Second):
			}
			continue
		}

		// On first poll with LiveEdge, skip to near the live edge.
		// This avoids downloading the entire manifest window, which causes
		// player stalls when piping (the player runs out of buffer while
		// the receiver downloads old segments sequentially).
		segments := manifest.Segments
		if firstPoll && recv.LiveEdge > 0 && len(segments) > recv.LiveEdge {
			segments = segments[len(segments)-recv.LiveEdge:]
		}

		// Count new segments
		newCount := 0
		for _, seg := range segments {
			if seg.Sequence > lastSeq || firstPoll {
				newCount++
			}
		}

		mr := ReceiverManifestResult{
			Segments:    len(manifest.Segments),
			NewSegments: newCount,
			FetchTimeMs: fetchMs,
		}
		recv.Stats.addManifest(mr)
		if recv.OnManifest != nil {
			recv.OnManifest(mr)
		}

		// Fetch new segments
		for _, seg := range segments {
			if !firstPoll && seg.Sequence <= lastSeq {
				continue
			}

			select {
			case <-ctx.Done():
				return nil
			default:
			}

			segURL, err := resolveSegmentURL(manifestURL, seg.URI)
			if err != nil {
				sr := ReceiverSegmentResult{Sequence: seg.Sequence, Err: err}
				recv.Stats.addSegment(sr)
				if recv.OnSegment != nil {
					recv.OnSegment(sr)
				}
				continue
			}

			segStart := time.Now()
			segData, cacheStatus, err := recv.fetchSegment(ctx, client, segURL)
			segMs := time.Since(segStart).Milliseconds()

			sr := ReceiverSegmentResult{
				Sequence:    seg.Sequence,
				Size:        len(segData),
				Duration:    seg.Duration,
				FetchTimeMs: segMs,
				CacheStatus: cacheStatus,
				Err:         err,
			}
			recv.Stats.addSegment(sr)
			if recv.OnSegment != nil {
				recv.OnSegment(sr)
			}

			// Write to record/pipe output if configured
			if err == nil && recv.RecordWriter != nil {
				recv.RecordWriter.Write(segData)
			}

			if err == nil && seg.Sequence > lastSeq {
				lastSeq = seg.Sequence
			}
		}

		firstPoll = false

		// Periodic metadata verification
		if metaVerifyTimer != nil {
			select {
			case <-metaVerifyTimer.C:
				if err := recv.verifyChannelMetadata(client, channelID); err != nil {
					logDebugf("metadata re-verification failed: %v", err)
				} else if recv.Failover != nil {
					// Refresh the failover pool from the latest verified metadata.
					recv.refreshFailoverPool(client, channelID)
				}
			default:
			}
		}

		// Wait for next poll (approximately half the target duration for freshness)
		pollInterval := time.Duration(manifest.TargetDuration*500) * time.Millisecond
		if pollInterval < 500*time.Millisecond {
			pollInterval = 500 * time.Millisecond
		}
		if pollInterval > 2*time.Second {
			pollInterval = 2 * time.Second
		}

		select {
		case <-ctx.Done():
			return nil
		case <-time.After(pollInterval):
		}
	}
}

// Stop signals the receiver to stop gracefully.
func (recv *Receiver) Stop() {
	recv.stopped.Store(true)
}

// resolveTarget parses the receiver's target into channel ID, host, and token.
// Handles tltv:// URIs, compact id@host, and bare hostnames.
func (recv *Receiver) resolveTarget(client *Client) (channelID, host, token string, err error) {
	channelID, host, err = parseTargetOrDiscover(recv.Target, client)
	if err != nil {
		err = fmt.Errorf("invalid target %q: %w", recv.Target, err)
		return
	}
	token = extractToken(recv.Target)
	return
}

// resolveStreamURL determines the HLS manifest URL from either a direct URL
// or a tltv:// URI.
func (recv *Receiver) resolveStreamURL(client *Client) (manifestURL, channelID string, err error) {
	if recv.DirectURL != "" {
		return recv.DirectURL, "", nil
	}

	id, host, token, err := recv.resolveTarget(client)
	if err != nil {
		return "", "", err
	}

	base := client.baseURL(host)
	manifestURL = base + "/tltv/v1/channels/" + id + "/stream.m3u8"
	if token != "" {
		manifestURL += "?token=" + token
	}
	return manifestURL, id, nil
}

// replaceManifestHost replaces the host:port in a manifest URL with newHost.
// Preserves the scheme, path, and query string.
func (recv *Receiver) replaceManifestHost(manifestURL, newHost string) string {
	parsed, err := url.Parse(manifestURL)
	if err != nil {
		return manifestURL // fallback: return unchanged
	}
	parsed.Host = newHost
	return parsed.String()
}

// verifyChannelMetadata fetches and verifies the channel's signed metadata.
// Also checks for unknown access modes per spec §5.2.
func (recv *Receiver) verifyChannelMetadata(client *Client, channelID string) error {
	_, host, token, err := recv.resolveTarget(client)
	if err != nil {
		return err
	}

	doc, err := client.FetchMetadata(host, channelID, token)
	if err != nil {
		return fmt.Errorf("fetch metadata: %w", err)
	}

	if err := verifyDocument(doc, channelID); err != nil {
		return fmt.Errorf("verify metadata: %w", err)
	}

	// Check for unknown access modes (spec §5.2)
	if err := checkAccessMode(doc); err != nil {
		return err
	}

	return nil
}

// refreshFailoverPool updates the failover pool from the current host's
// metadata and peers. Called periodically after successful metadata verification.
func (recv *Receiver) refreshFailoverPool(client *Client, channelID string) {
	if recv.Failover == nil {
		return
	}
	host := recv.Failover.CurrentHost()
	token := recv.Failover.Token()

	// Update from signed metadata (origins).
	doc, err := client.FetchMetadata(host, channelID, token)
	if err == nil {
		recv.Failover.UpdateFromMetadata(doc)
	}

	// Update from peers.
	peerExchange, err := client.FetchPeers(host)
	if err == nil {
		recv.Failover.UpdateFromPeers(peerExchange.Peers)
	}
}

// fetchWithRetry performs an HTTP GET with exponential backoff retries.
func (recv *Receiver) fetchWithRetry(ctx context.Context, client *Client, fetchURL string) ([]byte, error) {
	var lastErr error
	for attempt := 0; attempt < recv.RetryAttempts; attempt++ {
		if attempt > 0 {
			backoff := time.Duration(1<<uint(attempt-1)) * time.Second
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(backoff):
			}
		}

		req, err := http.NewRequestWithContext(ctx, "GET", fetchURL, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", "tltv-cli/"+version)

		resp, err := client.http.Do(req)
		if err != nil {
			lastErr = err
			continue
		}

		body, err := io.ReadAll(io.LimitReader(resp.Body, 10*1024*1024))
		resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			lastErr = fmt.Errorf("HTTP %d", resp.StatusCode)
			continue
		}

		if err != nil {
			lastErr = err
			continue
		}

		return body, nil
	}
	return nil, fmt.Errorf("after %d attempts: %w", recv.RetryAttempts, lastErr)
}

// fetchSegment fetches a single segment, returning data, cache status, and error.
func (recv *Receiver) fetchSegment(ctx context.Context, client *Client, segURL string) ([]byte, string, error) {
	var lastErr error
	for attempt := 0; attempt < recv.RetryAttempts; attempt++ {
		if attempt > 0 {
			backoff := time.Duration(1<<uint(attempt-1)) * time.Second
			select {
			case <-ctx.Done():
				return nil, "", ctx.Err()
			case <-time.After(backoff):
			}
		}

		req, err := http.NewRequestWithContext(ctx, "GET", segURL, nil)
		if err != nil {
			return nil, "", err
		}
		req.Header.Set("User-Agent", "tltv-cli/"+version)

		resp, err := client.http.Do(req)
		if err != nil {
			lastErr = err
			continue
		}

		data, err := io.ReadAll(io.LimitReader(resp.Body, hlsCacheMaxBody))
		resp.Body.Close()

		cacheStatus := resp.Header.Get("Cache-Status")

		if resp.StatusCode != http.StatusOK {
			lastErr = fmt.Errorf("HTTP %d", resp.StatusCode)
			continue
		}

		if err != nil {
			lastErr = err
			continue
		}

		return data, cacheStatus, nil
	}
	return nil, "", fmt.Errorf("after %d attempts: %w", recv.RetryAttempts, lastErr)
}

// ---------- Receiver Command ----------

func cmdReceiver(args []string) {
	fs := flag.NewFlagSet("receiver", flag.ExitOnError)

	// Mode flags
	monitor := fs.Bool("monitor", os.Getenv("MONITOR") == "1", "health check mode: exit 0 if stream is live, 1 if not")
	fs.BoolVar(monitor, "m", os.Getenv("MONITOR") == "1", "alias for --monitor")

	defaultTimeout := "10s"
	if v := os.Getenv("TIMEOUT"); v != "" {
		defaultTimeout = v
	}
	timeout := fs.String("timeout", defaultTimeout, "timeout for --monitor mode")
	fs.StringVar(timeout, "T", defaultTimeout, "alias for --timeout")

	defaultDuration := "0"
	if v := os.Getenv("DURATION"); v != "" {
		defaultDuration = v
	}
	duration := fs.String("duration", defaultDuration, "run for this long then exit (0 = until Ctrl-C)")
	fs.StringVar(duration, "d", defaultDuration, "alias for --duration")

	recordPath := fs.String("record", os.Getenv("RECORD"), "write raw TS segments to file")
	fs.StringVar(recordPath, "r", os.Getenv("RECORD"), "alias for --record")
	pipe := fs.Bool("pipe", os.Getenv("PIPE") == "1", "write raw segment data to stdout")
	fs.BoolVar(pipe, "p", os.Getenv("PIPE") == "1", "alias for --pipe")

	directURL := fs.String("url", os.Getenv("URL"), "direct HLS manifest URL (skip tltv:// resolution)")
	fs.StringVar(directURL, "u", os.Getenv("URL"), "alias for --url")
	quality := fs.String("quality", os.Getenv("QUALITY"), "variant quality: best (default), worst, or resolution (e.g. 720p)")
	fs.StringVar(quality, "q", os.Getenv("QUALITY"), "alias for --quality")
	proxyStr := addProxyFlag(fs)

	// --- Logging ---
	logLvl, logFmt, logPath := addLogFlags(fs)

	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Connect to a TLTV channel and consume the stream\n\n")
		fmt.Fprintf(os.Stderr, "Usage: tltv receiver [flags] <target>\n\n")
		fmt.Fprintf(os.Stderr, "Headless HLS client that connects to a TLTV channel, fetches the stream,\n")
		fmt.Fprintf(os.Stderr, "verifies protocol compliance, and reports live statistics.\n\n")
		fmt.Fprintf(os.Stderr, "Modes:\n")
		fmt.Fprintf(os.Stderr, "  -m, --monitor            health check: exit 0 if live, 1 if not\n")
		fmt.Fprintf(os.Stderr, "  -T, --timeout DURATION   monitor timeout (default: 10s)\n")
		fmt.Fprintf(os.Stderr, "  -d, --duration DURATION  run for N then exit with stats (0 = Ctrl-C)\n")
		fmt.Fprintf(os.Stderr, "  -r, --record PATH        write raw TS segments to file\n")
		fmt.Fprintf(os.Stderr, "  -p, --pipe               write raw segment data to stdout\n")
		fmt.Fprintf(os.Stderr, "  -u, --url URL            direct HLS manifest URL (skip resolution)\n")
		fmt.Fprintf(os.Stderr, "  -q, --quality QUALITY    variant: best (default), worst, or resolution (720p)\n")
		fmt.Fprintf(os.Stderr, "  -x, --proxy URL          proxy URL (socks5://, http://, https://)\n\n")
		fmt.Fprintf(os.Stderr, "Logging:\n")
		fmt.Fprintf(os.Stderr, "      --log-level LEVEL    log level: debug, info, error (default: info)\n")
		fmt.Fprintf(os.Stderr, "      --log-format FORMAT  log format: human, json (default: human)\n")
		fmt.Fprintf(os.Stderr, "      --log-file PATH      log to file instead of stderr\n\n")
		fmt.Fprintf(os.Stderr, "Examples:\n")
		fmt.Fprintf(os.Stderr, "  tltv receiver tltv://TVabc...@demo.timelooptv.org:443\n")
		fmt.Fprintf(os.Stderr, "  tltv receiver --monitor --timeout 10s TVabc...@demo.timelooptv.org:443\n")
		fmt.Fprintf(os.Stderr, "  tltv receiver --record out.ts TVabc...@localhost:8000\n")
		fmt.Fprintf(os.Stderr, "  tltv receiver --pipe TVabc...@localhost:8000 | mpv -\n")
		fmt.Fprintf(os.Stderr, "  tltv receiver --duration 5m --json TVabc...@demo.timelooptv.org:443\n\n")
		fmt.Fprintf(os.Stderr, "Environment variables: MONITOR=1, TIMEOUT, DURATION, RECORD, PIPE=1,\n")
		fmt.Fprintf(os.Stderr, "URL, QUALITY, LOG_LEVEL, LOG_FORMAT, LOG_FILE. Flags override env vars.\n")
	}
	fs.Parse(args)

	// Validate flags
	if *pipe && flagJSON {
		fmt.Fprintf(os.Stderr, "error: --pipe and --json are mutually exclusive\n")
		os.Exit(1)
	}
	if *pipe && *recordPath != "" {
		fmt.Fprintf(os.Stderr, "error: --pipe and --record are mutually exclusive\n")
		os.Exit(1)
	}

	target := fs.Arg(0)
	if target == "" && *directURL == "" {
		fmt.Fprintf(os.Stderr, "error: specify a target (tltv:// URI or id@host:port) or --url\n\n")
		fs.Usage()
		os.Exit(1)
	}

	// Set up logging
	if err := setupLogging(*logLvl, *logFmt, *logPath, "receiver"); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	stopLogReopen := startLogReopenWatcher()
	defer stopLogReopen()

	// Parse durations
	timeoutDur, err := time.ParseDuration(*timeout)
	if err != nil {
		fatal("invalid --timeout: %v", err)
	}

	var runDuration time.Duration
	if *duration != "0" {
		runDuration, err = time.ParseDuration(*duration)
		if err != nil {
			fatal("invalid --duration: %v", err)
		}
	}

	// Set up record writer
	var recordWriter io.Writer
	if *recordPath != "" {
		f, err := os.Create(*recordPath)
		if err != nil {
			fatal("cannot create %s: %v", *recordPath, err)
		}
		defer f.Close()
		recordWriter = f
	}
	if *pipe {
		recordWriter = os.Stdout
	}

	// Parse proxy URL
	proxyURL, err := parseProxyURL(*proxyStr)
	if err != nil {
		fatal("%v", err)
	}

	// Create receiver
	stats := &ReceiverStats{StartTime: time.Now()}
	recv := &Receiver{
		Target:         target,
		DirectURL:      *directURL,
		Client:         newClientWithProxy(flagInsecure, proxyURL),
		Stats:          stats,
		VerifyMetadata: !(*directURL != ""),
		RecordWriter:   recordWriter,
		Quality:        *quality,
	}

	// For --pipe, start from near the live edge (last 3 segments per HLS spec)
	// instead of downloading the entire manifest window. This prevents player
	// stalls caused by sequential downloads filling a stale buffer.
	if *pipe {
		recv.LiveEdge = 3
	}

	// Build failover pool from target (tltv:// URI or compact format).
	// Only when we have a resolvable target (not --url direct mode).
	if target != "" && *directURL == "" {
		recv.Failover = buildReceiverFailoverPool(target, recv.Client)
	}

	// Monitor mode: connect, check stream, exit
	if *monitor {
		ctx, cancel := context.WithTimeout(context.Background(), timeoutDur)
		defer cancel()

		// Just try to get one successful manifest + segment
		recv.OnSegment = func(sr ReceiverSegmentResult) {
			if sr.Err == nil {
				cancel() // success — stop immediately
			}
		}

		err := recv.Run(ctx)
		snap := stats.snapshot()

		if snap.SegmentsFetched > 0 {
			if !flagJSON {
				fmt.Fprintf(os.Stderr, "receiver: stream live (%d segments fetched)\n", snap.SegmentsFetched)
			} else {
				json.NewEncoder(os.Stdout).Encode(map[string]interface{}{
					"status":   "live",
					"segments": snap.SegmentsFetched,
				})
			}
			os.Exit(0)
		}

		if !flagJSON {
			errMsg := "stream unavailable"
			if err != nil {
				errMsg = err.Error()
			}
			fmt.Fprintf(os.Stderr, "receiver: %s\n", errMsg)
		} else {
			json.NewEncoder(os.Stdout).Encode(map[string]interface{}{
				"status": "unavailable",
				"error":  fmt.Sprintf("%v", err),
			})
		}
		os.Exit(1)
	}

	// Normal/duration mode
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if runDuration > 0 {
		ctx, cancel = context.WithTimeout(ctx, runDuration)
		defer cancel()
	}

	// Signal handler
	sigCh := make(chan os.Signal, 1)
	signalNotify(sigCh)
	go func() {
		<-sigCh
		cancel()
	}()

	// Status ticker for live output
	if !flagJSON && !*pipe {
		go receiverStatusLoop(ctx, stats)
	}

	// Run receiver
	recv.OnSegment = func(sr ReceiverSegmentResult) {
		if sr.Err != nil {
			logDebugf("segment %d error: %v", sr.Sequence, sr.Err)
		} else {
			logDebugf("segment %d: %d bytes, %dms, cache=%s",
				sr.Sequence, sr.Size, sr.FetchTimeMs, sr.CacheStatus)
		}
	}
	recv.OnManifest = func(mr ReceiverManifestResult) {
		if mr.Err != nil {
			logDebugf("manifest error: %v", mr.Err)
		}
	}

	if err := recv.Run(ctx); err != nil && ctx.Err() == nil {
		logErrorf("%v", err)
	}

	// Print summary
	snap := stats.snapshot()
	if flagJSON {
		receiverPrintJSON(snap)
	} else if !*pipe {
		receiverPrintSummary(snap)
	}
}

// receiverStatusLoop prints live stats every 10 seconds.
func receiverStatusLoop(ctx context.Context, stats *ReceiverStats) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			snap := stats.snapshot()
			elapsed := time.Since(snap.StartTime).Seconds()
			var bwMbps float64
			if elapsed > 0 {
				bwMbps = float64(snap.BytesReceived) * 8 / elapsed / 1_000_000
			}
			cacheStr := ""
			total := snap.CacheHits + snap.CacheMisses
			if total > 0 {
				cacheStr = fmt.Sprintf("  cache: %.1f%%", float64(snap.CacheHits)/float64(total)*100)
			}
			fmt.Fprintf(os.Stderr, "[%4.0fs] seg: %d  ok: %d  err: %d  bw: %.1f Mbps%s\n",
				elapsed, snap.SegmentsFetched+snap.SegmentErrors,
				snap.SegmentsFetched, snap.SegmentErrors, bwMbps, cacheStr)
		}
	}
}

// receiverPrintSummary prints a human-readable summary of receiver stats.
func receiverPrintSummary(snap *ReceiverStats) {
	elapsed := time.Since(snap.StartTime)
	fmt.Fprintf(os.Stderr, "\nreceiver: %d segments, %d errors, %s\n",
		snap.SegmentsFetched, snap.SegmentErrors, elapsed.Round(time.Second))

	if len(snap.SegmentLatencies) > 0 {
		sorted := make([]int64, len(snap.SegmentLatencies))
		copy(sorted, snap.SegmentLatencies)
		sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
		fmt.Fprintf(os.Stderr, "  segment latency: p50=%dms p95=%dms p99=%dms\n",
			percentile(sorted, 50), percentile(sorted, 95), percentile(sorted, 99))
	}

	total := snap.CacheHits + snap.CacheMisses
	if total > 0 {
		fmt.Fprintf(os.Stderr, "  cache: %d hits, %d misses (%.1f%% hit rate)\n",
			snap.CacheHits, snap.CacheMisses,
			float64(snap.CacheHits)/float64(total)*100)
	}

	if elapsed.Seconds() > 0 {
		bwMbps := float64(snap.BytesReceived) * 8 / elapsed.Seconds() / 1_000_000
		fmt.Fprintf(os.Stderr, "  bandwidth: %.1f Mbps (%s)\n",
			bwMbps, formatBytes(snap.BytesReceived))
	}
}

// receiverPrintJSON prints receiver stats as JSON.
func receiverPrintJSON(snap *ReceiverStats) {
	result := map[string]interface{}{
		"segments_fetched": snap.SegmentsFetched,
		"segment_errors":   snap.SegmentErrors,
		"manifest_polls":   snap.ManifestPolls,
		"manifest_errors":  snap.ManifestErrors,
		"bytes_received":   snap.BytesReceived,
		"duration_ms":      time.Since(snap.StartTime).Milliseconds(),
	}

	if len(snap.SegmentLatencies) > 0 {
		sorted := make([]int64, len(snap.SegmentLatencies))
		copy(sorted, snap.SegmentLatencies)
		sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
		result["segment_latency_p50_ms"] = percentile(sorted, 50)
		result["segment_latency_p95_ms"] = percentile(sorted, 95)
		result["segment_latency_p99_ms"] = percentile(sorted, 99)
	}

	total := snap.CacheHits + snap.CacheMisses
	if total > 0 {
		result["cache_hits"] = snap.CacheHits
		result["cache_misses"] = snap.CacheMisses
		result["cache_hit_rate"] = float64(snap.CacheHits) / float64(total)
	}

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	enc.Encode(result)
}

// ---------- Failover Pool Setup ----------

// buildReceiverFailoverPool builds a failover pool from a target string.
// Seeds with URI hints, then tries to fetch metadata and peers to populate
// origins and relays. Returns nil if the target cannot be parsed.
func buildReceiverFailoverPool(target string, client *Client) *failoverPool {
	var channelID, host, token string
	var uriHints []string

	if strings.HasPrefix(target, tltvScheme) {
		uri, err := parseTLTVUri(target)
		if err != nil || uri.ChannelID == "" || len(uri.Hints) == 0 {
			return nil
		}
		channelID = uri.ChannelID
		host = uri.Hints[0]
		token = uri.Token
		if len(uri.Hints) > 1 {
			uriHints = uri.Hints[1:]
		}
	} else {
		// Compact format: ID@host:port
		id, h, err := parseTarget(target)
		if err != nil {
			return nil
		}
		channelID = id
		host = h
		token = ""
	}

	pool := newFailoverPool(channelID, host, token, uriHints)

	// Try to seed from signed metadata (origins).
	doc, err := client.FetchMetadata(host, channelID, token)
	if err == nil {
		pool.UpdateFromMetadata(doc)
	}

	// Try to seed from peers.
	peerExchange, err := client.FetchPeers(host)
	if err == nil {
		pool.UpdateFromPeers(peerExchange.Peers)
	}

	return pool
}

// ---------- Helpers ----------

// percentile returns the Pth percentile from a sorted slice using the
// nearest-rank method: rank = ceil(p/100 * n).
func percentile(sorted []int64, p int) int64 {
	if len(sorted) == 0 {
		return 0
	}
	// Ceiling integer division: (p*n + 99) / 100
	rank := (p*len(sorted) + 99) / 100
	if rank < 1 {
		rank = 1
	}
	if rank > len(sorted) {
		rank = len(sorted)
	}
	return sorted[rank-1]
}

// formatBytes formats a byte count as a human-readable string.
func formatBytes(b int64) string {
	switch {
	case b >= 1024*1024*1024:
		return fmt.Sprintf("%.1f GB", float64(b)/1024/1024/1024)
	case b >= 1024*1024:
		return fmt.Sprintf("%.1f MB", float64(b)/1024/1024)
	case b >= 1024:
		return fmt.Sprintf("%.1f KB", float64(b)/1024)
	default:
		return fmt.Sprintf("%d B", b)
	}
}
