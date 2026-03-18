package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"
)

func cmdNode(args []string) {
	fs := flag.NewFlagSet("node", flag.ExitOnError)
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Probe a TLTV node's .well-known endpoint\n\n")
		fmt.Fprintf(os.Stderr, "Usage: tltv node <host[:port]>\n\n")
		fmt.Fprintf(os.Stderr, "Examples:\n")
		fmt.Fprintf(os.Stderr, "  tltv node example.com\n")
		fmt.Fprintf(os.Stderr, "  tltv node localhost:8000\n\n")
	}
	fs.Parse(args)

	if fs.NArg() < 1 {
		fs.Usage()
		os.Exit(1)
	}

	host := normalizeHost(fs.Arg(0))
	client := newClient(flagInsecure)

	info, err := client.FetchNodeInfo(host)
	if err != nil {
		fatal("could not reach node: %v", err)
	}

	if flagJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(info)
		return
	}

	printHeader("Node: " + host)
	printField("Protocol", info.Protocol)
	printField("Versions", fmt.Sprintf("%v", info.Versions))
	printField("Channels", fmt.Sprintf("%d", len(info.Channels)))
	printField("Relaying", fmt.Sprintf("%d", len(info.Relaying)))

	if len(info.Channels) > 0 {
		printHeader("Channels")
		var rows [][]string
		for _, ch := range info.Channels {
			rows = append(rows, []string{truncateID(ch.ID, 24), ch.Name})
		}
		printTable([]string{"ID", "Name"}, rows)
	}

	if len(info.Relaying) > 0 {
		printHeader("Relaying")
		var rows [][]string
		for _, ch := range info.Relaying {
			rows = append(rows, []string{truncateID(ch.ID, 24), ch.Name})
		}
		printTable([]string{"ID", "Name"}, rows)
	}
	fmt.Println()
}

func cmdFetch(args []string) {
	fs := flag.NewFlagSet("fetch", flag.ExitOnError)
	token := fs.String("token", "", "access token for private channels")
	noVerify := fs.Bool("no-verify", false, "skip signature verification")
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Fetch and verify channel metadata\n\n")
		fmt.Fprintf(os.Stderr, "Usage: tltv fetch <channel_id@host[:port]>\n\n")
		fmt.Fprintf(os.Stderr, "Examples:\n")
		fmt.Fprintf(os.Stderr, "  tltv fetch TVMkVH...@example.com\n")
		fmt.Fprintf(os.Stderr, "  tltv fetch TVMkVH...@localhost:8000\n\n")
		fmt.Fprintf(os.Stderr, "Flags:\n")
		fs.PrintDefaults()
	}
	fs.Parse(args)

	if fs.NArg() < 1 {
		fs.Usage()
		os.Exit(1)
	}

	channelID, host, err := parseTarget(fs.Arg(0))
	if err != nil {
		fatal("%v", err)
	}

	client := newClient(flagInsecure)
	doc, err := client.FetchMetadata(host, channelID, *token)
	if err != nil {
		fatal("%v", err)
	}

	if flagJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(doc)
		return
	}

	// Verify signature
	var sigErr error
	if !*noVerify {
		// Check if this is a migration document
		docType, _ := doc["type"].(string)
		if docType == "migration" {
			sigErr = verifyMigration(doc, channelID)
		} else {
			sigErr = verifyDocument(doc, channelID)
		}
		if sigErr != nil {
			printFail("Signature: " + sigErr.Error())
		} else {
			printOK("Signature valid")
		}
	}

	docType, _ := doc["type"].(string)
	if docType == "migration" {
		printHeader("Migration Document")
		printField("From", getString(doc, "from"))
		printField("To", getString(doc, "to"))
		printField("Reason", getString(doc, "reason"))
		printField("Migrated", getString(doc, "migrated"))
		printField("Seq", getString(doc, "seq"))
	} else {
		printHeader("Channel Metadata")
		printField("Channel ID", getString(doc, "id"))
		printField("Name", getString(doc, "name"))
		if desc := getString(doc, "description"); desc != "" {
			printField("Description", desc)
		}
		printField("Status", getStringDefault(doc, "status", "active"))
		printField("Access", getStringDefault(doc, "access", "public"))
		printField("Stream", getString(doc, "stream"))
		if guide := getString(doc, "guide"); guide != "" {
			printField("Guide", guide)
		}
		printField("Updated", getString(doc, "updated"))
		printField("Seq", getString(doc, "seq"))
		if lang := getString(doc, "language"); lang != "" {
			printField("Language", lang)
		}
		if tz := getString(doc, "timezone"); tz != "" {
			printField("Timezone", tz)
		}
		if tags := getStringSlice(doc, "tags"); len(tags) > 0 {
			printField("Tags", strings.Join(tags, ", "))
		}
		if origins := getStringSlice(doc, "origins"); len(origins) > 0 {
			printField("Origins", strings.Join(origins, ", "))
		}
		if onDemand, ok := doc["on_demand"].(bool); ok && onDemand {
			printField("On-Demand", "yes")
		}
	}

	if isTestChannel(channelID) {
		fmt.Println()
		printWarn("This is the RFC 8032 test channel. Do NOT use in production.")
	}
	fmt.Println()

	if sigErr != nil {
		os.Exit(1)
	}
}

