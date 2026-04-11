package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"
)

func cmdInfo(args []string) {
	fs := flag.NewFlagSet("info", flag.ExitOnError)
	token := fs.String("token", "", "access token for private channels")
	fs.StringVar(token, "t", "", "alias for --token")
	noVerify := fs.Bool("no-verify", false, "skip signature verification")
	fs.BoolVar(noVerify, "V", false, "alias for --no-verify")
	watch, interval := addWatchFlags(fs)
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Show all info about a target\n\n")
		fmt.Fprintf(os.Stderr, "Usage: tltv info <target>\n\n")
		fmt.Fprintf(os.Stderr, "With a channel target (tltv:// URI or ID@host), shows all 5 sections:\n")
		fmt.Fprintf(os.Stderr, "  channel, stream, guide, node, peers\n\n")
		fmt.Fprintf(os.Stderr, "With a bare host, shows the node section only.\n\n")
		fmt.Fprintf(os.Stderr, "Examples:\n")
		fmt.Fprintf(os.Stderr, "  tltv info \"tltv://TVMkVH...@example.com:443\"\n")
		fmt.Fprintf(os.Stderr, "  tltv info TVMkVH...@example.com\n")
		fmt.Fprintf(os.Stderr, "  tltv info example.com\n\n")
		fmt.Fprintf(os.Stderr, "Flags:\n")
		fmt.Fprintf(os.Stderr, "  -t, --token string    access token for private channels\n")
		fmt.Fprintf(os.Stderr, "  -V, --no-verify       skip signature verification\n")
		fmt.Fprintf(os.Stderr, "  -w, --watch           auto-refresh output\n")
		fmt.Fprintf(os.Stderr, "      --interval int    refresh interval in seconds (default 2)\n")
	}
	fs.Parse(args)

	if fs.NArg() < 1 {
		fs.Usage()
		os.Exit(1)
	}

	if *token == "" {
		*token = extractToken(fs.Arg(0))
	}

	client := newClient(flagInsecure)

	// Try to parse as a channel target first (tltv:// URI or id@host)
	channelID, host, parseErr := parseTarget(fs.Arg(0))
	if parseErr != nil {
		// Bare host mode — just show node info
		host = normalizeHost(fs.Arg(0))
		watchLoop(*watch, *interval, func() {
			infoNodeOnly(client, host)
		})
		return
	}

	// Full channel mode — all 5 sections
	watchLoop(*watch, *interval, func() {
		infoFull(client, channelID, host, *token, *noVerify)
	})
}

// infoNodeOnly shows node info for a bare host target.
func infoNodeOnly(client *Client, host string) {
	info, err := client.FetchNodeInfo(host)
	if err != nil {
		fatal("could not reach node: %v", err)
	}

	if flagJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(map[string]interface{}{
			"node": info,
		})
		return
	}

	printInfoNode(info, host, "", nil)
	fmt.Println()
}

