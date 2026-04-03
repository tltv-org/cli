package main

import (
	"math/bits"
)

// H.264 Baseline profile encoder with adaptive macroblock type selection.
// Uses Intra_16x16 prediction for flat regions (DC/Vertical/Horizontal modes)
// and Intra_4x4 prediction for detailed regions (9 directional modes per block).
// CAVLC entropy coding. Designed for test signal rendering: SMPTE bars with
// text overlays, but handles arbitrary source content.
//
// Reference: ITU-T H.264 (02/2014), sections 7.3, 7.4, 8.5, 9.2.

// i4x4SADThreshold is the SAD threshold per 4×4 block for I_4x4 mode decision.
// If any block's SAD against I_16x16 prediction exceeds this value (12 average
// per pixel × 16 pixels), the entire macroblock switches to I_4x4.
const i4x4SADThreshold = 192

// chromaQPTable maps luma QP ≥ 30 to chroma QP (H.264 Table 8-15).
var chromaQPTable = [...]int{29, 30, 31, 32, 32, 33, 34, 34, 35, 35, 36, 36, 37, 37, 37, 38, 38, 38, 39, 39, 39, 39}

// h264Settings holds configurable encoder parameters.
// width/height are the encoded dimensions (aligned to multiples of 16).
// cropRight/cropBottom tell the decoder how many pixels to crop for
// non-16-aligned source dimensions (stored in SPS frame_cropping).
type h264Settings struct {
	width      int
	height     int
	cropRight  int
	cropBottom int
	fps        int
	qp         int
}

// H.264 level table: select level based on resolution and frame rate.
// Each entry: maxMBs/sec threshold, level_idc.
var levelTable = [][2]int{
	{1485, 10},    // 1.0: 176x144 @ 15
	{3000, 11},    // 1.1: 320x240 @ 10
	{6000, 12},    // 1.2: 320x240 @ 20
	{11880, 13},   // 1.3: 352x288 @ 30
	{11880, 20},   // 2.0: 352x288 @ 30
	{19800, 21},   // 2.1: 352x576 @ 25
	{20250, 22},   // 2.2: 720x480 @ 15
	{40500, 30},   // 3.0: 720x480 @ 30
	{108000, 31},  // 3.1: 1280x720 @ 30
	{216000, 32},  // 3.2: 1280x720 @ 60
	{245760, 40},  // 4.0: 2048x1024 @ 30
	{245760, 41},  // 4.1: 2048x1024 @ 30
	{522240, 42},  // 4.2: 2048x1080 @ 60
	{589824, 50},  // 5.0: 3672x1536 @ 26
	{983040, 51},  // 5.1: 4096x2160 @ 30
	{2073600, 52}, // 5.2: 4096x2160 @ 60
}

// selectLevel returns the H.264 level_idc for the given resolution and fps.
func selectLevel(width, height, fps int) int {
	mbsPerSec := (width / 16) * (height / 16) * fps
	for _, entry := range levelTable {
		if mbsPerSec <= entry[0] {
			return entry[1]
		}
	}
	return 52 // max
}

// bitWriter accumulates bits MSB-first for H.264 bitstream generation.
type bitWriter struct {
	buf    []byte
	curBit uint // bits written into current byte (0-7)
}

func newBitWriter(capacity int) *bitWriter {
	return &bitWriter{buf: make([]byte, 0, capacity)}
}

func (w *bitWriter) writeBit(b uint8) {
	if w.curBit == 0 {
		w.buf = append(w.buf, 0)
	}
	if b != 0 {
		w.buf[len(w.buf)-1] |= 1 << (7 - w.curBit)
	}
	w.curBit++
	if w.curBit == 8 {
		w.curBit = 0
	}
}

func (w *bitWriter) writeBits(val uint32, n int) {
	for i := n - 1; i >= 0; i-- {
		w.writeBit(uint8((val >> i) & 1))
	}
}

func (w *bitWriter) writeUE(val uint32) {
	// Exp-Golomb unsigned: write (bits-1) leading zeros, then val+1
	vp1 := val + 1
	numBits := bits.Len32(vp1)
	for i := 0; i < numBits-1; i++ {
		w.writeBit(0)
	}
	w.writeBits(vp1, numBits)
}

func (w *bitWriter) writeSE(val int32) {
	// Signed Exp-Golomb: map to unsigned
	var uval uint32
	if val > 0 {
		uval = uint32(2*val - 1)
	} else if val < 0 {
		uval = uint32(-2 * val)
	}
	w.writeUE(uval)
}

func (w *bitWriter) alignToByte() {
	if w.curBit > 0 {
		for w.curBit != 0 {
			w.writeBit(0)
		}
	}
}

// rbspTrailingBits writes a 1 followed by zeros to byte-align.
func (w *bitWriter) rbspTrailingBits() {
	w.writeBit(1)
	w.alignToByte()
}

func (w *bitWriter) bytes() []byte {
	return w.buf
}

// insertEBSP applies Annex B emulation prevention: after 0x00 0x00,
// if the next byte is 0x00, 0x01, 0x02, or 0x03, insert 0x03 before it.
func insertEBSP(rbsp []byte) []byte {
	out := make([]byte, 0, len(rbsp)+len(rbsp)/100)
	count := 0
	for _, b := range rbsp {
		if count >= 2 && b <= 3 {
			out = append(out, 0x03)
			count = 0
		}
		out = append(out, b)
		if b == 0 {
			count++
		} else {
			count = 0
		}
	}
	return out
}

// annexBNALU wraps RBSP data in Annex B framing: start code + NAL header + EBSP data.
func annexBNALU(naluType, refIDC uint8, rbsp []byte) []byte {
	header := (refIDC << 5) | (naluType & 0x1F)
	ebsp := insertEBSP(rbsp)
	out := make([]byte, 0, 4+1+len(ebsp))
	out = append(out, 0x00, 0x00, 0x00, 0x01) // 4-byte start code
	out = append(out, header)
	out = append(out, ebsp...)
	return out
}

// --- SPS (Sequence Parameter Set) ---

// encodeSPS generates an SPS for Baseline profile with the given settings.
func encodeSPS(s *h264Settings) []byte {
	mbCols := s.width / 16
	mbRows := s.height / 16
	level := selectLevel(s.width, s.height, s.fps)

	w := newBitWriter(32)
	w.writeBits(66, 8)              // profile_idc = Baseline
	w.writeBit(1)                   // constraint_set0_flag
	w.writeBit(1)                   // constraint_set1_flag
	w.writeBit(0)                   // constraint_set2_flag
	w.writeBit(0)                   // constraint_set3_flag
	w.writeBit(0)                   // constraint_set4_flag
	w.writeBit(0)                   // constraint_set5_flag
	w.writeBits(0, 2)              // reserved_zero_2bits
	w.writeBits(uint32(level), 8)  // level_idc
	w.writeUE(0)                   // seq_parameter_set_id
	w.writeUE(0)                   // log2_max_frame_num_minus4 → frame_num is u(4)
	w.writeUE(0)                   // pic_order_cnt_type = 0
	w.writeUE(0)                   // log2_max_pic_order_cnt_lsb_minus4 → POC is u(4)
	w.writeUE(0)                   // max_num_ref_frames = 0 (I-only)
	w.writeBit(0)                  // gaps_in_frame_num_value_allowed_flag
	w.writeUE(uint32(mbCols - 1)) // pic_width_in_mbs_minus1
	w.writeUE(uint32(mbRows - 1)) // pic_height_in_map_units_minus1
	w.writeBit(1) // frame_mbs_only_flag
	// mb_adaptive_frame_field_flag: absent (frame_mbs_only=1)
	w.writeBit(0) // direct_8x8_inference_flag

	// Frame cropping for non-16-aligned source dimensions.
	// Luma crop units are 2 pixels for 4:2:0 frame_mbs_only (§7.4.2.1.1).
	cropRight := s.cropRight / 2
	cropBottom := s.cropBottom / 2
	if cropRight > 0 || cropBottom > 0 {
		w.writeBit(1)                      // frame_cropping_flag
		w.writeUE(0)                       // frame_crop_left_offset
		w.writeUE(uint32(cropRight))       // frame_crop_right_offset
		w.writeUE(0)                       // frame_crop_top_offset
		w.writeUE(uint32(cropBottom))      // frame_crop_bottom_offset
	} else {
		w.writeBit(0) // frame_cropping_flag
	}

	w.writeBit(1) // vui_parameters_present_flag = 1

	// Minimal VUI: timing info with EBSP-safe values.
	// Use num_units_in_tick=1001 to avoid 00 00 00 sequences in the RBSP
	// that trigger EBSP emulation prevention byte insertion.
	w.writeBit(1)     // aspect_ratio_info_present_flag = 1
	w.writeBits(1, 8) // aspect_ratio_idc = 1 (square pixels, SAR 1:1)
	w.writeBit(0) // overscan_info_present_flag
	w.writeBit(0) // video_signal_type_present_flag
	w.writeBit(0) // chroma_loc_info_present_flag
	w.writeBit(1) // timing_info_present_flag
	// fps = time_scale / (2 * num_units_in_tick) → time_scale = 2 * fps * 1001
	w.writeBits(1001, 32)                       // num_units_in_tick
	w.writeBits(uint32(2*s.fps*1001), 32)       // time_scale
	w.writeBit(1)                               // fixed_frame_rate_flag
	w.writeBit(0)                               // nal_hrd_parameters_present_flag
	w.writeBit(0)                               // vcl_hrd_parameters_present_flag
	// low_delay_hrd_flag: absent (no HRD)
	w.writeBit(0)                               // pic_struct_present_flag
	w.writeBit(0)                               // bitstream_restriction_flag

	w.rbspTrailingBits()
	return annexBNALU(7, 3, w.bytes()) // SPS: nal_unit_type=7, nal_ref_idc=3
}

// --- PPS (Picture Parameter Set) ---

func encodePPS(s *h264Settings) []byte {
	w := newBitWriter(8)
	w.writeUE(0) // pic_parameter_set_id
	w.writeUE(0) // seq_parameter_set_id
	w.writeBit(0) // entropy_coding_mode_flag = CAVLC (Baseline)
	w.writeBit(0) // bottom_field_pic_order_in_frame_present_flag
	w.writeUE(0) // num_slice_groups_minus1
	w.writeUE(0) // num_ref_idx_l0_default_active_minus1
	w.writeUE(0) // num_ref_idx_l1_default_active_minus1
	w.writeBit(0) // weighted_pred_flag
	w.writeBits(0, 2) // weighted_bipred_idc
	w.writeSE(int32(s.qp - 26)) // pic_init_qp_minus26
	w.writeSE(0) // pic_init_qs_minus26
	w.writeSE(0) // chroma_qp_index_offset
	w.writeBit(0) // deblocking_filter_control_present_flag
	w.writeBit(0) // constrained_intra_pred_flag
	w.writeBit(0) // redundant_pic_cnt_present_flag

	w.rbspTrailingBits()
	return annexBNALU(8, 3, w.bytes()) // PPS: nal_unit_type=8, nal_ref_idc=3
}

// --- Access Unit Delimiter ---

func encodeAUD() []byte {
	w := newBitWriter(2)
	w.writeBits(0, 3) // primary_pic_type = 0 (I-slices)
	w.rbspTrailingBits()
	return annexBNALU(9, 0, w.bytes()) // AUD: nal_unit_type=9, nal_ref_idc=0
}

// --- Block Scan Order ---

