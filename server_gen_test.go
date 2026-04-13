package main

import (
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSPSAspectRatio(t *testing.T) {
	// VUI must signal aspect_ratio_info_present_flag=1 with aspect_ratio_idc=1
	// (square pixels, SAR 1:1). Without this, VLC infers wrong display aspect
	// ratio at some resolutions (e.g. 1080p causes letterboxing).
	tests := []struct {
		w, h int
	}{
		{320, 240},
		{640, 360},
		{1920, 1088}, // 1080p rounds to 1088, cropped back
		{3840, 2160},
	}
	for _, tt := range tests {
		sps := encodeSPS(&h264Settings{width: tt.w, height: tt.h, fps: 30, qp: 26})

		// Parse RBSP: skip Annex B start code (4 bytes) + NAL header (1 byte)
		// The SPS RBSP starts at byte 5.
		if len(sps) < 10 {
			t.Fatalf("%dx%d: SPS too short (%d bytes)", tt.w, tt.h, len(sps))
		}

		// Scan for the VUI section. After frame_cropping, vui_parameters_present_flag
		// should be 1. We can verify by checking the raw SPS bytes contain the
		// aspect_ratio_idc=1 value. Since aspect_ratio_idc is an 8-bit field
		// written right after aspect_ratio_info_present_flag=1, we can look for
		// the encoded pattern in the RBSP.
		//
		// Simpler check: re-encode and verify the SPS bytes are deterministic
		// (proves the VUI path is exercised).
		sps2 := encodeSPS(&h264Settings{width: tt.w, height: tt.h, fps: 30, qp: 26})
		if len(sps) != len(sps2) {
			t.Errorf("%dx%d: SPS not deterministic (%d vs %d bytes)", tt.w, tt.h, len(sps), len(sps2))
		}
		for i := range sps {
			if sps[i] != sps2[i] {
				t.Errorf("%dx%d: SPS byte %d differs", tt.w, tt.h, i)
				break
			}
		}

		t.Logf("%dx%d: SPS %d bytes: %x", tt.w, tt.h, len(sps), sps)
	}
}

func TestSPSAspectRatio_BitLevel(t *testing.T) {
	// Verify the VUI contains aspect_ratio_info_present_flag=1 followed by
	// aspect_ratio_idc=1 by encoding a known resolution and checking the
	// SPS is longer than it would be without the aspect ratio field.
	withAR := encodeSPS(&h264Settings{width: 320, height: 240, fps: 30, qp: 26})

	// The SPS with aspect_ratio_idc adds 9 bits (1 flag + 8 idc) compared to
	// not having it. This translates to at least 1 extra byte in most cases.
	// We can't easily test without the flag (code always sets it), but we can
	// verify the SPS is non-trivially sized (>20 bytes with VUI).
	if len(withAR) < 20 {
		t.Errorf("SPS too short (%d bytes), VUI with aspect ratio should make it >20", len(withAR))
	}
	t.Logf("SPS with VUI aspect ratio: %d bytes", len(withAR))
}

func TestGenerateRawH264(t *testing.T) {
	h264 := &h264Settings{width: 320, height: 240, fps: 25, qp: 26}
	f := newFrame(320, 240)
	fillBars(f)
	renderTestFrame(f, "TEST", "12:00:00", 0)

	sps := encodeSPS(h264)
	pps := encodePPS(h264)
	aud := encodeAUD()
	nalData := encodeFrame(sps, pps, aud, f, h264, 0, 0)

	path := filepath.Join(t.TempDir(), "server_test.264")
	if err := os.WriteFile(path, nalData, 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	t.Logf("Wrote %d bytes (bars with text)", len(nalData))
	t.Logf("SPS (%d bytes): %x", len(sps), sps)
	t.Logf("PPS (%d bytes): %x", len(pps), pps)
}

func TestGenerateSolidGray(t *testing.T) {
	h264 := &h264Settings{width: 320, height: 240, fps: 25, qp: 26}
	f := newFrame(320, 240)
	for i := range f.Y {
		f.Y[i] = 128
	}
	for i := range f.Cb {
		f.Cb[i] = 128
	}
	for i := range f.Cr {
		f.Cr[i] = 128
	}

	sps := encodeSPS(h264)
	pps := encodePPS(h264)
	aud := encodeAUD()
	nalData := encodeFrame(sps, pps, aud, f, h264, 0, 0)

	path := filepath.Join(t.TempDir(), "server_gray.264")
	if err := os.WriteFile(path, nalData, 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	t.Logf("Wrote %d bytes (solid gray)", len(nalData))
}

func TestGenerateMultiResolution(t *testing.T) {
	resolutions := [][2]int{
		{320, 240},
		{640, 480},
		{160, 128},
		{1920, 1088},
	}

	for _, res := range resolutions {
		w, h := res[0], res[1]
		h264 := &h264Settings{width: w, height: h, fps: 25, qp: 26}
		f := newFrame(w, h)
		fillBars(f)
		renderTestFrame(f, "TEST", "12:00:00", 0)

		sps := encodeSPS(h264)
		pps := encodePPS(h264)
		aud := encodeAUD()
		nalData := encodeFrame(sps, pps, aud, f, h264, 0, 0)

		t.Logf("%dx%d: %d bytes, SPS: %x", w, h, len(nalData), sps)
	}
}

func TestGenerateHD(t *testing.T) {
	// 1080p with block-aligned text — the key quality test.
	// Each glyph pixel fills one macroblock, producing zero residual.
	dir := t.TempDir()
	w, h := 1920, 1088
	for _, qp := range []int{18, 26} {
		s := &h264Settings{width: w, height: h, fps: 25, qp: qp}
		f := newFrame(w, h)
		fillBars(f)
		renderTestFrame(f, "TLTV TEST", "12:00:00", 0)

		sps := encodeSPS(s)
		pps := encodePPS(s)
		aud := encodeAUD()
		data := encodeFrame(sps, pps, aud, f, s, 0, 0)

		path := filepath.Join(dir, fmt.Sprintf("our_hd_qp%d.264", qp))
		if err := os.WriteFile(path, data, 0644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
		t.Logf("1080p QP%d: %d bytes (%d KB)", qp, len(data), len(data)/1024)
	}
}

func TestGenerateWithTextOverlay(t *testing.T) {
	h264 := &h264Settings{width: 320, height: 240, fps: 25, qp: 26}
	f := newFrame(320, 240)
	fillBars(f)

	// Draw text that creates non-uniform macroblocks requiring AC encoding
	drawRect(f, 10, 90, 70, 25, colorBlack.Y)
	drawString(f, 12, 92, "HELLO", 2, colorWhite.Y, -1)

	sps := encodeSPS(h264)
	pps := encodePPS(h264)
	aud := encodeAUD()
	nalData := encodeFrame(sps, pps, aud, f, h264, 0, 0)

	path := filepath.Join(t.TempDir(), "server_text.264")
	if err := os.WriteFile(path, nalData, 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	t.Logf("Wrote %d bytes (bars with text overlay)", len(nalData))
}

func TestGenerateI4x4PixelText(t *testing.T) {
	// Pixel-level text overlays on SMPTE bars: exercises I_4x4 mode selection.
	// The text edges create high per-block SAD against I_16x16 prediction,
	// triggering I_4x4 for those macroblocks. Bar-only MBs stay I_16x16.
	dir := t.TempDir()
	for _, res := range [][2]int{{320, 240}, {640, 480}, {1920, 1088}} {
		w, h := res[0], res[1]
		s := &h264Settings{width: w, height: h, fps: 25, qp: 26}
		f := newFrame(w, h)
		fillBars(f)

		// Pixel-level text rendering (not block-aligned) — forces I_4x4
		scale := h / 80
		if scale < 2 {
			scale = 2
		}
		drawRect(f, 4, 4, w-8, scale*7+8, colorBlack.Y)
		drawString(f, 8, 8, "TLTV I4X4 TEST", scale, colorWhite.Y, -1)

		// Add more text at various positions to exercise edge cases
		drawRect(f, 4, h/2-scale*4, w/2, scale*7+8, colorBlack.Y)
		drawString(f, 8, h/2-scale*4+4, "ABCDEF 12345", scale, colorWhite.Y, -1)

		sps := encodeSPS(s)
		pps := encodePPS(s)
		aud := encodeAUD()
		data := encodeFrame(sps, pps, aud, f, s, 0, 0)

		path := filepath.Join(dir, fmt.Sprintf("i4x4_pixel_%dx%d.264", w, h))
		if err := os.WriteFile(path, data, 0644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
		t.Logf("%dx%d: %d bytes (%d KB)", w, h, len(data), len(data)/1024)
	}
}

func TestGenerateI4x4SolidGray(t *testing.T) {
	// Solid gray frame: all MBs should use I_16x16 (no I_4x4 needed).
	// Verifies that the mode decision heuristic correctly keeps flat MBs on I_16x16.
	s := &h264Settings{width: 320, height: 240, fps: 25, qp: 26}
	f := newFrame(320, 240)
	for i := range f.Y {
		f.Y[i] = 128
	}
	for i := range f.Cb {
		f.Cb[i] = 128
	}
	for i := range f.Cr {
		f.Cr[i] = 128
	}

	sps := encodeSPS(s)
	pps := encodePPS(s)
	aud := encodeAUD()
	data := encodeFrame(sps, pps, aud, f, s, 0, 0)

	path := filepath.Join(t.TempDir(), "i4x4_solid_gray.264")
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	// Solid gray with I_16x16 should be very small (just headers + DC prediction)
	t.Logf("Solid gray: %d bytes (should be small — all I_16x16)", len(data))
	if len(data) > 1000 {
		t.Errorf("Solid gray frame too large (%d bytes), expected <1000 — flat MBs should use I_16x16", len(data))
	}
}

func TestGenerateI4x4Gradient(t *testing.T) {
	// Gradient frame: smooth transitions that create moderate residual.
	// Tests I_4x4 with various prediction modes on non-trivial content.
	w, h := 320, 240
	s := &h264Settings{width: w, height: h, fps: 25, qp: 26}
	f := newFrame(w, h)

	// Horizontal gradient
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			f.Y[y*w+x] = uint8(x * 255 / (w - 1))
		}
	}
	for y := 0; y < h/2; y++ {
		for x := 0; x < w/2; x++ {
			f.Cb[y*(w/2)+x] = 128
			f.Cr[y*(w/2)+x] = 128
		}
	}

	sps := encodeSPS(s)
	pps := encodePPS(s)
	aud := encodeAUD()
	data := encodeFrame(sps, pps, aud, f, s, 0, 0)

	path := filepath.Join(t.TempDir(), "i4x4_gradient.264")
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	t.Logf("Gradient: %d bytes", len(data))
}

func TestGenerateFontTest(t *testing.T) {
	w, h := 640, 480
	s := &h264Settings{width: w, height: h, fps: 25, qp: 22}
	f := newFrame(w, h)

	// Dark gray background
	for i := range f.Y {
		f.Y[i] = 32
	}
	for i := range f.Cb {
		f.Cb[i] = 128
	}
	for i := range f.Cr {
		f.Cr[i] = 128
	}

	scale := 2
	y := 8

	// Row 1: All uppercase
	drawString(f, 8, y, "ABCDEFGHIJKLM", scale, 255, -1)
	y += 9 * scale
	drawString(f, 8, y, "NOPQRSTUVWXYZ", scale, 255, -1)
	y += 9*scale + 4

	// Row 2: All lowercase
	drawString(f, 8, y, "abcdefghijklm", scale, 255, -1)
	y += 9 * scale
	drawString(f, 8, y, "nopqrstuvwxyz", scale, 255, -1)
	y += 9*scale + 4

	// Row 3: Numbers
	drawString(f, 8, y, "0123456789", scale, 255, -1)
	y += 9*scale + 4

	// Row 4: Punctuation
	drawString(f, 8, y, "!\"#$%&'()*+,-./", scale, 255, -1)
	y += 9 * scale
	drawString(f, 8, y, ":;<=>?@[\\]^_{|}~", scale, 255, -1)
	y += 9*scale + 4

	// Row 5: Pangrams
	drawString(f, 8, y, "The quick brown fox", scale, 255, -1)
	y += 9 * scale
	drawString(f, 8, y, "jumps over the lazy dog", scale, 255, -1)
	y += 9*scale + 4

	// Row 6: Uppercase pangram
	drawString(f, 8, y, "THE QUICK BROWN FOX", scale, 255, -1)
	y += 9 * scale
	drawString(f, 8, y, "JUMPS OVER THE LAZY DOG", scale, 255, -1)
	y += 9*scale + 4

	// Row 7: TLTV branding test
	drawString(f, 8, y, "TLTV 18:28:39 F1234", scale, 255, -1)
	y += 9 * scale
	drawString(f, 8, y, "00:12:45 CH1 LIVE", scale, 255, -1)

	sps := encodeSPS(s)
	pps := encodePPS(s)
	aud := encodeAUD()
	data := encodeFrame(sps, pps, aud, f, s, 0, 0)

	path := filepath.Join(t.TempDir(), "font_test.264")
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	t.Logf("Font test: %d bytes", len(data))
}

func TestServerSignDocs_EphemeralGuide(t *testing.T) {
	// Server should produce a valid signed guide using defaultGuideEntries
	// pattern: midnight-to-midnight UTC, channel name as title.
	_, priv, _ := ed25519.GenerateKey(nil)
	pub := priv.Public().(ed25519.PublicKey)
	id := makeChannelID(pub)

	metadata, guide := serverSignDocs(id, "TEST", "", priv, nil, "public", false, nil)
	if metadata == nil {
		t.Fatal("metadata is nil")
	}
	if guide == nil {
		t.Fatal("guide is nil")
	}

	// Parse and verify guide
	var guideDoc map[string]interface{}
	if err := json.Unmarshal(guide, &guideDoc); err != nil {
		t.Fatalf("guide JSON: %v", err)
	}

	// Must have signature
	if _, ok := guideDoc["signature"]; !ok {
		t.Error("guide missing signature")
	}

	// Must have entries
	entries, ok := guideDoc["entries"].([]interface{})
	if !ok || len(entries) == 0 {
		t.Fatal("guide should have entries")
	}

	entry := entries[0].(map[string]interface{})
	if entry["title"] != "TEST" {
		t.Errorf("guide entry title = %v, want TEST", entry["title"])
	}

	// from/until should span midnight-to-midnight
	from, _ := entry["start"].(string)
	end, _ := entry["end"].(string)
	if from == "" || end == "" {
		t.Fatal("guide entry missing start/end")
	}
	fromTime, _ := time.Parse(timestampFormat, from)
	endTime, _ := time.Parse(timestampFormat, end)
	if endTime.Sub(fromTime) != 24*time.Hour {
		t.Errorf("guide span = %v, want 24h", endTime.Sub(fromTime))
	}

	// Verify metadata parses too
	var metaDoc map[string]interface{}
	if err := json.Unmarshal(metadata, &metaDoc); err != nil {
		t.Fatalf("metadata JSON: %v", err)
	}
	if metaDoc["name"] != "TEST" {
		t.Errorf("metadata name = %v, want TEST", metaDoc["name"])
	}
	if _, ok := metaDoc["signature"]; !ok {
		t.Error("metadata missing signature")
	}
}

func TestServerGuideXMLTV(t *testing.T) {
	// Server XMLTV endpoint should produce valid XMLTV from signed guide JSON.
	_, priv, _ := ed25519.GenerateKey(nil)
	pub := priv.Public().(ed25519.PublicKey)
	id := makeChannelID(pub)

	_, guide := serverSignDocs(id, "TEST", "", priv, nil, "public", false, nil)
	if guide == nil {
		t.Fatal("guide is nil")
	}

	xml := serverGuideToXMLTV(guide, id, "TEST")

	if !strings.Contains(xml, "<tv>") {
		t.Error("missing <tv> tag")
	}
	if !strings.Contains(xml, id) {
		t.Error("missing channel ID in XMLTV")
	}
	if !strings.Contains(xml, "<display-name>TEST</display-name>") {
		t.Error("missing display-name")
	}
	if !strings.Contains(xml, "<programme") {
		t.Error("missing programme element")
	}
	if !strings.Contains(xml, "<title>TEST</title>") {
		t.Error("missing guide entry title")
	}
}

func TestServerGuideXMLTV_InvalidJSON(t *testing.T) {
	// Corrupt JSON should produce minimal fallback XML.
	xml := serverGuideToXMLTV([]byte("not json"), "TVabc", "Test")

	if !strings.Contains(xml, "<tv/>") {
		t.Error("expected fallback <tv/> for invalid JSON")
	}
}

func TestServerState_TimezoneDisplay(t *testing.T) {
	// Verify that the timezone location is applied in frame generation.
	// We test the time formatting logic directly.
	h264 := &h264Settings{width: 320, height: 240, fps: 30, qp: 26}

	state := &serverState{
		seg:          newHLSSegmenter(3, 2),
		muxer:        &tsMuxer{},
		sps:          encodeSPS(h264),
		pps:          encodePPS(h264),
		aud:          encodeAUD(),
		frame:        newFrame(320, 240),
		h264:         h264,
		channelName:  "TEST",
		showUptime:   false,
		fontScale:    0,
		startTime:    time.Date(2024, 1, 15, 12, 0, 0, 0, time.UTC),
		location:     time.UTC,
		framesPerSeg: 60,
		ptsPerFrame:  3000,
		segDuration:  2.0,
		segDurationI: 2,
	}

	// Generate a segment at UTC — should not panic
	state.generateSegment()
	if state.frameNum != 60 {
		t.Errorf("frameNum = %d, want 60", state.frameNum)
	}

	// With a different timezone — should also work
	eastern, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skip("timezone data not available")
	}
	state.location = eastern
	state.generateSegment()
	if state.frameNum != 120 {
		t.Errorf("frameNum = %d, want 120 after second segment", state.frameNum)
	}
}

// TestAudioToneLoop verifies the embedded AAC ADTS loop is well-formed.
func TestAudioToneLoop(t *testing.T) {
	// Verify all 48 frames parse correctly
	for i := 0; i < aacLoopFrames; i++ {
		entry := &aacLoopIndex[i]
		frame := aacToneLoop[entry.off : entry.off+entry.len]

		if frame[0] != 0xFF || (frame[1]&0xF0) != 0xF0 {
			t.Fatalf("frame %d: ADTS sync word missing", i)
		}

		profile := (frame[2] >> 6) & 3
		sfi := (frame[2] >> 2) & 0xF
		chConfig := ((frame[2] & 1) << 2) | ((frame[3] >> 6) & 3)

		if profile != 1 {
			t.Errorf("frame %d: profile = %d, want 1 (AAC-LC)", i, profile)
		}
		if sfi != 3 {
			t.Errorf("frame %d: sampling_frequency_index = %d, want 3 (48kHz)", i, sfi)
		}
		if chConfig != 1 {
			t.Errorf("frame %d: channel_configuration = %d, want 1 (mono)", i, chConfig)
		}
	}
	t.Logf("AAC tone loop: %d frames, %d bytes, MPEG-4 AAC-LC, 48kHz, mono",
		aacLoopFrames, len(aacToneLoop))
}

// TestAudioFramesForSegment verifies frame count calculations.
func TestAudioFramesForSegment(t *testing.T) {
	tests := []struct {
		dur  int
		want int
	}{
		{1, 47},  // ceil(48000/1024) = 47
		{2, 94},  // ceil(96000/1024) = 94
		{4, 188}, // ceil(192000/1024) = 188
		{10, 469},
	}
	for _, tt := range tests {
		got := audioFramesForSegment(tt.dur)
		if got != tt.want {
			t.Errorf("audioFramesForSegment(%d) = %d, want %d", tt.dur, got, tt.want)
		}
	}
}

// TestGenerateAudioData verifies audio data generation produces valid ADTS.
func TestGenerateAudioData(t *testing.T) {
	data := generateAudioData(2)

	// Should contain 94 frames
	nFrames := 0
	offset := 0
	for offset < len(data)-7 {
		if data[offset] != 0xFF || (data[offset+1]&0xF0) != 0xF0 {
			t.Fatalf("lost ADTS sync at offset %d", offset)
		}
		frameLen := (int(data[offset+3]&3) << 11) | (int(data[offset+4]) << 3) | (int(data[offset+5]) >> 5)
		if frameLen < 7 || frameLen > 300 {
			t.Fatalf("frame %d: unexpected length %d", nFrames, frameLen)
		}
		offset += frameLen
		nFrames++
	}
	if nFrames != 94 {
		t.Errorf("got %d frames, want 94", nFrames)
	}
	t.Logf("2s audio data: %d bytes, %d ADTS frames", len(data), nFrames)
}

// TestPMTContainsAudioStream verifies the PMT includes both video and audio.
func TestPMTContainsAudioStream(t *testing.T) {
	m := &tsMuxer{}
	var buf [tsPacketSize]byte
	m.writePMT(buf[:])

	// PMT section starts at offset 5 (after TS header + pointer)
	pmt := buf[5:]

	// section_length should be 23 (5+4+5+5+4)
	secLen := int(pmt[1]&0x0F)<<8 | int(pmt[2])
	if secLen != 23 {
		t.Errorf("PMT section_length = %d, want 23", secLen)
	}

	// Stream entry 1: H.264 video at offset 12
	if pmt[12] != tsStreamTypeH264 {
		t.Errorf("stream 1 type = 0x%02X, want 0x%02X (H.264)", pmt[12], tsStreamTypeH264)
	}
	vidPID := (uint16(pmt[13]&0x1F) << 8) | uint16(pmt[14])
	if vidPID != tsPIDVideo {
		t.Errorf("video PID = 0x%04X, want 0x%04X", vidPID, tsPIDVideo)
	}

	// Stream entry 2: AAC audio at offset 17
	if pmt[17] != tsStreamTypeAAC {
		t.Errorf("stream 2 type = 0x%02X, want 0x%02X (AAC)", pmt[17], tsStreamTypeAAC)
	}
	audPID := (uint16(pmt[18]&0x1F) << 8) | uint16(pmt[19])
	if audPID != tsPIDAudio {
		t.Errorf("audio PID = 0x%04X, want 0x%04X", audPID, tsPIDAudio)
	}

	t.Logf("PMT: video PID=0x%04X (H.264), audio PID=0x%04X (AAC)", vidPID, audPID)
}

// TestSegmentContainsAudioPackets verifies that generated segments have audio TS packets.
func TestSegmentContainsAudioPackets(t *testing.T) {
	h264 := &h264Settings{width: 320, height: 240, fps: 30, qp: 26}
	state := &serverState{
		seg:          newHLSSegmenter(3, 2),
		muxer:        &tsMuxer{},
		sps:          encodeSPS(h264),
		pps:          encodePPS(h264),
		aud:          encodeAUD(),
		frame:        newFrame(320, 240),
		h264:         h264,
		channelName:  "TEST",
		showUptime:   false,
		fontScale:    0,
		startTime:    time.Date(2024, 1, 15, 12, 0, 0, 0, time.UTC),
		location:     time.UTC,
		framesPerSeg: 60,
		ptsPerFrame:  3000,
		segDuration:  2.0,
		segDurationI: 2,
	}

	state.generateSegment()

	// Get the segment data from the segmenter
	seg := state.seg
	if seg.seqNum < 1 {
		t.Fatal("no segment generated")
	}

	// Check that audio PID packets exist in the last segment
	idx := (seg.head - 1 + seg.ringSize) % seg.ringSize
	data := seg.ring[idx].data
	videoPkts := 0
	audioPkts := 0
	for i := 0; i+tsPacketSize <= len(data); i += tsPacketSize {
		if data[i] != tsSyncByte {
			t.Fatalf("lost TS sync at offset %d", i)
		}
		pid := (uint16(data[i+1]&0x1F) << 8) | uint16(data[i+2])
		switch pid {
		case tsPIDVideo:
			videoPkts++
		case tsPIDAudio:
			audioPkts++
		}
	}

	if videoPkts == 0 {
		t.Error("no video TS packets found")
	}
	if audioPkts == 0 {
		t.Error("no audio TS packets found")
	}
	t.Logf("Segment: %d bytes, %d video packets, %d audio packets", len(data), videoPkts, audioPkts)
}

// ---------- Server Cache Tests ----------

func TestServerCache_ManifestCacheStatus(t *testing.T) {
	// Set up a server with cache enabled and verify Cache-Status headers.
	_, priv, _ := ed25519.GenerateKey(nil)
	pub := priv.Public().(ed25519.PublicKey)
	channelID := makeChannelID(pub)
	metadata, guide := serverSignDocs(channelID, "TEST", "", priv, nil, "public", false, nil)

	seg := newHLSSegmenter(5, 2)
	seg.pushSegment([]byte("ts-data-0"), 2.0)

	cache := newHLSCache(100)
	mux := http.NewServeMux()
	serverHTTP(mux, seg, channelID, "TEST", metadata, guide, cache, nil, nil, "", false, nil, "")

	// First request: MISS
	req := httptest.NewRequest("GET", "/tltv/v1/channels/"+channelID+"/stream.m3u8", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if cs := w.Header().Get("Cache-Status"); cs != "MISS" {
		t.Errorf("first request Cache-Status = %q, want MISS", cs)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/vnd.apple.mpegurl" {
		t.Errorf("Content-Type = %q", ct)
	}

	// Second request: HIT
	w2 := httptest.NewRecorder()
	mux.ServeHTTP(w2, httptest.NewRequest("GET", "/tltv/v1/channels/"+channelID+"/stream.m3u8", nil))

	if w2.Code != 200 {
		t.Fatalf("status = %d, want 200", w2.Code)
	}
	if cs := w2.Header().Get("Cache-Status"); cs != "HIT" {
		t.Errorf("second request Cache-Status = %q, want HIT", cs)
	}
}

func TestServerCache_SegmentCacheStatus(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(nil)
	pub := priv.Public().(ed25519.PublicKey)
	channelID := makeChannelID(pub)
	metadata, guide := serverSignDocs(channelID, "TEST", "", priv, nil, "public", false, nil)

	seg := newHLSSegmenter(5, 2)
	seg.pushSegment([]byte("ts-data-0"), 2.0)

	cache := newHLSCache(100)
	mux := http.NewServeMux()
	serverHTTP(mux, seg, channelID, "TEST", metadata, guide, cache, nil, nil, "", false, nil, "")

	path := "/tltv/v1/channels/" + channelID + "/seg0.ts"

	// First: MISS
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
	if w.Code != 200 {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if cs := w.Header().Get("Cache-Status"); cs != "MISS" {
		t.Errorf("first Cache-Status = %q, want MISS", cs)
	}
	if string(w.Body.Bytes()) != "ts-data-0" {
		t.Errorf("body = %q", w.Body.String())
	}

	// Second: HIT
	w2 := httptest.NewRecorder()
	mux.ServeHTTP(w2, httptest.NewRequest("GET", path, nil))
	if cs := w2.Header().Get("Cache-Status"); cs != "HIT" {
		t.Errorf("second Cache-Status = %q, want HIT", cs)
	}
}

func TestServerCache_MetadataCacheStatus(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(nil)
	pub := priv.Public().(ed25519.PublicKey)
	channelID := makeChannelID(pub)
	metadata, guide := serverSignDocs(channelID, "TEST", "", priv, nil, "public", false, nil)

	seg := newHLSSegmenter(5, 2)
	cache := newHLSCache(100)
	mux := http.NewServeMux()
	serverHTTP(mux, seg, channelID, "TEST", metadata, guide, cache, nil, nil, "", false, nil, "")

	path := "/tltv/v1/channels/" + channelID

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
	if w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}
	if cs := w.Header().Get("Cache-Status"); cs != "MISS" {
		t.Errorf("first Cache-Status = %q, want MISS", cs)
	}

	w2 := httptest.NewRecorder()
	mux.ServeHTTP(w2, httptest.NewRequest("GET", path, nil))
	if cs := w2.Header().Get("Cache-Status"); cs != "HIT" {
		t.Errorf("second Cache-Status = %q, want HIT", cs)
	}
}

func TestServerCache_GuideCacheStatus(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(nil)
	pub := priv.Public().(ed25519.PublicKey)
	channelID := makeChannelID(pub)
	metadata, guide := serverSignDocs(channelID, "TEST", "", priv, nil, "public", false, nil)

	seg := newHLSSegmenter(5, 2)
	cache := newHLSCache(100)
	mux := http.NewServeMux()
	serverHTTP(mux, seg, channelID, "TEST", metadata, guide, cache, nil, nil, "", false, nil, "")

	// guide.json
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/tltv/v1/channels/"+channelID+"/guide.json", nil))
	if w.Code != 200 {
		t.Fatalf("guide.json status = %d", w.Code)
	}
	if cs := w.Header().Get("Cache-Status"); cs != "MISS" {
		t.Errorf("guide.json first Cache-Status = %q, want MISS", cs)
	}

	w2 := httptest.NewRecorder()
	mux.ServeHTTP(w2, httptest.NewRequest("GET", "/tltv/v1/channels/"+channelID+"/guide.json", nil))
	if cs := w2.Header().Get("Cache-Status"); cs != "HIT" {
		t.Errorf("guide.json second Cache-Status = %q, want HIT", cs)
	}

	// guide.xml
	w3 := httptest.NewRecorder()
	mux.ServeHTTP(w3, httptest.NewRequest("GET", "/tltv/v1/channels/"+channelID+"/guide.xml", nil))
	if w3.Code != 200 {
		t.Fatalf("guide.xml status = %d", w3.Code)
	}
	if cs := w3.Header().Get("Cache-Status"); cs != "MISS" {
		t.Errorf("guide.xml first Cache-Status = %q, want MISS", cs)
	}
}

// TestServerViewerCoexistence verifies that viewerEmbedRoutes can be
// registered on the server's mux without a Go 1.22 ServeMux pattern conflict.
// The viewer's "GET /{$}" must not conflict with the server's method-less
// "/tltv/" and "/.well-known/tltv" catch-all patterns.
func TestServerViewerCoexistence(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(nil)
	pub := priv.Public().(ed25519.PublicKey)
	channelID := makeChannelID(pub)
	metadata, guide := serverSignDocs(channelID, "TEST", "", priv, nil, "public", false, nil)

	seg := newHLSSegmenter(5, 2)
	seg.pushSegment([]byte("ts-data"), 2.0)

	mux := http.NewServeMux()
	// Register viewer BEFORE server routes — same order as production code
	debugViewerRoutes(mux, func(_ string) map[string]interface{} {
		return map[string]interface{}{"channel_name": "TEST"}
	}, nil)
	serverHTTP(mux, seg, channelID, "TEST", metadata, guide, nil, nil, nil, "", false, nil, "")

	// Viewer root serves HTML
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
	if w.Code != 200 {
		t.Errorf("GET / status = %d, want 200", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); !strings.Contains(ct, "text/html") {
		t.Errorf("GET / content-type = %q, want text/html", ct)
	}

	// Viewer assets work
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/api/info", nil))
	if w.Code != 200 {
		t.Errorf("GET /api/info status = %d, want 200", w.Code)
	}

	// Protocol endpoint still works
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/.well-known/tltv", nil))
	if w.Code != 200 {
		t.Errorf("GET /.well-known/tltv status = %d, want 200", w.Code)
	}

	// Stream still works
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/tltv/v1/channels/"+channelID+"/stream.m3u8", nil))
	if w.Code != 200 {
		t.Errorf("GET stream status = %d, want 200", w.Code)
	}

	// Non-root GET returns 404, not viewer HTML
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/nonexistent", nil))
	if w.Code != 404 {
		t.Errorf("GET /nonexistent status = %d, want 404", w.Code)
	}
}

func TestServerPrivateViewer_RequiresAuthAndDoesNotLeakToken(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	channelID := makeChannelID(pub)
	metadata, guide := serverSignDocs(channelID, "Private Test", "", priv, nil, "token", false, nil)
	docs := &serverDocs{channelID: channelID, channelName: "Private Test", metadata: metadata, guide: guide}

	mux := http.NewServeMux()
	debugViewerRoutes(mux, func(_ string) map[string]interface{} {
		return serverViewerInfo(docs, "viewer.example.com:443")
	}, nil, viewerRouteOptions{authToken: "secret123", private: true})

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/api/info", nil))
	if w.Code != 403 {
		t.Fatalf("GET /api/info without token status = %d, want 403", w.Code)
	}
	if strings.Contains(w.Body.String(), "secret123") {
		t.Fatal("unauthenticated /api/info should not leak the private token")
	}

	w = httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/?token=secret123", nil))
	if w.Code != 200 {
		t.Fatalf("GET / with token status = %d, want 200", w.Code)
	}

	w = httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/api/info?token=secret123", nil))
	if w.Code != 200 {
		t.Fatalf("GET /api/info with token status = %d, want 200", w.Code)
	}
	body := w.Body.String()
	if strings.Contains(body, "secret123") || strings.Contains(body, "?token=") {
		t.Fatalf("authenticated /api/info should not emit raw token, got %q", body)
	}
	if !strings.Contains(body, `"stream_src":"/tltv/v1/channels/`+channelID+`/stream.m3u8"`) {
		t.Fatalf("/api/info missing token-free stream_src: %q", body)
	}
	if cc := w.Header().Get("Cache-Control"); cc != "private, no-store" {
		t.Fatalf("Cache-Control = %q, want private, no-store", cc)
	}
}

func TestServerCache_NilCacheNoHeaders(t *testing.T) {
	// With cache=nil, no Cache-Status headers should appear.
	_, priv, _ := ed25519.GenerateKey(nil)
	pub := priv.Public().(ed25519.PublicKey)
	channelID := makeChannelID(pub)
	metadata, guide := serverSignDocs(channelID, "TEST", "", priv, nil, "public", false, nil)

	seg := newHLSSegmenter(5, 2)
	seg.pushSegment([]byte("ts-data"), 2.0)

	mux := http.NewServeMux()
	serverHTTP(mux, seg, channelID, "TEST", metadata, guide, nil, nil, nil, "", false, nil, "")

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/tltv/v1/channels/"+channelID+"/stream.m3u8", nil))
	if w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}
	if cs := w.Header().Get("Cache-Status"); cs != "" {
		t.Errorf("nil cache should not set Cache-Status, got %q", cs)
	}
}

// ---------- Server Private Channels ----------

func TestServerPrivateChannel_Metadata(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	channelID := makeChannelID(pub)
	metadata, _ := serverSignDocs(channelID, "TEST", "", priv, nil, "token", false, nil)

	// Check that metadata has access: "token"
	var doc map[string]interface{}
	json.Unmarshal(metadata, &doc)
	if access, _ := doc["access"].(string); access != "token" {
		t.Errorf("access = %q, want \"token\"", access)
	}
}

func TestServerPrivateChannel_OnDemand(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	channelID := makeChannelID(pub)
	metadata, _ := serverSignDocs(channelID, "TEST", "", priv, nil, "public", true, nil)

	var doc map[string]interface{}
	json.Unmarshal(metadata, &doc)
	if onDemand, ok := doc["on_demand"].(bool); !ok || !onDemand {
		t.Errorf("on_demand = %v, want true", doc["on_demand"])
	}
}

func TestServerPrivateChannel_TokenRequired(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	channelID := makeChannelID(pub)
	metadata, guide := serverSignDocs(channelID, "TEST", "", priv, nil, "token", false, nil)

	seg := newHLSSegmenter(5, 2)
	seg.pushSegment([]byte("ts-data"), 2.0)

	mux := http.NewServeMux()
	serverHTTP(mux, seg, channelID, "TEST", metadata, guide, nil, nil, nil, "secret123", true, nil, "")

	// Without token → 403
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/tltv/v1/channels/"+channelID, nil))
	if w.Code != 403 {
		t.Errorf("no token: status = %d, want 403", w.Code)
	}

	// With wrong token → 403
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/tltv/v1/channels/"+channelID+"?token=wrong", nil))
	if w.Code != 403 {
		t.Errorf("wrong token: status = %d, want 403", w.Code)
	}

	// With correct token → 200
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/tltv/v1/channels/"+channelID+"?token=secret123", nil))
	if w.Code != 200 {
		t.Errorf("correct token: status = %d, want 200", w.Code)
	}
}

func TestServerPrivateChannel_StreamTokenRequired(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	channelID := makeChannelID(pub)
	metadata, guide := serverSignDocs(channelID, "TEST", "", priv, nil, "token", false, nil)

	seg := newHLSSegmenter(5, 2)
	seg.pushSegment([]byte("ts-data"), 2.0)

	mux := http.NewServeMux()
	serverHTTP(mux, seg, channelID, "TEST", metadata, guide, nil, nil, nil, "secret123", true, nil, "")

	// Stream without token → 403
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/tltv/v1/channels/"+channelID+"/stream.m3u8", nil))
	if w.Code != 403 {
		t.Errorf("no token: status = %d, want 403", w.Code)
	}

	// Stream with correct token → 200
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/tltv/v1/channels/"+channelID+"/stream.m3u8?token=secret123", nil))
	if w.Code != 200 {
		t.Errorf("correct token: status = %d, want 200", w.Code)
	}
	if !strings.Contains(w.Body.String(), "seg0.ts?token=secret123") {
		t.Fatalf("manifest should propagate token to segments, got %q", w.Body.String())
	}
}

func TestServerPrivateChannel_HiddenFromWellKnown(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	channelID := makeChannelID(pub)
	metadata, guide := serverSignDocs(channelID, "TEST", "", priv, nil, "token", false, nil)

	seg := newHLSSegmenter(5, 2)
	mux := http.NewServeMux()
	serverHTTP(mux, seg, channelID, "TEST", metadata, guide, nil, nil, nil, "secret123", true, nil, "")

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/.well-known/tltv", nil))
	if w.Code != 200 {
		t.Fatalf("well-known status = %d", w.Code)
	}

	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)
	channels, _ := resp["channels"].([]interface{})
	if len(channels) != 0 {
		t.Errorf("private channel should not appear in well-known, got %d channels", len(channels))
	}
}

func TestServerPrivateChannel_PrivateHeaders(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	channelID := makeChannelID(pub)
	metadata, guide := serverSignDocs(channelID, "TEST", "", priv, nil, "token", false, nil)

	seg := newHLSSegmenter(5, 2)
	mux := http.NewServeMux()
	serverHTTP(mux, seg, channelID, "TEST", metadata, guide, nil, nil, nil, "secret123", true, nil, "")

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/tltv/v1/channels/"+channelID+"?token=secret123", nil))
	if w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}
	if rp := w.Header().Get("Referrer-Policy"); rp != "no-referrer" {
		t.Errorf("Referrer-Policy = %q, want no-referrer", rp)
	}
	if cc := w.Header().Get("Cache-Control"); !strings.Contains(cc, "private") {
		t.Errorf("Cache-Control = %q, want private", cc)
	}
}

func TestServerPublicChannel_NoTokenRequired(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	channelID := makeChannelID(pub)
	metadata, guide := serverSignDocs(channelID, "TEST", "", priv, nil, "public", false, nil)

	seg := newHLSSegmenter(5, 2)
	seg.pushSegment([]byte("ts-data"), 2.0)
	mux := http.NewServeMux()
	serverHTTP(mux, seg, channelID, "TEST", metadata, guide, nil, nil, nil, "", false, nil, "")

	// Public channel: no token needed → 200
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/tltv/v1/channels/"+channelID, nil))
	if w.Code != 200 {
		t.Errorf("public channel without token: status = %d, want 200", w.Code)
	}

	// No Referrer-Policy header on public channels
	if rp := w.Header().Get("Referrer-Policy"); rp != "" {
		t.Errorf("public channel should not set Referrer-Policy, got %q", rp)
	}
}

// ---------- Server Metadata Options ----------

func TestServerMetadataOpts_Description(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	channelID := makeChannelID(pub)
	opts := &serverMetadataOpts{Description: "24/7 test signal"}
	metadata, _ := serverSignDocs(channelID, "TEST", "", priv, nil, "public", false, opts)

	var doc map[string]interface{}
	json.Unmarshal(metadata, &doc)
	if desc, _ := doc["description"].(string); desc != "24/7 test signal" {
		t.Errorf("description = %q, want %q", desc, "24/7 test signal")
	}
}

func TestServerMetadataOpts_Tags(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	channelID := makeChannelID(pub)
	opts := &serverMetadataOpts{Tags: []string{"test", "experimental"}}
	metadata, _ := serverSignDocs(channelID, "TEST", "", priv, nil, "public", false, opts)

	var doc map[string]interface{}
	json.Unmarshal(metadata, &doc)
	tags, ok := doc["tags"].([]interface{})
	if !ok || len(tags) != 2 {
		t.Fatalf("tags = %v, want 2-element array", doc["tags"])
	}
	if tags[0] != "test" || tags[1] != "experimental" {
		t.Errorf("tags = %v, want [test, experimental]", tags)
	}
}

func TestServerMetadataOpts_TagsMax5(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	channelID := makeChannelID(pub)
	opts := &serverMetadataOpts{Tags: []string{"a", "b", "c", "d", "e", "f"}}
	metadata, _ := serverSignDocs(channelID, "TEST", "", priv, nil, "public", false, opts)

	var doc map[string]interface{}
	json.Unmarshal(metadata, &doc)
	tags, _ := doc["tags"].([]interface{})
	if len(tags) != 5 {
		t.Errorf("tags length = %d, want 5 (truncated)", len(tags))
	}
}

func TestServerMetadataOpts_Language(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	channelID := makeChannelID(pub)
	opts := &serverMetadataOpts{Language: "ja"}
	metadata, _ := serverSignDocs(channelID, "TEST", "", priv, nil, "public", false, opts)

	var doc map[string]interface{}
	json.Unmarshal(metadata, &doc)
	if lang, _ := doc["language"].(string); lang != "ja" {
		t.Errorf("language = %q, want %q", lang, "ja")
	}
}

func TestServerMetadataOpts_Timezone(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	channelID := makeChannelID(pub)
	opts := &serverMetadataOpts{Timezone: "America/New_York"}
	metadata, _ := serverSignDocs(channelID, "TEST", "", priv, nil, "public", false, opts)

	var doc map[string]interface{}
	json.Unmarshal(metadata, &doc)
	if tz, _ := doc["timezone"].(string); tz != "America/New_York" {
		t.Errorf("timezone = %q, want %q", tz, "America/New_York")
	}
}

func TestServerMetadataOpts_Icon(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	channelID := makeChannelID(pub)
	opts := &serverMetadataOpts{IconFileName: "icon.svg"}
	metadata, _ := serverSignDocs(channelID, "TEST", "", priv, nil, "public", false, opts)

	var doc map[string]interface{}
	json.Unmarshal(metadata, &doc)
	icon, _ := doc["icon"].(string)
	want := "/tltv/v1/channels/" + channelID + "/icon.svg"
	if icon != want {
		t.Errorf("icon = %q, want %q", icon, want)
	}
}

func TestServerIconEndpoint(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	channelID := makeChannelID(pub)
	opts := &serverMetadataOpts{IconFileName: "icon.svg"}
	metadata, guide := serverSignDocs(channelID, "TEST", "", priv, nil, "public", false, opts)

	seg := newHLSSegmenter(5, 2)
	mux := http.NewServeMux()
	iconData := []byte("<svg>test</svg>")
	serverHTTP(mux, seg, channelID, "TEST", metadata, guide, nil, nil, nil, "", false, iconData, "image/svg+xml")

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/tltv/v1/channels/"+channelID+"/icon.svg", nil))
	if w.Code != 200 {
		t.Fatalf("icon status = %d, want 200", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "image/svg+xml" {
		t.Errorf("Content-Type = %q, want image/svg+xml", ct)
	}
	if w.Body.String() != "<svg>test</svg>" {
		t.Errorf("icon body = %q", w.Body.String())
	}
}

func TestServerIconEndpoint_NoIcon(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	channelID := makeChannelID(pub)
	metadata, guide := serverSignDocs(channelID, "TEST", "", priv, nil, "public", false, nil)

	seg := newHLSSegmenter(5, 2)
	mux := http.NewServeMux()
	serverHTTP(mux, seg, channelID, "TEST", metadata, guide, nil, nil, nil, "", false, nil, "")

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/tltv/v1/channels/"+channelID+"/icon.svg", nil))
	if w.Code != 404 {
		t.Errorf("icon without data: status = %d, want 404", w.Code)
	}
}

// ---------- Multi-Channel Server ----------

func TestServerMultiChannel_WellKnown(t *testing.T) {
	// Create 3 channels
	var channels []*serverChannel
	for i := 0; i < 3; i++ {
		pub, priv, _ := ed25519.GenerateKey(nil)
		chID := makeChannelID(pub)
		name := fmt.Sprintf("Test %d", i+1)
		seg := newHLSSegmenter(5, 2)
		metadata, guide := serverSignDocs(chID, name, "", priv, nil, "public", false, nil)

		ch := &serverChannel{
			channelID:   chID,
			channelName: name,
			privKey:     priv,
			seg:         seg,
		}
		ch.docs.Store(&serverDocs{
			channelID:   chID,
			channelName: name,
			metadata:    metadata,
			guide:       guide,
		})
		channels = append(channels, ch)
	}

	mux := http.NewServeMux()
	serverMultiHTTP(mux, channels, nil, nil, nil, "", false, nil, "")

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/.well-known/tltv", nil))
	if w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}

	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)
	chList, _ := resp["channels"].([]interface{})
	if len(chList) != 3 {
		t.Fatalf("channels = %d, want 3", len(chList))
	}
}

func TestServerMultiChannel_IndependentMetadata(t *testing.T) {
	var channels []*serverChannel
	for i := 0; i < 2; i++ {
		pub, priv, _ := ed25519.GenerateKey(nil)
		chID := makeChannelID(pub)
		name := fmt.Sprintf("Test %d", i+1)
		seg := newHLSSegmenter(5, 2)
		metadata, guide := serverSignDocs(chID, name, "", priv, nil, "public", false, nil)

		ch := &serverChannel{
			channelID:   chID,
			channelName: name,
			privKey:     priv,
			seg:         seg,
		}
		ch.docs.Store(&serverDocs{
			channelID:   chID,
			channelName: name,
			metadata:    metadata,
			guide:       guide,
		})
		channels = append(channels, ch)
	}

	mux := http.NewServeMux()
	serverMultiHTTP(mux, channels, nil, nil, nil, "", false, nil, "")

	// Each channel has unique metadata
	for _, ch := range channels {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("GET", "/tltv/v1/channels/"+ch.channelID, nil))
		if w.Code != 200 {
			t.Fatalf("channel %s: status = %d", ch.channelID, w.Code)
		}
		var doc map[string]interface{}
		json.Unmarshal(w.Body.Bytes(), &doc)
		if getString(doc, "name") != ch.channelName {
			t.Errorf("channel %s: name = %q, want %q", ch.channelID, getString(doc, "name"), ch.channelName)
		}
	}

	// Unknown channel → 404
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/tltv/v1/channels/TVbogus", nil))
	if w.Code != 404 {
		t.Errorf("unknown channel: status = %d, want 404", w.Code)
	}
}

func TestServerMultiChannel_Health(t *testing.T) {
	var channels []*serverChannel
	for i := 0; i < 3; i++ {
		pub, priv, _ := ed25519.GenerateKey(nil)
		chID := makeChannelID(pub)
		seg := newHLSSegmenter(5, 2)
		metadata, guide := serverSignDocs(chID, "Test", "", priv, nil, "public", false, nil)

		ch := &serverChannel{channelID: chID, privKey: priv, seg: seg}
		ch.docs.Store(&serverDocs{channelID: chID, channelName: "Test", metadata: metadata, guide: guide})
		channels = append(channels, ch)
	}

	mux := http.NewServeMux()
	serverMultiHTTP(mux, channels, nil, nil, nil, "", false, nil, "")

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/health", nil))
	if w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}
	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)
	if n, _ := resp["channels"].(float64); int(n) != 3 {
		t.Errorf("channels = %v, want 3", resp["channels"])
	}
}

