package postgres

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/png"
	"slices"
	"testing"
	"time"

	c "github.com/StuHelper/stuhelper-catalog"
)

func bodyMediaDatabase(t *testing.T) (*Repository, *sql.DB) {
	t.Helper()
	repo, db := activityDatabase(t)
	service := &c.Service{Repo: repo}
	for _, school := range []string{"school-a", "school-b"} {
		importer := c.Principal{Subject: "issuer:importer", Schools: []string{school}, CanImport: true}
		seed := c.Import{SchoolID: school, SourceID: "fixture", Revision: 1, FetchedAt: time.Now().UTC(),
			Objects: []c.Object{{ID: school, Kind: "school", SchoolID: school, Name: school}}}
		if _, err := service.Import(context.Background(), importer, "seed-"+school, seed); err != nil {
			t.Fatal(err)
		}
	}
	var role string
	if err := repo.DB.QueryRow("SELECT quote_ident(current_user)").Scan(&role); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`REVOKE UPDATE ON catalog.teacher_homepage_body_images,catalog.teacher_homepage_body_refs FROM ` + role); err != nil {
		t.Fatal(err)
	}
	return repo, db
}

func bodyPicture(t *testing.T, shade uint8) (c.TeacherHomepageBodyImage, []byte) {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	img.Set(0, 0, color.RGBA{R: shade, A: 255})
	var out bytes.Buffer
	if err := png.Encode(&out, img); err != nil {
		t.Fatal(err)
	}
	data := out.Bytes()
	hash := sha256.Sum256(data)
	return c.TeacherHomepageBodyImage{ID: hex.EncodeToString(hash[:]), MediaType: "image/png", Width: 2, Height: 2,
		SizeBytes: int64(len(data))}, data
}

func homepageMediaSite(slug string, media ...c.TeacherHomepageBodyMedia) c.TeacherSite {
	return c.TeacherSite{
		SiteID: slug, Name: slug, URL: "https://shi.buaa.edu.cn/" + slug + "/en/index.htm",
		Profile: c.TeacherHomepage{Name: slug}, BodyMedia: media,
	}
}

func applyBodyMediaSnapshot(ctx context.Context, repo *Repository, school string, revision, budget int64,
	load func(context.Context, string) ([]byte, error), sites ...c.TeacherSite) (bool, error) {
	var limit bool
	err := repo.Transaction(ctx, func(raw c.Transaction) error {
		if err := raw.Lock(school); err != nil {
			return err
		}
		tx := raw.(*transaction)
		err := tx.saveTeacherHomepageSites(c.TeacherSiteSnapshot{
			SchoolID: school, Revision: revision, Sites: sites, ReadBodyImage: load,
		}, budget)
		limit = tx.TeacherHomepageBodyImageQuotaExceeded()
		return err
	})
	return limit, err
}

func assertStoredBodyMedia(t *testing.T, db *sql.DB, blobs, refs, storedBytes int64) {
	t.Helper()
	var gotBlobs, gotRefs, gotBytes int64
	if err := db.QueryRow(`SELECT count(*), (SELECT count(*) FROM catalog.teacher_homepage_body_refs),
 COALESCE(sum(size_bytes),0) FROM catalog.teacher_homepage_body_images`).Scan(&gotBlobs, &gotRefs, &gotBytes); err != nil {
		t.Fatal(err)
	}
	if gotBlobs != blobs || gotRefs != refs || gotBytes != storedBytes {
		t.Fatalf("stored copies=%d refs=%d bytes=%d; want %d, %d, %d", gotBlobs, gotRefs, gotBytes, blobs, refs, storedBytes)
	}
}

func storedBodyMedia(t *testing.T, db *sql.DB, school, site string) []c.TeacherHomepageBodyMedia {
	t.Helper()
	var raw []byte
	if err := db.QueryRow("SELECT body_media FROM catalog.teacher_homepage_sites WHERE school_id=$1 AND site_id=$2", school, site).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var items []c.TeacherHomepageBodyMedia
	if err := json.Unmarshal(raw, &items); err != nil {
		t.Fatal(err)
	}
	return items
}

