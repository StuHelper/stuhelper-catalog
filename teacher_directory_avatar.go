package catalog

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"image"
	"image/color"
	"image/jpeg"
)

const TeacherAvatarSize = 128
const TeacherAvatarMaxBytes = 32 << 10
const TeacherAvatarCacheEntries = 8192
const DirectoryPortrait TeacherImageKind = "directory-portrait"

type avatarCache interface {
	CachedTeacherAvatar(context.Context, string, bool) (TeacherHomepageImage, bool, error)
	CacheTeacherAvatar(context.Context, string, TeacherHomepageImage) error
}

// Authorization precedes even a warm cache lookup. Both the directory URL and
// the cache key identify the homepage photo from which the JPEG was generated.
func (s *Service) ReadTeacherAvatar(ctx context.Context, p Principal, id, source string, loadBytes bool) (TeacherHomepageImage, TeacherImageKey, error) {
	key, err := s.TeacherPhotoForDelivery(ctx, p, id, source)
	if err != nil {
		return TeacherHomepageImage{}, TeacherImageKey{}, err
	}
	cache, ok := s.Repo.(avatarCache)
	if !ok {
		return TeacherHomepageImage{}, TeacherImageKey{}, ErrUnavailable
	}
	thumb, hit, err := cache.CachedTeacherAvatar(ctx, source, loadBytes)
	if err != nil {
		return TeacherHomepageImage{}, TeacherImageKey{}, err
	}
	if !hit {
		select {
		case teacherHomepagePhotoUploads <- struct{}{}:
			defer func() { <-teacherHomepagePhotoUploads }()
		case <-ctx.Done():
			return TeacherHomepageImage{}, TeacherImageKey{}, ctx.Err()
		}
		// Another request can fill the cache while this one waits for a decoder.
		thumb, hit, err = cache.CachedTeacherAvatar(ctx, source, loadBytes)
		if err == nil && !hit {
			var photo TeacherHomepageImage
			photo, err = s.ReadTeacherHomepagePhoto(ctx, p, id, source)
			if err == nil {
				thumb, err = makeTeacherAvatar(photo)
			}
			if err == nil {
				err = cache.CacheTeacherAvatar(ctx, source, thumb)
			}
		}
		if err != nil {
			return TeacherHomepageImage{}, TeacherImageKey{}, err
		}
	}
	key.Kind = DirectoryPortrait
	key.Digest = thumb.ID
	return thumb, key, nil
}

func makeTeacherAvatar(photo TeacherHomepageImage) (TeacherHomepageImage, error) {
	cfg, format, err := image.DecodeConfig(bytes.NewReader(photo.Bytes))
	if err != nil || !validTeacherHomepagePhotoDimensions(cfg.Width, cfg.Height) || cfg.Width != photo.Width || cfg.Height != photo.Height || "image/"+format != photo.MediaType {
		return TeacherHomepageImage{}, ErrUnavailable
	}
	pixels, _, err := image.Decode(bytes.NewReader(photo.Bytes))
	if err != nil {
		return TeacherHomepageImage{}, ErrUnavailable
	}
	// Box averaging prevents fine portrait detail aliasing when reducing large photos.
	const size = TeacherAvatarSize
	crop := min(cfg.Width, cfg.Height)
	left, top := (cfg.Width-crop)/2, (cfg.Height-crop)/2
	dst := image.NewRGBA(image.Rect(0, 0, size, size))
	for y := 0; y < size; y++ {
		y0 := top + y*crop/size
		y1 := max(y0+1, top+(y+1)*crop/size)
		for x := 0; x < size; x++ {
			x0 := left + x*crop/size
			x1 := max(x0+1, left+(x+1)*crop/size)
			var red, green, blue uint64
			for sy := y0; sy < y1; sy++ {
				for sx := x0; sx < x1; sx++ {
					r, g, b, a := pixels.At(sx, sy).RGBA()
					white := uint32(65535) - a
					red += uint64(r + white)
					green += uint64(g + white)
					blue += uint64(b + white)
				}
			}
			scale := uint64((x1-x0)*(y1-y0)) * 257
			dst.SetRGBA(x, y, color.RGBA{R: uint8(red / scale), G: uint8(green / scale), B: uint8(blue / scale), A: 255})
		}
	}
	var out bytes.Buffer
	if err := jpeg.Encode(&out, dst, &jpeg.Options{Quality: 82}); err != nil || out.Len() > TeacherAvatarMaxBytes {
		return TeacherHomepageImage{}, ErrUnavailable
	}
	hash := sha256.Sum256(out.Bytes())
	return TeacherHomepageImage{ID: hex.EncodeToString(hash[:]), MediaType: "image/jpeg", Width: size, Height: size, Bytes: out.Bytes()}, nil
}
