package main

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// ACME directory URLs (Let's Encrypt).
const (
	acmeProductionDirectory = "https://acme-v02.api.letsencrypt.org/directory"
	acmeStagingDirectory    = "https://acme-staging-v02.api.letsencrypt.org/directory"
)

// acmeManager handles automatic TLS certificate provisioning via ACME HTTP-01.
// Zero external dependencies — uses only Go stdlib crypto.
type acmeManager struct {
	mu       sync.RWMutex
	hostname string
	email    string
	dataDir  string // certificate storage directory (default: ./certs/)

	directoryURL string // ACME directory URL

	// Current certificate (hot-swapped on renewal).
	cert       *tls.Certificate
	certExpiry time.Time

	// ACME account.
	accountKey *ecdsa.PrivateKey
	accountURL string

	// Replay nonce.
	nonceMu sync.Mutex
	nonce   string

	// Pending HTTP-01 challenges: token → keyAuthorization.
	challengeMu sync.Mutex
	challenges  map[string]string

	// ACME directory endpoints (populated by fetchDirectory).
	newNonceURL   string
	newAccountURL string
	newOrderURL   string

	// Internal HTTP server on :80 for challenges + redirect.
	httpServer *http.Server
}

// ---------- ACME Protocol Types ----------

type acmeDirectory struct {
	NewNonce   string `json:"newNonce"`
	NewAccount string `json:"newAccount"`
	NewOrder   string `json:"newOrder"`
}

type acmeOrder struct {
	Status         string   `json:"status"`
	Authorizations []string `json:"authorizations"`
	Finalize       string   `json:"finalize"`
	Certificate    string   `json:"certificate"`
}

type acmeAuthz struct {
	Status     string          `json:"status"`
	Challenges []acmeChallenge `json:"challenges"`
}

type acmeChallenge struct {
	Type   string `json:"type"`
	URL    string `json:"url"`
	Token  string `json:"token"`
	Status string `json:"status"`
}

type acmeError struct {
	Type   string `json:"type"`
	Detail string `json:"detail"`
	Status int    `json:"status"`
}

func (e *acmeError) Error() string {
	if e.Detail != "" {
		return fmt.Sprintf("acme: %s (%s)", e.Detail, e.Type)
	}
	return fmt.Sprintf("acme: %s", e.Type)
}

// ---------- Constructor ----------

func newACMEManager(hostname, email, dataDir, directoryURL string) *acmeManager {
	if dataDir == "" {
		dataDir = "./certs"
	}
	return &acmeManager{
		hostname:     hostname,
		email:        email,
		dataDir:      dataDir,
		directoryURL: directoryURL,
		challenges:   make(map[string]string),
	}
}

// ---------- TLS Integration ----------

// GetCertificate implements crypto/tls.Config.GetCertificate.
// Returns the current certificate, hot-swapped by the renewal goroutine.
func (m *acmeManager) GetCertificate(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.cert == nil {
		return nil, fmt.Errorf("acme: no certificate available")
	}
	return m.cert, nil
}

// ---------- Certificate Management ----------

// EnsureCert loads a cached certificate or issues a new one.
// Blocks until a valid certificate is available.
func (m *acmeManager) EnsureCert(ctx context.Context) error {
	// Try loading cached cert first.
	if err := m.loadCachedCert(); err == nil {
		logInfof("tls: loaded cached certificate (expires %s)", m.certExpiry.Format(time.RFC3339))
		return nil
	}

	// No valid cached cert — issue a new one.
	logInfof("tls: requesting certificate for %s", m.hostname)
	return m.Issue(ctx)
}

