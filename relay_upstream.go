package main

import (
	"encoding/json"
	"fmt"
	"os"
)

// ---------- Metadata Fetching ----------

// relayFetchResult holds the result of fetching upstream metadata.
type relayFetchResult struct {
	Raw         []byte                 // exact bytes from upstream (served verbatim)
	Doc         map[string]interface{} // parsed document (for field extraction)
	IsMigration bool
	MigratedTo  string // only if IsMigration
}

// relayFetchAndVerifyMetadata fetches channel metadata from upstream hints,
// verifies the signature, and returns both raw bytes and parsed document.
// Tries hints in order, returns first success.
func relayFetchAndVerifyMetadata(client *Client, channelID string, hints []string) (*relayFetchResult, error) {
	var lastErr error

	for _, hint := range hints {
		url := client.baseURL(hint) + "/tltv/v1/channels/" + channelID
		body, status, err := client.get(url)
		if err != nil {
			lastErr = fmt.Errorf("hint %s: %w", hint, err)
			continue
		}
		if status != 200 {
			lastErr = fmt.Errorf("hint %s: HTTP %d", hint, status)
			continue
		}

		// Parse with UseNumber for verification
		doc, err := readDocumentFromString(string(body))
		if err != nil {
			lastErr = fmt.Errorf("hint %s: %w", hint, err)
			continue
		}

		// Check if this is a migration document
		if relayIsMigration(doc) {
			if err := verifyMigration(doc, channelID); err != nil {
				lastErr = fmt.Errorf("hint %s: migration verification failed: %w", hint, err)
				continue
			}
			to, _ := doc["to"].(string)
			return &relayFetchResult{
				Raw:         body,
				Doc:         doc,
				IsMigration: true,
				MigratedTo:  to,
			}, nil
		}

		// Verify metadata signature
		if err := verifyDocument(doc, channelID); err != nil {
			lastErr = fmt.Errorf("hint %s: verification failed: %w", hint, err)
			continue
		}

		return &relayFetchResult{Raw: body, Doc: doc}, nil
	}

	if lastErr != nil {
		return nil, lastErr
	}
	return nil, fmt.Errorf("no hints available for %s", channelID)
}

// relayFollowMigration follows a migration chain up to maxHops.
// Returns the final channel ID and verified metadata, or an error.
func relayFollowMigration(client *Client, startID string, hints []string, maxHops int) (finalID string, result *relayFetchResult, err error) {
	seen := map[string]bool{startID: true}
	currentID := startID

	for hop := 0; hop < maxHops; hop++ {
		res, err := relayFetchAndVerifyMetadata(client, currentID, hints)
		if err != nil {
			return "", nil, fmt.Errorf("hop %d (%s): %w", hop, currentID, err)
		}

		if !res.IsMigration {
			return currentID, res, nil
		}

		nextID := res.MigratedTo
		if nextID == "" {
			return "", nil, fmt.Errorf("hop %d (%s): migration has no 'to' field", hop, currentID)
		}
		if seen[nextID] {
			return "", nil, fmt.Errorf("migration loop detected: %s -> %s", currentID, nextID)
		}
		seen[nextID] = true
		currentID = nextID
	}

	return "", nil, fmt.Errorf("migration chain exceeded %d hops from %s", maxHops, startID)
}

// ---------- Guide Fetching ----------

// relayFetchAndVerifyGuide fetches a channel guide from upstream, verifies it,
// and returns raw bytes plus parsed entries (for XMLTV generation).
func relayFetchAndVerifyGuide(client *Client, channelID string, hints []string) (raw []byte, entries []bridgeGuideEntry, err error) {
	var lastErr error

	for _, hint := range hints {
		url := client.baseURL(hint) + "/tltv/v1/channels/" + channelID + "/guide.json"
		body, status, err := client.get(url)
		if err != nil {
			lastErr = fmt.Errorf("hint %s: %w", hint, err)
			continue
		}
		if status == 404 {
			return nil, nil, nil
		}
		if status != 200 {
			lastErr = fmt.Errorf("hint %s: HTTP %d", hint, status)
			continue
		}

		doc, err := readDocumentFromString(string(body))
		if err != nil {
			lastErr = fmt.Errorf("hint %s: %w", hint, err)
			continue
		}

		if err := verifyDocument(doc, channelID); err != nil {
			lastErr = fmt.Errorf("hint %s: guide verification failed: %w", hint, err)
			continue
		}

		parsed := relayExtractGuideEntries(doc)
		return body, parsed, nil
	}

	if lastErr != nil {
		return nil, nil, lastErr
	}
	return nil, nil, nil
}

