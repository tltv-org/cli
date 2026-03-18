package main

import (
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
)

// maxClockSkew is the maximum allowed future timestamp (spec section 7.2).
const maxClockSkew = 3600 // seconds

// maxDocSize is the maximum allowed signed document size (spec section 5.6).
const maxDocSize = 65536 // 64 KB

// timestampFormat is the required format for all timestamps in signed documents (spec section 6.4).
const timestampFormat = "2006-01-02T15:04:05Z"

// ---------- JCS (RFC 8785) Canonical JSON ----------

// canonicalJSON produces RFC 8785 (JCS) canonical JSON bytes for a document.
// Sorts keys lexicographically, uses JCS string escaping (no HTML entity
// escaping for <, >, &), and formats numbers per ES6 Number.prototype.toString().
func canonicalJSON(doc map[string]interface{}) ([]byte, error) {
	var buf bytes.Buffer
	if err := jcsSerialize(&buf, doc); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// jcsSerialize recursively serializes a value per RFC 8785.
func jcsSerialize(buf *bytes.Buffer, v interface{}) error {
	switch val := v.(type) {
	case nil:
		buf.WriteString("null")
	case bool:
		if val {
			buf.WriteString("true")
		} else {
			buf.WriteString("false")
		}
	case json.Number:
		s, err := jcsFormatNumber(val)
		if err != nil {
			return err
		}
		buf.WriteString(s)
	case float64:
		s, err := jcsFormatFloat64(val)
		if err != nil {
			return err
		}
		buf.WriteString(s)
	case string:
		jcsWriteString(buf, val)
	case []interface{}:
		buf.WriteByte('[')
		for i, item := range val {
			if i > 0 {
				buf.WriteByte(',')
			}
			if err := jcsSerialize(buf, item); err != nil {
				return err
			}
		}
		buf.WriteByte(']')
	case map[string]interface{}:
		// JCS sorts keys by UTF-16 code unit order; for ASCII/BMP keys
		// this is equivalent to Go's default byte-order string sort.
		keys := make([]string, 0, len(val))
		for k := range val {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		buf.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				buf.WriteByte(',')
			}
			jcsWriteString(buf, k)
			buf.WriteByte(':')
			if err := jcsSerialize(buf, val[k]); err != nil {
				return err
			}
		}
		buf.WriteByte('}')
	default:
		return fmt.Errorf("unsupported JSON type: %T", v)
	}
	return nil
}

// jcsWriteString writes a JCS-compliant JSON string (RFC 8785 section 3.2.2.2).
// Only control characters (U+0000-U+001F), quotation mark, and reverse solidus
// are escaped. All other characters (including <, >, &, U+2028, U+2029) are
// written literally -- unlike Go's json.Marshal which HTML-escapes them.
func jcsWriteString(buf *bytes.Buffer, s string) {
	buf.WriteByte('"')
	for _, r := range s {
		switch {
		case r == '"':
			buf.WriteString(`\"`)
		case r == '\\':
			buf.WriteString(`\\`)
		case r == '\b':
			buf.WriteString(`\b`)
		case r == '\f':
			buf.WriteString(`\f`)
		case r == '\n':
			buf.WriteString(`\n`)
		case r == '\r':
			buf.WriteString(`\r`)
		case r == '\t':
			buf.WriteString(`\t`)
		case r < 0x20:
			fmt.Fprintf(buf, `\u%04x`, r)
		default:
			buf.WriteRune(r)
		}
	}
	buf.WriteByte('"')
}

// jcsFormatNumber formats a json.Number per JCS/ES6 Number.prototype.toString().
func jcsFormatNumber(n json.Number) (string, error) {
	f, err := n.Float64()
	if err != nil {
		return "", fmt.Errorf("invalid number %q: %w", n.String(), err)
	}
	return jcsFormatFloat64(f)
}

// jcsFormatFloat64 formats a float64 per ES6 Number.prototype.toString().
func jcsFormatFloat64(f float64) (string, error) {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return "", fmt.Errorf("NaN/Infinity not valid in JSON")
	}
	// Both +0 and -0 become "0"
	if f == 0 {
		return "0", nil
	}
	// Exact integers within safe range: fixed decimal, no exponent
	if f == math.Trunc(f) && math.Abs(f) < 1e21 {
		return strconv.FormatFloat(f, 'f', 0, 64), nil
	}
	// General case: ES6 shortest representation
	return es6FloatString(f), nil
}

