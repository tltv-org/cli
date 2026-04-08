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

// readSeed reads an Ed25519 seed from a file, accepting either:
//   - 64-byte hex-encoded text (new format, with optional trailing newline)
//   - 32-byte raw binary (old format, for backward compatibility)
func readSeed(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	// Try hex first (trimmed -- text files may have trailing newline)
	trimmed := strings.TrimSpace(string(data))
	if len(trimmed) == ed25519.SeedSize*2 {
		seed, err := hex.DecodeString(trimmed)
		if err != nil {
			return nil, fmt.Errorf("invalid hex in key file: %w", err)
		}
		return seed, nil
	}

	// Fall back to raw binary (use original data, not trimmed)
	if len(data) == ed25519.SeedSize {
		return data, nil
	}

	return nil, fmt.Errorf("invalid key file: expected %d hex chars or %d raw bytes, got %d bytes",
		ed25519.SeedSize*2, ed25519.SeedSize, len(data))
}

// writeSeed writes an Ed25519 seed as hex text with a trailing newline.
func writeSeed(path string, seed []byte) error {
	return os.WriteFile(path, []byte(hex.EncodeToString(seed)+"\n"), 0600)
}

// Global flags
var (
	flagJSON     bool
	flagNoColor  bool
	flagInsecure bool
	flagLocal    bool
)

