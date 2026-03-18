package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"
	"time"
)

var version = "dev"

// Global flags
var (
	flagJSON     bool
	flagNoColor  bool
	flagInsecure bool
	flagLocal    bool
)

func usage() {
	w := os.Stderr
	fmt.Fprintf(w, "tltv - TLTV Federation Protocol CLI (v%s, protocol v1)\n\n", version)
	fmt.Fprintf(w, "Usage: tltv [flags] <command> [options]\n\n")
	fmt.Fprintf(w, "Global Flags:\n")
	fmt.Fprintf(w, "  --json        Machine-readable JSON output\n")
	fmt.Fprintf(w, "  --no-color    Disable colored output\n")
	fmt.Fprintf(w, "  --insecure    Skip TLS verification\n")
	fmt.Fprintf(w, "  --local       Allow local/private address hints\n\n")
	fmt.Fprintf(w, "Identity & Keys:\n")
	fmt.Fprintf(w, "  keygen                 Generate a new channel keypair\n")
	fmt.Fprintf(w, "  vanity <pattern>       Mine vanity channel IDs\n")
	fmt.Fprintf(w, "  inspect <channel-id>   Inspect a channel ID\n\n")
	fmt.Fprintf(w, "Documents:\n")
	fmt.Fprintf(w, "  sign                   Sign a JSON document\n")
	fmt.Fprintf(w, "  verify [file]          Verify a signed document\n")
	fmt.Fprintf(w, "  template <type>        Output a document template\n\n")
	fmt.Fprintf(w, "URIs:\n")
	fmt.Fprintf(w, "  parse <uri>            Parse a tltv:// URI\n")
	fmt.Fprintf(w, "  format <channel-id>    Build a tltv:// URI\n\n")
	fmt.Fprintf(w, "Network:\n")
	fmt.Fprintf(w, "  resolve <uri>          Resolve a tltv:// URI end-to-end\n")
	fmt.Fprintf(w, "  node <host>            Probe a TLTV node\n")
	fmt.Fprintf(w, "  fetch <id@host>        Fetch channel metadata\n")
	fmt.Fprintf(w, "  guide <id@host>        Fetch channel guide\n")
	fmt.Fprintf(w, "  peers <host>           List peers from a node\n")
	fmt.Fprintf(w, "  stream <id@host>       Check stream availability\n")
	fmt.Fprintf(w, "  crawl <host>           Crawl the gossip network\n\n")
	fmt.Fprintf(w, "Operations:\n")
	fmt.Fprintf(w, "  migrate                Create a migration document\n")
	fmt.Fprintf(w, "  completion <shell>     Generate shell completions (bash, zsh, fish)\n")
	fmt.Fprintf(w, "  version                Show version\n\n")
	fmt.Fprintf(w, "Use \"tltv <command> -h\" for help with a specific command.\n")
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(1)
	}

	// Extract global flags and find the subcommand
	var globalArgs []string
	var cmd string
	var cmdArgs []string

	for i := 1; i < len(os.Args); i++ {
		arg := os.Args[i]
		switch arg {
		case "--json":
			flagJSON = true
		case "--no-color":
			flagNoColor = true
		case "--insecure":
			flagInsecure = true
		case "--local":
			flagLocal = true
		case "-v", "--version":
			initColor()
			cmdVersion()
			os.Exit(0)
		case "-h", "--help", "help":
			if cmd == "" {
				usage()
				os.Exit(0)
			}
			// Pass -h to the subcommand
			cmdArgs = append(cmdArgs, arg)
		default:
			if cmd == "" && !strings.HasPrefix(arg, "-") {
				cmd = arg
				cmdArgs = os.Args[i+1:]
				goto dispatch
			}
			if strings.HasPrefix(arg, "-") {
				fmt.Fprintf(os.Stderr, "unknown flag: %s\n\n", arg)
				usage()
				os.Exit(1)
			}
			globalArgs = append(globalArgs, arg)
		}
	}

