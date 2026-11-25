package catalog

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"maps"
	"math"
	"regexp"
	"slices"
	"strings"
	"time"
)

type TeacherHomepageAllowlist struct {
	Revision int64                          `json:"revision"`
	Fields   []TeacherHomepageFieldSelector `json:"fields"`
}

type TeacherHomepageAllowlistInput struct {
	ExpectedRevision *int64                         `json:"expected_revision"`
	Fields           []TeacherHomepageFieldSelector `json:"fields"`
}

type TeacherHomepageAllowlistTransaction interface {
	TeacherHomepageAllowlist(school string) (TeacherHomepageAllowlist, error)
	SaveTeacherHomepageAllowlist(school string, list TeacherHomepageAllowlist, now time.Time) error
}

var teacherHomepageScalarFields = []string{
	"name", "position", "education", "degree", "discipline", "biography", "research", "email", "phone", "office_location", "hire_date",
}

var teacherHomepageSectionLabels = []string{
	"其他联系方式", "联系方式", "Other contact information", "Contact details", "个人简介", "个人介绍", "简介", "个人概况", "Biography", "Introduction", "Profile", "Personal profile",
	"研究方向", "研究领域", "研究兴趣", "Research interests", "Research directions", "Research field", "Research fields",
	"科研成果", "学术成果", "科研项目", "科研工作", "Research", "Research projects", "Research achievements",
	"论文", "论文成果", "代表性论文", "论著", "代表著作", "学术论文", "著作", "Publications", "Papers", "Books",
	"专利", "知识产权", "Patents", "Intellectual property",
	"教学课程", "教学工作", "课程教学", "开授课程", "本科生课程", "研究生课程", "Teaching", "Courses",
	"教学成果", "教学研究", "教学获奖", "Teaching achievements",
	"招生信息", "招生与指导", "招生与培养", "招生", "指导学生", "研究生指导", "Admissions", "Supervision",
	"获奖荣誉", "获奖信息", "获奖与荣誉", "奖励荣誉", "荣誉称号", "Awards", "Honors", "Honours",
	"社会兼职", "学术兼职", "学术活动", "Academic service",
	"教育经历", "教育背景", "工作经历", "任职经历", "学术经历", "Education", "Experience", "Work experience",
}

var teacherHomepageExtraLabels = []string{
	"其他联系方式", "个人联系方式", "联系方式", "邮箱", "电子邮箱", "电子邮件", "电话", "办公电话", "联系电话", "手机", "办公地点", "办公地址", "E-mail", "Email", "Phone", "Other contact information", "Contact details",
	"研究方向", "研究领域", "研究兴趣", "Research interests", "Research directions",
	"社会兼职", "学术兼职", "学术活动", "Academic service",
	"教育经历", "教育背景", "工作经历", "任职经历", "学术经历", "Education", "Experience", "Work experience",
}

func defaultTeacherHomepageAllowlist() []TeacherHomepageFieldSelector {
	fields := make([]TeacherHomepageFieldSelector, 0, len(teacherHomepageScalarFields)+len(teacherHomepageSectionLabels)+len(teacherHomepageExtraLabels)+2)
	for _, field := range teacherHomepageScalarFields {
		fields = append(fields, TeacherHomepageFieldSelector{Field: field})
	}
	fields = append(fields, TeacherHomepageFieldSelector{Field: "supervisors"}, TeacherHomepageFieldSelector{Field: "photo"})
	for _, label := range teacherHomepageSectionLabels {
		fields = append(fields, TeacherHomepageFieldSelector{Field: "sections", Label: label})
	}
	for _, label := range teacherHomepageExtraLabels {
		fields = append(fields, TeacherHomepageFieldSelector{Field: "fields", Label: label})
	}
	// These are website facts, kept independently of the directory's authoritative unit and title.
	for _, labels := range []map[string]bool{homepageUnitLabels, homepageTitleLabels, personalHomepageLabels} {
		for _, label := range slices.Sorted(maps.Keys(labels)) {
			fields = append(fields, TeacherHomepageFieldSelector{Field: "fields", Label: label}, TeacherHomepageFieldSelector{Field: "sections", Label: label})
		}
	}
	return fields
}

var teacherHomepageRecordLabels = map[string]bool{
	"官网": true, "官网主页": true, "官方网站": true, "原始主页": true, "official website": true,
	"更新时间": true, "更新日期": true, "最后更新时间": true, "最后更新": true, "上次更新": true, "最近更新": true,
	"页面更新时间": true, "最后修改时间": true, "updated on": true, "last updated": true, "last update": true,
	"last update time": true, "last modified": true, "last modification": true,
}

var originalTeacherHomepageURL = regexp.MustCompile(`(?i)(?:(?:https?:)?//shi\.buaa\.edu\.cn(?::(?:80|443))?|^|[[:space:]])/[^/\s<>"']+/(?:zh_CN|en)/index\.htm\b`)