// blk4x4Pos maps luma4x4BlkIdx to (rowOffset, colOffset) within the MB.
// Follows H.264 Table 6-2: Z-scan of 4x4 blocks within 8x8 blocks.
// This is the mandatory block ordering for I_16x16 DC collection and AC transmission.
var blk4x4Pos = [16][2]int{
	{0, 0}, {0, 4}, {4, 0}, {4, 4}, // 8x8 block 0 (top-left)
	{0, 8}, {0, 12}, {4, 8}, {4, 12}, // 8x8 block 1 (top-right)
	{8, 0}, {8, 4}, {12, 0}, {12, 4}, // 8x8 block 2 (bottom-left)
	{8, 8}, {8, 12}, {12, 8}, {12, 12}, // 8x8 block 3 (bottom-right)
}

// blk4x4Grid maps luma4x4BlkIdx to (row, col) in 4x4-block units within MB.
var blk4x4Grid = [16][2]int{
	{0, 0}, {0, 1}, {1, 0}, {1, 1},
	{0, 2}, {0, 3}, {1, 2}, {1, 3},
	{2, 0}, {2, 1}, {3, 0}, {3, 1},
	{2, 2}, {2, 3}, {3, 2}, {3, 3},
}

// mbGridToBlkIdx maps (row, col) in 4×4-block units within a MB to Z-scan block index.
// Inverse of blk4x4Grid.
var mbGridToBlkIdx = [4][4]int{
	{0, 1, 4, 5},
	{2, 3, 6, 7},
	{8, 9, 12, 13},
	{10, 11, 14, 15},
}

// --- I_4x4 CBP Tables ---

// golombToIntraCBP maps Exp-Golomb code index to CBP value for Intra macroblocks.
// H.264 Table 9-4 (forward mapping: codeNum → coded_block_pattern).
var golombToIntraCBP = [48]int{
	47, 31, 15, 0, 23, 27, 29, 30, 7, 11, 13, 14, 39, 43, 45, 46,
	16, 3, 5, 10, 12, 19, 21, 26, 28, 35, 37, 42, 44, 1, 2, 4,
	8, 17, 18, 20, 24, 6, 9, 22, 25, 32, 33, 34, 36, 40, 38, 41,
}

// intraCBPToGolomb is the inverse: CBP value → Exp-Golomb code index.
// Initialized in init().
var intraCBPToGolomb [48]int

func init() {
	for i, cbp := range golombToIntraCBP {
		intraCBPToGolomb[cbp] = i
	}
}

// --- Macroblock Encoder State ---

// mbEncoder holds per-frame state for macroblock encoding.
// Created once per frame in encodeIDRFrame; per-MB methods take only (mbX, mbY).
type mbEncoder struct {
	bw         *bitWriter
	f          *Frame
	reconY     []uint8
	reconCb    []uint8
	reconCr    []uint8
	tcGrid     []int
	cbTCGrid   []int
	crTCGrid   []int
	mbModeGrid []int8
	gridW      int
	chromaGridW int
	width      int
	height     int
	chromaW    int
	qp         int
	chromaQP   int
	mbCols     int
}

// chromaData holds chroma encoding results shared between the bitstream
// writing and reconstruction phases. Computed once, used twice.
type chromaData struct {
	mode       int
	cbPredArr  [64]uint8
	crPredArr  [64]uint8
	cbDCQ      [4]int32
	crDCQ      [4]int32
	cbACBlocks [4][15]int32
	crACBlocks [4][15]int32
	chromaCBP  int
}

// --- IDR Slice ---

// encodeIDRFrame encodes one complete IDR frame with I_16x16 and I_4x4 macroblocks.
// Maintains reconstruction buffers, totalCoeff grid, and per-block mode grid.
func encodeIDRFrame(f *Frame, s *h264Settings, frameNum, idrPicID int) []byte {
	mbCols := s.width / 16
	mbRows := s.height / 16

	bw := newBitWriter(64 * 1024)

	// Slice header (ITU-T H.264 §7.3.3)
	bw.writeUE(0)                         // first_mb_in_slice
	bw.writeUE(2)                         // slice_type = 2 (I)
	bw.writeUE(0)                         // pic_parameter_set_id
	bw.writeBits(0, 4)                    // frame_num = 0 for IDR
	bw.writeUE(uint32(idrPicID & 0xFFFF)) // idr_pic_id
	bw.writeBits(0, 4)                    // pic_order_cnt_lsb
	bw.writeBit(0)                        // no_output_of_prior_pics_flag
	bw.writeBit(0)                        // long_term_reference_flag
	bw.writeSE(0)                         // slice_qp_delta = 0

	gridW := s.width / 4
	mbModeGrid := make([]int8, (s.height/4)*gridW)
	for i := range mbModeGrid {
		mbModeGrid[i] = -1
	}

	enc := &mbEncoder{
		bw:          bw,
		f:           f,
		reconY:      make([]uint8, s.width*s.height),
		reconCb:     make([]uint8, (s.width/2)*(s.height/2)),
		reconCr:     make([]uint8, (s.width/2)*(s.height/2)),
		tcGrid:      make([]int, (s.height/4)*gridW),
		cbTCGrid:    make([]int, (s.height/8)*(s.width/8)),
		crTCGrid:    make([]int, (s.height/8)*(s.width/8)),
		mbModeGrid:  mbModeGrid,
		gridW:       gridW,
		chromaGridW: s.width / 8,
		width:       s.width,
		height:      s.height,
		chromaW:     s.width / 2,
		qp:          s.qp,
		chromaQP:    chromaQPFromLuma(s.qp),
		mbCols:      mbCols,
	}

	for mbY := 0; mbY < mbRows; mbY++ {
		for mbX := 0; mbX < mbCols; mbX++ {
			enc.encodeMB(mbX, mbY)
		}
	}

	bw.rbspTrailingBits()
	return annexBNALU(5, 3, bw.bytes())
}

// --- Macroblock Encoding ---

// encodeMB dispatches to either I_16x16 or I_4x4 encoding based on a per-block
// residual energy heuristic. Flat/smooth MBs use I_16x16 (fewer mode signaling bits);
// MBs with fine detail (text, edges) use I_4x4 for better prediction quality.
// Computes I_16x16 prediction once and reuses it for both the decision and encoding.
func (e *mbEncoder) encodeMB(mbX, mbY int) {
	// Compute I_16x16 prediction once (used for both mode decision and encoding)
	lumaMode, lumaPredArr := selectLumaMode(e.f, e.reconY, e.width, e.height, mbX, mbY)

	// Check if any 4x4 block has high residual against I_16x16 prediction
	useI4x4 := false
	for blkIdx := 0; blkIdx < 16; blkIdx++ {
		blkRow := blk4x4Pos[blkIdx][0]
		blkCol := blk4x4Pos[blkIdx][1]
		sad := 0
		for r := 0; r < 4; r++ {
			for c := 0; c < 4; c++ {
				d := int(e.f.Y[(mbY*16+blkRow+r)*e.width+mbX*16+blkCol+c]) - int(lumaPredArr[(blkRow+r)*16+blkCol+c])
				if d < 0 {
					d = -d
				}
				sad += d
			}
		}
		if sad > i4x4SADThreshold {
			useI4x4 = true
			break
		}
	}

	if useI4x4 {
		e.encodeMB_I4x4(mbX, mbY)
	} else {
		e.encodeMB_I16x16(mbX, mbY, lumaMode, lumaPredArr)
	}
}

// --- Intra_16x16 Macroblock Encoding ---

// encodeMB_I16x16 encodes one macroblock using Intra_16x16 prediction with CAVLC.
// Selects the best luma prediction mode (DC/Vertical/Horizontal) per MB.
// Uses H.264 Z-scan block ordering, proper nC prediction from the totalCoeff grid,
// and spec-correct inverse Hadamard → scale reconstruction (§8.5.6).
func (e *mbEncoder) encodeMB_I16x16(mbX, mbY, lumaMode int, lumaPredArr [256]uint8) {
	bw := e.bw
	qp := e.qp
	width := e.width

	// Compute residuals and DCT for each 4x4 sub-block in Z-scan order.
	// dcCoeffs[blkIdx] is indexed by luma4x4BlkIdx for the Hadamard transform.
	var dcCoeffs [16]int32
	var acBlocks [16][15]int32 // AC coefficients in zigzag scan order per sub-block
	hasAC := false

	for blkIdx := 0; blkIdx < 16; blkIdx++ {
		blkRow := blk4x4Pos[blkIdx][0]
		blkCol := blk4x4Pos[blkIdx][1]
		var block [16]int32
		for r := 0; r < 4; r++ {
			for c := 0; c < 4; c++ {
				py := mbY*16 + blkRow + r
				px := mbX*16 + blkCol + c
				block[r*4+c] = int32(e.f.Y[py*width+px]) - int32(lumaPredArr[(blkRow+r)*16+blkCol+c])
			}
		}
		trans := forwardDCT4x4(block)
		dcCoeffs[blkIdx] = trans[0]

		// Quantize AC coefficients and check if any are non-zero
		quantAC := quantize4x4(trans, qp)
		for i := 1; i < 16; i++ {
			acBlocks[blkIdx][i-1] = quantAC[zigzag4x4[i]]
			if quantAC[zigzag4x4[i]] != 0 {
				hasAC = true
			}
		}
	}

	// Forward Hadamard + quantize DC
	hadDC := forwardHadamard4x4(dcCoeffs)
	quantDC := quantizeDC4x4(hadDC, qp)

	// Chroma: residual computation, prediction, DCT, quantize
	cd := e.encodeChromaResidual(mbX, mbY)

	// Determine coded block pattern
	lumaCBP := 0
	if hasAC {
		lumaCBP = 15
	}

	// mb_type for I_16x16: 1 + predMode + chromaCBP*4 + lumaCBPFlag*12
	// predMode: 0=Vertical, 1=Horizontal, 2=DC (H.264 Table 7-11)
	lumaCBPFlag := 0
	if lumaCBP == 15 {
		lumaCBPFlag = 1
	}
	mbType := 1 + lumaMode + cd.chromaCBP*4 + lumaCBPFlag*12
	bw.writeUE(uint32(mbType))
	bw.writeUE(uint32(cd.mode)) // intra_chroma_pred_mode
	bw.writeSE(0)               // mb_qp_delta = 0

	// Encode luma DC block (always present for I_16x16).
	// nC for DC block: hi264 uses AC nC grid at MB's top-left 4x4 block position.
	// The DC block's own totalCoeff is NOT stored back into the grid.
	dcNC := computeNC(e.tcGrid, e.gridW, width, e.height, mbX, mbY, 0, 0)
	var dcScan [16]int32
	for i := 0; i < 16; i++ {
		dcScan[i] = quantDC[zigzag4x4[i]]
	}
	encodeCAVLCBlock(bw, dcScan[:], dcNC, 16)

	// Encode luma AC blocks in Z-scan order with proper nC prediction
	if lumaCBP == 15 {
		for blkIdx := 0; blkIdx < 16; blkIdx++ {
			nC := computeNC(e.tcGrid, e.gridW, width, e.height, mbX, mbY, blk4x4Grid[blkIdx][0], blk4x4Grid[blkIdx][1])
			tc := encodeCAVLCBlock(bw, acBlocks[blkIdx][:], nC, 15)
			frameRow := mbY*4 + blk4x4Grid[blkIdx][0]
			frameCol := mbX*4 + blk4x4Grid[blkIdx][1]
			e.tcGrid[frameRow*e.gridW+frameCol] = tc
		}
	} else {
		for blkIdx := 0; blkIdx < 16; blkIdx++ {
			frameRow := mbY*4 + blk4x4Grid[blkIdx][0]
			frameCol := mbX*4 + blk4x4Grid[blkIdx][1]
			e.tcGrid[frameRow*e.gridW+frameCol] = 0
		}
	}

	// Encode chroma bitstream (DC/AC coefficients)
	e.writeChromaBitstream(&cd, mbX, mbY)

	// --- Reconstruction: simulate decoder (§8.5.6, §8.5.12) ---

	// Luma DC: spec §8.5.6 — inverse Hadamard FIRST, then scale.
	invHadDC := invHadamard4x4(quantDC)
	reconDCvals := scaleDC4x4(invHadDC, qp)

	for blkIdx := 0; blkIdx < 16; blkIdx++ {
		blkRow := blk4x4Pos[blkIdx][0]
		blkCol := blk4x4Pos[blkIdx][1]

		var reconBlock [16]int32
		reconBlock[0] = reconDCvals[blkIdx]

		if hasAC {
			var quantACRaster [16]int32
			for j := 1; j < 16; j++ {
				quantACRaster[zigzag4x4[j]] = acBlocks[blkIdx][j-1]
			}
			invAC := invQuantize4x4(quantACRaster, qp)
			for i := 1; i < 16; i++ {
				reconBlock[i] = invAC[i]
			}
		}

		idctBlock := inverseDCT4x4(reconBlock)

		for r := 0; r < 4; r++ {
			for c := 0; c < 4; c++ {
				val := int32(lumaPredArr[(blkRow+r)*16+blkCol+c]) + idctBlock[r*4+c]
				if val < 0 {
					val = 0
				} else if val > 255 {
					val = 255
				}
				e.reconY[(mbY*16+blkRow+r)*width+mbX*16+blkCol+c] = uint8(val)
			}
		}
	}

	// Chroma reconstruction
	e.reconstructChroma(&cd, mbX, mbY)
}