dispatch:
	_ = globalArgs
	initColor()

	switch cmd {
	case "keygen":
		cmdKeygen(cmdArgs)
	case "vanity":
		cmdVanity(cmdArgs)
	case "inspect":
		cmdInspect(cmdArgs)
	case "sign":
		cmdSign(cmdArgs)
	case "verify":
		cmdVerify(cmdArgs)
	case "template":
		cmdTemplate(cmdArgs)
	case "parse":
		cmdParse(cmdArgs)
	case "format":
		cmdFormat(cmdArgs)
	case "resolve":
		cmdResolve(cmdArgs)
	case "node":
		cmdNode(cmdArgs)
	case "fetch":
		cmdFetch(cmdArgs)
	case "guide":
		cmdGuide(cmdArgs)
	case "peers":
		cmdPeers(cmdArgs)
	case "stream":
		cmdStream(cmdArgs)
	case "crawl":
		cmdCrawl(cmdArgs)
	case "migrate":
		cmdMigrate(cmdArgs)
	case "completion":
		cmdCompletion(cmdArgs)
	case "version":
		cmdVersion()
	case "":
		usage()
		os.Exit(1)
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n\n", cmd)
		usage()
		os.Exit(1)
	}
}

// ---------- keygen ----------

func cmdKeygen(args []string) {
	fs := flag.NewFlagSet("keygen", flag.ExitOnError)
	outFile := fs.String("out", "", "output file for seed (- for stdout, default: <channel-id>.key)")
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Generate a new TLTV channel keypair\n\n")
		fmt.Fprintf(os.Stderr, "Usage: tltv keygen [flags]\n\n")
		fmt.Fprintf(os.Stderr, "Flags:\n")
		fs.PrintDefaults()
	}
	fs.Parse(args)

	// Generate keypair
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		fatal("key generation failed: %v", err)
	}

	channelID := makeChannelID(pub)
	seed := priv.Seed()

	// Determine output filename
	filename := *outFile
	if filename == "" {
		filename = channelID + ".key"
	}

	// Support --out - for writing seed to stdout
	if filename == "-" {
		os.Stdout.Write(seed)
		fmt.Fprintf(os.Stderr, "%s\n", channelID)
		return
	}

	// Check if file exists
	if _, err := os.Stat(filename); err == nil {
		fatal("file already exists: %s (use -out to specify a different path)", filename)
	}

	// Write seed file
	if err := os.WriteFile(filename, seed, 0600); err != nil {
		fatal("could not write key file: %v", err)
	}

	if flagJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(map[string]string{
			"channel_id": channelID,
			"public_key": hex.EncodeToString(pub),
			"seed_file":  filename,
		})
		return
	}

	printHeader("New Channel Identity")
	printField("Channel ID", channelID)
	printField("Public Key", hex.EncodeToString(pub))
	printField("Seed File", filename)
	printField("URI", "tltv://"+channelID)
	fmt.Println()

	if isTestChannel(channelID) {
		printWarn("This is the RFC 8032 test channel. Do NOT use in production.")
		fmt.Println()
	}

	fmt.Printf("  Keep %s safe. This is the only copy of your private key.\n", filename)
	fmt.Printf("  Anyone with this file can sign as your channel.\n\n")
}

// ---------- vanity ----------

func cmdVanity(args []string) {
	fs := flag.NewFlagSet("vanity", flag.ExitOnError)
	threads := fs.Int("threads", runtime.NumCPU(), "number of mining threads")
	mode := fs.String("mode", "prefix", "match mode: prefix (after TV), contains, suffix")
	ignoreCase := fs.Bool("i", false, "case-insensitive matching")
	count := fs.Int("count", 0, "stop after N matches (0 = unlimited)")
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Mine vanity channel IDs matching a pattern\n\n")
		fmt.Fprintf(os.Stderr, "Usage: tltv vanity [flags] <pattern>\n\n")
		fmt.Fprintf(os.Stderr, "Channel IDs always start with \"TV\". By default, the pattern\n")
		fmt.Fprintf(os.Stderr, "is matched against what follows TV (prefix mode).\n\n")
		fmt.Fprintf(os.Stderr, "Examples:\n")
		fmt.Fprintf(os.Stderr, "  tltv vanity cool              Find TVcool...\n")
		fmt.Fprintf(os.Stderr, "  tltv vanity --mode contains X  Find ...X... anywhere\n")
		fmt.Fprintf(os.Stderr, "  tltv vanity -i MOON            Case-insensitive\n")
		fmt.Fprintf(os.Stderr, "  tltv vanity -count 1 test      Stop after first match\n\n")
		fmt.Fprintf(os.Stderr, "Flags:\n")
		fs.PrintDefaults()
	}
	fs.Parse(args)

	if fs.NArg() < 1 {
		fs.Usage()
		os.Exit(1)
	}

	pattern := fs.Arg(0)
	runVanityMiner(pattern, *mode, *ignoreCase, *threads, *count)
}

