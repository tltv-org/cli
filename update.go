package main

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const (
	updateRepo    = "tltv-org/cli"
	updateBaseURL = "https://api.github.com/repos/" + updateRepo + "/releases/latest"
)

type ghRelease struct {
	TagName string    `json:"tag_name"`
	Assets  []ghAsset `json:"assets"`
}

type ghAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

func findReleaseAsset(assets []ghAsset, wantSuffix string) *ghAsset {
	for i := range assets {
		if strings.HasSuffix(assets[i].Name, wantSuffix) {
			return &assets[i]
		}
	}
	return nil
}

func downloadReleaseAsset(client *http.Client, assetURL string) ([]byte, error) {
	resp, err := client.Get(assetURL)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download returned %d", resp.StatusCode)
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, 100<<20))
	if err != nil {
		return nil, err
	}
	return data, nil
}

func parseChecksums(data []byte) (map[string]string, error) {
	checksums := make(map[string]string)
	s := bufio.NewScanner(bytes.NewReader(data))
	lineNo := 0
	for s.Scan() {
		lineNo++
		line := strings.TrimSpace(s.Text())
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			return nil, fmt.Errorf("invalid checksum line %d", lineNo)
		}
		hash := fields[0]
		if len(hash) != sha256.Size*2 {
			return nil, fmt.Errorf("invalid checksum length on line %d", lineNo)
		}
		if _, err := hex.DecodeString(hash); err != nil {
			return nil, fmt.Errorf("invalid checksum hex on line %d: %v", lineNo, err)
		}
		name := strings.TrimPrefix(fields[len(fields)-1], "*")
		checksums[name] = strings.ToLower(hash)
	}
	if err := s.Err(); err != nil {
		return nil, err
	}
	return checksums, nil
}

func verifyChecksum(data []byte, expectedHex string) error {
	want, err := hex.DecodeString(strings.TrimSpace(expectedHex))
	if err != nil {
		return fmt.Errorf("invalid expected checksum: %v", err)
	}
	got := sha256.Sum256(data)
	if !bytes.Equal(got[:], want) {
		return fmt.Errorf("checksum mismatch: got %x, want %s", got, strings.ToLower(strings.TrimSpace(expectedHex)))
	}
	return nil
}

