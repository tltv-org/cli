package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
	_ "time/tzdata"
)

// envInt returns the environment variable's integer value, or fallback if unset/invalid.
func envInt(name string, fallback int) int {
	if v := os.Getenv(name); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return fallback
}

// Named constants for pre-allocation and encoder thresholds.
const (
	// tsPreAllocPerFrame is the estimated TS packet data per frame (~12 KB).
	tsPreAllocPerFrame = 12288

	// maxWidth and maxHeight cap resolution to prevent OOM (8K).
	maxWidth  = 7680
	maxHeight = 4320
)

// cmdServer dispatches to server subcommands.
func cmdServer(args []string) {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "-help" {
		fmt.Fprintf(os.Stderr, "TLTV content server\n\n")
		fmt.Fprintf(os.Stderr, "Usage: tltv server <subcommand> [flags]\n\n")
		fmt.Fprintf(os.Stderr, "Subcommands:\n")
		fmt.Fprintf(os.Stderr, "  test    Start a test signal generator (SMPTE bars + 1 kHz tone, pure Go)\n\n")
		fmt.Fprintf(os.Stderr, "Use \"tltv server <subcommand> -h\" for help with a specific subcommand.\n")
		if len(args) == 0 {
			os.Exit(1)
		}
		return
	}
	switch args[0] {
	case "test":
		cmdServerTest(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "unknown server subcommand: %s\n\n", args[0])
		fmt.Fprintf(os.Stderr, "Run \"tltv server --help\" for available subcommands.\n")
		os.Exit(1)
	}
}

