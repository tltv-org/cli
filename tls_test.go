package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"flag"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ---------- padCoordinate ----------

func TestPadCoordinate(t *testing.T) {
	tests := []struct {
		name string
		in   []byte
		size int
		want int // expected output length
	}{
		{"short", []byte{0x01, 0x02}, 32, 32},
		{"exact", make([]byte, 32), 32, 32},
		{"longer", make([]byte, 33), 32, 33},
		{"empty", []byte{}, 32, 32},
		{"single", []byte{0xFF}, 4, 4},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := padCoordinate(tt.in, tt.size)
			if len(out) != tt.want {
				t.Errorf("len = %d, want %d", len(out), tt.want)
			}
			// Verify original bytes are at the end (right-aligned).
			if tt.size >= len(tt.in) && len(tt.in) > 0 {
				offset := len(out) - len(tt.in)
				for i, b := range tt.in {
					if out[offset+i] != b {
						t.Errorf("byte[%d] = %02x, want %02x", offset+i, out[offset+i], b)
					}
				}
				// Verify leading bytes are zero.
				for i := 0; i < offset; i++ {
					if out[i] != 0 {
						t.Errorf("padding byte[%d] = %02x, want 0", i, out[i])
					}
				}
			}
		})
	}
}

// ---------- JWK Thumbprint ----------

func TestJWKThumbprint(t *testing.T) {
	// Generate a key and verify the thumbprint is deterministic and well-formed.
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	mgr := &acmeManager{accountKey: key}

	thumb1, err := mgr.jwkThumbprint()
	if err != nil {
		t.Fatal(err)
	}
	thumb2, err := mgr.jwkThumbprint()
	if err != nil {
		t.Fatal(err)
	}

	if thumb1 != thumb2 {
		t.Error("thumbprint not deterministic")
	}

	// Must be valid base64url.
	decoded, err := base64.RawURLEncoding.DecodeString(thumb1)
	if err != nil {
		t.Fatalf("invalid base64url: %v", err)
	}

	// SHA-256 output is 32 bytes.
	if len(decoded) != 32 {
		t.Errorf("decoded length = %d, want 32", len(decoded))
	}
}

func TestJWKThumbprint_KeyOrder(t *testing.T) {
	// The canonical JWK must have keys in lexicographic order: crv, kty, x, y.
	// Verify by checking the jwk() method output.
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	mgr := &acmeManager{accountKey: key}

	jwk := mgr.jwk()

	if jwk["kty"] != "EC" {
		t.Errorf("kty = %q", jwk["kty"])
	}
	if jwk["crv"] != "P-256" {
		t.Errorf("crv = %q", jwk["crv"])
	}

	// x and y must be base64url-encoded 32-byte values.
	for _, field := range []string{"x", "y"} {
		decoded, err := base64.RawURLEncoding.DecodeString(jwk[field])
		if err != nil {
			t.Errorf("%s: invalid base64url: %v", field, err)
		}
		if len(decoded) != 32 {
			t.Errorf("%s: decoded length = %d, want 32 (P-256)", field, len(decoded))
		}
	}
}

// ---------- JWS Signature Format ----------

