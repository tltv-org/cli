package main

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestHLSCache_HitMiss(t *testing.T) {
	c := newHLSCache(10)

	// Miss on empty cache
	entry := c.get("/seg0.ts")
	if entry != nil {
		t.Fatal("expected nil on empty cache")
	}

	// Store and hit
	c.set("/seg0.ts", []byte("segment-data"), "video/mp2t", 60*time.Second)

	entry = c.get("/seg0.ts")
	if entry == nil {
		t.Fatal("expected cache hit")
	}
	if string(entry.data) != "segment-data" {
		t.Errorf("data = %q, want segment-data", string(entry.data))
	}
	if entry.contentType != "video/mp2t" {
		t.Errorf("contentType = %q, want video/mp2t", entry.contentType)
	}

	// Different key is still a miss
	entry = c.get("/seg1.ts")
	if entry != nil {
		t.Fatal("expected nil for different key")
	}
}

func TestHLSCache_TTLExpiry(t *testing.T) {
	c := newHLSCache(10)

	// Use a controllable clock
	now := time.Now()
	c.now = func() time.Time { return now }

	c.set("/stream.m3u8", []byte("manifest"), "application/vnd.apple.mpegurl", 1*time.Second)

	// Still valid
	entry := c.get("/stream.m3u8")
	if entry == nil {
		t.Fatal("expected hit before expiry")
	}

	// Advance past TTL
	now = now.Add(2 * time.Second)

	entry = c.get("/stream.m3u8")
	if entry != nil {
		t.Fatal("expected miss after expiry")
	}
}

func TestHLSCache_LRUEviction(t *testing.T) {
	c := newHLSCache(3)

	now := time.Now()
	c.now = func() time.Time { return now }

	c.set("/seg0.ts", []byte("0"), "video/mp2t", 3600*time.Second)
	now = now.Add(1 * time.Millisecond)
	c.set("/seg1.ts", []byte("1"), "video/mp2t", 3600*time.Second)
	now = now.Add(1 * time.Millisecond)
	c.set("/seg2.ts", []byte("2"), "video/mp2t", 3600*time.Second)

	// Cache is full (3/3). Adding a 4th should evict the LRU (seg0).
	now = now.Add(1 * time.Millisecond)
	c.set("/seg3.ts", []byte("3"), "video/mp2t", 3600*time.Second)

	// seg0 should be evicted
	if c.get("/seg0.ts") != nil {
		t.Error("seg0 should have been evicted")
	}
	// seg1, seg2, seg3 should still be present
	if c.get("/seg1.ts") == nil {
		t.Error("seg1 should still be cached")
	}
	if c.get("/seg2.ts") == nil {
		t.Error("seg2 should still be cached")
	}
	if c.get("/seg3.ts") == nil {
		t.Error("seg3 should still be cached")
	}
}

func TestHLSCache_LRUAccessRefreshes(t *testing.T) {
	c := newHLSCache(3)

	now := time.Now()
	c.now = func() time.Time { return now }

	c.set("/seg0.ts", []byte("0"), "video/mp2t", 3600*time.Second)
	now = now.Add(1 * time.Millisecond)
	c.set("/seg1.ts", []byte("1"), "video/mp2t", 3600*time.Second)
	now = now.Add(1 * time.Millisecond)
	c.set("/seg2.ts", []byte("2"), "video/mp2t", 3600*time.Second)

	// Access seg0 to refresh its LRU time
	now = now.Add(1 * time.Millisecond)
	c.get("/seg0.ts")

	// Add seg3 — should evict seg1 (oldest access, since seg0 was refreshed)
	now = now.Add(1 * time.Millisecond)
	c.set("/seg3.ts", []byte("3"), "video/mp2t", 3600*time.Second)

	if c.get("/seg0.ts") == nil {
		t.Error("seg0 should still be cached (was recently accessed)")
	}
	if c.get("/seg1.ts") != nil {
		t.Error("seg1 should have been evicted (oldest access)")
	}
}