// infoFull shows all 5 sections for a channel target.
func infoFull(client *Client, channelID, host, token string, noVerify bool) {
	// Collect all data upfront
	metaDoc, metaErr := client.FetchMetadata(host, channelID, token)
	guideDoc, guideErr := client.FetchGuide(host, channelID, token)
	streamStatus, streamContentType, streamBody, streamErr := client.CheckStream(host, channelID, token)
	nodeInfo, nodeErr := client.FetchNodeInfo(host)
	exchange, peersErr := client.FetchPeers(host)

	// Verify signatures
	var metaSigErr, guideSigErr error
	docType := ""
	if metaErr == nil && !noVerify {
		docType, _ = metaDoc["type"].(string)
		if docType == "migration" {
			metaSigErr = verifyMigration(metaDoc, channelID)
		} else {
			metaSigErr = verifyDocument(metaDoc, channelID)
		}
	}
	if guideErr == nil && !noVerify {
		guideSigErr = verifyDocument(guideDoc, channelID)
	}

	// Check access mode (spec §5.2)
	var accessErr error
	if metaErr == nil {
		accessErr = checkAccessMode(metaDoc)
	}

	// Check origin status from signed metadata (§11)
	var oc *originCheck
	if metaErr == nil && metaSigErr == nil && !noVerify {
		oc = checkOrigin(metaDoc, host)
	}
	var checks map[string]*originCheck
	if oc != nil {
		checks = map[string]*originCheck{channelID: oc}
	}

	if flagJSON {
		result := map[string]interface{}{}

		// Channel section
		if metaErr == nil {
			base := client.baseURL(host)
			ch := map[string]interface{}{
				"channel_id": channelID,
				"host":       host,
				"verified":   !noVerify && metaSigErr == nil,
				"document":   metaDoc,
			}
			if accessErr != nil {
				ch["access_warning"] = accessErr.Error()
			}
			status := getStringDefault(metaDoc, "status", "active")
			if status != "active" && status != "retired" {
				ch["status_warning"] = "unknown status: " + status
			}
			if stream := getString(metaDoc, "stream"); stream != "" {
				ch["stream_url"] = base + stream
			}
			if guide := getString(metaDoc, "guide"); guide != "" {
				ch["guide_url"] = base + guide
				xmltvPath := strings.Replace(guide, "guide.json", "guide.xml", 1)
				ch["xmltv_url"] = base + xmltvPath
			}
			ch["uri"] = formatTLTVUri(channelID, []string{host}, token)
			if metaSigErr != nil {
				ch["verification_error"] = metaSigErr.Error()
			}
			// Origin verification from signed metadata (§11)
			if oc != nil && oc.HasOrigins {
				ch["verified_origin"] = oc.IsOrigin
				ch["signed_origins"] = oc.Origins
				// Warn if discovery disagrees with signed origins
				if !oc.IsOrigin && nodeErr == nil {
					for _, nc := range nodeInfo.Channels {
						if nc.ID == channelID {
							ch["origin_warning"] = "node claims origin via unsigned discovery but hostname not in signed origins field"
							break
						}
					}
				}
			}
			result["channel"] = ch
		}

		// Stream section
		if streamErr == nil {
			streamURL := client.baseURL(host) + "/tltv/v1/channels/" + channelID + "/stream.m3u8"
			if token != "" {
				streamURL += "?token=" + token
			}
			st := map[string]interface{}{
				"status":       streamStatus,
				"content_type": streamContentType,
				"available":    streamStatus == 200,
				"stream_url":   streamURL,
			}
			if streamStatus == 200 {
				segs, td, ms := parseManifestFields(streamBody)
				st["segments"] = segs
				if td != "" {
					st["target_duration"] = td
				}
				if ms != "" {
					st["media_sequence"] = ms
				}
			}
			result["stream"] = st
		}

		// Guide section
		if guideErr == nil {
			g := map[string]interface{}{
				"verified": !noVerify && guideSigErr == nil,
				"document": guideDoc,
			}
			if guideSigErr != nil {
				g["verification_error"] = guideSigErr.Error()
			}
			result["guide"] = g
		}

		// Node section
		if nodeErr == nil {
			result["node"] = nodeInfo
		}

		// Peers section
		if peersErr == nil {
			result["peers"] = exchange
		}

		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(result)
		if metaSigErr != nil {
			os.Exit(1)
		}
		return
	}

	// === Human output: all 5 sections ===

	// 1. Channel
	if metaErr != nil {
		printHeader("Channel: " + channelID)
		printFail("could not fetch: " + metaErr.Error())
	} else if docType == "migration" {
		printHeader("Migration Document")
		if !noVerify {
			if metaSigErr != nil {
				printField("Verified", c(cRed, "✗ ")+metaSigErr.Error())
			} else {
				printField("Verified", c(cGreen, "✓")+" Signature valid")
			}
		}
		printField("From", getString(metaDoc, "from"))
		printField("To", getString(metaDoc, "to"))
		if reason := getString(metaDoc, "reason"); reason != "" {
			printField("Reason", reason)
		}
		printField("Migrated", getString(metaDoc, "migrated"))
		printField("Seq", getString(metaDoc, "seq"))
		printRemainingKeys(metaDoc, "v", "type", "from", "to", "reason", "migrated", "seq", "signature")
	} else {
		printHeader("Channel: " + channelID)
		if !noVerify {
			if metaSigErr != nil {
				printField("Verified", c(cRed, "✗ ")+metaSigErr.Error())
			} else {
				printField("Verified", c(cGreen, "✓")+" Signature valid")
			}
		}
		printField("Name", getString(metaDoc, "name"))
		uri := formatTLTVUri(channelID, []string{host}, token)
		printField("URI", uri)
		status := getStringDefault(metaDoc, "status", "active")
		if status != "active" && status != "retired" {
			printField("Status", c(cYellow, status)+" (unknown)")
		} else {
			printField("Status", status)
		}
		accessVal := getStringDefault(metaDoc, "access", "public")
		if accessErr != nil {
			printField("Access", c(cYellow, accessVal)+" (unsupported)")
		} else {
			printField("Access", accessVal)
		}

		if lang := getString(metaDoc, "language"); lang != "" {
			printField("Language", lang)
		}
		if tz := getString(metaDoc, "timezone"); tz != "" {
			printField("Timezone", tz)
		}

		base := client.baseURL(host)
		if stream := getString(metaDoc, "stream"); stream != "" {
			printField("Stream", base+stream)
		}
		if guide := getString(metaDoc, "guide"); guide != "" {
			printField("Guide", base+guide)
			xmltvPath := strings.Replace(guide, "guide.json", "guide.xml", 1)
			printField("XMLTV", base+xmltvPath)
		}
		if icon := getString(metaDoc, "icon"); icon != "" {
			printField("Icon", base+icon)
		}
		if origins := extractOrigins(metaDoc); origins != nil {
			printField("Origins", strings.Join(origins, ", "))
		}
		printField("Updated", getString(metaDoc, "updated"))
		printField("Seq", getString(metaDoc, "seq"))
		printRemainingKeys(metaDoc, "v", "id", "name", "status", "access", "stream",
			"guide", "icon", "origins", "updated", "seq", "signature", "language", "timezone",
			"on_demand", "description", "tags")
	}

	// 2. Stream
	streamURL := client.baseURL(host) + "/tltv/v1/channels/" + channelID + "/stream.m3u8"
	if token != "" {
		streamURL += "?token=" + token
	}
	if streamErr != nil {
		printHeader("Stream")
		printFail("could not check: " + streamErr.Error())
	} else {
		printHeader("Stream")
		switch streamStatus {
		case 200:
			printField("Status", c(cGreen, "✓")+" live")
			printField("URL", streamURL)
			printField("Content-Type", streamContentType)
			segs, td, ms := parseManifestFields(streamBody)
			printField("Segments", fmt.Sprintf("%d", segs))
			if td != "" {
				printField("Target Duration", td+"s")
			}
			if ms != "" {
				printField("Media Sequence", ms)
			}
		case 302:
			printField("Status", c(cGreen, "✓")+" live (redirect)")
			printField("URL", streamURL)
		case 403:
			printField("Status", c(cRed, "✗")+" access denied")
			printField("URL", streamURL)
		case 404:
			printField("Status", c(cRed, "✗")+" not found")
			printField("URL", streamURL)
		case 503:
			printField("Status", c(cYellow, "!")+" unavailable")
			printField("URL", streamURL)
		default:
			printField("Status", c(cRed, "✗")+" HTTP "+fmt.Sprintf("%d", streamStatus))
			printField("URL", streamURL)
		}
	}

	// 3. Guide
	if guideErr != nil {
		printHeader("Guide")
		printFail("could not fetch: " + guideErr.Error())
	} else {
		printHeader("Guide")
		if !noVerify {
			if guideSigErr != nil {
				printField("Verified", c(cRed, "✗ ")+guideSigErr.Error())
			} else {
				printField("Verified", c(cGreen, "✓")+" Signature valid")
			}
		}
		base := client.baseURL(host)
		if guide := getString(metaDoc, "guide"); guide != "" && metaErr == nil {
			printField("URL", base+guide)
			xmltvPath := strings.Replace(guide, "guide.json", "guide.xml", 1)
			printField("XMLTV", base+xmltvPath)
		}
		printField("From", getString(guideDoc, "from"))
		printField("Until", getString(guideDoc, "until"))
		entries, _ := guideDoc["entries"].([]interface{})
		printField("Entries", fmt.Sprintf("%d", len(entries)))

		if len(entries) > 0 {
			now := time.Now().UTC()
			fmt.Println()
			for _, e := range entries {
				entry, _ := e.(map[string]interface{})
				if entry == nil {
					continue
				}
				startStr := getString(entry, "start")
				endStr := getString(entry, "end")
				title := getString(entry, "title")

				startT, startErr := time.Parse("2006-01-02T15:04:05Z", startStr)
				endT, endErr := time.Parse("2006-01-02T15:04:05Z", endStr)
				nowPlaying := startErr == nil && endErr == nil && !now.Before(startT) && now.Before(endT)

				start := startStr
				end := endStr
				if startErr == nil {
					start = startT.Format("Jan 02 15:04")
				}
				if endErr == nil {
					end = endT.Format("15:04")
				}

				marker := "  "
				if nowPlaying {
					if useColor {
						marker = c(cGreen, "> ")
					} else {
						marker = "> "
					}
				}
				cat := getString(entry, "category")
				line := marker + start + " - " + end + "  " + title
				if cat != "" {
					line += "  [" + cat + "]"
				}
				fmt.Println(line)
			}
		}
	}

	// 4. Node
	if nodeErr != nil {
		printHeader("Node: " + host)
		printFail("could not reach: " + nodeErr.Error())
	} else {
		printInfoNode(nodeInfo, host, channelID, checks)
	}

	// 5. Peers
	if peersErr != nil {
		printHeader("Peers")
		printFail("could not fetch: " + peersErr.Error())
	} else {
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
				rows = append(rows, []string{p.ID, p.Name, hints})
			}
			printTable([]string{"ID", "Name", "Hints"}, rows)
		}
	}

	if isTestChannel(channelID) {
		fmt.Println()
		printWarn("This is the RFC 8032 test channel. Do NOT use in production.")
	}
	fmt.Println()
}

