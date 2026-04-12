package main

// Buffered relay: proactive segment fetching and time-delayed serving.
//
// When --buffer is enabled, the relay pre-fetches HLS segments from upstream
// and stores them in per-channel buffers. This provides:
//
//   1. Resilience: if the origin dies, the relay continues serving from
//      buffered manifests and segments.
//   2. Time delay: with --delay, the relay serves content from an earlier
//      position in the buffer, enabling time-shifted rebroadcast.
//
// Architecture:
//   - relayBufferManager owns all per-channel buffers and enforces total memory
//   - relayBufferedChannel tracks the root manifest and per-playlist buffers
//   - relayChannelBuffer is a per-media-playlist segment store (map by seqNum)
//   - relayBufferFetchLoop is a goroutine per channel that polls upstream
//   - relayServer checks the buffer before proxying upstream

import (
	"context"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// relayBufferManager tracks all per-channel buffers and enforces a total
// memory limit across all channels.
type relayBufferManager struct {
	mu             sync.RWMutex
	buffers        map[string]*relayBufferedChannel // channelID → buffered channel
	maxMem         int64                            // total max memory bytes (default 1 GB)
	totalMem       atomic.Int64                     // current total across all buffers
	delay          time.Duration                    // global delay for all channels
	bufferDuration time.Duration                    // requested buffer duration per channel
	fetchCancel    map[string]context.CancelFunc    // channelID → proactive fetch loop cancel
}

type relayBufferedChannel struct {
	mu           sync.RWMutex
	rootManifest string                         // cached top-level master playlist (if upstream is multivariant)
	rootIsMaster bool                           // true when rootManifest is a master playlist
	streams      map[string]*relayChannelBuffer // subPath → media-playlist buffer
}

func newRelayBufferManager(maxMem int64, delay time.Duration) *relayBufferManager {
	return &relayBufferManager{
		buffers:     make(map[string]*relayBufferedChannel),
		maxMem:      maxMem,
		delay:       delay,
		fetchCancel: make(map[string]context.CancelFunc),
	}
}

func newRelayBufferedChannel() *relayBufferedChannel {
	return &relayBufferedChannel{streams: make(map[string]*relayChannelBuffer)}
}

func newRelayChannelBuffer(maxSegments int, delay time.Duration, manager *relayBufferManager) *relayChannelBuffer {
	if maxSegments < 1 {
		maxSegments = 1
	}
	return &relayChannelBuffer{
		segments:    make(map[uint64]*relayBufferSegment),
		pathIndex:   make(map[string]uint64),
		maxSegments: maxSegments,
		delay:       delay,
		manager:     manager,
	}
}

func (m *relayBufferManager) ensureChannelLocked(channelID string) *relayBufferedChannel {
	ch, ok := m.buffers[channelID]
	if !ok {
		ch = newRelayBufferedChannel()
		m.buffers[channelID] = ch
	}
	return ch
}

func (m *relayBufferManager) initialMaxSegments() int {
	if m.bufferDuration <= 0 {
		return 10
	}
	est := int(m.bufferDuration.Seconds() / 6)
	if est < 10 {
		est = 10
	}
	return est
}

// GetBuffer returns the primary stream buffer for a channel (nil if not buffered).
func (m *relayBufferManager) GetBuffer(channelID string) *relayChannelBuffer {
	return m.GetStreamBuffer(channelID, "stream.m3u8")
}

// GetStreamBuffer returns the buffer for a specific media playlist path.
func (m *relayBufferManager) GetStreamBuffer(channelID, streamPath string) *relayChannelBuffer {
	m.mu.RLock()
	ch := m.buffers[channelID]
	m.mu.RUnlock()
	if ch == nil {
		return nil
	}
	ch.mu.RLock()
	defer ch.mu.RUnlock()
	return ch.streams[streamPath]
}

// AddBuffer creates and registers the primary stream buffer for a channel.
func (m *relayBufferManager) AddBuffer(channelID string, maxSegments int) *relayChannelBuffer {
	return m.EnsureStreamBuffer(channelID, "stream.m3u8", maxSegments)
}

// EnsureStreamBuffer creates a media-playlist buffer if it does not already exist.
func (m *relayBufferManager) EnsureStreamBuffer(channelID, streamPath string, maxSegments int) *relayChannelBuffer {
	m.mu.Lock()
	ch := m.ensureChannelLocked(channelID)
	m.mu.Unlock()

	ch.mu.Lock()
	defer ch.mu.Unlock()
	if buf, ok := ch.streams[streamPath]; ok {
		return buf
	}
	if maxSegments <= 0 {
		maxSegments = m.initialMaxSegments()
	}
	buf := newRelayChannelBuffer(maxSegments, m.delay, m)
	ch.streams[streamPath] = buf
	return buf
}

// SetRootManifest stores the top-level master playlist for a channel.
func (m *relayBufferManager) SetRootManifest(channelID, manifest string, isMaster bool) {
	m.mu.RLock()
	ch := m.buffers[channelID]
	m.mu.RUnlock()
	if ch == nil {
		return
	}
	ch.mu.Lock()
	defer ch.mu.Unlock()
	ch.rootManifest = manifest
	ch.rootIsMaster = isMaster
}

// GetRootManifest returns the cached top-level master playlist for a channel.
func (m *relayBufferManager) GetRootManifest(channelID string) string {
	m.mu.RLock()
	ch := m.buffers[channelID]
	m.mu.RUnlock()
	if ch == nil {
		return ""
	}
	ch.mu.RLock()
	defer ch.mu.RUnlock()
	if !ch.rootIsMaster {
		return ""
	}
	return ch.rootManifest
}

// GetManifest returns a generated media playlist for the given sub-path.
func (m *relayBufferManager) GetManifest(channelID, streamPath string) string {
	buf := m.GetStreamBuffer(channelID, streamPath)
	if buf == nil {
		return ""
	}
	return buf.GetManifest()
}

// GetSegmentByPath returns a buffered segment by its served sub-path.
func (m *relayBufferManager) GetSegmentByPath(channelID, subPath string) []byte {
	m.mu.RLock()
	ch := m.buffers[channelID]
	m.mu.RUnlock()
	if ch == nil {
		return nil
	}
	ch.mu.RLock()
	defer ch.mu.RUnlock()
	for _, buf := range ch.streams {
		if data := buf.GetSegmentByPath(subPath); data != nil {
			return data
		}
	}
	return nil
}

// StartBuffering starts the proactive fetch loop for a channel if it is not already running.
func (m *relayBufferManager) StartBuffering(ctx context.Context, channelID string, registry *relayRegistry, httpClient *http.Client) {
	m.mu.Lock()
	if _, ok := m.fetchCancel[channelID]; ok {
		m.mu.Unlock()
		return
	}
	_ = m.ensureChannelLocked(channelID)
	loopCtx, cancel := context.WithCancel(ctx)
	m.fetchCancel[channelID] = cancel
	m.mu.Unlock()
	go relayBufferFetchLoop(loopCtx, channelID, m, registry, httpClient)
}

// RemoveBuffer removes a channel's buffers, stops its fetch loop, and frees its memory.
func (m *relayBufferManager) RemoveBuffer(channelID string) {
	m.mu.Lock()
	cancel := m.fetchCancel[channelID]
	delete(m.fetchCancel, channelID)
	ch := m.buffers[channelID]
	delete(m.buffers, channelID)
	m.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	if ch == nil {
		return
	}

	var freed int64
	ch.mu.RLock()
	streams := make([]*relayChannelBuffer, 0, len(ch.streams))
	for _, buf := range ch.streams {
		streams = append(streams, buf)
	}
	ch.mu.RUnlock()
	for _, buf := range streams {
		buf.mu.Lock()
		freed += buf.memBytes
		buf.mu.Unlock()
	}
	if freed != 0 {
		m.totalMem.Add(-freed)
	}
}

// TotalMemory returns the current total memory usage.
func (m *relayBufferManager) TotalMemory() int64 {
	return m.totalMem.Load()
}

// OverMemLimit returns true if total memory exceeds the configured limit.
func (m *relayBufferManager) OverMemLimit() bool {
	return m.totalMem.Load() > m.maxMem
}

// ---------- Per-Channel Media Buffer ----------

// relayChannelBuffer stores segments for one media playlist in a map keyed by
// sequence number. Evicts oldest segments when full.
type relayChannelBuffer struct {
	mu          sync.RWMutex
	segments    map[uint64]*relayBufferSegment
	pathIndex   map[string]uint64
	maxSegments int
	delay       time.Duration
	manager     *relayBufferManager

	// Tracking
	oldestSeq uint64
	newestSeq uint64
	hasData   bool
	memBytes  int64

	// Upstream manifest state (from last successful poll)
	targetDuration float64
	upstreamSeq    uint64 // media sequence from upstream manifest
}

type relayBufferSegment struct {
	SeqNum   uint64
	Path     string
	Data     []byte
	Duration float64
	Fetched  time.Time
}

// PushSegment adds a segment using the default relay path format (seg{N}.ts).
func (b *relayChannelBuffer) PushSegment(seqNum uint64, data []byte, duration float64) {
	b.PushNamedSegment(seqNum, fmt.Sprintf("seg%d.ts", seqNum), data, duration)
}

// PushNamedSegment adds a segment to the buffer under the given served path.
// Evicts oldest if over capacity or if the global memory limit is exceeded.
func (b *relayChannelBuffer) PushNamedSegment(seqNum uint64, segPath string, data []byte, duration float64) {
	b.mu.Lock()
	defer b.mu.Unlock()

	// Don't re-add if we already have it.
	if _, exists := b.segments[seqNum]; exists {
		return
	}

	seg := &relayBufferSegment{
		SeqNum:   seqNum,
		Path:     segPath,
		Data:     data,
		Duration: duration,
		Fetched:  time.Now(),
	}

	b.segments[seqNum] = seg
	if segPath != "" {
		b.pathIndex[segPath] = seqNum
	}
	b.memBytes += int64(len(data))
	b.manager.totalMem.Add(int64(len(data)))

	if !b.hasData || seqNum > b.newestSeq {
		b.newestSeq = seqNum
	}
	if !b.hasData || seqNum < b.oldestSeq {
		b.oldestSeq = seqNum
	}
	b.hasData = true

	// Evict oldest segments if over capacity or memory limit.
	b.evictLocked()
}

// evictLocked removes oldest segments while over capacity or memory.
// Must be called with b.mu held.
func (b *relayChannelBuffer) evictLocked() {
	for len(b.segments) > b.maxSegments || b.manager.OverMemLimit() {
		if len(b.segments) <= 1 {
			break // keep at least one
		}
		oldest, ok := b.segments[b.oldestSeq]
		if !ok {
			// oldestSeq is stale, find actual oldest.
			b.recalcOldestLocked()
			oldest, ok = b.segments[b.oldestSeq]
			if !ok {
				break
			}
		}
		freed := int64(len(oldest.Data))
		delete(b.segments, b.oldestSeq)
		if oldest.Path != "" {
			delete(b.pathIndex, oldest.Path)
		}
		b.memBytes -= freed
		b.manager.totalMem.Add(-freed)
		b.recalcOldestLocked()
	}
}

// recalcOldestLocked finds the actual oldest sequence number in the map.
func (b *relayChannelBuffer) recalcOldestLocked() {
	if len(b.segments) == 0 {
		b.hasData = false
		b.oldestSeq = 0
		b.newestSeq = 0
		return
	}
	min := b.newestSeq
	for k := range b.segments {
		if k < min {
			min = k
		}
	}
	b.oldestSeq = min
}

// GetSegment returns segment data by sequence number, or nil if not buffered.
func (b *relayChannelBuffer) GetSegment(seqNum uint64) []byte {
	b.mu.RLock()
	defer b.mu.RUnlock()
	if seg, ok := b.segments[seqNum]; ok {
		return seg.Data
	}
	return nil
}

// GetSegmentByPath returns segment data by its served sub-path.
func (b *relayChannelBuffer) GetSegmentByPath(segPath string) []byte {
	b.mu.RLock()
	defer b.mu.RUnlock()
	if seqNum, ok := b.pathIndex[segPath]; ok {
		if seg, ok := b.segments[seqNum]; ok {
			return seg.Data
		}
	}
	return nil
}

// GetManifest generates an HLS media playlist from buffered segments.
// If delay > 0, the playlist window is shifted back by the delay duration.
// Returns "" if the buffer has no segments.
func (b *relayChannelBuffer) GetManifest() string {
	b.mu.RLock()
	defer b.mu.RUnlock()

	if !b.hasData || len(b.segments) == 0 {
		return ""
	}

	// Collect segments sorted by sequence number.
	segs := make([]*relayBufferSegment, 0, len(b.segments))
	for _, s := range b.segments {
		segs = append(segs, s)
	}
	sort.Slice(segs, func(i, j int) bool { return segs[i].SeqNum < segs[j].SeqNum })

	// Apply delay: exclude segments newer than (now - delay).
	cutoff := time.Now().Add(-b.delay)
	var visible []*relayBufferSegment
	for _, s := range segs {
		if b.delay > 0 && s.Fetched.After(cutoff) {
			break // segments are time-ordered; once past cutoff, done
		}
		visible = append(visible, s)
	}

	if len(visible) == 0 {
		return "" // all segments are within the delay window
	}

	// Use a sliding window like live HLS: last N segments where N ~= 3-5.
	windowSize := 5
	if b.targetDuration > 0 && visible[0].Duration > 0 {
		windowSize = int(3*b.targetDuration/visible[0].Duration + 1)
		if windowSize < 3 {
			windowSize = 3
		}
	}
	if len(visible) > windowSize {
		visible = visible[len(visible)-windowSize:]
	}

	// Generate manifest.
	td := int(b.targetDuration)
	if td < 1 {
		td = int(visible[0].Duration + 0.999)
	}

	var sb strings.Builder
	sb.WriteString("#EXTM3U\n")
	sb.WriteString("#EXT-X-VERSION:3\n")
	fmt.Fprintf(&sb, "#EXT-X-TARGETDURATION:%d\n", td)
	fmt.Fprintf(&sb, "#EXT-X-MEDIA-SEQUENCE:%d\n", visible[0].SeqNum)
	sb.WriteString("\n")

	for _, s := range visible {
		name := s.Path
		if name == "" {
			name = fmt.Sprintf("seg%d.ts", s.SeqNum)
		}
		fmt.Fprintf(&sb, "#EXTINF:%.6f,\n", s.Duration)
		fmt.Fprintf(&sb, "%s\n", name)
	}

	return sb.String()
}

// SegmentCount returns the number of segments currently buffered.
func (b *relayChannelBuffer) SegmentCount() int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.segments)
}

