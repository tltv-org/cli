package main

import (
	"fmt"
	"strings"
)

// TLTVUri represents a parsed tltv:// URI.
type TLTVUri struct {
	ChannelID string
	Hints     []string
	Token     string
}

const tltvScheme = "tltv://"

// parseTLTVUri parses a tltv:// URI string.
func parseTLTVUri(uri string) (*TLTVUri, error) {
	if !strings.HasPrefix(uri, tltvScheme) {
		return nil, fmt.Errorf("expected tltv:// scheme")
	}

	rest := uri[len(tltvScheme):]
	if rest == "" {
		return nil, fmt.Errorf("missing channel ID")
	}

	result := &TLTVUri{}

	// Split off query string
	var query string
	if qIdx := strings.IndexByte(rest, '?'); qIdx >= 0 {
		query = rest[qIdx+1:]
		rest = rest[:qIdx]
	}

	// Split off @ hint
	if atIdx := strings.IndexByte(rest, '@'); atIdx >= 0 {
		result.ChannelID = rest[:atIdx]
		atHint := rest[atIdx+1:]
		if atHint != "" {
			result.Hints = append(result.Hints, atHint)
		}
	} else {
		result.ChannelID = rest
	}

	if result.ChannelID == "" {
		return nil, fmt.Errorf("missing channel ID")
	}

	// Parse query parameters
	if query != "" {
		params := parseQuery(query)

		// Token — first occurrence
		if tok, ok := params["token"]; ok && len(tok) > 0 {
			result.Token = tok[0]
		}

		// Via hints — first occurrence, comma-separated
		if via, ok := params["via"]; ok && len(via) > 0 {
			for _, v := range strings.Split(via[0], ",") {
				v = strings.TrimSpace(v)
				if v != "" {
					result.Hints = append(result.Hints, v)
				}
			}
		}
	}

	return result, nil
}

// parseQuery is a minimal query string parser (avoids net/url for channel ID safety).
// Returns first occurrence for each key.
func parseQuery(q string) map[string][]string {
	params := make(map[string][]string)
	for _, pair := range strings.Split(q, "&") {
		if pair == "" {
			continue
		}
		k, v, _ := strings.Cut(pair, "=")
		params[k] = append(params[k], v)
	}
	return params
}

// formatTLTVUri builds a tltv:// URI string.
func formatTLTVUri(channelID string, hints []string, token string) string {
	var sb strings.Builder
	sb.WriteString(tltvScheme)
	sb.WriteString(channelID)

	var queryParts []string

	if token != "" {
		queryParts = append(queryParts, "token="+token)
	}
	if len(hints) > 0 {
		queryParts = append(queryParts, "via="+strings.Join(hints, ","))
	}

	if len(queryParts) > 0 {
		sb.WriteByte('?')
		sb.WriteString(strings.Join(queryParts, "&"))
	}

	return sb.String()
}