// es6FloatString formats non-integer, non-zero floats per the ES6 spec
// (ECMA-262 Number::toString). Produces the shortest decimal string that
// uniquely identifies the IEEE 754 double value.
func es6FloatString(f float64) string {
	negative := f < 0
	if negative {
		f = -f
	}
	// Get shortest decimal representation in exponential form
	s := strconv.FormatFloat(f, 'e', -1, 64)
	idx := strings.IndexByte(s, 'e')
	mantissa := s[:idx]
	exp, _ := strconv.Atoi(s[idx+1:])

	// Extract raw digits (remove decimal point from mantissa)
	digits := strings.Replace(mantissa, ".", "", 1)
	k := len(digits)
	n := exp + 1 // total digits before the decimal point

	var result strings.Builder
	if negative {
		result.WriteByte('-')
	}
	switch {
	case k <= n && n <= 21:
		// Integer-like with trailing zeros: e.g. 100
		result.WriteString(digits)
		for i := 0; i < n-k; i++ {
			result.WriteByte('0')
		}
	case 0 < n && n < k:
		// Fixed with decimal in the middle: e.g. 12.34
		result.WriteString(digits[:n])
		result.WriteByte('.')
		result.WriteString(digits[n:])
	case -6 < n && n <= 0:
		// Leading zeros: e.g. 0.001
		result.WriteString("0.")
		for i := 0; i < -n; i++ {
			result.WriteByte('0')
		}
		result.WriteString(digits)
	case k == 1:
		// Single digit with exponent: e.g. 1e+25
		result.WriteByte(digits[0])
		result.WriteByte('e')
		if n-1 > 0 {
			result.WriteByte('+')
		}
		result.WriteString(strconv.Itoa(n - 1))
	default:
		// Multiple digits with exponent: e.g. 1.23e+25
		result.WriteByte(digits[0])
		result.WriteByte('.')
		result.WriteString(digits[1:])
		result.WriteByte('e')
		if n-1 > 0 {
			result.WriteByte('+')
		}
		result.WriteString(strconv.Itoa(n - 1))
	}
	return result.String()
}

// ---------- Signing & Verification ----------

// signDocument signs a TLTV document with an Ed25519 private key.
// Removes any existing signature, canonicalizes, signs, and adds the signature.
func signDocument(doc map[string]interface{}, privKey ed25519.PrivateKey) (map[string]interface{}, error) {
	// Remove existing signature
	clean := make(map[string]interface{})
	for k, v := range doc {
		if k != "signature" {
			clean[k] = v
		}
	}

	payload, err := canonicalJSON(clean)
	if err != nil {
		return nil, fmt.Errorf("canonical JSON: %w", err)
	}

	sig := ed25519.Sign(privKey, payload)
	doc["signature"] = b58Encode(sig)

	return doc, nil
}

// checkVersion validates that the document's "v" field is 1 (the only supported protocol version).
func checkVersion(doc map[string]interface{}) error {
	vField, ok := doc["v"]
	if !ok {
		return fmt.Errorf("document missing 'v' field")
	}
	switch v := vField.(type) {
	case json.Number:
		n, err := v.Int64()
		if err != nil || n != 1 {
			return fmt.Errorf("unsupported protocol version: %s", v.String())
		}
	case float64:
		if v != 1 {
			return fmt.Errorf("unsupported protocol version: %v", v)
		}
	default:
		return fmt.Errorf("'v' field is not a number")
	}
	return nil
}

// verifyDocument verifies a signed TLTV document against its channel ID.
// The channel ID is extracted from the "id" field by default.
func verifyDocument(doc map[string]interface{}, channelID string) error {
	if err := checkVersion(doc); err != nil {
		return err
	}

	// Check identity binding
	docID, ok := doc["id"]
	if !ok {
		return fmt.Errorf("document missing 'id' field")
	}
	docIDStr, ok := docID.(string)
	if !ok {
		return fmt.Errorf("'id' field is not a string")
	}
	if channelID != "" && docIDStr != channelID {
		return fmt.Errorf("identity binding mismatch: document id %q != expected %q", docIDStr, channelID)
	}
	if channelID == "" {
		channelID = docIDStr
	}

	if err := checkTimestamps(doc); err != nil {
		return err
	}

	return verifySignatureOnly(doc, channelID)
}

