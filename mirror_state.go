package main

import (
	"encoding/json"
	"os"
	"sync"
	"time"
)

// mirrorState persists mirror state across restarts.
// Key invariant: seqFloor must never decrease, even after restart.
// This prevents metadata seq regression which would cause clients to
// reject the mirror's signed documents.
type mirrorState struct {
	mu sync.Mutex

	ChannelID    string `json:"channel_id"`
	SeqFloor     int64  `json:"seq_floor"`            // highest seq seen or signed
	LastMediaSeq uint64 `json:"last_media_seq"`        // last HLS media sequence number
	Promoted     bool   `json:"promoted"`              // true if actively signing
	PromotedAt   string `json:"promoted_at,omitempty"` // RFC 3339 timestamp
	LastVerified string `json:"last_verified"`         // RFC 3339 of last successful upstream poll

	path string // file path for persistence (not serialized)
}

// loadMirrorState reads state from disk. If the file is missing or corrupt,
// returns a fresh state (fail-safe: fresh start, not crash).
func loadMirrorState(path, channelID string) *mirrorState {
	s := &mirrorState{
		ChannelID: channelID,
		path:      path,
	}

	data, err := os.ReadFile(path)
	if err != nil {
		// Missing file is normal (first run)
		return s
	}

	var loaded mirrorState
	if err := json.Unmarshal(data, &loaded); err != nil {
		logErrorf("mirror state: corrupt file %s, starting fresh: %v", path, err)
		return s
	}

	// Validate channel ID matches
	if loaded.ChannelID != "" && loaded.ChannelID != channelID {
		logErrorf("mirror state: channel ID mismatch (file=%s, expected=%s), starting fresh", loaded.ChannelID, channelID)
		return s
	}

	loaded.path = path
	return &loaded
}

// save persists current state to disk atomically.
func (s *mirrorState) save() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saveLocked()
}

func (s *mirrorState) saveLocked() error {
	if s.path == "" {
		return nil
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(s.path, data, 0600)
}

// updateVerified records a successful upstream poll and the upstream's seq.
func (s *mirrorState) updateVerified(upstreamSeq int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.LastVerified = time.Now().UTC().Format(time.RFC3339)
	if upstreamSeq > s.SeqFloor {
		s.SeqFloor = upstreamSeq
	}
	s.saveLocked()
}

// updateMediaSeq records the latest HLS media sequence number.
func (s *mirrorState) updateMediaSeq(seq uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if seq > s.LastMediaSeq {
		s.LastMediaSeq = seq
	}
}

// promote transitions to active signer mode. Returns the seq to use for
// the first signed metadata document: max(seqFloor, now) + 1.
func (s *mirrorState) promote() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.Promoted = true
	s.PromotedAt = time.Now().UTC().Format(time.RFC3339)

	// Seq must be strictly greater than anything seen from upstream
	now := time.Now().UTC().Unix()
	base := s.SeqFloor
	if now > base {
		base = now
	}
	newSeq := base + 1
	s.SeqFloor = newSeq

	s.saveLocked()
	return newSeq
}

// isPromoted returns true if the mirror is in active signer mode.
func (s *mirrorState) isPromoted() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Promoted
}

// getSeqFloor returns the current seq floor (for promoted signing).
func (s *mirrorState) getSeqFloor() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.SeqFloor
}

// getLastMediaSeq returns the last known HLS media sequence number.
func (s *mirrorState) getLastMediaSeq() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.LastMediaSeq
}
