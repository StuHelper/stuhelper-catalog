package catalog

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
)

// Aliases affect a published directory generation, never the school's records.
type CategoryAlias struct {
	From string `json:"from"`
	To   string `json:"to"`
}
type SportsProject struct {
	Key         string   `json:"key"`
	Name        string   `json:"name"`
	SourceCodes []string `json:"source_codes"`
}
type SportsDirectoryPolicy struct {
	CategoryName string          `json:"category_name"`
	CourseNames  []string        `json:"course_names,omitempty"`
	Projects     []SportsProject `json:"projects"`
}

func PolicyName(value string) string { return strings.ToLower(strings.TrimSpace(value)) }
func policyNames(names []string, limit int) bool {
	if len(names) > limit {
		return false
	}
	seen := map[string]bool{}
	for _, name := range names {
		key := PolicyName(name)
		if key == "" || !validText(name, 256) || name != strings.TrimSpace(name) || seen[key] {
			return false
		}
		seen[key] = true
	}
	return true
}
func (p DirectoryPolicy) Valid() bool {
	if TermOrderKey(p.CutoffYear, p.CutoffSeason) == 0 || !policyNames(p.ExcludedCourseNames, 256) || !policyNames(p.ExcludedCategoryNames, 256) || len(p.CategoryAliases) > 256 {
		return false
	}
	aliases := map[string]string{}
	for _, a := range p.CategoryAliases {
		from, to := PolicyName(a.From), PolicyName(a.To)
		if !policyNames([]string{a.From, a.To}, 2) || aliases[from] != "" {
			return false
		}
		aliases[from] = to
	}
	for _, to := range aliases {
		if aliases[to] != "" {
			return false
		}
	}
	if p.Sports == nil {
		return true
	}
	v := p.Sports
	if !policyNames([]string{v.CategoryName}, 1) || !policyNames(v.CourseNames, 256) || len(v.Projects) > 256 {
		return false
	}
	for _, n := range p.ExcludedCategoryNames {
		if PolicyName(n) == PolicyName(v.CategoryName) {
			return false
		}
	}
	keys, names, codes := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, project := range v.Projects {
		name := PolicyName(project.Name)
		if !ValidID(project.Key) || !policyNames([]string{project.Name}, 1) || keys[project.Key] || names[name] || len(project.SourceCodes) == 0 || len(project.SourceCodes) > 256 {
			return false
		}
		keys[project.Key], names[name] = true, true
		for _, code := range project.SourceCodes {
			if code == "" || code != strings.TrimSpace(code) || !validText(code, 64) || codes[code] {
				return false
			}
			codes[code] = true
		}
	}
	return len(codes) <= 2048
}
func (p DirectoryPolicy) Equal(other DirectoryPolicy) bool {
	left, le := json.Marshal(p)
	right, re := json.Marshal(other)
	return le == nil && re == nil && string(left) == string(right)
}

// Project keys are configuration identities. Renaming a project preserves its
// target ID; regrouping source codes is an explicit policy change.
func SportsProjectID(school, key string) string {
	sum := sha256.Sum256([]byte(school + "\x00" + key))
	return "sports:course:" + hex.EncodeToString(sum[:16])
}
func SportsOfferingID(project, term string) string {
	sum := sha256.Sum256([]byte(project + "\x00" + term))
	return "sports:offering:" + hex.EncodeToString(sum[:16])
}
