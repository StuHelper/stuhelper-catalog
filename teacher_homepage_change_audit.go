package catalog

import (
	"cmp"
	"reflect"
	"slices"
	"strings"
)

const (
	teacherHomepageControlsAudit  = "catalog.teacher-sites.homepage-controls-updated.v1"
	teacherHomepageAllowlistAudit = "catalog.teacher-sites.homepage-allowlist-updated.v1"
)

type TeacherHomepageAuditChanges struct {
	Page   *TeacherHomepageAuditPageChange   `json:"page,omitempty"`
	Fields []TeacherHomepageAuditFieldChange `json:"fields,omitempty"`
}

type TeacherHomepageAuditPageChange struct {
	From string `json:"from"`
	To   string `json:"to"`
}

type TeacherHomepageAuditFieldChange struct {
	Field       string   `json:"field"`
	LabelDigest string   `json:"label_digest,omitempty"`
	Changes     []string `json:"changes"`
}

var homepageControlsAuditReasons = []string{
	"override_set", "override_cleared", "field_hidden", "field_restored",
	"page_hidden", "page_restored", "page_stopped", "page_resumed", "no_change",
}
var homepageAllowlistAuditReasons = []string{"field_allowed", "field_disallowed", "no_change"}

// ValidTeacherHomepageAuditReason allows safe operation codes in the Admin
// audit projection. Detailed selector hashes remain in the private audit row.
func ValidTeacherHomepageAuditReason(action, reason string) bool {
	var allowed []string
	switch action {
	case teacherHomepageControlsAudit:
		allowed = homepageControlsAuditReasons
	case teacherHomepageAllowlistAudit:
		allowed = homepageAllowlistAuditReasons
	default:
		return false
	}
	if len(reason) == 0 || len(reason) > 128 {
		return false
	}
	last := -1
	for part := range strings.SplitSeq(reason, ",") {
		index := slices.Index(allowed, part)
		if index <= last {
			return false
		}
		last = index
	}
	return true
}

func (changes TeacherHomepageAuditChanges) reason(operations []string) string {
	seen := make(map[string]bool, len(operations))
	if page := changes.Page; page != nil {
		if page.From == TeacherHomepageStopped {
			seen["page_resumed"] = true
		}
		switch page.To {
		case TeacherHomepageHidden:
			seen["page_hidden"] = true
		case TeacherHomepageStopped:
			seen["page_stopped"] = true
		case TeacherHomepageVisible:
			if page.From == TeacherHomepageHidden {
				seen["page_restored"] = true
			}
		}
	}
	for _, field := range changes.Fields {
		for _, operation := range field.Changes {
			seen[operation] = true
		}
	}
	if len(seen) == 0 {
		return "no_change"
	}
	names := []string{}
	for _, operation := range operations {
		if seen[operation] {
			names = append(names, operation)
		}
	}
	return strings.Join(names, ",")
}

func homepageControlsAuditChanges(school, teacher string, before, after TeacherHomepageControls) TeacherHomepageAuditChanges {
	var out TeacherHomepageAuditChanges
	if before.PageVisibility != after.PageVisibility {
		out.Page = &TeacherHomepageAuditPageChange{From: before.PageVisibility, To: after.PageVisibility}
	}
	type change struct {
		before, after TeacherHomepageFieldRule
	}
	bySelector := make(map[TeacherHomepageFieldSelector]change, len(before.Rules)+len(after.Rules))
	for _, rule := range before.Rules {
		bySelector[teacherHomepageSelectorKey(rule.Field)] = change{before: rule}
	}
	for _, rule := range after.Rules {
		key := teacherHomepageSelectorKey(rule.Field)
		d := bySelector[key]
		d.after = rule
		bySelector[key] = d
	}
	for selector, d := range bySelector {
		var operations []string
		if !reflect.DeepEqual(d.before.Override, d.after.Override) {
			if d.after.Override == nil {
				operations = append(operations, "override_cleared")
			} else {
				operations = append(operations, "override_set")
			}
		}
		if d.before.Hidden != d.after.Hidden {
			if d.after.Hidden {
				operations = append(operations, "field_hidden")
			} else {
				operations = append(operations, "field_restored")
			}
		}
		if len(operations) > 0 {
			out.Fields = append(out.Fields, homepageAuditField(school, teacher, selector, operations))
		}
	}
	sortHomepageAuditFields(out.Fields)
	return out
}

func homepageAllowlistAuditChanges(school string, before, after []TeacherHomepageFieldSelector) TeacherHomepageAuditChanges {
	type presence struct {
		before, after bool
	}
	bySelector := make(map[TeacherHomepageFieldSelector]presence, len(before)+len(after))
	for _, field := range before {
		bySelector[teacherHomepageSelectorKey(field)] = presence{before: true}
	}
	for _, field := range after {
		key := teacherHomepageSelectorKey(field)
		d := bySelector[key]
		d.after = true
		bySelector[key] = d
	}
	var out TeacherHomepageAuditChanges
	for selector, d := range bySelector {
		switch {
		case !d.before && d.after:
			out.Fields = append(out.Fields, homepageAuditField(school, "", selector, []string{"field_allowed"}))
		case d.before && !d.after:
			out.Fields = append(out.Fields, homepageAuditField(school, "", selector, []string{"field_disallowed"}))
		}
	}
	sortHomepageAuditFields(out.Fields)
	return out
}

func homepageAuditField(school, teacher string, selector TeacherHomepageFieldSelector, changes []string) TeacherHomepageAuditFieldChange {
	out := TeacherHomepageAuditFieldChange{Field: selector.Field, Changes: changes}
	if fieldType, _ := teacherHomepageValueType(selector); fieldType == "" {
		out.Field = "unknown"
	}
	if selector.Label != "" {
		out.LabelDigest = digest([]string{"teacher-homepage-audit-label-v1", school, teacher, selector.Field, selector.Label})
	}
	return out
}

func sortHomepageAuditFields(fields []TeacherHomepageAuditFieldChange) {
	slices.SortFunc(fields, func(a, b TeacherHomepageAuditFieldChange) int {
		if order := cmp.Compare(a.Field, b.Field); order != 0 {
			return order
		}
		return cmp.Compare(a.LabelDigest, b.LabelDigest)
	})
}