// printInfoNode prints the node section, optionally marking the active channel.
// checks maps channel IDs to origin verification results from signed metadata.
// When nil or missing an entry, falls back to unsigned discovery labels.
func printInfoNode(info *NodeInfo, host, activeID string, checks map[string]*originCheck) {
	printHeader("Node: " + host)
	if len(info.Versions) > 0 {
		printField("Protocol", fmt.Sprintf("%s protocol v%d", info.Protocol, info.Versions[0]))
	} else {
		printField("Protocol", info.Protocol)
	}

	// Helper: build per-channel origin annotation from signed metadata.
	// discoveryType is "channel" or "relay" from the unsigned discovery doc.
	originAnnotation := func(chID, discoveryType string) string {
		if checks == nil {
			return ""
		}
		oc, ok := checks[chID]
		if !ok || oc == nil || !oc.HasOrigins {
			return ""
		}
		if oc.IsOrigin {
			return c(cGreen, " (origin)")
		}
		originHint := ""
		if len(oc.Origins) > 0 {
			originHint = " — real origin is " + oc.Origins[0]
		}
		if discoveryType == "channel" {
			// Discovery claims origin but signed metadata says otherwise
			return c(cYellow, " (spoofed origin" + originHint + ")")
		}
		return c(cDim, " (relay" + originHint + ")")
	}

	// Determine section label — override "Origin" if signed data says otherwise
	channelsLabel := "Origin Channels"
	if activeID != "" && checks != nil {
		if oc, ok := checks[activeID]; ok && oc != nil && oc.HasOrigins && !oc.IsOrigin {
			channelsLabel = "Channels"
		}
	}

	if len(info.Channels) > 0 {
		fmt.Println()
		fmt.Printf("  %s (%d)\n", channelsLabel, len(info.Channels))
		for _, ch := range info.Channels {
			marker := "  "
			if activeID != "" && ch.ID == activeID {
				if useColor {
					marker = c(cGreen, "> ")
				} else {
					marker = "> "
				}
			}
			fmt.Printf("  %s%s  %s%s\n", marker, ch.ID, c(cDim, ch.Name), originAnnotation(ch.ID, "channel"))
		}
	}

	if len(info.Relaying) > 0 {
		fmt.Println()
		fmt.Printf("  Relay Channels (%d)\n", len(info.Relaying))
		for _, ch := range info.Relaying {
			marker := "  "
			if activeID != "" && ch.ID == activeID {
				if useColor {
					marker = c(cGreen, "> ")
				} else {
					marker = "> "
				}
			}
			fmt.Printf("  %s%s  %s%s\n", marker, ch.ID, c(cDim, ch.Name), originAnnotation(ch.ID, "relay"))
		}
	}
}