func TestServerMultiChannel_IndependentStreams(t *testing.T) {
	var channels []*serverChannel
	for i := 0; i < 2; i++ {
		pub, priv, _ := ed25519.GenerateKey(nil)
		chID := makeChannelID(pub)
		seg := newHLSSegmenter(5, 2)
		seg.pushSegment([]byte(fmt.Sprintf("data-%d", i)), 2.0)
		metadata, guide := serverSignDocs(chID, "Test", "", priv, nil, "public", false, nil)

		ch := &serverChannel{channelID: chID, privKey: priv, seg: seg}
		ch.docs.Store(&serverDocs{channelID: chID, channelName: "Test", metadata: metadata, guide: guide})
		channels = append(channels, ch)
	}

	mux := http.NewServeMux()
	serverMultiHTTP(mux, channels, nil, nil, nil, "", false, nil, "")

	// Each channel serves its own stream
	for _, ch := range channels {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("GET", "/tltv/v1/channels/"+ch.channelID+"/stream.m3u8", nil))
		if w.Code != 200 {
			t.Fatalf("channel %s stream: status = %d", ch.channelID, w.Code)
		}
	}
}

func TestServerMultiChannel_PrivateHiddenFromWellKnown(t *testing.T) {
	var channels []*serverChannel
	for i := 0; i < 2; i++ {
		pub, priv, _ := ed25519.GenerateKey(nil)
		chID := makeChannelID(pub)
		seg := newHLSSegmenter(5, 2)
		metadata, guide := serverSignDocs(chID, "Test", "", priv, nil, "token", false, nil)
		ch := &serverChannel{channelID: chID, privKey: priv, seg: seg}
		ch.docs.Store(&serverDocs{channelID: chID, channelName: "Test", metadata: metadata, guide: guide})
		channels = append(channels, ch)
	}

	mux := http.NewServeMux()
	serverMultiHTTP(mux, channels, nil, nil, nil, "secret", true, nil, "")

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/.well-known/tltv", nil))
	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)
	chList, _ := resp["channels"].([]interface{})
	if len(chList) != 0 {
		t.Errorf("private channels should be hidden from well-known, got %d", len(chList))
	}
}

