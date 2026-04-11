package main

import (
	"fmt"
)

// ---------- Metadata Fetching ----------

// fetchResult holds the result of fetching upstream metadata.
type fetchResult struct {
	Raw         []byte                 // exact bytes from upstream (served verbatim)
	Doc         map[string]interface{} // parsed document (for field extraction)
	IsMigration bool
	MigratedTo  string // only if IsMigration
}

// fetchAndVerifyMetadata fetches channel metadata from upstream hints,
// verifies the signature, and returns both raw bytes and parsed document.
// Tries hints in order, returns first success.
func fetchAndVerifyMetadata(client *Client, channelID string, hints []string) (*fetchResult, error) {
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
			return &fetchResult{
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

		return &fetchResult{Raw: body, Doc: doc}, nil
	}

	if lastErr != nil {
		return nil, lastErr
	}
	return nil, fmt.Errorf("no hints available for %s", channelID)
}

// relayFollowMigration follows a migration chain up to maxHops.
// Returns the final channel ID and verified metadata, or an error.
func relayFollowMigration(client *Client, startID string, hints []string, maxHops int) (finalID string, result *fetchResult, err error) {
	seen := map[string]bool{startID: true}
	currentID := startID

	for hop := 0; hop < maxHops; hop++ {
		res, err := fetchAndVerifyMetadata(client, currentID, hints)
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
func relayFetchAndVerifyGuide(client *Client, channelID string, hints []string) (raw []byte, entries []guideEntry, err error) {
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

		parsed := extractGuideEntries(doc)
		return body, parsed, nil
	}

	if lastErr != nil {
		return nil, nil, lastErr
	}
	return nil, nil, nil
}

// ---------- Access Checks ----------

// checkChannelAccess verifies that metadata allows relaying.
// Returns an error describing why relaying is not permitted.
// Per spec §5.2: any access value other than "public" is not relayable.
// Per spec §5.13: any status value other than "active" (or absent) is not relayable.
func checkChannelAccess(doc map[string]interface{}) error {
	access, _ := doc["access"].(string)
	if access == "" {
		access = "public"
	}
	if access != "public" {
		return fmt.Errorf("channel not relayable (access=%s)", access)
	}
	if onDemand, ok := doc["on_demand"].(bool); ok && onDemand {
		return fmt.Errorf("on-demand channel")
	}
	status, _ := doc["status"].(string)
	if status == "" {
		status = "active"
	}
	if status != "active" {
		return fmt.Errorf("channel not relayable (status=%s)", status)
	}
	return nil
}

// ---------- Helpers ----------

// relayIsMigration checks if a document is a migration document.
func relayIsMigration(doc map[string]interface{}) bool {
	docType, _ := doc["type"].(string)
	return docType == "migration"
}

// extractGuideEntries parses guide entries from a verified guide document.
func extractGuideEntries(doc map[string]interface{}) []guideEntry {
	entriesRaw, ok := doc["entries"].([]interface{})
	if !ok {
		return nil
	}

	var entries []guideEntry
	for _, raw := range entriesRaw {
		e, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}
		entry := guideEntry{
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
		if rf, ok := e["relay_from"].(string); ok {
			entry.RelayFrom = rf
		}
		entries = append(entries, entry)
	}
	return entries
}

// ---------- Config File ----------

// relayTarget is a channel to relay with its upstream hints.
type relayTarget struct {
	ChannelID string
	Hints     []string
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
