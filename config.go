package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"
)

// ---------- Config Loading ----------

// loadDaemonConfig reads a JSON config file and returns a generic map.
// Uses json.Decoder with UseNumber() to preserve number types.
// Unknown fields are silently ignored (forward compatibility).
func loadDaemonConfig(path string) (map[string]interface{}, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cfg map[string]interface{}
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.UseNumber()
	if err := dec.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	return cfg, nil
}

// ---------- Config → Flag Application ----------

// configKeyToFlag converts a config key (underscores) to a flag name (dashes).
// Example: "cache_max_entries" → "cache-max-entries"
func configKeyToFlag(key string) string {
	return strings.ReplaceAll(key, "_", "-")
}

// applyConfigToFlags applies scalar config values to flags that weren't explicitly
// set on the command line. Handles string, number, and bool values. Complex types
// (arrays, objects) are skipped — those are handled per-daemon (channels, guide, etc.).
// Returns the list of applied config keys.
func applyConfigToFlags(fs *flag.FlagSet, cfg map[string]interface{}) []string {
	// Build set of explicitly-set flags (including aliases).
	// When a user sets -k, Visit includes only "-k" but not "--key".
	// We detect aliases by marking all flags that share the same default
	// AND current value as any explicitly-set flag.
	explicit := make(map[string]bool)
	fs.Visit(func(f *flag.Flag) {
		explicit[f.Name] = true
		val := f.Value.String()
		def := f.DefValue
		fs.VisitAll(func(af *flag.Flag) {
			if af.DefValue == def && af.Value.String() == val {
				explicit[af.Name] = true
			}
		})
	})

	var applied []string
	for key, val := range cfg {
		// Try both underscore (config) and dash (flag) forms
		flagName := configKeyToFlag(key)
		f := fs.Lookup(flagName)
		if f == nil {
			f = fs.Lookup(key)
			if f == nil {
				continue
			}
			flagName = key
		}

		if explicit[flagName] {
			continue
		}

		var strVal string
		switch v := val.(type) {
		case string:
			strVal = v
		case json.Number:
			strVal = v.String()
		case bool:
			strVal = strconv.FormatBool(v)
		default:
			continue // skip arrays, objects (handled per-daemon)
		}

		if err := f.Value.Set(strVal); err == nil {
			applied = append(applied, key)
		}
	}
	return applied
}

// ---------- Config Field Extraction ----------

// configGetString extracts a string from config, returning ("", false) if missing or wrong type.
func configGetString(cfg map[string]interface{}, key string) (string, bool) {
	v, ok := cfg[key]
	if !ok {
		return "", false
	}
	s, ok := v.(string)
	return s, ok
}

// configGetBool extracts a bool from config.
func configGetBool(cfg map[string]interface{}, key string) (bool, bool) {
	v, ok := cfg[key]
	if !ok {
		return false, false
	}
	b, ok := v.(bool)
	return b, ok
}

// configGetInt extracts an int from config. Accepts json.Number or float64.
func configGetInt(cfg map[string]interface{}, key string) (int, bool) {
	v, ok := cfg[key]
	if !ok {
		return 0, false
	}
	switch n := v.(type) {
	case json.Number:
		i, err := n.Int64()
		if err != nil {
			return 0, false
		}
		return int(i), true
	case float64:
		return int(n), true
	}
	return 0, false
}

// configGetStringSlice extracts a []string from config (JSON array of strings).
func configGetStringSlice(cfg map[string]interface{}, key string) ([]string, bool) {
	v, ok := cfg[key]
	if !ok {
		return nil, false
	}
	arr, ok := v.([]interface{})
	if !ok {
		// Also accept a single string as a one-element slice
		if s, ok := v.(string); ok {
			return []string{s}, true
		}
		return nil, false
	}
	var result []string
	for _, item := range arr {
		if s, ok := item.(string); ok {
			result = append(result, s)
		}
	}
	return result, true
}

// ---------- Polymorphic Guide ----------

