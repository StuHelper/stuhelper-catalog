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

func (p homepagePresentation) deduplicateSections(profile *TeacherHomepage, items []homepageTextItem, placements homepageMediaPlacements) {
	// Compare every chunk to the same scalar. Clear it only after all sections
	// have been considered, so the result does not depend on their order.
	kept := profile.Sections[:0]
	containing := map[string]string{}
	for i, item := range items {
		section := profile.Sections[i]
		alias := teacherHomepageAliases[teacherHomepageLabelKey(section.Title)]
		if !item.overridden && alias != "" && alias != "supervisors" && p.rules[TeacherHomepageFieldSelector{Field: alias}].Override == nil {
			scalar := comparableHomepageText(*homepageScalar(profile, alias))
			body := comparableHomepageText(section.Text)
			if scalar != "" && body != "" {
				if strings.Contains(scalar, body) {
					if placements != nil && (alias == "biography" || alias == "research") {
						placements[TeacherHomepageFieldSelector{Field: "sections", Label: section.Title}] = TeacherHomepageFieldSelector{Field: alias}
					}
					continue
				}
				if strings.Contains(body, scalar) && containing[alias] == "" {
					containing[alias] = section.Title
				}
			}
		}
		kept = append(kept, section)
	}
	profile.Sections = kept
	for alias, title := range containing {
		*homepageScalar(profile, alias) = ""
		if placements != nil {
			placements[TeacherHomepageFieldSelector{Field: alias}] = TeacherHomepageFieldSelector{Field: "sections", Label: title}
		}
	}
	// A title can identify several chunks. Media belongs to a surviving chunk
	// with that title before it can move to another part of the page.
	for _, s := range kept {
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