func cmdGuide(args []string) {
	fs := flag.NewFlagSet("guide", flag.ExitOnError)
	token := fs.String("token", "", "access token for private channels")
	noVerify := fs.Bool("no-verify", false, "skip signature verification")
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Fetch and verify a channel guide\n\n")
		fmt.Fprintf(os.Stderr, "Usage: tltv guide <channel_id@host[:port]>\n\n")
		fmt.Fprintf(os.Stderr, "Flags:\n")
		fs.PrintDefaults()
	}
	fs.Parse(args)

	if fs.NArg() < 1 {
		fs.Usage()
		os.Exit(1)
	}

	channelID, host, err := parseTarget(fs.Arg(0))
	if err != nil {
		fatal("%v", err)
	}

	client := newClient(flagInsecure)
	doc, err := client.FetchGuide(host, channelID, *token)
	if err != nil {
		fatal("%v", err)
	}

	if flagJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(doc)
		return
	}

	// Verify signature
	var sigErr error
	if !*noVerify {
		sigErr = verifyDocument(doc, channelID)
		if sigErr != nil {
			printFail("Signature: " + sigErr.Error())
		} else {
			printOK("Signature valid")
		}
	}

	printHeader("Channel Guide")
	printField("Channel ID", getString(doc, "id"))
	printField("From", getString(doc, "from"))
	printField("Until", getString(doc, "until"))
	printField("Updated", getString(doc, "updated"))
	printField("Seq", getString(doc, "seq"))

	entries, _ := doc["entries"].([]interface{})
	if len(entries) > 0 {
		printHeader(fmt.Sprintf("Entries (%d)", len(entries)))
		var rows [][]string
		for _, e := range entries {
			entry, _ := e.(map[string]interface{})
			if entry == nil {
				continue
			}
			start := getString(entry, "start")
			end := getString(entry, "end")
			title := getString(entry, "title")
			cat := getString(entry, "category")

			// Format times more compactly
			if t, err := time.Parse("2006-01-02T15:04:05Z", start); err == nil {
				start = t.Format("Jan 02 15:04")
			}
			if t, err := time.Parse("2006-01-02T15:04:05Z", end); err == nil {
				end = t.Format("15:04")
			}

			timeRange := start + " - " + end
			rows = append(rows, []string{timeRange, title, cat})
		}
		printTable([]string{"Time", "Title", "Category"}, rows)
	}
	fmt.Println()

	if sigErr != nil {
		os.Exit(1)
	}
}

func cmdPeers(args []string) {
	fs := flag.NewFlagSet("peers", flag.ExitOnError)
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Fetch peer list from a TLTV node\n\n")
		fmt.Fprintf(os.Stderr, "Usage: tltv peers <host[:port]>\n\n")
	}
	fs.Parse(args)

	if fs.NArg() < 1 {
		fs.Usage()
		os.Exit(1)
	}

	host := normalizeHost(fs.Arg(0))
	client := newClient(flagInsecure)

	exchange, err := client.FetchPeers(host)
	if err != nil {
		fatal("%v", err)
	}

	if flagJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(exchange)
		return
	}

	printHeader(fmt.Sprintf("Peers (%d)", len(exchange.Peers)))
	if len(exchange.Peers) == 0 {
		fmt.Println("  No peers reported")
	} else {
		var rows [][]string
		for _, p := range exchange.Peers {
			hints := strings.Join(p.Hints, ", ")
			if hints == "" {
				hints = "-"
			}
			rows = append(rows, []string{
				truncateID(p.ID, 24),
				p.Name,
				hints,
				p.LastSeen,
			})
		}
		printTable([]string{"ID", "Name", "Hints", "Last Seen"}, rows)
	}
	fmt.Println()
}