// parseGuideConfig handles the polymorphic guide field in config.
//
// Three forms:
//
//	"guide": {"entries": [...]}  → inline guide entries (entries populated, filePath empty)
//	"guide": "guide.json"        → file reference (entries nil, filePath populated)
//	omitted or null              → use default (both nil/empty)
func parseGuideConfig(v interface{}) (entries []guideEntry, filePath string, err error) {
	if v == nil {
		return nil, "", nil
	}

	// String → file path
	if s, ok := v.(string); ok {
		return nil, s, nil
	}

	// Object with "entries" → inline entries
	obj, ok := v.(map[string]interface{})
	if !ok {
		return nil, "", fmt.Errorf("guide must be a string (file path) or object with entries")
	}

	entriesRaw, ok := obj["entries"]
	if !ok {
		return nil, "", fmt.Errorf("guide object must have an 'entries' field")
	}

	arr, ok := entriesRaw.([]interface{})
	if !ok {
		return nil, "", fmt.Errorf("guide entries must be an array")
	}

	for _, item := range arr {
		entryMap, ok := item.(map[string]interface{})
		if !ok {
			continue
		}

		entry := guideEntry{}
		if s, ok := entryMap["start"].(string); ok {
			entry.Start = s
		}
		if s, ok := entryMap["end"].(string); ok {
			entry.End = s
		}
		if s, ok := entryMap["title"].(string); ok {
			entry.Title = s
		}
		if s, ok := entryMap["description"].(string); ok {
			entry.Description = s
		}
		if s, ok := entryMap["category"].(string); ok {
			entry.Category = s
		}

		if entry.Start == "" || entry.End == "" || entry.Title == "" {
			continue // skip incomplete entries
		}
		entries = append(entries, entry)
	}

	return entries, "", nil
}

// ---------- Config Watcher ----------

// configWatcher tracks a config file's mtime for hot-reload.
// Uses os.Stat (no inotify, no platform-specific code). One stat per check.
// If the file disappears or is unreadable, returns false (fail-safe: keep current config).
type configWatcher struct {
	path    string
	lastMod time.Time
}

// newConfigWatcher creates a watcher, recording current mtime.
func newConfigWatcher(path string) *configWatcher {
	w := &configWatcher{path: path}
	if info, err := os.Stat(path); err == nil {
		w.lastMod = info.ModTime()
	}
	return w
}

// Changed returns true if the file's mtime has advanced since last check.
// Returns false on errors (file disappeared, permission denied) — fail-safe.
func (w *configWatcher) Changed() bool {
	info, err := os.Stat(w.path)
	if err != nil {
		return false
	}
	if info.ModTime().After(w.lastMod) {
		w.lastMod = info.ModTime()
		return true
	}
	return false
}

// ---------- Config Reload Loop ----------

// configReloadLoop watches a config file and calls reloadFn when it changes.
// Runs until ctx is cancelled. Check interval: 30 seconds.
// Each daemon provides its own reloadFn closure.
func configReloadLoop(ctx context.Context, watcher *configWatcher, reloadFn func(map[string]interface{})) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if !watcher.Changed() {
				continue
			}
			cfg, err := loadDaemonConfig(watcher.path)
			if err != nil {
				logErrorf("config reload failed: %v (keeping current)", err)
				continue
			}
			reloadFn(cfg)
		}
	}
}

// ---------- Config Dump ----------

// dumpDaemonConfig writes a config map as indented JSON to w.
// Zero/nil/empty values are omitted for a clean output.
func dumpDaemonConfig(cfg map[string]interface{}, w io.Writer) error {
	out := make(map[string]interface{})
	for k, v := range cfg {
		if v == nil {
			continue
		}
		switch val := v.(type) {
		case string:
			if val == "" {
				continue
			}
		case bool:
			if !val {
				continue
			}
		case int:
			if val == 0 {
				continue
			}
		case []string:
			if len(val) == 0 {
				continue
			}
			// Convert to []interface{} for json.Encoder
			arr := make([]interface{}, len(val))
			for i, s := range val {
				arr[i] = s
			}
			out[k] = arr
			continue
		case []guideEntry:
			if len(val) == 0 {
				continue
			}
			// Wrap in {"entries": [...]} for polymorphic guide format
			var entries []interface{}
			for _, e := range val {
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
				entries = append(entries, entry)
			}
			out[k] = map[string]interface{}{"entries": entries}
			continue
		}
		out[k] = v
	}

	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	return enc.Encode(out)
}