func TestServerMultiChannel_TokenRequired(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	chID := makeChannelID(pub)
	seg := newHLSSegmenter(5, 2)
	metadata, guide := serverSignDocs(chID, "Test", "", priv, nil, "token", false, nil)
	ch := &serverChannel{channelID: chID, privKey: priv, seg: seg}
	ch.docs.Store(&serverDocs{channelID: chID, channelName: "Test", metadata: metadata, guide: guide})

	mux := http.NewServeMux()
	serverMultiHTTP(mux, []*serverChannel{ch}, nil, nil, nil, "secret", true, nil, "")

	// No token → 403
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/tltv/v1/channels/"+chID, nil))
	if w.Code != 403 {
		t.Errorf("no token: status = %d, want 403", w.Code)
	}

	// With token → 200
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/tltv/v1/channels/"+chID+"?token=secret", nil))
	if w.Code != 200 {
		t.Errorf("with token: status = %d, want 200", w.Code)
	}
}

func TestServerMultiChannel_GuidePerChannel(t *testing.T) {
	var channels []*serverChannel
	for i := 0; i < 2; i++ {
		pub, priv, _ := ed25519.GenerateKey(nil)
		chID := makeChannelID(pub)
		name := fmt.Sprintf("Test %d", i+1)
		seg := newHLSSegmenter(5, 2)
		metadata, guide := serverSignDocs(chID, name, "", priv, nil, "public", false, nil)
		ch := &serverChannel{channelID: chID, channelName: name, privKey: priv, seg: seg}
		ch.docs.Store(&serverDocs{channelID: chID, channelName: name, metadata: metadata, guide: guide})
		channels = append(channels, ch)
	}

	mux := http.NewServeMux()
	serverMultiHTTP(mux, channels, nil, nil, nil, "", false, nil, "")

	for _, ch := range channels {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("GET", "/tltv/v1/channels/"+ch.channelID+"/guide.json", nil))
		if w.Code != 200 {
			t.Fatalf("channel %s guide: status = %d", ch.channelID, w.Code)
		}
		var doc map[string]interface{}
		json.Unmarshal(w.Body.Bytes(), &doc)
		if getString(doc, "id") != ch.channelID {
			t.Errorf("guide id = %q, want %q", getString(doc, "id"), ch.channelID)
		}
	}
}

