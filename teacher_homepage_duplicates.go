package catalog

import (
	"strings"

	"golang.org/x/text/unicode/norm"
)

// Only biography, research and named sections can host media in the public API.
type homepageMediaPlacements map[TeacherHomepageFieldSelector]TeacherHomepageFieldSelector

func comparableHomepageText(value string) string {
	return strings.Join(strings.Fields(norm.NFKC.String(value)), " ")
}

func homepageLineSet(text string) map[string]bool {
	lines := make(map[string]bool)
	for line := range strings.SplitSeq(text, "\n") {
		if key := comparableHomepageText(line); key != "" {
			lines[key] = true
		}
	}
	return lines
}

func remainingHomepageSection(text string, scalarLines map[string]bool) string {
	lines := strings.Split(text, "\n")
	kept := lines[:0]
	for _, line := range lines {
		if !scalarLines[comparableHomepageText(line)] {
			kept = append(kept, line)
		}
	}
	if len(kept) == len(lines) {
		return text
	}
	remaining := strings.Join(kept, "\n")
	if strings.TrimSpace(remaining) == "" {
		return ""
	}
	return remaining
}

func (moves homepageMediaPlacements) moveSection(title, alias string) {
	if moves != nil && (alias == "biography" || alias == "research") {
		moves[TeacherHomepageFieldSelector{Field: "sections", Label: title}] = TeacherHomepageFieldSelector{Field: alias}
	}
}

func (p homepagePresentation) deduplicateSections(profile *TeacherHomepage, items []homepageTextItem, placements homepageMediaPlacements) {
	// Compare every chunk to the same scalar. Clear it only after all sections
	// have been considered, so the result does not depend on their order.
	kept := profile.Sections[:0]
	containing := map[string]string{}
	partial := make(map[int]string)
	for i, item := range items {
		section := profile.Sections[i]
		alias := teacherHomepageAliases[teacherHomepageLabelKey(section.Title)]
		if !item.overridden && alias != "" && alias != "supervisors" && p.rules[TeacherHomepageFieldSelector{Field: alias}].Override == nil {
			scalar := comparableHomepageText(*homepageScalar(profile, alias))
			body := comparableHomepageText(section.Text)
			if scalar != "" && body != "" {
				if strings.Contains(scalar, body) {
					placements.moveSection(section.Title, alias)
					continue
				}
				if !strings.Contains(body, scalar) {
					partial[len(kept)] = alias
				} else if containing[alias] == "" {
					containing[alias] = section.Title
				}
			}
		}
		kept = append(kept, section)
	}
	for alias, title := range containing {
		*homepageScalar(profile, alias) = ""
		if placements != nil {
			placements[TeacherHomepageFieldSelector{Field: alias}] = TeacherHomepageFieldSelector{Field: "sections", Label: title}
		}
	}
	// A scalar displaced by another section cannot supply a visible copy of
	// an individual line. Compare partial sections only to surviving scalars.
	profile.Sections = kept[:0]
	lineSets := make(map[string]map[string]bool)
	for i, section := range kept {
		if alias := partial[i]; alias != "" && containing[alias] == "" {
			if lineSets[alias] == nil {
				lineSets[alias] = homepageLineSet(*homepageScalar(profile, alias))
			}
			section.Text = remainingHomepageSection(section.Text, lineSets[alias])
			if section.Text == "" {
				placements.moveSection(section.Title, alias)
				continue
			}
		}
		profile.Sections = append(profile.Sections, section)
	}
	// A title can identify several chunks. Media belongs to a surviving chunk
	// with that title before it can move to another part of the page.
	for _, s := range profile.Sections {
		delete(placements, TeacherHomepageFieldSelector{Field: "sections", Label: s.Title})
	}
}

func (moves homepageMediaPlacements) place(item *TeacherHomepageMedium) {
	if item.Field == "sections" {
		if dest, ok := moves[TeacherHomepageFieldSelector{Field: "sections", Label: item.Section}]; ok {
			item.Field, item.Section = dest.Field, dest.Label
		}
	}
	if item.Field != "sections" {
		if dest, ok := moves[TeacherHomepageFieldSelector{Field: item.Field}]; ok {
			item.Field, item.Section = dest.Field, dest.Label
		}
	}
}
