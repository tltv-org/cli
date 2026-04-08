package main

import (
	"context"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"sync/atomic"
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

	peersStr := addPeersFlag(fs)
	gossipEnabled := addGossipFlag(fs)

	// --- Config ---
	configPathBridge, dumpConfigBridge := addConfigFlags(fs)

	// --- Cache ---
	cacheEnabled, cacheMaxEntries, cacheStatsInterval := addCacheFlags(fs)

	// --- Viewer (parsed manually before fs.Parse) ---
	var viewer viewerConfig

	// --- TLS ---
	tlsEnabled, tlsCert, tlsKey, acmeEmail, tlsStaging := addTLSFlags(fs)

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
		fmt.Fprintf(os.Stderr, "  -l, --listen ADDR        listen address (default: :8000, :443 with --tls)\n")
		fmt.Fprintf(os.Stderr, "  -k, --keys-dir PATH      key storage directory (default: /data/keys)\n")
		fmt.Fprintf(os.Stderr, "  -H, --hostname HOST      public host:port for origins field\n\n")
		fmt.Fprintf(os.Stderr, "Peers:\n")
		fmt.Fprintf(os.Stderr, "  -P, --peers LIST         tltv:// URIs to advertise in peer exchange\n")
		fmt.Fprintf(os.Stderr, "  -g, --gossip             re-advertise validated gossip-discovered channels\n\n")
		fmt.Fprintf(os.Stderr, "Config:\n")
		fmt.Fprintf(os.Stderr, "      --config PATH        config file (JSON)\n")
		fmt.Fprintf(os.Stderr, "      --dump-config        print resolved config as JSON and exit\n\n")
		fmt.Fprintf(os.Stderr, "TLS:\n")
		fmt.Fprintf(os.Stderr, "      --tls                enable TLS (autocert via Let's Encrypt if no cert/key)\n")
		fmt.Fprintf(os.Stderr, "      --tls-cert FILE      TLS certificate file (PEM)\n")
		fmt.Fprintf(os.Stderr, "      --tls-key FILE       TLS private key file (PEM)\n")
		fmt.Fprintf(os.Stderr, "      --tls-staging        use Let's Encrypt staging (for testing)\n")
		fmt.Fprintf(os.Stderr, "      --acme-email EMAIL   email for ACME account (optional)\n\n")
		fmt.Fprintf(os.Stderr, "Cache:\n")
		fmt.Fprintf(os.Stderr, "      --cache              enable in-memory response cache\n")
		fmt.Fprintf(os.Stderr, "      --cache-max-entries  max cached items (default: 100)\n")
		fmt.Fprintf(os.Stderr, "      --cache-stats N      log cache stats every N seconds (0 = off)\n\n")
		fmt.Fprintf(os.Stderr, "Viewer:\n")
		fmt.Fprintf(os.Stderr, "      --viewer [CHANNEL]   serve built-in web player at / (channel ID or tltv:// URI;\n")
		fmt.Fprintf(os.Stderr, "                           must be a bridged channel; default: first public channel)\n\n")
		fmt.Fprintf(os.Stderr, "Logging:\n")
		fmt.Fprintf(os.Stderr, "      --log-level LEVEL    log level: debug, info, error (default: info)\n")
		fmt.Fprintf(os.Stderr, "      --log-format FORMAT  log format: human, json (default: human)\n")
		fmt.Fprintf(os.Stderr, "      --log-file PATH      log to file instead of stderr\n\n")
		fmt.Fprintf(os.Stderr, "Environment variables: STREAM, GUIDE, NAME, ON_DEMAND=1, POLL,\n")
		fmt.Fprintf(os.Stderr, "LISTEN, KEYS_DIR, HOSTNAME, PEERS, GOSSIP=1, CONFIG,\n")
		fmt.Fprintf(os.Stderr, "TLS=1, TLS_CERT, TLS_KEY, TLS_STAGING=1, TLS_DIR, ACME_EMAIL,\n")
		fmt.Fprintf(os.Stderr, "CACHE=1, CACHE_MAX_ENTRIES, CACHE_STATS, VIEWER,\n")
		fmt.Fprintf(os.Stderr, "LOG_LEVEL, LOG_FORMAT, LOG_FILE.\n")
		fmt.Fprintf(os.Stderr, "Flags override env vars.\n\n")
		fmt.Fprintf(os.Stderr, "Examples:\n")
		fmt.Fprintf(os.Stderr, "  tltv bridge --stream http://example.com/live.m3u8 --name \"My Channel\"\n")
		fmt.Fprintf(os.Stderr, "  tltv bridge --stream http://source.tv/live.m3u8 --tls --hostname mychannel.tv\n")
		fmt.Fprintf(os.Stderr, "  tltv bridge --stream http://provider.com/channels.m3u --guide http://provider.com/guide.xml\n")
		fmt.Fprintf(os.Stderr, "  tltv bridge --stream /media/hls\n")
		fmt.Fprintf(os.Stderr, "  tltv bridge --stream http://tunarr:8000/api/channels.m3u --guide http://tunarr:8000/api/xmltv.xml --on-demand\n")
	}
	args, viewer = parseViewerArg(args)
	fs.Parse(args)

	// Override default listen port for TLS.
	if *tlsEnabled || *tlsCert != "" {
		tlsOverrideListenPort(fs, listenAddr)
	}

	// Set up logging
	if err := setupLogging(*logLvl, *logFmt, *logPath, "bridge"); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	// Load config file (if specified). Config values fill in unset flags.
	var bridgeGuideEntries []guideEntry // from config inline guide
	if *configPathBridge != "" {
		cfg, err := loadDaemonConfig(*configPathBridge)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		applyConfigToFlags(fs, cfg)
		applyViewerConfig(&viewer, cfg)
		// Handle polymorphic guide from config
		if guideVal, ok := cfg["guide"]; ok {
			entries, filePath, gerr := parseGuideConfig(guideVal)
			if gerr != nil {
				fmt.Fprintf(os.Stderr, "error: config guide: %v\n", gerr)
				os.Exit(1)
			}
			if filePath != "" && *guideArg == "" {
				*guideArg = filePath
			}
			bridgeGuideEntries = entries
		}
	}

	// --dump-config: print resolved config and exit.
	// Only includes fields that differ from compiled defaults.
	if *dumpConfigBridge {
		cfg := map[string]interface{}{}
		if *streamArg != "" {
			cfg["stream"] = *streamArg
		}
		if *nameArg != "" {
			cfg["name"] = *nameArg
		}
		if *onDemand {
			cfg["on_demand"] = true
		}
		if *pollStr != "60s" {
			cfg["poll"] = *pollStr
		}
		if *hostnameArg != "" {
			cfg["hostname"] = *hostnameArg
		}
		if *cacheEnabled {
			cfg["cache"] = true
		}
		if viewer.enabled {
			if viewer.selector != "" {
				cfg["viewer"] = viewer.selector
			} else {
				cfg["viewer"] = true
			}
		}
		if *gossipEnabled {
			cfg["gossip"] = true
		}
		if *peersStr != "" {
			cfg["peers"] = *peersStr
		}
		if *tlsEnabled {
			cfg["tls"] = true
		}
		if *tlsCert != "" {
			cfg["tls_cert"] = *tlsCert
		}
		if *tlsKey != "" {
			cfg["tls_key"] = *tlsKey
		}
		if *acmeEmail != "" {
			cfg["acme_email"] = *acmeEmail
		}
		if *tlsStaging {
			cfg["tls_staging"] = true
		}
		if *logLvl != "" {
			cfg["log_level"] = *logLvl
		}
		if *logFmt != "" {
			cfg["log_format"] = *logFmt
		}
		if *logPath != "" {
			cfg["log_file"] = *logPath
		}
		if len(bridgeGuideEntries) > 0 {
			cfg["guide"] = bridgeGuideEntries
		} else if *guideArg != "" {
			cfg["guide"] = *guideArg
		}
		dumpDaemonConfig(cfg, os.Stdout)
		return
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

	// Parse --peers (tltv:// URIs for external peer exchange)
	peerTargets, err := parsePeerTargets(*peersStr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	// Ensure keys directory exists
	if err := os.MkdirAll(*keysDir, 0700); err != nil {
		fatal("could not create keys directory %s: %v", *keysDir, err)
	}

	// Create registry
	registry := newBridgeRegistry(*keysDir, *hostnameArg)

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
		guide = make(map[string][]guideEntry)
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

	// Context for background goroutines
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Set up peer registry (--peers)
	var peerReg *peerRegistry
	if len(peerTargets) > 0 {
		peerReg = newPeerRegistry()
		client := newClient(flagInsecure)
		go peerPollLoop(ctx, client, peerTargets, peerReg, 5*time.Minute)
		logInfof("peers: verifying %d external channels", len(peerTargets))
	}

	// Set up gossip registry (--gossip: discover channels from --peers nodes)
	var gossipReg *peerRegistry
	if *gossipEnabled && len(peerTargets) > 0 {
		gossipReg = newPeerRegistry()
		gossipNodes := gossipNodesFromPeers(peerTargets)
		client := newClient(flagInsecure)
		go gossipPollLoop(ctx, client, gossipNodes, gossipReg.Update, 10*time.Minute)
		logInfof("gossip: discovering channels from %d nodes", len(gossipNodes))
	}

	// Apply inline guide entries from config (if any)
	if len(bridgeGuideEntries) > 0 {
		guideMap := make(map[string][]guideEntry)
		for _, ch := range registry.ListChannels() {
			guideMap[ch.ChannelID] = bridgeGuideEntries
		}
		registry.UpdateGuide(guideMap)
	}

	// Start HTTP server
	server := newBridgeServer(registry, cache, peerReg, gossipReg)

	// Embed viewer
	var viewerChannelName string
	if viewer.enabled {
		viewerID, err := resolveViewerChannelID(viewer.selector)
		if err != nil {
			fatal("viewer: %v", err)
		}

		// Find the channel to display
		var viewerChID string
		if viewerID != "" {
			// Explicit channel selection
			ch := registry.GetChannel(viewerID)
			if ch == nil {
				fatal("viewer: channel %s not found in bridge", viewerID)
			}
			viewerChID = ch.ChannelID
			viewerChannelName = ch.Name
		} else {
			// Auto-select first public channel
			for _, ch := range registry.ListChannels() {
				if !ch.IsPrivate() {
					viewerChID = ch.ChannelID
					viewerChannelName = ch.Name
					break
				}
			}
		}

		if viewerChID != "" {
			chID := viewerChID
			viewerEmbedRoutes(server.mux, func() map[string]interface{} {
				current := registry.GetChannel(chID)
				if current == nil {
					return map[string]interface{}{}
				}
				info := viewerBuildInfo(current.ChannelID, current.Name, current.metadata, current.guideDoc)
				info["stream_src"] = "/tltv/v1/channels/" + current.ChannelID + "/stream.m3u8"
				info["xmltv_url"] = "/tltv/v1/channels/" + current.ChannelID + "/guide.xml"
				if registry.hostname != "" {
					info["tltv_uri"] = formatTLTVUri(current.ChannelID, []string{registry.hostname}, "")
				}
				return info
			})
		}
	}

	// Set up TLS (if enabled).
	tlsCfg, tlsCleanup, tlsErr := tlsSetup(*hostnameArg, *tlsEnabled, *tlsCert, *tlsKey, *acmeEmail, *tlsStaging)
	if tlsErr != nil {
		fatal("tls: %v", tlsErr)
	}
	defer tlsCleanup()

	scheme := "http"
	if tlsCfg != nil {
		scheme = "https"
	}

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
	logInfof("listening on %s (%s)", addr, scheme)
	channelList := registry.ListChannels()
	for _, ch := range channelList {
		logInfof("stream: %s://%s/tltv/v1/channels/%s/stream.m3u8", scheme, addr, ch.ChannelID)
	}
	if len(channelList) == 1 {
		logInfof("tltv URI: tltv://%s@%s", channelList[0].ChannelID, addr)
	}
	if viewer.enabled && viewerChannelName != "" {
		logInfof("viewer: %s://%s (channel: %s)", scheme, addr, viewerChannelName)
	}

	if tlsCfg != nil {
		httpSrv.TLSConfig = tlsCfg
		go func() {
			if err := httpSrv.ServeTLS(ln, "", ""); err != nil && err != http.ErrServerClosed {
				logFatalf("server error: %v", err)
			}
		}()
	} else {
		go func() {
			if err := httpSrv.Serve(ln); err != nil && err != http.ErrServerClosed {
				logFatalf("server error: %v", err)
			}
		}()
	}

	// Start cache goroutines
	if cache != nil {
		startCacheGoroutines(cache, *cacheStatsInterval, ctx.Done())
	}

	// Atomic config for reloadable fields (written by config goroutine, read by poll loop)
	var bridgeLiveConfig atomic.Pointer[bridgeReloadableConfig]
	bridgeLiveConfig.Store(&bridgeReloadableConfig{
		stream: *streamArg,
		name:   *nameArg,
		guide:  *guideArg,
	})

	// Config watcher goroutine (if config file provided)
	if *configPathBridge != "" {
		go configReloadLoop(ctx, newConfigWatcher(*configPathBridge), func(cfg map[string]interface{}) {
			bridgeApplyReloadedConfig(cfg, &bridgeLiveConfig, registry)
		})
	}

	if pollDur > 0 {
		go bridgePollLoop(ctx, pollDur, &bridgeLiveConfig, *onDemand, registry)
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
// Reads current config from the atomic pointer each cycle.
func bridgePollLoop(ctx context.Context, interval time.Duration,
	liveConfig *atomic.Pointer[bridgeReloadableConfig], onDemand bool, registry *bridgeRegistry) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			cfg := liveConfig.Load()
			bridgeDoPoll(cfg.stream, cfg.guide, cfg.name, onDemand, registry)
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
		guide = make(map[string][]guideEntry)
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

// ---------- Config Reload ----------

// bridgeReloadableConfig holds bridge fields that can be changed via config hot-reload.
// Written by configReloadLoop, read by bridgePollLoop.
type bridgeReloadableConfig struct {
	stream string
	name   string
	guide  string // guide file path or empty
}

// bridgeApplyReloadedConfig applies reloaded config values to the atomic config pointer.
// Inline guide entries are applied directly to the registry.
func bridgeApplyReloadedConfig(cfg map[string]interface{}, liveConfig *atomic.Pointer[bridgeReloadableConfig], registry *bridgeRegistry) {
	current := liveConfig.Load()
	newStream := current.stream
	newName := current.name
	newGuide := current.guide
	changed := false

	if s, ok := configGetString(cfg, "stream"); ok && s != current.stream {
		logInfof("config: stream changed to %q", s)
		newStream = s
		changed = true
	}
	if s, ok := configGetString(cfg, "name"); ok && s != current.name {
		logInfof("config: name changed to %q", s)
		newName = s
		changed = true
	}
	// Handle polymorphic guide from config
	if guideVal, ok := cfg["guide"]; ok {
		entries, filePath, gerr := parseGuideConfig(guideVal)
		if gerr != nil {
			logErrorf("config: guide: %v", gerr)
		} else if filePath != "" {
			if filePath != current.guide {
				logInfof("config: guide source changed to %q", filePath)
				newGuide = filePath
				changed = true
			}
		} else if len(entries) > 0 {
			// Apply inline guide entries directly to registry
			guideMap := make(map[string][]guideEntry)
			for _, ch := range registry.ListChannels() {
				guideMap[ch.ChannelID] = entries
			}
			registry.UpdateGuide(guideMap)
			logInfof("config: guide updated (%d inline entries)", len(entries))
			changed = true
		}
	}

	if changed {
		liveConfig.Store(&bridgeReloadableConfig{
			stream: newStream,
			name:   newName,
			guide:  newGuide,
		})
		logInfof("config reloaded")
	}
}
