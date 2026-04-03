package main

// YCbCr represents a single color sample in BT.601 limited range.
type YCbCr struct{ Y, Cb, Cr uint8 }

// SMPTE EG 1-1990 color bar reference — BT.601 limited range YCbCr values.
// Three-row pattern: 75% bars, reverse castellations, PLUGE.
//
// Row 1 (top ~67%): 75% color bars.
// Computed from: Y = 16 + 65.481*R + 128.553*G + 24.966*B
//                Cb = 128 - 37.797*R - 74.203*G + 112.0*B
//                Cr = 128 + 112.0*R  - 93.786*G - 18.214*B
// where R,G,B are 0.0 or 0.75 for 75% bars.
var smpteBars = [7]YCbCr{
	{180, 128, 128}, // White (75% gray)
	{162, 44, 142},  // Yellow
	{131, 156, 44},  // Cyan
	{112, 72, 58},   // Green
	{84, 184, 198},  // Magenta
	{65, 100, 212},  // Red
	{35, 212, 114},  // Blue
}

// Row 2 (middle ~8%): reverse castellations — complement of each bar.
// Black uses Y=19 (ffmpeg smptebars), not Y=16 (studio black).
var smpteRow2 = [7]YCbCr{
	{35, 212, 114},  // Blue (under White)
	{19, 128, 128},  // Black (under Yellow)
	{84, 184, 198},  // Magenta (under Cyan)
	{19, 128, 128},  // Black (under Green)
	{131, 156, 44},  // Cyan (under Magenta)
	{19, 128, 128},  // Black (under Red)
	{180, 128, 128}, // White 75% (under Blue)
}

// Row 3 (bottom ~25%) reference colors (BT.601 YCbCr).
// Values match ffmpeg's smptebars source filter exactly.
var (
	colorNegI = YCbCr{57, 156, 97}  // −I
	colorPosQ = YCbCr{44, 171, 147} // +Q
	colorSubBlack   = YCbCr{7, 128, 128}    // -4 IRE — PLUGE below black
	colorSuperBlack = YCbCr{24, 128, 128}   // +4 IRE — PLUGE above black
)

var colorBlack = YCbCr{16, 128, 128}
var colorWhite = YCbCr{235, 128, 128}

// Frame holds a YCbCr 4:2:0 frame with separate planes.
// Y: width*height, Cb: (width/2)*(height/2), Cr: (width/2)*(height/2).
type Frame struct {
	Width  int
	Height int
	Y      []uint8
	Cb     []uint8
	Cr     []uint8
}

// newFrame allocates a Frame with the given dimensions.
// Width and height must be multiples of 16 (macroblock-aligned).
func newFrame(width, height int) *Frame {
	chromaW := width / 2
	chromaH := height / 2
	return &Frame{
		Width:  width,
		Height: height,
		Y:      make([]uint8, width*height),
		Cb:     make([]uint8, chromaW*chromaH),
		Cr:     make([]uint8, chromaW*chromaH),
	}
}

// fillRect fills a pixel-aligned rectangle in all three YCbCr planes.
// Luma is filled at full resolution; chroma at half resolution (4:2:0).
// Coordinates are clamped to frame bounds.
func fillRect(f *Frame, x0, y0, rw, rh int, c YCbCr) {
	w := f.Width
	h := f.Height

	// Clamp to frame.
	if x0 < 0 {
		rw += x0
		x0 = 0
	}
	if y0 < 0 {
		rh += y0
		y0 = 0
	}
	if x0+rw > w {
		rw = w - x0
	}
	if y0+rh > h {
		rh = h - y0
	}
	if rw <= 0 || rh <= 0 {
		return
	}

	// Luma plane.
	for y := y0; y < y0+rh; y++ {
		off := y*w + x0
		for x := 0; x < rw; x++ {
			f.Y[off+x] = c.Y
		}
	}

	// Chroma planes (half resolution).
	chromaW := w / 2
	cx0 := x0 / 2
	cy0 := y0 / 2
	cw := (x0 + rw + 1) / 2
	ch := (y0 + rh + 1) / 2
	cw -= cx0
	ch -= cy0
	for y := cy0; y < cy0+ch; y++ {
		off := y*chromaW + cx0
		for x := 0; x < cw; x++ {
			f.Cb[off+x] = c.Cb
			f.Cr[off+x] = c.Cr
		}
	}
}

