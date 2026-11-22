package catalog_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"testing"

	c "github.com/StuHelper/stuhelper-catalog"
	"github.com/StuHelper/stuhelper-catalog/httpapi"
	"github.com/StuHelper/stuhelper-catalog/postgres"
	"github.com/StuHelper/stuhelper-catalog/teacherimages"
)

type testCOS struct {
	originals map[string][]byte
	serving   map[string]bool
	failRead  bool
}

func (m *testCOS) Write(_ context.Context, k c.TeacherImageKey, b []byte, save func([]byte) error) error {
	m.originals[k.Digest] = bytes.Clone(b)
	return save(nil)
}
func (m *testCOS) Read(_ context.Context, k c.TeacherImageKey, _ func() ([]byte, error)) ([]byte, error) {
	if m.failRead || len(m.originals[k.Digest]) == 0 {
		return nil, c.ErrUnavailable
	}
	return m.originals[k.Digest], nil
}
func (m *testCOS) PublishCopy(_ context.Context, k c.TeacherImageKey) error {
	if len(m.originals[k.Digest]) == 0 {
		return c.ErrUnavailable
	}
	m.serving[k.Digest] = true
	return nil
}
func (m *testCOS) RemoveObject(_ context.Context, k c.TeacherImageKey, original bool) error {
	if original {
		delete(m.originals, k.Digest)
	} else {
		delete(m.serving, k.Digest)
	}
	return nil
}

func TestTeacherImageMigrationAndRevokedDelivery(t *testing.T) {
	ctx := context.Background()
	s, _, db, snapshot := homepageFixture(t)
	s.ReadAccess = &bodyMediaReadAccess{}
	repo := s.Repo.(*postgres.Repository)
	migration := &postgres.Repository{DB: db}
	store := &testCOS{originals: map[string][]byte{}, serving: map[string]bool{}, failRead: true}
	if _, err := migration.MigrateTeacherImages(ctx, store, false, 1); err == nil {
		t.Fatal("unverified copy cleared inline bytes")
	}
	store.failRead = false
	for _, want := range []int{1, 1, 0} {
		n, err := migration.MigrateTeacherImages(ctx, store, false, 1)
		if err != nil || n != want {
			t.Fatalf("migration count %d, want %d: %v", n, want, err)
		}
	}
	repo.TeacherImageStorage = store
	ip := netip.MustParseAddr("127.0.0.1")
	delivery, err := teacherimages.NewDelivery("media.example.test", strings.Repeat("x", 32), repo)
	if err != nil {
		t.Fatal(err)
	}
	h := httpapi.Handler{Service: s, Authenticate: func(r *http.Request) (c.Principal, error) {
		return c.Principal{Subject: r.Header.Get("Authorization")}, nil
	}, ImageRedirect: func(r *http.Request, p c.Principal, k c.TeacherImageKey) (string, error) {
		return delivery.Issue(r.Context(), p.Subject, ip, k)
	}}.Routes()
	digest := snapshot.Sites[0].Photo.ID
	url := homepagePublic + "/photos/" + digest
	homepageCall(t, h, "GET", url, "issuer:verified-other", "", "", 403)
	res := homepageCall(t, h, "GET", url, homepageReader, "", "", 302)
	if !strings.HasPrefix(res.Header().Get("Location"), "https://media.example.test/avatars/"+digest+"?token=") || !store.serving[digest] {
		t.Fatal("missing signed delivery")
	}
	if _, err := db.Exec(`UPDATE catalog.teacher_image_signature_limits SET issued=120 WHERE scope='user'`); err != nil {
		t.Fatal(err)
	}
	limited := homepageCall(t, h, "GET", url, homepageReader, "", "", 429)
	retry, err := strconv.Atoi(limited.Header().Get("Retry-After"))
	if err != nil || retry < 1 || retry > 60 {
		t.Fatal("missing retry delay on 429")
	}
	homepageSet(t, homepageHandler(s), 0, "hidden", "[]", "hide-live-picture")
	homepageCall(t, h, "HEAD", url, homepageReader, "", "", 404)
	if _, err = repo.ReconcileTeacherImages(ctx, 10); err != nil {
		t.Fatal(err)
	}
	if store.serving[digest] || len(store.originals[digest]) == 0 {
		t.Fatal("hidden delivery or retained original incorrect")
	}
	purger := &testImagePurger{path: "avatars/" + digest}
	if _, err = repo.FlushTeacherImagePurges(ctx, 5, purger); err != nil {
		t.Fatal(err)
	}
	purger.fail = true
	if _, err = repo.FlushTeacherImagePurges(ctx, 5, purger); err == nil {
		t.Fatal("failed provider status acknowledged")
	}
	purger.fail = false
	n, err := repo.FlushTeacherImagePurges(ctx, 5, purger)
	if n != 1 || err != nil || purger.submitted != 1 {
		t.Fatalf("purge resume count %d: %v", n, err)
	}
	for _, want := range []int{1, 1, 0} {
		n, err = migration.MigrateTeacherImages(ctx, store, true, 1)
		if n != want || err != nil {
			t.Fatalf("restore count %d want %d: %v", n, want, err)
		}
	}
	var data []byte
	if err = db.QueryRow("SELECT data FROM catalog.teacher_homepage_photos LIMIT 1").Scan(&data); err != nil || !bytes.Equal(data, snapshot.Sites[0].Photo.Bytes) {
		t.Fatal("restored data mismatch")
	}
}

