package main

import (
	"context"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"time"
)

// cmdBridge implements the "tltv bridge" subcommand -- a TLTV origin server
// that bridges external streaming sources as first-class TLTV channels.
func cmdBridge(args []string) {
	fs := flag.NewFlagSet("bridge", flag.ExitOnError)

	// Source flags
	streamArg := fs.String("stream", os.Getenv("STREAM"), "channel source: HLS URL, M3U playlist, JSON file, or directory")
	guideArg := fs.String("guide", os.Getenv("GUIDE"), "guide source: XMLTV or JSON (optional)")

	// Channel defaults
	nameArg := fs.String("name", os.Getenv("NAME"), "channel name (single-stream mode only)")
	fs.StringVar(nameArg, "n", os.Getenv("NAME"), "alias for --name")
	onDemand := fs.Bool("on-demand", os.Getenv("ON_DEMAND") == "1", "mark all channels as on-demand")

	defaultPoll := "60s"
	if v := os.Getenv("POLL"); v != "" {
		defaultPoll = v
	}
	pollStr := fs.String("poll", defaultPoll, "re-poll interval")

	// Server flags
	defaultListen := ":8000"
	if v := os.Getenv("LISTEN"); v != "" {
		defaultListen = v
	}
	listenAddr := fs.String("listen", defaultListen, "listen address")
	fs.StringVar(listenAddr, "l", defaultListen, "alias for --listen")

	defaultKeysDir := "/data/keys"
	if v := os.Getenv("KEYS_DIR"); v != "" {
		defaultKeysDir = v
	}
	keysDir := fs.String("keys-dir", defaultKeysDir, "key storage directory")
	fs.StringVar(keysDir, "k", defaultKeysDir, "alias for --keys-dir")

	hostnameArg := fs.String("hostname", os.Getenv("HOSTNAME"), "public host:port for origins field")
	fs.StringVar(hostnameArg, "H", os.Getenv("HOSTNAME"), "alias for --hostname")

	peersStr := fs.String("peers", os.Getenv("PEERS"), "comma-separated peer host:port hints")
	fs.StringVar(peersStr, "P", os.Getenv("PEERS"), "alias for --peers")

	// --- Cache ---
	cacheEnabled, cacheMaxEntries, cacheStatsInterval := addCacheFlags(fs)

	// --- Logging ---
	logLvl, logFmt, logPath := addLogFlags(fs)

	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Start a TLTV bridge origin server\n\n")
		fmt.Fprintf(os.Stderr, "Usage: tltv bridge [flags]\n\n")
		fmt.Fprintf(os.Stderr, "Bridges external streaming sources (HLS, M3U, directories) as\n")
		fmt.Fprintf(os.Stderr, "first-class TLTV channels with Ed25519 identities and signed metadata.\n\n")
		fmt.Fprintf(os.Stderr, "Source:\n")
		fmt.Fprintf(os.Stderr, "      --stream URL/PATH    channel source: HLS URL, M3U playlist, JSON file, or directory\n")
		fmt.Fprintf(os.Stderr, "      --guide URL/PATH     guide source: XMLTV or JSON (optional)\n\n")
		fmt.Fprintf(os.Stderr, "Channel defaults:\n")
		fmt.Fprintf(os.Stderr, "      --name STRING        channel name (single-stream mode only)\n")
		fmt.Fprintf(os.Stderr, "      --on-demand          mark all channels as on-demand\n")
		fmt.Fprintf(os.Stderr, "      --poll DURATION      re-poll interval (default: 60s)\n\n")
		fmt.Fprintf(os.Stderr, "Server:\n")
		fmt.Fprintf(os.Stderr, "  -l, --listen ADDR        listen address (default: :8000)\n")
		fmt.Fprintf(os.Stderr, "  -k, --keys-dir PATH      key storage directory (default: /data/keys)\n")
		fmt.Fprintf(os.Stderr, "  -H, --hostname HOST      public host:port for origins field\n")
		fmt.Fprintf(os.Stderr, "  -P, --peers LIST         comma-separated peer host:port hints\n\n")
		fmt.Fprintf(os.Stderr, "Cache:\n")
		fmt.Fprintf(os.Stderr, "      --cache              enable in-memory response cache\n")
		fmt.Fprintf(os.Stderr, "      --cache-max-entries  max cached items (default: 100)\n")
		fmt.Fprintf(os.Stderr, "      --cache-stats N      log cache stats every N seconds (0 = off)\n\n")
		fmt.Fprintf(os.Stderr, "Logging:\n")
		fmt.Fprintf(os.Stderr, "      --log-level LEVEL    log level: debug, info, error (default: info)\n")
		fmt.Fprintf(os.Stderr, "      --log-format FORMAT  log format: human, json (default: human)\n")
		fmt.Fprintf(os.Stderr, "      --log-file PATH      log to file instead of stderr\n\n")
		fmt.Fprintf(os.Stderr, "Environment variables: STREAM, GUIDE, NAME, ON_DEMAND=1, POLL,\n")
		fmt.Fprintf(os.Stderr, "LISTEN, KEYS_DIR, HOSTNAME, PEERS, CACHE=1, CACHE_MAX_ENTRIES,\n")
		fmt.Fprintf(os.Stderr, "CACHE_STATS, LOG_LEVEL, LOG_FORMAT, LOG_FILE.\n")
		fmt.Fprintf(os.Stderr, "Flags override env vars.\n\n")
		fmt.Fprintf(os.Stderr, "Examples:\n")
		fmt.Fprintf(os.Stderr, "  tltv bridge --stream http://example.com/live.m3u8 --name \"My Channel\"\n")
		fmt.Fprintf(os.Stderr, "  tltv bridge --stream http://provider.com/channels.m3u --guide http://provider.com/guide.xml\n")
		fmt.Fprintf(os.Stderr, "  tltv bridge --stream /media/hls\n")
		fmt.Fprintf(os.Stderr, "  tltv bridge --stream http://tunarr:8000/api/channels.m3u --guide http://tunarr:8000/api/xmltv.xml --on-demand\n")
	}
	fs.Parse(args)

	// Set up logging
	if err := setupLogging(*logLvl, *logFmt, *logPath, "bridge"); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	if *streamArg == "" {
		fmt.Fprintf(os.Stderr, "error: --stream is required (or set STREAM env var)\n\n")
		fmt.Fprintf(os.Stderr, "Examples:\n")
		fmt.Fprintf(os.Stderr, "  tltv bridge --stream http://example.com/live.m3u8 --name \"My Channel\"\n")
		fmt.Fprintf(os.Stderr, "  tltv bridge --stream http://provider.com/channels.m3u\n")
		fmt.Fprintf(os.Stderr, "  tltv bridge --stream /media/hls\n")
		os.Exit(1)
	}

	// Parse poll duration
	pollDur, err := time.ParseDuration(*pollStr)
	if err != nil {
		fatal("invalid --poll value %q: %v", *pollStr, err)
	}

	// Parse peers
	var peers []string
	if *peersStr != "" {
		for _, p := range strings.Split(*peersStr, ",") {
			p = strings.TrimSpace(p)
			if p != "" {
				peers = append(peers, p)
			}
		}
	}

	// Ensure keys directory exists
	if err := os.MkdirAll(*keysDir, 0700); err != nil {
		fatal("could not create keys directory %s: %v", *keysDir, err)
	}

	// Create registry
	registry := newBridgeRegistry(*keysDir, *hostnameArg, peers)

	// Initial source poll
	logInfof("discovering channels from %s", *streamArg)
	channels, sidecarGuide, err := bridgePollSource(*streamArg, *nameArg, *onDemand)
	if err != nil {
		fatal("source discovery failed: %v", err)
	}
	if len(channels) == 0 {
		fatal("no channels discovered from %s", *streamArg)
	}

	if err := registry.UpdateChannels(channels); err != nil {
		fatal("channel registration failed: %v", err)
	}

	// Initial guide poll
	guide := sidecarGuide
	if guide == nil {
		guide = make(map[string][]bridgeGuideEntry)
	}
	if *guideArg != "" {
		externalGuide, err := bridgePollGuide(*guideArg)
		if err != nil {
			fatal("guide fetch failed: %v", err)
		}
		for id, entries := range externalGuide {
			if _, ok := guide[id]; !ok {
				guide[id] = entries
			}
		}
	}
	if len(guide) > 0 {
		registry.UpdateGuide(guide)
	}

	// Log registered channels
	for _, ch := range registry.ListChannels() {
		vis := "public"
		if ch.IsPrivate() {
			vis = "private"
		}
		logInfof("  %s  %s  (%s)", ch.ChannelID, ch.Name, vis)
	}
	logInfof("%d channels registered", len(channels))

	// Set up cache (if enabled)
	var cache *hlsCache
	if *cacheEnabled {
		cache = newHLSCache(*cacheMaxEntries)
		logInfof("cache enabled (max %d entries)", *cacheMaxEntries)
	}

	// Start HTTP server
	server := newBridgeServer(registry, cache)
	httpSrv := &http.Server{
		Handler:           server,
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	ln, err := net.Listen("tcp", *listenAddr)
	if err != nil {
		fatal("listen %s: %v", *listenAddr, err)
	}
	addr := displayListenAddr(ln.Addr().String())
	logInfof("listening on %s", addr)
	channelList := registry.ListChannels()
	for _, ch := range channelList {
		logInfof("stream: http://%s/tltv/v1/channels/%s/stream.m3u8", addr, ch.ChannelID)
	}
	if len(channelList) == 1 {
		logInfof("tltv URI: tltv://%s@%s", channelList[0].ChannelID, addr)
	}

	go func() {
		if err := httpSrv.Serve(ln); err != nil && err != http.ErrServerClosed {
			logFatalf("server error: %v", err)
		}
	}()

	// Start poll loop
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start cache goroutines
	if cache != nil {
		startCacheGoroutines(cache, *cacheStatsInterval, ctx.Done())
	}

	if pollDur > 0 {
		go bridgePollLoop(ctx, pollDur, *streamArg, *guideArg, *nameArg, *onDemand, registry)
	}

	// Wait for shutdown signal
	sigCh := make(chan os.Signal, 1)
	signalNotify(sigCh)
	<-sigCh

	logInfof("shutting down...")
	cancel()

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	httpSrv.Shutdown(shutdownCtx)
}