// fillBars fills the frame with the full SMPTE EG 1-1990 three-row color bar pattern,
// using width/height calculations matching ffmpeg's smptebars source filter.
//
// Row 1 (top 2/3): 7 vertical bars at 75% — Gray, Yellow, Cyan, Green, Magenta, Red, Blue.
// Row 2 (middle):  Reverse castellations — Blue, Black, Magenta, Black, Cyan, Black, Gray.
// Row 3 (bottom):  −I, 100% White, +Q, Black, PLUGE (sub-black, black, super-black, black).
//
// Row 3 uses wider sections (p_w = r_w*5/4) that don't align with bar boundaries,
// matching ffmpeg's draw_bar layout exactly. Rows 1-2 heights are rounded to even
// pixel boundaries for 4:2:0 chroma alignment.
func fillBars(f *Frame) {
	w := f.Width
	h := f.Height
	mbRows := h / 16

	// For very small frames (< 4 MB rows), fill entirely with row 1 bars.
	if mbRows < 4 {
		rw := (w + 6) / 7
		x := 0
		for i := 0; i < 7; i++ {
			bw := rw
			if x+bw > w {
				bw = w - x
			}
			fillRect(f, x, 0, bw, h, smpteBars[i])
			x += rw
		}
		return
	}

	// ffmpeg smptebars geometry.
	rw := (w + 6) / 7       // bar width (rows 1+2)
	rh := h * 2 / 3         // row 1 height
	rh = rh &^ 1            // snap to even for chroma alignment
	wh := h*3/4 - rh        // row 2 height
	wh = wh &^ 1            // snap to even for chroma alignment
	pw := rw * 5 / 4        // row 3 section width (wider than bars)
	ph := h - wh - rh       // row 3 height (remainder)

	r3y := rh + wh // row 3 y-offset

	// Row 1: 7 color bars.
	x := 0
	for i := 0; i < 7; i++ {
		bw := rw
		if x+bw > w {
			bw = w - x
		}
		fillRect(f, x, 0, bw, rh, smpteBars[i])
		x += rw
	}

	// Row 2: reverse castellations (wobnair), same widths as row 1.
	x = 0
	for i := 0; i < 7; i++ {
		bw := rw
		if x+bw > w {
			bw = w - x
		}
		fillRect(f, x, rh, bw, wh, smpteRow2[i])
		x += rw
	}

	// Row 3: wider sections matching ffmpeg layout.
	// -I, White, +Q, Black fill, then PLUGE in last 2*rw.
	x = 0
	fillRect(f, x, r3y, pw, ph, colorNegI)
	x += pw
	fillRect(f, x, r3y, pw, ph, colorWhite)
	x += pw
	fillRect(f, x, r3y, pw, ph, colorPosQ)
	x += pw

	// Black fill from current x to 5*rw.
	blackEnd := 5 * rw
	if blackEnd > w {
		blackEnd = w
	}
	if x < blackEnd {
		fillRect(f, x, r3y, blackEnd-x, ph, colorBlack)
	}
	x = blackEnd

	// PLUGE: 4 strips in the last 2*rw, each rw/3 wide.
	pw3 := rw / 3
	fillRect(f, x, r3y, pw3, ph, colorSubBlack)
	x += pw3
	fillRect(f, x, r3y, pw3, ph, colorBlack)
	x += pw3
	fillRect(f, x, r3y, pw3, ph, colorSuperBlack)
	x += pw3
	// Remaining pixels to right edge: black.
	if x < w {
		fillRect(f, x, r3y, w-x, ph, colorBlack)
	}
}

