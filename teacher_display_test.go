package catalog

import (
	"strings"
	"testing"
)

// Source-shaped fixtures retain the original site numbers and contain synthetic contacts and URLs.
func TestTeacherDisplayTenSites(t *testing.T) {
	const institute = "航空科学与工程学院"
	const chip = "集成电路科学与工程学院"
	cases := []struct{ site, department, title, source, want string }{
		{"1180", institute, "", "单位：北京航空航天大学航空科学与工程学院飞机系", "单位：北京航空航天大学航空科学与工程学院飞机系"},
		{"1189", institute, "", "所在单位：航空科学与工程学院 · 飞机系，低空技术与工程专业责任教授。", "所在单位：航空科学与工程学院 · 飞机系，低空技术与工程专业责任教授。"},
		{"1611", "", "", "主页：http://example.org/", "主页：http://example.org/"},
		{"1885", "", "", "主页：https://example.org/site/lab", "主页：https://example.org/site/lab"},
		{"2670", "能源与动力工程学院", "", "所在单位：空间与环境学院 环境科学与工程系", "所在单位：空间与地球科学学院 环境科学与工程系"},
		{"2684", "", "", "主页：https://lab.example.org", "主页：https://lab.example.org"},
		{"10104", chip, "", "所在单位：北京航空航天大学集成电路科学与工程学院", "所在单位：北京航空航天大学集成电路科学与工程学院"},
		{"10595", "", "教授", "职称：教授", ""},
		{"11995", "", "", "邮箱：faculty@example.org", "邮箱：faculty@example.org"},
		{"13361", chip, "", "所在单位：集成电路科学与工程学院", ""},
	}
	for _, tc := range cases {
		t.Run(tc.site, func(t *testing.T) {
			p := newHomepagePresentation(TeacherHomepageControls{PageVisibility: TeacherHomepageVisible}, TeacherHomepageAllowlist{Fields: defaultTeacherHomepageAllowlist()})
			// Directory metadata is set to the same values the teacher page presents.
			p.directory = homepageDirectory{department: tc.department, title: tc.title, renames: strings.NewReplacer("空间与环境学院", "空间与地球科学学院")}
			input := TeacherHomepage{Name: "测试教师", Biography: "研究记录\n" + tc.source}
			if tc.site == "11995" {
				input.Sections = []TeacherHomepageSection{{Title: "其他联系方式", Text: "faculty@example.org"}}
			}
			got := p.profile(&input, "teacher")
			if got == nil || got.Biography != strings.TrimSpace("研究记录\n"+tc.want) {
				t.Errorf("unexpected biography for site %s", tc.site)
			}
		})
	}
}

func TestTeacherDisplayContactRevealAndLinks(t *testing.T) {
	list := TeacherHomepageAllowlist{Fields: defaultTeacherHomepageAllowlist()}
	src := TeacherHomepage{Name: "合成教师", Email: "primary@example.org", Phone: "010-55551234",
		Biography: "邮箱：other@example.org\n主页：javascript:alert(1)\n个人主页：ftp://example.org/file\n主页：https://example.org:8443/学术",
		Sections:  []TeacherHomepageSection{{Title: "其他联系方式", Text: "other@example.org\n电话：010-55551234"}}}
	p := newHomepagePresentation(TeacherHomepageControls{PageVisibility: TeacherHomepageVisible}, list)
	view := p.profile(&src, "teacher")
	if view == nil || len(view.Sections) != 1 || view.Sections[0].Text != src.Sections[0].Text || view.Biography != "邮箱：other@example.org\n主页：https://example.org:8443/学术" {
		t.Fatal("reveal must retain source contacts and only valid links")
	}
	if !maskHomepageContacts(view) || view.Email != "" || view.Phone != "" || strings.Contains(view.Biography, "@") || strings.Contains(view.Sections[0].Text, "5555") {
		t.Fatal("initial response contains a contact")
	}
	p = newHomepagePresentation(TeacherHomepageControls{PageVisibility: TeacherHomepageVisible, Rules: []TeacherHomepageFieldRule{
		{Field: TeacherHomepageFieldSelector{Field: "email"}, Hidden: true},
		{Field: TeacherHomepageFieldSelector{Field: "sections", Label: "其他联系方式"}, Hidden: true},
	}}, list)
	view = p.profile(&src, "teacher")
	if view == nil || view.Email != "" || strings.Contains(view.Biography, "@") || len(view.Sections) != 0 || view.Phone != "" {
		t.Fatal("explicit hide must apply across repeated contact values")
	}
}
