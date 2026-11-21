package catalog_test

import (
	"context"
	"errors"
	"testing"
	"time"

	c "github.com/StuHelper/stuhelper-catalog"
)

func TestTeacherHomepageControlsPostgresPersistence(t *testing.T) {
	s, p, _, _ := projectionFixture(t)
	ctx := context.Background()
	read := func(schoolID, teacherID string) (c.TeacherHomepageControls, error) {
		var out c.TeacherHomepageControls
		err := s.Repo.Transaction(ctx, func(tx c.Transaction) error {
			var err error
			out, err = tx.(c.TeacherHomepageControlsTransaction).TeacherHomepageControls(schoolID, teacherID)
			return err
		})
		return out, err
	}
	write := func(schoolID, teacherID string, revision int64, visibility string) error {
		return s.Repo.Transaction(ctx, func(tx c.Transaction) error {
			return tx.(c.TeacherHomepageControlsTransaction).SaveTeacherHomepageControls(schoolID, teacherID, c.TeacherHomepageControls{
				Revision: revision, PageVisibility: visibility,
			}, time.Now())
		})
	}
	initial, err := read("school", "official")
	if err != nil || initial.Revision != 0 || initial.PageVisibility != "visible" || initial.Rules == nil {
		t.Fatalf("unexpected default publication controls: %+v, %v", initial, err)
	}
	for _, target := range [][2]string{{"other-school", "official"}, {"school", "dept"}} {
		if _, err := read(target[0], target[1]); !errors.Is(err, c.ErrNotFound) {
			t.Fatalf("read out-of-scope teacher controls: %v", err)
		}
		if err := write(target[0], target[1], 1, "hidden"); !errors.Is(err, c.ErrConflict) {
			t.Fatalf("write out-of-scope teacher controls: %v", err)
		}
	}
	if err := write("school", "official", 2, "hidden"); !errors.Is(err, c.ErrConflict) {
		t.Fatalf("allowed a skipped publication revision: %v", err)
	}
	if err := write("school", "official", 1, "hidden"); err != nil {
		t.Fatal(err)
	}
	if err := write("school", "official", 1, "visible"); !errors.Is(err, c.ErrConflict) {
		t.Fatalf("overwrote a newer publication revision: %v", err)
	}

	p.CanRefreshTeacherSites = true
	publishTeacherHomepagePostgresSnapshot(t, s, p, "homepage-publication-refresh", teacherHomepagePostgresSnapshot(t, s, p))
	after, err := read("school", "official")
	if err != nil || after.Revision != 1 || after.PageVisibility != "hidden" || after.Rules == nil {
		t.Fatalf("website refresh changed publication controls: %+v, %v", after, err)
	}
	if err := write("school", "official", 2, "visible"); err != nil {
		t.Fatal("restore publication controls", err)
	}
	restored, err := read("school", "official")
	if err != nil || restored.Revision != 2 || restored.PageVisibility != "visible" {
		t.Fatalf("restored publication controls: %+v, %v", restored, err)
	}
}
