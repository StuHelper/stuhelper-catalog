package catalog

import "strings"

const homepageContactMarker = "\uE000"

func stripHomepageContactMarker(value string) string {
	return strings.ReplaceAll(value, homepageContactMarker, "")
}

// The default HTTP projection marks structured contacts and masks email addresses
// in free text. Only the interactive read returns the current permitted originals.
func maskHomepageContacts(profile *TeacherHomepage) bool {
	if profile == nil {
		return false
	}
	masked := false
	mask := func(value string) string {
		// Preserve punctuation and academic prose around each address.
		result := homepageEmail.ReplaceAllString(value, homepageContactMarker)
		if result != value {
			masked = true
		}
		lines := strings.Split(result, "\n")
		for i, line := range lines {
			label := homepageLabelPrefix.FindStringSubmatch(line)
			if label != nil && contactLabel(teacherHomepageLabelKey(label[1])) && strings.TrimSpace(line[len(label[0]):]) != "" {
				lines[i] = label[0] + homepageContactMarker
				masked = true
			}
		}
		return strings.Join(lines, "\n")
	}
	for _, field := range teacherHomepageScalarFields {
		s := homepageScalar(profile, field)
		if field == "email" || field == "phone" || field == "office_location" {
			if *s != "" {
				masked = true
				*s = homepageContactMarker
			}
		} else {
			*s = mask(*s)
		}
	}
	for i := range profile.Fields {
		f := &profile.Fields[i]
		if contactLabel(teacherHomepageLabelKey(f.Label)) {
			masked = true
			f.Value = homepageContactMarker
		} else {
			f.Value = mask(f.Value)
		}
		f.Label = mask(f.Label)
	}
	for i := range profile.Sections {
		s := &profile.Sections[i]
		if contactLabel(teacherHomepageLabelKey(s.Title)) {
			masked = true
			s.Text = homepageContactMarker
		} else {
			s.Text = mask(s.Text)
		}
		s.Title = mask(s.Title)
	}
	return masked
}