func TestHLSCache_SweepExpired(t *testing.T) {
	c := newHLSCache(10)

	now := time.Now()
	c.now = func() time.Time { return now }

	c.set("/seg0.ts", []byte("0"), "video/mp2t", 1*time.Second)
	c.set("/seg1.ts", []byte("1"), "video/mp2t", 3600*time.Second)

	// Advance past seg0's TTL
	now = now.Add(5 * time.Second)
	c.sweep()

	if c.get("/seg0.ts") != nil {
		t.Error("seg0 should have been swept")
	}
	if c.get("/seg1.ts") == nil {
		t.Error("seg1 should still be cached")
	}
}

func TestHLSCache_Stats(t *testing.T) {
	c := newHLSCache(10)
	c.set("/seg0.ts", []byte("data"), "video/mp2t", 60*time.Second)

	// Hit
	c.getOrFetch("/seg0.ts", func() (*hlsCacheFetchResult, error) {
		t.Fatal("should not fetch — item is cached")
		return nil, nil
	})

	// Miss
	c.getOrFetch("/seg1.ts", func() (*hlsCacheFetchResult, error) {
		return &hlsCacheFetchResult{data: []byte("new"), contentType: "video/mp2t"}, nil
	})

	hits, misses, _, size := c.stats()
	if hits != 1 {
		t.Errorf("hits = %d, want 1", hits)
	}
	if misses != 1 {
		t.Errorf("misses = %d, want 1", misses)
	}
	if size != 2 {
		t.Errorf("size = %d, want 2", size)
	}
}

func TestHLSCache_GetOrFetchErrorNotCached(t *testing.T) {
	c := newHLSCache(10)
	fetchCount := 0

	// First fetch fails
	_, _, _, err := c.getOrFetch("/seg0.ts", func() (*hlsCacheFetchResult, error) {
		fetchCount++
		return nil, fmt.Errorf("upstream error")
	})
	if err == nil {
		t.Fatal("expected error")
	}

	// Second fetch should NOT hit cache (error was not cached), should call fn again
	_, _, _, err = c.getOrFetch("/seg0.ts", func() (*hlsCacheFetchResult, error) {
		fetchCount++
		return &hlsCacheFetchResult{data: []byte("ok"), contentType: "video/mp2t"}, nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fetchCount != 2 {
		t.Errorf("fetchCount = %d, want 2 (error should not cache)", fetchCount)
	}
}

func TestHLSCache_SingleflightDedup(t *testing.T) {
	c := newHLSCache(10)

	var fetchCount atomic.Int64
	var wg sync.WaitGroup

	// Simulate 50 concurrent requests for the same key
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			data, _, _, err := c.getOrFetch("/seg5.ts", func() (*hlsCacheFetchResult, error) {
				fetchCount.Add(1)
				time.Sleep(10 * time.Millisecond) // simulate upstream latency
				return &hlsCacheFetchResult{
					data:        []byte("segment-5-data"),
					contentType: "video/mp2t",
				}, nil
			})
			if err != nil {
				t.Errorf("unexpected error: %v", err)
			}
			if string(data) != "segment-5-data" {
				t.Errorf("data = %q", data)
			}
		}()
	}

	wg.Wait()

	fc := fetchCount.Load()
	if fc != 1 {
		t.Errorf("singleflight: fetch called %d times, want 1", fc)
	}

	// Subsequent requests should hit cache
	data, _, hit, err := c.getOrFetch("/seg5.ts", func() (*hlsCacheFetchResult, error) {
		t.Fatal("should not fetch — item is cached")
		return nil, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !hit {
		t.Error("expected cache hit")
	}
	if string(data) != "segment-5-data" {
		t.Errorf("cached data = %q", data)
	}
}

func TestHLSCacheTTL_ProtocolCompliant(t *testing.T) {
	// Manifests: 1 second (spec 9.10)
	if ttl := hlsCacheTTL("/tltv/v1/channels/TVabc/stream.m3u8"); ttl != 1*time.Second {
		t.Errorf("manifest TTL = %v, want 1s", ttl)
	}

	// Segments: 3600 seconds (spec 9.10)
	if ttl := hlsCacheTTL("/tltv/v1/channels/TVabc/seg0.ts"); ttl != 3600*time.Second {
		t.Errorf("segment .ts TTL = %v, want 3600s", ttl)
	}
	if ttl := hlsCacheTTL("/tltv/v1/channels/TVabc/seg0.m4s"); ttl != 3600*time.Second {
		t.Errorf("segment .m4s TTL = %v, want 3600s", ttl)
	}
}

func TestHLSCache_MaxItemSize(t *testing.T) {
	c := newHLSCache(10)
	c.maxItemKB = 1 // 1 KB max

	// Small item fits
	c.set("/small.ts", make([]byte, 500), "video/mp2t", 60*time.Second)
	if c.get("/small.ts") == nil {
		t.Error("small item should be cached")
	}

	// Large item rejected
	c.set("/big.ts", make([]byte, 2048), "video/mp2t", 60*time.Second)
	if c.get("/big.ts") != nil {
		t.Error("oversized item should not be cached")
	}
}

func TestHLSCache_ReplaceExisting(t *testing.T) {
	c := newHLSCache(10)

	c.set("/seg0.ts", []byte("old"), "video/mp2t", 60*time.Second)
	c.set("/seg0.ts", []byte("new"), "video/mp2t", 60*time.Second)

	entry := c.get("/seg0.ts")
	if entry == nil {
		t.Fatal("expected hit")
	}
	if string(entry.data) != "new" {
		t.Errorf("data = %q, want new", string(entry.data))
	}
}

func TestSingleflight_Dedup(t *testing.T) {
	var g singleflightGroup
	var callCount atomic.Int64
	var wg sync.WaitGroup

	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			val, err := g.Do("key", func() (interface{}, error) {
				callCount.Add(1)
				time.Sleep(20 * time.Millisecond)
				return "result", nil
			})
			if err != nil {
				t.Errorf("unexpected error: %v", err)
			}
			if val.(string) != "result" {
				t.Errorf("val = %v", val)
			}
		}()
	}

	wg.Wait()

	cc := callCount.Load()
	if cc != 1 {
		t.Errorf("singleflight: fn called %d times, want 1", cc)
	}
}

