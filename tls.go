package main

import (
	"context"
	"crypto/tls"
	"flag"
	"fmt"
	"net/http"
	"os"
	"time"
)

// addTLSFlags registers --tls, --tls-cert, --tls-key, --tls-staging, --acme-email
// on a FlagSet. Returns pointers to the parsed values.
// Env vars: TLS=1, TLS_CERT, TLS_KEY, TLS_STAGING=1, ACME_EMAIL.
func addTLSFlags(fs *flag.FlagSet) (enabled *bool, certFile, keyFile, email *string, staging *bool) {
	enabled = fs.Bool("tls", os.Getenv("TLS") == "1", "enable TLS (autocert if no cert/key given)")
	certFile = fs.String("tls-cert", os.Getenv("TLS_CERT"), "TLS certificate file (PEM)")
	keyFile = fs.String("tls-key", os.Getenv("TLS_KEY"), "TLS private key file (PEM)")
	email = fs.String("acme-email", os.Getenv("ACME_EMAIL"), "email for ACME account (optional)")
	staging = fs.Bool("tls-staging", os.Getenv("TLS_STAGING") == "1", "use Let's Encrypt staging (for testing)")
	return
}

// tlsOverrideListenPort changes the default listen address from :8000 to :443
// when TLS is enabled and --listen was not explicitly set by the user.
func tlsOverrideListenPort(fs *flag.FlagSet, listenAddr *string) {
	explicit := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "listen" || f.Name == "l" {
			explicit = true
		}
	})
	if !explicit && os.Getenv("LISTEN") == "" {
		*listenAddr = ":443"
	}
}

// tlsSetup configures TLS for a daemon. Returns:
//   - tlsConfig: non-nil if TLS is enabled (set on http.Server.TLSConfig)
//   - cleanup: call on shutdown to stop renewal goroutines and :80 server
//   - err: if configuration is invalid
//
// Modes:
//
//	--tls-cert + --tls-key: manual certificate files
//	--tls (no cert/key):    ACME Let's Encrypt (requires --hostname)
//	neither:                returns nil, nil (plain HTTP)
//
// For ACME mode, this function:
//  1. Starts an HTTP server on :80 for challenges + HTTP→HTTPS redirect
//  2. Loads a cached certificate or issues a new one (blocks)
//  3. Starts a background renewal goroutine
func tlsSetup(hostname string, enabled bool, certFile, keyFile, email string, staging bool) (*tls.Config, func(), error) {
	if !enabled && certFile == "" && keyFile == "" {
		return nil, func() {}, nil
	}

	// Manual certificate mode.
	if certFile != "" || keyFile != "" {
		if certFile == "" || keyFile == "" {
			return nil, nil, fmt.Errorf("--tls-cert and --tls-key must both be specified")
		}
		cert, err := tls.LoadX509KeyPair(certFile, keyFile)
		if err != nil {
			return nil, nil, fmt.Errorf("load certificate: %w", err)
		}
		logInfof("tls: using manual certificate from %s", certFile)
		cfg := &tls.Config{
			Certificates: []tls.Certificate{cert},
			MinVersion:   tls.VersionTLS12,
		}
		return cfg, func() {}, nil
	}

	// ACME autocert mode.
	if hostname == "" {
		return nil, nil, fmt.Errorf("--hostname is required for ACME (or use --tls-cert/--tls-key)")
	}

	directoryURL := acmeProductionDirectory
	if staging {
		directoryURL = acmeStagingDirectory
		logInfof("tls: using Let's Encrypt staging")
	}

	dataDir := os.Getenv("TLS_DIR")
	if dataDir == "" {
		dataDir = "./certs"
	}

	mgr := newACMEManager(hostname, email, dataDir, directoryURL)

	// Start :80 for ACME challenges + HTTP→HTTPS redirect.
	if err := mgr.StartHTTPServer(); err != nil {
		return nil, nil, fmt.Errorf("start challenge server: %w", err)
	}

	// Load cached cert or issue a new one (blocks).
	issueCtx, issueCancel := context.WithTimeout(context.Background(), 5*time.Minute)
	err := mgr.EnsureCert(issueCtx)
	issueCancel()
	if err != nil {
		mgr.StopHTTPServer()
		return nil, nil, err
	}

	// Start background renewal.
	renewCtx, renewCancel := context.WithCancel(context.Background())
	go mgr.RenewLoop(renewCtx)

	cfg := &tls.Config{
		GetCertificate: mgr.GetCertificate,
		MinVersion:     tls.VersionTLS12,
	}

	cleanup := func() {
		renewCancel()
		mgr.StopHTTPServer()
	}

	return cfg, cleanup, nil
}

// tlsSetupMulti configures TLS for multiple hostnames using a shared cert store.
// Used by the router and multi-hostname daemons.
//
// Modes:
//   - ACME: issues certs for all hostnames via Let's Encrypt
//   - Manual: loads a single cert file (fallback, all hostnames use same cert)
//
// Returns the cert store, HTTP server (for :80), and cleanup function.
func tlsSetupMulti(hostnames []string, enabled bool, certFile, keyFile, email string, staging bool, httpAddr string) (*certStore, *http.Server, func(), error) {
	if !enabled && certFile == "" && keyFile == "" {
		return nil, nil, func() {}, nil
	}

	dataDir := os.Getenv("TLS_DIR")
	if dataDir == "" {
		dataDir = "./certs"
	}

	directoryURL := ""
	if staging {
		directoryURL = acmeStagingDirectory
	}

	store := newCertStore(dataDir, email, directoryURL, staging)

	// Manual certificate mode — same cert for all hostnames.
	if certFile != "" || keyFile != "" {
		if certFile == "" || keyFile == "" {
			return nil, nil, nil, fmt.Errorf("--tls-cert and --tls-key must both be specified")
		}
		cert, err := tls.LoadX509KeyPair(certFile, keyFile)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("load certificate: %w", err)
		}
		for _, h := range hostnames {
			store.AddManualCert(h, &cert)
		}
		logInfof("tls: using manual certificate for %d hostnames", len(hostnames))
		return store, nil, func() {}, nil
	}

	// ACME mode — issue certs for each hostname.
	if len(hostnames) == 0 {
		return nil, nil, nil, fmt.Errorf("at least one hostname is required for ACME")
	}

	// Start :80 for ACME challenges.
	httpSrv, err := store.StartHTTPServer(httpAddr)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("start challenge server: %w", err)
	}

	// Issue certs for all hostnames (blocking).
	for _, h := range hostnames {
		issueCtx, issueCancel := context.WithTimeout(context.Background(), 5*time.Minute)
		if err := store.EnsureCert(issueCtx, h); err != nil {
			issueCancel()
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			httpSrv.Shutdown(ctx)
			cancel()
			return nil, nil, nil, fmt.Errorf("cert for %s: %w", h, err)
		}
		issueCancel()
	}

	// Start background renewals.
	store.StartRenewals()

	cleanup := func() {
		store.StopRenewals()
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		httpSrv.Shutdown(ctx)
		cancel()
	}

	return store, httpSrv, cleanup, nil
}
