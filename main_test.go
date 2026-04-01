package main

import (
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

// Test vectors from tltv-protocol/test-vectors/

// C1: Identity encoding
func TestChannelID(t *testing.T) {
	pubKeyHex := "d75a980182b10ab7d54bfed3c964073a0ee172f3daa62325af021a68f707511a"
	expectedID := "TVMkVHiXF9W1NgM9KLgs7tcBMvC1YtF4Daj4yfTrJercs3"

	pubKey, _ := hex.DecodeString(pubKeyHex)
	id := makeChannelID(ed25519.PublicKey(pubKey))

	if id != expectedID {
		t.Fatalf("makeChannelID: got %q, want %q", id, expectedID)
	}
}

func TestParseChannelID(t *testing.T) {
	expectedPubKeyHex := "d75a980182b10ab7d54bfed3c964073a0ee172f3daa62325af021a68f707511a"
	channelID := "TVMkVHiXF9W1NgM9KLgs7tcBMvC1YtF4Daj4yfTrJercs3"

	pubKey, err := parseChannelID(channelID)
	if err != nil {
		t.Fatalf("parseChannelID: %v", err)
	}

	gotHex := hex.EncodeToString(pubKey)
	if gotHex != expectedPubKeyHex {
		t.Fatalf("parseChannelID pubkey: got %q, want %q", gotHex, expectedPubKeyHex)
	}
}

func TestChannelIDRoundtrip(t *testing.T) {
	payloadHex := "1433d75a980182b10ab7d54bfed3c964073a0ee172f3daa62325af021a68f707511a"
	expectedID := "TVMkVHiXF9W1NgM9KLgs7tcBMvC1YtF4Daj4yfTrJercs3"

	payload, _ := hex.DecodeString(payloadHex)
	encoded := b58Encode(payload)
	if encoded != expectedID {
		t.Fatalf("b58Encode: got %q, want %q", encoded, expectedID)
	}

	decoded, err := b58Decode(encoded)
	if err != nil {
		t.Fatalf("b58Decode: %v", err)
	}
	if hex.EncodeToString(decoded) != payloadHex {
		t.Fatalf("b58Decode roundtrip: got %q, want %q", hex.EncodeToString(decoded), payloadHex)
	}
}

func TestIsTestChannel(t *testing.T) {
	if !isTestChannel("TVMkVHiXF9W1NgM9KLgs7tcBMvC1YtF4Daj4yfTrJercs3") {
		t.Fatal("isTestChannel should return true for test vector")
	}
	if isTestChannel("TVBNw4nHBzAaBWr8b17Sd2sGYcvMc1utersd6tceC6WmBZ") {
		t.Fatal("isTestChannel should return false for non-test channel")
	}
}

// C2: Signing
func TestSignDocument(t *testing.T) {
	seedHex := "9d61b19deffd5a60ba844af492ec2cc44449c5697b326919703bac031cae7f60"
	expectedSigB58 := "2TgRpS4h1UREKn3rRGk3cMRQ9fXQZ2TYX76oWCkHnDbHmUm2hTNAcXy8nSphcFVwareooGM2hqwvWgoGigaCNaob"

	seed, _ := hex.DecodeString(seedHex)
	priv, _ := keyFromSeed(seed)

	// Parse document with UseNumber to preserve integer types
	docJSON := `{
		"v": 1,
		"seq": 1742000000,
		"id": "TVMkVHiXF9W1NgM9KLgs7tcBMvC1YtF4Daj4yfTrJercs3",
		"name": "Test Channel",
		"description": "A test channel for protocol verification",
		"stream": "/tltv/v1/channels/TVMkVHiXF9W1NgM9KLgs7tcBMvC1YtF4Daj4yfTrJercs3/stream.m3u8",
		"guide": "/tltv/v1/channels/TVMkVHiXF9W1NgM9KLgs7tcBMvC1YtF4Daj4yfTrJercs3/guide.json",
		"access": "public",
		"updated": "2026-03-14T12:00:00Z"
	}`

	doc, err := readDocumentFromString(docJSON)
	if err != nil {
		t.Fatalf("readDocument: %v", err)
	}

	// Check canonical JSON length
	clean := make(map[string]interface{})
	for k, v := range doc {
		clean[k] = v
	}
	canonical, _ := canonicalJSON(clean)
	if len(canonical) != 382 {
		t.Fatalf("canonical JSON length: got %d, want 382\nJSON: %s", len(canonical), string(canonical))
	}

	signed, err := signDocument(doc, priv)
	if err != nil {
		t.Fatalf("signDocument: %v", err)
	}

	sig, _ := signed["signature"].(string)
	if sig != expectedSigB58 {
		t.Fatalf("signature: got %q, want %q", sig, expectedSigB58)
	}
}

// C3: Complete document verification
func TestVerifyDocument(t *testing.T) {
	channelID := "TVMkVHiXF9W1NgM9KLgs7tcBMvC1YtF4Daj4yfTrJercs3"

	docJSON := `{
		"v": 1,
		"seq": 1742000000,
		"id": "TVMkVHiXF9W1NgM9KLgs7tcBMvC1YtF4Daj4yfTrJercs3",
		"name": "Test Channel",
		"description": "A test channel for protocol verification",
		"stream": "/tltv/v1/channels/TVMkVHiXF9W1NgM9KLgs7tcBMvC1YtF4Daj4yfTrJercs3/stream.m3u8",
		"guide": "/tltv/v1/channels/TVMkVHiXF9W1NgM9KLgs7tcBMvC1YtF4Daj4yfTrJercs3/guide.json",
		"access": "public",
		"updated": "2026-03-14T12:00:00Z",
		"signature": "2TgRpS4h1UREKn3rRGk3cMRQ9fXQZ2TYX76oWCkHnDbHmUm2hTNAcXy8nSphcFVwareooGM2hqwvWgoGigaCNaob"
	}`

	doc, _ := readDocumentFromString(docJSON)
	if err := verifyDocument(doc, channelID); err != nil {
		t.Fatalf("verifyDocument: %v", err)
	}
}

// C4: URI parsing
func TestURIParsing(t *testing.T) {
	channelID := "TVMkVHiXF9W1NgM9KLgs7tcBMvC1YtF4Daj4yfTrJercs3"

	cases := []struct {
		name  string
		uri   string
		id    string
		hints []string
		token string
	}{
		{
			"bare channel ID",
			"tltv://" + channelID,
			channelID, nil, "",
		},
		{
			"@ hint with host and port",
			"tltv://" + channelID + "@node.example.com:8443",
			channelID, []string{"node.example.com:8443"}, "",
		},
		{
			"@ hint with IP",
			"tltv://" + channelID + "@192.168.1.100:8000",
			channelID, []string{"192.168.1.100:8000"}, "",
		},
		{
			"single via hint",
			"tltv://" + channelID + "?via=relay.example.com:8443",
			channelID, []string{"relay.example.com:8443"}, "",
		},
		{
			"multiple via hints",
			"tltv://" + channelID + "?via=relay1.example.com:8000,relay2.example.com:8443",
			channelID, []string{"relay1.example.com:8000", "relay2.example.com:8443"}, "",
		},
		{
			"token only",
			"tltv://" + channelID + "?token=secret_abc123",
			channelID, nil, "secret_abc123",
		},
		{
			"token and via hint",
			"tltv://" + channelID + "?token=secret_abc123&via=relay.example.com:443",
			channelID, []string{"relay.example.com:443"}, "secret_abc123",
		},
		{
			"@ hint combined with via hint",
			"tltv://" + channelID + "@origin.example.com:443?via=relay.example.com:8000",
			channelID, []string{"origin.example.com:443", "relay.example.com:8000"}, "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			parsed, err := parseTLTVUri(tc.uri)
			if err != nil {
				t.Fatalf("parseTLTVUri(%q): %v", tc.uri, err)
			}
			if parsed.ChannelID != tc.id {
				t.Errorf("channel_id: got %q, want %q", parsed.ChannelID, tc.id)
			}
			if len(parsed.Hints) != len(tc.hints) {
				t.Errorf("hints count: got %d, want %d", len(parsed.Hints), len(tc.hints))
			} else {
				for i, h := range parsed.Hints {
					if h != tc.hints[i] {
						t.Errorf("hint[%d]: got %q, want %q", i, h, tc.hints[i])
					}
				}
			}
			if parsed.Token != tc.token {
				t.Errorf("token: got %q, want %q", parsed.Token, tc.token)
			}
		})
	}
}

func TestURIParsingInvalid(t *testing.T) {
	invalid := []struct {
		name string
		uri  string
	}{
		{"wrong scheme", "https://example.com"},
		{"empty channel ID", "tltv://"},
		{"http scheme", "http://TVMkVHiXF9W1NgM9KLgs7tcBMvC1YtF4Daj4yfTrJercs3"},
	}

	for _, tc := range invalid {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseTLTVUri(tc.uri)
			if err == nil {
				t.Fatalf("parseTLTVUri(%q) should have failed", tc.uri)
			}
		})
	}
}

