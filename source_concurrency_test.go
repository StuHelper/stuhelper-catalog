package catalog_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	c "github.com/StuHelper/stuhelper-catalog"
)

type pausedReadRepository struct {
	c.Repository
	object, enrollment string
	entered, release   chan struct{}
	once               sync.Once
}
type pausedReadTransaction struct {
	c.Transaction
	repo *pausedReadRepository
	ctx  context.Context
}

func (r *pausedReadRepository) Transaction(ctx context.Context, run func(c.Transaction) error) error {
	return r.Repository.Transaction(ctx, func(tx c.Transaction) error { return run(&pausedReadTransaction{Transaction: tx, repo: r, ctx: ctx}) })
}
func (t *pausedReadTransaction) pause() error {
	wait := false
	t.repo.once.Do(func() { close(t.repo.entered); wait = true })
	if !wait {
		return nil
	}
	select {
	case <-t.repo.release:
		return nil
	case <-t.ctx.Done():
		return t.ctx.Err()
	}
}
func (t *pausedReadTransaction) Object(id string) (c.Record, error) {
	r, err := t.Transaction.Object(id)
	if err == nil && id == t.repo.object {
		err = t.pause()
	}
	return r, err
}
func (t *pausedReadTransaction) Enrollment(id string) (c.Enrollment, error) {
	r, err := t.Transaction.Enrollment(id)
	if err == nil && id == t.repo.enrollment {
		err = t.pause()
	}
	return r, err
}

func TestCorrectionRereadsSourceWatermarkAfterSchoolLock(t *testing.T) {
	s, p, in := fixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if _, err := s.Import(ctx, p, "seed", in); err != nil {
		t.Fatal(err)
	}
	paused := &pausedReadRepository{Repository: s.Repo, object: "course", entered: make(chan struct{}), release: make(chan struct{})}
	correcting := &c.Service{Repo: paused}
	done := make(chan error, 1)
	name := "Maintained course name"
	go func() {
		_, err := correcting.Correct(ctx, p, "course", "correction", c.Correction{Revision: 1, Name: &name})
		done <- err
	}()
	select {
	case <-paused.entered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	in.Revision = 3
	if _, err := s.Import(ctx, p, "new-source", in); err != nil {
		close(paused.release)
		t.Fatal(err)
	}
	close(paused.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	in.Revision = 2
	for _, object := range in.Objects {
		if object.ID == "course" {
			in.Objects = []c.Object{object}
			break
		}
	}
	in.Enrollments = nil
	if _, err := s.Import(ctx, p, "older-source", in); !errors.Is(err, c.ErrConflict) {
		t.Fatal("waiting correction rolled back the source watermark", err)
	}
}

func TestSourceSnapshotCoordinatesWithOrdinaryImport(t *testing.T) {
	s, p, in := fixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	in.Revision = 9
	if _, err := s.Import(ctx, p, "seed", in); err != nil {
		t.Fatal(err)
	}
	paused := &pausedReadRepository{Repository: s.Repo, enrollment: "enrollment", entered: make(chan struct{}), release: make(chan struct{})}
	refreshing := &c.Service{Repo: paused}
	snapshotDone := make(chan error, 1)
	snapshot := c.EnrollmentSnapshot{Subject: in.Enrollments[0].Subject, SchoolID: in.SchoolID, SourceID: in.SourceID, Version: "snapshot-10", Revision: 10, FetchedAt: in.FetchedAt, Enrollments: in.Enrollments}
	go func() { _, err := refreshing.ReplaceOwnEnrollments(ctx, p, snapshot); snapshotDone <- err }()
	select {
	case <-paused.entered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	in.Revision = 11
	in.Enrollments = append([]c.Enrollment(nil), in.Enrollments...)
	in.Enrollments[0].Active = false
	importDone := make(chan error, 1)
	go func() { _, err := s.Import(ctx, p, "newer-revocation", in); importDone <- err }()
	var imported bool
	select {
	case err := <-importDone:
		imported = true
		if err != nil {
			close(paused.release)
			t.Fatal(err)
		}
	case <-time.After(300 * time.Millisecond):
	}
	close(paused.release)
	if err := <-snapshotDone; err != nil {
		t.Fatal(err)
	}
	if !imported {
		if err := <-importDone; err != nil {
			t.Fatal(err)
		}
	}
	rows, err := s.MyEnrollments(ctx, c.Principal{Subject: snapshot.Subject}, in.SchoolID, "", "", "", 100)
	if err != nil || len(rows) != 0 {
		t.Fatal("older snapshot overwrote a newer enrollment revocation", err)
	}
}

func TestSourceSnapshotLimitDoesNotCountInactiveHistory(t *testing.T) {
	s, p, in := fixture(t)
	ctx := context.Background()
	if _, err := s.Import(ctx, p, "seed", in); err != nil {
		t.Fatal(err)
	}
	snapshot := c.EnrollmentSnapshot{Subject: "issuer:history-student", SchoolID: "school", SourceID: "replica-history", Revision: 1, FetchedAt: in.FetchedAt}
	for generation := 1; generation <= 2; generation++ {
		snapshot.Revision = int64(generation)
		snapshot.Version = fmt.Sprintf("batch-%d", generation)
		snapshot.Enrollments = nil
		for i := 0; i < 2000; i++ {
			snapshot.Enrollments = append(snapshot.Enrollments, c.Enrollment{ID: fmt.Sprintf("history-%d-%04d", generation, i), Subject: snapshot.Subject, SchoolID: "school", CourseID: "course", TermID: "term1", Active: true})
		}
		if _, err := s.ReplaceOwnEnrollments(ctx, p, snapshot); err != nil {
			t.Fatal(err)
		}
	}
	snapshot.Revision = 3
	snapshot.Version = "revoked"
	snapshot.Enrollments = nil
	if _, err := s.ReplaceOwnEnrollments(ctx, p, snapshot); err != nil {
		t.Fatal("inactive history prevented revocation", err)
	}
	rows, err := s.MyEnrollments(ctx, c.Principal{Subject: snapshot.Subject}, "school", "", "", "", 100)
	if err != nil || len(rows) != 0 {
		t.Fatal("active snapshot not cleared", err)
	}
	if _, err := s.MyReferences(ctx, c.Principal{Subject: snapshot.Subject}, []string{"course"}); err != nil {
		t.Fatal("historical ownership was removed", err)
	}
}