// cmdServerTest implements "tltv server test" — a self-contained TLTV
// origin server that generates live HLS video and audio entirely in Go.
// Produces SMPTE color bars with a wall clock, channel name, uptime counter,
// and a continuous 1 kHz audio tone (AAC-LC, 48kHz, mono).
func cmdServerTest(args []string) {
	fs := flag.NewFlagSet("server test", flag.ExitOnError)

	// --- Identity ---
	keyFile := fs.String("key", os.Getenv("KEY"), "channel key file (auto-generated if missing)")
	fs.StringVar(keyFile, "k", os.Getenv("KEY"), "alias for --key")

	// --- Source (test screen content) ---
	nameArg := fs.String("name", os.Getenv("NAME"), "channel name")
	fs.StringVar(nameArg, "n", os.Getenv("NAME"), "alias for --name")
	showUptime := fs.Bool("uptime", os.Getenv("UPTIME") == "1", "show uptime instead of wall clock")
	fs.BoolVar(showUptime, "u", os.Getenv("UPTIME") == "1", "alias for --uptime")
	fontScale := fs.Int("font-scale", envInt("FONT_SCALE", 0), "font scale factor (0 = auto from resolution)")
	fs.IntVar(fontScale, "f", envInt("FONT_SCALE", 0), "alias for --font-scale")
	timezoneArg := fs.String("timezone", os.Getenv("TIMEZONE"), "IANA timezone for clock display (e.g. America/New_York)")
	fs.StringVar(timezoneArg, "z", os.Getenv("TIMEZONE"), "alias for --timezone")

	// --- Metadata ---
	descriptionArg := fs.String("description", os.Getenv("DESCRIPTION"), "channel description")
	fs.StringVar(descriptionArg, "D", os.Getenv("DESCRIPTION"), "alias for --description")
	tagsArg := fs.String("tags", os.Getenv("TAGS"), "comma-separated tags (max 5)")
	fs.StringVar(tagsArg, "T", os.Getenv("TAGS"), "alias for --tags")
	languageArg := fs.String("language", os.Getenv("LANGUAGE"), "ISO 639-1 language code (e.g. en, ja)")
	fs.StringVar(languageArg, "a", os.Getenv("LANGUAGE"), "alias for --language")
	iconArg := fs.String("icon", os.Getenv("ICON"), "path to icon file (PNG, JPEG, or SVG)")
	fs.StringVar(iconArg, "c", os.Getenv("ICON"), "alias for --icon")

	// --- Access control ---
	accessArg := fs.String("access", os.Getenv("ACCESS"), "access mode: public (default) or token")
	fs.StringVar(accessArg, "A", os.Getenv("ACCESS"), "alias for --access")
	tokenArg := fs.String("token", os.Getenv("TOKEN"), "access token for private channels")
	fs.StringVar(tokenArg, "t", os.Getenv("TOKEN"), "alias for --token")
	onDemand := fs.Bool("on-demand", os.Getenv("ON_DEMAND") == "1", "mark channel as on-demand")
	fs.BoolVar(onDemand, "O", os.Getenv("ON_DEMAND") == "1", "alias for --on-demand")

	// --- Multi-channel ---
	numChannels := fs.Int("channels", envInt("CHANNELS", 1), "number of channels to generate")
	fs.IntVar(numChannels, "N", envInt("CHANNELS", 1), "alias for --channels")
	variantsArg := fs.String("variants", os.Getenv("VARIANTS"), "comma-separated renditions (e.g. 1080p,720p,360p)")
	fs.StringVar(variantsArg, "V", os.Getenv("VARIANTS"), "alias for --variants")

	// --- Encoder ---
	widthArg := fs.Int("width", envInt("WIDTH", 640), "video width")
	fs.IntVar(widthArg, "X", envInt("WIDTH", 640), "alias for --width")
	heightArg := fs.Int("height", envInt("HEIGHT", 360), "video height")
	fs.IntVar(heightArg, "Y", envInt("HEIGHT", 360), "alias for --height")
	fpsArg := fs.Int("fps", envInt("FPS", 30), "frames per second")
	fs.IntVar(fpsArg, "F", envInt("FPS", 30), "alias for --fps")
	qpArg := fs.Int("qp", envInt("QP", 26), "quantization parameter (0-51)")
	fs.IntVar(qpArg, "Q", envInt("QP", 26), "alias for --qp")

	// --- Stream ---
	defaultListen := ":8000"
	if v := os.Getenv("LISTEN"); v != "" {
		defaultListen = v
	}
	listenAddr := fs.String("listen", defaultListen, "listen address")
	fs.StringVar(listenAddr, "l", defaultListen, "alias for --listen")

	hostnameArg := fs.String("hostname", os.Getenv("HOSTNAME"), "public host:port for origins field (omit for private origin)")
	fs.StringVar(hostnameArg, "H", os.Getenv("HOSTNAME"), "alias for --hostname")

	peersStr := addPeersFlag(fs)
	gossipEnabled := addGossipFlag(fs)

	segDuration := fs.Int("segment-duration", envInt("SEGMENT_DURATION", 2), "HLS segment duration in seconds")
	fs.IntVar(segDuration, "S", envInt("SEGMENT_DURATION", 2), "alias for --segment-duration")
	segCount := fs.Int("segment-count", envInt("SEGMENT_COUNT", 5), "HLS playlist window size (number of segments)")
	fs.IntVar(segCount, "s", envInt("SEGMENT_COUNT", 5), "alias for --segment-count")

	// --- Cache ---
	cacheEnabled, cacheMaxEntries, cacheStatsInterval := addCacheFlags(fs)

	// --- Viewer (parsed manually before fs.Parse) ---
	var viewer viewerConfig

	// --- TLS ---
	tlsEnabled, tlsCert, tlsKey, acmeEmail, tlsStaging := addTLSFlags(fs)

	// --- Config ---
	configPath, dumpConfigFlag := addConfigFlags(fs)

	// --- Logging ---
	logLvl, logFmt, logPath := addLogFlags(fs)

	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Start a TLTV test signal generator\n\n")
		fmt.Fprintf(os.Stderr, "Usage: tltv server test [flags]\n\n")
		fmt.Fprintf(os.Stderr, "Generates a full SMPTE color bar test pattern with wall clock, channel\n")
		fmt.Fprintf(os.Stderr, "name, and 1 kHz audio tone. Pure Go H.264/AAC encoder and HLS segmenter\n")
		fmt.Fprintf(os.Stderr, "— no ffmpeg required. Useful for testing the full TLTV pipeline.\n\n")
		fmt.Fprintf(os.Stderr, "Identity:\n")
		fmt.Fprintf(os.Stderr, "  -k, --key FILE             channel key file (auto-generated if missing)\n")
		fmt.Fprintf(os.Stderr, "  -N, --channels N           number of channels to generate (default: 1)\n\n")
		fmt.Fprintf(os.Stderr, "Source:\n")
		fmt.Fprintf(os.Stderr, "  -n, --name STRING          channel name on test screen (default: TLTV)\n")
		fmt.Fprintf(os.Stderr, "  -u, --uptime               show elapsed time instead of wall clock\n")
		fmt.Fprintf(os.Stderr, "  -z, --timezone TZ          IANA timezone for clock display (default: UTC)\n")
		fmt.Fprintf(os.Stderr, "  -f, --font-scale N         font scale, 0 = auto (default: 0)\n\n")
		fmt.Fprintf(os.Stderr, "Metadata:\n")
		fmt.Fprintf(os.Stderr, "  -D, --description TEXT     channel description\n")
		fmt.Fprintf(os.Stderr, "  -T, --tags LIST            comma-separated tags (max 5)\n")
		fmt.Fprintf(os.Stderr, "  -a, --language CODE        ISO 639-1 language code (e.g. en, ja)\n")
		fmt.Fprintf(os.Stderr, "  -c, --icon PATH            icon file (PNG, JPEG, or SVG)\n\n")
		fmt.Fprintf(os.Stderr, "Access control:\n")
		fmt.Fprintf(os.Stderr, "  -A, --access MODE          access mode: public (default) or token\n")
		fmt.Fprintf(os.Stderr, "  -t, --token STRING         access token for private channels\n")
		fmt.Fprintf(os.Stderr, "  -O, --on-demand            mark channel as on-demand\n\n")
		fmt.Fprintf(os.Stderr, "Encoder:\n")
		fmt.Fprintf(os.Stderr, "  -V, --variants LIST        comma-separated renditions (e.g. 1080p,720p,360p)\n")
		fmt.Fprintf(os.Stderr, "  -X, --width N              video width (default: 640)\n")
		fmt.Fprintf(os.Stderr, "  -Y, --height N             video height (default: 360)\n")
		fmt.Fprintf(os.Stderr, "  -F, --fps N                frames per second (default: 30)\n")
		fmt.Fprintf(os.Stderr, "  -Q, --qp N                 compression quality 0-51, lower = better (default: 26)\n\n")
		fmt.Fprintf(os.Stderr, "Stream:\n")
		fmt.Fprintf(os.Stderr, "  -l, --listen ADDR          listen address (default: :8000, :443 with --tls)\n")
		fmt.Fprintf(os.Stderr, "  -H, --hostname HOST        public host:port for origins (omit for private origin)\n")
		fmt.Fprintf(os.Stderr, "  -S, --segment-duration N   HLS segment duration in seconds (default: 2)\n")
		fmt.Fprintf(os.Stderr, "  -s, --segment-count N      segments in playlist window (default: 5)\n\n")
		fmt.Fprintf(os.Stderr, "Peers:\n")
		fmt.Fprintf(os.Stderr, "  -P, --peers LIST           tltv:// URIs to advertise in peer exchange\n")
		fmt.Fprintf(os.Stderr, "  -g, --gossip               re-advertise validated gossip-discovered channels\n\n")
		fmt.Fprintf(os.Stderr, "Config:\n")
		fmt.Fprintf(os.Stderr, "      --config PATH          config file (JSON)\n")
		fmt.Fprintf(os.Stderr, "      --dump-config          print resolved config as JSON and exit\n\n")
		fmt.Fprintf(os.Stderr, "TLS:\n")
		fmt.Fprintf(os.Stderr, "      --tls                  enable TLS (autocert via Let's Encrypt if no cert/key)\n")
		fmt.Fprintf(os.Stderr, "      --tls-cert FILE        TLS certificate file (PEM)\n")
		fmt.Fprintf(os.Stderr, "      --tls-key FILE         TLS private key file (PEM)\n")
		fmt.Fprintf(os.Stderr, "      --tls-staging          use Let's Encrypt staging (for testing)\n")
		fmt.Fprintf(os.Stderr, "      --acme-email EMAIL     email for ACME account (optional)\n\n")
		fmt.Fprintf(os.Stderr, "Cache:\n")
		fmt.Fprintf(os.Stderr, "      --cache                enable in-memory response cache\n")
		fmt.Fprintf(os.Stderr, "      --cache-max-entries N  max cached items (default: 100)\n")
		fmt.Fprintf(os.Stderr, "      --cache-stats N        log cache stats every N seconds (0 = off)\n\n")
		fmt.Fprintf(os.Stderr, "Viewer:\n")
		fmt.Fprintf(os.Stderr, "      --viewer               serve built-in web player at /\n\n")
		fmt.Fprintf(os.Stderr, "Logging:\n")
		fmt.Fprintf(os.Stderr, "      --log-level LEVEL      log level: debug, info, error (default: info)\n")
		fmt.Fprintf(os.Stderr, "      --log-format FORMAT    log format: human, json (default: human)\n")
		fmt.Fprintf(os.Stderr, "      --log-file PATH        log to file instead of stderr\n\n")
		fmt.Fprintf(os.Stderr, "All flags also accept environment variables (uppercase, underscores):\n")
		fmt.Fprintf(os.Stderr, "  KEY, CHANNELS, NAME, UPTIME, TIMEZONE, FONT_SCALE,\n")
		fmt.Fprintf(os.Stderr, "  DESCRIPTION, TAGS, LANGUAGE, ICON,\n")
		fmt.Fprintf(os.Stderr, "  ACCESS, TOKEN, ON_DEMAND=1, WIDTH, HEIGHT, FPS, QP,\n")
		fmt.Fprintf(os.Stderr, "  LISTEN, HOSTNAME, SEGMENT_DURATION, SEGMENT_COUNT, PEERS, GOSSIP=1,\n")
		fmt.Fprintf(os.Stderr, "  CONFIG, TLS=1, TLS_CERT, TLS_KEY, TLS_STAGING=1, TLS_DIR, ACME_EMAIL,\n")
		fmt.Fprintf(os.Stderr, "  CACHE=1, CACHE_MAX_ENTRIES, CACHE_STATS, VIEWER,\n")
		fmt.Fprintf(os.Stderr, "  LOG_LEVEL, LOG_FORMAT, LOG_FILE\n\n")
		fmt.Fprintf(os.Stderr, "Examples:\n")
		fmt.Fprintf(os.Stderr, "  tltv server test -k channel.key --name \"TLTV Test\"\n")
		fmt.Fprintf(os.Stderr, "  tltv server test --name \"Demo\" --tls --hostname demo.timelooptv.org\n")
		fmt.Fprintf(os.Stderr, "  tltv server test --tls-cert cert.pem --tls-key key.pem\n")
		fmt.Fprintf(os.Stderr, "  tltv server test --width 1920 --height 1080 --fps 30\n")
		fmt.Fprintf(os.Stderr, "  docker run -e NAME=TEST -e TLS=1 -e HOSTNAME=demo.tv tltv server test\n")
	}
	args, viewer = parseViewerArg(args)
	fs.Parse(args)

	// Override default listen port for TLS.
	if *tlsEnabled || *tlsCert != "" {
		tlsOverrideListenPort(fs, listenAddr)
	}

	// Load config file (if specified). Config values fill in unset flags.
	var serverCfg map[string]interface{}
	var serverGuideEntries []guideEntry // from config inline guide
	if *configPath != "" {
		var err error
		serverCfg, err = loadDaemonConfig(*configPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		applyConfigToFlags(fs, serverCfg)
		applyViewerConfig(&viewer, serverCfg)
		// Handle polymorphic guide from config
		if guideVal, ok := serverCfg["guide"]; ok {
			entries, _, err := parseGuideConfig(guideVal)
			if err != nil {
				fmt.Fprintf(os.Stderr, "error: config guide: %v\n", err)
				os.Exit(1)
			}
			serverGuideEntries = entries
		}
	}

	// --dump-config: print resolved config and exit.
	// Only includes fields that differ from compiled defaults.
	if *dumpConfigFlag {
		cfg := map[string]interface{}{}
		if *keyFile != "" {
			cfg["key"] = *keyFile
		}
		if *nameArg != "" {
			cfg["name"] = *nameArg
		}
		if *showUptime {
			cfg["uptime"] = true
		}
		if *fontScale != 0 {
			cfg["font_scale"] = *fontScale
		}
		if *timezoneArg != "" {
			cfg["timezone"] = *timezoneArg
		}
		if *widthArg != 640 {
			cfg["width"] = *widthArg
		}
		if *heightArg != 360 {
			cfg["height"] = *heightArg
		}
		if *fpsArg != 30 {
			cfg["fps"] = *fpsArg
		}
		if *qpArg != 26 {
			cfg["qp"] = *qpArg
		}
		if *accessArg != "" && *accessArg != "public" {
			cfg["access"] = *accessArg
		}
		if *tokenArg != "" {
			cfg["token"] = *tokenArg
		}
		if *onDemand {
			cfg["on_demand"] = true
		}
		if *hostnameArg != "" {
			cfg["hostname"] = *hostnameArg
		}
		if *segDuration != 2 {
			cfg["segment_duration"] = *segDuration
		}
		if *segCount != 5 {
			cfg["segment_count"] = *segCount
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
		if len(serverGuideEntries) > 0 {
			cfg["guide"] = serverGuideEntries
		}
		dumpDaemonConfig(cfg, os.Stdout)
		return
	}

	// Set up logging
	if err := setupLogging(*logLvl, *logFmt, *logPath, "server"); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	channelName := *nameArg
	if channelName == "" {
		channelName = "TLTV"
	}
	displayName := strings.ToUpper(channelName) // for video overlay only

	// Validate settings
	if *widthArg < 16 || *widthArg > maxWidth {
		fmt.Fprintf(os.Stderr, "error: --width must be between 16 and %d\n", maxWidth)
		os.Exit(1)
	}
	if *heightArg < 16 || *heightArg > maxHeight {
		fmt.Fprintf(os.Stderr, "error: --height must be between 16 and %d\n", maxHeight)
		os.Exit(1)
	}
	if *fpsArg < 1 || *fpsArg > 120 {
		fmt.Fprintf(os.Stderr, "error: --fps must be between 1 and 120\n")
		os.Exit(1)
	}
	if *qpArg < 0 || *qpArg > 51 {
		fmt.Fprintf(os.Stderr, "error: --qp must be between 0 and 51\n")
		os.Exit(1)
	}
	if *segDuration < 1 || *segDuration > 30 {
		fmt.Fprintf(os.Stderr, "error: --segment-duration must be between 1 and 30\n")
		os.Exit(1)
	}
	if *segCount < 2 || *segCount > 30 {
		fmt.Fprintf(os.Stderr, "error: --segment-count must be between 2 and 30\n")
		os.Exit(1)
	}

	// Validate access control flags
	access := *accessArg
	if access == "" {
		access = "public"
	}
	if access != "public" && access != "token" {
		fmt.Fprintf(os.Stderr, "error: --access must be \"public\" or \"token\"\n")
		os.Exit(1)
	}
	serverToken := *tokenArg
	if access == "token" && serverToken == "" {
		fmt.Fprintf(os.Stderr, "error: --access token requires --token\n")
		os.Exit(1)
	}
	if serverToken != "" && access != "token" {
		access = "token" // --token implies --access token
	}
	if serverToken != "" {
		if err := validateToken(serverToken); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
	}
	serverIsPrivate := access == "token"

	// Parse timezone for clock display
	loc := time.UTC
	if *timezoneArg != "" {
		var err error
		loc, err = time.LoadLocation(*timezoneArg)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: invalid --timezone %q: %v\n", *timezoneArg, err)
			os.Exit(1)
		}
	}

	// Load icon
	iconData, iconCT := loadIcon(*iconArg)
	iconFileName := "icon." + iconExtension(iconCT)

	// Parse metadata options (always created — icon is always present)
	metaOpts := &serverMetadataOpts{
		Description:  *descriptionArg,
		Language:     *languageArg,
		Timezone:     *timezoneArg,
		IconFileName: iconFileName,
	}
	if *tagsArg != "" {
		for _, t := range strings.Split(*tagsArg, ",") {
			t = strings.TrimSpace(t)
			if t != "" {
				metaOpts.Tags = append(metaOpts.Tags, t)
			}
		}
		if len(metaOpts.Tags) > 5 {
			fmt.Fprintf(os.Stderr, "error: --tags accepts at most 5 tags\n")
			os.Exit(1)
		}
	}

	// Align dimensions to multiples of 16 for the H.264 encoder.
	// SPS frame cropping tells the decoder to crop back to the requested size.
	encWidth := (*widthArg + 15) / 16 * 16
	encHeight := (*heightArg + 15) / 16 * 16

	h264 := &h264Settings{
		width:      encWidth,
		height:     encHeight,
		cropRight:  encWidth - *widthArg,
		cropBottom: encHeight - *heightArg,
		fps:        *fpsArg,
		qp:         *qpArg,
	}

	framesPerSeg := h264.fps * *segDuration
	ptsPerFrame := int64(90000 / h264.fps)

	if *numChannels < 1 {
		fatal("--channels must be >= 1")
	}

	// Parse variants
	variants, err := parseVariants(*variantsArg)
	if err != nil {
		fatal("%v", err)
	}

	// Set up signal handling
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sigCh := make(chan os.Signal, 1)
	signalNotify(sigCh)
	go func() {
		<-sigCh
		logInfof("shutting down...")
		cancel()
	}()

	// Pre-encode static NAL units (shared across all channels at same resolution)
	sps := encodeSPS(h264)
	pps := encodePPS(h264)
	aud := encodeAUD()

	hostname := *hostnameArg

	// --- Initialize N channels ---
	channels := make([]*serverChannel, *numChannels)
	for i := 0; i < *numChannels; i++ {
		// Key management: channel 1 uses --key, others auto-generate
		var privKey ed25519.PrivateKey
		var pubKey ed25519.PublicKey
		keyPath := *keyFile
		if i > 0 && keyPath != "" {
			// Derive key path for additional channels
			keyPath = fmt.Sprintf("ch%d.key", i+1)
		}
		if keyPath != "" {
			seed, err := readSeed(keyPath)
			if err != nil {
				if os.IsNotExist(err) {
					logInfof("generating new key: %s", keyPath)
					pub, priv, gerr := ed25519.GenerateKey(rand.Reader)
					if gerr != nil {
						fatal("keygen: %v", gerr)
					}
					if werr := writeSeed(keyPath, priv.Seed()); werr != nil {
						fatal("write key: %v", werr)
					}
					privKey = priv
					pubKey = pub
				} else {
					fatal("read key %s: %v", keyPath, err)
				}
			} else {
				privKey, pubKey = keyFromSeed(seed)
			}
		} else {
			var err error
			pubKey, privKey, err = ed25519.GenerateKey(rand.Reader)
			if err != nil {
				fatal("keygen: %v", err)
			}
			if i == 0 {
				logInfof("using ephemeral keys (use --key to persist)")
			}
		}

		chID := makeChannelID(pubKey)
		chName := channelName
		chDisplayName := displayName
		if *numChannels > 1 {
			chName = fmt.Sprintf("%s %d", channelName, i+1)
			chDisplayName = strings.ToUpper(chName)
		}

		seg := newHLSSegmenter(*segCount, *segDuration)
		metadata, guide := serverSignDocs(chID, chName, hostname, privKey, serverGuideEntries, access, *onDemand, metaOpts)

		ch := &serverChannel{
			channelID:   chID,
			channelName: chName,
			privKey:     privKey,
			seg:         seg,
			state: &serverState{
				seg:          seg,
				muxer:        &tsMuxer{},
				sps:          sps,
				pps:          pps,
				aud:          aud,
				frame:        newFrame(h264.width, h264.height),
				h264:         h264,
				channelName:  chDisplayName,
				showUptime:   *showUptime,
				fontScale:    *fontScale,
				startTime:    time.Now().UTC(),
				location:     loc,
				framesPerSeg: framesPerSeg,
				ptsPerFrame:  ptsPerFrame,
				segDuration:  float64(*segDuration),
				segDurationI: *segDuration,
			},
		}
		// Initialize variants if specified
		if len(variants) > 0 {
			ch.variants = make([]serverVariant, len(variants))
			for vi, v := range variants {
				vw := (v.width + 15) / 16 * 16
				vh := (v.height + 15) / 16 * 16
				vH264 := &h264Settings{
					width:      vw,
					height:     vh,
					cropRight:  vw - v.width,
					cropBottom: vh - v.height,
					fps:        *fpsArg,
					qp:         *qpArg,
				}
				vSps := encodeSPS(vH264)
				vPps := encodePPS(vH264)
				vSeg := newHLSSegmenter(*segCount, *segDuration)
				vSeg.segPrefix = v.label + "_"
				levelIdc := selectLevel(v.width, v.height, *fpsArg)

				ch.variants[vi] = serverVariant{
					label:    v.label,
					width:    v.width,
					height:   v.height,
					seg:      vSeg,
					codecTag: codecTagFromLevel(levelIdc),
					state: &serverState{
						seg:          vSeg,
						muxer:        &tsMuxer{},
						sps:          vSps,
						pps:          vPps,
						aud:          aud,
						frame:        newFrame(vw, vh),
						h264:         vH264,
						channelName:  chDisplayName,
						showUptime:   *showUptime,
						fontScale:    *fontScale,
						startTime:    time.Now().UTC(),
						location:     loc,
						framesPerSeg: framesPerSeg,
						ptsPerFrame:  ptsPerFrame,
						segDuration:  float64(*segDuration),
						segDurationI: *segDuration,
					},
				}
			}
			// Primary seg/state = first variant
			ch.seg = ch.variants[0].seg
			ch.state = ch.variants[0].state
		}

		ch.docs.Store(&serverDocs{
			channelID:   chID,
			channelName: chName,
			metadata:    metadata,
			guide:       guide,
		})
		channels[i] = ch

		logInfof("channel %d: %s (%s)", i+1, chID, chName)
	}

	// For single-channel backward compat: also set global serverDocsState
	if len(channels) == 1 {
		d := channels[0].docs.Load()
		serverDocsState.Store(d)
	}

	logInfof("starting test signal generator (%d channel(s))", *numChannels)
	if h264.cropRight > 0 || h264.cropBottom > 0 {
		logInfof("resolution: %dx%d (encoded %dx%d) @ %dfps, QP=%d",
			*widthArg, *heightArg, h264.width, h264.height, h264.fps, h264.qp)
	} else {
		logInfof("resolution: %dx%d @ %dfps, QP=%d", h264.width, h264.height, h264.fps, h264.qp)
	}
	logInfof("HLS: %ds segments, %d-segment window", *segDuration, *segCount)

	// Set up cache (if enabled)
	var cache *hlsCache
	if *cacheEnabled {
		cache = newHLSCache(*cacheMaxEntries)
		logInfof("cache enabled (max %d entries)", *cacheMaxEntries)
	}

	// Set up peer registry (--peers)
	var peerReg *peerRegistry
	peerTargets, err := parsePeerTargets(*peersStr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	if len(peerTargets) > 0 {
		peerReg = newPeerRegistry()
		client := newClient(flagInsecure)
		go peerPollLoop(ctx, client, peerTargets, peerReg, 5*time.Minute)
		logInfof("peers: verifying %d external channels", len(peerTargets))
	}

	// Set up gossip registry
	var gossipReg *peerRegistry
	if *gossipEnabled && len(peerTargets) > 0 {
		gossipReg = newPeerRegistry()
		gossipNodes := gossipNodesFromPeers(peerTargets)
		client := newClient(flagInsecure)
		go gossipPollLoop(ctx, client, gossipNodes, gossipReg.Update, 10*time.Minute)
		logInfof("gossip: discovering channels from %d nodes", len(gossipNodes))
	}

	// HTTP server
	mux := http.NewServeMux()
	if viewer.enabled {
		// Build channel lookup for viewer
		chanMap := make(map[string]*serverChannel, len(channels))
		for _, ch := range channels {
			chanMap[ch.channelID] = ch
		}
		viewerEmbedRoutes(mux, func(reqChID string) map[string]interface{} {
			// Pick requested channel, default to first
			ch := channels[0]
			if reqChID != "" {
				if c, ok := chanMap[reqChID]; ok {
					ch = c
				}
			}
			docs := ch.docs.Load()
			info := viewerBuildInfo(docs.channelID, docs.channelName, docs.metadata, docs.guide)
			streamSrc := "/tltv/v1/channels/" + docs.channelID + "/stream.m3u8"
			if serverToken != "" {
				streamSrc += "?token=" + serverToken
			}
			info["stream_src"] = streamSrc
			xmltvURL := "/tltv/v1/channels/" + docs.channelID + "/guide.xml"
			if serverToken != "" {
				xmltvURL += "?token=" + serverToken
			}
			info["xmltv_url"] = xmltvURL
			if hostname != "" {
				info["tltv_uri"] = formatTLTVUri(docs.channelID, []string{hostname}, "")
			}
			return info
		}, func() []ChannelRef {
			var refs []ChannelRef
			for _, ch := range channels {
				docs := ch.docs.Load()
				refs = append(refs, ChannelRef{ID: docs.channelID, Name: docs.channelName})
			}
			return refs
		})
	} else {
		statusPageRoutes(mux, func() *NodeInfo {
			ni := &NodeInfo{
				Protocol: "tltv",
				Versions: []int{1},
			}
			if !serverIsPrivate {
				for _, ch := range channels {
					docs := ch.docs.Load()
					ni.Channels = append(ni.Channels, ChannelRef{ID: docs.channelID, Name: docs.channelName})
				}
			}
			return ni
		})
	}

	if len(channels) == 1 && len(channels[0].variants) == 0 {
		// Single-channel without variants: use existing serverHTTP for backward compat with tests
		ch := channels[0]
		d := ch.docs.Load()
		serverHTTP(mux, ch.seg, ch.channelID, ch.channelName, d.metadata, d.guide, cache, peerReg, gossipReg, serverToken, serverIsPrivate, iconData, iconCT)
	} else {
		serverMultiHTTP(mux, channels, cache, peerReg, gossipReg, serverToken, serverIsPrivate, iconData, iconCT)
	}

	// Set up TLS (if enabled).
	tlsCfg, tlsCleanup, tlsErr := tlsSetup(*hostnameArg, *tlsEnabled, *tlsCert, *tlsKey, *acmeEmail, *tlsStaging)
	if tlsErr != nil {
		fmt.Fprintf(os.Stderr, "server: tls: %v\n", tlsErr)
		os.Exit(1)
	}
	defer tlsCleanup()

	scheme := "http"
	if tlsCfg != nil {
		scheme = "https"
	}

	ln, err := net.Listen("tcp", *listenAddr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "server: listen: %v\n", err)
		os.Exit(1)
	}
	addr := displayListenAddr(ln.Addr().String())
	logInfof("listening on %s (%s)", addr, scheme)
	for _, ch := range channels {
		logInfof("stream: %s://%s/tltv/v1/channels/%s/stream.m3u8", scheme, addr, ch.channelID)
	}
	if viewer.enabled {
		logInfof("viewer: %s://%s", scheme, addr)
	}

	srv := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	if tlsCfg != nil {
		srv.TLSConfig = tlsCfg
		go func() {
			if err := srv.ServeTLS(ln, "", ""); err != http.ErrServerClosed {
				logErrorf("https: %v", err)
			}
		}()
	} else {
		go func() {
			if err := srv.Serve(ln); err != http.ErrServerClosed {
				logErrorf("http: %v", err)
			}
		}()
	}

	// Start cache goroutines
	if cache != nil {
		startCacheGoroutines(cache, *cacheStatsInterval, ctx.Done())
	}

	// Generate first segment for all channels (all variants)
	for _, ch := range channels {
		serverGenerateAllVariants(ch)
	}

	ticker := time.NewTicker(time.Duration(*segDuration) * time.Second)
	defer ticker.Stop()

	// Re-sign docs periodically (every 5 minutes)
	resignTicker := time.NewTicker(5 * time.Minute)
	defer resignTicker.Stop()

	// Atomic config for reloadable fields
	var serverLiveConfig atomic.Pointer[serverReloadableConfig]
	serverLiveConfig.Store(&serverReloadableConfig{
		channelName:  channelName,
		guideEntries: serverGuideEntries,
	})

	if *configPath != "" {
		go configReloadLoop(ctx, newConfigWatcher(*configPath), func(cfg map[string]interface{}) {
			serverApplyReloadedConfig(cfg, &serverLiveConfig)
		})
	}

	for {
		select {
		case <-ctx.Done():
			logInfof("generating final segment")
			shutCtx, shutCancel := context.WithTimeout(context.Background(), 3*time.Second)
			srv.Shutdown(shutCtx)
			shutCancel()
			return
		case <-ticker.C:
			for _, ch := range channels {
				serverGenerateAllVariants(ch)
			}
		case <-resignTicker.C:
			lc := serverLiveConfig.Load()
			for i, ch := range channels {
				chName := lc.channelName
				if *numChannels > 1 {
					chName = fmt.Sprintf("%s %d", lc.channelName, i+1)
				}
				ch.state.channelName = strings.ToUpper(chName)
				metadata, guide := serverSignDocs(ch.channelID, chName, hostname, ch.privKey, lc.guideEntries, access, *onDemand, metaOpts)
				ch.docs.Store(&serverDocs{
					channelID:   ch.channelID,
					channelName: chName,
					metadata:    metadata,
					guide:       guide,
				})
				// Keep global state in sync for single-channel backward compat
				if len(channels) == 1 {
					serverDocsState.Store(ch.docs.Load())
				}
			}
		}
	}
}

