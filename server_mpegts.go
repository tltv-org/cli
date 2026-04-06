package main

// MPEG-TS muxer for H.264 video and AAC-LC audio elementary streams.
// Produces 188-byte transport stream packets per ISO 13818-1 (ITU-T H.222.0).

const (
	tsPacketSize = 188
	tsSyncByte   = byte(0x47)
	tsPIDPAT     = uint16(0x0000)
	tsPIDPMT     = uint16(0x1000)
	tsPIDVideo   = uint16(0x0100)
	tsPIDAudio   = uint16(0x0101)
	tsPIDNull    = uint16(0x1FFF)

	tsStreamTypeH264 = byte(0x1B)
	tsStreamTypeAAC  = byte(0x0F) // ISO/IEC 13818-7 ADTS AAC
	pesSIDVideo      = byte(0xE0)
	pesSIDAudio      = byte(0xC0)
)

// tsMuxer writes MPEG-TS packets for H.264 video and AAC audio PIDs.
type tsMuxer struct {
	patCC uint8
	pmtCC uint8
	vidCC uint8
	audCC uint8
}

// writePAT writes a single TS packet containing the PAT.
func (m *tsMuxer) writePAT(buf []byte) {
	clear188(buf)
	// Header: sync, PUSI=1, PID=0x0000, payload-only, CC
	buf[0] = tsSyncByte
	buf[1] = 0x40 // PUSI=1
	buf[2] = 0x00 // PID low
	buf[3] = 0x10 | (m.patCC & 0x0F)
	m.patCC = (m.patCC + 1) & 0x0F

	// Pointer field
	buf[4] = 0x00

	// PAT section
	pat := buf[5:]
	pat[0] = 0x00 // table_id = PAT
	// section_syntax=1, '0', reserved=11, section_length=13
	pat[1] = 0xB0
	pat[2] = 0x0D // length = 13 (5 header + 4 program + 4 CRC)
	pat[3] = 0x00 // transport_stream_id high
	pat[4] = 0x00 // transport_stream_id low
	pat[5] = 0xC1 // reserved=11, version=0, current_next=1
	pat[6] = 0x00 // section_number
	pat[7] = 0x00 // last_section_number
	pat[8] = 0x00 // program_number high = 1
	pat[9] = 0x01 // program_number low
	// reserved=111, PMT PID = 0x1000
	pat[10] = 0xF0 | 0x10 // high byte: reserved + PID[12:8]
	pat[11] = 0x00         // low byte: PID[7:0]
	// CRC32 over table_id through last program entry
	crc := mpegCRC32(pat[0:12])
	pat[12] = uint8(crc >> 24)
	pat[13] = uint8(crc >> 16)
	pat[14] = uint8(crc >> 8)
	pat[15] = uint8(crc)

	// Fill remaining with 0xFF
	for i := 5 + 16; i < tsPacketSize; i++ {
		buf[i] = 0xFF
	}
}

// writePMT writes a single TS packet containing the PMT with video and audio streams.
func (m *tsMuxer) writePMT(buf []byte) {
	clear188(buf)
	buf[0] = tsSyncByte
	// PUSI=1, PID=0x1000
	buf[1] = 0x40 | 0x10 // PUSI + PID high
	buf[2] = 0x00         // PID low
	buf[3] = 0x10 | (m.pmtCC & 0x0F)
	m.pmtCC = (m.pmtCC + 1) & 0x0F

	buf[4] = 0x00 // pointer field

	pmt := buf[5:]
	pmt[0] = 0x02 // table_id = PMT
	// section_syntax=1, '0', reserved=11, section_length=23
	pmt[1] = 0xB0
	pmt[2] = 0x17 // 5 header + 4 PCR/info + 5 video + 5 audio + 4 CRC = 23
	pmt[3] = 0x00 // program_number high = 1
	pmt[4] = 0x01
	pmt[5] = 0xC1 // reserved=11, version=0, current_next=1
	pmt[6] = 0x00 // section_number
	pmt[7] = 0x00 // last_section_number
	// reserved=111, PCR_PID = video PID (0x0100)
	pmt[8] = 0xE0 | 0x01 // reserved + PID high
	pmt[9] = 0x00         // PID low
	// reserved=1111, program_info_length=0
	pmt[10] = 0xF0
	pmt[11] = 0x00
	// Stream entry 1: H.264 video (PID 0x0100)
	pmt[12] = tsStreamTypeH264 // stream_type = 0x1B
	pmt[13] = 0xE0 | 0x01     // reserved + elementary PID high
	pmt[14] = 0x00             // elementary PID low
	pmt[15] = 0xF0             // reserved=1111, ES_info_length=0
	pmt[16] = 0x00
	// Stream entry 2: AAC audio (PID 0x0101)
	pmt[17] = tsStreamTypeAAC // stream_type = 0x0F
	pmt[18] = 0xE0 | 0x01    // reserved + elementary PID high
	pmt[19] = 0x01            // elementary PID low
	pmt[20] = 0xF0            // reserved=1111, ES_info_length=0
	pmt[21] = 0x00
	// CRC32
	crc := mpegCRC32(pmt[0:22])
	pmt[22] = uint8(crc >> 24)
	pmt[23] = uint8(crc >> 16)
	pmt[24] = uint8(crc >> 8)
	pmt[25] = uint8(crc)

	for i := 5 + 26; i < tsPacketSize; i++ {
		buf[i] = 0xFF
	}
}

