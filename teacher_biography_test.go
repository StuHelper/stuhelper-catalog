package catalog

import (
	"strings"
	"testing"
)

func TestBiographySampleNarratives(t *testing.T) {
	// Synthetic counterparts of the ten empty and five shortened production biographies.
	samples := []struct{ site, label, value, bio string }{
		{"1062", "毕业院校", "甲大学", "甲大学教学奖获得者。\n1993 年毕业于甲大学。\n曾任甲大学实验室主任。"},
		{"1304", "毕业院校", "甲大学", "甲大学教授，兼任学会委员。"},
		{"1677", "毕业院校", "甲大学", "获甲大学博士，主持医疗机器人项目。"},
		{"1973", "毕业院校", "乙大学", "乙大学博士，海外访问学者，研究交通系统。"},
		{"2531", "毕业院校", "甲大学", "甲大学研究员、实验室副主任。"},
		{"3151", "毕业院校", "甲大学", "甲大学副教授，承担三十余项课题。"},
		{"4121", "毕业院校", "甲大学", "在甲大学获博士学位，后加入实验室。"},
		{"10440", "教师拼音名称", "linlei", "曾获甲大学资助，个人学术记录见 linlei 的论文。"},
		{"12811", "毕业院校", "甲大学", "甲大学助理教授，已获授权两项发明专利。"},
		{"14614", "毕业院校", "甲大学", "甲大学博士，后从事超声制造研究。"},
		{"1087", "教师拼音名称", "Li Ming", "研究兴趣\nLi Ming, Chen et al. Icing experiments."},
		{"2163", "毕业院校", "乙大学", "乙大学博士，主持交通调控项目。\n研究成果"},
		{"3770", "教师英文名称", "Ming Li", "学术论文\nMing Li et al. Synthetic segmentation study."},
		{"12148", "教师拼音名称", "liming", "在甲大学攻读材料学。\n学术邮箱 liming@example.org"},
		{"14829", "主页资料", "张甲", "张甲曾参与发动机研制。\n主持两项科研项目。"},
	}
	p := newHomepagePresentation(TeacherHomepageControls{PageVisibility: TeacherHomepageVisible}, TeacherHomepageAllowlist{Fields: defaultTeacherHomepageAllowlist()})
	for _, s := range samples {
		t.Run(s.site, func(t *testing.T) {
			source := TeacherHomepage{Name: "合成教师", Biography: s.bio, Fields: []TeacherHomepageField{{Label: s.label, Value: s.value}}}
			if s.site == "14829" {
				source.Fields = nil
				source.Sections = []TeacherHomepageSection{{Title: s.label, Text: s.value}}
			}
			got := p.profile(&source, "teacher")
			if got == nil || got.Biography != s.bio {
				t.Error("lost academic narrative")
			}
		})
	}
}

func TestBiographyOnlyDeduplicatesPublishedLines(t *testing.T) {
	source := TeacherHomepage{Name: "测试教师", Degree: "博士", Biography: "学位：博士\n院校经历：博士\n合成论文全文\n仍须显示的内容\n教学：相同值", Sections: []TeacherHomepageSection{{Title: "论文", Text: "合成论文全文\n另一条论文"}, {Title: "任意未发布栏目", Text: "仍须显示的内容"}, {Title: "研究方向", Text: "相同值"}}}
	p := newHomepagePresentation(TeacherHomepageControls{PageVisibility: TeacherHomepageVisible}, TeacherHomepageAllowlist{Fields: defaultTeacherHomepageAllowlist()})
	out := p.profile(&source, "teacher")
	if out == nil || out.Biography != "院校经历：博士\n仍须显示的内容\n教学：相同值" || len(out.Sections) != 2 {
		t.Fatal(out)
	}
}

func TestBiographyKeepsPrivacyAndDirectoryRules(t *testing.T) {
	source := TeacherHomepage{Name: "测试教师", Email: "test@example.org", Biography: "普通论文记录\n校外联系 test@example.org\n隐藏标记出现在履历中\n职称：教授", Sections: []TeacherHomepageSection{{Title: "私有栏目", Text: "隐藏标记"}, {Title: "论文", Text: "此文含隐藏标记"}}}
	p := newHomepagePresentation(TeacherHomepageControls{PageVisibility: TeacherHomepageVisible, Rules: []TeacherHomepageFieldRule{{Field: TeacherHomepageFieldSelector{Field: "email"}, Hidden: true}, {Field: TeacherHomepageFieldSelector{Field: "sections", Label: "私有栏目"}, Hidden: true}}}, TeacherHomepageAllowlist{Fields: defaultTeacherHomepageAllowlist()})
	out := p.profile(&source, "teacher")
	if out == nil || strings.Contains(out.Biography, "隐藏") || out.Biography != "普通论文记录" || len(out.Sections) != 0 {
		t.Fatal(out)
	}
}
