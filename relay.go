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

const relayMaxMigrationHops = 5

// cmdRelay implements the "tltv relay" subcommand -- a TLTV relay node
// that re-serves channels from upstream nodes with signature verification.
func cmdRelay(args []string) {
	fs := flag.NewFlagSet("relay", flag.ExitOnError)

	// Input flags
	channelsStr := fs.String("channels", os.Getenv("CHANNELS"), "tltv:// URIs or id@host:port (comma-separated)")
	nodeStr := fs.String("node", os.Getenv("NODE"), "relay all public channels from node(s) (comma-separated host:port)")
	configPath := fs.String("config", os.Getenv("CONFIG"), "path to relay config file (JSON)")

	// Server flags
	defaultListen := ":8000"
	if v := os.Getenv("LISTEN"); v != "" {
		defaultListen = v
	}
	listenAddr := fs.String("listen", defaultListen, "listen address")
	fs.StringVar(listenAddr, "l", defaultListen, "alias for --listen")

	hostnameArg := fs.String("hostname", os.Getenv("HOSTNAME"), "public host:port for peer exchange")
	fs.StringVar(hostnameArg, "H", os.Getenv("HOSTNAME"), "alias for --hostname")

	peersStr := fs.String("peers", os.Getenv("PEERS"), "additional peer hints to advertise")
	fs.StringVar(peersStr, "P", os.Getenv("PEERS"), "alias for --peers")

	// Cache flags
	cacheEnabled, cacheMaxEntries, cacheStatsInterval := addCacheFlags(fs)

	// Viewer
	viewerEnabled := addViewerFlag(fs)

	// --- TLS ---
	tlsEnabled, tlsCert, tlsKey, acmeEmail, tlsStaging := addTLSFlags(fs)

	// Tuning flags
	defaultMetaPoll := "60s"
	if v := os.Getenv("META_POLL"); v != "" {
		defaultMetaPoll = v
	}
	metaPollStr := fs.String("meta-poll", defaultMetaPoll, "metadata poll interval")

	defaultGuidePoll := "15m"
	if v := os.Getenv("GUIDE_POLL"); v != "" {
		defaultGuidePoll = v
	}
	guidePollStr := fs.String("guide-poll", defaultGuidePoll, "guide poll interval")

	defaultPeerPoll := "30m"
	if v := os.Getenv("PEER_POLL"); v != "" {
		defaultPeerPoll = v
	}
	peerPollStr := fs.String("peer-poll", defaultPeerPoll, "peer poll interval")

	defaultMaxPeers := 100
	if v := os.Getenv("MAX_PEERS"); v != "" {
		fmt.Sscanf(v, "%d", &defaultMaxPeers)
	}
	maxPeers := fs.Int("max-peers", defaultMaxPeers, "max peers in exchange")

	defaultStaleDays := 7
	if v := os.Getenv("STALE_DAYS"); v != "" {
		fmt.Sscanf(v, "%d", &defaultStaleDays)
	}
	staleDays := fs.Int("stale-days", defaultStaleDays, "drop peers not seen in N days")

	// --- Logging ---
	logLvl, logFmt, logPath := addLogFlags(fs)

	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Start a TLTV relay node\n\n")
		fmt.Fprintf(os.Stderr, "Usage: tltv relay [flags]\n\n")
		fmt.Fprintf(os.Stderr, "Re-serves existing TLTV channels from upstream nodes with full\n")
		fmt.Fprintf(os.Stderr, "signature verification. Proxies streams, participates in gossip.\n\n")
		fmt.Fprintf(os.Stderr, "Input:\n")
		fmt.Fprintf(os.Stderr, "      --channels LIST      tltv:// URIs or id@host:port (comma-separated)\n")
		fmt.Fprintf(os.Stderr, "      --node HOST:PORT     relay all public channels from a node (comma-separated)\n")
		fmt.Fprintf(os.Stderr, "      --config PATH        relay config file (JSON)\n\n")
		fmt.Fprintf(os.Stderr, "Server:\n")
		fmt.Fprintf(os.Stderr, "  -l, --listen ADDR        listen address (default: :8000, :443 with --tls)\n")
		fmt.Fprintf(os.Stderr, "  -H, --hostname HOST      public host:port for peer exchange\n")
		fmt.Fprintf(os.Stderr, "  -P, --peers LIST         additional peer hints to advertise\n\n")
		fmt.Fprintf(os.Stderr, "Cache:\n")
		fmt.Fprintf(os.Stderr, "      --cache              enable in-memory HLS stream cache\n")
		fmt.Fprintf(os.Stderr, "      --cache-max-entries  max cached items (default: 100)\n")
		fmt.Fprintf(os.Stderr, "      --cache-stats N      log cache stats every N seconds (0 = off)\n\n")
		fmt.Fprintf(os.Stderr, "TLS:\n")
		fmt.Fprintf(os.Stderr, "      --tls                enable TLS (autocert via Let's Encrypt if no cert/key)\n")
		fmt.Fprintf(os.Stderr, "      --tls-cert FILE      TLS certificate file (PEM)\n")
		fmt.Fprintf(os.Stderr, "      --tls-key FILE       TLS private key file (PEM)\n")
		fmt.Fprintf(os.Stderr, "      --tls-staging        use Let's Encrypt staging (for testing)\n")
		fmt.Fprintf(os.Stderr, "      --acme-email EMAIL   email for ACME account (optional)\n\n")
		fmt.Fprintf(os.Stderr, "Viewer:\n")
		fmt.Fprintf(os.Stderr, "      --viewer             serve built-in web player at / (default: off)\n\n")
		fmt.Fprintf(os.Stderr, "Tuning:\n")
		fmt.Fprintf(os.Stderr, "      --meta-poll DUR      metadata poll interval (default: 60s)\n")
		fmt.Fprintf(os.Stderr, "      --guide-poll DUR     guide poll interval (default: 15m)\n")
		fmt.Fprintf(os.Stderr, "      --peer-poll DUR      peer poll interval (default: 30m)\n")
		fmt.Fprintf(os.Stderr, "      --max-peers INT      max peers in exchange (default: 100)\n")
		fmt.Fprintf(os.Stderr, "      --stale-days INT     drop peers not seen in N days (default: 7)\n\n")
		fmt.Fprintf(os.Stderr, "Logging:\n")
		fmt.Fprintf(os.Stderr, "      --log-level LEVEL    log level: debug, info, error (default: info)\n")
		fmt.Fprintf(os.Stderr, "      --log-format FORMAT  log format: human, json (default: human)\n")
		fmt.Fprintf(os.Stderr, "      --log-file PATH      log to file instead of stderr\n\n")
		fmt.Fprintf(os.Stderr, "Environment variables: CHANNELS, NODE, CONFIG, LISTEN, HOSTNAME,\n")
		fmt.Fprintf(os.Stderr, "PEERS, TLS=1, TLS_CERT, TLS_KEY, TLS_STAGING=1, TLS_DIR,\n")
		fmt.Fprintf(os.Stderr, "ACME_EMAIL, CACHE=1, CACHE_MAX_ENTRIES, CACHE_STATS, VIEWER=1,\n")
		fmt.Fprintf(os.Stderr, "META_POLL, GUIDE_POLL, PEER_POLL, MAX_PEERS, STALE_DAYS,\n")
		fmt.Fprintf(os.Stderr, "LOG_LEVEL, LOG_FORMAT, LOG_FILE.\n")
		fmt.Fprintf(os.Stderr, "Flags override env vars.\n\n")
		fmt.Fprintf(os.Stderr, "Examples:\n")
		fmt.Fprintf(os.Stderr, "  tltv relay --channels \"tltv://TVabc...@origin.example.com:443\"\n")
		fmt.Fprintf(os.Stderr, "  tltv relay --channels \"tltv://TV...@origin.tv:443\" --tls --hostname relay.tv\n")
		fmt.Fprintf(os.Stderr, "  tltv relay --node origin.example.com:443\n")
		fmt.Fprintf(os.Stderr, "  tltv relay --config relay.json\n")
	}
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

	// Parse peer hints
	var peerHints []string
	if *peersStr != "" {
		for _, p := range strings.Split(*peersStr, ",") {
			p = strings.TrimSpace(p)
			if p != "" {
				peerHints = append(peerHints, p)
			}
		}
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

	// Load config file
	if *configPath != "" {
		cfg, err := relayLoadConfig(*configPath)
		if err != nil {
			fatal("config: %v", err)
		}
		channels = append(channels, cfg.Channels...)
		nodes = append(nodes, cfg.Nodes...)
	}

	if len(channels) == 0 && len(nodes) == 0 {
		fmt.Fprintf(os.Stderr, "error: specify --channels, --node, or --config\n\n")
		fmt.Fprintf(os.Stderr, "Examples:\n")
		fmt.Fprintf(os.Stderr, "  tltv relay --channels \"tltv://TVabc...@origin.example.com:443\"\n")
		fmt.Fprintf(os.Stderr, "  tltv relay --node origin.example.com:443\n")
		os.Exit(1)
	}

	// Create upstream client
	client := newClient(flagInsecure)

	// Discover relay targets
	logInfof("discovering channels...")
	targets, err := relayDiscoverTargets(client, channels, nodes)
	if err != nil {
		fatal("target discovery: %v", err)
	}
	if len(targets) == 0 {
		fatal("no channels found to relay")
	}

	// Create registry
	registry := newRelayRegistry(*hostnameArg, peerHints, *maxPeers, *staleDays)

	// Initial metadata fetch + verification for all targets
	var relayTargets []relayTarget // successfully verified targets
	for _, t := range targets {
		res, err := relayFetchAndVerifyMetadata(client, t.ChannelID, t.Hints)
		if err != nil {
			logErrorf("skip %s: %v", t.ChannelID, err)
			continue
		}

		if res.IsMigration {
			// Follow migration chain
			logInfof("%s migrated, following chain...", t.ChannelID)
			finalID, finalRes, err := relayFollowMigration(client, t.ChannelID, t.Hints, relayMaxMigrationHops)
			if err != nil {
				logErrorf("skip %s: migration: %v", t.ChannelID, err)
				continue
			}
			// Store migration doc at old ID
			registry.StoreMigration(t.ChannelID, res.Raw)
			// Relay the new channel
			res = finalRes
			t = relayTarget{ChannelID: finalID, Hints: t.Hints}
		}

		// Check access restrictions
		if err := relayCheckAccess(res.Doc); err != nil {
			logErrorf("skip %s: %v", t.ChannelID, err)
			continue
		}

		registry.UpdateChannel(t.ChannelID, res.Raw, res.Doc, t.Hints)
		relayTargets = append(relayTargets, t)

		name := getString(res.Doc, "name")
		logInfof("  %s  %s", t.ChannelID, name)
	}

	if len(relayTargets) == 0 {
		fatal("no channels could be verified for relaying")
	}

	// Initial guide fetch
	for _, t := range relayTargets {
		raw, entries, err := relayFetchAndVerifyGuide(client, t.ChannelID, t.Hints)
		if err != nil {
			logErrorf("guide %s: %v", t.ChannelID, err)
			continue
		}
		if raw != nil {
			registry.UpdateGuide(t.ChannelID, raw, entries)
		}
	}

	logInfof("%d channels relaying", len(relayTargets))

	// Set up HLS cache (if enabled)
	var cache *hlsCache
	if *cacheEnabled {
		cache = newHLSCache(*cacheMaxEntries)
		logInfof("HLS cache enabled (max %d entries)", *cacheMaxEntries)
	}

	// Start HTTP server
	server := newRelayServer(registry, client, cache)

	// Embed viewer (first non-migrated channel)
	var viewerChannelName string
	if *viewerEnabled {
		for _, ch := range registry.ListChannels() {
			if ch.Name != "(migrated)" {
				chID := ch.ChannelID
				viewerEmbedRoutes(server.mux, func() map[string]interface{} {
					current := registry.GetChannel(chID)
					if current == nil {
						return map[string]interface{}{}
					}
					info := viewerBuildInfo(current.ChannelID, current.Name, current.Metadata, current.Guide)
					info["stream_src"] = "/tltv/v1/channels/" + current.ChannelID + "/stream.m3u8"
					info["xmltv_url"] = "/tltv/v1/channels/" + current.ChannelID + "/guide.xml"
					if registry.hostname != "" {
						info["tltv_uri"] = formatTLTVUri(current.ChannelID, []string{registry.hostname}, "")
					}
					return info
				})
				viewerChannelName = ch.Name
				break
			}
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
	for _, t := range relayTargets {
		logInfof("stream: %s://%s/tltv/v1/channels/%s/stream.m3u8", scheme, addr, t.ChannelID)
	}
	if len(relayTargets) == 1 {
		logInfof("tltv URI: tltv://%s@%s", relayTargets[0].ChannelID, addr)
	}
	if *viewerEnabled && viewerChannelName != "" {
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

	// Start poll loops
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start cache stats + sweep goroutines
	if cache != nil {
		startCacheGoroutines(cache, *cacheStatsInterval, ctx.Done())
	}

	if metaPoll > 0 {
		go relayMetadataPollLoop(ctx, metaPoll, client, registry, relayTargets)
	}
	if guidePoll > 0 {
		go relayGuidePollLoop(ctx, guidePoll, client, registry)
	}
	if peerPoll > 0 && len(nodes) > 0 {
		go relayPeerPollLoop(ctx, peerPoll, client, registry, nodes)
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
func relayMetadataPollLoop(ctx context.Context, interval time.Duration, client *Client, registry *relayRegistry, targets []relayTarget) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	stopped := make(map[string]bool) // channels that migrated or were removed

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			for _, t := range targets {
				if stopped[t.ChannelID] {
					continue
				}

				res, err := relayFetchAndVerifyMetadata(client, t.ChannelID, t.Hints)
				if err != nil {
					logErrorf("meta poll %s: %v", t.ChannelID, err)
					continue
				}

				if res.IsMigration {
					logInfof("channel %s has migrated to %s, stopping relay", t.ChannelID, res.MigratedTo)
					registry.StoreMigration(t.ChannelID, res.Raw)
					stopped[t.ChannelID] = true
					continue
				}

				// Re-check access (channel may have gone private/on-demand/retired)
				if err := relayCheckAccess(res.Doc); err != nil {
					logInfof("channel %s now %s, stopping relay", t.ChannelID, err)
					registry.RemoveChannel(t.ChannelID)
					stopped[t.ChannelID] = true
					continue
				}

				registry.UpdateChannel(t.ChannelID, res.Raw, res.Doc, t.Hints)
			}
		}
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
				if raw != nil {
					registry.UpdateGuide(ch.ChannelID, raw, entries)
				}
			}
		}
	}
}