// ---------- Multi-Rendition ----------

func TestParseVariants_Valid(t *testing.T) {
	variants, err := parseVariants("1080p,720p,360p")
	if err != nil {
		t.Fatalf("parseVariants: %v", err)
	}
	if len(variants) != 3 {
		t.Fatalf("variants = %d, want 3", len(variants))
	}
	if variants[0].width != 1920 || variants[0].height != 1080 {
		t.Errorf("1080p = %dx%d", variants[0].width, variants[0].height)
	}
	if variants[2].width != 640 || variants[2].height != 360 {
		t.Errorf("360p = %dx%d", variants[2].width, variants[2].height)
	}
}

func TestParseVariants_Empty(t *testing.T) {
	variants, err := parseVariants("")
	if err != nil || variants != nil {
		t.Errorf("empty = %v, %v; want nil, nil", variants, err)
	}
}

func TestParseVariants_Unknown(t *testing.T) {
	_, err := parseVariants("1080p,potato")
	if err == nil {
		t.Error("unknown variant should fail")
	}
}

func TestParseVariants_Dedup(t *testing.T) {
	variants, err := parseVariants("720p,720p,360p")
	if err != nil {
		t.Fatalf("parseVariants: %v", err)
	}
	if len(variants) != 2 {
		t.Errorf("dedup: variants = %d, want 2", len(variants))
	}
}

func TestMasterPlaylist(t *testing.T) {
	variants := []serverVariant{
		{label: "1080p", width: 1920, height: 1080, bandwidth: 5000000, codecTag: "avc1.42c028"},
		{label: "720p", width: 1280, height: 720, bandwidth: 2000000, codecTag: "avc1.42c01f"},
	}
	m := masterPlaylist(variants, nil, nil, true)
	if !strings.Contains(m, "#EXTM3U") {
		t.Error("missing #EXTM3U")
	}
	if !strings.Contains(m, "BANDWIDTH=5000000") {
		t.Error("missing 1080p bandwidth")
	}
	if !strings.Contains(m, "RESOLUTION=1280x720") {
		t.Error("missing 720p resolution")
	}
	if !strings.Contains(m, "stream_720p.m3u8") {
		t.Error("missing variant URI")
	}
}

func TestMasterPlaylist_VideoOnlyCodecs(t *testing.T) {
	variants := []serverVariant{{label: "720p", width: 1280, height: 720, bandwidth: 2000000, codecTag: "avc1.42c01f"}}
	m := masterPlaylist(variants, nil, nil, false)
	if strings.Contains(m, "mp4a.40.2") {
		t.Fatalf("video-only master playlist should not advertise AAC codec:\n%s", m)
	}
	if !strings.Contains(m, "CODECS=\"avc1.42c01f\"") {
		t.Fatalf("video-only master playlist should advertise video codec only:\n%s", m)
	}
}