// Issue obtains a new certificate via ACME HTTP-01.
func (m *acmeManager) Issue(ctx context.Context) error {
	// Ensure data directory exists.
	if err := os.MkdirAll(m.dataDir, 0700); err != nil {
		return fmt.Errorf("create cert dir: %w", err)
	}

	// Load or create account key.
	if err := m.loadOrCreateAccountKey(); err != nil {
		return fmt.Errorf("account key: %w", err)
	}

	// Fetch ACME directory.
	if err := m.fetchDirectory(ctx); err != nil {
		return fmt.Errorf("directory: %w", err)
	}

	// Get initial nonce.
	if err := m.fetchNonce(ctx); err != nil {
		return fmt.Errorf("nonce: %w", err)
	}

	// Create or retrieve account.
	if err := m.createAccount(ctx); err != nil {
		return fmt.Errorf("account: %w", err)
	}

	// Create order.
	order, orderURL, err := m.createOrder(ctx, []string{m.hostname})
	if err != nil {
		return fmt.Errorf("order: %w", err)
	}

	// Complete all authorizations.
	for _, authzURL := range order.Authorizations {
		if err := m.completeAuthorization(ctx, authzURL); err != nil {
			return fmt.Errorf("authorization: %w", err)
		}
	}

	// Wait for order to be ready.
	order, err = m.pollOrder(ctx, orderURL)
	if err != nil {
		return fmt.Errorf("poll order: %w", err)
	}

	// Generate certificate key pair.
	certKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return fmt.Errorf("generate cert key: %w", err)
	}

	// Create CSR.
	csrDER, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		DNSNames: []string{m.hostname},
	}, certKey)
	if err != nil {
		return fmt.Errorf("create CSR: %w", err)
	}

	// Finalize order with CSR.
	order, err = m.finalizeOrder(ctx, order, orderURL, csrDER)
	if err != nil {
		return fmt.Errorf("finalize: %w", err)
	}

	// Download certificate chain.
	certPEM, err := m.downloadCert(ctx, order.Certificate)
	if err != nil {
		return fmt.Errorf("download cert: %w", err)
	}

	// Encode private key to PEM.
	keyDER, err := x509.MarshalECPrivateKey(certKey)
	if err != nil {
		return fmt.Errorf("marshal key: %w", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})

	// Save to disk.
	if err := m.saveCert(certPEM, keyPEM); err != nil {
		return fmt.Errorf("save cert: %w", err)
	}

	// Load into TLS config.
	tlsCert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return fmt.Errorf("parse cert: %w", err)
	}

	// Parse leaf for expiry.
	if tlsCert.Leaf == nil {
		leaf, err := x509.ParseCertificate(tlsCert.Certificate[0])
		if err == nil {
			tlsCert.Leaf = leaf
		}
	}

	m.mu.Lock()
	m.cert = &tlsCert
	if tlsCert.Leaf != nil {
		m.certExpiry = tlsCert.Leaf.NotAfter
	}
	m.mu.Unlock()

	logInfof("tls: certificate issued (expires %s)", m.certExpiry.Format(time.RFC3339))
	return nil
}

// RenewLoop checks hourly and renews when less than 30 days remain.
// Blocks until ctx is cancelled.
func (m *acmeManager) RenewLoop(ctx context.Context) {
	ticker := time.NewTicker(1 * time.Hour)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.mu.RLock()
			expiry := m.certExpiry
			m.mu.RUnlock()

			if expiry.IsZero() {
				continue
			}

			remaining := time.Until(expiry)
			if remaining > 30*24*time.Hour {
				continue
			}

			logInfof("tls: certificate expires in %d days, renewing...", int(remaining.Hours()/24))

			renewCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
			if err := m.Issue(renewCtx); err != nil {
				logErrorf("tls: renewal failed: %v (will retry in 1h)", err)
			}
			cancel()
		}
	}
}