func TestJWSSignatureFormat(t *testing.T) {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	mgr := &acmeManager{
		accountKey: key,
		accountURL: "https://acme.example/acct/1",
		nonce:      "test-nonce-123",
	}

	// Use doSignedPost with a test server that captures the JWS body.
	var receivedBody []byte
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := make([]byte, r.ContentLength)
		r.Body.Read(body)
		receivedBody = body
		w.Header().Set("Replay-Nonce", "next-nonce")
		w.WriteHeader(200)
		w.Write([]byte(`{}`))
	}))
	defer ts.Close()

	ctx := context.Background()
	_, _, err := mgr.doSignedPost(ctx, ts.URL, map[string]string{"test": "value"}, false)
	if err != nil {
		t.Fatal(err)
	}

	// Parse the JWS.
	var jws map[string]string
	if err := json.Unmarshal(receivedBody, &jws); err != nil {
		t.Fatalf("invalid JWS JSON: %v", err)
	}

	// Must have all three fields.
	for _, field := range []string{"protected", "payload", "signature"} {
		if _, ok := jws[field]; !ok {
			t.Errorf("missing JWS field: %s", field)
		}
	}

	// Protected header must be valid base64url JSON with alg, nonce, url, kid.
	protectedJSON, err := base64.RawURLEncoding.DecodeString(jws["protected"])
	if err != nil {
		t.Fatalf("invalid protected base64url: %v", err)
	}

	var header map[string]interface{}
	if err := json.Unmarshal(protectedJSON, &header); err != nil {
		t.Fatalf("invalid protected JSON: %v", err)
	}

	if header["alg"] != "ES256" {
		t.Errorf("alg = %v, want ES256", header["alg"])
	}
	if header["nonce"] != "test-nonce-123" {
		t.Errorf("nonce = %v", header["nonce"])
	}
	if header["url"] != ts.URL {
		t.Errorf("url = %v", header["url"])
	}
	if header["kid"] != "https://acme.example/acct/1" {
		t.Errorf("kid = %v", header["kid"])
	}

	// Signature must be exactly 64 bytes (P-256: 32+32 for r||s).
	sigBytes, err := base64.RawURLEncoding.DecodeString(jws["signature"])
	if err != nil {
		t.Fatalf("invalid signature base64url: %v", err)
	}
	if len(sigBytes) != 64 {
		t.Errorf("signature length = %d, want 64 (P-256 r||s)", len(sigBytes))
	}
}

func TestJWSPostAsGET(t *testing.T) {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	mgr := &acmeManager{
		accountKey: key,
		accountURL: "https://acme.example/acct/1",
		nonce:      "test-nonce",
	}

	var receivedBody []byte
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := make([]byte, r.ContentLength)
		r.Body.Read(body)
		receivedBody = body
		w.Header().Set("Replay-Nonce", "next")
		w.WriteHeader(200)
		w.Write([]byte(`{}`))
	}))
	defer ts.Close()

	// POST-as-GET: nil payload → empty string payload.
	ctx := context.Background()
	_, _, err := mgr.doSignedPost(ctx, ts.URL, nil, false)
	if err != nil {
		t.Fatal(err)
	}

	var jws map[string]string
	json.Unmarshal(receivedBody, &jws)

	// Payload must be empty string for POST-as-GET.
	if jws["payload"] != "" {
		t.Errorf("POST-as-GET payload = %q, want empty string", jws["payload"])
	}
}

func TestJWSNewAccountUsesJWK(t *testing.T) {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	mgr := &acmeManager{
		accountKey: key,
		nonce:      "test-nonce",
	}

	var receivedBody []byte
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := make([]byte, r.ContentLength)
		r.Body.Read(body)
		receivedBody = body
		w.Header().Set("Replay-Nonce", "next")
		w.WriteHeader(200)
		w.Write([]byte(`{}`))
	}))
	defer ts.Close()

	// useJWK=true for new account registration.
	ctx := context.Background()
	_, _, err := mgr.doSignedPost(ctx, ts.URL, map[string]string{}, true)
	if err != nil {
		t.Fatal(err)
	}

	var jws map[string]string
	json.Unmarshal(receivedBody, &jws)
	protectedJSON, _ := base64.RawURLEncoding.DecodeString(jws["protected"])

	var header map[string]interface{}
	json.Unmarshal(protectedJSON, &header)

	// Should have "jwk" not "kid".
	if _, ok := header["jwk"]; !ok {
		t.Error("new account request should include jwk in header")
	}
	if _, ok := header["kid"]; ok {
		t.Error("new account request should not include kid in header")
	}
}

// ---------- addTLSFlags ----------