// ---------- inspect ----------

func cmdInspect(args []string) {
	fs := flag.NewFlagSet("inspect", flag.ExitOnError)
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Inspect and validate a TLTV channel ID\n\n")
		fmt.Fprintf(os.Stderr, "Usage: tltv inspect <channel-id>\n\n")
	}
	fs.Parse(args)

	if fs.NArg() < 1 {
		fs.Usage()
		os.Exit(1)
	}

	id := fs.Arg(0)
	pubKey, err := parseChannelID(id)
	if err != nil {
		fatal("invalid channel ID: %v", err)
	}

	if flagJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(map[string]interface{}{
			"channel_id":   id,
			"public_key":   hex.EncodeToString(pubKey),
			"valid":        true,
			"test_channel": isTestChannel(id),
			"uri":          "tltv://" + id,
		})
		return
	}

	printHeader("Channel ID")
	printField("ID", id)
	printField("Public Key", hex.EncodeToString(pubKey))
	printField("URI", "tltv://"+id)
	printOK("Valid channel ID")

	if isTestChannel(id) {
		printWarn("This is the well-known RFC 8032 test channel")
		printWarn("Anyone can sign as this channel. Do NOT use in production.")
	}
	fmt.Println()
}

// ---------- sign ----------

func cmdSign(args []string) {
	fs := flag.NewFlagSet("sign", flag.ExitOnError)
	keyFile := fs.String("key", "", "path to seed file (required)")
	inFile := fs.String("in", "", "input JSON file (default: stdin)")
	autoSeq := fs.Bool("auto-seq", false, "set seq to current time and updated to now")
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Sign a TLTV JSON document\n\n")
		fmt.Fprintf(os.Stderr, "Usage: tltv sign -key <seed-file> [flags]\n\n")
		fmt.Fprintf(os.Stderr, "Reads a JSON document from stdin (or -in file), signs it,\n")
		fmt.Fprintf(os.Stderr, "and outputs the signed document to stdout.\n\n")
		fmt.Fprintf(os.Stderr, "Examples:\n")
		fmt.Fprintf(os.Stderr, "  tltv sign -key channel.key < metadata.json\n")
		fmt.Fprintf(os.Stderr, "  tltv sign -key channel.key -in doc.json -auto-seq\n\n")
		fmt.Fprintf(os.Stderr, "Flags:\n")
		fs.PrintDefaults()
	}
	fs.Parse(args)

	if *keyFile == "" {
		fatal("missing required -key flag")
	}

	// Read seed
	seed, err := os.ReadFile(*keyFile)
	if err != nil {
		fatal("could not read key file: %v", err)
	}
	if len(seed) != ed25519.SeedSize {
		fatal("invalid seed file: expected %d bytes, got %d", ed25519.SeedSize, len(seed))
	}

	priv, pub := keyFromSeed(seed)
	channelID := makeChannelID(pub)

	// Read document
	var reader io.Reader
	if *inFile != "" {
		f, err := os.Open(*inFile)
		if err != nil {
			fatal("could not open input file: %v", err)
		}
		defer f.Close()
		reader = f
	} else {
		// Check if stdin is a terminal
		fi, _ := os.Stdin.Stat()
		if fi.Mode()&os.ModeCharDevice != 0 {
			fmt.Fprintln(os.Stderr, "Reading document from stdin (paste JSON, then Ctrl-D)...")
		}
		reader = os.Stdin
	}

	doc, err := readDocument(reader)
	if err != nil {
		fatal("could not read document: %v", err)
	}

	// Auto-set fields if requested
	if *autoSeq {
		now := time.Now().UTC()
		doc["seq"] = json.Number(fmt.Sprintf("%d", now.Unix()))
		doc["updated"] = now.Format("2006-01-02T15:04:05Z")
	}

	// Validate timestamp formats before signing (spec section 6.4)
	if err := validateDocTimestamps(doc); err != nil {
		fatal("timestamp validation: %v", err)
	}

	// Ensure id field matches the signing key
	if docID, ok := doc["id"]; ok {
		if docIDStr, ok := docID.(string); ok && docIDStr != channelID {
			// Check if this is a migration (uses "from" instead of "id")
			if docType, _ := doc["type"].(string); docType != "migration" {
				fmt.Fprintf(os.Stderr, "warning: document id %q does not match signing key %q\n", docIDStr, channelID)
			}
		}
	}

	// Sign
	signed, err := signDocument(doc, priv)
	if err != nil {
		fatal("signing failed: %v", err)
	}

	// Output
	out, err := documentToJSON(signed)
	if err != nil {
		fatal("JSON encoding failed: %v", err)
	}
	fmt.Println(string(out))
}

