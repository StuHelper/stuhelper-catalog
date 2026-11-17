package catalog_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	c "github.com/StuHelper/stuhelper-catalog"
	"github.com/StuHelper/stuhelper-catalog/httpapi"
	"github.com/StuHelper/stuhelper-catalog/postgres"
)

// Exercise both repository modes: a published directory must preserve the same
// disclosure boundary as the source-backed standalone module.
func TestTeachingDetailsHiddenAcrossPublicReads(t *testing.T) {
	for _, projected := range []bool{false, true} {
		t.Run(fmt.Sprintf("projection=%t", projected), func(t *testing.T) {
			s, admin, in := fixture(t)
			ctx := context.Background()
			in.Objects = append(in.Objects,
				c.Object{ID: "meeting", Kind: "schedule", SchoolID: "school", ClassID: "class1", Name: "Private meeting", TimeText: "Private Monday 1-2", Location: "Private room A"},
				c.Object{ID: "other-meeting", Kind: "schedule", SchoolID: "school", ClassID: "class2", Name: "Other meeting", TimeText: "Other time", Location: "Other room"})
			if _, err := s.Import(ctx, admin, "visibility-fixture", in); err != nil {
				t.Fatal(err)
			}
			s.DirectoryMetrics = uiMetrics{}
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
			h := httpapi.Handler{Service: s, Authenticate: func(req *http.Request) (c.Principal, error) {
				switch req.Header.Get("User") {
				case "admin":
					return admin, nil
				case "student", "teacher":
					return c.Principal{Subject: "issuer:" + req.Header.Get("User")}, nil
				default:
					return c.Principal{}, nil
				}
			}}.Routes()
			call := func(path, user string, status int) []byte {
				t.Helper()
				req := httptest.NewRequest(http.MethodGet, "/api/v1/catalog/"+path, nil)
				req.Header.Set("User", user)
				req.Header.Set("X-User-ID", "issuer:admin")
				w := httptest.NewRecorder()
				h.ServeHTTP(w, req)
				if w.Code != status {
					t.Fatalf("%s as %s: status %d want %d: %s", path, user, w.Code, status, w.Body.String())
				}
				if status != 200 {
					var failure map[string]string
					if err := json.Unmarshal(w.Body.Bytes(), &failure); err != nil || len(failure) != 1 || failure["code"] != map[int]string{401: "unauthenticated", 403: "forbidden", 404: "not_found"}[status] {
						t.Fatalf("denied response disclosed data: %s", w.Body.String())
					}
				}
				return w.Body.Bytes()
			}
			for kind, id := range map[string]string{"term_course": "offering1", "class": "class1", "assignment": "assignment", "schedule": "meeting"} {
				for _, user := range []string{"student", "teacher"} {
					for _, route := range []string{"objects", "directory", "filter-options"} {
						call(route+"?kind="+kind, user, 403)
					}
					for _, suffix := range []string{"", "/summary", "/resolution"} {
						call("objects/"+id+suffix, user, 404)
					}
					call("admin/objects/"+id, user, 403)
				}
				call("objects/"+id, "", 401)
				for _, route := range []string{"objects?school_id=school&kind=" + kind, "objects/" + id, "objects/" + id + "/resolution", "objects/lookup?ids=" + id} {
					call("admin/"+route, "admin", 200)
				}
				var lookup c.Lookup
				if err := json.Unmarshal(call("objects/lookup?ids=course,"+id+",unknown", "student", 200), &lookup); err != nil || len(lookup.Items) != 1 || lookup.Items[0].ID != "course" || !slices.Equal(lookup.MissingIDs, []string{id, "unknown"}) {
					t.Fatal("restricted lookup", lookup, err)
				}
				if object, err := s.Get(ctx, id); err != nil || object.Kind != kind {
					t.Fatal("trusted Service port lost teaching facts", object, err)
				}
			}
			for _, filter := range []string{"class_id=class1", "term_course_id=offering1"} {
				for _, route := range []string{"objects", "directory"} {
					call(route+"?kind=teacher&"+filter, "student", 403)
				}
			}
			for query, count := range map[string]int{"kind=teacher&course_id=course": 2, "kind=course&teacher_id=teacher": 1, "kind=term": 2} {
				var page c.Page
				if err := json.Unmarshal(call("objects?"+query, "student", 200), &page); err != nil || len(page.Items) != count {
					t.Fatal("public relationship", query, page, err)
				}
			}
			for _, id := range []string{"course", "teacher"} {
				var summary map[string]json.RawMessage
				if err := json.Unmarshal(call("objects/"+id+"/summary", "student", 200), &summary); err != nil {
					t.Fatal(err)
				}
				if _, exists := summary["offering_count"]; exists || summary["course_count"] == nil || summary["teacher_count"] == nil {
					t.Fatal("public summary exposes teaching count or loses relationship counts", summary)
				}
				var full c.Summary
				if err := json.Unmarshal(call("admin/objects/"+id+"/summary", "admin", 200), &full); err != nil || full.OfferingCount < 1 {
					t.Fatal("admin summary lost teaching counts", full, err)
				}
			}
			var directory c.DirectoryPage
			if err := json.Unmarshal(call("directory?kind=course&sort=name", "student", 200), &directory); err != nil || directory.Total != 1 || len(directory.Items) != 1 || len(directory.CategoryFacets) != 1 || directory.CategoryFacets[0].Count != 1 {
				t.Fatal("directory counts", directory, err)
			}
			var options c.FilterOptions
			if err := json.Unmarshal(call("filter-options?kind=course", "student", 200), &options); err != nil || options.Total != 1 || len(options.Categories) != 1 || options.Categories[0].Count != 1 {
				t.Fatal("filter counts", options, err)
			}
			var own c.OwnSchedulePage
			if err := json.Unmarshal(call("me/schedule?term_id=term1", "student", 200), &own); err != nil || len(own.Items) != 1 || len(own.Items[0].Meetings) != 1 || own.Items[0].Meetings[0].Location != "Private room A" {
				t.Fatal("own schedule lost authorized meeting", own, err)
			}
			for _, route := range []string{"me/schedule?term_id=term1", "me/enrollments"} {
				if body := call(route, "teacher", 200); strings.Contains(string(body), "class1") || strings.Contains(string(body), "Private room") {
					t.Fatalf("another user's schedule leaked: %s", body)
				}
				call(route, "", 401)
			}
			var enrollments []c.Enrollment
			if err := json.Unmarshal(call("me/enrollments", "student", 200), &enrollments); err != nil || len(enrollments) != 1 || enrollments[0].ClassID != "class1" {
				t.Fatal("own enrollment lost", enrollments, err)
			}
			call("me/references?ids=class1", "student", 200)
			call("me/references?ids=class2", "student", 404)
			assertTeachingDiscoveryAbsent(t, s)
		})
	}
}

