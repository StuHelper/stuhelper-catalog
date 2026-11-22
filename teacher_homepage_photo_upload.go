package catalog

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"image"
	"image/jpeg"
	"image/png"
)

// Limit concurrent user-supplied decodes and encodes. Even a small upload
// can allocate a full raster once its compressed pixels are decoded.
var teacherHomepagePhotoUploads = make(chan struct{}, 2)

func validTeacherHomepagePhotoDimensions(width, height int) bool {
	return width >= 1 && width <= 4096 && height >= 1 && height <= 4096 && width*height <= 8_388_608
}

// ValidateTeacherHomepagePhotoUpload derives the public image from decoded
// pixels. Original comments, metadata and trailing bytes stay out of storage.
func ValidateTeacherHomepagePhotoUpload(contentType string, data []byte) (TeacherHomepageImage, error) {
	if len(data) == 0 || len(data) > TeacherHomepageMaxPhotoBytes ||
		contentType != "image/jpeg" && contentType != "image/png" {
		return TeacherHomepageImage{}, ErrInput
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || "image/"+format != contentType || !validTeacherHomepagePhotoDimensions(cfg.Width, cfg.Height) {
		return TeacherHomepageImage{}, ErrInput
	}
	select {
	case teacherHomepagePhotoUploads <- struct{}{}:
		defer func() { <-teacherHomepagePhotoUploads }()
	default:
		return TeacherHomepageImage{}, ErrUnavailable
	}
	pixels, decodedFormat, err := image.Decode(bytes.NewReader(data))
	if err != nil || decodedFormat != format || pixels.Bounds().Dx() != cfg.Width || pixels.Bounds().Dy() != cfg.Height {
		return TeacherHomepageImage{}, ErrInput
	}

	var output boundedTeacherHomepageImage
	switch format {
	case "jpeg":
		err = jpeg.Encode(&output, pixels, &jpeg.Options{Quality: 90})
	case "png":
		err = png.Encode(&output, pixels)
	default:
		return TeacherHomepageImage{}, ErrInput
	}
	if err != nil || output.data.Len() == 0 {
		return TeacherHomepageImage{}, ErrInput
	}
	raw := output.data.Bytes()
	sum := sha256.Sum256(raw)
	return TeacherHomepageImage{
		ID: hex.EncodeToString(sum[:]), MediaType: contentType, Width: cfg.Width, Height: cfg.Height, Bytes: raw,
	}, nil
}

type boundedTeacherHomepageImage struct {
	data bytes.Buffer
}

func (output *boundedTeacherHomepageImage) Write(data []byte) (int, error) {
	if len(data) > TeacherHomepageMaxPhotoBytes-output.data.Len() {
		return 0, ErrInput
	}
	return output.data.Write(data)
}

func (s *Service) ReadTeacherHomepageRequestPhoto(ctx context.Context, p Principal, school, id string) (TeacherHomepageImage, error) {
	return s.readRequestPhoto(ctx, p, school, id, true)
}

func (s *Service) TeacherRequestPhotoForDelivery(ctx context.Context, p Principal, school, id string) (TeacherImageKey, error) {
	img, err := s.readRequestPhoto(ctx, p, school, id, false)
	return TeacherImageKey{Kind: RequestPortrait, School: school, OwnerID: id, Digest: img.ID}, err
}

func (s *Service) readRequestPhoto(ctx context.Context, p Principal, school, id string, load bool) (TeacherHomepageImage, error) {
	const action = "request-photo-viewed"
	if err := s.teacherRefreshAccess(ctx, p, school, action, p.CanMaintain); err != nil {
		return TeacherHomepageImage{}, err
	}
	if !ValidID(id) {
		return TeacherHomepageImage{}, ErrInput
	}
	var photo TeacherHomepageImage
	err := s.Repo.Transaction(ctx, func(t Transaction) error {
		port, err := homepageRequestPort(t)
		if err != nil {
			return err
		}
		if load {
			photo, err = port.ReadTeacherHomepageRequestPhoto(school, id)
		} else {
			meta, ok := t.(interface {
				TeacherHomepageRequestPhotoMetadata(string, string) (TeacherHomepageImage, error)
			})
			if !ok {
				return ErrUnavailable
			}
			photo, err = meta.TeacherHomepageRequestPhotoMetadata(school, id)
		}
		if err != nil {
			return err
		}
		if !validTeacherHomepagePhotoMetadata(photo) {
			return ErrUnavailable
		}
		if load {
			if len(photo.Bytes) == 0 || len(photo.Bytes) > TeacherHomepageMaxPhotoBytes {
				return ErrUnavailable
			}
			sum := sha256.Sum256(photo.Bytes)
			if hex.EncodeToString(sum[:]) != photo.ID {
				return ErrUnavailable
			}
		}
		return s.teacherRefreshAudit(t, p, school, action, "teacher-homepage-request/"+id, "success")
	})
	if err != nil {
		return TeacherHomepageImage{}, err
	}
	return photo, nil
}