// HTTPHandler returns an http.Handler for port 80 that serves ACME
// HTTP-01 challenges and redirects all other traffic to HTTPS.
func (m *acmeManager) HTTPHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// ACME HTTP-01 challenge.
		if strings.HasPrefix(r.URL.Path, "/.well-known/acme-challenge/") {
			token := strings.TrimPrefix(r.URL.Path, "/.well-known/acme-challenge/")
			m.challengeMu.Lock()
			keyAuth, ok := m.challenges[token]
			m.challengeMu.Unlock()
			if ok {
				w.Header().Set("Content-Type", "application/octet-stream")
				w.Write([]byte(keyAuth))
				return
			}
			http.NotFound(w, r)
			return
		}

		// Redirect HTTP → HTTPS.
		target := "https://" + m.hostname + r.URL.RequestURI()
		http.Redirect(w, r, target, http.StatusMovedPermanently)
	})
}

// ---------- ACME Protocol Implementation ----------

// fetchDirectory retrieves the ACME directory endpoints.
func (m *acmeManager) fetchDirectory(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, "GET", m.directoryURL, nil)
	if err != nil {
		return err
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("directory: HTTP %d", resp.StatusCode)
	}

	var dir acmeDirectory
	if err := json.NewDecoder(resp.Body).Decode(&dir); err != nil {
		return err
	}

	m.newNonceURL = dir.NewNonce
	m.newAccountURL = dir.NewAccount
	m.newOrderURL = dir.NewOrder

	if m.newNonceURL == "" || m.newAccountURL == "" || m.newOrderURL == "" {
		return fmt.Errorf("directory: missing required endpoints")
	}
	return nil
}

// fetchNonce gets a fresh replay nonce from the ACME server.
func (m *acmeManager) fetchNonce(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, "HEAD", m.newNonceURL, nil)
	if err != nil {
		return err
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()

	nonce := resp.Header.Get("Replay-Nonce")
	if nonce == "" {
		return fmt.Errorf("no Replay-Nonce in response")
	}

	m.nonceMu.Lock()
	m.nonce = nonce
	m.nonceMu.Unlock()
	return nil
}

// consumeNonce returns the current nonce and clears it.
func (m *acmeManager) consumeNonce() string {
	m.nonceMu.Lock()
	defer m.nonceMu.Unlock()
	n := m.nonce
	m.nonce = ""
	return n
}

// saveNonce stores a nonce from a response header.
func (m *acmeManager) saveNonce(resp *http.Response) {
	if n := resp.Header.Get("Replay-Nonce"); n != "" {
		m.nonceMu.Lock()
		m.nonce = n
		m.nonceMu.Unlock()
	}
}

// createAccount creates or retrieves an ACME account.
func (m *acmeManager) createAccount(ctx context.Context) error {
	// Check for saved account URL.
	if m.accountURL != "" {
		return nil
	}

	payload := map[string]interface{}{
		"termsOfServiceAgreed": true,
	}
	if m.email != "" {
		payload["contact"] = []string{"mailto:" + m.email}
	}

	resp, body, err := m.signedPost(ctx, m.newAccountURL, payload, true)
	if err != nil {
		return err
	}

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return parseACMEError(body, resp.StatusCode)
	}

	m.accountURL = resp.Header.Get("Location")
	if m.accountURL == "" {
		return fmt.Errorf("no account URL in response")
	}

	// Save account URL.
	if err := m.saveAccountURL(); err != nil {
		logErrorf("tls: failed to save account URL: %v", err)
	}

	return nil
}

// createOrder submits a new certificate order.
func (m *acmeManager) createOrder(ctx context.Context, domains []string) (*acmeOrder, string, error) {
	identifiers := make([]map[string]string, len(domains))
	for i, d := range domains {
		identifiers[i] = map[string]string{"type": "dns", "value": d}
	}

	payload := map[string]interface{}{
		"identifiers": identifiers,
	}

	resp, body, err := m.signedPost(ctx, m.newOrderURL, payload, false)
	if err != nil {
		return nil, "", err
	}

	if resp.StatusCode != http.StatusCreated {
		return nil, "", parseACMEError(body, resp.StatusCode)
	}

	var order acmeOrder
	if err := json.Unmarshal(body, &order); err != nil {
		return nil, "", fmt.Errorf("parse order: %w", err)
	}

	orderURL := resp.Header.Get("Location")
	return &order, orderURL, nil
}

