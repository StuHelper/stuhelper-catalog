package catalog_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	c "github.com/StuHelper/stuhelper-catalog"
	"github.com/StuHelper/stuhelper-catalog/httpapi"
	"github.com/StuHelper/stuhelper-catalog/postgres"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"
)

type readAccess struct{ revoked, unavailable bool }

func (a *readAccess) Scope(_ context.Context, p c.Principal, school string) (string, error) {
	if a.unavailable {
		return "", c.ErrUnavailable
	}
	if a.revoked || school != "" && school != "school" || p.Subject != "issuer:student" && p.Subject != "issuer:teacher" {
		return "", c.ErrForbidden
	}
	return "school", nil
}
func TestContentReadAdmissionCoversEveryHTTPReadAndPreservesOwnEnrollment(t *testing.T) {
	s, admin, in := fixture(t)
	if _, e := s.Import(context.Background(), admin, "seed", in); e != nil {
		t.Fatal(e)
	}
	access := &readAccess{}
	s.ReadAccess = access
	h := httpapi.Handler{Service: s, Authenticate: func(req *http.Request) (c.Principal, error) {
		switch req.Header.Get("Authorization") {
		case "Bearer student":
			return c.Principal{Subject: "issuer:student"}, nil
		case "Bearer teacher":
			return c.Principal{Subject: "issuer:teacher"}, nil
		case "Bearer admin":
			return admin, nil
		case "Bearer unverified":
			return c.Principal{Subject: "issuer:unverified"}, nil
		default:
			return c.Principal{}, nil
		}
	}}.Routes()
	call := func(path, token string, want int) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest("GET", path, nil)
		r.Header.Set("Authorization", token)
		r.Header.Set("X-User-ID", "issuer:admin")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s status=%d want=%d body=%s", path, w.Code, want, w.Body.String())
		}
		if want != 200 && (strings.Contains(w.Body.String(), "items") || strings.Contains(w.Body.String(), "teacher_count") || strings.Contains(w.Body.String(), "credits")) {
			t.Fatal("denied request returned content")
		}
		return w
	}
	paths := []string{"/api/v1/catalog/objects?kind=course", "/api/v1/catalog/objects?kind=course&q=no-match", "/api/v1/catalog/objects/course", "/api/v1/catalog/objects/lookup?ids=course,teacher", "/api/v1/catalog/objects/course/summary", "/api/v1/catalog/objects/course/resolution"}
	for _, path := range paths {
		call(path, "", 401)
		call(path, "Bearer unverified", 403)
		call(path, "Bearer teacher", 200)
		call(path, "Bearer student", 200)
	}
	call("/api/v1/catalog/objects?kind=course&school_id=other", "Bearer student", 403)
	access.revoked = true
	for _, path := range paths {
		call(path, "Bearer student", 403)
	}
	own := call("/api/v1/catalog/me/enrollments?school_id=school&subject=issuer:other", "Bearer student", 200)
	if !strings.Contains(own.Body.String(), "enrollment") {
		t.Fatal("own enrollment blocked after admission expired")
	}
	call("/api/v1/catalog/admin/objects?kind=course&school_id=school", "Bearer admin", 200)
	call("/api/v1/catalog/admin/objects/course", "Bearer admin", 200)
	call("/api/v1/catalog/admin/objects/course/summary", "Bearer admin", 200)
	call("/api/v1/catalog/admin/objects/lookup?ids=course", "Bearer admin", 200)
	call("/api/v1/catalog/admin/objects/course/resolution", "Bearer admin", 200)
	call("/api/v1/catalog/admin/objects/course", "Bearer student", 403)
	call("/api/v1/catalog/admin/objects?kind=course&school_id=other", "Bearer admin", 403)
	access.unavailable = true
	for _, path := range paths {
		call(path, "Bearer student", 503)
	}
	call("/api/v1/catalog/admin/objects/course", "Bearer admin", 200)
	s.ReadAccess = nil
	if _, e := s.Read(context.Background(), c.Principal{Subject: "issuer:student"}, "course"); !errors.Is(e, c.ErrUnavailable) {
		t.Fatal("missing admission provider allowed read", e)
	}
}

