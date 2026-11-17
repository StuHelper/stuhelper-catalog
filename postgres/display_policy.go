package postgres

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"slices"
	"strings"
	"time"

	c "github.com/StuHelper/stuhelper-catalog"
)

//go:embed sports_build.sql
var sportsBuildSQL string

func normalizedNames(values []string) []string {
	out := make([]string, len(values))
	for i, v := range values {
		out[i] = c.PolicyName(v)
	}
	return out
}

func (t *transaction) applyDisplayPolicy(school string, report *c.ProjectionReport) error {
	version, p := report.Version, report.Policy
	if len(p.ExcludedCourseNames)+len(p.ExcludedCategoryNames) > 0 {
		if err := t.tx.QueryRowContext(t.ctx, `WITH excluded AS MATERIALIZED (
 SELECT co.id FROM catalog.projection_objects co WHERE co.version=$1 AND co.kind='course' AND
 (lower(btrim(co.name))=ANY($2::text[]) OR EXISTS(
 SELECT 1 FROM catalog.projection_objects v JOIN catalog.projection_objects ca ON ca.version=v.version AND ca.id=v.category_id AND ca.kind='category'
 WHERE v.version=co.version AND (v.id=co.id OR v.course_id=co.id) AND lower(btrim(ca.name))=ANY($3::text[])))
 ), removed AS (DELETE FROM catalog.projection_objects o WHERE o.version=$1 AND (o.id IN(SELECT id FROM excluded) OR o.course_id IN(SELECT id FROM excluded)) RETURNING kind)
 SELECT count(*) FILTER(WHERE kind='course') FROM removed`, version, normalizedNames(p.ExcludedCourseNames), normalizedNames(p.ExcludedCategoryNames)).Scan(&report.ExcludedCourses); err != nil {
			return safe(err)
		}
	}
	if len(p.CategoryAliases) > 0 || p.Sports != nil {
		if err := t.applyCategoryAliases(school, report); err != nil {
			return err
		}
	}
	if p.Sports == nil {
		return nil
	}
	return t.buildSportsProjects(school, report)
}

func (t *transaction) ensureDisplayCategory(school, name string, report *c.ProjectionReport, categories map[string]c.Object) (c.Object, error) {
	if object, ok := categories[c.PolicyName(name)]; ok {
		return object, nil
	}
	sum := sha256.Sum256([]byte(school + "\x00" + c.PolicyName(name)))
	object := c.Object{ID: "display:category:" + hex.EncodeToString(sum[:16]), Kind: "category", SchoolID: school, Name: name, Aliases: []string{}, Revision: report.Version, UpdatedAt: report.BuiltAt}
	raw, err := json.Marshal(object)
	if err != nil {
		return object, c.ErrInput
	}
	_, err = t.tx.ExecContext(t.ctx, `INSERT INTO catalog.projection_objects(version,id,school_id,kind,name,data) VALUES($1,$2,$3,'category',$4,$5::jsonb)`, report.Version, object.ID, school, name, string(raw))
	if err != nil {
		return object, safe(err)
	}
	categories[c.PolicyName(name)] = object
	return object, nil
}