// ---------- verify ----------

func cmdVerify(args []string) {
	fs := flag.NewFlagSet("verify", flag.ExitOnError)
	channelFlag := fs.String("channel", "", "expected channel ID (auto-detected from document if omitted)")
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Verify a signed TLTV document\n\n")
		fmt.Fprintf(os.Stderr, "Usage: tltv verify [flags] [file]\n\n")
		fmt.Fprintf(os.Stderr, "Reads from stdin if no file is specified.\n\n")
		fmt.Fprintf(os.Stderr, "Flags:\n")
		fs.PrintDefaults()
	}
	fs.Parse(args)

	// Read document
	var reader io.Reader
	if fs.NArg() > 0 {
		f, err := os.Open(fs.Arg(0))
		if err != nil {
			fatal("could not open file: %v", err)
		}
		defer f.Close()
		reader = f
	} else {
		fi, _ := os.Stdin.Stat()
		if fi.Mode()&os.ModeCharDevice != 0 {
			fmt.Fprintln(os.Stderr, "Reading document from stdin (paste JSON, then Ctrl-D)...")
		}
		reader = os.Stdin
	}

	doc, err := readDocument(reader)
	if err != nil {
		fatal("could not read document: %v", err)
	}

	// Determine verification type
	docType, _ := doc["type"].(string)

	var verifyErr error
	var channelID string

	if docType == "migration" {
		channelID = *channelFlag
		if channelID == "" {
			channelID, _ = doc["from"].(string)
		}
		verifyErr = verifyMigration(doc, channelID)
	} else {
		channelID = *channelFlag
		if channelID == "" {
			channelID, _ = doc["id"].(string)
		}
		verifyErr = verifyDocument(doc, channelID)
	}

	if flagJSON {
		result := map[string]interface{}{
			"valid":      verifyErr == nil,
			"channel_id": channelID,
			"type":       docType,
		}
		if verifyErr != nil {
			result["error"] = verifyErr.Error()
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(result)
		if verifyErr != nil {
			os.Exit(1)
		}
		return
	}

	if verifyErr != nil {
		printFail("Signature verification failed: " + verifyErr.Error())
		os.Exit(1)
	}

	printOK("Signature is valid")
	printField("Channel ID", channelID)
	if docType != "" {
		printField("Type", docType)
	}
	if name := getString(doc, "name"); name != "" {
		printField("Name", name)
	}
	if isTestChannel(channelID) {
		printWarn("This is the RFC 8032 test channel.")
	}
	fmt.Println()
}

// ---------- template ----------

