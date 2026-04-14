package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"time"
)

const relayMaxMigrationHops = 5

type relayStartupVerified struct {
	target relayTarget
	result *fetchResult
}

type relayStartupState struct {
	migrations map[string][]byte
	verified   []relayStartupVerified
}

// relayBackoff sleeps for the current backoff and doubles it (capped at 30s).
func relayBackoff(backoff *time.Duration) {
	time.Sleep(*backoff)
	*backoff *= 2
	if *backoff > 30*time.Second {
		*backoff = 30 * time.Second
	}
}

func relayInitialStartupState(client *Client, channels, nodes []string, timeout time.Duration) (*relayStartupState, error) {
	deadline := time.Now().Add(timeout)
	backoff := time.Second
	attempt := 1

	for {
		logInfof("startup discovery attempt %d", attempt)

		targets, err := relayDiscoverTargets(client, channels, nodes)
		if err != nil {
			if time.Now().After(deadline) {
				return nil, fmt.Errorf("target discovery: %w", err)
			}
			logErrorf("startup discovery attempt %d failed: %v (retrying in %s)", attempt, err, backoff)
			relayBackoff(&backoff)
			attempt++
			continue
		}
		if len(targets) == 0 {
			if time.Now().After(deadline) {
				return nil, fmt.Errorf("no channels found to relay")
			}
			logErrorf("startup discovery attempt %d found no channels (retrying in %s)", attempt, backoff)
			relayBackoff(&backoff)
			attempt++
			continue
		}

		state := &relayStartupState{migrations: make(map[string][]byte)}
		for _, t := range targets {
			res, err := fetchAndVerifyMetadata(client, t.ChannelID, t.Hints)
			if err != nil {
				logErrorf("startup skip %s: %v", t.ChannelID, err)
				continue
			}

			if res.IsMigration {
				logInfof("%s migrated, following chain...", t.ChannelID)
				finalID, finalRes, err := relayFollowMigration(client, t.ChannelID, t.Hints, relayMaxMigrationHops)
				if err != nil {
					logErrorf("startup skip %s: migration: %v", t.ChannelID, err)
					continue
				}
				state.migrations[t.ChannelID] = res.Raw
				res = finalRes
				t = relayTarget{ChannelID: finalID, Hints: t.Hints}
			}

			if err := checkChannelAccess(res.Doc); err != nil {
				logErrorf("startup skip %s: %v", t.ChannelID, err)
				continue
			}

			state.verified = append(state.verified, relayStartupVerified{target: t, result: res})
		}

		if len(state.verified) > 0 {
			return state, nil
		}

		if time.Now().After(deadline) {
			return nil, fmt.Errorf("no channels could be verified for relaying")
		}
		logErrorf("startup attempt %d verified no channels yet (retrying in %s)", attempt, backoff)
		relayBackoff(&backoff)
		attempt++
	}
}