// 8×8 sans-serif block font for printable ASCII (0x20-0x7E).
// Designed for pixel-level test signal rendering. 2-pixel stroke width throughout,
// no serifs, geometric/blocky shapes. Uppercase uses 6-pixel width (cols 0-5);
// M, N, W use 7 pixels. Based on IBM VGA character set structure (public domain)
// with all decorative elements removed.
//
// Each character is 8 rows of 8 pixels. Each byte is one row.
// Bit encoding: LSB = leftmost pixel. To test pixel at column x: (row >> x) & 1.
var font8x8 = [95][8]byte{
	{0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00}, // 0x20 ' '
	{0x0C, 0x0C, 0x0C, 0x0C, 0x0C, 0x00, 0x0C, 0x00}, // 0x21 '!'
	{0x36, 0x36, 0x36, 0x00, 0x00, 0x00, 0x00, 0x00}, // 0x22 '"'
	{0x36, 0x36, 0x7F, 0x36, 0x7F, 0x36, 0x36, 0x00}, // 0x23 '#'
	{0x0C, 0x3E, 0x03, 0x1E, 0x30, 0x1F, 0x0C, 0x00}, // 0x24 '$'
	{0x00, 0x63, 0x33, 0x18, 0x0C, 0x66, 0x63, 0x00}, // 0x25 '%'
	{0x0E, 0x1B, 0x0E, 0x37, 0x33, 0x33, 0x3E, 0x00}, // 0x26 '&'
	{0x0C, 0x0C, 0x06, 0x00, 0x00, 0x00, 0x00, 0x00}, // 0x27 '\''
	{0x18, 0x0C, 0x06, 0x06, 0x06, 0x0C, 0x18, 0x00}, // 0x28 '('
	{0x06, 0x0C, 0x18, 0x18, 0x18, 0x0C, 0x06, 0x00}, // 0x29 ')'
	{0x00, 0x36, 0x1C, 0x3E, 0x1C, 0x36, 0x00, 0x00}, // 0x2A '*'
	{0x00, 0x0C, 0x0C, 0x3F, 0x0C, 0x0C, 0x00, 0x00}, // 0x2B '+'
	{0x00, 0x00, 0x00, 0x00, 0x00, 0x0C, 0x0C, 0x06}, // 0x2C ','
	{0x00, 0x00, 0x00, 0x3F, 0x00, 0x00, 0x00, 0x00}, // 0x2D '-'
	{0x00, 0x00, 0x00, 0x00, 0x00, 0x0C, 0x0C, 0x00}, // 0x2E '.'
	{0x30, 0x30, 0x18, 0x0C, 0x06, 0x03, 0x03, 0x00}, // 0x2F '/'
	{0x1E, 0x33, 0x33, 0x33, 0x33, 0x33, 0x1E, 0x00}, // 0x30 '0'
	{0x0C, 0x0E, 0x0C, 0x0C, 0x0C, 0x0C, 0x3F, 0x00}, // 0x31 '1'
	{0x1E, 0x33, 0x30, 0x18, 0x0C, 0x06, 0x3F, 0x00}, // 0x32 '2'
	{0x1E, 0x33, 0x30, 0x1C, 0x30, 0x33, 0x1E, 0x00}, // 0x33 '3'
	{0x33, 0x33, 0x33, 0x3F, 0x30, 0x30, 0x30, 0x00}, // 0x34 '4'
	{0x3F, 0x03, 0x03, 0x1F, 0x30, 0x30, 0x1F, 0x00}, // 0x35 '5'
	{0x1E, 0x03, 0x03, 0x1F, 0x33, 0x33, 0x1E, 0x00}, // 0x36 '6'
	{0x3F, 0x30, 0x18, 0x0C, 0x0C, 0x0C, 0x0C, 0x00}, // 0x37 '7'
	{0x1E, 0x33, 0x33, 0x1E, 0x33, 0x33, 0x1E, 0x00}, // 0x38 '8'
	{0x1E, 0x33, 0x33, 0x3E, 0x30, 0x30, 0x1E, 0x00}, // 0x39 '9'
	{0x00, 0x0C, 0x0C, 0x00, 0x00, 0x0C, 0x0C, 0x00}, // 0x3A ':'
	{0x00, 0x0C, 0x0C, 0x00, 0x00, 0x0C, 0x0C, 0x06}, // 0x3B ';'
	{0x18, 0x0C, 0x06, 0x03, 0x06, 0x0C, 0x18, 0x00}, // 0x3C '<'
	{0x00, 0x00, 0x3F, 0x00, 0x00, 0x3F, 0x00, 0x00}, // 0x3D '='
	{0x06, 0x0C, 0x18, 0x30, 0x18, 0x0C, 0x06, 0x00}, // 0x3E '>'
	{0x1E, 0x33, 0x30, 0x18, 0x0C, 0x00, 0x0C, 0x00}, // 0x3F '?'
	{0x3E, 0x63, 0x7B, 0x7B, 0x7B, 0x03, 0x1E, 0x00}, // 0x40 '@'
	{0x1E, 0x33, 0x33, 0x3F, 0x33, 0x33, 0x33, 0x00}, // 0x41 'A'
	{0x1F, 0x33, 0x33, 0x1F, 0x33, 0x33, 0x1F, 0x00}, // 0x42 'B'
	{0x1E, 0x33, 0x03, 0x03, 0x03, 0x33, 0x1E, 0x00}, // 0x43 'C'
	{0x1F, 0x33, 0x33, 0x33, 0x33, 0x33, 0x1F, 0x00}, // 0x44 'D'
	{0x3F, 0x03, 0x03, 0x1F, 0x03, 0x03, 0x3F, 0x00}, // 0x45 'E'
	{0x3F, 0x03, 0x03, 0x1F, 0x03, 0x03, 0x03, 0x00}, // 0x46 'F'
	{0x1E, 0x33, 0x03, 0x3B, 0x33, 0x33, 0x1E, 0x00}, // 0x47 'G'
	{0x33, 0x33, 0x33, 0x3F, 0x33, 0x33, 0x33, 0x00}, // 0x48 'H'
	{0x1E, 0x0C, 0x0C, 0x0C, 0x0C, 0x0C, 0x1E, 0x00}, // 0x49 'I'
	{0x30, 0x30, 0x30, 0x30, 0x30, 0x33, 0x1E, 0x00}, // 0x4A 'J'
	{0x33, 0x1B, 0x0F, 0x07, 0x0F, 0x1B, 0x33, 0x00}, // 0x4B 'K'
	{0x03, 0x03, 0x03, 0x03, 0x03, 0x03, 0x3F, 0x00}, // 0x4C 'L'
	{0x63, 0x77, 0x7F, 0x6B, 0x63, 0x63, 0x63, 0x00}, // 0x4D 'M'
	{0x33, 0x37, 0x3F, 0x3B, 0x33, 0x33, 0x33, 0x00}, // 0x4E 'N'
	{0x1E, 0x33, 0x33, 0x33, 0x33, 0x33, 0x1E, 0x00}, // 0x4F 'O'
	{0x1F, 0x33, 0x33, 0x1F, 0x03, 0x03, 0x03, 0x00}, // 0x50 'P'
	{0x1E, 0x33, 0x33, 0x33, 0x33, 0x1E, 0x3C, 0x00}, // 0x51 'Q'
	{0x1F, 0x33, 0x33, 0x1F, 0x0F, 0x1B, 0x33, 0x00}, // 0x52 'R'
	{0x1E, 0x33, 0x03, 0x1E, 0x30, 0x33, 0x1E, 0x00}, // 0x53 'S'
	{0x3F, 0x0C, 0x0C, 0x0C, 0x0C, 0x0C, 0x0C, 0x00}, // 0x54 'T'
	{0x33, 0x33, 0x33, 0x33, 0x33, 0x33, 0x1E, 0x00}, // 0x55 'U'
	{0x33, 0x33, 0x33, 0x33, 0x33, 0x1E, 0x0C, 0x00}, // 0x56 'V'
	{0x63, 0x63, 0x63, 0x6B, 0x7F, 0x77, 0x63, 0x00}, // 0x57 'W'
	{0x33, 0x33, 0x1E, 0x0C, 0x1E, 0x33, 0x33, 0x00}, // 0x58 'X'
	{0x33, 0x33, 0x33, 0x1E, 0x0C, 0x0C, 0x0C, 0x00}, // 0x59 'Y'
	{0x3F, 0x30, 0x18, 0x0C, 0x06, 0x03, 0x3F, 0x00}, // 0x5A 'Z'
	{0x1E, 0x06, 0x06, 0x06, 0x06, 0x06, 0x1E, 0x00}, // 0x5B '['
	{0x03, 0x03, 0x06, 0x0C, 0x18, 0x30, 0x30, 0x00}, // 0x5C '\\'
	{0x1E, 0x18, 0x18, 0x18, 0x18, 0x18, 0x1E, 0x00}, // 0x5D ']'
	{0x0C, 0x1E, 0x33, 0x00, 0x00, 0x00, 0x00, 0x00}, // 0x5E '^'
	{0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x7F}, // 0x5F '_'
	{0x0C, 0x0C, 0x18, 0x00, 0x00, 0x00, 0x00, 0x00}, // 0x60 '`'
	{0x00, 0x00, 0x1E, 0x30, 0x3E, 0x33, 0x3E, 0x00}, // 0x61 'a'
	{0x03, 0x03, 0x1F, 0x33, 0x33, 0x33, 0x1F, 0x00}, // 0x62 'b'
	{0x00, 0x00, 0x1E, 0x03, 0x03, 0x03, 0x1E, 0x00}, // 0x63 'c'
	{0x30, 0x30, 0x3E, 0x33, 0x33, 0x33, 0x3E, 0x00}, // 0x64 'd'
	{0x00, 0x00, 0x1E, 0x33, 0x3F, 0x03, 0x1E, 0x00}, // 0x65 'e'
	{0x1C, 0x06, 0x06, 0x1F, 0x06, 0x06, 0x06, 0x00}, // 0x66 'f'
	{0x00, 0x00, 0x3E, 0x33, 0x33, 0x3E, 0x30, 0x1E}, // 0x67 'g'
	{0x03, 0x03, 0x1F, 0x33, 0x33, 0x33, 0x33, 0x00}, // 0x68 'h'
	{0x0C, 0x00, 0x0C, 0x0C, 0x0C, 0x0C, 0x0C, 0x00}, // 0x69 'i'
	{0x30, 0x00, 0x30, 0x30, 0x30, 0x30, 0x33, 0x1E}, // 0x6A 'j'
	{0x03, 0x03, 0x33, 0x1B, 0x0F, 0x1B, 0x33, 0x00}, // 0x6B 'k'
	{0x0C, 0x0C, 0x0C, 0x0C, 0x0C, 0x0C, 0x0C, 0x00}, // 0x6C 'l'
	{0x00, 0x00, 0x33, 0x7F, 0x7F, 0x6B, 0x63, 0x00}, // 0x6D 'm'
	{0x00, 0x00, 0x1F, 0x33, 0x33, 0x33, 0x33, 0x00}, // 0x6E 'n'
	{0x00, 0x00, 0x1E, 0x33, 0x33, 0x33, 0x1E, 0x00}, // 0x6F 'o'
	{0x00, 0x00, 0x1F, 0x33, 0x33, 0x1F, 0x03, 0x03}, // 0x70 'p'
	{0x00, 0x00, 0x3E, 0x33, 0x33, 0x3E, 0x30, 0x30}, // 0x71 'q'
	{0x00, 0x00, 0x1B, 0x0F, 0x03, 0x03, 0x03, 0x00}, // 0x72 'r'
	{0x00, 0x00, 0x3E, 0x03, 0x1E, 0x30, 0x1F, 0x00}, // 0x73 's'
	{0x06, 0x06, 0x1F, 0x06, 0x06, 0x06, 0x1C, 0x00}, // 0x74 't'
	{0x00, 0x00, 0x33, 0x33, 0x33, 0x33, 0x3E, 0x00}, // 0x75 'u'
	{0x00, 0x00, 0x33, 0x33, 0x33, 0x1E, 0x0C, 0x00}, // 0x76 'v'
	{0x00, 0x00, 0x63, 0x63, 0x6B, 0x7F, 0x77, 0x00}, // 0x77 'w'
	{0x00, 0x00, 0x33, 0x1E, 0x0C, 0x1E, 0x33, 0x00}, // 0x78 'x'
	{0x00, 0x00, 0x33, 0x33, 0x33, 0x3E, 0x30, 0x1E}, // 0x79 'y'
	{0x00, 0x00, 0x3F, 0x18, 0x0C, 0x06, 0x3F, 0x00}, // 0x7A 'z'
	{0x18, 0x0C, 0x0C, 0x06, 0x0C, 0x0C, 0x18, 0x00}, // 0x7B '{'
	{0x0C, 0x0C, 0x0C, 0x0C, 0x0C, 0x0C, 0x0C, 0x00}, // 0x7C '|'
	{0x06, 0x0C, 0x0C, 0x18, 0x0C, 0x0C, 0x06, 0x00}, // 0x7D '}'
	{0x00, 0x00, 0x26, 0x19, 0x00, 0x00, 0x00, 0x00}, // 0x7E '~'
}