func cmdTemplate(args []string) {
	fs := flag.NewFlagSet("template", flag.ExitOnError)
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Output a JSON document template\n\n")
		fmt.Fprintf(os.Stderr, "Usage: tltv template <type>\n\n")
		fmt.Fprintf(os.Stderr, "Types:\n")
		fmt.Fprintf(os.Stderr, "  metadata     Channel metadata document\n")
		fmt.Fprintf(os.Stderr, "  guide        Channel guide document\n")
		fmt.Fprintf(os.Stderr, "  migration    Key migration document\n\n")
	}
	fs.Parse(args)

	if fs.NArg() < 1 {
		fs.Usage()
		os.Exit(1)
	}

	t := time.Now().UTC()
	now := t.Format("2006-01-02T15:04:05Z")
	seq := t.Unix()

	var doc map[string]interface{}

	switch fs.Arg(0) {
	case "metadata":
		doc = map[string]interface{}{
			"v":       1,
			"seq":     seq,
			"id":      "<YOUR_CHANNEL_ID>",
			"name":    "My Channel",
			"stream":  "/tltv/v1/channels/<YOUR_CHANNEL_ID>/stream.m3u8",
			"access":  "public",
			"updated": now,
		}

	case "guide":
		tomorrow := time.Now().Add(24 * time.Hour).UTC().Format("2006-01-02T15:04:05Z")
		doc = map[string]interface{}{
			"v":     1,
			"seq":   seq,
			"id":    "<YOUR_CHANNEL_ID>",
			"from":  now,
			"until": tomorrow,
			"entries": []interface{}{
				map[string]interface{}{
					"start": now,
					"end":   tomorrow,
					"title": "Programming Block",
				},
			},
			"updated": now,
		}

	case "migration":
		doc = map[string]interface{}{
			"v":        1,
			"seq":      seq,
			"type":     "migration",
			"from":     "<OLD_CHANNEL_ID>",
			"to":       "<NEW_CHANNEL_ID>",
			"reason":   "key rotation",
			"migrated": now,
		}

	default:
		fatal("unknown template type: %s (valid: metadata, guide, migration)", fs.Arg(0))
	}

	out, _ := documentToJSON(doc)
	fmt.Println(string(out))
}

// ---------- parse (URI) ----------

func cmdParse(args []string) {
	fs := flag.NewFlagSet("parse", flag.ExitOnError)
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Parse a tltv:// URI\n\n")
		fmt.Fprintf(os.Stderr, "Usage: tltv parse <uri>\n\n")
		fmt.Fprintf(os.Stderr, "Examples:\n")
		fmt.Fprintf(os.Stderr, "  tltv parse \"tltv://TVMkVH...@example.com:443\"\n")
		fmt.Fprintf(os.Stderr, "  tltv parse \"tltv://TVMkVH...?via=relay.example.com:443\"\n\n")
	}
	fs.Parse(args)

	if fs.NArg() < 1 {
		fs.Usage()
		os.Exit(1)
	}

	uri, err := parseTLTVUri(fs.Arg(0))
	if err != nil {
		fatal("invalid URI: %v", err)
	}

	if flagJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		result := map[string]interface{}{
			"channel_id": uri.ChannelID,
			"hints":      uri.Hints,
		}
		if uri.Token != "" {
			result["token"] = uri.Token
		}
		enc.Encode(result)
		return
	}

	printHeader("URI")
	printField("Channel ID", uri.ChannelID)
	if len(uri.Hints) > 0 {
		for i, h := range uri.Hints {
			label := "Hint"
			if len(uri.Hints) > 1 {
				label = fmt.Sprintf("Hint %d", i+1)
			}
			printField(label, h)
		}
	} else {
		printField("Hints", "(none)")
	}
	if uri.Token != "" {
		printField("Token", uri.Token)
	} else {
		printField("Access", "public")
	}

	// Validate channel ID
	if err := validateChannelID(uri.ChannelID); err != nil {
		printFail("Channel ID: " + err.Error())
	} else {
		printOK("Valid channel ID")
	}
	fmt.Println()
}

// ---------- format (URI) ----------

func cmdFormat(args []string) {
	fs := flag.NewFlagSet("format", flag.ExitOnError)
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Build a tltv:// URI\n\n")
		fmt.Fprintf(os.Stderr, "Usage: tltv format <channel-id> [--hint host:port]... [--token value]\n\n")
		fmt.Fprintf(os.Stderr, "Examples:\n")
		fmt.Fprintf(os.Stderr, "  tltv format TVMkVH... --hint example.com:443\n")
		fmt.Fprintf(os.Stderr, "  tltv format TVMkVH... --token secret123\n\n")
	}

	// Manually extract --hint and --token (Go's flag package doesn't support
	// repeated flags and stops parsing at the first positional argument).
	var hints []string
	var token string
	var remaining []string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--hint", "-hint":
			if i+1 < len(args) {
				hints = append(hints, args[i+1])
				i++
			}
		case "--token", "-token":
			if i+1 < len(args) {
				token = args[i+1]
				i++
			}
		case "-h", "--help":
			fs.Usage()
			os.Exit(0)
		default:
			remaining = append(remaining, args[i])
		}
	}

	if len(remaining) < 1 {
		fs.Usage()
		os.Exit(1)
	}

	channelID := remaining[0]
	if err := validateChannelID(channelID); err != nil {
		fatal("invalid channel ID: %v", err)
	}

	uri := formatTLTVUri(channelID, hints, token)
	fmt.Println(uri)
}

