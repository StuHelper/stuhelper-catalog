package catalog

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestTeacherHomepagePresentationFiltersUnlistedAndHiddenCopies(t *testing.T) {
	office := "合成实验楼302室"
	biography := "合成教学经历\n请到" + office + "领取材料"
	source := &TeacherHomepage{
		Name: "合成教师", Email: "teacher@example.com", OfficeLocation: "旧资料中的办公地点",
		Fields: []TeacherHomepageField{
			{Label: "Ｅｍａｉｌ：", Value: "teacher@example.com"},
			{Label: "个人联系方式", Value: "合成备用联络地点"},
		},
		Sections: []TeacherHomepageSection{
			{Title: "教学课程", Text: "合成课程\n联络地点为合成备用联络地点\n邮箱 teacher@example.com\n办公请到合成实验楼302室"},
			{Title: "未列入白名单", Text: "未审核资料"},
		},
	}
	presentation := newHomepagePresentation(TeacherHomepageControls{
		PageVisibility: TeacherHomepageVisible,
		Rules: []TeacherHomepageFieldRule{
			{Field: TeacherHomepageFieldSelector{Field: "email"}, Hidden: true},
			{Field: TeacherHomepageFieldSelector{Field: "office_location"}, Hidden: true, Override: &TeacherHomepageFieldValue{Text: &office}},
			{Field: TeacherHomepageFieldSelector{Field: "biography"}, Override: &TeacherHomepageFieldValue{Text: &biography}},
		},
	}, TeacherHomepageAllowlist{Fields: defaultTeacherHomepageAllowlist()})
	profile := presentation.profile(source, "teacher")
	if profile == nil || profile.Email != "" || profile.OfficeLocation != "" || len(profile.Fields) != 0 || len(profile.Sections) != 1 ||
		profile.Sections[0].Text != "合成课程" || profile.Biography != "合成教学经历" {
		t.Fatalf("unexpected published profile: %+v", profile)
	}
}

func TestTeacherHomepagePresentationAllowsExplicitOverrideAndOmittedName(t *testing.T) {
	override := "合成补充说明"
	source := &TeacherHomepage{
		Name: "合成教师",
		Sections: []TeacherHomepageSection{
			{Title: "教学课程", Text: "源页前半段"},
			{Title: "教学课程", Text: "源页后半段"},
		},
	}
	controls := TeacherHomepageControls{
		PageVisibility: TeacherHomepageVisible,
		Rules: []TeacherHomepageFieldRule{
			{Field: TeacherHomepageFieldSelector{Field: "name"}, Hidden: true},
			{Field: TeacherHomepageFieldSelector{Field: "sections", Label: "教学课程："}, Override: &TeacherHomepageFieldValue{Text: &override}},
		},
	}
	presentation := newHomepagePresentation(controls, TeacherHomepageAllowlist{})
	profile := presentation.profile(source, "teacher")
	if profile == nil || profile.Name != "" || len(profile.Sections) != 1 || profile.Sections[0].Text != override {
		t.Fatalf("unexpected published profile: %+v", profile)
	}
	body, err := json.Marshal(TeacherHomepagePublication{Profile: &TeacherHomepagePublicProfile{TeacherHomepage: profile}})
	if err != nil || strings.Contains(string(body), "name") || strings.Contains(string(body), "合成教师") {
		t.Fatalf("name disclosure in dedicated route model: %s (%v)", body, err)
	}
	if presentation.profile(nil, "teacher") != nil {
		t.Fatal("an administrator edit alone must not publish an unmatched homepage")
	}
	controls.Independent = true
	presentation = newHomepagePresentation(controls, TeacherHomepageAllowlist{})
	if profile := presentation.profile(nil, "teacher"); profile == nil || len(profile.Sections) != 1 {
		t.Fatalf("approved independent profile was withheld: %+v", profile)
	}
	controls.PageVisibility = TeacherHomepageStopped
	presentation = newHomepagePresentation(controls, TeacherHomepageAllowlist{})
	if profile := presentation.profile(nil, "teacher"); profile != nil {
		t.Fatal("stopped page published a profile")
	}
}

func TestTeacherHomepagePresentationRetainsAcademicNumbersAndApprovedText(t *testing.T) {
	published := "合成材料"
	source := &TeacherHomepage{
		Name: "测试合成姓名", HireDate: "2020", Phone: "010-87654321、010-87654320", OfficeLocation: "主楼101室",
		Fields: []TeacherHomepageField{
			{Label: "未获白名单授权的字段", Value: published},
			{Label: "办公室", Value: "次楼202室"},
		},
		Sections: []TeacherHomepageSection{{
			Title: "论文", Text: "2020年发表合成论文\n专利号1234567\n联系电话：010(87654321)\n010(87654320)\n请前往次楼202室",
		}},
	}
	controls := TeacherHomepageControls{
		PageVisibility: TeacherHomepageVisible,
		Rules: []TeacherHomepageFieldRule{
			{Field: TeacherHomepageFieldSelector{Field: "hire_date"}, Hidden: true},
			{Field: TeacherHomepageFieldSelector{Field: "phone"}, Hidden: true},
			{Field: TeacherHomepageFieldSelector{Field: "fields", Label: "批准的资料"}, Override: &TeacherHomepageFieldValue{Text: &published}},
		},
	}
	p := newHomepagePresentation(controls, TeacherHomepageAllowlist{Fields: defaultTeacherHomepageAllowlist()})
	profile := p.profile(source, "teacher")
	if profile == nil || profile.HireDate != "" || profile.Phone != "" || profile.OfficeLocation != "主楼101室" ||
		len(profile.Fields) != 1 || profile.Fields[0].Value != published ||
		len(profile.Sections) != 1 || profile.Sections[0].Text != "2020年发表合成论文\n专利号1234567" {
		t.Fatalf("unexpected published profile: %+v", profile)
	}
}