// serverState holds all persistent state for the test signal generator loop.
// Methods on this struct replace what was a 15-parameter function.
type serverState struct {
	seg   *hlsSegmenter
	muxer *tsMuxer

	sps, pps, aud []byte
	frame         *Frame
	h264          *h264Settings

	channelName  string
	showUptime   bool
	fontScale    int
	startTime    time.Time
	location     *time.Location
	framesPerSeg int
	ptsPerFrame  int64
	segDuration  float64
	segDurationI int // integer seconds for audio frame count

	frameNum      uint64
	audioFrameNum uint64 // running AAC frame counter (continuous across segments)
}

// generateSegment renders frames, encodes them as H.264,
// wraps in MPEG-TS, and pushes the segment to the HLS segmenter.
//
// Frame caching: the test screen only changes once per second (clock tick),
// so we re-encode only when the time string changes. At 30fps this is a 30×
// reduction in encoder work.
func (s *serverState) generateSegment() {
	// Pre-allocate TS packet buffer
	tsData := make([]byte, 0, s.framesPerSeg*tsPreAllocPerFrame)

	// Write PAT + PMT at start of segment
	var patPkt [tsPacketSize]byte
	s.muxer.writePAT(patPkt[:])
	tsData = append(tsData, patPkt[:]...)

	var pmtPkt [tsPacketSize]byte
	s.muxer.writePMT(pmtPkt[:])
	tsData = append(tsData, pmtPkt[:]...)

	// Pre-generate all audio ADTS frames for this segment.
	// Audio frame count is derived from the running sample counter so that
	// PTS is continuous across segments with no gaps.
	segEndPTS := int64(s.frameNum+uint64(s.framesPerSeg)) * s.ptsPerFrame
	audioFrames := generateAudioFrames(s.audioFrameNum, segEndPTS)

	var cachedNAL []byte
	var cachedTimeStr string

	// Interleave video and audio in batches, matching ffmpeg's muxing strategy.
	// ffmpeg writes ~5-6 video frames then ~16 audio frames in alternating batches.
	// This keeps the player's decode buffers fed and produces the PES batching
	// that is critical for gapless audio at segment boundaries.
	//
	// Strategy: write all video frames first, then insert audio PES batches
	// at regular intervals. Each audio PES covers ~16 ADTS frames (~340ms).
	const audioPESBatchSize = 16 // frames per audio PES, matching ffmpeg's DEFAULT_PES_HEADER_FREQ

	// We interleave by writing a batch of video frames, then an audio PES
	// batch, repeating. The video batch size is chosen so that audio PES
	// batches are roughly evenly spaced.
	audioIdx := 0
	videoBatchSize := s.framesPerSeg / ((len(audioFrames) + audioPESBatchSize - 1) / audioPESBatchSize)
	if videoBatchSize < 1 {
		videoBatchSize = 1
	}

	for i := 0; i < s.framesPerSeg; i++ {
		var timeStr string
		if s.showUptime {
			totalSecs := int(s.frameNum) / s.h264.fps
			timeStr = fmt.Sprintf("%02d:%02d:%02d", totalSecs/3600, (totalSecs%3600)/60, totalSecs%60)
		} else {
			frameOffset := time.Duration(float64(s.frameNum) / float64(s.h264.fps) * float64(time.Second))
			displayTime := s.startTime.Add(frameOffset)
			timeStr = displayTime.In(s.location).Format("15:04:05")
		}

		if cachedNAL == nil || timeStr != cachedTimeStr {
			renderTestFrame(s.frame, s.channelName, timeStr, s.fontScale)
			cachedNAL = encodeFrame(s.sps, s.pps, s.aud, s.frame, s.h264, int(s.frameNum), int(s.frameNum))
			cachedTimeStr = timeStr
		}

		videoPTS := (int64(s.frameNum) * s.ptsPerFrame) & ((1 << 33) - 1)
		tsData = s.muxer.writeVideoPackets(tsData, cachedNAL, videoPTS, true)
		s.frameNum++

		// After every videoBatchSize frames, write one audio PES batch
		if (i+1)%videoBatchSize == 0 && audioIdx < len(audioFrames) {
			batchEnd := audioIdx + audioPESBatchSize
			if batchEnd > len(audioFrames) {
				batchEnd = len(audioFrames)
			}
			tsData = s.muxer.writeAudioPES(tsData, audioFrames[audioIdx:batchEnd])
			audioIdx = batchEnd
		}
	}

	// Write any remaining audio frames
	if audioIdx < len(audioFrames) {
		tsData = s.muxer.writeAudioPES(tsData, audioFrames[audioIdx:])
	}

	// Advance persistent audio frame counter
	s.audioFrameNum += uint64(len(audioFrames))

	s.seg.pushSegment(tsData, s.segDuration)

	if s.frameNum%(uint64(s.framesPerSeg)*5) == 0 {
		logDebugf("frame %d, segments: %d, last segment: %d bytes",
			s.frameNum, s.seg.seqNum, len(tsData))
	}
}

