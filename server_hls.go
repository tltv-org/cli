package main

import (
	"fmt"
	"strings"
	"sync"
	"time"
)

// HLS live segmenter with sliding-window playlist (RFC 8216).
// Ring buffer of MPEG-TS segments served as a live HLS stream.

// hlsSegment holds one MPEG-TS segment in memory.
type hlsSegment struct {
	seqNum    uint64
	data      []byte
	duration  float64
	startTime time.Time // wall-clock time when segment was generated (for EXT-X-PROGRAM-DATE-TIME)
}

// hlsSegmenter manages a ring buffer of HLS segments and generates m3u8 playlists.
type hlsSegmenter struct {
	mu             sync.RWMutex
	ring           []hlsSegment
	ringSize       int
	targetDuration int
	head            int    // next write position in ring
	seqNum          uint64 // next sequence number to assign
	count           int    // segments currently in ring (0 to ringSize)
	manifest        string // cached manifest string
	segPrefix       string // segment filename prefix (e.g. "720p_" for variant segments)
	programDateTime bool   // include EXT-X-PROGRAM-DATE-TIME tags in manifest
}

func newHLSSegmenter(ringSize, targetDuration int) *hlsSegmenter {
	return &hlsSegmenter{
		ring:           make([]hlsSegment, ringSize),
		ringSize:       ringSize,
		targetDuration: targetDuration,
	}
}

// pushSegment adds a new segment to the ring buffer and rebuilds the manifest.
func (s *hlsSegmenter) pushSegment(data []byte, duration float64) {
	s.mu.Lock()
	defer s.mu.Unlock()

	seg := hlsSegment{
		seqNum:    s.seqNum,
		data:      data,
		duration:  duration,
		startTime: time.Now().UTC(),
	}
	s.ring[s.head] = seg
	s.head = (s.head + 1) % s.ringSize
	s.seqNum++
	if s.count < s.ringSize {
		s.count++
	}

	s.rebuildManifest()
}

// rebuildManifest generates the m3u8 playlist from the current ring buffer.
// Must be called with s.mu held.
func (s *hlsSegmenter) rebuildManifest() {
	var b strings.Builder
	b.WriteString("#EXTM3U\n")
	b.WriteString("#EXT-X-VERSION:3\n")
	fmt.Fprintf(&b, "#EXT-X-TARGETDURATION:%d\n", s.targetDuration)

	// Media sequence is the sequence number of the first segment in the ring
	firstSeq := s.seqNum - uint64(s.count)
	fmt.Fprintf(&b, "#EXT-X-MEDIA-SEQUENCE:%d\n", firstSeq)
	b.WriteString("#EXT-X-INDEPENDENT-SEGMENTS\n")
	b.WriteString("\n")

	// List segments oldest-to-newest
	for i := 0; i < s.count; i++ {
		idx := (s.head - s.count + i + s.ringSize) % s.ringSize
		seg := &s.ring[idx]
		if s.programDateTime && !seg.startTime.IsZero() {
			fmt.Fprintf(&b, "#EXT-X-PROGRAM-DATE-TIME:%s\n", seg.startTime.Format("2006-01-02T15:04:05.000Z"))
		}
		fmt.Fprintf(&b, "#EXTINF:%.6f,\n", seg.duration)
		fmt.Fprintf(&b, "%sseg%d.ts\n", s.segPrefix, seg.seqNum)
	}

	s.manifest = b.String()
}

// getManifest returns the current m3u8 playlist string.
func (s *hlsSegmenter) getManifest() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.manifest
}

// getSegment returns the data for a segment by sequence number, or nil.
func (s *hlsSegmenter) getSegment(seqNum uint64) []byte {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.count == 0 {
		return nil
	}

	firstSeq := s.seqNum - uint64(s.count)
	if seqNum < firstSeq || seqNum >= s.seqNum {
		return nil // not in ring
	}

	// Map sequence number to ring index
	offset := int(seqNum - firstSeq)
	idx := (s.head - s.count + offset + s.ringSize) % s.ringSize
	return s.ring[idx].data
}

// hasSegments returns true if at least one segment is available.
func (s *hlsSegmenter) hasSegments() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.count > 0
}
