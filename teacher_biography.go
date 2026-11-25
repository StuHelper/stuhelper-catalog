package catalog

import "strings"

func contactLabel(label string) bool {
	switch teacherHomepageAliases[label] {
	case "email", "phone", "office_location":
		return true
	}
	for _, word := range []string{"联系", "电话", "邮箱", "地址", "办公地点", "contact", "phone", "email", "e-mail", "address", "office location"} {
		if strings.Contains(label, word) {
			return true
		}
	}
	return false
}

// Compare the biography with the final, filtered fields. A source-only section
// or a line removed by privacy rules cannot serve as the surviving copy.
func (r homepageRedaction) deduplicateVisibleBiography(profile *TeacherHomepage) string {
	// Existing opening text has priority over a collapsible section. Verify a
	// surviving copy only for lines that the former policy would have removed.
	candidates := map[string]bool{}
	for line := range strings.SplitSeq(profile.Biography, "\n") {
		key := teacherHomepageLabelKey(line)
		if key != "" && r.contains(key, false) {
			candidates[key] = true
		}
	}
	if len(candidates) == 0 {
		return profile.Biography
	}
	lines := map[string]bool{}
	type pair struct{ label, value string }
	labelled := map[pair]bool{}
	add := func(label, value string) {
		if value == "" {
			return
		}
		label = teacherHomepageLabelKey(label)
		if alias := teacherHomepageAliases[label]; alias != "" {
			label = alias
		}
		labelled[pair{label, teacherHomepageLabelKey(value)}] = true
		for line := range strings.SplitSeq(value, "\n") {
			if key := teacherHomepageLabelKey(line); key != "" {
				lines[key] = true
			}
		}
	}
	for _, field := range teacherHomepageScalarFields {
		if field != "biography" {
			add(field, *homepageScalar(profile, field))
		}
	}
	for _, field := range profile.Fields {
		add(field.Label, field.Value)
	}
	for _, section := range profile.Sections {
		add(section.Title, section.Text)
	}
	var keep []string
	for line := range strings.SplitSeq(profile.Biography, "\n") {
		key := teacherHomepageLabelKey(line)
		if !candidates[key] {
			keep = append(keep, line)
			continue
		}
		if lines[key] {
			continue
		}
		if m := homepageLabelPrefix.FindStringSubmatchIndex(line); m != nil {
			label := teacherHomepageLabelKey(line[m[2]:m[3]])
			if alias := teacherHomepageAliases[label]; alias != "" {
				label = alias
			}
			if labelled[pair{label, teacherHomepageLabelKey(line[m[1]:])}] {
				continue
			}
		}
		keep = append(keep, line)
	}
	return strings.TrimSpace(strings.Join(keep, "\n"))
}