// serverSignDocs signs metadata and guide documents for the server channel.
// Pass customGuide to use specific entries; nil falls back to ephemeral midnight-to-midnight.
// serverMetadataOpts holds optional metadata fields for serverSignDocs.
type serverMetadataOpts struct {
	Description  string
	Tags         []string
	Language     string
	Timezone     string
	IconFileName string // icon file name (e.g. "icon.svg"), empty = no icon field
}

// serverGenerateAllVariants generates a segment for the channel's primary
// state and all variant states (if multi-rendition). After the first segment,
// updates measured bandwidth on each variant.
func serverGenerateAllVariants(ch *serverChannel) {
	if len(ch.variants) == 0 {
		ch.state.generateSegment()
		return
	}
	for i := range ch.variants {
		v := &ch.variants[i]
		v.state.generateSegment()
		// Update bandwidth from actual segment sizes after first segment
		if v.bandwidth == 0 && v.seg.seqNum > 0 {
			data := v.seg.getSegment(v.seg.seqNum - 1)
			if data != nil {
				v.bandwidth = len(data) * 8 / v.state.segDurationI
			}
		}
	}
}

func serverSignDocs(channelID, channelName, hostname string, privKey ed25519.PrivateKey, customGuide []guideEntry, access string, onDemand bool, opts *serverMetadataOpts) ([]byte, []byte) {
	now := time.Now().UTC()

	// --- Metadata ---
	doc := map[string]interface{}{
		"v":       json.Number("1"),
		"seq":     json.Number(fmt.Sprintf("%d", now.Unix())),
		"id":      channelID,
		"name":    channelName,
		"stream":  "/tltv/v1/channels/" + channelID + "/stream.m3u8",
		"guide":   "/tltv/v1/channels/" + channelID + "/guide.json",
		"access":  access,
		"status":  "active",
		"updated": now.Format(timestampFormat),
	}
	if opts != nil {
		if opts.Description != "" {
			doc["description"] = opts.Description
		}
		if len(opts.Tags) > 0 {
			tags := opts.Tags
			if len(tags) > 5 {
				tags = tags[:5]
			}
			iface := make([]interface{}, len(tags))
			for i, t := range tags {
				iface[i] = t
			}
			doc["tags"] = iface
		}
		if opts.Language != "" {
			doc["language"] = opts.Language
		}
		if opts.Timezone != "" {
			doc["timezone"] = opts.Timezone
		}
		if opts.IconFileName != "" {
			doc["icon"] = "/tltv/v1/channels/" + channelID + "/" + opts.IconFileName
		}
	}
	if onDemand {
		doc["on_demand"] = true
	}
	if hostname != "" {
		doc["origins"] = []interface{}{hostname}
	}

	signed, err := signDocument(doc, privKey)
	if err != nil {
		logErrorf("metadata signing error: %v", err)
		return nil, nil
	}
	metadata, _ := json.Marshal(signed)

	// --- Guide ---
	// Use custom guide entries from config, or fall back to ephemeral
	// midnight-to-midnight UTC guide (regenerated every 5 minutes).
	guideEntries := customGuide
	if len(guideEntries) == 0 {
		guideEntries = defaultGuideEntries(channelName)
	}

	var entries []interface{}
	for _, e := range guideEntries {
		entry := map[string]interface{}{
			"start": e.Start,
			"end":   e.End,
			"title": e.Title,
		}
		if e.Description != "" {
			entry["description"] = e.Description
		}
		if e.Category != "" {
			entry["category"] = e.Category
		}
		if e.RelayFrom != "" {
			entry["relay_from"] = e.RelayFrom
		}
		entries = append(entries, entry)
	}

	guideDoc := map[string]interface{}{
		"v":       json.Number("1"),
		"seq":     json.Number(fmt.Sprintf("%d", now.Unix())),
		"id":      channelID,
		"from":    guideEntries[0].Start,
		"until":   guideEntries[len(guideEntries)-1].End,
		"entries": entries,
		"updated": now.Format(timestampFormat),
	}

	signedGuide, err := signDocument(guideDoc, privKey)
	if err != nil {
		logErrorf("guide signing error: %v", err)
		return metadata, nil
	}
	guide, _ := json.Marshal(signedGuide)

	return metadata, guide
}