func cmdStream(args []string) {
	fs := flag.NewFlagSet("stream", flag.ExitOnError)
	token := fs.String("token", "", "access token for private channels")
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Check stream availability for a channel\n\n")
		fmt.Fprintf(os.Stderr, "Usage: tltv stream <channel_id@host[:port]>\n\n")
		fmt.Fprintf(os.Stderr, "Flags:\n")
		fs.PrintDefaults()
	}
	fs.Parse(args)

	if fs.NArg() < 1 {
		fs.Usage()
		os.Exit(1)
	}

	channelID, host, err := parseTarget(fs.Arg(0))
	if err != nil {
		fatal("%v", err)
	}

	client := newClient(flagInsecure)
	status, contentType, body, err := client.CheckStream(host, channelID, *token)
	if err != nil {
		fatal("stream check failed: %v", err)
	}

	if flagJSON {
		result := map[string]interface{}{
			"status":       status,
			"content_type": contentType,
			"available":    status == 200,
		}
		if status == 200 {
			result["manifest_lines"] = strings.Count(body, "\n")
			result["manifest_bytes"] = len(body)
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(result)
		return
	}

	printHeader("Stream: " + truncateID(channelID, 24) + " @ " + host)

	switch status {
	case 200:
		printOK("Stream is live")
		printField("Content-Type", contentType)

		// Parse basic HLS info
		lines := strings.Split(body, "\n")
		segments := 0
		var targetDuration string
		for _, line := range lines {
			line = strings.TrimSpace(line)
			if strings.HasSuffix(line, ".ts") || strings.HasSuffix(line, ".m4s") {
				segments++
			}
			if strings.HasPrefix(line, "#EXT-X-TARGETDURATION:") {
				targetDuration = strings.TrimPrefix(line, "#EXT-X-TARGETDURATION:")
			}
		}
		printField("Segments", fmt.Sprintf("%d", segments))
		if targetDuration != "" {
			printField("Target Dur.", targetDuration+"s")
		}
		printField("Manifest", fmt.Sprintf("%d bytes, %d lines", len(body), len(lines)))

	case 302:
		printOK("Stream available (redirect)")

	case 403:
		printFail("Access denied (token required)")

	case 404:
		printFail("Channel not found")

	case 503:
		printWarn("Stream unavailable (channel may be on-demand and idle)")

	default:
		printFail(fmt.Sprintf("HTTP %d", status))
	}
	fmt.Println()
}

func cmdCrawl(args []string) {
	fs := flag.NewFlagSet("crawl", flag.ExitOnError)
	depth := fs.Int("depth", 2, "maximum crawl depth")
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Crawl the peer gossip network to discover channels\n\n")
		fmt.Fprintf(os.Stderr, "Usage: tltv crawl <host[:port]>\n\n")
		fmt.Fprintf(os.Stderr, "Flags:\n")
		fs.PrintDefaults()
	}
	fs.Parse(args)

	if fs.NArg() < 1 {
		fs.Usage()
		os.Exit(1)
	}

	startHost := normalizeHost(fs.Arg(0))
	client := newClient(flagInsecure)

	type discoveredChannel struct {
		ID     string
		Name   string
		Host   string
		Source string // "channel", "relay", "peer"
	}

	type crawlTarget struct {
		host  string
		depth int
	}

	visited := make(map[string]bool)
	channels := make(map[string]discoveredChannel) // dedup by ID
	queue := []crawlTarget{{host: startHost, depth: 0}}

	fmt.Printf("Crawling from %s (max depth %d)...\n\n", startHost, *depth)

	nodesProbed := 0

	for len(queue) > 0 {
		target := queue[0]
		queue = queue[1:]

		if visited[target.host] || target.depth > *depth {
			continue
		}
		visited[target.host] = true

		// Fetch node info
		fmt.Printf("  Probing %s...", target.host)
		info, err := client.FetchNodeInfo(target.host)
		if err != nil {
			fmt.Printf(" %s\n", c(cRed, "error: "+err.Error()))
			continue
		}

		nodesProbed++
		fmt.Printf(" %d ch, %d relay", len(info.Channels), len(info.Relaying))

		for _, ch := range info.Channels {
			if _, exists := channels[ch.ID]; !exists {
				channels[ch.ID] = discoveredChannel{
					ID: ch.ID, Name: ch.Name,
					Host: target.host, Source: "channel",
				}
			}
		}
		for _, ch := range info.Relaying {
			if _, exists := channels[ch.ID]; !exists {
				channels[ch.ID] = discoveredChannel{
					ID: ch.ID, Name: ch.Name,
					Host: target.host, Source: "relay",
				}
			}
		}

		// Fetch peers
		exchange, err := client.FetchPeers(target.host)
		if err != nil {
			fmt.Printf(", no peers\n")
			continue
		}

		fmt.Printf(", %d peers\n", len(exchange.Peers))

		for _, p := range exchange.Peers {
			if _, exists := channels[p.ID]; !exists {
				hintStr := ""
				if len(p.Hints) > 0 {
					hintStr = p.Hints[0]
				}
				channels[p.ID] = discoveredChannel{
					ID: p.ID, Name: p.Name,
					Host: hintStr, Source: "peer",
				}
			}
			// Add peer hints to crawl queue
			for _, hint := range p.Hints {
				if !visited[hint] {
					queue = append(queue, crawlTarget{host: hint, depth: target.depth + 1})
				}
			}
		}
	}

	if flagJSON {
		var result []map[string]string
		for _, ch := range channels {
			result = append(result, map[string]string{
				"id":     ch.ID,
				"name":   ch.Name,
				"host":   ch.Host,
				"source": ch.Source,
			})
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(map[string]interface{}{
			"nodes_probed": nodesProbed,
			"channels":     result,
		})
		return
	}

	printHeader(fmt.Sprintf("Discovered %d channel(s) across %d node(s)", len(channels), nodesProbed))
	if len(channels) > 0 {
		var rows [][]string
		for _, ch := range channels {
			rows = append(rows, []string{
				truncateID(ch.ID, 24),
				ch.Name,
				ch.Host,
				ch.Source,
			})
		}
		printTable([]string{"ID", "Name", "Host", "Source"}, rows)
	}
	fmt.Println()
}

