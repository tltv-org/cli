package main

import (
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"
)

// maxClockSkew is the maximum allowed future timestamp (spec section 7.2).
const maxClockSkew = 3600 // seconds

// canonicalJSON produces RFC 8785 (JCS) canonical JSON bytes for a document.
// Go's json.Marshal sorts map keys alphabetically and uses compact format.
func canonicalJSON(doc map[string]interface{}) ([]byte, error) {
	return json.Marshal(doc)
}

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

// checkTimestamps rejects documents with seq or updated more than 3600s in the future.
// Per spec section 7.2: "reject if seq or updated is more than 3600 seconds ahead."
func checkTimestamps(doc map[string]interface{}) error {
	now := time.Now().Unix()

	// Check seq (Unix timestamp)
	if seqVal, ok := doc["seq"]; ok {
		var seq int64
		switch v := seqVal.(type) {
		case json.Number:
			n, err := v.Int64()
			if err == nil {
				seq = n
			}
		case float64:
			seq = int64(v)
		}
		if seq > 0 && seq-now > maxClockSkew {
			return fmt.Errorf("seq is %d seconds in the future (max allowed: %d)", seq-now, maxClockSkew)
		}
	}

	// Check updated timestamp
	if updStr, ok := doc["updated"].(string); ok {
		if t, err := time.Parse("2006-01-02T15:04:05Z", updStr); err == nil {
			if t.Unix()-now > maxClockSkew {
				return fmt.Errorf("updated timestamp is %d seconds in the future", t.Unix()-now)
			}
		}
	}

	// Check migrated timestamp (for migration documents)
	if migStr, ok := doc["migrated"].(string); ok {
		if t, err := time.Parse("2006-01-02T15:04:05Z", migStr); err == nil {
			if t.Unix()-now > maxClockSkew {
				return fmt.Errorf("migrated timestamp is %d seconds in the future", t.Unix()-now)
			}
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

// readDocument reads a JSON document from a reader, preserving number types.
func readDocument(r io.Reader) (map[string]interface{}, error) {
	dec := json.NewDecoder(r)
	dec.UseNumber()

	var doc map[string]interface{}
	if err := dec.Decode(&doc); err != nil {
		return nil, fmt.Errorf("invalid JSON: %w", err)
	}
	return doc, nil
}

// readDocumentFromString reads a JSON document from a string.
func readDocumentFromString(s string) (map[string]interface{}, error) {
	return readDocument(strings.NewReader(s))
}

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