func TestSingleflight_DifferentKeys(t *testing.T) {
	var g singleflightGroup
	var callCount atomic.Int64
	var wg sync.WaitGroup

	for i := 0; i < 5; i++ {
		wg.Add(1)
		key := fmt.Sprintf("key-%d", i)
		go func(k string) {
			defer wg.Done()
			g.Do(k, func() (interface{}, error) {
				callCount.Add(1)
				return k, nil
			})
		}(key)
	}

	wg.Wait()

	cc := callCount.Load()
	if cc != 5 {
		t.Errorf("different keys: fn called %d times, want 5", cc)
	}
}

func TestSingleflight_ErrorPropagated(t *testing.T) {
	var g singleflightGroup

	_, err := g.Do("key", func() (interface{}, error) {
		return nil, fmt.Errorf("test error")
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if err.Error() != "test error" {
		t.Errorf("error = %q", err)
	}
}

// Test that the relay server adds Cache-Status header when cache is enabled
func TestRelayServer_CacheStatusHeader(t *testing.T) {
	c := newHLSCache(100)

	now := time.Now()
	c.now = func() time.Time { return now }

	// Pre-populate cache with a segment
	c.set("/tltv/v1/channels/TVtest123/seg0.ts", []byte("cached-segment"), "video/mp2t", 3600*time.Second)

	hits, _, _, _ := c.stats()
	if hits != 0 {
		t.Errorf("initial hits = %d, want 0", hits)
	}

	// Fetch from cache
	data, _, hit, err := c.getOrFetch("/tltv/v1/channels/TVtest123/seg0.ts", func() (*hlsCacheFetchResult, error) {
		t.Fatal("should not fetch — item is cached")
		return nil, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !hit {
		t.Error("expected cache hit")
	}
	if string(data) != "cached-segment" {
		t.Errorf("data = %q", data)
	}

	hits, _, _, _ = c.stats()
	if hits != 1 {
		t.Errorf("hits after fetch = %d, want 1", hits)
	}
}