// completeAuthorization fetches an authorization, finds the HTTP-01 challenge,
// provisions the response, and tells the ACME server to validate.
func (m *acmeManager) completeAuthorization(ctx context.Context, authzURL string) error {
	// Fetch authorization (POST-as-GET).
	resp, body, err := m.signedPost(ctx, authzURL, nil, false)
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return parseACMEError(body, resp.StatusCode)
	}

	var authz acmeAuthz
	if err := json.Unmarshal(body, &authz); err != nil {
		return fmt.Errorf("parse authz: %w", err)
	}

	// Already valid (e.g., reused authorization).
	if authz.Status == "valid" {
		return nil
	}

	// Find HTTP-01 challenge.
	var challenge *acmeChallenge
	for i := range authz.Challenges {
		if authz.Challenges[i].Type == "http-01" {
			challenge = &authz.Challenges[i]
			break
		}
	}
	if challenge == nil {
		return fmt.Errorf("no http-01 challenge offered")
	}

	// Compute key authorization: token + "." + JWK thumbprint.
	thumbprint, err := m.jwkThumbprint()
	if err != nil {
		return err
	}
	keyAuth := challenge.Token + "." + thumbprint

	// Provision the challenge response.
	m.challengeMu.Lock()
	m.challenges[challenge.Token] = keyAuth
	m.challengeMu.Unlock()

	defer func() {
		m.challengeMu.Lock()
		delete(m.challenges, challenge.Token)
		m.challengeMu.Unlock()
	}()

	// Tell the server to validate.
	resp, body, err = m.signedPost(ctx, challenge.URL, map[string]interface{}{}, false)
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return parseACMEError(body, resp.StatusCode)
	}

	// Poll authorization until valid or invalid.
	return m.pollAuthorization(ctx, authzURL)
}

// pollAuthorization polls until the authorization is valid or fails.
func (m *acmeManager) pollAuthorization(ctx context.Context, authzURL string) error {
	for i := 0; i < 30; i++ {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}

		resp, body, err := m.signedPost(ctx, authzURL, nil, false)
		if err != nil {
			return err
		}
		if resp.StatusCode != http.StatusOK {
			return parseACMEError(body, resp.StatusCode)
		}

		var authz acmeAuthz
		if err := json.Unmarshal(body, &authz); err != nil {
			return fmt.Errorf("parse authz: %w", err)
		}

		switch authz.Status {
		case "valid":
			return nil
		case "invalid":
			// Try to extract challenge error.
			for _, ch := range authz.Challenges {
				if ch.Status == "invalid" {
					return fmt.Errorf("challenge failed for %s", ch.Type)
				}
			}
			return fmt.Errorf("authorization invalid")
		case "pending", "processing":
			continue
		default:
			return fmt.Errorf("unexpected authz status: %s", authz.Status)
		}
	}
	return fmt.Errorf("authorization timed out")
}

// pollOrder polls the order URL until it becomes ready or valid.
func (m *acmeManager) pollOrder(ctx context.Context, orderURL string) (*acmeOrder, error) {
	for i := 0; i < 30; i++ {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(2 * time.Second):
		}

		resp, body, err := m.signedPost(ctx, orderURL, nil, false)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode != http.StatusOK {
			return nil, parseACMEError(body, resp.StatusCode)
		}

		var order acmeOrder
		if err := json.Unmarshal(body, &order); err != nil {
			return nil, fmt.Errorf("parse order: %w", err)
		}

		switch order.Status {
		case "ready", "valid":
			return &order, nil
		case "pending", "processing":
			continue
		case "invalid":
			return nil, fmt.Errorf("order invalid")
		default:
			return nil, fmt.Errorf("unexpected order status: %s", order.Status)
		}
	}
	return nil, fmt.Errorf("order timed out")
}