// --- Intra_4x4 Macroblock Encoding ---

// encodeMB_I4x4 encodes one macroblock using Intra_4x4 prediction with CAVLC.
// Each 4×4 block gets its own directional prediction mode (9 modes, §8.3.1.2),
// predicted from already-reconstructed neighbors including earlier blocks in
// the same MB. No Hadamard transform — DC is position [0] of regular 4×4 DCT.
// Chroma encoding is shared via encodeChromaResidual / writeChromaBitstream / reconstructChroma.
func (e *mbEncoder) encodeMB_I4x4(mbX, mbY int) {
	bw := e.bw
	qp := e.qp
	width := e.width

	// Phase 1: Sequential prediction, residual computation, and reconstruction.
	// Each block must be fully reconstructed before the next block's prediction.
	var modes [16]int
	var quantBlocks [16][16]int32 // full 16-coeff quantized blocks (zigzag-scanned)
	var hasNonZero [16]bool

	for blkIdx := 0; blkIdx < 16; blkIdx++ {
		mode, pred := selectIntra4x4Mode(e.f, e.reconY, width, e.height, mbX, mbY, blkIdx, e.mbCols)
		modes[blkIdx] = mode

		blkRow := blk4x4Pos[blkIdx][0]
		blkCol := blk4x4Pos[blkIdx][1]

		var block [16]int32
		for r := 0; r < 4; r++ {
			for c := 0; c < 4; c++ {
				py := mbY*16 + blkRow + r
				px := mbX*16 + blkCol + c
				block[r*4+c] = int32(e.f.Y[py*width+px]) - int32(pred[r*4+c])
			}
		}

		trans := forwardDCT4x4(block)
		quant := quantize4x4(trans, qp)

		for i := 0; i < 16; i++ {
			quantBlocks[blkIdx][i] = quant[zigzag4x4[i]]
			if quant[zigzag4x4[i]] != 0 {
				hasNonZero[blkIdx] = true
			}
		}

		var reconBlock [16]int32
		if hasNonZero[blkIdx] {
			reconBlock = invQuantize4x4(quant, qp)
		}
		idctBlock := inverseDCT4x4(reconBlock)

		for r := 0; r < 4; r++ {
			for c := 0; c < 4; c++ {
				val := int32(pred[r*4+c]) + idctBlock[r*4+c]
				if val < 0 {
					val = 0
				} else if val > 255 {
					val = 255
				}
				e.reconY[(mbY*16+blkRow+r)*width+mbX*16+blkCol+c] = uint8(val)
			}
		}

		frameRow := mbY*4 + blk4x4Grid[blkIdx][0]
		frameCol := mbX*4 + blk4x4Grid[blkIdx][1]
		e.mbModeGrid[frameRow*e.gridW+frameCol] = int8(mode)
	}

	// Phase 2: Compute per-8×8 luma CBP (§7.4.5.3)
	// Each bit covers one 8×8 block containing four 4×4 sub-blocks.
	lumaCBP := 0
	for b8 := 0; b8 < 4; b8++ {
		base := b8 * 4
		for i := 0; i < 4; i++ {
			if hasNonZero[base+i] {
				lumaCBP |= 1 << uint(b8)
				break
			}
		}
	}

	// Phase 3: Chroma residual (shared with I_16x16)
	cd := e.encodeChromaResidual(mbX, mbY)

	// Phase 4: Write bitstream

	// mb_type = 0 for I_NxN (I_4x4)
	bw.writeUE(0) // mb_type = 0 → ue(0) = single bit "1"

	// Prediction mode signaling (§7.3.5.1): 16 blocks in Z-scan order
	for blkIdx := 0; blkIdx < 16; blkIdx++ {
		blkRow := blk4x4Grid[blkIdx][0]
		blkCol := blk4x4Grid[blkIdx][1]
		frameRow := mbY*4 + blkRow
		frameCol := mbX*4 + blkCol

		modeA := -1
		if frameCol > 0 {
			modeA = int(e.mbModeGrid[frameRow*e.gridW+(frameCol-1)])
		}
		modeB := -1
		if frameRow > 0 {
			modeB = int(e.mbModeGrid[(frameRow-1)*e.gridW+frameCol])
		}

		predicted := derivePredIntra4x4Mode(modeA, modeB)
		actual := modes[blkIdx]

		if actual == predicted {
			bw.writeBit(1)
		} else {
			bw.writeBit(0)
			rem := actual
			if actual > predicted {
				rem = actual - 1
			}
			bw.writeBits(uint32(rem), 3)
		}
	}

	bw.writeUE(uint32(cd.mode)) // intra_chroma_pred_mode

	// coded_block_pattern as me(v) — mapped Exp-Golomb (§7.4.5.3, Table 9-4)
	cbpValue := cd.chromaCBP*16 + lumaCBP
	bw.writeUE(uint32(intraCBPToGolomb[cbpValue]))

	// mb_qp_delta — only if CBP > 0
	if cbpValue > 0 {
		bw.writeSE(0)
	}

	// Phase 5: Encode luma residual blocks (maxNumCoeff=16, per-8×8 CBP)
	for blkIdx := 0; blkIdx < 16; blkIdx++ {
		b8 := blkIdx / 4
		frameRow := mbY*4 + blk4x4Grid[blkIdx][0]
		frameCol := mbX*4 + blk4x4Grid[blkIdx][1]

		if lumaCBP&(1<<uint(b8)) != 0 {
			nC := computeNC(e.tcGrid, e.gridW, width, e.height, mbX, mbY, blk4x4Grid[blkIdx][0], blk4x4Grid[blkIdx][1])
			tc := encodeCAVLCBlock(bw, quantBlocks[blkIdx][:], nC, 16)
			e.tcGrid[frameRow*e.gridW+frameCol] = tc
		} else {
			e.tcGrid[frameRow*e.gridW+frameCol] = 0
		}
	}

	// Phase 6: Encode chroma bitstream (shared with I_16x16)
	e.writeChromaBitstream(&cd, mbX, mbY)

	// Phase 7: Chroma reconstruction (shared with I_16x16)
	e.reconstructChroma(&cd, mbX, mbY)
}

// --- Shared Chroma Encoding ---
//
// Chroma processing is identical for I_16x16 and I_4x4 macroblocks.
// These three methods handle the full pipeline: residual computation,
// bitstream writing, and reconstruction.

// encodeChromaResidual computes chroma prediction, residuals, DCT, quantization,
// and coded block pattern. Returns a chromaData struct used by the bitstream
// writer and reconstructor.
func (e *mbEncoder) encodeChromaResidual(mbX, mbY int) chromaData {
	cd := chromaData{}
	hasChromaDC := false
	hasChromaAC := false
	var cbDCCoeffs, crDCCoeffs [4]int32

	cd.mode, cd.cbPredArr, cd.crPredArr = selectChromaMode(e.f, e.reconCb, e.reconCr, e.chromaW, mbX, mbY)

	for cIdx := 0; cIdx < 4; cIdx++ {
		cRow := (cIdx / 2) * 4
		cCol := (cIdx % 2) * 4
		var cbBlock, crBlock [16]int32
		for r := 0; r < 4; r++ {
			for c := 0; c < 4; c++ {
				cy := mbY*8 + cRow + r
				cx := mbX*8 + cCol + c
				cbBlock[r*4+c] = int32(e.f.Cb[cy*e.chromaW+cx]) - int32(cd.cbPredArr[(cRow+r)*8+cCol+c])
				crBlock[r*4+c] = int32(e.f.Cr[cy*e.chromaW+cx]) - int32(cd.crPredArr[(cRow+r)*8+cCol+c])
			}
		}
		cbTrans := forwardDCT4x4(cbBlock)
		crTrans := forwardDCT4x4(crBlock)
		cbDCCoeffs[cIdx] = cbTrans[0]
		crDCCoeffs[cIdx] = crTrans[0]

		cbQuantAC := quantize4x4(cbTrans, e.chromaQP)
		crQuantAC := quantize4x4(crTrans, e.chromaQP)
		for i := 1; i < 16; i++ {
			cd.cbACBlocks[cIdx][i-1] = cbQuantAC[zigzag4x4[i]]
			cd.crACBlocks[cIdx][i-1] = crQuantAC[zigzag4x4[i]]
			if cbQuantAC[zigzag4x4[i]] != 0 || crQuantAC[zigzag4x4[i]] != 0 {
				hasChromaAC = true
			}
		}
	}

	cd.cbDCQ = quantizeChromaDC2x2(forwardHadamard2x2(cbDCCoeffs), e.qp)
	cd.crDCQ = quantizeChromaDC2x2(forwardHadamard2x2(crDCCoeffs), e.qp)

	for i := 0; i < 4; i++ {
		if cd.cbDCQ[i] != 0 || cd.crDCQ[i] != 0 {
			hasChromaDC = true
			break
		}
	}

	if hasChromaAC {
		cd.chromaCBP = 2
	} else if hasChromaDC {
		cd.chromaCBP = 1
	}
	return cd
}

// writeChromaBitstream encodes chroma DC and AC coefficients into the bitstream.
func (e *mbEncoder) writeChromaBitstream(cd *chromaData, mbX, mbY int) {
	if cd.chromaCBP > 0 {
		encodeChromaDCBlock(e.bw, cd.cbDCQ[:])
		encodeChromaDCBlock(e.bw, cd.crDCQ[:])
	}

	if cd.chromaCBP == 2 {
		for cIdx := 0; cIdx < 4; cIdx++ {
			blkRow := cIdx / 2
			blkCol := cIdx % 2
			nC := computeChromaNC(e.cbTCGrid, e.chromaGridW, mbX, mbY, blkRow, blkCol)
			tc := encodeCAVLCBlock(e.bw, cd.cbACBlocks[cIdx][:], nC, 15)
			e.cbTCGrid[(mbY*2+blkRow)*e.chromaGridW+mbX*2+blkCol] = tc
		}
		for cIdx := 0; cIdx < 4; cIdx++ {
			blkRow := cIdx / 2
			blkCol := cIdx % 2
			nC := computeChromaNC(e.crTCGrid, e.chromaGridW, mbX, mbY, blkRow, blkCol)
			tc := encodeCAVLCBlock(e.bw, cd.crACBlocks[cIdx][:], nC, 15)
			e.crTCGrid[(mbY*2+blkRow)*e.chromaGridW+mbX*2+blkCol] = tc
		}
	} else {
		for cIdx := 0; cIdx < 4; cIdx++ {
			blkRow := cIdx / 2
			blkCol := cIdx % 2
			e.cbTCGrid[(mbY*2+blkRow)*e.chromaGridW+mbX*2+blkCol] = 0
			e.crTCGrid[(mbY*2+blkRow)*e.chromaGridW+mbX*2+blkCol] = 0
		}
	}
}

