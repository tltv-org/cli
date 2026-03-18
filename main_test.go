package main

import (
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"fmt"
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
		name      string
		uri       string
		id        string
		hints     []string
		token     string
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
		name   string
		hints  []string
		token  string
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