func assertTeachingDiscoveryAbsent(t *testing.T, service *c.Service) {
	t.Helper()
	ctx := context.Background()
	check := func(docs []c.DiscoveryDocument) {
		t.Helper()
		for _, doc := range docs {
			if !slices.Contains([]string{"school", "course", "teacher"}, doc.Target.Kind) || strings.Contains(doc.Title, "Private") || strings.Contains(doc.Title, "Other room") {
				t.Fatalf("teaching details entered search: %+v", doc)
			}
		}
	}
	docs, err := service.DiscoveryCurrent(ctx, c.Principal{Subject: "issuer:student"}, []string{"course", "teacher", "offering1", "class1", "assignment", "meeting"})
	if err != nil || len(docs) != 2 {
		t.Fatal("current discovery", docs, err)
	}
	check(docs)
	reader := service.Repo.(*postgres.Repository)
	for cursor := ""; ; {
		page, err := reader.DiscoverySnapshot(ctx, cursor, 2)
		if err != nil {
			t.Fatal(err)
		}
		check(page.Items)
		if page.Next == "" {
			break
		}
		cursor = page.Next
	}
	events, err := reader.PendingOutbox(ctx, 100, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if event.Type == "catalog.discovery.v1" && (event.Data.Target == nil || !slices.Contains([]string{"school", "course", "teacher"}, event.Data.Target.Kind)) {
			t.Fatal("teaching details emitted as search event", event.ID)
		}
	}
}
