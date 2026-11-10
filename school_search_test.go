package catalog_test

import (
	"context"
	"encoding/json"
	c "github.com/StuHelper/stuhelper-catalog"
	"github.com/StuHelper/stuhelper-catalog/httpapi"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

func TestSchoolSearchCodesNamesAliasesAndDiscovery(t *testing.T) {
	repo, _ := database(t)
	s := &c.Service{Repo: repo}
	ctx := context.Background()
	p := c.Principal{Subject: "importer", CanImport: true, CanMaintain: true, Schools: []string{"4111010006", "4111010007", "alias-school"}}
	schools := []c.Object{{ID: "4111010006", SchoolID: "4111010006", Kind: "school", Code: "10006", Name: "北京航空航天大学", Aliases: []string{"北航", "Beihang", "literal%_"}}, {ID: "4111010007", SchoolID: "4111010007", Kind: "school", Name: "北京理工大学"}, {ID: "alias-school", SchoolID: "alias-school", Kind: "school", Code: "4123010213", Name: "哈尔滨工业大学"}}
	for _, school := range schools {
		_, e := s.Import(ctx, p, "import-"+school.ID, c.Import{SchoolID: school.ID, SourceID: "synthetic", Revision: 1, FetchedAt: time.Now(), Objects: []c.Object{school}})
		if e != nil {
			t.Fatal(e)
		}
	}
	for _, tc := range []struct{ q, want string }{{"4111010006", "4111010006"}, {"10006", "4111010006"}, {" 北京航空航天大学 ", "4111010006"}, {"北航", "4111010006"}, {"beIHAng", "4111010006"}, {"literal%_", "4111010006"}, {"99999", ""}, {"1000", ""}, {"4123010213", "alias-school"}, {"10213", "alias-school"}} {
		page, e := s.List(ctx, c.Filter{Kind: "school", Query: tc.q, Limit: 100})
		if e != nil {
			t.Fatal(e)
		}
		if tc.want == "" {
			if len(page.Items) != 0 {
				t.Fatalf("%q unexpectedly matched", tc.q)
			}
		} else if len(page.Items) != 1 || page.Items[0].ID != tc.want {
			t.Fatalf("%q: %+v", tc.q, page)
		}
	}
	page, e := s.List(ctx, c.Filter{Kind: "school", Query: "大学", IDs: []string{"4111010006"}, Limit: 1})
	if e != nil || len(page.Items) != 1 {
		t.Fatal(page, e)
	}
	// The same matching applies to the authorized admin HTTP list.
	handler := httpapi.Handler{Service: s, Authenticate: func(*http.Request) (c.Principal, error) { return p, nil }}.Routes()
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest("GET", "/api/v1/catalog/admin/objects?kind=school&q="+url.QueryEscape("10006")+"&school_id=4111010006", nil))
	if response.Code != 200 {
		t.Fatal(response.Code, response.Body.String())
	}
	var result c.Page
	if e = json.Unmarshal(response.Body.Bytes(), &result); e != nil || len(result.Items) != 1 {
		t.Fatal(e)
	}
	snapshot, e := repo.DiscoverySnapshot(ctx, "", 100)
	if e != nil {
		t.Fatal(e)
	}
	found := false
	for _, d := range snapshot.Items {
		if d.Target.ID == "4111010006" {
			found = d.Target.Kind == "school" && d.Code == "10006" && len(d.Aliases) == 3
		}
	}
	if !found {
		t.Fatal("school search metadata missing from snapshot")
	}
}