// MemoryBytes returns current memory usage of this buffer.
func (b *relayChannelBuffer) MemoryBytes() int64 {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.memBytes
}

// SetTargetDuration updates the target duration from an upstream manifest and
// resizes the ring capacity to match the configured --buffer duration.
func (b *relayChannelBuffer) SetTargetDuration(td float64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.targetDuration = td
	if td <= 0 || b.manager == nil || b.manager.bufferDuration <= 0 {
		return
	}
	desired := int(math.Ceil(b.manager.bufferDuration.Seconds() / td))
	if desired < 1 {
		desired = 1
	}
	if desired == b.maxSegments {
		return
	}
	b.maxSegments = desired
	b.evictLocked()
}

// ---------- Proactive Fetch Loop ----------

// relayBufferFetchLoop polls upstream manifests and pre-fetches new segments.
// One goroutine per buffered channel.
func relayBufferFetchLoop(ctx context.Context, channelID string, mgr *relayBufferManager, registry *relayRegistry, httpClient *http.Client) {
	// Initial poll interval: 1 second (will adjust after the first successful manifest).
	pollInterval := time.Second
	lastSeq := make(map[string]uint64) // media-playlist path → highest fetched sequence

	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(pollInterval):
		}

		ch := registry.GetChannel(channelID)
		if ch == nil || ch.StreamHint == "" {
			continue
		}

		// Build manifest URL from stream hint.
		manifestURL := buildBufferManifestURL(ch)
		if manifestURL == "" {
			continue
		}

		// Fetch top-level manifest.
		manifestBody, err := bufferFetchURL(ctx, httpClient, manifestURL)
		if err != nil {
			logDebugf("buffer[%s]: manifest fetch: %v", channelID[:12], err)
			continue
		}

		// Master playlist path: cache the root manifest and buffer each media playlist it references.
		mediaRefs := parseMasterMediaURIs(manifestBody)
		if len(mediaRefs) > 0 {
			mgr.SetRootManifest(channelID, string(rewriteManifest(manifestURL, manifestBody, "")), true)
			minTargetDuration := 0.0
			for _, ref := range mediaRefs {
				streamPath := bufferServedSubPath(manifestURL, ref)
				if streamPath == "" {
					continue
				}
				mediaURL, err := resolveSegmentURL(manifestURL, ref)
				if err != nil || mediaURL == "" {
					continue
				}
				td, err := relayBufferFetchMediaPlaylist(ctx, mgr, channelID, streamPath, mediaURL, httpClient, lastSeq, nil)
				if err != nil {
					logDebugf("buffer[%s]: media playlist %s: %v", channelID[:12], streamPath, err)
					continue
				}
				if td > 0 && (minTargetDuration == 0 || td < minTargetDuration) {
					minTargetDuration = td
				}
			}
			if minTargetDuration > 0 {
				pollInterval = relayBufferPollInterval(minTargetDuration)
			} else {
				pollInterval = 5 * time.Second
			}
			continue
		}

		// Single media-playlist path.
		mgr.SetRootManifest(channelID, "", false)
		td, err := relayBufferFetchMediaPlaylist(ctx, mgr, channelID, "stream.m3u8", manifestURL, httpClient, lastSeq, manifestBody)
		if err != nil {
			logDebugf("buffer[%s]: manifest parse: %v", channelID[:12], err)
			continue
		}
		if td > 0 {
			pollInterval = relayBufferPollInterval(td)
		}
	}
}