// finalizeOrder submits the CSR and waits for the certificate URL.
func (m *acmeManager) finalizeOrder(ctx context.Context, order *acmeOrder, orderURL string, csrDER []byte) (*acmeOrder, error) {
	payload := map[string]interface{}{
		"csr": base64.RawURLEncoding.EncodeToString(csrDER),
	}

	resp, body, err := m.signedPost(ctx, order.Finalize, payload, false)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode != http.StatusOK {
		return nil, parseACMEError(body, resp.StatusCode)
	}

	var finalized acmeOrder
	if err := json.Unmarshal(body, &finalized); err != nil {
		return nil, fmt.Errorf("parse finalized order: %w", err)
	}

	// If already valid with certificate URL, return immediately.
	if finalized.Status == "valid" && finalized.Certificate != "" {
		return &finalized, nil
	}

	// Poll for completion.
	for i := 0; i < 30; i++ {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(2 * time.Second):
		}

		resp, body, err := m.signedPost(ctx, orderURL, nil, false)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode != http.StatusOK {
			return nil, parseACMEError(body, resp.StatusCode)
		}

		if err := json.Unmarshal(body, &finalized); err != nil {
			return nil, fmt.Errorf("parse order: %w", err)
		}

		if finalized.Status == "valid" && finalized.Certificate != "" {
			return &finalized, nil
		}
		if finalized.Status == "invalid" {
			return nil, fmt.Errorf("order became invalid after finalization")
		}
	}
	return nil, fmt.Errorf("finalize timed out")
}

// downloadCert fetches the certificate chain PEM from the given URL.
func (m *acmeManager) downloadCert(ctx context.Context, certURL string) ([]byte, error) {
	resp, body, err := m.signedPost(ctx, certURL, nil, false)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, parseACMEError(body, resp.StatusCode)
	}
	return body, nil
}

// ---------- JWS / ES256 Signing ----------

// signedPost sends a JWS-signed POST request to the ACME server.
// If useJWK is true, includes the full JWK in the header (for newAccount).
// If payload is nil, sends a POST-as-GET (empty payload string).
// Returns the response, body bytes, and any transport error.
// Automatically retries once on badNonce errors.
func (m *acmeManager) signedPost(ctx context.Context, url string, payload interface{}, useJWK bool) (*http.Response, []byte, error) {
	resp, body, err := m.doSignedPost(ctx, url, payload, useJWK)
	if err != nil {
		return nil, nil, err
	}

	// Retry once on badNonce.
	if resp.StatusCode == http.StatusBadRequest {
		var ae acmeError
		if json.Unmarshal(body, &ae) == nil && strings.HasSuffix(ae.Type, ":badNonce") {
			if err := m.fetchNonce(ctx); err != nil {
				return nil, nil, fmt.Errorf("nonce retry: %w", err)
			}
			return m.doSignedPost(ctx, url, payload, useJWK)
		}
	}

	return resp, body, nil
}