func TestTeacherHomepageBodyImagesQuotaFallbackAndReplacement(t *testing.T) {
	ctx := context.Background()
	repo, db := bodyMediaDatabase(t)
	oldImage, oldBytes := bodyPicture(t, 10)
	nextImage, nextBytes := bodyPicture(t, 80)
	lastImage, lastBytes := bodyPicture(t, 160)
	brokenImage, _ := bodyPicture(t, 220)
	budget := max(oldImage.SizeBytes, nextImage.SizeBytes, lastImage.SizeBytes)
	imageEntry := func(slug string, img c.TeacherHomepageBodyImage) c.TeacherHomepageBodyMedia {
		return c.TeacherHomepageBodyMedia{Kind: "image", Field: "biography",
			LinkURL: "https://shi.buaa.edu.cn/" + slug + "/en/index.htm", Image: &img}
	}
	attachment := c.TeacherHomepageBodyMedia{
		Kind: "attachment", Field: "biography", LinkURL: "https://shi.buaa.edu.cn/alpha/paper.pdf",
	}
	images := map[string][]byte{oldImage.ID: oldBytes, nextImage.ID: nextBytes, lastImage.ID: lastBytes, brokenImage.ID: []byte("wrong")}
	var reads []string
	readImage := func(_ context.Context, id string) ([]byte, error) {
		reads = append(reads, id)
		return images[id], nil
	}
	apply := func(school string, rev int64, expectedLimit bool, sites ...c.TeacherSite) {
		t.Helper()
		reads = nil
		limited, err := applyBodyMediaSnapshot(ctx, repo, school, rev, budget, readImage, sites...)
		if err != nil || limited != expectedLimit {
			t.Fatalf("replace %s revision=%d limited=%t want=%t err=%v", school, rev, limited, expectedLimit, err)
		}
	}

	alpha := homepageMediaSite("alpha", imageEntry("alpha", oldImage), attachment)
	another := homepageMediaSite("another", imageEntry("another", oldImage))
	if limited, err := applyBodyMediaSnapshot(ctx, repo, "school-a", 1, oldImage.SizeBytes, readImage, alpha, another); limited || err != nil {
		t.Fatalf("exact capacity should accept bytes without alert: limited=%t err=%v", limited, err)
	}
	assertStoredBodyMedia(t, db, 1, 2, oldImage.SizeBytes)
	if !slices.Equal(reads, []string{oldImage.ID}) {
		t.Fatalf("duplicate digest was read more than once: %v", reads)
	}

	missingRead := func(context.Context, string) ([]byte, error) { return nil, errors.New("candidate file unavailable") }
	moved := homepageMediaSite("moved", imageEntry("moved", oldImage))
	moved.SiteID = another.SiteID
	if limited, err := applyBodyMediaSnapshot(ctx, repo, "school-a", 2, budget, missingRead, alpha, moved); limited || err != nil {
		t.Fatalf("changed origin URL should degrade unavailable copy: limited=%t err=%v", limited, err)
	}
	assertStoredBodyMedia(t, db, 1, 1, oldImage.SizeBytes)
	if got := storedBodyMedia(t, db, "school-a", another.SiteID); len(got) != 1 ||
		got[0].Image != nil || got[0].LinkURL != moved.BodyMedia[0].LinkURL {
		t.Fatalf("changed source inherited previous site's verified bytes: %+v", got)
	}
	apply("school-a", 3, false, alpha, another)
	assertStoredBodyMedia(t, db, 1, 2, oldImage.SizeBytes)

	beta := homepageMediaSite("beta", imageEntry("beta", oldImage))
	if limited, err := applyBodyMediaSnapshot(ctx, repo, "school-b", 1, oldImage.SizeBytes, missingRead, beta); limited || err != nil {
		t.Fatalf("unavailable new source should publish its link: limited=%t err=%v", limited, err)
	}
	assertStoredBodyMedia(t, db, 1, 2, oldImage.SizeBytes)
	if got := storedBodyMedia(t, db, "school-b", "beta"); len(got) != 1 ||
		got[0].Image != nil || got[0].LinkURL != beta.BodyMedia[0].LinkURL {
		t.Fatalf("matching digest borrowed another school's copy without valid bytes: %+v", got)
	}
	apply("school-b", 2, false, beta)
	assertStoredBodyMedia(t, db, 1, 3, oldImage.SizeBytes)
	if !slices.Equal(reads, []string{oldImage.ID}) {
		t.Fatalf("cross-school deduplication must verify the new source then share the stored bytes: %v", reads)
	}

	next := imageEntry("alpha", nextImage)
	last := imageEntry("alpha", lastImage)
	replacement := homepageMediaSite("alpha", next, last, attachment)
	canceledRead := func(context.Context, string) ([]byte, error) { return nil, context.Canceled }
	if _, err := applyBodyMediaSnapshot(ctx, repo, "school-a", 4, budget, canceledRead, replacement); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled replacement: %v", err)
	}
	assertStoredBodyMedia(t, db, 1, 3, oldImage.SizeBytes)
	if got := storedBodyMedia(t, db, "school-a", "alpha"); got[0].Image == nil || got[0].Image.ID != oldImage.ID {
		t.Fatalf("canceled replacement changed old site: %+v", got)
	}

	apply("school-a", 4, true, replacement)
	assertStoredBodyMedia(t, db, 1, 1, oldImage.SizeBytes)
	if got := storedBodyMedia(t, db, "school-a", "alpha"); len(got) != 3 ||
		got[0].Image != nil || got[0].LinkURL != next.LinkURL || got[1].Image != nil || got[2].LinkURL != attachment.LinkURL {
		t.Fatalf("quota fallback lost links or retained an image: %+v", got)
	}
	if !slices.Equal(reads, []string{nextImage.ID}) {
		t.Fatalf("quota overflow should stop further new reads: %v", reads)
	}

	apply("school-b", 3, false)
	assertStoredBodyMedia(t, db, 0, 0, 0)
	broken := imageEntry("alpha", brokenImage)
	apply("school-a", 5, false, homepageMediaSite("alpha", broken, next))
	assertStoredBodyMedia(t, db, 1, 1, nextImage.SizeBytes)
	if got := storedBodyMedia(t, db, "school-a", "alpha"); len(got) != 2 || got[0].Image != nil ||
		got[0].LinkURL != broken.LinkURL || got[1].Image == nil || got[1].Image.ID != nextImage.ID {
		t.Fatalf("invalid local bytes affected otherwise valid image or lost source link: %+v", got)
	}

	// A later replacement can use space from an image that the new snapshot no
	// longer needs, even though its previous copy filled the whole budget.
	apply("school-a", 6, false, homepageMediaSite("alpha", last))
	assertStoredBodyMedia(t, db, 1, 1, lastImage.SizeBytes)
	if got := storedBodyMedia(t, db, "school-a", "alpha"); got[0].Image == nil || got[0].Image.ID != lastImage.ID {
		t.Fatalf("existing copy kept space after it was removed from the new snapshot: %+v", got)
	}
	apply("school-a", 7, true, homepageMediaSite("new-first", imageEntry("new-first", nextImage)),
		homepageMediaSite("new-source", imageEntry("new-source", lastImage)))
	assertStoredBodyMedia(t, db, 1, 1, lastImage.SizeBytes)
	if got := storedBodyMedia(t, db, "school-a", "new-source"); len(got) != 1 || got[0].Image == nil || got[0].Image.ID != lastImage.ID {
		t.Fatalf("current candidate lost a verified reusable image at capacity: %+v", got)
	}
}