func relayBufferPollInterval(targetDuration float64) time.Duration {
	newInterval := time.Duration(targetDuration*500) * time.Millisecond
	if newInterval < 500*time.Millisecond {
		newInterval = 500 * time.Millisecond
	}
	return newInterval
}

func relayBufferFetchMediaPlaylist(ctx context.Context, mgr *relayBufferManager, channelID, streamPath, playlistURL string, httpClient *http.Client, lastSeq map[string]uint64, prefetchedBody []byte) (float64, error) {
	body := prefetchedBody
	if body == nil {
		var err error
		body, err = bufferFetchURL(ctx, httpClient, playlistURL)
		if err != nil {
			return 0, err
		}
	}

	manifest, err := parseHLSManifest(body)
	if err != nil {
		return 0, err
	}

	buf := mgr.EnsureStreamBuffer(channelID, streamPath, 0)
	if manifest.TargetDuration > 0 {
		buf.SetTargetDuration(manifest.TargetDuration)
	}
	buf.mu.Lock()
	buf.upstreamSeq = manifest.MediaSequence
	buf.mu.Unlock()

	last := lastSeq[streamPath]
	for _, seg := range manifest.Segments {
		if last > 0 && seg.Sequence <= last {
			continue // already fetched
		}

		segURL, err := resolveSegmentURL(playlistURL, seg.URI)
		if err != nil || segURL == "" {
			continue
		}
		segPath := bufferServedSubPath(playlistURL, seg.URI)
		if segPath == "" {
			continue
		}

		data, err := bufferFetchURL(ctx, httpClient, segURL)
		if err != nil {
			logDebugf("buffer[%s]: segment %s fetch: %v", channelID[:12], segPath, err)
			continue
		}

		buf.PushNamedSegment(seg.Sequence, segPath, data, seg.Duration)
		if seg.Sequence > last {
			last = seg.Sequence
		}
	}
	lastSeq[streamPath] = last
	return manifest.TargetDuration, nil
}