func validTeacherHomepageLabel(label string) bool {
	key := teacherHomepageLabelKey(label)
	return validText(label, 160) && key != "" && !teacherHomepageRecordLabels[key] && !originalTeacherHomepageURL.MatchString(label)
}

func normalizeTeacherHomepageAllowlist(fields []TeacherHomepageFieldSelector) ([]TeacherHomepageFieldSelector, error) {
	if fields == nil || len(fields) > 256 {
		return nil, ErrInput
	}
	result := make([]TeacherHomepageFieldSelector, 0, len(fields))
	seen := make(map[TeacherHomepageFieldSelector]bool, len(fields))
	for _, field := range fields {
		kind, _ := teacherHomepageValueType(field)
		key := teacherHomepageSelectorKey(field)
		dynamic := key.Field == "fields" || key.Field == "sections"
		if kind == "" || dynamic && !validTeacherHomepageLabel(field.Label) || !dynamic && field.Label != "" || seen[key] {
			return nil, ErrInput
		}
		seen[key] = true
		field.Label = strings.TrimSpace(field.Label)
		result = append(result, field)
	}
	return result, nil
}

func loadTeacherHomepageAllowlist(t Transaction, school string) (TeacherHomepageAllowlist, error) {
	store, ok := t.(TeacherHomepageAllowlistTransaction)
	if !ok {
		return TeacherHomepageAllowlist{}, ErrUnavailable
	}
	list, err := store.TeacherHomepageAllowlist(school)
	if err != nil {
		return list, err
	}
	return list.Effective(), nil
}

// Effective resolves a school's unconfigured policy for all homepage readers.
func (list TeacherHomepageAllowlist) Effective() TeacherHomepageAllowlist {
	if list.Revision == 0 {
		list.Fields = defaultTeacherHomepageAllowlist()
	}
	return list
}

func (s *Service) ReadTeacherHomepageAllowlist(ctx context.Context, p Principal, school string) (TeacherHomepageAllowlist, error) {
	var result TeacherHomepageAllowlist
	const action = "homepage-allowlist-viewed"
	if err := s.teacherRefreshAccess(ctx, p, school, action, p.CanMaintain); err != nil {
		return result, err
	}
	err := s.Repo.Transaction(ctx, func(t Transaction) error {
		var err error
		result, err = loadTeacherHomepageAllowlist(t, school)
		if err != nil {
			return err
		}
		return s.teacherRefreshAudit(t, p, school, action, "school/"+school, "success")
	})
	return result, err
}

func (s *Service) SetTeacherHomepageAllowlist(ctx context.Context, p Principal, school, key string, in TeacherHomepageAllowlistInput) (TeacherHomepageAllowlist, error) {
	var result TeacherHomepageAllowlist
	if err := s.teacherRefreshAccess(ctx, p, school, "homepage-allowlist-updated", p.CanMaintain); err != nil {
		return result, err
	}
	if !ValidKey(key) || in.ExpectedRevision == nil || *in.ExpectedRevision < 0 {
		return result, ErrInput
	}
	fields, err := normalizeTeacherHomepageAllowlist(in.Fields)
	if err != nil {
		return result, err
	}
	replayKey := "teacher-homepage-allowlist:" + digest([]string{p.Subject, school, key})
	fingerprint := digest(in)
	err = s.Repo.Transaction(ctx, func(t Transaction) error {
		if err := t.Lock(school); err != nil {
			return err
		}
		cached, err := t.Replay(replayKey)
		if err == nil {
			if cached.Digest != fingerprint {
				return ErrConflict
			}
			return json.Unmarshal(cached.Result, &result)
		}
		if !errors.Is(err, ErrNotFound) {
			return err
		}
		store, ok := t.(TeacherHomepageAllowlistTransaction)
		audit, err := refreshPort(t)
		if !ok || err != nil {
			return ErrUnavailable
		}
		current, err := loadTeacherHomepageAllowlist(t, school)
		if err != nil {
			return err
		}
		if current.Revision != *in.ExpectedRevision || current.Revision == math.MaxInt64 {
			return ErrConflict
		}
		result = TeacherHomepageAllowlist{Revision: current.Revision + 1, Fields: fields}
		now := s.now()
		if err := store.SaveTeacherHomepageAllowlist(school, result, now); err != nil {
			return err
		}
		changes := homepageAllowlistAuditChanges(school, current.Fields, result.Fields)
		if err := audit.RecordTeacherRefreshAudit(Event{
			ID: "homepage-allowlist:" + rand.Text(), Subject: "school/" + school, Type: teacherHomepageAllowlistAudit,
			SchoolID: school, Actor: p.Subject, Result: "success", Revision: result.Revision, Time: now,
			Reason: changes.reason(homepageAllowlistAuditReasons), HomepageChanges: &changes,
		}); err != nil {
			return err
		}
		data, err := json.Marshal(result)
		if err != nil {
			return ErrUnavailable
		}
		return t.SaveReplay(replayKey, Replay{Digest: fingerprint, Result: data})
	})
	return result, err
}