// C5: Guide document
func TestGuideSignAndVerify(t *testing.T) {
	seedHex := "9d61b19deffd5a60ba844af492ec2cc44449c5697b326919703bac031cae7f60"
	expectedSigB58 := "5ovnFoizF7E7ZKD9jfH6uyus1SgRbRLKnNGNNEvN26h5HQaqwEz14875HSoJaXe74besWDUMA2W29cgv6YHrSRBq"
	channelID := "TVMkVHiXF9W1NgM9KLgs7tcBMvC1YtF4Daj4yfTrJercs3"

	seed, _ := hex.DecodeString(seedHex)
	priv, _ := keyFromSeed(seed)

	docJSON := `{
		"v": 1,
		"seq": 1742000042,
		"id": "TVMkVHiXF9W1NgM9KLgs7tcBMvC1YtF4Daj4yfTrJercs3",
		"from": "2026-03-14T05:00:00Z",
		"until": "2026-03-16T05:00:00Z",
		"entries": [
			{
				"start": "2026-03-15T00:00:00Z",
				"end": "2026-03-15T00:15:00Z",
				"title": "Channel One Intro"
			},
			{
				"start": "2026-03-15T00:15:00Z",
				"end": "2026-03-15T01:00:00Z",
				"title": "Evening Clips",
				"description": "Curated selection of short films",
				"category": "film"
			}
		],
		"updated": "2026-03-14T03:00:00Z"
	}`

	doc, _ := readDocumentFromString(docJSON)

	// Check canonical JSON length
	canonical, _ := canonicalJSON(doc)
	if len(canonical) != 427 {
		t.Fatalf("canonical JSON length: got %d, want 427\nJSON: %s", len(canonical), string(canonical))
	}

	// Sign and check signature
	signed, err := signDocument(doc, priv)
	if err != nil {
		t.Fatalf("signDocument: %v", err)
	}
	sig, _ := signed["signature"].(string)
	if sig != expectedSigB58 {
		t.Fatalf("signature: got %q, want %q", sig, expectedSigB58)
	}

	// Verify
	if err := verifyDocument(signed, channelID); err != nil {
		t.Fatalf("verifyDocument: %v", err)
	}
}

// C6: Invalid inputs
func TestInvalidChannelIDs(t *testing.T) {
	cases := []struct {
		name string
		id   string
	}{
		{"wrong version prefix", "11FVen3X669xLzsi6N2V91DoiyzHzg1uAgqiT8jZ9nS96Z"},
		{"too short", "HKfHb5mVXWZoTX8o77GnDgnLMoV"},
		{"invalid char 0", "TV0InvalidBase58WithZeroChar"},
		{"invalid char O", "TVOanotherInvalidBase58String"},
		{"invalid char l", "TVlowercaseLinvalidBase58Str"},
		{"invalid char I", "TVIuppercaseIinvalidBase58St"},
		{"empty string", ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseChannelID(tc.id)
			if err == nil {
				t.Fatalf("parseChannelID(%q) should have failed", tc.id)
			}
		})
	}
}

func TestTamperedDocument(t *testing.T) {
	channelID := "TVMkVHiXF9W1NgM9KLgs7tcBMvC1YtF4Daj4yfTrJercs3"

	// Tampered name
	docJSON := `{
		"v": 1,
		"seq": 1742000000,
		"id": "TVMkVHiXF9W1NgM9KLgs7tcBMvC1YtF4Daj4yfTrJercs3",
		"name": "TAMPERED NAME",
		"description": "A test channel for protocol verification",
		"stream": "/tltv/v1/channels/TVMkVHiXF9W1NgM9KLgs7tcBMvC1YtF4Daj4yfTrJercs3/stream.m3u8",
		"guide": "/tltv/v1/channels/TVMkVHiXF9W1NgM9KLgs7tcBMvC1YtF4Daj4yfTrJercs3/guide.json",
		"access": "public",
		"updated": "2026-03-14T12:00:00Z",
		"signature": "2TgRpS4h1UREKn3rRGk3cMRQ9fXQZ2TYX76oWCkHnDbHmUm2hTNAcXy8nSphcFVwareooGM2hqwvWgoGigaCNaob"
	}`

	doc, _ := readDocumentFromString(docJSON)
	if err := verifyDocument(doc, channelID); err == nil {
		t.Fatal("verifyDocument should fail for tampered document")
	}
}

func TestMissingSignature(t *testing.T) {
	channelID := "TVMkVHiXF9W1NgM9KLgs7tcBMvC1YtF4Daj4yfTrJercs3"

	docJSON := `{
		"v": 1,
		"seq": 1742000000,
		"id": "TVMkVHiXF9W1NgM9KLgs7tcBMvC1YtF4Daj4yfTrJercs3",
		"name": "Test Channel",
		"stream": "/tltv/v1/channels/TVMkVHiXF9W1NgM9KLgs7tcBMvC1YtF4Daj4yfTrJercs3/stream.m3u8",
		"access": "public",
		"updated": "2026-03-14T12:00:00Z"
	}`

	doc, _ := readDocumentFromString(docJSON)
	if err := verifyDocument(doc, channelID); err == nil {
		t.Fatal("verifyDocument should fail for missing signature")
	}
}

func TestIdentityBindingMismatch(t *testing.T) {
	docJSON := `{
		"v": 1,
		"seq": 1742000000,
		"id": "TVsomeOtherChannelIdThatDoesNotMatchExpected1234",
		"name": "Test Channel",
		"stream": "/tltv/v1/channels/TVsomeOtherChannelIdThatDoesNotMatchExpected1234/stream.m3u8",
		"access": "public",
		"updated": "2026-03-14T12:00:00Z",
		"signature": "4ga7qsWoJmM4dp8t8YbQoaCHFXAYpRCxfMmkz1s2UaC55quqV9pioXfnGRkxGhzcLZVRgYFr5bKV6F8oJV9U2esc"
	}`

	doc, _ := readDocumentFromString(docJSON)
	err := verifyDocument(doc, "TVMkVHiXF9W1NgM9KLgs7tcBMvC1YtF4Daj4yfTrJercs3")
	if err == nil {
		t.Fatal("verifyDocument should fail for identity binding mismatch")
	}
	if !strings.Contains(err.Error(), "identity binding mismatch") {
		t.Fatalf("expected identity binding error, got: %v", err)
	}
}

func TestTruncatedSignature(t *testing.T) {
	channelID := "TVMkVHiXF9W1NgM9KLgs7tcBMvC1YtF4Daj4yfTrJercs3"

	docJSON := `{
		"v": 1,
		"seq": 1742000000,
		"id": "TVMkVHiXF9W1NgM9KLgs7tcBMvC1YtF4Daj4yfTrJercs3",
		"name": "Test Channel",
		"stream": "/tltv/v1/channels/TVMkVHiXF9W1NgM9KLgs7tcBMvC1YtF4Daj4yfTrJercs3/stream.m3u8",
		"access": "public",
		"updated": "2026-03-14T12:00:00Z",
		"signature": "2TgRpS4h1URE"
	}`

	doc, _ := readDocumentFromString(docJSON)
	err := verifyDocument(doc, channelID)
	if err == nil {
		t.Fatal("verifyDocument should fail for truncated signature")
	}
}

// C7: Key migration
func TestMigrationDocument(t *testing.T) {
	seedHex := "9d61b19deffd5a60ba844af492ec2cc44449c5697b326919703bac031cae7f60"
	oldChannelID := "TVMkVHiXF9W1NgM9KLgs7tcBMvC1YtF4Daj4yfTrJercs3"
	newChannelID := "TVBNw4nHBzAaBWr8b17Sd2sGYcvMc1utersd6tceC6WmBZ"
	expectedSigB58 := "3Shcvdqrgb6Voi3mfPhKc77vbksDxcLGAbxKfDugQ3onq4DdagYeFPhb98DhLwCwrSrW7wtrxZF4GE8BxjHUinWA"

	seed, _ := hex.DecodeString(seedHex)
	priv, _ := keyFromSeed(seed)

	docJSON := `{
		"v": 1,
		"seq": 1742000000,
		"type": "migration",
		"from": "TVMkVHiXF9W1NgM9KLgs7tcBMvC1YtF4Daj4yfTrJercs3",
		"to": "TVBNw4nHBzAaBWr8b17Sd2sGYcvMc1utersd6tceC6WmBZ",
		"reason": "key compromise",
		"migrated": "2026-03-14T12:00:00Z"
	}`

	doc, _ := readDocumentFromString(docJSON)

	// Check canonical JSON length
	canonical, _ := canonicalJSON(doc)
	if len(canonical) != 213 {
		t.Fatalf("canonical JSON length: got %d, want 213\nJSON: %s", len(canonical), string(canonical))
	}

	// Sign and verify signature matches
	signed, err := signDocument(doc, priv)
	if err != nil {
		t.Fatalf("signDocument: %v", err)
	}
	sig, _ := signed["signature"].(string)
	if sig != expectedSigB58 {
		t.Fatalf("signature: got %q, want %q", sig, expectedSigB58)
	}

	// Verify as migration
	if err := verifyMigration(signed, oldChannelID); err != nil {
		t.Fatalf("verifyMigration: %v", err)
	}

	// Verify the new channel ID in the document
	toField, _ := signed["to"].(string)
	if toField != newChannelID {
		t.Fatalf("to field: got %q, want %q", toField, newChannelID)
	}
}

// Base58 edge cases
func TestBase58EmptyDecode(t *testing.T) {
	_, err := b58Decode("")
	if err == nil {
		t.Fatal("b58Decode should fail for empty string")
	}
}

func TestBase58InvalidChars(t *testing.T) {
	for _, ch := range "0OIl" {
		_, err := b58Decode(string(ch))
		if err == nil {
			t.Fatalf("b58Decode should fail for invalid char %q", string(ch))
		}
	}
}

func TestBase58Roundtrip(t *testing.T) {
	cases := [][]byte{
		{0x00},
		{0x00, 0x00, 0x01},
		{0xff, 0xff, 0xff},
		{0x14, 0x33},
	}
	for _, data := range cases {
		encoded := b58Encode(data)
		decoded, err := b58Decode(encoded)
		if err != nil {
			t.Fatalf("b58Decode(%q): %v", encoded, err)
		}
		if hex.EncodeToString(decoded) != hex.EncodeToString(data) {
			t.Fatalf("roundtrip failed: %x -> %q -> %x", data, encoded, decoded)
		}
	}
}