// buildBufferManifestURL constructs the upstream manifest URL for a buffered channel.
func buildBufferManifestURL(ch *relayRegisteredChannel) string {
	if ch.StreamHint == "" {
		return ""
	}
	// StreamHint is the best currently-working upstream host:port.
	hint := ch.StreamHint
	scheme := "https"
	if isLocalAddress(hint) {
		scheme = "http"
	}
	return fmt.Sprintf("%s://%s/tltv/v1/channels/%s/stream.m3u8", scheme, hint, ch.ChannelID)
}

// bufferFetchURL performs an HTTP GET and returns the body.
func bufferFetchURL(ctx context.Context, client *http.Client, fetchURL string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", fetchURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	// Limit reads to 50 MB (same as cache).
	body, err := io.ReadAll(io.LimitReader(resp.Body, 50<<20))
	if err != nil {
		return nil, err
	}
	return body, nil
}

func parseMasterMediaURIs(body []byte) []string {
	seen := make(map[string]bool)
	var refs []string
	add := func(uri string) {
		uri = strings.TrimSpace(uri)
		if uri == "" || seen[uri] {
			return
		}
		seen[uri] = true
		refs = append(refs, uri)
	}

	for _, v := range parseMasterPlaylist(body) {
		add(v.URI)
	}
	for _, line := range strings.Split(string(body), "\n") {
		line = strings.TrimSpace(strings.TrimRight(line, "\r"))
		if !strings.HasPrefix(line, "#EXT-X-MEDIA:") {
			continue
		}
		attrs := strings.TrimPrefix(line, "#EXT-X-MEDIA:")
		typ := strings.ToUpper(hlsAttr(attrs, "TYPE"))
		if typ != "AUDIO" && typ != "SUBTITLES" {
			continue
		}
		add(hlsAttr(attrs, "URI"))
	}
	return refs
}