// drawChar draws a character at (px, py) in the Y plane at the given scale.
// Uses the 8×8 sans-serif block font with native 2-pixel strokes.
// fg/bg are luma values. If bg is negative, background pixels are not drawn (transparent).
func drawChar(f *Frame, px, py int, ch byte, scale int, fg uint8, bg int) {
	if ch < 0x20 || ch > 0x7E {
		return
	}
	glyph := &font8x8[ch-0x20]

	for row := 0; row < 8; row++ {
		for col := 0; col < 8; col++ {
			set := glyph[row]>>uint(col)&1 != 0

			var val uint8
			if set {
				val = fg
			} else if bg < 0 {
				continue
			} else {
				val = uint8(bg)
			}
			for sy := 0; sy < scale; sy++ {
				fy := py + row*scale + sy
				if fy < 0 || fy >= f.Height {
					continue
				}
				for sx := 0; sx < scale; sx++ {
					fx := px + col*scale + sx
					if fx < 0 || fx >= f.Width {
						continue
					}
					f.Y[fy*f.Width+fx] = val
				}
			}
		}
	}
}

// drawString draws a string at (px, py) in the Y plane.
// Character advance is 8*scale pixels (8px cell = 2px gap for 6-wide glyphs,
// 1px gap for 7-wide M/W/m/w).
func drawString(f *Frame, px, py int, s string, scale int, fg uint8, bg int) {
	for i := 0; i < len(s); i++ {
		drawChar(f, px+i*8*scale, py, s[i], scale, fg, bg)
	}
}