func TestAddTLSFlags(t *testing.T) {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	enabled, certFile, keyFile, email, staging := addTLSFlags(fs)

	// Parse with all flags.
	err := fs.Parse([]string{
		"--tls",
		"--tls-cert", "cert.pem",
		"--tls-key", "key.pem",
		"--acme-email", "test@example.com",
		"--tls-staging",
	})
	if err != nil {
		t.Fatal(err)
	}

	if !*enabled {
		t.Error("--tls not set")
	}
	if *certFile != "cert.pem" {
		t.Errorf("--tls-cert = %q", *certFile)
	}
	if *keyFile != "key.pem" {
		t.Errorf("--tls-key = %q", *keyFile)
	}
	if *email != "test@example.com" {
		t.Errorf("--acme-email = %q", *email)
	}
	if !*staging {
		t.Error("--tls-staging not set")
	}
}

func TestAddTLSFlags_Defaults(t *testing.T) {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	enabled, certFile, keyFile, email, staging := addTLSFlags(fs)
	fs.Parse([]string{})

	if *enabled {
		t.Error("--tls should default to false")
	}
	if *certFile != "" {
		t.Errorf("--tls-cert should default to empty, got %q", *certFile)
	}
	if *keyFile != "" {
		t.Errorf("--tls-key should default to empty, got %q", *keyFile)
	}
	if *email != "" {
		t.Errorf("--acme-email should default to empty, got %q", *email)
	}
	if *staging {
		t.Error("--tls-staging should default to false")
	}
}

// ---------- tlsOverrideListenPort ----------

func TestTLSOverrideListenPort_NoExplicit(t *testing.T) {
	// When --listen is not set and LISTEN env is not set, override to :443.
	old := os.Getenv("LISTEN")
	os.Unsetenv("LISTEN")
	defer func() {
		if old != "" {
			os.Setenv("LISTEN", old)
		}
	}()

	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	addr := fs.String("listen", ":8000", "listen")
	fs.Parse([]string{})

	tlsOverrideListenPort(fs, addr)
	if *addr != ":443" {
		t.Errorf("listen = %q, want :443", *addr)
	}
}

func TestTLSOverrideListenPort_ExplicitFlag(t *testing.T) {
	old := os.Getenv("LISTEN")
	os.Unsetenv("LISTEN")
	defer func() {
		if old != "" {
			os.Setenv("LISTEN", old)
		}
	}()

	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	addr := fs.String("listen", ":8000", "listen")
	fs.StringVar(addr, "l", ":8000", "alias")
	fs.Parse([]string{"--listen", ":9999"})

	tlsOverrideListenPort(fs, addr)
	if *addr != ":9999" {
		t.Errorf("explicit --listen should not be overridden, got %q", *addr)
	}
}

func TestTLSOverrideListenPort_ExplicitShortFlag(t *testing.T) {
	old := os.Getenv("LISTEN")
	os.Unsetenv("LISTEN")
	defer func() {
		if old != "" {
			os.Setenv("LISTEN", old)
		}
	}()

	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	addr := fs.String("listen", ":8000", "listen")
	fs.StringVar(addr, "l", ":8000", "alias")
	fs.Parse([]string{"-l", ":7777"})

	tlsOverrideListenPort(fs, addr)
	if *addr != ":7777" {
		t.Errorf("explicit -l should not be overridden, got %q", *addr)
	}
}

func TestTLSOverrideListenPort_EnvVar(t *testing.T) {
	os.Setenv("LISTEN", ":5555")
	defer os.Unsetenv("LISTEN")

	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	addr := fs.String("listen", ":5555", "listen")
	fs.Parse([]string{})

	tlsOverrideListenPort(fs, addr)
	// LISTEN env var is set, so don't override.
	if *addr != ":5555" {
		t.Errorf("LISTEN env should prevent override, got %q", *addr)
	}
}

// ---------- tlsSetup ----------