func usage() {
	w := os.Stderr
	fmt.Fprintf(w, "tltv - TLTV Federation Protocol CLI (%s, protocol v1)\n\n", version)
	fmt.Fprintf(w, "Usage: tltv [flags] <command> [options]\n\n")
	fmt.Fprintf(w, "Global Flags:\n")
	fmt.Fprintf(w, "  -j, --json        Machine-readable JSON output\n")
	fmt.Fprintf(w, "  -C, --no-color    Disable colored output\n")
	fmt.Fprintf(w, "  -I, --insecure    Use HTTP transport (and skip TLS verification)\n")
	fmt.Fprintf(w, "  -L, --local       Allow local/private address hints\n\n")
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
	fmt.Fprintf(w, "  fetch <uri|id@host>    Fetch channel metadata\n")
	fmt.Fprintf(w, "  guide <uri|id@host>    Fetch channel guide\n")
	fmt.Fprintf(w, "  peers <host>           List peers from a node\n")
	fmt.Fprintf(w, "  stream <uri|id@host>   Check stream availability\n")
	fmt.Fprintf(w, "  crawl <host>           Crawl the gossip network\n\n")
	fmt.Fprintf(w, "Server:\n")
	fmt.Fprintf(w, "  server test            Start a test signal generator (pure Go video)\n")
	fmt.Fprintf(w, "  bridge                 Start a bridge origin server\n")
	fmt.Fprintf(w, "  relay                  Start a relay node\n")
	fmt.Fprintf(w, "  receiver <target>      Connect to a channel and consume the stream\n")
	fmt.Fprintf(w, "  viewer <target>        Open a local web viewer for a channel\n")
	fmt.Fprintf(w, "  loadtest <target>      Load test with multiple concurrent receivers\n\n")
	fmt.Fprintf(w, "Operations:\n")
	fmt.Fprintf(w, "  migrate                Create a migration document\n")
	fmt.Fprintf(w, "  update                 Update to the latest release\n")
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
		case "-j", "--json":
			flagJSON = true
		case "-C", "--no-color":
			flagNoColor = true
		case "-I", "--insecure":
			flagInsecure = true
		case "-L", "--local":
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
	cmdArgs = hoistGlobalFlags(cmdArgs)
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
	case "server":
		cmdServer(cmdArgs)
	case "bridge":
		cmdBridge(cmdArgs)
	case "relay":
		cmdRelay(cmdArgs)
	case "receiver":
		cmdReceiver(cmdArgs)
	case "viewer":
		cmdViewer(cmdArgs)
	case "loadtest":
		cmdLoadtest(cmdArgs)
	case "migrate":
		cmdMigrate(cmdArgs)
	case "completion":
		cmdCompletion(cmdArgs)
	case "update":
		cmdUpdate(cmdArgs)
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

// hoistGlobalFlags scans subcommand args for known global flags, sets them,
// and returns the remaining args. This allows users to place global flags
// after the subcommand name (e.g. "tltv relay --insecure" instead of
// "tltv --insecure relay").
func hoistGlobalFlags(args []string) []string {
	var remaining []string
	for _, arg := range args {
		switch arg {
		case "-j", "--json":
			flagJSON = true
		case "-C", "--no-color":
			flagNoColor = true
		case "-I", "--insecure":
			flagInsecure = true
		case "-L", "--local":
			flagLocal = true
		default:
			remaining = append(remaining, arg)
		}
	}
	return remaining
}

// ---------- keygen ----------

func cmdKeygen(args []string) {
	fs := flag.NewFlagSet("keygen", flag.ExitOnError)
	outFile := fs.String("output", "", "output file for seed (- for stdout, default: <channel-id>.key)")
	fs.StringVar(outFile, "o", "", "alias for --output")
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Generate a new TLTV channel keypair\n\n")
		fmt.Fprintf(os.Stderr, "Usage: tltv keygen [flags]\n\n")
		fmt.Fprintf(os.Stderr, "Flags:\n")
		fmt.Fprintf(os.Stderr, "  -o, --output string   output file (- for stdout, default: <channel-id>.key)\n")
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

	// Support --output - for writing seed to stdout (hex)
	if filename == "-" {
		fmt.Fprintln(os.Stdout, hex.EncodeToString(seed))
		fmt.Fprintf(os.Stderr, "%s\n", channelID)
		return
	}

	// Check if file exists
	if _, err := os.Stat(filename); err == nil {
		fatal("file already exists: %s (use -o to specify a different path)", filename)
	}

	// Write seed file (hex-encoded)
	if err := writeSeed(filename, seed); err != nil {
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
	fs.IntVar(threads, "t", runtime.NumCPU(), "alias for --threads")
	mode := fs.String("mode", "prefix", "match mode: prefix (after TV), contains, suffix")
	fs.StringVar(mode, "m", "prefix", "alias for --mode")
	ignoreCase := fs.Bool("i", false, "case-insensitive matching")
	count := fs.Int("count", 1, "number of matches to find (0 = unlimited)")
	fs.IntVar(count, "n", 1, "alias for --count")
	outDir := fs.String("output", ".", "directory to save .key files")
	fs.StringVar(outDir, "o", ".", "alias for --output")
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Mine vanity channel IDs matching a pattern\n\n")
		fmt.Fprintf(os.Stderr, "Usage: tltv vanity [flags] <pattern>\n\n")
		fmt.Fprintf(os.Stderr, "All channel IDs start with \"TV\". Do not include the TV prefix\n")
		fmt.Fprintf(os.Stderr, "in your pattern -- it is implied. In prefix mode (default), the\n")
		fmt.Fprintf(os.Stderr, "pattern is matched immediately after TV.\n\n")
		fmt.Fprintf(os.Stderr, "Each match saves a .key file containing the channel's private key.\n\n")
		fmt.Fprintf(os.Stderr, "Examples:\n")
		fmt.Fprintf(os.Stderr, "  tltv vanity cool                Find TVcool...\n")
		fmt.Fprintf(os.Stderr, "  tltv vanity -n 5 cool           Find 5 matches\n")
		fmt.Fprintf(os.Stderr, "  tltv vanity -m contains X       Find ...X... anywhere\n")
		fmt.Fprintf(os.Stderr, "  tltv vanity -i MOON             Case-insensitive\n")
		fmt.Fprintf(os.Stderr, "  tltv vanity -o ~/keys cool      Save keys to ~/keys/\n\n")
		fmt.Fprintf(os.Stderr, "Flags:\n")
		fmt.Fprintf(os.Stderr, "  -t, --threads int   number of mining threads (default %d)\n", runtime.NumCPU())
		fmt.Fprintf(os.Stderr, "  -m, --mode string   match mode: prefix, contains, suffix (default \"prefix\")\n")
		fmt.Fprintf(os.Stderr, "  -i                  case-insensitive matching\n")
		fmt.Fprintf(os.Stderr, "  -n, --count int     number of matches to find, 0 = unlimited (default 1)\n")
		fmt.Fprintf(os.Stderr, "  -o, --output string   directory to save .key files (default \".\")\n")
	}
	fs.Parse(args)

	if fs.NArg() < 1 {
		fs.Usage()
		os.Exit(1)
	}

	pattern := fs.Arg(0)
	runVanityMiner(pattern, *mode, *ignoreCase, *threads, *count, *outDir)
}

// ---------- inspect ----------

func cmdInspect(args []string) {
	fs := flag.NewFlagSet("inspect", flag.ExitOnError)
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Inspect and validate a TLTV channel ID\n\n")
		fmt.Fprintf(os.Stderr, "Usage: tltv inspect <channel-id | tltv:// URI>\n\n")
	}
	fs.Parse(args)

	if fs.NArg() < 1 {
		fs.Usage()
		os.Exit(1)
	}

	id := fs.Arg(0)
	// Accept tltv:// URIs -- extract just the channel ID
	if strings.HasPrefix(id, tltvScheme) {
		uri, err := parseTLTVUri(id)
		if err != nil {
			fatal("invalid URI: %v", err)
		}
		id = uri.ChannelID
	}
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
	fs.StringVar(keyFile, "k", "", "alias for --key")
	inFile := fs.String("input", "", "input JSON file (default: stdin)")
	fs.StringVar(inFile, "i", "", "alias for --input")
	autoSeq := fs.Bool("auto-seq", false, "set seq to current time and updated to now")
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Sign a TLTV JSON document\n\n")
		fmt.Fprintf(os.Stderr, "Usage: tltv sign -k <seed-file> [flags]\n\n")
		fmt.Fprintf(os.Stderr, "Reads a JSON document from stdin (or --input file), signs it,\n")
		fmt.Fprintf(os.Stderr, "and outputs the signed document to stdout.\n\n")
		fmt.Fprintf(os.Stderr, "Examples:\n")
		fmt.Fprintf(os.Stderr, "  tltv sign -k channel.key < metadata.json\n")
		fmt.Fprintf(os.Stderr, "  tltv sign -k channel.key -i doc.json --auto-seq\n\n")
		fmt.Fprintf(os.Stderr, "Flags:\n")
		fmt.Fprintf(os.Stderr, "  -k, --key string      path to seed file (required)\n")
		fmt.Fprintf(os.Stderr, "  -i, --input string    input JSON file (default: stdin)\n")
		fmt.Fprintf(os.Stderr, "      --auto-seq        set seq to current time and updated to now\n")
	}
	fs.Parse(args)

	if *keyFile == "" {
		fatal("missing required -key flag")
	}

	// Read seed
	seed, err := readSeed(*keyFile)
	if err != nil {
		fatal("could not read key file: %v", err)
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
	fs.StringVar(channelFlag, "c", "", "alias for --channel")
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Verify a signed TLTV document\n\n")
		fmt.Fprintf(os.Stderr, "Usage: tltv verify [flags] [file]\n\n")
		fmt.Fprintf(os.Stderr, "Reads from stdin if no file is specified.\n\n")
		fmt.Fprintf(os.Stderr, "Flags:\n")
		fmt.Fprintf(os.Stderr, "  -c, --channel ID    expected channel ID (auto-detected from document if omitted)\n")
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
	fs.StringVar(fromKey, "k", "", "alias for --from-key")
	toID := fs.String("to", "", "NEW channel ID (required)")
	reason := fs.String("reason", "", "migration reason")
	fs.StringVar(reason, "r", "", "alias for --reason")
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Create a signed key migration document\n\n")
		fmt.Fprintf(os.Stderr, "Usage: tltv migrate --from-key <old-seed-file> --to <new-channel-id> [--reason text]\n\n")
		fmt.Fprintf(os.Stderr, "The migration document is signed by the OLD key and served\n")
		fmt.Fprintf(os.Stderr, "at the old channel's metadata endpoint.\n\n")
		fmt.Fprintf(os.Stderr, "Flags:\n")
		fmt.Fprintf(os.Stderr, "  -k, --from-key FILE    path to OLD channel's seed file (required)\n")
		fmt.Fprintf(os.Stderr, "      --to ID            NEW channel ID (required)\n")
		fmt.Fprintf(os.Stderr, "  -r, --reason TEXT      migration reason\n")
	}
	fs.Parse(args)

	if *fromKey == "" || *toID == "" {
		fs.Usage()
		os.Exit(1)
	}

	// Read old key
	seed, err := readSeed(*fromKey)
	if err != nil {
		fatal("could not read key file: %v", err)
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
	"server", "bridge", "relay", "receiver", "viewer", "loadtest",
	"migrate", "update", "completion", "version",
}

