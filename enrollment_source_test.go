package catalog_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	c "github.com/StuHelper/stuhelper-catalog"
)

type sourceStub struct {
	snapshot c.EnrollmentSnapshot
	err      error
}

func (s sourceStub) OwnSnapshot(_ context.Context, subject string) (c.EnrollmentSnapshot, error) {
	if s.err != nil {
		return c.EnrollmentSnapshot{}, s.err
	}
	out := s.snapshot
	out.Subject = subject
	out.Enrollments = append([]c.Enrollment(nil), out.Enrollments...)
	for i := range out.Enrollments {
		out.Enrollments[i].Subject = subject
	}
	return out, nil
}

func TestOwnSourceRefreshIsAtomicScopedAndDurablyIdempotent(t *testing.T) {
	s, p, in := fixture(t)
	ctx := context.Background()
	if _, err := s.Import(ctx, p, "seed", in); err != nil {
		t.Fatal(err)
	}
	owner := c.Principal{Subject: "issuer:source-student"}
	en := c.Enrollment{ID: "source-enrollment", Subject: owner.Subject, SchoolID: "school", CourseID: "course", TermID: "term1", ClassID: "class1", TeacherIDs: []string{"teacher"}, Active: true}
	snapshot := c.EnrollmentSnapshot{Subject: owner.Subject, SchoolID: "school", SourceID: "replica", Version: "batch1", Revision: 10, FetchedAt: in.FetchedAt, Enrollments: []c.Enrollment{en}}
	if _, err := s.ReplaceOwnEnrollments(ctx, owner, snapshot); !errors.Is(err, c.ErrForbidden) {
		t.Fatal("ordinary caller imported evidence", err)
	}
	var wg sync.WaitGroup
	ids := make(chan string, 8)
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out, err := s.ReplaceOwnEnrollments(ctx, p, snapshot)
			ids <- out.ID
			errs <- err
		}()
	}
	wg.Wait()
	close(ids)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	first := ""
	for id := range ids {
		if first == "" {
			first = id
		}
		if first != id {
			t.Fatal("concurrent snapshot produced duplicate audit")
		}
	}
	s.Clock = func() time.Time { return in.FetchedAt.Add(48 * time.Hour) }
	out, err := s.ReplaceOwnEnrollments(ctx, p, snapshot)
	if err != nil || out.ID != first {
		t.Fatal("24-hour replay expiry duplicated snapshot", err)
	}
	changed := snapshot
	changed.Enrollments = nil
	if _, err := s.ReplaceOwnEnrollments(ctx, p, changed); !errors.Is(err, c.ErrConflict) {
		t.Fatal("same version accepted different content", err)
	}
	changed.Version = "batch2"
	changed.Revision = 11
	changed.Enrollments = []c.Enrollment{en, {ID: "bad", Subject: owner.Subject, SchoolID: "school", CourseID: "missing", TermID: "term1", Active: true}}
	if _, err := s.ReplaceOwnEnrollments(ctx, p, changed); err == nil {
		t.Fatal("invalid reference accepted")
	}
	rows, err := s.MyEnrollments(ctx, owner, "school", "", "", "", 100)
	if err != nil || len(rows) != 1 {
		t.Fatal("failed refresh lost existing evidence", err)
	}
	changed.Enrollments = nil
	if _, err := s.ReplaceOwnEnrollments(ctx, p, changed); err != nil {
		t.Fatal(err)
	}
	rows, err = s.MyEnrollments(ctx, owner, "school", "", "", "", 100)
	if err != nil || len(rows) != 0 {
		t.Fatal("removed source enrollment stayed active", err)
	}
	other, err := s.MyEnrollments(ctx, c.Principal{Subject: "issuer:student"}, "school", "", "", "", 100)
	if err != nil || len(other) != 1 {
		t.Fatal("refresh changed another owner/source", err)
	}
	changed.Version = "batch2-new-binding"
	changed.Enrollments = []c.Enrollment{en}
	if _, err := s.ReplaceOwnEnrollments(ctx, p, changed); err != nil {
		t.Fatal("same source revision could not reactivate current binding", err)
	}
	if _, err := s.ReplaceOwnEnrollments(ctx, p, snapshot); !errors.Is(err, c.ErrConflict) {
		t.Fatal("older batch rolled back current evidence", err)
	}
}

func TestOwnReadsRefreshSourceAndFailClosed(t *testing.T) {
	s, p, in := fixture(t)
	ctx := context.Background()
	if _, err := s.Import(ctx, p, "seed", in); err != nil {
		t.Fatal(err)
	}
	owner := c.Principal{Subject: "issuer:student"}
	snapshot := c.EnrollmentSnapshot{SchoolID: "school", SourceID: "replica", Version: "batch1", Revision: 10, FetchedAt: in.FetchedAt, Enrollments: []c.Enrollment{{ID: "source-enrollment", SchoolID: "school", CourseID: "course", TermID: "term1", ClassID: "class1", TeacherIDs: []string{"teacher"}, Active: true}}}
	s.EnrollmentSource = sourceStub{snapshot: snapshot}
	rows, err := s.MyEnrollments(ctx, owner, "school", "course", "term1", "", 100)
	if err != nil || len(rows) != 2 {
		t.Fatal("own read did not refresh source", err)
	}
	s.EnrollmentSource = sourceStub{err: errors.New("offline")}
	if _, err := s.MyEnrollments(ctx, owner, "school", "course", "term1", "", 100); !errors.Is(err, c.ErrUnavailable) {
		t.Fatal("source failure returned stale evidence", err)
	}
	if _, err := s.MySchedule(ctx, owner, "term1", "", 100); !errors.Is(err, c.ErrUnavailable) {
		t.Fatal("schedule bypassed failed source", err)
	}
	if _, err := s.MyReferences(ctx, owner, []string{"course"}); !errors.Is(err, c.ErrUnavailable) {
		t.Fatal("references bypassed failed source", err)
	}
	s.EnrollmentSource = nil
	rows, err = s.MyEnrollments(ctx, owner, "school", "course", "term1", "", 100)
	if err != nil || len(rows) != 2 {
		t.Fatal("failure erased persisted evidence", err)
	}
}

func TestRepeatedSourceDirectoryKeepsPublicRevisionsAndRejectsOlderData(t *testing.T) {
	s, p, in := fixture(t)
	ctx := context.Background()
	first, err := s.Import(ctx, p, "first", in)
	if err != nil {
		t.Fatal(err)
	}
	s.Clock = func() time.Time { return in.FetchedAt.Add(48 * time.Hour) }
	repeated, err := s.Import(ctx, p, "first", in)
	if err != nil || repeated.ID != first.ID {
		t.Fatal("retained import replay expired", err)
	}
	before, err := s.Get(ctx, "course")
	if err != nil {
		t.Fatal(err)
	}
	in.Revision = 2
	if _, err := s.Import(ctx, p, "next-batch", in); err != nil {
		t.Fatal(err)
	}
	after, err := s.Get(ctx, "course")
	if err != nil || before.Revision != after.Revision || !before.UpdatedAt.Equal(after.UpdatedAt) {
		t.Fatal("identical source bumped public revision", err)
	}
	in.Revision = 1
	in.Objects[0].Name = "Older changed data"
	if _, err := s.Import(ctx, p, "older-batch", in); !errors.Is(err, c.ErrConflict) {
		t.Fatal("unchanged refresh lost source ordering", err)
	}
}
