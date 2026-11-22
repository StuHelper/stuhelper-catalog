package catalog_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	c "github.com/StuHelper/stuhelper-catalog"
	"github.com/StuHelper/stuhelper-catalog/postgres"
)

func TestTeacherHomepageRequestRepositoryScopeAndKeyset(t *testing.T) {
	s, p, seed := fixture(t)
	ctx := context.Background()
	p.Schools = append(p.Schools, "other-school")
	if _, err := s.Import(ctx, p, "homepage-requests-seed", seed); err != nil {
		t.Fatal(err)
	}
	other := c.Import{SchoolID: "other-school", SourceID: "fixture", Revision: 1, FetchedAt: seed.FetchedAt,
		Objects: []c.Object{
			{ID: "other-school", Kind: "school", Name: "Other School", SchoolID: "other-school"},
			{ID: "other-teacher", Kind: "teacher", Name: "Other Teacher", SchoolID: "other-school"},
		}}
	if _, err := s.Import(ctx, p, "homepage-requests-other-school", other); err != nil {
		t.Fatal(err)
	}
	const owner = "issuer:teacher-owner"
	now := seed.FetchedAt.Truncate(time.Microsecond)
	request := func(id, school, teacher, subject string, at time.Time) c.TeacherHomepageRequest {
		return c.TeacherHomepageRequest{
			ID: id, SchoolID: school, TeacherID: teacher, Submitter: subject,
			TeacherHomepageRequestInput: c.TeacherHomepageRequestInput{Kind: "hide", Message: "Original explanation"},
			Status:                      "pending", Revision: 1, CreatedAt: at,
		}
	}
	if err := s.Repo.Transaction(ctx, func(tx c.Transaction) error {
		port, ok := tx.(c.TeacherHomepageRequestTransaction)
		if !ok {
			return c.ErrUnavailable
		}
		for _, submission := range []c.TeacherHomepageRequest{
			request("req-a", "school", "teacher", owner, now),
			request("req-b", "school", "teacher", owner, now),
			request("req-c", "school", "teacher", owner, now.Add(time.Second)),
			request("req-other-owner", "school", "teacher", "issuer:other", now),
			request("req-other-teacher", "school", "teacher2", owner, now),
			request("req-other-school", "other-school", "other-teacher", owner, now),
		} {
			if err := port.InsertTeacherHomepageRequest(submission); err != nil {
				return err
			}
		}
		if err := port.InsertTeacherHomepageRequest(request("req-wrong-school", "other-school", "teacher", owner, now)); !errors.Is(err, c.ErrNotFound) {
			t.Errorf("cross-school insertion must miss the teacher: %v", err)
		}
		if _, err := port.GetTeacherHomepageRequest("other-school", "req-b"); !errors.Is(err, c.ErrNotFound) {
			t.Errorf("school-scoped getter must hide another school's request: %v", err)
		}
		query := c.TeacherHomepageRequestQuery{SchoolID: "school", TeacherID: "teacher", Submitter: owner, Status: "pending", Limit: 2}
		first, total, more, err := port.ListTeacherHomepageRequests(query)
		if err != nil || total != 3 || !more || !slices.Equal(teacherHomepageRequestIDs(first), []string{"req-c", "req-b"}) {
			t.Errorf("unexpected first page: ids=%v total=%d more=%t err=%v", teacherHomepageRequestIDs(first), total, more, err)
			return err
		}
		resolution := first[1]
		query.After, query.AfterCreatedAt = resolution.ID, resolution.CreatedAt
		resolvedAt := now.Add(time.Minute)
		resolution.Status, resolution.Revision = "rejected", 2
		resolution.ResolvedAt = &resolvedAt
		resolution.Message = "This must never replace the proposal"
		resolution.ResolutionNote = "Moderator decision"
		if err := port.ResolveTeacherHomepageRequest(resolution); err != nil {
			return err
		}
		if err := port.ResolveTeacherHomepageRequest(resolution); !errors.Is(err, c.ErrConflict) {
			t.Errorf("second transition must conflict: %v", err)
		}
		later, total, more, err := port.ListTeacherHomepageRequests(query)
		if err != nil || total != 2 || more || !slices.Equal(teacherHomepageRequestIDs(later), []string{"req-a"}) {
			t.Errorf("cursor must still work after its row is resolved: ids=%v total=%d more=%t err=%v", teacherHomepageRequestIDs(later), total, more, err)
		}
		owned, err := port.ReadTeacherHomepageRequestsForOwner(owner, []string{"req-b", "req-other-owner", "req-absent"})
		if err != nil || len(owned) != 1 || owned[0].ID != "req-b" {
			t.Errorf("owner-scoped reference lookup leaked or lost records: count=%d %v", len(owned), err)
			return err
		}
		if _, err := port.ReadTeacherHomepageRequestForOwner(owner, "req-other-owner"); !errors.Is(err, c.ErrNotFound) {
			t.Errorf("owner-scoped receipt lookup must hide another account's request: %v", err)
		}
		got, err := port.ReadTeacherHomepageRequestForOwner(owner, "req-b")
		if err != nil {
			return err
		}
		if got.Message != "Original explanation" || got.ResolutionNote != resolution.ResolutionNote || got.Status != "rejected" || got.Revision != 2 || got.SchoolID != "school" || got.Submitter != owner {
			t.Errorf("request or private ownership changed: %+v", got)
		}
		encoded, err := json.Marshal(got)
		if strings.Contains(string(encoded), owner) || strings.Contains(string(encoded), `"school_id"`) {
			t.Errorf("private ownership appeared in serialized request: %s", encoded)
		}
		if err != nil {
			return err
		}
		largeBody := strings.Repeat("x", 950_000)
		const largeOwner = "issuer:large-proposals"
		for _, id := range []string{"large-a", "large-b", "large-c", "large-d"} {
			submission := request(id, "school", "teacher", largeOwner, now)
			// Exercise materialized request sizes independent of input DTO normalization.
			submission.Message = largeBody
			if err := port.InsertTeacherHomepageRequest(submission); err != nil {
				return err
			}
		}
		large, total, more, err := port.ListTeacherHomepageRequests(c.TeacherHomepageRequestQuery{
			SchoolID: "school", Submitter: largeOwner, Limit: 100,
		})
		if err != nil || total != 4 || !more || !slices.Equal(teacherHomepageRequestIDs(large), []string{"large-d", "large-c", "large-b"}) {
			t.Errorf("database byte budget must constrain the page: ids=%v total=%d more=%t err=%v", teacherHomepageRequestIDs(large), total, more, err)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func teacherHomepageRequestIDs(requests []c.TeacherHomepageRequest) []string {
	ids := make([]string, 0, len(requests))
	for _, request := range requests {
		ids = append(ids, request.ID)
	}
	return ids
}

func TestTeacherHomepageRequestRetention(t *testing.T) {
	ctx := context.Background()
	repo, migration := database(t)
	service := &c.Service{Repo: repo}
	now := time.Now().UTC().Truncate(time.Microsecond)
	admin := c.Principal{Subject: "issuer:maintainer", Schools: []string{"school"}, CanImport: true}
	seed := c.Import{SchoolID: "school", SourceID: "fixture", Revision: 1, FetchedAt: now, Objects: []c.Object{
		{ID: "school", SchoolID: "school", Kind: "school", Name: "School"},
		{ID: "teacher", SchoolID: "school", Kind: "teacher", Name: "Teacher"},
	}}
	if _, err := service.Import(ctx, admin, "homepage-retention-seed", seed); err != nil {
		t.Fatal(err)
	}
	var role string
	if err := repo.DB.QueryRow(`SELECT quote_ident(current_user)`).Scan(&role); err != nil {
		t.Fatal(err)
	}
	if _, err := migration.Exec(`REVOKE INSERT,UPDATE,DELETE,TRUNCATE ON catalog.teacher_homepage_requests FROM ` + role + `;
 GRANT INSERT(id,school_id,teacher_id,submitter,status,revision,created_at,data),
 UPDATE(status,revision,resolved_at,data) ON catalog.teacher_homepage_requests TO ` + role + `;
 REVOKE ALL ON TABLE catalog.teacher_homepage_request_holds FROM ` + role); err != nil {
		t.Fatal(err)
	}
	repo.RequireTeacherDirectory = true
	if err := repo.CheckRuntime(ctx); err != nil {
		t.Fatalf("request repository must accept column-scoped production grants: %v", err)
	}
	photo, err := c.ValidateTeacherHomepagePhotoUpload("image/png", homepagePortrait(t))
	if err != nil {
		t.Fatal(err)
	}
	expiredAt := now.AddDate(0, -13, 0)
	recentAt := now.AddDate(0, -11, 0)
	// Pending and held requests use the same old clock as the expired request.
	for _, fixture := range []struct {
		id     string
		closed bool
		at     time.Time
	}{
		{"expire", true, expiredAt}, {"held", true, expiredAt},
		{"pending", false, expiredAt}, {"recent", true, recentAt}, {"expired-hold", true, expiredAt},
	} {
		err := repo.Transaction(ctx, func(tx c.Transaction) error {
			port := tx.(c.TeacherHomepageRequestTransaction)
			submission := c.TeacherHomepageRequest{SchoolID: "school", TeacherID: "teacher", ID: fixture.id,
				Submitter: "issuer:teacher-owner", Status: "pending", Revision: 1,
				CreatedAt: fixture.at.Add(-time.Hour), TeacherHomepageRequestInput: c.TeacherHomepageRequestInput{
					Kind: "correction", Message: "Private proposal",
				}}
			if err := port.InsertTeacherHomepageRequest(submission); err != nil {
				return err
			}
			if err := port.SaveTeacherHomepageRequestPhoto("school", submission.ID, photo); err != nil {
				return err
			}
			replayKey := homepageRetentionReplayKey(submission.ID)
			if err := tx.SaveReplay(replayKey, c.Replay{Digest: "test-digest", Result: []byte(submission.ID)}); err != nil {
				return err
			}
			if err := port.LinkTeacherHomepageRequestReplay(submission.ID, replayKey); err != nil {
				return err
			}
			if fixture.closed {
				submission.Status, submission.Revision, submission.ResolvedAt = "approved", 2, &fixture.at
				if err := port.ResolveTeacherHomepageRequest(submission); err != nil {
					return err
				}
			}
			return tx.(c.TeacherRefreshTransaction).RecordTeacherRefreshAudit(c.Event{
				ID: fixture.id + ":audit", Subject: "teacher-homepage-request/" + fixture.id,
				SchoolID: "school", Actor: admin.Subject, Type: "catalog.teacher-homepage-request.submitted.v1",
				Time: fixture.at, Revision: 1, Result: "success",
			})
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err := migration.Exec(`SELECT catalog.register_teacher_homepage_request_hold(
 'held','synthetic-case-one','Order','Evidence','Case resolution',$1,$2,'issuer:governance')`, now.Add(time.Hour), now.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := migration.Exec(`INSERT INTO catalog.teacher_homepage_request_holds
 (request_id,reference,basis,purpose,end_condition,registered_at,review_at,retain_until) VALUES
 ('expired-hold','synthetic-case-two','Order','Evidence','End of term',$1,$2,$3)`,
		expiredAt, now.Add(-2*time.Hour), now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.DB.Exec(`SELECT catalog.release_teacher_homepage_request_hold('held','issuer:teacher-owner')`); err == nil {
		t.Fatal("ordinary runtime must lack authority over legal holds")
	}
	if err := repo.Transaction(ctx, func(tx c.Transaction) error {
		port := tx.(c.TeacherHomepageRequestTransaction)
		if _, err := port.GetTeacherHomepageRequest("school", "held"); !errors.Is(err, c.ErrNotFound) {
			t.Errorf("ordinary school review must not expose held evidence after the normal window: %v", err)
		}
		if _, err := port.ReadTeacherHomepageRequestForOwner("issuer:teacher-owner", "held"); !errors.Is(err, c.ErrNotFound) {
			t.Errorf("ordinary owner read must not expose expired held evidence: %v", err)
		}
		if _, err := port.ReadTeacherHomepageRequestPhoto("school", "held"); !errors.Is(err, c.ErrNotFound) {
			t.Errorf("ordinary photo lookup must not expose expired held evidence: %v", err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := migration.Exec(`INSERT INTO catalog.audit(id,data,audit_action,audit_target,audit_time) VALUES
 ('unrelated:audit','{}','catalog.teacher-sites.decision-bind.v1','site/synthetic',$1)`, expiredAt); err != nil {
		t.Fatal(err)
	}
	if _, err := migration.Exec(`INSERT INTO catalog.teacher_homepage_controls(school_id,teacher_id,revision,page_visibility,rules,updated_at)
 VALUES ('school','teacher',1,'hidden','[]',$1)`, expiredAt); err != nil {
		t.Fatal(err)
	}
	if _, err := migration.Exec(`INSERT INTO catalog.teacher_homepage_override_photos(school_id,teacher_id,photo_id,media_type,width,height,data)
 VALUES ('school','teacher',$1,$2,$3,$4,$5)`, photo.ID, photo.MediaType, photo.Width, photo.Height, photo.Bytes); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.PruneTeacherHomepageRequests(ctx); !errors.Is(err, c.ErrUnavailable) {
		t.Fatalf("ordinary runtime must lack cleanup execution: %v", err)
	}
	maint := &postgres.Repository{DB: migration}
	if n, err := maint.PruneTeacherHomepageRequests(ctx); n != 5 || err != nil {
		t.Fatalf("first sweep must remove two expired requests and three feature audits: %d %v", n, err)
	}
	var requests, photos, replays, retainedDisplay, retainedPhoto int
	if err := migration.QueryRow(`SELECT (SELECT count(*) FROM catalog.teacher_homepage_requests),
 (SELECT count(*) FROM catalog.teacher_homepage_request_photos),
 (SELECT count(*) FROM catalog.teacher_homepage_request_replays),
 (SELECT count(*) FROM catalog.teacher_homepage_controls WHERE page_visibility='hidden'),
 (SELECT count(*) FROM catalog.teacher_homepage_override_photos WHERE photo_id=$1)`, photo.ID).
		Scan(&requests, &photos, &replays, &retainedDisplay, &retainedPhoto); err != nil {
		t.Fatal(err)
	}
	if requests != 3 || photos != 3 || replays != 3 || retainedDisplay != 1 || retainedPhoto != 1 {
		t.Fatalf("cleanup crossed a storage boundary: %d requests, %d private photos, %d links, %d controls, %d published photos",
			requests, photos, replays, retainedDisplay, retainedPhoto)
	}
	var dangling, retainedAudits int
	if err := migration.QueryRow(`SELECT (SELECT count(*) FROM catalog.replays WHERE key=$1),
 (SELECT count(*) FROM catalog.audit WHERE id IN ('held:audit','recent:audit','unrelated:audit'))`, homepageRetentionReplayKey("expire")).
		Scan(&dangling, &retainedAudits); err != nil || dangling != 0 || retainedAudits != 3 {
		t.Fatalf("wrong receipt or audit retention: %d expired receipts, %d retained audits, %v", dangling, retainedAudits, err)
	}
	if n, err := maint.PruneTeacherHomepageRequests(ctx); n != 0 || err != nil {
		t.Fatalf("repeat sweep must be idempotent: %d %v", n, err)
	}

	// A governance hold pending commit serializes the audit sweep as well as its material deletion.
	holdUpdate, err := migration.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer holdUpdate.Rollback()
	var released bool
	if err := holdUpdate.QueryRow(`SELECT catalog.release_teacher_homepage_request_hold('held','issuer:governance')`).Scan(&released); err != nil || !released {
		t.Fatalf("governance could not release a valid hold: %t %v", released, err)
	}
	if n, err := maint.PruneTeacherHomepageRequests(ctx); n != 0 || err != nil {
		t.Fatalf("sweep must defer while a hold changes: %d %v", n, err)
	}
	if err := holdUpdate.Commit(); err != nil {
		t.Fatal(err)
	}
	if n, err := maint.PruneTeacherHomepageRequests(ctx); n != 2 || err != nil {
		t.Fatalf("released hold must allow expired request and its audit to be swept: %d %v", n, err)
	}
	var governanceAudits int
	if err := migration.QueryRow(`SELECT count(*) FROM catalog.audit WHERE audit_action IN (
 'catalog.teacher-homepage-request.hold-registered.v1','catalog.teacher-homepage-request.hold-released.v1')
 AND audit_actor='issuer:governance' AND audit_target='teacher-homepage-request/held'`).Scan(&governanceAudits); err != nil || governanceAudits != 2 {
		t.Fatalf("hold registration and release audit: %d %v", governanceAudits, err)
	}

	if _, err := migration.Exec(`INSERT INTO catalog.teacher_homepage_requests
 (id,school_id,teacher_id,submitter,status,revision,created_at,resolved_at,data)
 SELECT 'expired-'||n,'school','teacher','issuer:teacher-owner','rejected',2,
 $1::timestamptz-interval '1 hour',$1,'{}' FROM generate_series(1,1001) n`, expiredAt); err != nil {
		t.Fatal(err)
	}
	for _, want := range []int64{1000, 1, 0} {
		if n, err := maint.PruneTeacherHomepageRequests(ctx); n != want || err != nil {
			t.Fatalf("cleanup batch size: got %d, want %d: %v", n, want, err)
		}
	}
	if _, err := migration.Exec(`REVOKE UPDATE(data) ON catalog.teacher_homepage_requests FROM ` + role); err != nil {
		t.Fatal(err)
	}
	if err := repo.CheckRuntime(ctx); !errors.Is(err, c.ErrUnavailable) {
		t.Fatalf("startup must reject a missing request writer column: %v", err)
	}
}

func homepageRetentionReplayKey(id string) string {
	return fmt.Sprintf("teacher-homepage-request-submit:%x", sha256.Sum256([]byte(id)))
}