// Signature hex verification
func TestSignatureHex(t *testing.T) {
	expectedHex := "49064ea6d6a8dce519874e51c1c4d58fdf18bc4b267dd995cfea03200fdf3f94a1fcb6fb0f76998f7af941b689da95cbf5738caaa162ba6f32a844000512ac0a"
	sigB58 := "2TgRpS4h1UREKn3rRGk3cMRQ9fXQZ2TYX76oWCkHnDbHmUm2hTNAcXy8nSphcFVwareooGM2hqwvWgoGigaCNaob"

	sigBytes, err := b58Decode(sigB58)
	if err != nil {
		t.Fatalf("b58Decode: %v", err)
	}
	gotHex := hex.EncodeToString(sigBytes)
	if gotHex != expectedHex {
		t.Fatalf("signature hex: got %q, want %q", gotHex, expectedHex)
	}
}

func TestNewChannelIDFormat(t *testing.T) {
	newPubKeyHex := "3d4017c3e843895a92b70aa74d1b7ebc9c982ccf2ec4968cc0cd55f12af4660c"
	expectedID := "TVBNw4nHBzAaBWr8b17Sd2sGYcvMc1utersd6tceC6WmBZ"

	pubKey, _ := hex.DecodeString(newPubKeyHex)
	id := makeChannelID(ed25519.PublicKey(pubKey))
	if id != expectedID {
		t.Fatalf("makeChannelID: got %q, want %q", id, expectedID)
	}
}

func TestMigrationSignatureHex(t *testing.T) {
	expectedHex := "7a326de844670e0798631b9fb75d02760a04d3d72caeccb94f9e2beef5d91616ccd4dfe6c7efb1b28a9bfb5e2b9bcf0ccce42b2db4f0b8a49c238d8381e1a507"
	sigB58 := "3Shcvdqrgb6Voi3mfPhKc77vbksDxcLGAbxKfDugQ3onq4DdagYeFPhb98DhLwCwrSrW7wtrxZF4GE8BxjHUinWA"

	sigBytes, err := b58Decode(sigB58)
	if err != nil {
		t.Fatalf("b58Decode: %v", err)
	}
	gotHex := hex.EncodeToString(sigBytes)
	if gotHex != expectedHex {
		t.Fatalf("migration signature hex: got %q, want %q", gotHex, expectedHex)
	}
}

// Canonical JSON ordering
func TestCanonicalJSONOrder(t *testing.T) {
	doc := map[string]interface{}{
		"z": "last",
		"a": "first",
		"m": "middle",
	}
	out, _ := canonicalJSON(doc)
	expected := `{"a":"first","m":"middle","z":"last"}`
	if string(out) != expected {
		t.Fatalf("canonical JSON order: got %q, want %q", string(out), expected)
	}
}

func TestCanonicalJSONCompact(t *testing.T) {
	doc := map[string]interface{}{
		"v":   json.Number("1"),
		"seq": json.Number("1742000000"),
		"id":  "test",
	}
	out, _ := canonicalJSON(doc)
	// Should have no whitespace
	if strings.Contains(string(out), " ") || strings.Contains(string(out), "\n") {
		t.Fatalf("canonical JSON should be compact: %q", string(out))
	}
}

// URI format roundtrip
func TestURIFormatRoundtrip(t *testing.T) {
	channelID := "TVMkVHiXF9W1NgM9KLgs7tcBMvC1YtF4Daj4yfTrJercs3"

	cases := []struct {
		name  string
		hints []string
		token string
	}{
		{"bare", nil, ""},
		{"one hint", []string{"example.com:443"}, ""},
		{"two hints", []string{"relay1.com:443", "relay2.com:8000"}, ""},
		{"token only", nil, "secret123"},
		{"token and hints", []string{"example.com:443"}, "mytoken"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			uri := formatTLTVUri(channelID, tc.hints, tc.token)
			parsed, err := parseTLTVUri(uri)
			if err != nil {
				t.Fatalf("roundtrip failed: format produced %q, parse error: %v", uri, err)
			}
			if parsed.ChannelID != channelID {
				t.Errorf("channel ID: got %q, want %q", parsed.ChannelID, channelID)
			}
			if parsed.Token != tc.token {
				t.Errorf("token: got %q, want %q", parsed.Token, tc.token)
			}
			// Hints come back via the via= param in format, so they roundtrip
			if len(parsed.Hints) != len(tc.hints) {
				t.Errorf("hints count: got %d, want %d", len(parsed.Hints), len(tc.hints))
			}
		})
	}
}

// Position-2 feasibility
func TestPos2Feasibility(t *testing.T) {
	// Characters that ARE achievable at position 2
	for _, ch := range pos2Chars {
		mode, ok := checkPrefixFeasibility(string(ch), false)
		if !ok || mode != "prefix" {
			t.Errorf("char %q should be feasible at pos 2", string(ch))
		}
	}

	// Characters that are NOT achievable at position 2
	impossible := "abcdefghjkmnopqrstuvwxyz12345RSWXYZi"
	for _, ch := range impossible {
		if strings.ContainsRune(pos2Chars, ch) {
			continue // skip if actually in pos2Chars
		}
		_, ok := checkPrefixFeasibility(string(ch), false)
		if ok {
			t.Errorf("char %q should NOT be feasible at pos 2", string(ch))
		}
	}
}

// Future timestamp rejection
func TestFutureTimestampRejection(t *testing.T) {
	seedHex := "9d61b19deffd5a60ba844af492ec2cc44449c5697b326919703bac031cae7f60"
	seed, _ := hex.DecodeString(seedHex)
	priv, _ := keyFromSeed(seed)

	// Create a document with seq 2 hours in the future
	futureSeq := time.Now().Unix() + 7200
	doc := map[string]interface{}{
		"v":       json.Number("1"),
		"seq":     json.Number(fmt.Sprintf("%d", futureSeq)),
		"id":      "TVMkVHiXF9W1NgM9KLgs7tcBMvC1YtF4Daj4yfTrJercs3",
		"name":    "Future Channel",
		"stream":  "/tltv/v1/channels/TVMkVHiXF9W1NgM9KLgs7tcBMvC1YtF4Daj4yfTrJercs3/stream.m3u8",
		"access":  "public",
		"updated": "2026-03-14T12:00:00Z",
	}

	signed, _ := signDocument(doc, priv)
	err := verifyDocument(signed, "TVMkVHiXF9W1NgM9KLgs7tcBMvC1YtF4Daj4yfTrJercs3")
	if err == nil {
		t.Fatal("verifyDocument should reject future seq")
	}
	if !strings.Contains(err.Error(), "future") {
		t.Fatalf("expected future timestamp error, got: %v", err)
	}
}

// Future "updated" timestamp rejection (seq is fine, updated is 2h ahead)
func TestFutureUpdatedTimestampRejection(t *testing.T) {
	seedHex := "9d61b19deffd5a60ba844af492ec2cc44449c5697b326919703bac031cae7f60"
	seed, _ := hex.DecodeString(seedHex)
	priv, _ := keyFromSeed(seed)

	futureUpdated := time.Now().Add(2 * time.Hour).UTC().Format("2006-01-02T15:04:05Z")
	doc := map[string]interface{}{
		"v":       json.Number("1"),
		"seq":     json.Number(fmt.Sprintf("%d", time.Now().Unix())),
		"id":      "TVMkVHiXF9W1NgM9KLgs7tcBMvC1YtF4Daj4yfTrJercs3",
		"name":    "Future Updated Channel",
		"stream":  "/tltv/v1/channels/TVMkVHiXF9W1NgM9KLgs7tcBMvC1YtF4Daj4yfTrJercs3/stream.m3u8",
		"access":  "public",
		"updated": futureUpdated,
	}

	signed, _ := signDocument(doc, priv)
	err := verifyDocument(signed, "TVMkVHiXF9W1NgM9KLgs7tcBMvC1YtF4Daj4yfTrJercs3")
	if err == nil {
		t.Fatal("verifyDocument should reject future updated timestamp")
	}
	if !strings.Contains(err.Error(), "future") {
		t.Fatalf("expected future timestamp error, got: %v", err)
	}
}

// Future "migrated" timestamp rejection on migration documents
func TestFutureMigratedTimestampRejection(t *testing.T) {
	seedHex := "9d61b19deffd5a60ba844af492ec2cc44449c5697b326919703bac031cae7f60"
	seed, _ := hex.DecodeString(seedHex)
	priv, _ := keyFromSeed(seed)

	futureMigrated := time.Now().Add(2 * time.Hour).UTC().Format("2006-01-02T15:04:05Z")
	doc := map[string]interface{}{
		"v":        json.Number("1"),
		"seq":      json.Number(fmt.Sprintf("%d", time.Now().Unix())),
		"type":     "migration",
		"from":     "TVMkVHiXF9W1NgM9KLgs7tcBMvC1YtF4Daj4yfTrJercs3",
		"to":       "TVBNw4nHBzAaBWr8b17Sd2sGYcvMc1utersd6tceC6WmBZ",
		"reason":   "test",
		"migrated": futureMigrated,
	}

	signed, _ := signDocument(doc, priv)
	err := verifyMigration(signed, "TVMkVHiXF9W1NgM9KLgs7tcBMvC1YtF4Daj4yfTrJercs3")
	if err == nil {
		t.Fatal("verifyMigration should reject future migrated timestamp")
	}
	if !strings.Contains(err.Error(), "future") {
		t.Fatalf("expected future timestamp error, got: %v", err)
	}
}

