package catalog

import (
	"slices"
	"testing"
)

func TestTeacherHomepageDuplicateSources(t *testing.T) {
	p := newHomepagePresentation(TeacherHomepageControls{PageVisibility: TeacherHomepageVisible}, TeacherHomepageAllowlist{Fields: defaultTeacherHomepageAllowlist()})
	for _, tc := range []struct {
		site, bio string
		fields    []TeacherHomepageField
	}{
		{"1189", "合成教师任职经历\n所在单位：示例学院飞机系", nil},
		{"1611", "主页：http://example.org/lab\n未列栏目的履历", []TeacherHomepageField{{Label: "其他源标签", Value: "未列栏目的履历"}}},
	} {
		t.Run(tc.site, func(t *testing.T) {
			src := TeacherHomepage{Biography: tc.bio, Fields: tc.fields, Sections: []TeacherHomepageSection{{"个人简介", tc.bio}, {"教学课程", "合成课程"}}}
			got := p.profile(&src, "test")
			if got == nil || got.Biography != tc.bio || !slices.Equal(got.Sections, src.Sections[1:]) {
				t.Fatalf("expected full opening and teaching section, got %+v", got)
			}
		})
	}
}

func TestTeacherHomepageSectionContainment(t *testing.T) {
	p := newHomepagePresentation(TeacherHomepageControls{PageVisibility: TeacherHomepageVisible}, TeacherHomepageAllowlist{Fields: defaultTeacherHomepageAllowlist()})
	for _, tc := range []struct {
		name, research string
		sections, keep []TeacherHomepageSection
		want           string
	}{
		{"normalized", "方向 Ａ", []TeacherHomepageSection{{"Research interests", "方向  A"}}, nil, "方向 Ａ"},
		{"longer section", "研究甲", []TeacherHomepageSection{{"研究方向", "研究甲\n项目乙"}}, []TeacherHomepageSection{{"研究方向", "研究甲\n项目乙"}}, ""},
		{"multiple chunks", "研究甲", []TeacherHomepageSection{{"研究方向", "研究甲\n项目乙"}, {"研究方向", "研究甲"}, {"研究领域", "另一课题"}}, []TeacherHomepageSection{{"研究方向", "研究甲\n项目乙"}, {"研究领域", "另一课题"}}, ""},
		{"partial", "研究甲\n课题乙", []TeacherHomepageSection{{"研究方向", "课题乙\n课题丙"}}, []TeacherHomepageSection{{"研究方向", "课题乙\n课题丙"}}, "研究甲\n课题乙"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := TeacherHomepage{Research: tc.research, Sections: tc.sections}
			got := p.profile(&src, "test")
			if got == nil || got.Research != tc.want || !slices.Equal(got.Sections, tc.keep) {
				t.Fatalf("unexpected publication %+v", got)
			}
		})
	}
	manual := "获批项目"
	for _, rule := range []TeacherHomepageFieldRule{
		{Field: TeacherHomepageFieldSelector{Field: "sections", Label: "研究方向"}, Override: &TeacherHomepageFieldValue{Text: &manual}},
		{Field: TeacherHomepageFieldSelector{Field: "sections"}, Override: &TeacherHomepageFieldValue{Sections: &[]TeacherHomepageSection{{"研究方向", manual}}}},
	} {
		p := newHomepagePresentation(TeacherHomepageControls{PageVisibility: TeacherHomepageVisible, Rules: []TeacherHomepageFieldRule{rule}}, TeacherHomepageAllowlist{Fields: defaultTeacherHomepageAllowlist()})
		got := p.profile(&TeacherHomepage{Research: manual, Sections: []TeacherHomepageSection{{"研究方向", "原始栏目"}}}, "test")
		if got.Research != manual || !slices.Equal(got.Sections, []TeacherHomepageSection{{"研究方向", manual}}) {
			t.Fatal("approved section changed", got)
		}
	}
}
