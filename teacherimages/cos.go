// Package teacherimages stores validated Catalog teacher images in private COS.
package teacherimages

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	c "github.com/StuHelper/stuhelper-catalog"
	cos "github.com/tencentyun/cos-go-sdk-v5"
)

var bucketName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,48}-[0-9]{5,20}$`)
var regionName = regexp.MustCompile(`^[a-z]{2}-[a-z]+(-[a-z0-9]+)?$`)
var digestName = regexp.MustCompile(`^[0-9a-f]{64}$`)

type Store struct{ client *cos.Client }

func NewStore(bucket, region, id, secret string) (*Store, error) {
	if !bucketName.MatchString(bucket) || !regionName.MatchString(region) || id == "" || secret == "" {
		return nil, c.ErrInput
	}
	origin := &url.URL{Scheme: "https", Host: bucket + ".cos." + region + ".myqcloud.com"}
	transport := &cos.AuthorizationTransport{SecretID: id, SecretKey: secret}
	client := &http.Client{Transport: transport, Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return &Store{cos.NewClient(&cos.BaseURL{BucketURL: origin}, client)}, nil
}

func object(k c.TeacherImageKey) (string, int, error) {
	if !digestName.MatchString(k.Digest) {
		return "", 0, c.ErrInput
	}
	switch k.Kind {
	case c.BodyImageObject:
		return "body/" + k.Digest, c.TeacherHomepageMaxBodyImageBytes, nil
	case c.SitePortrait, c.OverridePortrait, c.RequestPortrait:
		return "avatars/" + k.Digest, c.TeacherHomepageMaxPhotoBytes, nil
	default:
		return "", 0, c.ErrInput
	}
}

func verified(data []byte, k c.TeacherImageKey, max int) bool {
	if len(data) == 0 || len(data) > max {
		return false
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]) == k.Digest
}

func (s *Store) Write(ctx context.Context, k c.TeacherImageKey, data []byte, publish func([]byte) error) error {
	name, max, err := object(k)
	if err != nil || !verified(data, k, max) || publish == nil {
		return c.ErrInput
	}
	mime := http.DetectContentType(data)
	if mime != "image/jpeg" && mime != "image/png" {
		return c.ErrInput
	}
	checksum := md5.Sum(data) // Content-MD5 makes COS verify the upload in transit.
	headers := &cos.ObjectPutHeaderOptions{ContentType: mime, ContentMD5: base64.StdEncoding.EncodeToString(checksum[:]), CacheControl: "private, no-store"}
	if _, err = s.client.Object.Put(ctx, original(name), bytes.NewReader(data), &cos.ObjectPutOptions{ObjectPutHeaderOptions: headers}); err != nil {
		return c.ErrUnavailable
	}
	return publish(nil)
}

func (s *Store) Read(ctx context.Context, k c.TeacherImageKey, _ func() ([]byte, error)) ([]byte, error) {
	name, max, err := object(k)
	if err != nil {
		return nil, err
	}
	resp, err := s.client.Object.Get(ctx, original(name), nil)
	if err != nil {
		return nil, c.ErrUnavailable
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, int64(max)+1))
	if err != nil || !verified(data, k, max) {
		return nil, c.ErrUnavailable
	}
	return data, nil
}

func original(name string) string {
	prefix, digest, _ := strings.Cut(name, "/")
	return prefix + "/private/" + digest
}

func (s *Store) PublishCopy(ctx context.Context, k c.TeacherImageKey) error {
	name, _, err := object(k)
	if err != nil {
		return err
	}
	_, _, err = s.client.Object.Copy(ctx, name, s.client.BaseURL.BucketURL.Host+"/"+original(name), nil)
	if err != nil {
		return c.ErrUnavailable
	}
	return nil
}

func (s *Store) RemoveObject(ctx context.Context, k c.TeacherImageKey, source bool) error {
	name, _, err := object(k)
	if err != nil {
		return err
	}
	if source {
		name = original(name)
	}
	if _, err = s.client.Object.Delete(ctx, name); err != nil {
		return c.ErrUnavailable
	}
	return nil
}