// reconstructChroma simulates the decoder's chroma reconstruction (§8.5.7, §8.5.12).
func (e *mbEncoder) reconstructChroma(cd *chromaData, mbX, mbY int) {
	if cd.chromaCBP > 0 {
		invCbHad := invHadamard2x2(cd.cbDCQ)
		invCrHad := invHadamard2x2(cd.crDCQ)
		scaledCb := scaleChromaDC2x2(invCbHad, e.qp)
		scaledCr := scaleChromaDC2x2(invCrHad, e.qp)

		for cIdx := 0; cIdx < 4; cIdx++ {
			cRow := (cIdx / 2) * 4
			cCol := (cIdx % 2) * 4
			var cbRecon, crRecon [16]int32
			cbRecon[0] = scaledCb[cIdx]
			crRecon[0] = scaledCr[cIdx]

			if cd.chromaCBP == 2 {
				var cbACRaster, crACRaster [16]int32
				for j := 1; j < 16; j++ {
					cbACRaster[zigzag4x4[j]] = cd.cbACBlocks[cIdx][j-1]
					crACRaster[zigzag4x4[j]] = cd.crACBlocks[cIdx][j-1]
				}
				invCbAC := invQuantize4x4(cbACRaster, e.chromaQP)
				invCrAC := invQuantize4x4(crACRaster, e.chromaQP)
				for i := 1; i < 16; i++ {
					cbRecon[i] = invCbAC[i]
					crRecon[i] = invCrAC[i]
				}
			}

			cbIDCT := inverseDCT4x4(cbRecon)
			crIDCT := inverseDCT4x4(crRecon)

			for r := 0; r < 4; r++ {
				for c := 0; c < 4; c++ {
					cbVal := int32(cd.cbPredArr[(cRow+r)*8+cCol+c]) + cbIDCT[r*4+c]
					crVal := int32(cd.crPredArr[(cRow+r)*8+cCol+c]) + crIDCT[r*4+c]
					if cbVal < 0 {
						cbVal = 0
					} else if cbVal > 255 {
						cbVal = 255
					}
					if crVal < 0 {
						crVal = 0
					} else if crVal > 255 {
						crVal = 255
					}
					e.reconCb[(mbY*8+cRow+r)*e.chromaW+mbX*8+cCol+c] = uint8(cbVal)
					e.reconCr[(mbY*8+cRow+r)*e.chromaW+mbX*8+cCol+c] = uint8(crVal)
				}
			}
		}
	} else {
		for y := 0; y < 8; y++ {
			for x := 0; x < 8; x++ {
				e.reconCb[(mbY*8+y)*e.chromaW+mbX*8+x] = cd.cbPredArr[y*8+x]
				e.reconCr[(mbY*8+y)*e.chromaW+mbX*8+x] = cd.crPredArr[y*8+x]
			}
		}
	}
}

// computeNC computes the nC prediction for a 4x4 block (§9.2.1).
// blk4Row, blk4Col are in 4x4-block units within the MB.
func computeNC(tcGrid []int, gridW, width, height, mbX, mbY, blk4Row, blk4Col int) int {
	frameRow := mbY*4 + blk4Row
	frameCol := mbX*4 + blk4Col

	hasTop := frameRow > 0
	hasLeft := frameCol > 0

	if !hasTop && !hasLeft {
		return 0
	}

	nA, nB := -1, -1
	if hasLeft {
		nA = tcGrid[frameRow*gridW+(frameCol-1)]
	}
	if hasTop {
		nB = tcGrid[(frameRow-1)*gridW+frameCol]
	}

	if nA >= 0 && nB >= 0 {
		return (nA + nB + 1) >> 1
	}
	if nA >= 0 {
		return nA
	}
	return nB
}

// computeChromaNC computes the nC prediction for a chroma 4x4 block (§9.2.1).
// Uses chroma-resolution grid (2 blocks per MB dimension for 4:2:0).
func computeChromaNC(tcGrid []int, gridW, mbX, mbY, blkRow, blkCol int) int {
	row := mbY*2 + blkRow
	col := mbX*2 + blkCol
	hasTop := row > 0
	hasLeft := col > 0
	if !hasTop && !hasLeft {
		return 0
	}
	nA, nB := -1, -1
	if hasLeft {
		nA = tcGrid[row*gridW+(col-1)]
	}
	if hasTop {
		nB = tcGrid[(row-1)*gridW+col]
	}
	if nA >= 0 && nB >= 0 {
		return (nA + nB + 1) >> 1
	}
	if nA >= 0 {
		return nA
	}
	return nB
}

// reconDCPred computes the DC prediction value from reconstructed neighbors.
// Mode 2 (DC): average of left column and/or top row from the reconstruction buffer.
func reconDCPred(reconY []uint8, width, height, mbX, mbY int) uint8 {
	hasTop := mbY > 0
	hasLeft := mbX > 0

	if !hasTop && !hasLeft {
		return 128
	}

	sum := 0
	count := 0
	if hasTop {
		topY := mbY*16 - 1
		for x := 0; x < 16; x++ {
			sum += int(reconY[topY*width+mbX*16+x])
		}
		count += 16
	}
	if hasLeft {
		leftX := mbX*16 - 1
		for y := 0; y < 16; y++ {
			sum += int(reconY[(mbY*16+y)*width+leftX])
		}
		count += 16
	}
	return uint8((sum + count/2) / count)
}

// lumaPredict16x16 computes the 16x16 luma prediction array for a given mode (§8.3.3).
// Returns a [256]uint8 with per-pixel predictions for the entire luma MB.
// Mode 0: Vertical (each column copies from top neighbor row).
// Mode 1: Horizontal (each row copies from left neighbor column).
// Mode 2: DC (flat value from average of top row + left column).
func lumaPredict16x16(reconY []uint8, width, height, mbX, mbY, mode int) [256]uint8 {
	var pred [256]uint8
	hasTop := mbY > 0
	hasLeft := mbX > 0

	switch mode {
	case 0: // Vertical — each column copies from top neighbor (§8.3.3.1)
		if hasTop {
			topRow := mbY*16 - 1
			for x := 0; x < 16; x++ {
				val := reconY[topRow*width+mbX*16+x]
				for y := 0; y < 16; y++ {
					pred[y*16+x] = val
				}
			}
		} else {
			for i := range pred {
				pred[i] = 128
			}
		}

	case 1: // Horizontal — each row copies from left neighbor (§8.3.3.2)
		if hasLeft {
			leftCol := mbX*16 - 1
			for y := 0; y < 16; y++ {
				val := reconY[(mbY*16+y)*width+leftCol]
				for x := 0; x < 16; x++ {
					pred[y*16+x] = val
				}
			}
		} else {
			for i := range pred {
				pred[i] = 128
			}
		}

	case 2: // DC — uniform flat value (§8.3.3.3)
		dc := reconDCPred(reconY, width, height, mbX, mbY)
		for i := range pred {
			pred[i] = dc
		}
	}

	return pred
}

// selectLumaMode picks the best luma I_16x16 prediction mode by comparing prediction
// error for DC (2), Vertical (0), and Horizontal (1). Returns the mode and the
// per-pixel prediction array.
func selectLumaMode(f *Frame, reconY []uint8, width, height, mbX, mbY int) (int, [256]uint8) {
	// Always try DC (mode 2) as baseline
	bestMode := 2
	bestPred := lumaPredict16x16(reconY, width, height, mbX, mbY, 2)
	bestErr := lumaPredError(f, bestPred, width, mbX, mbY)

	// Try Vertical (mode 0) if top neighbor available
	if mbY > 0 {
		vPred := lumaPredict16x16(reconY, width, height, mbX, mbY, 0)
		vErr := lumaPredError(f, vPred, width, mbX, mbY)
		if vErr < bestErr {
			bestMode, bestPred, bestErr = 0, vPred, vErr
		}
	}

	// Try Horizontal (mode 1) if left neighbor available
	if mbX > 0 {
		hPred := lumaPredict16x16(reconY, width, height, mbX, mbY, 1)
		hErr := lumaPredError(f, hPred, width, mbX, mbY)
		if hErr < bestErr {
			bestMode, bestPred = 1, hPred
		}
	}

	return bestMode, bestPred
}

// lumaPredError computes the sum of absolute differences between the original
// luma pixels and the prediction array for a 16x16 macroblock.
func lumaPredError(f *Frame, pred [256]uint8, width, mbX, mbY int) int {
	total := 0
	for y := 0; y < 16; y++ {
		for x := 0; x < 16; x++ {
			orig := int(f.Y[(mbY*16+y)*width+mbX*16+x])
			diff := orig - int(pred[y*16+x])
			if diff < 0 {
				diff = -diff
			}
			total += diff
		}
	}
	return total
}

// --- Intra_4x4 Prediction ---

// gatherRef4x4 extracts the 13 reference samples for I_4x4 prediction from the
// reconstruction buffer. Layout: [L3, L2, L1, L0, TL, T0, T1, T2, T3, T4, T5, T6, T7]
// where L=left column (L0=top, L3=bottom), TL=top-left diagonal,
// T=top row, T4-T7=top-right extension (§8.3.1.2).
func gatherRef4x4(reconY []uint8, width, height, mbX, mbY int, blkIdx, mbCols int) (ref [13]uint8, leftAvail, topAvail bool) {
	blkRow := blk4x4Grid[blkIdx][0] // 0-3 in 4x4-block units within MB
	blkCol := blk4x4Grid[blkIdx][1]

	// Absolute pixel position of block's top-left corner
	px := mbX*16 + blkCol*4
	py := mbY*16 + blkRow*4

	leftAvail = px > 0
	topAvail = py > 0

	// Left samples: L0=ref[3], L1=ref[2], L2=ref[1], L3=ref[0]
	if leftAvail {
		for i := 0; i < 4; i++ {
			ref[3-i] = reconY[(py+i)*width+(px-1)]
		}
	} else {
		for i := 0; i < 4; i++ {
			ref[i] = 128
		}
	}

	// Top-left sample TL=ref[4]
	tlAvail := leftAvail && topAvail
	if tlAvail {
		ref[4] = reconY[(py-1)*width+(px-1)]
	} else if topAvail {
		ref[4] = reconY[(py-1)*width+px]
	} else if leftAvail {
		ref[4] = reconY[py*width+(px-1)]
	} else {
		ref[4] = 128
	}

	// Top samples T0-T3: ref[5]-ref[8]
	if topAvail {
		for i := 0; i < 4; i++ {
			ref[5+i] = reconY[(py-1)*width+(px+i)]
		}
	} else {
		for i := 0; i < 4; i++ {
			ref[5+i] = 128
		}
	}

	// Top-right samples T4-T7: ref[9]-ref[12]
	// Availability depends on whether the 4x4 block at (blkRow-1, blkCol+1)
	// has been reconstructed (earlier in Z-scan or in a previous MB).
	topRightAvail := false
	if topAvail {
		trPixCol := px + 4
		if trPixCol+3 < width { // top-right pixels exist in frame
			if blkRow == 0 {
				// Top row of MB: top-right is in the MB row above
				if blkCol < 3 {
					topRightAvail = true // in MB directly above (already encoded)
				} else {
					// blkCol==3: top-right is in MB above-right
					topRightAvail = mbX+1 < mbCols
				}
			} else {
				// Interior rows: check if top-right block is earlier in Z-scan
				trC := blkCol + 1
				if trC <= 3 {
					topRightAvail = mbGridToBlkIdx[blkRow-1][trC] < blkIdx
				}
				// trC > 3: in MB to the right, not yet encoded
			}
		}
	}

	if topRightAvail {
		trCol := px + 4
		for i := 0; i < 4; i++ {
			ref[9+i] = reconY[(py-1)*width+(trCol+i)]
		}
	} else if topAvail {
		// §8.3.1.2.1: fill T4-T7 with T3 when top-right unavailable
		for i := 0; i < 4; i++ {
			ref[9+i] = ref[8]
		}
	} else {
		for i := 0; i < 4; i++ {
			ref[9+i] = 128
		}
	}

	return
}