func TestPublicReadsRejectIncludeInactiveAndHideDeactivatedObjects(t *testing.T) {
	for _, projected := range []bool{false, true} {
		t.Run(fmt.Sprintf("projection=%t", projected), func(t *testing.T) {
			s, admin, in := fixture(t)
			ctx := context.Background()
			admin.Schools = append(admin.Schools, "school-inactive")
			credits := 2.0
			in.Objects = append(in.Objects,
				c.Object{ID: "dept-inactive", Kind: "department", SchoolID: "school", Name: "Retired Department", Inactive: true},
				c.Object{ID: "course-aid", Kind: "course", SchoolID: "school", DepartmentID: "dept-inactive", Name: "Active Course Retired Dept", Credits: &credits},
				c.Object{ID: "offering-aid", Kind: "term_course", SchoolID: "school", CourseID: "course-aid", TermID: "term1", Name: "Active Course Retired Dept"},
				c.Object{ID: "course-hidden-old", Kind: "course", SchoolID: "school", DepartmentID: "dept", Name: "Hidden Alias Course", Credits: &credits},
				c.Object{ID: "course-hidden", Kind: "course", SchoolID: "school", DepartmentID: "dept", Name: "Hidden Course", Credits: &credits},
				c.Object{ID: "offering-hidden", Kind: "term_course", SchoolID: "school", CourseID: "course-hidden", TermID: "term1", Name: "Hidden Course"},
			)
			if _, err := s.Import(ctx, admin, "seed-active", in); err != nil {
				t.Fatal(err)
			}
			if _, err := s.Import(ctx, admin, "seed-inactive-school", c.Import{
				SchoolID: "school-inactive", SourceID: "source", Revision: 1, FetchedAt: time.Now().UTC(),
				Objects: []c.Object{{ID: "school-inactive", Kind: "school", SchoolID: "school-inactive", Name: "Retired School", Inactive: true}},
			}); err != nil {
				t.Fatal(err)
			}
			if projected {
				if err := s.SaveTermOrders(ctx, admin, "school", []c.TermOrder{{ID: "term1", Year: 2025, Season: "autumn"}, {ID: "term2", Year: 2026, Season: "spring"}}); err != nil {
					t.Fatal(err)
				}
				if _, err := s.RebuildDirectory(ctx, admin, "school", nil); err != nil {
					t.Fatal(err)
				}
				repo := s.Repo.(*postgres.Repository)
				repo.RequireDirectoryProjection, repo.RequireTeacherDirectory = true, true
			}
			mergeIn := c.MergeInput{TargetID: "course-hidden", SourceRevision: 1, TargetRevision: 1, Reason: "merge before hide"}
			preview, err := s.PreviewMerge(ctx, admin, "course-hidden-old", mergeIn)
			if err != nil {
				t.Fatal(err)
			}
			mergeIn.PreviewToken = preview.PreviewToken
			merged, err := s.Merge(ctx, admin, "course-hidden-old", "merge-hidden", mergeIn)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.Delete(ctx, admin, "course-hidden", "hide-course", c.ObjectState{Revision: merged.Revision, Reason: "remove inappropriate course"}); err != nil {
				t.Fatal(err)
			}
			if _, err := s.Delete(ctx, admin, "teacher2", "hide-teacher", c.ObjectState{Revision: 1, Reason: "remove private biography"}); err != nil {
				t.Fatal(err)
			}

			student := c.Principal{Subject: "issuer:student"}
			studentInactiveSchool := c.Principal{Subject: "issuer:student-inactive"}
			s.ReadAccess = &scopedReadAccess{schools: map[string]string{
				student.Subject:               "school",
				studentInactiveSchool.Subject: "school-inactive",
			}}
			h := httpapi.Handler{Service: s, Authenticate: func(req *http.Request) (c.Principal, error) {
				switch req.Header.Get("Authorization") {
				case "Bearer student":
					return student, nil
				case "Bearer student-inactive":
					return studentInactiveSchool, nil
				case "Bearer admin":
					return admin, nil
				default:
					return c.Principal{}, nil
				}
			}}.Routes()
			call := func(path, token string, want int) []byte {
				t.Helper()
				req := httptest.NewRequest(http.MethodGet, path, nil)
				req.Header.Set("Authorization", token)
				req.Header.Set("X-User-ID", "issuer:admin")
				w := httptest.NewRecorder()
				h.ServeHTTP(w, req)
				if w.Code != want {
					t.Fatalf("%s (%s): status=%d want=%d body=%s", path, token, w.Code, want, w.Body.String())
				}
				return w.Body.Bytes()
			}

			for _, query := range []string{
				"/api/v1/catalog/objects?kind=school&include_inactive=true",
				"/api/v1/catalog/objects?kind=course&include_inactive=true",
				"/api/v1/catalog/objects?kind=course&include_inactive=yes",
				"/api/v1/catalog/admin/objects?school_id=school&kind=course&include_inactive=yes",
			} {
				call(query, "Bearer student", 400)
			}
			call("/api/v1/catalog/objects?kind=school&include_inactive=true", "Bearer student-inactive", 400)

			for _, path := range []string{"/api/v1/catalog/objects?kind=school", "/api/v1/catalog/objects?kind=school&include_inactive=false"} {
				var schools c.Page
				if err := json.Unmarshal(call(path, "Bearer student-inactive", 200), &schools); err != nil || len(schools.Items) != 0 {
					t.Fatalf("inactive school leaked via %s: %+v err=%v", path, schools, err)
				}
			}
			if schools, err := s.ReadList(ctx, studentInactiveSchool, c.Filter{Kind: "school", IncludeInactive: true, Limit: 20}); err != nil || len(schools.Items) != 0 {
				t.Fatalf("ReadList leaked inactive school: %+v err=%v", schools, err)
			}
			if courses, err := s.ReadList(ctx, student, c.Filter{Kind: "course", IncludeInactive: true, Limit: 20}); err != nil {
				t.Fatal(err)
			} else {
				for _, item := range courses.Items {
					if item.ID == "course-hidden" || item.Inactive {
						t.Fatalf("ReadList leaked deactivated course: %+v", item)
					}
				}
			}

			for _, hiddenID := range []string{"course-hidden", "course-hidden-old", "teacher2", "dept-inactive"} {
				for _, suffix := range []string{"", "/summary", "/resolution"} {
					body := call("/api/v1/catalog/objects/"+hiddenID+suffix, "Bearer student", 404)
					if strings.Contains(string(body), "Biography") || strings.Contains(string(body), "Hidden") || strings.Contains(string(body), "Retired") {
						t.Fatalf("deactivated %s%s leaked payload: %s", hiddenID, suffix, string(body))
					}
				}
			}
			call("/api/v1/catalog/objects/school-inactive", "Bearer student-inactive", 404)

			var lookup c.Lookup
			if err := json.Unmarshal(call("/api/v1/catalog/objects/lookup?ids=course,course-hidden,course-hidden-old,teacher2", "Bearer student", 200), &lookup); err != nil {
				t.Fatal(err)
			}
			if len(lookup.Items) != 1 || lookup.Items[0].ID != "course" || !slices.Equal(lookup.MissingIDs, []string{"course-hidden", "course-hidden-old", "teacher2"}) {
				t.Fatalf("ReadLookup leaked deactivated objects: %+v", lookup)
			}

			var aidSummary c.PublicSummary
			if err := json.Unmarshal(call("/api/v1/catalog/objects/course-aid/summary", "Bearer student", 200), &aidSummary); err != nil {
				t.Fatal(err)
			}
			if aidSummary.Department != nil {
				t.Fatalf("ReadSummary leaked deactivated department: %+v", aidSummary.Department)
			}

			var adminCourses c.Page
			if err := json.Unmarshal(call("/api/v1/catalog/admin/objects?school_id=school&kind=course&include_inactive=true", "Bearer admin", 200), &adminCourses); err != nil {
				t.Fatal(err)
			}
			if !slices.ContainsFunc(adminCourses.Items, func(o c.Object) bool { return o.ID == "course-hidden" && o.Inactive }) {
				t.Fatalf("admin list with include_inactive=true missing deactivated course: %+v", adminCourses.Items)
			}
			call("/api/v1/catalog/admin/objects/course-hidden", "Bearer admin", 200)
			call("/api/v1/catalog/admin/objects/teacher2", "Bearer admin", 200)
			var adminSummary c.Summary
			if err := json.Unmarshal(call("/api/v1/catalog/admin/objects/course-aid/summary", "Bearer admin", 200), &adminSummary); err != nil {
				t.Fatal(err)
			}
			if !projected && (adminSummary.Department == nil || adminSummary.Department.ID != "dept-inactive" || !adminSummary.Department.Inactive) {
				t.Fatalf("admin summary lost inactive department in source mode: %+v", adminSummary.Department)
			}
		})
	}
}

type scopedReadAccess struct{ schools map[string]string }

func (a *scopedReadAccess) Scope(_ context.Context, p c.Principal, requested string) (string, error) {
	school, ok := a.schools[p.Subject]
	if !ok || requested != "" && requested != school {
		return "", c.ErrForbidden
	}
	return school, nil
}