// cmdRelay implements the "tltv relay" subcommand -- a TLTV relay node
// that re-serves channels from upstream nodes with signature verification.
func cmdRelay(args []string) {
	fs := flag.NewFlagSet("relay", flag.ExitOnError)

	// Input flags
	channelsStr := fs.String("channels", os.Getenv("CHANNELS"), "tltv:// URIs or id@host:port (comma-separated)")
	fs.StringVar(channelsStr, "c", os.Getenv("CHANNELS"), "alias for --channels")
	nodeStr := fs.String("node", os.Getenv("NODE"), "relay all public channels from node(s) (comma-separated host:port)")
	fs.StringVar(nodeStr, "n", os.Getenv("NODE"), "alias for --node")
	configPath, dumpConfigRelay := addConfigFlags(fs, configFlagOpts{ConfigShort: "f", DumpShort: "D"})

	// Server flags
	defaultListen := ":8000"
	if v := os.Getenv("LISTEN"); v != "" {
		defaultListen = v
	}
	listenAddr := fs.String("listen", defaultListen, "listen address")
	fs.StringVar(listenAddr, "l", defaultListen, "alias for --listen")

	hostnameArg := fs.String("hostname", os.Getenv("HOSTNAME"), "public host:port for peer exchange")
	fs.StringVar(hostnameArg, "H", os.Getenv("HOSTNAME"), "alias for --hostname")

	peersStr := addPeersFlag(fs)
	gossipEnabled := addGossipFlag(fs)
	proxyStr := addProxyFlag(fs)

	// Cache flags
	cacheEnabled, cacheMaxEntries, cacheStatsInterval := addCacheFlags(fs)

	// Buffer flags
	bufferStr := fs.String("buffer", os.Getenv("BUFFER"), "proactive buffer duration (e.g. 2h, 30m)")
	fs.StringVar(bufferStr, "b", os.Getenv("BUFFER"), "alias for --buffer")
	delayStr := fs.String("delay", os.Getenv("DELAY"), "serve with time delay (e.g. 30m, requires --buffer)")
	fs.StringVar(delayStr, "y", os.Getenv("DELAY"), "alias for --delay")
	bufferMaxMemStr := fs.String("buffer-max-memory", os.Getenv("BUFFER_MAX_MEMORY"), "max total buffer memory (default: 1g)")
	fs.StringVar(bufferMaxMemStr, "B", os.Getenv("BUFFER_MAX_MEMORY"), "alias for --buffer-max-memory")

	// Viewer (parsed manually before fs.Parse)
	var viewer viewerConfig
	viewerTitle, viewerNoFooter := addViewerDisplayFlags(fs)

	// --- TLS ---
	tlsEnabled, tlsCert, tlsKey, acmeEmail, tlsStaging := addTLSFlags(fs)

	// Tuning flags
	defaultMetaPoll := "60s"
	if v := os.Getenv("META_POLL"); v != "" {
		defaultMetaPoll = v
	}
	metaPollStr := fs.String("meta-poll", defaultMetaPoll, "metadata poll interval")
	fs.StringVar(metaPollStr, "m", defaultMetaPoll, "alias for --meta-poll")

	defaultGuidePoll := "15m"
	if v := os.Getenv("GUIDE_POLL"); v != "" {
		defaultGuidePoll = v
	}
	guidePollStr := fs.String("guide-poll", defaultGuidePoll, "guide poll interval")
	fs.StringVar(guidePollStr, "G", defaultGuidePoll, "alias for --guide-poll")

	defaultPeerPoll := "30m"
	if v := os.Getenv("PEER_POLL"); v != "" {
		defaultPeerPoll = v
	}
	peerPollStr := fs.String("peer-poll", defaultPeerPoll, "peer poll interval")
	fs.StringVar(peerPollStr, "p", defaultPeerPoll, "alias for --peer-poll")

	defaultMaxPeers := 100
	if v := os.Getenv("MAX_PEERS"); v != "" {
		fmt.Sscanf(v, "%d", &defaultMaxPeers)
	}
	maxPeers := fs.Int("max-peers", defaultMaxPeers, "max peers in exchange")
	fs.IntVar(maxPeers, "M", defaultMaxPeers, "alias for --max-peers")

	defaultStaleDays := 7
	if v := os.Getenv("STALE_DAYS"); v != "" {
		fmt.Sscanf(v, "%d", &defaultStaleDays)
	}
	staleDays := fs.Int("stale-days", defaultStaleDays, "drop peers not seen in N days")
	fs.IntVar(staleDays, "s", defaultStaleDays, "alias for --stale-days")

	// --- Logging ---
	logLvl, logFmt, logPath := addLogFlags(fs)

	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Start a TLTV relay node\n\n")
		fmt.Fprintf(os.Stderr, "Usage: tltv relay [flags]\n\n")
		fmt.Fprintf(os.Stderr, "Re-serves existing TLTV channels from upstream nodes with full\n")
		fmt.Fprintf(os.Stderr, "signature verification. Proxies streams, participates in gossip.\n\n")
		fmt.Fprintf(os.Stderr, "Input:\n")
		fmt.Fprintf(os.Stderr, "  -c, --channels LIST      tltv:// URIs or id@host:port (comma-separated)\n")
		fmt.Fprintf(os.Stderr, "  -n, --node HOST:PORT     relay all public channels from a node (comma-separated)\n\n")
		fmt.Fprintf(os.Stderr, "Server:\n")
		fmt.Fprintf(os.Stderr, "  -l, --listen ADDR        listen address (default: :8000, :443 with --tls)\n")
		fmt.Fprintf(os.Stderr, "  -H, --hostname HOST      public host:port for peer exchange\n\n")
		fmt.Fprintf(os.Stderr, "Peers:\n")
		fmt.Fprintf(os.Stderr, "  -P, --peers LIST         tltv:// URIs to advertise in peer exchange\n")
		fmt.Fprintf(os.Stderr, "  -g, --gossip             re-advertise validated gossip-discovered channels\n")
		fmt.Fprintf(os.Stderr, "  -x, --proxy URL          proxy URL (socks5://, http://, https://)\n\n")
		fmt.Fprintf(os.Stderr, "Config:\n")
		fmt.Fprintf(os.Stderr, "  -f, --config PATH        relay config file (JSON)\n")
		fmt.Fprintf(os.Stderr, "  -D, --dump-config        print resolved config as JSON and exit\n\n")
		fmt.Fprintf(os.Stderr, "Cache:\n")
		fmt.Fprintf(os.Stderr, "      --cache              enable in-memory HLS stream cache\n")
		fmt.Fprintf(os.Stderr, "      --cache-max-entries  max cached items (default: 100)\n")
		fmt.Fprintf(os.Stderr, "      --cache-stats N      log cache stats every N seconds (0 = off)\n\n")
		fmt.Fprintf(os.Stderr, "Buffer:\n")
		fmt.Fprintf(os.Stderr, "  -b, --buffer DUR         proactive buffer duration (e.g. 2h, 30m)\n")
		fmt.Fprintf(os.Stderr, "  -y, --delay DUR          serve stream with time delay (requires --buffer)\n")
		fmt.Fprintf(os.Stderr, "  -B, --buffer-max-memory  max total buffer memory (default: 1g)\n\n")
		fmt.Fprintf(os.Stderr, "TLS:\n")
		fmt.Fprintf(os.Stderr, "      --tls                enable TLS (autocert via Let's Encrypt if no cert/key)\n")
		fmt.Fprintf(os.Stderr, "      --tls-cert FILE      TLS certificate file (PEM)\n")
		fmt.Fprintf(os.Stderr, "      --tls-key FILE       TLS private key file (PEM)\n")
		fmt.Fprintf(os.Stderr, "      --tls-staging        use Let's Encrypt staging (for testing)\n")
		fmt.Fprintf(os.Stderr, "      --acme-email EMAIL   email for ACME account (optional)\n\n")
		fmt.Fprintf(os.Stderr, "Viewer:\n")
		fmt.Fprintf(os.Stderr, "      --viewer [CHANNEL]   serve built-in web player at / (channel ID or tltv:// URI;\n")
		fmt.Fprintf(os.Stderr, "                           must be a relayed channel; default: first channel)\n\n")
		fmt.Fprintf(os.Stderr, "Tuning:\n")
		fmt.Fprintf(os.Stderr, "  -m, --meta-poll DUR      metadata poll interval (default: 60s)\n")
		fmt.Fprintf(os.Stderr, "  -G, --guide-poll DUR     guide poll interval (default: 15m)\n")
		fmt.Fprintf(os.Stderr, "  -p, --peer-poll DUR      peer poll interval (default: 30m)\n")
		fmt.Fprintf(os.Stderr, "  -M, --max-peers INT      max peers in exchange (default: 100)\n")
		fmt.Fprintf(os.Stderr, "  -s, --stale-days INT     drop peers not seen in N days (default: 7)\n\n")
		fmt.Fprintf(os.Stderr, "Logging:\n")
		fmt.Fprintf(os.Stderr, "      --log-level LEVEL    log level: debug, info, error (default: info)\n")
		fmt.Fprintf(os.Stderr, "      --log-format FORMAT  log format: human, json (default: human)\n")
		fmt.Fprintf(os.Stderr, "      --log-file PATH      log to file instead of stderr\n\n")
		fmt.Fprintf(os.Stderr, "Environment variables: CHANNELS, NODE, CONFIG, LISTEN, HOSTNAME,\n")
		fmt.Fprintf(os.Stderr, "PEERS, GOSSIP=1, BUFFER, DELAY, BUFFER_MAX_MEMORY,\n")
		fmt.Fprintf(os.Stderr, "TLS=1, TLS_CERT, TLS_KEY, TLS_STAGING=1, TLS_DIR, ACME_EMAIL,\n")
		fmt.Fprintf(os.Stderr, "CACHE=1, CACHE_MAX_ENTRIES, CACHE_STATS, VIEWER,\n")
		fmt.Fprintf(os.Stderr, "META_POLL, GUIDE_POLL, PEER_POLL, MAX_PEERS, STALE_DAYS,\n")
		fmt.Fprintf(os.Stderr, "LOG_LEVEL, LOG_FORMAT, LOG_FILE.\n")
		fmt.Fprintf(os.Stderr, "Flags override env vars.\n\n")
		fmt.Fprintf(os.Stderr, "Examples:\n")
		fmt.Fprintf(os.Stderr, "  tltv relay --channels \"tltv://TVabc...@origin.example.com:443\"\n")
		fmt.Fprintf(os.Stderr, "  tltv relay --channels \"tltv://TV...@origin.tv:443\" --tls --hostname relay.tv\n")
		fmt.Fprintf(os.Stderr, "  tltv relay --node origin.example.com:443\n")
		fmt.Fprintf(os.Stderr, "  tltv relay --config relay.json\n")
	}
	args, viewer = parseViewerArg(args)
	fs.Parse(args)

	// Override default listen port for TLS.
	if *tlsEnabled || *tlsCert != "" {
		tlsOverrideListenPort(fs, listenAddr)
	}

	// Set up logging
	if err := setupLogging(*logLvl, *logFmt, *logPath, "relay"); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	stopLogReopen := startLogReopenWatcher()
	defer stopLogReopen()

	// Parse durations
	metaPoll, err := time.ParseDuration(*metaPollStr)
	if err != nil {
		fatal("invalid --meta-poll: %v", err)
	}
	guidePoll, err := time.ParseDuration(*guidePollStr)
	if err != nil {
		fatal("invalid --guide-poll: %v", err)
	}
	peerPoll, err := time.ParseDuration(*peerPollStr)
	if err != nil {
		fatal("invalid --peer-poll: %v", err)
	}

	// Parse buffer/delay durations
	var bufferDur, delayDur time.Duration
	if *bufferStr != "" {
		bufferDur, err = time.ParseDuration(*bufferStr)
		if err != nil {
			fatal("invalid --buffer: %v", err)
		}
		if bufferDur < time.Second {
			fatal("--buffer must be at least 1s")
		}
	}
	if *delayStr != "" {
		delayDur, err = time.ParseDuration(*delayStr)
		if err != nil {
			fatal("invalid --delay: %v", err)
		}
		if bufferDur == 0 {
			fatal("--delay requires --buffer")
		}
		if delayDur >= bufferDur {
			fatal("--delay must be less than --buffer")
		}
	}
	var bufferMaxMem int64 = 1 << 30 // default 1 GB
	if *bufferMaxMemStr != "" {
		bufferMaxMem, err = parseMemorySize(*bufferMaxMemStr)
		if err != nil {
			fatal("invalid --buffer-max-memory: %v", err)
		}
	}

	// Parse --peers (tltv:// URIs for external peer exchange)
	extPeerTargets, err := parsePeerTargets(*peersStr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	// Collect channel and node lists from flags + config
	var channels, nodes []string

	if *channelsStr != "" {
		for _, ch := range strings.Split(*channelsStr, ",") {
			ch = strings.TrimSpace(ch)
			if ch != "" {
				channels = append(channels, ch)
			}
		}
	}
	if *nodeStr != "" {
		for _, n := range strings.Split(*nodeStr, ",") {
			n = strings.TrimSpace(n)
			if n != "" {
				nodes = append(nodes, n)
			}
		}
	}

	// Load config file (shared loader + relay-specific field extraction)
	if *configPath != "" {
		cfg, err := loadDaemonConfig(*configPath)
		if err != nil {
			fatal("config: %v", err)
		}
		// Extract relay-specific array fields
		if ch, ok := configGetStringSlice(cfg, "channels"); ok {
			channels = append(channels, ch...)
		}
		// Support both "node" (flag name) and "nodes" (legacy)
		if n, ok := configGetStringSlice(cfg, "node"); ok {
			nodes = append(nodes, n...)
		} else if n, ok := configGetStringSlice(cfg, "nodes"); ok {
			nodes = append(nodes, n...)
		}
		// Apply scalar config values to flags
		applyConfigToFlags(fs, cfg)
		applyViewerConfig(&viewer, cfg)
	}

	// --dump-config: print resolved config and exit.
	// Only includes fields that differ from compiled defaults.
	if *dumpConfigRelay {
		cfg := map[string]interface{}{}
		if len(channels) > 0 {
			cfg["channels"] = channels
		}
		if len(nodes) > 0 {
			cfg["node"] = nodes
		}
		if *hostnameArg != "" {
			cfg["hostname"] = *hostnameArg
		}
		if *cacheEnabled {
			cfg["cache"] = true
		}
		if *bufferStr != "" {
			cfg["buffer"] = *bufferStr
		}
		if *delayStr != "" {
			cfg["delay"] = *delayStr
		}
		if *bufferMaxMemStr != "" {
			cfg["buffer_max_memory"] = *bufferMaxMemStr
		}
		if viewer.enabled() {
			key := "viewer"
			if viewer.mode == "debug" {
				key = "debug_viewer"
			}
			if viewer.selector != "" {
				cfg[key] = viewer.selector
			} else {
				cfg[key] = true
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
		if *metaPollStr != "60s" {
			cfg["meta_poll"] = *metaPollStr
		}
		if *guidePollStr != "15m" {
			cfg["guide_poll"] = *guidePollStr
		}
		if *peerPollStr != "30m" {
			cfg["peer_poll"] = *peerPollStr
		}
		if *maxPeers != 100 {
			cfg["max_peers"] = *maxPeers
		}
		if *staleDays != 7 {
			cfg["stale_days"] = *staleDays
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
		dumpDaemonConfig(cfg, os.Stdout)
		return
	}

	if len(channels) == 0 && len(nodes) == 0 {
		fmt.Fprintf(os.Stderr, "error: specify --channels, --node, or --config\n\n")
		fmt.Fprintf(os.Stderr, "Examples:\n")
		fmt.Fprintf(os.Stderr, "  tltv relay --channels \"tltv://TVabc...@origin.example.com:443\"\n")
		fmt.Fprintf(os.Stderr, "  tltv relay --node origin.example.com:443\n")
		os.Exit(1)
	}

	// Parse proxy URL
	proxyURL, err := parseProxyURL(*proxyStr)
	if err != nil {
		fatal("%v", err)
	}

	// Create upstream client
	client := newClientWithProxy(flagInsecure, proxyURL)

	startupState, err := relayInitialStartupState(client, channels, nodes, 5*time.Minute)
	if err != nil {
		fatal("startup: %v", err)
	}

	// Create registry
	registry := newRelayRegistry(*hostnameArg, *gossipEnabled, *maxPeers, *staleDays)
	for oldID, raw := range startupState.migrations {
		registry.StoreMigration(oldID, raw)
	}

	// Initial metadata fetch + verification for all targets
	relayTargets := make([]relayTarget, 0, len(startupState.verified))
	for _, v := range startupState.verified {
		registry.UpdateChannel(v.target.ChannelID, v.result.Raw, v.result.Doc, v.target.Hints, v.result.Hint)
		relayTargets = append(relayTargets, v.target)
		name := getString(v.result.Doc, "name")
		logInfof("  %s  %s", v.target.ChannelID, name)
	}

	// Initial guide fetch
	for _, t := range relayTargets {
		raw, entries, err := relayFetchAndVerifyGuide(client, t.ChannelID, t.Hints)
		if err != nil {
			logErrorf("guide %s: %v", t.ChannelID, err)
			continue
		}
		registry.UpdateGuide(t.ChannelID, raw, entries)
	}

	logInfof("%d channels relaying", len(relayTargets))

	// Set up HLS cache (if enabled)
	var cache *hlsCache
	if *cacheEnabled {
		cache = newHLSCache(*cacheMaxEntries)
		logInfof("HLS cache enabled (max %d entries)", *cacheMaxEntries)
	}

	// Set up buffer manager (--buffer)
	var bufMgr *relayBufferManager
	if bufferDur > 0 {
		bufMgr = newRelayBufferManager(bufferMaxMem, delayDur)
		bufMgr.bufferDuration = bufferDur
		// Estimate max segments per channel: buffer duration / segment target duration.
		// Use 6s as default segment duration estimate (adjusted per channel once manifest is fetched).
		estMaxSegs := int(bufferDur.Seconds() / 6)
		if estMaxSegs < 10 {
			estMaxSegs = 10
		}
		for _, t := range relayTargets {
			bufMgr.AddBuffer(t.ChannelID, estMaxSegs)
		}
		logInfof("buffer enabled: %s per channel, max %d MB total", bufferDur, bufferMaxMem>>20)
		if delayDur > 0 {
			logInfof("delay: %s", delayDur)
		}
	}

	// Set up external peer registry (--peers)
	var peerReg *peerRegistry
	if len(extPeerTargets) > 0 {
		peerReg = newPeerRegistry()
		logInfof("peers: verifying %d external channels", len(extPeerTargets))
	}

	// Start HTTP server
	server := newRelayServer(registry, client, cache, peerReg, bufMgr)

	// Embed viewer
	var viewerChannelName string
	if viewer.enabled() {
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
				fatal("viewer: channel %s not found in relay", viewerID)
			}
			viewerChID = ch.ChannelID
			viewerChannelName = ch.Name
		} else {
			// Auto-select first non-migrated channel
			for _, ch := range registry.ListChannels() {
				if ch.Name != "(migrated)" {
					viewerChID = ch.ChannelID
					viewerChannelName = ch.Name
					break
				}
			}
		}

		if viewerChID != "" {
			defaultChID := viewerChID
			relayInfoFn := func(reqChID string) map[string]interface{} {
				chID := defaultChID
				if reqChID != "" {
					chID = reqChID
				}
				current := registry.GetChannel(chID)
				if current == nil {
					return map[string]interface{}{}
				}
				info := viewerBuildInfo(current.ChannelID, current.Name, current.Metadata, current.Guide)
				if current.Metadata != nil && current.StreamHint != "" {
					var meta map[string]interface{}
					if json.Unmarshal(current.Metadata, &meta) == nil {
						if icon, ok := meta["icon"].(string); ok && icon != "" {
							if dataURI := viewerFetchIconDataURI(client.http, client.baseURL(current.StreamHint)+icon, icon); dataURI != "" {
								info["icon_data"] = dataURI
							}
						}
					}
				}
				info["stream_src"] = "/tltv/v1/channels/" + current.ChannelID + "/stream.m3u8"
				info["xmltv_url"] = "/tltv/v1/channels/" + current.ChannelID + "/guide.xml"
				if registry.hostname != "" {
					info["tltv_uri"] = formatTLTVUri(current.ChannelID, []string{registry.hostname}, "")
				}
				return info
			}
			relayChannelsFn := func() []viewerChannelRef {
				channels := registry.ListChannels()
				var refs []viewerChannelRef
				for _, ch := range channels {
					if ch.Name == "(migrated)" {
						continue
					}
					ref := viewerChannelRef{
						ID:    ch.ChannelID,
						Name:  ch.Name,
						Guide: ch.Guide,
					}
					// Extract icon path from raw metadata
					if ch.Metadata != nil {
						var meta map[string]interface{}
						if json.Unmarshal(ch.Metadata, &meta) == nil {
							if icon, ok := meta["icon"].(string); ok && icon != "" {
								ref.IconPath = icon
								if ch.StreamHint != "" {
									ref.IconData = viewerFetchIconDataURI(client.http, client.baseURL(ch.StreamHint)+icon, icon)
								}
							}
						}
					}
					refs = append(refs, ref)
				}
				return refs
			}
			relayViewerOpts := viewerRouteOptions{title: *viewerTitle, noFooter: *viewerNoFooter}
			switch viewer.mode {
			case "debug":
				debugViewerRoutes(server.mux, relayInfoFn, relayChannelsFn, relayViewerOpts)
			default:
				productionViewerRoutes(server.mux, relayInfoFn, relayChannelsFn, relayViewerOpts)
			}
		}
	} else {
		statusPageRoutes(server.mux, func() *NodeInfo {
			channels := registry.ListChannels()
			var relaying []ChannelRef
			for _, ch := range channels {
				if ch.Name != "(migrated)" {
					relaying = append(relaying, ChannelRef{ID: ch.ChannelID, Name: ch.Name})
				}
			}
			return &NodeInfo{Protocol: "tltv", Versions: []int{1}, Relaying: relaying}
		})
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
	for _, t := range relayTargets {
		logInfof("stream: %s://%s/tltv/v1/channels/%s/stream.m3u8", scheme, addr, t.ChannelID)
	}
	if len(relayTargets) == 1 {
		logInfof("tltv URI: tltv://%s@%s", relayTargets[0].ChannelID, addr)
	}
	if viewer.enabled() && viewerChannelName != "" {
		logInfof("viewer: %s://%s (%s, channel: %s)", scheme, addr, viewer.mode, viewerChannelName)
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

	// Start poll loops
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start cache stats + sweep goroutines
	if cache != nil {
		startCacheGoroutines(cache, *cacheStatsInterval, ctx.Done())
	}

	if metaPoll > 0 {
		go relayMetadataPollLoop(ctx, metaPoll, client, registry, bufMgr)
	}
	if guidePoll > 0 {
		go relayGuidePollLoop(ctx, guidePoll, client, registry)
	}
	if peerPoll > 0 && len(nodes) > 0 {
		relayGossipStore := func(id, name string, hints []string) {
			registry.MergePeers([]peerEntry{{
				ChannelID: id, Name: name, Hints: hints, LastSeen: time.Now(),
			}})
		}
		go gossipPollLoop(ctx, client, nodes, relayGossipStore, peerPoll)
	}
	if len(extPeerTargets) > 0 && peerReg != nil {
		go peerPollLoop(ctx, client, extPeerTargets, peerReg, 5*time.Minute)
	}

	// Start buffer fetch goroutines (one per buffered channel)
	if bufMgr != nil {
		for _, t := range relayTargets {
			bufMgr.StartBuffering(ctx, t.ChannelID, registry, client.http)
		}
	}

	// Config watcher — periodically check for config changes.
	// Reloadable: channels, node (re-discover and sync with registry).
	if *configPath != "" {
		go configReloadLoop(ctx, newConfigWatcher(*configPath), func(cfg map[string]interface{}) {
			relayReloadConfig(ctx, cfg, client, registry, bufMgr)
		})
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

// ---------- Poll Loops ----------

// relayMetadataPollLoop periodically re-fetches and verifies metadata.
// Iterates registry.ListChannels() each cycle so dynamically-added channels
// (from config hot-reload) are automatically included.
func relayMetadataPollLoop(ctx context.Context, interval time.Duration, client *Client, registry *relayRegistry, bufMgr *relayBufferManager) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			for _, ch := range registry.ListChannels() {
				if ch.Name == "(migrated)" {
					continue
				}

				currentHints := ch.Hints
				res, err := fetchAndVerifyMetadata(client, ch.ChannelID, currentHints)
				if err != nil {
					// Try failover: enrich hints from cached metadata origins.
					enriched := relayEnrichHints(ch)
					if len(enriched) > len(ch.Hints) {
						logInfof("meta poll %s: primary hints failed, trying %d origin(s)", ch.ChannelID, len(enriched)-len(ch.Hints))
						res, err = fetchAndVerifyMetadata(client, ch.ChannelID, enriched)
						if err == nil {
							currentHints = enriched
						}
					}
					if err != nil {
						logErrorf("meta poll %s: %v", ch.ChannelID, err)
						continue
					}
					// Failover succeeded — update stored hints to include origins.
					logInfof("meta poll %s: failover to origin succeeded", ch.ChannelID)
				}

				if res.IsMigration {
					logInfof("channel %s has migrated to %s, stopping relay", ch.ChannelID, res.MigratedTo)
					registry.StoreMigration(ch.ChannelID, res.Raw)
					if bufMgr != nil {
						bufMgr.RemoveBuffer(ch.ChannelID)
					}
					continue
				}

				// Re-check access (channel may have gone private/on-demand/retired)
				if err := checkChannelAccess(res.Doc); err != nil {
					logInfof("channel %s now %s, stopping relay", ch.ChannelID, err)
					registry.RemoveChannel(ch.ChannelID)
					if bufMgr != nil {
						bufMgr.RemoveBuffer(ch.ChannelID)
					}
					continue
				}

				// Enrich stored hints with origins from metadata for future failover.
				updatedHints := relayMergeOriginHints(currentHints, res.Doc)
				registry.UpdateChannel(ch.ChannelID, res.Raw, res.Doc, updatedHints, res.Hint)
			}
		}
	}
}

func relayHintsEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// relayReloadConfig applies a reloaded config to a running relay.
// Reloadable: channels, node — re-discovers targets and syncs the registry.
func relayReloadConfig(ctx context.Context, cfg map[string]interface{}, client *Client, registry *relayRegistry, bufMgr *relayBufferManager) {
	var channels, nodes []string
	if ch, ok := configGetStringSlice(cfg, "channels"); ok {
		channels = ch
	}
	if n, ok := configGetStringSlice(cfg, "node"); ok {
		nodes = n
	} else if n, ok := configGetStringSlice(cfg, "nodes"); ok {
		nodes = n
	}

	if len(channels) == 0 && len(nodes) == 0 {
		return
	}

	targets, err := relayDiscoverTargets(client, channels, nodes)
	if err != nil {
		logErrorf("config reload: target discovery: %v", err)
		return
	}

	// Build set of new channel IDs
	newIDs := make(map[string]bool)
	for _, t := range targets {
		newIDs[t.ChannelID] = true
	}

	// Add new channels and refresh hints for existing ones.
	added := 0
	updated := 0
	for _, t := range targets {
		existing := registry.GetChannel(t.ChannelID)
		if existing != nil && relayHintsEqual(existing.Hints, t.Hints) {
			continue
		}
		res, err := fetchAndVerifyMetadata(client, t.ChannelID, t.Hints)
		if err != nil {
			logErrorf("config reload: skip %s: %v", t.ChannelID, err)
			continue
		}
		if res.IsMigration {
			continue
		}
		if err := checkChannelAccess(res.Doc); err != nil {
			logErrorf("config reload: skip %s: %v", t.ChannelID, err)
			continue
		}
		registry.UpdateChannel(t.ChannelID, res.Raw, res.Doc, t.Hints, res.Hint)
		if existing == nil && bufMgr != nil {
			bufMgr.AddBuffer(t.ChannelID, 0)
			bufMgr.StartBuffering(ctx, t.ChannelID, registry, client.http)
		}
		name := getString(res.Doc, "name")
		if existing == nil {
			logInfof("config reload: added %s %s", t.ChannelID, name)
			added++
		} else {
			logInfof("config reload: updated hints for %s %s", t.ChannelID, name)
			updated++
		}
	}

	// Remove channels no longer in config
	removed := 0
	for _, ch := range registry.ListChannels() {
		if ch.Name == "(migrated)" {
			continue
		}
		if !newIDs[ch.ChannelID] {
			registry.RemoveChannel(ch.ChannelID)
			if bufMgr != nil {
				bufMgr.RemoveBuffer(ch.ChannelID)
			}
			logInfof("config reload: removed %s", ch.ChannelID)
			removed++
		}
	}

	if added > 0 || updated > 0 || removed > 0 {
		logInfof("config reloaded: %d added, %d updated, %d removed, %d total", added, updated, removed, registry.ChannelCount())
	}
}

// relayGuidePollLoop periodically re-fetches and verifies guide documents.
func relayGuidePollLoop(ctx context.Context, interval time.Duration, client *Client, registry *relayRegistry) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			for _, ch := range registry.ListChannels() {
				if ch.Name == "(migrated)" {
					continue
				}
				raw, entries, err := relayFetchAndVerifyGuide(client, ch.ChannelID, ch.Hints)
				if err != nil {
					logErrorf("guide poll %s: %v", ch.ChannelID, err)
					continue
				}
				registry.UpdateGuide(ch.ChannelID, raw, entries)
			}
		}
	}
}