// parseManifestFields extracts segment count, target duration, and media sequence
// from an HLS manifest body.
func parseManifestFields(body string) (segments int, targetDuration, mediaSequence string) {
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			if strings.HasPrefix(line, "#EXT-X-TARGETDURATION:") {
				targetDuration = strings.TrimPrefix(line, "#EXT-X-TARGETDURATION:")
			}
			if strings.HasPrefix(line, "#EXT-X-MEDIA-SEQUENCE:") {
				mediaSequence = strings.TrimPrefix(line, "#EXT-X-MEDIA-SEQUENCE:")
			}
			continue
		}
		segments++
	}
	return
}

func cmdNode(args []string) {
	fs := flag.NewFlagSet("node", flag.ExitOnError)
	watch, interval := addWatchFlags(fs)
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Query a TLTV node's identity\n\n")
		fmt.Fprintf(os.Stderr, "Usage: tltv node <host[:port]>\n\n")
		fmt.Fprintf(os.Stderr, "Examples:\n")
		fmt.Fprintf(os.Stderr, "  tltv node example.com\n")
		fmt.Fprintf(os.Stderr, "  tltv node localhost:8000\n\n")
		fmt.Fprintf(os.Stderr, "Flags:\n")
		fmt.Fprintf(os.Stderr, "  -w, --watch           auto-refresh output\n")
		fmt.Fprintf(os.Stderr, "      --interval int    refresh interval in seconds (default 2)\n")
	}
	fs.Parse(args)

	if fs.NArg() < 1 {
		fs.Usage()
		os.Exit(1)
	}

	host := normalizeHost(fs.Arg(0))
	client := newClient(flagInsecure)

	watchLoop(*watch, *interval, func() {
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

		printInfoNode(info, host, "", nil)
		fmt.Println()
	})
}

func cmdChannel(args []string) {
	fs := flag.NewFlagSet("channel", flag.ExitOnError)
	token := fs.String("token", "", "access token for private channels")
	fs.StringVar(token, "t", "", "alias for --token")
	noVerify := fs.Bool("no-verify", false, "skip signature verification")
	fs.BoolVar(noVerify, "V", false, "alias for --no-verify")
	watch, interval := addWatchFlags(fs)
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Fetch and verify channel metadata\n\n")
		fmt.Fprintf(os.Stderr, "Usage: tltv channel <target>\n\n")
		fmt.Fprintf(os.Stderr, "Target can be a tltv:// URI or compact ID@host format:\n")
		fmt.Fprintf(os.Stderr, "  tltv channel \"tltv://TVMkVH...@example.com:443\"\n")
		fmt.Fprintf(os.Stderr, "  tltv channel TVMkVH...@example.com\n")
		fmt.Fprintf(os.Stderr, "  tltv channel TVMkVH...@localhost:8000\n\n")
		fmt.Fprintf(os.Stderr, "Flags:\n")
		fmt.Fprintf(os.Stderr, "  -t, --token string    access token for private channels\n")
		fmt.Fprintf(os.Stderr, "  -V, --no-verify       skip signature verification\n")
		fmt.Fprintf(os.Stderr, "  -w, --watch           auto-refresh output\n")
		fmt.Fprintf(os.Stderr, "      --interval int    refresh interval in seconds (default 2)\n")
	}
	fs.Parse(args)

	if fs.NArg() < 1 {
		fs.Usage()
		os.Exit(1)
	}

	if *token == "" {
		*token = extractToken(fs.Arg(0))
	}

	client := newClient(flagInsecure)
	channelID, host, err := parseTargetOrDiscover(fs.Arg(0), client)
	if err != nil {
		fatal("%v", err)
	}

	displayChannel := func() {
		doc, err := client.FetchMetadata(host, channelID, *token)
		if err != nil {
			fatal("%v", err)
		}

		var sigErr error
		docType, _ := doc["type"].(string)
		if !*noVerify {
			if docType == "migration" {
				sigErr = verifyMigration(doc, channelID)
			} else {
				sigErr = verifyDocument(doc, channelID)
			}
		}

		// Check for unknown access modes (spec §5.2)
		accessErr := checkAccessMode(doc)

		if flagJSON {
			base := client.baseURL(host)
			result := map[string]interface{}{
				"channel_id": channelID,
				"host":       host,
				"verified":   !*noVerify && sigErr == nil,
				"document":   doc,
			}
			if accessErr != nil {
				result["access_warning"] = accessErr.Error()
			}
			status := getStringDefault(doc, "status", "active")
			if status != "active" && status != "retired" {
				result["status_warning"] = "unknown status: " + status
			}
			if stream := getString(doc, "stream"); stream != "" {
				result["stream_url"] = base + stream
			}
			if guide := getString(doc, "guide"); guide != "" {
				result["guide_url"] = base + guide
			}
			xmltvPath := strings.Replace(getString(doc, "guide"), "guide.json", "guide.xml", 1)
			if xmltvPath != "" {
				result["xmltv_url"] = base + xmltvPath
			}
			result["uri"] = formatTLTVUri(channelID, []string{host}, *token)
			if sigErr != nil {
				result["verification_error"] = sigErr.Error()
			}
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			enc.Encode(result)
			if sigErr != nil {
				os.Exit(1)
			}
			return
		}

		if docType == "migration" {
			printHeader("Migration Document")
			if !*noVerify {
				if sigErr != nil {
					printField("Verified", c(cRed, "✗ ")+sigErr.Error())
				} else {
					printField("Verified", c(cGreen, "✓")+" Signature valid")
				}
			}
			printField("From", getString(doc, "from"))
			printField("To", getString(doc, "to"))
			if reason := getString(doc, "reason"); reason != "" {
				printField("Reason", reason)
			}
			printField("Migrated", getString(doc, "migrated"))
			printField("Seq", getString(doc, "seq"))
			printRemainingKeys(doc, "v", "type", "from", "to", "reason", "migrated", "seq", "signature")
		} else {
			printHeader("Channel: " + channelID)
			if !*noVerify {
				if sigErr != nil {
					printField("Verified", c(cRed, "✗ ")+sigErr.Error())
				} else {
					printField("Verified", c(cGreen, "✓")+" Signature valid")
				}
			}
			printField("Name", getString(doc, "name"))
			printField("URI", formatTLTVUri(channelID, []string{host}, *token))
			status := getStringDefault(doc, "status", "active")
			if status != "active" && status != "retired" {
				printField("Status", c(cYellow, status)+" (unknown)")
			} else {
				printField("Status", status)
			}
			accessVal := getStringDefault(doc, "access", "public")
			if accessErr != nil {
				printField("Access", c(cYellow, accessVal)+" (unsupported)")
			} else {
				printField("Access", accessVal)
			}

			if lang := getString(doc, "language"); lang != "" {
				printField("Language", lang)
			}
			if tz := getString(doc, "timezone"); tz != "" {
				printField("Timezone", tz)
			}

			base := client.baseURL(host)
			if stream := getString(doc, "stream"); stream != "" {
				printField("Stream", base+stream)
			}
			if guide := getString(doc, "guide"); guide != "" {
				printField("Guide", base+guide)
				xmltvPath := strings.Replace(guide, "guide.json", "guide.xml", 1)
				printField("XMLTV", base+xmltvPath)
			}
			if icon := getString(doc, "icon"); icon != "" {
				printField("Icon", base+icon)
			}
			if origins := extractOrigins(doc); origins != nil {
				printField("Origins", strings.Join(origins, ", "))
			}
			printField("Updated", getString(doc, "updated"))
			printField("Seq", getString(doc, "seq"))
			printRemainingKeys(doc, "v", "id", "name", "status", "access", "stream",
				"guide", "icon", "origins", "updated", "seq", "signature", "language", "timezone",
				"on_demand", "description", "tags")
		}

		if isTestChannel(channelID) {
			fmt.Println()
			printWarn("This is the RFC 8032 test channel. Do NOT use in production.")
		}
		fmt.Println()

		if sigErr != nil && !*watch {
			os.Exit(1)
		}
	}

	watchLoop(*watch, *interval, displayChannel)
}

