package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ---------- HLS Manifest Parser Tests ----------

func TestParseHLSManifest_Basic(t *testing.T) {
	body := []byte("#EXTM3U\n" +
		"#EXT-X-VERSION:3\n" +
		"#EXT-X-TARGETDURATION:2\n" +
		"#EXT-X-MEDIA-SEQUENCE:42\n" +
		"#EXTINF:2.000,\n" +
		"seg42.ts\n" +
		"#EXTINF:2.000,\n" +
		"seg43.ts\n" +
		"#EXTINF:2.000,\n" +
		"seg44.ts\n")

	m, err := parseHLSManifest(body)
	if err != nil {
		t.Fatal(err)
	}

	if m.TargetDuration != 2.0 {
		t.Errorf("TargetDuration = %v, want 2.0", m.TargetDuration)
	}
	if m.MediaSequence != 42 {
		t.Errorf("MediaSequence = %d, want 42", m.MediaSequence)
	}
	if len(m.Segments) != 3 {
		t.Fatalf("len(Segments) = %d, want 3", len(m.Segments))
	}
	if m.EndList {
		t.Error("EndList should be false for live manifest")
	}

	for i, seg := range m.Segments {
		expectedSeq := uint64(42 + i)
		expectedURI := fmt.Sprintf("seg%d.ts", 42+i)
		if seg.Sequence != expectedSeq {
			t.Errorf("seg[%d].Sequence = %d, want %d", i, seg.Sequence, expectedSeq)
		}
		if seg.URI != expectedURI {
			t.Errorf("seg[%d].URI = %q, want %q", i, seg.URI, expectedURI)
		}
		if seg.Duration != 2.0 {
			t.Errorf("seg[%d].Duration = %v, want 2.0", i, seg.Duration)
		}
	}
}

func TestParseHLSManifest_WindowsCRLF(t *testing.T) {
	body := []byte("#EXTM3U\r\n#EXT-X-TARGETDURATION:2\r\n#EXT-X-MEDIA-SEQUENCE:0\r\n#EXTINF:2.000,\r\nseg0.ts\r\n")

	m, err := parseHLSManifest(body)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Segments) != 1 {
		t.Fatalf("len(Segments) = %d, want 1", len(m.Segments))
	}
	if m.Segments[0].URI != "seg0.ts" {
		t.Errorf("URI = %q, want seg0.ts", m.Segments[0].URI)
	}
}

func TestParseHLSManifest_Endlist(t *testing.T) {
	body := []byte("#EXTM3U\n#EXT-X-TARGETDURATION:10\n#EXTINF:10.0,\nseg0.ts\n#EXT-X-ENDLIST\n")

	m, err := parseHLSManifest(body)
	if err != nil {
		t.Fatal(err)
	}
	if !m.EndList {
		t.Error("EndList should be true")
	}
}

func TestParseHLSManifest_NoSequence(t *testing.T) {
	// If EXT-X-MEDIA-SEQUENCE is absent, it defaults to 0
	body := []byte("#EXTM3U\n#EXT-X-TARGETDURATION:2\n#EXTINF:2.000,\nseg0.ts\n#EXTINF:2.000,\nseg1.ts\n")

	m, err := parseHLSManifest(body)
	if err != nil {
		t.Fatal(err)
	}
	if m.MediaSequence != 0 {
		t.Errorf("MediaSequence = %d, want 0", m.MediaSequence)
	}
	if m.Segments[0].Sequence != 0 {
		t.Errorf("seg[0].Sequence = %d, want 0", m.Segments[0].Sequence)
	}
	if m.Segments[1].Sequence != 1 {
		t.Errorf("seg[1].Sequence = %d, want 1", m.Segments[1].Sequence)
	}
}

func TestParseHLSManifest_InvalidMissing(t *testing.T) {
	// Missing #EXTM3U header
	_, err := parseHLSManifest([]byte("not a manifest"))
	if err == nil {
		t.Fatal("expected error for missing #EXTM3U")
	}
	if !strings.Contains(err.Error(), "missing #EXTM3U") {
		t.Errorf("error = %v", err)
	}
}

func TestParseHLSManifest_EmptyPlaylist(t *testing.T) {
	body := []byte("#EXTM3U\n#EXT-X-TARGETDURATION:2\n#EXT-X-MEDIA-SEQUENCE:100\n")

	m, err := parseHLSManifest(body)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Segments) != 0 {
		t.Errorf("len(Segments) = %d, want 0", len(m.Segments))
	}
}