// bridgePollLoop re-polls the source at the given interval.
func bridgePollLoop(ctx context.Context, interval time.Duration, streamArg, guideArg, nameArg string, onDemand bool, registry *bridgeRegistry) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			bridgeDoPoll(streamArg, guideArg, nameArg, onDemand, registry)
		}
	}
}

// bridgeDoPoll performs a single poll cycle. Errors are logged, not fatal.
func bridgeDoPoll(streamArg, guideArg, nameArg string, onDemand bool, registry *bridgeRegistry) {
	channels, sidecarGuide, err := bridgePollSource(streamArg, nameArg, onDemand)
	if err != nil {
		logErrorf("poll error: %v", err)
		return
	}

	if err := registry.UpdateChannels(channels); err != nil {
		logErrorf("update error: %v", err)
		return
	}

	guide := sidecarGuide
	if guide == nil {
		guide = make(map[string][]bridgeGuideEntry)
	}
	if guideArg != "" {
		externalGuide, err := bridgePollGuide(guideArg)
		if err != nil {
			logErrorf("guide poll error: %v", err)
		} else {
			for id, entries := range externalGuide {
				if _, ok := guide[id]; !ok {
					guide[id] = entries
				}
			}
		}
	}
	if len(guide) > 0 {
		registry.UpdateGuide(guide)
	}

	logDebugf("poll: %d channels", len(channels))
}