// writeVideoPackets writes a complete PES-wrapped H.264 access unit as TS packets.
// Returns the slice of 188-byte packets appended to dst.
func (m *tsMuxer) writeVideoPackets(dst []byte, nalData []byte, ptsBase int64, isIDR bool) []byte {
	// Build PES header
	pesHeader := buildPESHeader(ptsBase)

	// Total payload = PES header + NAL data
	totalPayload := len(pesHeader) + len(nalData)

	// Calculate how many TS packets we need
	// First packet: 4 (header) + AF (variable) + payload
	// Subsequent: 4 (header) + 184 payload
	written := 0
	first := true
	payloadBuf := make([]byte, 0, totalPayload)
	payloadBuf = append(payloadBuf, pesHeader...)
	payloadBuf = append(payloadBuf, nalData...)

	for written < len(payloadBuf) {
		var pkt [tsPacketSize]byte
		pkt[0] = tsSyncByte

		if first {
			// PUSI=1, PID=video (0x0100)
			pkt[1] = 0x40 | 0x01 // PUSI + PID high
		} else {
			pkt[1] = 0x01 // PID high
		}
		pkt[2] = 0x00 // PID low

		available := 184 // max payload bytes (188 - 4 header)
		afOffset := 4     // where AF starts
		payloadOffset := 4

		if first {
			// Adaptation field with PCR and random access indicator
			afLen := 1 + 6 // flags byte + 6-byte PCR
			if isIDR {
				// Set random_access_indicator
			}
			pkt[afOffset] = uint8(afLen)     // adaptation_field_length
			flags := uint8(0x10)             // PCR_flag
			if isIDR {
				flags |= 0x40 // random_access_indicator
			}
			pkt[afOffset+1] = flags
			encodePCR(pkt[afOffset+2:], ptsBase)
			payloadOffset = afOffset + 1 + afLen
			available = tsPacketSize - payloadOffset
			pkt[3] = 0x30 | (m.vidCC & 0x0F) // AF + payload
		} else {
			pkt[3] = 0x10 | (m.vidCC & 0x0F) // payload only
		}

		remaining := len(payloadBuf) - written
		if remaining < available {
			// Need stuffing — add AF with stuffing bytes
			stuffNeeded := available - remaining
			if first {
				// Already have AF, extend it with stuffing
				existingAFLen := int(pkt[afOffset])
				pkt[afOffset] = uint8(existingAFLen + stuffNeeded)
				// Insert stuffing bytes after existing AF content
				insertAt := afOffset + 1 + existingAFLen
				for i := 0; i < stuffNeeded; i++ {
					pkt[insertAt+i] = 0xFF
				}
				payloadOffset += stuffNeeded
			} else {
				// Create new AF for stuffing
				pkt[3] = 0x30 | (m.vidCC & 0x0F) // AF + payload
				if stuffNeeded == 1 {
					pkt[4] = 0 // AF length = 0 (just the length byte)
					payloadOffset = 5
				} else {
					pkt[4] = uint8(stuffNeeded - 1) // AF length
					pkt[5] = 0x00                    // flags = 0
					for i := 6; i < 4+stuffNeeded; i++ {
						pkt[i] = 0xFF
					}
					payloadOffset = 4 + stuffNeeded
				}
			}
			available = remaining
		}

		// Copy payload
		copy(pkt[payloadOffset:], payloadBuf[written:written+available])
		written += available

		m.vidCC = (m.vidCC + 1) & 0x0F
		dst = append(dst, pkt[:]...)
		first = false
	}

	return dst
}