func TestTLSSetup_Disabled(t *testing.T) {
	cfg, cleanup, err := tlsSetup("", false, "", "", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if cfg != nil {
		t.Error("disabled TLS should return nil config")
	}
	cleanup() // should not panic
}

func TestTLSSetup_ManualCert(t *testing.T) {
	setupLogging("error", "", "", "test")

	certFile, keyFile := generateSelfSignedCert(t)
	defer os.Remove(certFile)
	defer os.Remove(keyFile)

	cfg, cleanup, err := tlsSetup("", true, certFile, keyFile, "", false)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()

	if cfg == nil {
		t.Fatal("manual cert should return non-nil config")
	}
	if len(cfg.Certificates) != 1 {
		t.Errorf("expected 1 certificate, got %d", len(cfg.Certificates))
	}
	if cfg.MinVersion != tls.VersionTLS12 {
		t.Errorf("MinVersion = %d, want TLS 1.2", cfg.MinVersion)
	}
}

func TestTLSSetup_CertWithoutKey(t *testing.T) {
	_, _, err := tlsSetup("", true, "cert.pem", "", "", false)
	if err == nil {
		t.Error("expected error when --tls-cert set without --tls-key")
	}
	if !strings.Contains(err.Error(), "both be specified") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestTLSSetup_KeyWithoutCert(t *testing.T) {
	_, _, err := tlsSetup("", true, "", "key.pem", "", false)
	if err == nil {
		t.Error("expected error when --tls-key set without --tls-cert")
	}
}

func TestTLSSetup_ACMEMissingHostname(t *testing.T) {
	setupLogging("error", "", "", "test")

	_, _, err := tlsSetup("", true, "", "", "", false)
	if err == nil {
		t.Error("expected error when ACME mode without hostname")
	}
	if !strings.Contains(err.Error(), "hostname") {
		t.Errorf("error should mention hostname: %v", err)
	}
}

func TestTLSSetup_InvalidCertFile(t *testing.T) {
	setupLogging("error", "", "", "test")

	_, _, err := tlsSetup("", true, "/nonexistent/cert.pem", "/nonexistent/key.pem", "", false)
	if err == nil {
		t.Error("expected error for nonexistent cert files")
	}
}

// ---------- ACME HTTP Handler ----------

func TestACMEHTTPHandler_Challenge(t *testing.T) {
	mgr := newACMEManager("example.com", "", "", acmeProductionDirectory)

	// Provision a challenge.
	mgr.challenges["test-token-abc"] = "test-token-abc.thumbprint123"

	handler := mgr.HTTPHandler()

	// Request the challenge.
	req := httptest.NewRequest("GET", "/.well-known/acme-challenge/test-token-abc", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Errorf("status = %d, want 200", w.Code)
	}
	if w.Body.String() != "test-token-abc.thumbprint123" {
		t.Errorf("body = %q", w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/octet-stream" {
		t.Errorf("Content-Type = %q", ct)
	}
}

func TestACMEHTTPHandler_UnknownChallenge(t *testing.T) {
	mgr := newACMEManager("example.com", "", "", acmeProductionDirectory)

	handler := mgr.HTTPHandler()

	req := httptest.NewRequest("GET", "/.well-known/acme-challenge/unknown-token", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != 404 {
		t.Errorf("status = %d, want 404 for unknown token", w.Code)
	}
}

func TestACMEHTTPHandler_Redirect(t *testing.T) {
	mgr := newACMEManager("example.com", "", "", acmeProductionDirectory)
	handler := mgr.HTTPHandler()

	req := httptest.NewRequest("GET", "/some/page?q=1", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusMovedPermanently {
		t.Errorf("status = %d, want 301", w.Code)
	}
	loc := w.Header().Get("Location")
	if loc != "https://example.com/some/page?q=1" {
		t.Errorf("redirect Location = %q", loc)
	}
}

func TestACMEHTTPHandler_RedirectRoot(t *testing.T) {
	mgr := newACMEManager("myhost.tv", "", "", acmeProductionDirectory)
	handler := mgr.HTTPHandler()

	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusMovedPermanently {
		t.Errorf("status = %d, want 301", w.Code)
	}
	if loc := w.Header().Get("Location"); loc != "https://myhost.tv/" {
		t.Errorf("redirect Location = %q", loc)
	}
}

// ---------- Certificate Storage ----------

func TestCertSaveLoad_Roundtrip(t *testing.T) {
	tmpDir := t.TempDir()
	mgr := newACMEManager("test.example.com", "", tmpDir, "")

	// Generate a self-signed cert for testing.
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "test.example.com"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(90 * 24 * time.Hour), // 90 days
		DNSNames:     []string{"test.example.com"},
	}
	certDER, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	keyDER, _ := x509.MarshalECPrivateKey(key)
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})

	// Save.
	if err := mgr.saveCert(certPEM, keyPEM); err != nil {
		t.Fatal(err)
	}

	// Verify files exist.
	certPath := filepath.Join(tmpDir, "test.example.com.pem")
	keyPath := filepath.Join(tmpDir, "test.example.com-key.pem")
	if _, err := os.Stat(certPath); err != nil {
		t.Errorf("cert file not created: %v", err)
	}
	if _, err := os.Stat(keyPath); err != nil {
		t.Errorf("key file not created: %v", err)
	}

	// Load back.
	if err := mgr.loadCachedCert(); err != nil {
		t.Fatalf("loadCachedCert failed: %v", err)
	}

	if mgr.cert == nil {
		t.Fatal("cert should not be nil after load")
	}
	if mgr.certExpiry.IsZero() {
		t.Error("certExpiry should not be zero")
	}
	if time.Until(mgr.certExpiry) < 80*24*time.Hour {
		t.Errorf("cert expiry too soon: %v", mgr.certExpiry)
	}
}

func TestCertLoad_ExpiredCert(t *testing.T) {
	tmpDir := t.TempDir()
	mgr := newACMEManager("expired.example.com", "", tmpDir, "")

	// Generate a cert that expired yesterday.
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "expired.example.com"},
		NotBefore:    time.Now().Add(-48 * time.Hour),
		NotAfter:     time.Now().Add(-24 * time.Hour), // expired
		DNSNames:     []string{"expired.example.com"},
	}
	certDER, _ := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	keyDER, _ := x509.MarshalECPrivateKey(key)
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})

	mgr.saveCert(certPEM, keyPEM)

	// Loading should fail (expired = less than 7 days remaining).
	err := mgr.loadCachedCert()
	if err == nil {
		t.Error("expected error loading expired cert")
	}
}