// predict4x4 computes the 4×4 prediction for a given intra mode (§8.3.1.2).
// ref layout: [L3, L2, L1, L0, TL, T0, T1, T2, T3, T4, T5, T6, T7]
// Returns flat [16]uint8 array (row-major: pred[y*4+x]).
func predict4x4(ref [13]uint8, mode int, leftAvail, topAvail bool) [16]uint8 {
	// Named reference samples: L[i]=p[-1,i], T[i]=p[i,-1], TL=p[-1,-1]
	L := [4]int{int(ref[3]), int(ref[2]), int(ref[1]), int(ref[0])}
	TL := int(ref[4])
	T := [8]int{int(ref[5]), int(ref[6]), int(ref[7]), int(ref[8]),
		int(ref[9]), int(ref[10]), int(ref[11]), int(ref[12])}

	clip := func(v int) uint8 {
		if v < 0 {
			return 0
		}
		if v > 255 {
			return 255
		}
		return uint8(v)
	}

	// Helper: p[x, -1] where x=-1 maps to TL (H.264 §6.4.12)
	sT := func(x int) int {
		if x < 0 {
			return TL
		}
		return T[x]
	}
	// Helper: p[-1, y] where y=-1 maps to TL
	sL := func(y int) int {
		if y < 0 {
			return TL
		}
		return L[y]
	}

	var pred [16]uint8

	switch mode {
	case 0: // Vertical (§8.3.1.2.2): pred[y][x] = T[x]
		for y := 0; y < 4; y++ {
			for x := 0; x < 4; x++ {
				pred[y*4+x] = uint8(T[x])
			}
		}

	case 1: // Horizontal (§8.3.1.2.3): pred[y][x] = L[y]
		for y := 0; y < 4; y++ {
			for x := 0; x < 4; x++ {
				pred[y*4+x] = uint8(L[y])
			}
		}

	case 2: // DC (§8.3.1.2.4): average of available neighbors
		var dc int
		switch {
		case topAvail && leftAvail:
			dc = (T[0] + T[1] + T[2] + T[3] + L[0] + L[1] + L[2] + L[3] + 4) >> 3
		case topAvail:
			dc = (T[0] + T[1] + T[2] + T[3] + 2) >> 2
		case leftAvail:
			dc = (L[0] + L[1] + L[2] + L[3] + 2) >> 2
		default:
			dc = 128
		}
		for i := range pred {
			pred[i] = uint8(dc)
		}

	case 3: // Diagonal Down-Left (§8.3.1.2.5)
		for y := 0; y < 4; y++ {
			for x := 0; x < 4; x++ {
				if x == 3 && y == 3 {
					pred[y*4+x] = clip((T[6] + 3*T[7] + 2) >> 2)
				} else {
					pred[y*4+x] = clip((T[x+y] + 2*T[x+y+1] + T[x+y+2] + 2) >> 2)
				}
			}
		}

	case 4: // Diagonal Down-Right (§8.3.1.2.6)
		// Uses sT/sL helpers: when x-y-2=-1 or y-x-2=-1, maps to TL
		for y := 0; y < 4; y++ {
			for x := 0; x < 4; x++ {
				if x > y {
					pred[y*4+x] = clip((sT(x-y-2) + 2*sT(x-y-1) + T[x-y] + 2) >> 2)
				} else if x < y {
					pred[y*4+x] = clip((sL(y-x-2) + 2*sL(y-x-1) + L[y-x] + 2) >> 2)
				} else { // x == y: diagonal corner
					pred[y*4+x] = clip((T[0] + 2*TL + L[0] + 2) >> 2)
				}
			}
		}

	case 5: // Vertical-Right (§8.3.1.2.7)
		for y := 0; y < 4; y++ {
			for x := 0; x < 4; x++ {
				zVR := 2*x - y
				switch {
				case zVR >= 0 && zVR%2 == 0: // even non-negative
					idx := x - (y >> 1)
					if idx == 0 {
						pred[y*4+x] = clip((TL + T[0] + 1) >> 1)
					} else {
						pred[y*4+x] = clip((T[idx-1] + T[idx] + 1) >> 1)
					}
				case zVR >= 1: // odd positive
					idx := x - (y >> 1)
					pred[y*4+x] = clip((sT(idx-2) + 2*sT(idx-1) + T[idx] + 2) >> 2)
				case zVR == -1:
					pred[y*4+x] = clip((L[0] + 2*TL + T[0] + 2) >> 2)
				default: // zVR < -1 (zVR=-2,-3)
					pred[y*4+x] = clip((sL(y-1) + 2*sL(y-2) + sL(y-3) + 2) >> 2)
				}
			}
		}

	case 6: // Horizontal-Down (§8.3.1.2.8)
		for y := 0; y < 4; y++ {
			for x := 0; x < 4; x++ {
				zHD := 2*y - x
				switch {
				case zHD >= 0 && zHD%2 == 0: // even non-negative
					idx := y - (x >> 1)
					if idx == 0 {
						pred[y*4+x] = clip((TL + L[0] + 1) >> 1)
					} else {
						pred[y*4+x] = clip((L[idx-1] + L[idx] + 1) >> 1)
					}
				case zHD >= 1: // odd positive
					idx := y - (x >> 1)
					pred[y*4+x] = clip((sL(idx-2) + 2*sL(idx-1) + L[idx] + 2) >> 2)
				case zHD == -1:
					pred[y*4+x] = clip((T[0] + 2*TL + L[0] + 2) >> 2)
				default: // zHD < -1 (zHD=-2,-3)
					pred[y*4+x] = clip((sT(x-1) + 2*sT(x-2) + sT(x-3) + 2) >> 2)
				}
			}
		}

	case 7: // Vertical-Left (§8.3.1.2.9)
		for y := 0; y < 4; y++ {
			for x := 0; x < 4; x++ {
				idx := x + (y >> 1)
				if y%2 == 0 {
					pred[y*4+x] = clip((T[idx] + T[idx+1] + 1) >> 1)
				} else {
					pred[y*4+x] = clip((T[idx] + 2*T[idx+1] + T[idx+2] + 2) >> 2)
				}
			}
		}

	case 8: // Horizontal-Up (§8.3.1.2.10)
		for y := 0; y < 4; y++ {
			for x := 0; x < 4; x++ {
				zHU := x + 2*y
				switch {
				case zHU <= 4 && zHU%2 == 0: // zHU=0,2,4
					idx := y + (x >> 1)
					pred[y*4+x] = clip((L[idx] + L[idx+1] + 1) >> 1)
				case zHU <= 3 && zHU%2 == 1: // zHU=1,3
					idx := y + (x >> 1)
					pred[y*4+x] = clip((L[idx] + 2*L[idx+1] + L[idx+2] + 2) >> 2)
				case zHU == 5:
					pred[y*4+x] = clip((L[2] + 3*L[3] + 2) >> 2)
				default: // zHU > 5
					pred[y*4+x] = uint8(L[3])
				}
			}
		}
	}

	return pred
}

// intra4x4ModeAvail checks if a given I_4x4 prediction mode can be used
// given the availability of left and top neighbors.
func intra4x4ModeAvail(mode int, leftAvail, topAvail bool) bool {
	switch mode {
	case 0, 3, 7: // Vertical, DDL, VL: need top
		return topAvail
	case 1, 8: // Horizontal, HU: need left
		return leftAvail
	case 2: // DC: always available
		return true
	case 4, 5, 6: // DDR, VR, HD: need both
		return topAvail && leftAvail
	}
	return false
}

// sad4x4 computes the sum of absolute differences between two 4×4 blocks.
func sad4x4(src, pred [16]uint8) int {
	sad := 0
	for i := 0; i < 16; i++ {
		d := int(src[i]) - int(pred[i])
		if d < 0 {
			d = -d
		}
		sad += d
	}
	return sad
}

// selectIntra4x4Mode tries all available I_4x4 prediction modes for one 4×4 block
// and returns the mode with the lowest SAD against the source pixels.
func selectIntra4x4Mode(f *Frame, reconY []uint8, width, height, mbX, mbY int, blkIdx, mbCols int) (int, [16]uint8) {
	ref, leftAvail, topAvail := gatherRef4x4(reconY, width, height, mbX, mbY, blkIdx, mbCols)

	blkRow := blk4x4Grid[blkIdx][0]
	blkCol := blk4x4Grid[blkIdx][1]
	px := mbX*16 + blkCol*4
	py := mbY*16 + blkRow*4

	// Source pixels for this 4×4 block
	var src [16]uint8
	for r := 0; r < 4; r++ {
		for c := 0; c < 4; c++ {
			src[r*4+c] = f.Y[(py+r)*width+(px+c)]
		}
	}

	// Start with DC (mode 2, always available)
	bestMode := 2
	bestPred := predict4x4(ref, 2, leftAvail, topAvail)
	bestSAD := sad4x4(src, bestPred)

	// Try all other modes
	for mode := 0; mode <= 8; mode++ {
		if mode == 2 {
			continue
		}
		if !intra4x4ModeAvail(mode, leftAvail, topAvail) {
			continue
		}
		pred := predict4x4(ref, mode, leftAvail, topAvail)
		sad := sad4x4(src, pred)
		if sad < bestSAD {
			bestMode = mode
			bestPred = pred
			bestSAD = sad
		}
	}

	return bestMode, bestPred
}

// derivePredIntra4x4Mode computes the predicted mode for I_4x4 mode signaling (§8.3.1.1).
// Returns min(modeA, modeB) where unavailable/I_16x16 neighbors default to DC (2).
func derivePredIntra4x4Mode(modeA, modeB int) int {
	if modeA < 0 {
		modeA = 2
	}
	if modeB < 0 {
		modeB = 2
	}
	if modeA < modeB {
		return modeA
	}
	return modeB
}