// printRemainingKeys prints any document keys not in the skip set.
// Used after the curated field list to surface unknown/future fields.
func printRemainingKeys(doc map[string]interface{}, skip ...string) {
	skipSet := make(map[string]bool, len(skip))
	for _, k := range skip {
		skipSet[k] = true
	}

	// Collect remaining keys and sort for stable output
	var remaining []string
	for k := range doc {
		if !skipSet[k] {
			remaining = append(remaining, k)
		}
	}
	if len(remaining) == 0 {
		return
	}
	sort.Strings(remaining)

	for _, k := range remaining {
		v := doc[k]
		switch val := v.(type) {
		case string:
			printField(k, val)
		case bool:
			if val {
				printField(k, "yes")
			} else {
				printField(k, "no")
			}
		case []interface{}:
			parts := make([]string, 0, len(val))
			for _, item := range val {
				parts = append(parts, fmt.Sprintf("%v", item))
			}
			printField(k, strings.Join(parts, ", "))
		default:
			printField(k, fmt.Sprintf("%v", val))
		}
	}
}

func cmdGuide(args []string) {
	fs := flag.NewFlagSet("guide", flag.ExitOnError)
	token := fs.String("token", "", "access token for private channels")
	fs.StringVar(token, "t", "", "alias for --token")
	noVerify := fs.Bool("no-verify", false, "skip signature verification")
	fs.BoolVar(noVerify, "V", false, "alias for --no-verify")
	xmltv := fs.Bool("xmltv", false, "output as XMLTV XML")
	fs.BoolVar(xmltv, "x", false, "alias for --xmltv")
	watch, interval := addWatchFlags(fs)
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Fetch and verify a channel guide\n\n")
		fmt.Fprintf(os.Stderr, "Usage: tltv guide <target>\n\n")
		fmt.Fprintf(os.Stderr, "Target can be a tltv:// URI or compact ID@host format:\n")
		fmt.Fprintf(os.Stderr, "  tltv guide \"tltv://TVMkVH...@example.com:443\"\n")
		fmt.Fprintf(os.Stderr, "  tltv guide TVMkVH...@example.com\n\n")
		fmt.Fprintf(os.Stderr, "Flags:\n")
		fmt.Fprintf(os.Stderr, "  -t, --token string    access token for private channels\n")
		fmt.Fprintf(os.Stderr, "  -V, --no-verify       skip signature verification\n")
		fmt.Fprintf(os.Stderr, "  -x, --xmltv           output as XMLTV XML\n")
		fmt.Fprintf(os.Stderr, "  -w, --watch           auto-refresh output\n")
		fmt.Fprintf(os.Stderr, "      --interval int    refresh interval in seconds (default 2)\n")
	}
	fs.Parse(args)

	if fs.NArg() < 1 {
		fs.Usage()
		os.Exit(1)
	}

	// Token: flag overrides URI-embedded token
	if *token == "" {
		*token = extractToken(fs.Arg(0))
	}

	client := newClient(flagInsecure)
	channelID, host, err := parseTargetOrDiscover(fs.Arg(0), client)
	if err != nil {
		fatal("%v", err)
	}

	watchLoop(*watch, *interval, func() {
		doc, err := client.FetchGuide(host, channelID, *token)
		if err != nil {
			fatal("%v", err)
		}

		var sigErr error
		if !*noVerify {
			sigErr = verifyDocument(doc, channelID)
		}

		// XMLTV output
		if *xmltv {
			if sigErr != nil {
				fmt.Fprintf(os.Stderr, "warning: signature verification failed: %s\n", sigErr.Error())
			}
			outputXMLTV(channelID, doc)
			if sigErr != nil {
				os.Exit(1)
			}
			return
		}

		if flagJSON {
			result := map[string]interface{}{
				"channel_id": channelID,
				"host":       host,
				"verified":   !*noVerify && sigErr == nil,
				"document":   doc,
			}
			if sigErr != nil {
				result["verification_error"] = sigErr.Error()
			}
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			enc.Encode(result)
			if sigErr != nil {
				os.Exit(1)
			}
			return
		}

		printHeader("Guide")
		if !*noVerify {
			if sigErr != nil {
				printField("Verified", c(cRed, "✗ ")+sigErr.Error())
			} else {
				printField("Verified", c(cGreen, "✓")+" Signature valid")
			}
		}
		printField("From", getString(doc, "from"))
		printField("Until", getString(doc, "until"))
		entries, _ := doc["entries"].([]interface{})
		printField("Entries", fmt.Sprintf("%d", len(entries)))
		printField("Updated", getString(doc, "updated"))
		printField("Seq", getString(doc, "seq"))

		if len(entries) > 0 {
			now := time.Now().UTC()
			fmt.Println()
			var rows [][]string
			for _, e := range entries {
				entry, _ := e.(map[string]interface{})
				if entry == nil {
					continue
				}
				startStr := getString(entry, "start")
				endStr := getString(entry, "end")
				title := getString(entry, "title")
				cat := getString(entry, "category")

				startT, startErr := time.Parse("2006-01-02T15:04:05Z", startStr)
				endT, endErr := time.Parse("2006-01-02T15:04:05Z", endStr)
				nowPlaying := startErr == nil && endErr == nil && !now.Before(startT) && now.Before(endT)

				start := startStr
				end := endStr
				if startErr == nil {
					start = startT.Format("Jan 02 15:04")
				}
				if endErr == nil {
					end = endT.Format("15:04")
				}

				marker := " "
				if nowPlaying {
					marker = ">"
					if useColor {
						marker = c(cGreen, ">")
					}
				}
				timeRange := marker + " " + start + " - " + end
				if rf := getString(entry, "relay_from"); rf != "" {
					title += " [relay: " + rf + "]"
				}
				rows = append(rows, []string{timeRange, title, cat})
			}
			printTable([]string{"  Time", "Title", "Category"}, rows)
		}
		fmt.Println()

		if sigErr != nil && !*watch {
			os.Exit(1)
		}
	})
}