func TestCertLoad_SoonExpiring(t *testing.T) {
	tmpDir := t.TempDir()
	mgr := newACMEManager("soon.example.com", "", tmpDir, "")

	// Generate a cert expiring in 3 days (< 7 day threshold).
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "soon.example.com"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(3 * 24 * time.Hour),
		DNSNames:     []string{"soon.example.com"},
	}
	certDER, _ := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	keyDER, _ := x509.MarshalECPrivateKey(key)
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})

	mgr.saveCert(certPEM, keyPEM)

	err := mgr.loadCachedCert()
	if err == nil {
		t.Error("expected error loading soon-expiring cert (<7 days)")
	}
	if err != nil && !strings.Contains(err.Error(), "expires too soon") {
		t.Errorf("unexpected error: %v", err)
	}
}

// ---------- Account Key Management ----------

func TestAccountKey_SaveLoad(t *testing.T) {
	tmpDir := t.TempDir()
	mgr := newACMEManager("test.example.com", "", tmpDir, "")

	// First call: generates new key.
	if err := mgr.loadOrCreateAccountKey(); err != nil {
		t.Fatal(err)
	}
	if mgr.accountKey == nil {
		t.Fatal("account key should not be nil")
	}

	// Save the public key for comparison.
	origX := mgr.accountKey.PublicKey.X.Bytes()
	origY := mgr.accountKey.PublicKey.Y.Bytes()

	// Second call (new manager): loads existing key.
	mgr2 := newACMEManager("test.example.com", "", tmpDir, "")
	if err := mgr2.loadOrCreateAccountKey(); err != nil {
		t.Fatal(err)
	}

	if mgr2.accountKey == nil {
		t.Fatal("loaded account key should not be nil")
	}

	// Keys should match.
	loadedX := mgr2.accountKey.PublicKey.X.Bytes()
	loadedY := mgr2.accountKey.PublicKey.Y.Bytes()
	if string(origX) != string(loadedX) || string(origY) != string(loadedY) {
		t.Error("loaded key does not match generated key")
	}
}