func cmdCompletion(args []string) {
	fs := flag.NewFlagSet("completion", flag.ExitOnError)
	install := fs.Bool("install", false, "write completions to the standard shell location")
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Generate shell completion scripts\n\n")
		fmt.Fprintf(os.Stderr, "Usage: tltv completion [--install] <shell>\n\n")
		fmt.Fprintf(os.Stderr, "Shells: bash, zsh, fish\n\n")
		fmt.Fprintf(os.Stderr, "Without --install, prints the script to stdout.\n")
		fmt.Fprintf(os.Stderr, "With --install, writes to the standard location:\n")
		fmt.Fprintf(os.Stderr, "  bash:  /etc/bash_completion.d/tltv\n")
		fmt.Fprintf(os.Stderr, "  zsh:   /usr/local/share/zsh/site-functions/_tltv\n")
		fmt.Fprintf(os.Stderr, "  fish:  ~/.config/fish/completions/tltv.fish\n\n")
		fmt.Fprintf(os.Stderr, "Flags:\n")
		fmt.Fprintf(os.Stderr, "      --install    write completions to the standard shell location\n")
	}
	fs.Parse(args)

	if fs.NArg() < 1 {
		fs.Usage()
		os.Exit(1)
	}

	shell := fs.Arg(0)
	var script string
	var installPath string

	switch shell {
	case "bash":
		script = completionBash()
		installPath = "/etc/bash_completion.d/tltv"
	case "zsh":
		script = completionZsh()
		installPath = "/usr/local/share/zsh/site-functions/_tltv"
	case "fish":
		script = completionFish()
		home, _ := os.UserHomeDir()
		installPath = home + "/.config/fish/completions/tltv.fish"
	default:
		fatal("unknown shell: %s (supported: bash, zsh, fish)", shell)
	}

	if !*install {
		fmt.Print(script)
		return
	}

	// Ensure parent directory exists
	dir := installPath[:strings.LastIndex(installPath, "/")]
	if err := os.MkdirAll(dir, 0755); err != nil {
		fatal("could not create directory %s: %v\n  try: sudo tltv completion --install %s", dir, err, shell)
	}

	if err := os.WriteFile(installPath, []byte(script), 0644); err != nil {
		fatal("could not write %s: %v\n  try: sudo tltv completion --install %s", installPath, err, shell)
	}
	fmt.Fprintf(os.Stderr, "Installed %s completions to %s\n", shell, installPath)
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
        server)
            COMPREPLY=( $(compgen -W "test" -- "${cur}") )
            ;;
        completion)
            COMPREPLY=( $(compgen -W "bash zsh fish" -- "${cur}") )
            ;;
        -key|-k|-input|-i|-output|-o|-from-key)
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
        '(-j --json)'{-j,--json}'[JSON output]' \
        '(-C --no-color)'{-C,--no-color}'[Disable colors]' \
        '(-I --insecure)'{-I,--insecure}'[Use HTTP transport]' \
        '(-L --local)'{-L,--local}'[Allow local/private address hints]' \
        '1:command:->cmd' \
        '*::arg:->args'

    case $state in
        cmd)
            _describe 'command' commands
            ;;
        args)
            case $words[1] in
                server)
                    _values 'subcommand' test
                    ;;
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
	sb.WriteString("complete -c tltv -n \"not __fish_seen_subcommand_from $commands\" -s j -l json -d 'JSON output'\n")
	sb.WriteString("complete -c tltv -n \"not __fish_seen_subcommand_from $commands\" -s C -l no-color -d 'Disable colors'\n")
	sb.WriteString("complete -c tltv -n \"not __fish_seen_subcommand_from $commands\" -s I -l insecure -d 'Use HTTP transport'\n")
	sb.WriteString("complete -c tltv -n \"not __fish_seen_subcommand_from $commands\" -s L -l local -d 'Allow local addresses'\n")

	descriptions := map[string]string{
		"keygen": "Generate channel keypair", "vanity": "Mine vanity IDs",
		"inspect": "Inspect channel ID", "sign": "Sign document",
		"verify": "Verify document", "template": "Document template",
		"parse": "Parse tltv:// URI", "format": "Build tltv:// URI",
		"resolve": "Resolve URI end-to-end", "node": "Probe a node",
		"fetch": "Fetch metadata", "guide": "Fetch guide",
		"peers": "List peers", "stream": "Check stream",
		"crawl": "Crawl network",
		"server": "Content server", "bridge": "Bridge origin server", "relay": "Relay node",
		"migrate": "Create migration",
		"completion": "Shell completions", "version": "Show version",
	}
	for _, cmd := range allCommands {
		desc := descriptions[cmd]
		sb.WriteString(fmt.Sprintf("complete -c tltv -n \"not __fish_seen_subcommand_from $commands\" -a %s -d '%s'\n", cmd, desc))
	}
	sb.WriteString("\ncomplete -c tltv -n \"__fish_seen_subcommand_from server\" -a 'test'\n")
	sb.WriteString("complete -c tltv -n \"__fish_seen_subcommand_from template\" -a 'metadata guide migration'\n")
	sb.WriteString("complete -c tltv -n \"__fish_seen_subcommand_from completion\" -a 'bash zsh fish'\n")
	return sb.String()
}