func cmdPeers(args []string) {
	fs := flag.NewFlagSet("peers", flag.ExitOnError)
	watch, interval := addWatchFlags(fs)
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "List peers from a TLTV node\n\n")
		fmt.Fprintf(os.Stderr, "Usage: tltv peers <host[:port]>\n\n")
		fmt.Fprintf(os.Stderr, "Flags:\n")
		fmt.Fprintf(os.Stderr, "  -w, --watch           auto-refresh output\n")
		fmt.Fprintf(os.Stderr, "      --interval int    refresh interval in seconds (default 2)\n")
	}
	fs.Parse(args)

	if fs.NArg() < 1 {
		fs.Usage()
		os.Exit(1)
	}

	host := normalizeHost(fs.Arg(0))
	client := newClient(flagInsecure)

	watchLoop(*watch, *interval, func() {
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
				rows = append(rows, []string{p.ID, p.Name, hints})
			}
			printTable([]string{"ID", "Name", "Hints"}, rows)
		}
		fmt.Println()
	})
}

func cmdStream(args []string) {
	fs := flag.NewFlagSet("stream", flag.ExitOnError)
	token := fs.String("token", "", "access token for private channels")
	fs.StringVar(token, "t", "", "alias for --token")
	urlOnly := fs.Bool("url", false, "print only the stream URL")
	fs.BoolVar(urlOnly, "u", false, "alias for --url")
	watch, interval := addWatchFlags(fs)
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Check stream status and manifest info\n\n")
		fmt.Fprintf(os.Stderr, "Usage: tltv stream <target>\n\n")
		fmt.Fprintf(os.Stderr, "Target can be a tltv:// URI or compact ID@host format:\n")
		fmt.Fprintf(os.Stderr, "  tltv stream \"tltv://TVMkVH...@example.com:443\"\n")
		fmt.Fprintf(os.Stderr, "  tltv stream TVMkVH...@example.com\n\n")
		fmt.Fprintf(os.Stderr, "Flags:\n")
		fmt.Fprintf(os.Stderr, "  -t, --token string    access token for private channels\n")
		fmt.Fprintf(os.Stderr, "  -u, --url             print only the stream URL\n")
		fmt.Fprintf(os.Stderr, "  -w, --watch           auto-refresh output\n")
		fmt.Fprintf(os.Stderr, "      --interval int    refresh interval in seconds (default 2)\n")
	}
	fs.Parse(args)

	if fs.NArg() < 1 {
		fs.Usage()
		os.Exit(1)
	}

	// Token: flag overrides URI-embedded token
	if *token == "" {
		*token = extractToken(fs.Arg(0))
	}

	client := newClient(flagInsecure)
	channelID, host, err := parseTargetOrDiscover(fs.Arg(0), client)
	if err != nil {
		fatal("%v", err)
	}

	streamURL := client.baseURL(host) + "/tltv/v1/channels/" + channelID + "/stream.m3u8"
	if *token != "" {
		streamURL += "?token=" + *token
	}

	if *urlOnly {
		fmt.Println(streamURL)
		return
	}

	watchLoop(*watch, *interval, func() {
		status, contentType, body, err := client.CheckStream(host, channelID, *token)
		if err != nil {
			fatal("stream check failed: %v", err)
		}

		var segments int
		var targetDuration, mediaSequence string
		if status == 200 {
			segments, targetDuration, mediaSequence = parseManifestFields(body)
		}

		if flagJSON {
			result := map[string]interface{}{
				"status":       status,
				"content_type": contentType,
				"available":    status == 200,
				"stream_url":   streamURL,
			}
			if status == 200 {
				result["segments"] = segments
				if targetDuration != "" {
					result["target_duration"] = targetDuration
				}
				if mediaSequence != "" {
					result["media_sequence"] = mediaSequence
				}
			}
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			enc.Encode(result)
			return
		}

		printHeader("Stream")

		switch status {
		case 200:
			printField("Status", c(cGreen, "✓")+" live")
			printField("URL", streamURL)
			printField("Content-Type", contentType)
			printField("Segments", fmt.Sprintf("%d", segments))
			if targetDuration != "" {
				printField("Target Duration", targetDuration+"s")
			}
			if mediaSequence != "" {
				printField("Media Sequence", mediaSequence)
			}

		case 302:
			printField("Status", c(cGreen, "✓")+" live (redirect)")
			printField("URL", streamURL)

		case 403:
			printField("Status", c(cRed, "✗")+" access denied (token required)")
			printField("URL", streamURL)

		case 404:
			printField("Status", c(cRed, "✗")+" channel not found")
			printField("URL", streamURL)

		case 503:
			printField("Status", c(cYellow, "!")+" unavailable (on-demand idle)")
			printField("URL", streamURL)

		default:
			printField("Status", c(cRed, "✗")+" HTTP "+fmt.Sprintf("%d", status))
			printField("URL", streamURL)
		}
		fmt.Println()
	})
}