// ---------- migrate ----------

func cmdMigrate(args []string) {
	fs := flag.NewFlagSet("migrate", flag.ExitOnError)
	fromKey := fs.String("from-key", "", "path to OLD channel's seed file (required)")
	toID := fs.String("to", "", "NEW channel ID (required)")
	reason := fs.String("reason", "", "migration reason")
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Create a signed key migration document\n\n")
		fmt.Fprintf(os.Stderr, "Usage: tltv migrate -from-key <old-seed-file> -to <new-channel-id> [-reason text]\n\n")
		fmt.Fprintf(os.Stderr, "The migration document is signed by the OLD key and served\n")
		fmt.Fprintf(os.Stderr, "at the old channel's metadata endpoint.\n\n")
		fmt.Fprintf(os.Stderr, "Flags:\n")
		fs.PrintDefaults()
	}
	fs.Parse(args)

	if *fromKey == "" || *toID == "" {
		fs.Usage()
		os.Exit(1)
	}

	// Read old key
	seed, err := os.ReadFile(*fromKey)
	if err != nil {
		fatal("could not read key file: %v", err)
	}
	if len(seed) != ed25519.SeedSize {
		fatal("invalid seed file: expected %d bytes, got %d", ed25519.SeedSize, len(seed))
	}

	priv, pub := keyFromSeed(seed)
	oldChannelID := makeChannelID(pub)

	// Validate new channel ID
	if err := validateChannelID(*toID); err != nil {
		fatal("invalid new channel ID: %v", err)
	}

	if oldChannelID == *toID {
		fatal("old and new channel IDs are the same")
	}

	// Build migration document
	t := time.Now().UTC()
	doc := map[string]interface{}{
		"v":        json.Number("1"),
		"seq":      json.Number(fmt.Sprintf("%d", t.Unix())),
		"type":     "migration",
		"from":     oldChannelID,
		"to":       *toID,
		"migrated": t.Format("2006-01-02T15:04:05Z"),
	}
	if *reason != "" {
		doc["reason"] = *reason
	}

	signed, err := signDocument(doc, priv)
	if err != nil {
		fatal("signing failed: %v", err)
	}

	out, _ := documentToJSON(signed)
	fmt.Println(string(out))

	if !flagJSON {
		fmt.Fprintln(os.Stderr)
		fmt.Fprintf(os.Stderr, "Migration document created. Serve this at:\n")
		fmt.Fprintf(os.Stderr, "  GET /tltv/v1/channels/%s\n", oldChannelID)
		fmt.Fprintf(os.Stderr, "instead of the old channel metadata.\n")
	}
}

// ---------- version ----------

func cmdVersion() {
	if flagJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(map[string]string{
			"version":          version,
			"protocol_version": "1",
			"go_version":       runtime.Version(),
			"os":               runtime.GOOS,
			"arch":             runtime.GOARCH,
		})
		return
	}
	fmt.Printf("tltv %s (protocol v1, %s, %s/%s)\n", version, runtime.Version(), runtime.GOOS, runtime.GOARCH)
}

// ---------- completion ----------

var allCommands = []string{
	"keygen", "vanity", "inspect",
	"sign", "verify", "template",
	"parse", "format",
	"resolve", "node", "fetch", "guide", "peers", "stream", "crawl",
	"migrate", "completion", "version",
}

func cmdCompletion(args []string) {
	fs := flag.NewFlagSet("completion", flag.ExitOnError)
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Generate shell completion scripts\n\n")
		fmt.Fprintf(os.Stderr, "Usage: tltv completion <shell>\n\n")
		fmt.Fprintf(os.Stderr, "Shells: bash, zsh, fish\n\n")
		fmt.Fprintf(os.Stderr, "Install:\n")
		fmt.Fprintf(os.Stderr, "  bash:  tltv completion bash > /etc/bash_completion.d/tltv\n")
		fmt.Fprintf(os.Stderr, "  zsh:   tltv completion zsh > \"${fpath[1]}/_tltv\"\n")
		fmt.Fprintf(os.Stderr, "  fish:  tltv completion fish > ~/.config/fish/completions/tltv.fish\n\n")
	}
	fs.Parse(args)

	if fs.NArg() < 1 {
		fs.Usage()
		os.Exit(1)
	}

	switch fs.Arg(0) {
	case "bash":
		fmt.Print(completionBash())
	case "zsh":
		fmt.Print(completionZsh())
	case "fish":
		fmt.Print(completionFish())
	default:
		fatal("unknown shell: %s (supported: bash, zsh, fish)", fs.Arg(0))
	}
}