func TestParseHLSManifest_FractionalDuration(t *testing.T) {
	body := []byte("#EXTM3U\n#EXT-X-TARGETDURATION:3\n#EXTINF:2.500,\nseg0.ts\n")

	m, err := parseHLSManifest(body)
	if err != nil {
		t.Fatal(err)
	}
	if m.Segments[0].Duration != 2.5 {
		t.Errorf("Duration = %v, want 2.5", m.Segments[0].Duration)
	}
}

func TestParseHLSManifest_AbsoluteURLSegments(t *testing.T) {
	body := []byte("#EXTM3U\n#EXT-X-TARGETDURATION:2\n#EXTINF:2.0,\nhttps://cdn.example.com/seg0.ts\n")

	m, err := parseHLSManifest(body)
	if err != nil {
		t.Fatal(err)
	}
	if m.Segments[0].URI != "https://cdn.example.com/seg0.ts" {
		t.Errorf("URI = %q", m.Segments[0].URI)
	}
}

// ---------- Segment URL Resolution Tests ----------

func TestResolveSegmentURL_Relative(t *testing.T) {
	u, err := resolveSegmentURL("https://demo.example.com/tltv/v1/channels/TVabc/stream.m3u8", "seg42.ts")
	if err != nil {
		t.Fatal(err)
	}
	if u != "https://demo.example.com/tltv/v1/channels/TVabc/seg42.ts" {
		t.Errorf("resolved = %q", u)
	}
}

func TestResolveSegmentURL_Absolute(t *testing.T) {
	u, err := resolveSegmentURL("https://demo.example.com/stream.m3u8", "https://cdn.example.com/seg0.ts")
	if err != nil {
		t.Fatal(err)
	}
	if u != "https://cdn.example.com/seg0.ts" {
		t.Errorf("resolved = %q", u)
	}
}

func TestResolveSegmentURL_Subdirectory(t *testing.T) {
	u, err := resolveSegmentURL("https://demo.example.com/hls/live/stream.m3u8", "segments/seg0.ts")
	if err != nil {
		t.Fatal(err)
	}
	if u != "https://demo.example.com/hls/live/segments/seg0.ts" {
		t.Errorf("resolved = %q", u)
	}
}

// ---------- Receiver Stats Tests ----------

func TestReceiverStats_AddSegment(t *testing.T) {
	s := &ReceiverStats{StartTime: time.Now()}

	s.addSegment(ReceiverSegmentResult{Sequence: 1, Size: 400000, FetchTimeMs: 25, CacheStatus: "HIT"})
	s.addSegment(ReceiverSegmentResult{Sequence: 2, Size: 350000, FetchTimeMs: 30, CacheStatus: "MISS"})
	s.addSegment(ReceiverSegmentResult{Sequence: 3, Err: fmt.Errorf("timeout")})

	snap := s.snapshot()
	if snap.SegmentsFetched != 2 {
		t.Errorf("SegmentsFetched = %d, want 2", snap.SegmentsFetched)
	}
	if snap.SegmentErrors != 1 {
		t.Errorf("SegmentErrors = %d, want 1", snap.SegmentErrors)
	}
	if snap.BytesReceived != 750000 {
		t.Errorf("BytesReceived = %d, want 750000", snap.BytesReceived)
	}
	if snap.CacheHits != 1 {
		t.Errorf("CacheHits = %d, want 1", snap.CacheHits)
	}
	if snap.CacheMisses != 1 {
		t.Errorf("CacheMisses = %d, want 1", snap.CacheMisses)
	}
	if len(snap.SegmentLatencies) != 2 {
		t.Errorf("len(SegmentLatencies) = %d, want 2", len(snap.SegmentLatencies))
	}
}

func TestReceiverStats_AddManifest(t *testing.T) {
	s := &ReceiverStats{StartTime: time.Now()}

	s.addManifest(ReceiverManifestResult{Segments: 5, NewSegments: 2, FetchTimeMs: 15})
	s.addManifest(ReceiverManifestResult{Err: fmt.Errorf("timeout")})

	snap := s.snapshot()
	if snap.ManifestPolls != 1 {
		t.Errorf("ManifestPolls = %d, want 1", snap.ManifestPolls)
	}
	if snap.ManifestErrors != 1 {
		t.Errorf("ManifestErrors = %d, want 1", snap.ManifestErrors)
	}
}

