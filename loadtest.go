package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// cmdLoadtest implements the "tltv loadtest" subcommand -- a multi-receiver
// load simulator that spawns N receivers against a target.
func cmdLoadtest(args []string) {
	fs := flag.NewFlagSet("loadtest", flag.ExitOnError)

	defaultReceivers := 10
	if v := os.Getenv("RECEIVERS"); v != "" {
		fmt.Sscanf(v, "%d", &defaultReceivers)
	}
	numReceivers := fs.Int("receivers", defaultReceivers, "number of concurrent simulated receivers")
	fs.IntVar(numReceivers, "n", defaultReceivers, "alias for --receivers")

	defaultDuration := "1m"
	if v := os.Getenv("DURATION"); v != "" {
		defaultDuration = v
	}
	durationStr := fs.String("duration", defaultDuration, "how long to run the test")
	fs.StringVar(durationStr, "d", defaultDuration, "alias for --duration")

	defaultRamp := "0s"
	if v := os.Getenv("RAMP"); v != "" {
		defaultRamp = v
	}
	rampStr := fs.String("ramp", defaultRamp, "time to ramp from 0 to N receivers (0 = all at once)")
	fs.StringVar(rampStr, "r", defaultRamp, "alias for --ramp")

	directURL := fs.String("url", os.Getenv("URL"), "direct HLS manifest URL (skip tltv:// resolution)")
	fs.StringVar(directURL, "u", os.Getenv("URL"), "alias for --url")
	quality := fs.String("quality", os.Getenv("QUALITY"), "variant quality: best (default), worst, or resolution (e.g. 720p)")
	fs.StringVar(quality, "q", os.Getenv("QUALITY"), "alias for --quality")
	proxyStr := addProxyFlag(fs)

	defaultConnTimeout := "10s"
	if v := os.Getenv("CONNECT_TIMEOUT"); v != "" {
		defaultConnTimeout = v
	}
	connectTimeout := fs.String("connect-timeout", defaultConnTimeout, "initial connection validation timeout")
	fs.StringVar(connectTimeout, "T", defaultConnTimeout, "alias for --connect-timeout")

	// --- Logging ---
	logLvl, logFmt, logPath := addLogFlags(fs)

	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Load test a TLTV channel with multiple concurrent receivers\n\n")
		fmt.Fprintf(os.Stderr, "Usage: tltv loadtest [flags] <target>\n\n")
		fmt.Fprintf(os.Stderr, "Spawns N headless HLS receivers against a target to simulate concurrent\n")
		fmt.Fprintf(os.Stderr, "viewers. Reports aggregate statistics including latency percentiles,\n")
		fmt.Fprintf(os.Stderr, "cache hit rates, and bandwidth utilization.\n\n")
		fmt.Fprintf(os.Stderr, "Flags:\n")
		fmt.Fprintf(os.Stderr, "  -n, --receivers N        concurrent receivers (default: 10)\n")
		fmt.Fprintf(os.Stderr, "  -d, --duration DURATION  test duration (default: 1m)\n")
		fmt.Fprintf(os.Stderr, "  -r, --ramp DURATION      ramp-up time, 0 = all at once (default: 0s)\n")
		fmt.Fprintf(os.Stderr, "  -u, --url URL            direct HLS manifest URL (skip resolution)\n")
		fmt.Fprintf(os.Stderr, "  -q, --quality QUALITY    variant quality: best, worst, or 720p\n")
		fmt.Fprintf(os.Stderr, "  -x, --proxy URL          proxy URL (socks5://, http://, https://)\n")
		fmt.Fprintf(os.Stderr, "  -T, --connect-timeout D  initial validation timeout (default: 10s)\n\n")
		fmt.Fprintf(os.Stderr, "Logging:\n")
		fmt.Fprintf(os.Stderr, "      --log-level LEVEL    log level: debug, info, error (default: info)\n")
		fmt.Fprintf(os.Stderr, "      --log-format FORMAT  log format: human, json (default: human)\n")
		fmt.Fprintf(os.Stderr, "      --log-file PATH      log to file instead of stderr\n\n")
		fmt.Fprintf(os.Stderr, "Examples:\n")
		fmt.Fprintf(os.Stderr, "  tltv loadtest --receivers 200 --duration 5m tltv://TVabc...@demo.timelooptv.org:443\n")
		fmt.Fprintf(os.Stderr, "  tltv loadtest -n 500 --ramp 60s -d 10m TVabc...@localhost:8000\n")
		fmt.Fprintf(os.Stderr, "  tltv loadtest -n 100 -d 5m --url https://demo.timelooptv.org/stream.m3u8\n")
		fmt.Fprintf(os.Stderr, "  tltv loadtest -n 50 -d 2m --json TVabc...@localhost:8000\n\n")
		fmt.Fprintf(os.Stderr, "Environment variables: RECEIVERS, DURATION, RAMP, URL, QUALITY,\n")
		fmt.Fprintf(os.Stderr, "CONNECT_TIMEOUT, LOG_LEVEL, LOG_FORMAT, LOG_FILE. Flags override env vars.\n")
	}
	fs.Parse(args)

	target := fs.Arg(0)
	if target == "" && *directURL == "" {
		fmt.Fprintf(os.Stderr, "error: specify a target (tltv:// URI or id@host:port) or --url\n\n")
		fs.Usage()
		os.Exit(1)
	}

	// Set up logging
	if err := setupLogging(*logLvl, *logFmt, *logPath, "loadtest"); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	stopLogReopen := startLogReopenWatcher()
	defer stopLogReopen()

	// Parse durations
	testDuration, err := time.ParseDuration(*durationStr)
	if err != nil {
		fatal("invalid --duration: %v", err)
	}
	rampDuration, err := time.ParseDuration(*rampStr)
	if err != nil {
		fatal("invalid --ramp: %v", err)
	}
	connTimeout, err := time.ParseDuration(*connectTimeout)
	if err != nil {
		fatal("invalid --connect-timeout: %v", err)
	}

	if *numReceivers <= 0 {
		fatal("--receivers must be > 0")
	}

	// Parse proxy URL
	proxyURL, err := parseProxyURL(*proxyStr)
	if err != nil {
		fatal("%v", err)
	}

	// Initial validation: verify we can connect before spawning N receivers
	logInfof("validating target...")
	client := newClientWithProxy(flagInsecure, proxyURL)

	valRecv := newLoadtestValidationReceiver(target, *directURL, client, *quality)
	valCtx, valCancel := context.WithTimeout(context.Background(), connTimeout)
	valRecv.OnSegment = func(sr ReceiverSegmentResult) {
		if sr.Err == nil {
			valCancel()
		}
	}
	valRecv.Run(valCtx)
	valCancel()

	snap := valRecv.Stats.snapshot()
	if snap.SegmentsFetched == 0 {
		fatal("cannot connect to target — got 0 segments during validation")
	}
	logInfof("target validated: stream is live")

	// Set up aggregate stats
	agg := &loadtestAggregator{
		startTime:    time.Now(),
		numReceivers: *numReceivers,
	}

	// Create test context
	ctx, cancel := context.WithTimeout(context.Background(), testDuration)
	defer cancel()

	// Signal handler
	sigCh := make(chan os.Signal, 1)
	signalNotify(sigCh)
	go func() {
		<-sigCh
		cancel()
	}()

	// Live status ticker
	if !flagJSON {
		go loadtestStatusLoop(ctx, agg)
	}

	// Spawn receivers
	var wg sync.WaitGroup

	logInfof("starting %d receivers (ramp: %s, duration: %s)", *numReceivers, rampDuration, testDuration)

	// Uniform inter-arrival delay: spread N receivers evenly across rampDuration
	rampDelay := time.Duration(0)
	if rampDuration > 0 && *numReceivers > 1 {
		rampDelay = rampDuration / time.Duration(*numReceivers)
	}