func TestAccountURL_SaveLoad(t *testing.T) {
	tmpDir := t.TempDir()
	mgr := newACMEManager("test.example.com", "", tmpDir, "")

	mgr.accountURL = "https://acme.example/acct/12345"
	if err := mgr.saveAccountURL(); err != nil {
		t.Fatal(err)
	}

	// Load in new manager.
	mgr2 := newACMEManager("test.example.com", "", tmpDir, "")
	// loadOrCreateAccountKey also loads the URL.
	// First generate a key file so loadOrCreateAccountKey succeeds.
	mgr.accountKey, _ = ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	keyDER, _ := x509.MarshalECPrivateKey(mgr.accountKey)
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	os.WriteFile(filepath.Join(tmpDir, "account-key.pem"), keyPEM, 0600)

	if err := mgr2.loadOrCreateAccountKey(); err != nil {
		t.Fatal(err)
	}
	if mgr2.accountURL != "https://acme.example/acct/12345" {
		t.Errorf("accountURL = %q", mgr2.accountURL)
	}
}

// ---------- GetCertificate ----------

func TestGetCertificate_NoCert(t *testing.T) {
	mgr := newACMEManager("test.example.com", "", "", "")

	_, err := mgr.GetCertificate(nil)
	if err == nil {
		t.Error("expected error when no certificate loaded")
	}
}

func TestGetCertificate_WithCert(t *testing.T) {
	mgr := newACMEManager("test.example.com", "", "", "")

	// Create a minimal tls.Certificate.
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		NotBefore:    time.Now(),
		NotAfter:     time.Now().Add(24 * time.Hour),
	}
	certDER, _ := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	cert := tls.Certificate{Certificate: [][]byte{certDER}, PrivateKey: key}
	mgr.cert = &cert

	got, err := mgr.GetCertificate(nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != &cert {
		t.Error("returned cert doesn't match stored cert")
	}
}

// ---------- ACME Error Parsing ----------

func TestParseACMEError(t *testing.T) {
	body := `{"type":"urn:ietf:params:acme:error:badNonce","detail":"JWS has no anti-replay nonce","status":400}`

	err := parseACMEError([]byte(body), 400)
	if err == nil {
		t.Fatal("expected error")
	}

	ae, ok := err.(*acmeError)
	if !ok {
		t.Fatalf("expected *acmeError, got %T", err)
	}
	if !strings.Contains(ae.Type, "badNonce") {
		t.Errorf("type = %q", ae.Type)
	}
	if ae.Status != 400 {
		t.Errorf("status = %d", ae.Status)
	}
	if !strings.Contains(ae.Error(), "anti-replay") {
		t.Errorf("error message = %q", ae.Error())
	}
}

func TestParseACMEError_InvalidJSON(t *testing.T) {
	err := parseACMEError([]byte("not json"), 500)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("should include status code: %v", err)
	}
}

// ---------- Nonce Management ----------

func TestNonceConsumeAndSave(t *testing.T) {
	mgr := newACMEManager("test.example.com", "", "", "")
	mgr.nonce = "nonce-1"

	// Consume returns and clears.
	got := mgr.consumeNonce()
	if got != "nonce-1" {
		t.Errorf("consumeNonce = %q", got)
	}
	if mgr.nonce != "" {
		t.Error("nonce should be cleared after consume")
	}

	// Second consume returns empty.
	got2 := mgr.consumeNonce()
	if got2 != "" {
		t.Errorf("second consumeNonce = %q, want empty", got2)
	}

	// saveNonce from response header.
	resp := &http.Response{Header: http.Header{}}
	resp.Header.Set("Replay-Nonce", "nonce-2")
	mgr.saveNonce(resp)
	if mgr.nonce != "nonce-2" {
		t.Errorf("nonce after saveNonce = %q", mgr.nonce)
	}
}

// ---------- Helpers ----------