// serverDocs holds the signed documents, swapped atomically.
type serverDocs struct {
	channelID   string
	channelName string
	metadata    []byte
	guide       []byte
}

// serverChannel bundles all per-channel state for multi-channel server.
type serverChannel struct {
	channelID   string
	channelName string
	privKey     ed25519.PrivateKey
	seg         *hlsSegmenter    // primary segmenter (single-variant or first variant)
	state       *serverState     // primary state
	variants    []serverVariant  // nil = single media playlist; non-nil = master playlist mode
	docs        atomic.Pointer[serverDocs]
}

// serverVariant holds per-rendition state for multi-rendition mode.
type serverVariant struct {
	label     string         // e.g. "1080p"
	width     int
	height    int
	seg       *hlsSegmenter
	state     *serverState
	bandwidth int            // measured bits/sec (updated after first segment)
	codecTag  string         // e.g. "avc1.42c028"
}

// serverVariantPreset maps shorthand labels to width×height.
var serverVariantPreset = map[string][2]int{
	"4320p": {7680, 4320}, "2160p": {3840, 2160},
	"1440p": {2560, 1440}, "1080p": {1920, 1080},
	"720p":  {1280, 720},  "480p":  {854, 480},
	"360p":  {640, 360},   "240p":  {426, 240},
}