spawnLoop:
	for i := 0; i < *numReceivers; i++ {
		// Ramp-up delay
		if rampDelay > 0 && i > 0 {
			select {
			case <-ctx.Done():
				break spawnLoop
			case <-time.After(rampDelay):
			}
		}

		select {
		case <-ctx.Done():
			break spawnLoop
		default:
		}

		wg.Add(1)
		go func(id int) {
			defer wg.Done()

			stats := &ReceiverStats{StartTime: time.Now()}
			recv := &Receiver{
				Target:         target,
				DirectURL:      *directURL,
				Client:         newClientWithProxy(flagInsecure, proxyURL),
				Stats:          stats,
				Quality:        *quality,
				VerifyMetadata: false, // skip per-receiver verification for load testing
			}

			recv.OnSegment = func(sr ReceiverSegmentResult) {
				agg.addSegment(sr)
			}
			recv.OnManifest = func(mr ReceiverManifestResult) {
				agg.addManifest(mr)
			}

			recv.Run(ctx)

			agg.addReceiverStats(stats.snapshot())
		}(i)

		agg.activeReceivers.Add(1)
	}

	wg.Wait()

	// Print results
	if flagJSON {
		loadtestPrintJSON(agg, target, *directURL, testDuration, *numReceivers, rampDuration)
	} else {
		loadtestPrintSummary(agg, target, *directURL, testDuration, *numReceivers, rampDuration)
	}
}