// stringWidth returns the pixel width of a string at the given scale.
func stringWidth(s string, scale int) int {
	if len(s) == 0 {
		return 0
	}
	return len(s)*8*scale - 2*scale // last char has no trailing gap
}

// --- Pixel-level rendering ---

// drawRect fills a rectangle in the Y plane with the given luma value.
func drawRect(f *Frame, x, y, w, h int, val uint8) {
	for row := y; row < y+h && row < f.Height; row++ {
		if row < 0 {
			continue
		}
		for col := x; col < x+w && col < f.Width; col++ {
			if col < 0 {
				continue
			}
			f.Y[row*f.Width+col] = val
		}
	}
}

// drawChromaRect fills a rectangle in both chroma planes.
// Coordinates are in chroma-plane units (half luma resolution).
func drawChromaRect(f *Frame, x, y, w, h int, cbVal, crVal uint8) {
	chromaW := f.Width / 2
	chromaH := f.Height / 2
	for row := y; row < y+h && row < chromaH; row++ {
		if row < 0 {
			continue
		}
		for col := x; col < x+w && col < chromaW; col++ {
			if col < 0 {
				continue
			}
			f.Cb[row*chromaW+col] = cbVal
			f.Cr[row*chromaW+col] = crVal
		}
	}
}

// drawBox draws a background box in both luma and chroma planes.
// Coordinates are snapped to even boundaries so luma and chroma edges coincide
// in the 4:2:0 grid, preventing colored fringing from bar chroma bleeding in.
func drawBox(f *Frame, x, y, w, h int, bg YCbCr) {
	// Snap to even pixel grid for 4:2:0 alignment.
	x2 := x &^ 1
	y2 := y &^ 1
	w2 := ((x + w + 1) &^ 1) - x2
	h2 := ((y + h + 1) &^ 1) - y2
	drawRect(f, x2, y2, w2, h2, bg.Y)
	drawChromaRect(f, x2/2, y2/2, w2/2, h2/2, bg.Cb, bg.Cr)
}