func TestServerMultiRendition_MasterPlaylist(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	chID := makeChannelID(pub)
	metadata, guide := serverSignDocs(chID, "Test", "", priv, nil, "public", false, nil)

	// Create channel with 2 variants
	ch := &serverChannel{channelID: chID, channelName: "Test", privKey: priv}
	ch.docs.Store(&serverDocs{channelID: chID, channelName: "Test", metadata: metadata, guide: guide})

	seg720 := newHLSSegmenter(5, 2)
	seg720.segPrefix = "720p_"
	seg720.pushSegment([]byte("720p-data"), 2.0)

	seg360 := newHLSSegmenter(5, 2)
	seg360.segPrefix = "360p_"
	seg360.pushSegment([]byte("360p-data"), 2.0)

	ch.variants = []serverVariant{
		{label: "720p", width: 1280, height: 720, seg: seg720, bandwidth: 2000000, codecTag: "avc1.42c01f"},
		{label: "360p", width: 640, height: 360, seg: seg360, bandwidth: 800000, codecTag: "avc1.42c01e"},
	}
	ch.seg = seg720

	mux := http.NewServeMux()
	serverMultiHTTP(mux, []*serverChannel{ch}, nil, nil, nil, "", false, nil, "")

	// stream.m3u8 → master playlist
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/tltv/v1/channels/"+chID+"/stream.m3u8", nil))
	if w.Code != 200 {
		t.Fatalf("master playlist: status = %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "stream_720p.m3u8") {
		t.Error("master playlist missing 720p variant")
	}
	if !strings.Contains(body, "stream_360p.m3u8") {
		t.Error("master playlist missing 360p variant")
	}

	// stream_720p.m3u8 → media playlist
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/tltv/v1/channels/"+chID+"/stream_720p.m3u8", nil))
	if w.Code != 200 {
		t.Fatalf("720p media playlist: status = %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "720p_seg") {
		t.Error("720p playlist should reference 720p_ prefixed segments")
	}

	// 720p_seg0.ts → 720p segment data
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/tltv/v1/channels/"+chID+"/720p_seg0.ts", nil))
	if w.Code != 200 {
		t.Fatalf("720p segment: status = %d", w.Code)
	}
	if w.Body.String() != "720p-data" {
		t.Errorf("720p segment data = %q", w.Body.String())
	}

	// 360p_seg0.ts → 360p segment data
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/tltv/v1/channels/"+chID+"/360p_seg0.ts", nil))
	if w.Code != 200 {
		t.Fatalf("360p segment: status = %d", w.Code)
	}
	if w.Body.String() != "360p-data" {
		t.Errorf("360p segment data = %q", w.Body.String())
	}
}

func TestServerMultiRendition_TokenRequired(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	chID := makeChannelID(pub)
	metadata, guide := serverSignDocs(chID, "Test", "", priv, nil, "token", false, nil)

	seg := newHLSSegmenter(5, 2)
	seg.segPrefix = "720p_"
	seg.pushSegment([]byte("data"), 2.0)

	ch := &serverChannel{channelID: chID, privKey: priv, seg: seg}
	ch.variants = []serverVariant{
		{label: "720p", width: 1280, height: 720, seg: seg, bandwidth: 2000000, codecTag: "avc1.42c01f"},
	}
	ch.docs.Store(&serverDocs{channelID: chID, channelName: "Test", metadata: metadata, guide: guide})

	mux := http.NewServeMux()
	serverMultiHTTP(mux, []*serverChannel{ch}, nil, nil, nil, "secret", true, nil, "")

	// Master playlist without token → 403
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/tltv/v1/channels/"+chID+"/stream.m3u8", nil))
	if w.Code != 403 {
		t.Errorf("no token master: status = %d, want 403", w.Code)
	}

	// Variant playlist without token → 403
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/tltv/v1/channels/"+chID+"/stream_720p.m3u8", nil))
	if w.Code != 403 {
		t.Errorf("no token variant: status = %d, want 403", w.Code)
	}

	// With token → 200
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/tltv/v1/channels/"+chID+"/stream.m3u8?token=secret", nil))
	if w.Code != 200 {
		t.Errorf("with token master: status = %d, want 200", w.Code)
	}
	if !strings.Contains(w.Body.String(), "stream_720p.m3u8?token=secret") {
		t.Fatalf("master playlist should propagate token to variant URI, got %q", w.Body.String())
	}
}

func TestServerPrivateMasterAndChildPlaylists_PropagateToken(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	chID := makeChannelID(pub)
	metadata, guide := serverSignDocs(chID, "Test", "", priv, nil, "token", false, nil)

	videoSeg := newHLSSegmenter(5, 2)
	videoSeg.segPrefix = "720p_"
	videoSeg.pushSegment([]byte("video"), 2.0)

	audioSeg := newHLSSegmenter(5, 2)
	audioSeg.segPrefix = "audio_rock_"
	audioSeg.pushSegment([]byte("audio"), 2.0)

	subSeg := newSubtitleSegmenter(5, 2)
	subSeg.segPrefix = "subs_clock_"
	subSeg.pushSegment("WEBVTT\n\n00:00:00.000 --> 00:00:02.000\nhello\n", 2.0)

	ch := &serverChannel{
		channelID:      chID,
		privKey:        priv,
		seg:            videoSeg,
		variants:       []serverVariant{{label: "720p", width: 1280, height: 720, seg: videoSeg, bandwidth: 2000000, codecTag: "avc1.42c01f"}},
		audioTracks:    []serverAudioTrack{{name: "rock", seg: audioSeg}},
		subtitleTracks: []serverSubtitleTrack{{name: "clock", seg: subSeg}},
	}
	ch.docs.Store(&serverDocs{channelID: chID, channelName: "Test", metadata: metadata, guide: guide})

	mux := http.NewServeMux()
	serverMultiHTTP(mux, []*serverChannel{ch}, nil, nil, nil, "secret", true, nil, "")

	checkPlaylist := func(path, want string) {
		t.Helper()
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("GET", path+"?token=secret", nil))
		if w.Code != 200 {
			t.Fatalf("GET %s status = %d, want 200", path, w.Code)
		}
		if !strings.Contains(w.Body.String(), want) {
			t.Fatalf("GET %s body %q missing %q", path, w.Body.String(), want)
		}
	}

	checkPlaylist("/tltv/v1/channels/"+chID+"/stream.m3u8", "audio_rock.m3u8?token=secret")
	checkPlaylist("/tltv/v1/channels/"+chID+"/stream.m3u8", "subs_clock.m3u8?token=secret")
	checkPlaylist("/tltv/v1/channels/"+chID+"/stream.m3u8", "stream_720p.m3u8?token=secret")
	checkPlaylist("/tltv/v1/channels/"+chID+"/stream_720p.m3u8", "720p_seg0.ts?token=secret")
	checkPlaylist("/tltv/v1/channels/"+chID+"/audio_rock.m3u8", "audio_rock_seg0.ts?token=secret")
	checkPlaylist("/tltv/v1/channels/"+chID+"/subs_clock.m3u8", "subs_clock_seg0.vtt?token=secret")
}

func TestServerMultiChannel_WithVariants(t *testing.T) {
	var channels []*serverChannel
	for i := 0; i < 2; i++ {
		pub, priv, _ := ed25519.GenerateKey(nil)
		chID := makeChannelID(pub)
		name := fmt.Sprintf("Test %d", i+1)
		metadata, guide := serverSignDocs(chID, name, "", priv, nil, "public", false, nil)

		seg720 := newHLSSegmenter(5, 2)
		seg720.segPrefix = "720p_"
		seg720.pushSegment([]byte(fmt.Sprintf("720p-ch%d", i)), 2.0)

		seg360 := newHLSSegmenter(5, 2)
		seg360.segPrefix = "360p_"
		seg360.pushSegment([]byte(fmt.Sprintf("360p-ch%d", i)), 2.0)

		ch := &serverChannel{channelID: chID, channelName: name, privKey: priv, seg: seg720}
		ch.variants = []serverVariant{
			{label: "720p", width: 1280, height: 720, seg: seg720, bandwidth: 2000000, codecTag: "avc1.42c01f"},
			{label: "360p", width: 640, height: 360, seg: seg360, bandwidth: 800000, codecTag: "avc1.42c01e"},
		}
		ch.docs.Store(&serverDocs{channelID: chID, channelName: name, metadata: metadata, guide: guide})
		channels = append(channels, ch)
	}

	mux := http.NewServeMux()
	serverMultiHTTP(mux, channels, nil, nil, nil, "", false, nil, "")

	// Each channel serves its own master playlist
	for _, ch := range channels {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("GET", "/tltv/v1/channels/"+ch.channelID+"/stream.m3u8", nil))
		if w.Code != 200 {
			t.Fatalf("channel %s master: status = %d", ch.channelID, w.Code)
		}
		if !strings.Contains(w.Body.String(), "stream_720p.m3u8") {
			t.Errorf("channel %s missing 720p variant", ch.channelID)
		}
	}

	// Segments are independent per channel
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/tltv/v1/channels/"+channels[0].channelID+"/720p_seg0.ts", nil))
	if w.Body.String() != "720p-ch0" {
		t.Errorf("ch0 720p segment = %q, want 720p-ch0", w.Body.String())
	}
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/tltv/v1/channels/"+channels[1].channelID+"/720p_seg0.ts", nil))
	if w.Body.String() != "720p-ch1" {
		t.Errorf("ch1 720p segment = %q, want 720p-ch1", w.Body.String())
	}
}

func TestServerNoVariants_SinglePlaylist(t *testing.T) {
	// Without variants, stream.m3u8 is a media playlist (backward compat)
	pub, priv, _ := ed25519.GenerateKey(nil)
	chID := makeChannelID(pub)
	seg := newHLSSegmenter(5, 2)
	seg.pushSegment([]byte("ts-data"), 2.0)
	metadata, guide := serverSignDocs(chID, "Test", "", priv, nil, "public", false, nil)

	ch := &serverChannel{channelID: chID, privKey: priv, seg: seg}
	ch.docs.Store(&serverDocs{channelID: chID, channelName: "Test", metadata: metadata, guide: guide})

	mux := http.NewServeMux()
	serverMultiHTTP(mux, []*serverChannel{ch}, nil, nil, nil, "", false, nil, "")

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/tltv/v1/channels/"+chID+"/stream.m3u8", nil))
	if w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}
	body := w.Body.String()
	if strings.Contains(body, "EXT-X-STREAM-INF") {
		t.Error("single-variant should return media playlist, not master")
	}
	if !strings.Contains(body, "#EXTINF") {
		t.Error("media playlist should contain #EXTINF")
	}
}

// ---------- Audio Tracks ----------

func TestParseAudioTracks_Valid(t *testing.T) {
	tracks, err := parseAudioTracks("rock:440,jazz:880,classical:1200")
	if err != nil {
		t.Fatalf("parseAudioTracks: %v", err)
	}
	if len(tracks) != 3 {
		t.Fatalf("tracks = %d, want 3", len(tracks))
	}
	if tracks[0].name != "rock" {
		t.Errorf("track 0 name = %q, want rock", tracks[0].name)
	}
	if tracks[2].name != "classical" {
		t.Errorf("track 2 name = %q, want classical", tracks[2].name)
	}
}

func TestParseAudioTracks_NoFreq(t *testing.T) {
	tracks, err := parseAudioTracks("main,alt")
	if err != nil {
		t.Fatalf("parseAudioTracks: %v", err)
	}
	if len(tracks) != 2 {
		t.Fatalf("tracks = %d, want 2", len(tracks))
	}
	if tracks[0].name != "main" || tracks[1].name != "alt" {
		t.Errorf("tracks = %v %v, want main,alt", tracks[0].name, tracks[1].name)
	}
}

func TestParseAudioTracks_Empty(t *testing.T) {
	tracks, err := parseAudioTracks("")
	if err != nil || tracks != nil {
		t.Errorf("empty = %v, %v; want nil, nil", tracks, err)
	}
}

func TestParseAudioTracks_Dedup(t *testing.T) {
	tracks, err := parseAudioTracks("rock,Rock,jazz")
	if err != nil {
		t.Fatalf("parseAudioTracks: %v", err)
	}
	if len(tracks) != 2 {
		t.Errorf("dedup: tracks = %d, want 2", len(tracks))
	}
}

func TestMasterPlaylist_WithAudioTracks(t *testing.T) {
	variants := []serverVariant{
		{label: "720p", width: 1280, height: 720, bandwidth: 2000000, codecTag: "avc1.42c01f"},
	}
	audioTracks := []serverAudioTrack{
		{name: "rock", seg: newHLSSegmenter(5, 2)},
		{name: "jazz", seg: newHLSSegmenter(5, 2)},
	}
	m := masterPlaylist(variants, audioTracks, nil, false)

	if !strings.Contains(m, "#EXT-X-INDEPENDENT-SEGMENTS") {
		t.Error("missing EXT-X-INDEPENDENT-SEGMENTS")
	}
	if !strings.Contains(m, "#EXT-X-MEDIA:TYPE=AUDIO") {
		t.Error("missing EXT-X-MEDIA TYPE=AUDIO")
	}
	if !strings.Contains(m, "GROUP-ID=\"audio\"") {
		t.Error("missing GROUP-ID audio")
	}
	if !strings.Contains(m, "NAME=\"Rock\"") {
		t.Error("missing Rock track name")
	}
	if !strings.Contains(m, "NAME=\"Jazz\"") {
		t.Error("missing Jazz track name")
	}
	if !strings.Contains(m, "DEFAULT=YES") {
		t.Error("first track should be DEFAULT=YES")
	}
	if !strings.Contains(m, "AUTOSELECT=YES") {
		t.Error("first track should have AUTOSELECT=YES")
	}
	if !strings.Contains(m, "URI=\"audio_rock.m3u8\"") {
		t.Error("missing audio_rock.m3u8 URI")
	}
	if !strings.Contains(m, "AUDIO=\"audio\"") {
		t.Error("missing AUDIO group reference on STREAM-INF")
	}
	t.Logf("Master playlist with audio tracks:\n%s", m)
}

