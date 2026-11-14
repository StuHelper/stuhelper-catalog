package catalog_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	c "github.com/StuHelper/stuhelper-catalog"
	"github.com/StuHelper/stuhelper-catalog/postgres"
)

func TestOwnScheduleDoesNotScanUnrelatedMeetings(t *testing.T) {
	s, admin, in := fixture(t)
	owner := c.Principal{Subject: "issuer:performance-student"}
	for i := 0; i < 18; i++ {
		class := fmt.Sprintf("owned-class-%02d", i)
		in.Objects = append(in.Objects, c.Object{ID: class, Kind: "class", SchoolID: "school", Name: class, TermCourseID: "offering1"},
			c.Object{ID: fmt.Sprintf("owned-meeting-%02d", i), Kind: "schedule", SchoolID: "school", Name: "Own meeting", ClassID: class, Location: "Own room"})
		in.Enrollments = append(in.Enrollments, c.Enrollment{ID: fmt.Sprintf("owned-enrollment-%02d", i), Subject: owner.Subject, SchoolID: "school", CourseID: "course", TermID: "term1", ClassID: class, Active: true})
	}
	if _, err := s.Import(context.Background(), admin, "performance-seed", in); err != nil {
		t.Fatal(err)
	}
	// The fixture's concrete repository supplies an isolated runtime connection.
	// Bulk synthetic background rows reproduce the production relation cardinality.
	db := s.Repo.(*postgres.Repository).DB
	if _, err := db.Exec(`INSERT INTO catalog.objects(id,school_id,kind,name,department_id,category_id,nature_id,course_id,term_id,term_course_id,class_id,teacher_id,data)
 SELECT 'unrelated-meeting-'||n,'school','schedule','Unrelated','','','','','','','class2','',
 jsonb_build_object('object',jsonb_build_object('id','unrelated-meeting-'||n,'school_id','school','kind','schedule','class_id','class2','name','Unrelated','location','Other room'))
 FROM generate_series(1,100000) n; ANALYZE catalog.objects; ANALYZE catalog.enrollments`); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	start := time.Now()
	page, err := s.MySchedule(ctx, owner, "term1", "", 100)
	if err != nil {
		t.Fatalf("bounded own schedule exceeded query deadline: %v", err)
	}
	if len(page.Items) != 18 {
		t.Fatalf("got %d own enrollments", len(page.Items))
	}
	for _, item := range page.Items {
		if item.Enrollment.Subject != owner.Subject || len(item.Meetings) != 1 || item.Meetings[0].Location != "Own room" {
			t.Fatal("unrelated schedule leaked")
		}
	}
	t.Logf("18 owned classes against 100000 unrelated meetings: %s", time.Since(start))
	other, err := s.MySchedule(context.Background(), c.Principal{Subject: "issuer:unrelated"}, "term1", "", 100)
	if err != nil || len(other.Items) != 0 {
		t.Fatal("another account inherited the schedule", err)
	}
}

func TestUnchangedSnapshotDoesNotWaitForDirectoryWriter(t *testing.T) {
	s, admin, in := fixture(t)
	ctx := context.Background()
	if _, err := s.Import(ctx, admin, "snapshot-seed", in); err != nil {
		t.Fatal(err)
	}
	snapshot := c.EnrollmentSnapshot{Subject: in.Enrollments[0].Subject, SchoolID: in.SchoolID, SourceID: in.SourceID, Version: "stable-batch", Revision: 1, FetchedAt: in.FetchedAt, Enrollments: in.Enrollments}
	first, err := s.ReplaceOwnEnrollments(ctx, admin, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		done <- s.Repo.Transaction(ctx, func(tx c.Transaction) error {
			if err := tx.Lock(in.SchoolID); err != nil {
				return err
			}
			close(entered)
			<-release
			return nil
		})
	}()
	<-entered
	defer func() {
		close(release)
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	bounded, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	repeated, err := s.ReplaceOwnEnrollments(bounded, admin, snapshot)
	if err != nil || repeated.ID != first.ID {
		t.Fatal("unchanged owner snapshot waited for a directory writer", err)
	}
}

func TestOwnSchedulePreservesClassAliasesAndCurrentVisibility(t *testing.T) {
	s, admin, in := fixture(t)
	ctx := context.Background()
	in.Objects = append(in.Objects,
		c.Object{ID: "before-merge", Kind: "schedule", SchoolID: "school", Name: "Before", ClassID: "class1", Location: "Room A"},
		c.Object{ID: "after-merge", Kind: "schedule", SchoolID: "school", Name: "After", ClassID: "class2", Location: "Room B"},
		c.Object{ID: "hidden-meeting", Kind: "schedule", SchoolID: "school", Name: "Hidden", ClassID: "class2", Location: "Hidden room", Inactive: true})
	if _, err := s.Import(ctx, admin, "merge-schedule-seed", in); err != nil {
		t.Fatal(err)
	}
	// Class aliases are supported by the repository read contract even though
	// the public merge command currently accepts only courses and teachers.
	db := s.Repo.(*postgres.Repository).DB
	if _, err := db.Exec("INSERT INTO catalog.redirects(alias_id,canonical_id,reason,created_at) VALUES('class1','class2','synthetic relation alias',now())"); err != nil {
		t.Fatal(err)
	}
	page, err := s.MySchedule(ctx, c.Principal{Subject: "issuer:student"}, "term1", "", 100)
	if err != nil || len(page.Items) != 1 || len(page.Items[0].Meetings) != 2 {
		t.Fatal("merged class links changed", err)
	}
	for _, meeting := range page.Items[0].Meetings {
		if meeting.Location != "Room A" && meeting.Location != "Room B" {
			t.Fatal("inactive meeting leaked")
		}
	}
	other, err := s.MySchedule(ctx, c.Principal{Subject: "issuer:other"}, "term1", "", 100)
	if err != nil || len(other.Items) != 0 {
		t.Fatal("class merge crossed owner boundary", err)
	}
}
