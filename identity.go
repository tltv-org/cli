package main

import (
	"crypto/ed25519"
	"fmt"
	"regexp"
)

// VersionPrefix is the 2-byte prefix for TLTV channel IDs.
// Produces IDs that always start with "TV".
var VersionPrefix = []byte{0x14, 0x33}

// Well-known test channel ID (RFC 8032 section 7.1, test vector 1).
const testChannelIDConst = "TVMkVHiXF9W1NgM9KLgs7tcBMvC1YtF4Daj4yfTrJercs3"

var channelIDRegex = regexp.MustCompile(`^TV[1-9A-HJ-NP-Za-km-z]{44}$`)

// makeChannelID encodes an Ed25519 public key as a TLTV channel ID.
func makeChannelID(pub ed25519.PublicKey) string {
	payload := make([]byte, 0, 34)
	payload = append(payload, VersionPrefix...)
	payload = append(payload, pub...)
	return b58Encode(payload)
}

// parseChannelID decodes a channel ID string to an Ed25519 public key.
// Returns an error if the ID is malformed.
func parseChannelID(id string) (ed25519.PublicKey, error) {
	if id == "" {
		return nil, fmt.Errorf("empty channel ID")
	}

	decoded, err := b58Decode(id)
	if err != nil {
		return nil, fmt.Errorf("invalid base58: %w", err)
	}

	if len(decoded) != 34 {
		return nil, fmt.Errorf("wrong length: got %d bytes, expected 34", len(decoded))
	}

	if decoded[0] != VersionPrefix[0] || decoded[1] != VersionPrefix[1] {
		return nil, fmt.Errorf("wrong version prefix: got %02x%02x, expected %02x%02x",
			decoded[0], decoded[1], VersionPrefix[0], VersionPrefix[1])
	}

	return ed25519.PublicKey(decoded[2:]), nil
}

// validateChannelID checks if a string is a valid TLTV channel ID format.
func validateChannelID(id string) error {
	if !channelIDRegex.MatchString(id) {
		return fmt.Errorf("invalid channel ID format (expected TV + 44 base58 chars)")
	}
	// Also verify it decodes correctly
	_, err := parseChannelID(id)
	return err
}

// isTestChannel checks if a channel ID is the well-known RFC 8032 test vector.
func isTestChannel(id string) bool {
	return id == testChannelIDConst
}

// keyFromSeed creates an Ed25519 private key from a 32-byte seed.
func keyFromSeed(seed []byte) (ed25519.PrivateKey, ed25519.PublicKey) {
	priv := ed25519.NewKeyFromSeed(seed)
	pub := priv.Public().(ed25519.PublicKey)
	return priv, pub
}
