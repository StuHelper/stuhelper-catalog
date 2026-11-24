package catalog

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func TestTeacherHomepageRequestValidatesProposal(t *testing.T) {
	text := "新的办公地点"
	fields := []TeacherHomepageField{{Label: "荣誉", Value: "优秀指导教师"}}
	reserved := []TeacherHomepageField{{Label: "职称", Value: "外部职称"}}
	tests := []struct {
		name  string
		in    TeacherHomepageRequestInput
		valid bool
	}{
		{"text", TeacherHomepageRequestInput{Kind: "correction", Field: TeacherHomepageFieldSelector{Field: "office_location"}, Value: &TeacherHomepageFieldValue{Text: &text}, Message: "说明"}, true},
		{"new labels", TeacherHomepageRequestInput{Kind: "supplement", Field: TeacherHomepageFieldSelector{Field: "fields"}, Value: &TeacherHomepageFieldValue{Fields: &fields}, Message: "说明"}, true},
		{"hide page", TeacherHomepageRequestInput{Kind: "hide", Field: TeacherHomepageFieldSelector{Field: "page"}, Message: "说明"}, true},
		{"hide photo", TeacherHomepageRequestInput{Kind: "hide", Field: TeacherHomepageFieldSelector{Field: "photo"}, Message: "说明"}, true},
		{"required message", TeacherHomepageRequestInput{Kind: "hide", Field: TeacherHomepageFieldSelector{Field: "photo"}, Message: " "}, false},
		{"required proposed value", TeacherHomepageRequestInput{Kind: "correction", Field: TeacherHomepageFieldSelector{Field: "email"}, Message: "说明"}, false},
		{"incompatible slot", TeacherHomepageRequestInput{Kind: "supplement", Field: TeacherHomepageFieldSelector{Field: "fields"}, Value: &TeacherHomepageFieldValue{Text: &text}, Message: "说明"}, false},
		{"page cannot be overridden", TeacherHomepageRequestInput{Kind: "correction", Field: TeacherHomepageFieldSelector{Field: "page"}, Value: &TeacherHomepageFieldValue{Text: &text}, Message: "说明"}, false},
		{"photo hide has no replacement", TeacherHomepageRequestInput{Kind: "hide", Field: TeacherHomepageFieldSelector{Field: "photo"}, Value: &TeacherHomepageFieldValue{Text: &text}, Message: "说明"}, false},
		{"directory title is not a homepage field", TeacherHomepageRequestInput{Kind: "correction", Field: TeacherHomepageFieldSelector{Field: "title"}, Value: &TeacherHomepageFieldValue{Text: &text}, Message: "说明"}, false},
		{"source title can be supplemented separately from the directory title", TeacherHomepageRequestInput{Kind: "supplement", Field: TeacherHomepageFieldSelector{Field: "fields"}, Value: &TeacherHomepageFieldValue{Fields: &reserved}, Message: "说明"}, true},
		{"untrusted photo reference", TeacherHomepageRequestInput{Kind: "correction", Field: TeacherHomepageFieldSelector{Field: "photo"}, Value: &TeacherHomepageFieldValue{Photo: &TeacherHomepageFieldPhoto{ID: "foreign"}}, Message: "说明"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := normalizeTeacherHomepageRequest(tt.in)
			if tt.valid && err != nil || !tt.valid && !errors.Is(err, ErrInput) {
				t.Fatalf("normalize proposal: %v", err)
			}
		})
	}
}

func TestTeacherHomepageRequestApprovalPreservesHideAndOverride(t *testing.T) {
	before := "前一获批值"
	after := "新获批值"
	label := TeacherHomepageFieldSelector{Field: "fields", Label: "专业："}
	other := TeacherHomepageFieldRule{Field: TeacherHomepageFieldSelector{Field: "email"}, Hidden: true}
	rules := []TeacherHomepageFieldRule{{Field: label, Override: &TeacherHomepageFieldValue{Text: &before}}, other}
	normalized := TeacherHomepageFieldSelector{Field: "fields", Label: "专业"}
	rules = applyTeacherHomepageRequestField(rules, TeacherHomepageRequestInput{Kind: "hide", Field: normalized})
	if len(rules) != 2 || !rules[0].Hidden || rules[0].Override.Text == nil || *rules[0].Override.Text != before || rules[1] != other {
		t.Fatalf("hide lost existing rules: %#v", rules)
	}
	rules = applyTeacherHomepageRequestField(rules, TeacherHomepageRequestInput{Kind: "correction", Field: normalized, Value: &TeacherHomepageFieldValue{Text: &after}})
	if len(rules) != 2 || !rules[0].Hidden || rules[0].Override.Text == nil || *rules[0].Override.Text != after || rules[1] != other {
		t.Fatalf("override bypassed hide or changed another rule: %#v", rules)
	}
}

func TestTeacherHomepageRequestIndependentPublicationRequiresContent(t *testing.T) {
	emptyFields := []TeacherHomepageField{}
	fields := []TeacherHomepageField{{Label: "研究主题", Value: "合成资料"}}
	if teacherHomepageRequestAddsContent(nil) ||
		teacherHomepageRequestAddsContent(&TeacherHomepageFieldValue{Fields: &emptyFields}) ||
		!teacherHomepageRequestAddsContent(&TeacherHomepageFieldValue{Fields: &fields}) {
		t.Fatal("independent publication must require an approved non-empty value")
	}
}

func TestTeacherHomepageRequestCursorBindsAccountSchoolAndStatus(t *testing.T) {
	q := TeacherHomepageRequestQuery{SchoolID: "school-one", TeacherID: "teacher-one", Submitter: "account-one", Limit: 1}
	_, scope, err := prepareTeacherHomepageRequestQuery(q, "account-one", "all")
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, time.November, 22, 3, 8, 7, 321000, time.UTC)
	raw, err := json.Marshal(teacherHomepageRequestCursor{Scope: scope, At: at, ID: "cursor-id"})
	if err != nil {
		t.Fatal(err)
	}
	q.After = base64.RawURLEncoding.EncodeToString(raw)
	next, _, err := prepareTeacherHomepageRequestQuery(q, "account-one", "all")
	if err != nil || next.After != "cursor-id" || !next.AfterCreatedAt.Equal(at) {
		t.Fatalf("decode cursor: %#v, %v", next, err)
	}
	if _, _, err := prepareTeacherHomepageRequestQuery(q, "account-two", "all"); !errors.Is(err, ErrInput) {
		t.Fatalf("copied cursor: %v", err)
	}
	if _, _, err := prepareTeacherHomepageRequestQuery(q, "account-one", "pending"); !errors.Is(err, ErrInput) {
		t.Fatalf("filtered cursor: %v", err)
	}
	q.SchoolID = "school-two"
	if _, _, err := prepareTeacherHomepageRequestQuery(q, "account-one", "all"); !errors.Is(err, ErrInput) {
		t.Fatalf("cross-school cursor: %v", err)
	}
}
