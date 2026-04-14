package main

import (
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// ---------- Shared Cache Flag Helpers ----------

// addCacheFlags registers --cache, --cache-max-entries, --cache-stats on a FlagSet.
// Returns pointers to the parsed values. Env vars: CACHE=1, CACHE_MAX_ENTRIES, CACHE_STATS.
func addCacheFlags(fs *flag.FlagSet) (enabled *bool, maxEntries *int, statsInterval *int) {
	defaultCache := os.Getenv("CACHE") == "1"
	enabled = fs.Bool("cache", defaultCache, "enable in-memory response cache")

	defaultMaxEntries := 100
	if v := os.Getenv("CACHE_MAX_ENTRIES"); v != "" {
		if n, err := fmt.Sscanf(v, "%d", &defaultMaxEntries); n != 1 || err != nil {
			defaultMaxEntries = 100
		}
	}
	maxEntries = fs.Int("cache-max-entries", defaultMaxEntries, "max cached items")

	defaultCacheStats := 0
	if v := os.Getenv("CACHE_STATS"); v != "" {
		fmt.Sscanf(v, "%d", &defaultCacheStats)
	}
	statsInterval = fs.Int("cache-stats", defaultCacheStats, "log cache stats every N seconds (0 = off)")

	return
}

// startCacheGoroutines starts the sweep (30s) and optional stats logging goroutines.
// Call after creating the cache, before the main event loop.
func startCacheGoroutines(cache *hlsCache, statsInterval int, done <-chan struct{}) {
	go hlsCacheStatsLoop(cache, time.Duration(statsInterval)*time.Second, done)
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				cache.sweep()
			}
		}
	}()
}

// ---------- Singleflight (inline, zero deps) ----------

// singleflightCall represents an in-progress or completed call.
type singleflightCall struct {
	wg  sync.WaitGroup
	val interface{}
	err error
}

// singleflightGroup provides duplicate function call suppression.
// Multiple concurrent callers with the same key get the same result.
type singleflightGroup struct {
	mu sync.Mutex
	m  map[string]*singleflightCall
}

// Do executes fn once for a given key, deduplicating concurrent calls.
// All callers with the same key block until the first completes.
func (g *singleflightGroup) Do(key string, fn func() (interface{}, error)) (interface{}, error) {
	g.mu.Lock()
	if g.m == nil {
		g.m = make(map[string]*singleflightCall)
	}
	if c, ok := g.m[key]; ok {
		g.mu.Unlock()
		c.wg.Wait()
		return c.val, c.err
	}
	c := &singleflightCall{}
	c.wg.Add(1)
	g.m[key] = c
	g.mu.Unlock()

	c.val, c.err = fn()
	c.wg.Done()

	g.mu.Lock()
	delete(g.m, key)
	g.mu.Unlock()

	return c.val, c.err
}

// ---------- HLS Cache ----------

// hlsCacheEntry stores a cached response with expiration.
type hlsCacheEntry struct {
	data        []byte
	contentType string
	expires     time.Time
	accessTime  atomic.Int64 // UnixNano for LRU eviction
}

// hlsCache is an in-memory cache for HLS stream content with
// singleflight deduplication and LRU eviction. Keyed by request path.
//
// TTLs follow the TLTV protocol spec (section 9.10):
//   - Manifests (.m3u8): 1 second
//   - Segments (.ts, .m4s, .mp4): 3600 seconds
type hlsCache struct {
	mu         sync.RWMutex
	items      map[string]*hlsCacheEntry
	sf         singleflightGroup
	maxEntries int
	maxItemKB  int // max single item size in KB (0 = no limit)
	now        func() time.Time

	// Stats (atomics — no lock needed on the hot path)
	hits   atomic.Int64
	misses atomic.Int64
	evicts atomic.Int64
}

const (
	hlsCacheDefaultMax     = 100
	hlsCacheDefaultItemMax = 50 * 1024 // 50 MB in KB
	hlsCacheManifestTTL    = 1 * time.Second
	hlsCacheSegmentTTL     = 3600 * time.Second
)

// newHLSCache creates a cache bounded by maxEntries.
func newHLSCache(maxEntries int) *hlsCache {
	if maxEntries <= 0 {
		maxEntries = hlsCacheDefaultMax
	}
	return &hlsCache{
		items:      make(map[string]*hlsCacheEntry),
		maxEntries: maxEntries,
		maxItemKB:  hlsCacheDefaultItemMax,
		now:        time.Now,
	}
}

// hlsCacheTTL returns the protocol-recommended TTL for a given path.
// Manifests (.m3u8) and protocol documents (.json, .xml, no extension)
// use a short TTL (singleflight dedup without staleness).
// Segments (.ts, .m4s, etc.) use a long TTL (immutable by sequence number).
func hlsCacheTTL(path string) time.Duration {
	if strings.HasSuffix(path, ".m3u8") {
		return hlsCacheManifestTTL
	}
	if strings.HasSuffix(path, ".json") || strings.HasSuffix(path, ".xml") {
		return hlsCacheManifestTTL
	}
	// Paths with no file extension are protocol documents (e.g. /tltv/v1/channels/{id}).
	lastSlash := strings.LastIndex(path, "/")
	lastDot := strings.LastIndex(path, ".")
	if lastDot <= lastSlash {
		return hlsCacheManifestTTL
	}
	return hlsCacheSegmentTTL
}