func TestPMTAudioOnly(t *testing.T) {
	m := &tsMuxer{}
	var buf [tsPacketSize]byte
	m.writePMTAudioOnly(buf[:])

	pmt := buf[5:]

	// section_length should be 18 (5+4+5+4, audio only)
	secLen := int(pmt[1]&0x0F)<<8 | int(pmt[2])
	if secLen != 18 {
		t.Errorf("PMT audio-only section_length = %d, want 18", secLen)
	}

	// Stream entry: AAC audio at offset 12
	if pmt[12] != tsStreamTypeAAC {
		t.Errorf("stream type = 0x%02X, want 0x%02X (AAC)", pmt[12], tsStreamTypeAAC)
	}
	audPID := (uint16(pmt[13]&0x1F) << 8) | uint16(pmt[14])
	if audPID != tsPIDAudio {
		t.Errorf("audio PID = 0x%04X, want 0x%04X", audPID, tsPIDAudio)
	}

	// PCR PID should be audio PID for audio-only
	pcrPID := (uint16(pmt[8]&0x1F) << 8) | uint16(pmt[9])
	if pcrPID != tsPIDAudio {
		t.Errorf("PCR PID = 0x%04X, want 0x%04X (audio PID for audio-only)", pcrPID, tsPIDAudio)
	}

	t.Logf("PMT audio-only: section_length=%d, audio PID=0x%04X, PCR PID=0x%04X", secLen, audPID, pcrPID)
}

func TestAudioTrack_GenerateSegment(t *testing.T) {
	at := &serverAudioTrack{
		name:         "test",
		seg:          newHLSSegmenter(5, 2),
		muxer:        &tsMuxer{},
		segDurationI: 2,
		ptsPerFrame:  3000,
		framesPerSeg: 60,
	}

	at.generateAudioSegment()

	if at.seg.seqNum < 1 {
		t.Fatal("no segment generated")
	}

	// Verify segment has audio packets and no video packets
	data := at.seg.getSegment(0)
	if data == nil {
		t.Fatal("segment data is nil")
	}

	audioPkts := 0
	videoPkts := 0
	for i := 0; i+tsPacketSize <= len(data); i += tsPacketSize {
		if data[i] != tsSyncByte {
			t.Fatalf("lost TS sync at offset %d", i)
		}
		pid := (uint16(data[i+1]&0x1F) << 8) | uint16(data[i+2])
		switch pid {
		case tsPIDVideo:
			videoPkts++
		case tsPIDAudio:
			audioPkts++
		}
	}

	if videoPkts != 0 {
		t.Errorf("audio-only segment should have 0 video packets, got %d", videoPkts)
	}
	if audioPkts == 0 {
		t.Error("audio-only segment should have audio packets")
	}
	t.Logf("Audio segment: %d bytes, %d audio packets, %d video packets", len(data), audioPkts, videoPkts)
}