// chromaPredict8x8 computes the 8x8 chroma prediction array for a given mode (§8.3.4).
// Returns a [64]uint8 with per-pixel predictions for the entire chroma MB.
// Mode 0: DC (per-sub-block averages), Mode 1: Horizontal, Mode 2: Vertical.
func chromaPredict8x8(reconC []uint8, chromaW, mbX, mbY, mode int) [64]uint8 {
	var pred [64]uint8
	hasTop := mbY > 0
	hasLeft := mbX > 0

	switch mode {
	case 0: // DC — per-sub-block prediction (§8.3.4.1)
		var top, left [8]int
		if hasTop {
			topRow := mbY*8 - 1
			for x := 0; x < 8; x++ {
				top[x] = int(reconC[topRow*chromaW+mbX*8+x])
			}
		}
		if hasLeft {
			leftCol := mbX*8 - 1
			for y := 0; y < 8; y++ {
				left[y] = int(reconC[(mbY*8+y)*chromaW+leftCol])
			}
		}
		var dc [4]uint8
		// TL
		switch {
		case hasTop && hasLeft:
			dc[0] = uint8((top[0] + top[1] + top[2] + top[3] + left[0] + left[1] + left[2] + left[3] + 4) / 8)
		case hasTop:
			dc[0] = uint8((top[0] + top[1] + top[2] + top[3] + 2) / 4)
		case hasLeft:
			dc[0] = uint8((left[0] + left[1] + left[2] + left[3] + 2) / 4)
		default:
			dc[0] = 128
		}
		// TR
		switch {
		case hasTop:
			dc[1] = uint8((top[4] + top[5] + top[6] + top[7] + 2) / 4)
		case hasLeft:
			dc[1] = uint8((left[0] + left[1] + left[2] + left[3] + 2) / 4)
		default:
			dc[1] = 128
		}
		// BL
		switch {
		case hasLeft:
			dc[2] = uint8((left[4] + left[5] + left[6] + left[7] + 2) / 4)
		case hasTop:
			dc[2] = uint8((top[0] + top[1] + top[2] + top[3] + 2) / 4)
		default:
			dc[2] = 128
		}
		// BR
		switch {
		case hasTop && hasLeft:
			dc[3] = uint8((top[4] + top[5] + top[6] + top[7] + left[4] + left[5] + left[6] + left[7] + 4) / 8)
		case hasTop:
			dc[3] = uint8((top[4] + top[5] + top[6] + top[7] + 2) / 4)
		case hasLeft:
			dc[3] = uint8((left[4] + left[5] + left[6] + left[7] + 2) / 4)
		default:
			dc[3] = 128
		}
		// Fill each 4x4 sub-block with its DC value
		for blk := 0; blk < 4; blk++ {
			x0 := (blk % 2) * 4
			y0 := (blk / 2) * 4
			for y := 0; y < 4; y++ {
				for x := 0; x < 4; x++ {
					pred[(y0+y)*8+x0+x] = dc[blk]
				}
			}
		}

	case 1: // Horizontal — each row copies from left neighbor (§8.3.4.3)
		if hasLeft {
			leftCol := mbX*8 - 1
			for y := 0; y < 8; y++ {
				val := reconC[(mbY*8+y)*chromaW+leftCol]
				for x := 0; x < 8; x++ {
					pred[y*8+x] = val
				}
			}
		} else {
			for i := range pred {
				pred[i] = 128
			}
		}

	case 2: // Vertical — each column copies from top neighbor (§8.3.4.2)
		if hasTop {
			topRow := mbY*8 - 1
			for x := 0; x < 8; x++ {
				val := reconC[topRow*chromaW+mbX*8+x]
				for y := 0; y < 8; y++ {
					pred[y*8+x] = val
				}
			}
		} else {
			for i := range pred {
				pred[i] = 128
			}
		}
	}

	return pred
}

// selectChromaMode picks the best chroma prediction mode by comparing prediction
// error for DC (0), Vertical (2), and Horizontal (1). Returns the mode and the
// per-pixel prediction arrays for Cb and Cr.
func selectChromaMode(f *Frame, reconCb, reconCr []uint8, chromaW, mbX, mbY int) (int, [64]uint8, [64]uint8) {
	// Always try DC (mode 0) as baseline
	bestMode := 0
	bestCbPred := chromaPredict8x8(reconCb, chromaW, mbX, mbY, 0)
	bestCrPred := chromaPredict8x8(reconCr, chromaW, mbX, mbY, 0)
	bestErr := chromaPredError(f, bestCbPred, bestCrPred, chromaW, mbX, mbY)

	// Try Vertical (mode 2) if top neighbor available
	if mbY > 0 {
		vCb := chromaPredict8x8(reconCb, chromaW, mbX, mbY, 2)
		vCr := chromaPredict8x8(reconCr, chromaW, mbX, mbY, 2)
		vErr := chromaPredError(f, vCb, vCr, chromaW, mbX, mbY)
		if vErr < bestErr {
			bestMode, bestCbPred, bestCrPred, bestErr = 2, vCb, vCr, vErr
		}
	}

	// Try Horizontal (mode 1) if left neighbor available
	if mbX > 0 {
		hCb := chromaPredict8x8(reconCb, chromaW, mbX, mbY, 1)
		hCr := chromaPredict8x8(reconCr, chromaW, mbX, mbY, 1)
		hErr := chromaPredError(f, hCb, hCr, chromaW, mbX, mbY)
		if hErr < bestErr {
			bestMode, bestCbPred, bestCrPred = 1, hCb, hCr
		}
	}

	return bestMode, bestCbPred, bestCrPred
}

// chromaPredError computes the sum of absolute differences between the original
// chroma pixels and the prediction array for both Cb and Cr.
func chromaPredError(f *Frame, cbPred, crPred [64]uint8, chromaW, mbX, mbY int) int {
	total := 0
	for y := 0; y < 8; y++ {
		for x := 0; x < 8; x++ {
			cy := mbY*8 + y
			cx := mbX*8 + x
			cbOrig := int(f.Cb[cy*chromaW+cx])
			crOrig := int(f.Cr[cy*chromaW+cx])
			cbDiff := cbOrig - int(cbPred[y*8+x])
			crDiff := crOrig - int(crPred[y*8+x])
			if cbDiff < 0 {
				cbDiff = -cbDiff
			}
			if crDiff < 0 {
				crDiff = -crDiff
			}
			total += cbDiff + crDiff
		}
	}
	return total
}

// --- Inverse quantization and transforms (decoder simulation) ---

// Inverse quantization scaling factors (ITU-T H.264 Table 8-14).
var levelScale4x4 = [6][3]int32{
	{10, 13, 16}, {11, 14, 18}, {13, 16, 20},
	{14, 18, 23}, {16, 20, 25}, {18, 23, 29},
}

// scaleDC4x4 applies inverse quantization to the inverse-Hadamard output (§8.5.6).
// MUST be called AFTER invHadamard4x4, not before. The shift includes the >>2
// normalization for the 4x4 Hadamard round-trip.
func scaleDC4x4(hadOut [16]int32, qp int) [16]int32 {
	qpRem := qp % 6
	qpPer := qp / 6
	scale := levelScale4x4[qpRem][0]
	var out [16]int32
	if qpPer >= 2 {
		for i := 0; i < 16; i++ {
			out[i] = hadOut[i] * scale << (qpPer - 2)
		}
	} else {
		round := int32(1 << (1 - qpPer))
		for i := 0; i < 16; i++ {
			out[i] = (hadOut[i]*scale + round) >> (2 - qpPer)
		}
	}
	return out
}

// scaleChromaDC2x2 applies inverse quantization to the inverse-Hadamard 2x2 output (§8.5.7).
func scaleChromaDC2x2(hadOut [4]int32, qp int) [4]int32 {
	chromaQP := chromaQPFromLuma(qp)
	qpRem := chromaQP % 6
	qpPer := chromaQP / 6
	scale := levelScale4x4[qpRem][0]
	var out [4]int32
	if qpPer >= 1 {
		for i := 0; i < 4; i++ {
			out[i] = hadOut[i] * scale << (qpPer - 1)
		}
	} else {
		for i := 0; i < 4; i++ {
			out[i] = (hadOut[i] * scale) >> 1
		}
	}
	return out
}

// invQuantize4x4 performs inverse quantization of a 4x4 block (for AC coefficients).
func invQuantize4x4(coeffs [16]int32, qp int) [16]int32 {
	qpRem := qp % 6
	qpPer := qp / 6
	var out [16]int32
	for i := 0; i < 16; i++ {
		if coeffs[i] == 0 {
			continue
		}
		row, col := i/4, i%4
		scale := levelScale4x4[qpRem][qScaleIdx(row, col)]
		out[i] = (coeffs[i] * scale) << qpPer
	}
	return out
}

// invHadamard4x4 computes the inverse 4x4 Hadamard transform.
// Matches H.264 spec §8.5.10 and hi264 reference implementation.
// H = [[1,1,1,1],[1,1,-1,-1],[1,-1,-1,1],[1,-1,1,-1]]
func invHadamard4x4(dc [16]int32) [16]int32 {
	var temp [16]int32
	// Row transform
	for i := 0; i < 4; i++ {
		s0, s1, s2, s3 := dc[i*4], dc[i*4+1], dc[i*4+2], dc[i*4+3]
		temp[i*4+0] = s0 + s1 + s2 + s3
		temp[i*4+1] = s0 + s1 - s2 - s3
		temp[i*4+2] = s0 - s1 - s2 + s3
		temp[i*4+3] = s0 - s1 + s2 - s3
	}
	// Column transform (no /2 normalization for inverse)
	var result [16]int32
	for j := 0; j < 4; j++ {
		f0, f1, f2, f3 := temp[j], temp[4+j], temp[8+j], temp[12+j]
		result[j] = f0 + f1 + f2 + f3
		result[4+j] = f0 + f1 - f2 - f3
		result[8+j] = f0 - f1 - f2 + f3
		result[12+j] = f0 - f1 + f2 - f3
	}
	return result
}

// invHadamard2x2 computes the inverse 2x2 Hadamard transform for chroma DC.
func invHadamard2x2(dc [4]int32) [4]int32 {
	return [4]int32{
		dc[0] + dc[1] + dc[2] + dc[3],
		dc[0] - dc[1] + dc[2] - dc[3],
		dc[0] + dc[1] - dc[2] - dc[3],
		dc[0] - dc[1] - dc[2] + dc[3],
	}
}

// inverseDCT4x4 computes the inverse 4x4 integer DCT (H.264 §8.5.12).
func inverseDCT4x4(block [16]int32) [16]int32 {
	var out [16]int32

	// Column transform (note: transposed from forward)
	for j := 0; j < 4; j++ {
		s0 := block[0*4+j]
		s1 := block[1*4+j]
		s2 := block[2*4+j]
		s3 := block[3*4+j]
		e0 := s0 + s2
		e1 := s0 - s2
		e2 := (s1 >> 1) - s3
		e3 := s1 + (s3 >> 1)
		out[0*4+j] = e0 + e3
		out[1*4+j] = e1 + e2
		out[2*4+j] = e1 - e2
		out[3*4+j] = e0 - e3
	}

	// Row transform
	var tmp [16]int32
	for i := 0; i < 4; i++ {
		s0 := out[i*4+0]
		s1 := out[i*4+1]
		s2 := out[i*4+2]
		s3 := out[i*4+3]
		e0 := s0 + s2
		e1 := s0 - s2
		e2 := (s1 >> 1) - s3
		e3 := s1 + (s3 >> 1)
		tmp[i*4+0] = (e0 + e3 + 32) >> 6
		tmp[i*4+1] = (e1 + e2 + 32) >> 6
		tmp[i*4+2] = (e1 - e2 + 32) >> 6
		tmp[i*4+3] = (e0 - e3 + 32) >> 6
	}
	return tmp
}

// --- Forward transforms ---

// forwardDCT4x4 computes the 4x4 integer DCT (H.264 spec §8.5.8).
func forwardDCT4x4(block [16]int32) [16]int32 {
	var out [16]int32
	// Row transform
	for i := 0; i < 4; i++ {
		s0 := block[i*4+0]
		s1 := block[i*4+1]
		s2 := block[i*4+2]
		s3 := block[i*4+3]
		p0 := s0 + s3
		p1 := s1 + s2
		p2 := s1 - s2
		p3 := s0 - s3
		out[i*4+0] = p0 + p1
		out[i*4+1] = p2 + (p3 << 1)
		out[i*4+2] = p0 - p1
		out[i*4+3] = p3 - (p2 << 1)
	}
	// Column transform
	var tmp [16]int32
	for j := 0; j < 4; j++ {
		s0 := out[0*4+j]
		s1 := out[1*4+j]
		s2 := out[2*4+j]
		s3 := out[3*4+j]
		p0 := s0 + s3
		p1 := s1 + s2
		p2 := s1 - s2
		p3 := s0 - s3
		tmp[0*4+j] = p0 + p1
		tmp[1*4+j] = p2 + (p3 << 1)
		tmp[2*4+j] = p0 - p1
		tmp[3*4+j] = p3 - (p2 << 1)
	}
	return tmp
}

