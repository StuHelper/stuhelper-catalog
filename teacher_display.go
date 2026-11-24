package catalog

import (
	_ "embed"
	"encoding/json"
	"errors"
	"net/url"
	"slices"
	"strings"
	"unicode"
)

type homepageDirectory struct {
	department, title string
	renames           *strings.Replacer
}

// Only approved former names are rewritten in source prose. The current name
// comes from the school's directory record, including for an unrelated teacher.
//
//go:embed data/teacher-homepage-department-renames.json
var departmentRenamesJSON []byte
var homepageFormerNames map[string][]string

func init() {
	if err := json.Unmarshal(departmentRenamesJSON, &homepageFormerNames); err != nil {
		panic(err)
	}
}

func teacherDisplayDirectory(t Transaction, teacher Object) (homepageDirectory, error) {
	d := homepageDirectory{title: teacher.Biography}
	if teacher.DepartmentID != "" {
		dept, err := t.PublicObject(teacher.DepartmentID)
		if err != nil && !errors.Is(err, ErrNotFound) {
			return d, err
		}
		if dept.Kind == "department" && dept.SchoolID == teacher.SchoolID {
			d.department = dept.Name
		}
	}
	var replacements []string
	for _, old := range homepageFormerNames[teacher.SchoolID] {
		rows, err := t.List(Filter{Kind: "department", SchoolID: teacher.SchoolID, Query: old, Limit: 2})
		if err != nil {
			return d, err
		}
		if len(rows) == 1 && slices.Contains(rows[0].Aliases, old) && rows[0].Name != old {
			replacements = append(replacements, old, rows[0].Name)
		}
	}
	if len(replacements) > 0 {
		d.renames = strings.NewReplacer(replacements...)
	}
	return d, nil
}

func validPersonalHomepage(value string) bool {
	raw := strings.TrimSpace(value)
	if strings.ContainsAny(raw, "\\<>\"") || strings.ContainsFunc(raw, unicode.IsSpace) {
		return false
	}
	u, err := url.Parse(raw)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Hostname() != "" && u.User == nil
}

var homepageUnitLabels = map[string]bool{
	"所在单位": true, "工作单位": true, "单位": true, "department": true, "院系": true, "所属院系": true,
	"学院": true, "学校/院系": true, "department/school": true, "faculty": true, "affiliation": true,
}
var homepageTitleLabels = map[string]bool{"职称": true, "professional title": true, "technical title": true, "professional rank": true, "academic title": true, "acad. title": true}
var personalHomepageLabels = map[string]bool{"主页": true, "个人主页": true, "个人网站": true, "website": true, "homepage": true, "personal website": true, "personal homepage": true}

func (d homepageDirectory) value(label, value string) (string, bool) {
	switch {
	case homepageUnitLabels[label]:

		return value, teacherHomepageLabelKey(value) != teacherHomepageLabelKey(d.department)
	case homepageTitleLabels[label]:
		return value, teacherHomepageLabelKey(value) != teacherHomepageLabelKey(d.title)
	case personalHomepageLabels[label]:
		return value, validPersonalHomepage(value)
	}
	return value, true
}