// verifyMigration verifies a signed migration document.
// Identity binding uses the "from" field instead of "id".
func verifyMigration(doc map[string]interface{}, oldChannelID string) error {
	if err := checkVersion(doc); err != nil {
		return err
	}

	// Check type
	docType, _ := doc["type"].(string)
	if docType != "migration" {
		return fmt.Errorf("document type is %q, expected 'migration'", docType)
	}

	// Check identity binding via "from" field
	fromID, ok := doc["from"]
	if !ok {
		return fmt.Errorf("migration document missing 'from' field")
	}
	fromIDStr, ok := fromID.(string)
	if !ok {
		return fmt.Errorf("'from' field is not a string")
	}
	if oldChannelID != "" && fromIDStr != oldChannelID {
		return fmt.Errorf("identity binding mismatch: from %q != expected %q", fromIDStr, oldChannelID)
	}
	if oldChannelID == "" {
		oldChannelID = fromIDStr
	}

	// Validate "to" field: must be a valid channel ID, different from "from"
	toID, ok := doc["to"]
	if !ok {
		return fmt.Errorf("migration document missing 'to' field")
	}
	toIDStr, ok := toID.(string)
	if !ok {
		return fmt.Errorf("'to' field is not a string")
	}
	if toIDStr == oldChannelID {
		return fmt.Errorf("migration 'to' is the same as 'from'")
	}
	if err := validateChannelID(toIDStr); err != nil {
		return fmt.Errorf("invalid migration target: %w", err)
	}

	if err := checkTimestamps(doc); err != nil {
		return err
	}

	return verifySignatureOnly(doc, oldChannelID)
}

// checkTimestamps rejects documents with invalid or future seq/updated/migrated fields.
// Per spec section 7.2: "reject if seq or updated is more than 3600 seconds ahead."
// Strict: rejects malformed or wrongly typed fields instead of silently ignoring them.
func checkTimestamps(doc map[string]interface{}) error {
	now := time.Now().Unix()

	// Check seq (Unix timestamp integer)
	if seqVal, ok := doc["seq"]; ok {
		var seq int64
		switch v := seqVal.(type) {
		case json.Number:
			n, err := v.Int64()
			if err != nil {
				return fmt.Errorf("'seq' is not a valid integer: %s", v.String())
			}
			seq = n
		case float64:
			if v != math.Trunc(v) {
				return fmt.Errorf("'seq' must be an integer, got %v", v)
			}
			seq = int64(v)
		default:
			return fmt.Errorf("'seq' must be a number, got %T", seqVal)
		}
		if seq < 0 {
			return fmt.Errorf("'seq' must be non-negative, got %d", seq)
		}
		if seq-now > maxClockSkew {
			return fmt.Errorf("seq is %d seconds in the future (max allowed: %d)", seq-now, maxClockSkew)
		}
	}

	// Check updated timestamp
	if updVal, ok := doc["updated"]; ok {
		updStr, ok := updVal.(string)
		if !ok {
			return fmt.Errorf("'updated' must be a string, got %T", updVal)
		}
		if err := validateTimestamp(updStr); err != nil {
			return fmt.Errorf("'updated': %w", err)
		}
		t, _ := time.Parse(timestampFormat, updStr)
		if t.Unix()-now > maxClockSkew {
			return fmt.Errorf("updated timestamp is %d seconds in the future", t.Unix()-now)
		}
	}

	// Check migrated timestamp (for migration documents)
	if migVal, ok := doc["migrated"]; ok {
		migStr, ok := migVal.(string)
		if !ok {
			return fmt.Errorf("'migrated' must be a string, got %T", migVal)
		}
		if err := validateTimestamp(migStr); err != nil {
			return fmt.Errorf("'migrated': %w", err)
		}
		t, _ := time.Parse(timestampFormat, migStr)
		if t.Unix()-now > maxClockSkew {
			return fmt.Errorf("migrated timestamp is %d seconds in the future", t.Unix()-now)
		}
	}

	return nil
}

