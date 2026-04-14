package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"fmt"
	"strings"
	"testing"
)

func makeTarGz(t *testing.T, files map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range files {
		hdr := &tar.Header{Name: name, Mode: 0644, Size: int64(len(body))}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(body); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func makeZip(t *testing.T, files map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(body); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestExtractFromTarGz(t *testing.T) {
	archive := makeTarGz(t, map[string][]byte{"tltv": []byte("binary-data")})
	got, err := extractFromTarGz(archive, "tltv")
	if err != nil {
		t.Fatalf("extractFromTarGz: %v", err)
	}
	if string(got) != "binary-data" {
		t.Fatalf("got %q, want binary-data", got)
	}
}

func TestExtractFromZip(t *testing.T) {
	archive := makeZip(t, map[string][]byte{"tltv.exe": []byte("binary-data")})
	got, err := extractFromZip(archive, "tltv.exe")
	if err != nil {
		t.Fatalf("extractFromZip: %v", err)
	}
	if string(got) != "binary-data" {
		t.Fatalf("got %q, want binary-data", got)
	}
}

func TestExtractFromTarGz_MissingBinary(t *testing.T) {
	archive := makeTarGz(t, map[string][]byte{"other": []byte("x")})
	_, err := extractFromTarGz(archive, "tltv")
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("err = %v, want not found", err)
	}
}

func TestExtractFromZip_MissingBinary(t *testing.T) {
	archive := makeZip(t, map[string][]byte{"other": []byte("x")})
	_, err := extractFromZip(archive, "tltv.exe")
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("err = %v, want not found", err)
	}
}

func TestExtractFromTarGz_CorruptArchive(t *testing.T) {
	_, err := extractFromTarGz([]byte("not-a-tar-gz"), "tltv")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestExtractFromZip_CorruptArchive(t *testing.T) {
	_, err := extractFromZip([]byte("not-a-zip"), "tltv.exe")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestExtractFromTarGz_EmptyArchive(t *testing.T) {
	archive := makeTarGz(t, map[string][]byte{})
	_, err := extractFromTarGz(archive, "tltv")
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("err = %v, want not found", err)
	}
}

func TestExtractFromZip_EmptyArchive(t *testing.T) {
	archive := makeZip(t, map[string][]byte{})
	_, err := extractFromZip(archive, "tltv.exe")
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("err = %v, want not found", err)
	}
}

func TestParseChecksums(t *testing.T) {
	data := []byte("abc123  file1.tar.gz\n" + strings.Repeat("a", 64) + " *file2.zip\n")
	_, err := parseChecksums(data)
	if err == nil {
		t.Fatal("expected invalid checksum error for short hash")
	}

	valid := []byte(strings.Repeat("a", 64) + "  file1.tar.gz\n" + strings.Repeat("b", 64) + " *file2.zip\n")
	checksums, err := parseChecksums(valid)
	if err != nil {
		t.Fatalf("parseChecksums: %v", err)
	}
	if checksums["file1.tar.gz"] != strings.Repeat("a", 64) {
		t.Fatalf("file1 checksum = %q", checksums["file1.tar.gz"])
	}
	if checksums["file2.zip"] != strings.Repeat("b", 64) {
		t.Fatalf("file2 checksum = %q", checksums["file2.zip"])
	}
}

func TestVerifyChecksum(t *testing.T) {
	data := []byte("hello")
	hash := sha256.Sum256(data)
	if err := verifyChecksum(data, fmt.Sprintf("%x", hash)); err != nil {
		t.Fatalf("verifyChecksum match: %v", err)
	}
	if err := verifyChecksum(data, strings.Repeat("0", 64)); err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("verifyChecksum mismatch err = %v", err)
	}
}