func cmdUpdate(args []string) {
	if version == "dev" {
		fatal("cannot update a development build; install from a release first")
	}

	currentVersion := version
	if !strings.HasPrefix(currentVersion, "v") {
		currentVersion = "v" + currentVersion
	}

	// Fetch latest release info from GitHub API.
	if !flagJSON {
		fmt.Printf("Checking for updates...\n")
	}

	client := &http.Client{Timeout: 15 * time.Second}
	req, err := http.NewRequest("GET", updateBaseURL, nil)
	if err != nil {
		fatal("update: %v", err)
	}
	req.Header.Set("User-Agent", "tltv-cli/"+version)
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := client.Do(req)
	if err != nil {
		fatal("update: failed to check for updates: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		fatal("update: GitHub API returned %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20)) // 1 MB limit
	if err != nil {
		fatal("update: %v", err)
	}

	var release ghRelease
	if err := json.Unmarshal(body, &release); err != nil {
		fatal("update: failed to parse release info: %v", err)
	}

	latestVersion := release.TagName

	if latestVersion == currentVersion {
		if flagJSON {
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			enc.Encode(map[string]interface{}{
				"current":    currentVersion,
				"latest":     latestVersion,
				"up_to_date": true,
			})
		} else {
			printOK(fmt.Sprintf("already up to date (%s)", currentVersion))
		}
		return
	}

	if !flagJSON {
		fmt.Printf("  %s -> %s\n", c(cDim, currentVersion), c(cGreen, latestVersion))
	}

	// Find the matching asset for this OS/arch.
	ext := ".tar.gz"
	if runtime.GOOS == "windows" {
		ext = ".zip"
	}
	wantSuffix := fmt.Sprintf("%s-%s%s", runtime.GOOS, runtime.GOARCH, ext)

	asset := findReleaseAsset(release.Assets, wantSuffix)
	if asset == nil {
		fatal("update: no release asset found for %s/%s", runtime.GOOS, runtime.GOARCH)
	}
	checksumsAsset := findReleaseAsset(release.Assets, "checksums.txt")
	if checksumsAsset == nil {
		fatal("update: release is missing checksums.txt")
	}

	// Download the archive.
	if !flagJSON {
		fmt.Printf("  Downloading %s/%s binary...\n", runtime.GOOS, runtime.GOARCH)
	}

	archive, err := downloadReleaseAsset(client, asset.BrowserDownloadURL)
	if err != nil {
		fatal("update: download failed: %v", err)
	}
	checksumsData, err := downloadReleaseAsset(client, checksumsAsset.BrowserDownloadURL)
	if err != nil {
		fatal("update: failed to download checksums: %v", err)
	}
	checksums, err := parseChecksums(checksumsData)
	if err != nil {
		fatal("update: invalid checksums.txt: %v", err)
	}
	expectedChecksum, ok := checksums[asset.Name]
	if !ok {
		fatal("update: checksums.txt missing entry for %s", asset.Name)
	}
	if err := verifyChecksum(archive, expectedChecksum); err != nil {
		fatal("update: %v", err)
	}

	// Extract the binary from the archive.
	binName := "tltv"
	if runtime.GOOS == "windows" {
		binName = "tltv.exe"
	}

	var newBinary []byte
	if runtime.GOOS == "windows" {
		newBinary, err = extractFromZip(archive, binName)
	} else {
		newBinary, err = extractFromTarGz(archive, binName)
	}
	if err != nil {
		fatal("update: %v", err)
	}

	// Replace the running binary.
	execPath, err := os.Executable()
	if err != nil {
		fatal("update: cannot determine executable path: %v", err)
	}
	execPath, err = filepath.EvalSymlinks(execPath)
	if err != nil {
		fatal("update: cannot resolve executable path: %v", err)
	}

	// Write new binary to system temp dir (always writable), then move into place.
	tmp, err := os.CreateTemp("", ".tltv-update-*")
	if err != nil {
		fatal("update: cannot create temp file: %v", err)
	}
	tmpPath := tmp.Name()

	// Clean up temp file on failure.
	defer func() {
		if tmpPath != "" {
			os.Remove(tmpPath)
		}
	}()

	if _, err := tmp.Write(newBinary); err != nil {
		tmp.Close()
		fatal("update: write failed: %v", err)
	}
	if err := tmp.Close(); err != nil {
		fatal("update: %v", err)
	}

	// Preserve permissions from the original binary.
	info, err := os.Stat(execPath)
	if err != nil {
		fatal("update: %v", err)
	}
	if err := os.Chmod(tmpPath, info.Mode()); err != nil {
		fatal("update: %v", err)
	}

	// Try atomic rename (works when same filesystem).
	if err := os.Rename(tmpPath, execPath); err != nil {
		// Cross-device or permission denied -- fall back to copy.
		if err := copyFile(tmpPath, execPath); err != nil {
			fatal("update: cannot replace %s: %v\n  try: sudo tltv update", execPath, err)
		}
	}
	tmpPath = "" // Prevent deferred cleanup.

	if flagJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(map[string]interface{}{
			"current":    currentVersion,
			"latest":     latestVersion,
			"up_to_date": false,
			"updated":    true,
		})
	} else {
		printOK(fmt.Sprintf("updated to %s", latestVersion))
	}
}

func extractFromTarGz(data []byte, name string) ([]byte, error) {
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("failed to decompress archive: %v", err)
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("failed to read archive: %v", err)
		}
		if filepath.Base(hdr.Name) == name && hdr.Typeflag == tar.TypeReg {
			return io.ReadAll(io.LimitReader(tr, 100<<20))
		}
	}
	return nil, fmt.Errorf("binary %q not found in archive", name)
}

func extractFromZip(data []byte, name string) ([]byte, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("failed to read zip archive: %v", err)
	}
	for _, f := range zr.File {
		if filepath.Base(f.Name) == name {
			rc, err := f.Open()
			if err != nil {
				return nil, fmt.Errorf("failed to extract %s: %v", name, err)
			}
			defer rc.Close()
			return io.ReadAll(io.LimitReader(rc, 100<<20))
		}
	}
	return nil, fmt.Errorf("binary %q not found in archive", name)
}

// copyFile copies src to dst by reading and writing (works across filesystems).
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_TRUNC, 0)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, in)
	return err
}