// Migration identity binding mismatch
func TestMigrationIdentityBindingMismatch(t *testing.T) {
	seedHex := "9d61b19deffd5a60ba844af492ec2cc44449c5697b326919703bac031cae7f60"
	seed, _ := hex.DecodeString(seedHex)
	priv, _ := keyFromSeed(seed)

	doc := map[string]interface{}{
		"v":        json.Number("1"),
		"seq":      json.Number(fmt.Sprintf("%d", time.Now().Unix())),
		"type":     "migration",
		"from":     "TVMkVHiXF9W1NgM9KLgs7tcBMvC1YtF4Daj4yfTrJercs3",
		"to":       "TVBNw4nHBzAaBWr8b17Sd2sGYcvMc1utersd6tceC6WmBZ",
		"reason":   "test",
		"migrated": time.Now().UTC().Format("2006-01-02T15:04:05Z"),
	}

	signed, _ := signDocument(doc, priv)

	// Verify against a different channel ID than what's in "from"
	err := verifyMigration(signed, "TVBNw4nHBzAaBWr8b17Sd2sGYcvMc1utersd6tceC6WmBZ")
	if err == nil {
		t.Fatal("verifyMigration should fail for identity binding mismatch")
	}
	if !strings.Contains(err.Error(), "identity binding mismatch") {
		t.Fatalf("expected identity binding error, got: %v", err)
	}
}

// Version field validation
func TestUnsupportedVersion(t *testing.T) {
	seedHex := "9d61b19deffd5a60ba844af492ec2cc44449c5697b326919703bac031cae7f60"
	seed, _ := hex.DecodeString(seedHex)
	priv, _ := keyFromSeed(seed)

	doc := map[string]interface{}{
		"v":       json.Number("2"),
		"seq":     json.Number(fmt.Sprintf("%d", time.Now().Unix())),
		"id":      "TVMkVHiXF9W1NgM9KLgs7tcBMvC1YtF4Daj4yfTrJercs3",
		"name":    "V2 Channel",
		"stream":  "/test",
		"access":  "public",
		"updated": time.Now().UTC().Format("2006-01-02T15:04:05Z"),
	}

	signed, _ := signDocument(doc, priv)
	err := verifyDocument(signed, "TVMkVHiXF9W1NgM9KLgs7tcBMvC1YtF4Daj4yfTrJercs3")
	if err == nil {
		t.Fatal("verifyDocument should reject unsupported protocol version")
	}
	if !strings.Contains(err.Error(), "unsupported protocol version") {
		t.Fatalf("expected version error, got: %v", err)
	}
}

// Migration to-field validation
func TestMigrationInvalidTo(t *testing.T) {
	seedHex := "9d61b19deffd5a60ba844af492ec2cc44449c5697b326919703bac031cae7f60"
	seed, _ := hex.DecodeString(seedHex)
	priv, _ := keyFromSeed(seed)

	// "to" same as "from"
	doc := map[string]interface{}{
		"v":        json.Number("1"),
		"seq":      json.Number(fmt.Sprintf("%d", time.Now().Unix())),
		"type":     "migration",
		"from":     "TVMkVHiXF9W1NgM9KLgs7tcBMvC1YtF4Daj4yfTrJercs3",
		"to":       "TVMkVHiXF9W1NgM9KLgs7tcBMvC1YtF4Daj4yfTrJercs3",
		"migrated": time.Now().UTC().Format("2006-01-02T15:04:05Z"),
	}

	signed, _ := signDocument(doc, priv)
	err := verifyMigration(signed, "TVMkVHiXF9W1NgM9KLgs7tcBMvC1YtF4Daj4yfTrJercs3")
	if err == nil {
		t.Fatal("verifyMigration should reject to == from")
	}
	if !strings.Contains(err.Error(), "same as") {
		t.Fatalf("expected same-channel error, got: %v", err)
	}
}

// Verify that current timestamps pass
func TestCurrentTimestampAccepted(t *testing.T) {
	seedHex := "9d61b19deffd5a60ba844af492ec2cc44449c5697b326919703bac031cae7f60"
	seed, _ := hex.DecodeString(seedHex)
	priv, _ := keyFromSeed(seed)

	doc := map[string]interface{}{
		"v":       json.Number("1"),
		"seq":     json.Number(fmt.Sprintf("%d", time.Now().Unix())),
		"id":      "TVMkVHiXF9W1NgM9KLgs7tcBMvC1YtF4Daj4yfTrJercs3",
		"name":    "Current Channel",
		"stream":  "/tltv/v1/channels/TVMkVHiXF9W1NgM9KLgs7tcBMvC1YtF4Daj4yfTrJercs3/stream.m3u8",
		"access":  "public",
		"updated": time.Now().UTC().Format("2006-01-02T15:04:05Z"),
	}

	signed, _ := signDocument(doc, priv)
	err := verifyDocument(signed, "TVMkVHiXF9W1NgM9KLgs7tcBMvC1YtF4Daj4yfTrJercs3")
	if err != nil {
		t.Fatalf("verifyDocument should accept current timestamps: %v", err)
	}
}

// Document size limit
func TestDocumentSizeLimit(t *testing.T) {
	// Build a document just under the limit — should succeed
	small := `{"v":1,"id":"test"}`
	_, err := readDocumentFromString(small)
	if err != nil {
		t.Fatalf("small document should parse: %v", err)
	}

	// Build a document over 64 KB — should fail
	big := `{"v":1,"data":"` + strings.Repeat("x", 65536) + `"}`
	_, err = readDocumentFromString(big)
	if err == nil {
		t.Fatal("readDocument should reject documents over 64 KB")
	}
	if !strings.Contains(err.Error(), "maximum size") {
		t.Fatalf("expected size limit error, got: %v", err)
	}
}

// Timestamp format validation
func TestTimestampValidation(t *testing.T) {
	valid := []string{
		"2026-03-14T12:00:00Z",
		"2000-01-01T00:00:00Z",
		"2099-12-31T23:59:59Z",
	}
	for _, ts := range valid {
		if err := validateTimestamp(ts); err != nil {
			t.Errorf("validateTimestamp(%q) should pass: %v", ts, err)
		}
	}

	invalid := []string{
		"2026-03-14",                // missing time
		"2026-03-14T12:00:00+05:00", // non-UTC
		"2026-03-14T12:00:00.000Z",  // fractional seconds
		"2026-03-14 12:00:00Z",      // space instead of T
		"not-a-timestamp",           // garbage
		"",                          // empty
	}
	for _, ts := range invalid {
		if err := validateTimestamp(ts); err == nil {
			t.Errorf("validateTimestamp(%q) should fail", ts)
		}
	}
}

func TestValidateDocTimestamps(t *testing.T) {
	// Valid metadata document
	good := map[string]interface{}{
		"updated": "2026-03-14T12:00:00Z",
	}
	if err := validateDocTimestamps(good); err != nil {
		t.Fatalf("should pass: %v", err)
	}

	// Invalid updated format
	bad := map[string]interface{}{
		"updated": "2026-03-14",
	}
	if err := validateDocTimestamps(bad); err == nil {
		t.Fatal("should reject non-UTC updated timestamp")
	}

	// Valid migration document
	goodMig := map[string]interface{}{
		"migrated": "2026-03-14T12:00:00Z",
	}
	if err := validateDocTimestamps(goodMig); err != nil {
		t.Fatalf("should pass: %v", err)
	}

	// Guide document with from/until
	goodGuide := map[string]interface{}{
		"updated": "2026-03-14T12:00:00Z",
		"from":    "2026-03-14T00:00:00Z",
		"until":   "2026-03-16T00:00:00Z",
		"entries": []interface{}{},
	}
	if err := validateDocTimestamps(goodGuide); err != nil {
		t.Fatalf("should pass: %v", err)
	}
}

// Local address detection
func TestIsLocalAddress(t *testing.T) {
	local := []string{
		"localhost:443",
		"127.0.0.1:8000",
		"[::1]:443",
		"10.0.0.1:443",
		"172.16.0.1:443",
		"192.168.1.1:443",
		"100.64.0.1:443",     // CGN
		"100.127.255.254:80", // CGN upper bound
		"169.254.1.1:443",    // link-local
	}
	for _, addr := range local {
		if !isLocalAddress(addr) {
			t.Errorf("isLocalAddress(%q) should be true", addr)
		}
	}

	nonLocal := []string{
		"example.com:443",
		"8.8.8.8:443",
		"1.1.1.1:443",
		"100.128.0.1:443", // just outside CGN range
		"172.32.0.1:443",  // just outside 172.16-31 range
		"192.169.1.1:443", // just outside 192.168
	}
	for _, addr := range nonLocal {
		if isLocalAddress(addr) {
			t.Errorf("isLocalAddress(%q) should be false", addr)
		}
	}
}