// ---------- Access Checks ----------

// relayCheckAccess verifies that metadata allows relaying.
// Returns an error describing why relaying is not permitted.
func relayCheckAccess(doc map[string]interface{}) error {
	if access, _ := doc["access"].(string); access == "token" {
		return fmt.Errorf("private channel (access=token)")
	}
	if onDemand, ok := doc["on_demand"].(bool); ok && onDemand {
		return fmt.Errorf("on-demand channel")
	}
	if status, _ := doc["status"].(string); status == "retired" {
		return fmt.Errorf("retired channel")
	}
	return nil
}

// ---------- Helpers ----------

// relayIsMigration checks if a document is a migration document.
func relayIsMigration(doc map[string]interface{}) bool {
	docType, _ := doc["type"].(string)
	return docType == "migration"
}

// relayExtractGuideEntries parses guide entries from a verified guide document.
func relayExtractGuideEntries(doc map[string]interface{}) []bridgeGuideEntry {
	entriesRaw, ok := doc["entries"].([]interface{})
	if !ok {
		return nil
	}

	var entries []bridgeGuideEntry
	for _, raw := range entriesRaw {
		e, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}
		entry := bridgeGuideEntry{
			Start: getString(e, "start"),
			End:   getString(e, "end"),
			Title: getString(e, "title"),
		}
		if desc, ok := e["description"].(string); ok {
			entry.Description = desc
		}
		if cat, ok := e["category"].(string); ok {
			entry.Category = cat
		}
		entries = append(entries, entry)
	}
	return entries
}

// ---------- Config File ----------

// relayConfig is the JSON config file format for tltv relay.
type relayConfig struct {
	Channels []string `json:"channels"` // tltv:// URIs or id@host:port
	Nodes    []string `json:"nodes"`    // host:port to relay all public channels from
}

// relayTarget is a channel to relay with its upstream hints.
type relayTarget struct {
	ChannelID string
	Hints     []string
}

// relayLoadConfig loads a relay config file.
func relayLoadConfig(path string) (*relayConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cfg relayConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing config: %w", err)
	}
	return &cfg, nil
}

// relayDiscoverTargets resolves all configured sources into relay targets.
// Deduplicates by channel ID, merging hints.
func relayDiscoverTargets(client *Client, channels, nodes []string) ([]relayTarget, error) {
	targetMap := make(map[string]*relayTarget)

	for _, ch := range channels {
		channelID, host, err := parseTargetOrDiscover(ch, client)
		if err != nil {
			return nil, fmt.Errorf("invalid channel %q: %w", ch, err)
		}
		host = normalizeHost(host)
		if t, ok := targetMap[channelID]; ok {
			t.Hints = appendUnique(t.Hints, host)
		} else {
			targetMap[channelID] = &relayTarget{ChannelID: channelID, Hints: []string{host}}
		}
	}

	for _, node := range nodes {
		node = normalizeHost(node)
		info, err := client.FetchNodeInfo(node)
		if err != nil {
			return nil, fmt.Errorf("node %s: %w", node, err)
		}
		var refs []ChannelRef
		refs = append(refs, info.Channels...)
		refs = append(refs, info.Relaying...)
		for _, ref := range refs {
			if t, ok := targetMap[ref.ID]; ok {
				t.Hints = appendUnique(t.Hints, node)
			} else {
				targetMap[ref.ID] = &relayTarget{ChannelID: ref.ID, Hints: []string{node}}
			}
		}
	}

	targets := make([]relayTarget, 0, len(targetMap))
	for _, t := range targetMap {
		targets = append(targets, *t)
	}
	return targets, nil
}

// appendUnique appends s to slice if not already present.
func appendUnique(slice []string, s string) []string {
	for _, v := range slice {
		if v == s {
			return slice
		}
	}
	return append(slice, s)
}