// generateSelfSignedCert creates a temporary self-signed certificate
// and returns the paths to the cert and key PEM files.
func generateSelfSignedCert(t *testing.T) (certPath, keyPath string) {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "localhost"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		DNSNames:     []string{"localhost"},
	}

	certDER, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}

	tmpDir := t.TempDir()
	certPath = filepath.Join(tmpDir, "cert.pem")
	keyPath = filepath.Join(tmpDir, "key.pem")

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	os.WriteFile(certPath, certPEM, 0644)

	keyDER, _ := x509.MarshalECPrivateKey(key)
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	os.WriteFile(keyPath, keyPEM, 0600)

	return certPath, keyPath
}

// generateSelfSignedTLSCert creates an in-memory self-signed *tls.Certificate
// for testing certStore operations without filesystem.
func generateSelfSignedTLSCert(t *testing.T, hostname string) *tls.Certificate {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: hostname},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		DNSNames:     []string{hostname},
	}

	certDER, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}

	leaf, err := x509.ParseCertificate(certDER)
	if err != nil {
		t.Fatal(err)
	}

	return &tls.Certificate{
		Certificate: [][]byte{certDER},
		PrivateKey:  key,
		Leaf:        leaf,
	}
}

// ---------- certStore ----------

func TestCertStore_NewDefaults(t *testing.T) {
	cs := newCertStore("", "", "", false)
	if cs.dataDir != "./certs" {
		t.Errorf("dataDir = %q, want %q", cs.dataDir, "./certs")
	}
	if cs.directoryURL != acmeProductionDirectory {
		t.Errorf("directoryURL = %q, want production", cs.directoryURL)
	}
}

func TestCertStore_NewStaging(t *testing.T) {
	cs := newCertStore("", "", "", true)
	if cs.directoryURL != acmeStagingDirectory {
		t.Errorf("directoryURL = %q, want staging", cs.directoryURL)
	}
}

func TestCertStore_GetCertificate_NoSNI(t *testing.T) {
	cs := newCertStore("", "", "", false)

	_, err := cs.GetCertificate(&tls.ClientHelloInfo{ServerName: ""})
	if err == nil {
		t.Error("expected error for empty ServerName")
	}
	if !strings.Contains(err.Error(), "no SNI") {
		t.Errorf("error = %q, want mention of SNI", err.Error())
	}
}

func TestCertStore_GetCertificate_Unknown(t *testing.T) {
	cs := newCertStore("", "", "", false)

	_, err := cs.GetCertificate(&tls.ClientHelloInfo{ServerName: "unknown.tv"})
	if err == nil {
		t.Error("expected error for unknown hostname")
	}
	if !strings.Contains(err.Error(), "unknown.tv") {
		t.Errorf("error = %q, want hostname in message", err.Error())
	}
}

func TestCertStore_AddManualCert(t *testing.T) {
	cs := newCertStore("", "", "", false)

	cert := generateSelfSignedTLSCert(t, "demo.tv")
	cs.AddManualCert("demo.tv", cert)

	got, err := cs.GetCertificate(&tls.ClientHelloInfo{ServerName: "demo.tv"})
	if err != nil {
		t.Fatalf("GetCertificate failed: %v", err)
	}
	if got != cert {
		t.Error("returned certificate does not match the one added")
	}
}

func TestCertStore_AddManualCert_Multiple(t *testing.T) {
	cs := newCertStore("", "", "", false)

	hostnames := []string{"alpha.tv", "beta.tv", "gamma.tv"}
	certs := make(map[string]*tls.Certificate)
	for _, h := range hostnames {
		cert := generateSelfSignedTLSCert(t, h)
		certs[h] = cert
		cs.AddManualCert(h, cert)
	}

	for _, h := range hostnames {
		got, err := cs.GetCertificate(&tls.ClientHelloInfo{ServerName: h})
		if err != nil {
			t.Fatalf("GetCertificate(%q): %v", h, err)
		}
		if got != certs[h] {
			t.Errorf("GetCertificate(%q) returned wrong cert", h)
		}
	}
}