func TestTeacherImageIssuanceLimits(t *testing.T) {
	repo, db := database(t)
	ctx := context.Background()
	ip := netip.MustParseAddr("127.0.0.1")
	for _, v := range []struct {
		scope string
		limit int
	}{{"user", 120}, {"ip", 240}, {"global", 1200}} {
		if _, err := db.Exec("DELETE FROM catalog.teacher_image_signature_limits"); err != nil {
			t.Fatal(err)
		}
		if err := repo.TakeTeacherImageSignature(ctx, "u", ip); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec("UPDATE catalog.teacher_image_signature_limits SET issued=$1 WHERE scope=$2", v.limit-1, v.scope); err != nil {
			t.Fatal(err)
		}
		if err := repo.TakeTeacherImageSignature(ctx, "u", ip); err != nil {
			t.Fatalf("%s denied its last allowed issuance: %v", v.scope, err)
		}
		if err := repo.TakeTeacherImageSignature(ctx, "u", ip); !errors.Is(err, c.ErrRateLimited) {
			t.Fatalf("%s allowed: %v", v.scope, err)
		}
		var sum int
		if err := db.QueryRow("SELECT SUM(issued) FROM catalog.teacher_image_signature_limits").Scan(&sum); err != nil || sum != v.limit+4 {
			t.Fatal("rejected request changed counters")
		}
	}
}

type testImagePurger struct {
	path      string
	submitted int
	fail      bool
}

func (p *testImagePurger) Submit(_ context.Context, paths []string) (string, error) {
	if len(paths) != 1 || paths[0] != p.path {
		return "", c.ErrInput
	}
	p.submitted++
	return "test-job", nil
}
func (p *testImagePurger) Status(_ context.Context, job string, paths []string) (bool, bool, error) {
	if p.fail || job != "test-job" || len(paths) != 1 || paths[0] != p.path {
		return false, false, c.ErrUnavailable
	}
	return true, false, nil
}

func TestTeacherImageDeliveryRequiresCurrentOverrideRow(t *testing.T) {
	s, _, db, _ := homepageFixture(t)
	s.ReadAccess = &bodyMediaReadAccess{}
	h := homepageHandler(s)
	uploaded, err := json.Marshal(c.TeacherHomepageFieldPhoto{ContentType: "image/png", Data: homepagePortrait(t)})
	if err != nil {
		t.Fatal(err)
	}
	homepageSet(t, h, 0, "visible", fmt.Sprintf(`[{"field":{"field":"photo"},"hidden":false,"override":{"photo":%s}}]`, uploaded), "manual-photo")
	profile := homepagePublicProfile(t, h, homepagePublic)
	if profile == nil || profile.Photo == nil {
		t.Fatal("missing manual portrait")
	}
	_, digest, ok := strings.Cut(profile.Photo.URL, "/photos/")
	if !ok {
		t.Fatal("missing photo URL")
	}
	p := c.Principal{Subject: homepageReader}
	if _, err = s.TeacherPhotoForDelivery(context.Background(), p, "official", digest); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("DELETE FROM catalog.teacher_homepage_override_photos WHERE school_id='school' AND teacher_id='official'"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.TeacherPhotoForDelivery(context.Background(), p, "official", digest); !errors.Is(err, c.ErrNotFound) {
		t.Fatal("orphaned override metadata authorized a delivery")
	}
}
