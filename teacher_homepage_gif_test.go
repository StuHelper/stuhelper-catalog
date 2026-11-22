package catalog

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	"image/gif"
	"testing"
)

func TestBodyGIFLimitsAndSanitizedExtensions(t *testing.T) {
	frame := image.NewPaletted(image.Rect(0, 0, 2, 1), color.Palette{color.Black, color.White})
	encode := func(count int) []byte {
		t.Helper()
		anim := gif.GIF{}
		for i := 0; i < count; i++ {
			anim.Image = append(anim.Image, frame)
			anim.Delay = append(anim.Delay, i)
		}
		var b bytes.Buffer
		if err := gif.EncodeAll(&b, &anim); err != nil {
			t.Fatal(err)
		}
		return b.Bytes()
	}
	check := func(data []byte, w, h int, want bool) {
		t.Helper()
		meta := TeacherHomepageBodyImage{ID: fmt.Sprintf("%x", sha256.Sum256(data)), MediaType: "image/gif", Width: w, Height: h, SizeBytes: int64(len(data))}
		if (ValidateTeacherHomepageBodyImage(meta, data) == nil) != want {
			t.Fatal("unexpected GIF validation result")
		}
	}
	valid := encode(300)
	check(valid, 2, 1, true)
	check(encode(301), 2, 1, false)
	check(encode(1), 2, 1, false)
	check(valid[:len(valid)-2], 2, 1, false)
	check(append(bytes.Clone(valid), 0), 2, 1, false)
	check(append(append(bytes.Clone(valid[:len(valid)-1]), '!', 0xfe, 1, 'x', 0), ';'), 2, 1, false)
	larger := bytes.Clone(valid)
	binary.LittleEndian.PutUint16(larger[6:], 512)
	binary.LittleEndian.PutUint16(larger[8:], 512)
	check(larger, 512, 512, false)
}