func (m *acmeManager) doSignedPost(ctx context.Context, url string, payload interface{}, useJWK bool) (*http.Response, []byte, error) {
	// Build protected header.
	protected := map[string]interface{}{
		"alg":   "ES256",
		"nonce": m.consumeNonce(),
		"url":   url,
	}
	if useJWK {
		protected["jwk"] = m.jwk()
	} else {
		protected["kid"] = m.accountURL
	}

	protectedJSON, err := json.Marshal(protected)
	if err != nil {
		return nil, nil, err
	}
	protectedB64 := base64.RawURLEncoding.EncodeToString(protectedJSON)

	// Encode payload.
	var payloadB64 string
	if payload != nil {
		payloadJSON, err := json.Marshal(payload)
		if err != nil {
			return nil, nil, err
		}
		payloadB64 = base64.RawURLEncoding.EncodeToString(payloadJSON)
	} else {
		// POST-as-GET: empty string (not "null").
		payloadB64 = ""
	}

	// Sign: SHA-256(protected.payload).
	sigInput := protectedB64 + "." + payloadB64
	hash := sha256.Sum256([]byte(sigInput))

	r, s, err := ecdsa.Sign(rand.Reader, m.accountKey, hash[:])
	if err != nil {
		return nil, nil, fmt.Errorf("ecdsa sign: %w", err)
	}

	// Encode signature as r||s, each 32 bytes (P-256).
	sig := make([]byte, 64)
	rBytes := r.Bytes()
	sBytes := s.Bytes()
	copy(sig[32-len(rBytes):32], rBytes)
	copy(sig[64-len(sBytes):64], sBytes)
	sigB64 := base64.RawURLEncoding.EncodeToString(sig)

	// Build flattened JWS.
	jws := map[string]string{
		"protected": protectedB64,
		"payload":   payloadB64,
		"signature": sigB64,
	}
	jwsJSON, err := json.Marshal(jws)
	if err != nil {
		return nil, nil, err
	}

	// Send request.
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(jwsJSON))
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("Content-Type", "application/jose+json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()

	// Save nonce from response.
	m.saveNonce(resp)

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20)) // 1 MB limit
	if err != nil {
		return nil, nil, err
	}

	return resp, body, nil
}

// jwk returns the account public key as a JWK map (for newAccount header).
// Coordinates are zero-padded to 32 bytes per RFC 7518 §6.2.1.
func (m *acmeManager) jwk() map[string]string {
	pub := m.accountKey.PublicKey
	return map[string]string{
		"kty": "EC",
		"crv": "P-256",
		"x":   base64.RawURLEncoding.EncodeToString(padCoordinate(pub.X.Bytes(), 32)),
		"y":   base64.RawURLEncoding.EncodeToString(padCoordinate(pub.Y.Bytes(), 32)),
	}
}

// jwkThumbprint computes the JWK thumbprint (RFC 7638) of the account key.
// Returns the base64url-encoded SHA-256 hash of the canonical JWK.
func (m *acmeManager) jwkThumbprint() (string, error) {
	pub := m.accountKey.PublicKey

	// Canonical JWK must have keys in lexicographic order: crv, kty, x, y.
	// P-256 coordinates are 32 bytes each, left-padded.
	xBytes := pub.X.Bytes()
	yBytes := pub.Y.Bytes()

	// Pad to 32 bytes.
	xPadded := make([]byte, 32)
	yPadded := make([]byte, 32)
	copy(xPadded[32-len(xBytes):], xBytes)
	copy(yPadded[32-len(yBytes):], yBytes)

	// Build canonical JSON manually for correctness
	// (keys must be in lexicographic order per RFC 7638).
	canonical := fmt.Sprintf(`{"crv":"P-256","kty":"EC","x":"%s","y":"%s"}`,
		base64.RawURLEncoding.EncodeToString(xPadded),
		base64.RawURLEncoding.EncodeToString(yPadded))

	hash := sha256.Sum256([]byte(canonical))
	return base64.RawURLEncoding.EncodeToString(hash[:]), nil
}

// ---------- Account Key Management ----------

// loadOrCreateAccountKey loads the account key from disk, or generates a new one.
func (m *acmeManager) loadOrCreateAccountKey() error {
	keyPath := filepath.Join(m.dataDir, "account-key.pem")
	urlPath := filepath.Join(m.dataDir, "account.json")

	// Try loading existing key.
	keyPEM, err := os.ReadFile(keyPath)
	if err == nil {
		block, _ := pem.Decode(keyPEM)
		if block == nil {
			return fmt.Errorf("invalid PEM in %s", keyPath)
		}
		key, err := x509.ParseECPrivateKey(block.Bytes)
		if err != nil {
			return fmt.Errorf("parse account key: %w", err)
		}
		m.accountKey = key

		// Try loading account URL.
		urlData, err := os.ReadFile(urlPath)
		if err == nil {
			var acct struct {
				URL string `json:"url"`
			}
			if json.Unmarshal(urlData, &acct) == nil {
				m.accountURL = acct.URL
			}
		}

		return nil
	}

	// Generate new key.
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	m.accountKey = key

	// Save key.
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return err
	}
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	if err := os.WriteFile(keyPath, keyPEM, 0600); err != nil {
		return fmt.Errorf("write account key: %w", err)
	}

	logInfof("tls: generated new ACME account key")
	return nil
}

