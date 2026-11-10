package catalog_test

import (
	"context"
	"errors"
	c "github.com/StuHelper/stuhelper-catalog"
	"strings"
	"testing"
)

func TestOwnedScheduleReadsOnlyCurrentEnrollmentMeetings(t *testing.T) {
	s, admin, in := fixture(t)
	ctx := context.Background()
	in.Enrollments[0].TeacherIDs = nil
	in.Objects = append(in.Objects, c.Object{ID: "meeting-one", Kind: "schedule", SchoolID: "school", ClassID: "class1", Name: "Meeting", TimeText: "Monday 1-2", Location: "Room A"}, c.Object{ID: "meeting-other", Kind: "schedule", SchoolID: "school", ClassID: "class2", Name: "Other meeting", TimeText: "Private time", Location: "Private room"})
	if _, e := s.Import(ctx, admin, "seed-schedule", in); e != nil {
		t.Fatal(e)
	}
	s.ReadAccess = &readAccess{revoked: true, unavailable: true}
	if _, e := s.MySchedule(ctx, c.Principal{}, "", "", 20); !errors.Is(e, c.ErrUnauthenticated) {
		t.Fatal(e)
	}
	data, e := s.MySchedule(ctx, c.Principal{Subject: "issuer:student"}, "term1", "", 1)
	if e != nil || len(data.Items) != 1 || len(data.Items[0].Meetings) != 1 || data.Items[0].Meetings[0].Location != "Room A" || data.Next != "enrollment" {
		t.Fatal(data, e)
	}
	next, e := s.MySchedule(ctx, c.Principal{Subject: "issuer:student"}, "term1", data.Next, 1)
	if e != nil || len(next.Items) != 0 {
		t.Fatal(next, e)
	}
	for _, p := range []c.Principal{{Subject: "issuer:other"}, {Subject: "attacker"}} {
		v, e := s.MySchedule(ctx, p, "", "", 100)
		if e != nil || len(v.Items) != 0 {
			t.Fatal(v, e)
		}
	}
	wrong, e := s.MySchedule(ctx, c.Principal{Subject: "issuer:student"}, "term2", "", 100)
	if e != nil || len(wrong.Items) != 0 {
		t.Fatal(wrong, e)
	}
	if _, e = s.MySchedule(ctx, c.Principal{Subject: "issuer:student"}, strings.Repeat("x", 500), "", 100); !errors.Is(e, c.ErrInput) {
		t.Fatal(e)
	}
	in.Revision = 2
	in.Enrollments[0].Active = false
	if _, e = s.Import(ctx, admin, "inactive", in); e != nil {
		t.Fatal(e)
	}
	v, e := s.MySchedule(ctx, c.Principal{Subject: "issuer:student"}, "", "", 100)
	if e != nil || len(v.Items) != 0 {
		t.Fatal(v, e)
	}
}