func TestTeacherHomepageBodyImagesGlobalQuotaSerializesSchools(t *testing.T) {
	repo, db := bodyMediaDatabase(t)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	first, firstBytes := bodyPicture(t, 30)
	second, secondBytes := bodyPicture(t, 55)
	budget := max(first.SizeBytes, second.SizeBytes)
	inFirstRead, releaseFirst := make(chan struct{}), make(chan struct{})
	type result struct {
		limited bool
		err     error
	}
	firstResult, secondResult := make(chan result, 1), make(chan result, 1)
	run := func(school string, img c.TeacherHomepageBodyImage, read func(context.Context, string) ([]byte, error), done chan<- result) {
		item := c.TeacherHomepageBodyMedia{Kind: "image", Field: "biography", LinkURL: "https://shi.buaa.edu.cn/site/en/index.htm", Image: &img}
		limited, err := applyBodyMediaSnapshot(ctx, repo, school, 1, budget, read, homepageMediaSite("site", item))
		done <- result{limited: limited, err: err}
	}
	go run("school-a", first, func(context.Context, string) ([]byte, error) {
		close(inFirstRead)
		select {
		case <-releaseFirst:
			return firstBytes, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}, firstResult)
	select {
	case <-inFirstRead:
	case out := <-firstResult:
		t.Fatalf("first import did not reach image read: %+v", out)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	go run("school-b", second, func(context.Context, string) ([]byte, error) {
		return secondBytes, nil
	}, secondResult)
	// Observe the competing school waiting at the database while the first
	// school still holds the shared capacity lock and has not written bytes.
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		var waiting bool
		err := db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pg_locks
 WHERE locktype='advisory' AND NOT granted AND database=(SELECT oid FROM pg_database WHERE datname=current_database()))`).Scan(&waiting)
		if err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		select {
		case out := <-secondResult:
			t.Fatalf("competing import completed before the shared lock was released: %+v", out)
		case <-ticker.C:
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	close(releaseFirst)
	if out := <-firstResult; out.err != nil || out.limited {
		t.Fatalf("first import: %+v", out)
	}
	if out := <-secondResult; out.err != nil || !out.limited {
		t.Fatalf("competing import must reach the one global limit: %+v", out)
	}
	assertStoredBodyMedia(t, db, 1, 1, first.SizeBytes)
	if got := storedBodyMedia(t, db, "school-b", "site"); len(got) != 1 || got[0].Image != nil {
		t.Fatalf("competing school's skipped image must remain a link: %+v", got)
	}
}

func TestTeacherHomepageBodyImageQuotaWarningPersistsOutsidePublicJSON(t *testing.T) {
	repo, db := bodyMediaDatabase(t)
	ctx := t.Context()
	now := time.Now().UTC().Truncate(time.Microsecond)
	const firstID, secondID = "completed-with-warning", "completed-without-warning"
	older := c.TeacherRefreshRun{ID: firstID, SchoolID: "school-a", Status: "succeeded", RequestedAt: now, BodyImageQuotaExceeded: true}
	newer := c.TeacherRefreshRun{ID: secondID, SchoolID: older.SchoolID, Status: "succeeded", RequestedAt: now.Add(time.Second), BodyImageQuotaKnown: true}
	latest := c.TeacherRefreshRun{ID: "legacy-completed", SchoolID: older.SchoolID, Status: "succeeded", RequestedAt: now.Add(2 * time.Second)}
	if err := repo.Transaction(ctx, func(raw c.Transaction) error {
		tx := raw.(*transaction)
		if err := tx.SaveTeacherRefresh(older); err != nil {
			return err
		}
		if err := tx.SaveTeacherRefresh(newer); err != nil {
			return err
		}
		return tx.SaveTeacherRefresh(latest)
	}); err != nil {
		t.Fatal(err)
	}
	var flagged bool
	if err := db.QueryRowContext(ctx, `SELECT (data->>'body_image_quota_exceeded')::boolean
 FROM catalog.teacher_refresh_runs WHERE id=$1`, firstID).Scan(&flagged); err != nil || !flagged {
		t.Fatalf("private quota flag missing: %t %v", flagged, err)
	}
	if count, err := repo.PruneTeacherRefreshRuns(ctx, now.Add(time.Minute), 1); err != nil || count != 1 {
		t.Fatalf("expected older run to move to replay storage: %d %v", count, err)
	}
	if err := repo.Transaction(ctx, func(raw c.Transaction) error {
		tx := raw.(*transaction)
		run, err := tx.TeacherRefresh(firstID)
		if err != nil || !run.BodyImageQuotaKnown || !run.BodyImageQuotaExceeded {
			t.Fatalf("quota flag missing from replay: %+v %v", run, err)
		}
		exposed, err := json.Marshal(run)
		if err != nil || bytes.Contains(exposed, []byte("body_image_quota_exceeded")) {
			t.Fatalf("legacy response shape changed: %s %v", exposed, err)
		}
		newest, err := tx.TeacherRefreshLatest(older.SchoolID)
		if err != nil || newest == nil || newest.ID != latest.ID || newest.BodyImageQuotaKnown {
			t.Fatalf("legacy run acquired a quota result it never measured: %+v %v", newest, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	service := &c.Service{Repo: repo}
	importer := c.Principal{Subject: "issuer:operator", Schools: []string{older.SchoolID}, CanImport: true}
	run, err := service.LastTeacherRefreshBodyImageQuota(ctx, importer, older.SchoolID)
	if err != nil || run == nil || run.ID != secondID || !run.BodyImageQuotaKnown || run.BodyImageQuotaExceeded {
		t.Fatalf("latest explicit recovery state should be known clear: %+v %v", run, err)
	}
	if _, err := service.LastTeacherRefreshBodyImageQuota(ctx, importer, "school-b"); !errors.Is(err, c.ErrForbidden) {
		t.Fatalf("recovery access crossed school scope: %v", err)
	}
	importer.CanImport = false
	if _, err := service.LastTeacherRefreshBodyImageQuota(ctx, importer, older.SchoolID); !errors.Is(err, c.ErrForbidden) {
		t.Fatalf("ordinary school principal accessed private recovery state: %v", err)
	}
}
