package main

// WebVTT subtitle segment generator for HLS EXT-X-MEDIA TYPE=SUBTITLES.
//
// Each subtitle track has its own hlsSegmenter that stores WebVTT segments
// (text, not MPEG-TS). The segmenter uses the same ring buffer and manifest
// infrastructure as video/audio segments but with .vtt content.
//
// Built-in generators produce content based on the track name:
//   "clock"   — current wall clock time
//   "counter" — segment counter / sequence number
//   "lorem"   — rotating lorem ipsum text
//   "empty"   — valid but empty cues (edge case testing)
//   other     — segment counter fallback
//
// Each WebVTT segment includes X-TIMESTAMP-MAP for subtitle-to-video
// synchronization (RFC 8216 §3.5).

import (
	"fmt"
	"strings"
	"sync"
	"time"
)

// subtitleSegmenter manages WebVTT subtitle segments with an HLS media playlist.
// Unlike hlsSegmenter which stores binary TS data, this stores text VTT segments.
type subtitleSegmenter struct {
	mu             sync.RWMutex
	ring           []subtitleSegment
	ringSize       int
	targetDuration int
	head           int
	seqNum         uint64
	count          int
	manifest       string
	segPrefix      string // e.g. "subs_clock_"
}

type subtitleSegment struct {
	seqNum   uint64
	data     string // WebVTT content
	duration float64
}

func newSubtitleSegmenter(ringSize, targetDuration int) *subtitleSegmenter {
	return &subtitleSegmenter{
		ring:           make([]subtitleSegment, ringSize),
		ringSize:       ringSize,
		targetDuration: targetDuration,
	}
}

func (s *subtitleSegmenter) pushSegment(data string, duration float64) {
	s.mu.Lock()
	defer s.mu.Unlock()

	seg := subtitleSegment{
		seqNum:   s.seqNum,
		data:     data,
		duration: duration,
	}
	s.ring[s.head] = seg
	s.head = (s.head + 1) % s.ringSize
	s.seqNum++
	if s.count < s.ringSize {
		s.count++
	}
	s.rebuildManifest()
}

func (s *subtitleSegmenter) rebuildManifest() {
	var b strings.Builder
	b.WriteString("#EXTM3U\n")
	b.WriteString("#EXT-X-VERSION:3\n")
	fmt.Fprintf(&b, "#EXT-X-TARGETDURATION:%d\n", s.targetDuration)

	firstSeq := s.seqNum - uint64(s.count)
	fmt.Fprintf(&b, "#EXT-X-MEDIA-SEQUENCE:%d\n", firstSeq)
	b.WriteString("\n")

	for i := 0; i < s.count; i++ {
		idx := (s.head - s.count + i + s.ringSize) % s.ringSize
		seg := &s.ring[idx]
		fmt.Fprintf(&b, "#EXTINF:%.6f,\n", seg.duration)
		fmt.Fprintf(&b, "%sseg%d.vtt\n", s.segPrefix, seg.seqNum)
	}

	s.manifest = b.String()
}

func (s *subtitleSegmenter) getManifest() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.manifest
}

func (s *subtitleSegmenter) getSegment(seqNum uint64) string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.count == 0 {
		return ""
	}
	firstSeq := s.seqNum - uint64(s.count)
	if seqNum < firstSeq || seqNum >= s.seqNum {
		return ""
	}
	offset := int(seqNum - firstSeq)
	idx := (s.head - s.count + offset + s.ringSize) % s.ringSize
	return s.ring[idx].data
}

// generateSubtitleVTT produces a WebVTT segment for the given track type.
// mpegtsBase is the MPEG-TS PTS base for X-TIMESTAMP-MAP (90kHz ticks).
func generateSubtitleVTT(trackName string, seqNum uint64, segDuration int, now time.Time) string {
	var sb strings.Builder

	sb.WriteString("WEBVTT\n")
	// X-TIMESTAMP-MAP synchronizes VTT times with MPEG-TS PTS.
	// LOCAL:00:00:00.000 maps to the PTS base of this segment.
	// Using segment index × duration × 90000 as the PTS base.
	mpegtsBase := seqNum * uint64(segDuration) * 90000
	fmt.Fprintf(&sb, "X-TIMESTAMP-MAP=LOCAL:00:00:00.000,MPEGTS:%d\n\n", mpegtsBase)

	// Generate cue content based on track name
	cueText := subtitleCueText(trackName, seqNum, now)

	// Single cue spanning the segment duration.
	// Position ~85% from top (10% above bottom) with center alignment.
	endSec := segDuration
	fmt.Fprintf(&sb, "00:00:00.000 --> 00:%02d:%02d.000 line:85%% position:50%% align:center\n", endSec/60, endSec%60)
	sb.WriteString(cueText)
	sb.WriteString("\n")

	return sb.String()
}

// loremPhrases are rotating text for the "lorem" subtitle track.
var loremPhrases = []string{
	"Lorem ipsum dolor sit amet",
	"Consectetur adipiscing elit",
	"Sed do eiusmod tempor incididunt",
	"Ut labore et dolore magna aliqua",
	"Ut enim ad minim veniam",
	"Quis nostrud exercitation ullamco",
	"Laboris nisi ut aliquip ex ea",
	"Duis aute irure dolor in reprehenderit",
	"In voluptate velit esse cillum dolore",
	"Eu fugiat nulla pariatur",
}

// subtitleCueText generates the text content for a subtitle cue.
func subtitleCueText(trackName string, seqNum uint64, now time.Time) string {
	switch trackName {
	case "clock":
		return now.UTC().Format("15:04:05 UTC")
	case "counter":
		return fmt.Sprintf("Segment #%d", seqNum)
	case "lorem":
		return loremPhrases[int(seqNum)%len(loremPhrases)]
	case "empty":
		return "" // valid but empty cue
	default:
		return fmt.Sprintf("Segment #%d", seqNum) // fallback
	}
}

// parseSubtitleTracks parses a comma-separated subtitle track list.
// Returns subtitle track configs with the given names.
func parseSubtitleTracks(s string) ([]serverSubtitleTrack, error) {
	if s == "" {
		return nil, nil
	}
	var tracks []serverSubtitleTrack
	seen := make(map[string]bool)
	for _, name := range strings.Split(s, ",") {
		name = strings.TrimSpace(strings.ToLower(name))
		if name == "" {
			continue
		}
		if seen[name] {
			continue
		}
		seen[name] = true
		tracks = append(tracks, serverSubtitleTrack{name: name})
	}
	if len(tracks) == 0 {
		return nil, fmt.Errorf("no valid subtitle tracks specified")
	}
	return tracks, nil
}