// saveAccountURL persists the account URL to disk.
func (m *acmeManager) saveAccountURL() error {
	urlPath := filepath.Join(m.dataDir, "account.json")
	data, err := json.Marshal(map[string]string{"url": m.accountURL})
	if err != nil {
		return err
	}
	return os.WriteFile(urlPath, data, 0600)
}

// ---------- Certificate Storage ----------

// saveCert writes the certificate chain and private key to disk.
func (m *acmeManager) saveCert(certPEM, keyPEM []byte) error {
	certPath := filepath.Join(m.dataDir, m.hostname+".pem")
	keyPath := filepath.Join(m.dataDir, m.hostname+"-key.pem")

	if err := os.WriteFile(certPath, certPEM, 0644); err != nil {
		return fmt.Errorf("write cert: %w", err)
	}
	if err := os.WriteFile(keyPath, keyPEM, 0600); err != nil {
		return fmt.Errorf("write key: %w", err)
	}
	return nil
}

// loadCachedCert attempts to load a previously saved certificate.
// Returns an error if no valid certificate is available.
func (m *acmeManager) loadCachedCert() error {
	certPath := filepath.Join(m.dataDir, m.hostname+".pem")
	keyPath := filepath.Join(m.dataDir, m.hostname+"-key.pem")

	certPEM, err := os.ReadFile(certPath)
	if err != nil {
		return err
	}
	keyPEM, err := os.ReadFile(keyPath)
	if err != nil {
		return err
	}

	tlsCert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return err
	}

	// Parse leaf to check expiry.
	leaf, err := x509.ParseCertificate(tlsCert.Certificate[0])
	if err != nil {
		return err
	}
	tlsCert.Leaf = leaf

	// Reject if expired or less than 7 days remaining.
	if time.Until(leaf.NotAfter) < 7*24*time.Hour {
		return fmt.Errorf("certificate expires too soon (%s)", leaf.NotAfter.Format(time.RFC3339))
	}

	m.mu.Lock()
	m.cert = &tlsCert
	m.certExpiry = leaf.NotAfter
	m.mu.Unlock()

	return nil
}

// ---------- HTTP Server for Port 80 ----------

// StartHTTPServer starts the HTTP server on :80 for ACME challenges
// and HTTP→HTTPS redirect. Returns after the listener is ready.
func (m *acmeManager) StartHTTPServer() error {
	m.httpServer = &http.Server{
		Handler:           m.HTTPHandler(),
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	ln, err := net.Listen("tcp", ":80")
	if err != nil {
		return fmt.Errorf("listen :80: %w", err)
	}

	go func() {
		if err := m.httpServer.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logErrorf("tls: http:80: %v", err)
		}
	}()

	return nil
}

// StopHTTPServer gracefully shuts down the port 80 server.
func (m *acmeManager) StopHTTPServer() {
	if m.httpServer != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		m.httpServer.Shutdown(ctx)
		cancel()
	}
}

// ---------- Helpers ----------

// parseACMEError extracts an ACME error from a response body.
func parseACMEError(body []byte, statusCode int) error {
	var ae acmeError
	if json.Unmarshal(body, &ae) == nil && ae.Type != "" {
		return &ae
	}
	return fmt.Errorf("HTTP %d: %s", statusCode, string(body))
}

// padCoordinate left-pads a big-endian byte slice to the given length.
// Used for JWK coordinate encoding (P-256 → 32 bytes).
func padCoordinate(b []byte, size int) []byte {
	if len(b) >= size {
		return b
	}
	padded := make([]byte, size)
	copy(padded[size-len(b):], b)
	return padded
}