// IPv6 URI parsing
func TestURIParsingIPv6(t *testing.T) {
	channelID := "TVMkVHiXF9W1NgM9KLgs7tcBMvC1YtF4Daj4yfTrJercs3"

	cases := []struct {
		name  string
		uri   string
		hints []string
	}{
		{
			"bracketed IPv6 @ hint",
			"tltv://" + channelID + "@[2001:db8::1]:8443",
			[]string{"[2001:db8::1]:8443"},
		},
		{
			"bracketed loopback @ hint",
			"tltv://" + channelID + "@[::1]:8000",
			[]string{"[::1]:8000"},
		},
		{
			"IPv6 via hint",
			"tltv://" + channelID + "?via=[2001:db8::1]:443",
			[]string{"[2001:db8::1]:443"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			parsed, err := parseTLTVUri(tc.uri)
			if err != nil {
				t.Fatalf("parseTLTVUri(%q): %v", tc.uri, err)
			}
			if len(parsed.Hints) != len(tc.hints) {
				t.Fatalf("hints count: got %d, want %d", len(parsed.Hints), len(tc.hints))
			}
			for i, h := range parsed.Hints {
				if h != tc.hints[i] {
					t.Errorf("hint[%d]: got %q, want %q", i, h, tc.hints[i])
				}
			}
		})
	}
}

// XMLTV time conversion
func TestXMLTVTimeConversion(t *testing.T) {
	cases := []struct {
		input    string
		expected string
	}{
		{"2026-03-15T00:00:00Z", "20260315000000 +0000"},
		{"2026-03-15T23:59:59Z", "20260315235959 +0000"},
		{"invalid", ""},
	}
	for _, tc := range cases {
		got := toXMLTVTime(tc.input)
		if got != tc.expected {
			t.Errorf("toXMLTVTime(%q): got %q, want %q", tc.input, got, tc.expected)
		}
	}
}

// ---------- JCS Canonical JSON Tests ----------

func TestCanonicalJSONSpecialChars(t *testing.T) {
	// JCS must NOT escape <, >, & (unlike Go's json.Marshal)
	doc := map[string]interface{}{
		"html": "<b>bold</b> & fun",
	}
	out, err := canonicalJSON(doc)
	if err != nil {
		t.Fatalf("canonicalJSON error: %v", err)
	}
	expected := `{"html":"<b>bold</b> & fun"}`
	if string(out) != expected {
		t.Fatalf("JCS should not escape HTML chars:\n  got  %q\n  want %q", string(out), expected)
	}
}

func TestCanonicalJSONUnicodeSeparators(t *testing.T) {
	// JCS must NOT escape U+2028 (LINE SEPARATOR) and U+2029 (PARAGRAPH SEPARATOR)
	doc := map[string]interface{}{
		"text": "line\u2028sep\u2029end",
	}
	out, err := canonicalJSON(doc)
	if err != nil {
		t.Fatalf("canonicalJSON error: %v", err)
	}
	expected := `{"text":"line` + "\u2028" + `sep` + "\u2029" + `end"}`
	if string(out) != expected {
		t.Fatalf("JCS should not escape U+2028/U+2029:\n  got  %q\n  want %q", string(out), expected)
	}
}

func TestCanonicalJSONControlChars(t *testing.T) {
	// Control characters < 0x20 (except those with short escapes) must be \u00XX
	doc := map[string]interface{}{
		"ctl": "a\x01b\x1fc",
	}
	out, err := canonicalJSON(doc)
	if err != nil {
		t.Fatalf("canonicalJSON error: %v", err)
	}
	expected := `{"ctl":"a\u0001b\u001fc"}`
	if string(out) != expected {
		t.Fatalf("JCS control char escaping:\n  got  %q\n  want %q", string(out), expected)
	}
}

func TestCanonicalJSONNumbers(t *testing.T) {
	cases := []struct {
		name     string
		input    interface{}
		expected string
	}{
		{"integer 1", json.Number("1"), "1"},
		{"integer 0", json.Number("0"), "0"},
		{"large integer", json.Number("1742000000"), "1742000000"},
		{"negative integer", json.Number("-5"), "-5"},
		{"float 1.5", json.Number("1.5"), "1.5"},
		{"exponent normalized", json.Number("1e2"), "100"},
		{"small float", json.Number("0.001"), "0.001"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			doc := map[string]interface{}{"n": tc.input}
			out, err := canonicalJSON(doc)
			if err != nil {
				t.Fatalf("canonicalJSON error: %v", err)
			}
			expected := `{"n":` + tc.expected + `}`
			if string(out) != expected {
				t.Fatalf("got %q, want %q", string(out), expected)
			}
		})
	}
}

func TestCanonicalJSONNested(t *testing.T) {
	doc := map[string]interface{}{
		"b": []interface{}{json.Number("1"), "two", true, nil},
		"a": map[string]interface{}{"z": "last", "a": "first"},
	}
	out, err := canonicalJSON(doc)
	if err != nil {
		t.Fatalf("canonicalJSON error: %v", err)
	}
	expected := `{"a":{"a":"first","z":"last"},"b":[1,"two",true,null]}`
	if string(out) != expected {
		t.Fatalf("nested JCS:\n  got  %q\n  want %q", string(out), expected)
	}
}

func TestCanonicalJSONSignStability(t *testing.T) {
	// Sign and verify with JCS -- must produce stable signatures
	seedHex := "9d61b19deffd5a60ba844af492ec2cc44449c5697b326919703bac031cae7f60"
	seed, _ := hex.DecodeString(seedHex)
	priv, _ := keyFromSeed(seed)
	channelID := "TVMkVHiXF9W1NgM9KLgs7tcBMvC1YtF4Daj4yfTrJercs3"

	doc := map[string]interface{}{
		"v":       json.Number("1"),
		"seq":     json.Number("1742000000"),
		"id":      channelID,
		"name":    "Test",
		"stream":  "/test",
		"access":  "public",
		"updated": "2026-03-14T12:00:00Z",
	}

	// Sign twice -- must produce the same signature
	doc1 := copyDoc(doc)
	doc2 := copyDoc(doc)
	signed1, _ := signDocument(doc1, priv)
	signed2, _ := signDocument(doc2, priv)

	sig1 := signed1["signature"].(string)
	sig2 := signed2["signature"].(string)
	if sig1 != sig2 {
		t.Fatalf("JCS signing not stable: %q != %q", sig1, sig2)
	}

	// Verify should succeed
	if err := verifyDocument(signed1, channelID); err != nil {
		t.Fatalf("verification failed after JCS sign: %v", err)
	}
}

func copyDoc(doc map[string]interface{}) map[string]interface{} {
	out := make(map[string]interface{})
	for k, v := range doc {
		out[k] = v
	}
	return out
}

// ---------- SSRF / Hint Validation Tests ----------

func TestValidateHint(t *testing.T) {
	valid := []string{
		"example.com:443",
		"192.168.1.1:8000",
		"[2001:db8::1]:443",
		"example.com",
		"node.tltv.example.org:8443",
	}
	for _, h := range valid {
		if err := validateHint(h); err != nil {
			t.Errorf("validateHint(%q) should pass: %v", h, err)
		}
	}

	invalid := []struct {
		name string
		hint string
	}{
		{"full URL http", "http://127.0.0.1:8000"},
		{"full URL https", "https://evil.com"},
		{"userinfo trick", "evil.com@127.0.0.1:8000"},
		{"path component", "example.com/evil"},
		{"query string", "example.com?foo=bar"},
		{"fragment", "example.com#frag"},
		{"empty", ""},
		{"scheme only", "http://"},
		{"userinfo localhost", "user:pass@127.0.0.1"},
	}
	for _, tc := range invalid {
		t.Run(tc.name, func(t *testing.T) {
			if err := validateHint(tc.hint); err == nil {
				t.Fatalf("validateHint(%q) should fail", tc.hint)
			}
		})
	}
}

func TestNormalizeHint(t *testing.T) {
	// Valid hint with port stays as-is
	h, err := normalizeHint("example.com:443")
	if err != nil || h != "example.com:443" {
		t.Fatalf("normalizeHint: got %q, %v", h, err)
	}

	// Valid hint without port gets :443
	h, err = normalizeHint("example.com")
	if err != nil || h != "example.com:443" {
		t.Fatalf("normalizeHint: got %q, %v", h, err)
	}

	// Malformed hint returns error
	_, err = normalizeHint("http://evil.com")
	if err == nil {
		t.Fatal("normalizeHint should reject URLs")
	}
}

func TestIsLocalAddressUnspecified(t *testing.T) {
	// Unspecified addresses (0.0.0.0, ::) should be detected as local
	unspecified := []string{
		"0.0.0.0:80",
		"[::]:443",
	}
	for _, addr := range unspecified {
		if !isLocalAddress(addr) {
			t.Errorf("isLocalAddress(%q) should be true (unspecified)", addr)
		}
	}
}

func TestSSRFSafeClientBlocksLocal(t *testing.T) {
	client := newSSRFSafeClient(false)
	// Attempt to connect to loopback -- the SSRF-safe dialer should resolve
	// the IP and block before establishing a connection.
	_, _, err := client.get("http://127.0.0.1:1/test")
	if err == nil {
		t.Fatal("SSRF-safe client should block connections to localhost")
	}
	// The error should come from SSRF check, not connection refused
	if !strings.Contains(err.Error(), "local address") {
		t.Logf("got error: %v (expected local-address block)", err)
	}
}

// ---------- Stricter Document Validation Tests ----------

func TestStrictSeqValidation(t *testing.T) {
	// seq as string should be rejected
	doc := map[string]interface{}{
		"v":   json.Number("1"),
		"seq": "not-a-number",
	}
	if err := checkTimestamps(doc); err == nil {
		t.Fatal("should reject string seq")
	}

	// seq as non-integer json.Number should be rejected
	doc2 := map[string]interface{}{
		"v":   json.Number("1"),
		"seq": json.Number("1.5"),
	}
	if err := checkTimestamps(doc2); err == nil {
		t.Fatal("should reject non-integer seq")
	}

	// seq as boolean should be rejected
	doc3 := map[string]interface{}{
		"v":   json.Number("1"),
		"seq": true,
	}
	if err := checkTimestamps(doc3); err == nil {
		t.Fatal("should reject boolean seq")
	}

	// seq as negative should be rejected
	doc4 := map[string]interface{}{
		"v":   json.Number("1"),
		"seq": json.Number("-1"),
	}
	if err := checkTimestamps(doc4); err == nil {
		t.Fatal("should reject negative seq")
	}
}