// relayPeerPollLoop periodically fetches peers from known nodes and validates them.
func relayPeerPollLoop(ctx context.Context, interval time.Duration, client *Client, registry *relayRegistry, nodes []string) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			for _, node := range nodes {
				node = normalizeHost(node)
				exchange, err := client.FetchPeers(node)
				if err != nil {
					logErrorf("peer poll %s: %v", node, err)
					continue
				}

				var validated []relayPeerInfo
				for _, p := range exchange.Peers {
					// Validate: fetch node info from first hint, verify channel is listed
					if len(p.Hints) == 0 {
						continue
					}
					hint := p.Hints[0]

					info, err := client.FetchNodeInfo(hint)
					if err != nil {
						continue
					}

					// Check the channel is actually served there
					found := false
					for _, ch := range info.Channels {
						if ch.ID == p.ID {
							found = true
							break
						}
					}
					if !found {
						for _, ch := range info.Relaying {
							if ch.ID == p.ID {
								found = true
								break
							}
						}
					}
					if !found {
						continue
					}

					// Step 3 (spec 11.5): verify signed metadata before adding peer
					metaRes, err := relayFetchAndVerifyMetadata(client, p.ID, []string{hint})
					if err != nil {
						continue
					}
					if metaRes.IsMigration {
						continue
					}

					lastSeen := time.Now()
					if p.LastSeen != "" {
						if t, err := time.Parse(timestampFormat, p.LastSeen); err == nil {
							lastSeen = t
						}
					}

					validated = append(validated, relayPeerInfo{
						ChannelID: p.ID,
						Name:      p.Name,
						Hints:     p.Hints,
						LastSeen:  lastSeen,
					})
				}

				if len(validated) > 0 {
					registry.MergePeers(validated)
				}
			}
		}
	}
}