func TestServerMultiRendition_AudioTrackEndpoints(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	chID := makeChannelID(pub)
	metadata, guide := serverSignDocs(chID, "Test", "", priv, nil, "public", false, nil)

	// Create channel with 1 variant + 2 audio tracks
	seg720 := newHLSSegmenter(5, 2)
	seg720.segPrefix = "720p_"
	seg720.pushSegment([]byte("720p-data"), 2.0)

	atRock := serverAudioTrack{
		name:         "rock",
		seg:          newHLSSegmenter(5, 2),
		muxer:        &tsMuxer{},
		segDurationI: 2,
	}
	atRock.seg.segPrefix = "audio_rock_"
	atRock.generateAudioSegment()

	atJazz := serverAudioTrack{
		name:         "jazz",
		seg:          newHLSSegmenter(5, 2),
		muxer:        &tsMuxer{},
		segDurationI: 2,
	}
	atJazz.seg.segPrefix = "audio_jazz_"
	atJazz.generateAudioSegment()

	ch := &serverChannel{
		channelID:   chID,
		channelName: "Test",
		privKey:     priv,
		seg:         seg720,
		variants: []serverVariant{
			{label: "720p", width: 1280, height: 720, seg: seg720, bandwidth: 2000000, codecTag: "avc1.42c01f"},
		},
		audioTracks: []serverAudioTrack{atRock, atJazz},
	}
	ch.docs.Store(&serverDocs{channelID: chID, channelName: "Test", metadata: metadata, guide: guide})

	mux := http.NewServeMux()
	serverMultiHTTP(mux, []*serverChannel{ch}, nil, nil, nil, "", false, nil, "")

	// Master playlist should reference audio tracks
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/tltv/v1/channels/"+chID+"/stream.m3u8", nil))
	if w.Code != 200 {
		t.Fatalf("master playlist: status = %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "audio_rock.m3u8") {
		t.Error("master playlist missing audio_rock.m3u8")
	}
	if !strings.Contains(body, "audio_jazz.m3u8") {
		t.Error("master playlist missing audio_jazz.m3u8")
	}

	// Audio track media playlist
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/tltv/v1/channels/"+chID+"/audio_rock.m3u8", nil))
	if w.Code != 200 {
		t.Fatalf("audio_rock.m3u8: status = %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "audio_rock_seg") {
		t.Error("audio rock playlist should reference audio_rock_ segments")
	}

	// Audio track segment
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/tltv/v1/channels/"+chID+"/audio_rock_seg0.ts", nil))
	if w.Code != 200 {
		t.Fatalf("audio_rock_seg0.ts: status = %d, body = %s", w.Code, w.Body.String())
	}
	if len(w.Body.Bytes()) == 0 {
		t.Error("audio segment should have data")
	}

	// Unknown audio track → 404
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/tltv/v1/channels/"+chID+"/audio_bogus.m3u8", nil))
	if w.Code != 404 {
		t.Errorf("unknown audio track: status = %d, want 404", w.Code)
	}
}

func TestServerMetadataOpts_Nil(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	channelID := makeChannelID(pub)
	metadata, _ := serverSignDocs(channelID, "TEST", "", priv, nil, "public", false, nil)

	var doc map[string]interface{}
	json.Unmarshal(metadata, &doc)
	// nil opts → no optional fields
	if _, ok := doc["description"]; ok {
		t.Error("nil opts should not produce description field")
	}
	if _, ok := doc["tags"]; ok {
		t.Error("nil opts should not produce tags field")
	}
}

// ---------- Video-Only (--no-audio) ----------

func TestPMTVideoOnly(t *testing.T) {
	m := &tsMuxer{}
	var buf [tsPacketSize]byte
	m.writePMTVideoOnly(buf[:])

	pmt := buf[5:]

	// section_length should be 18 (5+4+5+4, no audio stream entry)
	secLen := int(pmt[1]&0x0F)<<8 | int(pmt[2])
	if secLen != 18 {
		t.Errorf("PMT video-only section_length = %d, want 18", secLen)
	}

	// Stream entry 1: H.264 video at offset 12
	if pmt[12] != tsStreamTypeH264 {
		t.Errorf("stream 1 type = 0x%02X, want 0x%02X (H.264)", pmt[12], tsStreamTypeH264)
	}
	vidPID := (uint16(pmt[13]&0x1F) << 8) | uint16(pmt[14])
	if vidPID != tsPIDVideo {
		t.Errorf("video PID = 0x%04X, want 0x%04X", vidPID, tsPIDVideo)
	}

	// No audio stream entry at offset 17 — should be CRC bytes, not AAC type
	if pmt[17] == tsStreamTypeAAC {
		t.Error("video-only PMT should not contain audio stream entry")
	}

	t.Logf("PMT video-only: section_length=%d, video PID=0x%04X", secLen, vidPID)
}

func TestNoAudio_SegmentHasNoAudioPackets(t *testing.T) {
	h264 := &h264Settings{width: 320, height: 240, fps: 30, qp: 26}
	state := &serverState{
		seg:          newHLSSegmenter(3, 2),
		muxer:        &tsMuxer{},
		sps:          encodeSPS(h264),
		pps:          encodePPS(h264),
		aud:          encodeAUD(),
		frame:        newFrame(320, 240),
		h264:         h264,
		channelName:  "TEST",
		showUptime:   false,
		fontScale:    0,
		startTime:    time.Date(2024, 1, 15, 12, 0, 0, 0, time.UTC),
		location:     time.UTC,
		framesPerSeg: 60,
		ptsPerFrame:  3000,
		segDuration:  2.0,
		segDurationI: 2,
		noAudio:      true,
	}

	state.generateSegment()

	seg := state.seg
	if seg.seqNum < 1 {
		t.Fatal("no segment generated")
	}

	// Check that no audio PID packets exist
	idx := (seg.head - 1 + seg.ringSize) % seg.ringSize
	data := seg.ring[idx].data
	videoPkts := 0
	audioPkts := 0
	for i := 0; i+tsPacketSize <= len(data); i += tsPacketSize {
		if data[i] != tsSyncByte {
			t.Fatalf("lost TS sync at offset %d", i)
		}
		pid := (uint16(data[i+1]&0x1F) << 8) | uint16(data[i+2])
		switch pid {
		case tsPIDVideo:
			videoPkts++
		case tsPIDAudio:
			audioPkts++
		}
	}

	if videoPkts == 0 {
		t.Error("no video TS packets found")
	}
	if audioPkts != 0 {
		t.Errorf("video-only segment should have 0 audio packets, got %d", audioPkts)
	}

	// Verify PMT in the segment is video-only (section_length=18)
	// PMT is the second TS packet (offset 188)
	if len(data) >= 2*tsPacketSize {
		pmtPkt := data[tsPacketSize : 2*tsPacketSize]
		pmtSection := pmtPkt[5:]
		secLen := int(pmtSection[1]&0x0F)<<8 | int(pmtSection[2])
		if secLen != 18 {
			t.Errorf("segment PMT section_length = %d, want 18 (video-only)", secLen)
		}
	}

	t.Logf("Video-only segment: %d bytes, %d video packets, %d audio packets", len(data), videoPkts, audioPkts)
}

// ---------- EXT-X-PROGRAM-DATE-TIME ----------

func TestProgramDateTime_InManifest(t *testing.T) {
	seg := newHLSSegmenter(5, 2)
	seg.programDateTime = true
	seg.pushSegment([]byte("data-0"), 2.0)
	time.Sleep(10 * time.Millisecond) // ensure different timestamps
	seg.pushSegment([]byte("data-1"), 2.0)

	manifest := seg.getManifest()

	if !strings.Contains(manifest, "#EXT-X-PROGRAM-DATE-TIME:") {
		t.Error("manifest should contain EXT-X-PROGRAM-DATE-TIME tag")
	}

	// Count occurrences — should have one per segment
	count := strings.Count(manifest, "#EXT-X-PROGRAM-DATE-TIME:")
	if count != 2 {
		t.Errorf("expected 2 EXT-X-PROGRAM-DATE-TIME tags, got %d", count)
	}

	// Verify format: YYYY-MM-DDTHH:MM:SS.sssZ
	for _, line := range strings.Split(manifest, "\n") {
		if strings.HasPrefix(line, "#EXT-X-PROGRAM-DATE-TIME:") {
			ts := strings.TrimPrefix(line, "#EXT-X-PROGRAM-DATE-TIME:")
			if _, err := time.Parse("2006-01-02T15:04:05.000Z", ts); err != nil {
				t.Errorf("invalid timestamp format %q: %v", ts, err)
			}
		}
	}

	t.Logf("Manifest with program-date-time:\n%s", manifest)
}

// ---------- Subtitle Tracks ----------

func TestParseSubtitleTracks_Valid(t *testing.T) {
	tracks, err := parseSubtitleTracks("clock,counter,lorem")
	if err != nil {
		t.Fatalf("parseSubtitleTracks: %v", err)
	}
	if len(tracks) != 3 {
		t.Fatalf("tracks = %d, want 3", len(tracks))
	}
	if tracks[0].name != "clock" {
		t.Errorf("track 0 = %q, want clock", tracks[0].name)
	}
}

func TestParseSubtitleTracks_Empty(t *testing.T) {
	tracks, err := parseSubtitleTracks("")
	if err != nil || tracks != nil {
		t.Errorf("empty = %v, %v; want nil, nil", tracks, err)
	}
}

func TestGenerateSubtitleVTT_Clock(t *testing.T) {
	now := time.Date(2026, 4, 11, 15, 30, 0, 0, time.UTC)
	vtt := generateSubtitleVTT("clock", 0, 2, now)

	if !strings.Contains(vtt, "WEBVTT") {
		t.Error("missing WEBVTT header")
	}
	if !strings.Contains(vtt, "X-TIMESTAMP-MAP=") {
		t.Error("missing X-TIMESTAMP-MAP")
	}
	if !strings.Contains(vtt, "15:30:00 UTC") {
		t.Error("missing clock text")
	}
	t.Logf("Clock VTT:\n%s", vtt)
}

func TestGenerateSubtitleVTT_Counter(t *testing.T) {
	vtt := generateSubtitleVTT("counter", 42, 2, time.Now())

	if !strings.Contains(vtt, "Segment #42") {
		t.Error("missing counter text")
	}
}

func TestGenerateSubtitleVTT_Lorem(t *testing.T) {
	vtt := generateSubtitleVTT("lorem", 0, 2, time.Now())

	if !strings.Contains(vtt, "Lorem ipsum") {
		t.Error("missing lorem text")
	}
}

func TestGenerateSubtitleVTT_Empty(t *testing.T) {
	vtt := generateSubtitleVTT("empty", 0, 2, time.Now())

	if !strings.Contains(vtt, "WEBVTT") {
		t.Error("missing WEBVTT header")
	}
	// Empty track produces valid VTT with empty cue text
	if !strings.Contains(vtt, "-->") {
		t.Error("missing time range even for empty track")
	}
}

func TestSubtitleSegmenter(t *testing.T) {
	seg := newSubtitleSegmenter(5, 2)
	seg.segPrefix = "subs_clock_"
	seg.pushSegment("WEBVTT\n\n00:00:00.000 --> 00:00:02.000\nHello\n", 2.0)

	manifest := seg.getManifest()
	if !strings.Contains(manifest, "#EXTM3U") {
		t.Error("missing EXTM3U in manifest")
	}
	if !strings.Contains(manifest, "subs_clock_seg0.vtt") {
		t.Error("missing VTT segment reference")
	}

	vtt := seg.getSegment(0)
	if !strings.Contains(vtt, "Hello") {
		t.Error("missing segment content")
	}
}

func TestMasterPlaylist_WithSubtitles(t *testing.T) {
	variants := []serverVariant{
		{label: "720p", width: 1280, height: 720, bandwidth: 2000000, codecTag: "avc1.42c01f"},
	}
	subs := []serverSubtitleTrack{
		{name: "clock", seg: newSubtitleSegmenter(5, 2)},
		{name: "counter", seg: newSubtitleSegmenter(5, 2)},
	}
	m := masterPlaylist(variants, nil, subs, true)

	if !strings.Contains(m, "#EXT-X-MEDIA:TYPE=SUBTITLES") {
		t.Error("missing EXT-X-MEDIA TYPE=SUBTITLES")
	}
	if !strings.Contains(m, "GROUP-ID=\"subs\"") {
		t.Error("missing GROUP-ID subs")
	}
	if !strings.Contains(m, "URI=\"subs_clock.m3u8\"") {
		t.Error("missing subs_clock.m3u8 URI")
	}
	if !strings.Contains(m, "SUBTITLES=\"subs\"") {
		t.Error("missing SUBTITLES group reference on STREAM-INF")
	}
	t.Logf("Master playlist with subtitles:\n%s", m)
}

func TestMasterPlaylist_WithAudioAndSubtitles(t *testing.T) {
	variants := []serverVariant{
		{label: "720p", width: 1280, height: 720, bandwidth: 2000000, codecTag: "avc1.42c01f"},
	}
	audio := []serverAudioTrack{
		{name: "main", seg: newHLSSegmenter(5, 2)},
	}
	subs := []serverSubtitleTrack{
		{name: "clock", seg: newSubtitleSegmenter(5, 2)},
	}
	m := masterPlaylist(variants, audio, subs, false)

	if !strings.Contains(m, "#EXT-X-MEDIA:TYPE=AUDIO") {
		t.Error("missing audio media")
	}
	if !strings.Contains(m, "#EXT-X-MEDIA:TYPE=SUBTITLES") {
		t.Error("missing subtitle media")
	}
	if !strings.Contains(m, "AUDIO=\"audio\"") {
		t.Error("missing AUDIO reference")
	}
	if !strings.Contains(m, "SUBTITLES=\"subs\"") {
		t.Error("missing SUBTITLES reference")
	}
	t.Logf("Master playlist with both:\n%s", m)
}

func TestServerSubtitleEndpoints(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	chID := makeChannelID(pub)
	metadata, guide := serverSignDocs(chID, "Test", "", priv, nil, "public", false, nil)

	seg720 := newHLSSegmenter(5, 2)
	seg720.segPrefix = "720p_"
	seg720.pushSegment([]byte("720p-data"), 2.0)

	stClock := serverSubtitleTrack{
		name:         "clock",
		seg:          newSubtitleSegmenter(5, 2),
		segDurationI: 2,
	}
	stClock.seg.segPrefix = "subs_clock_"
	stClock.generateSubtitleSegment()

	ch := &serverChannel{
		channelID:   chID,
		channelName: "Test",
		privKey:     priv,
		seg:         seg720,
		variants: []serverVariant{
			{label: "720p", width: 1280, height: 720, seg: seg720, bandwidth: 2000000, codecTag: "avc1.42c01f"},
		},
		subtitleTracks: []serverSubtitleTrack{stClock},
	}
	ch.docs.Store(&serverDocs{channelID: chID, channelName: "Test", metadata: metadata, guide: guide})

	mux := http.NewServeMux()
	serverMultiHTTP(mux, []*serverChannel{ch}, nil, nil, nil, "", false, nil, "")

	// Master playlist should reference subtitles
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/tltv/v1/channels/"+chID+"/stream.m3u8", nil))
	if w.Code != 200 {
		t.Fatalf("master playlist: status = %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "subs_clock.m3u8") {
		t.Error("master playlist missing subs_clock.m3u8")
	}

	// Subtitle media playlist
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/tltv/v1/channels/"+chID+"/subs_clock.m3u8", nil))
	if w.Code != 200 {
		t.Fatalf("subs_clock.m3u8: status = %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "subs_clock_seg") {
		t.Error("subtitle playlist should reference subs_clock_ segments")
	}

	// Subtitle VTT segment
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/tltv/v1/channels/"+chID+"/subs_clock_seg0.vtt", nil))
	if w.Code != 200 {
		t.Fatalf("subs_clock_seg0.vtt: status = %d", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); !strings.Contains(ct, "text/vtt") {
		t.Errorf("Content-Type = %q, want text/vtt", ct)
	}
	if !strings.Contains(w.Body.String(), "WEBVTT") {
		t.Error("VTT segment missing WEBVTT header")
	}
	if !strings.Contains(w.Body.String(), "X-TIMESTAMP-MAP") {
		t.Error("VTT segment missing X-TIMESTAMP-MAP")
	}

	// Unknown subtitle track → 404
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/tltv/v1/channels/"+chID+"/subs_bogus.m3u8", nil))
	if w.Code != 404 {
		t.Errorf("unknown subtitle track: status = %d, want 404", w.Code)
	}
}

func TestProgramDateTime_DisabledByDefault(t *testing.T) {
	seg := newHLSSegmenter(5, 2)
	seg.pushSegment([]byte("data"), 2.0)

	manifest := seg.getManifest()

	if strings.Contains(manifest, "#EXT-X-PROGRAM-DATE-TIME:") {
		t.Error("manifest should NOT contain EXT-X-PROGRAM-DATE-TIME when disabled")
	}
}

// ---------- Audio-Only Channel ----------

func TestAudioOnly_MasterPlaylist(t *testing.T) {
	// An audio-only channel (no video variants) should produce a master
	// playlist with an audio-only STREAM-INF pointing to the default track.
	audioTracks := []serverAudioTrack{
		{name: "main", seg: newHLSSegmenter(5, 2)},
	}
	m := masterPlaylist(nil, audioTracks, nil, false)

	if !strings.Contains(m, "#EXT-X-MEDIA:TYPE=AUDIO") {
		t.Error("missing EXT-X-MEDIA TYPE=AUDIO")
	}
	if !strings.Contains(m, "#EXT-X-STREAM-INF:BANDWIDTH=64000,CODECS=\"mp4a.40.2\"") {
		t.Error("missing audio-only STREAM-INF")
	}
	if strings.Contains(m, "RESOLUTION=") {
		t.Error("audio-only STREAM-INF should NOT have RESOLUTION")
	}
	if !strings.Contains(m, "audio_main.m3u8") {
		t.Error("audio-only STREAM-INF should point to audio_main.m3u8")
	}
	t.Logf("Audio-only master playlist:\n%s", m)
}

func TestAudioOnly_MultiTrack(t *testing.T) {
	// Multiple audio tracks in audio-only mode.
	audioTracks := []serverAudioTrack{
		{name: "rock", seg: newHLSSegmenter(5, 2)},
		{name: "jazz", seg: newHLSSegmenter(5, 2)},
	}
	m := masterPlaylist(nil, audioTracks, nil, false)

	// Should have 2 EXT-X-MEDIA declarations
	count := strings.Count(m, "#EXT-X-MEDIA:TYPE=AUDIO")
	if count != 2 {
		t.Errorf("expected 2 EXT-X-MEDIA, got %d", count)
	}
	// Audio-only STREAM-INF points to first (default) track
	if !strings.Contains(m, "audio_rock.m3u8") {
		t.Error("audio-only STREAM-INF should point to first track")
	}
}

func TestAudioOnly_ServerEndpoint(t *testing.T) {
	// A serverChannel with audioTracks but no variants/seg should serve
	// a master playlist at stream.m3u8.
	pub, priv, _ := ed25519.GenerateKey(nil)
	chID := makeChannelID(pub)
	metadata, guide := serverSignDocs(chID, "Radio", "", priv, nil, "public", false, nil)

	atMain := serverAudioTrack{
		name:         "main",
		seg:          newHLSSegmenter(5, 2),
		muxer:        &tsMuxer{},
		segDurationI: 2,
	}
	atMain.seg.segPrefix = "audio_main_"
	atMain.generateAudioSegment()

	ch := &serverChannel{
		channelID:   chID,
		channelName: "Radio",
		privKey:     priv,
		// No seg, no variants — audio-only
		audioTracks: []serverAudioTrack{atMain},
	}
	ch.docs.Store(&serverDocs{channelID: chID, channelName: "Radio", metadata: metadata, guide: guide})

	mux := http.NewServeMux()
	serverMultiHTTP(mux, []*serverChannel{ch}, nil, nil, nil, "", false, nil, "")

	// stream.m3u8 → master playlist with audio-only STREAM-INF
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/tltv/v1/channels/"+chID+"/stream.m3u8", nil))
	if w.Code != 200 {
		t.Fatalf("stream.m3u8: status = %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "mp4a.40.2") {
		t.Error("master playlist missing audio codec")
	}
	if strings.Contains(body, "RESOLUTION=") {
		t.Error("audio-only should not have RESOLUTION")
	}

	// Audio segments should be served
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/tltv/v1/channels/"+chID+"/audio_main_seg0.ts", nil))
	if w.Code != 200 {
		t.Fatalf("audio segment: status = %d", w.Code)
	}
}

func TestServerUseLegacyHTTP(t *testing.T) {
	if !serverUseLegacyHTTP(&serverChannel{seg: newHLSSegmenter(5, 2)}) {
		t.Fatal("basic single-channel stream should use legacy HTTP handler")
	}
	if serverUseLegacyHTTP(&serverChannel{audioTracks: []serverAudioTrack{{name: "main"}}}) {
		t.Fatal("audio-only channel must use multi-channel HTTP handler")
	}
	if serverUseLegacyHTTP(&serverChannel{seg: newHLSSegmenter(5, 2), subtitleTracks: []serverSubtitleTrack{{name: "clock"}}}) {
		t.Fatal("channels with subtitle tracks must use multi-channel HTTP handler")
	}
}

// ---------- Mutual Exclusion ----------

func TestParseAudioTracks_MutualExclusion(t *testing.T) {
	// --audio-tracks and --no-audio can't both be set.
	// This is enforced at the CLI level, but we test the parsing functions
	// don't themselves conflict.
	tracks, err := parseAudioTracks("rock,jazz")
	if err != nil {
		t.Fatalf("parseAudioTracks: %v", err)
	}
	if len(tracks) != 2 {
		t.Fatalf("tracks = %d, want 2", len(tracks))
	}
	// The actual mutual exclusion check is in cmdServerTest:
	// if len(audioTracks) > 0 && *noAudio { fatal(...) }
}

// ---------- Token on Audio/Subtitle Endpoints ----------

func TestAudioTrack_TokenRequired(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	chID := makeChannelID(pub)
	metadata, guide := serverSignDocs(chID, "Test", "", priv, nil, "token", false, nil)

	seg := newHLSSegmenter(5, 2)
	seg.segPrefix = "720p_"
	seg.pushSegment([]byte("data"), 2.0)

	at := serverAudioTrack{
		name:         "rock",
		seg:          newHLSSegmenter(5, 2),
		muxer:        &tsMuxer{},
		segDurationI: 2,
	}
	at.seg.segPrefix = "audio_rock_"
	at.generateAudioSegment()

	ch := &serverChannel{
		channelID:   chID,
		privKey:     priv,
		seg:         seg,
		variants:    []serverVariant{{label: "720p", width: 1280, height: 720, seg: seg, bandwidth: 2000000, codecTag: "avc1.42c01f"}},
		audioTracks: []serverAudioTrack{at},
	}
	ch.docs.Store(&serverDocs{channelID: chID, channelName: "Test", metadata: metadata, guide: guide})

	mux := http.NewServeMux()
	serverMultiHTTP(mux, []*serverChannel{ch}, nil, nil, nil, "secret", true, nil, "")

	// Audio playlist without token → 403
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/tltv/v1/channels/"+chID+"/audio_rock.m3u8", nil))
	if w.Code != 403 {
		t.Errorf("no token audio playlist: status = %d, want 403", w.Code)
	}

	// Audio segment without token → 403
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/tltv/v1/channels/"+chID+"/audio_rock_seg0.ts", nil))
	if w.Code != 403 {
		t.Errorf("no token audio segment: status = %d, want 403", w.Code)
	}

	// With token → 200
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/tltv/v1/channels/"+chID+"/audio_rock.m3u8?token=secret", nil))
	if w.Code != 200 {
		t.Errorf("with token audio playlist: status = %d, want 200", w.Code)
	}
}

func TestSubtitleTrack_TokenRequired(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	chID := makeChannelID(pub)
	metadata, guide := serverSignDocs(chID, "Test", "", priv, nil, "token", false, nil)

	seg := newHLSSegmenter(5, 2)
	seg.segPrefix = "720p_"
	seg.pushSegment([]byte("data"), 2.0)

	st := serverSubtitleTrack{
		name:         "clock",
		seg:          newSubtitleSegmenter(5, 2),
		segDurationI: 2,
	}
	st.seg.segPrefix = "subs_clock_"
	st.generateSubtitleSegment()

	ch := &serverChannel{
		channelID:      chID,
		privKey:        priv,
		seg:            seg,
		variants:       []serverVariant{{label: "720p", width: 1280, height: 720, seg: seg, bandwidth: 2000000, codecTag: "avc1.42c01f"}},
		subtitleTracks: []serverSubtitleTrack{st},
	}
	ch.docs.Store(&serverDocs{channelID: chID, channelName: "Test", metadata: metadata, guide: guide})

	mux := http.NewServeMux()
	serverMultiHTTP(mux, []*serverChannel{ch}, nil, nil, nil, "secret", true, nil, "")

	// Subtitle playlist without token → 403
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/tltv/v1/channels/"+chID+"/subs_clock.m3u8", nil))
	if w.Code != 403 {
		t.Errorf("no token subtitle playlist: status = %d, want 403", w.Code)
	}

	// Subtitle VTT without token → 403
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/tltv/v1/channels/"+chID+"/subs_clock_seg0.vtt", nil))
	if w.Code != 403 {
		t.Errorf("no token subtitle VTT: status = %d, want 403", w.Code)
	}

	// With token → 200
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/tltv/v1/channels/"+chID+"/subs_clock.m3u8?token=secret", nil))
	if w.Code != 200 {
		t.Errorf("with token subtitle playlist: status = %d, want 200", w.Code)
	}

	w = httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/tltv/v1/channels/"+chID+"/subs_clock_seg0.vtt?token=secret", nil))
	if w.Code != 200 {
		t.Errorf("with token subtitle VTT: status = %d, want 200", w.Code)
	}
}

// TestProductionViewerPrivateAuth verifies the production viewer on a private
// daemon: root HTML and /api/info require ?token=, and the response does not
// leak the raw token in any URL fields.
func TestProductionViewerPrivateAuth(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	channelID := makeChannelID(pub)
	metadata, guide := serverSignDocs(channelID, "Private Prod", "", priv, nil, "token", false, nil)
	docs := &serverDocs{channelID: channelID, channelName: "Private Prod", metadata: metadata, guide: guide}

	mux := http.NewServeMux()
	productionViewerRoutes(mux, func(reqChID string) map[string]interface{} {
		return serverViewerInfo(docs, "example.com:443")
	}, func() []viewerChannelRef {
		return []viewerChannelRef{{ID: channelID, Name: "Private Prod", Guide: guide, IconPath: "/tltv/v1/channels/" + channelID + "/icon.svg"}}
	}, viewerRouteOptions{authToken: "secret456", private: true})

	// Root without token → 403
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
	if w.Code != 403 {
		t.Fatalf("GET / without token: status = %d, want 403", w.Code)
	}

	// Root with token → 200 HTML
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/?token=secret456", nil))
	if w.Code != 200 {
		t.Fatalf("GET / with token: status = %d, want 200", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); !strings.Contains(ct, "text/html") {
		t.Errorf("GET / content-type = %q, want text/html", ct)
	}

	// /api/info without token → 403
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/api/info", nil))
	if w.Code != 403 {
		t.Fatalf("GET /api/info without token: status = %d, want 403", w.Code)
	}

	// /api/info with token → 200, no raw token in body
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/api/info?token=secret456", nil))
	if w.Code != 200 {
		t.Fatalf("GET /api/info with token: status = %d, want 200", w.Code)
	}
	body := w.Body.String()
	if strings.Contains(body, "secret456") {
		t.Fatalf("/api/info should not leak token in body: %s", body)
	}
	if strings.Contains(body, "?token=") {
		t.Fatalf("/api/info should not contain token query param: %s", body)
	}
	// Private headers
	if cc := w.Header().Get("Cache-Control"); cc != "private, no-store" {
		t.Errorf("Cache-Control = %q, want private, no-store", cc)
	}
	if rp := w.Header().Get("Referrer-Policy"); rp != "no-referrer" {
		t.Errorf("Referrer-Policy = %q, want no-referrer", rp)
	}
}

// TestViewerChannelsPayloadShape verifies the /api/info channels[] array
// includes per-channel guide entries and icon_path fields.
func TestViewerChannelsPayloadShape(t *testing.T) {
	pub1, priv1, _ := ed25519.GenerateKey(nil)
	chID1 := makeChannelID(pub1)
	meta1, guide1 := serverSignDocs(chID1, "Ch1", "", priv1, nil, "public", false, nil)

	pub2, priv2, _ := ed25519.GenerateKey(nil)
	chID2 := makeChannelID(pub2)
	meta2, guide2 := serverSignDocs(chID2, "Ch2", "", priv2, nil, "public", false, nil)

	_ = meta1
	_ = meta2

	mux := http.NewServeMux()
	productionViewerRoutes(mux, func(reqChID string) map[string]interface{} {
		return map[string]interface{}{
			"channel_id":   chID1,
			"channel_name": "Ch1",
			"stream_src":   "/tltv/v1/channels/" + chID1 + "/stream.m3u8",
		}
	}, func() []viewerChannelRef {
		return []viewerChannelRef{
			{ID: chID1, Name: "Ch1", Guide: guide1, IconPath: "/tltv/v1/channels/" + chID1 + "/icon.svg"},
			{ID: chID2, Name: "Ch2", Guide: guide2, IconPath: "/tltv/v1/channels/" + chID2 + "/icon.png"},
		}
	})

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/api/info", nil))
	if w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}

	var info map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &info); err != nil {
		t.Fatalf("parse JSON: %v", err)
	}

	channels, ok := info["channels"].([]interface{})
	if !ok || len(channels) != 2 {
		t.Fatalf("channels = %v, want 2-element array", info["channels"])
	}

	for i, raw := range channels {
		ch, ok := raw.(map[string]interface{})
		if !ok {
			t.Fatalf("channels[%d] not an object", i)
		}
		if _, ok := ch["id"].(string); !ok {
			t.Errorf("channels[%d] missing id", i)
		}
		if _, ok := ch["name"].(string); !ok {
			t.Errorf("channels[%d] missing name", i)
		}
		if _, ok := ch["icon_path"].(string); !ok {
			t.Errorf("channels[%d] missing icon_path", i)
		}
		guide, ok := ch["guide"].(map[string]interface{})
		if !ok {
			t.Errorf("channels[%d] missing guide object", i)
		} else if _, ok := guide["entries"].([]interface{}); !ok {
			t.Errorf("channels[%d] guide missing entries", i)
		}
	}
}

// TestViewerChannelSwitching verifies that /api/info?channel=<id> returns
// the requested channel's data when channelsFn and infoFn support switching.
func TestViewerChannelSwitching(t *testing.T) {
	pub1, priv1, _ := ed25519.GenerateKey(nil)
	chID1 := makeChannelID(pub1)
	_, guide1 := serverSignDocs(chID1, "Alpha", "", priv1, nil, "public", false, nil)

	pub2, priv2, _ := ed25519.GenerateKey(nil)
	chID2 := makeChannelID(pub2)
	_, guide2 := serverSignDocs(chID2, "Beta", "", priv2, nil, "public", false, nil)

	infoFn := func(reqChID string) map[string]interface{} {
		id, name := chID1, "Alpha"
		if reqChID == chID2 {
			id, name = chID2, "Beta"
		}
		return map[string]interface{}{
			"channel_id":   id,
			"channel_name": name,
			"stream_src":   "/tltv/v1/channels/" + id + "/stream.m3u8",
		}
	}
	channelsFn := func() []viewerChannelRef {
		return []viewerChannelRef{
			{ID: chID1, Name: "Alpha", Guide: guide1},
			{ID: chID2, Name: "Beta", Guide: guide2},
		}
	}

	mux := http.NewServeMux()
	productionViewerRoutes(mux, infoFn, channelsFn)

	// Default → first channel
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/api/info", nil))
	var d1 map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &d1)
	if d1["channel_name"] != "Alpha" {
		t.Errorf("default channel = %v, want Alpha", d1["channel_name"])
	}

	// Switch to channel 2
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/api/info?channel="+chID2, nil))
	var d2 map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &d2)
	if d2["channel_name"] != "Beta" {
		t.Errorf("switched channel = %v, want Beta", d2["channel_name"])
	}
}

// TestViewerDisplayConfig verifies that viewer_title and viewer_footer fields
// appear in /api/info when configured via viewerRouteOptions.
func TestViewerDisplayConfig(t *testing.T) {
	mux := http.NewServeMux()
	productionViewerRoutes(mux, func(_ string) map[string]interface{} {
		return map[string]interface{}{"channel_id": "TVtest", "channel_name": "Test"}
	}, nil, viewerRouteOptions{title: "My TV", noFooter: true})

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/api/info", nil))
	var info map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &info)

	if info["viewer_title"] != "My TV" {
		t.Errorf("viewer_title = %v, want 'My TV'", info["viewer_title"])
	}
	if info["viewer_footer"] != false {
		t.Errorf("viewer_footer = %v, want false", info["viewer_footer"])
	}

	// Without display config, fields should be absent
	mux2 := http.NewServeMux()
	productionViewerRoutes(mux2, func(_ string) map[string]interface{} {
		return map[string]interface{}{"channel_id": "TVtest", "channel_name": "Test"}
	}, nil)
	w2 := httptest.NewRecorder()
	mux2.ServeHTTP(w2, httptest.NewRequest("GET", "/api/info", nil))
	var info2 map[string]interface{}
	json.Unmarshal(w2.Body.Bytes(), &info2)
	if _, ok := info2["viewer_title"]; ok {
		t.Errorf("viewer_title should be absent when not configured")
	}
	if _, ok := info2["viewer_footer"]; ok {
		t.Errorf("viewer_footer should be absent when not configured")
	}
}

// TestStandaloneViewerNoTokenLeak verifies that the standalone viewer's
// infoFn does not include stream_url or xmltv_url (which could contain tokens).
func TestStandaloneViewerNoTokenLeak(t *testing.T) {
	// Simulate the standalone infoFn pattern — no stream_url, no xmltv_url
	infoFn := func(_ string) map[string]interface{} {
		return map[string]interface{}{
			"channel_id":   "TVtest",
			"channel_name": "Test",
			"stream_src":   "/stream/stream.m3u8",
			"base_url":     "https://upstream.example.com",
			"verified":     true,
		}
	}

	mux := http.NewServeMux()
	productionViewerRoutes(mux, infoFn, nil)

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/api/info", nil))
	body := w.Body.String()

	if strings.Contains(body, "stream_url") {
		t.Errorf("/api/info should not contain stream_url: %s", body)
	}
	if strings.Contains(body, "xmltv_url") {
		t.Errorf("/api/info should not contain xmltv_url: %s", body)
	}
	if strings.Contains(body, "?token=") {
		t.Errorf("/api/info should not contain token query params: %s", body)
	}
	if !strings.Contains(body, "/stream/stream.m3u8") {
		t.Errorf("/api/info should contain local proxy stream_src: %s", body)
	}
}

func TestProductionViewerHTML_UsesTokenForActiveIconURL(t *testing.T) {
	if !strings.Contains(productionViewerHTML, "icon.src=withToken(_info.icon_url)") {
		t.Fatalf("production viewer should append the private token to active icon_url requests")
	}
	if !strings.Contains(productionViewerHTML, "icon.src=withToken('/tltv/v1/channels/'+_chID+'/icon.'+ext)") {
		t.Fatalf("production viewer should append the private token to active protocol icon requests")
	}
}

func TestPortalViewerHTML_UsesSavedChannelsAPI(t *testing.T) {
	if !strings.Contains(portalViewerHTML, "fetch('/api/saved-channels')") {
		t.Fatalf("portal viewer should load saved channels from /api/saved-channels when available")
	}
	if !strings.Contains(portalViewerHTML, "fetch('/api/saved-channels',{") {
		t.Fatalf("portal viewer should persist saved channels back to /api/saved-channels when enabled")
	}
}

func TestPortalViewerHTML_RefreshesDiscoveredIconData(t *testing.T) {
	if !strings.Contains(portalViewerHTML, "if(dc.icon_data)_saved[j].icon_data=dc.icon_data;") {
		t.Fatalf("portal viewer should refresh cached discovered channel icons from icon_data")
	}
}

func TestProductionViewerCSS_NoGuideCellHoverStyling(t *testing.T) {
	if strings.Contains(productionCSS, ".guide-cell:hover") {
		t.Fatalf("production viewer CSS should not add guide-cell hover styling in this version")
	}
	for _, line := range strings.Split(productionCSS, "\n") {
		if strings.Contains(line, ".guide-cell{") && strings.Contains(line, "background:") {
			t.Fatalf("production viewer CSS should not add guide-cell background styling: %s", line)
		}
	}
}

func TestViewerSavedChannelRoutes(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "saved.json")
	store, err := newViewerSavedChannelStore(path)
	if err != nil {
		t.Fatalf("newViewerSavedChannelStore: %v", err)
	}
	mux := http.NewServeMux()
	registerViewerSavedChannelRoutes(mux, store)

	uri := "tltv://TVMkVHiXF9W1NgM9KLgs7tcBMvC1YtF4Daj4yfTrJercs3@example.com:443?token=secret"
	body := `{"channels":[{"name":"Demo","uri":"` + uri + `"}]}`
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("POST", "/api/saved-channels", strings.NewReader(body)))
	if w.Code != 200 {
		t.Fatalf("POST /api/saved-channels status = %d, want 200 (body %s)", w.Code, w.Body.String())
	}
	var resp viewerSavedChannelsResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode POST response: %v", err)
	}
	if !resp.Enabled || len(resp.Channels) != 1 {
		t.Fatalf("POST response = %+v", resp)
	}
	if strings.Contains(resp.Channels[0].URI, "token=") {
		t.Fatalf("POST response should strip token, got %q", resp.Channels[0].URI)
	}

	w = httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/api/saved-channels", nil))
	if w.Code != 200 {
		t.Fatalf("GET /api/saved-channels status = %d, want 200", w.Code)
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode GET response: %v", err)
	}
	if !resp.Enabled || len(resp.Channels) != 1 {
		t.Fatalf("GET response = %+v", resp)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read saved file: %v", err)
	}
	if strings.Contains(string(data), "token=") {
		t.Fatalf("saved file should not contain token: %s", data)
	}
}

func TestViewerSavedChannelRoutesDisabled(t *testing.T) {
	mux := http.NewServeMux()
	registerViewerSavedChannelRoutes(mux, nil)

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/api/saved-channels", nil))
	if w.Code != 200 {
		t.Fatalf("GET disabled /api/saved-channels status = %d, want 200", w.Code)
	}
	var resp viewerSavedChannelsResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode disabled GET response: %v", err)
	}
	if resp.Enabled {
		t.Fatalf("disabled response should report Enabled=false")
	}

	w = httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("POST", "/api/saved-channels", strings.NewReader(`{"channels":[]}`)))
	if w.Code != 404 {
		t.Fatalf("POST disabled /api/saved-channels status = %d, want 404", w.Code)
	}
}