func newLoadtestValidationReceiver(target, directURL string, client *Client, quality string) *Receiver {
	return &Receiver{
		Target:         target,
		DirectURL:      directURL,
		Client:         client,
		Stats:          &ReceiverStats{StartTime: time.Now()},
		Quality:        quality,
		VerifyMetadata: directURL == "",
	}
}

// ---------- Aggregator ----------

// loadtestAggregator collects statistics across all receivers.
type loadtestAggregator struct {
	mu sync.Mutex

	startTime       time.Time
	numReceivers    int
	activeReceivers atomic.Int64

	totalSegments  atomic.Int64
	segmentErrors  atomic.Int64
	totalManifests atomic.Int64
	manifestErrors atomic.Int64
	bytesReceived  atomic.Int64
	cacheHits      atomic.Int64
	cacheMisses    atomic.Int64

	// Latency samples (collected from individual receivers at completion)
	segmentLatencies  []int64
	manifestLatencies []int64
}

func (a *loadtestAggregator) addSegment(sr ReceiverSegmentResult) {
	if sr.Err != nil {
		a.segmentErrors.Add(1)
		return
	}
	a.totalSegments.Add(1)
	a.bytesReceived.Add(int64(sr.Size))
	switch sr.CacheStatus {
	case "HIT":
		a.cacheHits.Add(1)
	case "MISS":
		a.cacheMisses.Add(1)
	}
}

func (a *loadtestAggregator) addManifest(mr ReceiverManifestResult) {
	if mr.Err != nil {
		a.manifestErrors.Add(1)
		return
	}
	a.totalManifests.Add(1)
}

func (a *loadtestAggregator) addReceiverStats(stats *ReceiverStats) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.segmentLatencies = append(a.segmentLatencies, stats.SegmentLatencies...)
	a.manifestLatencies = append(a.manifestLatencies, stats.ManifestLatencies...)
	a.activeReceivers.Add(-1)
}

// ---------- Output ----------

func loadtestStatusLoop(ctx context.Context, agg *loadtestAggregator) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			elapsed := time.Since(agg.startTime).Seconds()
			segs := agg.totalSegments.Load()
			manifests := agg.totalManifests.Load()
			errs := agg.segmentErrors.Load() + agg.manifestErrors.Load()
			bytes := agg.bytesReceived.Load()
			hits := agg.cacheHits.Load()
			misses := agg.cacheMisses.Load()
			active := agg.activeReceivers.Load()

			var bwMbps float64
			if elapsed > 0 {
				bwMbps = float64(bytes) * 8 / elapsed / 1_000_000
			}

			cacheStr := ""
			total := hits + misses
			if total > 0 {
				cacheStr = fmt.Sprintf("  cache: %.1f%%", float64(hits)/float64(total)*100)
			}

			fmt.Fprintf(os.Stderr, "[%4.0fs] receivers: %d  seg: %d  manifest: %d  err: %d  bw: %.0f Mbps%s\n",
				elapsed, active, segs, manifests, errs, bwMbps, cacheStr)
		}
	}
}

