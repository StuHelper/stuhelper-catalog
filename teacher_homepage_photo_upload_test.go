package catalog

import (
	"bytes"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"testing"
)

func TestValidateTeacherHomepagePhotoUpload(t *testing.T) {
	pixels := image.NewNRGBA(image.Rect(0, 0, 3, 2))
	pixels.SetNRGBA(2, 1, color.NRGBA{R: 100, G: 40, B: 80, A: 128})
	var encodedPNG, encodedJPEG bytes.Buffer
	if err := png.Encode(&encodedPNG, pixels); err != nil {
		t.Fatal(err)
	}
	if err := jpeg.Encode(&encodedJPEG, pixels, nil); err != nil {
		t.Fatal(err)
	}
	const metadata = "comment\x00synthetic-private-upload-metadata"
	const trailing = "synthetic trailing payload"
	withPNGMetadata := bytes.Clone(encodedPNG.Bytes()[:33]) // PNG signature and IHDR
	withPNGMetadata = binary.BigEndian.AppendUint32(withPNGMetadata, uint32(len(metadata)))
	withPNGMetadata = append(withPNGMetadata, "tEXt"...)
	withPNGMetadata = append(withPNGMetadata, metadata...)
	withPNGMetadata = binary.BigEndian.AppendUint32(withPNGMetadata, crc32.ChecksumIEEE([]byte("tEXt"+metadata)))
	withPNGMetadata = append(withPNGMetadata, encodedPNG.Bytes()[33:]...)
	withJPEGMetadata := append([]byte{0xff, 0xd8, 0xff, 0xfe}, 0, byte(len(metadata)+2)) // SOI and COM
	withJPEGMetadata = append(withJPEGMetadata, metadata...)
	withJPEGMetadata = append(withJPEGMetadata, encodedJPEG.Bytes()[2:]...)

	for _, test := range []struct {
		format string
		data   []byte
	}{
		{"image/png", withPNGMetadata},
		{"image/jpeg", withJPEGMetadata},
	} {
		t.Run(test.format, func(t *testing.T) {
			data := append(bytes.Clone(test.data), trailing...)
			photo, err := ValidateTeacherHomepagePhotoUpload(test.format, data)
			if err != nil {
				t.Fatal(err)
			}
			if photo.MediaType != test.format || photo.Width != 3 || photo.Height != 2 ||
				ValidateTeacherHomepageImage(photo) != nil {
				t.Fatalf("invalid sanitized photo %s (%d x %d)", photo.MediaType, photo.Width, photo.Height)
			}
			if bytes.Contains(photo.Bytes, []byte(metadata)) || bytes.Contains(photo.Bytes, []byte(trailing)) {
				t.Fatal("upload metadata or trailing data was retained")
			}
			withoutTrailing, err := ValidateTeacherHomepagePhotoUpload(test.format, test.data)
			if err != nil || withoutTrailing.ID != photo.ID {
				t.Fatal("trailing bytes changed the photo digest", err)
			}
			if test.format == "image/png" {
				decoded, err := png.Decode(bytes.NewReader(photo.Bytes))
				if err != nil {
					t.Fatal(err)
				}
				if decoded.At(2, 1) != pixels.At(2, 1) {
					t.Fatal("transparent PNG pixels changed")
				}
			}
		})
	}

	oversized := func(width, height uint32) []byte {
		data := bytes.Clone(encodedPNG.Bytes())
		binary.BigEndian.PutUint32(data[16:20], width)
		binary.BigEndian.PutUint32(data[20:24], height)
		binary.BigEndian.PutUint32(data[29:33], crc32.ChecksumIEEE(data[12:29]))
		return data
	}
	cases := []struct {
		name        string
		contentType string
		data        []byte
	}{
		{"mime mismatch", "image/jpeg", encodedPNG.Bytes()},
		{"arbitrary content", "image/png", []byte("invalid")},
		{"truncated PNG", "image/png", encodedPNG.Bytes()[:encodedPNG.Len()-12]},
		{"truncated JPEG", "image/jpeg", encodedJPEG.Bytes()[:encodedJPEG.Len()-2]},
		{"oversize bytes", "image/png", make([]byte, TeacherHomepageMaxPhotoBytes+1)},
		{"oversize edge", "image/png", oversized(4097, 10)},
		{"oversize area", "image/png", oversized(4096, 2049)},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			photo, err := ValidateTeacherHomepagePhotoUpload(test.contentType, test.data)
			if !errors.Is(err, ErrInput) || len(photo.Bytes) != 0 {
				t.Fatal("invalid image accepted", err)
			}
		})
	}

	t.Run("capacity", func(t *testing.T) {
		for range cap(teacherHomepagePhotoUploads) {
			teacherHomepagePhotoUploads <- struct{}{}
		}
		defer func() {
			for range cap(teacherHomepagePhotoUploads) {
				<-teacherHomepagePhotoUploads
			}
		}()
		if _, err := ValidateTeacherHomepagePhotoUpload("image/png", encodedPNG.Bytes()); !errors.Is(err, ErrUnavailable) {
			t.Fatal("expected busy upload rejection", err)
		}
	})
}