// buildPESHeader constructs a PES header for H.264 video with PTS.
func buildPESHeader(pts int64) []byte {
	var h [14]byte
	// PES start code
	h[0] = 0x00
	h[1] = 0x00
	h[2] = 0x01
	h[3] = pesSIDVideo // stream_id = 0xE0
	// PES packet length = 0 (unbounded for video)
	h[4] = 0x00
	h[5] = 0x00
	// Optional header: MPEG-2 marker, data alignment
	h[6] = 0x84 // '10' + scrambling=0 + priority=0 + alignment=1 + copyright=0 + original=0
	h[7] = 0x80 // PTS only
	h[8] = 0x05 // PES header data length = 5

	// Encode PTS (5 bytes)
	h[9] = 0x20 | uint8((pts>>29)&0x0E) | 0x01
	h[10] = uint8(pts >> 22)
	h[11] = uint8((pts>>14)&0xFE) | 0x01
	h[12] = uint8(pts >> 7)
	h[13] = uint8((pts<<1)&0xFE) | 0x01

	return h[:]
}

// writeAudioPES writes a batch of AAC ADTS frames as a single PES packet on the audio PID.
// Batching multiple ADTS frames per PES (like ffmpeg's default of ~16 per PES) is critical
// for gapless HLS playback — players decode the ADTS frames within the PES using the sample
// rate for timing, with the PTS applying to the first frame in the batch.
func (m *tsMuxer) writeAudioPES(dst []byte, frames []audioFrame) []byte {
	if len(frames) == 0 {
		return dst
	}

	// Concatenate all ADTS frame data
	totalADTS := 0
	for i := range frames {
		totalADTS += len(frames[i].data)
	}

	// Build PES header with PTS from the first frame
	pts := frames[0].pts
	pesHeader := buildAudioPESHeader(pts, totalADTS)

	// Build complete PES payload
	payloadBuf := make([]byte, 0, len(pesHeader)+totalADTS)
	payloadBuf = append(payloadBuf, pesHeader...)
	for i := range frames {
		payloadBuf = append(payloadBuf, frames[i].data...)
	}

	// Write TS packets
	written := 0
	first := true
	for written < len(payloadBuf) {
		var pkt [tsPacketSize]byte
		pkt[0] = tsSyncByte

		// PID = audio (0x0101)
		if first {
			pkt[1] = 0x40 | 0x01 // PUSI + PID high
		} else {
			pkt[1] = 0x01 // PID high
		}
		pkt[2] = 0x01 // PID low (0x0101)

		available := 184
		payloadOffset := 4

		if first {
			// Adaptation field with random_access_indicator for audio
			pkt[3] = 0x30 | (m.audCC & 0x0F) // AF + payload
			pkt[4] = 1                         // AF length = 1
			pkt[5] = 0x40                      // random_access_indicator
			payloadOffset = 6
			available = tsPacketSize - payloadOffset
		} else {
			pkt[3] = 0x10 | (m.audCC & 0x0F) // payload only
		}

		remaining := len(payloadBuf) - written
		if remaining < available {
			// Need stuffing
			stuffNeeded := available - remaining
			if first {
				// Extend existing AF with stuffing
				existingAFLen := int(pkt[4])
				pkt[4] = uint8(existingAFLen + stuffNeeded)
				insertAt := 5 + existingAFLen
				for i := 0; i < stuffNeeded; i++ {
					pkt[insertAt+i] = 0xFF
				}
				payloadOffset += stuffNeeded
			} else {
				pkt[3] = 0x30 | (m.audCC & 0x0F) // AF + payload
				if stuffNeeded == 1 {
					pkt[4] = 0
					payloadOffset = 5
				} else {
					pkt[4] = uint8(stuffNeeded - 1)
					pkt[5] = 0x00
					for i := 6; i < 4+stuffNeeded; i++ {
						pkt[i] = 0xFF
					}
					payloadOffset = 4 + stuffNeeded
				}
			}
			available = remaining
		}

		copy(pkt[payloadOffset:], payloadBuf[written:written+available])
		written += available

		m.audCC = (m.audCC + 1) & 0x0F
		dst = append(dst, pkt[:]...)
		first = false
	}

	return dst
}