func cmdCrawl(args []string) {
	fs := flag.NewFlagSet("crawl", flag.ExitOnError)
	depth := fs.Int("depth", 2, "maximum crawl depth")
	fs.IntVar(depth, "d", 2, "alias for --depth")
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Crawl the peer gossip network to discover channels\n\n")
		fmt.Fprintf(os.Stderr, "Usage: tltv crawl <host[:port]>\n\n")
		fmt.Fprintf(os.Stderr, "Flags:\n")
		fmt.Fprintf(os.Stderr, "  -d, --depth int       maximum crawl depth (default 2)\n")
	}
	fs.Parse(args)

	if fs.NArg() < 1 {
		fs.Usage()
		os.Exit(1)
	}

	startHost := normalizeHost(fs.Arg(0))

	// Use SSRF-safe client for crawling untrusted peer hints
	var client *Client
	if flagLocal {
		client = newClient(flagInsecure)
	} else {
		client = newSSRFSafeClient(flagInsecure)
	}

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

	if !flagJSON {
		fmt.Printf("Crawling from %s (max depth %d)...\n\n", startHost, *depth)
	}

	nodesProbed := 0

	for len(queue) > 0 {
		target := queue[0]
		queue = queue[1:]

		if visited[target.host] || target.depth > *depth {
			continue
		}
		visited[target.host] = true

		// Fetch node info
		if !flagJSON {
			fmt.Printf("  Probing %s...", target.host)
		}
		info, err := client.FetchNodeInfo(target.host)
		if err != nil {
			if !flagJSON {
				fmt.Printf(" %s\n", c(cRed, "error: "+err.Error()))
			}
			continue
		}

		nodesProbed++
		if !flagJSON {
			fmt.Printf(" %d ch, %d relay", len(info.Channels), len(info.Relaying))
		}

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
			if !flagJSON {
				fmt.Printf(", no peers\n")
			}
			continue
		}

		if !flagJSON {
			fmt.Printf(", %d peers\n", len(exchange.Peers))
		}

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
			// Add validated peer hints to crawl queue
			for _, hint := range p.Hints {
				normalized, err := normalizeHint(hint)
				if err != nil {
					continue // silently skip malformed hints from peer exchange
				}
				if !visited[normalized] && (flagLocal || !isLocalAddress(normalized)) {
					queue = append(queue, crawlTarget{host: normalized, depth: target.depth + 1})
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
				ch.ID,
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
	fs.StringVar(token, "t", "", "alias for --token")
	noVerify := fs.Bool("no-verify", false, "skip signature verification")
	fs.BoolVar(noVerify, "V", false, "alias for --no-verify")
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Resolve a tltv:// URI end-to-end\n\n")
		fmt.Fprintf(os.Stderr, "Usage: tltv resolve <uri>\n\n")
		fmt.Fprintf(os.Stderr, "Parses the URI, tries each hint, fetches and verifies metadata,\n")
		fmt.Fprintf(os.Stderr, "and checks stream availability.\n\n")
		fmt.Fprintf(os.Stderr, "Examples:\n")
		fmt.Fprintf(os.Stderr, "  tltv resolve \"tltv://TVMkVH...@example.com:443\"\n")
		fmt.Fprintf(os.Stderr, "  tltv resolve \"tltv://TVMkVH...?via=relay1.com:443,relay2.com:443\"\n\n")
		fmt.Fprintf(os.Stderr, "Flags:\n")
		fmt.Fprintf(os.Stderr, "  -t, --token string    access token (overrides URI token)\n")
		fmt.Fprintf(os.Stderr, "  -V, --no-verify       skip signature verification\n")
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

	// Use SSRF-safe client for untrusted URI hints
	var client *Client
	if flagLocal {
		client = newClient(flagInsecure)
	} else {
		client = newSSRFSafeClient(flagInsecure)
	}

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
	var resolvedOC *originCheck
	var streamStatus int
	var streamContentType string

	for i, hint := range uri.Hints {
		// Validate hint structure (reject URLs, userinfo, paths)
		host, err := normalizeHint(hint)
		if err != nil {
			if !flagJSON {
				label := fmt.Sprintf("Hint %d", i+1)
				fmt.Printf("  %s  %s ... %s\n", c(cDim, label), hint, c(cRed, "rejected: "+err.Error()))
			}
			continue
		}

		// Filter local/private addresses unless --local (spec section 3.1)
		if !flagLocal && isLocalAddress(host) {
			if !flagJSON {
				label := fmt.Sprintf("Hint %d", i+1)
				fmt.Printf("  %s  %s ... %s\n", c(cDim, label), host, c(cYellow, "skipped (local address, use --local)"))
			}
			continue
		}

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

		// Check origin status from signed metadata (§11)
		resolvedOC = checkOrigin(doc, host)
		if !flagJSON {
			if resolvedOC != nil && resolvedOC.HasOrigins {
				if resolvedOC.IsOrigin {
					fmt.Printf(" ... %s %s\n", c(cGreen, "verified"), c(cDim, "(origin)"))
				} else {
					originHint := ""
					if len(resolvedOC.Origins) > 0 {
						originHint = " — origin is " + resolvedOC.Origins[0]
					}
					fmt.Printf(" ... %s %s\n", c(cGreen, "verified"), c(cYellow, "(relay"+originHint+")"))
				}
			} else {
				fmt.Printf(" ... %s\n", c(cGreen, "verified"))
			}
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

	// Step 5: Follow migration chain (max 5 hops, per spec section 5.14)
	finalChannelID := uri.ChannelID
	visited := map[string]bool{uri.ChannelID: true}
	var migrationErr error

	for hop := 0; hop < 5; hop++ {
		dt, _ := metadata["type"].(string)
		if dt != "migration" {
			break
		}

		toID, _ := metadata["to"].(string)
		if toID == "" {
			migrationErr = fmt.Errorf("migration document missing 'to' field")
			break
		}

		// Detect loops
		if visited[toID] {
			migrationErr = fmt.Errorf("migration loop detected at %s", toID)
			break
		}
		visited[toID] = true

		if !flagJSON {
			printWarn(fmt.Sprintf("Migrated -> %s (hop %d)", toID, hop+1))
		}

		newDoc, err := client.FetchMetadata(resolvedHost, toID, tok)
		if err != nil {
			migrationErr = fmt.Errorf("could not follow migration to %s: %v", toID, err)
			break
		}

		if !*noVerify {
			newDocType, _ := newDoc["type"].(string)
			if newDocType == "migration" {
				err = verifyMigration(newDoc, toID)
			} else {
				err = verifyDocument(newDoc, toID)
			}
			if err != nil {
				migrationErr = fmt.Errorf("migration target %s verification failed: %v", toID, err)
				break
			}
		}

		finalChannelID = toID
		metadata = newDoc
	}

	// Check if still stuck on a migration document (exceeded 5 hops)
	if migrationErr == nil {
		if dt, _ := metadata["type"].(string); dt == "migration" {
			migrationErr = fmt.Errorf("migration chain exceeded maximum of 5 hops")
		}
	}

	// Fail clearly if migration chain is broken
	if migrationErr != nil {
		if flagJSON {
			result := map[string]interface{}{
				"error":         migrationErr.Error(),
				"channel_id":    uri.ChannelID,
				"resolved_host": resolvedHost,
			}
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			enc.Encode(result)
			os.Exit(1)
		}
		printFail(migrationErr.Error())
		fmt.Println()
		os.Exit(1)
	}

	// Step 6: Check stream
	docType, _ := metadata["type"].(string)
	if docType != "migration" {
		streamStatus, streamContentType, _, _ = client.CheckStream(resolvedHost, finalChannelID, tok)
	}

	// JSON output
	if flagJSON {
		result := map[string]interface{}{
			"channel_id":    finalChannelID,
			"resolved_host": resolvedHost,
			"verified":      !*noVerify,
			"document":      metadata,
		}
		if finalChannelID != uri.ChannelID {
			result["migrated_from"] = uri.ChannelID
		}
		if resolvedOC != nil && resolvedOC.HasOrigins {
			result["verified_origin"] = resolvedOC.IsOrigin
			result["signed_origins"] = resolvedOC.Origins
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

// ---------- XMLTV ----------

// outputXMLTV writes a guide document as XMLTV XML to stdout.
func outputXMLTV(channelID string, guide map[string]interface{}) {
	fmt.Println(`<?xml version="1.0" encoding="UTF-8"?>`)
	fmt.Printf("<tv generator-info-name=\"tltv-cli/%s\">\n", version)

	fmt.Printf("  <channel id=\"%s\">\n", xmlEscape(channelID))
	fmt.Printf("    <display-name>%s</display-name>\n", xmlEscape(channelID))
	fmt.Println("  </channel>")

	entries, _ := guide["entries"].([]interface{})
	for _, e := range entries {
		entry, _ := e.(map[string]interface{})
		if entry == nil {
			continue
		}

		start := isoToXMLTV(getString(entry, "start"))
		stop := isoToXMLTV(getString(entry, "end"))

		fmt.Printf("  <programme start=\"%s\" stop=\"%s\" channel=\"%s\">\n",
			start, stop, xmlEscape(channelID))
		fmt.Printf("    <title>%s</title>\n", xmlEscape(getString(entry, "title")))
		if desc := getString(entry, "description"); desc != "" {
			fmt.Printf("    <desc>%s</desc>\n", xmlEscape(desc))
		}
		if cat := getString(entry, "category"); cat != "" {
			fmt.Printf("    <category>%s</category>\n", xmlEscape(cat))
		}
		if rf := getString(entry, "relay_from"); rf != "" {
			fmt.Printf("    <previously-shown channel=\"%s\" />\n", xmlEscape(rf))
		}
		fmt.Println("  </programme>")
	}

	fmt.Println("</tv>")
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
