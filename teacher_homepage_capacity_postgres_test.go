package catalog_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	c "github.com/StuHelper/stuhelper-catalog"
)

func homepageTextRules(group string, count int) []c.TeacherHomepageFieldRule {
	rules := make([]c.TeacherHomepageFieldRule, 0, count)
	for i := range count {
		value := fmt.Sprintf("Synthetic description %d", i)
		rules = append(rules, c.TeacherHomepageFieldRule{
			Field:    c.TeacherHomepageFieldSelector{Field: group, Label: fmt.Sprintf("Item %d", i)},
			Override: &c.TeacherHomepageFieldValue{Text: &value},
		})
	}
	return rules
}

func homepageRulesJSON(t *testing.T, rules []c.TeacherHomepageFieldRule) string {
	t.Helper()
	raw, err := json.Marshal(rules)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestTeacherHomepageControlsPostgresRejectsOverrideCapacity(t *testing.T) {
	s, _, migration, _ := homepageFixture(t)
	h := homepageHandler(s)
	assertRejected := func(key string, rules []c.TeacherHomepageFieldRule) {
		t.Helper()
		body := fmt.Sprintf(`{"expected_revision":0,"page_visibility":"visible","rules":%s}`, homepageRulesJSON(t, rules))
		response := homepageCall(t, h, http.MethodPut, homepageControls, homepageAdmin, key, body, http.StatusConflict)
		if code := homepageDecode[map[string]string](t, response)["code"]; code != "teacher_homepage_capacity_exceeded" {
			t.Fatalf("expected the capacity code, got %q", code)
		}
	}
	assertRejected("too-many-sections", homepageTextRules("sections", 65))
	assertRejected("too-many-fields", homepageTextRules("fields", 129))
	sections := make([]c.TeacherHomepageSection, 0, 64)
	for i := range 64 {
		sections = append(sections, c.TeacherHomepageSection{
			Title: fmt.Sprintf("Item %d", i),
			Text:  fmt.Sprintf("Section group content %02d.", i),
		})
	}
	replacement := "Updated text"
	combined := []c.TeacherHomepageFieldRule{
		{Field: c.TeacherHomepageFieldSelector{Field: "sections"}, Override: &c.TeacherHomepageFieldValue{Sections: &sections}},
		{Field: c.TeacherHomepageFieldSelector{Field: "sections", Label: "ＩＴＥＭ ０："}, Override: &c.TeacherHomepageFieldValue{Text: &replacement}},
		{Field: c.TeacherHomepageFieldSelector{Field: "sections", Label: "Added section"}, Override: &c.TeacherHomepageFieldValue{Text: &replacement}},
	}
	assertRejected("group-plus-new-section", combined)
	if count := homepageAuditCount(t, migration, "catalog.teacher-sites.homepage-controls-updated.v1", homepageAdmin); count != 0 {
		t.Fatalf("rejected controls were audited as a successful write: %d", count)
	}
	combined = append(combined, c.TeacherHomepageFieldRule{
		Field: c.TeacherHomepageFieldSelector{Field: "sections", Label: "Item 1"}, Hidden: true,
	})
	homepageSet(t, h, 0, "visible", homepageRulesJSON(t, combined), "group-with-a-hidden-item")
	profile := homepagePublicProfile(t, h, homepagePublic)
	if profile == nil || len(profile.Sections) != 64 || profile.Sections[0].Text != replacement ||
		profile.Sections[63].Title != "Added section" {
		t.Fatalf("manual slots did not count in-range replacements and hidden items correctly: %+v", profile)
	}

	// A full manual array still has room for compatible input from the source
	// only to the extent that source items do not displace manual ones.
	homepageSet(t, h, 1, "visible", homepageRulesJSON(t, homepageTextRules("fields", 128)), "fields-at-limit")
	profile = homepagePublicProfile(t, h, homepagePublic)
	if profile == nil || len(profile.Fields) != 128 || profile.Fields[127].Label != "Item 127" {
		t.Fatalf("published fields lost an accepted manual item: %+v", profile)
	}
}

func TestTeacherHomepageRequestsPostgresRejectsApprovalBeyondOverrideCapacity(t *testing.T) {
	s, _, migration, _ := homepageFixture(t)
	s.TeacherHomepageOwnership = &homepageOwnershipFixture{}
	h := homepageHandler(s)
	revision := homepageSet(t, h, 0, "visible", homepageRulesJSON(t, homepageTextRules("sections", 64)), "fill-section-slots")
	request := homepageAssertPending(t, homepageCall(t, h, http.MethodPost, homepageOwnRequests, homepageOwner, "extra-section",
		`{"kind":"supplement","field":{"field":"sections","label":"Additional section"},"value":{"text":"New section"},"message":"Please add this research description"}`, http.StatusOK))
	path := homepageAdminRequests + "/" + request.ID
	body := fmt.Sprintf(`{"action":"approve","expected_revision":1,"expected_homepage_revision":%d,"note":"Accepted source information"}`, revision)
	response := homepageCall(t, h, http.MethodPut, path+"/decision", homepageAdmin, "approve-section", body, http.StatusConflict)
	if code := homepageDecode[map[string]string](t, response)["code"]; code != "teacher_homepage_capacity_exceeded" {
		t.Fatalf("expected the capacity code, got %q", code)
	}
	homepageAssertPending(t, homepageCall(t, h, http.MethodGet, path, homepageAdmin, "", "", http.StatusOK))
	receipt := homepageDecode[c.TeacherHomepageControlsView](t, homepageCall(t, h, http.MethodGet, homepageControls, homepageAdmin, "", "", http.StatusOK))
	if receipt.Controls.Revision != revision || receipt.Controls.Independent ||
		homepageAuditCount(t, migration, homepageResolveAction, homepageAdmin) != 0 {
		t.Fatal("an unsuccessful approval changed the control revision, publication status or resolution audit")
	}
	revision = homepageSet(t, h, revision, "visible", homepageRulesJSON(t, homepageTextRules("sections", 63)), "free-section-slot")
	body = fmt.Sprintf(`{"action":"approve","expected_revision":1,"expected_homepage_revision":%d,"note":"Accepted source information"}`, revision)
	approved := homepageDecode[homepageRequestReceipt](t, homepageCall(t, h, http.MethodPut, path+"/decision", homepageAdmin, "approve-section", body, http.StatusOK))
	profile := homepagePublicProfile(t, h, homepagePublic)
	if approved.Status != "approved" || profile == nil || len(profile.Sections) != 64 ||
		profile.Sections[63].Title != "Additional section" {
		t.Fatalf("approval after clearing a slot did not publish the requested section: %+v", profile)
	}
}