func (t *transaction) applyCategoryAliases(school string, report *c.ProjectionReport) error {
	objects, err := list[c.Object](t, `SELECT data FROM catalog.projection_objects WHERE version=$1 AND kind='category' ORDER BY id LIMIT 1001`, report.Version)
	if err != nil {
		return err
	}
	if len(objects) > 1000 {
		return c.ErrUnavailable
	}
	byName := map[string]c.Object{}
	for _, object := range objects {
		key := c.PolicyName(object.Name)
		if _, ok := byName[key]; !ok {
			byName[key] = object
		}
	}
	aliases := map[string]string{}
	for _, rule := range report.Policy.CategoryAliases {
		target, err := t.ensureDisplayCategory(school, rule.To, report, byName)
		if err != nil {
			return err
		}
		for _, object := range objects {
			if (c.PolicyName(object.Name) == c.PolicyName(rule.From) || c.PolicyName(object.Name) == c.PolicyName(rule.To)) && object.ID != target.ID {
				aliases[object.ID] = target.ID
			}
		}
	}
	if report.Policy.Sports != nil {
		if _, err := t.ensureDisplayCategory(school, report.Policy.Sports.CategoryName, report, byName); err != nil {
			return err
		}
	}
	keys := make([]string, 0, len(aliases))
	for from := range aliases {
		keys = append(keys, from)
	}
	slices.Sort(keys)
	for _, from := range keys {
		to := aliases[from]
		if _, err = t.tx.ExecContext(t.ctx, `UPDATE catalog.projection_objects SET category_id=$3,data=data||jsonb_build_object('category_id',$3::text) WHERE version=$1 AND category_id=$2`, report.Version, from, to); err != nil {
			return safe(err)
		}
		if _, err = t.tx.ExecContext(t.ctx, `INSERT INTO catalog.projection_aliases(version,alias_id,canonical_id) VALUES($1,$2,$3)`, report.Version, from, to); err != nil {
			return safe(err)
		}
	}
	return nil
}

type sportsCode struct {
	Code string `json:"code"`
	ID   string `json:"id"`
	Name string `json:"name"`
}

func (t *transaction) buildSportsProjects(school string, report *c.ProjectionReport) error {
	policy := report.Policy.Sports
	codes := []sportsCode{}
	for _, project := range policy.Projects {
		for _, code := range project.SourceCodes {
			codes = append(codes, sportsCode{code, c.SportsProjectID(school, project.Key), project.Name})
		}
	}
	raw, err := json.Marshal(codes)
	if err != nil {
		return c.ErrInput
	}
	var category string
	if err = t.tx.QueryRowContext(t.ctx, `SELECT id FROM catalog.projection_objects WHERE version=$1 AND kind='category' AND lower(btrim(name))=$2 ORDER BY id LIMIT 1`, report.Version, c.PolicyName(policy.CategoryName)).Scan(&category); err != nil {
		return safe(err)
	}
	for index, step := range strings.Split(sportsBuildSQL, "-- step\n") {
		started := time.Now()
		step = strings.TrimSpace(step)
		prefix := `WITH args AS(SELECT $1::bigint AS version,$2::text AS school,$3::text AS category,$4::text[] AS course_names,$5::jsonb AS codes) `
		if strings.HasPrefix(step, "WITH ") {
			step = "," + strings.TrimPrefix(step, "WITH ")
		}
		if _, err = t.tx.ExecContext(t.ctx, prefix+step, report.Version, school, category, normalizedNames(policy.CourseNames), string(raw)); err != nil {
			slog.ErrorContext(t.ctx, "catalog sports projection failed", "step", index+1, "reason", projectionErrorCode(err))
			return safe(err)
		}
		if elapsed := time.Since(started); elapsed > 300*time.Millisecond {
			slog.InfoContext(t.ctx, "catalog sports projection stage", "step", index+1, "duration_ms", elapsed.Milliseconds())
		}
	}
	if err = t.tx.QueryRowContext(t.ctx, `SELECT
 (SELECT count(*) FROM catalog.projection_sports_sources WHERE version=$1),
 (SELECT count(*) FROM catalog.projection_objects WHERE version=$1 AND kind='course' AND data->>'course_type'='sports_project'),
 (SELECT count(*) FROM catalog.projection_sports_classes WHERE version=$1),
 (SELECT count(*) FROM catalog.projection_sports_classes WHERE version=$1 AND reason<>'')`, report.Version).Scan(&report.SportsSourceCourses, &report.SportsProjects, &report.SportsClasses, &report.UnresolvedSportsClasses); err != nil {
		return safe(err)
	}
	reasons, err := read[map[string]int64](t, `SELECT COALESCE(jsonb_object_agg(reason,n),'{}'::jsonb) FROM(SELECT reason,count(*) n FROM catalog.projection_sports_classes WHERE version=$1 AND reason<>'' GROUP BY reason) x`, report.Version)
	report.SportsUnresolvedReasons = reasons
	return err
}