func TestReceiverStats_SnapshotIsolation(t *testing.T) {
	s := &ReceiverStats{StartTime: time.Now()}
	s.addSegment(ReceiverSegmentResult{Sequence: 1, Size: 100, FetchTimeMs: 10})

	snap := s.snapshot()
	// Modify original — snapshot should be unaffected
	s.addSegment(ReceiverSegmentResult{Sequence: 2, Size: 200, FetchTimeMs: 20})

	if len(snap.SegmentLatencies) != 1 {
		t.Errorf("snapshot isolation broken: len = %d, want 1", len(snap.SegmentLatencies))
	}
}

// ---------- Percentile Tests ----------

func TestPercentile(t *testing.T) {
	sorted := []int64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}

	if p := percentile(sorted, 50); p != 5 {
		t.Errorf("p50 = %d, want 5", p)
	}
	if p := percentile(sorted, 95); p != 10 {
		t.Errorf("p95 = %d, want 10", p)
	}
	if p := percentile(sorted, 99); p != 10 {
		t.Errorf("p99 = %d, want 10", p)
	}
}

func TestPercentile_Empty(t *testing.T) {
	if p := percentile(nil, 50); p != 0 {
		t.Errorf("empty p50 = %d, want 0", p)
	}
}

func TestPercentile_Single(t *testing.T) {
	sorted := []int64{42}
	if p := percentile(sorted, 50); p != 42 {
		t.Errorf("single p50 = %d, want 42", p)
	}
}

// ---------- FormatBytes Tests ----------

