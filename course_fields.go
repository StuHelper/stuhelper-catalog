package catalog

import (
	"math"
	"strings"
)

func validCourseFields(o Object) bool {
	if o.Kind != "course" {
		return o.EnglishName == "" && len(o.FormerCodes) == 0 && len(o.FormerNames) == 0 && o.Hours == nil
	}
	if !validText(o.EnglishName, 256) || o.EnglishName != strings.TrimSpace(o.EnglishName) {
		return false
	}
	if o.Hours != nil && (*o.Hours < 0 || *o.Hours > 10000 || math.IsNaN(*o.Hours) || math.IsInf(*o.Hours, 0)) {
		return false
	}
	for _, group := range []struct {
		values []string
		max    int
	}{{o.FormerCodes, 128}, {o.FormerNames, 256}} {
		if len(group.values) > 50 {
			return false
		}
		seen := map[string]bool{}
		for _, value := range group.values {
			if value == "" || value != strings.TrimSpace(value) || !validText(value, group.max) || seen[value] {
				return false
			}
			seen[value] = true
		}
	}
	return true
}