// forwardHadamard4x4 computes the forward 4x4 Hadamard transform for luma DC coefficients.
// Same basis matrix as inverse, but with /2 normalization in the column pass.
// H = [[1,1,1,1],[1,1,-1,-1],[1,-1,-1,1],[1,-1,1,-1]]
func forwardHadamard4x4(dc [16]int32) [16]int32 {
	var temp [16]int32
	// Row transform (same as inverse — Hadamard is symmetric)
	for i := 0; i < 4; i++ {
		s0, s1, s2, s3 := dc[i*4], dc[i*4+1], dc[i*4+2], dc[i*4+3]
		temp[i*4+0] = s0 + s1 + s2 + s3
		temp[i*4+1] = s0 + s1 - s2 - s3
		temp[i*4+2] = s0 - s1 - s2 + s3
		temp[i*4+3] = s0 - s1 + s2 - s3
	}
	// Column transform with /2 normalization (forward only)
	var result [16]int32
	for j := 0; j < 4; j++ {
		f0, f1, f2, f3 := temp[j], temp[4+j], temp[8+j], temp[12+j]
		result[j] = (f0 + f1 + f2 + f3) / 2
		result[4+j] = (f0 + f1 - f2 - f3) / 2
		result[8+j] = (f0 - f1 - f2 + f3) / 2
		result[12+j] = (f0 - f1 + f2 - f3) / 2
	}
	return result
}

// forwardHadamard2x2 computes the 2x2 Hadamard transform for chroma DC.
func forwardHadamard2x2(dc [4]int32) [4]int32 {
	return [4]int32{
		dc[0] + dc[1] + dc[2] + dc[3],
		dc[0] - dc[1] + dc[2] - dc[3],
		dc[0] + dc[1] - dc[2] - dc[3],
		dc[0] - dc[1] - dc[2] + dc[3],
	}
}

// --- Quantization ---

// Forward quantization multiplication factors (H.264 Table 8-17).
var mf4x4 = [6][3]int32{
	{13107, 8066, 5243},
	{11916, 7490, 4660},
	{10082, 6554, 4194},
	{9362, 5825, 3647},
	{8192, 5243, 3355},
	{7282, 4559, 2893},
}

func qScaleIdx(row, col int) int {
	r, c := row%2, col%2
	if r == 0 && c == 0 {
		return 0
	}
	if r == 1 && c == 1 {
		return 2
	}
	return 1
}

func quantize4x4(coeffs [16]int32, qp int) [16]int32 {
	qpRem := qp % 6
	qBits := 15 + qp/6
	add := int32(1<<qBits) / 3 // intra rounding
	var out [16]int32
	for i := 0; i < 16; i++ {
		row, col := i/4, i%4
		c := coeffs[i]
		sign := int32(1)
		if c < 0 {
			sign = -1
			c = -c
		}
		out[i] = sign * ((c*mf4x4[qpRem][qScaleIdx(row, col)] + add) >> qBits)
	}
	return out
}

func quantizeDC4x4(coeffs [16]int32, qp int) [16]int32 {
	qpRem := qp % 6
	qBits := 15 + qp/6 + 1 // extra +1 for DC
	add := int32(1<<qBits) / 3
	var out [16]int32
	for i := 0; i < 16; i++ {
		c := coeffs[i]
		sign := int32(1)
		if c < 0 {
			sign = -1
			c = -c
		}
		out[i] = sign * ((c*mf4x4[qpRem][0] + add) >> qBits)
	}
	return out
}

func quantizeChromaDC2x2(coeffs [4]int32, qp int) [4]int32 {
	chromaQP := chromaQPFromLuma(qp)
	qpRem := chromaQP % 6
	qBits := 15 + chromaQP/6 + 1
	add := int32(1<<qBits) / 3
	var out [4]int32
	for i := 0; i < 4; i++ {
		c := coeffs[i]
		sign := int32(1)
		if c < 0 {
			sign = -1
			c = -c
		}
		out[i] = sign * ((c*mf4x4[qpRem][0] + add) >> qBits)
	}
	return out
}

// chromaQPFromLuma maps luma QP to chroma QP (H.264 Table 8-15).
func chromaQPFromLuma(qpY int) int {
	if qpY < 30 {
		return qpY
	}
	idx := qpY - 30
	if idx >= len(chromaQPTable) {
		return chromaQPTable[len(chromaQPTable)-1]
	}
	return chromaQPTable[idx]
}

// Zigzag scan order for 4x4 blocks (H.264 spec).
var zigzag4x4 = [16]int{0, 1, 4, 8, 5, 2, 3, 6, 9, 12, 13, 10, 7, 11, 14, 15}

// --- CAVLC Entropy Coding ---

// CAVLC coeff_token tables (H.264 Table 9-5).
// Indexed as [tableIdx][4*totalCoeff + trailingOnes].
var cavlcCoeffTokenLen = [4][68]uint8{
	// nC = 0,1
	{1, 0, 0, 0, 6, 2, 0, 0, 8, 6, 3, 0, 9, 8, 7, 5, 10, 9, 8, 6,
		11, 10, 9, 7, 13, 11, 10, 8, 13, 13, 11, 9, 13, 13, 13, 10,
		14, 14, 13, 11, 14, 14, 14, 13, 15, 15, 14, 14, 15, 15, 15, 14,
		16, 15, 15, 15, 16, 16, 16, 15, 16, 16, 16, 16, 16, 16, 16, 16},
	// nC = 2,3
	{2, 0, 0, 0, 6, 2, 0, 0, 6, 5, 3, 0, 7, 6, 6, 4, 8, 6, 6, 4,
		8, 7, 7, 5, 9, 8, 8, 6, 11, 9, 9, 6, 11, 11, 11, 7,
		12, 11, 11, 9, 12, 12, 12, 11, 12, 12, 12, 11, 13, 13, 13, 12,
		13, 13, 13, 13, 13, 14, 13, 13, 14, 14, 14, 13, 14, 14, 14, 14},
	// nC = 4-7
	{4, 0, 0, 0, 6, 4, 0, 0, 6, 5, 4, 0, 6, 5, 5, 4, 7, 5, 5, 4,
		7, 5, 5, 4, 7, 6, 6, 4, 7, 6, 6, 4, 8, 7, 7, 5,
		8, 8, 7, 6, 9, 8, 8, 7, 9, 9, 8, 8, 9, 9, 9, 8,
		10, 9, 9, 9, 10, 10, 10, 10, 10, 10, 10, 10, 10, 10, 10, 10},
	// nC >= 8
	{6, 0, 0, 0, 6, 6, 0, 0, 6, 6, 6, 0, 6, 6, 6, 6, 6, 6, 6, 6,
		6, 6, 6, 6, 6, 6, 6, 6, 6, 6, 6, 6, 6, 6, 6, 6,
		6, 6, 6, 6, 6, 6, 6, 6, 6, 6, 6, 6, 6, 6, 6, 6,
		6, 6, 6, 6, 6, 6, 6, 6, 6, 6, 6, 6, 6, 6, 6, 6},
}

var cavlcCoeffTokenBits = [4][68]uint8{
	{1, 0, 0, 0, 5, 1, 0, 0, 7, 4, 1, 0, 7, 6, 5, 3, 7, 6, 5, 3,
		7, 6, 5, 4, 15, 6, 5, 4, 11, 14, 5, 4, 8, 10, 13, 4,
		15, 14, 9, 4, 11, 10, 13, 12, 15, 14, 9, 12, 11, 10, 13, 8,
		15, 1, 9, 12, 11, 14, 13, 8, 7, 10, 9, 12, 4, 6, 5, 8},
	{3, 0, 0, 0, 11, 2, 0, 0, 7, 7, 3, 0, 7, 10, 9, 5, 7, 6, 5, 4,
		4, 6, 5, 6, 7, 6, 5, 8, 15, 6, 5, 4, 11, 14, 13, 4,
		15, 10, 9, 4, 11, 14, 13, 12, 8, 10, 9, 8, 15, 14, 13, 12,
		11, 10, 9, 12, 7, 11, 6, 8, 9, 8, 10, 1, 7, 6, 5, 4},
	{15, 0, 0, 0, 15, 14, 0, 0, 11, 15, 13, 0, 8, 12, 14, 12, 15, 10, 11, 11,
		11, 8, 9, 10, 9, 14, 13, 9, 8, 10, 9, 8, 15, 14, 13, 13,
		11, 14, 10, 12, 15, 10, 13, 12, 11, 14, 9, 12, 8, 10, 13, 8,
		13, 7, 9, 12, 9, 12, 11, 10, 5, 8, 7, 6, 1, 4, 3, 2},
	{3, 0, 0, 0, 0, 1, 0, 0, 4, 5, 6, 0, 8, 9, 10, 11, 12, 13, 14, 15,
		16, 17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29, 30, 31,
		32, 33, 34, 35, 36, 37, 38, 39, 40, 41, 42, 43, 44, 45, 46, 47,
		48, 49, 50, 51, 52, 53, 54, 55, 56, 57, 58, 59, 60, 61, 62, 63},
}

// Chroma DC coeff_token (nC = -1, maxNumCoeff = 4).
var chromaDCCoeffTokenLen = [20]uint8{
	2, 0, 0, 0, 6, 1, 0, 0, 6, 6, 3, 0, 6, 7, 7, 6, 6, 8, 8, 7,
}
var chromaDCCoeffTokenBits = [20]uint8{
	1, 0, 0, 0, 7, 1, 0, 0, 4, 6, 1, 0, 3, 3, 2, 5, 2, 3, 2, 0,
}

// total_zeros tables (H.264 Table 9-7).
var totalZerosLen = [15][16]uint8{
	{1, 3, 3, 4, 4, 5, 5, 6, 6, 7, 7, 8, 8, 9, 9, 9},
	{3, 3, 3, 3, 3, 4, 4, 4, 4, 5, 5, 6, 6, 6, 6},
	{4, 3, 3, 3, 4, 4, 3, 3, 4, 5, 5, 6, 5, 6},
	{5, 3, 4, 4, 3, 3, 3, 4, 3, 4, 5, 5, 5},
	{4, 4, 4, 3, 3, 3, 3, 3, 4, 5, 4, 5},
	{6, 5, 3, 3, 3, 3, 3, 3, 4, 3, 6},
	{6, 5, 3, 3, 3, 2, 3, 4, 3, 6},
	{6, 4, 5, 3, 2, 2, 3, 3, 6},
	{6, 6, 4, 2, 2, 3, 2, 5},
	{5, 5, 3, 2, 2, 2, 4},
	{4, 4, 3, 3, 1, 3},
	{4, 4, 2, 1, 3},
	{3, 3, 1, 2},
	{2, 2, 1},
	{1, 1},
}
var totalZerosBits = [15][16]uint8{
	{1, 3, 2, 3, 2, 3, 2, 3, 2, 3, 2, 3, 2, 3, 2, 1},
	{7, 6, 5, 4, 3, 5, 4, 3, 2, 3, 2, 3, 2, 1, 0},
	{5, 7, 6, 5, 4, 3, 4, 3, 2, 3, 2, 1, 1, 0},
	{3, 7, 5, 4, 6, 5, 4, 3, 3, 2, 2, 1, 0},
	{5, 4, 3, 7, 6, 5, 4, 3, 2, 1, 1, 0},
	{1, 1, 7, 6, 5, 4, 3, 2, 1, 1, 0},
	{1, 1, 5, 4, 3, 3, 2, 1, 1, 0},
	{1, 1, 1, 3, 3, 2, 2, 1, 0},
	{1, 0, 1, 3, 2, 1, 1, 1},
	{1, 0, 1, 3, 2, 1, 1},
	{0, 1, 1, 2, 1, 3},
	{0, 1, 1, 1, 1},
	{0, 1, 1, 1},
	{0, 1, 1},
	{0, 1},
}

