package catalog

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"image/gif"
)

// validateBodyGIF bounds DecodeAll's allocations before it gets the compressed
// frames. Only image data, graphic controls and the animation loop may survive
// sanitization; a valid first frame alone is insufficient.
func validateBodyGIF(img TeacherHomepageBodyImage, data []byte) error {
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != img.ID || !boundedBodyGIF(data, img.Width, img.Height) {
		return ErrInput
	}
	if _, err := gif.DecodeAll(bytes.NewReader(data)); err != nil {
		return ErrInput
	}
	return nil
}

func skipGIFPalette(b *bytes.Buffer, flags byte) bool {
	if flags&0x80 == 0 {
		return true
	}
	size := 3 << ((flags & 7) + 1)
	return len(b.Next(size)) == size
}

func boundedBodyGIF(data []byte, width, height int) bool {
	b := bytes.NewBuffer(data)
	header := b.Next(13)
	if len(header) != 13 || string(header[:6]) != "GIF89a" ||
		int(binary.LittleEndian.Uint16(header[6:])) != width || int(binary.LittleEndian.Uint16(header[8:])) != height ||
		!skipGIFPalette(b, header[10]) {
		return false
	}
	frames := 0
	hasLoop := false
	for b.Len() > 0 {
		marker, _ := b.ReadByte()
		switch marker {
		case ';':
			return frames >= 2 && b.Len() == 0
		case '!':
			label, err := b.ReadByte()
			if err != nil {
				return false
			}
			switch label {
			case 0xf9:
				ctrl := b.Next(6)
				if len(ctrl) != 6 || ctrl[0] != 4 || ctrl[1]&0xe2 != 0 || ctrl[1]>>2 > 3 || ctrl[5] != 0 {
					return false
				}
			case 0xff:
				loop := b.Next(17)
				if hasLoop || frames != 0 || len(loop) != 17 || string(loop[:14]) != "\x0bNETSCAPE2.0\x03\x01" || loop[16] != 0 {
					return false
				}
				hasLoop = true
			default:
				return false
			}
		case ',':
			frames++
			desc := b.Next(9)
			if frames > 300 || int64(width)*int64(height)*int64(frames) > teacherHomepageBodyImagePixels || len(desc) != 9 {
				return false
			}
			x, y := int(binary.LittleEndian.Uint16(desc)), int(binary.LittleEndian.Uint16(desc[2:]))
			w, h := int(binary.LittleEndian.Uint16(desc[4:])), int(binary.LittleEndian.Uint16(desc[6:]))
			if w == 0 || h == 0 || x+w > width || y+h > height || !skipGIFPalette(b, desc[8]) {
				return false
			}
			if _, err := b.ReadByte(); err != nil { // LZW code size is validated by the decoder.
				return false
			}
			for {
				n, err := b.ReadByte()
				if err != nil || len(b.Next(int(n))) != int(n) {
					return false
				}
				if n == 0 {
					break
				}
			}
		default:
			return false
		}
	}
	return false
}