func loadtestPrintSummary(agg *loadtestAggregator, target, directURL string, duration time.Duration, receivers int, ramp time.Duration) {
	elapsed := time.Since(agg.startTime)

	displayTarget := target
	if directURL != "" {
		displayTarget = directURL
	}

	fmt.Fprintf(os.Stderr, "\nLoad Test Results\n")
	fmt.Fprintf(os.Stderr, "%s\n", strings.Repeat("\u2500", 50))
	fmt.Fprintf(os.Stderr, "Target:     %s\n", displayTarget)
	fmt.Fprintf(os.Stderr, "Duration:   %s\n", elapsed.Round(time.Second))
	rampStr := "instant"
	if ramp > 0 {
		rampStr = fmt.Sprintf("ramped over %s", ramp)
	}
	fmt.Fprintf(os.Stderr, "Receivers:  %d (%s)\n\n", receivers, rampStr)

	// Manifests
	manifests := agg.totalManifests.Load()
	mErr := agg.manifestErrors.Load()
	fmt.Fprintf(os.Stderr, "Manifests\n")
	fmt.Fprintf(os.Stderr, "  Total:    %d\n", manifests+mErr)
	fmt.Fprintf(os.Stderr, "  OK:       %d", manifests)
	if manifests+mErr > 0 {
		fmt.Fprintf(os.Stderr, " (%.1f%%)", float64(manifests)/float64(manifests+mErr)*100)
	}
	fmt.Fprintf(os.Stderr, "\n")
	fmt.Fprintf(os.Stderr, "  Errors:   %d\n", mErr)

	agg.mu.Lock()
	if len(agg.manifestLatencies) > 0 {
		sorted := make([]int64, len(agg.manifestLatencies))
		copy(sorted, agg.manifestLatencies)
		sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
		fmt.Fprintf(os.Stderr, "  p50: %dms    p95: %dms    p99: %dms\n",
			percentile(sorted, 50), percentile(sorted, 95), percentile(sorted, 99))
	}
	agg.mu.Unlock()

	// Segments
	segs := agg.totalSegments.Load()
	sErr := agg.segmentErrors.Load()
	fmt.Fprintf(os.Stderr, "\nSegments\n")
	fmt.Fprintf(os.Stderr, "  Total:    %d\n", segs+sErr)
	fmt.Fprintf(os.Stderr, "  OK:       %d", segs)
	if segs+sErr > 0 {
		fmt.Fprintf(os.Stderr, " (%.1f%%)", float64(segs)/float64(segs+sErr)*100)
	}
	fmt.Fprintf(os.Stderr, "\n")
	fmt.Fprintf(os.Stderr, "  Errors:   %d\n", sErr)

	agg.mu.Lock()
	if len(agg.segmentLatencies) > 0 {
		sorted := make([]int64, len(agg.segmentLatencies))
		copy(sorted, agg.segmentLatencies)
		sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
		fmt.Fprintf(os.Stderr, "  p50: %dms    p95: %dms    p99: %dms\n",
			percentile(sorted, 50), percentile(sorted, 95), percentile(sorted, 99))
	}
	agg.mu.Unlock()

	bytes := agg.bytesReceived.Load()
	if elapsed.Seconds() > 0 {
		bwMbps := float64(bytes) * 8 / elapsed.Seconds() / 1_000_000
		fmt.Fprintf(os.Stderr, "  Bandwidth: %.0f Mbps avg (%s)\n", bwMbps, formatBytes(bytes))
	}

	// Cache
	hits := agg.cacheHits.Load()
	misses := agg.cacheMisses.Load()
	total := hits + misses
	if total > 0 {
		fmt.Fprintf(os.Stderr, "\nCache (Cache-Status)\n")
		fmt.Fprintf(os.Stderr, "  HIT:      %d (%.1f%%)\n", hits, float64(hits)/float64(total)*100)
		fmt.Fprintf(os.Stderr, "  MISS:     %d (%.1f%%)\n", misses, float64(misses)/float64(total)*100)
	}
}

func loadtestPrintJSON(agg *loadtestAggregator, target, directURL string, duration time.Duration, receivers int, ramp time.Duration) {
	elapsed := time.Since(agg.startTime)

	displayTarget := target
	if directURL != "" {
		displayTarget = directURL
	}

	result := map[string]interface{}{
		"target":          displayTarget,
		"receivers":       receivers,
		"duration_ms":     elapsed.Milliseconds(),
		"ramp_ms":         ramp.Milliseconds(),
		"segments_total":  agg.totalSegments.Load() + agg.segmentErrors.Load(),
		"segments_ok":     agg.totalSegments.Load(),
		"segment_errors":  agg.segmentErrors.Load(),
		"manifests_total": agg.totalManifests.Load() + agg.manifestErrors.Load(),
		"manifests_ok":    agg.totalManifests.Load(),
		"manifest_errors": agg.manifestErrors.Load(),
		"bytes_received":  agg.bytesReceived.Load(),
	}

	if elapsed.Seconds() > 0 {
		result["bandwidth_mbps"] = float64(agg.bytesReceived.Load()) * 8 / elapsed.Seconds() / 1_000_000
	}

	agg.mu.Lock()
	if len(agg.segmentLatencies) > 0 {
		sorted := make([]int64, len(agg.segmentLatencies))
		copy(sorted, agg.segmentLatencies)
		sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
		result["segment_latency_p50_ms"] = percentile(sorted, 50)
		result["segment_latency_p95_ms"] = percentile(sorted, 95)
		result["segment_latency_p99_ms"] = percentile(sorted, 99)
	}
	if len(agg.manifestLatencies) > 0 {
		sorted := make([]int64, len(agg.manifestLatencies))
		copy(sorted, agg.manifestLatencies)
		sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
		result["manifest_latency_p50_ms"] = percentile(sorted, 50)
		result["manifest_latency_p95_ms"] = percentile(sorted, 95)
		result["manifest_latency_p99_ms"] = percentile(sorted, 99)
	}
	agg.mu.Unlock()

	hits := agg.cacheHits.Load()
	misses := agg.cacheMisses.Load()
	total := hits + misses
	if total > 0 {
		result["cache_hits"] = hits
		result["cache_misses"] = misses
		result["cache_hit_rate"] = float64(hits) / float64(total)
	}

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	enc.Encode(result)
}