func TestFormatBytes(t *testing.T) {
	tests := []struct {
		in   int64
		want string
	}{
		{0, "0 B"},
		{512, "512 B"},
		{1024, "1.0 KB"},
		{1024 * 1024, "1.0 MB"},
		{1024 * 1024 * 1024, "1.0 GB"},
		{int64(1.5 * 1024 * 1024), "1.5 MB"},
	}
	for _, tt := range tests {
		got := formatBytes(tt.in)
		if got != tt.want {
			t.Errorf("formatBytes(%d) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// ---------- Integration: Receiver against test server ----------

func TestReceiver_LiveStream(t *testing.T) {
	// Create a minimal HLS server
	var segSeq atomic.Int64
	mux := http.NewServeMux()
	mux.HandleFunc("/stream.m3u8", func(w http.ResponseWriter, r *http.Request) {
		seq := segSeq.Load()
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		w.Header().Set("Cache-Control", "max-age=1, no-cache")
		fmt.Fprintf(w, "#EXTM3U\n#EXT-X-VERSION:3\n#EXT-X-TARGETDURATION:2\n#EXT-X-MEDIA-SEQUENCE:%d\n", seq)
		for i := int64(0); i < 3; i++ {
			fmt.Fprintf(w, "#EXTINF:2.000,\nseg%d.ts\n", seq+i)
		}
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, ".ts") {
			w.Header().Set("Content-Type", "video/mp2t")
			w.Header().Set("Cache-Control", "max-age=3600")
			w.Write([]byte("fake-ts-data-" + r.URL.Path))
		} else {
			http.NotFound(w, r)
		}
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	// Advance sequence every 100ms for test speed
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	go func() {
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				segSeq.Add(1)
			}
		}
	}()

	stats := &ReceiverStats{StartTime: time.Now()}
	recv := &Receiver{
		DirectURL:      srv.URL + "/stream.m3u8",
		Client:         newClient(false),
		Stats:          stats,
		VerifyMetadata: false,
	}

	recv.Run(ctx)

	snap := stats.snapshot()
	if snap.SegmentsFetched == 0 {
		t.Fatal("expected at least 1 segment fetched")
	}
	if snap.ManifestPolls == 0 {
		t.Fatal("expected at least 1 manifest poll")
	}
	if snap.BytesReceived == 0 {
		t.Fatal("expected bytes received > 0")
	}
	if snap.SegmentErrors != 0 {
		t.Errorf("SegmentErrors = %d, want 0", snap.SegmentErrors)
	}
}

func TestReceiver_LiveEdge(t *testing.T) {
	// A manifest with 7 segments. With LiveEdge=3, the receiver should only
	// fetch the last 3 on the first poll (skipping the first 4). Without
	// LiveEdge, it fetches all 7.
	var fetched []string
	var mu sync.Mutex

	mux := http.NewServeMux()
	mux.HandleFunc("/stream.m3u8", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		fmt.Fprint(w, "#EXTM3U\n#EXT-X-TARGETDURATION:2\n#EXT-X-MEDIA-SEQUENCE:100\n")
		for i := 0; i < 7; i++ {
			fmt.Fprintf(w, "#EXTINF:2.000,\nseg%d.ts\n", 100+i)
		}
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, ".ts") {
			mu.Lock()
			fetched = append(fetched, r.URL.Path)
			mu.Unlock()
			w.Header().Set("Content-Type", "video/mp2t")
			w.Write([]byte("ts-data"))
		}
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	// Test WITH LiveEdge=3: should fetch only last 3 segments (104, 105, 106)
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	stats := &ReceiverStats{StartTime: time.Now()}
	recv := &Receiver{
		DirectURL:      srv.URL + "/stream.m3u8",
		Client:         newClient(false),
		Stats:          stats,
		VerifyMetadata: false,
		LiveEdge:       3,
	}
	recv.OnSegment = func(sr ReceiverSegmentResult) {
		// Stop after first poll's segments are fetched
		if sr.Sequence == 106 {
			cancel()
		}
	}
	recv.Run(ctx)

	mu.Lock()
	edgeFetched := make([]string, len(fetched))
	copy(edgeFetched, fetched)
	mu.Unlock()

	// Should have fetched exactly 3 segments: 104, 105, 106
	if len(edgeFetched) < 3 {
		t.Fatalf("LiveEdge=3: fetched %d segments, want >= 3", len(edgeFetched))
	}
	// First segment fetched should be seg104, not seg100
	if edgeFetched[0] != "/seg104.ts" {
		t.Errorf("LiveEdge=3: first segment = %q, want /seg104.ts", edgeFetched[0])
	}
	for _, path := range edgeFetched[:3] {
		if path == "/seg100.ts" || path == "/seg101.ts" || path == "/seg102.ts" || path == "/seg103.ts" {
			t.Errorf("LiveEdge=3: should not fetch old segment %s", path)
		}
	}

	// Test WITHOUT LiveEdge: should fetch all 7 segments starting from 100
	fetched = nil
	ctx2, cancel2 := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel2()

	stats2 := &ReceiverStats{StartTime: time.Now()}
	recv2 := &Receiver{
		DirectURL:      srv.URL + "/stream.m3u8",
		Client:         newClient(false),
		Stats:          stats2,
		VerifyMetadata: false,
		// LiveEdge: 0 (default — fetch all)
	}
	recv2.OnSegment = func(sr ReceiverSegmentResult) {
		if sr.Sequence == 106 {
			cancel2()
		}
	}
	recv2.Run(ctx2)

	mu.Lock()
	allFetched := make([]string, len(fetched))
	copy(allFetched, fetched)
	mu.Unlock()

	if len(allFetched) < 7 {
		t.Fatalf("LiveEdge=0: fetched %d segments, want >= 7", len(allFetched))
	}
	if allFetched[0] != "/seg100.ts" {
		t.Errorf("LiveEdge=0: first segment = %q, want /seg100.ts", allFetched[0])
	}
}

func TestReceiver_LiveEdge_SmallManifest(t *testing.T) {
	// When the manifest has fewer segments than LiveEdge, all segments are fetched.
	var fetched []string
	var mu sync.Mutex

	mux := http.NewServeMux()
	mux.HandleFunc("/stream.m3u8", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		fmt.Fprint(w, "#EXTM3U\n#EXT-X-TARGETDURATION:2\n#EXT-X-MEDIA-SEQUENCE:50\n")
		fmt.Fprint(w, "#EXTINF:2.000,\nseg50.ts\n#EXTINF:2.000,\nseg51.ts\n")
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, ".ts") {
			mu.Lock()
			fetched = append(fetched, r.URL.Path)
			mu.Unlock()
			w.Header().Set("Content-Type", "video/mp2t")
			w.Write([]byte("ts-data"))
		}
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	stats := &ReceiverStats{StartTime: time.Now()}
	recv := &Receiver{
		DirectURL:      srv.URL + "/stream.m3u8",
		Client:         newClient(false),
		Stats:          stats,
		VerifyMetadata: false,
		LiveEdge:       3, // larger than manifest
	}
	recv.OnSegment = func(sr ReceiverSegmentResult) {
		if sr.Sequence == 51 {
			cancel()
		}
	}
	recv.Run(ctx)

	mu.Lock()
	defer mu.Unlock()

	// Both segments should be fetched (manifest has only 2, LiveEdge=3 doesn't skip any)
	if len(fetched) < 2 {
		t.Fatalf("small manifest: fetched %d segments, want >= 2", len(fetched))
	}
	if fetched[0] != "/seg50.ts" {
		t.Errorf("small manifest: first segment = %q, want /seg50.ts", fetched[0])
	}
}

func TestReceiver_CacheStatusTracking(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/stream.m3u8", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		fmt.Fprint(w, "#EXTM3U\n#EXT-X-TARGETDURATION:2\n#EXT-X-MEDIA-SEQUENCE:0\n#EXTINF:2.0,\nseg0.ts\n")
	})
	mux.HandleFunc("/seg0.ts", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "video/mp2t")
		w.Header().Set("Cache-Status", "HIT")
		w.Write([]byte("cached-data"))
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	stats := &ReceiverStats{StartTime: time.Now()}
	recv := &Receiver{
		DirectURL:      srv.URL + "/stream.m3u8",
		Client:         newClient(false),
		Stats:          stats,
		VerifyMetadata: false,
	}
	recv.OnSegment = func(sr ReceiverSegmentResult) {
		if sr.Err == nil {
			cancel()
		}
	}

	recv.Run(ctx)

	snap := stats.snapshot()
	if snap.CacheHits != 1 {
		t.Errorf("CacheHits = %d, want 1", snap.CacheHits)
	}
}

// ---------- Master Playlist ----------

func TestParseMasterPlaylist_Basic(t *testing.T) {
	body := []byte(`#EXTM3U
#EXT-X-STREAM-INF:BANDWIDTH=5000000,RESOLUTION=1920x1080,CODECS="avc1.42c028,mp4a.40.2"
stream_1080p.m3u8
#EXT-X-STREAM-INF:BANDWIDTH=2000000,RESOLUTION=1280x720,CODECS="avc1.42c01f,mp4a.40.2"
stream_720p.m3u8
#EXT-X-STREAM-INF:BANDWIDTH=800000,RESOLUTION=640x360,CODECS="avc1.42c01e,mp4a.40.2"
stream_360p.m3u8
`)

	variants := parseMasterPlaylist(body)
	if variants == nil {
		t.Fatal("parseMasterPlaylist returned nil")
	}
	if len(variants) != 3 {
		t.Fatalf("expected 3 variants, got %d", len(variants))
	}

	if variants[0].Bandwidth != 5000000 {
		t.Errorf("variant 0 bandwidth = %d, want 5000000", variants[0].Bandwidth)
	}
	if variants[0].Resolution != "1920x1080" {
		t.Errorf("variant 0 resolution = %q, want 1920x1080", variants[0].Resolution)
	}
	if variants[0].Width != 1920 || variants[0].Height != 1080 {
		t.Errorf("variant 0 dims = %dx%d, want 1920x1080", variants[0].Width, variants[0].Height)
	}
	if variants[0].URI != "stream_1080p.m3u8" {
		t.Errorf("variant 0 URI = %q, want stream_1080p.m3u8", variants[0].URI)
	}
	if variants[0].Codecs != "avc1.42c028,mp4a.40.2" {
		t.Errorf("variant 0 codecs = %q", variants[0].Codecs)
	}

	if variants[2].Bandwidth != 800000 {
		t.Errorf("variant 2 bandwidth = %d, want 800000", variants[2].Bandwidth)
	}
	if variants[2].Height != 360 {
		t.Errorf("variant 2 height = %d, want 360", variants[2].Height)
	}
}

func TestParseMasterPlaylist_MediaPlaylist(t *testing.T) {
	// A media playlist (no STREAM-INF) should return nil
	body := []byte(`#EXTM3U
#EXT-X-TARGETDURATION:6
#EXT-X-MEDIA-SEQUENCE:42
#EXTINF:6.0,
seg42.ts
#EXTINF:6.0,
seg43.ts
`)
	variants := parseMasterPlaylist(body)
	if variants != nil {
		t.Errorf("media playlist should return nil, got %d variants", len(variants))
	}
}

func TestParseMasterPlaylist_WithMedia(t *testing.T) {
	// Master playlist with #EXT-X-MEDIA (audio) — should still parse variants
	body := []byte(`#EXTM3U
#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID="audio",NAME="Main",DEFAULT=YES,URI="audio_main.m3u8"
#EXT-X-STREAM-INF:BANDWIDTH=2000000,RESOLUTION=1280x720,AUDIO="audio"
stream_720p.m3u8
#EXT-X-STREAM-INF:BANDWIDTH=800000,RESOLUTION=640x360,AUDIO="audio"
stream_360p.m3u8
`)
	variants := parseMasterPlaylist(body)
	if len(variants) != 2 {
		t.Fatalf("expected 2 variants, got %d", len(variants))
	}
}

func TestSelectVariant_Best(t *testing.T) {
	variants := []hlsVariant{
		{URI: "360p.m3u8", Bandwidth: 800000, Height: 360},
		{URI: "1080p.m3u8", Bandwidth: 5000000, Height: 1080},
		{URI: "720p.m3u8", Bandwidth: 2000000, Height: 720},
	}
	v := selectVariant(variants, "best")
	if v.URI != "1080p.m3u8" {
		t.Errorf("best = %q, want 1080p.m3u8", v.URI)
	}
}

func TestSelectVariant_Worst(t *testing.T) {
	variants := []hlsVariant{
		{URI: "360p.m3u8", Bandwidth: 800000, Height: 360},
		{URI: "1080p.m3u8", Bandwidth: 5000000, Height: 1080},
		{URI: "720p.m3u8", Bandwidth: 2000000, Height: 720},
	}
	v := selectVariant(variants, "worst")
	if v.URI != "360p.m3u8" {
		t.Errorf("worst = %q, want 360p.m3u8", v.URI)
	}
}

func TestSelectVariant_Resolution(t *testing.T) {
	variants := []hlsVariant{
		{URI: "360p.m3u8", Bandwidth: 800000, Height: 360},
		{URI: "1080p.m3u8", Bandwidth: 5000000, Height: 1080},
		{URI: "720p.m3u8", Bandwidth: 2000000, Height: 720},
	}

	v := selectVariant(variants, "720p")
	if v.URI != "720p.m3u8" {
		t.Errorf("720p = %q, want 720p.m3u8", v.URI)
	}

	v = selectVariant(variants, "480p")
	if v.URI != "360p.m3u8" {
		t.Errorf("480p closest = %q, want 360p.m3u8", v.URI)
	}
}

func TestSelectVariant_DefaultBest(t *testing.T) {
	variants := []hlsVariant{
		{URI: "a.m3u8", Bandwidth: 100},
		{URI: "b.m3u8", Bandwidth: 200},
	}
	v := selectVariant(variants, "")
	if v.URI != "b.m3u8" {
		t.Errorf("default = %q, want b.m3u8 (highest bandwidth)", v.URI)
	}
}

func TestHlsAttr(t *testing.T) {
	cases := []struct {
		attrs, name, want string
	}{
		{"BANDWIDTH=5000000,RESOLUTION=1920x1080", "BANDWIDTH", "5000000"},
		{"BANDWIDTH=5000000,RESOLUTION=1920x1080", "RESOLUTION", "1920x1080"},
		{`BANDWIDTH=5000000,CODECS="avc1.42c028,mp4a.40.2"`, "CODECS", "avc1.42c028,mp4a.40.2"},
		{"BANDWIDTH=500", "RESOLUTION", ""},
	}
	for _, tc := range cases {
		got := hlsAttr(tc.attrs, tc.name)
		if got != tc.want {
			t.Errorf("hlsAttr(%q, %q) = %q, want %q", tc.attrs, tc.name, got, tc.want)
		}
	}
}

func TestHlsAttr_NameCollision(t *testing.T) {
	// BANDWIDTH must not match inside BANDWIDTHRATE
	attrs := "BANDWIDTHRATE=999,BANDWIDTH=5000000"
	got := hlsAttr(attrs, "BANDWIDTH")
	if got != "5000000" {
		t.Errorf("hlsAttr name collision: got %q, want %q", got, "5000000")
	}
}

func TestHlsAttr_MissingClosingQuote(t *testing.T) {
	attrs := `CODECS="avc1.42c028`
	got := hlsAttr(attrs, "CODECS")
	if got != "" {
		t.Errorf("hlsAttr missing closing quote: got %q, want empty", got)
	}
}

func TestHlsAttr_AtStart(t *testing.T) {
	// Attribute at the start of the string (idx=0)
	attrs := "RESOLUTION=1920x1080,BANDWIDTH=5000000"
	got := hlsAttr(attrs, "RESOLUTION")
	if got != "1920x1080" {
		t.Errorf("hlsAttr at start: got %q, want %q", got, "1920x1080")
	}
}