// Chroma DC total_zeros (Table 9-9).
var chromaDCTotalZerosLen = [3][4]uint8{
	{1, 2, 3, 3}, {1, 2, 2}, {1, 1},
}
var chromaDCTotalZerosBits = [3][4]uint8{
	{1, 1, 1, 0}, {1, 1, 0}, {1, 0},
}

// run_before tables (H.264 Table 9-10).
var runBeforeLen = [7][16]uint8{
	{1, 1}, {1, 2, 2}, {2, 2, 2, 2}, {2, 2, 2, 3, 3},
	{2, 2, 3, 3, 3, 3}, {2, 3, 3, 3, 3, 3, 3},
	{3, 3, 3, 3, 3, 3, 3, 4, 5, 6, 7, 8, 9, 10, 11},
}
var runBeforeBits = [7][16]uint8{
	{1, 0}, {1, 1, 0}, {3, 2, 1, 0}, {3, 2, 1, 1, 0},
	{3, 2, 3, 2, 1, 0}, {3, 0, 1, 3, 2, 5, 4},
	{7, 6, 5, 4, 3, 2, 1, 1, 1, 1, 1, 1, 1, 1, 1},
}

func coeffTokenTableIdx(nC int) int {
	switch {
	case nC <= 1:
		return 0
	case nC <= 3:
		return 1
	case nC <= 7:
		return 2
	default:
		return 3
	}
}

// encodeCAVLCBlock encodes a block of quantized coefficients using CAVLC.
// coeffs must be in zigzag scan order. nC is the predicted number of
// non-zero coefficients (from neighbors). maxNumCoeff is 16 for luma DC,
// 15 for luma AC, or 4 for chroma DC (use encodeChromaDCBlock for that).
// Returns totalCoeff for use as nC by neighboring blocks.
//
// Reference: ITU-T H.264 §9.2.
func encodeCAVLCBlock(bw *bitWriter, coeffs []int32, nC int, maxNumCoeff int) int {
	// Find last non-zero in scan order
	lastNZ := -1
	for i := maxNumCoeff - 1; i >= 0; i-- {
		if coeffs[i] != 0 {
			lastNZ = i
			break
		}
	}

	if lastNZ < 0 {
		// All zeros — write coeff_token for totalCoeff=0, trailingOnes=0
		tblIdx := coeffTokenTableIdx(nC)
		bw.writeBits(uint32(cavlcCoeffTokenBits[tblIdx][0]), int(cavlcCoeffTokenLen[tblIdx][0]))
		return 0
	}

	// Collect non-zero coefficients in reverse scan order.
	// levels[0] = highest-frequency non-zero, levels[totalCoeff-1] = lowest-frequency.
	var levels [16]int32
	var runs [16]int
	totalCoeff := 0
	totalZeros := 0

	// Track positions to compute run_before values
	pos := lastNZ
	for i := lastNZ; i >= 0; i-- {
		if coeffs[i] != 0 {
			levels[totalCoeff] = coeffs[i]
			if totalCoeff > 0 {
				runs[totalCoeff-1] = pos - i - 1
				totalZeros += pos - i - 1
			}
			pos = i
			totalCoeff++
		}
	}
	// Last coefficient's run: zeros from its position to index 0
	if totalCoeff > 0 {
		runs[totalCoeff-1] = pos
		totalZeros += pos
	}

	// Count trailing ones (up to 3 ±1 at the high-frequency end)
	trailingOnes := 0
	for i := 0; i < totalCoeff && trailingOnes < 3; i++ {
		if levels[i] == 1 || levels[i] == -1 {
			trailingOnes++
		} else {
			break
		}
	}

	// Write coeff_token
	tblIdx := coeffTokenTableIdx(nC)
	ctIdx := 4*totalCoeff + trailingOnes
	bw.writeBits(uint32(cavlcCoeffTokenBits[tblIdx][ctIdx]), int(cavlcCoeffTokenLen[tblIdx][ctIdx]))

	// Write trailing ones sign flags (last T1 written first)
	for i := trailingOnes - 1; i >= 0; i-- {
		if levels[i] < 0 {
			bw.writeBit(1)
		} else {
			bw.writeBit(0)
		}
	}

	// Write remaining levels (from index trailingOnes onward)
	suffixLength := 0
	if totalCoeff > 10 && trailingOnes < 3 {
		suffixLength = 1
	}

	for i := trailingOnes; i < totalCoeff; i++ {
		level := int(levels[i])

		// Map signed level to unsigned levelCode
		var levelCode int
		if level > 0 {
			levelCode = 2 * (level - 1) // +1→0, +2→2, +3→4, ...
		} else {
			levelCode = -2*level - 1 // -1→1, -2→3, -3→5, ...
		}

		// First non-trailing-one level adjustment: when T1 < 3,
		// |level| is guaranteed ≥ 2, so codes 0,1 (±1) are impossible.
		if i == trailingOnes && trailingOnes < 3 {
			levelCode -= 2
		}

		writeLevelVLC(bw, levelCode, suffixLength)

		// Update suffixLength
		if suffixLength == 0 {
			suffixLength = 1
		}
		absLevel := level
		if absLevel < 0 {
			absLevel = -absLevel
		}
		if suffixLength < 6 && absLevel > (3<<(suffixLength-1)) {
			suffixLength++
		}
	}

	// Write total_zeros (only if not all positions are non-zero)
	if totalCoeff < maxNumCoeff {
		tzIdx := totalCoeff - 1
		if tzIdx >= 0 && tzIdx < len(totalZerosLen) && totalZeros < len(totalZerosLen[tzIdx]) {
			bw.writeBits(uint32(totalZerosBits[tzIdx][totalZeros]),
				int(totalZerosLen[tzIdx][totalZeros]))
		}
	}

	// Write run_before for each coefficient except the last
	zl := totalZeros
	for i := 0; i < totalCoeff-1 && zl > 0; i++ {
		rb := runs[i]
		tIdx := zl - 1
		if tIdx > 6 {
			tIdx = 6
		}
		bw.writeBits(uint32(runBeforeBits[tIdx][rb]), int(runBeforeLen[tIdx][rb]))
		zl -= rb
	}

	return totalCoeff
}

// encodeChromaDCBlock encodes a 2x2 (4-element) chroma DC block with CAVLC.
// Uses the chroma DC coeff_token and total_zeros tables (nC = -1).
func encodeChromaDCBlock(bw *bitWriter, coeffs []int32) {
	lastNZ := -1
	for i := 3; i >= 0; i-- {
		if coeffs[i] != 0 {
			lastNZ = i
			break
		}
	}

	if lastNZ < 0 {
		bw.writeBits(uint32(chromaDCCoeffTokenBits[0]), int(chromaDCCoeffTokenLen[0]))
		return
	}

	var levels [4]int32
	var runs [4]int
	totalCoeff := 0
	totalZeros := 0

	pos := lastNZ
	for i := lastNZ; i >= 0; i-- {
		if coeffs[i] != 0 {
			levels[totalCoeff] = coeffs[i]
			if totalCoeff > 0 {
				runs[totalCoeff-1] = pos - i - 1
				totalZeros += pos - i - 1
			}
			pos = i
			totalCoeff++
		}
	}
	if totalCoeff > 0 {
		runs[totalCoeff-1] = pos
		totalZeros += pos
	}

	trailingOnes := 0
	for i := 0; i < totalCoeff && trailingOnes < 3; i++ {
		if levels[i] == 1 || levels[i] == -1 {
			trailingOnes++
		} else {
			break
		}
	}

	// coeff_token (chroma DC table)
	ctIdx := 4*totalCoeff + trailingOnes
	bw.writeBits(uint32(chromaDCCoeffTokenBits[ctIdx]), int(chromaDCCoeffTokenLen[ctIdx]))

	// Trailing ones signs
	for i := trailingOnes - 1; i >= 0; i-- {
		if levels[i] < 0 {
			bw.writeBit(1)
		} else {
			bw.writeBit(0)
		}
	}

	// Remaining levels
	suffixLength := 0
	if totalCoeff > 10 && trailingOnes < 3 {
		suffixLength = 1
	}
	for i := trailingOnes; i < totalCoeff; i++ {
		level := int(levels[i])
		var levelCode int
		if level > 0 {
			levelCode = 2 * (level - 1)
		} else {
			levelCode = -2*level - 1
		}
		if i == trailingOnes && trailingOnes < 3 {
			levelCode -= 2
		}
		writeLevelVLC(bw, levelCode, suffixLength)

		if suffixLength == 0 {
			suffixLength = 1
		}
		absLevel := levels[i]
		if absLevel < 0 {
			absLevel = -absLevel
		}
		if int(absLevel) > (3 << (suffixLength - 1)) {
			suffixLength++
		}
	}

	// total_zeros (chroma DC table)
	if totalCoeff < 4 {
		tzIdx := totalCoeff - 1
		bw.writeBits(uint32(chromaDCTotalZerosBits[tzIdx][totalZeros]),
			int(chromaDCTotalZerosLen[tzIdx][totalZeros]))
	}

	// run_before
	zl := totalZeros
	for i := 0; i < totalCoeff-1 && zl > 0; i++ {
		rb := runs[i]
		tIdx := zl - 1
		if tIdx > 6 {
			tIdx = 6
		}
		bw.writeBits(uint32(runBeforeBits[tIdx][rb]), int(runBeforeLen[tIdx][rb]))
		zl -= rb
	}
}

// writeLevelVLC encodes a level value using VLC (H.264 §9.2.2).
func writeLevelVLC(bw *bitWriter, levelCode, suffixLength int) {
	if levelCode < 0 {
		levelCode = 0
	}
	if suffixLength == 0 {
		// Level prefix only (no suffix) for codes 0-13
		if levelCode < 14 {
			// prefix = levelCode leading zeros + 1
			for i := 0; i < levelCode; i++ {
				bw.writeBit(0)
			}
			bw.writeBit(1)
		} else if levelCode < 30 {
			// prefix = 14, suffix = 4 bits
			for i := 0; i < 14; i++ {
				bw.writeBit(0)
			}
			bw.writeBit(1)
			bw.writeBits(uint32(levelCode-14), 4)
		} else {
			// escape: prefix = 15
			for i := 0; i < 15; i++ {
				bw.writeBit(0)
			}
			bw.writeBit(1)
			bw.writeBits(uint32(levelCode-30), 12)
		}
	} else {
		prefix := levelCode >> suffixLength
		suffix := levelCode - (prefix << suffixLength)
		if prefix < 15 {
			for i := 0; i < prefix; i++ {
				bw.writeBit(0)
			}
			bw.writeBit(1)
			bw.writeBits(uint32(suffix), suffixLength)
		} else {
			for i := 0; i < 15; i++ {
				bw.writeBit(0)
			}
			bw.writeBit(1)
			bw.writeBits(uint32(levelCode-(15<<suffixLength)), 12)
		}
	}
}

// encodeFrame produces a complete H.264 access unit (AUD + SPS + PPS + IDR).
// SPS/PPS are included in every access unit for HLS segment independence.
func encodeFrame(sps, pps, aud []byte, f *Frame, s *h264Settings, frameNum, idrPicID int) []byte {
	idr := encodeIDRFrame(f, s, frameNum, idrPicID)

	out := make([]byte, 0, len(aud)+len(sps)+len(pps)+len(idr))
	out = append(out, aud...)
	out = append(out, sps...)
	out = append(out, pps...)
	out = append(out, idr...)
	return out
}