// buildAudioPESHeader constructs a PES header for AAC audio with PTS.
// dataLen is the total ADTS payload size (may contain multiple frames).
func buildAudioPESHeader(pts int64, dataLen int) []byte {
	var h [14]byte
	// PES start code
	h[0] = 0x00
	h[1] = 0x00
	h[2] = 0x01
	h[3] = pesSIDAudio // stream_id = 0xC0

	// PES packet length = header_data_length(3+5) + payload
	pesPayloadLen := 3 + 5 + dataLen
	if pesPayloadLen > 0xFFFF {
		// For large PES, use 0 (unbounded)
		h[4] = 0x00
		h[5] = 0x00
	} else {
		h[4] = uint8(pesPayloadLen >> 8)
		h[5] = uint8(pesPayloadLen)
	}

	// Optional header: MPEG-2 marker, data alignment
	h[6] = 0x84 // '10' + scrambling=0 + priority=0 + alignment=1 + copyright=0 + original=0
	h[7] = 0x80 // PTS only
	h[8] = 0x05 // PES header data length = 5

	// Encode PTS (5 bytes)
	h[9] = 0x20 | uint8((pts>>29)&0x0E) | 0x01
	h[10] = uint8(pts >> 22)
	h[11] = uint8((pts>>14)&0xFE) | 0x01
	h[12] = uint8(pts >> 7)
	h[13] = uint8((pts<<1)&0xFE) | 0x01

	return h[:]
}

// encodePCR writes a 6-byte PCR into dst.
// base is the 33-bit PCR base in 90kHz units.
func encodePCR(dst []byte, base int64) {
	// 33-bit base | 6 reserved (all 1) | 9-bit extension (0)
	v := uint64(base) << 15
	v |= 0x7E00 // reserved bits = 111111, extension = 0
	dst[0] = uint8(v >> 40)
	dst[1] = uint8(v >> 32)
	dst[2] = uint8(v >> 24)
	dst[3] = uint8(v >> 16)
	dst[4] = uint8(v >> 8)
	dst[5] = uint8(v)
}

// clear188 zeros a 188-byte buffer.
func clear188(buf []byte) {
	for i := range buf[:tsPacketSize] {
		buf[i] = 0
	}
}

// --- MPEG-2 CRC32 ---

// MPEG-2 CRC32 polynomial: 0x04C11DB7, init: 0xFFFFFFFF, no reflection.
func mpegCRC32(data []byte) uint32 {
	crc := uint32(0xFFFFFFFF)
	for _, b := range data {
		crc = (crc << 8) ^ crc32Table[((crc>>24)^uint32(b))&0xFF]
	}
	return crc
}