func TestCertStore_Hostnames(t *testing.T) {
	cs := newCertStore("", "", "", false)

	hostnames := []string{"alpha.tv", "beta.tv", "gamma.tv"}
	for _, h := range hostnames {
		cert := generateSelfSignedTLSCert(t, h)
		cs.AddManualCert(h, cert)
	}

	got := cs.Hostnames()
	if len(got) != 3 {
		t.Fatalf("Hostnames() returned %d entries, want 3", len(got))
	}

	// Verify all expected hostnames are present (order is unspecified).
	seen := make(map[string]bool)
	for _, h := range got {
		seen[h] = true
	}
	for _, h := range hostnames {
		if !seen[h] {
			t.Errorf("Hostnames() missing %q", h)
		}
	}
}

func TestCertStore_TLSConfig(t *testing.T) {
	cs := newCertStore("", "", "", false)

	cfg := cs.TLSConfig()
	if cfg == nil {
		t.Fatal("TLSConfig() returned nil")
	}
	if cfg.GetCertificate == nil {
		t.Error("TLSConfig().GetCertificate is nil")
	}
	if cfg.MinVersion != tls.VersionTLS12 {
		t.Errorf("MinVersion = %d, want TLS 1.2 (%d)", cfg.MinVersion, tls.VersionTLS12)
	}
}

func TestCertStore_HTTPHandler_Challenge(t *testing.T) {
	cs := newCertStore("", "", "", false)

	// Create a manager for a hostname and provision a challenge.
	mgr := cs.getOrCreateManager("demo.tv")
	mgr.challengeMu.Lock()
	mgr.challenges["store-test-token"] = "store-test-token.thumbprint456"
	mgr.challengeMu.Unlock()

	handler := cs.HTTPHandler()

	req := httptest.NewRequest("GET", "/.well-known/acme-challenge/store-test-token", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Errorf("status = %d, want 200", w.Code)
	}
	if w.Body.String() != "store-test-token.thumbprint456" {
		t.Errorf("body = %q", w.Body.String())
	}
}

func TestCertStore_HTTPHandler_Redirect(t *testing.T) {
	cs := newCertStore("", "", "", false)
	handler := cs.HTTPHandler()

	req := httptest.NewRequest("GET", "/some/page?q=1", nil)
	req.Host = "demo.tv"
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusMovedPermanently {
		t.Errorf("status = %d, want 301", w.Code)
	}
	loc := w.Header().Get("Location")
	if loc != "https://demo.tv/some/page?q=1" {
		t.Errorf("redirect Location = %q, want %q", loc, "https://demo.tv/some/page?q=1")
	}
}

func TestCertStore_HTTPHandler_RedirectPreservesHost(t *testing.T) {
	cs := newCertStore("", "", "", false)
	handler := cs.HTTPHandler()

	// First request with Host: alpha.tv
	req1 := httptest.NewRequest("GET", "/path", nil)
	req1.Host = "alpha.tv"
	w1 := httptest.NewRecorder()
	handler.ServeHTTP(w1, req1)

	if w1.Code != http.StatusMovedPermanently {
		t.Fatalf("status = %d, want 301", w1.Code)
	}
	loc1 := w1.Header().Get("Location")
	if loc1 != "https://alpha.tv/path" {
		t.Errorf("redirect for alpha.tv = %q", loc1)
	}

	// Second request with Host: beta.tv
	req2 := httptest.NewRequest("GET", "/path", nil)
	req2.Host = "beta.tv"
	w2 := httptest.NewRecorder()
	handler.ServeHTTP(w2, req2)

	if w2.Code != http.StatusMovedPermanently {
		t.Fatalf("status = %d, want 301", w2.Code)
	}
	loc2 := w2.Header().Get("Location")
	if loc2 != "https://beta.tv/path" {
		t.Errorf("redirect for beta.tv = %q", loc2)
	}

	// Verify they're different (the key multi-domain behavior).
	if loc1 == loc2 {
		t.Error("both redirects produced the same URL — Host header not used for redirect")
	}
}