func bufferServedSubPath(baseURL, ref string) string {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return ""
	}
	baseDir := bufferBaseDir(baseURL)
	if strings.HasPrefix(ref, "http://") || strings.HasPrefix(ref, "https://") {
		ref = bridgeMakeRelative(ref, baseDir)
		if strings.HasPrefix(ref, "http://") || strings.HasPrefix(ref, "https://") {
			return "" // relay only buffers paths it can serve itself
		}
	}
	u, err := url.Parse(ref)
	if err != nil || u.Path == "" {
		return ""
	}
	p := path.Clean(strings.TrimPrefix(u.Path, "/"))
	if p == "." || strings.HasPrefix(p, "../") {
		return ""
	}
	return p
}

func bufferBaseDir(manifestURL string) string {
	base, err := url.Parse(manifestURL)
	if err != nil {
		return ""
	}
	base.Path = path.Dir(base.Path) + "/"
	base.RawQuery = ""
	base.Fragment = ""
	return base.String()
}

// ---------- Memory Limit Parsing ----------

// parseMemorySize parses a human-readable memory size (e.g. "1g", "512m", "1073741824").
func parseMemorySize(s string) (int64, error) {
	s = strings.TrimSpace(strings.ToLower(s))
	if s == "" {
		return 0, nil
	}

	multiplier := int64(1)
	if strings.HasSuffix(s, "g") || strings.HasSuffix(s, "gb") {
		multiplier = 1 << 30
		s = strings.TrimRight(s, "gb")
	} else if strings.HasSuffix(s, "m") || strings.HasSuffix(s, "mb") {
		multiplier = 1 << 20
		s = strings.TrimRight(s, "mb")
	} else if strings.HasSuffix(s, "k") || strings.HasSuffix(s, "kb") {
		multiplier = 1 << 10
		s = strings.TrimRight(s, "kb")
	}

	s = strings.TrimSpace(s)
	var n int64
	_, err := fmt.Sscanf(s, "%d", &n)
	if err != nil {
		return 0, fmt.Errorf("invalid memory size: %q", s)
	}
	if n <= 0 {
		return 0, fmt.Errorf("memory size must be positive")
	}
	return n * multiplier, nil
}