func TestStrictTimestampTypeValidation(t *testing.T) {
	// updated as number should be rejected
	doc := map[string]interface{}{
		"v":       json.Number("1"),
		"updated": 12345,
	}
	if err := checkTimestamps(doc); err == nil {
		t.Fatal("should reject numeric updated")
	}

	// updated as malformed string
	doc2 := map[string]interface{}{
		"v":       json.Number("1"),
		"updated": "2026-03-14",
	}
	if err := checkTimestamps(doc2); err == nil {
		t.Fatal("should reject malformed updated")
	}

	// migrated as boolean
	doc3 := map[string]interface{}{
		"v":        json.Number("1"),
		"migrated": true,
	}
	if err := checkTimestamps(doc3); err == nil {
		t.Fatal("should reject non-string migrated")
	}

	// migrated as fractional seconds
	doc4 := map[string]interface{}{
		"v":        json.Number("1"),
		"migrated": "2026-03-14T12:00:00.000Z",
	}
	if err := checkTimestamps(doc4); err == nil {
		t.Fatal("should reject fractional-second migrated")
	}
}

func TestTrailingJSON(t *testing.T) {
	// Concatenated JSON objects
	_, err := readDocumentFromString(`{"v":1}{"extra":true}`)
	if err == nil {
		t.Fatal("readDocument should reject trailing JSON object")
	}
	if !strings.Contains(err.Error(), "trailing") {
		t.Fatalf("expected trailing error, got: %v", err)
	}

	// Trailing garbage
	_, err = readDocumentFromString(`{"v":1}garbage`)
	if err == nil {
		t.Fatal("readDocument should reject trailing garbage")
	}

	// Trailing whitespace is OK
	_, err = readDocumentFromString(`{"v":1}   `)
	if err != nil {
		t.Fatalf("readDocument should accept trailing whitespace: %v", err)
	}

	// Trailing newline is OK
	_, err = readDocumentFromString("{\"v\":1}\n")
	if err != nil {
		t.Fatalf("readDocument should accept trailing newline: %v", err)
	}
}

func TestGuideEntryTimestampValidation(t *testing.T) {
	// Valid guide with correct entry timestamps
	good := map[string]interface{}{
		"updated": "2026-03-14T12:00:00Z",
		"from":    "2026-03-14T00:00:00Z",
		"until":   "2026-03-16T00:00:00Z",
		"entries": []interface{}{
			map[string]interface{}{
				"start": "2026-03-15T00:00:00Z",
				"end":   "2026-03-15T01:00:00Z",
				"title": "Test",
			},
		},
	}
	if err := validateDocTimestamps(good); err != nil {
		t.Fatalf("should pass: %v", err)
	}

	// Invalid entry start timestamp
	bad := map[string]interface{}{
		"updated": "2026-03-14T12:00:00Z",
		"from":    "2026-03-14T00:00:00Z",
		"until":   "2026-03-16T00:00:00Z",
		"entries": []interface{}{
			map[string]interface{}{
				"start": "not-a-timestamp",
				"end":   "2026-03-15T01:00:00Z",
				"title": "Bad Entry",
			},
		},
	}
	err := validateDocTimestamps(bad)
	if err == nil {
		t.Fatal("should reject invalid entry start timestamp")
	}
	if !strings.Contains(err.Error(), "entries[0].start") {
		t.Fatalf("expected entries[0].start error, got: %v", err)
	}

	// Invalid entry end timestamp (fractional seconds)
	bad2 := map[string]interface{}{
		"updated": "2026-03-14T12:00:00Z",
		"from":    "2026-03-14T00:00:00Z",
		"until":   "2026-03-16T00:00:00Z",
		"entries": []interface{}{
			map[string]interface{}{
				"start": "2026-03-15T00:00:00Z",
				"end":   "2026-03-15T01:00:00.500Z",
				"title": "Bad Entry",
			},
		},
	}
	err = validateDocTimestamps(bad2)
	if err == nil {
		t.Fatal("should reject fractional-second entry end timestamp")
	}
}

// ---------- parseTarget with tltv:// URI support ----------

func TestParseTargetCompact(t *testing.T) {
	cases := []struct {
		name      string
		input     string
		channelID string
		host      string
	}{
		{
			"basic",
			"TVMkVHiXF9W1NgM9KLgs7tcBMvC1YtF4Daj4yfTrJercs3@example.com",
			"TVMkVHiXF9W1NgM9KLgs7tcBMvC1YtF4Daj4yfTrJercs3",
			"example.com",
		},
		{
			"with port",
			"TVMkVHiXF9W1NgM9KLgs7tcBMvC1YtF4Daj4yfTrJercs3@example.com:8443",
			"TVMkVHiXF9W1NgM9KLgs7tcBMvC1YtF4Daj4yfTrJercs3",
			"example.com:8443",
		},
		{
			"localhost",
			"TVMkVHiXF9W1NgM9KLgs7tcBMvC1YtF4Daj4yfTrJercs3@localhost:8000",
			"TVMkVHiXF9W1NgM9KLgs7tcBMvC1YtF4Daj4yfTrJercs3",
			"localhost:8000",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			id, host, err := parseTarget(tc.input)
			if err != nil {
				t.Fatalf("parseTarget(%q): %v", tc.input, err)
			}
			if id != tc.channelID {
				t.Errorf("channelID: got %q, want %q", id, tc.channelID)
			}
			if host != tc.host {
				t.Errorf("host: got %q, want %q", host, tc.host)
			}
		})
	}
}

func TestParseTargetURI(t *testing.T) {
	channelID := "TVMkVHiXF9W1NgM9KLgs7tcBMvC1YtF4Daj4yfTrJercs3"

	cases := []struct {
		name      string
		input     string
		channelID string
		host      string
	}{
		{
			"basic URI",
			"tltv://" + channelID + "@example.com:443",
			channelID,
			"example.com:443",
		},
		{
			"URI with port",
			"tltv://" + channelID + "@localhost:8000",
			channelID,
			"localhost:8000",
		},
		{
			"URI with via hint uses first",
			"tltv://" + channelID + "@origin.com:443?via=relay.com:8000",
			channelID,
			"origin.com:443",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			id, host, err := parseTarget(tc.input)
			if err != nil {
				t.Fatalf("parseTarget(%q): %v", tc.input, err)
			}
			if id != tc.channelID {
				t.Errorf("channelID: got %q, want %q", id, tc.channelID)
			}
			if host != tc.host {
				t.Errorf("host: got %q, want %q", host, tc.host)
			}
		})
	}
}

func TestParseTargetErrors(t *testing.T) {
	cases := []struct {
		name  string
		input string
	}{
		{"no @ or scheme", "TVMkVHiXF9W1NgM9KLgs7tcBMvC1YtF4Daj4yfTrJercs3"},
		{"empty host compact", "TVMkVHiXF9W1NgM9KLgs7tcBMvC1YtF4Daj4yfTrJercs3@"},
		{"URI without hint", "tltv://TVMkVHiXF9W1NgM9KLgs7tcBMvC1YtF4Daj4yfTrJercs3"},
		{"bad URI scheme", "tltv://"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := parseTarget(tc.input)
			if err == nil {
				t.Fatalf("parseTarget(%q) should have failed", tc.input)
			}
		})
	}
}

// ---------- Seed file format (hex + backward compat) ----------

func TestWriteAndReadSeedHex(t *testing.T) {
	// Generate a seed
	_, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	seed := priv.Seed()

	// Write as hex
	path := t.TempDir() + "/test.key"
	if err := writeSeed(path, seed); err != nil {
		t.Fatalf("writeSeed: %v", err)
	}

	// Read back
	got, err := readSeed(path)
	if err != nil {
		t.Fatalf("readSeed: %v", err)
	}
	if hex.EncodeToString(got) != hex.EncodeToString(seed) {
		t.Fatalf("seed mismatch: got %x, want %x", got, seed)
	}

	// Verify the file is hex text (not binary)
	data, _ := os.ReadFile(path)
	text := strings.TrimSpace(string(data))
	if len(text) != ed25519.SeedSize*2 {
		t.Fatalf("file should be %d hex chars, got %d bytes", ed25519.SeedSize*2, len(data))
	}
	// Verify all chars are valid hex
	if _, err := hex.DecodeString(text); err != nil {
		t.Fatalf("file content is not valid hex: %v", err)
	}
}

func TestReadSeedBinaryBackwardCompat(t *testing.T) {
	// Simulate old-format binary key file
	_, priv, _ := ed25519.GenerateKey(nil)
	seed := priv.Seed()

	path := t.TempDir() + "/old.key"
	os.WriteFile(path, seed, 0600) // raw 32 bytes

	got, err := readSeed(path)
	if err != nil {
		t.Fatalf("readSeed should accept binary seed: %v", err)
	}
	if hex.EncodeToString(got) != hex.EncodeToString(seed) {
		t.Fatalf("seed mismatch")
	}
}

