package catalog

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

func TestTeacherHomepageAuditRecordsChangesWithoutProfileData(t *testing.T) {
	email := "sensitive-audit-fixture@example.com"
	label := "sensitive-contact-selector"
	body := "synthetic-private-override"
	before := TeacherHomepageControls{
		PageVisibility: TeacherHomepageHidden,
		Rules: []TeacherHomepageFieldRule{
			{Field: TeacherHomepageFieldSelector{Field: "email"}, Override: &TeacherHomepageFieldValue{Text: &email}},
			{Field: TeacherHomepageFieldSelector{Field: "photo"}, Hidden: true},
		},
	}
	after := TeacherHomepageControls{
		PageVisibility: TeacherHomepageVisible,
		Rules: []TeacherHomepageFieldRule{
			{Field: TeacherHomepageFieldSelector{Field: "fields", Label: label}, Hidden: true, Override: &TeacherHomepageFieldValue{Text: &body}},
		},
	}
	changes := homepageControlsAuditChanges("school", "teacher", before, after)
	reason := changes.reason(homepageControlsAuditReasons)
	want := "override_set,override_cleared,field_hidden,field_restored,page_restored"
	if reason != want || !ValidTeacherHomepageAuditReason(teacherHomepageControlsAudit, reason) {
		t.Fatalf("audit reason %q, want %q", reason, want)
	}
	raw, err := json.Marshal(changes)
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{email, label, body} {
		if strings.Contains(string(raw), private) {
			t.Fatal("private homepage data entered audit metadata")
		}
	}
	if !slices.ContainsFunc(changes.Fields, func(change TeacherHomepageAuditFieldChange) bool {
		return change.Field == "fields" && validTeacherHomepageHash(change.LabelDigest) &&
			slices.Equal(change.Changes, []string{"override_set", "field_hidden"})
	}) {
		t.Fatalf("missing selector-level audit changes: %s", raw)
	}
}

func TestTeacherHomepageAuditDistinguishesPageAndAllowlistChanges(t *testing.T) {
	cases := []struct{ from, to, reason string }{
		{TeacherHomepageVisible, TeacherHomepageHidden, "page_hidden"},
		{TeacherHomepageHidden, TeacherHomepageVisible, "page_restored"},
		{TeacherHomepageHidden, TeacherHomepageStopped, "page_stopped"},
		{TeacherHomepageStopped, TeacherHomepageVisible, "page_resumed"},
		{TeacherHomepageStopped, TeacherHomepageHidden, "page_hidden,page_resumed"},
	}
	for _, tt := range cases {
		from, to := TeacherHomepageControls{PageVisibility: tt.from}, TeacherHomepageControls{PageVisibility: tt.to}
		changes := homepageControlsAuditChanges("school", "teacher", from, to)
		if changes.Page == nil || changes.Page.From != tt.from || changes.Page.To != tt.to ||
			changes.reason(homepageControlsAuditReasons) != tt.reason {
			t.Fatalf("unexpected transition audit for %s to %s: %+v", tt.from, tt.to, changes)
		}
	}
	before := []TeacherHomepageFieldSelector{{Field: "phone"}, {Field: "sections", Label: "Teaching"}}
	after := []TeacherHomepageFieldSelector{{Field: "photo"}, {Field: "sections", Label: "ＴＥＡＣＨＩＮＧ："}}
	changes := homepageAllowlistAuditChanges("school", before, after)
	if len(changes.Fields) != 2 || changes.reason(homepageAllowlistAuditReasons) != "field_allowed,field_disallowed" {
		t.Fatalf("allowlist audit included unchanged labels or missed membership changes: %+v", changes)
	}
	for _, reason := range []string{"sensitive-contents", "field_hidden,field_hidden", "field_hidden,override_set", "field_hidden\nother"} {
		if ValidTeacherHomepageAuditReason(teacherHomepageControlsAudit, reason) {
			t.Fatalf("noncanonical audit reason accepted: %q", reason)
		}
	}
	if ValidTeacherHomepageAuditReason("catalog.another-action.v1", "page_stopped") {
		t.Fatal("homepage reason escaped its audit action")
	}
}