var crc32Table = [256]uint32{
	0x00000000, 0x04C11DB7, 0x09823B6E, 0x0D4326D9, 0x130476DC, 0x17C56B6B, 0x1A864DB2, 0x1E475005,
	0x2608EDB8, 0x22C9F00F, 0x2F8AD6D6, 0x2B4BCB61, 0x350C9B64, 0x31CD86D3, 0x3C8EA00A, 0x384FBDBD,
	0x4C11DB70, 0x48D0C6C7, 0x4593E01E, 0x4152FDA9, 0x5F15ADAC, 0x5BD4B01B, 0x569796C2, 0x52568B75,
	0x6A1936C8, 0x6ED82B7F, 0x639B0DA6, 0x675A1011, 0x791D4014, 0x7DDC5DA3, 0x709F7B7A, 0x745E66CD,
	0x9823B6E0, 0x9CE2AB57, 0x91A18D8E, 0x95609039, 0x8B27C03C, 0x8FE6DD8B, 0x82A5FB52, 0x8664E6E5,
	0xBE2B5B58, 0xBAEA46EF, 0xB7A96036, 0xB3687D81, 0xAD2F2D84, 0xA9EE3033, 0xA4AD16EA, 0xA06C0B5D,
	0xD4326D90, 0xD0F37027, 0xDDB056FE, 0xD9714B49, 0xC7361B4C, 0xC3F706FB, 0xCEB42022, 0xCA753D95,
	0xF23A8028, 0xF6FB9D9F, 0xFBB8BB46, 0xFF79A6F1, 0xE13EF6F4, 0xE5FFEB43, 0xE8BCCD9A, 0xEC7DD02D,
	0x34867077, 0x30476DC0, 0x3D044B19, 0x39C556AE, 0x278206AB, 0x23431B1C, 0x2E003DC5, 0x2AC12072,
	0x128E9DCF, 0x164F8078, 0x1B0CA6A1, 0x1FCDBB16, 0x018AEB13, 0x054BF6A4, 0x0808D07D, 0x0CC9CDCA,
	0x7897AB07, 0x7C56B6B0, 0x71159069, 0x75D48DDE, 0x6B93DDDB, 0x6F52C06C, 0x6211E6B5, 0x66D0FB02,
	0x5E9F46BF, 0x5A5E5B08, 0x571D7DD1, 0x53DC6066, 0x4D9B3063, 0x495A2DD4, 0x44190B0D, 0x40D816BA,
	0xACA5C697, 0xA864DB20, 0xA527FDF9, 0xA1E6E04E, 0xBFA1B04B, 0xBB60ADFC, 0xB6238B25, 0xB2E29692,
	0x8AAD2B2F, 0x8E6C3698, 0x832F1041, 0x87EE0DF6, 0x99A95DF3, 0x9D684044, 0x902B669D, 0x94EA7B2A,
	0xE0B41DE7, 0xE4750050, 0xE9362689, 0xEDF73B3E, 0xF3B06B3B, 0xF771768C, 0xFA325055, 0xFEF34DE2,
	0xC6BCF05F, 0xC27DEDE8, 0xCF3ECB31, 0xCBFFD686, 0xD5B88683, 0xD1799B34, 0xDC3ABDED, 0xD8FBA05A,
	0x690CE0EE, 0x6DCDFD59, 0x608EDB80, 0x644FC637, 0x7A089632, 0x7EC98B85, 0x738AAD5C, 0x774BB0EB,
	0x4F040D56, 0x4BC510E1, 0x46863638, 0x42472B8F, 0x5C007B8A, 0x58C1663D, 0x558240E4, 0x51435D53,
	0x251D3B9E, 0x21DC2629, 0x2C9F00F0, 0x285E1D47, 0x36194D42, 0x32D850F5, 0x3F9B762C, 0x3B5A6B9B,
	0x0315D626, 0x07D4CB91, 0x0A97ED48, 0x0E56F0FF, 0x1011A0FA, 0x14D0BD4D, 0x19939B94, 0x1D528623,
	0xF12F560E, 0xF5EE4BB9, 0xF8AD6D60, 0xFC6C70D7, 0xE22B20D2, 0xE6EA3D65, 0xEBA91BBC, 0xEF68060B,
	0xD727BBB6, 0xD3E6A601, 0xDEA580D8, 0xDA649D6F, 0xC423CD6A, 0xC0E2D0DD, 0xCDA1F604, 0xC960EBB3,
	0xBD3E8D7E, 0xB9FF90C9, 0xB4BCB610, 0xB07DABA7, 0xAE3AFBA2, 0xAAFBE615, 0xA7B8C0CC, 0xA379DD7B,
	0x9B3660C6, 0x9FF77D71, 0x92B45BA8, 0x9675461F, 0x8832161A, 0x8CF30BAD, 0x81B02D74, 0x857130C3,
	0x5D8A9099, 0x594B8D2E, 0x5408ABF7, 0x50C9B640, 0x4E8EE645, 0x4A4FFBF2, 0x470CDD2B, 0x43CDC09C,
	0x7B827D21, 0x7F436096, 0x7200464F, 0x76C15BF8, 0x68860BFD, 0x6C47164A, 0x61043093, 0x65C52D24,
	0x119B4BE9, 0x155A565E, 0x18197087, 0x1CD86D30, 0x029F3D35, 0x065E2082, 0x0B1D065B, 0x0FDC1BEC,
	0x3793A651, 0x3352BBE6, 0x3E119D3F, 0x3AD08088, 0x2497D08D, 0x2056CD3A, 0x2D15EBE3, 0x29D4F654,
	0xC5A92679, 0xC1683BCE, 0xCC2B1D17, 0xC8EA00A0, 0xD6AD50A5, 0xD26C4D12, 0xDF2F6BCB, 0xDBEE767C,
	0xE3A1CBC1, 0xE760D676, 0xEA23F0AF, 0xEEE2ED18, 0xF0A5BD1D, 0xF464A0AA, 0xF9278673, 0xFDE69BC4,
	0x89B8FD09, 0x8D79E0BE, 0x803AC667, 0x84FBDBD0, 0x9ABC8BD5, 0x9E7D9662, 0x933EB0BB, 0x97FFAD0C,
	0xAFB010B1, 0xAB710D06, 0xA6322BDF, 0xA2F33668, 0xBCB4666D, 0xB8757BDA, 0xB5365D03, 0xB1F740B4,
}
