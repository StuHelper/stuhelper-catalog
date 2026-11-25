package catalog

import (
	"strings"
	"testing"
)

func TestTeacherHomepageInlineContactMarker(t *testing.T) {
	const marker = "\uE000"
	p := newHomepagePresentation(TeacherHomepageControls{PageVisibility: TeacherHomepageVisible}, TeacherHomepageAllowlist{Fields: defaultTeacherHomepageAllowlist()})
	src := TeacherHomepage{Name: "合" + marker + "成", Email: "test@example.org", Phone: "010-55551111", OfficeLocation: "样例楼", Biography: "作者…" + marker + "写信给 a@example.org 或 b@example.org。\n联系方式：独立值", Fields: []TeacherHomepageField{{"其他联系方式", "多个值"}}, Sections: []TeacherHomepageSection{{"其他联系方式", "多行\n说明"}}}
	reveal := p.profile(&src, "test")
	if reveal == nil || reveal.Name != "合成" || reveal.Biography != strings.ReplaceAll(src.Biography, marker, "") || reveal.Phone != src.Phone || reveal.Email != src.Email || reveal.OfficeLocation != src.OfficeLocation {
		t.Fatal("incorrect reveal", reveal)
	}
	view := p.profile(&src, "test")
	if !maskHomepageContacts(view) || view.Email != marker || view.Phone != marker || view.OfficeLocation != marker || view.Biography != "作者…写信给 "+marker+" 或 "+marker+"。\n联系方式："+marker || view.Fields[0].Value != marker || view.Sections[0].Text != marker {
		t.Fatal("incorrect initial contact locations", view)
	}
	// Unlisted prose can publish explicit email addresses only. Strip a source
	// marker before extraction as well, so a truncated address cannot escape.
	unlisted := p.profile(&TeacherHomepage{Sections: []TeacherHomepageSection{{"未列栏目", "合成个人资料 lab" + marker + "group@example.org"}}}, "test")
	if unlisted == nil || len(unlisted.Sections) != 1 || unlisted.Sections[0].Text != "labgroup@example.org" {
		t.Fatal("unlisted email not normalized before extraction", unlisted)
	}
	empty := p.profile(&TeacherHomepage{Biography: "字" + marker + "面省略号…", Email: marker}, "test")
	if maskHomepageContacts(empty) || empty.Email != "" || empty.Biography != "字面省略号…" {
		t.Fatal("fabricated contact", empty)
	}
}