// parseVariants parses a comma-separated variant string into resolution specs.
func parseVariants(s string) ([]serverVariant, error) {
	if s == "" {
		return nil, nil
	}
	var variants []serverVariant
	seen := make(map[string]bool)
	for _, label := range strings.Split(s, ",") {
		label = strings.TrimSpace(strings.ToLower(label))
		if label == "" {
			continue
		}
		if seen[label] {
			continue
		}
		seen[label] = true
		dims, ok := serverVariantPreset[label]
		if !ok {
			return nil, fmt.Errorf("unknown variant %q (use 4320p/2160p/1440p/1080p/720p/480p/360p/240p)", label)
		}
		variants = append(variants, serverVariant{
			label:  label,
			width:  dims[0],
			height: dims[1],
		})
	}
	if len(variants) == 0 {
		return nil, fmt.Errorf("no valid variants specified")
	}
	return variants, nil
}

// codecTagFromLevel returns the H.264 codec tag for a Baseline profile level.
func codecTagFromLevel(levelIdc int) string {
	return fmt.Sprintf("avc1.42c0%02x", levelIdc)
}

// masterPlaylist generates a master playlist string for the given variants.
func masterPlaylist(variants []serverVariant) string {
	var sb strings.Builder
	sb.WriteString("#EXTM3U\n")
	for _, v := range variants {
		sb.WriteString(fmt.Sprintf("#EXT-X-STREAM-INF:BANDWIDTH=%d,RESOLUTION=%dx%d,CODECS=\"%s,mp4a.40.2\"\n",
			v.bandwidth, v.width, v.height, v.codecTag))
		sb.WriteString("stream_" + v.label + ".m3u8\n")
	}
	return sb.String()
}