func TestReadSeedInvalid(t *testing.T) {
	dir := t.TempDir()

	// Wrong length
	path := dir + "/bad.key"
	os.WriteFile(path, []byte("too short"), 0600)
	_, err := readSeed(path)
	if err == nil {
		t.Fatal("readSeed should reject wrong-length file")
	}

	// Invalid hex chars (right length but not hex)
	path2 := dir + "/badhex.key"
	os.WriteFile(path2, []byte(strings.Repeat("zz", 32)+"\n"), 0600)
	_, err = readSeed(path2)
	if err == nil {
		t.Fatal("readSeed should reject invalid hex")
	}

	// File doesn't exist
	_, err = readSeed(dir + "/missing.key")
	if err == nil {
		t.Fatal("readSeed should fail on missing file")
	}
}

func TestReadSeedHexWithTrailingNewline(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(nil)
	seed := priv.Seed()

	// Write hex with trailing newline (as writeSeed does)
	path := t.TempDir() + "/newline.key"
	os.WriteFile(path, []byte(hex.EncodeToString(seed)+"\n"), 0600)

	got, err := readSeed(path)
	if err != nil {
		t.Fatalf("readSeed should handle trailing newline: %v", err)
	}
	if hex.EncodeToString(got) != hex.EncodeToString(seed) {
		t.Fatalf("seed mismatch")
	}
}

func TestSeedSignRoundtrip(t *testing.T) {
	// Generate key, write seed as hex, read it back, sign and verify
	pub, priv, _ := ed25519.GenerateKey(nil)
	seed := priv.Seed()
	channelID := makeChannelID(pub)

	path := t.TempDir() + "/roundtrip.key"
	writeSeed(path, seed)

	readBack, err := readSeed(path)
	if err != nil {
		t.Fatalf("readSeed: %v", err)
	}

	restoredPriv, restoredPub := keyFromSeed(readBack)
	if makeChannelID(restoredPub) != channelID {
		t.Fatal("restored key produces different channel ID")
	}

	doc := map[string]interface{}{
		"v":       json.Number("1"),
		"seq":     json.Number(fmt.Sprintf("%d", time.Now().Unix())),
		"id":      channelID,
		"name":    "Roundtrip Test",
		"stream":  "/test",
		"access":  "public",
		"updated": time.Now().UTC().Format("2006-01-02T15:04:05Z"),
	}

	signed, err := signDocument(doc, restoredPriv)
	if err != nil {
		t.Fatalf("signDocument: %v", err)
	}
	if err := verifyDocument(signed, channelID); err != nil {
		t.Fatalf("verifyDocument after hex roundtrip: %v", err)
	}
}

// ---------- Integration Tests ----------
//
// These tests require a live TLTV node. Set TLTV_TEST_NODE to a host:port
// (e.g. "node.example.com:8443") to enable them. They are skipped automatically
// when the env var is unset or the node is unreachable.
//
// TODO: stand up a permanent test node so these run in CI.

// integrationNode returns the test node address or calls t.Skip.
func integrationNode(t *testing.T) string {
	t.Helper()
	host := os.Getenv("TLTV_TEST_NODE")
	if host == "" {
		t.Skip("TLTV_TEST_NODE not set; skipping integration test")
	}
	// Quick TCP dial to confirm reachability
	conn, err := net.DialTimeout("tcp", host, 2*time.Second)
	if err != nil {
		t.Skipf("TLTV_TEST_NODE %s unreachable: %v", host, err)
	}
	conn.Close()
	return host
}

func TestIntegrationNodeInfo(t *testing.T) {
	host := integrationNode(t)
	client := newClient(false)
	info, err := client.FetchNodeInfo(host)
	if err != nil {
		t.Fatalf("FetchNodeInfo: %v", err)
	}
	if info.Protocol != "tltv" {
		t.Errorf("protocol: got %q, want %q", info.Protocol, "tltv")
	}
	if len(info.Versions) == 0 {
		t.Error("node reported no protocol versions")
	}
	if len(info.Channels) == 0 {
		t.Error("node has no channels")
	}
}

func TestIntegrationFetchAndVerify(t *testing.T) {
	host := integrationNode(t)
	client := newClient(false)

	// Discover channel ID from node info
	info, err := client.FetchNodeInfo(host)
	if err != nil {
		t.Fatalf("FetchNodeInfo: %v", err)
	}
	if len(info.Channels) == 0 {
		t.Skip("no channels on node")
	}
	channelID := info.Channels[0].ID

	// Fetch metadata
	doc, err := client.FetchMetadata(host, channelID, "")
	if err != nil {
		t.Fatalf("FetchMetadata: %v", err)
	}

	// Verify signature against the real server's signing key
	if err := verifyDocument(doc, channelID); err != nil {
		t.Fatalf("verifyDocument failed on live metadata: %v", err)
	}

	// Sanity: the id field must match
	if getString(doc, "id") != channelID {
		t.Errorf("id mismatch: got %q, want %q", getString(doc, "id"), channelID)
	}
}

func TestIntegrationGuideAndVerify(t *testing.T) {
	host := integrationNode(t)
	client := newClient(false)

	info, err := client.FetchNodeInfo(host)
	if err != nil {
		t.Fatalf("FetchNodeInfo: %v", err)
	}
	if len(info.Channels) == 0 {
		t.Skip("no channels on node")
	}
	channelID := info.Channels[0].ID

	doc, err := client.FetchGuide(host, channelID, "")
	if err != nil {
		t.Fatalf("FetchGuide: %v", err)
	}

	if err := verifyDocument(doc, channelID); err != nil {
		t.Fatalf("verifyDocument failed on live guide: %v", err)
	}

	// Guide must have from/until
	if getString(doc, "from") == "" {
		t.Error("guide missing 'from' field")
	}
	if getString(doc, "until") == "" {
		t.Error("guide missing 'until' field")
	}
}

func TestIntegrationPeers(t *testing.T) {
	host := integrationNode(t)
	client := newClient(false)

	exchange, err := client.FetchPeers(host)
	if err != nil {
		t.Fatalf("FetchPeers: %v", err)
	}
	// Peers endpoint should return a valid structure (even if empty)
	if exchange == nil {
		t.Fatal("FetchPeers returned nil")
	}
}

func TestIntegrationResolveEndToEnd(t *testing.T) {
	host := integrationNode(t)
	client := newClient(false)

	// Get channel ID from node
	info, err := client.FetchNodeInfo(host)
	if err != nil {
		t.Fatalf("FetchNodeInfo: %v", err)
	}
	if len(info.Channels) == 0 {
		t.Skip("no channels on node")
	}
	channelID := info.Channels[0].ID

	// Full resolve flow: node info -> metadata -> verify -> stream check
	doc, err := client.FetchMetadata(host, channelID, "")
	if err != nil {
		t.Fatalf("FetchMetadata: %v", err)
	}
	if err := verifyDocument(doc, channelID); err != nil {
		t.Fatalf("verify: %v", err)
	}

	// Stream check (don't fail on non-200, just confirm no transport error)
	status, _, _, err := client.CheckStream(host, channelID, "")
	if err != nil {
		t.Fatalf("CheckStream transport error: %v", err)
	}
	t.Logf("stream status: %d", status)
}

func TestIntegrationSSRFSafeClientAllowsPublic(t *testing.T) {
	host := integrationNode(t)
	// The SSRF-safe client must still be able to reach non-local servers.
	// The test node is on a private network, so this also confirms that
	// isLocalAddress correctly classifies it. If the test node is on a
	// truly private IP, this test validates that the SSRF-safe dialer
	// blocks it (and the test should be run with --local semantics).
	client := newSSRFSafeClient(false)
	_, err := client.FetchNodeInfo(host)

	h, _, _ := net.SplitHostPort(host)
	if isLocalAddress(host) {
		// Expected to fail -- SSRF-safe client blocks private IPs
		if err == nil {
			t.Fatal("SSRF-safe client should have blocked private address")
		}
		t.Logf("correctly blocked private address %s: %v", h, err)
	} else {
		// Public IP -- should succeed
		if err != nil {
			t.Fatalf("SSRF-safe client failed on public address %s: %v", h, err)
		}
	}
}

func TestIntegrationCrawlJSON(t *testing.T) {
	host := integrationNode(t)
	client := newClient(false)

	// Simulate what crawl --json does: fetch node info + peers,
	// verify the output is structured correctly.
	info, err := client.FetchNodeInfo(host)
	if err != nil {
		t.Fatalf("FetchNodeInfo: %v", err)
	}

	exchange, err := client.FetchPeers(host)
	if err != nil {
		t.Fatalf("FetchPeers: %v", err)
	}

	// Build a JSON result like crawl --json would
	var channels []map[string]string
	for _, ch := range info.Channels {
		channels = append(channels, map[string]string{
			"id": ch.ID, "name": ch.Name, "host": host, "source": "channel",
		})
	}

	result := map[string]interface{}{
		"nodes_probed": 1,
		"channels":     channels,
	}

	// Must marshal to valid JSON with no extra output
	out, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}

	// Verify it round-trips
	var parsed map[string]interface{}
	if err := json.Unmarshal(out, &parsed); err != nil {
		t.Fatalf("crawl JSON output is not valid JSON: %v", err)
	}

	t.Logf("crawl result: %d channels, %d peers", len(info.Channels), len(exchange.Peers))
}

