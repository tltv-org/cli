package main

// Audio tone generator for the test signal server.
//
// Produces a continuous 1 kHz sine tone as AAC-LC audio in ADTS frames.
// Rather than implementing a runtime AAC encoder, a loop of 48 pre-encoded
// ADTS frames (~1 second of audio) is cycled for the segment duration.
// The frames were captured from a steady-state region of a 1 kHz sine tone
// encoded with ffmpeg's AAC-LC encoder, so the MDCT overlap between adjacent
// frames is natural and produces a clean continuous tone.
//
// Audio PTS is derived from a persistent frame counter (audioFrameNum) that
// runs continuously across segments. Each frame advances by exactly 1920
// ticks (1024 samples × 90000/48000). This ensures gapless audio at segment
// boundaries — the decoder sees a continuous PTS timeline with no
// discontinuities.
//
// Frame parameters:
//   MPEG-4 AAC-LC, 48000 Hz, mono, 1024 samples/frame
//   ADTS header: 7 bytes (no CRC)
//   Stream type in MPEG-TS PMT: 0x0F (ISO/IEC 13818-7 ADTS AAC)
//   PTS increment per frame: 1920 (90kHz ticks)
//
// The loop data lives in server_audio_data.go (generated, ~8.5 KB).

const (
	// aacSampleRate is the audio sample rate in Hz.
	aacSampleRate = 48000

	// aacSamplesPerFrame is the number of PCM samples per AAC-LC frame.
	aacSamplesPerFrame = 1024

	// aacPTSPerFrame is the PTS increment per AAC frame in 90kHz ticks.
	// 1024 samples × 90000 / 48000 = 1920.
	aacPTSPerFrame = 1920

	// aacLoopFrames is the number of ADTS frames in the pre-encoded loop.
	aacLoopFrames = 48
)

// audioFrame is a single ADTS frame with its PTS for interleaved muxing.
type audioFrame struct {
	data []byte
	pts  int64
}

// aacLoopIndex holds the byte offset and length of each ADTS frame within
// aacToneLoop. Built once at init time by parsing ADTS sync words.
var aacLoopIndex [aacLoopFrames]struct{ off, len int }

func init() {
	off := 0
	for i := 0; i < aacLoopFrames; i++ {
		if off+7 > len(aacToneLoop) || aacToneLoop[off] != 0xFF || (aacToneLoop[off+1]&0xF0) != 0xF0 {
			panic("server_audio: corrupt ADTS tone loop data")
		}
		flen := (int(aacToneLoop[off+3]&0x03) << 11) |
			(int(aacToneLoop[off+4]) << 3) |
			(int(aacToneLoop[off+5]) >> 5)
		aacLoopIndex[i].off = off
		aacLoopIndex[i].len = flen
		off += flen
	}
}

// audioFramesForSegment returns the number of AAC frames needed to cover
// the given segment duration in seconds.
func audioFramesForSegment(segDurationSec int) int {
	// Each frame is 1024 samples at 48000 Hz = 21.333... ms
	// For a 2-second segment: ceil(2 * 48000 / 1024) = 94 frames
	totalSamples := segDurationSec * aacSampleRate
	return (totalSamples + aacSamplesPerFrame - 1) / aacSamplesPerFrame
}

// generateAudioFrames returns individual ADTS frames with PTS for one segment.
// audioFrameNum is the persistent frame counter (continuous across segments).
// segEndPTS is the PTS of the video frame just past this segment's end.
// Frames are generated until the audio PTS reaches or exceeds segEndPTS.
func generateAudioFrames(audioFrameNum uint64, segEndPTS int64) []audioFrame {
	// Estimate ~94 frames per 2s segment
	frames := make([]audioFrame, 0, 100)
	fn := audioFrameNum
	for {
		pts := int64(fn*aacPTSPerFrame) & ((1 << 33) - 1)
		if pts >= segEndPTS {
			break
		}
		loopIdx := int(fn % aacLoopFrames)
		entry := &aacLoopIndex[loopIdx]
		frames = append(frames, audioFrame{
			data: aacToneLoop[entry.off : entry.off+entry.len],
			pts:  pts,
		})
		fn++
	}
	return frames
}

// generateAudioData returns the raw ADTS frame data for one segment.
// It cycles through the 48-frame pre-encoded loop for the required
// number of frames. The caller PES-wraps and TS-packetizes the output.
func generateAudioData(segDurationSec int) []byte {
	nFrames := audioFramesForSegment(segDurationSec)
	// Estimate size: average ~178 bytes/frame
	data := make([]byte, 0, nFrames*180)
	for i := 0; i < nFrames; i++ {
		idx := i % aacLoopFrames
		entry := &aacLoopIndex[idx]
		data = append(data, aacToneLoop[entry.off:entry.off+entry.len]...)
	}
	return data
}