// serverDocsState is shared between the main goroutine (writer) and HTTP handlers
// (readers). Uses atomic.Pointer to avoid data races on document re-signing.
// Used by single-channel mode; multi-channel uses serverChannel.docs per channel.
var serverDocsState atomic.Pointer[serverDocs]

// serverUpdateDocs atomically swaps the signed documents read by HTTP handlers.
func serverUpdateDocs(channelID, channelName string, metadata, guide []byte) {
	serverDocsState.Store(&serverDocs{
		channelID:   channelID,
		channelName: channelName,
		metadata:    metadata,
		guide:       guide,
	})
}

// ---------- Config Reload ----------

// serverReloadableConfig holds server fields that can be changed via config hot-reload.
// Written by configReloadLoop, read by the resign ticker.
type serverReloadableConfig struct {
	channelName  string
	guideEntries []guideEntry
}

// serverApplyReloadedConfig applies reloaded config values to the atomic config pointer.
func serverApplyReloadedConfig(cfg map[string]interface{}, liveConfig *atomic.Pointer[serverReloadableConfig]) {
	current := liveConfig.Load()
	newName := current.channelName
	newEntries := current.guideEntries
	changed := false

	if name, ok := configGetString(cfg, "name"); ok && name != current.channelName {
		newName = name
		logInfof("config: name changed to %q", name)
		changed = true
	}
	if guideVal, ok := cfg["guide"]; ok {
		entries, _, gerr := parseGuideConfig(guideVal)
		if gerr == nil && len(entries) > 0 {
			newEntries = entries
			logInfof("config: guide updated (%d entries)", len(entries))
			changed = true
		} else if gerr != nil {
			logErrorf("config: guide: %v", gerr)
		}
	}

	if changed {
		liveConfig.Store(&serverReloadableConfig{
			channelName:  newName,
			guideEntries: newEntries,
		})
		logInfof("config reloaded")
	}
}