// get returns a cached entry if it exists and hasn't expired.
// Returns nil on miss or expiration.
func (c *hlsCache) get(key string) *hlsCacheEntry {
	c.mu.RLock()

	entry, ok := c.items[key]
	if !ok {
		c.mu.RUnlock()
		return nil
	}

	now := c.now()
	if now.After(entry.expires) {
		c.mu.RUnlock()
		c.mu.Lock()
		if current, ok := c.items[key]; ok && current == entry && now.After(current.expires) {
			delete(c.items, key)
		}
		c.mu.Unlock()
		return nil
	}

	entry.accessTime.Store(now.UnixNano())
	c.mu.RUnlock()
	return entry
}

// set stores a cache entry, evicting LRU entries if at capacity.
func (c *hlsCache) set(key string, data []byte, contentType string, ttl time.Duration) {
	// Don't cache items larger than the limit
	if c.maxItemKB > 0 && len(data)/1024 > c.maxItemKB {
		return
	}

	now := c.now()
	entry := &hlsCacheEntry{
		data:        data,
		contentType: contentType,
		expires:     now.Add(ttl),
	}
	entry.accessTime.Store(now.UnixNano())

	c.mu.Lock()
	defer c.mu.Unlock()

	// If key already exists, replace
	if _, ok := c.items[key]; ok {
		c.items[key] = entry
		return
	}

	// Evict LRU entries if at capacity
	for len(c.items) >= c.maxEntries {
		c.evictLRU()
	}

	c.items[key] = entry
}

// evictLRU removes the least-recently-accessed entry.
// Must be called with c.mu held.
func (c *hlsCache) evictLRU() {
	var oldestKey string
	var oldestTime time.Time
	first := true

	for k, v := range c.items {
		at := time.Unix(0, v.accessTime.Load())
		if first || at.Before(oldestTime) {
			oldestKey = k
			oldestTime = at
			first = false
		}
	}

	if !first {
		delete(c.items, oldestKey)
		c.evicts.Add(1)
	}
}

// getOrFetch returns cached data or fetches via fn, deduplicating
// concurrent requests for the same key via singleflight.
// Only caches successful (non-error) results.
func (c *hlsCache) getOrFetch(key string, fn func() (*hlsCacheFetchResult, error)) ([]byte, string, bool, error) {
	// Check cache first
	if entry := c.get(key); entry != nil {
		c.hits.Add(1)
		return entry.data, entry.contentType, true, nil
	}

	c.misses.Add(1)

	// Singleflight: N concurrent requests = 1 upstream fetch
	result, err := c.sf.Do(key, func() (interface{}, error) {
		return fn()
	})
	if err != nil {
		return nil, "", false, err
	}

	fr := result.(*hlsCacheFetchResult)

	// Cache the result with protocol-recommended TTL
	ttl := hlsCacheTTL(key)
	c.set(key, fr.data, fr.contentType, ttl)

	return fr.data, fr.contentType, false, nil
}

// stats returns current cache statistics.
func (c *hlsCache) stats() (hits, misses, evicts int64, size int) {
	c.mu.RLock()
	size = len(c.items)
	c.mu.RUnlock()
	return c.hits.Load(), c.misses.Load(), c.evicts.Load(), size
}

// sweep removes expired entries. Called periodically by background goroutine.
func (c *hlsCache) sweep() {
	now := c.now()
	c.mu.Lock()
	defer c.mu.Unlock()

	for k, v := range c.items {
		if now.After(v.expires) {
			delete(c.items, k)
		}
	}
}

// hlsCacheFetchResult is the result of an upstream fetch for caching.
type hlsCacheFetchResult struct {
	data        []byte
	contentType string
}

// hlsCacheMaxBody is the maximum upstream response body size for cached items (50 MB).
const hlsCacheMaxBody = 50 * 1024 * 1024

// hlsCacheFetchUpstream performs an HTTP GET and returns the response body + content-type.
// Only returns results for 200 OK responses. Limits body to hlsCacheMaxBody.
func hlsCacheFetchUpstream(client *http.Client, fetchURL string, r *http.Request) (*hlsCacheFetchResult, error) {
	req, err := http.NewRequestWithContext(r.Context(), "GET", fetchURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "tltv-cli/"+version)

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, &hlsCacheUpstreamError{status: resp.StatusCode}
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, hlsCacheMaxBody))
	if err != nil {
		return nil, err
	}

	ct := resp.Header.Get("Content-Type")
	if ct == "" {
		ct = "application/octet-stream"
	}

	return &hlsCacheFetchResult{data: data, contentType: ct}, nil
}

// hlsCacheUpstreamError represents a non-200 upstream response.
type hlsCacheUpstreamError struct {
	status int
}

func (e *hlsCacheUpstreamError) Error() string {
	return "upstream returned " + http.StatusText(e.status)
}

// hlsCacheStatsLoop logs cache statistics periodically.
func hlsCacheStatsLoop(cache *hlsCache, interval time.Duration, done <-chan struct{}) {
	if interval <= 0 {
		return
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-done:
			return
		case <-ticker.C:
			hits, misses, evicts, size := cache.stats()
			total := hits + misses
			var hitRate float64
			if total > 0 {
				hitRate = float64(hits) / float64(total) * 100
			}
			logInfof("cache: %d items, %d hits, %d misses (%.1f%% hit rate), %d evictions",
				size, hits, misses, hitRate, evicts)
		}
	}
}