func cmdResolve(args []string) {
	fs := flag.NewFlagSet("resolve", flag.ExitOnError)
	token := fs.String("token", "", "access token (overrides URI token)")
	noVerify := fs.Bool("no-verify", false, "skip signature verification")
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Resolve a tltv:// URI end-to-end\n\n")
		fmt.Fprintf(os.Stderr, "Usage: tltv resolve <uri>\n\n")
		fmt.Fprintf(os.Stderr, "Parses the URI, tries each hint, fetches and verifies metadata,\n")
		fmt.Fprintf(os.Stderr, "and checks stream availability.\n\n")
		fmt.Fprintf(os.Stderr, "Examples:\n")
		fmt.Fprintf(os.Stderr, "  tltv resolve \"tltv://TVMkVH...@example.com:443\"\n")
		fmt.Fprintf(os.Stderr, "  tltv resolve \"tltv://TVMkVH...?via=relay1.com:443,relay2.com:443\"\n\n")
		fmt.Fprintf(os.Stderr, "Flags:\n")
		fs.PrintDefaults()
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

	// Override token if flag provided
	tok := uri.Token
	if *token != "" {
		tok = *token
	}

	// Validate channel ID
	if err := validateChannelID(uri.ChannelID); err != nil {
		fatal("invalid channel ID: %v", err)
	}

	client := newClient(flagInsecure)

	if len(uri.Hints) == 0 {
		fatal("no peer hints in URI -- need at least one host to contact")
	}

	if !flagJSON {
		printHeader("Resolving: " + fs.Arg(0))
		printField("Channel ID", uri.ChannelID)
		if tok != "" {
			printField("Token", tok)
		}
		fmt.Println()
	}

	// Try each hint
	var resolvedHost string
	var metadata map[string]interface{}
	var streamStatus int
	var streamContentType string

	for i, hint := range uri.Hints {
		host := normalizeHost(hint)

		if !flagJSON {
			label := fmt.Sprintf("Hint %d", i+1)
			fmt.Printf("  %s  %s ... ", c(cDim, label), host)
		}

		// Step 1: Fetch .well-known/tltv and check for channel
		info, err := client.FetchNodeInfo(host)
		if err != nil {
			if !flagJSON {
				fmt.Printf("%s\n", c(cRed, "unreachable"))
			}
			continue
		}

		found := false
		source := ""
		for _, ch := range info.Channels {
			if ch.ID == uri.ChannelID {
				found = true
				source = "channel"
				break
			}
		}
		if !found {
			for _, ch := range info.Relaying {
				if ch.ID == uri.ChannelID {
					found = true
					source = "relay"
					break
				}
			}
		}

		if !flagJSON {
			if !found && tok == "" {
				fmt.Printf("%s", c(cYellow, "not listed"))
			} else if found {
				fmt.Printf("%s (%s)", c(cGreen, "found"), source)
			}
		}

		// Step 2: Negotiate version
		bestVersion := 0
		for _, v := range info.Versions {
			if v > bestVersion {
				bestVersion = v
			}
		}
		if bestVersion == 0 {
			bestVersion = 1 // fallback
		}

		// Step 3: Fetch metadata
		doc, err := client.FetchMetadata(host, uri.ChannelID, tok)
		if err != nil {
			if !flagJSON {
				fmt.Printf(" ... %s\n", c(cRed, "metadata: "+err.Error()))
			}
			continue
		}

		// Step 4: Verify signature
		if !*noVerify {
			docType, _ := doc["type"].(string)
			if docType == "migration" {
				err = verifyMigration(doc, uri.ChannelID)
			} else {
				err = verifyDocument(doc, uri.ChannelID)
			}
			if err != nil {
				if !flagJSON {
					fmt.Printf(" ... %s\n", c(cRed, "signature: "+err.Error()))
				}
				continue
			}
		}

		if !flagJSON {
			fmt.Printf(" ... %s\n", c(cGreen, "verified"))
		}
		resolvedHost = host
		metadata = doc
		break
	}

	if metadata == nil {
		if !flagJSON {
			fmt.Println()
		}
		fatal("could not resolve channel from any hint")
	}

	// Step 5: Check stream
	docType, _ := metadata["type"].(string)
	if docType != "migration" {
		streamStatus, streamContentType, _, _ = client.CheckStream(resolvedHost, uri.ChannelID, tok)
	}

	// JSON output
	if flagJSON {
		result := map[string]interface{}{
			"channel_id":    uri.ChannelID,
			"resolved_host": resolvedHost,
			"verified":      !*noVerify,
			"metadata":      metadata,
		}
		if docType != "migration" {
			result["stream_status"] = streamStatus
			result["stream_live"] = streamStatus == 200
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(result)
		return
	}

	// Human output
	if docType == "migration" {
		printHeader("Migration")
		printField("From", getString(metadata, "from"))
		printField("To", getString(metadata, "to"))
		printField("Reason", getString(metadata, "reason"))
		printField("Migrated", getString(metadata, "migrated"))
		printWarn("Channel has migrated to " + getString(metadata, "to"))
	} else {
		printHeader("Channel")
		printField("Name", getString(metadata, "name"))
		if desc := getString(metadata, "description"); desc != "" {
			printField("Description", desc)
		}
		printField("Status", getStringDefault(metadata, "status", "active"))
		printField("Access", getStringDefault(metadata, "access", "public"))
		printField("Updated", getString(metadata, "updated"))
		if tags := getStringSlice(metadata, "tags"); len(tags) > 0 {
			printField("Tags", strings.Join(tags, ", "))
		}

		fmt.Println()
		switch streamStatus {
		case 200:
			printOK("Stream is live (" + streamContentType + ")")
		case 302:
			printOK("Stream available (redirect)")
		case 503:
			printWarn("Stream unavailable (on-demand / idle)")
		case 0:
			printFail("Stream check failed")
		default:
			printFail(fmt.Sprintf("Stream HTTP %d", streamStatus))
		}
	}

	if isTestChannel(uri.ChannelID) {
		fmt.Println()
		printWarn("This is the RFC 8032 test channel. Do NOT use in production.")
	}

	fmt.Println()
}

// Helper functions for extracting typed values from map[string]interface{}
func getString(doc map[string]interface{}, key string) string {
	v, ok := doc[key]
	if !ok {
		return ""
	}
	switch val := v.(type) {
	case string:
		return val
	default:
		return fmt.Sprintf("%v", val)
	}
}

func getStringDefault(doc map[string]interface{}, key, defaultVal string) string {
	s := getString(doc, key)
	if s == "" {
		return defaultVal
	}
	return s
}

func getStringSlice(doc map[string]interface{}, key string) []string {
	v, ok := doc[key]
	if !ok {
		return nil
	}
	arr, ok := v.([]interface{})
	if !ok {
		return nil
	}
	result := make([]string, 0, len(arr))
	for _, item := range arr {
		if s, ok := item.(string); ok {
			result = append(result, s)
		}
	}
	return result
}
