package main

import "testing"

func TestNewLoadtestValidationReceiver_UsesRequestedQuality(t *testing.T) {
	client := newClient(false)
	recv := newLoadtestValidationReceiver("target", "", client, "720p")
	if recv.Quality != "720p" {
		t.Fatalf("Quality = %q, want 720p", recv.Quality)
	}
	if !recv.VerifyMetadata {
		t.Fatal("VerifyMetadata = false, want true for TLTV targets")
	}
}

func TestNewLoadtestValidationReceiver_DirectURLSkipsMetadataVerify(t *testing.T) {
	client := newClient(false)
	recv := newLoadtestValidationReceiver("", "https://example.com/stream.m3u8", client, "worst")
	if recv.Quality != "worst" {
		t.Fatalf("Quality = %q, want worst", recv.Quality)
	}
	if recv.VerifyMetadata {
		t.Fatal("VerifyMetadata = true, want false for direct URLs")
	}
}