func completionBash() string {
	cmds := strings.Join(allCommands, " ")
	return `# tltv bash completion
_tltv() {
    local cur prev commands
    COMPREPLY=()
    cur="${COMP_WORDS[COMP_CWORD]}"
    prev="${COMP_WORDS[COMP_CWORD-1]}"
    commands="` + cmds + `"

    if [[ ${COMP_CWORD} -eq 1 ]]; then
        COMPREPLY=( $(compgen -W "${commands}" -- "${cur}") )
        return 0
    fi

    case "${prev}" in
        template)
            COMPREPLY=( $(compgen -W "metadata guide migration" -- "${cur}") )
            ;;
        completion)
            COMPREPLY=( $(compgen -W "bash zsh fish" -- "${cur}") )
            ;;
        -key|-in|-out|-from-key)
            COMPREPLY=( $(compgen -f -- "${cur}") )
            ;;
        verify)
            COMPREPLY=( $(compgen -f -- "${cur}") )
            ;;
    esac
}
complete -F _tltv tltv
`
}

func completionZsh() string {
	cmds := strings.Join(allCommands, " ")
	return `#compdef tltv
# tltv zsh completion

_tltv() {
    local -a commands
    commands=(` + cmds + `)

    _arguments -C \
        '--json[JSON output]' \
        '--no-color[Disable colors]' \
        '--insecure[Skip TLS verification]' \
        '--local[Allow local/private address hints]' \
        '1:command:->cmd' \
        '*::arg:->args'

    case $state in
        cmd)
            _describe 'command' commands
            ;;
        args)
            case $words[1] in
                template)
                    _values 'type' metadata guide migration
                    ;;
                completion)
                    _values 'shell' bash zsh fish
                    ;;
                verify)
                    _files
                    ;;
            esac
            ;;
    esac
}

_tltv "$@"
`
}

func completionFish() string {
	var sb strings.Builder
	sb.WriteString("# tltv fish completion\n")
	sb.WriteString("set -l commands ")
	sb.WriteString(strings.Join(allCommands, " "))
	sb.WriteString("\n\n")
	sb.WriteString("complete -c tltv -e\n")
	sb.WriteString("complete -c tltv -n \"not __fish_seen_subcommand_from $commands\" -l json -d 'JSON output'\n")
	sb.WriteString("complete -c tltv -n \"not __fish_seen_subcommand_from $commands\" -l no-color -d 'Disable colors'\n")
	sb.WriteString("complete -c tltv -n \"not __fish_seen_subcommand_from $commands\" -l insecure -d 'Skip TLS'\n")
	sb.WriteString("complete -c tltv -n \"not __fish_seen_subcommand_from $commands\" -l local -d 'Allow local addresses'\n")

	descriptions := map[string]string{
		"keygen": "Generate channel keypair", "vanity": "Mine vanity IDs",
		"inspect": "Inspect channel ID", "sign": "Sign document",
		"verify": "Verify document", "template": "Document template",
		"parse": "Parse tltv:// URI", "format": "Build tltv:// URI",
		"resolve": "Resolve URI end-to-end", "node": "Probe a node",
		"fetch": "Fetch metadata", "guide": "Fetch guide",
		"peers": "List peers", "stream": "Check stream",
		"crawl": "Crawl network", "migrate": "Create migration",
		"completion": "Shell completions", "version": "Show version",
	}
	for _, cmd := range allCommands {
		desc := descriptions[cmd]
		sb.WriteString(fmt.Sprintf("complete -c tltv -n \"not __fish_seen_subcommand_from $commands\" -a %s -d '%s'\n", cmd, desc))
	}
	sb.WriteString("\ncomplete -c tltv -n \"__fish_seen_subcommand_from template\" -a 'metadata guide migration'\n")
	sb.WriteString("complete -c tltv -n \"__fish_seen_subcommand_from completion\" -a 'bash zsh fish'\n")
	return sb.String()
}