func verifySignatureOnly(doc map[string]interface{}, channelID string) error {
	// Extract signature
	sigField, ok := doc["signature"]
	if !ok {
		return fmt.Errorf("missing signature field")
	}
	sigStr, ok := sigField.(string)
	if !ok {
		return fmt.Errorf("signature field is not a string")
	}

	sigBytes, err := b58Decode(sigStr)
	if err != nil {
		return fmt.Errorf("invalid base58 signature: %w", err)
	}
	if len(sigBytes) != ed25519.SignatureSize {
		return fmt.Errorf("invalid signature length: got %d bytes, expected %d", len(sigBytes), ed25519.SignatureSize)
	}

	// Extract public key from channel ID
	pubKey, err := parseChannelID(channelID)
	if err != nil {
		return fmt.Errorf("invalid channel ID: %w", err)
	}

	// Build canonical JSON without signature
	clean := make(map[string]interface{})
	for k, v := range doc {
		if k != "signature" {
			clean[k] = v
		}
	}

	payload, err := canonicalJSON(clean)
	if err != nil {
		return fmt.Errorf("canonical JSON: %w", err)
	}

	// Verify
	if !ed25519.Verify(pubKey, payload, sigBytes) {
		return fmt.Errorf("invalid signature")
	}

	return nil
}

// ---------- Document I/O ----------

// readDocument reads a JSON document from a reader, preserving number types.
// Enforces the 64 KB document size limit per spec section 5.6.
// Rejects trailing data after the JSON document (concatenated JSON, garbage).
func readDocument(r io.Reader) (map[string]interface{}, error) {
	data, err := io.ReadAll(io.LimitReader(r, maxDocSize+1))
	if err != nil {
		return nil, fmt.Errorf("reading input: %w", err)
	}
	if len(data) > maxDocSize {
		return nil, fmt.Errorf("document exceeds maximum size of %d bytes", maxDocSize)
	}

	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.UseNumber()

	var doc map[string]interface{}
	if err := dec.Decode(&doc); err != nil {
		return nil, fmt.Errorf("invalid JSON: %w", err)
	}

	// Reject trailing data (concatenated JSON values, extra content)
	if _, err := dec.Token(); err != io.EOF {
		return nil, fmt.Errorf("trailing data after JSON document")
	}

	return doc, nil
}

// readDocumentFromString reads a JSON document from a string.
func readDocumentFromString(s string) (map[string]interface{}, error) {
	return readDocument(strings.NewReader(s))
}

// ---------- Timestamp Validation ----------

// validateTimestamp checks that a timestamp string conforms to the spec format.
// Per spec section 6.4: ISO 8601 UTC with Z suffix, second precision, no fractional seconds.
func validateTimestamp(s string) error {
	t, err := time.Parse(timestampFormat, s)
	if err != nil {
		return fmt.Errorf("must be UTC format YYYY-MM-DDTHH:MM:SSZ, got %q", s)
	}
	// Roundtrip check: rejects fractional seconds and other leniencies
	if t.Format(timestampFormat) != s {
		return fmt.Errorf("must be UTC format YYYY-MM-DDTHH:MM:SSZ, got %q", s)
	}
	return nil
}

// validateDocTimestamps checks all timestamp fields in a document before signing.
// For guide documents, also validates entry start/end timestamps.
func validateDocTimestamps(doc map[string]interface{}) error {
	fields := []string{"updated", "migrated"}
	// Guide documents also have from/until as timestamps
	if _, ok := doc["entries"]; ok {
		fields = append(fields, "from", "until")
	}
	for _, field := range fields {
		if v, ok := doc[field]; ok {
			if s, ok := v.(string); ok {
				if err := validateTimestamp(s); err != nil {
					return fmt.Errorf("%s: %w", field, err)
				}
			}
		}
	}

	// Validate guide entry start/end timestamps
	if entries, ok := doc["entries"].([]interface{}); ok {
		for i, e := range entries {
			entry, ok := e.(map[string]interface{})
			if !ok {
				continue
			}
			for _, field := range []string{"start", "end"} {
				if v, ok := entry[field]; ok {
					if s, ok := v.(string); ok {
						if err := validateTimestamp(s); err != nil {
							return fmt.Errorf("entries[%d].%s: %w", i, field, err)
						}
					}
				}
			}
		}
	}

	return nil
}

// ---------- JSON Output ----------

// documentToJSON formats a document as pretty-printed JSON.
// Uses SetEscapeHTML(false) to avoid escaping <, >, & in output.
func documentToJSON(doc map[string]interface{}) ([]byte, error) {
	var buf strings.Builder
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	if err := enc.Encode(doc); err != nil {
		return nil, err
	}
	// Encoder adds trailing newline; trim it so caller controls formatting
	return []byte(strings.TrimRight(buf.String(), "\n")), nil
}