// renderTestFrame renders a complete test signal frame:
// SMPTE bars background with "TLTV" branding at top, optional channel name
// below, and time string centered.
//
// fontScale controls glyph size: 0 = auto-scale from resolution.
// timeStr is the pre-computed display string (e.g. "15:04:05" or "00:12:34").
func renderTestFrame(f *Frame, channelName string, timeStr string, fontScale int) {
	fillBars(f)
	renderTestFramePx(f, channelName, timeStr, fontScale)
}

// renderTestFramePx renders the test screen at pixel resolution.
// fontScale 0 = auto-scale from resolution.
//
// Scale is rounded to even so glyph height (8×scale) is a multiple of 16,
// naturally aligning text to the macroblock grid for clean H.264 encoding.
// Boxes are tight to the text with minimal padding.
func renderTestFramePx(f *Frame, channelName, timeStr string, fontScale int) {
	scale := fontScale
	if scale <= 0 {
		if f.Height <= 480 {
			scale = f.Height / 120 // SD: 360p→3, 480p→4
		} else {
			scale = f.Height / 135 // HD: 720p→5, 1080p→8, 1440p→10, 4K→16
		}
	}
	if scale < 2 {
		scale = 2
	}
	pad := scale   // uniform padding inside each box
	gap := pad * 2 // visible gap between separate boxes

	// "TLTV" branding — centered at top
	brandW := stringWidth("TLTV", scale)
	brandH := 8 * scale
	brandX := (f.Width - brandW) / 2
	brandY := gap // margin from top of frame
	drawBox(f, brandX-pad, brandY-pad, brandW+pad*2, brandH+pad*2, colorBlack)
	drawString(f, brandX, brandY, "TLTV", scale, colorWhite.Y, -1)
	boxBottom := brandY + brandH + pad // bottom edge of branding box

	// Channel name — separate box below branding
	if channelName != "" && channelName != "TLTV" {
		nameW := stringWidth(channelName, scale)
		nameH := 8 * scale
		nameX := (f.Width - nameW) / 2
		nameY := boxBottom + gap + pad // gap of colored bars, then padding
		drawBox(f, nameX-pad, nameY-pad, nameW+pad*2, nameH+pad*2, colorBlack)
		drawString(f, nameX, nameY, channelName, scale, colorWhite.Y, -1)
	}

	// Time display — centered, larger scale
	clockScale := scale * 2
	timeW := stringWidth(timeStr, clockScale)
	timeH := 8 * clockScale
	timeX := (f.Width - timeW) / 2
	timeY := (f.Height - timeH) / 2
	drawBox(f, timeX-pad, timeY-pad, timeW+pad*2, timeH+pad*2, colorBlack)
	drawString(f, timeX, timeY, timeStr, clockScale, colorWhite.Y, -1)
}