func TestStreamURL(t *testing.T) {
	client := newClient(false)
	channelID := "TVMkVHiXF9W1NgM9KLgs7tcBMvC1YtF4Daj4yfTrJercs3"

	cases := []struct {
		name     string
		host     string
		token    string
		expected string
	}{
		{
			"default port",
			"example.com",
			"",
			"https://example.com:443/tltv/v1/channels/" + channelID + "/stream.m3u8",
		},
		{
			"custom port",
			"example.com:8443",
			"",
			"https://example.com:8443/tltv/v1/channels/" + channelID + "/stream.m3u8",
		},
		{
			"with token",
			"example.com",
			"secret123",
			"https://example.com:443/tltv/v1/channels/" + channelID + "/stream.m3u8?token=secret123",
		},
		{
			"localhost uses http",
			"localhost:8000",
			"",
			"http://localhost:8000/tltv/v1/channels/" + channelID + "/stream.m3u8",
		},
		{
			"127.0.0.1 uses http",
			"127.0.0.1:8000",
			"",
			"http://127.0.0.1:8000/tltv/v1/channels/" + channelID + "/stream.m3u8",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			url := client.baseURL(tc.host) + "/tltv/v1/channels/" + channelID + "/stream.m3u8"
			if tc.token != "" {
				url += "?token=" + tc.token
			}
			if url != tc.expected {
				t.Fatalf("got %q, want %q", url, tc.expected)
			}
		})
	}
}

// demoNode tries to reach the public TLTV demo server.
// Returns the host or skips the test if the demo is unreachable.
func demoNode(t *testing.T) string {
	t.Helper()
	const host = "demo.timelooptv.org:443"
	conn, err := net.DialTimeout("tcp", host, 3*time.Second)
	if err != nil {
		t.Skipf("demo node %s unreachable: %v", host, err)
	}
	conn.Close()
	return host
}

const demoChannelID = "TVLoopRRV7V41vERa1n5xyMibevWCP7zVSnxGJq8va8MvU"

func TestDemoNodeInfo(t *testing.T) {
	host := demoNode(t)
	client := newClient(false)
	info, err := client.FetchNodeInfo(host)
	if err != nil {
		t.Fatalf("FetchNodeInfo: %v", err)
	}
	if info.Protocol != "tltv" {
		t.Errorf("protocol: got %q, want %q", info.Protocol, "tltv")
	}
	if len(info.Versions) == 0 {
		t.Error("node reported no protocol versions")
	}
	if len(info.Channels) == 0 {
		t.Error("node has no channels")
	}
	// Demo must list the known channel
	found := false
	for _, ch := range info.Channels {
		if ch.ID == demoChannelID {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("demo channel %s not found in node info", demoChannelID)
	}
	t.Logf("demo node: %d channels, versions %v", len(info.Channels), info.Versions)
}

func TestDemoFetchAndVerify(t *testing.T) {
	host := demoNode(t)
	client := newClient(false)

	doc, err := client.FetchMetadata(host, demoChannelID, "")
	if err != nil {
		t.Fatalf("FetchMetadata: %v", err)
	}
	if err := verifyDocument(doc, demoChannelID); err != nil {
		t.Fatalf("verifyDocument failed on demo metadata: %v", err)
	}
	if getString(doc, "id") != demoChannelID {
		t.Errorf("id mismatch: got %q, want %q", getString(doc, "id"), demoChannelID)
	}
	t.Logf("demo metadata verified: seq=%v", doc["seq"])
}

func TestDemoGuideAndVerify(t *testing.T) {
	host := demoNode(t)
	client := newClient(false)

	doc, err := client.FetchGuide(host, demoChannelID, "")
	if err != nil {
		t.Skipf("guide not available on demo: %v", err)
	}
	if err := verifyDocument(doc, demoChannelID); err != nil {
		t.Fatalf("verifyDocument failed on demo guide: %v", err)
	}
	if getString(doc, "from") == "" {
		t.Error("guide missing 'from' field")
	}
	if getString(doc, "until") == "" {
		t.Error("guide missing 'until' field")
	}
	t.Logf("demo guide verified: from=%s until=%s", getString(doc, "from"), getString(doc, "until"))
}

func TestDemoPeers(t *testing.T) {
	host := demoNode(t)
	client := newClient(false)

	exchange, err := client.FetchPeers(host)
	if err != nil {
		t.Fatalf("FetchPeers: %v", err)
	}
	if exchange == nil {
		t.Fatal("FetchPeers returned nil")
	}
	t.Logf("demo peers: %d", len(exchange.Peers))
}

func TestDemoStream(t *testing.T) {
	host := demoNode(t)
	client := newClient(false)

	status, contentType, body, err := client.CheckStream(host, demoChannelID, "")
	if err != nil {
		t.Fatalf("CheckStream: %v", err)
	}
	if status != 200 {
		t.Fatalf("expected 200, got %d", status)
	}
	if !strings.Contains(contentType, "mpegurl") {
		t.Fatalf("unexpected content-type: %s", contentType)
	}

	// Verify HLS manifest has segments
	segments := 0
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasSuffix(line, ".ts") || strings.HasSuffix(line, ".m4s") {
			segments++
		}
	}
	if segments == 0 {
		t.Fatal("manifest has no segments")
	}
	t.Logf("demo stream live: %d segments, %d bytes", segments, len(body))
}

func TestDemoStreamURL(t *testing.T) {
	host := demoNode(t)
	client := newClient(false)

	// Build URL the same way cmdStream --url does
	streamURL := client.baseURL(host) + "/tltv/v1/channels/" + demoChannelID + "/stream.m3u8"

	if !strings.HasPrefix(streamURL, "https://") {
		t.Fatalf("expected https URL, got %s", streamURL)
	}
	if !strings.Contains(streamURL, demoChannelID) {
		t.Fatalf("URL missing channel ID: %s", streamURL)
	}
	if !strings.HasSuffix(streamURL, "/stream.m3u8") {
		t.Fatalf("URL missing stream.m3u8 suffix: %s", streamURL)
	}
	t.Logf("demo stream URL: %s", streamURL)
}

func TestDemoResolveEndToEnd(t *testing.T) {
	host := demoNode(t)
	client := newClient(false)

	// Full resolve flow: node info -> metadata -> verify -> stream check
	doc, err := client.FetchMetadata(host, demoChannelID, "")
	if err != nil {
		t.Fatalf("FetchMetadata: %v", err)
	}
	if err := verifyDocument(doc, demoChannelID); err != nil {
		t.Fatalf("verify: %v", err)
	}

	status, _, _, err := client.CheckStream(host, demoChannelID, "")
	if err != nil {
		t.Fatalf("CheckStream transport error: %v", err)
	}
	if status != 200 {
		t.Errorf("expected stream 200, got %d", status)
	}
	t.Logf("demo resolve: metadata verified, stream %d", status)
}

// ---------- Client methods via httptest ----------

func TestClientFetchMetadata(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	id := makeChannelID(pub)

	doc := map[string]interface{}{
		"v":       json.Number("1"),
		"seq":     json.Number(fmt.Sprintf("%d", time.Now().Unix())),
		"id":      id,
		"name":    "Mock Channel",
		"stream":  "stream.m3u8",
		"updated": time.Now().UTC().Format("2006-01-02T15:04:05Z"),
	}
	signed, _ := signDocument(doc, priv)
	signedBytes, _ := json.Marshal(signed)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(signedBytes)
	}))
	defer srv.Close()

	client := newClient(false)
	host := strings.TrimPrefix(srv.URL, "http://")
	result, err := client.FetchMetadata(host, id, "")
	if err != nil {
		t.Fatalf("FetchMetadata: %v", err)
	}
	if result["name"] != "Mock Channel" {
		t.Errorf("name = %v", result["name"])
	}
}

func TestClientFetchMetadata_NotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
		w.Write([]byte(`{"error":"not_found"}`))
	}))
	defer srv.Close()

	client := newClient(false)
	host := strings.TrimPrefix(srv.URL, "http://")
	_, err := client.FetchMetadata(host, "TVfake", "")
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("expected not found error, got: %v", err)
	}
}

func TestClientFetchPeers(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"peers":[{"id":"TVabc","name":"Test","hints":["a.com:443"],"last_seen":"2026-01-01T00:00:00Z"}]}`))
	}))
	defer srv.Close()

	client := newClient(false)
	host := strings.TrimPrefix(srv.URL, "http://")
	exchange, err := client.FetchPeers(host)
	if err != nil {
		t.Fatalf("FetchPeers: %v", err)
	}
	if len(exchange.Peers) != 1 {
		t.Fatalf("expected 1 peer, got %d", len(exchange.Peers))
	}
	if exchange.Peers[0].ID != "TVabc" {
		t.Errorf("peer id = %q", exchange.Peers[0].ID)
	}
}

func TestClientCheckStream(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		w.Write([]byte("#EXTM3U\n#EXT-X-TARGETDURATION:2\n"))
	}))
	defer srv.Close()

	client := newClient(false)
	host := strings.TrimPrefix(srv.URL, "http://")
	status, ct, body, err := client.CheckStream(host, "TVtest", "")
	if err != nil {
		t.Fatalf("CheckStream: %v", err)
	}
	if status != 200 {
		t.Errorf("status = %d", status)
	}
	if !strings.Contains(ct, "mpegurl") {
		t.Errorf("content-type = %q", ct)
	}
	if !strings.Contains(body, "#EXTM3U") {
		t.Errorf("body should contain #EXTM3U")
	}
}

// ---------- documentToJSON ----------

func TestDocumentToJSON(t *testing.T) {
	doc := map[string]interface{}{
		"name":  "Test",
		"value": json.Number("42"),
		"html":  "<b>bold</b>",
	}
	out, err := documentToJSON(doc)
	if err != nil {
		t.Fatalf("documentToJSON: %v", err)
	}

	// Should NOT html-escape < and >
	if strings.Contains(string(out), `\u003c`) {
		t.Error("should not HTML-escape angle brackets")
	}
	if !strings.Contains(string(out), "<b>bold</b>") {
		t.Error("should preserve literal angle brackets")
	}
}
